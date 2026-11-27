package githubapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// SignatureHeader is the header GitHub signs a webhook delivery's body in:
// "sha256=" and the hex HMAC-SHA256 of the body with the App's webhook secret.
const SignatureHeader = "X-Hub-Signature-256"

// Sign is the value of SignatureHeader for a body.
func Sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// VerifySignature reports whether header is the signature of body with the
// secret, compared in constant time. An empty secret verifies nothing.
func VerifySignature(secret string, body []byte, header string) bool {
	if secret == "" {
		return false
	}
	hexSig, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	got, err := hex.DecodeString(hexSig)
	if err != nil || len(got) != sha256.Size {
		return false
	}
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hmac.Equal(got, m.Sum(nil))
}

// InstallationEvent is the part of an "installation" webhook the server reads:
// what happened (created, deleted, ...), to which installation, and who did it.
type InstallationEvent struct {
	Action       string `json:"action"`
	Installation struct {
		ID      int64   `json:"id"`
		Account Account `json:"account"`
	} `json:"installation"`
	Sender Account `json:"sender"`
}
