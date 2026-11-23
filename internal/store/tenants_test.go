package store_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

const (
	tenA = store.DefaultTenantID
	tenB = "T000000002"
)

// twoTenants makes a second tenant and, in each, a user, a shared space and a
// group, with ids that say where they are.
func twoTenants(t *testing.T, st *store.Store) {
	t.Helper()
	now := time.Now()
	if err := st.CreateTenant(ctx, store.Tenant{ID: tenB, Slug: "acme", Name: "Acme", Created: now, Updated: now}); err != nil {
		t.Fatal(err)
	}
	for _, x := range []struct{ tenant, tag string }{{tenA, "a"}, {tenB, "b"}} {
		if err := store.InsertUser(ctx, st.Q(), store.User{ID: "U" + x.tag, Username: "user-" + x.tag, Name: "User " + x.tag, Role: "member", Created: now, TenantID: x.tenant}); err != nil {
			t.Fatal(err)
		}
		if err := store.InsertSpace(ctx, st.Q(), x.tenant, "S"+x.tag, "Space "+x.tag, "", "", now); err != nil {
			t.Fatal(err)
		}
		if err := store.InsertGroup(ctx, st.Q(), store.Group{ID: "G" + x.tag, TenantID: x.tenant, Name: "Group", Source: "local", Created: now, Updated: now}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLinksAcrossTenantsAreRefusedByTheStatement(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		twoTenants(t, st)
		now := time.Now()
		q := st.Q()
		// Same tenant works; the other does not and writes nothing.
		if err := store.AddMember(ctx, q, "Sa", "Ua", "owner", now); err != nil {
			t.Fatal(err)
		}
		if err := store.AddMember(ctx, q, "Sa", "Ub", "viewer", now); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a member of another tenant: %v", err)
		}
		if err := store.AddMember(ctx, q, "Sb", "Ua", "viewer", now); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a space of another tenant: %v", err)
		}
		if err := store.AddSpaceGroup(ctx, q, "Sa", "Gb", "viewer", now); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a group of another tenant in a space: %v", err)
		}
		if err := store.AddSpaceGroup(ctx, q, "Sa", "Ga", "viewer", now); err != nil {
			t.Fatal(err)
		}
		if err := store.AddLocalMembers(ctx, q, "Ga", []string{"Ua", "Ub"}, now); err != nil {
			t.Fatal(err)
		}
		if n := count(t, st, `SELECT COUNT(*) FROM group_members WHERE group_id = 'Ga'`); n != 1 {
			t.Fatalf("%d people in the group; the other tenant's person must not be there", n)
		}
		if err := store.ApplySynced(ctx, q, "Ub", "sso", nil, []store.SyncedMembership{{GroupID: "Ga", Value: "x"}, {GroupID: "Gb", Value: "x"}}, now); err != nil {
			t.Fatal(err)
		}
		if n := count(t, st, `SELECT COUNT(*) FROM group_members WHERE user_id = 'Ub'`); n != 1 {
			t.Fatalf("%d memberships of the person of tenant B; only the group of their own tenant counts", n)
		}
		if _, err := store.TransferOwnership(ctx, q, "Sa", "Ub", now); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a transfer to a person of another tenant: %v", err)
		}
		if n := count(t, st, `SELECT COUNT(*) FROM space_members WHERE space_id = 'Sa' AND user_id = 'Ua' AND role = 'owner'`); n != 1 {
			t.Fatal("the failed transfer changed the owners")
		}
		if _, err := store.TransferOwnership(ctx, q, "Sa", "Ua", now); err != nil {
			t.Fatal(err)
		}
		assertNoCrossTenantLinks(t, st)
	})
}

// assertNoCrossTenantLinks is the query the isolation fixtures end with: no
// membership, group membership or place of a group in a space joins two tenants.
func assertNoCrossTenantLinks(t *testing.T, st *store.Store) {
	t.Helper()
	for name, q := range map[string]string{
		"space_members": `SELECT COUNT(*) FROM space_members m JOIN spaces s ON s.id = m.space_id JOIN users u ON u.id = m.user_id WHERE s.tenant_id <> u.tenant_id`,
		"group_members": `SELECT COUNT(*) FROM group_members m JOIN groups g ON g.id = m.group_id JOIN users u ON u.id = m.user_id WHERE g.tenant_id <> u.tenant_id`,
		"space_groups":  `SELECT COUNT(*) FROM space_groups x JOIN spaces s ON s.id = x.space_id JOIN groups g ON g.id = x.group_id WHERE s.tenant_id <> g.tenant_id`,
	} {
		if n := count(t, st, q); n != 0 {
			t.Errorf("%d rows of %s join two tenants", n, name)
		}
	}
}

func TestScopeSeesOnlyItsTenant(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		twoTenants(t, st)
		q := st.Q()
		inB, all, none := store.InTenant(tenB), store.AllTenants(), store.Scope{}
		if _, err := store.GetUser(ctx, q, inB, "Ua"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%v", err)
		}
		if u, err := store.GetUser(ctx, q, inB, "Ub"); err != nil || u.TenantID != tenB {
			t.Fatalf("%v %v", u, err)
		}
		if _, err := store.GetUser(ctx, q, none, "Ua"); !errors.Is(err, store.ErrNotFound) {
			t.Fatal("the zero Scope must see nothing")
		}
		if l, _ := store.ListUsers(ctx, q, inB); len(l) != 1 || l[0].ID != "Ub" {
			t.Fatalf("%v", l)
		}
		if l, _ := store.ListUsers(ctx, q, all); len(l) != 2 {
			t.Fatalf("%v", l)
		}
		if l, _ := store.SearchUsers(ctx, q, st.Dialect(), inB, "user", false, 0); len(l) != 1 {
			t.Fatalf("%v", l)
		}
		if _, err := store.GetSpace(ctx, q, inB, "Sa"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%v", err)
		}
		if rows, _, _ := store.ListAllSpaces(ctx, q, inB, store.SpaceFilter{}); len(rows) != 1 || rows[0].ID != "Sb" || rows[0].TenantID != tenB {
			t.Fatalf("%v", rows)
		}
		if _, err := store.GetAdminSpace(ctx, q, inB, "Sa"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%v", err)
		}
		if _, err := store.GetGroup(ctx, q, inB, "Ga"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%v", err)
		}
		if l, _, _ := store.ListGroups(ctx, q, st.Dialect(), inB, store.GroupFilter{}); len(l) != 1 || l[0].ID != "Gb" {
			t.Fatalf("%v", l)
		}
		if l, _ := store.SearchGroups(ctx, q, st.Dialect(), inB, "grou", 10); len(l) != 1 || l[0].ID != "Gb" {
			t.Fatalf("%v", l)
		}
		if have, _ := store.ExistingUsers(ctx, q, inB, []string{"Ua", "Ub"}); len(have) != 1 || !have["Ub"] {
			t.Fatalf("%v", have)
		}
		// Writes by id are scoped too.
		if err := store.SetSpaceDisabled(ctx, q, inB, "Sa", true); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%v", err)
		}
		if _, err := store.DeleteSpace(ctx, q, inB, "Sa"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%v", err)
		}
		if n := count(t, st, `SELECT COUNT(*) FROM spaces WHERE id = 'Sa' AND disabled = 0`); n != 1 {
			t.Fatal("another tenant's space was changed")
		}
		// Invites and audit rows.
		now := time.Now()
		for _, tn := range []string{tenA, tenB} {
			if err := store.InsertInvite(ctx, q, store.Invite{ID: "I" + tn[len(tn)-1:], TokenHash: "h" + tn, Role: "member", CreatedBy: "Ua", Created: now, Expires: now.Add(time.Hour), TenantID: tn}); err != nil {
				t.Fatal(err)
			}
		}
		if l, _ := store.ListOpenInvites(ctx, q, inB, now); len(l) != 1 || l[0].TenantID != tenB {
			t.Fatalf("%v", l)
		}
		if err := store.DeleteInvite(ctx, q, inB, "I0"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%v", err)
		}
		if err := store.InsertAudit(ctx, q, []store.AuditRow{
			{ID: "1", At: now, Kind: "login", UserID: "Ua"},                         // takes the tenant of its user
			{ID: "2", At: now, Kind: "login", UserID: "Ub"},                         //
			{ID: "3", At: now, Kind: "security_changed", Actor: "Ua"},               // a site kind, in A
			{ID: "4", At: now, Kind: "setting_changed", Actor: "Ua", TenantID: "-"}, // the server's own
			{ID: "5", At: now, Kind: "dropped"},                                     // no user: the server's own
		}); err != nil {
			t.Fatal(err)
		}
		ids := func(sc store.Scope, hide bool) string {
			rows, err := store.ListAudit(ctx, q, sc, hide, "", "", "", 50)
			if err != nil {
				t.Fatal(err)
			}
			s := ""
			for i := len(rows) - 1; i >= 0; i-- {
				s += rows[i].ID
			}
			return s
		}
		if got := ids(inB, true); got != "2" {
			t.Errorf("tenant B sees rows %q", got)
		}
		if got := ids(store.InTenant(tenA), true); got != "1" {
			t.Errorf("tenant A's administrator sees rows %q (site kinds and the server's own are hidden)", got)
		}
		if got := ids(store.InTenant(tenA), false); got != "13" {
			t.Errorf("rows of tenant A with the site kinds: %q", got)
		}
		if got := ids(all, false); got != "12345" {
			t.Errorf("a site administrator sees rows %q", got)
		}
	})
}

func TestTenantsTable(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Now()
		for i, n := range []string{"Zed", "alpha", "Beta"} {
			if err := st.CreateTenant(ctx, store.Tenant{ID: fmt.Sprintf("T00000000%d", i+2), Slug: fmt.Sprintf("t-%d", i), Name: n, Created: now, Updated: now}); err != nil {
				t.Fatal(err)
			}
		}
		if err := st.CreateTenant(ctx, store.Tenant{ID: "TX", Slug: "t-0", Name: "dup", Created: now, Updated: now}); !store.IsUnique(err) {
			t.Fatalf("a taken slug: %v", err)
		}
		var names []string
		after := ""
		for {
			page, next, err := store.ListTenants(ctx, st.Q(), st.Dialect(), "", after, 2)
			if err != nil {
				t.Fatal(err)
			}
			for _, tn := range page {
				names = append(names, tn.Name)
			}
			if next == "" {
				break
			}
			after = next
		}
		if fmt.Sprint(names) != "[Default alpha Beta Zed]" {
			t.Fatalf("the default first, then by name: %v", names)
		}
		if l, _, _ := store.ListTenants(ctx, st.Q(), st.Dialect(), "ALPH", "", 10); len(l) != 1 || l[0].Name != "alpha" {
			t.Fatalf("%v", l)
		}
		// Disabling is in memory at once.
		tn, _ := store.GetTenant(ctx, st.Q(), "T000000002")
		tn.Disabled, tn.Name = true, "Zed 2"
		if err := st.UpdateTenant(ctx, *tn); err != nil {
			t.Fatal(err)
		}
		if !st.TenantDisabled("T000000002") || st.TenantDisabled(store.DefaultTenantID) {
			t.Fatal("the disabled state is not in memory")
		}
		// Not empty: refused with the counts; empty: deleted with its settings.
		mkUser(t, st, "U1", "u1", "member")
		store.InsertUser(ctx, st.Q(), store.User{ID: "U2", Username: "u2", Name: "u2", Role: "member", Created: now, TenantID: "T000000003"})
		var ne *store.ErrTenantNotEmpty
		if err := st.DeleteTenant(ctx, "T000000003"); !errors.As(err, &ne) || ne.Users != 1 {
			t.Fatalf("%v", err)
		}
		store.PutLayered(ctx, st.Q(), store.LayeredRow{Layer: "tenant", LayerID: "T000000002", Key: "k", Value: "1", Updated: now})
		if err := st.DeleteTenant(ctx, "T000000002"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.GetLayered(ctx, st.Q(), "tenant", "T000000002", "k"); !errors.Is(err, store.ErrNotFound) {
			t.Fatal("a deleted tenant's settings stay")
		}
		if st.TenantDisabled("T000000002") {
			t.Fatal("a deleted tenant is still disabled in memory")
		}
		if err := st.DeleteTenant(ctx, "T000000002"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%v", err)
		}
	})
}

func TestTenantLimit(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Now()
		for i := 1; i < store.MaxTenants; i++ {
			if _, err := st.Q().Exec(ctx, `INSERT INTO tenants(id, slug, name, name_key, created, updated) VALUES (?, ?, ?, ?, 0, 0)`, fmt.Sprintf("X%09d", i), fmt.Sprintf("x-%d", i), "x", "x"); err != nil {
				t.Fatal(err)
			}
		}
		err := st.CreateTenant(ctx, store.Tenant{ID: "TLAST", Slug: "last", Name: "Last", Created: now, Updated: now})
		if !errors.Is(err, store.ErrTenantLimit) {
			t.Fatalf("%v", err)
		}
	})
}

func TestSiteAdminFlagFollowsTheRole(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Now()
		store.InsertUser(ctx, st.Q(), store.User{ID: "A", Username: "a", Name: "a", Role: "admin", SiteAdmin: true, Created: now})
		store.InsertUser(ctx, st.Q(), store.User{ID: "B", Username: "b", Name: "b", Role: "admin", Created: now})
		store.InsertUser(ctx, st.Q(), store.User{ID: "M", Username: "m", Name: "m", Role: "member", Created: now})
		if n, _ := store.CountEnabledAdmins(ctx, st.Q()); n != 1 {
			t.Fatalf("only site administrators count: %d", n)
		}
		if err := store.SetSiteAdmin(ctx, st.Q(), "M", true); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a member cannot be made a site administrator: %v", err)
		}
		if err := store.SetSiteAdmin(ctx, st.Q(), "B", true); err != nil {
			t.Fatal(err)
		}
		if n, _ := store.CountEnabledAdmins(ctx, st.Q()); n != 2 {
			t.Fatalf("%d", n)
		}
		if err := store.SetSiteAdmin(ctx, st.Q(), "B", false); err != nil {
			t.Fatal(err)
		}
		if u, _ := store.GetUser(ctx, st.Q(), store.AllTenants(), "B"); u.SiteAdmin {
			t.Fatal("still a site administrator")
		}
	})
}

func TestSiteAdministratorsOfADisabledTenantAreNotCounted(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		twoTenants(t, st)
		now := time.Now()
		for _, u := range []store.User{
			{ID: "Ra", Username: "root", Role: "admin", SiteAdmin: true, Created: now, TenantID: tenA},
			{ID: "Xb", Username: "adm-x", Role: "admin", SiteAdmin: true, Created: now, TenantID: tenB},
		} {
			if err := store.InsertUser(ctx, st.Q(), u); err != nil {
				t.Fatal(err)
			}
		}
		count := func() (all, outsideB, outsideA int) {
			var err error
			if all, err = store.CountEnabledAdmins(ctx, st.Q()); err != nil {
				t.Fatal(err)
			}
			if outsideB, err = store.CountEnabledAdminsOutside(ctx, st.Q(), tenB); err != nil {
				t.Fatal(err)
			}
			if outsideA, err = store.CountEnabledAdminsOutside(ctx, st.Q(), tenA); err != nil {
				t.Fatal(err)
			}
			return
		}
		if a, b, c := count(); a != 2 || b != 1 || c != 1 {
			t.Fatalf("%d %d %d", a, b, c)
		}
		tb, err := store.GetTenant(ctx, st.Q(), tenB)
		if err != nil {
			t.Fatal(err)
		}
		tb.Disabled = true
		if err := st.UpdateTenant(ctx, *tb); err != nil {
			t.Fatal(err)
		}
		if a, b, c := count(); a != 1 || b != 1 || c != 0 {
			t.Fatalf("with B disabled: %d %d %d", a, b, c)
		}
	})
}
