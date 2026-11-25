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

	"github.com/KKazuhaK/PetalBook/internal/api"
	"github.com/KKazuhaK/PetalBook/internal/auth"
	"github.com/KKazuhaK/PetalBook/internal/connections"
	"github.com/KKazuhaK/PetalBook/internal/dailyfetch"
	"github.com/KKazuhaK/PetalBook/internal/drafts"
	"github.com/KKazuhaK/PetalBook/internal/forge"
	"github.com/KKazuhaK/PetalBook/internal/forge/forgetest"
	"github.com/KKazuhaK/PetalBook/internal/github"
	"github.com/KKazuhaK/PetalBook/internal/githubapp"
	"github.com/KKazuhaK/PetalBook/internal/githubapp/githubapptest"
	"github.com/KKazuhaK/PetalBook/internal/gitstore"
	"github.com/KKazuhaK/PetalBook/internal/oauth"
	"github.com/KKazuhaK/PetalBook/internal/secret"
	"github.com/KKazuhaK/PetalBook/internal/spaces"
	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
	"github.com/KKazuhaK/PetalBook/internal/tokens"
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
	// With accOpts.forges: a fake GitLab and a fake Gitea, and the manager of the
	// providers an administrator sets up.
	gl, gt *forgetest.Fake
	fm     *forge.Manager
}

type accOpts struct {
	public  string
	oidc    *auth.OIDCConfig
	httpc   *http.Client
	proxies []string
	mod     func(*auth.Config)
	// maxSpaces is how many spaces one user may own (0: no limit).
	maxSpaces int
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
	var gl, gt *forgetest.Fake
	var fm *forge.Manager
	var box *secret.Box
	if o.github || o.forges {
		box = secret.NewBoxWithKey([32]byte{4, 5, 6})
	}
	if o.github {
		fake = githubapptest.New(t, clk)
		gc, err := githubapp.New(githubapp.Config{API: fake.URL, Web: fake.URL, Now: clk.Now})
		if err != nil {
			t.Fatal(err)
		}
		if ghsvc, err = github.New(github.Options{Store: st, Secrets: box, Client: gc, PublicURL: o.public, Now: clk.Now, SettingsTTL: time.Nanosecond}); err != nil {
			t.Fatal(err)
		}
		reg = connections.NewRegistry(ghsvc, connections.NewGit())
	} else if o.forges {
		reg = connections.NewRegistry(connections.NewGit())
	}
	if o.forges {
		gl, gt = forgetest.NewGitLab(t, clk), forgetest.NewGitea(t, clk)
		var err error
		if fm, err = forge.NewManager(ctx, forge.Options{Store: st, Secrets: box, Registry: reg, PublicURL: o.public, Now: clk.Now, Unsafe: true}); err != nil {
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
	cfg := auth.Config{Logger: log, Store: st, Spaces: mgr, PublicURL: o.public, TrustedProxies: trusted, OIDC: o.oidc, HTTPClient: o.httpc,
		Password: fastPasswords, Now: clk.Now}
	if o.mod != nil {
		o.mod(&cfg)
	}
	svc, err := auth.New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// The mail queue is delivered (to a fake server of the test; mail stays off
	// until a test enables it).
	mailCtx, stopMail := context.WithCancel(ctx)
	t.Cleanup(stopMail)
	go svc.RunMail(mailCtx)
	// Nearly every test signs in from one address, many times: the allowance of
	// a minute is lifted here, and the tests of the limits set it back.
	var lift auth.SecurityPatch
	if err := json.Unmarshal([]byte(`{"limits":{"perMinute":100000}}`), &lift); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateSecurity(ctx, "", "", lift); err != nil {
		t.Fatal(err)
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
	opts := api.Options{Logger: log, Store: st, Spaces: mgr, Drafts: drf, Auth: svc, Tokens: tok, OAuth: oa, PublicURL: o.public, Version: "test-1", DailyFetch: o.dailyFetch, Connections: reg, Now: clk.Now, PrefsQuiet: o.prefsQuiet}
	if o.maxSpaces > 0 {
		n := o.maxSpaces // --max-spaces given: the site's quota.spacesPerPerson
		opts.MaxSpaces = &n
	}
	h := api.New(opts)
	// Registered after the manager's: cleanups run last first, so the handler's
	// background work has ended before the spaces under it are closed.
	t.Cleanup(h.Close)
	return &acc{apiOpts: opts, t: t, dir: dir, st: st, mgr: mgr, auth: svc, drafts: drf, tokens: tok, oauth: oa, clock: clk, h: h, public: o.public, fake: fake, ghsvc: ghsvc, gl: gl, gt: gt, fm: fm}
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
	c.do("POST", "/api/login", map[string]any{"username": username, "password": password}).want(a.t, 200, "")
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
