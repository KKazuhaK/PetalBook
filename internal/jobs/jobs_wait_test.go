package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// A cancel that reads a job as queued and finds it claimed when it writes does
// not answer 404: it looks again and stops the job it now finds running.
func TestCancelOfAJobClaimedWhileItIsBeingCancelled(t *testing.T) {
	t.Parallel()
	f := newFix(t, storetest.Open(t, "sqlite"), Bulk, nil)
	f.enqueue("Q", "")
	f.drain()
	f.r.beforeCancelWrite = func(id string) {
		f.r.beforeCancelWrite = nil
		f.r.dispatch() // claims and starts it between the read and the write
	}
	if err := f.r.Cancel(context.Background(), store.AllTenants(), "Q"); err != nil {
		t.Fatalf("cancel in the window: %v", err)
	}
	if j := waitEnded(t, f.k); j.ID != "Q" || j.State != store.JobCancelled {
		t.Fatalf("%+v", j)
	}
}

// EnqueueWait runs its record in the insert's transaction (a failure adds
// neither), and the channel gets the job's result: the handler's error and the
// value it left.
func TestEnqueueWaitGivesTheResultAndKeepsTheRecordInTheTransaction(t *testing.T) {
	t.Parallel()
	f := newFix(t, storetest.Open(t, "sqlite"), Bulk, nil)
	boom := errors.New("boom")
	if _, _, err := f.r.EnqueueWait(context.Background(), store.Job{ID: "X", TenantID: store.DefaultTenantID, Kind: "test"}, func(store.Q) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("%v", err)
	}
	if _, err := store.GetJob(context.Background(), f.st.Q(), store.AllTenants(), "X"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the job stayed although its record failed: %v", err)
	}
	f.k.step = func(ctx context.Context, j *store.Job, p *Progress) error {
		p.Return("the value")
		return boom
	}
	_, ch, err := f.r.EnqueueWait(context.Background(), store.Job{ID: "Y", TenantID: store.DefaultTenantID, Kind: "test"}, func(store.Q) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	f.drain()
	f.r.dispatch()
	close(f.k.gate("Y"))
	select {
	case res := <-ch:
		if !errors.Is(res.Err, boom) || res.Value != "the value" || res.Job.State != store.JobFailed {
			t.Fatalf("%+v", res)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no result")
	}
	// Close answers a waiter whose job did not end.
	_, ch, err = f.r.EnqueueWait(context.Background(), store.Job{ID: "Z", TenantID: store.DefaultTenantID, Kind: "test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.r.Close()
	if res := <-ch; !errors.Is(res.Err, ErrClosed) {
		t.Fatalf("%+v", res)
	}
}
