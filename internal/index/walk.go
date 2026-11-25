package index

import (
	"database/sql"
	"slices"
	"sort"
	"strings"
	"time"
)

// ordering is the order of a listing: by columns of the page row (updated,
// created, title), or by the first value of a property.
type ordering struct {
	name string // the sort, as a cursor names it
	// keys is an order by columns of the page; nil for an order by a property.
	keys []sortKey
	// folder, with keys, is the one folder the listing is about when the order
	// is by time of writing: the index of the folder holds that order, so the
	// walk stays inside it.
	folder *string
	// key is the property of an order by a property; opts, when not empty, the
	// options whose order comes before that of the text.
	key  string
	desc bool
	opts []string
}

// An order by a property is read in groups, one after the other. In the order
// of the options (ascending): each option in turn, then the other values (by
// text), then the pages without the property (by rowid). Descending: the other
// values first (by text, reversed), then the options from the last, and the
// pages without the property last as always. With no options there are two
// groups: the pages that have a value, by its text, and those that have none.
// Each group is a range of an index, in the order of the listing. The cursor
// names the group and the position in it; it also says how many options there
// were, so one made for another list of options is refused rather than read as
// something else.
type segKind int

const (
	segPages  segKind = iota // an order by columns of the page: the one segment
	segOption                // the pages whose first value is one option
	segOther                 // the pages whose first value is not an option
	segNone                  // the pages without a value
)

// segment is a stretch of a listing that one index range holds in order.
type segment struct {
	kind segKind
	g    int // the group, as the cursor numbers it
	// stream yields the ids of the segment's pages after the cursor, in order.
	// For segOther and segNone it yields more: extra says which of them belong.
	stream string
	args   []any
	extra  *cond
	// in says the cursor names a place inside the segment: after the page
	// after, whose sort key is t (segOther).
	in    bool
	after int64
	t     string
}

// tag is what a cursor says about the options: how many there were, plus one.
func (o *ordering) tag() int {
	if len(o.opts) == 0 {
		return 0
	}
	return len(o.opts) + 1
}

// kindOf says what group g is, and which option.
func (o *ordering) kindOf(g int) (segKind, int) {
	n := len(o.opts)
	switch {
	case g == n+1:
		return segNone, 0
	case o.desc && g == 0, !o.desc && g == n:
		return segOther, 0
	case o.desc:
		return segOption, n - g
	}
	return segOption, g
}

// groupOf is the group of a page whose first value is val (has: it has one).
func (o *ordering) groupOf(optAt map[string]int, has bool, val string) int {
	n := len(o.opts)
	if !has {
		return n + 1
	}
	i, ok := optAt[val]
	switch {
	case !ok && o.desc:
		return 0
	case !ok:
		return n
	case o.desc:
		return n - i
	}
	return i
}

func orderBy(keys []sortKey) string {
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k.expr + dir(k.desc)
	}
	return strings.Join(parts, ", ")
}

// segments are the stretches of the listing from the cursor on.
func (o *ordering) segments(cur *cursor) ([]segment, error) {
	if o.keys != nil {
		var w []string
		var a []any
		if o.folder != nil {
			w, a = append(w, `p.folder = ?`), append(a, *o.folder)
		}
		if cur != nil {
			pred, pargs := keysetPredicate(o.keys, cur)
			w, a = append(w, pred), append(a, pargs...)
		}
		q := `SELECT p.rowid AS id FROM pages p`
		if len(w) > 0 {
			q += ` WHERE ` + strings.Join(w, ` AND `)
		}
		return []segment{{kind: segPages, stream: q + ` ORDER BY ` + orderBy(o.keys), args: a}}, nil
	}
	n := len(o.opts)
	first := 0
	if cur != nil {
		if cur.N != o.tag() {
			return nil, invalid("Cursor belongs to another sort order.")
		}
		if cur.G < 0 || cur.G >= n+2 {
			if n == 0 {
				return nil, invalid("Bad cursor.")
			}
			return nil, invalid("Cursor belongs to another sort order.")
		}
		first = cur.G
	}
	d, op := "", ">"
	if o.desc {
		d, op = " DESC", "<"
	}
	var segs []segment
	for g := first; g < n+2; g++ {
		// The position in the cursor applies to the group it names, unless it is
		// the start of that group (no row seen yet: rowids begin at 1).
		in := cur != nil && cur.G == g && cur.R != 0
		kind, opt := o.kindOf(g)
		s := segment{kind: kind, g: g, in: in}
		if in {
			s.after, s.t = cur.R, cur.T
		}
		switch kind {
		case segOption:
			s.stream = `SELECT s.page AS id FROM props s INDEXED BY props_val WHERE s.key = ? AND s.val = ? AND s.pos = 0`
			s.args = []any{o.key, o.opts[opt]}
			if in {
				s.stream += ` AND s.page ` + op + ` ?`
				s.args = append(s.args, cur.R)
			}
			s.stream += ` ORDER BY s.page` + d
		case segOther:
			s.stream = `SELECT s.page AS id FROM props s INDEXED BY props_sort WHERE s.key = ? AND s.pos = 0`
			s.args = []any{o.key}
			if in {
				s.stream += ` AND (s.skey, s.page) ` + op + ` (?, ?)`
				s.args = append(s.args, cur.T, cur.R)
			}
			s.stream += ` ORDER BY s.skey` + d + `, s.page` + d
			if n > 0 {
				// The plus keeps the index of values out of it: a lookup by (page, key, pos).
				s.extra = &cond{where: `NOT EXISTS (SELECT 1 FROM props o WHERE o.page = p.rowid AND o.key = ? AND o.pos = 0 AND +o.val IN (` + marks(n) + `))`,
					args: append([]any{o.key}, anys(o.opts)...)}
			}
		default:
			s.stream = `SELECT p.rowid AS id FROM pages p`
			if in {
				s.stream += ` WHERE p.rowid > ?`
				s.args = []any{cur.R}
			}
			s.stream += ` ORDER BY p.rowid`
			s.extra = &cond{where: `NOT EXISTS (SELECT 1 FROM props s WHERE s.page = p.rowid AND s.key = ?)`, args: []any{o.key}}
		}
		segs = append(segs, s)
	}
	return segs, nil
}

// after is the cursor that names the place just after a page of the listing.
func (o *ordering) after(r *row) cursor {
	if o.keys != nil {
		return cursor{Sort: o.name, M: r.mtime, C: r.ckey, U: r.untitled, T: r.titleKey, R: r.rowid}
	}
	c := cursor{Sort: o.name, N: o.tag(), G: r.seg.g, R: r.rowid}
	if r.seg.kind == segOther {
		c.T = r.skey
	}
	return c
}

// result is the page of the listing that rows are. next is the segment of the
// page that follows them, nil when there is none: the cursor names the place
// after the last row, or the start of next when that is another group (a page
// written meanwhile into the group just finished is then not listed after it).
func (o *ordering) result(rows []row, next *segment) Result {
	res := Result{Items: []Summary{}}
	for i := range rows {
		res.Items = append(res.Items, rows[i].s)
	}
	if next != nil && len(rows) > 0 {
		last := &rows[len(rows)-1]
		c := o.after(last)
		if next.g != last.seg.g {
			c = cursor{Sort: o.name, N: o.tag(), G: next.g}
		}
		res.Next = c.encode()
	}
	return res
}

// row is a page read for a listing, with what a cursor says of it.
type row struct {
	s           Summary
	rowid       int64
	mtime, ckey int64
	untitled    int
	titleKey    string
	skey        string   // the sort key of its first value of the property sorted by
	seg         *segment // the segment it was read in
}

// pagesOf reads the summaries of the pages ids names, in the order of ids,
// leaving out those that do not meet conds, and stops after max. The loop over
// ids is the outer one (CROSS JOIN fixes that), so the rows come in its order
// and the read ends as soon as max pages are found.
func (x *read) pagesOf(ids []int64, conds []cond, props bool, key string, max int) ([]row, error) {
	if len(ids) == 0 || max <= 0 {
		return nil, nil
	}
	query := `SELECT ` + selectCols(props)
	args := []any{jsonIDs(ids)}
	from := ` FROM json_each(?) j CROSS JOIN pages p ON p.rowid = j.value`
	if key != "" {
		query += `, s0.skey`
		from += ` LEFT JOIN props s0 ON s0.page = p.rowid AND s0.key = ? AND s0.pos = 0`
		args = append(args, key)
	}
	query += from
	if where, wargs := whereOf(conds); where != "" {
		query += ` WHERE ` + where
		args = append(args, wargs...)
	}
	query += ` LIMIT ?`
	args = append(args, max)
	return x.readRows(query, args, props, key != "")
}

// readRows runs a query of selectCols, and of the sort key when withKey.
func (x *read) readRows(query string, args []any, props, withKey bool) ([]row, error) {
	x.ran(query, args)
	rs, err := x.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []row
	for rs.Next() {
		var r row
		var skey sql.NullString
		var extra []any
		if withKey {
			extra = []any{&skey}
		}
		if r.s, r.rowid, r.mtime, r.ckey, r.untitled, r.titleKey, err = scanSummary(rs, props, extra...); err != nil {
			return nil, err
		}
		r.skey = skey.String
		out = append(out, r)
	}
	return out, rs.Err()
}

// prune looks at which values the property has, when they are few enough to
// list (a select field: its options and some more), and with that leaves out
// the segments of options no page has, so a field of hundreds of options of
// which a handful are in use is not asked about each, and reads the segment of
// the other values from the rows of those values when they are few: the
// "other values" that come first in a descending order are then not looked for
// among every page that has an option.
func (x *read) prune(o *ordering, segs []segment) ([]segment, error) {
	n := len(o.opts)
	if n == 0 {
		return segs, nil
	}
	vals, whole, err := x.valuesOf(o.key, n+maxDistinct)
	if err != nil || !whole {
		return segs, err
	}
	used := make(map[string]bool, len(vals))
	for _, v := range vals {
		used[v] = true
	}
	var others []string
	for _, v := range vals {
		if !slices.Contains(o.opts, v) {
			others = append(others, v)
		}
	}
	out := segs[:0:0]
	for _, s := range segs {
		switch _, opt := o.kindOf(s.g); {
		case s.kind == segOption && !used[o.opts[opt]]:
			continue
		case s.kind == segOther && len(others) == 0:
			continue
		case s.kind == segOther:
			first := valueSet(o.key, others)
			first.sql += ` AND pos = 0`
			c, err := x.countUpTo(&first, x.plan.lead)
			if err != nil {
				return nil, err
			}
			if c > x.plan.lead {
				break
			}
			// Few rows, put in order by a sort of them.
			s.stream = `SELECT s.page AS id FROM props s INDEXED BY props_val WHERE s.key = ? AND s.val IN (` + marks(len(others)) + `) AND s.pos = 0`
			s.args = append([]any{o.key}, anys(others)...)
			d, op := "", ">"
			if o.desc {
				d, op = " DESC", "<"
			}
			if s.in {
				s.stream += ` AND (s.skey, s.page) ` + op + ` (?, ?)`
				s.args = append(s.args, s.t, s.after)
			}
			s.stream += ` ORDER BY s.skey` + d + `, s.page` + d
			s.extra = nil
		}
		out = append(out, s)
	}
	return out, nil
}

// walk reads a page of the listing by walking its order from the cursor and
// looking each page up against the conditions: a short stretch first, which
// holds the page where most pages match, then longer ones. It looks at no more
// than plan.budget pages, unless the last stretch found pages at a rate that
// says where the page will be full, and took a time that says getting there
// costs less than walkTime (conditions that most pages fail at the first
// lookup): then it goes on to there. ok is false when the walk is given up,
// and nothing is returned then.
func (x *read) walk(q Query, limit int, o *ordering, segs []segment, conds []cond) (res Result, ok bool, err error) {
	start := time.Now()
	allowed := x.plan.budget(limit) // pages that may be looked at
	looked, stretches := 0, 0
	// What the last stretch says: the pages it looked at, those it found, and
	// what a page cost.
	var lastLooked, lastFound int
	var rate time.Duration
	var rows []row
	for i := range segs {
		seg := &segs[i]
		cs := conds
		if seg.extra != nil {
			cs = append(conds[:len(conds):len(conds)], *seg.extra)
		}
		for seen := 0; ; {
			if stretches >= 2 && lastFound > 0 {
				// Where the page should be full, with a quarter more. (The first
				// stretch says little of the cost of a page: the query has its own.)
				more := (limit + 1 - len(rows)) * lastLooked / lastFound
				need := looked + more + more/4 + 16
				if need > allowed && need <= 20*x.plan.budget(limit) && time.Since(start)+rate*time.Duration(need-looked) <= walkTime {
					allowed = need
				}
			}
			stretch := min(max(2*(limit+1-len(rows)), 8*looked), allowed-looked)
			if stretch <= 0 {
				return Result{}, false, nil
			}
			began := time.Now()
			ids, err := x.firstIDs(seg.stream, seg.args, seen+stretch)
			if err != nil {
				return Result{}, false, err
			}
			fresh := ids[min(seen, len(ids)):]
			got, err := x.pagesOf(fresh, cs, q.Props, o.key, limit+1-len(rows))
			if err != nil {
				return Result{}, false, err
			}
			for j := range got {
				got[j].seg = seg
			}
			if rows = append(rows, got...); len(rows) > limit {
				return o.result(rows[:limit], rows[limit].seg), true, nil
			}
			looked, stretches = looked+len(fresh), stretches+1
			if lastLooked, lastFound = len(fresh), len(got); len(fresh) > 0 {
				rate = time.Since(began) / time.Duration(len(fresh))
			}
			if len(ids) < seen+stretch { // the end of the segment
				break
			}
			seen = len(ids)
		}
	}
	return o.result(rows, nil), true, nil
}

// walkSet reads a page of the listing whose matching pages are known (m, many
// of them): each segment's order is read from the cursor on, one scan of its
// index range, and the pages of m are picked out of it.
func (x *read) walkSet(q Query, limit int, o *ordering, segs []segment, m *idSet) (Result, error) {
	var picked []int64
	var from []*segment
	var has, optFirst *idSet // read when a segment needs them
	set := func(dst **idSet, s pageSet) (err error) {
		if *dst == nil {
			*dst, err = x.setOfQuery(s.sql, s.args)
		}
		return err
	}
	for i := range segs {
		seg := &segs[i]
		keep := func(id int64) bool { return m.has(id) }
		switch {
		case seg.kind == segNone:
			// The pages of m without the property, by rowid: no scan of the order.
			if err := set(&has, hasSet(o.key)); err != nil {
				return Result{}, err
			}
			m.each(seg.after, func(id int64) bool {
				if !has.has(id) {
					picked, from = append(picked, id), append(from, seg)
				}
				return len(picked) <= limit
			})
			continue
		case seg.kind == segOther && seg.extra != nil:
			first := valueSet(o.key, o.opts)
			first.sql += ` AND pos = 0`
			if err := set(&optFirst, first); err != nil {
				return Result{}, err
			}
			keep = func(id int64) bool { return m.has(id) && !optFirst.has(id) }
		}
		ids, err := x.idsOf(seg.stream, seg.args)
		if err != nil {
			return Result{}, err
		}
		for _, id := range ids {
			if keep(id) {
				if picked, from = append(picked, id), append(from, seg); len(picked) > limit {
					break
				}
			}
		}
		if len(picked) > limit {
			break
		}
	}
	var next *segment
	if len(picked) > limit {
		next, picked = from[limit], picked[:limit]
	}
	rows, err := x.pagesOf(picked, nil, q.Props, o.key, len(picked))
	if err != nil {
		return Result{}, err
	}
	// A page that went away in between is not among the rows.
	j := 0
	for i := range rows {
		for picked[j] != rows[i].rowid {
			j++
		}
		rows[i].seg = from[j]
	}
	return o.result(rows, next), nil
}

// listFew reads a page of the listing whose matching pages are known and few
// (m): what the order depends on is looked up for each, they are put in order,
// and the summaries of the one page are read.
func (x *read) listFew(q Query, limit int, o *ordering, cur *cursor, m *idSet) (Result, error) {
	ids := m.ids()
	if o.keys != nil {
		query := `SELECT ` + selectCols(q.Props) + ` FROM json_each(?) j CROSS JOIN pages p ON p.rowid = j.value`
		args := []any{jsonIDs(ids)}
		if cur != nil {
			pred, pargs := keysetPredicate(o.keys, cur)
			query += ` WHERE ` + pred
			args = append(args, pargs...)
		}
		query += ` ORDER BY ` + orderBy(o.keys) + ` LIMIT ?`
		rows, err := x.readRows(query, append(args, limit+1), q.Props, false)
		if err != nil {
			return Result{}, err
		}
		one := &segment{}
		for i := range rows {
			rows[i].seg = one
		}
		if len(rows) > limit {
			return o.result(rows[:limit], one), nil
		}
		return o.result(rows, nil), nil
	}

	// The first value of the property of each page.
	const query = `SELECT j.value, s.val, s.skey FROM json_each(?) j LEFT JOIN props s ON s.page = j.value AND s.key = ? AND s.pos = 0`
	args := []any{jsonIDs(ids), o.key}
	x.ran(query, args)
	rs, err := x.db.Query(query, args...)
	if err != nil {
		return Result{}, err
	}
	defer rs.Close()
	optAt := make(map[string]int, len(o.opts))
	for i := len(o.opts) - 1; i >= 0; i-- {
		optAt[o.opts[i]] = i
	}
	type placed struct {
		rowid int64
		g     int
		kind  segKind
		skey  string
	}
	// before says whether a comes before b in the listing.
	before := func(a, b placed) bool {
		switch {
		case a.g != b.g:
			return a.g < b.g
		case a.kind == segNone:
			return a.rowid < b.rowid
		case a.kind == segOther && a.skey != b.skey:
			return (a.skey < b.skey) != o.desc
		case o.desc:
			return a.rowid > b.rowid
		}
		return a.rowid < b.rowid
	}
	var at *placed
	if cur != nil && cur.R != 0 {
		kind, _ := o.kindOf(cur.G)
		at = &placed{rowid: cur.R, g: cur.G, kind: kind, skey: cur.T}
	}
	var ps []placed
	for rs.Next() {
		var p placed
		var val, skey sql.NullString
		if err := rs.Scan(&p.rowid, &val, &skey); err != nil {
			return Result{}, err
		}
		p.g, p.skey = o.groupOf(optAt, val.Valid, val.String), skey.String
		p.kind, _ = o.kindOf(p.g)
		switch {
		case cur == nil:
		case at == nil && p.g < cur.G, at != nil && !before(*at, p):
			continue // before the cursor, or the page it names
		}
		ps = append(ps, p)
	}
	if err := rs.Err(); err != nil {
		return Result{}, err
	}
	rs.Close()
	sort.Slice(ps, func(i, j int) bool { return before(ps[i], ps[j]) })
	segs := map[int]*segment{}
	seg := func(p placed) *segment {
		if segs[p.g] == nil {
			segs[p.g] = &segment{kind: p.kind, g: p.g}
		}
		return segs[p.g]
	}
	var next *segment
	if len(ps) > limit {
		next, ps = seg(ps[limit]), ps[:limit]
	}
	ids = ids[:0]
	for _, p := range ps {
		ids = append(ids, p.rowid)
	}
	rows, err := x.pagesOf(ids, nil, q.Props, o.key, len(ids))
	if err != nil {
		return Result{}, err
	}
	j := 0
	for i := range rows { // a page that went away in between is not among the rows
		for ps[j].rowid != rows[i].rowid {
			j++
		}
		rows[i].seg, rows[i].skey = seg(ps[j]), ps[j].skey
	}
	return o.result(rows, next), nil
}

// listPlanned reads a page of a listing that has conditions or an order by a
// property, in whichever of the three ways costs least (see the top of
// plan.go). sets says the order must not be walked: a condition is too dear to
// look up page by page (the words of a search).
func (x *read) listPlanned(q Query, limit int, o *ordering, cur *cursor, conds []cond, sets bool) (Result, error) {
	segs, err := o.segments(cur)
	if err != nil {
		return Result{}, err
	}
	m, ok, err := x.match(conds, !sets)
	if err != nil {
		return Result{}, err
	}
	pruned := false
	if !ok {
		if segs, err = x.prune(o, segs); err != nil {
			return Result{}, err
		}
		pruned = true
		res, ok, err := x.walk(q, limit, o, segs, conds)
		if err != nil || ok {
			return res, err
		}
		if m, err = x.matchAll(conds); err != nil {
			return Result{}, err
		}
	}
	switch n := m.count(); {
	case n == 0:
		return Result{Items: []Summary{}}, nil
	case n <= x.plan.set:
		return x.listFew(q, limit, o, cur, m)
	}
	if !pruned {
		if segs, err = x.prune(o, segs); err != nil {
			return Result{}, err
		}
	}
	return x.walkSet(q, limit, o, segs, m)
}
