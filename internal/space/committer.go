package space

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
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
	// revs are the pending paths that a transaction wrote, with the content
	// it wrote: they are committed with that content, whatever the work tree
	// has by then. The others are staged from the work tree.
	revs  map[string]gitstore.Blob
	first time.Time // first uncommitted write
	last  time.Time // latest write

	j     *journal   // what is pending, on disk, so a crash does not lose it
	runMu sync.Mutex // one git run at a time

	// active counts the changes running now (begin) by the number of the
	// last journal frame before each began (under mu). A change journals its
	// records before it writes and hands its paths over only after, so a
	// commit must keep in the journal every record from the earliest of them
	// on: what such a change journaled is not in the commit.
	active map[uint64]int
	// depth is how many changes are running (begin). What they touch and
	// apply is held until the last of them ends, so that no commit takes part
	// of a change: a move with its rewrites is always one commit.
	depth     int
	heldPaths map[string]Author
	heldRevs  map[string]gitstore.Blob
	txs       map[uint64]preparedTx // prepared, not applied or given up yet

	// Packing (see maintain). A commit leaves its objects loose; after
	// maintainEvery commits the loop packs them, under runMu so that it never
	// runs at the same time as a commit.
	maintainEvery atomic.Int64 // commits between two packings; 0 means DefaultMaintainEvery
	sinceMaintain atomic.Int64 // commits made since the last packing
	maintainOff   bool         // under runMu: the space follows a remote, whose fetches and pushes are not under runMu
	remote        atomic.Bool  // the space follows a remote (pauseMaintenance)

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

// preparedTx is a transaction journaled and not yet applied: who makes it and
// its files, with the blob id each gets (the content is in the journal).
type preparedTx struct {
	by    Author
	files []txFileRef
}

// txFileRef is a file of a prepared transaction: its place among the files,
// its path, the blob id it gets and, for a moved page, its old name.
type txFileRef struct {
	i         int
	path, rev string
	from      string
	ref       bool // content in Git, not in the journal
}

// newCommitter starts the committer. initial are paths recovered from an
// earlier run's journal, and revs the content some of them are to be
// committed with.
func newCommitter(g *gitRepo, idle, max time.Duration, log *slog.Logger, j *journal, initial map[string]Author, revs map[string]gitstore.Blob) *committer {
	c := &committer{git: g, idle: idle, max: max, log: log, j: j, paths: map[string]Author{}, active: map[uint64]int{},
		revs: map[string]gitstore.Blob{}, heldPaths: map[string]Author{}, heldRevs: map[string]gitstore.Blob{}, txs: map[uint64]preparedTx{},
		wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	if len(initial) > 0 {
		now := time.Now()
		c.first, c.last = now, now
		for p, by := range initial {
			c.paths[p] = by
		}
		for p, b := range revs {
			if _, ok := initial[p]; ok {
				c.revs[p] = b
			}
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
//
// It also marks the space as one that follows a remote: from then on a
// transaction applied is not handed to the background committer (the change
// commits and pushes it itself), until resumeMaintenance.
func (c *committer) pauseMaintenance() {
	c.runMu.Lock()
	c.maintainOff = true
	c.runMu.Unlock()
	c.remote.Store(true)
}

func (c *committer) resumeMaintenance() {
	c.runMu.Lock()
	c.maintainOff = false
	c.runMu.Unlock()
	c.remote.Store(false)
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
	if hook := c.recorded; hook != nil {
		c.mu.Unlock()
		hook()
		c.mu.Lock()
	}
	return nil
}

// Prepare journals a transaction by by, durably, before any of its files is
// written: the files with their content before and after, in one append with
// one sync. From then on a crash is finished by replaying the transaction (a
// local space) or taken back (one that follows a remote), never by planning
// its work again. It returns the transaction's number (0 with no journal).
func (c *committer) Prepare(by Author, files []txFile) (uint64, error) {
	if c == nil || c.j == nil {
		return 0, nil
	}
	b := c.j.newTx()
	for _, f := range files {
		if err := b.add(f); err != nil {
			b.discard()
			return 0, err
		}
	}
	return c.PrepareBuild(by, b)
}

// PrepareBuild is Prepare for a transaction put together file by file
// (journal.newTx).
func (c *committer) PrepareBuild(by Author, b *txBuild) (uint64, error) {
	if c == nil || c.j == nil {
		b.discard()
		return 0, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	tx, err := b.commit(by, nil)
	if err != nil {
		var u *uncertainAppend
		if errors.As(err, &u) && tx != 0 {
			// The change fails, but the transaction may be in the journal:
			// given up now, the next start does not make it.
			if kerr := c.j.append([]jrec{{Op: "k", Tx: tx}}); kerr != nil {
				c.log.Error("a change that failed may be in the journal and be made at the next start",
					"transaction", tx, "error", errors.Join(err, kerr))
			}
		}
		return 0, err
	}
	pt := preparedTx{by: by}
	for i, f := range b.files() {
		if !f.Dir {
			pt.files = append(pt.files, txFileRef{i: i, path: f.Path, rev: f.Rev, from: f.From, ref: f.Ref})
		}
	}
	c.txs[tx] = pt
	if hook := c.recorded; hook != nil {
		c.mu.Unlock()
		hook()
		c.mu.Lock()
	}
	return tx, nil
}

// Applied journals that transaction tx is written, without a sync (the
// replay does not need it, so losing it costs nothing), and hands its files
// to the commit with the content it wrote.
func (c *committer) Applied(tx uint64) {
	if c == nil || c.j == nil || tx == 0 {
		return
	}
	c.mu.Lock()
	pt, ok := c.txs[tx]
	delete(c.txs, tx)
	if c.remote.Load() {
		// A change of a space that follows a remote commits its files itself.
		ok = false
	}
	if ok {
		for _, f := range pt.files {
			seq := tx + 1 + uint64(f.i) // the "p" frames follow the "x"
			b := gitstore.Blob{ID: f.rev, Data: func() ([]byte, error) { return c.j.contentAt(seq) }}
			if f.ref {
				id := f.rev
				b.Data = func() ([]byte, error) {
					var buf bytes.Buffer
					err := c.git.r().CatBlob(context.Background(), id, &buf)
					return buf.Bytes(), err
				}
			}
			c.touchLocked(pt.by, f.path, &b)
			if f.from != "" {
				c.touchLocked(pt.by, f.from, &gitstore.Blob{}) // gone
			}
		}
	}
	c.mu.Unlock()
	if ok && len(pt.files) > 0 {
		c.wakeUp()
	}
	if err := c.j.appendNoSync([]jrec{{Op: "a", Tx: tx}}); err != nil {
		c.log.Warn("could not journal that a change was written", "error", err)
	}
}

// forget drops what the committer knows of transaction tx, which was neither
// applied nor given up: the writing stopped part way, the files written are
// touched as they are, and the next start replays the rest.
func (c *committer) forget(tx uint64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.txs, tx)
	c.mu.Unlock()
}

// AbortAll journals that the transactions txs are given up, in one append:
// for a change of a space that follows a remote, undone before it is put
// back from its parent commit.
func (c *committer) AbortAll(txs []uint64) {
	if c == nil || c.j == nil || len(txs) == 0 {
		return
	}
	recs := make([]jrec, len(txs))
	c.mu.Lock()
	for i, tx := range txs {
		recs[i] = jrec{Op: "k", Tx: tx}
		delete(c.txs, tx)
	}
	c.mu.Unlock()
	if err := c.j.append(recs); err != nil {
		c.log.Error("could not journal that a change was given up", "error", err)
	}
}

// Abort journals that transaction tx was given up before any of its files
// was written (its first write failed, and the change was answered with an
// error), so that the next start does not make it after all.
func (c *committer) Abort(tx uint64) {
	if c == nil || c.j == nil || tx == 0 {
		return
	}
	c.mu.Lock()
	delete(c.txs, tx)
	c.mu.Unlock()
	if err := c.j.append([]jrec{{Op: "k", Tx: tx}}); err != nil {
		c.log.Error("could not journal that a change was given up; the next start may make it", "error", err)
	}
}

// begin marks the start of a change that will journal records and then hand
// the paths it wrote to Touch; the function it returns marks its end. Until
// then no commit trims the records the change journals (see active). Call it
// under the space lock, after SwitchTo.
func (c *committer) begin() (end func()) {
	if c == nil {
		return func() {}
	}
	c.mu.Lock()
	var mark uint64
	if c.j != nil {
		mark = c.j.lastSeq()
		c.active[mark]++
	}
	c.depth++
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		if c.j != nil {
			if c.active[mark]--; c.active[mark] <= 0 {
				delete(c.active, mark)
			}
		}
		c.depth--
		released := c.depth == 0 && c.releaseLocked()
		c.mu.Unlock()
		if released {
			c.wakeUp()
		}
	}
}

// releaseLocked hands what the changes held to the commit (under mu), and
// reports whether there was anything.
func (c *committer) releaseLocked() bool {
	if len(c.heldPaths) == 0 {
		return false
	}
	now := time.Now()
	if len(c.paths) == 0 {
		c.first = now
	}
	for p, by := range c.heldPaths {
		c.paths[p] = by
		if b, ok := c.heldRevs[p]; ok {
			c.revs[p] = b
		} else {
			delete(c.revs, p)
		}
	}
	c.last = now
	c.heldPaths, c.heldRevs = map[string]Author{}, map[string]gitstore.Blob{}
	return true
}

// touchLocked notes that by changed p (under mu): with b, to the content b
// gives; without, to what the work tree will have. While a change runs it is
// held for the end of the change.
func (c *committer) touchLocked(by Author, p string, b *gitstore.Blob) {
	paths, revs := c.paths, c.revs
	if c.depth > 0 {
		paths, revs = c.heldPaths, c.heldRevs
	} else if len(c.paths) == 0 {
		c.first = time.Now()
	}
	paths[p] = by
	if b != nil {
		revs[p] = *b
	} else {
		delete(revs, p)
	}
	if c.depth == 0 {
		c.last = time.Now()
	}
}

func (c *committer) wakeUp() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// covers reports whether the change running has applied the content whose
// blob id is rev to p, so that touching p again is not needed (it would only
// make the commit read the work tree for it).
func (c *committer) covers(p, rev string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.heldRevs[p]
	return ok && c.depth > 0 && b.ID == rev
}

// pendingRev is the blob id a pending path is to be committed with; known is
// false when it has none (it is staged from the work tree). A known "" is a
// path to be committed as gone.
func (c *committer) pendingRev(p string) (id string, known bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if b, ok := c.heldRevs[p]; ok {
		return b.ID, true
	}
	if _, held := c.heldPaths[p]; held {
		return "", false
	}
	b, ok := c.revs[p]
	return b.ID, ok
}

// gone reports whether the change running has applied the removal of p (the
// old name of a page it moved), so that touching p again is not needed.
func (c *committer) gone(p string) bool { return c.covers(p, "") }

// FlushChange commits now what is pending, with what the change running has
// done so far: for a change that needs a commit of its own first (a repair of
// the way to a folder, settingspath.go).
func (c *committer) FlushChange() error {
	c.mu.Lock()
	c.releaseLocked()
	c.mu.Unlock()
	return c.commit()
}

// Touch records that by changed these files (relative, slash-separated). A
// moved file is two paths: the old and the new. It also drops the content a
// transaction recorded for them: a path with recorded content is committed
// with that content, without looking at the work tree, so not even whether it
// is still there. A change that deletes or moves such a file must touch its
// path, or the commit puts it back.
func (c *committer) Touch(by Author, paths ...string) {
	if len(paths) == 0 {
		return
	}
	c.mu.Lock()
	for _, p := range paths {
		c.touchLocked(by, p, nil)
	}
	held := c.depth > 0
	c.mu.Unlock()
	if !held {
		c.wakeUp()
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
		if _, held := c.heldPaths[p]; !held && strings.HasPrefix(p, dir+"/") {
			out = append(out, p)
		}
	}
	for p := range c.heldPaths {
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
	taken, takenRevs := c.paths, c.revs
	c.paths, c.revs = map[string]Author{}, map[string]gitstore.Blob{}
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
		var known map[string]gitstore.Blob
		for _, p := range grp.paths {
			if b, ok := takenRevs[p]; ok {
				if known == nil {
					known = map[string]gitstore.Blob{}
				}
				known[p] = b
			}
		}
		made, err := c.git.commitPaths(grp.id, grp.via, grp.paths, known)
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
				if b, ok := takenRevs[p]; ok {
					c.revs[p] = b
				}
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
		// A failed path that a transaction wrote keeps the content the
		// server wrote: read back now, before the trim drops its frame, and
		// journaled again with it, so the retry (or the next start) commits
		// that and not what the work tree has by then.
		keepBlob := map[string][]byte{}
		for _, p := range failed {
			if b, ok := takenRevs[p]; ok && b.Data != nil {
				if data, err := b.Data(); err == nil && gitstore.BlobID(data) == b.ID {
					keepBlob[p] = data
				}
			}
		}
		c.mu.Lock()
		var retx []retainedTx
		for _, p := range failed {
			by, ok := c.paths[p]
			if !ok {
				continue
			}
			cur, recorded := c.revs[p]
			if recorded && cur.ID != takenRevs[p].ID {
				continue // a newer change of the path: its records are kept
			}
			f := txFile{path: p}
			switch data, kept := keepBlob[p]; {
			case recorded && cur.ID == "":
				// A removal (a deletion, the old name of a move).
			case recorded && kept:
				f.rev, f.data = cur.ID, data
				c.revs[p] = gitstore.Blob{ID: cur.ID, Data: func() ([]byte, error) { return data, nil }}
			case recorded:
				f.rev, f.ref = cur.ID, true // its content is in Git, or the file
			default:
				// Staged from the work tree: what the file is now, by
				// reference, or gone.
				if f.rev = revOf(filepath.Join(c.git.dir, filepath.FromSlash(p))); f.rev != "" {
					f.ref = true
				}
			}
			retx = append(retx, retainedTx{by: by, file: f})
		}
		err := c.j.trim(upTo, nil, retx...)
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
	return c.j.trim(c.j.lastSeq(), nil)
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
