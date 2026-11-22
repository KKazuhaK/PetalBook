package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
)

// Authenticate resolves a session cookie value to its user. It answers from
// memory: the session and the user are cached, and the database is asked again
// only for a session not seen yet, after the cache's time to live, and once an
// hour to record that the session is in use. Expired, revoked and unknown
// sessions, and disabled users, are all Unauthenticated.
func (s *Service) Authenticate(ctx context.Context, token string) (*Principal, error) {
	if token == "" || len(token) > 256 {
		return nil, errf(Unauthenticated, "sign in to continue")
	}
	id := hashToken(token)
	now := s.now()
	ttl := s.cfg.CacheTTL

	s.cmu.Lock()
	gen := s.gen // an eviction while we work must not be undone by our insert
	if until, bad := s.negative[id]; bad {
		if now.Before(until) {
			s.cmu.Unlock()
			return nil, errf(Unauthenticated, "sign in to continue")
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
		row, err := store.GetSession(ctx, s.st.Q(), id)
		if errors.Is(err, store.ErrNotFound) {
			s.cmu.Lock()
			if len(s.negative) >= maxNegative {
				for k := range s.negative {
					delete(s.negative, k)
					break
				}
			}
			s.negative[id] = now.Add(negativeTTL)
			s.cmu.Unlock()
			return nil, errf(Unauthenticated, "sign in to continue")
		}
		if err != nil {
			return nil, err
		}
		snap = cachedSession{userID: row.UserID, created: row.Created, lastUsed: row.LastUsed, persisted: row.LastUsed, loaded: now}
	}
	if now.Sub(snap.lastUsed) > s.cfg.SessionIdle || now.Sub(snap.created) > s.cfg.SessionMax {
		s.endSession(ctx, id)
		return nil, errf(Unauthenticated, "your session has ended; sign in again")
	}
	u, err := s.cachedUser(ctx, snap.userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.endSession(ctx, id)
			return nil, errf(Unauthenticated, "sign in to continue")
		}
		return nil, err
	}
	if u.Disabled {
		s.endSession(ctx, id)
		return nil, errf(Unauthenticated, "sign in to continue")
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
	return &Principal{User: *u, SessionID: id}, nil
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
	u, err := store.GetUser(ctx, s.st.Q(), id)
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

// endSession removes one session from the database and the cache.
func (s *Service) endSession(ctx context.Context, id string) {
	s.cmu.Lock()
	s.gen++
	delete(s.sessions, id)
	s.cmu.Unlock()
	if err := store.DeleteSession(ctx, s.st.Q(), id); err != nil {
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
	s.endSession(ctx, hashToken(token))
}

// issueSession starts a session for a user and returns the cookie value.
func (s *Service) issueSession(ctx context.Context, userID string) (string, error) {
	token := randomToken()
	now := s.now().UTC()
	err := s.st.Tx(ctx, func(q store.Q) error {
		if err := store.InsertSession(ctx, q, store.Session{ID: hashToken(token), UserID: userID, Created: now, LastUsed: now}); err != nil {
			return err
		}
		return store.TrimSessions(ctx, q, userID, maxSessionsPerUser)
	})
	return token, err
}

// Login checks a username and password. A wrong password, an unknown user and
// a disabled user get the same answer after the same amount of work.
//
// The attempt is reserved against the address's and the username's allowance
// before any password is hashed, and given back only if it succeeds: hashes
// queue behind each other, and judging every queued attempt against the count
// from before the burst would let a burst through.
func (s *Service) Login(ctx context.Context, username, password, ip string) (*store.User, string, error) {
	ukey := userKey(username)
	if wait := s.reserve("ip:"+ip, ukey); wait > 0 {
		return nil, "", retryAfter(wait)
	}
	ok := false
	defer func() {
		if ok {
			s.byIP.refund("ip:" + ip)
			s.byUser.reset(ukey)
		}
	}()
	if len(password) > MaxPasswordBytes {
		return nil, "", errUnauth()
	}
	var u *store.User
	hash := s.hasher.dummy
	if un, err := NormalizeUsername(username); err == nil {
		switch row, err := store.GetUserByUsername(ctx, s.st.Q(), un); {
		case err == nil:
			u = row
			if h, err := store.GetPasswordHash(ctx, s.st.Q(), row.ID); err == nil {
				hash = h
			} else if !errors.Is(err, store.ErrNotFound) {
				return nil, "", err
			}
		case !errors.Is(err, store.ErrNotFound):
			return nil, "", err
		}
	}
	good, rehash, err := s.hasher.verify(ctx, hash, password)
	if err != nil {
		return nil, "", err
	}
	if !good || u == nil || u.Disabled || hash == s.hasher.dummy {
		return nil, "", errUnauth()
	}
	if rehash {
		if nh, err := s.hasher.hash(ctx, password); err == nil {
			if err := store.SetPasswordHash(ctx, s.st.Q(), u.ID, nh, s.now()); err != nil {
				s.log.Warn("could not upgrade a password hash", "error", err)
			}
		}
	}
	token, err := s.issueSession(ctx, u.ID)
	if err != nil {
		return nil, "", err
	}
	ok = true
	return u, token, nil
}

// reserve counts an attempt against an address and a username at once. It
// returns how long to wait when either is blocked; then neither is charged.
func (s *Service) reserve(ipKey, userKey string) time.Duration {
	if wait := s.byIP.reserve(ipKey); wait > 0 {
		return wait
	}
	if wait := s.byUser.reserve(userKey); wait > 0 {
		s.byIP.refund(ipKey)
		return wait
	}
	return 0
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

// Setup creates the first administrator from the first-run token.
func (s *Service) Setup(ctx context.Context, token, username, name, password, ip string) (*store.User, string, error) {
	if err := s.throttle(ip); err != nil {
		return nil, "", err
	}
	if wait := s.byIP.reserve("ip:" + ip); wait > 0 {
		return nil, "", retryAfter(wait)
	}
	good := false
	defer func() {
		if good {
			s.byIP.refund("ip:" + ip)
		}
	}()
	open, err := s.SetupOpen(ctx)
	if err != nil {
		return nil, "", err
	}
	if !open {
		return nil, "", errf(Exists, "setup is already done")
	}
	if !s.setupTokenOK(token) {
		return nil, "", errf(Forbidden, "the setup token is wrong")
	}
	good = true
	u, err := s.CreateUser(ctx, CreateParams{Username: username, Name: name, Role: "admin", Password: password, Setup: true})
	if err != nil {
		return nil, "", err
	}
	tok, err := s.issueSession(ctx, u.ID)
	return u, tok, err
}

// setupTokenOK compares in constant time; it is false once an account exists.
func (s *Service) setupTokenOK(given string) bool {
	want := s.SetupToken()
	return want != "" && ctEqual(given, want)
}
