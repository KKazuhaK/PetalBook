package settings_test

import (
	"slices"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/settings"
	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
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
		// The switch is the site's only, and on until the site turns it off.
		x, err := s.Get(ctx, ten, settings.StorageAssetsOwnExempt)
		if err != nil || x.Value != true || x.Tenant != nil {
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
		if e, _ := s.Get(ctx, ten, settings.StorageAssets); e.Tenant.InEffect || e.Tenant.Value != "repository" {
			t.Fatalf("the tenant's value is kept, not in effect: %+v", e.Tenant)
		}
		// Lifting the site's lock brings the one for the tenant back; lifting that one frees the tenant's own value.
		s.SetSite(ctx, settings.StorageAssets, settings.Change{HasLower: true, Lower: lower("allow")})
		if r := rule(t, s, settings.AssetsInput{}); r.Default != "repository" || r.LockedBy != settings.Site {
			t.Fatalf("%+v", r)
		}
		if err := s.SetTenant(ctx, ten, settings.StorageAssets, settings.Change{Site: true, HasSiteLock: true, SiteLock: false}); err != nil {
			t.Fatal(err)
		}
		// The tenant's own rule (a lock to its value) was there all along and counts again.
		if r := rule(t, s, settings.AssetsInput{}); r.Default != "repository" || len(r.Allowed) != 1 || r.LockedBy != settings.Tenant {
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

func TestASiteLockForOneTenantNeedsAValueAndTheSitesHand(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		if err := s.SetTenant(ctx, ten, settings.StorageAssets, settings.Change{Site: true, HasSiteLock: true, SiteLock: true}); code(err) != settings.CodeInvalid {
			t.Fatalf("a lock on nothing: %v", err)
		}
		if err := s.SetTenant(ctx, ten, settings.StorageAssets, settings.Change{HasSiteLock: true, SiteLock: true, HasValue: true, Value: "space"}); code(err) != settings.CodeLocked {
			t.Fatalf("a tenant administrator locked: %v", err)
		}
	})
}

// A tenant administrator's write that was checked against a stale cache (the site's lock was written a moment
// before, by another request or another server) is refused when the row is read in the transaction, and the
// lock is never cleared by a tenant-layer write.
func TestATenantWriteThatRacesASiteLockIsRefusedAndKeepsTheLock(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		if _, err := s.Get(ctx, ten, settings.StorageAssets); err != nil { // the cache is warm and says "not locked"
			t.Fatal(err)
		}
		row := store.LayeredRow{Layer: "tenant", LayerID: ten, Key: settings.StorageAssets, Value: `"repository"`, SiteLocked: true, Updated: time.Now()}
		if err := store.PutLayered(ctx, st.Q(), row); err != nil { // behind the cache's back
			t.Fatal(err)
		}
		for _, ch := range []settings.Change{
			{HasValue: true, Value: "space"},
			{HasValue: true, Value: nil},
			{HasLower: true, Lower: lower("lock")},
		} {
			if err := s.SetTenant(ctx, ten, settings.StorageAssets, ch); code(err) != settings.CodeLocked {
				t.Fatalf("%+v: %v", ch, err)
			}
		}
		got, err := store.GetLayered(ctx, st.Q(), "tenant", ten, settings.StorageAssets)
		if err != nil || !got.SiteLocked || got.Value != `"repository"` || got.Lower != "" {
			t.Fatalf("%+v %v", got, err)
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

// The tenant's limits table (sync.assetsRepository) is not built yet; its answer is an input.
func TestWhenTheLimitsTableTurnsTheAttachmentRepositoryOffOnlyTheSpaceIsPossible(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		if err := s.SetTenant(ctx, ten, settings.StorageAssets, settings.Change{HasValue: true, Value: "repository", Site: true, HasSiteLock: true, SiteLock: true}); err != nil {
			t.Fatal(err)
		}
		r := rule(t, s, settings.AssetsInput{RepositoryOff: true})
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
