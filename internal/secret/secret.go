// Package secret encrypts the credentials kept in the state database
// (access tokens, SSH private keys) with AES-256-GCM. The key comes from the
// PETALBOOK_SECRET_KEY environment variable or from DATA/secret.key, which is
// created with a random key (mode 0600) the first time it is needed. Each value
// is bound to the space and the purpose it belongs to, so a value copied to
// another row does not decrypt.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// EnvKey is the environment variable that holds the key instead of a file.
const EnvKey = "PETALBOOK_SECRET_KEY"

// FileName is the key file inside the data directory.
const FileName = "secret.key"

// ErrCannotDecrypt is the answer for a value that was not made with this key
// or for this space and purpose.
var ErrCannotDecrypt = errors.New("the stored secret cannot be decrypted with this server's key")

// Box seals and opens secrets.
type Box struct {
	dir string // where secret.key lives
	env string // the key from the environment, if given

	once sync.Once
	aead cipher.AEAD
	err  error
}

// NewBox returns a Box whose key is looked up on first use: the environment
// variable first, then DATA/secret.key (created if it does not exist). The
// environment value is a 64-digit hex string or base64 of 32 bytes; any other
// non-empty text is stretched with SHA-256, so a passphrase works too.
func NewBox(dataDir string) *Box {
	return &Box{dir: dataDir, env: os.Getenv(EnvKey)}
}

// NewBoxWithKey is a Box with a given 32-byte key (tests).
func NewBoxWithKey(key [32]byte) *Box {
	b := &Box{}
	b.once.Do(func() { b.aead, b.err = newGCM(key[:]) })
	return b
}

func newGCM(key []byte) (cipher.AEAD, error) {
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}

func (b *Box) init() (cipher.AEAD, error) {
	b.once.Do(func() {
		key, err := b.loadKey()
		if err != nil {
			b.err = err
			return
		}
		b.aead, b.err = newGCM(key)
	})
	return b.aead, b.err
}

func (b *Box) loadKey() ([]byte, error) {
	if v := strings.TrimSpace(b.env); v != "" {
		return keyFromText(v), nil
	}
	if b.dir == "" {
		return nil, errors.New("no data directory for the secret key")
	}
	path := filepath.Join(b.dir, FileName)
	data, err := os.ReadFile(path)
	if err == nil {
		k := keyFromText(strings.TrimSpace(string(data)))
		if len(k) != 32 {
			return nil, fmt.Errorf("%s is not a usable key", path)
		}
		return k, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(b.dir, 0o755); err != nil {
		return nil, err
	}
	// Written whole to a private temporary file and then linked into place:
	// two servers starting together must not each keep a different key, and
	// neither may read a half-written one.
	tmp, err := os.CreateTemp(b.dir, ".secret-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return nil, err
	}
	if _, err := tmp.WriteString(hex.EncodeToString(key) + "\n"); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return keyFromText(strings.TrimSpace(string(data))), nil
	}
	return key, nil
}

func keyFromText(v string) []byte {
	if b, err := hex.DecodeString(v); err == nil && len(b) == 32 {
		return b
	}
	if b, err := base64.StdEncoding.DecodeString(v); err == nil && len(b) == 32 {
		return b
	}
	sum := sha256.Sum256([]byte(v))
	return sum[:]
}

func aad(spaceID, purpose string) []byte { return []byte(spaceID + "\x00" + purpose) }

// Seal encrypts plaintext for a space and a purpose ("token", "ssh-key"). The
// result is text: base64 of nonce and ciphertext.
func (b *Box) Seal(spaceID, purpose string, plaintext []byte) (string, error) {
	a, err := b.init()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := a.Seal(nonce, nonce, plaintext, aad(spaceID, purpose))
	return base64.StdEncoding.EncodeToString(out), nil
}

// Open decrypts what Seal made for the same space and purpose.
func (b *Box) Open(spaceID, purpose, sealed string) ([]byte, error) {
	a, err := b.init()
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil || len(raw) < a.NonceSize()+a.Overhead() {
		return nil, ErrCannotDecrypt
	}
	pt, err := a.Open(nil, raw[:a.NonceSize()], raw[a.NonceSize():], aad(spaceID, purpose))
	if err != nil {
		return nil, ErrCannotDecrypt
	}
	return pt, nil
}
