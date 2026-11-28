package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/auth/testidp"
	"petalbook.app/server/internal/store"
)

func TestOAuth2LinkAndTest(t *testing.T) {
	t.Parallel()
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		g := newFakeOAuth2(t)
		g.users["neo"] = discordUser(1, "Neo", "neo@example.test", true)
		admin := a.setup("kazuha", pw)
		b := g.body("disco", map[string]any{"enabled": false})
		b["oauth2"].(map[string]any)["fields"].(map[string]any)["username"] = "missing.path"
		admin.do("POST", "/api/admin/providers", b).want(t, 201, "")
		// Off: nobody signs in ...
		c := a.newClient("")
		if r := c.do("GET", "/api/auth/oauth2/disco/start", nil); r.code != 303 || r.resp.Header.Get("Location") != "/login?sso_error=provider_unavailable" {
			t.Fatalf("%d %s", r.code, r.resp.Header.Get("Location"))
		}
		// ... but the administrator tests it, and reads what it said once,
		// with the paths that found nothing.
		cb, _ := g.beginAt(t, admin, "/api/admin/providers/disco/test", "disco", "neo", map[string]any{})
		if end, _ := finish(t, admin, cb, "Sec-Fetch-Site", "cross-site"); end != "/admin/security/providers?provider_test=disco" {
			t.Fatal(end)
		}
		r := admin.do("GET", "/api/admin/providers/disco/test", nil).want(t, 200, "")
		res := r.body["result"].(map[string]any)
		if res["subject"] != "1" || res["email"] != "neo@example.test" || res["name"] != "Neo" || res["emailTrusted"] != false {
			t.Fatal(r.raw)
		}
		if un, _ := res["unresolved"].([]any); len(un) != 1 || un[0] != "missing.path" {
			t.Fatal(r.raw)
		}
		if linkCount(t, a, "disco") != 0 || signedInAs(t, admin) != "kazuha" {
			t.Fatal("a test linked or signed in")
		}
		// Other kinds answer an empty list, never null.
		f := newIDPFor(t, "one")
		f.addUser("alice", testidp.Claims{"sub": "s-a", "preferred_username": "alice", "email": "a@example.test", "name": "A"})
		admin.do("POST", "/api/admin/providers", f.providerBody("one", map[string]any{"enabled": false})).want(t, 201, "")
		at := f.beginAt(t, admin, "/api/admin/providers/one/test", "/api/auth/oidc/one/callback", "alice", map[string]any{})
		finish(t, admin, at.cb)
		if rr := admin.do("GET", "/api/admin/providers/one/test", nil).want(t, 200, ""); !strings.Contains(rr.raw, `"unresolved":[]`) {
			t.Fatal(rr.raw)
		}

		// On, and linked from an account.
		admin.do("PATCH", "/api/admin/providers/disco", map[string]any{"enabled": true, "oauth2": map[string]any{"fields": map[string]any{"username": "username"}}}).want(t, 200, "")
		tang := a.addUser("tang", pw, "member")
		if end := g.signIn(t, tang, "disco", "neo", map[string]any{"intent": "link", "return": "/settings"}); end != "/settings" {
			t.Fatal(end)
		}
		if ids := tang.do("GET", "/api/account/links", nil).body["links"].([]any); len(ids) != 1 {
			t.Fatal(ids)
		}
		// Signing in at it now is tang.
		d := a.newClient("")
		if end := g.signIn(t, d, "disco", "neo", nil); end != "/" || signedInAs(t, d) != "tang" {
			t.Fatalf("%s %q", end, signedInAs(t, d))
		}
		// Someone else cannot link the same identity.
		pat := a.addUser("pat", pw, "member")
		if end := g.signIn(t, pat, "disco", "neo", map[string]any{"intent": "link"}); !strings.Contains(end, "sso_error=identity_in_use") {
			t.Fatal(end)
		}
	})
}

// Every way the provider can answer wrongly ends in an error and no session.
func TestOAuth2RefusedAnswers(t *testing.T) {
	t.Parallel()
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		g := newFakeOAuth2(t)
		g.users["neo"] = discordUser(1, "Neo", "neo@example.test", true)
		admin := a.setup("kazuha", pw)
		admin.putSetting("registration.mode", "open").want(t, 200, "")
		admin.do("POST", "/api/admin/providers", g.body("disco", nil)).want(t, 201, "")
		user := func(id any) { g.users["neo"] = map[string]any{"id": id, "username": "neo"} }
		cases := []struct {
			name string
			set  func()
		}{
			{"no subject", func() { g.users["neo"] = map[string]any{"username": "neo"} }},
			{"null subject", func() { user(nil) }},
			{"float subject", func() { user(json.RawMessage("1.5")) }},
			{"float subject with a zero", func() { user(json.RawMessage("1.0")) }},
			{"exponent subject", func() { user(json.RawMessage("1e3")) }},
			{"object subject", func() { user(map[string]any{"a": 1}) }},
			{"array subject", func() { user([]any{1}) }},
			{"bool subject", func() { user(true) }},
			{"blank subject", func() { user("   ") }},
			{"long subject", func() { user(strings.Repeat("s", 300)) }},
			{"control character in the subject", func() { user("a\u0007b") }},
			{"userinfo 401", func() { g.meStatus = 401 }},
			{"userinfo 500", func() { g.meStatus = 500 }},
			// Each redirect leads to a valid answer: only refusing to follow it passes.
			{"userinfo redirect", func() { g.meRedirect = g.srv.URL + "/me-ok" }},
			{"token redirect", func() { g.tokenRedirect = true }},
			{"userinfo not JSON", func() { g.meRaw = "<html>hello</html>" }},
			{"userinfo an array", func() { g.meRaw = `[{"id":1}]` }},
			{"userinfo a string", func() { g.meRaw = `"x"` }},
			{"userinfo trailing text", func() { g.meRaw = `{"id":1} {"id":2}` }},
			{"userinfo over 1 MiB", func() { g.meSize = 1<<20 + 10 }}, // a valid object, then spaces past 1 MiB
			{"no access token", func() { g.noToken = true }},
		}
		for i, tc := range cases {
			g.set(func() {
				g.users["neo"] = discordUser(1, "Neo", "neo@example.test", true)
				g.meStatus, g.meRaw, g.meSize, g.meRedirect, g.noToken, g.tokenRedirect = 0, "", 0, "", false, false
				tc.set()
			})
			c := a.newClient(fmt.Sprintf("203.0.113.%d", i+1)) // each start is rate limited by address
			if end := g.signIn(t, c, "disco", "neo", nil); end != "/login?sso_error=idp_error" || signedInAs(t, c) != "" {
				t.Fatalf("%s: %s %q", tc.name, end, signedInAs(t, c))
			}
		}
		if n := linkCount(t, a, "disco") + rowCount(t, a, `SELECT COUNT(*) FROM users WHERE username = 'neo'`); n != 0 {
			t.Fatalf("%d rows were made", n)
		}
		if rows := admin.audit(t, "login_failed"); len(rows) != len(cases) || rows[0]["detail"].(map[string]any)["method"] != "oauth2" {
			t.Fatalf("%d rows", len(rows))
		}
		// The wrong client authentication method: the provider wants the form.
		g.set(func() {
			g.users["neo"] = discordUser(1, "Neo", "neo@example.test", true)
			g.meStatus, g.meRaw, g.meSize, g.meRedirect, g.noToken, g.tokenRedirect, g.post = 0, "", 0, "", false, false, true
		})
		c := a.newClient("")
		if end := g.signIn(t, c, "disco", "neo", nil); end != "/login?sso_error=idp_error" || signedInAs(t, c) != "" {
			t.Fatalf("%s", end)
		}
		// And the right one works.
		admin.do("PATCH", "/api/admin/providers/disco", map[string]any{"oauth2": map[string]any{"authMethod": "client_secret_post"}}).want(t, 200, "")
		if end := g.signIn(t, a.newClient(""), "disco", "neo", nil); end != "/" {
			t.Fatal(end)
		}
	})
}

func TestOAuth2UserinfoTimesOut(t *testing.T) {
	t.Parallel()
	o := providersOpts()
	o.mod = func(c *auth.Config) {
		c.OAuth2RequestTimeout, c.OAuth2FlowTimeout = 100*time.Millisecond, 200*time.Millisecond
	}
	eachAccounts(t, o, func(t *testing.T, a *acc) {
		g := newFakeOAuth2(t)
		g.users["neo"] = discordUser(1, "Neo", "", nil)
		g.meDelay = 5 * time.Second
		admin := a.setup("kazuha", pw)
		admin.putSetting("registration.mode", "open").want(t, 200, "")
		admin.do("POST", "/api/admin/providers", g.body("disco", nil)).want(t, 201, "")
		c := a.newClient("")
		start := time.Now()
		if end := g.signIn(t, c, "disco", "neo", nil); end != "/login?sso_error=idp_error" || signedInAs(t, c) != "" {
			t.Fatalf("%s", end)
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Fatalf("took %s", d)
		}
	})
}

// A callback that read the provider before an administrator changed it is
// refused: what it read means nothing under the new settings.
func TestOAuth2CallbackRefusedWhenTheProviderChangedMeanwhile(t *testing.T) {
	t.Parallel()
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		g := newFakeOAuth2(t)
		g.users["neo"] = discordUser(1, "Neo", "neo@example.test", true)
		admin := a.setup("kazuha", pw)
		admin.putSetting("registration.mode", "open").want(t, 200, "")
		admin.do("POST", "/api/admin/providers", g.body("disco", nil)).want(t, 201, "")
		g.set(func() {
			g.meHook = func() {
				a.clock.Advance(time.Second)
				admin.do("PATCH", "/api/admin/providers/disco", map[string]any{"oauth2": map[string]any{"fields": map[string]any{"subject": "username"}}}).want(t, 200, "")
			}
		})
		c := a.newClient("")
		if end := g.signIn(t, c, "disco", "neo", nil); end != "/login?sso_error=provider_unavailable" || signedInAs(t, c) != "" {
			t.Fatalf("%s %q", end, signedInAs(t, c))
		}
		if n := linkCount(t, a, "disco") + rowCount(t, a, `SELECT COUNT(*) FROM users WHERE username = 'neo'`); n != 0 {
			t.Fatalf("%d rows were made", n)
		}
		// The next sign-in reads the new settings and works.
		g.set(func() { g.meHook = nil })
		d := a.newClient("")
		if end := g.signIn(t, d, "disco", "neo", nil); end != "/" || signedInAs(t, d) != "neo" {
			t.Fatalf("%s %q", end, signedInAs(t, d))
		}
		if _, err := store.FindLink(context.Background(), a.st.Q(), "disco", "neo"); err != nil {
			t.Fatal(err)
		}
	})
}

func TestOAuth2StateVerifierAndSlug(t *testing.T) {
	t.Parallel()
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		g := newFakeOAuth2(t)
		g.users["neo"] = discordUser(1, "Neo", "", nil)
		admin := a.setup("kazuha", pw)
		admin.putSetting("registration.mode", "open").want(t, 200, "")
		admin.do("POST", "/api/admin/providers", g.body("disco", nil)).want(t, 201, "")
		admin.do("POST", "/api/admin/providers", g.body("other", nil)).want(t, 201, "")

		// A replayed callback.
		c := a.newClient("")
		cb, _ := g.begin(t, c, "disco", "neo", nil)
		if end, _ := finish(t, c, cb); end != "/" {
			t.Fatal(end)
		}
		d := a.newClient("")
		if end, _ := finish(t, d, cb); end != "/login?sso_error=expired" || signedInAs(t, d) != "" {
			t.Fatal(end)
		}
		if end, _ := finish(t, c, cb); end != "/login?sso_error=expired" {
			t.Fatal(end)
		}
		// A callback from another browser: refused, and the real attempt stands.
		e, f := a.newClient(""), a.newClient("")
		cb, _ = g.begin(t, e, "disco", "neo", nil)
		g.begin(t, f, "disco", "neo", nil) // f has an attempt of its own
		if end, _ := finish(t, f, cb); end != "/login?sso_error=idp_error" || signedInAs(t, f) != "" {
			t.Fatal(end)
		}
		if end, _ := finish(t, e, cb); end != "/" {
			t.Fatal(end)
		}
		// The state of one provider at another's callback.
		h := a.newClient("")
		cb, _ = g.begin(t, h, "disco", "neo", nil)
		wrong := strings.Replace(cb, "/oauth2/disco/", "/oauth2/other/", 1)
		if end, _ := finish(t, h, wrong); end != "/login?sso_error=expired" || signedInAs(t, h) != "" {
			t.Fatal(end)
		}
		// A verifier the provider cannot match.
		g.set(func() { g.breakPKCE = true })
		i := a.newClient("")
		if end := g.signIn(t, i, "disco", "neo", nil); end != "/login?sso_error=idp_error" || signedInAs(t, i) != "" {
			t.Fatal(end)
		}
		// A callback the provider answers with an error.
		g.set(func() { g.breakPKCE = false })
		j := a.newClient("")
		cb, _ = g.begin(t, j, "disco", "neo", nil)
		u, _ := url.Parse(cb)
		st := u.Query().Get("state")
		if end, _ := finish(t, j, "/api/auth/oauth2/disco/callback?error=access_denied&state="+url.QueryEscape(st)); end != "/login?sso_error=idp_error" || signedInAs(t, j) != "" {
			t.Fatal(end)
		}
		// Nothing but this provider's own kind starts at its slug.
		a.newClient("").do("POST", "/api/auth/oidc/disco/start", map[string]any{}).want(t, 404, "not_found")
		a.newClient("").do("POST", "/api/auth/oauth2/nope/start", map[string]any{}).want(t, 404, "not_found")
	})
}

// An address is trusted only when the provider says so by its path AND the
// site trusts the provider's addresses. An address that is not trusted never
// finds or takes an account.
func TestOAuth2EmailTrust(t *testing.T) {
	t.Parallel()
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		g := newFakeOAuth2(t)
		n := 10
		for _, k := range []struct {
			key string
			v   any
		}{{"ver", true}, {"verstr", "true"}, {"unver", false}, {"no", nil}, {"yes", "yes"}} {
			n++
			g.users[k.key] = discordUser(n, k.key, k.key+"@example.test", k.v)
		}
		g.users["nopath"] = discordUser(99, "nopath", "nopath@example.test", true)
		admin := a.setup("kazuha", pw)
		admin.putSetting("registration.mode", "open").want(t, 200, "")
		// Path set, the site does not trust the provider's addresses: unverified.
		admin.do("POST", "/api/admin/providers", g.body("disco", nil)).want(t, 201, "")
		c := a.newClient("")
		g.signIn(t, c, "disco", "ver", nil)
		if u := c.do("GET", "/api/session", nil).user(); u["emailVerified"] != false || u["email"] != "ver@example.test" {
			t.Fatal(u)
		}
		// Trusted, and the answer is true or "true": verified.
		admin.do("PATCH", "/api/admin/providers/disco", map[string]any{"mergeVerifiedEmail": true}).want(t, 200, "")
		c = a.newClient("")
		g.signIn(t, c, "disco", "verstr", nil)
		if u := c.do("GET", "/api/session", nil).user(); u["emailVerified"] != true || u["email"] != "verstr@example.test" {
			t.Fatal(u)
		}
		// The answer false, null or anything else: not verified.
		for _, k := range []string{"unver", "no", "yes"} {
			c := a.newClient("")
			g.signIn(t, c, "disco", k, nil)
			if u := c.do("GET", "/api/session", nil).user(); u["emailVerified"] != false {
				t.Fatalf("%s: %v", k, u)
			}
		}
		// Trusted, but no emailVerified path: never verified.
		b := g.body("nop", map[string]any{"mergeVerifiedEmail": true})
		delete(b["oauth2"].(map[string]any)["fields"].(map[string]any), "emailVerified")
		admin.do("POST", "/api/admin/providers", b).want(t, 201, "")
		c = a.newClient("")
		g.signIn(t, c, "nop", "nopath", nil)
		if u := c.do("GET", "/api/session", nil).user(); u["emailVerified"] != false || u["email"] != "nopath@example.test" {
			t.Fatal(u)
		}

		// An address another account holds verified: a trusted sign-in is sent to
		// link from that account; an untrusted one never is, and takes nothing.
		if _, err := a.auth.CreateUser(context.Background(), auth.CreateParams{Username: "holder", Role: "member", Password: pw, Email: "victim@example.test", EmailVerified: true}); err != nil {
			t.Fatal(err)
		}
		g.users["victim"] = discordUser(500, "Vic", "victim@example.test", true)
		d := a.newClient("")
		if end := g.signIn(t, d, "disco", "victim", nil); end != "/login?sso_error=email_in_use" || signedInAs(t, d) != "" {
			t.Fatalf("%s %q", end, signedInAs(t, d))
		}
		g.users["victim2"] = discordUser(501, "Vic2", "victim@example.test", false)
		e := a.newClient("")
		end := g.signIn(t, e, "disco", "victim2", nil)
		if who := signedInAs(t, e); end != "/" || who == "holder" || who == "" {
			t.Fatalf("%s %q", end, who)
		}
		if u := e.do("GET", "/api/session", nil).user(); u["email"] != "" {
			t.Fatal(u)
		}
		if u := userTenant(t, a, "holder"); u.Email != "victim@example.test" || !u.EmailVerified {
			t.Fatalf("%+v", u)
		}
		// The site stops trusting the provider: the same answer is not trusted.
		admin.do("PATCH", "/api/admin/providers/disco", map[string]any{"mergeVerifiedEmail": false}).want(t, 200, "")
		g.users["victim3"] = discordUser(502, "Vic3", "victim@example.test", true)
		f := a.newClient("")
		end = g.signIn(t, f, "disco", "victim3", nil)
		if who := signedInAs(t, f); end != "/" || who == "holder" || who == "" {
			t.Fatalf("%s %q", end, who)
		}
		if u := userTenant(t, a, "holder"); u.Email != "victim@example.test" || !u.EmailVerified {
			t.Fatalf("%+v", u)
		}
	})
}

// A plain OAuth 2.0 provider is social: it never claims an account an
// administrator made for the organisation's single sign-on.
func TestOAuth2NeverClaimsAWaitingAccount(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		g := newFakeOAuth2(t)
		g.users["alice"] = discordUser(7, "Alice", "alice@acme.example", true)
		f := ssoClaimFx(t, a, idp, "open")
		f.root.do("POST", "/api/admin/providers", g.body("disco", map[string]any{"mergeVerifiedEmail": true})).want(t, 201, "")
		f.admB.do("POST", adminUsers, map[string]any{"username": "al", "email": "alice@acme.example"}).want(t, 201, "")
		c := a.newClient("")
		g.signIn(t, c, "disco", "alice", nil)
		if signedInAs(t, c) != "" || waiting(t, a) != 1 {
			t.Fatalf("claimed by a plain OAuth 2.0 provider: %q", signedInAs(t, c))
		}
		if u := userTenant(t, a, "al"); u.EmailVerified || u.Email != "alice@acme.example" {
			t.Fatalf("%+v", u)
		}
	})
}

func TestOAuth2AdminRules(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		g := newFakeOAuth2(t)
		g.users["neo"] = discordUser(1, "Neo", "", nil)
		f := ssoClaimFx(t, a, idp, "open")
		admin := f.root
		n := 0
		post := func(mut func(o map[string]any, top map[string]any)) result {
			n++
			b := g.body(fmt.Sprintf("d-%d", n), nil)
			mut(b["oauth2"].(map[string]any), b)
			return admin.do("POST", "/api/admin/providers", b)
		}
		// Addresses: https only, no userinfo part, query or fragment, 2048 at most.
		for _, key := range []string{"authorizationEndpoint", "tokenEndpoint", "userinfoEndpoint"} {
			for _, bad := range []string{"http://x.example/a", "https://u:p@x.example/a", "https://x.example/a?b=1", "https://x.example/a#f", "https://x.example/" + strings.Repeat("a", 2048), "ftp://x.example", "x.example/a", ""} {
				post(func(o, _ map[string]any) { o[key] = bad }).want(t, 400, "")
			}
		}
		// Scopes, method, client.
		post(func(o, _ map[string]any) { o["scopes"] = make([]string, 33) }).want(t, 400, "invalid")
		post(func(o, _ map[string]any) { o["scopes"] = []string{"a b"} }).want(t, 400, "invalid")
		post(func(o, _ map[string]any) { o["authMethod"] = "auto" }).want(t, 400, "invalid")
		post(func(o, _ map[string]any) { o["clientId"] = "" }).want(t, 400, "invalid")
		post(func(o, _ map[string]any) { delete(o, "clientSecret") }).want(t, 400, "invalid")
		post(func(o, _ map[string]any) { delete(o, "tokenEndpoint") }).want(t, 400, "")
		// Paths: dotted names of 1 to 64 characters of A-Za-z0-9_:-, 8 at most, 256 characters.
		for _, bad := range []string{"", "a..b", ".a", "a.", "a b", "a/b", "a[0]", "a.*", strings.Repeat("a", 65), "a.b.c.d.e.f.g.h.i", "é"} {
			post(func(o, _ map[string]any) { o["fields"].(map[string]any)["subject"] = bad }).want(t, 400, "invalid")
		}
		post(func(o, _ map[string]any) { o["fields"].(map[string]any)["email"] = "a b" }).want(t, 400, "invalid")
		post(func(o, _ map[string]any) { delete(o["fields"].(map[string]any), "subject") }).want(t, 400, "invalid")
		post(func(o, _ map[string]any) {
			o["fields"] = map[string]any{"subject": "a.b.c.d.e.f.g.h", "email": "emails.0.email", "name": "a:b-c_d.1"}
		}).want(t, 201, "")
		// Not for another kind, not groups, not the other class, not offered to tenants.
		post(func(_, top map[string]any) { top["oidc"] = map[string]any{"issuer": "https://x.example"} }).want(t, 400, "invalid")
		post(func(_, top map[string]any) { top["groups"] = map[string]any{"enabled": true} }).want(t, 400, "invalid")
		post(func(_, top map[string]any) { top["groups"] = map[string]any{"allow": []string{"x"}} }).want(t, 400, "invalid")
		post(func(_, top map[string]any) { top["class"] = "sso" }).want(t, 400, "invalid")
		post(func(_, top map[string]any) { top["tenants"] = "all" }).want(t, 400, "invalid")
		// An organisation's own provider is never this kind.
		tb := g.body("acme-disco", nil)
		f.admB.do("POST", providersPath(f.tenB), tb).want(t, 400, "invalid")
		tb["class"] = "sso"
		f.admB.do("POST", providersPath(f.tenB), tb).want(t, 400, "invalid")
		if n := rowCount(t, a, `SELECT COUNT(*) FROM providers WHERE owner <> 'site'`); n != 1 { // the organisation's own OpenID Connect one
			t.Fatalf("%d tenant providers", n)
		}

		admin.do("POST", "/api/admin/providers", g.body("disco", nil)).want(t, 201, "")
		admin.do("PATCH", "/api/admin/providers/disco", map[string]any{"oidc": map[string]any{"issuer": "https://x.example"}}).want(t, 400, "invalid")
		admin.do("PATCH", "/api/admin/providers/disco", map[string]any{"groups": map[string]any{"enabled": true}}).want(t, 400, "invalid")
		admin.do("PATCH", "/api/admin/providers/disco", map[string]any{"groups": map[string]any{"allow": []string{"x"}}}).want(t, 400, "invalid")
		admin.do("PATCH", "/api/admin/providers/disco", map[string]any{"class": "sso"}).want(t, 400, "invalid")
		admin.do("PATCH", "/api/admin/providers/disco", map[string]any{"oauth2": map[string]any{"userinfoEndpoint": "http://x.example/me"}}).want(t, 400, "")
		admin.do("PATCH", "/api/admin/providers/disco", map[string]any{"oauth2": map[string]any{"authMethod": "bearer"}}).want(t, 400, "invalid")

		// The secret is sealed, never shown, and stays when it is not sent.
		var enc, cfg string
		q := a.st.Q()
		if err := q.QueryRow(context.Background(), `SELECT secret_enc, config FROM providers WHERE id = 'disco'`).Scan(&enc, &cfg); err != nil || enc == "" || strings.Contains(enc, "o2-secret") || strings.Contains(cfg, "o2-secret") {
			t.Fatalf("%q %q %v", enc, cfg, err)
		}
		admin.do("PATCH", "/api/admin/providers/disco", map[string]any{"name": "Discord"}).want(t, 200, "")
		var enc2 string
		q.QueryRow(context.Background(), `SELECT secret_enc FROM providers WHERE id = 'disco'`).Scan(&enc2)
		if enc2 != enc {
			t.Fatal("the secret changed")
		}
		admin.putSetting("registration.mode", "open").want(t, 200, "")
		if end := g.signIn(t, a.newClient(""), "disco", "neo", nil); end != "/" {
			t.Fatal(end)
		}

		// The audit: the fields as oauth2.*, the secret as "changed".
		admin.do("PATCH", "/api/admin/providers/disco", map[string]any{"oauth2": map[string]any{"clientSecret": "o2-secret", "scopes": []string{"identify"}}}).want(t, 200, "")
		rows := admin.audit(t, "provider_changed")
		last := rows[0]["detail"].(map[string]any)
		if last["oauth2.clientSecret"] != "changed" || fmt.Sprint(last["oauth2.scopes"]) != "[identify]" || strings.Contains(fmt.Sprint(rows), "o2-secret") {
			t.Fatalf("%v", rows)
		}

		// With a person linked, the subject path and the userinfo host stay.
		if linkCount(t, a, "disco") != 1 {
			t.Fatal("no link")
		}
		same := map[string]any{"oauth2": map[string]any{"fields": map[string]any{"subject": "id", "name": "username"}, "userinfoEndpoint": g.srv.URL + "/me2"}}
		admin.do("PATCH", "/api/admin/providers/disco", same).want(t, 200, "") // same host, same subject path
		admin.do("PATCH", "/api/admin/providers/disco", map[string]any{"oauth2": map[string]any{"fields": map[string]any{"subject": "username"}}}).want(t, 409, "subject_fixed")
		admin.do("PATCH", "/api/admin/providers/disco", map[string]any{"oauth2": map[string]any{"userinfoEndpoint": "https://other.example.test/me"}}).want(t, 409, "subject_fixed")
		// With nobody linked they change freely.
		admin.do("POST", "/api/admin/providers", g.body("fresh", nil)).want(t, 201, "")
		admin.do("PATCH", "/api/admin/providers/fresh", map[string]any{"oauth2": map[string]any{"fields": map[string]any{"subject": "username"}, "userinfoEndpoint": "https://other.example.test/me"}}).want(t, 200, "")
	})
}
