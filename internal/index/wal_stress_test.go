package index

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestWALAndWritesUnderContinuousReaders writes pages without a pause while
// 1, 6 and 24 readers list and count without a pause either (a board that
// polls), and holds the index to two bounds: the write-ahead log never grows
// past walBound, however the reads overlap, and no write waits long, since
// writes have a connection of their own and never wait behind a read. It runs
// for a while, so only when asked for:
//
//	PETALBOOK_TIMING=1 go test -run TestWALAndWritesUnderContinuousReaders -v ./internal/index/
func TestWALAndWritesUnderContinuousReaders(t *testing.T) {
	if os.Getenv("PETALBOOK_TIMING") == "" {
		t.Skip("set PETALBOOK_TIMING=1 to measure")
	}
	// The log is checkpointed and cut when it passes walCheckpointAt; what it
	// may still grow by is what the writers add while the reads under way end.
	walBound := 3 * walCheckpointAt
	maxWrite := 150 * time.Millisecond
	if raceDetector {
		maxWrite = time.Second
	}
	for _, readers := range []int{1, 6, 24} {
		t.Run(fmt.Sprintf("%d readers", readers), func(t *testing.T) {
			// The slowest write is a time, and the load of a busy machine only
			// adds to it: once, with 24 readers and a load average of 5.5, one
			// write took 167 ms. So when the slowest write of a series is over
			// the bound, the series is run again after a pause, twice at most,
			// and the best series (the least slowest write) is what is held to
			// it: a write that really waits behind the reads does so in every
			// series and still fails. The log and the waits for a connection
			// do not depend on the load: every series is held to those.
			best := time.Duration(-1)
			for series := 0; series < 3; series++ {
				if series > 0 {
					time.Sleep(3 * time.Second)
				}
				st := stressWAL(t, 3000, readers, 8*time.Second)
				t.Logf("%2d readers: WAL at most %5.1f MB, %d writes (slowest %v, waits for a connection %d), %d reads (slowest %v), %d checkpoints",
					readers, float64(st.wal)/(1<<20), st.writes, st.slowest, st.connWaits, st.reads, st.slowestRead, st.checkpoints)
				if st.wal > walBound {
					t.Errorf("the WAL grew to %d bytes, more than %d", st.wal, walBound)
				}
				if st.connWaits != 0 {
					t.Errorf("writes waited %d times for a connection", st.connWaits)
				}
				if st.writes == 0 || st.reads == 0 {
					t.Errorf("%d writes and %d reads", st.writes, st.reads)
				}
				if best < 0 || st.slowest < best {
					best = st.slowest
				}
				if best <= maxWrite || t.Failed() {
					break
				}
			}
			if best > maxWrite {
				t.Errorf("the slowest write took %v in the best of three series, more than %v", best, maxWrite)
			}
		})
	}
}

// TestWALIsCutWhileReadersOverlap is the short form of the test above, run
// every time, with a smaller space and a smaller log: with readers that always
// overlap, so that some read is under way at every moment, the log is still
// checkpointed and cut.
func TestWALIsCutWhileReadersOverlap(t *testing.T) {
	old := walCheckpointAt
	walCheckpointAt = 1 << 20
	t.Cleanup(func() { walCheckpointAt = old })
	st := stressWAL(t, 500, 6, 2*time.Second)
	t.Logf("WAL at most %.1f MB, %d writes (slowest %v), %d reads, %d checkpoints",
		float64(st.wal)/(1<<20), st.writes, st.slowest, st.reads, st.checkpoints)
	if st.wal > 4*walCheckpointAt {
		t.Errorf("the WAL grew to %d bytes", st.wal)
	}
	if st.checkpoints == 0 {
		t.Error("the WAL was never checkpointed")
	}
	if st.connWaits != 0 {
		t.Errorf("writes waited %d times for a connection", st.connWaits)
	}
}

type walStats struct {
	wal           int64
	writes, reads int64
	connWaits     int64
	checkpoints   int64
	// slowest is the longest write, slowestRead the longest read.
	slowest, slowestRead time.Duration
}

// stressWAL indexes this many pages, then runs two writers that refresh pages
// and readers that list and count, all without a pause, for d.
func stressWAL(t *testing.T, pages, readers int, d time.Duration) walStats {
	t.Helper()
	x := openIndex(t)
	all := newMem()
	srcs := []*memSource{newMem(), newMem()}
	statuses := []string{"idea", "draft", "review", "done"}
	content := func(i, v int) string {
		return fmt.Sprintf("---\nid: W%09d\ntitle: page %d v%d\nstatus: %s\npoints: %d\ntags: [t%d]\n---\nbody of page %d, version %d, 正文\n",
			i, i, v, statuses[(i+v)%4], (i*7+v)%100, i%20, i, v)
	}
	for i := range pages {
		rel := fmt.Sprintf("w/g%d/%04d.md", i%5, i)
		all.put(rel, content(i, 0))
		srcs[i%2].files[rel] = all.files[rel]
	}
	syncAll(t, x, all, false)
	before := x.checkpoints.Load()
	base := clock

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var st walStats
	var mu sync.Mutex
	for w, src := range srcs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for v := 1; ; v++ {
				select {
				case <-stop:
					return
				default:
				}
				var paths []string
				for k := range 4 {
					i := ((v*4+k)*2 + w) % pages
					rel := fmt.Sprintf("w/g%d/%04d.md", i%5, i)
					// Not put: the clock it moves is shared by both writers.
					src.files[rel] = memFile{[]byte(content(i, v)), base + int64(v)*int64(time.Second)}
					paths = append(paths, rel)
				}
				start := time.Now()
				if _, err := x.Refresh(src, paths, nil); err != nil {
					t.Error(err)
					return
				}
				took := time.Since(start)
				mu.Lock()
				st.writes++
				st.slowest = max(st.slowest, took)
				mu.Unlock()
			}
		}()
	}
	var reads atomic.Int64
	for r := range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			folder := "w"
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				var err error
				start := time.Now()
				switch (i + r) % 3 {
				case 0:
					_, err = x.List(Query{Folder: &folder, Recursive: true, Sort: "prop:status", Limit: 50, Props: true,
						Filters: []Filter{{Key: "status", Op: "in", Values: []any{"draft", "done"}}}})
				case 1:
					_, err = x.countGroups(GroupQuery{Folder: &folder, Recursive: true, Key: "status"})
				default:
					_, err = x.List(Query{Folder: &folder, Recursive: true, Limit: 50})
				}
				if err != nil {
					t.Error(err)
					return
				}
				took := time.Since(start)
				reads.Add(1)
				mu.Lock()
				st.slowestRead = max(st.slowestRead, took)
				mu.Unlock()
			}
		}()
	}
	// The size of the log, looked at every few milliseconds.
	wg.Add(1)
	go func() {
		defer wg.Done()
		tick := time.NewTicker(2 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			if fi, err := os.Stat(x.path + "-wal"); err == nil {
				mu.Lock()
				st.wal = max(st.wal, fi.Size())
				mu.Unlock()
			}
		}
	}()
	time.Sleep(d)
	close(stop)
	wg.Wait()
	st.reads = reads.Load()
	st.connWaits = x.wdb.Stats().WaitCount
	st.checkpoints = x.checkpoints.Load() - before
	return st
}
