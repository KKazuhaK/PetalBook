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
// A cell is windowed like an entry: it forgets its count once a window has
// passed since its last increment (later than the window of any one key in
// it), and it keeps a lock end, the latest of the keys in it. Times are whole
// seconds from the sketch's start, rounded so that a cell never forgets early.
//
// Size: 4 × 8,192 cells of 12 bytes, 384 KiB, made once, the first time the
// table is full; a table that never fills has none. A check is one hash and
// four cell reads, and allocates nothing.
const (
	sketchRows  = 4
	sketchWidth = 1 << 13
)

type cell struct{ n, last, until uint32 }

type sketch struct {
	seed    maphash.Seed
	base    time.Time
	touch   uint32 // the latest increment
	lockEnd uint32 // the latest lock end
	cells   [sketchRows * sketchWidth]cell
}

func newSketch(now time.Time) *sketch {
	return &sketch{seed: maphash.MakeSeed(), base: now.Add(-time.Second)}
}

func (s *sketch) floor(t time.Time) uint32 { return clampSecs(t.Sub(s.base) / time.Second) }
func (s *sketch) ceil(t time.Time) uint32 {
	return clampSecs((t.Sub(s.base) + time.Second - 1) / time.Second)
}

// secs is a duration in whole seconds, rounded up.
func secs(d time.Duration) uint32 { return clampSecs((d + time.Second - 1) / time.Second) }

func clampSecs(d time.Duration) uint32 {
	switch {
	case d < 0:
		return 0
	case d > math.MaxUint32:
		return math.MaxUint32
	}
	return uint32(d)
}

// quiet says that nothing counted in the sketch still holds anyone back, so a
// key with no entry need not be looked up in it. A nil sketch is quiet.
func (s *sketch) quiet(now time.Time, window time.Duration) bool {
	if s == nil {
		return true
	}
	t := s.floor(now)
	return t >= s.lockEnd && t >= s.touch && t-s.touch >= secs(window)
}

// row returns the key's cell in row r.
func (s *sketch) row(r int, h uint64) *cell {
	h1, h2 := uint32(h), uint32(h>>32)|1
	return &s.cells[r*sketchWidth+int((h1+uint32(r)*h2)&(sketchWidth-1))]
}

func (s *sketch) hash(key string) uint64 { return maphash.String(s.seed, key) }

// live is the cell's count at t (floor seconds), 0 once its window is over.
func (c *cell) live(t, w uint32) uint32 {
	if t >= c.last && t-c.last >= w {
		return 0
	}
	return c.n
}

// count is the key's count in the window.
func (s *sketch) count(key string, now time.Time, window time.Duration) int {
	h, t, w := s.hash(key), s.floor(now), secs(window)
	n := uint32(math.MaxUint32)
	for r := range sketchRows {
		n = min(n, s.row(r, h).live(t, w))
	}
	return int(n)
}

// has says whether the sketch holds anything against the key.
func (s *sketch) has(key string, now time.Time, window time.Duration) bool {
	if _, locked := s.lockedFor(key, now); locked {
		return true
	}
	return s.count(key, now, window) > 0
}

// lockedFor says whether every cell of the key is under lock, and for how long.
func (s *sketch) lockedFor(key string, now time.Time) (time.Duration, bool) {
	h, t := s.hash(key), s.floor(now)
	u := uint32(math.MaxUint32)
	for r := range sketchRows {
		u = min(u, s.row(r, h).until)
	}
	if u <= t {
		return 0, false
	}
	return time.Duration(u-t) * time.Second, true
}

// add counts n more for the key and returns its count.
func (s *sketch) add(key string, now time.Time, window time.Duration, n int) int {
	h, t, w, up := s.hash(key), s.floor(now), secs(window), s.ceil(now)
	got := uint32(math.MaxUint32)
	for r := range sketchRows {
		c := s.row(r, h)
		v := c.live(t, w)
		v = uint32(min(uint64(v)+uint64(n), math.MaxUint32))
		c.n, c.last = v, max(c.last, up)
		got = min(got, v)
	}
	s.touch = max(s.touch, up)
	return int(got)
}

// sub takes back one that add counted (a reservation given back). It is a
// no-op when one of the key's cells is empty: the count it took back is gone.
func (s *sketch) sub(key string) {
	h := s.hash(key)
	for r := range sketchRows {
		if s.row(r, h).n == 0 {
			return
		}
	}
	for r := range sketchRows {
		s.row(r, h).n--
	}
}

// lock locks the key until then.
func (s *sketch) lock(key string, until time.Time) {
	h, u := s.hash(key), s.ceil(until)
	for r := range sketchRows {
		c := s.row(r, h)
		c.until = max(c.until, u)
	}
	s.lockEnd = max(s.lockEnd, u)
}
