package settings_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

var ctx = context.Background()

const ten = "T000000002"

func setup(t *testing.T, st *store.Store) *settings.Service {
	t.Helper()
	now := time.Now()
	if err := st.CreateTenant(ctx, store.Tenant{ID: ten, Slug: "acme", Name: "Acme", Created: now, Updated: now}); err != nil {
		t.Fatal(err)
	}
	return settings.New(st)
}

func code(err error) string {
	var se *settings.Error
	if errors.As(err, &se) {
		return se.Code
	}
	if err == nil {
		return ""
	}
	return "other: " + err.Error()
}

func lower(mode string) *settings.Lower { return &settings.Lower{Mode: mode} }

func TestDefaultsAndFollowing(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		e, err := s.Get(ctx, ten, settings.RegistrationOpen)
		if err != nil {
			t.Fatal(err)
		}
		if e.Value != false || e.Site.Lower.Mode != settings.Lock || e.Tenant == nil || e.Tenant.Value != nil || !e.Tenant.InEffect {
			t.Fatalf("%+v %+v", e, e.Tenant)
		}
		// The tenant follows the site until it says otherwise.
		if _, err := s.SetSite(ctx, settings.LegalTermsURL, settings.Change{HasValue: true, Value: "https://example.com/terms"}); err != nil {
			t.Fatal(err)
		}
		if e, _ := s.Get(ctx, ten, settings.LegalTermsURL); e.Value != "https://example.com/terms" {
			t.Fatalf("%v", e.Value)
		}
		if err := s.SetTenant(ctx, ten, settings.LegalTermsURL, settings.Change{HasValue: true, Value: "https://acme.example/terms"}); err != nil {
			t.Fatal(err)
		}
		if e, _ := s.Get(ctx, ten, settings.LegalTermsURL); e.Value != "https://acme.example/terms" {
			t.Fatalf("%v", e.Value)
		}
		// null follows the site again.
		if err := s.SetTenant(ctx, ten, settings.LegalTermsURL, settings.Change{HasValue: true, Value: nil}); err != nil {
			t.Fatal(err)
		}
		if e, _ := s.Get(ctx, ten, settings.LegalTermsURL); e.Value != "https://example.com/terms" || e.Tenant.Value != nil {
			t.Fatalf("%+v", e)
		}
		// The site alone ("" tenant) has no tenant layer.
		if e, _ := s.Get(ctx, "", settings.LegalTermsURL); e.Tenant != nil {
			t.Fatal("a tenant layer for no tenant")
		}
	})
}

func TestLockKeepsTheTenantsValueUntilUnlocked(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		// Open the lock, set the tenant's value, lock again: kept, not in effect.
		if _, err := s.SetSite(ctx, settings.LegalPrivacyURL, settings.Change{HasLower: true, Lower: lower("allow")}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetTenant(ctx, ten, settings.LegalPrivacyURL, settings.Change{HasValue: true, Value: "https://acme.example/privacy"}); err != nil {
			t.Fatal(err)
		}
		n, err := s.SetSite(ctx, settings.LegalPrivacyURL, settings.Change{HasLower: true, Lower: lower("lock")})
		if err != nil || n != 1 {
			t.Fatalf("affected %d %v", n, err)
		}
		e, _ := s.Get(ctx, ten, settings.LegalPrivacyURL)
		if e.Tenant.InEffect || e.Tenant.Value != "https://acme.example/privacy" || e.Value != "" {
			t.Fatalf("locked: %+v %+v", e, e.Tenant)
		}
		// A locked setting cannot be set: setting_locked, whoever asks.
		if err := s.SetTenant(ctx, ten, settings.LegalPrivacyURL, settings.Change{HasValue: true, Value: "https://x.example"}); code(err) != settings.CodeLocked {
			t.Fatalf("%v", err)
		}
		// Unlocking brings the kept value back.
		s.SetSite(ctx, settings.LegalPrivacyURL, settings.Change{HasLower: true, Lower: lower("allow")})
		if e, _ := s.Get(ctx, ten, settings.LegalPrivacyURL); !e.Tenant.InEffect || e.Value != "https://acme.example/privacy" {
			t.Fatalf("unlocked: %+v", e)
		}
	})
}

func TestRestrictLimit(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		// registration.perHour: the site's rule is restrict, max 50 by default.
		if err := s.SetTenant(ctx, ten, settings.RegistrationPerHour, settings.Change{HasValue: true, Value: 80.0}); code(err) != settings.CodeRestricted {
			t.Fatalf("above the site's max: %v", err)
		}
		if err := s.SetTenant(ctx, ten, settings.RegistrationPerHour, settings.Change{HasValue: true, Value: 20.0}); err != nil {
			t.Fatal(err)
		}
		if e, _ := s.Get(ctx, ten, settings.RegistrationPerHour); e.Value != int64(20) {
			t.Fatalf("%v", e.Value)
		}
		for _, bad := range []any{0.0, 20000.0, "7", 1.5} {
			if err := s.SetTenant(ctx, ten, settings.RegistrationPerHour, settings.Change{HasValue: true, Value: bad}); code(err) != settings.CodeInvalid {
				t.Fatalf("%v: %v", bad, err)
			}
		}
		// The site tightens to 10: the tenant's 20 is kept, not in effect, and counted.
		ten10 := int64(10)
		n, err := s.SetSite(ctx, settings.RegistrationPerHour, settings.Change{HasLower: true, Lower: &settings.Lower{Mode: settings.Restrict, Max: &ten10}})
		if err != nil || n != 1 {
			t.Fatalf("%d %v", n, err)
		}
		e, _ := s.Get(ctx, ten, settings.RegistrationPerHour)
		if e.Tenant.InEffect || e.Tenant.Value != int64(20) || e.Value != int64(50) {
			t.Fatalf("%+v %+v", e, e.Tenant)
		}
		// The tenant's own rule for lower layers must be at least as strict.
		loose := int64(30)
		if err := s.SetTenant(ctx, ten, settings.RegistrationPerHour, settings.Change{HasLower: true, Lower: &settings.Lower{Mode: settings.Restrict, Max: &loose}}); code(err) != settings.CodeRestricted {
			t.Fatalf("a looser rule: %v", err)
		}
		tight := int64(5)
		if err := s.SetTenant(ctx, ten, settings.RegistrationPerHour, settings.Change{HasLower: true, Lower: &settings.Lower{Mode: settings.Restrict, Max: &tight}}); err != nil {
			t.Fatal(err)
		}
		if e, _ := s.Get(ctx, ten, settings.RegistrationPerHour); e.Below.Max == nil || *e.Below.Max != 5 {
			t.Fatalf("what the layers under the tenant may do: %+v", e.Below)
		}
	})
}

func TestRegistrationOpenIsLockedBySite(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		if err := s.SetTenant(ctx, ten, settings.RegistrationOpen, settings.Change{HasValue: true, Value: true}); code(err) != settings.CodeLocked {
			t.Fatalf("%v", err)
		}
		// Unlocked at the site, the tenant may open it; the site's own value stays false.
		if _, err := s.SetSite(ctx, settings.RegistrationOpen, settings.Change{HasLower: true, Lower: lower("allow")}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetTenant(ctx, ten, settings.RegistrationOpen, settings.Change{HasValue: true, Value: true}); err != nil {
			t.Fatal(err)
		}
		if e, _ := s.Get(ctx, ten, settings.RegistrationOpen); e.Value != true || e.Site.Value != false {
			t.Fatalf("%+v", e)
		}
		if e, _ := s.Get(ctx, "", settings.RegistrationOpen); e.Value != false {
			t.Fatal("a tenant's value leaked into the site's")
		}
		// A boolean can only be restricted to [false].
		_, err := s.SetSite(ctx, settings.RegistrationOpen, settings.Change{HasLower: true, Lower: &settings.Lower{Mode: settings.Restrict, Options: []any{true}}})
		if code(err) != settings.CodeInvalid {
			t.Fatalf("%v", err)
		}
		if _, err := s.SetSite(ctx, settings.RegistrationOpen, settings.Change{HasLower: true, Lower: &settings.Lower{Mode: settings.Restrict, Options: []any{false}}}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetTenant(ctx, ten, settings.RegistrationOpen, settings.Change{HasValue: true, Value: true}); code(err) != settings.CodeRestricted {
			t.Fatalf("turning on under 'only off': %v", err)
		}
		// The tenant's true is kept but no longer in effect.
		if e, _ := s.Get(ctx, ten, settings.RegistrationOpen); e.Tenant.InEffect || e.Value != false {
			t.Fatalf("%+v", e)
		}
	})
}

func TestValuesAreChecked(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		cases := []struct {
			key  string
			v    any
			want string
		}{
			{settings.RegistrationDomains, []any{"example.com", "@Example.org"}, ""},
			{settings.RegistrationDomains, []any{"nodot"}, settings.CodeInvalid},
			{settings.RegistrationDomains, "example.com", settings.CodeInvalid},
			{settings.LegalTermsURL, "http://example.com", settings.CodeInvalid},
			{settings.LegalTermsURL, "https://user:pw@example.com", settings.CodeInvalid},
			{settings.LegalTermsURL, "", ""},
			{settings.RegistrationOpen, "yes", settings.CodeInvalid},
		}
		for _, c := range cases {
			_, err := s.SetSite(ctx, c.key, settings.Change{HasValue: true, Value: c.v})
			if code(err) != c.want {
				t.Errorf("%s %v: %v", c.key, c.v, err)
			}
		}
		if e, _ := s.Get(ctx, "", settings.RegistrationDomains); len(e.Value.([]string)) != 2 || e.Value.([]string)[1] != "example.org" {
			t.Fatalf("%v", e.Value)
		}
		if _, err := s.SetSite(ctx, "no.such", settings.Change{HasValue: true, Value: 1}); code(err) != settings.CodeNotFound {
			t.Fatalf("%v", err)
		}
		if err := s.SetTenant(ctx, ten, "no.such", settings.Change{HasValue: true, Value: 1}); code(err) != settings.CodeNotFound {
			t.Fatalf("%v", err)
		}
		// daily.own has a tenant layer and a space layer; the site's value is bound elsewhere.
		d, _ := s.Def(settings.DailyOwn)
		if !d.Has(settings.Space) || d.Has(settings.Person) {
			t.Fatalf("%v", d.Layers)
		}
	})
}

func TestTheEffectiveValuesAreCachedAndInvalidated(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		a, _ := s.For(ctx, ten)
		b, _ := s.For(ctx, ten)
		if len(a) == 0 || &a == &b {
			t.Fatal("no settings")
		}
		// The very same map: a request reads one cached map, it walks nothing.
		a["x"], b["x"] = nil, nil
		if _, ok := b["x"]; !ok {
			t.Fatal("the second read was computed again")
		}
		delete(a, "x")
		s.SetTenant(ctx, ten, settings.LegalTermsURL, settings.Change{HasValue: true, Value: "https://acme.example/t"})
		c, _ := s.For(ctx, ten)
		if c[settings.LegalTermsURL].Value != "https://acme.example/t" {
			t.Fatal("a change did not drop the cache")
		}
	})
}

func TestOfflineCopiesIsOnUntilSiteOrTenantTurnsItOff(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		d, _ := s.Def(settings.OfflineCopies)
		if d.Kind != settings.Bool || d.Has(settings.Space) || d.Has(settings.Person) || !d.Has(settings.Site) || !d.Has(settings.Tenant) {
			t.Fatalf("layers: %+v", d.Layers)
		}
		on := func() bool {
			e, err := s.Get(ctx, ten, settings.OfflineCopies)
			if err != nil {
				t.Fatal(err)
			}
			return e.Value == true
		}
		if !on() {
			t.Fatal("not allowed by default")
		}
		// A tenant turns it off for itself, and on again.
		if err := s.SetTenant(ctx, ten, settings.OfflineCopies, settings.Change{HasValue: true, Value: false}); err != nil {
			t.Fatal(err)
		}
		if on() {
			t.Fatal("the tenant's off did not count")
		}
		if e, _ := s.Get(ctx, "", settings.OfflineCopies); e.Value != true {
			t.Fatal("a tenant's value leaked into the site's")
		}
		if err := s.SetTenant(ctx, ten, settings.OfflineCopies, settings.Change{HasValue: true, Value: true}); err != nil {
			t.Fatal(err)
		}
		if !on() {
			t.Fatal("on again")
		}
		// The site's off is a default: a tenant that says on is on. Locked, it is off for every tenant.
		if _, err := s.SetSite(ctx, settings.OfflineCopies, settings.Change{HasValue: true, Value: false}); err != nil {
			t.Fatal(err)
		}
		if !on() {
			t.Fatal("the tenant's own on did not count under an unlocked site off")
		}
		if _, err := s.SetSite(ctx, settings.OfflineCopies, settings.Change{HasLower: true, Lower: lower("lock")}); err != nil {
			t.Fatal(err)
		}
		if on() {
			t.Fatal("the site's locked off did not count")
		}
		if _, err := s.SetSite(ctx, settings.OfflineCopies, settings.Change{HasValue: true, Value: true, HasLower: true, Lower: lower("allow")}); err != nil {
			t.Fatal(err)
		}
		// A lock: the tenant cannot change it, and what it kept before stays.
		if err := s.SetTenant(ctx, ten, settings.OfflineCopies, settings.Change{HasValue: true, Value: false}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SetSite(ctx, settings.OfflineCopies, settings.Change{HasLower: true, Lower: lower("lock")}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetTenant(ctx, ten, settings.OfflineCopies, settings.Change{HasValue: true, Value: false}); code(err) != settings.CodeLocked {
			t.Fatalf("%v", err)
		}
		if !on() {
			t.Fatal("a locked tenant value counted")
		}
		// Restricted to off: the tenant may only turn it off.
		if _, err := s.SetSite(ctx, settings.OfflineCopies, settings.Change{HasLower: true, Lower: &settings.Lower{Mode: settings.Restrict, Options: []any{false}}}); err != nil {
			t.Fatal(err)
		}
		if on() {
			t.Fatal("the tenant's kept off counts again")
		}
		if err := s.SetTenant(ctx, ten, settings.OfflineCopies, settings.Change{HasValue: true, Value: true}); code(err) != settings.CodeRestricted {
			t.Fatalf("%v", err)
		}
		if err := s.SetTenant(ctx, ten, settings.OfflineCopies, settings.Change{HasValue: true, Value: "yes"}); code(err) != settings.CodeInvalid {
			t.Fatalf("%v", err)
		}
	})
}
