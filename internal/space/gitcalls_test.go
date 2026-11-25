package space

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"petalbook.app/server/internal/gitstore"
)

var (
	watchOnce sync.Once
	watching  sync.Map // directory -> *[]string, the commands run there
	watchMu   sync.Mutex
)

// watchGit records the commands run in dir by any space of the process.
func watchGit(dir string) *[]string {
	watchOnce.Do(func() {
		f := func(d string, args []string) {
			if v, ok := watching.Load(d); ok {
				sub, _ := gitstore.Subcommand(args)
				watchMu.Lock()
				*v.(*[]string) = append(*v.(*[]string), sub)
				watchMu.Unlock()
			}
		}
		gitObserver.Store(&f)
	})
	l := &[]string{}
	watching.Store(dir, l)
	return l
}

func countOf(l *[]string, sub string) (n int) {
	watchMu.Lock()
	defer watchMu.Unlock()
	for _, s := range *l {
		if s == sub {
			n++
		}
	}
	return n
}

// Making a space and its first commit asks git one question: HEAD and its tree,
// in a single rev-parse. Where the git directory is is known from init, so it is
// never asked. (The commands that write the commit are the commit path's own
// business and are not pinned here.)
func TestCreatingASpaceAndCommittingRunsFewGitCommands(t *testing.T) {
	t.Parallel()
	needGit(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cmds := watchGit(root)
	s := openAt(t, root)
	mustCreate(t, s, "", meta("title", "First"), "x\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	watchMu.Lock()
	got := strings.Join(*cmds, " ")
	watchMu.Unlock()
	if !strings.HasPrefix(got, "init ") || countOf(cmds, "init") != 1 || countOf(cmds, "rev-parse") != 1 || countOf(cmds, "commit-tree") != 1 {
		t.Fatalf("want one init, one rev-parse and one commit; git ran: %s", got)
	}
}

// An existing repository is asked once for where its git directory is.
func TestOpeningAnExistingSpaceAsksForTheGitDirOnce(t *testing.T) {
	t.Parallel()
	needGit(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	firstCommit(t, root)
	cmds := watchGit(root)
	s := openAt(t, root)
	mustCreate(t, s, "", meta("title", "Second"), "x\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if n := countOf(cmds, "rev-parse"); n != 2 { // the git directory, then HEAD and its tree
		t.Fatalf("%d rev-parse commands: %v", n, *cmds)
	}
}

func TestResolveGitDirsRefusesWhatHasNoGitDirOfItsOwn(t *testing.T) {
	t.Parallel()
	needGit(t)
	top, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		setup func(dir string)
		want  string
	}{
		{"a folder that is not a repository", func(string) {}, "has no Git directory that can be used"},
		{"a missing folder", func(dir string) { os.RemoveAll(dir) }, "has no Git directory that can be used"},
		{"a folder inside a repository", func(dir string) { git(t, filepath.Dir(dir), "init", "-q", "-b", "main", ".") }, "no Git directory of its own"},
	} {
		dir := filepath.Join(top, strings.ReplaceAll(c.name, " ", "-"), "space")
		os.MkdirAll(dir, 0o755)
		c.setup(dir)
		_, err := resolveGitDirs(gitstore.New(gitstore.Options{Dir: dir}), dir)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	// And a repository of its own is found, with its git directory real.
	dir := filepath.Join(top, "own")
	git(t, top, "init", "-q", "-b", "main", dir)
	d, err := resolveGitDirs(gitstore.New(gitstore.Options{Dir: dir}), dir)
	if err != nil || d.git != filepath.Join(dir, ".git") || d.common != d.git {
		t.Errorf("%+v %v", d, err)
	}
}
