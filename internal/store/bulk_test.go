package store_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

// The daily pass deletes the bulk jobs that ended before the bound, by their
// index, and never one that is queued or running.
func TestPruneBulkJobs(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
		old, fresh := now.Add(-40*24*time.Hour), now.Add(-time.Hour)
		for i, j := range []struct {
			state string
			at    time.Time
		}{{"done", old}, {"failed", old}, {"interrupted", old}, {"queued", old}, {"running", old}, {"done", fresh}} {
			if err := store.InsertBulkJob(ctx, st.Q(), store.BulkJob{ID: fmt.Sprintf("J%d", i), TenantID: store.DefaultTenantID, MadeBy: "U", Action: "x", Total: 1, State: j.state, Created: j.at}); err != nil {
				t.Fatal(err)
			}
		}
		n, err := store.PruneBulkJobs(ctx, st.Q(), now.Add(-store.BulkJobKeep))
		if err != nil || n != 3 {
			t.Fatalf("pruned %d, %v", n, err)
		}
		if got := count(t, st, `SELECT COUNT(*) FROM bulk_jobs WHERE id IN ('J3', 'J4', 'J5')`); got != 3 {
			t.Fatalf("%d of the queued, running and recent jobs are left", got)
		}
		bulk(t, st, "bulk_jobs", "id, tenant_id, made_by, action, total, state, created, updated", 3000, 200, func(i int) []any {
			return []any{fmt.Sprintf("B%05d", i), store.DefaultTenantID, "U", "x", 1, "done", now.UnixMilli(), now.UnixMilli() - int64(i)*1000}
		})
		withDear(t, st, []string{"enable_seqscan"}, func(q store.Q) {
			plan := explain(t, st, q, `SELECT id FROM bulk_jobs WHERE updated < ? AND state NOT IN ('queued', 'running')`, now.UnixMilli()-2990*1000)
			if !strings.Contains(plan, "bulk_jobs_updated") {
				t.Errorf("the prune does not read bulk_jobs_updated:\n%s", plan)
			}
		})
	})
}
