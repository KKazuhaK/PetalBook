package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/page"
)

// FileInfo describes one page file as the file system reports it.
type FileInfo struct {
	Rel     string // slash-separated, relative to the space root
	Size    int64
	MtimeNs int64
}

// Source is the file tree the index mirrors.
type Source interface {
	// Walk calls file for every page file and dir for every folder (relative
	// path, "" for the root is not reported). Order does not matter.
	Walk(file func(FileInfo) error, dir func(rel string) error) error
	// Read returns the content of a page file.
	Read(rel string) ([]byte, error)
	// Stat describes a page file now (fs.ErrNotExist if it is gone). Sync uses
	// it to confirm what a walk saw, since the walk runs without the write lock.
	Stat(rel string) (FileInfo, error)
	// DirExists reports whether a folder exists now.
	DirExists(rel string) bool
}

// Stats is the outcome of a Sync.
type Stats struct {
	Pages    int // pages in the index afterwards
	Added    int
	Updated  int
	Removed  int
	Problems int // files that are not usable pages
	Elapsed  time.Duration
}

type known struct{ size, mtimeNs int64 }

// Sync makes the index match the source. With full false it compares each
// file's size and mtime with what the index recorded and reads only the files
// that differ, which is what keeps start-up on a large unchanged space cheap.
// With full true everything is read again.
//
// It works in phases so that stale rows can never conflict with their own
// replacements (a swap of two files, a rotation of three):
//
//  1. walk and stat the source, without any lock;
//  2. under the write lock, delete the rows of every path that was removed or
//     changed, so what remains is exactly the unchanged files;
//  3. read the changed files and insert them in path order; an id that an
//     unchanged file (or an earlier path) already holds makes the later file
//     a duplicate.
//
// Files recorded as problems because of a conflict (duplicate_id) or because
// they could not be read are looked at again on every sync: their state
// depends on other files, so size and mtime say nothing about them.
//
// Cost: O(files) to walk and stat, plus O(changed files) to read. It runs at
// start-up, on a reindex, and when a lookup misses (throttled), never per
// request otherwise.
func (x *Index) Sync(src Source, full bool) (Stats, error) {
	start := time.Now()
	ctx := context.Background()

	disk := map[string]FileInfo{}
	dirSet := map[string]bool{"": true}
	err := src.Walk(
		func(f FileInfo) error { disk[f.Rel] = f; return nil },
		func(rel string) error {
			for ; rel != "" && rel != "."; rel = path.Dir(rel) {
				dirSet[rel] = true
			}
			return nil
		})
	if err != nil {
		return Stats{}, err
	}

	x.wmu.Lock()
	defer x.unlockWrite()

	have := map[string]known{}
	var recheck []string
	if !full {
		if recheck, err = x.loadKnown(ctx, have); err != nil {
			return Stats{}, err
		}
	}
	var removed []string
	for p := range have {
		if _, ok := disk[p]; !ok {
			removed = append(removed, p)
		}
	}
	gone := len(removed)
	for _, p := range recheck {
		if _, ok := disk[p]; !ok {
			removed = append(removed, p)
		}
	}
	var todo []FileInfo
	// differs: the files are not what the index recorded (files looked at again on
	// every sync do not count).
	differs := gone > 0
	for p, f := range disk {
		if k, ok := have[p]; !ok || k.size != f.Size || k.mtimeNs != f.MtimeNs {
			todo = append(todo, f)
			if !differs && (ok || !slices.Contains(recheck, p)) {
				differs = true
			}
		}
	}
	// A fixed order makes the outcome independent of map order, e.g. which of
	// two files with the same id is kept.
	sort.Strings(removed)
	sort.Slice(todo, func(i, j int) bool { return todo[i].Rel < todo[j].Rel })

	tx, err := x.wdb.BeginTx(ctx, nil)
	if err != nil {
		return Stats{}, err
	}
	defer tx.Rollback()
	w := x.newWriter(ctx, tx)
	defer w.close()
	w.fd.pruneAge = true
	rules := x.rules.Load()

	st := Stats{}
	if full {
		if err := w.clearAll(); err != nil {
			return Stats{}, err
		}
	} else if !x.synced && differs && !x.restored {
		// The first sync after an open found files the index did not record: its
		// last commits may have been lost, with the sequence numbers they handed out.
		// Not after a restore (see NoteRestored): the index was closed cleanly and
		// the files differ because the remote moved on.
		if err := w.newEpoch(); err != nil {
			return Stats{}, err
		}
	}
	// Phase 2. The walk ran without the lock, so a path it did not see may
	// have been written since; check before deleting its row.
	for _, p := range removed {
		if _, err := src.Stat(p); err == nil {
			continue
		}
		if err := w.removePath(p); err != nil {
			return Stats{}, err
		}
		st.Removed++
	}
	for _, f := range todo {
		if w.fresh {
			break // everything was cleared above
		}
		if err := w.removePath(f.Rel); err != nil {
			return Stats{}, err
		}
	}
	onDisk := func(p string) bool { _, ok := disk[p]; return ok }

	// Phase 3.
	const batch = 256
	workers := runtime.GOMAXPROCS(0)
	for lo := 0; lo < len(todo); lo += batch {
		hi := min(lo+batch, len(todo))
		results := x.loadBatch(src, todo[lo:hi], workers, rules)
		for i, r := range results {
			f := todo[lo+i]
			if r.gone {
				continue // deleted since the walk
			}
			f = r.info
			_, existed := have[f.Rel]
			if r.entry != nil {
				conflict, err := w.upsert(r.entry, onDisk)
				if err != nil {
					return Stats{}, err
				}
				if conflict != "" {
					r.prob = &page.Problem{Code: "duplicate_id", Message: fmt.Sprintf("id %s is already used by %s", r.entry.ID, conflict)}
					r.dupID, r.rev = r.entry.ID, r.entry.Rev
					r.entry = nil
				}
			}
			if r.entry == nil {
				if err := w.setProblem(f, r.prob, r.dupID, r.rev); err != nil {
					return Stats{}, err
				}
			}
			if existed {
				st.Updated++
			} else {
				st.Added++
			}
		}
	}
	if err := w.replaceFolders(dirSet, src.DirExists); err != nil {
		return Stats{}, err
	}
	if err := w.finish(); err != nil {
		return Stats{}, err
	}
	if full && rules != nil {
		// Every page was checked against these rules just now.
		if _, err := w.exec(`INSERT OR REPLACE INTO meta(key, value) VALUES ('types_fp', ?)`, rules.Fingerprint); err != nil {
			return Stats{}, err
		}
	}
	if err := x.commitW(tx, w); err != nil {
		return Stats{}, err
	}
	x.synced = true
	if len(todo) > 1000 {
		// After a big load, compact the full-text index and shrink the WAL.
		x.wdb.ExecContext(ctx, `INSERT INTO fts(fts) VALUES ('optimize')`)
		x.askCheckpoint()
	}
	x.wdb.QueryRowContext(ctx, `SELECT count(*) FROM pages`).Scan(&st.Pages)
	x.wdb.QueryRowContext(ctx, `SELECT count(*) FROM problems`).Scan(&st.Problems)
	st.Elapsed = time.Since(start)
	return st, nil
}

// loadKnown fills into with what the index knows about each file. Problem
// rows that depend on other files (duplicate ids) or on a transient failure
// are returned in recheck instead, so they are evaluated again every time.
func (x *Index) loadKnown(ctx context.Context, into map[string]known) (recheck []string, err error) {
	for _, q := range []string{
		`SELECT path, size, mtime_ns FROM pages`,
		`SELECT path, size, mtime_ns FROM problems WHERE code NOT IN ('duplicate_id', 'unreadable')`,
	} {
		rows, err := x.wdb.QueryContext(ctx, q)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var p string
			var k known
			if err := rows.Scan(&p, &k.size, &k.mtimeNs); err != nil {
				rows.Close()
				return nil, err
			}
			into[p] = k
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	rows, err := x.wdb.QueryContext(ctx, `SELECT path FROM problems WHERE code IN ('duplicate_id', 'unreadable')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		recheck = append(recheck, p)
	}
	return recheck, rows.Err()
}

type loaded struct {
	entry *Entry
	prob  *page.Problem
	info  FileInfo // the file as it is now
	gone  bool
	// dupID is the id a file that is a duplicate shares.
	dupID string
	// rev is the rev of a file that is not a page (or not served: a duplicate).
	rev string
}

// loadBatch reads and parses files in parallel; the results keep the order of
// files so they can be applied deterministically.
func (x *Index) loadBatch(src Source, files []FileInfo, workers int, rules *TypeRules) []loaded {
	out := make([]loaded, len(files))
	var wg sync.WaitGroup
	sem := make(chan struct{}, workers)
	for i := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			// Stat before reading: if the file changes in between, the recorded
			// mtime is the older one and the next sync reads it again.
			info, err := src.Stat(files[i].Rel)
			if errors.Is(err, fs.ErrNotExist) {
				out[i].gone = true
				return
			}
			if err != nil {
				out[i].info = files[i]
				out[i].prob = &page.Problem{Code: "unreadable", Message: err.Error()}
				return
			}
			out[i].info = info
			data, err := src.Read(info.Rel)
			if errors.Is(err, fs.ErrNotExist) {
				out[i].gone = true
				return
			}
			if err != nil {
				out[i].prob = &page.Problem{Code: "unreadable", Message: err.Error()}
				return
			}
			out[i].entry, out[i].prob = BuildEntry(info.Rel, data, info.MtimeNs)
			if out[i].entry == nil {
				out[i].rev = page.Rev(data)
			}
			if out[i].entry != nil {
				// The size we recorded must be the size we parsed, in case the
				// file changed between the walk and the read.
				out[i].entry.Size = int64(len(data))
				x.annotate(out[i].entry, rules)
			}
		}()
	}
	wg.Wait()
	return out
}

// Put indexes one page file after the server wrote it. It returns false when
// the file is not a usable page (and records why).
func (x *Index) Put(rel string, data []byte, mtimeNs int64) (bool, error) {
	x.wmu.Lock()
	defer x.unlockWrite()
	ctx := context.Background()
	tx, err := x.wdb.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	w := x.newWriter(ctx, tx)
	defer w.close()

	e, prob := BuildEntry(rel, data, mtimeNs)
	ok := e != nil
	if ok {
		x.annotate(e, x.rules.Load())
		if conflict, err := w.upsert(e, nil); err != nil {
			return false, err
		} else if conflict != "" {
			return false, fmt.Errorf("id %s is already used by %s", e.ID, conflict)
		}
	} else if err := w.setProblem(FileInfo{Rel: rel, Size: int64(len(data)), MtimeNs: mtimeNs}, prob, "", page.Rev(data)); err != nil {
		return false, err
	}
	if err := w.finish(); err != nil {
		return false, err
	}
	return ok, x.commitW(tx, w)
}

// Remove drops a page from the index by id.
func (x *Index) Remove(id string) error {
	x.wmu.Lock()
	defer x.unlockWrite()
	ctx := context.Background()
	tx, err := x.wdb.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	w := x.newWriter(ctx, tx)
	defer w.close()
	var rel string
	if err := tx.QueryRowContext(ctx, `SELECT path FROM pages WHERE id = ?`, id).Scan(&rel); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	if err := w.removePath(rel); err != nil {
		return err
	}
	if err := w.finish(); err != nil {
		return err
	}
	return x.commitW(tx, w)
}

// AddFolder records a folder and its ancestors.
func (x *Index) AddFolder(rel string) error {
	x.wmu.Lock()
	defer x.unlockWrite()
	ctx := context.Background()
	tx, err := x.wdb.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	w := x.newWriter(ctx, tx)
	defer w.close()
	for ; rel != "" && rel != "."; rel = path.Dir(rel) {
		if err := w.addFolder(rel); err != nil {
			return err
		}
	}
	if err := w.flushFeed(); err != nil {
		return err
	}
	return x.commitW(tx, w)
}

// Forget drops the rows of these file paths and these folders, whatever is on
// disk. It is for paths that were just renamed away: on a file system that
// ignores case, the old spelling of a renamed folder still "exists", and
// looking would keep both spellings.
func (x *Index) Forget(paths, dirs []string) error {
	ctx := context.Background()
	x.wmu.Lock()
	defer x.unlockWrite()
	tx, err := x.wdb.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	w := x.newWriter(ctx, tx)
	defer w.close()
	for _, p := range paths {
		if err := w.removePath(p); err != nil {
			return err
		}
	}
	for _, d := range dirs {
		if err := w.dropFolder(d); err != nil {
			return err
		}
	}
	if err := w.finish(); err != nil {
		return err
	}
	return x.commitW(tx, w)
}

// PathByID returns the file path of a page.
func (x *Index) PathByID(id string) (string, bool) {
	var p string
	err := x.one(func(db querier) error { // one query, read like the others (read.go)
		return db.QueryRow(`SELECT path FROM pages WHERE id = ?`, id).Scan(&p)
	})
	return p, err == nil
}

// HasID reports whether a page with this id is indexed.
func (x *Index) HasID(id string) bool {
	_, ok := x.PathByID(id)
	return ok
}

// Problem is a file the index could not use as a page.
type Problem struct {
	Path, Code, Message string
}

// Problems lists the files that are not usable pages, by path.
func (x *Index) Problems() ([]Problem, error) {
	var out []Problem
	err := x.one(func(db querier) error { // one query, read like the others (read.go)
		rows, err := db.Query(`SELECT path, code, message FROM problems ORDER BY path`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p Problem
			if err := rows.Scan(&p.Path, &p.Code, &p.Message); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// writer holds the prepared statements of one write transaction.
type writer struct {
	ctx   context.Context
	tx    *sql.Tx
	stmts map[string]*sql.Stmt
	// dirty are the groups of pages with one title in one folder whose
	// duplicate_title problems are to be looked at again; allTitles says all
	// of them are (see finishTitles).
	dirty     map[titleGroup]bool
	allTitles bool
	// linkSrcs are the pages whose broken_link problems are to be looked at
	// again, linkIDs and linkPaths the targets whose links are (see
	// finishLinks); allLinks says all of them are.
	linkSrcs           map[int64]bool
	linkIDs, linkPaths map[string]bool
	allLinks           bool
	// fresh says the tables were emptied in this transaction (a full
	// rebuild): there is nothing to replace.
	fresh bool

	// The change feed (see feed.go): where it stands, read at the first need
	// and written back by finish.
	now       func() time.Time
	fd        feedState
	treeDirty bool
}

func (x *Index) newWriter(ctx context.Context, tx *sql.Tx) *writer {
	return &writer{ctx: ctx, tx: tx, stmts: map[string]*sql.Stmt{}, now: x.now}
}

func (w *writer) close() {
	for _, s := range w.stmts {
		s.Close()
	}
}

func (w *writer) exec(q string, args ...any) (sql.Result, error) {
	s, ok := w.stmts[q]
	if !ok {
		var err error
		if s, err = w.tx.PrepareContext(w.ctx, q); err != nil {
			return nil, err
		}
		w.stmts[q] = s
	}
	return s.ExecContext(w.ctx, args...)
}

// queryRow runs a query that is asked many times in one transaction on a
// statement prepared once.
func (w *writer) queryRow(q string, args ...any) *sql.Row {
	s, ok := w.stmts[q]
	if !ok {
		var err error
		if s, err = w.tx.PrepareContext(w.ctx, q); err != nil {
			return w.tx.QueryRowContext(w.ctx, q, args...)
		}
		w.stmts[q] = s
	}
	return s.QueryRowContext(w.ctx, args...)
}

func (w *writer) clearAll() error {
	for _, q := range []string{`DELETE FROM pages`, `DELETE FROM tags`, `DELETE FROM props`, `DELETE FROM links`, `DELETE FROM problems`, `DELETE FROM checks`, `INSERT INTO fts(fts) VALUES ('delete-all')`} {
		if _, err := w.tx.ExecContext(w.ctx, q); err != nil {
			return err
		}
	}
	w.allTitles, w.allLinks, w.fresh = true, true, true
	return w.newEpoch()
}

// removePath deletes whatever the index holds for a file path.
func (w *writer) removePath(rel string) error {
	if err := w.removeIfPage(rel); err != nil {
		return err
	}
	if _, err := w.exec(`DELETE FROM checks WHERE path = ?`, rel); err != nil {
		return err
	}
	_, err := w.exec(`DELETE FROM problems WHERE path = ?`, rel)
	return err
}

func (w *writer) deleteRow(rowid int64) error {
	var id, p string
	if err := w.queryRow(`SELECT id, path FROM pages WHERE rowid = ?`, rowid).Scan(&id, &p); err == nil {
		if !w.allLinks {
			// The links to the page are broken now.
			w.markLinkTarget(id, p)
		}
		// The page is gone for the change feed (until it is made again).
		if err := w.recordGone(id); err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	for _, q := range []string{`DELETE FROM tags WHERE page = ?`, `DELETE FROM props WHERE page = ?`, `DELETE FROM links WHERE src = ?`, `DELETE FROM fts WHERE rowid = ?`, `DELETE FROM pages WHERE rowid = ?`} {
		if _, err := w.exec(q, rowid); err != nil {
			return err
		}
	}
	return nil
}

func (w *writer) setProblem(f FileInfo, p *page.Problem, id, rev string) error {
	if err := w.removeIfPage(f.Rel); err != nil {
		return err
	}
	if _, err := w.exec(`DELETE FROM checks WHERE path = ?`, f.Rel); err != nil {
		return err
	}
	_, err := w.exec(`INSERT OR REPLACE INTO problems(path, code, message, size, mtime_ns, kind, id, line, rev) VALUES (?,?,?,?,?,?,?,?,?)`,
		f.Rel, p.Code, p.Message, f.Size, f.MtimeNs, kindOfCode(p.Code), id, headerLine(p), rev)
	return err
}

// removeIfPage drops a page row at this path, for a file that stopped being a page.
func (w *writer) removeIfPage(rel string) error {
	var rowid int64
	var folder, key string
	var untitled bool
	err := w.queryRow(`SELECT rowid, folder, title_key, untitled FROM pages WHERE path = ?`, rel).Scan(&rowid, &folder, &key, &untitled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	w.markTitle(folder, key, untitled)
	return w.deleteRow(rowid)
}

// upsert stores a page. The id is the identity: a known id at a new path is a
// move and keeps its row. If the id is held by a file at another path that
// onDisk says still exists, nothing is stored and that path is returned; the
// caller reports the new file as a duplicate.
func (w *writer) upsert(e *Entry, onDisk func(string) bool) (conflict string, err error) {
	var rowid int64
	var oldPath, oldFolder, oldKey string
	var oldUntitled bool
	found := true
	if err := w.queryRow(`SELECT rowid, path, folder, title_key, untitled FROM pages WHERE id = ?`, e.ID).Scan(&rowid, &oldPath, &oldFolder, &oldKey, &oldUntitled); errors.Is(err, sql.ErrNoRows) {
		found = false
	} else if err != nil {
		return "", err
	}
	if found && oldPath != e.Path && onDisk != nil && onDisk(oldPath) {
		return oldPath, nil
	}
	// Another page at this path whose id was edited away: that row is stale.
	if !w.fresh {
		var other int64
		var otherFolder, otherKey string
		var otherUntitled bool
		switch err := w.queryRow(`SELECT rowid, folder, title_key, untitled FROM pages WHERE path = ? AND id <> ?`, e.Path, e.ID).Scan(&other, &otherFolder, &otherKey, &otherUntitled); {
		case err == nil:
			w.markTitle(otherFolder, otherKey, otherUntitled)
			if err := w.deleteRow(other); err != nil {
				return "", err
			}
		case !errors.Is(err, sql.ErrNoRows):
			return "", err
		}
	}
	seq, err := w.nextSeq()
	if err != nil {
		return "", err
	}
	if !found || oldFolder != e.Folder {
		if err := w.treeChanged(); err != nil {
			return "", err
		}
	}
	if err := w.forgetGone(e.ID); err != nil {
		return "", err
	}
	args := []any{e.ID, e.Path, e.Folder, e.Title, e.Subtitle, e.Source, b2i(e.Untitled), strings.ToLower(e.Title), e.Label, e.Type,
		tagsJSON(e.Tags), e.Created, e.CreatedKey, e.MtimeNs, e.Size, e.Rev, e.Excerpt, e.PropsJSON, e.PartialJSON, seq}
	titleKey := strings.ToLower(e.Title)
	if found {
		w.markTitle(oldFolder, oldKey, oldUntitled)
		// What was recorded of the page, and of the path it had, is replaced.
		if _, err := w.exec(`DELETE FROM checks WHERE path = ?`, oldPath); err != nil {
			return "", err
		}
		if err := w.deleteRowKeepPage(rowid); err != nil {
			return "", err
		}
		_, err = w.exec(`UPDATE pages SET id=?, path=?, folder=?, title=?, subtitle=?, source=?, untitled=?, title_key=?, label=?, type=?,
			tags=?, created=?, created_key=?, mtime_ns=?, size=?, rev=?, excerpt=?, props=?, partial=?, seq=? WHERE rowid = ?`, append(args, rowid)...)
	} else {
		var res sql.Result
		res, err = w.exec(`INSERT INTO pages(id, path, folder, title, subtitle, source, untitled, title_key, label, type,
			tags, created, created_key, mtime_ns, size, rev, excerpt, props, partial, seq) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, args...)
		if err == nil {
			rowid, err = res.LastInsertId()
		}
	}
	if err != nil {
		return "", err
	}
	for _, t := range e.Tags {
		if _, err := w.exec(`INSERT OR IGNORE INTO tags(tag, page) VALUES (?, ?)`, t, rowid); err != nil {
			return "", err
		}
	}
	for _, r := range e.props {
		if _, err := w.exec(`INSERT INTO props(page, key, pos, val, skey) VALUES (?,?,?,?,?)`, rowid, r.key, r.pos, r.val, r.skey); err != nil {
			return "", err
		}
	}
	for i, l := range e.links {
		line := 0
		if i < len(e.linkLines) {
			line = e.linkLines[i]
		}
		if _, err := w.exec(`INSERT INTO links(src, dst_id, dst_path, anchor, text, pos, line) VALUES (?,?,?,?,?,?,?)`, rowid, l.ID, l.Path, l.Anchor, l.Text, i, line); err != nil {
			return "", err
		}
	}
	// Its own links are looked at again, and the links that a new page, or a
	// page at a new path, may now resolve or have stopped resolving.
	w.markLinkSrc(rowid)
	if !found {
		w.markLinkTarget(e.ID, e.Path)
	} else if oldPath != e.Path {
		w.markLinkTarget("", oldPath)
		w.markLinkTarget("", e.Path)
	}
	if _, err := w.exec(`INSERT INTO fts(rowid, title, subtitle, source, body) VALUES (?,?,?,?,?)`, rowid, e.ftsTitle, e.ftsSubtitle, e.ftsSource, e.ftsBody); err != nil {
		return "", err
	}
	// The file is a page now, whatever it was before.
	if !w.fresh {
		if _, err := w.exec(`DELETE FROM problems WHERE path = ?`, e.Path); err != nil {
			return "", err
		}
		if _, err := w.exec(`DELETE FROM checks WHERE path = ?`, e.Path); err != nil {
			return "", err
		}
	}
	for i, c := range e.checks {
		if err := w.insertCheck(e.Path, i, e.ID, c); err != nil {
			return "", err
		}
	}
	w.markTitle(e.Folder, titleKey, e.Untitled)
	// The page row may sit in a folder the index has not seen yet.
	for d := e.Folder; d != ""; d = path.Dir(d) {
		if d == "." {
			break
		}
		if err := w.addFolder(d); err != nil {
			return "", err
		}
	}
	return "", nil
}

// deleteRowKeepPage clears the dependent rows of a page that is about to be rewritten.
func (w *writer) deleteRowKeepPage(rowid int64) error {
	for _, q := range []string{`DELETE FROM tags WHERE page = ?`, `DELETE FROM props WHERE page = ?`, `DELETE FROM links WHERE src = ?`, `DELETE FROM fts WHERE rowid = ?`} {
		if _, err := w.exec(q, rowid); err != nil {
			return err
		}
	}
	return nil
}

// replaceFolders brings the folder table in line with a walk. The walk ran
// without the write lock, so a folder it did not see is dropped only if it is
// really gone.
func (w *writer) replaceFolders(dirs map[string]bool, exists func(string) bool) error {
	for d := range dirs {
		if err := w.addFolder(d); err != nil {
			return err
		}
	}
	rows, err := w.tx.QueryContext(w.ctx, `SELECT path FROM folders`)
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return err
		}
		if !dirs[p] && !exists(p) {
			stale = append(stale, p)
		}
	}
	rows.Close()
	for _, p := range stale {
		if err := w.dropFolder(p); err != nil {
			return err
		}
	}
	// Folders of indexed pages exist even if the walk skipped them.
	res, err := w.exec(`INSERT OR IGNORE INTO folders(path) SELECT DISTINCT folder FROM pages`)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return w.treeChanged()
	}
	return nil
}

// addFolder records a folder; the tree changed if it was not there.
func (w *writer) addFolder(rel string) error {
	res, err := w.exec(`INSERT OR IGNORE INTO folders(path) VALUES (?)`, rel)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return w.treeChanged()
	}
	return nil
}

// dropFolder forgets a folder; the tree changed if it was there.
func (w *writer) dropFolder(rel string) error {
	res, err := w.exec(`DELETE FROM folders WHERE path = ?`, rel)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return w.treeChanged()
	}
	return nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ErrNotExist can be returned by a Source's Read or Stat for a vanished file.
var ErrNotExist = fs.ErrNotExist

// RetryDuplicates looks again at the files recorded as duplicates, for use
// after a page is deleted or moved: the twin that blocked them may be gone. It
// reads only those files, which are few.
func (x *Index) RetryDuplicates(src Source) error {
	x.wmu.Lock()
	defer x.unlockWrite()
	ctx := context.Background()
	rows, err := x.wdb.QueryContext(ctx, `SELECT path FROM problems WHERE code = 'duplicate_id' ORDER BY path`)
	if err != nil {
		return err
	}
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return err
		}
		paths = append(paths, p)
	}
	rows.Close()
	if len(paths) == 0 {
		return nil
	}
	tx, err := x.wdb.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	w := x.newWriter(ctx, tx)
	defer w.close()
	for _, p := range paths {
		info, err := src.Stat(p)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				if err := w.removePath(p); err != nil {
					return err
				}
			}
			continue
		}
		data, err := src.Read(p)
		if err != nil {
			continue
		}
		e, prob := BuildEntry(p, data, info.MtimeNs)
		if e == nil {
			if err := w.setProblem(info, prob, "", page.Rev(data)); err != nil {
				return err
			}
			continue
		}
		x.annotate(e, x.rules.Load())
		// Every indexed page counts as present: only a twin that is really
		// gone from the index frees the id.
		conflict, err := w.upsert(e, func(string) bool { return true })
		if err != nil {
			return err
		}
		if conflict != "" {
			if err := w.setProblem(info, &page.Problem{Code: "duplicate_id", Message: fmt.Sprintf("id %s is already used by %s", e.ID, conflict)}, e.ID, e.Rev); err != nil {
				return err
			}
		}
	}
	if err := w.finish(); err != nil {
		return err
	}
	return x.commitW(tx, w)
}

// Refresh brings the index in line with the source for exactly these page
// files, and the folder rows of exactly these directories (and their
// ancestors): what a pull or the undoing of a change touched. It reads only
// the files that exist; a path that is gone is removed. Cost: O(len(paths)),
// never a walk of the tree.
//
// Removals come first and insertions after, in path order, so a page that
// moved, two pages that swapped paths and a file whose id changed cannot
// conflict with their own stale rows. An id that a file outside paths still
// holds makes the new file a duplicate, as in Sync.
func (x *Index) Refresh(src Source, paths, dirs []string) (Stats, error) {
	return x.refresh(src, nil, nil, paths, dirs)
}

// Move is Forget(oldPaths, oldDirs) and then Refresh(src, paths, dirs) in one
// write: what moved (a folder and the pages in it) is never seen gone from its
// old place and not yet at its new one, by a read or by the change feed.
func (x *Index) Move(src Source, oldPaths, oldDirs, paths, dirs []string) (Stats, error) {
	return x.refresh(src, oldPaths, oldDirs, paths, dirs)
}

func (x *Index) refresh(src Source, forget, forgetDirs, paths, dirs []string) (Stats, error) {
	start := time.Now()
	ctx := context.Background()
	sort.Strings(paths)
	var todo []FileInfo
	var gone []string
	for i, p := range paths {
		if i > 0 && p == paths[i-1] {
			continue
		}
		info, err := src.Stat(p)
		switch {
		case err == nil:
			todo = append(todo, info)
		case errors.Is(err, fs.ErrNotExist):
			gone = append(gone, p)
		default:
			return Stats{}, err
		}
	}

	x.wmu.Lock()
	defer x.unlockWrite()
	tx, err := x.wdb.BeginTx(ctx, nil)
	if err != nil {
		return Stats{}, err
	}
	defer tx.Rollback()
	w := x.newWriter(ctx, tx)
	defer w.close()

	// The old names are forgotten outright rather than looked up (see Forget).
	for _, p := range forget {
		if err := w.removePath(p); err != nil {
			return Stats{}, err
		}
	}
	for _, d := range forgetDirs {
		if err := w.dropFolder(d); err != nil {
			return Stats{}, err
		}
	}
	st := Stats{}
	for _, p := range gone {
		if err := w.removePath(p); err != nil {
			return Stats{}, err
		}
		st.Removed++
	}
	for _, f := range todo {
		if err := w.removePath(f.Rel); err != nil {
			return Stats{}, err
		}
	}
	onDisk := func(p string) bool { _, err := src.Stat(p); return err == nil }
	for _, r := range x.loadBatch(src, todo, runtime.GOMAXPROCS(0), x.rules.Load()) {
		if r.gone {
			continue
		}
		f := r.info
		if r.entry != nil {
			conflict, err := w.upsert(r.entry, onDisk)
			if err != nil {
				return Stats{}, err
			}
			if conflict != "" {
				r.prob = &page.Problem{Code: "duplicate_id", Message: fmt.Sprintf("id %s is already used by %s", r.entry.ID, conflict)}
				r.dupID, r.rev = r.entry.ID, r.entry.Rev
				r.entry = nil
			}
		}
		if r.entry == nil {
			if err := w.setProblem(f, r.prob, r.dupID, r.rev); err != nil {
				return Stats{}, err
			}
		}
		st.Updated++
	}
	seen := map[string]bool{}
	for _, d := range dirs {
		for ; d != "" && d != "."; d = path.Dir(d) {
			if seen[d] {
				break
			}
			seen[d] = true
			if src.DirExists(d) {
				err = w.addFolder(d)
			} else {
				// A folder that is gone takes its (page-less) descendants with it.
				err = w.dropFolder(d)
			}
			if err != nil {
				return Stats{}, err
			}
		}
	}
	if err := w.finish(); err != nil {
		return Stats{}, err
	}
	if err := x.commitW(tx, w); err != nil {
		return Stats{}, err
	}
	x.wdb.QueryRowContext(ctx, `SELECT count(*) FROM pages`).Scan(&st.Pages)
	x.wdb.QueryRowContext(ctx, `SELECT count(*) FROM problems`).Scan(&st.Problems)
	st.Elapsed = time.Since(start)
	return st, nil
}
