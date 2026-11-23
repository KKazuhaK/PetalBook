// Package tokens is PetalBook's API tokens (docs/api.md, "API tokens"): a
// secret that lets a script act for one person in a few of their spaces. Only
// the SHA-256 of a secret is stored, with its first characters to tell tokens
// apart. A lookup is answered from memory: the first sight of a token costs one
// query on a unique index, and a token in memory costs none.
package tokens

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"strings"
)

// Prefix starts every token.
const Prefix = "pbk_"

// secretLen is the number of base62 characters of the secret: 62^43 > 2^256.
const secretLen = 43

// Access levels of a token.
const (
	Read  = "read"
	Write = "write"
)

const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// Generate makes a new secret: "pbk_" and 32 random bytes in base62.
func Generate() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	n := new(big.Int).SetBytes(buf[:])
	base := big.NewInt(62)
	out := make([]byte, secretLen)
	for i := secretLen - 1; i >= 0; i-- {
		var m big.Int
		n.DivMod(n, base, &m)
		out[i] = alphabet[m.Int64()]
	}
	return Prefix + string(out), nil
}

// WellFormed reports whether s has the shape of a token this server issues.
// Anything else is refused without a lookup.
func WellFormed(s string) bool {
	if len(s) != len(Prefix)+secretLen || !strings.HasPrefix(s, Prefix) {
		return false
	}
	for i := len(Prefix); i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') {
			return false
		}
	}
	return true
}

// ShownPrefix is the part of a secret that stays visible: "pbk_" and the next 8
// characters.
func ShownPrefix(secret string) string {
	if len(secret) > len(Prefix)+8 {
		return secret[:len(Prefix)+8]
	}
	return secret
}

// HashSecret is the SHA-256 (hex) stored for a secret.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
