package settings_test

import (
	"slices"
	"testing"
	"time"

	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

func rule(t *testing.T, s *settings.Service, in settings.AssetsInput) settings.AssetsRule {
	t.Helper()
	r, err := s.AssetsRule(ctx, ten, in)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func asSite(v any) settings.Change {
	return settings.Change{HasValue: true, Value: v}
}

func TestAttachmentsAreSeparateByDefault(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		e, err := s.Get(ctx, ten, settings.StorageAssets)
		if err != nil || e.Value != "repository" || e.Site.Lower.Mode != settings.Allow || e.Tenant == nil {
			t.Fatalf("%+v %v", e, err)
		}
		// The switch is the site's (it may force it for one tenant), and on until
		// the site turns it off.
		x, err := s.Get(ctx, ten, settings.StorageAssetsOwnExempt)
		if err != nil || x.Value != true || x.Tenant == nil || x.Tenant.Value != nil {
			t.Fatalf("%+v %v", x, err)
		}
		r := rule(t, s, settings.AssetsInput{})
		if r.Default != "repository" || !slices.Equal(r.Allowed, []string{"space", "repository"}) || r.LockedBy != "" {
			t.Fatalf("%+v", r)
		}
	})
}

// site lock > site lock for one tenant > site default > tenant default > space owner.
func TestTheLayersOfWhereAttachmentsGoAreAppliedInTheOwnersOrder(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		// The tenant's default beats the site's default.
		if err := s.SetTenant(ctx, ten, settings.StorageAssets, settings.Change{HasValue: true, Value: "space"}); err != nil {
			t.Fatal(err)
		}
		if r := rule(t, s, settings.AssetsInput{}); r.Default != "space" || len(r.Allowed) != 2 {
			t.Fatalf("tenant default: %+v", r)
		}
		// The tenant can lock its own spaces' owners to its value.
		if err := s.SetTenant(ctx, ten, settings.StorageAssets, settings.Change{HasLower: true, Lower: lower("lock")}); err != nil {
			t.Fatal(err)
		}
		if r := rule(t, s, settings.AssetsInput{}); r.Default != "space" || !slices.Equal(r.Allowed, []string{"space"}) || r.LockedBy != settings.Tenant {
			t.Fatalf("tenant lock: %+v", r)
		}
		// A site lock for one tenant beats the tenant's default and rule, and the tenant cannot undo it.
		if err := s.SetTenant(ctx, ten, settings.StorageAssets, settings.Change{HasValue: true, Value: "repository", Site: true, HasSiteLock: true, SiteLock: true}); err != nil {
			t.Fatal(err)
		}
		r := rule(t, s, settings.AssetsInput{})
		if r.Default != "repository" || !slices.Equal(r.Allowed, []string{"repository"}) || r.LockedBy != settings.Site {
			t.Fatalf("site lock for one tenant: %+v", r)
		}
		if err := s.SetTenant(ctx, ten, settings.StorageAssets, settings.Change{HasValue: true, Value: "space"}); code(err) != settings.CodeLocked {
			t.Fatalf("the tenant changed a value the site locked for it: %v", err)
		}
		if err := s.SetTenant(ctx, ten, settings.StorageAssets, settings.Change{HasLower: true, Lower: lower("allow")}); code(err) != settings.CodeLocked {
			t.Fatalf("the tenant changed a rule the site locked for it: %v", err)
		}
		if err := s.SetTenant(ctx, ten, settings.StorageAssets, settings.Change{HasSiteLock: true, SiteLock: false}); code(err) != settings.CodeLocked {
			t.Fatalf("the tenant lifted the site's lock: %v", err)
		}
		// It beats a site default of the other kind.
		if _, err := s.SetSite(ctx, settings.StorageAssets, asSite("space")); err != nil {
			t.Fatal(err)
		}
		if r := rule(t, s, settings.AssetsInput{}); r.Default != "repository" || r.LockedBy != settings.Site {
			t.Fatalf("site default: %+v", r)
		}
		// The tenant's view says who locked it.
		if e, _ := s.Get(ctx, ten, settings.StorageAssets); !e.Tenant.SiteLocked || e.LockedBy != settings.Site {
			t.Fatalf("%+v %+v", e, e.Tenant)
		}
		// The site's lock over everything beats the one for the tenant.
		if _, err := s.SetSite(ctx, settings.StorageAssets, settings.Change{HasLower: true, Lower: lower("lock")}); err != nil {
			t.Fatal(err)
		}
		if r := rule(t, s, settings.AssetsInput{}); r.Default != "space" || !slices.Equal(r.Allowed, []string{"space"}) || r.LockedBy != settings.Site {
			t.Fatalf("site lock: %+v", r)
		}
		if e, _ := s.Get(ctx, ten, settings.StorageAssets); e.Tenant.InEffect || e.Tenant.Value != "space" ||
			e.Tenant.SiteRule.InEffect || e.Tenant.SiteRule.Value != "repository" {
			t.Fatalf("the tenant's value and the site's rule for it are kept, not in effect: %+v %+v", e.Tenant, e.Tenant.SiteRule)
		}
		// Lifting the site's lock brings the one for the tenant back; lifting that one frees the tenant's own value.
		s.SetSite(ctx, settings.StorageAssets, settings.Change{HasLower: true, Lower: lower("allow")})
		if r := rule(t, s, settings.AssetsInput{}); r.Default != "repository" || r.LockedBy != settings.Site {
			t.Fatalf("%+v", r)
		}
		if err := s.SetTenant(ctx, ten, settings.StorageAssets, settings.Change{Site: true, HasSiteLock: true, SiteLock: false}); err != nil {
			t.Fatal(err)
		}
		// The tenant's own value and rule (a lock to it) were there all along and count again.
		if r := rule(t, s, settings.AssetsInput{}); r.Default != "space" || len(r.Allowed) != 1 || r.LockedBy != settings.Tenant {
			t.Fatalf("the tenant's own lock comes back: %+v", r)
		}
		if err := s.SetTenant(ctx, ten, settings.StorageAssets, settings.Change{HasValue: true, Value: nil, HasLower: true, Lower: lower("allow")}); err != nil {
			t.Fatal(err)
		}
		if r := rule(t, s, settings.AssetsInput{}); r.Default != "space" || len(r.Allowed) != 2 || r.LockedBy != "" {
			t.Fatalf("back to the site's default: %+v", r)
		}
	})
}

// A site lock for one tenant with no value locks the site's value for it; only
// the site's administrators lock.
func TestASiteLockForOneTenantNeedsTheSitesHand(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		if err := s.SetTenant(ctx, ten, settings.StorageAssets, settings.Change{HasSiteLock: true, SiteLock: true, HasValue: true, Value: "space"}); code(err) != settings.CodeLocked {
			t.Fatalf("a tenant administrator locked: %v", err)
		}
		if err := s.SetTenant(ctx, ten, settings.StorageAssets, settings.Change{Site: true, HasSiteLock: true, SiteLock: true}); err != nil {
			t.Fatal(err)
		}
		if r := rule(t, s, settings.AssetsInput{}); r.Default != "repository" || len(r.Allowed) != 1 || r.LockedBy != settings.Site {
			t.Fatalf("the site's value locked: %+v", r)
		}
	})
}

// The site's rule for a tenant is a row of its own: a tenant administrator's
// write checked against a stale cache (the rule written a moment before, by
// another request or another server) writes the tenant's row only, and the
// rule wins as soon as it is read.
func TestATenantWriteThatRacesASiteLockNeverClearsIt(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		if _, err := s.Get(ctx, ten, settings.StorageAssets); err != nil { // the cache is warm and says "not locked"
			t.Fatal(err)
		}
		row := store.LayeredRow{Layer: "site", LayerID: ten, Key: settings.StorageAssets, Value: `"repository"`, Lower: `{"mode":"lock"}`, Updated: time.Now()}
		if err := store.PutLayered(ctx, st.Q(), row); err != nil { // behind the cache's back
			t.Fatal(err)
		}
		if err := s.SetTenant(ctx, ten, settings.StorageAssets, settings.Change{HasValue: true, Value: "space"}); err != nil {
			t.Fatal(err)
		}
		// That write dropped the stale view: the next one sees the lock.
		if err := s.SetTenant(ctx, ten, settings.StorageAssets, settings.Change{HasLower: true, Lower: lower("allow")}); code(err) != settings.CodeLocked {
			t.Fatalf("after the stale write: %v", err)
		}
		got, err := store.GetLayered(ctx, st.Q(), "site", ten, settings.StorageAssets, "manual")
		if err != nil || got.Value != `"repository"` || got.Lower != `{"mode":"lock"}` {
			t.Fatalf("%+v %v", got, err)
		}
		if r := rule(t, s, settings.AssetsInput{}); r.Default != "repository" || len(r.Allowed) != 1 || r.LockedBy != settings.Site {
			t.Fatalf("the rule does not win: %+v", r)
		}
	})
}

// A site lock committed while a tenant administrator's write is in its
// transaction survives it, and refuses the next one.
func TestASiteLockCommittedDuringATenantWriteSurvives(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		if _, err := s.Get(ctx, ten, settings.StorageAssets); err != nil { // the cache says "not locked"
			t.Fatal(err)
		}
		lockErr := make(chan error, 1)
		s.SetBeforeTenantWrite(func() {
			s.SetBeforeTenantWrite(nil)
			go func() {
				lockErr <- s.SetTenant(ctx, ten, settings.StorageAssets,
					settings.Change{Site: true, HasSiteLock: true, SiteLock: true, HasValue: true, Value: "repository"})
			}()
			// PostgreSQL commits the lock now; SQLite's single writer holds it until the tenant commits.
			select {
			case err := <-lockErr:
				lockErr <- err
			case <-time.After(300 * time.Millisecond):
			}
		})
		if err := s.SetTenant(ctx, ten, settings.StorageAssets, settings.Change{HasValue: true, Value: "space"}); err != nil {
			t.Fatalf("tenant write: %v", err)
		}
		if err := <-lockErr; err != nil {
			t.Fatalf("site lock: %v", err)
		}
		got, gerr := store.GetLayered(ctx, st.Q(), "site", ten, settings.StorageAssets, "manual")
		if gerr != nil || got.Value != `"repository"` || got.Lower != `{"mode":"lock"}` {
			t.Fatalf("the lock did not survive: %+v %v", got, gerr)
		}
		if err := s.SetTenant(ctx, ten, settings.StorageAssets, settings.Change{HasValue: true, Value: "space"}); code(err) != settings.CodeLocked {
			t.Fatalf("after the lock: %v", err)
		}
	})
}

func TestTheSitesRestrictionLeavesTheOwnersWhatItNames(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		if _, err := s.SetSite(ctx, settings.StorageAssets, settings.Change{HasLower: true, Lower: &settings.Lower{Mode: settings.Restrict, Options: []any{"space"}}}); err != nil {
			t.Fatal(err)
		}
		// The site's value is "repository", which its own restriction leaves out: the first allowed one is the default.
		if r := rule(t, s, settings.AssetsInput{}); r.Default != "space" || !slices.Equal(r.Allowed, []string{"space"}) {
			t.Fatalf("%+v", r)
		}
	})
}

// What the limits sheet's sync.assetsRepository was is now a rule for the tenant: storage.assets restricted to "space".
func TestARuleRestrictingTheAttachmentsToTheSpaceLeavesOnlyTheSpace(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		only := &settings.Rule{Lower: &settings.Lower{Mode: settings.Restrict, Options: []any{"space"}}}
		if err := s.SetTenant(ctx, ten, settings.StorageAssets, settings.Change{Site: true, HasSiteRule: true, SiteRule: only}); err != nil {
			t.Fatal(err)
		}
		r := rule(t, s, settings.AssetsInput{})
		if r.Default != "space" || !slices.Equal(r.Allowed, []string{"space"}) {
			t.Fatalf("%+v", r)
		}
	})
}

func TestATenantOnItsOwnStorageIsNotBoundByTheSitesDefaultOrLock(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		own := settings.AssetsInput{OwnStorage: true}
		if _, err := s.SetSite(ctx, settings.StorageAssets, settings.Change{HasValue: true, Value: "repository", HasLower: true, Lower: lower("lock")}); err != nil {
			t.Fatal(err)
		}
		// Bound: the site's lock applies. Exempt: it chooses freely, and with no value of its own it gets the built-in default.
		if r := rule(t, s, settings.AssetsInput{}); !slices.Equal(r.Allowed, []string{"repository"}) {
			t.Fatalf("%+v", r)
		}
		if r := rule(t, s, own); !slices.Equal(r.Allowed, []string{"space", "repository"}) || r.LockedBy != "" || r.Default != "repository" {
			t.Fatalf("exempt: %+v", r)
		}
		// The tenant's own layers still apply: its default and its rule for the owners.
		s.SetSite(ctx, settings.StorageAssets, settings.Change{HasLower: true, Lower: lower("allow")})
		if err := s.SetTenant(ctx, ten, settings.StorageAssets, settings.Change{HasValue: true, Value: "space", HasLower: true, Lower: lower("lock")}); err != nil {
			t.Fatal(err)
		}
		s.SetSite(ctx, settings.StorageAssets, settings.Change{HasLower: true, Lower: lower("lock")})
		if r := rule(t, s, own); r.Default != "space" || !slices.Equal(r.Allowed, []string{"space"}) || r.LockedBy != settings.Tenant {
			t.Fatalf("exempt, tenant's layers: %+v", r)
		}
		// A site lock for this one tenant is the site's too.
		s.SetSite(ctx, settings.StorageAssets, settings.Change{HasLower: true, Lower: lower("allow")})
		if err := s.SetTenant(ctx, ten, settings.StorageAssets, settings.Change{HasLower: true, Lower: lower("allow")}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetTenant(ctx, ten, settings.StorageAssets, settings.Change{HasValue: true, Value: "repository", Site: true, HasSiteLock: true, SiteLock: true}); err != nil {
			t.Fatal(err)
		}
		if r := rule(t, s, own); len(r.Allowed) != 2 || r.LockedBy != "" {
			t.Fatalf("exempt from the site's lock for the tenant: %+v", r)
		}
		// With the switch off, a tenant on its own storage is bound like any other.
		if _, err := s.SetSite(ctx, settings.StorageAssetsOwnExempt, asSite(false)); err != nil {
			t.Fatal(err)
		}
		if r := rule(t, s, own); !slices.Equal(r.Allowed, []string{"repository"}) || r.LockedBy != settings.Site {
			t.Fatalf("switch off: %+v", r)
		}
	})
}
