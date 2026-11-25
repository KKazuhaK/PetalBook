package settings_test

import (
	"testing"

	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// A stored rule is read into a value of its own: reading it never writes
// through the default rule's pointers, which every request shares (here the
// --max-upload ceiling itself).
func TestReadingAStoredRuleLeavesTheDefaultRuleAlone(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		s.SetFileCeiling(1000)
		half := int64(500)
		if _, err := s.SetSite(ctx, settings.FilesMaxBytes, settings.Change{HasLower: true, Lower: &settings.Lower{Mode: settings.Restrict, Max: &half}}); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			s.Invalidate()
			e, err := s.Get(ctx, ten, settings.FilesMaxBytes)
			if err != nil {
				t.Fatal(err)
			}
			if e.Value != int64(1000) {
				t.Fatalf("the ceiling became %v", e.Value)
			}
		}
	})
}
