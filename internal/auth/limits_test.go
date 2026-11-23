package auth

import (
	"strconv"
	"testing"
	"time"
)

func TestMinuteLimiterCountsAFixedMinute(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_000_000, 0)}
	l := newMinuteLimiter(100, clk.now)
	for range 10 {
		if _, ok, _ := l.take("a", 10); !ok {
			t.Fatal("refused within the allowance")
		}
	}
	wait, ok, first := l.take("a", 10)
	if ok || !first || wait <= 0 || wait > time.Minute {
		t.Fatalf("%v %v %v", wait, ok, first)
	}
	if _, ok, first := l.take("a", 10); ok || first {
		t.Fatal("the audit log is told once per window")
	}
	if _, ok, _ := l.take("b", 10); !ok {
		t.Fatal("another address shares the allowance")
	}
	clk.t = clk.t.Add(time.Minute)
	if _, ok, _ := l.take("a", 10); !ok {
		t.Fatal("the next minute starts again")
	}
	// A new limit applies at once.
	if _, ok, _ := l.take("c", 1); !ok {
		t.Fatal()
	}
	if _, ok, _ := l.take("c", 1); ok {
		t.Fatal("a lower limit did not apply")
	}
}

func TestFailTableWindowCapAndReserve(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_000_000, 0)}
	f := newFailTable(100, clk.now)
	const max, window = 3, 15 * time.Minute
	for range 3 {
		if _, ok, _ := f.reserve("ip", max, window); !ok {
			t.Fatal("blocked within the cap")
		}
	}
	wait, ok, first := f.reserve("ip", max, window)
	if ok || !first || wait != window {
		t.Fatalf("%v %v %v", wait, ok, first)
	}
	// The window ends where it began, from the first failure.
	clk.t = clk.t.Add(10 * time.Minute)
	if wait, ok, first := f.reserve("ip", max, window); ok || first || wait != 5*time.Minute {
		t.Fatalf("%v %v %v", wait, ok, first)
	}
	clk.t = clk.t.Add(5 * time.Minute)
	if _, ok, _ := f.reserve("ip", max, window); !ok {
		t.Fatal("still blocked after the window")
	}
	// A reservation given back leaves nothing.
	f.reset("ip")
	f.reserve("x", max, window)
	f.refund("x")
	f.refund("x")
	if f.size() != 0 {
		t.Fatal("the entry stays")
	}
}

func TestFailTableLocksAndTheLockOutlastsTheWindow(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_000_000, 0)}
	f := newFailTable(100, clk.now)
	const max = 2
	window, lock := 5*time.Minute, 30*time.Minute
	f.fail("k", max, window, lock)
	r := f.fail("k", max, window, lock)
	if !r.atCap || !r.locked || r.wait != lock || !r.first {
		t.Fatalf("%+v", r)
	}
	clk.t = clk.t.Add(10 * time.Minute) // the window is over, the lock is not
	if wait, locked := f.lockedFor("k", window); !locked || wait != 20*time.Minute {
		t.Fatalf("%v %v", wait, locked)
	}
	if _, blocked := f.blocked("k", max, window); !blocked {
		t.Fatal("a locked key tries")
	}
	// More failures while locked change nothing.
	if r := f.fail("k", max, window, lock); r.locked || r.wait != 20*time.Minute {
		t.Fatalf("%+v", r)
	}
	clk.t = clk.t.Add(20 * time.Minute)
	if _, locked := f.lockedFor("k", window); locked || f.count("k", window) != 0 {
		t.Fatal("the lock did not end")
	}
	// Without a lock the cap alone lasts the window.
	f.fail("n", max, window, 0)
	if r := f.fail("n", max, window, 0); !r.atCap || r.locked {
		t.Fatalf("%+v", r)
	}
}

func TestFailTableIsBoundedAndKeepsLockedKeysLongest(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_000_000, 0)}
	f := newFailTable(50, clk.now)
	window, lock := time.Hour, time.Hour
	f.fail("locked", 1, window, lock)
	for i := range 1000 {
		f.fail("k"+strconv.Itoa(i), 5, window, 0)
	}
	if n := f.size(); n > 50 {
		t.Fatalf("%d keys with a bound of 50", n)
	}
	if _, locked := f.lockedFor("locked", window); !locked {
		t.Fatal("a locked key was evicted while others were not locked")
	}
	// When every key is locked the longest-unseen goes.
	for i := range 100 {
		f.fail("l"+strconv.Itoa(i), 1, window, lock)
	}
	if n := f.size(); n > 50 {
		t.Fatalf("%d keys", n)
	}
	m := newMinuteLimiter(20, clk.now)
	for i := range 500 {
		m.take("k"+strconv.Itoa(i), 5)
	}
	if m.size() > 20 {
		t.Fatalf("%d minute entries", m.size())
	}
}

func TestLockIfAtCapLocksOnlyAKeyThatReachedTheCap(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_000_000, 0)}
	f := newFailTable(100, clk.now)
	f.reserve("ip", 2, time.Hour)
	if f.lockIfAtCap("ip", 2, time.Hour, time.Hour) {
		t.Fatal("locked below the cap")
	}
	f.reserve("ip", 2, time.Hour)
	if !f.lockIfAtCap("ip", 2, time.Hour, time.Hour) {
		t.Fatal("not locked at the cap")
	}
	if f.lockIfAtCap("ip", 2, time.Hour, time.Hour) {
		t.Fatal("locked twice")
	}
}

func TestTheAuditQueueDropsWhenFullAndSaysSo(t *testing.T) {
	s := &Service{audit: newAuditLog(), now: time.Now}
	for range auditQueueSize + 25 {
		s.Audit(AuditEvent{Kind: "login", Username: "u"})
	}
	if n := len(s.audit.ch); n != auditQueueSize {
		t.Fatalf("%d queued, want a full queue of %d", n, auditQueueSize)
	}
	if n := s.audit.dropped.Load(); n != 25 {
		t.Fatalf("%d dropped, want 25", n)
	}
	// Ids sort by arrival, so the log reads in order.
	a, _ := s.audit.row(AuditEvent{Kind: "x"}, time.Unix(1_000_000, 0))
	b, _ := s.audit.row(AuditEvent{Kind: "x"}, time.Unix(1_000_000, 0))
	if !(a.ID < b.ID) {
		t.Fatalf("%s %s", a.ID, b.ID)
	}
}

// A full table refuses a new key rather than forget an old one: forgetting
// would give a capped address its allowance back.
func TestAFullHourCapTableRefusesInsteadOfForgetting(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	h := newHourCaps(3)
	if _, ok := h.take(now, capKey{"to:victim", 1}); !ok {
		t.Fatal("refused within the allowance")
	}
	if _, ok := h.take(now, capKey{"to:victim", 1}); ok {
		t.Fatal("the cap is not kept")
	}
	refused := 0
	for i := range 10 {
		if _, ok := h.take(now.Add(time.Duration(i)*time.Second), capKey{"to:flood" + strconv.Itoa(i), 1}); !ok {
			refused++
		}
	}
	if refused == 0 || h.size() > 3 {
		t.Fatalf("refused %d, size %d", refused, h.size())
	}
	if _, ok := h.take(now.Add(time.Minute), capKey{"to:victim", 1}); ok {
		t.Fatal("flooding the table gave the capped key its allowance back")
	}
	// All or none: a refusal for room spends nothing of the other key.
	h2 := newHourCaps(1)
	h2.take(now, capKey{"by:ip", 5})
	if _, ok := h2.take(now, capKey{"by:ip", 5}, capKey{"to:new", 3}); ok {
		t.Fatal("no room, yet allowed")
	}
	// Room comes back an hour after a key was last used.
	if _, ok := h.take(now.Add(2*time.Hour), capKey{"to:later", 1}); !ok {
		t.Fatal("expired keys keep the table full")
	}
}
