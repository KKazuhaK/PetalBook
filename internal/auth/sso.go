package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	authoidc "github.com/KazuhaHub/authcore/oidc"
	authsaml "github.com/KazuhaHub/authcore/saml"
	crewjamsaml "github.com/crewjam/saml"
	"golang.org/x/oauth2"

	"petalbook.app/server/internal/store"
)

func canceled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// --- OpenID Connect runtime ----------------------------------------------------

// discoveryTransport serves the stored discovery document without asking the
// network, or records the one that comes back.
type discoveryTransport struct {
	base   http.RoundTripper
	url    string
	cached string
	mu     sync.Mutex
	got    string
}

func (t *discoveryTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.String() == t.url {
		if t.cached != "" {
			return &http.Response{StatusCode: 200, Status: "200 OK", Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
				Header:        http.Header{"Content-Type": {"application/json"}},
				Body:          io.NopCloser(strings.NewReader(t.cached)),
				ContentLength: int64(len(t.cached)), Request: r}, nil
		}
		resp, err := t.base.RoundTrip(r)
		if err != nil || resp.StatusCode != http.StatusOK {
			return resp, err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, metadataLimit+1))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if len(body) <= metadataLimit {
			t.mu.Lock()
			t.got = string(body)
			t.mu.Unlock()
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
		resp.ContentLength = int64(len(body))
		return resp, nil
	}
	return t.base.RoundTrip(r)
}

func (s *Service) discoveryClient(owner, issuer, cached string) (*http.Client, *discoveryTransport) {
	c := *s.httpClientFor(owner)
	base := c.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	t := &discoveryTransport{base: base, url: strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration", cached: cached}
	c.Transport = t
	return &c, t
}

// buildOIDC makes the library's provider; with a stored discovery document
// nothing is fetched.
func (s *Service) buildOIDC(ctx context.Context, owner string, cfg provCfg, secret, redirect, cached string) (*authoidc.Provider, string, error) {
	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{"profile", "email"}
	}
	client, tr := s.discoveryClient(owner, cfg.Issuer, cached)
	p, err := authoidc.New(context.WithoutCancel(ctx), authoidc.Config{IssuerURL: cfg.Issuer, ClientID: cfg.ClientID, ClientSecret: secret,
		RedirectURL: redirect, Scopes: scopes, HTTPClient: client})
	if err != nil {
		return nil, "", err
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return p, tr.got, nil
}

// discover fetches and checks the discovery document and returns it.
func (s *Service) discover(ctx context.Context, owner string, cfg provCfg, secret, redirect string) (string, error) {
	_, doc, err := s.buildOIDC(ctx, owner, cfg, secret, redirect, "")
	return doc, err
}

// oidcFor returns the provider's client, making it on first use (from the
// stored document, so without the network) and keeping it; after a failure it
// waits a few seconds before trying again.
func (s *Service) oidcFor(ctx context.Context, rt *providerRT) (*authoidc.Provider, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.secretErr != nil {
		return nil, rt.secretErr
	}
	secret, cached := rt.secret, rt.row.Cache
	if rt.row.Template != "" {
		// The addresses are the row's, copied from the template: nothing is
		// discovered.
		cached = templateDoc(rt.cfg)
	}
	if rt.row.Template == TemplateApple {
		// Apple's secret is signed here, and replaced as it nears its end:
		// a new one makes a new client (the state of an attempt under way
		// does not depend on the secret, so it still completes).
		if rt.apple == nil {
			return nil, errors.New("sign in with Apple has no key")
		}
		v, _, err := rt.apple.at(s.now())
		if err != nil {
			return nil, err
		}
		secret = v
	}
	if rt.oidc != nil && secret == rt.oidcSecret {
		return rt.oidc, nil
	}
	if rt.err != nil && s.now().Sub(rt.tried) < 5*time.Second {
		return nil, rt.err
	}
	p, doc, err := s.buildOIDC(ctx, rt.row.Owner, rt.cfg, secret, s.redirectURL(rt), cached)
	rt.tried = s.now()
	if err != nil {
		rt.err = err
		rt.statusOK, rt.statusChecked, rt.statusMessage = false, s.now().UTC(), oidcReason(err)
		return nil, err
	}
	rt.oidc, rt.oidcSecret, rt.err = p, secret, nil
	if doc != "" && rt.row.Template == "" {
		// Reached the provider just now: keep what it said for the next start.
		rt.statusOK, rt.statusChecked, rt.statusMessage = true, s.now().UTC(), ""
		go s.storeDiscovery(rt.row.ID, doc, rt.statusChecked)
	}
	return p, nil
}

func (s *Service) storeDiscovery(id, doc string, at time.Time) {
	if err := store.SetProviderStatus(context.Background(), s.st.Q(), id, true, at, "", doc, false); err != nil {
		s.log.Warn("could not store the discovery document", "provider", id, "error", err)
	}
}

// WarmProviders reaches the OpenID Connect providers that have no stored
// document yet, once, so a wrong issuer shows in the log at start-up. A
// failure is not fatal.
func (s *Service) WarmProviders(ctx context.Context) {
	for _, rt := range s.provs.all() {
		if rt.row.Kind != KindOIDC || !rt.row.Enabled || rt.row.Cache != "" {
			continue
		}
		if _, err := s.oidcFor(ctx, rt); err != nil {
			s.log.Warn("could not reach the OpenID Connect provider yet; will try again at the next sign-in", "provider", rt.row.Slug, "issuer", rt.cfg.Issuer, "reason", oidcReason(err))
			continue
		}
		s.log.Info("OpenID Connect provider ready", "provider", rt.row.Slug, "issuer", rt.cfg.Issuer)
	}
}

// --- SAML runtime ---------------------------------------------------------------

func (s *Service) samlFor(rt *providerRT) (*authsaml.Provider, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.saml != nil {
		return rt.saml, nil
	}
	if rt.secretErr != nil {
		return nil, rt.secretErr
	}
	meta, err := parseIDPMetadata(rt.row.Cache)
	if err != nil {
		return nil, err
	}
	key, cert, err := parseSAMLKey(rt.secret, rt.cfg.Certificate)
	if err != nil {
		return nil, err
	}
	entity, acs, _ := s.samlURLs(rt.row.Slug)
	if rt.cfg.EntityID != "" {
		entity = rt.cfg.EntityID
	}
	p, err := authsaml.New(authsaml.Config{EntityID: entity, ACSURL: acs, Key: key, Certificate: cert, SignAuthnRequests: rt.cfg.SignRequests,
		IDPMetadata: meta, AllowIDPInitiated: rt.cfg.AllowIDPInitiated, NameIDFormat: crewjamsaml.NameIDFormat(rt.cfg.NameIDFormat),
		ReplayCache: s.replay, StrictAttributes: true, Clock: s.now})
	if err != nil {
		return nil, err
	}
	rt.saml, rt.samlMeta = p, meta
	return p, nil
}

// SAMLMetadata is the service provider metadata of a provider (served even
// when the provider is not enabled yet).
func (s *Service) SAMLMetadata(slug string) ([]byte, error) {
	rt := s.provs.get(slug)
	if rt == nil || rt.row.Kind != KindSAML {
		return nil, errf(NotFound, "No such provider.")
	}
	p, err := s.samlFor(rt)
	if err != nil {
		return nil, errf(Unavailable, "The provider's metadata is not available.")
	}
	// Only the signing key is offered: encrypted assertions are not supported
	// (and are refused), and a provider that is shown an encryption key uses it.
	md := p.Metadata()
	for i := range md.SPSSODescriptors {
		var keep []crewjamsaml.KeyDescriptor
		for _, k := range md.SPSSODescriptors[i].KeyDescriptors {
			if k.Use != "encryption" {
				keep = append(keep, k)
			}
		}
		md.SPSSODescriptors[i].KeyDescriptors = keep
	}
	return xml.MarshalIndent(md, "", "  ")
}

// --- starting ------------------------------------------------------------------

// BeginParams is a request to start a sign-in.
type BeginParams struct {
	Intent string // login (default), setup, invite or link
	Token  string
	Return string
	// User is the signed-in user, for link.
	User *store.User
	// LoginHint is the address the person gave (the sign-in page after a
	// session ended, or the address typed first, 3.6 d4). An OpenID Connect
	// provider gets it as `login_hint`, a SAML provider whose NameID is the
	// address as the request's Subject; GitHub has no such parameter.
	LoginHint string
}

// withLoginHint adds `login_hint` to an authorization address, if the hint is
// a plausible one (one line, not long). Anything else leaves the address as it is.
func withLoginHint(authURL, hint string) string {
	if hint == "" || len(hint) > 254 || strings.ContainsFunc(hint, unicode.IsControl) {
		return authURL
	}
	u, err := url.Parse(authURL)
	if err != nil {
		return authURL
	}
	q := u.Query()
	q.Set("login_hint", hint)
	u.RawQuery = q.Encode()
	return u.String()
}

// withParam sets one parameter of an authorization address.
func withParam(authURL, k, v string) string {
	u, err := url.Parse(authURL)
	if err != nil {
		return authURL
	}
	q := u.Query()
	q.Set(k, v)
	u.RawQuery = q.Encode()
	return u.String()
}

// TakesFormPost says whether the site provider with the slug answers by a
// form posted to its callback (Apple): only then is that post taken.
func (s *Service) TakesFormPost(slug string) bool {
	rt := s.provs.get(slug)
	return rt != nil && rt.row.Kind == KindOIDC && rt.cfg.ResponseMode == "form_post"
}

// KeepFormPost keeps what a provider posted beside its answer (Apple's user
// field: the name, at the first sign-in only) with the attempt of the state,
// to be read when the callback takes it. Only the name is kept; the address
// in it is not (the ID token says which one is the person's). Nothing of it
// goes into the address the browser is sent on to.
func (s *Service) KeepFormPost(slug, state, user string) {
	if !s.TakesFormPost(slug) || state == "" {
		return
	}
	if name := appleUserName(user); name != "" {
		s.pending.noteFormName(slug, state, clip(name, 100))
	}
}

// BeginResult is where to send the browser and the cookie that ties the
// attempt to it.
type BeginResult struct {
	URL    string
	Cookie *http.Cookie
	// Binding is set too for a SAML link: the host-only cookie that ties the
	// attempt to the signed-in browser.
	Binding *http.Cookie
}

func (s *Service) ssoCookie(binding string) *http.Cookie {
	return s.newCookie(s.SSOCookieName(), binding, int(pendingTTL/time.Second))
}

// SSOCookieName is the binding cookie of OpenID Connect and GitHub attempts,
// with the same prefix rule as the session cookie.
func (s *Service) SSOCookieName() string { return s.cookieName("petalbook_oidc") }

// ClearSSOCookie removes the binding cookie.
func (s *Service) ClearSSOCookie() *http.Cookie { return s.newCookie(s.SSOCookieName(), "", -1) }

// samlCookieName is __Secure-petalbook_saml_<slug>: the post to the ACS comes
// from the identity provider's site, so the cookie needs SameSite=None.
func samlCookieName(slug string) string { return "__Secure-petalbook_saml_" + slug }

func (s *Service) samlCookie(slug, binding string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: samlCookieName(slug), Value: binding, Path: "/api/auth/saml/" + slug + "/acs", MaxAge: maxAge,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteNoneMode}
}

// samlLinkCookie is __Host-petalbook_saml_link_<slug>, set when a signed-in
// user starts linking a SAML identity. The attempt cookie is __Secure-, which a
// sibling subdomain can set; a __Host- cookie it cannot, so the ACS knows the
// post comes from the browser that asked. The post is cross-site: SameSite=None.
func (s *Service) samlLinkCookie(slug, value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: "__Host-petalbook_saml_link_" + slug, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteNoneMode}
}

// SAMLCookieName is the cookie name of a provider's attempts.
func SAMLCookieName(slug string) string { return samlCookieName(slug) }

// LinkBegin starts linking the signed-in user to an account at a provider (by
// the provider's id; a site provider's id is its slug). A tenant's provider
// links only its own tenant's people (3.6 d3); a provider the person may not
// use is answered as one that is not there.
func (s *Service) LinkBegin(ctx context.Context, providerID string, u *store.User, ret, ip string) (*BeginResult, error) {
	rt := s.provs.getID(providerID)
	if rt == nil || !s.signInOn(ctx, rt) {
		return nil, errf(NotFound, "Single sign-on is not configured.")
	}
	if err := s.mayUse(ctx, rt, *u); err != nil {
		var se *ssoError
		if errors.As(err, &se) {
			return nil, errf(NotFound, "Single sign-on is not configured.")
		}
		return nil, err
	}
	return s.SSOBegin(ctx, rt.row.Kind, rt.row.Slug, BeginParams{Intent: "link", Return: ret, User: u}, ip)
}

// SSOBegin starts a sign-in with a provider.
func (s *Service) SSOBegin(ctx context.Context, kind, slug string, p BeginParams, ip string) (*BeginResult, error) {
	ctx, note := noteOutbound(ctx)
	rt := s.provs.get(slug)
	if rt == nil || rt.row.Kind != kind {
		return nil, errf(NotFound, "Single sign-on is not configured.")
	}
	// A provider that is off answers only the test an administrator runs on
	// it; so does a tenant's whose tenant lost features.ownSso or its domains.
	if p.Intent != "test" && !s.signInOn(ctx, rt) {
		return nil, errf(NotFound, "Single sign-on is not configured.")
	}
	// A tenant's own provider makes no first administrator of the server.
	if p.Intent == "setup" && rt.row.Owner != store.ProviderSite {
		return nil, errf(NotFound, "Single sign-on is not configured.")
	}
	switch p.Intent {
	case "":
		p.Intent = "login"
	case "login", "setup", "invite":
	case "test":
		if p.User == nil {
			return nil, errf(Unauthenticated, "Sign in to test a provider.")
		}
	case "link":
		if p.User == nil {
			return nil, errf(Unauthenticated, "Sign in to link an account.")
		}
		// A link that only connects repositories (the GitHub row's) may be made
		// to sign in too, with the same account.
		if l, err := store.FindUserLink(ctx, s.st.Q(), rt.row.ID, p.User.ID); err == nil {
			if l.SignIn {
				return nil, errAlreadyLinked
			}
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
	default:
		return nil, errf(Invalid, "Intent is login, setup, invite, link or test.")
	}
	if len(p.Token) > 256 {
		return nil, errf(Invalid, "The token is too long.")
	}
	if d, ok := s.starts.Take(ip); !ok {
		return nil, retryAfter(d)
	}
	e := pendingEntry{kind: kind, slug: rt.row.Slug, intent: p.Intent, token: p.Token, ret: SafeReturn(p.Return)}
	if p.User != nil {
		e.userID = p.User.ID
	}
	unavailable := func(err error) error {
		if wait, capped := outboundOf(note, err); capped {
			return retryAfter(wait)
		}
		s.log.Error("sign-in provider unavailable", "provider", rt.row.Slug, "kind", kind, "reason", providerReason(err))
		return errf(Unavailable, "The single sign-on provider cannot be reached right now.")
	}
	switch kind {
	case KindOIDC:
		var az authoidc.Authorization
		if multiTenant(rt.cfg.Issuer) {
			tf, err := s.tenantFlowFor(rt)
			if err != nil {
				return nil, unavailable(err)
			}
			if az, err = tf.begin(); err != nil {
				return nil, unavailable(err)
			}
		} else {
			prov, err := s.oidcFor(ctx, rt)
			if err != nil {
				return nil, unavailable(err)
			}
			if az, err = prov.Begin(); err != nil {
				return nil, unavailable(err)
			}
		}
		e.state, e.stateValue = az.State, az.State.Value
		u := withLoginHint(az.URL, p.LoginHint)
		if rt.cfg.ResponseMode == "form_post" {
			// Apple posts its answer (it must, when the name or the address
			// is asked for); the post is turned back into the callback here.
			u = withParam(u, "response_mode", "form_post")
		}
		return &BeginResult{URL: u, Cookie: s.ssoCookie(s.pending.put(e))}, nil
	case KindGitHub:
		if rt.secretErr != nil {
			return nil, unavailable(rt.secretErr)
		}
		e.stateValue, e.verifier = randomToken(), oauth2.GenerateVerifier()
		cfg := s.githubOAuth(rt)
		return &BeginResult{URL: cfg.AuthCodeURL(e.stateValue, oauth2.S256ChallengeOption(e.verifier)), Cookie: s.ssoCookie(s.pending.put(e))}, nil
	case KindSAML:
		prov, err := s.samlFor(rt)
		if err != nil {
			return nil, unavailable(err)
		}
		binding := randomToken()
		// The address goes as the request's Subject only to a provider whose
		// NameID is the address (emailAddress): an identity provider that
		// honours a Subject authenticates that subject alone (3.6 d4).
		var opts authsaml.AuthnOptions
		if p.LoginHint != "" && rt.cfg.NameIDFormat == string(crewjamsaml.EmailAddressNameIDFormat) {
			opts.SubjectHint = p.LoginHint
		}
		req, err := prov.NewAuthnRequestWith(binding, opts)
		if err != nil {
			return nil, unavailable(err)
		}
		e.samlID = req.ID
		out := &BeginResult{URL: req.RedirectURL, Cookie: s.samlCookie(rt.row.Slug, binding, int(pendingTTL/time.Second))}
		if e.intent == "link" || e.intent == "test" {
			tie := randomToken()
			e.linkBinding = hashToken(tie)
			out.Binding = s.samlLinkCookie(rt.row.Slug, tie, int(pendingTTL/time.Second))
		}
		s.pending.putAs(binding, e)
		return out, nil
	}
	return nil, errf(NotFound, "Single sign-on is not configured.")
}

// --- finishing -------------------------------------------------------------------

// Outcome is how a callback ends: always a redirect.
type Outcome struct {
	Redirect string
	// Session is the cookie value of a new session; "" when none was started.
	Session string
	// Clear lists the cookies to remove: the sign-in is over, whatever the result.
	Clear []*http.Cookie
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
	switch intent {
	case "link":
		base = "/settings"
	case "test":
		base = "/admin/security/providers"
	}
	return base + "?sso_error=" + code
}

// tenantTestPage is where a test sign-in of a tenant's own provider goes back
// to: the organisation console's sign-in tab.
const tenantTestPage = "/admin/settings/signin"

// ssoFailFor is ssoFail for a sign-in refused once its account was found
// (not_allowed): the audit row names the account and its tenant.
func (s *Service) ssoFailFor(kind, slug, intent, code, ip string, u *store.User, clear ...*http.Cookie) Outcome {
	if intent == "test" {
		return Outcome{Redirect: failURL(intent, code), Clear: clear}
	}
	s.Audit(AuditEvent{Kind: "login_failed", Tenant: u.TenantID, User: u.ID, Username: u.Username, IP: ip,
		Detail: map[string]any{"method": kind, "reason": code, "provider": slug}})
	return Outcome{Redirect: failURL(intent, code), Clear: clear}
}

// ssoFail is the outcome of a sign-in that did not work, and the audit row for it.
func (s *Service) ssoFail(kind, slug, intent, code, ip string, clear ...*http.Cookie) Outcome {
	// Only a sign-in that was really under way (it has an intent) is written
	// down: anyone can send a callback that matches nothing, and the log is not
	// for them to fill.
	rt := s.provs.get(slug)
	if intent != "" && intent != "test" && rt != nil {
		s.Audit(AuditEvent{Kind: "login_failed", Tenant: auditTenant(rt.row.Owner), IP: ip, Detail: map[string]any{"method": kind, "reason": code, "provider": slug}})
	}
	if intent == "test" && rt != nil && rt.row.Owner != store.ProviderSite {
		return Outcome{Redirect: tenantTestPage + "?sso_error=" + code, Clear: clear}
	}
	return Outcome{Redirect: failURL(intent, code), Clear: clear}
}

// OIDCComplete finishes a sign-in from the redirect of an OpenID Connect or
// GitHub provider. binding is the value of
// the browser's binding cookie; sessionToken the browser's session cookie (for
// link). It never returns an error: every failure is a redirect to a page that
// can explain it.
func (s *Service) OIDCComplete(ctx context.Context, slug, binding, sessionToken string, q url.Values, ip string) Outcome {
	ctx, note := noteOutbound(ctx)
	rt := s.provs.get(slug)
	kind := KindOIDC
	if rt != nil {
		kind = rt.row.Kind
		slug = rt.row.Slug
	}
	clear := []*http.Cookie{s.ClearSSOCookie()}
	if rt == nil || kind == KindSAML {
		return s.ssoFail(kind, slug, "", "provider_unavailable", ip)
	}
	// A provider that is off answers only the test an administrator ran on it.
	off := !s.signInOn(ctx, rt)
	if off && binding == "" {
		return s.ssoFail(kind, slug, "", "provider_unavailable", ip)
	}
	state, ok1 := oneParam(q, "state")
	code, ok2 := oneParam(q, "code")
	idpErr, ok3 := oneParam(q, "error")
	if !ok1 || !ok2 || !ok3 {
		s.log.Warn("sign-in refused: repeated callback parameters")
		return s.ssoFail(kind, slug, "", "idp_error", ip)
	}
	if idpErr != "" {
		// Only the browser that started the attempt can end it.
		intent := ""
		if binding != "" && state != "" {
			if e, res := s.pending.take(binding, state); res == takeOK && e.slug == slug {
				intent = e.intent
			}
		}
		if off && intent != "test" {
			return s.ssoFail(kind, slug, "", "provider_unavailable", ip)
		}
		s.log.Warn("sign-in failed at the provider", "provider", slug, "error", sanitizeLog(idpErr))
		if intent == "" {
			return s.ssoFail(kind, slug, "", "idp_error", ip)
		}
		return s.ssoFail(kind, slug, intent, "idp_error", ip, clear...)
	}
	if state == "" || code == "" {
		return s.ssoFail(kind, slug, "", "idp_error", ip)
	}
	if binding == "" {
		return s.ssoFail(kind, slug, "", "expired", ip)
	}
	e, res := s.pending.take(binding, state)
	switch res {
	case takeUnknown:
		s.log.Warn("sign-in refused: unknown, expired or replayed state")
		return s.ssoFail(kind, slug, "", "expired", ip, clear...)
	case takeBinding:
		// The real attempt stays: a forged callback must not cancel it.
		s.log.Warn("sign-in refused: the state does not belong to this browser")
		return s.ssoFail(kind, slug, "", "idp_error", ip)
	}
	if e.slug != slug || e.kind != kind {
		return s.ssoFail(kind, slug, e.intent, "expired", ip, clear...)
	}
	if off && e.intent != "test" {
		return s.ssoFail(kind, slug, "", "provider_unavailable", ip, clear...)
	}
	fail := func(code string) Outcome { return s.ssoFail(kind, slug, e.intent, code, ip, clear...) }
	var c claimed
	switch kind {
	case KindOIDC:
		var sub string
		var raw map[string]json.RawMessage
		if multiTenant(rt.cfg.Issuer) {
			tf, err := s.tenantFlowFor(rt)
			if err != nil {
				s.log.Error("OpenID Connect provider unavailable", "provider", slug, "reason", oidcReason(err))
				return fail("provider_unavailable")
			}
			v, err := tf.complete(ctx, state, code, e.state)
			if err != nil {
				reason := oidcReason(err)
				if errors.Is(err, errTenantIssuer) {
					reason = "the token's issuer is not the one of its tenant (tid)"
				}
				s.log.Warn("sign-in failed: the code or ID token was rejected", "provider", slug, "reason", reason)
				return fail("idp_error")
			}
			sub, raw = v.subject, v.claims
		} else {
			prov, err := s.oidcFor(ctx, rt)
			if err != nil {
				if _, capped := outboundOf(note, err); capped {
					return fail("rate_limited")
				}
				s.log.Error("OpenID Connect provider unavailable", "provider", slug, "reason", oidcReason(err))
				return fail("provider_unavailable")
			}
			a, err := prov.Complete(ctx, state, code, e.state)
			if err != nil {
				// The organisation's outbound share is used up: nothing was asked.
				if _, capped := outboundOf(note, err); capped {
					return fail("rate_limited")
				}
				s.log.Warn("sign-in failed: the code or ID token was rejected", "provider", slug, "reason", oidcReason(err))
				return fail("idp_error")
			}
			if a.Claims(&raw) != nil {
				s.log.Warn("sign-in failed: unusable claims", "provider", slug, "reason", "the claims are not a JSON object")
				return fail("idp_error")
			}
			sub = a.Subject
		}
		var err error
		if c, err = identityFromClaims(rt, sub, raw); err != nil {
			s.log.Warn("sign-in failed: unusable claims", "provider", slug, "reason", err.Error())
			return fail("idp_error")
		}
		if c.name == "" && rt.row.Template == TemplateApple {
			// Apple sends the name once, beside the token (the form's user),
			// kept with the attempt (KeepFormPost), never in an address.
			c.name = e.formName
		}
	case KindGitHub:
		var errCode string
		c, errCode = s.githubClaims(ctx, rt, code, e.verifier)
		if errCode != "" {
			return fail(errCode)
		}
	}
	return s.finishSSO(ctx, rt, e, c, sessionToken, ip, clear)
}

// SAMLComplete finishes a sign-in from the form the identity provider posted
// to the ACS. r has its body limited already.
func (s *Service) SAMLComplete(ctx context.Context, slug string, r *http.Request, sessionToken, ip string) Outcome {
	if err := s.allow(ctx, ip); err != nil {
		return Outcome{Redirect: failURL("", "rate_limited")}
	}
	rt := s.provs.get(slug)
	if rt == nil || rt.row.Kind != KindSAML {
		return s.ssoFail(KindSAML, slug, "", "provider_unavailable", ip)
	}
	// A provider that is off answers only the test an administrator ran on it.
	off := !s.signInOn(ctx, rt)
	clear := []*http.Cookie{s.samlCookie(slug, "", -1)}
	linkTie := ""
	if ck, err := r.Cookie(s.samlLinkCookie(slug, "", 0).Name); err == nil {
		linkTie = ck.Value
		clear = append(clear, s.samlLinkCookie(slug, "", -1))
	}
	if err := r.ParseForm(); err != nil {
		return s.ssoFail(KindSAML, slug, "", "saml_assertion_rejected", ip)
	}
	relay := r.PostForm.Get("RelayState")
	cookieVal := ""
	if ck, err := r.Cookie(samlCookieName(slug)); err == nil {
		cookieVal = ck.Value
	}
	if off && cookieVal == "" {
		return s.ssoFail(KindSAML, slug, "", "provider_unavailable", ip)
	}
	prov, err := s.samlFor(rt)
	if err != nil {
		s.log.Error("SAML provider unavailable", "provider", slug, "reason", providerReason(err))
		return s.ssoFail(KindSAML, slug, "", "provider_unavailable", ip)
	}
	var e *pendingEntry
	var ids []string
	switch {
	case cookieVal != "" || relay != "":
		var res takeResult
		if cookieVal == "" {
			return s.ssoFail(KindSAML, slug, "", "expired", ip)
		}
		e, res = s.pending.take(cookieVal, relay)
		switch res {
		case takeUnknown:
			return s.ssoFail(KindSAML, slug, "", "expired", ip, clear...)
		case takeBinding:
			// RelayState is not the cookie: not the browser that started it.
			return s.ssoFail(KindSAML, slug, "", "expired", ip)
		}
		if e.slug != slug || e.kind != KindSAML {
			return s.ssoFail(KindSAML, slug, "", "expired", ip, clear...)
		}
		// A link or a test is answered only in the browser that started it.
		if (e.intent == "link" || e.intent == "test") && (e.linkBinding == "" || linkTie == "" || !ctEqual(hashToken(linkTie), e.linkBinding)) {
			return s.ssoFail(KindSAML, slug, e.intent, "expired", ip, clear...)
		}
		ids = []string{e.samlID}
	case rt.cfg.AllowIDPInitiated:
		e = &pendingEntry{kind: KindSAML, slug: slug, intent: "login", ret: "/"}
	default:
		return s.ssoFail(KindSAML, slug, "", "expired", ip)
	}
	if off && e.intent != "test" {
		return s.ssoFail(KindSAML, slug, "", "provider_unavailable", ip, clear...)
	}
	fail := func(code string) Outcome { return s.ssoFail(KindSAML, slug, e.intent, code, ip, clear...) }
	_, acs, _ := s.samlURLs(slug)
	acsURL, _ := url.Parse(acs)
	r2 := r.Clone(ctx)
	r2.URL = acsURL
	a, err := prov.ValidateResponse(ctx, r2, ids)
	if err != nil {
		code := samlCode(err)
		s.log.Warn("SAML response refused", "provider", slug, "code", code, "reason", sanitizeLog(err.Error()))
		return fail(code)
	}
	if a.IsTransient() {
		return fail("saml_transient_nameid")
	}
	if strings.TrimSpace(a.NameID) == "" {
		return fail("saml_no_nameid")
	}
	if a.Issuer != "" && a.Issuer != rt.cfg.IDPEntityID {
		return fail("saml_assertion_rejected")
	}
	c, err := samlClaims(rt, a)
	if errors.Is(err, errSAMLNoEmail) {
		return fail("saml_no_email")
	} else if err != nil {
		return fail("saml_assertion_rejected")
	}
	return s.finishSSO(ctx, rt, e, c, sessionToken, ip, clear)
}

func samlCode(err error) string {
	switch {
	case errors.Is(err, authsaml.ErrMissingDestination), errors.Is(err, authsaml.ErrDestinationMismatch):
		return "saml_destination"
	case errors.Is(err, authsaml.ErrWeakSignatureAlgorithm):
		return "saml_weak_signature"
	case errors.Is(err, authsaml.ErrEncryptedAssertionNotAllowed):
		return "saml_encrypted"
	case errors.Is(err, authsaml.ErrReplayed):
		return "saml_replay"
	case errors.Is(err, authsaml.ErrTooManyAssertions):
		return "saml_multiple_assertions"
	case errors.Is(err, authsaml.ErrDuplicateAttribute):
		return "saml_attributes"
	case errors.Is(err, authsaml.ErrReplayCacheFull):
		return "internal"
	}
	return "saml_assertion_rejected"
}

// errSAMLNoEmail: the assertion carries no address: neither in the attribute the
// provider names for it nor, with a NameID in the emailAddress format, as the NameID.
var errSAMLNoEmail = errors.New("the assertion has no email address")

// samlNameIDEmail is the NameID format that says the NameID is an address.
const samlNameIDEmail = "urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress"

func looksLikeEmail(v string) bool {
	i := strings.IndexByte(v, '@')
	return i > 0 && i < len(v)-1 && strings.Count(v, "@") == 1
}

func samlClaims(rt *providerRT, a *authsaml.Assertion) (claimed, error) {
	sub := strings.TrimSpace(a.NameID)
	if len(sub) > 255 || !utf8.ValidString(sub) || strings.ContainsFunc(sub, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return claimed{}, errors.New("the NameID is not usable")
	}
	at := rt.cfg.Attributes
	attr := func(name string) string {
		if name == "" {
			return ""
		}
		return clip(a.Attribute(name), 254)
	}
	// The address is the account's UPN, so it is required: from the attribute
	// the provider names (mail by default), else the NameID, only when its
	// format says it is an address. The NameID itself is the subject either way
	// and need not be the address (a persistent one is recommended: an address
	// can change).
	emailAttr := at.Email
	if emailAttr == "" {
		emailAttr = samlDefaultEmailAttr
	}
	email := attr(emailAttr)
	if !looksLikeEmail(email) {
		email = ""
		if a.NameIDFormat == samlNameIDEmail && looksLikeEmail(sub) {
			email = clip(sub, 254)
		}
	}
	if email == "" {
		return claimed{}, errSAMLNoEmail
	}
	c := claimed{subject: sub, name: attr(at.Name), preferred: attr(at.Username), email: email, src: emailSource{template: tmplSAML}}
	c.labelOverride = email
	if g, on := rt.groupsRead(); on {
		c.groups = samlGroupClaim(g, a.Attributes)
	}
	return c, nil
}

// --- who signed in -----------------------------------------------------------------

// claimed is the verified identity and what the provider says about it.
type claimed struct {
	// subject is the account's id at the provider (the OIDC sub, the SAML
	// NameID, the GitHub user id). With the provider's own id it is all that
	// finds the account: the issuer plays no part.
	subject   string
	name      string
	preferred string
	email     string
	// src is what the sign-in said that the trust rules read; finishSSO adds
	// what the provider's configuration says (trustSource).
	src emailSource
	// emailVerified: the sign-in vouches for the address (trustsEmail).
	emailVerified bool
	// providerID is the id of the provider the sign-in went through.
	providerID string
	// provider is the slug of the provider (verified_by of an address it
	// vouched for).
	provider string
	// labelOverride, when set, is the label shown for the identity.
	labelOverride string
	// groups is what the sign-in said about the person's groups (3.2).
	groups groupClaim
}

func (c claimed) label() string {
	if c.labelOverride != "" {
		return c.labelOverride
	}
	if c.email != "" {
		return c.email
	}
	return c.preferred
}

func (c claimed) params() *IdentityParams {
	return &IdentityParams{ProviderID: c.providerID, Subject: c.subject, Label: c.label(), Email: c.email, EmailTrusted: c.emailVerified}
}

// identityFromClaims reads who signed in from a verified ID token's subject
// and claims.
func identityFromClaims(rt *providerRT, subject string, raw map[string]json.RawMessage) (claimed, error) {
	if raw == nil {
		return claimed{}, errors.New("the claims are not a JSON object")
	}
	str := func(k string) string {
		var v string
		if json.Unmarshal(raw[k], &v) != nil {
			return ""
		}
		return v
	}
	verified := false
	var vb bool
	if json.Unmarshal(raw["email_verified"], &vb) == nil {
		verified = vb
	} else if str("email_verified") == "true" {
		verified = true
	}
	sub := strings.TrimSpace(subject)
	if sub == "" || len(sub) > 255 || !utf8.ValidString(sub) || strings.ContainsFunc(sub, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return claimed{}, errors.New("the sub claim is empty or not usable")
	}
	// The token's issuer was checked against the configured one (a
	// multi-tenant provider's against its tid); the account is found by the
	// provider's id and sub, not by the issuer.
	email := clip(str("email"), 254)
	c := claimed{subject: sub, name: str("name"), preferred: str("preferred_username"), email: email,
		src: emailSource{template: trustTemplate(rt.row), verified: verified, claims: raw}}
	if rt.row.Template == TemplateApple {
		c.src.relay = isAppleRelay(email) || claimTrue(raw, "is_private_email")
	}
	if g, on := rt.groupsRead(); on {
		c.groups = oidcGroupClaim(g, raw)
	}
	return c, nil
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

// ssoError is a sign-in refused with an sso_error code; user is the account
// it found, when it found one (its audit row names it).
type ssoError struct {
	code string
	user *store.User
}

func (e *ssoError) Error() string { return e.code }

func ssoErr(code string) error { return &ssoError{code: code} }

// notAllowed is the refusal of a sign-in through a provider the account may
// not use (mayUse): sso_error=not_allowed, audited with the account.
func notAllowed(u *store.User) error { return &ssoError{code: "not_allowed", user: u} }

// finishSSO turns a verified identity into what the intent asked for and ends
// the attempt with a redirect.
func (s *Service) finishSSO(ctx context.Context, rt *providerRT, e *pendingEntry, c claimed, sessionToken, ip string, clear []*http.Cookie) Outcome {
	kind, slug := rt.row.Kind, rt.row.Slug
	c.provider, c.providerID = slug, rt.row.ID
	c.emailVerified = trustsEmail(s.trustSource(ctx, rt, c.src, c.email))
	var user *store.User
	var created bool
	var err error
	// The groups allowlist is asked at every sign-in, not only when an
	// account is made: who the identity provider takes out of the groups
	// signs in no more.
	if (e.intent == "login" || e.intent == "invite") && !rt.allowedByGroups(c.groups) {
		return s.ssoFail(kind, slug, e.intent, "not_allowed", ip, clear...)
	}
	switch e.intent {
	case "login":
		user, created, err = s.ssoLogin(ctx, rt, c, ip)
	case "setup":
		user, err = s.ssoSetup(ctx, c, e.token)
		created = true
	case "invite":
		user, err = s.ssoInvite(ctx, rt, c, e.token)
		created = true
	case "link":
		err = s.ssoLink(ctx, rt, c, e.userID, sessionToken, ip)
	case "test":
		// Nothing is made, linked or signed in: what the provider said is kept
		// for the administrator who asked, to read once.
		s.keepTest(e.userID, rt.row.ID, c)
		return Outcome{Redirect: e.ret, Clear: clear}
	}
	if err != nil {
		var se *ssoError
		if errors.As(err, &se) {
			if se.user != nil {
				return s.ssoFailFor(kind, slug, e.intent, se.code, ip, se.user, clear...)
			}
			return s.ssoFail(kind, slug, e.intent, se.code, ip, clear...)
		}
		s.log.Error("sign-in failed", "error", err)
		return s.ssoFail(kind, slug, e.intent, "internal", ip, clear...)
	}
	out := Outcome{Redirect: e.ret, Clear: clear}
	switch e.intent {
	case "link":
		s.syncGroups(ctx, rt, e.userID, "", ip, c.groups)
		return out // the session stays as it is
	case "login", "invite":
		s.syncGroups(ctx, rt, user.ID, user.Username, ip, c.groups)
		// What the sync changed is asked again before a session starts: a
		// tenant's provider never signs in an account that holds a site key.
		if rt.row.Owner != store.ProviderSite {
			if err := s.mayUse(ctx, rt, *user); err != nil {
				var se *ssoError
				if errors.As(err, &se) && se.user != nil {
					return s.ssoFailFor(kind, slug, e.intent, se.code, ip, se.user, clear...)
				}
				s.log.Error("sign-in failed", "error", err)
				return s.ssoFail(kind, slug, e.intent, "internal", ip, clear...)
			}
		}
	}
	tok, err := s.issueSession(ctx, user.ID, false, "sso:"+slug)
	if err != nil {
		s.log.Error("could not start a session", "error", err)
		return s.ssoFail(kind, slug, e.intent, "internal", ip, clear...)
	}
	out.Session = tok
	s.log.Info("signed in", "user", user.ID, "method", kind, "provider", slug)
	s.Audit(AuditEvent{Kind: "login", Tenant: user.TenantID, User: user.ID, Username: user.Username, IP: ip, Detail: map[string]any{"method": kind, "provider": slug, "created": created}})
	return out
}

func (s *Service) ssoLogin(ctx context.Context, rt *providerRT, c claimed, ip string) (*store.User, bool, error) {
	ident, err := store.FindLink(ctx, s.st.Q(), rt.row.ID, c.subject)
	switch {
	case err == nil && !ident.SignIn:
		// The account is connected for repositories only: signing in with it was
		// never turned on, by the person, for this link ("use to sign in").
		return nil, false, ssoErr("not_for_signin")
	case err == nil:
		u, err := store.GetUser(ctx, s.st.Q(), store.AllTenants(), ident.UserID) // a link signs in to its account, whatever tenant
		if errors.Is(err, store.ErrNotFound) {
			// The person is in the recycle bin (their identities stay attached,
			// unusable): refused, not made a new account by autoCreate.
			return nil, false, ssoErr("disabled")
		}
		if err != nil {
			return nil, false, err
		}
		if u.Disabled || s.st.TenantDisabled(u.TenantID) {
			return nil, false, ssoErr("disabled")
		}
		// The link row is not trusted to keep to the provider's tenant: the
		// account it names is asked again (a row written across tenants
		// signs nobody in), as is everything else the provider may reach.
		if err := s.mayUse(ctx, rt, *u); err != nil {
			return nil, false, err
		}
		if err := store.TouchLink(ctx, s.st.Q(), ident, c.email, c.emailVerified, c.label(), s.now()); err != nil {
			s.log.Warn("could not record the sign-in on its linked account", "provider", rt.row.Slug, "error", err)
		}
		return u, false, nil
	case !errors.Is(err, store.ErrNotFound):
		return nil, false, err
	}
	// Nobody has this identity. It is never attached to an account by its
	// address: an address the sign-in vouches for that another account holds
	// verified (store.EmailRow.Holds: a tenant administrator's mark alone does
	// not count) sends the person to sign in to that account and link the
	// provider there (email_in_use). Making an account is opt-in, and never
	// the first: that is the administrator's, made with the setup token.
	if e, err := s.cleanEmailFor(ctx, c.email); err == nil && e != "" && c.emailVerified && matchesByAddress(c.src) {
		if row, err := store.EmailOwner(ctx, s.st.Q(), e); err == nil && row.Holds() {
			return nil, false, ssoErr("email_in_use")
		} else if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, false, err
		}
	}
	noUsers, _ := s.SetupOpen(ctx)
	if !rt.row.AutoCreate || noUsers {
		return nil, false, ssoErr("no_account")
	}
	if rt.row.Kind == KindGitHub && len(rt.cfg.Organizations) == 0 {
		return nil, false, ssoErr("no_account")
	}
	// A tenant's provider makes accounts in its own tenant only.
	tenant := rt.row.TenantID
	if rt.row.Owner != store.ProviderSite {
		tenant = rt.row.Owner
	}
	if s.st.TenantDisabled(tenant) { // the provider's tenant was disabled after it was set up
		return nil, false, ssoErr("no_account")
	}
	if !s.tenantMayUse(ctx, rt, tenant) {
		return nil, false, ssoErr("not_allowed")
	}
	u, err := s.createFromClaims(ctx, c, "member", "", false, tenant, false)
	return u, true, err
}

func (s *Service) ssoSetup(ctx context.Context, c claimed, token string) (*store.User, error) {
	if open, err := s.SetupOpen(ctx); err != nil {
		return nil, err
	} else if !open || !s.setupTokenOK(token) {
		return nil, ssoErr("setup_invalid")
	}
	u, err := s.createFromClaims(ctx, c, "admin", "", true, store.DefaultTenantID, true)
	var ae *Error
	if errors.As(err, &ae) && ae.Kind == Exists {
		return nil, ssoErr("setup_invalid")
	}
	return u, err
}

func (s *Service) ssoInvite(ctx context.Context, rt *providerRT, c claimed, token string) (*store.User, error) {
	inv, err := s.findInvite(ctx, token)
	var ae *Error
	if errors.As(err, &ae) {
		return nil, ssoErr("invite_invalid")
	} else if err != nil {
		return nil, err
	}
	// The account the invitation makes must be one the provider may sign in:
	// a tenant's provider makes no site administrator, nor anyone of another
	// tenant.
	if store.IsSiteAdminRole(inv.Role, inv.SiteAdmin) && rt.row.Owner != store.ProviderSite || !s.tenantMayUse(ctx, rt, inv.TenantID) {
		return nil, ssoErr("not_allowed")
	}
	u, err := s.createFromClaims(ctx, c, inv.Role, inv.ID, false, inv.TenantID, inv.SiteAdmin)
	if errors.As(err, &ae) && ae.Kind == NotFound {
		return nil, ssoErr("invite_invalid")
	}
	return u, err
}

func (s *Service) ssoLink(ctx context.Context, rt *providerRT, c claimed, userID, sessionToken, ip string) error {
	// The browser must still be signed in as the user who asked. A SAML
	// response comes in a post from the identity provider's site, where the
	// session cookie (SameSite=Lax) is not sent: there the attempt, which only
	// a signed-in user could start and which SAMLComplete has checked is tied
	// to this browser by its host-only binding cookie, says who asked.
	var p *Principal
	var err error
	if rt.row.Kind == KindSAML {
		u, e := store.GetUser(ctx, s.st.Q(), store.AllTenants(), userID)
		if e != nil || u.Disabled {
			return ssoErr("expired")
		}
		access, e := s.SubjectOf(ctx, *u)
		if e != nil {
			return ssoErr("expired")
		}
		p = &Principal{User: *u, Access: access}
	} else if p, err = s.Authenticate(ctx, sessionToken); err != nil || p.User.ID != userID {
		return ssoErr("expired")
	}
	// Only who may sign in through the provider links it: a tenant's own
	// provider, its tenant's people and never a site administrator.
	if err := s.mayUse(ctx, rt, p.User); err != nil {
		return err
	}
	if ident, err := store.FindLink(ctx, s.st.Q(), rt.row.ID, c.subject); err == nil {
		if ident.UserID != userID {
			return ssoErr("identity_in_use")
		}
		if ident.SignIn {
			return nil
		}
		// The person's own account, connected for repositories: it signs them in
		// now too, on the same link (no second link at the provider).
		if err := store.EnableLinkSignin(ctx, s.st.Q(), ident.ID, userID, c.email, c.emailVerified, c.label(), s.now()); err != nil {
			return err
		}
		s.Audit(AuditEvent{Kind: "identity_linked", User: userID, Username: p.User.Username, IP: ip, Detail: map[string]any{"provider": rt.row.Slug, "by": "user"}})
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	// One account per provider per person: to switch to another, unlink first.
	if _, err := store.FindUserLink(ctx, s.st.Q(), rt.row.ID, userID); err == nil {
		return ssoErr("already_linked")
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	err = store.InsertLink(ctx, s.st.Q(), store.Link{ID: s.newID(), ProviderID: rt.row.ID, UserID: userID, Subject: c.subject, Email: c.email, EmailTrusted: c.emailVerified,
		SignIn: true, Label: c.label(), Created: s.now(), LastUsed: s.now()})
	switch {
	case errors.Is(err, store.ErrLinkSubjectTaken):
		return ssoErr("identity_in_use")
	case errors.Is(err, store.ErrLinkProviderTaken):
		return ssoErr("already_linked")
	}
	if err == nil {
		s.Audit(AuditEvent{Kind: "identity_linked", User: userID, Username: p.User.Username, IP: ip, Detail: map[string]any{"provider": rt.row.Slug, "by": "user"}})
	}
	return err
}

// createFromClaims makes an account for an identity: the name from the name
// claim, the username from preferred_username or the part of the email before
// the @, made valid and unique. The address is the provider's, and verified
// only when the sign-in vouches for it (trustsEmail), recorded as the
// provider's. The address follows the rules of every address: one the sign-in
// vouches for is taken from an account that holds it unverified; any other
// holder keeps it and the account is made without one (as for an address the
// site does not accept).
func (s *Service) createFromClaims(ctx context.Context, c claimed, role, inviteID string, setup bool, tenantID string, siteAdmin bool) (*store.User, error) {
	base := usernameFrom(c)
	name := clip(c.name, 100)
	if strings.TrimSpace(name) == "" {
		name = base
	}
	email, verified, by := "", false, ""
	if e, err := s.cleanEmailFor(ctx, c.email); err == nil && e != "" {
		email, verified = e, c.emailVerified
		if verified {
			by = store.VerifiedByProvider(c.provider)
		}
	}
	u, err := s.CreateUser(ctx, CreateParams{Username: base, Name: name, Role: role, Email: email, EmailVerified: verified, VerifiedBy: by,
		TakeUnverified: verified && matchesByAddress(c.src), DropTakenEmail: true,
		Identity: c.params(), InviteID: inviteID, Setup: setup, RetryUsername: true, TenantID: tenantID, SiteAdmin: siteAdmin})
	var ae *Error
	if errors.As(err, &ae) && ae.Kind == Conflict && ae == errInUse {
		return nil, ssoErr("identity_in_use")
	}
	return u, err
}

// usernameFrom derives a valid username from the claims.
func usernameFrom(c claimed) string {
	src := c.preferred
	if src == "" {
		src, _, _ = strings.Cut(c.email, "@")
	}
	if src == "" {
		src = c.name
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

func providerReason(err error) string {
	if errors.Is(err, authoidc.ErrDiscovery) || errors.Is(err, authoidc.ErrExchange) || errors.Is(err, authoidc.ErrInvalidToken) || errors.Is(err, authoidc.ErrInvalidConfig) {
		return oidcReason(err)
	}
	return "other"
}

// --- GitHub --------------------------------------------------------------------------

func githubAPIBase(baseURL string) string {
	if u, err := url.Parse(baseURL); err == nil && strings.EqualFold(u.Hostname(), "github.com") {
		return "https://api.github.com"
	}
	return baseURL + "/api/v3"
}

func (s *Service) githubOAuth(rt *providerRT) *oauth2.Config {
	scopes := []string{"read:user", "user:email"}
	if len(rt.cfg.Organizations) > 0 {
		scopes = append(scopes, "read:org")
	}
	b := rt.cfg.BaseURL
	return &oauth2.Config{ClientID: rt.cfg.ClientID, ClientSecret: rt.secret, RedirectURL: s.redirectURL(rt), Scopes: scopes,
		Endpoint: oauth2.Endpoint{AuthURL: b + "/login/oauth/authorize", TokenURL: b + "/login/oauth/access_token", AuthStyle: oauth2.AuthStyleInParams}}
}

type ghUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name"`
}

func (s *Service) ghGet(ctx context.Context, token, apiBase, path string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "PetalBook")
	client := *s.httpClient()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub answered %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// githubClaims exchanges the code and reads the user. The user's token lives
// only in this call. A non-empty second value is the sso_error code.
func (s *Service) githubClaims(ctx context.Context, rt *providerRT, code, verifier string) (claimed, string) {
	if rt.secretErr != nil {
		return claimed{}, "provider_unavailable"
	}
	hctx := context.WithValue(ctx, oauth2.HTTPClient, s.httpClient())
	hctx, cancel := context.WithTimeout(hctx, 20*time.Second)
	defer cancel()
	tok, err := s.githubOAuth(rt).Exchange(hctx, code, oauth2.VerifierOption(verifier))
	if err != nil || tok.AccessToken == "" {
		s.log.Warn("GitHub refused the code", "provider", rt.row.Slug)
		return claimed{}, "idp_error"
	}
	api := githubAPIBase(rt.cfg.BaseURL)
	var u ghUser
	if err := s.ghGet(hctx, tok.AccessToken, api, "/user", &u); err != nil || u.ID <= 0 || u.Login == "" {
		s.log.Warn("could not read the GitHub user", "provider", rt.row.Slug)
		return claimed{}, "idp_error"
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	email, verified := "", false
	if err := s.ghGet(hctx, tok.AccessToken, api, "/user/emails", &emails); err == nil {
		for _, e := range emails {
			if e.Primary {
				email, verified = clip(e.Email, 254), e.Verified
			}
		}
	}
	_, wantGroups := rt.groupsOn()
	var gc groupClaim
	if len(rt.cfg.Organizations) > 0 || wantGroups {
		member := len(rt.cfg.Organizations) == 0
		gc.read, gc.present = wantGroups, true
		for page := 1; page <= 5 && (!member || wantGroups); page++ {
			var orgs []struct {
				Login string `json:"login"`
			}
			if err := s.ghGet(hctx, tok.AccessToken, api, "/user/orgs?per_page=100&page="+strconv.Itoa(page), &orgs); err != nil {
				s.log.Warn("could not read the GitHub organisations", "provider", rt.row.Slug)
				if !member {
					return claimed{}, "idp_error" // the organisation that lets them in was not read
				}
				// Let in already (or no organisation needed): the sign-in goes
				// on, and the groups claim is missing, not cut short.
				gc.present = false
				break
			}
			for _, o := range orgs {
				if wantGroups && len(gc.values) < maxGitHubOrgs {
					gc.add(o.Login)
				}
				for _, want := range rt.cfg.Organizations {
					member = member || strings.EqualFold(o.Login, want)
				}
			}
			if len(orgs) < 100 {
				break
			}
		}
		if !member {
			return claimed{}, "not_allowed"
		}
	}
	login := clip(u.Login, 100)
	return claimed{subject: strconv.FormatInt(u.ID, 10), name: clip(u.Name, 100), preferred: login, email: email,
		src: emailSource{template: tmplGitHub, publicGitHub: isPublicGitHub(rt.cfg.BaseURL), verified: verified}, labelOverride: login, groups: gc}, ""
}
