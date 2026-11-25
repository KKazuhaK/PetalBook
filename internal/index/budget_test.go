package index

import (
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"
)

func bigSource(n int) *memSource {
	src := newMem()
	for i := 0; i < n; i++ {
		src.put(fmt.Sprintf("f/p%d.md", i), pageFile(fmt.Sprintf("%010X", 0x700000000+i), fmt.Sprintf("Page %d", i), "", nil, "", "body\n"))
	}
	return src
}

func cacheOf(t *testing.T, q interface {
	QueryRow(string, ...any) *sql.Row
}) int64 {
	t.Helper()
	var n int64
	if err := q.QueryRow(`PRAGMA cache_size`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A sync that reads many files runs with the large writer cache, and the cache
// is small again once it is done; a sync of a few files never raises it.
func TestBigSyncRaisesTheWriterCacheAndLowersItAfter(t *testing.T) {
	x := openIndex(t)
	x.bigFiles = 3
	if got := cacheOf(t, x.wdb); got != writeCacheKiB {
		t.Fatalf("writer cache at open %d, want %d", got, writeCacheKiB)
	}
	var during []int64
	x.bigSyncHook = func(tx *sql.Tx) { during = append(during, cacheOf(t, tx)) }
	src := bigSource(5)
	syncAll(t, x, src, false)
	if len(during) != 1 || during[0] != bigWriteCacheKiB {
		t.Fatalf("cache during the big sync %v, want [%d]", during, bigWriteCacheKiB)
	}
	if got := cacheOf(t, x.wdb); got != writeCacheKiB {
		t.Fatalf("writer cache after %d, want %d", got, writeCacheKiB)
	}
	// Full: big whatever the number of files.
	syncAll(t, x, src, true)
	if len(during) != 2 || during[1] != bigWriteCacheKiB {
		t.Fatalf("cache during the full sync %v", during)
	}
	src.put("f/new.md", pageFile("7FFFFFFFF0", "New", "", nil, "", "x\n"))
	syncAll(t, x, src, false)
	if len(during) != 2 {
		t.Fatalf("an incremental sync of one file was big: %v", during)
	}
	if got := cacheOf(t, x.wdb); got != writeCacheKiB {
		t.Fatalf("writer cache after the incremental sync %d", got)
	}
}

// At most as many big syncs run at once as there are slots, across indexes; a
// sync of a few files does not take one and is not held up.
func TestBigSyncsWaitForASlotAndSmallOnesPass(t *testing.T) {
	slots := NewSyncSlots(1)
	mk := func() *Index {
		x := openIndex(t)
		x.bigFiles, x.slots = 3, slots
		return x
	}
	a, b, c := mk(), mk(), mk()
	cs := bigSource(5)
	syncAll(t, c, cs, false) // c is indexed; its next syncs are small
	entered, release := make(chan struct{}), make(chan struct{})
	a.bigSyncHook = func(*sql.Tx) { close(entered); <-release }
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); a.Sync(bigSource(5), false) }()
	<-entered
	bDone := make(chan struct{})
	go func() { defer wg.Done(); b.Sync(bigSource(5), true); close(bDone) }()
	for slots.Waiting() == 0 {
		time.Sleep(time.Millisecond)
	}
	// An incremental sync of another index, and a write, go on meanwhile.
	cs.put("f/new.md", pageFile("7FFFFFFFF1", "New", "", nil, "", "x\n"))
	if st := syncAll(t, c, cs, false); st.Added != 1 {
		t.Fatalf("incremental sync: %+v", st)
	}
	select {
	case <-bDone:
		t.Fatal("a second big sync ran while the only slot was taken")
	default:
	}
	// b waits without holding its write lock: its index can be written.
	b.wmu.Lock()
	b.wmu.Unlock()
	close(release)
	wg.Wait()
	if n, _ := b.Count(); n != 5 {
		t.Fatalf("b has %d pages after its sync", n)
	}
	if slots.Waiting() != 0 || len(slots.ch) != 0 {
		t.Fatalf("slots left taken: waiting %d, held %d", slots.Waiting(), len(slots.ch))
	}
}

// Read connections close once idle, and the estimated cost follows the
// connections that are open.
func TestReadConnectionsCloseWhenIdleAndTheCostFollows(t *testing.T) {
	x := openIndex(t)
	syncAll(t, x, bigSource(50), false)
	x.db.SetConnMaxIdleTime(20 * time.Millisecond)
	x.aux.SetConnMaxIdleTime(20 * time.Millisecond)
	if x.db.Stats().MaxOpenConnections != readConns || x.aux.Stats().MaxOpenConnections != auxConns {
		t.Fatalf("pools %d and %d", x.db.Stats().MaxOpenConnections, x.aux.Stats().MaxOpenConnections)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range readConns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			tx, err := x.db.Begin()
			if err != nil {
				t.Error(err)
				return
			}
			defer tx.Rollback()
			var n int
			tx.QueryRow(`SELECT count(*) FROM pages`).Scan(&n)
			time.Sleep(10 * time.Millisecond)
		}()
	}
	close(start)
	wg.Wait()
	busy := x.MemoryCost()
	if x.db.Stats().OpenConnections < 2 {
		t.Fatalf("open read connections %d", x.db.Stats().OpenConnections)
	}
	deadline := time.Now().Add(5 * time.Second)
	for x.db.Stats().OpenConnections > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := x.db.Stats().OpenConnections; n != 0 {
		t.Fatalf("idle read connections still open: %d", n)
	}
	if idle := x.MemoryCost(); idle >= busy || idle <= 0 {
		t.Fatalf("cost idle %d, busy %d", idle, busy)
	}
}
