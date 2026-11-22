package index

import (
	"database/sql"
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Filter keeps the pages whose property Key matches. Op is one of:
//
//	in        the property equals one of Values (a list property: holds one of them)
//	notIn     the page has a value for the property and none of them is one of
//	          Values (a board's "other values" column: not empty, not an option)
//	range     Values is [from, to], either end null or "" for none; numbers or
//	          dates (text); a list property matches when an element is in the range
//	empty     the page has no value for the property
//	notEmpty  the page has a value
//	contains  Values[0] is text that a value of the property contains (letters
//	          outside ASCII are matched in the case they are written)
//
// A value is a string, a number (int, int64, float64 or json.Number), a bool,
// or nil.
type Filter struct {
	Key    string
	Op     string
	Values []any

	// Set by checkFilters.
	vals   []string // in: the values as the val column holds them; contains: the needle
	lo, hi *string  // range: the bounds as the skey column holds them
}

const (
	// MaxViewFilters is the most filters a saved view has. A listing takes one
	// more (maxFilters): a board's column adds its own condition to the view's.
	MaxViewFilters = 10
	maxFilters     = MaxViewFilters + 1
	// maxFilterValues is the most values of an in or notIn filter: as many as a
	// select field has options, so that a board's "other values" column can
	// exclude them all.
	maxFilterValues = 500
	maxFilterKey    = 256
)

// checkFilters validates the filters and prepares them for use.
func checkFilters(fs []Filter) error { return checkFiltersMax(fs, maxFilters) }

func checkFiltersMax(fs []Filter, max int) error {
	if len(fs) > max {
		return invalid("at most %d filters", max)
	}
	for i := range fs {
		f := &fs[i]
		if f.Key == "" || len(f.Key) > maxFilterKey || !utf8.ValidString(f.Key) {
			return invalid("a filter needs the key of a property")
		}
		f.vals, f.lo, f.hi = nil, nil, nil
		switch f.Op {
		case "in", "notIn":
			if len(f.Values) == 0 || len(f.Values) > maxFilterValues {
				return invalid("an %s filter needs 1 to %d values", f.Op, maxFilterValues)
			}
			for _, v := range f.Values {
				t, ok := valText(v)
				if !ok {
					return invalid("the values of an %s filter must be text, numbers or true/false", f.Op)
				}
				f.vals = append(f.vals, t)
			}
		case "range":
			if len(f.Values) != 2 {
				return invalid("a range filter needs two values, [from, to], either may be null")
			}
			var kinds [2]string
			for j, v := range f.Values {
				k, key, open, ok := boundKey(v)
				if !ok {
					return invalid("the bounds of a range filter must be numbers, text or null")
				}
				kinds[j] = k
				if open {
					continue
				}
				if j == 0 {
					f.lo = &key
				} else {
					f.hi = &key
				}
			}
			if f.lo == nil && f.hi == nil {
				return invalid("a range filter needs at least one bound")
			}
			if kinds[0] != "" && kinds[1] != "" && kinds[0] != kinds[1] {
				return invalid("the bounds of a range filter must both be numbers or both be text")
			}
		case "empty", "notEmpty":
			if len(f.Values) != 0 {
				return invalid("%s takes no values", f.Op)
			}
		case "contains":
			t, ok := "", len(f.Values) == 1
			if ok {
				t, ok = f.Values[0].(string)
			}
			if !ok || t == "" || len(t) > 1000 || !utf8.ValidString(t) {
				return invalid("a contains filter needs one value, the text to look for")
			}
			f.vals = []string{asciiLower(t)}
		default:
			return invalid("unknown filter op %q (use in, notIn, range, empty, notEmpty or contains)", f.Op)
		}
	}
	return nil
}

func asciiLower(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, s)
}

// numberOf reads a number given as any of the types a JSON decoder or a
// caller produces.
func numberOf(v any) (float64, bool) {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		return f, err == nil && !math.IsInf(f, 0)
	case float64:
		return x, !math.IsNaN(x) && !math.IsInf(x, 0)
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	}
	return 0, false
}

// valText is a value as the val column holds it.
func valText(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return cutText(x), utf8.ValidString(x)
	case bool:
		if x {
			return "true", true
		}
		return "false", true
	}
	if n, ok := v.(json.Number); ok {
		// An integer too large for a float keeps its digits.
		if i, err := n.Int64(); err == nil {
			return strconv.FormatInt(i, 10), true
		}
	}
	if i, ok := v.(int64); ok {
		return strconv.FormatInt(i, 10), true
	}
	if f, ok := numberOf(v); ok {
		return numberText(f), true
	}
	return "", false
}

// boundKey is a range bound as the skey column holds it. kind is "n" for a
// number and "s" for text; open is true for null and for "".
func boundKey(v any) (kind, key string, open, ok bool) {
	switch x := v.(type) {
	case nil:
		return "", "", true, true
	case string:
		if x == "" {
			return "", "", true, true
		}
		return "s", strings.ToLower(cutText(x)), false, utf8.ValidString(x)
	}
	if f, ok := numberOf(v); ok {
		return "n", numberKey(f), false, true
	}
	return "", "", false, false
}

// sql is the condition on the page row p that the filter stands for.
func (f Filter) sql() (string, []any) {
	const has = `EXISTS (SELECT 1 FROM props f WHERE f.page = p.rowid AND f.key = ?`
	switch f.Op {
	case "in":
		marks := strings.TrimSuffix(strings.Repeat("?,", len(f.vals)), ",")
		args := []any{f.Key}
		for _, v := range f.vals {
			args = append(args, v)
		}
		return has + ` AND f.val IN (` + marks + `))`, args
	case "notIn":
		marks := strings.TrimSuffix(strings.Repeat("?,", len(f.vals)), ",")
		args := []any{f.Key, f.Key}
		for _, v := range f.vals {
			args = append(args, v)
		}
		return has + `) AND NOT ` + has + ` AND f.val IN (` + marks + `))`, args
	case "range":
		cond, args := has, []any{f.Key}
		if f.lo != nil {
			cond += ` AND f.skey >= ?`
			args = append(args, *f.lo)
		}
		if f.hi != nil {
			cond += ` AND f.skey <= ?`
			args = append(args, *f.hi)
		}
		return cond + `)`, args
	case "empty":
		return `NOT ` + has + `)`, []any{f.Key}
	case "notEmpty":
		return has + `)`, []any{f.Key}
	default: // contains
		return has + ` AND instr(lower(f.val), ?) > 0)`, []any{f.Key, f.vals[0]}
	}
}

// CheckFilters validates filters the way a listing does, for a caller that
// stores them (a saved view).
func CheckFilters(fs []Filter) error {
	cp := append([]Filter(nil), fs...)
	return checkFiltersMax(cp, MaxViewFilters)
}

// ValidSort reports whether name is a sort a listing accepts: "", updated,
// created, title or prop:<key>, each with an optional leading -.
func ValidSort(name string) error {
	if name == "" {
		return nil
	}
	if _, _, byProp, err := propSort(name); err != nil || byProp {
		return err
	}
	_, err := sortKeys(name)
	return err
}

// propSort reads a sort name of the form prop:<key>, with a leading - to
// reverse. byProp is false for any other name.
func propSort(name string) (key string, desc, byProp bool, err error) {
	rest := strings.TrimPrefix(name, "-")
	if !strings.HasPrefix(rest, "prop:") {
		return "", false, false, nil
	}
	key = strings.TrimPrefix(rest, "prop:")
	if key == "" || len(key) > maxFilterKey || !utf8.ValidString(key) {
		return "", false, false, invalid("sort=prop: needs the key of a property")
	}
	return key, rest != name, true, nil
}

// listByProp lists the pages sorted by the first value of a property: those
// that have one, in the order of the values, then those that have none, by
// rowid. The two groups are read one after the other (the cursor says which
// is being read) so that each is an index range scan that stops at the limit:
// no sort of the folder's pages, however many there are.
func (x *Index) listByProp(q Query, limit int, key string, desc bool, sortName string, cur *cursor, where []string, args []any) (Result, error) {
	res := Result{Items: []Summary{}}
	phase := 0
	if cur != nil {
		phase = cur.G
		if phase != 0 && phase != 1 {
			return Result{}, invalid("bad cursor")
		}
	}
	var last cursor
	if phase == 0 {
		d := ""
		op := ">"
		if desc {
			d, op = " DESC", "<"
		}
		w := append([]string{`s.key = ?`, `s.pos = 0`}, where...)
		a := append([]any{key}, args...)
		if cur != nil {
			w = append(w, `(s.skey, s.page) `+op+` (?, ?)`)
			a = append(a, cur.T, cur.R)
		}
		// CROSS JOIN fixes the order of the loops: the index of the property
		// drives, and each row looks its page up.
		query := `SELECT ` + selectCols(q.Props) + `, s.skey FROM props s CROSS JOIN pages p ON p.rowid = s.page WHERE ` +
			strings.Join(w, ` AND `) + ` ORDER BY s.skey` + d + `, s.page` + d + ` LIMIT ?`
		a = append(a, limit+1)
		more, err := x.scanInto(&res, query, a, q.Props, true, limit, func(rowid int64, skey string) {
			last = cursor{Sort: sortName, T: skey, R: rowid}
		})
		if err != nil {
			return Result{}, err
		}
		if more {
			res.Next = last.encode()
			return res, nil
		}
		last = cursor{Sort: sortName, G: 1}
	} else {
		last = *cur
	}
	// Pages without the property, by rowid.
	w := append([]string{`NOT EXISTS (SELECT 1 FROM props s WHERE s.page = p.rowid AND s.key = ?)`}, where...)
	a := append([]any{key}, args...)
	if phase == 1 && cur != nil {
		w = append(w, `p.rowid > ?`)
		a = append(a, cur.R)
	}
	query := `SELECT ` + selectCols(q.Props) + ` FROM pages p WHERE ` + strings.Join(w, ` AND `) + ` ORDER BY p.rowid LIMIT ?`
	a = append(a, limit-len(res.Items)+1)
	more, err := x.scanInto(&res, query, a, q.Props, false, limit, func(rowid int64, _ string) {
		last = cursor{Sort: sortName, G: 1, R: rowid}
	})
	if err != nil {
		return Result{}, err
	}
	if more {
		res.Next = last.encode()
	}
	return res, nil
}

// scanInto appends the rows of query to res until it holds limit items, and
// reports whether there was a row beyond. seen is called for each item added;
// withKey says the query selects the sort key as a last column, which seen
// gets.
func (x *Index) scanInto(res *Result, query string, args []any, props, withKey bool, limit int, seen func(rowid int64, skey string)) (more bool, err error) {
	x.queries.Add(1)
	rows, err := x.db.Query(query, args...)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		if len(res.Items) == limit {
			return true, nil
		}
		var skey string
		var extra []any
		if withKey {
			extra = []any{&skey}
		}
		s, rowid, _, _, _, _, err := scanSummary(rows, props, extra...)
		if err != nil {
			return false, err
		}
		res.Items = append(res.Items, s)
		seen(rowid, skey)
	}
	return false, rows.Err()
}

// listByOptions lists the pages sorted by the first value of a property whose
// values have an order of their own, the options of a select field. The pages
// are read in groups, one after the other: ascending, each option in turn,
// then the other values (by text), then the pages without the property (by
// rowid); descending, the other values first (by text, reversed), then the
// options from the last, and the pages without the property last as always.
// Each group is an index range scan that stops at the limit, so nothing is
// sorted however many pages there are. The cursor names the group and the
// position in it; it also says how many options there were, so one made for
// another list of options is refused rather than read as something else.
func (x *Index) listByOptions(q Query, limit int, key string, desc bool, sortName string, cur *cursor, where []string, args []any) (Result, error) {
	opts := q.PropOptions
	n := len(opts)
	// The groups, in the order they are read.
	type group struct {
		opt int // the index of the option, -1 for the other values, -2 for the pages without the property
	}
	var groups []group
	if desc {
		groups = append(groups, group{opt: -1})
		for i := n - 1; i >= 0; i-- {
			groups = append(groups, group{opt: i})
		}
	} else {
		for i := 0; i < n; i++ {
			groups = append(groups, group{opt: i})
		}
		groups = append(groups, group{opt: -1})
	}
	groups = append(groups, group{opt: -2})

	// A listing of few pages (a small folder, or a selective filter in a big
	// one) is read once, whole, and put in order here: no query per group.
	if rows, ok := x.smallMatches(q, key, where, args); ok {
		return x.listSmall(q, limit, desc, sortName, cur, opts, rows)
	}
	const fromProps = `props s CROSS JOIN pages p ON p.rowid = s.page`

	g := 0
	if cur != nil {
		if cur.N != n+1 || cur.G < 0 || cur.G >= len(groups) {
			return Result{}, invalid("cursor belongs to another sort order")
		}
		g = cur.G
	}
	res := Result{Items: []Summary{}}
	d, op := "", ">"
	if desc {
		d, op = " DESC", "<"
	}
	var last cursor
	for ; g < len(groups); g++ {
		grp := groups[g]
		var w []string
		var a []any
		var query string
		withKey := false
		remaining := limit - len(res.Items) + 1
		// The position in the cursor applies to the group it names, unless it is
		// the start of that group (no row seen yet: rowids begin at 1).
		inGroup := cur != nil && cur.G == g && cur.R != 0
		switch {
		case grp.opt >= 0:
			w = append([]string{`s.key = ?`, `s.val = ?`, `s.pos = 0`}, where...)
			a = append([]any{key, opts[grp.opt]}, args...)
			if inGroup {
				w = append(w, `s.page `+op+` ?`)
				a = append(a, cur.R)
			}
			query = `SELECT ` + selectCols(q.Props) + ` FROM ` + fromProps + ` WHERE ` +
				strings.Join(w, ` AND `) + ` ORDER BY s.page` + d + ` LIMIT ?`
			a = append(a, remaining)
		case grp.opt == -1:
			marks := strings.TrimSuffix(strings.Repeat("?,", n), ",")
			w = append([]string{`s.key = ?`, `s.pos = 0`, `s.val NOT IN (` + marks + `)`}, where...)
			a = []any{key}
			for _, o := range opts {
				a = append(a, o)
			}
			a = append(a, args...)
			if inGroup {
				w = append(w, `(s.skey, s.page) `+op+` (?, ?)`)
				a = append(a, cur.T, cur.R)
			}
			query = `SELECT ` + selectCols(q.Props) + `, s.skey FROM ` + fromProps + ` WHERE ` +
				strings.Join(w, ` AND `) + ` ORDER BY s.skey` + d + `, s.page` + d + ` LIMIT ?`
			a = append(a, remaining)
			withKey = true
		default:
			w = append([]string{`NOT EXISTS (SELECT 1 FROM props s WHERE s.page = p.rowid AND s.key = ?)`}, where...)
			a = append([]any{key}, args...)
			if inGroup {
				w = append(w, `p.rowid > ?`)
				a = append(a, cur.R)
			}
			query = `SELECT ` + selectCols(q.Props) + ` FROM pages p WHERE ` + strings.Join(w, ` AND `) + ` ORDER BY p.rowid LIMIT ?`
			a = append(a, remaining)
		}
		// The start of this group is where the next page begins if the limit was
		// reached just before its first row.
		last = cursor{Sort: sortName, N: n + 1, G: g}
		more, err := x.scanInto(&res, query, a, q.Props, withKey, limit, func(rowid int64, skey string) {
			last = cursor{Sort: sortName, N: n + 1, G: g, T: skey, R: rowid}
		})
		if err != nil {
			return Result{}, err
		}
		if more {
			res.Next = last.encode()
			return res, nil
		}
	}
	return res, nil
}

// maxSmallScope is the most pages a listing has to be read whole, in memory.
var maxSmallScope = 2000

// smallRow is a page of a small listing with what its place in the order
// depends on: its first value of the property, if it has one.
type smallRow struct {
	rowid int64
	has   bool
	val   string
	skey  string
}

// driver picks the filter that the read of the matching pages starts from: one
// whose rows can be found in an index of the props table (in, by value; range,
// by sort key), the one with the fewest values or the tightest bounds. The
// rows it finds are a superset of the pages that match it, and every
// condition is applied to them all the same. It returns the FROM clause that
// starts the read and the condition and arguments on its rows; ok is false
// when no filter is of this kind.
func driver(fs []Filter) (from, cond string, args []any, ok bool) {
	best, bestCost := -1, 0
	for i, f := range fs {
		cost := 0
		switch f.Op {
		case "in":
			cost = len(f.vals)
		case "range":
			cost = 100
			if f.lo != nil && f.hi != nil {
				cost = 50
			}
		default:
			continue
		}
		if best < 0 || cost < bestCost {
			best, bestCost = i, cost
		}
	}
	if best < 0 {
		return "", "", nil, false
	}
	f := fs[best]
	if f.Op == "in" {
		marks := strings.TrimSuffix(strings.Repeat("?,", len(f.vals)), ",")
		args = []any{f.Key}
		for _, v := range f.vals {
			args = append(args, v)
		}
		return `props d INDEXED BY props_val CROSS JOIN pages p ON p.rowid = d.page`, `d.key = ? AND d.val IN (` + marks + `)`, args, true
	}
	cond, args = `d.key = ?`, []any{f.Key}
	if f.lo != nil {
		cond += ` AND d.skey >= ?`
		args = append(args, *f.lo)
	}
	if f.hi != nil {
		cond += ` AND d.skey <= ?`
		args = append(args, *f.hi)
	}
	return `props d INDEXED BY props_skey CROSS JOIN pages p ON p.rowid = d.page`, cond, args, true
}

// smallMatches reads the pages of a listing of a folder when there are at most
// maxSmallScope of them, each with its first value of the property key, in
// one query. The cost follows the pages that match, not the folder: the read
// starts from the rows of the most selective filter when there is one that an
// index serves, and from the folder's pages otherwise (which the count of a
// folder first shows to be small). ok is false for any other scope or when it
// cannot tell.
func (x *Index) smallMatches(q Query, key string, where []string, args []any) (rows []smallRow, ok bool) {
	if q.Folder == nil {
		return nil, false
	}
	from, cond, dargs, hasDriver := driver(q.Filters)
	if !hasDriver {
		// Nothing to start from but the folder: a folder too large to be read
		// whole is not looked at further (its filters would be walked page by
		// page to count the matches).
		sw, sa := scopeConds(q.Folder, q.Recursive, q.Tags)
		w := ""
		if len(sw) > 0 {
			w = ` WHERE ` + strings.Join(sw, ` AND `)
		}
		var n int
		if err := x.db.QueryRow(`SELECT count(*) FROM (SELECT 1 FROM pages p`+w+` LIMIT ?)`, append(sa, maxSmallScope+1)...).Scan(&n); err != nil || n > maxSmallScope {
			return nil, false
		}
		from = `pages p`
	}
	sel := `SELECT p.rowid, s0.val, s0.skey FROM ` + from +
		` LEFT JOIN props s0 ON s0.page = p.rowid AND s0.key = ? AND s0.pos = 0`
	if hasDriver {
		sel = strings.Replace(sel, `SELECT `, `SELECT DISTINCT `, 1) // a list property can match through several of its elements
	}
	conds := where
	qargs := []any{key}
	if hasDriver {
		conds = append([]string{cond}, where...)
		qargs = append(qargs, dargs...)
	}
	qargs = append(qargs, args...)
	query := sel
	if len(conds) > 0 {
		query += ` WHERE ` + strings.Join(conds, ` AND `)
	}
	query += ` LIMIT ?`
	qargs = append(qargs, maxSmallScope+1)
	x.queries.Add(1)
	r, err := x.db.Query(query, qargs...)
	if err != nil {
		return nil, false
	}
	defer r.Close()
	for r.Next() {
		var row smallRow
		var val, skey sql.NullString
		if r.Scan(&row.rowid, &val, &skey) != nil {
			return nil, false
		}
		row.has, row.val, row.skey = val.Valid, val.String, skey.String
		if rows = append(rows, row); len(rows) > maxSmallScope {
			return nil, false
		}
	}
	if r.Err() != nil {
		return nil, false
	}
	return rows, true
}

// listSmall is listByOptions for pages that smallMatches read: the same order
// (groups of the options, the other values by text, the pages without a
// value) and the same cursor, put in order here, and the summaries of the one
// page that is returned read by rowid.
func (x *Index) listSmall(q Query, limit int, desc bool, sortName string, cur *cursor, opts []string, rows []smallRow) (Result, error) {
	n := len(opts)
	optAt := make(map[string]int, n)
	for i, o := range opts {
		optAt[o] = i
	}
	// The place of the groups in the order they are read (see listByOptions).
	other, none := n, n+1
	groupOf := func(r smallRow) int {
		g := none
		if r.has {
			if i, ok := optAt[r.val]; ok {
				g = i
			} else {
				g = other
			}
		}
		if !desc {
			return g
		}
		switch {
		case g == other:
			return 0
		case g == none:
			return n + 1
		}
		return n - g // the options from the last
	}
	// less orders two rows of the same group.
	type placed struct {
		smallRow
		g int
	}
	ps := make([]placed, len(rows))
	for i, r := range rows {
		ps[i] = placed{r, groupOf(r)}
	}
	isOther := func(g int) bool { return (desc && g == 0) || (!desc && g == other) }
	isNone := func(g int) bool { return g == none }
	// cmp reports a before b; keyed groups order by skey, then rowid; the
	// pages without the property always by rowid ascending.
	cmp := func(a, b placed) int {
		if a.g != b.g {
			if a.g < b.g {
				return -1
			}
			return 1
		}
		c := 0
		switch {
		case isNone(a.g):
			c = cmpInt(a.rowid, b.rowid)
			return c
		case isOther(a.g):
			if a.skey != b.skey {
				c = strings.Compare(a.skey, b.skey)
				if desc {
					c = -c
				}
				return c
			}
		}
		c = cmpInt(a.rowid, b.rowid)
		if desc {
			c = -c
		}
		return c
	}
	if cur != nil {
		if cur.N != n+1 || cur.G < 0 || cur.G >= n+2 {
			return Result{}, invalid("cursor belongs to another sort order")
		}
	}
	// after reports whether a row comes after the cursor.
	after := func(p placed) bool {
		if cur == nil || p.g > cur.G {
			return true
		}
		if p.g < cur.G {
			return false
		}
		if cur.R == 0 {
			return true
		}
		switch {
		case isNone(p.g):
			return p.rowid > cur.R
		case isOther(p.g):
			if p.skey != cur.T {
				if desc {
					return p.skey < cur.T
				}
				return p.skey > cur.T
			}
		}
		if desc {
			return p.rowid < cur.R
		}
		return p.rowid > cur.R
	}
	kept := ps[:0]
	for _, p := range ps {
		if after(p) {
			kept = append(kept, p)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return cmp(kept[i], kept[j]) < 0 })
	more := len(kept) > limit
	var nextRow placed
	if more {
		nextRow = kept[limit]
		kept = kept[:limit]
	}
	res := Result{Items: []Summary{}}
	if len(kept) == 0 {
		return res, nil
	}
	ids := make([]any, len(kept))
	marks := strings.TrimSuffix(strings.Repeat("?,", len(kept)), ",")
	for i, p := range kept {
		ids[i] = p.rowid
	}
	x.queries.Add(1)
	r, err := x.db.Query(`SELECT `+selectCols(q.Props)+` FROM pages p WHERE p.rowid IN (`+marks+`)`, ids...)
	if err != nil {
		return Result{}, err
	}
	defer r.Close()
	byRow := make(map[int64]Summary, len(kept))
	for r.Next() {
		s, rowid, _, _, _, _, err := scanSummary(r, q.Props)
		if err != nil {
			return Result{}, err
		}
		byRow[rowid] = s
	}
	if err := r.Err(); err != nil {
		return Result{}, err
	}
	var last placed
	for _, p := range kept {
		if s, ok := byRow[p.rowid]; ok { // a page that went away in between is not listed
			res.Items = append(res.Items, s)
			last = p
		}
	}
	if more && len(res.Items) > 0 {
		// The listing goes on after the last page listed, or at the start of the
		// next group when that is where the limit was reached (a page written
		// into a group that is passed is then not listed). skey is part of the
		// position only in the group of the other values.
		c := cursor{Sort: sortName, N: n + 1, G: nextRow.g}
		if nextRow.g == last.g {
			c.G, c.R = last.g, last.rowid
			if isOther(last.g) {
				c.T = last.skey
			}
		}
		res.Next = c.encode()
	}
	return res, nil
}

func cmpInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
