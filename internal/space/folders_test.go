package space

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/index"
)

func folderPaths(t *testing.T, s *Space) []string {
	t.Helper()
	tree, err := s.Tree()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range tree {
		out = append(out, f.Path)
	}
	return out
}

func pathOf(t *testing.T, s *Space, id string) string {
	t.Helper()
	p, err := s.Get(id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return p.Path
}

func TestMoveFolder(t *testing.T) {
	s := open(t)
	root := s.Root()
	a := mustCreate(t, s, "随笔/2026", meta("title", "一"), "x\n")
	b := mustCreate(t, s, "随笔/2026/深", meta("title", "二"), "y\n")
	keep := mustCreate(t, s, "别处", meta("title", "三"), "z\n")
	if _, err := s.CreateFolder(Author{}, "随笔/2026/空"); err != nil {
		t.Fatal(err)
	}
	// A file that is not a page and that Git has, and one that merely lies there.
	if err := os.WriteFile(filepath.Join(root, "随笔", "2026", "图.png"), []byte("PNG"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "随笔/2026/图.png")
	git(t, root, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-qm", "a picture")
	if err := os.WriteFile(filepath.Join(root, "随笔", "2026", "leftover.tmp"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A page written and not committed yet moves, and is committed where it lands.
	c := mustCreate(t, s, "随笔/2026", meta("title", "四"), "w\n")

	got, err := s.MoveFolder(Author{}, "随笔/2026", "归档/旧/2026")
	if err != nil || got != "归档/旧/2026" {
		t.Fatalf("%q %v", got, err)
	}
	for id, want := range map[string]string{a.ID: "归档/旧/2026/一.md", b.ID: "归档/旧/2026/深/二.md", c.ID: "归档/旧/2026/四.md", keep.ID: "别处/三.md"} {
		if p := pathOf(t, s, id); p != want {
			t.Errorf("%s is at %s, want %s", id, p, want)
		}
	}
	folders := strings.Join(folderPaths(t, s), "|")
	for _, want := range []string{"归档", "归档/旧", "归档/旧/2026", "归档/旧/2026/深", "归档/旧/2026/空", "随笔", "别处"} {
		if !strings.Contains("|"+folders+"|", "|"+want+"|") {
			t.Errorf("folder %q is missing from %s", want, folders)
		}
	}
	if strings.Contains(folders, "随笔/2026") {
		t.Errorf("the old folder is still listed: %s", folders)
	}
	f := "归档/旧/2026"
	res, err := s.List(index.Query{Folder: &f, Recursive: true})
	if err != nil || len(res.Items) != 3 {
		t.Fatalf("%v %d", err, len(res.Items))
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	tree := git(t, root, "ls-tree", "-r", "--name-only", "HEAD")
	tree = unquoteGitPaths(t, root)
	for _, want := range []string{"归档/旧/2026/一.md", "归档/旧/2026/深/二.md", "归档/旧/2026/四.md", "归档/旧/2026/图.png", "归档/旧/2026/空/.gitkeep"} {
		if !strings.Contains(tree, want+"\n") {
			t.Errorf("%q is not committed:\n%s", want, tree)
		}
	}
	if strings.Contains(tree, "随笔/2026/") || strings.Contains(tree, "leftover") {
		t.Errorf("the old paths, or a file that was never the repository's, are committed:\n%s", tree)
	}
	if _, err := os.Stat(filepath.Join(root, "归档", "旧", "2026", "leftover.tmp")); err != nil {
		t.Errorf("the file that lay in the folder did not move with it: %v", err)
	}
	if st, _ := gitTry(root, "status", "--porcelain", "--untracked-files=no"); st != "" {
		t.Fatalf("the clone is not clean: %q", st)
	}
	// One commit for the move.
	if msg := git(t, root, "log", "-1", "--format=%s"); !strings.Contains(strings.ToLower(msg), "move") && !strings.Contains(msg, "page") {
		t.Logf("commit message: %q", msg)
	}
}

func unquoteGitPaths(t *testing.T, root string) string {
	t.Helper()
	out, err := gitTry(root, "ls-tree", "-r", "--name-only", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return out + "\n"
}

func TestMoveFolderRenamesByCase(t *testing.T) {
	s := open(t)
	p := mustCreate(t, s, "notes", meta("title", "One"), "x\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	got, err := s.MoveFolder(Author{}, "notes", "Notes")
	if err != nil || got != "Notes" {
		t.Fatalf("%q %v", got, err)
	}
	if path := pathOf(t, s, p.ID); path != "Notes/One.md" {
		t.Fatalf("%s", path)
	}
	folders := folderPaths(t, s)
	sort.Strings(folders)
	if strings.Join(folders, "|") != "|Notes" {
		t.Fatalf("%v", folders)
	}
	entries, _ := os.ReadDir(s.Root())
	for _, e := range entries {
		if e.Name() == "notes" {
			t.Fatal("the old spelling is still on disk")
		}
	}
	if err := s.Flush(); err != nil {
		t.Fatalf("the commit of the rename: %v", err)
	}
	mustCreate(t, s, "", meta("title", "Later"), "y\n")
	if err := s.Flush(); err != nil {
		t.Fatalf("a later commit: %v", err)
	}
}

func TestMoveFolderRefusals(t *testing.T) {
	s := open(t)
	mustCreate(t, s, "a/b", meta("title", "P"), "x\n")
	mustCreate(t, s, "c", meta("title", "Q"), "y\n")
	var inv *InvalidError
	for _, c := range []struct{ from, to string }{
		{"", "x"}, {"a", ""}, {"a", "a"}, {"a", "a/b/c"}, {"a", "a/inside"}, {"a", "x.md"}, {"a", ".hidden"}, {"a", "assets"}, {"a", "new.md/x"},
	} {
		if _, err := s.MoveFolder(Author{}, c.from, c.to); !errors.As(err, &inv) {
			t.Errorf("%q -> %q: %v", c.from, c.to, err)
		}
	}
	if _, err := s.MoveFolder(Author{}, "nope", "x"); !errors.Is(err, ErrNoFolder) {
		t.Errorf("a folder that is not there: %v", err)
	}
	if _, err := s.MoveFolder(Author{}, "a/b/P.md", "x"); !errors.Is(err, ErrNoFolder) {
		t.Errorf("a page as the folder: %v", err)
	}
	if _, err := s.MoveFolder(Author{}, "a", "c"); !errors.Is(err, ErrExists) {
		t.Errorf("an existing target: %v", err)
	}
	// A link inside the folder: it could lead anywhere.
	if err := os.Symlink(t.TempDir(), filepath.Join(s.Root(), "a", "link")); err != nil {
		t.Skip("no symbolic links here")
	}
	if _, err := s.MoveFolder(Author{}, "a", "moved"); !errors.As(err, &inv) {
		t.Errorf("a folder holding a link: %v", err)
	}
	if got := strings.Join(folderPaths(t, s), "|"); got != "|a|a/b|c" {
		t.Errorf("something changed: %s", got)
	}
	if _, err := os.Stat(filepath.Join(s.Root(), "new.md")); !os.IsNotExist(err) {
		t.Errorf("a refused move left a folder behind")
	}
}

// The journal names every path of the move before the folder is renamed, so a
// crash on either side of the rename leaves nothing uncommitted for good.
func TestMoveFolderIsJournaledBeforeTheRename(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	p := mustCreate(t, s, "old", meta("title", "Doc"), "x\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(root, ".petalbook", "cache", "pending.journal")
	rename := s.ops.Rename
	var seen string
	s.ops.Rename = func(from, to string) error {
		b, _ := os.ReadFile(journal)
		seen = string(b)
		return rename(from, to)
	}
	if _, err := s.MoveFolder(Author{}, "old", "new"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seen, `"old/Doc.md"`) || !strings.Contains(seen, `"new/Doc.md"`) {
		t.Fatalf("the journal at the time of the rename: %q", seen)
	}
	crash(s)

	s2, err := Open(Options{Root: root, CommitIdle: time.Hour, CommitMax: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if got := pathOf(t, s2, p.ID); got != "new/Doc.md" {
		t.Fatalf("%s", got)
	}
	if err := s2.Flush(); err != nil {
		t.Fatal(err)
	}
	if got, _ := gitTry(root, "ls-tree", "-r", "--name-only", "HEAD"); !strings.Contains(got, "new/Doc.md") || strings.Contains(got, "old/Doc.md") {
		t.Fatalf("after recovery:\n%s", got)
	}
	if st, _ := gitTry(root, "status", "--porcelain"); st != "" {
		t.Fatalf("%q", st)
	}
}

func TestDeleteFolder(t *testing.T) {
	s := open(t)
	a := mustCreate(t, s, "f", meta("title", "A"), "x\n")
	b := mustCreate(t, s, "f/sub", meta("title", "B"), "y\n")
	if _, err := s.CreateFolder(Author{}, "f/empty/deeper"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateFolder(Author{}, "g/h"); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	var inv *InvalidError
	if _, err := s.DeleteFolder(Author{}, "", true); !errors.As(err, &inv) {
		t.Fatal(err)
	}
	if _, err := s.DeleteFolder(Author{}, "nope", true); !errors.Is(err, ErrNoFolder) {
		t.Fatal(err)
	}
	// Pages inside: refused unless they are to go too.
	if _, err := s.DeleteFolder(Author{}, "f", false); !errors.Is(err, ErrNotEmpty) {
		t.Fatal(err)
	}
	if _, err := s.Get(a.ID); err != nil {
		t.Fatal("a refused removal deleted a page")
	}
	// No pages: the folder and its empty subfolders go.
	if ids, err := s.DeleteFolder(Author{}, "g", false); err != nil || len(ids) != 0 {
		t.Fatalf("%v %v", ids, err)
	}
	if _, err := os.Stat(filepath.Join(s.Root(), "g")); !os.IsNotExist(err) {
		t.Fatalf("g is still there: %v", err)
	}
	// With its pages. A file that is not a page stays, and so does its folder.
	if err := os.WriteFile(filepath.Join(s.Root(), "f", "sub", "note.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ids, err := s.DeleteFolder(Author{}, "f", true)
	sort.Strings(ids)
	want := []string{a.ID, b.ID}
	sort.Strings(want)
	if err != nil || strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("%v %v", ids, err)
	}
	for _, id := range ids {
		if _, err := s.Get(id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s: %v", id, err)
		}
	}
	if _, err := os.Stat(filepath.Join(s.Root(), "f", "sub", "note.txt")); err != nil {
		t.Fatalf("the other file was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Root(), "f", "empty")); !os.IsNotExist(err) {
		t.Fatalf("the empty subfolder stayed: %v", err)
	}
	if got := strings.Join(folderPaths(t, s), "|"); got != "|f|f/sub" {
		t.Fatalf("folders: %s", got)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if tree, _ := gitTry(s.Root(), "ls-tree", "-r", "--name-only", "HEAD"); strings.Contains(tree, "A.md") || strings.Contains(tree, ".gitkeep") {
		t.Fatalf("still committed:\n%s", tree)
	}
}

func TestBatch(t *testing.T) {
	s := open(t)
	a := mustCreate(t, s, "x", meta("title", "A"), "1\n")
	b := mustCreate(t, s, "y", meta("title", "B"), "2\n")
	c := mustCreate(t, s, "dest", meta("title", "C"), "3\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	var inv *InvalidError
	for _, ids := range [][]string{nil, {a.ID, a.ID}, {"not an id"}, make([]string, MaxBatch+1)} {
		if err := s.BatchMove(Author{}, ids, "dest"); !errors.As(err, &inv) {
			t.Errorf("%v: %v", ids, err)
		}
		if _, err := s.BatchDelete(Author{}, ids); !errors.As(err, &inv) {
			t.Errorf("%v: %v", ids, err)
		}
	}
	// An id that is not a page: nothing moves.
	if err := s.BatchMove(Author{}, []string{a.ID, "ZZZZZZZZZZ", b.ID}, "dest"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if pathOf(t, s, a.ID) != "x/A.md" {
		t.Fatal("a page moved although the batch was refused")
	}
	if err := s.BatchMove(Author{}, []string{a.ID, b.ID}, "bad.md"); !errors.As(err, &inv) {
		t.Fatal(err)
	}
	head := git(t, s.Root(), "rev-parse", "HEAD")
	if err := s.BatchMove(Author{}, []string{a.ID, b.ID, c.ID}, "dest"); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{a.ID: "dest/A.md", b.ID: "dest/B.md", c.ID: "dest/C.md"} {
		if got := pathOf(t, s, id); got != want {
			t.Errorf("%s at %s", id, got)
		}
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if n := git(t, s.Root(), "rev-list", "--count", head+"..HEAD"); n != "1" {
		t.Fatalf("%s commits for one batch", n)
	}
	if _, err := s.BatchDelete(Author{}, []string{a.ID, "ZZZZZZZZZZ"}); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.Get(a.ID); err != nil {
		t.Fatal("a page was deleted although the batch was refused")
	}
	if _, err := s.BatchDelete(Author{}, []string{a.ID, c.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.Get(b.ID); err != nil {
		t.Fatal(err)
	}
}

func TestTagColors(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	mustCreate(t, s, "", meta("title", "A", "tags", []any{"栀子"}), "x\n")
	var inv *InvalidError
	for _, c := range []struct{ tag, color string }{{"栀子", "mauve"}, {"", "red"}, {"  ", "red"}, {strings.Repeat("长", 100), "red"}, {"a\nb", "red"}} {
		if err := s.SetTagColor(Author{}, c.tag, c.color); !errors.As(err, &inv) {
			t.Errorf("%q %q: %v", c.tag, c.color, err)
		}
	}
	if err := s.SetTagColor(Author{}, "栀子", "rose"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTagColor(Author{}, "not in use", "blue"); err != nil {
		t.Fatal(err)
	}
	if got := s.TagColors(); got["栀子"] != "rose" || got["not in use"] != "blue" {
		t.Fatalf("%v", got)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if tree, _ := gitTry(root, "ls-tree", "-r", "--name-only", "HEAD"); !strings.Contains(tree, ".petalbook/tags.yml") {
		t.Fatalf("not committed:\n%s", tree)
	}
	// Keys the server does not know stay in the file.
	file := filepath.Join(root, ".petalbook", "tags.yml")
	if err := os.WriteFile(file, []byte("note: mine\ntags:\n  栀子: {color: rose, about: 夏天}\n  odd: {color: chartreuse}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := s.TagColors(); len(got) != 1 || got["栀子"] != "rose" {
		t.Fatalf("%v", got)
	}
	if err := s.SetTagColor(Author{}, "栀子", ""); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(file)
	if !strings.Contains(string(data), "note: mine") || !strings.Contains(string(data), "about: 夏天") || strings.Contains(string(data), "rose") {
		t.Fatalf("%s", data)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2 := openAt(t, root)
	if err := s2.SetTagColor(Author{}, "冬", "teal"); err != nil {
		t.Fatal(err)
	}
	if got := s2.TagColors(); got["冬"] != "teal" || got["栀子"] != "" {
		t.Fatalf("%v", got)
	}
	// A file that does not parse is nobody's to fix: the colours are those of
	// the newest version in the history that can be used, and the next write
	// puts the file right, keeping what it was beside it.
	if err := s2.Flush(); err != nil {
		t.Fatal(err)
	}
	broken := []byte("tags: [unclosed\n")
	if err := os.WriteFile(file, broken, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := s2.TagColors(); got["冬"] != "teal" {
		t.Fatalf("%v", got)
	}
	if err := s2.SetTagColor(Author{}, "冬", "rose"); err != nil {
		t.Fatalf("a broken file stopped a write: %v", err)
	}
	if got := s2.TagColors(); got["冬"] != "rose" {
		t.Fatalf("%v", got)
	}
	if kept := copiesOf(root, tagsSettings); len(kept) != 1 || readDisk(t, s2, kept[0]) != string(broken) {
		t.Fatalf("what the file was is not kept: %v", kept)
	}

	// Keys that are not text to YAML, other settings of an entry, comments
	// and keys the server does not know all survive a write.
	hand := "# my tags\nnote: mine\ntags:\n  2026: {color: red}  # the year\n  栀子: {color: rose, about: 夏天}\n  空:\n"
	if err := os.WriteFile(file, []byte(hand), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := s2.TagColors(); got["2026"] != "red" || got["栀子"] != "rose" || len(got) != 2 {
		t.Fatalf("%v", got)
	}
	for _, c := range [][2]string{{"冬", "teal"}, {"2026", "blue"}, {"空", "green"}, {"栀子", ""}} {
		if err := s2.SetTagColor(Author{}, c[0], c[1]); err != nil {
			t.Fatalf("%v: %v", c, err)
		}
	}
	if got := s2.TagColors(); got["2026"] != "blue" || got["冬"] != "teal" || got["空"] != "green" || got["栀子"] != "" || len(got) != 3 {
		t.Fatalf("%v", got)
	}
	data, _ = os.ReadFile(file)
	for _, want := range []string{"# my tags", "note: mine", "about: 夏天", "# the year"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("%q is gone:\n%s", want, data)
		}
	}
	if strings.Count(string(data), "2026") != 1 {
		t.Fatalf("the tag 2026 is there twice:\n%s", data)
	}

	// Too large: put aside like any file that cannot be used.
	if err := s2.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, append([]byte("tags: {}\n# "), bytes.Repeat([]byte("x"), 1<<20)...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s2.SetTagColor(Author{}, "冬", "pink"); err != nil {
		t.Fatalf("%v", err)
	}
	if got := s2.TagColors(); got["2026"] != "blue" || got["冬"] != "pink" {
		t.Fatalf("%v", got)
	}
}

// A page and a folder renamed to another spelling of their own names are
// committed under the new names; moved on from there, nothing of the old
// spelling is left in the history's last commit.
func TestRenamesByCaseAreCommitted(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	p := mustCreate(t, s, "notes", meta("title", "Apple"), "x\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(Author{}, p.ID, p.Rev, meta("id", p.ID, "title", "apple"), "x\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MoveFolder(Author{}, "notes", "Notes"); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if got, _ := gitTry(root, "ls-tree", "-r", "--name-only", "HEAD"); !strings.Contains(got, "Notes/apple.md") || strings.Contains(got, "notes/") || strings.Contains(got, "Apple.md") {
		t.Fatalf("HEAD has:\n%s", got)
	}
	if _, err := s.MoveFolder(Author{}, "Notes", "Other"); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	got, _ := gitTry(root, "ls-tree", "-r", "--name-only", "HEAD")
	if strings.Count(got, "apple.md") != 1 || !strings.Contains(got, "Other/apple.md") || strings.Contains(strings.ToLower(got), "notes/") {
		t.Fatalf("HEAD has:\n%s", got)
	}
	if st, _ := gitTry(root, "status", "--porcelain"); st != "" {
		t.Fatalf("%q", st)
	}
}

// A folder cannot be moved onto another spelling of a folder that exists:
// to the rest of the space, and to a file system that ignores case, the two
// are one.
func TestMoveFolderOntoAnotherSpellingOfAnExistingOne(t *testing.T) {
	needGit(t)
	s := openAt(t, t.TempDir())
	mustCreate(t, s, "a", meta("title", "In a"), "x\n")
	mustCreate(t, s, "c", meta("title", "In c"), "x\n")
	mustCreate(t, s, "deep/c", meta("title", "In deep c"), "x\n")
	for _, to := range []string{"C", "deep/C"} {
		if _, err := s.MoveFolder(Author{}, "a", to); !errors.Is(err, ErrExists) {
			t.Fatalf("a -> %s: %v", to, err)
		}
	}
	if _, err := s.MoveFolder(Author{}, "a", "A"); err != nil {
		t.Fatalf("its own respelling: %v", err)
	}
}

// Two files with one id: deleting the page makes the other file the page.
// A recursive delete of their folder ends with neither.
func TestDeleteFolderWithFilesSharingAnID(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	p := mustCreate(t, s, "twins", meta("title", "First"), "x\n")
	mustCreate(t, s, "twins", meta("title", "Other"), "y\n")
	data, err := os.ReadFile(filepath.Join(root, "twins", "First.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "twins", "Second.md"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	ids, err := s.DeleteFolder(Author{}, "twins", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || ids[0] == "" {
		t.Fatalf("deleted %v (the page is %s)", ids, p.ID)
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "twins")); len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("left in the folder: %v", names)
	}
	if res, _ := s.List(index.Query{}); len(res.Items) != 0 {
		t.Fatalf("%d pages are still listed", len(res.Items))
	}
}

// A colour of the person's own: six hexadecimal digits after #, kept in lower
// case. Anything else is refused, and a value in the file that is no colour
// leaves the tag without one.
func TestTagColorsOfOnesOwn(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	if err := s.SetTagColor(Author{}, "栀子", "#A1b2C3"); err != nil {
		t.Fatal(err)
	}
	if got := s.TagColors(); got["栀子"] != "#a1b2c3" {
		t.Fatalf("%v", got)
	}
	var inv *InvalidError
	for _, bad := range []string{"#abc", "#gggggg", "abcdef", "rgb(1,2,3)", "#a1b2c3d", "# a1b2c", "＃a1b2c3"} {
		if err := s.SetTagColor(Author{}, "栀子", bad); !errors.As(err, &inv) {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	if got := s.TagColors(); got["栀子"] != "#a1b2c3" {
		t.Fatalf("a refused colour changed something: %v", got)
	}
	file := filepath.Join(root, ".petalbook", "tags.yml")
	if err := os.WriteFile(file, []byte("tags:\n  a: {color: \"#FFEEDD\"}\n  b: {color: chartreuse}\n  c: {color: \"#12\"}\n  d: {color: rose}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := s.TagColors(); len(got) != 2 || got["a"] != "#ffeedd" || got["d"] != "rose" {
		t.Fatalf("%v", got)
	}
}

// foldsNames reports whether the file system here takes two differently
// written names for one.
func foldsNames(t *testing.T, a, b string) bool {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, a), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := os.Stat(filepath.Join(dir, b))
	return err == nil
}

// Names some file systems hold to be one although no lower-casing makes them
// equal: ß and ss, the Greek sigmas. Renaming across them is a respelling,
// committed as such, with one copy afterwards.
func TestMoveFolderAcrossUnicodeFolds(t *testing.T) {
	needGit(t)
	for _, c := range [][2]string{{"Straße", "Strasse"}, {"ΟΔΟΣ", "οδος"}} {
		t.Run(c[0], func(t *testing.T) {
			folds := foldsNames(t, c[0], c[1])
			root := t.TempDir()
			s := openAt(t, root)
			p := mustCreate(t, s, c[0], meta("title", "一"), "x\n")
			if err := s.Flush(); err != nil {
				t.Fatal(err)
			}
			if folds {
				// A page made "in" the other spelling lands in the folder that is there.
				q := mustCreate(t, s, c[1], meta("title", "二"), "y\n")
				if q.Folder != c[0] {
					t.Fatalf("the page went to %q, not into %q", q.Folder, c[0])
				}
				// And the folder cannot be moved into itself under its other name.
				var inv *InvalidError
				if _, err := s.MoveFolder(Author{}, c[0], c[1]+"/inner"); !errors.As(err, &inv) {
					t.Fatalf("into itself: %v", err)
				}
			}
			got, err := s.MoveFolder(Author{}, c[0], c[1])
			if err != nil || got != c[1] {
				t.Fatalf("%q %v", got, err)
			}
			if err := s.Flush(); err != nil {
				t.Fatal(err)
			}
			tree, _ := gitTry(root, "-c", "core.quotepath=off", "ls-tree", "-r", "--name-only", "HEAD")
			if strings.Contains(tree, c[0]+"/") || strings.Count(tree, "一.md") != 1 || !strings.Contains(tree, c[1]+"/一.md") {
				t.Fatalf("folds=%v, HEAD has:\n%s", folds, tree)
			}
			if st, _ := gitTry(root, "status", "--porcelain"); st != "" {
				t.Fatalf("%q", st)
			}
			if now, err := s.Get(p.ID); err != nil || now.Folder != c[1] {
				t.Fatalf("%+v %v", now, err)
			}
			entries, _ := os.ReadDir(root)
			n := 0
			for _, e := range entries {
				if e.Name() == c[0] || e.Name() == c[1] {
					n++
				}
			}
			if n != 1 {
				t.Fatalf("%d folders of that name on disk", n)
			}
		})
	}
}

// A file with two YAML documents is not rewritten (the second would be
// lost), and a key written twice is edited where readers see it: at its last.
func TestTagsFileWithSeveralDocumentsOrRepeatedKeys(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	if err := s.SetTagColor(Author{}, "a", "red"); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, ".petalbook", "tags.yml")
	two := []byte("tags:\n  a: {color: red}\n---\nsecond: document\n")
	if err := os.WriteFile(file, two, 0o644); err != nil {
		t.Fatal(err)
	}
	// Such a file cannot be used: the write is made on the last version that
	// can, and the file with its second document is kept beside it.
	if got := s.TagColors(); got["a"] != "red" {
		t.Fatalf("%v", got)
	}
	if err := s.SetTagColor(Author{}, "a", "blue"); err != nil {
		t.Fatalf("%v", err)
	}
	if got := s.TagColors(); got["a"] != "blue" {
		t.Fatalf("%v", got)
	}
	if kept := copiesOf(root, tagsSettings); len(kept) != 1 || readDisk(t, s, kept[0]) != string(two) {
		t.Fatalf("what the file was is not kept: %v", kept)
	}

	if err := os.WriteFile(file, []byte("tags:\n  a: {color: red}\n  b: {color: teal}\n  a: {color: green}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := s.TagColors(); got["a"] != "green" {
		t.Fatalf("%v", got)
	}
	if err := s.SetTagColor(Author{}, "a", "blue"); err != nil {
		t.Fatal(err)
	}
	if got := s.TagColors(); got["a"] != "blue" || got["b"] != "teal" {
		t.Fatalf("%v", got)
	}
	data, _ := os.ReadFile(file)
	if !strings.Contains(string(data), "red") || strings.Contains(string(data), "green") {
		t.Fatalf("the wrong entry was edited:\n%s", data)
	}
}
