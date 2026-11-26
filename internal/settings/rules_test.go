package settings_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// clock is a clock a test moves by hand, with the timers it would have started.
type clock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*timer
}

type timer struct {
	at      time.Time
	f       func()
	stopped bool
}

func (t *timer) Stop() bool { s := !t.stopped; t.stopped = true; return s }

func newClock() *clock { return &clock{now: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

func (c *clock) after(d time.Duration, f func()) interface{ Stop() bool } {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &timer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return t
}

// advance moves the clock and runs the timers that are due.
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []*timer
	for _, t := range c.timers {
		if !t.stopped && !t.at.After(c.now) {
			t.stopped = true
			due = append(due, t)
		}
	}
	c.mu.Unlock()
	for _, t := range due {
		t.f()
	}
}

func (c *clock) armed() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.timers {
		if !t.stopped {
			n++
		}
	}
	return n
}

func withClock(t *testing.T, st *store.Store) (*settings.Service, *clock) {
	t.Helper()
	s := setup(t, st)
	c := newClock()
	s.SetClock(c.Now)
	s.SetAfter(c.after)
	return s, c
}

func spaces(t *testing.T, s *settings.Service, tenant string) any {
	t.Helper()
	e, err := s.Get(ctx, tenant, settings.QuotaSpaces)
	if err != nil {
		t.Fatal(err)
	}
	return e.Value
}

func quotaRule(v any, from, until time.Time) *settings.Rule {
	return &settings.Rule{Value: v, From: from, Until: until}
}

func setManual(t *testing.T, s *settings.Service, r *settings.Rule) settings.TenantResult {
	t.Helper()
	res, err := s.SetTenantResult(ctx, ten, settings.QuotaSpaces, settings.Change{Site: true, HasSiteRule: true, SiteRule: r, By: "U1"})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestARuleOfTheStrongerSourceWins(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s, _ := withClock(t, st)
		if err := st.CreateTenant(ctx, store.Tenant{ID: "T000000003", Slug: "other", Name: "Other", Created: time.Now(), Updated: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if err := store.InsertPlan(ctx, st.Q(), store.Plan{ID: "P1", Slug: "team", Name: "Team"}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetPlanRules(ctx, "P1", "U1", map[string]*settings.Rule{settings.QuotaSpaces: {Value: int64(30)}}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.SetTenantPlan(ctx, ten, "P1"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.SetTenantPlan(ctx, "T000000003", "P1"); err != nil {
			t.Fatal(err)
		}
		if v := spaces(t, s, ten); v != int64(30) {
			t.Fatalf("the plan: %v", v)
		}
		if err := s.SetBilling(ctx, ten, "billing", map[string]*settings.Rule{settings.QuotaSpaces: {Value: int64(20)}}); err != nil {
			t.Fatal(err)
		}
		if v := spaces(t, s, ten); v != int64(20) {
			t.Fatalf("billing over the plan: %v", v)
		}
		setManual(t, s, quotaRule(int64(10), time.Time{}, time.Time{}))
		e, _ := s.Get(ctx, ten, settings.QuotaSpaces)
		if e.Value != int64(10) || len(e.Tenant.Rules) != 3 {
			t.Fatalf("manual over billing: %v %+v", e.Value, e.Tenant.Rules)
		}
		var got []string
		for _, r := range e.Tenant.Rules {
			got = append(got, string(r.Source))
			if r.InEffect != (r.Source == settings.FromManual) || r.Underlying != (r.Source == settings.FromBilling) {
				t.Errorf("%s: inEffect %v underlying %v", r.Source, r.InEffect, r.Underlying)
			}
		}
		if got[0] != "manual" || got[1] != "billing" || got[2] != "plan" {
			t.Fatalf("order %v", got)
		}
		// The other tenant on the plan is untouched, and shares the plan's map.
		if v := spaces(t, s, "T000000003"); v != int64(30) {
			t.Fatalf("the other tenant: %v", v)
		}
		// Removing the manual rule falls back to billing, then the plan.
		setManual(t, s, nil)
		if v := spaces(t, s, ten); v != int64(20) {
			t.Fatalf("after the manual rule: %v", v)
		}
		if err := s.SetBilling(ctx, ten, "billing", map[string]*settings.Rule{settings.QuotaSpaces: nil}); err != nil {
			t.Fatal(err)
		}
		if v := spaces(t, s, ten); v != int64(30) {
			t.Fatalf("after billing: %v", v)
		}
	})
}

func TestAWindowStartsAndEndsWithTheClockAndNoTimer(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s, c := withClock(t, st)
		computed := 0
		s.SetOnCompute(func(tenant string) {
			if tenant == ten {
				computed++
			}
		})
		start := c.Now().Add(time.Hour)
		end := c.Now().Add(2 * time.Hour)
		setManual(t, s, quotaRule(int64(5), start, end))
		if v := spaces(t, s, ten); v != nil {
			t.Fatalf("before the window: %v", v)
		}
		e, _ := s.Get(ctx, ten, settings.QuotaSpaces)
		if e.Tenant.SiteRule != nil || len(e.Tenant.Rules) != 1 || !e.Tenant.Next.Equal(start) {
			t.Fatalf("a rule that has not begun is listed, not in effect, and next is its start: %+v", e.Tenant)
		}
		before := computed
		spaces(t, s, ten)
		spaces(t, s, ten)
		if computed != before {
			t.Fatal("a warm read computed again")
		}
		c.advance(time.Hour - time.Millisecond)
		if v := spaces(t, s, ten); v != nil {
			t.Fatalf("a millisecond before: %v", v)
		}

		c.advance(time.Millisecond)
		if v := spaces(t, s, ten); v != int64(5) {
			t.Fatalf("at the start: %v", v)
		}
		if computed != before+1 {
			t.Fatalf("the read at the start computed %d times, want once", computed-before)
		}
		e, _ = s.Get(ctx, ten, settings.QuotaSpaces)
		if !e.Tenant.Next.Equal(end) || !e.Tenant.SiteRule.InEffect {
			t.Fatalf("%+v", e.Tenant)
		}
		c.advance(time.Hour - time.Millisecond)
		if v := spaces(t, s, ten); v != int64(5) {
			t.Fatalf("a millisecond before the end: %v", v)
		}
		c.advance(time.Millisecond)
		if v := spaces(t, s, ten); v != nil {
			t.Fatalf("at the end (the end is not in the window): %v", v)
		}
		// The other tenants never computed again for this.
		if e, _ := s.Get(ctx, store.DefaultTenantID, settings.QuotaSpaces); e.Value != nil {
			t.Fatal("the default tenant")
		}
	})
}

func TestADatedRuleReplacesAnUndatedOneAndSaysSo(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s, c := withClock(t, st)
		if res := setManual(t, s, quotaRule(int64(7), time.Time{}, time.Time{})); res.UndatedReplaced {
			t.Fatal("nothing was replaced")
		}
		if res := setManual(t, s, quotaRule(int64(9), time.Time{}, c.Now().Add(time.Hour))); !res.UndatedReplaced {
			t.Fatal("the undated rule was replaced")
		}
		c.advance(time.Hour)
		if v := spaces(t, s, ten); v != nil {
			t.Fatalf("it does not return to the old manual rule: %v", v)
		}
		if res := setManual(t, s, quotaRule(int64(1), time.Time{}, c.Now().Add(-time.Hour))); res.UndatedReplaced {
			t.Fatal("a dated rule was replaced by a dated one")
		}
		if _, err := s.SetTenantResult(ctx, ten, settings.QuotaSpaces, settings.Change{Site: true, HasSiteRule: true,
			SiteRule: quotaRule(int64(1), c.Now().Add(time.Hour), c.Now())}); code(err) != settings.CodeInvalid {
			t.Fatalf("until before from: %v", err)
		}
	})
}

func TestTheSitesLockBeatsAManualRuleAndTheRuleComesBack(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s, _ := withClock(t, st)
		setManual(t, s, quotaRule(int64(5), time.Time{}, time.Time{}))
		if _, err := s.SetSite(ctx, settings.QuotaSpaces, settings.Change{HasValue: true, Value: int64(50), HasLower: true, Lower: lower("lock")}); err != nil {
			t.Fatal(err)
		}
		e, _ := s.Get(ctx, ten, settings.QuotaSpaces)
		if e.Value != int64(50) || e.Tenant.SiteRule.InEffect {
			t.Fatalf("%v %+v", e.Value, e.Tenant.SiteRule)
		}
		if _, err := s.SetSite(ctx, settings.QuotaSpaces, settings.Change{HasLower: true, Lower: lower("allow")}); err != nil {
			t.Fatal(err)
		}
		if v := spaces(t, s, ten); v != int64(5) {
			t.Fatalf("%v", v)
		}
	})
}

func TestARestrictionClampsAndAValueAboveItsOwnMaxIsRefused(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s, _ := withClock(t, st)
		if err := store.InsertPlan(ctx, st.Q(), store.Plan{ID: "P1", Slug: "small", Name: "Small"}); err != nil {
			t.Fatal(err)
		}
		max10 := &settings.Lower{Mode: settings.Restrict, Max: ptr(int64(10))}
		if err := s.SetPlanRules(ctx, "P1", "U1", map[string]*settings.Rule{settings.QuotaSpaces: {Value: int64(50), Lower: max10}}); code(err) != settings.CodeAboveMax {
			t.Fatalf("a plan's rule above its own max: %v", err)
		}
		if err := s.SetPlanRules(ctx, "P1", "U1", map[string]*settings.Rule{settings.QuotaSpaces: {Lower: max10}}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.SetTenantPlan(ctx, ten, "P1"); err != nil {
			t.Fatal(err)
		}
		// No limit is above any max: max 10 means max 10.
		if v := spaces(t, s, ten); v != int64(10) {
			t.Fatalf("no limit under a restriction: %v", v)
		}
		if err := s.SetTenant(ctx, ten, settings.QuotaSpaces, settings.Change{HasValue: true, Value: int64(50)}); code(err) != settings.CodeRestricted {
			t.Fatalf("a tenant above the plan's max: %v", err)
		}
		if err := s.SetTenant(ctx, ten, settings.QuotaSpaces, settings.Change{HasValue: true, Value: int64(4)}); err != nil {
			t.Fatal(err)
		}
		if v := spaces(t, s, ten); v != int64(4) {
			t.Fatalf("a tenant under it: %v", v)
		}
		if err := s.SetPlanRules(ctx, "P1", "U1", map[string]*settings.Rule{settings.QuotaSpaces: {Lower: max10, From: time.Now()}}); code(err) != settings.CodeInvalid {
			t.Fatalf("a plan's rule has no dates: %v", err)
		}
	})
}

func TestAPlanWriteIsSeenThroughItsGenerationAndNoOtherTenantComputesTwice(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s, _ := withClock(t, st)
		if err := store.InsertPlan(ctx, st.Q(), store.Plan{ID: "P1", Slug: "team", Name: "Team"}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.SetTenantPlan(ctx, ten, "P1"); err != nil {
			t.Fatal(err)
		}
		spaces(t, s, ten)
		n := 0
		s.SetOnCompute(func(string) { n++ })
		spaces(t, s, ten)
		if n != 0 {
			t.Fatal("warm")
		}
		if err := s.SetPlanRules(ctx, "P1", "U1", map[string]*settings.Rule{settings.QuotaSpaces: {Value: int64(3)}}); err != nil {
			t.Fatal(err)
		}
		if v := spaces(t, s, ten); v != int64(3) || n != 1 {
			t.Fatalf("%v after %d computations", v, n)
		}
	})
}

func TestTheBoundaryTimerFiresOncePerBoundaryAndNothingIsArmedWithoutWindows(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s, c := withClock(t, st)
		var got []settings.Boundary
		s.Watch(ctx, func(_ context.Context, b []settings.Boundary) { got = append(got, b...) })
		if c.armed() != 0 {
			t.Fatal("a timer with nothing dated")
		}
		start, end := c.Now().Add(time.Hour), c.Now().Add(2*time.Hour)
		setManual(t, s, quotaRule(int64(5), start, end))
		if c.armed() != 1 {
			t.Fatalf("armed %d", c.armed())
		}
		c.advance(time.Hour)
		if len(got) != 1 || !got[0].Started || got[0].Tenant != ten || got[0].Key != settings.QuotaSpaces {
			t.Fatalf("%+v", got)
		}
		c.advance(time.Hour)
		if len(got) != 2 || got[1].Started {
			t.Fatalf("%+v", got)
		}
		c.advance(time.Hour)
		if len(got) != 2 || c.armed() != 0 {
			t.Fatalf("nothing more: %+v, armed %d", got, c.armed())
		}
	})
}

func TestAChangeByAnotherProcessIsSeenAfterTheRevisionCheck(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		a, c := withClock(t, st)
		b := settings.New(st) // another process on the same database
		b.SetClock(c.Now)
		if v := spaces(t, b, ten); v != nil {
			t.Fatal(v)
		}
		setManual(t, a, quotaRule(int64(8), time.Time{}, time.Time{}))
		if v := spaces(t, b, ten); v != nil {
			t.Fatalf("seen before the check is due: %v", v)
		}
		c.advance(settings.RevCheck)
		if v := spaces(t, b, ten); v != int64(8) {
			t.Fatalf("after the check: %v", v)
		}
		// Its own writes are not taken for another process's.
		n := 0
		a.SetOnCompute(func(string) { n++ })
		spaces(t, a, ten)
		c.advance(settings.RevCheck)
		spaces(t, a, ten)
		if n > 2 {
			t.Fatalf("computed %d times", n)
		}
	})
}

func TestAPlanChangeClearsTheUndatedManualRulesAndKeepsTheDatedGrants(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s, c := withClock(t, st)
		if err := store.InsertPlan(ctx, st.Q(), store.Plan{ID: "P1", Slug: "team", Name: "Team"}); err != nil {
			t.Fatal(err)
		}
		setManual(t, s, quotaRule(int64(5), time.Time{}, time.Time{}))
		if _, err := s.SetTenantResult(ctx, ten, settings.QuotaBytes, settings.Change{Site: true, HasSiteRule: true,
			SiteRule: quotaRule(int64(1000), time.Time{}, c.Now().Add(24*time.Hour))}); err != nil {
			t.Fatal(err)
		}
		cleared, kept, err := s.SetTenantPlan(ctx, ten, "P1")
		if err != nil {
			t.Fatal(err)
		}
		if len(cleared) != 1 || cleared[0].Key != settings.QuotaSpaces || len(kept) != 1 || kept[0].Key != settings.QuotaBytes {
			t.Fatalf("cleared %+v kept %+v", cleared, kept)
		}
		if v := spaces(t, s, ten); v != nil {
			t.Fatalf("the undated rule is gone: %v", v)
		}
		if e, _ := s.Get(ctx, ten, settings.QuotaBytes); e.Value != int64(1000) {
			t.Fatalf("the grant is kept: %v", e.Value)
		}
	})
}

func TestAFeatureIsOnUnlessARuleTurnsItOffForOneTenant(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s, _ := withClock(t, st)
		if !s.On(ctx, ten, settings.FeatureAdminView) || s.On(ctx, ten, settings.MailOwnSender) {
			t.Fatal("defaults: adminView on, mail.ownSender off")
		}
		if err := s.SetTenant(ctx, ten, settings.FeatureAdminView, settings.Change{Site: true, HasSiteLock: true, SiteLock: true, HasValue: true, Value: false}); err != nil {
			t.Fatal(err)
		}
		if s.On(ctx, ten, settings.FeatureAdminView) || !s.On(ctx, store.DefaultTenantID, settings.FeatureAdminView) {
			t.Fatal("off for the one tenant only")
		}
	})
}

// A warm read is a cached lookup: no query and no allocation, with or without a
// window ahead (the comparison with the next boundary is the only addition).
func BenchmarkForWarm(b *testing.B) {
	for _, dated := range []bool{false, true} {
		b.Run(fmt.Sprintf("dated=%v", dated), func(b *testing.B) {
			st := storetest.Open(b, "sqlite")
			s := settings.New(st)
			now := time.Now()
			if err := st.CreateTenant(ctx, store.Tenant{ID: ten, Slug: "acme", Name: "Acme", Created: now, Updated: now}); err != nil {
				b.Fatal(err)
			}
			if dated {
				r := &settings.Rule{Value: int64(5), From: now.Add(time.Hour), Until: now.Add(2 * time.Hour)}
				if err := s.SetTenant(ctx, ten, settings.QuotaSpaces, settings.Change{Site: true, HasSiteRule: true, SiteRule: r}); err != nil {
					b.Fatal(err)
				}
			}
			if _, err := s.For(ctx, ten); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				m, _ := s.For(ctx, ten)
				_ = m[settings.QuotaSpaces]
			}
		})
	}
}
