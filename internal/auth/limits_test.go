package auth

import (
	"strconv"
	"testing"
	"time"

	"petalbook.app/server/internal/ratelimit"
)

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

// Each limiter keeps the limits it had before it moved to internal/ratelimit.
func TestTheLimitersKeepTheirLimits(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_000_000, 0)}
	s := &Service{}
	s.initLimits(clk.now)

	// Requests a minute per address: the setting's number, a fixed minute.
	for range 10 {
		if _, ok, _ := s.perMin.Take("ip:a", 1, 10); !ok {
			t.Fatal("perMin refused within the allowance")
		}
	}
	if wait, ok, first := s.perMin.Take("ip:a", 1, 10); ok || !first || wait != time.Minute {
		t.Fatalf("perMin: %v %v %v", wait, ok, first)
	}
	// One throttled audit row per address, reason and minute.
	if _, ok, _ := s.auditIP.Take("r|a", 1, 1); !ok {
		t.Fatal()
	}
	if wait, ok, _ := s.auditIP.Take("r|a", 1, 1); ok || wait != time.Minute {
		t.Fatalf("auditIP: %v %v", wait, ok)
	}
	// Starts of single sign-on: a burst of 20, then one every two seconds.
	for range 20 {
		if _, ok := s.starts.Take("a"); !ok {
			t.Fatal("starts refused within the burst")
		}
	}
	if wait, ok := s.starts.Take("a"); ok || wait != 2*time.Second {
		t.Fatalf("starts: %v %v", wait, ok)
	}
	// Mail: 3 an hour per address, 5 per caller, all or none.
	for i := range 3 {
		if _, ok := s.mailSlot("v@example.com", "ip"+strconv.Itoa(i)); !ok {
			t.Fatal("mail refused within the cap")
		}
	}
	if wait, ok := s.mailSlot("v@example.com", "ip9"); ok || wait != time.Hour {
		t.Fatalf("mail per address: %v %v", wait, ok)
	}
	for i := range 5 {
		s.mailSlot("p"+strconv.Itoa(i)+"@example.com", "caller")
	}
	if _, ok := s.mailSlot("q@example.com", "caller"); ok {
		t.Fatal("the caller's cap of 5 is not kept")
	}
	// The failure tables take their cap, window and lock from the settings at
	// each call; a lock outlasts the window.
	window, lock := 15*time.Minute, 30*time.Minute
	for range 9 {
		s.failUser.Fail("u", 10, window, lock)
	}
	if r := s.failUser.Fail("u", 10, window, lock); !r.Locked || r.Wait != lock {
		t.Fatalf("failUser: %+v", r)
	}
	// Bounds: 10,000 keys, the test mail 4, registrations per tenant 2,000.
	for i := range maxLimitKeys + 100 {
		s.failPair.Fail("p"+strconv.Itoa(i), 10, window, 0)
	}
	if n := s.failPair.Len(); n != maxLimitKeys {
		t.Fatalf("failPair holds %d keys", n)
	}
	for i := range 10 {
		s.testCaps.TakeAll(ratelimit.Cost{Key: "t" + strconv.Itoa(i), Limit: 10})
	}
	if n := s.testCaps.Len(); n != 4 {
		t.Fatalf("testCaps holds %d keys", n)
	}
}

// An attacker who fills every table with locked or capped keys must not lock
// everyone out: a new person's correct sign-in still gets through each gate
// that Login passes (the per-minute allowance, the address's reservation, the
// locks of the name and the pair), and wrong ones are still held back.
func TestFullTablesOfLocksDoNotBlockACorrectSignIn(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_000_000, 0)}
	s := &Service{}
	s.initLimits(clk.now)
	st := &secState{window: 15 * time.Minute, lock: 15 * time.Minute}
	st.Limits.PerMinute, st.Limits.MaxFailures = 10, 10
	for i := range maxLimitKeys {
		k := strconv.Itoa(i)
		s.perMin.Take("ip:x"+k, 10, 10)
		s.failIP.Fail("ip:x"+k, 1, st.window, st.lock)
		s.failUser.Fail("x"+k, 1, st.window, st.lock)
		s.failPair.Fail(pairKey("x"+k, "x"+k), 1, st.window, st.lock)
	}
	if s.failPair.Len() != maxLimitKeys || s.perMin.Len() != maxLimitKeys {
		t.Fatal("the tables are not full")
	}
	for i := range 3 * maxLimitKeys { // well past any overflow budget
		ip, ukey := "new"+strconv.Itoa(i), "user"+strconv.Itoa(i)
		if _, ok, _ := s.perMin.Take("ip:"+ip, 1, st.Limits.PerMinute); !ok {
			t.Fatal("the per-minute allowance refused a first request")
		}
		if err := s.gateIP(st, ip); err != nil {
			t.Fatalf("the address's reservation refused a sign-in: %v", err)
		}
		if _, locked := s.failUser.LockedFor(ukey, st.window); locked {
			t.Fatal("a new name is locked")
		}
		if _, locked := s.failPair.LockedFor(pairKey(ip, ukey), st.window); locked {
			t.Fatal("a new pair is locked")
		}
		s.failUser.Reset(ukey)
		s.failPair.Reset(pairKey(ip, ukey))
		s.refundIP(ip)
	}
	if _, locked := s.failPair.LockedFor(pairKey("x0", "x0"), st.window); !locked {
		t.Fatal("a lock was dropped")
	}
	// Wrong passwords of new names spend the overflow bucket, then are
	// answered as at the cap (429) rather than counted nowhere.
	capped := false
	for i := range 1000 {
		if r := s.failUser.Fail("guess"+strconv.Itoa(i), 10, st.window, st.lock); r.AtCap {
			capped = true
			break
		}
	}
	if !capped {
		t.Fatal("failures of new names are never held back")
	}
}
