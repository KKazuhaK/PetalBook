// Package domains is the email domains tenants claim and verify by DNS
// (milestone 3.6 d2, docs/api.md "Email domains"). A tenant claims a domain,
// publishes a record that carries the claim's code, and asks for a check; the
// first tenant whose record is found holds the domain. Verified domains are
// rechecked once a day, and a claim whose record has been missing for seven
// days lapses. Which tenant holds which domain is kept in memory, so that
// sign-in and registration ask it without a query.
package domains

import (
	"strings"

	"golang.org/x/net/idna"
)

// profile is UTS #46 for lookup, as browsers and mail servers map a typed
// name, with the rules of host names (letters, digits and hyphens).
var profile = idna.New(idna.MapForLookup(), idna.BidiRule(), idna.StrictDomainName(true), idna.Transitional(false), idna.VerifyDNSLength(true))

// Key is the form a domain is stored and compared in: UTS #46 mapped,
// punycode, lower case, without the root's dot. ok is false for what is not a
// domain one may claim: one label only, an address in place of a name, a
// label that is not a host name's, longer than DNS allows.
func Key(in string) (key string, ok bool) {
	in = strings.TrimSuffix(strings.TrimSpace(in), ".")
	if in == "" || strings.ContainsAny(in, "@/:\\ ") {
		return "", false
	}
	a, err := profile.ToASCII(in)
	if err != nil {
		return "", false
	}
	a = strings.ToLower(a)
	// A punycode label that does not decode, or decodes to another spelling,
	// is not a second name of the same domain.
	if u, err := profile.ToUnicode(a); err != nil {
		return "", false
	} else if back, err := profile.ToASCII(u); err != nil || strings.ToLower(back) != a {
		return "", false
	}
	labels := strings.Split(a, ".")
	if len(labels) < 2 {
		return "", false
	}
	tld := labels[len(labels)-1]
	if strings.Trim(tld, "0123456789") == "" {
		return "", false // an IPv4 address, or a numeric top level
	}
	for _, l := range labels {
		if l == "" || l[0] == '-' || l[len(l)-1] == '-' {
			return "", false
		}
	}
	return a, true
}

// Display is a domain's key as people read it (Unicode); the key itself when
// it does not decode.
func Display(key string) string {
	u, err := profile.ToUnicode(key)
	if err != nil {
		return key
	}
	return u
}

// EmailDomain is the key of an address's domain; "" when it has none.
func EmailDomain(email string) string {
	at := strings.LastIndexByte(email, '@')
	if at < 0 {
		return ""
	}
	k, _ := Key(email[at+1:])
	return k
}
