package api_test

import (
	"testing"
	"time"

	"petalbook.app/server/internal/settings"
)

// A change that switches the feature off later, or leaves it to go off when a
// rule that holds it on ends, stops it all the same: the gate looks at every
// start and end of a window, not only at now.
func TestADatedRuleThatSwitchesDomainsOffLaterNeedsTheConfirmation(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newDomFx(t, a)
		claim(t, f.admB, f.tenB, "acme.example")
		rule := "/api/admin/tenants/" + f.tenB + "/settings/features.domains"
		later := a.clock.Now().Add(48 * time.Hour).Format(time.RFC3339)
		// Off from the day after tomorrow.
		f.root.do("PUT", rule, map[string]any{"siteRule": map[string]any{"value": false, "from": later}}).want(t, 409, "confirm_required")
		// On until the day after tomorrow, while the site's value goes off: B is off then.
		f.root.do("PUT", rule, map[string]any{"siteRule": map[string]any{"value": true, "until": later}}).want(t, 200, "")
		r := f.root.do("PUT", "/api/admin/settings/features.domains", map[string]any{"value": false}).want(t, 409, "confirm_required")
		if p, _ := r.body["plan"].([]any); len(p) != 1 || p[0].(map[string]any)["tenant"] != f.tenB {
			t.Fatalf("the preview names B: %s", r.raw)
		}
		// A dated rule that keeps it on is direct.
		f.root.do("PUT", rule, map[string]any{"siteRule": map[string]any{"value": true, "from": later}}).want(t, 200, "")
		if v := featureOf(t, f.root, f.tenB, "features.domains"); v != true {
			t.Fatal(v)
		}
	})
}

// Plans can carry features; giving a tenant a plan without it, changing a
// plan, or a billing rule, switches it off as surely as a rule does.
func TestAPlanThatSwitchesDomainsOffNeedsTheConfirmation(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newDomFx(t, a)
		claim(t, f.admB, f.tenB, "acme.example")
		off := map[string]any{"features.domains": map[string]any{"value": false, "lower": map[string]any{"mode": "lock"}}}
		f.root.do("POST", "/api/admin/plans", map[string]any{"slug": "basic", "name": "Basic", "rules": off}).want(t, 201, "")
		f.root.do("POST", "/api/admin/plans", map[string]any{"slug": "team", "name": "Team"}).want(t, 201, "")
		plan := "/api/admin/tenants/" + f.tenB + "/plan"

		// Assigning a plan that has it off.
		r := f.root.do("PUT", plan, map[string]any{"plan": "basic"}).want(t, 409, "confirm_required")
		if p, _ := r.body["plan"].([]any); len(p) != 1 {
			t.Fatalf("the preview: %s", r.raw)
		}
		tok := f.root.do("POST", "/api/admin/confirm", map[string]any{"action": "change_capabilities", "key": "features.domains", "tenant": f.tenB}).want(t, 200, "").body["confirm"]
		f.root.do("PUT", plan, map[string]any{"plan": "basic", "confirm": tok}).want(t, 200, "")
		if v := featureOf(t, f.root, f.tenB, "features.domains"); v != false {
			t.Fatal(v)
		}
		// Back on a plan that leaves it on: direct.
		f.root.do("PUT", plan, map[string]any{"plan": "team"}).want(t, 200, "")

		// Changing the plan of the tenant: every tenant on it is looked at.
		f.root.do("PATCH", "/api/admin/plans/team", map[string]any{"rules": off}).want(t, 409, "confirm_required")
		f.root.do("PATCH", "/api/admin/plans/team", map[string]any{"description": "x"}).want(t, 200, "")
		tok = f.root.do("POST", "/api/admin/confirm", map[string]any{"action": "change_capabilities", "key": "features.domains"}).want(t, 200, "").body["confirm"]
		f.root.do("PATCH", "/api/admin/plans/team", map[string]any{"rules": off, "confirm": tok}).want(t, 200, "")
		if v := featureOf(t, f.root, f.tenB, "features.domains"); v != false {
			t.Fatal(v)
		}
		f.root.do("PATCH", "/api/admin/plans/team", map[string]any{"rules": map[string]any{"features.domains": nil}}).want(t, 200, "")

		// Billing (the internal writer) is refused the same way.
		err := settings.Of(a.st).SetBilling(t.Context(), f.tenB, "billing", map[string]*settings.Rule{"features.domains": {Value: false}})
		if se, ok := err.(*settings.Error); !ok || se.Code != "confirm_required" {
			t.Fatalf("billing: %v", err)
		}
		if v := featureOf(t, f.root, f.tenB, "features.domains"); v != true {
			t.Fatal(v)
		}
		// A tenant that uses nothing of it is billed directly.
		if err := settings.Of(a.st).SetBilling(t.Context(), f.tenC, "billing", map[string]*settings.Rule{"features.domains": {Value: false}}); err != nil {
			t.Fatal(err)
		}
	})
}
