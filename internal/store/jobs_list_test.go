package store_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// A page of jobs is the scope's: the site sees all (and may narrow to a tenant),
// a tenant its own and never the site's. The kinds and the state narrow it, the
// newest come first, and the cursor continues where a page ended.
func TestListJobsKeepsToTheScopeTheFilterAndTheCursor(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
		const other = "T000000002"
		if err := st.CreateTenant(ctx, store.Tenant{ID: other, Slug: "two", Name: "Two", Created: now, Updated: now}); err != nil {
			t.Fatal(err)
		}
		for i := range 5 {
			insertJob(t, st, store.Job{ID: fmt.Sprintf("E%d", i), Kind: store.JobExport, State: "done", Created: now.Add(time.Duration(i) * time.Second)})
		}
		insertJob(t, st, store.Job{ID: "B1", Kind: "bulk", State: "running", Created: now.Add(10 * time.Second)})
		insertJob(t, st, store.Job{ID: "S1", Kind: store.JobExport, State: "done", SiteOnly: true, Created: now.Add(11 * time.Second)})
		insertJob(t, st, store.Job{ID: "O1", TenantID: other, Kind: store.JobExport, State: "done", Created: now.Add(12 * time.Second)})

		ids := func(sc store.Scope, f store.JobFilter) (out []string, next string) {
			t.Helper()
			rows, next, err := store.ListJobs(ctx, st.Q(), sc, f)
			if err != nil {
				t.Fatal(err)
			}
			for _, j := range rows {
				out = append(out, j.ID)
			}
			return out, next
		}
		eq := func(got []string, want ...string) {
			t.Helper()
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
		all, _ := ids(store.AllTenants(), store.JobFilter{})
		eq(all, "O1", "S1", "B1", "E4", "E3", "E2", "E1", "E0")
		own, _ := ids(store.InTenant(store.DefaultTenantID), store.JobFilter{})
		eq(own, "B1", "E4", "E3", "E2", "E1", "E0") // not the site's, not another tenant's
		theirs, _ := ids(store.InTenant(other), store.JobFilter{})
		eq(theirs, "O1")
		exports, _ := ids(store.InTenant(store.DefaultTenantID), store.JobFilter{Kinds: []string{store.JobExport}})
		eq(exports, "E4", "E3", "E2", "E1", "E0")
		running, _ := ids(store.AllTenants(), store.JobFilter{State: "running"})
		eq(running, "B1")
		// Pages of two, continued by the cursor.
		var got []string
		after := ""
		for range 5 {
			page, next := ids(store.InTenant(store.DefaultTenantID), store.JobFilter{Kinds: []string{store.JobExport}, Limit: 2, After: after})
			got = append(got, page...)
			if next == "" {
				break
			}
			after = next
		}
		eq(got, "E4", "E3", "E2", "E1", "E0")
		if _, _, err := store.ListJobs(ctx, st.Q(), store.AllTenants(), store.JobFilter{After: "garbage"}); !errors.Is(err, store.ErrBadCursor) {
			t.Fatalf("a cursor that is not one: %v", err)
		}
	})
}

// An export is kept by its days, not by the prune of the month; a job that holds
// a recycle item is kept as long as it holds it.
func TestPruneJobsKeepsExportsAndTheJobsThatHoldAnItem(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
		old := now.Add(-40 * 24 * time.Hour)
		insertJob(t, st, store.Job{ID: "EXP", Kind: store.JobExport, State: "done", Created: old, Expires: now.Add(24 * time.Hour)})
		insertJob(t, st, store.Job{ID: "HELD", Kind: store.JobRecyclePurge, State: "failed", Created: old})
		insertJob(t, st, store.Job{ID: "FREE", Kind: store.JobRecyclePurge, State: "done", Created: old})
		if _, err := st.Q().Exec(ctx, `INSERT INTO recycle_items(id, tenant_id, kind, deleted_at, job_id) VALUES ('R1', ?, 'space', 0, 'HELD')`, store.DefaultTenantID); err != nil {
			t.Fatal(err)
		}
		if n, err := store.PruneJobs(ctx, st.Q(), now.Add(-store.JobKeep)); err != nil || n != 1 {
			t.Fatalf("pruned %d, %v", n, err)
		}
		if got := count(t, st, `SELECT COUNT(*) FROM jobs WHERE id IN ('EXP', 'HELD')`); got != 2 {
			t.Fatalf("%d of the export and the holder are left", got)
		}
		// The export is listed for expiry once its days are over.
		if l, err := store.ExpiredExports(ctx, st.Q(), now, 10); err != nil || len(l) != 0 {
			t.Fatalf("%v %v", l, err)
		}
		if l, err := store.ExpiredExports(ctx, st.Q(), now.Add(48*time.Hour), 10); err != nil || len(l) != 1 || l[0].ID != "EXP" {
			t.Fatalf("%v %v", l, err)
		}
	})
}

// An item is at rest (no job), held by one job at a time, and found again when
// the job that holds it has ended.
func TestARecycleItemIsHeldByOneJobAndStuckWhenItsJobEnded(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
		if _, err := st.Q().Exec(ctx, `INSERT INTO recycle_items(id, tenant_id, kind, deleted_at) VALUES ('R1', ?, 'space', 0)`, store.DefaultTenantID); err != nil {
			t.Fatal(err)
		}
		state := func() string {
			it, err := store.GetRecycleItem(ctx, st.Q(), store.AllTenants(), "R1", false)
			if err != nil {
				t.Fatal(err)
			}
			return it.State
		}
		if state() != store.RecycleReady {
			t.Fatal(state())
		}
		insertJob(t, st, store.Job{ID: "J1", Kind: store.JobRecyclePurge, Subject: "R1", State: "running", Created: now})
		if err := store.HoldRecycleItem(ctx, st.Q(), "R1", "J1"); err != nil {
			t.Fatal(err)
		}
		if state() != store.RecyclePurging {
			t.Fatal(state())
		}
		if err := store.HoldRecycleItem(ctx, st.Q(), "R1", "J2"); !errors.Is(err, store.ErrBusyItem) {
			t.Fatalf("a second holder: %v", err)
		}
		if err := store.HoldRecycleItem(ctx, st.Q(), "nope", "J2"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("no such item: %v", err)
		}
		// A job that runs holds it for good: it is not stuck.
		if l, err := store.RecycleStuck(ctx, st.Q()); err != nil || len(l) != 0 {
			t.Fatalf("%v %v", l, err)
		}
		if err := store.EndJob(ctx, st.Q(), store.Job{ID: "J1", State: store.JobFailed}, store.JobRunning, now); err != nil {
			t.Fatal(err)
		}
		if l, err := store.RecycleStuck(ctx, st.Q()); err != nil || len(l) != 1 || l[0].ID != "R1" || l[0].State != store.RecyclePurging {
			t.Fatalf("%v %v", l, err)
		}
		// Handed to a new job, released by the one that ends it.
		if err := store.HandRecycleItem(ctx, st.Q(), "R1", "J1", "J3"); err != nil {
			t.Fatal(err)
		}
		if err := store.ReleaseRecycleItem(ctx, st.Q(), "R1", "J1"); !errors.Is(err, store.ErrBusyItem) {
			t.Fatalf("released by a job that does not hold it: %v", err)
		}
		if err := store.ReleaseRecycleItem(ctx, st.Q(), "R1", "J3"); err != nil || state() != store.RecycleReady {
			t.Fatalf("%v %s", err, state())
		}
	})
}
