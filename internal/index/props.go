package index

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/KKazuhaK/PetalBook/internal/page"
)

// Limits on what one page puts into the index as properties. They bound the
// work and the size of the index for a header somebody filled with a great
// deal; what is over is left out of the index, never out of the file.
const (
	// MaxPropText is the most characters of a text value that are indexed and
	// shown in a listing.
	MaxPropText = 300
	// maxPropElements is the most elements of a list that are indexed.
	maxPropElements = 100
	// maxPropKeys is the most properties of one page that are indexed.
	maxPropKeys = 200
)

// propRow is one row of the props table: a scalar, or one element of a list.
type propRow struct {
	key  string
	pos  int
	val  string
	skey string
}

// propsOf extracts the properties of a header: every key that is not a field
// the application owns. It returns the rows of the props table and the JSON
// object of the pages.props column ("" when there is nothing).
//
//   - a scalar is one row; a list is one row per element, pos being the
//     position of the element;
//   - an object (and an object or list inside a list) is skipped: it is not
//     something a table cell can show or a filter can ask about;
//   - null and blank text are not values, so the key has no row: "empty" means
//     no row, and a sort puts the page among those without the property;
//   - text is cut to MaxPropText characters.
func propsOf(m page.Meta) ([]propRow, string) {
	var rows []propRow
	var col strings.Builder
	n := 0
	for _, f := range m {
		if page.IsBuiltinKey(f.Key) || n >= maxPropKeys {
			continue
		}
		var out any
		switch v := f.Value.(type) {
		case []any:
			list := make([]any, 0, min(len(v), maxPropElements))
			for _, e := range v {
				if len(list) == maxPropElements {
					break
				}
				if cv, r, ok := scalarProp(e); ok {
					r.key, r.pos = f.Key, len(list)
					rows = append(rows, r)
					list = append(list, cv)
				}
			}
			if len(list) == 0 {
				continue
			}
			out = list
		default:
			cv, r, ok := scalarProp(v)
			if !ok {
				continue
			}
			r.key = f.Key
			rows = append(rows, r)
			out = cv
		}
		b, err := json.Marshal(out)
		if err != nil {
			rows = dropKey(rows, f.Key)
			continue
		}
		k, _ := json.Marshal(f.Key)
		if n == 0 {
			col.WriteByte('{')
		} else {
			col.WriteByte(',')
		}
		col.Write(k)
		col.WriteByte(':')
		col.Write(b)
		n++
	}
	if n == 0 {
		return nil, ""
	}
	col.WriteByte('}')
	return rows, col.String()
}

func dropKey(rows []propRow, key string) []propRow {
	out := rows[:0]
	for _, r := range rows {
		if r.key != key {
			out = append(out, r)
		}
	}
	return out
}

// scalarProp turns a header value into the value shown (cut to length) and
// its row; ok is false for what is not a scalar value.
func scalarProp(v any) (shown any, r propRow, ok bool) {
	switch x := v.(type) {
	case string:
		if strings.TrimSpace(x) == "" {
			return nil, r, false
		}
		x = cutText(x)
		return x, propRow{val: x, skey: strings.ToLower(x)}, true
	case bool:
		if x {
			return x, propRow{val: "true", skey: "1"}, true
		}
		return x, propRow{val: "false", skey: "0"}, true
	case int64:
		return x, propRow{val: strconv.FormatInt(x, 10), skey: numberKey(float64(x))}, true
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, r, false // JSON cannot say it either
		}
		return x, propRow{val: numberText(x), skey: numberKey(x)}, true
	}
	return nil, r, false
}

// cutText keeps the first MaxPropText characters of s.
func cutText(s string) string {
	if len(s) <= MaxPropText {
		return s
	}
	n := 0
	for i := range s {
		if n == MaxPropText {
			return s[:i]
		}
		n++
	}
	return s
}

// numberText is a number as the val column holds it, the same text whether
// the header wrote 3 or 3.0.
func numberText(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// numberKey is a number as text that sorts, byte by byte, in the order of the
// numbers: the bits of the float, with the sign handled so that negative
// numbers come first, in 16 hexadecimal digits.
func numberKey(f float64) string {
	if f == 0 {
		f = 0 // -0 is 0
	}
	b := math.Float64bits(f)
	if b>>63 == 1 {
		b = ^b
	} else {
		b |= 1 << 63
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], b)
	return hex.EncodeToString(buf[:])
}
