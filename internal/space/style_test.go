package space

import (
	"reflect"
	"strings"
	"testing"

	"github.com/KKazuhaK/PetalBook/internal/index"
)

func TestFolderStyleFollowsTheFolderInTheSameCommit(t *testing.T) {
	s := open(t)
	mustCreate(t, s, "随笔/2026", meta("title", "A"), "x\n")
	if _, err := s.SetFolderStyle(Author{}, "随笔/2026", "book-open", "teal"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetFolderStyle(Author{}, "随笔", "🌸", ""); err != nil {
		t.Fatal(err)
	}
	want := []FolderStyle{{"随笔", "🌸", ""}, {"随笔/2026", "book-open", "teal"}}
	if got := s.Styles(); !reflect.DeepEqual(got, want) {
		t.Fatalf("styles = %+v", got)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	before := git(t, s.Root(), "rev-list", "--count", "HEAD")
	if _, err := s.MoveFolder(Author{}, "随笔", "归档/旧"); err != nil {
		t.Fatal(err)
	}
	want = []FolderStyle{{"归档/旧", "🌸", ""}, {"归档/旧/2026", "book-open", "teal"}}
	if got := s.Styles(); !reflect.DeepEqual(got, want) {
		t.Fatalf("after the move: %+v", got)
	}
	yml := readDisk(t, s, ".petalbook/space.yml")
	if strings.Contains(yml, "随笔") {
		t.Fatalf("the old keys are left:\n%s", yml)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	// The move and the settings are one commit: space.yml is not left behind.
	if after := git(t, s.Root(), "rev-list", "--count", "HEAD"); after == before {
		t.Fatal("nothing was committed")
	} else if n := git(t, s.Root(), "diff", "--name-only", "HEAD~1", "HEAD"); !strings.Contains(n, "space.yml") {
		t.Fatalf("the settings are not in the commit of the move:\n%s", n)
	}
	if st := git(t, s.Root(), "status", "--porcelain"); st != "" {
		t.Fatalf("not committed together: %s", st)
	}
}

func TestFolderStyleIsRemovedWithTheFolderAndChecked(t *testing.T) {
	s := open(t)
	mustCreate(t, s, "A", meta("title", "A"), "x\n")
	if _, err := s.SetFolderStyle(Author{}, "A", "star", "red"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetFolderStyle(Author{}, "A", "", ""); err != nil {
		t.Fatal(err)
	}
	if len(s.Styles()) != 0 {
		t.Fatalf("%+v", s.Styles())
	}
	if _, err := s.SetFolderStyle(Author{}, "A", "star", "mauve"); err == nil {
		t.Fatal("a colour that is not one of the eight")
	}
	if _, err := s.SetFolderStyle(Author{}, "Nope", "star", ""); err == nil {
		t.Fatal("no such folder")
	}
}

func TestPageIconAndColorAreKeptInTheHeader(t *testing.T) {
	s := open(t)
	p := mustCreate(t, s, "", meta("title", "A", "icon", "flower", "color", "pink"), "x\n")
	if p.Meta.Icon() != "flower" || p.Meta.Color() != "pink" {
		t.Fatalf("%+v", p.Meta)
	}
	if !strings.Contains(p.Header, "icon: flower") {
		t.Fatalf("header:\n%s", p.Header)
	}
	// A listing shows them (the sidebar, lists and tables draw the icon).
	r, err := s.List(index.Query{})
	if err != nil || len(r.Items) != 1 || r.Items[0].Icon != "flower" || r.Items[0].Color != "pink" {
		t.Fatalf("%+v %v", r, err)
	}
	// They are fields of every page, not properties a type may declare.
	if err := checkPropKey("icon"); err == nil {
		t.Fatal("icon is not a property")
	}
}

func TestSourcePresetsAreKeptInSpaceYml(t *testing.T) {
	s := open(t)
	if _, set := s.SourcePresets(); set {
		t.Fatal("a new space chooses none")
	}
	if err := s.SetSourcePresets(Author{}, []string{" 书 ", "电影", "书", ""}); err != nil {
		t.Fatal(err)
	}
	got, set := s.SourcePresets()
	if !set || !reflect.DeepEqual(got, []string{"书", "电影"}) {
		t.Fatalf("%v %v", got, set)
	}
	if err := s.SetSourcePresets(Author{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, set := s.SourcePresets(); set {
		t.Fatal("taking the choice away restores the defaults")
	}
}

func TestSourceFilterAndUsedSources(t *testing.T) {
	s := open(t)
	mustCreate(t, s, "", meta("title", "A", "source", []any{"书", "电影"}), "x\n")
	mustCreate(t, s, "", meta("title", "B", "source", "书"), "x\n") // written by hand as one text
	mustCreate(t, s, "", meta("title", "C"), "x\n")
	r, err := s.List(index.Query{Filters: []index.Filter{{Key: "source", Op: "in", Values: []any{"电影"}}}})
	if err != nil || len(r.Items) != 1 || r.Items[0].Title != "A" || !reflect.DeepEqual(r.Items[0].Sources, []string{"书", "电影"}) {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = s.List(index.Query{Filters: []index.Filter{{Key: "source", Op: "in", Values: []any{"书"}}}})
	if err != nil || len(r.Items) != 2 {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = s.List(index.Query{Sort: "prop:source"})
	if err != nil || len(r.Items) != 3 || r.Items[2].Title != "C" {
		t.Fatalf("sorted by source: %+v %v", r, err)
	}
	used, err := s.UsedSources()
	if err != nil || len(used) != 2 || used[0].Source != "书" || used[0].Pages != 2 {
		t.Fatalf("%+v %v", used, err)
	}
}

func TestRenameTypeFieldRewritesTheTypeAndItsPagesInOneCommit(t *testing.T) {
	s := open(t)
	writeType(t, s.Root(), "task", `{"title":"Task","properties":{"status":{"type":"string"},"due":{"type":"string"}}}`)
	mustCreate(t, s, "Work", meta("title", "A", "type", "task", "status", "open", "due", "2026-11-30"), "x\n")
	mustCreate(t, s, "Work", meta("title", "B", "type", "task", "due", "2026-12-01"), "x\n")
	mustCreate(t, s, "Work", meta("title", "Other", "status", "kept"), "x\n") // another type: left alone
	var rev string
	ts, _, err := s.Types()
	if err != nil {
		t.Fatal(err)
	}
	for _, ti := range ts {
		if ti.Name == "task" {
			rev = ti.Rev
		}
	}
	ti, n, err := s.RenameTypeField(Author{}, "task", rev, "status", "state")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("pages rewritten = %d", n)
	}
	if got := readType(t, s.Root(), "task"); !strings.Contains(got, `"state"`) || strings.Contains(got, `"status"`) {
		t.Fatalf("type file:\n%s", got)
	}
	_ = ti
	r, err := s.List(index.Query{Filters: []index.Filter{{Key: "state", Op: "notEmpty"}}})
	if err != nil || len(r.Items) != 1 || r.Items[0].Title != "A" {
		t.Fatalf("%+v %v", r, err)
	}
	r, _ = s.List(index.Query{Filters: []index.Filter{{Key: "status", Op: "notEmpty"}}})
	if len(r.Items) != 1 || r.Items[0].Title != "Other" {
		t.Fatalf("a page of another type keeps its key: %+v", r)
	}
	// A key that is there already, a key that is not a field, an old rev.
	if _, _, err := s.RenameTypeField(Author{}, "task", ti.Rev, "state", "due"); err == nil {
		t.Fatal("a key the type has already")
	}
	if _, _, err := s.RenameTypeField(Author{}, "task", ti.Rev, "nothing", "x"); err == nil {
		t.Fatal("not a field")
	}
	if _, _, err := s.RenameTypeField(Author{}, "task", rev, "state", "x"); err == nil {
		t.Fatal("a rev that is out of date")
	}
	if _, _, err := s.RenameTypeField(Author{}, "task", ti.Rev, "state", "id"); err == nil {
		t.Fatal("a field of every page")
	}
}

func TestATypeGivesItsPagesADefaultIcon(t *testing.T) {
	s := open(t)
	icon, color := "book-open", "teal"
	ti, err := s.PutType(Author{}, "poem", "", TypeInput{Title: "Poem", Icon: &icon, Color: &color})
	if err != nil || ti.Icon != "book-open" || ti.Color != "teal" {
		t.Fatalf("%+v %v", ti, err)
	}
	// A save that says nothing about the icon keeps it; "" takes it away; a bad colour is refused.
	ti, err = s.PutType(Author{}, "poem", ti.Rev, TypeInput{Title: "Poem 2"})
	if err != nil || ti.Icon != "book-open" {
		t.Fatalf("%+v %v", ti, err)
	}
	none := ""
	if ti, err = s.PutType(Author{}, "poem", ti.Rev, TypeInput{Title: "Poem 2", Icon: &none}); err != nil || ti.Icon != "" || ti.Color != "teal" {
		t.Fatalf("%+v %v", ti, err)
	}
	mauve := "mauve"
	if _, err = s.PutType(Author{}, "poem", ti.Rev, TypeInput{Color: &mauve}); err == nil {
		t.Fatal("a colour that is none of the eight")
	}
}
