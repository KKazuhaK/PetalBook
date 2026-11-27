package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// Provider templates (3.6 (d)): the presets of social sign-in that ship with
// the server. A template fills the issuer, the addresses, the scopes, the class
// and the icon of a new provider, and the administrator gives only the client
// (and, for Apple, the key its secret is signed with). What a template filled
// is copied into the row: a template that changes in a later release does not
// change a provider made from it. Nothing of a template is fetched from the
// network, its discovery document least: the addresses are the template's.

// The ids of the templates. github is the built-in kind of the same name; the
// others are OpenID Connect providers (kind oidc) with the template recorded,
// whose trust rules are the template's (emailtrust.go).
const (
	TemplateGoogle    = "google"
	TemplateMicrosoft = "microsoft"
	TemplateApple     = "apple"
	TemplateGitHub    = "github"
	TemplateGitLabCom = "gitlab-com"
)

// Classes of sign-in provider: consumer accounts the site offers on every
// sign-in page, or an organisation's single sign-on.
const (
	ClassSocial = "social"
	ClassSSO    = "sso"
)

// tenantIDPlaceholder stands, in the issuer of a multi-tenant template
// (Microsoft's /common), for the tenant that issued the token.
const tenantIDPlaceholder = "{tenantid}"

// ProviderTemplate is one preset, as GET /api/admin/provider-templates shows it.
type ProviderTemplate struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Class string `json:"class"`
	Icon  string `json:"icon"`
	// Issuer is the OpenID Connect issuer (with {tenantid} for Microsoft), or
	// GitHub's base address.
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorizationEndpoint,omitempty"`
	TokenEndpoint         string   `json:"tokenEndpoint,omitempty"`
	JWKSURI               string   `json:"jwksUri,omitempty"`
	Scopes                []string `json:"scopes"`
	// ResponseMode is form_post when the provider posts its answer (Apple).
	ResponseMode string `json:"responseMode,omitempty"`
	// Secret is what the administrator gives besides the client id: "client"
	// (a client secret) or "apple" (the team id, the key id and the key).
	Secret string `json:"secret"`
	// Common: listed first when a sign-in method is added; Region groups the
	// others under "More templates".
	Common bool   `json:"common"`
	Region string `json:"region"`
	// authMethods are how the token endpoint takes the client's credentials.
	authMethods []string
}

var providerTemplates = []ProviderTemplate{
	{ID: TemplateGoogle, Name: "Google", Kind: KindOIDC, Class: ClassSocial, Icon: "preset:google",
		Issuer:                "https://accounts.google.com",
		AuthorizationEndpoint: "https://accounts.google.com/o/oauth2/v2/auth",
		TokenEndpoint:         "https://oauth2.googleapis.com/token",
		JWKSURI:               "https://www.googleapis.com/oauth2/v3/certs",
		Scopes:                []string{"openid", "email", "profile"}, Secret: "client", Common: true, Region: "global",
		authMethods: []string{"client_secret_post", "client_secret_basic"}},
	{ID: TemplateMicrosoft, Name: "Microsoft", Kind: KindOIDC, Class: ClassSocial, Icon: "preset:microsoft",
		Issuer:                "https://login.microsoftonline.com/" + tenantIDPlaceholder + "/v2.0",
		AuthorizationEndpoint: "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
		TokenEndpoint:         "https://login.microsoftonline.com/common/oauth2/v2.0/token",
		JWKSURI:               "https://login.microsoftonline.com/common/discovery/v2.0/keys",
		Scopes:                []string{"openid", "email", "profile"}, Secret: "client", Common: true, Region: "global",
		authMethods: []string{"client_secret_post", "client_secret_basic"}},
	{ID: TemplateApple, Name: "Apple", Kind: KindOIDC, Class: ClassSocial, Icon: "preset:apple",
		Issuer:                "https://appleid.apple.com",
		AuthorizationEndpoint: "https://appleid.apple.com/auth/authorize",
		TokenEndpoint:         "https://appleid.apple.com/auth/token",
		JWKSURI:               "https://appleid.apple.com/auth/keys",
		Scopes:                []string{"openid", "email", "name"}, ResponseMode: "form_post", Secret: "apple", Common: true, Region: "global",
		authMethods: []string{"client_secret_post"}},
	{ID: TemplateGitHub, Name: "GitHub", Kind: KindGitHub, Class: ClassSocial, Icon: "preset:github",
		Issuer: "https://github.com", Scopes: []string{"read:user", "user:email"}, Secret: "client", Common: true, Region: "global"},
	{ID: TemplateGitLabCom, Name: "GitLab.com", Kind: KindOIDC, Class: ClassSocial, Icon: "preset:gitlab",
		Issuer:                "https://gitlab.com",
		AuthorizationEndpoint: "https://gitlab.com/oauth/authorize",
		TokenEndpoint:         "https://gitlab.com/oauth/token",
		JWKSURI:               "https://gitlab.com/oauth/discovery/keys",
		Scopes:                []string{"openid", "email", "profile"}, Secret: "client", Region: "global",
		authMethods: []string{"client_secret_basic", "client_secret_post"}},
}

// ProviderTemplates lists the templates, the common ones first.
func ProviderTemplates() []ProviderTemplate {
	out := make([]ProviderTemplate, len(providerTemplates))
	for i, t := range providerTemplates {
		t.Scopes = slices.Clone(t.Scopes)
		out[i] = t
	}
	return out
}

func templateOf(id string) (ProviderTemplate, bool) {
	for _, t := range providerTemplates {
		if t.ID == id {
			return t, true
		}
	}
	return ProviderTemplate{}, false
}

// templateDoc is the discovery document of a provider made from a template,
// from what the row holds: the library reads it as it would read the
// provider's own, so nothing is fetched. The issuer, the addresses and the
// methods are the template's, copied into the row when it was made.
func templateDoc(c provCfg) string {
	methods := c.AuthMethods
	if len(methods) == 0 {
		methods = []string{"client_secret_basic"}
	}
	b, _ := json.Marshal(map[string]any{
		"issuer":                                c.Issuer,
		"authorization_endpoint":                c.AuthURL,
		"token_endpoint":                        c.TokenURL,
		"jwks_uri":                              c.JWKSURL,
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public", "pairwise"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"token_endpoint_auth_methods_supported": methods,
		"code_challenge_methods_supported":      []string{"S256"},
	})
	return string(b)
}

// multiTenant says whether an issuer stands for any tenant ({tenantid} in it):
// the token's own issuer is then checked against its tid (tenantflow.go).
func multiTenant(issuer string) bool { return strings.Contains(issuer, tenantIDPlaceholder) }

// --- Sign in with Apple ---------------------------------------------------------

// Apple's client secret is not a fixed string: it is a JWT, signed with ES256 by
// a key of the developer's team, that Apple accepts for at most six months
// (15777000 seconds). The server signs one when it needs one and keeps it until
// it nears its end; nothing renews it on a timer.
const (
	appleAudience = "https://appleid.apple.com"
	// appleSecretLife is how long a secret is made for, under Apple's limit.
	appleSecretLife = 180 * 24 * time.Hour
	// appleSecretRenew: a secret with less than this left is replaced at its
	// next use, so one in use never runs out (nor does one Apple's clock sees
	// as a little later than ours).
	appleSecretRenew = 7 * 24 * time.Hour
)

var (
	appleIDRE = regexp.MustCompile(`^[A-Z0-9]{10}$`)
	// errAppleKey: the key is not a P-256 private key in PKCS #8 PEM (an
	// AuthKey_<id>.p8 as Apple gives it).
	errAppleKey = errors.New("the key is not an Apple private key (.p8)")
)

// parseAppleKey reads an AuthKey_<id>.p8.
func parseAppleKey(text string) (*ecdsa.PrivateKey, error) {
	blk, _ := pem.Decode([]byte(strings.TrimSpace(text)))
	if blk == nil || blk.Type != "PRIVATE KEY" {
		return nil, errAppleKey
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, errAppleKey
	}
	ek, ok := k.(*ecdsa.PrivateKey)
	if !ok || ek.Curve != elliptic.P256() {
		return nil, errAppleKey
	}
	return ek, nil
}

// signAppleSecret makes the client secret: iss the team, sub the client (the
// Services ID), aud Apple, the key id in the header.
func signAppleSecret(key *ecdsa.PrivateKey, teamID, keyID, clientID string, now time.Time) (string, time.Time, error) {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader(jose.HeaderKey("kid"), keyID))
	if err != nil {
		return "", time.Time{}, err
	}
	exp := now.Add(appleSecretLife).Truncate(time.Second)
	payload, err := json.Marshal(map[string]any{"iss": teamID, "iat": now.Unix(), "exp": exp.Unix(), "aud": appleAudience, "sub": clientID})
	if err != nil {
		return "", time.Time{}, err
	}
	obj, err := signer.Sign(payload)
	if err != nil {
		return "", time.Time{}, err
	}
	tok, err := obj.CompactSerialize()
	return tok, exp, err
}

// appleSecret is a provider's Apple key and the secret last signed with it.
type appleSecret struct {
	key                     *ecdsa.PrivateKey
	teamID, keyID, clientID string

	mu    sync.Mutex
	value string
	exp   time.Time
}

// at returns a secret good at now, signing a new one when there is none or
// the one there is nears its end. changed says it is not the one returned last.
func (a *appleSecret) at(now time.Time) (value string, changed bool, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.value != "" && now.Before(a.exp.Add(-appleSecretRenew)) {
		return a.value, false, nil
	}
	v, exp, err := signAppleSecret(a.key, a.teamID, a.keyID, a.clientID, now)
	if err != nil {
		return "", false, err
	}
	a.value, a.exp = v, exp
	return v, true, nil
}

// appleRelayDomain is the domain of the addresses Apple relays to a person's
// own (Hide My Email): one per person and application.
const appleRelayDomain = "privaterelay.appleid.com"

// appleUserName reads the name Apple posts with the first sign-in only (the
// form's user field, not part of the signed token): it is used for the name
// shown and nothing else.
func appleUserName(raw string) string {
	if raw == "" || len(raw) > 4096 {
		return ""
	}
	var u struct {
		Name struct {
			FirstName string `json:"firstName"`
			LastName  string `json:"lastName"`
		} `json:"name"`
	}
	if json.Unmarshal([]byte(raw), &u) != nil {
		return ""
	}
	return strings.TrimSpace(strings.TrimSpace(u.Name.FirstName) + " " + strings.TrimSpace(u.Name.LastName))
}
