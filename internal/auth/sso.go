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
	"unicode/utf8"

	authoidc "github.com/KazuhaHub/authcore/oidc"
	authsaml "github.com/KazuhaHub/authcore/saml"
	crewjamsaml "github.com/crewjam/saml"
	"golang.org/x/oauth2"

	"github.com/KKazuhaK/PetalBook/internal/store"
)

// OIDCConfig is the OpenID Connect provider of the command line (--oidc-*).
// Since 2.8 it is only read once, to make the provider "sso" of the state
// database (see importFlags).
type OIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	Name         string // shown on the sign-in page
	Scopes       []string
	AutoCreate   bool // an identity nobody has creates a member account
}

// ValidateOIDC checks the settings without touching the network, so a mistake
// fails at start-up before anything is created.
func ValidateOIDC(c OIDCConfig, publicURL string) error {
	if c.Issuer == "" || c.ClientID == "" {
		return errors.New("OIDC needs an issuer and a client id")
	}
	if publicURL == "" {
		return errors.New("OIDC needs --public-url: the provider is told to send people back to it")
	}
	scopes := c.Scopes
	if len(scopes) == 0 {
		scopes = []string{"profile", "email"}
	}
	if _, err := authoidc.New(canceled(), authoidc.Config{IssuerURL: c.Issuer, ClientID: c.ClientID, ClientSecret: c.ClientSecret,
		RedirectURL: strings.TrimRight(publicURL, "/") + legacyPath, Scopes: scopes}); errors.Is(err, authoidc.ErrInvalidConfig) {
		return errors.New("the OIDC settings are not valid: the issuer and the public URL must be https addresses, and the client id must be set")
	}
	return nil
}

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

func (s *Service) discoveryClient(issuer, cached string) (*http.Client, *discoveryTransport) {
	c := *s.httpClient()
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
func (s *Service) buildOIDC(ctx context.Context, cfg provCfg, secret, redirect, cached string) (*authoidc.Provider, string, error) {
	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{"profile", "email"}
	}
	client, tr := s.discoveryClient(cfg.Issuer, cached)
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
func (s *Service) discover(ctx context.Context, cfg provCfg, secret, redirect string) (string, error) {
	_, doc, err := s.buildOIDC(ctx, cfg, secret, redirect, "")
	return doc, err
}

// oidcFor returns the provider's client, making it on first use (from the
// stored document, so without the network) and keeping it; after a failure it
// waits a few seconds before trying again.
func (s *Service) oidcFor(ctx context.Context, rt *providerRT) (*authoidc.Provider, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.oidc != nil {
		return rt.oidc, nil
	}
	if rt.err != nil && s.now().Sub(rt.tried) < 5*time.Second {
		return nil, rt.err
	}
	if rt.secretErr != nil {
		return nil, rt.secretErr
	}
	p, doc, err := s.buildOIDC(ctx, rt.cfg, rt.secret, s.redirectURL(rt), rt.row.Cache)
	rt.tried = s.now()
	if err != nil {
		rt.err = err
		rt.statusOK, rt.statusChecked, rt.statusMessage = false, s.now().UTC(), oidcReason(err)
		return nil, err
	}
	rt.oidc, rt.err = p, nil
	if doc != "" {
		// Reached the provider just now: keep what it said for the next start.
		rt.statusOK, rt.statusChecked, rt.statusMessage = true, s.now().UTC(), ""
		go s.storeDiscovery(rt.row.Slug, doc, rt.statusChecked)
	}
	return p, nil
}

func (s *Service) storeDiscovery(slug, doc string, at time.Time) {
	if err := store.SetProviderStatus(context.Background(), s.st.Q(), slug, true, at, "", doc, false); err != nil {
		s.log.Warn("could not store the discovery document", "provider", slug, "error", err)
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
		return nil, errf(NotFound, "no such provider")
	}
	p, err := s.samlFor(rt)
	if err != nil {
		return nil, errf(Unavailable, "the provider's metadata is not available")
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

// SSOBegin starts a sign-in with a provider. kind "oidc" with an empty slug
// is the pre-2.8 endpoint: the provider with the legacy callback.
func (s *Service) SSOBegin(ctx context.Context, kind, slug string, p BeginParams, ip string) (*BeginResult, error) {
	var rt *providerRT
	if slug == "" && kind == KindOIDC {
		rt = s.provs.legacy()
	} else {
		rt = s.provs.get(slug)
	}
	if rt == nil || rt.row.Kind != kind || !rt.row.Enabled {
		return nil, errf(NotFound, "single sign-on is not configured")
	}
	switch p.Intent {
	case "":
		p.Intent = "login"
	case "login", "setup", "invite":
	case "link":
		if p.User == nil {
			return nil, errf(Unauthenticated, "sign in to link an identity")
		}
	default:
		return nil, errf(Invalid, "intent is login, setup, invite or link")
	}
	if len(p.Token) > 256 {
		return nil, errf(Invalid, "the token is too long")
	}
	if d, ok := s.starts.take(ip); !ok {
		return nil, retryAfter(d)
	}
	e := pendingEntry{kind: kind, slug: rt.row.Slug, intent: p.Intent, token: p.Token, ret: SafeReturn(p.Return)}
	if p.User != nil {
		e.userID = p.User.ID
	}
	unavailable := func(err error) error {
		s.log.Error("sign-in provider unavailable", "provider", rt.row.Slug, "kind", kind, "reason", providerReason(err))
		return errf(Unavailable, "the single sign-on provider cannot be reached right now")
	}
	switch kind {
	case KindOIDC:
		prov, err := s.oidcFor(ctx, rt)
		if err != nil {
			return nil, unavailable(err)
		}
		az, err := prov.Begin()
		if err != nil {
			return nil, unavailable(err)
		}
		e.state, e.stateValue = az.State, az.State.Value
		return &BeginResult{URL: az.URL, Cookie: s.ssoCookie(s.pending.put(e))}, nil
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
		req, err := prov.NewAuthnRequest(binding)
		if err != nil {
			return nil, unavailable(err)
		}
		e.samlID = req.ID
		out := &BeginResult{URL: req.RedirectURL, Cookie: s.samlCookie(rt.row.Slug, binding, int(pendingTTL/time.Second))}
		if e.intent == "link" {
			tie := randomToken()
			e.linkBinding = hashToken(tie)
			out.Binding = s.samlLinkCookie(rt.row.Slug, tie, int(pendingTTL/time.Second))
		}
		s.pending.putAs(binding, e)
		return out, nil
	}
	return nil, errf(NotFound, "single sign-on is not configured")
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
	if intent == "link" {
		base = "/settings"
	}
	return base + "?sso_error=" + code
}

// ssoFail is the outcome of a sign-in that did not work, and the audit row for it.
func (s *Service) ssoFail(kind, slug, intent, code, ip string, clear ...*http.Cookie) Outcome {
	// Only a sign-in that was really under way (it has an intent) is written
	// down: anyone can send a callback that matches nothing, and the log is not
	// for them to fill.
	if intent != "" && s.provs.get(slug) != nil {
		s.Audit(AuditEvent{Kind: "login_failed", IP: ip, Detail: map[string]any{"method": kind, "reason": code, "provider": slug}})
	}
	return Outcome{Redirect: failURL(intent, code), Clear: clear}
}

// OIDCComplete finishes a sign-in from the redirect of an OpenID Connect or
// GitHub provider. slug "" is the pre-2.8 callback. binding is the value of
// the browser's binding cookie; sessionToken the browser's session cookie (for
// link). It never returns an error: every failure is a redirect to a page that
// can explain it.
func (s *Service) OIDCComplete(ctx context.Context, slug, binding, sessionToken string, q url.Values, ip string) Outcome {
	var rt *providerRT
	if slug == "" {
		rt = s.provs.legacy()
	} else {
		rt = s.provs.get(slug)
	}
	kind := KindOIDC
	if rt != nil {
		kind = rt.row.Kind
		slug = rt.row.Slug
	}
	clear := []*http.Cookie{s.ClearSSOCookie()}
	if rt == nil || !rt.row.Enabled || kind == KindSAML {
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
	fail := func(code string) Outcome { return s.ssoFail(kind, slug, e.intent, code, ip, clear...) }
	var c claimed
	switch kind {
	case KindOIDC:
		prov, err := s.oidcFor(ctx, rt)
		if err != nil {
			s.log.Error("OpenID Connect provider unavailable", "provider", slug, "reason", oidcReason(err))
			return fail("provider_unavailable")
		}
		a, err := prov.Complete(ctx, state, code, e.state)
		if err != nil {
			s.log.Warn("sign-in failed: the code or ID token was rejected", "provider", slug, "reason", oidcReason(err))
			return fail("idp_error")
		}
		if c, err = identityFromAssertion(a); err != nil {
			s.log.Warn("sign-in failed: unusable claims", "provider", slug, "reason", err.Error())
			return fail("idp_error")
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
	if rt == nil || rt.row.Kind != KindSAML || !rt.row.Enabled {
		return s.ssoFail(KindSAML, slug, "", "provider_unavailable", ip)
	}
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
		// A link is answered only in the browser that started it.
		if e.intent == "link" && (e.linkBinding == "" || linkTie == "" || !ctEqual(hashToken(linkTie), e.linkBinding)) {
			return s.ssoFail(KindSAML, slug, e.intent, "expired", ip, clear...)
		}
		ids = []string{e.samlID}
	case rt.cfg.AllowIDPInitiated:
		e = &pendingEntry{kind: KindSAML, slug: slug, intent: "login", ret: "/"}
	default:
		return s.ssoFail(KindSAML, slug, "", "expired", ip)
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
	if err != nil {
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
	email := attr(at.Email)
	if email == "" && strings.Contains(a.NameIDFormat, "emailAddress") && strings.Contains(sub, "@") {
		email = clip(sub, 254)
	}
	c := claimed{issuer: a.Issuer, subject: sub, name: attr(at.Name), preferred: attr(at.Username), email: email}
	if c.issuer == "" {
		c.issuer = rt.cfg.IDPEntityID
	}
	// SAML has no verified-address claim: turning the option on is the
	// administrators vouching for every address the identity provider sends.
	c.emailVerified = email != "" && rt.row.MergeVerifiedEmail
	c.labelOverride = email
	if c.labelOverride == "" {
		c.labelOverride = sub
	}
	return c, nil
}

// --- who signed in -----------------------------------------------------------------

// claimed is the verified identity and what the provider says about it.
type claimed struct {
	issuer, subject string
	name            string
	preferred       string
	email           string
	emailVerified   bool
	// labelOverride, when set, is the label shown for the identity.
	labelOverride string
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
	verified := false
	var vb bool
	if json.Unmarshal(raw["email_verified"], &vb) == nil {
		verified = vb
	} else if str("email_verified") == "true" {
		verified = true
	}
	sub := strings.TrimSpace(a.Subject)
	if sub == "" || len(sub) > 255 || !utf8.ValidString(sub) || strings.ContainsFunc(sub, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return claimed{}, errors.New("the sub claim is empty or not usable")
	}
	// The issuer is the verified token's: the library checked it equals the configured one.
	email := clip(str("email"), 254)
	return claimed{issuer: a.Issuer, subject: sub, name: str("name"), preferred: str("preferred_username"), email: email, emailVerified: verified && email != ""}, nil
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

type ssoError struct{ code string }

func (e *ssoError) Error() string { return e.code }

func ssoErr(code string) error { return &ssoError{code} }

// finishSSO turns a verified identity into what the intent asked for and ends
// the attempt with a redirect.
func (s *Service) finishSSO(ctx context.Context, rt *providerRT, e *pendingEntry, c claimed, sessionToken, ip string, clear []*http.Cookie) Outcome {
	kind, slug := rt.row.Kind, rt.row.Slug
	var user *store.User
	var created bool
	var err error
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
	}
	if err != nil {
		var se *ssoError
		if errors.As(err, &se) {
			return s.ssoFail(kind, slug, e.intent, se.code, ip, clear...)
		}
		s.log.Error("sign-in failed", "error", err)
		return s.ssoFail(kind, slug, e.intent, "internal", ip, clear...)
	}
	out := Outcome{Redirect: e.ret, Clear: clear}
	if e.intent == "link" {
		return out // the session stays as it is
	}
	tok, err := s.issueSession(ctx, user.ID, false)
	if err != nil {
		s.log.Error("could not start a session", "error", err)
		return s.ssoFail(kind, slug, e.intent, "internal", ip, clear...)
	}
	out.Session = tok
	s.log.Info("signed in", "user", user.ID, "method", kind, "provider", slug)
	s.Audit(AuditEvent{Kind: "login", User: user.ID, Username: user.Username, IP: ip, Detail: map[string]any{"method": kind, "provider": slug, "created": created}})
	return out
}

func (s *Service) ssoLogin(ctx context.Context, rt *providerRT, c claimed, ip string) (*store.User, bool, error) {
	ident, err := store.FindIdentity(ctx, s.st.Q(), c.issuer, c.subject)
	switch {
	case err == nil:
		u, err := store.GetUser(ctx, s.st.Q(), ident.UserID)
		if err != nil {
			return nil, false, err
		}
		if u.Disabled {
			return nil, false, ssoErr("disabled")
		}
		return u, false, nil
	case !errors.Is(err, store.ErrNotFound):
		return nil, false, err
	}
	// Nobody has this identity. Matching an account by its address is opt-in
	// and strict; so is making one, and never the first: that is the
	// administrator's, made with the setup token.
	if rt.row.MergeVerifiedEmail && c.emailVerified && c.email != "" {
		u, err := s.mergeByEmail(ctx, rt, c, ip)
		if err != nil {
			return nil, false, err
		}
		if u != nil {
			return u, false, nil
		}
		if err := s.ambiguousEmail(ctx, c.email); err != nil {
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
	u, err := s.createFromClaims(ctx, c, "member", "", false)
	return u, true, err
}

// mergeByEmail attaches the identity to the one account whose address it is,
// when that account's address is verified too. Never to an administrator, a
// disabled account, or one that has an identity from this provider already.
func (s *Service) mergeByEmail(ctx context.Context, rt *providerRT, c claimed, ip string) (*store.User, error) {
	users, err := store.UsersByEmail(ctx, s.st.Q(), s.st.Dialect(), c.email, 2)
	if err != nil {
		return nil, err
	}
	if len(users) != 1 {
		return nil, nil
	}
	u := users[0]
	if !u.EmailVerified || u.Role == "admin" || u.Disabled {
		return nil, nil
	}
	if has, err := store.UserHasIdentityOf(ctx, s.st.Q(), u.ID, rt.row.Issuer); err != nil {
		return nil, err
	} else if has {
		return nil, nil
	}
	err = store.InsertIdentity(ctx, s.st.Q(), store.Identity{ID: s.newID(), UserID: u.ID, Issuer: c.issuer, Subject: c.subject, Label: c.label(), Created: s.now()})
	if store.IsUnique(err) {
		return nil, ssoErr("identity_in_use")
	}
	if err != nil {
		return nil, err
	}
	s.Audit(AuditEvent{Kind: "identity_linked", User: u.ID, Username: u.Username, IP: ip, Detail: map[string]any{"provider": rt.row.Slug, "by": "email"}})
	return &u, nil
}

// ambiguousEmail is where an address that matched nobody (or somebody the
// rules above kept out) stops: no_account, never a new account.
func (s *Service) ambiguousEmail(ctx context.Context, email string) error {
	users, err := store.UsersByEmail(ctx, s.st.Q(), s.st.Dialect(), email, 1)
	if err != nil {
		return err
	}
	if len(users) > 0 {
		return ssoErr("no_account")
	}
	return nil
}

func (s *Service) ssoSetup(ctx context.Context, c claimed, token string) (*store.User, error) {
	if open, err := s.SetupOpen(ctx); err != nil {
		return nil, err
	} else if !open || !s.setupTokenOK(token) {
		return nil, ssoErr("setup_invalid")
	}
	u, err := s.createFromClaims(ctx, c, "admin", "", true)
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
	u, err := s.createFromClaims(ctx, c, inv.Role, inv.ID, false)
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
		u, e := store.GetUser(ctx, s.st.Q(), userID)
		if e != nil || u.Disabled {
			return ssoErr("expired")
		}
		p = &Principal{User: *u}
	} else if p, err = s.Authenticate(ctx, sessionToken); err != nil || p.User.ID != userID {
		return ssoErr("expired")
	}
	if ident, err := store.FindIdentity(ctx, s.st.Q(), c.issuer, c.subject); err == nil {
		if ident.UserID == userID {
			return nil
		}
		return ssoErr("identity_in_use")
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	err = store.InsertIdentity(ctx, s.st.Q(), store.Identity{ID: s.newID(), UserID: userID, Issuer: c.issuer, Subject: c.subject, Label: c.label(), Created: s.now()})
	if store.IsUnique(err) {
		return ssoErr("identity_in_use")
	}
	if err == nil {
		s.Audit(AuditEvent{Kind: "identity_linked", User: userID, Username: p.User.Username, IP: ip, Detail: map[string]any{"provider": rt.row.Slug, "by": "user"}})
	}
	return err
}

// createFromClaims makes an account for an identity: the name from the name
// claim, the username from preferred_username or the part of the email before
// the @, made valid and unique. The address is the provider's, and verified
// only when the provider vouched for it.
func (s *Service) createFromClaims(ctx context.Context, c claimed, role, inviteID string, setup bool) (*store.User, error) {
	base := usernameFrom(c)
	name := clip(c.name, 100)
	if strings.TrimSpace(name) == "" {
		name = base
	}
	email, verified := "", false
	if e, err := cleanEmail(c.email); err == nil && e != "" {
		email, verified = e, c.emailVerified
	}
	u, err := s.CreateUser(ctx, CreateParams{Username: base, Name: name, Role: role, Email: email, EmailVerified: verified, Identity: c.params(), InviteID: inviteID, Setup: setup, RetryUsername: true})
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
	if len(rt.cfg.Organizations) > 0 {
		member := false
		for page := 1; page <= 5 && !member; page++ {
			var orgs []struct {
				Login string `json:"login"`
			}
			if err := s.ghGet(hctx, tok.AccessToken, api, "/user/orgs?per_page=100&page="+strconv.Itoa(page), &orgs); err != nil {
				s.log.Warn("could not read the GitHub organisations", "provider", rt.row.Slug)
				return claimed{}, "idp_error"
			}
			for _, o := range orgs {
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
	return claimed{issuer: rt.cfg.BaseURL, subject: strconv.FormatInt(u.ID, 10), name: clip(u.Name, 100), preferred: login, email: email,
		emailVerified: verified && email != "", labelOverride: login}, ""
}
