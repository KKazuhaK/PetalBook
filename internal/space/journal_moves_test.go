package space

import (
	"errors"
	"fmt"
	"log/slog"
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

// A folder move that was made is not made again at the next start, whatever
// is at its old and new places by then: here a new folder under the old name,
// and the moved folder moved on.
func TestAFolderMoveThatWasMadeIsNotMadeAgain(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s, _, _ := moveScene(t, root)
	if _, err := s.MoveFolder(Author{}, "Old", "New"); err != nil {
		t.Fatal(err)
	}
	fresh := mustCreate(t, s, "Old", meta("title", "fresh"), "x\n")
	if _, err := s.MoveFolder(Author{}, "New", "Archive"); err != nil {
		t.Fatal(err)
	}
	crash(s)
	s2 := openAt(t, root)
	if !exists(s2, fresh.Path) || exists(s2, "New") || !exists(s2, "Archive/目标.md") {
		t.Fatalf("the folders after the restart: Old/fresh.md %v, New %v, Archive/目标.md %v",
			exists(s2, fresh.Path), exists(s2, "New"), exists(s2, "Archive/目标.md"))
	}
	checkAllCommitted(t, s2, root)
}

// A folder move that was made, then a folder renamed and a new one made under
// the old name by hand before the next start: the next start leaves them so.
func TestAFolderMoveDoesNotTakeAFolderMadeOutsideUnderItsOldName(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s, _, _ := moveScene(t, root)
	if _, err := s.MoveFolder(Author{}, "Old", "New"); err != nil {
		t.Fatal(err)
	}
	crash(s)
	if err := os.Rename(filepath.Join(root, "New"), filepath.Join(root, "Mine")); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(root, "Old"), 0o755)
	os.WriteFile(filepath.Join(root, "Old", "hand.md"), []byte(pageText("HANDHAND01", "hand", "x\n")), 0o644)
	s2 := openAt(t, root)
	if !exists(s2, "Old/hand.md") || exists(s2, "New") || !exists(s2, "Mine/目标.md") {
		t.Fatalf("the folders after the restart: Old/hand.md %v, New %v, Mine/目标.md %v",
			exists(s2, "Old/hand.md"), exists(s2, "New"), exists(s2, "Mine/目标.md"))
	}
}

// A journal append whose outcome is not known (it failed, and cutting it off
// failed too) is given up at once, so the next start does not make a change
// the person was told failed.
func TestAPrepareWhoseAppendMayHaveReachedTheDiskIsGivenUp(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "pending.journal")
	j, _, err := openJournal(path, slog.New(slog.NewTextHandler(&logBuffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	c := &committer{j: j, log: slog.New(slog.NewTextHandler(&logBuffer{}, nil)), txs: map[uint64]preparedTx{}}
	j.write = func(f *os.File, b []byte) (int, error) {
		n, _ := f.Write(b)
		return n, errors.New("I/O error")
	}
	failCut := true
	j.truncate = func(f *os.File, size int64) error {
		if failCut {
			failCut = false
			return errors.New("I/O error")
		}
		return f.Truncate(size)
	}
	if _, err := c.Prepare(Author{}, []txFile{{path: "a.md", old: "o", rev: "n", data: []byte("x")}}, nil); err == nil {
		t.Fatal("the prepare should have failed")
	}
	j.write = nil
	j.close()
	data, _ := os.ReadFile(path)
	rd := readJournal(data)
	given := map[uint64]bool{}
	for _, r := range rd.recs {
		if r.Op == "k" {
			given[r.Tx] = true
		}
	}
	for _, r := range rd.recs {
		if r.Op == "x" && !given[r.Tx] {
			t.Errorf("the transaction is in the journal and not given up: %+v", rd.recs)
		}
	}
}

// A folder move journaled and not yet journaled applied when the process
// stopped; then, while the server was down, the moved folder was renamed and
// a new one made under the old name by hand. The next start does not take
// the new folder for the moved one: it is another folder (inode).
func TestAnUnfinishedFolderMoveDoesNotTakeAFolderMadeByHand(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s, _, _ := moveScene(t, root)
	stopAt(s, "edits journaled", 1)
	survive(t, func() { s.MoveFolder(Author{}, "Old", "New") })
	crash(s)
	if err := os.Rename(filepath.Join(root, "New"), filepath.Join(root, "Mine")); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(root, "Old"), 0o755)
	os.WriteFile(filepath.Join(root, "Old", "hand.md"), []byte(pageText("HANDHAND01", "hand", "x\n")), 0o644)
	s2 := openAt(t, root)
	if !exists(s2, "Old/hand.md") || exists(s2, "New") {
		t.Fatalf("the folder made by hand was moved: Old/hand.md %v, New %v", exists(s2, "Old/hand.md"), exists(s2, "New"))
	}
}
