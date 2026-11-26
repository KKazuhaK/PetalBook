package api_test

import (
	"strings"
	"testing"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/store"
)

// statuses are the statuses of a batch answer's rows, in order.
func statuses(r result) string {
	var out []string
	for _, x := range r.body["results"].([]any) {
		out = append(out, x.(map[string]any)["status"].(string))
	}
	return strings.Join(out, ",")
}

func batchRows(users ...string) []map[string]any {
	out := make([]map[string]any, len(users))
	for i, u := range users {
		out[i] = map[string]any{"user": u, "role": "editor"}
	}
	return out
}

// The picker's search shows the people of the caller's own tenant, with an id,
// a username and a name and nothing more; another tenant's people are not
// there, and it is limited in rate.
func TestPeopleSearchIsTenantScopedLimitedAndRateLimited(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		r := f.admB.do("GET", "/api/people?q=member", nil).want(t, 200, "")
		l := r.body["people"].([]any)
		if len(l) != 1 || l[0].(map[string]any)["username"] != "mem-b" || len(l[0].(map[string]any)) != 3 {
			t.Fatal(r.raw)
		}
		// Tenant A's people are not tenant B's to find, by name, username or address.
		for _, q := range []string{"mem-a", "adm-a", "root"} {
			if got := f.admB.do("GET", "/api/people?q="+q, nil).want(t, 200, "").body["people"].([]any); len(got) != 0 {
				t.Fatalf("%s found %v", q, got)
			}
		}
		f.admB.do("GET", "/api/people?q=m", nil).want(t, 400, "invalid")
		f.admB.do("GET", "/api/people?q=me&limit=21", nil).want(t, 400, "invalid")
		a.newClient("").do("GET", "/api/people?q=member", nil).want(t, 401, "")
		// 20 searches in the window are answered (four are made above), the next is not.
		for i := 0; i < 16; i++ {
			f.admB.do("GET", "/api/people?q=mem", nil).want(t, 200, "")
		}
		got := f.admB.do("GET", "/api/people?q=mem", nil).want(t, 429, "rate_limited")
		if got.resp.Header.Get("Retry-After") == "" {
			t.Fatal("no Retry-After")
		}
	})
}

// A batch of people for a space says what each row would do (dry run), writes
// nothing for it, and the same rows applied add exactly those; the audit log has
// one entry for the batch with a line for each row.
func TestSpaceMembersBatchChecksThenAddsAndAudits(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		base := "/api/spaces/" + f.spaceA + "/members/batch"
		extra := a.addUser("extra-a", pw, "member")
		body := func(dry bool, rs []map[string]any) map[string]any { return map[string]any{"dryRun": dry, "rows": rs} }
		rs := batchRows("mem-a", "EXTRA-A", "mem-b", "nobody", "extra-a", "")
		rs[0]["role"] = "editor"
		bad := batchRows("extra-a")
		bad[0]["role"] = "boss"
		// A person already in, a new one (any case), another tenant's (told as nobody,
		// as the single add does), nobody, the same person twice, and no name at all.
		r := f.admA.do("POST", base, body(true, rs)).want(t, 200, "")
		if got := statuses(r); got != "already_member,ok,not_found,not_found,duplicate,invalid" {
			t.Fatal(got, r.raw)
		}
		if n := rowCount(t, a, `SELECT COUNT(*) FROM space_members WHERE space_id = ?`, f.spaceA); n != 2 {
			t.Fatalf("a dry run wrote: %d members", n)
		}
		if got := statuses(f.admA.do("POST", base, body(true, bad)).want(t, 200, "")); got != "invalid" {
			t.Fatal(got)
		}
		r = f.admA.do("POST", base, body(false, rs)).want(t, 200, "")
		if got := statuses(r); got != "already_member,ok,not_found,not_found,duplicate,invalid" {
			t.Fatal(got)
		}
		if n := rowCount(t, a, `SELECT COUNT(*) FROM space_members WHERE space_id = ?`, f.spaceA); n != 3 {
			t.Fatalf("%d members", n)
		}
		if n := rowCount(t, a, `SELECT COUNT(*) FROM space_members m JOIN users u ON u.id = m.user_id WHERE u.tenant_id <> (SELECT tenant_id FROM spaces WHERE id = m.space_id)`); n != 0 {
			t.Fatal("a person of another tenant was added")
		}
		// The new member was told.
		if got := extra.do("GET", "/api/notifications", nil).want(t, 200, "").raw; !strings.Contains(got, "Space A") {
			t.Fatal(got)
		}
		a.auth.FlushAudit(t.Context())
		// Each batch has an entry, a dry run too (it says so); newest first.
		items := f.root.do("GET", "/api/admin/audit?kind=members_batch", nil).want(t, 200, "").body["items"].([]any)
		if len(items) != 3 {
			t.Fatalf("%d batch entries", len(items))
		}
		d := items[0].(map[string]any)["detail"].(map[string]any)
		if d["rows"] != float64(6) || len(d["results"].([]any)) != 6 || d["counts"].(map[string]any)["ok"] != float64(1) || d["dryRun"] != false {
			t.Fatalf("%v", d)
		}
		if d := items[2].(map[string]any)["detail"].(map[string]any); d["dryRun"] != true {
			t.Fatalf("%v", d)
		}
		// Too many rows, none, and a viewer asking.
		f.admA.do("POST", base, body(true, batchRows())).want(t, 400, "invalid")
		f.admA.do("POST", base, body(true, batchRows(make([]string, 1001)...))).want(t, 400, "invalid")
		f.memA.do("POST", base, body(true, batchRows("extra-a"))).want(t, 403, "no_permission")
		f.admB.do("POST", base, body(true, batchRows("extra-a"))).want(t, 404, "not_found")
	})
}

// People go into a local group by file under the same rules, never another
// tenant's, and the caller's own row is left to the single add.
func TestGroupMembersBatch(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		a.addUser("extra-a", pw, "member")
		base := "/api/admin/groups/" + f.groupA + "/members/batch"
		rs := batchRows("mem-a", "extra-a", "mem-b", "nobody", "adm-a")
		r := f.admA.do("POST", base, map[string]any{"dryRun": true, "rows": rs}).want(t, 200, "")
		if got := statuses(r); got != "already_member,ok,not_found,not_found,forbidden" {
			t.Fatal(got)
		}
		if n := rowCount(t, a, `SELECT COUNT(*) FROM group_members WHERE group_id = ?`, f.groupA); n != 1 {
			t.Fatalf("a dry run wrote: %d", n)
		}
		f.admA.do("POST", base, map[string]any{"rows": rs}).want(t, 200, "")
		if n := rowCount(t, a, `SELECT COUNT(*) FROM group_members WHERE group_id = ?`, f.groupA); n != 2 {
			t.Fatalf("%d", n)
		}
		// Another tenant's administrator does not see the group at all.
		f.admB.do("POST", base, map[string]any{"rows": batchRows("mem-b")}).want(t, 404, "not_found")
		a.auth.FlushAudit(t.Context())
		if n := len(f.root.do("GET", "/api/admin/audit?kind=group_members_batch", nil).want(t, 200, "").body["items"].([]any)); n != 2 {
			t.Fatalf("%d entries", n)
		}
	})
}

// A tenant administrator's file: the tenant's own people are put into groups,
// a new address is invited with the groups it names, and where no mail is
// configured the answer carries the token to hand over, once.
func TestTenantMembersBatchInvitesNewAddressesWithoutMail(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		a.addUser("extra-a", pw, "member")
		rs := []map[string]any{
			{"user": "extra-a", "role": "member", "groups": []string{"group a"}},
			{"user": "mem-a", "role": "member"},
			{"user": "new@example.test", "role": "member", "name": "Newcomer", "groups": []string{"Group A"}},
			{"user": "NEW@example.test", "role": "member"},
			{"user": "mem-b", "role": "member"},
			{"user": "ghost", "role": "member"},
			{"user": "x@example.test", "role": "member", "groups": []string{"No such group"}},
		}
		want := "added,already_member,invited,duplicate,other_tenant,invalid,invalid"
		r := f.admA.do("POST", "/api/admin/members/batch", map[string]any{"dryRun": true, "rows": rs}).want(t, 200, "")
		if got := statuses(r); got != want {
			t.Fatal(got, r.raw)
		}
		if n := rowCount(t, a, `SELECT COUNT(*) FROM invites WHERE email = 'new@example.test'`); n != 0 {
			t.Fatal("a dry run made an invite")
		}
		r = f.admA.do("POST", "/api/admin/members/batch", map[string]any{"rows": rs}).want(t, 200, "")
		if got := statuses(r); got != want {
			t.Fatal(got)
		}
		row := r.body["results"].([]any)[2].(map[string]any)
		if row["mail"] != "none" || row["token"] == nil || r.body["mail"].(map[string]any)["configured"] != false {
			t.Fatalf("%v", row)
		}
		// An address with an open invitation is not invited (nor mailed) again.
		again := f.admA.do("POST", "/api/admin/members/batch", map[string]any{"rows": []map[string]any{{"user": "New@Example.test", "role": "member"}}}).want(t, 200, "")
		if got := statuses(again); got != "already_invited" {
			t.Fatal(got, again.raw)
		}
		if n := rowCount(t, a, `SELECT COUNT(*) FROM invites WHERE email = 'new@example.test'`); n != 1 {
			t.Fatalf("%d invites", n)
		}
		// The existing person joined the group; the invite names it and accepting it joins it.
		if n := rowCount(t, a, `SELECT COUNT(*) FROM group_members WHERE group_id = ?`, f.groupA); n != 2 {
			t.Fatalf("%d in the group", n)
		}
		c := a.newClient("")
		c.do("POST", "/api/invites/accept", map[string]any{"token": row["token"], "username": "newcomer", "password": pw}).want(t, 201, "")
		if n := rowCount(t, a, `SELECT COUNT(*) FROM group_members WHERE group_id = ?`, f.groupA); n != 3 {
			t.Fatalf("%d in the group after accepting", n)
		}
		if n := rowCount(t, a, `SELECT COUNT(*) FROM users WHERE email = 'new@example.test' AND tenant_id = ?`, store.DefaultTenantID); n != 1 {
			t.Fatal("the account is not in the tenant")
		}
		// Only an administrator, and only of the tenant.
		f.memA.do("POST", "/api/admin/members/batch", map[string]any{"rows": batchRows("a@example.test")}).want(t, 403, "forbidden")
		f.admB.do("POST", "/api/admin/members/batch", map[string]any{"tenant": store.DefaultTenantID, "rows": batchRows("b@example.test")}).want(t, 404, "not_found")
		a.auth.FlushAudit(t.Context())
		if n := len(f.root.do("GET", "/api/admin/audit?kind=invites_batch", nil).want(t, 200, "").body["items"].([]any)); n != 3 {
			t.Fatalf("%d entries", n)
		}
	})
}

// With the server's mail configured the invitations go by mail and carry no
// token; an address that has had its three messages of the hour gets the link
// to hand over instead.
func TestTenantMembersBatchMailsInvitationsWithinTheCap(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		srv := fakeMail(t)
		enableMail(t, f.root, srv)
		one := func() map[string]any {
			r := f.admA.do("POST", "/api/admin/members/batch", map[string]any{"rows": []map[string]any{{"user": "cap@example.test", "role": "member"}}}).want(t, 200, "")
			if r.body["mail"].(map[string]any)["sender"] != "site" {
				t.Fatal(r.raw)
			}
			// The invitation is withdrawn, so that the next batch invites the address again.
			if _, err := a.st.Q().Exec(t.Context(), `DELETE FROM invites WHERE email = 'cap@example.test'`); err != nil {
				t.Fatal(err)
			}
			return r.body["results"].([]any)[0].(map[string]any)
		}
		for i := 0; i < 3; i++ {
			if row := one(); row["mail"] != "sent" || row["token"] != nil {
				t.Fatalf("%d: %v", i, row)
			}
		}
		m := srv.Wait(3)[0]
		if m.To != "cap@example.test" || !strings.Contains(m.Text, "/invite#") {
			t.Fatalf("%+v", m)
		}
		if row := one(); row["mail"] != "limited" || row["token"] == nil {
			t.Fatalf("%v", row)
		}
	})
}

// A space owner who is not an administrator learns nothing of the site from a
// batch: a person of another tenant, by username or address, and a disabled
// person of the tenant are all "nobody", as with the single add. A dry run is
// audited too, and batches are limited in rows per person.
func TestSpaceMembersBatchTellsAnOwnerNobodyOutsideAndIsLimited(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		ctx := t.Context()
		if _, err := a.auth.CreateUser(ctx, auth.CreateParams{Username: "far-b", Password: pw, Role: "member", TenantID: f.tenB, Email: "far@example.test"}); err != nil {
			t.Fatal(err)
		}
		if _, err := a.auth.CreateUser(ctx, auth.CreateParams{Username: "dis-a", Password: pw, Role: "member", Email: "dis@example.test"}); err != nil {
			t.Fatal(err)
		}
		f.root.do("PATCH", "/api/users/"+mustUserID(t, a, "dis-a"), map[string]any{"disabled": true}).want(t, 200, "")
		own := f.memA.do("POST", "/api/spaces", map[string]any{"name": "Own"}).want(t, 201, "").body["id"].(string)
		base := "/api/spaces/" + own + "/members/batch"
		rs := batchRows("mem-b", "far-b", "far@example.test", "dis-a", "dis@example.test")
		r := f.memA.do("POST", base, map[string]any{"dryRun": true, "rows": rs}).want(t, 200, "")
		if got := statuses(r); got != "not_found,not_found,not_found,not_found,not_found" {
			t.Fatal(got, r.raw)
		}
		a.auth.FlushAudit(ctx)
		items := f.root.do("GET", "/api/admin/audit?kind=members_batch", nil).want(t, 200, "").body["items"].([]any)
		if len(items) != 1 || items[0].(map[string]any)["detail"].(map[string]any)["dryRun"] != true {
			t.Fatalf("%v", items)
		}
		// 5,000 rows per person in the window: five rows are spent above, four
		// files of 1,000 more fit, the fifth does not.
		full := map[string]any{"dryRun": true, "rows": batchRows(make([]string, 1000)...)}
		for i := 0; i < 4; i++ {
			f.memA.do("POST", base, full).want(t, 200, "")
		}
		got := f.memA.do("POST", base, full).want(t, 429, "rate_limited")
		if got.resp.Header.Get("Retry-After") == "" {
			t.Fatal("no Retry-After")
		}
	})
}

// Putting people into groups is managing groups: someone who may only invite
// is refused every row that names a group (an existing person's and a new
// address's alike), and the rows without one go ahead.
func TestTenantMembersBatchNeedsGroupsManageForGroups(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		inv := a.addUser("inviter", pw, "member")
		grantRole(t, a, "INVITES", store.RoleScopeTenant, store.DefaultTenantID, store.GrantToUser, mustUserID(t, a, "inviter"), "tenant.invites.manage")
		a.addUser("extra-a", pw, "member")
		rs := []map[string]any{
			{"user": "g@example.test", "groups": []string{"Group A"}},
			{"user": "extra-a", "groups": []string{"Group A"}},
			{"user": "plain@example.test"},
		}
		want := "forbidden,forbidden,invited"
		if got := statuses(inv.do("POST", "/api/admin/members/batch", map[string]any{"dryRun": true, "rows": rs}).want(t, 200, "")); got != want {
			t.Fatal(got)
		}
		if got := statuses(inv.do("POST", "/api/admin/members/batch", map[string]any{"rows": rs}).want(t, 200, "")); got != want {
			t.Fatal(got)
		}
		if n := rowCount(t, a, `SELECT COUNT(*) FROM group_members WHERE group_id = ?`, f.groupA); n != 1 {
			t.Fatalf("%d in the group", n)
		}
		if n := rowCount(t, a, `SELECT COUNT(*) FROM invites WHERE email = 'g@example.test'`); n != 0 {
			t.Fatal("an invite naming a group was made")
		}
		if n := rowCount(t, a, `SELECT COUNT(*) FROM invites WHERE email = 'plain@example.test'`); n != 1 {
			t.Fatalf("%d invites", n)
		}
	})
}

// An address an account still holds is never invited: a disabled person of
// the tenant is "disabled" (by username or address), the address of an
// account that is deleted but not yet purged is "email_taken". A role given
// for a person who is already in the tenant is not applied, and the row says
// so.
func TestTenantMembersBatchNeverInvitesAHeldAddress(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		ctx := t.Context()
		for _, p := range [][2]string{{"dis-a", "dis@example.test"}, {"gone-a", "gone@example.test"}} {
			if _, err := a.auth.CreateUser(ctx, auth.CreateParams{Username: p[0], Password: pw, Role: "member", Email: p[1]}); err != nil {
				t.Fatal(err)
			}
		}
		f.root.do("PATCH", "/api/users/"+mustUserID(t, a, "dis-a"), map[string]any{"disabled": true}).want(t, 200, "")
		if _, err := a.st.Q().Exec(ctx, `UPDATE users SET deleted_at = 1 WHERE username = 'gone-a'`); err != nil {
			t.Fatal(err)
		}
		rs := []map[string]any{
			{"user": "DIS@example.test"},
			{"user": "dis-a"},
			{"user": "gone@example.test"},
			{"user": "mem-a", "role": "admin"},
			{"user": "fresh@example.test", "role": "member"},
		}
		want := "disabled,disabled,email_taken,already_member,invited"
		for _, dry := range []bool{true, false} {
			r := f.admA.do("POST", "/api/admin/members/batch", map[string]any{"dryRun": dry, "rows": rs}).want(t, 200, "")
			if got := statuses(r); got != want {
				t.Fatal(dry, got, r.raw)
			}
			if d, _ := r.body["results"].([]any)[3].(map[string]any)["detail"].(string); !strings.Contains(d, "role") {
				t.Fatalf("no word on the role: %v", r.raw)
			}
		}
		if n := rowCount(t, a, `SELECT COUNT(*) FROM invites WHERE email IN ('dis@example.test', 'DIS@example.test', 'gone@example.test')`); n != 0 {
			t.Fatalf("%d invites to held addresses", n)
		}
		if n := rowCount(t, a, `SELECT COUNT(*) FROM users WHERE username = 'mem-a' AND role = 'member'`); n != 1 {
			t.Fatal("the role was changed")
		}
	})
}

// A member who is not an administrator finds the enabled people of their own
// tenant (id, username, name); a disabled person is not there. The search of a
// space's dialog and of a group's is in that space's or group's tenant, so a
// site administrator managing another tenant's finds its people.
func TestPeopleSearchOfAMemberAndOfWhatIsManaged(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		a.addUser("dis-a", pw, "member")
		f.root.do("PATCH", "/api/users/"+mustUserID(t, a, "dis-a"), map[string]any{"disabled": true}).want(t, 200, "")
		names := func(r result) string {
			var out []string
			for _, p := range r.body["people"].([]any) {
				out = append(out, p.(map[string]any)["username"].(string))
			}
			return strings.Join(out, ",")
		}
		if got := names(f.memA.do("GET", "/api/people?q=adm", nil).want(t, 200, "")); got != "adm-a" {
			t.Fatal(got)
		}
		if got := names(f.memA.do("GET", "/api/people?q=dis", nil).want(t, 200, "")); got != "" {
			t.Fatal(got)
		}
		// Root is of tenant A: its own search does not reach B, the space's and the group's do.
		if got := names(f.root.do("GET", "/api/people?q=mem", nil).want(t, 200, "")); got != "mem-a" {
			t.Fatal(got)
		}
		if got := names(f.root.do("GET", "/api/spaces/"+f.spaceB+"/people?q=mem", nil).want(t, 200, "")); got != "mem-b" {
			t.Fatal(got)
		}
		if got := names(f.root.do("GET", "/api/people?q=mem&group="+f.groupB, nil).want(t, 200, "")); got != "mem-b" {
			t.Fatal(got)
		}
		// Someone who may not manage the space or the group is not answered.
		f.memA.do("GET", "/api/spaces/"+f.spaceB+"/people?q=mem", nil).want(t, 404, "not_found")
		f.memA.do("GET", "/api/spaces/"+f.spaceA+"/people?q=mem", nil).want(t, 403, "no_permission")
		f.memA.do("GET", "/api/people?q=mem&group="+f.groupA, nil).want(t, 404, "not_found")
		f.admA.do("GET", "/api/people?q=mem&group="+f.groupB, nil).want(t, 404, "not_found")
	})
}
