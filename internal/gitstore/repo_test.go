package gitstore

import (
	"context"
	"errors"
	"fmt"
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

// A file renamed to another spelling of its own name, then undone: on a file
// system that ignores case the two names are one file, and the undo must end
// with it there, not deleted.
func TestDiscardOfACaseOnlyRenameKeepsTheFile(t *testing.T) {
	needGit(t)
	ctx := context.Background()
	r := newClone(t, bareRemote(t), nil)
	write(t, r.Dir(), "apple.md", "fruit\n")
	write(t, r.Dir(), "dir/in.md", "inside\n")
	commit(t, r, "apple.md", "dir/in.md")
	parent := r.Head(ctx)
	if _, err := os.Stat(filepath.Join(r.Dir(), "APPLE.md")); err != nil {
		t.Skip("this file system tells names apart by case")
	}
	respell(t, r.Dir(), "apple.md", "Apple.md")
	respell(t, r.Dir(), "dir", "Dir")
	if err := r.Discard(ctx, parent, []string{"apple.md", "Apple.md", "dir/in.md", "Dir/in.md"}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"apple.md": "fruit\n", "dir/in.md": "inside\n"} {
		if b, err := os.ReadFile(filepath.Join(r.Dir(), filepath.FromSlash(name))); err != nil || string(b) != want {
			t.Fatalf("%s after the undo: %q %v", name, b, err)
		}
	}
	if mod, err := r.ModifiedTracked(ctx); err != nil || len(mod) != 0 {
		t.Fatalf("the clone is not as the parent had it: %v %v", mod, err)
	}
}

// respell renames a file or folder to another spelling of its name through
// a third name: Linux takes a rename to a name its disk folds to the same
// entry for a rename onto itself and changes nothing; macOS respells either way.
func respell(t testing.TB, dir, from, to string) {
	t.Helper()
	mid := filepath.Join(dir, "respell-tmp")
	if err := os.Rename(filepath.Join(dir, from), mid); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(mid, filepath.Join(dir, to)); err != nil {
		t.Fatal(err)
	}
}

// commitErr is commit that hands the error back.
func commitErr(r *Repo, paths ...string) (bool, error) {
	return r.CommitPaths(context.Background(), paths, msg, person, svc)
}

func tree(t testing.TB, r *Repo) string {
	return sh(t, r.Dir(), "-c", "core.quotepath=off", "ls-tree", "-r", "--name-only", "HEAD")
}

// An ordinary rename (file and folder) is committed as one: the old names go
// and the new ones come, on any file system.
func TestRenameIsCommitted(t *testing.T) {
	needGit(t)
	r := newClone(t, bareRemote(t), nil)
	write(t, r.Dir(), "apple.md", "fruit\n")
	write(t, r.Dir(), "dir/in.md", "inside\n")
	write(t, r.Dir(), "stays.md", "s\n")
	commit(t, r, "apple.md", "dir/in.md", "stays.md")
	os.Rename(filepath.Join(r.Dir(), "apple.md"), filepath.Join(r.Dir(), "pear.md"))
	os.Rename(filepath.Join(r.Dir(), "dir"), filepath.Join(r.Dir(), "other"))
	commit(t, r, "apple.md", "pear.md", "dir/in.md", "other/in.md")
	if got := tree(t, r); got != "other/in.md\npear.md\nstays.md" {
		t.Fatalf("%q", got)
	}
	if out := sh(t, r.Dir(), "status", "--porcelain"); out != "" {
		t.Fatalf("%q", out)
	}
}

// A file and a folder renamed to another spelling of their own names. A file
// system that ignores case still answers for the old names, and Git keeps the
// spelling it knows unless the old one is taken out first: the commit must
// hold the new names and only them.
func TestRenameByCaseIsCommitted(t *testing.T) {
	needGit(t)
	ctx := context.Background()
	r := newClone(t, bareRemote(t), nil)
	write(t, r.Dir(), "Apple.md", "fruit\n")
	write(t, r.Dir(), "notes/One.md", "one\n")
	write(t, r.Dir(), "notes/sub/Two.md", "two\n")
	commit(t, r, "Apple.md", "notes/One.md", "notes/sub/Two.md")
	if _, err := os.Stat(filepath.Join(r.Dir(), "APPLE.md")); err != nil {
		t.Skip("this file system tells names apart by case")
	}
	respell(t, r.Dir(), "Apple.md", "apple.md")
	respell(t, r.Dir(), "notes", "Notes")
	write(t, r.Dir(), "theirs.txt", "staged by a person\n")
	sh(t, r.Dir(), "add", "theirs.txt")
	defer func() {
		if out := sh(t, r.Dir(), "status", "--porcelain"); out != "A  theirs.txt" {
			t.Errorf("the person's staged file: %q", out)
		}
	}()
	made, err := commitErr(r, "Apple.md", "apple.md", "notes/One.md", "Notes/One.md", "notes/sub/Two.md", "Notes/sub/Two.md")
	if err != nil || !made {
		t.Fatalf("%v %v", made, err)
	}
	if got := tree(t, r); got != "Notes/One.md\nNotes/sub/Two.md\napple.md" {
		t.Fatalf("HEAD has %q", got)
	}
	// Nothing differs but what the person staged themself.
	if mod, err := r.ModifiedTracked(ctx); err != nil || len(mod) != 1 || mod[0] != "theirs.txt" {
		t.Fatalf("%v %v", mod, err)
	}
	// Moved on from there, nothing of the old spelling is left behind.
	os.Rename(filepath.Join(r.Dir(), "Notes"), filepath.Join(r.Dir(), "Other"))
	commit(t, r, "Notes/One.md", "Other/One.md", "Notes/sub/Two.md", "Other/sub/Two.md")
	if got := tree(t, r); got != "Other/One.md\nOther/sub/Two.md\napple.md" {
		t.Fatalf("HEAD has %q", got)
	}
	// A content edit after the rename is an edit of the new name.
	write(t, r.Dir(), "apple.md", "fruit, edited\n")
	commit(t, r, "apple.md")
	if got := sh(t, r.Dir(), "show", "HEAD:apple.md"); got != "fruit, edited" {
		t.Fatalf("%q", got)
	}
}

// The same rename while a person has something of their own staged: the
// rename is committed like any change, and theirs stays staged and
// uncommitted.
func TestRenameByCaseLeavesForeignStagedChangesAlone(t *testing.T) {
	needGit(t)
	r := newClone(t, bareRemote(t), nil)
	write(t, r.Dir(), "Apple.md", "fruit\n")
	write(t, r.Dir(), "plain.md", "p\n")
	commit(t, r, "Apple.md", "plain.md")
	write(t, r.Dir(), "theirs.txt", "staged by a person\n")
	sh(t, r.Dir(), "add", "theirs.txt")
	os.Rename(filepath.Join(r.Dir(), "Apple.md"), filepath.Join(r.Dir(), "tmp"))
	os.Rename(filepath.Join(r.Dir(), "tmp"), filepath.Join(r.Dir(), "apple.md"))
	write(t, r.Dir(), "plain.md", "p2\n")
	commit(t, r, "Apple.md", "apple.md", "plain.md")
	if got := tree(t, r); got != "apple.md\nplain.md" {
		t.Fatalf("HEAD has %q", got)
	}
	if out := sh(t, r.Dir(), "status", "--porcelain"); out != "A  theirs.txt" {
		t.Fatalf("the person's staged file: %q", out)
	}
}

// Renamed by case and rewritten in the same change: to Git a deletion and a
// new file. Later changes of the file, and deleting it, commit as usual.
func TestRenameByCaseWithNewContent(t *testing.T) {
	needGit(t)
	ctx := context.Background()
	r := newClone(t, bareRemote(t), nil)
	write(t, r.Dir(), "Apple.md", "a\nb\nc\nd\n")
	commit(t, r, "Apple.md")
	os.Rename(filepath.Join(r.Dir(), "Apple.md"), filepath.Join(r.Dir(), "tmp"))
	os.Rename(filepath.Join(r.Dir(), "tmp"), filepath.Join(r.Dir(), "apple.md"))
	write(t, r.Dir(), "apple.md", "entirely\nother\ntext\nnow\nhere\n")
	commit(t, r, "Apple.md", "apple.md")
	if got := tree(t, r); got != "apple.md" {
		t.Fatalf("HEAD has %q", got)
	}
	write(t, r.Dir(), "apple.md", "and again\n")
	commit(t, r, "apple.md", "Apple.md")
	if got := tree(t, r); got != "apple.md" {
		t.Fatalf("HEAD has %q", got)
	}
	os.Remove(filepath.Join(r.Dir(), "apple.md"))
	write(t, r.Dir(), "other.md", "o\n")
	commit(t, r, "apple.md", "other.md")
	if got := tree(t, r); got != "other.md" {
		t.Fatalf("HEAD has %q", got)
	}
	if mod, err := r.ModifiedTracked(ctx); err != nil || len(mod) != 0 {
		t.Fatalf("%v %v", mod, err)
	}
}

// Names a file system may hold to be the same without their being so letter
// for letter in lower case: ß and ss, the Greek sigmas. Whatever the disk
// thinks, the commit has the new names and only them.
func TestRenameAcrossUnicodeFolds(t *testing.T) {
	needGit(t)
	ctx := context.Background()
	for _, c := range [][2]string{{"Straße", "Strasse"}, {"ΟΔΟΣ", "οδος"}, {"Ünïcode", "ünïcode"}} {
		t.Run(c[0], func(t *testing.T) {
			r := newClone(t, bareRemote(t), nil)
			write(t, r.Dir(), c[0]+"/一.md", "one\n")
			write(t, r.Dir(), c[0]+"/二.md", "two\n")
			commit(t, r, c[0]+"/一.md", c[0]+"/二.md")
			_, serr := os.Stat(filepath.Join(r.Dir(), c[1]))
			folded := serr == nil // the disk takes the two names for one
			os.Rename(filepath.Join(r.Dir(), c[0]), filepath.Join(r.Dir(), "tmp"))
			if err := os.Rename(filepath.Join(r.Dir(), "tmp"), filepath.Join(r.Dir(), c[1])); err != nil {
				t.Fatal(err)
			}
			commit(t, r, c[0]+"/一.md", c[1]+"/一.md", c[0]+"/二.md", c[1]+"/二.md")
			if got := tree(t, r); got != c[1]+"/一.md\n"+c[1]+"/二.md" {
				t.Fatalf("folded=%v, HEAD has %q", folded, got)
			}
			if mod, err := r.ModifiedTracked(ctx); err != nil || len(mod) != 0 {
				t.Fatalf("%v %v", mod, err)
			}
			if got, err := r.TrackedUnder(ctx, c[1]); err != nil || len(got) != 2 {
				t.Fatalf("%v %v", got, err)
			}
			if got, _ := r.TrackedUnder(ctx, c[0]); len(got) != 0 {
				t.Fatalf("the old name still has %v", got)
			}
		})
	}
}

// A directory of the change that cannot be listed is an error, not a
// deletion of everything in it.
func TestCommitDoesNotTakeAnUnreadableDirectoryForAnEmptyOne(t *testing.T) {
	needGit(t)
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	r := newClone(t, bareRemote(t), nil)
	write(t, r.Dir(), "locked/a.md", "a\n")
	commit(t, r, "locked/a.md")
	dir := filepath.Join(r.Dir(), "locked")
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755)
	if _, err := os.ReadDir(dir); err == nil {
		t.Skip("this file system does not keep a directory from being listed")
	}
	if made, err := commitErr(r, "locked/a.md"); err == nil || made {
		t.Fatalf("made=%v err=%v", made, err)
	}
	os.Chmod(dir, 0o755)
	if got := tree(t, r); got != "locked/a.md" {
		t.Fatalf("HEAD has %q", got)
	}
	if out := sh(t, r.Dir(), "status", "--porcelain"); out != "" {
		t.Fatalf("something was staged: %q", out)
	}
}

// An undo of a change with thousands of long paths: more than a command line
// holds.
func TestDiscardOfVeryManyPaths(t *testing.T) {
	t.Parallel()
	needGit(t)
	ctx := context.Background()
	r := newClone(t, bareRemote(t), nil)
	long := strings.Repeat("长路径", 20)
	var old, moved []string
	for i := 0; i < 6000; i++ {
		old = append(old, fmt.Sprintf("src/%s/%04d.md", long, i))
		moved = append(moved, fmt.Sprintf("dst/%s/%04d.md", long, i))
	}
	dir := filepath.Join(r.Dir(), "src", long)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range old {
		if err := os.WriteFile(filepath.Join(r.Dir(), filepath.FromSlash(old[i])), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	commit(t, r, old...)
	parent := r.Head(ctx)
	if err := os.Rename(filepath.Join(r.Dir(), "src"), filepath.Join(r.Dir(), "dst")); err != nil {
		t.Fatal(err)
	}
	all := append(append([]string{}, old...), moved...)
	commit(t, r, all...)
	if err := r.Discard(ctx, parent, all); err != nil {
		t.Fatalf("the undo: %v", err)
	}
	if r.Head(ctx) != parent {
		t.Fatal("HEAD did not go back")
	}
	if _, err := os.Stat(filepath.Join(r.Dir(), filepath.FromSlash(old[5999]))); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.Dir(), filepath.FromSlash(moved[0]))); !os.IsNotExist(err) {
		t.Fatalf("the moved file is still there: %v", err)
	}
	if mod, err := r.ModifiedTracked(ctx); err != nil || len(mod) != 0 {
		t.Fatalf("%d files differ: %v", len(mod), err)
	}
}
