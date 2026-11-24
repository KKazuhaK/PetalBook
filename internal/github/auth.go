package github

import (
	"container/list"
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/connections"
	"github.com/KKazuhaK/PetalBook/internal/githubapp"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// Errors of using a user's authorisation.
var (
	// ErrNoAuthorization: the user has not connected a GitHub account.
	ErrNoAuthorization = errors.New("the user has not connected a GitHub account")
	// ErrAuthorizationExpired: the refresh token ran out or was revoked.
	ErrAuthorizationExpired = errors.New("the GitHub authorisation expired or was revoked")
)

// Bounds of the sign-ins in progress.
const (
	attemptTTL      = 10 * time.Minute
	attemptCapacity = 10_000
	// manifestCapacity bounds the attempts to make the App, which have a store of
	// their own: a flood of people connecting accounts must not push out an
	// administrator's attempt.
	manifestCapacity = 64
	renewBefore      = 5 * time.Minute
	maxUserTokens    = 10_000
)

// attempt is one authorisation between start and the callback.
type attempt struct {
	id, state, verifier, userID, ret string
	// kind is what the attempt is for: "" connecting an account, "manifest"
	// making the App. One never finishes the other.
	kind    string
	expires time.Time
	elem    *list.Element
}

// attemptStore keeps the authorisations in progress: in memory, bounded,
// one-time and short-lived. A restart only makes someone click again.
type attemptStore struct {
	mu    sync.Mutex
	items map[string]*attempt
	order *list.List
	now   func() time.Time
	cap   int
}

func newAttemptStore(now func() time.Time, capacity int) *attemptStore {
	return &attemptStore{items: map[string]*attempt{}, order: list.New(), now: now, cap: capacity}
}

func (p *attemptStore) put(a attempt) string {
	id := githubapp.RandomState()
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	for f := p.order.Front(); f != nil; f = p.order.Front() {
		old := p.items[f.Value.(string)]
		if old != nil && now.Before(old.expires) {
			break
		}
		p.order.Remove(f)
		delete(p.items, f.Value.(string))
	}
	for len(p.items) >= p.cap {
		f := p.order.Front()
		if f == nil {
			break
		}
		p.order.Remove(f)
		delete(p.items, f.Value.(string))
	}
	a.id, a.expires = id, now.Add(attemptTTL)
	a.elem = p.order.PushBack(id)
	p.items[id] = &a
	return id
}

type takeResult int

const (
	takeOK takeResult = iota
	takeUnknown
	takeOther // the state or the user is not the attempt's: it stays for its owner
)

// take removes and returns the attempt of a browser if the state and the user
// are the ones it was made for.
func (p *attemptStore) take(kind, id, state, userID string) (*attempt, takeResult) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a, ok := p.items[id]
	if !ok || a.kind != kind {
		return nil, takeUnknown
	}
	if !p.now().Before(a.expires) {
		p.order.Remove(a.elem)
		delete(p.items, id)
		return nil, takeUnknown
	}
	if subtle.ConstantTimeCompare([]byte(state), []byte(a.state)) != 1 || userID == "" || a.userID != userID {
		return nil, takeOther
	}
	p.order.Remove(a.elem)
	delete(p.items, id)
	return a, takeOK
}

func (p *attemptStore) len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.items)
}

// SafeReturn is auth.SafeReturn's rule, kept here so this package depends on
// nothing of sign-in: an absolute path on this site.
func safeReturn(s string) string {
	if s == "" || len(s) > 2048 || s[0] != '/' || (len(s) > 1 && (s[1] == '/' || s[1] == '\\')) {
		return "/"
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r == '\\' || r == 0x85 || r == 0x2028 || r == 0x2029 {
			return "/"
		}
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") {
		return "/"
	}
	if u.Path == "/login" || u.Path == "/setup" || u.Path == "/invite" || strings.HasPrefix(u.Path, "/api/") {
		return "/"
	}
	return s
}

// StartAuthorization begins connecting a user's GitHub account. It returns the
// address to send the browser to and the value of the cookie that ties the
// attempt to that browser.
func (s *Service) StartAuthorization(ctx context.Context, userID, ret string) (authURL, binding string, err error) {
	st, err := s.needUserAuth(ctx)
	if err != nil {
		return "", "", err
	}
	login := ""
	if a, err := store.GetGitHubAuthorization(ctx, s.opt.Store.Q(), userID); err == nil {
		login = a.Login
	}
	state := githubapp.RandomState()
	verifier, challenge := githubapp.PKCE()
	binding = s.attempts.put(attempt{state: state, verifier: verifier, userID: userID, ret: safeReturn(ret)})
	return s.c.AuthorizeURL(st.rec.ClientID, s.CallbackURL(), state, challenge, login), binding, nil
}

func (s *Service) needUserAuth(ctx context.Context) (*appState, error) {
	st, err := s.need(ctx)
	if err != nil {
		return nil, err
	}
	if !st.userAuth(s.opt.PublicURL) {
		return nil, errf(http.StatusConflict, CodeNotConfigured, "users cannot connect GitHub accounts: the App's client id and secret and the server's --public-url are needed")
	}
	return st, nil
}

// StartInstall begins connecting a user's GitHub account through the App's
// installation page: with "request user authorization during installation"
// GitHub authorises the person in the same flow, so installing on their own
// account and connecting it are one step. The attempt is bound as for
// StartAuthorization; its code carries no PKCE challenge, as GitHub gives
// none to an installation, so none is sent back.
func (s *Service) StartInstall(ctx context.Context, userID, ret string) (installPage, binding string, err error) {
	st, err := s.needUserAuth(ctx)
	if err != nil {
		return "", "", err
	}
	page := installURL(st.rec.HTMLURL)
	if page == "" {
		return "", "", errf(http.StatusConflict, CodeNotConfigured, "GitHub did not say where the App is installed")
	}
	state := githubapp.RandomState()
	binding = s.attempts.put(attempt{state: state, userID: userID, ret: safeReturn(ret)})
	return page + "?state=" + url.QueryEscape(state), binding, nil
}

// Callback failure codes (the connection_error of /settings).
const (
	cbDenied    = "denied"
	cbExpired   = "expired"
	cbError     = "provider_error"
	cbInUse     = "account_in_use"
	cbDifferent = "different_account"
	// cbInstalled: GitHub installed the App but sent no code (the App does not
	// ask for authorisation during installation). cbRequested: an organisation
	// has to approve the installation first.
	cbInstalled  = "installed"
	cbRequested  = "requested"
	failRedirect = "/settings?provider=github&connection_error="
)

// CompleteAuthorization finishes an authorisation from GitHub's redirect. It
// never fails: every outcome is a path to redirect to. userID is the user of
// the browser's session ("" without one); clearBinding says the attempt is over.
func (s *Service) CompleteAuthorization(ctx context.Context, binding, userID string, q url.Values) (redirect string, clearBinding bool) {
	one := func(k string) (string, bool) {
		v := q[k]
		if len(v) > 1 {
			return "", false
		}
		if len(v) == 0 {
			return "", true
		}
		return v[0], true
	}
	state, ok1 := one("state")
	code, ok2 := one("code")
	gerr, ok3 := one("error")
	if !ok1 || !ok2 || !ok3 || binding == "" || state == "" {
		return failRedirect + cbExpired, false
	}
	a, res := s.attempts.take("", binding, state, userID)
	if res != takeOK {
		return failRedirect + cbExpired, res == takeUnknown
	}
	fail := func(c string) (string, bool) { return failRedirect + c, true }
	if gerr != "" {
		s.log.Info("GitHub authorisation not given", "error", sanitize(gerr))
		return fail(cbDenied)
	}
	if code == "" {
		switch action, _ := one("setup_action"); action {
		case "request":
			return fail(cbRequested)
		case "install", "update":
			return fail(cbInstalled)
		}
		return fail(cbError)
	}
	st, err := s.state(ctx)
	if err != nil || st == nil || !st.userAuth(s.opt.PublicURL) {
		return fail(cbError)
	}
	tok, err := s.c.ExchangeCode(ctx, st.rec.ClientID, st.secret, code, s.CallbackURL(), a.verifier)
	if err != nil {
		s.log.Warn("GitHub refused the authorisation code", "reason", reason(err))
		return fail(cbError)
	}
	acct, err := s.c.User(ctx, tok.Access)
	if err != nil {
		s.log.Warn("could not read the GitHub account", "reason", reason(err))
		return fail(cbError)
	}
	now := s.opt.Now()
	db := s.opt.Store.Q()
	if prev, err := store.GetGitHubAuthorization(ctx, db, userID); err == nil && prev.GitHubID != acct.ID {
		n, err := store.CountUserGitHubSpaces(ctx, db, userID)
		if err != nil {
			s.log.Error("could not count the spaces of a GitHub connection", "error", err)
			return fail(cbError)
		}
		if n > 0 {
			return fail(cbDifferent)
		}
	}
	row := store.GitHubAuthorization{UserID: userID, GitHubID: acct.ID, Login: acct.Login, State: store.GitHubOK,
		AccessExpires: tok.AccessExpires, RefreshExpires: tok.RefreshExpires, Connected: now, Updated: now}
	if row.AccessEnc, err = s.opt.Secrets.Seal(userID, purposeAccess, []byte(tok.Access)); err != nil {
		s.log.Error("could not seal a GitHub token", "error", err)
		return fail(cbError)
	}
	if tok.Refresh != "" {
		if row.RefreshEnc, err = s.opt.Secrets.Seal(userID, purposeRefresh, []byte(tok.Refresh)); err != nil {
			s.log.Error("could not seal a GitHub token", "error", err)
			return fail(cbError)
		}
	}
	switch err := store.PutGitHubAuthorization(ctx, db, row); {
	case errors.Is(err, store.ErrExists):
		return fail(cbInUse)
	case err != nil:
		s.log.Error("could not store a GitHub authorisation", "error", err)
		return fail(cbError)
	}
	s.bump(userID)
	s.forgetNoApp(acct.Login) // they may have installed the App in this very flow
	s.changed(userID)
	return a.ret, true
}

// reason is an error as a log line may say it: never a token, since none of
// this package's errors carries one.
func reason(err error) string {
	var oe *githubapp.OAuthError
	if errors.As(err, &oe) {
		return oe.Code
	}
	var se *githubapp.StatusError
	if errors.As(err, &se) {
		return se.Error()
	}
	return "could not be reached"
}

func sanitize(s string) string {
	if len(s) > 64 {
		s = s[:64]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

const (
	purposeAccess  = "github-access"
	purposeRefresh = "github-refresh"
)

// --- the user's connection --------------------------------------------------------

// Connection returns the user's connection, or nil.
func (s *Service) Connection(ctx context.Context, userID string) (*connections.Connection, error) {
	db := s.opt.Store.Q()
	a, err := store.GetGitHubAuthorization(ctx, db, userID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	n, err := store.CountUserGitHubSpaces(ctx, db, userID)
	if err != nil {
		return nil, err
	}
	return &connections.Connection{Login: a.Login, AccountID: strconv.FormatInt(a.GitHubID, 10), State: a.State, Connected: a.Connected, RefreshExpires: a.RefreshExpires, Spaces: n}, nil
}

// Disconnect deletes a user's connection. GitHub is asked to revoke the grant
// (best effort); the tokens are deleted from the database and memory whatever it answers.
func (s *Service) Disconnect(ctx context.Context, userID string) error {
	db := s.opt.Store.Q()
	a, err := store.GetGitHubAuthorization(ctx, db, userID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if st, err := s.state(ctx); err == nil && st != nil {
		s.revoke(ctx, st, *a)
	}
	if err := store.DeleteGitHubAuthorization(ctx, db, userID); err != nil {
		return err
	}
	s.bump(userID)
	return nil
}

// revoke asks GitHub to revoke a grant, quickly and without caring how it ends.
func (s *Service) revoke(ctx context.Context, st *appState, a store.GitHubAuthorization) {
	if a.AccessEnc == "" || st.rec.ClientID == "" || st.secret == "" {
		return
	}
	b, err := s.opt.Secrets.Open(a.UserID, purposeAccess, a.AccessEnc)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := s.c.RevokeGrant(ctx, st.rec.ClientID, st.secret, string(b)); err != nil {
		s.log.Info("GitHub did not revoke a grant", "reason", reason(err))
	}
}

// --- tokens and their renewal -------------------------------------------------------

type cachedUser struct {
	access  string
	expires time.Time // zero: none
	version int64
}

type renewal struct {
	done   chan struct{}
	access string
	err    error
}

// UserToken returns the user's access token, renewing it first when it has
// less than five minutes left. ErrNoAuthorization or ErrAuthorizationExpired
// when there is nothing to use; an *Error when GitHub cannot be asked.
func (s *Service) UserToken(ctx context.Context, userID string) (string, error) {
	now := s.opt.Now()
	s.mu.Lock()
	if c, ok := s.tokens[userID]; ok && (c.expires.IsZero() || now.Add(renewBefore).Before(c.expires)) {
		s.mu.Unlock()
		return c.access, nil
	}
	s.mu.Unlock()
	return s.renew(ctx, userID, false)
}

// renew renews one user's tokens, one renewal at a time per user; a caller that
// arrives meanwhile waits for it. force renews even if the token is still good.
func (s *Service) renew(ctx context.Context, userID string, force bool) (string, error) {
	s.mu.Lock()
	if r, ok := s.renewing[userID]; ok {
		s.mu.Unlock()
		select {
		case <-r.done:
			return r.access, r.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	r := &renewal{done: make(chan struct{})}
	s.renewing[userID] = r
	s.mu.Unlock()
	// Once GitHub has handed out a new refresh token the old one is dead: the
	// new pair must reach the database even if this request goes away.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	r.access, r.err = s.doRenew(wctx, userID, force)
	cancel()
	s.mu.Lock()
	delete(s.renewing, userID)
	s.mu.Unlock()
	close(r.done)
	return r.access, r.err
}

// tokenGen is how many times a user's connection was replaced or removed in
// this process (user), and the whole cache dropped (all). A renewal notes it
// before it reads the database and caches what it read only if it is unchanged.
type tokenGen struct{ all, user uint64 }

func (s *Service) generation(userID string) tokenGen {
	s.mu.Lock()
	defer s.mu.Unlock()
	return tokenGen{all: s.genAll, user: s.gens[userID]}
}

// bump says the user's connection was replaced or removed (after the database
// was written): the cached token goes, and a renewal that read before it does
// not cache. One counter per user who connected or disconnected since the start.
func (s *Service) bump(userID string) {
	s.mu.Lock()
	s.gens[userID]++
	delete(s.tokens, userID)
	s.mu.Unlock()
}

// dropAllTokens empties the cache (the App changed or went away).
func (s *Service) dropAllTokens() {
	s.mu.Lock()
	s.genAll++
	s.tokens = map[string]cachedUser{}
	s.mu.Unlock()
}

// testHookAfterRenewRead, when set, runs after a renewal read the connection.
var testHookAfterRenewRead func(userID string)

func (s *Service) cacheToken(userID string, gen tokenGen, access string, expires time.Time, version int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if gen != (tokenGen{all: s.genAll, user: s.gens[userID]}) {
		return // the connection changed after this was read
	}
	if len(s.tokens) >= maxUserTokens {
		for k := range s.tokens {
			delete(s.tokens, k)
			break
		}
	}
	s.tokens[userID] = cachedUser{access: access, expires: expires, version: version}
}

func (s *Service) dropToken(userID string) {
	s.mu.Lock()
	delete(s.tokens, userID)
	s.mu.Unlock()
}

func (s *Service) doRenew(ctx context.Context, userID string, force bool) (string, error) {
	db := s.opt.Store.Q()
	for pass := 0; pass < 3; pass++ {
		gen := s.generation(userID)
		row, err := store.GetGitHubAuthorization(ctx, db, userID)
		if testHookAfterRenewRead != nil {
			testHookAfterRenewRead(userID)
		}
		if errors.Is(err, store.ErrNotFound) {
			s.dropToken(userID)
			return "", ErrNoAuthorization
		}
		if err != nil {
			return "", err
		}
		if row.State != store.GitHubOK {
			s.dropToken(userID)
			return "", ErrAuthorizationExpired
		}
		now := s.opt.Now()
		access, err := s.opt.Secrets.Open(userID, purposeAccess, row.AccessEnc)
		if err != nil {
			return "", err
		}
		good := row.AccessExpires.IsZero() || now.Add(renewBefore).Before(row.AccessExpires)
		if good && !force {
			s.cacheToken(userID, gen, string(access), row.AccessExpires, row.Version)
			return string(access), nil
		}
		if row.RefreshEnc == "" {
			if row.AccessExpires.IsZero() || now.Before(row.AccessExpires) {
				s.cacheToken(userID, gen, string(access), row.AccessExpires, row.Version) // no refresh token: use it to the end
				return string(access), nil
			}
			s.expire(ctx, row)
			return "", ErrAuthorizationExpired
		}
		st, err := s.state(ctx)
		if err != nil {
			return "", err
		}
		if st == nil || st.rec.ClientID == "" || st.secret == "" {
			return "", notConfigured()
		}
		refresh, err := s.opt.Secrets.Open(userID, purposeRefresh, row.RefreshEnc)
		if err != nil {
			return "", err
		}
		tok, err := s.c.Refresh(ctx, st.rec.ClientID, st.secret, string(refresh))
		if err != nil {
			if githubapp.IsBadRefresh(err) {
				s.log.Info("a GitHub authorisation expired or was revoked", "user", userID)
				s.expire(ctx, row)
				return "", ErrAuthorizationExpired
			}
			var oe *githubapp.OAuthError
			if errors.As(err, &oe) {
				// Another refusal (the client credentials changed): the App needs attention.
				s.log.Warn("GitHub refused to renew a token", "reason", oe.Code)
				return "", notConfigured()
			}
			return "", s.wrap(err)
		}
		accessEnc, err := s.opt.Secrets.Seal(userID, purposeAccess, []byte(tok.Access))
		if err != nil {
			return "", err
		}
		refreshEnc := ""
		if tok.Refresh != "" {
			if refreshEnc, err = s.opt.Secrets.Seal(userID, purposeRefresh, []byte(tok.Refresh)); err != nil {
				return "", err
			}
		}
		// The new pair is saved before it is used, and replaces only the pair it
		// was made from.
		var replaced bool
		for try := 0; try < 3; try++ {
			if replaced, err = store.ReplaceGitHubTokens(ctx, db, userID, row.Version, accessEnc, refreshEnc, tok.AccessExpires, tok.RefreshExpires, s.opt.Now()); err == nil {
				break
			}
			time.Sleep(time.Duration(try+1) * 100 * time.Millisecond)
		}
		if err != nil {
			// The old refresh token is dead and the new one is not stored: this
			// connection cannot go on.
			s.log.Error("could not store a renewed GitHub token", "user", userID, "error", err)
			s.expire(ctx, row)
			return "", ErrAuthorizationExpired
		}
		if !replaced {
			continue // reconnected or renewed elsewhere meanwhile: use what is there now
		}
		s.cacheToken(userID, gen, tok.Access, tok.AccessExpires, row.Version+1)
		return tok.Access, nil
	}
	return "", errf(http.StatusServiceUnavailable, CodeUnavailable, "the GitHub authorisation keeps changing; try again")
}

func (s *Service) expire(ctx context.Context, row *store.GitHubAuthorization) {
	if _, err := store.ExpireGitHubAuthorization(ctx, s.opt.Store.Q(), row.UserID, row.Version, s.opt.Now()); err != nil {
		s.log.Error("could not mark a GitHub authorisation expired", "user", row.UserID, "error", err)
	}
	s.dropToken(row.UserID)
}

// RenewDue renews, one at a time, the connections whose refresh token ends
// within RenewWithin, so someone whose spaces sit closed for months stays
// connected. It returns how many it renewed.
func (s *Service) RenewDue(ctx context.Context) int {
	st, err := s.state(ctx)
	if err != nil || st == nil || st.rec.ClientID == "" {
		return 0
	}
	n := 0
	// Pages by (refresh end, user id), so connections that keep failing do not
	// hold up the ones after them. A renewed one moves past the window; one
	// seen twice in a run (its new end still inside it) is not tried again.
	before := s.opt.Now().Add(s.opt.RenewWithin)
	var cur store.DueCursor
	tried := map[string]bool{}
	for {
		due, err := store.DueGitHubRenewals(ctx, s.opt.Store.Q(), before, cur, 50)
		if err != nil {
			s.log.Error("could not look for GitHub authorisations to renew", "error", err)
			return n
		}
		for _, a := range due {
			cur = a.Next()
			if tried[a.UserID] {
				continue
			}
			tried[a.UserID] = true
			if ctx.Err() != nil {
				return n
			}
			if _, err := s.renew(ctx, a.UserID, true); err != nil {
				s.log.Info("could not renew a GitHub authorisation", "user", a.UserID, "reason", err.Error())
				continue
			}
			n++
		}
		if len(due) < 50 {
			return n
		}
	}
}

// RunRenew renews the connections that need it now and then once every RenewEvery, until ctx ends.
func (s *Service) RunRenew(ctx context.Context) {
	first := time.Minute
	if s.opt.RenewEvery < first {
		first = s.opt.RenewEvery
	}
	t := time.NewTimer(first)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if n := s.RenewDue(ctx); n > 0 {
			s.log.Info("renewed GitHub authorisations", "count", n)
		}
		t.Reset(s.opt.RenewEvery)
	}
}

// Info is the state of GitHub on this server and of the user's connection.
func (s *Service) Info(ctx context.Context, userID string) (*connections.Info, error) {
	st, err := s.state(ctx)
	if err != nil {
		return nil, err
	}
	in := &connections.Info{}
	if st != nil {
		in.Configured = true
		in.App = &connections.App{Name: st.rec.Name, Slug: st.rec.Slug, HTMLURL: st.rec.HTMLURL, InstallURL: installURL(st.rec.HTMLURL)}
		in.UserAuthorization = st.userAuth(s.opt.PublicURL)
	}
	if in.Connection, err = s.Connection(ctx, userID); err != nil {
		return nil, err
	}
	if in.Leftovers, err = s.Leftovers(ctx, userID); err != nil {
		return nil, err
	}
	return in, nil
}

// Leftovers lists a user's reminders, newest first.
func (s *Service) Leftovers(ctx context.Context, userID string) ([]connections.Leftover, error) {
	rows, err := store.ListGitHubLeftovers(ctx, s.opt.Store.Q(), userID)
	if err != nil {
		return nil, err
	}
	out := make([]connections.Leftover, len(rows))
	for i, r := range rows {
		out[i] = connections.Leftover{RepoID: r.RepoID, FullName: r.FullName, HTMLURL: r.HTMLURL, SpaceName: r.SpaceName, Created: r.Created}
	}
	return out, nil
}

const leftoverTimeout = 10 * time.Second

// RecordLeftover remembers, for the user, a repository that was made for a
// space that then could not be made. It is written even when ctx has run out
// or was cancelled (the create that failed may have used all its time), under
// a short timeout of its own.
func (s *Service) RecordLeftover(ctx context.Context, userID string, repo *connections.Repo, spaceName string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), leftoverTimeout)
	defer cancel()
	return store.PutGitHubLeftover(ctx, s.opt.Store.Q(), store.GitHubLeftover{UserID: userID, RepoID: repo.ID, FullName: repo.FullName,
		HTMLURL: repo.HTMLURL, SpaceName: spaceName, Created: s.opt.Now()})
}

// DismissLeftover forgets the user's reminder of a repository; there being none is fine.
func (s *Service) DismissLeftover(ctx context.Context, userID string, repoID int64) error {
	_, err := store.DeleteGitHubLeftover(ctx, s.opt.Store.Q(), userID, repoID)
	return err
}

// Attempts is how many authorisations are in progress (tests).
func (s *Service) Attempts() int { return s.attempts.len() }
