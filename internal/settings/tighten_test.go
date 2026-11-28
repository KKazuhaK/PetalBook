package settings_test

import (
	"testing"

	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// signin.ssoOnly has a tenant layer that only tightens (3.6 d4): a tenant
// may turn it on, never off under a site that has it on. A value of the
// tenant's that is looser than the site's is refused when written, and one
// stored before the site turned it on is kept, not in effect.
func TestATenantMayOnlyTightenSSOOnly(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		d, _ := s.Def(settings.SigninSSOOnly)
		if !d.Has(settings.Tenant) || d.Strict != settings.StrictHigher || d.Lower.Mode != settings.Tighten {
			t.Fatalf("%+v", d)
		}
		// The site's is off: the tenant turns it on for itself.
		if err := s.SetTenant(ctx, ten, settings.SigninSSOOnly, settings.Change{HasValue: true, Value: true}); err != nil {
			t.Fatal(err)
		}
		if e, _ := s.Get(ctx, ten, settings.SigninSSOOnly); e.Value != true || e.Source != settings.Tenant || !e.Tenant.InEffect {
			t.Fatalf("%+v %+v", e, e.Tenant)
		}
		if e, _ := s.Get(ctx, "", settings.SigninSSOOnly); e.Value != false {
			t.Fatal("the tenant's value reached the site")
		}
		// Off again (as strict as the site's): allowed.
		if err := s.SetTenant(ctx, ten, settings.SigninSSOOnly, settings.Change{HasValue: true, Value: false}); err != nil {
			t.Fatal(err)
		}
		// The site turns it on: the tenant's false is kept and not in effect.
		if _, err := s.SetSite(ctx, settings.SigninSSOOnly, settings.Change{HasValue: true, Value: true}); err != nil {
			t.Fatal(err)
		}
		if e, _ := s.Get(ctx, ten, settings.SigninSSOOnly); e.Value != true || e.Tenant.InEffect || e.Tenant.Value != false {
			t.Fatalf("%+v %+v", e, e.Tenant)
		}
		// Writing false now is loosening: refused.
		if err := s.SetTenant(ctx, ten, settings.SigninSSOOnly, settings.Change{HasValue: true, Value: false}); code(err) != settings.CodeRestricted {
			t.Fatalf("%v", err)
		}
		// true is as strict: allowed.
		if err := s.SetTenant(ctx, ten, settings.SigninSSOOnly, settings.Change{HasValue: true, Value: true}); err != nil {
			t.Fatal(err)
		}
	})
}
