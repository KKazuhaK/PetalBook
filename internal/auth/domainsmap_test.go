package auth

import (
	"testing"

	"petalbook.app/server/internal/domains"
)

// The verified domains reach an organisation's SSO as the domains map keeps
// them (internal/domains: punycode, lower case), and it vouches for an address
// of exactly such a domain, in any spelling, never for one of a subdomain.
func TestTheDomainsMapsKeysAreTrustedExactly(t *testing.T) {
	t.Parallel()
	key, ok := domains.Key("Bücher.Example")
	if !ok {
		t.Fatal("no key")
	}
	for email, want := range map[string]bool{
		"a@bücher.example":              true,
		"a@BÜCHER.example":              true,
		"a@xn--bcher-kva.example":       true,
		"a@sub.bücher.example":          false,
		"a@sub.xn--bcher-kva.example":   false,
		"a@bucher.example":              false,
		"a@xn--bcher-kva.example.other": false,
	} {
		src := emailSource{template: tmplOIDC, org: true, email: email, orgDomains: []string{key}, verified: true}
		if got := trustsEmail(src); got != want {
			t.Errorf("%s on %s: %v, want %v", email, key, got, want)
		}
	}
}
