package forge

import (
	"container/list"
	"crypto/subtle"
	"net/url"
	"strings"
	"sync"
	"time"

	"petalbook.app/server/internal/githubapp"
)

// Bounds of the sign-ins in progress.
const (
	attemptTTL      = 10 * time.Minute
	attemptCapacity = 10_000
)

// attempt is one authorisation between start and the callback.
type attempt struct {
	id, state, verifier, userID, ret string
	expires                          time.Time
	elem                             *list.Element
}

// attemptStore keeps the authorisations in progress: in memory, bounded,
// one-time and short-lived. A restart only makes someone click again.
type attemptStore struct {
	mu    sync.Mutex
	items map[string]*attempt
	order *list.List
	now   func() time.Time
	cap   int
}

func newAttemptStore(now func() time.Time, capacity int) *attemptStore {
	return &attemptStore{items: map[string]*attempt{}, order: list.New(), now: now, cap: capacity}
}

func (p *attemptStore) put(a attempt) string {
	id := githubapp.RandomState()
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	for f := p.order.Front(); f != nil; f = p.order.Front() {
		old := p.items[f.Value.(string)]
		if old != nil && now.Before(old.expires) {
			break
		}
		p.order.Remove(f)
		delete(p.items, f.Value.(string))
	}
	for len(p.items) >= p.cap {
		f := p.order.Front()
		if f == nil {
			break
		}
		p.order.Remove(f)
		delete(p.items, f.Value.(string))
	}
	a.id, a.expires = id, now.Add(attemptTTL)
	a.elem = p.order.PushBack(id)
	p.items[id] = &a
	return id
}

type takeResult int

const (
	takeOK takeResult = iota
	takeUnknown
	takeOther // the state or the user is not the attempt's: it stays for its owner
)

// take removes and returns the attempt of a browser if the state and the user
// are the ones it was made for.
func (p *attemptStore) take(id, state, userID string) (*attempt, takeResult) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a, ok := p.items[id]
	if !ok {
		return nil, takeUnknown
	}
	if !p.now().Before(a.expires) {
		p.order.Remove(a.elem)
		delete(p.items, id)
		return nil, takeUnknown
	}
	if subtle.ConstantTimeCompare([]byte(state), []byte(a.state)) != 1 || userID == "" || a.userID != userID {
		return nil, takeOther
	}
	p.order.Remove(a.elem)
	delete(p.items, id)
	return a, takeOK
}

func (p *attemptStore) len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.items)
}

// safeReturn is an absolute path on this site, never a sign-in page or an API
// path; anything else is "/".
func safeReturn(s string) string {
	if s == "" || len(s) > 2048 || s[0] != '/' || (len(s) > 1 && (s[1] == '/' || s[1] == '\\')) {
		return "/"
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r == '\\' || r == 0x85 || r == 0x2028 || r == 0x2029 {
			return "/"
		}
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") {
		return "/"
	}
	if u.Path == "/login" || u.Path == "/setup" || u.Path == "/invite" || strings.HasPrefix(u.Path, "/api/") {
		return "/"
	}
	return s
}

// sanitize makes something a host or a person sent fit on a log line.
func sanitize(s string) string {
	if len(s) > 64 {
		s = s[:64]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}
