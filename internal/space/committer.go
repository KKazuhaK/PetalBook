package space

import (
	"errors"
	"log/slog"
	"strings"
	"sync"
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
// commit. When a path is changed by one person and then another before the
// commit, it is the second person's: a commit cannot split a file, and the
// file as it stands is their version of it. With one author, the common case,
// there is one group and one commit, as if authors did not exist.
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
	runMu sync.Mutex // one git run at a time

	wake chan struct{}
	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// newCommitter starts the committer. initial are paths recovered from an
// earlier run's journal.
func newCommitter(g *gitRepo, idle, max time.Duration, log *slog.Logger, j *journal, initial map[string]Author) *committer {
	c := &committer{git: g, idle: idle, max: max, log: log, j: j, paths: map[string]Author{},
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

// Record journals what is about to happen to files, durably, as a change by
// by. Call it before the write; if it fails, do not write.
func (c *committer) Record(by Author, recs ...jrec) error {
	if c == nil || c.j == nil {
		return nil
	}
	for i := range recs {
		recs[i].AuthorName, recs[i].AuthorEmail = by.Name, by.Email
	}
	return c.j.append(recs...)
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
	c.mu.Unlock()

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
		made, err := c.git.commitPaths(grp.id, grp.paths)
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
	// Everything journaled so far is in Git now, except what failed and what
	// was written during the commit, which are pending again.
	if c.j != nil {
		c.mu.Lock()
		err := c.j.rewrite(c.paths)
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
	return c.j.rewrite(nil)
}

// Flush commits pending changes immediately.
func (c *committer) Flush() error { return c.commit() }

// Close stops the background goroutine and commits what is pending.
func (c *committer) Close() error {
	c.once.Do(func() { close(c.stop) })
	<-c.done
	err := c.commit()
	if c.j != nil {
		c.j.close()
	}
	return err
}
