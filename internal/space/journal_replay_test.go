package space

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KKazuhaK/PetalBook/internal/page"
)

// crashed is what a test's step hook panics with to stop a change where a
// crash would.
type crashed struct{}

// stopAt makes the change running in s stop at the n-th time (from 1) it
// reaches the step name, as a crash there would.
func stopAt(s *Space, name string, n int) {
	seen := 0
	s.step = func(step string) error {
		if step == name {
			if seen++; seen == n {
				panic(crashed{})
			}
		}
		return nil
	}
}

// survive runs f, which a step hook stops with a crash.
func survive(t *testing.T, f func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(crashed); !ok {
				panic(r)
			}
			return
		}
		t.Fatal("the change was not stopped")
	}()
	f()
}

func blobsIn(t *testing.T, dir string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, "tx-*.blob"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// A move whose rewrite of links a crash cut short after the moved page's own
// links were rewritten: the next start replays what was journaled, and does
// not plan the links again from content that has already changed (which made
// a link without an id relative to its old place twice: A/B/A/B/y.md).
func TestARelinkCutShortByACrashIsReplayedNotPlannedAgain(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	if err := os.MkdirAll(filepath.Join(root, "A", "B"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A link to a page that is not there gets no id: only its path says where it goes.
	os.WriteFile(filepath.Join(root, "A", "B", "p.md"), []byte(pageText("PPPPPPPP01", "p", "see [y](y.md)\n")), 0o644)
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	stopAt(s, "edit written", 1)
	survive(t, func() { s.Move(Author{}, "PPPPPPPP01", "") })
	crash(s)
	s2 := openAt(t, root)
	if err := s2.Flush(); err != nil {
		t.Fatal(err)
	}
	got := readDisk(t, s2, "p.md")
	if !strings.Contains(got, "[y](A/B/y.md)\n") {
		t.Errorf("the moved page's link after the recovery:\n%s", got)
	}
	if head := git(t, root, "show", "HEAD:p.md"); strings.TrimSpace(head) != strings.TrimSpace(got) {
		t.Errorf("committed:\n%s\non disk:\n%s", head, got)
	}
}

// A rename whose link rewrites were journaled but not written when the
// server stopped: the next start writes them from the journal. A file changed
// outside PetalBook meanwhile is left as it is.
func TestARewriteCutShortBeforeItsWritesIsReplayed(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	target := mustCreate(t, s, "", meta("title", "Target"), "t\n")
	var from []*Page
	for _, n := range []string{"One", "Two", "Three"} {
		from = append(from, mustCreate(t, s, "Notes", meta("title", n), "see [Target](../Target.md)\n"))
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	stopAt(s, "edits journaled", 1)
	survive(t, func() { s.Move(Author{}, target.ID, "Archive") })
	crash(s)
	// Moved, nothing rewritten yet; one of the files is changed outside.
	if exists(s, "Target.md") || !exists(s, "Archive/Target.md") {
		t.Fatal("test setup: the page should have moved")
	}
	outside := filepath.Join(root, filepath.FromSlash(from[2].Path))
	b, _ := os.ReadFile(outside)
	os.WriteFile(outside, append(b, "changed outside\n"...), 0o644)

	s2 := openAt(t, root)
	if err := s2.Flush(); err != nil {
		t.Fatal(err)
	}
	for _, p := range from[:2] {
		if got := readDisk(t, s2, p.Path); !strings.Contains(got, "../Archive/Target.md") {
			t.Errorf("%s was not rewritten:\n%s", p.Path, got)
		}
		if got := git(t, root, "show", "HEAD:"+p.Path); !strings.Contains(got, "../Archive/Target.md") {
			t.Errorf("%s is not committed rewritten:\n%s", p.Path, got)
		}
	}
	if got := readDisk(t, s2, from[2].Path); !strings.Contains(got, "changed outside\n") {
		t.Errorf("the change made outside is lost:\n%s", got)
	}
}

// A save journaled but not written when the server stopped is written by the
// next start, from the journal or (content over 64 KB) from the file staged
// beside it, which goes once the save is committed.
func TestASaveCutShortAfterItsJournalIsReplayed(t *testing.T) {
	needGit(t)
	for _, c := range []struct {
		name string
		body string
	}{
		{"small", "v2 <&> \"quoted\"\n"},
		{"large", strings.Repeat("a long line of text, ", 5000) + "\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			s := openAt(t, root)
			p := mustCreate(t, s, "", meta("title", "Kept"), "v1\n")
			if err := s.Flush(); err != nil {
				t.Fatal(err)
			}
			stopAt(s, "save journaled", 1)
			survive(t, func() { s.Save(Author{}, p.ID, p.Rev, meta("title", "Kept"), c.body) })
			crash(s)
			if got := readDisk(t, s, p.Path); strings.Contains(got, c.body) {
				t.Fatal("test setup: the save should not have been written")
			}
			s2 := openAt(t, root)
			if got := readDisk(t, s2, p.Path); !strings.HasSuffix(got, "---\n"+c.body) {
				t.Errorf("the save was not replayed:\n%.300s", got)
			}
			if err := s2.Flush(); err != nil {
				t.Fatal(err)
			}
			if got := git(t, root, "show", "HEAD:"+p.Path); !strings.Contains(got, strings.TrimSpace(c.body)) {
				t.Errorf("the save was not committed:\n%.300s", got)
			}
			if b := blobsIn(t, s2.StateDir()); len(b) != 0 {
				t.Errorf("staged content left after the commit: %v", b)
			}
		})
	}
}

// A save whose write failed was answered with an error: the next start does
// not make it after all.
func TestAFailedSaveIsNotReplayed(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	p := mustCreate(t, s, "", meta("title", "Kept"), "v1\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	s.step = func(step string) error {
		if step == "save journaled" {
			return errors.New("disk full")
		}
		return nil
	}
	if _, err := s.Save(Author{}, p.ID, p.Rev, meta("title", "Kept"), "v2\n"); err == nil {
		t.Fatal("the save should have failed")
	}
	crash(s)
	s2 := openAt(t, root)
	if got := readDisk(t, s2, p.Path); !strings.HasSuffix(got, "---\nv1\n") {
		t.Errorf("a failed save was made at the next start:\n%s", got)
	}
}

// Content staged for a change that no journal record names any more (a
// crash after the trim, before the file went) is removed at startup.
func TestStagedContentNoRecordNamesIsRemovedAtStartup(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	crash(s)
	stale := filepath.Join(DefaultStateDir(root), "tx-7.blob")
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	openAt(t, root)
	if _, err := os.Stat(stale); err == nil {
		t.Error("the stale staged file is still there")
	}
}

// A transaction reads back with every file's content, text, not text, or
// staged; the journal is synced once for it and not at all for "a"; the trim
// that drops it removes its staged file.
func TestATransactionReadsBackWhole(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "pending.journal")
	j, _, err := openJournal(path, slog.New(slog.NewTextHandler(&logBuffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	syncs := 0
	j.sync = func(f *os.File) error { syncs++; return f.Sync() }
	files := []txFile{
		{path: "a.md", old: "o1", rev: "n1", data: []byte("text <with> & \"marks\"\n")},
		{path: "b.bin", old: "o2", rev: "n2", data: []byte{0xff, 0, 0xfe}},
		{path: "c.md", rev: "n3", data: bytes.Repeat([]byte("x"), txInline+1)},
		{path: "d.md", old: "o4", rev: "n4", data: nil},
	}
	tx, err := j.appendTx(Author{Name: "Ann"}, files, []jrec{{Op: "ld", From: "A", To: "B"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := j.appendNoSync([]jrec{{Op: "a", Tx: tx}}); err != nil {
		t.Fatal(err)
	}
	if syncs != 1 {
		t.Errorf("%d syncs of the journal, want 1", syncs)
	}
	if b := blobsIn(t, dir); len(b) != 1 {
		t.Fatalf("staged files: %v", b)
	}
	j.close()
	j, recs, err := openJournal(path, slog.New(slog.NewTextHandler(&logBuffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 7 || recs[0].Op != "x" || recs[0].Tx != tx || recs[0].seq != tx || recs[0].AuthorName != "Ann" || recs[5].Op != "ld" || recs[6].Op != "a" {
		t.Fatalf("records: %+v", recs)
	}
	for i, f := range files {
		r := recs[i+1]
		got, err := r.content()
		if err != nil || r.Op != "p" || r.Tx != tx || r.Path != f.path || r.Old != f.old || r.Rev != f.rev || !bytes.Equal(got, f.data) {
			t.Errorf("file %s: %+v %v", f.path, r, err)
		}
	}
	if err := j.trim(j.lastSeq(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if b := blobsIn(t, dir); len(b) != 0 {
		t.Errorf("staged files after the trim: %v", b)
	}
	j.close()
}

// The next start replays a transaction once: one that a crash stopped in the
// middle of its own replay is replayed to the same end.
func TestReplayingATransactionTwiceIsReplayingItOnce(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write := func(name, s string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.md", "a0")
	write("b.md", "b1")      // already written before the crash
	write("c.md", "outside") // changed outside PetalBook
	rev := func(s string) string { return page.Rev([]byte(s)) }
	recs := []jrec{{Op: "x", Tx: 1, AuthorName: "Ann"}}
	for _, f := range []struct{ name, old, new string }{{"a.md", "a0", "a1"}, {"b.md", "b0", "b1"}, {"c.md", "c0", "c1"}} {
		recs = append(recs, jrec{Op: "p", Tx: 1, Path: f.name, Old: rev(f.old), Rev: rev(f.new), Text: f.new})
	}
	for range 2 {
		got := recoverPending(root, recs)
		if len(got) != 2 || got["a.md"].Name != "Ann" || got["b.md"].Name != "Ann" {
			t.Errorf("pending: %v", got)
		}
		for name, want := range map[string]string{"a.md": "a1", "b.md": "b1", "c.md": "outside"} {
			if b, _ := os.ReadFile(filepath.Join(root, name)); string(b) != want {
				t.Errorf("%s: %q, want %q", name, b, want)
			}
		}
	}
	// Given up: nothing is made.
	write("a.md", "a0")
	recoverPending(root, append(recs, jrec{Op: "k", Tx: 1}))
	if b, _ := os.ReadFile(filepath.Join(root, "a.md")); string(b) != "a0" {
		t.Errorf("a transaction given up was replayed: %q", b)
	}
}
