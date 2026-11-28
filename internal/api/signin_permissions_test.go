package api_test

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/store"
)

// sameAsSession checks a sign-in answer against GET /api/session of the client
// it signed in: the same user (permissions included) and the same hold.
func sameAsSession(t *testing.T, what string, answer result, c *client) []string {
	t.Helper()
	s := c.do("GET", "/api/session", nil).want(t, 200, "")
	if !reflect.DeepEqual(answer.body["user"], s.body["user"]) {
		t.Fatalf("%s: user\n sign-in: %v\n session: %v", what, answer.body["user"], s.body["user"])
	}
	if h, ok := answer.body["hold"]; !ok || h != s.body["hold"] {
		t.Fatalf("%s: hold %v (present %v), session says %v", what, h, ok, s.body["hold"])
	}
	return toStrings(answer.user()["permissions"])
}

// Every JSON sign-in answers the person's permissions and hold exactly as
// GET /api/session does, so the app needs no second request and no guess from
// the role's name.
func TestSignInAnswersCarryTheSessionsPermissionsAndHold(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		if _, err := a.auth.CreateUser(context.Background(), auth.CreateParams{Username: "adm", Role: "admin", Password: pw, TenantID: store.DefaultTenantID}); err != nil {
			t.Fatal(err)
		}
		a.addUser("mem", pw, "member")
		a.addUser("aud", pw, "member")
		g := root.mkGroup(t, "Auditors", "local")
		grantRole(t, a, "R1", store.RoleScopeTenant, store.DefaultTenantID, store.GrantToGroup, g, "tenant.audit.read")
		root.groupAdd(t, g, mustUserID(t, a, "aud")).want(t, 200, "")

		login := func(name string) []string {
			c := a.newClient("")
			ans := c.do("POST", "/api/login", map[string]any{"identifier": name, "password": pw}).want(t, 200, "")
			return sameAsSession(t, "login "+name, ans, c)
		}
		if p := login("root"); !slices.Contains(p, "site.tenants.manage") || !slices.Contains(p, "tenant.users.manage") {
			t.Fatalf("site administrator: %v", p)
		}
		if p := login("adm"); slices.Contains(p, "site.tenants.manage") || !slices.Contains(p, "tenant.users.manage") {
			t.Fatalf("tenant administrator: %v", p)
		}
		if p := login("mem"); !slices.Equal(p, []string{"tenant.spaces.create"}) {
			t.Fatalf("member: %v", p)
		}
		if p := login("aud"); !slices.Contains(p, "tenant.audit.read") || !slices.Contains(p, "tenant.spaces.create") {
			t.Fatalf("custom role through a group: %v", p)
		}

		// The passkey sign-in, and its hold for a temporary password.
		mem := a.newClient("")
		mem.do("POST", "/api/login", map[string]any{"identifier": "mem", "password": pw}).want(t, 200, "")
		key := newVKey(mustUserID(t, a, "mem"))
		mem.register(t, key, "key").want(t, 201, "")
		c := a.newClient("")
		ans := c.signInWithPasskey(t, key, "mem", "/x").want(t, 200, "")
		if p := sameAsSession(t, "passkey", ans, c); !slices.Equal(p, []string{"tenant.spaces.create"}) || ans.body["return"] != "/x" {
			t.Fatalf("passkey: %v %v", p, ans.raw)
		}
		root.do("POST", "/api/users/"+mustUserID(t, a, "mem")+"/password", map[string]any{"password": pw + "2"}).want(t, 204, "")
		c = a.newClient("")
		ans = c.signInWithPasskey(t, key, "mem", "").want(t, 200, "")
		if sameAsSession(t, "passkey, temporary password", ans, c); ans.body["hold"] != "password" {
			t.Fatalf("hold %v", ans.body["hold"])
		}
	})
}

// Setup, an accepted invite and a confirmed registration answer the same, and
// a person who must register a passkey is told so.
func TestSetupInviteAndRegistrationAnswersCarryThePermissions(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		first := a.newClient("")
		ans := first.do("POST", "/api/setup", map[string]any{"token": a.auth.SetupToken(), "username": "root", "password": pw}).want(t, 201, "")
		if p := sameAsSession(t, "setup", ans, first); !slices.Contains(p, "site.tenants.manage") {
			t.Fatalf("setup: %v", p)
		}
		openRegistration(t, first, srv)

		inv := first.do("POST", "/api/invites", map[string]any{"role": "member", "days": 7}).want(t, 201, "")
		c := a.newClient("")
		ans = c.do("POST", "/api/invites/accept", map[string]any{"token": inv.body["token"], "username": "lin", "password": pw}).want(t, 201, "")
		if p := sameAsSession(t, "invite", ans, c); !slices.Equal(p, []string{"tenant.spaces.create"}) {
			t.Fatalf("invite: %v", p)
		}

		anon := a.newClient("")
		reg(anon, map[string]any{}).want(t, 202, "")
		ans = anon.do("POST", "/api/register/confirm", map[string]any{"token": srv.Wait(1)[0].Token(t)}).want(t, 201, "")
		if p := sameAsSession(t, "registration", ans, anon); !slices.Equal(p, []string{"tenant.spaces.create"}) {
			t.Fatalf("registration: %v", p)
		}
	})
}
