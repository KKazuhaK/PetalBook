package api_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/api"
	"github.com/KKazuhaK/PetalBook/internal/auth"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

func TestLoginRateLimitPerUsername(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		a.setup("kazuha", pw)
		bad := map[string]any{"username": "kazuha", "password": "wrong password here"}
		// Each attempt from a new address, so only the username is counted.
		try := func(i int, body map[string]any) result {
			return a.newClient(fmt.Sprintf("203.0.113.%d", i)).do("POST", "/api/login", body)
		}
		for i := 1; i <= 6; i++ {
			try(i, bad).want(t, 401, "unauthenticated") // five are free, the sixth starts the delay
		}
		r := try(7, bad).want(t, 429, "rate_limited")
		if ra := r.resp.Header.Get("Retry-After"); ra != "2" {
			t.Fatalf("Retry-After %q", ra)
		}
		// The right password does not get past a block.
		try(8, map[string]any{"username": "kazuha", "password": pw}).want(t, 429, "rate_limited")
		// Other usernames are not affected.
		try(9, map[string]any{"username": "someone", "password": "x"}).want(t, 401, "")
		// After the wait the next failure doubles the delay.
		a.clock.Advance(2 * time.Second)
		try(10, bad).want(t, 401, "unauthenticated")
		r = try(11, bad).want(t, 429, "rate_limited")
		if ra := r.resp.Header.Get("Retry-After"); ra != "4" {
			t.Fatalf("Retry-After %q after the second block", ra)
		}
		// A correct sign-in after the wait works and clears the count.
		a.clock.Advance(4 * time.Second)
		try(12, map[string]any{"username": "kazuha", "password": pw}).want(t, 200, "")
		for i := 20; i < 25; i++ {
			try(i, bad).want(t, 401, "unauthenticated")
		}
		try(26, bad).want(t, 401, "unauthenticated") // free again: only five since the reset
	})
}

func TestLoginRateLimitPerAddress(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		a.setup("kazuha", pw)
		c := a.newClient("203.0.113.50")
		for i := range 11 {
			c.do("POST", "/api/login", map[string]any{"username": fmt.Sprintf("nobody%d", i), "password": "x"}).want(t, 401, "")
		}
		c.do("POST", "/api/login", map[string]any{"username": "kazuha", "password": pw}).want(t, 429, "rate_limited")
		// Another address is untouched.
		a.newClient("203.0.113.51").do("POST", "/api/login", map[string]any{"username": "kazuha", "password": pw}).want(t, 200, "")
		a.clock.Advance(time.Minute)
		c.do("POST", "/api/login", map[string]any{"username": "kazuha", "password": pw}).want(t, 200, "")
	})
}

func TestRateLimitBehindATrustedProxy(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{proxies: []string{"10.0.0.0/8"}}, func(t *testing.T, a *acc) {
		a.setup("kazuha", pw)
		// Everything arrives from the proxy; the real clients differ.
		viaProxy := func(client string) *client {
			c := a.newClient("10.0.0.2")
			c.headers["X-Forwarded-For"] = client
			return c
		}
		for i := range 11 {
			viaProxy("198.51.100.1").do("POST", "/api/login", map[string]any{"username": fmt.Sprintf("n%d", i), "password": "x"}).want(t, 401, "")
		}
		viaProxy("198.51.100.1").do("POST", "/api/login", map[string]any{"username": "kazuha", "password": pw}).want(t, 429, "rate_limited")
		viaProxy("198.51.100.2").do("POST", "/api/login", map[string]any{"username": "kazuha", "password": pw}).want(t, 200, "")
	})
	// A peer that is not a trusted proxy cannot pick its own address.
	eachAccounts(t, accOpts{proxies: []string{"10.0.0.0/8"}}, func(t *testing.T, a *acc) {
		a.setup("kazuha", pw)
		for i := range 11 {
			c := a.newClient("203.0.113.9")
			c.headers["X-Forwarded-For"] = fmt.Sprintf("198.51.100.%d", i)
			c.do("POST", "/api/login", map[string]any{"username": fmt.Sprintf("n%d", i), "password": "x"}).want(t, 401, "")
		}
		c := a.newClient("203.0.113.9")
		c.headers["X-Forwarded-For"] = "198.51.100.200"
		c.do("POST", "/api/login", map[string]any{"username": "kazuha", "password": pw}).want(t, 429, "rate_limited")
	})
}

func TestSetupAndInviteEndpointsAreThrottled(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		c := a.newClient("203.0.113.60")
		for range 11 { // ten failures are free; the eleventh starts the delay
			c.do("POST", "/api/setup", map[string]any{"token": "guess", "username": "x1", "password": pw}).want(t, 403, "")
		}
		c.do("POST", "/api/setup", map[string]any{"token": a.auth.SetupToken(), "username": "kazuha", "password": pw}).want(t, 429, "rate_limited")
	})
}

func TestUnknownUserCostsAsMuchAsAWrongPassword(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		a.setup("kazuha", pw)
		ops := func() int64 { return auth.HashOps(a.auth) }
		before := ops()
		a.newClient("203.0.113.1").do("POST", "/api/login", map[string]any{"username": "kazuha", "password": "wrong password"}).want(t, 401, "")
		wrong := ops() - before
		before = ops()
		a.newClient("203.0.113.2").do("POST", "/api/login", map[string]any{"username": "nobody", "password": "wrong password"}).want(t, 401, "")
		unknown := ops() - before
		if wrong != 1 || unknown != 1 {
			t.Fatalf("a wrong password ran %d hashes, an unknown user %d", wrong, unknown)
		}
	})
}

// typeFor is the content type a route's body must have, as extra headers.
func typeFor(rt api.RouteInfo) []string {
	if rt.Upload {
		return []string{"Content-Type", "application/octet-stream"}
	}
	return nil
}

func TestEveryProtectedRouteWantsASession(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		sid := admin.personalSpace()
		anon := a.newClient("")
		fill := func(p string) string {
			return strings.NewReplacer("{space}", sid, "{id}", "01JD3K8Q7Z").Replace(p)
		}
		checked := 0
		for _, rt := range api.Routes() {
			path := fill(rt.Pattern)
			if rt.Public {
				continue
			}
			checked++
			r := anon.do(rt.Method, path, "{}", typeFor(rt)...)
			if r.code != 401 || r.body["error"] != "unauthenticated" {
				t.Errorf("%s %s without a session: %d %s", rt.Method, rt.Pattern, r.code, r.raw)
			}
		}
		if checked < 25 {
			t.Fatalf("only %d protected routes were checked: is the route list empty?", checked)
		}
		anon.do("GET", "/api/meta", nil).want(t, 200, "")
		anon.do("GET", "/api/session", nil).want(t, 200, "")
		// A cookie that names nothing is no session either.
		bad := a.newClient("")
		bad.cookies[a.auth.SessionCookieName()] = &http.Cookie{Name: a.auth.SessionCookieName(), Value: "forged"}
		bad.do("GET", "/api/spaces", nil).want(t, 401, "unauthenticated")
		bad.do("GET", "/api/meta", nil).want(t, 200, "") // meta just shows no spaces
	})
}

func TestMemberRoutesAreNotForOtherPeopleSpaces(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		boss := a.setup("kazuha", pw) // an administrator
		tang := a.addUser("tang", pw, "member")
		tangSpace := tang.personalSpace()
		r := tang.do("POST", "/api/spaces/"+tangSpace+"/pages", map[string]any{"meta": map[string]any{"title": "Private"}, "body": "secret\n"}).want(t, 201, "")
		pageID := r.body["id"].(string)
		tang.do("PUT", "/api/spaces/"+tangSpace+"/pages/"+pageID+"/draft", map[string]any{"baseRev": r.body["rev"], "meta": map[string]any{"title": "Private"}, "body": "secret edit\n"}).want(t, 200, "")

		// What "no such space" looks like for a space that is not there at all.
		ghost := boss.do("GET", "/api/spaces/0000000000/tree", nil).want(t, 404, "not_found")

		n := 0
		for _, rt := range api.Routes() {
			if !rt.Space {
				continue
			}
			n++
			path := strings.NewReplacer("{space}", tangSpace, "{id}", pageID).Replace(rt.Pattern)
			// A member of nothing here is told the same as for a space that does not
			// exist. (An administrator is not refused: see TestAdministratorSeesEverySpace.)
			for who, c := range map[string]*client{"member": a.addUser(fmt.Sprintf("m%d", n), pw, "member")} {
				got := c.do(rt.Method, path, "{}", typeFor(rt)...)
				if got.code != 404 || got.raw != ghost.raw {
					t.Errorf("%s %s as %s: %d %s", rt.Method, rt.Pattern, who, got.code, got.raw)
				}
			}
		}
		if n < 12 {
			t.Fatalf("only %d space routes: is the route list empty?", n)
		}
		// Nothing of it changed, and the owner still sees everything.
		p := tang.do("GET", "/api/spaces/"+tangSpace+"/pages/"+pageID, nil).want(t, 200, "")
		if p.body["body"] != "secret\n" || p.body["draft"] == nil {
			t.Fatal(p.raw)
		}
		// The administrator's lists do not include it.
		for _, l := range boss.do("GET", "/api/spaces", nil).body["spaces"].([]any) {
			if l.(map[string]any)["id"] == tangSpace {
				t.Fatal("an administrator can see another user's personal space")
			}
		}
		if l := boss.do("GET", "/api/meta", nil).body["spaces"].([]any); len(l) != 1 {
			t.Fatalf("%v", l)
		}
	})
}

func TestSpaces(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		c := a.setup("kazuha", pw)
		personal := c.personalSpace()
		r := c.do("POST", "/api/spaces", map[string]any{"name": "Tang"}).want(t, 201, "")
		if r.body["name"] != "Tang" || r.body["role"] != "owner" || r.body["personal"] != false || r.resp.Header.Get("Location") == "" {
			t.Fatal(r.raw)
		}
		tangID := r.body["id"].(string)
		c.do("POST", "/api/spaces", map[string]any{"name": "Alpha"}).want(t, 201, "")
		c.do("POST", "/api/spaces", map[string]any{"name": ""}).want(t, 400, "invalid")
		c.do("POST", "/api/spaces", map[string]any{"name": strings.Repeat("x", 81)}).want(t, 400, "invalid")
		c.do("POST", "/api/spaces", map[string]any{"name": "a\nb"}).want(t, 400, "invalid")
		l := c.do("GET", "/api/spaces", nil).body["spaces"].([]any)
		var names []string
		for _, s := range l {
			names = append(names, s.(map[string]any)["name"].(string))
		}
		if len(l) != 3 || l[0].(map[string]any)["id"] != personal || strings.Join(names[1:], ",") != "Alpha,Tang" {
			t.Fatalf("order: %v", names)
		}
		// The new space works: pages, and it is its own repository.
		c.do("POST", "/api/spaces/"+tangID+"/pages", map[string]any{"meta": map[string]any{"title": "x"}}).want(t, 201, "")
		if l := c.do("GET", "/api/spaces/"+tangID+"/pages", nil).body["items"].([]any); len(l) != 1 {
			t.Fatal("the page is not in the new space")
		}
		if l := c.do("GET", "/api/spaces/"+personal+"/pages", nil).body["items"].([]any); len(l) != 0 {
			t.Fatal("spaces share pages")
		}
		// Renaming shows at once in every list.
		r = c.do("PATCH", "/api/spaces/"+tangID, map[string]any{"name": "Zed"}).want(t, 200, "")
		if r.body["name"] != "Zed" || r.body["id"] != tangID {
			t.Fatal(r.raw)
		}
		if got := c.do("GET", "/api/meta", nil).raw; !strings.Contains(got, `"Zed"`) || strings.Contains(got, `"Tang"`) {
			t.Fatal(got)
		}
		c.do("PATCH", "/api/spaces/"+tangID, map[string]any{"name": ""}).want(t, 400, "invalid")
		// Someone else cannot rename it.
		other := a.addUser("lin", pw, "member")
		other.do("PATCH", "/api/spaces/"+tangID, map[string]any{"name": "Mine"}).want(t, 404, "not_found")
	})
}

func TestNoHalfMadeSpaceOrAccountWhenAStepFails(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		c := a.setup("kazuha", pw)
		ctx := context.Background()
		count := func(table string) int {
			var n int
			if err := a.st.Q().QueryRow(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		// Make the last step of each creation fail by hiding the table it writes.
		hide := func(table string) func() {
			if _, err := a.st.Q().Exec(ctx, `ALTER TABLE `+table+` RENAME TO `+table+`_hidden`); err != nil {
				t.Fatal(err)
			}
			return func() {
				if _, err := a.st.Q().Exec(ctx, `ALTER TABLE `+table+`_hidden RENAME TO `+table); err != nil {
					t.Fatal(err)
				}
			}
		}
		spaces0, dirs0 := count("spaces"), len(listDir(t, a.dir+"/spaces"))

		restore := hide("space_members")
		if _, err := a.mgr.Create(ctx, mustUserID(t, a, "kazuha"), "Doomed"); err == nil {
			t.Fatal("the space was made although a step failed")
		}
		if _, err := a.auth.CreateUser(ctx, auth.CreateParams{Username: "doomed", Role: "member", Password: pw}); err == nil {
			t.Fatal("the account was made although a step failed")
		}
		restore()
		if count("spaces") != spaces0 || count("users") != 1 || len(listDir(t, a.dir+"/spaces")) != dirs0 {
			t.Fatalf("a failure left something behind: %d spaces, %d users, %v", count("spaces"), count("users"), listDir(t, a.dir+"/spaces"))
		}
		if len(c.do("GET", "/api/spaces", nil).body["spaces"].([]any)) != 1 {
			t.Fatal("a half-made space is visible")
		}
		// And the same accounts can be made once the fault is gone.
		if _, err := a.auth.CreateUser(ctx, auth.CreateParams{Username: "doomed", Role: "member", Password: pw}); err != nil {
			t.Fatal(err)
		}
	})
}

func mustUserID(t *testing.T, a *acc, username string) string {
	t.Helper()
	us, err := a.auth.Users(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range us {
		if u.Username == username {
			return u.ID
		}
	}
	t.Fatalf("no user %s", username)
	return ""
}

func TestAConcurrentBurstOfWrongPasswordsGetsOnlyItsAllowance(t *testing.T) {
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
		codes := make([]int, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// Different addresses: this is the per-username allowance.
				c := a.newClient(fmt.Sprintf("203.0.%d.%d", i/200, i%200+1))
				codes[i] = c.do("POST", "/api/login", map[string]any{"username": "kazuha", "password": "wrong password here"}).code
			}()
		}
		wg.Wait()
		got := map[int]int{}
		for _, c := range codes {
			got[c]++
		}
		if got[401] != 6 || got[429] != n-6 {
			t.Fatalf("60 simultaneous wrong logins for one user: %v (want 6 answered 401, the rest 429)", got)
		}
		// And per address, with different usernames.
		codes = make([]int, n)
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c := a.newClient("198.51.100.99")
				codes[i] = c.do("POST", "/api/login", map[string]any{"username": fmt.Sprintf("nobody%d", i), "password": "wrong password here"}).code
			}()
		}
		wg.Wait()
		got = map[int]int{}
		for _, c := range codes {
			got[c]++
		}
		if got[401] != 11 || got[429] != n-11 {
			t.Fatalf("60 simultaneous wrong logins from one address: %v (want 11 answered 401, the rest 429)", got)
		}
		// A success gives its place back: it does not use up the allowance.
		a.clock.Advance(time.Hour)
		c := a.newClient("198.51.100.5")
		for range 20 {
			c.do("POST", "/api/login", map[string]any{"username": "kazuha", "password": pw}).want(t, 200, "")
		}
	})
}

func TestTheUsernameLimitIsKeyedOnTheUsernameSignInReads(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		a.setup("kazuha", pw)
		spellings := []string{"kazuha", " kazuha", "KAZUHA", "kazuha ", "Kazuha", "\tkazuha\n"}
		for i, name := range spellings {
			a.newClient(fmt.Sprintf("203.0.113.%d", i+1)).do("POST", "/api/login", map[string]any{"username": name, "password": "wrong password here"}).want(t, 401, "")
		}
		// Six failures under six spellings are six failures of one username.
		a.newClient("203.0.113.50").do("POST", "/api/login", map[string]any{"username": " KaZuHa ", "password": pw}).want(t, 429, "rate_limited")
		a.newClient("203.0.113.51").do("POST", "/api/login", map[string]any{"username": "kazuha", "password": pw}).want(t, 429, "rate_limited")
	})
}

func TestSpacesPerUserAreLimited(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{maxSpaces: 3}, func(t *testing.T, a *acc) {
		c := a.setup("kazuha", pw) // the personal space is the first of three
		c.do("POST", "/api/spaces", map[string]any{"name": "B"}).want(t, 201, "")
		c.do("POST", "/api/spaces", map[string]any{"name": "C"}).want(t, 201, "")
		r := c.do("POST", "/api/spaces", map[string]any{"name": "D"}).want(t, 403, "forbidden")
		if !strings.Contains(r.body["message"].(string), "3") {
			t.Fatalf("the message does not say what the limit is: %s", r.raw)
		}
		if len(c.do("GET", "/api/spaces", nil).body["spaces"].([]any)) != 3 {
			t.Fatal("a refused space is listed")
		}
		// Another user is not affected.
		a.addUser("tang", pw, "member").do("POST", "/api/spaces", map[string]any{"name": "B"}).want(t, 201, "")
	})
}

func mustUser(t *testing.T, a *acc, username string) store.User {
	t.Helper()
	u, err := store.GetUser(context.Background(), a.st.Q(), mustUserID(t, a, username))
	if err != nil {
		t.Fatal(err)
	}
	return *u
}
