package space

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
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

// gitRepo runs the system git in the space directory. It only ever adds,
// commits and (once) initialises: no push, no reset, no history rewriting.
type gitRepo struct {
	dir         string
	name, email string
	// before, if set, is called with the arguments of every git command
	// before it runs. Tests use it to hold a commit in progress.
	before func(args []string)
}

func (g *gitRepo) run(args ...string) ([]byte, error) { return g.runIn(nil, args...) }

func (g *gitRepo) runIn(stdin []byte, args ...string) ([]byte, error) {
	if g.before != nil {
		g.before(args)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, errors.New("git was not found in PATH; PetalBook needs it to keep history")
	}
	cmd := exec.Command(git, append([]string{"-C", g.dir, "-c", "core.quotepath=false", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Env = g.env()
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// env drops variables that would point git at some other repository and sets
// the identity of automatic commits.
func (g *gitRepo) env() []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_COMMON_DIR", "GIT_NAMESPACE",
			"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_AUTHOR_DATE", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "GIT_COMMITTER_DATE":
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"GIT_AUTHOR_NAME="+g.name, "GIT_AUTHOR_EMAIL="+g.email,
		"GIT_COMMITTER_NAME="+g.name, "GIT_COMMITTER_EMAIL="+g.email,
		"GIT_TERMINAL_PROMPT=0")
}

func (g *gitRepo) init() error {
	_, err := g.run("init", "-q")
	return err
}

// exclude keeps a path out of the repository without touching a tracked file.
func (g *gitRepo) exclude(pattern string) error {
	file := filepath.Join(g.dir, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return nil // a .git file (worktree) or read-only repository: not fatal
	}
	_, err := ensureLines(file, "/"+pattern)
	return err
}

// busyError means the repository is in the middle of an operation of the
// user's (merge, rebase...), which a commit of ours would disturb.
type busyError struct{ what string }

func (e *busyError) Error() string { return "the repository is in the middle of a " + e.what }

// busy reports an operation in progress in the repository, or "".
func (g *gitRepo) busy() (string, error) {
	out, err := g.run("rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	gitDir := strings.TrimSpace(string(out))
	for _, c := range []struct{ file, what string }{
		{"MERGE_HEAD", "merge"}, {"rebase-merge", "rebase"}, {"rebase-apply", "rebase"},
		{"CHERRY_PICK_HEAD", "cherry-pick"}, {"REVERT_HEAD", "revert"}, {"BISECT_LOG", "bisect"}, {"sequencer", "cherry-pick"},
	} {
		if _, err := os.Lstat(filepath.Join(gitDir, c.file)); err == nil {
			return c.what, nil
		}
	}
	return "", nil
}

func nulList(paths []string) []byte {
	return []byte(strings.Join(paths, "\x00") + "\x00")
}

// commitPaths commits the changes to exactly these paths (slash-separated,
// relative to the space) and nothing else: other modified or untracked files,
// and anything the user staged, stay as they are. It reports whether a commit
// was made.
func (g *gitRepo) commitPaths(paths []string) (bool, error) {
	if what, err := g.busy(); err != nil {
		return false, err
	} else if what != "" {
		return false, &busyError{what}
	}
	var present, gone []string
	for _, p := range paths {
		if _, err := os.Lstat(filepath.Join(g.dir, filepath.FromSlash(p))); err == nil {
			present = append(present, p)
		} else {
			gone = append(gone, p)
		}
	}
	if len(present) > 0 {
		if _, err := g.runIn(nulList(present), "add", "-A", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
			return false, err
		}
	}
	if len(gone) > 0 {
		// A path that was created and deleted before it was ever committed is
		// unknown to git; --ignore-unmatch makes that fine.
		if _, err := g.runIn(nulList(gone), "rm", "--cached", "-r", "-q", "--ignore-unmatch", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
			return false, err
		}
	}
	out, err := g.run("diff", "--cached", "--name-status", "-z", "-M")
	if err != nil {
		return false, err
	}
	mine := make(map[string]bool, len(paths))
	for _, p := range paths {
		mine[p] = true
	}
	var changes []change
	var touched []string
	for _, c := range parseNameStatus(out) {
		if mine[c.old] || mine[c.new] {
			changes = append(changes, c)
			touched = append(touched, c.new)
			if c.old != c.new {
				touched = append(touched, c.old)
			}
		}
	}
	if len(changes) == 0 {
		return false, nil
	}
	if _, err := g.runIn(nulList(touched), "commit", "-q", "-m", commitMessage(changes), "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
		return false, err
	}
	return true, nil
}

type change struct {
	status   byte // A, M, D, R, T...
	old, new string
}

// parseNameStatus reads `git diff --name-status -z`: "M\0path\0", and for
// renames "R100\0old\0new\0".
func parseNameStatus(out []byte) []change {
	f := strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
	var cs []change
	for i := 0; i < len(f); {
		st := f[i]
		if st == "" {
			i++
			continue
		}
		switch st[0] {
		case 'R', 'C':
			if i+2 >= len(f) {
				return cs
			}
			cs = append(cs, change{st[0], f[i+1], f[i+2]})
			i += 3
		default:
			if i+1 >= len(f) {
				return cs
			}
			cs = append(cs, change{st[0], f[i+1], f[i+1]})
			i += 2
		}
	}
	return cs
}

// commitMessage summarises a change set in a short English line: "Edit 暮雪",
// "Add 3 pages", "Add 2 pages, edit 1 page".
func commitMessage(cs []change) string {
	var added, edited, deleted, renamed, moved, others []string
	for _, c := range cs {
		p := c.new
		if !isPagePath(p) {
			others = append(others, path.Base(p))
			continue
		}
		title := strings.TrimSuffix(path.Base(p), path.Ext(p))
		switch c.status {
		case 'A', 'C':
			added = append(added, title)
		case 'D':
			deleted = append(deleted, strings.TrimSuffix(path.Base(c.old), path.Ext(c.old)))
		case 'R':
			if path.Dir(c.old) != path.Dir(c.new) {
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
