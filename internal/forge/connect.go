package forge

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"petalbook.app/server/internal/connections"
	"petalbook.app/server/internal/githubapp"
	"petalbook.app/server/internal/store"
)

// Errors of using a user's authorisation (the token service's).
var (
	// ErrNoAuthorization: the user has not connected an account here.
	ErrNoAuthorization = connections.ErrNoAuthorization
	// ErrAuthorizationExpired: the refresh token ran out or was revoked.
	ErrAuthorizationExpired = connections.ErrAuthorizationExpired
)

// StartConnect begins connecting a person's account: it returns the address to
// send the browser to and the value of the cookie that ties the attempt to that
// browser. State and a PKCE challenge go with it.
func (p *Provider) StartConnect(ctx context.Context, userID string, req connections.StartRequest) (*connections.Start, error) {
	r, _, err := p.need()
	if err != nil {
		return nil, err
	}
	if err := p.visibleTo(ctx, userID); err != nil {
		return nil, err
	}
	if p.m.opt.PublicURL == "" {
		return nil, p.errf(http.StatusConflict, connections.CodePublicURLNeeded, "People cannot connect accounts: the server needs its --public-url.")
	}
	state := githubapp.RandomState()
	verifier, challenge := githubapp.PKCE()
	binding := p.attempts.put(attempt{state: state, verifier: verifier, userID: userID, ret: safeReturn(req.Return)})
	return &connections.Start{URL: p.d.authorizeURL(p.c.base, r.ClientID, p.CallbackURL(), state, challenge), Binding: binding}, nil
}

// Callback failure codes (the connection_error of /settings).
const (
	cbDenied    = "denied"
	cbExpired   = "expired"
	cbError     = "provider_error"
	cbInUse     = "account_in_use"
	cbDifferent = "different_account"
)

func (p *Provider) failRedirect(code string) string {
	return "/settings?provider=" + url.QueryEscape(p.id) + "&connection_error=" + code
}

// CompleteConnect finishes an authorisation from the host's redirect. It never
// fails: every outcome is a path to redirect to. userID is the user of the
// browser's session ("" without one); clearBinding says the attempt is over.
func (p *Provider) CompleteConnect(ctx context.Context, binding, userID string, q url.Values) (redirect string, clearBinding bool) {
	one := func(k string) (string, bool) {
		v := q[k]
		if len(v) > 1 {
			return "", false
		}
		if len(v) == 0 {
			return "", true
		}
		return v[0], true
	}
	state, ok1 := one("state")
	code, ok2 := one("code")
	herr, ok3 := one("error")
	if !ok1 || !ok2 || !ok3 || binding == "" || state == "" {
		return p.failRedirect(cbExpired), false
	}
	a, res := p.attempts.take(binding, state, userID)
	if res != takeOK {
		return p.failRedirect(cbExpired), res == takeUnknown
	}
	fail := func(c string) (string, bool) { return p.failRedirect(c), true }
	if herr != "" {
		p.m.log.Info("an authorisation was not given", "provider", p.id, "error", sanitize(herr))
		return fail(cbDenied)
	}
	r, secret, err := p.need()
	if err != nil || code == "" || p.visibleTo(ctx, userID) != nil {
		return fail(cbError)
	}
	tok, err := p.m.token(ctx, p.d, p.c, r.ClientID, secret, [][2]string{{"grant_type", "authorization_code"}, {"code", code},
		{"redirect_uri", p.CallbackURL()}, {"code_verifier", a.verifier}})
	if err != nil {
		p.m.log.Warn("the host refused the authorisation code", "provider", p.id, "reason", reason(err))
		return fail(cbError)
	}
	acct, err := p.d.user(ctx, p.c, tok.Access)
	if err != nil {
		p.m.log.Warn("could not read the account", "provider", p.id, "reason", reason(err))
		return fail(cbError)
	}
	if r.PlatformVersion == "" {
		p.learnVersion(ctx, r, tok.Access)
	}
	db := p.m.opt.Store.Q()
	if prev, err := store.GetRepoLink(ctx, db, p.id, userID); err == nil && prev.Subject != acct.ID {
		n, err := store.CountUserProviderSpaces(ctx, db, p.id, userID)
		if err != nil {
			p.m.log.Error("could not count the spaces of a connection", "provider", p.id, "error", err)
			return fail(cbError)
		}
		if n > 0 {
			return fail(cbDifferent)
		}
	}
	switch err := p.m.tokens.Put(ctx, connections.Account{ProviderID: p.id, UserID: userID, Subject: acct.ID, Login: acct.Login, Admin: acct.Admin},
		connections.Pair{Access: tok.Access, Refresh: tok.Refresh, AccessExpires: tok.AccessExpires, RefreshExpires: tok.RefreshExpires}); {
	case errors.Is(err, store.ErrExists):
		return fail(cbInUse)
	case err != nil:
		p.m.log.Error("could not store an authorisation", "provider", p.id, "error", err)
		return fail(cbError)
	}
	p.changed(userID)
	return a.ret, true
}

// reason is an error as a log line may say it: never a token, since none of
// this package's errors carries one.
func reason(err error) string {
	var oe *OAuthError
	if errors.As(err, &oe) {
		return oe.Code
	}
	var se *StatusError
	if errors.As(err, &se) {
		return se.Error()
	}
	return "could not be reached"
}

// Disconnect deletes a user's connection. The host is asked to revoke the
// token when it can (best effort); the tokens are deleted from the database and
// memory whatever it answers.
func (p *Provider) Disconnect(ctx context.Context, userID string) error {
	a, err := store.GetRepoLink(ctx, p.m.opt.Store.Q(), p.id, userID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	// The tokens go first; the host is asked to revoke them once they are.
	if err := p.m.tokens.Remove(ctx, p.id, userID); err != nil {
		return err
	}
	p.revoke(ctx, a)
	return nil
}

// RevokeRemoved is told that a link went with its row (connections.Revoker).
func (p *Provider) RevokeRemoved(ctx context.Context, l *store.RepoLink) {
	p.m.tokens.Forget(p.id, l.UserID)
	p.revoke(ctx, l)
}

func (p *Provider) revoke(ctx context.Context, a *store.RepoLink) {
	path := p.d.revokePath()
	r, secret := p.snap()
	if path == "" || a.AccessEnc == "" || r.ClientID == "" || secret == "" {
		return
	}
	access, err := p.m.tokens.OpenAccess(a)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	form := url.Values{"client_id": {r.ClientID}, "client_secret": {secret}, "token": {access}}
	if _, err := p.c.do(ctx, request{method: http.MethodPost, path: path, form: form, kind: "revoke"}, nil); err != nil {
		p.m.log.Info("the host did not revoke a token", "provider", p.id, "reason", reason(err))
	}
}

// --- tokens ---------------------------------------------------------------------------

// UserToken returns the user's access token, renewing it first when it has
// less than five minutes left (the token service's). ErrNoAuthorization or
// ErrAuthorizationExpired when there is nothing to use; an *Error when the host
// cannot be asked.
func (p *Provider) UserToken(ctx context.Context, userID string) (string, error) {
	return p.m.tokens.Token(ctx, p.id, userID)
}

// RefreshTokens trades a refresh token for a new pair at the host: it is what
// the token service asks of this provider (connections.Refresher).
func (p *Provider) RefreshTokens(ctx context.Context, refresh string) (*connections.Pair, error) {
	r, secret, err := p.need()
	if err != nil {
		return nil, err
	}
	tok, err := p.m.token(ctx, p.d, p.c, r.ClientID, secret, [][2]string{{"grant_type", "refresh_token"}, {"refresh_token", refresh},
		{"redirect_uri", p.CallbackURL()}})
	if err != nil {
		if isBadGrant(err) {
			return nil, connections.ErrRevoked
		}
		var oe *OAuthError
		if errors.As(err, &oe) {
			// Another refusal (the client credentials changed): the provider needs attention.
			p.m.log.Warn("the host refused to renew a token", "provider", p.id, "reason", oe.Code)
			return nil, p.notConfigured()
		}
		return nil, p.wrap(err)
	}
	return &connections.Pair{Access: tok.Access, Refresh: tok.Refresh, AccessExpires: tok.AccessExpires, RefreshExpires: tok.RefreshExpires}, nil
}

// Attempts is how many authorisations are in progress (tests).
func (p *Provider) Attempts() int { return p.attempts.len() }

// learnVersion reads, with a person's token, the version of a host that does not
// say it to anyone signed out, and keeps it. A version that cannot do what this
// server needs (PKCE) leaves the provider not set up until an administrator has
// seen to it.
func (p *Provider) learnVersion(ctx context.Context, seen store.ConnProviderRow, access string) {
	v, err := p.d.version(ctx, p.c, access)
	if err != nil || v == "" {
		return
	}
	state := stateOK
	if !p.d.minVersion(v) {
		state = stateError
		p.m.log.Warn("a host is older than this server supports", "provider", p.id, "version", sanitize(v))
	}
	p.setState(ctx, seen, state, v, p.m.opt.Now())
}
