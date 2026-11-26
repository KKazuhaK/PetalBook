package api_test

import "testing"

// totalsKeys are the keys of the limits sheet that hold a tenant's totals.
var totalsKeys = map[string]string{"people": "other.people", "spaces": "storage.quota.spaces", "bytes": "storage.quota.bytes", "pages": "storage.quota.pages"}

// setTotals sets a tenant's totals (people, spaces, bytes, pages; nil is no
// limit) in its limits sheet, as a site administrator.
func setTotals(t *testing.T, root *client, tenant string, totals map[string]any) {
	t.Helper()
	values := map[string]any{}
	for k, v := range totals {
		values[totalsKeys[k]] = map[string]any{"value": v}
	}
	root.do("PUT", "/api/admin/tenants/"+tenant+"/limits", map[string]any{"values": values}).want(t, 200, "")
}
