package space

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func journalOpsAt(t *testing.T, s *Space) []string {
	t.Helper()
	data, _ := os.ReadFile(journalFile(s.Root()))
	var ops []string
	for _, r := range readJournal(data).recs {
		ops = append(ops, r.Op)
	}
	return ops
}

// A change of a space that follows a remote, cut short by a crash before its
// push went through: nobody was told it was made, so the next start takes it
// back, whether it was journaled only, written, or committed.
func TestAnUnpushedRemoteChangeIsTakenBackAtTheNextStart(t *testing.T) {
	for _, step := range []string{"save journaled", "remote committed"} {
		t.Run(step, func(t *testing.T) {
			t.Parallel()
			f := newRemoteSpace(t)
			p := mustCreate(t, f.s, "", meta("title", "Kept"), "v1\n")
			tip := f.tip()
			stopAt(f.s, step, 1)
			survive(t, func() { f.s.Save(Author{}, p.ID, p.Rev, meta("title", "Kept"), "v2\n") })
			crash(f.s)
			f.reopen()
			if got := readDisk(t, f.s, p.Path); !strings.HasSuffix(got, "---\nv1\n") {
				t.Errorf("the change was not taken back:\n%s", got)
			}
			if head := git(t, f.root, "rev-parse", "HEAD"); head != tip {
				t.Errorf("HEAD %s, the remote %s", head, tip)
			}
			if st := git(t, f.root, "status", "--porcelain"); st != "" {
				t.Errorf("the clone is not clean: %q", st)
			}
			if ops := journalOpsAt(t, f.s); len(ops) != 0 {
				t.Errorf("journal left: %v", ops)
			}
			if err := f.syncNow(); err != nil {
				t.Fatal(err)
			}
			if f.tip() != tip {
				t.Errorf("the change was pushed: %v", f.remoteLog())
			}
			consistent(t, f.s)
		})
	}
}

// A change whose push went through before the crash stays: HEAD is on the
// remote.
func TestAPushedRemoteChangeStaysAtTheNextStart(t *testing.T) {
	t.Parallel()
	f := newRemoteSpace(t)
	p := mustCreate(t, f.s, "", meta("title", "Kept"), "v1\n")
	stopAt(f.s, "remote pushed", 1)
	survive(t, func() { f.s.Save(Author{}, p.ID, p.Rev, meta("title", "Kept"), "v2\n") })
	crash(f.s)
	pushed := f.tip()
	f.reopen()
	if got := readDisk(t, f.s, p.Path); !strings.HasSuffix(got, "---\nv2\n") {
		t.Errorf("the pushed change was taken back:\n%s", got)
	}
	if head := git(t, f.root, "rev-parse", "HEAD"); head != pushed {
		t.Errorf("HEAD %s, the remote %s", head, pushed)
	}
	if st := git(t, f.root, "status", "--porcelain"); st != "" {
		t.Errorf("the clone is not clean: %q", st)
	}
	consistent(t, f.s)
}

// A move of a space that follows a remote is one transaction with its link
// rewrites, journaled without "l" or "m" records; cut short by a crash, it
// is taken back whole.
func TestAnUnpushedRemoteMoveIsTakenBackWhole(t *testing.T) {
	t.Parallel()
	f := newRemoteSpace(t)
	tg := mustCreate(t, f.s, "Old", meta("title", "Target"), "t\n")
	ref := mustCreate(t, f.s, "", meta("title", "Ref"), "see [t](Old/Target.md)\n")
	before := readDisk(t, f.s, ref.Path)
	tip := f.tip()
	stopAt(f.s, "edit written", 1)
	survive(t, func() { f.s.Move(Author{}, tg.ID, "New") })
	for _, op := range journalOpsAt(t, f.s) {
		if op == "l" || op == "ld" || op == "m" {
			t.Errorf("the move journaled %q", op)
		}
	}
	crash(f.s)
	f.reopen()
	if !exists(f.s, "Old/Target.md") || exists(f.s, "New/Target.md") {
		t.Fatalf("the move was not taken back")
	}
	if got := readDisk(t, f.s, ref.Path); got != before {
		t.Errorf("a link rewrite stayed:\n%s", got)
	}
	if head := git(t, f.root, "rev-parse", "HEAD"); head != tip {
		t.Errorf("HEAD moved")
	}
	if st := git(t, f.root, "status", "--porcelain"); st != "" {
		t.Errorf("the clone is not clean: %q", st)
	}
	consistent(t, f.s)
}

// A change that is undone (its commit failed) gives its transaction up in
// the journal before anything is put back.
func TestAnUndoneRemoteChangeGivesItsTransactionUp(t *testing.T) {
	t.Parallel()
	f := newRemoteSpace(t)
	p := mustCreate(t, f.s, "", meta("title", "Kept"), "v1\n")
	f.s.step = func(step string) error {
		switch step {
		case "remote committed":
			return errors.New("refused")
		case "remote undoing":
			panic(crashed{})
		}
		return nil
	}
	survive(t, func() { f.s.Save(Author{}, p.ID, p.Rev, meta("title", "Kept"), "v2\n") })
	ops := strings.Join(journalOpsAt(t, f.s), " ")
	if !strings.Contains(ops, "x") || !strings.Contains(ops, "k") {
		t.Errorf("journal: %s", ops)
	}
	crash(f.s)
	f.reopen()
	if got := readDisk(t, f.s, p.Path); !strings.HasSuffix(got, "---\nv1\n") {
		t.Errorf("the change was not taken back:\n%s", got)
	}
}

// A change of a folder name's case, made through a third name on a file
// system that folds case, cut short after it was committed and before the
// push: the next start opens, and the folder is back under its old name.
func TestAnUnpushedRemoteFolderRespellingIsTakenBack(t *testing.T) {
	t.Parallel()
	f := newRemoteSpace(t)
	p := mustCreate(t, f.s, "notes", meta("title", "Apple"), "fruit\n")
	tip := f.tip()
	f.s.ops = foldingOps(nil)
	stopAt(f.s, "remote committed", 1)
	survive(t, func() { f.s.MoveFolder(Author{}, "notes", "Notes") })
	crash(f.s)
	f.reopen()
	if !exists(f.s, "notes/Apple.md") || exists(f.s, "Notes") {
		t.Fatalf("the folder is not back: notes/Apple.md %v, Notes %v", exists(f.s, "notes/Apple.md"), exists(f.s, "Notes"))
	}
	if head := git(t, f.root, "rev-parse", "HEAD"); head != tip {
		t.Errorf("HEAD %s, the remote %s", head, tip)
	}
	if st := git(t, f.root, "status", "--porcelain"); st != "" {
		t.Errorf("the clone is not clean: %q", st)
	}
	if _, err := f.s.Get(p.ID); err != nil {
		t.Errorf("the page is lost: %v", err)
	}
	if st := f.last(); st.State == RemoteError {
		t.Errorf("status %+v", st)
	}
}

// A journal an earlier build left, with records this one no longer writes
// ("l", "ld", "m"): the next start opens, logs them, and takes nothing of
// them for files.
func TestRecordsAnEarlierBuildWroteDoNotStopTheStart(t *testing.T) {
	t.Parallel()
	f := newRemoteSpace(t)
	mustCreate(t, f.s, "Old", meta("title", "Apple"), "fruit\n")
	head := git(t, f.root, "rev-parse", "HEAD")
	f.s.Close()
	journal := `{"op":"rp","rev":"` + head + `"}` + "\n" +
		`{"op":"m","f":"Old/Apple.md","to":"New/Apple.md"}` + "\n" +
		`{"op":"l","f":"Old","to":"New","dir":true}` + "\n" +
		`{"op":"ld","f":"Old","to":"New","dir":true}` + "\n"
	if err := os.WriteFile(journalFile(f.root), framed(journal), 0o644); err != nil {
		t.Fatal(err)
	}
	f.reopen()
	if !exists(f.s, "Old/Apple.md") {
		t.Error("the page is gone")
	}
	if st := git(t, f.root, "status", "--porcelain"); st != "" {
		t.Errorf("the clone is not clean: %q", st)
	}
	if st := f.last(); st.State == RemoteError {
		t.Errorf("status %+v", st)
	}
}

// A repair of the way to a folder pushed, then a crash before the change
// journaled the new commit it starts from, with a file of the change half
// written: HEAD is on the remote and stays, and the files of the change are
// put back as HEAD has them, so the clone is not refused.
func TestAChangeWhoseCommitIsOnTheRemotePutsItsFilesBackToIt(t *testing.T) {
	t.Parallel()
	f := newRemoteSpace(t)
	p := mustCreate(t, f.s, "", meta("title", "Kept"), "v1\n")
	before := git(t, f.root, "rev-parse", "HEAD~1")
	head := git(t, f.root, "rev-parse", "HEAD")
	kept := readDisk(t, f.s, p.Path)
	f.s.Close()
	journal := `{"op":"rp","rev":"` + before + `"}` + "\n" + `{"op":"t","p":"` + p.Path + `"}` + "\n"
	if err := os.WriteFile(journalFile(f.root), framed(journal), 0o644); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(f.root, p.Path), []byte(kept[:len(kept)/2]), 0o644)
	f.reopen()
	if got := readDisk(t, f.s, p.Path); got != kept {
		t.Errorf("the half-written file:\n%s", got)
	}
	if h := git(t, f.root, "rev-parse", "HEAD"); h != head {
		t.Errorf("HEAD %s, want %s", h, head)
	}
	if st := f.last(); st.State == RemoteError {
		t.Errorf("status %+v", st)
	}
}
