package api_test

import (
	"context"
	"testing"
	"time"

	"petalbook.app/server/internal/auditlog"
	"petalbook.app/server/internal/store"
)

// GET /api/admin/tenants/{tenant}/overview: one call for the tenant's page,
// for the site's administrators (whole-site or narrowed) and the tenant's own.
func TestTenantOverview(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		p := "/api/admin/tenants/" + f.tenB + "/overview"
		inB := []string{scopeHeader, "tenant:" + f.tenB}

		check := func(who string, r result) {
			t.Helper()
			r.want(t, 200, "")
			tn := r.body["tenant"].(map[string]any)
			if tn["id"] != f.tenB || tn["slug"] != "acme" {
				t.Errorf("%s: tenant %v", who, tn)
			}
			if r.body["groups"].(float64) != 1 || r.body["adminCount"].(float64) != 1 {
				t.Errorf("%s: groups %v admins %v", who, r.body["groups"], r.body["adminCount"])
			}
			admins := r.body["admins"].([]any)
			if len(admins) != 1 || admins[0].(map[string]any)["username"] != "adm-b" {
				t.Errorf("%s: admins %v", who, admins)
			}
			if r.body["createdByName"] != "root" {
				t.Errorf("%s: created by %v", who, r.body["createdByName"])
			}
			if _, ok := r.body["featuresOff"].([]any); !ok {
				t.Errorf("%s: featuresOff %v", who, r.body["featuresOff"])
			}
			rows := r.body["recentAdminActivity"].([]any)
			if len(rows) == 0 || len(rows) > 10 {
				t.Errorf("%s: %d audit rows", who, len(rows))
			}
		}
		check("site admin", f.root.do("GET", p, nil))
		check("site admin in B's scope", f.root.do("GET", p, nil, inB...))
		check("B's administrator", f.admB.do("GET", p, nil))

		// Another tenant's administrator, and a member, are told there is no such tenant (or are refused).
		f.admA.do("GET", p, nil).want(t, 404, "not_found")
		f.memB.do("GET", p, nil).want(t, 403, "forbidden")
		f.root.do("GET", "/api/admin/tenants/"+f.tenB+"/overview", nil, scopeHeader, "tenant:"+store.DefaultTenantID).want(t, 404, "not_found")
		f.admB.do("GET", "/api/admin/tenants/"+fakeID+"/overview", nil).want(t, 404, "not_found")
	})
}

// The overview's recent administrative activity holds the rows an
// administrator acted in (a capacity) and the admin and security categories:
// not sign-ins, page reads or downloads, however many of them are newer.
func TestTenantOverviewRecentActivityIsAdministrative(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		ctx := context.Background()
		now := time.Now()
		var rows []store.AuditRow
		for i := 0; i < 15; i++ {
			rows = append(rows, store.AuditRow{ID: store.NewAuditID(now.Add(time.Hour)), At: now.Add(time.Hour), Kind: "login", TenantID: f.tenB, Username: "noise"})
		}
		capID := store.NewAuditID(now.Add(2 * time.Hour))
		rows = append(rows, store.AuditRow{ID: capID, At: now.Add(2 * time.Hour), Kind: "login", Category: "signins", TenantID: f.tenB, Username: "acting", Capacity: auditlog.CapacityTenantAdmin})
		if err := store.InsertAudit(ctx, a.st.Q(), rows); err != nil {
			t.Fatal(err)
		}
		r := f.root.do("GET", "/api/admin/tenants/"+f.tenB+"/overview", nil).want(t, 200, "")
		got := r.body["recentAdminActivity"].([]any)
		if len(got) < 2 {
			t.Fatalf("%d rows: %v", len(got), got)
		}
		if first := got[0].(map[string]any); first["id"] != capID {
			t.Errorf("the newest row is %v, want the one with a capacity", first)
		}
		for _, x := range got {
			row := x.(map[string]any)
			if row["username"] == "noise" {
				t.Errorf("a sign-in with no capacity is listed: %v", row)
			}
			if c := row["category"]; row["capacity"] == nil && c != "admin" && c != "security" {
				t.Errorf("row %v is neither administrative nor done in a capacity", row)
			}
		}
	})
}
