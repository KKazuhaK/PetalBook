package api_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/auth/testidp"
	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
)

// An organisation's own single sign-on (3.6 d3). Two organisations, B (acme)
// and C (beta), each with a verified domain, and one identity provider their
// providers share (the same issuer, one application per provider). The DNS is
// the fixture's fake; nothing waits.

type ssoFx struct {
	*domFx
	idp *fakeIDP
}

func newSSOFx(t *testing.T, a *acc, idp *fakeIDP) *ssoFx {
	t.Helper()
	f := &ssoFx{domFx: newDomFx(t, a), idp: idp}
	f.verify(t, f.admB, f.tenB, "acme.example")
	f.verify(t, f.admC, f.tenC, "beta.example")
	return f
}

// verify claims the domain for the tenant and verifies it by its TXT record.
func (f *ssoFx) verify(t *testing.T, c *client, tenant, domain string) {
	t.Helper()
	key, code := claim(t, c, tenant, domain)
	f.a.dns.SetTXT("_petalbook."+key, "petalbook-verify="+code)
	if r := checkNow(c, tenant, key).want(t, 200, ""); r.body["state"] != "verified" {
		t.Fatal(r.raw)
	}
}

func providersPath(tenant string) string { return "/api/admin/tenants/" + tenant + "/providers" }

// orgProvider makes the tenant's own OpenID Connect provider at the shared
// identity provider, as its administrator c, and returns its id.
func (f *ssoFx) orgProvider(t *testing.T, c *client, tenant, slug string, extra map[string]any) string {
	t.Helper()
	f.idp.idp.RedirectURIs = append(f.idp.idp.RedirectURIs, testPublic+"/api/auth/oidc/"+slug+"/callback")
	body := f.idp.providerBody(slug, map[string]any{"enabled": true})
	open := false
	for k, v := range extra {
		if k == "_open" {
			// Not a field of the provider: the organisation's registration is
			// opened, so that its sign-ins make accounts.
			open = v == true
			continue
		}
		body[k] = v
	}
	if open {
		c.do("PUT", "/api/admin/tenants/"+tenant+"/settings/registration.mode", map[string]any{"value": "open"}).want(t, 200, "")
	}
	r := c.do("POST", providersPath(tenant), body).want(t, 201, "")
	if r.body["owner"] != tenant || r.body["class"] != "sso" || r.body["enabled"] != true {
		t.Fatal(r.raw)
	}
	return r.body["id"].(string)
}

// member makes a member of the administrator c's tenant and signs them in.
func (f *ssoFx) member(t *testing.T, c *client, username string) *client {
	t.Helper()
	inv := c.do("POST", "/api/invites", map[string]any{"role": "member"}).want(t, 201, "")
	m := f.a.newClient("")
	m.do("POST", "/api/invites/accept", map[string]any{"token": inv.body["token"], "username": username, "name": username, "password": pw}).want(t, 201, "")
	return m
}

// link stores a sign-in link of the provider to the user, as a link made by a
// sign-in would be (or as a forged row would be).
func (f *ssoFx) link(t *testing.T, providerID, username, subject string) {
	t.Helper()
	now := f.a.clock.Now()
	if err := store.InsertLink(context.Background(), f.a.st.Q(), store.Link{ID: "L" + strings.ToUpper(username), ProviderID: providerID, UserID: mustUserID(t, f.a, username),
		Subject: subject, SignIn: true, Created: now, LastUsed: now}); err != nil {
		t.Fatal(err)
	}
}

func userTenant(t *testing.T, a *acc, username string) store.User {
	t.Helper()
	u, err := store.GetUser(context.Background(), a.st.Q(), store.AllTenants(), mustUserID(t, a, username))
	if err != nil {
		t.Fatal(err)
	}
	return *u
}

// refusedAudit is the login_failed row of a refusal not_allowed of username,
// as reader (an administrator of the account's tenant, or the site's) reads it.
func refusedAudit(t *testing.T, a *acc, reader *client, username string) map[string]any {
	t.Helper()
	a.auth.FlushAudit(context.Background())
	for _, r := range reader.audit(t, "login_failed") {
		d, _ := r["detail"].(map[string]any)
		if r["username"] == username && d["reason"] == "not_allowed" {
			return r
		}
	}
	t.Fatalf("no login_failed not_allowed for %s", username)
	return nil
}

func ssoIDP(t *testing.T) *fakeIDP {
	idp := newIDPFor(t, "acme-sso")
	idp.addUser("alice", testidp.Claims{"sub": "sub-alice", "preferred_username": "alice", "email": "alice@acme.example", "name": "Alice"})
	idp.addUser("carol", testidp.Claims{"sub": "sub-carol", "preferred_username": "carol", "email": "carol@beta.example", "name": "Carol"})
	return idp
}

// Two organisations on the same issuer, the same person at it: each
// organisation's provider makes and signs in to its own account, never the
// other's.
func TestTwoTenantsOnTheSameIssuerStayApart(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		f.orgProvider(t, f.admB, f.tenB, "acme-sso", map[string]any{"_open": true})
		f.orgProvider(t, f.admC, f.tenC, "beta-sso", map[string]any{"_open": true})
		b, c := a.newClient(""), a.newClient("")
		if loc := idp.signIn2(t, b, "acme-sso", "alice", nil); loc != "/" {
			t.Fatal(loc)
		}
		if loc := idp.signIn2(t, c, "beta-sso", "alice", nil); loc != "/" {
			t.Fatal(loc)
		}
		ub, uc := signedInAs(t, b), signedInAs(t, c)
		if ub == "" || uc == "" || ub == uc {
			t.Fatalf("one account for two organisations: %q %q", ub, uc)
		}
		if userTenant(t, a, ub).TenantID != f.tenB || userTenant(t, a, uc).TenantID != f.tenC {
			t.Fatal("an account in the wrong organisation")
		}
		// Each signs in again to its own.
		b2, c2 := a.newClient(""), a.newClient("")
		idp.signIn2(t, b2, "acme-sso", "alice", nil)
		idp.signIn2(t, c2, "beta-sso", "alice", nil)
		if signedInAs(t, b2) != ub || signedInAs(t, c2) != uc {
			t.Fatal("a sign-in reached the other organisation's account")
		}
		// Each administrator sees their own provider only.
		if l := f.admB.do("GET", providersPath(f.tenB), nil).want(t, 200, "").body["providers"].([]any); len(l) != 1 || l[0].(map[string]any)["slug"] != "acme-sso" {
			t.Fatal(l)
		}
	})
}

// An open mode makes the account in the provider's tenant: a member, never an
// administrator, whatever the provider's jit tenant would say.
func TestAnOpenOrganisationSignInLandsInTheProvidersTenant(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		// The jit tenant of a tenant's provider is its own: another is refused.
		f.admC.do("POST", providersPath(f.tenC), idp.providerBody("beta-sso", map[string]any{"tenant": f.tenB})).want(t, 400, "invalid")
		id := f.orgProvider(t, f.admC, f.tenC, "beta-sso", map[string]any{"_open": true})
		if p := f.admC.do("GET", providersPath(f.tenC)+"/"+id, nil).want(t, 200, ""); p.body["tenant"] != f.tenC {
			t.Fatal(p.raw)
		}
		c := a.newClient("")
		if loc := idp.signIn2(t, c, "beta-sso", "carol", nil); loc != "/" {
			t.Fatal(loc)
		}
		u := userTenant(t, a, signedInAs(t, c))
		if u.TenantID != f.tenC || u.Role != "member" || u.SiteAdmin {
			t.Fatalf("%+v", u)
		}
		// Its address is on C's verified domain: the provider vouches for it.
		if !u.EmailVerified || u.Email != "carol@beta.example" {
			t.Fatalf("%+v", u)
		}
	})
}

// A link row that names an account of another tenant (written by hand, or
// by a bug) signs nobody in: the account is checked against the provider's
// tenant at every sign-in.
func TestAForgedLinkRowCannotCrossTenants(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		id := f.orgProvider(t, f.admB, f.tenB, "acme-sso", nil)
		f.member(t, f.admC, "cm")
		f.link(t, id, "cm", "sub-alice")
		c := a.newClient("")
		if loc := idp.signIn2(t, c, "acme-sso", "alice", nil); loc != "/login?sso_error=not_allowed" || signedInAs(t, c) != "" {
			t.Fatalf("%s %q", loc, signedInAs(t, c))
		}
		// The refusal is written to the account's tenant: its administrators read it.
		refusedAudit(t, a, f.admC, "cm")
		refusedAudit(t, a, f.root, "cm")
	})
}

// A member of one organisation neither links nor signs in through another's
// provider; one of its own does.
func TestAMemberOfOneTenantCannotUseAnothersProvider(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		id := f.orgProvider(t, f.admB, f.tenB, "acme-sso", nil)
		cm := f.member(t, f.admC, "cm")
		// The link is not offered to C's member: no such provider for them.
		cm.do("POST", "/api/account/links/"+id+"/start", map[string]any{"return": "/settings"}).want(t, 404, "not_found")
		// A link started at the public route is refused once the provider answers.
		if loc := idp.signIn2(t, cm, "acme-sso", "alice", map[string]any{"intent": "link", "return": "/settings"}); loc != "/settings?sso_error=not_allowed" {
			t.Fatal(loc)
		}
		if _, err := store.FindLink(context.Background(), a.st.Q(), id, "sub-alice"); err == nil {
			t.Fatal("the link was made")
		}
		// B's member links it and signs in with it.
		bm := f.member(t, f.admB, "bm")
		bm.do("POST", "/api/account/links/"+id+"/start", map[string]any{"return": "/settings"}).want(t, 200, "")
		if loc := idp.signIn2(t, bm, "acme-sso", "alice", map[string]any{"intent": "link", "return": "/settings"}); loc != "/settings" {
			t.Fatal(loc)
		}
		c := a.newClient("")
		idp.signIn2(t, c, "acme-sso", "alice", nil)
		if signedInAs(t, c) != "bm" {
			t.Fatal(signedInAs(t, c))
		}
	})
}

// A tenant's provider never signs in a site administrator, even one of that
// tenant whose identity is linked: the tenant's administrators control it.
func TestASiteAdministratorIsRefusedThroughATenantProvider(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		id := f.orgProvider(t, f.admB, f.tenB, "acme-sso", nil)
		if _, err := a.auth.CreateUser(context.Background(), auth.CreateParams{Username: "boss", Role: "admin", Password: pw, TenantID: f.tenB, SiteAdmin: true}); err != nil {
			t.Fatal(err)
		}
		f.link(t, id, "boss", "sub-alice")
		c := a.newClient("")
		if loc := idp.signIn2(t, c, "acme-sso", "alice", nil); loc != "/login?sso_error=not_allowed" || signedInAs(t, c) != "" {
			t.Fatalf("%s %q", loc, signedInAs(t, c))
		}
		refusedAudit(t, a, f.admB, "boss")
		// Nor does the site administrator link it.
		boss := a.newClient("")
		boss.do("POST", "/api/login", map[string]any{"username": "boss", "password": pw}).want(t, 200, "")
		boss.do("POST", "/api/account/links/"+id+"/start", map[string]any{"return": "/settings"}).want(t, 404, "not_found")
	})
}

// Without features.ownSso a tenant's administrators get feature_off, and its
// providers sign nobody in; the site administrator still sees them.
func TestOwnSSOOffIsFeatureOff(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		f.root.do("PUT", "/api/admin/tenants/"+f.tenC+"/settings/features.ownSso", map[string]any{"siteRule": map[string]any{"value": false, "lower": map[string]any{"mode": "lock"}}}).want(t, 200, "")
		f.admC.do("GET", providersPath(f.tenC), nil).want(t, 403, "feature_off")
		f.admC.do("POST", providersPath(f.tenC), idp.providerBody("beta-sso", nil)).want(t, 403, "feature_off")
		f.admC.do("PATCH", domainsPath(f.tenC)+"/beta.example", map[string]any{"provider": nil}).want(t, 403, "feature_off")
		// The domain's own field is the domain feature's, as before.
		f.admC.do("PATCH", domainsPath(f.tenC)+"/beta.example", map[string]any{"restrictOrgs": true}).want(t, 200, "")
		f.root.do("GET", providersPath(f.tenC), nil).want(t, 200, "")
		// A provider the site administrator made for C stays off.
		f.root.do("POST", providersPath(f.tenC), idp.providerBody("beta-sso", map[string]any{"enabled": true})).want(t, 403, "feature_off")
	})
}

// Group mapping of a tenant's provider names its own tenant's groups only.
func TestGroupMappingCannotTargetAnotherTenant(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		id := f.orgProvider(t, f.admB, f.tenB, "acme-sso", nil)
		gb := f.admB.mkGroup(t, "Staff B", "synced")
		gc := f.admC.mkGroup(t, "Staff C", "synced")
		p := providersPath(f.tenB) + "/" + id
		f.admB.do("PATCH", p, map[string]any{"groups": map[string]any{"enabled": true, "map": gmap("staff", gc)}}).want(t, 404, "group_not_found")
		// Not through the site's route either, by the site's administrator.
		f.root.do("PATCH", "/api/admin/providers/"+id, map[string]any{"groups": map[string]any{"enabled": true, "map": gmap("staff", gc)}}).want(t, 404, "group_not_found")
		r := f.admB.do("PATCH", p, map[string]any{"groups": map[string]any{"enabled": true, "map": gmap("staff", gb)}}).want(t, 200, "")
		if m := r.body["groups"].(map[string]any)["map"].([]any); len(m) != 1 {
			t.Fatal(r.raw)
		}
	})
}

// The identity provider's group allowlist is asked at every sign-in: who it
// takes out of the groups signs in no more, and a missing claim lets nobody in.
func TestTheGroupAllowlistIsCheckedAtEverySignIn(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	idp.addUser("dave", testidp.Claims{"sub": "sub-dave", "preferred_username": "dave", "email": "dave@acme.example", "groups": []string{"staff", "x"}})
	idp.addUser("erin", testidp.Claims{"sub": "sub-erin", "preferred_username": "erin", "email": "erin@acme.example", "groups": []string{"x"}})
	idp.addUser("nogroups", testidp.Claims{"sub": "sub-ng", "preferred_username": "ng", "email": "ng@acme.example"})
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		idp.idp.Users["dave"]["groups"] = []string{"staff", "x"}
		f := newSSOFx(t, a, idp)
		id := f.orgProvider(t, f.admB, f.tenB, "acme-sso", map[string]any{"_open": true, "groups": map[string]any{"allow": []string{"staff"}}})
		if g := f.admB.do("GET", providersPath(f.tenB)+"/"+id, nil).want(t, 200, "").body["groups"].(map[string]any); strs(g["allow"])[0] != "staff" || g["enabled"] != false {
			t.Fatal(g)
		}
		c := a.newClient("")
		if loc := idp.signIn2(t, c, "acme-sso", "dave", nil); loc != "/" || signedInAs(t, c) != "dave" {
			t.Fatal(loc)
		}
		for _, who := range []string{"erin", "nogroups"} {
			c := a.newClient("")
			if loc := idp.signIn2(t, c, "acme-sso", who, nil); loc != "/login?sso_error=not_allowed" || signedInAs(t, c) != "" {
				t.Fatalf("%s: %s", who, loc)
			}
		}
		// Dave leaves the group at the identity provider: his account stays, he signs in no more.
		idp.idp.Users["dave"]["groups"] = []string{"x"}
		c = a.newClient("")
		if loc := idp.signIn2(t, c, "acme-sso", "dave", nil); loc != "/login?sso_error=not_allowed" || signedInAs(t, c) != "" {
			t.Fatal(loc)
		}
		mustUserID(t, a, "dave")
	})
}

// A site SSO provider offered to a list of tenants signs in, makes and lists
// for those tenants only; offered to all again, it does for everyone.
func TestASiteProviderOfferedToAList(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	idp.idp.RedirectURIs = append(idp.idp.RedirectURIs, testPublic+"/api/auth/oidc/corp/callback")
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		r := f.root.do("POST", "/api/admin/providers", idp.providerBody("corp", map[string]any{"tenants": []string{f.tenB}, "tenant": f.tenC})).want(t, 201, "")
		if ts := strs(r.body["tenants"]); len(ts) != 1 || ts[0] != f.tenB {
			t.Fatal(r.raw)
		}
		// A social provider is not offered by a list (signin.social prunes it).
		f.root.do("POST", "/api/admin/providers", idp.providerBody("soc", map[string]any{"class": "social", "tenants": []string{f.tenB}})).want(t, 400, "invalid")
		f.member(t, f.admB, "bm")
		f.member(t, f.admC, "cm")
		f.link(t, "corp", "bm", "sub-alice")
		f.link(t, "corp", "cm", "sub-carol")
		b, c := a.newClient(""), a.newClient("")
		if loc := idp.signIn2(t, b, "corp", "alice", nil); loc != "/" || signedInAs(t, b) != "bm" {
			t.Fatal(loc)
		}
		if loc := idp.signIn2(t, c, "corp", "carol", nil); loc != "/login?sso_error=not_allowed" {
			t.Fatal(loc)
		}
		// an account in C, which is not on the list: refused.
		idp.addUser("frank", testidp.Claims{"sub": "sub-frank", "preferred_username": "frank"})
		if loc := idp.signIn2(t, a.newClient(""), "corp", "frank", nil); loc != "/login?sso_error=not_allowed" {
			t.Fatal(loc)
		}
		// The sign-in pages: B's lists it, C's does not; the site's does.
		listed := func(q string) bool {
			for _, p := range a.newClient("").do("GET", "/api/auth/providers"+q, nil).want(t, 200, "").body["providers"].([]any) {
				if p.(map[string]any)["slug"] == "corp" {
					return true
				}
			}
			return false
		}
		if !listed("?tenant=acme") || listed("?tenant=beta") || !listed("") {
			t.Fatal("the lists")
		}
		// To all.
		f.root.do("PATCH", "/api/admin/providers/corp", map[string]any{"tenants": "all"}).want(t, 200, "")
		c = a.newClient("")
		if loc := idp.signIn2(t, c, "corp", "carol", nil); loc != "/" || signedInAs(t, c) != "cm" {
			t.Fatal(loc)
		}
		if !listed("?tenant=beta") {
			t.Fatal("offered to all")
		}
	})
}

// signin.social's tenant layer: an identity of a social provider the
// account's tenant does not allow is refused once it is resolved.
func TestSigninSocialRefusesWhatTheTenantDoesNotAllow(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	idp.idp.RedirectURIs = append(idp.idp.RedirectURIs, testPublic+"/api/auth/oidc/soc/callback")
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		f.root.do("POST", "/api/admin/providers", idp.providerBody("soc", map[string]any{"class": "social"})).want(t, 201, "")
		f.member(t, f.admB, "bm")
		f.member(t, f.admC, "cm")
		f.link(t, "soc", "bm", "sub-alice")
		f.link(t, "soc", "cm", "sub-carol")
		// C keeps only another provider.
		f.admC.do("PUT", "/api/admin/tenants/"+f.tenC+"/settings/signin.social", map[string]any{"value": []string{"github"}}).want(t, 200, "")
		f.admC.do("PUT", "/api/admin/tenants/"+f.tenC+"/settings/signin.social", map[string]any{"value": []string{"Not A Slug"}}).want(t, 400, "")
		c := a.newClient("")
		if loc := idp.signIn2(t, c, "soc", "carol", nil); loc != "/login?sso_error=not_allowed" || signedInAs(t, c) != "" {
			t.Fatal(loc)
		}
		refusedAudit(t, a, f.admC, "cm")
		b := a.newClient("")
		if loc := idp.signIn2(t, b, "soc", "alice", nil); loc != "/" || signedInAs(t, b) != "bm" {
			t.Fatal(loc)
		}
		pub := a.newClient("").do("GET", "/api/auth/providers?tenant=beta", nil).want(t, 200, "")
		// None for C; the mode is still the site's, as the server enforces it.
		if len(pub.body["providers"].([]any)) != 0 || pub.body["loginMode"] != "dual" {
			t.Fatal(pub.raw)
		}
		// A site lock for C (a rule) puts it back, whatever C says.
		f.root.do("PUT", "/api/admin/tenants/"+f.tenC+"/settings/signin.social", map[string]any{"siteRule": map[string]any{"value": []string{"*"}, "lower": map[string]any{"mode": "lock"}}}).want(t, 200, "")
		c = a.newClient("")
		if loc := idp.signIn2(t, c, "soc", "carol", nil); loc != "/" || signedInAs(t, c) != "cm" {
			t.Fatal(loc)
		}
	})
}

// Switching features.ownSso off while a tenant has its own providers goes
// through the confirmation of change_capabilities, as features.domains does:
// the site's switch, a rule (dated too), a plan and billing. Once off, its
// providers stop; nothing is deleted.
func TestOwnSSOOffNeedsTheConfirmation(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		id := f.orgProvider(t, f.admB, f.tenB, "acme-sso", map[string]any{"_open": true})
		rule := "/api/admin/tenants/" + f.tenB + "/settings/features.ownSso"

		// A dated rule that switches it off later.
		later := a.clock.Now().Add(48 * time.Hour).Format(time.RFC3339)
		f.root.do("PUT", rule, map[string]any{"siteRule": map[string]any{"value": false, "from": later}}).want(t, 409, "confirm_required")
		// The site's switch: the preview names B, its providers and how many are on.
		r := f.root.putSetting("features.ownSso", false).want(t, 409, "confirm_required")
		p, _ := r.body["plan"].([]any)
		if len(p) != 1 || p[0].(map[string]any)["tenant"] != f.tenB || p[0].(map[string]any)["providers"] != float64(1) || p[0].(map[string]any)["enabled"] != float64(1) {
			t.Fatalf("the preview: %s", r.raw)
		}
		// A plan that has it off, given to B.
		off := map[string]any{"features.ownSso": map[string]any{"value": false, "lower": map[string]any{"mode": "lock"}}}
		f.root.do("POST", "/api/admin/plans", map[string]any{"slug": "basic", "name": "Basic", "rules": off}).want(t, 201, "")
		f.root.do("PUT", "/api/admin/tenants/"+f.tenB+"/plan", map[string]any{"plan": "basic"}).want(t, 409, "confirm_required")
		// Billing is refused.
		if err := settings.Of(a.st).SetBilling(t.Context(), f.tenB, "billing", map[string]*settings.Rule{"features.ownSso": {Value: false}}); err == nil {
			t.Fatal("billing switched it off")
		} else if se, ok := err.(*settings.Error); !ok || se.Code != "confirm_required" {
			t.Fatalf("billing: %v", err)
		}
		// C has no provider: its rule is direct.
		f.root.do("PUT", "/api/admin/tenants/"+f.tenC+"/settings/features.ownSso", map[string]any{"siteRule": map[string]any{"value": false}}).want(t, 200, "")

		// With the confirmation: off for B, and its provider stops.
		tok := f.root.do("POST", "/api/admin/confirm", map[string]any{"action": "change_capabilities", "key": "features.ownSso", "tenant": f.tenB}).want(t, 200, "").body["confirm"]
		f.root.do("PUT", rule, map[string]any{"siteRule": map[string]any{"value": false}, "confirm": tok}).want(t, 200, "")
		// Its sign-in does not start, and one under way does not finish.
		a.newClient("").do("POST", "/api/auth/oidc/acme-sso/start", map[string]any{}).want(t, 404, "not_found")
		f.admB.do("GET", providersPath(f.tenB), nil).want(t, 403, "feature_off")
		// Nothing was deleted: on again, it signs in as it did.
		f.root.do("PUT", rule, map[string]any{"siteRule": nil}).want(t, 200, "")
		if v := f.root.do("GET", providersPath(f.tenB)+"/"+id, nil).want(t, 200, ""); v.body["enabled"] != true {
			t.Fatal(v.raw)
		}
		c := a.newClient("")
		if loc := idp.signIn2(t, c, "acme-sso", "alice", nil); loc != "/" {
			t.Fatal(loc)
		}
	})
}

// A verified domain routes to one of its tenant's own providers (the data
// discovery reads in d4); another tenant's provider, or an unverified domain,
// is refused, and deleting the provider clears the route.
func TestADomainRoutesToItsTenantsProvider(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		idB := f.orgProvider(t, f.admB, f.tenB, "acme-sso", nil)
		idC := f.orgProvider(t, f.admC, f.tenC, "beta-sso", nil)
		d := domainsPath(f.tenB) + "/acme.example"
		f.admB.do("PATCH", d, map[string]any{"provider": idC}).want(t, 404, "not_found")
		f.admB.do("PATCH", d, map[string]any{"provider": "corp"}).want(t, 404, "not_found")
		r := f.admB.do("PATCH", d, map[string]any{"provider": idB}).want(t, 200, "")
		if r.body["provider"] != idB {
			t.Fatal(r.raw)
		}
		if p := f.admB.do("GET", providersPath(f.tenB)+"/"+idB, nil).want(t, 200, ""); strs(p.body["domains"])[0] != "acme.example" {
			t.Fatal(p.raw)
		}
		claim(t, f.admB, f.tenB, "other-acme.example")
		f.admB.do("PATCH", domainsPath(f.tenB)+"/other-acme.example", map[string]any{"provider": idB}).want(t, 409, "domain_required")
		f.admB.do("DELETE", providersPath(f.tenB)+"/"+idB, nil).want(t, 204, "")
		if r := f.admB.do("GET", domainsPath(f.tenB), nil).want(t, 200, ""); strings.Contains(r.raw, idB) {
			t.Fatal(r.raw)
		}
		f.admB.do("PATCH", d, map[string]any{"provider": nil}).want(t, 200, "")
	})
}

// An organisation's provider is OpenID Connect or SAML, made by its
// administrators without a template; the site's own are not theirs to touch.
func TestATenantsAdministratorsManageOnlyItsOwnSSO(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		f.admB.do("POST", providersPath(f.tenB), map[string]any{"template": "google", "slug": "g", "clientId": "x", "clientSecret": "y"}).want(t, 400, "invalid")
		f.admB.do("POST", providersPath(f.tenB), map[string]any{"slug": "gh", "kind": "github", "name": "GH"}).want(t, 400, "invalid")
		f.admB.do("POST", providersPath(f.tenB), idp.providerBody("acme-sso", map[string]any{"owner": f.tenC})).want(t, 400, "invalid")
		f.admB.do("POST", providersPath(f.tenB), idp.providerBody("acme-sso", map[string]any{"mergeVerifiedEmail": true})).want(t, 400, "invalid")
		id := f.orgProvider(t, f.admB, f.tenB, "acme-sso", nil)
		f.admB.do("GET", "/api/admin/providers", nil).want(t, 403, "forbidden")
		f.admB.do("PATCH", "/api/admin/providers/"+id, map[string]any{"name": "x"}).want(t, 403, "forbidden")
		f.admB.do("PATCH", providersPath(f.tenB)+"/"+id, map[string]any{"tenants": "all"}).want(t, 400, "invalid")
		// Its audit rows are the tenant's.
		a.auth.FlushAudit(context.Background())
		if rows := f.admB.audit(t, "tenant_provider_created"); len(rows) != 1 {
			t.Fatalf("%v", rows)
		}
		if rows := f.root.audit(t, "provider_created"); len(rows) != 0 {
			t.Fatalf("a tenant's provider is no site provider: %v", rows)
		}
	})
}

// A tenant's provider vouches only for addresses on its own tenant's verified
// domains, each exactly: another tenant's domain, or a subdomain, is not.
func TestATenantsSSOTrustsOnlyItsOwnVerifiedDomains(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	idp.addUser("sub", testidp.Claims{"sub": "sub-sub", "preferred_username": "subby", "email": "subby@eu.acme.example", "email_verified": true})
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		f.orgProvider(t, f.admB, f.tenB, "acme-sso", map[string]any{"_open": true})
		for who, want := range map[string]bool{"alice": true, "carol": false, "sub": false} {
			c := a.newClient("")
			if loc := idp.signIn2(t, c, "acme-sso", who, nil); loc != "/" {
				t.Fatalf("%s: %s", who, loc)
			}
			if u := userTenant(t, a, signedInAs(t, c)); u.EmailVerified != want || u.TenantID != f.tenB {
				t.Fatalf("%s: %+v", who, u)
			}
		}
	})
}
