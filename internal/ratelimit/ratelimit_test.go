package ratelimit

import (
	"strconv"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newClock() *clock { return &clock{t: time.Unix(1_000_000, 0)} }

func TestWindowCountsAFixedWindow(t *testing.T) {
	c := newClock()
	w := NewWindow(100, time.Minute, EvictFree, c.now)
	for range 10 {
		if _, ok, _ := w.Take("a", 1, 10); !ok {
			t.Fatal("refused within the allowance")
		}
	}
	wait, ok, first := w.Take("a", 1, 10)
	if ok || !first || wait != time.Minute {
		t.Fatalf("%v %v %v", wait, ok, first)
	}
	if _, ok, first := w.Take("a", 1, 10); ok || first {
		t.Fatal("first is reported once per window")
	}
	c.t = c.t.Add(40 * time.Second)
	if wait, ok, _ := w.Take("a", 1, 10); ok || wait != 20*time.Second {
		t.Fatalf("the window does not end where it began: %v", wait)
	}
	c.t = c.t.Add(20 * time.Second)
	if _, ok, _ := w.Take("a", 1, 10); !ok {
		t.Fatal("the next window starts again")
	}
	// Weighted: what does not fit is not spent.
	if _, ok, _ := w.Take("rows", 4000, 5000); !ok {
		t.Fatal()
	}
	if wait, ok, _ := w.Take("rows", 1001, 5000); ok || wait != time.Minute {
		t.Fatalf("%v %v", wait, ok)
	}
	if _, ok, _ := w.Take("rows", 1000, 5000); !ok {
		t.Fatal("a refusal spent the allowance")
	}
	if wait, ok, _ := w.Take("big", 6000, 5000); ok || wait != time.Minute {
		t.Fatalf("more than the limit at once: %v %v", wait, ok)
	}
}

func TestTakeAllIsAllOrNone(t *testing.T) {
	c := newClock()
	w := NewWindow(100, time.Hour, KeepLive, c.now)
	if _, ok := w.TakeAll(Cost{"to:a", 3}, Cost{"by:ip", 1}); !ok {
		t.Fatal()
	}
	if _, ok := w.TakeAll(Cost{"to:b", 3}, Cost{"by:ip", 1}); ok {
		t.Fatal("the caller's cap is not kept")
	}
	if w.Len() != 2 {
		t.Fatalf("a refusal added a key: %d", w.Len())
	}
	if _, ok := w.TakeAll(Cost{"to:a", 3}); !ok {
		t.Fatal("the refusal spent to:a")
	}
}

func TestLockoutWindowCapAndReserve(t *testing.T) {
	c := newClock()
	f := NewLockout(100, c.now)
	const max, window = 3, 15 * time.Minute
	for range 3 {
		if _, ok, _ := f.Reserve("ip", max, window); !ok {
			t.Fatal("blocked within the cap")
		}
	}
	wait, ok, first := f.Reserve("ip", max, window)
	if ok || !first || wait != window {
		t.Fatalf("%v %v %v", wait, ok, first)
	}
	c.t = c.t.Add(10 * time.Minute)
	if wait, ok, first := f.Reserve("ip", max, window); ok || first || wait != 5*time.Minute {
		t.Fatalf("%v %v %v", wait, ok, first)
	}
	c.t = c.t.Add(5 * time.Minute)
	if _, ok, _ := f.Reserve("ip", max, window); !ok {
		t.Fatal("still blocked after the window")
	}
	f.Reset("ip")
	f.Reserve("x", max, window)
	f.Refund("x")
	f.Refund("x")
	if f.Len() != 0 {
		t.Fatal("a refunded entry stays")
	}
}

func TestLockoutLocksAndTheLockOutlastsTheWindow(t *testing.T) {
	c := newClock()
	f := NewLockout(100, c.now)
	const max = 2
	window, lock := 5*time.Minute, 30*time.Minute
	f.Fail("k", max, window, lock)
	r := f.Fail("k", max, window, lock)
	if !r.AtCap || !r.Locked || r.Wait != lock || !r.First {
		t.Fatalf("%+v", r)
	}
	c.t = c.t.Add(10 * time.Minute)
	if wait, locked := f.LockedFor("k", window); !locked || wait != 20*time.Minute {
		t.Fatalf("%v %v", wait, locked)
	}
	if _, blocked := f.Blocked("k", max, window); !blocked {
		t.Fatal("a locked key tries")
	}
	if r := f.Fail("k", max, window, lock); r.Locked || r.Wait != 20*time.Minute {
		t.Fatalf("%+v", r)
	}
	c.t = c.t.Add(20 * time.Minute)
	if _, locked := f.LockedFor("k", window); locked || f.Count("k", window) != 0 {
		t.Fatal("the lock did not end")
	}
	f.Fail("n", max, window, 0)
	if r := f.Fail("n", max, window, 0); !r.AtCap || r.Locked {
		t.Fatalf("%+v", r)
	}
	// LockIfAtCap locks only a key at its cap, once.
	f.Reserve("ip", 2, time.Hour)
	if f.LockIfAtCap("ip", 2, time.Hour, time.Hour) {
		t.Fatal("locked below the cap")
	}
	f.Reserve("ip", 2, time.Hour)
	if !f.LockIfAtCap("ip", 2, time.Hour, time.Hour) || f.LockIfAtCap("ip", 2, time.Hour, time.Hour) {
		t.Fatal("LockIfAtCap")
	}
}

func TestBucketBurstAndRefill(t *testing.T) {
	c := newClock()
	b := NewBucket(100, 60, 3, c.now) // one a second, burst of 3
	for range 3 {
		if _, ok := b.Take("a"); !ok {
			t.Fatal("burst refused")
		}
	}
	if d, ok := b.Take("a"); ok || d != time.Second {
		t.Fatalf("%v %v", d, ok)
	}
	if _, ok := b.Take("b"); !ok {
		t.Fatal("another key shares the bucket")
	}
	c.t = c.t.Add(1100 * time.Millisecond)
	if _, ok := b.Take("a"); !ok {
		t.Fatal("no token after waiting")
	}
}

// The attack: a flood of new keys must not wipe a victim's failures or lock.
func TestAFloodOfNewKeysKeepsTheVictimsFailuresAndLock(t *testing.T) {
	c := newClock()
	const keys, max = 50, 10
	window, lock := 15*time.Minute, 15*time.Minute
	f := NewLockout(keys, c.now)
	for range 5 {
		f.Fail("victim", max, window, lock)
	}
	for range max {
		f.Fail("locked", max, window, lock)
	}
	for i := range 10 * keys {
		c.t = c.t.Add(time.Millisecond)
		f.Fail("flood"+strconv.Itoa(i), max, window, lock)
		// Some flood keys climb too, still below the victim.
		f.Fail("flood"+strconv.Itoa(i), max, window, lock)
	}
	if n := f.Len(); n > keys {
		t.Fatalf("%d keys with a bound of %d", n, keys)
	}
	if n := f.Count("victim", window); n != 5 {
		t.Fatalf("the victim's failures went to %d", n)
	}
	if _, locked := f.LockedFor("locked", window); !locked {
		t.Fatal("the flood wiped a lock")
	}
}

// A table full of locks drops no lock, never refuses a new key's attempt, and
// gives new keys' failures a shared overflow bucket; once it is spent a
// failure is answered as at its cap.
func TestAFullTableOfLocksLetsNewKeysTry(t *testing.T) {
	c := newClock()
	window, lock := 15*time.Minute, 15*time.Minute
	f := NewLockout(3, c.now)
	for i := range 3 {
		f.Fail("l"+strconv.Itoa(i), 1, window, lock)
	}
	if _, ok, _ := f.Reserve("new", 10, window); !ok {
		t.Fatal("a full table refused a new key's attempt")
	}
	if _, blocked := f.Blocked("new", 10, window); blocked {
		t.Fatal("a full table blocks a new key")
	}
	f.Refund("new")  // the attempt was right: nothing to give back
	const spill = 16 // max(16, 3/100)
	for i := range spill {
		if r := f.Fail("new"+strconv.Itoa(i), 10, window, lock); !r.Full || r.AtCap {
			t.Fatalf("overflow failure %d: %+v", i, r)
		}
	}
	r := f.Fail("one-more", 10, window, lock)
	if !r.Full || !r.AtCap || r.Wait <= 0 || r.Wait > 4*time.Second {
		t.Fatalf("a spent overflow: %+v", r)
	}
	if _, blocked := f.Blocked("one-more", 10, window); !blocked {
		t.Fatal("Overflow mode: a spent bucket blocks newcomers")
	}
	if _, ok, _ := f.Reserve("one-more", 10, window); !ok {
		t.Fatal("even a spent overflow must not refuse an attempt")
	}
	if _, blocked := f.OnFull(Open).Blocked("one-more", 10, window); blocked {
		t.Fatal("Open mode blocks a newcomer")
	}
	for i := range 3 {
		if _, locked := f.LockedFor("l"+strconv.Itoa(i), window); !locked {
			t.Fatal("a lock was dropped for a newcomer")
		}
	}
	c.t = c.t.Add(lock)
	if _, ok, _ := f.Reserve("new", 10, window); !ok || f.Count("new", window) != 1 {
		t.Fatal("no room after the locks ended")
	}
}

func TestAFullWindowKeepsCappedKeys(t *testing.T) {
	c := newClock()
	w := NewWindow(3, time.Minute, EvictFree, c.now)
	w.Take("victim", 1, 1) // at its cap
	for i := range 100 {
		c.t = c.t.Add(time.Millisecond)
		w.Take("k"+strconv.Itoa(i), 1, 5)
	}
	if w.Len() > 3 {
		t.Fatalf("%d keys", w.Len())
	}
	if _, ok, _ := w.Take("victim", 1, 1); ok {
		t.Fatal("the flood gave the capped key its allowance back")
	}
	// Full of capped keys: newcomers share the overflow bucket, then wait.
	w2 := NewWindow(2, time.Minute, EvictFree, c.now)
	w2.Take("a", 1, 1)
	w2.Take("b", 1, 1)
	for i := range 16 {
		if _, ok, _ := w2.Take("c"+strconv.Itoa(i), 1, 1); !ok {
			t.Fatal("a newcomer was refused while the overflow had room")
		}
	}
	if _, ok, _ := w2.Take("late", 1, 1); ok {
		t.Fatal("a spent overflow let a newcomer through")
	}
	if _, ok, _ := w2.OnFull(Open).Take("late", 1, 1); !ok {
		t.Fatal("Open refused a first request")
	}
	// KeepLive (mail) is Closed: it forgets no live key and refuses newcomers.
	k := NewWindow(2, time.Hour, KeepLive, c.now)
	k.TakeAll(Cost{"to:victim", 3})
	k.TakeAll(Cost{"to:other", 3})
	if _, ok := k.TakeAll(Cost{"to:flood", 3}); ok {
		t.Fatal("KeepLive made room from a live key")
	}
	c.t = c.t.Add(time.Hour)
	if _, ok := k.TakeAll(Cost{"to:later", 3}); !ok {
		t.Fatal("ended windows keep the table full")
	}
}

func TestAFullBucketTableKeepsEmptyBuckets(t *testing.T) {
	c := newClock()
	b := NewBucket(2, 60, 1, c.now)
	b.Take("drained") // no token left
	b.Take("other")   // no token left either
	for range 16 {
		if _, ok := b.Take("new"); !ok {
			t.Fatal("a newcomer was refused while the overflow had room")
		}
	}
	if _, ok := b.Take("new"); ok {
		t.Fatal("a spent overflow let a newcomer through")
	}
	if _, ok := b.Take("drained"); ok {
		t.Fatal("the drained bucket came back full")
	}
	if _, ok := b.OnFull(Open).Take("new"); !ok {
		t.Fatal("Open refused a first request")
	}
	c.t = c.t.Add(time.Second)
	if _, ok := b.Take("new"); !ok || b.Len() != 2 {
		t.Fatal("a refilled bucket does not make room")
	}
}

// The people search and batch gates are buckets: the rate holds across what
// used to be a window edge, with no second burst there.
func TestABucketHasNoSecondBurstAtAWindowEdge(t *testing.T) {
	c := newClock()
	b := NewBucket(10, 500, 5000, c.now) // the batch gate: 5,000 rows in 10 minutes
	if _, ok := b.TakeN("p", 1); !ok {
		t.Fatal()
	}
	c.t = c.t.Add(10*time.Minute - time.Second)
	if _, ok := b.TakeN("p", 4999); !ok {
		t.Fatal("the burst is the old limit")
	}
	// A fixed window from the first row would start again here with 5,000.
	c.t = c.t.Add(2 * time.Second)
	if _, ok := b.TakeN("p", 100); ok {
		t.Fatal("a second burst at the window edge")
	}
	c.t = c.t.Add(time.Minute) // the average rate: 500 a minute
	if _, ok := b.TakeN("p", 500); !ok {
		t.Fatal("the rate is not 500 a minute")
	}
	if _, ok := b.TakeN("p", 100); ok {
		t.Fatal("refill beyond the rate")
	}
	if wait, ok := b.TakeN("p", 6000); ok || wait <= 0 {
		t.Fatal("more than the burst fits")
	}
}

func TestChecksDoNotAllocateForKnownKeys(t *testing.T) {
	c := newClock()
	w := NewWindow(10, time.Minute, EvictFree, c.now)
	f := NewLockout(10, c.now)
	b := NewBucket(10, 60, 1_000_000, c.now)
	w.Take("k", 1, 1<<30)
	f.Fail("k", 1<<30, time.Hour, 0)
	b.Take("k")
	// A full table counts a new key in its sketch, made by the first one.
	full := NewLockout(1, c.now).OnFull(Open)
	full.Fail("locked", 1, time.Hour, time.Hour)
	full.Fail("new", 1<<30, time.Hour, 0)
	if n := testing.AllocsPerRun(100, func() {
		full.Fail("new", 1<<30, time.Hour, 0)
		full.Reserve("new", 1<<30, time.Hour)
		full.Refund("new")
		full.LockedFor("new", time.Hour)
		w.Take("k", 1, 1<<30)
		f.Fail("k", 1<<30, time.Hour, 0)
		f.Refund("k")
		f.Fail("k", 1<<30, time.Hour, 0)
		b.Take("k")
	}); n != 0 {
		t.Fatalf("%v allocations a check", n)
	}
}

// fillWithLocks fills a table of 3 keys with locked entries.
func fillWithLocks(f *Lockout, window, lock time.Duration) {
	for i := range 3 {
		f.Fail("l"+strconv.Itoa(i), 1, window, lock)
	}
}

// The bypass: a table full of locks once let a new name be guessed without
// limit (nothing was counted, so it was never locked). Its failures now go to
// the overflow sketch: it is locked at its cap, and while locked even a
// correct attempt is refused. Another new name still signs in.
func TestAFullTableOfLocksStillLocksANewName(t *testing.T) {
	for _, mode := range []Full{Overflow, Open} {
		c := newClock()
		window, lock := 15*time.Minute, 15*time.Minute
		const max = 3
		f := NewLockout(3, c.now).OnFull(mode)
		fillWithLocks(f, window, lock)
		var r Result
		for range max + 1 { // as Login: LockedFor, then the wrong password
			if _, locked := f.LockedFor("victim", window); locked {
				break
			}
			r = f.Fail("victim", max, window, lock)
		}
		if !r.Locked || !r.AtCap || r.Wait != lock {
			t.Fatalf("mode %d: the new name's failures were not counted: %+v", mode, r)
		}
		if wait, locked := f.LockedFor("victim", window); !locked || wait != lock {
			t.Fatalf("mode %d: the next correct attempt is not refused: %v %v", mode, wait, locked)
		}
		if _, blocked := f.Blocked("victim", max, window); !blocked {
			t.Fatalf("mode %d: Blocked lets the locked name try", mode)
		}
		if _, locked := f.LockedFor("someone", window); locked || f.Count("someone", window) != 0 {
			t.Fatalf("mode %d: another new name is held back", mode)
		}
		c.t = c.t.Add(lock - time.Second)
		if _, locked := f.LockedFor("victim", window); !locked {
			t.Fatalf("mode %d: the lock ended early", mode)
		}
		c.t = c.t.Add(lock + time.Second) // the sketch holds a key one to two windows
		if _, locked := f.LockedFor("victim", window); locked {
			t.Fatalf("mode %d: the lock did not end", mode)
		}
	}
}

// The address table is gated by Reserve: a new address in a full table is
// counted in the sketch, refused at its cap and locked by LockIfAtCap; a
// right attempt gives its place back.
func TestAFullTableOfLocksStillCountsANewAddress(t *testing.T) {
	c := newClock()
	window, lock := 15*time.Minute, 15*time.Minute
	const max = 3
	f := NewLockout(3, c.now)
	fillWithLocks(f, window, lock)
	for range 2 * max { // right attempts give their place back
		if _, ok, _ := f.Reserve("ip:new", max, window); !ok {
			t.Fatal("a right attempt was refused")
		}
		f.Refund("ip:new")
	}
	for range max {
		if _, ok, _ := f.Reserve("ip:new", max, window); !ok {
			t.Fatal("refused within the cap")
		}
	}
	if f.Count("ip:new", window) != max {
		t.Fatalf("count %d", f.Count("ip:new", window))
	}
	if _, ok, _ := f.Reserve("ip:new", max, window); ok {
		t.Fatal("a new address in a full table tries without limit")
	}
	if !f.LockIfAtCap("ip:new", max, window, lock) {
		t.Fatal("LockIfAtCap did not lock the new address")
	}
	if _, locked := f.LockedFor("ip:new", window); !locked {
		t.Fatal("not locked")
	}
	if _, ok, _ := f.Reserve("ip:other", max, window); !ok {
		t.Fatal("another new address was refused")
	}
}

// A per-minute allowance that is Open counts newcomers in the sketch when full.
func TestAFullOpenWindowStillCountsANewKey(t *testing.T) {
	c := newClock()
	w := NewWindow(3, time.Minute, EvictFree, c.now).OnFull(Open)
	for i := range 3 {
		w.Take("capped"+strconv.Itoa(i), 1, 1)
	}
	for range 3 {
		if _, ok, _ := w.Take("new", 1, 3); !ok {
			t.Fatal("refused within the allowance")
		}
	}
	if wait, ok, _ := w.Take("new", 1, 3); ok || wait != time.Minute {
		t.Fatalf("a new key in a full Open table is never limited: %v %v", wait, ok)
	}
	if _, ok, _ := w.Take("other", 1, 3); !ok {
		t.Fatal("another new key was refused")
	}
	c.t = c.t.Add(2 * time.Minute) // the sketch counts a key one to two windows
	if _, ok, _ := w.Take("new", 1, 3); !ok {
		t.Fatal("the window did not end")
	}
}

// Eviction must not hand a partly drained bucket a fresh burst: only a bucket
// that has refilled to its burst is forgotten.
func TestEvictionKeepsAPartlyDrainedBucket(t *testing.T) {
	c := newClock()
	b := NewBucket(1, 60, 3, c.now)
	b.TakeN("a", 2) // one token left
	if _, ok := b.Take("b"); !ok {
		t.Fatal("the newcomer gets the overflow bucket")
	}
	if _, ok := b.TakeN("a", 2); ok {
		t.Fatal("eviction gave the drained bucket a fresh burst")
	}
	c.t = c.t.Add(2 * time.Second) // refilled to its burst
	if _, ok := b.Take("b"); !ok || b.Len() != 1 {
		t.Fatal("a full bucket does not make room")
	}
}

// A few goroutines on small tables, for -race.
func TestTablesAreSafeForConcurrentUse(t *testing.T) {
	c := newClock()
	f := NewLockout(3, c.now)
	w := NewWindow(3, time.Minute, EvictFree, c.now).OnFull(Open)
	b := NewBucket(3, 60, 5, c.now)
	done := make(chan struct{})
	for g := range 4 {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := range 20 {
				k := strconv.Itoa((g + i) % 6)
				f.Fail(k, 3, time.Minute, time.Minute)
				f.Reserve("r"+k, 3, time.Minute)
				f.Refund("r" + k)
				f.Blocked(k, 3, time.Minute)
				f.LockedFor(k, time.Minute)
				w.Take(k, 1, 3)
				b.Take(k)
			}
		}()
	}
	for range 4 {
		<-done
	}
	if f.Len() > 3 || w.Len() > 3 || b.Len() > 3 {
		t.Fatal("a bound was passed")
	}
}

// Round 2: a steady stream of one-off failures from distinct keys, sent while
// the table is full, must not pile up in the overflow sketch until it holds
// back keys it has never seen. The sketch is made small (64 cells a row) so a
// few thousand failures do what millions would do to the real one: 256
// failures a window are four a cell, eight over the two windows it keeps,
// far below the cap of 30, for as long as the stream lasts.
func TestSustainedOneOffFailuresDoNotHoldBackNewKeys(t *testing.T) {
	c := newClock()
	window, lock := 15*time.Minute, 15*time.Minute
	const max, perWindow, windows = 30, 256, 16
	f := NewLockout(3, c.now)
	f.skWidth = 64
	fillWithLocks(f, window, 100*window) // locks that outlast the stream
	for range max {                      // a name locked in the sketch: LockedFor reads it
		f.Fail("victim", max, window, lock)
	}
	for i := range windows * perWindow {
		c.t = c.t.Add(window / perWindow)
		if k := "bot" + strconv.Itoa(i); i%2 == 0 {
			f.Fail(k, max, window, lock) // a wrong password of a new name
		} else {
			f.Reserve(k, max, window) // an address's attempt that failed: never refunded
		}
	}
	held := 0
	for i := range 100 {
		k := "fresh" + strconv.Itoa(i)
		_, locked := f.LockedFor(k, window)
		_, blocked := f.Blocked(k, max, window)
		_, ok, _ := f.Reserve(k, max, window)
		if locked || blocked || !ok {
			held++
		}
		f.Refund(k)
	}
	if held > 0 {
		t.Fatalf("%d of 100 keys never seen are held back by others' one-off failures", held)
	}
}

// A key with no entry is never held back later than an entry would be: its
// failures still count after the sketch rolls to a new window, and a lock
// longer than the window holds it for at least the lock.
func TestASketchedKeyCountsAcrossTheRollAndKeepsItsLock(t *testing.T) {
	c := newClock()
	window, lock := 15*time.Minute, 45*time.Minute
	const max = 3
	f := NewLockout(3, c.now)
	fillWithLocks(f, 10*window, 10*window)
	f.Fail("victim", max, window, 0) // makes the sketch, its window starting now
	c.t = c.t.Add(window - time.Second)
	f.Fail("victim", max, window, 0)
	c.t = c.t.Add(2 * time.Second) // the sketch has rolled; the key's own window has not ended
	if r := f.Fail("victim", max, window, lock); !r.AtCap || !r.Locked || r.Wait != lock {
		t.Fatalf("the failures before the roll were forgotten: %+v", r)
	}
	c.t = c.t.Add(lock - time.Second)
	if _, locked := f.LockedFor("victim", window); !locked {
		t.Fatal("the lock ended before its time")
	}
	if _, ok, _ := f.Reserve("victim", max, window); ok {
		t.Fatal("a locked name may try")
	}
	c.t = c.t.Add(2 * lock)
	if _, locked := f.LockedFor("victim", window); locked {
		t.Fatal("the lock never ended")
	}
}

// Once two windows pass with nothing counted, the sketch is idle: keys are
// not looked up in it, and a new key that finds room gets an entry.
func TestAnIdleSketchIsNotConsulted(t *testing.T) {
	c := newClock()
	window := 15 * time.Minute
	const max = 5
	f := NewLockout(3, c.now)
	fillWithLocks(f, window, window)
	f.Fail("victim", max, window, window)
	f.Fail("victim", max, window, window)
	c.t = c.t.Add(window)
	if f.sk.quiet(c.t, window) || f.Count("victim", window) != 2 {
		t.Fatalf("the sketch forgot within a window: count %d", f.Count("victim", window))
	}
	c.t = c.t.Add(window)
	if !f.sk.quiet(c.t, window) {
		t.Fatal("a sketch idle for two windows is still consulted")
	}
	if r := f.Fail("victim", max, window, window); r.Full || r.N != 1 {
		t.Fatalf("a key with room was not given an entry: %+v", r)
	}
}

// Refund gives back only what the key's own Reserve counted: when its entry
// was forgotten in between, the sketch must not lose another key's failures.
// A sketch of one cell a row puts every key on the same cells.
func TestRefundAfterEvictionTakesNothingFromOthers(t *testing.T) {
	c := newClock()
	window, lock := 15*time.Minute, 15*time.Minute
	const max = 10
	f := NewLockout(3, c.now)
	f.skWidth = 1
	f.Fail("l0", 1, window, lock)
	f.Fail("l1", 1, window, lock)
	f.Reserve("a", max, window)  // a free entry of one
	f.Reserve("b", max, window)  // forgets a to make room
	f.Fail("b", 1, window, lock) // the table is now all locks
	for range 3 {
		f.Fail("victim", max, window, lock)
	}
	f.Refund("a") // a's attempt turned out well
	if n := f.Count("victim", window); n < 3 {
		t.Fatalf("a refund of a forgotten entry took the victim's failures: %d", n)
	}
}
