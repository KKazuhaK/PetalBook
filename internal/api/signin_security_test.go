package api_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/auth/testidp"
	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
)

func TestSecuritySettingsAreValidatedBeforeAnythingIsStored(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		// The defaults: the policy is the site's settings, and what is derived from it
		// (the mode in effect, whether single sign-on is there) is GET /api/admin/security.
		for key, want := range map[string]any{
			"signin.mode": "dual", "signin.lockout.minutes": float64(15), "signin.lockout.scope": "ip_account", "signin.lockout.enabled": false,
			"signin.maxFailures": float64(10), "signin.windowMinutes": float64(15), "sessions.idleDays": float64(30), "sessions.maxDays": float64(90),
		} {
			if got := admin.siteValue(key); got != want {
				t.Errorf("%s: %v, want %v", key, got, want)
			}
		}
		sec := admin.do("GET", "/api/admin/security", nil).want(t, 200, "")
		if sec.body["login"].(map[string]any)["ssoAvailable"] != false || sec.body["login"].(map[string]any)["effectiveMode"] != "local_only" || sec.body["passwordAdmins"] != float64(1) {
			t.Fatal(sec.raw)
		}
		ctxs := admin.siteValue("captcha.contexts").(map[string]any)
		if len(ctxs) != 5 || ctxs["login"].(map[string]any)["when"] != "after_failures" || ctxs["login"].(map[string]any)["failures"] != float64(3) || ctxs["login"].(map[string]any)["on"] != false {
			t.Fatal(jsonText(ctxs))
		}
		// A value out of range or not allowed is refused, and not clamped.
		for _, bad := range []struct {
			key   string
			value any
		}{
			{"signin.mode", "magic"}, {"signin.lockout.scope", "world"}, {"signin.lockout.minutes", 0}, {"signin.lockout.minutes", 10081},
			{"signin.maxFailures", 0}, {"signin.perMinute", -1}, {"signin.windowMinutes", 99999}, {"sessions.idleDays", 366},
			{"sessions.idleDays", 100}, // above the longest a session lasts (90)
			{"captcha.contexts", map[string]any{"nowhere": map[string]any{"on": true}}},
			{"captcha.contexts", map[string]any{"reset": map[string]any{"on": true}}},
			{"captcha.contexts", map[string]any{"login": map[string]any{"when": "sometimes"}}},
			{"captcha.contexts", map[string]any{"login": map[string]any{"failures": 101}}},
			{"signin.passkeyRoles", []string{"root"}},
		} {
			r := admin.putSetting(bad.key, bad.value)
			if r.code != 400 {
				t.Errorf("%s = %v: %d %s", bad.key, bad.value, r.code, r.raw)
			}
		}
		if admin.siteValue("signin.lockout.enabled") != false || admin.siteValue("sessions.idleDays") != float64(30) {
			t.Fatal("a refused request changed something")
		}
		// Each setting written is audited.
		before := len(admin.audit(t, "setting_changed"))
		admin.putSetting("signin.lockout.enabled", true).want(t, 200, "")
		admin.putSetting("signin.lockout.minutes", 5).want(t, 200, "")
		admin.putSetting("signin.lockout.scope", "account").want(t, 200, "")
		if admin.siteValue("signin.lockout.scope") != "account" || admin.siteValue("signin.maxFailures") != float64(10) {
			t.Fatal("a write changes its own setting and no other")
		}
		rows := admin.audit(t, "setting_changed")
		if len(rows) != before+3 {
			t.Fatalf("%d rows, want %d: one for each of the three settings", len(rows), before+3)
		}
		found := false
		for _, row := range rows {
			d := row["detail"].(map[string]any)
			if d["key"] == "signin.lockout.scope" {
				found = d["to"].(map[string]any)["value"] == "account" && d["from"].(map[string]any)["value"] == "ip_account" && d["source"] == "manual"
			}
		}
		if !found {
			t.Fatalf("no row for the scope: %v", rows)
		}
		a.newClient("").do("PUT", "/api/admin/settings/signin.lockout.scope", map[string]any{"value": "ip"}).want(t, 401, "unauthenticated")
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
		admin.putSetting("sessions.idleDays", 2).want(t, 200, "")
		admin.putSetting("sessions.maxDays", 5).want(t, 200, "")
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
		c.putSetting("signin.ssoOnly", true).want(t, 200, "")
		c.putSetting("signin.mode", "local_only").want(t, 200, "")
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
		c.putSetting("signin.mode", "dual").want(t, 400, "local_admin_required")
		// Enabling a provider would too.
		setRole("admin")
		c.do("PATCH", "/api/admin/auth/providers/sso", map[string]any{"enabled": false}).want(t, 200, "")
		c.putSetting("signin.mode", "dual").want(t, 200, "")
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
		c.putSetting("signin.ssoOnly", true).want(t, 400, "local_admin_required")
		// A second administrator with a password makes it possible.
		boss := a.addUser("boss", pw, "admin")
		tang := a.addUser("tang", pw, "member")
		_ = tang
		c.putSetting("signin.ssoOnly", true).want(t, 200, "")
		sec := c.do("GET", "/api/admin/security", nil).want(t, 200, "")
		lg := sec.body["login"].(map[string]any)
		if c.siteValue("signin.ssoOnly") != true || lg["ssoOnlyInEffect"] != true || lg["effectiveMode"] != "dual" || sec.body["passwordAdmins"] != float64(1) {
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
		boss.putSetting("signin.mode", "local_only").want(t, 200, "")
		sec = boss.do("GET", "/api/admin/security", nil).want(t, 200, "")
		if lg := sec.body["login"].(map[string]any); boss.siteValue("signin.ssoOnly") != true || lg["ssoOnlyInEffect"] != false || lg["effectiveMode"] != "local_only" || boss.siteValue("signin.mode") != "local_only" {
			t.Fatal(sec.raw)
		}
		a.newClient("203.0.113.6").do("POST", "/api/login", good("tang")).want(t, 200, "")
		boss.putSetting("signin.mode", "sso_redirect").want(t, 200, "")
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
			boss.putSetting("signin.mode", mode).want(t, 200, "")
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
		admin.putSetting("signin.ssoOnly", true).want(t, 200, "")
		tk.cred.Counter++
		a.newClient("").signInWithPasskey(t, tk, "", "").want(t, 403, "local_login_refused")
		ak.cred.Counter++
		a.newClient("").signInWithPasskey(t, ak, "", "").want(t, 200, "") // administrators are not refused
		admin.putSetting("signin.ssoOnly", false).want(t, 200, "")
		tk.cred.Counter++
		a.newClient("").signInWithPasskey(t, tk, "", "").want(t, 200, "")
	})
}

func TestSignInRequiresNoCaptchaForSingleSignOn(t *testing.T) {
	t.Parallel()
	f := newFakeIDP(t)
	eachAccounts(t, f.opts(true), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		admin.putSetting("captcha.contexts", map[string]any{"login": map[string]any{"on": true, "when": "always"}}).want(t, 200, "")
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
		admin.putSetting("signin.mode", "sso_first").want(t, 200, "")
		admin.putSetting("signin.lockout.enabled", true).want(t, 200, "")
		admin.putSetting("signin.maxFailures", 3).want(t, 200, "")
		// "petalbook security reset" is a second process writing the same rows: its
		// own settings service, which the running server learns of through
		// settings_rev, at most once a minute.
		other := settings.New(a.st)
		for _, w := range []struct {
			key string
			v   any
		}{{settings.SigninLockoutEnabled, false}, {settings.SigninMode, "dual"}} {
			if _, err := other.SetSite(context.Background(), w.key, settings.Change{HasValue: true, Value: w.v, By: "cli"}); err != nil {
				t.Fatal(err)
			}
		}
		if admin.siteValue("signin.lockout.enabled") != true {
			t.Fatal("the change arrived before the revision was looked at")
		}
		a.clock.Advance(settings.RevCheck + time.Second)
		if admin.siteValue("signin.lockout.enabled") != false || admin.siteValue("signin.mode") != "dual" {
			t.Fatal("the change of the other process was not seen")
		}
		// Limits stay as they were.
		if admin.siteValue("signin.maxFailures") != float64(3) {
			t.Fatal("a setting the other process did not write moved")
		}
	})
}

var _ = testidp.Claims{}
