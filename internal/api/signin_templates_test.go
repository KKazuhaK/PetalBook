package api_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/auth/testidp"
	"petalbook.app/server/internal/store"
)

// noNetwork is a client that reaches nothing: a template's provider is made
// and started without asking any host.
func noNetwork() *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("no network in this test: " + r.URL.String())
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func appleP8(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func TestProviderTemplatesAreListedForSiteAdministrators(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{httpc: noNetwork()}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		member := a.addUser("tang", pw, "member")
		member.do("GET", "/api/admin/provider-templates", nil).want(t, 403, "forbidden")
		r := admin.do("GET", "/api/admin/provider-templates", nil).want(t, 200, "")
		got := map[string]map[string]any{}
		for _, v := range r.body["templates"].([]any) {
			m := v.(map[string]any)
			got[m["id"].(string)] = m
		}
		for id, want := range map[string][2]string{
			"google": {"oidc", "https://accounts.google.com"}, "microsoft": {"oidc", "https://login.microsoftonline.com/{tenantid}/v2.0"},
			"apple": {"oidc", "https://appleid.apple.com"}, "github": {"github", "https://github.com"}, "gitlab-com": {"oidc", "https://gitlab.com"},
		} {
			m := got[id]
			if m == nil || m["kind"] != want[0] || m["issuer"] != want[1] || m["class"] != "social" || m["icon"] == "" || len(m["scopes"].([]any)) == 0 {
				t.Errorf("%s: %v", id, m)
			}
		}
		if got["apple"]["secret"] != "apple" || got["apple"]["responseMode"] != "form_post" || got["google"]["secret"] != "client" {
			t.Fatal(r.raw)
		}
	})
}

// A template's issuer, addresses and scopes are copied into the row, and the
// provider is made and started without asking any host. Its issuer, its
// template and its class do not change after.
func TestATemplateIsCopiedIntoTheRow(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{httpc: noNetwork()}, func(t *testing.T, a *acc) {
		ctx := context.Background()
		admin := a.setup("kazuha", pw)
		r := admin.do("POST", "/api/admin/providers", map[string]any{"template": "google", "clientId": "g-client", "clientSecret": "g-secret"}).want(t, 201, "")
		o := r.body["oidc"].(map[string]any)
		if r.body["slug"] != "google" || r.body["kind"] != "oidc" || r.body["class"] != "social" || r.body["template"] != "google" ||
			r.body["name"] != "Google" || r.body["icon"] != "preset:google" || r.body["enabled"] != true || r.body["status"].(map[string]any)["ok"] != true ||
			o["issuer"] != "https://accounts.google.com" || o["clientId"] != "g-client" || o["hasSecret"] != true ||
			strings.Join(anyStrings(o["scopes"]), " ") != "openid email profile" || o["redirectUrl"] != testPublic+"/api/auth/oidc/google/callback" {
			t.Fatal(r.raw)
		}
		if strings.Contains(r.raw, "g-secret") {
			t.Fatal("the secret is in the answer")
		}
		row, err := store.GetProvider(ctx, a.st.Q(), "google")
		if err != nil {
			t.Fatal(err)
		}
		var cfg map[string]any
		if err := json.Unmarshal([]byte(row.Config), &cfg); err != nil {
			t.Fatal(err)
		}
		if row.Class != "social" || row.Template != "google" || row.Kind != "oidc" || row.Issuer != "https://accounts.google.com" ||
			cfg["authorizationEndpoint"] != "https://accounts.google.com/o/oauth2/v2/auth" || cfg["tokenEndpoint"] != "https://oauth2.googleapis.com/token" ||
			cfg["jwksUri"] != "https://www.googleapis.com/oauth2/v3/certs" || cfg["issuer"] != "https://accounts.google.com" {
			t.Fatalf("%+v %v", row, cfg)
		}
		// Started from the copied addresses, nothing fetched.
		start := a.newClient("").do("POST", "/api/auth/oidc/google/start", map[string]any{}).want(t, 200, "")
		u, _ := url.Parse(start.body["url"].(string))
		if u.Scheme+"://"+u.Host+u.Path != "https://accounts.google.com/o/oauth2/v2/auth" || u.Query().Get("client_id") != "g-client" ||
			u.Query().Get("scope") != "openid email profile" || u.Query().Get("response_mode") != "" {
			t.Fatal(u)
		}
		// Microsoft's: the multi-tenant issuer, and its own start.
		ms := admin.do("POST", "/api/admin/providers", map[string]any{"template": "microsoft", "slug": "ms", "clientId": "m-client", "clientSecret": "m-secret"}).want(t, 201, "")
		if ms.body["oidc"].(map[string]any)["issuer"] != "https://login.microsoftonline.com/{tenantid}/v2.0" || ms.body["template"] != "microsoft" || ms.body["status"].(map[string]any)["ok"] != true {
			t.Fatal(ms.raw)
		}
		start = a.newClient("").do("POST", "/api/auth/oidc/ms/start", map[string]any{}).want(t, 200, "")
		u, _ = url.Parse(start.body["url"].(string))
		q := u.Query()
		if u.Host+u.Path != "login.microsoftonline.com/common/oauth2/v2.0/authorize" || q.Get("client_id") != "m-client" || q.Get("code_challenge_method") != "S256" ||
			q.Get("nonce") == "" || q.Get("state") == "" || q.Get("redirect_uri") != testPublic+"/api/auth/oidc/ms/callback" || q.Get("scope") != "openid email profile" {
			t.Fatal(u)
		}
		// GitLab.com, and GitHub's (the github kind, on github.com).
		gl := admin.do("POST", "/api/admin/providers", map[string]any{"template": "gitlab-com", "clientId": "l", "clientSecret": "s"}).want(t, 201, "")
		if gl.body["slug"] != "gitlab-com" || gl.body["oidc"].(map[string]any)["issuer"] != "https://gitlab.com" || gl.body["class"] != "social" {
			t.Fatal(gl.raw)
		}
		gh := admin.do("POST", "/api/admin/providers", map[string]any{"template": "github", "slug": "gh", "clientId": "h", "clientSecret": "s"}).want(t, 201, "")
		if gh.body["kind"] != "github" || gh.body["class"] != "social" || gh.body["template"] != "" || gh.body["github"].(map[string]any)["baseUrl"] != "https://github.com" {
			t.Fatal(gh.raw)
		}

		// What a template filled stays.
		admin.do("PATCH", "/api/admin/providers/google", map[string]any{"oidc": map[string]any{"issuer": "https://evil.example"}}).want(t, 400, "template_fixed")
		admin.do("PATCH", "/api/admin/providers/google", map[string]any{"template": "microsoft"}).want(t, 400, "invalid")
		admin.do("PATCH", "/api/admin/providers/google", map[string]any{"class": "sso"}).want(t, 400, "invalid")
		admin.do("PATCH", "/api/admin/providers/google", map[string]any{"oidc": map[string]any{"clientId": "g2", "issuer": "https://accounts.google.com"}}).want(t, 200, "")
		// Refusals.
		admin.do("POST", "/api/admin/providers", map[string]any{"template": "nope", "slug": "x", "clientId": "a", "clientSecret": "b"}).want(t, 400, "template_unknown")
		admin.do("POST", "/api/admin/providers", map[string]any{"template": "google", "slug": "g2", "clientId": "a"}).want(t, 400, "invalid")
		admin.do("POST", "/api/admin/providers", map[string]any{"template": "google", "slug": "g3", "class": "sso", "clientId": "a", "clientSecret": "b"}).want(t, 400, "invalid")
		admin.do("POST", "/api/admin/providers", map[string]any{"template": "google", "slug": "g4", "kind": "saml", "clientId": "a", "clientSecret": "b"}).want(t, 400, "invalid")
		admin.do("POST", "/api/admin/providers", map[string]any{"template": "google", "clientId": "a", "clientSecret": "b"}).want(t, 409, "exists")
		admin.putSetting("features.tenancy", true).want(t, 200, "")
		org := admin.do("POST", "/api/admin/tenants", map[string]any{"slug": "acme", "name": "Acme"}).want(t, 201, "").body["id"].(string)
		admin.do("POST", "/api/admin/providers", map[string]any{"template": "google", "slug": "g5", "owner": org, "clientId": "a", "clientSecret": "b"}).want(t, 400, "invalid")
	})
}

func anyStrings(v any) []string {
	var out []string
	for _, s := range v.([]any) {
		out = append(out, s.(string))
	}
	return out
}

// The class is chosen when a provider is made (or is its kind's) and never
// changes, through the API or the store.
func TestClassIsFixedWhenAProviderIsMade(t *testing.T) {
	t.Parallel()
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		ctx := context.Background()
		f := newIDPFor(t, "one")
		f.idp.RedirectURIs = append(f.idp.RedirectURIs, testPublic+"/api/auth/oidc/two/callback")
		admin := a.setup("kazuha", pw)
		one := admin.do("POST", "/api/admin/providers", f.providerBody("one", nil)).want(t, 201, "")
		if one.body["class"] != "sso" {
			t.Fatal(one.raw)
		}
		two := admin.do("POST", "/api/admin/providers", f.providerBody("two", map[string]any{"class": "social"})).want(t, 201, "")
		if two.body["class"] != "social" {
			t.Fatal(two.raw)
		}
		admin.do("PATCH", "/api/admin/providers/one", map[string]any{"class": "social"}).want(t, 400, "invalid")
		admin.do("PATCH", "/api/admin/providers/two", map[string]any{"class": "sso", "name": "Renamed"}).want(t, 400, "invalid")
		// The same class is no change.
		admin.do("PATCH", "/api/admin/providers/two", map[string]any{"class": "social", "name": "Renamed"}).want(t, 200, "")
		admin.do("POST", "/api/admin/providers", f.providerBody("three", map[string]any{"class": "enterprise"})).want(t, 400, "invalid")
		gh := map[string]any{"slug": "gh", "kind": "github", "name": "GitHub", "github": map[string]any{"clientId": "c", "clientSecret": "s"}}
		gh["class"] = "sso"
		admin.do("POST", "/api/admin/providers", gh).want(t, 400, "invalid")
		delete(gh, "class")
		if r := admin.do("POST", "/api/admin/providers", gh).want(t, 201, ""); r.body["class"] != "social" {
			t.Fatal(r.raw)
		}
		// An organisation's own provider is its single sign-on.
		admin.putSetting("features.tenancy", true).want(t, 200, "")
		org := admin.do("POST", "/api/admin/tenants", map[string]any{"slug": "acme", "name": "Acme"}).want(t, 201, "").body["id"].(string)
		admin.do("POST", "/api/admin/providers", f.providerBody("acme-sso", map[string]any{"owner": org, "class": "social"})).want(t, 400, "invalid")
		if r := admin.do("POST", "/api/admin/providers", f.providerBody("acme-sso", map[string]any{"owner": org})).want(t, 201, ""); r.body["class"] != "sso" {
			t.Fatal(r.raw)
		}
		// The store never writes it after the insert.
		row, err := store.GetProvider(ctx, a.st.Q(), "two")
		if err != nil {
			t.Fatal(err)
		}
		row.Class, row.Template, row.Name = "sso", "google", "Changed"
		if err := store.UpdateProvider(ctx, a.st.Q(), *row); err != nil {
			t.Fatal(err)
		}
		if got, _ := store.GetProvider(ctx, a.st.Q(), "two"); got.Class != "social" || got.Template != "" || got.Name != "Changed" {
			t.Fatalf("%+v", got)
		}
		// A template is one of the known ones, in the database too.
		if _, err := a.st.Q().Exec(ctx, `UPDATE providers SET template = 'evil' WHERE id = 'two'`); err == nil {
			t.Fatal("an unknown template was stored")
		}
		if _, err := a.st.Q().Exec(ctx, `UPDATE providers SET template = 'google' WHERE id = 'two'`); err != nil {
			t.Fatalf("a known template: %v", err)
		}
		// Nor can a row be a sign-in provider without one.
		if _, err := a.st.Q().Exec(ctx, `UPDATE providers SET class = '' WHERE id = 'two'`); err == nil {
			t.Fatal("a sign-in provider without a class")
		}
	})
}

// A slug is in the callback addresses registered at each identity provider,
// so it is unique on the whole server, whoever owns the provider.
func TestSlugsAreUniqueOnTheServer(t *testing.T) {
	t.Parallel()
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		ctx := context.Background()
		f := newIDPFor(t, "site")
		admin := a.setup("kazuha", pw)
		admin.putSetting("features.tenancy", true).want(t, 200, "")
		acme := admin.do("POST", "/api/admin/tenants", map[string]any{"slug": "acme", "name": "Acme"}).want(t, 201, "").body["id"].(string)
		beta := admin.do("POST", "/api/admin/tenants", map[string]any{"slug": "beta", "name": "Beta"}).want(t, 201, "").body["id"].(string)
		admin.do("POST", "/api/admin/providers", f.providerBody("site", nil)).want(t, 201, "")
		admin.do("POST", "/api/admin/providers", f.providerBody("site", map[string]any{"owner": acme})).want(t, 409, "exists")
		admin.do("POST", "/api/admin/providers", f.providerBody("acme-idp", map[string]any{"owner": acme})).want(t, 201, "")
		admin.do("POST", "/api/admin/providers", f.providerBody("acme-idp", map[string]any{"owner": beta})).want(t, 409, "exists")
		admin.do("POST", "/api/admin/providers", f.providerBody("acme-idp", nil)).want(t, 409, "exists")
		// Reserved slugs stay reserved.
		admin.do("POST", "/api/admin/providers", f.providerBody("github", nil)).want(t, 409, "slug_reserved")
		admin.do("POST", "/api/admin/providers", f.providerBody("git", nil)).want(t, 409, "slug_reserved")
		// The templates' slugs are the site's: an organisation cannot take one
		// before the site adds that sign-in (its slugs start with its own).
		for _, slug := range []string{"google", "apple", "microsoft", "gitlab-com"} {
			admin.do("POST", "/api/admin/providers", f.providerBody(slug, map[string]any{"owner": acme})).want(t, 400, "invalid")
		}
		admin.do("POST", "/api/admin/providers", f.providerBody("gitlab-com", nil)).want(t, 201, "")
		// The database holds it too, across owners and against the hosts'
		// rows (a GitLab's id is its slug).
		now := a.clock.Now()
		err := store.InsertProvider(ctx, a.st.Q(), store.ProviderRow{ID: "other-id", Owner: beta, Slug: "site", Kind: "oidc", Name: "x", Config: "{}", Created: now, Updated: now})
		if !store.IsUnique(err) {
			t.Fatalf("a second slug: %v", err)
		}
		if err := store.InsertConnProviderRow(ctx, a.st.Q(), store.ConnProviderRow{ID: "lab", Type: "gitlab", Host: "lab.example.test", Scheme: "https", Name: "Lab", Owner: "site", State: "ok", Created: now, Updated: now}); err != nil {
			t.Fatal(err)
		}
		err = store.InsertProvider(ctx, a.st.Q(), store.ProviderRow{ID: "lab-signin", Owner: store.ProviderSite, Slug: "lab", Kind: "oidc", Name: "x", Config: "{}", Created: now, Updated: now})
		if !store.IsUnique(err) {
			t.Fatalf("a slug of a host's row: %v", err)
		}
	})
}

// pointAt sends a template's provider to a fake one: the template's
// addresses are the real provider's, copied into the row.
func pointAt(t *testing.T, a *acc, id, issuer, base string) {
	t.Helper()
	ctx := context.Background()
	row, err := store.GetProvider(ctx, a.st.Q(), id)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(row.Config), &cfg); err != nil {
		t.Fatal(err)
	}
	cfg["issuer"], cfg["authorizationEndpoint"], cfg["tokenEndpoint"], cfg["jwksUri"] = issuer, base+"/authorize", base+"/token", base+"/keys"
	raw, _ := json.Marshal(cfg)
	row.Config, row.Issuer, row.Cache = string(raw), issuer, ""
	if err := store.UpdateProvider(ctx, a.st.Q(), *row); err != nil {
		t.Fatal(err)
	}
	if err := a.auth.ReloadProviders(ctx); err != nil {
		t.Fatal(err)
	}
}

// Microsoft's /common: each token's issuer is the one of the tenant in its
// tid, checked, never skipped. A token whose iss names another tenant, or
// whose tid is no tenant id, is refused. Its address is trusted only with
// xms_edov.
func TestMicrosoftIssuerIsCheckedAgainstTheTokensTenant(t *testing.T) {
	t.Parallel()
	const tidA = "72f988bf-86f1-41af-91ab-2d7cd011db47"
	const tidB = "9188040d-6c67-4c5b-b112-36a304b66dad"
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		a.setSetting(t, "registration.mode", "open")
		f := newIDPFor(t, "microsoft")
		var mu sync.Mutex
		iss, tid := "", ""
		set := func(i, d string) {
			mu.Lock()
			iss, tid = i, d
			mu.Unlock()
		}
		f.idp.Mutate = func(c map[string]any) {
			mu.Lock()
			defer mu.Unlock()
			if iss != "" {
				c["iss"] = iss
			}
			if tid != "-" {
				c["tid"] = tid
			} else {
				delete(c, "tid")
			}
		}
		f.addUser("edov", testidp.Claims{"sub": "s-edov", "preferred_username": "edov", "email": "edov@contoso.test", "xms_edov": true, "name": "Ed"})
		f.addUser("plain", testidp.Claims{"sub": "s-plain", "preferred_username": "plain", "email": "ceo@victim.test", "email_verified": true, "name": "Pl"})
		admin := a.setup("kazuha", pw)
		admin.do("POST", "/api/admin/providers", map[string]any{"template": "microsoft", "clientId": idpClient, "clientSecret": idpSecret}).want(t, 201, "")
		pattern := f.srv.URL + "/{tenantid}/v2.0"
		pointAt(t, a, "microsoft", pattern, f.srv.URL)
		in := func(who string) (string, *client) {
			c := a.newClient("")
			return f.signIn2(t, c, "microsoft", who, map[string]any{}), c
		}
		for _, bad := range []struct{ name, iss, tid string }{
			{"another tenant's issuer", f.srv.URL + "/" + tidA + "/v2.0", tidB},
			{"the pattern as issuer", pattern, tidA},
			{"no tid", f.srv.URL + "/" + tidA + "/v2.0", "-"},
			{"a tid that is no tenant id", f.srv.URL + "/common/v2.0", "common"},
			{"the plain issuer", f.srv.URL, tidA},
		} {
			set(bad.iss, bad.tid)
			if loc, c := in("edov"); loc != "/login?sso_error=idp_error" || signedInAs(t, c) != "" {
				t.Fatalf("%s: %s", bad.name, loc)
			}
		}
		// The token's own tenant: signed in, the address proved by xms_edov.
		set(f.srv.URL+"/"+tidB+"/v2.0", tidB)
		loc, c := in("edov")
		if loc != "/" || signedInAs(t, c) != "edov" {
			t.Fatal(loc)
		}
		if u := c.do("GET", "/api/session", nil).user(); u["email"] != "edov@contoso.test" || u["emailVerified"] != true {
			t.Fatal(u)
		}
		// The plain email claim is not trusted, email_verified or not.
		set(f.srv.URL+"/"+tidA+"/v2.0", tidA)
		loc, c = in("plain")
		if loc != "/" {
			t.Fatal(loc)
		}
		if u := c.do("GET", "/api/session", nil).user(); u["emailVerified"] == true {
			t.Fatal(u)
		}
	})
}

var metaRefresh = regexp.MustCompile(`http-equiv="refresh" content="0;url=([^"]+)"`)

// Sign in with Apple: the client secret is an ES256 JWT the server signs with
// the team's key, kept until it nears its end and then replaced; the answer
// is posted (form_post) and turned into the callback by a page of the site;
// the name comes once beside the token; a relay address is verified but meets
// nobody's.
func TestSignInWithApple(t *testing.T) {
	t.Parallel()
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		a.setSetting(t, "registration.mode", "open")
		ctx := context.Background()
		key, keyPEM := appleP8(t)
		f := newIDPFor(t, "apple")
		var mu sync.Mutex
		var seen []string
		var bad []string
		f.idp.CheckSecret = func(secret string) bool {
			obj, err := jose.ParseSigned(secret, []jose.SignatureAlgorithm{jose.ES256})
			if err != nil {
				return false
			}
			payload, err := obj.Verify(&key.PublicKey)
			if err != nil {
				return false
			}
			var c map[string]any
			_ = json.Unmarshal(payload, &c)
			mu.Lock()
			defer mu.Unlock()
			if obj.Signatures[0].Header.KeyID != "KEY1234567" || c["iss"] != "TEAM123456" || c["sub"] != idpClient || c["aud"] != "https://appleid.apple.com" {
				bad = append(bad, string(payload))
				return false
			}
			exp, iat := int64(c["exp"].(float64)), int64(c["iat"].(float64))
			if iat != a.clock.Now().Unix() && !slices.Contains(seen, secret) || exp-iat > 15777000 || exp <= a.clock.Now().Unix() {
				bad = append(bad, string(payload))
				return false
			}
			if !slices.Contains(seen, secret) {
				seen = append(seen, secret)
			}
			return true
		}
		f.addUser("ann", testidp.Claims{"sub": "s-ann", "email": "ann@icloud.test", "email_verified": "true", "is_private_email": "false"})
		relay := "q8x1b@privaterelay.appleid.com"
		f.addUser("relay", testidp.Claims{"sub": "s-relay", "email": relay, "email_verified": "true", "is_private_email": "true"})
		admin := a.setup("kazuha", pw)

		body := map[string]any{"template": "apple", "clientId": idpClient,
			"apple": map[string]any{"teamId": "TEAM123456", "keyId": "KEY1234567", "privateKey": keyPEM}}
		// What the secret is made of is checked; a client secret is not taken.
		withSecret := map[string]any{"template": "apple", "clientId": idpClient, "clientSecret": "x", "apple": body["apple"]}
		admin.do("POST", "/api/admin/providers", withSecret).want(t, 400, "invalid")
		admin.do("POST", "/api/admin/providers", map[string]any{"template": "apple", "clientId": idpClient, "apple": map[string]any{"teamId": "TEAM123456", "keyId": "KEY1234567"}}).want(t, 400, "invalid")
		admin.do("POST", "/api/admin/providers", map[string]any{"template": "apple", "clientId": idpClient, "apple": map[string]any{"teamId": "team", "keyId": "KEY1234567", "privateKey": keyPEM}}).want(t, 400, "invalid")
		admin.do("POST", "/api/admin/providers", map[string]any{"template": "apple", "clientId": idpClient, "apple": map[string]any{"teamId": "TEAM123456", "keyId": "KEY1234567", "privateKey": "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n"}}).want(t, 400, "apple_key_invalid")
		r := admin.do("POST", "/api/admin/providers", body).want(t, 201, "")
		ap := r.body["apple"].(map[string]any)
		if r.body["template"] != "apple" || r.body["class"] != "social" || ap["teamId"] != "TEAM123456" || ap["keyId"] != "KEY1234567" || ap["hasKey"] != true || r.body["oidc"].(map[string]any)["hasSecret"] != true {
			t.Fatal(r.raw)
		}
		if strings.Contains(r.raw, "PRIVATE KEY") || strings.Contains(r.raw, strings.Split(keyPEM, "\n")[1]) {
			t.Fatal("the key is in the answer")
		}
		// The key is the site's secret, and changed only here.
		sec := admin.do("GET", "/api/admin/secrets", nil).want(t, 200, "")
		if !strings.Contains(sec.raw, `"name":"signin.appleKey","set":true`) {
			t.Fatal(sec.raw)
		}
		admin.do("PUT", "/api/admin/secrets/signin.appleKey", map[string]any{"value": keyPEM}).want(t, 409, "conflict")
		// One per site: its key is the one secret.
		admin.do("POST", "/api/admin/providers", map[string]any{"template": "apple", "slug": "apple2", "clientId": idpClient, "apple": body["apple"]}).want(t, 409, "exists")

		pointAt(t, a, "apple", f.srv.URL, f.srv.URL)
		hc := f.srv.Client()
		hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		signIn := func(who, user string) (string, *client) {
			t.Helper()
			c := a.newClient("")
			st := c.do("POST", "/api/auth/oidc/apple/start", map[string]any{}).want(t, 200, "")
			au, _ := url.Parse(st.body["url"].(string))
			if au.Query().Get("response_mode") != "form_post" || au.Query().Get("scope") != "openid email name" {
				t.Fatal(au)
			}
			resp, err := hc.Get(st.body["url"].(string) + "&user=" + who)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			loc, _ := url.Parse(resp.Header.Get("Location"))
			// Apple posts the answer from its site.
			form := url.Values{"code": {loc.Query().Get("code")}, "state": {loc.Query().Get("state")}}
			if user != "" {
				form.Set("user", user)
			}
			page := c.do("POST", "/api/auth/oidc/apple/callback", form.Encode(), "Content-Type", "application/x-www-form-urlencoded",
				"Sec-Fetch-Site", "cross-site", "Origin", "https://appleid.apple.com")
			m := metaRefresh.FindStringSubmatch(page.raw)
			if page.code != 200 || m == nil || !strings.HasPrefix(page.resp.Header.Get("Content-Type"), "text/html") || page.resp.Header.Get("Cache-Control") != "no-store" {
				t.Fatalf("%d %s", page.code, page.raw)
			}
			next := html.UnescapeString(m[1])
			if !strings.HasPrefix(next, "/api/auth/oidc/apple/callback?") {
				t.Fatal(next)
			}
			// What Apple posts of the person (the name, the address) stays on the
			// server, with the attempt: the address the browser goes on to (its
			// history, the logs) has only the code and the state.
			if nu, _ := url.Parse(next); len(nu.Query()) != 2 || nu.Query().Get("code") == "" || nu.Query().Get("state") == "" ||
				strings.Contains(next, "Ann") || strings.Contains(next, "Lee") || strings.Contains(next, "icloud") || strings.Contains(page.raw, "Ann") {
				t.Fatalf("the forwarding address carries personal data: %s", next)
			}
			out, _ := finish(t, c, next)
			return out, c
		}
		loc, c := signIn("ann", `{"name":{"firstName":"Ann","lastName":"Lee"},"email":"ann@icloud.test"}`)
		if loc != "/" {
			t.Fatalf("%s %v", loc, bad)
		}
		u := c.do("GET", "/api/session", nil).user()
		if u["name"] != "Ann Lee" || u["email"] != "ann@icloud.test" || u["emailVerified"] != true {
			t.Fatal(u)
		}
		if by, _ := store.EmailVerifiedBy(ctx, a.st.Q(), mustUserID(t, a, signedInAs(t, c))); by != "provider:apple" {
			t.Fatal(by)
		}
		// Kept while it is far from its end.
		a.clock.Advance(100 * 24 * time.Hour)
		if loc, _ := signIn("ann", ""); loc != "/" {
			t.Fatal(loc)
		}
		mu.Lock()
		if len(seen) != 1 || len(bad) != 0 {
			t.Fatalf("%d secrets, refused %v", len(seen), bad)
		}
		mu.Unlock()
		// Near its end (a week left), a new one is signed at the next use.
		a.clock.Advance(74 * 24 * time.Hour)
		if loc, _ := signIn("ann", ""); loc != "/" {
			t.Fatalf("%s %v", loc, bad)
		}
		mu.Lock()
		if len(seen) != 2 || seen[0] == seen[1] || len(bad) != 0 {
			t.Fatalf("%d secrets, refused %v", len(seen), bad)
		}
		mu.Unlock()

		// A relay address is verified, but another account's hold on it is
		// left alone: the new account is made without it.
		holder, err := a.auth.CreateUser(ctx, auth.CreateParams{Username: "holder", Role: "member", Password: pw, Email: relay, EmailVerified: true})
		if err != nil {
			t.Fatal(err)
		}
		loc, c = signIn("relay", "")
		if loc != "/" || signedInAs(t, c) == "holder" || signedInAs(t, c) == "" {
			t.Fatalf("%s %s", loc, signedInAs(t, c))
		}
		if u := c.do("GET", "/api/session", nil).user(); u["email"] == relay {
			t.Fatal(u)
		}
		if h, _ := store.GetUser(ctx, a.st.Q(), store.AllTenants(), holder.ID); h.Email != relay || !h.EmailVerified {
			t.Fatalf("%+v", h)
		}

		// A post for a provider that did not ask for form_post is not taken.
		admin = a.newClient("")
		admin.do("POST", "/api/login", map[string]any{"username": "kazuha", "password": pw}).want(t, 200, "")
		admin.do("POST", "/api/admin/providers", map[string]any{"template": "google", "clientId": "g", "clientSecret": "s"}).want(t, 201, "")
		g := a.newClient("").do("POST", "/api/auth/oidc/google/callback", "code=x&state=y", "Content-Type", "application/x-www-form-urlencoded")
		if g.code != 303 || g.resp.Header.Get("Location") != "/login?sso_error=provider_unavailable" {
			t.Fatal(g.code, g.raw)
		}
		// Deleting the provider removes its key.
		admin.do("DELETE", "/api/admin/providers/apple?force=1", nil).want(t, 204, "")
		if sec := admin.do("GET", "/api/admin/secrets", nil).want(t, 200, ""); !strings.Contains(sec.raw, `"name":"signin.appleKey","set":false`) {
			t.Fatal(sec.raw)
		}
	})
}
