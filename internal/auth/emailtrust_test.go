package auth

import (
	"encoding/json"
	"testing"

	"petalbook.app/server/internal/store"
)

// Which sign-ins vouch for an address: every provider, and each way it may
// not. match is whether the address may then meet another account's
// (email_in_use, or taking it from an unverified holder): an Apple relay
// address never does.
func TestTrustsEmail(t *testing.T) {
	t.Parallel()
	edov := map[string]json.RawMessage{"xms_edov": json.RawMessage(`true`)}
	edovText := map[string]json.RawMessage{"xms_edov": json.RawMessage(`"true"`)}
	edovFalse := map[string]json.RawMessage{"xms_edov": json.RawMessage(`false`)}
	plainMS := map[string]json.RawMessage{"email": json.RawMessage(`"ceo@victim.test"`), "email_verified": json.RawMessage(`true`)}
	const relay = "x7q2m9@privaterelay.appleid.com"
	for _, c := range []struct {
		name         string
		src          emailSource
		trust, match bool
	}{
		// Google: email_verified.
		{"google verified", emailSource{template: tmplGoogle, email: "a@gmail.test", verified: true}, true, true},
		{"google unverified", emailSource{template: tmplGoogle, email: "a@gmail.test"}, false, true},
		{"google, the opt-in does not stand for the claim", emailSource{template: tmplGoogle, email: "a@gmail.test", trustOptIn: true}, false, true},
		{"google, no address", emailSource{template: tmplGoogle, verified: true}, false, true},

		// Apple: email_verified; a relay address is verified but meets nobody.
		{"apple verified", emailSource{template: tmplApple, email: "a@icloud.test", verified: true}, true, true},
		{"apple unverified", emailSource{template: tmplApple, email: "a@icloud.test"}, false, true},
		{"apple relay", emailSource{template: tmplApple, email: relay, verified: true, relay: true}, true, false},
		{"apple relay, unverified", emailSource{template: tmplApple, email: relay, relay: true}, false, false},

		// Microsoft: only xms_edov. Never the plain claim, not even on a
		// domain verified here ("nOAuth").
		{"microsoft plain email, no xms_edov", emailSource{template: tmplMicrosoft, email: "ceo@victim.test", verified: true, claims: plainMS}, false, true},
		{"microsoft plain email on a domain verified here", emailSource{template: tmplMicrosoft, email: "a@contoso.test", verified: true, orgDomains: []string{"contoso.test"}}, false, true},
		{"microsoft plain email, the opt-in on", emailSource{template: tmplMicrosoft, email: "a@contoso.test", verified: true, trustOptIn: true}, false, true},
		{"microsoft xms_edov false", emailSource{template: tmplMicrosoft, email: "a@contoso.test", verified: true, claims: edovFalse}, false, true},
		{"microsoft xms_edov", emailSource{template: tmplMicrosoft, email: "a@contoso.test", claims: edov}, true, true},
		{"microsoft xms_edov as text", emailSource{template: tmplMicrosoft, email: "a@contoso.test", claims: edovText}, true, true},
		{"microsoft xms_edov, no address", emailSource{template: tmplMicrosoft, claims: edov}, false, true},

		// GitHub: a verified address of github.com; GitHub Enterprise only
		// with the opt-in.
		{"github verified", emailSource{template: tmplGitHub, publicGitHub: true, email: "a@github.test", verified: true}, true, true},
		{"github unverified", emailSource{template: tmplGitHub, publicGitHub: true, email: "a@github.test"}, false, true},
		{"github enterprise without the opt-in", emailSource{template: tmplGitHub, email: "a@ghe.test", verified: true}, false, true},
		{"github enterprise with the opt-in", emailSource{template: tmplGitHub, email: "a@ghe.test", verified: true, trustOptIn: true}, true, true},
		{"github enterprise with the opt-in, unverified", emailSource{template: tmplGitHub, email: "a@ghe.test", trustOptIn: true}, false, true},

		// An organisation's own SSO (OIDC or SAML): only its verified domains,
		// each exactly; nothing before it has one.
		{"org oidc on its domain", emailSource{template: tmplOIDC, org: true, email: "a@Acme.test", orgDomains: []string{"acme.test"}, verified: true}, true, true},
		{"org oidc, a subdomain address", emailSource{template: tmplOIDC, org: true, email: "a@sub.acme.test", orgDomains: []string{"acme.test"}, verified: true}, false, true},
		{"org oidc, the subdomain claimed", emailSource{template: tmplOIDC, org: true, email: "a@sub.acme.test", orgDomains: []string{"acme.test", "sub.acme.test"}}, true, true},
		{"org oidc, the parent of its domain", emailSource{template: tmplOIDC, org: true, email: "a@acme.test", orgDomains: []string{"sub.acme.test"}, verified: true}, false, true},
		{"org oidc off its domain", emailSource{template: tmplOIDC, org: true, email: "a@acme.test.evil", orgDomains: []string{"acme.test"}, verified: true, trustOptIn: true}, false, true},
		{"org oidc before domains exist", emailSource{template: tmplOIDC, org: true, email: "a@acme.test", verified: true, trustOptIn: true}, false, true},
		{"org saml before domains exist", emailSource{template: tmplSAML, org: true, email: "a@acme.test", verified: true, trustOptIn: true}, false, true},
		{"org saml on its domain", emailSource{template: tmplSAML, org: true, email: "a@acme.test", orgDomains: []string{"acme.test"}}, true, true},
		{"org saml, a subdomain address", emailSource{template: tmplSAML, org: true, email: "a@eu.acme.test", orgDomains: []string{"acme.test"}}, false, true},
		{"org saml, the opt-in is not an org's", emailSource{template: tmplSAML, org: true, email: "a@evil.test", orgDomains: []string{"acme.test"}, trustOptIn: true}, false, true},

		// A provider the site's administrators configured (GitLab.com's
		// template too): the opt-in and email_verified.
		{"custom oidc without the opt-in", emailSource{template: tmplOIDC, email: "a@idp.test", verified: true}, false, true},
		{"custom oidc with the opt-in, verified", emailSource{template: tmplOIDC, email: "a@idp.test", verified: true, trustOptIn: true}, true, true},
		{"custom oidc with the opt-in, unverified", emailSource{template: tmplOIDC, email: "a@idp.test", trustOptIn: true}, false, true},
		{"oauth2 without the opt-in, verified", emailSource{template: tmplOAuth2, email: "a@idp.test", verified: true}, false, true},
		{"oauth2 with the opt-in, unverified", emailSource{template: tmplOAuth2, email: "a@idp.test", trustOptIn: true}, false, true},
		{"oauth2 with the opt-in, verified", emailSource{template: tmplOAuth2, email: "a@idp.test", verified: true, trustOptIn: true}, true, true},
		{"site saml with the opt-in", emailSource{template: tmplSAML, email: "a@idp.test", trustOptIn: true}, true, true},
		{"site saml without the opt-in", emailSource{template: tmplSAML, email: "a@idp.test", verified: true}, false, true},
		{"an unknown rule", emailSource{template: "", email: "a@idp.test", verified: true, trustOptIn: true}, false, true},
	} {
		if got := trustsEmail(c.src); got != c.trust {
			t.Errorf("%s: trusted %v, want %v", c.name, got, c.trust)
		}
		if got := matchesByAddress(c.src); got != c.match {
			t.Errorf("%s: matches by address %v, want %v", c.name, got, c.match)
		}
	}
}

// A provider follows its template's rule; GitLab.com's and any configured
// one's are a configured provider's. Apple's relays are found by domain.
func TestTrustTemplate(t *testing.T) {
	t.Parallel()
	for tmpl, want := range map[string]string{
		TemplateGoogle: tmplGoogle, TemplateApple: tmplApple, TemplateMicrosoft: tmplMicrosoft,
		TemplateGitLabCom: tmplOIDC, "": tmplOIDC, "google-ish": tmplOIDC,
	} {
		if got := trustTemplate(store.ProviderRow{Kind: KindOIDC, Template: tmpl}); got != want {
			t.Errorf("%q: %q, want %q", tmpl, got, want)
		}
	}
	if got := trustTemplate(store.ProviderRow{Kind: KindOAuth2}); got != tmplOAuth2 {
		t.Errorf("oauth2: %q", got)
	}
	for addr, want := range map[string]bool{
		"x@privaterelay.appleid.com": true, "x@PrivateRelay.AppleID.com": true,
		"x@sub.privaterelay.appleid.com": false, "x@privaterelay.appleid.com.evil": false, "x@icloud.com": false, "": false,
	} {
		if got := isAppleRelay(addr); got != want {
			t.Errorf("relay %q: %v, want %v", addr, got, want)
		}
	}
}

// A domain covers itself only: a subdomain is claimed by itself.
func TestInDomainsIsExact(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		email   string
		domains []string
		want    bool
	}{
		{"a@acme.test", []string{"acme.test"}, true},
		{"a@ACME.test", []string{"Acme.Test"}, true},
		{"a@sub.acme.test", []string{"acme.test"}, false},
		{"a@deep.sub.acme.test", []string{"sub.acme.test"}, false},
		{"a@xacme.test", []string{"acme.test"}, false},
		{"a@acme.test", []string{"", "other.test"}, false},
		{"a@acme.test", nil, false},
		{"not an address", []string{"acme.test"}, false},
	} {
		if got := inDomains(c.email, c.domains); got != c.want {
			t.Errorf("%s in %v: %v, want %v", c.email, c.domains, got, c.want)
		}
	}
}

// A configured domain that cannot be normalised matches nothing (fail closed):
// its raw text is never compared instead.
func TestInDomainsFailsClosedOnADomainThatDoesNotNormalise(t *testing.T) {
	t.Parallel()
	refuse := func(d string) (string, bool) {
		if d == "acme.test" {
			return "", false
		}
		return configuredDomainKey(d)
	}
	if inDomainsWith("a@acme.test", []string{"acme.test"}, refuse) {
		t.Fatal("a domain that did not normalise matched by its raw text")
	}
	if !inDomainsWith("a@other.test", []string{"acme.test", "Other.Test"}, refuse) {
		t.Fatal("the next domain was not looked at")
	}
	for _, bad := range []string{"acme..test", "", "acme.test\xff", ".acme.test", "xn--.test"} {
		if _, ok := configuredDomainKey(bad); ok {
			t.Errorf("%q normalised", bad)
		}
		if inDomains("a@acme.test", []string{bad}) {
			t.Errorf("%q matched", bad)
		}
	}
}

// Only github.com is GitHub: an Enterprise server's verified flag is its
// administrators' word.
func TestIsPublicGitHub(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]bool{
		"https://github.com":         true,
		"https://GitHub.com/":        true,
		"https://api.github.com":     false, // not a base URL: githubAPIBase would make it .../api/v3
		"https://ghe.example.test":   false,
		"https://github.com.evil":    false,
		"https://evilgithub.com":     false,
		"https://github.example.com": false,
		"":                           false,
	} {
		if got := isPublicGitHub(in); got != want {
			t.Errorf("%q: %v, want %v", in, got, want)
		}
	}
}

// An address is one the form accepts only with a domain the key keeps as
// typed: no root's dot at the end, no empty label.
func TestCleanEmailRefusesEmptyLabels(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"bob@example.com.", "a@xn--.com", "a@example..com", "a@.com"} {
		if _, err := cleanEmail(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if e, err := cleanEmail(" bob@example.com "); err != nil || e != "bob@example.com" {
		t.Errorf("%q %v", e, err)
	}
}
