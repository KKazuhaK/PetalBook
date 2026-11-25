//go:build linux || darwin || freebsd

package spaces

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// Closing the manager frees the data directory's lock at once, even while
// another copy of the lock's descriptor is still open (a child process holds
// one between fork and exec): the next server must not be refused.
func TestCloseFreesTheDataDirLockWhileACopyOfItsDescriptorIsOpen(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		dir := filepath.Join(t.TempDir(), "data")
		srv := Options{Store: st, DataDir: dir, Committer: "Svc <svc@example.test>", CloseWait: 200 * time.Millisecond, CommitIdle: time.Hour, CommitMax: time.Hour, Serve: true}
		m, err := NewAccounts(srv)
		if err != nil {
			t.Fatal(err)
		}
		dup, err := syscall.Dup(int(m.lock.Fd()))
		if err != nil {
			m.Close()
			t.Fatal(err)
		}
		defer syscall.Close(dup)
		m.Close()
		m2, err := NewAccounts(srv)
		if err != nil {
			t.Fatalf("the lock outlived Close: %v", err)
		}
		m2.Close()
	})
}
