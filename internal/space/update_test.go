package space

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KKazuhaK/PetalBook/internal/page"
)

// Update works on the file as it is: what edit gets is what is there, and a
// header it leaves out of what it returns is removed.
func TestUpdateWorksOnTheFileAsItIs(t *testing.T) {
	t.Parallel()
	s := open(t)
	p := mustCreate(t, s, "F", meta("title", "页", "tags", []any{"a"}, "keep", "me"), "正文\n")
	got, err := s.Update(Author{}, p.ID, p.Rev, func(m page.Meta, body string) (page.Meta, string, error) {
		if m.Title() != "页" || body != "正文\n" {
			t.Errorf("edit saw %v %q", m, body)
		}
		return m, body + "第二行\n", nil
	})
	if err != nil || got.Body != "正文\n第二行\n" || got.Rev == p.Rev || got.Meta.Title() != "页" {
		t.Fatalf("%v %+v", err, got)
	}
	b, _ := os.ReadFile(filepath.Join(s.Root(), got.Path))
	if !strings.Contains(string(b), "keep: me") {
		t.Fatalf("%s", b)
	}
	// A rev that is not the file's is a conflict and edit is not called; "" checks nothing.
	_, err = s.Update(Author{}, p.ID, p.Rev, func(m page.Meta, body string) (page.Meta, string, error) {
		t.Error("edit was called")
		return m, body, nil
	})
	var ce *ConflictError
	if !errors.As(err, &ce) || ce.Current.Rev != got.Rev {
		t.Fatalf("%v", err)
	}
	same, err := s.Update(Author{}, p.ID, "", func(m page.Meta, body string) (page.Meta, string, error) { return m, body, nil })
	if err != nil || same.Rev != got.Rev {
		t.Fatalf("%v %+v", err, same)
	}
	// An error of edit is the answer and nothing is written.
	boom := errors.New("boom")
	if _, err := s.Update(Author{}, p.ID, got.Rev, func(m page.Meta, body string) (page.Meta, string, error) { return nil, "", boom }); !errors.Is(err, boom) {
		t.Fatalf("%v", err)
	}
	// What is left out of the header goes; a new title renames the file.
	moved, err := s.Update(Author{}, p.ID, got.Rev, func(m page.Meta, body string) (page.Meta, string, error) {
		m = m[:0:0]
		m = append(m, page.Field{Key: "title", Value: "新"})
		return m, body, nil
	})
	if err != nil || moved.Path != "F/新.md" || moved.Meta.Index("keep") >= 0 || moved.Meta.Index("tags") >= 0 || moved.Meta.Created() == "" {
		t.Fatalf("%v %+v", err, moved)
	}
	if _, err := s.Update(Author{}, p.ID, moved.Rev, func(m page.Meta, body string) (page.Meta, string, error) { return m, "bad \xff", nil }); err == nil {
		t.Fatal("a body that is not UTF-8 was taken")
	}
	if _, err := s.Update(Author{}, "01JD3K8Q7Z", "", func(m page.Meta, body string) (page.Meta, string, error) { return m, body, nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestMoveIfChecksTheRev(t *testing.T) {
	t.Parallel()
	s := open(t)
	p := mustCreate(t, s, "F", meta("title", "页"), "正文\n")
	moved, err := s.MoveIf(Author{}, p.ID, "G", p.Rev)
	if err != nil || moved.Folder != "G" {
		t.Fatalf("%v %+v", err, moved)
	}
	changed, err := s.Save(Author{}, p.ID, moved.Rev, moved.Meta, "新\n")
	if err != nil {
		t.Fatal(err)
	}
	var ce *ConflictError
	if _, err := s.MoveIf(Author{}, p.ID, "H", moved.Rev); !errors.As(err, &ce) || ce.Current.Folder != "G" {
		t.Fatalf("%v", err)
	}
	if got, err := s.MoveIf(Author{}, p.ID, "H", changed.Rev); err != nil || got.Folder != "H" {
		t.Fatalf("%v %+v", err, got)
	}
	if got, err := s.MoveIf(Author{}, p.ID, "I", ""); err != nil || got.Folder != "I" {
		t.Fatalf("%v %+v", err, got)
	}
}

func TestPatchMetaIsWhatPatchPropsMakes(t *testing.T) {
	t.Parallel()
	m := meta("id", "X", "title", "t", "a", "1", "b", []any{"x"})
	got, err := PatchMeta(m, meta("a", "2", "c", "new", "b", []any{}), []string{"gone"})
	if err != nil {
		t.Fatal(err)
	}
	if !page.MetaEqual(got, meta("id", "X", "title", "t", "a", "2", "c", "new")) {
		t.Fatalf("%v", got)
	}
	if m.Index("c") >= 0 {
		t.Fatal("the header given was changed")
	}
	for _, bad := range []struct {
		set   page.Meta
		unset []string
	}{{meta("title", "x"), nil}, {meta("id", "x"), nil}, {meta("publish", true), nil}, {meta("a", "1"), []string{"a"}}, {nil, []string{"tags"}}, {meta("", "x"), nil}} {
		if _, err := PatchMeta(m, bad.set, bad.unset); err == nil {
			t.Errorf("%v %v was taken", bad.set, bad.unset)
		}
	}
}
