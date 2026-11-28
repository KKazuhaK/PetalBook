package settings_test

import (
	"testing"

	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// The keys registered ahead of the features that read them: their defaults are
// what a tenant has today, the tenant may set the ones with a tenant layer, and
// the site-only ones say why.
func TestReservedKeysAreRegisteredWithTheirDefaults(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		for _, k := range []string{settings.MailUseSite, settings.DailyUseSiteAPIs, settings.ConnectionsUseSiteApps} {
			d, ok := s.Def(k)
			if !ok || d.Kind != settings.Bool || !d.Has(settings.Site) || !d.Has(settings.Tenant) || d.Default != true {
				t.Fatalf("%s: %+v", k, d)
			}
			e, err := s.Get(ctx, ten, k)
			if err != nil || e.Value != true {
				t.Fatalf("%s: %+v %v", k, e, err)
			}
			if err := s.SetTenant(ctx, ten, k, settings.Change{HasValue: true, Value: false}); err != nil {
				t.Fatalf("%s: %v", k, err)
			}
			if e, _ = s.Get(ctx, ten, k); e.Value != false {
				t.Fatalf("%s: %+v", k, e)
			}
		}
		d, ok := s.Def(settings.StoragePartialUnsupported)
		if !ok || d.Kind != settings.Choice || d.Default != "allow" || !d.Has(settings.Tenant) || len(d.Choices) != 2 || d.Choices[1] != "refuse" {
			t.Fatalf("%+v", d)
		}
		if err := s.SetTenant(ctx, ten, settings.StoragePartialUnsupported, settings.Change{HasValue: true, Value: "sometimes"}); err == nil {
			t.Fatal("an unknown choice was accepted")
		}
		if err := s.SetTenant(ctx, ten, settings.StoragePartialUnsupported, settings.Change{HasValue: true, Value: "refuse"}); err != nil {
			t.Fatal(err)
		}
		if e, _ := s.Get(ctx, ten, settings.StoragePartialUnsupported); e.Value != "refuse" {
			t.Fatalf("%+v", e)
		}
		// The outbound caps are the server's, with the documented defaults.
		for k, want := range map[string]int64{settings.OutboundPerMinute: 600, settings.OutboundTenantPerMinute: 120} {
			d, ok := s.Def(k)
			if !ok || d.Kind != settings.Limit || d.Has(settings.Tenant) || d.ServerWide == "" || d.Default != want {
				t.Fatalf("%s: %+v", k, d)
			}
		}
	})
}
