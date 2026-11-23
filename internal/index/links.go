package index

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// BacklinkPage is a page that links to another.
type BacklinkPage struct {
	ID, Path, Title, Label string
	Untitled               bool
}

// Backlinks lists the pages that link to the page id, the most recently
// changed first, limit at a time (cursor "" for the first). A page that links
// twice is listed once; the page itself is not. A link counts when it carries
// the id, or, when it has no id, when its path is the page's. next is "" at the
// end. total is the number of pages that link to it.
//
// Cost: the two index lookups on the target, and one page row for each page
// listed.
func (x *Index) Backlinks(id, cursor string, limit int) (items []BacklinkPage, next string, total int, err error) {
	var curMtime, curRow int64
	if cursor != "" {
		a, b, ok := strings.Cut(cursor, ":")
		var e1, e2 error
		curMtime, e1 = strconv.ParseInt(a, 10, 64)
		curRow, e2 = strconv.ParseInt(b, 10, 64)
		if !ok || e1 != nil || e2 != nil {
			return nil, "", 0, ErrBadCursor
		}
	}
	err = x.one(func(db querier) error {
		var rowid int64
		var path string
		if err := db.QueryRow(`SELECT rowid, path FROM pages WHERE id = ?`, id).Scan(&rowid, &path); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		const from = `(SELECT src FROM links WHERE dst_id = ?1 UNION SELECT src FROM links WHERE dst_id = '' AND dst_path = ?2)`
		if err := db.QueryRow(`SELECT count(*) FROM `+from+` WHERE src <> ?3`, id, path, rowid).Scan(&total); err != nil {
			return err
		}
		q := `SELECT p.rowid, p.mtime_ns, p.id, p.path, p.title, p.label, p.untitled FROM pages p WHERE p.rowid IN ` + from + ` AND p.rowid <> ?3`
		args := []any{id, path, rowid}
		if cursor != "" {
			q += ` AND (p.mtime_ns < ?4 OR (p.mtime_ns = ?4 AND p.rowid > ?5))`
			args = append(args, curMtime, curRow)
		}
		q += ` ORDER BY p.mtime_ns DESC, p.rowid LIMIT ` + strconv.Itoa(limit+1)
		rows, err := db.Query(q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		var lastM, lastR int64
		for rows.Next() {
			var b BacklinkPage
			var r, m int64
			var untitled int
			if err := rows.Scan(&r, &m, &b.ID, &b.Path, &b.Title, &b.Label, &untitled); err != nil {
				return err
			}
			if len(items) == limit {
				next = fmt.Sprintf("%d:%d", lastM, lastR)
				break
			}
			b.Untitled = untitled != 0
			items = append(items, b)
			lastM, lastR = m, r
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", 0, err
	}
	return items, next, total, nil
}

// ErrBadCursor is a cursor that this index did not hand out.
var ErrBadCursor = errors.New("bad cursor")

// OutLink is a link of a page, with the page it leads to now.
type OutLink struct {
	Text, Path, ID, Anchor string
	// Target is the page the link leads to: by its id when the link has one,
	// else by its path. Nil when there is none (a broken link).
	Target *BacklinkPage
}

// OutLinks lists the links of the page id, in the order of the text. ok is
// false when there is no such page.
func (x *Index) OutLinks(id string) (out []OutLink, ok bool, err error) {
	err = x.one(func(db querier) error {
		var rowid int64
		if err := db.QueryRow(`SELECT rowid FROM pages WHERE id = ?`, id).Scan(&rowid); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		ok = true
		rows, err := db.Query(`SELECT dst_id, dst_path, anchor, text FROM links WHERE src = ? ORDER BY pos`, rowid)
		if err != nil {
			return err
		}
		for rows.Next() {
			var l OutLink
			if err := rows.Scan(&l.ID, &l.Path, &l.Anchor, &l.Text); err != nil {
				rows.Close()
				return err
			}
			out = append(out, l)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		// The targets, one lookup per distinct id or path.
		byKey := map[string]*BacklinkPage{}
		for i := range out {
			l := &out[i]
			key, q, arg := "i"+l.ID, `SELECT id, path, title, label, untitled FROM pages WHERE id = ?`, l.ID
			if l.ID == "" {
				key, q, arg = "p"+l.Path, `SELECT id, path, title, label, untitled FROM pages WHERE path = ?`, l.Path
			}
			t, seen := byKey[key]
			if !seen {
				var b BacklinkPage
				var untitled int
				switch err := db.QueryRow(q, arg).Scan(&b.ID, &b.Path, &b.Title, &b.Label, &untitled); {
				case err == nil:
					b.Untitled = untitled != 0
					t = &b
				case errors.Is(err, sql.ErrNoRows):
				default:
					return err
				}
				byKey[key] = t
			}
			l.Target = t
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return out, ok, nil
}

// PageRef names a page.
type PageRef struct{ ID, Path string }

// inChunk is how many values one IN list holds.
const inChunk = 500

func chunks[T any](l []T, f func(part []T) error) error {
	for i := 0; i < len(l); i += inChunk {
		if err := f(l[i:min(i+inChunk, len(l))]); err != nil {
			return err
		}
	}
	return nil
}

func (x *Index) refs(q func(part []string) (string, []any), keys []string) ([]PageRef, error) {
	seen := map[string]bool{}
	var out []PageRef
	err := x.one(func(db querier) error {
		return chunks(keys, func(part []string) error {
			sqlText, args := q(part)
			rows, err := db.Query(sqlText, args...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var r PageRef
				if err := rows.Scan(&r.ID, &r.Path); err != nil {
					return err
				}
				if !seen[r.ID] {
					seen[r.ID] = true
					out = append(out, r)
				}
			}
			return rows.Err()
		})
	})
	return out, err
}

// Referrers lists the pages that have a link to one of the pages with these ids,
// or, without an id, to one of these paths (the paths the pages had).
func (x *Index) Referrers(ids, paths []string) ([]PageRef, error) {
	a, err := x.refs(func(part []string) (string, []any) {
		return `SELECT DISTINCT p.id, p.path FROM links l JOIN pages p ON p.rowid = l.src WHERE l.dst_id IN (` + marks(len(part)) + `)`, anys(part)
	}, ids)
	if err != nil {
		return nil, err
	}
	b, err := x.refs(func(part []string) (string, []any) {
		return `SELECT DISTINCT p.id, p.path FROM links l JOIN pages p ON p.rowid = l.src WHERE l.dst_id = '' AND l.dst_path IN (` + marks(len(part)) + `)`, anys(part)
	}, paths)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []PageRef
	for _, r := range append(a, b...) {
		if !seen[r.ID] {
			seen[r.ID] = true
			out = append(out, r)
		}
	}
	return out, nil
}

// WithLinks keeps, of the pages with these ids, those that have links.
func (x *Index) WithLinks(ids []string) ([]PageRef, error) {
	return x.refs(func(part []string) (string, []any) {
		return `SELECT p.id, p.path FROM pages p WHERE p.id IN (` + marks(len(part)) + `) AND EXISTS (SELECT 1 FROM links WHERE src = p.rowid)`, anys(part)
	}, ids)
}

// PagesAt maps each of these file paths that holds a page to the page's id.
func (x *Index) PagesAt(paths []string) (map[string]string, error) {
	out := make(map[string]string, len(paths))
	err := x.one(func(db querier) error {
		return chunks(paths, func(part []string) error {
			rows, err := db.Query(`SELECT path, id FROM pages WHERE path IN (`+marks(len(part))+`)`, anys(part)...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var p, id string
				if err := rows.Scan(&p, &id); err != nil {
					return err
				}
				out[p] = id
			}
			return rows.Err()
		})
	})
	return out, err
}

// PagesUnder lists the pages in a folder and below it (the root is "").
func (x *Index) PagesUnder(folder string) ([]PageRef, error) {
	var out []PageRef
	err := x.one(func(db querier) error {
		q, args := `SELECT id, path FROM pages ORDER BY path`, []any{}
		if folder != "" {
			q, args = `SELECT id, path FROM pages WHERE path >= ? AND path < ? ORDER BY path`, []any{folder + "/", folder + "0"}
		}
		rows, err := db.Query(q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r PageRef
			if err := rows.Scan(&r.ID, &r.Path); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// Stale is a page with a link that carries the id of a page now at another
// path than the link names.
type Stale struct {
	Src    PageRef // the page with the link
	Target PageRef // the page the id names, and where it is now
}

// StaleLinks lists the links that carry an id and name another path than the
// page with that id has. With ids, only the links to those pages; with nil, all.
func (x *Index) StaleLinks(ids []string) ([]Stale, error) {
	var out []Stale
	const base = `SELECT DISTINCT s.id, s.path, t.id, t.path FROM links l JOIN pages t ON t.id = l.dst_id JOIN pages s ON s.rowid = l.src WHERE l.dst_id <> '' AND l.dst_path <> t.path`
	read := func(db querier, q string, args []any) error {
		rows, err := db.Query(q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s Stale
			if err := rows.Scan(&s.Src.ID, &s.Src.Path, &s.Target.ID, &s.Target.Path); err != nil {
				return err
			}
			out = append(out, s)
		}
		return rows.Err()
	}
	err := x.one(func(db querier) error {
		if ids == nil {
			return read(db, base+` ORDER BY s.path`, nil)
		}
		return chunks(ids, func(part []string) error {
			return read(db, base+` AND l.dst_id IN (`+marks(len(part))+`)`, anys(part))
		})
	})
	return out, err
}

// DuplicateIDs is the set of ids that more than one file claims.
func (x *Index) DuplicateIDs() (map[string]bool, error) {
	out := map[string]bool{}
	err := x.one(func(db querier) error {
		rows, err := db.Query(`SELECT message FROM problems WHERE code = 'duplicate_id'`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m string
			if err := rows.Scan(&m); err != nil {
				return err
			}
			// "id X is already used by P"
			if f := strings.Fields(m); len(f) >= 2 && f[0] == "id" {
				out[f[1]] = true
			}
		}
		return rows.Err()
	})
	return out, err
}

// IDAt is the id of the page at a file path.
func (x *Index) IDAt(rel string) (string, bool) {
	var id string
	err := x.one(func(db querier) error {
		return db.QueryRow(`SELECT id FROM pages WHERE path = ?`, rel).Scan(&id)
	})
	return id, err == nil
}
