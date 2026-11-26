package api_test

import "testing"

// The site's rule for one tenant over the API: siteRule locks offline.copies
// off for Acme (what a capability was), and its administrators cannot change
// it or the rule; a site-only key the site may set for one tenant
// (exports.days) is the site's to write there; a server-wide key is not
// there at all.
func TestTheSitesRuleForOneTenant(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		path := func(key string) string { return "/api/admin/tenants/" + f.tenB + "/settings/" + key }
		f.admB.do("PUT", path("offline.copies"), map[string]any{"value": true}).want(t, 200, "")
		f.admB.do("PUT", path("offline.copies"), map[string]any{"siteRule": map[string]any{"value": false, "lower": map[string]any{"mode": "lock"}}}).want(t, 409, "setting_locked")
		r := f.root.do("PUT", path("offline.copies"), map[string]any{"siteRule": map[string]any{"value": false, "lower": map[string]any{"mode": "lock"}}}).want(t, 200, "")
		tv := r.body["tenant"].(map[string]any)
		sr, _ := tv["siteRule"].(map[string]any)
		if r.body["effective"] != false || tv["siteLocked"] != true || tv["value"] != true || tv["inEffect"] != false || sr["value"] != false || sr["inEffect"] != true {
			t.Fatal(r.raw)
		}
		f.admB.do("PUT", path("offline.copies"), map[string]any{"value": false}).want(t, 409, "setting_locked")
		f.root.do("PUT", path("offline.copies"), map[string]any{"siteRule": nil}).want(t, 200, "")
		if r := f.admB.do("GET", "/api/admin/tenants/"+f.tenB+"/settings", nil).want(t, 200, ""); settingOf(t, r, "offline.copies")["effective"] != true {
			t.Fatal(r.raw)
		}

		f.admB.do("PUT", path("exports.days"), map[string]any{"value": 3}).want(t, 409, "setting_locked")
		f.root.do("PUT", path("exports.days"), map[string]any{"lower": map[string]any{"mode": "lock"}}).want(t, 400, "invalid")
		r = f.root.do("PUT", path("exports.days"), map[string]any{"value": 3}).want(t, 200, "")
		if r.body["effective"] != float64(3) {
			t.Fatal(r.raw)
		}
		f.root.do("PUT", path("users.reuseUsernames"), map[string]any{"value": "never"}).want(t, 404, "not_found")
	})
}

// A site value above its own max is 400 setting_above_max with a sentence,
// for the site's layer and for the site's rule for one tenant.
func TestASiteValueAboveItsMaxIsRefused(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		site := "/api/admin/settings/quota.spacesPerPerson"
		f.root.do("PUT", site, map[string]any{"lower": map[string]any{"mode": "restrict", "max": 10}}).want(t, 200, "")
		r := f.root.do("PUT", site, map[string]any{"value": 20}).want(t, 400, "setting_above_max")
		if m, _ := r.body["message"].(string); m != "The value is above the maximum; raise the maximum first." {
			t.Fatal(r.raw)
		}
		f.root.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/quota.spacesPerPerson",
			map[string]any{"siteRule": map[string]any{"value": 30, "lower": map[string]any{"mode": "restrict", "max": 12}}}).want(t, 400, "setting_above_max")
	})
}
