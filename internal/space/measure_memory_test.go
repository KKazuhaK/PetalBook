//go:build linux

package space

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"petalbook.app/server/internal/index"
)

// TestMeasureMemoryOfAnOpenSpace prints what one open space costs in memory,
// what a search takes, and what a rebuild costs, on a generated space (the
// numbers in docs/design/database.md, §5.5). It is skipped
// unless PETALBOOK_MEASURE_MEM names a step, and needs Linux
// (/proc/self/smaps_rollup). Each step is run in a process of its own, since
// memory SQLite frees is kept by the process:
//
//	export PETALBOOK_MEASURE_DIR=$(mktemp -d) PETALBOOK_MEASURE_PAGES=50000
//	for s in build open rebuild; do
//		PETALBOOK_MEASURE_MEM=$s go test ./internal/space -run MeasureMemory -v -count=1
//	done
//	rm -rf $PETALBOOK_MEASURE_DIR
//
// build opens the space with no index (the first open, or one after a schema
// change); open opens it indexed and searches, one at a time and 16 at a time,
// then waits PETALBOOK_MEASURE_IDLE; rebuild rebuilds the index of the open
// space. Memory is the process's anonymous memory after a GC that gives the Go
// heap back: SQLite's caches live there. The memory map of the index is
// file-backed and is not counted.
func TestMeasureMemoryOfAnOpenSpace(t *testing.T) {
	step := os.Getenv("PETALBOOK_MEASURE_MEM")
	if step == "" {
		t.Skip("PETALBOOK_MEASURE_MEM is not set")
	}
	if _, err := os.Stat("/proc/self/smaps_rollup"); err != nil {
		t.Skip("needs /proc/self/smaps_rollup")
	}
	needGit(t)
	dir := os.Getenv("PETALBOOK_MEASURE_DIR")
	if dir == "" {
		t.Fatal("PETALBOOK_MEASURE_DIR is not set")
	}
	root, state := filepath.Join(dir, "space"), filepath.Join(dir, "state")
	words := strings.Fields("spring river flower moon night garden mountain rain wind autumn cloud snow 春眠 不觉 晓 处处 闻 啼鸟 夜来 风雨 声 花落 知 多少")
	if _, err := os.Stat(root); err != nil {
		n, _ := strconv.Atoi(os.Getenv("PETALBOOK_MEASURE_PAGES"))
		seedPages(t, root, n, words)
	}
	opt := Options{Root: root, StateDir: state, CommitIdle: time.Hour, CommitMax: 2 * time.Hour}
	base := anonMB()
	if step == "build" {
		os.RemoveAll(state)
		var sp *Space
		peak := sampling(func() {
			var err error
			if sp, err = Open(opt); err != nil {
				t.Fatal(err)
			}
		})
		t.Logf("open with no index (builds it): %v, peak +%.1f MB, after +%.1f MB", peak.took.Round(time.Millisecond), peak.max-base, anonMB()-base)
		sp.Close()
		if fi, err := os.Stat(filepath.Join(state, indexFile)); err == nil {
			t.Logf("index file: %.1f MB", float64(fi.Size())/1e6)
		}
		return
	}
	start := time.Now()
	sp, err := Open(opt)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if sp != nil {
			sp.Close()
		}
	}()
	t.Logf("open of the indexed space: %v, +%.1f MB", time.Since(start).Round(time.Millisecond), anonMB()-base)
	if step == "rebuild" {
		var ru0, ru1 syscall.Rusage
		syscall.Getrusage(syscall.RUSAGE_SELF, &ru0)
		peak := sampling(func() {
			if _, err := sp.Reindex(); err != nil {
				t.Fatal(err)
			}
		})
		syscall.Getrusage(syscall.RUSAGE_SELF, &ru1)
		cpu := time.Duration(ru1.Utime.Nano()-ru0.Utime.Nano()) + time.Duration(ru1.Stime.Nano()-ru0.Stime.Nano())
		t.Logf("full rebuild: %v, CPU %.0f%%, peak +%.1f MB, after +%.1f MB", peak.took.Round(time.Millisecond), 100*cpu.Seconds()/peak.took.Seconds(), peak.max-base, anonMB()-base)
		sp.Close()
		sp = nil
		t.Logf("after closing it: +%.1f MB (what SQLite's allocator keeps for the process)", anonMB()-base)
		return
	}
	search := func(i int) time.Duration {
		s := time.Now()
		if _, err := sp.List(index.Query{Text: words[i%len(words)], Limit: 50}); err != nil {
			t.Fatal(err)
		}
		return time.Since(s)
	}
	list := func(i int) time.Duration {
		s := time.Now()
		f := fmt.Sprintf("folder%02d", i%50)
		if _, err := sp.List(index.Query{Folder: &f, Recursive: true, Sort: "-updated", Limit: 50}); err != nil {
			t.Fatal(err)
		}
		return time.Since(s)
	}
	for name, f := range map[string]func(int) time.Duration{"search": search, "folder listing": list} {
		var seq []time.Duration
		for i := 0; i < 200; i++ {
			seq = append(seq, f(i))
		}
		t.Logf("one %s at a time: %s", name, pct(seq))
	}
	var mu sync.Mutex
	var par []time.Duration
	var wg sync.WaitGroup
	start = time.Now()
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				d := search(g*25 + i)
				mu.Lock()
				par = append(par, d)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	t.Logf("16 searches at a time: %s, %v for 400; now +%.1f MB", pct(par), time.Since(start).Round(time.Millisecond), anonMB()-base)
	if w := os.Getenv("PETALBOOK_MEASURE_IDLE"); w != "" {
		d, _ := time.ParseDuration(w)
		time.Sleep(d)
		t.Logf("after %v idle: +%.1f MB", d, anonMB()-base)
	}
}

func seedPages(t *testing.T, root string, n int, words []string) {
	if n <= 0 {
		t.Fatal("PETALBOOK_MEASURE_PAGES is not set")
	}
	for i := 0; i < n; i++ {
		dir := filepath.Join(root, fmt.Sprintf("folder%02d", i%50), fmt.Sprintf("sub%d", i%7))
		if i < 350 {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		var b strings.Builder
		fmt.Fprintf(&b, "---\nid: %010X\ntitle: Page %d %s\ntags: [t%d, %s]\nstatus: s%d\n---\n", 0x300000000+i, i, words[i%len(words)], i%40, words[(i*7)%len(words)], i%5)
		for j := 0; j < 30; j++ {
			b.WriteString(words[(i+j*j)%len(words)])
			b.WriteByte(' ')
			if j%10 == 9 {
				b.WriteString("\n\n")
			}
		}
		b.WriteString(strings.Repeat("夜来风雨声，花落知多少。The quick brown fox jumps over the lazy dog.\n", 15))
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("page%d.md", i)), []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

type sample struct {
	took time.Duration
	max  float64
}

// sampling runs f and samples the anonymous memory every 20 ms meanwhile.
func sampling(f func()) sample {
	stop := make(chan struct{})
	done := make(chan float64)
	go func() {
		m := 0.0
		t := time.NewTicker(20 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				done <- m
				return
			case <-t.C:
				m = max(m, rawAnonMB())
			}
		}
	}()
	start := time.Now()
	f()
	took := time.Since(start)
	close(stop)
	return sample{took: took, max: <-done}
}

func pct(d []time.Duration) string {
	slices.Sort(d)
	q := func(p float64) time.Duration { return d[int(p*float64(len(d)-1))].Round(10 * time.Microsecond) }
	return fmt.Sprintf("p50 %v, p90 %v, p99 %v", q(0.5), q(0.9), q(0.99))
}

// anonMB is the anonymous memory of the process after the Go heap was given back.
func anonMB() float64 {
	debug.FreeOSMemory()
	return rawAnonMB()
}

func rawAnonMB() float64 {
	f, err := os.Open("/proc/self/smaps_rollup")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "Anonymous:"); ok {
			kb, _ := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "kB")))
			return float64(kb) / 1024
		}
	}
	return 0
}
