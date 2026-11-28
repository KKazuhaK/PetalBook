package api_test

import (
	"context"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
)

// The review of 3.6 d4.

// A sign-in through a tenant's provider spends nothing of the tenant's
// outbound share (rate.outboundPerMinute): anyone could start sign-ins and
// send junk callbacks, which would drain it, stop its domain checks and, with
// ssoOnly on, lock its people out. The per-address sign-in limits bound them.
func TestSignInsDoNotSpendTheTenantsOutboundShare(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		f.orgProvider(t, f.admB, f.tenB, "acme-sso", map[string]any{"autoCreate": true})
		share := func(v any) {
			f.root.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/rate.outboundPerMinute", map[string]any{"value": v}).want(t, 200, "")
		}
		// No share at all: the sign-in still works.
		share(0)
		if c := a.newClient(""); idp.signIn2(t, c, "acme-sso", "alice", nil) != "/" || signedInAs(t, c) == "" {
			t.Fatal("a sign-in was held to the outbound share")
		}
		// One a minute: sign-ins leave it to the domain's check.
		share(1)
		a.clock.Advance(61 * time.Second)
		for range 3 {
			if loc := idp.signIn2(t, a.newClient(""), "acme-sso", "alice", nil); loc != "/" {
				t.Fatal(loc)
			}
		}
		checkNow(f.admB, f.tenB, "acme.example").want(t, 200, "")
	})
}

// A domain of a tenant without features.domains routes nowhere, and does not
// make the sign-in page ask for the address first.
func TestARouteOfATenantWithoutDomainsDoesNotAskFirst(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		f.orgProvider(t, f.admB, f.tenB, "acme-sso", nil)
		// The route to a site SSO, which stays usable without the feature.
		f.root.do("POST", "/api/admin/providers", idp.providerBody("corp", map[string]any{"enabled": true})).want(t, 201, "")
		idp.idp.RedirectURIs = append(idp.idp.RedirectURIs, testPublic+"/api/auth/oidc/corp/callback")
		f.route(t, f.admB, f.tenB, "acme.example", "corp")
		page := func() map[string]any {
			return a.newClient("").do("GET", "/api/auth/providers", nil).want(t, 200, "").body
		}
		if p := page(); p["identifierFirst"] != true {
			t.Fatal(p)
		}
		tok := f.root.do("POST", "/api/admin/confirm", map[string]any{"action": "change_capabilities", "key": "features.domains", "tenant": f.tenB}).want(t, 200, "").body["confirm"]
		f.root.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/features.domains", map[string]any{"value": false, "confirm": tok}).want(t, 200, "")
		if p := page(); p["identifierFirst"] != false || p["ssoEntry"] != false {
			t.Fatal(p)
		}
		if d := discovered(t, discover(a.newClient(""), "x@acme.example")); d != nil {
			t.Fatal(d)
		}
	})
}

// A plan's rule that turns signin.ssoOnly on is checked for each tenant on the
// plan, and for a tenant given the plan: each needs a single sign-on its
// people can use. The site's own checks (open registration, the site's
// administrators) are not what decides.
func TestAPlanThatRequiresSSOIsCheckedPerTenant(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		f.orgProvider(t, f.admB, f.tenB, "acme-sso", nil)
		on := map[string]any{"signin.ssoOnly": map[string]any{"value": true}}
		f.member(t, f.admB, "bm")
		f.root.do("POST", "/api/admin/plans", map[string]any{"slug": "strict", "name": "Strict"}).want(t, 201, "")
		f.root.do("PUT", "/api/admin/tenants/"+f.tenB+"/plan", map[string]any{"plan": "strict"}).want(t, 200, "")
		f.root.do("PUT", "/api/admin/tenants/"+f.tenC+"/plan", map[string]any{"plan": "strict"}).want(t, 200, "")
		// C has no single sign-on: the plan's rule is refused for it.
		f.root.do("PATCH", "/api/admin/plans/strict", map[string]any{"rules": on}).want(t, 400, "sso_provider_required")
		f.root.do("PUT", "/api/admin/tenants/"+f.tenC+"/plan", map[string]any{"plan": nil}).want(t, 200, "")
		f.root.do("PATCH", "/api/admin/plans/strict", map[string]any{"rules": on}).want(t, 200, "")
		// In effect for B: its member is refused a password.
		a.newClient("203.0.113.80").do("POST", "/api/login", map[string]any{"username": "bm", "password": pw}).want(t, 403, "local_login_refused")
		// Giving C the plan now is refused as well.
		f.root.do("PUT", "/api/admin/tenants/"+f.tenC+"/plan", map[string]any{"plan": "strict"}).want(t, 400, "sso_provider_required")
	})
}

// While a tenant's ssoOnly is on, its last enabled administrator with a
// password (the back door) cannot be demoted, disabled or deleted:
// 409 last_local_admin, as the site's 2.8 rule. Another with a password, or
// ssoOnly off, lets it go.
func TestTheLastPasswordAdministratorOfAnSSOOnlyTenantStays(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		f.orgProvider(t, f.admB, f.tenB, "acme-sso", nil)
		ssoOnly := func(v bool) {
			f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/signin.ssoOnly", map[string]any{"value": v}).want(t, 200, "")
		}
		// A second administrator of B, made while ssoOnly is off.
		inv := f.admB.do("POST", "/api/invites", map[string]any{"role": "admin"}).want(t, 201, "")
		a.newClient("").do("POST", "/api/invites/accept", map[string]any{"token": inv.body["token"], "username": "adm-b2", "name": "adm-b2", "password": pw}).want(t, 201, "")
		ssoOnly(true)
		admB, admB2 := mustUserID(t, a, "adm-b"), mustUserID(t, a, "adm-b2")
		// Two with a password: one may go.
		f.root.do("PATCH", "/api/users/"+admB2, map[string]any{"role": "member"}).want(t, 200, "")
		// The last one stays, however it would go.
		f.root.do("PATCH", "/api/users/"+admB, map[string]any{"role": "member"}).want(t, 409, "last_local_admin")
		f.root.do("PATCH", "/api/users/"+admB, map[string]any{"disabled": true}).want(t, 409, "last_local_admin")
		f.root.do("DELETE", "/api/users/"+admB, map[string]any{}).want(t, 409, "last_local_admin")
		// ssoOnly off: it may go.
		ssoOnly(false)
		f.root.do("PATCH", "/api/users/"+admB, map[string]any{"disabled": true}).want(t, 200, "")
	})
}

// The tenant's ssoOnly, for passkeys and resets: a member's passkey and reset
// link are refused, an administrator's passkey gets through.
func TestATenantsSSOOnlyRefusesMembersPasskeysAndResets(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		f := newSSOFx(t, a, idp)
		enableMail(t, f.root, srv)
		f.orgProvider(t, f.admB, f.tenB, "acme-sso", nil)
		bm := f.member(t, f.admB, "bm")
		mk, ak := newVKey(mustUserID(t, a, "bm")), newVKey(mustUserID(t, a, "adm-b"))
		bm.register(t, mk, "k").want(t, 201, "")
		f.admB.register(t, ak, "k").want(t, 201, "")
		// A reset link of the member, made before.
		if _, err := store.SetPrimaryEmail(context.Background(), a.st.Q(), mustUserID(t, a, "bm"), "bm@acme.example", true, "", store.PrimaryOpts{}); err != nil {
			t.Fatal(err)
		}
		forgot(a.newClient("203.0.113.70"), "bm").want(t, 202, "")
		tok := srv.Wait(1)[0].Token(t)
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/signin.ssoOnly", map[string]any{"value": true}).want(t, 200, "")
		mk.cred.Counter++
		a.newClient("").signInWithPasskey(t, mk, "", "").want(t, 403, "local_login_refused")
		ak.cred.Counter++
		a.newClient("").signInWithPasskey(t, ak, "", "").want(t, 200, "")
		a.newClient("").do("POST", "/api/password/reset", map[string]any{"token": tok, "password": "a brand new password"}).want(t, 404, "not_found")
	})
}

// signin.ssoOnlyAdmins: with it on, the tenant's administrators are held to
// its single sign-on like its members (password and password invite refused),
// the last-password-administrator guard does not apply, and a site
// administrator keeps the password. Off (the default), nothing changes.
func TestSSOOnlyAdminsHoldsAdministratorsToSSOToo(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		f.orgProvider(t, f.admB, f.tenB, "acme-sso", nil)
		put := func(key string, v bool) result {
			return f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/"+key, map[string]any{"value": v})
		}
		login := func(ip, user string) result {
			return a.newClient(ip).do("POST", "/api/login", map[string]any{"username": user, "password": pw})
		}
		put("signin.ssoOnly", true).want(t, 200, "")
		// Off by default: the administrator keeps the password, and is the last one.
		login("203.0.113.70", "adm-b").want(t, 200, "")
		admB := mustUserID(t, a, "adm-b")
		f.root.do("PATCH", "/api/users/"+admB, map[string]any{"role": "member"}).want(t, 409, "last_local_admin")
		// On: the administrator is refused like a member, and the guard is gone.
		put("signin.ssoOnlyAdmins", true).want(t, 200, "")
		login("203.0.113.71", "adm-b").want(t, 403, "local_login_refused")
		login("203.0.113.72", "root").want(t, 200, "")
		inv := f.admB.do("POST", "/api/invites", map[string]any{"role": "admin"}).want(t, 201, "")
		a.newClient("").do("POST", "/api/invites/accept", map[string]any{"token": inv.body["token"], "username": "adm-b9", "name": "adm-b9", "password": pw}).want(t, 403, "local_login_refused")
		f.root.do("PATCH", "/api/users/"+admB, map[string]any{"role": "member"}).want(t, 200, "")
	})
}
