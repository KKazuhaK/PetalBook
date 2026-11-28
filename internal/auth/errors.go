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
	Coded                           // Code and Status say what the answer is
)

// Error is a refusal with a message fit for people.
type Error struct {
	Kind       Kind
	Msg        string
	RetryAfter time.Duration // RateLimited only
	// Code and Status, when set, are the answer's error code and HTTP status
	// (the codes of the sign-in section have their own); Kind is then Coded.
	Code   string
	Status int
	// Accounts is the count a provider_in_use answer carries.
	Accounts *int
	// Organization is the organisation a domain_managed answer names.
	Organization *OrgRef
	// SSO is where an sso_required answer sends the person instead.
	SSO *Discovered
	// Reason, on an Unauthenticated refusal of a cookie whose session ended, is
	// why (store.EndExpired and the others); "" for any other cookie.
	Reason string
}

func (e *Error) Error() string { return e.Msg }

func errf(k Kind, format string, a ...any) *Error {
	return &Error{Kind: k, Msg: fmt.Sprintf(format, a...)}
}

// errUnauth is the one answer to every failed sign-in: it must not tell a
// wrong password from an unknown or disabled user.
func errUnauth() *Error { return errf(Unauthenticated, "Wrong username or password.") }

// coded is a refusal with an error code of its own.
func coded(status int, code, format string, a ...any) *Error {
	return &Error{Kind: Coded, Status: status, Code: code, Msg: fmt.Sprintf(format, a...)}
}

// errAlreadyLinked: the person has an account linked at the provider already
// (one per provider; to switch to another, unlink the first).
var errAlreadyLinked = coded(409, "already_linked", "You already have an account linked at this provider; unlink it first to link another.")
