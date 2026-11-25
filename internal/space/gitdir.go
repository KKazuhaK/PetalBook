package space

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"petalbook.app/server/internal/gitstore"
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

// resolveGitDirs asks git (through the restricted runner of repo, which
// remembers the answer: a repository made by repo is not asked about at all)
// for the Git directory of the work tree at repo's directory. It fails when
// there is none, or when that directory is not the top of its work tree (a
// .git that git does not take for one, and a repository further up is found
// instead).
func resolveGitDirs(repo *gitstore.Repo, root string) (gitDirs, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	l, err := repo.Layout(ctx)
	if err != nil {
		msg := err.Error()
		var ge *gitstore.GitError
		if errors.As(err, &ge) {
			if m := strings.TrimSpace(ge.Stderr); m != "" {
				msg = m
			} else {
				msg = ge.Err.Error()
			}
		}
		return gitDirs{}, fmt.Errorf("the space %s has no Git directory that can be used (%s)", root, gitstore.Scrub(msg))
	}
	if l.GitDir == "" || !filepath.IsAbs(l.GitDir) {
		return gitDirs{}, fmt.Errorf("the space %s has no Git directory that can be used (git said %q)", root, l.GitDir)
	}
	if strings.TrimSpace(l.Prefix) != "" {
		return gitDirs{}, fmt.Errorf("the space %s has no Git directory of its own: git found the repository of a folder above it", root)
	}
	gd, err := filepath.EvalSymlinks(l.GitDir)
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
		if d, err := resolveGitDirs(gitstore.New(gitstore.Options{Dir: abs}), abs); err == nil {
			return stateDirIn(d.git)
		}
		root = abs
	}
	return stateDirIn(filepath.Join(root, ".git"))
}

// within reports whether p is dir or inside it (both real paths).
func within(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && filepath.IsLocal(rel)
}
