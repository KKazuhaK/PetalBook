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
	mu      sync.Mutex
	m       map[string]*entry
	age     list   // every entry, oldest start first
	held    list   // held entries, in the order they were held
	low     *group // free entries with the fewest failures; groups rise from here
	spare   *group // groups for reuse, so counting allocates nothing
	max     int
	full    Full
	spill   budget
	sk      *sketch // failures of keys with no entry; nil until the table is first full
	skWidth int     // cells a sketch row; 0 is sketchWidth (tests make it small)
	now     Clock
}

// group is the free entries with one failure count, oldest first.
type group struct {
	n          int
	l          list
	prev, next *group
}

// NewLockout makes a table of at most maxKeys keys.
func NewLockout(maxKeys int, now Clock) *Lockout {
	return &Lockout{m: map[string]*entry{}, age: list{age: true}, max: maxKeys, spill: newBudget(maxKeys), now: now}
}

// OnFull sets what a new key's failure (and Blocked) gets when there is no
// room (see the package doc). Reserve lets a new key try in every mode.
func (t *Lockout) OnFull(f Full) *Lockout {
	t.full = f
	return t
}

// Result is what one more failure did to a key.
type Result struct {
	N       int
	AtCap   bool          // the key now has max failures, is locked, or could not be counted
	First   bool          // the first time in this window it reached the cap
	Locked  bool          // this failure started a lock
	Full    bool          // the key has no entry: it was counted in the overflow sketch (see OnFull)
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
// place back with Refund. A new key that finds no room is counted in the
// overflow sketch and refused only at its own cap there (or under its lock):
// a full table never refuses an attempt of a key that may be right.
func (t *Lockout) Reserve(key string, max int, window time.Duration) (wait time.Duration, ok, first bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	e := t.live(key, window, now)
	if e == nil && t.sketched(key, now, window) {
		return t.reserveSketch(key, max, window, now)
	}
	if e != nil {
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
	if e == nil {
		if _, ok := t.canAdd(now, window); !ok {
			return t.reserveSketch(key, max, window, now)
		}
	}
	t.count(key, now, window, 0, 0)
	return 0, true, false
}

// reserveSketch is Reserve for a key with no entry. Its refusals are not
// reported as first (the sketch keeps no per-key flag).
func (t *Lockout) reserveSketch(key string, max int, window time.Duration, now time.Time) (time.Duration, bool, bool) {
	sk := t.sketch(now)
	if sk.count(key, now, window) >= max {
		return sk.wait(now, window), false, false
	}
	sk.add(key, now, window, 1)
	return 0, true, false
}

// sketch returns the overflow sketch, made the first time it is needed.
func (t *Lockout) sketch(now time.Time) *sketch {
	if t.sk == nil {
		t.sk = newSketch(now, t.skWidth)
	}
	return t.sk
}

// sketched says whether a key with no entry has failures in the sketch: it
// is then counted there, not given an entry, until they are over. An idle
// sketch is not looked at.
func (t *Lockout) sketched(key string, now time.Time, window time.Duration) bool {
	return !t.sk.quiet(now, window) && t.sk.count(key, now, window) > 0
}

// Blocked says whether the key may not try now, and for how long: it is under
// lock or has max failures in the current window, in its entry or else in
// the overflow sketch. A new key that finds no room is otherwise blocked only
// in Closed mode, or in Overflow mode while the overflow bucket is spent.
func (t *Lockout) Blocked(key string, max int, window time.Duration) (time.Duration, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	e := t.live(key, window, now)
	if e == nil {
		if !t.sk.quiet(now, window) && t.sk.count(key, now, window) >= max {
			return t.sk.wait(now, window), true
		}
		d, ok := t.canAdd(now, window)
		switch {
		case ok || t.full == Open:
			return 0, false
		case t.full == Overflow:
			if w, ok := t.spill.has(now); !ok {
				return w, true
			}
			return 0, false
		}
		return d, true
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
	if e == nil {
		if t.sk.quiet(now, window) {
			return 0, false
		}
		return t.sk.lockedFor(key, now, window)
	}
	if e.until.IsZero() {
		return 0, false
	}
	return e.until.Sub(now), true
}

// Count is the key's failures in the current window (0 under lock); for a
// key with no entry, its count in the sketch (a lock there is a count at the
// cap).
func (t *Lockout) Count(key string, window time.Duration) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	e := t.live(key, window, now)
	switch {
	case e != nil && e.until.IsZero():
		return e.n
	case e == nil && !t.sk.quiet(now, window):
		return t.sk.count(key, now, window)
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
	if e == nil && lock > 0 && !t.sk.quiet(now, window) {
		if t.sk.count(key, now, window) < max {
			return false
		}
		t.sk.lock(key, max, lock)
		return true
	}
	if e == nil || !e.until.IsZero() || e.n < max || lock <= 0 {
		return false
	}
	t.lock(e, now.Add(lock))
	return true
}

// Refund takes back one failure (an attempt that turned out well). A locked
// key keeps its lock. A key with no entry gives back one count of the
// sketch: that is where its Reserve counted it, or where its entry's
// failures went when the entry was forgotten to make room (evict).
func (t *Lockout) Refund(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.m[key]
	if e == nil {
		if t.sk != nil {
			t.sk.refund(key)
		}
		return
	}
	if !e.until.IsZero() {
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

// Reset forgets a key's entry. What the overflow sketch counted for it stays
// until its two windows are over: a sketch cannot take one key out.
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
		if t.sketched(key, now, window) {
			return t.countSketch(key, now, window, max, lock, 0)
		}
		if d, ok := t.room(now, window); !ok {
			return t.countSketch(key, now, window, max, lock, d)
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

// countSketch counts a failure of a key with no entry in the overflow
// sketch, locking it there at its cap. Then the table's Full mode: Overflow
// spends the shared bucket and answers as at the cap once it is spent; Closed
// answers as at the cap (d is the wait for room).
func (t *Lockout) countSketch(key string, now time.Time, window time.Duration, max int, lock, d time.Duration) Result {
	sk := t.sketch(now)
	if max > 0 {
		if n := sk.count(key, now, window); n >= max { // held back already: nothing more to count
			return Result{N: n, AtCap: true, Full: true, Wait: sk.wait(now, window)}
		}
	}
	res := Result{N: sk.add(key, now, window, 1), Full: true, Wait: sk.wait(now, window)}
	if max > 0 && res.N >= max {
		res.AtCap = true
		res.First = true
		if lock > 0 {
			sk.lock(key, max, lock)
			res.Locked, res.LockFor, res.Wait = true, lock, lock
		}
		return res
	}
	switch t.full {
	case Overflow:
		if w, ok := t.spill.take(1, now); !ok {
			res.AtCap, res.Wait = true, w
		}
	case Closed:
		res.AtCap, res.Wait = true, d
		if d < time.Second {
			res.Wait = time.Second
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
			t.evict(t.low.l.head, now, window)
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

// evict forgets a live free entry to make room. Its failures go to the
// overflow sketch, so the key is still held to them (and a Refund of an
// attempt it reserved takes back its own count there, no other key's).
func (t *Lockout) evict(e *entry, now time.Time, window time.Duration) {
	sk := t.sketch(now)
	sk.at(now, window)
	sk.addTo(sk.hash(e.key), e.n)
	t.drop(e)
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
