package tokens

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/page"
	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
)

// Limits and times of docs/api.md.
const (
	MaxNameRunes = 80
	MaxSpaces    = 100
	MaxLive      = 50
	DefaultDays  = 90
	MaxDays      = 365
	KeepAfterEnd = 90 * 24 * time.Hour
	DefaultTTL   = 30 * time.Second
	touchEvery   = time.Hour
	maxCached    = 10_000
	maxNegative  = 4096
)

// Error is a refusal of a token request, with the status and code of the API.
type Error struct {
	Status     int
	Code       string
	Msg        string
	RetryAfter time.Duration // Status 429 only
}

func (e *Error) Error() string { return e.Msg }

func refuse(status int, code, msg string) *Error { return &Error{Status: status, Code: code, Msg: msg} }

// Invalid is a request to make a token that cannot be made.
type Invalid struct{ Msg string }

func (e *Invalid) Error() string { return e.Msg }

// ErrLimit: the owner already has MaxLive tokens that are neither revoked nor expired.
var ErrLimit = errors.New("too many tokens")

// Users is where the owner of a token is read from. It is the cache sessions
// use, so a token request costs no query for its owner.
type Users interface {
	CachedUser(ctx context.Context, id string) (*store.User, error)
}

// Config configures a Service.
type Config struct {
	Store *store.Store
	// Users is required by Authenticate; the command line makes tokens without it.
	Users Users
	// TTL is how long a token read from the database is trusted in memory
	// (DefaultTTL when 0): a revocation made by another process takes effect
	// within it.
	TTL     time.Duration
	Limiter *Limiter
	Logger  *slog.Logger
	Now     func() time.Time
}

// Service is safe for concurrent use.
type Service struct {
	cfg     Config
	st      *store.Store
	log     *slog.Logger
	now     func() time.Time
	limiter *Limiter

	mu    sync.Mutex
	gen   uint64 // bumped by every revocation, so a slow lookup cannot put back what was revoked
	byKey map[[32]byte]*entry
	byID  map[string]*entry
	neg   map[[32]byte]time.Time

	set *settings.Service
}

type entry struct {
	key       [32]byte
	id        string
	userID    string
	grant     *auth.Grant
	expires   time.Time // zero: never
	revoked   bool
	loaded    time.Time
	persisted time.Time // the last_used the database has
}

// New returns a Service.
func New(cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultTTL
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	lim := cfg.Limiter
	if lim == nil {
		lim = &Limiter{Now: cfg.Now}
	}
	s := &Service{cfg: cfg, st: cfg.Store, log: cfg.Logger, now: cfg.Now, limiter: lim,
		byKey: map[[32]byte]*entry{}, byID: map[string]*entry{}, neg: map[[32]byte]time.Time{}}
	if cfg.Store != nil {
		s.set = settings.Of(cfg.Store)
	}
	return s
}

// Now is the service's clock.
func (s *Service) Now() time.Time { return s.now() }

// Authenticate resolves the secret of a bearer token to the person it acts for,
// narrowed by a Grant. ip is the client's address, for the limit on failures.
// The errors are *Error (and a database error as itself).
//
// A token in memory costs no query; the owner comes from the user cache, and
// what the owner may do in a space is the caller's to decide from the grant.
func (s *Service) Authenticate(ctx context.Context, secret, ip string) (*auth.Principal, error) {
	if blocked, wait := s.limiter.Blocked(ip); blocked {
		return nil, &Error{Status: http.StatusTooManyRequests, Code: "rate_limited", Msg: "Too many failed tokens from this address; wait before trying again.",
			RetryAfter: time.Duration(wait) * time.Second}
	}
	if !WellFormed(secret) {
		s.limiter.Fail(ip)
		return nil, refuse(http.StatusUnauthorized, "token_invalid", "This is not a token this server issued.")
	}
	now := s.now()
	e, err := s.lookup(ctx, sha256.Sum256([]byte(secret)), now)
	if err != nil {
		return nil, err
	}
	switch {
	case e == nil:
		s.limiter.Fail(ip)
		return nil, refuse(http.StatusUnauthorized, "token_invalid", "This is not a token this server issued.")
	case e.revoked:
		s.limiter.Fail(ip)
		return nil, refuse(http.StatusUnauthorized, "token_revoked", "This token was revoked.")
	case !e.expires.IsZero() && !now.Before(e.expires):
		s.limiter.Fail(ip)
		return nil, refuse(http.StatusUnauthorized, "token_expired", "This token has expired.")
	}
	u, err := s.cfg.Users.CachedUser(ctx, e.userID)
	if errors.Is(err, store.ErrNotFound) {
		s.limiter.Fail(ip)
		return nil, refuse(http.StatusUnauthorized, "token_invalid", "This is not a token this server issued.")
	}
	if err != nil {
		return nil, err
	}
	if u.Disabled || s.st != nil && s.st.TenantDisabled(u.TenantID) { // a disabled tenant is a disabled account
		return nil, refuse(http.StatusUnauthorized, "token_owner_disabled", "The account this token belongs to is disabled.")
	}
	if on, err := s.TokensOn(ctx, u.TenantID); err != nil {
		return nil, err
	} else if !on {
		return nil, refuse(http.StatusForbidden, "tokens_off", "An administrator has switched API tokens off.")
	}
	s.touch(ctx, e.key, now)
	if s.st != nil {
		if err := s.st.NoteActive(ctx, u.ID, now); err != nil {
			s.log.Warn("could not record a person's last use", "error", err)
		}
	}
	// No Access: no route outside spaces takes a token, so none is built.
	return &auth.Principal{User: *u, Grant: e.grant}, nil
}

// lookup finds the entry of a token hash in memory, else reads it once. nil
// means no such token. The entry returned must not be changed.
func (s *Service) lookup(ctx context.Context, key [32]byte, now time.Time) (*entry, error) {
	s.mu.Lock()
	if until, bad := s.neg[key]; bad {
		if now.Before(until) {
			s.mu.Unlock()
			return nil, nil
		}
		delete(s.neg, key)
	}
	if e := s.byKey[key]; e != nil {
		if now.Sub(e.loaded) <= s.cfg.TTL {
			snap := *e
			s.mu.Unlock()
			return &snap, nil
		}
		s.dropLocked(e)
	}
	gen := s.gen
	s.mu.Unlock()

	row, err := store.GetAPITokenByHash(ctx, s.st.Q(), hexOf(key))
	if errors.Is(err, store.ErrNotFound) {
		s.mu.Lock()
		if len(s.neg) >= maxNegative {
			for k := range s.neg {
				delete(s.neg, k)
				break
			}
		}
		s.neg[key] = now.Add(DefaultTTL)
		s.mu.Unlock()
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(row.Spaces))
	for i, sp := range row.Spaces {
		ids[i] = sp.ID
	}
	e := &entry{key: key, id: row.ID, userID: row.UserID, grant: auth.NewGrant("token", row.ID, row.Name, row.Access == Write, ids),
		expires: row.Expires, revoked: !row.Revoked.IsZero(), loaded: now, persisted: row.LastUsed}
	s.mu.Lock()
	if s.gen == gen { // nothing was revoked while we read
		if len(s.byKey) >= maxCached {
			for _, old := range s.byKey {
				s.dropLocked(old)
				break
			}
		}
		s.byKey[key] = e
		s.byID[e.id] = e
	}
	snap := *e
	s.mu.Unlock()
	return &snap, nil
}

func (s *Service) dropLocked(e *entry) {
	delete(s.byKey, e.key)
	delete(s.byID, e.id)
}

func hexOf(key [32]byte) string {
	const digits = "0123456789abcdef"
	b := make([]byte, 64)
	for i, c := range key {
		b[2*i], b[2*i+1] = digits[c>>4], digits[c&15]
	}
	return string(b)
}

// touch writes when the token was last used, at most once an hour.
func (s *Service) touch(ctx context.Context, key [32]byte, now time.Time) {
	s.mu.Lock()
	e := s.byKey[key]
	if e == nil || now.Sub(e.persisted) < touchEvery {
		s.mu.Unlock()
		return
	}
	e.persisted = now
	id := e.id
	s.mu.Unlock()
	if err := store.TouchAPIToken(ctx, s.st.Q(), id, now); err != nil {
		s.log.Warn("could not record the use of a token", "error", err)
	}
}

// --- making, listing, revoking ----------------------------------------------

// CreateParams describes a new token.
type CreateParams struct {
	UserID string
	Name   string
	Access string   // "" is read
	Spaces []string // ids
	// ExpiresIn is how long the token lasts; 0 is forever.
	ExpiresIn time.Duration
}

// CleanName trims a token's name and checks it: 1 to MaxNameRunes characters,
// no control characters.
func CleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	n := utf8.RuneCountInString(name)
	if n < 1 || n > MaxNameRunes || !utf8.ValidString(name) {
		return "", &Invalid{fmt.Sprintf("the name must be 1 to %d characters", MaxNameRunes)}
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", &Invalid{"the name must not contain control characters"}
		}
	}
	return name, nil
}

// Create makes a token for a user and returns it with its secret, which is
// not kept anywhere and cannot be shown again. The caller has checked that
// the spaces are ones the user may name. Invalid, ErrLimit.
func (s *Service) Create(ctx context.Context, p CreateParams) (store.APIToken, string, error) {
	name, err := CleanName(p.Name)
	if err != nil {
		return store.APIToken{}, "", err
	}
	access := p.Access
	if access == "" {
		access = Read
	}
	if access != Read && access != Write {
		return store.APIToken{}, "", &Invalid{"access must be read or write"}
	}
	seen := map[string]bool{}
	var spaces []store.TokenSpace
	for _, id := range p.Spaces {
		if id == "" || len(id) > 64 {
			return store.APIToken{}, "", &Invalid{"a space id is not valid"}
		}
		if !seen[id] {
			seen[id] = true
			spaces = append(spaces, store.TokenSpace{ID: id})
		}
	}
	if len(spaces) < 1 || len(spaces) > MaxSpaces {
		return store.APIToken{}, "", &Invalid{fmt.Sprintf("name 1 to %d spaces", MaxSpaces)}
	}
	sort.Slice(spaces, func(i, j int) bool { return spaces[i].ID < spaces[j].ID })
	secret, err := Generate()
	if err != nil {
		return store.APIToken{}, "", err
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	t := store.APIToken{ID: page.NewID(now), UserID: p.UserID, Name: name, Prefix: ShownPrefix(secret), Hash: HashSecret(secret),
		Access: access, Created: now, Spaces: spaces}
	if p.ExpiresIn > 0 {
		t.Expires = now.Add(p.ExpiresIn)
	}
	err = s.st.Tx(ctx, func(q store.Q) error {
		n, err := store.CountLiveAPITokens(ctx, q, p.UserID, now)
		if err != nil {
			return err
		}
		if n >= MaxLive {
			return ErrLimit
		}
		return store.InsertAPIToken(ctx, q, t)
	})
	if err != nil {
		return store.APIToken{}, "", err
	}
	return t, secret, nil
}

// List returns tokens as store.ListAPITokens does.
func (s *Service) List(ctx context.Context, sc store.Scope, f store.TokenFilter) ([]store.TokenRow, error) {
	return store.ListAPITokens(ctx, s.st.Q(), sc, f)
}

// Revoke revokes a token and reports whether it exists. In this process it
// takes effect at once; another process notices within the cache's time to live.
func (s *Service) Revoke(ctx context.Context, id string) (bool, error) {
	ok, err := store.RevokeAPIToken(ctx, s.st.Q(), id, s.now())
	if err != nil || !ok {
		return ok, err
	}
	s.mu.Lock()
	s.gen++
	if e := s.byID[id]; e != nil {
		e.revoked = true
	}
	s.mu.Unlock()
	return true, nil
}

// Sweep removes tokens that ended more than KeepAfterEnd ago.
func (s *Service) Sweep(ctx context.Context) {
	n, err := store.DeleteOldAPITokens(ctx, s.st.Q(), s.now().Add(-KeepAfterEnd))
	if err != nil {
		s.log.Warn("could not delete old tokens", "error", err)
	} else if n > 0 {
		s.log.Info("deleted old tokens", "count", n)
	}
}

// Run keeps the table tidy until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		s.Sweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// --- what an administrator switched on --------------------------------------

// TokensOn says whether people of a tenant may use API tokens: the setting
// features.tokens in effect for the tenant ("Machine access" in docs/api.md).
// It is one read of the settings' cache, so a request costs no query. Tenant ""
// is the site's own value.
func (s *Service) TokensOn(ctx context.Context, tenant string) (bool, error) {
	return s.featureOn(ctx, tenant, settings.FeatureTokens)
}

// MCPOn says whether MCP, and with it the authorization server, is on for a
// tenant (features.mcp).
func (s *Service) MCPOn(ctx context.Context, tenant string) (bool, error) {
	return s.featureOn(ctx, tenant, settings.FeatureMCP)
}

// MCPAnywhere says whether MCP can be on for anybody: the site's features.mcp
// is on, or off without being forced, so that a tenant may switch it on for
// itself. While it is false the authorization server is not there at all.
func (s *Service) MCPAnywhere(ctx context.Context) (bool, error) {
	if s.set == nil {
		return true, nil
	}
	e, err := s.set.Get(ctx, "", settings.FeatureMCP)
	if err != nil {
		return false, err
	}
	on, _ := e.Value.(bool)
	return on || e.Site.Lower.Mode == settings.Allow, nil
}

func (s *Service) featureOn(ctx context.Context, tenant, key string) (bool, error) {
	if s.set == nil {
		return true, nil
	}
	e, err := s.set.Get(ctx, tenant, key)
	if err != nil {
		return false, err
	}
	on, _ := e.Value.(bool)
	return on, nil
}
