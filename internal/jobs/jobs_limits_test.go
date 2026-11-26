package jobs

import (
	"sync/atomic"
	"testing"

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
