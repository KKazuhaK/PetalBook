package tokens

import (
	"sync"
	"time"

	"petalbook.app/server/internal/ratelimit"
)

// Limiter counts failed token attempts (or, for OAuth, registrations) per
// client address and blocks a client that reaches Max in a Window until the
// window ends. State is in memory only, in a ratelimit.Lockout of maxClients
// keys: a full table forgets the clients with the fewest failures first, never
// a blocked one. A new client that finds no room is let through (a correct
// token is never refused), unless Overflow is set: then new clients share the
// table's overflow bucket (OAuth registrations, which make rows).
type Limiter struct {
	Max      int           // failures allowed per Window; default 10
	Window   time.Duration // default 1 minute
	Now      func() time.Time
	Overflow bool

	once sync.Once
	t    *ratelimit.Lockout
}

// maxClients bounds the memory of the limiter.
const maxClients = 10000

func (l *Limiter) table() *ratelimit.Lockout {
	l.once.Do(func() {
		full := ratelimit.Open
		if l.Overflow {
			full = ratelimit.Overflow
		}
		l.t = ratelimit.NewLockout(maxClients, l.now).OnFull(full)
	})
	return l.t
}

func (l *Limiter) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l *Limiter) params() (int, time.Duration) {
	max, w := l.Max, l.Window
	if max <= 0 {
		max = 10
	}
	if w <= 0 {
		w = time.Minute
	}
	return max, w
}

// Blocked reports whether ip has used up its failures in the current window,
// and for how many more seconds.
func (l *Limiter) Blocked(ip string) (bool, int) {
	max, w := l.params()
	d, blocked := l.table().Blocked(ip, max, w)
	if !blocked {
		return false, 0
	}
	return true, int(d/time.Second + 1)
}

// Fail records a failed attempt.
func (l *Limiter) Fail(ip string) {
	max, w := l.params()
	l.table().Fail(ip, max, w, 0)
}
