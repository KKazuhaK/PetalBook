package store_test

import (
	"errors"
	"slices"
	"sort"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

func TestCustomRolesAndTheirGrants(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Now()
		q := st.Q()
		if st.CustomRoles() {
			t.Fatal("a fresh database has no custom role")
		}
		for _, u := range []string{"U1", "U2"} {
			if err := store.InsertUser(ctx, q, store.User{ID: u, Username: u, Name: u, Role: "member", Created: now}); err != nil {
				t.Fatal(err)
			}
		}
		mkGroup(t, st, "G1", "auditors", store.GroupLocal)
		if err := store.AddLocalMembers(ctx, q, "G1", []string{"U1"}, now); err != nil {
			t.Fatal(err)
		}
		site := store.Role{ID: "R1", Scope: store.RoleScopeSite, Name: "watchers", Keys: []string{"site.status.read"}, Created: now, Updated: now}
		ten := store.Role{ID: "R2", TenantID: store.DefaultTenantID, Scope: store.RoleScopeTenant, Name: "audit", Keys: []string{"tenant.audit.read"}, Created: now, Updated: now}
		for _, r := range []store.Role{site, ten} {
			if err := st.InsertRole(ctx, q, r); err != nil {
				t.Fatal(err)
			}
		}
		if !st.CustomRoles() {
			t.Fatal("inserting a role does not say so")
		}
		dup := ten
		dup.ID = "R3"
		if err := st.InsertRole(ctx, q, dup); !store.IsUnique(err) {
			t.Fatalf("a role's name is unique in its tenant: %v", err)
		}
		bad := site
		bad.ID, bad.Name, bad.Scope = "R4", "odd", "planet"
		if err := st.InsertRole(ctx, q, bad); err == nil {
			t.Fatal("a role of no scope was stored")
		}
		if err := store.UpdateRole(ctx, q, "R2", "audit", []string{"tenant.audit.read", "tenant.usage.read"}, now); err != nil {
			t.Fatal(err)
		}
		if r, err := store.GetRole(ctx, q, "R2"); err != nil || !slices.Equal(r.Keys, []string{"tenant.audit.read", "tenant.usage.read"}) {
			t.Fatal(r, err)
		}
		if l, err := store.ListRoles(ctx, q, ""); err != nil || len(l) != 1 || l[0].ID != "R1" {
			t.Fatal(l, err)
		}

		for _, g := range []store.RoleGrant{
			{RoleID: "R1", SubjectKind: store.GrantToUser, SubjectID: "U1"},
			{RoleID: "R2", SubjectKind: store.GrantToGroup, SubjectID: "G1"},
		} {
			g.Created = now
			if err := store.InsertRoleGrant(ctx, q, g); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.InsertRoleGrant(ctx, q, store.RoleGrant{RoleID: "R1", SubjectKind: "robot", SubjectID: "U1", Created: now}); err == nil {
			t.Fatal("a grant to neither a person nor a group was stored")
		}
		held, err := store.HeldRoles(ctx, q, "U1")
		if err != nil {
			t.Fatal(err)
		}
		sort.Slice(held, func(i, j int) bool { return held[i].Scope < held[j].Scope })
		if len(held) != 2 || held[0].Scope != "site" || held[0].TenantID != "" ||
			held[1].Scope != "tenant" || held[1].TenantID != store.DefaultTenantID || len(held[1].Keys) != 2 {
			t.Fatalf("%+v", held)
		}
		if held, _ := store.HeldRoles(ctx, q, "U2"); len(held) != 0 {
			t.Fatalf("%+v", held)
		}

		// A group's grants go with the group; a role's with the role.
		if err := store.DeleteGroup(ctx, q, "G1"); err != nil {
			t.Fatal(err)
		}
		if held, _ := store.HeldRoles(ctx, q, "U1"); len(held) != 1 {
			t.Fatalf("%+v", held)
		}
		if err := store.DeleteRole(ctx, q, "R1"); err != nil {
			t.Fatal(err)
		}
		if held, _ := store.HeldRoles(ctx, q, "U1"); len(held) != 0 {
			t.Fatalf("%+v", held)
		}
		if err := store.DeleteRole(ctx, q, "R1"); !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
		// The flag is read at start: a role left is found, none is not.
		if err := st.ReloadCustomRoles(); err != nil || !st.CustomRoles() {
			t.Fatal(err)
		}
		if err := store.DeleteRole(ctx, q, "R2"); err != nil {
			t.Fatal(err)
		}
		if err := st.ReloadCustomRoles(); err != nil || st.CustomRoles() {
			t.Fatal(err)
		}
	})
}

func TestATenantsRolesGoWithIt(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Now()
		const tn = "T000000002"
		if err := st.CreateTenant(ctx, store.Tenant{ID: tn, Slug: "two", Name: "Two", Created: now, Updated: now}); err != nil {
			t.Fatal(err)
		}
		if err := st.InsertRole(ctx, st.Q(), store.Role{ID: "R1", TenantID: tn, Scope: store.RoleScopeTenant, Name: "r", Created: now, Updated: now}); err != nil {
			t.Fatal(err)
		}
		if err := st.InsertRole(ctx, st.Q(), store.Role{ID: "R2", Scope: store.RoleScopeSite, Name: "s", Created: now, Updated: now}); err != nil {
			t.Fatal(err)
		}
		// A site role held in the tenant: the grant goes, the role stays.
		if err := store.InsertRoleGrant(ctx, st.Q(), store.RoleGrant{RoleID: "R2", SubjectKind: store.GrantToUser, SubjectID: "U9", TenantID: tn, Created: now}); err != nil {
			t.Fatal(err)
		}
		if err := st.DeleteTenant(ctx, tn); err != nil {
			t.Fatal(err)
		}
		if n := count(t, st, `SELECT COUNT(*) FROM roles`); n != 1 {
			t.Fatal(n)
		}
		if n := count(t, st, `SELECT COUNT(*) FROM role_grants`); n != 0 {
			t.Fatal(n)
		}
	})
}
