package space

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KKazuhaK/PetalBook/internal/index"
)

func TestAViewWriteThatWouldLeaveADanglingAliasIsRefused(t *testing.T) {
	s := open(t)
	mustCreate(t, s, "F", meta("title", "x"), "x\n")
	src := `folders:
  F:
    type: wenan
    views:
      - &base {id: aaaaaa, name: one, layout: list}
      - {<<: *base, id: bbbbbb, name: two}
`
	writeSpaceYML(t, s, src)
	l, err := s.Views(folderScope("F"))
	if err != nil || len(l.Views) != 2 {
		t.Fatalf("%v %+v", err, l)
	}
	// Delete the view that carries the anchor.
	_, err = s.PutViews(Author{}, folderScope("F"), l.Rev, l.Views[1:])
	var inv *InvalidError
	if !errors.As(err, &inv) {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if got := spaceYML(s.Root()); got != src {
		t.Errorf("the file was changed:\n%s", got)
	}
	if len(s.Bindings()) != 1 {
		t.Error("the bindings were lost")
	}
	if l2, _ := s.Views(folderScope("F")); len(l2.Views) != 2 {
		t.Errorf("the views were lost: %+v", l2)
	}
	// The same for a tag's colour and views.
	setTagsYML(t, s.Root(), `tags:
  one:
    color: &c red
    views:
      - &v {id: aaaaaa, name: one, layout: list}
  two:
    color: *c
    views: [{<<: *v, id: bbbbbb, name: two}]
`)
	tl, _ := s.Views(tagScope("one"))
	if len(tl.Views) != 1 {
		t.Fatalf("%+v", tl)
	}
	before := tagsYML(s.Root())
	if _, err := s.PutViews(Author{}, tagScope("one"), tl.Rev, nil); !errors.As(err, &inv) {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if tagsYML(s.Root()) != before {
		t.Error("tags.yml was changed")
	}
	if s.TagColors()["one"] != "red" {
		t.Errorf("colours lost: %v", s.TagColors())
	}
}

func TestSavingOneViewLeavesTheOthersAsTheyAre(t *testing.T) {
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
