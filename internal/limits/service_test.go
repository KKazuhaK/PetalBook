package limits

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

var ctx = context.Background()

const tenB = "T000000002"

var t0 = time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)

// fakeClock moves only when a test says so; the timers it holds fire in
// Advance, in the order of their times.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at       time.Time
	f        func()
	done, on bool
}

func (t *fakeTimer) Stop() bool { was := !t.done; t.done = true; return was }

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) AfterFunc(d time.Duration, f func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return t
}

// pending are the times the timers that have neither fired nor been stopped wait for.
func (c *fakeClock) pending() []time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []time.Time
	for _, t := range c.timers {
		if !t.done {
			out = append(out, t.at)
		}
	}
	return out
}

// advanceTo moves the clock to at, firing the timers that fall due on the way
// as the real one would, each at its own time.
func (c *fakeClock) advanceTo(at time.Time) {
	for {
		c.mu.Lock()
		var next *fakeTimer
		for _, t := range c.timers {
			if !t.done && !t.at.After(at) && (next == nil || t.at.Before(next.at)) {
				next = t
			}
		}
		if next == nil {
			c.now = at
			c.mu.Unlock()
			return
		}
		next.done = true
		c.now = next.at
		c.mu.Unlock()
		next.f()
	}
}

type invalidations struct{ n int }

func (i *invalidations) Invalidate() { i.n++ }

type fixture struct {
	st   *store.Store
	clk  *fakeClock
	inv  *invalidations
	svc  *Service
	seen []string
}

func newFixture(t *testing.T, st *store.Store) *fixture {
	t.Helper()
	now := t0.Add(-time.Hour)
	if err := st.CreateTenant(ctx, store.Tenant{ID: tenB, Slug: "acme", Name: "Acme", Created: now, Updated: now}); err != nil {
		t.Fatal(err)
	}
	f := &fixture{st: st, clk: &fakeClock{now: t0}, inv: &invalidations{}}
	svc, err := New(ctx, Config{Store: st, Clock: f.clk, Invalidate: f.inv})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	svc.recomputed = func(tenant string) { f.seen = append(f.seen, tenant) }
	f.svc = svc
	return f
}

func each(t *testing.T, fn func(t *testing.T, f *fixture)) {
	t.Helper()
	storetest.Each(t, func(t *testing.T, st *store.Store) { fn(t, newFixture(t, st)) })
}

func raw(s string) *Change { return &Change{Value: json.RawMessage(s)} }

func window(s string, from, until time.Time) *Change {
	return &Change{Value: json.RawMessage(s), From: from, Until: until}
}

func (f *fixture) manual(tenant, key string, c *Change) { f.setAs(tenant, store.LimitManual, key, c) }

func (f *fixture) setAs(tenant, source, key string, c *Change) {
	if _, err := f.svc.set(ctx, tenant, "U1", source, map[string]*Change{key: c}); err != nil {
		panic(err)
	}
}

func quota(f *fixture, tenant string) (int64, bool, Source) {
	n, ok := f.svc.Sheet(tenant).Limit("storage.quota.bytes")
	_, src, _ := f.svc.Sheet(tenant).Value("storage.quota.bytes")
	return n, ok, src
}

func TestTheFirstPresentSourceWins(t *testing.T) {
	t.Parallel()
	each(t, func(t *testing.T, f *fixture) {
		q := f.st.Q()
		const key = "storage.quota.bytes"
		if _, _, src := quota(f, tenB); src != FromSite {
			t.Fatal(src)
		}
		// Site, over the built-in default (null).
		if _, err := store.PutSetting(ctx, q, store.SettingCapabilities, `{"storage.quota.bytes": 1000}`, t0); err != nil {
			t.Fatal(err)
		}
		// The plan.
		if err := store.InsertPlan(ctx, q, store.Plan{ID: "P1", Slug: "team", Name: "Team", Values: `{"storage.quota.bytes": 2000}`}); err != nil {
			t.Fatal(err)
		}
		if err := store.SetTenantPlan(ctx, q, tenB, "P1"); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.Reload(ctx); err != nil {
			t.Fatal(err)
		}
		if n, _, src := quota(f, tenB); n != 2000 || src != FromPlan {
			t.Fatalf("plan: %d %s", n, src)
		}
		if n, _, src := quota(f, store.DefaultTenantID); n != 1000 || src != FromSite {
			t.Fatalf("a tenant without a plan: %d %s", n, src)
		}
		f.setAs(tenB, store.LimitBilling, key, raw("3000"))
		if n, _, src := quota(f, tenB); n != 3000 || src != FromBilling {
			t.Fatalf("billing: %d %s", n, src)
		}
		f.manual(tenB, key, raw("4000"))
		if n, _, src := quota(f, tenB); n != 4000 || src != FromManual {
			t.Fatalf("manual: %d %s", n, src)
		}
		// A null is a value: manual "no limit" beats a billing number.
		f.manual(tenB, key, raw("null"))
		if _, ok, src := quota(f, tenB); ok || src != FromManual {
			t.Fatalf("manual null: %v %s", ok, src)
		}
		// Removing falls through to the next, in order.
		f.manual(tenB, key, nil)
		if n, _, src := quota(f, tenB); n != 3000 || src != FromBilling {
			t.Fatalf("%d %s", n, src)
		}
		f.setAs(tenB, store.LimitBilling, key, nil)
		if n, _, src := quota(f, tenB); n != 2000 || src != FromPlan {
			t.Fatalf("%d %s", n, src)
		}
		// What was written is what a new service reads.
		f.manual(tenB, key, raw("5000"))
		again, err := New(ctx, Config{Store: f.st, Clock: f.clk})
		if err != nil {
			t.Fatal(err)
		}
		defer again.Close()
		if n, _ := again.Sheet(tenB).Limit(key); n != 5000 {
			t.Fatal(n)
		}
	})
}

func TestAValueCountsOnlyInsideItsWindow(t *testing.T) {
	t.Parallel()
	each(t, func(t *testing.T, f *fixture) {
		from, until := t0.Add(time.Hour), t0.Add(2*time.Hour)
		f.manual(tenB, "storage.pinnedBytes", window("500", from, until))
		pinned := func() int64 { n, _ := f.svc.Sheet(tenB).Limit("storage.pinnedBytes"); return n }

		if got := pinned(); got != 0 {
			t.Fatalf("before the start: %d", got)
		}
		f.clk.advanceTo(from.Add(-time.Nanosecond))
		if got := pinned(); got != 0 {
			t.Fatalf("just before the start: %d", got)
		}
		f.clk.advanceTo(from) // the start itself is inside
		if got := pinned(); got != 500 {
			t.Fatalf("at the start: %d", got)
		}
		f.clk.advanceTo(until.Add(-time.Nanosecond))
		if got := pinned(); got != 500 {
			t.Fatalf("just before the end: %d", got)
		}
		f.clk.advanceTo(until) // the end itself is outside
		if got := pinned(); got != 0 {
			t.Fatalf("at the end: %d", got)
		}
		if _, src, _ := f.svc.Sheet(tenB).Value("storage.pinnedBytes"); src != FromSite {
			t.Fatal(src)
		}
		if p := f.clk.pending(); len(p) != 0 {
			t.Fatalf("nothing is left to wait for: %v", p)
		}

		// A write inside a window that began already counts at once, and a
		// window with only an end or only a start works too.
		f.manual(tenB, "storage.pinnedBytes", window("7", time.Time{}, until.Add(time.Hour)))
		if got := pinned(); got != 7 {
			t.Fatal(got)
		}
		f.manual(tenB, "storage.pinnedBytes", window("8", until.Add(time.Hour), time.Time{}))
		if got := pinned(); got != 0 {
			t.Fatal(got)
		}
		f.clk.advanceTo(until.Add(time.Hour))
		if got := pinned(); got != 8 {
			t.Fatal(got)
		}
	})
}

func TestAWriteRecomputesOnlyThatTenant(t *testing.T) {
	t.Parallel()
	each(t, func(t *testing.T, f *fixture) {
		f.manual(tenB, "signin.sso", raw("false"))
		if !slices.Equal(f.seen, []string{tenB}) {
			t.Fatalf("recomputed %v", f.seen)
		}
		if f.svc.Sheet(tenB).Bool("signin.sso") || !f.svc.Sheet(store.DefaultTenantID).Bool("signin.sso") {
			t.Fatal("the write reached the other tenant, or not this one")
		}
		if f.inv.n != 1 {
			t.Fatalf("the settings were told %d times", f.inv.n)
		}
		// The sheet a reader holds is not changed under it: it is replaced.
		before := f.svc.Sheet(store.DefaultTenantID)
		f.manual(tenB, "signin.sso", nil)
		if f.svc.Sheet(store.DefaultTenantID) != before {
			t.Fatal("another tenant's sheet was rebuilt")
		}
	})
}

func TestTheTimerFollowsTheNextBoundaryAcrossTenants(t *testing.T) {
	t.Parallel()
	each(t, func(t *testing.T, f *fixture) {
		if len(f.clk.pending()) != 0 {
			t.Fatal("a timer with nothing dated")
		}
		h := func(n int) time.Time { return t0.Add(time.Duration(n) * time.Hour) }
		f.manual(tenB, "other.people", window("5", h(3), h(5)))
		if p := f.clk.pending(); !slices.Equal(p, []time.Time{h(3)}) {
			t.Fatalf("%v", p)
		}
		// An earlier boundary of another tenant replaces the timer, never adds one.
		f.manual(store.DefaultTenantID, "other.people", window("9", h(1), h(4)))
		if p := f.clk.pending(); !slices.Equal(p, []time.Time{h(1)}) {
			t.Fatalf("%v", p)
		}
		f.seen, f.inv.n = nil, 0
		f.clk.advanceTo(h(1)) // only the default tenant's value starts
		if !slices.Equal(f.seen, []string{store.DefaultTenantID}) || f.inv.n != 1 {
			t.Fatalf("recomputed %v, told %d", f.seen, f.inv.n)
		}
		if p := f.clk.pending(); !slices.Equal(p, []time.Time{h(3)}) {
			t.Fatalf("%v", p)
		}
		// Removing the value the timer waits for moves it on.
		f.manual(tenB, "other.people", nil)
		if p := f.clk.pending(); !slices.Equal(p, []time.Time{h(4)}) {
			t.Fatalf("%v", p)
		}
		f.clk.advanceTo(h(4))
		if n, ok := f.svc.Sheet(store.DefaultTenantID).Limit("other.people"); ok {
			t.Fatalf("it ended: %d", n)
		}
		if p := f.clk.pending(); len(p) != 0 {
			t.Fatalf("%v", p)
		}
	})
}

func TestAWriteDoesNotLoseABoundaryThatIsDue(t *testing.T) {
	t.Parallel()
	each(t, func(t *testing.T, f *fixture) {
		f.manual(tenB, "other.people", window("5", t0.Add(time.Minute), time.Time{}))
		// The clock passes the start, but the timer has not run yet when a write
		// for another tenant stops it.
		f.clk.mu.Lock()
		f.clk.now = t0.Add(time.Minute)
		f.clk.mu.Unlock()
		f.manual(store.DefaultTenantID, "signin.sso", raw("false"))
		if n, _ := f.svc.Sheet(tenB).Limit("other.people"); n != 5 {
			t.Fatalf("the due start was forgotten: %d", n)
		}
	})
}

func TestValuesAreCheckedAgainstTheRegistry(t *testing.T) {
	t.Parallel()
	each(t, func(t *testing.T, f *fixture) {
		bad := map[string]*Change{
			"nope":                 raw("1"),
			"signin.sso":           raw("1"),
			"other.people":         raw("-1"),
			"storage.pinnedBytes":  raw("null"),
			"storage.modes":        raw(`["server","cloud"]`),
			"usage.mailPerDay":     raw("1.5"),
			"storage.quota.bytes":  window("1", t0, t0),
			"storage.maxFileBytes": raw(`"big"`),
		}
		for k, c := range bad {
			var ve *ValueError
			if _, err := f.svc.SetManual(ctx, tenB, "U1", map[string]*Change{"signin.domains": raw("false"), k: c}); !errors.As(err, &ve) {
				t.Fatalf("%s: %v", k, err)
			}
		}
		if !f.svc.Sheet(tenB).Bool("signin.domains") || f.inv.n != 0 {
			t.Fatal("an invalid change wrote part of itself")
		}
		if _, err := f.svc.SetManual(ctx, "nobody", "U1", map[string]*Change{"signin.sso": raw("false")}); !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
		if _, err := f.svc.SetManual(ctx, tenB, "U1", map[string]*Change{"storage.modes": raw(`["own","server"]`)}); err != nil {
			t.Fatal(err)
		}
		if got := f.svc.Sheet(tenB).List("storage.modes"); !slices.Equal(got, []string{"own", "server"}) {
			t.Fatal(got)
		}
	})
}

func TestOnIsTrueForWhatIsNoCapability(t *testing.T) {
	t.Parallel()
	each(t, func(t *testing.T, f *fixture) {
		f.manual(tenB, "data.adminView", raw("false"))
		f.manual(tenB, "other.people", raw("0"))
		if f.svc.On(tenB, "data.adminView") || !f.svc.On(store.DefaultTenantID, "data.adminView") {
			t.Fatal("data.adminView")
		}
		for _, k := range []string{"", "nope", "other.people", "storage.modes"} {
			if !f.svc.On(tenB, k) {
				t.Fatalf("%q is no switch and stays on", k)
			}
		}
		if f.svc.On(tenB, "signin.idpAdmins") {
			t.Fatal("signin.idpAdmins is off by default")
		}
		// A tenant the service has not seen has the site's values.
		if !f.svc.On("unknown", "data.adminView") {
			t.Fatal("unknown tenant")
		}
	})
}
