package space

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/gitstore"
)

// The server's own state is kept in the Git directory of the space. That is
// not always <space>/.git: a checkout made with `git worktree add`, a
// submodule and a repository made with --separate-git-dir have a .git file
// that names it, and .git can be a link to a folder elsewhere. Git itself
// says where it is.

// gitDirs is where the Git directory of a space is.
type gitDirs struct {
	// git is the Git directory of the work tree: for a checkout made with
	// `git worktree add`, its own one (.git/worktrees/<name> of the main
	// repository), so two checkouts of one repository never share state.
	git string
	// common is the directory the checkouts of one repository share (where
	// info/exclude is read from); git itself for a plain repository.
	common string
}

// resolveGitDirs asks git (through the restricted runner) for the Git
// directory of the work tree at root. It fails when there is none, or when
// root is not the top of its work tree (a .git that git does not take for
// one, and a repository further up is found instead).
func resolveGitDirs(run gitstore.Runner, root string) (gitDirs, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, errOut, err := run.Run(ctx, gitstore.Cmd{Dir: root, Args: []string{"rev-parse", "--absolute-git-dir", "--show-prefix"}})
	if err != nil {
		msg := strings.TrimSpace(errOut)
		if msg == "" {
			msg = err.Error()
		}
		return gitDirs{}, fmt.Errorf("the space %s has no Git directory that can be used (%s)", root, gitstore.Scrub(msg))
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 || lines[0] == "" || !filepath.IsAbs(lines[0]) {
		return gitDirs{}, fmt.Errorf("the space %s has no Git directory that can be used (git said %q)", root, out)
	}
	if len(lines) > 1 && strings.TrimSpace(lines[1]) != "" {
		return gitDirs{}, fmt.Errorf("the space %s has no Git directory of its own: git found the repository of a folder above it", root)
	}
	gd, err := filepath.EvalSymlinks(lines[0])
	if err != nil {
		return gitDirs{}, err
	}
	d := gitDirs{git: gd, common: gd}
	if b, err := os.ReadFile(filepath.Join(gd, "commondir")); err == nil {
		c := strings.TrimSpace(string(b))
		if c != "" {
			if !filepath.IsAbs(c) {
				c = filepath.Join(gd, c)
			}
			if c, err = filepath.EvalSymlinks(c); err == nil {
				d.common = c
			}
		}
	}
	return d, nil
}

// stateDirIn is the default state directory in the Git directory gd.
func stateDirIn(gd string) string { return filepath.Join(gd, "petalbook-state") }

// DefaultStateDir is where the space at root keeps its state (index,
// journal, notices; in single-user mode the state database too) when no
// other place is given: in its Git directory (see resolveGitDirs), which no
// pull or checkout ever writes to, so nothing a pull brings can take its
// place. When git cannot say where that is, it is <root>/.git/petalbook-state.
func DefaultStateDir(root string) string {
	if abs, err := filepath.Abs(root); err == nil {
		if r, err := filepath.EvalSymlinks(abs); err == nil {
			abs = r
		}
		if d, err := resolveGitDirs(realRunner{}, abs); err == nil {
			return stateDirIn(d.git)
		}
		root = abs
	}
	return stateDirIn(filepath.Join(root, ".git"))
}

type realRunner struct{}

func (realRunner) Run(ctx context.Context, c gitstore.Cmd) (string, string, error) {
	return gitstore.RunReal(ctx, c)
}

// within reports whether p is dir or inside it (both real paths).
func within(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && filepath.IsLocal(rel)
}
