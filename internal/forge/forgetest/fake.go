// Package forgetest is a fake GitLab and a fake Gitea for tests: the OAuth
// authorisation-code flow with PKCE and refresh-token rotation, the REST calls
// PetalBook makes (the version, the user, the groups or organisations a person
// may create in, creating and listing and reading repositories), and Git over
// smart HTTP (git http-backend on bare repositories) that accepts only an
// access token that is good for the repository. Nothing here ever contacts a
// real host.
//
// The two follow their documented APIs (GitLab REST v4, Gitea API v1) as far as
// PetalBook uses them; the failures the real ones have are switches on the
// Fake: a rate limit, a host that is down, a revoked or expired refresh token,
// a name already taken, a group that does not let a person create, and a host
// that cannot filter a fetch.
package forgetest

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

// SystemClock is the real time.
func SystemClock() Clocker { return systemClock{} }

// Access levels of a member of a group (GitLab's numbers; Gitea's teams follow them).
const (
	Guest      = 10
	Reporter   = 20
	Developer  = 30
	Maintainer = 40
	Owner      = 50
)

// User is an account.
type User struct {
	ID    int64
	Login string
}

// Group is a GitLab group (full path, subgroups with slashes) or a Gitea organisation.
type Group struct {
	ID      int64
	Path    string
	Members map[string]int // login -> access level
	// CreateLevel is the lowest level that may create a repository in it
	// (default Developer for GitLab, Maintainer for Gitea).
	CreateLevel int
}

// Repo is a repository.
type Repo struct {
	ID      int64
	Owner   string // a login, or a group's full path
	Name    string
	Private bool
	Desc    string
	// Collab are people with rights on it beyond their ownership of its namespace.
	Collab map[string]int
}

// Request is one request the fake served.
type Request struct {
	Method, Path, Query string
	// Auth is "bearer", "basic", "git" or "".
	Auth string
}

type accessToken struct {
	token   string
	login   string
	expires time.Time // zero: none
	revoked bool
}

type refreshToken struct {
	token   string
	login   string
	expires time.Time
	used    bool
	revoked bool
}

type code struct {
	login, challenge, redirect string
}

// Fake is the fake host. Kind says which platform it is.
type Fake struct {
	*httptest.Server
	t     testing.TB
	Clock Clocker
	Kind  string // "gitlab" or "gitea"

	// Version is what the host says it is.
	Version      string
	ClientID     string
	ClientSecret string
	// AccessTTL is how long an access token lives (0: forever).
	AccessTTL time.Duration
	// RefreshTTL is how long a refresh token lives (0: forever; Gitea's default is 730 hours).
	RefreshTTL time.Duration
	// NoRefresh: tokens never expire and no refresh token is given.
	NoRefresh bool
	// RejectGit makes Git refuse every credential (a revoked token).
	RejectGit bool
	// Down makes the API answer 503.
	Down bool
	// NoPartial: the host does not allow filtered fetches (a Git server whose
	// uploadpack.allowFilter is off).
	NoPartial bool
	// NotThisPlatform makes the version endpoint answer like some other server.
	NotThisPlatform bool
	// Redirect makes the API answer a redirect to Redirect (a token must not follow it).
	Redirect string

	mu      sync.Mutex
	nextID  int64
	users   map[string]*User  // lower login
	groups  map[string]*Group // lower full path
	repos   map[string]*Repo  // lower "owner/name"
	access  map[string]*accessToken
	refresh map[string]*refreshToken
	codes   map[string]*code
	limitTo time.Time
	reqs    []Request
	gitRoot string
	gitUsed []string // tokens Git was given
	gitUser []string // user names Git was given
	git     *cgi.Handler
	// bad logins: a person whose account was deleted on the host.
	gone map[string]bool
	// admins are the accounts that are administrators of the host.
	admins map[string]bool
	hook   func(method, path string)
}

// NewGitLab starts a fake GitLab.
func NewGitLab(t testing.TB, clock Clocker) *Fake { return newFake(t, clock, "gitlab") }

// NewGitea starts a fake Gitea (a Forgejo or Codeberg looks the same).
func NewGitea(t testing.TB, clock Clocker) *Fake { return newFake(t, clock, "gitea") }

func newFake(t testing.TB, clock Clocker, kind string) *Fake {
	t.Helper()
	if clock == nil {
		clock = SystemClock()
	}
	f := &Fake{t: t, Clock: clock, Kind: kind, ClientID: "fake-client-id-" + kind, ClientSecret: "fake-client-secret-" + kind + "-0123456789", nextID: 100,
		users: map[string]*User{}, groups: map[string]*Group{}, repos: map[string]*Repo{}, access: map[string]*accessToken{}, refresh: map[string]*refreshToken{},
		codes: map[string]*code{}, gone: map[string]bool{}, admins: map[string]bool{}, gitRoot: t.TempDir()}
	switch kind {
	case "gitlab":
		f.Version, f.AccessTTL = "16.7.2", 2*time.Hour
	default:
		f.Version, f.AccessTTL, f.RefreshTTL = "1.22.0", time.Hour, 730*time.Hour
	}
	if gitBin, err := gitstore.Path(); err == nil {
		f.git = &cgi.Handler{Path: gitBin, Args: []string{"http-backend"}, Root: "/", InheritEnv: []string{"PATH"},
			Env: []string{"GIT_PROJECT_ROOT=" + f.gitRoot, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1"}}
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Server.Close)
	return f
}

// Host is the host and port of the fake, as a provider's host is given.
func (f *Fake) Host() string { return strings.TrimPrefix(f.URL, "http://") }

// HostURL is the host with its scheme (a provider's host on a plain-http fake).
func (f *Fake) HostURL() string { return f.URL }

func (f *Fake) id() int64 { f.nextID++; return f.nextID }

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// --- what a test sets up -------------------------------------------------------------

// AddUser makes an account.
func (f *Fake) AddUser(login string) *User {
	f.mu.Lock()
	defer f.mu.Unlock()
	u := &User{ID: f.id(), Login: login}
	f.users[strings.ToLower(login)] = u
	return u
}

// AddGroup makes a group (GitLab) or organisation (Gitea) with members at levels.
func (f *Fake) AddGroup(path string, members map[string]int) *Group {
	f.mu.Lock()
	defer f.mu.Unlock()
	level := Developer
	if f.Kind == "gitea" {
		level = Maintainer
	}
	g := &Group{ID: f.id(), Path: path, Members: map[string]int{}, CreateLevel: level}
	for k, v := range members {
		g.Members[k] = v
	}
	f.groups[strings.ToLower(path)] = g
	return g
}

// SetGroup changes a group.
func (f *Fake) SetGroup(path string, fn func(*Group)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f.groups[strings.ToLower(path)])
}

// AddRepo makes a repository exist, with its bare repository on disk.
func (f *Fake) AddRepo(owner, name string, private bool) *Repo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.newRepoLocked(owner, name, private, "")
}

// Collaborate gives a person rights on a repository.
func (f *Fake) Collaborate(full, login string, level int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repos[strings.ToLower(full)]
	if r.Collab == nil {
		r.Collab = map[string]int{}
	}
	r.Collab[login] = level
}

// RepoByName returns a repository by its full path, or nil.
func (f *Fake) RepoByName(full string) *Repo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.repos[strings.ToLower(full)]
}

// BareDir is the bare repository of a repository on disk.
func (f *Fake) BareDir(full string) string {
	return filepath.Join(f.gitRoot, strings.ToLower(full)+".git")
}

func (f *Fake) newRepoLocked(owner, name string, private bool, desc string) *Repo {
	r := &Repo{ID: f.id(), Owner: owner, Name: name, Private: private, Desc: desc}
	key := strings.ToLower(owner + "/" + name)
	f.repos[key] = r
	dir := filepath.Join(f.gitRoot, key+".git")
	// A bare repository is a few files; making it by hand keeps git itself out
	// of this package (only gitstore's runner starts git).
	for _, d := range []string{"objects", "refs/heads", "refs/tags"} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(d)), 0o755); err != nil {
			f.t.Fatal(err)
		}
	}
	allow := "true"
	if f.NoPartial {
		allow = "false"
	}
	files := map[string]string{
		"HEAD": "ref: refs/heads/main\n",
		"config": "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = true\n" +
			"[uploadpack]\n\tallowFilter = " + allow + "\n\tallowAnySHA1InWant = true\n[http]\n\treceivepack = true\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
	return r
}

// LimitUntil makes the API answer 429 until the time.
func (f *Fake) LimitUntil(t time.Time) { f.mu.Lock(); f.limitTo = t; f.mu.Unlock() }

// RemoveUser deletes an account on the host: its tokens stop working.
func (f *Fake) RemoveUser(login string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gone[strings.ToLower(login)] = true
}

// RevokeUser revokes every token of a person (they removed the application in their settings).
func (f *Fake) RevokeUser(login string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.access {
		if strings.EqualFold(a.login, login) {
			a.revoked = true
		}
	}
	for _, r := range f.refresh {
		if strings.EqualFold(r.login, login) {
			r.revoked = true
		}
	}
}

// RevokeAccess revokes the access tokens of a person and leaves their refresh
// tokens good (one access token was removed at the host).
func (f *Fake) RevokeAccess(login string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.access {
		if strings.EqualFold(a.login, login) {
			a.revoked = true
		}
	}
}

// SetAdmin makes an account an administrator of the host (or not): Gitea says
// so in the answer about the signed-in person.
func (f *Fake) SetAdmin(login string, admin bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.admins[strings.ToLower(login)] = admin
}

// OnRequest sets a function called, outside the fake's lock, at the start of
// every API and OAuth request, before it is answered (nil: none). A test uses
// it to make something happen while a request is on its way.
func (f *Fake) OnRequest(fn func(method, path string)) {
	f.mu.Lock()
	f.hook = fn
	f.mu.Unlock()
}

// SetDown makes the API answer 503 (or answer again) while requests are being
// served: a test that has a background loop asking the host sets it so.
func (f *Fake) SetDown(down bool) {
	f.mu.Lock()
	f.Down = down
	f.mu.Unlock()
}

// ExpireRefresh makes a person's refresh tokens end now.
func (f *Fake) ExpireRefresh(login string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.refresh {
		if strings.EqualFold(r.login, login) {
			r.expires = f.Clock.Now().Add(-time.Second)
		}
	}
}

// RefreshTokenOf is a person's newest refresh token ("" if none).
func (f *Fake) RefreshTokenOf(login string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var best *refreshToken
	for _, r := range f.refresh {
		if strings.EqualFold(r.login, login) && !r.used && (best == nil || r.token > best.token) {
			best = r
		}
	}
	if best == nil {
		return ""
	}
	return best.token
}

// AccessTokenOf is a person's newest access token ("" if none).
func (f *Fake) AccessTokenOf(login string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var best *accessToken
	for _, a := range f.access {
		if strings.EqualFold(a.login, login) && (best == nil || a.expires.After(best.expires) || a.token > best.token) {
			best = a
		}
	}
	if best == nil {
		return ""
	}
	return best.token
}

// AllTokens is every token the fake ever issued, for a test that looks for them in logs and answers.
func (f *Fake) AllTokens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for k := range f.access {
		out = append(out, k)
	}
	for k := range f.refresh {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// GitTokens is the tokens Git was given, in order.
func (f *Fake) GitTokens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.gitUsed...)
}

// GitUsers is the user names Git was given, in order.
func (f *Fake) GitUsers() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.gitUser...)
}

// Requests is every request served, oldest first.
func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.reqs...)
}

// Count is how many requests with the method had a path starting with the prefix.
func (f *Fake) Count(method, prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.reqs {
		if r.Method == method && strings.HasPrefix(r.Path, prefix) {
			n++
		}
	}
	return n
}

// --- the people's side ---------------------------------------------------------------

func (f *Fake) authorizePath() string {
	if f.Kind == "gitlab" {
		return "/oauth/authorize"
	}
	return "/login/oauth/authorize"
}

// Authorize plays the browser: it reads the authorisation URL PetalBook made,
// has the person approve it and returns the URL the host redirects the browser to.
func (f *Fake) Authorize(authURL, login string) string {
	f.t.Helper()
	u, err := url.Parse(authURL)
	if err != nil || u.Path != f.authorizePath() || u.Host != f.Host() {
		f.t.Fatalf("not an authorisation URL of this host: %q", authURL)
	}
	q := u.Query()
	if q.Get("client_id") != f.ClientID || q.Get("response_type") != "code" {
		f.t.Fatalf("the authorisation URL names client %q, response_type %q", q.Get("client_id"), q.Get("response_type"))
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" || q.Get("redirect_uri") == "" {
		f.t.Fatalf("the authorisation URL has no PKCE, state or redirect: %q", authURL)
	}
	if f.Kind == "gitlab" && q.Get("scope") != "api" {
		f.t.Fatalf("the authorisation asks for scope %q", q.Get("scope"))
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

// Deny plays a person who refuses.
func (f *Fake) Deny(authURL string) string {
	f.t.Helper()
	u, _ := url.Parse(authURL)
	back, _ := url.Parse(u.Query().Get("redirect_uri"))
	bq := back.Query()
	bq.Set("error", "access_denied")
	bq.Set("state", u.Query().Get("state"))
	back.RawQuery = bq.Encode()
	return back.String()
}

// --- the server ----------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	path := r.URL.EscapedPath()
	auth := ""
	switch {
	case strings.HasPrefix(r.Header.Get("Authorization"), "Bearer "):
		auth = "bearer"
	case strings.HasPrefix(r.Header.Get("Authorization"), "Basic "):
		auth = "basic"
	}
	isGit := strings.Contains(path, ".git/")
	if isGit {
		auth = "git"
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, Request{r.Method, path, r.URL.RawQuery, auth})
	down, redirect := f.Down, f.Redirect
	limited := f.Clock.Now().Before(f.limitTo)
	until := f.limitTo
	hook := f.hook
	f.mu.Unlock()
	if hook != nil && !isGit {
		hook(r.Method, path)
	}
	if isGit {
		f.serveGit(w, r)
		return
	}
	if redirect != "" && strings.HasPrefix(path, "/api/") {
		http.Redirect(w, r, redirect, http.StatusFound)
		return
	}
	if down && (strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/oauth/") || strings.HasPrefix(path, "/login/oauth/")) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"message": "down"})
		return
	}
	if limited && strings.HasPrefix(path, "/api/") {
		w.Header().Set("Retry-After", strconv.Itoa(int(until.Sub(f.Clock.Now()).Seconds())+1))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"message": "Retry later"})
		return
	}
	switch {
	case path == "/oauth/token" && f.Kind == "gitlab", path == "/login/oauth/access_token" && f.Kind == "gitea":
		f.token(w, r)
	case path == "/oauth/revoke" && f.Kind == "gitlab":
		f.revoke(w, r)
	case strings.HasPrefix(path, "/api/v4/") && f.Kind == "gitlab":
		f.serveGitLab(w, r, strings.TrimPrefix(path, "/api/v4"))
	case strings.HasPrefix(path, "/api/v1/") && f.Kind == "gitea":
		f.serveGitea(w, r, strings.TrimPrefix(path, "/api/v1"))
	default:
		http.NotFound(w, r)
	}
}

func oauthError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

func (f *Fake) jwt(login string, exp time.Time) string {
	enc := base64.RawURLEncoding.EncodeToString
	h := enc([]byte(`{"alg":"HS256","typ":"JWT"}`))
	p, _ := json.Marshal(map[string]any{"sub": login, "exp": exp.Unix(), "jti": randHex(6)})
	return h + "." + enc(p) + "." + enc([]byte(randHex(8)))
}

func (f *Fake) newAccess(login string) *accessToken {
	now := f.Clock.Now()
	a := &accessToken{token: randHex(20), login: login}
	if f.Kind == "gitlab" {
		a.token = "gloas-" + a.token
	}
	if f.AccessTTL > 0 && !f.NoRefresh {
		a.expires = now.Add(f.AccessTTL)
	}
	f.access[a.token] = a
	return a
}

func (f *Fake) newRefresh(login string) *refreshToken {
	now := f.Clock.Now()
	r := &refreshToken{login: login}
	if f.RefreshTTL > 0 {
		r.expires = now.Add(f.RefreshTTL)
	}
	if f.Kind == "gitea" {
		exp := r.expires
		if exp.IsZero() {
			exp = now.Add(730 * time.Hour)
		}
		r.token = f.jwt(login, exp)
	} else {
		r.token = randHex(20)
	}
	f.refresh[r.token] = r
	return r
}

// token is the token endpoint: the authorisation code (with PKCE) and the refresh token.
func (f *Fake) token(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", 405)
		return
	}
	if err := r.ParseForm(); err != nil {
		oauthError(w, 400, "invalid_request", "bad form")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	id, sec := r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	if u, p, ok := r.BasicAuth(); ok {
		id, sec = u, p
	}
	if id != f.ClientID || subtle.ConstantTimeCompare([]byte(sec), []byte(f.ClientSecret)) != 1 {
		oauthError(w, 401, "invalid_client", "Client authentication failed due to unknown client, no client authentication included, or unsupported authentication method.")
		return
	}
	now := f.Clock.Now()
	var login string
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		c, ok := f.codes[r.PostForm.Get("code")]
		if !ok {
			oauthError(w, 400, "invalid_grant", "The provided authorization grant is invalid, expired, revoked, does not match the redirection URI used in the authorization request, or was issued to another client.")
			return
		}
		delete(f.codes, r.PostForm.Get("code")) // one use
		if c.redirect != r.PostForm.Get("redirect_uri") {
			oauthError(w, 400, "invalid_grant", "redirect_uri does not match")
			return
		}
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if c.challenge != "" && base64.RawURLEncoding.EncodeToString(sum[:]) != c.challenge {
			oauthError(w, 400, "invalid_grant", "code_verifier does not match the challenge")
			return
		}
		login = c.login
	case "refresh_token":
		rt, ok := f.refresh[r.PostForm.Get("refresh_token")]
		if !ok || rt.used || rt.revoked || (!rt.expires.IsZero() && !now.Before(rt.expires)) {
			oauthError(w, 400, "invalid_grant", "The provided authorization grant is invalid, expired, revoked, does not match the redirection URI used in the authorization request, or was issued to another client.")
			return
		}
		rt.used = true // a refresh token is spent by its use and replaced
		login = rt.login
	default:
		oauthError(w, 400, "unsupported_grant_type", "")
		return
	}
	if f.gone[strings.ToLower(login)] {
		oauthError(w, 400, "invalid_grant", "")
		return
	}
	a := f.newAccess(login)
	out := map[string]any{"access_token": a.token, "token_type": "Bearer"}
	if f.Kind == "gitea" {
		out["token_type"] = "bearer"
	} else {
		out["created_at"] = now.Unix()
	}
	if !a.expires.IsZero() {
		out["expires_in"] = int64(a.expires.Sub(now).Seconds())
	}
	if !f.NoRefresh {
		out["refresh_token"] = f.newRefresh(login).token
	}
	writeJSON(w, 200, out)
}

func (f *Fake) revoke(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.PostForm.Get("client_id") != f.ClientID || r.PostForm.Get("client_secret") != f.ClientSecret {
		oauthError(w, 401, "invalid_client", "")
		return
	}
	if a, ok := f.access[r.PostForm.Get("token")]; ok {
		a.revoked = true
	}
	writeJSON(w, 200, map[string]string{})
}

// who is the person the request's bearer token belongs to.
func (f *Fake) who(w http.ResponseWriter, r *http.Request) (*User, bool) {
	h := r.Header.Get("Authorization")
	tok, ok := strings.CutPrefix(h, "Bearer ")
	f.mu.Lock()
	defer f.mu.Unlock()
	var a *accessToken
	if ok {
		a = f.access[tok]
	}
	now := f.Clock.Now()
	if a == nil || a.revoked || (!a.expires.IsZero() && !now.Before(a.expires)) || f.gone[strings.ToLower(a.login)] {
		if f.Kind == "gitlab" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "401 Unauthorized"})
		} else {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "token is required", "url": f.URL + "/api/swagger"})
		}
		return nil, false
	}
	return f.users[strings.ToLower(a.login)], true
}

// level is a person's level on a repository: 0 none; Guest to Owner.
func (f *Fake) levelLocked(login string, repo *Repo) int {
	if strings.EqualFold(repo.Owner, login) {
		return Owner
	}
	best := 0
	if l, ok := repo.Collab[login]; ok {
		best = l
	}
	if g := f.groups[strings.ToLower(repo.Owner)]; g != nil {
		if l, ok := g.Members[login]; ok && l > best {
			best = l
		}
	}
	if best == 0 && !repo.Private {
		best = Guest
	}
	return best
}

func (f *Fake) serveGit(w http.ResponseWriter, r *http.Request) {
	if f.git == nil {
		http.Error(w, "git is not installed", 500)
		return
	}
	path := r.URL.Path
	idx := strings.Index(path, ".git/")
	repoPath := strings.ToLower(strings.TrimPrefix(path[:idx], "/"))
	user, pw, ok := r.BasicAuth()
	if !ok {
		w.Header().Set("WWW-Authenticate", `Basic realm="`+f.Kind+`"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	push := strings.Contains(r.URL.RawQuery, "git-receive-pack") || strings.HasSuffix(path, "/git-receive-pack")
	now := f.Clock.Now()
	f.mu.Lock()
	f.gitUsed = append(f.gitUsed, pw)
	f.gitUser = append(f.gitUser, user)
	repo := f.repos[repoPath]
	status := http.StatusOK
	switch {
	case repo == nil:
		status = http.StatusNotFound
	default:
		allowed := false
		if a, ok := f.access[pw]; ok && !f.RejectGit && !a.revoked && (a.expires.IsZero() || now.Before(a.expires)) && user == "oauth2" && !f.gone[strings.ToLower(a.login)] {
			l := f.levelLocked(a.login, repo)
			allowed = l >= Developer || (l >= Guest && !push)
		}
		if !allowed {
			status = http.StatusUnauthorized
		}
	}
	f.mu.Unlock()
	switch status {
	case http.StatusNotFound:
		http.Error(w, "Repository not found.", http.StatusNotFound)
		return
	case http.StatusUnauthorized:
		w.Header().Set("WWW-Authenticate", `Basic realm="`+f.Kind+`"`)
		http.Error(w, "Bad credentials", http.StatusUnauthorized)
		return
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/" + repoPath + path[idx:]
	f.git.ServeHTTP(w, r2)
}

func page(r *http.Request, total int) (lo, hi, next int) {
	pg, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if pg < 1 {
		pg = 1
	}
	per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if per == 0 {
		per, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	}
	if per < 1 || per > 100 {
		per = 30
	}
	lo = (pg - 1) * per
	if lo > total {
		lo = total
	}
	hi = lo + per
	if hi > total {
		hi = total
	}
	if hi < total {
		next = pg + 1
	}
	return lo, hi, next
}

func setNext(w http.ResponseWriter, r *http.Request, kind string, next int) {
	if kind == "gitlab" {
		if next > 0 {
			w.Header().Set("X-Next-Page", strconv.Itoa(next))
		} else {
			w.Header().Set("X-Next-Page", "")
		}
		return
	}
	if next > 0 {
		u := *r.URL
		q := u.Query()
		q.Set("page", strconv.Itoa(next))
		u.RawQuery = q.Encode()
		w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"`, u.String()))
	}
}

func readJSON(r *http.Request, v any) {
	_ = json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(v)
}

// Clock is a clock tests move by hand; the fake and the service under test share one.
type Clock struct {
	mu sync.Mutex
	t  time.Time
}

// NewClock starts at a fixed time.
func NewClock() *Clock { return &Clock{t: time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC)} }

// Now is the clock's time.
func (c *Clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

// Advance moves the clock on.
func (c *Clock) Advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }
