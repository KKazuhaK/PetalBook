package auth

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"petalbook.app/server/internal/store"
)

// Which sign-ins vouch for an address (3.6). An address a trusted sign-in
// vouches for is verified at once, with verified_by "provider:<slug>"; it then
// follows the rules of every address (store.SetPrimaryEmail): taken from an
// account that holds it unverified, never from one that holds it verified
// (the person signs in to that account and links the provider there).
//
// The rule is here and nowhere else.

// Templates of the providers. The built-in social ones (google, apple,
// microsoft) arrive with 3.6's templates; github is the built-in kind of
// today; oidc and saml are a provider the administrators configured.
const (
	tmplGoogle    = "google"
	tmplApple     = "apple"
	tmplMicrosoft = "microsoft"
	tmplGitHub    = "github"
	tmplOIDC      = "oidc"
	tmplSAML      = "saml"
)

// emailSource is what a sign-in said about an address, and about the
// provider that said it.
type emailSource struct {
	template string
	// org: the provider is owned by a tenant (an organisation's own SSO, OIDC
	// or SAML); its addresses are trusted only on orgDomains, the domains the
	// organisation verified. Until domain verification exists the list is
	// empty, so an organisation's SSO vouches for nothing.
	org        bool
	orgDomains []string
	// trustOptIn is a configured provider's "trust this provider's verified
	// addresses" (its mergeVerifiedEmail switch, off by default). It counts for
	// the site's own providers only, never for an organisation's.
	trustOptIn bool
	// publicGitHub: a GitHub provider is github.com itself. A GitHub
	// Enterprise server's verified flag is its administrators' word (they can
	// mark any address verified), so it counts only with trustOptIn.
	publicGitHub bool
	email        string
	// verified is the email_verified claim (true or "true"), or for GitHub
	// that its API lists the address as verified.
	verified bool
	// claims are an OIDC token's claims (Microsoft's xms_edov is read).
	claims map[string]json.RawMessage
}

// trustsEmail says whether the sign-in vouches for the address.
func trustsEmail(src emailSource) bool {
	if src.email == "" {
		return false
	}
	if src.org {
		return inDomains(src.email, src.orgDomains)
	}
	switch src.template {
	case tmplGoogle, tmplApple:
		return src.verified
	case tmplGitHub:
		return src.verified && (src.publicGitHub || src.trustOptIn)
	case tmplMicrosoft:
		// Never the plain email claim: any Entra tenant can put any address
		// in it ("nOAuth"). Only xms_edov (the address's domain is verified
		// by the tenant that issued the token), or an address on a domain
		// verified here.
		return claimTrue(src.claims, "xms_edov") || inDomains(src.email, src.orgDomains)
	case tmplOIDC:
		return src.trustOptIn && src.verified
	case tmplSAML:
		// SAML has no verified-address claim: the site's own SAML provider
		// vouches for its addresses only by the administrator's opt-in.
		return src.trustOptIn
	default:
		return false
	}
}

// samlDefaultEmailAttr is the attribute a SAML provider's address is read
// from when none is configured.
const samlDefaultEmailAttr = "mail"

// orgDomains is the verified email domains of a tenant. It is the hook for
// 3.6 (d): none until a domain source is configured.
func (s *Service) orgDomains(ctx context.Context, tenantID string) []string {
	if s.cfg.OrgDomains == nil || tenantID == "" || tenantID == store.ProviderSite {
		return nil
	}
	return s.cfg.OrgDomains(ctx, tenantID)
}

// orgSignInGate refuses to turn on a sign-in provider owned by a tenant that
// has no verified domain: an organisation's SSO may vouch only for its own
// domains, so without one it has nothing to say. The site's own are not held.
func (s *Service) orgSignInGate(ctx context.Context, owner string) error {
	if owner == "" || owner == store.ProviderSite {
		return nil
	}
	if len(s.orgDomains(ctx, owner)) == 0 {
		return coded(409, "domain_required", "Verify a domain for this organisation first: its single sign-on can be turned on only once it has one.")
	}
	return nil
}

// signInOn says whether a provider may be used to sign in now: it is enabled,
// and a tenant's also has a verified domain.
func (s *Service) signInOn(ctx context.Context, rt *providerRT) bool {
	if !rt.row.Enabled {
		return false
	}
	return s.orgSignInGate(ctx, rt.row.Owner) == nil
}

// trustSource fills what the provider's own configuration says into what a
// sign-in said: the organisation's verified domains for a tenant's provider,
// the administrator's opt-in for the site's.
func (s *Service) trustSource(ctx context.Context, rt *providerRT, src emailSource, email string) emailSource {
	src.email = email
	if rt.row.Owner != store.ProviderSite {
		src.org, src.orgDomains = true, s.orgDomains(ctx, rt.row.Owner)
		src.trustOptIn = false
	} else {
		src.trustOptIn = rt.row.MergeVerifiedEmail
	}
	return src
}

// isPublicGitHub says whether a GitHub provider's base URL is github.com, not
// a GitHub Enterprise server.
func isPublicGitHub(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	// The same test as githubAPIBase's: only github.com is the public
	// service (its API host is not a base URL).
	return strings.EqualFold(u.Hostname(), "github.com")
}

// claimTrue reads a boolean claim, true or "true".
func claimTrue(claims map[string]json.RawMessage, name string) bool {
	raw, ok := claims[name]
	if !ok {
		return false
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b
	}
	var s string
	return json.Unmarshal(raw, &s) == nil && s == "true"
}

// inDomains says whether the address's domain is one of domains (or below
// one), compared in store.EmailKey form.
func inDomains(email string, domains []string) bool {
	if len(domains) == 0 {
		return false
	}
	key, ok := store.EmailKey(email)
	if !ok {
		return false
	}
	d := key[strings.LastIndex(key, "@")+1:]
	for _, a := range domains {
		if a == "" {
			continue
		}
		if ak, ok := store.EmailKey("x@" + a); ok {
			a = ak[2:]
		}
		if d == a || strings.HasSuffix(d, "."+a) {
			return true
		}
	}
	return false
}
