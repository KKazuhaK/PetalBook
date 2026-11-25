package space

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The record types a journal may hold: transactions, and the records of the
// steps of a move and of a remote change.
var txOps = map[string]bool{"x": true, "p": true, "a": true, "k": true, "c": true, "mr": true, "md": true, "mdd": true, "rp": true, "r": true}

func checkOnlyTransactions(t *testing.T, root, what string) {
	t.Helper()
	data, _ := os.ReadFile(journalFile(root))
	for _, r := range readJournal(data).recs {
		if !txOps[r.Op] {
			t.Errorf("%s journaled %q (%s%s)", what, r.Op, r.Path, r.From)
		}
	}
}

// Every write of the space, of pages, settings, types, templates, tags and
// preferences, moves and deletions, is journaled as a transaction.
func TestEveryWriteIsATransaction(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	step := func(what string, f func() error) {
		t.Helper()
		if err := s.Flush(); err != nil {
			t.Fatal(err)
		}
		if err := f(); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		checkOnlyTransactions(t, root, what)
	}
	var p *Page
	step("create", func() (err error) {
		p, err = s.Create(Author{}, CreateInput{Meta: meta("title", "A", "status", "open"), Body: "x\n"})
		return
	})
	step("type", func() error {
		_, err := putCurrent(s, "task", TypeInput{Title: "Task"})
		return err
	})
	step("folder", func() error { _, err := s.CreateFolder(Author{}, "Work"); return err })
	step("bind", func() error { _, err := s.BindFolder(Author{}, "Work", "task"); return err })
	step("tag color", func() error { return s.SetTagColor(Author{}, "x", "red") })
	step("prefs", func() error {
		return s.WritePrefs(Author{}, func(m map[string]any) bool { m["k"] = "v"; return true })
	})
	var tpl *Template
	step("template", func() (err error) { tpl, err = s.PutTemplate(Author{}, "T", "", meta("title", "T"), "t\n"); return })
	step("template delete", func() error { return s.DeleteTemplate(Author{}, "T", tpl.Rev) })
	step("move to folder", func() error { _, err := s.Move(Author{}, p.ID, "Work"); return err })
	step("folder move", func() error { _, err := s.MoveFolder(Author{}, "Work", "Done"); return err })
	step("page delete", func() error { return s.Delete(Author{}, p.ID) })
	step("folder delete", func() error { _, err := s.DeleteFolder(Author{}, "Done", true); return err })
	step("type delete", func() error { return deleteCurrent(s, "task") })
}

// A type written, then its file changed outside PetalBook before the next
// start committed it: Git records what the server wrote (owner decision 1).
func TestASettingsWriteChangedOutsideBeforeRecoveryIsCommittedAsWritten(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := putCurrent(s, "task", TypeInput{Title: "Task"}); err != nil {
		t.Fatal(err)
	}
	crash(s)
	rel := typesDir + "/task.json"
	server, _ := os.ReadFile(filepath.Join(root, rel))
	outside := strings.Replace(string(server), "Task", "Changed outside", 1)
	os.WriteFile(filepath.Join(root, rel), []byte(outside), 0o644)
	s2 := openAt(t, root)
	if err := s2.Flush(); err != nil {
		t.Fatal(err)
	}
	checkServerCommitted(t, root, rel, string(server), outside)
}

// A page deleted, then a file put back under its name outside PetalBook
// before the next start committed the deletion: Git records the deletion.
func TestADeletionIsCommittedEvenIfTheNameIsTakenAgainOutside(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	p := mustCreate(t, s, "", meta("title", "Gone"), "x\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(Author{}, p.ID); err != nil {
		t.Fatal(err)
	}
	crash(s)
	os.WriteFile(filepath.Join(root, p.Path), []byte("someone else's\n"), 0o644)
	s2 := openAt(t, root)
	if err := s2.Flush(); err != nil {
		t.Fatal(err)
	}
	if files := git(t, root, "ls-files"); strings.Contains(files, p.Path) {
		t.Errorf("the deletion was not committed:\n%s", files)
	}
	if b, _ := os.ReadFile(filepath.Join(root, p.Path)); string(b) != "someone else's\n" {
		t.Errorf("the file put there outside: %q", b)
	}
}

// A field rename cut short by a crash after it was journaled, before any page
// was written: the next start writes the type, every page and the views, from
// the journal.
func TestAFieldRenameStoppedAfterItsTransactionIsFinished(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s, rev, pages := renameScene(t, root)
	stopAt(s, "edits journaled", 1)
	survive(t, func() { s.RenameTypeField(Author{}, "task", rev, "status", "state") })
	crash(s)
	s2 := openAt(t, root)
	if _, err := s2.Reindex(); err != nil {
		t.Fatal(err)
	}
	checkRenamed(t, s2, pages)
	if err := s2.Flush(); err != nil {
		t.Fatal(err)
	}
	if st := uncommitted(t, root); st != "" {
		t.Errorf("not committed:\n%s", st)
	}
}

// One author's commit fails (the index is locked) while another's is made,
// and the server stops before the retry: the deletion and the old name of a
// move of the failed author are still committed by the next start.
func TestADeletionAndAMoveWhoseCommitFailedAreCommittedAfterACrash(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openTwoAuthors(t, root)
	a := mustCreateAs(t, s, ann, "Alpha")
	gone := mustCreateAs(t, s, bo, "Gone")
	moved := mustCreateAs(t, s, bo, "Moved")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	// Two authors pending at once: the commit when the author switches fails.
	lock := filepath.Join(root, ".git", "index.lock")
	if _, err := s.Save(ann, a.ID, a.Rev, meta("title", "Alpha"), "a2\n"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(lock, nil, 0o644)
	if err := s.Delete(bo, gone.ID); err != nil {
		t.Fatal(err)
	}
	m, err := s.Move(bo, moved.ID, "Archive")
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(lock)
	s.cm.git.before = func(args []string) {
		if args[0] == "update-ref" { // Ann's commit is made: Bo's fails
			os.WriteFile(lock, nil, 0o644)
		}
	}
	if err := s.Flush(); err == nil {
		t.Fatal("Bo's commit should have failed")
	}
	s.cm.git.before = nil
	crash(s)
	os.Remove(lock)
	s2 := openTwoAuthors(t, root)
	if err := s2.Flush(); err != nil {
		t.Fatal(err)
	}
	files := git(t, root, "ls-tree", "-r", "--name-only", "HEAD")
	has := func(p string) bool { return strings.Contains("\n"+files+"\n", "\n"+p+"\n") }
	if has(gone.Path) || has(moved.Path) || !has(m.Path) {
		t.Errorf("committed (%s gone, %s moved to %s):\n%s\n%s", gone.Path, moved.Path, m.Path, files, git(t, root, "status", "--short"))
	}
}

// A folder move or delete stopped by a crash once its transaction was
// journaled, before anything else was written, also with a space.yml that
// cannot be used (it is put right by the change): the next start finishes
// it, and the bindings of the folder are where it is, or gone with it.
func TestAFolderChangeStoppedAfterItsTransactionFinishesItsBindings(t *testing.T) {
	needGit(t)
	for _, c := range []struct {
		name     string
		del      bool
		unusable bool
		want     map[string]string
	}{
		{"move", false, false, map[string]string{"B": "t", "B/sub": "t"}},
		{"move, space.yml unusable", false, true, map[string]string{"B": "t", "B/sub": "t"}},
		{"delete", true, false, map[string]string{}},
		{"delete, space.yml unusable", true, true, map[string]string{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			s := openAt(t, root)
			if _, err := putCurrent(s, "t", TypeInput{}); err != nil {
				t.Fatal(err)
			}
			mustCreate(t, s, "A/sub", meta("title", "P"), "x\n")
			for _, f := range []string{"A", "A/sub"} {
				if _, err := s.BindFolder(Author{}, f, "t"); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Flush(); err != nil {
				t.Fatal(err)
			}
			if c.unusable {
				setSpaceYML(t, root, "folders: [unclosed\n")
			}
			stopAt(s, "edits journaled", 1)
			survive(t, func() {
				if c.del {
					s.DeleteFolder(Author{}, "A", true)
				} else {
					s.MoveFolder(Author{}, "A", "B")
				}
			})
			crash(s)
			s2 := openAt(t, root)
			if m := bindingMap(s2); !reflect.DeepEqual(m, c.want) {
				t.Errorf("bindings after recovery: %v\n%s", m, spaceYML(root))
			}
		})
	}
}

// A field rename stopped by a crash after the type's file and the first page
// were written: the next start writes the rest from the journal.
func TestAFieldRenameStoppedAfterItsFirstPageIsFinished(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s, rev, pages := renameScene(t, root)
	stopAt(s, "rename written", 2)
	survive(t, func() { s.RenameTypeField(Author{}, "task", rev, "status", "state") })
	crash(s)
	s2 := openAt(t, root)
	if _, err := s2.Reindex(); err != nil {
		t.Fatal(err)
	}
	checkRenamed(t, s2, pages)
}

// The things a repair of the way to the settings moves aside are journaled:
// a crash before they are committed has the next start commit them.
func TestWhatARepairOfTheWayMovesAsideIsCommittedAfterACrash(t *testing.T) {
	t.Parallel()
	s := settled(t)
	root := s.Root()
	os.RemoveAll(filepath.Join(root, ".petalbook"))
	if err := os.WriteFile(filepath.Join(root, ".petalbook"), []byte("a file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stopAt(s, "way cleared", 1)
	survive(t, func() { s.BindFolder(Author{}, "C", "wenan") })
	crash(s)
	copies := copiesAt(root, ".petalbook")
	if len(copies) != 1 {
		t.Fatalf("what was moved aside: %v", copies)
	}
	s2 := openAt(t, root)
	if err := s2.Flush(); err != nil {
		t.Fatal(err)
	}
	if files := git(t, root, "ls-tree", "-r", "--name-only", "HEAD"); !strings.Contains(files, copies[0]) {
		t.Errorf("the copy is not committed:\n%s", files)
	}
}
