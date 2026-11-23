package index

import (
	"crypto/rand"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The change feed (milestone 4.1, docs/api.md "Offline sync").
//
// Every write of a page to the index gives the page row a sequence number
// (pages.seq), higher than any before, inside the write transaction that
// stores the row; a page that is removed leaves a record (gone) with the
// number of the removal. "What changed after a cursor" is then one range read
// of each of two indexes, whatever the size of the space. A cursor is the
// epoch of the index, which is new whenever the index is built from scratch,
// and a sequence number.
//
// The feed keeps its state in the meta table, read at the first need of a
// write and written back by finish in the same transaction; the same values
// are kept in memory, moved when the commit has succeeded (see publish), so
// that the head of the feed is known without a read.

// goneMax and goneAge bound the records of removals: a cursor older than what
// they still hold cannot be answered (ErrExpired). Tests make them smaller.
var (
	goneMax = 100_000
	goneAge = 180 * 24 * time.Hour
)

// Limits of the reads of the feed.
const (
	MaxFolders      = 50
	DefaultCatalog  = 2000
	MaxCatalog      = 5000
	DefaultChanges  = 1000
	MaxChanges      = 5000
	epochLen        = 8
	maxCursorDigits = 18
)

// ErrExpired: the cursor belongs to an index that was built again, or is older
// than the removals the index still holds. The client reads the catalogue
// again.
var ErrExpired = errors.New("the change cursor is too old")

// ErrBadFeedCursor: the string is not a cursor this index made (or is one made for
// something else).
var ErrBadFeedCursor = errors.New("not a cursor")

type headState struct {
	epoch string
	seq   uint64
	tree  uint64
}

func newEpoch() string {
	var b [epochLen / 2]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("index: no randomness: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

func cursorOf(epoch string, seq uint64) string { return epoch + "-" + strconv.FormatUint(seq, 10) }

func treeToken(epoch string, tree uint64) string { return epoch + "." + strconv.FormatUint(tree, 36) }

func isEpoch(s string) bool {
	if len(s) != epochLen {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func parseNumber(s string) (uint64, bool) {
	if s == "" || len(s) > maxCursorDigits {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil
}

// parseCursor reads a change cursor.
func parseCursor(c string) (epoch string, seq uint64, err error) {
	e, n, ok := strings.Cut(c, "-")
	if !ok || !isEpoch(e) {
		return "", 0, ErrBadFeedCursor
	}
	seq, ok = parseNumber(n)
	if !ok {
		return "", 0, ErrBadFeedCursor
	}
	return e, seq, nil
}

func (x *Index) loadHead() error {
	var h headState
	var epoch, seq, tree string
	rows, err := x.wdb.Query(`SELECT key, value FROM meta WHERE key IN ('epoch', 'seq', 'tree')`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return err
		}
		switch k {
		case "epoch":
			epoch = v
		case "seq":
			seq = v
		case "tree":
			tree = v
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	var ok1, ok2 bool
	h.epoch = epoch
	h.seq, ok1 = parseNumber(seq)
	h.tree, ok2 = parseNumber(tree)
	if !isEpoch(epoch) || !ok1 || !ok2 {
		return errors.New("index: the change feed state is not readable")
	}
	x.head.Store(&h)
	return nil
}

// Head is the change cursor the index is at now, from memory.
func (x *Index) Head() string {
	h := x.head.Load()
	return cursorOf(h.epoch, h.seq)
}

// TreeStamp changes whenever a folder is made, moved or removed, or the number
// of pages in a folder changes. From memory.
func (x *Index) TreeStamp() string {
	h := x.head.Load()
	return treeToken(h.epoch, h.tree)
}

// publish moves the head to where the writer left the feed; called inside the
// generation of the commit, after it succeeded.
func (x *Index) publish(w *writer) {
	if !w.fd.loaded {
		return
	}
	x.head.Store(&headState{epoch: w.fd.epoch, seq: w.fd.seq, tree: w.fd.tree})
}

// --- the writer's side --------------------------------------------------------

type feedState struct {
	loaded       bool
	epoch        string
	seq, tree    uint64
	goneN, floor uint64
	dirty        bool // seq, the removals, the epoch or the floor changed
	goneAdded    bool
	pruneAge     bool // look at the age of the removals even if none was added
	recount      bool // goneN drifted below the table: pruneGone counts it again
}

// dropGone takes n records of removals off the count. A count that would go
// below zero has drifted from the table: it stays at zero and is counted again.
func (f *feedState) dropGone(n uint64) {
	if n > f.goneN {
		f.recount = true
	}
	f.goneN -= min(f.goneN, n)
	f.dirty = true
}

// feed loads the state of the feed from the transaction, once.
func (w *writer) feed() (*feedState, error) {
	f := &w.fd
	if f.loaded {
		return f, nil
	}
	rows, err := w.tx.QueryContext(w.ctx, `SELECT key, value FROM meta WHERE key IN ('epoch', 'seq', 'tree', 'gone_n', 'gone_floor')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		n, _ := parseNumber(v)
		switch k {
		case "epoch":
			f.epoch = v
		case "seq":
			f.seq = n
		case "tree":
			f.tree = n
		case "gone_n":
			f.goneN = n
		case "gone_floor":
			f.floor = n
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	f.loaded = true
	return f, nil
}

// nextSeq is the sequence number of the next write of a page.
func (w *writer) nextSeq() (uint64, error) {
	f, err := w.feed()
	if err != nil {
		return 0, err
	}
	f.seq++
	f.dirty = true
	return f.seq, nil
}

// treeChanged notes that the folders, or the number of pages in one, changed.
func (w *writer) treeChanged() error {
	if _, err := w.feed(); err != nil {
		return err
	}
	w.treeDirty = true
	return nil
}

// forgetGone drops the record of a removal of a page that is there again.
func (w *writer) forgetGone(id string) error {
	if w.fresh {
		return nil // the index was emptied in this transaction, the removals with it
	}
	f, err := w.feed()
	if err != nil {
		return err
	}
	res, err := w.exec(`DELETE FROM gone WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		f.dropGone(uint64(n))
	}
	return nil
}

// recordGone leaves the record of the removal of a page, with the sequence
// number of the removal.
func (w *writer) recordGone(id string) error {
	f, err := w.feed()
	if err != nil {
		return err
	}
	seq, err := w.nextSeq()
	if err != nil {
		return err
	}
	res, err := w.exec(`DELETE FROM gone WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		f.dropGone(uint64(n))
	}
	if _, err := w.exec(`INSERT INTO gone(id, seq, at) VALUES (?, ?, ?)`, id, seq, w.now().Unix()); err != nil {
		return err
	}
	f.goneN++
	f.goneAdded = true
	return w.treeChanged()
}

// newEpoch starts a new epoch: the index was emptied to be built again, so
// every cursor made before is void.
func (w *writer) newEpoch() error {
	f, err := w.feed()
	if err != nil {
		return err
	}
	f.epoch = newEpoch()
	f.goneN, f.floor = 0, 0
	f.dirty = true
	if _, err := w.tx.ExecContext(w.ctx, `DELETE FROM gone`); err != nil {
		return err
	}
	return w.treeChanged()
}

// flushFeed writes the state of the feed back, after pruning the removals.
func (w *writer) flushFeed() error {
	f := &w.fd
	if !f.loaded && f.pruneAge {
		if _, err := w.feed(); err != nil {
			return err
		}
	}
	if !f.loaded {
		return nil
	}
	if f.goneAdded || f.pruneAge || f.recount {
		if err := w.pruneGone(); err != nil {
			return err
		}
	}
	if w.treeDirty {
		f.tree++
		f.dirty = true
	}
	if !f.dirty {
		return nil
	}
	for _, kv := range [][2]string{{"epoch", f.epoch}, {"seq", strconv.FormatUint(f.seq, 10)}, {"tree", strconv.FormatUint(f.tree, 10)},
		{"gone_n", strconv.FormatUint(f.goneN, 10)}, {"gone_floor", strconv.FormatUint(f.floor, 10)}} {
		if _, err := w.exec(`INSERT OR REPLACE INTO meta(key, value) VALUES (?, ?)`, kv[0], kv[1]); err != nil {
			return err
		}
	}
	return nil
}

// pruneGone keeps the removals to goneMax and goneAge, and notes in the floor
// the highest sequence number it dropped: a cursor below it cannot be answered.
func (w *writer) pruneGone() error {
	f := &w.fd
	cut := func(upTo uint64) error {
		res, err := w.exec(`DELETE FROM gone WHERE seq <= ?`, upTo)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		f.goneN -= min(f.goneN, uint64(n))
		f.floor = max(f.floor, upTo)
		f.dirty = true
		return nil
	}
	if f.recount {
		var n int64
		if err := w.queryRow(`SELECT count(*) FROM gone`).Scan(&n); err != nil {
			return err
		}
		f.goneN, f.recount, f.dirty = uint64(n), false, true
	}
	var old sql.NullInt64
	if err := w.queryRow(`SELECT max(seq) FROM gone WHERE at < ?`, w.now().Add(-goneAge).Unix()).Scan(&old); err != nil {
		return err
	}
	if old.Valid {
		if err := cut(uint64(old.Int64)); err != nil {
			return err
		}
	}
	if f.goneN > uint64(goneMax) {
		var at int64
		err := w.queryRow(`SELECT seq FROM gone ORDER BY seq LIMIT 1 OFFSET ?`, f.goneN-uint64(goneMax)-1).Scan(&at)
		if errors.Is(err, sql.ErrNoRows) {
			// The count drifted above the table: it is counted again (one read of the index of the table, rare).
			var n int64
			if err := w.queryRow(`SELECT count(*) FROM gone`).Scan(&n); err != nil {
				return err
			}
			f.goneN, f.dirty = uint64(n), true
			if f.goneN <= uint64(goneMax) {
				return nil
			}
			err = w.queryRow(`SELECT seq FROM gone ORDER BY seq LIMIT 1 OFFSET ?`, f.goneN-uint64(goneMax)-1).Scan(&at)
		}
		if err != nil {
			return err
		}
		if err := cut(uint64(at)); err != nil {
			return err
		}
	}
	return nil
}

// --- reading ------------------------------------------------------------------

// CatalogEntry is the light summary of a page that a catalogue and a change
// list hold. Deleted is set, and nothing else but the id, for a page that was
// removed (in a change list).
type CatalogEntry struct {
	ID       string    `json:"id"`
	Path     string    `json:"path"`
	Folder   string    `json:"folder"`
	Title    string    `json:"title,omitempty"`
	Label    string    `json:"label"`
	Untitled bool      `json:"untitled"`
	Tags     []string  `json:"tags"`
	Type     string    `json:"type"`
	Created  string    `json:"created,omitempty"`
	Updated  time.Time `json:"updated"`
	Rev      string    `json:"rev"`
	Deleted  bool      `json:"-"`
}

// MarshalJSON writes a removed page as {"id": …, "deleted": true}.
func (e CatalogEntry) MarshalJSON() ([]byte, error) {
	if e.Deleted {
		return json.Marshal(struct {
			ID      string `json:"id"`
			Deleted bool   `json:"deleted"`
		}{e.ID, true})
	}
	type plain CatalogEntry
	return json.Marshal(plain(e))
}

const catalogCols = `p.rowid, p.id, p.path, p.folder, p.title, p.untitled, p.label, p.type, p.tags, p.created, p.mtime_ns, p.rev`

func scanCatalog(rows *sql.Rows, seq bool) (e CatalogEntry, rowid, sq int64, err error) {
	var untitled int
	var mtime int64
	var tags string
	dest := []any{&rowid, &e.ID, &e.Path, &e.Folder, &e.Title, &untitled, &e.Label, &e.Type, &tags, &e.Created, &mtime, &e.Rev}
	if seq {
		dest = append(dest, &sq)
	}
	if err = rows.Scan(dest...); err != nil {
		return
	}
	e.Untitled = untitled == 1
	e.Updated = time.Unix(0, mtime).UTC()
	if json.Unmarshal([]byte(tags), &e.Tags) != nil || e.Tags == nil {
		e.Tags = []string{}
	}
	return
}

// feedMeta is the state of the feed in a snapshot.
type feedMeta struct {
	epoch            string
	seq, tree, floor uint64
}

func (r *read) feedMeta() (feedMeta, error) {
	var m feedMeta
	rows, err := r.db.Query(`SELECT key, value FROM meta WHERE key IN ('epoch', 'seq', 'tree', 'gone_floor')`)
	if err != nil {
		return m, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return m, err
		}
		n, _ := parseNumber(v)
		switch k {
		case "epoch":
			m.epoch = v
		case "seq":
			m.seq = n
		case "tree":
			m.tree = n
		case "gone_floor":
			m.floor = n
		}
	}
	return m, rows.Err()
}

// Batch is a batch of a catalogue.
type Batch struct {
	Items []CatalogEntry
	// Next is the cursor of the following batch, "" after the last.
	Next string
	// Changes is the change cursor as it was at the start of the walk, in the
	// first batch only.
	Changes string
	// Tree is the stamp of the folders (see TreeStamp).
	Tree string
}

func foldersKey(folders []string) string {
	s := slices.Clone(folders)
	slices.Sort(s)
	h := sha1.Sum([]byte(strings.Join(s, "\x00")))
	return hex.EncodeToString(h[:4])
}

// NormalizeFolders trims, sorts and de-duplicates the folders of a catalogue
// request; the root, "", makes it the whole space (nil).
func NormalizeFolders(in []string) []string {
	var out []string
	for _, f := range in {
		f = strings.Trim(f, "/")
		if f == "" {
			return nil
		}
		out = append(out, f)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// Catalog reads a batch of the catalogue of the pages in folders (and below;
// nil: all), after the cursor ("" for the first batch), at most limit entries
// in the order the index holds them. It is one range read of the index.
func (x *Index) Catalog(folders []string, cursor string, limit int) (Batch, error) {
	if limit <= 0 {
		limit = DefaultCatalog
	}
	limit = min(limit, MaxCatalog)
	folders = NormalizeFolders(folders)
	fkey := foldersKey(folders)
	r, err := x.begin()
	if err != nil {
		return Batch{}, err
	}
	defer r.end()
	m, err := r.feedMeta()
	if err != nil {
		return Batch{}, err
	}
	var after int64
	if cursor != "" {
		parts := strings.Split(cursor, ".")
		if len(parts) != 3 || !isEpoch(parts[0]) || parts[2] != fkey {
			return Batch{}, ErrBadFeedCursor
		}
		n, ok := parseNumber(parts[1])
		if !ok {
			return Batch{}, ErrBadFeedCursor
		}
		if parts[0] != m.epoch {
			return Batch{}, ErrExpired
		}
		after = int64(n)
	}
	q := `SELECT ` + catalogCols + ` FROM pages p WHERE p.rowid > ?`
	args := []any{after}
	if len(folders) > 0 {
		var ors []string
		for _, f := range folders {
			ors = append(ors, `(p.folder = ? OR (p.folder > ? AND p.folder < ?))`)
			args = append(args, f, f+"/", f+"0")
		}
		q += ` AND (` + strings.Join(ors, ` OR `) + `)`
	}
	q += ` ORDER BY p.rowid LIMIT ?`
	args = append(args, limit+1)
	rows, err := r.db.Query(q, args...)
	if err != nil {
		return Batch{}, err
	}
	defer rows.Close()
	b := Batch{Items: []CatalogEntry{}, Tree: treeToken(m.epoch, m.tree)}
	var last int64
	for rows.Next() {
		e, rowid, _, err := scanCatalog(rows, false)
		if err != nil {
			return Batch{}, err
		}
		if len(b.Items) == limit {
			b.Next = m.epoch + "." + strconv.FormatInt(last, 10) + "." + fkey
			break
		}
		b.Items, last = append(b.Items, e), rowid
	}
	if err := rows.Err(); err != nil {
		return Batch{}, err
	}
	if cursor == "" {
		b.Changes = cursorOf(m.epoch, m.seq)
	}
	return b, nil
}

// ChangeList is what changed after a cursor.
type ChangeList struct {
	Items []CatalogEntry
	// Next is the cursor to keep.
	Next string
	More bool
	Tree string
}

// Changes reads what changed after the change cursor since: the pages added,
// changed, renamed or moved, and the pages removed, in the order they last
// changed, at most limit. It costs in proportion to what it returns.
func (x *Index) Changes(since string, limit int) (ChangeList, error) {
	if limit <= 0 {
		limit = DefaultChanges
	}
	limit = min(limit, MaxChanges)
	epoch, seq, err := parseCursor(since)
	if err != nil {
		return ChangeList{}, err
	}
	r, err := x.begin()
	if err != nil {
		return ChangeList{}, err
	}
	defer r.end()
	m, err := r.feedMeta()
	if err != nil {
		return ChangeList{}, err
	}
	if epoch != m.epoch || seq < m.floor || seq > m.seq {
		return ChangeList{}, ErrExpired
	}
	out := ChangeList{Items: []CatalogEntry{}, Tree: treeToken(m.epoch, m.tree)}
	if seq == m.seq {
		out.Next = since
		return out, nil
	}
	type item struct {
		e   CatalogEntry
		seq int64
	}
	var items []item
	rows, err := r.db.Query(`SELECT `+catalogCols+`, p.seq FROM pages p WHERE p.seq > ? ORDER BY p.seq LIMIT ?`, seq, limit+1)
	if err != nil {
		return ChangeList{}, err
	}
	for rows.Next() {
		e, _, sq, err := scanCatalog(rows, true)
		if err != nil {
			rows.Close()
			return ChangeList{}, err
		}
		items = append(items, item{e, sq})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return ChangeList{}, err
	}
	grows, err := r.db.Query(`SELECT id, seq FROM gone WHERE seq > ? ORDER BY seq LIMIT ?`, seq, limit+1)
	if err != nil {
		return ChangeList{}, err
	}
	onPage := make(map[string]bool, len(items))
	for _, it := range items {
		onPage[it.e.ID] = true
	}
	for grows.Next() {
		var it item
		if err := grows.Scan(&it.e.ID, &it.seq); err != nil {
			grows.Close()
			return ChangeList{}, err
		}
		if onPage[it.e.ID] {
			continue // made again since: a page, not a removal
		}
		it.e.Deleted = true
		items = append(items, it)
	}
	grows.Close()
	if err := grows.Err(); err != nil {
		return ChangeList{}, err
	}
	slices.SortFunc(items, func(a, b item) int { return int(a.seq - b.seq) })
	if len(items) > limit {
		out.More, items = true, items[:limit]
	}
	for _, it := range items {
		out.Items = append(out.Items, it.e)
	}
	if out.More {
		out.Next = cursorOf(m.epoch, uint64(items[len(items)-1].seq))
	} else {
		out.Next = cursorOf(m.epoch, m.seq)
	}
	return out, nil
}
