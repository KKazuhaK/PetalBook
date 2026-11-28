package api_test

import (
	"testing"
	"time"

	"petalbook.app/server/internal/auth"
)

// The sign-in policy an organisation decides inside what the site allows:
// sessions.*, signin.passkeyRoles, captcha.contexts and signin.mode have a
// tenant layer, and what enforces them asks for the tenant of the person
// concerned. The fixture is two organisations (acme and beta) besides the
// default tenant (root's), each with a member.
type tlFx struct {
	*domFx
	mB, mC, mD *client // a member of acme, of beta and of the default tenant
}

func newTLFx(t *testing.T, a *acc) *tlFx {
	t.Helper()
	f := &tlFx{domFx: newDomFx(t, a)}
	member := func(adm *client, username string) *client {
		inv := adm.do("POST", "/api/invites", map[string]any{"role": "member"}).want(t, 201, "")
		c := a.newClient("")
		c.do("POST", "/api/invites/accept", map[string]any{"token": inv.body["token"], "username": username, "name": username, "password": pw}).want(t, 201, "")
		return c
	}
	f.mB, f.mC, f.mD = member(f.admB, "mb"), member(f.admC, "mc"), member(f.root, "md")
	return f
}

// tset writes a tenant's own value as its administrator.
func (f *domFx) tset(c *client, tenant, key string, v any) result {
	return c.do("PUT", "/api/admin/tenants/"+tenant+"/settings/"+key, map[string]any{"value": v})
}

// lowerOf sets the site's rule for the layer below.
func (f *domFx) lowerOf(t *testing.T, key string, lower map[string]any) {
	t.Helper()
	f.root.do("PUT", "/api/admin/settings/"+key, map[string]any{"lower": lower}).want(t, 200, "")
}

func sessionMaxAge(r result, name string) int {
	for _, ck := range r.resp.Cookies() {
		if ck.Name == name {
			return ck.MaxAge
		}
	}
	return 0
}

func TestATenantsSessionLifetimesApplyToItsPeople(t *testing.T) {
	t.Parallel()
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newTLFx(t, a)
		f.tset(f.admB, f.tenB, "sessions.idleDays", 6).want(t, 200, "") // never more than the lifetime when read
		f.tset(f.admB, f.tenB, "sessions.maxDays", 5).want(t, 200, "")
		// Not above the maximum, and not above what the site allows.
		f.tset(f.admB, f.tenB, "sessions.idleDays", 2).want(t, 200, "")
		f.tset(f.admB, f.tenB, "sessions.maxDays", 120).want(t, 409, "setting_restricted")
		// A new session lasts as long as its person's tenant says.
		day := 24 * 60 * 60
		login := func(user string) result {
			return a.newClient("").do("POST", "/api/login", good(user))
		}
		if got := sessionMaxAge(login("mb"), a.auth.SessionCookieName()); got != 5*day {
			t.Fatalf("acme's cookie lasts %d seconds", got)
		}
		if got := sessionMaxAge(login("mc"), a.auth.SessionCookieName()); got != 90*day {
			t.Fatalf("beta's cookie lasts %d seconds", got)
		}
		if got := sessionMaxAge(login("md"), a.auth.SessionCookieName()); got != 90*day {
			t.Fatalf("the default tenant's cookie lasts %d seconds", got)
		}
		// Idle for two days: acme's people are out; nobody else is.
		a.clock.Advance(49 * time.Hour)
		f.mB.do("GET", "/api/account", nil).want(t, 401, "unauthenticated")
		f.admB.do("GET", "/api/account", nil).want(t, 401, "unauthenticated")
		f.mC.do("GET", "/api/account", nil).want(t, 200, "")
		f.mD.do("GET", "/api/account", nil).want(t, 200, "")
		f.root.do("GET", "/api/account", nil).want(t, 200, "")
		// Used every day, acme's session still ends at its maximum.
		mb := a.newClient("")
		mb.do("POST", "/api/login", good("mb")).want(t, 200, "")
		for range 4 {
			a.clock.Advance(36 * time.Hour)
			mb.do("GET", "/api/account", nil)
			f.mC.do("GET", "/api/account", nil).want(t, 200, "")
		}
		mb.do("GET", "/api/account", nil).want(t, 401, "unauthenticated")
		// The sweep keeps by the person's tenant: acme's old sessions go, beta's stay.
		a.auth.Sweep(t.Context())
		f.mC.do("GET", "/api/account", nil).want(t, 200, "")
	})
}

// A site lock or tighten holds the tenant to the site's lifetimes, even for a
// value stored before; allow lets it lengthen them.
func TestASiteHoldsATenantsSessionLifetimes(t *testing.T) {
	t.Parallel()
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newTLFx(t, a)
		// Default: the tenant may only shorten.
		f.tset(f.admB, f.tenB, "sessions.maxDays", 91).want(t, 409, "setting_restricted")
		f.lowerOf(t, "sessions.maxDays", map[string]any{"mode": "allow"})
		f.tset(f.admB, f.tenB, "sessions.maxDays", 200).want(t, 200, "")
		// Allowed, the tenant's people outlast the site's 90 days (the site's own session does not).
		for range 5 {
			a.clock.Advance(20 * 24 * time.Hour)
			f.mB.do("GET", "/api/account", nil).want(t, 200, "")
		}
		f.root = a.newClient("")
		f.root.do("POST", "/api/login", good("root")).want(t, 200, "")
		// The site tightens again: the tenant's longer value is kept, not in effect.
		f.lowerOf(t, "sessions.maxDays", map[string]any{"mode": "tighten"})
		f.mB.do("GET", "/api/account", nil).want(t, 401, "unauthenticated")
		// Locked: the tenant cannot write at all.
		f.lowerOf(t, "sessions.maxDays", map[string]any{"mode": "lock"})
		f.admB = a.newClient("")
		f.admB.do("POST", "/api/login", good("adm-b")).want(t, 200, "")
		f.tset(f.admB, f.tenB, "sessions.maxDays", 30).want(t, 409, "setting_locked")
	})
}

// The settings cache serves the check of every request: once warm, a tenant's
// people cost no read of the settings from the database.
func TestSessionChecksReadTheSettingsFromTheCache(t *testing.T) {
	t.Parallel()
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newTLFx(t, a)
		f.tset(f.admB, f.tenB, "sessions.maxDays", 30).want(t, 200, "")
		f.mB.do("GET", "/api/account", nil).want(t, 200, "")
		f.mC.do("GET", "/api/account", nil).want(t, 200, "")
		computes := auth.SettingsComputes(a.auth)
		for range 50 {
			f.mB.do("GET", "/api/account", nil).want(t, 200, "")
			f.mC.do("GET", "/api/account", nil).want(t, 200, "")
			f.mD.do("GET", "/api/account", nil).want(t, 200, "")
		}
		if n := computes.Load(); n != 0 {
			t.Fatalf("%d reads of the settings from the database in 150 requests", n)
		}
	})
}

func TestATenantsPasskeyRolesApplyToItsPeople(t *testing.T) {
	t.Parallel()
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newTLFx(t, a)
		held := func(user string) bool {
			c := a.newClient("")
			c.do("POST", "/api/login", good(user)).want(t, 200, "")
			return c.do("GET", "/api/session", nil).want(t, 200, "").body["hold"] == "passkey"
		}
		f.tset(f.admB, f.tenB, "signin.passkeyRoles", []string{"nobody"}).want(t, 400, "invalid")
		f.tset(f.admB, f.tenB, "signin.passkeyRoles", []string{"member"}).want(t, 200, "")
		if !held("mb") || held("mc") || held("md") || held("adm-b") {
			t.Fatal("the requirement is acme's members' alone")
		}
		// The person's page says so too.
		mbID := mustUserID(t, a, "mb")
		if f.admB.do("GET", "/api/users/"+mbID, nil).want(t, 200, "").user()["passkeyRequired"] != true {
			t.Fatal("passkeyRequired")
		}
		// The site requires one of administrators: the tenant keeps the site's role and adds its own.
		f.root.putSetting("signin.passkeyRoles", []string{"admin"}).want(t, 200, "")
		f.tset(f.admB, f.tenB, "signin.passkeyRoles", []string{"member"}).want(t, 409, "setting_restricted")
		if held("mb") || !held("adm-b") || !held("adm-c") || held("mc") {
			t.Fatal("the site's role holds the tenant; its own value is kept, not in effect")
		}
		f.tset(f.admB, f.tenB, "signin.passkeyRoles", []string{"admin", "member"}).want(t, 200, "")
		if !held("mb") || !held("adm-b") || held("mc") {
			t.Fatal("the tenant adds to the site's")
		}
		// Locked at the site: the tenant writes nothing.
		f.lowerOf(t, "signin.passkeyRoles", map[string]any{"mode": "lock"})
		f.tset(f.admB, f.tenB, "signin.passkeyRoles", []string{"admin", "member"}).want(t, 409, "setting_locked")
	})
}

func TestATenantsCaptchaAppliesToItsRequests(t *testing.T) {
	t.Parallel()
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newTLFx(t, a)
		srv := fakeMail(t)
		enableMail(t, f.root, srv)
		required := func(c *client, ctx, tenant string) bool {
			path := "/api/captcha?ctx=" + ctx
			if tenant != "" {
				path += "&tenant=" + tenant
			}
			return c.do("GET", path, nil).want(t, 200, "").body["required"] == true
		}
		c := a.newClient("203.0.113.40")
		// The forms an organisation may ask a captcha of; the invitation and the setup are the site's.
		f.tset(f.admB, f.tenB, "captcha.contexts", map[string]any{"invite": map[string]any{"on": true}}).want(t, 400, "invalid")
		f.tset(f.admB, f.tenB, "captcha.contexts", map[string]any{"setup": map[string]any{"on": true}}).want(t, 400, "invalid")
		f.tset(f.admB, f.tenB, "captcha.contexts", map[string]any{
			"login": map[string]any{"on": true, "when": "always"}, "forgot": map[string]any{"on": true, "when": "always"}}).want(t, 200, "")
		// acme's own people are asked; beta's, the default tenant's and an unscoped request are not.
		for _, ctx := range []string{"login", "forgot"} {
			if !required(c, ctx, "acme") || required(c, ctx, "beta") || required(c, ctx, "") {
				t.Fatalf("%s: acme alone is asked", ctx)
			}
		}
		if required(c, "register", "acme") || required(c, "invite", "acme") {
			t.Fatal("a form the tenant left off")
		}
		c.do("GET", "/api/captcha?ctx=login&tenant=nowhere", nil).want(t, 404, "")
		// Signing in, and asking for a reset, are checked by the tenant the request names.
		c.do("POST", "/api/login", map[string]any{"username": "mb", "password": pw, "tenant": "acme"}).want(t, 400, "captcha_failed")
		c.do("POST", "/api/login", map[string]any{"username": "mc", "password": pw, "tenant": "beta"}).want(t, 200, "")
		c.do("POST", "/api/login", map[string]any{"username": "md", "password": pw}).want(t, 200, "")
		forgot(c, "mb", "tenant", "acme").want(t, 400, "captcha_failed")
		forgot(c, "mc", "tenant", "beta").want(t, 202, "")
		// The tenant is the request's, never worked out from the username: acme's person
		// signing in without naming acme is not asked, as the site asks nobody.
		c.do("POST", "/api/login", good("mb")).want(t, 200, "")
		// With the captcha, acme's login passes.
		r := c.do("GET", "/api/captcha?ctx=login&tenant=acme", nil).want(t, 200, "")
		id := r.body["id"].(string)
		c.do("POST", "/api/login", map[string]any{"username": "mb", "password": pw, "tenant": "acme",
			"captcha": map[string]any{"id": id, "answer": auth.CaptchaSolution(a.auth, id)}}).want(t, 200, "")

		// With no tenant named, the site's own value is used (the site asks login always).
		f.root.putSetting("captcha.contexts", map[string]any{"login": map[string]any{"on": true, "when": "always"}}).want(t, 200, "")
		if !required(c, "login", "") || !required(c, "login", "beta") || !required(c, "login", "acme") {
			t.Fatal("the site's value is the tenants' floor, and an unscoped request's")
		}
		// beta turns on another form; the site's own login stays: tighten.
		f.tset(f.admC, f.tenC, "captcha.contexts", map[string]any{"login": map[string]any{"on": false}}).want(t, 409, "setting_restricted")
		f.tset(f.admC, f.tenC, "captcha.contexts", map[string]any{"register": map[string]any{"on": true, "when": "always"}}).want(t, 200, "")
		if !required(c, "register", "beta") || required(c, "register", "acme") || required(c, "register", "") {
			t.Fatal("beta's register alone")
		}
		// acme's after-failures ... is looser than the site's always: refused.
		f.tset(f.admB, f.tenB, "captcha.contexts", map[string]any{"login": map[string]any{"when": "after_failures", "failures": 2}}).want(t, 409, "setting_restricted")
		// A lock at the site: nothing written below.
		f.lowerOf(t, "captcha.contexts", map[string]any{"mode": "lock"})
		f.tset(f.admC, f.tenC, "captcha.contexts", map[string]any{"register": map[string]any{"on": true, "when": "always"}}).want(t, 409, "setting_locked")
	})
}

// A third-party captcha needs the site's keys: an organisation cannot turn one
// on that nobody could pass.
func TestATenantsCaptchaNeedsTheSitesKeysForAThirdParty(t *testing.T) {
	t.Parallel()
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newTLFx(t, a)
		f.root.putSetting("captcha.provider", "turnstile").want(t, 200, "")
		f.tset(f.admB, f.tenB, "captcha.contexts", map[string]any{"login": map[string]any{"on": true, "when": "always"}}).want(t, 400, "captcha_site_key_required")
	})
}

func TestATenantsSigninModeIsItsPagesOwn(t *testing.T) {
	t.Parallel()
	idp := newIDPFor(t, "site-sso")
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newDomFx(t, a)
		// The page shows a mode once the site has a provider.
		f.root.do("POST", "/api/admin/providers", idp.providerBody("site-sso", nil)).want(t, 201, "")
		mode := func(tenant string) any {
			path := "/api/auth/providers"
			if tenant != "" {
				path += "?tenant=" + tenant
			}
			return a.newClient("").do("GET", path, nil).want(t, 200, "").body["loginMode"]
		}
		if mode("") != "dual" || mode("acme") != "dual" {
			t.Fatal("dual by default")
		}
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/signin.mode", map[string]any{"value": "sso_first"}).want(t, 200, "")
		if mode("acme") != "sso_first" || mode("beta") != "dual" || mode("") != "dual" {
			t.Fatalf("acme's page alone: %v %v %v", mode("acme"), mode("beta"), mode(""))
		}
		// The mode has no order: the site may restrict it, never tighten.
		f.root.do("PUT", "/api/admin/settings/signin.mode", map[string]any{"lower": map[string]any{"mode": "tighten"}}).want(t, 400, "invalid")
		f.lowerOf(t, "signin.mode", map[string]any{"mode": "restrict", "options": []string{"dual", "local_only"}})
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/signin.mode", map[string]any{"value": "sso_redirect"}).want(t, 409, "setting_restricted")
		// local_only never lifts the site's ssoOnly for a tenant: the page follows the site's mode.
		f.lowerOf(t, "signin.mode", map[string]any{"mode": "allow"})
		f.root.putSetting("signin.ssoOnly", true).want(t, 200, "")
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/signin.mode", map[string]any{"value": "local_only"}).want(t, 200, "")
		if mode("acme") != "dual" {
			t.Fatalf("acme's page: %v", mode("acme"))
		}
	})
}

// A request that names a tenant never weakens the site's own captcha: the form
// asked is the stricter of the site's and the named tenant's, even where the
// site allows a tenant to loosen. The named tenant is not tied to the account.
func TestANamedTenantNeverWeakensTheSitesCaptcha(t *testing.T) {
	t.Parallel()
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newTLFx(t, a)
		f.lowerOf(t, "captcha.contexts", map[string]any{"mode": "allow"})
		f.root.putSetting("captcha.contexts", map[string]any{"login": map[string]any{"on": true, "when": "always"}}).want(t, 200, "")
		f.tset(f.admC, f.tenC, "captcha.contexts", map[string]any{"login": map[string]any{"on": false}}).want(t, 200, "")
		c := a.newClient("203.0.113.41")
		if c.do("GET", "/api/captcha?ctx=login&tenant=beta", nil).want(t, 200, "").body["required"] != true {
			t.Fatal("beta's looser value reached the form")
		}
		c.do("POST", "/api/login", map[string]any{"username": "root", "password": pw, "tenant": "beta"}).want(t, 400, "captcha_failed")
		// Stricter than the site's still holds: the tenant asks always where the site asks after failures.
		f.root.putSetting("captcha.contexts", map[string]any{"login": map[string]any{"on": true, "when": "after_failures", "failures": 5}}).want(t, 200, "")
		f.tset(f.admB, f.tenB, "captcha.contexts", map[string]any{"login": map[string]any{"on": true, "when": "always"}}).want(t, 200, "")
		if c.do("GET", "/api/captcha?ctx=login&tenant=acme", nil).want(t, 200, "").body["required"] != true ||
			c.do("GET", "/api/captcha?ctx=login&tenant=beta", nil).want(t, 200, "").body["required"] != false {
			t.Fatal("the stricter of the two")
		}
	})
}

// A tenant's write of a stricter login captcha holds while the site asks the
// invitation's: the tenant's document has only its own forms.
func TestATenantsCaptchaWriteIgnoresTheSitesOtherForms(t *testing.T) {
	t.Parallel()
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newTLFx(t, a)
		f.root.putSetting("captcha.contexts", map[string]any{"invite": map[string]any{"on": true, "when": "always"}}).want(t, 200, "")
		f.tset(f.admB, f.tenB, "captcha.contexts", map[string]any{"login": map[string]any{"on": true, "when": "always"}}).want(t, 200, "")
		c := a.newClient("203.0.113.42")
		if c.do("GET", "/api/captcha?ctx=login&tenant=acme", nil).want(t, 200, "").body["required"] != true {
			t.Fatal("acme's value is not in effect")
		}
	})
}

// A site administrator who belongs to an organisation follows the site's
// sessions and passkey roles, not the organisation's.
func TestSiteAdministratorsKeepTheSitesSessionsAndPasskeyRules(t *testing.T) {
	t.Parallel()
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newTLFx(t, a)
		if _, err := a.auth.CreateUser(t.Context(), auth.CreateParams{Username: "sa", Role: "admin", Password: pw, TenantID: f.tenB, SiteAdmin: true}); err != nil {
			t.Fatal(err)
		}
		f.tset(f.admB, f.tenB, "sessions.maxDays", 5).want(t, 200, "")
		f.tset(f.admB, f.tenB, "signin.passkeyRoles", []string{"admin", "member"}).want(t, 200, "")
		sa := a.newClient("")
		r := sa.do("POST", "/api/login", good("sa")).want(t, 200, "")
		if got := sessionMaxAge(r, a.auth.SessionCookieName()); got != 90*24*60*60 {
			t.Fatalf("the cookie lasts %d seconds", got)
		}
		if sa.do("GET", "/api/session", nil).want(t, 200, "").body["hold"] == "passkey" {
			t.Fatal("held for the organisation's passkey rule")
		}
		for range 4 {
			a.clock.Advance(36 * time.Hour)
			sa.do("GET", "/api/account", nil).want(t, 200, "")
		}
		f.mB.do("GET", "/api/account", nil).want(t, 401, "unauthenticated")
	})
}

// A tenant that shortens only maxDays is not refused: the idle time never
// exceeds the lifetime.
func TestATenantMayShortenMaxDaysAlone(t *testing.T) {
	t.Parallel()
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newTLFx(t, a)
		f.tset(f.admB, f.tenB, "sessions.maxDays", 20).want(t, 200, "")
		f.tset(f.admB, f.tenB, "sessions.idleDays", 25).want(t, 200, "") // clamped to the lifetime when read
	})
}

// An unknown tenant is counted against the address before it is refused.
func TestAnUnknownTenantIsCountedBeforeItIsRefused(t *testing.T) {
	t.Parallel()
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newTLFx(t, a)
		f.root.putSetting("signin.perMinute", 3).want(t, 200, "")
		c := a.newClient("203.0.113.43")
		limited := false
		for range 15 {
			if c.do("GET", "/api/captcha?ctx=login&tenant=nowhere", nil).code == 429 {
				limited = true
			}
		}
		if !limited {
			t.Fatal("probing slugs is not limited")
		}
		d := a.newClient("203.0.113.44")
		limited = false
		for range 15 {
			if d.do("POST", "/api/login", map[string]any{"username": "x", "password": pw, "tenant": "nowhere"}).code == 429 {
				limited = true
			}
		}
		if !limited {
			t.Fatal("probing slugs at login is not limited")
		}
	})
}

// The sweep keeps by each person's tenant: a tenant that allows 200 days keeps
// a session idle for 95; one with shorter limits loses it, the others' stay.
func TestTheSweepUsesTheTenantsCutoffs(t *testing.T) {
	t.Parallel()
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newTLFx(t, a)
		f.lowerOf(t, "sessions.maxDays", map[string]any{"mode": "allow"})
		f.lowerOf(t, "sessions.idleDays", map[string]any{"mode": "allow"})
		f.root.putSetting("sessions.maxDays", 365).want(t, 200, "")
		f.root.putSetting("sessions.idleDays", 30).want(t, 200, "")
		f.tset(f.admB, f.tenB, "sessions.maxDays", 400).want(t, 200, "")
		f.tset(f.admB, f.tenB, "sessions.idleDays", 200).want(t, 200, "")
		f.tset(f.admC, f.tenC, "sessions.idleDays", 7).want(t, 200, "")
		mb, mc := a.newClient(""), a.newClient("")
		mb.do("POST", "/api/login", good("mb")).want(t, 200, "")
		mc.do("POST", "/api/login", good("mc")).want(t, 200, "")
		a.clock.Advance(95 * 24 * time.Hour)
		a.auth.Sweep(t.Context())
		mb.do("GET", "/api/account", nil).want(t, 200, "")
		mc.do("GET", "/api/account", nil).want(t, 401, "unauthenticated")
		var n int
		if err := a.st.Q().QueryRow(t.Context(), `SELECT COUNT(*) FROM sessions s JOIN users u ON u.id = s.user_id WHERE u.username = 'mc'`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("beta's session is still stored: %d %v", n, err)
		}
	})
}
