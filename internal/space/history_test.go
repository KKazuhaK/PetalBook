package space

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func flushed(t *testing.T, s *Space) {
	t.Helper()
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryFollowsAPageThroughRenamesAndMoves(t *testing.T) {
	t.Parallel()
	s := localSpace(t)
	ctx := context.Background()
	p := mustCreate(t, s, "Tang", meta("title", "暮雪"), "v1\n")
	flushed(t, s)
	p2, err := s.Save(Author{}, p.ID, p.Rev, meta("title", "暮雪"), "v2\n")
	if err != nil {
		t.Fatal(err)
	}
	flushed(t, s)
	p3, err := s.Save(Author{}, p.ID, p2.Rev, meta("title", "夜雪"), "v2\n") // rename: path and header change
	if err != nil {
		t.Fatal(err)
	}
	flushed(t, s)
	p4, err := s.Move(Author{}, p.ID, "随笔")
	if err != nil {
		t.Fatal(err)
	}
	flushed(t, s)
	p5, err := s.Save(Author{}, p.ID, p4.Rev, meta("title", "夜雪"), "v3\n")
	if err != nil {
		t.Fatal(err)
	}
	flushed(t, s)
	// Another page is made over an old path, and has its own history.
	other := mustCreate(t, s, "Tang", meta("title", "暮雪"), "other\n")
	flushed(t, s)
	if other.Path != "Tang/暮雪.md" {
		t.Fatalf("the old name was not free: %s", other.Path)
	}
	_ = p3

	items, next, err := s.History(ctx, p.ID, 30, "")
	if err != nil || next != "" {
		t.Fatalf("%v %q", err, next)
	}
	var got []string
	for _, it := range items {
		got = append(got, it.Change)
	}
	if strings.Join(got, ",") != "edit,move,edit,edit,add" {
		t.Fatalf("changes %v", got)
	}
	if items[0].Path != "随笔/夜雪.md" || items[0].Rev != p5.Rev || items[0].Author != "Ann" || items[0].Time.IsZero() {
		t.Fatalf("%+v", items[0])
	}
	if items[1].Path != "随笔/夜雪.md" || items[2].Path != "Tang/夜雪.md" || items[4].Path != "Tang/暮雪.md" {
		t.Fatalf("paths %+v", items)
	}
	// The other page, made at the first page's old path, shows only its own version.
	oi, _, err := s.History(ctx, other.ID, 30, "")
	if err != nil || len(oi) != 1 || oi[0].Change != "add" || oi[0].Path != "Tang/暮雪.md" {
		t.Fatalf("%+v %v", oi, err)
	}

	// Paging by cursor gives the same list, two at a time.
	var paged []HistoryItem
	cursor := ""
	for i := 0; i < 10; i++ {
		page, n, err := s.History(ctx, p.ID, 2, cursor)
		if err != nil {
			t.Fatal(err)
		}
		paged = append(paged, page...)
		if n == "" {
			break
		}
		cursor = n
	}
	if len(paged) != len(items) {
		t.Fatalf("paged %d, want %d", len(paged), len(items))
	}
	for i := range items {
		if paged[i] != items[i] {
			t.Fatalf("item %d: %+v vs %+v", i, paged[i], items[i])
		}
	}

	// Reading versions.
	for _, it := range items {
		v, err := s.Version(ctx, p.ID, it.Commit)
		if err != nil {
			t.Fatalf("%s: %v", it.Commit, err)
		}
		if v.Meta.ID() != p.ID || v.Rev != it.Rev || v.Path != it.Path || v.Commit != it.Commit || v.Author != "Ann" {
			t.Fatalf("%+v vs %+v", v, it)
		}
	}
	v, _ := s.Version(ctx, p.ID, items[3].Commit)
	if v.Body != "v2\n" || v.Meta.Title() != "暮雪" {
		t.Fatalf("%+v", v)
	}
	// An uncommitted edit is not in the history yet.
	if _, err := s.Save(Author{}, p.ID, p5.Rev, meta("title", "夜雪"), "v4\n"); err != nil {
		t.Fatal(err)
	}
	if items2, _, _ := s.History(ctx, p.ID, 30, ""); len(items2) != len(items) {
		t.Fatal("an uncommitted write is in the history")
	}
	flushed(t, s)
	if items2, _, _ := s.History(ctx, p.ID, 30, ""); len(items2) != len(items)+1 || items2[0].Change != "edit" {
		t.Fatalf("%+v", items2)
	}
}

func TestVersionsOfAPageNeverShowAnyOtherFile(t *testing.T) {
	t.Parallel()
	s := localSpace(t)
	ctx := context.Background()
	a := mustCreate(t, s, "", meta("title", "Alpha"), "alpha\n")
	flushed(t, s)
	b := mustCreate(t, s, "", meta("title", "Beta"), "beta secret\n")
	flushed(t, s)
	if err := os.WriteFile(filepath.Join(s.Root(), "notes.txt"), []byte("not a page, secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.cm.Touch(Author{}, "notes.txt")
	flushed(t, s)
	commitOfB := git(t, s.Root(), "log", "-1", "--format=%H", "--", b.Path)
	commitOfTxt := git(t, s.Root(), "log", "-1", "--format=%H", "--", "notes.txt")
	a2, err := s.Save(Author{}, a.ID, a.Rev, meta("title", "Alpha"), "alpha 2\n")
	if err != nil {
		t.Fatal(err)
	}
	flushed(t, s)
	_ = a2
	head := git(t, s.Root(), "rev-parse", "HEAD")
	aCommit := git(t, s.Root(), "log", "-1", "--format=%H", "--", a.Path)

	notAVersion := map[string]string{
		"another page's commit":             commitOfB,
		"a commit that only touched a text": commitOfTxt,
		"a branch name":                     "main",
		"HEAD":                              "HEAD",
		"a revision expression":             head + "~1",
		"a tree":                            head + "^{tree}",
		"a path at a commit":                head + ":" + b.Path,
		"an option":                         "--output=/tmp/x",
		"a short id":                        aCommit[:10],
		"upper case":                        strings.ToUpper(aCommit),
		"all zeros":                         strings.Repeat("0", 40),
		"too long":                          aCommit + "00",
		"empty":                             "",
		"a stash":                           "stash@{0}",
		"a path":                            "../../etc/passwd",
	}
	for what, c := range notAVersion {
		if v, err := s.Version(ctx, a.ID, c); !errors.Is(err, ErrNoVersion) {
			t.Errorf("%s: %v %v", what, v, err)
		}
	}
	// B's own commit is B's version, and only B's.
	if v, err := s.Version(ctx, b.ID, commitOfB); err != nil || !strings.Contains(v.Body, "beta secret") {
		t.Fatalf("%v %v", v, err)
	}
	// A page that does not exist has no history.
	if _, _, err := s.History(ctx, "0000000000", 10, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if _, err := s.Version(ctx, "0000000000", commitOfB); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	// Hostile cursors: bad forms are invalid, and a well-formed one that names
	// another page's file lists nothing of that file.
	for _, c := range []string{"!!!", "AAAA", encode("nothex", "a.md"), encode(head, "../x.md"), encode(head, "/etc/passwd"), encode(head, "notes.txt"), encode(head, "")} {
		var inv *InvalidError
		if _, _, err := s.History(ctx, a.ID, 10, c); !errors.As(err, &inv) {
			t.Errorf("cursor %q: %v", c, err)
		}
	}
	// A path that merely starts with a dash is an ordinary (empty) lookup, never an option.
	if items, _, err := s.History(ctx, a.ID, 10, encode(head, "-x.md")); err != nil || len(items) != 0 {
		t.Fatalf("%+v %v", items, err)
	}
	items, _, err := s.History(ctx, a.ID, 10, encode(head, b.Path))
	if err != nil || len(items) != 0 {
		t.Fatalf("a cursor on another page's path listed %+v (%v)", items, err)
	}
	items, _, err = s.History(ctx, a.ID, 10, encode(commitOfB, "nowhere.md"))
	if err != nil || len(items) != 0 {
		t.Fatalf("%+v %v", items, err)
	}
}

func encode(commit, path string) string {
	return encodeCursor(histCursor{Commit: commit, Path: path})
}

func TestAFileReplacedByAnotherPageUnderTheSamePath(t *testing.T) {
	t.Parallel()
	s := localSpace(t)
	ctx := context.Background()
	a := mustCreate(t, s, "", meta("title", "Alpha"), "alpha\n")
	flushed(t, s)
	firstA := git(t, s.Root(), "log", "-1", "--format=%H", "--", a.Path)
	// Outside the server, another page's content is put at A's path and committed.
	other := pageText("FAKER00001", "Alpha", "an impostor\n")
	if err := os.WriteFile(filepath.Join(s.Root(), a.Path), []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, s.Root(), "add", "-A")
	git(t, s.Root(), "-c", "user.name=X", "-c", "user.email=x@x", "commit", "-qm", "replace")
	impostor := git(t, s.Root(), "rev-parse", "HEAD")
	if _, err := s.Get(a.ID); !errors.Is(err, ErrNotFound) { // the file is not A any more
		t.Fatalf("A should be gone: %v", err)
	}
	// B, a real page now at that path, cannot read A's old version through its own id, nor the reverse.
	s.Reindex()
	b, err := s.Get("FAKER00001")
	if err != nil {
		t.Fatal(err)
	}
	items, _, err := s.History(ctx, b.ID, 10, "")
	if err != nil || len(items) != 1 || items[0].Commit != impostor {
		t.Fatalf("B's history: %+v %v", items, err)
	}
	if _, err := s.Version(ctx, b.ID, firstA); !errors.Is(err, ErrNoVersion) {
		t.Fatalf("B read A's version: %v", err)
	}
	// Restore A's content at a new path; its history must not include the impostor's commit.
	if err := os.WriteFile(filepath.Join(s.Root(), "Alpha restored.md"), []byte(pageText(a.ID, "Alpha", "alpha\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, s.Root(), "add", "-A")
	git(t, s.Root(), "-c", "user.name=X", "-c", "user.email=x@x", "commit", "-qm", "restore")
	s.Reindex()
	ai, _, err := s.History(ctx, a.ID, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range ai {
		if it.Commit == impostor {
			t.Fatal("the impostor's commit is in A's history")
		}
		if v, err := s.Version(ctx, a.ID, it.Commit); err != nil || v.Meta.ID() != a.ID {
			t.Fatalf("%v %v", v, err)
		}
	}
	if _, err := s.Version(ctx, a.ID, impostor); !errors.Is(err, ErrNoVersion) {
		t.Fatalf("A read the impostor's version: %v", err)
	}
}

func TestHistoryOfAPageThatIsNotCommittedYetIsEmpty(t *testing.T) {
	t.Parallel()
	s := localSpace(t)
	p := mustCreate(t, s, "", meta("title", "New"), "x\n")
	// Nothing is committed in this space yet (the committer is quiet).
	items, next, err := s.History(context.Background(), p.ID, 10, "")
	if err != nil || len(items) != 0 || next != "" {
		t.Fatalf("%+v %q %v", items, next, err)
	}
	flushed(t, s)
	if items, _, _ = s.History(context.Background(), p.ID, 10, ""); len(items) != 1 || items[0].Change != "add" {
		t.Fatalf("%+v", items)
	}
}

func TestHistoryPagingDoesNotWalkFromTheTop(t *testing.T) {
	t.Parallel()
	s := localSpace(t)
	ctx := context.Background()
	p := mustCreate(t, s, "", meta("title", "Many"), "0\n")
	flushed(t, s)
	rev := p.Rev
	for i := 1; i <= 12; i++ {
		n, err := s.Save(Author{}, p.ID, rev, meta("title", "Many"), fmt.Sprintf("%d\n", i))
		if err != nil {
			t.Fatal(err)
		}
		rev = n.Rev
		flushed(t, s)
	}
	first, cursor, err := s.History(ctx, p.ID, 5, "")
	if err != nil || len(first) != 5 || cursor == "" {
		t.Fatalf("%v %q %v", len(first), cursor, err)
	}
	// The cursor is a commit and a path: the next page starts there.
	c, err := decodeCursor(cursor)
	if err != nil || c.Commit != first[4].Commit {
		t.Fatalf("%+v %v", c, err)
	}
	var seen []string
	for _, it := range first {
		seen = append(seen, it.Commit)
	}
	rest, cursor2, err := s.History(ctx, p.ID, 100, cursor)
	if err != nil || cursor2 != "" || len(rest) != 13-5 {
		t.Fatalf("%d %q %v", len(rest), cursor2, err)
	}
	for _, it := range rest {
		for _, c := range seen {
			if it.Commit == c {
				t.Fatal("a version came twice")
			}
		}
	}
}

// Changing the title renames the file. When the text changes a lot in the same
// commit Git sees an unrelated new file, and its own rename following stops
// there. The page's id is in the file, so the history goes on.
func TestHistoryFollowsARenameThatRewroteTheText(t *testing.T) {
	t.Parallel()
	s := localSpace(t)
	ctx := context.Background()
	long := strings.Repeat("the first text of this page, which will be replaced entirely. ", 20) + "\n"
	p := mustCreate(t, s, "", meta("title", "雨停之后"), long)
	flushed(t, s)
	p2, err := s.Save(Author{}, p.ID, p.Rev, meta("title", "雨停之后"), long+"one more line\n")
	if err != nil {
		t.Fatal(err)
	}
	flushed(t, s)
	// New title and a completely different body, in one commit.
	p3, err := s.Save(Author{}, p.ID, p2.Rev, meta("title", "巷口"), "卖豆浆的车，一只不怕人的猫。\n")
	if err != nil {
		t.Fatal(err)
	}
	flushed(t, s)
	if p3.Path == p2.Path {
		t.Fatal("the title change did not rename the file")
	}
	// A second rename-with-rewrite, so two hops are needed.
	p4, err := s.Save(Author{}, p.ID, p3.Rev, meta("title", "书店"), strings.Repeat("完全不同的另一段文字。", 30)+"\n")
	if err != nil {
		t.Fatal(err)
	}
	flushed(t, s)

	items, next, err := s.History(ctx, p.ID, 30, "")
	if err != nil || next != "" {
		t.Fatalf("%v %q", err, next)
	}
	var got []string
	for _, it := range items {
		got = append(got, it.Change+" "+it.Path)
	}
	want := []string{"edit 书店.md", "edit 巷口.md", "edit 雨停之后.md", "add 雨停之后.md"}
	if strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Fatalf("history\n got %v\nwant %v", got, want)
	}
	if items[0].Rev != p4.Rev || items[3].Rev != p.Rev {
		t.Fatalf("revs %+v", items)
	}
	// Every one of them can be read, the old names included.
	for _, it := range items {
		v, err := s.Version(ctx, p.ID, it.Commit)
		if err != nil || v.Rev != it.Rev || v.Path != it.Path {
			t.Fatalf("version %s: %+v %v", it.Commit, v, err)
		}
	}
	// Paging across the hops gives the same list, whatever the page size.
	for size := 1; size <= 4; size++ {
		var paged []string
		cur := ""
		for i := 0; i < 10; i++ {
			its, n, err := s.History(ctx, p.ID, size, cur)
			if err != nil {
				t.Fatal(err)
			}
			for _, it := range its {
				paged = append(paged, it.Change+" "+it.Path)
			}
			if n == "" {
				break
			}
			cur = n
		}
		if strings.Join(paged, " | ") != strings.Join(want, " | ") {
			t.Fatalf("page size %d\n got %v\nwant %v", size, paged, want)
		}
	}
	// Another page deleted in the same commit is never taken for the earlier name.
	other := mustCreate(t, s, "", meta("title", "别的页"), "x\n")
	flushed(t, s)
	if err := s.Delete(Author{}, other.ID); err != nil {
		t.Fatal(err)
	}
	p5, err := s.Save(Author{}, p.ID, p4.Rev, meta("title", "又改名"), strings.Repeat("第三次全部重写。", 30)+"\n")
	if err != nil {
		t.Fatal(err)
	}
	flushed(t, s) // one commit: 别的页 deleted, 书店 -> 又改名
	items, _, err = s.History(ctx, p.ID, 30, "")
	if err != nil || len(items) != 5 || items[0].Rev != p5.Rev || items[1].Path != "书店.md" {
		t.Fatalf("%+v %v", items, err)
	}
}
