package gitstore

// Rewritten-history detection. The store remembers the last remote commit it
// accepted, in .git/petalbook-last-good/<branch> inside its clone. After every
// fetch it checks that origin/<branch> is a descendant of that commit
// (git merge-base --is-ancestor). If it is not, somebody rewrote the remote
// history (a force push, or a deleted and recreated branch): the caller keeps
// serving the last good commit read-only without touching the clone, and
// refuses changes until the new tip is accepted.

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const lastGoodDir = "petalbook-last-good"

var (
	hexRe       = regexp.MustCompile(`^[0-9a-f]{40,64}$`)
	hexPrefixRe = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
)

func (r *Repo) lastGoodPath(ctx context.Context, branch string) (string, error) {
	d, err := r.absGitDir(ctx)
	if err != nil {
		return "", err
	}
	base := filepath.Join(d, lastGoodDir)
	return filepath.Join(base, url.PathEscape(branch)), os.MkdirAll(base, 0o755)
}

// ReadLastGood returns the stored commit of branch, or "" when none is stored.
func (r *Repo) ReadLastGood(ctx context.Context, branch string) string {
	p, err := r.lastGoodPath(ctx, branch)
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	if h := strings.TrimSpace(string(b)); hexRe.MatchString(h) {
		return h
	}
	return ""
}

// WriteLastGood stores the last accepted commit of branch.
func (r *Repo) WriteLastGood(ctx context.Context, branch, hash string) error {
	if !hexRe.MatchString(hash) {
		return fmt.Errorf("not a commit id: %q", hash)
	}
	p, err := r.lastGoodPath(ctx, branch)
	if err != nil {
		return err
	}
	return writeFileSync(p, []byte(hash+"\n"))
}

// writeFileSync writes data to path through a temporary file in the same
// directory (named ".tmp-<random>", which no escaped branch name can equal),
// fsyncs it and renames it into place. Every error is returned.
func writeFileSync(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	fail := func(err error) error {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// VerifyDescends checks that remote contains lastGood. An empty lastGood or
// the same commit passes. A commit that git cannot find counts as not
// contained: when in doubt, changes stop.
func (r *Repo) VerifyDescends(ctx context.Context, lastGood, remote string) (bool, error) {
	if lastGood == "" || lastGood == remote {
		return true, nil
	}
	return r.IsAncestor(ctx, lastGood, remote)
}

// CheckRemote looks at the fetched remote branch against the last good commit
// and returns the remote tip. A rewritten history is a *HistoryRewrittenError;
// a branch that does not exist is ErrNoBranch.
func (r *Repo) CheckRemote(ctx context.Context, branch, lastGood string) (string, error) {
	remote := r.RemoteTip(ctx, branch)
	if remote == "" {
		return "", ErrNoBranch
	}
	ok, err := r.VerifyDescends(ctx, lastGood, remote)
	if err != nil {
		return "", err
	}
	if !ok {
		return remote, &HistoryRewrittenError{LastGood: lastGood, Remote: remote}
	}
	return remote, nil
}

// AcceptTip fetches and checks that commit may be accepted as the new base,
// without touching the work tree: the remote really is not a descendant of
// lastGood (ErrNothingToAccept), and commit is exactly its tip
// (ErrNotRemoteTip). It returns the tip.
func (r *Repo) AcceptTip(ctx context.Context, branch, lastGood, commit string) (string, error) {
	commit = strings.ToLower(strings.TrimSpace(commit))
	if !hexPrefixRe.MatchString(commit) {
		return "", fmt.Errorf("%w: give the full commit id (at least 7 hex characters)", ErrNotRemoteTip)
	}
	if err := r.Fetch(ctx, branch, false); err != nil {
		return "", err
	}
	remote := r.RemoteTip(ctx, branch)
	if remote == "" {
		return "", ErrNoBranch
	}
	ok, err := r.VerifyDescends(ctx, lastGood, remote)
	if err != nil {
		return "", err
	}
	if ok {
		return "", ErrNothingToAccept
	}
	if !strings.HasPrefix(remote, commit) {
		return "", fmt.Errorf("%w (it is %s)", ErrNotRemoteTip, short(remote))
	}
	return remote, nil
}
