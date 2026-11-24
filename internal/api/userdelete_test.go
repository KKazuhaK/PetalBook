package api_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/api"
	"github.com/KKazuhaK/PetalBook/internal/spaces"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// udFix is a team (newTeam: olive owns Team with edith as editor and victor as
// viewer) and the other spaces a deleted person can have.
type udFix struct {
	*team
	admin *client
	a     *acc
	// readers: olive owns it alone and only a viewer is in it (an heir is needed);
	// joint: olive and edith own it (olive just leaves); alone: nobody else is in it.
	readers, joint, alone string
}

func newUdFix(t *testing.T, a *acc) *udFix {
	t.Helper()
	admin := a.setup("kazuha", pw)
	tm := newTeam(t, a)
	f := &udFix{team: &tm, admin: admin, a: a}
	mk := func(name string) string {
		return tm.owner.do("POST", "/api/spaces", map[string]any{"name": name}).want(t, 201, "").body["id"].(string)
	}
	f.readers, f.joint, f.alone = mk("Readers"), mk("Joint"), mk("Alone")
	tm.owner.do("POST", "/api/spaces/"+f.readers+"/members", map[string]any{"username": "victor", "role": "viewer"}).want(t, 201, "")
	tm.owner.do("POST", "/api/spaces/"+f.joint+"/members", map[string]any{"username": "edith", "role": "owner"}).want(t, 201, "")
	return f
}

func ownerOf(t *testing.T, a *acc, space string) string {
	t.Helper()
	var n string
	if err := a.st.Q().QueryRow(context.Background(), `SELECT user_id FROM space_members WHERE space_id = ? AND role = 'owner' ORDER BY created, user_id LIMIT 1`, space).Scan(&n); err != nil {
		return ""
	}
	return n
}

func (f *udFix) noSpaceWithoutAnOwner(t *testing.T) {
	t.Helper()
	var n int
	if err := f.a.st.Q().QueryRow(context.Background(), `SELECT COUNT(*) FROM spaces s WHERE NOT EXISTS (SELECT 1 FROM space_members m WHERE m.space_id = s.id AND m.role = 'owner')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d spaces have no owner", n)
	}
}

func idsOf(list any, path ...string) []string {
	var out []string
	for _, x := range list.([]any) {
		v := x
		for _, p := range path {
			v = v.(map[string]any)[p]
		}
		out = append(out, v.(string))
	}
	return out
}

func TestTheDeletionOfAPersonIsPlannedByTheContractsRules(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newUdFix(t, a)
		// A remote token of olive's own syncs Team: after the transfer it stops until reconnected.
		if err := store.PutRemote(context.Background(), a.st.Q(), store.Remote{SpaceID: f.sid, URL: "https://github.com/o/team.git", Branch: "main", CredType: store.CredConnection,
			Provider: store.ProviderGitHub, GHVia: "user", GHUserID: f.ownerID, State: "ok", Updated: a.clock.Now()}); err != nil {
			t.Fatal(err)
		}
		pre := f.admin.do("GET", "/api/admin/users/"+f.ownerID+"/deletion", nil).want(t, 200, "")
		// 1 and 2: the personal space, and the spaces nobody else is in.
		rec := idsOf(pre.body["recycle"], "space", "id")
		if len(rec) != 2 || !hasID(rec, f.owner.personalSpace()) || !hasID(rec, f.alone) {
			t.Fatalf("recycle %v: %s", rec, pre.raw)
		}
		// 3: Team goes to its earliest direct editor; the viewer-only space has nobody.
		tr := pre.body["transfer"].([]any)
		if len(tr) != 1 {
			t.Fatalf("%s", pre.raw)
		}
		t0 := tr[0].(map[string]any)
		if t0["space"].(map[string]any)["id"] != f.sid || t0["to"].(map[string]any)["id"] != f.editorID || t0["why"] != "editor" || t0["credential"] != "user" {
			t.Fatalf("%v", t0)
		}
		if nh := idsOf(pre.body["needsHeir"], "id"); len(nh) != 1 || nh[0] != f.readers {
			t.Fatalf("needsHeir %v", nh)
		}
		// 4: a space with another owner is just left.
		if lv := pre.body["leave"].([]any); len(lv) != 1 || lv[0].(map[string]any)["space"].(map[string]any)["id"] != f.joint {
			t.Fatalf("leave %v", lv)
		}
		// An heir from the request fills the gap, but never beats a named editor; a
		// viewer is not made an owner on their own.
		pre2 := f.admin.do("GET", "/api/admin/users/"+f.ownerID+"/deletion?heir="+f.viewerID, nil).want(t, 200, "")
		if len(pre2.body["needsHeir"].([]any)) != 0 {
			t.Fatalf("%s", pre2.raw)
		}
		for _, x := range pre2.body["transfer"].([]any) {
			x := x.(map[string]any)
			switch x["space"].(map[string]any)["id"] {
			case f.sid:
				if x["to"].(map[string]any)["id"] != f.editorID || x["why"] != "editor" {
					t.Fatalf("%v", x)
				}
			case f.readers:
				if x["to"].(map[string]any)["id"] != f.viewerID || x["why"] != "heir" {
					t.Fatalf("%v", x)
				}
			}
		}
		if r := pre.body["revoke"].(map[string]any); r["sessions"].(float64) < 1 {
			t.Fatalf("%v", r)
		}
		// The preview reads nothing and changes nothing.
		if n := f.a.countRows(t, `SELECT COUNT(*) FROM users WHERE id = '`+f.ownerID+`' AND deleted_at IS NULL AND disabled = 0`); n != 1 {
			t.Fatal("the preview changed the person")
		}
		// An administrator of another tenant, a member, and a person who does not exist.
		f.owner.do("GET", "/api/admin/users/"+f.editorID+"/deletion", nil).want(t, 403, "forbidden")
		f.admin.do("GET", "/api/admin/users/"+fakeID+"/deletion", nil).want(t, 404, "user_not_found")
	})
}

func hasID(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func (a *acc) countRows(t *testing.T, q string) int {
	t.Helper()
	var n int
	if err := a.st.Q().QueryRow(context.Background(), q).Scan(&n); err != nil {
		t.Fatal(q, err)
	}
	return n
}

func TestDeletingAPersonNeedsAnHeirWhenNoOneCanBeChosenAndChangesNothing(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newUdFix(t, a)
		r := f.admin.do("DELETE", "/api/users/"+f.ownerID, map[string]any{}).want(t, 409, "heir_required")
		if ids := idsOf(r.body["spaces"], "id"); len(ids) != 1 || ids[0] != f.readers {
			t.Fatalf("%s", r.raw)
		}
		// Refused before anything: the person signs in and owns what they owned.
		f.owner.do("GET", "/api/spaces", nil).want(t, 200, "")
		if ownerOf(t, a, f.readers) != f.ownerID || ownerOf(t, a, f.sid) != f.ownerID {
			t.Fatal("a refused deletion transferred a space")
		}
		// A named heir for that space is enough, and beats the editor for Team.
		r = f.admin.do("DELETE", "/api/users/"+f.ownerID, map[string]any{"transfers": map[string]string{f.readers: f.viewerID, f.sid: f.outsiderID(t)}}).want(t, 200, "")
		if r.body["kind"] != "user" {
			t.Fatalf("%s", r.raw)
		}
		if ownerOf(t, a, f.readers) != f.viewerID || ownerOf(t, a, f.sid) != f.outsiderID(t) {
			t.Fatal("the named heirs did not inherit")
		}
		f.noSpaceWithoutAnOwner(t)
	})
}

func (f *udFix) outsiderID(t *testing.T) string { return mustUserID(t, f.a, "oscar") }

func TestADeletedPersonIsInvisibleDisabledFirstAndTheirSpacesAreHandled(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newUdFix(t, a)
		grp := f.admin.mkGroup(t, "Writers", "local")
		f.admin.groupAdd(t, grp, f.ownerID, f.editorID).want(t, 200, "")
		_, secret := mkToken(t, f.owner, "write", nil, f.sid)
		tok := a.bearer(secret, "")
		tok.do("GET", "/api/spaces", nil).want(t, 200, "")
		usersBefore := store.Usage{}
		if u, err := store.GetUsage(context.Background(), a.st.Q(), store.UsageTenant, store.DefaultTenantID); err == nil {
			usersBefore = u
		}
		personal := f.owner.personalSpace()

		r := f.admin.do("DELETE", "/api/users/"+f.ownerID, map[string]any{"heir": f.viewerID, "reason": "left the company"}).want(t, 200, "")
		item := r.body["id"].(string)
		if r.body["kind"] != "user" || r.body["reason"] != "left the company" || r.body["user"].(map[string]any)["username"] != "olive" {
			t.Fatalf("%s", r.raw)
		}
		with := idsOf(r.body["with"], "space", "id")
		if len(with) != 2 || !hasID(with, personal) || !hasID(with, f.alone) {
			t.Fatalf("with %v", with)
		}
		// The spaces: transferred, left, recycled; none without an owner.
		if ownerOf(t, a, f.sid) != f.editorID || ownerOf(t, a, f.readers) != f.viewerID || ownerOf(t, a, f.joint) != f.editorID {
			t.Fatal("wrong heirs")
		}
		if n := a.countRows(t, `SELECT COUNT(*) FROM space_members WHERE user_id = '`+f.ownerID+`'`); n != 0 {
			t.Fatalf("the person is still a member of %d spaces", n)
		}
		f.noSpaceWithoutAnOwner(t)
		// Their access ended at once: the session, the token.
		f.owner.do("GET", "/api/spaces", nil).want(t, 401, "unauthenticated")
		tok.do("GET", "/api/spaces", nil).want(t, 401, "")
		a.newClient("").do("POST", "/api/login", map[string]any{"username": "olive", "password": pw}).want(t, 401, "")
		// Invisible: not in the lists, the pickers, the members, the groups; their id is no one's.
		for _, p := range []string{"/api/users", "/api/users?q=olive"} {
			if strings.Contains(f.admin.do("GET", p, nil).want(t, 200, "").raw, `"olive"`) {
				t.Fatalf("%s lists the deleted person", p)
			}
		}
		if strings.Contains(f.edith().do("GET", "/api/spaces/"+f.sid+"/members", nil).want(t, 200, "").raw, "olive") {
			t.Fatal("the members list shows the deleted person")
		}
		if strings.Contains(f.admin.do("GET", "/api/admin/groups/"+grp+"/members", nil).want(t, 200, "").raw, f.ownerID) {
			t.Fatal("the group lists the deleted person")
		}
		f.admin.do("PATCH", "/api/users/"+f.ownerID, map[string]any{"disabled": false}).want(t, 404, "")
		f.admin.do("GET", "/api/admin/users/"+f.ownerID+"/deletion", nil).want(t, 404, "user_not_found")
		f.edith().do("POST", "/api/spaces/"+f.sid+"/members", map[string]any{"username": "olive", "role": "viewer"}).want(t, 404, "user_not_found")
		f.admin.do("POST", "/api/admin/groups/"+grp+"/members", map[string]any{"userIds": []string{f.ownerID}}).want(t, 404, "")
		// The username stays taken while they are in the bin.
		inv := f.admin.do("POST", "/api/invites", map[string]any{"role": "member"}).want(t, 201, "")
		a.newClient("").do("POST", "/api/invites/accept", map[string]any{"token": inv.body["token"], "username": "olive", "name": "Other Olive", "password": pw}).want(t, 409, "exists")
		// The tenant has one person fewer.
		if u, _ := store.GetUsage(context.Background(), a.st.Q(), store.UsageTenant, store.DefaultTenantID); u.People != usersBefore.People-1 {
			t.Fatalf("people %d, were %d", u.People, usersBefore.People)
		}
		// Audited, with where each space went.
		a.auth.FlushAudit(context.Background())
		rows := f.admin.audit(t, "user_deleted")
		if len(rows) != 1 {
			t.Fatalf("%v", rows)
		}
		d := rows[0]["detail"].(map[string]any)
		if d["item"] != item || d["username"] != "olive" || d["reason"] != "left the company" || len(d["transfers"].([]any)) != 2 || len(d["recycled"].([]any)) != 2 {
			t.Fatalf("%v", d)
		}
		// Only their item and the audit log name them: the recycle list shows it.
		l := f.admin.do("GET", "/api/admin/recycle?kind=user", nil).want(t, 200, "").body["items"].([]any)
		if len(l) != 1 || l[0].(map[string]any)["id"] != item {
			t.Fatalf("%v", l)
		}
		// Tenant isolation of the plan: a tenant administrator cannot name a site administrator.
		f.admin.do("DELETE", "/api/users/"+mustUserID(t, a, "kazuha"), nil).want(t, 409, "conflict") // yourself
	})
}

func (f *udFix) edith() *client { return f.editor }

func TestAHalfDoneDeletionLeavesADisabledPersonAndCanBeAskedAgain(t *testing.T) {
	t.Parallel()
	for _, step := range []string{"delete-user:disabled", "delete-user:transferred", "delete-user:left", "delete-user:recycled"} {
		t.Run(step, func(t *testing.T) {
			t.Parallel()
			var stop atomic.Value
			stop.Store("")
			opts := accOpts{spaces: func(o *spaces.Options) {
				o.RecycleHook = func(s string) error {
					if v, _ := stop.Load().(string); v == s {
						return errors.New("crash at " + s)
					}
					return nil
				}
			}}
			eachAccounts(t, opts, func(t *testing.T, a *acc) {
				f := newUdFix(t, a)
				stop.Store(step)
				f.admin.do("DELETE", "/api/users/"+f.ownerID, map[string]any{"heir": f.viewerID}).want(t, 500, "")
				stop.Store("")
				// Disabled, not deleted; fewer spaces, never one without an owner.
				if n := a.countRows(t, `SELECT COUNT(*) FROM users WHERE id = '`+f.ownerID+`' AND deleted_at IS NULL AND disabled = 1`); n != 1 {
					t.Fatalf("the person after a crash at %s", step)
				}
				f.noSpaceWithoutAnOwner(t)
				f.owner.do("GET", "/api/spaces", nil).want(t, 401, "unauthenticated")
				// Asked again, it goes on from where it stopped, and the item has everything.
				r := f.admin.do("DELETE", "/api/users/"+f.ownerID, map[string]any{"heir": f.viewerID}).want(t, 200, "")
				with := idsOf(r.body["with"], "space", "id")
				if len(with) != 2 {
					t.Fatalf("with %v: %s", with, r.raw)
				}
				f.noSpaceWithoutAnOwner(t)
				if ownerOf(t, a, f.sid) != f.editorID || ownerOf(t, a, f.readers) != f.viewerID {
					t.Fatal("the spaces were not handed on")
				}
				if n := a.countRows(t, `SELECT COUNT(*) FROM recycle_items WHERE parent LIKE 'u:%'`); n != 0 {
					t.Fatalf("%d spaces are still waiting under the marker", n)
				}
			})
		})
	}
}

func TestRestoringAPersonBringsBackWhatCanBeAndListsWhatIsNot(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newUdFix(t, a)
		grp := f.admin.mkGroup(t, "Writers", "local")
		f.admin.groupAdd(t, grp, f.ownerID).want(t, 200, "")
		_, secret := mkToken(t, f.owner, "write", nil, f.sid)
		personal := f.owner.personalSpace()
		item := f.admin.do("DELETE", "/api/users/"+f.ownerID, map[string]any{"heir": f.viewerID}).want(t, 200, "").body["id"].(string)

		r := f.admin.do("POST", "/api/admin/recycle/"+item+"/restore", map[string]any{}).want(t, 200, "")
		if r.body["restored"].(map[string]any)["user"].(map[string]any)["username"] != "olive" {
			t.Fatalf("%s", r.raw)
		}
		// Back as they were: enabled, in their group, with their own spaces.
		c := a.newClient("")
		c.do("POST", "/api/login", map[string]any{"username": "olive", "password": pw}).want(t, 200, "")
		if !hasID(idsOf(c.do("GET", "/api/spaces", nil).want(t, 200, "").body["spaces"], "id"), personal) || !hasID(idsOf(c.do("GET", "/api/spaces", nil).body["spaces"], "id"), f.alone) {
			t.Fatal("their spaces did not come back")
		}
		if !strings.Contains(f.admin.do("GET", "/api/admin/groups/"+grp+"/members", nil).raw, f.ownerID) {
			t.Fatal("their group did not come back")
		}
		// What was done is not undone: Team stays with edith, Joint with edith; the answer lists it.
		if ownerOf(t, a, f.sid) != f.editorID || ownerOf(t, a, f.readers) != f.viewerID {
			t.Fatal("a transfer was undone")
		}
		tr := idsOf(r.body["transfers"], "space")
		if len(tr) != 2 || !hasID(tr, f.sid) || !hasID(tr, f.readers) {
			t.Fatalf("transfers %v", tr)
		}
		if lv := idsOf(r.body["left"], "space"); !hasID(lv, f.joint) {
			t.Fatalf("left %v", lv)
		}
		// Revoked tokens stay revoked.
		a.bearer(secret, "").do("GET", "/api/spaces", nil).want(t, 401, "")
		f.noSpaceWithoutAnOwner(t)
		if n := a.countRows(t, `SELECT COUNT(*) FROM recycle_items`); n != 0 {
			t.Fatalf("%d items are left after the restore", n)
		}
		// Audited.
		a.auth.FlushAudit(context.Background())
		if rows := f.admin.audit(t, "user_restored"); len(rows) != 1 {
			t.Fatalf("%v", rows)
		}
		// A person who was disabled before comes back disabled.
		f.admin.do("PATCH", "/api/users/"+f.viewerID, map[string]any{"disabled": true}).want(t, 200, "")
		it2 := f.admin.do("DELETE", "/api/users/"+f.viewerID, map[string]any{"heir": f.editorID}).want(t, 200, "").body["id"].(string)
		f.admin.do("POST", "/api/admin/recycle/"+it2+"/restore", map[string]any{}).want(t, 200, "")
		if n := a.countRows(t, `SELECT COUNT(*) FROM users WHERE id = '`+f.viewerID+`' AND disabled = 1 AND deleted_at IS NULL`); n != 1 {
			t.Fatal("a person who was disabled came back enabled")
		}
	})
}

func TestPurgingAPersonDeletesTheirDataLeavesATombstoneAndRetiresTheName(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newUdFix(t, a)
		personal := f.owner.personalSpace()
		item := f.admin.do("DELETE", "/api/users/"+f.ownerID, map[string]any{"heir": f.viewerID}).want(t, 200, "").body["id"].(string)
		if _, err := a.st.Q().Exec(context.Background(), `UPDATE users SET email = 'olive@example.test', name = 'Olive Q' WHERE id = ?`, f.ownerID); err != nil {
			t.Fatal(err)
		}
		binDirs := a.dir + "/recycle"
		tok := confirmFor(t, f.admin, "purge", "items", item)
		f.admin.do("DELETE", "/api/admin/recycle/"+item+"?confirm="+tok, nil).want(t, 202, "")
		a.mgr.WaitPurges()
		// Their personal data is gone; the row stays as a tombstone.
		for _, q := range []string{
			`SELECT COUNT(*) FROM passwords WHERE user_id = '` + f.ownerID + `'`,
			`SELECT COUNT(*) FROM sessions WHERE user_id = '` + f.ownerID + `'`,
			`SELECT COUNT(*) FROM api_tokens WHERE user_id = '` + f.ownerID + `'`,
			`SELECT COUNT(*) FROM recycle_items`,
			`SELECT COUNT(*) FROM users WHERE id = '` + f.ownerID + `' AND (email <> '' OR name <> '' OR username <> 'deleted-` + f.ownerID + `')`,
			`SELECT COUNT(*) FROM spaces WHERE id IN ('` + personal + `', '` + f.alone + `')`,
		} {
			if n := a.countRows(t, q); n != 0 {
				t.Errorf("%d: %s", n, q)
			}
		}
		if n := a.countRows(t, `SELECT COUNT(*) FROM users WHERE id = '`+f.ownerID+`' AND deleted_at IS NOT NULL AND username = 'deleted-`+f.ownerID+`'`); n != 1 {
			t.Fatal("no tombstone")
		}
		if n := a.countRows(t, `SELECT COUNT(*) FROM retired_usernames WHERE name = 'olive'`); n != 1 {
			t.Fatal("the name was not recorded as retired")
		}
		if ents := listDirOrNone(binDirs); len(ents) != 0 {
			t.Fatalf("the recycle bin still holds %v", ents)
		}
		// The audit log still names them, so the row stays; with nothing naming it, it goes.
		a.auth.FlushAudit(context.Background())
		if n, err := a.mgr.SweepTombstones(context.Background()); err != nil || n != 0 {
			t.Fatalf("a tombstone the audit log names was swept: %d %v", n, err)
		}
		if _, err := a.st.Q().Exec(context.Background(), `DELETE FROM audit_log WHERE user_id = ? OR actor = ?`, f.ownerID, f.ownerID); err != nil {
			t.Fatal(err)
		}
		if _, err := a.st.Q().Exec(context.Background(), `DELETE FROM invites WHERE created_by = ?`, f.ownerID); err != nil {
			t.Fatal(err)
		}
		if n, err := a.mgr.SweepTombstones(context.Background()); err != nil || n != 1 {
			t.Fatalf("the tombstone nothing refers to was kept: %d %v", n, err)
		}
		if n := a.countRows(t, `SELECT COUNT(*) FROM users WHERE id = '`+f.ownerID+`'`); n != 0 {
			t.Fatal("the row is still there")
		}
	})
}

func listDirOrNone(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

func TestAPurgedUsernameIsFreeByDefaultAndNeverWhenTheSiteSaysSo(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newUdFix(t, a)
		purge := func(id string) {
			item := f.admin.do("DELETE", "/api/users/"+id, map[string]any{"heir": f.editorID}).want(t, 200, "").body["id"].(string)
			tok := confirmFor(t, f.admin, "purge", "items", item)
			f.admin.do("DELETE", "/api/admin/recycle/"+item+"?confirm="+tok, nil).want(t, 202, "")
			a.mgr.WaitPurges()
		}
		take := func(username string) result {
			inv := f.admin.do("POST", "/api/invites", map[string]any{"role": "member"}).want(t, 201, "")
			return a.newClient("").do("POST", "/api/invites/accept", map[string]any{"token": inv.body["token"], "username": username, "name": "New " + username, "password": pw})
		}
		// Two people are purged under the default; the setting is "after_purge".
		purge(f.viewerID)
		purge(f.ownerID)
		r := f.admin.do("GET", "/api/admin/settings", nil).want(t, 200, "")
		if !strings.Contains(r.raw, `"users.reuseUsernames"`) {
			t.Fatal("the setting is not listed")
		}
		take("victor").want(t, 201, "")
		// Site only: a tenant has no say, and the values are checked.
		f.admin.do("PUT", "/api/admin/tenants/"+store.DefaultTenantID+"/settings/users.reuseUsernames", map[string]any{"value": "never"}).want(t, 404, "")
		f.admin.do("PUT", "/api/admin/settings/users.reuseUsernames", map[string]any{"value": "sometimes"}).want(t, 400, "invalid")
		f.admin.do("PUT", "/api/admin/settings/users.reuseUsernames", map[string]any{"value": "never"}).want(t, 200, "")
		// Names purged before the switch are protected too; every purge recorded its name.
		take("olive").want(t, 409, "exists")
		take("OLIVE").want(t, 409, "exists")
		// ... and a name nobody ever had is free.
		take("fresh").want(t, 201, "")
		// Back to the default: the name is free again.
		f.admin.do("PUT", "/api/admin/settings/users.reuseUsernames", map[string]any{"value": "after_purge"}).want(t, 200, "")
		take("olive").want(t, 201, "")
	})
}

func TestADeletedPersonIsRefusedBySSOInsteadOfGettingANewAccount(t *testing.T) {
	t.Parallel()
	f := newFakeIDP(t)
	eachAccounts(t, f.opts(true), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		c := a.newClient("")
		f.signIn(t, c, "alice", map[string]any{})
		if signedInAs(t, c) != "alice" {
			t.Fatal("alice did not sign in")
		}
		id := mustUserID(t, a, "alice")
		admin.do("DELETE", "/api/users/"+id, map[string]any{}).want(t, 200, "")
		before := a.countRows(t, `SELECT COUNT(*) FROM users`)
		c2 := a.newClient("")
		f.signIn(t, c2, "alice", map[string]any{})
		if signedInAs(t, c2) != "" {
			t.Fatal("a deleted person signed in")
		}
		if after := a.countRows(t, `SELECT COUNT(*) FROM users`); after != before {
			t.Fatalf("autoCreate made %d new accounts for a deleted person", after-before)
		}
		// Restored, the same identity signs in again.
		items := admin.do("GET", "/api/admin/recycle?kind=user", nil).want(t, 200, "").body["items"].([]any)
		admin.do("POST", "/api/admin/recycle/"+items[0].(map[string]any)["id"].(string)+"/restore", map[string]any{}).want(t, 200, "")
		c3 := a.newClient("")
		f.signIn(t, c3, "alice", map[string]any{})
		if signedInAs(t, c3) != "alice" {
			t.Fatal("a restored person cannot sign in by SSO")
		}
	})
}

func TestATenantAdministratorDeletesOnlyTheirTenantsPeopleAndNoSiteAdministrator(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		f.admA.do("DELETE", "/api/users/"+f.userB, nil).want(t, 404, "user_not_found")
		f.admA.do("DELETE", "/api/users/"+mustUserID(t, a, "root"), nil).want(t, 403, "forbidden")
		f.memA.do("DELETE", "/api/users/"+f.userA, nil).want(t, 403, "forbidden")
		// Their own tenant's person, yes; and it is audited on their tenant's rows only.
		f.admA.do("DELETE", "/api/users/"+f.userA, nil).want(t, 200, "")
		a.auth.FlushAudit(context.Background())
		if rows := f.admA.audit(t, "user_deleted"); len(rows) != 1 {
			t.Fatalf("%v", rows)
		}
		if rows := f.admB.audit(t, "user_deleted"); len(rows) != 0 {
			t.Fatalf("B sees A's deletion: %v", rows)
		}
		// The last enabled site administrator cannot be deleted, by anyone.
		f.root.do("DELETE", "/api/users/"+mustUserID(t, a, "root"), nil).want(t, 409, "conflict")
	})
}

// --- confirmations and bulk jobs -------------------------------------------------------

func waitJob(t *testing.T, c *client, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		j := c.do("GET", "/api/admin/jobs/"+id, nil).want(t, 200, "").body["job"].(map[string]any)
		if s := j["state"]; s == "done" || s == "interrupted" || s == "failed" {
			return j
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the job did not finish")
	return nil
}

func TestABulkJobRunsOneItemAtATimeAndListsWhatFailed(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{spaces: func(o *spaces.Options) { o.CloseWait = 50 * time.Millisecond }}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		var ids []string
		for _, n := range []string{"One", "Two", "Three"} {
			ids = append(ids, tang.do("POST", "/api/spaces", map[string]any{"name": n}).want(t, 201, "").body["id"].(string))
		}
		// A confirmation shows the plan: each deletion's preview, and its size.
		c := admin.do("POST", "/api/admin/confirm", map[string]any{"action": "delete_spaces", "spaces": ids}).want(t, 200, "")
		if c.body["count"] != float64(3) || len(c.body["plan"].([]any)) != 3 || c.body["expires"] == "" || !strings.HasPrefix(c.body["confirm"].(string), "c_") {
			t.Fatalf("%s", c.raw)
		}
		// Refusals: too many, none, the same twice, an unknown action, a space of nobody.
		admin.do("POST", "/api/admin/confirm", map[string]any{"action": "delete_spaces", "spaces": []string{}}).want(t, 400, "invalid")
		admin.do("POST", "/api/admin/confirm", map[string]any{"action": "delete_spaces", "spaces": []string{ids[0], ids[0]}}).want(t, 400, "invalid")
		admin.do("POST", "/api/admin/confirm", map[string]any{"action": "dance", "spaces": ids}).want(t, 400, "invalid")
		admin.do("POST", "/api/admin/confirm", map[string]any{"action": "delete_spaces", "spaces": []string{ids[0], fakeID}}).want(t, 404, "not_found")
		tang.do("POST", "/api/admin/confirm", map[string]any{"action": "delete_spaces", "spaces": ids}).want(t, 403, "forbidden")
		// A bulk action needs a token, its own; one that is not the caller's is unknown.
		admin.do("POST", "/api/admin/bulk", map[string]any{}).want(t, 400, "invalid")
		admin.do("POST", "/api/admin/bulk", map[string]any{"confirm": "c_nope"}).want(t, 404, "not_found")
		tang.do("POST", "/api/admin/bulk", map[string]any{"confirm": c.body["confirm"]}).want(t, 403, "forbidden")
		other := a.addUser("li", pw, "admin")
		other.do("POST", "/api/admin/bulk", map[string]any{"confirm": c.body["confirm"]}).want(t, 404, "not_found")

		// One space is in use: that item fails with 409 busy, and the rest go on.
		h, err := a.mgr.Open(context.Background(), mustUser(t, a, "tang"), ids[1])
		if err != nil {
			t.Fatal(err)
		}
		r := admin.do("POST", "/api/admin/bulk", map[string]any{"confirm": c.body["confirm"]}).want(t, 202, "")
		j := r.body["job"].(map[string]any)
		if j["action"] != "delete_spaces" || j["total"] != float64(3) {
			t.Fatalf("%s", r.raw)
		}
		done := waitJob(t, admin, j["id"].(string))
		h.Release()
		failed := done["failed"].([]any)
		if done["done"] != float64(2) || len(failed) != 1 || failed[0].(map[string]any)["id"] != ids[1] || failed[0].(map[string]any)["code"] != "busy" {
			t.Fatalf("%v", done)
		}
		for _, id := range []string{ids[0], ids[2]} {
			tang.do("GET", "/api/spaces/"+id+"/tree", nil).want(t, 404, "not_found")
		}
		tang.do("GET", "/api/spaces/"+ids[1]+"/tree", nil).want(t, 200, "")
		// Used once: the token is spent.
		admin.do("POST", "/api/admin/bulk", map[string]any{"confirm": c.body["confirm"]}).want(t, 404, "not_found")
		// The job is the tenant's: someone of another tenant does not see it.
		// Audited: started, finished, and each deletion with the job.
		a.auth.FlushAudit(context.Background())
		if rows := admin.audit(t, "bulk_started"); len(rows) != 1 {
			t.Fatalf("%v", rows)
		}
		fin := admin.audit(t, "bulk_finished")
		if len(fin) != 1 || fin[0]["detail"].(map[string]any)["failed"] != float64(1) {
			t.Fatalf("%v", fin)
		}
		for _, row := range admin.audit(t, "space_deleted") {
			if row["detail"].(map[string]any)["bulk"] != j["id"] {
				t.Fatalf("%v", row)
			}
		}
	})
}

func TestAConfirmationIsStaleWhenTheMembersChanged(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newUdFix(t, a)
		tok := confirmFor(t, f.admin, "delete_spaces", "spaces", f.alone, f.joint)
		// A member is added to one of them after the plan was shown.
		f.owner.do("POST", "/api/spaces/"+f.alone+"/members", map[string]any{"username": "oscar", "role": "viewer"}).want(t, 201, "")
		f.admin.do("POST", "/api/admin/bulk", map[string]any{"confirm": tok}).want(t, 409, "confirm_stale")
		// It was spent: ask again, and now it holds.
		tok = confirmFor(t, f.admin, "delete_spaces", "spaces", f.alone, f.joint)
		j := f.admin.do("POST", "/api/admin/bulk", map[string]any{"confirm": tok}).want(t, 202, "").body["job"].(map[string]any)
		if done := waitJob(t, f.admin, j["id"].(string)); done["done"] != float64(2) {
			t.Fatalf("%v", done)
		}
		// A measurement again is not a change.
		tok = confirmFor(t, f.admin, "purge", "items", f.itemsOf(t)[0])
		if _, err := a.st.Q().Exec(context.Background(), `UPDATE recycle_items SET bytes = bytes + 5`); err != nil {
			t.Fatal(err)
		}
		f.admin.do("POST", "/api/admin/bulk", map[string]any{"confirm": tok}).want(t, 202, "")
	})
}

func (f *udFix) itemsOf(t *testing.T) []string {
	t.Helper()
	return idsOf(f.admin.do("GET", "/api/admin/recycle", nil).want(t, 200, "").body["items"], "id")
}

func TestBulkDeletingPeopleListsTheOnesThatCannotBeDeleted(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newUdFix(t, a)
		kazuha := mustUserID(t, a, "kazuha")
		// The caller themself is in the set: that one is a blocker, the others are deleted.
		c := f.admin.do("POST", "/api/admin/confirm", map[string]any{"action": "delete_users", "users": []string{kazuha, f.viewerID, f.outsiderID(t)}, "heir": f.editorID}).want(t, 200, "")
		if bl := c.body["blockers"].([]any); len(bl) == 0 || bl[0].(map[string]any)["user"] != kazuha || bl[0].(map[string]any)["reason"] != "self" {
			t.Fatalf("%s", c.raw)
		}
		j := f.admin.do("POST", "/api/admin/bulk", map[string]any{"confirm": c.body["confirm"]}).want(t, 202, "").body["job"].(map[string]any)
		done := waitJob(t, f.admin, j["id"].(string))
		failed := done["failed"].([]any)
		if done["done"] != float64(2) || len(failed) != 1 || failed[0].(map[string]any)["id"] != kazuha {
			t.Fatalf("%v", done)
		}
		if n := a.countRows(t, `SELECT COUNT(*) FROM users WHERE deleted_at IS NOT NULL`); n != 2 {
			t.Fatalf("%d people deleted", n)
		}
		f.noSpaceWithoutAnOwner(t)
		// Disable and enable are bulk actions too.
		c = f.admin.do("POST", "/api/admin/confirm", map[string]any{"action": "disable_users", "users": []string{f.editorID}}).want(t, 200, "")
		j = f.admin.do("POST", "/api/admin/bulk", map[string]any{"confirm": c.body["confirm"]}).want(t, 202, "").body["job"].(map[string]any)
		if d := waitJob(t, f.admin, j["id"].(string)); d["done"] != float64(1) {
			t.Fatalf("%v", d)
		}
		if n := a.countRows(t, `SELECT COUNT(*) FROM users WHERE id = '`+f.editorID+`' AND disabled = 1`); n != 1 {
			t.Fatal("not disabled")
		}
		// A person of nobody's tenant is as an id that does not exist.
		f.admin.do("POST", "/api/admin/confirm", map[string]any{"action": "delete_users", "users": []string{fakeID}}).want(t, 404, "user_not_found")
	})
}

func TestAJobThatWasRunningWhenTheServerStoppedReadsInterrupted(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		if _, err := a.st.Q().Exec(context.Background(), `INSERT INTO bulk_jobs(id, tenant_id, made_by, action, total, done, failed, state, created, updated) VALUES ('JOB0000001', ?, 'x', 'delete_spaces', 5, 2, '[]', 'running', 1, 1)`, store.DefaultTenantID); err != nil {
			t.Fatal(err)
		}
		if _, err := a.st.Q().Exec(context.Background(), `INSERT INTO bulk_jobs(id, tenant_id, made_by, action, total, done, failed, state, created, updated) VALUES ('JOB0000002', ?, 'x', 'purge', 1, 1, '[]', 'done', 1, 1)`, store.DefaultTenantID); err != nil {
			t.Fatal(err)
		}
		// A new server over the same database (a restart).
		a.restart(t)
		j := admin.do("GET", "/api/admin/jobs/JOB0000001", nil).want(t, 200, "").body["job"].(map[string]any)
		if j["state"] != "interrupted" || j["done"] != float64(2) || j["total"] != float64(5) {
			t.Fatalf("%v", j)
		}
		if j := admin.do("GET", "/api/admin/jobs/JOB0000002", nil).want(t, 200, "").body["job"].(map[string]any); j["state"] != "done" {
			t.Fatalf("%v", j)
		}
		admin.do("GET", "/api/admin/jobs/"+fakeID, nil).want(t, 404, "not_found")
	})
}

var _ = fmt.Sprint

// A restore or a purge of a person that a crash cut short is finished at the next
// start: the restore can be asked again, the purge goes on to the end.
func TestARestoreAndAPurgeOfAPersonThatWereCutShortAreFinished(t *testing.T) {
	t.Parallel()
	var stop atomic.Value
	stop.Store("")
	opts := accOpts{spaces: func(o *spaces.Options) {
		o.RecycleHook = func(s string) error {
			if v, _ := stop.Load().(string); v == s {
				return errors.New("crash at " + s)
			}
			return nil
		}
	}}
	eachAccounts(t, opts, func(t *testing.T, a *acc) {
		f := newUdFix(t, a)
		item := f.admin.do("DELETE", "/api/users/"+f.ownerID, map[string]any{"heir": f.viewerID}).want(t, 200, "").body["id"].(string)

		// Restore: the person is back, their spaces are not yet.
		stop.Store("restore-user:person")
		f.admin.do("POST", "/api/admin/recycle/"+item+"/restore", map[string]any{}).want(t, 500, "")
		stop.Store("")
		if n := a.countRows(t, `SELECT COUNT(*) FROM users WHERE id = '`+f.ownerID+`' AND deleted_at IS NULL`); n != 1 {
			t.Fatal("the person was not brought back")
		}
		if err := a.mgr.FinishRecycle(context.Background()); err != nil {
			t.Fatal(err)
		}
		// Purging the item of a person who is not deleted must not touch their data.
		tok := confirmFor(t, f.admin, "purge", "items", item)
		f.admin.do("DELETE", "/api/admin/recycle/"+item+"?confirm="+tok, nil).want(t, 202, "")
		a.mgr.WaitPurges()
		if n := a.countRows(t, `SELECT COUNT(*) FROM passwords WHERE user_id = '`+f.ownerID+`'`); n != 1 {
			t.Fatal("a purge of an item whose person is not deleted deleted their data")
		}
		if n := a.countRows(t, `SELECT COUNT(*) FROM users WHERE id = '`+f.ownerID+`' AND username = 'olive'`); n != 1 {
			t.Fatal("their row was emptied")
		}

		// Deleted again; the purge is cut short after their spaces: finished at the next start.
		item = f.admin.do("DELETE", "/api/users/"+f.ownerID, map[string]any{"heir": f.viewerID}).want(t, 200, "").body["id"].(string)
		stop.Store("purge-user:spaces")
		tok = confirmFor(t, f.admin, "purge", "items", item)
		f.admin.do("DELETE", "/api/admin/recycle/"+item+"?confirm="+tok, nil).want(t, 202, "")
		a.mgr.WaitPurges()
		stop.Store("")
		if n := a.countRows(t, `SELECT COUNT(*) FROM recycle_items WHERE id = '`+item+`' AND state = 'purging'`); n != 1 {
			t.Fatal("the item is not left purging")
		}
		if n := a.countRows(t, `SELECT COUNT(*) FROM passwords WHERE user_id = '`+f.ownerID+`'`); n != 1 {
			t.Fatal("their data went before their spaces were purged")
		}
		if err := a.mgr.FinishRecycle(context.Background()); err != nil {
			t.Fatal(err)
		}
		if n := a.countRows(t, `SELECT COUNT(*) FROM recycle_items`); n != 0 {
			t.Fatalf("%d items are left", n)
		}
		if n := a.countRows(t, `SELECT COUNT(*) FROM users WHERE id = '`+f.ownerID+`' AND username = 'deleted-`+f.ownerID+`' AND deleted_at IS NOT NULL`); n != 1 {
			t.Fatal("the purge was not finished")
		}
		if n := a.countRows(t, `SELECT COUNT(*) FROM passwords WHERE user_id = '`+f.ownerID+`'`); n != 0 {
			t.Fatal("their password is still there")
		}
	})
}

func TestAnExportOfAPersonHoldsTheirPersonalSpaceAndTheSpacesNobodyElseIsIn(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newUdFix(t, a)
		personal := f.owner.personalSpace()
		x := f.admin.do("POST", "/api/admin/exports", map[string]any{"user": f.ownerID}).want(t, 202, "")
		var names []string
		for _, s := range x.body["spaces"].([]any) {
			names = append(names, s.(map[string]any)["space"].(string))
		}
		if len(names) != 2 || !hasID(names, personal) || !hasID(names, f.alone) {
			t.Fatalf("%v", x.raw)
		}
		if done := waitExport(t, f.admin, x.body["id"].(string)); done["state"] != "ready" {
			t.Fatalf("%v", done)
		}
		f.admin.do("POST", "/api/admin/exports", map[string]any{"user": fakeID}).want(t, 404, "user_not_found")
		// Once the person is in the bin the same call exports what went with them.
		f.admin.do("DELETE", "/api/users/"+f.ownerID, map[string]any{"heir": f.viewerID}).want(t, 200, "")
		y := f.admin.do("POST", "/api/admin/exports", map[string]any{"user": f.ownerID}).want(t, 202, "")
		if n := len(y.body["spaces"].([]any)); n != 2 {
			t.Fatalf("%v", y.raw)
		}
		if done := waitExport(t, f.admin, y.body["id"].(string)); done["state"] != "ready" {
			t.Fatalf("%v", done)
		}
	})
}

// A person's item that went stale (a restore of it was cut short, so the person
// is back) and is purged later must never take what a later deletion of the same
// person holds: not the spaces waiting under the marker of a deletion cut short,
// and not the person's data once a later item holds them.
func TestPurgingAStaleItemOfAPersonLeavesALaterDeletionWhole(t *testing.T) {
	t.Parallel()
	var stop atomic.Value
	stop.Store("")
	opts := accOpts{spaces: func(o *spaces.Options) {
		o.RecycleHook = func(s string) error {
			if v, _ := stop.Load().(string); v == s {
				return errors.New("crash at " + s)
			}
			return nil
		}
	}}
	eachAccounts(t, opts, func(t *testing.T, a *acc) {
		f := newUdFix(t, a)
		purge := func(item string) {
			t.Helper()
			tok := confirmFor(t, f.admin, "purge", "items", item)
			f.admin.do("DELETE", "/api/admin/recycle/"+item+"?confirm="+tok, nil).want(t, 202, "")
			a.mgr.WaitPurges()
			if err := a.mgr.FinishRecycle(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		// staleItem deletes olive and cuts the restore of the item short: olive is
		// back and the item is in the bin again.
		staleItem := func() string {
			t.Helper()
			item := f.admin.do("DELETE", "/api/users/"+f.ownerID, map[string]any{"heir": f.viewerID}).want(t, 200, "").body["id"].(string)
			stop.Store("restore-user:person")
			f.admin.do("POST", "/api/admin/recycle/"+item+"/restore", map[string]any{}).want(t, 500, "")
			stop.Store("")
			if err := a.mgr.FinishRecycle(context.Background()); err != nil {
				t.Fatal(err)
			}
			f.owner.do("POST", "/api/login", map[string]any{"username": "olive", "password": pw}).want(t, 200, "")
			return item
		}

		// 1. A later deletion cut short: its space waits under the marker.
		i1 := staleItem()
		fresh := f.owner.do("POST", "/api/spaces", map[string]any{"name": "Fresh"}).want(t, 201, "").body["id"].(string)
		stop.Store("delete-user:recycled")
		f.admin.do("DELETE", "/api/users/"+f.ownerID, map[string]any{}).want(t, 500, "")
		stop.Store("")
		marker := `SELECT COUNT(*) FROM recycle_items WHERE parent = 'u:` + f.ownerID + `' AND space_id = '` + fresh + `'`
		if a.countRows(t, marker) != 1 {
			t.Fatal("the space is not waiting under the marker")
		}
		purge(i1)
		if a.countRows(t, marker) != 1 {
			t.Fatal("purging a stale item took the spaces of a later deletion")
		}
		// That deletion, asked again, adopts its space and can be restored whole.
		i2 := f.admin.do("DELETE", "/api/users/"+f.ownerID, map[string]any{}).want(t, 200, "")
		if with := idsOf(i2.body["with"], "space", "id"); len(with) != 1 || with[0] != fresh {
			t.Fatalf("%s", i2.raw)
		}

		// 2. A later deletion that finished: the person's data is that item's.
		stop.Store("restore-user:person")
		f.admin.do("POST", "/api/admin/recycle/"+i2.body["id"].(string)+"/restore", map[string]any{}).want(t, 500, "")
		stop.Store("")
		if err := a.mgr.FinishRecycle(context.Background()); err != nil {
			t.Fatal(err)
		}
		i3 := f.admin.do("DELETE", "/api/users/"+f.ownerID, map[string]any{}).want(t, 200, "").body["id"].(string)
		purge(i2.body["id"].(string))
		if n := a.countRows(t, `SELECT COUNT(*) FROM users WHERE id = '`+f.ownerID+`' AND username = 'olive' AND recycle_item = '`+i3+`'`); n != 1 {
			t.Fatal("purging a stale item emptied the person a later item holds")
		}
		if n := a.countRows(t, `SELECT COUNT(*) FROM passwords WHERE user_id = '`+f.ownerID+`'`); n != 1 {
			t.Fatal("purging a stale item deleted the data of a later deletion")
		}
		f.admin.do("POST", "/api/admin/recycle/"+i3+"/restore", map[string]any{}).want(t, 200, "")
		f.owner.do("POST", "/api/login", map[string]any{"username": "olive", "password": pw}).want(t, 200, "")
	})
}

// A deletion asked again after a failure records what the person was before the
// first attempt (enabled), not what that attempt made them (disabled), and keeps
// the transfers the first attempt made: a restore brings them back enabled and
// lists every space that was handed on.
func TestADeletionAskedAgainKeepsWhatTheFirstAttemptKnew(t *testing.T) {
	t.Parallel()
	var stop atomic.Value
	stop.Store("")
	opts := accOpts{spaces: func(o *spaces.Options) {
		o.RecycleHook = func(s string) error {
			if v, _ := stop.Load().(string); v == s {
				return errors.New("crash at " + s)
			}
			return nil
		}
	}}
	eachAccounts(t, opts, func(t *testing.T, a *acc) {
		f := newUdFix(t, a)
		stop.Store("delete-user:left")
		f.admin.do("DELETE", "/api/users/"+f.ownerID, map[string]any{"heir": f.viewerID}).want(t, 500, "")
		stop.Store("")
		item := f.admin.do("DELETE", "/api/users/"+f.ownerID, map[string]any{"heir": f.viewerID}).want(t, 200, "").body["id"].(string)
		r := f.admin.do("POST", "/api/admin/recycle/"+item+"/restore", map[string]any{}).want(t, 200, "")
		if tr := idsOf(r.body["transfers"], "space"); len(tr) != 2 || !hasID(tr, f.sid) || !hasID(tr, f.readers) {
			t.Fatalf("the transfers of the first attempt are not listed: %s", r.raw)
		}
		if lv := idsOf(r.body["left"], "space"); !hasID(lv, f.joint) || !hasID(lv, f.sid) {
			t.Fatalf("left %s", r.raw)
		}
		f.owner.do("POST", "/api/login", map[string]any{"username": "olive", "password": pw}).want(t, 200, "")
		if n := a.countRows(t, `SELECT COUNT(*) FROM users WHERE id = '`+f.ownerID+`' AND disabled = 0`); n != 1 {
			t.Fatal("came back disabled")
		}
		// And a person who was disabled before the first attempt comes back disabled.
		f.admin.do("PATCH", "/api/users/"+f.viewerID, map[string]any{"disabled": true}).want(t, 200, "")
		stop.Store("delete-user:disabled")
		f.admin.do("DELETE", "/api/users/"+f.viewerID, map[string]any{"heir": f.editorID}).want(t, 500, "")
		stop.Store("")
		it2 := f.admin.do("DELETE", "/api/users/"+f.viewerID, map[string]any{"heir": f.editorID}).want(t, 200, "").body["id"].(string)
		f.admin.do("POST", "/api/admin/recycle/"+it2+"/restore", map[string]any{}).want(t, 200, "")
		if n := a.countRows(t, `SELECT COUNT(*) FROM users WHERE id = '`+f.viewerID+`' AND disabled = 1 AND deleted_at IS NULL`); n != 1 {
			t.Fatal("a person who was disabled came back enabled")
		}
	})
}

// The confirmation of deleting people covers what the plan decides: which spaces
// are recycled, handed on and to whom, left, and which need an heir. A member
// added to a space of the person, or another heir, makes it stale.
func TestAConfirmationOfDeletingPeopleIsStaleWhenWhatThePlanDecidesChanged(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newUdFix(t, a)
		ask := func() string {
			return f.admin.do("POST", "/api/admin/confirm", map[string]any{"action": "delete_users", "users": []string{f.ownerID}, "heir": f.viewerID}).
				want(t, 200, "").body["confirm"].(string)
		}
		// A member added to the space nobody else was in: it is handed on now, not recycled.
		tok := ask()
		f.owner.do("POST", "/api/spaces/"+f.alone+"/members", map[string]any{"username": "oscar", "role": "viewer"}).want(t, 201, "")
		f.admin.do("POST", "/api/admin/bulk", map[string]any{"confirm": tok}).want(t, 409, "confirm_stale")
		// Another heir: Team's earliest editor is someone else now.
		tok = ask()
		f.owner.do("PATCH", "/api/spaces/"+f.sid+"/members/"+f.editorID, map[string]any{"role": "viewer"}).want(t, 200, "")
		f.owner.do("POST", "/api/spaces/"+f.sid+"/members", map[string]any{"username": "oscar", "role": "editor"}).want(t, 201, "")
		f.admin.do("POST", "/api/admin/bulk", map[string]any{"confirm": tok}).want(t, 409, "confirm_stale")
		// Unchanged, it holds.
		tok = ask()
		j := f.admin.do("POST", "/api/admin/bulk", map[string]any{"confirm": tok}).want(t, 202, "").body["job"].(map[string]any)
		if done := waitJob(t, f.admin, j["id"].(string)); done["done"] != float64(1) {
			t.Fatalf("%v", done)
		}
	})
}

// A membership that slipped in while a person was being deleted (made before
// their deletion disabled them, after it planned) is removed by the last step
// and listed with the others: a deleted person is never left in a live space.
func TestTheLastStepOfADeletionRemovesALeftoverMembership(t *testing.T) {
	t.Parallel()
	var slip atomic.Value
	slip.Store(func() {})
	opts := accOpts{spaces: func(o *spaces.Options) {
		o.RecycleHook = func(s string) error {
			if s == "delete-user:left" {
				slip.Load().(func())()
			}
			return nil
		}
	}}
	eachAccounts(t, opts, func(t *testing.T, a *acc) {
		f := newUdFix(t, a)
		other := f.editor.do("POST", "/api/spaces", map[string]any{"name": "Other"}).want(t, 201, "").body["id"].(string)
		slip.Store(func() {
			if _, err := a.st.Q().Exec(context.Background(), `INSERT INTO space_members(space_id, user_id, role, created) VALUES (?, ?, 'editor', 1)`, other, f.ownerID); err != nil {
				t.Error(err)
			}
		})
		item := f.admin.do("DELETE", "/api/users/"+f.ownerID, map[string]any{"heir": f.viewerID}).want(t, 200, "").body["id"].(string)
		slip.Store(func() {})
		if n := a.countRows(t, `SELECT COUNT(*) FROM space_members WHERE user_id = '`+f.ownerID+`'`); n != 0 {
			t.Fatalf("a deleted person is still a member of %d spaces", n)
		}
		f.noSpaceWithoutAnOwner(t)
		r := f.admin.do("POST", "/api/admin/recycle/"+item+"/restore", map[string]any{}).want(t, 200, "")
		if lv := idsOf(r.body["left"], "space"); !hasID(lv, other) {
			t.Fatalf("the leftover membership is not listed: %s", r.raw)
		}
	})
}

// A job that panics is marked failed, with the item it was on, and the server
// goes on; a job's progress that cannot be written is retried, and a job whose
// progress could not be written is marked failed rather than left running.
func TestABulkJobThatBreaksIsMarkedFailed(t *testing.T) {
	t.Parallel()
	var boom atomic.Bool
	opts := accOpts{spaces: func(o *spaces.Options) {
		o.RecycleHook = func(s string) error {
			if s == "delete-user:disabled" && boom.Load() {
				panic("a bug in a step")
			}
			return nil
		}
	}}
	eachAccounts(t, opts, func(t *testing.T, a *acc) {
		f := newUdFix(t, a)
		boom.Store(true)
		c := f.admin.do("POST", "/api/admin/confirm", map[string]any{"action": "delete_users", "users": []string{f.viewerID}, "heir": f.editorID}).want(t, 200, "")
		j := f.admin.do("POST", "/api/admin/bulk", map[string]any{"confirm": c.body["confirm"]}).want(t, 202, "").body["job"].(map[string]any)
		done := waitJob(t, f.admin, j["id"].(string))
		boom.Store(false)
		failed := done["failed"].([]any)
		if done["state"] != "failed" || len(failed) != 1 || failed[0].(map[string]any)["id"] != f.viewerID {
			t.Fatalf("%v", done)
		}
		f.admin.do("GET", "/api/session", nil).want(t, 200, "")

		// Progress that cannot be written: tried again, and the job ends.
		kazuha := mustUserID(t, a, "kazuha")
		restore := api.SetUpdateBulkJob(kazuha, func(state string, try int) error {
			if state == "running" && try == 0 {
				return errors.New("the database is busy")
			}
			return nil
		})
		c = f.admin.do("POST", "/api/admin/confirm", map[string]any{"action": "disable_users", "users": []string{f.viewerID}}).want(t, 200, "")
		j = f.admin.do("POST", "/api/admin/bulk", map[string]any{"confirm": c.body["confirm"]}).want(t, 202, "").body["job"].(map[string]any)
		if done := waitJob(t, f.admin, j["id"].(string)); done["state"] != "done" || done["done"] != float64(1) {
			t.Fatalf("%v", done)
		}
		// Progress that can never be written: the job is marked failed at the end.
		api.SetUpdateBulkJob(kazuha, func(state string, try int) error {
			if state == "running" {
				return errors.New("the database is gone")
			}
			return nil
		})
		c = f.admin.do("POST", "/api/admin/confirm", map[string]any{"action": "enable_users", "users": []string{f.viewerID}}).want(t, 200, "")
		j = f.admin.do("POST", "/api/admin/bulk", map[string]any{"confirm": c.body["confirm"]}).want(t, 202, "").body["job"].(map[string]any)
		if done := waitJob(t, f.admin, j["id"].(string)); done["state"] != "failed" {
			t.Fatalf("%v", done)
		}
		restore()
	})
}

// The rights of the administrator who started a job are checked again for each
// item: one who is no longer an administrator stops being obeyed.
func TestABulkJobChecksTheAdministratorForEachItem(t *testing.T) {
	t.Parallel()
	var demote atomic.Value
	demote.Store(func() {})
	opts := accOpts{spaces: func(o *spaces.Options) {
		o.RecycleHook = func(s string) error {
			if s == "recycle:rows" {
				demote.Load().(func())()
			}
			return nil
		}
	}}
	eachAccounts(t, opts, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		var ids []string
		for _, n := range []string{"One", "Two"} {
			ids = append(ids, f.admA.do("POST", "/api/spaces", map[string]any{"name": n}).want(t, 201, "").body["id"].(string))
		}
		c := f.admA.do("POST", "/api/admin/confirm", map[string]any{"action": "delete_spaces", "spaces": ids}).want(t, 200, "")
		admAID := mustUserID(t, a, "adm-a")
		var once sync.Once
		demote.Store(func() {
			once.Do(func() {
				if _, err := a.st.Q().Exec(context.Background(), `UPDATE users SET role = 'member' WHERE id = ?`, admAID); err != nil {
					t.Error(err)
				}
				a.auth.Forget(admAID)
			})
		})
		j := f.admA.do("POST", "/api/admin/bulk", map[string]any{"confirm": c.body["confirm"]}).want(t, 202, "").body["job"].(map[string]any)
		done := waitJob(t, f.root, j["id"].(string))
		failed := done["failed"].([]any)
		if done["done"] != float64(1) || len(failed) != 1 || failed[0].(map[string]any)["id"] != ids[1] || failed[0].(map[string]any)["code"] != "forbidden" {
			t.Fatalf("%v", done)
		}
	})
}

// A site administrator's job waits in the queue of the tenant it works on, not
// of their own: the row says so, and the tenant's administrators do not see it.
func TestASiteAdministratorsJobIsQueuedByTheTenantItWorksOn(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		c := f.root.do("POST", "/api/admin/confirm", map[string]any{"action": "disable_spaces", "spaces": []string{f.spaceB}}).want(t, 200, "")
		j := f.root.do("POST", "/api/admin/bulk", map[string]any{"confirm": c.body["confirm"]}).want(t, 202, "").body["job"].(map[string]any)
		waitJob(t, f.root, j["id"].(string))
		if n := f.countOf(`SELECT COUNT(*) FROM bulk_jobs WHERE id = '` + j["id"].(string) + `' AND tenant_id = '` + f.tenB + `'`); n != 1 {
			t.Fatal("the job is not queued by the tenant it works on")
		}
		for _, cl := range []*client{f.admA, f.admB} {
			cl.do("GET", "/api/admin/jobs/"+j["id"].(string), nil).want(t, 404, "not_found")
		}
		f.root.do("POST", "/api/admin/spaces/"+f.spaceB+"/enable", nil).want(t, 204, "")
	})
}

// The answer to restoring a person shows a tenant administrator no personal
// space (its id or its name); it counts them. The site administrator and the
// audit row see them.
func TestRestoringAPersonShowsATenantAdministratorNoPersonalSpace(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		personal := f.memA.personalSpace()
		del := func(c *client) string {
			return c.do("DELETE", "/api/users/"+f.userA, nil).want(t, 200, "").body["id"].(string)
		}
		r := f.admA.do("POST", "/api/admin/recycle/"+del(f.admA)+"/restore", map[string]any{}).want(t, 200, "")
		if strings.Contains(r.raw, personal) {
			t.Fatalf("a tenant administrator sees the personal space: %s", r.raw)
		}
		if n, _ := r.body["restored"].(map[string]any)["personal"].(float64); n != 1 {
			t.Fatalf("the personal spaces are not counted: %s", r.raw)
		}
		r = f.root.do("POST", "/api/admin/recycle/"+del(f.root)+"/restore", map[string]any{}).want(t, 200, "")
		if !strings.Contains(r.raw, personal) {
			t.Fatalf("the site administrator does not see it: %s", r.raw)
		}
		a.auth.FlushAudit(context.Background())
		if rows := f.root.audit(t, "user_restored"); len(rows) != 2 || !strings.Contains(fmt.Sprint(rows), personal) {
			t.Fatalf("the audit row is incomplete: %v", rows)
		}
	})
}
