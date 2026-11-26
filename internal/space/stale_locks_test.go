package space

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// leaveLock puts the lock file rel of the Git directory where a git killed
// long ago would have left it.
func leaveLock(t *testing.T, root, rel string, age time.Duration) string {
	t.Helper()
	p := filepath.Join(root, ".git", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-age)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	return p
}

// A crash (an out-of-memory kill, a power loss) or a kill while a fetch or
// a push writes a ref leaves git's lock files behind. A space opened by the
// server that holds the data directory removes the old ones, says so, and
// takes changes again; it no longer waits for an administrator.
func TestOpenRemovesStaleGitLocks(t *testing.T) {
	t.Parallel()
	for _, rel := range []string{"HEAD.lock", "index.lock", "refs/heads/main.lock", "refs/remotes/origin/main.lock", "packed-refs.lock", "config.lock"} {
		t.Run(strings.ReplaceAll(rel, "/", "_"), func(t *testing.T) {
			t.Parallel()
			f := newRemoteSpace(t)
			a := mustCreate(t, f.s, "", meta("title", "A"), "a1\n")
			f.s.Close()
			lock := leaveLock(t, f.root, rel, 2*time.Minute)
			var logs logBuffer
			o := f.options()
			o.Status = RemoteStatus{LastGood: f.tip()}
			f.s = openAt(t, f.root, func(opt *Options) {
				opt.Committer = "Service <svc@example.test>"
				opt.Author = "Ann <ann@example.test>"
				opt.Remote = &o
				opt.ClearStaleGitLocks = true
				opt.Logger = slog.New(slog.NewTextHandler(&logs, nil))
			})
			if _, err := os.Stat(lock); err == nil {
				t.Fatalf("%s is still there", rel)
			}
			if !strings.Contains(logs.String(), rel) {
				t.Errorf("the removal of %s was not logged:\n%s", rel, logs.String())
			}
			if _, err := f.s.Save(Author{}, a.ID, a.Rev, meta("title", "A"), "a2\n"); err != nil {
				t.Fatalf("the space refuses changes: %v", err)
			}
			if h := git(t, f.root, "rev-parse", "HEAD"); h != f.tip() {
				t.Errorf("HEAD %s, the remote %s", h, f.tip())
			}
		})
	}
}

// A lock younger than a git command can be is left alone, and so is every
// lock when the caller does not say that nothing else uses the repository.
func TestOpenLeavesYoungOrUnclaimedLocks(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		age   time.Duration
		clear bool
	}{{"young", 0, true}, {"not claimed", 2 * time.Minute, false}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			root := filepath.Join(t.TempDir(), "space")
			openAt(t, root).Close()
			lock := leaveLock(t, root, "refs/heads/other.lock", c.age)
			openAt(t, root, func(o *Options) { o.ClearStaleGitLocks = c.clear })
			if _, err := os.Stat(lock); err != nil {
				t.Errorf("the lock was removed: %v", err)
			}
		})
	}
}
