package api_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"petalbook.app/server/internal/store"
)

// A site administrator who holds a narrow role in B still acts in B with the
// site's keys (the tenant keys they hold across the site stay): that is a site
// action, so entering is recorded once and the writes say site_admin. The list
// says the same.
func TestASiteAdministratorWithANarrowRoleInATenantActsAsSiteAdmin(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		inB := []string{scopeHeader, "tenant:" + f.tenB}
		grantRole(t, a, "RN", store.RoleScopeTenant, f.tenB, store.GrantToUser, mustUserID(t, a, "root"), "tenant.settings.read")

		f.root.do("GET", "/api/users", nil, inB...).want(t, 200, "")
		f.root.do("PATCH", "/api/users/"+f.userB, map[string]any{"name": "By root"}, inB...).want(t, 200, "")
		a.auth.FlushAudit(context.Background())
		if rows := f.admB.audit(t, "tenant_console_opened"); len(rows) != 1 || rows[0]["capacity"] != "site_admin" {
			t.Fatalf("entering B with site keys must be recorded once: %v", rows)
		}
		if rows := f.admB.audit(t, "user_changed"); len(rows) == 0 || rows[len(rows)-1]["capacity"] != "site_admin" {
			t.Fatalf("the write's row: %v", rows)
		}
		// The list reports what is enforced: the tenant's own keys do not cover what the console uses here.
		if got := scopeList(t, f.root); !slices.Equal(got, []string{"site:=site_admin", "tenant:" + f.tenB + "=site_admin"}) {
			t.Fatalf("the list: %v", got)
		}
	})
}

// Each entry of the list carries the keys the person holds in that scope: the
// tenant keys only in a tenant (what a request in its scope is held to), the
// site's own in the site.
func TestTheScopeListCarriesTheKeysOfEachScope(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		keysOf := func(c *client, kind, id string) []string {
			for _, it := range c.do("GET", "/api/admin/scopes", nil).want(t, 200, "").body["scopes"].([]any) {
				m := it.(map[string]any)
				if m["kind"] == kind && (kind == "site" || m["id"] == id) {
					return toStrings(m["permissions"])
				}
			}
			t.Fatalf("no %s %s in the list", kind, id)
			return nil
		}
		// A member of A with a narrow role in B holds those keys in B and no others.
		grantRole(t, a, "RB", store.RoleScopeTenant, f.tenB, store.GrantToUser, f.userA, "tenant.settings.read", "tenant.users.read")
		got := keysOf(f.memA, "tenant", f.tenB)
		slices.Sort(got)
		if want := []string{"tenant.settings.read", "tenant.users.read"}; !slices.Equal(got, want) {
			t.Errorf("a narrow role in B: %v", got)
		}
		// B's administrator holds every tenant key in B, and none of the site's.
		adm := keysOf(f.admB, "tenant", f.tenB)
		for _, k := range adm {
			if strings.HasPrefix(k, "site.") {
				t.Errorf("a site key in a tenant's entry: %s", k)
			}
		}
		if !slices.Contains(adm, "tenant.users.manage") {
			t.Errorf("B's administrator lacks tenant.users.manage")
		}
		// The site's entry has the site's keys.
		if !slices.Contains(keysOf(f.root, "site", ""), "site.tenants.manage") {
			t.Errorf("the site's entry lacks site.tenants.manage")
		}
		// B's administrator holds the whole role in B: its own keys cover the console there.
		if got := scopeList(t, f.admB); !slices.Equal(got, []string{"tenant:" + f.tenB + "=tenant_admin"}) {
			t.Errorf("B's administrator: %v", got)
		}
	})
}
