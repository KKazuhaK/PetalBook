package index

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrInvalidQuery wraps problems with the parameters of a List call.
var ErrInvalidQuery = errors.New("invalid query")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidQuery, fmt.Sprintf(format, a...))
}

// Summary is a page as listings show it (docs/api.md, "Page summary").
type Summary struct {
	ID       string    `json:"id"`
	Path     string    `json:"path"`
	Folder   string    `json:"folder"`
	Title    string    `json:"title,omitempty"`
	Subtitle string    `json:"subtitle,omitempty"`
	Source   string    `json:"source,omitempty"`
	Label    string    `json:"label"`
	Untitled bool      `json:"untitled"`
	Tags     []string  `json:"tags"`
	Type     string    `json:"type"`
	Created  string    `json:"created,omitempty"`
	Updated  time.Time `json:"updated"`
	Excerpt  string    `json:"excerpt"`
	// Props and Rev are filled only when the query asks for them (Query.Props):
	// the page's properties as a JSON object, and the rev of its file.
	Props json.RawMessage `json:"props,omitempty"`
	Rev   string          `json:"rev,omitempty"`
}

// Query selects pages. The zero value lists the newest 50 pages of the space.
type Query struct {
	Folder    *string // nil: any folder; "" is the root
	Recursive bool    // include subfolders of Folder
	Tags      []string
	Text      string // full-text search
	TitleOnly bool   // with Text: match titles only
	// UpdatedFrom and UpdatedTo bound the time the file was last written, in
	// nanoseconds since the epoch: from inclusive, to exclusive; 0 is no bound.
	UpdatedFrom, UpdatedTo int64
	// CreatedFrom and CreatedTo bound the header's created date (2006-01-02),
	// both inclusive; "" is no bound. A page without that date never matches.
	CreatedFrom, CreatedTo string
	// Filters keep the pages whose properties match every one of them.
	Filters []Filter
	// Props asks for the properties and the rev of every page in the summaries.
	Props bool
	Sort  string // "", updated, created, title, prop:<key>; a leading "-" reverses
	Limit int    // 1-200, default 50
	// Cursor is Result.Next of the previous page.
	Cursor string
}

// Result is one page of a listing.
type Result struct {
	Items []Summary
	Next  string // "" when there is nothing more
}

const (
	defaultLimit = 50
	maxLimit     = 200
)

// cursor is the position after the last item returned. Keyset pagination keeps
// a page cheap however deep it is and stable while pages are being written;
// relevance order has no stable key and uses an offset.
type cursor struct {
	Sort string `json:"s"`
	Off  int    `json:"o,omitempty"`
	G    int    `json:"g,omitempty"` // prop sorts: 1 once the pages without the property are being listed
	M    int64  `json:"m,omitempty"` // mtime_ns
	C    int64  `json:"c,omitempty"` // created_key
	U    int    `json:"u,omitempty"` // untitled
	T    string `json:"t,omitempty"` // title_key; skey in a prop sort
	R    int64  `json:"r,omitempty"` // rowid
}

func (c cursor) encode() string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string) (cursor, error) {
	var c cursor
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || json.Unmarshal(b, &c) != nil {
		return c, invalid("bad cursor")
	}
	return c, nil
}

// sortKey is one column of an ordering.
type sortKey struct {
	expr string
	desc bool
}

// List returns pages matching q. Cost: with a folder or tag filter or none, it
// walks an index in sort order and stops after limit rows; with Text it is
// bounded by the number of matching pages (ranking), which is the one
// O(matches) case; sorting by title descending walks its own index.
func (x *Index) List(q Query) (Result, error) {
	limit := q.Limit
	if limit == 0 {
		limit = defaultLimit
	}
	if limit < 1 || limit > maxLimit {
		return Result{}, invalid("limit must be between 1 and %d", maxLimit)
	}
	sortName := q.Sort
	relevance := false
	match, searching := "", false
	if strings.TrimSpace(q.Text) != "" {
		var ok bool
		if match, ok = MatchQuery(q.Text); !ok {
			return Result{Items: []Summary{}}, nil
		}
		if q.TitleOnly {
			match = "title : (" + match + ")"
		}
		searching = true
		relevance = sortName == ""
	}
	if sortName == "" && !relevance {
		sortName = "-updated"
	}
	var order []sortKey
	propKey, propDesc, byProp, err := propSort(sortName)
	if err != nil {
		return Result{}, err
	}
	if !relevance && !byProp {
		var err error
		if order, err = sortKeys(sortName); err != nil {
			return Result{}, err
		}
	}
	var cur *cursor
	if q.Cursor != "" {
		c, err := decodeCursor(q.Cursor)
		if err != nil {
			return Result{}, err
		}
		if c.Sort != sortName && !(relevance && c.Sort == "relevance") {
			return Result{}, invalid("cursor belongs to another sort order")
		}
		cur = &c
	}

	var (
		from  = `FROM pages p`
		where []string
		args  []any
	)
	if err := checkFilters(q.Filters); err != nil {
		return Result{}, err
	}
	if searching {
		if relevance {
			from = `FROM fts JOIN pages p ON p.rowid = fts.rowid`
			where = append(where, `fts MATCH ?`)
		} else {
			where = append(where, `p.rowid IN (SELECT rowid FROM fts WHERE fts MATCH ?)`)
		}
		args = append(args, match)
	}
	if q.Folder != nil {
		f := *q.Folder
		switch {
		case !q.Recursive:
			where = append(where, `p.folder = ?`)
			args = append(args, f)
		case f != "":
			// Subfolders are the range "f/" .. "f0" ('0' follows '/'), which
			// the (folder, mtime) index can serve.
			where = append(where, `(p.folder = ? OR (p.folder >= ? AND p.folder < ?))`)
			args = append(args, f, f+"/", f+"0")
		}
	}
	for _, t := range q.Tags {
		where = append(where, `EXISTS (SELECT 1 FROM tags t WHERE t.page = p.rowid AND t.tag = ?)`)
		args = append(args, t)
	}
	if q.UpdatedFrom != 0 {
		where = append(where, `p.mtime_ns >= ?`)
		args = append(args, q.UpdatedFrom)
	}
	if q.UpdatedTo != 0 {
		where = append(where, `p.mtime_ns < ?`)
		args = append(args, q.UpdatedTo)
	}
	// `created` holds a date or nothing; dates in this form compare as text.
	if q.CreatedFrom != "" || q.CreatedTo != "" {
		where = append(where, `p.created <> ''`)
	}
	if q.CreatedFrom != "" {
		where = append(where, `p.created >= ?`)
		args = append(args, q.CreatedFrom)
	}
	if q.CreatedTo != "" {
		where = append(where, `p.created <= ?`)
		args = append(args, q.CreatedTo)
	}

	for _, f := range q.Filters {
		cond, cargs := f.sql()
		where = append(where, cond)
		args = append(args, cargs...)
	}
	if byProp {
		return x.listByProp(q, limit, propKey, propDesc, sortName, cur, where, args)
	}

	var orderBy string
	offset := 0
	if relevance {
		// Title matches count more than body matches.
		orderBy = `bm25(fts, 10.0, 5.0, 3.0, 1.0), p.rowid`
		if cur != nil {
			offset = cur.Off
		}
	} else {
		if cur != nil {
			pred, pargs := keysetPredicate(order, cur)
			where = append(where, pred)
			args = append(args, pargs...)
		}
		parts := make([]string, len(order))
		for i, k := range order {
			parts[i] = k.expr + dir(k.desc)
		}
		orderBy = strings.Join(parts, ", ")
	}
	query := `SELECT ` + selectCols(q.Props) + ` ` + from
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	query += ` ORDER BY ` + orderBy + ` LIMIT ?`
	args = append(args, limit+1)
	if relevance {
		query += ` OFFSET ?`
		args = append(args, offset)
	}

	rows, err := x.db.Query(query, args...)
	if err != nil {
		return Result{}, err
	}
	defer rows.Close()
	res := Result{Items: []Summary{}}
	var last cursor
	for rows.Next() {
		s, rowid, mtime, ckey, untitled, titleKey, err := scanSummary(rows, q.Props)
		if err != nil {
			return Result{}, err
		}
		if len(res.Items) == limit {
			// One more row than asked for: there is a next page.
			if relevance {
				res.Next = cursor{Sort: "relevance", Off: offset + limit}.encode()
			} else {
				last.Sort = sortName
				res.Next = last.encode()
			}
			break
		}
		last = cursor{M: mtime, C: ckey, U: untitled, T: titleKey, R: rowid}
		res.Items = append(res.Items, s)
	}
	return res, rows.Err()
}

// selectCols are the columns of a summary, in the order scanSummary reads them.
func selectCols(props bool) string {
	c := `p.rowid, p.id, p.path, p.folder, p.title, p.subtitle, p.source, p.untitled, p.label, p.type, p.tags, p.created,
		p.mtime_ns, p.excerpt, p.title_key, p.created_key`
	if props {
		c += `, p.props, p.rev`
	}
	return c
}

// scanSummary reads one row of selectCols, then the extra columns, if any.
func scanSummary(rows *sql.Rows, props bool, extra ...any) (s Summary, rowid, mtime, ckey int64, untitled int, titleKey string, err error) {
	var tagsText, propsText string
	dest := []any{&rowid, &s.ID, &s.Path, &s.Folder, &s.Title, &s.Subtitle, &s.Source, &untitled, &s.Label, &s.Type,
		&tagsText, &s.Created, &mtime, &s.Excerpt, &titleKey, &ckey}
	if props {
		dest = append(dest, &propsText, &s.Rev)
	}
	dest = append(dest, extra...)
	if err = rows.Scan(dest...); err != nil {
		return
	}
	s.Untitled = untitled == 1
	s.Updated = time.Unix(0, mtime).UTC()
	if err := json.Unmarshal([]byte(tagsText), &s.Tags); err != nil || s.Tags == nil {
		s.Tags = []string{}
	}
	if props {
		if propsText == "" {
			propsText = "{}"
		}
		s.Props = json.RawMessage(propsText)
	}
	return
}

func dir(desc bool) string {
	if desc {
		return " DESC"
	}
	return ""
}

// sortKeys returns the ordering for a sort name. rowid is the last key so the
// order is total; for updated and created it follows the direction of the sort,
// so one index read forwards or backwards serves both directions. Title sorts
// have an index for each direction (rowid ascending in both).
func sortKeys(name string) ([]sortKey, error) {
	desc := strings.HasPrefix(name, "-")
	switch strings.TrimPrefix(name, "-") {
	case "updated":
		return []sortKey{{"p.mtime_ns", desc}, {"p.rowid", desc}}, nil
	case "created":
		return []sortKey{{"p.created_key", desc}, {"p.rowid", desc}}, nil
	case "title":
		// Untitled pages go last in either direction, ordered by created.
		return []sortKey{{"p.untitled", false}, {"p.title_key", desc}, {"p.created_key", desc}, {"p.rowid", false}}, nil
	}
	return nil, invalid("unknown sort %q (use updated, created or title, optionally with a leading -)", name)
}

func keysetPredicate(order []sortKey, c *cursor) (string, []any) {
	vals := make([]any, len(order))
	for i, k := range order {
		switch k.expr {
		case "p.mtime_ns":
			vals[i] = c.M
		case "p.created_key":
			vals[i] = c.C
		case "p.untitled":
			vals[i] = c.U
		case "p.title_key":
			vals[i] = c.T
		case "p.rowid":
			vals[i] = c.R
		}
	}
	same := true
	for _, k := range order {
		if k.desc != order[0].desc {
			same = false
		}
	}
	if same {
		// A row-value comparison lets SQLite seek into the index.
		exprs := make([]string, len(order))
		marks := make([]string, len(order))
		for i, k := range order {
			exprs[i], marks[i] = k.expr, "?"
		}
		op := ">"
		if order[0].desc {
			op = "<"
		}
		return fmt.Sprintf("(%s) %s (%s)", strings.Join(exprs, ", "), op, strings.Join(marks, ", ")), vals
	}
	// Mixed directions: (a > ?) OR (a = ? AND ((b < ?) OR (b = ? AND ...))).
	var build func(i int) (string, []any)
	build = func(i int) (string, []any) {
		op := ">"
		if order[i].desc {
			op = "<"
		}
		cmp := fmt.Sprintf("%s %s ?", order[i].expr, op)
		if i == len(order)-1 {
			return cmp, []any{vals[i]}
		}
		rest, rargs := build(i + 1)
		return fmt.Sprintf("(%s OR (%s = ? AND (%s)))", cmp, order[i].expr, rest),
			append([]any{vals[i], vals[i]}, rargs...)
	}
	return build(0)
}

// FolderCount is a folder with the number of pages directly inside it.
type FolderCount struct {
	Path  string `json:"path"`
	Pages int    `json:"pages"`
}

// Tree returns every known folder, empty ones included. Cost: O(folders), each
// count served by the (folder, mtime) index.
func (x *Index) Tree() ([]FolderCount, error) {
	rows, err := x.db.Query(`SELECT f.path, (SELECT count(*) FROM pages p WHERE p.folder = f.path) FROM folders f ORDER BY f.path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FolderCount{}
	for rows.Next() {
		var f FolderCount
		if err := rows.Scan(&f.Path, &f.Pages); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// TagCount is a tag with the number of pages using it.
type TagCount struct {
	Tag   string `json:"tag"`
	Pages int    `json:"pages"`
}

// Tags returns every tag by use, most used first. Cost: O(tag assignments),
// measured at 15 ms for 50,000 pages in spikes/search-scale.
func (x *Index) Tags() ([]TagCount, error) {
	rows, err := x.db.Query(`SELECT tag, count(*) AS n FROM tags GROUP BY tag ORDER BY n DESC, tag`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TagCount{}
	for rows.Next() {
		var t TagCount
		if err := rows.Scan(&t.Tag, &t.Pages); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Count returns the number of indexed pages.
func (x *Index) Count() (n int, err error) {
	err = x.db.QueryRow(`SELECT count(*) FROM pages`).Scan(&n)
	return
}
