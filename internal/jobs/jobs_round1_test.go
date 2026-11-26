package jobs

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// timer is one wait the runner asked for: how long, and the channel the test
// fires to end it.
type timer struct {
	d    time.Duration
	fire chan time.Time
}

// fakeAfter gives the runner timers the test fires by hand.
func fakeAfter() (func(time.Duration) <-chan time.Time, chan timer) {
	asked := make(chan timer, 16)
	return func(d time.Duration) <-chan time.Time {
		t := timer{d: d, fire: make(chan time.Time, 1)}
		asked <- t
		return t.fire
	}, asked
}

func nextTimer(t *testing.T, asked chan timer) timer {
	t.Helper()
	select {
	case tm := <-asked:
		return tm
	case <-time.After(10 * time.Second):
		t.Fatal("the runner asked for no retry")
	}
	return timer{}
}

func waitEnded(t *testing.T, k *kind) *store.Job {
	t.Helper()
	select {
	case j := <-k.ended:
		return j
	case <-time.After(10 * time.Second):
		t.Fatal("no job ended")
	}
	return nil
}

// faults fails the named store operations of the runner as many times as set.
type faults struct {
	mu   sync.Mutex
	left map[string]int
}

func (f *faults) set(op string, n int) {
	f.mu.Lock()
	f.left[op] = n
	f.mu.Unlock()
}

func (f *faults) fault(op string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.left[op] > 0 {
		f.left[op]--
		return errors.New("the database is busy: " + op)
	}
	return nil
}

// A scan or a claim that fails is tried again after a backoff (1s, doubling, at
// most 30s, back to 1s after a pass that worked): an accepted job does not wait
// for another job's event or a restart.
func TestAFailedScanOrClaimIsTriedAgainAfterABackoff(t *testing.T) {
	t.Parallel()
	for _, op := range []string{"scan", "claim"} {
		t.Run(op, func(t *testing.T) {
			t.Parallel()
			after, asked := fakeAfter()
			fs := &faults{left: map[string]int{}}
			f := newFix(t, storetest.Open(t, "sqlite"), Bulk, func(o *Options) { o.After, o.Fault = after, fs.fault })
			close(f.k.gate("J1"))
			close(f.k.gate("J2"))
			f.enqueue("J1", "")
			fs.set(op, 2)
			f.r.Start()
			// Two failures: 1s, then 2s.
			for _, want := range []time.Duration{time.Second, 2 * time.Second} {
				tm := nextTimer(t, asked)
				if tm.d != want {
					t.Fatalf("backoff %v; want %v", tm.d, want)
				}
				if n := f.k.runsOf("J1"); n != 0 {
					t.Fatalf("J1 ran %d times while the %s failed", n, op)
				}
				tm.fire <- f.clk.Now()
			}
			if j := waitEnded(t, f.k); j.ID != "J1" || j.State != store.JobDone {
				t.Fatalf("%+v", j)
			}
			// A pass that worked resets the backoff.
			fs.set(op, 1)
			f.enqueue("J2", "")
			if tm := nextTimer(t, asked); tm.d != time.Second {
				t.Fatalf("backoff after a pass that worked: %v; want 1s", tm.d)
			} else {
				tm.fire <- f.clk.Now()
			}
			if j := waitEnded(t, f.k); j.ID != "J2" || j.State != store.JobDone {
				t.Fatalf("%+v", j)
			}
		})
	}
	if d := backoffAfter(time.Minute); d != maxBackoff {
		t.Fatalf("the backoff grows past %v: %v", maxBackoff, d)
	}
}

// A cancel that comes between a job's claim and its start is not lost: the job
// starts with its context ended and ends cancelled. Cancel keeps to its scope:
// another tenant's cancel finds nothing, queued or running.
func TestCancelBetweenClaimAndStartAndCancelKeepsToItsScope(t *testing.T) {
	t.Parallel()
	f := newFix(t, storetest.Open(t, "sqlite"), Bulk, nil)
	f.tenant("TB")
	other := store.InTenant("TB")
	var errInWindow error
	f.r.afterClaim = func(id string) {
		if id == "W" {
			if err := f.r.Cancel(context.Background(), other, id); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("another tenant cancelled a claimed job: %v", err)
			}
			errInWindow = f.r.Cancel(context.Background(), store.InTenant(store.DefaultTenantID), id)
		}
	}
	f.enqueue("W", "")
	f.drain()
	f.r.dispatch()
	if errInWindow != nil {
		t.Fatalf("cancel in the window: %v", errInWindow)
	}
	if j := waitEnded(t, f.k); j.ID != "W" || j.State != store.JobCancelled {
		t.Fatalf("%+v", j)
	}
	<-f.r.wakeup
	f.r.afterClaim = nil
	// Another tenant's cancel does not reach a queued or a running job.
	f.enqueue("R", "")
	f.enqueue("Q", "")
	f.drain()
	f.r.dispatch()
	for _, id := range []string{"R", "Q"} {
		if err := f.r.Cancel(context.Background(), other, id); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("another tenant cancelling %s: %v", id, err)
		}
	}
	if f.state("Q").State != store.JobQueued || f.running() != "R" {
		t.Fatalf("Q is %s, running %s", f.state("Q").State, f.running())
	}
	f.finish("R")
}

// panicky is a kind whose named method panics.
type panicky struct {
	*kind
	at string
}

func (p *panicky) Class() Class {
	if p.at == "class" {
		panic("a bug in Class")
	}
	return p.kind.Class()
}

func (p *panicky) Queues(j *store.Job) []string {
	if p.at == "queues" {
		panic("a bug in Queues")
	}
	return nil
}

func (p *panicky) Recover(ctx context.Context, j *store.Job) Outcome {
	if p.at == "recover" {
		panic("a bug in Recover")
	}
	return Resume
}

func (p *panicky) Ended(ctx context.Context, j *store.Job) {
	if p.at == "ended" {
		p.kind.ended <- j
		panic("a bug in Ended")
	}
	p.kind.Ended(ctx, j)
}

// A panic in a kind's Class, Queues, Recover or Ended does not stop the runner:
// the job ends failed (Ended: it had ended already) and the others run.
func TestAPanickingKindDoesNotStopTheRunner(t *testing.T) {
	t.Parallel()
	f := newFix(t, storetest.Open(t, "sqlite"), Bulk, nil)
	now := f.clk.Now()
	for _, at := range []string{"class", "queues", "recover", "ended"} {
		f.r.Register(at, &panicky{kind: f.k, at: at})
		state := store.JobQueued
		if at == "recover" {
			state = store.JobRunning
		}
		if err := store.InsertJob(context.Background(), f.st.Q(), store.Job{ID: at, TenantID: store.DefaultTenantID, Kind: at, State: state, Created: now}); err != nil {
			t.Fatal(err)
		}
	}
	close(f.k.gate("ended"))
	f.r.recover()
	if j := f.state("recover"); j.State != store.JobFailed {
		t.Fatalf("a job whose Recover panicked is %s", j.State)
	}
	f.r.dispatch()
	for _, id := range []string{"class", "queues"} {
		if j := f.state(id); j.State != store.JobFailed || j.Error == "" {
			t.Fatalf("a job whose %s panicked: %+v", id, j)
		}
	}
	// Each job's end is told to its kind, the two that broke before they could
	// start included (their end is audited like any other).
	got := map[string]string{}
	for range 3 {
		j := waitEnded(t, f.k)
		got[j.ID] = j.State
	}
	if got["class"] != store.JobFailed || got["queues"] != store.JobFailed || got["ended"] != store.JobDone {
		t.Fatalf("ended: %v", got)
	}
	<-f.r.wakeup
	// The runner goes on.
	f.enqueue("after", "")
	f.r.dispatch()
	f.finish("after")
	if j := f.state("after"); j.State != store.JobDone {
		t.Fatalf("%+v", j)
	}
}

// Ended runs with a context of its own: a job that finishes as Close begins
// still has its end recorded (the audit row of a bulk job's end).
func TestEndedHasALiveContextWhenAJobFinishesAsCloseBegins(t *testing.T) {
	t.Parallel()
	f := newFix(t, storetest.Open(t, "sqlite"), Bulk, nil)
	running := make(chan struct{})
	var endErr atomic.Value
	k := &endCtxKind{kind: f.k, run: func(ctx context.Context, p *Progress) error {
		close(running)
		<-ctx.Done() // Close has begun; the last item finishes anyway
		p.Step(1, 0)
		return nil
	}, seen: &endErr}
	f.r.Register("finishing", k)
	f.r.Start()
	if _, err := f.r.Enqueue(context.Background(), store.Job{ID: "F", TenantID: store.DefaultTenantID, Kind: "finishing", Total: 1}); err != nil {
		t.Fatal(err)
	}
	<-running
	f.r.Close()
	if v := endErr.Load(); v == nil {
		t.Fatal("Ended was not called")
	} else if e, _ := v.(errBox); e.err != nil {
		t.Fatalf("Ended ran with an ended context: %v", e.err)
	}
	if j := f.state("F"); j.State != store.JobDone || j.Done != 1 {
		t.Fatalf("%+v", j)
	}
}

type errBox struct{ err error }

type endCtxKind struct {
	*kind
	run  func(ctx context.Context, p *Progress) error
	seen *atomic.Value
}

func (e *endCtxKind) Run(ctx context.Context, _ *store.Job, p *Progress) error { return e.run(ctx, p) }

func (e *endCtxKind) Ended(ctx context.Context, j *store.Job) { e.seen.Store(errBox{ctx.Err()}) }

// A job whose lock key an active job holds is refused with store.ErrJobLocked.
func TestEnqueueRefusesAHeldLockKey(t *testing.T) {
	t.Parallel()
	f := newFix(t, storetest.Open(t, "sqlite"), Bulk, nil)
	if _, err := f.r.Enqueue(context.Background(), store.Job{ID: "A", TenantID: store.DefaultTenantID, Kind: "test", LockKey: "space:S"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.r.Enqueue(context.Background(), store.Job{ID: "B", TenantID: store.DefaultTenantID, Kind: "test", LockKey: "space:S"}); !errors.Is(err, store.ErrJobLocked) {
		t.Fatalf("%v", err)
	}
	f.r.mu.Lock()
	defer f.r.mu.Unlock()
	if f.r.mine["B"] {
		t.Fatal("a refused job is still counted as enqueued")
	}
}
