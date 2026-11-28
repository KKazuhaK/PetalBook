package api_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"petalbook.app/server/internal/store"
)

// The stray site settings fold into the layered settings (settings fold, part
// 2): their endpoints are gone, what the other endpoints keep is not a setting,
// and the secrets have one home.

func TestTheEndpointsOfTheFoldedSettingsAreGone(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		root.do("GET", "/api/admin/access", nil).want(t, 404, "not_found")
		root.do("PUT", "/api/admin/access", map[string]any{"tokens": false}).want(t, 404, "not_found")
		root.do("GET", "/api/admin/tenancy", nil).want(t, 404, "not_found")
		root.do("PUT", "/api/admin/tenancy", map[string]any{"enabled": true}).want(t, 404, "not_found")
		// The paths that still have a GET answer 405 to the writes that went.
		root.do("PUT", "/api/admin/daily", map[string]any{"languages": []string{"en"}}).want(t, 405, "")
		root.do("PUT", "/api/admin/storage", map[string]any{"cache": map[string]any{"minIdleHours": 5}}).want(t, 405, "")
		// What they kept holds no setting.
		d := root.do("GET", "/api/admin/daily", nil).want(t, 200, "")
		for _, k := range []string{"default", "languages", "allow"} {
			if _, ok := d.body[k]; ok {
				t.Errorf("/api/admin/daily still has %s", k)
			}
		}
		st := root.do("GET", "/api/admin/storage", nil).want(t, 200, "")
		for _, k := range []string{"hosts", "localSpaces", "cache"} {
			if _, ok := st.body[k]; ok {
				t.Errorf("/api/admin/storage still has %s", k)
			}
		}
		// The security endpoint takes the captcha's keys and nothing else.
		root.do("PUT", "/api/admin/security", map[string]any{"login": map[string]any{"mode": "dual"}}).want(t, 400, "invalid")
		sec := root.do("GET", "/api/admin/security", nil).want(t, 200, "")
		for _, k := range []string{"lockout", "limits"} {
			if _, ok := sec.body[k]; ok {
				t.Errorf("/api/admin/security still has %s", k)
			}
		}
	})
}

func TestTenancyIsASettingAndTheCountIsOnTheTenantList(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		root.do("POST", "/api/admin/tenants", map[string]any{"slug": "acme", "name": "Acme"}).want(t, 403, "feature_off")
		root.do("PUT", "/api/admin/settings/features.tenancy", map[string]any{"value": true}).want(t, 200, "")
		root.do("POST", "/api/admin/tenants", map[string]any{"slug": "acme", "name": "Acme"}).want(t, 201, "")
		if n := root.do("GET", "/api/admin/tenants", nil).want(t, 200, "").body["count"]; n != float64(2) {
			t.Fatalf("count %v", n)
		}
		// Off again at once: the switch is read from the settings' cache, which the write dropped.
		root.do("PUT", "/api/admin/settings/features.tenancy", map[string]any{"value": false}).want(t, 200, "")
		root.do("POST", "/api/admin/tenants", map[string]any{"slug": "more", "name": "More"}).want(t, 403, "feature_off")
	})
}

// features.tokens is the tenant's: a rule for one tenant refuses its tokens
// and the others' still work.
func TestTokensOffForOneTenantRefuseThereOnly(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		_, secA := mkToken(t, f.admA, "read", nil, f.spaceA)
		_, secB := mkToken(t, f.admB, "read", nil, f.spaceB)
		a.bearer(secA, "").do("GET", spacePath(f.spaceA)+"/tree", nil).want(t, 200, "")
		a.bearer(secB, "").do("GET", spacePath(f.spaceB)+"/tree", nil).want(t, 200, "")
		f.root.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/features.tokens",
			map[string]any{"siteRule": map[string]any{"value": false, "lower": map[string]any{"mode": "lock"}}}).want(t, 200, "")
		a.bearer(secB, "").do("GET", spacePath(f.spaceB)+"/tree", nil).want(t, 403, "tokens_off")
		a.bearer(secA, "").do("GET", spacePath(f.spaceA)+"/tree", nil).want(t, 200, "")
		// Its administrators cannot turn it back on: the site's lock is over theirs.
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/features.tokens", map[string]any{"value": true}).want(t, 409, "setting_locked")
		// A tenant may switch its own off, the site's default being on.
		f.root.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/features.tokens", map[string]any{"siteRule": nil}).want(t, 200, "")
		a.bearer(secB, "").do("GET", spacePath(f.spaceB)+"/tree", nil).want(t, 200, "")
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/features.tokens", map[string]any{"value": false}).want(t, 200, "")
		a.bearer(secB, "").do("GET", spacePath(f.spaceB)+"/tree", nil).want(t, 403, "tokens_off")
	})
}

// The storage policy's "since" is observed state, kept in a record when the
// requirement of a remote changes its form.
func TestTheRemoteRequirementKeepsItsSince(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		since := func() any {
			return root.do("GET", "/api/admin/storage", nil).want(t, 200, "").body["remotes"].(map[string]any)["since"]
		}
		if since() != nil {
			t.Fatalf("since with nothing required: %v", since())
		}
		a.clock.Advance(0)
		r := root.do("PUT", "/api/admin/settings/storage.remotes.require", map[string]any{"value": "any"}).want(t, 200, "")
		if r.body["remotes"] == nil {
			t.Fatalf("the answer does not say how many spaces the requirement asks something of: %s", r.raw)
		}
		first := since()
		if first == nil {
			t.Fatal("no since once something is required")
		}
		// The same requirement again keeps it; a changed scope starts the grace afresh.
		a.clock.Advance(3600e9)
		root.do("PUT", "/api/admin/settings/storage.remotes.require", map[string]any{"value": "any"}).want(t, 200, "")
		if since() != first {
			t.Fatalf("an unchanged requirement moved since: %v %v", first, since())
		}
		root.do("PUT", "/api/admin/settings/storage.remotes.scope", map[string]any{"value": "all"}).want(t, 200, "")
		if since() == first {
			t.Fatal("a changed scope kept the old since")
		}
		root.do("PUT", "/api/admin/settings/storage.remotes.require", map[string]any{"value": ""}).want(t, 200, "")
		if since() != nil {
			t.Fatalf("since stays with nothing required: %v", since())
		}
		// A provider the server does not have cannot be required.
		root.do("PUT", "/api/admin/settings/storage.remotes.require", map[string]any{"value": "nowhere"}).want(t, 400, "invalid")
	})
}

func TestTheDailyTextsLanguagesAndDefaultAreSettings(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		r := root.do("PUT", "/api/admin/settings/daily.languages", map[string]any{"value": []string{"zh", "pt-br", "en", "EN"}}).want(t, 200, "")
		if got := jsonText(r.body["site"].(map[string]any)["value"]); got != `["zh-Hans","pt-BR","en"]` {
			t.Fatalf("not canonical: %s", got)
		}
		root.do("PUT", "/api/admin/settings/daily.languages", map[string]any{"value": []string{"zz zz"}}).want(t, 400, "bad_language")
		root.do("PUT", "/api/admin/settings/daily.default", map[string]any{"value": map[string]any{"kind": "bogus"}}).want(t, 400, "")
		root.do("PUT", "/api/admin/settings/daily.own", map[string]any{"value": false}).want(t, 200, "")
		raw, _, err := store.GetSiteRecord(context.Background(), a.st.Q(), "daily")
		if err == nil && (strings.Contains(raw, "languages") || strings.Contains(raw, `"allow"`)) {
			t.Fatalf("the daily record holds settings: %s", raw)
		}
	})
}

// The secrets: write-only, scoped, and gated by what the site allows a tenant.
func TestSecretsAreWriteOnlyAndScoped(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		const value = "a-very-secret-value-42"
		list := func(c *client, path string) map[string]any {
			t.Helper()
			r := c.do("GET", path, nil).want(t, 200, "")
			if strings.Contains(r.raw, value) {
				t.Fatalf("a secret was shown: %s", r.raw)
			}
			out := map[string]any{}
			for _, it := range r.body["secrets"].([]any) {
				m := it.(map[string]any)
				out[m["name"].(string)] = m
			}
			return out
		}
		// The site's: set, listed as set (never the value), and cleared.
		r := f.root.do("PUT", "/api/admin/secrets/mail.password", map[string]any{"value": value}).want(t, 200, "")
		if strings.Contains(r.raw, value) {
			t.Fatalf("the answer to a write held the value: %s", r.raw)
		}
		if m := list(f.root, "/api/admin/secrets")["mail.password"].(map[string]any); m["set"] != true || m["updatedAt"] == nil {
			t.Fatalf("%v", m)
		}
		if list(f.root, "/api/admin/secrets")["captcha.secret"].(map[string]any)["set"] != false {
			t.Fatal("an unset secret is set")
		}
		// The mail settings see it as a password that is there, and still never show it.
		if g := f.root.do("GET", "/api/admin/mail", nil).want(t, 200, ""); g.body["hasPassword"] != true || strings.Contains(g.raw, value) {
			t.Fatal(g.raw)
		}
		// The GitHub App's secrets are changed through the connection, which checks them.
		f.root.do("PUT", "/api/admin/secrets/github.appKey", map[string]any{"value": "x"}).want(t, 409, "conflict")
		f.root.do("PUT", "/api/admin/secrets/nothing", map[string]any{"value": "x"}).want(t, 404, "not_found")
		f.root.do("PUT", "/api/admin/secrets/mail.password", map[string]any{"value": ""}).want(t, 400, "invalid")
		f.root.do("PUT", "/api/admin/secrets/mail.password", map[string]any{}).want(t, 400, "invalid")
		// Only the site's administrators have the site's.
		f.admA.do("GET", "/api/admin/secrets", nil).want(t, 403, "forbidden")
		f.admA.do("PUT", "/api/admin/secrets/mail.password", map[string]any{"value": "x"}).want(t, 403, "forbidden")
		f.memA.do("GET", "/api/admin/secrets", nil).want(t, 403, "forbidden")

		// A tenant's own: only for what the site allows it. mail.ownSender is off by default.
		tpath := "/api/admin/tenants/" + f.tenB + "/secrets"
		if _, ok := list(f.admB, tpath)["github.appKey"]; ok {
			t.Fatal("a tenant is offered the site's secrets")
		}
		f.admB.do("PUT", tpath+"/mail.password", map[string]any{"value": value}).want(t, 403, "feature_off")
		f.root.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/mail.ownSender", map[string]any{"siteRule": map[string]any{"value": true}}).want(t, 200, "")
		f.admB.do("PUT", tpath+"/mail.password", map[string]any{"value": value}).want(t, 200, "")
		if list(f.admB, tpath)["mail.password"].(map[string]any)["set"] != true {
			t.Fatal("the tenant's secret is not set")
		}
		// It is the tenant's: the site's is its own, and tenant A's administrators cannot reach it.
		if list(f.root, "/api/admin/secrets")["mail.password"].(map[string]any)["updatedAt"] == nil {
			t.Fatal("the site's secret went")
		}
		f.admA.do("GET", tpath, nil).want(t, 404, "not_found")
		f.admA.do("PUT", tpath+"/mail.password", map[string]any{"value": "x"}).want(t, 404, "not_found")
		f.admA.do("DELETE", tpath+"/mail.password", nil).want(t, 404, "not_found")
		f.memB.do("PUT", tpath+"/mail.password", map[string]any{"value": "x"}).want(t, 403, "forbidden")
		f.admB.do("PUT", tpath+"/github.appKey", map[string]any{"value": "x"}).want(t, 404, "not_found")
		// The site may take the feature away; the tenant may still remove what it left.
		f.root.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/mail.ownSender", map[string]any{"siteRule": nil}).want(t, 200, "")
		f.admB.do("PUT", tpath+"/mail.password", map[string]any{"value": "again"}).want(t, 403, "feature_off")
		f.admB.do("DELETE", tpath+"/mail.password", nil).want(t, 200, "")
		if list(f.admB, tpath)["mail.password"].(map[string]any)["set"] != false {
			t.Fatal("not cleared")
		}
		f.root.do("DELETE", "/api/admin/secrets/mail.password", nil).want(t, 200, "")
		if g := f.root.do("GET", "/api/admin/mail", nil).want(t, 200, ""); g.body["hasPassword"] != false {
			t.Fatal(g.raw)
		}

		// The audit trail says what was done to which secret, never the value.
		raw := f.root.do("GET", "/api/admin/audit?kind=secret_changed&limit=100", nil).want(t, 200, "").raw
		if strings.Contains(raw, value) || !strings.Contains(raw, `"name":"mail.password"`) || !strings.Contains(raw, `"change":"cleared"`) || !strings.Contains(raw, `"change":"set"`) {
			t.Fatalf("%s", raw)
		}
		// And the table holds it sealed.
		var n int
		if err := a.st.Q().QueryRow(context.Background(), `SELECT COUNT(*) FROM secrets WHERE value LIKE '%a-very-secret%'`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("a secret is in the table in the clear: %d %v", n, err)
		}
	})
}

// The generic secrets endpoint is held to what the captcha form is: the secret
// of a third-party captcha in use cannot go, or nobody could sign in.
func TestTheCaptchaSecretStaysWhileTheCaptchaIsOn(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		on := map[string]any{"login": map[string]any{"on": true, "when": "always"}}
		admin.putSetting("captcha.provider", "turnstile").want(t, 200, "")
		admin.do("PUT", "/api/admin/security", map[string]any{"captcha": map[string]any{"siteKey": "1x00", "secret": "s3cret-value"}}).want(t, 200, "")
		admin.putSetting("captcha.contexts", on).want(t, 200, "")
		admin.do("DELETE", "/api/admin/secrets/captcha.secret", nil).want(t, 400, "captcha_secret_required")
		admin.do("PUT", "/api/admin/secrets/captcha.secret", map[string]any{"value": ""}).want(t, 400, "invalid")
		if g := admin.do("GET", "/api/admin/security", nil).want(t, 200, ""); g.body["captcha"].(map[string]any)["hasSecret"] != true {
			t.Fatal("the secret went")
		}
		// Replacing it is fine; with the captcha off, so is clearing it.
		admin.do("PUT", "/api/admin/secrets/captcha.secret", map[string]any{"value": "another"}).want(t, 200, "")
		admin.putSetting("captcha.contexts", map[string]any{"login": map[string]any{"on": false}}).want(t, 200, "")
		admin.do("DELETE", "/api/admin/secrets/captcha.secret", nil).want(t, 200, "")
		if g := admin.do("GET", "/api/admin/security", nil).want(t, 200, ""); g.body["captcha"].(map[string]any)["hasSecret"] != false {
			t.Fatal(g.raw)
		}
	})
}

// A captcha switched on and its secret cleared at the same moment: each alone
// is fine, both would keep everybody out. The sign-in policy's writes take
// one lock, so one of them sees the other.
func TestTheCaptchaAndItsSecretAreNotChangedAtOnce(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		admin.putSetting("captcha.provider", "turnstile").want(t, 200, "")
		admin.do("PUT", "/api/admin/security", map[string]any{"captcha": map[string]any{"siteKey": "1x00", "secret": "s3cret-value"}}).want(t, 200, "")
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			admin.do("PUT", "/api/admin/settings/captcha.contexts", map[string]any{"value": map[string]any{"login": map[string]any{"on": true, "when": "always"}}})
		}()
		go func() {
			defer wg.Done()
			admin.do("DELETE", "/api/admin/secrets/captcha.secret", nil)
		}()
		wg.Wait()
		g := admin.do("GET", "/api/admin/security", nil).want(t, 200, "").body["captcha"].(map[string]any)
		login := admin.siteValue("captcha.contexts").(map[string]any)["login"].(map[string]any)
		if login["on"] == true && g["hasSecret"] != true {
			t.Fatalf("a captcha is on without its secret: %v", g)
		}
	})
}
