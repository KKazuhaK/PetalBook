// Package domainstest is a DNS of the tests' own for internal/domains.
package domainstest

import (
	"context"
	"strings"
	"sync"

	"petalbook.app/server/internal/domains"
)

// Fake answers from what the test set: TXT values and a CNAME per name, or an
// error. A name it knows nothing of has no record (domains.ErrNoRecord).
type Fake struct {
	mu    sync.Mutex
	txt   map[string][]string
	cname map[string]string
	fail  map[string]error
	// Asked counts the questions, by name.
	asked map[string]int
	// Hook, when set, runs at each question, before it is answered (tests
	// that race two checks).
	Hook func(name string)
}

// New makes an empty DNS.
func New() *Fake {
	return &Fake{txt: map[string][]string{}, cname: map[string]string{}, fail: map[string]error{}, asked: map[string]int{}}
}

func key(name string) string { return strings.ToLower(strings.TrimSuffix(name, ".")) }

// SetTXT sets the TXT values of a name (none removes them).
func (f *Fake) SetTXT(name string, values ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(values) == 0 {
		delete(f.txt, key(name))
		return
	}
	f.txt[key(name)] = values
}

// SetCNAME sets the CNAME target of a name ("" removes it).
func (f *Fake) SetCNAME(name, target string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if target == "" {
		delete(f.cname, key(name))
		return
	}
	f.cname[key(name)] = target
}

// Fail makes every question about a name fail with err (nil: answer again).
func (f *Fake) Fail(name string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		delete(f.fail, key(name))
		return
	}
	f.fail[key(name)] = err
}

// Asked is how many questions were asked about a name.
func (f *Fake) Asked(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.asked[key(name)]
}

func (f *Fake) LookupTXT(_ context.Context, name string) ([]string, error) {
	if f.Hook != nil {
		f.Hook(name)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked[key(name)]++
	if err := f.fail[key(name)]; err != nil {
		return nil, err
	}
	if v, ok := f.txt[key(name)]; ok {
		return append([]string(nil), v...), nil
	}
	return nil, domains.ErrNoRecord
}

func (f *Fake) LookupCNAME(_ context.Context, name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail[key(name)]; err != nil {
		return "", err
	}
	if v, ok := f.cname[key(name)]; ok {
		return v, nil
	}
	return "", domains.ErrNoRecord
}
