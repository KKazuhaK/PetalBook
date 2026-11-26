package ratelimit

import (
	"sync"
	"time"
)

// Lockout counts failures per key in a fixed window that starts at the key's
// first failure, caps them, and can lock a key for a while. The cap, the
// window and the lock are given with each call, so a change of settings
// applies at once. Safe for concurrent use.
//
// Free entries (not held) sit in groups by failure count, lowest first, so
// the entry a full table forgets is always one with the fewest failures; an
// entry at its cap or under lock is held and is never forgotten while it
// lasts (see the package doc).
type Lockout struct {
	mu    sync.Mutex
	m     map[string]*entry
	age   list   // every entry, oldest start first
	held  list   // held entries, in the order they were held
	low   *group // free entries with the fewest failures; groups rise from here
	spare *group // groups for reuse, so counting allocates nothing
	max   int
	now   Clock
}

// group is the free entries with one failure count, oldest first.
type group struct {
	n          int
	l          list
	prev, next *group
}

// NewLockout makes a table of at most maxKeys keys.
func NewLockout(maxKeys int, now Clock) *Lockout {
	return &Lockout{m: map[string]*entry{}, age: list{age: true}, max: maxKeys, now: now}
}

// Result is what one more failure did to a key.
type Result struct {
	N       int
	AtCap   bool          // the key now has max failures, is locked, or could not be counted
	First   bool          // the first time in this window it reached the cap
	Locked  bool          // this failure started a lock
	Full    bool          // the table was full of held keys: nothing was counted, the key is refused
	Wait    time.Duration // until the window ends, or the lock does
	LockFor time.Duration
}

// Fail records one failure. With max > 0 it judges the count against the cap,
// and with lock > 0 reaching the cap locks the key for that long.
func (t *Lockout) Fail(key string, max int, window, lock time.Duration) Result {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.count(key, t.now(), window, max, lock)
}

// Reserve counts an attempt as a failure before it starts, unless the key is
// blocked: attempts that run together then cannot each be judged against the
// count from before the burst. first says that this refusal is the first of
// the window (for an audit log). An attempt that turns out well gives its
// place back with Refund. A full table refuses a new key (fail closed).
func (t *Lockout) Reserve(key string, max int, window time.Duration) (wait time.Duration, ok, first bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	if e := t.live(key, window, now); e != nil {
		if !e.until.IsZero() {
			return e.until.Sub(now), false, false
		}
		if e.n >= max {
			first = !e.warned
			e.warned = true
			t.hold(e)
			return e.start.Add(window).Sub(now), false, first
		}
	}
	if r := t.count(key, now, window, 0, 0); r.Full {
		return r.Wait, false, false
	}
	return 0, true, false
}

// Blocked says whether the key may not try now, and for how long: it is under
// lock, has max failures in the current window, or is new to a table full of
// held keys.
func (t *Lockout) Blocked(key string, max int, window time.Duration) (time.Duration, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	e := t.live(key, window, now)
	if e == nil {
		if d, ok := t.canAdd(now, window); !ok {
			return d, true
		}
		return 0, false
	}
	if !e.until.IsZero() {
		return e.until.Sub(now), true
	}
	if e.n >= max {
		return e.start.Add(window).Sub(now), true
	}
	return 0, false
}

// LockedFor says whether the key is under lock, and for how long.
func (t *Lockout) LockedFor(key string, window time.Duration) (time.Duration, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	e := t.live(key, window, now)
	if e == nil || e.until.IsZero() {
		return 0, false
	}
	return e.until.Sub(now), true
}

// Count is the key's failures in the current window (0 under lock).
func (t *Lockout) Count(key string, window time.Duration) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e := t.live(key, window, t.now()); e != nil && e.until.IsZero() {
		return e.n
	}
	return 0
}

// LockIfAtCap locks a key that has reached max failures (a failure of the
// attempt that reserved it has just been confirmed).
func (t *Lockout) LockIfAtCap(key string, max int, window, lock time.Duration) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	e := t.live(key, window, now)
	if e == nil || !e.until.IsZero() || e.n < max || lock <= 0 {
		return false
	}
	t.lock(e, now.Add(lock))
	return true
}

// Refund takes back one failure (an attempt that turned out well). A locked
// key keeps its lock.
func (t *Lockout) Refund(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.m[key]
	if e == nil || !e.until.IsZero() {
		return
	}
	if e.n <= 1 {
		t.drop(e)
		return
	}
	if e.held {
		e.n--
		return
	}
	t.regroup(e, e.n-1)
}

// Reset forgets a key.
func (t *Lockout) Reset(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e := t.m[key]; e != nil {
		t.drop(e)
	}
}

// ResetWhere forgets every key the function says yes to (a password reset
// forgives the failures of one username, whichever address they came from).
// It walks the whole table.
func (t *Lockout) ResetWhere(match func(key string) bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, e := range t.m {
		if match(k) {
			t.drop(e)
		}
	}
}

// Len is the number of keys held.
func (t *Lockout) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.m)
}

// count adds one failure to the key, making it when it is new and there is room.
func (t *Lockout) count(key string, now time.Time, window time.Duration, max int, lock time.Duration) Result {
	e := t.live(key, window, now)
	if e == nil {
		if d, ok := t.room(now, window); !ok {
			return Result{AtCap: true, Full: true, Wait: d}
		}
		e = &entry{key: key, start: now}
		t.m[key] = e
		t.age.push(e)
	}
	if !e.until.IsZero() { // already locked: nothing more to count
		return Result{N: e.n, AtCap: true, Wait: e.until.Sub(now)}
	}
	if e.held {
		e.n++
	} else {
		t.regroup(e, e.n+1)
	}
	res := Result{N: e.n, Wait: e.start.Add(window).Sub(now)}
	if max > 0 && e.n >= max {
		res.AtCap = true
		res.First = !e.warned
		e.warned = true
		if lock > 0 {
			t.lock(e, now.Add(lock))
			res.Locked, res.LockFor, res.Wait = true, lock, lock
		} else {
			t.hold(e)
		}
	}
	return res
}

// live returns the entry if it still counts: a lock that has ended or a
// window that has run out forgets the key.
func (t *Lockout) live(key string, window time.Duration, now time.Time) *entry {
	e := t.m[key]
	if e != nil && t.over(e, window, now) {
		t.drop(e)
		return nil
	}
	return e
}

func (t *Lockout) over(e *entry, window time.Duration, now time.Time) bool {
	if !e.until.IsZero() {
		return !now.Before(e.until)
	}
	return now.Sub(e.start) >= window
}

func (t *Lockout) ends(e *entry, window time.Duration) time.Time {
	if !e.until.IsZero() {
		return e.until
	}
	return e.start.Add(window)
}

// canAdd forgets entries that are over from the heads of the lists, and says
// whether a new key would find room; when not, how long until it might.
func (t *Lockout) canAdd(now time.Time, window time.Duration) (time.Duration, bool) {
	for len(t.m) >= t.max {
		if h := t.age.head; h != nil && t.over(h, window, now) {
			t.drop(h)
			continue
		}
		if h := t.held.head; h != nil && t.over(h, window, now) {
			t.drop(h)
			continue
		}
		if t.low != nil {
			return 0, true
		}
		var first time.Time
		for _, h := range [...]*entry{t.age.head, t.held.head} {
			if h != nil && (first.IsZero() || t.ends(h, window).Before(first)) {
				first = t.ends(h, window)
			}
		}
		if first.IsZero() {
			first = now.Add(window)
		}
		return fullWait(first, now), false
	}
	return 0, true
}

// room makes space for one more key: an entry that is over, else the free
// entry with the fewest failures; never a held one.
func (t *Lockout) room(now time.Time, window time.Duration) (time.Duration, bool) {
	for len(t.m) >= t.max {
		if d, ok := t.canAdd(now, window); !ok {
			return d, false
		}
		if len(t.m) >= t.max {
			t.drop(t.low.l.head)
		}
	}
	return 0, true
}

// regroup moves a free entry to the group of count n, one above or below its
// own (or into the first group when it is new).
func (t *Lockout) regroup(e *entry, n int) {
	old := e.g
	var to *group
	switch {
	case old == nil: // new: n is 1, the lowest a free entry has
		if t.low != nil && t.low.n == n {
			to = t.low
		} else {
			to = t.newGroup(n)
			t.insertBefore(t.low, to)
		}
	case n > old.n:
		if to = old.next; to == nil || to.n != n {
			to = t.newGroup(n)
			t.insertAfter(old, to)
		}
	default:
		if to = old.prev; to == nil || to.n != n {
			to = t.newGroup(n)
			t.insertBefore(old, to)
		}
	}
	if old != nil {
		t.leave(e)
	}
	e.n, e.g = n, to
	to.l.push(e)
}

// hold takes an entry out of the free groups: it is not forgotten while it lasts.
func (t *Lockout) hold(e *entry) {
	if e.held {
		return
	}
	t.leave(e)
	e.held = true
	t.held.push(e)
}

func (t *Lockout) lock(e *entry, until time.Time) {
	e.until = until
	if e.held {
		t.held.toBack(e)
		return
	}
	t.hold(e)
}

// leave takes a free entry out of its group, dropping the group when empty.
func (t *Lockout) leave(e *entry) {
	g := e.g
	if g == nil {
		return
	}
	g.l.remove(e)
	e.g = nil
	if g.l.n > 0 {
		return
	}
	if g.prev != nil {
		g.prev.next = g.next
	} else {
		t.low = g.next
	}
	if g.next != nil {
		g.next.prev = g.prev
	}
	*g = group{next: t.spare}
	t.spare = g
}

func (t *Lockout) drop(e *entry) {
	t.age.remove(e)
	if e.held {
		t.held.remove(e)
	} else {
		t.leave(e)
	}
	delete(t.m, e.key)
}

func (t *Lockout) newGroup(n int) *group {
	g := t.spare
	if g == nil {
		return &group{n: n}
	}
	t.spare = g.next
	*g = group{n: n}
	return g
}

// insertBefore puts g before b; b nil means the groups are empty.
func (t *Lockout) insertBefore(b, g *group) {
	if b == nil {
		g.prev, g.next = nil, nil
		t.low = g
		return
	}
	g.prev, g.next = b.prev, b
	if b.prev != nil {
		b.prev.next = g
	} else {
		t.low = g
	}
	b.prev = g
}

func (t *Lockout) insertAfter(a, g *group) {
	g.prev, g.next = a, a.next
	if a.next != nil {
		a.next.prev = g
	}
	a.next = g
}
