package gitstore

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCommitTimeInALargeSpace measures one commit of one changed page in a
// space of 20 000 small files, made the way CommitPaths makes it, against the
// `git commit <path>` it replaced. It takes a while, so it runs only when
// asked for:
//
//	PETALBOOK_TIMING=1 go test -run TestCommitTimeInALargeSpace -v ./internal/gitstore/
//
// The two are compared, not held to a fixed number: a commit must not grow
// with the space, and the old way did.
func TestCommitTimeInALargeSpace(t *testing.T) {
	if os.Getenv("PETALBOOK_TIMING") == "" {
		t.Skip("set PETALBOOK_TIMING=1 to measure")
	}
	needGit(t)
	ctx := context.Background()
	const files, rounds = 20000, 5
	r := newClone(t, bareRemote(t), nil)
	var paths []string
	body := strings.Repeat("一段不长的正文，够一个小文件用。\n", 4)
	for i := 0; i < files; i++ {
		p := fmt.Sprintf("d%03d/%05d.md", i%200, i)
		write(t, r.Dir(), p, fmt.Sprintf("id: %d\n%s", i, body))
		paths = append(paths, p)
	}
	start := time.Now()
	commit(t, r, paths...)
	t.Logf("first commit of %d files: %v", files, time.Since(start))

	var now, before, between time.Duration
	for i := 0; i < rounds; i++ {
		write(t, r.Dir(), paths[7], fmt.Sprintf("edit %d\n", i))
		start = time.Now()
		commit(t, r, paths[7])
		now += time.Since(start)

		// The old last step: stage, then `git commit` of the path, which
		// builds its index afresh from HEAD and looks at every tracked file.
		write(t, r.Dir(), paths[8], fmt.Sprintf("edit %d\n", i))
		start = time.Now()
		if _, err := r.gitIn(ctx, nil, []byte(paths[8]+"\x00"), "add", "-A", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("git", "-C", r.Dir(), "-c", "core.hooksPath="+os.DevNull, "-c", "commit.gpgsign=false",
			"commit", "--quiet", "--no-verify", "--message=old way", "--pathspec-from-file=-", "--pathspec-file-nul")
		cmd.Env = append(baseEnv(true), "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@example.test", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@example.test")
		cmd.Stdin = strings.NewReader(paths[8] + "\x00")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git commit: %v\n%s", err, out)
		}
		before += time.Since(start)

		// The step in between, given up for this: the commit assembled in an
		// index of its own and `git commit` run on it. A fresh index knows
		// nothing of the files on disk, so git reads every one of them.
		write(t, r.Dir(), paths[9], fmt.Sprintf("edit %d\n", i))
		start = time.Now()
		if _, err := r.gitIn(ctx, nil, []byte(paths[9]+"\x00"), "add", "-A", "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
			t.Fatal(err)
		}
		idx := filepath.Join(t.TempDir(), "index")
		ienv := append(baseEnv(true), "GIT_INDEX_FILE="+idx, "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@example.test", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@example.test")
		entry, err := r.git(ctx, "ls-files", "-s", "-z", "--", paths[9])
		if err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{
			{"read-tree", "HEAD"},
			{"update-index", "-z", "--index-info"},
			{"-c", "commit.gpgsign=false", "commit", "--quiet", "--no-verify", "--message=in between"},
		} {
			cmd := exec.Command("git", append([]string{"-C", r.Dir(), "-c", "core.hooksPath=" + os.DevNull}, args...)...)
			cmd.Env = ienv
			cmd.Stdin = strings.NewReader(entry)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		between += time.Since(start)
	}
	t.Logf("one changed page, %d files in the space, mean of %d: now %v; git commit on the real index %v; git commit on an index of its own %v",
		files, rounds, now/rounds, before/rounds, between/rounds)
	if dir, _ := filepath.Abs(r.Dir()); dir != "" {
		t.Logf("measured in %s", dir)
	}
}
