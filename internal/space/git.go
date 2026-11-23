package space

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
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

// Author is who made a change, as Git records them. The core knows nothing of
// accounts: whoever calls a change (the API, the drafts sweeper) passes the
// identity of the person acting. The zero value is the space's default author
// (the owner set at open), which is what a single-user server passes.
//
// Via says by what means the change was made when it was not the person typing
// in the app (for example "API token"): the commit ends with a
// "Changed-via: <Via>" trailer, and a change made by one means is never
// committed together with a change made by another.
type Author struct{ Name, Email, Via string }

// ViaToken is the Via of a change made on the API with an API token.
const ViaToken = "API token"

// ViaMCP is the Via of a change made through MCP.
const ViaMCP = "MCP"

type viaKey struct{}

// ContextWithVia carries a Via down to the code that learns the author of a
// change only by the id of the person (the drafts service).
func ContextWithVia(ctx context.Context, via string) context.Context {
	return context.WithValue(ctx, viaKey{}, via)
}

// ViaFrom is the Via ContextWithVia put in ctx, or "".
func ViaFrom(ctx context.Context) string {
	v, _ := ctx.Value(viaKey{}).(string)
	return v
}

// cleanVia makes a Via fit a trailer: one line, no surrounding space.
func cleanVia(via string) string {
	return strings.TrimSpace(strings.NewReplacer("\n", " ", "\r", " ", "\x00", "").Replace(via))
}

// authorGroup is the paths one identity gets credit for in one commit, made by
// one means (via).
type authorGroup struct {
	id    gitstore.Identity
	via   string
	paths []string // sorted
}

type groupKey struct {
	id  gitstore.Identity
	via string
}

// groupByAuthor splits pending paths into one group per resulting author, in
// a fixed order (by name, then email), so the commits of one idle window come
// out the same way every time. Authors are resolved first: the zero Author
// and the owner spelled out are one person and one commit. With a single
// author, the common case, this is one group and no extra work.
func (g *gitRepo) groupByAuthor(pending map[string]Author) []authorGroup {
	byID := map[groupKey]*authorGroup{}
	resolved := map[Author]groupKey{}
	for p, by := range pending {
		k, ok := resolved[by]
		if !ok {
			id, _ := g.identities(by)
			k = groupKey{id, cleanVia(by.Via)}
			resolved[by] = k
		}
		grp := byID[k]
		if grp == nil {
			grp = &authorGroup{id: k.id, via: k.via}
			byID[k] = grp
		}
		grp.paths = append(grp.paths, p)
	}
	out := make([]authorGroup, 0, len(byID))
	for _, grp := range byID {
		sort.Strings(grp.paths)
		out = append(out, *grp)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].id.Name != out[j].id.Name {
			return out[i].id.Name < out[j].id.Name
		}
		if out[i].id.Email != out[j].id.Email {
			return out[i].id.Email < out[j].id.Email
		}
		return out[i].via < out[j].via
	})
	return out
}

// gitRepo is the committer's view of the repository: the service identity,
// the author commits are made for, and the restricted runner of the gitstore
// package. It only ever adds, commits and (once) initialises: nothing here
// pushes, resets or rewrites history.
type gitRepo struct {
	dir         string
	dirs        gitDirs // where the Git directory is, once the space is opened
	name, email string  // the committer: the service
	// author is who commits are made for when a change names nobody (the
	// space's owner); empty means the
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

// identities are the author and the committer of a commit made for by. A
// zero Author, or one missing its name or email, is the default author: the
// owner set at open, else the committer. Git would refuse an empty name or
// email, so a half-filled identity is never used as given.
func (g *gitRepo) identities(by Author) (author, committer gitstore.Identity) {
	an, ae := by.Name, by.Email
	if an == "" || ae == "" {
		g.amu.RLock()
		an, ae = g.authorName, g.authorEmail
		g.amu.RUnlock()
	}
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

// exclude keeps a path (from the top of the work tree) out of the
// repository without touching a tracked file: in info/exclude of the
// repository, which every checkout of it reads.
func (g *gitRepo) exclude(pattern string) error { return g.excludeLine("/" + pattern) }

// tempPattern, in info/exclude, matches every name tempName and writeIn give,
// in any folder: git status lists none, and `git add` of a folder takes none.
const tempPattern = ".*petalbook-w-*"

// excludeLine adds the pattern line to info/exclude as it is.
func (g *gitRepo) excludeLine(line string) error {
	file := filepath.Join(g.dirs.common, "info", "exclude")
	if g.dirs.common == "" {
		file = filepath.Join(g.dir, ".git", "info", "exclude")
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return nil // a read-only repository: not fatal
	}
	_, err := ensureLines(file, line)
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
// and anything the user staged, stay as they are. The commit is authored by
// author (already resolved, see identities). It reports whether a commit was
// made.
func (g *gitRepo) commitPaths(author gitstore.Identity, via string, paths []string) (bool, error) {
	_, committer := g.identities(Author{})
	return g.r().CommitPaths(context.Background(), paths, messageVia(via), author, committer)
}

// messageVia is commitMessage with the trailer of a change made by means other
// than the app (none when via is empty).
func messageVia(via string) func([]gitstore.Change) string {
	via = cleanVia(via)
	if via == "" {
		return commitMessage
	}
	return func(cs []gitstore.Change) string { return commitMessage(cs) + "\n\nChanged-via: " + via }
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
