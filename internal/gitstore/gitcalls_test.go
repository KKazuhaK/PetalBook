package gitstore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// counter is a Runner that records the arguments of every command and passes
// it on, optionally letting a test change what git printed.
type counter struct {
	mu    sync.Mutex
	calls [][]string
	edit  func(c Cmd, out string) string
}

func (k *counter) Run(ctx context.Context, c Cmd) (string, string, error) {
	k.mu.Lock()
	k.calls = append(k.calls, append([]string{}, c.Args...))
	k.mu.Unlock()
	out, errOut, err := rec.Run(ctx, c)
	if k.edit != nil && err == nil {
		out = k.edit(c, out)
	}
	return out, errOut, err
}

func (k *counter) count(sub string) (n int) {
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, a := range k.calls {
		if s, _ := subcommand(a); s == sub {
			n++
		}
	}
	return n
}

func (k *counter) reset() {
	k.mu.Lock()
	k.calls = nil
	k.mu.Unlock()
}

func (k *counter) revParses() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	var out []string
	for _, a := range k.calls {
		if s, _ := subcommand(a); s == "rev-parse" {
			out = append(out, strings.Join(a, " "))
		}
	}
	return out
}

// One commit on a repository with history asks git for HEAD's commit and tree
// once, in one command.
func TestCommitAsksForHeadAndItsTreeInOneCommand(t *testing.T) {
	t.Parallel()
	needGit(t)
	k := &counter{}
	r := newClone(t, bareRemote(t), nil)
	r.opt.Runner = k
	write(t, r.Dir(), "a.md", "a\n")
	commit(t, r, "a.md")
	write(t, r.Dir(), "a.md", "a2\n")
	k.reset()
	commit(t, r, "a.md")
	got := k.revParses()
	if len(got) != 1 || !strings.HasSuffix(got[0], "rev-parse --revs-only HEAD^{commit} HEAD^{tree}") {
		t.Fatalf("rev-parse calls: %q", got)
	}
}

// A repository with no commit yet is not a failure: the commit is a root commit.
func TestCommitOnAnUnbornBranchIsARootCommit(t *testing.T) {
	t.Parallel()
	needGit(t)
	r := newClone(t, bareRemote(t), nil)
	write(t, r.Dir(), "a.md", "a\n")
	commit(t, r, "a.md")
	if got := sh(t, r.Dir(), "rev-list", "--count", "HEAD"); got != "1" {
		t.Fatalf("%s commits", got)
	}
	if got := sh(t, r.Dir(), "rev-list", "--max-parents=0", "HEAD"); got != r.Head(context.Background()) {
		t.Fatalf("not a root commit: %s", got)
	}
}

// A HEAD that has a commit but whose tree git cannot give is an error, not
// "no commit yet": nothing is committed on top of it.
func TestCommitFailsWhenHeadHasACommitButNoTree(t *testing.T) {
	t.Parallel()
	needGit(t)
	k := &counter{}
	r := newClone(t, bareRemote(t), nil)
	write(t, r.Dir(), "a.md", "a\n")
	commit(t, r, "a.md")
	before := r.Head(context.Background())
	r.opt.Runner = k
	k.edit = func(c Cmd, out string) string {
		if s, _ := subcommand(c.Args); s == "rev-parse" && strings.Contains(strings.Join(c.Args, " "), "--revs-only") {
			l, _, _ := strings.Cut(out, "\n")
			return l + "\n"
		}
		return out
	}
	write(t, r.Dir(), "a.md", "a2\n")
	if made, err := commitErr(r, "a.md"); err == nil || made {
		t.Fatalf("made=%v err=%v", made, err)
	}
	if k.count("commit-tree") != 0 || r.Head(context.Background()) != before {
		t.Fatal("a commit was made")
	}
}

// And with the tree object really gone, the commit fails too.
func TestCommitFailsWhenTheTreeOfHeadIsMissing(t *testing.T) {
	t.Parallel()
	needGit(t)
	r := newClone(t, bareRemote(t), nil)
	write(t, r.Dir(), "a.md", "a\n")
	commit(t, r, "a.md")
	tr := sh(t, r.Dir(), "rev-parse", "HEAD^{tree}")
	if err := os.Remove(filepath.Join(r.Dir(), ".git", "objects", tr[:2], tr[2:])); err != nil {
		t.Skip("objects are packed")
	}
	write(t, r.Dir(), "a.md", "a2\n")
	if made, err := commitErr(r, "a.md"); err == nil || made {
		t.Fatalf("made=%v err=%v", made, err)
	}
}

// The git directory is not asked for again after Init, nor by IsRepo and the
// other readers of the layout once one of them has asked.
func TestGitDirIsKnownAfterInitAndAskedForOnce(t *testing.T) {
	t.Parallel()
	needGit(t)
	ctx := context.Background()
	k := &counter{}
	dir := filepath.Join(t.TempDir(), "r")
	r := New(Options{Dir: dir, Runner: k})
	if err := r.Init(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	k.reset()
	l, err := r.Layout(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := filepath.EvalSymlinks(filepath.Join(dir, ".git")); l.GitDir != want || l.Prefix != "" {
		t.Fatalf("%+v", l)
	}
	if _, err := r.absGitDir(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(k.revParses()); n != 0 {
		t.Fatalf("%d rev-parse calls after Init", n)
	}
	if want := sh(t, dir, "rev-parse", "--absolute-git-dir"); l.GitDir != want {
		t.Fatalf("%s, git says %s", l.GitDir, want)
	}

	// A repository this process did not make: IsRepo asks once, with all three
	// flags, and the git directory is then known.
	dir2 := filepath.Join(t.TempDir(), "o")
	sh(t, t.TempDir(), "init", "-q", dir2)
	k2 := &counter{}
	r2 := New(Options{Dir: dir2, Runner: k2})
	if !r2.IsRepo(ctx) {
		t.Fatal("not a repo")
	}
	if _, err := r2.absGitDir(ctx); err != nil {
		t.Fatal(err)
	}
	if got := k2.revParses(); len(got) != 1 || !strings.HasSuffix(got[0], "rev-parse --is-inside-work-tree --absolute-git-dir --show-prefix") {
		t.Fatalf("%q", got)
	}
}

// IsRepo is asked afresh every time: a directory can stop being a repository.
func TestIsRepoFollowsTheDirectory(t *testing.T) {
	t.Parallel()
	needGit(t)
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "r")
	r := New(Options{Dir: dir})
	if r.IsRepo(ctx) {
		t.Fatal("a missing directory is a repo")
	}
	if err := r.Init(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	if !r.IsRepo(ctx) {
		t.Fatal("not a repo after Init")
	}
	if err := os.RemoveAll(filepath.Join(dir, ".git")); err != nil {
		t.Fatal(err)
	}
	if r.IsRepo(ctx) {
		t.Fatal("still a repo with its .git gone")
	}
}
