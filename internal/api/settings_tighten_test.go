package api_test

import (
	"strings"
	"testing"
)

// The lower mode tighten over the API: the site sets a value and a note; the
// tenant's administrators see the rule and the note ("why can't I change
// this"), may write a value as strict or stricter, and a looser one is 409
// setting_restricted. The note is audited, a key without an order of
// strictness refuses the mode, and a note longer than 200 characters is
// refused.
func TestTightenAndRuleNotesOverTheAPI(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		site := "/api/admin/settings/registration.perHour"
		tenant := "/api/admin/tenants/" + f.tenB + "/settings/registration.perHour"
		list := "/api/admin/tenants/" + f.tenB + "/settings"
		note := "Company policy: at most 20 an hour."
		r := f.root.do("PUT", site, map[string]any{"value": 20, "lower": map[string]any{"mode": "tighten", "note": note}}).want(t, 200, "")
		if r.body["strictness"] != "lower" {
			t.Fatal(r.raw)
		}
		if lw := r.body["site"].(map[string]any)["lower"].(map[string]any); lw["mode"] != "tighten" || lw["note"] != note {
			t.Fatal(r.raw)
		}
		// The tenant sees the rule, the floor and the note.
		s := settingOf(t, f.admB.do("GET", list, nil).want(t, 200, ""), "registration.perHour")
		reason, _ := s["reason"].(map[string]any)
		if reason["by"] != "site" || reason["mode"] != "tighten" || reason["note"] != note || reason["floor"] != float64(20) || s["strictness"] != "lower" {
			t.Fatalf("%v", s)
		}
		f.admB.do("PUT", tenant, map[string]any{"value": 30}).want(t, 409, "setting_restricted")
		r = f.admB.do("PUT", tenant, map[string]any{"value": 10}).want(t, 200, "")
		if r.body["effective"] != float64(10) {
			t.Fatal(r.raw)
		}
		// A site value stricter than the tenant's: the tenant's is not in effect.
		f.root.do("PUT", site, map[string]any{"value": 5}).want(t, 200, "")
		s = settingOf(t, f.admB.do("GET", list, nil).want(t, 200, ""), "registration.perHour")
		if tv := s["tenant"].(map[string]any); s["effective"] != float64(5) || tv["inEffect"] != false || tv["value"] != float64(10) {
			t.Fatalf("%v", s)
		}
		// The note rides with a rule for one tenant too.
		r = f.root.do("PUT", tenant, map[string]any{"siteRule": map[string]any{"value": 3, "lower": map[string]any{"mode": "tighten", "note": "Trial."}}}).want(t, 200, "")
		if rules := r.body["tenant"].(map[string]any)["rules"].([]any); rules[0].(map[string]any)["lower"].(map[string]any)["note"] != "Trial." {
			t.Fatal(r.raw)
		}
		if rs := r.body["reason"].(map[string]any); rs["note"] != "Trial." || rs["floor"] != float64(3) {
			t.Fatal(r.raw)
		}
		// Audited.
		if au := f.root.do("GET", "/api/admin/audit?kind=setting_changed", nil).want(t, 200, ""); !strings.Contains(au.raw, `"note":"`+note+`"`) || !strings.Contains(au.raw, `"note":"Trial."`) {
			t.Fatal(au.raw)
		}
		// A key with no order, and a note that is too long.
		f.root.do("PUT", "/api/admin/settings/daily.own", map[string]any{"lower": map[string]any{"mode": "tighten"}}).want(t, 400, "invalid")
		f.root.do("PUT", site, map[string]any{"lower": map[string]any{"mode": "tighten", "note": strings.Repeat("x", 201)}}).want(t, 400, "invalid")
	})
}
