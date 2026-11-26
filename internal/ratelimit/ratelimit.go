// Package ratelimit is the server's one in-memory rate limiter. It has three
// kinds of table, each keyed by a string (an address, a username, a person):
//
//   - Window counts what a key spends in a fixed window that starts at its
//     first spending (requests a minute, messages an hour, rows of batches).
//   - Lockout counts failures in a window that starts at the first failure,
//     caps them, and can lock a key for a while (sign-in, tokens, OAuth).
//   - Bucket is a token bucket per key (starts of single sign-on).
//
// # Bounds
//
// Every table holds at most its MaxKeys entries, in this one process, so
// made-up keys cannot grow memory. An entry is about 150 bytes plus its key
// and the map's share (roughly 200 bytes and the key all told), so a table of
// 10,000 keys holds about 2–3 MB at most. Each check is O(1) and allocates
// only when it adds a key, as the limiters this package replaced did.
//
// # Eviction
//
// A full table must make room for a new key, and the room must never be
// taken from the state that holds an attacker back. So a full table frees, in
// this order:
//
//  1. an entry whose window (or lock) is over: forgetting it changes nothing;
//  2. the entry of least weight that is not held: in a Window the one whose
//     window is nearest its end, in a Lockout the one with the fewest failures
//     (oldest first among equals), in a Bucket the least recently used one
//     while it still has a token;
//  3. nothing: when every entry is held — locked, at its cap, or a bucket
//     with no token — existing state is never dropped to let a newcomer in.
//
// A held entry is one that refuses its key now. Flooding the table with new
// keys therefore cannot wipe a victim's lockout or failure count: new keys
// carry one failure each and go before any key with more.
//
// # A full table is not a denial of service
//
// Filling a table with held entries must not lock everyone else out, so a new
// key that finds no room is not simply refused. What it gets is the table's
// Full mode (OnFull):
//
//   - Overflow (the default): all such keys share one overflow bucket of
//     max(16, MaxKeys/100) tokens, refilled at that many a minute. While it
//     has tokens they pass, uncounted per key; when it is spent they wait.
//   - Open: they pass, uncounted. For limits on a first request that must
//     never be turned away (the per-minute allowance of the sign-in
//     endpoints, the starts of single sign-on, bearer-token checks).
//   - Closed: they are refused until an entry runs out. Only for caps that
//     must fail closed and guard no sign-in: outgoing mail (a Window made
//     with KeepLive, which also never forgets a live entry).
//
// A Lockout never refuses a new key's attempt, whatever its mode: Reserve and
// Blocked let it try (a correct sign-in always gets through), and only a
// failure spends the overflow bucket; once it is spent, the failure is
// answered as at its cap (an immediate short lock, the bucket's wait).
package ratelimit

import (
	"time"
)

// Clock is the time source of a table; tests give a fake one.
type Clock func() time.Time

// entry is one key of any table. Each table uses the fields of its kind.
type entry struct {
	key    string
	start  time.Time // window start (Window, Lockout); last refill (Bucket)
	until  time.Time // Lockout: the lock ends; zero when not locked
	tokens float64   // Bucket
	n      int       // spent (Window) or failures (Lockout)
	warned bool      // the first refusal of the window has been reported
	held   bool      // refuses its key now: never evicted before free entries
	age    links     // Window, Lockout: every entry, oldest start first
	cls    links     // the entry's group (free) or the held list; Bucket: recency
	g      *group    // Lockout: the group of a free entry
}

type links struct{ prev, next *entry }

// list is an intrusive doubly linked list of entries over one of their links.
type list struct {
	head, tail *entry
	n          int
	age        bool // over entry.age, else entry.cls
}

func (l *list) at(e *entry) *links {
	if l.age {
		return &e.age
	}
	return &e.cls
}

func (l *list) push(e *entry) {
	k := l.at(e)
	k.prev, k.next = l.tail, nil
	if l.tail != nil {
		l.at(l.tail).next = e
	} else {
		l.head = e
	}
	l.tail = e
	l.n++
}

func (l *list) remove(e *entry) {
	k := l.at(e)
	if k.prev != nil {
		l.at(k.prev).next = k.next
	} else {
		l.head = k.next
	}
	if k.next != nil {
		l.at(k.next).prev = k.prev
	} else {
		l.tail = k.prev
	}
	k.prev, k.next = nil, nil
	l.n--
}

// toBack moves an entry to the tail (most recent).
func (l *list) toBack(e *entry) {
	if l.tail == e {
		return
	}
	l.remove(e)
	l.push(e)
}

// Full is what a new key gets from a table that has no room for it (see the
// package doc).
type Full uint8

const (
	Overflow Full = iota // a share of one overflow bucket; when it is spent, a wait
	Open                 // passes, uncounted
	Closed               // refused until an entry runs out
)

// budget is the shared overflow bucket of a table.
type budget struct {
	tokens, rate, burst float64
	last                time.Time
}

func newBudget(maxKeys int) budget {
	b := float64(max(16, maxKeys/100))
	return budget{tokens: b, burst: b, rate: b / 60}
}

// take spends n tokens; when there are not enough it says how long to wait.
func (b *budget) take(n float64, now time.Time) (time.Duration, bool) {
	d, ok := b.enough(n, now)
	if ok {
		b.tokens -= n
	}
	return d, ok
}

// has says whether a token is left, without spending it.
func (b *budget) has(now time.Time) (time.Duration, bool) { return b.enough(1, now) }

func (b *budget) enough(n float64, now time.Time) (time.Duration, bool) {
	if !b.last.IsZero() {
		b.tokens = min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.rate)
	}
	b.last = now
	if b.tokens < n {
		return time.Duration((n - b.tokens) / b.rate * float64(time.Second)), false
	}
	return 0, true
}

// fullWait is what a refused newcomer is told: until the first entry may go,
// never less than a second.
func fullWait(at, now time.Time) time.Duration {
	return max(at.Sub(now), time.Second)
}
