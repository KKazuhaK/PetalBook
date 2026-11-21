package space

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KazuhaHub/PetalBook/internal/index"
)

// crash stops a space the way kill -9 would: nothing is flushed or committed.
func crash(s *Space) {
	s.cm.once.Do(func() { close(s.cm.stop) })
	<-s.cm.done
	s.cm.mu.Lock()
	s.cm.paths = map[string]bool{} // what a dead process cannot commit later
	s.cm.mu.Unlock()
	s.cm.j.close()
	s.idx.Close()
}

func TestSwapOfFilesOutsideTheServerRecoversWithoutReindex(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	g := mustCreate(t, s, "", meta("title", "Gamma"), "gamma\n")
	d := mustCreate(t, s, "", meta("title", "Delta"), "delta\n")
	r := s.Root()
	os.Rename(filepath.Join(r, g.Path), filepath.Join(r, "t"))
	os.Rename(filepath.Join(r, d.Path), filepath.Join(r, g.Path))
	os.Rename(filepath.Join(r, "t"), filepath.Join(r, d.Path))

	for _, c := range []struct {
		p    *Page
		body string
		path string
	}{{g, "gamma\n", d.Path}, {d, "delta\n", g.Path}} {
		got, err := s.Get(c.p.ID)
		if err != nil || got.Body != c.body || got.Path != c.path {
			t.Fatalf("Get(%s) after the swap: %v %+v", c.p.Meta.Title(), err, got)
		}
	}
	if probs, _ := s.Problems(); len(probs) != 0 {
		t.Fatalf("%+v", probs)
	}
	// And after a restart.
	s.Close()
	s2 := openAt(t, root)
	if got, err := s2.Get(g.ID); err != nil || got.Body != "gamma\n" {
		t.Fatalf("after restart: %v %+v", err, got)
	}
}

func TestDuplicateBecomesAPageWhenTheTwinIsDeleted(t *testing.T) {
	s := open(t)
	a := mustCreate(t, s, "", meta("title", "First"), "one\n")
	raw := readDisk(t, s, a.Path)
	os.WriteFile(filepath.Join(s.Root(), "twin.md"), []byte(strings.Replace(raw, "one", "two", 1)), 0o644)
	s.Reindex()
	if probs, _ := s.Problems(); len(probs) != 1 || probs[0].Code != "duplicate_id" {
		t.Fatalf("%+v", probs)
	}
	if err := s.Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(a.ID); err != nil || got.Path != "twin.md" || got.Body != "two\n" {
		t.Fatalf("the twin did not become the page: %v %+v", err, got)
	}
	if probs, _ := s.Problems(); len(probs) != 0 {
		t.Fatalf("%+v", probs)
	}
}

// --- the journal -------------------------------------------------------------

func TestUncommittedWritesSurviveACrash(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	if err := s.Flush(); err != nil { // the start-up files
		t.Fatal(err)
	}
	p := mustCreate(t, s, "", meta("title", "崩溃前"), "kept\n")
	crash(s)

	s2, err := Open(Options{Root: root, CommitIdle: time.Hour, CommitMax: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Close(); err != nil { // a clean shutdown
		t.Fatal(err)
	}
	if got, _ := gitTry(root, "ls-files"); !strings.Contains(got, p.Path) {
		t.Fatalf("the page written before the crash was never committed:\n%s", got)
	}
	if c := commits(t, root); len(c) == 0 || c[0] != "Add 崩溃前" {
		t.Fatalf("%q", c)
	}
	// Later it can be deleted and the deletion is recorded, history keeps it.
	s3, err := Open(Options{Root: root, CommitIdle: time.Hour, CommitMax: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := s3.Delete(p.ID); err != nil {
		t.Fatal(err)
	}
	s3.Close()
	if c := commits(t, root); c[0] != "Delete 崩溃前" {
		t.Fatalf("%q", c)
	}
	if body, _ := gitTry(root, "show", "HEAD~1:"+p.Path); !strings.Contains(body, "kept") {
		t.Fatalf("history lost the page: %q", body)
	}
	// The journal is empty after a clean commit.
	if b, _ := os.ReadFile(filepath.Join(root, ".petalbook", "cache", "pending.journal")); len(b) != 0 {
		t.Fatalf("journal not trimmed: %q", b)
	}
}

func TestCrashBetweenLinkAndRemoveLeavesOnePage(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	p := mustCreate(t, s, "x", meta("title", "半途"), "body\n")
	s.Flush()
	// The crash: the new name is linked, the old one never removed.
	s.ops.Remove = func(string) error { return errors.New("crash") }
	if _, err := s.Move(p.ID, "y"); err == nil {
		t.Fatal("the move should have failed")
	}
	if !exists(s, "x/半途.md") || !exists(s, "y/半途.md") {
		t.Fatal("test setup: both names should exist")
	}
	crash(s)

	s2 := openAt(t, root)
	if exists(s2, "x/半途.md") || !exists(s2, "y/半途.md") {
		t.Fatalf("the interrupted move was not finished: old=%v new=%v", exists(s2, "x/半途.md"), exists(s2, "y/半途.md"))
	}
	r, _ := s2.List(index.Query{Limit: 50})
	if len(r.Items) != 1 || r.Items[0].Path != "y/半途.md" {
		t.Fatalf("%+v", r.Items)
	}
	if probs, _ := s2.Problems(); len(probs) != 0 {
		t.Fatalf("a duplicate was left behind: %+v", probs)
	}
	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}
	if c := commits(t, root); c[0] != "Move 半途" {
		t.Fatalf("%q", c)
	}
}

func TestJournalWithMissingOrForeignPathsIsTolerated(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	git(t, root, "init", "-q")
	state := filepath.Join(root, ".petalbook", "cache")
	os.MkdirAll(state, 0o755)
	os.WriteFile(filepath.Join(root, "other.txt"), []byte("someone else's file\n"), 0o644)
	os.WriteFile(filepath.Join(root, "taken.md"), []byte("not ours\n"), 0o644)
	journal := strings.Join([]string{
		`{"op":"w","p":"gone.md","rev":"0000000000000000000000000000000000000000"}`, // never written
		`{"op":"t","p":"vanished.md"}`,
		`{"op":"d","p":"deleted-long-ago.md"}`,
		`{"op":"w","p":"taken.md","rev":"1111111111111111111111111111111111111111"}`, // name held by another file
		`{"op":"m","f":"a.md","to":"b.md"}`,
		`{"op":"t","p":"../escape.md"}`, `{"op":"t","p":"/etc/passwd"}`, `{"op":"t","p":".git/config"}`,
		`not json at all`, `{"op":""}`, ``,
	}, "\n")
	os.WriteFile(filepath.Join(state, "pending.journal"), []byte(journal), 0o644)

	s := openAt(t, root)
	p := mustCreate(t, s, "", meta("title", "新页"), "x\n")
	if err := s.Flush(); err != nil {
		t.Fatalf("a stale journal broke the commit: %v", err)
	}
	files, _ := gitTry(root, "ls-files")
	if !strings.Contains(files, p.Path) {
		t.Fatal("the new page was not committed")
	}
	for _, bad := range []string{"other.txt", "taken.md", "escape", "passwd"} {
		if strings.Contains(files, bad) {
			t.Fatalf("%s was committed:\n%s", bad, files)
		}
	}
	if st, _ := gitTry(root, "status", "--porcelain"); !strings.Contains(st, "other.txt") || !strings.Contains(st, "taken.md") {
		t.Fatalf("someone else's files should still be untracked:\n%s", st)
	}
}

func TestStrayFilesStayOutAfterRecovery(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	s.Flush()
	mustCreate(t, s, "", meta("title", "写了"), "x\n")
	os.WriteFile(filepath.Join(root, "stray.txt"), []byte("mine\n"), 0o644)
	crash(s)
	s2 := openAt(t, root)
	if err := s2.Flush(); err != nil {
		t.Fatal(err)
	}
	files, _ := gitTry(root, "ls-files")
	if strings.Contains(files, "stray.txt") || !strings.Contains(files, "写了.md") {
		t.Fatalf("%s", files)
	}
}

func TestRecoveredChangesWaitWhileARebaseIsRunning(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	s.Flush()
	mustCreate(t, s, "", meta("title", "等待"), "x\n")
	crash(s)
	os.MkdirAll(filepath.Join(root, ".git", "rebase-merge"), 0o755)
	s2 := openAt(t, root)
	var busy *busyError
	if err := s2.Flush(); !errors.As(err, &busy) {
		t.Fatalf("%v", err)
	}
	os.RemoveAll(filepath.Join(root, ".git", "rebase-merge"))
	if err := s2.Flush(); err != nil {
		t.Fatal(err)
	}
	if c := commits(t, root); c[0] != "Add 等待" {
		t.Fatalf("%q", c)
	}
}

// --- throttled re-sync ---------------------------------------------------------

type blockingSource struct {
	dirSource
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingSource) Walk(f func(index.FileInfo) error, d func(string) error) error {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return b.dirSource.Walk(f, d)
}

func TestUnknownIDsCauseOneWalkPerInterval(t *testing.T) {
	s := open(t, func(o *Options) { o.ResyncInterval = time.Hour })
	s.lastSync = time.Time{} // let the first miss through
	for i := 0; i < 50; i++ {
		id := "00000000" + string(rune('0'+i/10)) + string(rune('0'+i%10))
		if _, err := s.Get(id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%v", err)
		}
	}
	for i := 0; i < 20; i++ {
		s.Delete("ZZZZZZZZZZ")
		s.Save("YYYYYYYYYY", "x", nil, "")
		s.Move("XXXXXXXXXX", "f")
	}
	if n := s.walks.Load(); n != 1 {
		t.Fatalf("%d directory walks for repeated unknown ids, want 1", n)
	}
	// After the interval the next miss walks again, and finds a page that
	// appeared outside the server.
	s.syncEvery = time.Millisecond
	time.Sleep(5 * time.Millisecond)
	os.WriteFile(filepath.Join(s.Root(), "new.md"), []byte("---\nid: NEWNEWNEW1\n---\nhi\n"), 0o644)
	if got, err := s.Get("NEWNEWNEW1"); err != nil || got.Body != "hi\n" {
		t.Fatalf("%v %+v", err, got)
	}
}

func TestSavesProceedWhileAWalkIsRunning(t *testing.T) {
	s := open(t, func(o *Options) { o.ResyncInterval = time.Millisecond })
	p := mustCreate(t, s, "", meta("title", "忙着"), "v1\n")
	time.Sleep(5 * time.Millisecond)
	blk := &blockingSource{dirSource: dirSource{s.root}, entered: make(chan struct{}), release: make(chan struct{})}
	s.src = blk
	var walked atomic.Bool
	go func() {
		s.Get("QQQQQQQQQQ") // unknown: starts a walk that blocks
		walked.Store(true)
	}()
	select {
	case <-blk.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the walk never started")
	}
	done := make(chan error, 1)
	go func() {
		n, err := s.Save(p.ID, p.Rev, meta("title", "忙着"), "v2\n")
		if err == nil {
			_, err = s.Create(CreateInput{Meta: meta("title", "另一页")})
		}
		if err == nil {
			err = s.Delete(n.ID)
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		close(blk.release)
		t.Fatal("writes waited for a directory walk")
	}
	if walked.Load() {
		t.Fatal("the walk should still be blocked")
	}
	close(blk.release)
}

// A stale row (a file moved outside the server) is repaired at once even when
// the interval for unknown ids has not passed.
func TestStaleRowIsRepairedImmediately(t *testing.T) {
	s := open(t, func(o *Options) { o.ResyncInterval = time.Hour })
	g := mustCreate(t, s, "", meta("title", "Gamma"), "gamma\n")
	d := mustCreate(t, s, "", meta("title", "Delta"), "delta\n")
	r := s.Root()
	os.Rename(filepath.Join(r, g.Path), filepath.Join(r, "t"))
	os.Rename(filepath.Join(r, d.Path), filepath.Join(r, g.Path))
	os.Rename(filepath.Join(r, "t"), filepath.Join(r, d.Path))
	if got, err := s.Get(g.ID); err != nil || got.Body != "gamma\n" {
		t.Fatalf("%v %+v", err, got)
	}
	if got, err := s.Get(d.ID); err != nil || got.Body != "delta\n" {
		t.Fatalf("%v %+v", err, got)
	}
	// An id that was never indexed is still throttled.
	before := s.walks.Load()
	for i := 0; i < 10; i++ {
		s.Get("NOPENOPE" + "0" + string(rune('0'+i)))
	}
	if s.walks.Load() > before {
		t.Fatalf("%d walks for unknown ids inside the interval", s.walks.Load()-before)
	}
}
