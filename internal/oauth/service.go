// Package oauth is PetalBook's authorization server for MCP clients ("OAuth for
// MCP clients" in docs/api.md): OAuth 2.1 with PKCE (S256 only), resource
// indicators (RFC 8707), the iss parameter (RFC 9207), client ID metadata
// documents with dynamic registration (RFC 7591) as the fallback, and refresh
// tokens that rotate. Clients are public. It is adapted from Rackbook's server,
// with the clients, grants and the signing key in the state database.
//
// An access token is a short ES256 JWT that only this process checks. A check
// costs no query: the signature is verified once and remembered by the hash of
// the token, and the grant it names is read from a small cache, as API tokens
// are.
package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/secret"
	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/tokens"
)

// Endpoint paths.
const (
	PathMetadata    = "/.well-known/oauth-authorization-server"
	PathResource    = "/.well-known/oauth-protected-resource"
	PathResourceMCP = "/.well-known/oauth-protected-resource/mcp"
	PathAuthorize   = "/oauth/authorize"
	PathToken       = "/oauth/token"
	PathRegister    = "/oauth/register"
	PathConsent     = "/oauth/consent" // a page of the web app
	ResourcePath    = "/mcp"
	GrantKind       = "oauth" // the Kind of an auth.Grant made from an access token

)

const (
	scopeRead  = "read"
	scopeWrite = "write"
)

// Times and limits of docs/api.md.
const (
	AccessTTL    = 10 * time.Minute
	RefreshGrace = 60 * time.Second
	IdleExpiry   = 90 * 24 * time.Hour
	codeTTL      = 60 * time.Second
	pendingTTL   = 10 * time.Minute
	maxPending   = 1000 // pending authorization requests in all
	// maxPendingPerUser is the pending requests of one person, so that one
	// signed-in account cannot fill the table for everyone.
	maxPendingPerUser = 20
	maxRetired        = 8
	// cacheTTL is how long a grant read from the database is trusted in memory.
	cacheTTL    = tokens.DefaultTTL
	maxCached   = 10_000
	maxVerified = 10_000
)

// Config configures a Service.
type Config struct {
	Store   *store.Store
	Secrets *secret.Box
	// Users is where the owner of a grant is read from (the cache of sessions).
	Users tokens.Users
	// Machine says whether MCP, and with it the authorization server, is on.
	Machine *tokens.Service
	// Issuer is the public origin of the server; tokens are bound to
	// Issuer+ResourcePath.
	Issuer string
	// ClientIP names the address of a request, for the limit on registrations
	// and on failed tokens.
	ClientIP func(*http.Request) string
	Logger   *slog.Logger
	Now      func() time.Time
	// CIMDClient fetches client metadata documents; nil is a client that
	// refuses private addresses. Tests replace it.
	CIMDClient *http.Client
}

// Service is safe for concurrent use.
type Service struct {
	cfg      Config
	st       *store.Store
	log      *slog.Logger
	now      func() time.Time
	issuer   string
	resource string
	key      *signingKey
	cimd     *cimdFetcher

	regLimit  *tokens.Limiter // new registrations per client address
	failLimit *tokens.Limiter // refused access tokens per client address

	mu        sync.Mutex
	pending   map[string]*pendingAuth // consent requests by id
	pendingOf map[string]int          // the number of them per person
	codes     map[string]*authCode    // by SHA-256 of the code
	used      map[string]usedCode     // codes exchanged, to end the grant on a replay

	refreshMu sync.Mutex // one refresh at a time
	replayMu  sync.Mutex
	replay    map[string]replayEntry // answers to recent refreshes, by hash of the replaced token

	gmu      sync.Mutex
	gen      uint64 // bumped by every revocation, so a slow read cannot put back what was revoked
	grants   map[string]*grantEntry
	gone     map[string]time.Time // grants known to be revoked, for a while
	verified map[[32]byte]verifiedToken
}

// New opens the service: it loads the signing key, making one at the first
// start. Issuer must be an origin (scheme and host).
func New(ctx context.Context, cfg Config) (*Service, error) {
	if cfg.Store == nil || cfg.Secrets == nil || cfg.Users == nil || cfg.Machine == nil {
		return nil, errors.New("oauth: a store, a secret box, users and the machine access setting are required")
	}
	cfg.Issuer = strings.TrimRight(cfg.Issuer, "/")
	if cfg.Issuer == "" {
		return nil, errors.New("oauth: the authorization server needs the public URL")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.ClientIP == nil {
		cfg.ClientIP = func(r *http.Request) string { return r.RemoteAddr }
	}
	key, err := loadKey(ctx, cfg.Store, cfg.Secrets, cfg.Now())
	if err != nil {
		return nil, err
	}
	s := &Service{cfg: cfg, st: cfg.Store, log: cfg.Logger, now: cfg.Now, issuer: cfg.Issuer, resource: cfg.Issuer + ResourcePath, key: key,
		pending: map[string]*pendingAuth{}, pendingOf: map[string]int{}, codes: map[string]*authCode{}, used: map[string]usedCode{}, replay: map[string]replayEntry{},
		grants: map[string]*grantEntry{}, gone: map[string]time.Time{}, verified: map[[32]byte]verifiedToken{}}
	s.cimd = newCIMDFetcher(cfg.CIMDClient, s.now, s.log)
	s.regLimit = &tokens.Limiter{Max: registerPerWindow, Window: registerWindow, Now: s.now}
	s.failLimit = &tokens.Limiter{Now: s.now}
	return s, nil
}

// Issuer is the issuer identifier ("iss") of the server's tokens.
func (s *Service) Issuer() string { return s.issuer }

// Resource is the URL of /mcp, which access tokens are bound to.
func (s *Service) Resource() string { return s.resource }

// Challenge is the WWW-Authenticate value of a request to /mcp without a token.
func (s *Service) Challenge() string {
	return `Bearer resource_metadata="` + s.issuer + PathResourceMCP + `", scope="read write"`
}

// On says whether the authorization server answers: an administrator may
// switch MCP, and with it OAuth, off.
func (s *Service) On(ctx context.Context) (bool, error) {
	a, err := s.cfg.Machine.Access(ctx)
	return a.MCP, err
}

// Run keeps the tables tidy until ctx ends: grants nobody refreshed for 90 days
// and clients nobody uses.
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

// Sweep does one round of Run.
func (s *Service) Sweep(ctx context.Context) {
	now := s.now()
	if n, err := store.DeleteIdleOAuthGrants(ctx, s.st.Q(), now.Add(-IdleExpiry)); err != nil {
		s.log.Warn("could not delete idle oauth grants", "error", err)
	} else if n > 0 {
		s.log.Info("deleted idle oauth grants", "count", n)
	}
	if _, err := store.PruneOAuthClients(ctx, s.st.Q(), now.Add(-unusedClientTTL), now.Add(-idleClientTTL)); err != nil {
		s.log.Warn("could not delete unused oauth clients", "error", err)
	}
}

// randomToken is n random bytes in base64url.
func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func equalStrings(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Scopes the server knows.
func scopesSupported() []string { return []string{scopeRead, scopeWrite} }

// RefreshPrefix starts every refresh token.
const RefreshPrefix = "pbr_"

// refresh tokens are "pbr_<grant id>.<secret>": the id finds the grant, the
// secret is checked against its stored hash.
func splitRefresh(t string) (gid, secret string, ok bool) {
	t, ok = strings.CutPrefix(t, RefreshPrefix)
	if !ok {
		return "", "", false
	}
	gid, secret, ok = strings.Cut(t, ".")
	return gid, secret, ok && gid != "" && secret != ""
}
