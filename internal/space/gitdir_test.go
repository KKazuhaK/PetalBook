package space

import (
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
