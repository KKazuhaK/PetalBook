package oauth

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/KKazuhaK/PetalBook/internal/auth"
	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/tokens"
)

// grantEntry is a grant as kept in memory: what a request needs to know of it.
type grantEntry struct {
	id       string
	userID   string
	name     string // the client's
	write    bool
	spaces   []string
	lastUsed time.Time
	loaded   time.Time
	grant    *auth.Grant
}

// verifiedToken is an access token whose signature and claims were checked,
// remembered by its hash until it expires.
type verifiedToken struct {
	gid     string
	write   bool // the scope claim has write
	expires time.Time
}

func refuse(status int, code, msg string) *tokens.Error {
	return &tokens.Error{Status: status, Code: code, Msg: msg}
}

// LooksLikeAccessToken says a bearer secret has the shape of an access token
// (a JWT, which an API token never is), so a route that is not for it can say
// so instead of calling it malformed.
func LooksLikeAccessToken(raw string) bool {
	return !strings.HasPrefix(raw, tokens.Prefix) && strings.Count(raw, ".") == 2
}

// Authenticate resolves an OAuth access token to the person it acts for,
// narrowed by a Grant exactly as an API token's is: what the owner may do now,
// within the grant's spaces and access. ip is the client's address, for the
// limit on refused tokens. The errors are *tokens.Error, and a database error
// as itself.
//
// A token already checked, whose grant is in memory, costs no query and no
// signature check; a revocation made through this server takes effect at once,
// one made by another process within the cache's time to live.
func (s *Service) Authenticate(ctx context.Context, raw, ip string) (*auth.Principal, error) {
	if blocked, wait := s.failLimit.Blocked(ip); blocked {
		return nil, &tokens.Error{Status: http.StatusTooManyRequests, Code: "rate_limited", Msg: "Too many failed tokens from this address; wait before trying again.",
			RetryAfter: time.Duration(wait) * time.Second}
	}
	on, err := s.On(ctx)
	if err != nil {
		return nil, err
	}
	if !on {
		return nil, refuse(http.StatusNotFound, "not_found", "No such endpoint.")
	}
	now := s.now()
	v, bad := s.verify(raw, now)
	if bad != nil {
		s.failLimit.Fail(ip)
		return nil, bad
	}
	e, err := s.lookupGrant(ctx, v.gid, now)
	if err != nil {
		return nil, err
	}
	if e == nil {
		s.failLimit.Fail(ip)
		return nil, refuse(http.StatusUnauthorized, "token_revoked", "This authorization was revoked.")
	}
	u, err := s.cfg.Users.CachedUser(ctx, e.userID)
	if errors.Is(err, store.ErrNotFound) {
		s.failLimit.Fail(ip)
		return nil, refuse(http.StatusUnauthorized, "token_invalid", "This is not a token this server issued.")
	}
	if err != nil {
		return nil, err
	}
	if u.Disabled || s.cfg.Store != nil && s.cfg.Store.TenantDisabled(u.TenantID) { // a disabled tenant is a disabled account
		return nil, refuse(http.StatusUnauthorized, "token_owner_disabled", "The account this authorization belongs to is disabled.")
	}
	g := e.grant
	if e.write && !v.write { // a refresh asked for less than the grant
		g = auth.NewGrant(GrantKind, e.id, e.name, false, e.spaces)
	}
	if s.st != nil {
		if err := s.st.NoteActive(ctx, u.ID, now); err != nil {
			s.log.Warn("could not record a person's last use", "error", err)
		}
	}
	return &auth.Principal{User: *u, Grant: g}, nil
}

// verify checks an access token in process. Whatever is wrong with it is one of
// the refusals of an API token.
func (s *Service) verify(raw string, now time.Time) (verifiedToken, *tokens.Error) {
	invalid := refuse(http.StatusUnauthorized, "token_invalid", "This is not a token this server issued.")
	if len(raw) > 4096 {
		return verifiedToken{}, invalid
	}
	key := sha256.Sum256([]byte(raw))
	s.gmu.Lock()
	v, ok := s.verified[key]
	if ok && !now.Before(v.expires) {
		delete(s.verified, key)
		ok = false
	}
	s.gmu.Unlock()
	if ok {
		return v, nil
	}
	tok, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil || len(tok.Headers) != 1 {
		return verifiedToken{}, invalid
	}
	h := tok.Headers[0]
	if typ, _ := h.ExtraHeaders[jose.HeaderType].(string); !strings.EqualFold(typ, "at+jwt") && !strings.EqualFold(typ, "application/at+jwt") {
		return verifiedToken{}, invalid
	}
	if h.KeyID != s.key.kid {
		return verifiedToken{}, invalid
	}
	var c accessClaims
	if err := tok.Claims(&s.key.priv.PublicKey, &c); err != nil {
		return verifiedToken{}, invalid
	}
	if c.Issuer != s.issuer || !c.Audience.Contains(s.resource) || c.Subject == "" || c.GrantID == "" || c.Expiry == nil {
		return verifiedToken{}, invalid
	}
	exp := c.Expiry.Time()
	if !now.Before(exp) {
		return verifiedToken{}, refuse(http.StatusUnauthorized, "token_expired", "This access token has expired.")
	}
	if (c.NotBefore != nil && now.Before(c.NotBefore.Time())) || (c.IssuedAt != nil && now.Before(c.IssuedAt.Time())) {
		return verifiedToken{}, invalid
	}
	v = verifiedToken{gid: c.GrantID, write: contains(strings.Fields(c.Scope), scopeWrite), expires: exp}
	s.gmu.Lock()
	if len(s.verified) >= maxVerified {
		for k, o := range s.verified {
			if !now.Before(o.expires) {
				delete(s.verified, k)
			}
		}
		for k := range s.verified {
			if len(s.verified) < maxVerified {
				break
			}
			delete(s.verified, k)
		}
	}
	s.verified[key] = v
	s.gmu.Unlock()
	return v, nil
}

// lookupGrant finds a grant in memory, else reads it once. nil means it is
// gone: revoked, or not refreshed for too long. The entry must not be changed.
func (s *Service) lookupGrant(ctx context.Context, gid string, now time.Time) (*grantEntry, error) {
	s.gmu.Lock()
	if until, bad := s.gone[gid]; bad {
		if now.Before(until) {
			s.gmu.Unlock()
			return nil, nil
		}
		delete(s.gone, gid)
	}
	if e := s.grants[gid]; e != nil {
		if now.Sub(e.loaded) <= cacheTTL {
			s.gmu.Unlock()
			return s.live(e, now), nil
		}
		delete(s.grants, gid)
	}
	gen := s.gen
	s.gmu.Unlock()

	g, err := store.GetOAuthGrant(ctx, s.st.Q(), gid, false)
	if errors.Is(err, store.ErrNotFound) {
		s.gmu.Lock()
		s.markGoneLocked(gid, now)
		s.gmu.Unlock()
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(g.Spaces))
	for i, sp := range g.Spaces {
		ids[i] = sp.ID
	}
	e := &grantEntry{id: g.ID, userID: g.UserID, name: g.ClientName, write: g.Access == scopeWrite, spaces: ids, lastUsed: g.LastUsed, loaded: now,
		grant: auth.NewGrant(GrantKind, g.ID, g.ClientName, g.Access == scopeWrite, ids)}
	s.gmu.Lock()
	if s.gen == gen { // nothing was revoked while we read
		if len(s.grants) >= maxCached {
			for k := range s.grants {
				delete(s.grants, k)
				break
			}
		}
		s.grants[gid] = e
	}
	s.gmu.Unlock()
	return s.live(e, now), nil
}

// live is the entry unless the grant went idle past its expiry.
func (s *Service) live(e *grantEntry, now time.Time) *grantEntry {
	if now.Sub(e.lastUsed) > IdleExpiry {
		return nil
	}
	return e
}

func (s *Service) markGoneLocked(gid string, now time.Time) {
	if len(s.gone) >= 4096 {
		for k := range s.gone {
			delete(s.gone, k)
			break
		}
	}
	s.gone[gid] = now.Add(cacheTTL)
}

// --- listing and revoking ----------------------------------------------------

// List returns grants as store.ListOAuthGrants does.
func (s *Service) List(ctx context.Context, sc store.Scope, f store.GrantFilter) ([]store.OAuthGrantRow, error) {
	return store.ListOAuthGrants(ctx, s.st.Q(), sc, f)
}

// Revoke ends a grant and reports whether it existed. Its refresh token stops
// working now; its access tokens stop at once in this process, and within the
// cache's time to live in another.
func (s *Service) Revoke(ctx context.Context, gid, why string) (bool, error) {
	found, err := store.DeleteOAuthGrant(ctx, s.st.Q(), gid)
	if err != nil {
		s.log.Error("could not revoke an oauth grant", "grant", gid, "error", err)
		return false, err
	}
	s.gmu.Lock()
	s.gen++
	delete(s.grants, gid)
	s.markGoneLocked(gid, s.now())
	s.gmu.Unlock()
	s.dropReplay(gid)
	if found {
		s.log.Info("oauth grant revoked", "grant", gid, "why", why)
	}
	return found, nil
}
