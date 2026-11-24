package spaces

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/index"
	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

// The index of an evicted space and its restore (3.1 with 4.1): an index that
// cannot answer index-only reads is never opened for them, and an index-only
// handle never gets in the way of the full space that restores it.

func (f *rm) indexPath(id string) string {
	_, state := f.m.paths(id)
	return filepath.Join(state, "index.db")
}

func indexDB(t *testing.T, p string, ro bool) *sql.DB {
	t.Helper()
	dsn := "file:" + p + "?_pragma=busy_timeout(10000)"
	if ro {
		dsn += "&mode=ro"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	return db
}

func indexVersion(t *testing.T, p string) int {
	t.Helper()
	db := indexDB(t, p, true)
	defer db.Close()
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func indexMeta(t *testing.T, p, key string) string {
	t.Helper()
	db := indexDB(t, p, true)
	defer db.Close()
	var v string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v); err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	return v
}

// An evicted space whose index is of another schema version (an upgrade came
// between the eviction and now) or is missing is restored, not opened
// index-only: Open would rebuild it empty, with no files to fill it, and every
// list would answer empty. The old index is left as it is until the restore
// has the files.
func TestAnEvictedSpaceWhoseIndexCannotAnswerIsRestoredNotEmptied(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spoil func(t *testing.T, p string)
		check func(t *testing.T, p string)
	}{
		{"another version", func(t *testing.T, p string) {
			db := indexDB(t, p, false)
			defer db.Close()
			if _, err := db.Exec(`PRAGMA user_version = 9`); err != nil {
				t.Fatal(err)
			}
		}, func(t *testing.T, p string) {
			if v := indexVersion(t, p); v != 9 {
				t.Fatalf("the index of another version was changed before the restore (version %d)", v)
			}
		}},
		{"missing", func(t *testing.T, p string) {
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
		}, func(t *testing.T, p string) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storetest.Each(t, func(t *testing.T, st *store.Store) {
				f := remoteManager(t, st)
				remote := filterBare(t)
				seedRemote(t, remote, 1)
				in, h := f.connected("Notes", remote)
				before := pageIDs(t, h.Space)
				h.Release()
				f.idle(72 * time.Hour)
				if err := f.m.Evict(ctx, in.ID, EvictOptions{}); err != nil {
					t.Fatal(err)
				}
				tc.spoil(t, f.indexPath(in.ID))

				old := RestoreWait
				RestoreWait = 100 * time.Millisecond
				defer func() { RestoreWait = old }()
				f.r.mu.Lock()
				f.r.hold = make(chan struct{})
				hold := f.r.hold
				f.r.mu.Unlock()
				ih, err := f.m.OpenIndex(ctx, f.user, in.ID)
				var rs *RestoringError
				if !errors.As(err, &rs) {
					if err == nil {
						n := len(pageIDs(t, ih.Space))
						ih.Release()
						t.Fatalf("opened index-only (%d pages), want a restore", n)
					}
					t.Fatalf("want restoring, got %v", err)
				}
				tc.check(t, f.indexPath(in.ID))
				close(hold)
				f.r.mu.Lock()
				f.r.hold = nil
				f.r.mu.Unlock()

				deadline := time.Now().Add(10 * time.Second)
				for {
					ih, err = f.m.OpenIndex(ctx, f.user, in.ID)
					if err == nil {
						break
					}
					if !errors.As(err, &rs) || time.Now().After(deadline) {
						t.Fatalf("the restore did not go on: %v", err)
					}
					time.Sleep(20 * time.Millisecond)
				}
				defer ih.Release()
				if ih.IndexOnly {
					t.Fatal("still index-only after the restore")
				}
				if got := pageIDs(t, ih.Space); fmt.Sprint(got) != fmt.Sprint(before) {
					t.Fatalf("pages %v after the restore, were %v", got, before)
				}
				if v := indexVersion(t, f.indexPath(in.ID)); v == 9 {
					t.Fatal("the index was not rebuilt")
				}
			})
		})
	}
}

// Once a restore has closed the index-only handle of a space, nothing gets a
// new one: it would stay in the map after the full space opened the index,
// report an old head, and mark the index closed cleanly under the full space.
func TestNoIndexOnlyHandleIsGivenOnceARestoreClosedIt(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		var f *rm
		var once sync.Once
		var reached bool
		var hookErr error
		var id string
		f = remoteManager(t, st, func(o *Options) {
			o.EvictHook = func(step string) error {
				if step != "restore:closed" {
					return nil
				}
				once.Do(func() {
					reached = true
					_, release, err := f.m.acquireIndex(ctx, id)
					if err == nil {
						release()
					}
					hookErr = err
				})
				return nil
			}
		})
		remote := filterBare(t)
		seedRemote(t, remote, 1)
		in, _ := f.evictable(remote)
		id = in.ID
		if err := f.m.Evict(ctx, in.ID, EvictOptions{}); err != nil {
			t.Fatal(err)
		}
		h, err := f.m.Open(ctx, f.user, in.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer h.Release()
		if !reached {
			t.Fatal("the restore did not pass the step after closing the index-only handle")
		}
		var rs *RestoringError
		if !errors.As(hookErr, &rs) {
			t.Fatalf("an index-only handle was given after the restore closed it: %v", hookErr)
		}
		f.m.ev.mu.Lock()
		e := f.m.ev.idx[in.ID]
		f.m.ev.mu.Unlock()
		if e != nil {
			t.Fatal("an index-only handle is left beside the full space")
		}
	})
}

// An index-only handle that outlives the restore (its holder took longer than
// the restore waits) never writes that the index was closed cleanly once the
// space's directory exists: the full space has the index open then.
func TestAStaleIndexOnlyHandleNeverMarksTheIndexOfTheFullSpaceClean(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := remoteManager(t, st)
		remote := filterBare(t)
		seedRemote(t, remote, 1)
		in, _ := f.evictable(remote)
		if err := f.m.Evict(ctx, in.ID, EvictOptions{}); err != nil {
			t.Fatal(err)
		}
		old := indexCloseWait
		indexCloseWait = 50 * time.Millisecond
		defer func() { indexCloseWait = old }()
		held, release, err := f.m.acquireIndex(ctx, in.ID)
		if err != nil || !held.Evicted() {
			t.Fatalf("%v", err)
		}
		h, err := f.m.Open(ctx, f.user, in.ID)
		if err != nil {
			release()
			t.Fatal(err)
		}
		defer h.Release()
		if c := indexMeta(t, f.indexPath(in.ID), "clean"); c != "0" {
			t.Fatalf("the full space is open and the index says clean=%q", c)
		}
		release() // the stale handle closes now
		if c := indexMeta(t, f.indexPath(in.ID), "clean"); c != "0" {
			t.Fatalf("a stale index-only handle marked the index of the open full space clean (clean=%q)", c)
		}
		if h, err := index.StoredHead(f.indexPath(in.ID)); err != nil || h != "" {
			t.Fatalf("the index of an open space has a stored head %q (%v)", h, err)
		}
	})
}
