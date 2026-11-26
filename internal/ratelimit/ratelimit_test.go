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

// A table full of locks refuses newcomers (fail closed) and drops nothing.
func TestAFullTableOfLocksRefusesNewKeys(t *testing.T) {
	c := newClock()
	window, lock := 15*time.Minute, 15*time.Minute
	f := NewLockout(3, c.now)
	for i := range 3 {
		f.Fail("l"+strconv.Itoa(i), 1, window, lock)
	}
	c.t = c.t.Add(5 * time.Minute)
	if r := f.Fail("new", 1, window, lock); !r.Full || !r.AtCap || r.Wait != 10*time.Minute {
		t.Fatalf("%+v", r)
	}
	if wait, ok, _ := f.Reserve("new", 10, window); ok || wait != 10*time.Minute {
		t.Fatalf("%v %v", wait, ok)
	}
	if _, blocked := f.Blocked("new", 10, window); !blocked {
		t.Fatal("a full table lets a newcomer through")
	}
	for i := range 3 {
		if _, locked := f.LockedFor("l"+strconv.Itoa(i), window); !locked {
			t.Fatal("a lock was dropped for a newcomer")
		}
	}
	// When the locks end there is room again.
	c.t = c.t.Add(10 * time.Minute)
	if _, ok, _ := f.Reserve("new", 10, window); !ok || f.Len() > 3 {
		t.Fatalf("no room after the locks ended: %d", f.Len())
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
	// Full of capped keys: newcomers are refused.
	w2 := NewWindow(2, time.Minute, EvictFree, c.now)
	w2.Take("a", 1, 1)
	w2.Take("b", 1, 1)
	if _, ok, _ := w2.Take("c", 1, 1); ok {
		t.Fatal("a table full of capped keys took a newcomer")
	}
	// KeepLive forgets no live key at all.
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
	if _, ok := b.Take("new"); ok {
		t.Fatal("an empty bucket was forgotten for a newcomer")
	}
	if _, ok := b.Take("drained"); ok {
		t.Fatal("the drained bucket came back full")
	}
	c.t = c.t.Add(time.Second)
	if _, ok := b.Take("new"); !ok || b.Len() != 2 {
		t.Fatal("a refilled bucket does not make room")
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
	if n := testing.AllocsPerRun(100, func() {
		w.Take("k", 1, 1<<30)
		f.Fail("k", 1<<30, time.Hour, 0)
		f.Refund("k")
		f.Fail("k", 1<<30, time.Hour, 0)
		b.Take("k")
	}); n != 0 {
		t.Fatalf("%v allocations a check", n)
	}
}
