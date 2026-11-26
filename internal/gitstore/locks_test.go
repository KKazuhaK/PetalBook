package gitstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A write of the index or a ref is not stopped by cancelling. Without a
// deadline of the caller's it ends after writeCap; a caller's deadline, later
// or earlier (a restore of a large space has a long one), is kept as it is.
func TestUncancelledWritesAreCapped(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, done := notCancelled(ctx)
	defer done()
	if c.Err() != nil {
		t.Fatal("cancelling stops the write")
	}
	if d, ok := c.Deadline(); !ok || time.Until(d) > writeCap {
		t.Errorf("no cap: %v %v", d, ok)
	}
	for _, within := range []time.Duration{time.Second, writeCap + 3*time.Minute} {
		parent, cancel := context.WithTimeout(context.Background(), within)
		want, _ := parent.Deadline()
		cancel() // cancelled, deadline still there
		c, done := notCancelled(parent)
		if c.Err() != nil {
			t.Errorf("%v: cancelling stops the write", within)
		}
		if d, ok := c.Deadline(); !ok || !d.Equal(want) {
			t.Errorf("%v: deadline %v, the caller's %v", within, d, want)
		}
		done()
	}
}

// The lock of a commit-graph write (Maintain) that was killed is removed with
// the others; nothing else under objects/ is touched.
func TestRemoveStaleLocksTakesTheCommitGraphChainLock(t *testing.T) {
	t.Parallel()
	gd := t.TempDir()
	old := time.Now().Add(-2 * time.Minute)
	put := func(rel string) string {
		p := filepath.Join(gd, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
		return p
	}
	chain := put("objects/info/commit-graphs/commit-graph-chain.lock")
	other := put("objects/pack/tmp.lock")
	if _, err := RemoveStaleLocks(gd, StaleLockAge, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(chain); err == nil {
		t.Error("commit-graph-chain.lock is still there")
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("a file under objects/ was removed: %v", err)
	}
}
