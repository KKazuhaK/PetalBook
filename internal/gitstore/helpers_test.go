package gitstore

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

// sh runs plain git (not through the restricted runner) as a test's own
// stand-in for "someone else".
func sh(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_AUTHOR_NAME=Other", "GIT_AUTHOR_EMAIL=other@example.test", "GIT_COMMITTER_NAME=Other", "GIT_COMMITTER_EMAIL=other@example.test")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t testing.TB, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

var (
	svc    = Identity{"Service", "svc@example.test"}
	person = Identity{"Ann", "ann@example.test"}
)

func msg(cs []Change) string { return fmt.Sprintf("change of %d", len(cs)) }

// bareRemote makes an empty bare repository to act as the remote.
func bareRemote(t testing.TB) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "remote.git")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sh(t, dir, "init", "--bare", "-q", "-b", "main")
	return dir
}

// newClone makes a Repo with origin set to remote.
func newClone(t testing.TB, remote string, cred Credential) *Repo {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "clone")
	r := New(Options{Dir: dir, AllowUnsafe: true, Cred: cred})
	ctx := context.Background()
	if err := r.Init(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetRemote(ctx, remote); err != nil {
		t.Fatal(err)
	}
	return r
}

// other is a clone made by plain git, for changes "somebody else" pushes.
func other(t testing.TB, remote string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "other")
	cmd := exec.Command("git", "clone", "-q", remote, dir)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	sh(t, dir, "checkout", "-q", "-B", "main")
	return dir
}

// recorder routes every command of the suite through a list, so that the
// suite as a whole can be checked for destructive commands.
type recorder struct {
	mu   sync.Mutex
	cmds []Cmd
	next Runner
}

func (r *recorder) Run(ctx context.Context, c Cmd) (string, string, error) {
	r.mu.Lock()
	r.cmds = append(r.cmds, Cmd{Dir: c.Dir, Args: append([]string{}, c.Args...), Secrets: c.Secrets, Env: append([]string{}, c.Env...)})
	r.mu.Unlock()
	return r.next.Run(ctx, c)
}

func (r *recorder) all() []Cmd {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Cmd{}, r.cmds...)
}

var rec *recorder
