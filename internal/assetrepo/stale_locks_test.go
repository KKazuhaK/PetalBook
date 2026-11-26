package assetrepo

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"petalbook.app/server/internal/gitstore"
)

// The attachment repository opened by the server that holds the data
// directory removes the lock files a crash left in it, and takes files again.
func TestOpenRemovesStaleGitLocks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	remote := remoteWith(t, 0)
	cache := newCache(t)
	dir := filepath.Join(t.TempDir(), "meta")
	open := func() *Repo {
		r, err := Open(ctx, Options{Space: "s", Dir: dir, URL: "file://" + remote, Cache: cache, AllowUnsafe: true, FetchFresh: time.Nanosecond, ClearStaleLocks: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(r.Close)
		return r
	}
	id := gitstore.Identity{Name: "T", Email: "t@example.test"}
	r := open()
	if err := r.Put(ctx, []File{{Rel: "p/a.bin", Data: []byte("a")}}, id, id, "add"); err != nil {
		t.Fatal(err)
	}
	r.Close()
	old := time.Now().Add(-2 * time.Minute)
	var locks []string
	for _, rel := range []string{"index.lock", "config.lock", "refs/heads/main.lock", "refs/remotes/origin/main.lock"} {
		p := filepath.Join(dir, ".git", filepath.FromSlash(rel))
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
		locks = append(locks, p)
	}
	r = open()
	for _, p := range locks {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s is still there", p)
		}
	}
	if err := r.Put(ctx, []File{{Rel: "p/b.bin", Data: []byte("b")}}, id, id, "add"); err != nil {
		t.Fatalf("the repository refuses files: %v", err)
	}
}
