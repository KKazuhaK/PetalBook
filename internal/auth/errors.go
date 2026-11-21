package auth

import (
	"fmt"
	"time"
)

// Kind classifies an Error for the API, which maps it to a status and code.
type Kind int

const (
	Invalid         Kind = iota + 1 // 400 invalid
	Unauthenticated                 // 401 unauthenticated
	Forbidden                       // 403 forbidden
	NotFound                        // 404 not_found
	Conflict                        // 409 conflict
	Exists                          // 409 exists
	RateLimited                     // 429 rate_limited
	Unavailable                     // the identity provider cannot be reached
)

// Error is a refusal with a message fit for people.
type Error struct {
	Kind       Kind
	Msg        string
	RetryAfter time.Duration // RateLimited only
}

func (e *Error) Error() string { return e.Msg }

func errf(k Kind, format string, a ...any) *Error {
	return &Error{Kind: k, Msg: fmt.Sprintf(format, a...)}
}

// errUnauth is the one answer to every failed sign-in: it must not tell a
// wrong password from an unknown or disabled user.
func errUnauth() *Error { return errf(Unauthenticated, "wrong username or password") }
