package space

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/text/unicode/norm"

	"petalbook.app/server/internal/index"
	"petalbook.app/server/internal/page"
)

// --- 1. a name the file system treats as taken must not hang a create --------

func TestCreateWithNameTakenInAnotherUnicodeForm(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	nfd := norm.NFD.String("Café")
	if nfd == "Café" {
		t.Fatal("test needs a decomposed name")
	}
	if err := os.WriteFile(filepath.Join(root, nfd+".md"), []byte("---\nid: NFDNFDNFD0\ntitle: Café\n---\nold\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := openAt(t, root)

	done := make(chan *Page, 1)
	go func() {
		p, err := s.Create(Author{}, CreateInput{Meta: meta("title", "Café")})
		if err != nil {
			t.Errorf("create: %v", err)
		}
		done <- p
	}()
	select {
	case p := <-done:
		if p == nil || p.Path != "Café 2.md" {
			t.Fatalf("%+v", p)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("create hangs on a name taken in another Unicode form")
	}
	// The lock was released: other requests and shutdown still work.
	if _, err := s.Get("NFDNFDNFD0"); err != nil {
		t.Fatal(err)
	}
	// The file system's own refusal (exclusive create) also ends in an error,
	// not a loop, when every candidate looks free to the listing.
	always := s.ops
	always.Link = func(string, string) error { return fs.ErrExist }
	always.ReadDir = func(string) ([]fs.DirEntry, error) { return nil, nil }
	s.ops = always
	errc := make(chan error, 1)
	go func() { _, err := s.Create(Author{}, CreateInput{Meta: meta("title", "Never")}); errc <- err }()
	select {
	case err := <-errc:
		if !errors.Is(err, ErrExists) {
			t.Fatalf("want ErrExists, got %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("unbounded retry loop")
	}
	s.ops = osOps()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// --- 2. a rename never replaces a different file -----------------------------

type fakeInfo struct {
	name string
	ino  int
}

func (f fakeInfo) Name() string       { return f.name }
func (f fakeInfo) Size() int64        { return 1 }
func (f fakeInfo) Mode() fs.FileMode  { return 0o644 }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return false }
func (f fakeInfo) Sys() any           { return nil }

// fakeFS is a directory of files with inode numbers, case-sensitive or not.
type fakeFS struct {
	mu   sync.Mutex
	fold bool
	// selfNoop: a rename onto a name that is the same entry does nothing and
	// succeeds, as on Linux (rename(2) of a file onto itself).
	selfNoop bool
	files    map[string]int // exact name -> inode
}

func (f *fakeFS) find(name string) (string, bool) {
	if !f.fold {
		_, ok := f.files[name]
		return name, ok
	}
	for n := range f.files {
		if strings.EqualFold(n, name) {
			return n, true
		}
	}
	return "", false
}

func (f *fakeFS) ops() fileOps {
	base := func(p string) string { return filepath.Base(p) }
	return fileOps{
		Lstat: func(p string) (fs.FileInfo, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			n, ok := f.find(base(p))
			if !ok {
				return nil, fs.ErrNotExist
			}
			return fakeInfo{n, f.files[n]}, nil
		},
		SameFile: func(a, b fs.FileInfo) bool { return a.(fakeInfo).ino == b.(fakeInfo).ino },
		Rename: func(from, to string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			n, ok := f.find(base(from))
			if !ok {
				return fs.ErrNotExist
			}
			ino := f.files[n]
			if t, ok := f.find(base(to)); ok && f.selfNoop && f.files[t] == ino {
				return nil
			}
			delete(f.files, n)
			if old, ok := f.find(base(to)); ok { // rename(2) replaces
				delete(f.files, old)
			}
			f.files[base(to)] = ino
			return nil
		},
		Link: func(from, to string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if _, ok := f.find(base(to)); ok {
				return fs.ErrExist
			}
			n, _ := f.find(base(from))
			f.files[base(to)] = f.files[n]
			return nil
		},
		Remove: func(p string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			n, ok := f.find(base(p))
			if !ok {
				return fs.ErrNotExist
			}
			// A hard link: only this name goes. Names are distinct entries.
			delete(f.files, n)
			return nil
		},
		ReadDir: func(string) ([]fs.DirEntry, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			var out []fs.DirEntry
			for n, ino := range f.files {
				out = append(out, fs.FileInfoToDirEntry(fakeInfo{n, ino}))
			}
			return out, nil
		},
	}
}

func TestRenameNeverReplacesAnotherFile(t *testing.T) {
	t.Parallel()
	// Case-sensitive: A.md is page X (inode 1) and a.md is page Y (inode 2).
	cs := &fakeFS{files: map[string]int{"A.md": 1, "a.md": 2}}
	s := &Space{ops: cs.ops()}
	name, _, err := s.moveNamed(Author{}, "/d/A.md", "/d", "a", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if name != "a 2.md" {
		t.Fatalf("renamed to %q; a.md belongs to another page", name)
	}
	if cs.files["a.md"] != 2 || cs.files["a 2.md"] != 1 || len(cs.files) != 2 {
		t.Fatalf("files: %v", cs.files)
	}

	// Case-sensitive, nothing in the way: a case-only rename is fine.
	cs = &fakeFS{files: map[string]int{"A.md": 1}}
	s = &Space{ops: cs.ops()}
	if name, _, err = s.moveNamed(Author{}, "/d/A.md", "/d", "a", nil, false); err != nil || name != "a.md" || cs.files["a.md"] != 1 || len(cs.files) != 1 {
		t.Fatalf("%q %v %v", name, err, cs.files)
	}

	// Case-insensitive: "a.md" is the same file as "A.md", so renaming is allowed.
	ci := &fakeFS{fold: true, files: map[string]int{"A.md": 1}}
	s = &Space{ops: ci.ops()}
	if name, _, err = s.moveNamed(Author{}, "/d/A.md", "/d", "a", nil, false); err != nil || name != "a.md" || ci.files["a.md"] != 1 || len(ci.files) != 1 {
		t.Fatalf("%q %v %v", name, err, ci.files)
	}
	// The same where a rename onto the same entry changes nothing (Linux):
	// the file is still respelled, and nothing else is left.
	ci = &fakeFS{fold: true, selfNoop: true, files: map[string]int{"A.md": 1}}
	s = &Space{ops: ci.ops()}
	if name, _, err = s.moveNamed(Author{}, "/d/A.md", "/d", "a", nil, false); err != nil || name != "a.md" || ci.files["a.md"] != 1 || len(ci.files) != 1 {
		t.Fatalf("%q %v %v", name, err, ci.files)
	}
	// ... but a different file with the folded name is never replaced.
	ci = &fakeFS{fold: true, files: map[string]int{"B.md": 1, "a.md": 2}}
	s = &Space{ops: ci.ops()}
	if name, _, err = s.moveNamed(Author{}, "/d/B.md", "/d", "A", nil, false); err != nil || name != "A 2.md" || ci.files["a.md"] != 2 {
		t.Fatalf("%q %v %v", name, err, ci.files)
	}
	// A move into another folder skips taken names, whatever their spelling.
	cs = &fakeFS{files: map[string]int{"x.md": 1, "X.md": 2, "X 2.md": 3}}
	s = &Space{ops: cs.ops()}
	if name, _, err = s.moveNamed(Author{}, "/src/x.md", "/d", "X", nil, false); err != nil || name != "X 3.md" {
		t.Fatalf("%q %v %v", name, err, cs.files)
	}
}

// On a case-sensitive file system the same thing through Save.
func TestSaveRenameDoesNotOverwriteCaseVariant(t *testing.T) {
	t.Parallel()
	needGit(t)
	probe := t.TempDir()
	os.WriteFile(filepath.Join(probe, "p"), nil, 0o644)
	if _, err := os.Stat(filepath.Join(probe, "P")); err == nil {
		t.Skip("case-insensitive file system; covered by TestRenameNeverReplacesAnotherFile")
	}
	s := open(t)
	x := mustCreate(t, s, "", meta("title", "A"), "page X\n")
	y := mustCreate(t, s, "", meta("title", "B"), "page Y\n")
	// Make Y's file a.md, as a person could.
	if err := os.Rename(filepath.Join(s.Root(), y.Path), filepath.Join(s.Root(), "a.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	n, err := s.Save(Author{}, x.ID, x.Rev, meta("title", "a"), "page X\n")
	if err != nil {
		t.Fatal(err)
	}
	if n.Path == "a.md" || readDisk(t, s, "a.md") == "" || !strings.Contains(readDisk(t, s, "a.md"), "page Y") {
		t.Fatalf("page Y was overwritten (X is now %q)", n.Path)
	}
}

// --- 3. never act on a file whose id differs ---------------------------------

// swapped makes the index stale: X's file is moved away and Y's file takes the
// path X had.
func swapped(t *testing.T) (s *Space, x, y *Page, xNow string) {
	t.Helper()
	s = open(t)
	x = mustCreate(t, s, "", meta("title", "X"), "page X\n")
	y = mustCreate(t, s, "", meta("title", "Y"), "page Y\n")
	xNow = "moved-away.md"
	r := s.Root()
	if err := os.Rename(filepath.Join(r, x.Path), filepath.Join(r, xNow)); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(r, y.Path), filepath.Join(r, x.Path)); err != nil {
		t.Fatal(err)
	}
	return
}

func TestDeleteActsOnTheRightFile(t *testing.T) {
	t.Parallel()
	s, x, y, xNow := swapped(t)
	if err := s.Delete(Author{}, x.ID); err != nil {
		t.Fatal(err)
	}
	if exists(s, xNow) {
		t.Fatal("page X's file was not deleted")
	}
	if !exists(s, x.Path) || !strings.Contains(readDisk(t, s, x.Path), "page Y") {
		t.Fatal("page Y's file was deleted instead")
	}
	if g, err := s.Get(y.ID); err != nil || g.Body != "page Y\n" || g.Path != x.Path {
		t.Fatalf("%v %+v", err, g)
	}
	// An id that no longer exists anywhere is not found, and nothing is deleted.
	if err := s.Delete(Author{}, x.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if !exists(s, x.Path) {
		t.Fatal("deleted a file for an id that is gone")
	}
}

func TestSaveAndMoveActOnTheRightFile(t *testing.T) {
	t.Parallel()
	s, x, y, xNow := swapped(t)
	// The conflict answer shows X, never Y.
	_, err := s.Save(Author{}, x.ID, "stale", x.Meta, "mine\n")
	var ce *ConflictError
	if !errors.As(err, &ce) || ce.Current.ID != x.ID || ce.Current.Body != "page X\n" || ce.Current.Path != xNow {
		t.Fatalf("%v %+v", err, ce)
	}
	n, err := s.Save(Author{}, x.ID, ce.Current.Rev, x.Meta, "X edited\n")
	if err != nil || n.ID != x.ID || n.Path != xNow {
		t.Fatalf("%v %+v", err, n)
	}
	if !strings.Contains(readDisk(t, s, x.Path), "page Y") {
		t.Fatal("page Y's file was overwritten")
	}
	m, err := s.Move(Author{}, x.ID, "sub")
	if err != nil || m.ID != x.ID || !strings.HasPrefix(m.Path, "sub/") {
		t.Fatalf("%v %+v", err, m)
	}
	if g, err := s.Get(y.ID); err != nil || g.Body != "page Y\n" {
		t.Fatalf("%v %+v", err, g)
	}
	// Gone entirely: not found for every write.
	os.Remove(filepath.Join(s.Root(), m.Path))
	if _, err := s.Save(Author{}, x.ID, m.Rev, x.Meta, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if _, err := s.Move(Author{}, x.ID, "other"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

// --- 5. header order is the file's -------------------------------------------

func TestSaveKeepsFileOrderWhateverTheRequestOrder(t *testing.T) {
	t.Parallel()
	s := open(t)
	p := mustCreate(t, s, "", meta("title", "T"), "b\n")
	today := p.Meta.Created()
	hand := "---\nid: " + p.ID + "\n2024: 年度\ntitle: T\nzeta: z\ncreated: " + today + "\nalpha: a\n---\nb\n"
	if err := os.WriteFile(filepath.Join(s.Root(), p.Path), []byte(hand), 0o644); err != nil {
		t.Fatal(err)
	}
	s.Reindex()
	g, _ := s.Get(p.ID)

	// A browser puts integer-like keys first; the file does not follow.
	req := page.Meta{{Key: "2024", Value: "年度"}, {Key: "alpha", Value: "a"}, {Key: "zeta", Value: "z"}, {Key: "title", Value: "T"}, {Key: "id", Value: p.ID}}
	n, err := s.Save(Author{}, p.ID, g.Rev, req, "b\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := readDisk(t, s, n.Path); got != hand {
		t.Fatalf("reordered the header:\n%s\nwant\n%s", got, hand)
	}

	// New keys are appended in request order; absent keys are removed.
	req = page.Meta{{Key: "new2", Value: int64(2)}, {Key: "title", Value: "T"}, {Key: "new1", Value: "n"}, {Key: "2024", Value: "年度"}}
	n, err = s.Save(Author{}, p.ID, n.Rev, req, "b\n")
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, f := range page.Parse([]byte(readDisk(t, s, n.Path))).Meta() {
		keys = append(keys, f.Key)
	}
	if got := strings.Join(keys, ","); got != "id,2024,title,created,new2,new1" {
		t.Fatalf("keys %s", got)
	}
}

// --- 8. folders that differ only in case or Unicode form ----------------------

func TestFolderNamesResolveToTheExistingSpelling(t *testing.T) {
	t.Parallel()
	s := open(t)
	if _, err := s.CreateFolder(Author{}, "Tang/诗"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateFolder(Author{}, "tang/诗"); !errors.Is(err, ErrExists) {
		t.Fatalf("case variant of an existing folder: %v", err)
	}
	if c, err := s.CreateFolder(Author{}, "TANG/新"); err != nil || c != "Tang/新" {
		t.Fatalf("%q %v", c, err)
	}
	a := mustCreate(t, s, "", meta("title", "A"), "")
	m, err := s.Move(Author{}, a.ID, "tang")
	if err != nil || m.Path != "Tang/A.md" || m.Folder != "Tang" {
		t.Fatalf("%v %+v", err, m)
	}
	// Moving to another spelling of the folder it is in changes nothing.
	same, err := s.Move(Author{}, a.ID, "TANG")
	if err != nil || same.Path != "Tang/A.md" {
		t.Fatalf("%v %+v", err, same)
	}
	c := mustCreate(t, s, "tAnG/诗", meta("title", "C"), "")
	if c.Path != "Tang/诗/C.md" {
		t.Fatalf("%q", c.Path)
	}
	// Composed and decomposed forms are one folder.
	nfd := norm.NFD.String("café")
	if _, err := s.CreateFolder(Author{}, nfd); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateFolder(Author{}, norm.NFC.String("café")); !errors.Is(err, ErrExists) {
		t.Fatalf("NFC after NFD: %v", err)
	}
	tree, _ := s.Tree()
	seen := map[string]bool{}
	for _, f := range tree {
		k := page.NameKey(f.Path)
		if seen[k] {
			t.Fatalf("tree lists %q twice (case or form variants): %+v", f.Path, tree)
		}
		seen[k] = true
	}
	r, _ := s.List(index.Query{Folder: ptr("Tang")})
	if len(r.Items) != 1 {
		t.Fatalf("index and path disagree: %+v", r.Items)
	}
}

func ptr(s string) *string { return &s }

func TestMoveFailsCleanlyWhenNoNameIsFree(t *testing.T) {
	t.Parallel()
	s := open(t)
	a := mustCreate(t, s, "", meta("title", "A"), "")
	s.ops.Link = func(string, string) error { return fs.ErrExist }
	s.ops.ReadDir = func(string) ([]fs.DirEntry, error) { return nil, nil }
	_, err := s.Move(Author{}, a.ID, "x")
	if !errors.Is(err, ErrExists) {
		t.Fatalf("%v", err)
	}
	s.ops = osOps()
	if !exists(s, a.Path) {
		t.Fatal("the page was lost")
	}
}

// --- 9 and 10. the committer ---------------------------------------------------

func TestCommitIncludesOnlyThePathsTheServerWrote(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	git(t, root, "init", "-q")
	os.WriteFile(filepath.Join(root, "mine.txt"), []byte("v1\n"), 0o644)
	os.WriteFile(filepath.Join(root, "draft.md"), []byte("---\nid: DRAFT00001\n---\nhand\n"), 0o644)
	git(t, root, "add", "mine.txt", "draft.md")
	git(t, root, "-c", "user.name=u", "-c", "user.email=u@x", "commit", "-q", "-m", "mine")
	// The person's work in progress: a modified tracked file, an untracked
	// file, a staged new file, and an unrelated edit to a tracked page.
	os.WriteFile(filepath.Join(root, "mine.txt"), []byte("v2 in progress\n"), 0o644)
	os.WriteFile(filepath.Join(root, "scratch.txt"), []byte("untracked\n"), 0o644)
	os.WriteFile(filepath.Join(root, "staged.txt"), []byte("staged\n"), 0o644)
	git(t, root, "add", "staged.txt")
	os.WriteFile(filepath.Join(root, "draft.md"), []byte("---\nid: DRAFT00001\n---\nhand edited\n"), 0o644)

	s := openAt(t, root)
	p := mustCreate(t, s, "", meta("title", "服务写的"), "x\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	files, _ := gitTry(root, "show", "--name-only", "--format=%s", "HEAD")
	lines := strings.Split(files, "\n")
	if !strings.HasPrefix(lines[0], "Add 服务写的") {
		t.Fatalf("message %q", lines[0])
	}
	got := strings.Join(lines[1:], "|")
	for _, bad := range []string{"mine.txt", "scratch.txt", "staged.txt", "draft.md"} {
		if strings.Contains(got, bad) {
			t.Fatalf("the commit swept in %s:\n%s", bad, files)
		}
	}
	for _, want := range []string{p.Path, ".petalbook/space.yml"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%s missing from the commit:\n%s", want, files)
		}
	}
	st, _ := gitTry(root, "status", "--porcelain")
	for _, want := range []string{"mine.txt", "?? scratch.txt", "A  staged.txt", "draft.md"} {
		if !strings.Contains(st, want) {
			t.Fatalf("%s is no longer pending in the working tree:\n%s", want, st)
		}
	}
	// The person's modification is intact, and so is their staged state.
	if b, _ := os.ReadFile(filepath.Join(root, "mine.txt")); string(b) != "v2 in progress\n" {
		t.Fatal("working file touched")
	}
}

func TestCommitWaitsWhileTheRepositoryIsBusy(t *testing.T) {
	t.Parallel()
	needGit(t)
	for _, marker := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_LOG", "rebase-merge", "rebase-apply"} {
		t.Run(marker, func(t *testing.T) {
			root := t.TempDir()
			s := openAt(t, root)
			if err := s.Flush(); err != nil { // the start-up files
				t.Fatal(err)
			}
			before := len(commits(t, root))
			target := filepath.Join(root, ".git", marker)
			if strings.HasPrefix(marker, "rebase") {
				os.Mkdir(target, 0o755)
			} else {
				os.WriteFile(target, []byte("x\n"), 0o644)
			}
			mustCreate(t, s, "", meta("title", "忙"), "")
			err := s.Flush()
			var busy *busyError
			if !errors.As(err, &busy) {
				t.Fatalf("want a busy error, got %v", err)
			}
			if len(commits(t, root)) != before {
				t.Fatal("committed in the middle of an operation")
			}
			os.RemoveAll(target)
			if err := s.Flush(); err != nil {
				t.Fatal(err)
			}
			if got := commits(t, root); len(got) != before+1 || got[0] != "Add 忙" {
				t.Fatalf("the change was not kept for later: %q", got)
			}
		})
	}
}

func TestSavesDoNotWaitForGit(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	p := mustCreate(t, s, "", meta("title", "并发"), "v1\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	s.cm.git.before = func(args []string) {
		if args[0] == "commit-tree" {
			once.Do(func() { close(entered) })
			<-release
		}
	}
	n, err := s.Save(Author{}, p.ID, p.Rev, meta("title", "并发"), "v2\n")
	if err != nil {
		t.Fatal(err)
	}
	flushed := make(chan error, 1)
	go func() { flushed <- s.Flush() }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("commit never started")
	}
	// git is busy committing v2. A save, a create and a read must not wait.
	finished := make(chan error, 1)
	go func() {
		m, err := s.Save(Author{}, p.ID, n.Rev, meta("title", "并发"), "v3 during the commit\n")
		if err == nil {
			_, err = s.Create(Author{}, CreateInput{Meta: meta("title", "期间新建")})
		}
		if err == nil {
			_, err = s.Get(m.ID)
		}
		finished <- err
	}()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("a save waited for git")
	}
	close(release)
	if err := <-flushed; err != nil {
		t.Fatal(err)
	}
	s.cm.git.before = nil
	// What landed during the commit is picked up by the next one, and by Close.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if body, _ := gitTry(root, "show", "HEAD:"+p.Path); !strings.HasSuffix(body, "v3 during the commit") {
		t.Fatalf("the save made during the commit is not in history: %q", body)
	}
	if got, _ := gitTry(root, "ls-files"); !strings.Contains(got, "期间新建.md") {
		t.Fatalf("the page created during the commit is not committed:\n%s", got)
	}
	if st, _ := gitTry(root, "status", "--porcelain"); st != "" {
		t.Fatalf("uncommitted changes remain:\n%s", st)
	}
}
