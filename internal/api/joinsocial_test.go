package api_test

import (
	"strings"
	"testing"

	"petalbook.app/server/internal/auth/testidp"
)

// A new account made by a site social provider's autoCreate with an address
// the provider vouches for follows the domain's join mode (3.6 d5), as a
// password sign-up does: auto joins the organisation, request waits for its
// administrators, off is as before (the provider's own tenant).
func TestSocialAutoCreateFollowsTheDomainsJoinMode(t *testing.T) {
	t.Parallel()
	idp := newIDPFor(t, "soc")
	add := func(key, email string, verified bool) {
		idp.addUser(key, testidp.Claims{"sub": "sub-" + key, "preferred_username": key, "email": email, "email_verified": verified, "name": key})
	}
	add("alice", "alice@acme.example", true)
	add("bob", "bob@acme.example", true)
	add("liar", "liar@acme.example", false) // the provider does not vouch for it
	add("sub", "sub@sub.acme.example", true)
	add("dan", "dan@acme.example", true)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newJoinFx(t, a)
		f.root.do("POST", "/api/admin/providers", idp.providerBody("soc", map[string]any{"enabled": true, "class": "social", "autoCreate": true, "mergeVerifiedEmail": true})).want(t, 201, "")
		in := func(who string) (string, *client) {
			c := a.newClient("")
			return idp.signIn2(t, c, "soc", who, nil), c
		}

		// Off (the default): as before, the account is made where the provider puts it.
		if loc, c := in("dan"); loc != "/" || signedInAs(t, c) != "dan" || userTenant(t, a, "dan").TenantID == f.tenB {
			t.Fatalf("off: %s", loc)
		}

		// Auto: the verified address joins the organisation; the others do not.
		setJoin(f.admB, f.tenB, "acme.example", "auto").want(t, 200, "")
		if loc, c := in("alice"); loc != "/" || signedInAs(t, c) != "alice" || userTenant(t, a, "alice").TenantID != f.tenB {
			t.Fatalf("auto: %s", loc)
		}
		if loc, _ := in("liar"); userTenant(t, a, "liar").TenantID == f.tenB {
			t.Fatalf("an address the provider does not vouch for joined: %s", loc)
		}
		if loc, _ := in("sub"); userTenant(t, a, "sub").TenantID == f.tenB {
			t.Fatalf("a subdomain joined: %s", loc)
		}
		a.auth.FlushAudit(t.Context())
		if rows := f.admB.audit(t, "join_auto"); len(rows) != 1 {
			t.Fatalf("join_auto rows: %v", rows)
		}

		// Request: a pending request and the waiting page; no account.
		add("erin", "erin@acme.example", true)
		setJoin(f.admB, f.tenB, "acme.example", "request").want(t, 200, "")
		loc, c := in("bob")
		tok, ok := strings.CutPrefix(loc, "/register/waiting#")
		if !ok || tok == "" || signedInAs(t, c) != "" || !mustNoUser(a, "bob") {
			t.Fatalf("request: %s", loc)
		}
		anon := a.newClient("")
		if s := joinStatus(anon, tok).want(t, 200, ""); s.body["state"] != "pending" || s.body["username"] != "bob" {
			t.Fatal(s.raw)
		}
		// Again while it waits: no second request.
		if loc2, _ := in("bob"); strings.HasPrefix(loc2, "/register/waiting") || !strings.Contains(loc2, "sso_error=join_pending") {
			t.Fatalf("a second request: %s", loc2)
		}
		id := pendingID(t, f.admB, f.tenB)
		f.admB.do("POST", joinPath(f.tenB)+"/"+id+"/approve", map[string]any{}).want(t, 200, "")
		u := userTenant(t, a, "bob")
		if u.TenantID != f.tenB || u.Role != "member" || !u.EmailVerified {
			t.Fatalf("%+v", u)
		}
		// The provider is linked by the approval: the next sign-in is the account's.
		if loc, c := in("bob"); loc != "/" || signedInAs(t, c) != "bob" {
			t.Fatalf("after approval: %s", loc)
		}
		if s := joinStatus(anon, tok).want(t, 200, ""); s.body["state"] != "approved" {
			t.Fatal(s.raw)
		}

		// A disabled organisation takes nobody.
		f.root.do("PATCH", "/api/admin/tenants/"+f.tenB, map[string]any{"disabled": true}).want(t, 200, "")
		if loc, _ := in("erin"); mustNoUser(a, "erin") == false || loc == "/" {
			t.Fatalf("a disabled organisation took someone: %s", loc)
		}
	})
}
