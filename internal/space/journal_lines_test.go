package space

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

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

// A record of the journal that cannot be read, however long, is reported, and the records after it still count.
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
	rev := func(f string) string { return page.Rev([]byte("---\ntitle: " + f + "\n---\nx\n")) }
	journal := strings.Join([]string{
		`{"op":"x","tx":1,"pn":1}`, `{"op":"p","tx":1,"p":"a.md","rev":"` + rev("a.md") + `"}`, `{"op":"a","tx":1}`,
		`{"op":"p","p":`,           // cut short
		strings.Repeat("x", 2<<20), // longer than any record
		`{"op":"x","tx":6,"pn":1}`, `{"op":"p","tx":6,"p":"b.md","rev":"` + rev("b.md") + `"}`, `{"op":"a","tx":6}`, ``,
	}, "\n")
	os.WriteFile(filepath.Join(state, "pending.journal"), framed(journal), 0o644)
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
	for _, line := range []string{"seq=4", "seq=5"} {
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
	if err := os.WriteFile(journalFile(root), framed(`{"op":"t","p":"new.md"}`), 0o644); err != nil {
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
