package authz_test

import (
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

type capsOff map[string]bool

func (c capsOff) On(_, capability string) bool { return !c[capability] }

func TestDecideAsksTheCapabilityFirstAndASiteAdministratorIsNotBound(t *testing.T) {
	off := capsOff{"data.adminView": true}
	site := authz.SubjectFor("admin", true, "A")
	ten := authz.SubjectFor("admin", false, "A")
	mem := authz.SubjectFor("member", false, "A")
	if d := authz.Decide(ten, authz.TenantDataView, "A", off); d != authz.CapabilityOff {
		t.Fatal(d)
	}
	if d := authz.Decide(mem, authz.TenantDataView, "A", off); d != authz.CapabilityOff {
		t.Fatal(d)
	}
	if d := authz.Decide(site, authz.TenantDataView, "A", off); d != authz.Allowed {
		t.Fatal(d)
	}
	if d := authz.Decide(ten, authz.TenantDataView, "A", nil); d != authz.Allowed {
		t.Fatal(d)
	}
	if d := authz.Decide(mem, authz.TenantUsersRead, "A", nil); d != authz.Denied {
		t.Fatal(d)
	}
	if d := authz.Decide(ten, authz.SiteSettingsManage, "A", nil); d != authz.Denied {
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
