package index

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
)

// bigSyncFiles is the number of files to read past which a sync is big: it
// runs with the large writer cache and needs one of the server's SyncSlots. A
// rebuild (Sync with full) is always big. A sync after a few edits, a pull of
// a few changed files or the open of an indexed space reads far fewer.
const bigSyncFiles = 1000

// DefaultBigSyncs is how many big syncs run at once by default, across all the
// indexes of the process. A big sync of a 50 000-page space keeps about two
// cores busy for 15 to 35 seconds and some 150 MB of memory; two at a time
// leave a 4-core server room for its requests.
const DefaultBigSyncs = 2

// SyncSlots bounds the big syncs that run at the same time. The zero value is
// not usable; see NewSyncSlots.
type SyncSlots struct {
	ch      chan struct{}
	waiting atomic.Int64
}

// NewSyncSlots returns slots for n big syncs at once (at least one).
func NewSyncSlots(n int) *SyncSlots {
	return &SyncSlots{ch: make(chan struct{}, max(n, 1))}
}

// Waiting is the number of big syncs waiting for a slot.
func (s *SyncSlots) Waiting() int { return int(s.waiting.Load()) }

func (s *SyncSlots) acquire(ctx context.Context) error {
	select {
	case s.ch <- struct{}{}:
		return nil
	default:
	}
	s.waiting.Add(1)
	defer s.waiting.Add(-1)
	select {
	case s.ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *SyncSlots) release() { <-s.ch }

var bigSyncSlots atomic.Pointer[SyncSlots]

func init() { bigSyncSlots.Store(NewSyncSlots(DefaultBigSyncs)) }

// SetBigSyncLimit sets how many big syncs run at once across the process, for
// the indexes opened from then on (the server calls it at start-up).
func SetBigSyncLimit(n int) { bigSyncSlots.Store(NewSyncSlots(n)) }

// BigSyncsWaiting is the number of big syncs of the process waiting for a slot.
func BigSyncsWaiting() int { return bigSyncSlots.Load().Waiting() }

// What an index holds in memory, for MemoryCost; measured on a 50 000-page
// space (docs/design/database.md, §5.5). A connection with
// the schema parsed and its statements prepared holds about connBytes beside
// its page cache. A reader maps the file, so its page cache stays nearly empty
// (readerBytes is its whole cost, measured); the writer's cache fills up to its
// size (or the size of the file) after the scan of the pages that every sync
// makes. pageBytes is what one page costs in Go besides.
const (
	connBytes   = 512 << 10
	readerBytes = 512 << 10
	pageBytes   = 64
)

// MemoryCost estimates the memory the index holds now, in bytes: the writer's
// connection and cache, the read connections that are open, and a part per
// page. The memory map is file-backed (shared with the operating system's
// cache, given back under pressure) and is not counted. It runs no query: a
// file stat and the pools' counters.
func (x *Index) MemoryCost() int64 {
	var size int64
	if fi, err := os.Stat(x.path); err == nil {
		size = fi.Size()
	}
	cache := min(-x.wcache.Load()*1024, size)
	readers := int64(x.db.Stats().OpenConnections + x.aux.Stats().OpenConnections)
	return connBytes + cache + readers*readerBytes + x.pages.Load()*pageBytes
}

// setWriteCache sets the writer's cache size (KiB, negative), on the writer's
// one connection; the caller holds wmu and no transaction is open. Lowering it
// frees what the cache held beyond the new size.
func (x *Index) setWriteCache(ctx context.Context, kib int64) {
	if _, err := x.wdb.ExecContext(ctx, fmt.Sprintf(`PRAGMA cache_size = %d`, kib)); err != nil {
		return
	}
	x.wcache.Store(kib)
	if kib == writeCacheKiB {
		x.wdb.ExecContext(ctx, `PRAGMA shrink_memory`)
	}
}
