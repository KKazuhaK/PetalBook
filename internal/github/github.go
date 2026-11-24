// Package github is the server's use of its one GitHub App: the App's settings,
// the users' GitHub authorisations and their renewal, the accounts and
// repositories a person can choose from, the creation of a repository, and the
// token Git is given for a space. It keeps its state in the state database
// (everything secret sealed with the server's key) and in memory (tokens, and
// the sign-ins in progress), and reaches GitHub only through the githubapp
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

	"github.com/KKazuhaK/PetalBook/internal/connections"
	"github.com/KKazuhaK/PetalBook/internal/githubapp"
	"github.com/KKazuhaK/PetalBook/internal/secret"
	"github.com/KKazuhaK/PetalBook/internal/store"
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
	return errf(http.StatusConflict, CodeNotConfigured, "this server has no GitHub App set up (an administrator sets it up in the administration)")
}

func notConnected() *connections.Error {
	return errf(http.StatusConflict, CodeNotConnected, "connect your GitHub account first")
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
	// RenewEvery is how often the connections that need it are renewed
	// (default a day), and RenewWithin how close to its end a refresh token
	// must be for that (default 30 days).
	RenewEvery, RenewWithin time.Duration
	// SettingsTTL is how long the App's settings are kept in memory before they
	// are read again, for servers sharing a database (default a minute).
	SettingsTTL time.Duration
}

// Service is safe for concurrent use.
type Service struct {
	opt Options
	log *slog.Logger
	c   *githubapp.Client

	mu        sync.Mutex
	cur       *appState // nil: not loaded
	curAt     time.Time
	tokens    map[string]cachedUser // by PetalBook user id
	renewing  map[string]*renewal
	gens      map[string]uint64 // see tokenGen
	genAll    uint64
	attempts  *attemptStore
	manifests *attemptStore          // the attempts to make the App: apart from attempts (see manifestCapacity)
	noApp     map[string]time.Time   // GitHub logins the App is known not to be installed for, until (guarded by mu)
	rechecked map[string]time.Time   // PetalBook user ids, until when their next recheck is refused (guarded by mu)
	pending   map[string]*pendingApp // Apps GitHub made that could not be saved, by administrator (guarded by mu)
	onChange  func(userID string)
	autoAt    time.Time // when the App was last re-checked because a token was refused
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
	if opt.RenewEvery <= 0 {
		opt.RenewEvery = 24 * time.Hour
	}
	if opt.RenewWithin <= 0 {
		opt.RenewWithin = 30 * 24 * time.Hour
	}
	if opt.SettingsTTL <= 0 {
		opt.SettingsTTL = time.Minute
	}
	opt.PublicURL = strings.TrimRight(opt.PublicURL, "/")
	opt.Client.UseLogger(opt.Logger) // what GitHub refuses is told in the server's log
	return &Service{opt: opt, log: opt.Logger, c: opt.Client, tokens: map[string]cachedUser{}, renewing: map[string]*renewal{}, gens: map[string]uint64{},
		attempts: newAttemptStore(opt.Now, attemptCapacity), manifests: newAttemptStore(opt.Now, manifestCapacity),
		noApp: map[string]time.Time{}, rechecked: map[string]time.Time{}, pending: map[string]*pendingApp{}}, nil
}

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

const settingKey = "github_app"

// appRecord is what is stored of the App; the key and the secret are sealed.
type appRecord struct {
	AppID     string `json:"appId"`
	ClientID  string `json:"clientId,omitempty"`
	KeyEnc    string `json:"keyEnc"`
	SecretEnc string `json:"secretEnc,omitempty"`
	// WebhookSecretEnc is the secret GitHub gives with an App made from a
	// manifest; no webhook is used, so it is only kept, sealed.
	WebhookSecretEnc string            `json:"webhookSecretEnc,omitempty"`
	Fingerprint      string            `json:"fingerprint"`
	Slug             string            `json:"slug"`
	Name             string            `json:"name"`
	Owner            string            `json:"owner"`
	HTMLURL          string            `json:"htmlUrl"`
	Permissions      map[string]string `json:"permissions"`
	Checked          time.Time         `json:"checked"`
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

const (
	purposeKey    = "github-app-key"
	purposeSecret = "github-client-secret"
	purposeHook   = "github-webhook-secret"
	siteBinding   = "site"
)

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
	raw, ok, err := store.GetSiteSetting(ctx, s.opt.Store.Q(), settingKey)
	if err != nil {
		return nil, err
	}
	var st *appState
	if ok {
		if st, err = s.build(raw); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	// Keep the App client already in use when the saved one is the same (it
	// holds the token cache), but publish a new state: callers may still be
	// reading the one they took, so a published state is never written to.
	if st != nil && s.cur != nil && s.cur.rec.KeyEnc == st.rec.KeyEnc && s.cur.rec.AppID == st.rec.AppID {
		st.app, st.key = s.cur.app, s.cur.key
	}
	s.cur, s.curAt = st, s.opt.Now()
	s.mu.Unlock()
	return st, nil
}

func (s *Service) build(raw string) (*appState, error) {
	var rec appRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return nil, fmt.Errorf("the GitHub App settings are unreadable: %w", err)
	}
	pemb, err := s.opt.Secrets.Open(siteBinding, purposeKey, rec.KeyEnc)
	if err != nil {
		return nil, fmt.Errorf("the GitHub App key: %w", err)
	}
	key, err := githubapp.ParseKey(pemb)
	if err != nil {
		return nil, fmt.Errorf("the GitHub App key: %w", err)
	}
	app, err := s.c.NewApp(rec.AppID, key)
	if err != nil {
		return nil, err
	}
	st := &appState{rec: rec, app: app, key: key}
	if rec.SecretEnc != "" {
		b, err := s.opt.Secrets.Open(siteBinding, purposeSecret, rec.SecretEnc)
		if err != nil {
			return nil, fmt.Errorf("the GitHub client secret: %w", err)
		}
		st.secret = string(b)
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
	return s.opt.PublicURL + "/api/connections/github/authorization/callback"
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
			return &connections.Error{Provider: ProviderID, Code: CodeRateLimited, Status: http.StatusTooManyRequests, Msg: "GitHub is limiting this server; try again in a moment",
				RetryAfter: se.RetryAfter(s.opt.Now()), Err: err}
		case se.Status >= 500:
			return &connections.Error{Provider: ProviderID, Code: CodeUnavailable, Status: http.StatusServiceUnavailable, Msg: "GitHub cannot be reached right now", Err: err}
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
	return &connections.Error{Provider: ProviderID, Code: CodeUnavailable, Status: http.StatusServiceUnavailable, Msg: "GitHub cannot be reached right now", Err: err}
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
