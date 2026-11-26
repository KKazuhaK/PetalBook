package auth

import (
	"encoding/json"
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
	// org: the provider is an organisation's own SSO (OIDC or SAML); its
	// addresses are trusted only on orgDomains, the domains the organisation
	// verified. Until domain verification exists the list is empty, so an
	// organisation's SSO vouches for nothing.
	org        bool
	orgDomains []string
	// trustOptIn is a configured provider's "trust this provider's verified
	// addresses" (its mergeVerifiedEmail switch, off by default).
	trustOptIn bool
	email      string
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
	case tmplGoogle, tmplApple, tmplGitHub:
		return src.verified
	case tmplMicrosoft:
		// Never the plain email claim: any Entra tenant can put any address
		// in it ("nOAuth"). Only xms_edov (the address's domain is verified
		// by the tenant that issued the token), or an address on a domain
		// verified here.
		return claimTrue(src.claims, "xms_edov") || inDomains(src.email, src.orgDomains)
	case tmplOIDC:
		return src.trustOptIn && src.verified
	default:
		// SAML has no verified-address claim: a configured SAML provider is
		// an organisation's SSO and waits for verified domains.
		return false
	}
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
