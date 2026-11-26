package ratelimit

import (
	"sync"
	"time"
)

// Bucket is a token bucket per key: burst tokens, refilled at perMinute a
// minute. Safe for concurrent use.
//
// A full table forgets its least recently used key while that key still has a
// token (forgetting it gives back at most what it would have refilled to
// anyway); when that key has none, the newcomer is refused until it has.
type Bucket struct {
	mu    sync.Mutex
	m     map[string]*entry
	order list // least recently used first
	max   int
	rate  float64 // tokens a second
	burst float64
	now   Clock
}

// NewBucket makes a table of at most maxKeys keys.
func NewBucket(maxKeys, perMinute, burst int, now Clock) *Bucket {
	return &Bucket{m: map[string]*entry{}, max: maxKeys, rate: float64(perMinute) / 60, burst: float64(burst), now: now}
}

// Take spends a token; when none is left it returns how long to wait.
func (b *Bucket) Take(key string) (time.Duration, bool) {
	now := b.now()
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.m[key]
	if e == nil {
		for len(b.m) >= b.max {
			h := b.order.head
			if h == nil {
				break
			}
			b.refill(h, now)
			if h.tokens < 1 {
				return b.until(h), false
			}
			b.order.remove(h)
			delete(b.m, h.key)
		}
		e = &entry{key: key, tokens: b.burst, start: now}
		b.m[key] = e
		b.order.push(e)
	} else {
		b.order.toBack(e)
	}
	b.refill(e, now)
	if e.tokens < 1 {
		return b.until(e), false
	}
	e.tokens--
	return 0, true
}

// Len is the number of keys held.
func (b *Bucket) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.m)
}

func (b *Bucket) refill(e *entry, now time.Time) {
	e.tokens = min(b.burst, e.tokens+now.Sub(e.start).Seconds()*b.rate)
	e.start = now
}

// until is how long until the entry has a token.
func (b *Bucket) until(e *entry) time.Duration {
	return time.Duration((1 - e.tokens) / b.rate * float64(time.Second))
}
