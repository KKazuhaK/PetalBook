package index

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
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
	Sources  []string  `json:"source"`
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
	// PartialProps are the keys of Props whose value there is not the whole of
	// what the header holds (cut text, a list that lost elements, an object):
	// a table must not write them back. Absent when there are none.
	PartialProps []string `json:"partialProps,omitempty"`
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
	// PropOptions, with a sort by a property, puts the pages in the order of
	// these options (the options of a select or multiselect field) instead of
	// the order of the text: the options in turn, then the other values by
	// text, then the pages without a value. Empty: the order of the text.
	PropOptions []string
	Sort        string // "", updated, created, title, prop:<key>; a leading "-" reverses
	Limit       int    // 1-200, default 50
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
	G    int    `json:"g,omitempty"` // prop sorts: 1 once the pages without the property are being listed; in an option order, the group
	M    int64  `json:"m,omitempty"` // mtime_ns
	C    int64  `json:"c,omitempty"` // created_key
	U    int    `json:"u,omitempty"` // untitled
	N    int    `json:"n,omitempty"` // option-order prop sorts: the number of options plus one
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
		return c, invalid("Bad cursor.")
	}
	return c, nil
}

// sortKey is one column of an ordering.
type sortKey struct {
	expr string
	desc bool
}

// List returns pages matching q. Cost: with no condition but a folder and the
// order of time of writing, or none at all, it walks the index of the order
// and stops after limit rows. With tags, filters, dates, a recursive folder or
// an order by a property it is read in the cheapest of three ways (plan.go):
// from the smallest condition, by a bounded walk of the order, or as the
// intersection of the conditions' index ranges, whose cost is the size of
// those ranges and not the number of pages looked up. With Text and no sort it
// is bounded by the number of matching pages (ranking).
func (x *Index) List(q Query) (Result, error) {
	limit := q.Limit
	if limit == 0 {
		limit = defaultLimit
	}
	if limit < 1 || limit > maxLimit {
		return Result{}, invalid("Limit must be between 1 and %d.", maxLimit)
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
			return Result{}, invalid("Cursor belongs to another sort order.")
		}
		cur = &c
	}

	if err := checkFilters(q.Filters); err != nil {
		return Result{}, err
	}
	conds := conditions(q.Folder, q.Recursive, q.Tags, q.Filters)
	if q.UpdatedFrom != 0 || q.UpdatedTo != 0 {
		c := cond{sets: []pageSet{{sql: `SELECT rowid AS id FROM pages WHERE 1 = 1`}}}
		var w []string
		if q.UpdatedFrom != 0 {
			w, c.args = append(w, `p.mtime_ns >= ?`), append(c.args, q.UpdatedFrom)
			c.sets[0].sql += ` AND mtime_ns >= ?`
		}
		if q.UpdatedTo != 0 {
			w, c.args = append(w, `p.mtime_ns < ?`), append(c.args, q.UpdatedTo)
			c.sets[0].sql += ` AND mtime_ns < ?`
		}
		c.where, c.sets[0].args = strings.Join(w, ` AND `), c.args
		conds = append(conds, c)
	}
	// `created` holds a date or nothing; dates in this form compare as text.
	if q.CreatedFrom != "" || q.CreatedTo != "" {
		c := cond{where: `p.created <> ''`}
		// The index is of created_key, which for a page with a date is that
		// date, and for one without is something else: its range holds the
		// pages that match and maybe others, so it is a place to start only.
		lead := pageSet{sql: `SELECT rowid AS id FROM pages WHERE 1 = 1`}
		for _, b := range []struct{ day, op string }{{q.CreatedFrom, `>=`}, {q.CreatedTo, `<=`}} {
			if b.day == "" {
				continue
			}
			c.where += ` AND p.created ` + b.op + ` ?`
			c.args = append(c.args, b.day)
			if t, err := time.Parse("2006-01-02", b.day); err == nil && lead.sql != "" {
				lead.sql += ` AND created_key ` + b.op + ` ?`
				lead.args = append(lead.args, t.UnixNano())
			} else {
				lead.sql = ""
			}
		}
		if lead.sql != "" {
			c.lead = &lead
		}
		conds = append(conds, c)
	}
	if relevance {
		return x.listDirect(q, limit, sortName, nil, cur, match, conds)
	}
	o := &ordering{name: sortName, keys: order, key: propKey, desc: propDesc}
	if byProp {
		o.opts = q.PropOptions
	}
	// The order of time of writing inside one folder is an index of its own.
	inFolder := q.Folder != nil && !q.Recursive && strings.TrimPrefix(sortName, "-") == "updated"
	if inFolder {
		o.folder = q.Folder
	}
	if !byProp && !searching && (len(conds) == 0 || (len(conds) == 1 && inFolder)) {
		// Nothing to look up: the walk of the index is the listing.
		return x.listDirect(q, limit, sortName, order, cur, "", conds)
	}
	if searching {
		// The words of a search are a set of pages like any other condition,
		// but finding them is not a lookup by page: they are read once.
		conds = append([]cond{{where: `p.rowid IN (SELECT rowid FROM fts WHERE fts MATCH ?)`, args: []any{match},
			sets: []pageSet{{sql: `SELECT rowid AS id FROM fts WHERE fts MATCH ?`, args: []any{match}}}}}, conds...)
	}
	// The queries of the listing all read one snapshot (read.go).
	r, err := x.begin()
	if err != nil {
		return Result{}, err
	}
	defer r.end()
	return r.listPlanned(q, limit, o, cur, conds, searching)
}

// listDirect reads a page of a listing as one query: the index of the order
// is walked and each row checked. It is for a listing with no condition but
// the one its index holds (the cost is the page), and for a search in order
// of relevance, whose cost is the number of pages that match the words.
func (x *Index) listDirect(q Query, limit int, sortName string, order []sortKey, cur *cursor, match string, conds []cond) (Result, error) {
	relevance := order == nil
	from := `FROM pages p`
	var where []string
	var args []any
	if relevance {
		from = `FROM fts JOIN pages p ON p.rowid = fts.rowid`
		where, args = append(where, `fts MATCH ?`), append(args, match)
	}
	if w, a := whereOf(conds); w != "" {
		where, args = append(where, w), append(args, a...)
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

	res := Result{Items: []Summary{}}
	err := x.one(func(db querier) error { // one query, read like the others (read.go)
		rows, err := db.Query(query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		return res.fill(rows, q.Props, limit, relevance, offset, sortName)
	})
	if err != nil {
		return Result{}, err
	}
	return res, nil
}

// fill reads the rows of a listing of one query into res: limit of them, and
// the cursor of the next page when there is one more.
func (res *Result) fill(rows *sql.Rows, props bool, limit int, relevance bool, offset int, sortName string) error {
	var last cursor
	for rows.Next() {
		s, rowid, mtime, ckey, untitled, titleKey, err := scanSummary(rows, props)
		if err != nil {
			return err
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
	return rows.Err()
}

// selectCols are the columns of a summary, in the order scanSummary reads them.
func selectCols(props bool) string {
	c := `p.rowid, p.id, p.path, p.folder, p.title, p.subtitle, p.source, p.untitled, p.label, p.type, p.tags, p.created,
		p.mtime_ns, p.excerpt, p.title_key, p.created_key`
	if props {
		c += `, p.props, p.rev, p.partial`
	}
	return c
}

// scanSummary reads one row of selectCols, then the extra columns, if any.
func scanSummary(rows *sql.Rows, props bool, extra ...any) (s Summary, rowid, mtime, ckey int64, untitled int, titleKey string, err error) {
	var tagsText, sourceText, propsText, partialText string
	dest := []any{&rowid, &s.ID, &s.Path, &s.Folder, &s.Title, &s.Subtitle, &sourceText, &untitled, &s.Label, &s.Type,
		&tagsText, &s.Created, &mtime, &s.Excerpt, &titleKey, &ckey}
	if props {
		dest = append(dest, &propsText, &s.Rev, &partialText)
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
	if err := json.Unmarshal([]byte(sourceText), &s.Sources); err != nil || s.Sources == nil {
		s.Sources = []string{}
	}
	if props {
		if propsText == "" {
			propsText = "{}"
		}
		s.Props = json.RawMessage(propsText)
		if partialText != "" {
			json.Unmarshal([]byte(partialText), &s.PartialProps)
		}
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
	return nil, invalid("Unknown sort %q (use updated, created or title, optionally with a leading -).", name)
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
	out := []FolderCount{}
	err := x.one(func(db querier) error { // one query, read like the others (read.go)
		rows, err := db.Query(`SELECT f.path, (SELECT count(*) FROM pages p WHERE p.folder = f.path) FROM folders f ORDER BY f.path`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var f FolderCount
			if err := rows.Scan(&f.Path, &f.Pages); err != nil {
				return err
			}
			out = append(out, f)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// TagCount is a tag with the number of pages using it.
type TagCount struct {
	Tag   string `json:"tag"`
	Pages int    `json:"pages"`
}

// Tags returns every tag by use, most used first. Cost: O(tag assignments),
// measured at 15 ms for 50,000 pages in spikes/search-scale.
func (x *Index) Tags() ([]TagCount, error) {
	out := []TagCount{}
	err := x.one(func(db querier) error { // one query, read like the others (read.go)
		rows, err := db.Query(`SELECT tag, count(*) AS n FROM tags GROUP BY tag ORDER BY n DESC, tag`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t TagCount
			if err := rows.Scan(&t.Tag, &t.Pages); err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SourceCount is a source with the number of pages that have it.
type SourceCount struct {
	Source string `json:"source"`
	Pages  int    `json:"pages"`
}

// maxUsedSources is the most sources UsedSources lists.
const maxUsedSources = 500

// UsedSources returns the sources the pages have, most used first, for the
// suggestions of an input. The sources are rows of the props table (see
// BuildEntry), and props_val serves the grouping.
func (x *Index) UsedSources() ([]SourceCount, error) {
	out := []SourceCount{}
	err := x.one(func(db querier) error {
		rows, err := db.Query(`SELECT val, count(*) AS n FROM props INDEXED BY props_val WHERE key = 'source' GROUP BY val ORDER BY n DESC, val LIMIT ?`, maxUsedSources)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t SourceCount
			if err := rows.Scan(&t.Source, &t.Pages); err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Count returns the number of indexed pages.
func (x *Index) Count() (n int, err error) {
	err = x.one(func(db querier) error { // one query, read like the others (read.go)
		return db.QueryRow(`SELECT count(*) FROM pages`).Scan(&n)
	})
	return
}

// CountFile counts the pages of the index database at dbPath without opening an
// Index: for a space that is not open, whose index is only read. ok is false
// when there is no such file, or it is no index (a space never opened, or a
// cache that was cleared); the count of such a space is not known.
func CountFile(dbPath string) (n int, ok bool, err error) {
	if _, err := os.Stat(dbPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, err
	}
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=query_only(1)")
	if err != nil {
		return 0, false, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := db.QueryRow(`SELECT count(*) FROM pages`).Scan(&n); err != nil {
		return 0, false, err
	}
	return n, true, nil
}
