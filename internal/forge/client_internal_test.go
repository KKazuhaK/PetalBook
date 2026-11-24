package forge

import (
	"strings"
	"testing"
)

func TestWhatAHostSaysCannotForgeALogLineOrCarryAToken(t *testing.T) {
	// Put together at run time, so the secret scanner does not take this
	// made-up token for a real one.
	jwt := strings.Join([]string{"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9", "eyJzdWIiOiJ0YW5nIiwiZXhwIjoxNzY0MDAwMDAwfQ", "c2lnbmF0dXJlLXNpZ25hdHVyZS14eA"}, ".")
	evil := "bad\nlevel=ERROR msg=forged\r\x1b[31m next \u0085 " + jwt + " glpat-abcdef123456 -----BEGIN PRIVATE KEY-----"
	for name, err := range map[string]error{
		"status":     &StatusError{Method: "GET", Kind: "user", Status: 400, Message: evil},
		"oauth":      &OAuthError{Code: "invalid_request", Description: evil},
		"oauth-code": &OAuthError{Code: evil},
	} {
		got := err.Error()
		for _, bad := range []string{"\n", "\r", "\x1b", " ", "\u0085", jwt, "glpat-abcdef123456", "BEGIN"} {
			if strings.Contains(got, bad) {
				t.Errorf("%s: %q is in %q", name, bad, got)
			}
		}
		if len(got) > 400 {
			t.Errorf("%s: %d bytes long", name, len(got))
		}
	}
	// What is not a token is left as it is: a host name has three parts too.
	if got := quote("notes.example.com is down"); got != "notes.example.com is down" {
		t.Fatalf("%q", got)
	}
	if got := quote("gitlab.internal.example.org"); got != "gitlab.internal.example.org" {
		t.Fatalf("%q", got)
	}
}
