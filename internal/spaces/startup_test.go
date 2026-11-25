package spaces

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

// What a crash leaves (an eviction cut short, a staged space, a restore's
// directory) is cleaned up by the server at its start, and by nothing else: a
// command run against the data directory of a running server must not remove
// a restore that server has in progress, nor a space it is making.
func TestOnlyTheServerCleansUpAtStartAndOnlyOneServerRuns(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		needGit(t)
		dir := filepath.Join(t.TempDir(), "data")
		opt := Options{Store: st, DataDir: dir, Committer: "Svc <svc@example.test>", CloseWait: 200 * time.Millisecond, CommitIdle: time.Hour, CommitMax: time.Hour}
		srv := opt
		srv.Serve = true
		m, err := NewAccounts(srv)
		if err != nil {
			t.Fatal(err)
		}
		defer m.Close()
		// The server is restoring a space and making another.
		inRestore := filepath.Join(m.restoringDir(), "SPACE00001.abc")
		if err := os.MkdirAll(filepath.Join(inRestore, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		staged, err := m.Stage("Being made", Identity{Name: "x", Email: "x@example.test"})
		if err != nil {
			t.Fatal(err)
		}
		defer staged.sp.Close()
		old := time.Now().Add(-time.Hour)
		os.Chtimes(filepath.Join(dir, "staging", staged.ID()), old, old)
		stagedRoot, _ := m.paths(staged.ID())

		// A command (petalbook user …) opens the same data directory meanwhile.
		cli, err := NewAccounts(opt)
		if err != nil {
			t.Fatal(err)
		}
		cli.Close()
		if !exists(inRestore) {
			t.Fatal("a command removed a restore the server has in progress")
		}
		if !exists(stagedRoot) {
			t.Fatal("a command removed a space the server is making")
		}
		// A second server on the same data directory is refused.
		if m2, err := NewAccounts(srv); err == nil {
			m2.Close()
			t.Fatal("a second server started on a data directory in use")
		}
		// Once the first has stopped, the next server cleans up.
		m.Close()
		m3, err := reopenAccounts(srv)
		if err != nil {
			t.Fatal(err)
		}
		defer m3.Close()
		if exists(inRestore) {
			t.Fatal("the server did not remove what a stopped run left")
		}
	})
}

// reopenAccounts opens a manager on a data directory whose previous manager
// has just been closed. The lock of a data directory is released when the last
// copy of its file descriptor goes, and a command another test starts at that
// moment still holds a copy for the instant between fork and exec; so, with
// tests running side by side, the lock may be seen as held a moment longer.
// A directory that stays held is still an error.
func reopenAccounts(opt Options) (*Manager, error) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		m, err := NewAccounts(opt)
		if err == nil || !errors.Is(err, ErrDataDirInUse) || time.Now().After(deadline) {
			return m, err
		}
		time.Sleep(20 * time.Millisecond)
	}
}
