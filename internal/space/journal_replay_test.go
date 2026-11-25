package space

import (
	"bytes"
	"errors"
	"fmt"
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
	recs := []jrec{{Op: "x", Tx: 1, N: 3, AuthorName: "Ann"}}
	for _, f := range []struct{ name, old, new string }{{"a.md", "a0", "a1"}, {"b.md", "b0", "b1"}, {"c.md", "c0", "c1"}} {
		recs = append(recs, jrec{Op: "p", Tx: 1, Path: f.name, Old: rev(f.old), Rev: rev(f.new), Text: f.new})
	}
	for range 2 {
		// c.md was changed outside before the transaction was applied: it
		// is left alone and not committed (the transaction is given up).
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

// A save that was written is not made again at the next start: the file
// changed after it (here outside PetalBook, back to its old bytes) keeps
// that change.
func TestAnAppliedSaveIsNotReplayed(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	p := mustCreate(t, s, "", meta("title", "Kept"), "v1\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	old := readDisk(t, s, p.Path)
	if _, err := s.Save(Author{}, p.ID, p.Rev, meta("title", "Kept"), "v2\n"); err != nil {
		t.Fatal(err)
	}
	crash(s)
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(p.Path)), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	s2 := openAt(t, root)
	if got := readDisk(t, s2, p.Path); got != old {
		t.Errorf("the file changed after the save was written over at the next start:\n%s", got)
	}
}

// A rewrite whose append a crash cut short (the last frames, with the
// closing of its "l" record, never reached the disk): none of it is
// replayed, and the move's links are planned again from the files, which
// nothing has touched. Half a replay under a new plan would make the moved
// page's link relative to its old place twice.
func TestARewriteWhoseJournalIsCutShortIsNotHalfReplayed(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	os.MkdirAll(filepath.Join(root, "A", "B"), 0o755)
	os.WriteFile(filepath.Join(root, "A", "B", "p.md"), []byte(pageText("PPPPPPPP01", "p", "see [y](y.md)\n")), 0o644)
	os.WriteFile(filepath.Join(root, "z.md"), []byte(pageText("ZZZZZZZZ01", "z", "see [p](A/B/p.md \"PPPPPPPP01\")\n")), 0o644)
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	stopAt(s, "edits journaled", 1)
	survive(t, func() { s.Move(Author{}, "PPPPPPPP01", "") })
	crash(s)
	data, err := os.ReadFile(journalFile(root))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journalFile(root), data[:len(data)-20], 0o644); err != nil {
		t.Fatal(err)
	}
	s2 := openAt(t, root)
	if got := readDisk(t, s2, "p.md"); !strings.Contains(got, "[y](A/B/y.md)\n") {
		t.Errorf("the moved page's link:\n%s", got)
	}
	if got := readDisk(t, s2, "z.md"); !strings.Contains(got, "[p](p.md ") {
		t.Errorf("the link to the moved page:\n%s", got)
	}
}

// Content staged for a save that cannot be read back at the next start is
// reported, with the file and the transaction, not passed over in silence.
func TestStagedContentThatIsGoneIsReported(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	p := mustCreate(t, s, "", meta("title", "Kept"), "v1\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	stopAt(s, "save journaled", 1)
	survive(t, func() {
		s.Save(Author{}, p.ID, p.Rev, meta("title", "Kept"), strings.Repeat("long text ", 10000))
	})
	crash(s)
	blobs := blobsIn(t, s.StateDir())
	if len(blobs) != 1 {
		t.Fatalf("staged files: %v", blobs)
	}
	os.Remove(blobs[0])
	var logs logBuffer
	openAt(t, root, withLog(&logs))
	if l := logs.String(); !strings.Contains(l, "level=ERROR") || !strings.Contains(l, filepath.Base(blobs[0])) || !strings.Contains(l, p.Path) {
		t.Errorf("the lost content is not reported:\n%s", l)
	}
}

// A transaction of many files near the inline limit stages what is over a
// bound for the whole transaction: no append is hundreds of megabytes.
func TestALargeTransactionStagesWhatIsOverItsBound(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "pending.journal")
	j, _, err := openJournal(path, slog.New(slog.NewTextHandler(&logBuffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer j.close()
	var files []txFile
	for i := range 40 {
		files = append(files, txFile{path: fmt.Sprintf("f%d.md", i), old: "o", rev: "n", data: bytes.Repeat([]byte{'a' + byte(i%26)}, txInline-10)})
	}
	if _, err := j.appendTx(Author{}, files, nil); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Size() > 2*txInlineTotal {
		t.Errorf("the journal is %d bytes", st.Size())
	}
	data, _ := os.ReadFile(path)
	rd := readJournal(data)
	for i, f := range files {
		r := rd.recs[i+1]
		r.blob = j.blobPath(r.Tx)
		if got, err := r.content(); err != nil || !bytes.Equal(got, f.data) {
			t.Errorf("%s: %v", f.path, err)
		}
	}
}
