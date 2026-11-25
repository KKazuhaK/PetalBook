package gitstore

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

// An export reads a repository: the files of a commit as a tar, and the whole
// history as a bundle. The runner allows those two shapes and no other.
func TestArchiveAndBundleReadARepository(t *testing.T) {
	t.Parallel()
	needGit(t)
	dir := t.TempDir()
	ctx := context.Background()
	r := New(Options{Dir: dir})
	if err := r.Init(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "a.md", "alpha\n")
	write(t, dir, "sub/b.md", "beta\n")
	if _, err := r.CommitPaths(ctx, []string{"a.md", "sub/b.md"}, func([]Change) string { return "first" }, person, svc); err != nil {
		t.Fatal(err)
	}
	head := r.Head(ctx)
	var buf bytes.Buffer
	if err := r.Archive(ctx, head, &buf); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	tr := tar.NewReader(&buf)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			b, _ := io.ReadAll(tr)
			got[h.Name] = string(b)
		}
	}
	if got["a.md"] != "alpha\n" || got["sub/b.md"] != "beta\n" || len(got) != 2 {
		t.Fatalf("%v", got)
	}
	bundle := filepath.Join(t.TempDir(), "h.bundle")
	if err := r.Bundle(ctx, bundle); err != nil {
		t.Fatal(err)
	}
	if out := sh(t, dir, "bundle", "verify", bundle); !strings.Contains(out, "okay") {
		t.Fatalf("%s", out)
	}
	// Nothing but those shapes: a branch name or an option is not a commit, a bundle
	// goes to an absolute path, and no other archive format is run.
	for _, args := range [][]string{
		{"archive", "--format=tar", "HEAD"},
		{"archive", "--format=zip", head},
		{"archive", "--format=tar", "--remote=origin", head},
		{"archive", "--format=tar", "-o", "/tmp/x", head},
		{"archive", "--format=tar", head, "extra"},
		{"bundle", "create", "rel.bundle", "--all"},
		{"bundle", "create", "-", "--all"},
		{"bundle", "unbundle", bundle},
		{"bundle", "create", bundle},
	} {
		if err := refuseDestructive(args); !errors.Is(err, errDestructive) {
			t.Errorf("git %v was allowed", args)
		}
	}
}
