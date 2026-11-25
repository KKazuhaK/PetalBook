package space

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"petalbook.app/server/internal/page"
)

// renameScene is a space with a type task (status, due), a folder Work bound
// to it with a board grouped by status, a folder Other with a view of its own
// on status, and pages A (status), B (status and state), C (a header that
// cannot be read) in Work and D (status, no type) in Other.
func renameScene(t *testing.T, root string) (s *Space, rev string, pages map[string]*Page) {
	t.Helper()
	s = openAt(t, root)
	writeType(t, root, "task", `{"title":"Task","properties":{"status":{"type":"string"},"due":{"type":"string"}}}`)
	pages = map[string]*Page{
		"A": mustCreate(t, s, "Work", meta("title", "A", "status", "open"), "x\n"),
		"B": mustCreate(t, s, "Work", meta("title", "B", "status", "open", "state", "kept"), "x\n"),
		"D": mustCreate(t, s, "Other", meta("title", "D", "status", "open"), "x\n"),
	}
	if err := os.WriteFile(filepath.Join(root, "Work", "C.md"), []byte("---\nid: [\nstatus: open\n---\nx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	setSpaceYML(t, root, `folders:
  Work:
    type: task
    views:
      - id: aaaaaa
        name: Board
        layout: board
        group: status
        sort: -prop:status
        filter: [{key: status, op: in, values: [open]}, {key: due, op: notEmpty}]
        hidden: [status, due]
        cardFields: [status, due]
  Other:
    views:
      - id: bbbbbb
        name: Mine
        layout: board
        group: status
        sort: prop:status
`)
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	ts, _, err := s.Types()
	if err != nil {
		t.Fatal(err)
	}
	for _, ti := range ts {
		if ti.Name == "task" {
			rev = ti.Rev
		}
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	return s, rev, pages
}

func folderViews(t *testing.T, s *Space, folder string) []View {
	t.Helper()
	l, err := s.Views(ViewScope{Folder: &folder})
	if err != nil {
		t.Fatal(err)
	}
	return l.Views
}

// checkRenamed says whether the pages and the views of the scene have the new key.
func checkRenamed(t *testing.T, s *Space, pages map[string]*Page) {
	t.Helper()
	a, err := s.Get(pages["A"].ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Meta.Get("state"); !ok || a.Meta.Index("status") >= 0 {
		t.Errorf("A: %+v", a.Meta)
	}
	v := folderViews(t, s, "Work")[0]
	if v.Group != "state" || v.Sort != "-prop:state" || v.Filter[0].Key != "state" || v.Filter[1].Key != "due" ||
		!reflect.DeepEqual(v.Hidden, []string{"state", "due"}) || !reflect.DeepEqual(v.Extra["cardFields"], []any{"state", "due"}) {
		t.Errorf("the view of Work: %+v", v)
	}
	if o := folderViews(t, s, "Other")[0]; o.Group != "status" || o.Sort != "prop:status" {
		t.Errorf("the view of another folder changed: %+v", o)
	}
	d, _ := s.Get(pages["D"].ID)
	if d.Meta.Index("status") < 0 {
		t.Errorf("a page of no type changed: %+v", d.Meta)
	}
}

func TestFieldRenameRewritesViewsAndReportsWhatItSkipped(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s, rev, pages := renameScene(t, root)
	res, err := s.RenameTypeField(Author{}, "task", rev, "status", "state")
	if err != nil {
		t.Fatal(err)
	}
	if res.Pages != 1 {
		t.Errorf("pages = %d", res.Pages)
	}
	want := []SkippedPage{{ID: pages["B"].ID, Path: pages["B"].Path, Reason: "has_key"}, {Path: "Work/C.md", Reason: "unreadable"}}
	if !reflect.DeepEqual(res.Skipped, want) {
		t.Errorf("skipped = %+v", res.Skipped)
	}
	b, _ := s.Get(pages["B"].ID)
	if b.Meta.Index("status") < 0 || b.Meta.Index("state") < 0 {
		t.Errorf("B was changed: %+v", b.Meta)
	}
	checkRenamed(t, s, pages)
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	// One commit: the type, the page and the views.
	files := git(t, root, "diff", "--name-only", "HEAD~1", "HEAD")
	for _, f := range []string{".petalbook/types/task.json", pages["A"].Path, ".petalbook/space.yml"} {
		if !strings.Contains(files, f) {
			t.Errorf("%s is not in the commit:\n%s", f, files)
		}
	}
}

// Not parallel: it changes how headers are written for the whole package.
func TestFieldRenameNamesThePageItCannotWrite(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s, rev, pages := renameScene(t, root)
	orig := setHeader
	setHeader = func(*page.Doc, page.Meta) error { return errors.New("boom") }
	defer func() { setHeader = orig }()
	_, err := s.RenameTypeField(Author{}, "task", rev, "status", "state")
	if err == nil || !strings.Contains(err.Error(), pages["A"].Path) {
		t.Errorf("the error does not name the page: %v", err)
	}
	// Nothing was written.
	setHeader = orig
	if !strings.Contains(readType(t, root, "task"), `"status"`) {
		t.Error("the type was changed")
	}
}

// journalRecords reads the records of the journal of the space at root.
func journalRecords(t *testing.T, root string) []jrec {
	t.Helper()
	data, err := os.ReadFile(journalFile(root))
	if err != nil {
		t.Fatal(err)
	}
	rd := readJournal(data)
	if len(rd.damage) > 0 || rd.cut != "" {
		t.Fatalf("the journal is damaged: %+v (%s at %d)", rd.damage, rd.cut, rd.end)
	}
	return rd.recs
}
