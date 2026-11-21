package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// PasswordParams are the argon2id cost parameters. They are written into every
// stored hash, so raising them later affects new hashes (and old ones are
// upgraded at the next sign-in) while old hashes keep verifying.
type PasswordParams struct {
	MemoryKiB uint32
	Time      uint32
	Threads   uint8
}

// DefaultPasswordParams: 64 MiB, 2 passes, one lane, above the OWASP minimum
// for argon2id. Memory is the scarce resource, hence the concurrency limit.
var DefaultPasswordParams = PasswordParams{MemoryKiB: 64 * 1024, Time: 2, Threads: 1}

const (
	saltLen = 16
	keyLen  = 32
	// Limits on what a stored hash may ask for, so a damaged or hostile row
	// cannot make one sign-in allocate gigabytes.
	maxHashMemoryKiB = 1 << 20
	maxHashTime      = 20
)

// MinPasswordChars and MaxPasswordBytes are the policy of docs/api.md.
const (
	MinPasswordChars = 8
	MaxPasswordBytes = 1024
)

// CheckPassword applies the password policy.
func CheckPassword(pw string) error {
	if len(pw) > MaxPasswordBytes {
		return errf(Invalid, "a password is at most %d bytes", MaxPasswordBytes)
	}
	if !utf8.ValidString(pw) || utf8.RuneCountInString(pw) < MinPasswordChars {
		return errf(Invalid, "a password is at least %d characters", MinPasswordChars)
	}
	return nil
}

// hasher computes and checks hashes, a few at a time: each one needs tens of
// megabytes, and a burst of sign-ins must queue rather than exhaust memory.
type hasher struct {
	params PasswordParams
	sem    chan struct{}
	dummy  string // a hash of nothing anyone knows, for users that do not exist

	// Counters for tests: hashes started, running now, and the most at once.
	ops, running, peak atomic.Int64
}

func newHasher(p PasswordParams, concurrent int) (*hasher, error) {
	if p == (PasswordParams{}) {
		p = DefaultPasswordParams
	}
	if p.Threads == 0 || p.Time == 0 || p.MemoryKiB < 8*uint32(p.Threads) {
		return nil, fmt.Errorf("invalid password hashing parameters")
	}
	if concurrent <= 0 {
		concurrent = 4
	}
	h := &hasher{params: p, sem: make(chan struct{}, concurrent)}
	var junk [16]byte
	rand.Read(junk[:])
	d, err := h.hash(context.Background(), base64.RawStdEncoding.EncodeToString(junk[:]))
	if err != nil {
		return nil, err
	}
	h.dummy = d
	return h, nil
}

func (h *hasher) acquire(ctx context.Context) error {
	select {
	case h.sem <- struct{}{}:
		h.ops.Add(1)
		n := h.running.Add(1)
		for p := h.peak.Load(); n > p && !h.peak.CompareAndSwap(p, n); p = h.peak.Load() {
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *hasher) release() {
	h.running.Add(-1)
	<-h.sem
}

// hash returns the encoded argon2id hash of pw.
func (h *hasher) hash(ctx context.Context, pw string) (string, error) {
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	p := h.params
	key := argon2.IDKey([]byte(pw), salt, p.Time, p.MemoryKiB, p.Threads, keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.MemoryKiB, p.Time, p.Threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// verify checks pw against an encoded hash in constant time. rehash reports a
// correct password whose hash was made with other parameters than today's.
func (h *hasher) verify(ctx context.Context, encoded, pw string) (ok, rehash bool, err error) {
	p, salt, want, perr := parseHash(encoded)
	if perr != nil {
		// A row we cannot read still costs a full hash, like any other miss.
		_, err := h.hash(ctx, pw)
		return false, false, err
	}
	if err := h.acquire(ctx); err != nil {
		return false, false, err
	}
	defer h.release()
	got := argon2.IDKey([]byte(pw), salt, p.Time, p.MemoryKiB, p.Threads, uint32(len(want)))
	ok = subtle.ConstantTimeCompare(got, want) == 1
	return ok, ok && p != h.params, nil
}

func parseHash(s string) (PasswordParams, []byte, []byte, error) {
	var p PasswordParams
	parts := strings.Split(s, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return p, nil, nil, fmt.Errorf("not an argon2id hash")
	}
	var m, t uint32
	var th uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &th); err != nil {
		return p, nil, nil, err
	}
	if m == 0 || m > maxHashMemoryKiB || t == 0 || t > maxHashTime || th == 0 {
		return p, nil, nil, fmt.Errorf("hash parameters out of range")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return p, nil, nil, fmt.Errorf("bad salt")
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) < 16 {
		return p, nil, nil, fmt.Errorf("bad key")
	}
	return PasswordParams{MemoryKiB: m, Time: t, Threads: th}, salt, key, nil
}
