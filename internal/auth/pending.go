package auth

import (
	"container/list"
	"crypto/subtle"
	"net/url"
	"strings"
	"sync"
	"time"

	authoidc "github.com/KazuhaHub/authcore/oidc"
)

// Bounds of the store of sign-ins in progress.
const (
	pendingTTL      = 10 * time.Minute
	pendingCapacity = 10_000
)

// pendingEntry is one sign-in between /api/auth/oidc/start and the callback.
type pendingEntry struct {
	kind string
	slug string
	// state is the protocol state of an OpenID Connect attempt.
	state authoidc.State
	// stateValue is what the callback must echo: the OpenID Connect state, the
	// GitHub state, or the id itself for SAML (RelayState equals the cookie).
	stateValue string
	// verifier is the PKCE verifier of a GitHub attempt.
	verifier string
	// samlID is the ID of the AuthnRequest a SAML response must answer.
	samlID string
	intent string
	token  string // setup or invite token, held in memory only
	userID string // link: the user who asked
	// linkBinding is the hash of the host-only cookie a SAML link set in the
	// browser that started it (see samlLinkCookie).
	linkBinding string
	ret         string
	expires     time.Time
	elem        *list.Element
	id          string
}

// pendingStore keeps the protocol state of sign-ins in progress: in memory,
// bounded, one-time and short-lived. A restart only makes someone in the
// middle of signing in click again.
type pendingStore struct {
	mu    sync.Mutex
	items map[string]*pendingEntry
	order *list.List
	now   func() time.Time
}

func newPendingStore(now func() time.Time) *pendingStore {
	return &pendingStore{items: map[string]*pendingEntry{}, order: list.New(), now: now}
}

// put stores an entry and returns the random id for the browser-binding cookie.
func (p *pendingStore) put(e pendingEntry) string {
	id := randomToken()
	p.putAs(id, e)
	return id
}

// putAs stores an entry under an id the caller made (SAML: the id is also the
// RelayState, which must equal the cookie).
func (p *pendingStore) putAs(id string, e pendingEntry) {
	if e.stateValue == "" {
		e.stateValue = id
	}
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	// Entries are in insertion order and share one lifetime, so the expired
	// ones are at the front.
	for f := p.order.Front(); f != nil; f = p.order.Front() {
		old := p.items[f.Value.(string)]
		if old != nil && now.Before(old.expires) {
			break
		}
		p.order.Remove(f)
		delete(p.items, f.Value.(string))
	}
	for len(p.items) >= pendingCapacity {
		f := p.order.Front()
		if f == nil {
			break
		}
		p.order.Remove(f)
		delete(p.items, f.Value.(string))
	}
	e.id, e.expires = id, now.Add(pendingTTL)
	e.elem = p.order.PushBack(id)
	p.items[id] = &e
}

type takeResult int

const (
	takeOK      takeResult = iota
	takeUnknown            // no such id: expired, replayed, or never started here
	takeBinding            // the id exists but the callback's state is not its state
)

// take returns and removes the entry of id, atomically, if the callback's
// state is the one stored with it. A state that does not match leaves the entry
// alone, so a forged callback cannot burn someone else's sign-in. An expired
// entry is removed and counts as unknown.
func (p *pendingStore) take(id, state string) (*pendingEntry, takeResult) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.items[id]
	if !ok {
		return nil, takeUnknown
	}
	if !p.now().Before(e.expires) {
		p.order.Remove(e.elem)
		delete(p.items, id)
		return nil, takeUnknown
	}
	if subtle.ConstantTimeCompare([]byte(state), []byte(e.stateValue)) != 1 {
		return nil, takeBinding
	}
	p.order.Remove(e.elem)
	delete(p.items, id)
	return e, takeOK
}

func (p *pendingStore) len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.items)
}

// SafeReturn turns a client-supplied return path into one that is safe to
// redirect to: an absolute path on this site ("/s/01JD3K8Q7Z?x=1"), never a URL
// with a scheme or host, never "//host", never with a backslash or control
// character (browsers treat "\" like "/" and drop tabs and line breaks, which
// would turn "/\t/evil.example" into "//evil.example"). Anything else becomes "/".
func SafeReturn(s string) string {
	if s == "" || len(s) > 2048 || s[0] != '/' {
		return "/"
	}
	if len(s) > 1 && (s[1] == '/' || s[1] == '\\') {
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
	// The pages that handle sign-in themselves are not somewhere to return to.
	if u.Path == "/login" || u.Path == "/setup" || u.Path == "/invite" || strings.HasPrefix(u.Path, "/api/") {
		return "/"
	}
	return s
}
