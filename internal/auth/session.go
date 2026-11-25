package auth

import (
	"context"
	"errors"
	"strings"

	"github.com/KKazuhaK/PetalBook/internal/store"
)

// Authenticate resolves a session cookie value to its user. It answers from
// memory: the session and the user are cached, and the database is asked again
// only for a session not seen yet, after the cache's time to live, and once an
// hour to record that the session is in use. Expired, revoked and unknown
// sessions, and disabled users, are all Unauthenticated.
func (s *Service) Authenticate(ctx context.Context, token string) (*Principal, error) {
	if token == "" || len(token) > 256 {
		return nil, errf(Unauthenticated, "Sign in to continue.")
	}
	id := hashToken(token)
	now := s.now()
	ttl := s.cfg.CacheTTL

	s.cmu.Lock()
	gen := s.gen // an eviction while we work must not be undone by our insert
	if neg, bad := s.negative[id]; bad {
		if now.Before(neg.until) {
			s.cmu.Unlock()
			return nil, ended(neg.reason, "Sign in to continue.")
		}
		delete(s.negative, id)
	}
	cs := s.sessions[id]
	if cs != nil && now.Sub(cs.loaded) > ttl {
		delete(s.sessions, id)
		cs = nil
	}
	var snap cachedSession
	if cs != nil {
		snap = *cs
	}
	s.cmu.Unlock()

	if cs == nil {
		// The session, or why it ended if it was one: one query by the hash, and a
		// refusal is then remembered with its reason.
		row, reason, err := store.LookupSession(ctx, s.st.Q(), id)
		if errors.Is(err, store.ErrNotFound) {
			s.cmu.Lock()
			if len(s.negative) >= maxNegative {
				for k := range s.negative {
					delete(s.negative, k)
					break
				}
			}
			s.negative[id] = negative{until: now.Add(negativeTTL), reason: reason}
			s.cmu.Unlock()
			return nil, ended(reason, "Sign in to continue.")
		}
		if err != nil {
			return nil, err
		}
		snap = cachedSession{userID: row.UserID, created: row.Created, lastUsed: row.LastUsed, persisted: row.LastUsed, loaded: now, held: row.Held}
	}
	sec := s.sec(ctx)
	if now.Sub(snap.lastUsed) > sec.idle || now.Sub(snap.created) > sec.max {
		s.endSession(ctx, id, snap.userID, store.EndExpired)
		return nil, ended(store.EndExpired, "Your session has ended; sign in again.")
	}
	u, err := s.cachedUser(ctx, snap.userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.endSession(ctx, id, "", "")
			return nil, errf(Unauthenticated, "Sign in to continue.")
		}
		return nil, err
	}
	// A disabled tenant is a disabled account: one atomic load, and a map lookup
	// only while some tenant is disabled.
	if u.Disabled || s.st.TenantDisabled(u.TenantID) {
		s.endSession(ctx, id, u.ID, store.EndDisabled)
		return nil, ended(store.EndDisabled, "Sign in to continue.")
	}

	touch := false
	s.cmu.Lock()
	cur := s.sessions[id]
	if cur == nil && s.gen != gen {
		// A logout, password change or disabling happened since we looked:
		// answer this request, but do not put the session back in the cache.
		c := snap
		cur = &c
	} else if cur == nil {
		if len(s.sessions) >= maxCachedSessions {
			for k := range s.sessions {
				delete(s.sessions, k)
				break
			}
		}
		c := snap
		cur = &c
		s.sessions[id] = cur
	}
	cur.lastUsed = now
	if now.Sub(cur.persisted) >= touchEvery {
		cur.persisted = now
		touch = true
	}
	s.cmu.Unlock()
	if touch {
		if err := store.TouchSession(ctx, s.st.Q(), id, now); err != nil {
			s.log.Warn("could not record session use", "error", err)
		}
	}
	// The person's last use: a lookup in memory unless an hour has passed.
	if err := s.st.NoteActive(ctx, u.ID, now); err != nil {
		s.log.Warn("could not record a person's last use", "error", err)
	}
	return &Principal{User: *u, SessionID: id, SessionCreated: snap.created, Held: snap.held}, nil
}

// releaseHold forgets, in the cache, that a session was held.
func (s *Service) releaseHold(id string) {
	s.cmu.Lock()
	if cs := s.sessions[id]; cs != nil {
		cs.held = false
	}
	s.cmu.Unlock()
}

// CachedUser is the user of an id, from the cache that sessions use: no query
// until the entry is older than the cache time to live or something about the
// user changed. store.ErrNotFound when there is no such user.
func (s *Service) CachedUser(ctx context.Context, id string) (*store.User, error) {
	return s.cachedUser(ctx, id)
}

func (s *Service) cachedUser(ctx context.Context, id string) (*store.User, error) {
	now := s.now()
	s.cmu.Lock()
	if c := s.users[id]; c != nil && now.Sub(c.loaded) <= s.cfg.CacheTTL {
		u := c.u
		s.cmu.Unlock()
		return &u, nil
	}
	gen := s.gen
	s.cmu.Unlock()
	u, err := store.GetUser(ctx, s.st.Q(), store.AllTenants(), id) // by session or token: the account decides the tenant
	if err != nil {
		return nil, err
	}
	s.cmu.Lock()
	// If the user changed while we read, what we read may be the old row:
	// use it for this request only.
	if s.gen == gen {
		if len(s.users) >= maxCachedSessions {
			for k := range s.users {
				delete(s.users, k)
				break
			}
		}
		s.users[id] = &cachedUser{u: *u, loaded: now}
	}
	s.cmu.Unlock()
	return u, nil
}

// ended is the refusal of a cookie whose session is gone, with why when it is known.
func ended(reason, msg string) *Error {
	e := errf(Unauthenticated, "%s.", msg)
	e.Reason = reason
	return e
}

// endSession removes one session from the database and the cache. With a
// reason, a record of why is kept for the user (a sign-out on purpose has none).
func (s *Service) endSession(ctx context.Context, id, userID, reason string) {
	s.cmu.Lock()
	s.gen++
	delete(s.sessions, id)
	s.cmu.Unlock()
	var err error
	if reason == "" {
		err = store.DeleteSession(ctx, s.st.Q(), id)
	} else {
		err = store.EndSession(ctx, s.st.Q(), id, userID, reason, s.now())
	}
	if err != nil {
		s.log.Warn("could not delete a session", "error", err)
	}
}

// evictUser forgets the cached user and every cached session of the user
// except keep. A scan, but the cache is bounded and this is rare (password
// change, disabling).
func (s *Service) evictUser(userID, keep string) {
	s.cmu.Lock()
	defer s.cmu.Unlock()
	s.gen++
	delete(s.users, userID)
	for id, cs := range s.sessions {
		if cs.userID == userID && id != keep {
			delete(s.sessions, id)
		}
	}
}

// Forget drops what the service remembers of a person (their sessions and the
// copy of their row): for a change made behind its back, such as a person who was
// deleted or brought back.
func (s *Service) Forget(userID string) { s.evictUser(userID, "") }

// forgetRefusals empties the cache of refused cookies, so that a reason that is
// no longer true (a person enabled again) is not given for its 30 seconds.
func (s *Service) forgetRefusals() {
	s.cmu.Lock()
	clear(s.negative)
	s.cmu.Unlock()
}

// TenantAccessChanged follows a tenant being disabled or enabled again. Disabled:
// every session of its people ends now, recorded as `disabled`, so a cookie that
// is idle until long after says so rather than `expired`. Enabled: those records
// are forgotten. Either way the caches are emptied (a rare administrator action).
// Safe to repeat.
func (s *Service) TenantAccessChanged(ctx context.Context, tenantID string, disabled bool) error {
	var err error
	if disabled {
		err = store.EndTenantSessions(ctx, s.st.Q(), tenantID, store.EndDisabled, s.now())
	} else {
		err = store.DeleteTenantSessionEnds(ctx, s.st.Q(), tenantID, store.EndDisabled)
	}
	s.cmu.Lock()
	s.gen++
	clear(s.sessions)
	clear(s.users)
	clear(s.negative)
	s.cmu.Unlock()
	return err
}

func (s *Service) dropUser(userID string) {
	s.cmu.Lock()
	s.gen++
	delete(s.users, userID)
	s.cmu.Unlock()
}

// Logout ends the session of a cookie value, if it names one.
func (s *Service) Logout(ctx context.Context, token string) {
	if token == "" || len(token) > 256 {
		return
	}
	s.endSession(ctx, hashToken(token), "", "")
}

// SignOutEverywhere ends every session of the user but keep ("" keeps none),
// for the person signing out of the other devices, or an administrator
// revoking them. Each one answers 401 with `signed_out_elsewhere` afterwards.
func (s *Service) SignOutEverywhere(ctx context.Context, userID, keep string) error {
	if err := store.DeleteUserSessions(ctx, s.st.Q(), userID, keep, store.EndElsewhere, s.now()); err != nil {
		return err
	}
	s.evictUser(userID, keep)
	return nil
}

type userAgentKey struct{}

// WithUserAgent says, for the sessions that start under ctx, the browser's name
// for itself (the administrator's list of a person's sessions shows it).
func WithUserAgent(ctx context.Context, ua string) context.Context {
	return context.WithValue(ctx, userAgentKey{}, ua)
}

// issueSession starts a session for a user and returns the cookie value. method
// is how they signed in (see the sessions table).
func (s *Service) issueSession(ctx context.Context, userID string, held bool, method string) (string, error) {
	token := randomToken()
	now := s.now().UTC()
	ua, _ := ctx.Value(userAgentKey{}).(string)
	err := s.st.Tx(ctx, func(q store.Q) error {
		if err := store.InsertSession(ctx, q, store.Session{ID: hashToken(token), UserID: userID, Created: now, LastUsed: now, Held: held, UserAgent: ua, Method: method}); err != nil {
			return err
		}
		return store.TrimSessions(ctx, q, userID, maxSessionsPerUser)
	})
	return token, err
}

// userKey is the username as sign-in reads it: trimmed and folded, and cut to
// a bounded length, so " Kazuha" and "kazuha" are one username to the limiter.
func userKey(username string) string {
	u := []byte(strings.TrimSpace(username))
	if len(u) > 64 {
		u = u[:64]
	}
	for i, c := range u {
		if c >= 'A' && c <= 'Z' {
			u[i] = c + 'a' - 'A'
		}
	}
	return "u:" + string(u)
}
