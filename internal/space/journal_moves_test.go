package space

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// moveScene is a page Old/目标.md with five pages linking to it (one link
// with an id, one without), and another page in its folder.
func moveScene(t *testing.T, root string) (*Space, *Page, []*Page) {
	t.Helper()
	s := openAt(t, root)
	tg := mustCreate(t, s, "Old", meta("title", "目标"), "x\n")
	mustCreate(t, s, "Old", meta("title", "own"), "see [m](missing.md)\n")
	var refs []*Page
	for i := range 5 {
		refs = append(refs, mustCreate(t, s, "R", meta("title", fmt.Sprintf("r%d", i)), "[t](../Old/目标.md) [u](../Old/目标.md#a \""+tg.ID+"\")\n"))
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	return s, tg, refs
}

func checkMovedLinks(t *testing.T, s *Space, refs []*Page, to string) {
	t.Helper()
	for _, r := range refs {
		got := readDisk(t, s, r.Path)
		if strings.Contains(got, "Old/") || strings.Count(got, "../"+to) != 2 {
			t.Errorf("%s:\n%s", r.Path, got)
		}
	}
}

func checkAllCommitted(t *testing.T, s *Space, root string) {
	t.Helper()
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if st := git(t, root, "status", "--porcelain"); st != "" {
		t.Errorf("not committed: %q", st)
	}
}

// A move of a page or a folder that a crash stopped once its transaction was
// journaled (before any write, or after some): the next start finishes it
// from the journal, and nothing is planned again.
func TestAMoveStoppedAfterItsTransactionIsFinished(t *testing.T) {
	needGit(t)
	for _, c := range []struct {
		name, step string
		n          int
		folder     bool
	}{
		{"page, nothing written", "edits journaled", 1, false},
		{"page, two written", "edit written", 2, false},
		{"folder, nothing written", "edits journaled", 1, true},
		{"folder, two written", "edit written", 2, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			s, tg, refs := moveScene(t, root)
			stopAt(s, c.step, c.n)
			to := "New/目标.md"
			if c.folder {
				survive(t, func() { s.MoveFolder(Author{}, "Old", "New") })
			} else {
				survive(t, func() { s.Move(Author{}, tg.ID, "New") })
			}
			crash(s)
			s2 := openAt(t, root)
			if exists(s2, "Old/目标.md") || !exists(s2, to) {
				t.Fatalf("the move was not finished: old %v, new %v", exists(s2, "Old/目标.md"), exists(s2, to))
			}
			checkMovedLinks(t, s2, refs, to)
			checkAllCommitted(t, s2, root)
		})
	}
}

// A move that a crash stopped before its transaction was journaled (the new
// name taken, or the folder renamed, nothing else): the next start takes it
// back, since nobody was told it was made.
func TestAMoveStoppedBeforeItsTransactionIsTakenBack(t *testing.T) {
	needGit(t)
	for _, folder := range []bool{false, true} {
		t.Run(fmt.Sprintf("folder=%v", folder), func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			s, tg, refs := moveScene(t, root)
			before := map[string]string{}
			for _, r := range refs {
				before[r.Path] = readDisk(t, s, r.Path)
			}
			if folder {
				stopAt(s, "folder moved", 1)
				survive(t, func() { s.MoveFolder(Author{}, "Old", "New") })
			} else {
				stopAt(s, "name reserved", 1)
				survive(t, func() { s.Move(Author{}, tg.ID, "New") })
			}
			crash(s)
			s2 := openAt(t, root)
			if !exists(s2, "Old/目标.md") || exists(s2, "New/目标.md") {
				t.Fatalf("the move was not taken back: old %v, new %v", exists(s2, "Old/目标.md"), exists(s2, "New/目标.md"))
			}
			after := map[string]string{}
			for _, r := range refs {
				after[r.Path] = readDisk(t, s2, r.Path)
			}
			if !reflect.DeepEqual(before, after) {
				t.Errorf("links changed for a move that was taken back")
			}
			if probs, _ := s2.Problems(); len(probs) != 0 {
				t.Errorf("problems: %+v", probs)
			}
			if err := s2.Flush(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A move is committed from what the server wrote: the old name as gone, the
// new one with the content the move gave it, even if the file was changed
// outside before the commit. And the journal of a move has no "l" record.
func TestAMoveIsCommittedFromWhatTheServerWrote(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s, tg, _ := moveScene(t, root)
	if _, err := s.Move(Author{}, tg.ID, "New"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(journalFile(root))
	for _, r := range readJournal(data).recs {
		if r.Op == "l" || r.Op == "ld" {
			t.Errorf("a move journaled %q", r.Op)
		}
	}
	abs := filepath.Join(root, "New", "目标.md")
	server, _ := os.ReadFile(abs)
	outside := string(server) + "outside\n"
	os.WriteFile(abs, []byte(outside), 0o644)
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	checkServerCommitted(t, root, "New/目标.md", string(server), outside)
	if files := git(t, root, "ls-files", "Old"); strings.Contains(files, "目标") {
		t.Errorf("the old name is still committed: %s", files)
	}
}
