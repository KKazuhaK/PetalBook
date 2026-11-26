package store_test

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

func insertJob(t *testing.T, st *store.Store, j store.Job) {
	t.Helper()
	if j.TenantID == "" {
		j.TenantID = store.DefaultTenantID
	}
	if j.Kind == "" {
		j.Kind = "bulk"
	}
	if err := store.InsertJob(ctx, st.Q(), j); err != nil {
		t.Fatal(err)
	}
}

// The daily pass deletes the jobs that ended before the bound, by their index,
// and never one that is queued or running.
func TestPruneJobs(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
		old, fresh := now.Add(-40*24*time.Hour), now.Add(-time.Hour)
		for i, j := range []struct {
			state string
			at    time.Time
		}{{"done", old}, {"failed", old}, {"interrupted", old}, {"queued", old}, {"running", old}, {"done", fresh}, {"cancelled", old}} {
			insertJob(t, st, store.Job{ID: fmt.Sprintf("J%d", i), MadeBy: "U", Total: 1, State: j.state, Created: j.at})
		}
		n, err := store.PruneJobs(ctx, st.Q(), now.Add(-store.JobKeep))
		if err != nil || n != 4 {
			t.Fatalf("pruned %d, %v", n, err)
		}
		if got := count(t, st, `SELECT COUNT(*) FROM jobs WHERE id IN ('J3', 'J4', 'J5')`); got != 3 {
			t.Fatalf("%d of the queued, running and recent jobs are left", got)
		}
		bulk(t, st, "jobs", "id, tenant_id, kind, state, created, updated", 3000, 200, func(i int) []any {
			return []any{fmt.Sprintf("B%05d", i), store.DefaultTenantID, "bulk", "done", now.UnixMilli(), now.UnixMilli() - int64(i)*1000}
		})
		withDear(t, st, []string{"enable_seqscan"}, func(q store.Q) {
			plan := explain(t, st, q, `SELECT id FROM jobs WHERE updated < ? AND state NOT IN ('queued', 'running')`, now.UnixMilli()-2990*1000)
			if !strings.Contains(plan, "jobs_updated") {
				t.Errorf("the prune does not read jobs_updated:\n%s", plan)
			}
			// The runner's scan reads the partial index of the active jobs, not
			// the thousands that ended.
			plan = explain(t, st, q, `SELECT id FROM jobs WHERE state IN ('queued', 'running') ORDER BY state, created, id`)
			if !strings.Contains(plan, "jobs_active") {
				t.Errorf("the active scan does not read jobs_active:\n%s", plan)
			}
		})
		act, err := store.ActiveJobs(ctx, st.Q())
		if err != nil || len(act) != 2 || act[0].ID != "J4" && act[0].ID != "J3" {
			t.Fatalf("%v %v", act, err)
		}
	})
}

// Two enqueues with one lock key at once: exactly one wins, and the key is free
// again once that job has ended.
func TestOneActiveJobPerLockKey(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Now()
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i := range errs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = store.InsertJob(ctx, st.Q(), store.Job{ID: fmt.Sprintf("L%d", i), TenantID: store.DefaultTenantID, Kind: "assets.move", LockKey: "space:S1", Created: now})
			}()
		}
		wg.Wait()
		won := 0
		for _, err := range errs {
			switch {
			case err == nil:
				won++
			case !errors.Is(err, store.ErrJobLocked):
				t.Fatalf("a refused enqueue: %v", err)
			}
		}
		if won != 1 {
			t.Fatalf("%d of two enqueues with one key won", won)
		}
		winner := "L0"
		if errs[0] != nil {
			winner = "L1"
		}
		if err := store.SetJobState(ctx, st.Q(), winner, store.JobQueued, store.JobDone, now); err != nil {
			t.Fatal(err)
		}
		insertJob(t, st, store.Job{ID: "L2", Kind: "assets.move", LockKey: "space:S1", Created: now})
		// A job without a key never conflicts.
		insertJob(t, st, store.Job{ID: "L3", Created: now})
		insertJob(t, st, store.Job{ID: "L4", Created: now})
	})
}

// A claim is one statement that only one of two runners wins; progress and the
// end are written from the state the runner holds.
func TestAJobIsClaimedOnce(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Now()
		insertJob(t, st, store.Job{ID: "C1", Total: 3, Created: now})
		var wg sync.WaitGroup
		got := make([]bool, 2)
		for i := range got {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ok, err := store.ClaimJob(ctx, st.Q(), "C1", fmt.Sprintf("runner-%d", i), now)
				if err != nil {
					t.Error(err)
				}
				got[i] = ok
			}()
		}
		wg.Wait()
		if got[0] == got[1] {
			t.Fatalf("claims: %v", got)
		}
		if err := store.JobProgress(ctx, st.Q(), "C1", 2, 0, []store.JobFailure{{ID: "x", Error: "no"}}); err != nil {
			t.Fatal(err)
		}
		j, err := store.GetJob(ctx, st.Q(), store.AllTenants(), "C1")
		if err != nil || j.State != store.JobRunning || j.Done != 2 || len(j.Failed) != 1 || j.Attempt != 1 || !strings.HasPrefix(j.Runner, "runner-") {
			t.Fatalf("%+v %v", j, err)
		}
		end := *j
		end.State, end.Done = store.JobDone, 3
		if err := store.EndJob(ctx, st.Q(), end, store.JobRunning, now); err != nil {
			t.Fatal(err)
		}
		// Ended: neither progress nor a second end nor a claim moves it.
		if err := store.JobProgress(ctx, st.Q(), "C1", 1, 0, nil); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("progress on an ended job: %v", err)
		}
		if err := store.EndJob(ctx, st.Q(), end, store.JobRunning, now); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a second end: %v", err)
		}
		if ok, _ := store.ClaimJob(ctx, st.Q(), "C1", "r", now); ok {
			t.Fatal("an ended job was claimed")
		}
		// A tenant's scope sees its own jobs, not the site's.
		insertJob(t, st, store.Job{ID: "C2", SiteOnly: true, Created: now})
		if _, err := store.GetJob(ctx, st.Q(), store.InTenant(store.DefaultTenantID), "C2"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a tenant sees a site job: %v", err)
		}
		if _, err := store.GetJob(ctx, st.Q(), store.InTenant(store.DefaultTenantID), "C1"); err != nil {
			t.Fatal(err)
		}
	})
}
