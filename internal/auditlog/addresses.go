package auditlog

import (
	"encoding/json"
	"strings"
)

// HiddenAddress is what a row says in place of an address its reader may not see.
const HiddenAddress = "(hidden)"

// looksLikeAddress says whether v is shaped like an email address: something,
// an @, and something with no space or second @.
func looksLikeAddress(v string) bool {
	i := strings.IndexByte(v, '@')
	if i <= 0 || i == len(v)-1 || strings.ContainsAny(v, " \t\r\n") {
		return false
	}
	return !strings.Contains(v[i+1:], "@")
}

// walk calls fn for every string value of v with its path (see
// Kind.AddressFields); fn returns the value to put there.
func walk(v any, path string, fn func(path, s string) string) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			p := k
			if path != "" {
				p = path + "." + k
			}
			x[k] = walk(e, p, fn)
		}
	case []any:
		for i, e := range x {
			x[i] = walk(e, path+"[]", fn)
		}
	case string:
		return fn(path, x)
	}
	return v
}

func declared(k Kind, path string) bool {
	for _, f := range k.AddressFields {
		if f == path {
			return true
		}
	}
	return false
}

// RedactAddresses is detail with each address-shaped value at a place the kind
// declares (Kind.AddressFields) replaced by HiddenAddress unless show says the
// reader may see it. A detail that is not an object, or has none, is returned
// as it was.
func RedactAddresses(kind string, detail json.RawMessage, show func(addr string) bool) json.RawMessage {
	k, ok := Lookup(kind)
	if !ok || len(k.AddressFields) == 0 || len(detail) == 0 {
		return detail
	}
	var d map[string]any
	if json.Unmarshal(detail, &d) != nil {
		return detail
	}
	changed := false
	walk(d, "", func(path, s string) string {
		if declared(k, path) && looksLikeAddress(s) && !show(s) {
			changed = true
			return HiddenAddress
		}
		return s
	})
	if !changed {
		return detail
	}
	out, err := json.Marshal(d)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return out
}

// UndeclaredAddress finds a value that looks like an email address in a place
// the kind does not declare for addresses (Kind.AddressFields): the path and
// the value, or "" when there is none. It is the writer's check that nothing a
// reader must not see is carried where the redaction does not look.
func UndeclaredAddress(kind string, detail string) (path, value string) {
	var d map[string]any
	if detail == "" || json.Unmarshal([]byte(detail), &d) != nil {
		return "", ""
	}
	k, _ := Lookup(kind)
	walk(d, "", func(p, s string) string {
		if path == "" && !declared(k, p) && looksLikeAddress(s) {
			path, value = p, s
		}
		return s
	})
	return path, value
}
