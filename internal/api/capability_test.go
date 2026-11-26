package api_test

import (
	"context"
	"encoding/json"
	"testing"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/limits"
	"petalbook.app/server/internal/store"
)

// A capability a tenant does not have refuses what it gates (403
// capability_off, naming it) to the tenant's administrators and people, never
// to a site administrator; turning it on again lets them through; and a route
// whose key has no capability does not notice. Every check reads the sheet in
// memory.
func TestACapabilityThatIsOffRefusesTheTenantNotTheSite(t *testing.T) {
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

		sheet := a.h.Limits()
		if sheet == nil {
			t.Fatal("accounts mode has no limits sheet")
		}
		off := map[string]*limits.Change{"data.adminView": {Value: json.RawMessage("false")}}
		if err := sheet.SetManual(ctx, store.DefaultTenantID, "root", off); err != nil {
			t.Fatal(err)
		}
		r := adm.do("POST", "/api/admin/spaces/"+sp+"/view", view).want(t, 403, "capability_off")
		if r.body["capability"] != "data.adminView" {
			t.Fatalf("%v", r.body)
		}
		// The check comes before the permission: a member is told the same.
		mem.do("POST", "/api/admin/spaces/"+sp+"/view", view).want(t, 403, "capability_off")
		// What has no capability is untouched, and so is the site administrator.
		adm.do("GET", "/api/users", nil).want(t, 200, "")
		root.do("POST", "/api/admin/spaces/"+sp+"/view", view).want(t, 200, "")

		off["data.adminView"] = nil // remove the override: back to the site's default (on)
		if err := sheet.SetManual(ctx, store.DefaultTenantID, "root", off); err != nil {
			t.Fatal(err)
		}
		adm.do("POST", "/api/admin/spaces/"+sp+"/view", view).want(t, 200, "")
	})
}
