package gitstore

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testSSHD runs an unprivileged sshd on a high loopback port with a host key
// of its own, accepting one public key as the current user.
type testSSHD struct {
	t      *testing.T
	dir    string
	port   int
	cmd    *exec.Cmd
	done   chan struct{}
	cancel context.CancelFunc
	pub    string // authorized key line
}

func startSSHD(t *testing.T, authorized string) *testSSHD {
	t.Helper()
	sshd := "/usr/sbin/sshd"
	if _, err := os.Stat(sshd); err != nil {
		t.Skip("sshd is not installed")
	}
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen is not installed")
	}
	s := &testSSHD{t: t, dir: t.TempDir(), pub: authorized}
	s.newHostKey()
	os.WriteFile(filepath.Join(s.dir, "authorized_keys"), []byte(authorized+"\n"), 0o600)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("cannot listen")
	}
	s.port = ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	s.start()
	return s
}

func (s *testSSHD) newHostKey() {
	s.t.Helper()
	key := filepath.Join(s.dir, "host_ed25519")
	os.Remove(key)
	os.Remove(key + ".pub")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		s.t.Skipf("ssh-keygen: %v %s", err, out)
	}
}

// hostFingerprint is the SHA256 fingerprint of the current host key.
func (s *testSSHD) hostFingerprint() string {
	out, err := exec.Command("ssh-keygen", "-l", "-f", filepath.Join(s.dir, "host_ed25519.pub")).Output()
	if err != nil {
		s.t.Fatal(err)
	}
	return strings.Fields(string(out))[1]
}

func (s *testSSHD) start() {
	s.t.Helper()
	conf := fmt.Sprintf("Port %d\nListenAddress 127.0.0.1\nHostKey %s\nAuthorizedKeysFile %s\nPidFile %s\n"+
		"PasswordAuthentication no\nKbdInteractiveAuthentication no\nPubkeyAuthentication yes\nUsePAM no\nStrictModes no\nLogLevel ERROR\n",
		s.port, filepath.Join(s.dir, "host_ed25519"), filepath.Join(s.dir, "authorized_keys"), filepath.Join(s.dir, "pid"))
	cf := filepath.Join(s.dir, "sshd_config")
	os.WriteFile(cf, []byte(conf), 0o600)
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.cmd = exec.CommandContext(ctx, "/usr/sbin/sshd", "-D", "-e", "-f", cf)
	// A file, not a pipe: the per-connection children inherit it, and a pipe
	// would keep Wait from returning until they are gone.
	logf, err := os.Create(filepath.Join(s.dir, "sshd.log"))
	if err != nil {
		s.t.Fatal(err)
	}
	s.cmd.Stderr = logf
	setProcessGroup(s.cmd)
	if err := s.cmd.Start(); err != nil {
		s.t.Skipf("sshd does not start unprivileged here: %v", err)
	}
	done := make(chan struct{})
	cmd := s.cmd
	go func() { cmd.Wait(); close(done) }()
	s.done = done
	s.t.Cleanup(s.stop)
	for i := 0; i < 100; i++ {
		select {
		case <-done:
			b, _ := os.ReadFile(filepath.Join(s.dir, "sshd.log"))
			s.t.Skipf("sshd exited: %s", b)
		default:
		}
		if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", s.port), 100*time.Millisecond); err == nil {
			c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.t.Skip("sshd did not come up")
}

func (s *testSSHD) stop() {
	if s.cmd != nil && s.cmd.Process != nil {
		s.cancel() // kills the whole process group: sshd and its children
		<-s.done
		s.cmd = nil
	}
}

func TestSSHEndToEnd(t *testing.T) {
	needGit(t)
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("ssh is not installed")
	}
	u, err := user.Current()
	if err != nil {
		t.Skip("no current user")
	}
	key, _ := GenerateSSHKey("petalbook-test")
	srv := startSSHD(t, key.Public)
	bare := bareRemote(t)
	ctx := context.Background()
	known := filepath.Join(t.TempDir(), "known_hosts", "space")
	cred := SSH{Key: func() ([]byte, error) { return key.Private, nil }, KnownHosts: known}

	dir := filepath.Join(t.TempDir(), "clone")
	r := New(Options{Dir: dir, Cred: cred, NetTimeout: 20 * time.Second}) // no AllowUnsafe: ssh is a normal transport
	if err := r.Init(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	url := fmt.Sprintf("ssh://%s@127.0.0.1:%d%s", u.Username, srv.port, bare)
	if err := r.SetRemote(ctx, url); err != nil {
		t.Fatal(err)
	}
	if ok, err := r.HasBranch(ctx, "main"); err != nil || ok {
		t.Fatalf("an empty remote over ssh: %v %v", ok, err)
	}
	write(t, dir, "a.md", "over ssh\n")
	commit(t, r, "a.md")
	if err := r.Push(ctx, "main"); err != nil {
		t.Fatalf("push over ssh: %v", err)
	}
	if got := sh(t, bare, "log", "-1", "--format=%s", "main"); got != "change of 1" {
		t.Fatalf("the remote has %q", got)
	}
	o := other(t, bare)
	write(t, o, "b.md", "b\n")
	sh(t, o, "add", "-A")
	sh(t, o, "commit", "-qm", "b")
	sh(t, o, "push", "-q", "origin", "main")
	if err := r.Fetch(ctx, "main", false); err != nil {
		t.Fatalf("fetch over ssh: %v", err)
	}
	// The host key was trusted on first use, and is reported.
	if fp := HostKeyFingerprint(known); fp == "" || fp != srv.hostFingerprint() {
		t.Fatalf("host key fingerprint %q, sshd has %q", fp, srv.hostFingerprint())
	}
	// No key material is left behind.
	if m, _ := filepath.Glob(filepath.Join(os.TempDir(), "petalbook-ssh-*")); len(m) != 0 {
		t.Errorf("key directories left: %v", m)
	}

	// A key the server does not know is a refused credential.
	other, _ := GenerateSSHKey("stranger")
	r2 := r.WithCredential(SSH{Key: func() ([]byte, error) { return other.Private, nil }, KnownHosts: known})
	if err := r2.Fetch(ctx, "main", false); KindOf(err) != KindAuth {
		t.Fatalf("an unknown key: %v", err)
	}

	// A host that now presents another key is refused, not trusted again.
	srv.stop()
	srv.newHostKey()
	srv.start()
	err = r.Fetch(ctx, "main", false)
	if KindOf(err) != KindHostKey {
		t.Fatalf("a changed host key: %v", err)
	}
	if fp := HostKeyFingerprint(known); fp == srv.hostFingerprint() {
		t.Fatal("the new host key was trusted")
	}
}
