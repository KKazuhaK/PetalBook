package api_test

import (
	"strings"
	"testing"

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
		// A person already in, a new one (any case), another tenant's, nobody, the same
		// person twice, and no name at all.
		r := f.admA.do("POST", base, body(true, rs)).want(t, 200, "")
		if got := statuses(r); got != "already_member,ok,other_tenant,not_found,duplicate,invalid" {
			t.Fatal(got, r.raw)
		}
		if n := rowCount(t, a, `SELECT COUNT(*) FROM space_members WHERE space_id = ?`, f.spaceA); n != 2 {
			t.Fatalf("a dry run wrote: %d members", n)
		}
		if got := statuses(f.admA.do("POST", base, body(true, bad)).want(t, 200, "")); got != "invalid" {
			t.Fatal(got)
		}
		r = f.admA.do("POST", base, body(false, rs)).want(t, 200, "")
		if got := statuses(r); got != "already_member,ok,other_tenant,not_found,duplicate,invalid" {
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
		items := f.root.do("GET", "/api/admin/audit?kind=members_batch", nil).want(t, 200, "").body["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("%d batch entries", len(items))
		}
		d := items[0].(map[string]any)["detail"].(map[string]any)
		if d["rows"] != float64(6) || len(d["results"].([]any)) != 6 || d["counts"].(map[string]any)["ok"] != float64(1) {
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
		if got := statuses(r); got != "already_member,ok,other_tenant,not_found,forbidden" {
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
		if n := len(f.root.do("GET", "/api/admin/audit?kind=group_members_batch", nil).want(t, 200, "").body["items"].([]any)); n != 1 {
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
		if n := len(f.root.do("GET", "/api/admin/audit?kind=invites_batch", nil).want(t, 200, "").body["items"].([]any)); n != 1 {
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
