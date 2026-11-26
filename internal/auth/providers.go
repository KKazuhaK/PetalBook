package auth

import (
	"bytes"
	"context"
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
	"net/http"
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

	"petalbook.app/server/internal/secret"
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
	legacyPath    = "/api/auth/oidc/callback"
)

var (
	slugRE      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	presetIcons = []string{"google", "microsoft", "github", "gitlab", "okta", "keycloak", "authentik", "key"}
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
}

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

	mu       sync.Mutex
	oidc     *authoidc.Provider
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
	mu     sync.RWMutex
	list   []*providerRT
	bySlug map[string]*providerRT
	pub    atomic.Pointer[[]PublicProvider]
	// anyOn is whether some provider is enabled.
	anyOn atomic.Bool
}

func newProviderSet() *providerSet {
	p := &providerSet{bySlug: map[string]*providerRT{}}
	empty := []PublicProvider{}
	p.pub.Store(&empty)
	return p
}

func (p *providerSet) get(slug string) *providerRT {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.bySlug[slug]
}

func (p *providerSet) legacy() *providerRT {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, rt := range p.list {
		if rt.row.LegacyCallback && rt.row.Kind == KindOIDC {
			return rt
		}
	}
	return nil
}

func (p *providerSet) anyEnabled() bool { return p.anyOn.Load() }

func (p *providerSet) publicList() []PublicProvider { return *p.pub.Load() }

func (p *providerSet) all() []*providerRT {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return slices.Clone(p.list)
}

func (p *providerSet) set(list []*providerRT) {
	pub := []PublicProvider{}
	by := make(map[string]*providerRT, len(list))
	on := false
	for _, rt := range list {
		by[rt.row.Slug] = rt
		if rt.row.Enabled {
			on = true
			pub = append(pub, PublicProvider{Slug: rt.row.Slug, Kind: rt.row.Kind, Name: rt.row.Name, Icon: rt.row.Icon})
		}
	}
	p.mu.Lock()
	p.list, p.bySlug = list, by
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

func (s *Service) newRT(row store.ProviderRow) *providerRT {
	rt := &providerRT{row: row, statusOK: row.StatusOK, statusChecked: row.StatusChecked, statusMessage: row.StatusMessage}
	if err := json.Unmarshal([]byte(row.Config), &rt.cfg); err != nil {
		rt.secretErr = errors.New("the stored configuration is not readable")
		return rt
	}
	if row.SecretEnc != "" {
		purpose := "provider:" + row.Slug
		if row.Kind == KindSAML {
			purpose += ":key"
		}
		rt.secret, rt.secretErr = s.open(purpose, row.SecretEnc)
	}
	return rt
}

// reloadProviders reads the providers again and rebuilds only those that
// changed; the others keep what they have built.
func (s *Service) reloadProviders(ctx context.Context, rebuild ...string) error {
	rows, err := store.ListProviders(ctx, s.st.Q())
	if err != nil {
		return err
	}
	old := map[string]*providerRT{}
	for _, rt := range s.provs.all() {
		old[rt.row.Slug] = rt
	}
	list := make([]*providerRT, 0, len(rows))
	for _, row := range rows {
		if o := old[row.Slug]; o != nil && !slices.Contains(rebuild, row.Slug) && o.row.Updated.Equal(row.Updated) && o.row.GroupsVersion == row.GroupsVersion {
			list = append(list, o)
			continue
		}
		list = append(list, s.newRT(row))
	}
	s.provs.set(list)
	return nil
}

// --- the view ----------------------------------------------------------------

// ProviderView is a provider as the administrators see it.
type ProviderView struct {
	Slug               string `json:"slug"`
	Kind               string `json:"kind"`
	Name               string `json:"name"`
	Icon               string `json:"icon"`
	Enabled            bool   `json:"enabled"`
	Order              int    `json:"order"`
	AutoCreate         bool   `json:"autoCreate"`
	MergeVerifiedEmail bool   `json:"mergeVerifiedEmail"`
	// Tenant is the tenant autoCreate puts new accounts in (an id).
	Tenant           string       `json:"tenant"`
	OIDC             *OIDCView    `json:"oidc,omitempty"`
	SAML             *SAMLView    `json:"saml,omitempty"`
	GitHub           *GitHubView  `json:"github,omitempty"`
	Groups           GroupsCfg    `json:"groups"`
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
	Issuer         string   `json:"issuer"`
	ClientID       string   `json:"clientId"`
	HasSecret      bool     `json:"hasSecret"`
	Scopes         []string `json:"scopes"`
	LegacyCallback bool     `json:"legacyCallback"`
	RedirectURL    string   `json:"redirectUrl"`
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
}

func (s *Service) publicBase() string { return strings.TrimRight(s.cfg.PublicURL, "/") }

func (s *Service) redirectURL(rt *providerRT) string {
	if rt.row.LegacyCallback && rt.row.Kind == KindOIDC {
		return s.publicBase() + legacyPath
	}
	return s.publicBase() + "/api/auth/" + rt.row.Kind + "/" + rt.row.Slug + "/callback"
}

func (s *Service) samlURLs(slug string) (entity, acs, metaPath string) {
	metaPath = "/api/auth/saml/" + slug + "/metadata"
	return s.publicBase() + metaPath, s.publicBase() + "/api/auth/saml/" + slug + "/acs", metaPath
}

func (s *Service) viewOf(rt *providerRT, people int) ProviderView {
	row := rt.row
	ok, checked, msg := rt.status()
	v := ProviderView{Slug: row.Slug, Kind: row.Kind, Name: row.Name, Icon: row.Icon, Enabled: row.Enabled, Order: row.Position,
		AutoCreate: row.AutoCreate, MergeVerifiedEmail: row.MergeVerifiedEmail, Tenant: row.TenantID, People: people, Groups: groupsView(row.Kind, rt.cfg.Groups),
		Status:  ProviderStat{OK: ok, Message: msg},
		Created: rfc3339(row.Created), Updated: rfc3339(row.Updated), SecretUnreadable: rt.secretErr != nil}
	if !checked.IsZero() {
		v.Status.Checked = rfc3339(checked)
	}
	switch row.Kind {
	case KindOIDC:
		v.OIDC = &OIDCView{Issuer: rt.cfg.Issuer, ClientID: rt.cfg.ClientID, HasSecret: row.SecretEnc != "", Scopes: slices.Clone(rt.cfg.Scopes),
			LegacyCallback: row.LegacyCallback, RedirectURL: s.redirectURL(rt)}
	case KindGitHub:
		orgs := slices.Clone(rt.cfg.Organizations)
		if orgs == nil {
			orgs = []string{}
		}
		v.GitHub = &GitHubView{BaseURL: rt.cfg.BaseURL, ClientID: rt.cfg.ClientID, HasSecret: row.SecretEnc != "", Organizations: orgs, RedirectURL: s.redirectURL(rt)}
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
	people, err := store.ProviderPeople(ctx, s.st.Q())
	if err != nil {
		return nil, err
	}
	list := s.provs.all()
	out := make([]ProviderView, len(list))
	for i, rt := range list {
		out[i] = s.viewOf(rt, people[rt.row.Issuer])
	}
	return out, nil
}

// Provider returns one provider's view.
func (s *Service) Provider(ctx context.Context, slug string) (*ProviderView, error) {
	rt := s.provs.get(slug)
	if rt == nil {
		return nil, errf(NotFound, "No such provider.")
	}
	people, err := store.ProviderPeople(ctx, s.st.Q())
	if err != nil {
		return nil, err
	}
	v := s.viewOf(rt, people[rt.row.Issuer])
	return &v, nil
}

// PublicProviders is what GET /api/auth/providers lists, answered from memory.
func (s *Service) PublicProviders() []PublicProvider { return s.provs.publicList() }

// --- input -------------------------------------------------------------------

// ProviderInput is a POST or PATCH of a provider: any field.
type ProviderInput struct {
	Slug               *string `json:"slug"`
	Kind               *string `json:"kind"`
	Name               *string `json:"name"`
	Icon               *string `json:"icon"`
	Enabled            *bool   `json:"enabled"`
	Order              *int    `json:"order"`
	AutoCreate         *bool   `json:"autoCreate"`
	MergeVerifiedEmail *bool   `json:"mergeVerifiedEmail"`
	Tenant             *string `json:"tenant"`
	OIDC               *struct {
		Issuer         *string   `json:"issuer"`
		ClientID       *string   `json:"clientId"`
		ClientSecret   *string   `json:"clientSecret"`
		Scopes         *[]string `json:"scopes"`
		LegacyCallback *bool     `json:"legacyCallback"`
	} `json:"oidc"`
	SAML *struct {
		MetadataURL       *string `json:"metadataUrl"`
		MetadataXML       *string `json:"metadataXml"`
		EntityID          *string `json:"entityId"`
		Attributes        *SAMLAttrs
		NameIDFormat      *string `json:"nameIdFormat"`
		SignRequests      *bool   `json:"signRequests"`
		AllowIDPInitiated *bool   `json:"allowIdpInitiated"`
	} `json:"saml"`
	GitHub *struct {
		BaseURL       *string   `json:"baseUrl"`
		ClientID      *string   `json:"clientId"`
		ClientSecret  *string   `json:"clientSecret"`
		Organizations *[]string `json:"organizations"`
	} `json:"github"`
	Groups *groupsInput `json:"groups"`
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
}

var providerMu sync.Mutex // serialises changes: the checks and the write are one step

// applyInput checks input against the current provider (nil when creating) and
// returns what to store. Nothing is changed or fetched here.
func (s *Service) applyInput(ctx context.Context, cur *providerRT, in ProviderInput, rows []store.ProviderRow) (*built, error) {
	creating := cur == nil
	b := &built{}
	if creating {
		b.row = store.ProviderRow{Enabled: true, TenantID: store.DefaultTenantID}
		if in.Slug == nil || !slugRE.MatchString(*in.Slug) {
			return nil, providerInvalid("Slug is 1 to 32 characters of a-z, 0-9 and -, starting with a letter or digit.")
		}
		if in.Kind == nil || !slices.Contains([]string{KindOIDC, KindSAML, KindGitHub}, *in.Kind) {
			return nil, providerInvalid("Kind is oidc, saml or github.")
		}
		b.row.Slug, b.row.Kind = *in.Slug, *in.Kind
		if in.Name == nil {
			return nil, providerInvalid("Name is required.")
		}
	} else {
		b.row, b.cfg, b.curSecret = cur.row, cur.cfg, cur.secret
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
	}
	kind := b.row.Kind
	if (in.OIDC != nil && kind != KindOIDC) || (in.SAML != nil && kind != KindSAML) || (in.GitHub != nil && kind != KindGitHub) {
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
	if in.AutoCreate != nil {
		if *in.AutoCreate != b.row.AutoCreate {
			mark("autoCreate")
		}
		b.row.AutoCreate = *in.AutoCreate
	}
	if in.MergeVerifiedEmail != nil {
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
		if o != nil {
			if o.Issuer != nil {
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
			if o.LegacyCallback != nil {
				if *o.LegacyCallback != b.row.LegacyCallback {
					mark("oidc.legacyCallback")
				}
				b.row.LegacyCallback = *o.LegacyCallback
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
		// The settings must be ones the library accepts.
		if _, err := authoidc.New(canceled(), authoidc.Config{IssuerURL: b.cfg.Issuer, ClientID: b.cfg.ClientID, RedirectURL: s.publicBase() + legacyPath, Scopes: b.cfg.Scopes}); errors.Is(err, authoidc.ErrInvalidConfig) {
			return nil, providerInvalid("The OpenID Connect settings are not valid (issuer, client id or scopes).")
		}
	case KindGitHub:
		g := in.GitHub
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
		if b.cfg.ClientID == "" {
			return nil, providerInvalid("github.clientId is required.")
		}
		if creating && (b.secret == "" || g == nil || g.ClientSecret == nil) {
			return nil, providerInvalid("github.clientSecret is required.")
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
	// A provider's issuer is its own.
	if b.row.Issuer != "" {
		for _, o := range rows {
			if o.Slug != b.row.Slug && o.Issuer == b.row.Issuer {
				return nil, coded(409, "provider_issuer_in_use", "Another provider already has this issuer.")
			}
		}
	}
	if b.row.LegacyCallback {
		if kind != KindOIDC {
			return nil, providerInvalid("Only an OpenID Connect provider has a legacy callback.")
		}
		for _, o := range rows {
			if o.Slug != b.row.Slug && o.LegacyCallback {
				return nil, coded(409, "legacy_callback_in_use", "Another provider has the legacy callback.")
			}
		}
	}
	return b, nil
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
	if len(rows) >= maxProviders {
		return nil, coded(409, "provider_limit", "At most %d providers.", maxProviders)
	}
	for _, o := range rows {
		if in.Slug != nil && o.Slug == *in.Slug {
			return nil, coded(409, "exists", "That slug is taken.")
		}
	}
	b, err := s.applyInput(ctx, nil, in, rows)
	if err != nil {
		return nil, err
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
	// What the metadata names may be an identity provider that has a provider already.
	for _, o := range rows {
		if b.row.Issuer != "" && o.Issuer == b.row.Issuer {
			return nil, coded(409, "provider_issuer_in_use", "Another provider already has this issuer.")
		}
	}
	if !rt.statusOK && !enabledAsked {
		b.row.Enabled = false
	}
	if in.Enabled == nil && rt.statusOK {
		b.row.Enabled = true
	}
	if b.row.Enabled && s.sec(ctx).Login.SSOOnly {
		if err := s.needPasswordAdmin(ctx); err != nil {
			return nil, err
		}
	}
	if err := s.writeBuilt(b, true); err != nil {
		return nil, err
	}
	if err := store.InsertProvider(ctx, s.st.Q(), b.row); err != nil {
		if store.IsUnique(err) {
			return nil, coded(409, "exists", "That slug is taken.")
		}
		return nil, err
	}
	if err := s.reloadProviders(ctx, b.row.Slug); err != nil {
		return nil, err
	}
	s.Audit(AuditEvent{Kind: "provider_created", Actor: actorID, IP: ip, Detail: map[string]any{"slug": b.row.Slug, "kind": b.row.Kind, "name": b.row.Name}})
	return s.Provider(ctx, b.row.Slug)
}

// UpdateProvider changes a provider; only what is sent changes.
func (s *Service) UpdateProvider(ctx context.Context, actorID, ip, slug string, in ProviderInput) (*ProviderView, error) {
	providerMu.Lock()
	defer providerMu.Unlock()
	cur := s.provs.get(slug)
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
		// Metadata that changed may name another identity provider: check again.
		if b.row.Issuer != "" {
			rows2, _ := store.ListProviders(ctx, s.st.Q())
			for _, o := range rows2 {
				if o.Slug != b.row.Slug && o.Issuer == b.row.Issuer {
					return nil, coded(409, "provider_issuer_in_use", "Another provider already has this issuer.")
				}
			}
		}
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
		var err error
		dropped, err = s.dropUnmapped(ctx, q, slug, cur.cfg.Groups, b.cfg.Groups)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.cfg.Spaces.MembershipChanged(dropped...)
	if err := s.reloadProviders(ctx, slug); err != nil {
		return nil, err
	}
	if len(b.changed) > 0 {
		d := map[string]any{"slug": slug, "kind": b.row.Kind}
		for _, f := range b.changed {
			d[f] = changedValue(b, f)
		}
		s.Audit(AuditEvent{Kind: "provider_changed", Actor: actorID, IP: ip, Detail: d})
	}
	return s.Provider(ctx, slug)
}

func secretChanged(in ProviderInput) bool {
	return in.OIDC != nil && in.OIDC.ClientSecret != nil || in.GitHub != nil && in.GitHub.ClientSecret != nil
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
	enc, err := s.seal("provider:"+b.row.Slug, b.secret)
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

// DeleteProvider removes a provider. Its people's identities stay. Without
// force it is refused while some account has no other way to sign in.
func (s *Service) DeleteProvider(ctx context.Context, actorID, ip, slug string, force bool) error {
	providerMu.Lock()
	defer providerMu.Unlock()
	cur := s.provs.get(slug)
	if cur == nil {
		return errf(NotFound, "No such provider.")
	}
	if !force && cur.row.Issuer != "" {
		n, err := store.UsersWithOnlyIssuer(ctx, s.st.Q(), cur.row.Issuer)
		if err != nil {
			return err
		}
		if n > 0 {
			e := coded(409, "provider_in_use", "%d account(s) can sign in only through this provider; delete it with force=1 to remove it anyway.", n)
			e.Accounts = &n
			return e
		}
	}
	// What the provider brought goes with it, in the same transaction; the
	// identities stay.
	var dropped []string
	err := s.st.Tx(ctx, func(q store.Q) error {
		if err := store.DeleteProvider(ctx, q, slug); err != nil {
			return err
		}
		var err error
		dropped, err = s.cfg.Spaces.DropProvider(ctx, q, slug)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return errf(NotFound, "No such provider.")
	}
	if err != nil {
		return err
	}
	s.cfg.Spaces.MembershipChanged(dropped...)
	if err := s.reloadProviders(ctx); err != nil {
		return err
	}
	s.Audit(AuditEvent{Kind: "provider_deleted", Actor: actorID, IP: ip, Detail: map[string]any{"slug": slug, "kind": cur.row.Kind}})
	return nil
}

// RefreshProvider fetches the discovery document or the metadata now.
func (s *Service) RefreshProvider(ctx context.Context, slug string) (*ProviderView, error) {
	providerMu.Lock()
	defer providerMu.Unlock()
	cur := s.provs.get(slug)
	if cur == nil {
		return nil, errf(NotFound, "No such provider.")
	}
	if err := s.refresh(ctx, cur); err != nil && !errors.Is(err, errUnreachable) {
		return nil, err
	}
	if err := s.reloadProviders(ctx, slug); err != nil {
		return nil, err
	}
	return s.Provider(ctx, slug)
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
	if rt.statusOK && b.row.Issuer != cur.row.Issuer && b.row.Issuer != "" {
		rows, _ := store.ListProviders(ctx, s.st.Q())
		for _, o := range rows {
			if o.Slug != b.row.Slug && o.Issuer == b.row.Issuer {
				return coded(409, "provider_issuer_in_use", "Another provider already has this issuer.")
			}
		}
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
	return store.SetProviderStatus(ctx, s.st.Q(), cur.row.Slug, false, rt.statusChecked, rt.statusMessage, "", true)
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
		doc, err := s.discover(ctx, b.cfg, b.secretFor(), s.publicBase()+redirectPath(b.row))
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
			xmlText, err = s.fetchMetadata(ctx, b.cfg.MetadataURL)
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
	if row.LegacyCallback && row.Kind == KindOIDC {
		return legacyPath
	}
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

// fetchMetadata gets metadata from an https address, at most 1 MiB, in 10 seconds.
func (s *Service) fetchMetadata(ctx context.Context, rawURL string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	client := *s.httpClient()
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
	enc, err := s.seal("provider:"+b.row.Slug+":key", string(keyPEM))
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
	case "oidc.issuer":
		return b.cfg.Issuer
	case "oidc.clientId", "github.clientId":
		return b.cfg.ClientID
	case "oidc.scopes":
		return b.cfg.Scopes
	case "oidc.legacyCallback":
		return b.row.LegacyCallback
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
	}
	return "changed" // clientSecret, metadataXml
}
