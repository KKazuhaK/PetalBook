package ratelimit

import (
	"sync"
	"time"
)

// Policy says what a full Window may forget to make room.
type Policy uint8

const (
	// EvictFree lets a full table forget the live entry nearest the end of
	// its window that is not at its cap.
	EvictFree Policy = iota
	// KeepLive never forgets a live entry: a full table refuses newcomers
	// until a window is over.
	KeepLive
)

// Window counts what each key spends in a fixed window that starts at the
// key's first spending. Safe for concurrent use.
type Window struct {
	mu      sync.Mutex
	m       map[string]*entry
	age     list // every entry, oldest start first: the first to end
	free    list // entries below their cap, oldest start first
	max     int
	window  time.Duration
	policy  Policy
	full    Full
	spill   budget
	sk      *sketch // what keys with no entry spent (Open, Overflow); nil until first full
	skWidth int     // cells a sketch row; 0 is sketchWidth (tests make it small)
	now     Clock
}

// NewWindow makes a table of at most maxKeys keys with windows of the given
// length. A full EvictFree table gives newcomers the Overflow bucket, a
// KeepLive one refuses them (Closed); OnFull changes that.
func NewWindow(maxKeys int, window time.Duration, policy Policy, now Clock) *Window {
	full := Overflow
	if policy == KeepLive {
		full = Closed
	}
	return &Window{m: map[string]*entry{}, age: list{age: true}, max: maxKeys, window: window, policy: policy, full: full, spill: newBudget(maxKeys), now: now}
}

// OnFull sets what a new key gets when there is no room (see the package doc).
func (w *Window) OnFull(f Full) *Window {
	w.full = f
	return w
}

// Take spends n of the key's allowance of limit in its window. When that would
// go over, nothing is spent and wait is how long until the window ends; first
// reports the first refusal of the window (for an audit log, once per key and
// window). A new key that finds no room gets the table's Full mode; in Open
// and Overflow mode what it spends is counted in the overflow sketch and held
// to its limit there.
func (w *Window) Take(key string, n, limit int) (wait time.Duration, ok, first bool) {
	now := w.now()
	w.mu.Lock()
	defer w.mu.Unlock()
	e := w.live(key, now)
	if e == nil {
		sketched := !w.sk.quiet(now, w.window) && w.sk.count(key, now, w.window) > 0
		d, room := time.Duration(0), true
		if !sketched {
			d, room = w.room(1, now, w.policy)
		}
		if sketched || !room {
			switch {
			case n > limit:
				return w.window, false, false
			case w.full == Closed && !sketched:
				return d, false, false
			}
			return w.takeSketch(key, n, limit, now)
		}
		e = w.add(key, now)
	}
	if e.n+n > limit {
		first = !e.warned
		e.warned = true
		w.hold(e)
		return e.start.Add(w.window).Sub(now), false, first
	}
	e.n += n
	if e.n >= limit {
		w.hold(e)
	}
	return 0, true, false
}

// takeSketch is Take for a key with no entry: its own count in the sketch
// first, then (Overflow) the shared bucket.
func (w *Window) takeSketch(key string, n, limit int, now time.Time) (time.Duration, bool, bool) {
	if w.sk == nil {
		w.sk = newSketch(now, w.skWidth)
	}
	if w.sk.count(key, now, w.window)+n > limit {
		return w.sk.wait(now, w.window), false, false
	}
	if w.full == Overflow {
		if d, ok := w.spill.take(float64(n), now); !ok {
			return d, false, false
		}
	}
	w.sk.add(key, now, w.window, n)
	return 0, true, false
}

// Cost is one key and its limit for TakeAll.
type Cost struct {
	Key   string
	Limit int
}

// TakeAll spends one of each key's allowance, all or none; when one is spent
// it returns how long until its window ends. Room for new keys is made only
// from windows that are over, whatever the policy: a key of this very call is
// never forgotten to make room for another. It is for Closed tables (mail):
// without room it refuses.
func (w *Window) TakeAll(keys ...Cost) (wait time.Duration, ok bool) {
	now := w.now()
	w.mu.Lock()
	defer w.mu.Unlock()
	need := 0
	for _, k := range keys {
		e := w.live(k.Key, now)
		if e == nil {
			need++
			continue
		}
		if e.n >= k.Limit {
			w.hold(e)
			return e.start.Add(w.window).Sub(now), false
		}
	}
	if d, room := w.room(need, now, KeepLive); !room {
		return d, false
	}
	for _, k := range keys {
		e := w.m[k.Key]
		if e == nil {
			e = w.add(k.Key, now)
		}
		if e.n++; e.n >= k.Limit {
			w.hold(e)
		}
	}
	return 0, true
}

// Allows says whether TakeAll of the keys would pass now, spending nothing:
// for a caller that spends in two tables and must not spend in one when the
// other refuses (it holds its own lock over both). When not, it returns how
// long until the refusing key's window ends.
func (w *Window) Allows(keys ...Cost) (wait time.Duration, ok bool) {
	now := w.now()
	w.mu.Lock()
	defer w.mu.Unlock()
	need := 0
	for _, k := range keys {
		e := w.live(k.Key, now)
		if e == nil {
			need++
			continue
		}
		if e.n >= k.Limit {
			return e.start.Add(w.window).Sub(now), false
		}
	}
	if len(w.m)+need > w.max {
		return w.room(need, now, KeepLive)
	}
	return 0, true
}

// Len is the number of keys held.
func (w *Window) Len() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.m)
}

// live returns the key's entry while its window lasts; an ended one is forgotten.
func (w *Window) live(key string, now time.Time) *entry {
	e := w.m[key]
	if e != nil && now.Sub(e.start) >= w.window {
		w.drop(e)
		return nil
	}
	return e
}

// room frees space for need more keys (see the package doc); false and the
// wait when it cannot.
func (w *Window) room(need int, now time.Time, policy Policy) (time.Duration, bool) {
	for need > 0 && len(w.m)+need > w.max {
		if h := w.age.head; h != nil && now.Sub(h.start) >= w.window {
			w.drop(h)
			continue
		}
		if policy == EvictFree && w.free.head != nil {
			w.drop(w.free.head)
			continue
		}
		if h := w.age.head; h != nil {
			return fullWait(h.start.Add(w.window), now), false
		}
		return fullWait(now.Add(w.window), now), false
	}
	return 0, true
}

func (w *Window) add(key string, now time.Time) *entry {
	e := &entry{key: key, start: now}
	w.m[key] = e
	w.age.push(e)
	w.free.push(e)
	return e
}

func (w *Window) hold(e *entry) {
	if e.held {
		return
	}
	w.free.remove(e)
	e.held = true
}

func (w *Window) drop(e *entry) {
	w.age.remove(e)
	if !e.held {
		w.free.remove(e)
	}
	delete(w.m, e.key)
}
