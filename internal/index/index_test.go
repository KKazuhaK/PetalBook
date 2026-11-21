package index

import (
	"database/sql"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// memSource is a space in memory that counts reads.
type memSource struct {
	files map[string]memFile
	reads atomic.Int64
}

type memFile struct {
	data  []byte
	mtime int64
}

func newMem() *memSource { return &memSource{files: map[string]memFile{}} }

var clock int64 = 1_700_000_000_000_000_000

func (m *memSource) put(rel, content string) {
	clock += int64(time.Second)
	m.files[rel] = memFile{[]byte(content), clock}
}

func (m *memSource) Walk(file func(FileInfo) error, dir func(string) error) error {
	for rel, f := range m.files {
		if err := file(FileInfo{rel, int64(len(f.data)), f.mtime}); err != nil {
			return err
		}
		if d := path.Dir(rel); d != "." {
			if err := dir(d); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *memSource) Stat(rel string) (FileInfo, error) {
	f, ok := m.files[rel]
	if !ok {
		return FileInfo{}, ErrNotExist
	}
	return FileInfo{rel, int64(len(f.data)), f.mtime}, nil
}

func (m *memSource) DirExists(rel string) bool {
	for p := range m.files {
		if strings.HasPrefix(p, rel+"/") {
			return true
		}
	}
	return false
}

func (m *memSource) Read(rel string) ([]byte, error) {
	m.reads.Add(1)
	f, ok := m.files[rel]
	if !ok {
		return nil, ErrNotExist
	}
	return f.data, nil
}

func pageFile(id, title, subtitle string, tags []string, created, body string) string {
	var b strings.Builder
	b.WriteString("---\nid: " + id + "\n")
	if title != "" {
		b.WriteString("title: " + title + "\n")
	}
	if subtitle != "" {
		b.WriteString("subtitle: " + subtitle + "\n")
	}
	if len(tags) > 0 {
		b.WriteString("tags: [" + strings.Join(tags, ", ") + "]\n")
	}
	if created != "" {
		b.WriteString("created: " + created + "\n")
	}
	b.WriteString("---\n" + body)
	return b.String()
}

func openIndex(t *testing.T) *Index {
	t.Helper()
	x, err := Open(filepath.Join(t.TempDir(), "state", "index.db"), "/space")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { x.Close() })
	return x
}

func syncAll(t *testing.T, x *Index, src Source, full bool) Stats {
	t.Helper()
	st, err := x.Sync(src, full)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func ids(r Result) []string {
	out := []string{}
	for _, s := range r.Items {
		out = append(out, s.ID)
	}
	return out
}

func search(t *testing.T, x *Index, q string) []string {
	t.Helper()
	r, err := x.List(Query{Text: q, Limit: 200})
	if err != nil {
		t.Fatalf("search %q: %v", q, err)
	}
	got := ids(r)
	sort.Strings(got)
	return got
}

func TestSearchChineseLatinAndPunctuation(t *testing.T) {
	m := newMem()
	m.put("a.md", pageFile("AAAAAAAAAA", "暮雪", "", nil, "", "栀子花开了\n"))
	m.put("b.md", pageFile("BBBBBBBBBB", "", "", nil, "", "栀子，花开了\n"))
	m.put("c.md", pageFile("CCCCCCCCCC", "", "", nil, "", "白玉兰和栀子\n\n花园里\n"))
	m.put("d.md", pageFile("DDDDDDDDDD", "Deployed", "a rainy night", nil, "", "PetalBook ships on a Rainy night.\nsecond line\n"))
	m.put("e.md", pageFile("EEEEEEEEEE", "", "", nil, "", "诗行一，到了栀子\n花落了\n"))
	m.put("f.md", pageFile("FFFFFFFFFF", "", "", nil, "", "海\n"))
	m.put("g.md", pageFile("GGGGGGGGGG", "", "", nil, "", "**栀子**花 [链接](http://example.com/x)\n"))
	x := openIndex(t)
	syncAll(t, x, m, false)

	cases := []struct {
		q    string
		want []string
	}{
		{"栀子花", []string{"AAAAAAAAAA", "GGGGGGGGGG"}}, // not b (comma), c (paragraph), e (line break)
		{"栀子", []string{"AAAAAAAAAA", "BBBBBBBBBB", "CCCCCCCCCC", "EEEEEEEEEE", "GGGGGGGGGG"}},
		{"栀", []string{"AAAAAAAAAA", "BBBBBBBBBB", "CCCCCCCCCC", "EEEEEEEEEE", "GGGGGGGGGG"}},
		{"海", []string{"FFFFFFFFFF"}},
		{"暮雪", []string{"AAAAAAAAAA"}},                               // title
		{"花开", []string{"AAAAAAAAAA", "BBBBBBBBBB"}},                 // across no punctuation
		{"栀子，花", []string{"BBBBBBBBBB", "CCCCCCCCCC", "EEEEEEEEEE"}}, // punctuation in the query matches any separator: comma, paragraph or verse break
		{"栀子 花开", []string{"AAAAAAAAAA", "BBBBBBBBBB"}},
		{"栀子 白玉兰", []string{"CCCCCCCCCC"}}, // AND
		{"petalbook", []string{"DDDDDDDDDD"}},
		{"RAINY NIGHT", []string{"DDDDDDDDDD"}}, // title/subtitle/body, case-insensitive
		{"rainy deployed", []string{"DDDDDDDDDD"}},
		{"rainy-night", []string{}},               // a hyphen is not a space
		{"night. second", []string{"DDDDDDDDDD"}}, // punctuation in the query matches the same punctuation
		{"line second", []string{"DDDDDDDDDD"}},   // separate terms are ANDed, in any order
		{"line second absent", []string{}},
		{"链接", []string{"GGGGGGGGGG"}},
		{"example", []string{}}, // link targets are not text
		{"，，", []string{}},      // nothing searchable
		{"", nil},
	}
	for _, c := range cases {
		if c.q == "" {
			r, _ := x.List(Query{Text: "", Limit: 200})
			if len(r.Items) != 7 {
				t.Errorf("empty query should list everything, got %d", len(r.Items))
			}
			continue
		}
		if got := search(t, x, c.q); !reflect.DeepEqual(got, c.want) && !(len(got) == 0 && len(c.want) == 0) {
			t.Errorf("search %q = %v, want %v", c.q, got, c.want)
		}
	}
	// "second line" is on two lines of Latin text and still one phrase.
	if got := search(t, x, "ships on a rainy"); len(got) != 1 {
		t.Errorf("phrase of Latin words: %v", got)
	}
}

func TestFoldAndMatchQuery(t *testing.T) {
	if got := strings.Join(strings.Fields(Fold("栀子，花开了")), "|"); got != "栀|子||花|开|了" {
		t.Fatalf("%q", got)
	}
	if got := strings.Join(strings.Fields(Fold("Hello,   World!")), "|"); got != "Hello||World" {
		t.Fatalf("%q", got)
	}
	if got := strings.Join(strings.Fields(Fold("foo\nbar")), "|"); got != "foo|bar" {
		t.Fatalf("latin line wrap must not separate: %q", got)
	}
	if got := strings.Join(strings.Fields(Fold("一\n二")), "|"); got != "一||二" {
		t.Fatalf("verse line break: %q", got)
	}
	if got := strings.Join(strings.Fields(Fold("a\n\nb")), "|"); got != "a||b" {
		t.Fatalf("paragraph: %q", got)
	}
	if m, ok := MatchQuery(`栀子 "花"`); !ok || m != `"栀 子" AND "花"` {
		t.Fatalf("%q %v", m, ok)
	}
	if _, ok := MatchQuery(" … "); ok {
		t.Fatal("punctuation only is not a query")
	}
}

func TestSyncReadsOnlyWhatChanged(t *testing.T) {
	m := newMem()
	for i := 0; i < 40; i++ {
		m.put(fmt.Sprintf("dir%d/p%d.md", i%4, i), pageFile(fmt.Sprintf("%010d", i), fmt.Sprintf("T%d", i), "", nil, "", "body\n"))
	}
	x := openIndex(t)
	st := syncAll(t, x, m, false)
	if st.Pages != 40 || st.Added != 40 || m.reads.Load() != 40 {
		t.Fatalf("%+v reads=%d", st, m.reads.Load())
	}
	m.reads.Store(0)
	st = syncAll(t, x, m, false)
	if m.reads.Load() != 0 || st.Added+st.Updated+st.Removed != 0 || st.Pages != 40 {
		t.Fatalf("unchanged space was read again: %+v reads=%d", st, m.reads.Load())
	}
	m.put("dir1/p1.md", pageFile("0000000001", "Changed", "", nil, "", "new body\n"))
	delete(m.files, "dir2/p2.md")
	m.put("new.md", pageFile("NNNNNNNNNN", "New", "", nil, "", "x"))
	st = syncAll(t, x, m, false)
	if m.reads.Load() != 2 || st.Updated != 1 || st.Added != 1 || st.Removed != 1 || st.Pages != 40 {
		t.Fatalf("%+v reads=%d", st, m.reads.Load())
	}
	// A forced rebuild reads everything.
	m.reads.Store(0)
	syncAll(t, x, m, true)
	if m.reads.Load() != 40 {
		t.Fatalf("full rebuild read %d files", m.reads.Load())
	}
}

// dump captures everything the API can observe through the index.
func dump(t *testing.T, x *Index) map[string]any {
	t.Helper()
	out := map[string]any{}
	for _, s := range []string{"updated", "-updated", "created", "-created", "title", "-title"} {
		var all []Summary
		cur := ""
		for {
			r, err := x.List(Query{Sort: s, Limit: 7, Cursor: cur})
			if err != nil {
				t.Fatal(err)
			}
			all = append(all, r.Items...)
			if r.Next == "" {
				break
			}
			cur = r.Next
		}
		out["sort "+s] = all
	}
	tree, _ := x.Tree()
	tags, _ := x.Tags()
	probs, _ := x.Problems()
	out["tree"], out["tags"], out["problems"] = tree, tags, probs
	out["search"] = search(t, x, "栀子")
	f := "a"
	r, _ := x.List(Query{Folder: &f, Recursive: true, Tags: []string{"x"}, Limit: 200})
	out["filtered"] = r.Items
	return out
}

func TestRebuildAndIncrementalAgree(t *testing.T) {
	m := newMem()
	m.put("a/one.md", pageFile("1111111111", "One", "sub", []string{"x", "y"}, "2025-01-05", "栀子花\n"))
	m.put("a/b/two.md", pageFile("2222222222", "", "", []string{"x"}, "2025-02-01", "无题 栀子\n"))
	m.put("three.md", pageFile("3333333333", "Three", "", nil, "", "plain\n"))
	m.put("c/four.md", pageFile("4444444444", "四", "", []string{"y"}, "2024-12-31", "栀子，花\n"))
	m.put("c/headerless.md", "no header at all\n")
	m.put("c/zdup.md", pageFile("4444444444", "Dup", "", nil, "", "same id as four\n"))

	incremental := openIndex(t)
	syncAll(t, incremental, m, false)

	// Edit through the incremental calls the server uses on every write.
	apply := func(rel, content string) {
		m.put(rel, content)
		if _, err := incremental.Put(rel, []byte(content), m.files[rel].mtime); err != nil {
			t.Fatal(err)
		}
	}
	apply("a/one.md", pageFile("1111111111", "One edited", "sub", []string{"y"}, "2025-01-05", "栀子花 和 更多\n"))
	apply("a/five.md", pageFile("5555555555", "Five", "", []string{"x", "z"}, "2025-03-03", "栀子\n"))
	// A rename: same id, new path.
	data := m.files["three.md"].data
	delete(m.files, "three.md")
	apply("a/Three renamed.md", string(data))
	if err := incremental.AddFolder("empty/deeper"); err != nil {
		t.Fatal(err)
	}
	delete(m.files, "a/b/two.md")
	if err := incremental.Remove("2222222222"); err != nil {
		t.Fatal(err)
	}
	// On a real disk the emptied folders are still there; say so in the source.
	src := &withDirs{memSource: m, extra: []string{"empty/deeper", "a/b"}}

	got := dump(t, incremental)

	rebuilt := openIndex(t)
	syncAll(t, rebuilt, src, true)
	want := dump(t, rebuilt)
	for k := range want {
		if !reflect.DeepEqual(got[k], want[k]) {
			t.Errorf("%s differs\nincremental: %+v\nrebuilt:     %+v", k, got[k], want[k])
		}
	}
	// A third path: incremental Sync from the old state (what start-up does).
	resynced := incremental
	syncAll(t, resynced, src, false)
	if again := dump(t, resynced); !reflect.DeepEqual(again, want) {
		t.Errorf("start-up sync after incremental edits changed the result")
	}
	probs, _ := rebuilt.Problems()
	if len(probs) != 2 {
		t.Fatalf("problems: %+v", probs)
	}
	codes := probs[0].Code + "," + probs[1].Code
	if codes != "no_header,duplicate_id" {
		t.Fatalf("problem codes %s (%+v)", codes, probs)
	}
}

type withDirs struct {
	*memSource
	extra []string
}

func (w *withDirs) DirExists(rel string) bool {
	for _, d := range w.extra {
		if d == rel || strings.HasPrefix(d, rel+"/") {
			return true
		}
	}
	return w.memSource.DirExists(rel)
}

func (w *withDirs) Walk(file func(FileInfo) error, dir func(string) error) error {
	if err := w.memSource.Walk(file, dir); err != nil {
		return err
	}
	for _, d := range w.extra {
		if err := dir(d); err != nil {
			return err
		}
	}
	return nil
}

func TestListFiltersSortsAndPagination(t *testing.T) {
	m := newMem()
	// 30 pages, deliberately with equal timestamps and titles to test tie-breaks.
	for i := 0; i < 30; i++ {
		folder := []string{"", "x", "x/y", "z"}[i%4]
		title := []string{"b", "a", "", "c"}[i%4]
		tags := []string{"t1"}
		if i%3 == 0 {
			tags = append(tags, "t2")
		}
		created := fmt.Sprintf("2025-01-%02d", 1+i%9)
		rel := path.Join(folder, fmt.Sprintf("p%02d.md", i))
		m.put(rel, pageFile(fmt.Sprintf("%010d", i), title, "", tags, created, fmt.Sprintf("body %d\n", i)))
	}
	// Same mtime for a block of files.
	for i := 0; i < 10; i++ {
		for rel, f := range m.files {
			if strings.HasSuffix(rel, fmt.Sprintf("p%02d.md", i)) {
				f.mtime = 1_800_000_000_000_000_000
				m.files[rel] = f
			}
		}
	}
	x := openIndex(t)
	syncAll(t, x, m, false)

	walk := func(q Query) []Summary {
		var all []Summary
		seen := map[string]bool{}
		for n := 0; n < 100; n++ {
			r, err := x.List(q)
			if err != nil {
				t.Fatalf("%+v: %v", q, err)
			}
			for _, s := range r.Items {
				if seen[s.ID] {
					t.Fatalf("%+v: %s returned twice", q, s.ID)
				}
				seen[s.ID] = true
			}
			all = append(all, r.Items...)
			if r.Next == "" {
				return all
			}
			q.Cursor = r.Next
		}
		t.Fatal("pagination does not end")
		return nil
	}

	for _, s := range []string{"updated", "-updated", "created", "-created", "title", "-title", ""} {
		all := walk(Query{Sort: s, Limit: 4})
		if len(all) != 30 {
			t.Fatalf("sort %q returned %d pages", s, len(all))
		}
		// The paged order equals the order of one big query.
		one, _ := x.List(Query{Sort: s, Limit: 200})
		if !reflect.DeepEqual(ids(Result{Items: all}), ids(one)) {
			t.Fatalf("sort %q: paged order differs from a single query", s)
		}
	}
	// Title sort: titled pages first by title, untitled last, by created.
	all := walk(Query{Sort: "title", Limit: 200})
	seenUntitled := false
	var lastTitle, lastCreated string
	for _, s := range all {
		if s.Untitled {
			if !seenUntitled {
				lastCreated = ""
			}
			seenUntitled = true
			if s.Created < lastCreated {
				t.Fatalf("untitled pages not ordered by created: %s after %s", s.Created, lastCreated)
			}
			lastCreated = s.Created
			continue
		}
		if seenUntitled {
			t.Fatalf("titled page %s after untitled ones", s.ID)
		}
		if s.Title < lastTitle {
			t.Fatalf("titles out of order: %q after %q", s.Title, lastTitle)
		}
		lastTitle = s.Title
	}
	desc := walk(Query{Sort: "-title", Limit: 200})
	if !desc[len(desc)-1].Untitled || desc[0].Title != "c" {
		t.Fatalf("-title: first %q last untitled=%v", desc[0].Title, desc[len(desc)-1].Untitled)
	}

	root, xf := "", "x"
	if got := walk(Query{Folder: &root, Limit: 5}); len(got) != 8 { // i%4==0 -> 0,4,...,28
		t.Fatalf("root folder: %d", len(got))
	}
	for _, s := range walk(Query{Folder: &xf, Limit: 3}) {
		if s.Folder != "x" {
			t.Fatalf("non-recursive folder returned %q", s.Folder)
		}
	}
	rec := walk(Query{Folder: &xf, Recursive: true, Limit: 3})
	folders := map[string]int{}
	for _, s := range rec {
		folders[s.Folder]++
	}
	if len(folders) != 2 || folders["x"] == 0 || folders["x/y"] == 0 {
		t.Fatalf("recursive: %v", folders)
	}
	if got := walk(Query{Folder: &root, Recursive: true, Limit: 50}); len(got) != 30 {
		t.Fatalf("recursive from root: %d", len(got))
	}
	// A folder whose name merely starts like another must not leak in.
	if got := walk(Query{Folder: ptr("x/"), Recursive: true}); len(got) != 0 {
		t.Fatalf("odd folder: %d", len(got))
	}
	if got := walk(Query{Tags: []string{"t1", "t2"}, Limit: 4}); len(got) != 10 {
		t.Fatalf("two tags (all must match): %d", len(got))
	}
	if got := walk(Query{Tags: []string{"nope"}}); len(got) != 0 {
		t.Fatalf("unknown tag: %d", len(got))
	}
	// Search with a sort, a tag and a cursor.
	if got := walk(Query{Text: "body", Tags: []string{"t2"}, Sort: "title", Limit: 3}); len(got) != 10 {
		t.Fatalf("search+tag+sort: %d", len(got))
	}
	// Relevance order pages by offset.
	if got := walk(Query{Text: "body", Limit: 4}); len(got) != 30 {
		t.Fatalf("relevance paging: %d", len(got))
	}
	if r, _ := x.List(Query{Text: "body", Limit: 30}); r.Next != "" {
		t.Fatalf("next set on the last page: %q", r.Next)
	}

	for _, bad := range []Query{{Limit: 201}, {Limit: -1}, {Sort: "name"}, {Cursor: "!!!"}, {Sort: "created", Cursor: mustCursor(t, x, Query{Sort: "updated", Limit: 1})}} {
		if _, err := x.List(bad); err == nil || !strings.Contains(err.Error(), "invalid query") {
			t.Errorf("%+v: expected an invalid-query error, got %v", bad, err)
		}
	}
}

func ptr(s string) *string { return &s }

func mustCursor(t *testing.T, x *Index, q Query) string {
	t.Helper()
	r, err := x.List(q)
	if err != nil || r.Next == "" {
		t.Fatalf("no cursor: %v %+v", err, r)
	}
	return r.Next
}

func TestTreeAndTags(t *testing.T) {
	m := newMem()
	m.put("a.md", pageFile("AAAAAAAAAA", "A", "", []string{"x", "y"}, "", ""))
	m.put("d/b.md", pageFile("BBBBBBBBBB", "B", "", []string{"x"}, "", ""))
	m.put("d/c.md", pageFile("CCCCCCCCCC", "C", "", []string{"x", "z"}, "", ""))
	x := openIndex(t)
	src := &withDirs{m, []string{"empty", "d/sub/deep"}}
	syncAll(t, x, src, false)
	tree, _ := x.Tree()
	want := []FolderCount{{"", 1}, {"d", 2}, {"d/sub", 0}, {"d/sub/deep", 0}, {"empty", 0}}
	if !reflect.DeepEqual(tree, want) {
		t.Fatalf("%+v", tree)
	}
	tags, _ := x.Tags()
	if !reflect.DeepEqual(tags, []TagCount{{"x", 3}, {"y", 1}, {"z", 1}}) {
		t.Fatalf("%+v", tags)
	}
}

func TestSummaryFields(t *testing.T) {
	m := newMem()
	m.put("Tang/暮雪.md", pageFile("AAAAAAAAAA", "暮雪", "下一个路口", []string{"玉兰"}, "2025-12-03", "# 标题\n\n你听得见 **今天** 屋檐滴水的声音么？\n"))
	m.put("2026-11-21-b2b2.md", pageFile("BBBBBBBBB2", "", "", nil, "", "只有一句话，没有标题。很长很长很长很长很长很长很长很长很长很长很长很长\n"))
	x := openIndex(t)
	syncAll(t, x, m, false)
	r, _ := x.List(Query{Sort: "title"})
	a, b := r.Items[0], r.Items[1]
	if a.Title != "暮雪" || a.Subtitle != "下一个路口" || a.Label != "暮雪" || a.Untitled || a.Folder != "Tang" || a.Path != "Tang/暮雪.md" ||
		a.Created != "2025-12-03" || !reflect.DeepEqual(a.Tags, []string{"玉兰"}) || a.Excerpt != "标题\n你听得见 今天 屋檐滴水的声音么？" {
		t.Fatalf("%+v", a)
	}
	if !b.Untitled || b.Title != "" || b.Folder != "" || b.Tags == nil || !strings.HasSuffix(b.Label, "…") || b.Created != "" {
		t.Fatalf("%+v", b)
	}
	if got := []rune(b.Label); len(got) != 31 {
		t.Fatalf("label length %d", len(got))
	}
}

func TestOpenDiscardsStaleIndexes(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "index.db")
	m := newMem()
	m.put("a.md", pageFile("AAAAAAAAAA", "A", "", nil, "", "x"))
	x, err := Open(db, "/space")
	if err != nil {
		t.Fatal(err)
	}
	syncAll(t, x, m, false)
	x.Close()

	// Same version, same root: kept, nothing to read.
	x, _ = Open(db, "/space")
	m.reads.Store(0)
	syncAll(t, x, m, false)
	if m.reads.Load() != 0 {
		t.Fatal("existing index was not reused")
	}
	x.Close()

	// Another root: thrown away.
	x, _ = Open(db, "/elsewhere")
	if n, _ := x.Count(); n != 0 {
		t.Fatalf("index of another space kept %d pages", n)
	}
	x.Close()

	// A schema version change: thrown away.
	x, _ = Open(db, "/space")
	syncAll(t, x, m, false)
	x.Close()
	raw, _ := sql.Open("sqlite", db)
	if _, err := raw.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion+1)); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	x, _ = Open(db, "/space")
	if n, _ := x.Count(); n != 0 {
		t.Fatalf("old schema kept %d pages", n)
	}
	x.Close()

	// Garbage in the file: replaced.
	if err := writeFile(db, "this is not a database, just text that is long enough to be read as a header"); err != nil {
		t.Fatal(err)
	}
	x, err = Open(db, "/space")
	if err != nil {
		t.Fatalf("corrupt index not recovered: %v", err)
	}
	syncAll(t, x, m, false)
	if n, _ := x.Count(); n != 1 {
		t.Fatal("rebuild after corruption failed")
	}
	x.Close()
}

func writeFile(p, content string) error { return os.WriteFile(p, []byte(content), 0o644) }

func pathOf(t *testing.T, x *Index, id string) string {
	t.Helper()
	p, ok := x.PathByID(id)
	if !ok {
		t.Fatalf("page %s is not indexed", id)
	}
	return p
}

// mv Gamma.md t; mv Delta.md Gamma.md; mv t Delta.md
func TestSwappedFilesAreBothIndexed(t *testing.T) {
	m := newMem()
	m.put("Gamma.md", pageFile("GAMMA00001", "Gamma", "", nil, "", "g\n"))
	m.put("Delta.md", pageFile("DXXTA00001", "Delta", "", nil, "", "d\n"))
	x := openIndex(t)
	syncAll(t, x, m, false)

	g, d := m.files["Gamma.md"], m.files["Delta.md"]
	m.files["Gamma.md"], m.files["Delta.md"] = d, g
	check := func(when string) {
		t.Helper()
		if p := pathOf(t, x, "GAMMA00001"); p != "Delta.md" {
			t.Fatalf("%s: Gamma is at %q", when, p)
		}
		if p := pathOf(t, x, "DXXTA00001"); p != "Gamma.md" {
			t.Fatalf("%s: Delta is at %q", when, p)
		}
		if probs, _ := x.Problems(); len(probs) != 0 {
			t.Fatalf("%s: problems %+v", when, probs)
		}
	}
	syncAll(t, x, m, false)
	check("after the sync")
	// And after a restart: nothing remembered that blocks a re-read.
	x.Close()
	x2, err := Open(x.Path(), "/space")
	if err != nil {
		t.Fatal(err)
	}
	defer x2.Close()
	syncAll(t, x2, m, false)
	x = x2
	check("after a restart")
}

func TestThreeWayRotationOfFiles(t *testing.T) {
	m := newMem()
	for _, n := range []string{"a", "b", "c"} {
		m.put(n+".md", pageFile(strings.ToUpper(n)+"AAAAAAAAA", n, "", nil, "", n+"\n"))
	}
	x := openIndex(t)
	syncAll(t, x, m, false)
	a, b, c := m.files["a.md"], m.files["b.md"], m.files["c.md"]
	m.files["a.md"], m.files["b.md"], m.files["c.md"] = b, c, a // a<-b, b<-c, c<-a
	syncAll(t, x, m, false)
	for id, want := range map[string]string{"AAAAAAAAAA": "c.md", "BAAAAAAAAA": "a.md", "CAAAAAAAAA": "b.md"} {
		if got := pathOf(t, x, id); got != want {
			t.Errorf("%s is at %q, want %q", id, got, want)
		}
	}
	if probs, _ := x.Problems(); len(probs) != 0 {
		t.Fatalf("%+v", probs)
	}
}

func TestDuplicateBecomesAPageWhenItsTwinGoes(t *testing.T) {
	m := newMem()
	m.put("a.md", pageFile("TWXNTWXN01", "First", "", nil, "", "one\n"))
	m.put("b.md", pageFile("TWXNTWXN01", "Second", "", nil, "", "two\n"))
	x := openIndex(t)
	syncAll(t, x, m, false)
	if probs, _ := x.Problems(); len(probs) != 1 || probs[0].Path != "b.md" || probs[0].Code != "duplicate_id" {
		t.Fatalf("%+v", probs)
	}
	// The twin is deleted; b.md itself did not change (same size and mtime).
	delete(m.files, "a.md")
	st := syncAll(t, x, m, false)
	if p := pathOf(t, x, "TWXNTWXN01"); p != "b.md" || st.Problems != 0 {
		t.Fatalf("b.md stayed a problem: %q %+v", p, st)
	}
	// The same through the cheap call used after a delete through the server.
	m.put("c.md", pageFile("TWXNTWXN01", "Third", "", nil, "", "three\n"))
	syncAll(t, x, m, false)
	if probs, _ := x.Problems(); len(probs) != 1 || probs[0].Path != "c.md" {
		t.Fatalf("%+v", probs)
	}
	delete(m.files, "b.md")
	if err := x.Remove("TWXNTWXN01"); err != nil {
		t.Fatal(err)
	}
	if err := x.RetryDuplicates(m); err != nil {
		t.Fatal(err)
	}
	if p := pathOf(t, x, "TWXNTWXN01"); p != "c.md" {
		t.Fatalf("%q", p)
	}
	if probs, _ := x.Problems(); len(probs) != 0 {
		t.Fatalf("%+v", probs)
	}
	// Other problems (no header) are still remembered without being read again.
	m.put("plain.md", "no header\n")
	syncAll(t, x, m, false)
	m.reads.Store(0)
	syncAll(t, x, m, false)
	if m.reads.Load() != 0 {
		t.Fatalf("an unchanged headerless file was read again (%d reads)", m.reads.Load())
	}
}

// A page written between the walk and the apply must not be dropped.
func TestSyncDoesNotDropPagesWrittenDuringTheWalk(t *testing.T) {
	m := newMem()
	m.put("a.md", pageFile("AAAAAAAAAA", "A", "", nil, "", "x"))
	x := openIndex(t)
	syncAll(t, x, m, false)
	src := &hookSource{memSource: m}
	src.afterWalk = func() { // a server write, after the walk saw only a.md
		m.put("late.md", pageFile("RATERATE01", "Late", "", nil, "", "y"))
		x.Put("late.md", m.files["late.md"].data, m.files["late.md"].mtime)
	}
	syncAll(t, x, src, false)
	if _, ok := x.PathByID("RATERATE01"); !ok {
		t.Fatal("a page written during the walk was removed from the index")
	}
}

type hookSource struct {
	*memSource
	afterWalk func()
}

func (h *hookSource) Walk(f func(FileInfo) error, d func(string) error) error {
	err := h.memSource.Walk(f, d)
	if h.afterWalk != nil {
		h.afterWalk()
	}
	return err
}
