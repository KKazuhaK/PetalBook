package forge_test

import (
	"context"
	"net/url"
	"sync"
	"testing"
	"time"

	"petalbook.app/server/internal/connections"
	"petalbook.app/server/internal/forge"
	"petalbook.app/server/internal/forge/forgetest"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// The one token-refresh loop (consolidation 5b): driven by the next renewal time
// in the database and by wake-ups, never by polling; a few links at a time; a
// failure it can survive is tried again later, one it cannot marks the link as
// needing the person's attention and tells them once. The clock is the tests'
// and so are the loop's sleeps.

// sleeps are the loop's timers in the test's hands: every time the loop goes to
// sleep it says for how long (asked), and sleeps until the test fires the timer.
type sleeps struct {
	asked chan sleep
}

type sleep struct {
	d    time.Duration
	fire chan time.Time
}

func newSleeps() *sleeps { return &sleeps{asked: make(chan sleep, 100)} }

func (s *sleeps) after(d time.Duration) (<-chan time.Time, func()) {
	c := make(chan time.Time, 1)
	s.asked <- sleep{d, c}
	return c, func() {}
}

// next waits for the loop to go to sleep.
func (s *sleeps) next(t *testing.T) sleep {
	t.Helper()
	select {
	case sl := <-s.asked:
		return sl
	case <-time.After(10 * time.Second):
		t.Fatal("the loop did not go to sleep")
		return sleep{}
	}
}

// noMore says the loop did not go to sleep on a timer (it waits to be woken).
func (s *sleeps) none(t *testing.T) {
	t.Helper()
	select {
	case sl := <-s.asked:
		t.Fatalf("the loop sleeps for %v although nothing is due ever", sl.d)
	case <-time.After(50 * time.Millisecond):
	}
}

// runLoop starts the token service's loop; the returned function stops it and waits.
func runLoop(h *harness) (stop func()) {
	c, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { h.mgr.Tokens().Run(c); close(done) }()
	return func() { cancel(); <-done }
}

// ending is when a link's refresh token ends and when the loop renews it.
func linkTimes(t *testing.T, h *harness, user string) (refreshEnds, renewAt time.Time) {
	t.Helper()
	l, err := store.GetRepoLink(ctx, h.st.Q(), "main", user)
	if err != nil {
		t.Fatal(err)
	}
	return l.RefreshExpires, l.RenewAt
}

func reauthNotes(t *testing.T, h *harness, user string) []store.Notification {
	t.Helper()
	rows, err := store.ListNotifications(ctx, h.st.Q(), user, store.DefaultTenantID, store.NotifyFilter{Kind: store.NotifyLinkReauth, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestTheLoopRenewsBeforeTheRefreshTokenEndsAndSleepsUntilTheNextOne(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		sl := newSleeps()
		h := newHarnessWith(t, st, "gitea", sl.after)
		p := h.add("main")
		h.user("U1", "tang")
		h.f.AddUser("tang-h")
		stop := runLoop(h)
		defer stop()
		sl.none(t) // nobody is connected: it waits to be woken, not on a timer
		h.connect(p, "U1", "tang-h")
		ends, renewAt := linkTimes(t, h, "U1")
		// The refresh token lives 730 hours; it is renewed in the last tenth.
		if want := ends.Add(-73 * time.Hour); renewAt.Sub(want).Abs() > time.Minute {
			t.Fatalf("renewal at %v, want about %v", renewAt, want)
		}
		first := sl.next(t)
		if first.d != renewAt.Sub(h.clk.Now()) {
			t.Fatalf("the loop sleeps %v, the link is due in %v", first.d, renewAt.Sub(h.clk.Now()))
		}
		oldRefresh := h.f.RefreshTokenOf("tang-h")
		before := h.f.Count("POST", "/")
		h.clk.Advance(first.d)
		first.fire <- h.clk.Now()
		second := sl.next(t)
		if n := h.f.Count("POST", "/") - before; n != 1 || h.f.RefreshTokenOf("tang-h") == oldRefresh {
			t.Fatalf("%d requests, refresh token rotated: %v", n, h.f.RefreshTokenOf("tang-h") != oldRefresh)
		}
		// It has all its life ahead again: the next sleep is weeks, and no request is made meanwhile.
		if second.d < 600*time.Hour {
			t.Fatalf("the next sleep is %v", second.d)
		}
		if l, _ := store.GetRepoLink(ctx, st.Q(), "main", "U1"); l.State != "ok" || l.Version != 2 {
			t.Fatalf("%+v", l)
		}
		h.mustNotLeak()
	})
}

func TestOneLoopRenewsTheLinksOfBothKindsDueInTheSamePass(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		sl := newSleeps()
		h := newHarnessWith(t, st, "gitea", sl.after) // a Gitea provider "main"
		pea := h.add("main")
		lab := forgetest.NewGitLab(t, h.clk)
		lab.RefreshTTL = 730 * time.Hour
		pab, _, err := h.mgr.CreateProvider(ctx, "", h.bodyFor(lab, "labhost", map[string]any{"type": "gitlab"}))
		if err != nil {
			t.Fatal(err)
		}
		h.user("U1", "tang")
		h.user("U2", "kim")
		h.f.AddUser("tang-h")
		lab.AddUser("kim-l")
		h.connect(pea, "U1", "tang-h")
		stGL, err := pab.StartConnect(ctx, "U2", connections.StartRequest{Return: "/"})
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(lab.Authorize(stGL.URL, "kim-l"))
		if to, _ := pab.CompleteConnect(ctx, stGL.Binding, "U2", u.Query()); to != "/" {
			t.Fatalf("%q", to)
		}
		// GitLab's refresh tokens do not end by themselves; give this one a life, as a
		// host that ends them would, so that the two kinds are due together.
		now := h.clk.Now()
		if _, err := st.Q().Exec(ctx, `UPDATE linked_accounts SET refresh_expires = ?, renew_at = ? WHERE provider_id = 'labhost'`,
			now.Add(730*time.Hour).UnixMilli(), now.Add(657*time.Hour).UnixMilli()); err != nil {
			t.Fatal(err)
		}
		stop := runLoop(h)
		defer stop()
		first := sl.next(t)
		h.clk.Advance(first.d + time.Hour) // both are due now
		beforeEa, beforeLab := h.f.Count("POST", "/"), lab.Count("POST", "/")
		first.fire <- h.clk.Now()
		sl.next(t)
		if h.f.Count("POST", "/")-beforeEa != 1 || lab.Count("POST", "/")-beforeLab != 1 {
			t.Fatalf("one pass made %d and %d refreshes", h.f.Count("POST", "/")-beforeEa, lab.Count("POST", "/")-beforeLab)
		}
		for _, c := range []struct{ prov, user string }{{"main", "U1"}, {"labhost", "U2"}} {
			if l, err := store.GetRepoLink(ctx, st.Q(), c.prov, c.user); err != nil || l.Version != 2 || l.State != "ok" {
				t.Fatalf("%s: %+v %v", c.prov, l, err)
			}
		}
	})
}

func TestAWokenLoopSleepsUntilTheSoonerOfTwoLinks(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		sl := newSleeps()
		h := newHarnessWith(t, st, "gitea", sl.after)
		p := h.add("main")
		h.user("U1", "tang")
		h.user("U2", "kim")
		h.f.AddUser("tang-h")
		h.f.AddUser("kim-h")
		stop := runLoop(h)
		defer stop()
		h.connect(p, "U1", "tang-h")
		first := sl.next(t)
		// A link made later has a later end; making it wakes the loop, which sleeps for the sooner one still.
		h.clk.Advance(48 * time.Hour)
		h.connect(p, "U2", "kim-h")
		again := sl.next(t)
		if want := first.d - 48*time.Hour; again.d != want {
			t.Fatalf("slept %v, want %v (the first link's)", again.d, want)
		}
	})
}

func TestARefreshThatTheHostRefusesForGoodNeedsReauthorisationAndTellsThePersonOnce(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		h.user("U1", "tang")
		h.f.AddUser("tang-h")
		h.connect(p, "U1", "tang-h")
		if h.kind == "gitlab" { // its refresh tokens do not end: give this one a life, as a host that ends them would
			if _, err := h.st.Q().Exec(ctx, `UPDATE linked_accounts SET refresh_expires = ?, renew_at = ? WHERE provider_id = 'main'`,
				h.now.Add(730*time.Hour).UnixMilli(), h.now.Add(657*time.Hour).UnixMilli()); err != nil {
				t.Fatal(err)
			}
		}
		h.f.RevokeUser("tang-h")
		h.clk.Advance(658 * time.Hour)
		// The loop and callers that need the token meet the failure together: one marks it, one tells.
		var wg sync.WaitGroup
		for i := 0; i < 4; i++ {
			wg.Add(2)
			go func() { defer wg.Done(); h.mgr.Tokens().RenewDue(ctx) }()
			go func() { defer wg.Done(); _, _ = p.UserToken(ctx, "U1") }()
		}
		wg.Wait()
		l, err := store.GetRepoLink(ctx, h.st.Q(), "main", "U1")
		if err != nil || l.State != store.LinkExpired || l.AccessEnc != "" || l.RefreshEnc != "" || !l.RenewAt.IsZero() {
			t.Fatalf("%+v %v", l, err)
		}
		if in, _ := p.Info(ctx, "U1"); in.Connection.State != "expired" {
			t.Fatalf("%+v", in.Connection)
		}
		notes := reauthNotes(t, h, "U1")
		if len(notes) != 1 || notes[0].Ref != "main" {
			t.Fatalf("%d notifications: %+v", len(notes), notes)
		}
		if want := `{"login":"tang-h","name":"Test main","provider":"main"}`; notes[0].Payload != want {
			t.Fatalf("payload %s", notes[0].Payload)
		}
		// Later passes and calls do not tell again, and ask the host nothing.
		before := h.f.Count("POST", "/")
		h.mgr.Tokens().RenewDue(ctx)
		if _, err := p.UserToken(ctx, "U1"); err == nil {
			t.Fatal("a token for an expired link")
		}
		if n := len(reauthNotes(t, h, "U1")); n != 1 || h.f.Count("POST", "/") != before {
			t.Fatalf("%d notifications, %d requests", n, h.f.Count("POST", "/")-before)
		}
		// Authorising again mends it, and a later failure is told again.
		h.connect(p, "U1", "tang-h")
		if in, _ := p.Info(ctx, "U1"); in.Connection.State != "ok" {
			t.Fatalf("%+v", in.Connection)
		}
	})
}

func TestAHostThatIsDownIsTriedAgainWithGrowingWaitsAndNobodyIsTold(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		sl := newSleeps()
		h := newHarnessWith(t, st, "gitea", sl.after)
		p := h.add("main")
		h.user("U1", "tang")
		h.f.AddUser("tang-h")
		h.connect(p, "U1", "tang-h")
		_, renewAt := linkTimes(t, h, "U1")
		stop := runLoop(h)
		defer stop()
		first := sl.next(t)
		h.f.SetDown(true)
		h.clk.Advance(renewAt.Sub(h.clk.Now()))
		first.fire <- h.clk.Now()
		// It is due and the host is down: it waits five minutes, then ten, then twenty.
		for _, want := range []time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute} {
			s := sl.next(t)
			if s.d != want {
				t.Fatalf("waits %v, want %v", s.d, want)
			}
			if want == 20*time.Minute {
				h.f.SetDown(false) // the host is back for the next try
			}
			h.clk.Advance(s.d)
			s.fire <- h.clk.Now()
		}
		// The next try renews; nothing was marked and nobody was told.
		after := sl.next(t)
		if after.d < 600*time.Hour {
			t.Fatalf("slept %v after the renewal", after.d)
		}
		if l, _ := store.GetRepoLink(ctx, st.Q(), "main", "U1"); l.State != "ok" || l.Version != 2 {
			t.Fatalf("%+v", l)
		}
		if n := len(reauthNotes(t, h, "U1")); n != 0 {
			t.Fatalf("%d notifications for a host that was only down", n)
		}
	})
}

func TestAnExpiringLinkIsRenewedByOneRequestHoweverManyAsk(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		h.user("U1", "tang")
		h.f.AddUser("tang-h")
		h.connect(p, "U1", "tang-h")
		if h.kind == "gitlab" {
			if _, err := h.st.Q().Exec(ctx, `UPDATE linked_accounts SET refresh_expires = ?, renew_at = ? WHERE provider_id = 'main'`,
				h.now.Add(730*time.Hour).UnixMilli(), h.now.Add(657*time.Hour).UnixMilli()); err != nil {
				t.Fatal(err)
			}
		}
		h.clk.Advance(660 * time.Hour) // the access token ended long ago and the refresh token is due
		before := h.f.Count("POST", "/")
		var wg sync.WaitGroup
		got := make([]string, 6)
		for i := range got {
			wg.Add(2)
			go func() { defer wg.Done(); h.mgr.Tokens().RenewDue(ctx) }()
			go func() { defer wg.Done(); got[i], _ = p.UserToken(ctx, "U1") }()
		}
		wg.Wait()
		if n := h.f.Count("POST", "/") - before; n != 1 {
			t.Fatalf("%d refreshes for the loop and %d callers", n, len(got))
		}
		for _, g := range got {
			if g == "" || g != got[0] {
				t.Fatalf("%q, %q", g, got[0])
			}
		}
	})
}

func TestUnlinkingStopsTheRenewal(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		h.user("U1", "tang")
		h.f.AddUser("tang-h")
		h.connect(p, "U1", "tang-h")
		if h.kind == "gitlab" {
			if _, err := h.st.Q().Exec(ctx, `UPDATE linked_accounts SET refresh_expires = ?, renew_at = ? WHERE provider_id = 'main'`,
				h.now.Add(730*time.Hour).UnixMilli(), h.now.Add(657*time.Hour).UnixMilli()); err != nil {
				t.Fatal(err)
			}
		}
		if _, ok, err := store.NextLinkRenewal(ctx, h.st.Q(), h.clk.Now()); err != nil || !ok {
			t.Fatalf("a link to renew: %v %v", ok, err)
		}
		if err := p.Disconnect(ctx, "U1"); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := store.NextLinkRenewal(ctx, h.st.Q(), h.clk.Now()); err != nil || ok {
			t.Fatalf("a link to renew after unlinking: %v %v", ok, err)
		}
		before := h.f.Count("POST", "/")
		h.clk.Advance(700 * time.Hour)
		if n := h.mgr.Tokens().RenewDue(ctx); n != 0 || h.f.Count("POST", "/") != before {
			t.Fatalf("%d renewed, %d requests after unlinking", n, h.f.Count("POST", "/")-before)
		}
	})
}

func TestASpaceSyncingThroughALinkKeepsWorkingAfterTheLoopRenewedIt(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		h.user("U1", "tang")
		h.f.AddUser("tang-h")
		h.connect(p, "U1", "tang-h")
		h.space("S1", "main", "U1", map[string]string{"U1": "owner"})
		if h.kind == "gitlab" {
			if _, err := h.st.Q().Exec(ctx, `UPDATE linked_accounts SET refresh_expires = ?, renew_at = ? WHERE provider_id = 'main'`,
				h.now.Add(730*time.Hour).UnixMilli(), h.now.Add(657*time.Hour).UnixMilli()); err != nil {
				t.Fatal(err)
			}
		}
		old, err := p.GitToken(ctx, h.remote("S1"), false)
		if err != nil {
			t.Fatal(err)
		}
		h.clk.Advance(658 * time.Hour)
		if n := h.mgr.Tokens().RenewDue(ctx); n != 1 {
			t.Fatalf("%d renewed", n)
		}
		before := h.f.Count("POST", "/")
		tok, err := p.GitToken(ctx, h.remote("S1"), false)
		if err != nil || tok == old {
			t.Fatalf("%q after the renewal, %q before: %v", tok, old, err)
		}
		if h.f.Count("POST", "/") != before {
			t.Fatal("the renewed token was asked for again")
		}
		if st, err := forgeUser(h, tok); err != nil {
			t.Fatalf("the host does not take the renewed token: %d %v", st, err)
		}
	})
}
