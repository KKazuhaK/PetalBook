package api_test

import (
	"context"
	"slices"
	"testing"

	"petalbook.app/server/internal/store"
)

// scopeList is GET /api/admin/scopes as rows of "kind:id=capacity".
func scopeList(t *testing.T, c *client, hdr ...string) []string {
	t.Helper()
	var out []string
	for _, it := range c.do("GET", "/api/admin/scopes", nil, hdr...).want(t, 200, "").body["scopes"].([]any) {
		m := it.(map[string]any)
		id, _ := m["id"].(string)
		out = append(out, m["kind"].(string)+":"+id+"="+m["capacity"].(string))
	}
	return out
}

// The console's scope selector lists exactly the places the person may act in:
// the site for whoever holds the tenant keys across it, then every tenant they
// administer, by name; nothing for a person who administers nothing.
func TestTheScopeListHoldsExactlyWhatThePersonAdministers(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		eq := func(what string, got, want []string) {
			t.Helper()
			if !slices.Equal(got, want) {
				t.Errorf("%s: got %v, want %v", what, got, want)
			}
		}
		// A site administrator holds the site; tenants are entered from the tenant list, not listed here.
		eq("root", scopeList(t, f.root), []string{"site:=site_admin"})
		// A tenant's administrator holds their own tenant alone.
		eq("adm-b", scopeList(t, f.admB), []string{"tenant:" + f.tenB + "=tenant_admin"})
		eq("adm-a", scopeList(t, f.admA), []string{"tenant:" + store.DefaultTenantID + "=tenant_admin"})
		// Nothing to administer: no console.
		f.memB.do("GET", "/api/admin/scopes", nil).want(t, 403, "forbidden")
		a.newClient("").do("GET", "/api/admin/scopes", nil).want(t, 401, "")

		// One person who administers several tenants, and the site.
		grantRole(t, a, "RB", store.RoleScopeTenant, f.tenB, store.GrantToUser, mustUserID(t, a, "adm-a"), "tenant.settings.read", "tenant.users.read")
		eq("adm-a with a role in B", scopeList(t, f.admA), []string{
			"tenant:" + f.tenB + "=tenant_admin", "tenant:" + store.DefaultTenantID + "=tenant_admin"})
		grantRole(t, a, "RC", store.RoleScopeTenant, f.tenB, store.GrantToUser, mustUserID(t, a, "root"), "tenant.settings.read")
		eq("root with a role in B", scopeList(t, f.root), []string{"site:=site_admin", "tenant:" + f.tenB + "=tenant_admin"})

		// A role that holds no settings key there lists nothing of that tenant.
		grantRole(t, a, "RD", store.RoleScopeTenant, f.tenB, store.GrantToUser, f.userA, "tenant.users.read")
		f.memA.do("GET", "/api/admin/scopes", nil).want(t, 403, "forbidden")
	})
}

// Acting in a tenant the list does not hold is refused as an unknown tenant,
// and a role in a tenant is acted on as that tenant's administrator, even for
// a site administrator who holds the key across the site too.
func TestAnotherTenantsIdInTheScopeIsRefusedAndACapacityFollowsTheList(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		inA := []string{scopeHeader, "tenant:" + store.DefaultTenantID}
		inB := []string{scopeHeader, "tenant:" + f.tenB}
		f.admB.do("GET", "/api/users", nil, inA...).want(t, 404, "not_found")
		f.admA.do("GET", "/api/users", nil, inB...).want(t, 404, "not_found")
		// The list is a person's own route: it takes no scope and answers their own.
		f.admA.do("GET", "/api/admin/scopes", nil, inB...).want(t, 400, "invalid")
		f.admB.do("GET", "/api/users", nil, inB...).want(t, 200, "")

		// Once adm-a holds a role in B, B's scope opens.
		grantRole(t, a, "RB", store.RoleScopeTenant, f.tenB, store.GrantToUser, mustUserID(t, a, "adm-a"), "tenant.settings.read", "tenant.users.read")
		f.admA.do("GET", "/api/users", nil, inB...).want(t, 200, "")

		// A site administrator who also holds a key in B acts there as its administrator.
		grantRole(t, a, "RC", store.RoleScopeTenant, f.tenB, store.GrantToUser, mustUserID(t, a, "root"), "tenant.settings.read", "tenant.users.manage")
		f.root.do("PATCH", "/api/users/"+f.userB, map[string]any{"name": "By root in B"}, inB...).want(t, 200, "")
		a.auth.FlushAudit(context.Background())
		if rows := f.admB.audit(t, "user_changed"); len(rows) == 0 || rows[len(rows)-1]["capacity"] != "tenant_admin" {
			t.Fatalf("a role held in B is a tenant administrator's: %v", rows)
		}
		if rows := f.admB.audit(t, "tenant_console_opened"); len(rows) != 0 {
			t.Fatalf("entering a tenant one administers is no site action: %v", rows)
		}
	})
}

// Someone who is only a member of their own tenant and administers another
// one through a role gets the console for that tenant alone: the list holds
// it and nothing else, they act there and not in their own, and the list asks
// for no key in their own tenant.
func TestAMemberOfOneTenantWhoAdministersAnotherGetsItsConsole(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		inA := []string{scopeHeader, "tenant:" + store.DefaultTenantID}
		inB := []string{scopeHeader, "tenant:" + f.tenB}
		// Before the role: nothing to administer, no console.
		f.memA.do("GET", "/api/admin/scopes", nil).want(t, 403, "forbidden")
		f.memA.do("GET", "/api/users", nil, inB...).want(t, 404, "not_found")

		grantRole(t, a, "RB", store.RoleScopeTenant, f.tenB, store.GrantToUser, f.userA, "tenant.settings.read", "tenant.users.read")
		if got := scopeList(t, f.memA); !slices.Equal(got, []string{"tenant:" + f.tenB + "=tenant_admin"}) {
			t.Fatalf("the member's list: %v", got)
		}
		// They act in B, as its administrator, and not in A (their own).
		f.memA.do("GET", "/api/users", nil, inB...).want(t, 200, "")
		f.memA.do("GET", "/api/users", nil, inA...).want(t, 404, "not_found")
		f.memA.do("GET", "/api/users", nil).want(t, 403, "forbidden")
		f.memA.do("PATCH", "/api/users/"+f.userB, map[string]any{"name": "nope"}, inB...).want(t, 403, "forbidden")

		// The scopes list is a person's own route: it never takes the scope header.
		f.memA.do("GET", "/api/admin/scopes", nil, inB...).want(t, 400, "invalid")
	})
}
