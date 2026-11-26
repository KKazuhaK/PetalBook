package ratelimit

import (
	"hash/maphash"
	"math"
	"time"
)

// The overflow sketch of a table counts the keys that have no entry: a
// count-min sketch of sketchRows rows of sketchWidth cells. A key maps to one
// cell in each row by a keyed hash (a seed of its own per table, so nobody can
// choose keys that land on a victim's cells), and its count is the least of its
// cells. Other keys can only add to a cell, so the count is never below the
// truth: a collision may hold a key back early, never late.
//
// Two windows. The sketch has two halves of the same shape, the current window
// and the one before, and a key's count is the sum of its cells in both. When
// a window ends the older half is zeroed and becomes the current one: a cell
// holds at most two windows of traffic, however long a stream of failures
// lasts, so one-off keys cannot pile up in it until they hold back keys never
// seen. A failure counted at t stays in the sum until at least t + window, so
// every failure of a key's own window (which started no earlier than a window
// ago) is in its count: rolling over counts longer, never shorter.
//
// A sketch window is at least as long as the longest lock asked of the sketch
// (hold), and a lock tops the key's count in the current half up to its cap:
// the key is then refused until that half is dropped, at least a window and
// so at least the lock later. There is no lock state of its own: a key is held
// back only by its own count, never by a cell another key locked.
//
// Cells are uint16 and stop at their top (sat); a saturated cell reads as
// above any cap.
//
// Size: 2 halves × 4 rows × 65,536 cells of 2 bytes, 1 MiB, made once, the
// first time the table is full; a table that never fills has none. A check is
// one hash and eight cell reads, and allocates nothing. A sketch used in
// neither half is idle and is not looked at.
const (
	sketchRows  = 4
	sketchWidth = 1 << 16
	sat         = math.MaxUint16
)

type sketch struct {
	seed   maphash.Seed
	width  int      // cells a row, a power of two
	cells  []uint16 // two halves of sketchRows*width cells
	cur    int      // the current half, 0 or 1
	used   [2]bool  // something was counted in the half since it was zeroed
	start  time.Time
	hold   time.Duration // the longest lock asked of the sketch
	capMax int           // the cap of the latest lock (LockedFor reads it)
}

func newSketch(now time.Time, width int) *sketch {
	if width <= 0 {
		width = sketchWidth
	}
	return &sketch{seed: maphash.MakeSeed(), width: width, cells: make([]uint16, 2*sketchRows*width), start: now}
}

func (s *sketch) period(window time.Duration) time.Duration { return max(window, s.hold) }

// at rolls the sketch forward to now: one window over drops the older half,
// two drop both.
func (s *sketch) at(now time.Time, window time.Duration) {
	p := s.period(window)
	switch {
	case now.Before(s.start.Add(p)):
	case now.Before(s.start.Add(2 * p)):
		s.cur ^= 1
		s.zero(s.cur)
		s.start = s.start.Add(p)
	default:
		s.zero(0)
		s.zero(1)
		s.start = now
	}
}

func (s *sketch) zero(h int) {
	if s.used[h] {
		n := sketchRows * s.width
		clear(s.cells[h*n : (h+1)*n])
		s.used[h] = false
	}
}

// quiet says that nothing counted in the sketch can hold anyone back, so a
// key with no entry need not be looked up in it. A nil sketch is quiet.
func (s *sketch) quiet(now time.Time, window time.Duration) bool {
	if s == nil {
		return true
	}
	s.at(now, window)
	return !s.used[0] && !s.used[1]
}

// wait is how long until the current window ends (at least a second): a key
// held back now is held at least that long.
func (s *sketch) wait(now time.Time, window time.Duration) time.Duration {
	return fullWait(s.start.Add(s.period(window)), now)
}

func (s *sketch) hash(key string) uint64 { return maphash.String(s.seed, key) }

// idx is the key's cell of row r in half h.
func (s *sketch) idx(h int, r int, x uint64) int {
	h1, h2 := uint32(x), uint32(x>>32)|1
	return (h*sketchRows+r)*s.width + int((h1+uint32(r)*h2)&uint32(s.width-1))
}

// count is the key's count over the two windows; a saturated cell counts as
// more than any cap.
func (s *sketch) count(key string, now time.Time, window time.Duration) int {
	s.at(now, window)
	return s.countOf(s.hash(key))
}

func (s *sketch) countOf(x uint64) int {
	n := math.MaxInt
	for r := range sketchRows {
		a, b := s.cells[s.idx(s.cur, r, x)], s.cells[s.idx(s.cur^1, r, x)]
		if a == sat || b == sat {
			continue
		}
		n = min(n, int(a)+int(b))
	}
	return n
}

// add counts n more for the key in the current window and returns its count.
func (s *sketch) add(key string, now time.Time, window time.Duration, n int) int {
	s.at(now, window)
	x := s.hash(key)
	s.addTo(x, n)
	return s.countOf(x)
}

func (s *sketch) addTo(x uint64, n int) {
	if n <= 0 {
		return
	}
	for r := range sketchRows {
		c := &s.cells[s.idx(s.cur, r, x)]
		*c = uint16(min(int(*c)+n, sat))
	}
	s.used[s.cur] = true
}

// refund takes back one that add counted for the key (a reservation given
// back): from the current half when the key has a count in each of its cells
// there, else from the older one. Without a count in every cell of either half
// there is nothing of the key's left to take back. A saturated cell stays, and
// a key at the cap of a lock keeps its lock. It does not roll the sketch (it
// has no window): what it reads may be a window old, which can only keep a
// count, never take one that is not there.
func (s *sketch) refund(key string) {
	x := s.hash(key)
	if s.capMax > 0 && s.countOf(x) >= s.capMax {
		return
	}
	for _, h := range [...]int{s.cur, s.cur ^ 1} {
		if s.minOf(h, x) == 0 {
			continue
		}
		for r := range sketchRows {
			if c := &s.cells[s.idx(h, r, x)]; *c != sat {
				*c--
			}
		}
		return
	}
}

func (s *sketch) minOf(h int, x uint64) uint16 {
	m := uint16(sat)
	for r := range sketchRows {
		m = min(m, s.cells[s.idx(h, r, x)])
	}
	return m
}

// lock holds the key back for at least lock: its count in the current half
// is topped up to the cap, and the sketch's windows are at least lock long,
// so the count stays at the cap until that half is dropped.
func (s *sketch) lock(key string, capMax int, lock time.Duration) {
	s.hold, s.capMax = max(s.hold, lock), capMax
	x := s.hash(key)
	s.addTo(x, capMax-int(s.minOf(s.cur, x)))
}

// lockedFor says whether the key is held back by a lock of the sketch: its
// count is at the cap of the latest lock. The sketch keeps no lock per key,
// so a key that reached that cap in a table that also counts without a lock
// reads as locked too (no table here does both).
func (s *sketch) lockedFor(key string, now time.Time, window time.Duration) (time.Duration, bool) {
	if s.capMax <= 0 || s.count(key, now, window) < s.capMax {
		return 0, false
	}
	return s.wait(now, window), true
}
