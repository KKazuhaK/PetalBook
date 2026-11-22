package space

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/index"
	"github.com/KKazuhaK/PetalBook/internal/page"
)

func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

// open starts a space in a fresh directory with a committer that stays quiet
// (an hour) unless the test says otherwise.
func open(t *testing.T, mod ...func(*Options)) *Space {
	t.Helper()
	needGit(t)
	return openAt(t, t.TempDir(), mod...)
}

func openAt(t *testing.T, root string, mod ...func(*Options)) *Space {
	t.Helper()
	opt := Options{Root: root, CommitIdle: time.Hour, CommitMax: 2 * time.Hour, ResyncInterval: time.Millisecond}
	for _, m := range mod {
		m(&opt)
	}
	s, err := Open(opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func meta(kv ...any) page.Meta {
	var m page.Meta
	for i := 0; i < len(kv); i += 2 {
		m = append(m, page.Field{Key: kv[i].(string), Value: kv[i+1]})
	}
	return m
}

func mustCreate(t *testing.T, s *Space, folder string, m page.Meta, body string) *Page {
	t.Helper()
	p, err := s.Create(Author{}, CreateInput{Folder: folder, Meta: m, Body: body})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return p
}

func readDisk(t *testing.T, s *Space, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(s.Root(), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(s *Space, rel string) bool {
	_, err := os.Lstat(filepath.Join(s.Root(), filepath.FromSlash(rel)))
	return err == nil
}

func TestOpenInitialisesTheDirectory(t *testing.T) {
	needGit(t)
	root := filepath.Join(t.TempDir(), "notes", "deep") // does not exist yet
	s := openAt(t, root, func(o *Options) { o.Name = "我的笔记" })
	for _, f := range []string{".git", ".petalbook/space.yml", ".petalbook/.gitignore"} {
		if !exists(s, f) {
			t.Errorf("%s missing", f)
		}
	}
	if s.Name() != "我的笔记" || !strings.Contains(readDisk(t, s, ".petalbook/space.yml"), "我的笔记") {
		t.Fatalf("name %q, space.yml %q", s.Name(), readDisk(t, s, ".petalbook/space.yml"))
	}
	ign := readDisk(t, s, ".petalbook/.gitignore")
	if !strings.Contains(ign, "cache/") || !strings.Contains(ign, "tmp/") {
		t.Fatalf("gitignore: %q", ign)
	}
	s.Close()

	// The name in space.yml is used when none is given, and is not rewritten.
	s2 := openAt(t, root)
	if s2.Name() != "我的笔记" {
		t.Fatalf("name %q", s2.Name())
	}
	s2.Close()
	s3 := openAt(t, root, func(o *Options) { o.Name = "Other" })
	if s3.Name() != "Other" || !strings.Contains(readDisk(t, s3, ".petalbook/space.yml"), "我的笔记") {
		t.Fatal("a name given at start must not rewrite space.yml")
	}
	// Default name for a directory that is already a repository without space.yml.
	root2 := t.TempDir()
	git(t, root2, "init", "-q")
	if s4 := openAt(t, root2); s4.Name() != DefaultName {
		t.Fatalf("%q", s4.Name())
	}
}

func TestCreateAndGet(t *testing.T) {
	s := open(t)
	p := mustCreate(t, s, "Tang", meta("title", "暮雪", "subtitle", "下一个路口", "tags", []any{"玉兰", "黄昏", "玉兰"}, "series", "冬季", "id", "CLIENTID00", "created", "1999-01-01"), "你听得见么？\n")
	if !page.ValidID(p.ID) || p.ID == "CLIENTID00" {
		t.Fatalf("id %q", p.ID)
	}
	if p.Path != "Tang/暮雪.md" || p.Folder != "Tang" {
		t.Fatalf("path %q folder %q", p.Path, p.Folder)
	}
	today := time.Now().UTC().Format("2006-01-02")
	if p.Meta.Created() != today {
		t.Fatalf("created %q", p.Meta.Created())
	}
	var keys []string
	for _, f := range p.Meta {
		keys = append(keys, f.Key)
	}
	if got := strings.Join(keys, ","); got != "id,title,subtitle,tags,created,series" {
		t.Fatalf("header order: %s", got)
	}
	if tags := p.Meta.Tags(); len(tags) != 2 {
		t.Fatalf("tags not deduplicated: %v", tags)
	}
	want := "---\nid: " + p.ID + "\ntitle: 暮雪\nsubtitle: 下一个路口\ntags: [玉兰, 黄昏]\ncreated: " + today + "\nseries: 冬季\n---\n你听得见么？\n"
	if got := readDisk(t, s, p.Path); got != want {
		t.Fatalf("file:\n%s\nwant:\n%s", got, want)
	}
	if p.Rev != page.Rev([]byte(want)) || p.Body != "你听得见么？\n" {
		t.Fatalf("rev/body %s %q", p.Rev, p.Body)
	}
	g, err := s.Get(p.ID)
	if err != nil || g.Rev != p.Rev || g.Path != p.Path || !page.MetaEqual(g.Meta, p.Meta) {
		t.Fatalf("get: %v %+v", err, g)
	}
	if _, err := s.Get("0000000000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
	tree, _ := s.Tree()
	if len(tree) != 2 || tree[0] != (index.FolderCount{Path: "", Pages: 0}) || tree[1] != (index.FolderCount{Path: "Tang", Pages: 1}) {
		t.Fatalf("tree %+v", tree)
	}
	// Listing finds it and reports the summary fields.
	r, _ := s.List(index.Query{Tags: []string{"玉兰"}})
	if len(r.Items) != 1 || r.Items[0].ID != p.ID || r.Items[0].Label != "暮雪" {
		t.Fatalf("%+v", r)
	}
}

func TestUntitledPage(t *testing.T) {
	s := open(t)
	p := mustCreate(t, s, "", meta("title", "   ", "subtitle", "", "tags", []any{}), "只有一句话，没有标题。\n")
	today := time.Now().UTC().Format("2006-01-02")
	wantName := today + "-" + page.ShortID(p.ID) + ".md"
	if p.Path != wantName {
		t.Fatalf("path %q, want %q", p.Path, wantName)
	}
	// No title field at all, not an empty one.
	file := readDisk(t, s, p.Path)
	if strings.Contains(file, "title") || strings.Contains(file, "tags") || strings.Contains(file, "subtitle") {
		t.Fatalf("empty fields written:\n%s", file)
	}
	r, _ := s.List(index.Query{})
	it := r.Items[0]
	if !it.Untitled || it.Title != "" || it.Label != "只有一句话，没有标题。" {
		t.Fatalf("%+v", it)
	}
	// Created with nothing at all.
	e := mustCreate(t, s, "", nil, "")
	if e.Body != "" || !strings.HasPrefix(e.Path, today) {
		t.Fatalf("%+v", e)
	}
	// Adding a title renames the file to it; the id and the links to it stay.
	m := p.Meta.Clone()
	m = append(m, page.Field{Key: "title", Value: "有了标题"})
	q, err := s.Save(Author{}, p.ID, p.Rev, m, p.Body)
	if err != nil || q.Path != "有了标题.md" || q.ID != p.ID || exists(s, p.Path) {
		t.Fatalf("%v %+v", err, q)
	}
	// Removing it again goes back to the date-and-id name.
	m2 := page.Meta{{Key: "id", Value: p.ID}}
	q2, err := s.Save(Author{}, p.ID, q.Rev, m2, p.Body)
	if err != nil || q2.Path != wantName {
		t.Fatalf("%v %+v", err, q2)
	}
	if q2.Meta.Created() != today {
		t.Fatalf("created lost when the client left it out: %v", q2.Meta)
	}
}

func TestNamingAndCollisions(t *testing.T) {
	s := open(t)
	var paths []string
	for i := 0; i < 3; i++ {
		paths = append(paths, mustCreate(t, s, "a", meta("title", "同名"), "x").Path)
	}
	if strings.Join(paths, "|") != "a/同名.md|a/同名 2.md|a/同名 3.md" {
		t.Fatalf("%v", paths)
	}
	// Case-insensitive, as on macOS and Windows.
	mustCreate(t, s, "a", meta("title", "Hello"), "")
	if p := mustCreate(t, s, "a", meta("title", "HELLO"), ""); p.Path != "a/HELLO 2.md" {
		t.Fatalf("%q", p.Path)
	}
	// Illegal characters are dropped, leading dots too.
	if p := mustCreate(t, s, "", meta("title", `.../a:b*c?"d<e>f|g\h`), ""); p.Path != "abcdefgh.md" {
		t.Fatalf("%q", p.Path)
	}
	// A title that is only illegal characters falls back to the untitled name.
	if p := mustCreate(t, s, "", meta("title", `???`), ""); !strings.HasPrefix(p.Path, time.Now().UTC().Format("2006-01-02")) {
		t.Fatalf("%q", p.Path)
	}
	long := mustCreate(t, s, "", meta("title", strings.Repeat("长", 120)), "")
	if got := []rune(strings.TrimSuffix(long.Path, ".md")); len(got) != 66 && len(got) != 80 { // 80 runes, or the byte cap
		t.Fatalf("%d runes", len(got))
	}
	// The title is kept whole in the header even when the file name is cut.
	if long.Meta.Title() != strings.Repeat("长", 120) {
		t.Fatal("title truncated in the header")
	}
	// Another page is saved with a title that collides: gets a number.
	a := mustCreate(t, s, "b", meta("title", "甲"), "")
	b := mustCreate(t, s, "b", meta("title", "乙"), "")
	got, err := s.Save(Author{}, b.ID, b.Rev, meta("id", b.ID, "title", "甲"), "")
	if err != nil || got.Path != "b/甲 2.md" || !exists(s, a.Path) {
		t.Fatalf("%v %+v", err, got)
	}
	// Saving again with the same title does not rename it once more.
	again, err := s.Save(Author{}, b.ID, got.Rev, meta("id", b.ID, "title", "甲", "extra", "x"), "")
	if err != nil || again.Path != "b/甲 2.md" {
		t.Fatalf("%v %+v", err, again)
	}
	// Case-only change of its own name is allowed.
	c := mustCreate(t, s, "", meta("title", "case"), "")
	c2, err := s.Save(Author{}, c.ID, c.Rev, meta("title", "CASE"), "")
	if err != nil || c2.Path != "CASE.md" {
		t.Fatalf("%v %+v", err, c2)
	}
}

func TestSaveRenamesButKeepsID(t *testing.T) {
	s := open(t)
	p := mustCreate(t, s, "Tang", meta("title", "旧名", "series", "冬季"), "正文\n")
	n, err := s.Save(Author{}, p.ID, p.Rev, meta("id", p.ID, "title", "新名", "series", "冬季"), "正文改\n")
	if err != nil {
		t.Fatal(err)
	}
	if n.ID != p.ID || n.Path != "Tang/新名.md" || n.Rev == p.Rev || exists(s, p.Path) || !exists(s, n.Path) {
		t.Fatalf("%+v", n)
	}
	g, err := s.Get(p.ID)
	if err != nil || g.Path != n.Path || g.Body != "正文改\n" {
		t.Fatalf("%v %+v", err, g)
	}
	r, _ := s.List(index.Query{})
	if len(r.Items) != 1 || r.Items[0].Path != n.Path || r.Items[0].Title != "新名" {
		t.Fatalf("index after rename: %+v", r.Items)
	}
}

func TestSaveKeepsUnknownFieldsInOrder(t *testing.T) {
	s := open(t)
	p := mustCreate(t, s, "", meta("title", "T"), "b")
	m := meta("zeta", int64(1), "id", p.ID, "title", "T", "alpha", page.Meta{{Key: "k", Value: "v"}}, "created", p.Meta.Created(), "status", "定稿")
	n, err := s.Save(Author{}, p.ID, p.Rev, m, "b")
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, f := range n.Meta {
		keys = append(keys, f.Key)
	}
	if strings.Join(keys, ",") != "id,title,created,zeta,alpha,status" {
		t.Fatalf("%v", keys)
	}
	// A save that changes only the body leaves every header byte alone.
	hand := "---\n# my note\nid: " + p.ID + "\ntitle:   \"T\"\nseries: 冬季\n---\nold\n"
	if err := os.WriteFile(filepath.Join(s.Root(), n.Path), []byte(hand), 0o644); err != nil {
		t.Fatal(err)
	}
	cur, _ := s.Reindex()
	_ = cur
	g, _ := s.Get(p.ID)
	out, err := s.Save(Author{}, p.ID, g.Rev, g.Meta, "new\n")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := readDisk(t, s, out.Path), strings.Replace(hand, "old\n", "new\n", 1); got != want {
		t.Fatalf("header reformatted:\n%s\nwant\n%s", got, want)
	}
}

func TestStaleBaseRevConflicts(t *testing.T) {
	s := open(t)
	p := mustCreate(t, s, "", meta("title", "T"), "v1\n")
	if _, err := s.Save(Author{}, p.ID, p.Rev, p.Meta, "v2\n"); err != nil {
		t.Fatal(err)
	}
	before := readDisk(t, s, "T.md")
	_, err := s.Save(Author{}, p.ID, p.Rev, p.Meta, "mine\n") // p.Rev is stale now
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("want a conflict, got %v", err)
	}
	if ce.Current.Body != "v2\n" || ce.Current.Rev != page.Rev([]byte(before)) || ce.Current.ID != p.ID {
		t.Fatalf("current: %+v", ce.Current)
	}
	if readDisk(t, s, "T.md") != before {
		t.Fatal("a refused save changed the file")
	}
	// A missing baseRev is not the current rev either.
	if _, err := s.Save(Author{}, p.ID, "", p.Meta, "x"); !errors.As(err, &ce) {
		t.Fatalf("empty baseRev: %v", err)
	}
	// A change made outside the server is noticed the same way.
	if err := os.WriteFile(filepath.Join(s.Root(), "T.md"), []byte("---\nid: "+p.ID+"\n---\nedited elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(Author{}, p.ID, ce.Current.Rev, p.Meta, "x"); !errors.As(err, &ce) || ce.Current.Body != "edited elsewhere\n" {
		t.Fatalf("external edit: %v", err)
	}
}

func TestSaveValidation(t *testing.T) {
	s := open(t)
	p := mustCreate(t, s, "", meta("title", "T"), "b")
	var inv *InvalidError
	if _, err := s.Save(Author{}, p.ID, p.Rev, meta("id", "ZZZZZZZZZZ"), "b"); !errors.As(err, &inv) {
		t.Fatalf("id mismatch: %v", err)
	}
	if _, err := s.Save(Author{}, p.ID, p.Rev, nil, "bad \xff utf8"); !errors.As(err, &inv) {
		t.Fatalf("invalid utf-8: %v", err)
	}
	if _, err := s.Create(Author{}, CreateInput{Body: "bad \xff"}); !errors.As(err, &inv) {
		t.Fatalf("create invalid utf-8: %v", err)
	}
	if _, err := s.Save(Author{}, "0000000000", "x", nil, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown page: %v", err)
	}
	// An unchanged save is not a write.
	st1, _ := os.Stat(filepath.Join(s.Root(), p.Path))
	time.Sleep(10 * time.Millisecond)
	same, err := s.Save(Author{}, p.ID, p.Rev, p.Meta, p.Body)
	st2, _ := os.Stat(filepath.Join(s.Root(), p.Path))
	if err != nil || same.Rev != p.Rev || !st1.ModTime().Equal(st2.ModTime()) {
		t.Fatalf("no-op save wrote the file: %v", err)
	}
}

func TestMoveAndDelete(t *testing.T) {
	s := open(t)
	a := mustCreate(t, s, "x", meta("title", "A"), "")
	mustCreate(t, s, "y/z", meta("title", "A"), "") // the name is taken there
	m, err := s.Move(Author{}, a.ID, "y/z")
	if err != nil || m.Path != "y/z/A 2.md" || m.Folder != "y/z" || exists(s, a.Path) {
		t.Fatalf("%v %+v", err, m)
	}
	if m.ID != a.ID || m.Rev != a.Rev { // moving does not change the bytes
		t.Fatalf("%+v", m)
	}
	n, err := s.Move(Author{}, a.ID, "")
	if err != nil || n.Path != "A.md" {
		t.Fatalf("%v %+v", err, n)
	}
	same, err := s.Move(Author{}, a.ID, "")
	if err != nil || same.Path != "A.md" {
		t.Fatalf("%v %+v", err, same)
	}
	tree, _ := s.Tree()
	want := map[string]int{"": 1, "x": 0, "y": 0, "y/z": 1}
	for _, f := range tree {
		if want[f.Path] != f.Pages {
			t.Errorf("folder %q has %d, want %d", f.Path, f.Pages, want[f.Path])
		}
		delete(want, f.Path)
	}
	if len(want) != 0 {
		t.Fatalf("missing folders %v", want)
	}
	if _, err := s.Move(Author{}, "0000000000", "q"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}

	if err := s.Delete(Author{}, a.ID); err != nil {
		t.Fatal(err)
	}
	if exists(s, "A.md") {
		t.Fatal("file not removed")
	}
	if _, err := s.Get(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if err := s.Delete(Author{}, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if r, _ := s.List(index.Query{Text: "A"}); len(r.Items) != 1 { // the other "A" page
		t.Fatalf("%+v", r.Items)
	}
}

func TestFolders(t *testing.T) {
	s := open(t)
	if _, err := s.CreateFolder(Author{}, "随笔/2027"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateFolder(Author{}, "随笔/2027"); !errors.Is(err, ErrExists) {
		t.Fatalf("%v", err)
	}
	if _, err := s.CreateFolder(Author{}, ""); !errors.Is(err, ErrExists) {
		t.Fatalf("root: %v", err)
	}
	tree, _ := s.Tree()
	if len(tree) != 3 || tree[1].Path != "随笔" || tree[2].Path != "随笔/2027" || tree[2].Pages != 0 {
		t.Fatalf("%+v", tree)
	}
	// The marker file that keeps the folder in Git is not a page.
	if r, _ := s.List(index.Query{}); len(r.Items) != 0 {
		t.Fatalf("%+v", r.Items)
	}
	// A folder made by hand shows up after the next sync, with dot-directories,
	// .git, .petalbook and the root assets folder left out.
	for _, d := range []string{"手工/内", ".hidden", "assets/01JD", "deep/assets"} {
		os.MkdirAll(filepath.Join(s.Root(), d), 0o755)
	}
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	var got []string
	tree, _ = s.Tree()
	for _, f := range tree {
		got = append(got, f.Path)
	}
	if strings.Join(got, "|") != "|deep|deep/assets|手工|手工/内|随笔|随笔/2027" {
		t.Fatalf("%q", strings.Join(got, "|"))
	}
}

func TestPathTraversalIsRejected(t *testing.T) {
	s := open(t)
	p := mustCreate(t, s, "", meta("title", "T"), "")
	outside := t.TempDir()
	bad := []string{"..", "../x", "a/../../x", "a/..", "/abs", "/", "a//b", "a/", "./a", "a/./b", ".git", ".petalbook/x", ".hidden", "assets", "assets/x", `a\..\b`, "a/b\x00c", "C:/x", "a/b?", "x ", "..."}
	for _, f := range bad {
		var inv *InvalidError
		if _, err := s.Create(Author{}, CreateInput{Folder: f}); !errors.As(err, &inv) {
			t.Errorf("Create(%q): %v", f, err)
		}
		if _, err := s.Move(Author{}, p.ID, f); !errors.As(err, &inv) {
			t.Errorf("Move(%q): %v", f, err)
		}
		if _, err := s.CreateFolder(Author{}, f); !errors.As(err, &inv) {
			t.Errorf("CreateFolder(%q): %v", f, err)
		}
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("something was written outside the space: %v", entries)
	}
	if exists(s, "../x") {
		t.Fatal("escaped")
	}

	// Symbolic links: a folder link that points outside is never followed.
	if err := os.Symlink(outside, filepath.Join(s.Root(), "link")); err != nil {
		t.Skip("symlinks not available")
	}
	var inv *InvalidError
	if _, err := s.Create(Author{}, CreateInput{Folder: "link"}); !errors.As(err, &inv) {
		t.Errorf("create through link: %v", err)
	}
	if _, err := s.Create(Author{}, CreateInput{Folder: "link/sub"}); !errors.As(err, &inv) {
		t.Errorf("create through link/sub: %v", err)
	}
	if _, err := s.Move(Author{}, p.ID, "link"); !errors.As(err, &inv) {
		t.Errorf("move through link: %v", err)
	}
	if _, err := s.CreateFolder(Author{}, "link/new"); !errors.As(err, &inv) {
		t.Errorf("folder through link: %v", err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("something was written outside the space through a link: %v", entries)
	}
	// Files outside, linked in as pages or folders, are not indexed.
	os.WriteFile(filepath.Join(outside, "secret.md"), []byte("---\nid: SECRETSECR\n---\nsecret\n"), 0o644)
	os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(s.Root(), "evil.md"))
	if _, err := s.Reindex(); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.List(index.Query{Text: "secret"}); len(r.Items) != 0 {
		t.Fatalf("a linked file was indexed: %+v", r.Items)
	}
	if _, err := s.Get("SECRETSECR"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	// A page file swapped for a link after it was indexed is not read.
	q := mustCreate(t, s, "", meta("title", "Swap"), "x")
	os.Remove(filepath.Join(s.Root(), q.Path))
	os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(s.Root(), q.Path))
	if _, err := s.Get(q.ID); err == nil {
		t.Fatal("read through a swapped link")
	}
}

func TestExternalEditsAreSeenAtStartup(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s := openAt(t, root)
	a := mustCreate(t, s, "", meta("title", "A", "tags", []any{"x"}), "栀子\n")
	b := mustCreate(t, s, "", meta("title", "B"), "")
	s.Close()

	// While the server is off: edit A, delete B, add C and a file without a header.
	os.WriteFile(filepath.Join(root, a.Path), []byte("---\nid: "+a.ID+"\ntitle: A edited\ntags: [y]\n---\n黄昏\n"), 0o644)
	os.Remove(filepath.Join(root, b.Path))
	os.WriteFile(filepath.Join(root, "C.md"), []byte("---\nid: CCCCCCCCCC\ntitle: C\n---\n"), 0o644)
	os.WriteFile(filepath.Join(root, "plain.md"), []byte("no header\n"), 0o644)
	os.WriteFile(filepath.Join(root, "notes.txt"), []byte("not a page\n"), 0o644)

	s2 := openAt(t, root)
	r, _ := s2.List(index.Query{Sort: "title"})
	var titles []string
	for _, it := range r.Items {
		titles = append(titles, it.Title)
	}
	if strings.Join(titles, "|") != "A edited|C" {
		t.Fatalf("%v", titles)
	}
	if r, _ := s2.List(index.Query{Text: "黄昏"}); len(r.Items) != 1 {
		t.Fatalf("new text not searchable: %+v", r.Items)
	}
	if r, _ := s2.List(index.Query{Text: "栀子"}); len(r.Items) != 0 {
		t.Fatalf("old text still searchable")
	}
	if tags, _ := s2.Tags(); len(tags) != 1 || tags[0].Tag != "y" {
		t.Fatalf("%+v", tags)
	}
	probs, _ := s2.Problems()
	if len(probs) != 1 || probs[0].Path != "plain.md" || probs[0].Code != "no_header" {
		t.Fatalf("problems: %+v", probs)
	}
	if _, err := s2.Get(b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	// A file that disappears behind the server's back is dropped on first touch.
	os.Remove(filepath.Join(root, "C.md"))
	if _, err := s2.Get("CCCCCCCCCC"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if n, _ := s2.List(index.Query{}); len(n.Items) != 1 {
		t.Fatalf("stale entry kept: %+v", n.Items)
	}
}

func TestRevMatchesGitAndKeepsCRLFFilesReadable(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	raw := "---\r\nid: CRFCRF0001\r\ntitle: 行尾\r\n---\r\n第一行\r\n第二行\r\n"
	os.WriteFile(filepath.Join(root, "x.md"), []byte(raw), 0o644)
	s := openAt(t, root)
	g, err := s.Get("CRFCRF0001")
	if err != nil {
		t.Fatal(err)
	}
	if want := git(t, root, "hash-object", "x.md"); g.Rev != want {
		t.Fatalf("rev %s, git says %s", g.Rev, want)
	}
	if g.Body != "第一行\n第二行\n" || g.Meta.Title() != "行尾" {
		t.Fatalf("%+v", g)
	}
	// Saving normalises the line ends and gives the new blob id.
	n, err := s.Save(Author{}, g.ID, g.Rev, g.Meta, g.Body+"三\n")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readDisk(t, s, "x.md"), "\r") {
		t.Fatal("CRLF kept")
	}
	if want := git(t, root, "hash-object", "x.md"); n.Rev != want {
		t.Fatalf("rev %s, git says %s", n.Rev, want)
	}
}

func TestWritesLeaveNothingBehind(t *testing.T) {
	s := open(t)
	for i := 0; i < 5; i++ {
		p := mustCreate(t, s, "f", meta("title", fmt.Sprint("t", i)), "x")
		if _, err := s.Save(Author{}, p.ID, p.Rev, meta("title", fmt.Sprint("u", i)), "y"); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(s.Root(), ".petalbook", "tmp"))
	if err == nil && len(entries) != 0 {
		t.Fatalf("temporary files left: %v", entries)
	}
	if es, _ := os.ReadDir(filepath.Join(s.Root(), "f")); len(es) != 5 {
		t.Fatalf("%d files in f", len(es))
	}
}

func TestParseCommitter(t *testing.T) {
	n, e, err := ParseCommitter("")
	if err != nil || n != "PetalBook" || e != "petalbook@localhost" {
		t.Fatalf("%q %q %v", n, e, err)
	}
	n, e, err = ParseCommitter("Ann Lee <ann@example.com>")
	if err != nil || n != "Ann Lee" || e != "ann@example.com" {
		t.Fatalf("%q %q %v", n, e, err)
	}
	for _, bad := range []string{"no email", "<a@b>", "Name <>", "Name <a@b> trailing", "Name >a@b<"} {
		if _, _, err := ParseCommitter(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestConcurrentWritersAndReaders(t *testing.T) {
	s := open(t)
	base := mustCreate(t, s, "", meta("title", "共享"), "v0\n")

	// Ten clients save from the same base revision: exactly one wins, the
	// others get a conflict, and the file ends up as the winner wrote it.
	var wg sync.WaitGroup
	var ok, conflicts atomic.Int32
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Save(Author{}, base.ID, base.Rev, meta("title", "共享"), fmt.Sprintf("writer %d\n", i))
			var ce *ConflictError
			switch {
			case err == nil:
				ok.Add(1)
			case errors.As(err, &ce):
				conflicts.Add(1)
			default:
				t.Errorf("save: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 1 || conflicts.Load() != 9 {
		t.Fatalf("%d saves won, %d conflicts", ok.Load(), conflicts.Load())
	}

	// Creates, renames, moves and listings at once.
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 8; i++ {
				p, err := s.Create(Author{}, CreateInput{Folder: fmt.Sprint("f", w%2), Meta: meta("title", "同名", "tags", []any{"x"}), Body: "栀子\n"})
				if err != nil {
					t.Errorf("create: %v", err)
					return
				}
				if p, err = s.Save(Author{}, p.ID, p.Rev, meta("title", fmt.Sprint("改", w, i)), "改\n"); err != nil {
					t.Errorf("save: %v", err)
					return
				}
				if _, err := s.Move(Author{}, p.ID, "g"); err != nil {
					t.Errorf("move: %v", err)
					return
				}
				if _, err := s.List(index.Query{Text: "栀子", Limit: 5}); err != nil {
					t.Errorf("list: %v", err)
				}
				if _, err := s.Get(p.ID); err != nil {
					t.Errorf("get: %v", err)
				}
			}
		}()
	}
	wg.Wait()
	r, _ := s.List(index.Query{Limit: 200})
	if len(r.Items) != 6*8+1 {
		t.Fatalf("%d pages", len(r.Items))
	}
	// The index agrees with a rebuild from the files.
	st, err := s.Reindex()
	if err != nil || st.Pages != 6*8+1 {
		t.Fatalf("%v %+v", err, st)
	}
	r2, _ := s.List(index.Query{Limit: 200})
	if fmt.Sprint(r.Items) != fmt.Sprint(r2.Items) {
		t.Fatal("index differs from a rebuild after concurrent writes")
	}
}
