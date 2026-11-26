package settings_test

import (
	"testing"
	"time"

	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// Every setting either has a tenant layer, or says whether a site
// administrator may set it for one tenant (ForTenant) or why it is the same
// for everyone (ServerWide): exactly one of them.
func TestEverySettingIsPerTenantOrServerWide(t *testing.T) {
	for _, d := range settings.Definitions() {
		n := 0
		for _, b := range []bool{d.Has(settings.Tenant), d.ForTenant, d.ServerWide != ""} {
			if b {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s: a tenant layer, ForTenant or ServerWide, exactly one (layers %v, forTenant %v, serverWide %q)",
				d.Key, d.Layers, d.ForTenant, d.ServerWide)
		}
	}
	for _, k := range []string{settings.UsersReuseUsernames, settings.UsersEmailNonASCII, settings.RecycleDays} {
		if d, _ := settings.New(nil).Def(k); d.ServerWide == "" {
			t.Errorf("%s is not server-wide", k)
		}
	}
}

const ten2 = "T000000003"

func setupTwo(t *testing.T, st *store.Store) *settings.Service {
	t.Helper()
	s := setup(t, st)
	now := time.Now()
	if err := st.CreateTenant(ctx, store.Tenant{ID: ten2, Slug: "globex", Name: "Globex", Created: now, Updated: now}); err != nil {
		t.Fatal(err)
	}
	return s
}

func siteRule(v any, mode string) *settings.Rule {
	r := &settings.Rule{Value: v}
	if mode != "" {
		r.Lower = lower(mode)
	}
	return r
}

// The layers, strongest first: the site's lock over everyone, the site's lock
// for one tenant, the tenant's default, the site's value for one tenant, the
// site's default, the definition's. For a key with a tenant layer
// (legal.termsUrl) and for a site-only key the site may set for one tenant
// (exports.days). The other tenant sees only the site's.
func TestPrecedenceOfTheLayers(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setupTwo(t, st)
		for _, c := range []struct {
			key                        string
			def, site, forTen, own, fv any // the definition's, the site's, the site's for ten, ten's own, forced for ten
		}{
			{settings.LegalTermsURL, "", "https://site.example/t", "https://for-acme.example/t", "https://acme.example/t", "https://forced.example/t"},
			{settings.ExportsDays, int64(7), int64(10), int64(5), nil, int64(3)},
		} {
			t.Run(c.key, func(t *testing.T) {
				site := func(v any, mode string) func() error {
					return func() error {
						_, err := s.SetSite(ctx, c.key, settings.Change{HasValue: v != nil, Value: v, HasLower: true, Lower: lower(mode)})
						return err
					}
				}
				forTen := func(r *settings.Rule) func() error {
					return func() error {
						return s.SetTenant(ctx, ten, c.key, settings.Change{Site: true, HasSiteRule: true, SiteRule: r})
					}
				}
				type step struct {
					name        string
					do          func() error
					want, want2 any // what ten and ten2 see then
				}
				steps := []step{
					{"the definition's default", func() error { return nil }, c.def, c.def},
					{"the site's default", site(c.site, settings.Allow), c.site, c.site},
					{"the site's value for one tenant", forTen(siteRule(c.forTen, "")), c.forTen, c.site},
				}
				if c.own != nil {
					steps = append(steps, step{"the tenant's default", func() error {
						return s.SetTenant(ctx, ten, c.key, settings.Change{HasValue: true, Value: c.own})
					}, c.own, c.site})
				}
				steps = append(steps,
					step{"the site's lock for one tenant", forTen(siteRule(c.fv, settings.Lock)), c.fv, c.site},
					step{"the site's lock over everyone", site(nil, settings.Lock), c.site, c.site},
					step{"that lock lifted: the lock for one tenant again", site(nil, settings.Allow), c.fv, c.site},
					step{"the site's rule for the tenant removed", forTen(nil), func() any {
						if c.own != nil {
							return c.own // the tenant's own value was kept all along
						}
						return c.site
					}(), c.site},
				)
				for _, st := range steps {
					if err := st.do(); err != nil {
						t.Fatalf("%s: %v", st.name, err)
					}
					for _, w := range []struct {
						tenant string
						want   any
					}{{ten, st.want}, {ten2, st.want2}} {
						e, err := s.Get(ctx, w.tenant, c.key)
						if err != nil {
							t.Fatal(err)
						}
						if e.Value != w.want {
							t.Errorf("%s, %s: %v, want %v", st.name, w.tenant, e.Value, w.want)
						}
					}
				}
			})
		}
	})
}

// What was a capability is the site's rule for one tenant: "Acme has no
// offline copies" is offline.copies locked off for Acme, "at most 10 spaces a
// person in Acme" is quota.spacesPerPerson restricted to 10 for Acme. The
// tenant's own value is kept, and counts when it fits; another tenant is not
// touched; the tenant's administrators cannot change the rule.
func TestTheSitesRuleForOneTenantIsTheCapability(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setupTwo(t, st)
		site := settings.Change{Site: true, HasSiteRule: true}

		off := site
		off.SiteRule = siteRule(false, settings.Lock)
		if err := s.SetTenant(ctx, ten, settings.OfflineCopies, off); err != nil {
			t.Fatal(err)
		}
		e, _ := s.Get(ctx, ten, settings.OfflineCopies)
		if e.Value != false || e.LockedBy != settings.Site || !e.Tenant.SiteLocked || !e.Tenant.SiteRule.InEffect {
			t.Fatalf("locked off: %+v %+v", e, e.Tenant)
		}
		if c := code(s.SetTenant(ctx, ten, settings.OfflineCopies, settings.Change{HasValue: true, Value: true})); c != settings.CodeLocked {
			t.Fatalf("the tenant's write: %s", c)
		}
		if c := code(s.SetTenant(ctx, ten, settings.OfflineCopies, settings.Change{HasSiteRule: true})); c != settings.CodeLocked {
			t.Fatalf("the tenant removed the site's rule: %s", c)
		}
		if e, _ := s.Get(ctx, ten2, settings.OfflineCopies); e.Value != true || e.Tenant.SiteRule != nil {
			t.Fatalf("another tenant: %+v", e)
		}

		k := settings.QuotaSpacesPerPerson
		if err := s.SetTenant(ctx, ten, k, settings.Change{HasValue: true, Value: int64(15)}); err != nil {
			t.Fatal(err)
		}
		max := site
		max.SiteRule = &settings.Rule{Lower: &settings.Lower{Mode: settings.Restrict, Max: ptr(int64(10))}}
		if err := s.SetTenant(ctx, ten, k, max); err != nil {
			t.Fatal(err)
		}
		e, _ = s.Get(ctx, ten, k)
		if e.Value != int64(20) || e.Tenant.Value != int64(15) || e.Tenant.InEffect || e.Above.Mode != settings.Restrict {
			t.Fatalf("over the tenant's max: %+v %+v", e, e.Tenant)
		}
		if c := code(s.SetTenant(ctx, ten, k, settings.Change{HasValue: true, Value: int64(11)})); c != settings.CodeRestricted {
			t.Fatalf("above the max: %s", c)
		}
		if err := s.SetTenant(ctx, ten, k, settings.Change{HasValue: true, Value: int64(8)}); err != nil {
			t.Fatal(err)
		}
		if e, _ := s.Get(ctx, ten, k); e.Value != int64(8) {
			t.Fatalf("inside the max: %v", e.Value)
		}
		if err := s.SetTenant(ctx, ten2, k, settings.Change{HasValue: true, Value: int64(15)}); err != nil {
			t.Fatalf("another tenant: %v", err)
		}
	})
}

func ptr[T any](v T) *T { return &v }

// A site-only key set for one tenant is the site's: the tenant's
// administrators cannot write it and it takes no rule of the tenant's. A
// server-wide key has no tenant view at all.
func TestSiteOnlyKeysForOneTenant(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		k := settings.StorageAssetsOwnExempt
		if c := code(s.SetTenant(ctx, ten, k, settings.Change{HasValue: true, Value: false})); c != settings.CodeLocked {
			t.Fatalf("a tenant administrator: %s", c)
		}
		if c := code(s.SetTenant(ctx, ten, k, settings.Change{Site: true, HasLower: true, Lower: lower(settings.Lock)})); c != settings.CodeInvalid {
			t.Fatalf("a tenant's rule: %s", c)
		}
		if err := s.SetTenant(ctx, ten, k, settings.Change{Site: true, HasValue: true, Value: false}); err != nil {
			t.Fatal(err)
		}
		e, _ := s.Get(ctx, ten, k)
		if e.Value != false || e.Tenant.SiteRule == nil || e.Tenant.SiteRule.Value != false || e.Tenant.Value != nil {
			t.Fatalf("%+v %+v", e, e.Tenant)
		}
		if c := code(s.SetTenant(ctx, ten, settings.UsersReuseUsernames, settings.Change{Site: true, HasValue: true, Value: "never"})); c != settings.CodeNotFound {
			t.Fatalf("server-wide: %s", c)
		}
		if e, _ := s.Get(ctx, ten, settings.UsersReuseUsernames); e.Tenant != nil {
			t.Fatal("a tenant view of a server-wide key")
		}
	})
}

// Warm, For reads nothing: the database may as well be closed. One tenant's
// write recomputes that tenant only; a site write recomputes everyone.
func TestTheCacheIsPerTenant(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setupTwo(t, st)
		computes := map[string]int{}
		s.SetOnCompute(func(tenant string) { computes[tenant]++ })
		read := func() {
			t.Helper()
			for _, id := range []string{ten, ten2} {
				if _, err := s.For(ctx, id); err != nil {
					t.Fatal(err)
				}
			}
		}
		read()
		read()
		if computes[ten] != 1 || computes[ten2] != 1 {
			t.Fatalf("warm: %v", computes)
		}
		if err := s.SetTenant(ctx, ten, settings.LegalTermsURL, settings.Change{HasValue: true, Value: "https://acme.example/t"}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetTenant(ctx, ten, settings.OfflineCopies, settings.Change{Site: true, HasSiteRule: true, SiteRule: siteRule(false, settings.Lock)}); err != nil {
			t.Fatal(err)
		}
		read()
		if computes[ten] != 3 || computes[ten2] != 1 { // the second write reads ten again first
			t.Fatalf("after one tenant's writes: %v", computes)
		}
		if _, err := s.SetSite(ctx, settings.LegalTermsURL, settings.Change{HasValue: true, Value: "https://site.example/t"}); err != nil {
			t.Fatal(err)
		}
		read()
		if computes[ten] != 4 || computes[ten2] != 2 {
			t.Fatalf("after a site write: %v", computes)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		read() // no query: a closed database would fail it
		if e, _ := s.Get(ctx, ten, settings.LegalTermsURL); e.Value != "https://acme.example/t" {
			t.Fatalf("%v", e.Value)
		}
	})
}
