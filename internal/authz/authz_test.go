package authz_test

import (
	"context"
	"slices"
	"testing"

	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/perm"
)

func TestRegisterRefusesADuplicateAndAWrongScope(t *testing.T) {
	for name, f := range map[string]func(){
		"duplicate":   func() { authz.Register("tenant.users.read", authz.ScopeTenant, "", "again") },
		"wrong scope": func() { authz.Register("site.something", authz.ScopeTenant, "", "x") },
		"no desc":     func() { authz.Register("site.nothing", authz.ScopeSite, "", "") },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", name)
				}
			}()
			f()
		}()
	}
}

func TestASetIsCheckedByItsBits(t *testing.T) {
	s := authz.Of(authz.TenantUsersRead, authz.SpaceManage)
	if !s.Has(authz.TenantUsersRead) || !s.Has(authz.SpaceManage) || s.Has(authz.TenantUsersDelete) || s.Has(0) {
		t.Fatal(s)
	}
	if got := s.Names(); !slices.Equal(got, []string{"tenant.users.read", "space.manage"}) {
		t.Fatal(got)
	}
	if k, ok := authz.Lookup("tenant.users.read"); !ok || k != authz.TenantUsersRead {
		t.Fatal(k, ok)
	}
}

func TestBuiltInRoles(t *testing.T) {
	site := authz.SubjectFor("admin", true, "A")
	ten := authz.SubjectFor("admin", false, "A")
	mem := authz.SubjectFor("member", false, "A")
	none := authz.SubjectFor("", false, "A")
	for _, in := range authz.All() {
		k, _ := authz.Lookup(in.Name)
		switch in.Scope {
		case authz.ScopeSite:
			if !site.Can(k, "") || ten.Can(k, "A") || mem.Can(k, "A") {
				t.Errorf("%s", in.Name)
			}
		case authz.ScopeTenant:
			// A site administrator holds every tenant key in every tenant; a
			// tenant's administrator in their own only.
			if !site.Can(k, "A") || !site.Can(k, "B") || !ten.Can(k, "A") || ten.Can(k, "B") {
				t.Errorf("%s", in.Name)
			}
		case authz.ScopeSpace:
			// Nobody holds a space key outside a space: admin keys never reach content.
			if site.Can(k, "A") || ten.Can(k, "A") || mem.Can(k, "A") {
				t.Errorf("%s is held outside a space", in.Name)
			}
		}
		if none.Can(k, "A") {
			t.Errorf("no role holds %s", in.Name)
		}
	}
	if got := mem.In("A").Names(); !slices.Equal(got, []string{"tenant.spaces.create"}) {
		t.Fatal(got)
	}
	if !ten.Can(authz.TenantSpacesTakeover, "A") {
		t.Fatal("a tenant administrator may take over a space explicitly")
	}
	if v, _ := authz.RoleSet(authz.RoleViewer); v.Has(authz.TenantSpacesCreate) {
		t.Fatal("a viewer creates no spaces")
	}
}

type featuresOff map[string]bool

func (c featuresOff) On(_ context.Context, _, feature string) bool { return !c[feature] }

func TestDecideAsksTheFeatureFirstAndASiteAdministratorIsNotBound(t *testing.T) {
	off := featuresOff{"features.adminView": true}
	site := authz.SubjectFor("admin", true, "A")
	ten := authz.SubjectFor("admin", false, "A")
	mem := authz.SubjectFor("member", false, "A")
	if d := authz.Decide(context.Background(), ten, authz.TenantDataView, "A", off); d != authz.FeatureOff {
		t.Fatal(d)
	}
	if d := authz.Decide(context.Background(), mem, authz.TenantDataView, "A", off); d != authz.FeatureOff {
		t.Fatal(d)
	}
	if d := authz.Decide(context.Background(), site, authz.TenantDataView, "A", off); d != authz.Allowed {
		t.Fatal(d)
	}
	if d := authz.Decide(context.Background(), ten, authz.TenantDataView, "A", nil); d != authz.Allowed {
		t.Fatal(d)
	}
	if d := authz.Decide(context.Background(), mem, authz.TenantUsersRead, "A", nil); d != authz.Denied {
		t.Fatal(d)
	}
	if d := authz.Decide(context.Background(), ten, authz.SiteSettingsManage, "A", nil); d != authz.Denied {
		t.Fatal(d)
	}
}

func TestEverySpacePermissionHasAKey(t *testing.T) {
	for _, p := range []perm.Perm{perm.Read, perm.Write, perm.Members, perm.Manage} {
		if authz.SpaceKey(p).Scope() != authz.ScopeSpace {
			t.Fatal(p)
		}
	}
}

func TestRegisteringAfterTheRolesWereBuiltPanics(t *testing.T) {
	authz.SubjectFor("member", false, "A")
	defer func() {
		if recover() == nil {
			t.Fatal("no panic")
		}
	}()
	authz.Register("site.late", authz.ScopeSite, "", "late")
}

func TestAFormerNameStillNamesItsKey(t *testing.T) {
	authz.Alias("tenant.people.read", authz.TenantUsersRead)
	if k, ok := authz.Lookup("tenant.people.read"); !ok || k != authz.TenantUsersRead || k.Name() != "tenant.users.read" {
		t.Fatal(k, ok)
	}
	s, unknown := authz.SetOf([]string{"tenant.people.read", "tenant.gone", "site.status.read"})
	if s != authz.Of(authz.TenantUsersRead, authz.SiteStatusRead) || !slices.Equal(unknown, []string{"tenant.gone"}) {
		t.Fatal(s.Names(), unknown)
	}
	for name, f := range map[string]func(){
		"alias of a key's name": func() { authz.Alias("tenant.users.read", authz.TenantUsersManage) },
		"duplicate alias":       func() { authz.Alias("tenant.people.read", authz.TenantUsersManage) },
		"unregistered key":      func() { authz.Alias("tenant.nothing", 0) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", name)
				}
			}()
			f()
		}()
	}
}

// The content of personal spaces is the built-in site administrator's alone: no
// stored role can hold it, whatever it lists.
func TestPersonalContentIsNotGrantable(t *testing.T) {
	if authz.SiteSpacesPersonalContent.Grantable() || !authz.SiteSpacesPersonalMeta.Grantable() || !authz.TenantUsersRead.Grantable() {
		t.Fatal("grantable marks")
	}
	if !authz.SubjectFor("admin", true, "A").Can(authz.SiteSpacesPersonalContent, "") {
		t.Fatal("the site administrator holds it")
	}
	got := authz.Subject{}.WithGrants([]authz.Grant{{Scope: authz.ScopeSite, Keys: authz.Of(authz.SiteSpacesPersonalContent, authz.SiteSpacesPersonalMeta)}})
	if got.Can(authz.SiteSpacesPersonalContent, "") || !got.Can(authz.SiteSpacesPersonalMeta, "") {
		t.Fatal(got)
	}
}

func TestCustomGrantsKeepToTheirScope(t *testing.T) {
	mem := authz.SubjectFor("member", false, "A")
	got := mem.WithGrants([]authz.Grant{
		// A tenant role naming a site key and a space key: only its tenant key counts.
		{Scope: authz.ScopeTenant, Tenant: "A", Keys: authz.Of(authz.TenantAuditRead, authz.SiteSettingsManage, authz.SpaceManage)},
		// A tenant role held nowhere adds nothing.
		{Scope: authz.ScopeTenant, Keys: authz.Of(authz.TenantUsersDelete)},
		// A site role across the site.
		{Scope: authz.ScopeSite, Keys: authz.Of(authz.SiteStatusRead, authz.TenantUsageRead, authz.SpaceRead)},
		// A site role held in one tenant: its tenant keys there only.
		{Scope: authz.ScopeSite, Tenant: "B", Keys: authz.Of(authz.SiteMailManage, authz.TenantTokensRead)},
		// A space role is the space level's.
		{Scope: authz.ScopeSpace, Tenant: "A", Keys: authz.Of(authz.TenantGroupsRead)},
	})
	if !got.Can(authz.TenantAuditRead, "A") || got.Can(authz.TenantAuditRead, "B") || got.Can(authz.SiteSettingsManage, "") ||
		got.Can(authz.TenantUsersDelete, "A") || got.Can(authz.TenantGroupsRead, "A") {
		t.Fatal(got)
	}
	if !got.Can(authz.SiteStatusRead, "") || !got.SiteWide(authz.TenantUsageRead) || !got.Can(authz.TenantUsageRead, "C") || got.Site.Has(authz.SpaceRead) {
		t.Fatal(got)
	}
	if !got.Can(authz.TenantTokensRead, "B") || got.Can(authz.TenantTokensRead, "A") || got.Can(authz.SiteMailManage, "") {
		t.Fatal(got)
	}
	if !got.Can(authz.TenantSpacesCreate, "A") {
		t.Fatal("the built-in role is kept")
	}
	if mem.Can(authz.TenantAuditRead, "A") {
		t.Fatal("the subject granted to was changed")
	}
}

// NarrowTo is a subject cut to one tenant's keys there: never more than it
// held, and no site key, so a site administrator in a tenant's scope holds
// exactly what that tenant's administrator does, and features bind them.
func TestNarrowToKeepsOnlyTheTenantKeysHeldThere(t *testing.T) {
	site := authz.SubjectFor("admin", true, "t0")
	tenantAdmin, _ := authz.RoleSet(authz.RoleTenantAdmin)
	n := site.NarrowTo("t1")
	if !n.In("t1").Has(authz.TenantUsersRead) || n.In("t1") != tenantAdmin {
		t.Fatalf("a site administrator narrowed to t1 holds %v there, want the tenant administrator's keys", n.In("t1").Names())
	}
	if n.Can(authz.SiteTenantsManage, "t1") || n.SiteWide(authz.TenantUsersRead) || n.Can(authz.SiteSpacesPersonalMeta, "") {
		t.Fatal("a narrowed subject keeps a site key or a key across the site")
	}
	if n.Can(authz.TenantUsersRead, "t0") || n.Can(authz.TenantUsersRead, "t2") {
		t.Fatal("a narrowed subject holds keys in another tenant")
	}
	// It never widens: a tenant administrator of t0 narrowed to t1 holds nothing.
	if got := authz.SubjectFor("admin", false, "t0").NarrowTo("t1").In("t1"); !got.Empty() {
		t.Fatalf("narrowing widened: %v", got.Names())
	}
	member := authz.SubjectFor("member", false, "t0")
	if got := member.NarrowTo("t0").In("t0"); got != member.In("t0") {
		t.Fatalf("a member narrowed to their own tenant holds %v, want %v", got.Names(), member.In("t0").Names())
	}
	// Features bind a narrowed site administrator.
	off := offFeatures{}
	if d := authz.Decide(context.Background(), n, authz.TenantDataView, "t1", off); d != authz.FeatureOff {
		t.Fatalf("a narrowed site administrator was not bound by the tenant's feature: %v", d)
	}
}

type offFeatures struct{}

func (offFeatures) On(context.Context, string, string) bool { return false }
