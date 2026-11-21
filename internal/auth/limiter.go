package auth

import (
	"container/list"
	"sync"
	"time"
)

// failLimiter slows down guessing. Each key (a client address, a username)
// gets a few free failures, then a delay that doubles with every further one
// up to a cap. Entries are forgotten after a quiet window, and the table is
// bounded: the least recently seen key is dropped first, so a flood of made-up
// names cannot grow memory.
type failLimiter struct {
	mu      sync.Mutex
	entries map[string]*failEntry
	order   *list.List // front = most recently seen
	max     int
	free    int
	base    time.Duration
	cap     time.Duration
	window  time.Duration
	// decay forgives one failure per interval, so a slip now and then (one a
	// few minutes) never adds up to a lockout.
	decay time.Duration
	now   func() time.Time
}

type failEntry struct {
	key   string
	fails int
	until time.Time
	last  time.Time
	elem  *list.Element
}

func newFailLimiter(max, free int, base, cap, window time.Duration, now func() time.Time) *failLimiter {
	return &failLimiter{entries: map[string]*failEntry{}, order: list.New(), max: max, free: free, base: base, cap: cap, window: window, decay: 5 * time.Minute, now: now}
}

// wait returns how long the key must wait before trying again (0: go ahead).
func (l *failLimiter) wait(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.waitLocked(key)
}

// decayLocked forgives failures with the passing of time; false when the entry
// has nothing left and was dropped.
func (l *failLimiter) decayLocked(e *failEntry, now time.Time) bool {
	if steps := int(now.Sub(e.last) / l.decay); steps > 0 {
		e.fails -= steps
		e.last = e.last.Add(time.Duration(steps) * l.decay)
		if e.fails <= 0 {
			l.drop(e)
			return false
		}
		if e.fails <= l.free {
			e.until = time.Time{}
		}
	}
	return true
}

func (l *failLimiter) waitLocked(key string) time.Duration {
	now := l.now()
	e := l.entries[key]
	if e == nil || !l.decayLocked(e, now) {
		return 0
	}
	if now.Sub(e.last) > l.window {
		l.drop(e)
		return 0
	}
	if now.Before(e.until) {
		return e.until.Sub(now)
	}
	return 0
}

// reserve is wait and fail in one step, under one lock: the attempt is counted
// as a failure before it starts, so attempts that are in flight together
// cannot each be judged against the same count. It returns how long the key
// must wait (the attempt is then not counted), or 0 with the attempt reserved.
// An attempt that succeeds gives its place back with refund.
func (l *failLimiter) reserve(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if d := l.waitLocked(key); d > 0 {
		return d
	}
	l.failLocked(key)
	return 0
}

// refund takes back a reservation whose attempt succeeded (or was never made).
func (l *failLimiter) refund(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[key]
	if e == nil {
		return
	}
	if e.fails--; e.fails <= 0 {
		l.drop(e)
		return
	}
	if e.fails <= l.free {
		e.until = time.Time{}
	}
}

// fail records a failed attempt.
func (l *failLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failLocked(key)
}

func (l *failLimiter) failLocked(key string) {
	now := l.now()
	e := l.entries[key]
	if e != nil && !l.decayLocked(e, now) {
		e = nil
	}
	if e != nil && now.Sub(e.last) > l.window {
		l.drop(e)
		e = nil
	}
	if e == nil {
		for len(l.entries) >= l.max {
			if back := l.order.Back(); back != nil {
				l.drop(back.Value.(*failEntry))
			} else {
				break
			}
		}
		e = &failEntry{key: key}
		e.elem = l.order.PushFront(e)
		l.entries[key] = e
	} else {
		l.order.MoveToFront(e.elem)
	}
	e.fails++
	e.last = now
	if over := e.fails - l.free; over > 0 {
		d := l.base
		for i := 1; i < over && d < l.cap; i++ {
			d *= 2
		}
		e.until = now.Add(min(d, l.cap))
	}
}

// reset forgets a key (a successful sign-in clears the username's failures).
func (l *failLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.entries[key]; e != nil {
		l.drop(e)
	}
}

func (l *failLimiter) drop(e *failEntry) {
	l.order.Remove(e.elem)
	delete(l.entries, e.key)
}

func (l *failLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// bucket is a token bucket per key, bounded like failLimiter, for endpoints
// that anyone can call and that cannot "fail" (starting a sign-in).
type bucketLimiter struct {
	mu      sync.Mutex
	entries map[string]*bucketEntry
	order   *list.List
	max     int
	rate    float64 // tokens per second
	burst   float64
	now     func() time.Time
}

type bucketEntry struct {
	key    string
	tokens float64
	last   time.Time
	elem   *list.Element
}

func newBucketLimiter(max, perMinute, burst int, now func() time.Time) *bucketLimiter {
	return &bucketLimiter{entries: map[string]*bucketEntry{}, order: list.New(), max: max, rate: float64(perMinute) / 60, burst: float64(burst), now: now}
}

// take spends a token; when none is left it returns how long to wait.
func (b *bucketLimiter) take(key string) (time.Duration, bool) {
	now := b.now()
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.entries[key]
	if e == nil {
		for len(b.entries) >= b.max {
			back := b.order.Back()
			if back == nil {
				break
			}
			old := back.Value.(*bucketEntry)
			b.order.Remove(back)
			delete(b.entries, old.key)
		}
		e = &bucketEntry{key: key, tokens: b.burst, last: now}
		e.elem = b.order.PushFront(e)
		b.entries[key] = e
	} else {
		b.order.MoveToFront(e.elem)
	}
	e.tokens = min(b.burst, e.tokens+now.Sub(e.last).Seconds()*b.rate)
	e.last = now
	if e.tokens < 1 {
		return time.Duration((1 - e.tokens) / b.rate * float64(time.Second)), false
	}
	e.tokens--
	return 0, true
}
