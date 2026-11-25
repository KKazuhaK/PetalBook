package gitstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A commit of a few files in a large repository is built from the folders
// it changes: no command reads or writes the whole index (read-tree,
// write-tree, diff --cached, add), and the number of git commands does not
// grow with the repository.
func TestACommitIsBuiltFromTheChangedFoldersOnly(t *testing.T) {
	t.Parallel()
	needGit(t)
	k := &counter{}
	r := newClone(t, bareRemote(t), nil)
	var all []string
	for i := 0; i < 300; i++ {
		p := fmt.Sprintf("d%02d/%03d.md", i%30, i)
		write(t, r.Dir(), p, fmt.Sprintf("page %d\n", i))
		all = append(all, p)
	}
	commit(t, r, all...)
	r.opt.Runner = k
	write(t, r.Dir(), "d03/003.md", "edited\n")
	write(t, r.Dir(), "new/deep/x.md", "new\n")
	os.Remove(filepath.Join(r.Dir(), "d04", "004.md"))
	k.reset()
	commit(t, r, "d03/003.md", "new/deep/x.md", "d04/004.md")
	for _, sub := range []string{"read-tree", "write-tree", "diff", "add", "ls-files", "rm"} {
		if n := k.count(sub); n != 0 {
			t.Errorf("%d %s commands", n, sub)
		}
	}
	if n := len(k.calls); n > 8 {
		t.Errorf("%d git commands for a commit: %v", n, k.calls)
	}
	// What is committed is what git itself makes of it.
	if got := sh(t, r.Dir(), "show", "HEAD:d03/003.md"); got != "edited" {
		t.Errorf("d03/003.md: %q", got)
	}
	if got := sh(t, r.Dir(), "show", "HEAD:new/deep/x.md"); got != "new" {
		t.Errorf("new/deep/x.md: %q", got)
	}
	tree := sh(t, r.Dir(), "ls-tree", "-r", "--name-only", "HEAD")
	if strings.Contains(tree, "d04/004.md") || strings.Count(tree, "\n")+1 != 300 {
		t.Errorf("the tree:\n%s", tree)
	}
	if st := sh(t, r.Dir(), "status", "--porcelain"); st != "" {
		t.Errorf("the index and work tree do not agree with the commit: %q", st)
	}
	if fsck := sh(t, r.Dir(), "fsck", "--no-progress", "--no-dangling"); fsck != "" {
		t.Errorf("fsck: %s", fsck)
	}
}

// A folder whose last file goes is gone from the tree, a file put where a
// folder was replaces it, and a page moved between folders is a move in the
// commit message, as git's own rename detection made it.
func TestACommitBuiltFromFoldersHandlesFoldersComingAndGoing(t *testing.T) {
	t.Parallel()
	needGit(t)
	r := newClone(t, bareRemote(t), nil)
	write(t, r.Dir(), "only/a.md", "a\n")
	write(t, r.Dir(), "x/b.md", "b\n")
	write(t, r.Dir(), "Old/Moved.md", "moved page\n")
	commit(t, r, "only/a.md", "x/b.md", "Old/Moved.md")
	os.RemoveAll(filepath.Join(r.Dir(), "only"))
	os.RemoveAll(filepath.Join(r.Dir(), "x"))
	write(t, r.Dir(), "x", "now a file\n")
	os.MkdirAll(filepath.Join(r.Dir(), "New"), 0o755)
	os.Rename(filepath.Join(r.Dir(), "Old", "Moved.md"), filepath.Join(r.Dir(), "New", "Moved.md"))
	var msg []Change
	if _, err := r.CommitPaths(context.Background(), []string{"only/a.md", "x/b.md", "x", "Old/Moved.md", "New/Moved.md"},
		func(cs []Change) string { msg = cs; return "m" }, Identity{"a", "a@example.test"}, Identity{"a", "a@example.test"}); err != nil {
		t.Fatal(err)
	}
	if tree := sh(t, r.Dir(), "ls-tree", "-r", "--name-only", "HEAD"); tree != "New/Moved.md\nx" {
		t.Errorf("the tree:\n%s", tree)
	}
	moved := false
	for _, c := range msg {
		moved = moved || (c.Status == 'R' && c.Old == "Old/Moved.md" && c.New == "New/Moved.md")
	}
	if !moved {
		t.Errorf("the changes: %+v", msg)
	}
	if st := sh(t, r.Dir(), "status", "--porcelain"); st != "" {
		t.Errorf("status: %q", st)
	}
}

// A new file that is executable on disk is committed executable, as git add
// would commit it.
func TestANewExecutableFileIsCommittedExecutable(t *testing.T) {
	t.Parallel()
	needGit(t)
	r := newClone(t, bareRemote(t), nil)
	write(t, r.Dir(), "a.md", "a\n")
	commit(t, r, "a.md")
	write(t, r.Dir(), "run.sh", "echo\n")
	if err := os.Chmod(filepath.Join(r.Dir(), "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	commit(t, r, "run.sh")
	if got := sh(t, r.Dir(), "ls-tree", "HEAD", "run.sh"); !strings.HasPrefix(got, "100755 ") {
		t.Errorf("committed as %q", got)
	}
}

// A file whose attributes have git change it on the way in (here line ends)
// is committed as git add commits it.
func TestAFileWithAttributesIsCommittedAsGitWould(t *testing.T) {
	t.Parallel()
	needGit(t)
	r := newClone(t, bareRemote(t), nil)
	write(t, r.Dir(), ".gitattributes", "*.md text\n")
	write(t, r.Dir(), "a.md", "a\n")
	commit(t, r, ".gitattributes", "a.md")
	write(t, r.Dir(), "a.md", "one\r\ntwo\r\n")
	commit(t, r, "a.md")
	if got := sh(t, r.Dir(), "cat-file", "-p", "HEAD:a.md"); strings.Contains(got, "\r") {
		t.Errorf("committed with CR: %q", got)
	}
}
