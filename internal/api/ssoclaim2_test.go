package api_test

import (
	"testing"
	"time"

	"petalbook.app/server/internal/auth/testidp"
)

func waiting(t *testing.T, a *acc) int {
	t.Helper()
	return rowCount(t, a, `SELECT COUNT(*) FROM awaiting_sso`)
}

// A site SSO routed to the tenant for its verified domain claims, as the
// tenant's own does.
func TestASiteSSORoutedToTheTenantClaimsItsWaitingAccount(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	corp := newIDPFor(t, "corp")
	corp.addUser("carol", testidp.Claims{"sub": "s-carol", "preferred_username": "carol", "email": "carol@beta.example", "email_verified": true, "name": "Carol"})
	corp.idp.RedirectURIs = append(corp.idp.RedirectURIs, testPublic+"/api/auth/oidc/corp/callback")
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		f.root.do("POST", "/api/admin/providers", corp.providerBody("corp", map[string]any{"enabled": true, "tenants": []string{f.tenC}, "mergeVerifiedEmail": true})).want(t, 201, "")
		f.route(t, f.admC, f.tenC, "beta.example", "corp")
		f.admC.do("PUT", "/api/admin/tenants/"+f.tenC+"/settings/signin.ssoOnly", map[string]any{"value": true}).want(t, 200, "")
		setMode(t, f.admC, f.tenC, "closed")
		f.admC.do("POST", adminUsers, map[string]any{"username": "cm", "email": "carol@beta.example"}).want(t, 201, "")
		c := a.newClient("")
		if loc := corp.signIn2(t, c, "corp", "carol", nil); loc != "/" || signedInAs(t, c) != "cm" {
			t.Fatalf("%s %q", loc, signedInAs(t, c))
		}
		if waiting(t, a) != 0 {
			t.Fatal("still waiting")
		}
	})
}

// A site social provider never claims, though it vouches for the address.
func TestASiteSocialProviderNeverClaims(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	soc := newIDPFor(t, "soc")
	soc.addUser("alice", testidp.Claims{"sub": "s-alice", "preferred_username": "alice", "email": "alice@acme.example", "email_verified": true, "name": "Alice"})
	soc.idp.RedirectURIs = append(soc.idp.RedirectURIs, testPublic+"/api/auth/oidc/soc/callback")
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := ssoClaimFx(t, a, idp, "open")
		f.root.do("POST", "/api/admin/providers", soc.providerBody("soc", map[string]any{"enabled": true, "class": "social", "mergeVerifiedEmail": true})).want(t, 201, "")
		f.admB.do("POST", adminUsers, map[string]any{"username": "al", "email": "alice@acme.example"}).want(t, 201, "")
		c := a.newClient("")
		soc.signIn2(t, c, "soc", "alice", nil)
		if signedInAs(t, c) != "" || waiting(t, a) != 1 {
			t.Fatalf("claimed by a social provider: %q", signedInAs(t, c))
		}
		if u := userTenant(t, a, "al"); u.EmailVerified || u.Email != "alice@acme.example" {
			t.Fatalf("%+v", u)
		}
	})
}

// Another organisation's own SSO never claims, even where it vouches for the
// address (the domain moved to it after the account was made).
func TestAnotherTenantsSSONeverClaims(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := ssoClaimFx(t, a, idp, "open")
		f.admB.do("POST", adminUsers, map[string]any{"username": "al", "email": "alice@acme.example"}).want(t, 201, "")
		// acme.example leaves B and is verified by C, whose SSO then vouches for it.
		f.admB.do("DELETE", domainsPath(f.tenB)+"/acme.example", nil).want(t, 204, "")
		a.clock.Advance(time.Hour)
		f.verify(t, f.admC, f.tenC, "acme.example")
		f.orgProvider(t, f.admC, f.tenC, "beta-sso", nil)
		c := a.newClient("")
		idp.signIn2(t, c, "beta-sso", "alice", nil)
		if signedInAs(t, c) == "al" || waiting(t, a) != 1 {
			t.Fatalf("claimed by another tenant's SSO: %q", signedInAs(t, c))
		}
	})
}

// A disabled tenant claims nothing.
func TestADisabledTenantsWaitingAccountIsNotClaimed(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := ssoClaimFx(t, a, idp, "open")
		f.admB.do("POST", adminUsers, map[string]any{"username": "al", "email": "alice@acme.example"}).want(t, 201, "")
		f.root.do("PATCH", "/api/admin/tenants/"+f.tenB, map[string]any{"disabled": true}).want(t, 200, "")
		c := a.newClient("")
		idp.signIn2(t, c, "acme-sso", "alice", nil)
		if signedInAs(t, c) != "" || waiting(t, a) != 1 {
			t.Fatalf("claimed in a disabled tenant: %q", signedInAs(t, c))
		}
	})
}
