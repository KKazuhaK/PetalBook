package space

import (
	"os"
	"path/filepath"
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
