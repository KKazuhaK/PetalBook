package space

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/KKazuhaK/PetalBook/internal/index"
)

func TestRemovingAViewThatCarriesAnAnchorKeepsWhatItsAliasesMean(t *testing.T) {
	t.Parallel()
	s := open(t)
	mustCreate(t, s, "F", meta("title", "x"), "x\n")
	src := `folders:
  F:
    type: wenan
    views:
      - &base {id: aaaaaa, name: one, layout: board}
      - {<<: *base, id: bbbbbb, name: two}
`
	writeSpaceYML(t, s, src)
	l, err := s.Views(folderScope("F"))
	if err != nil || len(l.Views) != 2 || l.Views[1].Layout != "board" {
		t.Fatalf("%v %+v", err, l)
	}
	// Delete the view that carries the anchor: the anchor moves to its alias.
	if _, err = s.PutViews(Author{}, folderScope("F"), l.Rev, l.Views[1:]); err != nil {
		t.Fatal(err)
	}
	got := spaceYML(s.Root())
	if !strings.Contains(got, "<<: &base") {
		t.Errorf("the anchor did not move to its alias:\n%s", got)
	}
	if len(s.Bindings()) != 1 {
		t.Error("the bindings were lost")
	}
	if l2, _ := s.Views(folderScope("F")); len(l2.Views) != 1 || l2.Views[0].ID != "bbbbbb" || l2.Views[0].Name != "two" || l2.Views[0].Layout != "board" {
		t.Errorf("the view that is left changed: %+v", l2)
	}
	// The same for a tag's colour and views.
	setTagsYML(t, s.Root(), `tags:
  one:
    color: &c red
    views:
      - &v {id: aaaaaa, name: one, layout: board}
  two:
    color: *c
    views: [{<<: *v, id: bbbbbb, name: two}]
`)
	tl, _ := s.Views(tagScope("one"))
	if len(tl.Views) != 1 {
		t.Fatalf("%+v", tl)
	}
	if _, err := s.PutViews(Author{}, tagScope("one"), tl.Rev, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTagColor(Author{}, "one", ""); err != nil {
		t.Fatal(err)
	}
	if c := s.TagColors(); c["one"] != "" || c["two"] != "red" {
		t.Errorf("colours: %v\n%s", c, tagsYML(s.Root()))
	}
	if l2, _ := s.Views(tagScope("two")); len(l2.Views) != 1 || l2.Views[0].Name != "two" || l2.Views[0].Layout != "board" {
		t.Errorf("the other tag's view changed: %+v\n%s", l2, tagsYML(s.Root()))
	}
	if strings.Contains(tagsYML(s.Root()), "one:") {
		t.Errorf("the tag with nothing left is still in the file:\n%s", tagsYML(s.Root()))
	}
}

func TestSavingOneViewLeavesTheOthersAsTheyAre(t *testing.T) {
	t.Parallel()
	s := open(t)
	mustCreate(t, s, "F", meta("title", "x"), "x\n")
	writeSpaceYML(t, s, `defs:
  f: &f [{key: series, op: in, values: [a]}]
  h: &h [series]
folders:
  F:
    views:
      - {id: aaaaaa, name: one, layout: list}
      - {id: bbbbbb, name: two, layout: list, filter: *f, hidden: *h, sort: {x: 1}, group: [g]}
      - {id: cccccc, name: three, layout: list, hidden: series}
`)
	l, _ := s.Views(folderScope("F"))
	l.Views[0].Filter = []ViewFilter{{Key: "series", Op: "in", Values: []any{"b"}}}
	l.Views[0].Sort = "title"
	if _, err := s.PutViews(Author{}, folderScope("F"), l.Rev, l.Views); err != nil {
		t.Fatal(err)
	}
	got := spaceYML(s.Root())
	for _, want := range []string{"filter: *f", "hidden: *h", "sort: {x: 1}", "group: [g]", "hidden: series"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q is gone from:\n%s", want, got)
		}
	}
	// A view that is changed keeps what it did not change that cannot be read.
	l, _ = s.Views(folderScope("F"))
	l.Views[1].Name = "TWO"
	if _, err := s.PutViews(Author{}, folderScope("F"), l.Rev, l.Views); err != nil {
		t.Fatal(err)
	}
	got = spaceYML(s.Root())
	for _, want := range []string{"filter: *f", "hidden: *h", "name: TWO"} {
		if !strings.Contains(got, want) {
			t.Errorf("after a rename, %q is gone from:\n%s", want, got)
		}
	}
}

func TestOptionsWrittenTwiceInATypeAreListedOnce(t *testing.T) {
	t.Parallel()
	s := open(t)
	writeType(t, s.Root(), "task", `{"title":"task","type":"object","properties":{"status":{"type":"string","enum":["a","b","a"]}}}`)
	for _, st := range []string{"a", "b", "a", "z"} {
		mustCreate(t, s, "Work", meta("title", st, "status", st), "x\n")
	}
	if _, err := s.BindFolder(Author{}, "Work", "task"); err != nil {
		t.Fatal(err)
	}
	f := "Work"
	for _, sort := range []string{"prop:status", "-prop:status"} {
		res, err := s.List(index.Query{Folder: &f, Sort: sort, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Items) != 4 {
			t.Errorf("%s: %d items, want 4", sort, len(res.Items))
		}
	}
	info, ok := s.typeInfoCached("task")
	if !ok {
		t.Fatal("no type")
	}
	for _, fl := range info.Fields {
		if fl.Key == "status" && len(fl.Options) != 2 {
			t.Errorf("options %v", fl.Options)
		}
	}
}

func TestAMoveThatFillsTheSettingsFileIsRefusedAndUndone(t *testing.T) {
	t.Parallel()
	s := open(t)
	mustCreate(t, s, "A", meta("title", "x"), "x\n")
	if _, err := putCurrent(s, "t", TypeInput{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindFolder(Author{}, "A", "t"); err != nil {
		t.Fatal(err)
	}
	// Pad the file with a comment to a few bytes below the limit.
	file := filepath.Join(s.Root(), ".petalbook", "space.yml")
	b, _ := os.ReadFile(file)
	pad := maxSpaceFile - len(b) - 4
	if err := os.WriteFile(file, append([]byte("# "+strings.Repeat("x", pad-3)+"\n"), b...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MoveFolder(Author{}, "A", "A-much-longer-name"); err == nil {
		t.Fatal("the move succeeded and left the type under the old key")
	}
	if _, err := os.Stat(filepath.Join(s.Root(), "A")); err != nil {
		t.Errorf("the folder was moved anyway: %v", err)
	}
	if bs := s.Bindings(); len(bs) != 1 || bs[0].Folder != "A" {
		t.Errorf("bindings: %v", bs)
	}
}

const crossAnchors = `folders:
  A:
    type: wenan
    views:
      - {id: aaa111, name: one, layout: list, filter: &f [{key: series, op: in, values: [a]}]}
  B:
    views:
      - {id: bbb111, name: two, layout: list, filter: *f}
`

func TestDeletingAFolderWhoseAnchorAnotherFolderUsesForgetsItsViews(t *testing.T) {
	t.Parallel()
	s := open(t)
	mustCreate(t, s, "A", meta("title", "x"), "x\n")
	mustCreate(t, s, "B", meta("title", "y"), "y\n")
	writeSpaceYML(t, s, crossAnchors)
	if _, err := s.DeleteFolder(Author{}, "A", true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(spaceYML(s.Root()), "aaa111") {
		t.Errorf("the deleted folder's views stay in space.yml:\n%s", spaceYML(s.Root()))
	}
	bl, err := s.Views(folderScope("B"))
	if err != nil || len(bl.Views) != 1 || len(bl.Views[0].Filter) != 1 {
		t.Fatalf("B lost its filter: %v %+v", err, bl)
	}
	if err := os.MkdirAll(filepath.Join(s.Root(), "A"), 0o755); err != nil {
		t.Fatal(err)
	}
	if nl, _ := s.Views(folderScope("A")); len(nl.Views) != 0 {
		t.Errorf("a new folder A inherits %+v", nl.Views)
	}
}

func TestMovingAFolderWhoseAnchorAnotherFolderUsesKeepsBothViews(t *testing.T) {
	t.Parallel()
	s := open(t)
	mustCreate(t, s, "A", meta("title", "x"), "x\n")
	mustCreate(t, s, "B", meta("title", "y"), "y\n")
	writeSpaceYML(t, s, crossAnchors)
	// A stale entry at the target holds the anchor B's view refers to.
	writeSpaceYML(t, s, `folders:
  A:
    views:
      - {id: aaa111, name: one, layout: list}
  C:
    views:
      - {id: ccc111, name: old, layout: list, filter: &f [{key: series, op: in, values: [a]}]}
  B:
    views:
      - {id: bbb111, name: two, layout: list, filter: *f}
`)
	if _, err := s.MoveFolder(Author{}, "A", "C"); err != nil {
		t.Fatal(err)
	}
	got := spaceYML(s.Root())
	if strings.Contains(got, "ccc111") || !strings.Contains(got, "aaa111") {
		t.Errorf("the bindings did not follow the folder:\n%s", got)
	}
	if bl, err := s.Views(folderScope("B")); err != nil || len(bl.Views) != 1 || len(bl.Views[0].Filter) != 1 {
		t.Fatalf("B lost its filter: %v %+v", err, bl)
	}
}

func TestRecoveryOfAMoveWhoseSettingsDoNotFitMovesTheFolderBack(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	if _, err := putCurrent(s, "t", TypeInput{}); err != nil {
		t.Fatal(err)
	}
	mustCreate(t, s, "A", meta("title", "x"), "x\n")
	if _, err := s.BindFolder(Author{}, "A", "t"); err != nil {
		t.Fatal(err)
	}
	s.Flush()
	crash(s)
	// Pad space.yml to a few bytes below the limit: the longer key does not fit.
	file := filepath.Join(root, ".petalbook", "space.yml")
	b, _ := os.ReadFile(file)
	pad := maxSpaceFile - len(b) - 4
	if err := os.WriteFile(file, append([]byte("# "+strings.Repeat("x", pad-3)+"\n"), b...), 0o644); err != nil {
		t.Fatal(err)
	}
	// The rename was made; the edit of space.yml never was.
	if err := os.Rename(filepath.Join(root, "A"), filepath.Join(root, "A-much-longer-name")); err != nil {
		t.Fatal(err)
	}
	rec := `{"op":"b","f":"A","to":"A-much-longer-name"}` + "\n"
	if err := os.WriteFile(journalFile(root), []byte(rec), 0o644); err != nil {
		t.Fatal(err)
	}
	s2 := openAt(t, root)
	if _, err := os.Stat(filepath.Join(root, "A")); err != nil {
		t.Errorf("the folder was left at the name its settings are not under: %v", err)
	}
	if m := bindingMap(s2); !reflect.DeepEqual(m, map[string]string{"A": "t"}) {
		t.Errorf("bindings: %v", m)
	}
}
