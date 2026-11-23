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
	"sync/atomic"

	_ "modernc.org/sqlite" // pure-Go driver, keeps the binary static
)

// schemaVersion changes whenever the tables or the way text is tokenised
// change; an index with another version is thrown away and rebuilt.
const schemaVersion = 9

const schema = `
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE pages (
	rowid       INTEGER PRIMARY KEY,
	id          TEXT NOT NULL UNIQUE,
	path        TEXT NOT NULL UNIQUE,
	folder      TEXT NOT NULL,
	title       TEXT NOT NULL,
	subtitle    TEXT NOT NULL,
	source      TEXT NOT NULL,
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
	excerpt     TEXT NOT NULL,
	props       TEXT NOT NULL DEFAULT '',
	partial     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX pages_folder_mtime ON pages(folder, mtime_ns);
CREATE INDEX pages_mtime ON pages(mtime_ns);
CREATE INDEX pages_created ON pages(created_key);
CREATE INDEX pages_title ON pages(untitled, title_key, created_key);
CREATE INDEX pages_title_desc ON pages(untitled, title_key DESC, created_key DESC);
CREATE TABLE tags (tag TEXT NOT NULL, page INTEGER NOT NULL, PRIMARY KEY (tag, page)) WITHOUT ROWID;
CREATE INDEX tags_page ON tags(page);
-- The properties of the pages (the header keys that are not built in): one row
-- for a scalar, one per element for a list. skey sorts in the order of the
-- values (see propsOf). The primary key serves a page's own rows; props_sort
-- serves sorting by the first value of a property, props_val the lookups by
-- value.
CREATE TABLE props (page INTEGER NOT NULL, key TEXT NOT NULL, pos INTEGER NOT NULL, val TEXT NOT NULL, skey TEXT NOT NULL, PRIMARY KEY (page, key, pos)) WITHOUT ROWID;
CREATE INDEX props_sort ON props(key, pos, skey, page);
CREATE INDEX props_val ON props(key, val, page);
CREATE INDEX props_skey ON props(key, skey, page);
-- The links from a page to other pages (see internal/links). dst_id is the id
-- the link carries, or '' for a link written with a path only, which is
-- resolved when it is read (by path), so that it stays right when the target is
-- added, moved or deleted. dst_path is the file the path names, always; a link
-- whose id is known to be at another path is one to be repaired. The primary
-- key serves the links of one page.
CREATE TABLE links (src INTEGER NOT NULL, dst_id TEXT NOT NULL, dst_path TEXT NOT NULL, anchor TEXT NOT NULL, text TEXT NOT NULL, pos INTEGER NOT NULL, PRIMARY KEY (src, pos)) WITHOUT ROWID;
CREATE INDEX links_dst_id ON links(dst_id, src);
CREATE INDEX links_dst_path ON links(dst_path, src);
CREATE TABLE folders (path TEXT PRIMARY KEY);
-- Files that are not pages (no usable id). They are remembered with their size
-- and mtime so that start-up does not read them again.
-- kind is what the check page calls the problem ('' for a file that could not
-- be read: not listed), id the id a duplicate shares, line where a bad header
-- is wrong (0 when not known), rev the rev of the file (what a fix is based on).
CREATE TABLE problems (path TEXT PRIMARY KEY, code TEXT NOT NULL, message TEXT NOT NULL, size INTEGER NOT NULL, mtime_ns INTEGER NOT NULL,
	kind TEXT NOT NULL DEFAULT '', id TEXT NOT NULL DEFAULT '', line INTEGER NOT NULL DEFAULT 0, rev TEXT NOT NULL DEFAULT '');
CREATE INDEX problems_kind ON problems(kind, path);
CREATE INDEX problems_id ON problems(id) WHERE id <> '';
-- The problems of pages that are served (see checks.go). target is the file a
-- missing_asset is about, as a path from the root of the space.
CREATE TABLE checks (kind TEXT NOT NULL, path TEXT NOT NULL, seq INTEGER NOT NULL, id TEXT NOT NULL, message TEXT NOT NULL, detail TEXT NOT NULL, target TEXT NOT NULL,
	PRIMARY KEY (kind, path, seq)) WITHOUT ROWID;
CREATE INDEX checks_path ON checks(path);
CREATE INDEX checks_target ON checks(target) WHERE target <> '';
CREATE INDEX pages_folder_title ON pages(folder, title_key) WHERE untitled = 0;
CREATE VIRTUAL TABLE fts USING fts5(title, subtitle, source, body, content='', contentless_delete=1, tokenize='unicode61');
INSERT INTO folders(path) VALUES ('');
`

// Index is safe for concurrent use. Reads run in parallel (SQLite WAL); writes
// are serialised.
//
// Writes have a connection of their own (wdb), taken only under wmu, so a
// write never waits for a connection behind reads; reads use db and aux, which
// writes never touch.
//
// The write-ahead log can only be cut back to its start when no read uses a
// snapshot older than its last frame, and with reads that follow one another
// without a pause there is always one: the log would grow for as long as
// writes go on. So when it passes walCheckpointAt after a commit, the
// checkpointer waits for the write under way, closes the gate to new reads,
// lets those under way end (a request is short), and checkpoints the whole log
// and truncates it while no read holds a snapshot. Reads and writes wait at
// most for the reads under way and the checkpoint.
type Index struct {
	db   *sql.DB
	wdb  *sql.DB
	path string
	root string
	wmu  sync.Mutex

	// plan holds the bounds that choose how a listing or a count is read.
	plan planBounds

	// gate is held shared by every read from the pin of its snapshot to its
	// end, and alone by the checkpointer (see above).
	gate sync.RWMutex
	// ckpt asks the checkpointer to run; stop ends it, and ckptDone is
	// closed when it has ended.
	ckpt     chan struct{}
	stop     chan struct{}
	ckptDone chan struct{}
	closing  sync.Once
	// checkpoints counts the checkpoints made (read by tests).
	checkpoints atomic.Int64
	// walMax is walCheckpointAt as it was at Open.
	walMax int64

	// rules are what the type problems of a page are judged by, assetExists
	// how an attachment is looked for (see checks.go).
	rules       atomic.Pointer[TypeRules]
	assetExists atomic.Pointer[func(string) bool]

	// aux holds the connections a read uses beside its own to read several
	// index ranges at the same time (see read.go); auxFree has a token for
	// each, taken without waiting, so a read never waits for one while it
	// holds a connection of db.
	aux     *sql.DB
	auxFree chan struct{}

	// gen is a sequence lock on the content of the index: odd while a write
	// commits, moved to the next even value as soon as the commit is done (not
	// after the work that follows it). A read whose snapshot was taken while
	// gen held one even value has the content of that generation, and a result
	// computed from it is good for as long as gen has not changed (see Groups).
	gen    atomic.Uint64
	groups groupCache

	// queries counts the queries of listings with conditions and of counts
	// (read by tests); trace, when a test sets it, is called with each.
	queries atomic.Int64
	trace   func(query string, args []any)
	// shared and serial count the times a read had other connections share
	// its snapshot, and the times it read in turn because none could (tests).
	shared, serial atomic.Int64
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

// walCheckpointAt is the size of the write-ahead log past which it is
// checkpointed and truncated after a commit; it is also the size a log that
// SQLite starts again from the beginning is cut back to (journal_size_limit).
// It is read at Open; tests make it smaller.
var walCheckpointAt int64 = 16 << 20

// readConns is the number of connections of db: the requests reading at the
// same time, each on one (and maybe others of aux).
const readConns = 8

func open(dbPath, root string) (*Index, error) {
	x := &Index{path: dbPath, root: root, walMax: walCheckpointAt, plan: defaultPlan}
	dsn := dbPath + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(10000)&_pragma=cache_size(-65536)" +
		fmt.Sprintf("&_pragma=journal_size_limit(%d)", x.walMax)
	var err error
	if x.wdb, err = sql.Open("sqlite", dsn); err != nil {
		return nil, err
	}
	// One connection, used under wmu only: a write finds it free.
	x.wdb.SetMaxOpenConns(1)
	x.wdb.SetMaxIdleConns(1)
	if err := x.init(); err != nil {
		x.wdb.Close()
		return nil, err
	}
	// Reads cannot change the database: no write can end up on their
	// connections by mistake.
	rdsn := dsn + "&_pragma=query_only(1)"
	if x.db, err = sql.Open("sqlite", rdsn); err != nil {
		x.wdb.Close()
		return nil, err
	}
	x.db.SetMaxOpenConns(readConns)
	x.db.SetMaxIdleConns(readConns)
	if x.aux, err = sql.Open("sqlite", rdsn); err != nil {
		x.db.Close()
		x.wdb.Close()
		return nil, err
	}
	x.aux.SetMaxOpenConns(auxConns)
	x.aux.SetMaxIdleConns(auxConns)
	x.auxFree = make(chan struct{}, auxConns)
	for range auxConns {
		x.auxFree <- struct{}{}
	}
	x.ckpt, x.stop, x.ckptDone = make(chan struct{}, 1), make(chan struct{}), make(chan struct{})
	go x.checkpointer()
	return x, nil
}

func (x *Index) init() error {
	ctx := context.Background()
	var version int
	if err := x.wdb.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	fresh := version == 0
	if !fresh {
		var root string
		err := x.wdb.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'root'`).Scan(&root)
		if version != schemaVersion || err != nil || root != x.root {
			return errors.New("index does not match this space or version")
		}
		return nil
	}
	tx, err := x.wdb.BeginTx(ctx, nil)
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
func (x *Index) Close() error {
	var err error
	x.closing.Do(func() {
		close(x.stop)
		<-x.ckptDone
		x.aux.Close()
		x.db.Close()
		err = x.wdb.Close()
	})
	return err
}

// Path is where the database file lives.
func (x *Index) Path() string { return x.path }

// unlockWrite ends a write. The generation has moved already, at the commit.
func (x *Index) unlockWrite() { x.wmu.Unlock() }

// commit commits a write, with gen odd for as long as the commit may be under
// way: a read that took its snapshot while gen held one even value cannot have
// seen a part of what it changed.
func (x *Index) commit(tx *sql.Tx) error {
	err := func() error {
		x.gen.Add(1)
		defer x.gen.Add(1)
		return tx.Commit()
	}()
	if err == nil {
		x.checkWAL()
	}
	return err
}

// checkWAL asks the checkpointer to run when the log has grown past
// walCheckpointAt. It does not wait.
func (x *Index) checkWAL() {
	if fi, err := os.Stat(x.path + "-wal"); err == nil && fi.Size() > x.walMax {
		x.askCheckpoint()
	}
}

func (x *Index) askCheckpoint() {
	select {
	case x.ckpt <- struct{}{}:
	default: // asked already
	}
}

// checkpointer checkpoints the whole log and truncates it, each time it is
// asked, with no read holding a snapshot and no write under way. It takes the
// write lock first, between two writes, and then closes the gate, so new reads
// wait and those under way end. Writes wait for the reads under way (a request
// is short) and the checkpoint; reads for the same. The other order would make
// reads wait for whole writes, and a full Sync takes seconds.
func (x *Index) checkpointer() {
	defer close(x.ckptDone)
	for {
		select {
		case <-x.stop:
			return
		case <-x.ckpt:
		}
		x.wmu.Lock()
		x.gate.Lock()
		// No read holds a snapshot and no write is under way: the checkpoint
		// does not wait. The busy timeout is short all the same, so that a
		// read from outside (a tool opening the file) cannot hold writes back.
		ctx := context.Background()
		if c, err := x.wdb.Conn(ctx); err == nil {
			c.ExecContext(ctx, `PRAGMA busy_timeout = 100`)
			var busy, log, done int
			if c.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &log, &done) == nil && busy == 0 {
				x.checkpoints.Add(1)
			}
			c.ExecContext(ctx, `PRAGMA busy_timeout = 10000`)
			c.Close()
		}
		x.gate.Unlock()
		x.wmu.Unlock()
	}
}
