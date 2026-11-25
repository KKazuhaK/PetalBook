package space

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// gitLog records the subcommands git runs for a space, and can hold the
// packing's first command until released.
type gitLog struct {
	mu      sync.Mutex
	seen    []string
	holding chan struct{} // when set, "repack" waits on it
	inside  chan struct{} // closed when "repack" has started
	once    sync.Once
}

func (l *gitLog) hook(args []string) {
	l.mu.Lock()
	l.seen = append(l.seen, args[0])
	hold := l.holding
	l.mu.Unlock()
	if args[0] == "repack" && hold != nil {
		l.once.Do(func() { close(l.inside) })
		<-hold
	}
}

func (l *gitLog) count(sub string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, a := range l.seen {
		if a == sub {
			n++
		}
	}
	return n
}

func (l *gitLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.seen...)
}

func looseCount(t *testing.T, root string) int {
	t.Helper()
	out := git(t, root, "count-objects", "-v")
	for _, line := range strings.Split(out, "\n") {
		var n int
		if _, err := fmt.Sscanf(line, "count: %d", &n); err == nil {
			return n
		}
	}
	t.Fatalf("no count in %q", out)
	return 0
}

// After the threshold's worth of commits the loop packs the Git objects, not
// the commit that reached it; and the packing is not run alongside a commit:
// a commit that starts while it runs waits for it.
func TestPackingStartsAtTheThresholdAndNeverOverlapsACommit(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s := openTwoAuthors(t, root)
	if err := s.Flush(); err != nil { // the start-up files: one commit
		t.Fatal(err)
	}
	l := &gitLog{holding: make(chan struct{}), inside: make(chan struct{})}
	s.cm.git.before = l.hook
	s.cm.maintainEvery.Store(3)

	mustCreate(t, s, "", meta("title", "One"), "1\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if l.count("repack") != 0 {
		t.Fatal("packed before the threshold")
	}
	mustCreate(t, s, "", meta("title", "Two"), "2\n")
	// The commit that reaches the threshold returns without waiting for the packing.
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-l.inside:
	case <-time.After(10 * time.Second):
		t.Fatal("the packing did not start")
	}
	// A commit asked for now waits for the packing to end.
	mustCreate(t, s, "", meta("title", "Three"), "3\n")
	done := make(chan error, 1)
	commitTreesBefore := l.count("commit-tree")
	go func() { done <- s.Flush() }()
	select {
	case err := <-done:
		t.Fatalf("a commit ran while the packing was running: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if n := l.count("commit-tree"); n != commitTreesBefore {
		t.Fatalf("commit-tree ran during the packing")
	}
	close(l.holding)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	seen := l.snapshot()
	var packing []string
	for _, a := range seen {
		switch a {
		case "repack", "prune-packed", "commit-graph":
			packing = append(packing, a)
		}
	}
	if strings.Join(packing, ",") != "repack,prune-packed,commit-graph" {
		t.Fatalf("packing ran as %v (all: %v)", packing, seen)
	}
	// Everything still reads, and the objects are packed.
	if got := git(t, root, "rev-list", "--count", "HEAD"); got != "4" {
		t.Fatalf("%s commits", got)
	}
	git(t, root, "fsck", "--strict")
	if m, _ := filepath.Glob(filepath.Join(root, ".git", "objects", "pack", "*.pack")); len(m) == 0 {
		t.Fatal("no pack")
	}
}

// Closing the space packs what the commits since the last packing left loose,
// and does nothing when there were none.
func TestCloseRunsThePackingOnceWhenCommitsWereMade(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s := openTwoAuthors(t, root)
	l := &gitLog{}
	s.cm.git.before = l.hook
	mustCreate(t, s, "", meta("title", "One"), "1\n")
	if err := s.Close(); err != nil { // far below the default threshold
		t.Fatal(err)
	}
	if n := l.count("repack"); n != 1 {
		t.Fatalf("%d packings at Close, want 1", n)
	}
	if n := looseCount(t, root); n != 0 {
		t.Fatalf("%d loose objects after Close", n)
	}
	// Opening and closing again, with nothing committed, packs nothing.
	s2 := openTwoAuthors(t, root)
	l2 := &gitLog{}
	s2.cm.git.before = l2.hook
	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}
	if n := l2.count("repack"); n != 0 {
		t.Fatalf("%d packings with no commits", n)
	}
}
