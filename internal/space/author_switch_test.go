package space

import (
	"strings"
	"testing"
)

// Ann edits a page and Bo moves it in the same quiet window: the edit stays
// Ann's, because Bo's change first commits what is pending for Ann.
func TestAnotherAuthorsMoveDoesNotTakeOverEarlierEdit(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s := openTwoAuthors(t, root)
	p, err := s.Create(Author{}, CreateInput{Folder: "a", Meta: meta("title", "Pee"), Body: "v1\n"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	p2, err := s.Save(ann, p.ID, p.Rev, meta("title", "Pee"), "v2 by ann\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Move(bo, p2.ID, "b"); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	log := commitLog(t, root)
	if len(log) < 3 || !strings.HasPrefix(log[1], "Ann Lee|Service|Edit Pee|") || !strings.HasPrefix(log[0], "Bo Chen|Service|") {
		t.Fatalf("got\n%s", strings.Join(log, "\n"))
	}
	if out, err := gitTry(root, "show", "HEAD~1:"+p.Path); err != nil || !strings.Contains(out, "v2 by ann") {
		t.Fatalf("Ann's commit does not hold her text: %q %v", out, err)
	}
}

// Ann moves a folder and Bo edits a page in it: the rename is Ann's alone and
// Bo's commit holds only his edit.
func TestFolderMoveAndLaterEditAreNotSplitOrMixed(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s := openTwoAuthors(t, root)
	p, err := s.Create(Author{}, CreateInput{Folder: "x", Meta: meta("title", "Pee"), Body: "v1\n"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MoveFolder(ann, "x", "y"); err != nil {
		t.Fatal(err)
	}
	cur, err := s.Get(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(bo, p.ID, cur.Rev, meta("title", "Pee"), "v2 by bo\n"); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	log := commitLog(t, root)
	if len(log) < 3 || !strings.HasPrefix(log[1], "Ann Lee|Service|") || !strings.HasPrefix(log[0], "Bo Chen|Service|Edit Pee|") {
		t.Fatalf("got\n%s", strings.Join(log, "\n"))
	}
	if !strings.Contains(log[1], "y/") || strings.Contains(log[0], "x/") {
		t.Fatalf("the rename is split or mixed:\n%s", strings.Join(log, "\n"))
	}
	if out, err := gitTry(root, "show", "HEAD~1:"+cur.Path); err != nil || strings.Contains(out, "by bo") {
		t.Fatalf("Ann's commit holds Bo's text: %q %v", out, err)
	}
}
