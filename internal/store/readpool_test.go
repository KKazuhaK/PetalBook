package store_test

import (
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

// On SQLite the reads outside a transaction run on read-only connections: one
// does not wait for a write transaction that is open, it reads what was
// committed before it began, and a write followed by a read sees the write.
func TestReadsOutsideATransactionDoNotWaitForTheWriter(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "A", "alice", "member")
		held := make(chan struct{})
		release := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- st.Tx(ctx, func(q store.Q) error {
				if err := store.SetRole(ctx, q, "A", "admin"); err != nil {
					return err
				}
				close(held)
				<-release
				return nil
			})
		}()
		<-held
		read := make(chan *store.User, 1)
		go func() {
			u, err := store.GetUserByUsername(ctx, st.Q(), "alice")
			if err != nil {
				t.Error(err)
			}
			read <- u
		}()
		select {
		case u := <-read:
			if u == nil || u.Role != "member" {
				t.Errorf("read the open transaction's write: %+v", u)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a read waited for the open write transaction")
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		// Committed: the next read sees it.
		if u, err := store.GetUserByUsername(ctx, st.Q(), "alice"); err != nil || u.Role != "admin" {
			t.Fatalf("%+v %v", u, err)
		}
		// A write on Q, then a read on Q.
		if err := store.SetRole(ctx, st.Q(), "A", "member"); err != nil {
			t.Fatal(err)
		}
		if u, err := store.GetUserByUsername(ctx, st.Q(), "alice"); err != nil || u.Role != "member" {
			t.Fatalf("a read after a write on Q: %+v %v", u, err)
		}
	})
}

// The read pool cannot write: a statement that is not a plain SELECT goes to the
// writer even when it is asked for rows.
func TestStatementsThatWriteGoToTheWriter(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		var id string
		if err := st.Q().QueryRow(ctx, `INSERT INTO site_settings(key, value, updated) VALUES ('k', 'v', 0) RETURNING key`).Scan(&id); err != nil || id != "k" {
			t.Fatalf("%q %v", id, err)
		}
		var fk int
		if err := st.Q().QueryRow(ctx, `PRAGMA foreign_keys`).Scan(&fk); st.Dialect() == store.SQLite && (err != nil || fk != 1) {
			t.Fatalf("%d %v", fk, err)
		}
	})
}
