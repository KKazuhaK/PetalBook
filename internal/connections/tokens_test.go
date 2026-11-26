package connections_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"petalbook.app/server/internal/connections"
	"petalbook.app/server/internal/connections/conntest"
	"petalbook.app/server/internal/secret"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// The one token service: its loop's schedule, the lease that keeps a refresh
// token from being handed to the host twice (by this process or another one on
// the same database), and what a renewal does when the link moved on under it.
// The clock, the loop's sleeps, the host and the failures of the database are
// the tests'.

var ctx = context.Background()

// clock is the tests' time.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// sleeps are the loop's timers in the test's hands: each time the loop goes to
// sleep it says for how long, and sleeps until the test fires the timer.
type sleeps struct{ asked chan sleep }

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

// next waits for the loop to go to sleep (the bound only ends a broken test).
func (s *sleeps) next(t *testing.T) sleep {
	t.Helper()
	select {
	case sl := <-s.asked:
		return sl
	case <-time.After(10 * time.Second):
		t.Fatal("the loop did not go to sleep on a timer")
		return sleep{}
	}
}

// host is a provider whose refresh tokens are single use, as Gitea's are: a
// refresh token handed in twice is refused for good. hook runs at the start of
// each refresh (the call's number, from 1) and may fail it.
type host struct {
	*conntest.Fake
	clk *clock

	mu      sync.Mutex
	calls   int
	n       int
	refresh map[string]bool // the refresh tokens that are good
	hook    func(call int) error
}

func newHost(id string, clk *clock) *host {
	return &host{Fake: conntest.NewFake(id), clk: clk, refresh: map[string]bool{}}
}

// issue makes a new pair: the access token lives an hour, the refresh token 30 days.
func (h *host) issue() connections.Pair {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.issueLocked()
}

func (h *host) issueLocked() connections.Pair {
	h.n++
	r := fmt.Sprintf("refresh-%d", h.n)
	h.refresh[r] = true
	now := h.clk.Now()
	return connections.Pair{Access: fmt.Sprintf("access-%d", h.n), Refresh: r, AccessExpires: now.Add(time.Hour), RefreshExpires: now.Add(30 * 24 * time.Hour)}
}

func (h *host) setHook(f func(call int) error) { h.mu.Lock(); h.hook = f; h.mu.Unlock() }

func (h *host) Calls() int { h.mu.Lock(); defer h.mu.Unlock(); return h.calls }

func (h *host) RefreshTokens(_ context.Context, refresh string) (*connections.Pair, error) {
	h.mu.Lock()
	h.calls++
	call, hook := h.calls, h.hook
	h.mu.Unlock()
	if hook != nil {
		if err := hook(call); err != nil {
			return nil, err
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.refresh[refresh] {
		return nil, connections.ErrRevoked
	}
	delete(h.refresh, refresh)
	p := h.issueLocked()
	return &p, nil
}

// env is one database, a clock, a host, and the token services of the
// processes that share them.
type env struct {
	t    *testing.T
	st   *store.Store
	clk  *clock
	host *host
	box  *secret.Box
}

const provider = "gt"

func newEnv(t *testing.T, st *store.Store) *env {
	t.Helper()
	clk := &clock{now: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)}
	e := &env{t: t, st: st, clk: clk, host: newHost(provider, clk), box: secret.NewBoxWithKey([32]byte{1, 2, 3})}
	if err := store.InsertConnProviderRow(ctx, st.Q(), store.ConnProviderRow{ID: provider, Type: "gitea", Host: "gt.example.com", Scheme: "https", Name: "Our Gitea",
		Owner: store.ConnProviderSite, State: "ok", Created: clk.Now(), Updated: clk.Now()}); err != nil {
		t.Fatal(err)
	}
	return e
}

// tokens is the token service of one process.
func (e *env) tokens(after func(time.Duration) (<-chan time.Time, func())) *connections.Tokens {
	return connections.NewTokens(connections.TokenOptions{Store: e.st, Secrets: e.box, Registry: connections.NewRegistry(e.host),
		Now: e.clk.Now, After: after})
}

func (e *env) user(id string) {
	e.t.Helper()
	if err := store.InsertUser(ctx, e.st.Q(), store.User{ID: id, Username: id, Name: id, Role: "member", Created: e.clk.Now()}); err != nil {
		e.t.Fatal(err)
	}
}

// connect links the user's account at the host with a new pair.
func (e *env) connect(tk *connections.Tokens, user string) connections.Pair {
	e.t.Helper()
	p := e.host.issue()
	if err := tk.Put(ctx, connections.Account{ProviderID: provider, UserID: user, Subject: "s-" + user, Login: user + "-h"}, p); err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *env) link(user string) *store.RepoLink {
	e.t.Helper()
	l, err := store.GetRepoLink(ctx, e.st.Q(), provider, user)
	if err != nil {
		e.t.Fatal(err)
	}
	return l
}

func (e *env) notes(user string) int {
	e.t.Helper()
	rows, err := store.ListNotifications(ctx, e.st.Q(), user, store.DefaultTenantID, store.NotifyFilter{Kind: store.NotifyLinkReauth, Limit: 10})
	if err != nil {
		e.t.Fatal(err)
	}
	return len(rows)
}

func runLoop(tk *connections.Tokens) (stop func()) {
	c, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { tk.Run(c); close(done) }()
	return func() { cancel(); <-done }
}

// --- the schedule ---------------------------------------------------------------------

func TestALinkThatCameDueDuringASlowPassIsRenewedInTheNextPass(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		e := newEnv(t, st)
		sl := newSleeps()
		tk := e.tokens(sl.after)
		e.user("U1")
		e.user("U2")
		e.connect(tk, "U1")
		e.clk.Advance(time.Minute)
		e.connect(tk, "U2") // due a minute after U1's
		first, second := e.link("U1").RenewAt, e.link("U2").RenewAt
		if second.Sub(first) != time.Minute {
			t.Fatalf("%v %v", first, second)
		}
		// The host takes three minutes to answer U1's renewal: U2 comes due meanwhile.
		e.host.setHook(func(call int) error {
			if call == 1 {
				e.clk.Advance(3 * time.Minute)
			}
			return nil
		})
		stop := runLoop(tk)
		defer stop()
		s := sl.next(t)
		e.clk.Advance(first.Sub(e.clk.Now()))
		s.fire <- e.clk.Now()
		// It goes on at once for U2, not at the next link stored or the next renewal weeks away.
		s = sl.next(t)
		if s.d != 0 {
			t.Fatalf("after the pass the loop sleeps %v; U2 has been due for %v", s.d, e.clk.Now().Sub(second))
		}
		if e.host.Calls() != 1 || e.link("U2").Version != 1 {
			t.Fatalf("%d calls, U2 at %d", e.host.Calls(), e.link("U2").Version)
		}
		s.fire <- e.clk.Now()
		s = sl.next(t)
		if e.host.Calls() != 2 || e.link("U1").Version != 2 || e.link("U2").Version != 2 {
			t.Fatalf("%d calls, versions %d %d", e.host.Calls(), e.link("U1").Version, e.link("U2").Version)
		}
		// Both have their lives ahead: the next sleep is weeks.
		if s.d < 20*24*time.Hour {
			t.Fatalf("then it sleeps %v", s.d)
		}
	})
}

func TestALoopThatCouldNotLookForDueLinksLooksAgainAfterTheBackoff(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		e := newEnv(t, st)
		sl := newSleeps()
		tk := e.tokens(sl.after)
		var mu sync.Mutex
		failures := 1
		connections.SetDueHook(tk, func() error {
			mu.Lock()
			defer mu.Unlock()
			if failures > 0 {
				failures--
				return errors.New("the database is away")
			}
			return nil
		})
		e.user("U1")
		e.connect(tk, "U1")
		e.clk.Advance(e.link("U1").RenewAt.Sub(e.clk.Now()) + time.Minute) // due when the loop starts
		stop := runLoop(tk)
		defer stop()
		// The first look fails: the loop tries again in five minutes, not when something else happens.
		s := sl.next(t)
		if s.d != 5*time.Minute || e.host.Calls() != 0 {
			t.Fatalf("sleeps %v after a failed look, %d calls", s.d, e.host.Calls())
		}
		e.clk.Advance(s.d)
		s.fire <- e.clk.Now()
		s = sl.next(t)
		if e.host.Calls() != 1 || e.link("U1").Version != 2 || s.d < 20*24*time.Hour {
			t.Fatalf("%d calls, version %d, then sleeps %v", e.host.Calls(), e.link("U1").Version, s.d)
		}
	})
}

func TestALinkWaitingAfterAFailureDoesNotKeepTheLoopAwake(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		e := newEnv(t, st)
		sl := newSleeps()
		tk := e.tokens(sl.after)
		e.user("U1")
		e.connect(tk, "U1")
		e.host.setHook(func(int) error { return errors.New("the host is down") })
		e.clk.Advance(e.link("U1").RenewAt.Sub(e.clk.Now()))
		stop := runLoop(tk)
		defer stop()
		// It is overdue and waiting: the loop sleeps for its wait, not 0 again and again.
		for _, want := range []time.Duration{5 * time.Minute, 10 * time.Minute} {
			s := sl.next(t)
			if s.d != want {
				t.Fatalf("sleeps %v, want %v", s.d, want)
			}
			e.clk.Advance(s.d)
			s.fire <- e.clk.Now()
		}
		sl.next(t)
		if e.host.Calls() != 3 {
			t.Fatalf("%d calls", e.host.Calls())
		}
	})
}

// --- a link that moved on ---------------------------------------------------------------

func TestARefreshTokenThatEndedDoesNotEndALinkThePersonJustConnectedAgain(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		e := newEnv(t, st)
		tk := e.tokens(nil)
		e.user("U1")
		e.connect(tk, "U1")
		// The host was down until the refresh token ended; while the loop asks it one
		// last time, the person connects again.
		e.clk.Advance(30*24*time.Hour + time.Minute)
		e.host.setHook(func(call int) error {
			if call == 1 {
				e.connect(tk, "U1")
			}
			return errors.New("the host is down")
		})
		tk.RenewDue(ctx)
		l := e.link("U1")
		if l.State != store.LinkOK || l.Version != 2 || l.AccessEnc == "" {
			t.Fatalf("the new link was ended: %+v", l)
		}
		if n := e.notes("U1"); n != 0 {
			t.Fatalf("%d notifications", n)
		}
		// Without a reconnection, an ended refresh token ends the link and tells the person once.
		e.user("U2")
		e.connect(tk, "U2")
		e.clk.Advance(30*24*time.Hour + time.Minute)
		e.host.setHook(func(int) error { return errors.New("the host is down") })
		tk.RenewDue(ctx)
		tk.RenewDue(ctx)
		if l := e.link("U2"); l.State != store.LinkExpired || e.notes("U2") != 1 {
			t.Fatalf("%+v, %d notifications", l, e.notes("U2"))
		}
	})
}

func TestARefusalOfAnOldPairAnswersThePairThePersonJustConnected(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		e := newEnv(t, st)
		tk := e.tokens(nil)
		e.user("U1")
		e.connect(tk, "U1")
		e.clk.Advance(2 * time.Hour) // the access token ended
		var fresh connections.Pair
		e.host.setHook(func(call int) error {
			if call == 1 {
				fresh = e.connect(tk, "U1") // connected again while the old pair was being renewed
				return connections.ErrRevoked
			}
			return nil
		})
		got, err := tk.Token(ctx, provider, "U1")
		if err != nil || got != fresh.Access {
			t.Fatalf("%q %v, want the new pair's %q", got, err, fresh.Access)
		}
		if l := e.link("U1"); l.State != store.LinkOK || e.notes("U1") != 0 {
			t.Fatalf("%+v, %d notifications", l, e.notes("U1"))
		}
		if e.host.Calls() != 1 {
			t.Fatalf("%d calls", e.host.Calls())
		}
	})
}

// --- one renewal across processes --------------------------------------------------------

func TestTwoServersOnOneDatabaseHandTheRefreshTokenToTheHostOnce(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		e := newEnv(t, st)
		a, b := e.tokens(nil), e.tokens(nil) // the server and its standby
		e.user("U1")
		e.connect(a, "U1")
		e.clk.Advance(2 * time.Hour) // the access token ended
		inHost, letGo := make(chan struct{}), make(chan struct{})
		e.host.setHook(func(call int) error {
			if call == 1 {
				close(inHost)
				<-letGo
			}
			return nil
		})
		type result struct {
			tok string
			err error
		}
		got := make(chan result, 1)
		go func() { tok, err := a.Token(ctx, provider, "U1"); got <- result{tok, err} }()
		<-inHost
		// While a asks the host, b does not: not for a caller, not in its loop.
		var ce *connections.Error
		if _, err := b.Token(ctx, provider, "U1"); !errors.As(err, &ce) || ce.Code != connections.CodeUnavailable || ce.RetryAfter <= 0 {
			t.Fatalf("%v", err)
		}
		if _, err := b.Renew(ctx, provider, "U1"); !errors.As(err, &ce) || ce.Code != connections.CodeUnavailable {
			t.Fatalf("%v", err)
		}
		// Due for the loop as well (the lease is still a's: a renewal takes seconds, a lease minutes).
		if _, err := st.Q().Exec(ctx, `UPDATE linked_accounts SET renew_at = ? WHERE provider_id = ?`, e.clk.Now().UnixMilli(), provider); err != nil {
			t.Fatal(err)
		}
		if n := b.RenewDue(ctx); n != 0 || e.host.Calls() != 1 {
			t.Fatalf("%d renewed, %d calls", n, e.host.Calls())
		}
		close(letGo)
		r := <-got
		if r.err != nil {
			t.Fatal(r.err)
		}
		// b uses what a stored; the host was asked once, and the link is good.
		if tok, err := b.Token(ctx, provider, "U1"); err != nil || tok != r.tok {
			t.Fatalf("%q %v, a got %q", tok, err, r.tok)
		}
		if l := e.link("U1"); e.host.Calls() != 1 || l.State != store.LinkOK || l.Version != 2 || !l.RenewingUntil.IsZero() {
			t.Fatalf("%d calls, %+v", e.host.Calls(), l)
		}
	})
}

func TestALeaseLeftByAServerThatDiedEndsAndTheLinkIsRenewed(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		e := newEnv(t, st)
		sl := newSleeps()
		tk := e.tokens(sl.after)
		e.user("U1")
		e.connect(tk, "U1")
		l := e.link("U1")
		e.clk.Advance(l.RenewAt.Sub(e.clk.Now()))
		// Another server took the lease and died with it.
		until := e.clk.Now().Add(2 * time.Minute)
		if ok, err := store.ClaimLinkRenewal(ctx, st.Q(), l.ID, l.Version, e.clk.Now(), until); err != nil || !ok {
			t.Fatalf("%v %v", ok, err)
		}
		stop := runLoop(tk)
		defer stop()
		// The loop leaves the link alone until the lease ends, then renews it.
		s := sl.next(t)
		if s.d != 2*time.Minute || e.host.Calls() != 0 {
			t.Fatalf("sleeps %v while another holds the lease, %d calls", s.d, e.host.Calls())
		}
		e.clk.Advance(s.d)
		s.fire <- e.clk.Now()
		sl.next(t)
		if l := e.link("U1"); e.host.Calls() != 1 || l.Version != 2 || !l.RenewingUntil.IsZero() {
			t.Fatalf("%d calls, %+v", e.host.Calls(), l)
		}
	})
}
