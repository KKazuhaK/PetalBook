package api_test

import (
	"strings"
	"testing"

	"github.com/KKazuhaK/PetalBook/internal/store"
)

func TestAttachmentsAreSeparateByDefaultAndTheLayeredRulesDecideOverTheAPI(t *testing.T) {
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		admin, member := ghSetup(t, a)
		mk := func(name string, assets any) result {
			f := map[string]any{"owner": "kazuhahub", "name": name, "private": true, "create": true}
			if assets != nil {
				f["assets"] = assets
			}
			return member.do("POST", "/api/spaces", map[string]any{"name": name, "storage": ghSync(f)})
		}
		// Nothing said: a repository of attachments is made beside the space's.
		mk("a", nil).want(t, 201, "")
		if a.fake.RepoByName("kazuhahub/a-assets") == nil {
			t.Fatal("no attachment repository by default")
		}
		// The administrators see the choice and the switch.
		r := admin.do("GET", "/api/admin/settings", nil).want(t, 200, "")
		s := settingOf(t, r, "storage.assets")
		if s["site"].(map[string]any)["value"] != "repository" || strings.Join(strs(s["choices"]), ",") != "space,repository" {
			t.Fatal(s)
		}
		if x := settingOf(t, r, "storage.assets.ownStorageExempt"); x["site"].(map[string]any)["value"] != true || strings.Join(strs(x["layers"]), ",") != "site" {
			t.Fatal(x)
		}
		// The site locks it to the space: a request that does not say gets none, one that asks is refused.
		admin.do("PUT", "/api/admin/settings/storage.assets", map[string]any{"value": "space", "lower": map[string]any{"mode": "lock"}}).want(t, 200, "")
		mk("b", nil).want(t, 201, "")
		if a.fake.RepoByName("kazuhahub/b-assets") != nil {
			t.Fatal("an attachment repository under the site's lock")
		}
		mk("c", true).want(t, 409, "setting_locked")
		if a.fake.RepoByName("kazuhahub/c") != nil {
			t.Fatal("a repository was made for a refused request")
		}
		// A site lock for this one tenant beats the site's default and is not the tenant's to lift.
		def := "/api/admin/tenants/" + store.DefaultTenantID + "/settings/storage.assets"
		admin.do("PUT", def, map[string]any{"value": "repository", "siteLock": true}).want(t, 200, "")
		admin.do("PUT", "/api/admin/settings/storage.assets", map[string]any{"lower": map[string]any{"mode": "allow"}}).want(t, 200, "")
		mk("d", nil).want(t, 201, "")
		if a.fake.RepoByName("kazuhahub/d-assets") == nil {
			t.Fatal("the lock for the tenant did not beat the site's default")
		}
		mk("e", false).want(t, 409, "setting_locked")
		tv := settingOf(t, admin.do("GET", "/api/admin/tenants/"+store.DefaultTenantID+"/settings", nil).want(t, 200, ""), "storage.assets")["tenant"].(map[string]any)
		if tv["siteLocked"] != true || tv["value"] != "repository" {
			t.Fatal(tv)
		}
		// The key is the tenant layer's only; the site's own layer has no lock for a tenant.
		admin.do("PUT", "/api/admin/settings/storage.assets", map[string]any{"siteLock": true}).want(t, 400, "invalid")
	})
}
