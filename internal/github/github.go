// Package github is the server's use of its one GitHub App: the App's settings
// (the config of the provider row "github"), the people's GitHub links (a link
// of linked_accounts that also holds their repository tokens; the tokens and
// their renewal are the token service's, internal/connections), the accounts
// and repositories a person can choose from, the creation of a repository, and
// the token Git is given for a space. It keeps its state in the state database
// (everything secret sealed with the server's key) and in memory (the
// authorisations in progress), and reaches GitHub only through the githubapp
// client it was given.
package github

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"petalbook.app/server/internal/connections"
	"petalbook.app/server/internal/githubapp"
	"petalbook.app/server/internal/secret"
	"petalbook.app/server/internal/secrets"
	"petalbook.app/server/internal/store"
)

// ProviderID is the id this provider has in the registry and in every path.
const ProviderID = "github"

// The codes this package uses, by their generic names (see connections).
const (
	CodeNotConfigured     = connections.CodeNotConfigured
	CodeNotConnected      = connections.CodeNotConnected
	CodeOwnerNotAllowed   = connections.CodeOwnerNotAllowed
	CodeNameTaken         = connections.CodeNameTaken
	CodeRefused           = connections.CodeRefused
	CodeMissingPermission = connections.CodeMissingPermission
	CodeAppNotInstalled   = connections.CodeAppNotInstalled
	CodePublicURLNeeded   = connections.CodePublicURLNeeded
	CodeRepoNotInInstall  = connections.CodeRepoNotInInstall
	CodePushFailed        = connections.CodePushFailed
	CodeKeyInvalid        = connections.CodeKeyInvalid
	CodeAppRejected       = connections.CodeAppRejected
	CodeClientRejected    = connections.CodeClientRejected
	CodeInUse             = connections.CodeInUse
	CodeProviderError     = connections.CodeProviderError
	CodeInvalid           = connections.CodeInvalid
	CodeForbidden         = connections.CodeForbidden
	CodeNotFound          = connections.CodeNotFound
	CodeRateLimited       = connections.CodeRateLimited
	CodeUnavailable       = connections.CodeUnavailable
	CodeInternal          = connections.CodeInternal

	CodeConnectionExpired = connections.CodeConnectionExpired
	CodeConnectionMissing = connections.CodeConnectionMissing
	CodeInstallationGone  = connections.CodeInstallationGone
	CodeHostMismatch      = connections.CodeHostMismatch
)

// Via values.
const (
	ViaApp  = connections.ViaApp
	ViaUser = connections.ViaUser
)

func errf(status int, code, format string, a ...any) *connections.Error {
	return connections.Errorf(ProviderID, status, code, format, a...)
}

func notConfigured() *connections.Error {
	return errf(http.StatusConflict, CodeNotConfigured, "This server has no GitHub App set up (an administrator sets it up in the administration).")
}

func notConnected() *connections.Error {
	return errf(http.StatusConflict, CodeNotConnected, "Connect your GitHub account first.")
}

// Options configures a Service.
type Options struct {
	Store   *store.Store
	Secrets *secret.Box
	Client  *githubapp.Client
	// PublicURL is the address people use; user authorisation needs it.
	PublicURL string
	Now       func() time.Time
	Logger    *slog.Logger
	// Tokens is the token service that keeps the people's tokens and renews them
	// (one for every kind of provider, run by the server). nil makes the
	// service's own, which asks this service to renew.
	Tokens *connections.Tokens
	// SettingsTTL is how long the App's settings are kept in memory before they
	// are read again, for servers sharing a database (default a minute).
	SettingsTTL time.Duration
}

// Service is safe for concurrent use.
type Service struct {
	opt   Options
	vault *secrets.Service
	log   *slog.Logger
	c     *githubapp.Client
	tok   *connections.Tokens

	mu        sync.Mutex
	cur       *appState // nil: not loaded
	curAt     time.Time
	attempts  *attemptStore
	manifests *attemptStore          // the attempts to make the App: apart from attempts (see manifestCapacity)
	noApp     map[string]time.Time   // GitHub logins the App is known not to be installed for, until (guarded by mu)
	rechecked map[string]time.Time   // PetalBook user ids, until when their next recheck is refused (guarded by mu)
	pending   map[string]*pendingApp // Apps GitHub made that could not be saved, by administrator (guarded by mu)
	onChange  func(userID string)
	autoAt    time.Time // when the App was last re-checked because a token was refused
	// guard says whether a person's sign-in link may be given up (they have
	// another way to sign in); providersChanged is told when the GitHub row
	// was written, so the sign-in side reads it again.
	guard            SigninGuard
	providersChanged func(ctx context.Context)
}

// SigninGuard says whether a person may give up the sign-in of one of their
// links (linkID): that they have another way to sign in. It is asked in the
// transaction that moves the link (q), so its answer holds for the write. An
// error is a refusal to say yes.
type SigninGuard func(ctx context.Context, q store.Q, userID, linkID string) (bool, error)

// SetSigninGuard sets how a link that signs a person in is protected: connecting
// repositories with another GitHub account moves the link to that account, and
// the sign-in with it, so it is not done to the person's last way in.
func (s *Service) SetSigninGuard(g SigninGuard) {
	s.mu.Lock()
	s.guard = g
	s.mu.Unlock()
}

// SetProvidersChanged sets what is called when the GitHub row was written (the
// App saved, changed or removed), so that sign-in reads it again.
func (s *Service) SetProvidersChanged(f func(ctx context.Context)) {
	s.mu.Lock()
	s.providersChanged = f
	s.mu.Unlock()
}

func (s *Service) rowChanged(ctx context.Context) {
	s.mu.Lock()
	f := s.providersChanged
	s.mu.Unlock()
	if f != nil {
		f(ctx)
	}
}

// New returns a Service.
func New(opt Options) (*Service, error) {
	if opt.Store == nil || opt.Secrets == nil || opt.Client == nil {
		return nil, errors.New("github: a store, a secret box and a client are required")
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Logger == nil {
		opt.Logger = slog.New(slog.DiscardHandler)
	}
	if opt.SettingsTTL <= 0 {
		opt.SettingsTTL = time.Minute
	}
	opt.PublicURL = strings.TrimRight(opt.PublicURL, "/")
	opt.Client.UseLogger(opt.Logger) // what GitHub refuses is told in the server's log
	s := &Service{opt: opt, vault: secrets.Of(opt.Store, opt.Secrets), log: opt.Logger, c: opt.Client, tok: opt.Tokens,
		attempts: newAttemptStore(opt.Now, attemptCapacity), manifests: newAttemptStore(opt.Now, manifestCapacity),
		noApp: map[string]time.Time{}, rechecked: map[string]time.Time{}, pending: map[string]*pendingApp{}}
	if s.tok == nil {
		reg := connections.NewRegistry(s)
		s.tok = connections.NewTokens(connections.TokenOptions{Store: opt.Store, Secrets: opt.Secrets, Registry: reg, Now: opt.Now, Logger: opt.Logger})
	}
	return s, nil
}

// Tokens is the token service the service keeps the people's tokens with.
func (s *Service) Tokens() *connections.Tokens { return s.tok }

// Client is the GitHub client; its web base is where repositories are.
func (s *Service) Client() *githubapp.Client { return s.c }

// OnChange registers what is told when something that spaces wait on is fixed:
// a user connected (userID), or the App was saved (userID is empty).
func (s *Service) OnChange(f func(userID string)) {
	s.mu.Lock()
	s.onChange = f
	s.mu.Unlock()
}

func (s *Service) changed(userID string) {
	s.mu.Lock()
	f := s.onChange
	s.mu.Unlock()
	if f != nil {
		f(userID)
	}
}

// --- the App's settings ------------------------------------------------------------

// appRecord is what is stored of the App in the config of the provider row
// (store.GetGitHubApp). Its secrets
// are not in it: the private key, the client secret and the webhook secret
// (which GitHub gives with an App made from a manifest; no webhook is used, so
// it is only kept) are secrets of the site (internal/secrets). The fingerprint
// of the key says which key the record was made with.
type appRecord struct {
	AppID       string            `json:"appId"`
	ClientID    string            `json:"clientId,omitempty"`
	Fingerprint string            `json:"fingerprint"`
	Slug        string            `json:"slug"`
	Name        string            `json:"name"`
	Owner       string            `json:"owner"`
	HTMLURL     string            `json:"htmlUrl"`
	Permissions map[string]string `json:"permissions"`
	Checked     time.Time         `json:"checked"`
	// Gaps are the installations that lack a permission the App needs (an
	// App whose permissions grew, installed where nobody accepted that yet).
	Gaps []gapRec `json:"gaps,omitempty"`
	// AnyAccount says whether the App can be installed on any account; nil when
	// GitHub did not say and nothing shows it.
	AnyAccount *bool `json:"anyAccount,omitempty"`
}

type gapRec struct {
	ID      int64    `json:"id"`
	Account string   `json:"account"`
	Missing []string `json:"missing"`
}

// appState is the App ready for use. Once published in Service.cur it is
// immutable: a reload builds a new one and swaps it in under mu, so a caller
// holding one always sees a whole, consistent state.
type appState struct {
	rec    appRecord
	app    *githubapp.App
	key    *rsa.PrivateKey
	secret string
}

// state is the App as it is saved, or nil when there is none.
func (s *Service) state(ctx context.Context) (*appState, error) {
	s.mu.Lock()
	if !s.curAt.IsZero() && s.opt.Now().Sub(s.curAt) < s.opt.SettingsTTL {
		cur := s.cur
		s.mu.Unlock()
		return cur, nil
	}
	s.mu.Unlock()
	raw, ok, err := store.GetGitHubApp(ctx, s.opt.Store.Q())
	if err != nil {
		return nil, err
	}
	var st *appState
	if ok {
		if st, err = s.build(ctx, raw); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	// Keep the App client already in use when the saved one is the same (it
	// holds the token cache), but publish a new state: callers may still be
	// reading the one they took, so a published state is never written to.
	if st != nil && s.cur != nil && s.cur.rec.Fingerprint == st.rec.Fingerprint && s.cur.rec.AppID == st.rec.AppID {
		st.app, st.key = s.cur.app, s.cur.key
	}
	s.cur, s.curAt = st, s.opt.Now()
	s.mu.Unlock()
	return st, nil
}

func (s *Service) build(ctx context.Context, raw string) (*appState, error) {
	var rec appRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return nil, fmt.Errorf("the GitHub App settings are unreadable: %w", err)
	}
	q := s.opt.Store.Q()
	pemb, ok, err := s.vault.Open(ctx, q, "", secrets.GitHubAppKey)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("there is none")
		}
		return nil, fmt.Errorf("the GitHub App key: %w", err)
	}
	key, err := githubapp.ParseKey([]byte(pemb))
	if err != nil {
		return nil, fmt.Errorf("the GitHub App key: %w", err)
	}
	app, err := s.c.NewApp(rec.AppID, key)
	if err != nil {
		return nil, err
	}
	st := &appState{rec: rec, app: app, key: key}
	if rec.ClientID != "" {
		v, ok, err := s.vault.Open(ctx, q, "", secrets.GitHubClientSecret)
		if err != nil {
			return nil, fmt.Errorf("the GitHub client secret: %w", err)
		}
		if ok {
			st.secret = v
		}
	}
	return st, nil
}

func (s *Service) forget() {
	s.mu.Lock()
	s.cur, s.curAt = nil, time.Time{}
	s.mu.Unlock()
}

// need is the App, or the "not configured" answer.
func (s *Service) need(ctx context.Context) (*appState, error) {
	st, err := s.state(ctx)
	if err != nil {
		return nil, err
	}
	if st == nil {
		return nil, notConfigured()
	}
	return st, nil
}

// Configured reports whether an App is set up.
func (s *Service) Configured(ctx context.Context) bool {
	st, err := s.state(ctx)
	return err == nil && st != nil
}

// UserAuthorization reports whether users can connect their GitHub accounts:
// the client id and secret are saved and the server has a public URL.
func (st *appState) userAuth(publicURL string) bool {
	return st != nil && st.rec.ClientID != "" && st.secret != "" && publicURL != ""
}

// CallbackURL is where GitHub sends people back to; "" without a public URL.
func (s *Service) CallbackURL() string {
	if s.opt.PublicURL == "" {
		return ""
	}
	return s.opt.PublicURL + connections.CallbackPath(ProviderID)
}

// SigninCallbackURL is where GitHub sends people back to when they sign in with
// the App's client (the sign-in provider's own route); "" without a public URL.
func (s *Service) SigninCallbackURL() string {
	if s.opt.PublicURL == "" {
		return ""
	}
	return s.opt.PublicURL + "/api/auth/github/" + ProviderID + "/callback"
}

// wrap turns an error of the GitHub client into the answer for the caller:
// rate limits, an unreachable GitHub; anything else is returned as it came.
func (s *Service) wrap(err error) error {
	if err == nil {
		return nil
	}
	var he *connections.Error
	if errors.As(err, &he) {
		return err
	}
	var se *githubapp.StatusError
	if errors.As(err, &se) {
		switch {
		case se.RateLimited:
			return &connections.Error{Provider: ProviderID, Code: CodeRateLimited, Status: http.StatusTooManyRequests, Msg: "GitHub is limiting this server; try again in a moment.",
				RetryAfter: se.RetryAfter(s.opt.Now()), Err: err}
		case se.Status >= 500:
			return &connections.Error{Provider: ProviderID, Code: CodeUnavailable, Status: http.StatusServiceUnavailable, Msg: "GitHub cannot be reached right now.", Err: err}
		}
		return err
	}
	var oe *githubapp.OAuthError
	if errors.As(err, &oe) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	return &connections.Error{Provider: ProviderID, Code: CodeUnavailable, Status: http.StatusServiceUnavailable, Msg: "GitHub cannot be reached right now.", Err: err}
}

// Offline reports whether err means GitHub could not be reached or is limiting
// us, as opposed to GitHub having refused something.
func Offline(err error) bool {
	var he *connections.Error
	if errors.As(err, &he) {
		return he.Code == CodeUnavailable || he.Code == CodeRateLimited
	}
	return false
}
