package space

import (
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/KazuhaHub/PetalBook/internal/gitstore"
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
type committer struct {
	git  *gitRepo
	idle time.Duration
	max  time.Duration
	log  *slog.Logger

	mu    sync.Mutex
	paths map[string]bool // changed since the last commit
	first time.Time       // first uncommitted write
	last  time.Time       // latest write

	j     *journal   // what is pending, on disk, so a crash does not lose it
	runMu sync.Mutex // one git run at a time

	wake chan struct{}
	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// newCommitter starts the committer. initial are paths recovered from an
// earlier run's journal.
func newCommitter(g *gitRepo, idle, max time.Duration, log *slog.Logger, j *journal, initial []string) *committer {
	c := &committer{git: g, idle: idle, max: max, log: log, j: j, paths: map[string]bool{},
		wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	if len(initial) > 0 {
		now := time.Now()
		c.first, c.last = now, now
		for _, p := range initial {
			c.paths[p] = true
		}
	}
	go c.loop()
	return c
}

// Record journals what is about to happen to files, durably. Call it before
// the write; if it fails, do not write.
func (c *committer) Record(recs ...jrec) error {
	if c == nil || c.j == nil {
		return nil
	}
	return c.j.append(recs...)
}

// Touch records that these files (relative, slash-separated) changed. A moved
// file is two paths: the old and the new.
func (c *committer) Touch(paths ...string) {
	if len(paths) == 0 {
		return
	}
	now := time.Now()
	c.mu.Lock()
	if len(c.paths) == 0 {
		c.first = now
	}
	for _, p := range paths {
		c.paths[p] = true
	}
	c.last = now
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
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

// commit commits what is pending. A failed commit (or a repository that is
// busy with a merge or rebase) leaves the paths pending, to be tried again
// after another quiet period. Writes that arrive while git runs are not lost:
// they are pending again and go into the next commit.
func (c *committer) commit() error {
	c.runMu.Lock()
	defer c.runMu.Unlock()
	c.mu.Lock()
	if len(c.paths) == 0 {
		c.mu.Unlock()
		return nil
	}
	taken := make([]string, 0, len(c.paths))
	for p := range c.paths {
		taken = append(taken, p)
	}
	sort.Strings(taken)
	c.paths = map[string]bool{}
	c.mu.Unlock()

	made, err := c.git.commitPaths(taken)
	// Paths Git could not stage are left out for good: kept pending they would
	// fail this commit again, and every commit after it. The files are still
	// where they are; a person has to look.
	var skipped *gitstore.SkippedError
	if errors.As(err, &skipped) {
		c.log.Error("some files could not be committed and are left out", "paths", skipped.Paths)
		err = nil
	}
	if err != nil {
		now := time.Now()
		c.mu.Lock()
		if len(c.paths) == 0 {
			c.first = now
		}
		for _, p := range taken {
			c.paths[p] = true
		}
		c.last = now
		c.mu.Unlock()
		return err
	}
	if made {
		c.log.Info("committed changes", "files", len(taken))
	}
	// Everything journaled so far is in Git now, except what was written
	// during the commit, which is pending again.
	if c.j != nil {
		c.mu.Lock()
		pending := make([]string, 0, len(c.paths))
		for p := range c.paths {
			pending = append(pending, p)
		}
		err := c.j.rewrite(pending)
		c.mu.Unlock()
		if err != nil {
			c.log.Error("could not trim the commit journal", "error", err)
		}
	}
	return nil
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
