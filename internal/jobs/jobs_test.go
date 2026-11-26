package jobs

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// These tests drive the runner without fixed sleeps: the dispatcher is run by
// hand (dispatch is synchronous, and a job's end is seen on the wake-up channel
// when no loop reads it), or, where the loop runs, the tests wait on channels
// the handlers close.

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// kind is a test handler: each job blocks until its release channel is closed
// (or its context ends), then runs step if set.
type kind struct {
	class   Class
	outcome Outcome
	queues  func(j *store.Job) []string

	mu      sync.Mutex
	release map[string]chan struct{}
	runs    map[string]int
	step    func(ctx context.Context, j *store.Job, p *Progress) error
	ended   chan *store.Job
}

func newKind(cl Class) *kind {
	return &kind{class: cl, outcome: Resume, release: map[string]chan struct{}{}, runs: map[string]int{}, ended: make(chan *store.Job, 16)}
}

func (k *kind) gate(id string) chan struct{} {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.release[id] == nil {
		k.release[id] = make(chan struct{})
	}
	return k.release[id]
}

func (k *kind) Class() Class { return k.class }

func (k *kind) Run(ctx context.Context, j *store.Job, p *Progress) error {
	k.mu.Lock()
	k.runs[j.ID]++
	step := k.step
	k.mu.Unlock()
	select {
	case <-k.gate(j.ID):
	case <-ctx.Done():
		return ctx.Err()
	}
	if step != nil {
		return step(ctx, j, p)
	}
	p.Step(1, 0)
	return nil
}

func (k *kind) Recover(context.Context, *store.Job) Outcome { return k.outcome }

func (k *kind) Queues(j *store.Job) []string {
	if k.queues != nil {
		return k.queues(j)
	}
	return nil
}

func (k *kind) Ended(_ context.Context, j *store.Job) { k.ended <- j }

func (k *kind) runsOf(id string) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.runs[id]
}

type fix struct {
	t   *testing.T
	st  *store.Store
	clk *clock
	r   *Runner
	k   *kind
}

func newFix(t *testing.T, st *store.Store, cl Class, opt func(*Options)) *fix {
	clk := &clock{t: time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)}
	o := Options{Store: st, Now: clk.Now, ID: "test", Sleep: func(time.Duration) {}}
	if opt != nil {
		opt(&o)
	}
	f := &fix{t: t, st: st, clk: clk, r: New(o), k: newKind(cl)}
	f.r.Register("test", f.k)
	t.Cleanup(f.r.Close)
	return f
}

func (f *fix) tenant(id string) {
	f.t.Helper()
	now := time.Now()
	if err := f.st.CreateTenant(context.Background(), store.Tenant{ID: id, Slug: strings.ToLower(id), Name: id, Created: now, Updated: now}); err != nil {
		f.t.Fatal(err)
	}
}

// enqueue adds a job of the test kind; the clock moves on a millisecond so that
// the jobs are ordered as they were enqueued.
func (f *fix) enqueue(id, tenant string) {
	f.t.Helper()
	if tenant == "" {
		tenant = store.DefaultTenantID
	}
	f.clk.add(time.Millisecond)
	if _, err := f.r.Enqueue(context.Background(), store.Job{ID: id, TenantID: tenant, Kind: "test", Total: 1}); err != nil {
		f.t.Fatal(err)
	}
}

// running is the ids of the jobs this runner runs now.
func (f *fix) running() string {
	f.r.mu.Lock()
	defer f.r.mu.Unlock()
	var ids []string
	for id := range f.r.live {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

// finish lets a job end and waits until it has released its slots (it wakes
// the runner last; no loop reads the wake-up here).
func (f *fix) finish(id string) {
	f.t.Helper()
	close(f.k.gate(id))
	select {
	case <-f.k.ended:
	case <-time.After(10 * time.Second):
		f.t.Fatalf("%s did not end", id)
	}
	select {
	case <-f.r.wakeup:
	case <-time.After(10 * time.Second):
		f.t.Fatalf("%s did not release its slots", id)
	}
}

func (f *fix) state(id string) *store.Job {
	f.t.Helper()
	j, err := store.GetJob(context.Background(), f.st.Q(), store.AllTenants(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return j
}

// drain empties the wake-up channel (enqueues fill it).
func (f *fix) drain() {
	select {
	case <-f.r.wakeup:
	default:
	}
}

// A class runs one job per tenant and two on the server; the others wait in the
// queue, oldest first, and start as slots free.
func TestTheRunnerKeepsToTheSlotsOfAClass(t *testing.T) {
	t.Parallel()
	f := newFix(t, storetest.Open(t, "sqlite"), Bulk, nil)
	f.tenant("TB")
	f.tenant("TC")
	f.enqueue("J1", store.DefaultTenantID)
	f.enqueue("J2", "TB")
	f.enqueue("J3", "TC")
	f.enqueue("J4", store.DefaultTenantID)
	f.drain()
	f.r.dispatch()
	if got := f.running(); got != "J1,J2" {
		t.Fatalf("running %s; want J1,J2 (two on the server)", got)
	}
	f.finish("J2")
	f.r.dispatch()
	// J3 (tenant C) takes the free place; J4 waits for tenant A's turn.
	if got := f.running(); got != "J1,J3" {
		t.Fatalf("running %s; want J1,J3", got)
	}
	f.finish("J1")
	f.r.dispatch()
	if got := f.running(); got != "J3,J4" {
		t.Fatalf("running %s; want J3,J4", got)
	}
	f.finish("J3")
	f.finish("J4")
	for _, id := range []string{"J1", "J2", "J3", "J4"} {
		if j := f.state(id); j.State != store.JobDone || j.Done != 1 || j.Attempt != 1 {
			t.Fatalf("%s: %+v", id, j)
		}
	}
}

// A job that works on several tenants takes the turn of each of them.
func TestAJobOfSeveralTenantsTakesEachTenantsTurn(t *testing.T) {
	t.Parallel()
	f := newFix(t, storetest.Open(t, "sqlite"), Bulk, nil)
	f.tenant("TB")
	f.k.queues = func(j *store.Job) []string {
		if j.ID == "M" {
			return []string{store.DefaultTenantID, "TB"}
		}
		return nil
	}
	f.enqueue("B1", "TB")
	f.enqueue("M", store.DefaultTenantID)
	f.drain()
	f.r.dispatch()
	if got := f.running(); got != "B1" {
		t.Fatalf("running %s; the job of both tenants started while tenant B's turn was taken", got)
	}
	f.finish("B1")
	f.r.dispatch()
	if got := f.running(); got != "M" {
		t.Fatalf("running %s; want M", got)
	}
	f.finish("M")
}

// Two runners on one database (a standby server, two processes on PostgreSQL)
// claim a job once: one runs it, the other passes it by.
func TestAJobIsRunOnceByTwoRunners(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newFix(t, st, Bulk, nil)
		other := New(Options{Store: st, Now: f.clk.Now, ID: "other", Sleep: func(time.Duration) {}})
		other.Register("test", f.k)
		t.Cleanup(other.Close)
		for i := 0; i < 5; i++ {
			f.enqueue(fmt.Sprintf("J%d", i), "")
			f.drain()
			var wg sync.WaitGroup
			for _, r := range []*Runner{f.r, other} {
				wg.Add(1)
				go func() { defer wg.Done(); r.dispatch() }()
			}
			wg.Wait()
			id := fmt.Sprintf("J%d", i)
			close(f.k.gate(id))
			<-f.k.ended
			if n := f.k.runsOf(id); n != 1 {
				t.Fatalf("%s ran %d times", id, n)
			}
			if j := f.state(id); j.State != store.JobDone || j.Attempt != 1 {
				t.Fatalf("%+v", j)
			}
		}
	})
}

// At start every queued or running row is given to its kind's Recover, and each
// outcome is written: resumed (queued again, and run), interrupted, failed or
// done. A kind this server does not know is left alone.
func TestARestartGivesEachActiveJobItsKindsOutcome(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
		kinds := map[string]Outcome{"resume": Resume, "interrupt": Interrupt, "fail": Fail, "done": Done}
		for name := range kinds {
			for _, state := range []string{store.JobQueued, store.JobRunning} {
				if err := store.InsertJob(context.Background(), st.Q(), store.Job{ID: name + "-" + state, TenantID: store.DefaultTenantID, Kind: name, State: state, Done: 2, Total: 5, Created: now}); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := store.InsertJob(context.Background(), st.Q(), store.Job{ID: "unknown", TenantID: store.DefaultTenantID, Kind: "later", State: store.JobRunning, Created: now}); err != nil {
			t.Fatal(err)
		}
		r := New(Options{Store: st, Now: func() time.Time { return now }, Sleep: func(time.Duration) {}})
		t.Cleanup(r.Close)
		ks := map[string]*kind{}
		for name, out := range kinds {
			k := newKind(Bulk)
			k.outcome = out
			ks[name] = k
			r.Register(name, k)
		}
		r.recover()
		want := map[string]string{"resume": store.JobQueued, "interrupt": store.JobInterrupted, "fail": store.JobFailed, "done": store.JobDone}
		for name, w := range want {
			for _, state := range []string{store.JobQueued, store.JobRunning} {
				j, err := store.GetJob(context.Background(), st.Q(), store.AllTenants(), name+"-"+state)
				if err != nil || j.State != w || j.Done != 2 {
					t.Fatalf("%s-%s: %+v %v; want %s with what was done kept", name, state, j, err, w)
				}
			}
		}
		if j, _ := store.GetJob(context.Background(), st.Q(), store.AllTenants(), "unknown"); j.State != store.JobRunning {
			t.Fatalf("a kind this server does not run was touched: %+v", j)
		}
		// The resumed ones run again (one at a time: one tenant).
		r.dispatch()
		for _, id := range []string{"resume-queued", "resume-running"} {
			close(ks["resume"].gate(id))
		}
		for range 2 {
			j := <-ks["resume"].ended
			<-r.wakeup
			r.dispatch()
			if j.State != store.JobDone || j.Attempt != 1 {
				t.Fatalf("%+v", j)
			}
		}
	})
}

// Progress is written at most once a second or every 100 items, the first step
// at once; while the job runs, Get reads it from memory.
func TestProgressWritesAreThrottled(t *testing.T) {
	t.Parallel()
	var writes atomic.Int32
	f := newFix(t, storetest.Open(t, "sqlite"), Bulk, func(o *Options) {
		o.WriteFault = func(j *store.Job, state string, try int) error {
			if state == store.JobRunning {
				writes.Add(1)
			}
			return nil
		}
	})
	mid := make(chan struct{})
	goOn := make(chan struct{})
	f.k.step = func(ctx context.Context, j *store.Job, p *Progress) error {
		p.Step(1, 10) // written at once
		for range 99 {
			p.Step(1, 10) // within the second, under 100 items: not written
		}
		if n := writes.Load(); n != 1 {
			t.Errorf("%d writes after 100 steps in one instant; want 1", n)
		}
		p.Step(1, 10) // the 100th since the last write
		if n := writes.Load(); n != 2 {
			t.Errorf("%d writes after the 100th unwritten step; want 2", n)
		}
		p.Step(1, 10)
		f.clk.add(time.Second)
		p.Step(1, 10) // a second later
		if n := writes.Load(); n != 3 {
			t.Errorf("%d writes a second later; want 3", n)
		}
		p.Step(1, 10)
		close(mid)
		<-goOn
		return nil
	}
	f.enqueue("P", "")
	f.r.dispatch()
	close(f.k.gate("P"))
	<-mid
	j, err := f.r.Get(context.Background(), store.InTenant(store.DefaultTenantID), "P")
	if err != nil || j.Done != 104 || j.BytesDone != 1040 || j.State != store.JobRunning {
		t.Fatalf("from memory: %+v %v", j, err)
	}
	if row := f.state("P"); row.Done != 103 {
		t.Fatalf("the row says %d done; the last write was at 103", row.Done)
	}
	// The scope applies to what memory answers too.
	if _, err := f.r.Get(context.Background(), store.InTenant("other"), "P"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another tenant reads the job: %v", err)
	}
	close(goOn)
	<-f.k.ended
	if row := f.state("P"); row.State != store.JobDone || row.Done != 104 {
		t.Fatalf("%+v", row)
	}
}

// The end of a job is written though one try of it fails; a job whose progress
// could never be written ends failed, not done; a panic ends it failed with the
// item it was on.
func TestTheEndIsWrittenAndABrokenJobEndsFailed(t *testing.T) {
	t.Parallel()
	var progressDown atomic.Bool
	f := newFix(t, storetest.Open(t, "sqlite"), Bulk, func(o *Options) {
		o.WriteFault = func(j *store.Job, state string, try int) error {
			if state == store.JobDone && try == 0 {
				return errors.New("the database is busy")
			}
			if state == store.JobRunning && progressDown.Load() {
				return errors.New("the database is gone")
			}
			return nil
		}
	})
	f.enqueue("E1", "")
	f.r.dispatch()
	f.finish("E1")
	if j := f.state("E1"); j.State != store.JobDone {
		t.Fatalf("%+v", j)
	}
	progressDown.Store(true)
	f.enqueue("E2", "")
	f.r.dispatch()
	f.finish("E2")
	if j := f.state("E2"); j.State != store.JobFailed || j.Done != 1 {
		t.Fatalf("%+v", j)
	}
	progressDown.Store(false)
	f.k.step = func(ctx context.Context, j *store.Job, p *Progress) error {
		p.Step(1, 0)
		p.Item("second")
		panic("a bug")
	}
	f.enqueue("E3", "")
	f.r.dispatch()
	f.finish("E3")
	j := f.state("E3")
	if j.State != store.JobFailed || j.Done != 1 || len(j.Failed) != 1 || j.Failed[0].ID != "second" || j.Failed[0].Code != "internal" {
		t.Fatalf("%+v", j)
	}
	// A handler's own error fails the job with it.
	f.k.step = func(context.Context, *store.Job, *Progress) error { return errors.New("no room") }
	f.enqueue("E4", "")
	f.r.dispatch()
	f.finish("E4")
	if j := f.state("E4"); j.State != store.JobFailed || j.Error != "no room" {
		t.Fatalf("%+v", j)
	}
}

// Cancel ends a queued job at once (it never runs) and a running one through its
// context.
func TestCancel(t *testing.T) {
	t.Parallel()
	f := newFix(t, storetest.Open(t, "sqlite"), Bulk, nil)
	f.enqueue("R", "")
	f.enqueue("Q", "")
	f.drain()
	f.r.dispatch()
	if err := f.r.Cancel(context.Background(), store.AllTenants(), "Q"); err != nil {
		t.Fatal(err)
	}
	if err := f.r.Cancel(context.Background(), store.AllTenants(), "R"); err != nil {
		t.Fatal(err)
	}
	if j := <-f.k.ended; j.ID != "R" || j.State != store.JobCancelled {
		t.Fatalf("%+v", j)
	}
	<-f.r.wakeup
	f.r.dispatch()
	if f.k.runsOf("Q") != 0 || f.state("Q").State != store.JobCancelled || f.state("R").State != store.JobCancelled {
		t.Fatalf("Q ran %d times; Q %s, R %s", f.k.runsOf("Q"), f.state("Q").State, f.state("R").State)
	}
	if err := f.r.Cancel(context.Background(), store.AllTenants(), "R"); !errors.Is(err, ErrEnded) {
		t.Fatalf("cancelling an ended job: %v", err)
	}
	if err := f.r.Cancel(context.Background(), store.AllTenants(), "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cancelling no job: %v", err)
	}
}

// A kind that keeps something for its queued jobs is told when one ends in
// the queue without running: cancelled there, or broken before it could start.
func TestAJobThatEndsInTheQueueIsToldToItsKind(t *testing.T) {
	t.Parallel()
	f := newFix(t, storetest.Open(t, "sqlite"), Bulk, nil)
	k := &queueEndKind{kind: f.k, got: make(chan *store.Job, 4)}
	f.r.Register("test", k)
	f.enqueue("Q", "")
	if err := f.r.Cancel(context.Background(), store.AllTenants(), "Q"); err != nil {
		t.Fatal(err)
	}
	select {
	case j := <-k.got:
		if j.ID != "Q" || j.State != store.JobCancelled {
			t.Fatalf("%+v", j)
		}
	default:
		t.Fatal("the kind was not told")
	}
	k.broken = true
	f.enqueue("B", "")
	f.drain()
	f.r.dispatch()
	select {
	case j := <-k.got:
		if j.ID != "B" || j.State != store.JobFailed {
			t.Fatalf("%+v", j)
		}
	default:
		t.Fatal("the kind was not told of a job that broke in the queue")
	}
}

type queueEndKind struct {
	*kind
	broken bool
	got    chan *store.Job
}

func (k *queueEndKind) Class() Class {
	if k.broken {
		panic("broken")
	}
	return k.kind.Class()
}

func (k *queueEndKind) EndedInQueue(_ context.Context, j *store.Job) { k.got <- j }

// Close stops the running jobs through their contexts and returns once they
// have ended (interrupted, with what was done); the jobs it had queued get
// their kind's outcome, and nothing is enqueued after it.
func TestCloseStopsTheJobsAndWaitsForThem(t *testing.T) {
	t.Parallel()
	f := newFix(t, storetest.Open(t, "sqlite"), Bulk, nil)
	f.k.outcome = Interrupt
	started := make(chan struct{})
	var ended atomic.Bool
	f.k.step = nil
	f.r.Register("slow", &stepKind{kind: f.k, run: func(ctx context.Context, p *Progress) error {
		p.Step(1, 0)
		close(started)
		<-ctx.Done()
		ended.Store(true)
		return ctx.Err()
	}})
	f.r.Start()
	f.clk.add(time.Millisecond)
	if _, err := f.r.Enqueue(context.Background(), store.Job{ID: "S", TenantID: store.DefaultTenantID, Kind: "slow", Total: 3}); err != nil {
		t.Fatal(err)
	}
	<-started
	// Same tenant: queued behind S.
	f.enqueue("W", "")
	f.r.Close()
	if !ended.Load() {
		t.Fatal("Close returned while the job ran")
	}
	if j := f.state("S"); j.State != store.JobInterrupted || j.Done != 1 {
		t.Fatalf("%+v", j)
	}
	if j := f.state("W"); j.State != store.JobInterrupted || f.k.runsOf("W") != 0 {
		t.Fatalf("the queued job is %s, run %d times", j.State, f.k.runsOf("W"))
	}
	if _, err := f.r.Enqueue(context.Background(), store.Job{ID: "late", TenantID: store.DefaultTenantID, Kind: "test"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("an enqueue after Close: %v", err)
	}
	f.r.Close()
}

// A running job of a kind that resumes is queued again when Close stops it,
// not ended (no Ended), and the next start runs it.
func TestCloseQueuesAgainAJobWhoseKindResumes(t *testing.T) {
	t.Parallel()
	st := storetest.Open(t, "sqlite")
	f := newFix(t, st, IO, nil)
	started := make(chan struct{})
	f.r.Register("slow", &stepKind{kind: f.k, run: func(ctx context.Context, p *Progress) error {
		p.Step(1, 0)
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}})
	f.r.Start()
	if _, err := f.r.Enqueue(context.Background(), store.Job{ID: "M", TenantID: store.DefaultTenantID, Kind: "slow", LockKey: "space:S", Total: 2}); err != nil {
		t.Fatal(err)
	}
	<-started
	f.r.Close()
	if j := f.state("M"); j.State != store.JobQueued || j.Done != 1 {
		t.Fatalf("%+v", j)
	}
	select {
	case j := <-f.k.ended:
		t.Fatalf("Ended was called for a job that goes on: %+v", j)
	default:
	}
	// The next start takes it up.
	g := newFix(t, st, IO, nil)
	g.r.Register("slow", g.k)
	g.r.Start()
	close(g.k.gate("M"))
	select {
	case j := <-g.k.ended:
		if j.ID != "M" || j.State != store.JobDone {
			t.Fatalf("%+v", j)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the next start did not run it")
	}
}

// A job that does not stop when asked holds Close up for StopWait at most.
func TestCloseWaitsForAStuckJobWithinABound(t *testing.T) {
	t.Parallel()
	f := newFix(t, storetest.Open(t, "sqlite"), Bulk, func(o *Options) { o.StopWait = 20 * time.Millisecond })
	stuck, started := make(chan struct{}), make(chan struct{})
	f.r.Register("stuck", &stepKind{kind: f.k, run: func(ctx context.Context, p *Progress) error {
		close(started)
		<-stuck
		return nil
	}})
	f.r.Start()
	if _, err := f.r.Enqueue(context.Background(), store.Job{ID: "X", TenantID: store.DefaultTenantID, Kind: "stuck"}); err != nil {
		t.Fatal(err)
	}
	<-started
	f.r.Close()
	close(stuck)
	f.r.wg.Wait()
}

// stepKind is kind with its own Run.
type stepKind struct {
	*kind
	run func(ctx context.Context, p *Progress) error
}

func (s *stepKind) Run(ctx context.Context, _ *store.Job, p *Progress) error { return s.run(ctx, p) }
