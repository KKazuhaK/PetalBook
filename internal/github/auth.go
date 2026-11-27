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

	"petalbook.app/server/internal/connections"
	"petalbook.app/server/internal/githubapp"
	"petalbook.app/server/internal/store"
)

// Errors of using a user's authorisation (the token service's).
var (
	// ErrNoAuthorization: the user has not connected a GitHub account.
	ErrNoAuthorization = connections.ErrNoAuthorization
	// ErrAuthorizationExpired: the refresh token ran out or was revoked.
	ErrAuthorizationExpired = connections.ErrAuthorizationExpired
)

// Bounds of the sign-ins in progress.
const (
	attemptTTL      = 10 * time.Minute
	attemptCapacity = 10_000
	// manifestCapacity bounds the attempts to make the App, which have a store of
	// their own: a flood of people connecting accounts must not push out an
	// administrator's attempt.
	manifestCapacity = 64
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
	if a, err := store.GetRepoLink(ctx, s.opt.Store.Q(), ProviderID, userID); err == nil {
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
		return nil, errf(http.StatusConflict, CodeNotConfigured, "Users cannot connect GitHub accounts: the App's client id and secret and the server's --public-url are needed.")
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
		return "", "", errf(http.StatusConflict, CodeNotConfigured, "GitHub did not say where the App is installed.")
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
	// cbLastMethod: the account the person authorised is not the one their link
	// is to, and the link is their only way to sign in.
	cbLastMethod = "last_method"
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
	db := s.opt.Store.Q()
	subject := strconv.FormatInt(acct.ID, 10)
	// Their link at GitHub, whatever it is for: a sign-in that holds no tokens is
	// moved to the other account just the same.
	if prev, err := store.FindUserLink(ctx, db, ProviderID, userID); err == nil && prev.Subject != subject {
		// Another GitHub account takes the link over: what syncs through the old
		// one would be left without a credential, and a sign-in the link gave
		// (and the address it brought) belongs to the old account.
		n, err := store.CountUserProviderSpaces(ctx, db, ProviderID, userID)
		if err != nil {
			s.log.Error("could not count the spaces of a GitHub connection", "error", err)
			return fail(cbError)
		}
		if n > 0 {
			return fail(cbDifferent)
		}
		if prev.SignIn {
			s.mu.Lock()
			guard := s.guard
			s.mu.Unlock()
			if guard == nil {
				return fail(cbError)
			}
			ok, err := guard(ctx, userID, prev.ID)
			if err != nil {
				s.log.Error("could not check the person's other ways to sign in", "error", err)
				return fail(cbError)
			}
			if !ok {
				return fail(cbLastMethod)
			}
		}
	}
	switch err := s.tok.Put(ctx, connections.Account{ProviderID: ProviderID, UserID: userID, Subject: subject, Login: acct.Login},
		connections.Pair{Access: tok.Access, Refresh: tok.Refresh, AccessExpires: tok.AccessExpires, RefreshExpires: tok.RefreshExpires}); {
	case errors.Is(err, store.ErrExists):
		return fail(cbInUse)
	case err != nil:
		s.log.Error("could not store a GitHub authorisation", "error", err)
		return fail(cbError)
	}
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

// --- the user's connection --------------------------------------------------------

// Connection returns the user's connection, or nil.
func (s *Service) Connection(ctx context.Context, userID string) (*connections.Connection, error) {
	db := s.opt.Store.Q()
	a, err := store.GetRepoLink(ctx, db, ProviderID, userID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	n, err := store.CountUserProviderSpaces(ctx, db, ProviderID, userID)
	if err != nil {
		return nil, err
	}
	return &connections.Connection{Login: a.Login, AccountID: a.Subject, State: a.State, Connected: a.Connected, RefreshExpires: a.RefreshExpires, Spaces: n}, nil
}

// Disconnect drops a user's repository tokens (a link that also signs them in
// stays, without tokens). GitHub is asked to revoke the grant (best effort);
// the tokens are deleted from the database and memory whatever it answers.
func (s *Service) Disconnect(ctx context.Context, userID string) error {
	a, err := store.GetRepoLink(ctx, s.opt.Store.Q(), ProviderID, userID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if st, err := s.state(ctx); err == nil && st != nil {
		s.revoke(ctx, st, a)
	}
	return s.tok.Remove(ctx, ProviderID, userID)
}

// revoke asks GitHub to revoke a grant, quickly and without caring how it ends.
func (s *Service) revoke(ctx context.Context, st *appState, a *store.RepoLink) {
	if a.AccessEnc == "" || st.rec.ClientID == "" || st.secret == "" {
		return
	}
	access, err := s.tok.OpenAccess(a)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := s.c.RevokeGrant(ctx, st.rec.ClientID, st.secret, access); err != nil {
		s.log.Info("GitHub did not revoke a grant", "reason", reason(err))
	}
}

// --- tokens ------------------------------------------------------------------------

// UserToken returns the user's access token, renewing it first when it has
// less than five minutes left (the token service's). ErrNoAuthorization or
// ErrAuthorizationExpired when there is nothing to use; an *Error when GitHub
// cannot be asked.
func (s *Service) UserToken(ctx context.Context, userID string) (string, error) {
	return s.tok.Token(ctx, ProviderID, userID)
}

// RefreshTokens trades a refresh token for a new pair at GitHub: it is what the
// token service asks of this provider (connections.Refresher).
func (s *Service) RefreshTokens(ctx context.Context, refresh string) (*connections.Pair, error) {
	st, err := s.state(ctx)
	if err != nil {
		return nil, err
	}
	if st == nil || st.rec.ClientID == "" || st.secret == "" {
		return nil, notConfigured()
	}
	tok, err := s.c.Refresh(ctx, st.rec.ClientID, st.secret, refresh)
	if err != nil {
		if githubapp.IsBadRefresh(err) {
			return nil, connections.ErrRevoked
		}
		var oe *githubapp.OAuthError
		if errors.As(err, &oe) {
			// Another refusal (the client credentials changed): the App needs attention.
			s.log.Warn("GitHub refused to renew a token", "reason", oe.Code)
			return nil, notConfigured()
		}
		return nil, s.wrap(err)
	}
	return &connections.Pair{Access: tok.Access, Refresh: tok.Refresh, AccessExpires: tok.AccessExpires, RefreshExpires: tok.RefreshExpires}, nil
}

// expire marks a link whose token GitHub no longer accepts as needing the
// person's attention (the token service's).
func (s *Service) expire(ctx context.Context, row *store.RepoLink) {
	s.tok.Expire(ctx, row)
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
	rows, err := store.ListRepoLeftovers(ctx, s.opt.Store.Q(), ProviderID, userID)
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
	return store.PutRepoLeftover(ctx, s.opt.Store.Q(), store.RepoLeftover{ProviderID: ProviderID, UserID: userID, RepoID: repo.ID, FullName: repo.FullName,
		HTMLURL: repo.HTMLURL, SpaceName: spaceName, Created: s.opt.Now()})
}

// DismissLeftover forgets the user's reminder of a repository; there being none is fine.
func (s *Service) DismissLeftover(ctx context.Context, userID string, repoID int64) error {
	_, err := store.DeleteRepoLeftover(ctx, s.opt.Store.Q(), ProviderID, userID, repoID)
	return err
}

// Attempts is how many authorisations are in progress (tests).
func (s *Service) Attempts() int { return s.attempts.len() }
