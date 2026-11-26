package api_test

import (
	"context"
	"testing"
	"time"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/store"
)

// ruleOf finds the rule of one source in the rules a tenant's setting lists.
func ruleOf(t *testing.T, r result, key, source string) map[string]any {
	t.Helper()
	for _, s := range r.body["settings"].([]any) {
		m := s.(map[string]any)
		if m["key"] != key {
			continue
		}
		tv, _ := m["tenant"].(map[string]any)
		for _, x := range tv["rules"].([]any) {
			if rule := x.(map[string]any); rule["source"] == source {
				return rule
			}
		}
		return nil
	}
	t.Fatalf("no setting %s in %s", key, r.raw)
	return nil
}

// A feature a tenant does not have refuses what it gates (403 feature_off,
// naming it) to the tenant's administrators and people, never to a site
// administrator; lifting the rule lets them through; and a route whose key has
// no feature does not notice.
func TestAFeatureThatIsOffRefusesTheTenantNotTheSite(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		ctx := context.Background()
		root := a.setup("root", pw)
		if _, err := a.auth.CreateUser(ctx, auth.CreateParams{Username: "adm", Role: "admin", Password: pw, TenantID: store.DefaultTenantID}); err != nil {
			t.Fatal(err)
		}
		adm := a.newClient("")
		adm.do("POST", "/api/login", map[string]any{"username": "adm", "password": pw}).want(t, 200, "")
		mem := a.addUser("mem", pw, "member")
		sp := mem.do("POST", "/api/spaces", map[string]any{"name": "Notes"}).want(t, 201, "").body["id"].(string)
		view := map[string]any{"reason": "a check"}

		// By default everything is on.
		adm.do("POST", "/api/admin/spaces/"+sp+"/view", view).want(t, 200, "")
		adm.do("DELETE", "/api/admin/spaces/"+sp+"/view", nil).want(t, 204, "")

		off := "/api/admin/tenants/" + store.DefaultTenantID + "/settings/features.adminView"
		root.do("PUT", off, map[string]any{"siteLock": true, "value": false}).want(t, 200, "")
		r := adm.do("POST", "/api/admin/spaces/"+sp+"/view", view).want(t, 403, "feature_off")
		if r.body["feature"] != "features.adminView" {
			t.Fatalf("%v", r.body)
		}
		// The check comes before the permission: a member is told the same.
		mem.do("POST", "/api/admin/spaces/"+sp+"/view", view).want(t, 403, "feature_off")
		// What has no feature is untouched, and so is the site administrator.
		adm.do("GET", "/api/users", nil).want(t, 200, "")
		root.do("POST", "/api/admin/spaces/"+sp+"/view", view).want(t, 200, "")

		root.do("PUT", off, map[string]any{"siteRule": nil}).want(t, 200, "")
		adm.do("POST", "/api/admin/spaces/"+sp+"/view", view).want(t, 200, "")
	})
}

// The own SMTP sender is off unless a rule turns it on, and what the limits
// sheet answered is gone.
func TestTheOwnSenderIsOffByDefaultAndTheSheetIsGone(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		got := settingOf(t, f.admB.do("GET", "/api/admin/tenants/"+f.tenB+"/settings", nil).want(t, 200, ""), "mail.ownSender")
		if got["effective"] != false {
			t.Fatalf("%v", got)
		}
		tenant := "/api/admin/tenants/" + f.tenB
		for _, rt := range [][2]string{{"GET", "/api/admin/capabilities"}, {"PUT", "/api/admin/capabilities"}, {"GET", tenant + "/limits"},
			{"PUT", tenant + "/limits"}, {"GET", tenant + "/capabilities"}, {"PUT", tenant + "/capabilities"}} {
			f.root.do(rt[0], rt[1], map[string]any{}).want(t, 404, "")
		}
	})
}

// A manual quota rule with a window refuses the tenant inside it and not
// after it, and the clock alone decides (no timer is needed to read it).
func TestAManualQuotaWithAWindowHoldsOnlyInsideIt(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		path := "/api/admin/tenants/" + f.tenB + "/settings/quota.spaces"
		now := a.clock.Now()
		from, until := now.Add(time.Hour), now.Add(2*time.Hour)
		mk := func() result { return f.memB.do("POST", "/api/spaces", map[string]any{"name": "Extra"}) }
		rule := map[string]any{"value": 1, "from": from.Format(time.RFC3339), "until": until.Format(time.RFC3339)}
		r := f.root.do("PUT", path, map[string]any{"siteRule": rule}).want(t, 200, "")
		if r.body["tenant"].(map[string]any)["next"] != from.Format(time.RFC3339) {
			t.Fatalf("next is the start of the window: %s", r.raw)
		}
		f.root.do("PUT", path, map[string]any{"siteRule": map[string]any{"value": 1, "from": until.Format(time.RFC3339), "until": from.Format(time.RFC3339)}}).want(t, 400, "invalid")
		f.root.do("PUT", path, map[string]any{"siteRule": map[string]any{"value": 1, "until": now.Add(-time.Hour).Format(time.RFC3339)}}).want(t, 400, "setting_until_past")
		a.clock.Advance(time.Hour)
		mk().want(t, 403, "quota_exceeded")
		a.clock.Advance(time.Hour)
		mk().want(t, 201, "")
	})
}

// A plan with a restriction, a tenant on it, a manual override that cannot go
// above it, and a change of plan that clears every manual rule, the undated
// override and the dated grant alike.
func TestAPlanRestrictsAndAChangeOfPlanClearsTheManualRules(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		path := "/api/admin/tenants/" + f.tenB
		f.admB.do("GET", "/api/admin/plans", nil).want(t, 403, "forbidden")
		f.root.do("POST", "/api/admin/plans", map[string]any{"slug": "Bad Slug", "name": "x"}).want(t, 400, "invalid")
		f.root.do("POST", "/api/admin/plans", map[string]any{"slug": "team", "name": "Team", "rules": map[string]any{"quota.people": map[string]any{"value": "many"}}}).want(t, 400, "invalid")
		f.root.do("GET", "/api/admin/plans/team", nil).want(t, 404, "not_found") // the refused plan was not made
		f.root.do("POST", "/api/admin/plans", map[string]any{"slug": "team", "name": "Team", "rules": map[string]any{
			"quota.people": map[string]any{"value": 50, "lower": map[string]any{"mode": "restrict", "max": 10}}}}).want(t, 400, "setting_above_max")
		r := f.root.do("POST", "/api/admin/plans", map[string]any{"slug": "team", "name": "Team", "description": "Small teams", "rules": map[string]any{
			"quota.people": map[string]any{"lower": map[string]any{"mode": "restrict", "max": 10}}, "features.ownSso": map[string]any{"value": false, "lower": map[string]any{"mode": "lock"}}}}).want(t, 201, "")
		if r.body["slug"] != "team" || r.body["tenants"] != float64(0) || len(r.body["rules"].(map[string]any)) != 2 {
			t.Fatal(r.raw)
		}
		f.root.do("POST", "/api/admin/plans", map[string]any{"slug": "team", "name": "Again"}).want(t, 409, "exists")

		// The tenant has its own adjustments first: an undated one and a dated grant.
		until := a.clock.Now().Add(72 * time.Hour).Format(time.RFC3339)
		f.root.do("PUT", path+"/settings/quota.pages", map[string]any{"siteRule": map[string]any{"value": 500}}).want(t, 200, "")
		f.root.do("PUT", path+"/settings/quota.bytes", map[string]any{"siteRule": map[string]any{"value": 1000, "until": until}}).want(t, 200, "")
		f.root.do("PUT", path+"/plan", map[string]any{"plan": "none"}).want(t, 400, "invalid")
		r = f.root.do("PUT", path+"/plan", map[string]any{"plan": "team"}).want(t, 200, "")
		cleared := r.body["cleared"].([]any)
		if _, has := r.body["kept"]; has || r.body["plan"].(map[string]any)["slug"] != "team" || len(cleared) != 2 ||
			cleared[0].(map[string]any)["key"] != "quota.bytes" || cleared[1].(map[string]any)["key"] != "quota.pages" {
			t.Fatal(r.raw)
		}
		s := f.admB.do("GET", path+"/settings", nil).want(t, 200, "")
		if e := settingOf(t, s, "quota.people"); e["effective"] != float64(10) {
			t.Fatalf("no limit under the plan's max of 10 is 10: %v", e)
		}
		if e := settingOf(t, s, "quota.pages"); e["effective"] != nil {
			t.Fatalf("the undated rule is cleared: %v", e)
		}
		if e := settingOf(t, s, "quota.bytes"); e["effective"] != nil {
			t.Fatalf("the dated rule is cleared too: %v", e)
		}
		if ruleOf(t, s, "features.ownSso", "plan")["inEffect"] != true {
			t.Fatal("the plan's lock is in effect")
		}
		// A tenant cannot set its own total, nor can a manual rule hold more than its own max.
		f.admB.do("PUT", path+"/settings/quota.people", map[string]any{"value": 50}).want(t, 409, "setting_locked")
		f.root.do("PUT", path+"/settings/quota.people", map[string]any{"siteRule": map[string]any{"value": 30, "lower": map[string]any{"mode": "restrict", "max": 20}}}).want(t, 400, "setting_above_max")

		// A change of the plan reaches the tenants on it, and a plan in use stays.
		r = f.root.do("PATCH", "/api/admin/plans/team", map[string]any{"rules": map[string]any{
			"quota.people": map[string]any{"lower": map[string]any{"mode": "restrict", "max": 20}}, "features.ownSso": nil}}).want(t, 200, "")
		if got := strs(r.body["affected"]); len(got) != 1 || got[0] != f.tenB || r.body["tenants"] != float64(1) {
			t.Fatal(r.raw)
		}
		if e := settingOf(t, f.admB.do("GET", path+"/settings", nil).want(t, 200, ""), "quota.people"); e["effective"] != float64(20) {
			t.Fatalf("%v", e)
		}
		r = f.root.do("DELETE", "/api/admin/plans/team", nil).want(t, 409, "plan_in_use")
		if r.body["tenants"] != float64(1) {
			t.Fatal(r.raw)
		}
		if r = f.root.do("PUT", path+"/plan", map[string]any{"plan": nil}).want(t, 200, ""); r.body["plan"] != nil {
			t.Fatal(r.raw)
		}
		f.root.do("DELETE", "/api/admin/plans/team", nil).want(t, 204, "")
		f.root.do("GET", "/api/admin/plans/team", nil).want(t, 404, "not_found")

		a.auth.FlushAudit(context.Background())
		for _, kind := range []string{"plan_created", "plan_changed", "plan_deleted", "tenant_plan_changed", "setting_changed"} {
			if len(f.root.audit(t, kind)) == 0 {
				t.Fatalf("no %s row", kind)
			}
		}
	})
}

// The timer's boundary writes one audit row for the end of a window, with no
// actor, and looks at the quotas again.
func TestTheEndOfAWindowIsAuditedOnceWithNoActor(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		path := "/api/admin/tenants/" + f.tenB + "/settings/quota.people"
		until := a.clock.Now().Add(time.Hour)
		f.root.do("PUT", path, map[string]any{"siteRule": map[string]any{"value": 1, "until": until.Format(time.RFC3339)}}).want(t, 200, "")
		a.h.CheckBoundaries()
		if n := peopleOver(t, f, 2); n == 0 {
			t.Fatal("tenant B has two people over a limit of one")
		}
		a.clock.Advance(time.Hour)
		a.h.CheckBoundaries()
		a.h.CheckBoundaries()
		a.auth.FlushAudit(context.Background())
		rows := f.root.audit(t, "setting_rule_ended")
		if len(rows) != 1 || rows[0]["actor"] != nil && rows[0]["actor"] != "" {
			t.Fatalf("%v", rows)
		}
		if d := rows[0]["detail"].(map[string]any); d["key"] != "quota.people" || d["tenant"] != f.tenB || d["source"] != "manual" {
			t.Fatalf("%v", d)
		}
	})
}

// peopleOver counts the over-quota rows for people of a tenant whose total of
// people was used (the rows carry the figures, not the tenant).
func peopleOver(t *testing.T, f *tenFx, used float64) int {
	t.Helper()
	f.a.auth.FlushAudit(context.Background())
	n := 0
	for _, row := range f.root.audit(t, "quota_exceeded") {
		if d, _ := row["detail"].(map[string]any); d["quota"] == "people" && d["scope"] == "tenant" && d["used"] == used {
			n++
		}
	}
	return n
}

// A tenant's totals are the site's: a tenant administrator cannot write them,
// as a value or as a rule, so they cannot raise what their tenant may use.
// The other settings a site rule allows a tenant (features.*, rate.*,
// storage.pinnedBytes, mail.ownSender) are refused the same way.
func TestATenantAdministratorCannotRaiseTheTenantsOwnAllowance(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		setTotals(t, f.root, f.tenB, map[string]any{"spaces": 1})
		base := "/api/admin/tenants/" + f.tenB
		quotaOf := func() any {
			return f.root.do("GET", base, nil).want(t, 200, "").body["quota"].(map[string]any)["spaces"]
		}
		if got := quotaOf(); got != float64(1) {
			t.Fatalf("spaces total %v", got)
		}
		for _, key := range []string{"quota.spaces", "quota.people", "quota.bytes", "quota.pages", "rate.apiPerMinute", "storage.pinnedBytes"} {
			f.admB.do("PUT", base+"/settings/"+key, map[string]any{"value": 1 << 40}).want(t, 409, "setting_locked")
			f.admB.do("PUT", base+"/settings/"+key, map[string]any{"value": nil}).want(t, 409, "setting_locked")
		}
		for _, key := range []string{"features.adminView", "mail.ownSender"} {
			f.admB.do("PUT", base+"/settings/"+key, map[string]any{"value": true}).want(t, 409, "setting_locked")
		}
		f.admB.do("PUT", base+"/settings/quota.spaces", map[string]any{"lower": map[string]any{"mode": "allow"}}).want(t, 409, "setting_locked")
		if got := quotaOf(); got != float64(1) {
			t.Fatalf("a tenant administrator changed the spaces total to %v", got)
		}
	})
}

// Who set a rule is a site administrator's id: the site's administrators see
// it, the tenant's administrators see the rule without it.
func TestATenantAdministratorSeesARuleButNotWhoSetIt(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		path := "/api/admin/tenants/" + f.tenB + "/settings"
		r := f.root.do("PUT", path+"/quota.spaces", map[string]any{"siteRule": map[string]any{"value": 3}}).want(t, 200, "")
		if r.body["tenant"].(map[string]any)["rules"].([]any)[0].(map[string]any)["setBy"] == nil {
			t.Fatalf("the site administrator sees who set it: %s", r.raw)
		}
		if rule := ruleOf(t, f.root.do("GET", path, nil).want(t, 200, ""), "quota.spaces", "manual"); rule["setBy"] == nil {
			t.Fatalf("%v", rule)
		}
		rule := ruleOf(t, f.admB.do("GET", path, nil).want(t, 200, ""), "quota.spaces", "manual")
		if _, has := rule["setBy"]; has || rule["value"] != float64(3) {
			t.Fatalf("the tenant administrator sees the rule, not who set it: %v", rule)
		}
		// The cached view is not changed for the next reader.
		if rule := ruleOf(t, f.root.do("GET", path, nil).want(t, 200, ""), "quota.spaces", "manual"); rule["setBy"] == nil {
			t.Fatalf("%v", rule)
		}
	})
}
