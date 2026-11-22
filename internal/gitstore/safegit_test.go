package gitstore

// The promise (docs/design.md 4.1): PetalBook never force-pushes, deletes or
// rewrites anything on the remote. These tests prove it three ways:
//
//  1. TestMain routes every git command of this package's whole test suite
//     through a recorder and fails the run if any recorded command is
//     destructive, judged by a checker written independently of safegit.go;
//  2. unit tests of the checker and of the runtime guard in safegit.go;
//  3. static tests that keep the dangerous words, and every push command line,
//     out of every other non-test source file of the application.

import (
	"context"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	rec = &recorder{next: execRunner{}}
	defaultRunner = rec
	code := m.Run()
	if bad := destructive(rec.all()); len(bad) > 0 {
		fmt.Fprintln(os.Stderr, "FAIL: destructive git commands were run:")
		for _, b := range bad {
			fmt.Fprintln(os.Stderr, "  ", b)
		}
		code = 1
	}
	os.Exit(code)
}

func destructive(cmds []Cmd) []string {
	var out []string
	for _, c := range cmds {
		for _, why := range problems(c) {
			out = append(out, why+": git "+strings.Join(c.Args, " "))
		}
	}
	return out
}

func problems(c Cmd) []string {
	var out []string
	sub, rest := "", []string(nil)
	for i := 0; i < len(c.Args); i++ {
		a := c.Args[i]
		if a == "-c" || a == "-C" || a == "--git-dir" || a == "--work-tree" || a == "--namespace" || a == "--config-env" {
			i++
			continue
		}
		if !strings.HasPrefix(a, "-") {
			sub, rest = a, c.Args[i+1:]
			break
		}
	}
	for _, a := range c.Args {
		if strings.HasPrefix(a, "--force") || a == "--mirror" || a == "--prune" {
			out = append(out, "force/mirror/prune flag "+a)
		}
	}
	switch sub {
	case "push":
		for _, a := range rest {
			switch {
			case a == "--delete":
				out = append(out, "push --delete")
			case !strings.HasPrefix(a, "--") && strings.HasPrefix(a, "-") && strings.ContainsAny(a[1:], "fd"):
				out = append(out, "push short flag "+a+" (force or delete)")
			case strings.HasPrefix(a, "+") || strings.HasPrefix(a, ":") || strings.Contains(a, ":+"):
				out = append(out, "push refspec "+a+" forces or deletes")
			}
		}
		if len(rest) != 3 || rest[0] != "--quiet" || rest[1] != "origin" || !strings.HasPrefix(rest[2], "HEAD:refs/heads/") {
			out = append(out, "push is not `push --quiet origin HEAD:refs/heads/<branch>`")
		}
	case "branch", "tag":
		for _, a := range rest {
			if a == "-d" || a == "-D" || a == "--delete" {
				out = append(out, sub+" deletion")
			}
		}
	case "update-ref":
		for _, a := range rest {
			if a == "-d" || a == "--delete" || a == "--stdin" {
				out = append(out, "update-ref deletion or batch")
			}
		}
		// Allowed: a new ref under refs/petalbook/unpushed/, or HEAD moved from
		// the commit it is at (all zeros: it must not exist yet) to a new one.
		// Both values are full ids: a ref name or "" would let it move anywhere
		// or delete.
		switch {
		case len(rest) == 2 && strings.HasPrefix(rest[0], "refs/petalbook/unpushed/"):
		case len(rest) == 3 && rest[0] == "HEAD" && fullID(rest[1]) && fullID(rest[2]) &&
			len(rest[1]) == len(rest[2]) && strings.Trim(rest[1], "0") != "":
		default:
			out = append(out, "update-ref other than a new unpushed ref or a compare-and-swap of HEAD")
		}
	case "remote":
		if len(rest) > 0 && rest[0] != "get-url" && rest[0] != "add" && rest[0] != "set-url" && rest[0] != "remove" {
			out = append(out, "remote "+rest[0])
		}
		if len(rest) > 1 && rest[1] != "origin" {
			out = append(out, "remote "+rest[0]+" "+rest[1])
		}
	case "gc", "prune", "reflog", "filter-branch", "replace", "notes":
		out = append(out, "history rewriting command "+sub)
	}
	return out
}

// fullID is a whole commit id, SHA-1 or SHA-256, in lower-case hex.
func fullID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func TestCheckerCatchesDestructiveCommands(t *testing.T) {
	bad := [][]string{
		{"push", "--force", "origin", "HEAD:refs/heads/main"},
		{"push", "-f", "origin", "HEAD:refs/heads/main"},
		{"push", "-fu", "origin", "HEAD:refs/heads/main"},
		{"push", "--force-with-lease", "origin", "HEAD:refs/heads/main"},
		{"push", "--mirror", "origin"},
		{"push", "--delete", "origin", "x"},
		{"push", "-d", "origin", "x"},
		{"push", "--quiet", "origin", "+HEAD:refs/heads/main"},
		{"push", "--quiet", "origin", ":refs/heads/main"},
		{"push", "--quiet", "origin", "HEAD:refs/heads/main", "main"},
		{"push", "origin", "main"},
		{"branch", "-D", "x"},
		{"tag", "-d", "v1"},
		{"update-ref", "-d", "refs/heads/main"},
		{"update-ref", "refs/heads/main", "0000000000000000000000000000000000000000"},
		{"update-ref", "HEAD", strings.Repeat("a", 40)},
		{"update-ref", "HEAD", strings.Repeat("0", 40), strings.Repeat("a", 40)},
		{"update-ref", "HEAD", "", strings.Repeat("a", 40)},
		{"update-ref", "HEAD", "refs/heads/other", strings.Repeat("a", 40)},
		{"update-ref", "HEAD", strings.Repeat("b", 40), "main"},
		{"update-ref", "HEAD", strings.Repeat("b", 64), strings.Repeat("a", 40)},
		{"update-ref", "refs/heads/main", strings.Repeat("b", 40), strings.Repeat("a", 40)},
		{"update-ref", "--no-deref", "HEAD", strings.Repeat("b", 40), strings.Repeat("a", 40)},
		{"-c", "x=y", "push", "-f", "origin"},
		{"--git-dir", "x", "push", "-f", "origin"},
		{"remote", "prune", "origin"},
		{"remote", "remove", "other"},
		{"gc", "--prune=now"},
	}
	for _, a := range bad {
		if len(problems(Cmd{Args: a})) == 0 {
			t.Errorf("not caught: git %s", strings.Join(a, " "))
		}
	}
	good := [][]string{
		{"push", "--quiet", "origin", "HEAD:refs/heads/main"},
		{"push", "--quiet", "origin", "HEAD:refs/heads/notes/2026"},
		{"reset", "--quiet", "--hard", "refs/remotes/origin/main"},
		{"fetch", "--quiet", "--no-tags", "origin", "main"},
		{"update-ref", "refs/petalbook/unpushed/20261121-120000", strings.Repeat("a", 40)},
		{"update-ref", "HEAD", strings.Repeat("b", 40), strings.Repeat("a", 40)},
		{"update-ref", "HEAD", strings.Repeat("b", 40), strings.Repeat("0", 40)},
		{"remote", "add", "origin", "https://github.com/a/b.git"},
		{"remote", "remove", "origin"},
		{"log", "--follow", "--name-status", "-z", "-n", "5", "HEAD", "--", "a.md"},
	}
	for _, a := range good {
		if p := problems(Cmd{Args: a}); len(p) > 0 {
			t.Errorf("false positive for git %s: %v", strings.Join(a, " "), p)
		}
	}
}

func TestRuntimeGuardRefusesDestructiveCommands(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for _, args := range [][]string{
		{"push", "--force", "origin", "HEAD:refs/heads/main"},
		{"push", "-f", "origin", "HEAD:refs/heads/main"},
		{"push", "--force-with-lease", "origin", "HEAD:refs/heads/main"},
		{"push", "--quiet", "origin", "+HEAD:refs/heads/main"},
		{"push", "--quiet", "origin", ":refs/heads/main"},
		{"push", "--quiet", "origin", "HEAD:refs/heads/-x"},
		{"push", "--delete", "origin", "main"},
		{"push", "--mirror"},
		{"push", "origin", "main"},
		{"push", "--quiet", "--push-option=x", "origin", "HEAD:refs/heads/main"},
		{"branch", "-D", "main"},
		{"-c", "a=b", "tag", "-d", "v1"},
		{"update-ref", "-d", "refs/heads/main"},
		{"update-ref", "refs/heads/main", sha},
		{"update-ref", "refs/petalbook/unpushed/x", "HEAD"},
		{"update-ref", "refs/petalbook/unpushed/x/../../heads/main", sha},
		{"update-ref", "refs/petalbook/unpushed/x", sha, sha},
		{"update-ref", "HEAD", sha},
		{"update-ref", "HEAD", sha, "main"},
		{"update-ref", "HEAD", strings.Repeat("0", 40), sha},
		{"update-ref", "HEAD", strings.Repeat("b", 64), sha},
		{"update-ref", "--no-deref", "HEAD", strings.Repeat("b", 40), sha},
		{"update-ref", "-m", "x", "HEAD", strings.Repeat("b", 40), sha},
		{"read-tree", "--reset", "-u", "HEAD"},
		{"read-tree", sha},
		{"write-tree", "--prefix=x"},
		{"commit-tree", sha, "-m", "x"},
		{"commit-tree", sha, "-p", "HEAD", "-F", "-"},
		{"commit-tree", sha, "-F", "/etc/passwd"},
		{"commit-tree", sha, "-p", sha, "-p", sha, "-F", "-"},
		{"update-index", "--index-info", "--chmod=+x"},
		{"ls-files", "-s", "-z", "--with-tree=x"},
		{"remote", "prune", "origin"},
		{"remote", "set-url", "origin"},
		{"remote", "add", "upstream", "https://x/y"},
		{"remote", "add", "origin", "--mirror=push"},
		{"remote", "remove", "upstream"},
		{"--git-dir", "x", "push", "--force", "origin"},
		{"--git-dir=x", "push", "-f", "origin"},
		{"--work-tree", "x", "--namespace", "n", "push", "--delete", "origin", "main"},
		{"--exec-path=/x", "--config-env", "a=B", "branch", "-D", "main"},
		{"send-pack", "origin", "--force"},
		{"receive-pack", "x"},
		{"clone", "https://x/y"},
		{"-c", "alias.p=push", "p", "--force", "origin"},
		{"--attr-source", "HEAD", "status"},
		{"--literal-pathspecs", "status"},
		{"--foo", "x", "status"},
		{"-c", "alias.s=status", "s"},
		{"-c", "core.hooksPath=/tmp/x", "status"},
		{"-c", "core.sshCommand=evil", "fetch", "origin"},
		{"-ccommit.gpgsign=false", "status"},
		{"-C"},
		// Options that run a program or write somewhere, per subcommand.
		{"fetch", "--quiet", "--upload-pack=evil", "origin"},
		{"fetch", "--quiet", "-u", "origin"},
		{"fetch", "--quiet", "--receive-pack=evil", "origin"},
		{"fetch", "--quiet", "--exec=evil", "origin"},
		{"fetch", "--quiet", "--prune", "origin"},
		{"ls-remote", "--upload-pack=evil", "origin"},
		{"log", "-n", "1", "--output=/tmp/x"},
		{"log", "-n", "1", "--ext-diff"},
		{"log", "-n", "1", "--textconv"},
		{"log", "--follow", "-n", "1", "--exec-path"},
		{"cat-file", "--batch", "--filters"},
		{"cat-file", "-p", "HEAD"},
		{"merge", "--quiet", "--ff-only", "-o", "x", "refs/remotes/origin/main"},
		{"merge", "--quiet", "--no-ff", "x"},
		{"reset", "--quiet", "--hard", "--push-option=x", "origin/main"},
		{"reset", "--quiet", "--keep", "x"},
		{"status", "--porcelain", "-uall", "--output=x"},
		{"commit", "--quiet", "-m", "x"},
		{"commit", "--quiet", "--amend", "-F", "-"},
		{"commit", "--quiet", "-F", "-"},
		{"commit", "--quiet", "--no-verify", "--message=x", "--pathspec-from-file=-", "--pathspec-file-nul"},
		{"rev-parse", "--git-dir", "--exec-path"},
		{"config", "user.name", "evil"},
		{"config", "--global", "user.name"},
		{"checkout", "--quiet", "-B", "x"},
		{"checkout", "--quiet", "--orphan", "x"},
		{"rm", "-f", "x"},
		{"add", "-f", "x"},
		{"init", "--template=/tmp/x"},
		{"init", "--separate-git-dir=/tmp/x"},
	} {
		if _, _, err := (execRunner{}).Run(context.Background(), Cmd{Dir: t.TempDir(), Args: args}); err == nil || !strings.Contains(err.Error(), "refusing") {
			t.Errorf("git %v: %v", args, err)
		}
	}
	for _, args := range [][]string{
		{"fetch", "--quiet", "--no-tags", "origin", "main"},
		{"reset", "--quiet", "--hard", sha},
		{"-C", "d", "log", "-n", "1"},
		{"-c", "commit.gpgsign=false", "-C", "d", "status"},
		{"-c", "core.autocrlf=false", "status"},
		{"--no-pager", "log"},
		{"remote", "get-url", "origin"},
		{"remote", "add", "origin", "https://github.com/a/b.git"},
		{"remote", "remove", "origin"},
		{"update-ref", "refs/petalbook/unpushed/20261121-120000", sha},
		{"update-ref", "HEAD", strings.Repeat("b", 40), sha},
		{"update-ref", "HEAD", sha, strings.Repeat("0", 40)},
		{"read-tree", "HEAD"},
		{"write-tree"},
		{"commit-tree", sha, "-F", "-"},
		{"commit-tree", sha, "-p", strings.Repeat("b", 40), "-F", "-"},
		{"update-index", "-z", "--index-info"},
		{"ls-files", "-s", "-z", "--", "a.md", "-f"},
		{"ls-remote", "--heads", "origin", "refs/heads/main"},
		{"log", "--follow", "--name-status", "-z", "--no-color", "-n", "5", "--format=%H%x00%ct%x00%an", sha, "--", "--weird-file"},
		{"cat-file", "--batch"},
		{"diff", "--name-status", "-z", "--no-renames", sha, sha},
		{"diff", "--cached", "--name-status", "-z", "-M"},
		{"ls-tree", "-r", "-z", "--name-only", sha, "--", "-f"},
		{"rev-list", "--left-right", "--count", "HEAD...refs/remotes/origin/main"},
		{"init", "--quiet", "--initial-branch=main"},
	} {
		if err := refuseDestructive(args); err != nil {
			t.Errorf("%v refused: %v", args, err)
		}
	}
}

func TestPushArgsIsTheOnlyPushShape(t *testing.T) {
	args, err := pushArgs("main")
	if err != nil || strings.Join(args, " ") != "push --quiet origin HEAD:refs/heads/main" {
		t.Fatalf("%v %v", args, err)
	}
	if args, err = pushArgs("notes/2026"); err != nil || args[3] != "HEAD:refs/heads/notes/2026" {
		t.Fatalf("%v %v", args, err)
	}
	for _, b := range []string{"", "+main", ":main", "-f", "a b", "a..b", "x/", "x.lock", "a:b", "a\nb", "main^", "a~1", "a//b"} {
		if _, err := pushArgs(b); err == nil {
			t.Errorf("branch %q accepted", b)
		}
	}
	if _, err := keepRefArgs("../heads/main", strings.Repeat("a", 40)); err == nil {
		t.Error("a ref outside the namespace was accepted")
	}
}

func TestRecorderSeesTheSuitesPushes(t *testing.T) {
	needGit(t)
	before := len(rec.all())
	remote := bareRemote(t)
	r := newClone(t, remote, nil)
	write(t, r.Dir(), "a.md", "x\n")
	if _, err := r.CommitPaths(context.Background(), []string{"a.md"}, msg, person, svc); err != nil {
		t.Fatal(err)
	}
	if err := r.Push(context.Background(), "main"); err != nil {
		t.Fatal(err)
	}
	pushes := 0
	for _, c := range rec.all()[before:] {
		if sub, _ := subcommand(c.Args); sub == "push" {
			pushes++
			if strings.Join(c.Args, " ") != "push --quiet origin HEAD:refs/heads/main" {
				t.Errorf("unexpected push: %v", c.Args)
			}
		}
	}
	if pushes != 1 {
		t.Fatalf("expected exactly one push, saw %d", pushes)
	}
}

// Static checks --------------------------------------------------------------

// nonTestSources lists the Go files under cmd and internal that are not tests.
func nonTestSources(t *testing.T) []string {
	t.Helper()
	root := repoRoot(t)
	var files []string
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
				files = append(files, p)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(files) < 20 {
		t.Fatalf("found only %d sources", len(files))
	}
	return files
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, _ := os.Getwd()
	for d := dir; d != filepath.Dir(d); d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
	}
	t.Fatal("go.mod not found")
	return ""
}

func isSafegit(f string) bool {
	return filepath.Base(f) == "safegit.go" && filepath.Base(filepath.Dir(f)) == "gitstore"
}

func TestStaticNoForcePushAnywhereButSafegit(t *testing.T) {
	words := []string{"--force", "force-with-lease", "--mirror", "--delete", "--prune", "update-ref",
		`"-f"`, `"-d"`, `"-D"`, `"+refs`, `"+HEAD`, `"+"+`, "HEAD:refs/"}
	for _, f := range nonTestSources(t) {
		if isSafegit(f) {
			continue
		}
		b, _ := os.ReadFile(f)
		for i, line := range strings.Split(string(b), "\n") {
			for _, w := range words {
				if strings.Contains(line, w) {
					t.Errorf("%s:%d mentions %q; only gitstore/safegit.go may build or refuse such commands", f, i+1, w)
				}
			}
		}
	}
}

func TestStaticOnlyOneGitRunnerAndOnePushBuilder(t *testing.T) {
	for _, f := range nonTestSources(t) {
		src, _ := os.ReadFile(f)
		fset := token.NewFileSet()
		af, err := parser.ParseFile(fset, f, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		isRunner := strings.HasSuffix(f, filepath.Join("gitstore", "runner.go"))
		for _, imp := range af.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if p == "os/exec" && !isRunner && strings.Contains(string(src), `"git"`) {
				t.Errorf("%s runs git itself: git must only be started by gitstore's runner", f)
			}
		}
		if isSafegit(f) {
			continue
		}
		for i, line := range strings.Split(string(src), "\n") {
			if strings.Contains(line, `"push"`) {
				t.Errorf("%s:%d: a push command line must be built by pushArgs in safegit.go", f, i+1)
			}
		}
	}
}
