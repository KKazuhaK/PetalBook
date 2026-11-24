package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A request that carries the cookie of a session that ended says why, in
// `reason` of the 401; a cookie that never was a session says nothing.

func (c *client) reasonOf(t *testing.T) any {
	t.Helper()
	r := c.do("GET", "/api/account", nil).want(t, 401, "unauthenticated")
	if r.body["reason"] == nil {
		return ""
	}
	return r.body["reason"]
}

// session is the cookie value of the client's session.
func (c *client) session(t *testing.T) string {
	t.Helper()
	ck := c.cookies[c.a.auth.SessionCookieName()]
	if ck == nil {
		t.Fatal("no session cookie")
	}
	return ck.Value
}

func wantReason(t *testing.T, c *client, want string) {
	t.Helper()
	// Twice: the second answer comes from the cache of refusals and says the same.
	for range 2 {
		if got := c.reasonOf(t); got != want {
			t.Fatalf("reason %v, want %q", got, want)
		}
	}
	// The session endpoint says it too, for a page that is opened afresh.
	r := c.do("GET", "/api/session", nil).want(t, 200, "")
	got := r.body["endedReason"]
	if got == nil {
		got = ""
	}
	if got != want {
		t.Fatalf("endedReason %v, want %q", got, want)
	}
}

func TestAnIdleSessionAnswersExpired(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		a.clock.Advance(31 * 24 * time.Hour)
		wantReason(t, tang, "expired")
	})
}

func TestASessionPastItsMaximumAnswersExpired(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		for range 3 { // in use all along, so never idle
			a.clock.Advance(29 * 24 * time.Hour)
			tang.do("GET", "/api/account", nil).want(t, 200, "")
		}
		a.clock.Advance(29 * 24 * time.Hour)
		wantReason(t, tang, "expired")
	})
}

func TestASessionTheSweeperRemovedAnswersExpired(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		a.clock.Advance(31 * 24 * time.Hour)
		a.auth.Sweep(context.Background())
		wantReason(t, tang, "expired")
	})
}

func TestSigningOutEverywhereAnswersSignedOutElsewhere(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		a.setup("kazuha", pw)
		one := a.addUser("tang", pw, "member")
		two := a.newClient("")
		two.do("POST", "/api/login", good("tang")).want(t, 200, "")
		id := mustUserID(t, a, "tang")
		// Everywhere but where the request came from.
		p, err := a.auth.Authenticate(context.Background(), two.session(t))
		if err != nil {
			t.Fatal(err)
		}
		keep := p.SessionID
		if err := a.auth.SignOutEverywhere(context.Background(), id, keep); err != nil {
			t.Fatal(err)
		}
		wantReason(t, one, "signed_out_elsewhere")
		two.do("GET", "/api/account", nil).want(t, 200, "")
	})
}

func TestAChangedPasswordAnswersPasswordChanged(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		one := a.setup("kazuha", pw)
		two := a.newClient("")
		two.do("POST", "/api/login", good("kazuha")).want(t, 200, "")
		one.do("POST", "/api/account/password", map[string]any{"current": pw, "new": "a brand new password"}).want(t, 204, "")
		wantReason(t, two, "password_changed")
		one.do("GET", "/api/account", nil).want(t, 200, "") // the one that changed it is still in
	})
}

func TestAPasswordSetByTheCommandLineAnswersPasswordChanged(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		one := a.setup("kazuha", pw)
		if err := a.auth.SetPassword(context.Background(), "kazuha", "another long password"); err != nil {
			t.Fatal(err)
		}
		wantReason(t, one, "password_changed")
	})
}

func TestADisabledAccountAnswersDisabled(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		admin.do("PATCH", "/api/users/"+mustUserID(t, a, "tang"), map[string]any{"disabled": true}).want(t, 200, "")
		wantReason(t, tang, "disabled")
		// Signing in again is refused like any wrong password: nothing more is said.
		r := a.newClient("").do("POST", "/api/login", good("tang")).want(t, 401, "")
		if _, ok := r.body["reason"]; ok {
			t.Fatal(r.raw)
		}
	})
}

func TestAnAccountDisabledWhileTheCookieWasInTheCacheAnswersDisabled(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		tang.do("GET", "/api/account", nil).want(t, 200, "") // cached
		admin.do("PATCH", "/api/users/"+mustUserID(t, a, "tang"), map[string]any{"disabled": true}).want(t, 200, "")
		wantReason(t, tang, "disabled")
	})
}

func TestSigningOutOnPurposeAndUnknownCookiesSayNothing(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		cookie := tang.session(t)
		tang.do("POST", "/api/logout", nil).want(t, 204, "")
		// The cookie of a session that was signed out on purpose.
		old := a.newClient("")
		old.cookies[a.auth.SessionCookieName()] = &http.Cookie{Name: a.auth.SessionCookieName(), Value: cookie}
		wantReason(t, old, "")

		for _, v := range []string{"garbage", "0123456789abcdef0123456789abcdef", strings.Repeat("a", 300)} {
			c := a.newClient("")
			c.cookies[a.auth.SessionCookieName()] = &http.Cookie{Name: a.auth.SessionCookieName(), Value: v}
			r := c.do("GET", "/api/account", nil).want(t, 401, "unauthenticated")
			if _, ok := r.body["reason"]; ok {
				t.Fatalf("%q: %s", v[:min(len(v), 8)], r.raw)
			}
		}
		// No cookie at all.
		r := a.newClient("").do("GET", "/api/account", nil).want(t, 401, "unauthenticated")
		if _, ok := r.body["reason"]; ok {
			t.Fatal(r.raw)
		}
		r = a.newClient("").do("GET", "/api/session", nil).want(t, 200, "")
		if _, ok := r.body["endedReason"]; ok {
			t.Fatal(r.raw)
		}
	})
}

func TestTheRecordOfAnEndedSessionIsSweptAfterThirtyDays(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		a.clock.Advance(31 * 24 * time.Hour)
		wantReason(t, tang, "expired")
		a.clock.Advance(29 * 24 * time.Hour)
		a.auth.SweepSessionEnds(context.Background())
		a.clock.Advance(40 * time.Second) // past the cache of refusals
		wantReason(t, tang, "expired")    // 29 days since it ended: kept
		a.clock.Advance(2 * 24 * time.Hour)
		a.auth.SweepSessionEnds(context.Background())
		a.clock.Advance(40 * time.Second)
		wantReason(t, tang, "")
	})
}

// Disabling a tenant ends its people's sessions there and then, as `disabled`: a
// cookie that is idle until after it would have expired still says disabled,
// whether the sweeper or the next request finds it.
func TestADisabledTenantAnswersDisabledLongAfter(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		f.root.do("PATCH", "/api/admin/tenants/"+f.tenB, map[string]any{"disabled": true}).want(t, 200, "")
		a.clock.Advance(31 * 24 * time.Hour)
		wantReason(t, f.admB, "disabled")
		a.auth.Sweep(context.Background())
		wantReason(t, f.memB, "disabled")
	})
}

// Enabling a person, or their tenant, again forgets that their sessions ended
// for being disabled: the old cookie is then one the server knows nothing of.
func TestEnablingAgainForgetsDisabled(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		id := mustUserID(t, a, "tang")
		admin.do("PATCH", "/api/users/"+id, map[string]any{"disabled": true}).want(t, 200, "")
		wantReason(t, tang, "disabled")
		admin.do("PATCH", "/api/users/"+id, map[string]any{"disabled": false}).want(t, 200, "")
		wantReason(t, tang, "") // at once: the cache of refusals is not trusted either
	})
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		// One of them is disabled on their own, and stays so when the tenant is enabled again.
		f.admB.do("PATCH", "/api/users/"+f.userB, map[string]any{"disabled": true}).want(t, 200, "")
		f.root.do("PATCH", "/api/admin/tenants/"+f.tenB, map[string]any{"disabled": true}).want(t, 200, "")
		wantReason(t, f.admB, "disabled")
		f.root.do("PATCH", "/api/admin/tenants/"+f.tenB, map[string]any{"disabled": false}).want(t, 200, "")
		wantReason(t, f.admB, "")
		wantReason(t, f.memB, "disabled")
	})
}
