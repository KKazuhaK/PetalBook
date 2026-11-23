package space

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

// --- helpers ---------------------------------------------------------------------

// plainParse is how long this machine, as loaded as it is now and with the
// race detector if it is on, takes to load a settings file of the largest size
// that has no aliases at all: the measure of "no more than the file is large".
var plainParse = sync.OnceValue(func() time.Duration {
	text := []byte("tags:\n  t:\n    views:\n" + strings.Repeat("      - {q: [x, x, x]}\n", maxTagsFile/24-2))
	start := time.Now()
	parseYAMLDoc(text, errTagsFile)
	return time.Since(start)
})

// bounded runs f and fails when it took long or allocated much: what a file
// whose aliases stand for far more than its size must never cost. Long is a
// few loads of a full file without aliases (what the bound replaced took
// hundreds of them, or never ended).
func bounded(t *testing.T, what string, f func()) {
	t.Helper()
	limit := 2*time.Second + 8*plainParse()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	f()
	d := time.Since(start)
	runtime.ReadMemStats(&after)
	if d > limit {
		t.Errorf("%s took %v (the limit here is %v)", what, d, limit)
	}
	if mb := (after.TotalAlloc - before.TotalAlloc) >> 20; mb > 400 {
		t.Errorf("%s allocated %d MiB", what, mb)
	}
}

// aliasBomb is YAML for `key` at the given indent: a list of ten that is
// referred to ten times by the next, depth times over. Under 1 KiB at depth 7,
// it stands for 10^(depth+1) elements.
func aliasBomb(indent, key string, depth int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s%s:\n%s  l0: &l0 [x, x, x, x, x, x, x, x, x, x]\n", indent, key, indent)
	for i := 1; i <= depth; i++ {
		fmt.Fprintf(&b, "%s  l%d: &l%d [", indent, i, i)
		for j := 0; j < 10; j++ {
			if j > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "*l%d", i-1)
		}
		b.WriteString("]\n")
	}
	return b.String()
}

// meaningOf reads YAML the way any other reader of the file would: the
// library resolves the aliases and the merge keys.
func meaningOf(t *testing.T, text string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal([]byte(text), &m); err != nil {
		t.Fatalf("the file does not read: %v\n%s", err, text)
	}
	return m
}

func section(t *testing.T, text, name string) map[string]any {
	t.Helper()
	m, _ := meaningOf(t, text)[name].(map[string]any)
	return m
}

// written is section for a file with nested aliases, which the library
// refuses to resolve: every alias is written out here, with no bound, which is
// what a test may do with a file it made.
func written(t *testing.T, text, name string) map[string]any {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatalf("the file does not read: %v\n%s", err, text)
	}
	var value func(n *yaml.Node) any
	value = func(n *yaml.Node) any {
		switch n.Kind {
		case yaml.AliasNode:
			return value(n.Alias)
		case yaml.SequenceNode:
			out := []any{}
			for _, c := range n.Content {
				out = append(out, value(c))
			}
			return out
		case yaml.MappingNode:
			out := map[string]any{}
			for i := 0; i+1 < len(n.Content); i += 2 {
				out[n.Content[i].Value] = value(n.Content[i+1])
			}
			return out
		}
		return n.Value
	}
	m, _ := value(doc.Content[0]).(map[string]any)[name].(map[string]any)
	return m
}

func viewNames(t *testing.T, s *Space, scope ViewScope) string {
	t.Helper()
	l, err := s.Views(scope)
	if err != nil {
		t.Fatal(err)
	}
	return names(l)
}

// --- the budget of a document ------------------------------------------------

func TestTheBudgetCountsWhatAliasesStandForOnceForTheDocument(t *testing.T) {
	for _, c := range []struct {
		name string
		text string
		ok   bool
	}{
		{"no aliases", "folders:\n  A: {type: t}\n", true},
		{"an alias and a merge key", "d: &d {type: t}\nfolders:\n  A: *d\n  B: {<<: *d}\n", true},
		{"nested, within the budget", aliasBomb("", "defs", 3), true},
		{"nested, far over it", aliasBomb("", "defs", 7), false},
		{"an alias inside its own anchor", "a: &a [*a]\n", false},
		{"many small uses of a large anchor", "a: &a [" + strings.Repeat("x, ", 5000) + "x]\nb: [" + strings.Repeat("*a, ", 30) + "*a]\n", false},
		{"deeper than the server goes", "a: " + strings.Repeat("[", maxDocDepth+5) + strings.Repeat("]", maxDocDepth+5) + "\n", false},
	} {
		start := time.Now()
		doc, err := parseYAMLDoc([]byte(c.text), errSpaceFile)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want readable = %v", c.name, err, c.ok)
		}
		if err == nil && doc == nil {
			t.Errorf("%s: no document", c.name)
		}
		if d := time.Since(start); d > time.Second {
			t.Errorf("%s: took %v", c.name, d)
		}
	}
}

func TestAFileWhoseAliasesStandForTooMuchIsUnreadableNotSlow(t *testing.T) {
	s := open(t)
	mustCreate(t, s, "F", meta("title", "x"), "x\n")
	// Round 1 and round 4: nested aliases in space.yml.
	writeSpaceYML(t, s, "folders:\n  F:\n    type: wenan\n    views:\n      - {id: aaaaaa, name: v, layout: list}\n"+aliasBomb("", "defs", 7))
	bounded(t, "reading space.yml", func() {
		if l, err := s.Views(folderScope("F")); err != nil || len(l.Views) != 0 {
			t.Errorf("views of an unreadable file: %v %+v", err, l)
		}
		if len(s.Bindings()) != 0 {
			t.Errorf("bindings of an unreadable file: %v", s.Bindings())
		}
	})
	_, err := s.PutViews(Author{}, folderScope("F"), newViewList(nil).Rev, []View{{Name: "n", Layout: "list"}})
	wantInvalid(t, "saving views into it", err, "fixed by hand")

	// The same budget for tags.yml: one anchor of 10^4 nodes, used by 61,664
	// views of one tag in a file of 1 MiB.
	var b strings.Builder
	b.WriteString("big: &a4 [" + strings.Repeat("x,", 9999) + "x]\ntags:\n  t:\n    color: red\n    views:\n")
	for b.Len() < maxTagsFile-64 {
		b.WriteString("      - {q: *a4}\n")
	}
	setTagsYML(t, s.Root(), b.String())
	bounded(t, "reading tags.yml", func() {
		if l, err := s.Views(tagScope("t")); err != nil || len(l.Views) != 0 {
			t.Errorf("views of an unreadable tags file: %v, %d views", err, len(l.Views))
		}
		if len(s.TagColors()) != 0 {
			t.Errorf("colours of an unreadable file: %v", s.TagColors())
		}
	})
	bounded(t, "writing a colour into it", func() {
		wantInvalid(t, "a colour", s.SetTagColor(Author{}, "t", "blue"), "fixed by hand")
	})
}

func TestAtMostMaxViewsAreReadOfAnEntry(t *testing.T) {
	s := open(t)
	mustCreate(t, s, "F", meta("title", "x"), "x\n")
	var b strings.Builder
	b.WriteString("folders:\n  F:\n    views:\n")
	for i := 0; i < MaxViews+5; i++ {
		fmt.Fprintf(&b, "      - {id: view%02d, name: n%02d, layout: list}\n", i, i)
	}
	writeSpaceYML(t, s, b.String())
	l, err := s.Views(folderScope("F"))
	if err != nil || len(l.Views) != MaxViews || l.Views[MaxViews-1].ID != fmt.Sprintf("view%02d", MaxViews-1) {
		t.Fatalf("%v: %d views", err, len(l.Views))
	}
	// Saving the views that are read keeps the ones that are not.
	l.Views[0].Name = "renamed"
	if _, err := s.PutViews(Author{}, folderScope("F"), l.Rev, l.Views); err != nil {
		t.Fatal(err)
	}
	got := spaceYML(s.Root())
	for i := MaxViews; i < MaxViews+5; i++ {
		if !strings.Contains(got, fmt.Sprintf("view%02d", i)) {
			t.Errorf("view%02d is gone from the file", i)
		}
	}
	if l2, _ := s.Views(folderScope("F")); len(l2.Views) != MaxViews || l2.Views[0].Name != "renamed" {
		t.Errorf("after saving: %s", names(l2))
	}

	// A tag with tens of thousands of hand-written views answers with MaxViews.
	b.Reset()
	b.WriteString("tags:\n  t:\n    views:\n")
	for i := 0; i < 30000; i++ {
		b.WriteString("      - {name: n, layout: list}\n")
	}
	setTagsYML(t, s.Root(), b.String())
	bounded(t, "reading 30000 views", func() {
		if tl, err := s.Views(tagScope("t")); err != nil || len(tl.Views) != MaxViews {
			t.Errorf("%v: %d views", err, len(tl.Views))
		}
	})
}

// --- reading: an alias means what its anchor means ----------------------------

const sharedSettings = `defaults: &defaults
  type: wenan
  note: shared
folders:
  A:
    type: &t wenan
    views:
      - &base {id: aaa111, name: one, layout: board, filter: &f [{key: series, op: in, values: [a]}], hidden: &h [series]}
  B:
    type: *t
    views:
      - {id: bbb111, name: two, layout: list, filter: *f, hidden: *h}
      - {<<: *base, id: bbb222, name: three}
  C: &c
    type: other
    views: [{id: ccc111, name: four, layout: table}]
  D: *c
  E:
    <<: *defaults
    views: [{id: eee111, name: five, layout: list}]
`

func TestReadersFollowAliasesAndMergeKeys(t *testing.T) {
	s := open(t)
	writeSpaceYML(t, s, sharedSettings)
	want := map[string]string{"A": "wenan", "B": "wenan", "C": "other", "D": "other", "E": "wenan"}
	if got := bindingMap(s); !reflect.DeepEqual(got, want) {
		t.Errorf("bindings %v, want %v", got, want)
	}
	l, _ := s.Views(folderScope("B"))
	if len(l.Views) != 2 {
		t.Fatalf("B: %+v", l)
	}
	two, three := l.Views[0], l.Views[1]
	if len(two.Filter) != 1 || two.Filter[0].Key != "series" || !reflect.DeepEqual(two.Hidden, []string{"series"}) {
		t.Errorf("a filter and hidden columns written as aliases read as %+v", two)
	}
	if three.ID != "bbb222" || three.Name != "three" || three.Layout != "board" || len(three.Filter) != 1 || len(three.Extra) != 0 {
		t.Errorf("a view with a merge key reads as %+v", three)
	}
	if got := viewNames(t, s, folderScope("D")); got != "four" {
		t.Errorf("the views of an entry that is an alias: %q", got)
	}
	// The same in tags.yml, with a `tags` entry that comes in by a merge key.
	setTagsYML(t, s.Root(), `more: &more
  three: {color: green}
tags:
  <<: *more
  one:
    color: &c red
    views: &v [{id: ttt111, name: tv, layout: list}]
  two: {color: *c, views: *v}
`)
	if got := s.TagColors(); !reflect.DeepEqual(got, map[string]string{"one": "red", "two": "red", "three": "green"}) {
		t.Errorf("colours %v", got)
	}
	if got := viewNames(t, s, tagScope("two")); got != "tv" {
		t.Errorf("the views of a tag written as an alias: %q", got)
	}
}

// --- writing: nothing but the target changes -----------------------------------

func TestDeletingAFolderLeavesWhatItsAnchorsMeantToOthers(t *testing.T) {
	s := open(t)
	for _, f := range []string{"A", "B", "C", "D", "E"} {
		mustCreate(t, s, f, meta("title", f), "x\n")
	}
	writeSpaceYML(t, s, sharedSettings)
	before := section(t, spaceYML(s.Root()), "folders")
	if _, err := s.DeleteFolder(Author{}, "A", true); err != nil {
		t.Fatal(err)
	}
	got := spaceYML(s.Root())
	after := section(t, got, "folders")
	delete(before, "A")
	if !reflect.DeepEqual(after, before) {
		t.Errorf("deleting A changed what the others mean:\n%s", got)
	}
	// The reviewers' case: B's type was an alias of A's.
	if m := bindingMap(s); m["B"] != "wenan" || m["A"] != "" {
		t.Errorf("bindings %v", m)
	}
	// The anchors moved to their first alias; nothing was copied.
	for _, want := range []string{"type: &t wenan", "filter: &f", "hidden: &h", "<<: &base"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q is not in:\n%s", want, got)
		}
	}
	// And an entry that is an alias: C goes, D keeps what it meant.
	if _, err := s.DeleteFolder(Author{}, "C", true); err != nil {
		t.Fatal(err)
	}
	delete(before, "C")
	if after := section(t, spaceYML(s.Root()), "folders"); !reflect.DeepEqual(after, before) {
		t.Errorf("deleting C changed what the others mean:\n%s", spaceYML(s.Root()))
	}
	// D is an alias no more; deleting it now takes only its own entry.
	if _, err := s.DeleteFolder(Author{}, "D", true); err != nil {
		t.Fatal(err)
	}
	delete(before, "D")
	if after := section(t, spaceYML(s.Root()), "folders"); !reflect.DeepEqual(after, before) {
		t.Errorf("deleting D:\n%s", spaceYML(s.Root()))
	}
}

func TestDeletingAFolderThatIsAnAliasLeavesTheAnchor(t *testing.T) {
	s := open(t)
	for _, f := range []string{"C", "D", "E"} {
		mustCreate(t, s, f, meta("title", f), "x\n")
	}
	writeSpaceYML(t, s, sharedSettings)
	before := section(t, spaceYML(s.Root()), "folders")
	// E has its type from a merge key, D is an alias of C.
	for _, f := range []string{"D", "E"} {
		if _, err := s.DeleteFolder(Author{}, f, true); err != nil {
			t.Fatal(err)
		}
	}
	delete(before, "D")
	before["E"] = map[string]any{"note": "shared"} // what a person wrote there stays
	got := spaceYML(s.Root())
	if after := section(t, got, "folders"); !reflect.DeepEqual(after, before) {
		t.Errorf("after deleting D and E:\n%s", got)
	}
	if m := bindingMap(s); m["C"] != "other" || m["D"] != "" || m["E"] != "" {
		t.Errorf("bindings %v", m)
	}
	if d := meaningOf(t, got)["defaults"]; !reflect.DeepEqual(d, map[string]any{"type": "wenan", "note": "shared"}) {
		t.Errorf("the defaults were changed: %v", d)
	}
}

func TestMovingAFolderLeavesWhatItsAnchorsMeantToOthers(t *testing.T) {
	s := open(t)
	for _, f := range []string{"A", "B", "C", "D"} {
		mustCreate(t, s, f, meta("title", f), "x\n")
	}
	// A stale entry at the target carries anchors too.
	writeSpaceYML(t, s, sharedSettings+`  New:
    type: &stale old
    views: [{id: nnn111, name: stale, layout: list}]
  Z: {type: *stale}
`)
	before := section(t, spaceYML(s.Root()), "folders")
	if _, err := s.MoveFolder(Author{}, "A", "New"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MoveFolder(Author{}, "D", "Moved/D"); err != nil {
		t.Fatal(err)
	}
	before["New"], before["Moved/D"] = before["A"], before["D"]
	delete(before, "A")
	delete(before, "D")
	got := spaceYML(s.Root())
	if after := section(t, got, "folders"); !reflect.DeepEqual(after, before) {
		t.Errorf("the moves changed what entries mean:\n%s", got)
	}
	if m := bindingMap(s); m["New"] != "wenan" || m["B"] != "wenan" || m["Z"] != "old" || m["Moved/D"] != "other" {
		t.Errorf("bindings %v", m)
	}
	// Moving copies nothing: D is still an alias of C.
	if !strings.Contains(got, "Moved/D: *c") {
		t.Errorf("the alias was written out:\n%s", got)
	}
}

func TestViewEditsLeaveWhatAnchorsMeanToOthers(t *testing.T) {
	s := open(t)
	for _, f := range []string{"A", "B", "C", "D", "E"} {
		mustCreate(t, s, f, meta("title", f), "x\n")
	}
	writeSpaceYML(t, s, sharedSettings)
	before := section(t, spaceYML(s.Root()), "folders")
	// Change the view that carries the anchors, where a refusal with "fix the
	// file by hand" used to be.
	l, _ := s.Views(folderScope("A"))
	l.Views[0].Layout = "table"
	l.Views[0].Filter = nil
	l.Views[0].Hidden = []string{"other"}
	if _, err := s.PutViews(Author{}, folderScope("A"), l.Rev, l.Views); err != nil {
		t.Fatal(err)
	}
	after := section(t, spaceYML(s.Root()), "folders")
	for _, f := range []string{"B", "C", "D", "E"} {
		if !reflect.DeepEqual(after[f], before[f]) {
			t.Errorf("changing A's view changed %s:\n%s", f, spaceYML(s.Root()))
		}
	}
	if l, _ := s.Views(folderScope("A")); len(l.Views) != 1 || l.Views[0].Layout != "table" || len(l.Views[0].Filter) != 0 || !reflect.DeepEqual(l.Views[0].Hidden, []string{"other"}) {
		t.Errorf("A's view: %+v", l.Views)
	}
	// Remove all of A's views, then save a view of D, which is an alias of C.
	l, _ = s.Views(folderScope("A"))
	if _, err := s.PutViews(Author{}, folderScope("A"), l.Rev, nil); err != nil {
		t.Fatal(err)
	}
	l, _ = s.Views(folderScope("D"))
	l.Views[0].Name = "mine"
	if _, err := s.PutViews(Author{}, folderScope("D"), l.Rev, l.Views); err != nil {
		t.Fatal(err)
	}
	// And one of B that has its keys from a merge key.
	l, _ = s.Views(folderScope("B"))
	l.Views[1].Layout = "cards"
	if _, err := s.PutViews(Author{}, folderScope("B"), l.Rev, l.Views); err != nil {
		t.Fatal(err)
	}
	got := spaceYML(s.Root())
	after = section(t, got, "folders")
	if !reflect.DeepEqual(after["C"], before["C"]) || !reflect.DeepEqual(after["E"], before["E"]) {
		t.Errorf("C or E changed:\n%s", got)
	}
	if got := viewNames(t, s, folderScope("D")); got != "mine" {
		t.Errorf("D: %q", got)
	}
	if got := viewNames(t, s, folderScope("C")); got != "four" {
		t.Errorf("C: %q", got)
	}
	bl, _ := s.Views(folderScope("B"))
	if len(bl.Views) != 2 || bl.Views[0].Name != "two" || len(bl.Views[0].Filter) != 1 || bl.Views[1].Layout != "cards" || len(bl.Views[1].Filter) != 1 || bl.Views[1].Name != "three" {
		t.Errorf("B: %+v", bl.Views)
	}
	if m := bindingMap(s); m["A"] != "wenan" || m["B"] != "wenan" || m["D"] != "other" {
		t.Errorf("bindings %v", m)
	}
}

func TestUnbindingAndBindingLeaveWhatAnchorsMeanToOthers(t *testing.T) {
	s := open(t)
	for _, f := range []string{"A", "B", "C", "D", "E"} {
		mustCreate(t, s, f, meta("title", f), "x\n")
	}
	if _, err := putCurrent(s, "t2", TypeInput{}); err != nil {
		t.Fatal(err)
	}
	writeSpaceYML(t, s, sharedSettings)
	before := section(t, spaceYML(s.Root()), "folders")
	without := func(f string) map[string]any {
		e := map[string]any{}
		for k, v := range before[f].(map[string]any) {
			if k != "type" {
				e[k] = v
			}
		}
		return e
	}
	// Unbind the folder with the anchor, one that is an alias and one with a
	// merge key; bind one whose type is an alias.
	for _, f := range []string{"A", "D", "E"} {
		if _, err := s.BindFolder(Author{}, f, ""); err != nil {
			t.Fatalf("unbinding %s: %v", f, err)
		}
		before[f] = without(f)
	}
	if _, err := s.BindFolder(Author{}, "C", "t2"); err != nil {
		t.Fatal(err)
	}
	c := without("C")
	c["type"] = "t2"
	before["C"] = c
	got := spaceYML(s.Root())
	if after := section(t, got, "folders"); !reflect.DeepEqual(after, before) {
		t.Errorf("after binding and unbinding:\n%s\nwant %v\n got %v", got, before, after)
	}
	if m := bindingMap(s); !reflect.DeepEqual(m, map[string]string{"B": "wenan", "C": "t2"}) {
		t.Errorf("bindings %v", m)
	}
	if d := meaningOf(t, got)["defaults"]; !reflect.DeepEqual(d, map[string]any{"type": "wenan", "note": "shared"}) {
		t.Errorf("the defaults were changed: %v", d)
	}
}

func TestDeletingATypeUnbindsThroughAliasesAndKeepsTheRest(t *testing.T) {
	s := open(t)
	if _, err := putCurrent(s, "wenan", TypeInput{}); err != nil {
		t.Fatal(err)
	}
	writeSpaceYML(t, s, sharedSettings)
	if err := deleteCurrent(s, "wenan"); err != nil {
		t.Fatal(err)
	}
	if m := bindingMap(s); !reflect.DeepEqual(m, map[string]string{"C": "other", "D": "other"}) {
		t.Errorf("bindings %v", m)
	}
	for f, want := range map[string]string{"A": "one", "B": "two,three", "D": "four", "E": "five"} {
		if got := viewNames(t, s, folderScope(f)); got != want {
			t.Errorf("views of %s: %q, want %q", f, got, want)
		}
	}
	if l, _ := s.Views(folderScope("B")); len(l.Views) != 2 || len(l.Views[0].Filter) != 1 || l.Views[1].Layout != "board" {
		t.Errorf("B's views changed: %+v", l.Views)
	}
}

func TestTagColourWritesLeaveWhatAnchorsMeanToOthers(t *testing.T) {
	s := open(t)
	src := `tags:
  one:
    color: &c red
    views: &v [{id: ttt111, name: tv, layout: list}]
  two: {color: *c, views: *v}
  three: &e {color: green}
  four: *e
`
	check := func(what string, want map[string]string) {
		t.Helper()
		if got := s.TagColors(); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: colours %v, want %v\n%s", what, got, want, tagsYML(s.Root()))
		}
		for _, tag := range []string{"one", "two"} {
			if got := viewNames(t, s, tagScope(tag)); got != "tv" {
				t.Errorf("%s: the views of %s are %q", what, tag, got)
			}
		}
	}
	setTagsYML(t, s.Root(), src)
	if err := s.SetTagColor(Author{}, "one", "blue"); err != nil {
		t.Fatal(err)
	}
	check("recolouring the anchor", map[string]string{"one": "blue", "two": "red", "three": "green", "four": "green"})
	setTagsYML(t, s.Root(), src)
	if err := s.SetTagColor(Author{}, "one", ""); err != nil {
		t.Fatal(err)
	}
	check("taking the anchor's colour away", map[string]string{"two": "red", "three": "green", "four": "green"})
	if err := s.SetTagColor(Author{}, "four", "pink"); err != nil {
		t.Fatal(err)
	}
	check("recolouring an alias", map[string]string{"two": "red", "three": "green", "four": "pink"})
	if err := s.SetTagColor(Author{}, "three", ""); err != nil {
		t.Fatal(err)
	}
	check("uncolouring an anchor", map[string]string{"two": "red", "four": "pink"})
	// The views of a tag whose list is another tag's anchor.
	setTagsYML(t, s.Root(), src)
	l, _ := s.Views(tagScope("one"))
	if _, err := s.PutViews(Author{}, tagScope("one"), l.Rev, nil); err != nil {
		t.Fatal(err)
	}
	if got := viewNames(t, s, tagScope("one")) + "|" + viewNames(t, s, tagScope("two")); got != "|tv" {
		t.Errorf("after emptying the views of one: %q\n%s", got, tagsYML(s.Root()))
	}
	if got := s.TagColors(); got["one"] != "red" || got["two"] != "red" {
		t.Errorf("colours %v", got)
	}
}

func TestAnAnchorNameUsedTwiceDoesNotChangeWhatAnAliasMeans(t *testing.T) {
	s := open(t)
	for _, f := range []string{"A", "B", "C", "D"} {
		mustCreate(t, s, f, meta("title", f), "x\n")
	}
	// Moving &y to B and &e to D would leave D's `keep: *y` after the second
	// &y, which is another list: the file is then written without aliases.
	writeSpaceYML(t, s, `folders:
  A: &e {type: t, keep: &y [1]}
  B: {k: *y}
  C: {k: &y [2]}
  D: *e
`)
	before := section(t, spaceYML(s.Root()), "folders")
	if _, err := s.DeleteFolder(Author{}, "A", true); err != nil {
		t.Fatal(err)
	}
	before["A"] = map[string]any{"keep": []any{1}}
	got := spaceYML(s.Root())
	if after := section(t, got, "folders"); !reflect.DeepEqual(after, before) {
		t.Errorf("after deleting A:\n%s", got)
	}
	if strings.Contains(got, "*") {
		t.Errorf("an alias is left in a file whose anchor names cannot be trusted:\n%s", got)
	}
}

func TestAnEditThatWritesOverAnAnchorIsMadeAgainWithoutAliases(t *testing.T) {
	// What the check before a write is for: an edit that does not go by the
	// rule (it changes a node in place that an alias elsewhere refers to) still
	// does not change what the alias means.
	root := t.TempDir()
	setSpaceYML(t, root, "folders:\n  A: &e {type: t}\n  B: *e\n")
	data, changed, err := planSpaceYAML(root, "x", func(top *yaml.Node) (bool, error) {
		folders, _ := mapValue(top, "folders")
		a := folders.Content[1]
		a.Content[1].Value = "changed"
		return true, nil
	})
	if err != nil || !changed {
		t.Fatalf("%v %v", changed, err)
	}
	want := map[string]any{"A": map[string]any{"type": "changed"}, "B": map[string]any{"type": "t"}}
	if got := section(t, string(data), "folders"); !reflect.DeepEqual(got, want) {
		t.Errorf("the edit reached the alias:\n%s", data)
	}
}

// --- alias bombs on every write path -----------------------------------------

// nestedWithin is settings whose nested aliases stay within the budget; every
// folder and tag in it refers to them.
func nestedFolders() string {
	return aliasBomb("", "defs", 3) + `folders:
  A:
    type: wenan
    big: *l3
    views:
      - {id: aaa111, name: one, layout: list, big: *l3, also: &mine [*l1, *l1]}
  B:
    type: wenan
    big: *l3
    views:
      - {id: bbb111, name: two, layout: list, also: *mine}
  C: {type: wenan, big: [*l3, *l3]}
`
}

func TestWritesToAFileWithNestedAliasesCostNoMoreThanTheFile(t *testing.T) {
	needGit(t)
	for _, c := range []struct {
		name string
		do   func(t *testing.T, s *Space) error
	}{
		{"delete", func(t *testing.T, s *Space) error { _, err := s.DeleteFolder(Author{}, "A", true); return err }},
		{"move", func(t *testing.T, s *Space) error { _, err := s.MoveFolder(Author{}, "A", "Z"); return err }},
		{"unbind", func(t *testing.T, s *Space) error { _, err := s.BindFolder(Author{}, "A", ""); return err }},
		{"views", func(t *testing.T, s *Space) error {
			l, _ := s.Views(folderScope("A"))
			if len(l.Views) != 1 {
				t.Fatalf("%+v", l)
			}
			l.Views[0].Name = "renamed"
			_, err := s.PutViews(Author{}, folderScope("A"), l.Rev, l.Views)
			return err
		}},
		{"no views", func(t *testing.T, s *Space) error {
			l, _ := s.Views(folderScope("A"))
			_, err := s.PutViews(Author{}, folderScope("A"), l.Rev, nil)
			return err
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := open(t)
			for _, f := range []string{"A", "B", "C"} {
				mustCreate(t, s, f, meta("title", f), "x\n")
			}
			src := nestedFolders()
			writeSpaceYML(t, s, src)
			before := written(t, src, "folders")
			var err error
			bounded(t, c.name, func() { err = c.do(t, s) })
			if err != nil {
				t.Fatal(err)
			}
			got := spaceYML(s.Root())
			if len(got) > 2*len(src) {
				t.Errorf("the file grew from %d to %d bytes", len(src), len(got))
			}
			after := written(t, got, "folders")
			for _, f := range []string{"B", "C"} {
				if !reflect.DeepEqual(after[f], before[f]) {
					t.Errorf("%s changed what %s means", c.name, f)
				}
			}
		})
	}
	// Over the budget the file is not read, so a delete or a move leaves it as
	// it is, at once (it took seconds at depth 5 and all memory at depth 7).
	// The folders it has entries for are not moved or deleted, since their
	// settings could not follow; a folder it does not name is.
	for _, depth := range []int{5, 7} {
		s := open(t)
		for _, f := range []string{"A", "B", "C", "D"} {
			mustCreate(t, s, f, meta("title", f), "x\n")
		}
		src := "folders:\n  A:\n    type: wenan\n" + aliasBomb("    ", "views", depth) + "  B:\n    type: wenan\n    big: *l" + fmt.Sprint(depth) + "\n"
		writeSpaceYML(t, s, src)
		bounded(t, "a delete and a move over the budget", func() {
			_, err := s.DeleteFolder(Author{}, "A", true)
			wantInvalid(t, "the delete", err, "space.yml cannot be read")
			_, err = s.MoveFolder(Author{}, "B", "B2")
			wantInvalid(t, "the move", err, "space.yml cannot be read")
			if _, err := s.DeleteFolder(Author{}, "C", true); err != nil {
				t.Error(err)
			}
			if _, err := s.MoveFolder(Author{}, "D", "D2"); err != nil {
				t.Error(err)
			}
		})
		if !exists(s, "A") || !exists(s, "B") || exists(s, "B2") || exists(s, "C") || !exists(s, "D2") {
			t.Errorf("depth %d: the folders are not where they should be: %v", depth, folderPaths(t, s))
		}
		if spaceYML(s.Root()) != src {
			t.Errorf("depth %d: an unreadable file was written", depth)
		}
	}
}

func TestTagWritesToAFileWithNestedAliasesCostNoMoreThanTheFile(t *testing.T) {
	s := open(t)
	src := aliasBomb("", "defs", 3) + `tags:
  one: {color: &c red, big: *l3, views: &v [{id: ttt111, name: tv, layout: list, big: *l3}]}
  two: {color: *c, big: *l3, views: *v}
`
	for _, do := range []func() error{
		func() error { return s.SetTagColor(Author{}, "one", "blue") },
		func() error { return s.SetTagColor(Author{}, "one", "") },
		func() error {
			l, _ := s.Views(tagScope("one"))
			_, err := s.PutViews(Author{}, tagScope("one"), l.Rev, nil)
			return err
		},
	} {
		setTagsYML(t, s.Root(), src)
		before := written(t, src, "tags")
		var err error
		bounded(t, "a tag write", func() { err = do() })
		if err != nil {
			t.Fatal(err)
		}
		got := tagsYML(s.Root())
		if len(got) > 2*len(src) {
			t.Errorf("the file grew from %d to %d bytes", len(src), len(got))
		}
		if after := written(t, got, "tags"); !reflect.DeepEqual(after["two"], before["two"]) {
			t.Errorf("the write changed what the other tag means:\n%s", got)
		}
	}
}

// --- recovery ------------------------------------------------------------------

func TestRecoveryLeavesAFileOverTheBudgetAloneAndTheSpaceOpens(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	mustCreate(t, s, "A", meta("title", "x"), "x\n")
	mustCreate(t, s, "G", meta("title", "y"), "y\n")
	mustCreate(t, s, "Keep", meta("title", "z"), "z\n")
	s.Flush()
	crash(s)
	// A delete and a move were made on disk; space.yml was not edited, and it
	// is a file that used to take all memory to edit.
	src := "folders:\n  G:\n    type: wenan\n" + aliasBomb("    ", "views", 7) + "  A:\n    type: wenan\n    big: *l7\n  Keep: {type: wenan}\n"
	setSpaceYML(t, root, src)
	if err := os.RemoveAll(filepath.Join(root, "G")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "A"), filepath.Join(root, "A2")); err != nil {
		t.Fatal(err)
	}
	recs := `{"op":"u","p":"G"}` + "\n" + `{"op":"b","f":"A","to":"A2"}` + "\n"
	for round := 0; round < 2; round++ { // and again at the next start
		if err := os.WriteFile(journalFile(root), []byte(recs), 0o644); err != nil {
			t.Fatal(err)
		}
		var s2 *Space
		bounded(t, "opening the space", func() { s2 = openAt(t, root) })
		if spaceYML(root) != src {
			t.Fatalf("recovery wrote a file it cannot read")
		}
		// The space works: pages can be made and folders changed.
		p := mustCreate(t, s2, "Keep", meta("title", fmt.Sprint("after ", round)), "x\n")
		if _, err := s2.Get(p.ID); err != nil {
			t.Error(err)
		}
		crash(s2)
	}
	// Within the budget, recovery finishes both and keeps what others mean.
	src = aliasBomb("", "defs", 3) + "folders:\n  G: {type: &t wenan, big: *l3}\n  A: {type: *t, big: *l3}\n  Keep: {type: *t}\n"
	setSpaceYML(t, root, src)
	if err := os.WriteFile(journalFile(root), []byte(recs), 0o644); err != nil {
		t.Fatal(err)
	}
	var s3 *Space
	bounded(t, "recovery", func() { s3 = openAt(t, root) })
	if m := bindingMap(s3); !reflect.DeepEqual(m, map[string]string{"A2": "wenan", "Keep": "wenan"}) {
		t.Errorf("after recovery: %v\n%s", m, spaceYML(root))
	}
}

// --- a delete that cannot take its settings out ------------------------------

func TestADeleteWhoseSettingsCannotBeRemovedIsRefusedBeforeAnythingIsDeleted(t *testing.T) {
	s := open(t)
	p := mustCreate(t, s, "A", meta("title", "x"), "x\n")
	mustCreate(t, s, "B", meta("title", "y"), "y\n")
	// A and B share an entry with a long note of the person's own. Deleting A
	// leaves A the note (it is not the server's to remove), written out: the
	// file would be larger than the server reads.
	src := "shared: &e\n  type: wenan\n  note: " + strings.Repeat("x", maxSpaceFile*6/10) + "\nfolders:\n  A: *e\n  B: *e\n"
	writeSpaceYML(t, s, src)
	ids, err := s.DeleteFolder(Author{}, "A", true)
	wantInvalid(t, "the delete", err, "was not deleted")
	if err != nil && strings.Contains(err.Error(), "moved") {
		t.Errorf("the refusal of a delete speaks of a move: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("pages were deleted: %v", ids)
	}
	if _, err := s.Get(p.ID); err != nil {
		t.Errorf("the page is gone: %v", err)
	}
	if spaceYML(s.Root()) != src {
		t.Error("space.yml was changed")
	}
	// The same file refuses a move in the words of a move, before the rename.
	pad := "# " + strings.Repeat("x", maxSpaceFile-len(src)-8) + "\n"
	writeSpaceYML(t, s, pad+src)
	_, err = s.MoveFolder(Author{}, "A", "Parent/A-much-longer-name")
	wantInvalid(t, "the move", err, "was not moved")
	if _, err := os.Stat(filepath.Join(s.Root(), "A")); err != nil {
		t.Errorf("the folder is not where it was: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Root(), "Parent")); err == nil {
		t.Error("a folder made for the move was left behind")
	}
}

// --- properties ------------------------------------------------------------------

// randomSettings writes a space.yml with anchors, aliases and merge keys in
// places the server reads and in places it does not. Keys are written once.
func randomSettings(r *rand.Rand) (text string, folders []string) {
	var b strings.Builder
	var defs []string // the anchors of mappings usable as an entry or a merge
	types := []string{"t1", "t2", "t3"}
	scalarAnchors := 0
	b.WriteString("name: x\ndefs:\n")
	for i := 0; i < 1+r.Intn(3); i++ {
		name := fmt.Sprintf("d%d", i)
		if r.Intn(5) == 0 && i > 0 {
			name = "d0" // an anchor name used twice
		}
		defs = append(defs, name)
		fmt.Fprintf(&b, "  k%d: &%s {type: %s, note: n%d", i, name, types[r.Intn(3)], i)
		if r.Intn(2) == 0 {
			fmt.Fprintf(&b, ", views: [{id: def%03d, name: dv, layout: list}]", i)
		}
		b.WriteString("}\n")
	}
	b.WriteString("folders:\n")
	all := []string{"A", "A/x", "A/x/y", "B", "B/z", "C", "D", "E", "New", "New/x"}
	r.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })
	entryAnchors := 0
	for _, f := range all[:3+r.Intn(len(all)-3)] {
		folders = append(folders, f)
		switch k := r.Intn(10); {
		case k == 0:
			fmt.Fprintf(&b, "  %s: *%s\n", f, defs[r.Intn(len(defs))])
		case k == 1 && entryAnchors > 0:
			fmt.Fprintf(&b, "  %s: *e%d\n", f, r.Intn(entryAnchors))
		case k == 2:
			fmt.Fprintf(&b, "  %s: 7\n", f)
		default:
			fmt.Fprintf(&b, "  %s:", f)
			if r.Intn(3) == 0 {
				fmt.Fprintf(&b, " &e%d", entryAnchors)
				entryAnchors++
			}
			b.WriteString("\n")
			wrote := false
			if r.Intn(4) == 0 {
				fmt.Fprintf(&b, "    <<: *%s\n", defs[r.Intn(len(defs))])
				wrote = true
			}
			switch r.Intn(4) {
			case 0:
				fmt.Fprintf(&b, "    type: &s%d %s\n", scalarAnchors, types[r.Intn(3)])
				scalarAnchors++
				wrote = true
			case 1:
				if scalarAnchors > 0 {
					fmt.Fprintf(&b, "    type: *s%d\n", r.Intn(scalarAnchors))
					wrote = true
				}
			case 2:
				fmt.Fprintf(&b, "    type: %s\n", types[r.Intn(3)])
				wrote = true
			}
			if r.Intn(2) == 0 {
				fmt.Fprintf(&b, "    views:\n      - {id: v%05d, name: n, layout: list, hidden: &s%d [a, b]}\n", r.Intn(99999), scalarAnchors)
				scalarAnchors++
				if r.Intn(2) == 0 {
					fmt.Fprintf(&b, "      - {id: w%05d, name: m, layout: list, hidden: *s%d}\n", r.Intn(99999), r.Intn(scalarAnchors))
				}
				wrote = true
			}
			if r.Intn(3) == 0 && scalarAnchors > 0 {
				fmt.Fprintf(&b, "    mine: [*s%d, &s%d own]\n", r.Intn(scalarAnchors), scalarAnchors)
				scalarAnchors++
				wrote = true
			}
			if !wrote {
				b.WriteString("    other: 1\n")
			}
		}
	}
	return b.String(), folders
}

// TestWritesChangeOnlyWhatTheyTarget makes random settings with anchors,
// aliases and merge keys, applies each kind of write to them, and compares
// what every folder's entry means afterwards (as the YAML library reads the
// file) with what the write was to do.
func TestWritesChangeOnlyWhatTheyTarget(t *testing.T) {
	r := rand.New(rand.NewSource(20261122))
	under := func(f, of string) bool { return f == of || strings.HasPrefix(f, of+"/") }
	strip := func(e any) (any, bool) { // an entry without what the server keeps there; false if nothing is left
		m, ok := e.(map[string]any)
		if !ok {
			return e, true
		}
		out := map[string]any{}
		for k, v := range m {
			if k != "type" && k != "views" {
				out[k] = v
			}
		}
		return out, len(out) > 0
	}
	writes, fallbacks := 0, 0
	for i := 0; i < 600; i++ {
		text, folders := randomSettings(r)
		root := t.TempDir()
		setSpaceYML(t, root, text)
		if _, err := parseYAMLDoc([]byte(text), errSpaceFile); err != nil {
			t.Fatalf("the generator wrote something unreadable:\n%s", text)
		}
		before := section(t, text, "folders")
		want := map[string]any{}
		for k, v := range before {
			want[k] = v
		}
		target := folders[r.Intn(len(folders))]
		var edit func(top *yaml.Node) (bool, error)
		var what string
		switch r.Intn(4) {
		case 0:
			what = "forget " + target
			edit = func(top *yaml.Node) (bool, error) {
				return forgetFolders(top, func(f string) bool { return under(f, target) })
			}
			for f, e := range before {
				if under(f, target) {
					if rest, some := strip(e); some {
						want[f] = rest
					} else {
						delete(want, f)
					}
				}
			}
		case 1:
			to := []string{"New", "Other/place", "E"}[r.Intn(3)]
			if under(to, target) || under(target, to) {
				continue
			}
			what = "move " + target + " to " + to
			edit = func(top *yaml.Node) (bool, error) { return rebind(top, target, to, false) }
			for f := range before {
				if under(f, to) {
					delete(want, f)
				}
			}
			for f, e := range before {
				if under(f, target) {
					delete(want, f)
					want[to+f[len(target):]] = e
				}
			}
		case 2:
			typ := []string{"", "t9"}[r.Intn(2)]
			what = "bind " + target + " to " + typ
			edit = func(top *yaml.Node) (bool, error) { return setBinding(top, target, typ) }
			switch m, isMap := before[target].(map[string]any); {
			case typ == "" && isMap:
				rest := map[string]any{}
				for k, v := range m {
					if k != "type" {
						rest[k] = v
					}
				}
				if len(rest) > 0 {
					want[target] = rest
				} else {
					delete(want, target)
				}
			case typ != "" && isMap:
				bound := map[string]any{"type": typ}
				for k, v := range m {
					if k != "type" {
						bound[k] = v
					}
				}
				want[target] = bound
			case typ != "" && before[target] == nil:
				want[target] = map[string]any{"type": typ}
			default:
				continue // an entry that is not settings is the person's to fix
			}
		case 3:
			typ := "t" + fmt.Sprint(1+r.Intn(3))
			what = "unbind every " + typ
			edit = func(top *yaml.Node) (bool, error) {
				return unbindWhere(top, func(_, t string) bool { return t == typ })
			}
			for f, e := range before {
				m, isMap := e.(map[string]any)
				if !isMap || m["type"] != typ {
					continue
				}
				rest := map[string]any{}
				for k, v := range m {
					if k != "type" {
						rest[k] = v
					}
				}
				if len(rest) > 0 {
					want[f] = rest
				} else {
					delete(want, f)
				}
			}
		}
		data, changed, err := planSpaceYAML(root, "x", edit)
		if err != nil {
			t.Fatalf("%s: %v\n%s", what, err, text)
		}
		if !changed {
			if !reflect.DeepEqual(want, before) {
				t.Fatalf("%s: nothing was written, but something was to change\n%s", what, text)
			}
			continue
		}
		writes++
		if !strings.Contains(string(data), "&") && strings.Contains(text, "*") {
			fallbacks++
		}
		after := section(t, string(data), "folders")
		if after == nil {
			after = map[string]any{}
		}
		if !reflect.DeepEqual(after, want) {
			keys := func(m map[string]any) []string {
				var ks []string
				for k := range m {
					ks = append(ks, k)
				}
				sort.Strings(ks)
				return ks
			}
			t.Fatalf("%s: the entries are\n  %v %v\nwant\n  %v %v\nfrom:\n%s\nto:\n%s", what, keys(after), after, keys(want), want, text, data)
		}
		if d1, d2 := meaningOf(t, text)["defs"], meaningOf(t, string(data))["defs"]; !reflect.DeepEqual(d1, d2) {
			t.Fatalf("%s changed what is outside the folders:\n%s\nto:\n%s", what, text, data)
		}
		// What the server reads of the result is what any reader does.
		doc, err := parseYAMLDoc(data, errSpaceFile)
		if err != nil {
			t.Fatalf("%s: the result is not readable:\n%s", what, data)
		}
		for _, b := range bindingsOf(doc) {
			if m, _ := after[b.Folder].(map[string]any); m["type"] != b.Type {
				t.Fatalf("%s: the server reads the type %q for %s, the file says %v\n%s", what, b.Type, b.Folder, m["type"], data)
			}
		}
	}
	if writes < 200 {
		t.Errorf("only %d of the cases wrote anything", writes)
	}
	t.Logf("%d writes, %d of them with every alias written out", writes, fallbacks)
}

// TestTheServerReadsSettingsAsTheLibraryDoes compares the bindings and the
// views the server reads from random settings with what the YAML library makes
// of the same text.
func TestTheServerReadsSettingsAsTheLibraryDoes(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for i := 0; i < 400; i++ {
		text, _ := randomSettings(r)
		doc, err := parseYAMLDoc([]byte(text), errSpaceFile)
		if err != nil {
			t.Fatalf("unreadable:\n%s", text)
		}
		lib := section(t, text, "folders")
		want := map[string]string{}
		wantViews := map[string][]string{}
		for f, e := range lib {
			m, _ := e.(map[string]any)
			if typ, ok := m["type"].(string); ok {
				want[f] = typ
			}
			vs, _ := m["views"].([]any)
			for _, v := range vs {
				vm := v.(map[string]any)
				hidden := []string{}
				hs, _ := vm["hidden"].([]any)
				for _, h := range hs {
					hidden = append(hidden, h.(string))
				}
				wantViews[f] = append(wantViews[f], fmt.Sprint(vm["id"], hidden))
			}
		}
		got := map[string]string{}
		for _, b := range bindingsOf(doc) {
			got[b.Folder] = b.Type
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("bindings %v, the library reads %v\n%s", got, want, text)
		}
		gotViews := map[string][]string{}
		for f, vs := range viewsOf(doc, "folders", bindingKey) {
			for _, v := range vs {
				gotViews[f] = append(gotViews[f], fmt.Sprint(v.ID, append([]string{}, v.Hidden...)))
			}
		}
		if !reflect.DeepEqual(gotViews, wantViews) {
			t.Fatalf("views %v, the library reads %v\n%s", gotViews, wantViews, text)
		}
	}
}
