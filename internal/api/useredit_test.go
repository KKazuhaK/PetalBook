package api_test

import (
	"strings"
	"testing"
)

// Who may edit whom, and what an edit leaves behind: the audit row with what it
// replaced, and a notice to the person.
func TestAdminEditsNameAndUsernameOfAPersonOfTheirTenant(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		url := "/api/users/" + f.userB
		r := f.admB.do("PATCH", url, map[string]any{"name": "  Member Bee ", "username": "Bee-B"}).want(t, 200, "")
		if u := r.body["user"].(map[string]any); u["name"] != "Member Bee" || u["username"] != "bee-b" {
			t.Fatal(r.raw)
		}
		// The new username signs in, the old one does not.
		c := a.newClient("")
		c.do("POST", "/api/login", map[string]any{"username": "mem-b", "password": pw}).want(t, 401, "")
		c.do("POST", "/api/login", map[string]any{"username": "bee-b", "password": pw}).want(t, 200, "")
		// What it replaced is in the audit row.
		a.auth.FlushAudit(t.Context())
		items := f.admB.do("GET", "/api/admin/audit?kind=user_changed", nil).want(t, 200, "").body["items"].([]any)
		d := items[0].(map[string]any)["detail"].(map[string]any)
		old := d["old"].(map[string]any)
		if d["name"] != "Member Bee" || d["username"] != "bee-b" || old["name"] != "Member B" || old["username"] != "mem-b" {
			t.Fatalf("%v", d)
		}
		// The person is told that someone else changed these, and by whom, never the values.
		n := f.memB.do("GET", "/api/notifications", nil).want(t, 200, "").raw
		if !contains(n, "account_changed", "Admin B") || strings.Contains(n, "Bee-B") {
			t.Fatal(n)
		}
		// Taken, malformed, and not theirs to edit.
		f.admB.do("PATCH", url, map[string]any{"username": "adm-b"}).want(t, 409, "exists")
		f.admB.do("PATCH", url, map[string]any{"username": "no spaces"}).want(t, 400, "invalid")
		f.admB.do("PATCH", url, map[string]any{"name": " "}).want(t, 400, "invalid")
		f.admB.do("PATCH", "/api/users/"+f.userA, map[string]any{"name": "x"}).want(t, 404, "not_found")
		f.admA.do("PATCH", "/api/users/"+mustUserID(t, a, "root"), map[string]any{"name": "x"}).want(t, 403, "forbidden")
		f.memB.do("PATCH", url, map[string]any{"name": "x"}).want(t, 403, "forbidden")
		// A site administrator edits anyone, another site administrator included.
		f.root.do("PATCH", url, map[string]any{"name": "Member B"}).want(t, 200, "")
		f.root.do("PATCH", "/api/users/"+mustUserID(t, a, "root"), map[string]any{"name": "The Root"}).want(t, 200, "")
	})
}

// A new address gets the link that proves it, and the old one a note; "mark" is
// the other way, and what a tenant administrator marks does not hold the
// address on the server (3.6).
func TestAdminChangesAnAddressByVerifyOrMark(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		srv := fakeMail(t)
		enableMail(t, f.root, srv)
		verifiedUser(t, a, "vera", "vera@example.test")
		id := mustUserID(t, a, "vera")
		r := f.root.do("PATCH", "/api/users/"+id, map[string]any{"email": "vera.new@example.test"}).want(t, 200, "")
		if r.body["verification"] != "sent" || r.body["user"].(map[string]any)["emailVerified"] != false {
			t.Fatal(r.raw)
		}
		srv.Wait(2)
		if m := lastTo(t, srv, "vera.new@example.test"); !contains(m.Text, "/verify-email#") {
			t.Fatalf("%+v", m)
		}
		if m := lastTo(t, srv, "vera@example.test"); contains(m.Text, "#") {
			t.Fatalf("the old address was sent a link: %+v", m)
		}
		// Marked: verified at once, nothing is sent to prove it.
		before := len(srv.Messages())
		r = f.root.do("PATCH", "/api/users/"+id, map[string]any{"email": "vera.marked@example.test", "emailMode": "mark"}).want(t, 200, "")
		if r.body["verification"] != nil || r.body["user"].(map[string]any)["emailVerified"] != true {
			t.Fatal(r.raw)
		}
		f.root.do("PATCH", "/api/users/"+id, map[string]any{"email": "x@example.test", "emailMode": "sometimes"}).want(t, 400, "invalid")
		if n := len(srv.Messages()); n != before {
			t.Fatalf("%d messages were sent for a marked address", n-before)
		}
	})
}

// A temporary password: the administrator types it and never sees it again, the
// person's sessions end, the next sign-in with it may only choose a new one, and
// choosing one lifts that.
func TestTemporaryPasswordForcesAChangeAtTheNextSignIn(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		url := "/api/users/" + f.userB + "/password"
		const temp = "temporary-pass-1"
		f.admB.do("POST", url, map[string]any{"password": "short"}).want(t, 400, "invalid")
		f.admB.do("POST", url, map[string]any{"password": temp}).want(t, 204, "")
		// Every session of theirs ended.
		f.memB.do("GET", "/api/spaces", nil).want(t, 401, "")
		// Signing in with it works but is held: nothing else, until a new password is chosen.
		c := a.newClient("")
		c.do("POST", "/api/login", map[string]any{"username": "mem-b", "password": temp}).want(t, 200, "")
		if r := c.do("GET", "/api/session", nil).want(t, 200, ""); r.body["hold"] != "password" {
			t.Fatal(r.raw)
		}
		c.do("GET", "/api/spaces", nil).want(t, 403, "password_change_required")
		c.do("POST", "/api/account/password", map[string]any{"current": "wrong", "new": "my-own-password-2"}).want(t, 403, "forbidden")
		c.do("POST", "/api/account/password", map[string]any{"current": temp, "new": "my-own-password-2"}).want(t, 204, "")
		c.do("GET", "/api/spaces", nil).want(t, 200, "")
		if r := c.do("GET", "/api/session", nil).want(t, 200, ""); r.body["hold"] != nil {
			t.Fatal(r.raw)
		}
		// The next sign-in is an ordinary one.
		d := a.newClient("")
		d.do("POST", "/api/login", map[string]any{"username": "mem-b", "password": "my-own-password-2"}).want(t, 200, "")
		d.do("GET", "/api/spaces", nil).want(t, 200, "")
		// The password is in no audit row, and the person was told.
		a.auth.FlushAudit(t.Context())
		raw := f.root.do("GET", "/api/admin/audit?kind=password_set_by_admin", nil).want(t, 200, "").raw
		if strings.Contains(raw, temp) || !contains(raw, "password_set_by_admin") {
			t.Fatal(raw)
		}
		if n := d.do("GET", "/api/notifications", nil).want(t, 200, "").raw; !contains(n, "password_set") {
			t.Fatal(n)
		}
		// Not for a site administrator by a tenant administrator, nor across tenants.
		f.admA.do("POST", "/api/users/"+mustUserID(t, a, "root")+"/password", map[string]any{"password": temp}).want(t, 403, "forbidden")
		f.admB.do("POST", "/api/users/"+f.userA+"/password", map[string]any{"password": temp}).want(t, 404, "not_found")
		f.memA.do("POST", url, map[string]any{"password": temp}).want(t, 403, "forbidden")
	})
}

// A person renames themself; the old name is free for nobody else's login and
// the change is audited with what it replaced.
func TestAPersonChangesTheirOwnUsername(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		r := f.memB.do("PATCH", "/api/account", map[string]any{"username": "Bee.Two"}).want(t, 200, "")
		if r.user()["username"] != "bee.two" {
			t.Fatal(r.raw)
		}
		f.memB.do("PATCH", "/api/account", map[string]any{"username": "adm-b"}).want(t, 409, "exists")
		f.memB.do("PATCH", "/api/account", map[string]any{"username": "x"}).want(t, 400, "invalid")
		c := a.newClient("")
		c.do("POST", "/api/login", map[string]any{"username": "bee.two", "password": pw}).want(t, 200, "")
		a.auth.FlushAudit(t.Context())
		d := f.admB.do("GET", "/api/admin/audit?kind=user_changed", nil).want(t, 200, "").body["items"].([]any)[0].(map[string]any)["detail"].(map[string]any)
		if d["username"] != "bee.two" || d["old"].(map[string]any)["username"] != "mem-b" {
			t.Fatalf("%v", d)
		}
	})
}
