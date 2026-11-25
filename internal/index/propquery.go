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
		return invalid("At most %d filters.", max)
	}
	for i := range fs {
		f := &fs[i]
		if f.Key == "" || len(f.Key) > maxFilterKey || !utf8.ValidString(f.Key) {
			return invalid("A filter needs the key of a property.")
		}
		f.vals, f.lo, f.hi = nil, nil, nil
		switch f.Op {
		case "in", "notIn":
			if len(f.Values) == 0 || len(f.Values) > maxFilterValues {
				return invalid("An %s filter needs 1 to %d values.", f.Op, maxFilterValues)
			}
			for _, v := range f.Values {
				t, ok := valText(v)
				if !ok {
					return invalid("The values of an %s filter must be text, numbers or true/false.", f.Op)
				}
				f.vals = append(f.vals, t)
			}
		case "range":
			if len(f.Values) != 2 {
				return invalid("A range filter needs two values, [from, to], either may be null.")
			}
			var kinds [2]string
			for j, v := range f.Values {
				k, key, open, ok := boundKey(v)
				if !ok {
					return invalid("The bounds of a range filter must be numbers, text or null.")
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
				return invalid("A range filter needs at least one bound.")
			}
			if kinds[0] != "" && kinds[1] != "" && kinds[0] != kinds[1] {
				return invalid("The bounds of a range filter must both be numbers or both be text.")
			}
		case "empty", "notEmpty":
			if len(f.Values) != 0 {
				return invalid("%s takes no values.", f.Op)
			}
		case "contains":
			t, ok := "", len(f.Values) == 1
			if ok {
				t, ok = f.Values[0].(string)
			}
			if !ok || t == "" || len(t) > 1000 || !utf8.ValidString(t) {
				return invalid("A contains filter needs one value, the text to look for.")
			}
			f.vals = []string{asciiLower(t)}
		default:
			return invalid("Unknown filter op %q (use in, notIn, range, empty, notEmpty or contains).", f.Op)
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

// sql is the condition on the page row p that the filter stands for. The
// plus before f.val (and f.skey) keeps the props_val and props_skey indexes
// out of the subquery: it is a lookup by (page, key), once per page in scope.
func (f Filter) sql() (string, []any) {
	const has = `EXISTS (SELECT 1 FROM props f WHERE f.page = p.rowid AND f.key = ?`
	switch f.Op {
	case "in":
		marks := strings.TrimSuffix(strings.Repeat("?,", len(f.vals)), ",")
		args := []any{f.Key}
		for _, v := range f.vals {
			args = append(args, v)
		}
		return has + ` AND +f.val IN (` + marks + `))`, args
	case "notIn":
		// One lookup of the page's rows of the property: no row, nothing
		// (NULL); a row whose value is on the list, 1; otherwise 0.
		args := anys(f.vals)
		return `(SELECT max(+f.val IN (` + marks(len(f.vals)) + `)) FROM props f WHERE f.page = p.rowid AND f.key = ?) = 0`, append(args, f.Key)
	case "range":
		cond, args := has, []any{f.Key}
		if f.lo != nil {
			cond += ` AND +f.skey >= ?` // the plus keeps props_skey out of it: a lookup by (page, key) is what this needs
			args = append(args, *f.lo)
		}
		if f.hi != nil {
			cond += ` AND +f.skey <= ?`
			args = append(args, *f.hi)
		}
		return cond + `)`, args
	case "empty":
		return `NOT ` + has + `)`, []any{f.Key}
	case "notEmpty":
		return has + `)`, []any{f.Key}
	default: // contains
		return has + ` AND +f.val LIKE ? ESCAPE '\')`, []any{f.Key, likePattern(f.vals[0])}
	}
}

// likePattern is the pattern of LIKE that matches the texts that contain s.
// LIKE takes the ASCII letters in either case and every other character as it
// is written, which is what contains promises; and unlike lower() it does not
// make a copy of every value it looks at.
func likePattern(s string) string {
	var b strings.Builder
	b.WriteByte('%')
	for i := 0; i < len(s); i++ {
		if c := s[i]; c == '%' || c == '_' || c == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	b.WriteByte('%')
	return b.String()
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
		return "", false, false, invalid("sort=prop: needs the key of a property.")
	}
	return key, rest != name, true, nil
}
