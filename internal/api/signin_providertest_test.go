package api_test

import (
	"context"
	"errors"
	"testing"

	crewjamsaml "github.com/crewjam/saml"

	"petalbook.app/server/internal/auth/testidp"
	"petalbook.app/server/internal/store"
)

// A test sign-in shows an administrator what a provider says about them, with
// nothing made, linked or signed in, and works for a provider that is still off.
func TestAdministratorsCanTestAProviderBeforeTurningItOn(t *testing.T) {
	t.Parallel()
	f := newIDPFor(t, "one")
	f.addUser("alice", testidp.Claims{"sub": "sub-alice", "preferred_username": "alice", "email": "alice@example.test", "name": "Alice A", "email_verified": true})
	idp := newFakeSAML(t, false)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		ctx := context.Background()
		a.clock.Advance(timeSince(a))
		admin := a.setup("kazuha", pw)
		admin.do("POST", "/api/admin/providers", f.providerBody("one", map[string]any{"enabled": false, "autoCreate": true, "mergeVerifiedEmail": true})).want(t, 201, "")

		// Nobody signs in at a provider that is off ...
		c := a.newClient("")
		if r := c.do("GET", "/api/auth/oidc/one/start", nil); r.code != 303 || r.resp.Header.Get("Location") != "/login?sso_error=provider_unavailable" {
			t.Fatalf("%d %s", r.code, r.resp.Header.Get("Location"))
		}
		// ... but the administrator can try it.
		member := a.addUser("tang", pw, "member")
		usersBefore := admin.do("GET", "/api/users", nil).want(t, 200, "").raw
		member.do("POST", "/api/admin/providers/one/test", nil).want(t, 403, "forbidden")
		a.newClient("").do("POST", "/api/admin/providers/one/test", nil).want(t, 401, "unauthenticated")
		admin.do("POST", "/api/admin/providers/nope/test", nil).want(t, 404, "not_found")
		at := f.beginAt(t, admin, "/api/admin/providers/one/test", "/api/auth/oidc/one/callback", "alice", map[string]any{})
		loc, _ := finish(t, admin, at.cb)
		if loc != "/admin/security/providers?provider_test=one" {
			t.Fatal(loc)
		}
		// Only the administrator who ran it reads the result, once.
		member.do("GET", "/api/admin/providers/one/test", nil).want(t, 403, "forbidden")
		r := admin.do("GET", "/api/admin/providers/one/test", nil).want(t, 200, "")
		res, _ := r.body["result"].(map[string]any)
		if res["subject"] != "sub-alice" || res["email"] != "alice@example.test" || res["emailTrusted"] != true || res["name"] != "Alice A" || res["username"] != "alice" {
			t.Fatal(r.raw)
		}
		if admin.do("GET", "/api/admin/providers/one/test", nil).want(t, 200, "").body["result"] != nil {
			t.Fatal("the result was read twice")
		}
		// Nothing was made or linked, and the administrator is still the same person.
		if got := admin.do("GET", "/api/users", nil).want(t, 200, "").raw; got != usersBefore {
			t.Fatal("a test sign-in changed the users")
		}
		if _, err := store.FindLink(ctx, a.st.Q(), "one", "sub-alice"); !errors.Is(err, store.ErrNotFound) {
			t.Fatal("a test sign-in linked an account")
		}
		if signedInAs(t, admin) != "kazuha" {
			t.Fatal("the session changed")
		}

		// A SAML provider that sends no address says why, and nothing is made.
		admin.do("POST", "/api/admin/providers", idp.body(samlSlug, map[string]any{"enabled": false})).want(t, 201, "")
		idp.learn(t, admin, samlSlug)
		idp.reset()
		idp.setSession(func(x *crewjamsaml.Session) {
			x.NameID, x.CustomAttributes = "opaque-1", []crewjamsaml.Attribute{samlAttr("displayName", "Pat")}
		})
		sat := idp.beginVia(t, admin, samlSlug, "/api/admin/providers/"+samlSlug+"/test", map[string]any{})
		sr := admin.postACS(samlSlug, sat.response, sat.relay, sat.cookie)
		if sr.code != 303 || sr.resp.Header.Get("Location") != "/admin/security/providers?sso_error=saml_no_email" {
			t.Fatalf("%d %s", sr.code, sr.resp.Header.Get("Location"))
		}
		// With an address it shows it.
		idp.reset()
		sat = idp.beginVia(t, admin, samlSlug, "/api/admin/providers/"+samlSlug+"/test", map[string]any{})
		sr = admin.postACS(samlSlug, sat.response, sat.relay, sat.cookie)
		if sr.code != 303 || sr.resp.Header.Get("Location") != "/admin/security/providers?provider_test="+samlSlug {
			t.Fatalf("%d %s", sr.code, sr.resp.Header.Get("Location"))
		}
		res, _ = admin.do("GET", "/api/admin/providers/"+samlSlug+"/test", nil).want(t, 200, "").body["result"].(map[string]any)
		if res["subject"] != "subject-001" || res["email"] != "saml.person@example.test" || res["emailTrusted"] != false {
			t.Fatalf("%v", res)
		}
		// The test attempt of an off provider cannot be used as a real sign-in:
		// a login attempt at the SAML provider that is off is refused.
		idp.reset()
		plain := a.newClient("")
		if r := plain.do("POST", "/api/auth/saml/"+samlSlug+"/start", map[string]any{}); r.code != 404 {
			t.Fatalf("%d %s", r.code, r.raw)
		}
	})
}

// Only the administrator's own route starts a test: the public start refuses
// the intent, even for a signed-in person, and makes nothing pending.
func TestThePublicStartRefusesTheTestIntent(t *testing.T) {
	t.Parallel()
	f := newIDPFor(t, "one")
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		admin.do("POST", "/api/admin/providers", f.providerBody("one", map[string]any{"enabled": false})).want(t, 201, "")
		member := a.addUser("tang", pw, "member")
		r := member.do("POST", "/api/auth/oidc/one/start", map[string]any{"intent": "test"}).want(t, 400, "invalid")
		if r.resp.Header.Get("Set-Cookie") != "" {
			t.Fatal("a pending sign-in was made")
		}
	})
}

// Deleting a provider ends the sessions that started through it, and no others.
func TestDeletingAProviderEndsTheSessionsStartedThroughIt(t *testing.T) {
	t.Parallel()
	f := newIDPFor(t, "one")
	f.addUser("alice", testidp.Claims{"sub": "sub-alice", "preferred_username": "alice", "email": "alice@example.test", "name": "Alice A", "email_verified": true})
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		admin.do("POST", "/api/admin/providers", f.providerBody("one", map[string]any{"autoCreate": true})).want(t, 201, "")
		via := a.newClient("")
		if loc := f.signIn2(t, via, "one", "alice", map[string]any{}); loc == "" || signedInAs(t, via) != "alice" {
			t.Fatalf("not signed in: %q", loc)
		}
		admin.do("DELETE", "/api/admin/providers/one?force=1", nil).want(t, 204, "")
		if signedInAs(t, via) != "" {
			t.Fatal("the session started through the deleted provider lives on")
		}
		if signedInAs(t, admin) != "kazuha" {
			t.Fatal("another session ended")
		}
	})
}
