package auth

import (
	"encoding/json"
	"strings"
	"testing"
)

func decodeJSON(t *testing.T, s string) any {
	t.Helper()
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestResolvePath(t *testing.T) {
	doc := decodeJSON(t, `{"id": 4711, "data": {"user": {"id": "u1", "n": null}}, "emails": [{"email": "a@x"}, {"email": "b@x"}], "0": "zero", "list": ["p", "q"], "a:b-c_d": true, "deep": {"1": "one"}}`)
	cases := []struct {
		path string
		want any
		ok   bool
	}{
		{"id", json.Number("4711"), true},
		{"data.user.id", "u1", true},
		{"emails.0.email", "a@x", true},
		{"emails.1.email", "b@x", true},
		{"emails.2.email", nil, false},
		{"list.1", "q", true},
		{"list.01", nil, false},
		{"list.-1", nil, false},
		{"list.x", nil, false},
		{"0", "zero", true},      // an object's key that looks like a number
		{"deep.1", "one", true},  // the same, below
		{"emails.0", nil, false}, // not a leaf the caller wanted is still a value...
		{"a:b-c_d", true, true},
		{"data.user.n", nil, true}, // null is found, as null
		{"data.user.missing", nil, false},
		{"id.x", nil, false}, // a number has no members
		{"data.user.id.x", nil, false},
		{"", nil, false},
		{"a..b", nil, false},
	}
	for _, c := range cases {
		got, ok := resolvePath(doc, c.path)
		if c.path == "emails.0" {
			if !ok {
				t.Errorf("%q: not found", c.path)
			}
			continue
		}
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("%q: got %v %v, want %v %v", c.path, got, ok, c.want, c.ok)
		}
	}
	if _, ok := resolvePath(nil, "a"); ok {
		t.Error("a nil document has no members")
	}
	if _, ok := resolvePath([]any{"x"}, "0"); !ok {
		t.Error("a document that is an array is indexed")
	}
}

func TestValidFieldPath(t *testing.T) {
	good := []string{"id", "data.user.id", "emails.0.email", "a:b-c_d", strings.Repeat("a", 64), "a.b.c.d.e.f.g.h"}
	bad := []string{"", "a..b", ".a", "a.", "a b", "a/b", "a[0]", "é", strings.Repeat("a", 65), "a.b.c.d.e.f.g.h.i", strings.Repeat("a.", 128) + "a"}
	for _, p := range good {
		if !validFieldPath(p) {
			t.Errorf("%q should be valid", p)
		}
	}
	for _, p := range bad {
		if validFieldPath(p) {
			t.Errorf("%q should be invalid", p)
		}
	}
}

func TestOAuth2Subject(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{`"abc"`, "abc", true},
		{`"  abc "`, "abc", true},
		{`4711`, "4711", true},
		{`-5`, "-5", true},
		{`0`, "0", true},
		{`9007199254740993`, "9007199254740993", true},
		{`1.5`, "", false},
		{`1.0`, "", false},
		{`1e3`, "", false},
		{`true`, "", false},
		{`null`, "", false},
		{`{}`, "", false},
		{`[1]`, "", false},
		{`""`, "", false},
		{`"   "`, "", false},
		{`"a\u0007b"`, "", false},
	}
	for _, c := range cases {
		got, ok := oauth2Subject(decodeJSON(t, c.in))
		if ok != c.ok || got != c.want {
			t.Errorf("%s: got %q %v, want %q %v", c.in, got, ok, c.want, c.ok)
		}
	}
	if _, ok := oauth2Subject(nil); ok {
		t.Error("nil")
	}
}
