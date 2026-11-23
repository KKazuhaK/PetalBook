package space

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/KKazuhaK/PetalBook/internal/index"
	"github.com/KKazuhaK/PetalBook/internal/page"
)

func typeFile(root, name string) string {
	return filepath.Join(root, ".petalbook", "types", name+".json")
}

func writeType(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(typeFile(root, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(typeFile(root, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readType(t *testing.T, root, name string) string {
	t.Helper()
	b, err := os.ReadFile(typeFile(root, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func wantInvalid(t *testing.T, what string, err error, contains string) {
	t.Helper()
	var inv *InvalidError
	if !errors.As(err, &inv) {
		t.Errorf("%s: want a refusal, got %v", what, err)
		return
	}
	if contains != "" && !strings.Contains(inv.Msg, contains) {
		t.Errorf("%s: the message %q does not say %q", what, inv.Msg, contains)
	}
}

func allKinds() TypeInput {
	return TypeInput{Title: "文案", Fields: []FieldInput{
		{Key: "状态", Title: "状态", Kind: KindSelect, Options: []string{"草稿", "完成"}},
		{Key: "notes", Kind: KindLongText},
		{Key: "blurb", Kind: KindText, Title: "Blurb"},
		{Key: "labels", Kind: KindMultiSelect, Options: []string{"a", "b", "c"}},
		{Key: "due", Kind: KindDate},
		{Key: "points", Kind: KindNumber},
		{Key: "done", Kind: KindCheckbox},
		{Key: "link", Kind: KindURL},
	}}
}

func TestPutTypeWritesTheSchemaForEveryKind(t *testing.T) {
	s := open(t)
	got, err := putCurrent(s, "文案", allKinds())
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, f := range got.Fields {
		kinds = append(kinds, f.Key+":"+f.Kind)
	}
	if want := "状态:select notes:longtext blurb:text labels:multiselect due:date points:number done:checkbox link:url"; strings.Join(kinds, " ") != want {
		t.Errorf("fields %v, want %s", kinds, want)
	}
	if got.Title != "文案" || got.Fields[0].Title != "状态" || !reflect.DeepEqual(got.Fields[0].Options, []string{"草稿", "完成"}) || !reflect.DeepEqual(got.Fields[3].Options, []string{"a", "b", "c"}) {
		t.Errorf("%+v", got)
	}
	// The file is a JSON Schema as the table of kinds says, fields in order.
	data := readType(t, s.Root(), "文案")
	if !strings.HasSuffix(data, "}\n") {
		t.Errorf("the file ends with a newline: %q", data[len(data)-5:])
	}
	var top struct {
		Title      string                     `json:"title"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal([]byte(data), &top); err != nil || top.Title != "文案" {
		t.Fatalf("%v\n%s", err, data)
	}
	for key, want := range map[string]string{
		"notes":  `{"type":"string","x-multiline":true}`,
		"labels": `{"type":"array","items":{"type":"string","enum":["a","b","c"]},"uniqueItems":true}`,
		"due":    `{"type":"string","format":"date"}`,
		"points": `{"type":"number"}`,
		"done":   `{"type":"boolean"}`,
		"link":   `{"type":"string","format":"uri"}`,
		"blurb":  `{"title":"Blurb","type":"string"}`,
		"状态":     `{"title":"状态","type":"string","enum":["草稿","完成"]}`,
	} {
		var buf bytes.Buffer
		if err := json.Compact(&buf, top.Properties[key]); err != nil || buf.String() != want {
			t.Errorf("%s: %s, want %s", key, buf.String(), want)
		}
	}
	if i, j := strings.Index(data, `"状态"`), strings.Index(data, `"link"`); i < 0 || j < i {
		t.Errorf("the fields are not in the order given:\n%s", data)
	}
	// It is the repository's: committed with the next flush.
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if tree := git(t, s.Root(), "ls-tree", "-r", "--name-only", "HEAD"); !strings.Contains(tree, ".petalbook/types/") {
		t.Errorf("not committed:\n%s", tree)
	}
	// Read back, in both directions of the API.
	types, _, err := s.Types()
	if err != nil || len(types) != 1 || !reflect.DeepEqual(types[0], got) {
		t.Errorf("Types: %+v %v\nwant %+v", types, err, got)
	}
	// The same request again changes nothing.
	again, err := putCurrent(s, "文案", allKinds())
	if err != nil || readType(t, s.Root(), "文案") != data || !reflect.DeepEqual(again, got) {
		t.Errorf("an unchanged type was rewritten: %v", err)
	}
}

func TestPutTypeKeepsWhatItDoesNotUnderstand(t *testing.T) {
	s := open(t)
	writeType(t, s.Root(), "log", `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "description": "kept",
  "title": "Log",
  "properties": {
    "level":  {"type": "string", "enum": ["info", "warn"], "default": "info", "description": "how bad"},
    "count":  {"type": "integer", "minimum": 0, "maximum": 1e3},
    "when":   {"type": "string", "format": "date", "title": "When"},
    "meta":   {"type": "object", "properties": {"a": {"type": "string"}}},
    "anyof":  {"anyOf": [{"type": "string"}, {"type": "number"}]},
    "always": true,
    "tags2":  {"type": "array", "items": {"type": "string", "enum": ["x", "y"], "description": "tag"}, "maxItems": 3}
  },
  "required": ["level"]
}`)
	types, _, err := s.Types()
	if err != nil || len(types) != 1 {
		t.Fatal(types, err)
	}
	var got []string
	for _, f := range types[0].Fields {
		got = append(got, f.Key+":"+f.Kind)
	}
	if want := "level:select count:number when:date meta:other anyof:other always:other tags2:multiselect"; strings.Join(got, " ") != want {
		t.Fatalf("kinds %v, want %s", got, want)
	}
	if string(types[0].Fields[3].Schema) != `{"type": "object", "properties": {"a": {"type": "string"}}}` {
		t.Errorf("the raw schema of an unknown field is as written: %s", types[0].Fields[3].Schema)
	}
	// Change the options and the title of one field, rename another's title,
	// reorder, and drop one: only those change.
	in := TypeInput{Title: "Logs", Fields: []FieldInput{
		{Key: "when", Title: "Date"},
		{Key: "level", Kind: KindSelect, Options: []string{"info", "warn", "error"}},
		{Key: "tags2", Options: []string{"x", "y", "z"}},
		{Key: "count"},
		{Key: "meta"},
		{Key: "anyof", Title: "Either"},
		{Key: "always"},
	}}
	if _, err := putCurrent(s, "log", in); err != nil {
		t.Fatal(err)
	}
	data := readType(t, s.Root(), "log")
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(data), &top); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"$schema", "description", "required"} {
		if _, ok := top[key]; !ok {
			t.Errorf("%s of the schema is gone:\n%s", key, data)
		}
	}
	for _, want := range []string{
		`"title": "Logs"`, `"default": "info"`, `"description": "how bad"`, `"enum": [`, `"error"`,
		`"minimum": 0`, `"maximum": 1e3`, `"type": "integer"`, `"title": "Date"`, `"title": "Either"`,
		`"anyOf"`, `"always": true`, `"maxItems": 3`, `"description": "tag"`, `"z"`, `"a": {`,
	} {
		if !strings.Contains(data, want) {
			t.Errorf("%s is gone:\n%s", want, data)
		}
	}
	// A field whose title was "When" and is now "Date": the old title is not left.
	if strings.Contains(data, `"When"`) {
		t.Errorf("the old title stayed:\n%s", data)
	}
	order := []string{`"when"`, `"level"`, `"tags2"`, `"count"`, `"meta"`, `"anyof"`, `"always"`}
	last := strings.Index(data, `"properties"`)
	for _, k := range order {
		i := strings.Index(data[last:], k)
		if i < 0 {
			t.Fatalf("%s is missing:\n%s", k, data)
		}
		last += i
	}
	// A field of the type that the request leaves out is removed from the type.
	if _, err := putCurrent(s, "log", TypeInput{Title: "Logs", Fields: []FieldInput{{Key: "when"}}}); err != nil {
		t.Fatal(err)
	}
	if data := readType(t, s.Root(), "log"); strings.Contains(data, `"level":`) || !strings.Contains(data, `"$schema"`) || !strings.Contains(data, `"required"`) {
		t.Errorf("%s", data)
	}
}

func TestPutTypeChangingAKindKeepsTheOtherKeywords(t *testing.T) {
	s := open(t)
	writeType(t, s.Root(), "t", `{"properties":{"x":{"type":"string","format":"date","default":"2026-01-01","description":"d"}}}`)
	got, err := putCurrent(s, "t", TypeInput{Fields: []FieldInput{{Key: "x", Kind: KindSelect, Options: []string{"a"}}}})
	if err != nil {
		t.Fatal(err)
	}
	f := got.Fields[0]
	if f.Kind != KindSelect || !reflect.DeepEqual(f.Options, []string{"a"}) {
		t.Errorf("%+v", f)
	}
	data := readType(t, s.Root(), "t")
	if strings.Contains(data, "format") || !strings.Contains(data, `"default": "2026-01-01"`) || !strings.Contains(data, `"description": "d"`) {
		t.Errorf("%s", data)
	}
	// A field that is not understood cannot be given a kind, and a kind cannot be dropped to "other".
	writeType(t, s.Root(), "o", `{"properties":{"x":{"type":"object"}}}`)
	_, err = putCurrent(s, "o", TypeInput{Fields: []FieldInput{{Key: "x", Kind: KindOther}, {Key: "y", Kind: KindOther}}})
	wantInvalid(t, "a new field of kind other", err, "does not know")
	_, err = putCurrent(s, "t", TypeInput{Fields: []FieldInput{{Key: "x", Kind: KindOther}}})
	wantInvalid(t, "a field changed to other", err, "other")
}

func TestTypeFilesThatCannotBeReadAreNotReplaced(t *testing.T) {
	s := open(t)
	root := s.Root()
	tooMany := `{"properties":{` + strings.TrimSuffix(strings.Repeat(`"k",`, 0), ",")
	var b strings.Builder
	b.WriteString(`{"properties":{`)
	for i := 0; i <= MaxTypeFields; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"k%d":{"type":"string"}`, i)
	}
	b.WriteString(`}}`)
	tooMany = b.String()
	cases := map[string]string{
		"syntax":         `{"properties": {`,
		"duplicate":      `{"properties":{"a":{"type":"string"},"a":{"type":"number"}}}`,
		"a list":         `[1,2]`,
		"props a list":   `{"properties":[1]}`,
		"two documents":  `{"properties":{}} {}`,
		"too many":       tooMany,
		"too large":      `{"x":"` + strings.Repeat("x", MaxTypeFile) + `"}`,
		"empty file":     ``,
		"yaml not json?": "properties:\n  a: {type: string}\n",
	}
	for name, content := range cases {
		writeType(t, root, "bad", content)
		_, err := putCurrent(s, "bad", TypeInput{Fields: []FieldInput{{Key: "a", Kind: KindText}}})
		wantInvalid(t, name, err, "fixed by hand")
		if got := readType(t, root, "bad"); got != content {
			t.Errorf("%s: the file was replaced", name)
		}
		types, _, err := s.Types()
		if err != nil || len(types) != 1 || types[0].Name != "bad" || !strings.Contains(types[0].Error, "fixed by hand") || len(types[0].Fields) != 0 {
			t.Errorf("%s: listed as %+v %v", name, types, err)
		}
	}
	// Not a regular file: a folder or a link out of the space.
	os.Remove(typeFile(root, "bad"))
	outside := filepath.Join(t.TempDir(), "outside.json")
	os.WriteFile(outside, []byte(`{"properties":{}}`), 0o644)
	if err := os.Symlink(outside, typeFile(root, "bad")); err != nil {
		t.Skip("no symbolic links here")
	}
	_, err := putCurrent(s, "bad", TypeInput{})
	wantInvalid(t, "a link", err, "fixed by hand")
	if b, _ := os.ReadFile(outside); string(b) != `{"properties":{}}` {
		t.Error("a file outside the space was written through a link")
	}
	// The way out is to delete it.
	if err := deleteCurrent(s, "bad"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(typeFile(root, "bad")); !os.IsNotExist(err) {
		t.Errorf("%v", err)
	}
}

func TestTypeRules(t *testing.T) {
	s := open(t)
	ok := func(key string) TypeInput { return TypeInput{Fields: []FieldInput{{Key: key, Kind: KindText}}} }
	for _, key := range []string{"id", "title", "subtitle", "source", "tags", "created", "type", "publish", "slug", "", strings.Repeat("a", 65), "a:b", "a\nb", "a\x00"} {
		_, err := putCurrent(s, "t", ok(key))
		wantInvalid(t, fmt.Sprintf("the key %q", key), err, "")
	}
	for _, key := range []string{"状态", strings.Repeat("a", 64), "with space", "Title", "a.b"} {
		if _, err := putCurrent(s, "t", ok(key)); err != nil {
			t.Errorf("the key %q is allowed: %v", key, err)
		}
	}
	for _, name := range []string{"", ".hidden", "a/b", `a\b`, "a:b", "a ", " a", "a.", strings.Repeat("x", 101), "a\nb", "a*"} {
		_, err := putCurrent(s, name, TypeInput{})
		wantInvalid(t, fmt.Sprintf("the name %q", name), err, "")
	}
	_, err := putCurrent(s, "ok", TypeInput{Fields: []FieldInput{{Key: "a", Kind: KindText}, {Key: "a", Kind: KindText}}})
	wantInvalid(t, "a key twice", err, "twice")
	_, err = putCurrent(s, "ok", TypeInput{Fields: []FieldInput{{Key: "a"}}})
	wantInvalid(t, "a new field without a kind", err, "needs a kind")
	_, err = putCurrent(s, "ok", TypeInput{Fields: []FieldInput{{Key: "a", Kind: "color"}}})
	wantInvalid(t, "an unknown kind", err, "unknown kind")
	for name, f := range map[string]FieldInput{
		"a select with no options":   {Key: "a", Kind: KindSelect},
		"an empty option":            {Key: "a", Kind: KindSelect, Options: []string{"x", " "}},
		"an option twice":            {Key: "a", Kind: KindMultiSelect, Options: []string{"x", "x"}},
		"options on a number":        {Key: "a", Kind: KindNumber, Options: []string{"x"}},
		"a long option":              {Key: "a", Kind: KindSelect, Options: []string{strings.Repeat("x", 201)}},
		"a title with a line break":  {Key: "a", Kind: KindText, Title: "a\nb"},
		"a field title that is long": {Key: "a", Kind: KindText, Title: strings.Repeat("x", 201)},
	} {
		_, err := putCurrent(s, "ok", TypeInput{Fields: []FieldInput{f}})
		wantInvalid(t, name, err, "")
	}
	opts := make([]string, maxOptions+1)
	for i := range opts {
		opts[i] = fmt.Sprint("o", i)
	}
	_, err = putCurrent(s, "ok", TypeInput{Fields: []FieldInput{{Key: "a", Kind: KindSelect, Options: opts}}})
	wantInvalid(t, "too many options", err, "")
	fields := make([]FieldInput, MaxTypeFields+1)
	for i := range fields {
		fields[i] = FieldInput{Key: fmt.Sprint("k", i), Kind: KindText}
	}
	_, err = putCurrent(s, "ok", TypeInput{Fields: fields})
	wantInvalid(t, "too many fields", err, fmt.Sprint(MaxTypeFields))
	if _, err := putCurrent(s, "ok", TypeInput{Fields: fields[:MaxTypeFields]}); err != nil {
		t.Errorf("exactly %d fields are allowed: %v", MaxTypeFields, err)
	}
	// The largest file that is allowed: long options fill it; one more is refused.
	big := make([]string, 400)
	for i := range big {
		big[i] = fmt.Sprintf("%0200d", i)
	}
	var many []FieldInput
	for i := 0; i < 5; i++ {
		many = append(many, FieldInput{Key: fmt.Sprint("s", i), Kind: KindSelect, Options: big})
	}
	_, err = putCurrent(s, "huge", TypeInput{Fields: many})
	wantInvalid(t, "a file over the limit", err, "KiB")
	if _, err := os.Lstat(typeFile(s.Root(), "huge")); !os.IsNotExist(err) {
		t.Error("a refused type left a file")
	}
}

func TestATypeNeedsAFreeNameAndTheSpaceHasALimit(t *testing.T) {
	s := open(t)
	if _, err := putCurrent(s, "Case", TypeInput{}); err != nil {
		t.Fatal(err)
	}
	// Two names that one file system folds to one are one name.
	if _, err := putCurrent(s, "case", TypeInput{}); !errors.Is(err, ErrExists) {
		t.Errorf("%v", err)
	}
	for i := 1; i < MaxTypes; i++ {
		if _, err := putCurrent(s, fmt.Sprintf("t%03d", i), TypeInput{}); err != nil {
			t.Fatal(i, err)
		}
	}
	_, err := putCurrent(s, "one-too-many", TypeInput{})
	wantInvalid(t, "the 101st type", err, fmt.Sprint(MaxTypes))
	// An existing type can still be changed.
	if _, err := putCurrent(s, "t001", TypeInput{Title: "changed"}); err != nil {
		t.Error(err)
	}
	// Files beyond the limit, put there by hand, are listed and not read.
	writeType(t, s.Root(), "zzz", `{"title":"hand made"}`)
	types, _, err := s.Types()
	if err != nil || len(types) != MaxTypes+1 || types[MaxTypes].Name != "zzz" || types[MaxTypes].Error == "" || types[0].Name != "Case" {
		t.Errorf("%d types, last %+v %v", len(types), types[len(types)-1], err)
	}
}

func TestEffectiveType(t *testing.T) {
	s := open(t)
	for _, n := range []string{"文案", "task", "memo"} {
		if _, err := putCurrent(s, n, TypeInput{}); err != nil {
			t.Fatal(err)
		}
	}
	a := mustCreate(t, s, "Tang", meta("title", "A"), "x\n")
	b := mustCreate(t, s, "Tang/Sub/Deep", meta("title", "B"), "x\n")
	c := mustCreate(t, s, "Tang/Sub", meta("title", "C", "type", "memo"), "x\n")
	d := mustCreate(t, s, "Other", meta("title", "D"), "x\n")
	root := mustCreate(t, s, "", meta("title", "R"), "x\n")
	for _, p := range []*Page{a, b, c, d, root} {
		if p.EffectiveType != p.Meta.Type() {
			t.Fatalf("with nothing bound the page has its own type: %+v", p)
		}
	}
	// A new page does not get `type` written into its header: it inherits.
	if _, err := s.BindFolder(Author{}, "Tang", "文案"); err != nil {
		t.Fatal(err)
	}
	e := mustCreate(t, s, "Tang", meta("title", "E"), "x\n")
	if strings.Contains(e.Header, "type") || e.EffectiveType != "文案" {
		t.Errorf("a page in a bound folder: type in header %q, effective %q", e.Header, e.EffectiveType)
	}
	eff := func(p *Page) string {
		t.Helper()
		got, err := s.Get(p.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got.EffectiveType
	}
	for p, want := range map[*Page]string{a: "文案", b: "文案", c: "memo", d: "", root: ""} {
		if got := eff(p); got != want {
			t.Errorf("%s: %q, want %q", p.Path, got, want)
		}
	}
	// A subfolder can be bound on its own, and a header still wins.
	if _, err := s.BindFolder(Author{}, "Tang/Sub", "task"); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[*Page]string{a: "文案", b: "task", c: "memo", d: ""} {
		if got := eff(p); got != want {
			t.Errorf("%s: %q, want %q", p.Path, got, want)
		}
	}
	// The listing resolves the same way.
	res, err := s.List(index.Query{Recursive: true, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	r := s.TypeResolver()
	got := map[string]string{}
	for _, it := range res.Items {
		got[it.Title] = r.For(it.Folder, it.Type)
	}
	if want := map[string]string{"A": "文案", "B": "task", "C": "memo", "D": "", "E": "文案", "R": ""}; !reflect.DeepEqual(got, want) {
		t.Errorf("%v, want %v", got, want)
	}
	// The root can be bound: every page without a type of its own takes it.
	if _, err := s.BindFolder(Author{}, "", "memo"); err != nil {
		t.Fatal(err)
	}
	if got := eff(d); got != "memo" || eff(root) != "memo" || eff(a) != "文案" {
		t.Errorf("root binding: %q %q %q", got, eff(root), eff(a))
	}
	// And unbound again: back to what the folders above say.
	if _, err := s.BindFolder(Author{}, "Tang/Sub", ""); err != nil {
		t.Fatal(err)
	}
	if got := eff(b); got != "文案" {
		t.Errorf("after unbinding: %q", got)
	}
	// A header that names a type there is no file for is still the type.
	f := mustCreate(t, s, "Other", meta("title", "F", "type", "ghost"), "x\n")
	if got := eff(f); got != "ghost" {
		t.Errorf("%q", got)
	}
	// Moving a page moves it into another folder's type.
	moved, err := s.Move(Author{}, d.ID, "Tang")
	if err != nil || moved.EffectiveType != "文案" {
		t.Errorf("%+v %v", moved, err)
	}
	// The bindings are the file's: a restart sees them, and so does a pull.
	if bs := s.Bindings(); len(bs) != 2 || bs[0] != (Binding{"Tang", "文案"}) || bs[1] != (Binding{"", "memo"}) {
		t.Errorf("%+v", bs)
	}
	data, _ := os.ReadFile(filepath.Join(s.Root(), ".petalbook", "space.yml"))
	if !strings.Contains(string(data), "name: My notes") || !strings.Contains(string(data), "folders:") {
		t.Errorf("space.yml:\n%s", data)
	}
	s2 := openAt(t, s.Root())
	if got, _ := s2.Get(a.ID); got.EffectiveType != "文案" {
		t.Errorf("after a restart: %q", got.EffectiveType)
	}
}

func TestBindFolderRefusals(t *testing.T) {
	s := open(t)
	if _, err := putCurrent(s, "t", TypeInput{}); err != nil {
		t.Fatal(err)
	}
	mustCreate(t, s, "F", meta("title", "A"), "x\n")
	if _, err := s.BindFolder(Author{}, "Nowhere", "t"); !errors.Is(err, ErrNoFolder) {
		t.Errorf("%v", err)
	}
	_, err := s.BindFolder(Author{}, "F", "nothing")
	wantInvalid(t, "a type that is not there", err, "no type")
	_, err = s.BindFolder(Author{}, "F", "a/b")
	wantInvalid(t, "a bad type name", err, "")
	_, err = s.BindFolder(Author{}, "../x", "t")
	wantInvalid(t, "a bad folder", err, "")
	// The folder is answered as it is spelled on disk.
	if got, err := s.BindFolder(Author{}, "F", "t"); err != nil || got != "F" {
		t.Errorf("%q %v", got, err)
	}
	// Binding twice changes nothing; the same for unbinding what is not bound.
	file := filepath.Join(s.Root(), ".petalbook", "space.yml")
	before, _ := os.ReadFile(file)
	for _, c := range [][2]string{{"F", "t"}, {"", ""}} {
		if _, err := s.BindFolder(Author{}, c[0], c[1]); err != nil {
			t.Fatal(err)
		}
	}
	if after, _ := os.ReadFile(file); !bytes.Equal(before, after) {
		t.Errorf("the file changed:\n%s\n%s", before, after)
	}
	// A settings file that cannot be used does not stop a binding: it is made
	// on the last version that can be used, and what the file was is kept.
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	big := string(append([]byte("name: x\n# "), bytes.Repeat([]byte("x"), maxSpaceFile)...))
	n := 0
	for name, content := range map[string]string{
		"syntax":          "name: x\nfolders: [unclosed\n",
		"folders a list":  "name: x\nfolders: [a, b]\n",
		"top-level a seq": "- a\n",
		"too large":       big,
	} {
		if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		n++
		g := fmt.Sprintf("G%d", n)
		mustCreate(t, s, g, meta("title", g), "x\n")
		if _, err := s.BindFolder(Author{}, g, "t"); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if m := bindingMap(s); m["F"] != "t" || m[g] != "t" {
			t.Errorf("%s: the bindings are %v", name, m)
		}
		if got, _ := os.ReadFile(file); string(got) == content {
			t.Errorf("%s: the file was not put right", name)
		}
		kept := false
		for _, c := range copiesOf(s.Root(), spaceSettings) {
			kept = kept || readDisk(t, s, c) == content
		}
		if !kept {
			t.Errorf("%s: what the file was is not kept", name)
		}
		if err := s.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	// An entry that is not a list of settings is the one thing left to a
	// person: the file reads, and the server does not write over the entry.
	content := "name: x\nfolders:\n  F: yes\n"
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = s.BindFolder(Author{}, "F", "t")
	wantInvalid(t, "entry a scalar", err, "fixed by hand")
	if got, _ := os.ReadFile(file); string(got) != content {
		t.Errorf("the file was replaced:\n%s", got)
	}
}

func TestBindingsFollowTheirFolders(t *testing.T) {
	s := open(t)
	root := s.Root()
	for _, n := range []string{"a", "b"} {
		if _, err := putCurrent(s, n, TypeInput{}); err != nil {
			t.Fatal(err)
		}
	}
	p := mustCreate(t, s, "随笔/2026", meta("title", "一"), "x\n")
	q := mustCreate(t, s, "随笔/2026/深", meta("title", "二"), "x\n")
	mustCreate(t, s, "随笔twin", meta("title", "孪"), "x\n")
	mustCreate(t, s, "别处", meta("title", "三"), "x\n")
	for folder, typ := range map[string]string{"随笔": "a", "随笔/2026": "b", "随笔twin": "a", "别处": "b"} {
		if _, err := s.BindFolder(Author{}, folder, typ); err != nil {
			t.Fatal(err)
		}
	}
	// Other settings of a folder, and comments, are the person's.
	file := filepath.Join(root, ".petalbook", "space.yml")
	data, _ := os.ReadFile(file)
	data = bytes.Replace(data, []byte("随笔/2026:\n"), []byte("随笔/2026:\n    views: [kept]\n"), 1)
	if err := os.WriteFile(file, append([]byte("# mine\n"), data...), 0o644); err != nil {
		t.Fatal(err)
	}
	asMap := func() map[string]string {
		m := map[string]string{}
		for _, b := range s.Bindings() {
			m[b.Folder] = b.Type
		}
		return m
	}
	// Moving the folder that has a binding, and has bindings below it.
	if _, err := s.MoveFolder(Author{}, "随笔", "归档/旧"); err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"归档/旧": "a", "归档/旧/2026": "b", "随笔twin": "a", "别处": "b"}; !reflect.DeepEqual(asMap(), want) {
		t.Errorf("after the move: %v, want %v", asMap(), want)
	}
	for id, want := range map[string]string{p.ID: "b", q.ID: "b"} {
		if got, _ := s.Get(id); got.EffectiveType != want {
			t.Errorf("%s: %q, want %q", got.Path, got.EffectiveType, want)
		}
	}
	data, _ = os.ReadFile(file)
	if !strings.Contains(string(data), "# mine") || !strings.Contains(string(data), "views: [kept]") || !strings.Contains(string(data), "name: My notes") {
		t.Errorf("what the person wrote is gone:\n%s", data)
	}
	// Renaming one level.
	if _, err := s.MoveFolder(Author{}, "归档/旧", "归档/新"); err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"归档/新": "a", "归档/新/2026": "b", "随笔twin": "a", "别处": "b"}; !reflect.DeepEqual(asMap(), want) {
		t.Errorf("after the rename: %v", asMap())
	}
	// The change of the settings is committed with the move: nothing is left over.
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if st := git(t, root, "status", "--porcelain", "--untracked-files=no"); st != "" {
		t.Errorf("not committed: %q", st)
	}
	if got := git(t, root, "show", "HEAD:.petalbook/space.yml"); !strings.Contains(got, "归档/新/2026") {
		t.Errorf("committed settings:\n%s", got)
	}
	// Deleting a folder takes its bindings, and those of the folders in it.
	if _, err := s.DeleteFolder(Author{}, "归档/新", true); err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"随笔twin": "a", "别处": "b"}; !reflect.DeepEqual(asMap(), want) {
		t.Errorf("after the delete: %v", asMap())
	}
	// A folder that stays (a file in it is not a page) keeps its binding.
	os.WriteFile(filepath.Join(root, "别处", "note.txt"), []byte("mine"), 0o644)
	if _, err := s.DeleteFolder(Author{}, "别处", true); err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"随笔twin": "a", "别处": "b"}; !reflect.DeepEqual(asMap(), want) {
		t.Errorf("a folder that is still there: %v", asMap())
	}
	// The last binding goes with its folder, and the folders key with it.
	if _, err := s.DeleteFolder(Author{}, "随笔twin", true); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(file)
	if strings.Contains(string(data), "随笔twin") || !strings.Contains(string(data), "别处") {
		t.Errorf("%s", data)
	}
}

func TestMovingAFolderIsNotStoppedBySettingsThatCannotBeRead(t *testing.T) {
	s := open(t)
	mustCreate(t, s, "A", meta("title", "x"), "x\n")
	file := filepath.Join(s.Root(), ".petalbook", "space.yml")
	broken := []byte("name: x\nfolders: [unclosed\n")
	if err := os.WriteFile(file, broken, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MoveFolder(Author{}, "A", "B"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(file); !bytes.Equal(got, broken) {
		t.Errorf("the file was written: %s", got)
	}
	if _, err := s.DeleteFolder(Author{}, "B", true); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteTypeRemovesItsBindingsAndNotThePages(t *testing.T) {
	s := open(t)
	for _, n := range []string{"a", "b"} {
		if _, err := putCurrent(s, n, TypeInput{}); err != nil {
			t.Fatal(err)
		}
	}
	p := mustCreate(t, s, "F/G", meta("title", "x", "type", "a"), "x\n")
	mustCreate(t, s, "H", meta("title", "y"), "x\n")
	for folder, typ := range map[string]string{"F": "a", "F/G": "b", "H": "a"} {
		if _, err := s.BindFolder(Author{}, folder, typ); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := deleteCurrent(s, "a"); err != nil {
		t.Fatal(err)
	}
	if bs := s.Bindings(); len(bs) != 1 || bs[0] != (Binding{"F/G", "b"}) {
		t.Errorf("%+v", bs)
	}
	if _, err := os.Lstat(typeFile(s.Root(), "a")); !os.IsNotExist(err) {
		t.Errorf("the file is still there: %v", err)
	}
	// The page's header is not touched: it still says its type.
	got, _ := s.Get(p.ID)
	if got.Meta.Type() != "a" || got.Rev != p.Rev {
		t.Errorf("%+v", got.Meta)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if st := git(t, s.Root(), "status", "--porcelain", "--untracked-files=no"); st != "" {
		t.Errorf("one change, one commit: %q", st)
	}
	if err := deleteCurrent(s, "a"); !errors.Is(err, ErrNotFound) {
		t.Errorf("%v", err)
	}
	wantInvalid(t, "a bad name", deleteCurrent(s, "a/b"), "")
	// A binding to a type whose file went away by hand can still be cleared by deleting the type.
	os.Remove(typeFile(s.Root(), "b"))
	if err := deleteCurrent(s, "b"); err != nil {
		t.Fatal(err)
	}
	if bs := s.Bindings(); len(bs) != 0 {
		t.Errorf("%+v", bs)
	}
}

func TestPatchProps(t *testing.T) {
	s := open(t)
	root := s.Root()
	p := mustCreate(t, s, "F", meta("title", "页", "tags", []any{"a"}, "status", "draft", "keep", "me", "labels", []any{"x"}), "正文\n第二行\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(root, p.Path))
	got, err := s.PatchProps(Author{}, p.ID, p.Rev, meta("status", "done", "points", int64(3), "due", "2026-11-22", "labels", []any{"x", "y"}, "ratio", 2.5, "ok", true), nil)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(root, p.Path))
	if got.Rev == p.Rev || got.Rev != page.Rev(after) || got.Path != p.Path {
		t.Errorf("%+v", got)
	}
	want := string(before)
	want = strings.Replace(want, "status: draft\n", "status: done\n", 1)
	want = strings.Replace(want, "labels: [x]\n", "labels: [x, y]\n", 1)
	want = strings.Replace(want, "---\n正文", "points: 3\ndue: 2026-11-22\nratio: 2.5\nok: true\n---\n正文", 1)
	if string(after) != want {
		t.Errorf("the file:\n%s\nwant\n%s", after, want)
	}
	if got.EffectiveType != "" {
		t.Errorf("%q", got.EffectiveType)
	}
	// Unset, a null, blank text and an empty list remove a property; the body
	// and everything else stay.
	got, err = s.PatchProps(Author{}, p.ID, got.Rev, meta("status", nil, "due", "  ", "labels", []any{}), []string{"keep", "never-there"})
	if err != nil {
		t.Fatal(err)
	}
	after, _ = os.ReadFile(filepath.Join(root, p.Path))
	if want := "---\nid: " + p.ID + "\ntitle: 页\ntags: [a]\ncreated: " + got.Meta.Created() + "\npoints: 3\nratio: 2.5\nok: true\n---\n正文\n第二行\n"; !strings.Contains(string(after), strings.SplitN(want, "created", 2)[0]) || !strings.HasSuffix(string(after), "points: 3\nratio: 2.5\nok: true\n---\n正文\n第二行\n") {
		t.Errorf("%s", after)
	}
	if strings.Contains(string(after), "status") || strings.Contains(string(after), "keep") || strings.Contains(string(after), "labels") || strings.Contains(string(after), "due") {
		t.Errorf("%s", after)
	}
	// The file is committed as one change, and nothing else is.
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if st := git(t, root, "status", "--porcelain", "--untracked-files=no"); st != "" {
		t.Errorf("%q", st)
	}
	// Nothing to change: not written, same rev, no new commit.
	head := git(t, root, "rev-parse", "HEAD")
	same, err := s.PatchProps(Author{}, p.ID, got.Rev, meta("points", int64(3), "ratio", 2.5), []string{"gone"})
	if err != nil || same.Rev != got.Rev {
		t.Errorf("%+v %v", same, err)
	}
	if err := s.Flush(); err != nil || git(t, root, "rev-parse", "HEAD") != head {
		t.Errorf("a patch that changed nothing made a commit: %v", err)
	}
	// The index sees it: properties are queryable straight away.
	res, err := s.List(index.Query{Filters: []index.Filter{{Key: "points", Op: "in", Values: []any{3}}}, Props: true})
	if err != nil || len(res.Items) != 1 || string(res.Items[0].Props) != `{"points":3,"ratio":2.5,"ok":true}` || res.Items[0].Rev != got.Rev {
		t.Errorf("%+v %v", res.Items, err)
	}
	consistent(t, s)

	// A stale rev is the ordinary conflict, with the current page, and writes nothing.
	_, err = s.PatchProps(Author{}, p.ID, p.Rev, meta("points", int64(9)), nil)
	var ce *ConflictError
	if !errors.As(err, &ce) || ce.Current.Rev != got.Rev {
		t.Fatalf("%v", err)
	}
	if now, _ := os.ReadFile(filepath.Join(root, p.Path)); !bytes.Equal(now, after) {
		t.Error("a conflict wrote the file")
	}
	// Not a page.
	_, err = s.PatchProps(Author{}, "ZZZZZZZZZZ", "x", meta("a", "b"), nil)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("%v", err)
	}
	// Fields that are not properties, and keys that cannot be.
	for _, key := range []string{"id", "title", "subtitle", "source", "tags", "created", "type", "publish", "slug", "", "a\nb"} {
		_, err = s.PatchProps(Author{}, p.ID, got.Rev, meta(key, "x"), nil)
		wantInvalid(t, "set "+key, err, "")
		_, err = s.PatchProps(Author{}, p.ID, got.Rev, nil, []string{key})
		wantInvalid(t, "unset "+key, err, "")
	}
	_, err = s.PatchProps(Author{}, p.ID, got.Rev, meta("a", "b"), []string{"a"})
	wantInvalid(t, "set and unset", err, "both")
	many := make(page.Meta, MaxPatchProps+1)
	for i := range many {
		many[i] = page.Field{Key: fmt.Sprint("k", i), Value: "v"}
	}
	_, err = s.PatchProps(Author{}, p.ID, got.Rev, many, nil)
	wantInvalid(t, "too many", err, "")
	// Values of any shape a header can hold: a nested object is kept as it is.
	obj := page.Meta{{Key: "x", Value: int64(1)}}
	if _, err := s.PatchProps(Author{}, p.ID, got.Rev, meta("obj", obj), nil); err != nil {
		t.Fatal(err)
	}
	if now, _ := s.Get(p.ID); !page.ValuesEqual(now.Meta[now.Meta.Index("obj")].Value, obj) {
		t.Errorf("%+v", now.Meta)
	}
}

func TestPatchPropsOfAPageWithoutAHeaderField(t *testing.T) {
	s := open(t)
	p := mustCreate(t, s, "", meta("title", "t"), "b\n")
	got, err := s.PatchProps(Author{}, p.ID, p.Rev, meta("due", "2026-11-22"), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(s.Root(), got.Path))
	if !strings.Contains(string(b), "\ndue: 2026-11-22\n") || strings.Contains(string(b), `"2026`) {
		t.Errorf("a date is written as a date:\n%s", b)
	}
}

// In a space that follows a remote, the type files, the bindings and the
// property patches are changes like any other: pushed before they are
// answered, one commit each, and taken back completely when the remote cannot
// be reached.
func TestTypesBindingsAndPatchesGoThroughTheRemoteCommitPath(t *testing.T) {
	f := newRemoteSpace(t)
	remoteFile := func(rel string) string { return git(t, f.remote, "show", "main:"+rel) }
	n := len(f.remoteLog())
	step := func(what string) {
		t.Helper()
		got := f.remoteLog()
		if len(got) != n+1 {
			t.Fatalf("%s: %d commits, want %d: %v", what, len(got), n+1, got)
		}
		n++
	}
	p := mustCreate(t, f.s, "Tang/Sub", meta("title", "暮雪"), "x\n")
	n = len(f.remoteLog())
	if _, err := putCurrent(f.s, "文案", allKinds()); err != nil {
		t.Fatal(err)
	}
	step("PutType")
	if !strings.Contains(remoteFile(".petalbook/types/文案.json"), `"状态"`) {
		t.Error("the type is not on the remote")
	}
	if _, err := f.s.BindFolder(Author{}, "Tang", "文案"); err != nil {
		t.Fatal(err)
	}
	step("BindFolder")
	if !strings.Contains(remoteFile(".petalbook/space.yml"), "Tang:") {
		t.Error("the binding is not on the remote")
	}
	got, err := f.s.PatchProps(Author{}, p.ID, p.Rev, meta("状态", "完成"), nil)
	if err != nil {
		t.Fatal(err)
	}
	step("PatchProps")
	if got.EffectiveType != "文案" || !strings.Contains(remoteFile(p.Path), "状态: 完成") {
		t.Errorf("%+v", got)
	}
	// The move of the folder and the update of its binding are one commit.
	if _, err := f.s.MoveFolder(Author{}, "Tang", "唐"); err != nil {
		t.Fatal(err)
	}
	step("MoveFolder")
	if y := remoteFile(".petalbook/space.yml"); !strings.Contains(y, "唐:") || strings.Contains(y, "Tang") {
		t.Errorf("space.yml on the remote:\n%s", y)
	}
	if st := git(t, f.root, "status", "--porcelain"); st != "" {
		t.Errorf("status: %q", st)
	}
	if b, _ := os.ReadFile(filepath.Join(f.s.StateDir(), "pending.journal")); len(b) != 0 {
		t.Errorf("the journal still holds %q", b)
	}
	consistent(t, f.s)

	// With the remote down every one of them leaves nothing behind.
	moved, _ := f.s.Get(p.ID)
	f.net.setDown(true)
	before := snapshot(t, f.s)
	unavailable := func(what string, err error) {
		t.Helper()
		var ue *UnavailableError
		if !errors.As(err, &ue) {
			t.Fatalf("%s: want unavailable, got %v", what, err)
		}
		if now := snapshot(t, f.s); !reflect.DeepEqual(before, now) {
			t.Fatalf("%s changed something:\nbefore %+v\nafter  %+v", what, before, now)
		}
		consistent(t, f.s)
	}
	_, err = putCurrent(f.s, "文案", TypeInput{Title: "changed", Fields: []FieldInput{{Key: "due", Kind: KindText}}})
	unavailable("PutType", err)
	_, err = putCurrent(f.s, "new", TypeInput{})
	unavailable("PutType of a new type", err)
	_, err = f.s.BindFolder(Author{}, "唐/Sub", "文案")
	unavailable("BindFolder", err)
	_, err = f.s.PatchProps(Author{}, p.ID, moved.Rev, meta("状态", "草稿"), nil)
	unavailable("PatchProps", err)
	_, err = f.s.MoveFolder(Author{}, "唐", "宋")
	unavailable("MoveFolder", err)
	unavailable("DeleteType", deleteCurrent(f.s, "文案"))
	_, err = f.s.DeleteFolder(Author{}, "唐", true)
	unavailable("DeleteFolder", err)
	if bs := f.s.Bindings(); len(bs) != 1 || bs[0] != (Binding{"唐", "文案"}) {
		t.Errorf("the bindings after the undo: %+v", bs)
	}
	f.net.setDown(false)
	if err := deleteCurrent(f.s, "文案"); err != nil {
		t.Fatal(err)
	}
	if got := f.remoteLog(); len(got) != n+1 {
		t.Errorf("%v", got)
	}
	if y := remoteFile(".petalbook/space.yml"); strings.Contains(y, "folders") {
		t.Errorf("deleting the type left its binding on the remote:\n%s", y)
	}
	consistent(t, f.s)
}

// The writes are journaled before they are made, so a crash before the commit
// still commits them at the next start.
func TestTypeAndBindingWritesAreJournaled(t *testing.T) {
	s := open(t)
	p := mustCreate(t, s, "F", meta("title", "x"), "x\n")
	if _, err := putCurrent(s, "t", allKinds()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindFolder(Author{}, "F", "t"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PatchProps(Author{}, p.ID, p.Rev, meta("points", int64(1)), nil); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(s.StateDir(), "pending.journal"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`.petalbook/types/t.json`, `.petalbook/space.yml`, `"op":"w","p":"` + p.Path} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the journal does not name %s:\n%s", want, b)
		}
	}
	// What the journal names is what the commit holds.
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	tree := git(t, s.Root(), "ls-tree", "-r", "--name-only", "HEAD")
	for _, want := range []string{".petalbook/types/t.json", ".petalbook/space.yml"} {
		if !strings.Contains(tree, want) {
			t.Errorf("%s is not committed:\n%s", want, tree)
		}
	}
}

// currentRev is the rev of the type file as it is now ("" when there is none).
func currentRev(s *Space, name string) string {
	data, err := s.readTypeFile(name)
	switch {
	case err != nil:
		return UnreadableRev
	case data == nil:
		return ""
	}
	return page.Rev(data)
}

// putCurrent is PutType made from the type as it is now.
func putCurrent(s *Space, name string, in TypeInput) (TypeInfo, error) {
	return s.PutType(Author{}, name, currentRev(s, name), in)
}

// deleteCurrent is DeleteType made from the type as it is now.
func deleteCurrent(s *Space, name string) error {
	return s.DeleteType(Author{}, name, currentRev(s, name))
}
