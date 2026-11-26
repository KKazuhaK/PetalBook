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
		{"github verified", emailSource{template: tmplGitHub, email: "a@github.test", verified: true}, true},
		{"github unverified", emailSource{template: tmplGitHub, email: "a@github.test"}, false},
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
