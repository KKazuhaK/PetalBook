package space

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- a space whose .git is not a folder ------------------------------------------------

// works checks that the space at root takes a page, commits it into its own
// repository and leaves the work tree clean, with its state in stateWant.
func works(t *testing.T, s *Space, stateWant string) {
	t.Helper()
	root := s.Root()
	p := mustCreate(t, s, "", meta("title", "In a checkout"), "x\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if files := git(t, root, "ls-files"); !strings.Contains(files, "In a checkout.md") {
		t.Errorf("the page was not committed:\n%s", files)
	}
	if st := git(t, root, "status", "--porcelain", "--untracked-files=all"); st != "" {
		t.Errorf("the work tree is not clean:\n%s", st)
	}
	if want, _ := filepath.EvalSymlinks(stateWant); s.StateDir() != want {
		t.Errorf("the state directory is %s, not %s", s.StateDir(), want)
	}
	if _, err := os.Stat(filepath.Join(s.StateDir(), "index.db")); err != nil {
		t.Error(err)
	}
	if _, err := s.Save(Author{}, p.ID, p.Rev, meta("title", "In a checkout"), "y\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Move(Author{}, p.ID, "Sub"); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if st := git(t, root, "status", "--porcelain", "--untracked-files=all"); st != "" {
		t.Errorf("the work tree is not clean after a rename:\n%s", st)
	}
}

func firstCommit(t *testing.T, root string) {
	t.Helper()
	s := openAt(t, root)
	mustCreate(t, s, "", meta("title", "First"), "x\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	s.Close()
}

func TestACheckoutMadeWithWorktreeAddOpensWithStateOfItsOwn(t *testing.T) {
	t.Parallel()
	needGit(t)
	main := filepath.Join(t.TempDir(), "main")
	firstCommit(t, main)
	wt := filepath.Join(t.TempDir(), "wt")
	git(t, main, "worktree", "add", "-q", "-b", "other", wt)
	if fi, err := os.Lstat(filepath.Join(wt, ".git")); err != nil || fi.IsDir() {
		t.Fatalf("the checkout's .git is not a file: %v", err)
	}
	gd := git(t, wt, "rev-parse", "--absolute-git-dir")
	s := openAt(t, wt)
	works(t, s, filepath.Join(gd, "petalbook-state"))
	// The main checkout, open at the same time, has state of its own.
	m := openAt(t, main)
	if m.StateDir() == s.StateDir() {
		t.Errorf("two checkouts share %s", m.StateDir())
	}
	works(t, m, filepath.Join(main, ".git", "petalbook-state"))
	if git(t, main, "log", "-1", "--format=%s", "other") == git(t, main, "log", "-1", "--format=%s", "main") && git(t, main, "rev-parse", "other") == git(t, main, "rev-parse", "main") {
		t.Error("the two checkouts did not commit on their own branches")
	}
}

func TestASubmoduleLikeGitFileOpens(t *testing.T) {
	t.Parallel()
	needGit(t)
	top := t.TempDir()
	root := filepath.Join(top, "sub")
	firstCommit(t, root)
	// As git keeps a submodule: the repository under the parent's
	// .git/modules, and a .git file with a relative path to it.
	mod := filepath.Join(top, ".git", "modules", "sub")
	os.MkdirAll(filepath.Dir(mod), 0o755)
	if err := os.Rename(filepath.Join(root, ".git"), mod); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: ../.git/modules/sub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	works(t, openAt(t, root), filepath.Join(mod, "petalbook-state"))
}

func TestARepositoryMadeWithSeparateGitDirOpens(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := filepath.Join(t.TempDir(), "space")
	gd := filepath.Join(t.TempDir(), "repo.git")
	git(t, t.TempDir(), "init", "-q", "--separate-git-dir", gd, root)
	works(t, openAt(t, root), filepath.Join(gd, "petalbook-state"))
	if _, err := os.Stat(filepath.Join(gd, "info", "exclude")); err != nil {
		t.Log("no info/exclude:", err)
	}
}

func TestAGitLinkToAFolderOpens(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := filepath.Join(t.TempDir(), "space")
	firstCommit(t, root)
	real := filepath.Join(t.TempDir(), "real.git")
	if err := os.Rename(filepath.Join(root, ".git"), real); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(root, ".git")); err != nil {
		t.Fatal(err)
	}
	works(t, openAt(t, root), filepath.Join(real, "petalbook-state"))
	if !isLink(root, ".git") {
		t.Error("the link was replaced")
	}
}

func TestAGivenStateDirectoryWithAGitFile(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := filepath.Join(t.TempDir(), "space")
	gd := filepath.Join(t.TempDir(), "repo.git")
	git(t, t.TempDir(), "init", "-q", "--separate-git-dir", gd, root)
	state := filepath.Join(t.TempDir(), "state")
	works(t, openAt(t, root, func(o *Options) { o.StateDir = state }), state)
	if _, err := os.Lstat(filepath.Join(gd, "petalbook-state")); err == nil {
		t.Error("state was made in the Git directory too")
	}
}

func TestAGitFileThatLeadsNowhereIsRefused(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /nonexistent/x.git\n"), 0o644)
	if s, err := Open(Options{Root: root}); err == nil {
		s.Close()
		t.Fatal("a space with no Git directory was opened")
	}
	if b, _ := os.ReadFile(filepath.Join(root, ".git")); string(b) != "gitdir: /nonexistent/x.git\n" {
		t.Errorf(".git was changed: %q", b)
	}
}

// --- files of the repository where an earlier version kept its state ----------------------

const otherJournal = `{"op":"t","p":"Other.md"}` + "\n"

// committedCache puts into .petalbook/cache of dir files that are committed:
// a note and another machine's journal.
func committedCache(t *testing.T, dir string) {
	t.Helper()
	c := filepath.Join(dir, ".petalbook", "cache")
	os.MkdirAll(c, 0o755)
	os.WriteFile(filepath.Join(c, "notes.md"), []byte("mine\n"), 0o644)
	os.WriteFile(filepath.Join(c, "pending.journal"), []byte(otherJournal), 0o644)
	gitO(t, dir, "add", "-f", ".petalbook/cache/notes.md", ".petalbook/cache/pending.journal")
	gitO(t, dir, "commit", "-qm", "cache files")
}

func cacheKept(t *testing.T, root string) {
	t.Helper()
	for rel, want := range map[string]string{"notes.md": "mine\n", "pending.journal": otherJournal} {
		if b, err := os.ReadFile(filepath.Join(root, ".petalbook", "cache", rel)); err != nil || string(b) != want {
			t.Errorf(".petalbook/cache/%s: %q %v", rel, b, err)
		}
	}
	if st := git(t, root, "status", "--porcelain", "--untracked-files=no"); st != "" {
		t.Errorf("a change nobody made:\n%s", st)
	}
}

func TestCommittedFilesWhereTheOldStateWasAreNeitherMovedNorUsed(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	mustCreate(t, s, "", meta("title", "Kept"), "x\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	committedCache(t, root)
	// The server's own files of an earlier version beside them, untracked.
	old := filepath.Join(root, ".petalbook", "cache")
	os.WriteFile(filepath.Join(old, "state.db"), []byte("drafts"), 0o644)
	os.WriteFile(filepath.Join(root, "Other.md"), []byte(pageText("OTHERPAGE1", "Other", "o\n")), 0o644)
	os.RemoveAll(DefaultStateDir(root))

	s = openAt(t, root)
	defer s.Close()
	cacheKept(t, root)
	if b, _ := os.ReadFile(filepath.Join(s.StateDir(), "state.db")); string(b) != "drafts" {
		t.Errorf("the untracked state database was not moved: %q", b)
	}
	if _, err := os.Lstat(filepath.Join(old, "state.db")); err == nil {
		t.Error("the untracked state database is still in the old place")
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if files := git(t, root, "ls-files"); strings.Contains(files, "Other.md") {
		t.Errorf("the committed journal was replayed:\n%s", files)
	}
	cacheKept(t, root)
}

func TestCommittedFilesWhereTheOldStateWasDoNotBlockARemoteSpace(t *testing.T) {
	t.Parallel()
	f := newRemoteSpace(t)
	mustCreate(t, f.s, "", meta("title", "Mine"), "x\n")
	o := f.theirs()
	committedCache(t, o.dir)
	gitO(t, o.dir, "push", "-q", "origin", "main")
	if err := f.s.Pull(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	cacheKept(t, f.root)
	os.WriteFile(filepath.Join(f.root, "Other.md"), []byte(pageText("OTHERPAGE1", "Other", "o\n")), 0o644)
	// Opened again with no state of its own (as after the upgrade).
	f.s.Close()
	os.RemoveAll(DefaultStateDir(f.root))
	opt := f.options()
	opt.Status = RemoteStatus{LastGood: f.tip()}
	f.s = openAt(t, f.root, func(o *Options) {
		o.Committer = "Service <svc@example.test>"
		o.Author = "Ann <ann@example.test>"
		o.Remote = &opt
	})
	if st := f.last(); st.State == RemoteError {
		t.Fatalf("the space is blocked: %+v", st)
	}
	cacheKept(t, f.root)
	if _, err := f.s.Create(Author{}, CreateInput{Meta: meta("title", "After"), Body: "y\n"}); err != nil {
		t.Fatal(err)
	}
	if files := git(t, f.root, "ls-files"); strings.Contains(files, "Other.md") {
		t.Errorf("the committed journal was replayed:\n%s", files)
	}
}

// --- an old state folder beside the current one ---------------------------------------------

func TestAnOldStateBesideTheCurrentOneIsTakenOverAndNothingIsLost(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	p := mustCreate(t, s, "", meta("title", "Kept"), "x\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	state := DefaultStateDir(root)
	os.WriteFile(filepath.Join(state, "state.db"), []byte("current"), 0o644)
	// An earlier version ran after this one: its own state, with a change it
	// did not commit, drafts and notices.
	old := filepath.Join(root, ".petalbook", "cache")
	os.MkdirAll(old, 0o755)
	os.WriteFile(filepath.Join(root, "Late.md"), []byte(pageText("LATEPAGE01", "Late", "l\n")), 0o644)
	os.WriteFile(filepath.Join(old, "pending.journal"), []byte(`{"op":"t","p":"Late.md"}`), 0o644)
	os.WriteFile(filepath.Join(old, "state.db"), []byte("earlier"), 0o644)
	os.WriteFile(filepath.Join(old, "state.db-wal"), []byte("earlier-wal"), 0o644)
	os.WriteFile(filepath.Join(old, "settings-restored.json"), []byte("[]"), 0o644)
	os.WriteFile(filepath.Join(old, "index.db"), []byte("stale"), 0o644)

	s = openAt(t, root)
	defer s.Close()
	if _, err := os.Lstat(old); err == nil {
		t.Errorf(".petalbook/cache is still there")
	}
	if b, _ := os.ReadFile(filepath.Join(state, "state.db")); string(b) != "current" {
		t.Errorf("the current state database: %q", b)
	}
	dbs := s.EarlierStateDBs()
	if len(dbs) != 1 {
		t.Fatalf("earlier state databases: %v", dbs)
	}
	if b, _ := os.ReadFile(dbs[0]); string(b) != "earlier" {
		t.Errorf("the earlier state database: %q", b)
	}
	if b, _ := os.ReadFile(dbs[0] + "-wal"); string(b) != "earlier-wal" {
		t.Errorf("its log: %q", b)
	}
	if m, _ := filepath.Glob(filepath.Join(state, "settings-restored-earlier-*.json")); len(m) != 1 {
		t.Errorf("the earlier notices: %v", m)
	}
	if _, err := s.Get(p.ID); err != nil {
		t.Error(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if files := git(t, root, "ls-files"); !strings.Contains(files, "Late.md") {
		t.Errorf("the earlier journal was not replayed:\n%s", files)
	}
	if st := git(t, root, "status", "--porcelain", "--untracked-files=all"); st != "" {
		t.Errorf("not all committed:\n%s", st)
	}
}
