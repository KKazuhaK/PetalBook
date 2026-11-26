package auth

import (
	"context"
	"errors"
	"strings"

	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/store"
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
		snap = cachedSession{userID: row.UserID, created: row.Created, lastUsed: row.LastUsed, persisted: row.LastUsed, loaded: now, held: row.Held, holdBy: holdOf(row.Method),
			currentTenant: row.CurrentTenant, proofs: row.Proofs}
	}
	sec := s.sec(ctx)
	if now.Sub(snap.lastUsed) > sec.idle || now.Sub(snap.created) > sec.max {
		s.endSession(ctx, id, snap.userID, store.EndExpired)
		return nil, ended(store.EndExpired, "Your session has ended; sign in again.")
	}
	u, access, err := s.cachedEntry(ctx, snap.userID)
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
	return &Principal{User: *u, Access: access, SessionID: id, SessionCreated: snap.created, Held: snap.held, HeldBy: snap.holdBy}, nil
}

// UpdateSessionState sets the context a session is in and its proofs (3.7),
// and forgets the cached copy of the session so that the next request reads
// what was written.
func (s *Service) UpdateSessionState(ctx context.Context, id, currentTenant string, proofs map[string]store.Proof) error {
	err := store.UpdateSessionState(ctx, s.st.Q(), id, currentTenant, proofs)
	s.cmu.Lock()
	s.gen++
	delete(s.sessions, id)
	s.cmu.Unlock()
	return err
}

// HoldPassword is what a session waits for after a sign-in with a temporary
// password: the person's choice of a new one. The session's method says so.
const HoldPassword = "password"
const holdMethodPassword = "password_change"

func holdOf(method string) string {
	if method == holdMethodPassword {
		return HoldPassword
	}
	return ""
}

// holdForPasskey makes, in the cache, a session held for a passkey (no longer
// for a new password).
func (s *Service) holdForPasskey(id string) {
	s.cmu.Lock()
	if cs := s.sessions[id]; cs != nil {
		cs.held, cs.holdBy = true, ""
	}
	s.cmu.Unlock()
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
	u, _, err := s.cachedEntry(ctx, id)
	return u, err
}

// cachedEntry is cachedUser with what the person holds outside spaces, which
// is built only when the entry is loaded: a request reads both from memory.
func (s *Service) cachedEntry(ctx context.Context, id string) (*store.User, authz.Subject, error) {
	now := s.now()
	s.cmu.Lock()
	if c := s.users[id]; c != nil && now.Sub(c.loaded) <= s.cfg.CacheTTL {
		u, access := c.u, c.access
		s.cmu.Unlock()
		return &u, access, nil
	}
	gen := s.gen
	s.cmu.Unlock()
	u, err := store.GetUser(ctx, s.st.Q(), store.AllTenants(), id) // by session or token: the account decides the tenant
	if err != nil {
		return nil, authz.Subject{}, err
	}
	access, err := s.SubjectOf(ctx, *u)
	if err != nil {
		return nil, authz.Subject{}, err
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
		s.users[id] = &cachedUser{u: *u, access: access, loaded: now}
	}
	s.cmu.Unlock()
	return u, access, nil
}

// AccessOf is what an account holds outside spaces by its built-in role: from
// its role and site-administrator flag (3.7 adds its memberships of
// organizations). SubjectOf adds the custom roles it holds.
func AccessOf(u store.User) authz.Subject {
	return authz.SubjectFor(u.Role, u.SiteAdmin, u.TenantID)
}

// SubjectOf is what an account holds outside spaces: its built-in role and
// the custom roles given to it or to its groups. The grants are read only
// while some custom role exists (store.Store.CustomRoles, one atomic load), so
// a server without any pays nothing. The session cache keeps the result.
//
// It builds a principal's subject (a session or token as it is loaded) and an
// actor's outside any request (a bulk job's actor as they are now, the CLI);
// never a request's. A request's subject may be narrowed to a tenant's scope,
// which rebuilding it from the user would undo (a custom site role would bring
// site keys back, and the home role would be held in the narrowed tenant), so
// the account functions take the caller's subject as a parameter and never
// rebuild it.
func (s *Service) SubjectOf(ctx context.Context, u store.User) (authz.Subject, error) {
	sub := AccessOf(u)
	if !s.st.CustomRoles() {
		return sub, nil
	}
	held, err := store.HeldRoles(ctx, s.st.Q(), u.ID)
	if err != nil {
		return authz.Subject{}, err
	}
	gs := make([]authz.Grant, 0, len(held))
	for _, h := range held {
		if h.SpaceID != "" {
			continue // the space level's (reserved)
		}
		var sc authz.Scope
		switch h.Scope {
		case store.RoleScopeSite:
			sc = authz.ScopeSite
		case store.RoleScopeTenant:
			sc = authz.ScopeTenant
		default:
			continue
		}
		keys, unknown := authz.SetOf(h.Keys)
		if len(unknown) > 0 {
			s.log.Warn("a custom role names keys that do not exist; they grant nothing", "keys", unknown)
		}
		gs = append(gs, authz.Grant{Scope: sc, Keys: keys, Tenant: h.TenantID})
	}
	return sub.WithGrants(gs), nil
}

// membershipChanged follows a change of people's groups (and spaces): while
// some custom role exists, a role may be held through a group, so those people
// are read again. Without custom roles nothing is dropped.
func (s *Service) membershipChanged(userIDs ...string) {
	if !s.st.CustomRoles() || len(userIDs) == 0 {
		return
	}
	s.cmu.Lock()
	defer s.cmu.Unlock()
	s.gen++
	for _, id := range userIDs {
		delete(s.users, id)
	}
}

// RolesChanged follows a change of custom roles or their grants: every cached
// person is read again, with what they hold now.
func (s *Service) RolesChanged() {
	s.cmu.Lock()
	defer s.cmu.Unlock()
	s.gen++
	clear(s.users)
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

type clientKey struct{}

// WithClient says, for the sessions that start under ctx, which client signs
// in: "app" or (the default) "web". It picks the site-set session lifetime in 3.7.
func WithClient(ctx context.Context, client string) context.Context {
	return context.WithValue(ctx, clientKey{}, client)
}

// proofMethod is the kind of proof a sign-in method makes (sso, domain,
// password, passkey or social). A person who set a password, accepted an
// invitation or reset it has proved it as a password is.
func proofMethod(method string) string {
	switch {
	case strings.HasPrefix(method, "sso:"):
		return "sso"
	case method == "passkey":
		return "passkey"
	default:
		return "password"
	}
}

// issueSession starts a session for a user and returns the cookie value. method
// is how they signed in (see the sessions table).
func (s *Service) issueSession(ctx context.Context, userID string, held bool, method string) (string, error) {
	token := randomToken()
	now := s.now().UTC()
	ua, _ := ctx.Value(userAgentKey{}).(string)
	client, _ := ctx.Value(clientKey{}).(string)
	if client != "app" {
		client = "web"
	}
	err := s.st.Tx(ctx, func(q store.Q) error {
		// The person's own context is proved by the way they signed in.
		var tenant string
		if err := q.QueryRow(ctx, `SELECT tenant_id FROM users WHERE id = ?`, userID).Scan(&tenant); err != nil {
			return err
		}
		at := now.UnixMilli()
		proof := store.Proof{Method: proofMethod(method), At: at, Seen: at}
		if slug, ok := strings.CutPrefix(method, "sso:"); ok {
			proof.Provider = slug
		}
		if err := store.InsertSession(ctx, q, store.Session{ID: hashToken(token), UserID: userID, Created: now, LastUsed: now, Held: held, UserAgent: ua, Method: method,
			Client: client, Proofs: map[string]store.Proof{tenant: proof}}); err != nil {
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
