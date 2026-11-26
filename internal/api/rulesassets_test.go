package api_test

import (
	"strings"
	"testing"

	"petalbook.app/server/internal/store"
)

// A rule for the tenant restricting storage.assets to "space" (what the limits
// sheet's sync.assetsRepository was): a space that has no attachment repository
// cannot get one; a space that has one keeps it.
func TestARuleRestrictingTheAttachmentsRefusesANewAttachmentRepository(t *testing.T) {
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
		set := func(rule any) {
			admin.do("PUT", "/api/admin/tenants/"+store.DefaultTenantID+"/settings/storage.assets", map[string]any{"siteRule": rule}).want(t, 200, "")
		}
		admin.do("PUT", "/api/spaces/"+first+"/remote/assets", connectBody("file://"+filterRemote(t))).want(t, 200, "")
		set(map[string]any{"lower": map[string]any{"mode": "restrict", "options": []string{"space"}}})
		admin.do("PUT", "/api/spaces/"+second+"/remote/assets", connectBody("file://"+filterRemote(t))).want(t, 409, "setting_restricted")
		admin.do("GET", "/api/spaces/"+second+"/remote/assets", nil).want(t, 404, "not_found")
		admin.do("PUT", "/api/spaces/"+first+"/remote/assets", connectBody("file://"+filterRemote(t))).want(t, 200, "")
		set(nil)
		admin.do("PUT", "/api/spaces/"+second+"/remote/assets", connectBody("file://"+filterRemote(t))).want(t, 200, "")
	})
}

func TestARuleRestrictingTheAttachmentsShowsInTheCallersSettings(t *testing.T) {
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		admin, member := ghSetup(t, a)
		get := func() map[string]any {
			return settingOf(t, member.do("GET", "/api/settings", nil).want(t, 200, ""), "storage.assets")
		}
		if s := get(); s["effective"] != "repository" || strings.Join(strs(s["allowed"]), ",") != "space,repository" {
			t.Fatal(s)
		}
		path := "/api/admin/tenants/" + store.DefaultTenantID + "/settings/storage.assets"
		admin.do("PUT", path, map[string]any{"siteRule": map[string]any{"lower": map[string]any{"mode": "restrict", "options": []string{"space"}}}}).want(t, 200, "")
		if s := get(); s["effective"] != "space" || strings.Join(strs(s["allowed"]), ",") != "space" {
			t.Fatal(s)
		}
		admin.do("PUT", path, map[string]any{"siteRule": nil}).want(t, 200, "")
		if s := get(); s["effective"] != "repository" {
			t.Fatal(s)
		}
	})
}
