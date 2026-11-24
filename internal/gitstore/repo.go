package gitstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/text/unicode/norm"
)

// Identity is a Git author or committer.
type Identity struct {
	Name  string
	Email string
}

// Options configures a Repo.
type Options struct {
	Dir    string
	Runner Runner // nil: the real git
	// Cred is what commands that talk to the remote run with; nil for a
	// repository with no remote.
	Cred Credential
	// NetTimeout bounds every command that talks to the remote (default 60 s).
	// InitialTimeout bounds the first fetch of a whole repository (default 4 min: it must end before an HTTP response that waits for it times out).
	NetTimeout, InitialTimeout time.Duration
	// AllowUnsafe lets the transports of a test through (local paths, http);
	// see Policy.AllowUnsafe. Tests only.
	AllowUnsafe bool
}

// Repo runs git in one repository. It is safe for concurrent use; callers
// serialise what must not interleave.
type Repo struct {
	opt Options

	gdMu sync.Mutex
	gd   string // the absolute git directory, once known

	// ensureFn is set for a partial clone (see SetEnsure).
	ensureFn atomic.Pointer[EnsureFunc]
}

// New returns a Repo.
func New(o Options) *Repo {
	if o.NetTimeout <= 0 {
		o.NetTimeout = 60 * time.Second
	}
	if o.InitialTimeout <= 0 {
		o.InitialTimeout = 4 * time.Minute
	}
	return &Repo{opt: o}
}

// Dir is the work tree.
func (r *Repo) Dir() string { return r.opt.Dir }

// WithCredential returns a Repo for the same directory that talks to its
// remote with c.
func (r *Repo) WithCredential(c Credential) *Repo {
	o := r.opt
	o.Cred = c
	return &Repo{opt: o}
}

func (r *Repo) runner() Runner {
	if r.opt.Runner != nil {
		return r.opt.Runner
	}
	return defaultRunner
}

func (r *Repo) run(ctx context.Context, c Cmd) (string, error) {
	c.Dir = r.opt.Dir
	c.AllowUnsafe = r.opt.AllowUnsafe
	out, errOut, err := r.runner().Run(ctx, c)
	if err != nil {
		sub, _ := subcommand(c.Args)
		return "", &GitError{Sub: sub, Stderr: errOut, Err: err}
	}
	return out, nil
}

// git runs a command that does not touch the network and returns its output
// without the final newline.
func (r *Repo) git(ctx context.Context, args ...string) (string, error) {
	out, err := r.run(ctx, Cmd{Args: args})
	return strings.TrimRight(out, "\n"), err
}

func (r *Repo) gitIn(ctx context.Context, env []string, stdin []byte, args ...string) (string, error) {
	if stdin == nil {
		stdin = []byte{}
	}
	out, err := r.run(ctx, Cmd{Args: args, Env: env, Stdin: stdin})
	return strings.TrimRight(out, "\n"), err
}

// net runs a command that talks to the remote: with the credential, under a
// deadline, and with its failure classified (see NetError).
func (r *Repo) net(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	var env, secrets []string
	if r.opt.Cred != nil {
		e, s, cleanup, err := r.opt.Cred.prepare()
		if err != nil {
			var ce *CredentialError
			if errors.As(err, &ce) {
				kind := KindAuth
				if ce.Offline {
					kind = KindOffline
				}
				return "", &NetError{kind, Scrub(ce.Msg), err}
			}
			return "", &NetError{KindAuth, "the stored credential cannot be used", err}
		}
		defer cleanup()
		env, secrets = e, s
	}
	if timeout <= 0 {
		timeout = r.opt.NetTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := r.run(ctx, Cmd{Args: args, Env: env, Secrets: secrets})
	if err != nil {
		sub, _ := subcommand(args)
		return "", classify(sub, err)
	}
	return strings.TrimRight(out, "\n"), nil
}

// Init makes a repository whose first branch is branch.
func (r *Repo) Init(ctx context.Context, branch string) error {
	if err := validBranch(branch); err != nil {
		return err
	}
	if err := os.MkdirAll(r.opt.Dir, 0o755); err != nil {
		return err
	}
	_, err := r.git(ctx, "init", "--quiet", "--initial-branch="+branch)
	return err
}

// IsRepo reports whether Dir is inside a Git work tree.
func (r *Repo) IsRepo(ctx context.Context) bool {
	out, err := r.git(ctx, "rev-parse", "--is-inside-work-tree")
	return err == nil && out == "true"
}

// CommitOf resolves a ref to a full commit id, or "" when it does not exist.
func (r *Repo) CommitOf(ctx context.Context, ref string) string {
	out, err := r.git(ctx, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return ""
	}
	return out
}

// Head is the commit id of HEAD, "" in a repository with no commit yet.
func (r *Repo) Head(ctx context.Context) string { return r.CommitOf(ctx, "HEAD") }

// TrackedUnder lists the files HEAD has in dir (slash-separated, relative to
// the work tree), recursively. Nothing when there is no commit yet.
func (r *Repo) TrackedUnder(ctx context.Context, dir string) ([]string, error) {
	if r.Head(ctx) == "" {
		return nil, nil
	}
	out, err := r.run(ctx, Cmd{Args: []string{"ls-tree", "-r", "-z", "--name-only", "HEAD", "--", dir}})
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(strings.TrimRight(out, "\x00"), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

func remoteRef(branch string) string { return "refs/remotes/origin/" + branch }

// RemoteTip is the commit the remote branch was at when it was last fetched,
// "" if it never was.
func (r *Repo) RemoteTip(ctx context.Context, branch string) string {
	if validBranch(branch) != nil {
		return ""
	}
	return r.CommitOf(ctx, remoteRef(branch))
}

// Busy reports an operation in progress in the repository that is not ours (a
// merge or rebase someone started), or "".
func (r *Repo) Busy(ctx context.Context) (string, error) {
	gitDir, err := r.absGitDir(ctx)
	if err != nil {
		return "", err
	}
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

// absGitDir is the repository's git directory. It does not move, so it is asked for once.
func (r *Repo) absGitDir(ctx context.Context) (string, error) {
	r.gdMu.Lock()
	defer r.gdMu.Unlock()
	if r.gd != "" {
		return r.gd, nil
	}
	d, err := r.git(ctx, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	r.gd = d
	return d, nil
}

// BusyError means the repository is in the middle of an operation of the
// user's (merge, rebase...), which a commit of ours would disturb.
type BusyError struct{ What string }

func (e *BusyError) Error() string { return "the repository is in the middle of a " + e.What }

// Change is one file's change between two trees. For a rename (Status 'R' or
// 'C') Old is the earlier name; otherwise Old equals New.
type Change struct {
	Status   byte // A, M, D, R, T...
	Old, New string
}

func nulList(paths []string) []byte {
	return []byte(strings.Join(paths, "\x00") + "\x00")
}

// ParseNameStatus reads `git diff --name-status -z`: "M\0path\0", and for
// renames "R100\0old\0new\0".
func ParseNameStatus(out []byte) []Change {
	f := strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
	var cs []Change
	for i := 0; i < len(f); {
		st := strings.TrimLeft(f[i], "\n")
		if st == "" {
			i++
			continue
		}
		switch st[0] {
		case 'R', 'C':
			if i+2 >= len(f) {
				return cs
			}
			cs = append(cs, Change{st[0], f[i+1], f[i+2]})
			i += 3
		default:
			if i+1 >= len(f) {
				return cs
			}
			cs = append(cs, Change{st[0], f[i+1], f[i+1]})
			i += 2
		}
	}
	return cs
}

// CommitPaths commits the changes to exactly these paths (slash-separated,
// relative to the work tree) and nothing else: other modified or untracked
// files, and anything a person staged, stay as they are. message is given the
// changes that will be in the commit. It reports whether a commit was made.
func (r *Repo) CommitPaths(ctx context.Context, paths []string, message func([]Change) string, author, committer Identity) (bool, error) {
	if what, err := r.Busy(ctx); err != nil {
		return false, err
	} else if what != "" {
		return false, &BusyError{what}
	}
	var present, gone, skipped []string
	skip := map[string]bool{}
	// Present means present under exactly this name. Asking the file system
	// is not enough: one that ignores case answers for "Apple.md" when the file
	// is now "apple.md", and the old name would never be recorded as gone.
	names := exactNames{root: r.opt.Dir, dirs: map[string]dirNames{}}
	for _, p := range paths {
		e, err := names.entry(p)
		if err != nil {
			// Not knowing is not the same as gone: a directory that cannot be
			// read must not turn its files into deletions.
			return false, err
		}
		switch {
		case e == nil:
			gone = append(gone, p)
		case e.IsDir():
			// A path of the change that is a directory now was a file when it was
			// noted, and something outside the server put a directory in its place
			// (the server itself never makes a folder named like a page). Git cannot
			// commit "this file is gone" for a name that is a directory on disk, so
			// the path is left as it is in the repository: that is for a person to
			// sort out, and must not stop this commit or the ones after it.
			skip[p] = true
			skipped = append(skipped, p)
		default:
			present = append(present, p)
		}
	}
	if len(gone) > 0 {
		// Removals first: git adds a file under the spelling the index already
		// has for it, so the old name has to be out of the index before the new
		// one goes in. A path that was created and deleted before it was ever
		// committed is unknown to git; --ignore-unmatch makes that fine.
		if _, err := r.gitIn(ctx, nil, nulList(gone), "rm", "--cached", "-r", "-q", "--ignore-unmatch", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
			return false, err
		}
	}
	if len(present) > 0 {
		if _, err := r.gitIn(ctx, nil, nulList(present), "add", "-A", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
			// add refuses the whole list when one path cannot be added: a file an
			// ignore rule matches (the repository's, or the user's own
			// ~/.config/git/ignore), a path that something outside the server put
			// behind a symbolic link or turned into a device. The server wrote these
			// files and named them one by one, so each is staged on its own,
			// whatever the ignore rules say; one that still cannot be staged is left
			// out of the commit. Otherwise this commit would fail, and with the
			// paths still pending, every commit after it.
			for _, p := range present {
				one := nulList([]string{p})
				// add first, on its own: it knows about names that differ only in
				// case, which update-index would stage as a second file.
				if _, aerr := r.gitIn(ctx, nil, one, "add", "-A", "--pathspec-from-file=-", "--pathspec-file-nul"); aerr == nil {
					continue
				}
				if _, uerr := r.gitIn(ctx, nil, one, "update-index", "--add", "--replace", "-z", "--stdin"); uerr != nil {
					// Left out only when the path is something Git cannot hold at
					// all. Any other failure (the index is locked for a moment, the
					// disk is full) is the whole commit's failure, to be tried again.
					if !unstageable(uerr) {
						return false, err
					}
					skip[p] = true
					skipped = append(skipped, p)
				}
			}
		}
	}
	out, err := r.run(ctx, Cmd{Args: []string{"diff", "--cached", "--name-status", "-z", "-M"}})
	if err != nil {
		return false, err
	}
	mine := make(map[string]bool, len(paths))
	for _, p := range paths {
		mine[p] = true
	}
	var changes []Change
	for _, c := range ParseNameStatus([]byte(out)) {
		if skip[c.Old] || skip[c.New] {
			continue
		}
		switch {
		case mine[c.Old] && mine[c.New]:
			changes = append(changes, c)
		case c.Status == 'R' && mine[c.Old]:
			// Rename detection paired a path of the change with one a person
			// staged (or one that is simply new): only this side is ours. The
			// other stays staged, as it was.
			changes = append(changes, Change{'D', c.Old, c.Old})
		case c.Status == 'R' && mine[c.New]:
			changes = append(changes, Change{'A', c.New, c.New})
		case mine[c.New]:
			changes = append(changes, c)
		}
	}
	// What could not be staged or committed is reported, after the rest is committed.
	var left error
	if len(skipped) > 0 {
		left = &SkippedError{Paths: skipped}
	}
	if len(changes) == 0 {
		return false, left
	}
	env := []string{
		"GIT_AUTHOR_NAME=" + author.Name, "GIT_AUTHOR_EMAIL=" + author.Email,
		"GIT_COMMITTER_NAME=" + committer.Name, "GIT_COMMITTER_EMAIL=" + committer.Email,
	}
	made, err := r.commitChanges(ctx, changes, message(changes), env)
	if err != nil {
		return false, err
	}
	return made, left
}

// commitChanges commits these staged changes and nothing else, and reports
// whether there was anything to commit. The commit is assembled without the
// work tree: an index of its own gets the last commit's files, the removed
// names are taken out and the added and changed ones put in as the real index
// has them staged; that index is written as a tree, the tree made a commit on
// top of the last one, and the branch moved to it.
//
// `git commit <paths>` would do the same on most days, but it looks at the
// work tree again for every name it is given, and on a file system that
// ignores case the old name of a renamed file still answers there: the old
// name would be committed back next to the new one. And `git commit` on a
// fresh index re-reads every tracked file to see whether it changed, which
// makes one commit cost as much as the whole space is large. This way nothing
// is read from the work tree at all, and what a person has staged besides
// stays staged and uncommitted.
func (r *Repo) commitChanges(ctx context.Context, changes []Change, message string, env []string) (bool, error) {
	dir, err := r.tempIndexDir(ctx)
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(dir)
	ienv := []string{"GIT_INDEX_FILE=" + filepath.Join(dir, "index")}
	// The index of this commit is thrown away; a repository set to split its
	// index would keep a shared copy of it, the size of the whole index, in
	// .git for every commit.
	noSplit := []string{"-c", "core.splitIndex=false"}
	head := r.Head(ctx)
	headTree := ""
	if head != "" {
		if _, err := r.gitIn(ctx, ienv, nil, append(noSplit, "read-tree", "HEAD")...); err != nil {
			return false, err
		}
		if headTree, err = r.git(ctx, "rev-parse", "--verify", "--quiet", "HEAD^{tree}"); err != nil {
			return false, err
		}
	}
	want := map[string]bool{}
	var removed []string
	for _, c := range changes {
		switch c.Status {
		case 'D':
			removed = append(removed, c.Old)
		case 'R':
			removed = append(removed, c.Old)
			want[c.New] = true
		default:
			want[c.New] = true
		}
	}
	staged, err := r.stagedEntries(ctx, want)
	if err != nil {
		return false, err
	}
	zero := strings.Repeat("0", 40)
	if len(head) > 40 {
		zero = strings.Repeat("0", len(head))
	}
	var in bytes.Buffer
	// Removals first: a name that was a file and is a folder now (or the
	// other way round) must be gone before what replaces it goes in.
	for _, p := range removed {
		if !want[p] {
			fmt.Fprintf(&in, "0 %s 0\t%s\x00", zero, p)
		}
	}
	for _, e := range staged {
		in.WriteString(e)
		in.WriteByte(0)
	}
	if _, err := r.gitIn(ctx, ienv, in.Bytes(), append(noSplit, "update-index", "-z", "--index-info")...); err != nil {
		return false, err
	}
	tree, err := r.gitIn(ctx, ienv, nil, append(noSplit, "write-tree")...)
	if err != nil {
		return false, err
	}
	if tree == headTree {
		return false, nil // the change netted out to nothing
	}
	args, err := commitTreeArgs(tree, head)
	if err != nil {
		return false, err
	}
	commit, err := r.gitIn(ctx, env, []byte(message+"\n"), args...)
	if err != nil {
		return false, err
	}
	// The branch moves only if it is still where this commit was built.
	if args, err = advanceHeadArgs(commit, head); err != nil {
		return false, err
	}
	if _, err := r.git(ctx, args...); err != nil {
		return false, err
	}
	return true, nil
}

// tempIndexDir makes the directory for a commit's own index, inside the git
// directory: on the same disk as the repository, and found again after a
// crash. Directories a crash left behind (older than any commit takes) are
// removed first, so they do not pile up.
func (r *Repo) tempIndexDir(ctx context.Context) (string, error) {
	gd, err := r.absGitDir(ctx)
	if err != nil {
		return "", err
	}
	if old, _ := filepath.Glob(filepath.Join(gd, tempIndexPrefix+"*")); len(old) > 0 {
		for _, d := range old {
			if fi, err := os.Lstat(d); err == nil && fi.IsDir() && time.Since(fi.ModTime()) > time.Hour {
				os.RemoveAll(d)
			}
		}
	}
	return os.MkdirTemp(gd, tempIndexPrefix)
}

const tempIndexPrefix = "petalbook-index-"

// stagedEntries returns the real index's entries of these paths, each as
// "mode id stage<TAB>path": what update-index --index-info takes. A path that
// is not staged is an error: there would be nothing to commit for it.
func (r *Repo) stagedEntries(ctx context.Context, want map[string]bool) ([]string, error) {
	if len(want) == 0 {
		return nil, nil
	}
	// Asked for by name, a command line's worth at a time: the whole index
	// is never listed for the sake of a few files.
	paths := make([]string, 0, len(want))
	for p := range want {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var entries []string
	found := map[string]bool{}
	for len(paths) > 0 {
		n := min(len(paths), maxArgPaths)
		out, err := r.run(ctx, Cmd{Args: append([]string{"ls-files", "-s", "-z", "--"}, paths[:n]...)})
		if err != nil {
			return nil, err
		}
		paths = paths[n:]
		for _, e := range strings.Split(out, "\x00") {
			// A name given may be a folder to Git and bring its files: only
			// the names asked for are taken.
			if _, p, ok := strings.Cut(e, "\t"); ok && want[p] && !found[p] {
				entries = append(entries, e)
				found[p] = true
			}
		}
	}
	for p := range want {
		if !found[p] {
			return nil, fmt.Errorf("%q is part of the change but is not staged", p)
		}
	}
	return entries, nil
}

// exactNames answers whether a path exists under exactly the name given, by
// the listing of each directory on the way (read once per directory). A name
// in another Unicode form than the one asked for counts only when the file
// system itself takes the two for one (macOS hands back names in a form of
// its own): on one that does not, they are two different files, and the one
// asked for is not there.
type exactNames struct {
	root string
	dirs map[string]dirNames
}

type dirNames struct {
	exact map[string]fs.DirEntry
	nfc   map[string][]fs.DirEntry
}

func (x exactNames) list(dir string) (dirNames, error) {
	if m, ok := x.dirs[dir]; ok {
		return m, nil
	}
	m := dirNames{exact: map[string]fs.DirEntry{}, nfc: map[string][]fs.DirEntry{}}
	entries, err := os.ReadDir(filepath.Join(x.root, filepath.FromSlash(dir)))
	// No such directory (or a file where one is expected): nothing is in it.
	if err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
		return dirNames{}, err
	}
	for _, e := range entries {
		m.exact[e.Name()] = e
		k := norm.NFC.String(e.Name())
		m.nfc[k] = append(m.nfc[k], e)
	}
	x.dirs[dir] = m
	return m, nil
}

// entry is the directory entry of rel, or nil when no file or directory has
// exactly that name.
func (x exactNames) entry(rel string) (fs.DirEntry, error) {
	dir := ""
	segs := strings.Split(rel, "/")
	for i, seg := range segs {
		m, err := x.list(dir)
		if err != nil {
			return nil, err
		}
		e := m.exact[seg]
		if e == nil {
			e, err = x.sameForm(dir, seg, m.nfc[norm.NFC.String(seg)])
			if err != nil {
				return nil, err
			}
		}
		if e == nil || i == len(segs)-1 {
			return e, nil
		}
		dir = path.Join(dir, seg)
	}
	return nil, nil
}

// sameForm picks, of the entries whose names differ from seg only in Unicode
// form, the one the file system finds under seg; nil if it finds none.
func (x exactNames) sameForm(dir, seg string, cands []fs.DirEntry) (fs.DirEntry, error) {
	if len(cands) == 0 {
		return nil, nil
	}
	fi, err := os.Lstat(filepath.Join(x.root, filepath.FromSlash(dir), seg))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			return nil, nil
		}
		return nil, err
	}
	for _, c := range cands {
		if ci, err := c.Info(); err == nil && os.SameFile(fi, ci) {
			return c, nil
		}
	}
	return nil, nil
}

// unstageable reports the errors of staging a path that trying again cannot
// cure: the path is behind a symbolic link, or is not a file or a link.
func unstageable(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "beyond a symbolic link") || strings.Contains(msg, "unsupported file type")
}

// SkippedError says that CommitPaths committed what it could and left these
// paths out: Git could not stage them (see CommitPaths). A caller that keeps
// committing in the background goes on; one that promised the change to
// someone treats it as the failure it is.
type SkippedError struct{ Paths []string }

func (e *SkippedError) Error() string {
	return fmt.Sprintf("git could not stage %d path(s), first %q; they were left out of the commit", len(e.Paths), e.Paths[0])
}

// ModifiedTracked lists tracked files that differ from the index or the
// index from HEAD. Untracked files are not looked at.
func (r *Repo) ModifiedTracked(ctx context.Context) ([]string, error) {
	out, err := r.run(ctx, Cmd{Args: []string{"status", "--porcelain", "-z", "--untracked-files=no"}})
	if err != nil {
		return nil, err
	}
	f := strings.Split(strings.TrimRight(out, "\x00"), "\x00")
	var paths []string
	for i := 0; i < len(f); i++ {
		e := f[i]
		if len(e) < 4 {
			continue
		}
		paths = append(paths, e[3:])
		if e[0] == 'R' || e[0] == 'C' || e[1] == 'R' || e[1] == 'C' {
			i++
			if i < len(f) {
				paths = append(paths, f[i])
			}
		}
	}
	return paths, nil
}

// Discard undoes an unpushed change: HEAD's commit (if parent differs from
// HEAD) and the files it touched go back to what parent had, and files that
// parent did not have are removed. Only these paths are touched; nothing else
// in the work tree is reset. It does at most four git commands, whatever the
// number of paths.
func (r *Repo) Discard(ctx context.Context, parent string, paths []string) error {
	if parent == "" {
		return errors.New("no commit to go back to")
	}
	if _, err := r.git(ctx, "reset", "--quiet", parent); err != nil {
		return err
	}
	if len(paths) == 0 {
		return nil
	}
	// Which of the paths the parent had. A few are asked for by name; many
	// would not fit on a command line, so then the whole tree is listed once
	// and looked through here.
	args := []string{"ls-tree", "-r", "-z", "--name-only", parent}
	few := len(paths) <= maxArgPaths
	if few {
		args = append(append(args, "--"), paths...)
	}
	out, err := r.run(ctx, Cmd{Args: args})
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, p := range paths {
		want[p] = true
	}
	had := map[string]bool{}
	var restore []string
	for _, p := range strings.Split(strings.TrimRight(out, "\x00"), "\x00") {
		if p != "" && (few || want[p]) {
			had[p] = true
			restore = append(restore, p)
		}
	}
	// What the change added goes first, and what it replaced or removed comes
	// back after. The other way round, a file renamed to another spelling of
	// its own name (a change of case, on a file system that ignores case) would
	// be restored under the old name and then deleted under the new one, which
	// there is the same file.
	var errs []error
	for _, p := range paths {
		if had[p] {
			continue
		}
		abs, err := safeJoin(r.opt.Dir, p)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := os.Remove(abs); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if len(restore) > 0 {
		if _, err := r.gitIn(ctx, nil, nulList(restore), "checkout", "--quiet", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
			return errors.Join(append(errs, err)...)
		}
	}
	return errors.Join(errs...)
}

// maxArgPaths is how many paths are put on a git command line; more go
// through standard input or are filtered here.
const maxArgPaths = 100

// safeJoin is dir plus rel, refusing a path that goes through a symbolic link.
func safeJoin(dir, rel string) (string, error) {
	p := dir
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("unusable path %q", rel)
		}
		p = filepath.Join(p, seg)
		if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("path %q goes through a symbolic link", rel)
		}
	}
	return p, nil
}

// ResetHard moves the branch and the work tree to a commit: for a space whose
// work tree holds nothing that is not committed.
func (r *Repo) ResetHard(ctx context.Context, commit string) error {
	if !commitRe.MatchString(commit) {
		return fmt.Errorf("not a commit id: %q", commit)
	}
	_, err := r.git(ctx, "reset", "--quiet", "--hard", commit)
	return err
}

// Connecting -----------------------------------------------------------------

// RemoteURL returns the URL of origin, or "" when there is none.
func (r *Repo) RemoteURL(ctx context.Context) string {
	out, err := r.git(ctx, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	return out
}

// SetRemote makes origin point at url (which must have passed ParseURL).
func (r *Repo) SetRemote(ctx context.Context, url string) error {
	verb := "add"
	if r.RemoteURL(ctx) != "" {
		verb = "set-url"
	}
	_, err := r.git(ctx, "remote", verb, "origin", url)
	return err
}

// RemoveRemote forgets origin and its remote-tracking branches.
func (r *Repo) RemoveRemote(ctx context.Context) error {
	if r.RemoteURL(ctx) == "" {
		return nil
	}
	_, err := r.git(ctx, "remote", "remove", "origin")
	return err
}

// HasBranch asks the remote whether it has the branch.
func (r *Repo) HasBranch(ctx context.Context, branch string) (bool, error) {
	if err := validBranch(branch); err != nil {
		return false, err
	}
	out, err := r.net(ctx, 0, "ls-remote", "--heads", "origin", "refs/heads/"+branch)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// Fetch brings the remote branch to refs/remotes/origin/<branch>. The first
// fetch of a whole repository may take a long time; initial says so.
func (r *Repo) Fetch(ctx context.Context, branch string, initial bool) error {
	if err := validBranch(branch); err != nil {
		return err
	}
	t := r.opt.NetTimeout
	if initial {
		t = r.opt.InitialTimeout
	}
	_, err := r.net(ctx, t, "fetch", "--quiet", "--no-tags", "origin", branch)
	return err
}

// Push pushes HEAD to the branch on origin: a plain fast-forward push built
// by pushArgs (safegit.go), the only function that builds a push command.
func (r *Repo) Push(ctx context.Context, branch string) error {
	args, err := pushArgs(branch)
	if err != nil {
		return err
	}
	_, err = r.net(ctx, 0, args...)
	return err
}

// AheadBehind counts the commits only HEAD has and the commits only the
// fetched remote branch has.
func (r *Repo) AheadBehind(ctx context.Context, branch string) (ahead, behind int, err error) {
	if err := validBranch(branch); err != nil {
		return 0, 0, err
	}
	out, err := r.git(ctx, "rev-list", "--left-right", "--count", "HEAD..."+remoteRef(branch))
	if err != nil {
		return 0, 0, err
	}
	f := strings.Fields(out)
	if len(f) != 2 {
		return 0, 0, fmt.Errorf("unexpected rev-list output %q", out)
	}
	if ahead, err = strconv.Atoi(f[0]); err != nil {
		return 0, 0, err
	}
	behind, err = strconv.Atoi(f[1])
	return
}

// FastForward moves the branch to the fetched remote branch. It fails, and
// changes nothing, if that is not a fast-forward or would overwrite a file.
func (r *Repo) FastForward(ctx context.Context, branch string) error {
	if err := validBranch(branch); err != nil {
		return err
	}
	_, err := r.git(ctx, "merge", "--quiet", "--ff-only", remoteRef(branch))
	return err
}

// IsAncestor reports whether a is an ancestor of (or equal to) b.
func (r *Repo) IsAncestor(ctx context.Context, a, b string) (bool, error) {
	if !commitRe.MatchString(a) || !commitRe.MatchString(b) {
		return false, errors.New("not a commit id")
	}
	_, err := r.git(ctx, "merge-base", "--is-ancestor", a, b)
	if err == nil {
		return true, nil
	}
	var ge *GitError
	if errors.As(err, &ge) && (ge.ExitCode() == 1 || ge.ExitCode() == 128) {
		return false, nil
	}
	return false, err
}

// Landed answers "did the push go through after all?" when its answer was
// lost: it fetches and reports whether HEAD is contained in the remote branch.
func (r *Repo) Landed(ctx context.Context, branch string) (bool, error) {
	if err := r.Fetch(ctx, branch, false); err != nil {
		return false, err
	}
	head, tip := r.Head(ctx), r.RemoteTip(ctx, branch)
	if head == "" || tip == "" {
		return false, nil
	}
	return r.IsAncestor(ctx, head, tip)
}

// DiffNames lists the files that differ between two commits, without rename
// detection (a rename is a deletion and an addition).
func (r *Repo) DiffNames(ctx context.Context, from, to string) ([]Change, error) {
	if !commitRe.MatchString(from) || !commitRe.MatchString(to) {
		return nil, errors.New("not a commit id")
	}
	out, err := r.run(ctx, Cmd{Args: []string{"diff", "--name-status", "-z", "--no-renames", from, to}})
	if err != nil {
		return nil, err
	}
	return ParseNameStatus([]byte(out)), nil
}

// KeepUnpushed makes the commit reachable under refs/petalbook/unpushed/<name>
// so that moving the branch cannot lose it, and returns the full ref name.
func (r *Repo) KeepUnpushed(ctx context.Context, commit string, now time.Time) (string, error) {
	name := now.UTC().Format("20060102-150405")
	for i := 0; ; i++ {
		n := name
		if i > 0 {
			n = fmt.Sprintf("%s-%d", name, i)
		}
		if r.CommitOf(ctx, keepRefPrefix+n) != "" {
			if i > 50 {
				return "", errors.New("could not find a free name for the kept commits")
			}
			continue
		}
		args, err := keepRefArgs(n, commit)
		if err != nil {
			return "", err
		}
		if _, err := r.git(ctx, args...); err != nil {
			return "", err
		}
		return keepRefPrefix + n, nil
	}
}

// CountNotIn counts the commits reachable from HEAD that are not reachable
// from other.
func (r *Repo) CountNotIn(ctx context.Context, other string) (int, error) {
	if !commitRe.MatchString(other) {
		return 0, errors.New("not a commit id")
	}
	out, err := r.git(ctx, "rev-list", "--count", "HEAD", "--not", other)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(out)
}

// TouchesOutside reports whether any commit in the history of HEAD changed a
// file other than under the given top-level directory (".petalbook" for a
// space): "has anything beyond its own set-up ever been put in here". It is
// false for a repository with no commit.
func (r *Repo) TouchesOutside(ctx context.Context, dir string) (bool, error) {
	if r.Head(ctx) == "" {
		return false, nil
	}
	if strings.ContainsAny(dir, "/:*?[\\ ") || strings.HasPrefix(dir, "-") || dir == "" {
		return false, fmt.Errorf("unusable directory %q", dir)
	}
	// The one command that wants a pattern (an exclusion), so the one that turns
	// literal paths off; dir was checked above to hold nothing a pattern reads.
	out, err := r.run(ctx, Cmd{Args: []string{"log", "-n", "1", "--format=%H", "--", ".", ":(exclude)" + dir}, Env: []string{"GIT_LITERAL_PATHSPECS=0"}})
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}
