package api_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"petalbook.app/server/internal/auth"
)

func TestFailureCapByUsernameDoesNotBlockTheRightPassword(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		admin.putSetting("signin.maxFailures", 3).want(t, 200, "")
		try := func(i int, body map[string]any) result {
			return a.newClient(fmt.Sprintf("203.0.113.%d", i)).do("POST", "/api/login", body)
		}
		try(1, bad("kazuha")).want(t, 401, "unauthenticated")
		try(2, bad("kazuha")).want(t, 401, "unauthenticated")
		// The failure that reaches the cap is answered 429, until the window ends.
		r := try(3, bad("kazuha")).want(t, 429, "rate_limited")
		if ra := r.resp.Header.Get("Retry-After"); ra != "900" {
			t.Fatalf("Retry-After %q", ra)
		}
		// Someone who types the right password from elsewhere is not stopped by failures against the name.
		try(4, good("kazuha")).want(t, 200, "")
		// And that cleared them.
		try(5, bad("kazuha")).want(t, 401, "unauthenticated")
		// Other usernames are not affected; the name is the one sign-in reads.
		try(6, bad("someone")).want(t, 401, "")
		for i, name := range []string{" KAZUHA", "Kazuha\n"} {
			try(10+i, bad(name))
		}
		try(20, bad("kazuha")).want(t, 429, "rate_limited")
	})
}

func TestFailureCapByAddressIsCheckedBeforeThePassword(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		admin.putSetting("signin.maxFailures", 3).want(t, 200, "")
		c := a.newClient("203.0.113.50")
		for i := range 3 {
			c.do("POST", "/api/login", bad(fmt.Sprintf("nobody%d", i))).want(t, 401, "")
		}
		ops := auth.HashOps(a.auth)
		r := c.do("POST", "/api/login", good("kazuha")).want(t, 429, "rate_limited")
		if auth.HashOps(a.auth) != ops {
			t.Fatal("a password was hashed for an address over its cap")
		}
		if ra := r.resp.Header.Get("Retry-After"); ra != "900" {
			t.Fatalf("Retry-After %q", ra)
		}
		a.newClient("203.0.113.51").do("POST", "/api/login", good("kazuha")).want(t, 200, "")
		a.clock.Advance(10 * time.Minute)
		r = c.do("POST", "/api/login", good("kazuha")).want(t, 429, "rate_limited")
		if ra := r.resp.Header.Get("Retry-After"); ra != "300" {
			t.Fatalf("Retry-After %q is not the rest of the window", ra)
		}
		a.clock.Advance(5 * time.Minute)
		c.do("POST", "/api/login", good("kazuha")).want(t, 200, "")
		rows := admin.audit(t, "rate_limited")
		if len(rows) != 1 { // the address, once per window; not per request
			t.Fatalf("%d rate_limited rows: %v", len(rows), rows)
		}
	})
}

func TestRequestsPerMinuteAreSharedAndAudited(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		admin.putSetting("signin.perMinute", 3).want(t, 200, "")
		c := a.newClient("203.0.113.70")
		c.do("POST", "/api/login", good("kazuha")).want(t, 200, "")
		c.do("GET", "/api/captcha?ctx=login", nil).want(t, 200, "")
		c.do("POST", "/api/auth/passkey/begin", map[string]any{}).want(t, 200, "")
		r := c.do("POST", "/api/login", good("kazuha")).want(t, 429, "rate_limited")
		if ra := r.resp.Header.Get("Retry-After"); ra != "60" {
			t.Fatalf("Retry-After %q", ra)
		}
		c.do("GET", "/api/captcha?ctx=login", nil).want(t, 429, "rate_limited")
		c.do("POST", "/api/invites/check", map[string]any{"token": "x"}).want(t, 429, "rate_limited")
		c.do("POST", "/api/setup", map[string]any{"token": "x", "username": "zz", "password": pw}).want(t, 429, "rate_limited")
		a.newClient("203.0.113.71").do("POST", "/api/login", good("kazuha")).want(t, 200, "")
		a.clock.Advance(time.Minute)
		c.do("POST", "/api/login", good("kazuha")).want(t, 200, "")
		// Once per key and minute in the audit log.
		n := 0
		for _, row := range admin.audit(t, "rate_limited") {
			if row["detail"].(map[string]any)["limit"] == "per_minute" && row["ip"] == "203.0.113.70" {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("%d rows", n)
		}
		// A change applies at once.
		admin.putSetting("signin.perMinute", 1).want(t, 200, "")
		d := a.newClient("203.0.113.72")
		d.do("GET", "/api/captcha?ctx=login", nil).want(t, 200, "")
		d.do("GET", "/api/captcha?ctx=login", nil).want(t, 429, "rate_limited")
	})
}

func TestTheLimitsFollowTheTrustedProxy(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{proxies: []string{"10.0.0.0/8"}}, func(t *testing.T, a *acc) {
		a.setup("kazuha", pw)
		viaProxy := func(client string) *client {
			c := a.newClient("10.0.0.2")
			c.headers["X-Forwarded-For"] = client
			return c
		}
		for i := range 10 {
			viaProxy("198.51.100.1").do("POST", "/api/login", bad(fmt.Sprintf("n%d", i))).want(t, 401, "")
		}
		viaProxy("198.51.100.1").do("POST", "/api/login", good("kazuha")).want(t, 429, "rate_limited")
		viaProxy("198.51.100.2").do("POST", "/api/login", good("kazuha")).want(t, 200, "")
	})
	// A peer that is not a trusted proxy cannot pick its own address.
	eachAccounts(t, accOpts{proxies: []string{"10.0.0.0/8"}}, func(t *testing.T, a *acc) {
		a.setup("kazuha", pw)
		for i := range 10 {
			c := a.newClient("203.0.113.9")
			c.headers["X-Forwarded-For"] = fmt.Sprintf("198.51.100.%d", i)
			c.do("POST", "/api/login", bad(fmt.Sprintf("n%d", i))).want(t, 401, "")
		}
		c := a.newClient("203.0.113.9")
		c.headers["X-Forwarded-For"] = "198.51.100.200"
		c.do("POST", "/api/login", good("kazuha")).want(t, 429, "rate_limited")
	})
}

func TestWrongSetupAndInviteTokensCountAgainstTheAddress(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		c := a.newClient("203.0.113.60")
		for range 10 {
			c.do("POST", "/api/setup", map[string]any{"token": "guess", "username": "x1", "password": pw}).want(t, 403, "")
		}
		c.do("POST", "/api/setup", map[string]any{"token": a.auth.SetupToken(), "username": "kazuha", "password": pw}).want(t, 429, "rate_limited")
		d := a.newClient("203.0.113.61")
		d.do("POST", "/api/setup", map[string]any{"token": a.auth.SetupToken(), "username": "kazuha", "password": pw}).want(t, 201, "")
		e := a.newClient("203.0.113.62")
		for range 10 {
			e.do("POST", "/api/invites/check", map[string]any{"token": "guess"}).want(t, 404, "")
		}
		e.do("POST", "/api/invites/check", map[string]any{"token": "guess"}).want(t, 429, "rate_limited")
	})
}

func TestABurstOfWrongPasswordsGetsOnlyItsAllowance(t *testing.T) {
	t.Parallel()
	// Hashing is slow and one at a time, so the burst queues behind it. Every
	// attempt must be counted before it waits, not judged by the count from before.
	o := accOpts{mod: func(c *auth.Config) {
		c.Password = auth.PasswordParams{MemoryKiB: 4 * 1024, Time: 1, Threads: 1}
		c.HashConcurrency = 1
	}}
	eachAccounts(t, o, func(t *testing.T, a *acc) {
		a.setup("kazuha", pw)
		const n = 60
		run := func(mk func(i int) (string, map[string]any)) map[int]int {
			codes := make([]int, n)
			var wg sync.WaitGroup
			for i := range n {
				wg.Add(1)
				go func() {
					defer wg.Done()
					ip, body := mk(i)
					codes[i] = a.newClient(ip).do("POST", "/api/login", body).code
				}()
			}
			wg.Wait()
			got := map[int]int{}
			for _, c := range codes {
				got[c]++
			}
			return got
		}
		// One username from many addresses: the ninth failure reaches the cap of ten... counted from the first.
		got := run(func(i int) (string, map[string]any) { return fmt.Sprintf("203.0.%d.%d", i/200, i%200+1), bad("kazuha") })
		if got[401] != 9 || got[429] != n-9 {
			t.Fatalf("60 simultaneous wrong logins for one user: %v", got)
		}
		// Many usernames from one address: ten reservations, the rest refused before any hash.
		a.clock.Advance(time.Hour)
		ops := auth.HashOps(a.auth)
		got = run(func(i int) (string, map[string]any) { return "198.51.100.99", bad(fmt.Sprintf("nobody%d", i)) })
		if got[401] != 10 || got[429] != n-10 {
			t.Fatalf("60 simultaneous wrong logins from one address: %v", got)
		}
		if h := auth.HashOps(a.auth) - ops; h != 10 {
			t.Fatalf("%d hashes for ten attempts", h)
		}
		// A success gives its place back: it does not use up the allowance.
		a.clock.Advance(time.Hour)
		c := a.newClient("198.51.100.5")
		for range 20 {
			c.do("POST", "/api/login", good("kazuha")).want(t, 200, "")
		}
	})
}

func TestLockoutScopes(t *testing.T) {
	t.Parallel()
	for _, scope := range []string{"ip_account", "ip", "account"} {
		t.Run(scope, func(t *testing.T) {
			t.Parallel()
			eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
				admin := a.setup("kazuha", pw)
				a.addUser("tang", pw, "member")
				admin.putSetting("signin.maxFailures", 3).want(t, 200, "")
				admin.putSetting("signin.windowMinutes", 5).want(t, 200, "")
				admin.putSetting("signin.lockout.enabled", true).want(t, 200, "")
				admin.putSetting("signin.lockout.minutes", 30).want(t, 200, "")
				admin.putSetting("signin.lockout.scope", scope).want(t, 200, "")
				c := a.newClient("203.0.113.80")
				c.do("POST", "/api/login", bad("kazuha")).want(t, 401, "")
				c.do("POST", "/api/login", bad("kazuha")).want(t, 401, "")
				c.do("POST", "/api/login", bad("kazuha")).want(t, 429, "rate_limited")
				// The window (5 minutes) is over, the lock (30) is not.
				a.clock.Advance(10 * time.Minute)
				r := c.do("POST", "/api/login", good("kazuha"))
				switch scope {
				case "ip_account", "ip", "account":
					r.want(t, 429, "rate_limited")
				}
				if ra := r.resp.Header.Get("Retry-After"); ra != "1200" {
					t.Fatalf("Retry-After %q, want the rest of the lock", ra)
				}
				other := func(ip, user string) result {
					return a.newClient(ip).do("POST", "/api/login", good(user))
				}
				switch scope {
				case "ip_account":
					other("203.0.113.81", "kazuha").want(t, 200, "") // someone elsewhere is not locked out
					c.do("POST", "/api/login", good("tang")).want(t, 200, "")
				case "ip":
					other("203.0.113.81", "kazuha").want(t, 200, "")
					c.do("POST", "/api/login", good("tang")).want(t, 429, "rate_limited") // the address, for every name
				case "account":
					other("203.0.113.81", "kazuha").want(t, 429, "rate_limited") // the name, from every address
					c.do("POST", "/api/login", good("tang")).want(t, 200, "")
				}
				a.clock.Advance(21 * time.Minute)
				other("203.0.113.80", "kazuha").want(t, 200, "")
				rows := admin.audit(t, "lockout")
				if len(rows) != 1 || rows[0]["detail"].(map[string]any)["scope"] != scope || rows[0]["detail"].(map[string]any)["minutes"] != float64(30) {
					t.Fatalf("%v", rows)
				}
			})
		})
	}
}

func TestLoginOrderAndAudit(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		a.addUser("tang", pw, "member")
		c := a.newClient("203.0.113.90")
		c.do("POST", "/api/login", bad("kazuha")).want(t, 401, "")
		c.do("POST", "/api/login", bad("nobody-here")).want(t, 401, "")
		c.do("POST", "/api/login", good("tang")).want(t, 200, "")
		rows := admin.audit(t, "login_failed")
		if len(rows) != 2 {
			t.Fatalf("%v", rows)
		}
		names := []string{}
		for _, r := range rows {
			if r["detail"].(map[string]any)["reason"] != "bad_password" || r["detail"].(map[string]any)["method"] != "password" {
				t.Fatalf("%v", r)
			}
			names = append(names, r["username"].(string))
		}
		if strings.Join(names, ",") != "nobody-here,kazuha" { // newest first; a name nobody has is recorded too
			t.Fatalf("%v", names)
		}
		if n := len(admin.audit(t, "login")); n < 3 {
			t.Fatalf("%d login rows", n)
		}
	})
}

// reasons counts the login_failed rows by reason.
func failedReasons(t *testing.T, admin *client) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, r := range admin.audit(t, "login_failed") {
		d := r["detail"].(map[string]any)
		out[d["method"].(string)+":"+d["reason"].(string)]++
	}
	return out
}

// Refusals anyone can make up cost a row of the audit log at most once a
// minute per address: a wrong captcha, and an attempt from a locked address.
// A passkey ceremony that never existed costs none.
func TestMadeUpFailuresDoNotFloodTheAuditLog(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		admin.putSetting("captcha.contexts", map[string]any{"login": map[string]any{"on": true, "when": "always"}}).want(t, 200, "")
		admin.putSetting("signin.perMinute", 100).want(t, 200, "")
		c := a.newClient("203.0.113.100")
		for range 5 {
			c.do("POST", "/api/login", map[string]any{"identifier": "kazuha", "password": pw, "captcha": map[string]any{"id": "made-up", "answer": "1"}}).want(t, 400, "captcha_failed")
		}
		if n := failedReasons(t, admin)["password:captcha_failed"]; n != 1 {
			t.Fatalf("%d captcha rows from one address in a minute", n)
		}
		a.newClient("203.0.113.101").do("POST", "/api/login", map[string]any{"identifier": "kazuha", "password": pw}).want(t, 400, "captcha_failed")
		a.clock.Advance(time.Minute)
		c.do("POST", "/api/login", map[string]any{"identifier": "kazuha", "password": pw}).want(t, 400, "captcha_failed")
		if n := failedReasons(t, admin)["password:captcha_failed"]; n != 3 {
			t.Fatalf("%d captcha rows, want one per address and minute", n)
		}

		// Passkey sign-ins: a ceremony nobody began is not written down.
		for range 3 {
			c.do("POST", "/api/auth/passkey/finish?ceremony=made-up", map[string]any{"id": "x", "rawId": "eA", "type": "public-key", "response": map[string]any{}}).want(t, 401, "")
		}
		if n := failedReasons(t, admin)["passkey:passkey_failed"]; n != 0 {
			t.Fatalf("%d rows for made-up ceremonies", n)
		}
		// One that was begun and failed is.
		begin := c.do("POST", "/api/auth/passkey/begin", map[string]any{}).want(t, 200, "")
		c.do("POST", "/api/auth/passkey/finish?ceremony="+begin.body["ceremony"].(string), map[string]any{"id": "x", "rawId": "eA", "type": "public-key", "response": map[string]any{}}).want(t, 401, "")
		if n := failedReasons(t, admin)["passkey:passkey_failed"]; n != 1 {
			t.Fatalf("%d rows for a failed ceremony", n)
		}

		// A locked address: each refusal is login_failed rate_limited, once a minute.
		admin.putSetting("captcha.contexts", map[string]any{"login": map[string]any{"on": false}}).want(t, 200, "")
		admin.putSetting("signin.maxFailures", 2).want(t, 200, "")
		admin.putSetting("signin.windowMinutes", 5).want(t, 200, "")
		admin.putSetting("signin.lockout.enabled", true).want(t, 200, "")
		admin.putSetting("signin.lockout.minutes", 30).want(t, 200, "")
		admin.putSetting("signin.lockout.scope", "ip").want(t, 200, "")
		l := a.newClient("203.0.113.102")
		l.do("POST", "/api/login", bad("kazuha")).want(t, 401, "")
		l.do("POST", "/api/login", bad("kazuha")).want(t, 429, "rate_limited")
		for range 4 {
			l.do("POST", "/api/login", good("kazuha")).want(t, 429, "rate_limited")
		}
		if n := failedReasons(t, admin)["password:rate_limited"]; n != 1 {
			t.Fatalf("%d rate_limited rows from a locked address in a minute", n)
		}
		a.clock.Advance(time.Minute)
		l.do("POST", "/api/login", good("kazuha")).want(t, 429, "rate_limited")
		if n := failedReasons(t, admin)["password:rate_limited"]; n != 2 {
			t.Fatalf("%d rate_limited rows after a minute", n)
		}
	})
}
