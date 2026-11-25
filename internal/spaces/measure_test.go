package spaces

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"petalbook.app/server/internal/index"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// TestMeasureStorageOnAGeneratedCorpus prints what partial clones, eviction and
// a restore cost on a generated corpus. It is skipped unless PETALBOOK_MEASURE
// holds the number of pages (the numbers in docs/design/milestones/phase-3.md, "3.1 as built"):
//
//	PETALBOOK_MEASURE=3000 go test ./internal/spaces -run MeasureStorage -v
func TestMeasureStorageOnAGeneratedCorpus(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("PETALBOOK_MEASURE"))
	if n == 0 {
		t.Skip("PETALBOOK_MEASURE is not set")
	}
	const versions = 5
	st := storetest.Open(t, "sqlite")
	f := remoteManager(t, st)
	remote := filterBare(t)
	// The history is written with fast-import: n pages, each in `versions` versions of about 4 KB.
	var in bytes.Buffer
	mark := 0
	for v := 1; v <= versions; v++ {
		mark++
		fmt.Fprintf(&in, "commit refs/heads/main\nmark :%d\ncommitter G <g@example.test> %d +0000\ndata 3\nv%d\n", mark, 1700000000+v*60, v)
		if v > 1 {
			fmt.Fprintf(&in, "from :%d\n", mark-1)
		}
		for i := 0; i < n; i++ {
			body := pageFile(fmt.Sprintf("%010X", 0x300000000+i), fmt.Sprintf("Page %d", i), fmt.Sprintf("version %d of page %d\n", v, i)+string(bytes.Repeat([]byte("春眠不觉晓，处处闻啼鸟。夜来风雨声，花落知多少。\n"), 40)))
			fmt.Fprintf(&in, "M 100644 inline Folder%d/Page%d.md\ndata %d\n%s\n", i%50, i, len(body), body)
		}
	}
	cmd := exec.Command("git", "-C", remote, "fast-import", "--quiet")
	cmd.Stdin = &in
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	du := func(p string) int64 { n, _ := dirSize(ctx, p); return n }
	t.Logf("remote (bare): %.1f MB, %d pages, %d versions each", float64(du(remote))/1e6, n, versions)

	full := filepath.Join(t.TempDir(), "full")
	start := time.Now()
	runGit(t, t.TempDir(), "clone", "-q", "file://"+remote, full)
	t.Logf("full clone: %.1f MB (.git %.1f MB), %v", float64(du(full))/1e6, float64(du(filepath.Join(full, ".git")))/1e6, time.Since(start).Round(time.Millisecond))

	start = time.Now()
	in2, h := f.connected("Corpus", remote)
	connect := time.Since(start)
	h.Release()
	f.m.Resize(ctx, store.AllTenants(), in2.ID)
	_, state := f.m.paths(in2.ID)
	root := f.root(in2.ID)
	t.Logf("partial clone made by connecting (fetch, checkout, index): %.1f MB (.git %.1f MB, working tree %.1f MB), index %.1f MB, %v",
		float64(du(root))/1e6, float64(du(filepath.Join(root, ".git")))/1e6, float64(du(root)-du(filepath.Join(root, ".git")))/1e6, float64(du(state))/1e6, connect.Round(time.Millisecond))

	f.idle(72 * time.Hour)
	start = time.Now()
	if err := f.m.Evict(ctx, in2.ID, EvictOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Logf("eviction (hold, check against the remote, move aside, remove): %v; left on disk: index %.1f MB, clone %.1f MB", time.Since(start).Round(time.Millisecond), float64(du(state))/1e6, float64(du(root))/1e6)

	start = time.Now()
	ih, err := f.m.OpenIndex(ctx, f.user, in2.ID)
	if err != nil {
		t.Fatal(err)
	}
	res, _ := ih.Space.List(index.Query{Recursive: true, Text: "version"})
	t.Logf("a search and a list on the evicted space (index only, includes opening it): %v, %d pages found", time.Since(start).Round(time.Millisecond), len(res.Items))
	ih.Release()

	start = time.Now()
	h2, err := f.m.Open(ctx, f.user, in2.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("restore (partial clone, checkout, times put back, open): %v; clone %.1f MB", time.Since(start).Round(time.Millisecond), float64(du(root))/1e6)
	h2.Release()
	_ = store.CacheFull
}
