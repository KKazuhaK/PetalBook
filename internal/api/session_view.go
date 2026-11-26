package api

import (
	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/store"
)

// sessionUser is the user of GET /api/session and of every sign-in answer: the
// account, and the permission keys it holds in its own tenant.
type sessionUser struct {
	userResp
	Permissions []string `json:"permissions"`
}

// signInAnswer is what a JSON sign-in answers: the shape of GET /api/session's
// user and hold.
type signInAnswer struct {
	User sessionUser `json:"user"`
	Hold *string     `json:"hold"`
}

// holdName is a session's hold as the API says it: null, "passkey" (the person
// must register one) or "password" (they must choose a new one).
func holdName(held bool, by string) *string {
	if !held {
		return nil
	}
	h := "passkey"
	if by == auth.HoldPassword {
		h = "password"
	}
	return &h
}

// signedIn is the answer's user and hold of a sign-in: the same as
// GET /api/session gives the new session. The keys are the person's in their
// home tenant (the session's current tenant, to which the acting scope does
// not apply). They come from the cache entry the next request reads, so a
// sign-in adds no query. It answers the error itself and reports false.
func (s *server) signedIn(c *call, u store.User, held bool, by string) (sessionUser, *string, bool) {
	_, access, err := s.auth.CachedSubject(c.r.Context(), u.ID)
	if err != nil {
		s.fail(c.w, err)
		return sessionUser{}, nil, false
	}
	return sessionUser{s.toUser(u), access.In(u.TenantID).Names()}, holdName(held, by), true
}
