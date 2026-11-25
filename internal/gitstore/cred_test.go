package gitstore

import (
	"bytes"
	"context"
	"io/fs"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// gitHTTP serves the bare repositories under root over git's smart HTTP
// protocol (git http-backend, as CGI) behind basic authentication.
func gitHTTP(t *testing.T, root, user, token string) *httptest.Server {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	backend := &cgi.Handler{Path: git, Args: []string{"http-backend"}, Root: "/", InheritEnv: []string{"PATH"},
		Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != user || p != token {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func tokenRepo(t *testing.T, srv *httptest.Server, name string, token string, user string) *Repo {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "clone")
	r := New(Options{Dir: dir, AllowUnsafe: true, Cred: Token{Username: user, Get: func() (string, error) { return token, nil }}})
	if err := r.Init(context.Background(), "main"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetRemote(context.Background(), srv.URL+"/"+name); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestTokenFetchAndPushOverHTTP(t *testing.T) {
	needGit(t)
	ctx := context.Background()
	root := t.TempDir()
	bare := filepath.Join(root, "notes.git")
	os.MkdirAll(bare, 0o755)
	sh(t, bare, "init", "--bare", "-q", "-b", "main")
	sh(t, bare, "config", "http.receivepack", "true")
	const token = "ghp_S3cr3tT0kenValue1234567890"
	srv := gitHTTP(t, root, "x-access-token", token)

	before := len(rec.all())
	r := tokenRepo(t, srv, "notes.git", token, "x-access-token")
	if ok, err := r.HasBranch(ctx, "main"); err != nil || ok {
		t.Fatalf("%v %v", ok, err)
	}
	write(t, r.Dir(), "a.md", "one\n")
	commit(t, r, "a.md")
	if err := r.Push(ctx, "main"); err != nil {
		t.Fatalf("push with the token: %v", err)
	}
	if got := sh(t, bare, "log", "-1", "--format=%s", "main"); got != "change of 1" {
		t.Fatalf("the remote has %q", got)
	}
	o := other(t, bare)
	write(t, o, "b.md", "two\n")
	sh(t, o, "add", "-A")
	sh(t, o, "commit", "-qm", "b")
	sh(t, o, "push", "-q", "origin", "main")
	if err := r.Fetch(ctx, "main", false); err != nil {
		t.Fatalf("fetch with the token: %v", err)
	}
	if a, b, err := r.AheadBehind(ctx, "main"); err != nil || a != 0 || b != 1 {
		t.Fatalf("%d %d %v", a, b, err)
	}

	// The token is in the environment of the commands and nowhere else: not in
	// any argument, not in any file of the repository, not in an error text.
	sawEnv := false
	for _, c := range rec.all()[before:] {
		for _, a := range c.Args {
			if strings.Contains(a, token) {
				t.Errorf("the token is in the arguments of git %s", strings.Join(c.Args, " "))
			}
		}
		for _, e := range c.Env {
			if strings.Contains(e, token) {
				sawEnv = true
			}
		}
		if len(c.Env) > 0 && strings.Contains(strings.Join(c.Env, "\n"), "GIT_SSH_COMMAND") {
			t.Error("an ssh command on an https remote")
		}
	}
	if !sawEnv {
		t.Error("the token did not reach git through the environment")
	}
	filepath.WalkDir(r.Dir(), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, _ := os.ReadFile(p); bytes.Contains(b, []byte(token)) {
				t.Errorf("the token is in %s", p)
			}
		}
		return nil
	})
	cfg, _ := os.ReadFile(filepath.Join(r.Dir(), ".git", "config"))
	if strings.Contains(string(cfg), "helper") || strings.Contains(string(cfg), "@") {
		t.Errorf("the repository's configuration holds a helper or credentials:\n%s", cfg)
	}
}

func TestWrongTokenIsARefusedCredential(t *testing.T) {
	t.Parallel()
	needGit(t)
	root := t.TempDir()
	bare := filepath.Join(root, "notes.git")
	os.MkdirAll(bare, 0o755)
	sh(t, bare, "init", "--bare", "-q", "-b", "main")
	srv := gitHTTP(t, root, "x-access-token", "right-token-123456")
	r := tokenRepo(t, srv, "notes.git", "wrong-token-654321", "x-access-token")
	for name, err := range map[string]error{
		"fetch": r.Fetch(context.Background(), "main", false),
		"check": func() error { _, err := r.HasBranch(context.Background(), "main"); return err }(),
	} {
		if KindOf(err) != KindAuth {
			t.Errorf("%s: %v", name, err)
		}
		if err != nil && (strings.Contains(err.Error(), "wrong-token") || strings.Contains(err.Error(), "right-token")) {
			t.Errorf("%s: the message has a token: %v", name, err)
		}
	}
	// A wrong user name is refused the same way.
	r2 := tokenRepo(t, srv, "notes.git", "right-token-123456", "somebody-else")
	if err := r2.Fetch(context.Background(), "main", false); KindOf(err) != KindAuth {
		t.Errorf("wrong user: %v", err)
	}
}

func TestUnreachableAndHungRemotes(t *testing.T) {
	t.Parallel()
	needGit(t)
	// Nothing listens.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	r := New(Options{Dir: filepath.Join(t.TempDir(), "c"), AllowUnsafe: true, Cred: Token{Get: func() (string, error) { return "tok", nil }}})
	ctx := context.Background()
	r.Init(ctx, "main")
	r.SetRemote(ctx, "http://"+addr+"/x.git")
	if err := r.Fetch(ctx, "main", false); KindOf(err) != KindOffline {
		t.Fatalf("refused connection: %v", err)
	}
	// Something accepts and never answers: the deadline ends it, and the process with it.
	hang, _ := net.Listen("tcp", "127.0.0.1:0")
	defer hang.Close()
	go func() {
		for {
			c, err := hang.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	r2 := New(Options{Dir: filepath.Join(t.TempDir(), "c"), AllowUnsafe: true, NetTimeout: 400 * time.Millisecond, Cred: Token{Get: func() (string, error) { return "tok", nil }}})
	r2.Init(ctx, "main")
	r2.SetRemote(ctx, "http://"+hang.Addr().String()+"/x.git")
	start := time.Now()
	err := r2.Fetch(ctx, "main", false)
	if KindOf(err) != KindOffline || time.Since(start) > 10*time.Second {
		t.Fatalf("hung remote: %v after %v", err, time.Since(start))
	}
}

func TestSSHKeyGeneration(t *testing.T) {
	t.Parallel()
	k, err := GenerateSSHKey("petalbook-01JD3K8Q7Z")
	if err != nil {
		t.Fatal(err)
	}
	pub, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(k.Public))
	if err != nil || comment != "petalbook-01JD3K8Q7Z" || pub.Type() != "ssh-ed25519" {
		t.Fatalf("public key: %v %q %v", err, comment, pub)
	}
	if k.Fingerprint != ssh.FingerprintSHA256(pub) || !strings.HasPrefix(k.Fingerprint, "SHA256:") {
		t.Fatalf("fingerprint %q", k.Fingerprint)
	}
	if fp, err := Fingerprint(k.Public); err != nil || fp != k.Fingerprint {
		t.Fatalf("%q %v", fp, err)
	}
	signer, err := ssh.ParsePrivateKey(k.Private)
	if err != nil {
		t.Fatalf("private key: %v", err)
	}
	if !bytes.Equal(signer.PublicKey().Marshal(), pub.Marshal()) {
		t.Fatal("the private key does not match the public key")
	}
	if !strings.Contains(string(k.Private), "OPENSSH PRIVATE KEY") || strings.Contains(k.Public, "PRIVATE") {
		t.Fatal("wrong key formats")
	}
	k2, _ := GenerateSSHKey("x")
	if k2.Fingerprint == k.Fingerprint {
		t.Fatal("two keys alike")
	}
}

func TestSSHCommandAndKeyFileLifetime(t *testing.T) {
	t.Parallel()
	want := "ssh -i '/tmp/k' -F /dev/null -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=accept-new " +
		"-o UserKnownHostsFile='/data/known hosts/it'\\''s' -o GlobalKnownHostsFile=/dev/null -o HashKnownHosts=no " +
		"-o PreferredAuthentications=publickey -o ClearAllForwardings=yes -o ConnectTimeout=20 -o ServerAliveInterval=15 -o ServerAliveCountMax=2"
	if got := SSHCommand("", "/tmp/k", "/data/known hosts/it's"); got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}

	k, _ := GenerateSSHKey("c")
	known := filepath.Join(t.TempDir(), "kh", "space")
	var seenFile string
	c := SSH{Key: func() ([]byte, error) { return k.Private, nil }, KnownHosts: known}
	env, secrets, cleanup, err := c.prepare()
	if err != nil {
		t.Fatal(err)
	}
	var cmd string
	for _, e := range env {
		if strings.HasPrefix(e, "GIT_SSH_COMMAND=") {
			cmd = strings.TrimPrefix(e, "GIT_SSH_COMMAND=")
		}
	}
	if !strings.Contains(cmd, "IdentitiesOnly=yes") || !strings.Contains(cmd, "BatchMode=yes") || !strings.Contains(cmd, "StrictHostKeyChecking=accept-new") || !strings.Contains(cmd, "UserKnownHostsFile='"+known+"'") {
		t.Fatalf("command: %s", cmd)
	}
	// The key is a 0600 file in a 0700 directory while the command runs...
	for _, s := range secrets {
		if fi, err := os.Stat(s); err == nil && !fi.IsDir() {
			seenFile = s
			if fi.Mode().Perm() != 0o600 {
				t.Errorf("key file mode %v", fi.Mode())
			}
			if b, _ := os.ReadFile(s); !bytes.Equal(b, k.Private) {
				t.Error("key file content")
			}
			if di, _ := os.Stat(filepath.Dir(s)); di.Mode().Perm() != 0o700 {
				t.Errorf("key directory mode %v", di.Mode())
			}
		}
	}
	if seenFile == "" {
		t.Fatal("no key file")
	}
	// ... and gone afterwards, together with its directory.
	cleanup()
	if _, err := os.Stat(seenFile); err == nil {
		t.Fatal("the key file outlived the command")
	}
	if _, err := os.Stat(filepath.Dir(seenFile)); err == nil {
		t.Fatal("the key directory outlived the command")
	}
	// A failing key source stops the command before git starts.
	bad := SSH{Key: func() ([]byte, error) { return nil, os.ErrNotExist }, KnownHosts: known}
	if _, _, _, err := bad.prepare(); err == nil {
		t.Fatal("no error")
	}
}

func TestHostKeyFingerprint(t *testing.T) {
	t.Parallel()
	k, _ := GenerateSSHKey("host")
	line := "example.com " + strings.SplitN(k.Public, " ", 3)[0] + " " + strings.SplitN(k.Public, " ", 3)[1] + "\n"
	f := filepath.Join(t.TempDir(), "known")
	if HostKeyFingerprint(f) != "" {
		t.Fatal("a fingerprint without a file")
	}
	os.WriteFile(f, []byte("# comment\n\n"+line), 0o600)
	if got := HostKeyFingerprint(f); got != k.Fingerprint {
		t.Fatalf("%q want %q", got, k.Fingerprint)
	}
}
