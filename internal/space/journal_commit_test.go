package space

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A move whose rewrite of the links to it is half written when a commit
// comes (the idle timer fires in the middle): the commit takes none of it,
// and the move with all its rewrites is one commit.
func TestAMoveIsOneCommitEvenWhenACommitComesInTheMiddle(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	target := mustCreate(t, s, "", meta("title", "Target"), "t\n")
	var from []*Page
	for _, n := range []string{"One", "Two", "Three"} {
		from = append(from, mustCreate(t, s, "Notes", meta("title", n), "see [Target](../Target.md)\n"))
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	before := len(commits(t, root))
	written := 0
	s.step = func(step string) error {
		if step == "edit written" {
			if written++; written == 1 {
				if err := s.cm.commit(); err != nil {
					t.Error(err)
				}
			}
		}
		return nil
	}
	if _, err := s.Move(Author{}, target.ID, "Archive"); err != nil {
		t.Fatal(err)
	}
	s.step = nil
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if c := commits(t, root); len(c) != before+1 {
		t.Fatalf("the move made %d commits: %q", len(c)-before, c[:len(c)-before])
	}
	for _, p := range from {
		if got := git(t, root, "show", "HEAD:"+p.Path); !strings.Contains(got, "../Archive/Target.md") {
			t.Errorf("%s is not committed rewritten:\n%s", p.Path, got)
		}
	}
}

// A save, then an edit of the same file outside PetalBook before the commit:
// Git records what the server wrote, and the edit stays in the work tree, not
// committed.
func TestAnOutsideEditBeforeTheCommitStaysUncommitted(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	p := mustCreate(t, s, "", meta("title", "Kept"), "v1\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(Author{}, p.ID, p.Rev, meta("title", "Kept"), "v2\n"); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(root, filepath.FromSlash(p.Path))
	server, _ := os.ReadFile(abs)
	outside := strings.Replace(string(server), "v2", "v3 from outside", 1)
	os.WriteFile(abs, []byte(outside), 0o644)
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	checkServerCommitted(t, root, p.Path, string(server), outside)
}

func checkServerCommitted(t *testing.T, root, rel, server, outside string) {
	t.Helper()
	if got := git(t, root, "show", "HEAD:"+rel); strings.TrimSpace(got) != strings.TrimSpace(server) {
		t.Errorf("committed:\n%s\nwant what the server wrote:\n%s", got, server)
	}
	if got, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))); string(got) != outside {
		t.Errorf("the work tree lost the edit made outside:\n%s", got)
	}
	if git(t, root, "diff", "--name-only", "--", rel) != rel || git(t, root, "diff", "--cached", "--name-only", "--", rel) != "" {
		t.Errorf("the edit made outside should be an uncommitted change:\n%s", git(t, root, "status", "--short"))
	}
}

// A page the server made, or a save it wrote, changed outside PetalBook
// before the next start committed it: Git records what the server wrote, and
// the outside edit stays in the work tree, not committed.
func TestAChangeEditedOutsideBeforeRecoveryIsCommittedAsTheServerWroteIt(t *testing.T) {
	needGit(t)
	for _, c := range []string{"created", "saved"} {
		t.Run(c, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			s := openAt(t, root)
			if err := s.Flush(); err != nil {
				t.Fatal(err)
			}
			p := mustCreate(t, s, "", meta("title", "Made"), "v1\n")
			if c == "saved" {
				if err := s.Flush(); err != nil {
					t.Fatal(err)
				}
				var err error
				if p, err = s.Save(Author{}, p.ID, p.Rev, meta("title", "Made"), "v2\n"); err != nil {
					t.Fatal(err)
				}
			}
			crash(s)
			abs := filepath.Join(root, filepath.FromSlash(p.Path))
			server, _ := os.ReadFile(abs)
			outside := string(server) + "added outside\n"
			os.WriteFile(abs, []byte(outside), 0o644)
			s2 := openAt(t, root)
			if err := s2.Flush(); err != nil {
				t.Fatal(err)
			}
			checkServerCommitted(t, root, p.Path, string(server), outside)
		})
	}
}

// A save that renames the page, whose rewrite of the links to it then fails:
// the old name is committed as gone, not put back with the new content.
func TestARenamingSaveWhoseRelinkFailsDoesNotCommitTheOldName(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	p := mustCreate(t, s, "", meta("title", "Old"), "v1\n")
	mustCreate(t, s, "", meta("title", "Linker"), "see [Old](Old.md)\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	s.step = func(step string) error {
		if step == "edits journaled" {
			return errors.New("disk full")
		}
		return nil
	}
	n, err := s.Save(Author{}, p.ID, p.Rev, meta("title", "New"), "v2\n")
	s.step = nil
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	files := git(t, root, "ls-files")
	if strings.Contains(files, "Old.md") || !strings.Contains(files, n.Path) {
		t.Errorf("committed files:\n%s", files)
	}
}

// A page that is executable in Git keeps its mode when a save is committed
// from the blob the server wrote.
func TestACommitKeepsTheModeOfTheFile(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	p := mustCreate(t, s, "", meta("title", "Run"), "v1\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	git(t, root, "update-index", "--chmod=+x", p.Path)
	git(t, root, "-c", "user.name=T", "-c", "user.email=t@example.test", "commit", "-qm", "executable")
	os.Chmod(filepath.Join(root, p.Path), 0o755)
	if _, err := s.Save(Author{}, p.ID, p.Rev, meta("title", "Run"), "v2\n"); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := git(t, root, "ls-tree", "HEAD", p.Path); !strings.HasPrefix(got, "100755 ") {
		t.Errorf("the mode was lost: %s", got)
	}
}

// One author's commit fails and another's is made: the trim keeps the
// content the server wrote for the failed one, so its retry commits that,
// not an edit made outside in between.
func TestARetriedCommitKeepsTheContentTheServerWrote(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openTwoAuthors(t, root)
	a := mustCreateAs(t, s, ann, "Alpha")
	b := mustCreateAs(t, s, bo, "Beta")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	// Two authors pending at once: the commit when the author switches fails.
	lock := filepath.Join(root, ".git", "index.lock")
	if _, err := s.Save(ann, a.ID, a.Rev, meta("title", "Alpha"), "a2\n"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(lock, nil, 0o644)
	b2, err := s.Save(bo, b.ID, b.Rev, meta("title", "Beta"), "b2\n")
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
	os.Remove(lock)
	abs := filepath.Join(root, b2.Path)
	server, _ := os.ReadFile(abs)
	outside := string(server) + "outside\n"
	os.WriteFile(abs, []byte(outside), 0o644)
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	checkServerCommitted(t, root, b2.Path, string(server), outside)
}

func mustCreateAs(t *testing.T, s *Space, by Author, title string) *Page {
	t.Helper()
	p, err := s.Create(by, CreateInput{Meta: meta("title", title), Body: "x\n"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A save journaled but never written, whose file was then changed outside
// PetalBook: the next start gives it up. Nothing that never was on disk is
// committed, and the outside edit stays uncommitted.
func TestASaveNeverWrittenWhoseFileChangedOutsideIsGivenUp(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	p := mustCreate(t, s, "", meta("title", "Kept"), "v1\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	stopAt(s, "save journaled", 1)
	survive(t, func() { s.Save(Author{}, p.ID, p.Rev, meta("title", "Kept"), "v2\n") })
	crash(s)
	abs := filepath.Join(root, filepath.FromSlash(p.Path))
	before, _ := os.ReadFile(abs)
	os.WriteFile(abs, append(before, "outside\n"...), 0o644)
	var logs logBuffer
	s2 := openAt(t, root, withLog(&logs))
	data, _ := os.ReadFile(journalFile(root))
	gaveUp := false
	for _, r := range readJournal(data).recs {
		gaveUp = gaveUp || r.Op == "k"
	}
	if !gaveUp {
		t.Errorf("the save was not given up in the journal")
	}
	if err := s2.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := git(t, root, "show", "HEAD:"+p.Path); strings.Contains(got, "v2") {
		t.Errorf("a save never written was committed:\n%s", got)
	}
	if got := readDisk(t, s2, p.Path); !strings.Contains(got, "outside") {
		t.Errorf("the outside edit is lost:\n%s", got)
	}
}

// A rewrite that stops part way leaves nothing of its transaction behind in
// the committer.
func TestARewriteThatStopsLeavesNoPreparedTransaction(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	target := mustCreate(t, s, "", meta("title", "Target"), "t\n")
	mustCreate(t, s, "", meta("title", "One"), "see [Target](Target.md)\n")
	mustCreate(t, s, "", meta("title", "Two"), "see [Target](Target.md)\n")
	s.step = func(step string) error {
		if step == "edit written" {
			return errors.New("disk full")
		}
		return nil
	}
	s.Move(Author{}, target.ID, "Archive")
	s.step = nil
	s.cm.mu.Lock()
	n := len(s.cm.txs)
	s.cm.mu.Unlock()
	if n != 0 {
		t.Errorf("%d prepared transactions left", n)
	}
}
