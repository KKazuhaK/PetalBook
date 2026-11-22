package page

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

// Field is one key of a page header.
type Field struct {
	Key   string
	Value any
}

// Meta is a page header as an ordered list of fields. Order matters: the
// header is part of a file people read and diff, so it must survive a round
// trip. Values are string, int64, float64, bool, nil, []any or Meta.
type Meta []Field

// Get returns the value of key.
func (m Meta) Get(key string) (any, bool) {
	for _, f := range m {
		if f.Key == key {
			return f.Value, true
		}
	}
	return nil, false
}

// Index returns the position of key, or -1.
func (m Meta) Index(key string) int {
	for i, f := range m {
		if f.Key == key {
			return i
		}
	}
	return -1
}

// Clone returns a copy that shares no slices with m.
func (m Meta) Clone() Meta {
	if m == nil {
		return nil
	}
	out := make(Meta, len(m))
	for i, f := range m {
		out[i] = Field{f.Key, cloneValue(f.Value)}
	}
	return out
}

func cloneValue(v any) any {
	switch x := v.(type) {
	case Meta:
		return x.Clone()
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = cloneValue(x[i])
		}
		return out
	}
	return v
}

// MarshalJSON writes the fields in order.
func (m Meta) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range m {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(f.Key)
		b.Write(k)
		b.WriteByte(':')
		v, err := json.Marshal(f.Value)
		if err != nil {
			return nil, err
		}
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// UnmarshalJSON reads a JSON object keeping the order of its keys, which
// encoding/json does not do for maps. Repeated keys are an error: which one
// wins would be a guess.
func (m *Meta) UnmarshalJSON(data []byte) error {
	if string(bytes.TrimSpace(data)) == "null" {
		return nil // like encoding/json: null leaves the value alone
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return err
	}
	obj, ok := v.(Meta)
	if !ok {
		return errors.New("meta must be an object")
	}
	*m = obj
	return nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := Meta{}
			seen := map[string]bool{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := kt.(string)
				if !ok {
					return nil, errors.New("object key is not a string")
				}
				if seen[key] {
					return nil, fmt.Errorf("duplicate key %q", key)
				}
				seen[key] = true
				val, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				obj = append(obj, Field{key, val})
			}
			if _, err := dec.Token(); err != nil { // closing brace
				return nil, err
			}
			return obj, nil
		case '[':
			arr := []any{}
			for dec.More() {
				val, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, val)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return arr, nil
		}
		return nil, fmt.Errorf("unexpected %v", t)
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i, nil
		}
		f, err := t.Float64()
		if err != nil {
			return nil, err
		}
		return f, nil
	case string, bool, nil:
		return t, nil
	}
	return nil, fmt.Errorf("unexpected token %v", tok)
}

// valuesEqual compares header values; int and float compare by number so that
// `1.0` in a file and `1` from JSON do not count as a change.
func valuesEqual(a, b any) bool {
	switch x := a.(type) {
	case nil:
		return b == nil
	case string:
		y, ok := b.(string)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case int64, float64:
		if ia, ok := a.(int64); ok {
			if ib, ok := b.(int64); ok {
				return ia == ib
			}
		}
		fa, oka := toFloat(a)
		fb, okb := toFloat(b)
		return oka && okb && (fa == fb || (math.IsNaN(fa) && math.IsNaN(fb)))
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !valuesEqual(x[i], y[i]) {
				return false
			}
		}
		return true
	case Meta:
		// Nested maps compare without regard to key order: browsers reorder
		// integer-like keys, and order inside a nested map carries no meaning
		// we promise to keep.
		y, ok := b.(Meta)
		if !ok || len(x) != len(y) {
			return false
		}
		for _, f := range x {
			v, ok := y.Get(f.Key)
			if !ok || !valuesEqual(f.Value, v) {
				return false
			}
		}
		return true
	}
	return false
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case int64:
		return float64(x), true
	case float64:
		return x, true
	}
	return 0, false
}

// MetaEqual reports whether two headers have the same keys, in the same
// order, with the same values.
func MetaEqual(a, b Meta) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Key != b[i].Key || !valuesEqual(a[i].Value, b[i].Value) {
			return false
		}
	}
	return true
}

// String returns a scalar field as text, or "" for anything else.
func (m Meta) String(key string) string {
	v, _ := m.Get(key)
	return scalarText(v)
}

func scalarText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int64:
		return fmt.Sprint(x)
	case float64:
		return strings.TrimSuffix(fmt.Sprintf("%v", x), ".0")
	case bool:
		return fmt.Sprint(x)
	}
	return ""
}

// ValuesEqual reports whether two header values are the same value: strings
// by text, numbers by number, nested maps without regard to key order.
func ValuesEqual(a, b any) bool { return valuesEqual(a, b) }

// builtinKeys are the header fields the application itself gives a meaning
// to; every other key of a header is a property a page type may describe.
var builtinKeys = map[string]bool{"id": true, "title": true, "subtitle": true, "source": true, "tags": true, "created": true, "type": true}

// IsBuiltinKey reports whether key is one of the header fields with a meaning
// of its own (id, title, subtitle, source, tags, created, type).
func IsBuiltinKey(key string) bool { return builtinKeys[key] }

// IsReservedKey reports whether key is kept for later use by the application
// (publishing): a property may not be given this name.
func IsReservedKey(key string) bool { return key == "publish" || key == "slug" }
