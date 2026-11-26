package auth

import (
	"encoding/json"
	"testing"
)

// Which sign-ins vouch for an address: one case for each rule.
func TestTrustsEmail(t *testing.T) {
	t.Parallel()
	edov := map[string]json.RawMessage{"xms_edov": json.RawMessage(`true`)}
	edovText := map[string]json.RawMessage{"xms_edov": json.RawMessage(`"true"`)}
	for _, c := range []struct {
		name string
		src  emailSource
		want bool
	}{
		{"google verified", emailSource{template: tmplGoogle, email: "a@gmail.test", verified: true}, true},
		{"google unverified", emailSource{template: tmplGoogle, email: "a@gmail.test"}, false},
		{"apple verified", emailSource{template: tmplApple, email: "a@icloud.test", verified: true}, true},
		{"apple unverified", emailSource{template: tmplApple, email: "a@icloud.test"}, false},
		{"github verified", emailSource{template: tmplGitHub, publicGitHub: true, email: "a@github.test", verified: true}, true},
		{"github unverified", emailSource{template: tmplGitHub, publicGitHub: true, email: "a@github.test"}, false},
		{"github enterprise", emailSource{template: tmplGitHub, email: "a@ghe.test", verified: true}, false},
		{"github enterprise, trusted", emailSource{template: tmplGitHub, email: "a@ghe.test", verified: true, trustOptIn: true}, true},
		{"github enterprise, trusted, unverified", emailSource{template: tmplGitHub, email: "a@ghe.test", trustOptIn: true}, false},
		{"microsoft plain claim", emailSource{template: tmplMicrosoft, email: "ceo@victim.test", verified: true}, false},
		{"microsoft xms_edov", emailSource{template: tmplMicrosoft, email: "a@contoso.test", claims: edov}, true},
		{"microsoft xms_edov as text", emailSource{template: tmplMicrosoft, email: "a@contoso.test", claims: edovText}, true},
		{"microsoft verified domain", emailSource{template: tmplMicrosoft, email: "a@Contoso.test", orgDomains: []string{"contoso.test"}}, true},
		{"microsoft other domain", emailSource{template: tmplMicrosoft, email: "a@evil.test", orgDomains: []string{"contoso.test"}}, false},
		{"org on its domain", emailSource{template: tmplOIDC, org: true, email: "a@sub.acme.test", orgDomains: []string{"acme.test"}, verified: true}, true},
		{"org off its domain", emailSource{template: tmplOIDC, org: true, email: "a@acme.test.evil", orgDomains: []string{"acme.test"}, verified: true, trustOptIn: true}, false},
		{"org before domains exist", emailSource{template: tmplSAML, org: true, email: "a@acme.test", verified: true, trustOptIn: true}, false},
		{"custom off", emailSource{template: tmplOIDC, email: "a@idp.test", verified: true}, false},
		{"custom on, verified", emailSource{template: tmplOIDC, email: "a@idp.test", verified: true, trustOptIn: true}, true},
		{"custom on, unverified", emailSource{template: tmplOIDC, email: "a@idp.test", trustOptIn: true}, false},
		{"saml with the switch", emailSource{template: tmplSAML, email: "a@idp.test", verified: true, trustOptIn: true}, false},
		{"no address", emailSource{template: tmplGoogle, verified: true}, false},
	} {
		if got := trustsEmail(c.src); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
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
		"https://api.github.com":     true,
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
