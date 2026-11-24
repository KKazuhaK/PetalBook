package assetrepo

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/assetcache"
	"github.com/KKazuhaK/PetalBook/internal/gitstore"
)

// PETALBOOK_MEASURE=20000 go test ./internal/assetrepo -run Measure -v
// measures what an attachment repository costs: the table (build time and
// memory), a lookup, an upload, a read from the cache and a fetch by id.
func TestMeasureAttachmentRepository(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("PETALBOOK_MEASURE"))
	if n == 0 {
		t.Skip("set PETALBOOK_MEASURE to the number of files")
	}
	ctx := context.Background()
	remote := filepath.Join(t.TempDir(), "remote.git")
	os.MkdirAll(remote, 0o755)
	g := func(dir string, args ...string) {
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	g(remote, "init", "--bare", "-q", "-b", "main")
	g(remote, "config", "uploadpack.allowFilter", "true")
	g(remote, "config", "uploadpack.allowAnySHA1InWant", "true")
	// n files of 100 KiB in n/10 folders, made with fast-import.
	var in bytes.Buffer
	in.WriteString("commit refs/heads/main\ncommitter T <t@x> 0 +0000\ndata 1\nx\n")
	body := bytes.Repeat([]byte("0123456789abcdef"), 6400)
	for i := 0; i < n; i++ {
		fmt.Fprintf(&in, "M 100644 inline p%05d/f%d-%08x.bin\ndata %d\n", i/10, i, i, len(body))
		in.Write(body)
		in.WriteString("\n")
	}
	c := exec.Command("git", "-C", remote, "fast-import", "--quiet")
	c.Stdin = &in
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	cache, _ := assetcache.Open(assetcache.Options{Dir: t.TempDir(), Budget: func() int64 { return 1 << 40 }})
	dir := filepath.Join(t.TempDir(), "meta")
	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	start := time.Now()
	r, err := Open(ctx, Options{Space: "s", Dir: dir, URL: "file://" + remote, Cache: cache, AllowUnsafe: true, FetchFresh: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Refresh(ctx, true); err != nil {
		t.Fatal(err)
	}
	fetched := time.Since(start)
	r.Close()
	start = time.Now()
	r2, err := Open(ctx, Options{Space: "s", Dir: dir, URL: "file://" + remote, Cache: cache, AllowUnsafe: true})
	if err != nil {
		t.Fatal(err)
	}
	opened := time.Since(start)
	runtime.GC()
	runtime.ReadMemStats(&m1)
	du := func(p string) int64 {
		out, _ := exec.Command("du", "-sk", p).Output()
		k, _ := strconv.Atoi(string(bytes.Fields(out)[0]))
		return int64(k) * 1024
	}
	rel := fmt.Sprintf("p%05d/f%d-%08x.bin", 0, 0, 0)
	start = time.Now()
	for i := 0; i < 100000; i++ {
		r2.Lookup(rel)
	}
	lookup := time.Since(start) / 100000
	start = time.Now()
	f, info, err := r2.Open(ctx, rel)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	fetch := time.Since(start)
	start = time.Now()
	f, _, _ = r2.Open(ctx, rel)
	f.Close()
	cached := time.Since(start)
	svc := gitstore.Identity{Name: "S", Email: "s@x"}
	start = time.Now()
	if err := r2.Put(ctx, []File{{Rel: "new/up.bin", Data: body}}, svc, svc, "Add"); err != nil {
		t.Fatal(err)
	}
	upload := time.Since(start)
	t.Logf("files %d: first fetch of the commits and trees + table %v; reopen from disk (table) %v; table memory %.1f MB; meta repository on disk %.1f MB; remote %.1f MB",
		n, fetched, opened, float64(m1.HeapAlloc-m0.HeapAlloc)/1e6, float64(du(dir))/1e6, float64(du(remote))/1e6)
	t.Logf("lookup %v; read of a file not cached (fetch by id, %d bytes) %v; read from the cache %v; upload of 100 KiB (hash, trees, commit, push, refresh) %v", lookup, info.Size, fetch, cached, upload)
}
