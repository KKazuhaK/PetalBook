package gitstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func commit(t testing.TB, r *Repo, paths ...string) {
	t.Helper()
	made, err := r.CommitPaths(context.Background(), paths, msg, person, svc)
	if err != nil || !made {
		t.Fatalf("commit %v: %v %v", paths, made, err)
	}
}

func TestChangePushFetchAndAheadBehind(t *testing.T) {
	needGit(t)
	ctx := context.Background()
	remote := bareRemote(t)
	r := newClone(t, remote, nil)

	if ok, err := r.HasBranch(ctx, "main"); err != nil || ok {
		t.Fatalf("an empty remote has no branch: %v %v", ok, err)
	}
	write(t, r.Dir(), "a.md", "one\n")
	write(t, r.Dir(), "untouched.txt", "stays untracked\n")
	commit(t, r, "a.md")
	if out := sh(t, r.Dir(), "log", "-1", "--format=%an <%ae>|%cn <%ce>|%s"); out != "Ann <ann@example.test>|Service <svc@example.test>|change of 1" {
		t.Fatalf("identity: %s", out)
	}
	if out := sh(t, r.Dir(), "status", "--porcelain"); out != "?? untouched.txt" {
		t.Fatalf("only the named path is committed: %q", out)
	}
	if err := r.Push(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	if ok, err := r.HasBranch(ctx, "main"); err != nil || !ok {
		t.Fatalf("the branch exists now: %v %v", ok, err)
	}

	// Somebody else pushes; we are behind after a fetch and can fast-forward.
	o := other(t, remote)
	write(t, o, "b.md", "two\n")
	sh(t, o, "add", "-A")
	sh(t, o, "commit", "-qm", "b")
	sh(t, o, "push", "-q", "origin", "main")
	if err := r.Fetch(ctx, "main", false); err != nil {
		t.Fatal(err)
	}
	if a, b, err := r.AheadBehind(ctx, "main"); err != nil || a != 0 || b != 1 {
		t.Fatalf("ahead/behind %d/%d %v", a, b, err)
	}
	old := r.Head(ctx)
	if err := r.FastForward(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	ch, err := r.DiffNames(ctx, old, r.Head(ctx))
	if err != nil || len(ch) != 1 || ch[0].Status != 'A' || ch[0].New != "b.md" {
		t.Fatalf("diff %v %v", ch, err)
	}

	// A push of a commit based on a moved remote is rejected, as a plain one.
	write(t, r.Dir(), "c.md", "three\n")
	commit(t, r, "c.md")
	write(t, o, "d.md", "four\n")
	sh(t, o, "add", "-A")
	sh(t, o, "commit", "-qm", "d")
	sh(t, o, "push", "-q", "origin", "main")
	err = r.Push(ctx, "main")
	if KindOf(err) != KindRejected {
		t.Fatalf("expected a rejection, got %v", err)
	}
	if ok, err := r.Landed(ctx, "main"); err != nil || ok {
		t.Fatalf("the push did not land: %v %v", ok, err)
	}
}

func TestDiscardRestoresOnlyTheNamedFiles(t *testing.T) {
	needGit(t)
	ctx := context.Background()
	r := newClone(t, bareRemote(t), nil)
	write(t, r.Dir(), "keep.md", "keep\n")
	write(t, r.Dir(), "edit.md", "v1\n")
	write(t, r.Dir(), "gone.md", "gone\n")
	commit(t, r, "keep.md", "edit.md", "gone.md")
	parent := r.Head(ctx)

	// A change that edits, creates, deletes and renames, and is committed.
	write(t, r.Dir(), "edit.md", "v2\n")
	write(t, r.Dir(), "new/created.md", "created\n")
	os.Remove(filepath.Join(r.Dir(), "gone.md"))
	if err := os.Rename(filepath.Join(r.Dir(), "keep.md"), filepath.Join(r.Dir(), "kept.md")); err != nil {
		t.Fatal(err)
	}
	write(t, r.Dir(), "unrelated.txt", "not ours\n")
	paths := []string{"edit.md", "new/created.md", "gone.md", "keep.md", "kept.md"}
	commit(t, r, paths...)
	if r.Head(ctx) == parent {
		t.Fatal("no commit")
	}

	if err := r.Discard(ctx, parent, paths); err != nil {
		t.Fatal(err)
	}
	if r.Head(ctx) != parent {
		t.Fatal("HEAD did not go back")
	}
	for f, want := range map[string]string{"edit.md": "v1\n", "gone.md": "gone\n", "keep.md": "keep\n", "unrelated.txt": "not ours\n"} {
		if b, err := os.ReadFile(filepath.Join(r.Dir(), f)); err != nil || string(b) != want {
			t.Errorf("%s: %q %v", f, b, err)
		}
	}
	for _, f := range []string{"new/created.md", "kept.md"} {
		if _, err := os.Stat(filepath.Join(r.Dir(), f)); err == nil {
			t.Errorf("%s was not removed", f)
		}
	}
	if out := sh(t, r.Dir(), "status", "--porcelain"); out != "?? new/\n?? unrelated.txt" && out != "?? unrelated.txt" {
		t.Fatalf("status: %q", out)
	}
	// An uncommitted change is undone the same way, with parent = HEAD.
	write(t, r.Dir(), "edit.md", "v3\n")
	if err := r.Discard(ctx, r.Head(ctx), []string{"edit.md"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(r.Dir(), "edit.md")); string(b) != "v1\n" {
		t.Fatalf("%q", b)
	}
}

func TestDiscardNeverFollowsASymlinkOutOfTheTree(t *testing.T) {
	needGit(t)
	ctx := context.Background()
	r := newClone(t, bareRemote(t), nil)
	write(t, r.Dir(), "a.md", "a\n")
	commit(t, r, "a.md")
	outside := t.TempDir()
	write(t, outside, "victim.md", "precious\n")
	if err := os.Symlink(outside, filepath.Join(r.Dir(), "link")); err != nil {
		t.Skip("no symlinks")
	}
	err := r.Discard(ctx, r.Head(ctx), []string{"link/victim.md"})
	if err == nil {
		t.Fatal("a path through a link was accepted")
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "victim.md")); string(b) != "precious\n" {
		t.Fatal("the file outside the tree was deleted")
	}
}

func TestRewrittenHistoryAndAccept(t *testing.T) {
	needGit(t)
	ctx := context.Background()
	remote := bareRemote(t)
	r := newClone(t, remote, nil)
	write(t, r.Dir(), "a.md", "a\n")
	commit(t, r, "a.md")
	if err := r.Push(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	good := r.Head(ctx)
	if err := r.WriteLastGood(ctx, "main", good); err != nil {
		t.Fatal(err)
	}
	if r.ReadLastGood(ctx, "main") != good {
		t.Fatal("last good not stored")
	}

	// The remote gains a commit: still a descendant.
	o := other(t, remote)
	write(t, o, "b.md", "b\n")
	sh(t, o, "add", "-A")
	sh(t, o, "commit", "-qm", "b")
	sh(t, o, "push", "-q", "origin", "main")
	if err := r.Fetch(ctx, "main", false); err != nil {
		t.Fatal(err)
	}
	tip, err := r.CheckRemote(ctx, "main", good)
	if err != nil || tip == good {
		t.Fatalf("%q %v", tip, err)
	}

	// Somebody rewrites the history under us (with plain git: the store never does).
	sh(t, o, "checkout", "-q", "--orphan", "fresh")
	sh(t, o, "rm", "-rfq", ".")
	write(t, o, "z.md", "z\n")
	sh(t, o, "add", "-A")
	sh(t, o, "commit", "-qm", "new root content")
	sh(t, o, "push", "-q", "--force", "origin", "fresh:main")
	if err := r.Fetch(ctx, "main", false); err != nil {
		t.Fatal(err)
	}
	_, err = r.CheckRemote(ctx, "main", good)
	var hr *HistoryRewrittenError
	if !errors.Is(err, ErrHistoryRewritten) || !errors.As(err, &hr) || hr.LastGood != good {
		t.Fatalf("expected a rewrite: %v", err)
	}
	// Accepting needs exactly the tip.
	if _, err := r.AcceptTip(ctx, "main", good, good); !errors.Is(err, ErrNotRemoteTip) {
		t.Fatalf("not the tip: %v", err)
	}
	if _, err := r.AcceptTip(ctx, "main", good, "zz"); !errors.Is(err, ErrNotRemoteTip) {
		t.Fatalf("garbage: %v", err)
	}
	newTip := sh(t, o, "rev-parse", "HEAD")
	got, err := r.AcceptTip(ctx, "main", good, newTip)
	if err != nil || got != newTip {
		t.Fatalf("%v %v", got, err)
	}
	// Once it is the last good there is nothing to accept.
	if _, err := r.AcceptTip(ctx, "main", newTip, newTip); !errors.Is(err, ErrNothingToAccept) {
		t.Fatalf("%v", err)
	}

	// Unpushed work is kept reachable before the clone moves.
	write(t, r.Dir(), "mine.md", "mine\n")
	commit(t, r, "mine.md")
	mine := r.Head(ctx)
	ref, err := r.KeepUnpushed(ctx, mine, time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC))
	if err != nil || ref != "refs/petalbook/unpushed/20261121-080000" {
		t.Fatalf("%q %v", ref, err)
	}
	if err := r.ResetHard(ctx, newTip); err != nil {
		t.Fatal(err)
	}
	if got := r.CommitOf(ctx, ref); got != mine {
		t.Fatalf("the kept commit is not reachable: %q", got)
	}
	// A second keep in the same second gets another name.
	ref2, err := r.KeepUnpushed(ctx, mine, time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC))
	if err != nil || ref2 == ref {
		t.Fatalf("%q %v", ref2, err)
	}
}

func TestTouchesOutside(t *testing.T) {
	needGit(t)
	ctx := context.Background()
	r := newClone(t, bareRemote(t), nil)
	if has, err := r.TouchesOutside(ctx, ".petalbook"); err != nil || has {
		t.Fatalf("empty: %v %v", has, err)
	}
	write(t, r.Dir(), ".petalbook/space.yml", "name: x\n")
	commit(t, r, ".petalbook/space.yml")
	if has, err := r.TouchesOutside(ctx, ".petalbook"); err != nil || has {
		t.Fatalf("only set-up: %v %v", has, err)
	}
	write(t, r.Dir(), "p.md", "x\n")
	commit(t, r, "p.md")
	os.Remove(filepath.Join(r.Dir(), "p.md"))
	commit(t, r, "p.md")
	if has, err := r.TouchesOutside(ctx, ".petalbook"); err != nil || !has {
		t.Fatalf("a deleted page still counts: %v %v", has, err)
	}
	if _, err := r.TouchesOutside(ctx, "../x"); err == nil {
		t.Fatal("a path outside was accepted")
	}
}

func TestErrorsAreScrubbed(t *testing.T) {
	for in, want := range map[string]string{
		"fatal: unable to access 'https://user:s3cret@github.com/a/b.git/': error": "https://github.com/a/b.git",
		"remote: token ghp_abcdef rejected":                                        "remote: token *** rejected",
	} {
		got := scrub(in, []string{"ghp_abcdef"})
		if !strings.Contains(got, want) || strings.Contains(got, "s3cret") || strings.Contains(got, "ghp_abcdef") {
			t.Errorf("%q -> %q", in, got)
		}
	}
}

// A path is taken literally. Read as a pattern, "[ab].md" would also match
// "a.md" and "b.md" and sweep them into the commit.
func TestPathsAreNotPatterns(t *testing.T) {
	needGit(t)
	r := newClone(t, bareRemote(t), nil)
	write(t, r.Dir(), "a.md", "a1\n")
	write(t, r.Dir(), "b.md", "b1\n")
	write(t, r.Dir(), "[ab].md", "brackets1\n")
	commit(t, r, "a.md", "b.md", "[ab].md")
	write(t, r.Dir(), "a.md", "a2, not part of the change\n")
	write(t, r.Dir(), "[ab].md", "brackets2\n")
	write(t, r.Dir(), "*.md", "star\n")
	commit(t, r, "[ab].md", "*.md")
	if out := sh(t, r.Dir(), "show", "--name-only", "--format=", "HEAD"); out != "*.md\n[ab].md" {
		t.Fatalf("the commit took files that were not named: %q", out)
	}
	if out := sh(t, r.Dir(), "status", "--porcelain"); out != "M a.md" {
		t.Fatalf("a.md should still be modified and uncommitted: %q", out)
	}
	// Discard is literal too: it puts back only the named file.
	write(t, r.Dir(), "[ab].md", "brackets3\n")
	parent := r.Head(context.Background())
	commit(t, r, "[ab].md")
	if err := r.Discard(context.Background(), parent, []string{"[ab].md"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(r.Dir(), "a.md")); string(b) != "a2, not part of the change\n" {
		t.Fatalf("discarding one file reset another: %q", b)
	}
}
