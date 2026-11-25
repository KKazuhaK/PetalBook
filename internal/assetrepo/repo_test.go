package assetrepo

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"petalbook.app/server/internal/assetcache"
	"petalbook.app/server/internal/gitstore"
	"petalbook.app/server/internal/space"
)

// countRunner counts the fetches, how many run at once, and holds them while
// hold is open.
type countRunner struct {
	mu                    sync.Mutex
	fetches, during, most int
	hold                  chan struct{}
}

func (c *countRunner) Run(ctx context.Context, cmd gitstore.Cmd) (string, string, error) {
	if sub, _ := gitstore.Subcommand(cmd.Args); sub == "fetch" {
		c.mu.Lock()
		c.fetches++
		c.during++
		c.most = max(c.most, c.during)
		hold := c.hold
		c.mu.Unlock()
		defer func() { c.mu.Lock(); c.during--; c.mu.Unlock() }()
		if hold != nil {
			select {
			case <-hold:
			case <-ctx.Done():
				return "", "fatal: stopped", ctx.Err()
			}
		}
	}
	return gitstore.RunReal(ctx, cmd)
}

func (c *countRunner) reset(hold chan struct{}) {
	c.mu.Lock()
	c.fetches, c.most, c.hold = 0, 0, hold
	c.mu.Unlock()
}

func (c *countRunner) counts() (fetches, most int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fetches, c.most
}

// remoteWith makes a bare repository whose main has n small files.
func remoteWith(t *testing.T, n int) string {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "remote.git")
	os.MkdirAll(remote, 0o755)
	g := func(args ...string) {
		c := exec.Command("git", append([]string{"-C", remote}, args...)...)
		c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	g("init", "--bare", "-q", "-b", "main")
	g("config", "uploadpack.allowFilter", "true")
	g("config", "uploadpack.allowAnySHA1InWant", "true")
	var in bytes.Buffer
	in.WriteString("commit refs/heads/main\ncommitter T <t@x> 0 +0000\ndata 1\nx\n")
	for i := 0; i < n; i++ {
		body := fmt.Sprintf("file %d of %s", i, remote)
		fmt.Fprintf(&in, "M 100644 inline p/f%d.bin\ndata %d\n%s\n", i, len(body), body)
	}
	c := exec.Command("git", "-C", remote, "fast-import", "--quiet")
	c.Stdin = &in
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	return remote
}

func openRepo(t *testing.T, space, remote string, cache *assetcache.Cache, run gitstore.Runner) *Repo {
	t.Helper()
	ctx := context.Background()
	r, err := Open(ctx, Options{Space: space, Dir: filepath.Join(t.TempDir(), "meta"), URL: "file://" + remote, Cache: cache, Runner: run, AllowUnsafe: true, FetchFresh: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	if _, _, err := r.Refresh(ctx, true); err != nil {
		t.Fatal(err)
	}
	return r
}

func newCache(t *testing.T) *assetcache.Cache {
	t.Helper()
	c, err := assetcache.Open(assetcache.Options{Dir: t.TempDir(), Budget: func() int64 { return 1 << 30 }})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Fetches by id are bounded: a few per space, and a few more for the server.
func TestFetchesByIDAreBounded(t *testing.T) {
	run := &countRunner{}
	cache := newCache(t)
	var repos []*Repo
	for i := 0; i < 6; i++ {
		repos = append(repos, openRepo(t, fmt.Sprintf("s%d", i), remoteWith(t, 8), cache, run))
	}
	hold := make(chan struct{})
	run.reset(hold)
	var wg sync.WaitGroup
	for _, r := range repos {
		for _, id := range r.Files() {
			wg.Add(1)
			go func() { defer wg.Done(); r.fetchOne(id) }()
		}
	}
	time.Sleep(500 * time.Millisecond)
	_, most := run.counts()
	close(hold)
	wg.Wait()
	if most > fetchesOverall {
		t.Fatalf("%d fetches at once over all spaces (at most %d)", most, fetchesOverall)
	}
	// One space alone.
	r := openRepo(t, "one", remoteWith(t, 12), cache, run)
	hold = make(chan struct{})
	run.reset(hold)
	for _, id := range r.Files() {
		wg.Add(1)
		go func() { defer wg.Done(); r.fetchOne(id) }()
	}
	time.Sleep(500 * time.Millisecond)
	_, most = run.counts()
	close(hold)
	wg.Wait()
	if most > fetchesPerSpace {
		t.Fatalf("%d fetches at once for one space (at most %d)", most, fetchesPerSpace)
	}
	for _, id := range r.Files() {
		if !cache.Has("one", id) {
			t.Fatal("a fetch was lost")
		}
	}
}

// A pinned space's attachments are fetched in batches, not one by one.
func TestFetchAllFetchesInBatches(t *testing.T) {
	run := &countRunner{}
	cache := newCache(t)
	r := openRepo(t, "pinned", remoteWith(t, 30), cache, run)
	run.reset(nil)
	if err := r.FetchAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n, _ := run.counts(); n != 1 {
		t.Fatalf("%d fetches for 30 files", n)
	}
	for rel, id := range r.Files() {
		f, _, err := cache.Open("pinned", id)
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		b := new(bytes.Buffer)
		b.ReadFrom(f)
		f.Close()
		if gitstore.BlobID(b.Bytes()) != id || !strings.HasPrefix(b.String(), "file ") {
			t.Fatalf("%s: %q", rel, b.String())
		}
	}
}

// When the first file went to the repository but that could not be recorded,
// the next push tries again.
func TestUsedIsRecordedAgainAfterAFailure(t *testing.T) {
	ctx := context.Background()
	remote := remoteWith(t, 0)
	calls := 0
	r, err := Open(ctx, Options{Space: "u", Dir: filepath.Join(t.TempDir(), "meta"), URL: "file://" + remote, Cache: newCache(t), AllowUnsafe: true, FetchFresh: time.Nanosecond,
		OnUsed: func() error {
			calls++
			if calls == 1 {
				return fmt.Errorf("the database is away")
			}
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	id := gitstore.Identity{Name: "T", Email: "t@example.test"}
	for i, name := range []string{"p/a.bin", "p/b.bin", "p/c.bin"} {
		if err := r.Put(ctx, []File{{Rel: name, Data: []byte(name)}}, id, id, "add"); err != nil {
			t.Fatalf("%d: %v", i, err)
		}
	}
	if calls != 2 {
		t.Fatalf("recorded %d times", calls)
	}
}

// A command stopped by its caller (a request that went away, the server
// stopping) or by the repository being closed says nothing about the remote:
// the state stays as it was, and nothing is recorded.
func TestACancelledRefreshLeavesTheStateAlone(t *testing.T) {
	remote := remoteWith(t, 1)
	var mu sync.Mutex
	var told []space.RemoteStatus
	r, err := Open(context.Background(), Options{Space: "c", Dir: filepath.Join(t.TempDir(), "meta"), URL: "file://" + remote, Cache: newCache(t), AllowUnsafe: true, FetchFresh: time.Nanosecond,
		Status:   space.RemoteStatus{State: space.RemoteOK, LastSync: time.Now().UTC()},
		OnStatus: func(st space.RemoteStatus) { mu.Lock(); told = append(told, st); mu.Unlock() }})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	stopped, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := r.Refresh(stopped, true); err == nil {
		t.Fatal("a refresh with its context ended went through")
	}
	r.Close()
	if _, _, err := r.Refresh(context.Background(), true); err == nil {
		t.Fatal("a refresh of a closed repository went through")
	}
	mu.Lock()
	defer mu.Unlock()
	if st := r.Status(); st.State != space.RemoteOK || len(told) > 0 {
		t.Fatalf("the state is %+v; told %+v", st, told)
	}
}
