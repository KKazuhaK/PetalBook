package settings_test

import (
	"testing"

	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// The site's own value of a key with a tenant default (registration.mode)
// governs its own people only: no site-level rule holds a tenant, so the
// site's layer refuses one; only a rule for the tenant does, and the floor
// of the read, the write and the count of affected tenants is that rule's.
func TestASiteRuleNeverHoldsAKeyThatGovernsItsOwnPeople(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		key := settings.RegistrationMode
		if _, err := s.SetSite(ctx, key, settings.Change{HasValue: true, Value: settings.RegClosed}); err != nil {
			t.Fatal(err)
		}
		for _, mode := range []string{settings.Tighten, settings.Lock, settings.Restrict} {
			l := lower(mode)
			if mode == settings.Restrict {
				l.Options = []any{settings.RegClosed}
			}
			if _, err := s.SetSite(ctx, key, settings.Change{HasLower: true, Lower: l}); code(err) != settings.CodeInvalid {
				t.Fatalf("%s: %v", mode, err)
			}
		}
		// Allowing is fine, and counts nobody.
		if err := s.SetTenant(ctx, ten, key, settings.Change{HasValue: true, Value: settings.RegOpen}); err != nil {
			t.Fatal(err)
		}
		if n, err := s.SetSite(ctx, key, settings.Change{HasLower: true, Lower: lower(settings.Allow)}); err != nil || n != 0 {
			t.Fatalf("%d %v", n, err)
		}
		e, _ := s.Get(ctx, ten, key)
		d, _ := s.Def(key)
		if above, floor, by := settings.HeldTo(d, e.Site, nil); above.Mode != settings.Allow || floor != settings.RegInvite || by || e.Floor != settings.RegInvite || e.Value != settings.RegOpen {
			t.Fatalf("%v %v %v %+v", above, floor, by, e)
		}
		// A rule for the tenant that tightens holds it: a looser PUT is 409.
		rule := &settings.Rule{Value: settings.RegInvite, Lower: lower(settings.Tighten)}
		if err := s.SetTenant(ctx, ten, key, settings.Change{Site: true, HasSiteRule: true, SiteRule: rule}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetTenant(ctx, ten, key, settings.Change{HasValue: true, Value: settings.RegOpen}); code(err) != settings.CodeRestricted {
			t.Fatalf("%v", err)
		}
		e, _ = s.Get(ctx, ten, key)
		above, floor, by := settings.HeldTo(d, e.Site, e.Tenant.SiteRule)
		if above.Mode != settings.Tighten || floor != settings.RegInvite || !by || e.Floor != floor || e.Above.Mode != above.Mode || !e.ByRule {
			t.Fatalf("%v %v %v %+v", above, floor, by, e)
		}
		if err := s.SetTenant(ctx, ten, key, settings.Change{HasValue: true, Value: settings.RegClosed}); err != nil {
			t.Fatal(err)
		}
	})
}

// A floor that is not one of the choices holds everything: nothing is looser
// than the strictest.
func TestAFloorOutsideTheChoicesIsTheStrictest(t *testing.T) {
	ch := &settings.Def{Kind: settings.Choice, Strict: settings.StrictOrder, Choices: []string{"a", "b", "c"}}
	if !settings.Looser(ch, "c", "gone") || settings.Looser(ch, "gone", "gone") {
		t.Fatal("a floor outside the choices")
	}
}

// A key with no layer below the site has nothing to tighten.
func TestTightenNeedsALayerBelow(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		if _, err := s.SetSite(ctx, settings.SessionsMaxDays, settings.Change{HasLower: true, Lower: lower(settings.Tighten)}); code(err) != settings.CodeInvalid {
			t.Fatalf("%v", err)
		}
	})
}
