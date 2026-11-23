package api_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/auth"
	"github.com/KKazuhaK/PetalBook/internal/auth/testidp"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

func TestSecuritySettingsAreValidatedBeforeAnythingIsStored(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		sec := admin.do("GET", "/api/admin/security", nil).want(t, 200, "")
		if sec.body["login"].(map[string]any)["mode"] != "dual" || sec.body["login"].(map[string]any)["ssoAvailable"] != false || sec.body["login"].(map[string]any)["effectiveMode"] != "local_only" ||
			sec.body["lockout"].(map[string]any)["minutes"] != float64(15) || sec.body["lockout"].(map[string]any)["scope"] != "ip_account" || sec.body["passwordAdmins"] != float64(1) ||
			sec.body["limits"].(map[string]any)["maxFailures"] != float64(10) || sec.body["limits"].(map[string]any)["windowMinutes"] != float64(15) ||
			sec.body["limits"].(map[string]any)["sessionIdleDays"] != float64(30) || sec.body["limits"].(map[string]any)["sessionMaxDays"] != float64(90) {
			t.Fatal(sec.raw)
		}
		ctxs := sec.body["captcha"].(map[string]any)["contexts"].(map[string]any)
		if len(ctxs) != 3 || ctxs["login"].(map[string]any)["when"] != "after_failures" || ctxs["login"].(map[string]any)["failures"] != float64(3) || ctxs["login"].(map[string]any)["on"] != false {
			t.Fatal(sec.raw)
		}
		// Everything is checked first: one bad field refuses the whole request, and a value out of range is not clamped.
		for name, body := range map[string]map[string]any{
			"mode":        {"login": map[string]any{"mode": "magic"}},
			"scope":       {"lockout": map[string]any{"scope": "world"}},
			"minutes":     {"lockout": map[string]any{"minutes": 0}},
			"minutes max": {"lockout": map[string]any{"minutes": 10081}},
			"failures":    {"limits": map[string]any{"maxFailures": 0}},
			"perMinute":   {"limits": map[string]any{"perMinute": -1}},
			"window":      {"limits": map[string]any{"windowMinutes": 99999}},
			"idle":        {"limits": map[string]any{"sessionIdleDays": 366}},
			"max < idle":  {"limits": map[string]any{"sessionIdleDays": 60, "sessionMaxDays": 30}},
			"context":     {"captcha": map[string]any{"contexts": map[string]any{"nowhere": map[string]any{"on": true}}}},
			"reserved":    {"captcha": map[string]any{"contexts": map[string]any{"forgot": map[string]any{"on": true}}}},
			"when":        {"captcha": map[string]any{"contexts": map[string]any{"login": map[string]any{"when": "sometimes"}}}},
			"n failures":  {"captcha": map[string]any{"contexts": map[string]any{"login": map[string]any{"failures": 101}}}},
			"role":        {"passkeys": map[string]any{"requireRoles": []string{"root"}}},
		} {
			// One good field next to the bad one must not be stored either.
			body["login"] = mergeMaps(body["login"], map[string]any{"ssoOnly": false})
			body["lockout"] = mergeMaps(body["lockout"], map[string]any{"enabled": true})
			r := admin.do("PUT", "/api/admin/security", body)
			if r.code != 400 || r.body["error"] != "invalid" {
				t.Errorf("%s: %d %s", name, r.code, r.raw)
			}
		}
		if g := admin.do("GET", "/api/admin/security", nil).want(t, 200, ""); g.body["lockout"].(map[string]any)["enabled"] != false || g.body["limits"].(map[string]any)["sessionIdleDays"] != float64(30) {
			t.Fatalf("a refused request changed something: %s", g.raw)
		}
		// A partial update keeps the rest; only a change writes the audit log.
		before := len(admin.audit(t, "security_changed"))
		r := admin.putSecurity(t, map[string]any{"lockout": map[string]any{"enabled": true, "minutes": 5, "scope": "account"}})
		lk := r.body["lockout"].(map[string]any)
		if lk["enabled"] != true || lk["minutes"] != float64(5) || lk["scope"] != "account" || r.body["limits"].(map[string]any)["maxFailures"] != float64(10) {
			t.Fatal(r.raw)
		}
		admin.putSecurity(t, map[string]any{"lockout": map[string]any{"enabled": true}})
		rows := admin.audit(t, "security_changed")
		if len(rows) != before+1 {
			t.Fatalf("%d rows, want %d: an unchanged save wrote one", len(rows), before+1)
		}
		d := rows[0]["detail"].(map[string]any)
		if d["to"].(map[string]any)["lockout.scope"] != "account" || d["from"].(map[string]any)["lockout.scope"] != "ip_account" || len(d["to"].(map[string]any)) != 3 {
			t.Fatalf("%v", d)
		}
		a.newClient("").do("PUT", "/api/admin/security", map[string]any{}).want(t, 401, "unauthenticated")
	})
}

func mergeMaps(a any, b map[string]any) map[string]any {
	out := map[string]any{}
	if m, ok := a.(map[string]any); ok {
		for k, v := range m {
			out[k] = v
		}
	}
	for k, v := range b {
		if _, ok := out[k]; !ok {
			out[k] = v
		}
	}
	return out
}

func TestSessionLifetimeIsASecuritySetting(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		admin.putSecurity(t, map[string]any{"limits": map[string]any{"sessionIdleDays": 2, "sessionMaxDays": 5}})
		a.clock.Advance(49 * time.Hour)
		tang.do("GET", "/api/account", nil).want(t, 401, "unauthenticated") // applies to sessions that exist already
		admin.do("POST", "/api/login", good("kazuha")).want(t, 200, "")
		for range 4 { // used every day, and ended at the maximum
			a.clock.Advance(36 * time.Hour)
			admin.do("GET", "/api/account", nil)
		}
		admin.do("GET", "/api/account", nil).want(t, 401, "unauthenticated")
	})
}

// While ssoOnly is stored, an enabled administrator with a password is kept
// even when the setting is not in effect (local_only), so switching the page
// to the providers, or enabling one, cannot lock everyone out.
func TestSSOOnlyKeepsAPasswordAdminWhileNotInEffect(t *testing.T) {
	t.Parallel()
	f := newFakeIDP(t)
	eachAccounts(t, f.opts(false), func(t *testing.T, a *acc) {
		c := a.newClient("")
		at := f.begin(t, c, "alice", map[string]any{"intent": "setup", "token": a.auth.SetupToken()})
		finish(t, c, at.cb)
		boss := a.addUser("boss", pw, "admin")
		bossID := mustUserID(t, a, "boss")
		c.putSecurity(t, map[string]any{"login": map[string]any{"ssoOnly": true, "mode": "local_only"}})
		// Not in effect, but the only administrator with a password stays one.
		boss.do("PATCH", "/api/users/"+bossID, map[string]any{"role": "member"}).want(t, 409, "conflict")
		c.do("PATCH", "/api/users/"+bossID, map[string]any{"disabled": true}).want(t, 409, "conflict")
		// A database that has no such administrator anyway (made outside the API).
		ctx := context.Background()
		setRole := func(role string) {
			if err := store.SetRole(ctx, a.st.Q(), bossID, role); err != nil {
				t.Fatal(err)
			}
		}
		setRole("member")
		// Leaving local_only would put ssoOnly in effect: refused.
		c.do("PUT", "/api/admin/security", map[string]any{"login": map[string]any{"mode": "dual"}}).want(t, 400, "local_admin_required")
		// Enabling a provider would too.
		setRole("admin")
		c.do("PATCH", "/api/admin/auth/providers/sso", map[string]any{"enabled": false}).want(t, 200, "")
		c.putSecurity(t, map[string]any{"login": map[string]any{"mode": "dual"}})
		setRole("member")
		c.do("PATCH", "/api/admin/auth/providers/sso", map[string]any{"enabled": true}).want(t, 400, "local_admin_required")
		setRole("admin")
		c.do("PATCH", "/api/admin/auth/providers/sso", map[string]any{"enabled": true}).want(t, 200, "")
	})
}

func TestSSOOnlyIsCheckedWhenSavedAndAfterThePassword(t *testing.T) {
	t.Parallel()
	f := newFakeIDP(t)
	eachAccounts(t, f.opts(false), func(t *testing.T, a *acc) {
		// The first administrator has no password: nobody can use the way out.
		c := a.newClient("")
		at := f.begin(t, c, "alice", map[string]any{"intent": "setup", "token": a.auth.SetupToken()})
		finish(t, c, at.cb)
		c.do("PUT", "/api/admin/security", map[string]any{"login": map[string]any{"ssoOnly": true}}).want(t, 400, "local_admin_required")
		// A second administrator with a password makes it possible.
		boss := a.addUser("boss", pw, "admin")
		tang := a.addUser("tang", pw, "member")
		_ = tang
		sec := c.putSecurity(t, map[string]any{"login": map[string]any{"ssoOnly": true}})
		lg := sec.body["login"].(map[string]any)
		if lg["ssoOnly"] != true || lg["ssoOnlyInEffect"] != true || lg["effectiveMode"] != "dual" || sec.body["passwordAdmins"] != float64(1) {
			t.Fatal(sec.raw)
		}
		// Public answers never mention it.
		pub := a.newClient("").do("GET", "/api/auth/providers", nil).want(t, 200, "")
		sess := a.newClient("").do("GET", "/api/session", nil).want(t, 200, "")
		for _, raw := range []string{pub.raw, sess.raw} {
			if l := strings.ToLower(raw); strings.Contains(l, "ssoonly") || strings.Contains(l, "sso_only") || strings.Contains(l, "local_login") {
				t.Fatal(raw)
			}
		}
		// A member's right password is refused after it was checked; a wrong one is the ordinary 401.
		m := a.newClient("203.0.113.5")
		m.do("POST", "/api/login", bad("tang")).want(t, 401, "unauthenticated")
		m.do("POST", "/api/login", good("tang")).want(t, 403, "local_login_refused")
		if signedInAs(t, m) != "" {
			t.Fatal("signed in")
		}
		// Administrators are never refused.
		a.newClient("").do("POST", "/api/login", good("boss")).want(t, 200, "")
		// A member invite cannot be accepted with a password; an administrator invite can.
		inv := boss.do("POST", "/api/invites", map[string]any{"role": "member"}).want(t, 201, "")
		n := a.newClient("")
		n.do("POST", "/api/invites/accept", map[string]any{"token": inv.body["token"], "username": "newbie", "password": pw}).want(t, 403, "local_login_refused")
		n.do("POST", "/api/invites/check", map[string]any{"token": inv.body["token"]}).want(t, 200, "") // the invite was not used up
		adminInv := boss.do("POST", "/api/invites", map[string]any{"role": "admin"}).want(t, 201, "")
		n.do("POST", "/api/invites/accept", map[string]any{"token": adminInv.body["token"], "username": "newadmin", "password": pw}).want(t, 201, "")
		// Changing a role or disabling must leave an administrator with a password.
		bossID, aliceID := mustUserID(t, a, "boss"), mustUserID(t, a, "alice")
		_ = aliceID
		newAdmin := mustUserID(t, a, "newadmin")
		boss.do("PATCH", "/api/users/"+newAdmin, map[string]any{"role": "member"}).want(t, 200, "") // boss is still one
		boss.do("PATCH", "/api/users/"+bossID, map[string]any{"role": "member"}).want(t, 409, "conflict")
		c.do("PATCH", "/api/users/"+bossID, map[string]any{"disabled": true}).want(t, 409, "conflict")
		// With the sign-in page not offering the provider the setting means nothing, so passwords work again.
		boss.putSecurity(t, map[string]any{"login": map[string]any{"mode": "local_only"}})
		sec = boss.do("GET", "/api/admin/security", nil).want(t, 200, "")
		if lg := sec.body["login"].(map[string]any); lg["ssoOnly"] != true || lg["ssoOnlyInEffect"] != false || lg["effectiveMode"] != "local_only" || lg["mode"] != "local_only" {
			t.Fatal(sec.raw)
		}
		a.newClient("203.0.113.6").do("POST", "/api/login", good("tang")).want(t, 200, "")
		boss.putSecurity(t, map[string]any{"login": map[string]any{"mode": "sso_redirect"}})
		a.newClient("203.0.113.7").do("POST", "/api/login", good("tang")).want(t, 403, "local_login_refused")
		// No enabled provider: the page says local_only whatever is stored.
		boss.do("PATCH", "/api/admin/auth/providers/sso", map[string]any{"enabled": false}).want(t, 200, "")
		pub = a.newClient("").do("GET", "/api/auth/providers", nil).want(t, 200, "")
		if pub.body["loginMode"] != "local_only" || pub.body["local"] != true || pub.body["sso"] != false || len(pub.body["providers"].([]any)) != 0 {
			t.Fatal(pub.raw)
		}
		a.newClient("203.0.113.8").do("POST", "/api/login", good("tang")).want(t, 200, "")
		// The four modes and what the page may draw.
		boss.do("PATCH", "/api/admin/auth/providers/sso", map[string]any{"enabled": true}).want(t, 200, "")
		for mode, want := range map[string][2]bool{"dual": {true, true}, "sso_first": {true, true}, "sso_redirect": {false, true}, "local_only": {true, false}} {
			boss.putSecurity(t, map[string]any{"login": map[string]any{"mode": mode}})
			pub = a.newClient("").do("GET", "/api/auth/providers", nil).want(t, 200, "")
			if pub.body["loginMode"] != mode || pub.body["local"] != want[0] || pub.body["sso"] != want[1] {
				t.Errorf("%s: %s", mode, pub.raw)
			}
		}
		// Audit: refusals are recorded with their reason.
		found := false
		for _, row := range boss.audit(t, "login_failed") {
			found = found || row["detail"].(map[string]any)["reason"] == "local_login_refused"
		}
		if !found {
			t.Fatal("the refusal is not in the audit log")
		}
	})
}

func TestSSOOnlyRefusesPasskeysOfMembers(t *testing.T) {
	t.Parallel()
	f := newFakeIDP(t)
	eachAccounts(t, f.opts(false), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		tk, ak := newVKey(mustUserID(t, a, "tang")), newVKey(mustUserID(t, a, "kazuha"))
		tang.register(t, tk, "k").want(t, 201, "")
		admin.register(t, ak, "k").want(t, 201, "")
		admin.putSecurity(t, map[string]any{"login": map[string]any{"ssoOnly": true}})
		tk.cred.Counter++
		a.newClient("").signInWithPasskey(t, tk, "", "").want(t, 403, "local_login_refused")
		ak.cred.Counter++
		a.newClient("").signInWithPasskey(t, ak, "", "").want(t, 200, "") // administrators are not refused
		admin.putSecurity(t, map[string]any{"login": map[string]any{"ssoOnly": false}})
		tk.cred.Counter++
		a.newClient("").signInWithPasskey(t, tk, "", "").want(t, 200, "")
	})
}

func TestSignInRequiresNoCaptchaForSingleSignOn(t *testing.T) {
	t.Parallel()
	f := newFakeIDP(t)
	eachAccounts(t, f.opts(true), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		admin.putSecurity(t, map[string]any{"captcha": map[string]any{"contexts": map[string]any{"login": map[string]any{"on": true, "when": "always"}}}})
		c := a.newClient("")
		if loc := f.signIn(t, c, "alice", map[string]any{}); loc != "/" || signedInAs(t, c) != "alice" {
			t.Fatal(loc)
		}
	})
}

func TestAuditLogIsPagedFilteredAndTrimmed(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		for i := range 7 {
			a.clock.Advance(time.Second)
			a.newClient(fmt.Sprintf("203.0.113.%d", i+10)).do("POST", "/api/login", bad(fmt.Sprintf("name%d", i))).want(t, 401, "")
		}
		var seen []string
		cursor := ""
		for {
			p := "/api/admin/audit?kind=login_failed&limit=3"
			if cursor != "" {
				p += "&cursor=" + cursor
			}
			r := admin.do("GET", p, nil).want(t, 200, "")
			for _, it := range r.body["items"].([]any) {
				m := it.(map[string]any)
				seen = append(seen, m["username"].(string))
				if m["kind"] != "login_failed" || m["ip"] == "" || m["at"] == "" || m["id"] == "" {
					t.Fatalf("%v", m)
				}
			}
			if r.body["next"] == nil {
				break
			}
			cursor = r.body["next"].(string)
		}
		if strings.Join(seen, ",") != "name6,name5,name4,name3,name2,name1,name0" {
			t.Fatalf("%v", seen)
		}
		uid := mustUserID(t, a, "kazuha")
		if r := admin.do("GET", "/api/admin/audit?user="+uid, nil).want(t, 200, ""); len(r.body["items"].([]any)) == 0 {
			t.Fatal("no rows for the user")
		}
		for _, bad := range []string{"limit=0", "limit=201", "limit=x"} {
			admin.do("GET", "/api/admin/audit?"+bad, nil).want(t, 400, "invalid")
		}
		// 90 days, and a limit of rows.
		ctx := context.Background()
		if n, err := store.TrimAudit(ctx, a.st.Q(), a.clock.Now().Add(24*time.Hour), 100_000); err != nil || n == 0 {
			t.Fatalf("%d %v", n, err)
		}
		a.auth.Audit(auth.AuditEvent{Kind: "login_failed", Username: "after"})
		a.auth.FlushAudit(ctx)
		for i := range 20 {
			a.auth.Audit(auth.AuditEvent{Kind: "login", Username: fmt.Sprint("u", i)})
		}
		a.auth.FlushAudit(ctx)
		if n, err := store.TrimAudit(ctx, a.st.Q(), time.Time{}, 5); err != nil || n < 15 {
			t.Fatalf("%d %v", n, err)
		}
		rows, err := store.ListAudit(ctx, a.st.Q(), store.AllTenants(), false, "", "", "", 100)
		if err != nil || len(rows) != 5 {
			t.Fatalf("%d %v", len(rows), err)
		}
	})
}

func TestSettingsChangedByAnotherProcessReachTheRunningServer(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		admin.putSecurity(t, map[string]any{"limits": map[string]any{"maxFailures": 3}, "lockout": map[string]any{"enabled": true}, "login": map[string]any{"mode": "sso_first"}})
		// "petalbook security reset" is a second process writing the same row.
		other, err := auth.New(context.Background(), auth.Config{Store: a.st, Spaces: a.mgr, PublicURL: a.public, Password: fastPasswords, Now: a.clock.Now})
		if err != nil {
			t.Fatal(err)
		}
		if err := other.ResetSecurity(context.Background()); err != nil {
			t.Fatal(err)
		}
		g := admin.do("GET", "/api/admin/security", nil).want(t, 200, "")
		if g.body["lockout"].(map[string]any)["enabled"] != true {
			t.Fatal("the change arrived before the cache expired")
		}
		a.clock.Advance(6 * time.Minute) // CacheTTL is 5 minutes
		g = admin.do("GET", "/api/admin/security", nil).want(t, 200, "")
		if g.body["lockout"].(map[string]any)["enabled"] != false || g.body["login"].(map[string]any)["mode"] != "dual" {
			t.Fatal(g.raw)
		}
		// Limits stay as they were; the reset is written down as the command line's.
		if g.body["limits"].(map[string]any)["maxFailures"] != float64(3) {
			t.Fatal(g.raw)
		}
		found := false
		for _, row := range admin.audit(t, "security_changed") {
			found = found || row["actor"] == nil && row["detail"].(map[string]any)["by"] == "cli"
		}
		if !found {
			t.Fatal("the reset is not in the audit log")
		}
	})
}

var _ = testidp.Claims{}
