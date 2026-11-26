// Package jobs runs the server's background work from one table, jobs
// (docs/design/consolidation-2.md, section 4).
//
// Each kind registers a Handler: how to run a job, what a restart makes of one
// that was queued or running, and the class whose slots it takes. Enqueue
// inserts the row and wakes the runner; nothing polls. When woken (an enqueue,
// or a job ending) the runner reads the active jobs from their partial index (a
// handful of rows), and for each queued one whose class has a free slot (per
// tenant and on the server) claims it with one conditional UPDATE on the
// primary key: of two runners on one database only one wins a job.
//
// Progress is kept in memory, where Get reads it while the job runs, and
// written at most once a second or every 100 items (the first step at once);
// that write changes no indexed column, so on PostgreSQL it is a HOT update. The
// end of a job is always written, tried three times.
//
// At Start the runner reads the active jobs once and asks each one's kind what
// a restart makes of it (Recover): taken up again, or ended interrupted,
// failed or done. Close stops the running jobs through their contexts and
// waits for them, within a bound.
//
// runner and lease_until are written for a standby server's leases (phase 5)
// but not read yet: today a start recovers every active row, as the single
// server it is.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"petalbook.app/server/internal/store"
)

// Outcome is what a restart makes of a job that was queued or running.
type Outcome string

const (
	// Resume queues it again: it runs from the start of its handler, which
	// picks up where the job's own records say it was.
	Resume Outcome = "resume"
	// Interrupt ends it interrupted: what was not done is not done.
	Interrupt Outcome = store.JobInterrupted
	// Fail ends it failed.
	Fail Outcome = store.JobFailed
	// Done ends it done (the work had finished; only its end was not written).
	Done Outcome = store.JobDone
)

// Class is the slots a kind's jobs take: at most PerTenant running for each
// tenant a job works on and PerServer on the server (0: no limit).
type Class struct {
	Name                 string
	PerTenant, PerServer int
}

// The classes, with the numbers the separate runners had.
var (
	// Bulk actions: one per tenant, two on the server.
	Bulk = Class{Name: "bulk", PerTenant: 1, PerServer: 2}
	// IO: exports and moves of storage: one per tenant, two on the server.
	IO = Class{Name: "io", PerTenant: 1, PerServer: 2}
	// Recycle actions: one per item through the lock key, paced by the handler.
	Recycle = Class{Name: "recycle"}
)

// Handler runs the jobs of one kind.
type Handler interface {
	// Class is the slots its jobs take.
	Class() Class
	// Run does the work, reporting through p. It returns nil once the job is
	// done (items that did not work are listed with p.Fail; the job is still
	// done), and an error when the job as a whole failed. When ctx ends it
	// stops as soon as it can and returns ctx.Err(): the job ends interrupted
	// (the server is stopping) or cancelled.
	Run(ctx context.Context, j *store.Job, p *Progress) error
	// Recover says what a restart makes of a job that was queued or running.
	Recover(ctx context.Context, j *store.Job) Outcome
}

// Queuer is a Handler whose jobs work on several tenants: a job takes the slot
// of each (by default only the slot of its own tenant).
type Queuer interface {
	Queues(j *store.Job) []string
}

// Ender is a Handler told when a job has ended and its end is written (the
// audit row and notice of its end), with the job as it ended.
type Ender interface {
	Ended(ctx context.Context, j *store.Job)
}

// Result is how a job ended, for the process that waited for it (EnqueueWait):
// Err is what the handler returned (nil: done; ErrClosed: the runner closed
// before the job ended), Value what it left with Progress.Return.
type Result struct {
	Job   *store.Job
	Err   error
	Value any
}

// Options configure a Runner.
type Options struct {
	Store  *store.Store
	Logger *slog.Logger
	// Now is the clock of the rows and of the progress throttle.
	Now func() time.Time
	// ID names this process in the rows it claims (default host-pid).
	ID string
	// StopWait bounds how long Close waits for the running jobs to stop
	// (default one minute); a job still running then is left to the next
	// start's Recover.
	StopWait time.Duration
	// WriteFault, when set, runs before each try of a write of a job's state
	// ("running" for progress, the end state for the end) and fails that try
	// with the error it returns (tests).
	WriteFault func(j *store.Job, state string, try int) error
	// Sleep waits between the tries of a write (default time.Sleep).
	Sleep func(time.Duration)
	// After is the clock of the retries of a scan or claim that failed
	// (default time.After).
	After func(time.Duration) <-chan time.Time
	// MaxRunning and PerTenant, when set, are the limits of the classes that
	// have one (Bulk and IO, which share them: the settings jobs.maxRunning
	// and jobs.perTenant). They are read each time the runner decides what to
	// start, so a change applies at the next dispatch; a lowered limit stops
	// nothing that runs. Unset, a class keeps its own numbers.
	MaxRunning func() int
	PerTenant  func(tenant string) int
	// Fault, when set, runs before the runner's scan ("scan") and each claim
	// ("claim") and fails it with the error it returns (tests).
	Fault func(op string) error
}

// Errors of the runner.
var (
	ErrClosed   = errors.New("jobs: the runner is closed")
	ErrNoKind   = errors.New("jobs: no handler for the kind")
	ErrEnded    = errors.New("jobs: the job has ended")
	errPanicked = errors.New("jobs: the handler panicked")
)

// A progress write is made at most this often, or every flushItems items.
const (
	flushEvery = time.Second
	flushItems = 100
	writeTries = 3
	// A scan or claim that failed is tried again after minBackoff, doubled at
	// each failure up to maxBackoff, and back to minBackoff after a pass that
	// worked: nothing else would wake the runner for a job already accepted.
	minBackoff = time.Second
	maxBackoff = 30 * time.Second
	// endedWait bounds an Ender's Ended.
	endedWait = 10 * time.Second
)

// backoffAfter is the wait after a failure that came prev after the last one
// (0: the first).
func backoffAfter(prev time.Duration) time.Duration {
	if prev <= 0 {
		return minBackoff
	}
	return min(prev*2, maxBackoff)
}

// Runner runs the jobs of the kinds registered with it.
type claim struct {
	job    *store.Job
	cancel bool
}

type Runner struct {
	opt Options
	log *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wakeup chan struct{}

	mu       sync.Mutex
	handlers map[string]Handler
	closed   bool
	started  bool
	live     map[string]*Progress
	// mine are the jobs this runner enqueued that it has not seen claimed: at
	// Close the ones still queued get their kind's restart outcome.
	mine map[string]bool
	// server and tenant count the running jobs per class, and per class and
	// tenant.
	server map[string]int
	tenant map[string]map[string]int
	// claiming are the jobs being claimed and not yet running: a Cancel then is
	// recorded on them and applied when they start.
	claiming map[string]*claim
	// backoff is the last wait before a retry; retrying, that one is pending.
	backoff  time.Duration
	retrying bool
	// watch are the jobs somebody waits for (EnqueueWait), each channel
	// buffered for its one result.
	watch map[string]chan Result
	// idle is closed (and replaced) whenever a job has ended and released its
	// slots: WaitIdle looks again then.
	idle chan struct{}
	// running counts the goroutines of jobs, until their Ended is done.
	running int
	// afterClaim, when set, runs after a claim that worked (tests).
	afterClaim func(id string)
	// beforeCancelWrite, when set, runs in Cancel between reading a job's row
	// and writing its cancel (tests).
	beforeCancelWrite func(id string)

	wg        sync.WaitGroup
	closeOnce sync.Once
}

// New returns a runner; register the kinds, then Start it.
func New(opt Options) *Runner {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Sleep == nil {
		opt.Sleep = time.Sleep
	}
	if opt.After == nil {
		opt.After = time.After
	}
	if opt.StopWait <= 0 {
		opt.StopWait = time.Minute
	}
	if opt.ID == "" {
		host, _ := os.Hostname()
		opt.ID = fmt.Sprintf("%s-%d", host, os.Getpid())
	}
	log := opt.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	r := &Runner{opt: opt, log: log, wakeup: make(chan struct{}, 1), handlers: map[string]Handler{}, live: map[string]*Progress{},
		mine: map[string]bool{}, server: map[string]int{}, tenant: map[string]map[string]int{}, claiming: map[string]*claim{}, watch: map[string]chan Result{}, idle: make(chan struct{})}
	r.ctx, r.cancel = context.WithCancel(context.Background())
	return r
}

// Register sets the handler of a kind. Call it before Start.
func (r *Runner) Register(kind string, h Handler) {
	r.mu.Lock()
	r.handlers[kind] = h
	r.mu.Unlock()
}

func (r *Runner) handler(kind string) Handler {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.handlers[kind]
}

// Start recovers the jobs a stop left queued or running (each by its kind's
// Recover; a kind with no handler here is left alone) and starts running jobs.
// It reads the active jobs once.
func (r *Runner) Start() {
	r.mu.Lock()
	if r.started || r.closed {
		r.mu.Unlock()
		return
	}
	r.started = true
	r.wg.Add(1)
	r.mu.Unlock()
	r.recover()
	go r.loop()
	r.Wake()
}

func (r *Runner) recover() {
	ctx := r.ctx
	act, err := store.ActiveJobs(ctx, r.opt.Store.Q())
	if err != nil {
		r.log.Warn("could not read the jobs a stop left", "error", err)
		return
	}
	counts := map[Outcome]int{}
	for _, j := range act {
		h := r.handler(j.Kind)
		if h == nil {
			continue
		}
		out := r.recoverOf(ctx, h, j)
		counts[out]++
		r.settle(j, out)
	}
	if len(counts) > 0 {
		r.log.Info("jobs a stop left were recovered", "resumed", counts[Resume], "interrupted", counts[Interrupt], "failed", counts[Fail], "done", counts[Done])
	}
}

// recoverOf is the kind's Recover; a panic in it is logged and fails the job.
func (r *Runner) recoverOf(ctx context.Context, h Handler, j *store.Job) Outcome {
	out := Fail
	r.guard(j, "Recover", func() { out = h.Recover(ctx, j) })
	return out
}

// guard runs f, a call into a kind; a panic is logged and reported as false,
// and the runner goes on.
func (r *Runner) guard(j *store.Job, what string, f func()) (ok bool) {
	defer func() {
		if v := recover(); v != nil {
			r.log.Error("a job's kind broke", "job", j.ID, "kind", j.Kind, "in", what, "panic", v, "stack", string(debug.Stack()))
			ok = false
		}
	}()
	f()
	return true
}

// settle applies a restart's outcome to a job in the state it was read in.
func (r *Runner) settle(j *store.Job, out Outcome) {
	q, now := r.opt.Store.Q(), r.opt.Now()
	var err error
	switch {
	case out == Resume && j.State == store.JobRunning:
		err = store.RequeueJob(context.Background(), q, j.ID, now)
	case out == Resume:
	default:
		err = store.SetJobState(context.Background(), q, j.ID, j.State, string(out), now)
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		r.log.Warn("could not record what a stop left of a job", "job", j.ID, "kind", j.Kind, "outcome", out, "error", err)
	}
}

// Wake makes the runner look for queued jobs it can start. Enqueue and the end
// of a job wake it; another process's enqueue does not (a standby server, phase
// 5, will lease instead).
func (r *Runner) Wake() {
	select {
	case r.wakeup <- struct{}{}:
	default:
	}
}

// Enqueue adds a job, queued, and wakes the runner. The job's ID, TenantID and
// Kind must be set; Created is set to now. ErrClosed: the runner is closing
// and nothing was added. store.ErrJobLocked: an active job holds its lock key.
func (r *Runner) Enqueue(ctx context.Context, j store.Job) (*store.Job, error) {
	out, _, err := r.enqueue(ctx, j, nil, false)
	return out, err
}

// EnqueueWait is Enqueue for a caller that waits for the job: with is run in
// the same transaction as the insert (a record that must point at the job; its
// error adds nothing), and the channel gets the job's Result once it has ended
// and its end is written, or ErrClosed when the runner closes first. The
// channel holds the one result, so a caller that never reads costs nothing.
func (r *Runner) EnqueueWait(ctx context.Context, j store.Job, with func(q store.Q) error) (*store.Job, <-chan Result, error) {
	return r.enqueue(ctx, j, with, true)
}

func (r *Runner) enqueue(ctx context.Context, j store.Job, with func(q store.Q) error, watch bool) (*store.Job, <-chan Result, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, nil, ErrClosed
	}
	if r.handlers[j.Kind] == nil {
		r.mu.Unlock()
		return nil, nil, fmt.Errorf("%w %q", ErrNoKind, j.Kind)
	}
	// Close waits for an enqueue under way, and settles what it added.
	r.wg.Add(1)
	r.mine[j.ID] = true
	var ch chan Result
	if watch {
		ch = make(chan Result, 1)
		r.watch[j.ID] = ch
	}
	r.mu.Unlock()
	defer r.wg.Done()
	j.State, j.Created = store.JobQueued, r.opt.Now()
	var err error
	if with == nil {
		err = store.InsertJob(ctx, r.opt.Store.Q(), j)
	} else {
		err = r.opt.Store.Tx(ctx, func(q store.Q) error {
			if err := store.InsertJob(ctx, q, j); err != nil {
				return err
			}
			return with(q)
		})
	}
	if err != nil {
		r.mu.Lock()
		delete(r.mine, j.ID)
		delete(r.watch, j.ID)
		r.mu.Unlock()
		return nil, nil, err
	}
	j.Updated = j.Created
	r.Wake()
	return &j, ch, nil
}

// deliver gives a job's result to whoever waits for it.
func (r *Runner) deliver(id string, res Result) {
	r.mu.Lock()
	ch := r.watch[id]
	delete(r.watch, id)
	r.mu.Unlock()
	if ch != nil {
		ch <- res
	}
}

// Get returns a job in the scope: from memory while this runner runs it (no
// database read), from its row otherwise.
func (r *Runner) Get(ctx context.Context, sc store.Scope, id string) (*store.Job, error) {
	r.mu.Lock()
	p := r.live[id]
	r.mu.Unlock()
	if p != nil {
		j := p.snapshot()
		if !sc.Sees(&j) {
			return nil, store.ErrNotFound
		}
		return &j, nil
	}
	return store.GetJob(ctx, r.opt.Store.Q(), sc, id)
}

// Cancel stops a job in the scope: a queued one ends cancelled at once; one
// this runner runs (or is starting) has its context ended and ends cancelled
// once its handler returns. ErrEnded: it had ended; store.ErrNotFound: there
// is no such job in the scope, or another process runs it.
func (r *Runner) Cancel(ctx context.Context, sc store.Scope, id string) error {
	// A job claimed between Cancel's look at the memory and its write of the
	// row is in the memory by the next look.
	for try := 0; ; try++ {
		err := r.cancelOnce(ctx, sc, id)
		if !errors.Is(err, errRaced) {
			return err
		}
		if try >= 3 {
			return store.ErrNotFound // another process runs it
		}
	}
}

// errRaced: the row was queued when read and running when written.
var errRaced = errors.New("jobs: claimed while it was being cancelled")

func (r *Runner) cancelOnce(ctx context.Context, sc store.Scope, id string) error {
	r.mu.Lock()
	if p := r.live[id]; p != nil {
		snap := p.snapshot()
		if !sc.Sees(&snap) {
			r.mu.Unlock()
			return store.ErrNotFound
		}
		p.mu.Lock()
		p.cancelled = true
		p.mu.Unlock()
		p.stop()
		r.mu.Unlock()
		return nil
	}
	if c := r.claiming[id]; c != nil {
		if !sc.Sees(c.job) {
			r.mu.Unlock()
			return store.ErrNotFound
		}
		// Applied when it starts; and should the claim lose to this, the job
		// ends cancelled in the queue.
		c.cancel = true
		r.mu.Unlock()
		if err := store.SetJobState(ctx, r.opt.Store.Q(), id, store.JobQueued, store.JobCancelled, r.opt.Now()); err != nil && !errors.Is(err, store.ErrNotFound) {
			r.log.Warn("could not cancel a job being claimed", "job", id, "error", err)
		}
		return nil
	}
	r.mu.Unlock()
	j, err := store.GetJob(ctx, r.opt.Store.Q(), sc, id)
	if err != nil {
		return err
	}
	if store.JobEnded(j.State) {
		return ErrEnded
	}
	if r.beforeCancelWrite != nil {
		r.beforeCancelWrite(id)
	}
	err = store.SetJobState(ctx, r.opt.Store.Q(), id, store.JobQueued, store.JobCancelled, r.opt.Now())
	if errors.Is(err, store.ErrNotFound) {
		// Not queued any more: ended, or claimed since the row was read.
		cur, gerr := store.GetJob(ctx, r.opt.Store.Q(), sc, id)
		switch {
		case gerr != nil:
			return gerr
		case store.JobEnded(cur.State):
			return ErrEnded
		}
		return errRaced
	}
	if err == nil {
		j.State = store.JobCancelled
		r.deliver(id, Result{Job: j, Err: context.Canceled})
	}
	return err
}

// WaitIdle returns once no job is queued or running (tests, and a caller that
// wants the work over): it looks again each time a job ends. A job that cannot
// start (a kind this runner has no handler for) holds it until ctx ends.
func (r *Runner) WaitIdle(ctx context.Context) error {
	for {
		r.mu.Lock()
		ch, running := r.idle, r.running
		r.mu.Unlock()
		act, err := store.ActiveJobs(ctx, r.opt.Store.Q())
		if err != nil {
			return err
		}
		if len(act) == 0 && running == 0 {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Close stops the runner: no job is enqueued or started any more, the running
// ones are stopped through their contexts, and Close waits for them (within
// StopWait). The jobs it enqueued that are still queued get their kind's
// restart outcome. It may be called again.
func (r *Runner) Close() {
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		r.mu.Unlock()
		r.cancel()
		ended := make(chan struct{})
		go func() { r.wg.Wait(); close(ended) }()
		t := time.NewTimer(r.opt.StopWait)
		defer t.Stop()
		select {
		case <-ended:
		case <-t.C:
			r.mu.Lock()
			var ids []string
			for id := range r.live {
				ids = append(ids, id)
			}
			r.mu.Unlock()
			r.log.Error("jobs still running when the wait for them ended; the next start recovers them", "jobs", ids, "wait", r.opt.StopWait)
		}
		r.mu.Lock()
		var waiting []chan Result
		for id, ch := range r.watch {
			waiting = append(waiting, ch)
			delete(r.watch, id)
		}
		var mine []string
		for id := range r.mine {
			mine = append(mine, id)
		}
		r.mine = map[string]bool{}
		r.mu.Unlock()
		for _, ch := range waiting {
			ch <- Result{Err: ErrClosed}
		}
		for _, id := range mine {
			j, err := store.GetJob(context.Background(), r.opt.Store.Q(), store.AllTenants(), id)
			if err != nil || j.State != store.JobQueued {
				continue
			}
			if h := r.handler(j.Kind); h != nil {
				r.settle(j, r.recoverOf(context.Background(), h, j))
			}
		}
	})
}

func (r *Runner) loop() {
	defer r.wg.Done()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-r.wakeup:
		}
		err := r.dispatch()
		switch {
		case r.ctx.Err() != nil:
		case err != nil:
			r.retryLater()
		default:
			r.mu.Lock()
			r.backoff = 0
			r.mu.Unlock()
		}
	}
}

// retryLater wakes the runner again after the backoff, unless a retry is
// pending already.
func (r *Runner) retryLater() {
	r.mu.Lock()
	if r.retrying {
		r.mu.Unlock()
		return
	}
	r.retrying = true
	r.backoff = backoffAfter(r.backoff)
	d := r.backoff
	r.wg.Add(1) // the loop holds the group: Close cannot be waiting yet
	r.mu.Unlock()
	go func() {
		defer r.wg.Done()
		select {
		case <-r.opt.After(d):
			r.mu.Lock()
			r.retrying = false
			r.mu.Unlock()
			r.Wake()
		case <-r.ctx.Done():
		}
	}()
}

func (r *Runner) fault(op string) error {
	if r.opt.Fault != nil {
		return r.opt.Fault(op)
	}
	return nil
}

// dispatch starts the queued jobs that have a free slot, oldest first. It
// returns the error of a scan or claim that failed (the loop tries again
// later).
func (r *Runner) dispatch() error {
	act, err := store.ActiveJobs(r.ctx, r.opt.Store.Q())
	if ferr := r.fault("scan"); ferr != nil {
		err = ferr
	}
	if err != nil {
		if r.ctx.Err() == nil {
			r.log.Warn("could not read the queued jobs; trying again later", "error", err)
		}
		return err
	}
	var failed error
	for _, j := range act {
		if j.State != store.JobQueued || r.ctx.Err() != nil {
			continue
		}
		h := r.handler(j.Kind)
		if h == nil {
			continue
		}
		var cl Class
		var queues []string
		if !r.guard(j, "Class", func() { cl, queues = h.Class(), queuesOf(h, j) }) {
			r.failQueued(h, j)
			continue
		}
		if !r.take(cl, queues) {
			continue
		}
		r.mu.Lock()
		r.claiming[j.ID] = &claim{job: j}
		r.mu.Unlock()
		now := r.opt.Now()
		ok, err := false, r.fault("claim")
		if err == nil {
			ok, err = store.ClaimJob(r.ctx, r.opt.Store.Q(), j.ID, r.opt.ID, now)
		}
		if err != nil || !ok {
			if err != nil && r.ctx.Err() == nil {
				r.log.Warn("could not claim a job; trying again later", "job", j.ID, "error", err)
				failed = err
			}
			r.mu.Lock()
			delete(r.claiming, j.ID)
			r.mu.Unlock()
			r.release(cl, queues)
			continue
		}
		if r.afterClaim != nil {
			r.afterClaim(j.ID)
		}
		j.State, j.Runner, j.Attempt, j.Started, j.Updated = store.JobRunning, r.opt.ID, j.Attempt+1, now, now
		r.start(h, j, cl, queues)
	}
	return failed
}

// failQueued ends a queued job failed: its kind broke before it could start.
// Its end is told to the kind like any other (Ended), so that it is audited.
func (r *Runner) failQueued(h Handler, j *store.Job) {
	end := *j
	end.State, end.Error = store.JobFailed, "an internal error stopped the job"
	err := store.EndJob(context.Background(), r.opt.Store.Q(), end, store.JobQueued, r.opt.Now())
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			r.log.Warn("could not record that a job broke", "job", j.ID, "error", err)
		}
		return
	}
	end.Updated = r.opt.Now()
	r.ended(context.Background(), h, &end)
	r.deliver(j.ID, Result{Job: &end, Err: errPanicked})
}

// ended tells an Ender kind that a job has ended and its end is written.
func (r *Runner) ended(ctx context.Context, h Handler, end *store.Job) {
	if e, ok := h.(Ender); ok {
		ectx, cancel := context.WithTimeout(ctx, endedWait)
		r.guard(end, "Ended", func() { e.Ended(ectx, end) })
		cancel()
	}
}

func queuesOf(h Handler, j *store.Job) []string {
	if q, ok := h.(Queuer); ok {
		if qs := q.Queues(j); len(qs) > 0 {
			return qs
		}
	}
	return []string{j.TenantID}
}

// take takes a slot of the class on the server and for each tenant, all or
// none.
func (r *Runner) take(cl Class, tenants []string) bool {
	cl = r.limited(cl)
	perTenant := make(map[string]int, len(tenants))
	if cl.PerTenant > 0 {
		for _, t := range tenants {
			perTenant[t] = cl.PerTenant
			if r.opt.PerTenant != nil {
				perTenant[t] = max(1, r.opt.PerTenant(t))
			}
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if cl.PerServer > 0 && r.server[cl.Name] >= cl.PerServer {
		return false
	}
	per := r.tenant[cl.Name]
	for _, t := range tenants {
		if cl.PerTenant > 0 && per[t] >= perTenant[t] {
			return false
		}
	}
	if per == nil {
		per = map[string]int{}
		r.tenant[cl.Name] = per
	}
	for _, t := range tenants {
		per[t]++
	}
	r.server[cl.Name]++
	return true
}

// limitedName is the slot group of the classes that the settings limit.
const limitedName = "limited"

// limited is the class as it counts its slots: the classes with limits (bulk
// actions, exports and moves) share one group, whose numbers are the settings'.
func (r *Runner) limited(cl Class) Class {
	if cl.PerTenant == 0 && cl.PerServer == 0 {
		return cl
	}
	cl.Name = limitedName
	if r.opt.MaxRunning != nil {
		cl.PerServer = max(1, r.opt.MaxRunning())
	}
	return cl
}

func (r *Runner) release(cl Class, tenants []string) {
	cl = r.limited(cl)
	r.mu.Lock()
	per := r.tenant[cl.Name]
	for _, t := range tenants {
		if per[t]--; per[t] <= 0 {
			delete(per, t)
		}
	}
	r.server[cl.Name]--
	r.mu.Unlock()
}

func (r *Runner) start(h Handler, j *store.Job, cl Class, queues []string) {
	ctx, stop := context.WithCancel(r.ctx)
	p := &Progress{r: r, job: *j, stop: stop}
	p.job.Failed = append([]store.JobFailure(nil), j.Failed...)
	r.mu.Lock()
	r.live[j.ID] = p
	delete(r.mine, j.ID)
	cancelled := false
	if c := r.claiming[j.ID]; c != nil {
		cancelled = c.cancel
		delete(r.claiming, j.ID)
	}
	if cancelled {
		// Cancelled between its claim and now.
		p.cancelled = true
		stop()
	}
	r.wg.Add(1)
	r.running++
	r.mu.Unlock()
	go func() {
		defer r.wg.Done()
		defer stop()
		r.run(ctx, h, j, p)
		r.release(cl, queues)
		r.Wake()
		r.mu.Lock()
		r.running--
		close(r.idle)
		r.idle = make(chan struct{})
		r.mu.Unlock()
	}()
}

// run runs one job and writes how it ended.
func (r *Runner) run(ctx context.Context, h Handler, j *store.Job, p *Progress) {
	err := r.call(ctx, h, j, p)
	p.mu.Lock()
	end := p.job
	end.Failed = append([]store.JobFailure(nil), p.job.Failed...)
	switch {
	case errors.Is(err, errPanicked):
		id := p.current
		if id == "" {
			id = "*"
		}
		end.Failed = append(end.Failed, store.JobFailure{ID: id, Error: "an internal error stopped the job", Code: "internal"})
		end.State = store.JobFailed
	case err != nil && ctx.Err() != nil && p.cancelled:
		end.State = store.JobCancelled
	case err != nil && ctx.Err() != nil:
		end.State = store.JobInterrupted
	case err != nil:
		end.State, end.Error = store.JobFailed, err.Error()
	case p.broken:
		end.State = store.JobFailed
	default:
		end.State = store.JobDone
	}
	p.mu.Unlock()
	if werr := r.write(&end, end.State, func() error {
		return store.EndJob(context.Background(), r.opt.Store.Q(), end, store.JobRunning, r.opt.Now())
	}); werr != nil {
		r.log.Error("a job ended and its end could not be recorded; the next start recovers it", "job", j.ID, "kind", j.Kind, "state", end.State, "error", werr)
	}
	end.Updated = r.opt.Now()
	r.mu.Lock()
	delete(r.live, j.ID)
	r.mu.Unlock()
	// A context of its own: the job's may have ended (Close), and what Ended
	// records (the audit row of the end) must not be lost to it.
	r.ended(context.WithoutCancel(ctx), h, &end)
	p.mu.Lock()
	value := p.value
	p.mu.Unlock()
	r.deliver(j.ID, Result{Job: &end, Err: err, Value: value})
}

// call runs the handler; a panic is logged and reported as errPanicked.
func (r *Runner) call(ctx context.Context, h Handler, j *store.Job, p *Progress) (err error) {
	defer func() {
		if v := recover(); v != nil {
			p.mu.Lock()
			item := p.current
			p.mu.Unlock()
			r.log.Error("a job broke", "job", j.ID, "kind", j.Kind, "item", item, "panic", v, "stack", string(debug.Stack()))
			err = errPanicked
		}
	}()
	jc := *j
	return h.Run(ctx, &jc, p)
}

// write runs f, trying again a few times; the error of the last try is
// returned (ErrNotFound at once: the row is not in the state written from).
func (r *Runner) write(j *store.Job, state string, f func() error) error {
	var err error
	for try := 0; try < writeTries; try++ {
		if try > 0 {
			r.opt.Sleep(time.Duration(try) * 50 * time.Millisecond)
		}
		if r.opt.WriteFault != nil {
			if err = r.opt.WriteFault(j, state, try); err != nil {
				continue
			}
		}
		if err = f(); err == nil || errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	r.log.Warn("could not record how far a job is", "job", j.ID, "kind", j.Kind, "state", state, "error", err)
	return err
}

// Progress is how far a running job is. Its methods are safe to call from the
// handler's goroutines.
type Progress struct {
	r    *Runner
	stop context.CancelFunc
	// flushing keeps the progress writes in order.
	flushing sync.Mutex

	mu        sync.Mutex
	job       store.Job
	current   string
	lastFlush time.Time
	unflushed int
	// broken: a progress write failed every try; the job ends failed rather
	// than done.
	broken    bool
	cancelled bool
	value     any
}

// Return leaves a value for the process that waits for the job (EnqueueWait):
// what the request that started it answers with. It is not kept in the row.
func (p *Progress) Return(v any) {
	p.mu.Lock()
	p.value = v
	p.mu.Unlock()
}

// SetResult sets the job's result (JSON), written with its end.
func (p *Progress) SetResult(b []byte) {
	p.mu.Lock()
	p.job.Result = b
	p.mu.Unlock()
}

// Item names the item the job is working on (a panic lists it as failed).
func (p *Progress) Item(id string) {
	p.mu.Lock()
	p.current = id
	p.mu.Unlock()
}

// SetTotal sets how many items and bytes the job has (when its handler learns
// them only once running).
func (p *Progress) SetTotal(items, bytes int64) {
	p.mu.Lock()
	p.job.Total, p.job.BytesTotal = items, bytes
	p.mu.Unlock()
}

// Step adds n items and bytes done. The row is written at most once a second
// or every 100 items (the first step at once).
func (p *Progress) Step(n, bytes int64) {
	p.mu.Lock()
	p.job.Done += n
	p.job.BytesDone += bytes
	p.current = ""
	p.unflushed += int(n)
	p.mu.Unlock()
	p.maybeFlush()
}

// Fail lists an item that did not work; the job goes on. It counts as a step
// for the write throttle.
func (p *Progress) Fail(f store.JobFailure) {
	p.mu.Lock()
	p.job.Failed = append(p.job.Failed, f)
	p.current = ""
	p.unflushed++
	p.mu.Unlock()
	p.maybeFlush()
}

func (p *Progress) maybeFlush() {
	p.flushing.Lock()
	defer p.flushing.Unlock()
	now := p.r.opt.Now()
	p.mu.Lock()
	due := p.lastFlush.IsZero() || now.Sub(p.lastFlush) >= flushEvery || p.unflushed >= flushItems
	if !due {
		p.mu.Unlock()
		return
	}
	p.lastFlush, p.unflushed = now, 0
	j := p.job
	j.Failed = append([]store.JobFailure(nil), p.job.Failed...)
	p.mu.Unlock()
	err := p.r.write(&j, store.JobRunning, func() error {
		return store.JobProgress(context.Background(), p.r.opt.Store.Q(), j.ID, j.Done, j.BytesDone, j.Failed)
	})
	if err != nil {
		p.mu.Lock()
		p.broken = true
		p.mu.Unlock()
	}
}

func (p *Progress) snapshot() store.Job {
	p.mu.Lock()
	defer p.mu.Unlock()
	j := p.job
	j.Failed = append([]store.JobFailure(nil), p.job.Failed...)
	return j
}
