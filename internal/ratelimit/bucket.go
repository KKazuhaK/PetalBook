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
// anyway); when that key has none, the newcomer gets the table's Full mode.
type Bucket struct {
	mu    sync.Mutex
	m     map[string]*entry
	order list // least recently used first
	max   int
	rate  float64 // tokens a second
	burst float64
	full  Full
	spill budget
	now   Clock
}

// NewBucket makes a table of at most maxKeys keys; a full one gives
// newcomers the Overflow bucket unless OnFull says otherwise.
func NewBucket(maxKeys, perMinute, burst int, now Clock) *Bucket {
	return &Bucket{m: map[string]*entry{}, max: maxKeys, rate: float64(perMinute) / 60, burst: float64(burst), spill: newBudget(maxKeys), now: now}
}

// OnFull sets what a new key gets when there is no room (see the package doc).
func (b *Bucket) OnFull(f Full) *Bucket {
	b.full = f
	return b
}

// Take spends a token; when none is left it returns how long to wait.
func (b *Bucket) Take(key string) (time.Duration, bool) { return b.TakeN(key, 1) }

// TakeN spends n tokens, all or none; when there are not enough it returns how
// long until there are (more than the burst never fits).
func (b *Bucket) TakeN(key string, n int) (time.Duration, bool) {
	now := b.now()
	want := float64(n)
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
				switch {
				case want > b.burst:
					return b.wait(want, 0), false
				case b.full == Open:
					return 0, true
				case b.full == Overflow:
					return b.spill.take(want, now)
				}
				return b.wait(1, h.tokens), false
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
	if e.tokens < want {
		return b.wait(want, e.tokens), false
	}
	e.tokens -= want
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

// wait is how long until a bucket at tokens has want.
func (b *Bucket) wait(want, tokens float64) time.Duration {
	return time.Duration((want - tokens) / b.rate * float64(time.Second))
}
