package space

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/page"
)

// logBuffer collects what a space logs, for the tests to read.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func withLog(l *logBuffer) func(*Options) {
	return func(o *Options) { o.Logger = slog.New(slog.NewTextHandler(l, nil)) }
}

// uncommitted is what Git sees changed in the scene, but for C (a page whose header cannot be read, never committed).
func uncommitted(t *testing.T, root string) string {
	t.Helper()
	var out []string
	for _, l := range strings.Split(git(t, root, "status", "--porcelain", "--", ".petalbook", "Work", "Other"), "\n") {
		if l != "" && l != "?? Work/C.md" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// journalUpToRename is the journal as it was when the rename's record was written and nothing after it: a crash came
// before the rename was closed.
func journalUpToRename(t *testing.T, root string) []byte {
	t.Helper()
	data, err := os.ReadFile(journalFile(root))
	if err != nil {
		t.Fatal(err)
	}
	at := bytes.Index(data, []byte(`{"op":"f",`))
	if at < 0 {
		t.Fatalf("no record of the rename in the journal")
	}
	end := bytes.IndexByte(data[at:], '\n')
	return data[:at+end+1]
}

// A rename over so many pages that their paths take more than a megabyte, stopped by a crash after the type's file was
// written: the next start finishes every page.
func TestABigFieldRenameStoppedByACrashIsFinished(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	writeType(t, root, "task", `{"title":"Task","properties":{"status":{"type":"string"}}}`)
	// Long paths (within the limits for a page) so that a megabyte of them is about a thousand pages.
	dir := filepath.Join("Work", strings.Repeat("a", 240), strings.Repeat("b", 240), strings.Repeat("c", 240))
	if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
		t.Fatal(err)
	}
	const n = 1200
	paths := make([]string, n)
	old := make([][]byte, n)
	now := time.Now()
	for i := range n {
		rel := filepath.Join(dir, fmt.Sprintf("p%04d-%s.md", i, strings.Repeat("d", 190)))
		paths[i] = filepath.ToSlash(rel)
		old[i] = fmt.Appendf(nil, "---\nid: %s\ntitle: P%d\ntype: task\nstatus: open\n---\nx\n", page.NewID(now.Add(time.Duration(i)*time.Second)), i)
		if err := os.WriteFile(filepath.Join(root, rel), old[i], 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	rev := ""
	ts, _, err := s.Types()
	if err != nil {
		t.Fatal(err)
	}
	for _, ti := range ts {
		if ti.Name == "task" {
			rev = ti.Rev
		}
	}
	r, err := s.RenameTypeField(Author{}, "task", rev, "status", "state")
	if err != nil {
		t.Fatal(err)
	}
	if r.Pages != n {
		t.Fatalf("renamed in %d pages, want %d", r.Pages, n)
	}
	journal := journalUpToRename(t, root)
	if len(journal) <= 1<<20 {
		t.Fatalf("the journal is %d bytes: the test needs more than a megabyte of paths", len(journal))
	}
	for i, line := range bytes.Split(journal, []byte("\n")) {
		if len(line) > maxJournalLine {
			t.Errorf("line %d of the journal is %d bytes", i+1, len(line))
		}
	}
	crash(s)
	// The crash came after the type's file was written, before the pages.
	for i, p := range paths {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(p)), old[i], 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(journalFile(root), journal, 0o644); err != nil {
		t.Fatal(err)
	}
	s2 := openAt(t, root)
	for i, p := range paths {
		got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(got, []byte("\nstate: open\n")) || bytes.Contains(got, []byte("status:")) {
			t.Fatalf("page %d was not finished:\n%s", i, got)
		}
	}
	if err := s2.Flush(); err != nil {
		t.Fatal(err)
	}
	if st := git(t, root, "status", "--porcelain"); st != "" {
		t.Errorf("not committed:\n%.500s", st)
	}
}

// The crash came after some of the pages were written, before the others and the views.
func TestAFieldRenameStoppedPartWayThroughThePagesIsFinished(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s, rev, pages := renameScene(t, root)
	more := []*Page{
		mustCreate(t, s, "Work", meta("title", "E", "status", "open"), "x\n"),
		mustCreate(t, s, "Work", meta("title", "F", "status", "done"), "x\n"),
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	oldYML := spaceYML(root)
	oldA, oldE := readDisk(t, s, pages["A"].Path), readDisk(t, s, more[0].Path)
	if _, err := s.RenameTypeField(Author{}, "task", rev, "status", "state"); err != nil {
		t.Fatal(err)
	}
	journal := journalUpToRename(t, root)
	crash(s)
	// F was written; A and E were not, nor the views.
	os.WriteFile(filepath.Join(root, filepath.FromSlash(pages["A"].Path)), []byte(oldA), 0o644)
	os.WriteFile(filepath.Join(root, filepath.FromSlash(more[0].Path)), []byte(oldE), 0o644)
	setSpaceYML(t, root, oldYML)
	if err := os.WriteFile(journalFile(root), journal, 0o644); err != nil {
		t.Fatal(err)
	}
	s2 := openAt(t, root)
	if _, err := s2.Reindex(); err != nil {
		t.Fatal(err)
	}
	checkRenamed(t, s2, pages)
	for _, p := range more {
		got, err := s2.Get(p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := got.Meta.Get("state"); !ok || got.Meta.Index("status") >= 0 {
			t.Errorf("%s: %+v", p.Path, got.Meta)
		}
	}
	if err := s2.Flush(); err != nil {
		t.Fatal(err)
	}
	if st := uncommitted(t, root); st != "" {
		t.Errorf("not committed:\n%s", st)
	}
}

// The crash came after everything was written, before the rename was closed in the journal: the next start finds
// nothing left to do and changes nothing, and the start after that does not look at the rename again.
func TestAFieldRenameStoppedBeforeItWasClosedChangesNothingMore(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s, rev, pages := renameScene(t, root)
	if _, err := s.RenameTypeField(Author{}, "task", rev, "status", "state"); err != nil {
		t.Fatal(err)
	}
	journal := journalUpToRename(t, root)
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	head := git(t, root, "rev-parse", "HEAD")
	crash(s)
	if err := os.WriteFile(journalFile(root), journal, 0o644); err != nil {
		t.Fatal(err)
	}
	yml := spaceYML(root)
	a := readDisk(t, s, pages["A"].Path)
	s2 := openAt(t, root)
	if spaceYML(root) != yml || readDisk(t, s2, pages["A"].Path) != a {
		t.Error("a finished rename was made again")
	}
	checkRenamed(t, s2, pages)
	if err := s2.Flush(); err != nil {
		t.Fatal(err)
	}
	if st := uncommitted(t, root); st != "" {
		t.Errorf("not committed:\n%s", st)
	}
	if got := git(t, root, "rev-parse", "HEAD"); got != head {
		t.Errorf("a commit was made for nothing: %s", git(t, root, "log", "-1", "--stat"))
	}
	for _, r := range journalRecords(t, root) {
		if r.Op == "f" {
			t.Errorf("the rename is still open in the journal: %+v", r)
		}
	}
}

// A line of the journal that cannot be read, however long, is reported, and the records after it still count.
func TestADamagedJournalLineIsReportedAndTheRestIsKept(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	git(t, root, "init", "-q")
	state := DefaultStateDir(root)
	os.MkdirAll(state, 0o755)
	for _, f := range []string{"a.md", "b.md"} {
		os.WriteFile(filepath.Join(root, f), []byte("---\ntitle: "+f+"\n---\nx\n"), 0o644)
	}
	journal := strings.Join([]string{
		`{"op":"t","p":"a.md"}`,
		`{"op":"t","p":`,           // cut short
		strings.Repeat("x", 2<<20), // longer than any record
		`{"op":"t","p":"b.md"}`, ``,
	}, "\n")
	os.WriteFile(filepath.Join(state, "pending.journal"), []byte(journal), 0o644)
	var logs logBuffer
	s := openAt(t, root, withLog(&logs))
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	files, _ := gitTry(root, "ls-files")
	for _, f := range []string{"a.md", "b.md"} {
		if !strings.Contains(files, f) {
			t.Errorf("%s was not committed:\n%s", f, files)
		}
	}
	out := logs.String()
	for _, line := range []string{"line=2", "line=3"} {
		if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, line) {
			t.Errorf("the damaged %s is not reported:\n%.2000s", line, out)
		}
	}
}

// A crash in the middle of an append leaves the last line cut short: that record's write never happened (the record
// is written first). It is dropped, so the next record appended does not join it and become unreadable too.
func TestATornLastJournalLineDoesNotSwallowTheNextRecord(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	crash(s)
	if err := os.WriteFile(journalFile(root), []byte(`{"op":"t","p":"ne`), 0o644); err != nil {
		t.Fatal(err)
	}
	s2 := openAt(t, root)
	p := mustCreate(t, s2, "", meta("title", "崩溃前"), "kept\n")
	crash(s2)
	s3 := openAt(t, root)
	if err := s3.Flush(); err != nil {
		t.Fatal(err)
	}
	if got, _ := gitTry(root, "ls-files"); !strings.Contains(got, p.Path) {
		t.Fatalf("the page written before the second crash was never committed:\n%s", got)
	}
}
