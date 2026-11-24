package index

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// Creating the tables of a new index takes far longer than a test that uses it,
// most of all under the race detector. A test binary that makes many spaces
// calls ReuseSchema once; from then on an index that does not exist yet starts
// as a copy of one that was made once. The server never calls it, and the tests
// of this package do not either, so the schema is still created and tested here.

// templateRoot stands in for the root of the space in the copied file; init
// puts the real one in its place.
const templateRoot = "\x00template"

var (
	reuse    atomic.Bool
	template struct {
		once sync.Once
		data []byte
		err  error
	}
)

// ReuseSchema makes Open copy a prepared empty index when it has to create one
// (see above). It is for tests.
func ReuseSchema() { reuse.Store(true) }

// copyTemplate puts the prepared index at dbPath when nothing is there, and
// reports whether it did.
func copyTemplate(dbPath string) bool {
	if !reuse.Load() {
		return false
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(dbPath + suffix); err == nil || !os.IsNotExist(err) {
			return false
		}
	}
	template.once.Do(func() { template.data, template.err = buildTemplate() })
	if template.err != nil {
		return false
	}
	return os.WriteFile(dbPath, template.data, 0o644) == nil
}

func buildTemplate() ([]byte, error) {
	dir, err := os.MkdirTemp("", "petalbook-index-template-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "index.db")
	x, err := open(path, templateRoot)
	if err != nil {
		return nil, err
	}
	// Everything in the main file, so that it is the whole database.
	if _, err := x.wdb.ExecContext(context.Background(), `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		x.Close()
		return nil, err
	}
	if err := x.Close(); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}
