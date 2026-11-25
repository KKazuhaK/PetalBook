// Package forge is the connection to GitLab (gitlab.com and self-hosted) and to
// Gitea, Forgejo and Codeberg (milestone 3.8, docs/api.md "Connections to
// GitLab and Gitea"): one provider per host, set up by an administrator as an
// OAuth application, that people connect their accounts to once. A provider
// implements connections.Provider; what differs between the two platforms is
// the driver (gitlab.go, gitea.go), and everything else is shared.
//
// State is in the state database (providers, provider_authorizations,
// provider_leftovers; every secret sealed with the server's key) and in memory
// (access tokens, and the authorisations in progress). A provider reaches its
// host only through a client that refuses addresses that are not public.
package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"petalbook.app/server/internal/connections"
	"petalbook.app/server/internal/secret"
	"petalbook.app/server/internal/store"
)

// The most providers the server may have, and a tenant (3.6).
const (
	SiteLimit   = 20
	TenantLimit = 5
)

// Reasons an invalid configuration gives (the "reason" of a 400 invalid).
const (
	ReasonHost        = "host_invalid"
	ReasonType        = "type_invalid"
	ReasonUnreachable = "host_unreachable"
	ReasonNotPlatform = "not_this_platform"
	ReasonVersion     = "unsupported_version"
	ReasonClient      = "client_rejected"
	ReasonPublicHost  = "host_not_public"
)

// Options configures a Manager.
type Options struct {
	Store    *store.Store
	Secrets  *secret.Box
	Registry *connections.Registry
	// PublicURL is the address people use; connecting an account needs it.
	PublicURL string
	Now       func() time.Time
	Logger    *slog.Logger
	// Unsafe lets a host be plain http and on a private or loopback address
	// (tests, and a lab), as --remote-unsafe lets a remote be.
	Unsafe bool
	// RenewEvery is how often the connections that need it are renewed (default
	// a day), and RenewWithin how close to its end a refresh token must be for
	// that (default 30 days).
	RenewEvery, RenewWithin time.Duration
}

// Manager holds the providers an administrator set up and makes new ones.
type Manager struct {
	opt Options
	log *slog.Logger
	hc  *http.Client

	mu          sync.Mutex // serialises making and removing providers
	hostAllowed func(host string) error
	provs       map[string]*Provider
}

var _ connections.Creator = (*Manager)(nil)

// NewManager loads the providers from the database into the registry.
func NewManager(ctx context.Context, opt Options) (*Manager, error) {
	if opt.Store == nil || opt.Secrets == nil || opt.Registry == nil {
		return nil, errors.New("forge: a store, a secret box and a registry are required")
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Logger == nil {
		opt.Logger = slog.New(slog.DiscardHandler)
	}
	if opt.RenewEvery <= 0 {
		opt.RenewEvery = 24 * time.Hour
	}
	if opt.RenewWithin <= 0 {
		opt.RenewWithin = 30 * 24 * time.Hour
	}
	opt.PublicURL = strings.TrimRight(opt.PublicURL, "/")
	m := &Manager{opt: opt, log: opt.Logger, hc: newHTTPClient(opt.Unsafe), provs: map[string]*Provider{}}
	if err := m.Reload(ctx); err != nil {
		return nil, err
	}
	opt.Registry.SetCreator(m)
	return m, nil
}

// testHookReloadRead is called by Reload right after it has read the rows (tests).
var testHookReloadRead func()

// Reload adds the providers of the database that this process does not have yet
// (set up by another process sharing the database, or a tenant's own).
func (m *Manager) Reload(ctx context.Context) error {
	// The rows are read with the lock held: a removal that finished meanwhile
	// (it holds the lock) would otherwise be undone by what was read before it.
	m.mu.Lock()
	defer m.mu.Unlock()
	rows, err := store.ListConnProviderRows(ctx, m.opt.Store.Q())
	if err != nil {
		return err
	}
	if h := testHookReloadRead; h != nil {
		h()
	}
	for _, r := range rows {
		if _, ok := m.provs[r.ID]; ok {
			continue
		}
		p := m.build(r)
		m.provs[r.ID] = p
		m.opt.Registry.Add(p)
	}
	return nil
}

// SetHostAllowed says how a provider's host is held to the allowed hosts of the
// storage settings (spaces.Manager.HostAllowed).
func (m *Manager) SetHostAllowed(f func(host string) error) {
	m.mu.Lock()
	m.hostAllowed = f
	m.mu.Unlock()
}

func (m *Manager) driverFor(typ string) driver {
	switch typ {
	case "gitlab":
		return gitlab{}
	case "gitea":
		return gitea{}
	}
	return nil
}

func secretPurpose(owner string) string {
	if owner == "" || owner == store.ConnProviderSite {
		return "connection-secret"
	}
	return "connection-secret:" + owner
}

// build makes the provider of a stored row. A secret that cannot be opened
// (the server's key changed) leaves the provider there but not set up: the
// administrator saves the secret again.
func (m *Manager) build(r store.ConnProviderRow) *Provider {
	p := &Provider{m: m, d: m.driverFor(r.Type), id: r.ID, row: r, tokens: map[string]cachedUser{}, renewing: map[string]*renewal{}, gens: map[string]uint64{},
		attempts: newAttemptStore(m.opt.Now, attemptCapacity)}
	p.c = &client{base: r.Scheme + "://" + r.Host, hc: m.hc, now: m.opt.Now, log: m.log}
	if r.SecretEnc != "" {
		if b, err := m.opt.Secrets.Open(r.ID, secretPurpose(r.Owner), r.SecretEnc); err == nil {
			p.secret = string(b)
		} else {
			m.log.Error("a provider's client secret cannot be opened; it must be saved again", "provider", r.ID)
			p.row.State = stateError
		}
	}
	return p
}

// States of a provider.
const (
	stateOK    = "ok"
	stateError = "error"
)

var (
	idRe   = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$`)
	labelR = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
)

// reservedIDs are ids that belong to the server's own providers.
var reservedIDs = map[string]bool{"git": true, "github": true}

// Host is a provider's host as it was entered.
type hostSpec struct{ scheme, host string }

// parseHost reads what an administrator typed: "gitlab.example.com",
// "gitlab.example.com:8443", or "https://gitlab.example.com". Plain http, and
// anything but a name or address with an optional port, is for the unsafe
// switch only.
func (m *Manager) parseHost(raw string) (hostSpec, error) {
	raw = strings.TrimSpace(raw)
	scheme := "https"
	if i := strings.Index(raw, "://"); i >= 0 {
		scheme = strings.ToLower(raw[:i])
		raw = raw[i+3:]
	}
	raw = strings.TrimSuffix(raw, "/")
	if scheme != "https" && !(scheme == "http" && m.opt.Unsafe) {
		return hostSpec{}, invalid(ReasonHost, "The host is given without a scheme (it is always https).")
	}
	if raw == "" || strings.ContainsAny(raw, "/?#@\\ \t\r\n%") {
		return hostSpec{}, invalid(ReasonHost, "The host is a name or address, with a port if it has one: no path, user or query.")
	}
	name, port := raw, ""
	if h, p, err := net.SplitHostPort(raw); err == nil {
		name, port = h, p
	} else if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		name = raw[1 : len(raw)-1]
	} else if strings.HasPrefix(raw, "[") {
		return hostSpec{}, invalid(ReasonHost, "The host is not valid.")
	}
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	if port != "" {
		n := 0
		for _, r := range port {
			if r < '0' || r > '9' || n > 65535 {
				return hostSpec{}, invalid(ReasonHost, "The port is not valid.")
			}
			n = n*10 + int(r-'0')
		}
		if n < 1 || n > 65535 {
			return hostSpec{}, invalid(ReasonHost, "The port is not valid.")
		}
	}
	if ip := net.ParseIP(name); ip != nil {
		name = ip.String()
	} else {
		if len(name) > 253 || name == "" {
			return hostSpec{}, invalid(ReasonHost, "The host name is not valid.")
		}
		for _, l := range strings.Split(name, ".") {
			if !labelR.MatchString(l) {
				return hostSpec{}, invalid(ReasonHost, "The host name is not valid.")
			}
		}
	}
	host := name
	if strings.Contains(name, ":") {
		host = "[" + name + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return hostSpec{scheme: scheme, host: host}, nil
}

func invalid(reason, msg string) *connections.Error {
	e := connections.Errorf("", http.StatusBadRequest, connections.CodeInvalid, "%s.", msg)
	e.Reason = reason
	return e
}

// createRequest is the body of POST /api/admin/connections.
type createRequest struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	Host         string `json:"host"`
	Name         string `json:"name"`
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
}

// defaultName is what a provider is called when its administrator did not say.
func defaultName(typ, host string) string {
	switch host {
	case "gitlab.com":
		return "GitLab"
	case "codeberg.org":
		return "Codeberg"
	}
	if typ == "gitlab" {
		return "GitLab (" + host + ")"
	}
	return "Gitea (" + host + ")"
}

func validName(s string) bool {
	if s == "" || len(s) > 80 || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func validClient(id, sec string) bool {
	return id != "" && len(id) <= 512 && sec != "" && len(sec) <= 2048 && !strings.ContainsAny(id+sec, "\r\n\x00")
}

// CreateProvider sets up a provider for the site: it checks the request, the
// host and the client's credentials against the host, stores the provider with
// its secret sealed, and adds it to the registry.
func (m *Manager) CreateProvider(ctx context.Context, actorID string, body []byte) (connections.Provider, any, error) {
	var req createRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, nil, connections.Errorf("", http.StatusBadRequest, connections.CodeInvalid, "The body is not what this takes: %s.", err.Error())
	}
	if !idRe.MatchString(req.ID) || reservedIDs[req.ID] || strings.HasPrefix(req.ID, "t-") {
		return nil, nil, connections.Errorf("", http.StatusBadRequest, connections.CodeInvalid,
			"ID is 1 to 40 lowercase letters, digits and '-' (not starting or ending with '-', not git or github, not starting with t-).")
	}
	d := m.driverFor(req.Type)
	if d == nil {
		return nil, nil, invalid(ReasonType, "Type is gitlab or gitea (Forgejo and Codeberg are gitea).")
	}
	hs, err := m.parseHost(req.Host)
	if err != nil {
		return nil, nil, withProvider(err, req.ID)
	}
	if !validClient(req.ClientID, req.ClientSecret) {
		return nil, nil, connections.Errorf(req.ID, http.StatusBadRequest, connections.CodeInvalid, "clientId and clientSecret are required.")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = defaultName(req.Type, hs.host)
	}
	if !validName(name) {
		return nil, nil, connections.Errorf(req.ID, http.StatusBadRequest, connections.CodeInvalid, "Name is at most 80 characters, without control characters.")
	}
	m.mu.Lock()
	allow := m.hostAllowed
	m.mu.Unlock()
	if allow != nil {
		if err := allow(hs.host); err != nil {
			return nil, nil, err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, taken := m.provs[req.ID]; taken {
		return nil, nil, connections.Errorf(req.ID, http.StatusConflict, "exists", "A provider with this id exists already.")
	}
	db := m.opt.Store.Q()
	n, err := store.CountConnProviderRows(ctx, db, store.ConnProviderSite)
	if err != nil {
		return nil, nil, err
	}
	if n >= SiteLimit {
		return nil, nil, connections.Errorf(req.ID, http.StatusConflict, "provider_limit", "The server has %d such providers, the most there can be.", SiteLimit)
	}
	for _, p := range m.provs {
		if r, _ := p.snap(); r.Owner == store.ConnProviderSite && r.Host == hs.host {
			return nil, nil, connections.Errorf(req.ID, http.StatusConflict, "exists", "This host has a provider already (%s): one OAuth application per host.", p.id)
		}
	}
	now := m.opt.Now()
	c := &client{base: hs.scheme + "://" + hs.host, hc: m.hc, now: m.opt.Now, log: m.log}
	version, err := m.checkHost(ctx, d, c, req.ClientID, req.ClientSecret, m.callbackURL(req.ID))
	if err != nil {
		return nil, nil, withProvider(err, req.ID)
	}
	enc, err := m.opt.Secrets.Seal(req.ID, secretPurpose(store.ConnProviderSite), []byte(req.ClientSecret))
	if err != nil {
		return nil, nil, err
	}
	row := store.ConnProviderRow{ID: req.ID, Type: req.Type, Host: hs.host, Scheme: hs.scheme, Name: name, Owner: store.ConnProviderSite,
		ClientID: req.ClientID, SecretEnc: enc, State: stateOK, PlatformVersion: version, PartialSync: "unknown",
		Checked: now, UpdatedBy: actorID, Created: now, Updated: now}
	switch err := store.InsertConnProviderRow(ctx, db, row); {
	case errors.Is(err, store.ErrExists):
		return nil, nil, connections.Errorf(req.ID, http.StatusConflict, "exists", "A provider with this id or host exists already.")
	case err != nil:
		return nil, nil, err
	}
	p := m.build(row)
	m.provs[req.ID] = p
	m.opt.Registry.Add(p)
	v, err := p.AdminView(ctx)
	return p, v, err
}

func withProvider(err error, id string) error {
	var ce *connections.Error
	if errors.As(err, &ce) && ce.Provider == "" {
		c := *ce
		c.Provider = id
		return &c
	}
	return err
}

func (m *Manager) callbackURL(id string) string {
	if m.opt.PublicURL == "" {
		return ""
	}
	return m.opt.PublicURL + "/api/connections/" + id + "/authorization/callback"
}

// checkHost asks the host who it is and whether it accepts the client: its
// version endpoint answers as a GitLab or Gitea of a supported version, and the
// token endpoint, given an authorisation code that cannot be valid, refuses
// with "invalid_grant" (it knows the client) rather than "invalid_client".
func (m *Manager) checkHost(ctx context.Context, d driver, c *client, clientID, clientSecret, redirect string) (version string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	version, err = d.probe(ctx, c)
	if err != nil {
		if errors.Is(err, errNotThisPlatform) || isStatus(err, http.StatusNotFound) || isStatus(err, http.StatusUnauthorized, http.StatusForbidden) {
			return "", invalid(ReasonNotPlatform, "The host does not answer as a "+d.typ()+" does.")
		}
		return "", invalid(unreachable(err), "The host could not be asked: "+hostFailure(err)+".")
	}
	if !d.minVersion(version) {
		return "", invalid(ReasonVersion, "This version ("+version+") is too old: it has no authorisation with PKCE.")
	}
	if redirect == "" {
		redirect = "https://notes.invalid/api/connections/check/authorization/callback"
	}
	_, err = m.token(ctx, d, c, clientID, clientSecret, [][2]string{{"grant_type", "authorization_code"}, {"code", "petalbook-check"},
		{"redirect_uri", redirect}, {"code_verifier", "petalbook-check-petalbook-check-petalbook-check-0123456789"}})
	var oe *OAuthError
	switch {
	case err == nil, isBadGrant(err), errors.As(err, &oe) && (oe.Code == "invalid_request" || oe.Code == "unsupported_grant_type"):
		return version, nil // the host knows the client and refused the (made-up) code
	case isBadClient(err):
		return "", invalid(ReasonClient, "The host does not accept this client id and secret.")
	}
	if isStatus(err, http.StatusUnauthorized, http.StatusForbidden) {
		return "", invalid(ReasonClient, "The host does not accept this client id and secret.")
	}
	if errors.As(err, &oe) {
		return "", invalid(ReasonClient, "The host refused the client: "+quote(oe.Code)+".")
	}
	return "", invalid(unreachable(err), "The host could not be asked: "+hostFailure(err)+".")
}

// unreachable is the reason for an error of reaching a host: one that is not
// a public address says so.
func unreachable(err error) string {
	if err != nil && strings.Contains(err.Error(), "is not a public address") {
		return ReasonPublicHost
	}
	return ReasonUnreachable
}

// hostFailure says what went wrong reaching a host, without anything of the request.
func hostFailure(err error) string {
	if strings.Contains(err.Error(), "is not a public address") {
		return "it is not on a public address"
	}
	var se *StatusError
	if errors.As(err, &se) {
		return fmt.Sprintf("it answered %d", se.Status)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "it did not answer in time"
	}
	return "it could not be reached"
}

// token calls the token endpoint with the form's fields and the client's credentials.
func (m *Manager) token(ctx context.Context, d driver, c *client, clientID, clientSecret string, fields [][2]string) (*tokens, error) {
	form := url.Values{"client_id": {clientID}, "client_secret": {clientSecret}}
	for _, f := range fields {
		form.Set(f[0], f[1])
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		ExpiresIn    int64  `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
		Error        string `json:"error"`
		Description  string `json:"error_description"`
	}
	if _, err := c.do(ctx, request{method: http.MethodPost, path: d.tokenPath(), form: form, kind: "oauth_token"}, &out); err != nil {
		return nil, err
	}
	if out.Error != "" {
		return nil, &OAuthError{Code: out.Error, Description: out.Description}
	}
	if out.AccessToken == "" {
		return nil, errors.New("oauth: the answer has no access token")
	}
	now := m.opt.Now()
	t := &tokens{Access: out.AccessToken, Refresh: out.RefreshToken}
	if out.ExpiresIn > 0 {
		t.AccessExpires = now.Add(time.Duration(out.ExpiresIn) * time.Second)
	}
	if out.RefreshToken != "" {
		t.RefreshExpires = d.refreshExpires(out.RefreshToken, now)
	}
	return t, nil
}

// Providers is how many providers the manager has (tests).
func (m *Manager) Providers() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.provs)
}

// remove forgets a provider after its rows were deleted.
func (m *Manager) remove(id string) {
	m.mu.Lock()
	delete(m.provs, id)
	m.mu.Unlock()
	m.opt.Registry.Remove(id)
}

// RunRenew renews the connections that need it now and then once every
// RenewEvery, until ctx ends.
func (m *Manager) RunRenew(ctx context.Context) {
	first := time.Minute
	if m.opt.RenewEvery < first {
		first = m.opt.RenewEvery
	}
	t := time.NewTimer(first)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		// A host another process of this database set up appears here too.
		if err := m.Reload(ctx); err != nil {
			m.log.Warn("could not look for providers set up elsewhere", "error", err)
		}
		m.mu.Lock()
		list := make([]*Provider, 0, len(m.provs))
		for _, p := range m.provs {
			list = append(list, p)
		}
		m.mu.Unlock()
		for _, p := range list {
			if n := p.RenewDue(ctx); n > 0 {
				m.log.Info("renewed authorisations", "provider", p.id, "count", n)
			}
		}
		t.Reset(m.opt.RenewEvery)
	}
}

// SealForTest seals a client secret as the provider id and owner would have it
// sealed (tests that write a provider's row by hand); owner "" is the site.
func (m *Manager) SealForTest(id, owner, clientSecret string) (string, error) {
	if owner == "site" {
		owner = ""
	}
	return m.opt.Secrets.Seal(id, secretPurpose(owner), []byte(clientSecret))
}
