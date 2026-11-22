package gitstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Only the commands that build a commit read names exactly; the ones that
// move the work tree (fast-forward, reset, checkout) and look at it (status)
// keep the repository's own setting, or a rename by case coming from the
// remote could never be fast-forwarded on a disk that ignores case.
func TestIgnoreCaseIsOffOnlyWhileACommitIsBuilt(t *testing.T) {
	sha := strings.Repeat("a", 40)
	off := func(args ...string) bool {
		return slices.Contains(commandLine("d", args), "core.ignorecase=false")
	}
	for _, args := range [][]string{
		{"add", "-A", "--pathspec-from-file=-", "--pathspec-file-nul"},
		{"rm", "--cached", "-r", "-q", "--ignore-unmatch", "--pathspec-from-file=-", "--pathspec-file-nul"},
		{"diff", "--cached", "--name-status", "-z", "-M"},
		{"ls-files", "-s", "-z", "--", "a.md"},
		{"update-index", "-z", "--index-info"},
		{"read-tree", "HEAD"},
		{"write-tree"},
		{"-c", "commit.gpgsign=false", "commit-tree", sha, "-F", "-"},
	} {
		if !off(args...) {
			t.Errorf("git %v runs with the repository's ignorecase", args)
		}
	}
	for _, args := range [][]string{
		{"merge", "--quiet", "--ff-only", "refs/remotes/origin/main"},
		{"reset", "--quiet", "--hard", sha},
		{"checkout", "--quiet", "--pathspec-from-file=-", "--pathspec-file-nul"},
		{"fetch", "--quiet", "--no-tags", "origin", "main"},
		{"status", "--porcelain", "-z", "--untracked-files=no"},
		{"update-ref", "HEAD", sha, strings.Repeat("b", 40)},
	} {
		if off(args...) {
			t.Errorf("git %v runs with ignorecase off", args)
		}
	}
}

// Somebody renames a file and a folder by case on the remote. Fetching and
// fast-forwarding to it works, the clone ends with the new names only, and
// the next local change commits on top of them. On a disk that ignores case
// this needs the merge to know it does; on any other the test is a plain
// rename.
func TestFastForwardOverARemoteRenameByCase(t *testing.T) {
	needGit(t)
	ctx := context.Background()
	remote := bareRemote(t)
	r := newClone(t, remote, nil)
	write(t, r.Dir(), "Apple.md", "fruit\n")
	write(t, r.Dir(), "notes/One.md", "one\n")
	commit(t, r, "Apple.md", "notes/One.md")
	if err := r.Push(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	o := other(t, remote)
	// Through a third name: Linux does not respell an entry in one rename.
	for _, mv := range [][2]string{{"Apple.md", "apple.md"}, {"notes", "Notes"}} {
		sh(t, o, "mv", mv[0], "tmp")
		sh(t, o, "mv", "tmp", mv[1])
	}
	sh(t, o, "commit", "-qm", "rename by case")
	sh(t, o, "push", "-q", "origin", "main")

	if err := r.Fetch(ctx, "main", false); err != nil {
		t.Fatal(err)
	}
	if err := r.FastForward(ctx, "main"); err != nil {
		t.Fatalf("the fast-forward over a rename by case: %v", err)
	}
	if got := tree(t, r); got != "Notes/One.md\napple.md" {
		t.Fatalf("HEAD has %q", got)
	}
	names := exactNames{root: r.Dir(), dirs: map[string]dirNames{}}
	for _, p := range []string{"apple.md", "Notes/One.md"} {
		if e, err := names.entry(p); err != nil || e == nil {
			t.Fatalf("%s is not on disk under that name: %v", p, err)
		}
	}
	if mod, err := r.ModifiedTracked(ctx); err != nil || len(mod) != 0 {
		t.Fatalf("after the fast-forward: %v %v", mod, err)
	}
	write(t, r.Dir(), "apple.md", "fruit, edited here\n")
	commit(t, r, "apple.md")
	if err := r.Push(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	if got := tree(t, r); got != "Notes/One.md\napple.md" {
		t.Fatalf("HEAD has %q", got)
	}
}

// A commit reads the files of its change and no other: every other file of
// the space can be unreadable and look changed, and the commit still goes
// through and leaves them as they were.
func TestCommitReadsNoOtherFile(t *testing.T) {
	needGit(t)
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	ctx := context.Background()
	r := newClone(t, bareRemote(t), nil)
	var others []string
	for i := 0; i < 30; i++ {
		p := fmt.Sprintf("d%d/other%02d.md", i%3, i)
		write(t, r.Dir(), p, "other\n")
		others = append(others, p)
	}
	write(t, r.Dir(), "mine.md", "v1\n")
	commit(t, r, append([]string{"mine.md"}, others...)...)
	later := time.Now().Add(time.Hour)
	for _, p := range others {
		full := filepath.Join(r.Dir(), filepath.FromSlash(p))
		// A different time makes Git think the file may have changed, so
		// anything that refreshes the index would have to open it.
		if err := os.Chtimes(full, later, later); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(full, 0o000); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		for _, p := range others {
			os.Chmod(filepath.Join(r.Dir(), filepath.FromSlash(p)), 0o644)
		}
	}()
	if _, err := os.ReadFile(filepath.Join(r.Dir(), filepath.FromSlash(others[0]))); err == nil {
		t.Skip("this file system does not keep a file from being read")
	}
	write(t, r.Dir(), "mine.md", "v2\n")
	write(t, r.Dir(), "new.md", "new\n")
	if made, err := commitErr(r, "mine.md", "new.md"); err != nil || !made {
		t.Fatalf("made=%v err=%v", made, err)
	}
	if got := sh(t, r.Dir(), "show", "HEAD:mine.md"); got != "v2" {
		t.Fatalf("%q", got)
	}
	if got := sh(t, r.Dir(), "diff", "--name-only", "HEAD~1", "HEAD"); got != "mine.md\nnew.md" {
		t.Fatalf("the commit touched %q", got)
	}
	if r.Head(ctx) == "" {
		t.Fatal("no HEAD")
	}
}

// A change of more names than one command line takes is looked up in the
// index a part at a time, never by listing the whole index.
func TestCommitOfManyPathsAsksForThemInParts(t *testing.T) {
	needGit(t)
	r := newClone(t, bareRemote(t), nil)
	var paths []string
	for i := 0; i < 2*maxArgPaths+50; i++ {
		p := fmt.Sprintf("many/%03d.md", i)
		write(t, r.Dir(), p, fmt.Sprintf("v1 %d\n", i))
		paths = append(paths, p)
	}
	commit(t, r, paths...)
	for i, p := range paths {
		write(t, r.Dir(), p, fmt.Sprintf("v2 %d\n", i))
	}
	before := len(rec.all())
	commit(t, r, paths...)
	var lists int
	for _, c := range rec.all()[before:] {
		if c.Dir != r.Dir() {
			continue
		}
		sub, rest := subcommand(c.Args)
		if sub != "ls-files" {
			continue
		}
		lists++
		i := slices.Index(rest, "--")
		if i < 0 || len(rest)-i-1 == 0 || len(rest)-i-1 > maxArgPaths {
			t.Fatalf("ls-files with %v", rest)
		}
	}
	if want := (len(paths) + maxArgPaths - 1) / maxArgPaths; lists != want {
		t.Fatalf("%d ls-files commands, want %d", lists, want)
	}
	if got := sh(t, r.Dir(), "diff", "--name-only", "HEAD~1", "HEAD"); strings.Count(got, "\n")+1 != len(paths) {
		t.Fatalf("the commit has %d files", strings.Count(got, "\n")+1)
	}
	if out := sh(t, r.Dir(), "status", "--porcelain"); out != "" {
		t.Fatalf("%q", out)
	}
}

// A repository set to sign its commits is still committed to: there is no
// key here, and a commit that keeps failing would hold up every later one.
func TestCommitIgnoresTheRepositorysSigningSetting(t *testing.T) {
	needGit(t)
	r := newClone(t, bareRemote(t), nil)
	sh(t, r.Dir(), "config", "commit.gpgsign", "true")
	sh(t, r.Dir(), "config", "gpg.program", "false")
	write(t, r.Dir(), "a.md", "a\n")
	commit(t, r, "a.md")
	write(t, r.Dir(), "a.md", "b\n")
	commit(t, r, "a.md")
	if got := sh(t, r.Dir(), "rev-list", "--count", "HEAD"); got != "2" {
		t.Fatalf("%s commits", got)
	}
}

// The branch moves only from the commit the new one was built on. When a
// person commits while the server's commit is being made, the server's fails,
// theirs stays, and the change goes in on top of it the next time.
func TestCommitDoesNotOverwriteABranchThatMoved(t *testing.T) {
	needGit(t)
	ctx := context.Background()
	r := newClone(t, bareRemote(t), nil)
	write(t, r.Dir(), "a.md", "a\n")
	commit(t, r, "a.md")
	var theirs string
	r.opt.Runner = hook{func(c Cmd) {
		if sub, _ := subcommand(c.Args); sub == "commit-tree" && theirs == "" {
			write(t, r.Dir(), "b.md", "b\n")
			sh(t, r.Dir(), "add", "b.md")
			sh(t, r.Dir(), "commit", "-qm", "theirs", "--", "b.md")
			theirs = r.Head(ctx)
		}
	}}
	write(t, r.Dir(), "a.md", "a2\n")
	if made, err := commitErr(r, "a.md"); err == nil || made {
		t.Fatalf("made=%v err=%v", made, err)
	}
	if r.Head(ctx) != theirs {
		t.Fatal("the branch lost the other commit")
	}
	commit(t, r, "a.md")
	if got := sh(t, r.Dir(), "log", "--format=%s", "-n", "2"); got != "change of 1\ntheirs" {
		t.Fatalf("%q", got)
	}
	if got := tree(t, r); got != "a.md\nb.md" {
		t.Fatalf("HEAD has %q", got)
	}
}

// hook runs a function before every command, then the real git.
type hook struct{ before func(Cmd) }

func (h hook) Run(ctx context.Context, c Cmd) (string, string, error) {
	h.before(c)
	return rec.Run(ctx, c)
}

// Nothing to commit when nothing of the change is staged differently from
// HEAD: no empty commit.
func TestUnchangedPathMakesNoCommit(t *testing.T) {
	needGit(t)
	ctx := context.Background()
	r := newClone(t, bareRemote(t), nil)
	write(t, r.Dir(), "a.md", "a\n")
	commit(t, r, "a.md")
	head := r.Head(ctx)
	write(t, r.Dir(), "a.md", "b\n")
	write(t, r.Dir(), "a.md", "a\n")
	if made, err := commitErr(r, "a.md"); err != nil || made {
		t.Fatalf("made=%v err=%v", made, err)
	}
	if r.Head(ctx) != head {
		t.Fatal("an empty commit was made")
	}
}

// Rename detection may pair a path of the change with a file a person
// staged. Only the change's side is committed; the person's stays staged.
func TestRenameDetectionDoesNotTakeAPersonsStagedFile(t *testing.T) {
	needGit(t)
	body := strings.Repeat("the same template, line by line\n", 20)
	t.Run("their deletion, our new file", func(t *testing.T) {
		r := newClone(t, bareRemote(t), nil)
		write(t, r.Dir(), "theirs.md", body)
		commit(t, r, "theirs.md")
		sh(t, r.Dir(), "rm", "-q", "--cached", "theirs.md")
		write(t, r.Dir(), "new.md", body)
		commit(t, r, "new.md")
		if got := tree(t, r); got != "new.md\ntheirs.md" {
			t.Fatalf("HEAD has %q", got)
		}
		if out := sh(t, r.Dir(), "status", "--porcelain"); out != "D  theirs.md\n?? theirs.md" && out != "D  theirs.md" {
			t.Fatalf("the person's staged deletion: %q", out)
		}
	})
	t.Run("our deletion, their new file", func(t *testing.T) {
		r := newClone(t, bareRemote(t), nil)
		write(t, r.Dir(), "page.md", body)
		commit(t, r, "page.md")
		write(t, r.Dir(), "theirs.md", body)
		sh(t, r.Dir(), "add", "theirs.md")
		os.Remove(filepath.Join(r.Dir(), "page.md"))
		commit(t, r, "page.md")
		if got := tree(t, r); got != "" {
			t.Fatalf("HEAD has %q", got)
		}
		if out := sh(t, r.Dir(), "status", "--porcelain"); out != "A  theirs.md" {
			t.Fatalf("the person's staged file: %q", out)
		}
	})
}

// Two names that differ only in Unicode form are two files on a disk that
// does not normalise names. A change naming one that does not exist (made
// and removed before it was committed) is a deletion, not a failure, even
// with the other spelling right next to it.
func TestUnicodeFormsAreDifferentNamesWhereTheDiskSaysSo(t *testing.T) {
	needGit(t)
	nfc, nfd := "caf\u00e9.md", "cafe\u0301.md"
	r := newClone(t, bareRemote(t), nil)
	write(t, r.Dir(), nfd, "decomposed\n")
	if _, err := os.Stat(filepath.Join(r.Dir(), nfc)); err == nil {
		t.Skip("this file system takes the two forms for one name")
	}
	commit(t, r, nfd)
	made, err := commitErr(r, nfc)
	if err != nil || made {
		t.Fatalf("made=%v err=%v", made, err)
	}
	write(t, r.Dir(), nfd, "decomposed, edited\n")
	commit(t, r, nfd, nfc)
	if got := sh(t, r.Dir(), "-c", "core.quotepath=off", "ls-tree", "--name-only", "HEAD"); got != nfd {
		t.Fatalf("HEAD has %q", got)
	}
}

// A crash during a commit leaves its index directory behind in .git; the
// next commit clears it once it is old.
func TestLeftoverCommitIndexesAreCleared(t *testing.T) {
	needGit(t)
	r := newClone(t, bareRemote(t), nil)
	gd := filepath.Join(r.Dir(), ".git")
	old := filepath.Join(gd, tempIndexPrefix+"crashed")
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	long := time.Now().Add(-2 * time.Hour)
	os.Chtimes(old, long, long)
	write(t, r.Dir(), "a.md", "a\n")
	commit(t, r, "a.md")
	if left, _ := filepath.Glob(filepath.Join(gd, tempIndexPrefix+"*")); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
}
