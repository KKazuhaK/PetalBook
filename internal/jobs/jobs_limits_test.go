package jobs

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// The limits are read at each dispatch: raising the per-tenant limit lets a
// second job of a tenant start, a rule for one tenant applies to it only, and
// lowering the server's limit holds jobs queued without stopping any.
func TestTheLimitsAreReadAtEachDispatch(t *testing.T) {
	t.Parallel()
	var perDefault, perTB, maxRun atomic.Int64
	perDefault.Store(1)
	perTB.Store(1)
	maxRun.Store(10)
	f := newFix(t, storetest.Open(t, "sqlite"), Bulk, func(o *Options) {
		o.MaxRunning = func() int { return int(maxRun.Load()) }
		o.PerTenant = func(tn string) int {
			if tn == "TB" {
				return int(perTB.Load())
			}
			return int(perDefault.Load())
		}
	})
	f.tenant("TB")
	for _, id := range []string{"A1", "A2"} {
		f.enqueue(id, "")
	}
	for _, id := range []string{"B1", "B2"} {
		f.enqueue(id, "TB")
	}
	f.drain()
	f.r.dispatch()
	if got := f.running(); got != "A1,B1" {
		t.Fatalf("one per tenant: %s", got)
	}
	perTB.Store(2)
	f.r.dispatch()
	if got := f.running(); got != "A1,B1,B2" {
		t.Fatalf("TB raised to 2: %s", got)
	}
	perDefault.Store(2)
	f.r.dispatch()
	if got := f.running(); got != "A1,A2,B1,B2" {
		t.Fatalf("all raised: %s", got)
	}
	// Lowering the server's limit stops nothing; the jobs that end are not replaced.
	maxRun.Store(1)
	f.enqueue("A3", "")
	f.drain()
	f.r.dispatch()
	if got := f.running(); got != "A1,A2,B1,B2" {
		t.Fatalf("lowered: %s", got)
	}
	for _, id := range []string{"A1", "A2", "B1"} {
		f.finish(id)
	}
	f.r.dispatch()
	if got := f.running(); got != "B2" {
		t.Fatalf("one running at a limit of 1, A3 held: %s", got)
	}
	f.finish("B2")
	f.r.dispatch()
	if got := f.running(); got != "A3" {
		t.Fatalf("A3 starts when there is room: %s", got)
	}
	f.finish("A3")
}

// jobs.perTenant applies to each class on its own: a tenant's long bulk action
// does not hold its export, while jobs.maxRunning stays the server's total of
// both classes.
func TestThePerTenantLimitIsPerClassAndTheServersIsTheTotal(t *testing.T) {
	t.Parallel()
	var maxRun atomic.Int64
	maxRun.Store(10)
	f := newFix(t, storetest.Open(t, "sqlite"), Bulk, func(o *Options) {
		o.MaxRunning = func() int { return int(maxRun.Load()) }
		o.PerTenant = func(string) int { return 1 }
	})
	io := newKind(IO)
	f.r.Register("io", io)
	enqueue := func(id, kind string) {
		t.Helper()
		f.clk.add(time.Millisecond)
		if _, err := f.r.Enqueue(context.Background(), store.Job{ID: id, TenantID: store.DefaultTenantID, Kind: kind, Total: 1}); err != nil {
			t.Fatal(err)
		}
	}
	enqueue("B1", "test")
	enqueue("B2", "test")
	enqueue("E1", "io")
	enqueue("E2", "io")
	f.drain()
	f.r.dispatch()
	if got := f.running(); got != "B1,E1" {
		t.Fatalf("one of each class for the tenant: %s", got)
	}
	// The server's total counts both classes.
	f.finish("B1")
	maxRun.Store(2)
	f.r.dispatch()
	if got := f.running(); got != "B2,E1" {
		t.Fatalf("%s", got)
	}
	close(io.gate("E1"))
	<-io.ended
	<-f.r.wakeup
	maxRun.Store(1)
	f.r.dispatch()
	if got := f.running(); got != "B2" {
		t.Fatalf("a total of 1 holds E2 while B2 runs: %s", got)
	}
	f.finish("B2")
	f.r.dispatch()
	if got := f.running(); got != "E2" {
		t.Fatalf("%s", got)
	}
	close(io.gate("E2"))
	<-io.ended
}

// A job that fails in the queue (its kind broke before it could start) wakes
// WaitIdle like a job that ran: it returns instead of waiting for an end that
// already happened.
func TestWaitIdleReturnsAfterAQueuedJobFails(t *testing.T) {
	t.Parallel()
	f := newFix(t, storetest.Open(t, "sqlite"), Bulk, nil)
	f.r.Register("class", &panicky{kind: f.k, at: "class"})
	if err := store.InsertJob(context.Background(), f.st.Q(), store.Job{ID: "class", TenantID: store.DefaultTenantID, Kind: "class", State: store.JobQueued, Created: f.clk.Now()}); err != nil {
		t.Fatal(err)
	}
	// What WaitIdle waits on once it has seen the queued job.
	f.r.mu.Lock()
	ch := f.r.idle
	f.r.mu.Unlock()
	f.r.dispatch()
	<-f.k.ended
	select {
	case <-ch:
	default:
		t.Fatal("the queued job failed and WaitIdle was not woken")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := f.r.WaitIdle(ctx); err != nil {
		t.Fatalf("WaitIdle: %v", err)
	}
}
