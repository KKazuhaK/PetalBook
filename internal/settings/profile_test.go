package settings_test

import (
	"strings"
	"testing"

	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// Every key a profile names is in the registry, and cn switches off every
// setting the registry marks as letting content leave the organisation, so a
// new one cannot be forgotten and a renamed one fails here.
func TestProfilesNameRegistryKeysAndCNClosesEveryOutwardOne(t *testing.T) {
	t.Parallel()
	s := settings.New(nil)
	in := map[string]any{}
	for _, name := range settings.ProfileNames() {
		vals, _ := settings.ProfileValues(name)
		for _, kv := range vals {
			d, ok := s.Def(kv.Key)
			if !ok {
				t.Fatalf("profile %s: no setting %s", name, kv.Key)
			}
			if _, err := d.Value(kv.Value); err != nil || !d.Has(settings.Site) {
				t.Fatalf("profile %s: %s: %v", name, kv.Key, err)
			}
			if name == settings.ProfileCN {
				in[kv.Key] = kv.Value
			}
		}
	}
	for _, d := range s.Defs() {
		if d.Outward && in[d.Key] != false {
			t.Errorf("%s lets content leave the organisation and cn does not switch it off", d.Key)
		}
	}
	// GitHub stays reachable.
	if v, ok := in[settings.FeatureOwnConnections]; ok && v == false {
		t.Error("cn switches off connections: GitHub is reachable from China")
	}
}

func TestProfileSeedsAnEmptyDatabaseOnceAndOnlyOnce(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := settings.New(st)
		seeded, err := s.SeedProfile(ctx, settings.ProfileCN)
		if err != nil || !seeded {
			t.Fatal(seeded, err)
		}
		m, _, _ := s.Site(ctx)
		if m[settings.FeatureMCP].Value != false || m[settings.FeatureTokens].Value != false {
			t.Fatalf("not seeded: %v %v", m[settings.FeatureMCP].Value, m[settings.FeatureTokens].Value)
		}
		rows, err := store.ListLayered(ctx, st.Q(), store.LayeredSite, "")
		if err != nil || len(rows) == 0 {
			t.Fatal(len(rows), err)
		}
		for _, r := range rows {
			if r.SetBy != "profile:cn" || r.Lower != "" {
				t.Errorf("%s: set_by %q lower %q", r.Key, r.SetBy, r.Lower)
			}
		}
		// The same profile again is ignored; a different one is refused; no flag is fine.
		if seeded, err := s.SeedProfile(ctx, settings.ProfileCN); err != nil || seeded {
			t.Fatal(seeded, err)
		}
		if seeded, err := s.SeedProfile(ctx, ""); err != nil || seeded {
			t.Fatal(seeded, err)
		}
		if _, err := s.SeedProfile(ctx, settings.ProfileDefault); err == nil || !strings.Contains(err.Error(), "first start") {
			t.Fatal(err)
		}
		// An administrator can change a seeded value: it is a default, not a lock.
		if _, err := s.SetSite(ctx, settings.FeatureMCP, settings.Change{HasValue: true, Value: true}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestProfileIsRefusedOnADatabaseThatHasBeenUsed(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := settings.New(st)
		if _, err := s.SetSite(ctx, settings.SigninPerMinute, settings.Change{HasValue: true, Value: int64(77)}); err != nil {
			t.Fatal(err)
		}
		if seeded, err := s.SeedProfile(ctx, settings.ProfileCN); err == nil || seeded || !strings.Contains(err.Error(), "default") {
			t.Fatal(seeded, err)
		}
		if m, _, _ := s.Site(ctx); m[settings.FeatureMCP].Value != true {
			t.Fatal("a refused profile changed a value")
		}
		if seeded, err := s.SeedProfile(ctx, settings.ProfileDefault); err != nil || seeded {
			t.Fatal(seeded, err)
		}
		if _, err := s.SeedProfile(ctx, "mars"); err == nil {
			t.Fatal("an unknown profile was accepted")
		}
	})
}
