package gitstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// filterRemote is a bare remote that serves partial clones and fetches by id.
func filterRemote(t testing.TB) string {
	t.Helper()
	r := bareRemote(t)
	sh(t, r, "config", "uploadpack.allowFilter", "true")
	sh(t, r, "config", "uploadpack.allowAnySHA1InWant", "true")
	return r
}

// fileURL is what origin is set to: the file transport honours filters, a bare path does too but this is what the server uses.
func fileURL(p string) string { return "file://" + p }

// longBody makes a page long enough that an edit of one line leaves it similar.
var longBody = strings.Repeat("A line of the page that stays as it is.\n", 12)

func seedHistory(t testing.TB, remote string) string {
	t.Helper()
	o := other(t, remote)
	write(t, o, "d/a.md", "id: A\n\none\n")
	write(t, o, "b.md", "id: B\n\nbase\n")
	sh(t, o, "add", ".")
	sh(t, o, "commit", "-qm", "1")
	write(t, o, "d/a.md", "id: A\n\ntwo\n")
	sh(t, o, "commit", "-qam", "2")
	sh(t, o, "mv", "d/a.md", "d/c.md")
	sh(t, o, "commit", "-qm", "3 (exact rename)")
	write(t, o, "d/c.md", "id: A\n\nthree\n"+longBody)
	sh(t, o, "commit", "-qam", "4")
	// Renamed and edited in one commit: Git's search on a full clone sees a
	// rename (most of the text is the same); a partial clone sees only exact
	// renames, so it sees an addition and a deletion.
	sh(t, o, "mv", "d/c.md", "e.md")
	write(t, o, "e.md", "id: A\n\nthree, and four\n"+longBody)
	sh(t, o, "add", "-A")
	sh(t, o, "commit", "-qm", "5 (rename and edit)")
	sh(t, o, "push", "-q", "origin", "main")
	return o
}

func TestPartialCloneFetchesOnlyWhatIsAskedFor(t *testing.T) {
	needGit(t)
	ctx := context.Background()
	remote := filterRemote(t)
	seedHistory(t, remote)

	r := newClone(t, fileURL(remote), nil)
	if r.IsPartial(ctx) {
		t.Fatal("a fresh repository is not partial")
	}
	if err := r.FetchFiltered(ctx, "main", true); err != nil {
		t.Fatal(err)
	}
	if !r.IsPartial(ctx) {
		t.Fatal("a filtered fetch leaves the clone partial")
	}
	tip := r.RemoteTip(ctx, "main")
	if tip == "" {
		t.Fatal("no tip")
	}
	// Nothing of the contents is there, and Git does not go and fetch them.
	missing, err := r.MissingForMove(ctx, "", tip)
	if err != nil || len(missing) != 2 {
		t.Fatalf("the tip has 2 files, missing %v (%v)", missing, err)
	}
	if err := r.ResetHard(ctx, tip); err == nil {
		t.Fatal("a checkout of contents that are missing must fail, not fetch on its own")
	}
	if err := r.PrefetchMove(ctx, "", tip); err != nil {
		t.Fatal(err)
	}
	if err := r.ResetHard(ctx, tip); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(r.Dir(), "e.md"))
	if !strings.Contains(string(b), "three, and four") {
		t.Fatalf("checkout: %q", b)
	}
	// The old versions are not here until someone asks.
	older, err := r.MissingBlobs(ctx, []string{sh(t, r.Dir(), "rev-parse", "HEAD~4:d/a.md")})
	if err != nil || len(older) != 1 {
		t.Fatalf("the oldest version should be missing: %v %v", older, err)
	}
}

func TestHistoryOfAPartialCloneFetchesVersionsInOneRequest(t *testing.T) {
	needGit(t)
	ctx := context.Background()
	remote := filterRemote(t)
	seedHistory(t, remote)

	full := newClone(t, fileURL(remote), nil)
	if err := full.Fetch(ctx, "main", true); err != nil {
		t.Fatal(err)
	}
	if err := full.ResetHard(ctx, full.RemoteTip(ctx, "main")); err != nil {
		t.Fatal(err)
	}
	part := newClone(t, fileURL(remote), nil)
	if err := part.FetchFiltered(ctx, "main", true); err != nil {
		t.Fatal(err)
	}
	tip := part.RemoteTip(ctx, "main")
	if err := part.PrefetchMove(ctx, "", tip); err != nil {
		t.Fatal(err)
	}
	if err := part.ResetHard(ctx, tip); err != nil {
		t.Fatal(err)
	}
	fetches := 0
	part.SetEnsure(func(ctx context.Context, ids []string) error {
		fetches++
		return part.FetchBlobs(ctx, ids)
	})
	accept := func(_ string, head []byte) bool { return strings.Contains(string(head), "id: A") }
	// The same page along one path (an exact rename on the way), on a full
	// clone and on a partial one: the same versions.
	before := sh(t, part.Dir(), "rev-parse", "HEAD~1")
	want, wnext, err := full.FileHistory(ctx, HistoryQuery{Start: before, Path: "d/c.md", Limit: 30, Accept: accept})
	if err != nil {
		t.Fatal(err)
	}
	got, gnext, err := part.FileHistory(ctx, HistoryQuery{Start: before, Path: "d/c.md", Limit: 30, Accept: accept})
	if err != nil {
		t.Fatal(err)
	}
	if (wnext == nil) != (gnext == nil) {
		t.Fatalf("cursors differ: %v %v", wnext, gnext)
	}
	if len(got) != len(want) || len(got) != 4 {
		t.Fatalf("the partial clone has %d versions, the full one %d (want 4)", len(got), len(want))
	}
	for i := range got {
		if got[i].Commit != want[i].Commit || got[i].Rev != want[i].Rev || got[i].Status != want[i].Status || got[i].Path != want[i].Path {
			t.Errorf("version %d: partial %+v, full %+v", i, got[i], want[i])
		}
	}
	if fetches != 1 {
		t.Errorf("the contents are fetched in one request per page, not %d", fetches)
	}
	// Fetched once, kept: the same page again fetches nothing.
	fetches = 0
	if _, _, err := part.FileHistory(ctx, HistoryQuery{Start: before, Path: "d/c.md", Limit: 30, Accept: accept}); err != nil {
		t.Fatal(err)
	}
	if fetches != 0 {
		t.Errorf("a version that was fetched is not fetched again (%d)", fetches)
	}

	// From the top, across the rename that was also an edit, followed as the
	// space follows a page (Git's search, then the page's id): every version
	// on both. Only the status of that commit differs: a rename on the full
	// clone, an addition (whose predecessor is found by id) on the partial one.
	want = followByID(t, full, "e.md", accept)
	got = followByID(t, part, "e.md", accept)
	if len(got) != len(want) || len(got) != 5 {
		t.Fatalf("followed by id: the partial clone has %d versions, the full one %d (want 5)", len(got), len(want))
	}
	if want[0].Status != 'R' || got[0].Status != 'A' {
		t.Errorf("the rename and edit: full %c, partial %c", want[0].Status, got[0].Status)
	}
	for i := range got {
		if got[i].Commit != want[i].Commit || got[i].Rev != want[i].Rev || got[i].Path != want[i].Path || (i > 0 && got[i].Status != want[i].Status) {
			t.Errorf("version %d: partial %+v, full %+v", i, got[i], want[i])
		}
	}
}

// followByID walks a file's history as the space walks a page's: along one
// path, and where that path began with an addition, on from the file the same
// commit deleted that accept recognises.
func followByID(t *testing.T, r *Repo, path string, accept func(string, []byte) bool) []Version {
	t.Helper()
	ctx := context.Background()
	var out []Version
	start := "HEAD"
	for hops := 0; hops < 8; hops++ {
		vs, next, err := r.FileHistory(ctx, HistoryQuery{Start: start, Path: path, Limit: 30, Accept: accept})
		if err != nil || next != nil {
			t.Fatalf("%v %v", next, err)
		}
		out = append(out, vs...)
		if len(vs) == 0 || vs[len(vs)-1].Status != 'A' {
			return out
		}
		parent, old, err := r.Predecessor(ctx, vs[len(vs)-1].Commit, nil, accept)
		if err != nil {
			t.Fatal(err)
		}
		if old == "" {
			return out
		}
		start, path = parent, old
	}
	return out
}

func TestEnsureBlobsReportsWhatCannotBeFetched(t *testing.T) {
	needGit(t)
	ctx := context.Background()
	remote := filterRemote(t)
	seedHistory(t, remote)
	r := newClone(t, fileURL(remote), nil)
	if err := r.FetchFiltered(ctx, "main", true); err != nil {
		t.Fatal(err)
	}
	ids := mustMissing(t, r, "main")
	// A fetch that "succeeds" without delivering is an error, not a silent gap.
	err := r.EnsureBlobs(ctx, ids, func(context.Context, []string) error { return nil })
	if !errors.Is(err, ErrMissing) {
		t.Fatalf("want ErrMissing, got %v", err)
	}
	// And a remote that is gone is a network error the caller can classify.
	sh(t, r.Dir(), "remote", "set-url", "origin", fileURL(filepath.Join(t.TempDir(), "gone.git")))
	err = r.FetchBlobs(ctx, ids)
	var ne *NetError
	if !errors.As(err, &ne) {
		t.Fatalf("want a NetError, got %v", err)
	}
}

func mustMissing(t testing.TB, r *Repo, branch string) []string {
	t.Helper()
	ids, err := r.MissingForMove(context.Background(), "", r.RemoteTip(context.Background(), branch))
	if err != nil || len(ids) == 0 {
		t.Fatalf("nothing missing: %v %v", ids, err)
	}
	return ids
}
