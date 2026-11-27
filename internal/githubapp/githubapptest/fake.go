// Package githubapptest is a fake GitHub for tests: the REST endpoints
// PetalBook uses, the OAuth web flow with PKCE and refresh-token rotation, and
// a smart-HTTP Git server (git http-backend) that accepts only a token that is
// valid for the repository. One server answers for the API and the web base
// URL. Nothing here ever contacts the real GitHub.
//
// The failures the real one has are switches on the Fake: a name already taken,
// an App without the Administration permission, an installation of "selected
// repositories" that does not take new repositories by itself, a repository
// that is "not found" for its first few Git requests, a rate limit with a reset
// time, and expired or revoked refresh tokens.
package githubapptest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"petalbook.app/server/internal/gitstore"
)

// Clocker is a source of time; tests give the fake the one the service under
// test uses.
type Clocker interface{ Now() time.Time }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// SystemClock is the real time, for a test that runs a whole server.
func SystemClock() Clocker { return systemClock{} }

// Clock is a clock tests move by hand; the fake, the client and the service
// under test share one.
type Clock struct {
	mu sync.Mutex
	t  time.Time
}

// NewClock starts at a fixed time.
func NewClock() *Clock { return &Clock{t: time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)} }

// Now is the time.
func (c *Clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

// Advance moves the clock.
func (c *Clock) Advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

var (
	keyOnce sync.Once
	theKey  *rsa.PrivateKey
)

func sharedKey() *rsa.PrivateKey {
	keyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		theKey = k
	})
	return theKey
}

// NewKey makes another RSA key (for a test that changes the App's key).
func NewKey(t testing.TB) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// KeyPEM is a key in PKCS#1 PEM form.
func KeyPEM(k *rsa.PrivateKey) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
}

// Account is a user or an organisation.
type Account struct {
	ID    int64
	Login string
	// Name and Email are what a person's profile says (the sign-in reads them);
	// EmailVerified is whether GitHub lists the address as verified.
	Name          string
	Email         string
	EmailVerified bool
	Org           bool
	Members       map[string]bool // org: logins of members (they can create repositories there)
	// NoCreate: members cannot create repositories in this organisation.
	NoCreate bool
	// NoPrivate: private repositories cannot be created.
	NoPrivate bool
	// Admins: logins of the organisation's owners (GET /user/memberships/orgs/{org}
	// answers role "admin" for them, "member" for the other members).
	Admins map[string]bool
}

// MakeAdmin makes login a member and an owner of the organisation org.
func (f *Fake) MakeAdmin(org, login string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.accounts[strings.ToLower(org)]
	if a.Members == nil {
		a.Members = map[string]bool{}
	}
	if a.Admins == nil {
		a.Admins = map[string]bool{}
	}
	a.Members[strings.ToLower(login)] = true
	a.Admins[strings.ToLower(login)] = true
}

// Install is one installation of the App.
type Install struct {
	ID        int64
	Account   string
	Selection string // "all" or "selected"
	Repos     map[int64]bool
	Suspended bool
	// Deleted: the App was uninstalled.
	Deleted bool
	// NoAdd: the user cannot add repositories to it.
	NoAdd bool
	// Perms is what the installation was granted when it differs from the
	// App's (an App whose permissions changed, and an installation that has not
	// accepted the change); nil: the App's.
	Perms map[string]string
}

// Repo is a repository.
type Repo struct {
	ID      int64
	Owner   string
	Name    string
	Private bool
	Desc    string
	hide    int // Git requests still answered "not found"
}

// repoPerms is the "permissions" of a repository in GitHub's answers.
type repoPerms struct {
	Push  bool `json:"push"`
	Admin bool `json:"admin"`
	Pull  bool `json:"pull"`
}

type instToken struct {
	token   string
	install int64
	repos   []int64
	perms   map[string]string
	expires time.Time
}

type userToken struct {
	seq     int64
	access  string
	login   string
	expires time.Time // zero: none
	revoked bool
}

type refreshToken struct {
	seq     int64
	token   string
	login   string
	expires time.Time
	used    bool
	revoked bool
}

type code struct {
	login, challenge, redirect string
}

type manifestCode struct {
	state string
	m     map[string]any
}

// Request is one request the fake served.
type Request struct {
	Method, Path, Query string
	// Auth is "jwt", "installation", "user", "basic", "git" or "".
	Auth string
}

// Fake is the fake GitHub.
type Fake struct {
	*httptest.Server
	t     testing.TB
	Clock Clocker

	AppID        int64
	Slug, Name   string
	ClientID     string
	ClientSecret string
	Key          *rsa.PrivateKey
	// AppPerms is what the App has: GET /app says so and installation tokens
	// may not ask for more.
	AppPerms map[string]string
	// Public is whether the App can be installed on any account, as GET /app
	// says; nil leaves it out (GitHub does not always say).
	Public *bool
	// PersonalNeedsInstall makes GitHub refuse a user's token a repository in the
	// user's own account unless the App is installed there (403 "Resource not
	// accessible by integration"), as the real one does.
	PersonalNeedsInstall bool
	// PermissionRefusal403 makes a token that asks for a permission the
	// installation lacks answer 403 "Resource not accessible by integration"
	// instead of 422.
	PermissionRefusal403 bool
	// AutoAdd makes a repository created in an account that has an installation
	// of selected repositories join it (GitHub does not promise to).
	AutoAdd bool
	// HiddenGit is how many Git requests (with credentials) a new repository
	// answers "not found" to, as GitHub sometimes does for a few seconds.
	HiddenGit int
	// NoRefresh: user tokens never expire and no refresh token is given.
	NoRefresh bool
	// RejectGit makes Git refuse every credential (a revoked token).
	RejectGit bool
	// AppDown makes GET /app answer 503: GitHub made an App but cannot be asked about it.
	AppDown bool
	// OrgNeedsInstall makes GitHub refuse a user's token a repository in an
	// organisation the App is not installed on ("Resource not accessible by integration").
	OrgNeedsInstall bool
	// BreakRefreshReply makes the answer to a renewal one the client cannot use
	// (after the old refresh token was spent), to test that a pair is kept.
	BreakRefreshReply bool

	mu       sync.Mutex
	nextID   int64
	accounts map[string]*Account // lower login
	installs map[int64]*Install
	repos    map[string]*Repo // lower "owner/name"
	inst     map[string]*instToken
	users    map[string]*userToken
	refresh  map[string]*refreshToken
	codes    map[string]*code
	mcodes   map[string]*manifestCode
	manifest []map[string]any // every manifest posted, newest last
	limitTo  time.Time
	reqs     []Request
	gitRoot  string
	gitUsed  []string
	hidden   int // Git requests answered "not found" because a repository was new
	git      *cgi.Handler
}

// New starts the fake. Git must be installed for the Git side; tests that use it skip otherwise.
func New(t testing.TB, clock Clocker) *Fake {
	t.Helper()
	if clock == nil {
		clock = NewClock()
	}
	yes := true
	f := &Fake{t: t, Clock: clock, Public: &yes, AppID: 424242, Slug: "petalbook-notes", Name: "PetalBook Notes",
		ClientID: "Iv23liFakeClientId", ClientSecret: "fake-client-secret-0123456789", Key: sharedKey(), nextID: 1000,
		AppPerms: map[string]string{"administration": "write", "contents": "write", "members": "read", "metadata": "read"},
		accounts: map[string]*Account{}, installs: map[int64]*Install{}, repos: map[string]*Repo{},
		inst: map[string]*instToken{}, users: map[string]*userToken{}, refresh: map[string]*refreshToken{}, codes: map[string]*code{}, mcodes: map[string]*manifestCode{},
		gitRoot: t.TempDir()}
	if gitBin, err := gitstore.Path(); err == nil {
		f.git = &cgi.Handler{Path: gitBin, Args: []string{"http-backend"}, Root: "/", InheritEnv: []string{"PATH"},
			Env: []string{"GIT_PROJECT_ROOT=" + f.gitRoot, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1"}}
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Server.Close)
	return f
}

// KeyPEM is the App's private key.
func (f *Fake) KeyPEM() string { return KeyPEM(f.Key) }

func (f *Fake) id() int64 { f.nextID++; return f.nextID }

// --- setup ------------------------------------------------------------------------

// AddUser makes a GitHub user.
func (f *Fake) AddUser(login string) *Account {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := &Account{ID: f.id(), Login: login}
	f.accounts[strings.ToLower(login)] = a
	return a
}

// AddOrg makes an organisation with these members.
func (f *Fake) AddOrg(login string, members ...string) *Account {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := &Account{ID: f.id(), Login: login, Org: true, Members: map[string]bool{}}
	for _, m := range members {
		a.Members[strings.ToLower(m)] = true
	}
	f.accounts[strings.ToLower(login)] = a
	return a
}

// Account returns an account by login.
func (f *Fake) Account(login string) *Account {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.accounts[strings.ToLower(login)]
}

// SetAccount changes an account's switches under the lock.
func (f *Fake) SetAccount(login string, fn func(*Account)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f.accounts[strings.ToLower(login)])
}

// Install installs the App on an account; selection is "all" or "selected".
func (f *Fake) Install(account, selection string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	in := &Install{ID: f.id(), Account: account, Selection: selection, Repos: map[int64]bool{}}
	f.installs[in.ID] = in
	return in.ID
}

// SetInstall changes an installation under the lock.
func (f *Fake) SetInstall(id int64, fn func(*Install)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f.installs[id])
}

// AddRepo makes a repository (with a Git repository behind it).
func (f *Fake) AddRepo(owner, name string, private bool) *Repo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.newRepoLocked(owner, name, private, "", 0)
}

// SelectRepo adds a repository to an installation's selection.
func (f *Fake) SelectRepo(install int64, repo string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installs[install].Repos[f.repos[strings.ToLower(repo)].ID] = true
}

// RepoByName returns a repository.
func (f *Fake) RepoByName(full string) *Repo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.repos[strings.ToLower(full)]
}

// BareDir is where a repository's Git data is, for tests that look into it.
func (f *Fake) BareDir(full string) string {
	return filepath.Join(f.gitRoot, strings.ToLower(full)+".git")
}

func (f *Fake) newRepoLocked(owner, name string, private bool, desc string, hide int) *Repo {
	r := &Repo{ID: f.id(), Owner: owner, Name: name, Private: private, Desc: desc, hide: hide}
	f.repos[strings.ToLower(owner+"/"+name)] = r
	dir := filepath.Join(f.gitRoot, strings.ToLower(owner), strings.ToLower(name)+".git")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	// A bare repository is a few files; making it by hand keeps git itself out of this package.
	for _, d := range []string{"objects", "refs/heads", "refs/tags"} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(d)), 0o755); err != nil {
			f.t.Fatal(err)
		}
	}
	files := map[string]string{
		"HEAD":   "ref: refs/heads/main\n",
		"config": "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = true\n[http]\n\treceivepack = true\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
	return r
}

// --- the people's side ------------------------------------------------------------

// Authorize plays the browser: it reads the authorisation URL PetalBook made,
// has user login approve it and returns the URL GitHub redirects the browser to.
func (f *Fake) Authorize(authURL, login string) string {
	f.t.Helper()
	u, err := url.Parse(authURL)
	if err != nil || !strings.HasSuffix(u.Path, "/login/oauth/authorize") {
		f.t.Fatalf("not an authorisation URL: %q", authURL)
	}
	q := u.Query()
	if q.Get("client_id") != f.ClientID {
		f.t.Fatalf("the authorisation URL names client %q", q.Get("client_id"))
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" {
		f.t.Fatalf("the authorisation URL has no PKCE or no state: %q", authURL)
	}
	c := randHex(10)
	f.mu.Lock()
	f.codes[c] = &code{login: login, challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri")}
	f.mu.Unlock()
	back, _ := url.Parse(q.Get("redirect_uri"))
	bq := back.Query()
	bq.Set("code", c)
	bq.Set("state", q.Get("state"))
	back.RawQuery = bq.Encode()
	return back.String()
}

// InstallApp plays a person installing the App from its installation page with
// "request user authorization during installation": installURL is the page the
// server sent them to (its state is kept), login is who installs and
// authorises, callback is the App's callback URL. The App is installed on that
// account (all repositories) unless it is there already. It returns the URL
// GitHub then sends the browser to: the code has no PKCE challenge, as GitHub
// gives none to an installation.
func (f *Fake) InstallApp(installURL, login, callback string) string {
	f.t.Helper()
	return f.InstallAppOn(installURL, login, login, callback, true)
}

// InstallAppOn is InstallApp on another account than the person's own (an
// organisation login may install on), and, with authorize false, an App that
// does not ask for authorisation during installation: no code comes back.
func (f *Fake) InstallAppOn(installURL, login, account, callback string, authorize bool) string {
	f.t.Helper()
	u, err := url.Parse(installURL)
	if err != nil || !strings.HasSuffix(u.Path, "/installations/new") {
		f.t.Fatalf("not an installation URL: %q", installURL)
	}
	state := u.Query().Get("state")
	if state == "" {
		f.t.Fatalf("the installation URL carries no state: %q", installURL)
	}
	f.mu.Lock()
	var inst int64
	for _, in := range f.installs {
		if !in.Deleted && strings.EqualFold(in.Account, account) {
			inst = in.ID
		}
	}
	action := "update"
	if inst == 0 {
		in := &Install{ID: f.id(), Account: account, Selection: "all", Repos: map[int64]bool{}}
		f.installs[in.ID] = in
		inst, action = in.ID, "install"
	}
	c := ""
	if authorize {
		c = randHex(10)
		f.codes[c] = &code{login: login, redirect: callback}
	}
	f.mu.Unlock()
	back, _ := url.Parse(callback)
	q := back.Query()
	if c != "" {
		q.Set("code", c)
	}
	q.Set("state", state)
	q.Set("installation_id", strconv.FormatInt(inst, 10))
	q.Set("setup_action", action)
	back.RawQuery = q.Encode()
	return back.String()
}

// SubmitManifest plays GitHub's page for the manifest flow: the browser posted
// manifestJSON to postURL (whose query holds the state); the person accepts the
// App. It returns the URL GitHub sends the browser back to, with a code that
// POST /app-manifests/{code}/conversions turns into the fake's App. The
// manifest is kept for Manifests.
func (f *Fake) SubmitManifest(postURL, manifestJSON string) string {
	f.t.Helper()
	u, err := url.Parse(postURL)
	if err != nil || !strings.HasSuffix(u.Path, "/settings/apps/new") {
		f.t.Fatalf("not a manifest URL: %q", postURL)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(manifestJSON), &m); err != nil {
		f.t.Fatalf("the manifest is not JSON: %v", err)
	}
	redirect, _ := m["redirect_url"].(string)
	back, err := url.Parse(redirect)
	if err != nil || redirect == "" {
		f.t.Fatalf("the manifest has no redirect_url")
	}
	c := randHex(10)
	f.mu.Lock()
	f.mcodes[c] = &manifestCode{state: u.Query().Get("state"), m: m}
	f.manifest = append(f.manifest, m)
	f.mu.Unlock()
	q := back.Query()
	q.Set("code", c)
	if st := u.Query().Get("state"); st != "" {
		q.Set("state", st)
	}
	back.RawQuery = q.Encode()
	return back.String()
}

// Manifests are the manifests submitted so far, oldest first.
func (f *Fake) Manifests() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.manifest...)
}

func (f *Fake) convertManifest(w http.ResponseWriter, c string) {
	f.mu.Lock()
	mc := f.mcodes[c]
	delete(f.mcodes, c) // a code works once
	f.mu.Unlock()
	if mc == nil {
		apiError(w, 404, "Not Found")
		return
	}
	name, _ := mc.m["name"].(string)
	if name == "" {
		name = f.Name
	}
	writeJSON(w, 201, map[string]any{"id": f.AppID, "slug": f.Slug, "name": name, "client_id": f.ClientID,
		"client_secret": f.ClientSecret, "webhook_secret": "fake-webhook-secret-0123456789", "pem": f.KeyPEM(),
		"html_url": "https://github.com/apps/" + f.Slug, "owner": map[string]any{"login": "kazuhahub", "id": 1, "type": "Organization"},
		"permissions": f.AppPerms})
}

// Deny is the redirect when the user cancels.
func (f *Fake) Deny(authURL string) string {
	u, _ := url.Parse(authURL)
	back, _ := url.Parse(u.Query().Get("redirect_uri"))
	bq := back.Query()
	bq.Set("error", "access_denied")
	bq.Set("state", u.Query().Get("state"))
	back.RawQuery = bq.Encode()
	return back.String()
}

// ExpireRefresh makes every refresh token of the user expired.
func (f *Fake) ExpireRefresh(login string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.refresh {
		if strings.EqualFold(r.login, login) {
			r.revoked = true
		}
	}
}

// RevokeUser revokes every token of the user.
func (f *Fake) RevokeUser(login string) {
	f.ExpireRefresh(login)
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.users {
		if strings.EqualFold(u.login, login) {
			u.revoked = true
		}
	}
}

// LimitUntil makes every API request answer with a rate limit until t.
func (f *Fake) LimitUntil(t time.Time) { f.mu.Lock(); f.limitTo = t; f.mu.Unlock() }

// RefreshTokenOf is the current refresh token of a user (the newest).
func (f *Fake) RefreshTokenOf(login string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var best *refreshToken
	for _, r := range f.refresh {
		if strings.EqualFold(r.login, login) && !r.used && !r.revoked && (best == nil || r.seq > best.seq) {
			best = r
		}
	}
	if best == nil {
		return ""
	}
	return best.token
}

// AccessTokenOf is the newest unexpired access token of a user.
func (f *Fake) AccessTokenOf(login string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var best *userToken
	for _, u := range f.users {
		if strings.EqualFold(u.login, login) && !u.revoked && (best == nil || u.seq > best.seq) {
			best = u
		}
	}
	if best == nil {
		return ""
	}
	return best.access
}

// AllTokens lists every token the fake ever handed out (to look for leaks).
func (f *Fake) AllTokens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for k := range f.inst {
		out = append(out, k)
	}
	for k := range f.users {
		out = append(out, k)
	}
	for k := range f.refresh {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// HideRepo makes a repository answer "not found" to its next n Git requests
// that carry credentials.
func (f *Fake) HideRepo(full string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repos[strings.ToLower(full)].hide = n
}

// HiddenServed counts the Git requests answered "not found" on that account.
func (f *Fake) HiddenServed() int { f.mu.Lock(); defer f.mu.Unlock(); return f.hidden }

// Requests are the requests served so far.
func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.reqs...)
}

// Count counts the requests with the method whose path starts with prefix.
func (f *Fake) Count(method, prefix string) int {
	n := 0
	for _, r := range f.Requests() {
		if r.Method == method && strings.HasPrefix(r.Path, prefix) {
			n++
		}
	}
	return n
}

// Minted counts the installation tokens made.
func (f *Fake) Minted() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.inst)
}

// GitTokens are the tokens Git presented, in order.
func (f *Fake) GitTokens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.gitUsed...)
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	const h = "0123456789abcdef"
	out := make([]byte, 2*n)
	for i, x := range b {
		out[2*i], out[2*i+1] = h[x>>4], h[x&15]
	}
	return string(out)
}

// --- serving ------------------------------------------------------------------------

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	// A GitHub Enterprise server's API lives under /api/v3 of its web address
	// (what sign-in with the App's client reads): the same API as the root's.
	if strings.HasPrefix(r.URL.Path, "/api/v3/") {
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/v3")
	}
	path := r.URL.Path
	auth := ""
	switch {
	case strings.Contains(path, ".git/"):
		auth = "git"
	case strings.HasPrefix(path, "/login/oauth/"):
		auth = "web"
	case strings.HasPrefix(r.Header.Get("Authorization"), "Bearer "):
		auth = "bearer"
	case strings.HasPrefix(r.Header.Get("Authorization"), "Basic "):
		auth = "basic"
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, Request{Method: r.Method, Path: path, Query: r.URL.RawQuery, Auth: auth})
	f.mu.Unlock()
	switch auth {
	case "git":
		f.serveGit(w, r)
	case "web":
		f.serveWeb(w, r)
	default:
		f.serveAPI(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func apiError(w http.ResponseWriter, status int, msg string, errs ...string) {
	body := map[string]any{"message": msg}
	if len(errs) > 0 {
		var es []map[string]string
		for _, e := range errs {
			es = append(es, map[string]string{"resource": "Repository", "code": "custom", "field": "name", "message": e})
		}
		body["errors"] = es
	}
	writeJSON(w, status, body)
}

func readBody(r *http.Request, v any) {
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if len(b) > 0 {
		json.Unmarshal(b, v)
	}
}

// principal is who an API request is.
type principal struct {
	kind  string // "jwt", "installation", "user", "client"
	inst  *instToken
	login string
}

func (f *Fake) b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (f *Fake) checkJWT(tok string) bool {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(&f.Key.PublicKey, crypto.SHA256, sum[:], sig) != nil {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var c struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return false
	}
	now := f.Clock.Now()
	return c.Iss == strconv.FormatInt(f.AppID, 10) && now.Unix() >= c.Iat && now.Unix() < c.Exp && c.Exp-c.Iat <= 600
}

// who authenticates an API request; false after answering 401.
func (f *Fake) who(w http.ResponseWriter, r *http.Request) (principal, bool) {
	h := r.Header.Get("Authorization")
	tok := strings.TrimPrefix(h, "Bearer ")
	if tok == h || tok == "" {
		apiError(w, http.StatusUnauthorized, "Requires authentication")
		return principal{}, false
	}
	now := f.Clock.Now()
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.Count(tok, ".") == 2 {
		if f.checkJWT(tok) {
			return principal{kind: "jwt"}, true
		}
		apiError(w, http.StatusUnauthorized, "A JSON web token could not be decoded")
		return principal{}, false
	}
	if it, ok := f.inst[tok]; ok && now.Before(it.expires) {
		if in := f.installs[it.install]; in != nil && !in.Deleted && !in.Suspended {
			return principal{kind: "installation", inst: it}, true
		}
	}
	if ut, ok := f.users[tok]; ok && !ut.revoked && (ut.expires.IsZero() || now.Before(ut.expires)) {
		return principal{kind: "user", login: ut.login}, true
	}
	apiError(w, http.StatusUnauthorized, "Bad credentials")
	return principal{}, false
}

func (f *Fake) limited(w http.ResponseWriter) bool {
	f.mu.Lock()
	until := f.limitTo
	f.mu.Unlock()
	if until.IsZero() || !f.Clock.Now().Before(until) {
		return false
	}
	w.Header().Set("X-RateLimit-Remaining", "0")
	w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(until.Unix(), 10))
	apiError(w, http.StatusForbidden, "API rate limit exceeded")
	return true
}

func page(r *http.Request, w http.ResponseWriter, total int) (lo, hi int) {
	per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if per < 1 || per > 100 {
		per = 30
	}
	p, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if p < 1 {
		p = 1
	}
	lo, hi = (p-1)*per, p*per
	if lo > total {
		lo = total
	}
	if hi > total {
		hi = total
	}
	if hi < total {
		q := r.URL.Query()
		q.Set("page", strconv.Itoa(p+1))
		w.Header().Set("Link", fmt.Sprintf(`<http://example.invalid%s?%s>; rel="next"`, r.URL.Path, q.Encode()))
	}
	return lo, hi
}

// reaches reports whether an installation reaches a repository.
func (f *Fake) reaches(in *Install, repo *Repo) bool {
	if in == nil || in.Deleted || !strings.EqualFold(in.Account, repo.Owner) {
		return false
	}
	return in.Selection == "all" || in.Repos[repo.ID]
}

func (f *Fake) installJSON(in *Install) map[string]any {
	a := f.accounts[strings.ToLower(in.Account)]
	typ := "User"
	var id int64
	if a != nil {
		id = a.ID
		if a.Org {
			typ = "Organization"
		}
	}
	perms := f.AppPerms
	if in.Perms != nil {
		perms = in.Perms
	}
	m := map[string]any{"id": in.ID, "account": map[string]any{"id": id, "login": in.Account, "type": typ},
		"repository_selection": in.Selection, "permissions": perms, "app_id": f.AppID,
		"html_url": fmt.Sprintf("https://github.com/settings/installations/%d", in.ID), "suspended_at": nil}
	if in.Suspended {
		m["suspended_at"] = f.Clock.Now()
	}
	return m
}

func (f *Fake) repoJSON(repo *Repo, push bool) map[string]any {
	a := f.accounts[strings.ToLower(repo.Owner)]
	typ := "User"
	var id int64
	if a != nil {
		id = a.ID
		if a.Org {
			typ = "Organization"
		}
	}
	return map[string]any{"id": repo.ID, "name": repo.Name, "full_name": repo.Owner + "/" + repo.Name, "private": repo.Private,
		"default_branch": "main", "html_url": f.URL + "/" + repo.Owner + "/" + repo.Name,
		"owner": map[string]any{"id": id, "login": repo.Owner, "type": typ}, "permissions": repoPerms{Push: push, Admin: push, Pull: true}}
}

// canPush reports whether the user has write access to the repository.
func (f *Fake) canPush(login string, repo *Repo) bool {
	if strings.EqualFold(login, repo.Owner) {
		return true
	}
	a := f.accounts[strings.ToLower(repo.Owner)]
	return a != nil && a.Org && a.Members[strings.ToLower(login)]
}

func (f *Fake) serveAPI(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	// The check of the client id and secret uses Basic authentication.
	if strings.HasPrefix(path, "/applications/") {
		f.serveClientAPI(w, r, path)
		return
	}
	// A manifest's code is the only credential of its conversion.
	if r.Method == "POST" && strings.HasPrefix(path, "/app-manifests/") && strings.HasSuffix(path, "/conversions") {
		f.convertManifest(w, strings.Split(path, "/")[2])
		return
	}
	if f.limited(w) {
		return
	}
	p, ok := f.who(w, r)
	if !ok {
		return
	}
	seg := strings.Split(strings.TrimPrefix(path, "/"), "/")
	switch {
	case r.Method == "GET" && path == "/app" && p.kind == "jwt":
		if f.AppDown {
			apiError(w, 503, "Service Unavailable")
			return
		}
		body := map[string]any{"id": f.AppID, "slug": f.Slug, "name": f.Name, "client_id": f.ClientID,
			"html_url": "https://github.com/apps/" + f.Slug, "owner": map[string]any{"login": "kazuhahub", "id": 1, "type": "Organization"},
			"permissions": f.AppPerms}
		if f.Public != nil {
			body["public"] = *f.Public
		}
		writeJSON(w, 200, body)
	case r.Method == "GET" && path == "/app/installations" && p.kind == "jwt":
		f.mu.Lock()
		var all []*Install
		for _, in := range f.installs {
			if !in.Deleted {
				all = append(all, in)
			}
		}
		sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
		lo, hi := page(r, w, len(all))
		out := []map[string]any{}
		for _, in := range all[lo:hi] {
			out = append(out, f.installJSON(in))
		}
		f.mu.Unlock()
		writeJSON(w, 200, out)
	case len(seg) == 3 && seg[0] == "app" && seg[1] == "installations" && r.Method == "GET" && p.kind == "jwt":
		id, _ := strconv.ParseInt(seg[2], 10, 64)
		f.mu.Lock()
		in := f.installs[id]
		f.mu.Unlock()
		if in == nil || in.Deleted {
			apiError(w, 404, "Not Found")
			return
		}
		f.mu.Lock()
		j := f.installJSON(in)
		f.mu.Unlock()
		writeJSON(w, 200, j)
	case len(seg) == 4 && seg[0] == "app" && seg[1] == "installations" && seg[3] == "access_tokens" && r.Method == "POST" && p.kind == "jwt":
		f.mint(w, r, seg[2])
	case r.Method == "GET" && p.kind == "jwt" && ((len(seg) == 4 && seg[0] == "repos" && seg[3] == "installation") ||
		(len(seg) == 3 && (seg[0] == "orgs" || seg[0] == "users") && seg[2] == "installation")):
		f.findInstallation(w, seg)
	case r.Method == "POST" && len(seg) == 3 && seg[0] == "orgs" && seg[2] == "repos":
		f.createRepo(w, r, p, seg[1])
	case r.Method == "POST" && path == "/user/repos" && p.kind == "user":
		f.createRepo(w, r, p, "")
	case r.Method == "GET" && len(seg) == 3 && seg[0] == "repos":
		f.getRepo(w, p, seg[1], seg[2])
	case r.Method == "GET" && path == "/installation/repositories" && p.kind == "installation":
		f.mu.Lock()
		in := f.installs[p.inst.install]
		var all []*Repo
		for _, rp := range f.repos {
			if f.reaches(in, rp) {
				all = append(all, rp)
			}
		}
		sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
		lo, hi := page(r, w, len(all))
		out := []map[string]any{}
		for _, rp := range all[lo:hi] {
			out = append(out, f.repoJSON(rp, p.inst.perms["contents"] == "write"))
		}
		f.mu.Unlock()
		writeJSON(w, 200, map[string]any{"total_count": len(all), "repositories": out})
	case r.Method == "GET" && path == "/user" && p.kind == "user":
		f.mu.Lock()
		a := f.accounts[strings.ToLower(p.login)]
		f.mu.Unlock()
		writeJSON(w, 200, map[string]any{"id": a.ID, "login": a.Login, "name": a.Name})
	case r.Method == "GET" && strings.HasPrefix(path, "/user/memberships/orgs/") && p.kind == "user":
		org := strings.ToLower(strings.TrimPrefix(path, "/user/memberships/orgs/"))
		f.mu.Lock()
		// GitHub answers this to an App's user token only when the App has the
		// organisation's Members permission.
		if f.AppPerms["members"] == "" {
			f.mu.Unlock()
			apiError(w, 403, "Resource not accessible by integration")
			return
		}
		a := f.accounts[org]
		me := strings.ToLower(p.login)
		member := a != nil && a.Org && a.Members[me]
		admin := member && a.Admins[me]
		f.mu.Unlock()
		if !member {
			apiError(w, 404, "Not Found")
			return
		}
		role := "member"
		if admin {
			role = "admin"
		}
		writeJSON(w, 200, map[string]any{"state": "active", "role": role, "organization": map[string]any{"login": a.Login, "id": a.ID}})
	case r.Method == "GET" && path == "/user/emails" && p.kind == "user":
		f.mu.Lock()
		a := f.accounts[strings.ToLower(p.login)]
		f.mu.Unlock()
		out := []map[string]any{}
		if a.Email != "" {
			out = append(out, map[string]any{"email": a.Email, "primary": true, "verified": a.EmailVerified})
		}
		writeJSON(w, 200, out)
	case r.Method == "GET" && path == "/user/orgs" && p.kind == "user":
		f.mu.Lock()
		out := []map[string]any{}
		for _, o := range f.accounts {
			if o.Org && o.Members[strings.ToLower(p.login)] {
				out = append(out, map[string]any{"login": o.Login})
			}
		}
		f.mu.Unlock()
		sort.Slice(out, func(i, j int) bool { return out[i]["login"].(string) < out[j]["login"].(string) })
		writeJSON(w, 200, out)
	case r.Method == "GET" && path == "/user/installations" && p.kind == "user":
		f.mu.Lock()
		var all []*Install
		for _, in := range f.installs {
			if !in.Deleted && f.userSees(p.login, in) {
				all = append(all, in)
			}
		}
		sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
		lo, hi := page(r, w, len(all))
		out := []map[string]any{}
		for _, in := range all[lo:hi] {
			out = append(out, f.installJSON(in))
		}
		f.mu.Unlock()
		writeJSON(w, 200, map[string]any{"total_count": len(all), "installations": out})
	case r.Method == "GET" && len(seg) == 4 && seg[0] == "user" && seg[1] == "installations" && seg[3] == "repositories" && p.kind == "user":
		id, _ := strconv.ParseInt(seg[2], 10, 64)
		f.mu.Lock()
		in := f.installs[id]
		if in == nil || in.Deleted || !f.userSees(p.login, in) {
			f.mu.Unlock()
			apiError(w, 404, "Not Found")
			return
		}
		var all []*Repo
		for _, rp := range f.repos {
			if f.reaches(in, rp) && (!rp.Private || f.canPush(p.login, rp)) {
				all = append(all, rp)
			}
		}
		sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
		lo, hi := page(r, w, len(all))
		out := []map[string]any{}
		for _, rp := range all[lo:hi] {
			out = append(out, f.repoJSON(rp, f.canPush(p.login, rp)))
		}
		f.mu.Unlock()
		writeJSON(w, 200, map[string]any{"total_count": len(all), "repositories": out})
	case r.Method == "PUT" && len(seg) == 5 && seg[0] == "user" && seg[1] == "installations" && seg[3] == "repositories" && p.kind == "user":
		iid, _ := strconv.ParseInt(seg[2], 10, 64)
		rid, _ := strconv.ParseInt(seg[4], 10, 64)
		f.mu.Lock()
		defer f.mu.Unlock()
		in := f.installs[iid]
		var repo *Repo
		for _, rp := range f.repos {
			if rp.ID == rid {
				repo = rp
			}
		}
		if in == nil || in.Deleted || repo == nil || !f.userSees(p.login, in) {
			apiError(w, 404, "Not Found")
			return
		}
		if in.NoAdd || !strings.EqualFold(in.Account, repo.Owner) || !f.canPush(p.login, repo) {
			apiError(w, 403, "Resource not accessible by user")
			return
		}
		in.Repos[repo.ID] = true
		w.WriteHeader(204)
	case r.Method == "GET" && path == "/user/repos" && p.kind == "user":
		f.mu.Lock()
		var all []*Repo
		for _, rp := range f.repos {
			if strings.EqualFold(rp.Owner, p.login) {
				all = append(all, rp)
			}
		}
		sort.Slice(all, func(i, j int) bool { return strings.ToLower(all[i].Name) < strings.ToLower(all[j].Name) })
		lo, hi := page(r, w, len(all))
		out := []map[string]any{}
		for _, rp := range all[lo:hi] {
			out = append(out, f.repoJSON(rp, true))
		}
		f.mu.Unlock()
		writeJSON(w, 200, out)
	default:
		apiError(w, 404, "Not Found")
	}
}

// userSees reports whether the user can see an installation: it is on their
// own account or on an organisation they belong to.
func (f *Fake) userSees(login string, in *Install) bool {
	if strings.EqualFold(in.Account, login) {
		return true
	}
	a := f.accounts[strings.ToLower(in.Account)]
	return a != nil && a.Org && a.Members[strings.ToLower(login)]
}

func (f *Fake) serveClientAPI(w http.ResponseWriter, r *http.Request, path string) {
	u, pw, ok := r.BasicAuth()
	seg := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if !ok || len(seg) < 3 || seg[1] != f.ClientID || u != f.ClientID || subtle.ConstantTimeCompare([]byte(pw), []byte(f.ClientSecret)) != 1 {
		apiError(w, http.StatusUnauthorized, "Bad credentials")
		return
	}
	switch {
	case r.Method == "POST" && seg[2] == "token":
		var b struct {
			AccessToken string `json:"access_token"`
		}
		readBody(r, &b)
		f.mu.Lock()
		ut, known := f.users[b.AccessToken]
		f.mu.Unlock()
		if !known || ut.revoked {
			apiError(w, 404, "Not Found")
			return
		}
		writeJSON(w, 200, map[string]any{"token": b.AccessToken})
	case r.Method == "DELETE" && seg[2] == "grant":
		var b struct {
			AccessToken string `json:"access_token"`
		}
		readBody(r, &b)
		f.mu.Lock()
		ut, known := f.users[b.AccessToken]
		if known {
			login := ut.login
			for _, x := range f.users {
				if strings.EqualFold(x.login, login) {
					x.revoked = true
				}
			}
			for _, x := range f.refresh {
				if strings.EqualFold(x.login, login) {
					x.revoked = true
				}
			}
		}
		f.mu.Unlock()
		if !known {
			apiError(w, 404, "Not Found")
			return
		}
		w.WriteHeader(204)
	default:
		apiError(w, 404, "Not Found")
	}
}

func (f *Fake) mint(w http.ResponseWriter, r *http.Request, idStr string) {
	id, _ := strconv.ParseInt(idStr, 10, 64)
	var b struct {
		RepoIDs []int64           `json:"repository_ids"`
		Perms   map[string]string `json:"permissions"`
	}
	readBody(r, &b)
	f.mu.Lock()
	defer f.mu.Unlock()
	in := f.installs[id]
	if in == nil || in.Deleted {
		apiError(w, 404, "Not Found")
		return
	}
	if in.Suspended {
		apiError(w, 403, "This installation has been suspended")
		return
	}
	granted := f.AppPerms
	if in.Perms != nil {
		granted = in.Perms
	}
	for k, want := range b.Perms {
		have := granted[k]
		if have == "" || (want == "write" && have != "write") {
			if f.PermissionRefusal403 {
				apiError(w, 403, "Resource not accessible by integration")
			} else {
				apiError(w, 422, "The permissions requested are not granted to this installation.")
			}
			return
		}
	}
	for _, rid := range b.RepoIDs {
		var repo *Repo
		for _, rp := range f.repos {
			if rp.ID == rid {
				repo = rp
			}
		}
		if repo == nil || !f.reaches(in, repo) {
			apiError(w, 422, "There is at least one repository that does not exist or is not accessible to the parent installation.")
			return
		}
	}
	tok := "ghs_" + randHex(18)
	it := &instToken{token: tok, install: id, repos: b.RepoIDs, perms: b.Perms, expires: f.Clock.Now().Add(time.Hour)}
	f.inst[tok] = it
	writeJSON(w, 201, map[string]any{"token": tok, "expires_at": it.expires, "permissions": b.Perms})
}

func (f *Fake) findInstallation(w http.ResponseWriter, seg []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []int64
	for id := range f.installs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		in := f.installs[id]
		if in.Deleted {
			continue
		}
		switch seg[0] {
		case "repos":
			if rp := f.repos[strings.ToLower(seg[1]+"/"+seg[2])]; rp != nil && f.reaches(in, rp) {
				writeJSON(w, 200, f.installJSON(in))
				return
			}
		default:
			if strings.EqualFold(in.Account, seg[1]) {
				writeJSON(w, 200, f.installJSON(in))
				return
			}
		}
	}
	apiError(w, 404, "Not Found")
}

func (f *Fake) getRepo(w http.ResponseWriter, p principal, owner, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rp := f.repos[strings.ToLower(owner+"/"+name)]
	if rp == nil {
		apiError(w, 404, "Not Found")
		return
	}
	switch p.kind {
	case "user":
		if rp.Private && !f.canPush(p.login, rp) {
			apiError(w, 404, "Not Found")
			return
		}
		writeJSON(w, 200, f.repoJSON(rp, f.canPush(p.login, rp)))
	case "installation":
		if !f.reaches(f.installs[p.inst.install], rp) {
			apiError(w, 404, "Not Found")
			return
		}
		writeJSON(w, 200, f.repoJSON(rp, p.inst.perms["contents"] == "write"))
	default:
		apiError(w, 403, "Resource not accessible")
	}
}

func (f *Fake) installOfLocked(account string) *Install {
	for _, in := range f.installs {
		if !in.Deleted && strings.EqualFold(in.Account, account) {
			return in
		}
	}
	return nil
}

func (f *Fake) installedLocked(account string) bool { return f.installOfLocked(account) != nil }

func (f *Fake) createRepo(w http.ResponseWriter, r *http.Request, p principal, org string) {
	var b struct {
		Name        string `json:"name"`
		Private     bool   `json:"private"`
		Description string `json:"description"`
		AutoInit    bool   `json:"auto_init"`
	}
	readBody(r, &b)
	f.mu.Lock()
	defer f.mu.Unlock()
	owner := org
	switch {
	case org == "" && p.kind == "user":
		owner = f.accounts[strings.ToLower(p.login)].Login
		if f.PersonalNeedsInstall && !f.installedLocked(owner) {
			apiError(w, 403, "Resource not accessible by integration")
			return
		}
		if in := f.installOfLocked(owner); f.PersonalNeedsInstall && in != nil && in.Perms != nil && in.Perms["administration"] != "write" {
			apiError(w, 403, "Resource not accessible by integration")
			return
		}
	case org == "":
		apiError(w, 403, "Resource not accessible by integration")
		return
	}
	acc := f.accounts[strings.ToLower(owner)]
	if acc == nil {
		apiError(w, 404, "Not Found")
		return
	}
	switch p.kind {
	case "installation":
		in := f.installs[p.inst.install]
		if !strings.EqualFold(in.Account, owner) {
			apiError(w, 404, "Not Found")
			return
		}
		if p.inst.perms["administration"] != "write" {
			apiError(w, 403, "Resource not accessible by integration")
			return
		}
	case "user":
		if org != "" && (!acc.Org || !acc.Members[strings.ToLower(p.login)]) {
			apiError(w, 404, "Not Found")
			return
		}
		if org != "" && f.OrgNeedsInstall && !f.installedLocked(org) {
			apiError(w, 403, "Resource not accessible by integration")
			return
		}
		if org != "" && acc.NoCreate {
			apiError(w, 403, "You don't have the permission to create repositories in this organization.")
			return
		}
	default:
		apiError(w, 403, "Resource not accessible")
		return
	}
	if b.AutoInit {
		apiError(w, 422, "auto_init is not supported by this fake")
		return
	}
	if b.Private && acc.NoPrivate {
		apiError(w, 403, "Private repositories are not allowed in this organization.")
		return
	}
	if b.Name == "" || strings.ContainsAny(b.Name, " /") {
		apiError(w, 422, "Repository creation failed.", "name is invalid")
		return
	}
	if f.repos[strings.ToLower(owner+"/"+b.Name)] != nil {
		apiError(w, 422, "Repository creation failed.", "name already exists on this account")
		return
	}
	repo := f.newRepoLocked(owner, b.Name, b.Private, b.Description, f.HiddenGit)
	if f.AutoAdd {
		for _, in := range f.installs {
			if !in.Deleted && in.Selection == "selected" && strings.EqualFold(in.Account, owner) {
				in.Repos[repo.ID] = true
			}
		}
	}
	writeJSON(w, 201, f.repoJSON(repo, true))
}

// --- OAuth ---------------------------------------------------------------------------

func (f *Fake) serveWeb(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/login/oauth/access_token" || r.Method != "POST" {
		http.NotFound(w, r)
		return
	}
	if f.limited(w) {
		return
	}
	r.ParseForm()
	reply := func(v map[string]any) { writeJSON(w, 200, v) }
	fail := func(code, desc string) { reply(map[string]any{"error": code, "error_description": desc}) }
	if r.Form.Get("client_id") != f.ClientID || subtle.ConstantTimeCompare([]byte(r.Form.Get("client_secret")), []byte(f.ClientSecret)) != 1 {
		fail("incorrect_client_credentials", "The client_id and/or client_secret passed are incorrect.")
		return
	}
	now := f.Clock.Now()
	f.mu.Lock()
	defer f.mu.Unlock()
	var login string
	if r.Form.Get("grant_type") == "refresh_token" {
		rt := f.refresh[r.Form.Get("refresh_token")]
		if rt == nil || rt.used || rt.revoked || !now.Before(rt.expires) {
			fail("bad_refresh_token", "The refresh token passed is incorrect or expired.")
			return
		}
		rt.used = true
		login = rt.login
	} else {
		c := f.codes[r.Form.Get("code")]
		if c == nil {
			fail("bad_verification_code", "The code passed is incorrect or expired.")
			return
		}
		delete(f.codes, r.Form.Get("code"))
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if c.challenge != "" && c.challenge != f.b64(sum[:]) {
			fail("bad_verification_code", "The code_verifier does not match the code_challenge.")
			return
		}
		if c.redirect != "" && r.Form.Get("redirect_uri") != c.redirect {
			fail("redirect_uri_mismatch", "The redirect_uri does not match.")
			return
		}
		login = c.login
	}
	access := "ghu_" + randHex(18)
	out := map[string]any{"access_token": access, "token_type": "bearer", "scope": ""}
	ut := &userToken{seq: f.id(), access: access, login: login}
	if !f.NoRefresh {
		ut.expires = now.Add(8 * time.Hour)
		out["expires_in"] = 8 * 3600
		ref := "ghr_" + randHex(24)
		f.refresh[ref] = &refreshToken{seq: f.id(), token: ref, login: login, expires: now.Add(180 * 24 * time.Hour)}
		out["refresh_token"] = ref
		out["refresh_token_expires_in"] = 180 * 24 * 3600
	}
	f.users[access] = ut
	if f.BreakRefreshReply && r.Form.Get("grant_type") == "refresh_token" {
		f.BreakRefreshReply = false
		w.WriteHeader(502)
		return
	}
	reply(out)
}

// --- Git -----------------------------------------------------------------------------

func (f *Fake) serveGit(w http.ResponseWriter, r *http.Request) {
	if f.git == nil {
		http.Error(w, "git is not installed", 500)
		return
	}
	idx := strings.Index(r.URL.Path, ".git/")
	repoPath := strings.ToLower(strings.TrimPrefix(r.URL.Path[:idx], "/"))
	u, pw, ok := r.BasicAuth()
	if !ok {
		w.Header().Set("WWW-Authenticate", `Basic realm="GitHub"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	_ = u
	push := strings.Contains(r.URL.RawQuery, "git-receive-pack") || strings.HasSuffix(r.URL.Path, "/git-receive-pack")
	now := f.Clock.Now()
	f.mu.Lock()
	f.gitUsed = append(f.gitUsed, pw)
	repo := f.repos[repoPath]
	status := http.StatusOK
	switch {
	case repo == nil:
		status = http.StatusNotFound
	default:
		allowed := false
		if f.RejectGit {
			// nothing is allowed
		} else if it, ok := f.inst[pw]; ok && now.Before(it.expires) {
			in := f.installs[it.install]
			narrowed := len(it.repos) == 0
			for _, id := range it.repos {
				narrowed = narrowed || id == repo.ID
			}
			level := it.perms["contents"]
			allowed = f.reaches(in, repo) && !in.Suspended && narrowed && (level == "write" || (level == "read" && !push))
		} else if ut, ok := f.users[pw]; ok && !ut.revoked && (ut.expires.IsZero() || now.Before(ut.expires)) {
			allowed = f.canPush(ut.login, repo)
		}
		switch {
		case !allowed:
			status = http.StatusUnauthorized
		case repo.hide > 0:
			repo.hide--
			f.hidden++
			status = http.StatusNotFound
		}
	}
	f.mu.Unlock()
	switch status {
	case http.StatusNotFound:
		http.Error(w, "Repository not found.", http.StatusNotFound)
		return
	case http.StatusUnauthorized:
		w.Header().Set("WWW-Authenticate", `Basic realm="GitHub"`)
		http.Error(w, "Bad credentials", http.StatusUnauthorized)
		return
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/" + repoPath + r.URL.Path[idx:]
	f.git.ServeHTTP(w, r2)
}
