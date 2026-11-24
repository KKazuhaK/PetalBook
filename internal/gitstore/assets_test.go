package gitstore

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAttachmentRepositoryBuiltWithoutAWorkTree(t *testing.T) {
	needGit(t)
	remote := filterRemote(t)
	ctx := context.Background()
	// Someone put two files in the remote; the server's meta clone has no contents.
	seed := other(t, remote)
	write(t, seed, "p1/a.txt", "hello")
	write(t, seed, "p1/b.txt", "world")
	sh(t, seed, "add", "-A")
	sh(t, seed, "commit", "-qm", "seed")
	sh(t, seed, "push", "-q", "origin", "HEAD:main")

	meta := newClone(t, fileURL(remote), nil)
	if err := meta.FetchFiltered(ctx, "main", true); err != nil {
		t.Fatal(err)
	}
	tip := meta.RemoteTip(ctx, "main")
	if tip == "" {
		t.Fatal("no tip")
	}
	if ids, err := meta.MissingEverything(ctx, tip); err != nil || len(ids) != 2 {
		t.Fatalf("the meta clone has contents it should not: %v %v", ids, err)
	}

	scratch, err := meta.Scratch(ctx, filepath.Join(t.TempDir(), "scratch"))
	if err != nil {
		t.Fatal(err)
	}
	if err := scratch.SetHead(ctx, tip); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "c.txt")
	os.WriteFile(f, []byte("new"), 0o644)
	ids, err := scratch.HashFiles(ctx, []string{f})
	if err != nil || ids[0] != BlobID([]byte("new")) {
		t.Fatalf("hash %v %v", ids, err)
	}
	tree, _, err := scratch.EditTree(ctx, scratch.TreeOf(ctx, tip), TreeEdit{Put: map[string]string{"p1/c.txt": ids[0], "p2/d.txt": ids[0]}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := scratch.CommitTree(ctx, tree, tip, "add", person, svc)
	if err != nil {
		t.Fatal(err)
	}
	if err := scratch.Push(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	if got := sh(t, remote, "ls-tree", "-r", "--name-only", c); got != "p1/a.txt\np1/b.txt\np1/c.txt\np2/d.txt" {
		t.Fatalf("remote tree:\n%s", got)
	}
	// The old contents were not sent again and the meta clone never got the new one.
	if err := meta.FetchFiltered(ctx, "main", false); err != nil {
		t.Fatal(err)
	}
	if ms, _ := meta.MissingBlobs(ctx, []string{ids[0]}); len(ms) != 1 {
		t.Fatal("the contents of a new file stayed in the meta clone")
	}
	// Contents come by id into a scratch repository and are read out of it.
	s2, err := meta.Scratch(ctx, filepath.Join(t.TempDir(), "scratch2"))
	if err != nil {
		t.Fatal(err)
	}
	a := BlobID([]byte("hello"))
	if err := s2.FetchBlobs(ctx, []string{a}); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := s2.CatBlob(ctx, a, &b); err != nil || b.String() != "hello" {
		t.Fatalf("cat: %q %v", b.String(), err)
	}
	// A folder taken out, with the line added to .gitignore.
	sp := newClone(t, fileURL(remote), nil)
	if err := sp.Fetch(ctx, "main", true); err != nil {
		t.Fatal(err)
	}
	sh(t, sp.Dir(), "reset", "-q", "--hard", "origin/main")
	cm, err := sp.CommitWithout(ctx, "p1", "/p1/", "without p1", person, svc)
	if err != nil || cm == "" {
		t.Fatalf("commit without: %q %v", cm, err)
	}
	if got := sh(t, sp.Dir(), "ls-tree", "-r", "--name-only", "HEAD"); got != ".gitignore\np2/d.txt" {
		t.Fatalf("tree without p1:\n%s", got)
	}
	if got := sh(t, sp.Dir(), "cat-file", "-p", "HEAD:.gitignore"); !strings.Contains(got, "/p1/") {
		t.Fatalf(".gitignore: %q", got)
	}
	if cm2, err := sp.CommitWithout(ctx, "p1", "/p1/", "again", person, svc); err != nil || cm2 != "" {
		t.Fatalf("a second time should do nothing: %q %v", cm2, err)
	}
}
