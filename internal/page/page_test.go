package page

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

var roundTripInputs = map[string]string{
	"plain":        "---\nid: 01JD3K8Q7Z\ntitle: 暮雪\ntags: [玉兰, 栀子]\ncreated: 2025-12-03\n---\n你听得见么？\n",
	"comments":     "---\n# my own note\nid: 01JD3K8Q7Z   # the id\ntitle:   \"Quoted  title\"\nseries: 冬季\n\nstatus: 定稿\n---\nbody\n",
	"block tags":   "---\nid: 01JD3K8Q7Z\ntags:\n    - a\n    - b\nextra:\n  nested: {x: 1,   y: [1,2]}\n---\n\n\nbody with blank lines\n\n",
	"no header":    "just text\n\n---\n\nmore\n",
	"empty":        "",
	"header only":  "---\nid: 01JD3K8Q7Z\n---\n",
	"no final nl":  "---\nid: 01JD3K8Q7Z\n---",
	"empty header": "---\n---\nbody",
	"dots close":   "---\nid: 01JD3K8Q7Z\n...\nbody",
	"trailing sp":  "---  \nid: 01JD3K8Q7Z\n---\t\nbody",
	"unterminated": "---\nid: 01JD3K8Q7Z\nbody that is not yaml\n",
	"bad yaml":     "---\nid: [unclosed\n---\nbody\n",
	"list header":  "---\n- a\n- b\n---\nbody\n",
	"hr start":     "---\n\n---\nafter a rule\n",
	"bom":          "\xef\xbb\xbf---\nid: 01JD3K8Q7Z\n---\nbody\n",
	"anchors":      "---\nid: 01JD3K8Q7Z\nbase: &b {k: v}\nother: *b\n---\nx\n",
	"multi doc":    "---\nid: 01JD3K8Q7Z\n---\nbody\n---\nnot a header\n---\n",
}

func TestRoundTripIsByteExact(t *testing.T) {
	for name, in := range roundTripInputs {
		t.Run(name, func(t *testing.T) {
			d := Parse([]byte(in))
			if got := string(d.Bytes()); got != in {
				t.Fatalf("not reproduced\n got %q\nwant %q", got, in)
			}
			// Setting the header it already has, and the body it already has,
			// is not a change either.
			d.SetMeta(d.Meta())
			d.SetBody(d.Body)
			if got := string(d.Bytes()); got != in {
				t.Fatalf("after no-op set\n got %q\nwant %q", got, in)
			}
		})
	}
}

func TestCRLFBecomesLF(t *testing.T) {
	in := "---\r\nid: 01JD3K8Q7Z\r\ntitle: A\r\n---\r\nline one\r\nline two\r\n"
	d := Parse([]byte(in))
	want := strings.ReplaceAll(in, "\r\n", "\n")
	if got := string(d.Bytes()); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if d.Meta().Title() != "A" || d.Body != "line one\nline two\n" {
		t.Fatalf("%v %q", d.Meta(), d.Body)
	}
}

func TestMalformedFilesStayReadable(t *testing.T) {
	cases := []struct{ in, code string }{
		{"hello\n", "no_header"},
		{"---\nid: x\nno close\n", "unterminated_header"},
		{"---\nid: [oops\n---\nb", "bad_yaml"},
		{"---\n- a\n---\nb", "header_not_mapping"},
		{"---\ntitle: x\n---\nb", "no_id"},
		{"---\nid: lowercase1\n---\nb", "bad_id"},
		{"---\nid: 01JD3K8Q7I\n---\nb", "bad_id"}, // I is not in the alphabet
	}
	for _, c := range cases {
		d := Parse([]byte(c.in))
		ps := d.Problems()
		if len(ps) != 1 || ps[0].Code != c.code {
			t.Errorf("%q: problems %v, want %s", c.in, ps, c.code)
		}
		if !strings.Contains(string(d.Bytes()), strings.TrimSpace(strings.Split(c.in, "\n")[0])) {
			t.Errorf("%q: content dropped: %q", c.in, d.Bytes())
		}
	}
	// A broken header is part of the body, so nothing disappears from view.
	d := Parse([]byte("---\nid: [oops\n---\nbody\n"))
	if d.HasHeader() || d.Body != "---\nid: [oops\n---\nbody\n" {
		t.Fatalf("header %v body %q", d.HasHeader(), d.Body)
	}
	if ps := Parse([]byte("---\nid: 01JD3K8Q7Z\n---\nb")).Problems(); len(ps) != 0 {
		t.Fatalf("healthy page reports %v", ps)
	}
}

func TestUnknownFieldsAndOrderSurviveEdits(t *testing.T) {
	in := "---\nid: 01JD3K8Q7Z\nseries: 冬季 # keep me\ntitle: 暮雪\nstatus: 定稿\ncustom:\n  a: 1\n  b: [x, y]\ntags: [玉兰]\n---\nbody\n"
	d := Parse([]byte(in))
	m := d.Meta()
	if keys := keyList(m); strings.Join(keys, ",") != "id,series,title,status,custom,tags" {
		t.Fatalf("order %v", keys)
	}
	// Change only the title: everything else, including the comment, stays.
	m2 := m.Clone()
	m2[m2.Index("title")].Value = "暮雪（改）"
	d.SetMeta(m2)
	got := string(d.Bytes())
	want := "---\nid: 01JD3K8Q7Z\nseries: 冬季 # keep me\ntitle: 暮雪（改）\nstatus: 定稿\ncustom:\n  a: 1\n  b: [x, y]\ntags: [玉兰]\n---\nbody\n"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	// Reordering is honoured ("in the order given").
	m3 := Meta{m[2], m[0], m[1]}
	d.SetMeta(m3)
	if keys := keyList(Parse(d.Bytes()).Meta()); strings.Join(keys, ",") != "title,id,series" {
		t.Fatalf("order after set: %v", keys)
	}
	// Unknown fields are written back as given, even when they are new.
	d = Parse([]byte("---\nid: 01JD3K8Q7Z\n---\nb"))
	var nm Meta
	if err := json.Unmarshal([]byte(`{"id":"01JD3K8Q7Z","zeta":[1,2.5,true,null],"alpha":{"k":"v"},"title":"T"}`), &nm); err != nil {
		t.Fatal(err)
	}
	d.SetMeta(nm)
	back := Parse(d.Bytes()).Meta()
	if !MetaEqual(back, nm) {
		t.Fatalf("round trip through YAML: %v vs %v\n%s", back, nm, d.Bytes())
	}
	if keys := keyList(back); strings.Join(keys, ",") != "id,zeta,alpha,title" {
		t.Fatalf("order %v", keys)
	}
}

func keyList(m Meta) []string {
	var k []string
	for _, f := range m {
		k = append(k, f.Key)
	}
	return k
}

func TestWrittenHeaderIsTidy(t *testing.T) {
	d := Parse([]byte("body only"))
	var m Meta
	_ = json.Unmarshal([]byte(`{"id":"01JD3K8Q7Z","title":"暮雪","tags":["玉兰","黄昏"],"created":"2025-12-03","odd":"true","num":"12","multi":"a\nb"}`), &m)
	d.SetMeta(m)
	got := string(d.Bytes())
	for _, want := range []string{"---\nid: 01JD3K8Q7Z\n", "title: 暮雪\n", "tags: [玉兰, 黄昏]\n", "created: 2025-12-03\n", "odd: \"true\"\n", "num: \"12\"\n", "---\nbody only"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	// A long value is not folded across lines.
	long := strings.Repeat("长", 120)
	d.SetMeta(Meta{{"id", "01JD3K8Q7Z"}, {"subtitle", long}})
	if !strings.Contains(string(d.Bytes()), "subtitle: "+long+"\n") {
		t.Fatalf("long value was reflowed:\n%s", d.Bytes())
	}
	// An id made of digits stays a string.
	d.SetMeta(Meta{{"id", "1234567890"}})
	if id := Parse(d.Bytes()).Meta().ID(); id != "1234567890" {
		t.Fatalf("id %q\n%s", id, d.Bytes())
	}
}

func TestScalarsReadAsText(t *testing.T) {
	d := Parse([]byte("---\nid: 01JD3K8Q7Z\ntitle: 1984\ncreated: 2025-12-03\ntags: [2024, true, x]\nn: 5\nf: 1.5\nb: yes\n---\n"))
	m := d.Meta()
	if m.Title() != "1984" || m.Created() != "2025-12-03" {
		t.Fatalf("%q %q", m.Title(), m.Created())
	}
	if tags := m.Tags(); strings.Join(tags, "|") != "2024|true|x" {
		t.Fatalf("tags %v", tags)
	}
	if v, _ := m.Get("n"); v != int64(5) {
		t.Fatalf("n=%#v", v)
	}
	if v, _ := m.Get("f"); v != 1.5 {
		t.Fatalf("f=%#v", v)
	}
	b, _ := json.Marshal(m)
	if !strings.Contains(string(b), `"created":"2025-12-03"`) || !strings.HasPrefix(string(b), `{"id":"01JD3K8Q7Z","title":"1984"`) {
		t.Fatalf("%s", b)
	}
}

func TestMetaJSONKeepsOrderAndRejectsDuplicates(t *testing.T) {
	var m Meta
	if err := json.Unmarshal([]byte(`{"z":1,"a":{"y":2,"b":3},"m":[{"q":1}]}`), &m); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(m)
	if string(out) != `{"z":1,"a":{"y":2,"b":3},"m":[{"q":1}]}` {
		t.Fatalf("%s", out)
	}
	if err := json.Unmarshal([]byte(`{"a":1,"a":2}`), &m); err == nil {
		t.Fatal("duplicate keys accepted")
	}
	if err := json.Unmarshal([]byte(`[1]`), &m); err == nil {
		t.Fatal("array accepted as meta")
	}
	if b, _ := json.Marshal(Meta(nil)); string(b) != "{}" {
		t.Fatalf("nil meta: %s", b)
	}
}

func TestNewID(t *testing.T) {
	seen := map[string]bool{}
	now := time.Date(2026, 11, 21, 8, 30, 0, 0, time.UTC)
	for i := 0; i < 2000; i++ {
		id := NewID(now)
		if !ValidID(id) || len(id) != 10 {
			t.Fatalf("bad id %q", id)
		}
		seen[id] = true
	}
	if len(seen) < 1990 { // 2000 draws from 2^20: a couple of repeats are fine
		t.Fatalf("too many repeats: %d distinct", len(seen))
	}
	a, b := NewID(now), NewID(now.Add(time.Hour))
	if a[:6] >= b[:6] {
		t.Fatalf("ids do not sort by time: %s %s", a, b)
	}
	if ValidID("01JD3K8Q7") || ValidID("01JD3K8Q7U") || ValidID("01jd3k8q7z") {
		t.Fatal("accepted a malformed id")
	}
	if ShortID("01JD3K8Q7Z") != "8q7z" {
		t.Fatalf("short id %q", ShortID("01JD3K8Q7Z"))
	}
}

func TestFileStem(t *testing.T) {
	long := strings.Repeat("长", 200)
	cases := []struct{ in, want string }{
		{"暮雪", "暮雪"},
		{"  spaced  ", "spaced"},
		{`a/b\c:d*e?f"g<h>i|j`, "abcdefghij"},
		{"...hidden", "hidden"},
		{".", ""},
		{"???", ""},
		{"ends with dots...", "ends with dots"},
		{"tab\tand\nnewline", "tabandnewline"},
		{"CON", "CON_"},
		{"nul", "nul_"},
		{"", ""},
		{strings.Repeat("a", 100), strings.Repeat("a", 80)},
		{long, strings.Repeat("长", 66)}, // 200 bytes cap
		{"终", "终"},
	}
	for _, c := range cases {
		if got := FileStem(c.in); got != c.want {
			t.Errorf("FileStem(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if s := FileStem(strings.Repeat("🌸", 100)); utf8.RuneCountInString(s) > 80 || len(s) > 200 || !utf8.ValidString(s) {
		t.Fatalf("emoji stem %d runes %d bytes", utf8.RuneCountInString(s), len(s))
	}
}

func TestUntitledAndNumberedStems(t *testing.T) {
	now := time.Date(2026, 11, 21, 8, 30, 0, 0, time.UTC)
	if got := UntitledStem("2025-12-03", "01JD3K8Q7Z", now); got != "2025-12-03-8q7z" {
		t.Fatalf("%q", got)
	}
	if got := UntitledStem("", "01JD3K8Q7Z", now); got != "2026-11-21-8q7z" {
		t.Fatalf("%q", got)
	}
	if got := UntitledStem("not a date", "01JD3K8Q7Z", now); got != "2026-11-21-8q7z" {
		t.Fatalf("%q", got)
	}
	if NumberedStem("x", 1) != "x" || NumberedStem("x", 2) != "x 2" || NumberedStem("x", 13) != "x 13" {
		t.Fatal("numbering")
	}
	for base, want := range map[string]bool{"暮雪": true, "暮雪 2": true, "暮雪 10": true, "暮雪 x": false, "暮雪二": false, "other": false, "暮雪 2 2": false} {
		if SameStem(base, "暮雪") != want {
			t.Errorf("SameStem(%q) = %v", base, !want)
		}
	}
}

func TestPlainText(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"heading", "# 标题\n\n正文", "标题\n\n正文"},
		{"emphasis", "这是**粗体**和*斜体*和~~删除~~", "这是粗体和斜体和删除"},
		{"link", "见[这里](https://example.com/a_b)吧", "见这里吧"},
		{"image", "![一朵花](a.png) 之后", "一朵花 之后"},
		{"ref link", "[文字][1]\n\n[1]: http://x.y", "文字"},
		{"autolink", "<https://example.com>", "https://example.com"},
		{"code", "用 `fmt.Println` 输出", "用 fmt.Println 输出"},
		{"fence", "前\n```go\nx := 1\n```\n后", "前\nx := 1\n后"},
		{"quote list", "> 引用\n- 项一\n1. 项二\n- [x] 完成", "引用\n项一\n项二\n完成"},
		{"hr", "上\n\n---\n\n下", "上\n\n下"},
		{"table", "| a | b |\n|---|---|\n| 1 | 2 |", "a   b\n1   2"},
		{"html", "x<br>y <b>bold</b>", "xy bold"},
		{"escape", `\*not emphasis\*`, "*not emphasis*"},
		{"snake", "snake_case_name and _em_", "snake_case_name and em"},
		{"entity", "a &amp; b", "a & b"},
		{"footnote", "字[^1]\n\n[^1]: 注释", "字\n\n注释"},
		{"blank squeeze", "\n\na\n\n\n\nb\n\n", "a\n\nb"},
		{"plain chinese", "你听得见今天屋檐滴水的声音么？", "你听得见今天屋檐滴水的声音么？"},
	}
	for _, c := range cases {
		got := collapseInner(PlainText(c.in))
		if got != collapseInner(c.want) {
			t.Errorf("%s: PlainText(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// collapseInner makes the table case independent of how many spaces a cell
// separator became.
func collapseInner(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.Join(strings.Fields(l), " ")
	}
	return strings.Join(lines, "\n")
}

func TestLabelAndExcerpt(t *testing.T) {
	if l, u := Label("暮雪", "ignored"); l != "暮雪" || u {
		t.Fatalf("%q %v", l, u)
	}
	if l, u := Label("   ", "\n\n## 第一行 **加粗**\n第二行"); l != "第一行 加粗" || !u {
		t.Fatalf("%q %v", l, u)
	}
	long := strings.Repeat("字", 50)
	l, u := Label("", long+"\n后面")
	if !u || l != strings.Repeat("字", 30)+"…" {
		t.Fatalf("%q", l)
	}
	if l, u := Label("", ""); l != "" || !u {
		t.Fatalf("empty: %q %v", l, u)
	}
	ex := Excerpt("# 标题\n\n" + strings.Repeat("花", 600))
	if n := utf8.RuneCountInString(ex); n != 400 || !strings.HasPrefix(ex, "标题\n花") {
		t.Fatalf("%d runes: %q", n, ex)
	}
	if Excerpt("") != "" {
		t.Fatal("empty excerpt")
	}
}

func TestRevIsTheGitBlobID(t *testing.T) {
	// `printf hello | git hash-object --stdin`
	if got := Rev([]byte("hello")); got != "b6fc4c620b67d95f953a5c1c1230aaab5db5a1b0" {
		t.Fatalf("%s", got)
	}
	// The empty blob has a well-known id.
	if got := Rev(nil); got != "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391" {
		t.Fatalf("%s", got)
	}
}

func TestSetMetaReportsEncodeFailureAndChangesNothing(t *testing.T) {
	in := "---\nid: 01JD3K8Q7Z\ntitle: T\n---\nbody\n"
	d := Parse([]byte(in))
	orig := encodeHeader
	encodeHeader = func(*yaml.Node) (string, error) { return "", errors.New("boom") }
	defer func() { encodeHeader = orig }()
	err := d.SetMeta(Meta{{"id", "01JD3K8Q7Z"}, {"title", "changed"}})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("encode failure swallowed: %v", err)
	}
	if string(d.Bytes()) != in || d.Meta().Title() != "T" {
		t.Fatalf("document changed despite the error: %q", d.Bytes())
	}
	// A header that is not changed needs no encoding and is not an error.
	if err := d.SetMeta(d.Meta()); err != nil {
		t.Fatal(err)
	}
}

func TestNestedMapsCompareWithoutOrder(t *testing.T) {
	a := Meta{{"x", Meta{{"2024", "a"}, {"b", int64(1)}}}}
	b := Meta{{"x", Meta{{"b", 1.0}, {"2024", "a"}}}}
	if !MetaEqual(a, b) {
		t.Fatal("key order inside a nested map is not a change")
	}
	if MetaEqual(a, Meta{{"x", Meta{{"b", int64(1)}}}}) {
		t.Fatal("missing nested key not noticed")
	}
}

// A date in a property is written as a date, as `created` is, and reads back
// as the same text; a title or a tag that looks like one stays text.
func TestDatesInPropertiesAreWrittenUnquoted(t *testing.T) {
	d := Parse([]byte("---\nid: 01JD3K8Q7Z\n---\nbody\n"))
	var m Meta
	_ = json.Unmarshal([]byte(`{"id":"01JD3K8Q7Z","due":"2026-11-22","title":"2026-11-22","tags":["2026-11-23"],"note":"2026-11-22 morning"}`), &m)
	if err := d.SetMeta(m); err != nil {
		t.Fatal(err)
	}
	out := string(d.Bytes())
	for _, want := range []string{"due: 2026-11-22\n", `title: "2026-11-22"`, `"2026-11-23"`, "note: 2026-11-22 morning\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	back := Parse([]byte(out)).Meta()
	if v, _ := back.Get("due"); v != "2026-11-22" {
		t.Errorf("due reads back as %#v", v)
	}
}

func TestKeyKinds(t *testing.T) {
	for _, k := range []string{"id", "title", "subtitle", "source", "tags", "created", "type"} {
		if !IsBuiltinKey(k) {
			t.Errorf("%s is built in", k)
		}
	}
	for _, k := range []string{"status", "publish", "slug", "Title", ""} {
		if IsBuiltinKey(k) {
			t.Errorf("%q is not built in", k)
		}
	}
	if !IsReservedKey("publish") || !IsReservedKey("slug") || IsReservedKey("status") {
		t.Error("publish and slug are reserved, nothing else")
	}
}

// Text that looks like a date but is not one is written as a string, never with
// an explicit tag: `!!timestamp 2026-13-45` cannot be read by other tools, and
// unquoted it would fail in readers that take the shape for a date.
func TestDateLookingTextIsNeverWrittenWithATag(t *testing.T) {
	for _, v := range []string{"2026-13-45", "2026-02-30", "2026-00-10", "2026-1-5", "2026-11-22T10:00:00Z", "2026-11-22 10:00:00", "0000-00-00"} {
		d := Parse([]byte("---\nid: 01JD3K8Q7Z\n---\nbody\n"))
		m := Meta{}
		raw, _ := json.Marshal(map[string]any{"id": "01JD3K8Q7Z", "due": v, "created": v, "list": []string{v}})
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		if err := d.SetMeta(m); err != nil {
			t.Fatal(err)
		}
		out := string(d.Bytes())
		if strings.Contains(out, "!!") {
			t.Errorf("%q: an explicit tag in\n%s", v, out)
		}
		if !strings.Contains(out, `due: "`+v+`"`) {
			t.Errorf("%q should be written quoted:\n%s", v, out)
		}
		back := Parse([]byte(out)).Meta()
		if got, _ := back.Get("due"); got != v {
			t.Errorf("%q reads back as %#v", v, got)
		}
	}
	// A real date stays the plain date form, and nothing else gets a tag.
	d := Parse([]byte("---\nid: 01JD3K8Q7Z\n---\nbody\n"))
	m := Meta{}
	_ = json.Unmarshal([]byte(`{"id":"01JD3K8Q7Z","due":"2026-02-28","leap":"2028-02-29"}`), &m)
	d.SetMeta(m)
	if out := string(d.Bytes()); !strings.Contains(out, "due: 2026-02-28\n") || !strings.Contains(out, "leap: 2028-02-29\n") || strings.Contains(out, "!!") {
		t.Errorf("%s", out)
	}
}

// A real date in a list is written as a date, like one alone, so that what the
// file says survives a round trip through a client; tags stay text.
func TestDatesInListsAreWrittenUnquoted(t *testing.T) {
	d := Parse([]byte("---\nid: 01JD3K8Q7Z\n---\nbody\n"))
	m := Meta{}
	_ = json.Unmarshal([]byte(`{"id":"01JD3K8Q7Z","l":["2026-11-22","2026-13-45","x"],"tags":["2026-11-23"]}`), &m)
	if err := d.SetMeta(m); err != nil {
		t.Fatal(err)
	}
	out := string(d.Bytes())
	if !strings.Contains(out, "l: [2026-11-22, \"2026-13-45\", x]") || !strings.Contains(out, `"2026-11-23"`) || strings.Contains(out, "!!") {
		t.Errorf("%s", out)
	}
	back := Parse([]byte(out))
	if got := back.Meta(); !MetaEqual(got, m) {
		t.Errorf("round trip: %v", got)
	}
	if k := back.InexactKeys(); k["l"] {
		t.Errorf("the list is exact and was reported inexact: %v", k)
	}
	// And what it was before the write: a list of dates in the file is exact, and writing the Meta back changes nothing.
	again := Parse([]byte(out))
	if err := again.SetMeta(again.Meta()); err != nil || string(again.Bytes()) != out {
		t.Errorf("writing it back changed it:\n%s", again.Bytes())
	}
}
