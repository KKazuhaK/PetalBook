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

// The prepared index has the tables and the schema version and no meta rows;
// a copy gets them from the same code as a new index (see init), so nothing
// that belongs to one index is ever shared through the template.

// templateRoot is the root the prepared index is opened with; it is never stored.
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
	return place(dbPath, template.data)
}

// place writes data to a file of its own beside dbPath and links it to dbPath,
// which fails if anything is there: the file appears whole or not at all, and
// two opens of the same new path cannot write into one file. The one that loses
// reports false and finds what the winner made.
func place(dbPath string, data []byte) bool {
	f, err := os.CreateTemp(filepath.Dir(dbPath), filepath.Base(dbPath)+".tmp-*")
	if err != nil {
		return false
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return false
	}
	return os.Link(tmp, dbPath) == nil
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
