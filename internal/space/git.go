package space

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/KKazuhaK/PetalBook/internal/gitstore"
)

// ParseCommitter splits "Name <email>". An empty string means DefaultCommitter.
func ParseCommitter(s string) (name, email string, err error) {
	if strings.TrimSpace(s) == "" {
		s = DefaultCommitter
	}
	i, j := strings.LastIndex(s, "<"), strings.LastIndex(s, ">")
	if i < 0 || j < i || strings.TrimSpace(s[j+1:]) != "" {
		return "", "", fmt.Errorf("committer %q must look like: Name <email>", s)
	}
	name, email = strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:j])
	if name == "" || email == "" || strings.ContainsAny(name+email, "\n\x00") {
		return "", "", fmt.Errorf("committer %q must look like: Name <email>", s)
	}
	return name, email, nil
}

// gitRepo is the committer's view of the repository: the service identity,
// the author commits are made for, and the restricted runner of the gitstore
// package. It only ever adds, commits and (once) initialises: nothing here
// pushes, resets or rewrites history.
type gitRepo struct {
	dir         string
	name, email string // the committer: the service
	// author is who commits are made for (the space's owner); empty means the
	// committer. It can change while the server runs, when the owner edits
	// their profile.
	amu                     sync.RWMutex
	authorName, authorEmail string
	// before, if set, is called with the subcommand and arguments of every git
	// command before it runs. Tests use it to hold a commit in progress.
	before func(args []string)

	once sync.Once
	repo *gitstore.Repo
}

func (g *gitRepo) r() *gitstore.Repo {
	g.once.Do(func() { g.repo = gitstore.New(gitstore.Options{Dir: g.dir, Runner: hookRunner{g}}) })
	return g.repo
}

// hookRunner calls the test hook and then the real runner.
type hookRunner struct{ g *gitRepo }

func (h hookRunner) Run(ctx context.Context, c gitstore.Cmd) (string, string, error) {
	if h.g.before != nil {
		sub, rest := gitstore.Subcommand(c.Args)
		h.g.before(append([]string{sub}, rest...))
	}
	return gitstore.RunReal(ctx, c)
}

// identities are the author and the committer of the next commit.
func (g *gitRepo) identities() (author, committer gitstore.Identity) {
	g.amu.RLock()
	an, ae := g.authorName, g.authorEmail
	g.amu.RUnlock()
	if an == "" || ae == "" {
		an, ae = g.name, g.email
	}
	return gitstore.Identity{Name: an, Email: ae}, gitstore.Identity{Name: g.name, Email: g.email}
}

func (g *gitRepo) setAuthor(name, email string) {
	g.amu.Lock()
	g.authorName, g.authorEmail = name, email
	g.amu.Unlock()
}

func (g *gitRepo) init() error { return g.r().Init(context.Background(), "main") }

// exclude keeps a path out of the repository without touching a tracked file.
func (g *gitRepo) exclude(pattern string) error {
	file := filepath.Join(g.dir, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return nil // a .git file (worktree) or read-only repository: not fatal
	}
	_, err := ensureLines(file, "/"+pattern)
	return err
}

// Names the rest of the package (and its tests) use for what gitstore owns.
type (
	change    = gitstore.Change
	busyError = gitstore.BusyError
)

var parseNameStatus = gitstore.ParseNameStatus

// commitPaths commits the changes to exactly these paths (slash-separated,
// relative to the space) and nothing else: other modified or untracked files,
// and anything the user staged, stay as they are. It reports whether a commit
// was made.
func (g *gitRepo) commitPaths(paths []string) (bool, error) {
	author, committer := g.identities()
	return g.r().CommitPaths(context.Background(), paths, commitMessage, author, committer)
}

// commitMessage summarises a change set in a short English line: "Edit 暮雪",
// "Add 3 pages", "Add 2 pages, edit 1 page".
func commitMessage(cs []gitstore.Change) string {
	var added, edited, deleted, renamed, moved, others []string
	for _, c := range cs {
		p := c.New
		if !isPagePath(p) {
			others = append(others, path.Base(p))
			continue
		}
		title := strings.TrimSuffix(path.Base(p), path.Ext(p))
		switch c.Status {
		case 'A', 'C':
			added = append(added, title)
		case 'D':
			deleted = append(deleted, strings.TrimSuffix(path.Base(c.Old), path.Ext(c.Old)))
		case 'R':
			if path.Dir(c.Old) != path.Dir(c.New) {
				moved = append(moved, title)
			} else {
				renamed = append(renamed, title)
			}
		default:
			edited = append(edited, title)
		}
	}
	var parts []string
	for _, g := range []struct {
		verb  string
		items []string
	}{{"Add", added}, {"Edit", edited}, {"Delete", deleted}, {"Rename", renamed}, {"Move", moved}} {
		if len(g.items) == 0 {
			continue
		}
		parts = append(parts, phrase(g.verb, g.items, "page"))
	}
	if len(others) > 0 {
		parts = append(parts, phrase("Update", others, "file"))
	}
	if len(parts) == 0 {
		return "Update notes"
	}
	for i := 1; i < len(parts); i++ {
		parts[i] = strings.ToLower(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts, ", ")
}

func phrase(verb string, items []string, noun string) string {
	if len(items) == 1 {
		return verb + " " + clip(items[0], 50)
	}
	return fmt.Sprintf("%s %d %ss", verb, len(items), noun)
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

func isPagePath(p string) bool {
	if !strings.EqualFold(path.Ext(p), ".md") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(seg, ".") {
			return false
		}
	}
	return !strings.HasPrefix(p, "assets/")
}
