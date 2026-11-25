package space

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/gitstore"
)

// committer turns a stream of file writes into Git commits. Saving only
// writes the file; the commit happens after a quiet period (so a burst of
// auto-saves is one commit), after a maximum age (so a long writing session
// still gets history), and on shutdown.
//
// It commits only the paths the server itself wrote, moved or deleted, so a
// person's own work in the same repository is never swept into a commit. It
// takes its list of paths under a small lock of its own and runs git without
// holding any lock the writers need, so a save never waits for git.
//
// Every pending path remembers who changed it last, and a commit holds one
// person's changes: the paths are grouped by author and each group is its own
// commit. So that a later change by someone else cannot take earlier work for
// itself (Ann edits a page, Bo moves it in the same window: Git sees one
// file, and its latest toucher would get all of it), a change starts with
// SwitchTo: if anything is pending for another author, that is committed
// first, before the change writes anything. With one author, the common case,
// nothing is pending for anyone else and this costs nothing; it costs one
// commit only when the author switches inside a quiet window.
type committer struct {
	git  *gitRepo
	idle time.Duration
	max  time.Duration
	log  *slog.Logger

	mu    sync.Mutex
	paths map[string]Author // changed since the last commit, and by whom
	first time.Time         // first uncommitted write
	last  time.Time         // latest write

	j     *journal   // what is pending, on disk, so a crash does not lose it
	open  []jrec     // "b" and "u" records whose edit of space.yml is not done yet (under mu)
	runMu sync.Mutex // one git run at a time

	// active counts the changes running now (begin) by the number of the
	// last journal frame before each began (under mu). A change journals its
	// records before it writes and hands its paths over only after, so a
	// commit must keep in the journal every record from the earliest of them
	// on: what such a change journaled is not in the commit.
	active map[uint64]int

	// Packing (see maintain). A commit leaves its objects loose; after
	// maintainEvery commits the loop packs them, under runMu so that it never
	// runs at the same time as a commit.
	maintainEvery atomic.Int64 // commits between two packings; 0 means DefaultMaintainEvery
	sinceMaintain atomic.Int64 // commits made since the last packing
	maintainOff   bool         // under runMu: the space follows a remote, whose fetches and pushes are not under runMu

	// onCommit is told the paths of each commit made (under mu).
	onCommit func(paths []string)

	// Test hooks, nil otherwise: recorded runs after a Record has journaled
	// its records, snapshotted after a commit has taken the pending paths.
	// Both are read under mu.
	recorded, snapshotted func()

	wake chan struct{}
	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// newCommitter starts the committer. initial are paths recovered from an
// earlier run's journal.
func newCommitter(g *gitRepo, idle, max time.Duration, log *slog.Logger, j *journal, initial map[string]Author) *committer {
	c := &committer{git: g, idle: idle, max: max, log: log, j: j, paths: map[string]Author{}, active: map[uint64]int{},
		wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	if len(initial) > 0 {
		now := time.Now()
		c.first, c.last = now, now
		for p, by := range initial {
			c.paths[p] = by
		}
	}
	go c.loop()
	return c
}

// DefaultMaintainEvery is how many commits the committer makes between two
// packings of the Git objects they leave loose.
const DefaultMaintainEvery = 50

// closeMaintainMin is how many commits since the last packing make Close pack.
// Fewer are left for later: a space that is closed and opened again often must
// not add a pack every time.
const closeMaintainMin = 10

// pauseMaintenance stops packing, for good until resumeMaintenance: a space
// that follows a remote does its fetches, commits and pushes under the space
// lock, not under runMu, so packing here could overlap a fetch of its own.
// It waits for a packing that is running.
func (c *committer) pauseMaintenance() {
	c.runMu.Lock()
	c.maintainOff = true
	c.runMu.Unlock()
}

func (c *committer) resumeMaintenance() {
	c.runMu.Lock()
	c.maintainOff = false
	c.runMu.Unlock()
}

func (c *committer) every() int64 {
	if n := c.maintainEvery.Load(); n > 0 {
		return n
	}
	return DefaultMaintainEvery
}

// maintain packs the loose objects now, if enough commits have been made
// since the last time (at Close: at least closeMaintainMin). Under
// runMu, so never alongside a commit. A failure is only a warning: the commits
// it follows are done.
func (c *committer) maintain(atClose bool) {
	n := c.sinceMaintain.Load()
	if n == 0 || (!atClose && n < c.every()) || (atClose && n < closeMaintainMin) {
		return
	}
	c.runMu.Lock()
	defer c.runMu.Unlock()
	if c.maintainOff || c.sinceMaintain.Load() == 0 {
		return
	}
	// Reset first: a failure is retried after another batch, not on every pass.
	c.sinceMaintain.Store(0)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	start := time.Now()
	if err := c.git.r().Maintain(ctx); err != nil {
		c.log.Warn("could not pack the Git objects; will try again later", "error", err)
		return
	}
	c.log.Debug("packed the Git objects", "commits", n, "took", time.Since(start))
}

// setOnCommit sets what is told the paths of each commit made.
func (c *committer) setOnCommit(f func(paths []string)) {
	c.mu.Lock()
	c.onCommit = f
	c.mu.Unlock()
}

// Record journals what is about to happen to files, durably, as a change by
// by. Call it before the write; if it fails, do not write.
func (c *committer) Record(by Author, recs ...jrec) error {
	if c == nil || c.j == nil {
		return nil
	}
	for i := range recs {
		recs[i].AuthorName, recs[i].AuthorEmail, recs[i].Via = by.Name, by.Email, by.Via
	}
	// A record of a folder move or delete is the only trace of the bindings
	// still to be changed: the trim after a commit keeps it until Done.
	// Under the lock the trim takes too, so that it cannot replace the journal
	// between the append and the entry in the open list (it would drop the
	// record); and entered only once the append has worked, so a record that
	// was never journaled does not stay open for the life of the process.
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.j.append(recs); err != nil {
		return err
	}
	for _, r := range recs {
		if r.Op == "b" || r.Op == "u" || r.Op == "l" || r.Op == "f" {
			c.open = append(c.open, r)
		}
	}
	if hook := c.recorded; hook != nil {
		c.mu.Unlock()
		hook()
		c.mu.Lock()
	}
	return nil
}

// Prepare journals a transaction by by, durably, before any of its files is
// written: the files with their content before and after, and the closing of
// the open records in closing ("l" records whose links the transaction makes
// right), all in one append with one sync. From then on a crash is finished
// by replaying the transaction, never by planning its work again. It returns
// the transaction's number (0 with no journal).
func (c *committer) Prepare(by Author, files []txFile, closing []jrec) (uint64, error) {
	if c == nil || c.j == nil {
		return 0, nil
	}
	extra := make([]jrec, len(closing))
	for i, r := range closing {
		r.Op += "d"
		r.Pages, r.N = nil, 0
		extra[i] = r
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	tx, err := c.j.appendTx(by, files, extra)
	if err != nil {
		return 0, err
	}
	for _, rec := range closing {
		for i, r := range c.open {
			if r.Op == rec.Op && r.From == rec.From && r.To == rec.To && r.Path == rec.Path && r.Rev == rec.Rev {
				c.open = append(c.open[:i:i], c.open[i+1:]...)
				break
			}
		}
	}
	if hook := c.recorded; hook != nil {
		c.mu.Unlock()
		hook()
		c.mu.Lock()
	}
	return tx, nil
}

// Applied journals that transaction tx is written, without a sync: the replay
// does not need it, so losing it costs nothing.
func (c *committer) Applied(tx uint64) {
	if c == nil || c.j == nil || tx == 0 {
		return
	}
	if err := c.j.appendNoSync([]jrec{{Op: "a", Tx: tx}}); err != nil {
		c.log.Warn("could not journal that a change was written", "error", err)
	}
}

// Abort journals that transaction tx was given up before any of its files
// was written (its first write failed, and the change was answered with an
// error), so that the next start does not make it after all.
func (c *committer) Abort(tx uint64) {
	if c == nil || c.j == nil || tx == 0 {
		return
	}
	if err := c.j.append([]jrec{{Op: "k", Tx: tx}}); err != nil {
		c.log.Error("could not journal that a change was given up; the next start may make it", "error", err)
	}
}

// hold keeps records that an earlier run left open, until Done says they are
// finished (a commit's trim of the journal keeps them).
func (c *committer) hold(recs ...jrec) {
	c.mu.Lock()
	c.open = append(c.open, recs...)
	c.mu.Unlock()
}

// Done says the edit of space.yml that the "b" or "u" record rec announced has
// been made (or is not going to be: the move was taken back). From then on a
// crash must not make it again, so a record that says so goes after it, and
// the trim no longer keeps it.
func (c *committer) Done(rec jrec) {
	if c == nil || c.j == nil {
		return
	}
	c.mu.Lock()
	for i, r := range c.open {
		if r.Op == rec.Op && r.From == rec.From && r.To == rec.To && r.Path == rec.Path && r.Rev == rec.Rev {
			c.open = append(c.open[:i:i], c.open[i+1:]...)
			break
		}
	}
	c.mu.Unlock()
	done := rec
	done.Op += "d"
	// The fields name the record; its pages are not needed to close it.
	done.Pages, done.N = nil, 0
	if err := c.j.append([]jrec{done}); err != nil {
		c.log.Error("could not journal that the bindings were changed", "error", err)
	}
}

// begin marks the start of a change that will journal records and then hand
// the paths it wrote to Touch; the function it returns marks its end. Until
// then no commit trims the records the change journals (see active). Call it
// under the space lock, after SwitchTo.
func (c *committer) begin() (end func()) {
	if c == nil || c.j == nil {
		return func() {}
	}
	c.mu.Lock()
	mark := c.j.lastSeq()
	c.active[mark]++
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		if c.active[mark]--; c.active[mark] <= 0 {
			delete(c.active, mark)
		}
		c.mu.Unlock()
	}
}

// Touch records that by changed these files (relative, slash-separated). A
// moved file is two paths: the old and the new.
func (c *committer) Touch(by Author, paths ...string) {
	if len(paths) == 0 {
		return
	}
	now := time.Now()
	c.mu.Lock()
	if len(c.paths) == 0 {
		c.first = now
	}
	for _, p := range paths {
		c.paths[p] = by
	}
	c.last = now
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// SwitchTo commits what is pending for authors other than by, so that the
// change by by that is about to write files cannot take their paths over. Call
// it under the space lock (mutate does), before the change records or writes
// anything: nothing else adds pending paths meanwhile, and the commit takes no
// lock of the space's, so it cannot wait on the caller. A commit that fails (a
// repository busy with a merge, a full disk) does not hold the change back:
// it goes ahead, and a path that both authors touched is then pending for by,
// the later one; the loop retries the failed commit. That is the one case
// where earlier work can be credited to someone else, and better than
// refusing a person's change for another's error.
func (c *committer) SwitchTo(by Author) {
	if c == nil {
		return
	}
	id, _ := c.git.identities(by)
	via := cleanVia(by.Via)
	c.mu.Lock()
	other := false
	for _, a := range c.paths {
		if x, _ := c.git.identities(a); x != id || cleanVia(a.Via) != via {
			other = true
			break
		}
	}
	c.mu.Unlock()
	if !other {
		return
	}
	if err := c.commit(); err != nil {
		c.log.Warn("could not commit earlier changes before another author's; they may be credited to that author", "error", err)
	}
}

// pendingUnder lists the paths in dir (and below) that changed and are not
// committed yet.
func (c *committer) pendingUnder(dir string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for p := range c.paths {
		if strings.HasPrefix(p, dir+"/") {
			out = append(out, p)
		}
	}
	return out
}

func (c *committer) loop() {
	defer close(c.done)
	for {
		c.mu.Lock()
		dirty, first, last := len(c.paths) > 0, c.first, c.last
		c.mu.Unlock()
		var timer <-chan time.Time
		var t *time.Timer
		if dirty {
			deadline := last.Add(c.idle)
			if m := first.Add(c.max); m.Before(deadline) {
				deadline = m
			}
			t = time.NewTimer(time.Until(deadline))
			timer = t.C
		}
		select {
		case <-c.stop:
			if t != nil {
				t.Stop()
			}
			return
		case <-c.wake:
			// A write moved the deadline: recompute.
		case <-timer:
			if err := c.commit(); err != nil {
				var busy *gitstore.BusyError
				if errors.As(err, &busy) {
					c.log.Warn("not committing now; will retry", "reason", err.Error())
				} else {
					c.log.Error("commit failed; will retry", "error", err)
				}
			}
		}
		if t != nil {
			t.Stop()
		}
		c.maintain(false)
	}
}

// commit commits what is pending, one commit per author. A failed commit (or
// a repository that is busy with a merge or rebase) leaves that author's paths
// pending, to be tried again after another quiet period; the other authors'
// commits are not held back by it. Writes that arrive while git runs are not
// lost: they are pending again and go into the next commit.
func (c *committer) commit() error {
	c.runMu.Lock()
	defer c.runMu.Unlock()
	c.mu.Lock()
	if len(c.paths) == 0 {
		c.mu.Unlock()
		return nil
	}
	taken := c.paths
	c.paths = map[string]Author{}
	// The journal's records up to upTo are of the paths taken: what the
	// changes before this point journaled, and handed over. A change still
	// running may have journaled records without handing its paths over yet:
	// upTo stops before the first of them.
	var upTo uint64
	if c.j != nil {
		upTo = c.j.lastSeq()
		for mark := range c.active {
			upTo = min(upTo, mark)
		}
	}
	hook := c.snapshotted
	c.mu.Unlock()
	if hook != nil {
		hook()
	}

	var errs []error
	var failed []string
	committed := 0
	repoBusy := false
	for _, grp := range c.git.groupByAuthor(taken) {
		if repoBusy {
			// The repository is mid-merge or mid-rebase for everyone: do not
			// ask git again for each author.
			failed = append(failed, grp.paths...)
			continue
		}
		made, err := c.git.commitPaths(grp.id, grp.via, grp.paths)
		if made {
			c.log.Info("committed changes", "files", len(grp.paths), "author", grp.id.Name)
		}
		// Paths Git could not stage are left out for good: kept pending they
		// would fail this commit again, and every commit after it. The files
		// are still where they are; a person has to look.
		var skipped *gitstore.SkippedError
		if errors.As(err, &skipped) {
			c.log.Error("some files could not be committed and are left out", "paths", skipped.Paths)
			err = nil
		}
		if err != nil {
			var busy *gitstore.BusyError
			repoBusy = errors.As(err, &busy)
			errs = append(errs, err)
			failed = append(failed, grp.paths...)
			continue
		}
		committed++
		if made {
			c.sinceMaintain.Add(1)
		}
		c.mu.Lock()
		tell := c.onCommit
		c.mu.Unlock()
		if tell != nil {
			tell(grp.paths)
		}
	}
	if len(failed) > 0 {
		now := time.Now()
		c.mu.Lock()
		if len(c.paths) == 0 {
			c.first = now
		}
		for _, p := range failed {
			// A path changed again while git ran is pending with its newer
			// author; only paths nobody touched since go back as they were.
			if _, newer := c.paths[p]; !newer {
				c.paths[p] = taken[p]
			}
		}
		c.last = now
		c.mu.Unlock()
		if committed == 0 {
			return errors.Join(errs...)
		}
	}
	// Packing is not done here: Flush and SwitchTo are waited for by a save.
	// The loop does it, after waking.
	if c.sinceMaintain.Load() >= c.every() {
		select {
		case c.wake <- struct{}{}:
		default:
		}
	}
	// Everything journaled up to upTo is in Git now, except what failed, which
	// is pending again and journaled anew. Records after upTo stay, at their
	// place: they are of changes this commit did not take, whether or not
	// their paths are pending yet.
	if c.j != nil {
		c.mu.Lock()
		again := make(map[string]Author, len(failed))
		for _, p := range failed {
			if by, ok := c.paths[p]; ok {
				again[p] = by
			}
		}
		err := c.j.trim(upTo, c.open, again)
		c.mu.Unlock()
		if err != nil {
			c.log.Error("could not trim the commit journal", "error", err)
		}
	}
	return errors.Join(errs...)
}

// clearJournal empties the journal: for a space that follows a remote, whose
// changes are committed and pushed (or undone) before the request is answered,
// so nothing is ever pending between two changes.
func (c *committer) clearJournal() error {
	if c == nil || c.j == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.j.trim(c.j.lastSeq(), c.open, nil)
}

// Flush commits pending changes immediately.
func (c *committer) Flush() error { return c.commit() }

// Close stops the background goroutine and commits what is pending.
func (c *committer) Close() error {
	c.once.Do(func() { close(c.stop) })
	<-c.done
	err := c.commit()
	c.maintain(true)
	if c.j != nil {
		c.j.close()
	}
	return err
}
