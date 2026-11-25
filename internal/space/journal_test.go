package space

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/index"
)

// crash stops a space the way kill -9 would: nothing is flushed or committed.
func crash(s *Space) {
	s.cm.once.Do(func() { close(s.cm.stop) })
	<-s.cm.done
	s.cm.mu.Lock()
	s.cm.paths = map[string]Author{} // what a dead process cannot commit later
	s.cm.mu.Unlock()
	s.cm.j.close()
	s.idx.Close()
}

func TestSwapOfFilesOutsideTheServerRecoversWithoutReindex(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	s := open(t)
	a := mustCreate(t, s, "", meta("title", "First"), "one\n")
	raw := readDisk(t, s, a.Path)
	os.WriteFile(filepath.Join(s.Root(), "twin.md"), []byte(strings.Replace(raw, "one", "two", 1)), 0o644)
	s.Reindex()
	if probs, _ := s.Problems(); len(probs) != 1 || probs[0].Code != "duplicate_id" {
		t.Fatalf("%+v", probs)
	}
	if err := s.Delete(Author{}, a.ID); err != nil {
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
	t.Parallel()
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
	if err := s3.Delete(Author{}, p.ID); err != nil {
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
	if b, _ := os.ReadFile(filepath.Join(DefaultStateDir(root), "pending.journal")); len(b) != 0 {
		t.Fatalf("journal not trimmed: %q", b)
	}
}

func TestCrashBetweenLinkAndRemoveLeavesOnePage(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	p := mustCreate(t, s, "x", meta("title", "半途"), "body\n")
	s.Flush()
	// The crash: the new name is linked, the old one never removed.
	s.ops.Remove = func(string) error { return errors.New("crash") }
	if _, err := s.Move(Author{}, p.ID, "y"); err == nil {
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
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	git(t, root, "init", "-q")
	state := DefaultStateDir(root)
	os.MkdirAll(state, 0o755)
	os.WriteFile(filepath.Join(root, "other.txt"), []byte("someone else's file\n"), 0o644)
	os.WriteFile(filepath.Join(root, "taken.md"), []byte("not ours\n"), 0o644)
	journal := strings.Join([]string{
		`{"op":"w","p":"gone.md","rev":"0000000000000000000000000000000000000000"}`, // never written
		`{"op":"t","p":"vanished.md"}`,
		`{"op":"d","p":"deleted-long-ago.md"}`,
		`{"op":"w","p":"taken.md","rev":"1111111111111111111111111111111111111111"}`, // name held by another file
		`{"op":"mr","f":"a.md","to":"b.md"}`,
		`{"op":"t","p":"../escape.md"}`, `{"op":"t","p":"/etc/passwd"}`, `{"op":"t","p":".git/config"}`,
		`not json at all`, `{"op":""}`, ``,
	}, "\n")
	os.WriteFile(filepath.Join(state, "pending.journal"), framed(journal), 0o644)

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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	s := open(t, func(o *Options) { o.ResyncInterval = time.Hour })
	s.lastSync = time.Time{} // let the first miss through
	for i := 0; i < 50; i++ {
		id := "00000000" + string(rune('0'+i/10)) + string(rune('0'+i%10))
		if _, err := s.Get(id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%v", err)
		}
	}
	for i := 0; i < 20; i++ {
		s.Delete(Author{}, "ZZZZZZZZZZ")
		s.Save(Author{}, "YYYYYYYYYY", "x", nil, "")
		s.Move(Author{}, "XXXXXXXXXX", "f")
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
	t.Parallel()
	s := open(t, func(o *Options) { o.ResyncInterval = time.Millisecond })
	p := mustCreate(t, s, "", meta("title", "忙着"), "v1\n")
	time.Sleep(5 * time.Millisecond)
	blk := &blockingSource{dirSource: dirSource{root: s.root}, entered: make(chan struct{}), release: make(chan struct{})}
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
		n, err := s.Save(Author{}, p.ID, p.Rev, meta("title", "忙着"), "v2\n")
		if err == nil {
			_, err = s.Create(Author{}, CreateInput{Meta: meta("title", "另一页")})
		}
		if err == nil {
			err = s.Delete(Author{}, n.ID)
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
	t.Parallel()
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

// A rename through a third name (Space.rename) that a crash stopped between
// its two steps, before the transaction of the move: the next start takes it
// back, and the page or folder is under its old name, as committed, never
// lost and never left under the third name. A file that took the new name
// meanwhile is left alone.
func TestRespellingStoppedByACrashIsTakenBack(t *testing.T) {
	t.Parallel()
	needGit(t)
	type step struct{ from, mid, to string }
	for _, c := range []struct {
		name     string
		folder   string
		take     bool // something else holds the new name at the restart
		want     []string
		wantGone []string
	}{
		{name: "page", want: []string{"Apple.md"}, wantGone: []string{"apple.md", "apple (renaming 1).md"}},
		{name: "page whose new name was taken", take: true, want: []string{"Apple.md"}, wantGone: []string{"apple (renaming 1).md", "apple.md"}},
		{name: "folder", folder: "notes", want: []string{"notes/Apple.md"}, wantGone: []string{"Notes/Apple.md", "Notes (renaming 1)/Apple.md"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			s := openAt(t, root)
			p := mustCreate(t, s, c.folder, meta("title", "Apple"), "fruit\n")
			if err := s.Flush(); err != nil {
				t.Fatal(err)
			}
			crash(s)
			st := step{"Apple.md", "apple (renaming 1).md", "apple.md"}
			recs := []string{`{"op":"mr","f":"Apple.md","to":"apple.md"}`}
			inside := ""
			if c.folder != "" {
				st = step{"notes", "Notes (renaming 1)", "Notes"}
				inside = "/Apple.md"
				recs = []string{`{"op":"t","p":"notes/Apple.md"}`, `{"op":"t","p":"Notes/Apple.md"}`}
			}
			dir := ""
			if c.folder != "" {
				dir = `,"dir":true`
			}
			recs = append(recs, `{"op":"r","f":"`+st.from+`","to":"`+st.to+`","p":"`+st.mid+`"`+dir+`}`,
				`{"op":"t","p":"`+st.mid+inside+`"}`)
			if err := os.Rename(filepath.Join(root, st.from), filepath.Join(root, st.mid)); err != nil {
				t.Fatal(err)
			}
			if c.take {
				os.WriteFile(filepath.Join(root, st.to), []byte("someone else's\n"), 0o644)
			}
			os.WriteFile(filepath.Join(DefaultStateDir(root), "pending.journal"), framed(strings.Join(recs, "\n")+"\n"), 0o644)

			s2 := openAt(t, root)
			defer s2.Close()
			if err := s2.Flush(); err != nil {
				t.Fatal(err)
			}
			files, _ := gitTry(root, "-c", "core.quotepath=off", "ls-files")
			for _, w := range c.want {
				if !strings.Contains("\n"+files+"\n", "\n"+w+"\n") {
					t.Fatalf("%s is not committed:\n%s", w, files)
				}
			}
			for _, g := range c.wantGone {
				if strings.Contains("\n"+files+"\n", "\n"+g+"\n") {
					t.Fatalf("%s is still committed:\n%s", g, files)
				}
			}
			if _, err := s2.Get(p.ID); err != nil {
				t.Fatalf("the page is lost: %v", err)
			}
			if c.take {
				if b, _ := os.ReadFile(filepath.Join(root, st.to)); string(b) != "someone else's\n" {
					t.Fatalf("the file that took the new name: %q", b)
				}
			}
		})
	}
}

// A name taken for a move by case, and then a crash: on a file system that
// folds names both names find the one file, and taking the name back must not
// remove it.
func TestTakingBackANameKeepsAFileRenamedByCase(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "apple.md"), []byte("the page\n"), 0o644)
	if _, err := os.Stat(filepath.Join(root, "Apple.md")); err != nil {
		t.Skip("this file system tells names apart by case")
	}
	recoverPending(root, []jrec{{Op: "mr", From: "Apple.md", To: "apple.md"}})
	if b, err := os.ReadFile(filepath.Join(root, "apple.md")); err != nil || string(b) != "the page\n" {
		t.Fatalf("%q %v", b, err)
	}
}

// A move to another folder under the same file name, stopped after the new
// name was taken and before its transaction: the new name goes again.
func TestANameTakenForAMoveWithoutItsTransactionIsTakenBack(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "a"), 0o755)
	os.MkdirAll(filepath.Join(root, "b"), 0o755)
	os.WriteFile(filepath.Join(root, "a", "x.md"), []byte("the page\n"), 0o644)
	if err := os.Link(filepath.Join(root, "a", "x.md"), filepath.Join(root, "b", "x.md")); err != nil {
		t.Skip("no hard links here")
	}
	recoverPending(root, []jrec{{Op: "mr", From: "a/x.md", To: "b/x.md"}})
	if _, err := os.Lstat(filepath.Join(root, "b", "x.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the new name is still there: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(root, "a", "x.md")); err != nil || string(b) != "the page\n" {
		t.Fatalf("%q %v", b, err)
	}
}

// foldingOps makes a case-sensitive disk act like a case-folding Linux one
// for the space's own file operations: a name finds an entry spelled in any
// case, and a rename onto the same entry under another spelling does nothing.
// fail, when set, is asked before every rename and may refuse it.
func foldingOps(fail func(n int, from, to string) error) fileOps {
	ops := osOps()
	find := func(p string) string {
		if _, err := os.Lstat(p); err == nil {
			return p
		}
		entries, _ := os.ReadDir(filepath.Dir(p))
		for _, e := range entries {
			if strings.EqualFold(e.Name(), filepath.Base(p)) {
				return filepath.Join(filepath.Dir(p), e.Name())
			}
		}
		return p
	}
	ops.Lstat = func(p string) (fs.FileInfo, error) { return os.Lstat(find(p)) }
	var n int
	ops.Rename = func(from, to string) error {
		n++
		if fail != nil {
			if err := fail(n, from, to); err != nil {
				return err
			}
		}
		if a, b := find(from), find(to); a == b {
			return nil
		}
		return os.Rename(find(from), to)
	}
	return ops
}

// The same end to end, through Space.rename: a page and a folder renamed by
// case on such a disk, and the process dying between the two steps (the
// second rename and the way back both fail, then the process is gone).
func TestRespellingThroughTheSpaceSurvivesACrash(t *testing.T) {
	t.Parallel()
	needGit(t)
	stop := func(n int, from, to string) error {
		if n >= 2 {
			return errors.New("the process is gone")
		}
		return nil
	}
	for _, keepRunning := range []bool{false, true} {
		t.Run(fmt.Sprintf("page, running on %v", keepRunning), func(t *testing.T) {
			root := t.TempDir()
			s := openAt(t, root)
			p := mustCreate(t, s, "", meta("title", "Apple"), "fruit\n")
			if err := s.Flush(); err != nil {
				t.Fatal(err)
			}
			s.ops = foldingOps(stop)
			if _, err := s.Save(Author{}, p.ID, p.Rev, meta("title", "apple"), "fruit\n"); err == nil {
				t.Fatal("the save went through although the rename did not")
			}
			if keepRunning {
				// No crash: the space goes on, and its next commit must not
				// take the page for deleted.
				s.ops = osOps()
				if err := s.Flush(); err != nil {
					t.Fatal(err)
				}
				files, _ := gitTry(root, "ls-files", "--", "*.md")
				if !strings.HasPrefix(files, "apple (renaming ") {
					t.Fatalf("committed: %q", files)
				}
				s.Close()
				return
			}
			crash(s)
			s2 := openAt(t, root)
			defer s2.Close()
			if err := s2.Flush(); err != nil {
				t.Fatal(err)
			}
			// No transaction of the move was journaled: it is taken back.
			files, _ := gitTry(root, "ls-files", "--", "*.md")
			if files != "Apple.md" || !listed(root, "Apple.md") {
				t.Fatalf("committed: %q", files)
			}
			if _, err := s2.Get(p.ID); err != nil {
				t.Fatalf("the page is lost: %v", err)
			}
		})
	}
	t.Run("folder", func(t *testing.T) {
		root := t.TempDir()
		s := openAt(t, root)
		p := mustCreate(t, s, "notes", meta("title", "Apple"), "fruit\n")
		if err := s.Flush(); err != nil {
			t.Fatal(err)
		}
		s.ops = foldingOps(stop)
		if _, err := s.MoveFolder(Author{}, "notes", "Notes"); err == nil {
			t.Fatal("the move went through although the rename did not")
		}
		crash(s)
		s2 := openAt(t, root)
		defer s2.Close()
		if err := s2.Flush(); err != nil {
			t.Fatal(err)
		}
		files, _ := gitTry(root, "ls-files", "--", "*.md")
		if files != "notes/Apple.md" || !listed(root, "notes") {
			t.Fatalf("committed: %q", files)
		}
		if _, err := s2.Get(p.ID); err != nil {
			t.Fatalf("the page is lost: %v", err)
		}
	})
}

// pathsOf lists the paths of a pending set, sorted.
func pathsOf(m map[string]Author) []string {
	out := make([]string, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
