package githubapp_test

import (
	"strings"
	"testing"

	"petalbook.app/server/internal/githubapp"
)

// A delivery is believed only with the HMAC-SHA256 of its exact body under the
// secret, as "sha256=<hex>"; no secret believes nothing.
func TestWebhookSignatures(t *testing.T) {
	t.Parallel()
	body := []byte(`{"action":"created"}`)
	sig := githubapp.Sign("s3cret", body)
	if !strings.HasPrefix(sig, "sha256=") || !githubapp.VerifySignature("s3cret", body, sig) {
		t.Fatalf("%q", sig)
	}
	for name, c := range map[string]struct {
		secret, header string
		body           []byte
	}{
		"other secret": {"other", sig, body},
		"other body":   {"s3cret", sig, []byte(`{"action":"deleted"}`)},
		"no prefix":    {"s3cret", strings.TrimPrefix(sig, "sha256="), body},
		"sha1":         {"s3cret", "sha1=" + strings.TrimPrefix(sig, "sha256="), body},
		"short":        {"s3cret", sig[:20], body},
		"not hex":      {"s3cret", "sha256=" + strings.Repeat("z", 64), body},
		"empty":        {"s3cret", "", body},
		"no secret":    {"", githubapp.Sign("", body), body},
	} {
		if githubapp.VerifySignature(c.secret, c.body, c.header) {
			t.Errorf("%s: believed", name)
		}
	}
}
