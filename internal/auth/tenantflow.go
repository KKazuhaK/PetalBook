package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	authoidc "github.com/KazuhaHub/authcore/oidc"
	coreoidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// A multi-tenant OpenID Connect provider (Microsoft's /common): one
// application that people of any Entra tenant, and personal Microsoft
// accounts, sign in to. Its issuer is not one string: each token's iss is
// https://login.microsoftonline.com/<the tenant that issued it>/v2.0. The
// library checks the issuer against one configured string, so these providers
// are run here, the same way (the code flow with PKCE, the state and the nonce
// bound to the browser, the signature checked against the provider's keys, the
// audience and the authorized party), with one change: the issuer is checked
// against the template with the token's own tid in it. It is never skipped: a
// token whose iss does not name the tenant in its tid is refused, and so is one
// whose tid is not a tenant id.

// tenantIDRE is a tenant id as Entra writes it: a lower-case GUID.
var tenantIDRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// tenantIssuerOK says whether iss is pattern with tid in place of {tenantid}.
func tenantIssuerOK(pattern, iss, tid string) bool {
	if !tenantIDRE.MatchString(tid) {
		return false
	}
	before, after, ok := strings.Cut(pattern, tenantIDPlaceholder)
	if !ok || strings.Contains(after, tenantIDPlaceholder) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(iss), []byte(before+tid+after)) == 1
}

const tenantFlowLimit = 1 << 20

// tenantFlow is the client of one multi-tenant provider.
type tenantFlow struct {
	pattern  string
	oauth    oauth2.Config
	verifier *coreoidc.IDTokenVerifier
	client   *http.Client
	// config ties a state to the settings it was made with.
	config string
}

// limitedBody bounds what the provider answers.
type limitedBody struct{ base http.RoundTripper }

func (t limitedBody) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" {
		return nil, errors.New("only https")
	}
	resp, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	if resp.ContentLength > tenantFlowLimit {
		resp.Body.Close()
		return nil, errors.New("the answer is too large")
	}
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.LimitReader(resp.Body, tenantFlowLimit), resp.Body}
	return resp, nil
}

func (s *Service) newTenantFlow(rt *providerRT) (*tenantFlow, error) {
	c := rt.cfg
	if !multiTenant(c.Issuer) || c.AuthURL == "" || c.TokenURL == "" || c.JWKSURL == "" || c.ClientID == "" {
		return nil, authoidc.ErrInvalidConfig
	}
	if rt.secretErr != nil {
		return nil, rt.secretErr
	}
	base := *s.httpClient()
	tr := base.Transport
	if tr == nil {
		tr = http.DefaultTransport
	}
	base.Transport = limitedBody{tr}
	if base.Timeout <= 0 || base.Timeout > 15*time.Second {
		base.Timeout = 15 * time.Second
	}
	base.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	scopes := []string{"openid"}
	for _, sc := range c.Scopes {
		if sc != "openid" {
			scopes = append(scopes, sc)
		}
	}
	style := oauth2.AuthStyleInParams
	if len(c.AuthMethods) > 0 && c.AuthMethods[0] == "client_secret_basic" {
		style = oauth2.AuthStyleInHeader
	}
	redirect := s.redirectURL(rt)
	keys := coreoidc.NewRemoteKeySet(coreoidc.ClientContext(context.Background(), &base), c.JWKSURL)
	f := &tenantFlow{
		pattern: c.Issuer, client: &base,
		oauth: oauth2.Config{ClientID: c.ClientID, ClientSecret: rt.secret, RedirectURL: redirect, Scopes: scopes,
			Endpoint: oauth2.Endpoint{AuthURL: c.AuthURL, TokenURL: c.TokenURL, AuthStyle: style}},
		// The issuer is checked below, against the token's tid, never left out.
		verifier: coreoidc.NewVerifier("", keys, &coreoidc.Config{ClientID: c.ClientID, SupportedSigningAlgs: []string{"RS256"}, SkipIssuerCheck: true, Now: s.now}),
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{"tenant/v1", c.Issuer, c.ClientID, redirect, c.AuthURL, c.TokenURL, c.JWKSURL, strings.Join(scopes, " ")}, "\x00")))
	f.config = base64.RawURLEncoding.EncodeToString(sum[:])
	return f, nil
}

// tenantFlowFor returns the provider's client, made on first use.
func (s *Service) tenantFlowFor(rt *providerRT) (*tenantFlow, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.tenant != nil {
		return rt.tenant, nil
	}
	f, err := s.newTenantFlow(rt)
	if err != nil {
		return nil, err
	}
	rt.tenant = f
	return f, nil
}

func (f *tenantFlow) begin() (authoidc.Authorization, error) {
	value, err := stateValue()
	if err != nil {
		return authoidc.Authorization{}, err
	}
	nonce, err := stateValue()
	if err != nil {
		return authoidc.Authorization{}, err
	}
	st := authoidc.State{Value: value, Nonce: nonce, Verifier: oauth2.GenerateVerifier(), Configuration: f.config}
	u := f.oauth.AuthCodeURL(value, oauth2.SetAuthURLParam("nonce", nonce), oauth2.S256ChallengeOption(st.Verifier))
	return authoidc.Authorization{URL: u, State: st}, nil
}

// verifiedToken is what a multi-tenant sign-in proved.
type verifiedToken struct {
	subject string
	claims  map[string]json.RawMessage
}

// complete exchanges the code and checks the ID token, its issuer against its
// tenant included. st is the state the caller took (once) for this browser.
func (f *tenantFlow) complete(ctx context.Context, callbackState, code string, st authoidc.State) (*verifiedToken, error) {
	if st.Configuration != f.config || st.Value == "" || st.Nonce == "" || st.Verifier == "" ||
		subtle.ConstantTimeCompare([]byte(callbackState), []byte(st.Value)) != 1 || code == "" || len(code) > 8192 {
		return nil, authoidc.ErrInvalidState
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ctx = coreoidc.ClientContext(ctx, f.client)
	tok, err := f.oauth.Exchange(ctx, code, oauth2.VerifierOption(st.Verifier))
	if err != nil {
		return nil, authoidc.ErrExchange
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" || len(raw) > tenantFlowLimit {
		return nil, authoidc.ErrInvalidToken
	}
	return f.check(ctx, raw, st.Nonce, tok.AccessToken)
}

// check verifies an ID token: the signature, the audience, the time, the
// nonce, the authorized party, and the issuer against the tenant in the token.
func (f *tenantFlow) check(ctx context.Context, raw, nonce, accessToken string) (*verifiedToken, error) {
	idt, err := f.verifier.Verify(ctx, raw)
	if err != nil {
		return nil, authoidc.ErrInvalidToken
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(nonce)) != 1 {
		return nil, authoidc.ErrNonceMismatch
	}
	var claims map[string]json.RawMessage
	if idt.Claims(&claims) != nil || idt.Subject == "" || len(idt.Subject) > 4096 {
		return nil, authoidc.ErrInvalidToken
	}
	var tid string
	if json.Unmarshal(claims["tid"], &tid) != nil || !tenantIssuerOK(f.pattern, idt.Issuer, tid) {
		return nil, errTenantIssuer
	}
	if azp, ok := claims["azp"]; ok {
		var v string
		if json.Unmarshal(azp, &v) != nil || v != f.oauth.ClientID {
			return nil, authoidc.ErrInvalidToken
		}
	} else if len(idt.Audience) > 1 {
		return nil, authoidc.ErrInvalidToken
	}
	if idt.AccessTokenHash != "" && idt.VerifyAccessToken(accessToken) != nil {
		return nil, authoidc.ErrInvalidToken
	}
	return &verifiedToken{subject: idt.Subject, claims: claims}, nil
}

// errTenantIssuer: the token's issuer is not the one of the tenant it names.
var errTenantIssuer = errors.Join(authoidc.ErrInvalidToken, errors.New("the issuer is not the token's tenant's"))

// stateValue is a state or a nonce: 32 random bytes, as the library makes them.
func stateValue() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
