package gitstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// idOf is a stand-in for the page header: the id is the first line.
func idOf(head []byte) string {
	l, _, _ := strings.Cut(string(head), "\n")
	return strings.TrimPrefix(l, "id: ")
}

func accept(id string) func(string, []byte) bool {
	return func(_ string, head []byte) bool { return idOf(head) == id }
}

func TestFileHistoryFollowsRenamesAndVerifiesTheID(t *testing.T) {
	t.Parallel()
	needGit(t)
	ctx := context.Background()
	r := newClone(t, bareRemote(t), nil)
	step := func(msgText string, f func(), paths ...string) {
		t.Helper()
		f()
		commit(t, r, paths...)
	}
	body := strings.Repeat("a line of the body\n", 30)
	step("add", func() { write(t, r.Dir(), "a.md", "id: AAAA\n"+body) }, "a.md")
	step("edit", func() { write(t, r.Dir(), "a.md", "id: AAAA\n"+body+"more\n") }, "a.md")
	step("move", func() {
		os.MkdirAll(filepath.Join(r.Dir(), "d"), 0o755)
		os.Rename(filepath.Join(r.Dir(), "a.md"), filepath.Join(r.Dir(), "d/a.md"))
	}, "a.md", "d/a.md")
	step("edit again", func() { write(t, r.Dir(), "d/a.md", "id: AAAA\n"+body+"more\nand more\n") }, "d/a.md")
	// Another page is created over the old path: it must not show up in AAAA's history.
	step("other page at the old path", func() { write(t, r.Dir(), "a.md", "id: BBBB\nother\n") }, "a.md")
	step("edit the other", func() { write(t, r.Dir(), "a.md", "id: BBBB\nother again\n") }, "a.md")
	// A different page's file is replaced by AAAA's path: a deletion of BBBB is not AAAA's either.
	step("delete the other", func() { os.Remove(filepath.Join(r.Dir(), "a.md")) }, "a.md")
	head := r.Head(ctx)

	vs, next, err := r.FileHistory(ctx, HistoryQuery{Start: "HEAD", Path: "d/a.md", Limit: 10, Accept: accept("AAAA")})
	if err != nil || next != nil {
		t.Fatalf("%v %v", err, next)
	}
	var got []string
	for _, v := range vs {
		got = append(got, fmt.Sprintf("%c:%s:%d", v.Status, v.Path, v.Similarity))
	}
	want := []string{"M:d/a.md:0", "R:d/a.md:100", "M:a.md:0", "A:a.md:0"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("got %v want %v", got, want)
	}
	if vs[0].Rev == "" || vs[0].Author != "Ann" || vs[0].Time.IsZero() {
		t.Fatalf("%+v", vs[0])
	}
	// The rev is the blob id of the file in that version.
	if blob := sh(t, r.Dir(), "rev-parse", vs[1].Commit+":d/a.md"); vs[1].Rev != blob {
		t.Fatalf("rev %s vs %s", vs[1].Rev, blob)
	}

	// Paging: one at a time, each page starting from the cursor.
	var paged []string
	var cur *Cursor
	startCommit, path := "HEAD", "d/a.md"
	skip := ""
	for i := 0; i < 10; i++ {
		if cur != nil {
			startCommit, path, skip = cur.Commit, cur.Path, cur.Commit
		}
		page, n, err := r.FileHistory(ctx, HistoryQuery{Start: startCommit, Path: path, Limit: 1, Accept: accept("AAAA"), SkipFirst: skip})
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range page {
			paged = append(paged, v.Commit)
		}
		if n == nil {
			break
		}
		cur = n
	}
	if len(paged) != 4 {
		t.Fatalf("paged %d: %v", len(paged), paged)
	}
	for i := range paged {
		if paged[i] != vs[i].Commit {
			t.Fatalf("page %d: %s vs %s", i, paged[i], vs[i].Commit)
		}
	}

	// The history of the other page at the old path, and of the deletion.
	vs, _, err = r.FileHistory(ctx, HistoryQuery{Start: "HEAD", Path: "a.md", Limit: 10, Accept: accept("BBBB")})
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	for _, v := range vs {
		got = append(got, string(v.Status))
	}
	if strings.Join(got, "") != "DMA" {
		t.Fatalf("BBBB: %v", got)
	}
	if vs[0].Rev != "" {
		t.Fatal("a deletion has no rev")
	}

	// Reading one version in full.
	data, oid, err := r.ReadBlob(ctx, head, "d/a.md")
	if err != nil || !strings.HasPrefix(string(data), "id: AAAA\n") || oid == "" {
		t.Fatalf("%q %v", data, err)
	}
	if _, _, err := r.ReadBlob(ctx, head, "nope.md"); err != ErrNoSuchFile {
		t.Fatalf("%v", err)
	}
	// Hostile input is a plain error, never another file.
	for _, bad := range []string{"HEAD", "main", head + ":x", "--output=x", head + "^{tree}"} {
		if _, _, err := r.ReadBlob(ctx, bad, "d/a.md"); err == nil {
			t.Errorf("commit %q accepted", bad)
		}
	}
	if _, _, err := r.FileHistory(ctx, HistoryQuery{Start: "--output=x", Path: "a.md"}); err == nil {
		t.Error("a start that is an option was accepted")
	}
	// A path that looks like an option is only a path: it is after "--".
	if vs, _, err := r.FileHistory(ctx, HistoryQuery{Start: "HEAD", Path: "--output=" + filepath.Join(r.Dir(), "leak"), Limit: 5}); err != nil || len(vs) != 0 {
		t.Errorf("%v %v", vs, err)
	}
	if _, err := os.Stat(filepath.Join(r.Dir(), "leak")); err == nil {
		t.Error("an option in a path wrote a file")
	}
}

func TestFileHistoryKeepsLookingWhenVersionsAreRefused(t *testing.T) {
	t.Parallel()
	needGit(t)
	ctx := context.Background()
	r := newClone(t, bareRemote(t), nil)
	// 40 versions of a path, all of another page, then 2 of ours at the top.
	for i := 0; i < 40; i++ {
		write(t, r.Dir(), "a.md", fmt.Sprintf("id: OLD\nv%d\n", i))
		commit(t, r, "a.md")
	}
	write(t, r.Dir(), "a.md", "id: OURS\nv1\n")
	commit(t, r, "a.md")
	// A cursor into a history that has nothing of ours returns an empty page, not an error.
	vs, next, err := r.FileHistory(ctx, HistoryQuery{Start: "HEAD", Path: "a.md", Limit: 5, Accept: accept("NOBODY")})
	if err != nil || len(vs) != 0 || next != nil {
		t.Fatalf("%v %v %v", vs, next, err)
	}
	vs, _, err = r.FileHistory(ctx, HistoryQuery{Start: "HEAD", Path: "a.md", Limit: 5, Accept: accept("OURS")})
	if err != nil || len(vs) != 1 {
		t.Fatalf("%v %v", vs, err)
	}
}

func TestParseLogSurvivesStrangeNamesAndPaths(t *testing.T) {
	t.Parallel()
	h := strings.Repeat("a", 40)
	h2 := strings.Repeat("b", 40)
	out := h + "\x00" + "1700000000" + "\x00" + "Mallory\x1e" + h2 + "\x1f x" + "\x00" + "API token\x00" + "\nR090\x00old name.md\x00new\nline.md\x00" +
		h2 + "\x00" + "1600000000" + "\x00" + "Ann" + "\x00" + "\x00" + "\nA\x00weird\x00path.md\x00"
	vs := parseLog(out)
	if len(vs) != 2 || vs[0].Commit != h || vs[0].Status != 'R' || vs[0].Similarity != 90 || vs[0].Path != "new\nline.md" ||
		vs[1].Commit != h2 || vs[1].Path != "weird\x00path.md"[:5] || vs[0].Via != "API token" || vs[1].Via != "" {
		t.Fatalf("%+v", vs)
	}
}
