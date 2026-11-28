package auth

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strings"

	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
)

// Which sign-ins vouch for an address (3.6). An address a trusted sign-in
// vouches for is verified at once, with verified_by "provider:<slug>"; it then
// follows the rules of every address (store.SetPrimaryEmail): taken from an
// account that holds it unverified, never from one that holds it verified
// (the person signs in to that account and links the provider there).
//
// The rule is here and nowhere else.

// Which rules a provider's addresses follow: the built-in social ones
// (google, apple, microsoft: a provider made from that template), github (the
// built-in kind), and oidc and saml for a provider the administrators
// configured (GitLab.com's template too: see trustTemplate).
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
	// relay: the address is one Apple relays to the person's own (Hide My
	// Email). It is verified, but it is the person's for this site only:
	// it never finds, displaces or takes from another account by address
	// (matchesByAddress), and it routes to no organisation's domain.
	relay bool
}

// trustsEmail says whether the sign-in vouches for the address.
func trustsEmail(src emailSource) bool {
	if src.email == "" {
		return false
	}
	if src.org {
		// An organisation's own single sign-on: only its verified domains,
		// each exactly (a subdomain is claimed by itself).
		return inDomains(src.email, src.orgDomains)
	}
	switch src.template {
	case tmplGoogle, tmplApple:
		return src.verified
	case tmplGitHub:
		return src.verified && (src.publicGitHub || src.trustOptIn)
	case tmplMicrosoft:
		// Only xms_edov: the tenant that issued the token owns the domain of
		// the address. Never the plain email claim, whatever its domain, a
		// domain verified here included: any Entra tenant can put any address
		// in it ("nOAuth"), and the person's tenant is not the one that
		// verified the domain here. (Recorded decision, 2026-11-27: the safe
		// rule; the owner may loosen it later.)
		return claimTrue(src.claims, "xms_edov")
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

// matchesByAddress says whether the sign-in's address may meet another
// account's: send the person to the account that holds it verified
// (email_in_use), or take it from one that holds it unverified. An Apple
// relay address never does.
func matchesByAddress(src emailSource) bool { return !src.relay }

// trustTemplate is the rule a provider's addresses follow: its template's
// for Google, Apple and Microsoft; a configured provider's for any other
// OpenID Connect provider, GitLab.com's template included (its addresses are
// trusted only with the administrators' opt-in, as a configured provider's).
func trustTemplate(row store.ProviderRow) string {
	switch row.Template {
	case TemplateGoogle:
		return tmplGoogle
	case TemplateApple:
		return tmplApple
	case TemplateMicrosoft:
		return tmplMicrosoft
	}
	return tmplOIDC
}

// isAppleRelay says whether an address is one of Apple's relays.
func isAppleRelay(email string) bool {
	key, ok := store.EmailKey(email)
	return ok && key[strings.LastIndex(key, "@")+1:] == appleRelayDomain
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
	if !s.ownSSOOn(ctx, owner) {
		return coded(403, "feature_off", "This organisation does not have its own single sign-on (features.ownSso).")
	}
	if len(s.orgDomains(ctx, owner)) == 0 {
		return coded(409, "domain_required", "Verify a domain for this organisation first: its single sign-on can be turned on only once it has one.")
	}
	return nil
}

// signInOn says whether a provider may be used to sign in now: it is enabled,
// and a tenant's also has features.ownSso and a verified domain. Switching
// the feature off (or the domain lapsing) stops its sign-ins at once and
// deletes nothing: they work again as they were when it is back.
func (s *Service) signInOn(ctx context.Context, rt *providerRT) bool {
	if !rt.row.Enabled {
		return false
	}
	if rt.row.Owner == store.ProviderSite {
		return true
	}
	return s.orgSignInGate(ctx, rt.row.Owner) == nil
}

// ownSSOOn says whether the tenant has features.ownSso; a read that fails
// is off (the sign-in is refused, never let through).
func (s *Service) ownSSOOn(ctx context.Context, tenant string) bool {
	st := s.settings.Load()
	if st == nil {
		return true
	}
	on, err := st.Feature(ctx, tenant, settings.FeatureOwnSSO)
	return on && err == nil
}

// mayUse says whether the account u may sign in (or link, or be made)
// through the provider, once the provider has said who it is (3.6 d3):
//
//   - a tenant's own provider: only an account of that tenant, and never a
//     site administrator (the tenant's administrators control the provider,
//     and could point it at an identity provider of their own);
//   - a site SSO provider offered to a list: only an account of a tenant on it;
//   - a site social provider: only when the account's tenant allows it
//     (signin.social).
//
// The refusal is not_allowed, audited with the account.
func (s *Service) mayUse(ctx context.Context, rt *providerRT, u store.User) error {
	if rt.row.Owner != store.ProviderSite {
		if u.TenantID != rt.row.Owner || u.IsSiteAdmin() {
			return notAllowed(&u)
		}
		sub, err := s.SubjectOf(ctx, u)
		if err != nil {
			return err
		}
		if !sub.Site.Empty() { // a custom role of the site
			return notAllowed(&u)
		}
		return nil
	}
	// A site administrator always signs in through the site's own SSO,
	// whatever its list of tenants says: the list cannot lock them out.
	if rt.row.Class == ClassSSO && u.IsSiteAdmin() {
		return nil
	}
	// A tenant that requires its single sign-on refuses the site's social
	// sign-in to its people (3.6 d4); a site administrator is never refused
	// by a tenant's setting.
	if rt.row.Class == ClassSocial && s.socialAllowed(ctx, u.TenantID, rt.row.Slug) {
		if !u.IsSiteAdmin() && s.tenantSSOOnly(ctx, u.TenantID) {
			return &ssoError{code: "sso_required", user: &u}
		}
		return nil
	}
	if !s.tenantMayUse(ctx, rt, u.TenantID) {
		return notAllowed(&u)
	}
	return nil
}

// tenantMayUse is what mayUse asks of the account's tenant alone: what an
// account about to be made there (autoCreate, an invitation) is held to.
func (s *Service) tenantMayUse(ctx context.Context, rt *providerRT, tenant string) bool {
	switch {
	case rt.row.Owner != store.ProviderSite:
		return tenant == rt.row.Owner
	case rt.row.Class == ClassSSO:
		return rt.cfg.Offer != OfferList || s.provs.sharedWith(rt.row.ID, tenant)
	case rt.row.Class == ClassSocial:
		return s.socialAllowed(ctx, tenant, rt.row.Slug) && !s.tenantSSOOnly(ctx, tenant)
	}
	return true
}

// socialAllowed says whether the tenant's people may use the site's social
// provider with the slug (signin.social: its slugs, or "*"). A read that
// fails allows nothing.
func (s *Service) socialAllowed(ctx context.Context, tenant, slug string) bool {
	st := s.settings.Load()
	if st == nil {
		return true
	}
	e, err := st.Get(ctx, tenant, settings.SigninSocial)
	if err != nil {
		return false
	}
	l := stringList(e.Value)
	return slices.Contains(l, "*") || slices.Contains(l, slug)
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

// inDomains says whether the address's domain is one of domains, exactly,
// compared in store.EmailKey form. A subdomain is not covered by its parent:
// each is claimed and verified by itself.
func inDomains(email string, domains []string) bool {
	return inDomainsWith(email, domains, configuredDomainKey)
}

// inDomainsWith is inDomains with the normalisation of the configured
// domains given. It fails closed: a configured domain that does not normalise
// matches nothing, and its raw text is never compared instead.
func inDomainsWith(email string, domains []string, norm func(string) (string, bool)) bool {
	if len(domains) == 0 {
		return false
	}
	key, ok := store.EmailKey(email)
	if !ok {
		return false
	}
	d := key[strings.LastIndex(key, "@")+1:]
	for _, a := range domains {
		ak, ok := norm(a)
		if !ok {
			continue
		}
		if d == ak {
			return true
		}
	}
	return false
}

// configuredDomainKey is a configured domain in store.EmailKey form; false
// when it has none (empty, an empty label, not UTF-8, ...).
func configuredDomainKey(domain string) (string, bool) {
	if domain == "" {
		return "", false
	}
	k, ok := store.EmailKey("x@" + domain)
	if !ok || !strings.HasPrefix(k, "x@") {
		return "", false
	}
	return k[2:], true
}
