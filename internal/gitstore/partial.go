package gitstore

// Partial clones (milestone 3.1, docs/api.md "Partial clones"). A space that
// follows a remote can be cloned with --filter=blob:none: every commit and
// every tree, and the contents only of the files of the version that is
// checked out. The contents of older versions are fetched when someone asks
// for them, by their ids, in one request per batch.
//
// Git never fetches on its own here (GIT_NO_LAZY_FETCH, see baseEnv): a
// missing object is an error this package turns into a fetch of its own, with
// the credential, the deadline and the error classes of every other network
// command, or into ErrMissing for the caller to map.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// ErrMissing: objects the clone lacks could not be had.
var ErrMissing = errors.New("objects of the clone are missing and could not be fetched")

// EnsureFunc fetches objects a partial clone lacks. It is called with the ids
// of blobs that are missing, and returns when they are there or cannot be had.
type EnsureFunc func(ctx context.Context, ids []string) error

// SetEnsure puts the repository in partial mode: history reads look for blobs
// they need that are missing and call f for them. nil leaves partial mode.
func (r *Repo) SetEnsure(f EnsureFunc) {
	if f == nil {
		r.ensureFn.Store(nil)
		return
	}
	r.ensureFn.Store(&f)
}

func (r *Repo) ensure() EnsureFunc {
	if p := r.ensureFn.Load(); p != nil {
		return *p
	}
	return nil
}

// IsPartial reports whether the clone is a partial one: origin is a promisor
// remote of it. That is what a filtered fetch sets and what LeavePartial
// removes last, once nothing is missing. The filter itself is no guide:
// StopFilter removes it first, while old versions may still be missing, and a
// clone cut off between the two must still read as partial.
func (r *Repo) IsPartial(ctx context.Context) bool {
	out, err := r.git(ctx, "config", "--local", "--get-regexp", partialKeysPattern)
	if err != nil {
		return false // exit 1: neither is set
	}
	for _, l := range strings.Split(out, "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(l), " ")
		switch strings.ToLower(k) {
		case "extensions.partialclone":
			if v != "" {
				return true
			}
		case "remote.origin.promisor":
			if b := strings.ToLower(v); b == "" || b == "true" || b == "yes" || b == "on" || b == "1" {
				return true
			}
		}
	}
	return false
}

// netIn is net with something on standard input.
func (r *Repo) netIn(ctx context.Context, timeout time.Duration, stdin []byte, args ...string) (string, error) {
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
	out, err := r.run(ctx, Cmd{Args: args, Env: env, Secrets: secrets, Stdin: stdin})
	if err != nil {
		sub, _ := subcommand(args)
		return "", classify(sub, err)
	}
	return strings.TrimRight(out, "\n"), nil
}

// FetchFiltered is Fetch for a partial clone: commits and trees, no file
// contents. The remote becomes a promisor remote of the clone.
func (r *Repo) FetchFiltered(ctx context.Context, branch string, initial bool) error {
	if err := validBranch(branch); err != nil {
		return err
	}
	t := r.opt.NetTimeout
	if initial {
		t = r.opt.InitialTimeout
	}
	_, err := r.net(ctx, t, "fetch", "--quiet", "--no-tags", "--filter=blob:none", "origin", branch)
	return err
}

// blobBatch is how many ids one fetch asks for.
const blobBatch = 20000

// FetchBlobs fetches the contents of these blobs, by id, from origin: one
// request per batch, the way Git's own lazy fetch asks and nothing more.
func (r *Repo) FetchBlobs(ctx context.Context, ids []string) error {
	for len(ids) > 0 {
		n := min(len(ids), blobBatch)
		batch := ids[:n]
		ids = ids[n:]
		for _, id := range batch {
			if !hexRe.MatchString(id) {
				return fmt.Errorf("not an object id: %q", id)
			}
		}
		t := r.opt.NetTimeout
		if n > 1000 {
			t = r.opt.InitialTimeout
		}
		stdin := []byte(strings.Join(batch, "\n") + "\n")
		if _, err := r.netIn(ctx, t, stdin, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--filter=blob:none", "origin", "--stdin"); err != nil {
			return err
		}
	}
	return nil
}

// MissingBlobs returns those of the ids the clone does not have.
func (r *Repo) MissingBlobs(ctx context.Context, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	for _, id := range ids {
		if !hexRe.MatchString(id) {
			return nil, fmt.Errorf("not an object id: %q", id)
		}
	}
	out, err := r.run(ctx, Cmd{Args: []string{"cat-file", "--batch-check"}, Stdin: []byte(strings.Join(ids, "\n") + "\n")})
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if id, ok := strings.CutSuffix(l, " missing"); ok {
			missing = append(missing, id)
		}
	}
	return missing, nil
}

// EnsureBlobs makes sure the clone has these blobs: those it lacks are
// fetched by f (or by FetchBlobs when f is nil). It returns ErrMissing when
// they are still not there.
func (r *Repo) EnsureBlobs(ctx context.Context, ids []string, f EnsureFunc) error {
	missing, err := r.MissingBlobs(ctx, uniqIDs(ids))
	if err != nil || len(missing) == 0 {
		return err
	}
	if f == nil {
		f = r.FetchBlobs
	}
	if err := f(ctx, missing); err != nil {
		return err
	}
	if still, err := r.MissingBlobs(ctx, missing); err != nil {
		return err
	} else if len(still) > 0 {
		return fmt.Errorf("%w (%d of %d)", ErrMissing, len(still), len(missing))
	}
	return nil
}

func uniqIDs(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := ids[:0:0]
	for _, id := range ids {
		if id != "" && !seen[id] && strings.Trim(id, "0") != "" {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// TreeEntry is one file of a commit's tree.
type TreeEntry struct {
	Mode, Type, ID, Path string
}

// ListTree lists every entry of the commit's tree, recursively (files only).
func (r *Repo) ListTree(ctx context.Context, commit string) ([]TreeEntry, error) {
	if !commitRe.MatchString(commit) {
		return nil, errors.New("not a commit id")
	}
	out, err := r.run(ctx, Cmd{Args: []string{"ls-tree", "-r", "-z", commit}})
	if err != nil {
		return nil, err
	}
	var es []TreeEntry
	for _, rec := range strings.Split(out, "\x00") {
		if rec == "" {
			continue
		}
		meta, p, ok := strings.Cut(rec, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 3 {
			return nil, fmt.Errorf("unexpected ls-tree output %q", rec)
		}
		es = append(es, TreeEntry{Mode: f[0], Type: f[1], ID: f[2], Path: p})
	}
	return es, nil
}

// RawChange is one file's change between two commits, with the ids of its contents.
type RawChange struct {
	Status       byte
	Old, New     string // paths
	OldID, NewID string // blob ids; all zeros for none
}

// DiffRaw lists what differs between two commits, with blob ids and without
// rename detection. Nothing is read but the trees.
func (r *Repo) DiffRaw(ctx context.Context, from, to string) ([]RawChange, error) {
	if !commitRe.MatchString(from) || !commitRe.MatchString(to) {
		return nil, errors.New("not a commit id")
	}
	out, err := r.run(ctx, Cmd{Args: []string{"diff", "--raw", "--no-abbrev", "-z", "--no-renames", from, to}})
	if err != nil {
		return nil, err
	}
	return parseRaw(out), nil
}

// parseRaw reads `--raw -z` output: ":mode mode old new S\0path\0", and for
// renames and copies two paths.
func parseRaw(out string) []RawChange {
	tok := strings.Split(strings.TrimRight(out, "\x00"), "\x00")
	var cs []RawChange
	for i := 0; i < len(tok); {
		h := strings.TrimLeft(tok[i], "\n")
		if !strings.HasPrefix(h, ":") {
			i++
			continue
		}
		f := strings.Fields(h[1:])
		if len(f) != 5 || i+1 >= len(tok) {
			return cs
		}
		c := RawChange{Status: f[4][0], OldID: f[2], NewID: f[3], Old: tok[i+1], New: tok[i+1]}
		i += 2
		if c.Status == 'R' || c.Status == 'C' {
			if i >= len(tok) {
				return cs
			}
			c.New = tok[i]
			i++
		}
		cs = append(cs, c)
	}
	return cs
}

// MissingForMove lists the blobs of commit `to` that the clone lacks, the
// ones a move of the work tree from `from` to `to` has to check out: what
// differs between them, or the whole tree when from is "". Nothing is read but trees.
func (r *Repo) MissingForMove(ctx context.Context, from, to string) ([]string, error) {
	var ids []string
	if from == "" {
		es, err := r.ListTree(ctx, to)
		if err != nil {
			return nil, err
		}
		for _, e := range es {
			if e.Type == "blob" {
				ids = append(ids, e.ID)
			}
		}
	} else {
		cs, err := r.DiffRaw(ctx, from, to)
		if err != nil {
			return nil, err
		}
		for _, c := range cs {
			if c.Status != 'D' {
				ids = append(ids, c.NewID)
			}
		}
	}
	return r.MissingBlobs(ctx, uniqIDs(ids))
}

// PrefetchMove fetches what MissingForMove lists, so that the move that
// follows needs nothing from the network. It is the step before a
// fast-forward, a reset or a checkout in a partial clone.
func (r *Repo) PrefetchMove(ctx context.Context, from, to string) error {
	missing, err := r.MissingForMove(ctx, from, to)
	if err != nil || len(missing) == 0 {
		return err
	}
	return r.FetchBlobs(ctx, missing)
}

// parseLogRaw is parseLog for `log --raw -z` output.
func parseLogRaw(out string) []Version {
	tok := strings.Split(out, "\x00")
	var vs []Version
	for i := 0; i < len(tok); {
		h := strings.TrimLeft(tok[i], "\n")
		if !hexRe.MatchString(h) || i+4 > len(tok) {
			i++
			continue
		}
		var v Version
		v.Commit = h
		v.Time, v.Author, v.Via = logHeader(tok[i+1], tok[i+2], tok[i+3])
		i += 4
		for i < len(tok) {
			e := strings.TrimLeft(tok[i], "\n")
			if !strings.HasPrefix(e, ":") {
				break
			}
			f := strings.Fields(e[1:])
			if len(f) != 5 || i+1 >= len(tok) {
				return vs
			}
			v.Status, v.OldID, v.NewID, v.Path = f[4][0], f[2], f[3], tok[i+1]
			v.Similarity = 0
			i += 2
			if v.Status == 'R' || v.Status == 'C' {
				if i >= len(tok) {
					return vs
				}
				v.Path = tok[i]
				i++
				if n, err := atoi(f[4][1:]); err == nil {
					v.Similarity = n
				}
			}
		}
		if v.Status != 0 {
			vs = append(vs, v)
		}
	}
	return vs
}

// GitVersion is the Git the server runs ("2.53.0") and whether it can make
// partial clones that never fetch on their own (2.44 and later:
// GIT_NO_LAZY_FETCH). "" and false when git cannot be run.
func GitVersion(ctx context.Context, run Runner) (string, bool) {
	if run == nil {
		run = defaultRunner
	}
	out, _, err := run.Run(ctx, Cmd{Dir: os.TempDir(), Args: []string{"version"}})
	if err != nil {
		return "", false
	}
	f := strings.Fields(out)
	if len(f) < 3 {
		return "", false
	}
	v := f[2]
	var maj, min int
	if _, err := fmt.Sscanf(v, "%d.%d", &maj, &min); err != nil {
		return v, false
	}
	return v, maj > 2 || (maj == 2 && min >= 44)
}

// UnpushedRefs lists the refs under refs/petalbook/unpushed/: commits that
// moving the clone to the remote's history would have lost and that exist
// nowhere else.
func (r *Repo) UnpushedRefs(ctx context.Context) ([]string, error) {
	out, err := r.git(ctx, "for-each-ref", "--format=%(refname)", keepRefPrefix)
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			refs = append(refs, l)
		}
	}
	return refs, nil
}

// Untracked lists files of the work tree that no commit has and no ignore
// rule hides: what a removal of the clone would lose.
func (r *Repo) Untracked(ctx context.Context) ([]string, error) {
	out, err := r.run(ctx, Cmd{Args: []string{"status", "--porcelain", "-z", "--untracked-files=normal"}})
	if err != nil {
		return nil, err
	}
	var files []string
	for _, rec := range strings.Split(out, "\x00") {
		if strings.HasPrefix(rec, "?? ") {
			files = append(files, rec[3:])
		}
	}
	return files, nil
}

// MissingEverything lists the ids of every object reachable from the commit
// that the clone lacks: what making a partial clone whole has to fetch.
func (r *Repo) MissingEverything(ctx context.Context, commit string) ([]string, error) {
	if !commitRe.MatchString(commit) {
		return nil, errors.New("not a commit id")
	}
	out, err := r.run(ctx, Cmd{Args: []string{"rev-list", "--objects", "--missing=print", commit}})
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, l := range strings.Split(out, "\n") {
		if id, ok := strings.CutPrefix(l, "?"); ok && hexRe.MatchString(id) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// MissingAll lists the ids of every object reachable from any ref that the
// clone lacks.
func (r *Repo) MissingAll(ctx context.Context) ([]string, error) {
	out, err := r.run(ctx, Cmd{Args: []string{"rev-list", "--objects", "--missing=print", "--all"}})
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, l := range strings.Split(out, "\n") {
		if id, ok := strings.CutPrefix(l, "?"); ok && hexRe.MatchString(id) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// StopFilter makes the fetches from origin bring every object again: a fetch
// of a partial clone otherwise keeps the filter the clone was made with.
// Nothing already fetched changes, and the clone stays partial.
func (r *Repo) StopFilter(ctx context.Context) error {
	return r.unsetConfig(ctx, "remote.origin.partialclonefilter")
}

// LeavePartial makes a partial clone that has every object an ordinary one:
// origin is no longer a promisor remote. The caller has checked (MissingAll)
// that nothing is missing; Git never fetches by itself here anyway.
func (r *Repo) LeavePartial(ctx context.Context) error {
	for _, k := range []string{"remote.origin.partialclonefilter", "remote.origin.promisor", "extensions.partialclone"} {
		if err := r.unsetConfig(ctx, k); err != nil {
			return err
		}
	}
	return nil
}

// unsetConfig removes a key of the repository's configuration; a key that is
// not set is fine (git config exits with 5 for it).
func (r *Repo) unsetConfig(ctx context.Context, key string) error {
	_, err := r.git(ctx, "config", "--local", "--unset-all", key)
	var ge *GitError
	if errors.As(err, &ge) && ge.ExitCode() == 5 {
		return nil
	}
	return err
}
