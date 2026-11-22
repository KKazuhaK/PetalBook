package gitstore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
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
	var present, gone []string
	for _, p := range paths {
		if _, err := os.Lstat(filepath.Join(r.opt.Dir, filepath.FromSlash(p))); err == nil {
			present = append(present, p)
		} else {
			gone = append(gone, p)
		}
	}
	if len(present) > 0 {
		if _, err := r.gitIn(ctx, nil, nulList(present), "add", "-A", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
			return false, err
		}
	}
	if len(gone) > 0 {
		// A path that was created and deleted before it was ever committed is
		// unknown to git; --ignore-unmatch makes that fine.
		if _, err := r.gitIn(ctx, nil, nulList(gone), "rm", "--cached", "-r", "-q", "--ignore-unmatch", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
			return false, err
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
	var touched []string
	for _, c := range ParseNameStatus([]byte(out)) {
		if mine[c.Old] || mine[c.New] {
			changes = append(changes, c)
			touched = append(touched, c.New)
			if c.Old != c.New {
				touched = append(touched, c.Old)
			}
		}
	}
	if len(changes) == 0 {
		return false, nil
	}
	env := []string{
		"GIT_AUTHOR_NAME=" + author.Name, "GIT_AUTHOR_EMAIL=" + author.Email,
		"GIT_COMMITTER_NAME=" + committer.Name, "GIT_COMMITTER_EMAIL=" + committer.Email,
	}
	if _, err := r.gitIn(ctx, env, nulList(touched), "-c", "commit.gpgsign=false", "commit", "--quiet", "--no-verify",
		"--message="+message(changes), "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
		return false, err
	}
	return true, nil
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
	args := append([]string{"ls-tree", "-r", "-z", "--name-only", parent, "--"}, paths...)
	out, err := r.run(ctx, Cmd{Args: args})
	if err != nil {
		return err
	}
	had := map[string]bool{}
	var restore []string
	for _, p := range strings.Split(strings.TrimRight(out, "\x00"), "\x00") {
		if p != "" {
			had[p] = true
			restore = append(restore, p)
		}
	}
	if len(restore) > 0 {
		if _, err := r.gitIn(ctx, nil, nulList(restore), "checkout", "--quiet", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
			return err
		}
	}
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
	return errors.Join(errs...)
}

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
