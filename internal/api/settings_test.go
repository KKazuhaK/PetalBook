package api_test

import (
	"strings"
	"testing"

	"github.com/KKazuhaK/PetalBook/internal/store"
)

func settingOf(t *testing.T, r result, key string) map[string]any {
	t.Helper()
	for _, s := range r.body["settings"].([]any) {
		if m := s.(map[string]any); m["key"] == key {
			return m
		}
	}
	t.Fatalf("no setting %s in %s", key, r.raw)
	return nil
}

func TestSiteSettingsByLayer(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		// Only the site's administrators; the tenant's cannot read or write the server's own.
		f.admA.do("GET", "/api/admin/settings", nil).want(t, 403, "forbidden")
		f.admA.do("PUT", "/api/admin/settings/legal.termsUrl", map[string]any{"value": "https://x.example"}).want(t, 403, "forbidden")
		r := f.root.do("GET", "/api/admin/settings", nil).want(t, 200, "")
		s := settingOf(t, r, "registration.open")
		site := s["site"].(map[string]any)
		if s["kind"] != "boolean" || site["value"] != false || site["lower"].(map[string]any)["mode"] != "lock" || strings.Join(strs(s["layers"]), ",") != "site,tenant" {
			t.Fatal(s)
		}
		if d := settingOf(t, r, "daily.own"); strings.Join(strs(d["layers"]), ",") != "site,tenant,space" || d["site"].(map[string]any)["value"] != true {
			t.Fatal(d)
		}
		// The 3.1 and 2.7 settings are reserved: they do not exist yet.
		for _, k := range []string{"storage.remotes.require", "github.app.id"} {
			f.root.do("PUT", "/api/admin/settings/"+k, map[string]any{"value": 1}).want(t, 404, "not_found")
		}
		// Values are checked.
		f.root.do("PUT", "/api/admin/settings/registration.perHour", map[string]any{"value": 0}).want(t, 400, "invalid")
		f.root.do("PUT", "/api/admin/settings/registration.perHour", map[string]any{"value": "many"}).want(t, 400, "invalid")
		f.root.do("PUT", "/api/admin/settings/registration.perHour", map[string]any{}).want(t, 400, "invalid")
		f.root.do("PUT", "/api/admin/settings/registration.perHour", map[string]any{"valu": 5}).want(t, 400, "invalid")
		f.root.do("PUT", "/api/admin/settings/legal.termsUrl", map[string]any{"value": "http://plain.example"}).want(t, 400, "invalid")
		f.root.do("PUT", "/api/admin/settings/registration.open", map[string]any{"lower": map[string]any{"mode": "sometimes"}}).want(t, 400, "invalid")
		r = f.root.do("PUT", "/api/admin/settings/legal.termsUrl", map[string]any{"value": "https://example.com/terms"}).want(t, 200, "")
		if r.body["site"].(map[string]any)["value"] != "https://example.com/terms" || r.body["affected"] != nil {
			t.Fatal(r.raw)
		}
		// Registration cannot be opened while mail is not enabled, at the site or in a tenant.
		f.root.do("PUT", "/api/admin/settings/registration.open", map[string]any{"value": true}).want(t, 409, "mail_not_configured")
		f.root.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/registration.open", map[string]any{"value": true}).want(t, 409, "setting_locked")
		f.root.do("PUT", "/api/admin/settings/registration.open", map[string]any{"lower": map[string]any{"mode": "allow"}}).want(t, 200, "")
		f.root.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/registration.open", map[string]any{"value": true}).want(t, 409, "mail_not_configured")
		// The change is in the audit log with its layer.
		a.auth.FlushAudit(ctxBG)
		if r := f.root.do("GET", "/api/admin/audit?kind=setting_changed", nil).want(t, 200, ""); !strings.Contains(r.raw, `"key":"legal.termsUrl"`) || !strings.Contains(r.raw, `"layer":"site"`) {
			t.Fatal(r.raw)
		}
	})
}

func TestTenantSettingsHonourTheSitesLocksAndRestrictions(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		path := func(key string) string { return "/api/admin/tenants/" + f.tenB + "/settings/" + key }
		// A tenant administrator reads their tenant's, and no other's.
		r := f.admB.do("GET", "/api/admin/tenants/"+f.tenB+"/settings", nil).want(t, 200, "")
		s := settingOf(t, r, "registration.open")
		if s["effective"] != false || s["site"].(map[string]any)["lower"].(map[string]any)["mode"] != "lock" ||
			s["tenant"].(map[string]any)["value"] != nil || s["tenant"].(map[string]any)["inEffect"] != true {
			t.Fatal(s)
		}
		if len(r.body["settings"].([]any)) != 14 {
			t.Fatalf("every setting that has a tenant layer: %s", r.raw)
		}
		f.admB.do("GET", "/api/admin/tenants/"+store.DefaultTenantID+"/settings", nil).want(t, 404, "not_found")
		// Locked: refused for the tenant's administrator and for the site's.
		f.admB.do("PUT", path("registration.open"), map[string]any{"value": false}).want(t, 409, "setting_locked")
		f.root.do("PUT", path("registration.open"), map[string]any{"value": false}).want(t, 409, "setting_locked")
		// Restricted: perHour may not go above the site's 50.
		f.admB.do("PUT", path("registration.perHour"), map[string]any{"value": 80}).want(t, 409, "setting_restricted")
		r = f.admB.do("PUT", path("registration.perHour"), map[string]any{"value": 30}).want(t, 200, "")
		if r.body["effective"] != float64(30) || r.body["tenant"].(map[string]any)["value"] != float64(30) {
			t.Fatal(r.raw)
		}
		// The site tightens: the value is kept, not in effect, and the answer counts the tenants.
		r = f.root.do("PUT", "/api/admin/settings/registration.perHour", map[string]any{"lower": map[string]any{"mode": "restrict", "max": 10}}).want(t, 200, "")
		if r.body["affected"] != float64(1) {
			t.Fatal(r.raw)
		}
		s = settingOf(t, f.admB.do("GET", "/api/admin/tenants/"+f.tenB+"/settings", nil).want(t, 200, ""), "registration.perHour")
		if tv := s["tenant"].(map[string]any); tv["inEffect"] != false || tv["value"] != float64(30) || s["effective"] != float64(50) {
			t.Fatal(s)
		}
		// The tenant's rule for what is below it must be as strict.
		f.admB.do("PUT", path("registration.perHour"), map[string]any{"lower": map[string]any{"mode": "restrict", "max": 20}}).want(t, 409, "setting_restricted")
		f.admB.do("PUT", path("registration.perHour"), map[string]any{"lower": map[string]any{"mode": "lock"}}).want(t, 200, "")
		// Unlocked and loosened again at the site, the kept value counts again.
		f.root.do("PUT", "/api/admin/settings/registration.perHour", map[string]any{"lower": map[string]any{"mode": "allow"}}).want(t, 200, "")
		s = settingOf(t, f.admB.do("GET", "/api/admin/tenants/"+f.tenB+"/settings", nil).want(t, 200, ""), "registration.perHour")
		if s["effective"] != float64(30) || s["tenant"].(map[string]any)["inEffect"] != true {
			t.Fatal(s)
		}
		// null follows the site; an unknown key is not found; a bad value is invalid.
		f.admB.do("PUT", path("registration.perHour"), map[string]any{"value": nil}).want(t, 200, "")
		f.admB.do("PUT", path("no.such"), map[string]any{"value": 1}).want(t, 404, "not_found")
		f.admB.do("PUT", path("registration.domains"), map[string]any{"value": []string{"x"}}).want(t, 400, "invalid")
		r = f.admB.do("PUT", path("registration.domains"), map[string]any{"value": []string{"Acme.example"}}).want(t, 200, "")
		if strings.Join(strs(r.body["effective"]), ",") != "acme.example" {
			t.Fatal(r.raw)
		}
		// The other tenant is not touched.
		if s := settingOf(t, f.admA.do("GET", "/api/admin/tenants/"+store.DefaultTenantID+"/settings", nil).want(t, 200, ""), "registration.domains"); len(s["effective"].([]any)) != 0 {
			t.Fatal(s)
		}
	})
}

func TestDailyTextSwitchesAreTheSameRecordAsTheLayeredSettings(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		own := func(c *client, space string) map[string]any {
			return c.do("GET", "/api/spaces/"+space+"/daily/settings", nil).want(t, 200, "").body
		}
		if own(f.admA, f.spaceA)["inEffect"] != true || own(f.admB, f.spaceB)["inEffect"] != true {
			t.Fatal("a space may have its own source by default")
		}
		// 2.3's PUT /api/admin/daily is the site's value of the setting.
		f.root.do("PUT", "/api/admin/daily", map[string]any{"allow": map[string]any{"own": false}}).want(t, 200, "")
		if v := settingOf(t, f.root.do("GET", "/api/admin/settings", nil).want(t, 200, ""), "daily.own")["site"].(map[string]any)["value"]; v != false {
			t.Fatalf("%v", v)
		}
		if own(f.admA, f.spaceA)["inEffect"] != false || own(f.admB, f.spaceB)["inEffect"] != false {
			t.Fatal("the switch is not in effect for every tenant")
		}
		// And the layered API writes the same record back.
		f.root.do("PUT", "/api/admin/settings/daily.own", map[string]any{"value": true}).want(t, 200, "")
		if f.root.do("GET", "/api/admin/daily", nil).want(t, 200, "").body["allow"].(map[string]any)["own"] != true {
			t.Fatal("/api/admin/daily does not say what the settings do")
		}
		// A tenant may switch it off for its own spaces; the other tenant is not touched.
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/daily.own", map[string]any{"value": false}).want(t, 200, "")
		if own(f.admB, f.spaceB)["inEffect"] != false || own(f.admA, f.spaceA)["inEffect"] != true {
			t.Fatal("a tenant's switch is not its own")
		}
		// GET /api/settings says so, so a client can grey the control out.
		r := f.memB.do("GET", "/api/settings?space="+f.spaceB, nil).want(t, 200, "")
		if s := settingOf(t, r, "daily.own"); s["effective"] != false || s["space"].(map[string]any)["canChange"] != false || s["space"].(map[string]any)["inEffect"] != false || s["space"].(map[string]any)["source"] != "tenant" {
			t.Fatal(s)
		}
		r = f.memA.do("GET", "/api/settings?space="+f.spaceA, nil).want(t, 200, "")
		if s := settingOf(t, r, "daily.own"); s["effective"] != true || s["space"].(map[string]any)["canChange"] != true {
			t.Fatal(s)
		}
		f.memA.do("GET", "/api/settings?space="+f.spaceB, nil).want(t, 404, "not_found")
		// A lock says whose it is.
		f.root.do("PUT", "/api/admin/settings/daily.api", map[string]any{"lower": map[string]any{"mode": "lock"}}).want(t, 200, "")
		if s := settingOf(t, f.memB.do("GET", "/api/settings", nil).want(t, 200, ""), "daily.api"); s["space"].(map[string]any)["canChange"] != false || s["space"].(map[string]any)["source"] != "site" {
			t.Fatal(s)
		}
		f.root.do("PUT", "/api/admin/settings/daily.api", map[string]any{"lower": nil}).want(t, 200, "")
		// With the site's lock on, a tenant cannot set it, and the value it kept stays.
		f.root.do("PUT", "/api/admin/settings/daily.own", map[string]any{"lower": map[string]any{"mode": "lock"}}).want(t, 200, "")
		f.admA.do("PUT", "/api/admin/tenants/"+store.DefaultTenantID+"/settings/daily.own", map[string]any{"value": false}).want(t, 409, "setting_locked")
		if own(f.admB, f.spaceB)["inEffect"] != true {
			t.Fatal("a locked tenant value counted")
		}
		f.root.do("PUT", "/api/admin/settings/daily.own", map[string]any{"lower": map[string]any{"mode": "allow"}}).want(t, 200, "")
		if own(f.admB, f.spaceB)["inEffect"] != false {
			t.Fatal("the kept value did not count again")
		}
	})
}

func TestTheDailyDefaultComesBetweenTheSpaceAndTheSite(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		page := func(c *client, space, title string) {
			c.do("POST", "/api/spaces/"+space+"/pages", map[string]any{"folder": "Q", "meta": map[string]any{"title": title, "tags": []any{"lang-en"}}, "body": title + " text\n"}).want(t, 201, "")
		}
		page(f.admB, f.spaceB, "B quote")
		page(f.admA, f.spaceA, "A quote")
		def := func(space string) map[string]any {
			return map[string]any{"kind": "space", "space": space, "source": map[string]any{"en": map[string]any{"folder": "Q"}}}
		}
		// The site's default is A's space; a tenant names a space of its own tenant only.
		f.root.do("PUT", "/api/admin/daily", map[string]any{"default": def(f.spaceA)}).want(t, 200, "")
		tpath := "/api/admin/tenants/" + f.tenB + "/settings/daily.default"
		f.admB.do("PUT", tpath, map[string]any{"value": def(f.spaceA)}).want(t, 400, "invalid")
		f.admB.do("PUT", tpath, map[string]any{"value": map[string]any{"kind": "list"}}).want(t, 400, "invalid")
		f.admB.do("PUT", tpath, map[string]any{"value": def(f.spaceB)}).want(t, 200, "")
		quote := func(c *client, space string) map[string]any {
			return c.do("GET", "/api/spaces/"+space+"/daily?date=2026-11-23&lang=en", nil).want(t, 200, "").body
		}
		qb := quote(f.memB, f.spaceB)
		if q, _ := qb["quote"].(map[string]any); q == nil || q["source"] != "B quote" {
			// B's member is in a space of the same tenant: the tenant's default applies to a space with no source of its own.
			t.Fatalf("tenant B's default did not apply: %v", qb)
		}
		pa := f.memA.personalSpace()
		qa := quote(f.memA, pa)
		if q, _ := qa["quote"].(map[string]any); q == nil || q["source"] != "A quote" {
			t.Fatalf("tenant A kept the site's default: %v", qa)
		}
		// null follows the site again.
		f.admB.do("PUT", tpath, map[string]any{"value": nil}).want(t, 200, "")
		qb = quote(f.memB, f.memB.personalSpace())
		if q, _ := qb["quote"].(map[string]any); q == nil || q["source"] != "A quote" {
			t.Fatalf("%v", qb)
		}
	})
}

func TestSettingsRoutesDoNotExistInSingleUserMode(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for _, p := range []string{"/api/settings", "/api/admin/settings", "/api/admin/tenancy", "/api/admin/tenants", "/api/admin/tenants/x/settings"} {
		if code, r, raw := f.do("GET", p, "{}"); code != 404 || r["error"] != "not_found" {
			t.Errorf("%s: %d %s", p, code, raw)
		}
	}
}
