// Package index is the page index: a SQLite database holding page metadata,
// tags, folders and a full-text index. It is a cache that can be deleted and
// rebuilt from the files at any time; the files stay the source of truth.
//
// The full-text design follows spikes/search-scale: a contentless FTS5 table
// with one token per CJK character, so a query is a phrase.
package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite" // pure-Go driver, keeps the binary static
)

// schemaVersion changes whenever the tables or the way text is tokenised
// change; an index with another version is thrown away and rebuilt.
const schemaVersion = 2

const schema = `
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE pages (
	rowid       INTEGER PRIMARY KEY,
	id          TEXT NOT NULL UNIQUE,
	path        TEXT NOT NULL UNIQUE,
	folder      TEXT NOT NULL,
	title       TEXT NOT NULL,
	subtitle    TEXT NOT NULL,
	untitled    INTEGER NOT NULL,
	title_key   TEXT NOT NULL,
	label       TEXT NOT NULL,
	type        TEXT NOT NULL,
	tags        TEXT NOT NULL,
	created     TEXT NOT NULL,
	created_key INTEGER NOT NULL,
	mtime_ns    INTEGER NOT NULL,
	size        INTEGER NOT NULL,
	rev         TEXT NOT NULL,
	excerpt     TEXT NOT NULL
);
CREATE INDEX pages_folder_mtime ON pages(folder, mtime_ns);
CREATE INDEX pages_mtime ON pages(mtime_ns);
CREATE INDEX pages_created ON pages(created_key);
CREATE INDEX pages_title ON pages(untitled, title_key, created_key);
CREATE INDEX pages_title_desc ON pages(untitled, title_key DESC, created_key DESC);
CREATE TABLE tags (tag TEXT NOT NULL, page INTEGER NOT NULL, PRIMARY KEY (tag, page)) WITHOUT ROWID;
CREATE INDEX tags_page ON tags(page);
CREATE TABLE folders (path TEXT PRIMARY KEY);
-- Files that are not pages (no usable id). They are remembered with their size
-- and mtime so that start-up does not read them again.
CREATE TABLE problems (path TEXT PRIMARY KEY, code TEXT NOT NULL, message TEXT NOT NULL, size INTEGER NOT NULL, mtime_ns INTEGER NOT NULL);
CREATE VIRTUAL TABLE fts USING fts5(title, subtitle, body, content='', contentless_delete=1, tokenize='unicode61');
INSERT INTO folders(path) VALUES ('');
`

// Index is safe for concurrent use. Reads run in parallel (SQLite WAL); writes
// are serialised.
type Index struct {
	db   *sql.DB
	path string
	root string
	wmu  sync.Mutex
}

// Open opens or creates the index at dbPath for the space at root. An index
// that is unreadable, has another schema version or belongs to another root
// is deleted and created again empty; Sync then fills it.
func Open(dbPath, root string) (*Index, error) {
	if strings.ContainsAny(dbPath, "?#") {
		return nil, fmt.Errorf("index path %q: '?' and '#' are not supported", dbPath)
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, err
	}
	x, err := open(dbPath, root)
	if err != nil {
		// Corrupt or foreign file: it is only a cache.
		removeDB(dbPath)
		if x, err = open(dbPath, root); err != nil {
			return nil, err
		}
	}
	return x, nil
}

func removeDB(p string) {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		os.Remove(p + suffix)
	}
}

func open(dbPath, root string) (*Index, error) {
	dsn := dbPath + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(10000)&_pragma=cache_size(-65536)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	x := &Index{db: db, path: dbPath, root: root}
	if err := x.init(); err != nil {
		db.Close()
		return nil, err
	}
	return x, nil
}

func (x *Index) init() error {
	ctx := context.Background()
	var version int
	if err := x.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	fresh := version == 0
	if !fresh {
		var root string
		err := x.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'root'`).Scan(&root)
		if version != schemaVersion || err != nil || root != x.root {
			return errors.New("index does not match this space or version")
		}
		return nil
	}
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES ('root', ?)`, x.root); err != nil {
		return err
	}
	// PRAGMA does not take parameters; the value is a constant.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

// Close closes the database.
func (x *Index) Close() error { return x.db.Close() }

// Path is where the database file lives.
func (x *Index) Path() string { return x.path }
