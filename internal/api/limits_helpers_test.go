package api_test

import "testing"

// totalsKeys are the settings that hold a tenant's totals.
var totalsKeys = map[string]string{"people": "quota.people", "spaces": "quota.spaces", "bytes": "quota.bytes", "pages": "quota.pages"}

// setTotals sets a tenant's totals (people, spaces, bytes, pages; nil is no
// limit) as a site administrator's rule for the tenant.
func setTotals(t *testing.T, root *client, tenant string, totals map[string]any) {
	t.Helper()
	for k, v := range totals {
		rule := any(nil)
		if v != nil {
			rule = map[string]any{"value": v}
		}
		root.do("PUT", "/api/admin/tenants/"+tenant+"/settings/"+totalsKeys[k], map[string]any{"siteRule": rule}).want(t, 200, "")
	}
}
