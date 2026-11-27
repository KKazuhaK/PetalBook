package domains

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"time"
)

// The record: TXT _petalbook.<domain> = "petalbook-verify=<code>", or a
// CNAME _petalbook.<domain> to <code>.verify.<the site's host>. The CNAME's
// target is only compared, never resolved.
const (
	RecordPrefix = "_petalbook."
	TXTPrefix    = "petalbook-verify="
	CNAMELabel   = ".verify."
)

// Resolver asks DNS. ErrNoRecord (wrapped or not) is the answer that there is
// no such record or no such name; any other error is a failure to ask, which is
// never taken for "missing". Tests give a fake.
type Resolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
	// LookupCNAME is the name's CNAME target; ErrNoRecord when it has none.
	LookupCNAME(ctx context.Context, name string) (string, error)
}

// ErrNoRecord: DNS answered that the name or the record does not exist.
var ErrNoRecord = errors.New("no such record")

// NetResolver is the system's resolver.
type NetResolver struct{ R *net.Resolver }

func (n NetResolver) res() *net.Resolver {
	if n.R != nil {
		return n.R
	}
	return net.DefaultResolver
}

func notFound(err error) error {
	var de *net.DNSError
	if errors.As(err, &de) && de.IsNotFound {
		return ErrNoRecord
	}
	return err
}

func (n NetResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	v, err := n.res().LookupTXT(ctx, name)
	return v, notFound(err)
}

func (n NetResolver) LookupCNAME(ctx context.Context, name string) (string, error) {
	v, err := n.res().LookupCNAME(ctx, name)
	if err != nil {
		return "", notFound(err)
	}
	// The resolver answers the name itself when it has no CNAME.
	if strings.EqualFold(strings.TrimSuffix(v, "."), strings.TrimSuffix(name, ".")) {
		return "", ErrNoRecord
	}
	return v, nil
}

// The results of a check.
const (
	Found   = "found"
	Missing = "missing"
	Wrong   = "wrong_value"
	Failed  = "dns_error"
)

// Who asked for a check: a person ("Check now") or the daily recheck.
const (
	ByManual = "manual"
	ByDaily  = "daily"
)

// maxSeen and maxSeenBytes bound what a wrong value's check keeps of what it saw.
const (
	maxSeen      = 5
	maxSeenBytes = 255
)

// Check is what one check saw.
type Check struct {
	At     time.Time `json:"at"`
	By     string    `json:"by"`
	Result string    `json:"result"`
	// Via is the kind of record that was found (txt or cname).
	Via string `json:"via,omitempty"`
	// Seen are the values of the claim's kind that were there but not its code
	// (a wrong value): TXT values that start with petalbook-verify=, a CNAME target.
	Seen []string `json:"seen,omitempty"`
	// Error is why DNS could not be asked (result error).
	Error string `json:"error,omitempty"`
}

// JSON is the check as stored.
func (c Check) JSON() string {
	b, _ := json.Marshal(c)
	return string(b)
}

// ParseCheck reads a stored check; nil for none.
func ParseCheck(s string) *Check {
	if s == "" {
		return nil
	}
	var c Check
	if json.Unmarshal([]byte(s), &c) != nil {
		return nil
	}
	return &c
}

// NewCode makes a verification code: 26 letters and digits of base32 (128
// bits), in lower case, so that it is a DNS label too.
func NewCode() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))
}

// HashCode is what is stored of a code.
func HashCode(code string) string {
	h := sha256.Sum256([]byte(strings.ToLower(code)))
	return hex.EncodeToString(h[:])
}

// RecordName is the name the record is published at.
func RecordName(domain string) string { return RecordPrefix + domain }

// TXTValue and CNAMETarget are the record's value for a code.
func TXTValue(code string) string { return TXTPrefix + code }

func CNAMETarget(code, siteHost string) string { return code + CNAMELabel + siteHost }

// see keeps a value a wrong check saw: at most 5, each cut to 255 bytes.
func (c *Check) see(v string) {
	if len(c.Seen) >= maxSeen {
		return
	}
	if len(v) > maxSeenBytes {
		v = strings.ToValidUTF8(v[:maxSeenBytes], "")
	}
	c.Seen = append(c.Seen, v)
}

// lookup asks DNS for the record of domain and compares what it finds with the
// claim's code. A TXT that carries the code, or a CNAME to its target, is
// found; any other petalbook-verify value or CNAME target is wrong; neither is
// missing, unless DNS could not be asked, which is an error, never missing.
func lookup(ctx context.Context, r Resolver, domain, codeHash, siteHost string, at time.Time) Check {
	name := RecordName(domain)
	c := Check{At: at}
	txts, terr := r.LookupTXT(ctx, name)
	if terr == nil {
		for _, v := range txts {
			code, ok := strings.CutPrefix(strings.TrimSpace(v), TXTPrefix)
			if !ok {
				continue
			}
			if HashCode(code) == codeHash {
				c.Result, c.Via, c.Seen = Found, "txt", nil
				return c
			}
			c.see(v)
		}
	}
	target, cerr := r.LookupCNAME(ctx, name)
	if cerr == nil {
		t := strings.ToLower(strings.TrimSuffix(target, "."))
		if code, ok := strings.CutSuffix(t, CNAMELabel+siteHost); ok && siteHost != "" && code != "" && !strings.Contains(code, ".") && HashCode(code) == codeHash {
			c.Result, c.Via, c.Seen = Found, "cname", nil
			return c
		}
		c.see(t)
	}
	if len(c.Seen) > 0 {
		c.Result = Wrong
		return c
	}
	for _, err := range []error{terr, cerr} {
		if err != nil && !errors.Is(err, ErrNoRecord) {
			c.Result, c.Error = Failed, err.Error()
			return c
		}
	}
	c.Result = Missing
	return c
}
