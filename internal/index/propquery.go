package index

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Filter keeps the pages whose property Key matches. Op is one of:
//
//	in        the property equals one of Values (a list property: holds one of them)
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
	maxFilters      = 10
	maxFilterValues = 100
	maxFilterKey    = 256
)

// checkFilters validates the filters and prepares them for use.
func checkFilters(fs []Filter) error {
	if len(fs) > maxFilters {
		return invalid("at most %d filters", maxFilters)
	}
	for i := range fs {
		f := &fs[i]
		if f.Key == "" || len(f.Key) > maxFilterKey || !utf8.ValidString(f.Key) {
			return invalid("a filter needs the key of a property")
		}
		f.vals, f.lo, f.hi = nil, nil, nil
		switch f.Op {
		case "in":
			if len(f.Values) == 0 || len(f.Values) > maxFilterValues {
				return invalid("an in filter needs 1 to %d values", maxFilterValues)
			}
			for _, v := range f.Values {
				t, ok := valText(v)
				if !ok {
					return invalid("the values of an in filter must be text, numbers or true/false")
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
			return invalid("unknown filter op %q (use in, range, empty, notEmpty or contains)", f.Op)
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
