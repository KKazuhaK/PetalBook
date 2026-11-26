package mail

import (
	"strings"
	"testing"
)

// A link that proves an address says which account it is for; the claim of an
// address another account holds unverified says so too, and that confirming
// moves it.
func TestVerifyMessagesNameTheAccount(t *testing.T) {
	t.Parallel()
	v := Vars{Site: "pb.example.test", Name: "Tang Mei", Username: "tang", Email: "work@example.test", Link: "https://pb.example.test/verify-email#x", Hours: 24}
	for _, lang := range Langs {
		verify := Render(KindVerify, lang, v)
		claim := Render(KindEmailClaim, lang, v)
		if verify.Subject == claim.Subject {
			t.Errorf("%s: the claim has the subject of an ordinary verification", lang)
		}
		for name, m := range map[string]Message{"verify": verify, "claim": claim} {
			for _, want := range []string{"Tang Mei", "tang", "work@example.test", v.Link} {
				if !strings.Contains(m.Text, want) || !strings.Contains(m.HTML, want) {
					t.Errorf("%s %s: no %q in %q", lang, name, want, m.Text)
				}
			}
		}
	}
	if !strings.Contains(Render(KindEmailClaim, "en", v).Text, "moved away from") {
		t.Error("the English claim does not say that confirming moves the address")
	}
	if !strings.Contains(Render(KindEmailClaim, "zh-Hans", v).Text, "移走") {
		t.Error("the Chinese claim does not say that confirming moves the address")
	}
	// Without an account (a registration's own address) nothing is made up.
	if m := Render(KindVerify, "en", Vars{Site: "s", Email: "a@b.test", Hours: 1}); strings.Contains(m.Text, "()") {
		t.Errorf("%q", m.Text)
	}
}
