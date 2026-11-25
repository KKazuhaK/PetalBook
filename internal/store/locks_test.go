package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

// The locks of a space, a group and a person's favourites write nothing: on
// PostgreSQL the row keeps its version (the no-op UPDATE they replace made a
// new one and a dead tuple each time), on SQLite no row changes.
func TestRowLocksWriteNothing(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Now()
		mkUser(t, st, "U1", "u1", "member")
		if err := store.InsertSpace(ctx, st.Q(), store.DefaultTenantID, "S1", "S1", "", "", now); err != nil {
			t.Fatal(err)
		}
		if err := store.InsertGroup(ctx, st.Q(), store.Group{ID: "G1", Name: "g", Source: store.GroupLocal, Created: now, Updated: now}); err != nil {
			t.Fatal(err)
		}
		locks := []struct {
			table, id string
			lock      func(q store.Q, id string) error
		}{
			{"spaces", "S1", func(q store.Q, id string) error { return store.LockSpace(ctx, q, id) }},
			{"groups", "G1", func(q store.Q, id string) error { return store.LockGroup(ctx, q, id) }},
			{"users", "U1", func(q store.Q, id string) error { return store.LockFavorites(ctx, q, id) }},
		}
		for _, l := range locks {
			version := func(q store.Q) (v string) {
				t.Helper()
				stmt := `SELECT total_changes()`
				var args []any
				if st.Dialect() == store.Postgres {
					stmt, args = `SELECT xmin::text FROM `+l.table+` WHERE id = ?`, []any{l.id}
				}
				if err := q.QueryRow(ctx, stmt, args...).Scan(&v); err != nil {
					t.Fatal(err)
				}
				return v
			}
			err := st.Tx(ctx, func(q store.Q) error {
				before := version(q)
				if err := l.lock(q, l.id); err != nil {
					return err
				}
				if after := version(q); after != before {
					t.Errorf("locking %s wrote: %s, then %s", l.table, before, after)
				}
				if err := l.lock(q, "missing"); !errors.Is(err, store.ErrNotFound) {
					t.Errorf("locking a missing row of %s: %v", l.table, err)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
	})
}

// On PostgreSQL two transactions that lock one space take turns, and the lock
// does not hold up a row that refers to the space (its foreign-key check).
func TestSpaceLockSerialisesWithoutBlockingReferences(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		if st.Dialect() != store.Postgres {
			t.Skip("SQLite has one writer: its transactions take turns anyway")
		}
		now := time.Now()
		if err := store.InsertSpace(ctx, st.Q(), store.DefaultTenantID, "S1", "S1", "", "", now); err != nil {
			t.Fatal(err)
		}
		held := make(chan struct{})
		release := make(chan struct{})
		first := make(chan error, 1)
		go func() {
			first <- st.Tx(ctx, func(q store.Q) error {
				if err := store.LockSpace(ctx, q, "S1"); err != nil {
					return err
				}
				close(held)
				<-release
				return nil
			})
		}()
		<-held
		// A row that refers to the space is written while the lock is held.
		if _, err := st.Q().Exec(ctx, `INSERT INTO space_daily(space_id) VALUES ('S1')`); err != nil {
			t.Fatal(err)
		}
		second := make(chan error, 1)
		go func() {
			second <- st.Tx(ctx, func(q store.Q) error { return store.LockSpace(ctx, q, "S1") })
		}()
		select {
		case err := <-second:
			t.Fatalf("the second lock did not wait: %v", err)
		case <-time.After(200 * time.Millisecond):
		}
		close(release)
		if err := <-first; err != nil {
			t.Fatal(err)
		}
		if err := <-second; err != nil {
			t.Fatal(err)
		}
	})
}
