package api_test

import "testing"

// domains.joinRole is the role of a person who joins by domain, auto or
// approved: an organisation chooses among the roles that exist and are not the
// administrator's (today only member). An approver naming another role is
// refused too.
func TestTheJoinRoleIsASettingOfNonAdministratorRoles(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newJoinFx(t, a)
		put := func(v any) result {
			return f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/domains.joinRole", map[string]any{"value": v})
		}
		put("admin").want(t, 400, "invalid")
		put("nobody").want(t, 400, "invalid")
		put("member").want(t, 200, "")
		// Another organisation's setting is its own.
		f.admC.do("PUT", "/api/admin/tenants/"+f.tenC+"/settings/domains.joinRole", map[string]any{"value": "admin"}).want(t, 400, "invalid")

		setJoin(f.admB, f.tenB, "acme.example", "auto").want(t, 200, "")
		f.signUp(t, a.newClient(""), "ann", "ann@acme.example").want(t, 201, "")
		if u := userTenant(t, a, "ann"); u.TenantID != f.tenB || u.Role != "member" {
			t.Fatalf("%+v", u)
		}

		setJoin(f.admB, f.tenB, "acme.example", "request").want(t, 200, "")
		f.request(t, a.newClient(""), "bob", "bob@acme.example")
		id := pendingID(t, f.admB, f.tenB)
		f.admB.do("POST", joinPath(f.tenB)+"/"+id+"/approve", map[string]any{"role": "admin"}).want(t, 400, "invalid")
		f.admB.do("POST", joinPath(f.tenB)+"/"+id+"/approve", map[string]any{}).want(t, 200, "")
		if u := userTenant(t, a, "bob"); u.Role != "member" {
			t.Fatalf("%+v", u)
		}
	})
}
