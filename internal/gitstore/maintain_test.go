package gitstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// looseObjects counts the objects stored one per file.
func looseObjects(t testing.TB, dir string) int {
	t.Helper()
	n := 0
	filepath.WalkDir(filepath.Join(dir, ".git", "objects"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && len(d.Name()) == 38 {
			n++
		}
		return nil
	})
	return n
}

func packFiles(t testing.TB, dir string) int {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, ".git", "objects", "pack", "*.pack"))
	return len(m)
}

// Commits are built with plumbing, so Git never packs them by itself.
// Maintain packs the loose objects, removes the loose copies, writes a commit
// graph, and every commit and file stays readable. An object that no ref
// reaches (a commit still being built) is neither packed nor removed.
func TestMaintainPacksLooseObjectsAndKeepsHistory(t *testing.T) {
	needGit(t)
	ctx := context.Background()
	dir := t.TempDir()
	r := New(Options{Dir: dir})
	if err := r.Init(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	var heads []string
	for i := 0; i < 12; i++ {
		name := fmt.Sprintf("p%d.md", i%4)
		write(t, dir, name, fmt.Sprintf("version %d\n", i))
		if _, err := r.CommitPaths(ctx, []string{name}, msg, person, svc); err != nil {
			t.Fatal(err)
		}
		heads = append(heads, r.Head(ctx))
	}
	// An object nothing refers to yet, as a commit in flight leaves behind.
	orphanDir := t.TempDir()
	write(t, orphanDir, "o.txt", "unreferenced\n")
	orphan := sh(t, dir, "hash-object", "-w", filepath.Join(orphanDir, "o.txt"))
	if n := looseObjects(t, dir); n < 24 {
		t.Fatalf("expected loose objects after 12 commits, got %d", n)
	}
	if err := r.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if n := packFiles(t, dir); n != 1 {
		t.Fatalf("%d packs after Maintain", n)
	}
	if n := looseObjects(t, dir); n != 1 {
		t.Fatalf("%d loose objects after Maintain, want only the unreferenced one", n)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "objects", orphan[:2], orphan[2:])); err != nil {
		t.Fatalf("the unreferenced object was removed: %v", err)
	}
	_, e1 := os.Stat(filepath.Join(dir, ".git", "objects", "info", "commit-graph"))
	m, _ := filepath.Glob(filepath.Join(dir, ".git", "objects", "info", "commit-graphs", "*"))
	if e1 != nil && len(m) == 0 {
		t.Fatal("no commit graph written")
	}
	sh(t, dir, "fsck", "--strict")
	for i, h := range heads {
		if got := sh(t, dir, "show", h+":"+fmt.Sprintf("p%d.md", i%4)); got != fmt.Sprintf("version %d", i) {
			t.Fatalf("commit %d: %q", i, got)
		}
	}
	if got := sh(t, dir, "rev-list", "--count", "HEAD"); got != "12" {
		t.Fatalf("%s commits", got)
	}
	// A second run with nothing new is harmless, and commits still work after.
	if err := r.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "after.md", "x\n")
	if _, err := r.CommitPaths(ctx, []string{"after.md"}, msg, person, svc); err != nil {
		t.Fatal(err)
	}
}

// The forms Maintain runs are allowed exactly; the variants that could remove
// something Git has not been told is garbage are refused.
func TestMaintainCommandFormsAreExact(t *testing.T) {
	for _, args := range [][]string{
		{"repack", "-d", "-q"},
		{"prune-packed", "-q"},
		{"commit-graph", "write", "--reachable"},
	} {
		if err := refuseDestructive(args); err != nil {
			t.Errorf("%v refused: %v", args, err)
		}
		if p := problems(Cmd{Args: args}); len(p) > 0 {
			t.Errorf("%v flagged by the test checker: %v", args, p)
		}
	}
	for _, args := range [][]string{
		{"gc"},
		{"gc", "--auto"},
		{"gc", "--prune=now"},
		{"prune"},
		{"prune", "--expire=now"},
		{"repack"},
		{"repack", "-a", "-d"},
		{"repack", "-A", "-d"},
		{"repack", "-a", "-d", "-q"},
		{"repack", "-d", "-q", "-a"},
		{"repack", "-ad"},
		{"repack", "-d", "-q", "-k"},
		{"repack", "-d", "-q", "--unpack-unreachable=now"},
		{"repack", "-d", "-q", "--cruft"},
		{"repack", "-d"},
		{"prune-packed"},
		{"prune-packed", "-n"},
		{"prune-packed", "-q", "x"},
		{"commit-graph", "write"},
		{"commit-graph", "write", "--stdin-commits"},
		{"commit-graph", "write", "--reachable", "--object-dir=/tmp/x"},
		{"commit-graph", "verify"},
		{"commit-graph", "write", "--reachable", "--split=replace"},
		{"maintenance", "run"},
		{"-c", "gc.auto=0", "repack", "-a", "-d"},
	} {
		if err := refuseDestructive(args); err == nil {
			t.Errorf("git %s was allowed", strings.Join(args, " "))
		}
	}
}
