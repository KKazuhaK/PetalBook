package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/auth"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

func TestSessionBeforeSetup(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		c := a.newClient("")
		r := c.do("GET", "/api/session", nil).want(t, 200, "")
		if r.body["mode"] != "accounts" || r.body["user"] != nil || r.body["setup"] != true {
			t.Fatalf("%s", r.raw)
		}
		m := r.body["methods"].(map[string]any)
		if m["password"] != true || m["oidc"] != nil {
			t.Fatalf("%s", r.raw)
		}
		if r.resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("session answer is cacheable")
		}
		// Meta works without a session and lists no spaces.
		r = c.do("GET", "/api/meta", nil).want(t, 200, "")
		if l, ok := r.body["spaces"].([]any); !ok || len(l) != 0 {
			t.Fatalf("%s", r.raw)
		}
	})
}

func TestSetup(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		c := a.newClient("")
		tok := a.auth.SetupToken()
		if len(tok) < 40 {
			t.Fatalf("setup token %q", tok)
		}
		good := map[string]any{"token": tok, "username": "Kazuha", "name": "", "password": pw}
		with := func(k string, v any) map[string]any {
			m := map[string]any{}
			for kk, vv := range good {
				m[kk] = vv
			}
			m[k] = v
			return m
		}
		c.do("POST", "/api/setup", with("token", "nope")).want(t, 403, "forbidden")
		c.do("POST", "/api/setup", with("token", "")).want(t, 403, "forbidden")
		c.do("POST", "/api/setup", with("username", "a b")).want(t, 400, "invalid")
		c.do("POST", "/api/setup", with("password", "short")).want(t, 400, "invalid")
		c.do("POST", "/api/setup", with("password", strings.Repeat("x", 1025))).want(t, 400, "invalid")
		if c.cookies[a.auth.SessionCookieName()] != nil {
			t.Fatal("a refused setup started a session")
		}
		r := c.do("POST", "/api/setup", good).want(t, 201, "")
		u := r.user()
		if u["username"] != "kazuha" || u["name"] != "kazuha" || u["role"] != "admin" || u["disabled"] != false {
			t.Fatalf("%s", r.raw)
		}
		ck := r.resp.Cookies()[0]
		if ck.Name != "__Host-petalbook_session" || !ck.HttpOnly || !ck.Secure || ck.Path != "/" || ck.SameSite != http.SameSiteLaxMode || ck.Domain != "" {
			t.Fatalf("cookie %+v", ck)
		}
		// Signed in, with a personal space, and setup is over for good.
		s := c.do("GET", "/api/session", nil).want(t, 200, "")
		if s.user()["username"] != "kazuha" || s.body["setup"] != false {
			t.Fatalf("%s", s.raw)
		}
		id := c.personalSpace()
		if st, err := os.Stat(filepath.Join(a.dir, "spaces", id, ".git")); err != nil || !st.IsDir() {
			t.Fatalf("the personal space has no repository: %v", err)
		}
		other := a.newClient("")
		other.do("POST", "/api/setup", good).want(t, 409, "exists")
		if a.auth.SetupToken() != "" {
			t.Fatal("the setup token outlives the first account")
		}
	})
}

func TestConcurrentSetupHasOneWinner(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tok := a.auth.SetupToken()
		const n = 8
		var wg sync.WaitGroup
		codes := make([]int, n)
		for i := range codes {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// Different addresses: the point is the race, not the rate limit.
				c := a.newClient(fmt.Sprintf("198.51.100.%d", i+1))
				codes[i] = c.do("POST", "/api/setup", map[string]any{"token": tok, "username": fmt.Sprintf("admin%d", i), "password": pw}).code
			}()
		}
		wg.Wait()
		wins, exists := 0, 0
		for _, c := range codes {
			switch c {
			case 201:
				wins++
			case 409:
				exists++
			default:
				t.Errorf("unexpected status %d", c)
			}
		}
		if wins != 1 || exists != n-1 {
			t.Fatalf("codes %v", codes)
		}
		users, _ := store.ListUsers(context.Background(), a.st.Q())
		if len(users) != 1 {
			t.Fatalf("%d users", len(users))
		}
		// Losers must not leave repositories behind.
		entries, _ := os.ReadDir(filepath.Join(a.dir, "spaces"))
		if len(entries) != 1 {
			t.Fatalf("%d space directories for one account", len(entries))
		}
	})
}

func TestLogin(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		a.addUser("tang", pw, "member")
		a.addUser("off", pw, "member")
		offID := ""
		users := admin.do("GET", "/api/users", nil).want(t, 200, "").body["users"].([]any)
		for _, u := range users {
			if u.(map[string]any)["username"] == "off" {
				offID = u.(map[string]any)["id"].(string)
			}
		}
		admin.do("PATCH", "/api/users/"+offID, map[string]any{"disabled": true}).want(t, 200, "")

		c := a.newClient("")
		cases := []map[string]any{
			{"username": "tang", "password": "wrong password here"},
			{"username": "nobody", "password": pw},
			{"username": "off", "password": pw}, // disabled
			{"username": "", "password": ""},
			{"username": "tang", "password": strings.Repeat("x", 5000)},
		}
		var first string
		for i, body := range cases {
			r := c.do("POST", "/api/login", body).want(t, 401, "unauthenticated")
			if i == 0 {
				first = r.raw
			} else if r.raw != first {
				t.Fatalf("answers differ:\n%s\n%s", first, r.raw)
			}
		}
		if len(c.cookies) != 0 {
			t.Fatal("a failed sign-in set a cookie")
		}
		// Case in the username is folded.
		r := c.do("POST", "/api/login", map[string]any{"username": "TANG", "password": pw}).want(t, 200, "")
		if r.user()["username"] != "tang" {
			t.Fatal(r.raw)
		}
		c.do("GET", "/api/account", nil).want(t, 200, "")
	})
}

func TestCookieNameFollowsThePublicURL(t *testing.T) {
	eachAccounts(t, accOpts{public: "http://localhost:8080"}, func(t *testing.T, a *acc) {
		c := a.setup("kazuha", pw)
		if _, ok := c.cookies["petalbook_session"]; !ok || len(c.cookies) != 1 {
			t.Fatalf("%v", c.cookies)
		}
		if c.cookies["petalbook_session"].Secure {
			t.Fatal("a Secure cookie over plain http would never be sent back")
		}
	})
}

func TestSessionLifetimeAndRevocation(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		c := a.setup("kazuha", pw)
		c.do("GET", "/api/account", nil).want(t, 200, "")

		// Idle for 29 days is fine and counts as use; 31 days after that is not.
		a.clock.Advance(29 * 24 * time.Hour)
		c.do("GET", "/api/account", nil).want(t, 200, "")
		a.clock.Advance(29 * 24 * time.Hour)
		c.do("GET", "/api/account", nil).want(t, 200, "")
		a.clock.Advance(29 * 24 * time.Hour)
		// 87 days after sign-in, used every 29 days: still open ...
		c.do("GET", "/api/account", nil).want(t, 200, "")
		// ... but 90 days after sign-in is the end, however recently used.
		a.clock.Advance(4 * 24 * time.Hour)
		c.do("GET", "/api/account", nil).want(t, 401, "unauthenticated")

		c = a.newClient("")
		c.do("POST", "/api/login", map[string]any{"username": "kazuha", "password": pw}).want(t, 200, "")
		a.clock.Advance(31 * 24 * time.Hour)
		c.do("GET", "/api/account", nil).want(t, 401, "unauthenticated")
		// The expired session row is gone, not just ignored.
		var n int
		a.st.Q().QueryRow(context.Background(), `SELECT COUNT(*) FROM sessions`).Scan(&n)
		if n != 0 {
			t.Fatalf("%d session rows left", n)
		}
	})
}

func TestLogoutEndsTheSession(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		c := a.setup("kazuha", pw)
		copyOf := a.newClient("")
		for k, v := range c.cookies {
			copyOf.cookies[k] = v
		}
		c.do("POST", "/api/logout", nil).want(t, 204, "")
		if len(c.cookies) != 0 {
			t.Fatal("the cookie was not cleared")
		}
		// The token itself is dead, not just forgotten by this browser.
		copyOf.do("GET", "/api/account", nil).want(t, 401, "unauthenticated")
		// Logging out when not signed in is fine.
		c.do("POST", "/api/logout", nil).want(t, 204, "")
	})
}

func TestLastUsedIsWrittenOncePerHour(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		c := a.setup("kazuha", pw)
		lastUsed := func() time.Time {
			var ms int64
			if err := a.st.Q().QueryRow(context.Background(), `SELECT last_used FROM sessions`).Scan(&ms); err != nil {
				t.Fatal(err)
			}
			return time.UnixMilli(ms)
		}
		t0 := lastUsed()
		for range 5 {
			a.clock.Advance(10 * time.Minute)
			c.do("GET", "/api/account", nil).want(t, 200, "")
		}
		if got := lastUsed(); !got.Equal(t0) {
			t.Fatalf("last used written within the hour: %v -> %v", t0, got)
		}
		a.clock.Advance(15 * time.Minute) // 65 minutes in
		c.do("GET", "/api/account", nil).want(t, 200, "")
		if got := lastUsed(); !got.After(t0) {
			t.Fatal("last used never written")
		}
	})
}

func TestPasswordChangeEndsOtherSessions(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		one := a.setup("kazuha", pw)
		two := a.newClient("")
		two.do("POST", "/api/login", map[string]any{"username": "kazuha", "password": pw}).want(t, 200, "")
		two.do("GET", "/api/account", nil).want(t, 200, "")

		one.do("POST", "/api/account/password", map[string]any{"current": "not it at all", "new": "a brand new password"}).want(t, 403, "forbidden")
		one.do("POST", "/api/account/password", map[string]any{"new": "a brand new password"}).want(t, 400, "invalid")
		one.do("POST", "/api/account/password", map[string]any{"current": pw, "new": "short"}).want(t, 400, "invalid")
		one.do("POST", "/api/account/password", map[string]any{"current": pw, "new": "a brand new password"}).want(t, 204, "")

		one.do("GET", "/api/account", nil).want(t, 200, "") // the one that asked stays signed in
		two.do("GET", "/api/account", nil).want(t, 401, "unauthenticated")
		c := a.newClient("")
		c.do("POST", "/api/login", map[string]any{"username": "kazuha", "password": pw}).want(t, 401, "")
		c.do("POST", "/api/login", map[string]any{"username": "kazuha", "password": "a brand new password"}).want(t, 200, "")
	})
}

func TestAccountProfile(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		c := a.setup("kazuha", pw)
		r := c.do("GET", "/api/account", nil).want(t, 200, "")
		if r.body["hasPassword"] != true || len(r.body["identities"].([]any)) != 0 {
			t.Fatal(r.raw)
		}
		r = c.do("PATCH", "/api/account", map[string]any{"name": "Kazuha Z", "email": "k@example.test"}).want(t, 200, "")
		if r.user()["name"] != "Kazuha Z" || r.user()["email"] != "k@example.test" {
			t.Fatal(r.raw)
		}
		// The change is visible at once (the user cache was dropped).
		if c.do("GET", "/api/session", nil).user()["name"] != "Kazuha Z" {
			t.Fatal("stale user")
		}
		r = c.do("PATCH", "/api/account", map[string]any{"email": ""}).want(t, 200, "")
		if r.user()["name"] != "Kazuha Z" || r.user()["email"] != "" {
			t.Fatal(r.raw)
		}
		c.do("PATCH", "/api/account", map[string]any{"name": " "}).want(t, 400, "invalid")
		c.do("PATCH", "/api/account", map[string]any{"email": "not an address"}).want(t, 400, "invalid")
		c.do("PATCH", "/api/account", map[string]any{"name": "a\nb"}).want(t, 400, "invalid")
	})
}

func TestInvites(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		member := a.addUser("tang", pw, "member")
		member.do("POST", "/api/invites", nil).want(t, 403, "forbidden")
		member.do("GET", "/api/invites", nil).want(t, 403, "forbidden")
		member.do("GET", "/api/users", nil).want(t, 403, "forbidden")

		r := admin.do("POST", "/api/invites", map[string]any{"role": "member", "days": 7}).want(t, 201, "")
		token, id := r.body["token"].(string), r.body["id"].(string)
		if len(token) < 40 || r.body["role"] != "member" || r.body["created"] == nil || r.body["expires"] == nil {
			t.Fatal(r.raw)
		}
		// Only a hash is kept.
		var n int
		a.st.Q().QueryRow(context.Background(), `SELECT COUNT(*) FROM invites WHERE token_hash = ?`, token).Scan(&n)
		if n != 0 {
			t.Fatal("the raw token is stored")
		}
		l := admin.do("GET", "/api/invites", nil).want(t, 200, "").body["invites"].([]any)
		if len(l) != 1 || l[0].(map[string]any)["id"] != id || l[0].(map[string]any)["createdBy"] == nil || l[0].(map[string]any)["token"] != nil {
			t.Fatalf("%v", l)
		}
		admin.do("POST", "/api/invites", map[string]any{"days": 31}).want(t, 400, "invalid")
		admin.do("POST", "/api/invites", map[string]any{"days": 0}).want(t, 400, "invalid")
		admin.do("POST", "/api/invites", map[string]any{"days": -1}).want(t, 400, "invalid")
		admin.do("POST", "/api/invites", map[string]any{"days": nil}).want(t, 201, "")
		admin.do("POST", "/api/invites", map[string]any{"role": "root"}).want(t, 400, "invalid")

		anon := a.newClient("")
		anon.do("POST", "/api/invites/check", map[string]any{"token": "wrong"}).want(t, 404, "not_found")
		ck := anon.do("POST", "/api/invites/check", map[string]any{"token": token}).want(t, 200, "")
		if ck.body["role"] != "member" {
			t.Fatal(ck.raw)
		}
		// A taken username leaves the invite usable.
		anon.do("POST", "/api/invites/accept", map[string]any{"token": token, "username": "tang", "password": pw}).want(t, 409, "exists")
		anon.do("POST", "/api/invites/check", map[string]any{"token": token}).want(t, 200, "")
		anon.do("POST", "/api/invites/accept", map[string]any{"token": token, "username": "new one", "password": pw}).want(t, 400, "invalid")
		acc := anon.do("POST", "/api/invites/accept", map[string]any{"token": token, "username": "Lin", "name": "Lin", "password": pw}).want(t, 201, "")
		if acc.user()["role"] != "member" || acc.user()["username"] != "lin" {
			t.Fatal(acc.raw)
		}
		anon.personalSpace()
		// It worked once.
		other := a.newClient("")
		other.do("POST", "/api/invites/accept", map[string]any{"token": token, "username": "again", "password": pw}).want(t, 404, "not_found")
		other.do("POST", "/api/invites/check", map[string]any{"token": token}).want(t, 404, "not_found")
		if l := admin.do("GET", "/api/invites", nil).body["invites"].([]any); len(l) != 1 { // the one made with days: null
			t.Fatalf("a used invite is listed: %v", l)
		}

		// Expiry and revocation.
		r = admin.do("POST", "/api/invites", map[string]any{"days": 1}).want(t, 201, "")
		a.clock.Advance(25 * time.Hour)
		other.do("POST", "/api/invites/check", map[string]any{"token": r.body["token"]}).want(t, 404, "not_found")
		r = admin.do("POST", "/api/invites", nil).want(t, 201, "") // defaults: member, 7 days
		if r.body["role"] != "member" {
			t.Fatal(r.raw)
		}
		admin.do("DELETE", "/api/invites/"+r.body["id"].(string), nil).want(t, 204, "")
		admin.do("DELETE", "/api/invites/"+r.body["id"].(string), nil).want(t, 404, "not_found")
		other.do("POST", "/api/invites/check", map[string]any{"token": r.body["token"]}).want(t, 404, "not_found")
	})
}

func TestUsersAdministration(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		users := admin.do("GET", "/api/users", nil).want(t, 200, "").body["users"].([]any)
		if len(users) != 2 || users[0].(map[string]any)["username"] != "kazuha" {
			t.Fatalf("%v", users)
		}
		ids := map[string]string{}
		for _, u := range users {
			m := u.(map[string]any)
			ids[m["username"].(string)] = m["id"].(string)
		}
		// The only administrator can neither disable nor demote themself.
		admin.do("PATCH", "/api/users/"+ids["kazuha"], map[string]any{"disabled": true}).want(t, 409, "conflict")
		admin.do("PATCH", "/api/users/"+ids["kazuha"], map[string]any{"role": "member"}).want(t, 409, "conflict")
		admin.do("PATCH", "/api/users/nope", map[string]any{"disabled": true}).want(t, 404, "not_found")
		admin.do("PATCH", "/api/users/"+ids["tang"], map[string]any{"role": "root"}).want(t, 400, "invalid")
		admin.do("PATCH", "/api/users/"+ids["tang"], map[string]any{}).want(t, 200, "")

		// Promote tang: now either may demote the other, but not both.
		r := admin.do("PATCH", "/api/users/"+ids["tang"], map[string]any{"role": "admin"}).want(t, 200, "")
		if r.user()["role"] != "admin" {
			t.Fatal(r.raw)
		}
		tang.do("GET", "/api/users", nil).want(t, 200, "") // the role change is live
		// An administrator may not disable themself, even with another around.
		admin.do("PATCH", "/api/users/"+ids["kazuha"], map[string]any{"disabled": true}).want(t, 409, "conflict")
		// But may demote themself when another enabled administrator exists.
		admin.do("PATCH", "/api/users/"+ids["kazuha"], map[string]any{"role": "member"}).want(t, 200, "")
		admin.do("GET", "/api/users", nil).want(t, 403, "forbidden")
		// tang is the last administrator now.
		tang.do("PATCH", "/api/users/"+ids["tang"], map[string]any{"role": "member"}).want(t, 409, "conflict")
		tang.do("PATCH", "/api/users/"+ids["tang"], map[string]any{"disabled": true}).want(t, 409, "conflict")
		// A disabled administrator does not count: tang cannot demote kazuha's replacement
		// into nothing, and disabling the other admin first leaves tang alone.
		tang.do("PATCH", "/api/users/"+ids["kazuha"], map[string]any{"role": "admin"}).want(t, 200, "")
		tang.do("PATCH", "/api/users/"+ids["kazuha"], map[string]any{"disabled": true}).want(t, 200, "")
		admin.do("GET", "/api/account", nil).want(t, 401, "unauthenticated") // disabling ends sessions at once
		tang.do("PATCH", "/api/users/"+ids["tang"], map[string]any{"role": "member"}).want(t, 409, "conflict")
		c := a.newClient("")
		c.do("POST", "/api/login", map[string]any{"username": "kazuha", "password": pw}).want(t, 401, "unauthenticated")
		tang.do("PATCH", "/api/users/"+ids["kazuha"], map[string]any{"disabled": false}).want(t, 200, "")
		c.do("POST", "/api/login", map[string]any{"username": "kazuha", "password": pw}).want(t, 200, "")
	})
}

func TestConcurrentDemotionsKeepAnAdministrator(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		one := a.setup("kazuha", pw)
		two := a.addUser("tang", pw, "admin")
		ids := map[string]string{}
		for _, u := range one.do("GET", "/api/users", nil).body["users"].([]any) {
			m := u.(map[string]any)
			ids[m["username"].(string)] = m["id"].(string)
		}
		var wg sync.WaitGroup
		codes := make([]int, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			codes[0] = one.do("PATCH", "/api/users/"+ids["tang"], map[string]any{"role": "member"}).code
		}()
		go func() {
			defer wg.Done()
			codes[1] = two.do("PATCH", "/api/users/"+ids["kazuha"], map[string]any{"role": "member"}).code
		}()
		wg.Wait()
		n, _ := store.CountEnabledAdmins(context.Background(), a.st.Q())
		if n != 1 {
			t.Fatalf("%d administrators left (statuses %v)", n, codes)
		}
	})
}

func TestIdentityUnlinkRefusedWhenItIsTheOnlyWayIn(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		ctx := context.Background()
		// An SSO-only account: one identity, no password.
		u, err := a.auth.CreateUser(ctx, auth.CreateParams{Username: "sso", Role: "member", Identity: &auth.IdentityParams{Issuer: "https://idp.example", Subject: "s1", Label: "sso@example.test"}})
		if err != nil {
			t.Fatal(err)
		}
		other, err := a.auth.CreateUser(ctx, auth.CreateParams{Username: "pw", Role: "member", Password: pw})
		if err != nil {
			t.Fatal(err)
		}
		_ = other
		tok := mustSession(t, a, u.ID)
		c := a.newClient("")
		c.cookies[a.auth.SessionCookieName()] = tok
		r := c.do("GET", "/api/account", nil).want(t, 200, "")
		ids := r.body["identities"].([]any)
		if len(ids) != 1 || r.body["hasPassword"] != false {
			t.Fatal(r.raw)
		}
		idn := ids[0].(map[string]any)
		if idn["label"] != "sso@example.test" || idn["provider"] != "idp.example" {
			t.Fatal(r.raw)
		}
		path := "/api/account/identities/" + idn["id"].(string)
		c.do("DELETE", path, nil).want(t, 409, "conflict")
		// Someone else's identity id is not found.
		pwc := a.newClient("")
		pwc.do("POST", "/api/login", map[string]any{"username": "pw", "password": pw}).want(t, 200, "")
		pwc.do("DELETE", path, nil).want(t, 404, "not_found")
		// With a password set (no current needed: there is none) it can go.
		c.do("POST", "/api/account/password", map[string]any{"current": "x", "new": "a first password"}).want(t, 400, "invalid")
		c.do("POST", "/api/account/password", map[string]any{"new": "a first password"}).want(t, 204, "")
		c.do("DELETE", path, nil).want(t, 204, "")
		if r := c.do("GET", "/api/account", nil).want(t, 200, ""); len(r.body["identities"].([]any)) != 0 || r.body["hasPassword"] != true {
			t.Fatal(r.raw)
		}
	})
}

// mustSession starts a session for a user without a password, as sign-in
// through an identity provider does, by writing the row the way the service
// would: only the hash of the token is stored.
func mustSession(t *testing.T, a *acc, userID string) *http.Cookie {
	t.Helper()
	token := "test-token-" + userID
	sum := sha256.Sum256([]byte(token))
	now := a.clock.Now()
	err := store.InsertSession(context.Background(), a.st.Q(), store.Session{ID: hex.EncodeToString(sum[:]), UserID: userID, Created: now, LastUsed: now})
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: a.auth.SessionCookieName(), Value: token}
}

func TestConcurrentAcceptsOfOneInviteHaveOneWinner(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		token := admin.do("POST", "/api/invites", nil).want(t, 201, "").body["token"].(string)
		const n = 6
		var wg sync.WaitGroup
		codes := make([]int, n)
		for i := range codes {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c := a.newClient(fmt.Sprintf("198.51.100.%d", i+10))
				codes[i] = c.do("POST", "/api/invites/accept", map[string]any{"token": token, "username": fmt.Sprintf("new%d", i), "password": pw}).code
			}()
		}
		wg.Wait()
		wins := 0
		for _, c := range codes {
			switch c {
			case 201:
				wins++
			case 404:
			default:
				t.Errorf("status %d", c)
			}
		}
		if wins != 1 {
			t.Fatalf("codes %v", codes)
		}
		if len(mustUsers(t, a)) != 2 || len(listDir(t, a.dir+"/spaces")) != 2 {
			t.Fatalf("%d users, spaces %v", len(mustUsers(t, a)), listDir(t, a.dir+"/spaces"))
		}
	})
}

func TestChangesMadeByAnotherProcessReachTheCacheWithinItsLifetime(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		ctx := context.Background()
		a.setup("kazuha", pw)
		c := a.addUser("tang", pw, "member")
		c.do("GET", "/api/account", nil).want(t, 200, "") // now cached
		// The command-line tool ends the sessions and disables the user behind the server's back.
		if _, err := a.st.Q().Exec(ctx, `DELETE FROM sessions`); err != nil {
			t.Fatal(err)
		}
		c.do("GET", "/api/account", nil).want(t, 200, "") // still served from memory ...
		a.clock.Advance(5*time.Minute + time.Second)
		c.do("GET", "/api/account", nil).want(t, 401, "unauthenticated") // ... but not for longer than the cache's lifetime

		c = a.addUser("lin", pw, "member")
		c.do("GET", "/api/account", nil).want(t, 200, "")
		if err := store.SetDisabled(ctx, a.st.Q(), mustUserID(t, a, "lin"), true); err != nil {
			t.Fatal(err)
		}
		a.clock.Advance(5*time.Minute + time.Second)
		c.do("GET", "/api/account", nil).want(t, 401, "unauthenticated")
	})
}
