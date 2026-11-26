package api_test

import (
	"testing"

	"petalbook.app/server/internal/store"
)

// sync.assetsRepository off: a space that has no attachment repository cannot
// get one; a space that has one keeps it.
func TestTheLimitsSheetRefusesANewAttachmentRepository(t *testing.T) {
	t.Parallel()
	n := &netSwitch{}
	eachAccounts(t, storageOpts(n, nil), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		mk := func(name string) string {
			r := filterRemote(t)
			pushVersions(t, r, 1)
			return admin.do("POST", "/api/spaces", map[string]any{"name": name, "remote": connectBody("file://" + r)}).want(t, 201, "").body["id"].(string)
		}
		first, second := mk("First"), mk("Second")
		set := func(v any) {
			admin.do("PUT", "/api/admin/tenants/"+store.DefaultTenantID+"/limits", map[string]any{"values": map[string]any{"sync.assetsRepository": v}}).want(t, 200, "")
		}
		admin.do("PUT", "/api/spaces/"+first+"/remote/assets", connectBody("file://"+filterRemote(t))).want(t, 200, "")
		set(false)
		admin.do("PUT", "/api/spaces/"+second+"/remote/assets", connectBody("file://"+filterRemote(t))).want(t, 409, "setting_restricted")
		admin.do("GET", "/api/spaces/"+second+"/remote/assets", nil).want(t, 404, "not_found")
		admin.do("PUT", "/api/spaces/"+first+"/remote/assets", connectBody("file://"+filterRemote(t))).want(t, 200, "")
		set(nil)
		admin.do("PUT", "/api/spaces/"+second+"/remote/assets", connectBody("file://"+filterRemote(t))).want(t, 200, "")
	})
}
