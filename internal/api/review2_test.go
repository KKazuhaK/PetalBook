package api_test

import (
	"context"
	"strings"
	"testing"
	"unicode"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/auth/testidp"
	"petalbook.app/server/internal/store"
)

// signin.ssoOnlyAdmins is read as ssoOnly is: the tenant's own layer, or the
// site's rule for it. The site's own layer alone holds no tenant's administrators.
func TestTheSitesOwnSSOOnlyAdminsValueHoldsNoTenantsAdministrators(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		f.orgProvider(t, f.admB, f.tenB, "acme-sso", nil)
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/signin.ssoOnly", map[string]any{"value": true}).want(t, 200, "")
		f.root.putSetting("signin.ssoOnlyAdmins", true).want(t, 200, "")
		a.newClient("203.0.113.80").do("POST", "/api/login", map[string]any{"identifier": "adm-b", "password": pw}).want(t, 200, "")
		// The site's rule for the one tenant does hold it.
		f.root.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/signin.ssoOnlyAdmins", map[string]any{"siteRule": map[string]any{"value": true}}).want(t, 200, "")
		a.newClient("203.0.113.81").do("POST", "/api/login", map[string]any{"identifier": "adm-b", "password": pw}).want(t, 403, "local_login_refused")
	})
}

// With ssoOnlyAdmins on, a tenant administrator's passkey, reset link and the
// site's social sign-in are refused as a member's are; a site administrator of
// the tenant keeps the password.
func TestSSOOnlyAdminsRefusesPasskeyResetAndSocialToATenantAdministrator(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	idp.idp.RedirectURIs = append(idp.idp.RedirectURIs, testPublic+"/api/auth/oidc/soc/callback")
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		f := newSSOFx(t, a, idp)
		enableMail(t, f.root, srv)
		f.orgProvider(t, f.admB, f.tenB, "acme-sso", nil)
		f.root.do("POST", "/api/admin/providers", idp.providerBody("soc", map[string]any{"enabled": true, "class": "social"})).want(t, 201, "")
		f.link(t, "soc", "adm-b", "sub-alice")
		// A second administrator of B who is also a site administrator.
		inv := f.admB.do("POST", "/api/invites", map[string]any{"role": "admin"}).want(t, 201, "")
		a.newClient("").do("POST", "/api/invites/accept", map[string]any{"token": inv.body["token"], "username": "adm-b2", "name": "adm-b2", "password": pw}).want(t, 201, "")
		if _, err := a.st.Q().Exec(context.Background(), `UPDATE users SET site_admin = 1 WHERE username = 'adm-b2'`); err != nil {
			t.Fatal(err)
		}
		ak := newVKey(mustUserID(t, a, "adm-b"))
		f.admB.register(t, ak, "k").want(t, 201, "")
		if _, err := store.SetPrimaryEmail(context.Background(), a.st.Q(), mustUserID(t, a, "adm-b"), "adm-b@acme.example", true, "", store.PrimaryOpts{}); err != nil {
			t.Fatal(err)
		}
		forgot(a.newClient("203.0.113.82"), "adm-b").want(t, 202, "")
		tok := srv.Wait(1)[0].Token(t)

		put := func(key string) {
			f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/"+key, map[string]any{"value": true}).want(t, 200, "")
		}
		put("signin.ssoOnly")
		put("signin.ssoOnlyAdmins")
		ak.cred.Counter++
		a.newClient("").signInWithPasskey(t, ak, "", "").want(t, 403, "local_login_refused")
		a.newClient("").do("POST", "/api/password/reset", map[string]any{"token": tok, "password": "a brand new password"}).want(t, 404, "not_found")
		if loc := idp.signIn2(t, a.newClient(""), "soc", "alice", nil); loc != "/login?sso_error=sso_required" {
			t.Fatal(loc)
		}
		a.newClient("203.0.113.83").do("POST", "/api/login", map[string]any{"identifier": "adm-b", "password": pw}).want(t, 403, "local_login_refused")
		a.newClient("203.0.113.84").do("POST", "/api/login", map[string]any{"identifier": "adm-b2", "password": pw}).want(t, 200, "")
	})
}

// A social request to join: the approval's account takes the number the
// username needed, the mail names that account, and the mail is in the
// language of the sign-in; the approval is refused when the tenant has turned
// the provider away meanwhile, when it requires its own SSO, when the provider
// is gone, or when the identity was linked elsewhere.
func TestApprovingASocialRequestToJoin(t *testing.T) {
	t.Parallel()
	idp := newIDPFor(t, "soc")
	for _, k := range []string{"bob", "cat", "dot", "eve"} {
		idp.addUser(k, testidp.Claims{"sub": "sub-" + k, "preferred_username": k, "email": k + "@acme.example", "email_verified": true, "name": k})
	}
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newJoinFx(t, a)
		sf := &ssoFx{domFx: f.domFx, idp: idp}
		f.root.do("POST", "/api/admin/providers", idp.providerBody("soc", map[string]any{"enabled": true, "class": "social", "mergeVerifiedEmail": true})).want(t, 201, "")
		setJoin(f.admB, f.tenB, "acme.example", "request").want(t, 200, "")
		ask := func(who, lang string) string {
			t.Helper()
			c := a.newClient("")
			if lang != "" {
				c.headers = map[string]string{"Accept-Language": lang}
			}
			loc := idp.signIn2(t, c, "soc", who, nil)
			if !strings.HasPrefix(loc, "/register/waiting#") {
				t.Fatalf("%s: %s", who, loc)
			}
			return loc
		}
		pending := func(who string) string {
			t.Helper()
			for _, it := range f.admB.do("GET", joinPath(f.tenB), nil).want(t, 200, "").body["items"].([]any) {
				if m := it.(map[string]any); m["username"] == who {
					return m["id"].(string)
				}
			}
			t.Fatalf("no request of %s", who)
			return ""
		}
		approve := func(id string) result {
			return f.admB.do("POST", joinPath(f.tenB)+"/"+id+"/approve", map[string]any{})
		}

		// The name is taken after the request was made: the account is bob-2, and the mail says so, in Chinese.
		ask("bob", "zh-CN")
		if _, err := a.auth.CreateUser(context.Background(), auth.CreateParams{Username: "bob", Role: "member", Password: pw}); err != nil {
			t.Fatal(err)
		}
		var r result
		m := f.mailed(t, "bob@acme.example", func() { r = approve(pending("bob")).want(t, 200, "") })
		if mustNoUser(a, "bob-2") || !strings.Contains(m.Text, "bob-2") {
			t.Fatalf("the mail names the account made: %+v", m)
		}
		if !strings.ContainsFunc(m.Subject, func(c rune) bool { return unicode.Is(unicode.Han, c) }) && !strings.Contains(m.Text, "=E4=BD=A0") {
			t.Fatalf("the mail is not in the language of the sign-in: %+v", m)
		}
		_ = r

		// The identity was linked to another account meanwhile.
		ask("cat", "")
		sf.link(t, mustProviderID(t, a, "soc"), "bob", "sub-cat")
		if r := approve(pending("cat")); r.code != 409 || r.body["error"] != "conflict" || !strings.Contains(r.raw, "linked to another person") {
			t.Fatalf("%d %s", r.code, r.raw)
		}

		// The tenant turns the provider away (signin.social).
		ask("dot", "")
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/signin.social", map[string]any{"value": []string{"other"}}).want(t, 200, "")
		approve(pending("dot")).want(t, 409, "provider_unavailable")
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/signin.social", map[string]any{"value": []string{"*"}}).want(t, 200, "")

		// The tenant requires its own SSO.
		ask("eve", "")
		sf.orgProvider(t, f.admB, f.tenB, "acme-sso", nil)
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/signin.ssoOnly", map[string]any{"value": true}).want(t, 200, "")
		approve(pending("eve")).want(t, 409, "provider_unavailable")
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/signin.ssoOnly", map[string]any{"value": false}).want(t, 200, "")

		// The provider is removed.
		f.root.do("DELETE", "/api/admin/providers/"+mustProviderID(t, a, "soc")+"?force=1", nil).want(t, 204, "")
		approve(pending("eve")).want(t, 409, "provider_unavailable")
	})
}

func mustProviderID(t *testing.T, a *acc, slug string) string {
	t.Helper()
	var id string
	if err := a.st.Q().QueryRow(context.Background(), `SELECT id FROM providers WHERE slug = ?`, slug).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
