package index

import (
	"database/sql"
	"fmt"
	"math/bits"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// How a listing with conditions, and a count by values, are read.
//
// A listing is a scope (a folder, tags, dates, words) and filters: conditions
// that must all hold, in an order, one page of it at a time. The page table and
// the props table have an index for every order and for every condition, but
// no one way of joining them is cheap for every question: walking the order
// and looking each page up is right when most pages match and reads the whole
// folder when few do; starting from a filter's rows is right when the filter
// keeps few pages and wrong when it keeps half of them. So the cost of each
// way is bounded, and the next is taken when the bound is reached:
//
//  1. The size of every condition an index can count is counted, up to
//     smallLead. When one keeps at most that many pages, the read starts from
//     its pages and looks the other conditions up for each (matchFrom): the
//     cost follows the smallest condition.
//  2. Otherwise the order is walked from the cursor, each page looked up, in
//     stretches that grow, for at most walkBudget pages (walk): where a
//     fiftieth of the pages or more match, as in a board's column for an
//     option in use, that fills the page. The walk goes past the bound only
//     while its last stretch says the page will be full within walkTime.
//  3. When it is given up, the conditions are read as sets, each one scan of
//     the index range that holds it, a few at a time, and intersected here
//     (matchAll): no lookup by page, so the cost is the size of the ranges,
//     some 6 ms for each 50 000 rows. With more ranges than are read at a
//     time, those that keep the fewest pages (by a sample) are read first, and
//     the read stops when no page is left or few are. Few matches are then
//     put in order here (listFew); many are picked out of one scan of the
//     order (walkSet).
//
// A count by values (Groups) has no order and no limit: it is step 1, or
// step 3, and then one scan of the rows of the property.
//
// The sets are read as one row each, the ids joined by group_concat: the
// driver's cost for each row it hands over is what a listing cannot pay 50 000
// times, several times over.
//
// What stays dear is a request whose every condition keeps most pages while
// together they keep few, or a count under such conditions: each of them is a
// range of the size of the space, and all must be read (twenty of them, the
// most a request may hold, are some 100 ms for 50 000 pages).
var (
	// smallLead is the most pages of a condition for the read to start from it.
	smallLead = 2000
	// smallSet is the most matching pages that are put in order by looking each
	// up; more are picked out of a scan of the order.
	smallSet = 6000
	// walkBudget is the most pages a walk of the order looks at before the
	// conditions are read as sets instead.
	walkBudget = func(limit int) int { return max(1000, 5*(limit+1)) }
	// scanWorkers is the most sets read, and the most parts of a long list of
	// pages looked up, at the same time, each on a connection of its own.
	scanWorkers = 4
	// walkTime is the most a walk that goes past walkBudget may be expected
	// to take in all.
	walkTime = 50 * time.Millisecond
)

const (
	// maxDistinct is the most distinct values of a property, beyond those a
	// notIn filter or a list of options names, that are listed to see which
	// of them the filter keeps, or which options have pages.
	maxDistinct = 100
	// lookupPart is the number of pages looked up in one query, when many are
	// looked up in several at the same time.
	lookupPart = 500
	// maxRowid bounds the ids a set holds; the page table numbers its rows from 1.
	maxRowid = 1 << 28
)

// pageSet is a query whose rows are the ids of pages, in a column named id,
// read from one range of an index.
type pageSet struct {
	sql  string
	args []any
	// neg says the condition is met by the pages that are not among these.
	neg bool
	// scan says that counting the rows reads the whole range (contains): it is
	// not counted to choose where to start.
	scan bool
}

// cond is a condition on the pages of a listing.
type cond struct {
	// where is the condition on the page row p: what a lookup by page checks.
	where string
	args  []any
	// sets, taken together (the neg ones taken away), are exactly the pages
	// that meet the condition. nil when no index says it exactly.
	sets []pageSet
	// lead, when set, is pages among which every match is, and maybe more: a
	// place to start that is tighter or cheaper than the sets.
	lead *pageSet
	// loose, in a read of sets, says sets is only a place to start: the pages
	// that meet the condition are among them.
	loose bool
	// notIn is the filter of a notIn condition, for which lead is found by
	// asking the index which values the property has (see setLeads).
	notIn *Filter
}

// start is the set a read of the condition's pages may begin with; exact says
// it holds the matching pages and no others.
func (c cond) start() (s *pageSet, exact bool) {
	if c.lead != nil {
		return c.lead, false
	}
	for i := range c.sets {
		if !c.sets[i].neg {
			return &c.sets[i], len(c.sets) == 1
		}
	}
	return nil, false
}

func marks(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

func anys(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// hasSet is the pages that have a value for the property.
func hasSet(key string) pageSet {
	return pageSet{sql: `SELECT page AS id FROM props INDEXED BY props_sort WHERE key = ? AND pos = 0`, args: []any{key}}
}

// valueSet is the pages that hold one of the values (a list: in any place).
func valueSet(key string, vals []string) pageSet {
	if len(vals) == 0 {
		return pageSet{sql: `SELECT page AS id FROM props WHERE 0`}
	}
	return pageSet{sql: `SELECT page AS id FROM props INDEXED BY props_val WHERE key = ? AND val IN (` + marks(len(vals)) + `)`,
		args: append([]any{key}, anys(vals)...)}
}

// allPages is every page.
var allPages = pageSet{sql: `SELECT rowid AS id FROM pages`}

// scopeConditions are the conditions that say which pages a listing is about:
// those in a folder (and below it), those with every tag.
func scopeConditions(folder *string, recursive bool, tags []string) []cond {
	var out []cond
	if folder != nil {
		f := *folder
		switch {
		case !recursive:
			out = append(out, cond{where: `p.folder = ?`, args: []any{f},
				sets: []pageSet{{sql: `SELECT rowid AS id FROM pages WHERE folder = ?`, args: []any{f}}}})
		case f != "":
			// Subfolders are the range "f/" .. "f0" ('0' follows '/'), which
			// the (folder, mtime) index serves. As a set it is read as the one
			// range from "f" to "f0", leaving out what lies between the folder
			// and its subfolders ("f-x", a sibling).
			out = append(out, cond{where: `(p.folder = ? OR (p.folder >= ? AND p.folder < ?))`, args: []any{f, f + "/", f + "0"},
				sets: []pageSet{{sql: `SELECT rowid AS id FROM pages WHERE folder >= ? AND folder < ? AND (folder = ? OR folder >= ?)`,
					args: []any{f, f + "0", f, f + "/"}}}})
		}
	}
	for _, t := range tags {
		out = append(out, cond{where: `EXISTS (SELECT 1 FROM tags t WHERE t.page = p.rowid AND t.tag = ?)`, args: []any{t},
			sets: []pageSet{{sql: `SELECT page AS id FROM tags WHERE tag = ?`, args: []any{t}}}})
	}
	return out
}

// cond is the filter as a condition: what sql says of a page, and the index
// ranges that hold the pages it keeps.
func (f *Filter) cond() cond {
	where, args := f.sql()
	c := cond{where: where, args: args}
	switch f.Op {
	case "in":
		c.sets = []pageSet{valueSet(f.Key, f.vals)}
	case "notIn":
		in := valueSet(f.Key, f.vals)
		in.neg = true
		c.sets = []pageSet{hasSet(f.Key), in}
		c.notIn = f
	case "range":
		s := pageSet{sql: `SELECT page AS id FROM props INDEXED BY props_skey WHERE key = ?`, args: []any{f.Key}}
		if f.lo != nil {
			s.sql += ` AND skey >= ?`
			s.args = append(s.args, *f.lo)
		}
		if f.hi != nil {
			s.sql += ` AND skey <= ?`
			s.args = append(s.args, *f.hi)
		}
		c.sets = []pageSet{s}
	case "empty":
		has := hasSet(f.Key)
		has.neg = true
		c.sets = []pageSet{has}
	case "notEmpty":
		c.sets = []pageSet{hasSet(f.Key)}
	default: // contains
		c.sets = []pageSet{{sql: `SELECT page AS id FROM props INDEXED BY props_val WHERE key = ? AND val LIKE ? ESCAPE '\'`,
			args: []any{f.Key, likePattern(f.vals[0])}, scan: true}}
	}
	return c
}

// conditions are the scope and the filters of a listing or a count.
func conditions(folder *string, recursive bool, tags []string, fs []Filter) []cond {
	out := scopeConditions(folder, recursive, tags)
	for i := range fs {
		out = append(out, fs[i].cond())
	}
	return out
}

func whereOf(conds []cond) (string, []any) {
	var parts []string
	var args []any
	for _, c := range conds {
		if c.where != "" { // a place to start only: what it stands for is among the others
			parts = append(parts, c.where)
			args = append(args, c.args...)
		}
	}
	return strings.Join(parts, ` AND `), args
}

// ran counts a query of a listing or a count (tests read the number, and the
// queries themselves through trace).
func (x *Index) ran(query string, args []any) {
	x.queries.Add(1)
	if x.trace != nil {
		x.trace(query, args)
	}
}

// countUpTo counts the rows of s, stopping after max+1.
func (x *Index) countUpTo(s *pageSet, max int) (n int, err error) {
	query := `SELECT count(*) FROM (` + s.sql + ` LIMIT ?)`
	args := append(append([]any{}, s.args...), max+1)
	x.ran(query, args)
	err = x.db.QueryRow(query, args...).Scan(&n)
	return
}

// idsText is every id of the query, in the order of its rows, as one text
// with commas between: the query is read as one row.
// (group_concat keeps the order of the rows of a subquery that has one; SQLite
// leaves such a subquery's ORDER BY alone for exactly this function.)
func (x *Index) idsText(query string, args []any, use func(text []byte) error) error {
	query = `SELECT group_concat(id) FROM (` + query + `)`
	x.ran(query, args)
	rows, err := x.db.Query(query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var text sql.RawBytes // no copy of a text that can be long
		if err := rows.Scan(&text); err != nil {
			return err
		}
		if err := use(text); err != nil {
			return err
		}
	}
	return rows.Err()
}

// idsOf is every id of the query, in the order of its rows.
func (x *Index) idsOf(query string, args []any) (ids []int64, err error) {
	err = x.idsText(query, args, func(text []byte) error {
		ids = make([]int64, 0, len(text)/6+1)
		return parseIDs(text, func(id int64) { ids = append(ids, id) })
	})
	return
}

// setOfQuery is the ids of the query as a set.
func (x *Index) setOfQuery(query string, args []any) (*idSet, error) {
	s := &idSet{}
	err := x.idsText(query, args, func(text []byte) error { return parseIDs(text, s.add) })
	return s, err
}

// firstIDs is the first n ids of the query, in the order of its rows.
func (x *Index) firstIDs(query string, args []any, n int) ([]int64, error) {
	query += ` LIMIT ?`
	args = append(append([]any{}, args...), n)
	x.ran(query, args)
	rows, err := x.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// parseIDs calls add with each id of a text of ids with commas between.
func parseIDs(text []byte, add func(int64)) error {
	if len(text) == 0 {
		return nil
	}
	var n int64
	digits := 0
	for i := 0; i <= len(text); i++ {
		if i < len(text) && text[i] >= '0' && text[i] <= '9' {
			if n = n*10 + int64(text[i]-'0'); n > maxRowid {
				return fmt.Errorf("index: page number %d… is out of range", n)
			}
			digits++
			continue
		}
		if digits == 0 || (i < len(text) && text[i] != ',') {
			return fmt.Errorf("index: bad list of pages")
		}
		add(n)
		n, digits = 0, 0
	}
	return nil
}

// jsonIDs is the ids as the JSON array json_each reads.
func jsonIDs(ids []int64) string {
	b := make([]byte, 0, len(ids)*7+2)
	b = append(b, '[')
	for i, id := range ids {
		if i > 0 {
			b = append(b, ',')
		}
		b = strconv.AppendInt(b, id, 10)
	}
	return string(append(b, ']'))
}

// idSet is a set of page ids, a bit for each.
type idSet struct{ w []uint64 }

func setOf(ids []int64) *idSet {
	s := &idSet{}
	for _, id := range ids {
		s.add(id)
	}
	return s
}

func (s *idSet) add(id int64) {
	i := int(id >> 6)
	if i >= len(s.w) {
		s.w = append(s.w, make([]uint64, i+1-len(s.w))...)
	}
	s.w[i] |= 1 << (uint(id) & 63)
}

func (s *idSet) has(id int64) bool {
	i := int(id >> 6)
	return id >= 0 && i < len(s.w) && s.w[i]&(1<<(uint(id)&63)) != 0
}

func (s *idSet) and(o *idSet) {
	for i := range s.w {
		if i < len(o.w) {
			s.w[i] &= o.w[i]
		} else {
			s.w[i] = 0
		}
	}
}

func (s *idSet) andNot(o *idSet) {
	for i := 0; i < len(s.w) && i < len(o.w); i++ {
		s.w[i] &^= o.w[i]
	}
}

func (s *idSet) count() (n int) {
	for _, w := range s.w {
		n += bits.OnesCount64(w)
	}
	return
}

// each calls f with the ids from the one after `after`, ascending, until f
// returns false.
func (s *idSet) each(after int64, f func(id int64) bool) {
	start := after + 1
	if start < 0 {
		start = 0
	}
	for i := int(start >> 6); i < len(s.w); i++ {
		w := s.w[i]
		if int64(i) == start>>6 {
			w &^= 1<<(uint(start)&63) - 1
		}
		for w != 0 {
			if !f(int64(i)<<6 + int64(bits.TrailingZeros64(w))) {
				return
			}
			w &= w - 1
		}
	}
}

func (s *idSet) ids() []int64 {
	out := make([]int64, 0, s.count())
	s.each(-1, func(id int64) bool {
		out = append(out, id)
		return true
	})
	return out
}

// valuesOf lists the distinct values of a property, at most max of them, each
// a seek in the index of values; whole is false when there are more.
func (x *Index) valuesOf(key string, max int) (vals []string, whole bool, err error) {
	const query = `WITH RECURSIVE d(v) AS (
		SELECT min(val) FROM props WHERE key = ?1
		UNION ALL
		SELECT (SELECT min(val) FROM props WHERE key = ?1 AND val > d.v) FROM d WHERE d.v IS NOT NULL)
		SELECT v FROM d WHERE v IS NOT NULL LIMIT ?2`
	args := []any{key, max + 1}
	x.ran(query, args)
	rows, err := x.db.Query(query, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, false, err
		}
		vals = append(vals, v)
	}
	if len(vals) > max {
		return nil, false, rows.Err()
	}
	return vals, true, rows.Err()
}

// setLeads finds where the read of a notIn condition may start. The pages it
// keeps hold a value that is not on its list; when the property has few
// distinct values (a select field), those other values are known from a few
// seeks, and their rows are the place: the "other values" column of a board
// costs the pages in it, and no page at all when every value is an option.
func (x *Index) setLeads(conds []cond) error {
	for i := range conds {
		f := conds[i].notIn
		if f == nil {
			continue
		}
		vals, whole, err := x.valuesOf(f.Key, len(f.vals)+maxDistinct)
		if err != nil {
			return err
		}
		if !whole {
			continue
		}
		listed := make(map[string]bool, len(f.vals))
		for _, v := range f.vals {
			listed[v] = true
		}
		var others []string
		for _, v := range vals {
			if !listed[v] {
				others = append(others, v)
			}
		}
		s := valueSet(f.Key, others)
		conds[i].lead = &s
	}
	return nil
}

// match is the pages that meet every condition.
//
// With onlySmall (a listing, before its order is walked) it answers only when
// a condition keeps few enough pages to start from, or none: ok is false
// otherwise. Without it, it always answers, reading the conditions as sets
// when none is small.
func (x *Index) match(conds []cond, onlySmall bool) (m *idSet, ok bool, err error) {
	if err := x.setLeads(conds); err != nil {
		return nil, false, err
	}
	best, bestN := -1, 0
	for i, c := range conds {
		s, _ := c.start()
		if s == nil || s.scan {
			continue
		}
		n, err := x.countUpTo(s, smallLead)
		if err != nil {
			return nil, false, err
		}
		if n == 0 {
			return &idSet{}, true, nil // a condition no page meets
		}
		if n <= smallLead && (best < 0 || n < bestN) {
			best, bestN = i, n
		}
	}
	if best >= 0 {
		m, err = x.matchFrom(conds, best)
		return m, err == nil, err
	}
	if onlySmall {
		return nil, false, nil
	}
	m, err = x.matchAll(conds)
	return m, err == nil, err
}

// matchFrom reads the pages of the condition at lead, which are few, and keeps
// those that meet the others, each a lookup by page.
func (x *Index) matchFrom(conds []cond, lead int) (*idSet, error) {
	s, exact := conds[lead].start()
	ids, err := x.idsOf(s.sql, s.args)
	if err != nil {
		return nil, err
	}
	rest := conds
	if exact {
		rest = append(append([]cond{}, conds[:lead]...), conds[lead+1:]...)
	}
	if ids, err = x.among(setOf(ids).ids(), rest); err != nil { // a list can be among the rows twice
		return nil, err
	}
	return setOf(ids), nil
}

// among is those of the pages that meet the conditions, each looked up. Many
// pages are looked up in a few parts at the same time.
func (x *Index) among(ids []int64, conds []cond) ([]int64, error) {
	where, wargs := whereOf(conds)
	if where == "" || len(ids) == 0 {
		return ids, nil
	}
	query := `SELECT p.rowid AS id FROM json_each(?) j CROSS JOIN pages p ON p.rowid = j.value WHERE ` + where
	look := func(part []int64) ([]int64, error) {
		return x.idsOf(query, append([]any{jsonIDs(part)}, wargs...))
	}
	parts := min(scanWorkers, len(ids)/lookupPart+1)
	if parts == 1 {
		return look(ids)
	}
	got := make([][]int64, parts)
	errs := make([]error, parts)
	var wg sync.WaitGroup
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], errs[i] = look(ids[i*len(ids)/parts : (i+1)*len(ids)/parts])
		}()
	}
	wg.Wait()
	var out []int64
	for i := range got {
		if errs[i] != nil {
			return nil, errs[i]
		}
		out = append(out, got[i]...)
	}
	return out, nil
}

// sampleSize is the number of pages a sample of the space has.
const sampleSize = 64

// fewestFirst puts the conditions in the order of how few pages each keeps,
// as a sample of the pages of the space says: every condition is looked up for
// each page of the sample. The order changes what is read first and nothing
// else.
func (x *Index) fewestFirst(conds []cond) error {
	var last int64
	const top = `SELECT ifnull(max(rowid), 0) FROM pages`
	x.ran(top, nil)
	if err := x.db.QueryRow(top).Scan(&last); err != nil {
		return err
	}
	ids := make([]int64, 0, sampleSize)
	for i := int64(1); i <= sampleSize; i++ {
		ids = append(ids, i*last/sampleSize) // spread over the rows; one that is gone is not counted
	}
	query := `SELECT `
	args := []any{}
	for i, c := range conds {
		if i > 0 {
			query += `, `
		}
		query += `ifnull(sum(` + c.where + `), 0)`
		args = append(args, c.args...)
	}
	query += ` FROM json_each(?) j CROSS JOIN pages p ON p.rowid = j.value`
	args = append(args, jsonIDs(ids))
	x.ran(query, args)
	kept := make([]int, len(conds))
	dest := make([]any, len(conds))
	for i := range kept {
		dest[i] = &kept[i]
	}
	if err := x.db.QueryRow(query, args...).Scan(dest...); err != nil {
		return err
	}
	at := make([]int, len(conds))
	for i := range at {
		at[i] = i
	}
	sort.SliceStable(at, func(a, b int) bool { return kept[at[a]] < kept[at[b]] })
	sorted := make([]cond, len(conds))
	for i, j := range at {
		sorted[i] = conds[j]
	}
	copy(conds, sorted)
	return nil
}

// matchAll reads every condition as the sets of pages its index ranges hold
// and intersects them: one scan of each range, a few at a time, and no lookup
// by page, whatever the conditions keep. It stops when no page is left, and
// when few are left (smallLead) it looks the remaining conditions up for those,
// which then costs less than the scans of their ranges.
func (x *Index) matchAll(conds []cond) (*idSet, error) {
	// The conditions an index says exactly, each with what it takes to read
	// it, and those none does: looked up at the end for what is left.
	var order, rest []cond
	sets := 0
	for _, c := range conds {
		if c.sets == nil {
			rest = append(rest, c)
			if c.lead == nil {
				continue
			}
			// A place to start: the pages are among these, and which of them
			// they are is looked up at the end (the condition is in rest).
			c.sets, c.loose = []pageSet{*c.lead}, true
		}
		order = append(order, c)
		sets += len(c.sets)
	}
	// More ranges than are read at a time: the conditions that keep the fewest
	// pages go first, so that the read can stop early, when no page or few are
	// left. Which those are is asked of a sample of the pages.
	if sets > scanWorkers {
		if err := x.fewestFirst(order); err != nil {
			return nil, err
		}
	}
	// The first must give pages, not only take them away (empty).
	first := slices.IndexFunc(order, func(c cond) bool { s, _ := c.start(); return s != nil })
	if first < 0 {
		order = append([]cond{{sets: []pageSet{allPages}, loose: true}}, order...)
	} else if first > 0 {
		c := order[first]
		copy(order[1:first+1], order[:first])
		order[0] = c
	}

	// Every range is read once (two conditions may share one), scanWorkers of
	// them at a time, in the order of the conditions; the sets are intersected
	// in that order as they arrive, and the ranges not yet begun are left
	// unread once the answer is known.
	type scan struct {
		set  pageSet
		ids  *idSet
		err  error
		done chan struct{}
	}
	scans := map[string]*scan{}
	var queue []*scan
	key := func(s pageSet) string { return s.sql + "\x00" + fmt.Sprint(s.args...) }
	for _, c := range order {
		for _, s := range c.sets {
			if scans[key(s)] == nil {
				scans[key(s)] = &scan{set: s, done: make(chan struct{})}
				queue = append(queue, scans[key(s)])
			}
		}
	}
	var stop atomic.Bool
	if len(queue) == 1 {
		queue[0].ids, queue[0].err = x.setOfQuery(queue[0].set.sql, queue[0].set.args)
		close(queue[0].done)
	} else {
		sem := make(chan struct{}, scanWorkers)
		go func() {
			for _, sc := range queue {
				sem <- struct{}{}
				go func() {
					defer func() { <-sem; close(sc.done) }()
					if !stop.Load() {
						sc.ids, sc.err = x.setOfQuery(sc.set.sql, sc.set.args)
					}
				}()
			}
		}()
	}
	var m *idSet
	var failed error
	for i, c := range order {
		for _, s := range c.sets {
			sc := scans[key(s)]
			<-sc.done
			switch {
			case sc.err != nil:
				failed = sc.err
			case failed != nil:
			case s.neg:
				m.andNot(sc.ids)
			case m == nil:
				m = &idSet{w: append([]uint64(nil), sc.ids.w...)}
			default:
				m.and(sc.ids)
			}
		}
		if failed != nil {
			break
		}
		if left := m.count(); left == 0 || (left <= smallLead && i+1 < len(order)) {
			for _, c := range order[i+1:] {
				if left > 0 && !c.loose {
					rest = append(rest, c)
				}
			}
			if left == 0 {
				rest = nil
			}
			break
		}
	}
	stop.Store(true)
	for _, sc := range queue {
		<-sc.done // none is left running
	}
	if failed != nil {
		return nil, failed
	}
	if len(rest) > 0 {
		ids, err := x.among(m.ids(), rest)
		if err != nil {
			return nil, err
		}
		m = setOf(ids)
	}
	return m, nil
}
