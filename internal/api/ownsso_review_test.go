package api_test

import (
	"context"
	"testing"

	"petalbook.app/server/internal/auth/testidp"
	"petalbook.app/server/internal/store"
)

// The review of 3.6 d3: each finding as a test.

// A group sync through a tenant's provider never puts anyone in a group that
// holds a role of the site: the "no site key" check passed before the sync,
// so the sync must not hand one out behind it.
func TestAGroupSyncNeverGivesASiteKey(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	idp.addUser("gina", testidp.Claims{"sub": "sub-gina", "preferred_username": "gina", "email": "gina@acme.example", "groups": []string{"staff", "crew"}})
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		ctx := context.Background()
		f := newSSOFx(t, a, idp)
		id := f.orgProvider(t, f.admB, f.tenB, "acme-sso", map[string]any{"_open": true})
		ops := f.admB.mkGroup(t, "Ops B", "synced")
		crew := f.admB.mkGroup(t, "Crew B", "synced")
		f.admB.do("PATCH", providersPath(f.tenB)+"/"+id, map[string]any{"groups": map[string]any{"enabled": true, "map": gmap("staff", ops, "crew", crew)}}).want(t, 200, "")
		// A site administrator gives the group a role of the site afterwards.
		now := a.clock.Now()
		if err := a.st.InsertRole(ctx, a.st.Q(), store.Role{ID: "ROLESITE01", Scope: store.RoleScopeSite, Name: "ops", Keys: []string{"site.status.read"}, Created: now, Updated: now}); err != nil {
			t.Fatal(err)
		}
		if err := store.InsertRoleGrant(ctx, a.st.Q(), store.RoleGrant{RoleID: "ROLESITE01", SubjectKind: store.GrantToGroup, SubjectID: ops}); err != nil {
			t.Fatal(err)
		}
		c := a.newClient("")
		if loc := idp.signIn2(t, c, "acme-sso", "gina", nil); loc != "/" {
			t.Fatal(loc)
		}
		ids, err := store.UserGroupIDs(ctx, a.st.Q(), mustUserID(t, a, "gina"))
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != 1 || ids[0] != crew {
			t.Fatalf("the sync put gina in %v (ops holds a site role)", ids)
		}
		c.do("GET", "/api/admin/status", nil).want(t, 403, "forbidden")
	})
}

// A routing the PATCH refuses changes nothing else the PATCH said.
func TestARefusedDomainPatchChangesNothing(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		id := f.orgProvider(t, f.admB, f.tenB, "acme-sso", nil)
		claim(t, f.admB, f.tenB, "later-acme.example")
		d := domainsPath(f.tenB) + "/later-acme.example"
		f.admB.do("PATCH", d, map[string]any{"restrictOrgs": true, "provider": id}).want(t, 409, "domain_required")
		f.admB.do("PATCH", d, map[string]any{"restrictOrgs": true, "provider": "0000000ZZZ"}).want(t, 404, "not_found")
		for _, it := range f.admB.do("GET", domainsPath(f.tenB), nil).want(t, 200, "").body["domains"].([]any) {
			if m := it.(map[string]any); m["domain"] == "later-acme.example" && (m["restrictOrgs"] != false || m["provider"] != nil) {
				t.Fatalf("a refused PATCH changed the claim: %v", m)
			}
		}
	})
}

// A site administrator always signs in through the site's own SSO, whatever
// its list of tenants says: they cannot lock themselves out with it.
func TestASiteAdministratorUsesASiteProviderWhateverItsList(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	idp.idp.RedirectURIs = append(idp.idp.RedirectURIs, testPublic+"/api/auth/oidc/corp/callback")
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		f.root.do("POST", "/api/admin/providers", idp.providerBody("corp", map[string]any{"tenants": []string{f.tenB}})).want(t, 201, "")
		f.link(t, "corp", "root", "sub-alice")
		c := a.newClient("")
		if loc := idp.signIn2(t, c, "corp", "alice", nil); loc != "/" || signedInAs(t, c) != "root" {
			t.Fatalf("%s %q", loc, signedInAs(t, c))
		}
	})
}

// A tenant's provider slug starts with its tenant's slug and "-": plain names
// (sso, okta) stay the site's to take.
func TestATenantsProviderSlugStartsWithItsTenantsSlug(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		for _, slug := range []string{"okta", "sso", "acme", "acmeokta", "beta-okta"} {
			if r := f.admB.do("POST", providersPath(f.tenB), idp.providerBody(slug, nil)); r.code != 400 || r.body["error"] != "invalid" {
				t.Fatalf("%s: %s", slug, r.raw)
			}
		}
		f.root.do("POST", "/api/admin/providers", idp.providerBody("okta", map[string]any{"owner": f.tenB})).want(t, 400, "invalid")
		f.orgProvider(t, f.admB, f.tenB, "acme-okta", nil)
		// The site's own take any free slug.
		f.root.do("POST", "/api/admin/providers", idp.providerBody("okta", nil)).want(t, 201, "")
	})
}

// The sign-in page of a tenant whose people may use no provider follows the
// site's mode: the hint is what the server enforces, not a looser one.
func TestTheSignInPageOfATenantWithoutProvidersFollowsTheSitesMode(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	idp.idp.RedirectURIs = append(idp.idp.RedirectURIs, testPublic+"/api/auth/oidc/soc/callback")
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		f.root.do("POST", "/api/admin/providers", idp.providerBody("soc", map[string]any{"class": "social"})).want(t, 201, "")
		f.root.putSetting("signin.mode", "sso_redirect").want(t, 200, "")
		f.admC.do("PUT", "/api/admin/tenants/"+f.tenC+"/settings/signin.social", map[string]any{"value": []string{"github"}}).want(t, 200, "")
		pub := a.newClient("").do("GET", "/api/auth/providers?tenant=beta", nil).want(t, 200, "")
		if len(pub.body["providers"].([]any)) != 0 || pub.body["loginMode"] != "sso_redirect" || pub.body["local"] != false || pub.body["sso"] != true {
			t.Fatal(pub.raw)
		}
	})
}
