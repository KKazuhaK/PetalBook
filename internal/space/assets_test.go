package space

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/page"
)

func TestAssetIsJournaledBeforeItIsWrittenAndSurvivesACrash(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	p := mustCreate(t, s, "", meta("title", "Doc"), "x\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	data := []byte("\x89PNG picture")
	journal := filepath.Join(root, ".petalbook", "cache", "pending.journal")

	// The record is on disk at the moment the file gets its name.
	link := s.ops.Link
	var seen string
	s.ops.Link = func(from, to string) error {
		b, _ := os.ReadFile(journal)
		seen = string(b)
		return link(from, to)
	}
	a, err := s.PutAsset(Author{}, p.ID, "pic.png", data)
	if err != nil || !a.Created || a.Rel != "assets/"+p.ID+"/"+a.Name || a.Size != int64(len(data)) {
		t.Fatalf("%v %+v", err, a)
	}
	if !strings.Contains(seen, a.Rel) || !strings.Contains(seen, page.Rev(data)) {
		t.Fatalf("the journal at the time of the write: %q", seen)
	}
	crash(s)

	s2, err := Open(Options{Root: root, CommitIdle: time.Hour, CommitMax: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}
	if got, _ := gitTry(root, "ls-files"); !strings.Contains(got, a.Rel) {
		t.Fatalf("the file written before the crash was never committed:\n%s", got)
	}
	if got, _ := gitTry(root, "status", "--porcelain"); got != "" {
		t.Fatalf("%q", got)
	}
}

func TestPutAsset(t *testing.T) {
	s := open(t)
	p := mustCreate(t, s, "Tang", meta("title", "Doc"), "x\n")
	data := []byte("hello")

	if _, err := s.PutAsset(Author{}, "0000000000", "a.txt", data); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown page: %v", err)
	}
	if _, err := s.PutAsset(Author{}, "not an id", "a.txt", data); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bad id: %v", err)
	}
	var inv *InvalidError
	if _, err := s.PutAsset(Author{}, p.ID, "a.txt", nil); !errors.As(err, &inv) {
		t.Fatalf("empty: %v", err)
	}
	if _, err := s.PutAsset(Author{}, p.ID, "  ", data); !errors.As(err, &inv) {
		t.Fatalf("no name: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Root(), "assets")); !os.IsNotExist(err) {
		t.Fatalf("a refused upload made the directory: %v", err)
	}

	a, err := s.PutAsset(Author{}, p.ID, "a.txt", data)
	if err != nil || !a.Created {
		t.Fatalf("%v %+v", err, a)
	}
	b, err := s.PutAsset(Author{}, p.ID, "a.txt", data)
	if err != nil || b.Created || b.Rel != a.Rel {
		t.Fatalf("%v %+v", err, b)
	}
	// Another file holding the name (a clash of the short hash) is not replaced.
	target := filepath.Join(s.Root(), filepath.FromSlash(a.Rel))
	os.WriteFile(target, []byte("other bytes"), 0o644)
	if _, err := s.PutAsset(Author{}, p.ID, "a.txt", data); !errors.Is(err, ErrExists) {
		t.Fatalf("clash: %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "other bytes" {
		t.Fatalf("replaced: %q", got)
	}

	// The page is found after it moved; the files stay with its id.
	if _, err := s.Move(Author{}, p.ID, "Elsewhere"); err != nil {
		t.Fatal(err)
	}
	c, err := s.PutAsset(Author{}, p.ID, "b.txt", []byte("more"))
	if err != nil || !strings.HasPrefix(c.Rel, "assets/"+p.ID+"/") {
		t.Fatalf("%v %+v", err, c)
	}
	// A deleted page takes no more files.
	if err := s.Delete(Author{}, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutAsset(Author{}, p.ID, "c.txt", data); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted page: %v", err)
	}

	// assets being a link is refused, and nothing is written through it.
	s2 := open(t)
	p2 := mustCreate(t, s2, "", meta("title", "Doc"), "x\n")
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(s2.Root(), "assets")); err != nil {
		t.Skip("no symbolic links here")
	}
	if _, err := s2.PutAsset(Author{}, p2.ID, "a.txt", data); !errors.As(err, &inv) {
		t.Fatalf("link: %v", err)
	}
	if l, _ := os.ReadDir(outside); len(l) != 0 {
		t.Fatalf("written through the link: %v", l)
	}
}

func TestOpenFile(t *testing.T) {
	s := open(t)
	p := mustCreate(t, s, "Tang", meta("title", "Doc"), "x\n")
	a, err := s.PutAsset(Author{}, p.ID, "a.bin", []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	f, st, err := s.OpenFile(a.Rel)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	buf.ReadFrom(f)
	f.Close()
	if buf.String() != "payload" || st.Size() != 7 {
		t.Fatalf("%q %d", buf.String(), st.Size())
	}
	outside := filepath.Join(t.TempDir(), "o.txt")
	os.WriteFile(outside, []byte("secret"), 0o644)
	os.Symlink(outside, filepath.Join(s.Root(), "l.txt"))
	os.Symlink(filepath.Dir(outside), filepath.Join(s.Root(), "ld"))
	for _, bad := range []string{
		"", "/", ".", "..", "../x", "a/../" + a.Rel, "/" + a.Rel, a.Rel + "/", "assets", "assets/" + p.ID, p.Path, strings.ToUpper(p.Path),
		".git/config", ".petalbook/space.yml", "Tang/.x", "l.txt", "ld/o.txt", "nope", a.Rel + "/x", "a\x00b", "assets//" + p.ID + "/" + a.Name,
	} {
		if f, _, err := s.OpenFile(bad); !errors.Is(err, ErrNotFound) {
			if f != nil {
				f.Close()
			}
			t.Errorf("%q: %v", bad, err)
		}
	}
}

// An ignore rule that matches a file the server wrote must not stop the
// commit, nor the commits after it.
func TestAnIgnoredAttachmentIsStillCommitted(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*.pdf\n*.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := mustCreate(t, s, "", meta("title", "Doc"), "x\n")
	a, err := s.PutAsset(Author{}, p.ID, "c.pdf", []byte("%PDF-1.7"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	second := mustCreate(t, s, "", meta("title", "Second"), "y\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	got, _ := gitTry(root, "ls-tree", "-r", "--name-only", "HEAD")
	for _, want := range []string{a.Rel, "Doc.md", "Second.md"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%s is not committed (page %s):\n%s", want, second.ID, got)
		}
	}
}

// On a file system that ignores case, a folder of pages called Assets would
// receive the attachments.
func TestAFolderNamedLikeAssetsInAnotherCaseIsNotUsed(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "Assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := openAt(t, root)
	p := mustCreate(t, s, "", meta("title", "Doc"), "x\n")
	_, err := s.PutAsset(Author{}, p.ID, "a.png", []byte("x"))
	if _, lerr := os.Lstat(filepath.Join(root, "assets")); lerr == nil {
		// The file system ignores case: the upload must have been refused.
		var inv *InvalidError
		if !errors.As(err, &inv) {
			t.Fatalf("stored inside the folder Assets: %v", err)
		}
	}
	if _, cerr := s.CreateFolder(Author{}, "ASSETS"); cerr == nil {
		t.Fatal("a folder ASSETS could be made")
	}
}

// A folder cannot be named like a page file; and when something outside the
// server puts a directory where a deleted page's file was, the commit goes
// through all the same, and so do the ones after it.
func TestAFileThatBecameAFolderDoesNotBlockCommits(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	p := mustCreate(t, s, "", meta("title", "Foo"), "x\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	var inv *InvalidError
	if _, err := s.CreateFolder(Author{}, "Bar.MD"); !errors.As(err, &inv) {
		t.Fatalf("a folder named like a page: %v", err)
	}
	if _, err := s.Create(Author{}, CreateInput{Folder: "a/b.md", Meta: meta("title", "In")}); !errors.As(err, &inv) {
		t.Fatalf("a page in a new folder named like a page: %v", err)
	}
	if err := s.Delete(Author{}, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "Foo.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatalf("the commit after the swap: %v", err)
	}
	mustCreate(t, s, "", meta("title", "Other"), "y\n")
	if err := s.Flush(); err != nil {
		t.Fatalf("a later commit: %v", err)
	}
	if got, _ := gitTry(root, "ls-tree", "-r", "--name-only", "HEAD"); !strings.Contains(got, "Other.md") {
		t.Fatalf("%s", got)
	}
}

// Renaming a page to another spelling of its own name (a change of case) is
// committed, and nothing is wedged or lost, whatever the file system thinks
// of case.
func TestACaseOnlyRenameIsCommitted(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	p := mustCreate(t, s, "", meta("title", "Apple"), "x\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(Author{}, p.ID, p.Rev, meta("id", p.ID, "title", "apple"), "x\n"); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatalf("the commit of the rename: %v", err)
	}
	mustCreate(t, s, "", meta("title", "Other"), "y\n")
	if err := s.Flush(); err != nil {
		t.Fatalf("a later commit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "apple.md")); err != nil {
		t.Fatal(err)
	}
	if got, _ := gitTry(root, "ls-tree", "-r", "--name-only", "HEAD"); !strings.Contains(got, "Other.md") {
		t.Fatalf("%s", got)
	}
}
