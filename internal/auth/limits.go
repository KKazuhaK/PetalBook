package auth

import (
	"container/list"
	"sync"
	"time"
)

// Every table here is in memory, in this one process, and bounded: a key seen
// longest ago goes first, so made-up addresses and names cannot grow memory.
const maxLimitKeys = 10_000

// minuteLimiter counts requests per key in fixed one-minute windows.
type minuteLimiter struct {
	mu      sync.Mutex
	entries map[string]*minuteEntry
	order   *list.List // front = most recently seen
	max     int
	now     func() time.Time
}

type minuteEntry struct {
	key    string
	start  time.Time
	n      int
	warned bool
	elem   *list.Element
}

func newMinuteLimiter(max int, now func() time.Time) *minuteLimiter {
	return &minuteLimiter{entries: map[string]*minuteEntry{}, order: list.New(), max: max, now: now}
}

// take spends one request of the key's allowance (limit a minute). When none is
// left it returns how long to wait, and first reports whether this is the first
// refusal of the window (for the audit log, once per key and window).
func (l *minuteLimiter) take(key string, limit int) (wait time.Duration, ok, first bool) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[key]
	if e != nil && now.Sub(e.start) >= time.Minute {
		l.drop(e)
		e = nil
	}
	if e == nil {
		for len(l.entries) >= l.max {
			back := l.order.Back()
			if back == nil {
				break
			}
			l.drop(back.Value.(*minuteEntry))
		}
		e = &minuteEntry{key: key, start: now}
		e.elem = l.order.PushFront(e)
		l.entries[key] = e
	} else {
		l.order.MoveToFront(e.elem)
	}
	if e.n >= limit {
		first = !e.warned
		e.warned = true
		return e.start.Add(time.Minute).Sub(now), false, first
	}
	e.n++
	return 0, true, false
}

func (l *minuteLimiter) drop(e *minuteEntry) {
	l.order.Remove(e.elem)
	delete(l.entries, e.key)
}

func (l *minuteLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// failTable counts failures per key in a fixed window that starts at the key's
// first failure, and can lock a key for a while. A key under lock goes from the
// table only when every other key is locked too.
type failTable struct {
	mu      sync.Mutex
	entries map[string]*failEntry
	free    *list.List // keys not locked, front = most recently seen
	locked  *list.List // keys under lock, front = most recently seen
	max     int
	now     func() time.Time
}

type failEntry struct {
	key    string
	n      int
	start  time.Time
	until  time.Time // the lock ends; zero when not locked
	warned bool      // the audit log has been told this key went over the cap in this window
	elem   *list.Element
}

func newFailTable(max int, now func() time.Time) *failTable {
	return &failTable{entries: map[string]*failEntry{}, free: list.New(), locked: list.New(), max: max, now: now}
}

// liveLocked returns the entry if it still counts: a lock that has ended or a
// window that has run out forgets the key.
func (t *failTable) liveLocked(key string, window time.Duration) *failEntry {
	e := t.entries[key]
	if e == nil {
		return nil
	}
	now := t.now()
	if !e.until.IsZero() {
		if now.Before(e.until) {
			return e
		}
		t.drop(e)
		return nil
	}
	if now.Sub(e.start) >= window {
		t.drop(e)
		return nil
	}
	return e
}

// blocked says whether the key may not try now, and for how long: it is under
// lock, or has max failures in the current window.
func (t *failTable) blocked(key string, max int, window time.Duration) (time.Duration, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.liveLocked(key, window)
	if e == nil {
		return 0, false
	}
	if !e.until.IsZero() {
		return e.until.Sub(t.now()), true
	}
	if e.n >= max {
		return e.start.Add(window).Sub(t.now()), true
	}
	return 0, false
}

// lockedFor says whether the key is under lock, and for how long.
func (t *failTable) lockedFor(key string, window time.Duration) (time.Duration, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.liveLocked(key, window)
	if e == nil || e.until.IsZero() {
		return 0, false
	}
	return e.until.Sub(t.now()), true
}

// count is the key's failures in the current window.
func (t *failTable) count(key string, window time.Duration) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e := t.liveLocked(key, window); e != nil && e.until.IsZero() {
		return e.n
	}
	return 0
}

// failResult is what one more failure did to a key.
type failResult struct {
	n       int
	atCap   bool          // the key now has max failures (or is locked)
	first   bool          // the first time in this window it reached the cap
	locked  bool          // this failure started a lock
	wait    time.Duration // until the window ends, or the lock does
	lockFor time.Duration
}

// fail records one failure. With lock > 0, reaching max locks the key for that long.
func (t *failTable) fail(key string, max int, window, lock time.Duration) failResult {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.countLocked(key, window, max, lock)
}

// countLocked adds one failure to the key (making it when it is new). With
// max > 0 it also judges the count against the cap, locking the key when lock
// is set.
func (t *failTable) countLocked(key string, window time.Duration, max int, lock time.Duration) failResult {
	now := t.now()
	e := t.liveLocked(key, window)
	if e == nil {
		for len(t.entries) >= t.max {
			back := t.free.Back()
			if back == nil {
				back = t.locked.Back()
			}
			if back == nil {
				break
			}
			t.drop(back.Value.(*failEntry))
		}
		e = &failEntry{key: key, start: now}
		e.elem = t.free.PushFront(e)
		t.entries[key] = e
	} else {
		t.touch(e)
	}
	if !e.until.IsZero() { // already locked: nothing more to count
		return failResult{n: e.n, atCap: true, wait: e.until.Sub(now)}
	}
	e.n++
	res := failResult{n: e.n, wait: e.start.Add(window).Sub(now)}
	if max > 0 && e.n >= max {
		res.atCap = true
		res.first = !e.warned
		e.warned = true
		if lock > 0 {
			e.until = now.Add(lock)
			t.free.Remove(e.elem)
			e.elem = t.locked.PushFront(e)
			res.locked, res.lockFor, res.wait = true, lock, lock
		}
	}
	return res
}

// reserve counts an attempt as a failure before it starts, unless the key is
// blocked: attempts that run together then cannot each be judged against the
// count from before the burst. first says that this refusal is the first of
// the window (for the audit log). An attempt that turns out well gives its
// place back with refund.
func (t *failTable) reserve(key string, max int, window time.Duration) (wait time.Duration, ok, first bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	e := t.liveLocked(key, window)
	if e != nil {
		if !e.until.IsZero() {
			return e.until.Sub(now), false, false
		}
		if e.n >= max {
			first = !e.warned
			e.warned = true
			t.touch(e)
			return e.start.Add(window).Sub(now), false, first
		}
	}
	t.countLocked(key, window, 0, 0)
	return 0, true, false
}

// lockIfAtCap locks a key that has reached max failures (a failure of the
// attempt that reserved it has just been confirmed).
func (t *failTable) lockIfAtCap(key string, max int, window, lock time.Duration) (locked bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.liveLocked(key, window)
	if e == nil || !e.until.IsZero() || e.n < max || lock <= 0 {
		return false
	}
	e.until = t.now().Add(lock)
	t.free.Remove(e.elem)
	e.elem = t.locked.PushFront(e)
	return true
}

// refund takes back one failure (an attempt that turned out well).
func (t *failTable) refund(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.entries[key]
	if e == nil || !e.until.IsZero() {
		return
	}
	if e.n--; e.n <= 0 {
		t.drop(e)
	}
}

// resetWhere forgets every key the function says yes to (a password reset
// forgives the failures of one username, whichever address they came from).
func (t *failTable) resetWhere(match func(key string) bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, e := range t.entries {
		if match(k) {
			t.drop(e)
		}
	}
}

// reset forgets a key.
func (t *failTable) reset(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e := t.entries[key]; e != nil {
		t.drop(e)
	}
}

func (t *failTable) touch(e *failEntry) {
	if e.until.IsZero() {
		t.free.MoveToFront(e.elem)
	} else {
		t.locked.MoveToFront(e.elem)
	}
}

func (t *failTable) drop(e *failEntry) {
	if e.until.IsZero() {
		t.free.Remove(e.elem)
	} else {
		t.locked.Remove(e.elem)
	}
	delete(t.entries, e.key)
}

func (t *failTable) size() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.entries)
}

// bucketLimiter is a token bucket per key, bounded like the tables above, for
// the starts of single sign-on (30 a minute per address).
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
