package index

import (
	"database/sql"
	"encoding/json"
	"slices"
	"sort"
	"sync"
	"unicode/utf8"
)

// MaxGroups is the most distinct values Groups returns.
const MaxGroups = 1000

// GroupQuery asks how the pages of a scope are spread over the values of one
// property. The scope is that of a listing: a folder (and below it), tags,
// filters.
type GroupQuery struct {
	Folder    *string
	Recursive bool
	Tags      []string
	Filters   []Filter
	// Key is the property to count by.
	Key string
}

// Group is a value of the property and the number of pages that have it (a
// list property counts a page once for each distinct value it holds).
type Group struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// GroupResult is the answer of Groups.
type GroupResult struct {
	// Groups are the values, most pages first, then by value.
	Groups []Group `json:"groups"`
	// Empty is the number of pages in the scope with no value for the property.
	Empty int `json:"empty"`
	// Truncated says there were more than MaxGroups values and the rarest are
	// left out.
	Truncated bool `json:"truncated"`
}

// Groups counts the pages of a scope by the values of a property. It reads the
// props table only (and the pages and tags tables for the scope), never a
// file. Cost: one scan of the index range of each condition of a large scope
// and one of the rows of the property; for a small scope, its pages.
func (x *Index) Groups(q GroupQuery) (GroupResult, error) {
	ck, cacheable := q.cacheKey()
	if gen := x.gen.Load(); cacheable && gen%2 == 0 {
		if res, ok := x.groups.get(ck, gen); ok {
			return res, nil
		}
	}
	r, err := x.begin()
	if err != nil {
		return GroupResult{}, err
	}
	defer r.end()
	res, err := r.countGroups(q)
	// Only a count whose snapshot is known to be of one generation is
	// remembered, under that generation.
	if err == nil && cacheable && r.known {
		x.groups.put(ck, r.at, res)
	}
	return res, err
}

// maxGroupCache is the most answers of Groups remembered.
const maxGroupCache = 64

// groupCache remembers the answers of Groups for as long as the index has not
// been written: a board that is opened again, or read again after something
// that did not touch the index, costs nothing, and the count over a large
// folder is made once per change rather than once per request.
type groupCache struct {
	mu  sync.Mutex
	gen uint64
	m   map[string]GroupResult
}

func (c *groupCache) get(key string, gen uint64) (GroupResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen != gen {
		return GroupResult{}, false
	}
	res, ok := c.m[key]
	if ok {
		res.Groups = append([]Group{}, res.Groups...)
	}
	return res, ok
}

func (c *groupCache) put(key string, gen uint64, res GroupResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen < c.gen {
		return // a count of an older state: those of the newer are kept
	}
	if c.gen != gen || c.m == nil || len(c.m) >= maxGroupCache {
		c.gen, c.m = gen, map[string]GroupResult{}
	}
	res.Groups = append([]Group{}, res.Groups...)
	c.m[key] = res
}

// cacheKey is the question as a key, false when it cannot be made one.
func (q GroupQuery) cacheKey() (string, bool) {
	type f struct {
		Key, Op string
		Values  []any
	}
	fs := make([]f, len(q.Filters))
	for i, x := range q.Filters {
		fs[i] = f{x.Key, x.Op, x.Values}
	}
	b, err := json.Marshal(struct {
		Folder    *string
		Recursive bool
		Tags      []string
		Filters   []f
		Key       string
	}{q.Folder, q.Recursive, q.Tags, fs, q.Key})
	return string(b), err == nil
}

// countGroups is Groups without the cache: the pages of the scope that meet
// the filters are found the way a listing finds them (see plan.go), starting
// from a condition that keeps few pages when there is one, and otherwise as the
// intersection of one scan of each condition's index range; then the rows of
// the property are counted, by lookup for few pages and in one scan of the
// property's values for many. No lookup by page is made for a large scope,
// so the cost is the size of the index ranges read.
func (x *Index) countGroups(q GroupQuery) (GroupResult, error) {
	r, err := x.begin()
	if err != nil {
		return GroupResult{}, err
	}
	defer r.end()
	return r.countGroups(q)
}

// countGroups counts in the snapshot of the read.
func (x *read) countGroups(q GroupQuery) (GroupResult, error) {
	if q.Key == "" || len(q.Key) > maxFilterKey || !utf8.ValidString(q.Key) {
		return GroupResult{}, invalid("Groups needs the key of a property.")
	}
	if err := checkFilters(q.Filters); err != nil {
		return GroupResult{}, err
	}
	conds := conditions(q.Folder, q.Recursive, q.Tags, q.Filters)
	m, _, err := x.match(conds, false)
	if err != nil {
		return GroupResult{}, err
	}
	return x.countBy(q.Key, m)
}

// countBy counts the pages of m by their values of the property.
func (x *read) countBy(key string, m *idSet) (GroupResult, error) {
	res := GroupResult{Groups: []Group{}}
	total := m.count()
	if total == 0 {
		return res, nil
	}
	var all []Group
	var err error
	with := 0
	if total <= x.plan.set {
		const query = `SELECT s.val, count(DISTINCT s.page), sum(s.pos = 0) FROM json_each(?) j CROSS JOIN props s ON s.page = j.value AND s.key = ? GROUP BY s.val`
		args := []any{jsonIDs(m.ids()), key}
		x.ran(query, args)
		rows, err := x.db.Query(query, args...)
		if err != nil {
			return GroupResult{}, err
		}
		defer rows.Close()
		for rows.Next() {
			var g Group
			var first int
			if err := rows.Scan(&g.Value, &g.Count, &first); err != nil {
				return GroupResult{}, err
			}
			with += first
			all = append(all, g)
		}
		if err := rows.Err(); err != nil {
			return GroupResult{}, err
		}
	} else if with, all, err = x.countMany(key, m); err != nil {
		return GroupResult{}, err
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Count != all[j].Count {
			return all[i].Count > all[j].Count
		}
		return all[i].Value < all[j].Value
	})
	if len(all) > MaxGroups {
		all, res.Truncated = all[:MaxGroups], true
	}
	if len(all) > 0 {
		res.Groups = all
	}
	res.Empty = total - with
	return res, nil
}

// countMany counts the pages of m, which are many, by their values of the
// property, in one scan of the property's rows in the index of values: no
// lookup by page. It returns the values with their counts, and how many pages
// of m have a value at all.
func (x *read) countMany(key string, m *idSet) (with int, all []Group, err error) {
	seen := &idSet{w: make([]uint64, len(m.w))} // the pages of m that have a value
	count := func(g *Group, page, last int64) {
		if !m.has(page) {
			return
		}
		if page != last { // a list that holds the value twice counts once
			g.Count++
		}
		seen.w[page>>6] |= 1 << (uint(page) & 63)
	}
	// Few distinct values (a select field): a row for each, with all its pages.
	// Many (a name, a number): the rows as they are, which costs the driver a
	// row each but SQLite no grouping.
	_, few, err := x.valuesOf(key, maxDistinct)
	if err != nil {
		return 0, nil, err
	}
	query := `SELECT val, page FROM props INDEXED BY props_val WHERE key = ?`
	if few {
		query = `SELECT val, group_concat(page) FROM props INDEXED BY props_val WHERE key = ? GROUP BY val`
	}
	args := []any{key}
	x.ran(query, args)
	rows, err := x.db.Query(query, args...)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	var val, pages sql.RawBytes
	var page, last int64
	var ids []int64
	for rows.Next() {
		if few {
			if err := rows.Scan(&val, &pages); err != nil {
				return 0, nil, err
			}
			ids = ids[:0]
			if err := parseIDs(pages, func(id int64) { ids = append(ids, id) }); err != nil {
				return 0, nil, err
			}
			if !slices.IsSorted(ids) { // the index holds them in order; nothing promises the rows come so
				slices.Sort(ids)
			}
			g := Group{Value: string(val)}
			last = 0
			for _, id := range ids {
				count(&g, id, last)
				last = id
			}
			if g.Count > 0 {
				all = append(all, g)
			}
			continue
		}
		if err := rows.Scan(&val, &page); err != nil {
			return 0, nil, err
		}
		if !m.has(page) {
			continue
		}
		if n := len(all); n == 0 || all[n-1].Value != string(val) {
			all = append(all, Group{Value: string(val)})
			last = 0
		}
		count(&all[len(all)-1], page, last)
		last = page
	}
	return seen.count(), all, rows.Err()
}
