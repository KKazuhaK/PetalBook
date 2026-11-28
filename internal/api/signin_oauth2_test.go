package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
)

// --- a plain OAuth 2.0 provider (no OpenID Connect: no id_token, no discovery) ---

type fakeOAuth2 struct {
	srv *httptest.Server
	mu  sync.Mutex
	// users are what /me says, by key: any JSON value (json.RawMessage for the
	// exact text).
	users map[string]any
	codes map[string]o2Grant
	toks  map[string]string // token -> key
	// post: the token endpoint wants the client in the form, not in the header.
	post bool
	// noToken: the token endpoint answers without an access_token.
	noToken bool
	// breakPKCE: /authorize remembers a challenge the verifier cannot match.
	breakPKCE bool
	// /me: a status other than 200, a text to answer instead of the user, a
	// size to pad the answer to, a delay, a redirect.
	meStatus   int
	meRaw      string
	meSize     int
	meDelay    time.Duration
	meRedirect string
	// tokenRedirect: the token endpoint answers with a redirect to /token-ok,
	// which has the valid answer.
	tokenRedirect bool
	lastTok       string
	// meHook runs while /me is being answered (a change made mid-callback).
	meHook func()
	// What the provider saw.
	meHeaders   []http.Header
	tokenAuth   []string // "basic" or "post" per token request
	tokenScopes []string
	paths       []string
}

type o2Grant struct{ key, challenge string }

func newFakeOAuth2(t *testing.T) *fakeOAuth2 {
	t.Helper()
	g := &fakeOAuth2{users: map[string]any{}, codes: map[string]o2Grant{}, toks: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		key := q.Get("user")
		g.mu.Lock()
		defer g.mu.Unlock()
		g.paths = append(g.paths, "/authorize")
		if _, ok := g.users[key]; !ok || q.Get("client_id") != "o2-client" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" || q.Get("response_type") != "code" {
			http.Error(w, "bad request", 400)
			return
		}
		g.tokenScopes = append(g.tokenScopes, q.Get("scope"))
		code := fmt.Sprintf("code-%d", len(g.codes)+1)
		ch := q.Get("code_challenge")
		if g.breakPKCE {
			ch += "x"
		}
		g.codes[code] = o2Grant{key, ch}
		to, _ := url.Parse(q.Get("redirect_uri"))
		v := to.Query()
		v.Set("code", code)
		v.Set("state", q.Get("state"))
		to.RawQuery = v.Encode()
		http.Redirect(w, r, to.String(), http.StatusFound)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		g.mu.Lock()
		defer g.mu.Unlock()
		g.paths = append(g.paths, "/token")
		fail := func() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
		}
		id, secret, basic := r.BasicAuth()
		method := "post"
		if basic {
			method = "basic"
			id, _ = url.QueryUnescape(id)
			secret, _ = url.QueryUnescape(secret)
		} else {
			id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
		}
		g.tokenAuth = append(g.tokenAuth, method)
		if id != "o2-client" || secret != "o2-secret" || (method == "post") != g.post {
			fail()
			return
		}
		gr, ok := g.codes[r.PostForm.Get("code")]
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if !ok || r.PostForm.Get("code_verifier") == "" || base64.RawURLEncoding.EncodeToString(sum[:]) != gr.challenge {
			fail()
			return
		}
		delete(g.codes, r.PostForm.Get("code"))
		if g.tokenRedirect {
			g.lastTok = "tok_" + gr.key
			g.toks[g.lastTok] = gr.key
			http.Redirect(w, r, "/token-ok", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if g.noToken {
			json.NewEncoder(w).Encode(map[string]string{"token_type": "bearer"})
			return
		}
		tok := "tok_" + gr.key
		g.toks[tok] = gr.key
		json.NewEncoder(w).Encode(map[string]string{"access_token": tok, "token_type": "Bearer", "scope": "identify email"})
	})
	// The valid answers a redirect would lead to.
	mux.HandleFunc("GET /token-ok", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		tok := g.lastTok
		g.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"access_token": tok, "token_type": "Bearer"})
	})
	mux.HandleFunc("GET /me-ok", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		key := g.toks[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		user := g.users[key]
		if user == nil {
			user = g.users["neo"]
		}
		g.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(user)
	})
	mux.HandleFunc("GET /me", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.paths = append(g.paths, "/me")
		g.meHeaders = append(g.meHeaders, r.Header.Clone())
		key, ok := g.toks[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		status, raw, size, delay, redir := g.meStatus, g.meRaw, g.meSize, g.meDelay, g.meRedirect
		user, hook := g.users[key], g.meHook
		g.mu.Unlock()
		if hook != nil {
			hook()
		}
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		if !ok {
			http.Error(w, "unauthorized", 401)
			return
		}
		if redir != "" {
			http.Redirect(w, r, redir, http.StatusFound)
			return
		}
		if status != 0 && status != 200 {
			http.Error(w, "no", status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if raw != "" {
			w.Write([]byte(raw))
			return
		}
		b, _ := json.Marshal(user)
		if size > len(b) {
			// A valid object followed by whitespace: only the size check refuses it.
			b = append(b, strings.Repeat(" ", size-len(b))...)
		}
		w.Write(b)
	})
	g.srv = httptest.NewTLSServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

func discordUser(id any, name string, email string, verified any) map[string]any {
	return map[string]any{"id": id, "username": strings.ToLower(name), "global_name": name, "email": email, "verified": verified}
}

func (g *fakeOAuth2) block() map[string]any {
	return map[string]any{
		"authorizationEndpoint": g.srv.URL + "/authorize", "tokenEndpoint": g.srv.URL + "/token", "userinfoEndpoint": g.srv.URL + "/me",
		"scopes": []string{"identify", "email"}, "authMethod": "client_secret_basic",
		"fields":   map[string]any{"subject": "id", "email": "email", "emailVerified": "verified", "name": "global_name", "username": "username"},
		"clientId": "o2-client", "clientSecret": "o2-secret",
	}
}

func (g *fakeOAuth2) body(slug string, extra map[string]any) map[string]any {
	b := map[string]any{"slug": slug, "kind": "oauth2", "name": "Disco", "oauth2": g.block()}
	for k, v := range extra {
		b[k] = v
	}
	return b
}

// begin starts a sign-in as the fake user and returns the callback's address.
func (g *fakeOAuth2) begin(t *testing.T, c *client, slug, who string, body map[string]any) (cb string, authURL string) {
	t.Helper()
	return g.beginAt(t, c, "/api/auth/oauth2/"+slug+"/start", slug, who, body)
}

func (g *fakeOAuth2) beginAt(t *testing.T, c *client, startPath, slug, who string, body map[string]any) (cb string, authURL string) {
	t.Helper()
	r := c.do("POST", startPath, body).want(t, 200, "")
	authURL = r.body["url"].(string)
	hc := anyTLSClient()
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
	if err != nil || loc.Path != "/api/auth/oauth2/"+slug+"/callback" {
		t.Fatalf("redirect back to %v", resp.Header.Get("Location"))
	}
	return loc.RequestURI(), authURL
}

func (g *fakeOAuth2) signIn(t *testing.T, c *client, slug, who string, body map[string]any) string {
	t.Helper()
	cb, _ := g.begin(t, c, slug, who, body)
	end, _ := finish(t, c, cb, "Sec-Fetch-Site", "cross-site")
	return end
}

func (g *fakeOAuth2) set(f func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	f()
}

func linkCount(t *testing.T, a *acc, provider string) int {
	t.Helper()
	return rowCount(t, a, `SELECT COUNT(*) FROM linked_accounts WHERE provider_id = ?`, provider)
}

func TestOAuth2SignInReadsTheUserinfoAndMakesAnAccount(t *testing.T) {
	t.Parallel()
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		g := newFakeOAuth2(t)
		g.users["neo"] = discordUser(json.RawMessage("4711"), "Neo", "neo@example.test", true)
		admin := a.setup("kazuha", pw)
		admin.putSetting("registration.mode", "open").want(t, 200, "")
		p := admin.do("POST", "/api/admin/providers", g.body("disco", nil)).want(t, 201, "")
		o := p.body["oauth2"].(map[string]any)
		if p.body["class"] != "social" || p.body["kind"] != "oauth2" || o["redirectUrl"] != testPublic+"/api/auth/oauth2/disco/callback" || o["hasSecret"] != true ||
			o["authMethod"] != "client_secret_basic" || o["clientId"] != "o2-client" || strings.Contains(p.raw, "o2-secret") || p.body["oidc"] != nil || p.body["github"] != nil {
			t.Fatal(p.raw)
		}
		if f := o["fields"].(map[string]any); f["subject"] != "id" || f["emailVerified"] != "verified" {
			t.Fatal(p.raw)
		}
		// It is on the public list as a social provider.
		l := a.newClient("").do("GET", "/api/auth/providers", nil).want(t, 200, "")
		if !strings.Contains(l.raw, `"oauth2"`) {
			t.Fatal(l.raw)
		}
		c := a.newClient("")
		cb, authURL := g.begin(t, c, "disco", "neo", map[string]any{"return": "/s/x"})
		q, _ := url.Parse(authURL)
		if q.Query().Get("scope") != "identify email" || q.Query().Get("code_challenge_method") != "S256" ||
			q.Query().Get("redirect_uri") != testPublic+"/api/auth/oauth2/disco/callback" || q.Query().Get("client_id") != "o2-client" {
			t.Fatal(authURL)
		}
		end, r := finish(t, c, cb, "Sec-Fetch-Site", "cross-site")
		if end != "/s/x" || signedInAs(t, c) != "neo" {
			t.Fatalf("%s %q", end, signedInAs(t, c))
		}
		if len(r.resp.Cookies()) == 0 {
			t.Fatal("no cookies")
		}
		// The client went in the header, the verifier with it, and the user's
		// token was used for the userinfo alone.
		if len(g.tokenAuth) != 1 || g.tokenAuth[0] != "basic" {
			t.Fatalf("%v", g.tokenAuth)
		}
		if got := strings.Join(g.paths, " "); got != "/authorize /token /me" {
			t.Fatal(got)
		}
		h := g.meHeaders[0]
		if h.Get("Authorization") != "Bearer tok_neo" || h.Get("Accept") != "application/json" || h.Get("User-Agent") != "PetalBook" {
			t.Fatal(h)
		}
		u := c.do("GET", "/api/session", nil).user()
		if u["name"] != "Neo" || u["email"] != "neo@example.test" || u["emailVerified"] != false {
			t.Fatal(u)
		}
		if _, err := store.FindLink(context.Background(), a.st.Q(), "disco", "4711"); err != nil {
			t.Fatal(err)
		}
		links := c.do("GET", "/api/account/links", nil).want(t, 200, "").body["links"].([]any)
		if l := links[0].(map[string]any); l["kind"] != "oauth2" || l["slug"] != "disco" || l["label"] != "neo@example.test" {
			t.Fatal(links)
		}
		if rows := admin.audit(t, "login"); len(rows) == 0 || rows[0]["detail"].(map[string]any)["method"] != "oauth2" {
			t.Fatal(rows)
		}
		// The same person again: the same account.
		d := a.newClient("")
		if end := g.signIn(t, d, "disco", "neo", nil); end != "/" || signedInAs(t, d) != "neo" {
			t.Fatalf("%s %q", end, signedInAs(t, d))
		}
		// The client in the form, when the provider is set so.
		g.set(func() { g.post = true })
		admin.do("PATCH", "/api/admin/providers/disco", map[string]any{"oauth2": map[string]any{"authMethod": "client_secret_post"}}).want(t, 200, "")
		e := a.newClient("")
		if end := g.signIn(t, e, "disco", "neo", nil); end != "/" || g.tokenAuth[len(g.tokenAuth)-1] != "post" {
			t.Fatalf("%s %v", end, g.tokenAuth)
		}
	})
}

// The paths are dotted: objects by name, arrays by index. A number and a string
// of the same digits are the same subject.
func TestOAuth2FieldPathsAndSubjects(t *testing.T) {
	t.Parallel()
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		g := newFakeOAuth2(t)
		g.users["nested"] = map[string]any{
			"data":   map[string]any{"user": map[string]any{"id": json.RawMessage("42"), "login": "Trin", "profile": map[string]any{"display": "Trinity"}}},
			"emails": []any{map[string]any{"email": "trin@example.test", "verified": "true"}, map[string]any{"email": "other@example.test"}},
		}
		g.users["num"] = map[string]any{"id": json.RawMessage("9007199254740993"), "username": "num"}
		g.users["str"] = map[string]any{"id": " 9007199254740993 ", "username": "str"}
		admin := a.setup("kazuha", pw)
		admin.putSetting("registration.mode", "open").want(t, 200, "")
		b := g.body("nest", nil)
		b["oauth2"].(map[string]any)["fields"] = map[string]any{"subject": "data.user.id", "email": "emails.0.email", "emailVerified": "emails.0.verified",
			"name": "data.user.profile.display", "username": "data.user.login"}
		admin.do("POST", "/api/admin/providers", b).want(t, 201, "")
		c := a.newClient("")
		if end := g.signIn(t, c, "nest", "nested", nil); end != "/" || signedInAs(t, c) != "trin" {
			t.Fatalf("%s %q", end, signedInAs(t, c))
		}
		if u := c.do("GET", "/api/session", nil).user(); u["name"] != "Trinity" || u["email"] != "trin@example.test" {
			t.Fatal(u)
		}
		if _, err := store.FindLink(context.Background(), a.st.Q(), "nest", "42"); err != nil {
			t.Fatal(err)
		}
		// 9007199254740993 does not fit a float64: it is kept as written.
		d := a.newClient("")
		b2 := g.body("flat", nil)
		admin.do("POST", "/api/admin/providers", b2).want(t, 201, "")
		if end := g.signIn(t, d, "flat", "num", nil); end != "/" {
			t.Fatal(end)
		}
		if _, err := store.FindLink(context.Background(), a.st.Q(), "flat", "9007199254740993"); err != nil {
			t.Fatal(err)
		}
		e := a.newClient("")
		if end := g.signIn(t, e, "flat", "str", nil); end != "/" || signedInAs(t, e) != signedInAs(t, d) {
			t.Fatalf("%s %q %q", end, signedInAs(t, e), signedInAs(t, d))
		}
		if n := linkCount(t, a, "flat"); n != 1 {
			t.Fatalf("%d links", n)
		}
	})
}

// Creation follows the registration mode of the landing tenant, like every
// other sign-in.
func TestOAuth2AccountsFollowTheRegistrationMode(t *testing.T) {
	t.Parallel()
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		g := newFakeOAuth2(t)
		g.users["neo"] = discordUser(1, "Neo", "", nil)
		g.users["old"] = discordUser(2, "Old", "", nil)
		admin := a.setup("kazuha", pw)
		admin.do("POST", "/api/admin/providers", g.body("disco", nil)).want(t, 201, "")
		inv := admin.do("POST", "/api/invites", nil).want(t, 201, "").body["token"].(string)
		setMode(t, admin, "", "invite")
		if end := g.signIn(t, a.newClient(""), "disco", "neo", nil); end != "/login?sso_error=invite_required" {
			t.Fatal(end)
		}
		c := a.newClient("")
		if end := g.signIn(t, c, "disco", "neo", map[string]any{"intent": "invite", "token": inv, "return": "/welcome"}); end != "/welcome" || signedInAs(t, c) != "neo" {
			t.Fatalf("%s %q", end, signedInAs(t, c))
		}
		setMode(t, admin, "", "closed")
		if end := g.signIn(t, a.newClient(""), "disco", "old", nil); end != "/login?sso_error=registration_closed" {
			t.Fatal(end)
		}
		// Somebody who has an account signs in under any mode.
		if end := g.signIn(t, a.newClient(""), "disco", "neo", nil); end != "/" {
			t.Fatal(end)
		}
		setMode(t, admin, "", "open")
		d := a.newClient("")
		if end := g.signIn(t, d, "disco", "old", nil); end != "/" || signedInAs(t, d) != "old" {
			t.Fatalf("%s %q", end, signedInAs(t, d))
		}
	})
}
