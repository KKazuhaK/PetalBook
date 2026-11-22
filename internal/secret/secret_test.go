package secret

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSealOpenAndBinding(t *testing.T) {
	t.Setenv(EnvKey, "")
	dir := t.TempDir()
	b := NewBox(dir)
	sealed, err := b.Seal("SPACE00001", "token", []byte("ghp_secret"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "ghp_secret") {
		t.Fatal("plaintext in the sealed text")
	}
	if got, err := b.Open("SPACE00001", "token", sealed); err != nil || string(got) != "ghp_secret" {
		t.Fatalf("%q %v", got, err)
	}
	// Bound to the space and to the purpose.
	if _, err := b.Open("SPACE00002", "token", sealed); !errors.Is(err, ErrCannotDecrypt) {
		t.Fatalf("another space: %v", err)
	}
	if _, err := b.Open("SPACE00001", "ssh-key", sealed); !errors.Is(err, ErrCannotDecrypt) {
		t.Fatalf("another purpose: %v", err)
	}
	// Sealing twice gives different texts (fresh nonce).
	again, _ := b.Seal("SPACE00001", "token", []byte("ghp_secret"))
	if again == sealed {
		t.Fatal("same nonce twice")
	}
	// Tampering, truncation and junk fail cleanly.
	raw := []byte(sealed)
	raw[len(raw)/2] ^= 1
	for _, bad := range []string{string(raw), sealed[:10], "", "!!!not base64!!!"} {
		if _, err := b.Open("SPACE00001", "token", bad); !errors.Is(err, ErrCannotDecrypt) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestKeyFileIsCreatedPrivateAndReused(t *testing.T) {
	t.Setenv(EnvKey, "")
	dir := t.TempDir()
	b := NewBox(dir)
	sealed, err := b.Seal("S", "token", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, FileName))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", fi, err)
	}
	// A new Box (a restart) reads the same key.
	if got, err := NewBox(dir).Open("S", "token", sealed); err != nil || string(got) != "x" {
		t.Fatalf("%q %v", got, err)
	}
	// Another data directory has another key.
	if _, err := NewBox(t.TempDir()).Open("S", "token", sealed); !errors.Is(err, ErrCannotDecrypt) {
		t.Fatalf("%v", err)
	}
}

func TestKeyFromTheEnvironmentWinsAndNoFileIsWritten(t *testing.T) {
	dir := t.TempDir()
	for _, v := range []string{strings.Repeat("ab", 32), "a passphrase of some length"} {
		t.Setenv(EnvKey, v)
		b := NewBox(dir)
		sealed, err := b.Seal("S", "ssh-key", []byte("k"))
		if err != nil {
			t.Fatal(err)
		}
		if got, err := NewBox(dir).Open("S", "ssh-key", sealed); err != nil || !bytes.Equal(got, []byte("k")) {
			t.Fatalf("%q %v", got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); err == nil {
		t.Fatal("a key file was written although the environment gave the key")
	}
}

func TestConcurrentFirstUseAgreesOnOneKey(t *testing.T) {
	t.Setenv(EnvKey, "")
	dir := t.TempDir()
	var wg sync.WaitGroup
	sealed := make([]string, 8)
	for i := range sealed {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := NewBox(dir).Seal("S", "token", []byte("same"))
			if err != nil {
				t.Error(err)
			}
			sealed[i] = s
		}()
	}
	wg.Wait()
	b := NewBox(dir)
	for _, s := range sealed {
		if got, err := b.Open("S", "token", s); err != nil || string(got) != "same" {
			t.Fatalf("%q %v", got, err)
		}
	}
}
