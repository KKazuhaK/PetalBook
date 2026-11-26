package spaces

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"petalbook.app/server/internal/jobs"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

func (f *rbFix) idle() {
	f.t.Helper()
	ictx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := f.m.runner().WaitIdle(ictx); err != nil {
		f.t.Fatal(err)
	}
}

// An export is a job: its progress is the spaces made into the zip and the bytes
// written, its result the file; it is kept for its days, and the daily pass then
// deletes its file and its row.
func TestAnExportIsAJobWithProgressAResultAndItsDays(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newRbFix(t, st)
		info := f.fullSpace("olive", "edith", "mia")
		j, err := f.m.StartExport(ctx, ExportRequest{TenantID: store.DefaultTenantID, By: "olive", ByName: "olive", Sources: []store.ExportSource{{Space: info.ID, SpaceID: info.ID, Name: "one"}}})
		if err != nil {
			t.Fatal(err)
		}
		if j.Kind != store.JobExport || j.State != store.JobQueued || j.Total != 1 || j.Expires.Sub(j.Created) != 7*24*time.Hour {
			t.Fatalf("%+v", j)
		}
		f.idle()
		got, err := store.GetJob(ctx, st.Q(), store.AllTenants(), j.ID)
		if err != nil || got.State != store.JobDone || got.Done != 1 || got.BytesDone <= 0 {
			t.Fatalf("%+v %v", got, err)
		}
		file := f.m.ExportPath(got)
		fi, err := os.Stat(file)
		if err != nil || fi.Size() != got.BytesDone {
			t.Fatalf("the file: %v %v (bytes done %d)", fi, err, got.BytesDone)
		}
		if used, err := store.ExportBytes(ctx, st.Q()); err != nil || used != got.BytesDone {
			t.Fatalf("counted against the room: %d %v", used, err)
		}
		f.advance(7*24*time.Hour + time.Minute)
		if err := f.m.ExpireExports(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(file); !os.IsNotExist(err) {
			t.Fatalf("the file stayed: %v", err)
		}
		if _, err := store.GetJob(ctx, st.Q(), store.AllTenants(), j.ID); err == nil {
			t.Fatal("the row stayed")
		}
	})
}

// A crash between the rename and the end's write leaves a whole <id>.zip behind
// a job that is not done: a restart removes it with the half one, and the
// expiry or deletion of an export removes both whatever the job's state.
func TestAnOrphanedExportZipIsRemovedOnRecoverExpiryAndDelete(t *testing.T) {
	t.Parallel()
	f := newRbFix(t, storetest.Open(t, "sqlite"))
	if err := os.MkdirAll(f.m.exportsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	files := func(id string) []string {
		return []string{filepath.Join(f.m.exportsDir(), id+".zip"), filepath.Join(f.m.exportsDir(), id+".zip.tmp")}
	}
	plant := func(id string) {
		t.Helper()
		for _, p := range files(id) {
			if err := os.WriteFile(p, []byte("zip"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	gone := func(id, when string) {
		t.Helper()
		for _, p := range files(id) {
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Fatalf("%s: %s stayed (%v)", when, filepath.Base(p), err)
			}
		}
	}
	plant("EXR")
	if out := (exportKind{f.m}).Recover(ctx, &store.Job{ID: "EXR", Kind: store.JobExport, State: store.JobRunning}); out != jobs.Fail {
		t.Fatalf("%s", out)
	}
	gone("EXR", "recover")

	// A job whose end could not be written as done (failed, no result) still
	// has its zip removed when it expires or is deleted.
	now := f.m.opt.Now()
	for _, id := range []string{"EXE", "EXD"} {
		if err := store.InsertJob(ctx, f.st.Q(), store.Job{ID: id, TenantID: store.DefaultTenantID, Kind: store.JobExport, State: store.JobFailed, Created: now, Expires: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		plant(id)
	}
	f.m.DeleteExportFile(&store.Job{ID: "EXD", Kind: store.JobExport, State: store.JobFailed})
	gone("EXD", "delete")
	f.advance(2 * time.Hour)
	if err := f.m.ExpireExports(ctx); err != nil {
		t.Fatal(err)
	}
	gone("EXE", "expiry")
}

// A restart does not take an export up again: it ends failed and what it wrote
// is removed.
func TestAnExportACrashCutShortEndsFailedAndLeavesNoFile(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newRbFix(t, st)
		if err := os.MkdirAll(f.m.exportsDir(), 0o755); err != nil {
			t.Fatal(err)
		}
		tmp := filepath.Join(f.m.exportsDir(), "EXP1.zip.tmp")
		if err := os.WriteFile(tmp, []byte("half"), 0o600); err != nil {
			t.Fatal(err)
		}
		if out := (exportKind{f.m}).Recover(ctx, &store.Job{ID: "EXP1", Kind: store.JobExport}); out != jobs.Fail {
			t.Fatalf("%s", out)
		}
		if _, err := os.Stat(tmp); !os.IsNotExist(err) {
			t.Fatalf("the half-written file stayed: %v", err)
		}
	})
}

// A purge asked for is a job: the item is purging at once (the job holds it), the
// result says what it was after it is gone, and the server is told of its end.
func TestAPurgeIsAJobThatHoldsTheItemAndTellsItsEnd(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newRbFix(t, st)
		info := f.fullSpace("olive", "edith", "mia")
		item, err := f.m.RecycleSpace(ctx, store.AllTenants(), info.ID, RecycleOptions{By: "olive", Reason: "test"})
		if err != nil {
			t.Fatal(err)
		}
		ended := make(chan *store.Job, 4)
		f.m.SetRecycleEnded(func(_ context.Context, j *store.Job) { ended <- j })
		held, release := make(chan struct{}), make(chan struct{})
		f.mu.Lock()
		f.pause = func(step string) {
			if step == "purge:marked" {
				close(held)
				<-release
			}
		}
		f.mu.Unlock()
		it, done, err := f.m.StartPurge(ctx, store.AllTenants(), item.ID, PurgeBy{ID: "olive", Name: "olive", Capacity: "site_admin", IP: "10.0.0.1"})
		if err != nil {
			t.Fatal(err)
		}
		<-held
		if it.State != store.RecyclePurging || f.item(item.ID).State != store.RecyclePurging || f.item(item.ID).JobID != it.JobID {
			t.Fatalf("%+v", f.item(item.ID))
		}
		// A second action on the item is refused while the job holds it.
		if _, err := f.m.RestoreSpace(ctx, store.AllTenants(), item.ID, ""); err != ErrBusyItem {
			t.Fatalf("restoring an item being purged: %v", err)
		}
		if _, _, err := f.m.StartPurge(ctx, store.AllTenants(), item.ID, PurgeBy{}); err != ErrBusyItem {
			t.Fatalf("a second purge: %v", err)
		}
		close(release)
		if r := <-done; r.Err != nil || r.Job.State != store.JobDone {
			t.Fatalf("%+v", r)
		}
		f.noItem(item.ID)
		j := <-ended
		res := PurgeResultOf(j)
		if j.Kind != store.JobRecyclePurge || j.State != store.JobDone || j.MadeBy != "olive" || j.Capacity != "site_admin" || RecycleParamsOf(j).IP != "10.0.0.1" ||
			res.Name != info.Name || res.SpaceID != info.ID || res.Reason != "test" || res.Kind != store.RecycleSpace {
			t.Fatalf("%+v %+v", j, res)
		}
	})
}

// A restore that was running when the server stopped is undone by its kind's
// Recover; a move and a purge are taken up again.
func TestARecycleJobsRestartOutcomeFollowsItsKind(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newRbFix(t, st)
		for kind, want := range map[string]jobs.Outcome{store.JobRecycleMove: jobs.Resume, store.JobRecyclePurge: jobs.Resume, store.JobRecycleRestore: jobs.Fail} {
			if got := (recycleKind{f.m, kind}).Recover(ctx, &store.Job{ID: "J", Subject: "gone", Kind: kind}); got != want {
				t.Errorf("%s: %s, want %s", kind, got, want)
			}
		}
	})
}
