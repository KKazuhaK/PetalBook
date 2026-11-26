package api_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
)

// entryOf finds a key in the "limits" or "capabilities" list of an answer.
func entryOf(t *testing.T, r result, list, key string) map[string]any {
	t.Helper()
	for _, e := range r.body[list].([]any) {
		if m := e.(map[string]any); m["key"] == key {
			return m
		}
	}
	t.Fatalf("no %s in %s", key, r.raw)
	return nil
}

func TestTheLimitsSheetIsReadByTheTenantAndWrittenBySiteAdministrators(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		path := "/api/admin/tenants/" + f.tenB
		future := time.Now().UTC().Add(48 * time.Hour)
		ts := func(h int) string { return future.Add(time.Duration(h) * time.Hour).Format(time.RFC3339) }

		// Reading: the tenant's administrator, for their tenant only; every key, with its source.
		r := f.admB.do("GET", path+"/limits", nil).want(t, 200, "")
		if r.body["plan"] != nil || len(r.body["limits"].([]any)) < 25 {
			t.Fatal(r.raw)
		}
		if e := entryOf(t, r, "limits", "signin.sso"); e["value"] != true || e["source"] != "site" || e["kind"] != "boolean" {
			t.Fatalf("%v", e)
		}
		for _, e := range f.admB.do("GET", path+"/capabilities", nil).want(t, 200, "").body["capabilities"].([]any) {
			if k := e.(map[string]any)["key"].(string); strings.HasPrefix(k, "usage.") {
				t.Fatalf("%s is a limit, not a capability", k)
			}
		}
		f.admA.do("GET", path+"/limits", nil).want(t, 404, "not_found")
		// Writing: site administrators only.
		f.admB.do("PUT", path+"/limits", map[string]any{"values": map[string]any{"other.people": 5}}).want(t, 403, "forbidden")
		f.admB.do("PUT", path+"/capabilities", map[string]any{"values": map[string]any{"signin.sso": false}}).want(t, 403, "forbidden")

		// One encoding: a bare value, or {"value", "from", "until"}; null removes; {"value": null} is no limit.
		r = f.root.do("PUT", path+"/capabilities", map[string]any{"values": map[string]any{"data.adminView": false, "signin.sso": map[string]any{"value": false}}}).want(t, 200, "")
		if e := entryOf(t, r, "capabilities", "data.adminView"); e["value"] != false || e["source"] != "manual" {
			t.Fatalf("%v", e)
		}
		f.root.do("PUT", path+"/capabilities", map[string]any{"values": map[string]any{"other.people": 3}}).want(t, 400, "invalid")
		r = f.root.do("PUT", path+"/limits", map[string]any{"values": map[string]any{"other.people": map[string]any{"value": nil}, "storage.quota.bytes": 1000}}).want(t, 200, "")
		if e := entryOf(t, r, "limits", "other.people"); e["value"] != nil || e["source"] != "manual" {
			t.Fatalf("a manual no limit: %v", e)
		}
		if r.body["warning"] != nil {
			t.Fatal(r.raw)
		}
		for _, bad := range []any{map[string]any{}, map[string]any{"value": 1, "extra": 2}, map[string]any{"value": 1, "from": "soon"},
			map[string]any{"value": 1, "from": ts(2), "until": ts(1)}, -1, "big"} {
			f.root.do("PUT", path+"/limits", map[string]any{"values": map[string]any{"storage.quota.bytes": bad}}).want(t, 400, "invalid")
		}
		f.root.do("PUT", path+"/limits", map[string]any{"values": map[string]any{}}).want(t, 400, "invalid")
		f.root.do("PUT", path+"/limits", map[string]any{"values": map[string]any{"nope": 1}}).want(t, 400, "invalid")

		// A dated value replaces the undated manual one, and the answer says so.
		r = f.root.do("PUT", path+"/limits", map[string]any{"values": map[string]any{"storage.quota.bytes": map[string]any{"value": 5000, "from": ts(0), "until": ts(24)}}}).want(t, 200, "")
		w, _ := r.body["warning"].(map[string]any)
		if w == nil || w["code"] != "undated_value_replaced" || strings.Join(strs(w["keys"]), ",") != "storage.quota.bytes" {
			t.Fatalf("%s", r.raw)
		}
		// Outside its window the site's value applies, and the pending one is shown.
		e := entryOf(t, r, "limits", "storage.quota.bytes")
		if e["value"] != nil || e["source"] != "site" || e["manual"].(map[string]any)["value"] != float64(5000) {
			t.Fatalf("%v", e)
		}
		// The totals the tenant shows are the sheet's.
		if q := f.admB.do("GET", path, nil).want(t, 200, "").body["quota"].(map[string]any); q["people"] != nil || q["bytes"] != nil {
			t.Fatalf("%v", q)
		}
		r = f.root.do("PUT", path+"/limits", map[string]any{"values": map[string]any{"storage.quota.bytes": nil, "other.people": 7}}).want(t, 200, "")
		if e := entryOf(t, r, "limits", "storage.quota.bytes"); e["source"] != "site" || e["manual"] != nil {
			t.Fatalf("%v", e)
		}
		if e := entryOf(t, r, "limits", "other.people"); e["used"] == nil || e["value"] != float64(7) {
			t.Fatalf("%v", e)
		}

		// Every change is audited.
		a.auth.FlushAudit(context.Background())
		if rows := f.root.audit(t, "capability_changed"); len(rows) != 2 {
			t.Fatalf("%v", rows)
		}
		rows := f.root.audit(t, "tenant_limit_changed")
		if len(rows) < 4 {
			t.Fatalf("%d rows", len(rows))
		}
		d := rows[len(rows)-1]["detail"].(map[string]any)
		if d["tenant"] != f.tenB || d["key"] == nil || d["source"] == nil {
			t.Fatalf("%v", d)
		}
	})
}

func TestPlansAreSetsOfValuesAndAChangeOfPlanReplacesTheOverrides(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		path := "/api/admin/tenants/" + f.tenB
		f.admB.do("GET", "/api/admin/plans", nil).want(t, 403, "forbidden")
		f.root.do("POST", "/api/admin/plans", map[string]any{"slug": "Bad Slug", "name": "x"}).want(t, 400, "invalid")
		f.root.do("POST", "/api/admin/plans", map[string]any{"slug": "team", "name": "Team", "values": map[string]any{"other.people": "many"}}).want(t, 400, "invalid")
		r := f.root.do("POST", "/api/admin/plans", map[string]any{"slug": "team", "name": "Team", "description": "Small teams", "values": map[string]any{"other.people": 10, "signin.sso": false}}).want(t, 201, "")
		if r.body["slug"] != "team" || r.body["tenants"] != float64(0) {
			t.Fatal(r.raw)
		}
		f.root.do("POST", "/api/admin/plans", map[string]any{"slug": "team", "name": "Again"}).want(t, 409, "exists")
		if n := len(f.root.do("GET", "/api/admin/plans", nil).want(t, 200, "").body["plans"].([]any)); n != 1 {
			t.Fatal(n)
		}
		f.root.do("GET", "/api/admin/plans/none", nil).want(t, 404, "not_found")

		f.root.do("PUT", path+"/limits", map[string]any{"values": map[string]any{"other.people": 99, "usage.mailPerDay": 50}}).want(t, 200, "")
		f.root.do("PUT", path+"/plan", map[string]any{"plan": "none"}).want(t, 400, "invalid")
		r = f.root.do("PUT", path+"/plan", map[string]any{"plan": "team"}).want(t, 200, "")
		cleared := r.body["cleared"].([]any)
		if r.body["plan"].(map[string]any)["slug"] != "team" || len(cleared) != 2 || cleared[0].(map[string]any)["key"] != "other.people" {
			t.Fatal(r.raw)
		}
		r = f.admB.do("GET", path+"/limits", nil).want(t, 200, "")
		if e := entryOf(t, r, "limits", "other.people"); e["value"] != float64(10) || e["source"] != "plan" {
			t.Fatalf("%v", e)
		}
		if e := entryOf(t, r, "limits", "usage.mailPerDay"); e["source"] != "site" {
			t.Fatalf("the overrides are cleared: %v", e)
		}

		// A change of the plan reaches the tenants on it, and a plan in use stays.
		r = f.root.do("PATCH", "/api/admin/plans/team", map[string]any{"values": map[string]any{"other.people": 20, "signin.sso": nil}}).want(t, 200, "")
		if got := strs(r.body["affected"]); len(got) != 1 || got[0] != f.tenB || r.body["tenants"] != float64(1) {
			t.Fatal(r.raw)
		}
		if e := entryOf(t, f.admB.do("GET", path+"/limits", nil).want(t, 200, ""), "limits", "other.people"); e["value"] != float64(20) {
			t.Fatalf("%v", e)
		}
		f.root.do("PATCH", "/api/admin/plans/team", map[string]any{"slug": "other"}).want(t, 400, "invalid")
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
		for _, kind := range []string{"plan_created", "plan_changed", "plan_deleted", "tenant_plan_changed"} {
			if len(f.root.audit(t, kind)) == 0 {
				t.Fatalf("no %s row", kind)
			}
		}
		// The rows carry what a value was and what it became.
		d := f.root.audit(t, "plan_changed")[0]["detail"].(map[string]any)
		from, to := d["from"].(map[string]any), d["to"].(map[string]any)
		if from["other.people"] != float64(10) || to["other.people"] != float64(20) || from["signin.sso"] != false || to["signin.sso"] != nil {
			t.Fatalf("plan_changed: %v", d)
		}
		rows := f.root.audit(t, "tenant_plan_changed")
		var clearedRows []any
		for _, row := range rows { // the row that gave the plan "team" is the one that cleared the overrides
			if c, _ := row["detail"].(map[string]any)["cleared"].([]any); len(c) > 0 {
				clearedRows = c
			}
		}
		if len(clearedRows) != 2 {
			t.Fatalf("tenant_plan_changed: %v", rows)
		}
		if c := clearedRows[0].(map[string]any); c["key"] != "other.people" || c["value"] != float64(99) {
			t.Fatalf("the cleared override has no value: %v", c)
		}
	})
}

func TestTheSiteDefaultsAreTheBottomOfEverySheet(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		f.admB.do("GET", "/api/admin/capabilities", nil).want(t, 403, "forbidden")
		r := f.root.do("GET", "/api/admin/capabilities", nil).want(t, 200, "")
		if e := entryOf(t, r, "capabilities", "signin.idpAdmins"); e["value"] != false || e["group"] != "signin" {
			t.Fatalf("%v", e)
		}
		f.root.do("PUT", "/api/admin/capabilities", map[string]any{"values": map[string]any{"signin.idpAdmins": map[string]any{"value": true, "from": "2030-01-01T00:00:00Z"}}}).want(t, 400, "invalid")
		r = f.root.do("PUT", "/api/admin/capabilities", map[string]any{"values": map[string]any{"signin.idpAdmins": true}}).want(t, 200, "")
		if len(strs(r.body["affected"])) < 2 {
			t.Fatalf("every tenant's sheet changed: %s", r.raw)
		}
		if e := entryOf(t, f.admB.do("GET", "/api/admin/tenants/"+f.tenB+"/capabilities", nil).want(t, 200, ""), "capabilities", "signin.idpAdmins"); e["value"] != true || e["source"] != "site" {
			t.Fatalf("%v", e)
		}
		f.root.do("PUT", "/api/admin/capabilities", map[string]any{"values": map[string]any{"signin.idpAdmins": nil}}).want(t, 200, "")
		a.auth.FlushAudit(context.Background())
		rows := f.root.audit(t, "capabilities_default_changed")
		if len(rows) != 2 {
			t.Fatal("audit")
		}
		// Newest first: the second write took the value away, the first set it.
		for i, want := range [][2]any{{true, false}, {false, true}} {
			d := rows[i]["detail"].(map[string]any)
			from, to := d["from"].(map[string]any), d["to"].(map[string]any)
			if from["signin.idpAdmins"] != want[0] || to["signin.idpAdmins"] != want[1] {
				t.Fatalf("row %d: %v", i, d)
			}
		}
	})
}

// A deleted tenant leaves the service's memory with its rows, and the totals
// are read from the sheet, not from the tenant row.
func TestADeletedTenantLeavesTheSheetAndNoTotalIsReadFromTheTenantRow(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		root.do("PUT", "/api/admin/tenancy", map[string]any{"enabled": true}).want(t, 200, "")
		id := root.do("POST", "/api/admin/tenants", map[string]any{"slug": "tmp", "name": "Temp"}).want(t, 201, "").body["id"].(string)
		root.do("PUT", "/api/admin/tenants/"+id+"/limits", map[string]any{"values": map[string]any{"other.people": 3}}).want(t, 200, "")
		sheets := a.h.Limits()
		if n, ok := sheets.Sheet(id).Limit("other.people"); !ok || n != 3 {
			t.Fatal(n)
		}
		root.do("DELETE", "/api/admin/tenants/"+id, nil).want(t, 204, "")
		if _, ok := sheets.Sheet(id).Limit("other.people"); ok {
			t.Fatal("the deleted tenant's sheet is still held")
		}
		rows, err := store.ListTenantLimits(context.Background(), a.st.Q())
		if err != nil || len(rows) != 0 {
			t.Fatalf("%v %v", rows, err)
		}
	})
}

// sync.assetsRepository off: a tenant's spaces keep their attachments in the space.
func TestTheLimitsSheetTurnsTheAttachmentRepositoryOff(t *testing.T) {
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		admin, member := ghSetup(t, a)
		get := func() map[string]any {
			return settingOf(t, member.do("GET", "/api/settings", nil).want(t, 200, ""), "storage.assets")
		}
		if s := get(); s["effective"] != "repository" || strings.Join(strs(s["allowed"]), ",") != "space,repository" {
			t.Fatal(s)
		}
		admin.do("PUT", "/api/admin/tenants/"+store.DefaultTenantID+"/limits", map[string]any{"values": map[string]any{"sync.assetsRepository": false}}).want(t, 200, "")
		if s := get(); s["effective"] != "space" || strings.Join(strs(s["allowed"]), ",") != "space" {
			t.Fatal(s)
		}
		admin.do("PUT", "/api/admin/tenants/"+store.DefaultTenantID+"/limits", map[string]any{"values": map[string]any{"sync.assetsRepository": nil}}).want(t, 200, "")
		if s := get(); s["effective"] != "repository" {
			t.Fatal(s)
		}
	})
}
