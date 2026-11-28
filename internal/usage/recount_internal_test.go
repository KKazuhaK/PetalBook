package usage

import (
	"context"
	"testing"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// A tenant deleted between the list and the write does not fail the others'
// totals: it is skipped, and what was written is remembered.
func TestRecountWriteSkipsATenantThatIsGone(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		ctx := context.Background()
		r := &Recounter{day: "2026-03-05", last: map[string]map[string]int64{}}
		changes := map[string]map[string]int64{
			"T000099999":          {store.UsageKeyPeople: 1}, // no such tenant
			store.DefaultTenantID: {store.UsageKeyPeople: 7, store.UsageKeyBytes: 9},
		}
		n, err := r.write(ctx, st, "2026-03-05", changes)
		if err != nil || n != 2 {
			t.Fatalf("wrote %d: %v", n, err)
		}
		v, err := store.UsageDailyValue(ctx, st.Q(), store.DefaultTenantID, "2026-03-05", store.UsageKeyPeople)
		if err != nil || v != 7 {
			t.Fatalf("%d %v", v, err)
		}
		if r.last[store.DefaultTenantID][store.UsageKeyBytes] != 9 || r.last["T000099999"] != nil {
			t.Fatalf("remembered %v", r.last)
		}
	})
}
