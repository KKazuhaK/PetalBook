package space

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

// --- edits of very many entries ----------------------------------------------

// manyFolders is how many folders the tests of a very large space.yml bind: as
// many as a full file holds (49900 of them fill the 1 MiB). Under the race
// detector, where reading YAML costs many times as much, a quarter of that; the
// full size runs in the tests without it.
func manyFolders() int {
	if raceDetector {
		return 12000
	}
	return 49900
}

// manyEntries is a settings document with n entries under section, the i-th
// written by entry, read as the server reads it.
func manyEntries(t *testing.T, section string, n int, entry func(i int) string) (doc *yaml.Node, size int) {
	t.Helper()
	var b strings.Builder
	b.WriteString(section + ":\n")
	for i := 0; i < n; i++ {
		b.WriteString("  " + entry(i) + "\n")
	}
	doc, err := parseYAMLDoc([]byte(b.String()), errSpaceFile)
	if err != nil || doc == nil {
		t.Fatalf("the generated file does not read: %v", err)
	}
	return doc, b.Len()
}

// timed is bounded, and says how long it took.
func timed(t *testing.T, what string, f func()) {
	t.Helper()
	start := time.Now()
	bounded(t, what, f)
	t.Logf("%s: %v", what, time.Since(start).Round(time.Millisecond))
}

func entriesOf(doc *yaml.Node, section string) int {
	m, _ := mapValue(doc.Content[0], section)
	if m == nil {
		return 0
	}
	return len(m.Content) / 2
}

// What an edit of the folders or tags costs grows with the size of the file,
// not with the size times the number of entries that go: each of these took
// from seconds to more than a minute at this size when entries were cut out of
// the list one at a time.
func TestEditsOfManyEntriesTakeOnePassOverTheFile(t *testing.T) {
	n := manyFolders() // a full space.yml: 1 MiB of `fNNNNN: {type: t}`
	typed := func(i int) string { return fmt.Sprintf("f%05d: {type: t}", i) }
	twoTypes := func(i int) string { return fmt.Sprintf("f%05d: {type: %c}", i, "tu"[i%2]) }

	doc, size := manyEntries(t, "folders", n, typed)
	if size > maxSpaceFile || (!raceDetector && size < maxSpaceFile*9/10) {
		t.Fatalf("the file is %d bytes, not a full one", size)
	}
	timed(t, "unbinding every folder", func() {
		changed, err := unbindWhere(doc.Content[0], func(_, typ string) bool { return typ == "t" })
		if err != nil || !changed {
			t.Errorf("%v %v", changed, err)
		}
	})
	if hasKey(doc.Content[0], "folders") {
		t.Errorf("%d entries are left", entriesOf(doc, "folders"))
	}

	doc, _ = manyEntries(t, "folders", n, twoTypes)
	timed(t, "unbinding every other folder", func() {
		if changed, err := unbindWhere(doc.Content[0], func(_, typ string) bool { return typ == "t" }); err != nil || !changed {
			t.Errorf("%v %v", changed, err)
		}
	})
	if got := entriesOf(doc, "folders"); got != n/2 {
		t.Errorf("%d entries are left, not %d", got, n/2)
	}
	for _, b := range bindingsOf(doc) {
		if b.Type != "u" {
			t.Fatalf("%+v was to go", b)
		}
	}

	doc, _ = manyEntries(t, "folders", n, twoTypes)
	timed(t, "forgetting every other folder", func() {
		i := 0
		if changed, err := forgetFolders(doc.Content[0], func(string) bool { i++; return i%2 == 0 }); err != nil || !changed {
			t.Errorf("%v %v", changed, err)
		}
	})
	if got := entriesOf(doc, "folders"); got != n/2 {
		t.Errorf("%d entries are left, not %d", got, n/2)
	}

	// The same folder written again and again between others, as only a file
	// written by hand has it: unbinding it takes every writing out.
	again := func(i int) string {
		if i%2 == 0 {
			return "D: {type: t}"
		}
		return fmt.Sprintf("f%05d: {type: u}", i)
	}
	doc, _ = manyEntries(t, "folders", n, again)
	timed(t, "unbinding a folder written many times", func() {
		if changed, err := setBinding(doc.Content[0], "D", ""); err != nil || !changed {
			t.Errorf("%v %v", changed, err)
		}
	})
	if got := entriesOf(doc, "folders"); got != n/2 {
		t.Errorf("%d entries are left, not %d", got, n/2)
	}

	doc, _ = manyEntries(t, "folders", n, func(i int) string { return fmt.Sprintf("P/%05d: {type: %c}", i, "tu"[i%2]) })
	timed(t, "moving a folder with many folders in it", func() {
		if changed, err := rebind(doc.Content[0], "P", "Q", false); err != nil || !changed {
			t.Errorf("%v %v", changed, err)
		}
	})
	if b := bindingsOf(doc); len(b) != n || b[0].Folder != "Q/00000" || b[n-1].Folder != fmt.Sprintf("Q/%05d", n-1) {
		t.Errorf("%d bindings after the move", len(b))
	}

	// Taking the views off a folder, or a tag, that is written many times.
	for _, c := range []struct {
		section string
		node    func(*yaml.Node, bool) (*yaml.Node, error)
		norm    func(string) string
	}{
		{"folders", foldersNode, bindingKey},
		{"tags", tagsNode, func(k string) string { return k }},
	} {
		view := "{views: [{id: aaa111, name: one, layout: list}]}"
		doc, _ = manyEntries(t, c.section, n/2, func(i int) string {
			if i%2 == 0 {
				return "D: " + view
			}
			return fmt.Sprintf("f%05d: {color: red}", i)
		})
		entry, _ := mapValue(first(mapValue(doc.Content[0], c.section)), "D")
		rev := newViewList(viewsIn(entry)).Rev
		timed(t, "taking the views off an entry of "+c.section+" written many times", func() {
			changed, _, err := putViewsInto(doc.Content[0], c.section, c.node, "D", c.norm, rev, nil, map[string]bool{})
			if err != nil || !changed {
				t.Errorf("%v %v", changed, err)
			}
		})
		if got := entriesOf(doc, c.section); got != n/4 {
			t.Errorf("%s: %d entries are left, not %d", c.section, got, n/4)
		}
	}

	// A key written many times in one entry.
	var b strings.Builder
	b.WriteString("{")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "type: t, k%d: 1, ", i)
	}
	b.WriteString("}")
	var m yaml.Node
	if err := yaml.Unmarshal([]byte(b.String()), &m); err != nil {
		t.Fatal(err)
	}
	timed(t, "removing a key written many times", func() { removeKey(m.Content[0], "type") })
	if got := len(m.Content[0].Content) / 2; got != n {
		t.Errorf("%d keys are left, not %d", got, n)
	}
}

// Deleting a type that every folder of a full space.yml is bound to: this is
// made inside the one change a space makes at a time, so while it runs nothing
// else is written (it ran for 76 seconds).
func TestDeletingATypeBoundToManyFoldersDoesNotStallTheSpace(t *testing.T) {
	s := open(t)
	if _, err := putCurrent(s, "t", allKinds()); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("folders:\n")
	n := manyFolders()
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "  f%05d: {type: t}\n", i)
	}
	b.WriteString("  other: {type: u}\n")
	writeSpaceYML(t, s, b.String())
	if got := len(s.Bindings()); got != n+1 {
		t.Fatalf("%d bindings", got)
	}
	timed(t, fmt.Sprintf("deleting the type of %d folders", n), func() {
		if err := deleteCurrent(s, "t"); err != nil {
			t.Error(err)
		}
	})
	if m := bindingMap(s); !reflect.DeepEqual(m, map[string]string{"other": "u"}) {
		t.Errorf("%d bindings are left", len(m))
	}
}

// --- the trial of a delete is the edit that is made ---------------------------

// sharedAndKept is a space.yml in which taking the settings of D/a out can be
// written with its aliases when D/keep goes too, and only with every alias
// written out when D/keep stays: the anchor &e then moves to D/keep, behind a
// second &y, and `*y` in it would mean another list. Written out, the file is
// larger than the server reads.
func sharedAndKept() string {
	// 14 aliases of a list of 92 KiB: few nodes, and 1.3 MiB when written out.
	big := "big: &big [" + strings.Repeat(strings.Repeat("x", 100)+", ", 900) + "x]\n"
	var others strings.Builder
	for i := 0; i < 14; i++ {
		fmt.Fprintf(&others, "  o%d: {note: *big}\n", i)
	}
	return big + "folders:\n" +
		"  D/a: &e {type: t, keep: &y [1]}\n" +
		"  X: {k: *y}\n" +
		"  Y: {k: &y [2]}\n" +
		"  D/keep: *e\n" + others.String()
}

func TestADeleteIsTriedOnTheFoldersThatWillBeLeft(t *testing.T) {
	t.Parallel()
	s := open(t)
	p := mustCreate(t, s, "D/a", meta("title", "a"), "x\n")
	q := mustCreate(t, s, "D/keep", meta("title", "k"), "x\n")
	// D/keep holds a file that is not a page, so it stays, and so does its entry.
	if err := os.WriteFile(filepath.Join(s.Root(), "D", "keep", "notes.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := sharedAndKept()
	writeSpaceYML(t, s, src)
	if len(s.Bindings()) != 2 {
		t.Fatalf("the file does not read: %v", s.Bindings())
	}
	ids, err := s.DeleteFolder(Author{}, "D", true)
	wantInvalid(t, "the delete", err, "was not deleted")
	if len(ids) != 0 {
		t.Errorf("pages were deleted: %v", ids)
	}
	for _, id := range []string{p.ID, q.ID} {
		if _, err := s.Get(id); err != nil {
			t.Errorf("a page is gone though the delete was refused: %v", err)
		}
	}
	if spaceYML(s.Root()) != src {
		t.Error("space.yml was changed")
	}
	// Without the file that keeps D/keep, everything goes and the edit fits.
	if err := os.Remove(filepath.Join(s.Root(), "D", "keep", "notes.txt")); err != nil {
		t.Fatal(err)
	}
	if ids, err := s.DeleteFolder(Author{}, "D", true); err != nil || len(ids) != 2 {
		t.Fatalf("%v %v", ids, err)
	}
	got := section(t, spaceYML(s.Root()), "folders")
	if !reflect.DeepEqual(got["D/a"], map[string]any{"keep": []any{1}}) || !reflect.DeepEqual(got["D/keep"], map[string]any{"keep": []any{1}}) ||
		!reflect.DeepEqual(got["X"], map[string]any{"k": []any{1}}) || !reflect.DeepEqual(got["Y"], map[string]any{"k": []any{2}}) {
		t.Errorf("after the delete: %v %v %v %v", got["D/a"], got["D/keep"], got["X"], got["Y"])
	}
	if len(s.Bindings()) != 0 {
		t.Errorf("bindings are left: %v", s.Bindings())
	}
}

func TestADeleteWhoseFoldersTurnOutOtherwiseMakesTheEditThatWasTried(t *testing.T) {
	t.Parallel()
	s := open(t)
	p := mustCreate(t, s, "D/a", meta("title", "a"), "x\n")
	q := mustCreate(t, s, "D/keep", meta("title", "k"), "x\n")
	writeSpaceYML(t, s, sharedAndKept())
	// D/keep was to go and does not (something came to lie in it, say): the
	// edit for the folders as they are then cannot be written.
	keep := filepath.Join(s.Root(), "D", "keep")
	s.ops.Remove = func(p string) error {
		if p == keep {
			return errors.New("the folder is not empty")
		}
		return os.Remove(p)
	}
	ids, err := s.DeleteFolder(Author{}, "D", true)
	s.ops = osOps()
	if !exists(s, "D/keep") {
		t.Fatal("the folder went: the test tests nothing")
	}
	if err != nil || len(ids) != 2 {
		t.Fatalf("%v %v", ids, err)
	}
	for _, id := range []string{p.ID, q.ID} {
		if _, err := s.Get(id); !errors.Is(err, ErrNotFound) {
			t.Errorf("a page is still there: %v", err)
		}
	}
	// The settings went as they were tried, and nothing else changed its meaning.
	text := spaceYML(s.Root())
	if len(text) > maxSpaceFile {
		t.Fatalf("the file is %d bytes", len(text))
	}
	got := section(t, text, "folders")
	if !reflect.DeepEqual(got["D/a"], map[string]any{"keep": []any{1}}) || !reflect.DeepEqual(got["D/keep"], map[string]any{"keep": []any{1}}) ||
		!reflect.DeepEqual(got["X"], map[string]any{"k": []any{1}}) || !reflect.DeepEqual(got["Y"], map[string]any{"k": []any{2}}) {
		t.Errorf("after the delete: %v %v %v %v", got["D/a"], got["D/keep"], got["X"], got["Y"])
	}
	if len(s.Bindings()) != 0 {
		t.Errorf("bindings are left: %v", s.Bindings())
	}
}

// --- the comments of a merge key ----------------------------------------------

func TestWritingAMergeKeyOutKeepsItsComments(t *testing.T) {
	t.Parallel()
	s := open(t)
	mustCreate(t, s, "A", meta("title", "a"), "x\n")
	writeSpaceYML(t, s, `defs: &m {type: t, color: red}
folders:
  A:
    # why A is like the others
    <<: *m # as agreed
    # until the review

    x: 1
`)
	if _, err := putCurrent(s, "u", allKinds()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindFolder(Author{}, "A", "u"); err != nil {
		t.Fatal(err)
	}
	got := spaceYML(s.Root())
	want := map[string]any{"type": "u", "color": "red", "x": 1}
	if a := section(t, got, "folders")["A"]; !reflect.DeepEqual(a, want) {
		t.Errorf("A is now %v", a)
	}
	head, typ, foot, x := strings.Index(got, "# why A is like the others"), strings.Index(got, "type: u # as agreed"), strings.Index(got, "# until the review"), strings.Index(got, "x: 1")
	color := strings.LastIndex(got, "color: red")
	if head < 0 || typ < head || color < typ || foot < color || x < foot {
		t.Errorf("the comments of the merge key are not where it was:\n%s", got)
	}

	// The other shapes, on the mapping itself.
	for _, c := range []struct{ name, src, want string }{
		{"a value written under its key", "m: &m\n  deep:\n    a: 1\ne:\n  # head\n  <<: *m # line\n  # foot\n",
			"# head\ndeep: # line\n    a: 1\n# foot\n"},
		{"nothing written in its place", "m: &m {a: 1}\ne:\n  a: 2\n  # head\n  <<: *m # line\n  b: 3\n",
			"a: 2\n# head\n# line\nb: 3\n"},
		{"nothing written, nothing after", "m: &m {a: 1}\ne:\n  a: 2\n  # head\n  <<: *m # line\n",
			"a: 2\n# head\n# line\n"},
		{"two merge keys", "m: &m {a: 1}\nn: &n {b: 2}\ne:\n  # one\n  <<: *m\n  # two\n  <<: *n # second\n  c: 3\n",
			"# one\na: 1\nb: 2\n# two\n# second\nc: 3\n"},
	} {
		var doc yaml.Node
		if err := yaml.Unmarshal([]byte(c.src), &doc); err != nil {
			t.Fatal(err)
		}
		e, _ := mapValue(doc.Content[0], "e")
		before := map[string]any{}
		eachPair(e, func(k string, v *yaml.Node, _ int) { before[k] = v.Value })
		flattenMerges(e)
		out, err := yaml.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		if squeeze(string(out)) != squeeze(c.want) {
			t.Errorf("%s:\n%s", c.name, out)
		}
		after := map[string]any{}
		eachPair(e, func(k string, v *yaml.Node, _ int) { after[k] = v.Value })
		if hasMerge(e) || !reflect.DeepEqual(before, after) {
			t.Errorf("%s: the mapping means %v, not %v", c.name, after, before)
		}
	}
}

// squeeze takes the blank lines out of a text.
func squeeze(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}
