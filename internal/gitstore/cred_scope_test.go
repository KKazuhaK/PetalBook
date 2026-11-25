package gitstore

import (
	"strings"
	"testing"
)

// The token is configured for the remote's own scheme and host only.
func TestTokenIsScopedToTheRemoteHost(t *testing.T) {
	t.Parallel()
	env, _, cleanup, err := Token{Username: "u", URL: "https://Git.Example.com/a/b.git", Get: func() (string, error) { return "s3cret", nil }}.prepare()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "GIT_CONFIG_KEY_1=credential.https://Git.Example.com.helper") {
		t.Fatalf("the helper is not scoped to the host:\n%s", strings.ReplaceAll(joined, "s3cret", "***"))
	}
	if strings.Contains(joined, "GIT_CONFIG_KEY_1=credential.helper") {
		t.Fatal("the helper answers for every host")
	}
	for _, raw := range []string{"", "git@example.com:a/b.git", "ssh://example.com/a", "file:///x"} {
		if s := credentialScope(raw); s != "" {
			t.Fatalf("credentialScope(%q) = %q", raw, s)
		}
	}
}
