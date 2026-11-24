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
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, keeps the binary static
)

// schemaVersion changes whenever the tables or the way text is tokenised
// change; an index with another version is thrown away and rebuilt.
const schemaVersion = 10

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
	partial     TEXT NOT NULL DEFAULT '',
	seq         INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX pages_seq ON pages(seq);
-- The pages that were removed, for the change feed (see feed.go): the sequence
-- number the removal got and when it happened (Unix seconds). Bounded by
-- goneMax records and goneAge.
CREATE TABLE gone (id TEXT PRIMARY KEY, seq INTEGER NOT NULL, at INTEGER NOT NULL) WITHOUT ROWID;
CREATE INDEX gone_seq ON gone(seq);
CREATE INDEX gone_at ON gone(at);
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
-- key serves the links of one page. line is the line of the file the link is
-- on (from 1; 0 when it is not known), for the broken_link problems.
CREATE TABLE links (src INTEGER NOT NULL, dst_id TEXT NOT NULL, dst_path TEXT NOT NULL, anchor TEXT NOT NULL, text TEXT NOT NULL, pos INTEGER NOT NULL, line INTEGER NOT NULL, PRIMARY KEY (src, pos)) WITHOUT ROWID;
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
	// synced: a sync has been committed since the index was opened (under wmu).
	synced bool
	// restored: the space's clone was made again from its remote while the
	// index was closed cleanly (see NoteRestored), so the first sync's finding
	// that files differ is the remote's changes, not lost commits.
	restored bool

	// head is where the change feed is (see feed.go), moved by every commit.
	head atomic.Pointer[headState]
	// now is the clock of the removal records (tests set it).
	now func() time.Time

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
	// copied: the file was made from the prepared index of ReuseSchema.
	copied bool
	// template: this is the prepared index itself, made with no meta rows.
	template bool
	// closeGuard, when set, is asked inside the last write of Close, with the
	// database's write lock held: false leaves the clean note (and the other
	// notes) unwritten (see SetCloseGuard).
	closeGuard func() bool
	trace      func(query string, args []any)
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

// ErrUnusable: the index at a path is missing, of another schema version, of
// another space or unreadable. OpenExisting returns it and changes nothing.
var ErrUnusable = errors.New("the index is missing, of another version or of another space")

// OpenExisting opens the index at dbPath for the space at root only when it
// is there and is one Open would keep: of this schema version, of this root
// and readable. It is for reading an index whose files are not there to build
// it again from (an evicted space). Anything else gives ErrUnusable, and the
// file is left exactly as it was: it is looked at on a read-only connection
// first, and never removed or created.
func OpenExisting(dbPath, root string) (*Index, error) {
	if strings.ContainsAny(dbPath, "?#") {
		return nil, fmt.Errorf("index path %q: '?' and '#' are not supported", dbPath)
	}
	if err := usable(dbPath, root); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnusable, err)
	}
	x, err := open(dbPath, root)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnusable, err)
	}
	return x, nil
}

// usable checks, without writing anything, that the index at dbPath is there,
// of this schema version and of root.
func usable(dbPath, root string) error {
	if _, err := os.Stat(dbPath); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=busy_timeout(10000)")
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version != schemaVersion {
		return fmt.Errorf("schema version %d, not %d", version, schemaVersion)
	}
	var r string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key = 'root'`).Scan(&r); err != nil {
		return err
	}
	if r != root {
		return errors.New("the index is of another directory")
	}
	return nil
}

// SetCloseGuard sets what Close asks before it notes the index closed cleanly.
// It is asked inside the last write, with the write lock of the database held,
// so nothing else can open the index and write between the answer and the
// commit. An index opened for reads beside its owner (an evicted space's) says
// false once the owner may have it open.
func (x *Index) SetCloseGuard(f func() bool) { x.closeGuard = f }

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
	x := &Index{path: dbPath, root: root, walMax: walCheckpointAt, plan: defaultPlan, now: time.Now}
	x.template = root == templateRoot
	x.copied = !x.template && copyTemplate(dbPath)
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
	// The prepared index stays without meta rows: the feed of a copy is
	// started by insertMeta and markOpen of the copy.
	if !x.template {
		if err := x.markOpen(); err != nil {
			x.wdb.Close()
			return nil, err
		}
		if err := x.loadHead(); err != nil {
			x.wdb.Close()
			return nil, err
		}
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
	if x.copied && version == schemaVersion {
		// A copy of the prepared index: the tables are there, and what is
		// of this index alone is put in by the same code as a fresh one.
		return x.inTx(ctx, func(tx *sql.Tx) error { return x.insertMeta(ctx, tx) })
	}
	if !fresh {
		var root string
		err := x.wdb.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'root'`).Scan(&root)
		if version != schemaVersion || err != nil || root != x.root {
			return errors.New("index does not match this space or version")
		}
		return nil
	}
	return x.inTx(ctx, func(tx *sql.Tx) error {
		if err := x.createSchema(ctx, tx); err != nil {
			return err
		}
		if x.template {
			return nil
		}
		return x.insertMeta(ctx, tx)
	})
}

func (x *Index) inTx(ctx context.Context, f func(*sql.Tx) error) error {
	tx, err := x.wdb.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := f(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// markOpen starts a new epoch of the change feed when the index was not closed
// since it was last opened, and notes that it is open now. The commits are not
// synced one by one (synchronous NORMAL): a power cut can take the last of them
// and leave the epoch, and the sequence numbers they handed out would then be
// handed out again. The note is synced before any other write, so an index
// whose last commits can have been lost always says so.
func (x *Index) markOpen() error {
	ctx := context.Background()
	conn, err := x.wdb.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA synchronous = FULL`); err != nil {
		return err
	}
	defer conn.ExecContext(ctx, `PRAGMA synchronous = NORMAL`)
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var clean string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'clean'`).Scan(&clean); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var restored string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'restored'`).Scan(&restored); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	x.restored = clean == "1" && restored == "1"
	if _, err := tx.ExecContext(ctx, `DELETE FROM meta WHERE key = 'restored'`); err != nil {
		return err
	}
	if clean != "1" {
		w := x.newWriter(ctx, tx)
		defer w.close()
		if err := w.newEpoch(); err != nil {
			return err
		}
		if err := w.flushFeed(); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO meta(key, value) VALUES ('clean', '0')`); err != nil {
		return err
	}
	return tx.Commit()
}

// createSchema makes the tables and sets the schema version: the step a copy
// of the prepared index has already had.
func (x *Index) createSchema(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return err
	}
	// PRAGMA does not take parameters; the value is a constant.
	_, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion))
	return err
}

// insertMeta writes the rows that belong to this index alone: its root and
// the start of its change feed (a new epoch, sequence and tree at zero, no
// removals held). The prepared index has none, so nothing of one index can
// reach another through a copy, and a copy starts its feed exactly as a fresh
// index does. markOpen then runs for both alike (the clean flag is absent, so
// it starts a further epoch and notes the index open).
func (x *Index) insertMeta(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES ('root', ?)`, x.root); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES ('epoch', ?), ('seq', '0'), ('tree', '0'), ('gone_n', '0'), ('gone_floor', '0')`, newEpoch())
	return err
}

// Close closes the database. The last thing written is the note that it was
// closed (see markOpen); no write can come after it. The prepared index gets
// no note: it holds no meta row at all.
func (x *Index) Close() error { return x.CloseWith(nil) }

// CloseWith is Close that also stores notes in the meta table, in the same
// last write as the clean note: what the owner of the index wants read while
// it is closed (see StoredNote). Keys of the index's own are not allowed.
func (x *Index) CloseWith(notes map[string]string) error {
	var err error
	x.closing.Do(func() {
		close(x.stop)
		<-x.ckptDone
		x.aux.Close()
		x.db.Close()
		x.wmu.Lock()
		defer x.wmu.Unlock()
		// When this fails the next open starts a new epoch, which is safe.
		if !x.template {
			x.writeCloseNotes(notes)
		}
		err = x.wdb.Close()
	})
	return err
}

// noteKeys are the notes CloseWith may store.
var noteKeys = map[string]bool{"settings": true}

// writeCloseNotes writes the notes and the clean note in one transaction.
// When it fails the next open starts a new epoch, which is safe.
func (x *Index) writeCloseNotes(notes map[string]string) {
	tx, err := x.wdb.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	for k, v := range notes {
		if !noteKeys[k] {
			continue
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO meta(key, value) VALUES (?, ?)`, "note_"+k, v); err != nil {
			return
		}
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO meta(key, value) VALUES ('clean', '1')`); err != nil {
		return
	}
	// The write above holds the write lock: what the guard sees stays so
	// until the commit.
	if x.closeGuard != nil && !x.closeGuard() {
		return
	}
	tx.Commit()
}

// StoredNote is a note CloseWith stored the last time it closed the index
// ("" when there is none).
func (x *Index) StoredNote(key string) (string, error) {
	var v string
	err := x.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, "note_"+key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// NoteRestored notes in the closed index at dbPath that the space's clone is
// being made again from its remote (milestone 3.1's restore), so that the
// first sync after the next open takes the files that differ from what the
// index recorded as changes, keeping the epoch: the cursors of offline
// clients stay good. It notes nothing unless the index was closed cleanly
// (its commits are all there), and the note is read and removed by the next
// open (markOpen). A missing index or one of another version is left alone:
// its next open builds it again, with a new epoch.
func NoteRestored(dbPath string) error {
	if strings.ContainsAny(dbPath, "?#") {
		return fmt.Errorf("index path %q: '?' and '#' are not supported", dbPath)
	}
	if _, err := os.Stat(dbPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version != schemaVersion {
		return nil
	}
	_, err = db.Exec(`INSERT OR REPLACE INTO meta(key, value) SELECT 'restored', '1' WHERE EXISTS (SELECT 1 FROM meta WHERE key = 'clean' AND value = '1')`)
	return err
}

// Path is where the database file lives.
func (x *Index) Path() string { return x.path }

// unlockWrite ends a write. The generation has moved already, at the commit.
func (x *Index) unlockWrite() { x.wmu.Unlock() }

// commit commits a write, with gen odd for as long as the commit may be under
// way: a read that took its snapshot while gen held one even value cannot have
// seen a part of what it changed.
func (x *Index) commit(tx *sql.Tx) error { return x.commitW(tx, nil) }

// commitW is commit for a write that has a writer: when the commit succeeds,
// the head of the change feed moves to where the writer left it (inside the
// odd generation, so a read that sees the commit sees the head too).
func (x *Index) commitW(tx *sql.Tx, w *writer) error {
	err := func() error {
		x.gen.Add(1)
		defer x.gen.Add(1)
		if err := tx.Commit(); err != nil {
			return err
		}
		if w != nil {
			x.publish(w)
		}
		return nil
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
