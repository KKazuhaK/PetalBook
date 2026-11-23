package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/auth"
	"github.com/KKazuhaK/PetalBook/internal/auth/testidp"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

const (
	testPublic = "https://notes.example.test"
	idpClient  = "petalbook-test"
	idpSecret  = "a-long-test-secret"
)

type fakeIDP struct {
	idp *testidp.IdP
	srv *httptest.Server
}

func newFakeIDP(t *testing.T) *fakeIDP {
	t.Helper()
	idp := testidp.New("", idpClient, idpSecret, testPublic+"/api/auth/oidc/callback")
	srv := httptest.NewTLSServer(idp.Handler())
	t.Cleanup(srv.Close)
	idp.Issuer = srv.URL
	idp.Users = map[string]testidp.Claims{
		"alice":  {"sub": "sub-alice", "preferred_username": "Alice", "email": "alice@example.test", "name": "Alice A"},
		"bob":    {"sub": "sub-bob", "preferred_username": "bob", "email": "bob@example.test", "name": "Bob B"},
		"alice2": {"sub": "sub-alice-2", "preferred_username": "alice", "name": "Another Alice"},
		"noname": {"sub": "sub-noname", "email": "q.r@example.test"},
	}
	return &fakeIDP{idp, srv}
}

func (f *fakeIDP) config(autoCreate bool) *auth.OIDCConfig {
	return &auth.OIDCConfig{Issuer: f.srv.URL, ClientID: idpClient, ClientSecret: idpSecret, Name: "Company SSO", AutoCreate: autoCreate}
}

func (f *fakeIDP) opts(autoCreate bool) accOpts {
	return accOpts{public: testPublic, oidc: f.config(autoCreate), httpc: f.srv.Client()}
}

// signIn runs the browser's part of a sign-in as the identity-provider user
// `who`: start, follow the authorization redirect, and return the callback's
// path and query, without calling it yet.
type attempt struct {
	start result
	cb    string // path and query of the redirect back to the app
	state string
}

func (f *fakeIDP) begin(t *testing.T, c *client, who string, body map[string]any) attempt {
	t.Helper()
	return f.beginAt(t, c, "/api/auth/oidc/start", "/api/auth/oidc/callback", who, body)
}

// beginAt starts a sign-in at startPath and follows the provider's redirect back
// to callbackPath.
func (f *fakeIDP) beginAt(t *testing.T, c *client, startPath, callbackPath, who string, body map[string]any) attempt {
	t.Helper()
	r := c.do("POST", startPath, body).want(t, 200, "")
	authURL := r.body["url"].(string)
	if !strings.HasPrefix(authURL, f.srv.URL+"/authorize?") {
		t.Fatalf("authorization URL %s", authURL)
	}
	hc := f.srv.Client()
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := hc.Get(authURL + "&user=" + url.QueryEscape(who))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 302 {
		t.Fatalf("the provider answered %d", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || loc.Scheme+"://"+loc.Host != testPublic || loc.Path != callbackPath {
		t.Fatalf("redirect back to %v", resp.Header.Get("Location"))
	}
	return attempt{start: r, cb: loc.RequestURI(), state: loc.Query().Get("state")}
}

// finish calls the callback and returns the redirect it answers with.
func finish(t *testing.T, c *client, cb string, extra ...string) (string, result) {
	t.Helper()
	r := c.do("GET", cb, nil, extra...)
	if r.code != 303 {
		t.Fatalf("callback answered %d, not a redirect: %s", r.code, r.raw)
	}
	if r.resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("callback answer is cacheable")
	}
	return r.resp.Header.Get("Location"), r
}

func (f *fakeIDP) signIn(t *testing.T, c *client, who string, body map[string]any) string {
	t.Helper()
	at := f.begin(t, c, who, body)
	loc, _ := finish(t, c, at.cb)
	return loc
}

func signedInAs(t *testing.T, c *client) string {
	t.Helper()
	u := c.do("GET", "/api/session", nil).want(t, 200, "").user()
	if u == nil {
		return ""
	}
	return u["username"].(string)
}

func TestOIDCStartIsBoundToTheBrowser(t *testing.T) {
	t.Parallel()
	f := newFakeIDP(t)
	eachAccounts(t, f.opts(false), func(t *testing.T, a *acc) {
		a.setup("kazuha", pw)
		c := a.newClient("")
		r := c.do("POST", "/api/auth/oidc/start", map[string]any{"return": "/s/x"}).want(t, 200, "")
		u, _ := url.Parse(r.body["url"].(string))
		q := u.Query()
		if q.Get("redirect_uri") != testPublic+"/api/auth/oidc/callback" || q.Get("code_challenge_method") != "S256" || q.Get("state") == "" || q.Get("nonce") == "" ||
			q.Get("client_id") != idpClient || q.Get("response_type") != "code" || q.Get("scope") != "openid profile email" {
			t.Fatalf("%v", q)
		}
		ck := r.resp.Cookies()[0]
		if ck.Name != "__Host-petalbook_oidc" || !ck.HttpOnly || !ck.Secure || ck.MaxAge != 600 || ck.Path != "/" || ck.SameSite != http.SameSiteLaxMode || ck.Value == "" {
			t.Fatalf("%+v", ck)
		}
		// Everything the client cannot do.
		c.do("POST", "/api/auth/oidc/start", map[string]any{"intent": "bogus"}).want(t, 400, "invalid")
		c.do("POST", "/api/auth/oidc/start", map[string]any{"intent": "link"}).want(t, 401, "unauthenticated")
		c.do("POST", "/api/auth/oidc/start", map[string]any{"token": strings.Repeat("x", 300)}).want(t, 400, "invalid")
		// An empty body means a plain sign-in.
		c.do("POST", "/api/auth/oidc/start", "").want(t, 200, "")
		// Methods advertise the provider.
		m := c.do("GET", "/api/session", nil).body["methods"].(map[string]any)
		if m["oidc"].(map[string]any)["name"] != "Company SSO" {
			t.Fatal(m)
		}
	})
}

func TestOIDCNotConfigured(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		a.newClient("").do("POST", "/api/auth/oidc/start", map[string]any{}).want(t, 404, "not_found")
	})
}

func TestOIDCStartsAreRateLimited(t *testing.T) {
	t.Parallel()
	f := newFakeIDP(t)
	eachAccounts(t, f.opts(false), func(t *testing.T, a *acc) {
		c := a.newClient("203.0.113.77")
		for range 20 {
			c.do("POST", "/api/auth/oidc/start", map[string]any{}).want(t, 200, "")
		}
		r := c.do("POST", "/api/auth/oidc/start", map[string]any{}).want(t, 429, "rate_limited")
		if r.resp.Header.Get("Retry-After") == "" {
			t.Fatal("no Retry-After")
		}
		a.newClient("203.0.113.78").do("POST", "/api/auth/oidc/start", map[string]any{}).want(t, 200, "")
	})
}

func TestOIDCSetupCreatesTheAdministrator(t *testing.T) {
	t.Parallel()
	f := newFakeIDP(t)
	eachAccounts(t, f.opts(true), func(t *testing.T, a *acc) {
		c := a.newClient("")
		tok := a.auth.SetupToken()
		// Auto-create never makes the first account: a plain sign-in finds nobody.
		if loc := f.signIn(t, c, "alice", map[string]any{}); loc != "/login?sso_error=no_account" {
			t.Fatal(loc)
		}
		if loc := f.signIn(t, c, "alice", map[string]any{"intent": "setup", "token": "wrong"}); loc != "/login?sso_error=setup_invalid" {
			t.Fatal(loc)
		}
		if loc := f.signIn(t, c, "alice", map[string]any{"intent": "setup"}); loc != "/login?sso_error=setup_invalid" {
			t.Fatal(loc)
		}
		if signedInAs(t, c) != "" {
			t.Fatal("a refused setup signed someone in")
		}
		at := f.begin(t, c, "alice", map[string]any{"intent": "setup", "token": tok, "return": "/s/start"})
		loc, r := finish(t, c, at.cb)
		if loc != "/s/start" {
			t.Fatal(loc)
		}
		var session, binding *http.Cookie
		for _, ck := range r.resp.Cookies() {
			switch ck.Name {
			case "__Host-petalbook_session":
				session = ck
			case "__Host-petalbook_oidc":
				binding = ck
			}
		}
		if session == nil || !session.HttpOnly || !session.Secure || binding == nil || binding.MaxAge >= 0 {
			t.Fatalf("cookies %v", r.resp.Cookies())
		}
		s := c.do("GET", "/api/session", nil).want(t, 200, "")
		u := s.user()
		if u["username"] != "alice" || u["name"] != "Alice A" || u["role"] != "admin" || u["email"] != "alice@example.test" || u["emailVerified"] != false || s.body["setup"] != false {
			t.Fatal(s.raw)
		}
		c.personalSpace()
		acct := c.do("GET", "/api/account", nil).want(t, 200, "")
		if acct.body["hasPassword"] != false {
			t.Fatal(acct.raw)
		}
		id := acct.body["identities"].([]any)[0].(map[string]any)
		if id["provider"] != "Company SSO" || id["label"] != "alice@example.test" {
			t.Fatal(acct.raw)
		}
		// Over: the token is dead and so is the endpoint.
		if loc := f.signIn(t, a.newClient(""), "bob", map[string]any{"intent": "setup", "token": tok}); loc != "/login?sso_error=setup_invalid" {
			t.Fatal(loc)
		}
		a.newClient("").do("POST", "/api/setup", map[string]any{"token": tok, "username": "x1", "password": pw}).want(t, 409, "exists")
	})
}

func TestOIDCLoginAndAutoCreate(t *testing.T) {
	t.Parallel()
	f := newFakeIDP(t)
	for _, auto := range []bool{false, true} {
		eachAccounts(t, f.opts(auto), func(t *testing.T, a *acc) {
			admin := a.setup("kazuha", pw)
			_ = admin
			c := a.newClient("")
			loc := f.signIn(t, c, "alice", map[string]any{})
			if !auto {
				if loc != "/login?sso_error=no_account" || signedInAs(t, c) != "" {
					t.Fatalf("auto-create off: %s", loc)
				}
				return
			}
			if loc != "/" || signedInAs(t, c) != "alice" {
				t.Fatalf("auto-create on: %s %q", loc, signedInAs(t, c))
			}
			u := c.do("GET", "/api/session", nil).user()
			if u["role"] != "member" || u["name"] != "Alice A" {
				t.Fatalf("%v", u)
			}
			c.personalSpace()
			// The same identity signs into the same account next time.
			c2 := a.newClient("")
			if loc := f.signIn(t, c2, "alice", map[string]any{"return": "/s/x?y=1"}); loc != "/s/x?y=1" || signedInAs(t, c2) != "alice" {
				t.Fatal(loc)
			}
			if n := len(mustUsers(t, a)); n != 2 {
				t.Fatalf("%d users", n)
			}
			// Another identity with the same preferred username gets a free one.
			c3 := a.newClient("")
			f.signIn(t, c3, "alice2", map[string]any{})
			if got := signedInAs(t, c3); got != "alice-2" {
				t.Fatalf("%q", got)
			}
			// No name claim: the name is the username, taken from the email.
			c4 := a.newClient("")
			f.signIn(t, c4, "noname", map[string]any{})
			u = c4.do("GET", "/api/session", nil).user()
			if u["username"] != "q.r" || u["name"] != "q.r" {
				t.Fatalf("%v", u)
			}
		})
	}
}

func mustUsers(t *testing.T, a *acc) []store.User {
	t.Helper()
	us, err := a.auth.Users(context.Background(), store.AllTenants())
	if err != nil {
		t.Fatal(err)
	}
	return us
}

func TestOIDCNeverMatchesAccountsByEmail(t *testing.T) {
	t.Parallel()
	f := newFakeIDP(t)
	for _, auto := range []bool{false, true} {
		eachAccounts(t, f.opts(auto), func(t *testing.T, a *acc) {
			// A local account whose email is the one the provider reports for alice.
			c := a.setup("kazuha", pw)
			c.do("PATCH", "/api/account", map[string]any{"email": "alice@example.test"}).want(t, 200, "")
			a.clock.Advance(time.Minute)
			other := a.newClient("")
			f.signIn(t, other, "alice", map[string]any{})
			got := signedInAs(t, other)
			if got == "kazuha" {
				t.Fatal("signed into an account because the email matched")
			}
			if auto && got != "alice" {
				t.Fatalf("auto-create: %q", got)
			}
			if !auto && got != "" {
				t.Fatalf("%q", got)
			}
			// And the local account has no identity.
			if ids := c.do("GET", "/api/account", nil).body["identities"].([]any); len(ids) != 0 {
				t.Fatal(ids)
			}
		})
	}
}

func TestOIDCInvite(t *testing.T) {
	t.Parallel()
	f := newFakeIDP(t)
	eachAccounts(t, f.opts(false), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		token := admin.do("POST", "/api/invites", map[string]any{"role": "admin"}).want(t, 201, "").body["token"].(string)

		c := a.newClient("")
		if loc := f.signIn(t, c, "bob", map[string]any{"intent": "invite", "token": "wrong"}); loc != "/login?sso_error=invite_invalid" {
			t.Fatal(loc)
		}
		if loc := f.signIn(t, c, "bob", map[string]any{"intent": "invite"}); loc != "/login?sso_error=invite_invalid" {
			t.Fatal(loc)
		}
		if loc := f.signIn(t, c, "bob", map[string]any{"intent": "invite", "token": token, "return": "/welcome"}); loc != "/welcome" {
			t.Fatal(loc)
		}
		s := c.do("GET", "/api/session", nil).want(t, 200, "")
		if s.user()["username"] != "bob" || s.user()["role"] != "admin" || s.user()["name"] != "Bob B" {
			t.Fatal(s.raw)
		}
		c.personalSpace()
		// It worked once.
		if loc := f.signIn(t, a.newClient(""), "alice", map[string]any{"intent": "invite", "token": token}); loc != "/login?sso_error=invite_invalid" {
			t.Fatal(loc)
		}
		// A person who already has an identity cannot use an invite with it.
		t2 := admin.do("POST", "/api/invites", nil).want(t, 201, "").body["token"].(string)
		if loc := f.signIn(t, a.newClient(""), "bob", map[string]any{"intent": "invite", "token": t2}); loc != "/login?sso_error=identity_in_use" {
			t.Fatal(loc)
		}
		// ... and the invite is still good for someone else.
		c3 := a.newClient("")
		if loc := f.signIn(t, c3, "alice", map[string]any{"intent": "invite", "token": t2}); loc != "/" || signedInAs(t, c3) != "alice" {
			t.Fatal(loc)
		}
		// Expired.
		t3 := admin.do("POST", "/api/invites", map[string]any{"days": 1}).want(t, 201, "").body["token"].(string)
		a.clock.Advance(25 * time.Hour)
		if loc := f.signIn(t, a.newClient(""), "noname", map[string]any{"intent": "invite", "token": t3}); loc != "/login?sso_error=invite_invalid" {
			t.Fatal(loc)
		}
	})
}

func TestOIDCLinkAndUnlink(t *testing.T) {
	t.Parallel()
	f := newFakeIDP(t)
	eachAccounts(t, f.opts(false), func(t *testing.T, a *acc) {
		kazuha := a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")

		// Linking needs a session at the start ...
		a.newClient("").do("POST", "/api/auth/oidc/start", map[string]any{"intent": "link"}).want(t, 401, "unauthenticated")
		// ... and leaves it as it is.
		at := f.begin(t, kazuha, "alice", map[string]any{"intent": "link", "return": "/settings"})
		sessionBefore := kazuha.cookies["__Host-petalbook_session"].Value
		loc, r := finish(t, kazuha, at.cb)
		if loc != "/settings" || kazuha.cookies["__Host-petalbook_session"].Value != sessionBefore {
			t.Fatalf("%s", loc)
		}
		for _, ck := range r.resp.Cookies() {
			if ck.Name == "__Host-petalbook_session" {
				t.Fatal("linking replaced the session")
			}
		}
		ids := kazuha.do("GET", "/api/account", nil).body["identities"].([]any)
		if len(ids) != 1 || ids[0].(map[string]any)["label"] != "alice@example.test" {
			t.Fatalf("%v", ids)
		}
		// Linking the same identity again changes nothing.
		if loc := f.signIn(t, kazuha, "alice", map[string]any{"intent": "link", "return": "/settings"}); loc != "/settings" {
			t.Fatal(loc)
		}
		if ids := kazuha.do("GET", "/api/account", nil).body["identities"].([]any); len(ids) != 1 {
			t.Fatal(ids)
		}
		// Someone else cannot take it.
		if loc := f.signIn(t, tang, "alice", map[string]any{"intent": "link"}); loc != "/settings?sso_error=identity_in_use" {
			t.Fatal(loc)
		}
		// Signing in with it reaches the linked account.
		c := a.newClient("")
		f.signIn(t, c, "alice", map[string]any{})
		if signedInAs(t, c) != "kazuha" {
			t.Fatal("the linked identity does not sign into its account")
		}
		// The browser must still be signed in as the one who asked.
		at = f.begin(t, tang, "bob", map[string]any{"intent": "link"})
		tang.do("POST", "/api/logout", nil).want(t, 204, "")
		if loc, _ := finish(t, tang, at.cb); loc != "/settings?sso_error=expired" {
			t.Fatal(loc)
		}
		at = f.begin(t, kazuha, "bob", map[string]any{"intent": "link"})
		swap := a.newClient("")
		swap.do("POST", "/api/login", map[string]any{"username": "tang", "password": pw}).want(t, 200, "")
		swap.cookies["__Host-petalbook_oidc"] = kazuha.cookies["__Host-petalbook_oidc"] // the same browser binding, another account's session
		if loc, _ := finish(t, swap, at.cb); loc != "/settings?sso_error=expired" {
			t.Fatal(loc)
		}
		// Unlinking: the account has a password, so the identity can go.
		idn := kazuha.do("GET", "/api/account", nil).body["identities"].([]any)[0].(map[string]any)["id"].(string)
		kazuha.do("DELETE", "/api/account/identities/"+idn, nil).want(t, 204, "")
		d := a.newClient("")
		if loc := f.signIn(t, d, "alice", map[string]any{}); loc != "/login?sso_error=no_account" {
			t.Fatal(loc)
		}
	})
}

func TestOIDCDisabledUser(t *testing.T) {
	t.Parallel()
	f := newFakeIDP(t)
	eachAccounts(t, f.opts(true), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		c := a.newClient("")
		f.signIn(t, c, "alice", map[string]any{})
		var id string
		for _, u := range admin.do("GET", "/api/users", nil).body["users"].([]any) {
			if u.(map[string]any)["username"] == "alice" {
				id = u.(map[string]any)["id"].(string)
			}
		}
		admin.do("PATCH", "/api/users/"+id, map[string]any{"disabled": true}).want(t, 200, "")
		c.do("GET", "/api/account", nil).want(t, 401, "unauthenticated") // existing sessions end
		d := a.newClient("")
		if loc := f.signIn(t, d, "alice", map[string]any{}); loc != "/login?sso_error=disabled" || signedInAs(t, d) != "" {
			t.Fatal(loc)
		}
	})
}

func TestOIDCReplayExpiryAndForgery(t *testing.T) {
	t.Parallel()
	f := newFakeIDP(t)
	eachAccounts(t, f.opts(true), func(t *testing.T, a *acc) {
		a.setup("kazuha", pw)

		// A callback works once.
		c := a.newClient("")
		at := f.begin(t, c, "alice", map[string]any{})
		if loc, _ := finish(t, c, at.cb); loc != "/" {
			t.Fatal(loc)
		}
		replayer := a.newClient("")
		replayer.cookies["__Host-petalbook_oidc"] = &http.Cookie{Name: "__Host-petalbook_oidc", Value: "stolen-or-stale"}
		if loc, _ := finish(t, replayer, at.cb); loc != "/login?sso_error=expired" {
			t.Fatal(loc)
		}
		// Even with the right cookie: it was used.
		again := a.newClient("")
		for k, v := range c.cookies {
			again.cookies[k] = v
		}
		again.cookies["__Host-petalbook_oidc"] = &http.Cookie{Name: "__Host-petalbook_oidc", Value: at.start.resp.Cookies()[0].Value}
		if loc, r := finish(t, again, at.cb); loc != "/login?sso_error=expired" {
			t.Fatalf("%s %v", loc, r.resp.Cookies())
		}

		// Older than ten minutes.
		c = a.newClient("")
		at = f.begin(t, c, "bob", map[string]any{})
		a.clock.Advance(10*time.Minute + time.Second)
		if loc, _ := finish(t, c, at.cb); loc != "/login?sso_error=expired" || signedInAs(t, c) != "" {
			t.Fatal(loc)
		}
		// Just inside ten minutes is fine.
		c = a.newClient("")
		at = f.begin(t, c, "bob", map[string]any{})
		a.clock.Advance(9*time.Minute + 50*time.Second)
		if loc, _ := finish(t, c, at.cb); loc != "/" {
			t.Fatal(loc)
		}

		// No cookie: a callback from a browser that did not start the attempt.
		c = a.newClient("")
		at = f.begin(t, c, "alice2", map[string]any{})
		stranger := a.newClient("")
		if loc, _ := finish(t, stranger, at.cb); loc != "/login?sso_error=expired" || signedInAs(t, stranger) != "" {
			t.Fatal(loc)
		}
		// A browser with its own attempt cannot finish someone else's, and does not burn it.
		own := a.newClient("")
		f.begin(t, own, "bob", map[string]any{})
		if loc, r := finish(t, own, at.cb); loc != "/login?sso_error=idp_error" || signedInAs(t, own) != "" {
			t.Fatalf("%s %v", loc, r.resp.Cookies())
		}
		if loc, _ := finish(t, c, at.cb); loc != "/" || signedInAs(t, c) != "alice-2" {
			t.Fatal("the real attempt was cancelled by a forged callback")
		}

		// Repeated, missing and malformed parameters.
		c = a.newClient("")
		at = f.begin(t, c, "alice", map[string]any{})
		if loc, _ := finish(t, c, at.cb+"&state=other"); loc != "/login?sso_error=idp_error" {
			t.Fatal(loc)
		}
		if loc, _ := finish(t, c, at.cb+"&code=other"); loc != "/login?sso_error=idp_error" {
			t.Fatal(loc)
		}
		if loc, _ := finish(t, c, "/api/auth/oidc/callback"); loc != "/login?sso_error=idp_error" {
			t.Fatal(loc)
		}
		if loc, _ := finish(t, c, "/api/auth/oidc/callback?state="+at.state); loc != "/login?sso_error=idp_error" {
			t.Fatal(loc)
		}
		// The real one still works after all of that.
		if loc, _ := finish(t, c, at.cb); loc != "/" {
			t.Fatal(loc)
		}

		// The provider refuses: only the matching browser's attempt is ended.
		c = a.newClient("")
		at = f.begin(t, c, "alice", map[string]any{})
		if loc, _ := finish(t, a.newClient(""), "/api/auth/oidc/callback?error=access_denied&state="+at.state); loc != "/login?sso_error=idp_error" {
			t.Fatal(loc)
		}
		if loc, _ := finish(t, c, "/api/auth/oidc/callback?error=access_denied&state="+at.state); loc != "/login?sso_error=idp_error" {
			t.Fatal(loc)
		}
		if loc, _ := finish(t, c, at.cb); loc != "/login?sso_error=expired" {
			t.Fatalf("a provider error did not end the attempt: %s", loc)
		}
		// A refusal during a link goes back to settings.
		k := a.newClient("")
		k.do("POST", "/api/login", map[string]any{"username": "kazuha", "password": pw}).want(t, 200, "")
		at = f.begin(t, k, "bob", map[string]any{"intent": "link"})
		if loc, _ := finish(t, k, "/api/auth/oidc/callback?error=access_denied&state="+at.state); loc != "/settings?sso_error=idp_error" {
			t.Fatal(loc)
		}
	})
}

func TestOIDCRejectsTokensThatDoNotVerify(t *testing.T) {
	t.Parallel()
	f := newFakeIDP(t)
	eachAccounts(t, f.opts(true), func(t *testing.T, a *acc) {
		a.setup("kazuha", pw)
		for name, mutate := range map[string]func(map[string]any){
			"wrong audience": func(c map[string]any) { c["aud"] = "someone-else" },
			"wrong issuer":   func(c map[string]any) { c["iss"] = "https://evil.example" },
			"wrong nonce":    func(c map[string]any) { c["nonce"] = "not-the-nonce" },
			"expired":        func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() },
			"no subject":     func(c map[string]any) { c["sub"] = "" },
		} {
			f.idp.Mutate = mutate
			c := a.newClient("")
			if loc := f.signIn(t, c, "alice", map[string]any{}); loc != "/login?sso_error=idp_error" || signedInAs(t, c) != "" {
				t.Errorf("%s: %s", name, loc)
			}
		}
		f.idp.Mutate = nil
		f.idp.TokenStatus = 500
		c := a.newClient("")
		if loc := f.signIn(t, c, "alice", map[string]any{}); loc != "/login?sso_error=idp_error" {
			t.Error(loc)
		}
		f.idp.TokenStatus = 0
		if n := len(mustUsers(t, a)); n != 1 {
			t.Fatalf("a refused sign-in made an account: %d users", n)
		}
	})
}

func TestOIDCReturnPathCannotLeaveTheApp(t *testing.T) {
	t.Parallel()
	f := newFakeIDP(t)
	eachAccounts(t, f.opts(true), func(t *testing.T, a *acc) {
		a.setup("kazuha", pw)
		for ret, want := range map[string]string{
			"/s/abc?x=1":           "/s/abc?x=1",
			"/":                    "/",
			"//evil.example":       "/",
			"https://evil.example": "/",
			"/\\evil.example":      "/",
			"/\t/evil.example":     "/",
			"javascript:alert(1)":  "/",
			"evil.example":         "/",
			"/api/session":         "/",
			"/login":               "/",
			"":                     "/",
		} {
			c := a.newClient("")
			if got := f.signIn(t, c, "alice", map[string]any{"return": ret}); got != want {
				t.Errorf("return %q went to %q, want %q", ret, got, want)
			}
		}
	})
}

func TestOIDCProviderDown(t *testing.T) {
	t.Parallel()
	f := newFakeIDP(t)
	eachAccounts(t, f.opts(false), func(t *testing.T, a *acc) {
		f.srv.Close()
		r := a.newClient("").do("POST", "/api/auth/oidc/start", map[string]any{}).want(t, 500, "internal")
		if strings.Contains(r.raw, f.srv.URL) {
			t.Fatal("the answer leaks the provider address")
		}
	})
}
