package api_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/auth/testidp"
	"petalbook.app/server/internal/store"
)

func providersOpts() accOpts { return accOpts{httpc: anyTLSClient()} }

func TestProviderCRUDAndRefusals(t *testing.T) {
	t.Parallel()
	f := newIDPFor(t, "one")
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		member := a.addUser("tang", pw, "member")
		member.do("GET", "/api/admin/auth/providers", nil).want(t, 403, "forbidden")
		member.do("POST", "/api/admin/auth/providers", f.providerBody("one", nil)).want(t, 403, "forbidden")
		a.newClient("").do("GET", "/api/admin/security", nil).want(t, 401, "unauthenticated")

		r := admin.do("POST", "/api/admin/auth/providers", f.providerBody("one", map[string]any{"icon": "preset:microsoft", "order": 2})).want(t, 201, "")
		if strings.Contains(r.raw, idpSecret) {
			t.Fatal("the secret is in the answer")
		}
		o := r.body["oidc"].(map[string]any)
		if r.body["slug"] != "one" || r.body["enabled"] != true || r.body["icon"] != "preset:microsoft" || r.body["order"] != float64(2) || o["hasSecret"] != true ||
			o["redirectUrl"] != testPublic+"/api/auth/oidc/one/callback" || o["legacyCallback"] != false || r.body["status"].(map[string]any)["ok"] != true ||
			r.body["people"] != float64(0) || r.body["saml"] != nil || r.body["github"] != nil {
			t.Fatalf("%s", r.raw)
		}
		if scopes := o["scopes"].([]any); len(scopes) != 2 || scopes[0] != "profile" {
			t.Fatalf("%v", scopes)
		}
		// The secret is sealed in the database, and not the plain text.
		var enc string
		if err := a.st.Q().QueryRow(context.Background(), `SELECT secret_enc FROM sign_in_providers WHERE slug = 'one'`).Scan(&enc); err != nil || enc == "" || strings.Contains(enc, idpSecret) {
			t.Fatalf("%q %v", enc, err)
		}
		if g := admin.do("GET", "/api/admin/auth/providers/one", nil).want(t, 200, ""); g.body["name"] != "Provider one" || strings.Contains(g.raw, idpSecret) {
			t.Fatal(g.raw)
		}
		l := admin.do("GET", "/api/admin/auth/providers", nil).want(t, 200, "")
		if n := len(l.body["providers"].([]any)); n != 1 {
			t.Fatal(l.raw)
		}
		admin.do("GET", "/api/admin/auth/providers/nope", nil).want(t, 404, "not_found")

		// Refusals.
		admin.do("POST", "/api/admin/auth/providers", f.providerBody("one", nil)).want(t, 409, "exists")
		admin.do("POST", "/api/admin/auth/providers", f.providerBody("two", nil)).want(t, 409, "provider_issuer_in_use")
		for _, slug := range []string{"", "Bad", "-x", "has space", strings.Repeat("a", 33)} {
			admin.do("POST", "/api/admin/auth/providers", f.providerBody(slug, nil)).want(t, 400, "invalid")
		}
		other := newIDPFor(t, "other")
		for _, extra := range []map[string]any{{"icon": "https://evil.example/x.png"}, {"icon": "preset:nothing"}, {"name": ""}, {"name": strings.Repeat("n", 61)}, {"kind": "ldap"}} {
			admin.do("POST", "/api/admin/auth/providers", other.providerBody("other", extra)).want(t, 400, "invalid")
		}
		bad := other.providerBody("other", nil)
		bad["oidc"].(map[string]any)["issuer"] = "http://insecure.example.test"
		admin.do("POST", "/api/admin/auth/providers", bad).want(t, 400, "oidc_issuer_invalid")
		bad["oidc"].(map[string]any)["issuer"] = other.srv.URL
		bad["github"] = map[string]any{"clientId": "x"}
		admin.do("POST", "/api/admin/auth/providers", bad).want(t, 400, "invalid") // the settings of another kind
		// GitHub that creates accounts must name an organisation.
		gh := newFakeGitHub(t)
		admin.do("POST", "/api/admin/auth/providers", gh.body("gh", map[string]any{"autoCreate": true})).want(t, 400, "auto_create_needs_org")
		admin.do("POST", "/api/admin/auth/providers", gh.body("gh", map[string]any{"autoCreate": true, "github": map[string]any{"baseUrl": gh.srv.URL, "clientId": "gh-client", "clientSecret": "gh-secret", "organizations": []string{"KazuhaHub"}}})).want(t, 201, "")
		admin.do("PATCH", "/api/admin/auth/providers/gh", map[string]any{"github": map[string]any{"organizations": []string{}}}).want(t, 400, "auto_create_needs_org")
		// Only one provider has the legacy callback.
		admin.do("PATCH", "/api/admin/auth/providers/one", map[string]any{"oidc": map[string]any{"legacyCallback": true}}).want(t, 200, "")
		r = admin.do("POST", "/api/admin/auth/providers", other.providerBody("other", map[string]any{"oidc": map[string]any{"issuer": other.srv.URL, "clientId": idpClient, "legacyCallback": true}})).want(t, 409, "legacy_callback_in_use")
		_ = r

		// A change: only what is sent, the secret kept unless sent.
		p := admin.do("PATCH", "/api/admin/auth/providers/one", map[string]any{"name": "Renamed", "autoCreate": true, "mergeVerifiedEmail": true}).want(t, 200, "")
		if p.body["name"] != "Renamed" || p.body["autoCreate"] != true || p.body["mergeVerifiedEmail"] != true || p.body["oidc"].(map[string]any)["hasSecret"] != true {
			t.Fatal(p.raw)
		}
		p = admin.do("PATCH", "/api/admin/auth/providers/one", map[string]any{"oidc": map[string]any{"clientSecret": ""}}).want(t, 200, "")
		if p.body["oidc"].(map[string]any)["hasSecret"] != false {
			t.Fatal(p.raw)
		}
		admin.do("PATCH", "/api/admin/auth/providers/one", map[string]any{"slug": "other"}).want(t, 400, "invalid")
		admin.do("PATCH", "/api/admin/auth/providers/one", map[string]any{"kind": "saml"}).want(t, 400, "invalid")
		admin.do("PATCH", "/api/admin/auth/providers/nope", map[string]any{"name": "x"}).want(t, 404, "not_found")

		// A provider that cannot be reached is stored, disabled, and says why.
		down := newIDPFor(t, "down")
		down.srv.Close()
		d := admin.do("POST", "/api/admin/auth/providers", down.providerBody("down", nil)).want(t, 201, "")
		if d.body["enabled"] != false || d.body["status"].(map[string]any)["ok"] != false {
			t.Fatal(d.raw)
		}
		// Refresh on a reachable provider records the fetch.
		rf := admin.do("POST", "/api/admin/auth/providers/gh/refresh", nil).want(t, 200, "")
		if rf.body["status"].(map[string]any)["ok"] != true || rf.body["status"].(map[string]any)["checked"] == "" {
			t.Fatal(rf.raw)
		}
		admin.do("POST", "/api/admin/auth/providers/nope/refresh", nil).want(t, 404, "not_found")

		// Deleting.
		admin.do("DELETE", "/api/admin/auth/providers/down?force=2", nil).want(t, 400, "invalid")
		admin.do("DELETE", "/api/admin/auth/providers/down", nil).want(t, 204, "")
		admin.do("DELETE", "/api/admin/auth/providers/down", nil).want(t, 404, "not_found")
		rows := admin.audit(t, "")
		kinds := map[string]bool{}
		for _, row := range rows {
			kinds[row["kind"].(string)] = true
			if strings.Contains(fmt.Sprint(row["detail"]), idpSecret) || strings.Contains(fmt.Sprint(row["detail"]), "gh-secret") {
				t.Fatalf("a secret in the audit log: %v", row)
			}
		}
		for _, k := range []string{"provider_created", "provider_changed", "provider_deleted"} {
			if !kinds[k] {
				t.Errorf("no %s row", k)
			}
		}
	})
}

func TestProviderLimitAndSAMLNeedsHTTPS(t *testing.T) {
	t.Parallel()
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		for i := range 50 {
			admin.do("POST", "/api/admin/auth/providers", map[string]any{"slug": fmt.Sprintf("g%d", i), "kind": "github", "name": "G",
				"github": map[string]any{"baseUrl": fmt.Sprintf("https://ghe%d.example.test", i), "clientId": "c", "clientSecret": "s"}}).want(t, 201, "")
		}
		admin.do("POST", "/api/admin/auth/providers", map[string]any{"slug": "g50", "kind": "github", "name": "G",
			"github": map[string]any{"baseUrl": "https://ghe50.example.test", "clientId": "c", "clientSecret": "s"}}).want(t, 409, "provider_limit")
	})
	idp := newFakeSAML(t, false)
	o := providersOpts()
	o.public = "http://notes.example.test"
	eachAccounts(t, o, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		admin.do("POST", "/api/admin/auth/providers", idp.body("campus", nil)).want(t, 400, "saml_needs_https")
	})
}

func TestTwoOpenIDConnectProvidersSignIn(t *testing.T) {
	t.Parallel()
	one, two := newIDPFor(t, "one"), newIDPFor(t, "two")
	one.addUser("alice", testidp.Claims{"sub": "sub-alice", "preferred_username": "alice", "email": "alice@example.test", "name": "Alice A"})
	two.addUser("alice", testidp.Claims{"sub": "other-sub", "preferred_username": "alice", "email": "alice@example.test", "name": "Alice A"})
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		admin.do("POST", "/api/admin/auth/providers", one.providerBody("one", map[string]any{"autoCreate": true, "order": 1})).want(t, 201, "")
		admin.do("POST", "/api/admin/auth/providers", two.providerBody("two", map[string]any{"order": 0})).want(t, 201, "")

		pub := a.newClient("").do("GET", "/api/auth/providers", nil).want(t, 200, "")
		list := pub.body["providers"].([]any)
		if len(list) != 2 || list[0].(map[string]any)["slug"] != "two" || list[1].(map[string]any)["slug"] != "one" || pub.body["loginMode"] != "dual" ||
			pub.body["local"] != true || pub.body["sso"] != true || pub.body["passkey"] != true || strings.Contains(strings.ToLower(pub.raw), "ssoonly") {
			t.Fatal(pub.raw)
		}
		if strings.Contains(pub.raw, "issuer") || strings.Contains(pub.raw, one.srv.URL) {
			t.Fatal("the public list says more than it should: " + pub.raw)
		}

		// A navigation from the page starts the sign-in and redirects.
		c := a.newClient("")
		r := c.do("GET", "/api/auth/oidc/one/start?return=/s/x", nil)
		if r.code != 303 || !strings.HasPrefix(r.resp.Header.Get("Location"), one.srv.URL+"/authorize?") {
			t.Fatalf("%d %s", r.code, r.raw)
		}
		if q := r.resp.Header.Get("Location"); !strings.Contains(q, url.QueryEscape(testPublic+"/api/auth/oidc/one/callback")) {
			t.Fatalf("redirect uri: %s", q)
		}
		// The page of someone coming back passes the address as login_hint; a hint that is not one line is dropped.
		r = c.do("GET", "/api/auth/oidc/one/start?login_hint="+url.QueryEscape("a+b@example.test"), nil)
		if loc, err := url.Parse(r.resp.Header.Get("Location")); err != nil || r.code != 303 || loc.Query().Get("login_hint") != "a+b@example.test" {
			t.Fatalf("%d %s", r.code, r.resp.Header.Get("Location"))
		}
		r = c.do("GET", "/api/auth/oidc/one/start?login_hint="+url.QueryEscape("a\nb"), nil)
		if loc, err := url.Parse(r.resp.Header.Get("Location")); err != nil || r.code != 303 || loc.Query().Has("login_hint") {
			t.Fatalf("%d %s", r.code, r.resp.Header.Get("Location"))
		}
		// Unknown and disabled providers go back to the sign-in page.
		for _, p := range []string{"/api/auth/oidc/nope/start", "/api/auth/saml/one/start"} {
			r := c.do("GET", p, nil)
			if r.code != 303 || r.resp.Header.Get("Location") != "/login?sso_error=provider_unavailable" {
				t.Fatalf("%s: %d %s", p, r.code, r.resp.Header.Get("Location"))
			}
		}
		c.do("GET", "/api/auth/ldap/one/start", nil).want(t, 404, "not_found")
		// Another site cannot start one.
		c.do("GET", "/api/auth/oidc/one/start", nil, "Sec-Fetch-Site", "cross-site").want(t, 403, "forbidden")

		// Signing in through the first creates the account; the same person at the second provider is another identity.
		at := one.beginAt(t, c, "/api/auth/oidc/one/start", "/api/auth/oidc/one/callback", "alice", map[string]any{"return": "/s/here"})
		loc, _ := finish(t, c, at.cb)
		if loc != "/s/here" || signedInAs(t, c) != "alice" {
			t.Fatalf("%s %s", loc, signedInAs(t, c))
		}
		acct := c.do("GET", "/api/account", nil).want(t, 200, "")
		ids := acct.body["identities"].([]any)
		if len(ids) != 1 || ids[0].(map[string]any)["providerSlug"] != "one" || ids[0].(map[string]any)["kind"] != "oidc" || ids[0].(map[string]any)["provider"] != "Provider one" || ids[0].(map[string]any)["label"] != "alice@example.test" {
			t.Fatal(acct.raw)
		}
		d := a.newClient("")
		if loc := two.signIn2(t, d, "two", "alice", map[string]any{}); loc != "/login?sso_error=no_account" {
			t.Fatal(loc) // provider two does not create accounts, and an address alone never merges
		}
		// Linking from the account page; a callback of the other provider does not fit this attempt.
		if loc := two.signIn2(t, c, "two", "alice", map[string]any{"intent": "link", "return": "/settings"}); loc != "/settings" {
			t.Fatal(loc)
		}
		acct = c.do("GET", "/api/account", nil).want(t, 200, "")
		if len(acct.body["identities"].([]any)) != 2 {
			t.Fatal(acct.raw)
		}
		e := a.newClient("")
		if loc := two.signIn2(t, e, "two", "alice", map[string]any{}); loc != "/" || signedInAs(t, e) != "alice" {
			t.Fatal(loc)
		}
		pl := admin.do("GET", "/api/admin/auth/providers", nil).want(t, 200, "")
		for _, p := range pl.body["providers"].([]any) {
			if p.(map[string]any)["people"] != float64(1) {
				t.Fatalf("people: %s", pl.raw)
			}
		}
		// A person who can sign in only through a provider keeps it from being deleted, unless forced.
		f := a.newClient("")
		two.addUser("zed", testidp.Claims{"sub": "sub-zed", "preferred_username": "zed"})
		one.addUser("zed", testidp.Claims{"sub": "sub-zed-1", "preferred_username": "zed"})
		if loc := one.signIn2(t, f, "one", "zed", map[string]any{}); loc != "/" {
			t.Fatal(loc)
		}
		r = admin.do("DELETE", "/api/admin/auth/providers/one", nil).want(t, 409, "provider_in_use")
		if r.body["accounts"] != float64(1) {
			t.Fatal(r.raw)
		}
		admin.do("DELETE", "/api/admin/auth/providers/one?force=1", nil).want(t, 204, "")
		// Identities stay; a provider added again with the same issuer brings them back.
		if _, err := store.FindIdentity(context.Background(), a.st.Q(), one.srv.URL, "sub-alice"); err != nil {
			t.Fatal(err)
		}
		c.do("GET", "/api/auth/oidc/one/start", nil)
		admin.do("POST", "/api/admin/auth/providers", one.providerBody("again", map[string]any{"order": 3})).want(t, 201, "")
		pl = admin.do("GET", "/api/admin/auth/providers/again", nil).want(t, 200, "")
		if pl.body["people"] != float64(2) {
			t.Fatal(pl.raw)
		}
		// Removing an identity is allowed while another way in is left, and not the last one.
		acct = c.do("GET", "/api/account", nil).want(t, 200, "")
		ids = acct.body["identities"].([]any)
		if len(ids) != 2 {
			t.Fatal(acct.raw)
		}
		c.do("DELETE", "/api/account/identities/"+ids[0].(map[string]any)["id"].(string), nil).want(t, 204, "")
		c.do("DELETE", "/api/account/identities/"+ids[1].(map[string]any)["id"].(string), nil).want(t, 409, "conflict")
	})
}

// signIn2 runs a whole sign-in at the provider with the slug and returns where it ended.
func (f *fakeIDP) signIn2(t *testing.T, c *client, slug, who string, body map[string]any) string {
	t.Helper()
	at := f.beginAt(t, c, "/api/auth/oidc/"+slug+"/start", "/api/auth/oidc/"+slug+"/callback", who, body)
	loc, _ := finish(t, c, at.cb)
	return loc
}

func TestFlagsBecomeTheProviderSsoWithoutLockingAnyoneOut(t *testing.T) {
	t.Parallel()
	f := newFakeIDP(t)
	opts := f.opts(true)
	eachAccounts(t, opts, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		// The provider is in the database with the old callback, enabled; nothing else changed.
		p := admin.do("GET", "/api/admin/auth/providers/sso", nil).want(t, 200, "")
		o := p.body["oidc"].(map[string]any)
		if p.body["name"] != "Company SSO" || p.body["enabled"] != true || p.body["autoCreate"] != true || o["legacyCallback"] != true || o["hasSecret"] != true ||
			o["issuer"] != f.srv.URL || o["redirectUrl"] != testPublic+"/api/auth/oidc/callback" || o["clientId"] != idpClient || strings.Contains(p.raw, idpSecret) {
			t.Fatal(p.raw)
		}
		sec := admin.do("GET", "/api/admin/security", nil).want(t, 200, "")
		lg := sec.body["login"].(map[string]any)
		if lg["mode"] != "dual" || lg["ssoOnly"] != false || lg["effectiveMode"] != "dual" || len(sec.body["notices"].([]any)) != 0 {
			t.Fatal(sec.raw)
		}
		// Passwords still work, the old endpoints serve the provider, and its identities work.
		a.newClient("").do("POST", "/api/login", good("kazuha")).want(t, 200, "")
		c := a.newClient("")
		if loc := f.signIn(t, c, "alice", map[string]any{}); loc != "/" || signedInAs(t, c) != "alice" {
			t.Fatal(loc)
		}
		m := c.do("GET", "/api/session", nil).body["methods"].(map[string]any)
		if m["oidc"].(map[string]any)["name"] != "Company SSO" {
			t.Fatal(m)
		}
		// The new-style callback of the same provider works for attempts started the new way.
		// Its redirect URI is still the old one, whichever way the attempt started.
		d := a.newClient("")
		at := f.beginAt(t, d, "/api/auth/oidc/sso/start", "/api/auth/oidc/callback", "bob", map[string]any{})
		if loc, _ := finish(t, d, at.cb); loc != "/" {
			t.Fatal(loc)
		}

		// A restart with other flags: the database wins, the flags only warn.
		ctx := context.Background()
		other := *f.config(false)
		other.ClientID, other.Name = "another-client", "Another name"
		svc2, err := auth.New(ctx, auth.Config{Store: a.st, Spaces: a.mgr, PublicURL: testPublic, OIDC: &other, HTTPClient: f.srv.Client(), Password: fastPasswords, Now: a.clock.Now})
		if err != nil {
			t.Fatal(err)
		}
		view, err := svc2.SecurityView(ctx)
		if err != nil || len(view.Notices) != 1 || view.Notices[0].Code != "flags_ignored" {
			t.Fatalf("%+v %v", view, err)
		}
		pv, err := svc2.Provider(ctx, "sso")
		if err != nil || pv.Name != "Company SSO" || pv.OIDC.ClientID != idpClient {
			t.Fatalf("the flags changed the stored provider: %+v %v", pv, err)
		}
		// The same flags: no notice. No flags: none either, and the provider stays.
		same := *f.config(true)
		svc3, err := auth.New(ctx, auth.Config{Store: a.st, Spaces: a.mgr, PublicURL: testPublic, OIDC: &same, HTTPClient: f.srv.Client(), Password: fastPasswords, Now: a.clock.Now})
		if err != nil {
			t.Fatal(err)
		}
		if v, _ := svc3.SecurityView(ctx); len(v.Notices) != 0 {
			t.Fatalf("%+v", v.Notices)
		}
		// Deleting the provider and starting again with the flags does not bring it back; the notice says so.
		admin.do("DELETE", "/api/admin/auth/providers/sso?force=1", nil).want(t, 204, "")
		svc4, err := auth.New(ctx, auth.Config{Store: a.st, Spaces: a.mgr, PublicURL: testPublic, OIDC: &same, HTTPClient: f.srv.Client(), Password: fastPasswords, Now: a.clock.Now})
		if err != nil {
			t.Fatal(err)
		}
		if v, _ := svc4.SecurityView(ctx); len(v.Notices) != 1 || v.Notices[0].Code != "flags_ignored" || len(svc4.PublicProviders()) != 0 {
			t.Fatalf("%+v", v.Notices)
		}
		// The identities stay for a provider added later with the same issuer.
		if _, err := store.FindIdentity(ctx, a.st.Q(), f.srv.URL, "sub-alice"); err != nil {
			t.Fatal(err)
		}
	})
}

func TestMergeByVerifiedEmailIsStrict(t *testing.T) {
	t.Parallel()
	f := newIDPFor(t, "m")
	set := func(key, sub, email string, verified bool) {
		f.addUser(key, testidp.Claims{"sub": sub, "preferred_username": key, "email": email, "email_verified": verified, "name": key})
	}
	set("pat", "s-pat", "pat@example.test", true)
	set("unverified", "s-un", "un@example.test", true)
	set("boss", "s-boss", "boss@example.test", true)
	set("off", "s-off", "off@example.test", true)
	set("claimfalse", "s-cf", "cf@example.test", false)
	set("twin", "s-twin", "twin@example.test", true)
	set("gone", "s-gone", "gone@example.test", true)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		admin.do("POST", "/api/admin/auth/providers", f.providerBody("m", map[string]any{"mergeVerifiedEmail": true})).want(t, 201, "")
		ctx := context.Background()
		mk := func(username, email string, verified bool, role string) string {
			u, err := a.auth.CreateUser(ctx, auth.CreateParams{Username: username, Role: role, Password: pw, Email: email, EmailVerified: verified})
			if err != nil {
				t.Fatal(err)
			}
			return u.ID
		}
		mk("pat-local", "Pat@Example.test", true, "member")
		mk("un-local", "un@example.test", false, "member")
		adminBoss := mk("boss-local", "boss@example.test", true, "admin")
		_ = adminBoss
		mk("off-local", "off@example.test", true, "member")
		mk("cf-local", "cf@example.test", true, "member")
		mk("twin-a", "twin@example.test", true, "member")
		mk("twin-b", "twin@example.test", true, "member")
		goneID := mk("gone-local", "gone@example.test", true, "member")
		if _, err := a.auth.UpdateUser(ctx, store.User{Role: "admin", SiteAdmin: true}, "", goneID, auth.UserPatch{Disabled: ptr(true)}); err != nil {
			t.Fatal(err)
		}
		try := func(key string) (string, *client) {
			c := a.newClient("")
			return f.signIn2(t, c, "m", key, map[string]any{}), c
		}
		// The one account with that address, verified on both sides: merged.
		loc, c := try("pat")
		if loc != "/" || signedInAs(t, c) != "pat-local" {
			t.Fatalf("%s %s", loc, signedInAs(t, c))
		}
		if rows := admin.audit(t, "identity_linked"); len(rows) != 1 || rows[0]["detail"].(map[string]any)["by"] != "email" || rows[0]["detail"].(map[string]any)["provider"] != "m" {
			t.Fatalf("%v", rows)
		}
		// Every other case is no_account.
		for _, key := range []string{"unverified", "boss", "claimfalse", "twin", "gone"} {
			if loc, _ := try(key); loc != "/login?sso_error=no_account" {
				t.Errorf("%s: %s", key, loc)
			}
		}
		// The provider's option is off by default: an address alone never merges.
		admin.do("PATCH", "/api/admin/auth/providers/m", map[string]any{"mergeVerifiedEmail": false}).want(t, 200, "")
		if loc, _ := try("off"); loc != "/login?sso_error=no_account" {
			t.Fatal(loc)
		}
		// A person changing their own address clears the mark; an administrator confirming it sets it.
		off := a.newClient("")
		off.do("POST", "/api/login", good("off-local")).want(t, 200, "")
		u := off.do("PATCH", "/api/account", map[string]any{"email": "off2@example.test"}).want(t, 200, "").user()
		if u["emailVerified"] != false || u["email"] != "off2@example.test" {
			t.Fatal(u)
		}
		id := mustUserID(t, a, "off-local")
		w := admin.do("PATCH", "/api/users/"+id, map[string]any{"email": "off3@example.test"}).want(t, 200, "").user()
		if w["emailVerified"] != false {
			t.Fatal(w)
		}
		w = admin.do("PATCH", "/api/users/"+id, map[string]any{"emailVerified": true}).want(t, 200, "").user()
		if w["emailVerified"] != true || w["email"] != "off3@example.test" {
			t.Fatal(w)
		}
		w = admin.do("PATCH", "/api/users/"+id, map[string]any{"email": "off4@example.test", "emailVerified": true}).want(t, 200, "").user()
		if w["emailVerified"] != true {
			t.Fatal(w)
		}
		// A new account made by the provider has the provider's address, verified when it vouched for it.
		admin.do("PATCH", "/api/admin/auth/providers/m", map[string]any{"autoCreate": true}).want(t, 200, "")
		set("fresh", "s-fresh", "fresh@example.test", true)
		set("fresh2", "s-fresh2", "fresh2@example.test", false)
		if loc, c := try("fresh"); loc != "/" {
			t.Fatal(loc)
		} else if u := c.do("GET", "/api/session", nil).user(); u["email"] != "fresh@example.test" || u["emailVerified"] != true || u["role"] != "member" {
			t.Fatal(u)
		}
		if _, c := try("fresh2"); c.do("GET", "/api/session", nil).user()["emailVerified"] != false {
			t.Fatal("a claim that is not verified made a verified account")
		}
	})
}

func ptr[T any](v T) *T { return &v }

// countingClient counts the requests that go to a path.
type countingTransport struct {
	base http.RoundTripper
	mu   sync.Mutex
	n    map[string]int
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.n[r.URL.Path]++
	c.mu.Unlock()
	return c.base.RoundTrip(r)
}

func (c *countingTransport) count(path string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n[path]
}

func TestADiscoveryDocumentIsKeptAndASignInDoesNotFetchIt(t *testing.T) {
	t.Parallel()
	f := newIDPFor(t, "one")
	f.addUser("alice", testidp.Claims{"sub": "sub-alice", "preferred_username": "alice"})
	o := providersOpts()
	counter := &countingTransport{base: anyTLSClient().Transport, n: map[string]int{}}
	o.httpc = &http.Client{Transport: counter, Timeout: 20 * time.Second}
	eachAccounts(t, o, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		const doc = "/.well-known/openid-configuration"
		before := counter.count(doc)
		admin.do("POST", "/api/admin/auth/providers", f.providerBody("one", map[string]any{"autoCreate": true})).want(t, 201, "")
		if counter.count(doc) != before+1 {
			t.Fatalf("%d fetches to make a provider", counter.count(doc)-before)
		}
		c := a.newClient("")
		if loc := f.signIn2(t, c, "one", "alice", map[string]any{}); loc != "/" {
			t.Fatal(loc)
		}
		if counter.count(doc) != before+1 {
			t.Fatalf("a sign-in fetched the discovery document: %d", counter.count(doc)-before)
		}
		// A restart reads it from the state database, with the identity provider unreachable for discovery.
		svc2, err := auth.New(context.Background(), auth.Config{Store: a.st, Spaces: a.mgr, PublicURL: testPublic, HTTPClient: o.httpc, Password: fastPasswords, Now: a.clock.Now})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc2.SSOBegin(context.Background(), "oidc", "one", auth.BeginParams{}, "203.0.113.1"); err != nil {
			t.Fatal(err)
		}
		if counter.count(doc) != before+1 {
			t.Fatalf("the restarted server fetched the discovery document: %d", counter.count(doc)-before)
		}
		// Refresh fetches it, with the stored secret.
		r := admin.do("POST", "/api/admin/auth/providers/one/refresh", nil).want(t, 200, "")
		if r.body["status"].(map[string]any)["ok"] != true || counter.count(doc) != before+2 {
			t.Fatalf("%s %d", r.raw, counter.count(doc)-before)
		}
	})
}

func TestASecretThatCannotBeOpenedMakesTheProviderUnavailableAndIsSaid(t *testing.T) {
	t.Parallel()
	f := newIDPFor(t, "one")
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		admin.do("POST", "/api/admin/auth/providers", f.providerBody("one", nil)).want(t, 201, "")
		ctx := context.Background()
		// The key of this server changed, or the row was copied from another one.
		if _, err := a.st.Q().Exec(ctx, `UPDATE sign_in_providers SET secret_enc = 'bm90IHNlYWxlZCBieSB0aGlzIHNlcnZlcg==' WHERE slug = 'one'`); err != nil {
			t.Fatal(err)
		}
		svc2, err := auth.New(ctx, auth.Config{Store: a.st, Spaces: a.mgr, PublicURL: testPublic, HTTPClient: anyTLSClient(), Password: fastPasswords, Now: a.clock.Now})
		if err != nil {
			t.Fatal(err)
		}
		v, err := svc2.SecurityView(ctx)
		if err != nil || len(v.Notices) != 1 || v.Notices[0].Code != "secret_unreadable" || v.Notices[0].Slug != "one" {
			t.Fatalf("%+v %v", v, err)
		}
		if _, err := svc2.SSOBegin(ctx, "oidc", "one", auth.BeginParams{}, "203.0.113.2"); err == nil {
			t.Fatal("a provider whose secret cannot be opened started a sign-in")
		}
	})
}
