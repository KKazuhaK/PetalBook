package api_test

import "testing"

// The limits of the jobs runner are settings: jobs.maxRunning is the server's
// (no rule for one tenant), jobs.perTenant is the site's, with a rule for one
// tenant that its administrators cannot touch.
func TestTheJobLimitsAreSiteSettings(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		path := func(key string) string { return "/api/admin/tenants/" + f.tenB + "/settings/" + key }
		r := f.root.do("PUT", "/api/admin/settings/jobs.maxRunning", map[string]any{"value": 3}).want(t, 200, "")
		if r.body["site"].(map[string]any)["value"] != float64(3) {
			t.Fatal(r.raw)
		}
		f.root.do("PUT", "/api/admin/settings/jobs.maxRunning", map[string]any{"value": 0}).want(t, 400, "")
		f.root.do("PUT", path("jobs.maxRunning"), map[string]any{"value": 5}).want(t, 404, "not_found")
		f.admB.do("PUT", path("jobs.perTenant"), map[string]any{"value": 4}).want(t, 409, "setting_locked")
		r = f.root.do("PUT", path("jobs.perTenant"), map[string]any{"value": 2}).want(t, 200, "")
		if r.body["effective"] != float64(2) {
			t.Fatal(r.raw)
		}
		if r := f.admB.do("GET", "/api/admin/tenants/"+f.tenB+"/settings", nil).want(t, 200, ""); settingOf(t, r, "jobs.perTenant")["effective"] != float64(2) {
			t.Fatal(r.raw)
		}
	})
}
