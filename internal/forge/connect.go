package forge

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/connections"
	"github.com/KKazuhaK/PetalBook/internal/githubapp"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// Errors of using a user's authorisation.
var (
	// ErrNoAuthorization: the user has not connected an account here.
	ErrNoAuthorization = errors.New("the user has not connected an account")
	// ErrAuthorizationExpired: the refresh token ran out or was revoked.
	ErrAuthorizationExpired = errors.New("the authorisation expired or was revoked")
)

const (
	purposeAccess  = "access"
	purposeRefresh = "refresh"
)

// sealBinding is what a person's tokens are sealed under: the provider and the
// person, so a token cannot be moved to another row.
func (p *Provider) sealBinding(userID string) string { return p.id + ":" + userID }

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
	now := p.m.opt.Now()
	db := p.m.opt.Store.Q()
	if prev, err := store.GetProviderAuthorization(ctx, db, p.id, userID); err == nil && prev.AccountID != acct.ID {
		n, err := store.CountUserProviderSpaces(ctx, db, p.id, userID)
		if err != nil {
			p.m.log.Error("could not count the spaces of a connection", "provider", p.id, "error", err)
			return fail(cbError)
		}
		if n > 0 {
			return fail(cbDifferent)
		}
	}
	row := store.ProviderAuthorization{Provider: p.id, UserID: userID, AccountID: acct.ID, Login: acct.Login, State: store.GitHubOK,
		AccessExpires: tok.AccessExpires, RefreshExpires: tok.RefreshExpires, Admin: acct.Admin, Connected: now, Updated: now}
	box := p.m.opt.Secrets
	if row.AccessEnc, err = box.Seal(p.sealBinding(userID), purposeAccess, []byte(tok.Access)); err != nil {
		p.m.log.Error("could not seal a token", "provider", p.id, "error", err)
		return fail(cbError)
	}
	if tok.Refresh != "" {
		if row.RefreshEnc, err = box.Seal(p.sealBinding(userID), purposeRefresh, []byte(tok.Refresh)); err != nil {
			p.m.log.Error("could not seal a token", "provider", p.id, "error", err)
			return fail(cbError)
		}
	}
	switch err := store.PutProviderAuthorization(ctx, db, row); {
	case errors.Is(err, store.ErrExists):
		return fail(cbInUse)
	case err != nil:
		p.m.log.Error("could not store an authorisation", "provider", p.id, "error", err)
		return fail(cbError)
	}
	p.bump(userID)
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
	db := p.m.opt.Store.Q()
	a, err := store.GetProviderAuthorization(ctx, db, p.id, userID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	p.revoke(ctx, *a)
	if err := store.DeleteProviderAuthorization(ctx, db, p.id, userID); err != nil {
		return err
	}
	p.bump(userID)
	return nil
}

func (p *Provider) revoke(ctx context.Context, a store.ProviderAuthorization) {
	path := p.d.revokePath()
	r, secret := p.snap()
	if path == "" || a.AccessEnc == "" || r.ClientID == "" || secret == "" {
		return
	}
	b, err := p.m.opt.Secrets.Open(p.sealBinding(a.UserID), purposeAccess, a.AccessEnc)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	form := url.Values{"client_id": {r.ClientID}, "client_secret": {secret}, "token": {string(b)}}
	if _, err := p.c.do(ctx, request{method: http.MethodPost, path: path, form: form, kind: "revoke"}, nil); err != nil {
		p.m.log.Info("the host did not revoke a token", "provider", p.id, "reason", reason(err))
	}
}

// --- tokens and their renewal -------------------------------------------------------

type cachedUser struct {
	access  string
	expires time.Time // zero: none
	version int64
}

type renewal struct {
	done   chan struct{}
	access string
	err    error
}

// UserToken returns the user's access token, renewing it first when it has
// less than five minutes left. ErrNoAuthorization or ErrAuthorizationExpired
// when there is nothing to use; an *Error when the host cannot be asked.
func (p *Provider) UserToken(ctx context.Context, userID string) (string, error) {
	now := p.m.opt.Now()
	p.mu.Lock()
	if c, ok := p.tokens[userID]; ok && (c.expires.IsZero() || now.Add(renewBefore).Before(c.expires)) {
		p.mu.Unlock()
		return c.access, nil
	}
	p.mu.Unlock()
	return p.renew(ctx, userID, false)
}

// renew renews one user's tokens, one renewal at a time per user; a caller that
// arrives meanwhile waits for it. force renews even if the token is still good.
func (p *Provider) renew(ctx context.Context, userID string, force bool) (string, error) {
	p.mu.Lock()
	if r, ok := p.renewing[userID]; ok {
		p.mu.Unlock()
		select {
		case <-r.done:
			return r.access, r.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	r := &renewal{done: make(chan struct{})}
	p.renewing[userID] = r
	p.mu.Unlock()
	// Once the host has handed out a new refresh token the old one is dead: the
	// new pair must reach the database even if this request goes away.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	r.access, r.err = p.doRenew(wctx, userID, force)
	cancel()
	p.mu.Lock()
	delete(p.renewing, userID)
	p.mu.Unlock()
	close(r.done)
	return r.access, r.err
}

// tokenGen is how many times a user's connection was replaced or removed in
// this process (user), and the whole cache dropped (all). A renewal notes it
// before it reads the database and caches what it read only if it is unchanged.
type tokenGen struct{ all, user uint64 }

func (p *Provider) generation(userID string) tokenGen {
	p.mu.Lock()
	defer p.mu.Unlock()
	return tokenGen{all: p.genAll, user: p.gens[userID]}
}

// bump says the user's connection was replaced or removed (after the database
// was written): the cached token goes, and a renewal that read before it does
// not cache.
func (p *Provider) bump(userID string) {
	p.mu.Lock()
	p.gens[userID]++
	delete(p.tokens, userID)
	p.mu.Unlock()
}

// dropAllTokens empties the cache (the client changed or the provider went away).
func (p *Provider) dropAllTokens() {
	p.mu.Lock()
	p.genAll++
	p.tokens = map[string]cachedUser{}
	p.mu.Unlock()
}

func (p *Provider) cacheToken(userID string, gen tokenGen, access string, expires time.Time, version int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if gen != (tokenGen{all: p.genAll, user: p.gens[userID]}) {
		return // the connection changed after this was read
	}
	if len(p.tokens) >= maxUserTokens {
		for k := range p.tokens {
			delete(p.tokens, k)
			break
		}
	}
	p.tokens[userID] = cachedUser{access: access, expires: expires, version: version}
}

func (p *Provider) dropToken(userID string) {
	p.mu.Lock()
	delete(p.tokens, userID)
	p.mu.Unlock()
}

func (p *Provider) doRenew(ctx context.Context, userID string, force bool) (string, error) {
	db := p.m.opt.Store.Q()
	box := p.m.opt.Secrets
	bind := p.sealBinding(userID)
	for pass := 0; pass < 3; pass++ {
		gen := p.generation(userID)
		row, err := store.GetProviderAuthorization(ctx, db, p.id, userID)
		if errors.Is(err, store.ErrNotFound) {
			p.dropToken(userID)
			return "", ErrNoAuthorization
		}
		if err != nil {
			return "", err
		}
		if row.State != store.GitHubOK {
			p.dropToken(userID)
			return "", ErrAuthorizationExpired
		}
		now := p.m.opt.Now()
		access, err := box.Open(bind, purposeAccess, row.AccessEnc)
		if err != nil {
			return "", err
		}
		good := row.AccessExpires.IsZero() || now.Add(renewBefore).Before(row.AccessExpires)
		if good && !force {
			p.cacheToken(userID, gen, string(access), row.AccessExpires, row.Version)
			return string(access), nil
		}
		if row.RefreshEnc == "" {
			if row.AccessExpires.IsZero() || now.Before(row.AccessExpires) {
				p.cacheToken(userID, gen, string(access), row.AccessExpires, row.Version) // no refresh token: use it to the end
				return string(access), nil
			}
			p.expire(ctx, row)
			return "", ErrAuthorizationExpired
		}
		r, secret, nerr := p.need()
		if nerr != nil {
			return "", nerr
		}
		refresh, err := box.Open(bind, purposeRefresh, row.RefreshEnc)
		if err != nil {
			return "", err
		}
		tok, err := p.m.token(ctx, p.d, p.c, r.ClientID, secret, [][2]string{{"grant_type", "refresh_token"}, {"refresh_token", string(refresh)},
			{"redirect_uri", p.CallbackURL()}})
		if err != nil {
			if isBadGrant(err) {
				p.m.log.Info("an authorisation expired or was revoked", "provider", p.id, "user", userID)
				p.expire(ctx, row)
				return "", ErrAuthorizationExpired
			}
			var oe *OAuthError
			if errors.As(err, &oe) {
				// Another refusal (the client credentials changed): the provider needs attention.
				p.m.log.Warn("the host refused to renew a token", "provider", p.id, "reason", oe.Code)
				return "", p.notConfigured()
			}
			return "", p.wrap(err)
		}
		if tok.Refresh == "" { // a host that does not rotate: the refresh token stays
			tok.Refresh, tok.RefreshExpires = string(refresh), row.RefreshExpires
		}
		accessEnc, err := box.Seal(bind, purposeAccess, []byte(tok.Access))
		if err != nil {
			return "", err
		}
		refreshEnc, err := box.Seal(bind, purposeRefresh, []byte(tok.Refresh))
		if err != nil {
			return "", err
		}
		// The new pair is saved before it is used, and replaces only the pair it
		// was made from.
		var replaced bool
		for try := 0; try < 3; try++ {
			if replaced, err = store.ReplaceProviderTokens(ctx, db, p.id, userID, row.Version, accessEnc, refreshEnc, tok.AccessExpires, tok.RefreshExpires, p.m.opt.Now()); err == nil {
				break
			}
			time.Sleep(time.Duration(try+1) * 100 * time.Millisecond)
		}
		if err != nil {
			// The old refresh token is dead and the new one is not stored: this
			// connection cannot go on.
			p.m.log.Error("could not store a renewed token", "provider", p.id, "user", userID, "error", err)
			p.expire(ctx, row)
			return "", ErrAuthorizationExpired
		}
		if !replaced {
			continue // reconnected or renewed elsewhere meanwhile: use what is there now
		}
		p.cacheToken(userID, gen, tok.Access, tok.AccessExpires, row.Version+1)
		return tok.Access, nil
	}
	return "", p.errf(http.StatusServiceUnavailable, connections.CodeUnavailable, "The authorisation keeps changing; try again.")
}

func (p *Provider) expire(ctx context.Context, row *store.ProviderAuthorization) {
	if _, err := store.ExpireProviderAuthorization(ctx, p.m.opt.Store.Q(), p.id, row.UserID, row.Version, p.m.opt.Now()); err != nil {
		p.m.log.Error("could not mark an authorisation expired", "provider", p.id, "user", row.UserID, "error", err)
	}
	p.dropToken(row.UserID)
}

// RenewDue renews, one at a time, the connections whose refresh token is in the
// last tenth of its life (and ends within RenewWithin at the latest), so
// someone whose spaces sit closed for months stays connected without the host
// being asked every day. It returns how many it renewed.
func (p *Provider) RenewDue(ctx context.Context) int {
	if !p.configured() {
		return 0
	}
	n := 0
	now := p.m.opt.Now()
	var cur store.DueCursor
	tried := map[string]bool{}
	for {
		due, err := store.DueProviderRenewals(ctx, p.m.opt.Store.Q(), p.id, now, p.m.opt.RenewWithin, cur, 50)
		if err != nil {
			p.m.log.Error("could not look for authorisations to renew", "provider", p.id, "error", err)
			return n
		}
		for _, a := range due {
			cur = store.DueCursor{RefreshExpires: a.RefreshExpires, UserID: a.UserID}
			if tried[a.UserID] {
				continue
			}
			tried[a.UserID] = true
			if ctx.Err() != nil {
				return n
			}
			if _, err := p.renew(ctx, a.UserID, true); err != nil {
				p.m.log.Info("could not renew an authorisation", "provider", p.id, "user", a.UserID, "reason", err.Error())
				continue
			}
			n++
		}
		if len(due) < 50 {
			return n
		}
	}
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
