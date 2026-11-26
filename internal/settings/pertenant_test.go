package settings_test

import (
	"errors"
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
// offline copies" is features.offline locked off for Acme, "at most 10 spaces a
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
		// "At most 10" bounds what the tenant gets: not the site's 20.
		if e.Value != int64(10) || e.Tenant.Value != int64(15) || e.Tenant.InEffect || e.Above.Mode != settings.Restrict {
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

// Locking a setting for a tenant without a value locks what the tenant sees
// at that moment: its own value, the value of the site's rule, or the value in
// effect under the rule's restriction. It never changes what the tenant sees.
func TestLockingForOneTenantKeepsWhatItSees(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setupTwo(t, st)
		lock := settings.Change{Site: true, HasSiteLock: true, SiteLock: true}
		check := func(name, tenant, key string, want any) {
			t.Helper()
			e, err := s.Get(ctx, tenant, key)
			if err != nil {
				t.Fatal(err)
			}
			if e.Value != want || !e.Tenant.SiteLocked || e.LockedBy != settings.Site {
				t.Errorf("%s: %v (locked %v by %q), want %v", name, e.Value, e.Tenant.SiteLocked, e.LockedBy, want)
			}
		}
		do := func(tenant, key string, ch settings.Change) {
			t.Helper()
			if err := s.SetTenant(ctx, tenant, key, ch); err != nil {
				t.Fatal(err)
			}
		}

		do(ten, settings.StorageAssets, settings.Change{HasValue: true, Value: "space"})
		do(ten, settings.StorageAssets, lock)
		check("the tenant's own value", ten, settings.StorageAssets, "space")

		do(ten2, settings.StorageAssets, settings.Change{Site: true, HasSiteRule: true, SiteRule: siteRule("space", "")})
		do(ten2, settings.StorageAssets, lock)
		check("the site's value for the tenant", ten2, settings.StorageAssets, "space")

		k := settings.QuotaSpacesPerPerson
		max := settings.Change{Site: true, HasSiteRule: true, SiteRule: &settings.Rule{Lower: &settings.Lower{Mode: settings.Restrict, Max: ptr(int64(10))}}}
		do(ten, k, max)
		do(ten, k, settings.Change{HasValue: true, Value: int64(8)})
		do(ten, k, lock)
		check("the tenant's value under the rule's restriction", ten, k, int64(8))

		do(ten2, k, max)
		do(ten2, k, lock)
		check("nothing of the tenant's, under the rule's restriction", ten2, k, int64(10))
	})
}

// A restriction bounds the value in effect, not only the tenant's own: a
// value outside it gives way to the nearest one it allows (a limit its max, a
// boolean false, a choice the next stricter allowed one). For the site's
// lower over every tenant and for the site's rule for one.
func TestARestrictionBoundsTheValueInEffect(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setupTwo(t, st)
		for key, l := range map[string]settings.Lower{
			settings.QuotaSpacesPerPerson: {Mode: settings.Restrict, Max: ptr(int64(5))},
			settings.QuotaBytesPerPerson:  {Mode: settings.Restrict, Max: ptr(int64(1 << 30))},
			settings.OfflineCopies:        {Mode: settings.Restrict, Options: []any{false}},
			settings.StorageAssets:        {Mode: settings.Restrict, Options: []any{"space"}},
		} {
			if _, err := s.SetSite(ctx, key, settings.Change{HasLower: true, Lower: &l}); err != nil {
				t.Fatal(err)
			}
		}
		for key, want := range map[string]any{
			settings.QuotaSpacesPerPerson: int64(5),       // the site's 20
			settings.QuotaBytesPerPerson:  int64(1 << 30), // the site's no limit
			settings.OfflineCopies:        false,          // the site's true
			settings.StorageAssets:        "space",        // the site's repository
		} {
			if e, _ := s.Get(ctx, ten2, key); e.Value != want {
				t.Errorf("the site's restriction, %s: %v, want %v", key, e.Value, want)
			}
		}
		// The site's own value is not held to what it lets tenants do.
		if e, _ := s.Get(ctx, "", settings.QuotaSpacesPerPerson); e.Value != int64(20) {
			t.Errorf("the site's own: %v", e.Value)
		}
		// The site's rule for one tenant, looser than the site's, bounds that tenant alone: the site's 20 is 12.
		if err := s.SetTenant(ctx, ten, settings.QuotaSpacesPerPerson, settings.Change{Site: true, HasSiteRule: true,
			SiteRule: &settings.Rule{Lower: &settings.Lower{Mode: settings.Restrict, Max: ptr(int64(12))}}}); err != nil {
			t.Fatal(err)
		}
		if e, _ := s.Get(ctx, ten, settings.QuotaSpacesPerPerson); e.Value != int64(12) {
			t.Errorf("the rule for one tenant: %v, want 12", e.Value)
		}
	})
}

// Removing the site's rule for a tenant removes its row.
func TestRemovingTheSitesRuleDeletesItsRow(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		for _, remove := range []settings.Change{
			{Site: true, HasSiteRule: true},
			{Site: true, HasSiteLock: true, SiteLock: false},
		} {
			if err := s.SetTenant(ctx, ten, settings.OfflineCopies, settings.Change{Site: true, HasSiteRule: true, SiteRule: siteRule(false, settings.Lock)}); err != nil {
				t.Fatal(err)
			}
			if err := s.SetTenant(ctx, ten, settings.OfflineCopies, remove); err != nil {
				t.Fatal(err)
			}
			if r, err := store.GetLayered(ctx, st.Q(), string(settings.Site), ten, settings.OfflineCopies, "manual"); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("%+v: the row is %+v (%v)", remove, r, err)
			}
			if e, _ := s.Get(ctx, ten, settings.OfflineCopies); e.Value != true || e.Tenant.SiteRule != nil {
				t.Fatalf("%+v: %+v", remove, e.Tenant)
			}
		}
	})
}

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

// The version of a tenant's settings (the ETag of GET /api/settings) holds
// while nothing changed, changes with the tenant's own write and with the site's,
// and is not another tenant's; a read of a feature (On) and of the version cost
// no query once warm.
func TestTheVersionOfATenantsSettingsFollowsItsChanges(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setupTwo(t, st)
		ver := func(id string) string {
			t.Helper()
			v, err := s.Version(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			return v
		}
		a, b := ver(ten), ver(ten2)
		if a == b || a != ver(ten) || b != ver(ten2) {
			t.Fatalf("a tenant's version is its own and stays: %q %q", a, b)
		}
		if err := s.SetTenant(ctx, ten, settings.OfflineCopies, settings.Change{HasValue: true, Value: false}); err != nil {
			t.Fatal(err)
		}
		a2 := ver(ten)
		if a2 == a || ver(ten2) != b {
			t.Fatalf("a write moves its tenant's version only: %q -> %q, other %q", a, a2, ver(ten2))
		}
		if _, err := s.SetSite(ctx, settings.OfflineCopies, settings.Change{HasValue: true, Value: false}); err != nil {
			t.Fatal(err)
		}
		if ver(ten) == a2 || ver(ten2) == b {
			t.Fatal("a site write moves every tenant's version")
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		if s.On(ctx, ten, settings.OfflineCopies) || ver(ten) == "" { // no query: a closed database would fail it
			t.Fatal("off for the tenant that said so")
		}
	})
}

// A site value above the maximum it is held to is refused, not capped: the
// site's value against the site's own restriction, and the value of the site's
// rule for one tenant against that rule's. Lowering a maximum alone still
// bounds the value in effect (TestARestrictionBoundsTheValueInEffect).
func TestASiteValueAboveItsOwnMaxIsRefused(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		k := settings.QuotaSpacesPerPerson
		max10 := &settings.Lower{Mode: settings.Restrict, Max: ptr(int64(10))}
		refused := func(name string, err error) {
			t.Helper()
			var se *settings.Error
			if !errors.As(err, &se) || se.Code != settings.CodeAboveMax || se.Msg == "" || se.Msg[0] < 'A' || se.Msg[0] > 'Z' {
				t.Fatalf("%s: %v, want %s with a sentence", name, err, settings.CodeAboveMax)
			}
		}
		if _, err := s.SetSite(ctx, k, settings.Change{HasLower: true, Lower: max10}); err != nil {
			t.Fatal(err)
		}
		_, err := s.SetSite(ctx, k, settings.Change{HasValue: true, Value: int64(20)})
		refused("the site's value", err)
		_, err = s.SetSite(ctx, k, settings.Change{HasValue: true, Value: int64(12), HasLower: true, Lower: max10})
		refused("the value and the max together", err)
		if e, _ := s.Get(ctx, "", k); e.Value != int64(20) {
			t.Fatalf("a refused write changed the site's value: %v", e.Value)
		}
		if _, err := s.SetSite(ctx, k, settings.Change{HasValue: true, Value: int64(10)}); err != nil {
			t.Fatalf("at the max: %v", err)
		}

		refused("the rule for one tenant", s.SetTenant(ctx, ten, k, settings.Change{Site: true, HasSiteRule: true,
			SiteRule: &settings.Rule{Value: int64(30), Lower: &settings.Lower{Mode: settings.Restrict, Max: ptr(int64(12))}}}))
		if e, _ := s.Get(ctx, ten, k); e.Tenant.SiteRule != nil {
			t.Fatalf("a refused rule was kept: %+v", e.Tenant.SiteRule)
		}
		if err := s.SetTenant(ctx, ten, k, settings.Change{Site: true, HasSiteRule: true,
			SiteRule: &settings.Rule{Value: int64(12), Lower: &settings.Lower{Mode: settings.Restrict, Max: ptr(int64(12))}}}); err != nil {
			t.Fatalf("the rule at its max: %v", err)
		}
	})
}
