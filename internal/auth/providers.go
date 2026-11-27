package auth

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	authoidc "github.com/KazuhaHub/authcore/oidc"
	authsaml "github.com/KazuhaHub/authcore/saml"
	crewjamsaml "github.com/crewjam/saml"
	xrv "github.com/mattermost/xml-roundtrip-validator"

	"petalbook.app/server/internal/dailyfetch"
	"petalbook.app/server/internal/secret"
	"petalbook.app/server/internal/secrets"
	"petalbook.app/server/internal/store"
)

// Kinds of sign-in provider.
const (
	KindOIDC   = "oidc"
	KindSAML   = "saml"
	KindGitHub = "github"
)

const (
	maxProviders  = 50
	metadataLimit = 1 << 20
)

var (
	slugRE      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	presetIcons = []string{"google", "microsoft", "apple", "github", "gitlab", "okta", "keycloak", "authentik", "key"}
)

// provCfg is the configuration of one provider, as JSON in the row. Only the
// fields of its kind are used. Nothing here is secret.
type provCfg struct {
	// OpenID Connect and GitHub
	Issuer   string   `json:"issuer,omitempty"`
	ClientID string   `json:"clientId,omitempty"`
	Scopes   []string `json:"scopes,omitempty"`
	// GitHub
	BaseURL       string   `json:"baseUrl,omitempty"`
	Organizations []string `json:"organizations,omitempty"`
	// App is the GitHub App's record, when the row is the one the App is set up
	// on (internal/github): the row's client is the App's, its secret is in
	// secrets, and only the App's own settings change them. Kept as it is here.
	App json.RawMessage `json:"app,omitempty"`
	// SAML
	MetadataURL        string    `json:"metadataUrl,omitempty"`
	MetadataXML        string    `json:"metadataXml,omitempty"`
	IDPEntityID        string    `json:"idpEntityId,omitempty"`
	EntityID           string    `json:"entityId,omitempty"`
	Attributes         SAMLAttrs `json:"attributes"`
	NameIDFormat       string    `json:"nameIdFormat,omitempty"`
	SignRequests       bool      `json:"signRequests,omitempty"`
	AllowIDPInitiated  bool      `json:"allowIdpInitiated,omitempty"`
	Certificate        string    `json:"certificate,omitempty"`
	CertificateExpires time.Time `json:"certificateExpires,omitempty"`
	// Groups (any kind, from 3.2): the groups claim and what it maps to.
	Groups *GroupsCfg `json:"groups,omitempty"`
	// A provider made from a template (3.6): the addresses and the token
	// endpoint's methods copied from it (nothing is discovered), and how the
	// provider answers (form_post: Apple posts it).
	AuthURL      string   `json:"authorizationEndpoint,omitempty"`
	TokenURL     string   `json:"tokenEndpoint,omitempty"`
	JWKSURL      string   `json:"jwksUri,omitempty"`
	AuthMethods  []string `json:"authMethods,omitempty"`
	ResponseMode string   `json:"responseMode,omitempty"`
	// Apple: the team and the key its client secret is signed with (the key
	// itself is the site's secret signin.appleKey).
	AppleTeamID string `json:"appleTeamId,omitempty"`
	AppleKeyID  string `json:"appleKeyId,omitempty"`
	// Offer is to whom a site SSO provider is offered (3.6 d3): "" every
	// tenant, OfferList the tenants of provider_shares only.
	Offer string `json:"offer,omitempty"`
}

// OfferList is provCfg.Offer of a site SSO provider offered to a list of tenants.
const OfferList = "list"

// SAMLAttrs name where the email, display name and username come from.
type SAMLAttrs struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Username string `json:"username"`
}

// providerRT is a provider as the server runs it. It is replaced, not edited,
// when the provider changes.
type providerRT struct {
	row       store.ProviderRow
	cfg       provCfg
	secret    string
	secretErr error

	idxOnce sync.Once
	idx     map[string]string // the groups map by claim value (see groupIdx)

	// apple signs the client secret of a provider made from the Apple
	// template (nil for any other, or while it has no key).
	apple *appleSecret

	mu   sync.Mutex
	oidc *authoidc.Provider
	// oidcSecret is the client secret oidc was made with: an Apple secret
	// that is renewed makes a new one.
	oidcSecret string
	// tenant is the client of a multi-tenant provider (tenantflow.go).
	tenant   *tenantFlow
	saml     *authsaml.Provider
	samlMeta *crewjamsaml.EntityDescriptor
	tried    time.Time
	err      error
	// status of the last fetch
	statusOK      bool
	statusChecked time.Time
	statusMessage string
}

func (rt *providerRT) status() (bool, time.Time, string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.statusOK, rt.statusChecked, rt.statusMessage
}

func (rt *providerRT) setStatus(ok bool, at time.Time, msg string) {
	rt.mu.Lock()
	rt.statusOK, rt.statusChecked, rt.statusMessage = ok, at, msg
	rt.mu.Unlock()
}

// PublicProvider is what the sign-in page may know of a provider.
type PublicProvider struct {
	Slug string `json:"slug"`
	Kind string `json:"kind"`
	Name string `json:"name"`
	Icon string `json:"icon"`
}

// providerSet is every provider, in memory.
type providerSet struct {
	mu   sync.RWMutex
	list []*providerRT
	// byID and bySlug have every provider, of every owner: slugs are unique
	// on the server (they are in the callback addresses), so the public
	// routes resolve a tenant's provider by its slug too (3.6 d3); what it
	// may sign in to is its tenant's alone (see mayUse).
	byID   map[string]*providerRT
	bySlug map[string]*providerRT
	// shares are the tenants each site SSO provider offered to a list is
	// offered to (provider_shares), by provider id.
	shares map[string]map[string]bool
	pub    atomic.Pointer[[]PublicProvider]
	// anyOn is whether some site provider is enabled.
	anyOn atomic.Bool
}

func newProviderSet() *providerSet {
	p := &providerSet{byID: map[string]*providerRT{}, bySlug: map[string]*providerRT{}, shares: map[string]map[string]bool{}}
	empty := []PublicProvider{}
	p.pub.Store(&empty)
	return p
}

// get is the provider with the slug, of any owner: what the public sign-in
// routes resolve.
func (p *providerSet) get(slug string) *providerRT {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.bySlug[slug]
}

// sharedWith reports whether the site provider id is offered to tenant by
// its list (provider_shares).
func (p *providerSet) sharedWith(id, tenant string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.shares[id][tenant]
}

// sharesOf is the tenants a site provider's list names, sorted.
func (p *providerSet) sharesOf(id string) []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, 0, len(p.shares[id]))
	for t := range p.shares[id] {
		out = append(out, t)
	}
	slices.Sort(out)
	return out
}

// getID is the provider (of any owner) with the id.
func (p *providerSet) getID(id string) *providerRT {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.byID[id]
}

func (p *providerSet) anyEnabled() bool { return p.anyOn.Load() }

func (p *providerSet) publicList() []PublicProvider { return *p.pub.Load() }

func (p *providerSet) all() []*providerRT {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return slices.Clone(p.list)
}

func (p *providerSet) set(list []*providerRT, shares map[string][]string) {
	pub := []PublicProvider{}
	byID := make(map[string]*providerRT, len(list))
	by := make(map[string]*providerRT, len(list))
	sh := make(map[string]map[string]bool, len(shares))
	for id, l := range shares {
		m := make(map[string]bool, len(l))
		for _, t := range l {
			m[t] = true
		}
		sh[id] = m
	}
	on := false
	for _, rt := range list {
		byID[rt.row.ID] = rt
		by[rt.row.Slug] = rt
		if rt.row.Owner != store.ProviderSite {
			continue
		}
		if rt.row.Enabled {
			on = true
			pub = append(pub, PublicProvider{Slug: rt.row.Slug, Kind: rt.row.Kind, Name: rt.row.Name, Icon: rt.row.Icon})
		}
	}
	p.mu.Lock()
	p.list, p.byID, p.bySlug, p.shares = list, byID, by, sh
	p.mu.Unlock()
	p.pub.Store(&pub)
	p.anyOn.Store(on)
}

// --- sealing -----------------------------------------------------------------

// rawBox is the server's box of sealed secrets.
func (s *Service) rawBox() *secret.Box {
	if s.cfg.Secrets != nil {
		return s.cfg.Secrets
	}
	return s.cfg.Spaces.Secrets()
}

func (s *Service) box() interface {
	Seal(spaceID, purpose string, plaintext []byte) (string, error)
	Open(spaceID, purpose, sealed string) ([]byte, error)
} {
	if s.cfg.Secrets != nil {
		return s.cfg.Secrets
	}
	return s.cfg.Spaces.Secrets()
}

func (s *Service) seal(purpose, plain string) (string, error) {
	return s.box().Seal("auth", purpose, []byte(plain))
}

func (s *Service) open(purpose, enc string) (string, error) {
	b, err := s.box().Open("auth", purpose, enc)
	return string(b), err
}

// --- loading -----------------------------------------------------------------

func (s *Service) newRT(ctx context.Context, row store.ProviderRow) *providerRT {
	rt := &providerRT{row: row, statusOK: row.StatusOK, statusChecked: row.StatusChecked, statusMessage: row.StatusMessage}
	if err := json.Unmarshal([]byte(row.Config), &rt.cfg); err != nil {
		rt.secretErr = errors.New("the stored configuration is not readable")
		return rt
	}
	if row.Kind == KindGitHub && len(rt.cfg.App) > 0 {
		// The row of the GitHub App: its client secret is a secret of the site.
		v, ok, err := secrets.Of(s.st, s.rawBox()).Open(ctx, s.st.Q(), "", secrets.GitHubClientSecret)
		switch {
		case err != nil:
			rt.secretErr = err
		case ok:
			rt.secret = v
		}
		return rt
	}
	if row.Template == TemplateApple {
		// The key the client secret is signed with is a secret of the site.
		v, ok, err := secrets.Of(s.st, s.rawBox()).Open(ctx, s.st.Q(), "", secrets.AppleSignInKey)
		switch {
		case err != nil:
			rt.secretErr = err
		case ok:
			k, err := parseAppleKey(v)
			if err != nil {
				rt.secretErr = err
				break
			}
			rt.apple = &appleSecret{key: k, teamID: rt.cfg.AppleTeamID, keyID: rt.cfg.AppleKeyID, clientID: rt.cfg.ClientID}
		}
		return rt
	}
	if row.SecretEnc != "" {
		purpose := "provider:" + row.ID
		if row.Kind == KindSAML {
			purpose += ":key"
		}
		rt.secret, rt.secretErr = s.open(purpose, row.SecretEnc)
	}
	return rt
}

// ReloadProviders reads the providers again: something outside this service
// wrote one (the GitHub App was set up on its row).
func (s *Service) ReloadProviders(ctx context.Context) error {
	providerMu.Lock()
	defer providerMu.Unlock()
	return s.reloadProviders(ctx)
}

// reloadProviders reads the providers again and rebuilds only those that
// changed; the others keep what they have built.
func (s *Service) reloadProviders(ctx context.Context, rebuild ...string) error {
	rows, err := store.ListProviders(ctx, s.st.Q())
	if err != nil {
		return err
	}
	shares, err := store.ProviderShares(ctx, s.st.Q())
	if err != nil {
		return err
	}
	old := map[string]*providerRT{}
	for _, rt := range s.provs.all() {
		old[rt.row.ID] = rt
	}
	list := make([]*providerRT, 0, len(rows))
	for _, row := range rows {
		if o := old[row.ID]; o != nil && !slices.Contains(rebuild, row.ID) && o.row.Updated.Equal(row.Updated) && o.row.GroupsVersion == row.GroupsVersion {
			list = append(list, o)
			continue
		}
		list = append(list, s.newRT(ctx, row))
	}
	s.provs.set(list, shares)
	return nil
}

// --- the view ----------------------------------------------------------------

// ProviderView is a provider as the administrators see it.
type ProviderView struct {
	// ID is the provider's own id (a site provider's is its slug); Owner is
	// "site" or the id of the tenant that owns it.
	ID    string `json:"id"`
	Owner string `json:"owner"`
	Slug  string `json:"slug"`
	Kind  string `json:"kind"`
	// Class is social or sso, Template the preset the provider was made from
	// ("" for none); both are fixed when it is made.
	Class              string `json:"class"`
	Template           string `json:"template"`
	Name               string `json:"name"`
	Icon               string `json:"icon"`
	Enabled            bool   `json:"enabled"`
	Order              int    `json:"order"`
	AutoCreate         bool   `json:"autoCreate"`
	MergeVerifiedEmail bool   `json:"mergeVerifiedEmail"`
	// Tenant is the tenant autoCreate puts new accounts in (an id).
	Tenant string `json:"tenant"`
	// Tenants is to whom a site SSO provider is offered: "all" or a list of
	// tenant ids; null for a social provider and for a tenant's own (3.6 d3).
	Tenants any `json:"tenants"`
	// Domains are the verified domains of the provider's tenant routed to it
	// (a tenant's own; [] for any other).
	Domains []string    `json:"domains"`
	OIDC    *OIDCView   `json:"oidc,omitempty"`
	SAML    *SAMLView   `json:"saml,omitempty"`
	GitHub  *GitHubView `json:"github,omitempty"`
	Apple   *AppleView  `json:"apple,omitempty"`
	Groups  GroupsCfg   `json:"groups"`
	// Signin is always true here (these are the providers people sign in with);
	// Repos says the provider also holds repositories (the GitHub row once its App
	// is set up), whose own settings are the repository side's.
	Signin           bool         `json:"signin"`
	Repos            bool         `json:"repos"`
	Status           ProviderStat `json:"status"`
	People           int          `json:"people"`
	Created          string       `json:"created"`
	Updated          string       `json:"updated"`
	SecretUnreadable bool         `json:"-"`
}

// ProviderStat is the result of the last discovery or metadata fetch.
type ProviderStat struct {
	OK      bool   `json:"ok"`
	Checked string `json:"checked,omitempty"`
	Message string `json:"message"`
}

// OIDCView is the OpenID Connect part of a provider.
type OIDCView struct {
	Issuer      string   `json:"issuer"`
	ClientID    string   `json:"clientId"`
	HasSecret   bool     `json:"hasSecret"`
	Scopes      []string `json:"scopes"`
	RedirectURL string   `json:"redirectUrl"`
}

// AppleView is what Sign in with Apple's client secret is made with: the key
// itself is never shown.
type AppleView struct {
	TeamID string `json:"teamId"`
	KeyID  string `json:"keyId"`
	HasKey bool   `json:"hasKey"`
}

// SAMLView is the SAML part of a provider.
type SAMLView struct {
	MetadataURL        string    `json:"metadataUrl"`
	MetadataXML        string    `json:"metadataXml"`
	IDPEntityID        string    `json:"idpEntityId"`
	EntityID           string    `json:"entityId"`
	ACSURL             string    `json:"acsUrl"`
	MetadataPath       string    `json:"metadataPath"`
	Attributes         SAMLAttrs `json:"attributes"`
	NameIDFormat       string    `json:"nameIdFormat"`
	SignRequests       bool      `json:"signRequests"`
	AllowIDPInitiated  bool      `json:"allowIdpInitiated"`
	Certificate        string    `json:"certificate"`
	CertificateExpires string    `json:"certificateExpires,omitempty"`
}

// GitHubView is the GitHub part of a provider.
type GitHubView struct {
	BaseURL       string   `json:"baseUrl"`
	ClientID      string   `json:"clientId"`
	HasSecret     bool     `json:"hasSecret"`
	Organizations []string `json:"organizations"`
	RedirectURL   string   `json:"redirectUrl"`
	// App: the client is the GitHub App's (set with the App, not here).
	App bool `json:"app"`
}

func (s *Service) publicBase() string { return strings.TrimRight(s.cfg.PublicURL, "/") }

func (s *Service) redirectURL(rt *providerRT) string {
	return s.publicBase() + redirectPath(rt.row)
}

func (s *Service) samlURLs(slug string) (entity, acs, metaPath string) {
	metaPath = "/api/auth/saml/" + slug + "/metadata"
	return s.publicBase() + metaPath, s.publicBase() + "/api/auth/saml/" + slug + "/acs", metaPath
}

func (s *Service) viewOf(rt *providerRT, people int, routes map[string][]string) ProviderView {
	row := rt.row
	ok, checked, msg := rt.status()
	v := ProviderView{ID: row.ID, Owner: row.Owner, Slug: row.Slug, Kind: row.Kind, Class: row.Class, Template: row.Template, Name: row.Name, Icon: row.Icon, Enabled: row.Enabled, Order: row.Position,
		AutoCreate: row.AutoCreate, MergeVerifiedEmail: row.MergeVerifiedEmail, Tenant: row.TenantID, People: people, Groups: groupsView(row.Kind, rt.cfg.Groups),
		Signin: true, Repos: row.Repos,
		Status:  ProviderStat{OK: ok, Message: msg},
		Created: rfc3339(row.Created), Updated: rfc3339(row.Updated), SecretUnreadable: rt.secretErr != nil}
	if !checked.IsZero() {
		v.Status.Checked = rfc3339(checked)
	}
	v.Domains = slices.Clone(routes[row.ID])
	if v.Domains == nil {
		v.Domains = []string{}
	}
	if row.Owner == store.ProviderSite && row.Class == ClassSSO {
		v.Tenants = "all"
		if rt.cfg.Offer == OfferList {
			v.Tenants = s.provs.sharesOf(row.ID)
		}
	}
	switch row.Kind {
	case KindOIDC:
		v.OIDC = &OIDCView{Issuer: rt.cfg.Issuer, ClientID: rt.cfg.ClientID, HasSecret: row.SecretEnc != "" || rt.apple != nil, Scopes: slices.Clone(rt.cfg.Scopes), RedirectURL: s.redirectURL(rt)}
		if row.Template == TemplateApple {
			v.Apple = &AppleView{TeamID: rt.cfg.AppleTeamID, KeyID: rt.cfg.AppleKeyID, HasKey: rt.apple != nil}
		}
	case KindGitHub:
		orgs := slices.Clone(rt.cfg.Organizations)
		if orgs == nil {
			orgs = []string{}
		}
		v.GitHub = &GitHubView{BaseURL: rt.cfg.BaseURL, ClientID: rt.cfg.ClientID, HasSecret: row.SecretEnc != "" || (len(rt.cfg.App) > 0 && rt.secret != ""), Organizations: orgs,
			RedirectURL: s.redirectURL(rt), App: len(rt.cfg.App) > 0}
	case KindSAML:
		entity, acs, metaPath := s.samlURLs(row.Slug)
		if rt.cfg.EntityID != "" {
			entity = rt.cfg.EntityID
		}
		v.SAML = &SAMLView{MetadataURL: rt.cfg.MetadataURL, MetadataXML: rt.cfg.MetadataXML, IDPEntityID: rt.cfg.IDPEntityID, EntityID: entity, ACSURL: acs,
			MetadataPath: metaPath, Attributes: rt.cfg.Attributes, NameIDFormat: rt.cfg.NameIDFormat, SignRequests: rt.cfg.SignRequests,
			AllowIDPInitiated: rt.cfg.AllowIDPInitiated, Certificate: rt.cfg.Certificate}
		if !rt.cfg.CertificateExpires.IsZero() {
			v.SAML.CertificateExpires = rfc3339(rt.cfg.CertificateExpires)
		}
	}
	return v
}

func rfc3339(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

// Providers lists every provider in order.
func (s *Service) Providers(ctx context.Context) ([]ProviderView, error) {
	return s.ProvidersOf(ctx, "")
}

// ProvidersOf lists the providers of one owner ("site" or a tenant id; ""
// for every owner) in order.
func (s *Service) ProvidersOf(ctx context.Context, owner string) ([]ProviderView, error) {
	people, err := store.ProviderPeople(ctx, s.st.Q())
	if err != nil {
		return nil, err
	}
	routes, err := store.DomainRoutes(ctx, s.st.Q())
	if err != nil {
		return nil, err
	}
	out := []ProviderView{}
	for _, rt := range s.provs.all() {
		if owner == "" || rt.row.Owner == owner {
			out = append(out, s.viewOf(rt, people[rt.row.ID], routes))
		}
	}
	return out, nil
}

// Provider returns one provider's view.
func (s *Service) Provider(ctx context.Context, id string) (*ProviderView, error) {
	rt := s.provs.getID(id)
	if rt == nil {
		return nil, errf(NotFound, "No such provider.")
	}
	people, err := store.ProviderPeople(ctx, s.st.Q())
	if err != nil {
		return nil, err
	}
	routes, err := store.DomainRoutes(ctx, s.st.Q())
	if err != nil {
		return nil, err
	}
	v := s.viewOf(rt, people[rt.row.ID], routes)
	return &v, nil
}

// ProviderOwner is the owner of a provider ("site" or a tenant id), "" when
// there is none with the id.
func (s *Service) ProviderOwner(id string) string {
	if rt := s.provs.getID(id); rt != nil {
		return rt.row.Owner
	}
	return ""
}

// ProviderUse counts each tenant's own sign-in providers, and how many of
// them are enabled, from memory: what switching features.ownSso off stops.
func (s *Service) ProviderUse() map[string][2]int {
	out := map[string][2]int{}
	for _, rt := range s.provs.all() {
		if rt.row.Owner == store.ProviderSite {
			continue
		}
		n := out[rt.row.Owner]
		n[0]++
		if rt.row.Enabled {
			n[1]++
		}
		out[rt.row.Owner] = n
	}
	return out
}

// PublicProviders is what GET /api/auth/providers lists, answered from memory.
func (s *Service) PublicProviders() []PublicProvider { return s.provs.publicList() }

// PublicProvidersFor is what the sign-in page of one tenant lists
// (GET /api/auth/providers?tenant=): the site's social providers the tenant
// allows (signin.social), the site's SSO offered to it, and its own that can
// sign in now, in the administrators' order. From memory and the settings'
// cache: no query.
func (s *Service) PublicProvidersFor(ctx context.Context, tenant string) []PublicProvider {
	out := []PublicProvider{}
	for _, rt := range s.provs.all() {
		if rt.row.Owner != store.ProviderSite && rt.row.Owner != tenant || !s.signInOn(ctx, rt) || !s.tenantMayUse(ctx, rt, tenant) {
			continue
		}
		out = append(out, PublicProvider{Slug: rt.row.Slug, Kind: rt.row.Kind, Name: rt.row.Name, Icon: rt.row.Icon})
	}
	return out
}

// --- input -------------------------------------------------------------------

// ProviderInput is a POST or PATCH of a provider: any field.
type ProviderInput struct {
	// Owner is "site" (the default) or a tenant's id; it is set when the
	// provider is made and cannot change.
	Owner *string `json:"owner"`
	Slug  *string `json:"slug"`
	Kind  *string `json:"kind"`
	// Template makes the provider from a preset (POST only): it fills the
	// kind, the class, the addresses, the scopes, the name and the icon;
	// ClientID and ClientSecret (or Apple) are the administrator's.
	Template     *string `json:"template"`
	Class        *string `json:"class"`
	ClientID     *string `json:"clientId"`
	ClientSecret *string `json:"clientSecret"`
	// Apple is what Sign in with Apple's client secret is signed with.
	Apple              *AppleInput `json:"apple"`
	Name               *string     `json:"name"`
	Icon               *string     `json:"icon"`
	Enabled            *bool       `json:"enabled"`
	Order              *int        `json:"order"`
	AutoCreate         *bool       `json:"autoCreate"`
	MergeVerifiedEmail *bool       `json:"mergeVerifiedEmail"`
	Tenant             *string     `json:"tenant"`
	// Tenants is to whom a site SSO provider is offered: "all" or a list of
	// tenant ids (3.6 d3).
	Tenants *TenantsInput `json:"tenants"`
	OIDC    *OIDCInput    `json:"oidc"`
	SAML    *struct {
		MetadataURL       *string `json:"metadataUrl"`
		MetadataXML       *string `json:"metadataXml"`
		EntityID          *string `json:"entityId"`
		Attributes        *SAMLAttrs
		NameIDFormat      *string `json:"nameIdFormat"`
		SignRequests      *bool   `json:"signRequests"`
		AllowIDPInitiated *bool   `json:"allowIdpInitiated"`
	} `json:"saml"`
	GitHub *GitHubInput `json:"github"`
	Groups *groupsInput `json:"groups"`
}

// TenantsInput is "all" or a list of tenant ids.
type TenantsInput struct {
	All  bool
	List []string
}

// UnmarshalJSON reads "all" or a list of ids.
func (t *TenantsInput) UnmarshalJSON(b []byte) error {
	var all string
	if json.Unmarshal(b, &all) == nil {
		if all != "all" {
			return errors.New(`tenants is "all" or a list of tenant ids`)
		}
		*t = TenantsInput{All: true}
		return nil
	}
	var l []string
	if err := json.Unmarshal(b, &l); err != nil || l == nil {
		return errors.New(`tenants is "all" or a list of tenant ids`)
	}
	*t = TenantsInput{List: l}
	return nil
}

// maxShares is how many tenants one provider's list may name.
const maxShares = 1000

// OIDCInput is the OpenID Connect part of a POST or PATCH.
type OIDCInput struct {
	Issuer       *string   `json:"issuer"`
	ClientID     *string   `json:"clientId"`
	ClientSecret *string   `json:"clientSecret"`
	Scopes       *[]string `json:"scopes"`
}

// GitHubInput is the GitHub part of a POST or PATCH.
type GitHubInput struct {
	BaseURL       *string   `json:"baseUrl"`
	ClientID      *string   `json:"clientId"`
	ClientSecret  *string   `json:"clientSecret"`
	Organizations *[]string `json:"organizations"`
}

// AppleInput is the team id, the key id and the key (an AuthKey_<id>.p8) that
// Sign in with Apple's client secret is signed with.
type AppleInput struct {
	TeamID     *string `json:"teamId"`
	KeyID      *string `json:"keyId"`
	PrivateKey *string `json:"privateKey"`
}

func providerInvalid(format string, a ...any) *Error { return coded(400, "invalid", format, a...) }

func cleanText(name, v string, max int) (string, error) {
	v = strings.TrimSpace(v)
	if n := utf8.RuneCountInString(v); n < 1 || n > max || !utf8.ValidString(v) || strings.ContainsFunc(v, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", providerInvalid("%s is 1 to %d characters.", name, max)
	}
	return v, nil
}

func checkIcon(icon string) error {
	if icon == "" {
		return nil
	}
	if name, ok := strings.CutPrefix(icon, "preset:"); ok && slices.Contains(presetIcons, name) {
		return nil
	}
	return providerInvalid("Icon is empty or preset:<name> with one of %s.", strings.Join(presetIcons, ", "))
}

func httpsOnly(name, raw string, path bool) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || len(raw) > 2048 || (!path && u.Path != "" && u.Path != "/") {
		return "", providerInvalid("%s must be an https address.", name)
	}
	u.Scheme, u.Host = "https", strings.ToLower(u.Host)
	if !path {
		u.Path = ""
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func cleanList(name string, in []string, max int) ([]string, error) {
	if len(in) > max {
		return nil, providerInvalid("%s holds at most %d entries.", name, max)
	}
	out := []string{}
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || len(v) > 256 || strings.ContainsFunc(v, func(r rune) bool { return r <= 0x20 || r == 0x7f }) {
			return nil, providerInvalid("%s has an entry that is empty or has spaces.", name)
		}
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out, nil
}

func samlNameIDFormat(v string) (string, error) {
	switch strings.TrimSpace(v) {
	case "":
		return "", nil
	case "persistent", "urn:oasis:names:tc:SAML:2.0:nameid-format:persistent":
		return string(crewjamsaml.PersistentNameIDFormat), nil
	case "emailAddress", "urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress":
		return string(crewjamsaml.EmailAddressNameIDFormat), nil
	case "unspecified", "urn:oasis:names:tc:SAML:1.1:nameid-format:unspecified":
		return string(crewjamsaml.UnspecifiedNameIDFormat), nil
	}
	return "", providerInvalid("nameIdFormat is empty, persistent, emailAddress or unspecified.")
}

func cleanAttr(name, v string) (string, error) {
	v = strings.TrimSpace(v)
	if len(v) > 256 || strings.ContainsFunc(v, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", providerInvalid("%s is at most 256 characters.", name)
	}
	return v, nil
}

// built is a provider ready to be stored, and what to tell about it.
type built struct {
	row     store.ProviderRow
	cfg     provCfg
	secret  string // plaintext, to seal
	changed []string
	refetch bool
	metaErr error
	// curSecret is the stored secret, used for discovery when none is given now.
	curSecret string
	secretSet bool
	// appleKey is the Apple key the client secret is signed with: the one
	// given now (appleKeyPEM is set, and is stored with the row) or the stored one.
	appleKey    *ecdsa.PrivateKey
	appleKeyPEM string
	// shares are the tenants a site SSO provider is offered to, written with
	// the row when sharesSet.
	shares    []string
	sharesSet bool
}

// appleSecretNow signs a client secret with what b holds (to check the
// settings with the library); "" without a key.
func (s *Service) appleSecretNow(b *built) string {
	if b.appleKey == nil {
		return ""
	}
	v, _, err := signAppleSecret(b.appleKey, b.cfg.AppleTeamID, b.cfg.AppleKeyID, b.cfg.ClientID, s.now())
	if err != nil {
		return ""
	}
	return v
}

// expandTemplate turns a POST with a template into the ordinary one it
// stands for: the template's kind, class, name and icon (the latter two unless
// given), its slug unless one is given, and the client in the kind's block.
func expandTemplate(in ProviderInput, tm ProviderTemplate) ProviderInput {
	kind, cls := tm.Kind, tm.Class
	in.Kind, in.Class = &kind, &cls
	if in.Slug == nil {
		v := tm.ID
		in.Slug = &v
	}
	if in.Name == nil {
		v := tm.Name
		in.Name = &v
	}
	if in.Icon == nil {
		v := tm.Icon
		in.Icon = &v
	}
	if tm.Kind == KindGitHub && in.GitHub == nil {
		base := tm.Issuer
		in.GitHub = &GitHubInput{BaseURL: &base}
	}
	return in
}

// foldClient puts a top-level clientId and clientSecret (the shape of a POST
// with a template) into the block of the provider's kind.
func foldClient(in ProviderInput, kind string) (ProviderInput, error) {
	if in.ClientID == nil && in.ClientSecret == nil {
		return in, nil
	}
	both := func(a, b *string) (*string, error) {
		if a != nil && b != nil && *a != *b {
			return nil, providerInvalid("Give the client once, at the top or in the %s block.", kind)
		}
		if a != nil {
			return a, nil
		}
		return b, nil
	}
	var err error
	switch kind {
	case KindOIDC:
		o := OIDCInput{}
		if in.OIDC != nil {
			o = *in.OIDC
		}
		if o.ClientID, err = both(in.ClientID, o.ClientID); err != nil {
			return in, err
		}
		if o.ClientSecret, err = both(in.ClientSecret, o.ClientSecret); err != nil {
			return in, err
		}
		in.OIDC = &o
	case KindGitHub:
		g := GitHubInput{}
		if in.GitHub != nil {
			g = *in.GitHub
		}
		if g.ClientID, err = both(in.ClientID, g.ClientID); err != nil {
			return in, err
		}
		if g.ClientSecret, err = both(in.ClientSecret, g.ClientSecret); err != nil {
			return in, err
		}
		in.GitHub = &g
	default:
		return in, providerInvalid("A SAML provider has no client id or secret.")
	}
	in.ClientID, in.ClientSecret = nil, nil
	return in, nil
}

// classFor is the class of a new provider: the one asked for, or its kind's
// (GitHub is social, a configured OpenID Connect or SAML provider enterprise
// sign-on). An organisation's own provider is its single sign-on, and GitHub
// is never anyone's.
func classFor(in ProviderInput, owner string) (string, error) {
	cls := ClassSSO
	if *in.Kind == KindGitHub {
		cls = ClassSocial
	}
	if in.Class != nil {
		cls = *in.Class
	}
	switch {
	case cls != ClassSocial && cls != ClassSSO:
		return "", providerInvalid("Class is social or sso.")
	case owner != store.ProviderSite && cls != ClassSSO:
		return "", providerInvalid("An organisation's own provider is its single sign-on (class sso).")
	case *in.Kind == KindGitHub && cls != ClassSocial:
		return "", providerInvalid("GitHub is social sign-in (class social).")
	}
	return cls, nil
}

// applyApple checks and takes the team id, the key id and the key of Sign in
// with Apple; all three are needed when the provider is made. The key is
// stored as a secret of the site (CreateProvider, UpdateProvider).
func (s *Service) applyApple(b *built, in *AppleInput, creating bool, mark func(string)) error {
	if in != nil {
		if in.TeamID != nil {
			if !appleIDRE.MatchString(strings.TrimSpace(*in.TeamID)) {
				return providerInvalid("apple.teamId is the 10 letters and digits of the team's id.")
			}
			if v := strings.TrimSpace(*in.TeamID); v != b.cfg.AppleTeamID {
				b.cfg.AppleTeamID = v
				mark("apple.teamId")
			}
		}
		if in.KeyID != nil {
			if !appleIDRE.MatchString(strings.TrimSpace(*in.KeyID)) {
				return providerInvalid("apple.keyId is the 10 letters and digits of the key's id.")
			}
			if v := strings.TrimSpace(*in.KeyID); v != b.cfg.AppleKeyID {
				b.cfg.AppleKeyID = v
				mark("apple.keyId")
			}
		}
		if in.PrivateKey != nil {
			k, err := parseAppleKey(*in.PrivateKey)
			if err != nil {
				return coded(400, "apple_key_invalid", "apple.privateKey must be the key Apple gave (an AuthKey_<key id>.p8 file: a P-256 private key in PEM).")
			}
			b.appleKeyPEM, b.appleKey = strings.TrimSpace(*in.PrivateKey), k
			mark("apple.privateKey")
		}
	}
	if creating && (b.cfg.AppleTeamID == "" || b.cfg.AppleKeyID == "" || b.appleKeyPEM == "") {
		return providerInvalid("Sign in with Apple needs apple.teamId, apple.keyId and apple.privateKey.")
	}
	return nil
}

// reservedSlugs are the slugs of the server's own providers, with the kind
// that alone may have each ("" for none: a plain Git address is no provider to
// sign in with).
var reservedSlugs = map[string]string{store.ProviderGitHub: KindGitHub, store.ProviderGit: ""}

var providerMu sync.Mutex // serialises changes: the checks and the write are one step

// applyInput checks input against the current provider (nil when creating) and
// returns what to store. Nothing is changed or fetched here.
func (s *Service) applyInput(ctx context.Context, cur *providerRT, in ProviderInput, rows []store.ProviderRow) (*built, error) {
	creating := cur == nil
	b := &built{}
	var tmpl *ProviderTemplate
	if creating && in.Template != nil {
		tm, ok := templateOf(*in.Template)
		if !ok {
			ids := make([]string, len(providerTemplates))
			for i, t := range providerTemplates {
				ids[i] = t.ID
			}
			return nil, coded(400, "template_unknown", "Template is one of %s.", strings.Join(ids, ", "))
		}
		if in.Owner != nil && *in.Owner != "" && *in.Owner != store.ProviderSite {
			return nil, providerInvalid("Templates are the site's social sign-in: an organisation's single sign-on is OpenID Connect or SAML.")
		}
		if in.Kind != nil && *in.Kind != tm.Kind || in.Class != nil && *in.Class != tm.Class {
			return nil, providerInvalid("A template's kind and class are its own.")
		}
		tmpl, in = &tm, expandTemplate(in, tm)
	} else if !creating && in.Template != nil && *in.Template != cur.row.Template {
		return nil, providerInvalid("The template cannot be changed.")
	}
	if creating {
		b.row = store.ProviderRow{Enabled: true, TenantID: store.DefaultTenantID, Owner: store.ProviderSite}
		if in.Slug == nil || !slugRE.MatchString(*in.Slug) {
			return nil, providerInvalid("Slug is 1 to 32 characters of a-z, 0-9 and -, starting with a letter or digit.")
		}
		if in.Owner != nil && *in.Owner != "" && *in.Owner != store.ProviderSite {
			t, err := store.GetTenant(ctx, s.st.Q(), *in.Owner)
			if errors.Is(err, store.ErrNotFound) {
				return nil, providerInvalid("Owner is site or the id of a tenant.")
			} else if err != nil {
				return nil, err
			}
			// A tenant's provider puts its new accounts in that tenant, and
			// starts off: it can be turned on only once the tenant has a
			// verified domain (see orgSignInGate).
			b.row.Owner, b.row.TenantID, b.row.Enabled = t.ID, t.ID, false
			// Its slug starts with its tenant's and "-" (acme-okta): plain
			// names (sso, okta) stay the site's to take, and no tenant
			// squats another's.
			if !strings.HasPrefix(*in.Slug, t.Slug+"-") || len(*in.Slug) == len(t.Slug)+1 {
				return nil, providerInvalid("An organisation's provider slug starts with its own slug and a hyphen: %s-….", t.Slug)
			}
		}
		// A site provider's id is its slug; a tenant's is generated. Slugs
		// are unique on the whole server: they are in the callback addresses
		// registered at each identity provider.
		b.row.ID = *in.Slug
		if b.row.Owner != store.ProviderSite {
			b.row.ID = s.newID()
		}
		if in.Kind == nil || !slices.Contains([]string{KindOIDC, KindSAML, KindGitHub}, *in.Kind) {
			return nil, providerInvalid("Kind is oidc, saml or github.")
		}
		// The ids of the server's own providers belong to their kind: a site
		// provider's id is its slug, and links, installations and remotes
		// name a provider by its id.
		if k, ok := reservedSlugs[*in.Slug]; ok && k != *in.Kind {
			return nil, coded(409, "slug_reserved", "The slug %s is reserved for the server's own provider.", *in.Slug)
		}
		// The templates' slugs are the site's: slugs are unique on the
		// server, so an organisation that took one first would keep the site
		// from adding that sign-in under its usual address.
		if _, ok := templateOf(*in.Slug); ok && b.row.Owner != store.ProviderSite {
			return nil, coded(409, "slug_reserved", "The slug %s is reserved for the site's own sign-in.", *in.Slug)
		}
		b.row.Slug, b.row.Kind = *in.Slug, *in.Kind
		if in.Name == nil {
			return nil, providerInvalid("Name is required.")
		}
		cls, err := classFor(in, b.row.Owner)
		if err != nil {
			return nil, err
		}
		b.row.Class = cls
		if tmpl != nil && tmpl.Kind == KindOIDC {
			// What the template fills is copied into the row: a later
			// release's template does not change it.
			b.row.Template = tmpl.ID
			b.cfg.Issuer, b.cfg.AuthURL, b.cfg.TokenURL, b.cfg.JWKSURL = tmpl.Issuer, tmpl.AuthorizationEndpoint, tmpl.TokenEndpoint, tmpl.JWKSURI
			b.cfg.AuthMethods, b.cfg.ResponseMode = slices.Clone(tmpl.authMethods), tmpl.ResponseMode
			b.cfg.Scopes = slices.Clone(tmpl.Scopes)
		}
	} else {
		if in.Class != nil && *in.Class != cur.row.Class {
			return nil, providerInvalid("The class cannot be changed: it is chosen when the provider is made.")
		}
		b.row, b.cfg, b.curSecret = cur.row, cur.cfg, cur.secret
		if cur.apple != nil {
			b.appleKey = cur.apple.key
		}
		b.cfg.AuthMethods = slices.Clone(cur.cfg.AuthMethods)
		b.cfg.Scopes = slices.Clone(cur.cfg.Scopes)
		b.cfg.Organizations = slices.Clone(cur.cfg.Organizations)
		if cur.cfg.Groups != nil {
			g := *cur.cfg.Groups
			g.Map = slices.Clone(g.Map)
			b.cfg.Groups = &g
		}
		if in.Slug != nil && *in.Slug != cur.row.Slug || in.Kind != nil && *in.Kind != cur.row.Kind {
			return nil, providerInvalid("Slug and kind cannot be changed.")
		}
		if in.Owner != nil && *in.Owner != cur.row.Owner && !(*in.Owner == "" && cur.row.Owner == store.ProviderSite) {
			return nil, providerInvalid("The owner cannot be changed.")
		}
	}
	kind := b.row.Kind
	in, err := foldClient(in, kind)
	if err != nil {
		return nil, err
	}
	if (in.OIDC != nil && kind != KindOIDC) || (in.SAML != nil && kind != KindSAML) || (in.GitHub != nil && kind != KindGitHub) ||
		(in.Apple != nil && b.row.Template != TemplateApple) {
		return nil, providerInvalid("The settings are for another kind of provider.")
	}
	if s.cfg.PublicURL == "" {
		return nil, coded(400, "public_url_required", "Providers need --public-url: it is part of what the identity provider is told.")
	}
	mark := func(f string) { b.changed = append(b.changed, f) }
	if in.Name != nil {
		n, err := cleanText("name", *in.Name, 60)
		if err != nil {
			return nil, err
		}
		if n != b.row.Name {
			mark("name")
		}
		b.row.Name = n
	}
	if in.Icon != nil {
		if err := checkIcon(*in.Icon); err != nil {
			return nil, err
		}
		if *in.Icon != b.row.Icon {
			mark("icon")
		}
		b.row.Icon = *in.Icon
	}
	if in.Enabled != nil {
		if *in.Enabled != b.row.Enabled {
			mark("enabled")
		}
		b.row.Enabled = *in.Enabled
	}
	if in.Order != nil {
		if *in.Order < -100000 || *in.Order > 100000 {
			return nil, providerInvalid("Order is -100000 to 100000.")
		}
		if *in.Order != b.row.Position {
			mark("order")
		}
		b.row.Position = *in.Order
	}
	if in.Tenant != nil && b.row.Owner != store.ProviderSite && *in.Tenant != b.row.Owner {
		return nil, providerInvalid("A tenant's provider puts new accounts in that tenant.")
	}
	if in.Tenant != nil {
		t, err := store.GetTenant(ctx, s.st.Q(), *in.Tenant)
		if errors.Is(err, store.ErrNotFound) {
			return nil, providerInvalid("Tenant is the id of a tenant.")
		} else if err != nil {
			return nil, err
		}
		if t.Disabled && *in.Tenant != b.row.TenantID {
			return nil, providerInvalid("That tenant is disabled.")
		}
		if *in.Tenant != b.row.TenantID {
			mark("tenant")
		}
		b.row.TenantID = *in.Tenant
	}
	if in.Tenants != nil {
		if err := s.applyTenants(ctx, b, cur, in.Tenants); err != nil {
			return nil, err
		}
	}
	if in.AutoCreate != nil {
		if *in.AutoCreate != b.row.AutoCreate {
			mark("autoCreate")
		}
		b.row.AutoCreate = *in.AutoCreate
	}
	if in.MergeVerifiedEmail != nil {
		if *in.MergeVerifiedEmail && b.row.Owner != store.ProviderSite {
			return nil, providerInvalid("Only the site's own providers can vouch for addresses; an organisation's vouch for its verified domains.")
		}
		if *in.MergeVerifiedEmail != b.row.MergeVerifiedEmail {
			mark("mergeVerifiedEmail")
		}
		b.row.MergeVerifiedEmail = *in.MergeVerifiedEmail
	}
	secretSet := func(v *string) {
		if v != nil {
			b.secret, b.secretSet = *v, true
			mark("clientSecret")
		}
	}

	switch kind {
	case KindOIDC:
		o := in.OIDC
		fromTemplate := b.row.Template != ""
		if o != nil {
			if fromTemplate && o.Issuer != nil && *o.Issuer != b.cfg.Issuer {
				// The addresses were copied from the template with the issuer.
				return nil, coded(400, "template_fixed", "The issuer of a provider made from a template is the template's.")
			}
			if b.row.Template == TemplateApple && o.ClientSecret != nil {
				return nil, providerInvalid("Apple's client secret is made by the server: give apple.teamId, apple.keyId and apple.privateKey.")
			}
			if o.Issuer != nil && !fromTemplate {
				iss, err := httpsOnly("oidc.issuer", *o.Issuer, true)
				if err != nil {
					return nil, coded(400, "oidc_issuer_invalid", "oidc.issuer must be an https address.")
				}
				if iss != b.cfg.Issuer {
					mark("oidc.issuer")
					b.refetch = true
				}
				b.cfg.Issuer = iss
			}
			if o.ClientID != nil {
				id, err := cleanText("oidc.clientId", *o.ClientID, 512)
				if err != nil {
					return nil, err
				}
				if id != b.cfg.ClientID {
					mark("oidc.clientId")
					b.refetch = true
				}
				b.cfg.ClientID = id
			}
			if o.Scopes != nil {
				sc, err := cleanList("oidc.scopes", *o.Scopes, 32)
				if err != nil {
					return nil, err
				}
				if !slices.Equal(sc, b.cfg.Scopes) {
					mark("oidc.scopes")
					b.refetch = true
				}
				b.cfg.Scopes = sc
			}
			if o.ClientSecret != nil && len(*o.ClientSecret) > 16*1024 {
				return nil, providerInvalid("oidc.clientSecret is too long.")
			}
			secretSet(o.ClientSecret)
		}
		if b.cfg.Issuer == "" {
			return nil, coded(400, "oidc_issuer_invalid", "oidc.issuer must be an https address.")
		}
		if b.cfg.ClientID == "" {
			return nil, providerInvalid("oidc.clientId is required.")
		}
		if len(b.cfg.Scopes) == 0 && creating && (o == nil || o.Scopes == nil) {
			b.cfg.Scopes = []string{"profile", "email"}
		}
		b.row.Issuer = b.cfg.Issuer
		if b.row.Template == TemplateApple {
			if err := s.applyApple(b, in.Apple, creating, mark); err != nil {
				return nil, err
			}
		} else if fromTemplate && creating && b.secret == "" {
			return nil, providerInvalid("clientSecret is required.")
		}
		// The settings must be ones the library accepts (a multi-tenant
		// issuer is not one string: its client is tenantflow.go's).
		if multiTenant(b.cfg.Issuer) {
			if !fromTemplate {
				return nil, coded(400, "oidc_issuer_invalid", "oidc.issuer must be an https address.")
			}
		} else if _, err := authoidc.New(canceled(), authoidc.Config{IssuerURL: b.cfg.Issuer, ClientID: b.cfg.ClientID, RedirectURL: s.publicBase() + redirectPath(b.row), Scopes: b.cfg.Scopes}); errors.Is(err, authoidc.ErrInvalidConfig) {
			return nil, providerInvalid("The OpenID Connect settings are not valid (issuer, client id or scopes).")
		}
	case KindGitHub:
		g := in.GitHub
		hasApp := len(b.cfg.App) > 0
		if g != nil && hasApp && (g.BaseURL != nil || g.ClientID != nil || g.ClientSecret != nil) {
			return nil, coded(400, "github_app_client", "This provider's client is the GitHub App's: change it with the App's settings.")
		}
		if g != nil {
			if g.BaseURL != nil {
				u, err := httpsOnly("github.baseUrl", *g.BaseURL, false)
				if err != nil {
					return nil, err
				}
				if u != b.cfg.BaseURL {
					mark("github.baseUrl")
				}
				b.cfg.BaseURL = u
			}
			if g.ClientID != nil {
				id, err := cleanText("github.clientId", *g.ClientID, 512)
				if err != nil {
					return nil, err
				}
				if id != b.cfg.ClientID {
					mark("github.clientId")
				}
				b.cfg.ClientID = id
			}
			if g.Organizations != nil {
				orgs, err := cleanList("github.organizations", *g.Organizations, 50)
				if err != nil {
					return nil, err
				}
				if !slices.Equal(orgs, b.cfg.Organizations) {
					mark("github.organizations")
				}
				b.cfg.Organizations = orgs
			}
			if g.ClientSecret != nil && len(*g.ClientSecret) > 16*1024 {
				return nil, providerInvalid("github.clientSecret is too long.")
			}
			secretSet(g.ClientSecret)
		}
		if creating && b.cfg.BaseURL == "" {
			b.cfg.BaseURL = "https://github.com"
		}
		if hasApp {
			// Signing in needs the App's client id and secret.
			if b.row.Enabled && (cur == nil || !cur.row.Enabled) && (b.cfg.ClientID == "" || cur == nil || cur.secret == "") {
				return nil, coded(400, "github_app_client", "The GitHub App has no client id and secret: signing in with GitHub needs them.")
			}
		} else {
			if b.cfg.ClientID == "" {
				return nil, providerInvalid("github.clientId is required.")
			}
			if creating && (b.secret == "" || g == nil || g.ClientSecret == nil) {
				return nil, providerInvalid("github.clientSecret is required.")
			}
		}
		b.row.Issuer = b.cfg.BaseURL
		if b.row.AutoCreate && len(b.cfg.Organizations) == 0 {
			return nil, coded(400, "auto_create_needs_org", "Name at least one organisation: anyone with a GitHub account could otherwise make an account here.")
		}
	case KindSAML:
		if !strings.HasPrefix(s.cfg.PublicURL, "https://") {
			return nil, coded(400, "saml_needs_https", "SAML needs an https --public-url: the cookie that crosses from the identity provider must be Secure.")
		}
		m := in.SAML
		if m != nil {
			if m.MetadataURL != nil {
				u := ""
				if strings.TrimSpace(*m.MetadataURL) != "" {
					var err error
					if u, err = httpsOnly("saml.metadataUrl", *m.MetadataURL, true); err != nil {
						return nil, err
					}
				}
				if u != b.cfg.MetadataURL {
					mark("saml.metadataUrl")
					b.refetch = true
				}
				b.cfg.MetadataURL = u
			}
			if m.MetadataXML != nil {
				if len(*m.MetadataXML) > metadataLimit {
					return nil, providerInvalid("saml.metadataXml is too large.")
				}
				if *m.MetadataXML != b.cfg.MetadataXML {
					mark("saml.metadataXml")
					b.refetch = true
				}
				b.cfg.MetadataXML = *m.MetadataXML
			}
			if m.EntityID != nil {
				e := strings.TrimSpace(*m.EntityID)
				if len(e) > 512 || strings.ContainsFunc(e, func(r rune) bool { return r <= 0x20 || r == 0x7f }) {
					return nil, providerInvalid("saml.entityId is at most 512 characters, without spaces.")
				}
				if e != b.cfg.EntityID {
					mark("saml.entityId")
				}
				b.cfg.EntityID = e
			}
			if m.Attributes != nil {
				for _, a := range []struct {
					n string
					v *string
				}{{"saml.attributes.email", &m.Attributes.Email}, {"saml.attributes.name", &m.Attributes.Name}, {"saml.attributes.username", &m.Attributes.Username}} {
					v, err := cleanAttr(a.n, *a.v)
					if err != nil {
						return nil, err
					}
					*a.v = v
				}
				if *m.Attributes != b.cfg.Attributes {
					mark("saml.attributes")
				}
				b.cfg.Attributes = *m.Attributes
			}
			if m.NameIDFormat != nil {
				f, err := samlNameIDFormat(*m.NameIDFormat)
				if err != nil {
					return nil, err
				}
				if f != b.cfg.NameIDFormat {
					mark("saml.nameIdFormat")
				}
				b.cfg.NameIDFormat = f
			}
			if m.SignRequests != nil {
				if *m.SignRequests != b.cfg.SignRequests {
					mark("saml.signRequests")
				}
				b.cfg.SignRequests = *m.SignRequests
			}
			if m.AllowIDPInitiated != nil {
				if *m.AllowIDPInitiated != b.cfg.AllowIDPInitiated {
					mark("saml.allowIdpInitiated")
				}
				b.cfg.AllowIDPInitiated = *m.AllowIDPInitiated
			}
		}
		if (b.cfg.MetadataURL == "") == (strings.TrimSpace(b.cfg.MetadataXML) == "") {
			return nil, providerInvalid("Give exactly one of saml.metadataUrl and saml.metadataXml.")
		}
		if b.cfg.Attributes.Email == "" {
			b.cfg.Attributes.Email = samlDefaultEmailAttr
		}
		b.row.Issuer = b.cfg.IDPEntityID
	}
	if in.Groups != nil {
		if err := s.applyGroups(ctx, b, in.Groups); err != nil {
			return nil, err
		}
	}
	if creating {
		b.changed = nil
	}
	// Issuers may repeat: an account is found by the provider's own id and the
	// subject, never by the issuer, so another provider with the same issuer
	// string (an organisation copying it) reaches none of this one's accounts.
	return b, nil
}

// applyTenants checks and takes to whom a site SSO provider is offered.
func (s *Service) applyTenants(ctx context.Context, b *built, cur *providerRT, in *TenantsInput) error {
	if b.row.Owner != store.ProviderSite || b.row.Class != ClassSSO {
		return providerInvalid("Only the site's single sign-on (class sso) is offered to tenants: social sign-in is pruned by each tenant's signin.social, and a tenant's own provider is its own.")
	}
	var was []string
	wasList := false
	if cur != nil {
		wasList = cur.cfg.Offer == OfferList
		if wasList {
			was = s.provs.sharesOf(cur.row.ID)
		}
	}
	b.sharesSet = true
	if in.All {
		b.cfg.Offer, b.shares = "", nil
		if wasList {
			b.changed = append(b.changed, "tenants")
		}
		return nil
	}
	if len(in.List) > maxShares {
		return providerInvalid("Tenants lists at most %d tenants.", maxShares)
	}
	l := slices.Clone(in.List)
	slices.Sort(l)
	l = slices.Compact(l)
	for _, id := range l {
		if _, err := store.GetTenant(ctx, s.st.Q(), id); errors.Is(err, store.ErrNotFound) {
			return providerInvalid("Tenants are ids of tenants; %s is none.", id)
		} else if err != nil {
			return err
		}
	}
	b.cfg.Offer, b.shares = OfferList, l
	if !wasList || !slices.Equal(was, l) {
		b.changed = append(b.changed, "tenants")
	}
	return nil
}

// --- creating, changing, deleting ---------------------------------------------

// CreateProvider adds a provider. The discovery document or the metadata is
// fetched first; a provider that cannot be reached is stored anyway, disabled
// unless the request asked for it enabled.
func (s *Service) CreateProvider(ctx context.Context, actorID, ip string, in ProviderInput) (*ProviderView, error) {
	providerMu.Lock()
	defer providerMu.Unlock()
	rows, err := store.ListProviders(ctx, s.st.Q())
	if err != nil {
		return nil, err
	}
	// The limit is each owner's: one organisation's providers never keep
	// another, or the site, from adding theirs.
	owner := store.ProviderSite
	if in.Owner != nil && *in.Owner != "" {
		owner = *in.Owner
	}
	if n := len(slices.DeleteFunc(slices.Clone(rows), func(o store.ProviderRow) bool { return o.Owner != owner })); n >= maxProviders {
		return nil, coded(409, "provider_limit", "At most %d providers.", maxProviders)
	}
	// Slugs are unique on the whole server, whoever owns the provider: the
	// answer says only that it is taken.
	taken := func(slug string) bool {
		return slices.ContainsFunc(rows, func(o store.ProviderRow) bool { return o.Slug == slug })
	}
	if in.Slug != nil && taken(*in.Slug) {
		return nil, coded(409, "exists", "That slug is taken.")
	}
	b, err := s.applyInput(ctx, nil, in, rows)
	if err != nil {
		return nil, err
	}
	if taken(b.row.Slug) {
		return nil, coded(409, "exists", "That slug is taken.")
	}
	if b.row.Template == TemplateApple && slices.ContainsFunc(rows, func(o store.ProviderRow) bool { return o.Template == TemplateApple }) {
		// Its key is the site's one secret signin.appleKey.
		return nil, coded(409, "exists", "The site has a Sign in with Apple provider already.")
	}
	now := s.now().UTC()
	b.row.Created, b.row.Updated = now, now
	enabledAsked := in.Enabled != nil && *in.Enabled
	if b.row.Kind == KindSAML {
		if err := s.makeSAMLKey(b); err != nil {
			return nil, err
		}
	}
	if err := s.sealSecret(b); err != nil {
		return nil, err
	}
	rt := s.fetchInto(ctx, b)
	if b.metaErr != nil && !errors.Is(b.metaErr, errUnreachable) {
		return nil, b.metaErr
	}
	if err := s.writeBuilt(b, true); err != nil {
		return nil, err
	}
	if !rt.statusOK && !enabledAsked {
		b.row.Enabled = false
	}
	if in.Enabled == nil && rt.statusOK && b.row.Owner == store.ProviderSite {
		b.row.Enabled = true
	}
	if b.row.Enabled {
		if err := s.orgSignInGate(ctx, b.row.Owner); err != nil {
			return nil, err
		}
	}
	if b.row.Enabled && s.sec(ctx).Login.SSOOnly {
		if err := s.needPasswordAdmin(ctx); err != nil {
			return nil, err
		}
	}
	if err := s.writeBuilt(b, true); err != nil {
		return nil, err
	}
	err = s.st.Tx(ctx, func(q store.Q) error {
		if err := store.InsertProvider(ctx, q, b.row); err != nil {
			return err
		}
		if b.sharesSet {
			if err := store.SetProviderShares(ctx, q, b.row.ID, b.shares); err != nil {
				return err
			}
		}
		return s.storeAppleKey(ctx, q, b, actorID)
	})
	if err != nil {
		if store.IsUnique(err) {
			return nil, coded(409, "exists", "That slug is taken.")
		}
		return nil, err
	}
	if err := s.reloadProviders(ctx, b.row.ID); err != nil {
		return nil, err
	}
	// A tenant's own provider has rows of its own kind, its tenant's: the site's are site-only.
	auditKind := "provider_created"
	if b.row.Owner != store.ProviderSite {
		auditKind = "tenant_provider_created"
	}
	s.AuditIn(ctx, AuditEvent{Kind: auditKind, Actor: actorID, IP: ip, Tenant: auditTenant(b.row.Owner), Detail: map[string]any{"slug": b.row.Slug, "kind": b.row.Kind, "name": b.row.Name}})
	return s.Provider(ctx, b.row.ID)
}

// storeAppleKey keeps the Apple key given now, in the provider's write.
func (s *Service) storeAppleKey(ctx context.Context, q store.Q, b *built, actorID string) error {
	if b.appleKeyPEM == "" {
		return nil
	}
	return secrets.Of(s.st, s.rawBox()).Set(ctx, q, "", secrets.AppleSignInKey, b.appleKeyPEM, actorID)
}

// UpdateProvider changes a provider; only what is sent changes.
func (s *Service) UpdateProvider(ctx context.Context, actorID, ip, id string, in ProviderInput) (*ProviderView, error) {
	providerMu.Lock()
	defer providerMu.Unlock()
	cur := s.provs.getID(id)
	if cur == nil {
		return nil, errf(NotFound, "No such provider.")
	}
	rows, err := store.ListProviders(ctx, s.st.Q())
	if err != nil {
		return nil, err
	}
	b, err := s.applyInput(ctx, cur, in, rows)
	if err != nil {
		return nil, err
	}
	if b.row.Enabled && !cur.row.Enabled {
		if err := s.orgSignInGate(ctx, b.row.Owner); err != nil {
			return nil, err
		}
	}
	if b.row.Enabled && !cur.row.Enabled && s.sec(ctx).Login.SSOOnly {
		if err := s.needPasswordAdmin(ctx); err != nil {
			return nil, err
		}
	}
	b.row.Updated = s.now().UTC()
	b.row.SecretEnc = cur.row.SecretEnc
	b.row.Cache = cur.row.Cache
	if b.secret != "" || secretChanged(in) {
		if err := s.sealSecret(b); err != nil {
			return nil, err
		}
	}
	if b.refetch {
		b.row.Cache = ""
		rt := s.fetchInto(ctx, b)
		if b.metaErr != nil && !errors.Is(b.metaErr, errUnreachable) {
			return nil, b.metaErr
		}
		_ = rt
		if err := s.writeBuilt(b, false); err != nil {
			return nil, err
		}
	} else {
		if err := s.writeBuilt(b, false); err != nil {
			return nil, err
		}
		b.row.StatusOK, b.row.StatusChecked, b.row.StatusMessage = cur.statusOK, cur.statusChecked, cur.statusMessage
	}
	// The row and the memberships of entries that are gone or now point
	// elsewhere change together; a sign-in's sync waits for both and then sees
	// the new groups version (see SyncProviderGroups).
	var dropped []string
	err = s.st.Tx(ctx, func(q store.Q) error {
		if err := store.UpdateProvider(ctx, q, b.row); err != nil {
			return err
		}
		if err := s.storeAppleKey(ctx, q, b, actorID); err != nil {
			return err
		}
		if b.sharesSet {
			if err := store.SetProviderShares(ctx, q, id, b.shares); err != nil {
				return err
			}
		}
		var err error
		dropped, err = s.dropUnmapped(ctx, q, id, cur.cfg.Groups, b.cfg.Groups)
		return err
	})
	var he *store.GitHubHostError
	if errors.As(err, &he) {
		return nil, coded(409, "provider_host_mismatch", "%d account(s) are linked to this provider on %s, so its base URL cannot change to %s. Delete the provider, or remove its linked accounts, first.", he.Links, he.Have, he.Want)
	}
	if err != nil {
		return nil, err
	}
	s.cfg.Spaces.MembershipChanged(dropped...)
	if err := s.reloadProviders(ctx, id); err != nil {
		return nil, err
	}
	if len(b.changed) > 0 {
		d := map[string]any{"slug": cur.row.Slug, "kind": b.row.Kind}
		for _, f := range b.changed {
			d[f] = changedValue(b, f)
		}
		auditKind := "provider_changed"
		if cur.row.Owner != store.ProviderSite {
			auditKind = "tenant_provider_changed"
		}
		s.AuditIn(ctx, AuditEvent{Kind: auditKind, Actor: actorID, IP: ip, Tenant: auditTenant(cur.row.Owner), Detail: d})
	}
	return s.Provider(ctx, id)
}

// auditTenant is the tenant a provider's audit rows belong to: its own
// tenant's for a tenant's provider (its administrators read them), none for
// the site's.
func auditTenant(owner string) string {
	if owner == store.ProviderSite {
		return ""
	}
	return owner
}

func secretChanged(in ProviderInput) bool {
	return in.ClientSecret != nil || in.OIDC != nil && in.OIDC.ClientSecret != nil || in.GitHub != nil && in.GitHub.ClientSecret != nil
}

// sealSecret seals the plaintext secret into the row ("" clears it).
func (s *Service) sealSecret(b *built) error {
	if b.row.Kind == KindSAML {
		return nil // sealed by makeSAMLKey
	}
	if b.secret == "" {
		b.row.SecretEnc = ""
		return nil
	}
	enc, err := s.seal("provider:"+b.row.ID, b.secret)
	if err != nil {
		return err
	}
	b.row.SecretEnc = enc
	return nil
}

// writeBuilt puts the config into the row.
func (s *Service) writeBuilt(b *built, creating bool) error {
	raw, err := json.Marshal(b.cfg)
	if err != nil {
		return err
	}
	b.row.Config = string(raw)
	b.row.Issuer = b.cfg.issuerFor(b.row.Kind)
	return nil
}

func (c provCfg) issuerFor(kind string) string {
	switch kind {
	case KindOIDC:
		return c.Issuer
	case KindGitHub:
		return c.BaseURL
	}
	return c.IDPEntityID
}

// DeleteProvider removes a provider and the accounts its people linked to it.
// Without force it is refused while some account has no other way to sign in.
func (s *Service) DeleteProvider(ctx context.Context, actorID, ip, id string, force bool) error {
	providerMu.Lock()
	defer providerMu.Unlock()
	cur := s.provs.getID(id)
	if cur == nil {
		return errf(NotFound, "No such provider.")
	}
	if !force {
		n, err := store.UsersWithOnlyProvider(ctx, s.st.Q(), id)
		if err != nil {
			return err
		}
		if n > 0 {
			e := coded(409, "provider_in_use", "%d account(s) can sign in only through this provider; delete it with force=1 to remove it anyway.", n)
			e.Accounts = &n
			return e
		}
	}
	// What the provider brought goes with it, in the same transaction, and the
	// linked accounts with the row (by cascade).
	var dropped []string
	err := s.st.Tx(ctx, func(q store.Q) error {
		if err := store.DeleteProvider(ctx, q, id); err != nil {
			return err
		}
		// The sessions that started through it end with it.
		if err := store.EndSessionsByMethod(ctx, q, "sso:"+cur.row.Slug, store.EndElsewhere, s.now()); err != nil {
			return err
		}
		if cur.row.Template == TemplateApple {
			// Its key goes with it.
			if err := secrets.Of(s.st, s.rawBox()).Clear(ctx, q, "", secrets.AppleSignInKey); err != nil {
				return err
			}
		}
		var err error
		dropped, err = s.cfg.Spaces.DropProvider(ctx, q, id)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return errf(NotFound, "No such provider.")
	}
	if err != nil {
		return err
	}
	s.forgetSessions()
	s.cfg.Spaces.MembershipChanged(dropped...)
	if err := s.reloadProviders(ctx); err != nil {
		return err
	}
	auditKind := "provider_deleted"
	if cur.row.Owner != store.ProviderSite {
		auditKind = "tenant_provider_deleted"
	}
	s.AuditIn(ctx, AuditEvent{Kind: auditKind, Actor: actorID, IP: ip, Tenant: auditTenant(cur.row.Owner), Detail: map[string]any{"slug": cur.row.Slug, "kind": cur.row.Kind}})
	return nil
}

// RefreshProvider fetches the discovery document or the metadata now.
func (s *Service) RefreshProvider(ctx context.Context, id string) (*ProviderView, error) {
	providerMu.Lock()
	defer providerMu.Unlock()
	cur := s.provs.getID(id)
	if cur == nil {
		return nil, errf(NotFound, "No such provider.")
	}
	if err := s.refresh(ctx, cur); err != nil && !errors.Is(err, errUnreachable) {
		return nil, err
	}
	if err := s.reloadProviders(ctx, id); err != nil {
		return nil, err
	}
	return s.Provider(ctx, id)
}

// refresh fetches the document of a stored provider and records how it went.
func (s *Service) refresh(ctx context.Context, cur *providerRT) error {
	b := &built{row: cur.row, cfg: cur.cfg, curSecret: cur.secret}
	b.row.Cache = ""
	rt := s.fetchInto(ctx, b)
	err := b.metaErr
	if err != nil && !errors.Is(err, errUnreachable) {
		rt.statusOK, rt.statusMessage = false, "the metadata is not valid"
	}
	cache := b.row.Cache
	if !rt.statusOK && cur.row.Cache != "" {
		cache = cur.row.Cache // keep the last good document
	}
	if err := s.writeBuilt(b, false); err != nil {
		return err
	}
	cur.row.Cache = cache
	if rt.statusOK {
		b.row.Updated = s.now().UTC()
		b.row.SecretEnc = cur.row.SecretEnc
		b.row.Cache = cache
		b.row.Enabled = cur.row.Enabled
		if e := store.UpdateProvider(ctx, s.st.Q(), b.row); e != nil {
			return e
		}
		return nil
	}
	return store.SetProviderStatus(ctx, s.st.Q(), cur.row.ID, false, rt.statusChecked, rt.statusMessage, "", true)
}

var errUnreachable = errors.New("the identity provider could not be reached")

// fetchInto fetches what the provider needs (the OpenID Connect discovery
// document, the SAML metadata), checks it and records the result in b.
func (s *Service) fetchInto(ctx context.Context, b *built) *providerRT {
	rt := &providerRT{row: b.row, cfg: b.cfg}
	now := s.now().UTC()
	rt.statusChecked = now
	switch b.row.Kind {
	case KindOIDC:
		if b.row.Template != "" {
			// Nothing to fetch: the addresses are the template's, read by the
			// library from the row (a multi-tenant one by tenantflow.go).
			doc := templateDoc(b.cfg)
			if !multiTenant(b.cfg.Issuer) {
				secret := b.secretFor()
				if b.row.Template == TemplateApple {
					secret = s.appleSecretNow(b)
				}
				if _, _, err := s.buildOIDC(ctx, b.row.Owner, b.cfg, secret, s.publicBase()+redirectPath(b.row), doc); err != nil {
					rt.statusMessage = oidcReason(err)
					b.metaErr = providerInvalid("The provider's settings are not valid: %s.", oidcReason(err))
					b.setStatus(rt)
					return rt
				}
			}
			b.row.Cache = doc
			rt.statusOK = true
			break
		}
		doc, err := s.discover(ctx, b.row.Owner, b.cfg, b.secretFor(), s.publicBase()+redirectPath(b.row))
		if err != nil {
			rt.statusMessage = oidcReason(err)
			b.metaErr = errUnreachable
			b.setStatus(rt)
			return rt
		}
		b.row.Cache = doc
		rt.statusOK = true
	case KindGitHub:
		rt.statusOK = true // nothing to fetch: GitHub's endpoints are fixed
	case KindSAML:
		xmlText := b.cfg.MetadataXML
		if b.cfg.MetadataURL != "" {
			var err error
			xmlText, err = s.fetchMetadata(ctx, b.row.Owner, b.cfg.MetadataURL)
			if err != nil {
				rt.statusMessage = "the metadata could not be fetched"
				b.metaErr = errUnreachable
				if b.row.Cache != "" {
					// Keep working with the last metadata that was good.
				}
				b.setStatus(rt)
				return rt
			}
		}
		meta, err := parseIDPMetadata(xmlText)
		if err != nil {
			rt.statusMessage = "the metadata is not valid"
			b.metaErr = coded(400, "saml_metadata_invalid", "The identity provider's metadata cannot be used: %s.", err.Error())
			b.setStatus(rt)
			return rt
		}
		b.cfg.IDPEntityID = meta.EntityID
		b.row.Cache = xmlText
		if b.cfg.MetadataURL == "" {
			b.cfg.MetadataXML = xmlText
		}
		rt.statusOK = true
	}
	b.setStatus(rt)
	return rt
}

func redirectPath(row store.ProviderRow) string {
	return "/api/auth/" + row.Kind + "/" + row.Slug + "/callback"
}

func (b *built) secretFor() string {
	if b.secretSet || b.secret != "" {
		return b.secret
	}
	return b.curSecret
}

func (b *built) setStatus(rt *providerRT) {
	b.row.StatusOK, b.row.StatusChecked, b.row.StatusMessage = rt.statusOK, rt.statusChecked, rt.statusMessage
}

// --- SAML material -----------------------------------------------------------

// parseIDPMetadata reads an identity provider's metadata and checks that it can
// be used: a signing certificate and a redirect binding for signing in.
func parseIDPMetadata(text string) (*crewjamsaml.EntityDescriptor, error) {
	data := []byte(text)
	if len(data) == 0 || len(data) > metadataLimit {
		return nil, errors.New("the metadata is empty or too large")
	}
	if err := xrv.Validate(bytes.NewReader(data)); err != nil {
		return nil, errors.New("the metadata is not well-formed XML")
	}
	ent, err := decodeEntity(data)
	if err != nil {
		return nil, errors.New("the metadata is not SAML metadata")
	}
	if ent.EntityID == "" || len(ent.IDPSSODescriptors) == 0 {
		return nil, errors.New("the metadata has no identity provider")
	}
	d := ent.IDPSSODescriptors[0]
	hasCert, hasRedirect := false, false
	for _, k := range d.KeyDescriptors {
		if (k.Use == "" || k.Use == "signing") && len(k.KeyInfo.X509Data.X509Certificates) > 0 {
			hasCert = true
		}
	}
	for _, e := range d.SingleSignOnServices {
		if e.Binding == crewjamsaml.HTTPRedirectBinding && e.Location != "" {
			hasRedirect = true
		}
	}
	if !hasCert {
		return nil, errors.New("the metadata has no signing certificate")
	}
	if !hasRedirect {
		return nil, errors.New("the metadata has no HTTP-Redirect binding for signing in")
	}
	return ent, nil
}

func decodeEntity(data []byte) (*crewjamsaml.EntityDescriptor, error) {
	ent := &crewjamsaml.EntityDescriptor{}
	err := xml.Unmarshal(data, ent)
	if err != nil && strings.Contains(err.Error(), "expected element type <EntityDescriptor> but have <EntitiesDescriptor>") {
		ents := &crewjamsaml.EntitiesDescriptor{}
		if err := xml.Unmarshal(data, ents); err != nil {
			return nil, err
		}
		for i, e := range ents.EntityDescriptors {
			if len(e.IDPSSODescriptors) > 0 {
				return &ents.EntityDescriptors[i], nil
			}
		}
		return nil, errors.New("no identity provider in the metadata")
	}
	return ent, err
}

// fetchMetadata gets metadata from an https address, at most 1 MiB, in 10
// seconds, for a provider of owner (see httpClientFor).
func (s *Service) fetchMetadata(ctx context.Context, owner, rawURL string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	client := *s.httpClientFor(owner)
	client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if r.URL.Scheme != "https" || len(via) >= 3 {
			return http.ErrUseLastResponse
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, metadataLimit+1))
	if err != nil {
		return "", err
	}
	if len(body) > metadataLimit {
		return "", errors.New("too large")
	}
	return string(body), nil
}

func (s *Service) httpClient() *http.Client {
	if s.cfg.HTTPClient != nil {
		return s.cfg.HTTPClient
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// httpClientFor is the client that reaches a provider of owner. A tenant's
// administrators choose its addresses (an issuer, a metadata address), but the
// server makes the connections: they reach public addresses only, checked on
// the address each name resolves to as it is dialled (docs/api.md, "Outgoing
// requests made for a tenant"), with at most 3 redirects. The site's own
// providers, and a client the tests give, are not held to it. Each call a
// tenant's provider makes counts against the tenant's outbound share (3.6 d4).
func (s *Service) httpClientFor(owner string) *http.Client {
	if owner == "" || owner == store.ProviderSite {
		return s.httpClient()
	}
	c := tenantHTTPClient
	if s.cfg.HTTPClient != nil {
		c = s.cfg.HTTPClient
	}
	if s.cfg.Outbound == nil {
		return c
	}
	out := *c
	base := out.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	out.Transport = &outboundTransport{base: base, tenant: owner, take: s.cfg.Outbound}
	return &out
}

// tenantHTTPClient is httpClientFor's client of the tenants' providers. It
// uses no proxy: the check is on the address dialled, which a proxy hides.
var tenantHTTPClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		DialContext:            (&net.Dialer{Timeout: 10 * time.Second, Control: dailyfetch.RefuseAddrs(func(ip netip.Addr) bool { return !dailyfetch.Public(ip) })}).DialContext,
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  10 * time.Second,
		MaxResponseHeaderBytes: dailyfetch.MaxResponseHeaderBytes,
		ForceAttemptHTTP2:      true,
	},
	CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("too many redirects")
		}
		return nil
	},
}

// makeSAMLKey makes the signing key and certificate of a SAML provider (RSA
// 2048, ten years); the key is sealed and never shown.
func (s *Service) makeSAMLKey(b *built) error {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return err
	}
	now := s.now().UTC()
	host := s.publicHost()
	if host == "" {
		host = "petalbook"
	}
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: host + " (" + b.row.Slug + ")"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(10, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	enc, err := s.seal("provider:"+b.row.ID+":key", string(keyPEM))
	if err != nil {
		return err
	}
	b.row.SecretEnc = enc
	b.cfg.Certificate = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	b.cfg.CertificateExpires = tmpl.NotAfter
	return nil
}

func parseSAMLKey(keyPEM, certPEM string) (*rsa.PrivateKey, *x509.Certificate, error) {
	kb, _ := pem.Decode([]byte(keyPEM))
	cb, _ := pem.Decode([]byte(certPEM))
	if kb == nil || cb == nil {
		return nil, nil, errors.New("the signing key or certificate is not readable")
	}
	k, err := x509.ParsePKCS8PrivateKey(kb.Bytes)
	if err != nil {
		return nil, nil, err
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, nil, errors.New("the signing key is not RSA")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, nil, err
	}
	return rk, cert, nil
}

// changedValue is what the audit log says a changed field became: its value,
// except for secrets and large documents, which show only that they changed.
func changedValue(b *built, field string) any {
	switch field {
	case "name":
		return b.row.Name
	case "icon":
		return b.row.Icon
	case "enabled":
		return b.row.Enabled
	case "order":
		return b.row.Position
	case "autoCreate":
		return b.row.AutoCreate
	case "mergeVerifiedEmail":
		return b.row.MergeVerifiedEmail
	case "apple.teamId":
		return b.cfg.AppleTeamID
	case "apple.keyId":
		return b.cfg.AppleKeyID
	case "oidc.issuer":
		return b.cfg.Issuer
	case "oidc.clientId", "github.clientId":
		return b.cfg.ClientID
	case "oidc.scopes":
		return b.cfg.Scopes
	case "github.baseUrl":
		return b.cfg.BaseURL
	case "github.organizations":
		return b.cfg.Organizations
	case "saml.metadataUrl":
		return b.cfg.MetadataURL
	case "saml.entityId":
		return b.cfg.EntityID
	case "saml.attributes":
		return b.cfg.Attributes
	case "saml.nameIdFormat":
		return b.cfg.NameIDFormat
	case "saml.signRequests":
		return b.cfg.SignRequests
	case "saml.allowIdpInitiated":
		return b.cfg.AllowIDPInitiated
	case "groups.enabled":
		return b.cfg.Groups.Enabled
	case "groups.claim":
		return b.cfg.Groups.Claim
	case "groups.missing":
		return orKeep(b.cfg.Groups.Missing)
	case "groups.map":
		return len(b.cfg.Groups.Map)
	case "groups.allow":
		return len(b.cfg.Groups.Allow)
	case "tenants":
		if b.cfg.Offer != OfferList {
			return "all"
		}
		return b.shares
	}
	return "changed" // clientSecret, metadataXml
}
