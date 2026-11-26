package api_test

import (
	"testing"

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
