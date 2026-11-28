package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func rgit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Other", "-c", "user.email=o@example.test"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestServeFlagsOfRemotes(t *testing.T) {
	out, err := run(t, "serve", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--pull-interval", "--remote-host", "--remote-timeout", "PETALBOOK_SECRET_KEY", "secret.key", "github.com", "codeberg.org", "(default 1m0s)"} {
		if !strings.Contains(out, want) {
			t.Errorf("the help does not mention %q", want)
		}
	}
	data := filepath.Join(tempDir(t), "data")
	space := filepath.Join(tempDir(t), "space")
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"pull interval in single mode", []string{"serve", "--space", space, "--pull-interval", "30s"}, "accounts mode"},
		{"remote host in single mode", []string{"serve", "--space", space, "--remote-host", "github.com"}, "accounts mode"},
		{"negative interval", []string{"serve", "--data", data, "--pull-interval", "-1s"}, "--pull-interval"},
		{"remote host with a scheme", []string{"serve", "--data", data, "--remote-host", "https://github.com"}, "--remote-host"},
		{"blank remote host", []string{"serve", "--data", data, "--remote-host", " ,github.com"}, "--remote-host"},
		{"remote host with a path", []string{"serve", "--data", data, "--remote-host", "github.com/a"}, "--remote-host"},
	} {
		_, err := run(t, append(c.args, "--listen", "127.0.0.1:0")...)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v (want it to mention %q)", c.name, err, c.want)
		}
	}
	for _, p := range []string{data, space} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s was created although the configuration was refused", p)
		}
	}
	// The environment supplies them like any other flag.
	t.Setenv("PETALBOOK_DATA", data)
	t.Setenv("PETALBOOK_PULL_INTERVAL", "banana")
	if _, err := run(t, "serve", "--listen", "127.0.0.1:0"); err == nil || !strings.Contains(err.Error(), "PETALBOOK_PULL_INTERVAL") {
		t.Fatalf("%v", err)
	}
	t.Setenv("PETALBOOK_PULL_INTERVAL", "")
	t.Setenv("PETALBOOK_REMOTE_HOST", "https://github.com")
	if _, err := run(t, "serve", "--listen", "127.0.0.1:0"); err == nil || !strings.Contains(err.Error(), "--remote-host") {
		t.Fatalf("PETALBOOK_REMOTE_HOST not applied: %v", err)
	}
	// Valid values are accepted: * and several hosts.
	for _, hosts := range [][]string{{"*"}, {"github.com", "git.example.org"}, {"[2001:db8::1]"}} {
		if _, err := (serveConfig{data: data, remoteHosts: hosts}).check(); err != nil {
			t.Errorf("%v: %v", hosts, err)
		}
	}
}

func TestRemoteSpaceEndToEnd(t *testing.T) {
	needGit(t)
	data := filepath.Join(tempDir(t), "data")
	remote := filepath.Join(tempDir(t), "remote.git")
	os.MkdirAll(remote, 0o755)
	rgit(t, remote, "init", "--bare", "-q", "-b", "main")
	cfg := serveConfig{data: data, committer: "Svc <svc@example.test>", commitIdle: time.Hour, commitMax: time.Hour,
		pullInterval: 100 * time.Millisecond, remoteHosts: []string{"*"}, remoteUnsafe: true, remoteTimeout: 30 * time.Second}
	srv := start(t, cfg)
	b := &browser{t: t, base: srv.base}
	const password = "a long enough password"
	token := strings.Trim(regexpSetup(t, srv.log.String()), `"`)
	if code, r := b.do("POST", "/api/setup", map[string]any{"token": token, "username": "kazuha", "name": "Kazuha Z", "password": password}); code != 201 {
		t.Fatalf("%d %v", code, r)
	}
	_, r := b.do("GET", "/api/spaces", nil)
	sid := r["spaces"].([]any)[0].(map[string]any)["id"].(string)
	const secretToken = "ghp_EndToEndSecretToken_42"
	code, r := b.do("PUT", "/api/spaces/"+sid+"/remote", map[string]any{"url": remote, "credential": map[string]any{"type": "token", "token": secretToken}})
	if code != 200 || r["state"] != "ok" {
		t.Fatalf("%d %v", code, r)
	}
	if code, r := b.do("POST", "/api/spaces/"+sid+"/pages", map[string]any{"meta": map[string]any{"title": "Pushed"}, "body": "x\n"}); code != 201 {
		t.Fatalf("%d %v", code, r)
	}
	if got := rgit(t, remote, "log", "-1", "--format=%an|%cn|%s", "main"); got != "Kazuha Z|Svc|Add Pushed" {
		t.Fatalf("the remote has %q", got)
	}
	// Somebody else pushes; the puller (every 100 ms here) brings it in with no request in between.
	other := filepath.Join(tempDir(t), "other")
	rgit(t, tempDir(t), "clone", "-q", remote, other)
	rgit(t, other, "checkout", "-q", "-B", "main")
	os.WriteFile(filepath.Join(other, "FromElsewhere.md"), []byte("---\nid: EXTRN00001\ntitle: From elsewhere\n---\nhello\n"), 0o644)
	rgit(t, other, "add", "-A")
	rgit(t, other, "commit", "-qm", "elsewhere")
	rgit(t, other, "push", "-q", "origin", "main")
	deadline := time.Now().Add(20 * time.Second)
	for {
		code, r := b.do("GET", "/api/spaces/"+sid+"/pages/EXTRN00001", nil)
		if code == 200 && r["body"] == "hello\n" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the pull did not bring the page in: %d %v\n%s", code, r, srv.log)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := srv.stop(); err != nil {
		t.Fatal(err)
	}
	// The credential is encrypted at rest, in a private key file's care, and in no log.
	if fi, err := os.Stat(filepath.Join(data, "secret.key")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("secret.key: %v %v", fi, err)
	}
	db, _ := os.ReadFile(filepath.Join(data, "petalbook.db"))
	if strings.Contains(string(db), secretToken) {
		t.Fatal("the token is in the state database in the clear")
	}
	for _, s := range []string{secretToken, password} {
		if strings.Contains(srv.log.String(), s) {
			t.Fatalf("a secret is in the log:\n%s", srv.log)
		}
	}
	filepath.WalkDir(filepath.Join(data, "spaces", sid), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), secretToken) {
				t.Errorf("the token is in %s", p)
			}
		}
		return nil
	})
	// A restart reopens the space as a remote space.
	srv2 := start(t, cfg)
	b2 := &browser{t: t, base: srv2.base}
	if code, _ := b2.do("POST", "/api/login", map[string]any{"identifier": "kazuha", "password": password}); code != 200 {
		t.Fatal("cannot sign in after a restart")
	}
	if code, r := b2.do("GET", "/api/spaces/"+sid+"/remote", nil); code != 200 || r["url"] != remote {
		t.Fatalf("%d %v", code, r)
	}
	if code, r := b2.do("POST", "/api/spaces/"+sid+"/pages", map[string]any{"meta": map[string]any{"title": "After restart"}}); code != 201 {
		t.Fatalf("%d %v", code, r)
	}
	if got := rgit(t, remote, "log", "-1", "--format=%s", "main"); got != "Add After restart" {
		t.Fatalf("%q", got)
	}
}

func regexpSetup(t *testing.T, log string) string {
	t.Helper()
	m := regexp.MustCompile(`link=\S*/setup#(\S+)`).FindStringSubmatch(log)
	if m == nil {
		t.Fatalf("no set-up link in the log:\n%s", log)
	}
	return m[1]
}
