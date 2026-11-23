package space

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KKazuhaK/PetalBook/internal/page"
)

// crashedWithUnpushedCommit leaves the clone as a crash between the commit and
// the push does: a commit the remote does not have, and a journal that was not trimmed.
func (f *rfx) crashedWithUnpushedCommit(title, id string) {
	f.t.Helper()
	f.s.Close()
	p := filepath.Join(f.root, title+".md")
	if err := os.WriteFile(p, []byte(pageText(id, title, "unpushed\n")), 0o644); err != nil {
		f.t.Fatal(err)
	}
	git(f.t, f.root, "add", "-A")
	git(f.t, f.root, "-c", "user.name=Ann", "-c", "user.email=ann@example.test", "commit", "-qm", "Add "+title)
}

// crashedWithUncommittedFile leaves a file written and journaled but never
// committed, as a crash right after the write does.
func (f *rfx) crashedWithUncommittedFile(title, id string) {
	f.t.Helper()
	data := []byte(pageText(id, title, "written\n"))
	if err := f.s.cm.Record(Author{}, jrec{Op: "w", Path: title + ".md", Rev: page.Rev(data)}); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, title+".md"), data, 0o644); err != nil {
		f.t.Fatal(err)
	}
	crash(f.s)
}

func (f *rfx) syncNow() error { return f.s.Pull(context.Background(), true) }

func TestCrashLeavingAnUnpushedCommitIsFinishedOnTheNextSync(t *testing.T) {
	t.Parallel()
	f := newRemoteSpace(t)
	mustCreate(t, f.s, "", meta("title", "Before"), "b\n")
	f.crashedWithUnpushedCommit("Crashed", "CRASH00001")
	pre := f.tip()
	f.reopen()
	// Opening touched no network; the clone is ahead until the first sync.
	if f.tip() != pre {
		t.Fatal("opening pushed")
	}
	if err := f.syncNow(); err != nil {
		t.Fatal(err)
	}
	if f.tip() == pre || f.remoteLog()[0] != "Add Crashed" {
		t.Fatalf("the unpushed commit was not pushed: %v", f.remoteLog())
	}
	if got, err := f.s.Get("CRASH00001"); err != nil || got.Body != "unpushed\n" {
		t.Fatalf("%v %v", got, err)
	}
	if st := f.last(); st.State != RemoteOK || st.LastGood != f.tip() {
		t.Fatalf("%+v", st)
	}
	if git(t, f.root, "rev-list", "--count", "origin/main..HEAD") != "0" {
		t.Fatal("still ahead")
	}
	consistent(t, f.s)
}

func TestCrashLeavingAWrittenButUncommittedFileIsCommittedAndPushed(t *testing.T) {
	t.Parallel()
	f := newRemoteSpace(t)
	f.crashedWithUncommittedFile("Written", "WRITE00001")
	f.reopen()
	// It is committed on open (no network needed), with the owner as author.
	if got := git(t, f.root, "log", "-1", "--format=%s|%an|%cn"); got != "Add Written|Ann|Service" {
		t.Fatalf("%q", got)
	}
	if err := f.syncNow(); err != nil {
		t.Fatal(err)
	}
	if f.remoteLog()[0] != "Add Written" {
		t.Fatalf("%v", f.remoteLog())
	}
	if b, _ := os.ReadFile(filepath.Join(f.s.StateDir(), "pending.journal")); len(b) != 0 {
		t.Fatalf("journal: %q", b)
	}
	consistent(t, f.s)
}

func TestCrashRecoveryWhenTheRemoteMovedKeepsTheWorkAndFollowsTheRemote(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"unpushed commit", "uncommitted file"} {
		t.Run(kind, func(t *testing.T) {
			f := newRemoteSpace(t)
			mustCreate(t, f.s, "", meta("title", "Before"), "b\n")
			if kind == "unpushed commit" {
				f.crashedWithUnpushedCommit("Mine", "MINE0000001")
			} else {
				f.crashedWithUncommittedFile("Mine", "MINE0000001")
			}
			// Somebody else pushes while the server is down.
			o := f.theirs()
			o.write("Theirs.md", pageText("THEMS00001", "Theirs", "t\n"))
			o.push("theirs")
			theirTip := f.tip()
			f.reopen()
			err := f.syncNow()
			var ue *UnavailableError
			if errors.As(err, &ue) {
				t.Fatalf("a sync that set work aside is not a failure: %v", err)
			}
			// The clone follows the remote, never ahead or diverged.
			if got := git(t, f.root, "rev-parse", "HEAD"); got != theirTip {
				t.Fatalf("HEAD %s, remote %s", got, theirTip)
			}
			if git(t, f.root, "rev-list", "--count", "origin/main..HEAD") != "0" || git(t, f.root, "status", "--porcelain") != "" {
				t.Fatal("the clone is not clean and equal to the remote")
			}
			// The work is kept under a ref, named in the error state.
			refs := git(t, f.root, "for-each-ref", "--format=%(refname)", "refs/petalbook/unpushed/")
			if !strings.HasPrefix(refs, "refs/petalbook/unpushed/") || strings.Count(refs, "\n") != 0 {
				t.Fatalf("refs: %q", refs)
			}
			if kept := fileList(t, f.root, refs); !strings.Contains(kept, "Mine.md") {
				t.Fatalf("the ref does not hold the work:\n%s", kept)
			}
			st := f.last()
			if st.State != RemoteError || st.Kind != KindRescued || !strings.Contains(st.Error, refs) {
				t.Fatalf("status %+v, ref %s", st, refs)
			}
			// The remote was not touched by this, and the space serves the remote's content.
			if f.tip() != theirTip {
				t.Fatal("the remote changed")
			}
			if _, err := f.s.Get("MINE0000001"); !errors.Is(err, ErrNotFound) {
				t.Fatal("the set-aside page is still served")
			}
			if got, err := f.s.Get("THEMS00001"); err != nil || got.Body != "t\n" {
				t.Fatalf("%v %v", got, err)
			}
			// Changes still work, and the notice stays until it is cleared.
			if _, err := f.s.Create(Author{}, CreateInput{Meta: meta("title", "After")}); err != nil {
				t.Fatal(err)
			}
			if f.last().Kind != KindRescued {
				t.Fatal("the notice cleared itself")
			}
			f.s.ClearNotice()
			if st := f.last(); st.State != RemoteOK {
				t.Fatalf("%+v", st)
			}
			if got := git(t, f.root, "for-each-ref", "--format=%(refname)", "refs/petalbook/unpushed/"); got != refs {
				t.Fatal("clearing the notice dropped the kept work")
			}
			consistent(t, f.s)
		})
	}
}

func TestACloneWithChangesTheServerDidNotMakeRefusesChanges(t *testing.T) {
	t.Parallel()
	f := newRemoteSpace(t)
	p := mustCreate(t, f.s, "", meta("title", "Tracked"), "t\n")
	f.s.Close()
	// Somebody edits a tracked file in the server's clone by hand.
	if err := os.WriteFile(filepath.Join(f.root, p.Path), []byte(pageText(p.ID, "Tracked", "by hand\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	f.reopen()
	st := f.last()
	if st.State != RemoteError || st.Kind != KindClone || !strings.Contains(st.Error, p.Path) {
		t.Fatalf("status %+v", st)
	}
	var ue *UnavailableError
	if _, err := f.s.Create(Author{}, CreateInput{Meta: meta("title", "Nope")}); !errors.As(err, &ue) {
		t.Fatalf("create: %v", err)
	}
	// Nothing of theirs was discarded.
	if b, _ := os.ReadFile(filepath.Join(f.root, p.Path)); !strings.Contains(string(b), "by hand") {
		t.Fatal("the hand edit was thrown away")
	}
	// Reads work, and once the clone is put right the owner's sync lifts the pause.
	if got, err := f.s.Get(p.ID); err != nil || got.Body != "by hand\n" {
		t.Fatalf("%v %v", got, err)
	}
	git(t, f.root, "checkout", "--", p.Path)
	f.s.ClearNotice()
	if st := f.last(); st.State != RemoteOK {
		t.Fatalf("%+v", st)
	}
	// The index still holds the hand edit until the page is read again.
	f.s.Reindex()
	if _, err := f.s.Create(Author{}, CreateInput{Meta: meta("title", "Now fine")}); err != nil {
		t.Fatal(err)
	}
	consistent(t, f.s)
}

func TestOpeningARemoteSpaceWhoseRepositoryIsGoneIsRefused(t *testing.T) {
	t.Parallel()
	f := newRemoteSpace(t)
	mustCreate(t, f.s, "", meta("title", "Unpushed? no"), "x\n")
	f.s.Close()
	if err := os.RemoveAll(filepath.Join(f.root, ".git")); err != nil {
		t.Fatal(err)
	}
	o := f.options()
	_, err := Open(Options{Root: f.root, Remote: &o})
	if err == nil || !strings.Contains(err.Error(), "repository of this space is missing") {
		t.Fatalf("%v", err)
	}
	// Nothing was made in its place.
	if _, err := os.Stat(filepath.Join(f.root, ".git")); err == nil {
		t.Fatal("an empty repository was made")
	}
}

func TestOpeningPointsOriginAtTheStoredAddress(t *testing.T) {
	t.Parallel()
	f := newRemoteSpace(t)
	f.s.Close()
	git(t, f.root, "remote", "set-url", "origin", "/somewhere/else.git")
	f.reopen()
	if got := git(t, f.root, "remote", "get-url", "origin"); got != f.remote {
		t.Fatalf("origin is %q", got)
	}
}
