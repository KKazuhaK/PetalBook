package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// pendingAuth is an authorization request that passed every check and waits for
// the person to approve it. It is bound to the person who made it.
type pendingAuth struct {
	id          string
	client      clientInfo
	redirectURI string
	state       string
	challenge   string
	scopes      []string
	resource    string
	userID      string
	expires     time.Time
}

// authCode is an issued authorization code (memory only, single use).
type authCode struct {
	client      clientInfo
	redirectURI string
	challenge   string
	resource    string
	userID      string
	access      string
	spaces      []string
	expires     time.Time
}

type usedCode struct {
	gid     string
	expires time.Time
}

var (
	challengeRe = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	verifierRe  = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)
)

var (
	errUnknownClient = errors.New("unknown client_id")
	// ErrNotFound: the request is not there (expired, decided, or someone else's).
	ErrNotFound = errors.New("no such authorization request")
)

// Invalid is a decision that cannot be made.
type Invalid struct{ Msg string }

func (e *Invalid) Error() string { return e.Msg }

// callback builds the redirect to the client, with state and the RFC 9207 iss.
func (s *Service) callback(redirectURI, state string, params url.Values) string {
	u, _ := url.Parse(redirectURI)
	q := u.Query()
	for k, v := range params {
		q[k] = v
	}
	if state != "" {
		q.Set("state", state)
	}
	q.Set("iss", s.issuer)
	u.RawQuery = q.Encode()
	return u.String()
}

func (s *Service) errorBack(redirectURI, state, code, desc string) string {
	return s.callback(redirectURI, state, url.Values{"error": {code}, "error_description": {desc}})
}

// lookupClient finds the client behind a client_id: a metadata document for an
// https URL, else a registered client.
func (s *Service) lookupClient(ctx context.Context, clientID string) (*clientInfo, error) {
	if isCIMDClientID(clientID) {
		return s.cimd.get(ctx, clientID)
	}
	c, err := s.registered(ctx, clientID)
	if err != nil {
		return nil, err
	}
	return &clientInfo{ID: c.ID, Name: c.Name, RedirectURIs: c.RedirectURIs}, nil
}

// Problem is an authorization request that cannot be sent back to the client
// (an unknown client, a redirect_uri it did not register): the person is shown
// the problem and nothing is redirected.
type Problem struct {
	Status int
	why    pageReason
	err    error
}

// Begin judges the parameters of GET /oauth/authorize for a signed-in person
// (userID) and keeps the request for the consent page. It returns where to send
// the browser: the consent page, or the client's redirect_uri with an error.
// Problem is set instead when the browser must not be redirected.
func (s *Service) Begin(r *http.Request, userID string) (string, *Problem) {
	q := r.URL.Query()
	clientID := q.Get("client_id")
	if clientID == "" {
		return "", &Problem{http.StatusBadRequest, reasonNoClientID, nil}
	}
	client, err := s.lookupClient(r.Context(), clientID)
	if err != nil {
		s.log.Info("oauth authorize: client refused", "client", clientID, "err", err.Error())
		return "", &Problem{http.StatusBadRequest, reasonClient, err}
	}
	redirectURI := q.Get("redirect_uri")
	if redirectURI == "" || !redirectAllowed(client.RedirectURIs, redirectURI) {
		s.log.Info("oauth authorize: redirect_uri not registered", "client", clientID, "redirect_uri", redirectURI)
		return "", &Problem{http.StatusBadRequest, reasonRedirect, nil}
	}
	// From here on the redirect target is known to be the client's own.
	state := q.Get("state")
	if q.Get("response_type") != "code" {
		return s.errorBack(redirectURI, state, "unsupported_response_type", "response_type must be code"), nil
	}
	challenge := q.Get("code_challenge")
	if method := q.Get("code_challenge_method"); challenge == "" || method != "S256" {
		return s.errorBack(redirectURI, state, "invalid_request", "PKCE with code_challenge_method S256 is required"), nil
	}
	if !challengeRe.MatchString(challenge) {
		return s.errorBack(redirectURI, state, "invalid_request", "code_challenge is not a base64url SHA-256 value"), nil
	}
	resource := q.Get("resource")
	if resource == "" {
		return s.errorBack(redirectURI, state, "invalid_target", "the resource parameter (RFC 8707) is required"), nil
	}
	if resource != s.resource {
		return s.errorBack(redirectURI, state, "invalid_target", "unknown resource"), nil
	}
	scopes, ok := grantScopes(q.Get("scope"))
	if !ok {
		return s.errorBack(redirectURI, state, "invalid_scope", "scope must be one of: "+strings.Join(scopesSupported(), " ")), nil
	}
	id, err := randomToken(24)
	if err != nil {
		return s.errorBack(redirectURI, state, "server_error", "could not start the request"), nil
	}
	now := s.now()
	s.mu.Lock()
	s.prune(now)
	if len(s.pending) >= maxPending {
		s.mu.Unlock()
		return s.errorBack(redirectURI, state, "temporarily_unavailable", "too many pending requests"), nil
	}
	s.pending[id] = &pendingAuth{id: id, client: *client, redirectURI: redirectURI, state: state, challenge: challenge,
		scopes: scopes, resource: resource, userID: userID, expires: now.Add(pendingTTL)}
	s.mu.Unlock()
	return PathConsent + "?req=" + url.QueryEscape(id), nil
}

// grantScopes checks the requested scopes against the supported ones. No
// request asks for everything; the person may still approve less.
func grantScopes(requested string) ([]string, bool) {
	if strings.TrimSpace(requested) == "" {
		return scopesSupported(), true
	}
	out := []string{}
	for _, sc := range strings.Fields(requested) {
		if !contains(scopesSupported(), sc) {
			return nil, false
		}
		if !contains(out, sc) {
			out = append(out, sc)
		}
	}
	return out, true
}

// prune drops expired requests, codes and replay markers. s.mu is held.
func (s *Service) prune(now time.Time) {
	for k, p := range s.pending {
		if now.After(p.expires) {
			delete(s.pending, k)
		}
	}
	for k, c := range s.codes {
		if now.After(c.expires) {
			delete(s.codes, k)
		}
	}
	for k, u := range s.used {
		if now.After(u.expires) {
			delete(s.used, k)
		}
	}
}

// takePending finds the person's own pending request; a request that is
// decided is removed (keep false).
func (s *Service) takePending(id, userID string, keep bool) *pendingAuth {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pending[id]
	if p == nil || s.now().After(p.expires) || p.userID != userID {
		return nil
	}
	if !keep {
		delete(s.pending, id)
	}
	return p
}

// ConsentInfo is what the consent page shows of a request.
type ConsentInfo struct {
	ClientName   string
	ClientID     string
	ClientHost   string // the host of a client_id URL; "" for a registered client
	RedirectHost string
	Punycode     bool
	Scopes       []string
}

// Consent reads the pending request of a person. ErrNotFound when it expired,
// was decided already or is someone else's.
func (s *Service) Consent(userID, req string) (*ConsentInfo, error) {
	p := s.takePending(req, userID, true)
	if p == nil {
		return nil, ErrNotFound
	}
	host := ""
	if u, err := url.Parse(p.client.ID); err == nil && isCIMDClientID(p.client.ID) {
		host = u.Hostname()
	}
	return &ConsentInfo{ClientName: p.client.Name, ClientID: p.client.ID, ClientHost: host,
		RedirectHost: redirectHost(p.redirectURI), Scopes: p.scopes,
		Punycode: hasPunycode(redirectHostname(p.redirectURI)) || hasPunycode(host)}, nil
}

// Decision is the answer of the person on the consent page.
type Decision struct {
	Req     string
	Approve bool
	Spaces  []string // ids; the caller has checked the person is a member of each
	Access  string   // "read" or "write"
}

// Decide ends a pending request and returns where to send the browser: the
// client's redirect_uri with a code, or with access_denied. ErrNotFound, *Invalid.
func (s *Service) Decide(userID string, d Decision) (string, error) {
	p := s.takePending(d.Req, userID, true)
	if p == nil {
		return "", ErrNotFound
	}
	if !d.Approve {
		s.takePending(d.Req, userID, false)
		s.log.Info("oauth authorization denied", "user", userID, "client", p.client.ID)
		return s.errorBack(p.redirectURI, p.state, "access_denied", "the user denied the request"), nil
	}
	access := d.Access
	if access == "" {
		access = scopeRead
	}
	switch {
	case access != scopeRead && access != scopeWrite:
		return "", &Invalid{"access must be read or write"}
	case access == scopeWrite && !contains(p.scopes, scopeWrite):
		return "", &Invalid{"the client asked for read only"}
	case len(d.Spaces) < 1 || len(d.Spaces) > 100:
		return "", &Invalid{"choose 1 to 100 spaces"}
	}
	// A request is decided once, and only a good decision uses it up.
	if s.takePending(d.Req, userID, false) == nil {
		return "", ErrNotFound
	}
	seen := map[string]bool{}
	var spaces []string
	for _, id := range d.Spaces {
		if !seen[id] {
			seen[id] = true
			spaces = append(spaces, id)
		}
	}
	raw, err := randomToken(32)
	if err != nil {
		return "", err
	}
	now := s.now()
	s.mu.Lock()
	s.codes[codeKey(raw)] = &authCode{client: p.client, redirectURI: p.redirectURI, challenge: p.challenge, resource: p.resource,
		userID: userID, access: access, spaces: spaces, expires: now.Add(codeTTL)}
	s.mu.Unlock()
	s.log.Info("oauth authorization approved", "user", userID, "client", p.client.ID, "access", access)
	return s.callback(p.redirectURI, p.state, url.Values{"code": {raw}}), nil
}

func codeKey(code string) string {
	h := sha256.Sum256([]byte(code))
	return hex.EncodeToString(h[:])
}

// pkceOK checks code_verifier against the stored S256 challenge.
func pkceOK(verifier, challenge string) bool {
	if !verifierRe.MatchString(verifier) {
		return false
	}
	h := sha256.Sum256([]byte(verifier))
	return equalStrings(base64.RawURLEncoding.EncodeToString(h[:]), challenge)
}

// redirectHostname is the host name of a redirect URI, without the port.
func redirectHostname(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// redirectAllowed reports whether redirectURI is one of the client's
// registered redirect URIs. The match is exact, with one exception from
// RFC 8252 section 7.3: for an http loopback redirect (localhost, 127.0.0.1 or
// [::1]) the port may differ, because a native app listens on a port the system
// picks at sign-in time. Scheme, host, path and query must still match exactly,
// and the requested URI must itself pass the checks of a registered one.
func redirectAllowed(registered []string, redirectURI string) bool {
	if contains(registered, redirectURI) {
		return true
	}
	if validateRedirect(redirectURI) != nil {
		return false
	}
	got, err := url.Parse(redirectURI)
	if err != nil || got.Scheme != "http" || !isLoopbackHost(got.Hostname()) || !validPort(got) {
		return false
	}
	for _, r := range registered {
		want, err := url.Parse(r)
		if err != nil || want.Scheme != "http" || !isLoopbackHost(want.Hostname()) {
			continue
		}
		if want.Hostname() == got.Hostname() && want.RawPath == got.RawPath && want.Path == got.Path && want.RawQuery == got.RawQuery {
			return true
		}
	}
	return false
}

// validPort accepts no port at all, or a decimal port from 1 to 65535 written
// without leading zeros.
func validPort(u *url.URL) bool {
	p := u.Port()
	if p == "" {
		return !strings.HasSuffix(u.Host, ":")
	}
	n, err := strconv.Atoi(p)
	return err == nil && n >= 1 && n <= 65535 && strconv.Itoa(n) == p
}

// isLoopbackHost is the one list of hosts an http redirect may use.
func isLoopbackHost(h string) bool {
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}
