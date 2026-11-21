package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	authoidc "github.com/KazuhaHub/authcore/oidc"

	"github.com/KazuhaHub/PetalBook/internal/store"
)

// OIDCConfig is the configured OpenID Connect provider.
type OIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	Name         string // shown on the sign-in page
	Scopes       []string
	AutoCreate   bool // an identity nobody has creates a member account
}

type oidcState struct {
	cfg      OIDCConfig
	redirect string
	client   *http.Client

	mu      sync.Mutex
	prov    *authoidc.Provider
	provErr error
	tried   time.Time
}

// ValidateOIDC checks the settings without touching the network, so a mistake
// fails at start-up before anything is created.
func ValidateOIDC(c OIDCConfig, publicURL string) error {
	_, err := newOIDCState(Config{OIDC: &c, PublicURL: publicURL})
	return err
}

func newOIDCState(cfg Config) (*oidcState, error) {
	c := *cfg.OIDC
	if c.Issuer == "" || c.ClientID == "" {
		return nil, errors.New("OIDC needs an issuer and a client id")
	}
	if cfg.PublicURL == "" {
		return nil, errors.New("OIDC needs --public-url: the provider is told to send people back to it")
	}
	if c.Name == "" {
		c.Name = "Single sign-on"
	}
	if len(c.Scopes) == 0 {
		// openid is always asked for; these make the provider include the name
		// and the username or email that new accounts are named after.
		c.Scopes = []string{"profile", "email"}
	}
	pu, err := url.Parse(strings.TrimRight(cfg.PublicURL, "/"))
	if err != nil {
		return nil, err
	}
	pu.Path += "/api/auth/oidc/callback"
	redirect := pu.String()
	// Check the settings now, so a typo fails at start-up and not at the first
	// sign-in. Reaching the provider can wait: it may be down when we start.
	if _, err := authoidc.New(canceled(), authoidc.Config{IssuerURL: c.Issuer, ClientID: c.ClientID, ClientSecret: c.ClientSecret, RedirectURL: redirect, Scopes: c.Scopes}); errors.Is(err, authoidc.ErrInvalidConfig) {
		return nil, errors.New("the OIDC settings are not valid: the issuer and the public URL must be https addresses, and the client id must be set")
	}
	return &oidcState{cfg: c, redirect: redirect, client: cfg.HTTPClient}, nil
}

func canceled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// provider discovers the provider on first use and keeps it; after a failure
// it waits a few seconds before trying again.
func (s *Service) provider(ctx context.Context) (*authoidc.Provider, error) {
	o := s.oidc
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.prov != nil {
		return o.prov, nil
	}
	if o.provErr != nil && s.now().Sub(o.tried) < 5*time.Second {
		return nil, o.provErr
	}
	p, err := authoidc.New(context.WithoutCancel(ctx), authoidc.Config{
		IssuerURL: o.cfg.Issuer, ClientID: o.cfg.ClientID, ClientSecret: o.cfg.ClientSecret,
		RedirectURL: o.redirect, Scopes: o.cfg.Scopes, HTTPClient: o.client,
	})
	o.tried = s.now()
	if err != nil {
		o.provErr = err
		return nil, err
	}
	o.prov, o.provErr = p, nil
	return p, nil
}

// WarmOIDC reaches the provider once, in the background, so a wrong issuer
// shows up in the log at start-up. Failure is not fatal.
func (s *Service) WarmOIDC(ctx context.Context) {
	if s.oidc == nil {
		return
	}
	if _, err := s.provider(ctx); err != nil {
		s.log.Warn("could not reach the OpenID Connect provider yet; will try again at the next sign-in", "issuer", s.oidc.cfg.Issuer, "reason", oidcReason(err))
		return
	}
	s.log.Info("OpenID Connect provider ready", "issuer", s.oidc.cfg.Issuer)
}

// BeginParams is a request to start an OIDC sign-in.
type BeginParams struct {
	Intent string // login (default), setup, invite or link
	Token  string
	Return string
	// User is the signed-in user, for link.
	User *store.User
}

// OIDCBegin starts a sign-in: it keeps the protocol state in memory and returns
// the provider's address and the value of the cookie that ties the attempt to
// this browser.
func (s *Service) OIDCBegin(ctx context.Context, p BeginParams, ip string) (authURL, binding string, err error) {
	if s.oidc == nil {
		return "", "", errf(NotFound, "single sign-on is not configured")
	}
	switch p.Intent {
	case "":
		p.Intent = "login"
	case "login", "setup", "invite":
	case "link":
		if p.User == nil {
			return "", "", errf(Unauthenticated, "sign in to link an identity")
		}
	default:
		return "", "", errf(Invalid, "intent is login, setup, invite or link")
	}
	if len(p.Token) > 256 {
		return "", "", errf(Invalid, "the token is too long")
	}
	if d, ok := s.starts.take(ip); !ok {
		return "", "", retryAfter(d)
	}
	prov, err := s.provider(ctx)
	if err != nil {
		s.log.Error("OpenID Connect provider unavailable", "reason", oidcReason(err))
		return "", "", errf(Unavailable, "the single sign-on provider cannot be reached right now")
	}
	az, err := prov.Begin()
	if err != nil {
		s.log.Error("OpenID Connect begin failed", "reason", oidcReason(err))
		return "", "", errf(Unavailable, "could not start the sign-in")
	}
	e := pendingEntry{state: az.State, intent: p.Intent, token: p.Token, ret: SafeReturn(p.Return)}
	if p.User != nil {
		e.userID = p.User.ID
	}
	return az.URL, s.pending.put(e), nil
}

// OIDCCookie carries the binding value to the browser; ClearOIDCCookie removes it.
func (s *Service) OIDCCookie(binding string) *http.Cookie {
	return s.newCookie(s.OIDCCookieName(), binding, int(pendingTTL/time.Second))
}

func (s *Service) ClearOIDCCookie() *http.Cookie { return s.newCookie(s.OIDCCookieName(), "", -1) }

// OIDCCookieName follows the same prefix rule as the session cookie.
func (s *Service) OIDCCookieName() string { return s.cookieName("petalbook_oidc") }

// Outcome is how a callback ends: always a redirect.
type Outcome struct {
	Redirect string
	// Session is the cookie value of a new session; "" when none was started.
	Session string
	// ClearBinding says the sign-in is over, whatever the result.
	ClearBinding bool
}

func oneParam(q url.Values, k string) (string, bool) {
	v := q[k]
	if len(v) > 1 {
		return "", false
	}
	if len(v) == 0 {
		return "", true
	}
	return v[0], true
}

func failURL(intent, code string) string {
	base := "/login"
	if intent == "link" {
		base = "/settings"
	}
	return base + "?error=" + code
}

// OIDCComplete finishes a sign-in from the provider's redirect. binding is the
// value of the browser's binding cookie; sessionToken the browser's session
// cookie (for link). It never returns an error: every failure is a redirect
// to a page that can explain it.
func (s *Service) OIDCComplete(ctx context.Context, binding, sessionToken string, q url.Values, ip string) Outcome {
	if s.oidc == nil {
		return Outcome{Redirect: failURL("", "idp_error")}
	}
	state, ok1 := oneParam(q, "state")
	code, ok2 := oneParam(q, "code")
	idpErr, ok3 := oneParam(q, "error")
	if !ok1 || !ok2 || !ok3 {
		s.log.Warn("sign-in refused: repeated callback parameters")
		return Outcome{Redirect: failURL("", "idp_error")}
	}
	if idpErr != "" {
		// Only the browser that started the attempt can end it.
		intent := ""
		if binding != "" && state != "" {
			if e, res := s.pending.take(binding, state); res == takeOK {
				intent = e.intent
			}
		}
		s.log.Warn("sign-in failed at the provider", "error", sanitizeLog(idpErr))
		return Outcome{Redirect: failURL(intent, "idp_error"), ClearBinding: intent != ""}
	}
	if state == "" || code == "" {
		return Outcome{Redirect: failURL("", "idp_error")}
	}
	if binding == "" {
		return Outcome{Redirect: failURL("", "expired")}
	}
	e, res := s.pending.take(binding, state)
	switch res {
	case takeUnknown:
		s.log.Warn("sign-in refused: unknown, expired or replayed state")
		return Outcome{Redirect: failURL("", "expired"), ClearBinding: true}
	case takeBinding:
		// The real attempt stays: a forged callback must not cancel it.
		s.log.Warn("sign-in refused: the state does not belong to this browser")
		return Outcome{Redirect: failURL("", "idp_error")}
	}
	out := Outcome{ClearBinding: true}
	fail := func(code string) Outcome {
		out.Redirect = failURL(e.intent, code)
		return out
	}
	prov, err := s.provider(ctx)
	if err != nil {
		s.log.Error("OpenID Connect provider unavailable", "reason", oidcReason(err))
		return fail("idp_error")
	}
	a, err := prov.Complete(ctx, state, code, e.state)
	if err != nil {
		s.log.Warn("sign-in failed: the code or ID token was rejected", "reason", oidcReason(err))
		return fail("idp_error")
	}
	id, err := identityFromAssertion(a)
	if err != nil {
		s.log.Warn("sign-in failed: unusable claims", "reason", err.Error())
		return fail("idp_error")
	}

	var user *store.User
	switch e.intent {
	case "login":
		user, err = s.oidcLogin(ctx, id)
	case "setup":
		user, err = s.oidcSetup(ctx, id, e.token)
	case "invite":
		user, err = s.oidcInvite(ctx, id, e.token)
	case "link":
		err = s.oidcLink(ctx, id, e.userID, sessionToken)
	}
	if err != nil {
		var ae *oidcError
		if errors.As(err, &ae) {
			return fail(ae.code)
		}
		s.log.Error("sign-in failed", "error", err)
		return fail("idp_error")
	}
	out.Redirect = e.ret
	if e.intent == "link" {
		return out // the session stays as it is
	}
	tok, err := s.issueSession(ctx, user.ID)
	if err != nil {
		s.log.Error("could not start a session", "error", err)
		return fail("idp_error")
	}
	out.Session = tok
	s.log.Info("signed in", "user", user.ID, "method", "oidc")
	return out
}

type oidcError struct{ code string }

func (e *oidcError) Error() string { return e.code }

func oidcErr(code string) error { return &oidcError{code} }

// claimed is the verified identity and what the provider says about it.
type claimed struct {
	issuer, subject string
	name            string
	preferred       string
	email           string
}

func (c claimed) label() string {
	if c.email != "" {
		return c.email
	}
	return c.preferred
}

func (c claimed) params() *IdentityParams {
	return &IdentityParams{Issuer: c.issuer, Subject: c.subject, Label: c.label()}
}

func identityFromAssertion(a *authoidc.Assertion) (claimed, error) {
	var raw map[string]json.RawMessage
	if err := a.Claims(&raw); err != nil {
		return claimed{}, errors.New("the claims are not a JSON object")
	}
	str := func(k string) string {
		var v string
		if json.Unmarshal(raw[k], &v) != nil {
			return ""
		}
		return v
	}
	sub := strings.TrimSpace(a.Subject)
	if sub == "" || len(sub) > 255 || !utf8.ValidString(sub) || strings.ContainsFunc(sub, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return claimed{}, errors.New("the sub claim is empty or not usable")
	}
	// The issuer is the verified token's: the library checked it equals the configured one.
	return claimed{issuer: a.Issuer, subject: sub, name: str("name"), preferred: str("preferred_username"), email: clip(str("email"), 254)}, nil
}

func clip(s string, n int) string {
	if len(s) > n {
		s = strings.ToValidUTF8(s[:n], "")
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

func (s *Service) oidcLogin(ctx context.Context, c claimed) (*store.User, error) {
	ident, err := store.FindIdentity(ctx, s.st.Q(), c.issuer, c.subject)
	switch {
	case err == nil:
		u, err := store.GetUser(ctx, s.st.Q(), ident.UserID)
		if err != nil {
			return nil, err
		}
		if u.Disabled {
			return nil, oidcErr("disabled")
		}
		return u, nil
	case !errors.Is(err, store.ErrNotFound):
		return nil, err
	}
	// Nobody has this identity. Making an account is opt-in, and never the
	// first one: that is the administrator's, made with the setup token.
	noUsers, _ := s.SetupOpen(ctx)
	if !s.oidc.cfg.AutoCreate || noUsers {
		return nil, oidcErr("no_account")
	}
	return s.createFromClaims(ctx, c, "member", "", false)
}

func (s *Service) oidcSetup(ctx context.Context, c claimed, token string) (*store.User, error) {
	if open, err := s.SetupOpen(ctx); err != nil {
		return nil, err
	} else if !open || !s.setupTokenOK(token) {
		return nil, oidcErr("setup_invalid")
	}
	u, err := s.createFromClaims(ctx, c, "admin", "", true)
	var ae *Error
	if errors.As(err, &ae) && ae.Kind == Exists {
		return nil, oidcErr("setup_invalid")
	}
	return u, err
}

func (s *Service) oidcInvite(ctx context.Context, c claimed, token string) (*store.User, error) {
	inv, err := s.findInvite(ctx, token)
	var ae *Error
	if errors.As(err, &ae) {
		return nil, oidcErr("invite_invalid")
	} else if err != nil {
		return nil, err
	}
	u, err := s.createFromClaims(ctx, c, inv.Role, inv.ID, false)
	if errors.As(err, &ae) && ae.Kind == NotFound {
		return nil, oidcErr("invite_invalid")
	}
	return u, err
}

func (s *Service) oidcLink(ctx context.Context, c claimed, userID, sessionToken string) error {
	// The browser must still be signed in as the user who asked.
	p, err := s.Authenticate(ctx, sessionToken)
	if err != nil || p.User.ID != userID {
		return oidcErr("expired")
	}
	if ident, err := store.FindIdentity(ctx, s.st.Q(), c.issuer, c.subject); err == nil {
		if ident.UserID == userID {
			return nil
		}
		return oidcErr("identity_in_use")
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	err = store.InsertIdentity(ctx, s.st.Q(), store.Identity{ID: s.newID(), UserID: userID, Issuer: c.issuer, Subject: c.subject, Label: c.label(), Created: s.now()})
	if store.IsUnique(err) {
		return oidcErr("identity_in_use")
	}
	return err
}

// createFromClaims makes an account for an identity: the name from the name
// claim, the username from preferred_username or the part of the email before
// the @, made valid and unique. The email claim is shown as a label only and
// never decides which account an identity belongs to.
func (s *Service) createFromClaims(ctx context.Context, c claimed, role, inviteID string, setup bool) (*store.User, error) {
	base := usernameFrom(c)
	name := clip(c.name, 100)
	if strings.TrimSpace(name) == "" {
		name = base
	}
	u, err := s.CreateUser(ctx, CreateParams{Username: base, Name: name, Role: role, Identity: c.params(), InviteID: inviteID, Setup: setup, RetryUsername: true})
	var ae *Error
	if errors.As(err, &ae) && ae.Kind == Conflict && ae == errInUse {
		return nil, oidcErr("identity_in_use")
	}
	return u, err
}

// usernameFrom derives a valid username from the claims.
func usernameFrom(c claimed) string {
	src := c.preferred
	if src == "" {
		src, _, _ = strings.Cut(c.email, "@")
	}
	var b []byte
	for _, r := range src {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b = append(b, byte(r))
		case r >= 'A' && r <= 'Z':
			b = append(b, byte(r)+'a'-'A')
		default:
			b = append(b, '-')
		}
	}
	out := strings.TrimLeft(string(b), "._-")
	if len(out) > 32 {
		out = out[:32]
	}
	if len(out) < 2 {
		out = "user"
	}
	return out
}

func sanitizeLog(s string) string {
	if len(s) > 100 {
		s = s[:100]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s)
}

func oidcReason(err error) string {
	switch {
	case errors.Is(err, authoidc.ErrExchange):
		return "code exchange"
	case errors.Is(err, authoidc.ErrInvalidToken):
		return "id token"
	case errors.Is(err, authoidc.ErrNonceMismatch):
		return "nonce"
	case errors.Is(err, authoidc.ErrInvalidState):
		return "state"
	case errors.Is(err, authoidc.ErrDiscovery):
		return "discovery"
	case errors.Is(err, authoidc.ErrInvalidConfig):
		return "configuration"
	}
	return "other"
}
