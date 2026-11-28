package api_test

import (
	"testing"
)

// Whether a tenant's administrators get the audit log is the site's: a switch
// (tenants.newAuditDefault, off) stamps each new tenant with a locked rule for
// features.tenantAudit; the site sets one tenant by hand.

// auditTenant makes a tenant and its administrator, and answers the tenant's
// id and the administrator's client.
func auditTenant(t *testing.T, a *acc, root *client, slug string) (string, *client) {
	t.Helper()
	adm := "adm-" + slug
	r := root.do("POST", "/api/admin/tenants", map[string]any{"slug": slug, "name": slug, "admin": map[string]any{"username": adm}}).want(t, 201, "")
	c := a.newClient("")
	c.do("POST", "/api/invites/accept", map[string]any{"token": r.body["invite"].(map[string]any)["token"], "username": adm, "name": adm, "password": pw}).want(t, 201, "")
	return r.body["id"].(string), c
}

// stampRows are the setting_changed rows of a tenant about features.tenantAudit, as the site reads them.
func stampRows(t *testing.T, a *acc, root *client, tenant string) []map[string]any {
	t.Helper()
	a.auth.FlushAudit(ctxBG)
	var out []map[string]any
	for _, it := range root.do("GET", "/api/admin/audit?kind=setting_changed&limit=200&tenant="+tenant, nil).want(t, 200, "").body["items"].([]any) {
		row := it.(map[string]any)
		if d, _ := row["detail"].(map[string]any); d["key"] == "features.tenantAudit" {
			out = append(out, row)
		}
	}
	return out
}

// tenantAuditOn is the tenant's own view of the switch: what its feature map
// says, and whether the audit routes answer.
func tenantAuditOn(t *testing.T, c *client) bool {
	t.Helper()
	on := c.do("GET", "/api/settings", nil).want(t, 200, "").body["features"].(map[string]any)["features.tenantAudit"] == true
	if code := c.do("GET", "/api/admin/audit", nil).code; on != (code == 200) {
		t.Fatalf("the feature map says %v and the audit answers %d", on, code)
	}
	return on
}

func TestANewTenantGetsTheSitesAuditDefaultAndNothingChangesLater(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		root.putSetting("features.tenancy", true).want(t, 200, "")
		if root.siteValue("tenants.newAuditDefault") != false {
			t.Fatal("the default is off")
		}
		// Off: the administrator of a new tenant has none, and no tab.
		idA, admA := auditTenant(t, a, root, "alpha")
		// The rule made with the tenant is in the audit log, as a site administrator's change of that tenant.
		if rows := stampRows(t, a, root, idA); len(rows) != 1 || rows[0]["capacity"] != "site_admin" || rows[0]["actor"] == nil ||
			rows[0]["detail"].(map[string]any)["layer"] != "tenant" || rows[0]["detail"].(map[string]any)["source"] != "manual" {
			t.Fatalf("the audit rows of the stamped rule: %v", rows)
		}
		admA.do("GET", "/api/admin/audit", nil).want(t, 403, "feature_off")
		admA.do("GET", "/api/admin/audit/kinds", nil).want(t, 403, "feature_off")
		if tenantAuditOn(t, admA) {
			t.Fatal("a tenant made while the default was off has the audit")
		}
		// The site turns the default on: the next tenant has it, the first still does not.
		root.putSetting("tenants.newAuditDefault", true).want(t, 200, "")
		_, admB := auditTenant(t, a, root, "bravo")
		if !tenantAuditOn(t, admB) || tenantAuditOn(t, admA) {
			t.Fatal("changing the default changed a tenant that exists, or missed the new one")
		}
		// And off again: the second keeps it.
		root.putSetting("tenants.newAuditDefault", false).want(t, 200, "")
		if !tenantAuditOn(t, admB) || tenantAuditOn(t, admA) {
			t.Fatal("changing the default back changed a tenant that exists")
		}
		// The tenant that was there before any rule (the default one) keeps its audit.
		admDefault := a.addUser("adm-default", pw, "admin")
		if !tenantAuditOn(t, admDefault) {
			t.Fatal("the default tenant lost its audit")
		}

		// The site sets one tenant by hand, both ways; a change of plan clears it like any manual rule.
		put := "/api/admin/tenants/" + idA + "/settings/features.tenantAudit"
		root.do("PUT", put, map[string]any{"siteLock": true, "value": true}).want(t, 200, "")
		if !tenantAuditOn(t, admA) {
			t.Fatal("the site turned it on for one tenant")
		}
		root.do("POST", "/api/admin/plans", map[string]any{"slug": "free", "name": "Free"}).want(t, 201, "")
		root.do("PUT", "/api/admin/tenants/"+idA+"/plan", map[string]any{"plan": "free"}).want(t, 200, "")
		if !tenantAuditOn(t, admA) {
			t.Fatal("the rule was on, and so is the default")
		}
		root.do("PUT", put, map[string]any{"siteLock": true, "value": false}).want(t, 200, "")
		if tenantAuditOn(t, admA) {
			t.Fatal("the site turned it off for one tenant")
		}
		// The owner's decision of 2026-11-27: no exception, the rule goes with the others.
		root.do("PUT", "/api/admin/tenants/"+idA+"/plan", map[string]any{"plan": "free"}).want(t, 200, "")
		if !tenantAuditOn(t, admA) {
			t.Fatal("a change of plan left the audit rule")
		}
	})
}

func TestATenantAdministratorCannotChangeTheAuditSwitchAndTheSiteAuditIsUnaffected(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		root.putSetting("features.tenancy", true).want(t, 200, "")
		id, adm := auditTenant(t, a, root, "alpha")
		put := "/api/admin/tenants/" + id + "/settings/features.tenantAudit"
		adm.do("PUT", put, map[string]any{"value": true}).want(t, 409, "setting_locked")
		adm.do("PUT", put, map[string]any{"siteLock": true, "value": true}).want(t, 409, "setting_locked")
		adm.do("PUT", put, map[string]any{"siteRule": map[string]any{"value": true}}).want(t, 409, "setting_locked")
		if tenantAuditOn(t, adm) {
			t.Fatal("the tenant turned its own audit on")
		}
		// The rows are written all the same; the site reads them.
		a.auth.FlushAudit(ctxBG)
		if len(auditKinds(t, root, "tenant_created")) != 1 {
			t.Fatal("the site audit lost the tenant's rows")
		}
		if r := root.do("GET", "/api/admin/audit?tenant="+id, nil).want(t, 200, ""); len(r.body["items"].([]any)) == 0 {
			t.Fatal("the site sees none of the tenant's rows")
		}
		root.do("GET", "/api/admin/audit/kinds", nil).want(t, 200, "")
		// The overview shows the tenant's administrators no activity while it is off, and the site all of it.
		ov := adm.do("GET", "/api/admin/tenants/"+id+"/overview", nil).want(t, 200, "")
		if n := len(ov.body["recentAdminActivity"].([]any)); n != 0 {
			t.Fatalf("the overview shows %d audit rows", n)
		}
		if ov := root.do("GET", "/api/admin/tenants/"+id+"/overview", nil).want(t, 200, ""); len(ov.body["recentAdminActivity"].([]any)) == 0 {
			t.Fatal("the site's overview lost the activity")
		}
		// On, and downloading needs its own feature as before.
		root.do("PUT", put, map[string]any{"siteLock": true, "value": true}).want(t, 200, "")
		adm.do("GET", "/api/admin/audit", nil).want(t, 200, "")
		adm.do("GET", "/api/admin/audit/kinds", nil).want(t, 200, "")
		_ = adm.do("GET", "/api/admin/audit/download", nil).want(t, 200, "")
		root.do("PUT", "/api/admin/tenants/"+id+"/settings/features.auditDownload", map[string]any{"siteLock": true, "value": false}).want(t, 200, "")
		adm.do("GET", "/api/admin/audit/download", nil).want(t, 403, "feature_off")
		adm.do("GET", "/api/admin/audit", nil).want(t, 200, "")
	})
}

func TestAnOrganisationAPersonRegistersGetsTheAuditDefaultToo(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root, srv := orgSetup(t, a)
		anon := a.newClient("")
		regOrg(anon, "Acme", "acme", nil).want(t, 202, "")
		anon.do("POST", "/api/register/confirm", map[string]any{"token": srv.Wait(1)[0].Token(t)}).want(t, 201, "")
		if tenantAuditOn(t, anon) {
			t.Fatal("an organisation made while the default was off has the audit")
		}
		id := root.do("GET", "/api/admin/tenants?origin=self", nil).want(t, 200, "").body["items"].([]any)[0].(map[string]any)["id"].(string)
		if rows := stampRows(t, a, root, id); len(rows) != 1 || rows[0]["capacity"] != nil && rows[0]["capacity"] != "" {
			t.Fatalf("the audit rows of the stamped rule: %v", rows)
		}
		root.putSetting("tenants.newAuditDefault", true).want(t, 200, "")
		other := a.newClient("")
		regOrg(other, "Beta", "beta", map[string]any{"username": "other", "email": "other@example.test"}).want(t, 202, "")
		other.do("POST", "/api/register/confirm", map[string]any{"token": srv.Wait(2)[1].Token(t)}).want(t, 201, "")
		if !tenantAuditOn(t, other) || tenantAuditOn(t, anon) {
			t.Fatal("the default was not stamped on the second organisation, or changed the first")
		}
	})
}
