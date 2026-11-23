package index

import (
	"fmt"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestReadsWritesAndCheckpointsDoNotDeadlock runs every kind of read at once,
// more readers than the index has connections, against a writer that keeps
// the log growing past a small walCheckpointAt, so that the checkpointer waits
// for the gate again and again while reads wait for connections. Every read
// takes its connection before the gate (read.go); a read that took the gate
// first would wait for a connection held by a read that waits for the gate
// behind the checkpointer, and everything would stop. Each round must end
// within a hard limit; when one does not, the goroutines are dumped.
func TestReadsWritesAndCheckpointsDoNotDeadlock(t *testing.T) {
	old := walCheckpointAt
	walCheckpointAt = 64 << 10
	t.Cleanup(func() { walCheckpointAt = old })

	rounds, d := 12, 250*time.Millisecond
	if testing.Short() {
		rounds = 3
	}
	var checkpoints int64
	for round := range rounds {
		checkpoints += deadlockRound(t, round, d)
		if t.Failed() {
			return
		}
	}
	// Without checkpoints the test would prove nothing.
	if checkpoints < int64(rounds) {
		t.Errorf("%d checkpoints in %d rounds: the log did not pass walCheckpointAt often enough", checkpoints, rounds)
	}
}

func deadlockRound(t *testing.T, round int, d time.Duration) int64 {
	const pages, readers = 200, 40
	x, err := Open(filepath.Join(t.TempDir(), "state", "index.db"), "/space")
	if err != nil {
		t.Fatal(err)
	}
	src := newMem()
	statuses := []string{"idea", "draft", "review", "done"}
	content := func(i, v int) string {
		return fmt.Sprintf("---\nid: D%06d\ntitle: page %d v%d\nstatus: %s\ntags: [t%d]\n---\n%s\n",
			i, i, v, statuses[(i+v)%4], i%7, strings.Repeat("body text ", 40))
	}
	rel := func(i int) string { return fmt.Sprintf("d/g%d/%04d.md", i%4, i) }
	for i := range pages {
		src.put(rel(i), content(i, 0))
	}
	if _, err := x.Sync(src, false); err != nil {
		x.Close()
		t.Fatal(err)
	}
	// Checkpoints made by the Sync above do not count.
	before := x.checkpoints.Load()
	base := clock()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	fail := func(err error) {
		t.Error(err)
	}
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
			for k := range 8 {
				i := (v*8 + k) % pages
				src.files[rel(i)] = memFile{[]byte(content(i, v)), base + int64(v)*int64(time.Second)}
				paths = append(paths, rel(i))
			}
			if _, err := x.Refresh(src, paths, nil); err != nil {
				fail(err)
				return
			}
		}
	}()
	folder := "d"
	for r := range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				var err error
				switch (i + r) % 8 {
				case 0: // planned: a read transaction and aux connections
					_, err = x.List(Query{Folder: &folder, Recursive: true, Sort: "prop:status", Limit: 20,
						Filters: []Filter{{Key: "status", Op: "in", Values: []any{"draft", "done"}}}})
				case 1:
					_, err = x.Count()
				case 2: // one query
					_, err = x.List(Query{Folder: &folder, Recursive: true, Limit: 20})
				case 3:
					x.PathByID(fmt.Sprintf("D%06d", i%pages))
				case 4:
					_, err = x.Groups(GroupQuery{Folder: &folder, Recursive: true, Key: "status"})
				case 5:
					_, err = x.Tree()
				case 6:
					_, err = x.Tags()
				default:
					_, err = x.Problems()
				}
				if err != nil {
					fail(err)
					return
				}
			}
		}()
	}
	time.Sleep(d)
	close(stop)

	ended := make(chan struct{})
	go func() {
		wg.Wait()
		x.Close()
		close(ended)
	}()
	select {
	case <-ended:
	case <-time.After(20 * time.Second):
		var dump strings.Builder
		pprof.Lookup("goroutine").WriteTo(&dump, 2)
		// The index is left open: closing it would wait for ever too.
		t.Fatalf("round %d: reads, writes and the checkpointer did not end within 20s (deadlock); goroutines:\n%s", round, dump.String())
	}
	return x.checkpoints.Load() - before
}
