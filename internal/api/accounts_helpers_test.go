package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"petalbook.app/server/internal/api"
	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/connections"
	"petalbook.app/server/internal/dailyfetch"
	"petalbook.app/server/internal/domains"
	"petalbook.app/server/internal/domains/domainstest"
	"petalbook.app/server/internal/drafts"
	"petalbook.app/server/internal/forge"
	"petalbook.app/server/internal/forge/forgetest"
	"petalbook.app/server/internal/github"
	"petalbook.app/server/internal/githubapp"
	"petalbook.app/server/internal/githubapp/githubapptest"
	"petalbook.app/server/internal/gitstore"
	"petalbook.app/server/internal/oauth"
	"petalbook.app/server/internal/secret"
	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/spaces"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
	"petalbook.app/server/internal/tokens"
)

// clock is a settable time shared by the services of a fixture.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fastPasswords keeps the tests quick; the real parameters are tested apart.
var fastPasswords = auth.PasswordParams{MemoryKiB: 64, Time: 1, Threads: 1}

type acc struct {
	t      *testing.T
	dir    string
	st     *store.Store
	mgr    *spaces.Manager
	auth   *auth.Service
	drafts *drafts.Service
	tokens *tokens.Service
	oauth  *oauth.Service
	clock  *clock
	h      *api.Handler
	public string
	// apiOpts are the options the handler was made with (see restart).
	apiOpts api.Options
	// With accOpts.github: the fake GitHub and the server's use of it.
	fake  *githubapptest.Fake
	ghsvc *github.Service
	// linkTokens is the one token service of every provider with tokens (as the server has).
	linkTokens *connections.Tokens
	// With accOpts.forges: a fake GitLab and a fake Gitea, and the manager of the
	// providers an administrator sets up.
	gl, gt *forgetest.Fake
	fm     *forge.Manager
	// dns is the DNS the email domains' checks ask; domains the service (its
	// recheck is run by the tests, RecheckDue).
	dns     *domainstest.Fake
	domains *domains.Service
}

type accOpts struct {
	public string
	// sso, when set, is made the site provider "sso" (OpenID Connect, named
	// "Company SSO", on) before the server starts; ssoOpen opens the site's registration
	// mode, so that its sign-ins make accounts (the default, invite, makes none).
	sso     *fakeIDP
	ssoOpen bool
	httpc   *http.Client
	proxies []string
	mod     func(*auth.Config)
	// maxSpaces is how many spaces one user may own (0: no limit).
	maxSpaces int
	// defaultQuota leaves quota.spacesPerPerson at the registry's default (3);
	// otherwise most tests, which make more spaces than that, lift it to 20.
	defaultQuota bool
	// spaces and draftCfg adjust the manager and the draft service.
	spaces   func(*spaces.Options)
	draftCfg func(*drafts.Config)
	// dailyFetch replaces the fetcher of external daily text (tests talk to a server of their own).
	dailyFetch *dailyfetch.Fetcher
	// github gives the server a GitHub service that talks to a fake GitHub.
	github bool
	// forges gives the server the means to set up GitLab and Gitea providers,
	// and a fake host of each.
	forges bool
	// oauth turns the authorization server on; cimd fetches client metadata documents.
	oauth bool
	cimd  *http.Client
	// prefsQuiet is how long a first preference write waits before its commit.
	prefsQuiet time.Duration
}

func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

// eachAccounts runs a test in accounts mode on every available database.
func eachAccounts(t *testing.T, o accOpts, fn func(t *testing.T, a *acc)) {
	t.Helper()
	eachAccountsWith(t, o, fn)
}

func eachAccountsWith(t *testing.T, o accOpts, fn func(t *testing.T, a *acc)) {
	t.Helper()
	needGit(t)
	storetest.Each(t, func(t *testing.T, st *store.Store) { fn(t, newAcc(t, st, o)) })
}

func newAcc(t *testing.T, st *store.Store, o accOpts) *acc {
	t.Helper()
	ctx := context.Background()
	if o.public == "" {
		o.public = "https://notes.example.test"
	}
	clk := &clock{t: time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)}
	// Not t.TempDir(): its name contains the test name, and "#" in a path is not supported.
	base, err := os.MkdirTemp("", "petalbook-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	dir := filepath.Join(base, "data")
	sopt := spaces.Options{Store: st, DataDir: dir, Committer: "PetalBook Test <svc@example.test>", CloseWait: 200 * time.Millisecond,
		CommitIdle: time.Hour, CommitMax: 2 * time.Hour, Now: clk.Now, MaxSpaces: o.maxSpaces}
	var fake *githubapptest.Fake
	var ghsvc *github.Service
	var reg *connections.Registry
	var linkTokens *connections.Tokens
	var gl, gt *forgetest.Fake
	var fm *forge.Manager
	var box *secret.Box
	if o.github || o.forges {
		box = secret.NewBoxWithKey([32]byte{4, 5, 6})
		reg = connections.NewRegistry(connections.NewGit())
		linkTokens = connections.NewTokens(connections.TokenOptions{Store: st, Secrets: box, Registry: reg, Now: clk.Now})
	}
	if o.github {
		fake = githubapptest.New(t, clk)
		gc, err := githubapp.New(githubapp.Config{API: fake.URL, Web: fake.URL, Now: clk.Now})
		if err != nil {
			t.Fatal(err)
		}
		if ghsvc, err = github.New(github.Options{Store: st, Secrets: box, Client: gc, PublicURL: o.public, Now: clk.Now, SettingsTTL: time.Nanosecond, Tokens: linkTokens}); err != nil {
			t.Fatal(err)
		}
		reg.Add(ghsvc)
	}
	if o.forges {
		gl, gt = forgetest.NewGitLab(t, clk), forgetest.NewGitea(t, clk)
		var err error
		if fm, err = forge.NewManager(ctx, forge.Options{Store: st, Secrets: box, Registry: reg, PublicURL: o.public, Now: clk.Now, Unsafe: true, Tokens: linkTokens}); err != nil {
			t.Fatal(err)
		}
	}
	if reg != nil {
		sopt.Secrets, sopt.Connections = box, reg
		sopt.RemotePolicy = gitstore.Policy{Hosts: []string{"*"}, AllowUnsafe: true}
		sopt.PushRetry = []time.Duration{5 * time.Millisecond, 5 * time.Millisecond, 5 * time.Millisecond}
	}
	if o.spaces != nil {
		o.spaces(&sopt)
	}
	mgr, err := spaces.NewAccounts(sopt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mgr.Close() })
	trusted, err := auth.ParseTrustedProxies(o.proxies)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	dns := domainstest.New()
	dom := domains.New(domains.Options{Store: st, Resolver: dns, SiteHost: api.SiteHostOf(o.public), Now: clk.Now, Logger: log})
	cfg := auth.Config{Logger: log, Store: st, Spaces: mgr, PublicURL: o.public, TrustedProxies: trusted, HTTPClient: o.httpc,
		Password: fastPasswords, Now: clk.Now, OrgDomains: dom.OfTenant, ManagedDomain: dom.Managed,
		RouteDomain: dom.Route, RoutedProviders: dom.RoutedProviders, JoinDomain: dom.Join, JoiningTenants: dom.JoiningTenants}
	if o.mod != nil {
		o.mod(&cfg)
	}
	svc, err := auth.New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if o.sso != nil {
		var in auth.ProviderInput
		body, _ := json.Marshal(o.sso.providerBody("sso", map[string]any{"name": "Company SSO", "enabled": true}))
		if err := json.Unmarshal(body, &in); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.CreateProvider(ctx, "", "", in); err != nil {
			t.Fatal(err)
		}
	}
	// The mail queue is delivered (to a fake server of the test; mail stays off
	// until a test enables it).
	mailCtx, stopMail := context.WithCancel(ctx)
	t.Cleanup(stopMail)
	go svc.RunMail(mailCtx)
	// Nearly every test signs in from one address, many times: the allowance of
	// a minute is lifted here, and the tests of the limits set it back.
	if o.ssoOpen {
		if _, err := settings.Of(st).SetSite(ctx, settings.RegistrationMode, settings.Change{HasValue: true, Value: settings.RegOpen}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := settings.Of(st).SetSite(ctx, settings.SigninPerMinute, settings.Change{HasValue: true, Value: int64(100000)}); err != nil {
		t.Fatal(err)
	}
	if !o.defaultQuota && o.maxSpaces == 0 {
		if _, err := settings.Of(st).SetSite(ctx, settings.QuotaSpacesPerPerson, settings.Change{HasValue: true, Value: int64(20)}); err != nil {
			t.Fatal(err)
		}
	}
	dcfg := drafts.Config{Now: clk.Now}
	if o.draftCfg != nil {
		o.draftCfg(&dcfg)
	}
	drf, err := drafts.New(ctx, st, mgr, dcfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ghsvc != nil {
		ghsvc.OnChange(mgr.ConnectionFixed)
	}
	if fm != nil {
		fm.SetHostAllowed(mgr.HostAllowed)
		reg.OnChange(func(_, userID string) { mgr.ConnectionFixed(userID) })
	}
	tok := api.NewTokens(tokens.Config{Store: st, Now: clk.Now, Logger: log}, svc)
	var oa *oauth.Service
	if o.oauth {
		oa, err = oauth.New(ctx, oauth.Config{Store: st, Secrets: mgr.Secrets(), Users: svc, Machine: tok, Issuer: o.public, ClientIP: svc.ClientAddr,
			Logger: log, Now: clk.Now, CIMDClient: o.cimd})
		if err != nil {
			t.Fatal(err)
		}
	}
	opts := api.Options{Logger: log, Store: st, Spaces: mgr, Drafts: drf, Auth: svc, Tokens: tok, OAuth: oa, PublicURL: o.public, Version: "test-1", DailyFetch: o.dailyFetch, Connections: reg, Now: clk.Now, PrefsQuiet: o.prefsQuiet,
		Domains: dom}
	if o.maxSpaces > 0 {
		n := o.maxSpaces // --max-spaces given: the site's quota.spacesPerPerson
		opts.MaxSpaces = &n
	}
	h := api.New(opts)
	// Registered after the manager's: cleanups run last first, so the handler's
	// background work has ended before the spaces under it are closed.
	t.Cleanup(h.Close)
	return &acc{apiOpts: opts, t: t, dir: dir, st: st, mgr: mgr, auth: svc, drafts: drf, tokens: tok, oauth: oa, clock: clk, h: h, public: o.public, fake: fake, ghsvc: ghsvc, linkTokens: linkTokens, gl: gl, gt: gt, fm: fm, dns: dns, domains: dom}
}

// client is a browser: it keeps cookies and has an address.
type client struct {
	a       *acc
	cookies map[string]*http.Cookie
	ip      string
	headers map[string]string
	// versions are the draft versions the client knows of, by page URL.
	versions map[string]string
}

func (a *acc) newClient(ip string) *client {
	if ip == "" {
		ip = "198.51.100.7"
	}
	return &client{a: a, cookies: map[string]*http.Cookie{}, ip: ip, headers: map[string]string{}, versions: map[string]string{}}
}

// result is one response.
type result struct {
	code int
	body map[string]any
	raw  string
	resp *http.Response
}

// do sends a request as it is.
func (c *client) do(method, path string, body any, extra ...string) result {
	c.a.t.Helper()
	var rdr io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rdr = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			c.a.t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Host = strings.TrimPrefix(strings.TrimPrefix(c.a.public, "https://"), "http://")
	req.RemoteAddr = c.ip + ":4321"
	if method != "GET" && method != "HEAD" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	for _, ck := range c.cookies {
		req.AddCookie(&http.Cookie{Name: ck.Name, Value: ck.Value})
	}
	for i := 0; i+1 < len(extra); i += 2 {
		if extra[i+1] == "" {
			req.Header.Del(extra[i])
		} else {
			req.Header.Set(extra[i], extra[i+1])
		}
	}
	rec := httptest.NewRecorder()
	c.a.h.ServeHTTP(rec, req)
	resp := rec.Result()
	for _, ck := range resp.Cookies() {
		if ck.MaxAge < 0 || ck.Value == "" {
			delete(c.cookies, ck.Name)
		} else {
			c.cookies[ck.Name] = ck
		}
	}
	raw := rec.Body.String()
	var out map[string]any
	if strings.HasPrefix(strings.TrimSpace(raw), "{") {
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			c.a.t.Fatalf("%s %s: not JSON: %v\n%s", method, path, err, raw)
		}
	}
	return result{rec.Code, out, raw, resp}
}

func (r result) want(t *testing.T, code int, errCode string) result {
	t.Helper()
	if r.code != code || (errCode != "" && r.body["error"] != errCode) {
		t.Fatalf("want %d %s, got %d %s", code, errCode, r.code, r.raw)
	}
	return r
}

func (r result) user() map[string]any {
	u, _ := r.body["user"].(map[string]any)
	return u
}

// setup creates the first administrator through the HTTP API and returns the signed-in client.
func (a *acc) setup(username, password string) *client {
	a.t.Helper()
	c := a.newClient("")
	c.do("POST", "/api/setup", map[string]any{"token": a.auth.SetupToken(), "username": username, "password": password}).want(a.t, 201, "")
	a.clock.Advance(time.Second) // accounts are listed by creation time
	return c
}

// addUser makes an account through the service and signs it in.
func (a *acc) addUser(username, password, role string) *client {
	a.t.Helper()
	if _, err := a.auth.CreateUser(context.Background(), auth.CreateParams{Username: username, Role: role, Password: password}); err != nil {
		a.t.Fatal(err)
	}
	c := a.newClient("")
	c.do("POST", "/api/login", map[string]any{"identifier": username, "password": password}).want(a.t, 200, "")
	a.clock.Advance(time.Second)
	return c
}

// personalSpace returns the id of the client's personal space.
func (c *client) personalSpace() string {
	c.a.t.Helper()
	r := c.do("GET", "/api/spaces", nil).want(c.a.t, 200, "")
	l := r.body["spaces"].([]any)
	first := l[0].(map[string]any)
	if first["personal"] != true {
		c.a.t.Fatalf("the first space is not the personal one: %v", l)
	}
	return first["id"].(string)
}

const pw = "correct horse battery"

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// testWriter sends log lines to the test's log, so a 500 explains itself.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Helper()
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// dropMembershipCache makes the manager read a user's spaces again (tests add
// memberships behind its back).
func (a *acc) dropMembershipCache(t *testing.T, username string) {
	t.Helper()
	a.mgr.MembershipChanged(mustUserID(t, a, username))
}

// restart makes a new handler over the same database, spaces and services, as a
// restart of the server does: what it reads at its start it reads again.
func (a *acc) restart(t *testing.T) {
	t.Helper()
	a.h.Close()
	a.h = api.New(a.apiOpts)
	t.Cleanup(a.h.Close)
}

// putSetting writes a site setting: PUT /api/admin/settings/{key}.
func (c *client) putSetting(key string, value any) result {
	c.a.t.Helper()
	return c.do("PUT", "/api/admin/settings/"+key, map[string]any{"value": value})
}

// siteValue is the value of one site setting in GET /api/admin/settings.
func (c *client) siteValue(key string) any {
	c.a.t.Helper()
	r := c.do("GET", "/api/admin/settings", nil).want(c.a.t, 200, "")
	for _, it := range r.body["settings"].([]any) {
		if m := it.(map[string]any); m["key"] == key {
			return m["site"].(map[string]any)["value"]
		}
	}
	c.a.t.Fatalf("no site setting %s", key)
	return nil
}

// setSetting writes a site setting through the service, before anyone can
// sign in to do it through the API.
func (a *acc) setSetting(t *testing.T, key string, v any) {
	t.Helper()
	if _, err := settings.Of(a.st).SetSite(t.Context(), key, settings.Change{HasValue: true, Value: v}); err != nil {
		t.Fatal(err)
	}
}

// jsonText is v as JSON text, for comparing what a response holds.
func jsonText(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

// switchFeature turns a feature (features.tokens, features.mcp) off for every
// organisation, which is the value off and a lock, or back to on with nothing
// locked: PUT /api/admin/settings/{key}.
func (c *client) switchFeature(key string, on bool) result {
	c.a.t.Helper()
	lower := any(nil)
	if !on {
		lower = map[string]any{"mode": "lock"}
	}
	return c.do("PUT", "/api/admin/settings/"+key, map[string]any{"value": on, "lower": lower})
}

// siteSettingOf is the value of one site setting (GET /api/admin/settings), read by a caller that may.
func siteSettingOf(t *testing.T, c caller, key string) any {
	t.Helper()
	code, r := c("GET", "/api/admin/settings", nil)
	if code != 200 {
		t.Fatalf("GET /api/admin/settings: %d %v", code, r)
	}
	for _, it := range r["settings"].([]any) {
		if m := it.(map[string]any); m["key"] == key {
			return m["site"].(map[string]any)["value"]
		}
	}
	t.Fatalf("no site setting %s", key)
	return nil
}

// valueOf is the site value in the answer to a write of a setting.
func valueOf(r map[string]any) any { return asMap(r["site"])["value"] }
