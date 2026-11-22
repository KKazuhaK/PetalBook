package space

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/page"
)

func eventually(t *testing.T, what string, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func commits(t *testing.T, dir string) []string {
	t.Helper()
	out, err := gitTry(dir, "log", "--format=%s")
	if err != nil || out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// gitTry runs plain git, independent of the code under test.
func gitTry(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "core.quotepath=false"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func TestCommitAfterQuietPeriod(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	// The quiet period is long next to the 40 ms between the saves below: a save
	// journals and syncs to disk, which on a slow disk under -race (CI's
	// case-folding image) took longer than a shorter period, and the burst
	// was split in two.
	s := openAt(t, root, func(o *Options) {
		o.CommitIdle, o.CommitMax = time.Second, time.Hour
		o.Committer = "Ann Lee <ann@example.com>"
	})
	// Opening a new space commits the files the server made.
	eventually(t, "initial commit", 5*time.Second, func() bool { return len(commits(t, root)) == 1 })

	p := mustCreate(t, s, "Tang", meta("title", "暮雪"), "v1\n")
	// A burst of saves, each inside the quiet period, is one commit.
	rev := p.Rev
	for i := 0; i < 5; i++ {
		time.Sleep(40 * time.Millisecond)
		n, err := s.Save(p.ID, rev, meta("title", "暮雪"), fmt.Sprintf("v%d\n", i+2))
		if err != nil {
			t.Fatal(err)
		}
		rev = n.Rev
	}
	eventually(t, "commit after the quiet period", 10*time.Second, func() bool { return len(commits(t, root)) == 2 })
	// Twice the quiet period: a committer that started another one after its
	// own commit would have made it by then.
	time.Sleep(2 * time.Second)
	log := commits(t, root)
	if len(log) != 2 || log[0] != "Add 暮雪" {
		t.Fatalf("commits %q", log)
	}
	if who, _ := gitTry(root, "log", "-1", "--format=%an <%ae>|%cn <%ce>"); who != "Ann Lee <ann@example.com>|Ann Lee <ann@example.com>" {
		t.Fatalf("author %q", who)
	}
	if body, _ := gitTry(root, "show", "HEAD:Tang/暮雪.md"); !strings.HasSuffix(body, "v6") {
		t.Fatalf("last save is not in the commit: %q", body)
	}

	// The next edit is described as an edit; a rename and a move too.
	n, _ := s.Save(p.ID, rev, meta("title", "暮雪"), "again\n")
	eventually(t, "edit commit", 10*time.Second, func() bool { return len(commits(t, root)) == 3 })
	if got := commits(t, root)[0]; got != "Edit 暮雪" {
		t.Fatalf("%q", got)
	}
	m, err := s.Move(p.ID, "随笔")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "move commit", 10*time.Second, func() bool { return len(commits(t, root)) == 4 })
	if got := commits(t, root)[0]; got != "Move 暮雪" {
		t.Fatalf("%q", got)
	}
	_ = n
	if err := s.Delete(m.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "delete commit", 10*time.Second, func() bool { return len(commits(t, root)) == 5 })
	if got := commits(t, root)[0]; got != "Delete 暮雪" {
		t.Fatalf("%q", got)
	}
	// The cache and temporary files are never committed.
	files, _ := gitTry(root, "ls-files")
	for _, f := range strings.Split(files, "\n") {
		if strings.Contains(f, "cache") || strings.Contains(f, "tmp") || strings.HasSuffix(f, ".db") {
			t.Fatalf("committed %q", f)
		}
	}
	if !strings.Contains(files, ".petalbook/space.yml") || !strings.Contains(files, ".petalbook/.gitignore") {
		t.Fatalf("tracked files:\n%s", files)
	}
}

func TestCommitAfterMaximumAgeEvenWhileWriting(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	// A commit is due 400 ms after the first write. The quiet period is far
	// longer than any gap between the saves, even on a slow disk, so the
	// commit can only come from the maximum age.
	s := openAt(t, root, func(o *Options) { o.CommitIdle, o.CommitMax = 5*time.Second, 400*time.Millisecond })
	eventually(t, "initial commit", 5*time.Second, func() bool { return len(commits(t, root)) >= 1 })
	base := len(commits(t, root))
	p := mustCreate(t, s, "", meta("title", "长写"), "0")
	rev := p.Rev
	stop := time.Now().Add(3 * time.Second)
	for i := 1; time.Now().Before(stop); i++ {
		n, err := s.Save(p.ID, rev, meta("title", "长写"), fmt.Sprint(i))
		if err != nil {
			t.Fatal(err)
		}
		rev = n.Rev
		if len(commits(t, root)) > base {
			return // committed while writes never paused
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("no commit while writing continuously")
}

func TestCloseFlushesPendingChanges(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	s, err := Open(Options{Root: root, CommitIdle: time.Hour, CommitMax: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		mustCreate(t, s, "", meta("title", fmt.Sprint("页", i)), "")
	}
	if n := len(commits(t, root)); n != 0 {
		t.Fatalf("committed before the timers: %d", n)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	log := commits(t, root)
	if len(log) != 1 {
		t.Fatalf("commits after close: %q", log)
	}
	// The first commit holds the pages and the files made at start-up.
	files, _ := gitTry(root, "show", "--name-only", "--format=", "HEAD")
	for _, want := range []string{"页0.md", "页1.md", "页2.md", ".petalbook/space.yml"} {
		if !strings.Contains(files, want) {
			t.Fatalf("%s not in the commit:\n%s", want, files)
		}
	}
	if who, _ := gitTry(root, "log", "-1", "--format=%an <%ae>"); who != DefaultCommitter {
		t.Fatalf("default author: %q", who)
	}

	// Reopen, add three pages, close: the message counts them.
	s2, err := Open(Options{Root: root, CommitIdle: time.Hour, CommitMax: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		mustCreate(t, s2, "", meta("title", fmt.Sprint("新", i)), "")
	}
	s2.Close()
	if got := commits(t, root)[0]; got != "Add 3 pages" {
		t.Fatalf("%q", got)
	}
	// Close with nothing pending makes no empty commit, and can be repeated.
	before := len(commits(t, root))
	s3, _ := Open(Options{Root: root, CommitIdle: time.Hour, CommitMax: 2 * time.Hour})
	if err := s3.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s3.Close(); err != nil {
		t.Fatal(err)
	}
	if len(commits(t, root)) != before {
		t.Fatal("empty commit")
	}
	// A page edited outside the server is picked up by the next commit.
	s4, _ := Open(Options{Root: root, CommitIdle: time.Hour, CommitMax: 2 * time.Hour})
	os.WriteFile(filepath.Join(root, "手写.md"), []byte("---\nid: HANDWR1TTN\n---\n"), 0o644)
	mustCreate(t, s4, "", meta("title", "再一页"), "")
	s4.Close()
	// Only what the server wrote: the page made by hand is left alone.
	if got := commits(t, root)[0]; got != "Add 再一页" {
		t.Fatalf("%q", got)
	}
	if st, _ := gitTry(root, "status", "--porcelain"); !strings.Contains(st, "手写.md") {
		t.Fatalf("hand-written page should still be untracked:\n%s", st)
	}
}

func TestCommitMessages(t *testing.T) {
	cs := func(spec ...string) []change {
		var out []change
		for _, s := range spec {
			f := strings.Split(s, " ")
			if len(f) == 3 {
				out = append(out, change{Status: f[0][0], Old: f[1], New: f[2]})
			} else {
				out = append(out, change{Status: f[0][0], Old: f[1], New: f[1]})
			}
		}
		return out
	}
	for _, c := range []struct {
		in   []change
		want string
	}{
		{cs("M a/暮雪.md"), "Edit 暮雪"},
		{cs("A 1.md", "A 2.md", "A 3.md"), "Add 3 pages"},
		{cs("A a.md", "M b.md", "M c.md", "D d.md"), "Add a, edit 2 pages, delete d"},
		{cs("R100 x/a.md x/b.md"), "Rename b"},
		{cs("R090 x/a.md y/a.md"), "Move a"},
		{cs("A .petalbook/space.yml"), "Update space.yml"},
		{cs("A .petalbook/space.yml", "A .petalbook/.gitignore"), "Update 2 files"},
		{cs("A p.md", "A .petalbook/space.yml"), "Add p, update space.yml"},
		{cs("A assets/x/a.md"), "Update a.md"},
		{nil, "Update notes"},
		{cs("M " + strings.Repeat("长", 80) + ".md"), "Edit " + strings.Repeat("长", 50) + "…"},
	} {
		if got := commitMessage(c.in); got != c.want {
			t.Errorf("%v: %q, want %q", c.in, got, c.want)
		}
	}
	got := parseNameStatus([]byte("M\x00a.md\x00R100\x00b.md\x00c.md\x00D\x00d.md\x00"))
	if len(got) != 3 || got[1].Old != "b.md" || got[1].New != "c.md" || got[2].Status != 'D' {
		t.Fatalf("%+v", got)
	}
}

func TestOpenKeepsStateDirOutOfTheRepository(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	state := filepath.Join(root, "state-here")
	s := openAt(t, root, func(o *Options) { o.StateDir = state })
	mustCreate(t, s, "", meta("title", "T"), "")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	files, _ := gitTry(root, "ls-files")
	if strings.Contains(files, "state-here") {
		t.Fatalf("state dir committed:\n%s", files)
	}
	if _, err := os.Stat(filepath.Join(state, "index.db")); err != nil {
		t.Fatalf("index not in the state dir: %v", err)
	}
	_ = page.ValidID
}
