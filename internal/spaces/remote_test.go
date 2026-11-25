package spaces

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/gitstore"
	"github.com/KKazuhaK/PetalBook/internal/index"
	"github.com/KKazuhaK/PetalBook/internal/page"
	"github.com/KKazuhaK/PetalBook/internal/secret"
	"github.com/KKazuhaK/PetalBook/internal/space"
	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

// runner is the real git, counting what talks to the remote and able to hold it.
type runner struct {
	mu      sync.Mutex
	fetches int
	down    bool
	// during counts the fetches in flight, max the most at once; hold, if set,
	// is waited on inside every fetch.
	during, max int
	hold        chan struct{}
	// entered, if set, is told each time a fetch starts waiting on hold.
	entered chan struct{}
	// loseAnswer, if set, lets the next push through and then answers it as
	// failed, with the remote down from then on: an answer lost on the way.
	loseAnswer bool
}

func (r *runner) Run(c context.Context, cmd gitstore.Cmd) (string, string, error) {
	sub, _ := gitstore.Subcommand(cmd.Args)
	if sub == "push" {
		r.mu.Lock()
		lose := r.loseAnswer && !r.down
		r.loseAnswer = false
		r.mu.Unlock()
		if lose {
			gitstore.RunReal(c, cmd)
			r.setDown(true)
			return "", "fatal: the remote end hung up unexpectedly", errors.New("exit status 128")
		}
	}
	if sub == "fetch" || sub == "push" || sub == "ls-remote" {
		r.mu.Lock()
		down, hold := r.down, r.hold
		if sub == "fetch" {
			r.fetches++
			r.during++
			r.max = max(r.max, r.during)
		}
		r.mu.Unlock()
		if sub == "fetch" {
			defer func() { r.mu.Lock(); r.during--; r.mu.Unlock() }()
			if hold != nil {
				r.mu.Lock()
				entered := r.entered
				r.mu.Unlock()
				if entered != nil {
					entered <- struct{}{}
				}
				select { // a git that hangs is killed when its context ends
				case <-hold:
				case <-c.Done():
					return "", "fatal: the command was stopped", c.Err()
				}
			}
		}
		if down {
			return "", "fatal: unable to access 'https://example.test/x.git/': Could not resolve host: example.test", errors.New("exit status 128")
		}
	}
	return gitstore.RunReal(c, cmd)
}

func (r *runner) fetchCount() int { r.mu.Lock(); defer r.mu.Unlock(); return r.fetches }
func (r *runner) setDown(v bool)  { r.mu.Lock(); r.down = v; r.mu.Unlock() }

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type rm struct {
	t    *testing.T
	m    *Manager
	st   *store.Store
	r    *runner
	clk  *fakeClock
	user store.User
	dir  string
}

func remoteManager(t *testing.T, st *store.Store, mod ...func(*Options)) *rm {
	t.Helper()
	needGit(t)
	base, err := os.MkdirTemp("", "petalbook-remote-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	f := &rm{t: t, st: st, r: &runner{}, clk: &fakeClock{t: time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)}, dir: filepath.Join(base, "data")}
	f.m = f.open(mod...)
	f.user = addUser(t, st, "U1", "kazuha")
	return f
}

func (f *rm) open(mod ...func(*Options)) *Manager {
	opt := Options{Store: f.st, DataDir: f.dir, Committer: "Svc <svc@example.test>", CloseWait: 200 * time.Millisecond, CommitIdle: time.Hour, CommitMax: time.Hour,
		Now: f.clk.Now, PullInterval: time.Minute, RemoteRunner: f.r, FetchFresh: time.Nanosecond, FailFast: time.Nanosecond,
		RemotePolicy: gitstore.Policy{Hosts: []string{"*"}, AllowUnsafe: true}}
	for _, m := range mod {
		m(&opt)
	}
	m, err := NewAccounts(opt)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { m.Close() })
	return m
}

func (f *rm) space(name string) (Info, *Handle) {
	f.t.Helper()
	in, err := f.m.Create(ctx, f.user.ID, name)
	if err != nil {
		f.t.Fatal(err)
	}
	h, err := f.m.Open(ctx, f.user, in.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(h.Release)
	return in, h
}

func bare(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "remote.git")
	os.MkdirAll(dir, 0o755)
	runGit(t, dir, "init", "--bare", "-q", "-b", "main")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Other", "-c", "user.email=o@example.test"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// push puts a file on the remote as somebody else.
func push(t *testing.T, remote, rel, content string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "other")
	runGit(t, t.TempDir(), "clone", "-q", remote, dir)
	runGit(t, dir, "checkout", "-q", "-B", "main")
	p := filepath.Join(dir, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(content), 0o644)
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-qm", "other: "+rel)
	runGit(t, dir, "push", "-q", "origin", "main")
}

func pageFile(id, title, body string) string {
	return "---\nid: " + id + "\ntitle: " + title + "\n---\n" + body
}

func tokenReq(remote string) ConnectRequest {
	return ConnectRequest{URL: remote, Branch: "main", CredType: CredToken, Token: "ghp_S3cretToken1234567890"}
}

func TestConnectStoresTheCredentialEncryptedAndReopensAsARemoteSpace(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := remoteManager(t, st)
		in, h := f.space("Notes")
		if in.Sync != "local" {
			t.Fatalf("%+v", in)
		}
		remote := bare(t)
		v, err := f.m.Connect(ctx, h, tokenReq(remote))
		if err != nil {
			t.Fatal(err)
		}
		if v.URL != remote || v.Branch != "main" || v.CredType != CredToken || v.Username != "x-access-token" || v.State != "ok" || v.LastSync.IsZero() {
			t.Fatalf("%+v", v)
		}
		// The token is encrypted in the database and the key is in a private file.
		row, err := store.GetRemote(ctx, st.Q(), in.ID)
		if err != nil || row.TokenEnc == "" || strings.Contains(row.TokenEnc, "ghp_S3cret") || !row.Connected() {
			t.Fatalf("%+v %v", row, err)
		}
		if fi, err := os.Stat(filepath.Join(f.dir, secret.FileName)); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("secret.key: %v %v", fi, err)
		}
		box := secret.NewBox(f.dir)
		if b, err := box.Open(in.ID, "token", row.TokenEnc); err != nil || string(b) != "ghp_S3cretToken1234567890" {
			t.Fatalf("%q %v", b, err)
		}
		if _, err := box.Open("OTHERSPACE", "token", row.TokenEnc); err == nil {
			t.Fatal("the token decrypts for another space")
		}
		// It lists as connected, with the remote's state.
		if l, _ := f.m.List(ctx, f.user.ID); l[0].ID != in.ID || l[0].Sync != "ok" {
			t.Fatalf("%+v", l)
		}
		// A page made now is pushed; then the server restarts.
		p, err := h.Space.Create(space.Author{}, space.CreateInput{Meta: page.Meta{{Key: "title", Value: "Pushed"}}})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(runGit(t, remote, "ls-tree", "-r", "--name-only", "main"), "Pushed.md") {
			t.Fatal("not pushed")
		}
		h.Release()
		f.m.Close()

		m2 := f.open()
		h2, err := m2.Open(ctx, f.user, in.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer h2.Release()
		if _, ok := h2.Space.Remote(); !ok {
			t.Fatal("the space did not reopen as a remote space")
		}
		if got, err := h2.Space.Get(p.ID); err != nil || got.Meta.Title() != "Pushed" {
			t.Fatalf("%v %v", got, err)
		}
		// Changes from outside arrive through the puller, with no request in between.
		push(t, remote, "Outside.md", pageFile("EXTRN00001", "Outside", "o\n"))
		if err := m2.PullNow(ctx, in.ID); err != nil {
			t.Fatal(err)
		}
		if got, err := h2.Space.Get("EXTRN00001"); err != nil || got.Body != "o\n" {
			t.Fatalf("%v %v", got, err)
		}
	})
}

func TestSSHKeyBeforeConnectingAndRotation(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := remoteManager(t, st)
		in, h := f.space("Keys")
		pub, fp, err := f.m.SSHKey(ctx, h, false)
		if err != nil || !strings.HasPrefix(pub, "ssh-ed25519 ") || !strings.HasSuffix(pub, "petalbook-"+in.ID) || !strings.HasPrefix(fp, "SHA256:") {
			t.Fatalf("%q %q %v", pub, fp, err)
		}
		if got, got2, _ := f.m.SSHKey(ctx, h, false); got != pub || got2 != fp {
			t.Fatal("a second call made another key")
		}
		row, _ := store.GetRemote(ctx, st.Q(), in.ID)
		if row.Connected() || strings.Contains(row.PrivKeyEnc, "PRIVATE") || row.PrivKeyEnc == "" {
			t.Fatalf("%+v", row)
		}
		// A key-only space is still local.
		if _, err := f.m.Remote(ctx, h); !errors.Is(err, ErrNoRemote) {
			t.Fatalf("%v", err)
		}
		if l, _ := f.m.List(ctx, f.user.ID); l[0].Sync != "local" {
			t.Fatalf("%+v", l)
		}
		pub2, _, err := f.m.SSHKey(ctx, h, true)
		if err != nil || pub2 == pub {
			t.Fatalf("rotate: %q %v", pub2, err)
		}
		// An ssh address with the key that was made; the network is down (nothing is
		// reached: the runner answers for it), so it fails, and nothing changes.
		f.r.setDown(true)
		_, err = f.m.Connect(ctx, h, ConnectRequest{URL: "git@example.test:a/b.git", CredType: CredSSH})
		f.r.setDown(false)
		var ue *space.UnavailableError
		if !errors.As(err, &ue) {
			t.Fatalf("%v", err)
		}
		if _, err := f.m.Remote(ctx, h); !errors.Is(err, ErrNoRemote) {
			t.Fatalf("a failed connection left a remote: %v", err)
		}
		if pub3, _, _ := f.m.SSHKey(ctx, h, false); pub3 != pub2 {
			t.Fatal("the key was lost by a failed connection")
		}
		// Rotating is for local spaces only.
		if _, err := f.m.Connect(ctx, h, tokenReq(bare(t))); err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.m.SSHKey(ctx, h, true); !errors.Is(err, ErrConnected) {
			t.Fatalf("%v", err)
		}
	})
}

func TestConnectValidation(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := remoteManager(t, st, func(o *Options) { o.RemotePolicy = gitstore.Policy{Hosts: gitstore.DefaultHosts} })
		_, h := f.space("V")
		var ue *gitstore.URLError
		var he *gitstore.HostError
		for _, c := range []struct {
			req  ConnectRequest
			want any
		}{
			{ConnectRequest{URL: "git://github.com/a/b", CredType: CredToken, Token: "x"}, &ue},
			{ConnectRequest{URL: "https://user:pw@github.com/a/b", CredType: CredToken, Token: "x"}, &ue},
			{ConnectRequest{URL: "/etc", CredType: CredToken, Token: "x"}, &ue},
			{ConnectRequest{URL: "https://example.com/a/b", CredType: CredToken, Token: "x"}, &he},
		} {
			err := func() error { _, err := f.m.Connect(ctx, h, c.req); return err }()
			if !errors.As(err, c.want) {
				t.Errorf("%+v: %v", c.req, err)
			}
		}
		for _, c := range []ConnectRequest{
			{URL: "https://github.com/a/b", CredType: CredToken},                              // no token
			{URL: "https://github.com/a/b", CredType: "password", Token: "x"},                 // unknown type
			{URL: "https://github.com/a/b", CredType: CredSSH},                                // ssh key with an https address
			{URL: "git@github.com:a/b", CredType: CredToken, Token: "x"},                      // token with an ssh address
			{URL: "https://github.com/a/b", Branch: "-x", CredType: CredToken, Token: "x"},    // bad branch
			{URL: "https://github.com/a/b", Branch: "a..b", CredType: CredToken, Token: "x"},  // bad branch
			{URL: "https://github.com/a/b", CredType: CredToken, Token: "a\nb"},               // bad token
			{URL: "https://github.com/a/b", CredType: CredToken, Username: "a b", Token: "x"}, // bad user name
			{URL: "", CredType: CredToken, Token: "x"},
		} {
			if _, err := f.m.Connect(ctx, h, c); err == nil || (!errors.Is(err, ErrInvalid) && !errors.As(err, &ue)) {
				t.Errorf("%+v: %v", c, err)
			}
		}
		if _, err := f.m.Remote(ctx, h); !errors.Is(err, ErrNoRemote) {
			t.Fatalf("a refused request left a remote: %v", err)
		}
	})
}

func TestReconnectingKeepsTheStoredTokenAndDisconnectDeletesEverything(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := remoteManager(t, st)
		in, h := f.space("T")
		remote := bare(t)
		if _, err := f.m.Connect(ctx, h, tokenReq(remote)); err != nil {
			t.Fatal(err)
		}
		before, _ := store.GetRemote(ctx, st.Q(), in.ID)
		// Same address and branch, no token given: the stored one is kept.
		if _, err := f.m.Connect(ctx, h, ConnectRequest{URL: remote, CredType: CredToken, Username: "someone"}); err != nil {
			t.Fatal(err)
		}
		after, _ := store.GetRemote(ctx, st.Q(), in.ID)
		if after.TokenEnc != before.TokenEnc || after.Username != "someone" {
			t.Fatalf("%+v", after)
		}
		v, err := f.m.Remote(ctx, h)
		if err != nil || v.Username != "someone" {
			t.Fatalf("%+v %v", v, err)
		}
		if err := f.m.Disconnect(ctx, h); err != nil {
			t.Fatal(err)
		}
		if _, err := store.GetRemote(ctx, st.Q(), in.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("the credential is still stored: %v", err)
		}
		if l, _ := f.m.List(ctx, f.user.ID); l[0].Sync != "local" {
			t.Fatalf("%+v", l)
		}
		if _, ok := h.Space.Remote(); ok {
			t.Fatal("still remote")
		}
		// Disconnecting a local space is fine.
		if err := f.m.Disconnect(ctx, h); err != nil {
			t.Fatal(err)
		}
	})
}

func TestSyncStateInTheListFollowsTheRemote(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := remoteManager(t, st)
		in, h := f.space("S")
		remote := bare(t)
		if _, err := f.m.Connect(ctx, h, tokenReq(remote)); err != nil {
			t.Fatal(err)
		}
		state := func() string {
			l, err := f.m.List(ctx, f.user.ID)
			if err != nil {
				t.Fatal(err)
			}
			return l[0].Sync
		}
		if state() != "ok" {
			t.Fatal(state())
		}
		f.r.setDown(true)
		v, err := f.m.Sync(ctx, h) // a remote that cannot be reached is not an error of the call
		if err != nil || v.State != "offline" || v.Error == "" {
			t.Fatalf("%+v %v", v, err)
		}
		if state() != "offline" {
			t.Fatal(state())
		}
		row, _ := store.GetRemote(ctx, st.Q(), in.ID)
		if row.State != "offline" {
			t.Fatalf("not stored: %+v", row)
		}
		f.r.setDown(false)
		if v, err = f.m.Sync(ctx, h); err != nil || v.State != "ok" || v.Error != "" {
			t.Fatalf("%+v %v", v, err)
		}
		if state() != "ok" {
			t.Fatal(state())
		}
	})
}

func TestRewrittenHistoryThroughTheManager(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := remoteManager(t, st)
		_, h := f.space("R")
		remote := bare(t)
		if _, err := f.m.Connect(ctx, h, tokenReq(remote)); err != nil {
			t.Fatal(err)
		}
		p, _ := h.Space.Create(space.Author{}, space.CreateInput{Meta: page.Meta{{Key: "title", Value: "Old"}}})
		other := filepath.Join(t.TempDir(), "o")
		runGit(t, t.TempDir(), "clone", "-q", remote, other)
		runGit(t, other, "checkout", "-q", "--orphan", "fresh")
		runGit(t, other, "rm", "-rfq", ".")
		os.WriteFile(filepath.Join(other, "New.md"), []byte(pageFile("NEWER00001", "New", "n\n")), 0o644)
		runGit(t, other, "add", "-A")
		runGit(t, other, "commit", "-qm", "rewritten")
		runGit(t, other, "push", "-q", "--force", "origin", "fresh:main")
		tip := runGit(t, remote, "rev-parse", "main")

		v, err := f.m.Sync(ctx, h)
		if err != nil || v.State != "rewritten" || v.Rewritten == nil || v.Rewritten.Remote != tip || v.Rewritten.LastGood == "" {
			t.Fatalf("%+v %v", v, err)
		}
		if l, _ := f.m.List(ctx, f.user.ID); l[0].Sync != "rewritten" {
			t.Fatal(l[0].Sync)
		}
		if _, err := h.Space.Create(space.Author{}, space.CreateInput{Meta: page.Meta{{Key: "title", Value: "Nope"}}}); err == nil {
			t.Fatal("a change was accepted while paused")
		}
		if got, err := h.Space.Get(p.ID); err != nil || got.Meta.Title() != "Old" {
			t.Fatalf("reads must keep working: %v %v", got, err)
		}
		if _, err := f.m.Accept(ctx, h, runGit(t, remote, "rev-parse", "HEAD~0")[:7]); err != nil { // a prefix of the tip is enough
			t.Fatal(err)
		}
		if v, _ := f.m.Remote(ctx, h); v.State != "ok" || v.Rewritten != nil {
			t.Fatalf("%+v", v)
		}
		row, _ := store.GetRemote(ctx, st.Q(), h.Info.ID)
		if row.State != "ok" || row.RemoteTip != "" || row.LastGood != tip {
			t.Fatalf("%+v", row)
		}
	})
}

// ---- the puller ------------------------------------------------------------------------

func TestPullerPullsOpenSpacesOnlyAndNeverCountsAsUse(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := remoteManager(t, st, func(o *Options) { o.IdleAfter = 10 * time.Minute })
		in, h := f.space("P")
		remote := bare(t)
		if _, err := f.m.Connect(ctx, h, tokenReq(remote)); err != nil {
			t.Fatal(err)
		}
		_, hl := f.space("Local only") // a local space is never pulled
		_ = hl
		h.Release()
		hl.Release()
		e := func() *opening { f.m.mu.Lock(); defer f.m.mu.Unlock(); return f.m.open[in.ID] }

		// Opening a space with a remote makes it due at once. (Connect did not open it again.)
		oe := e()
		f.m.mu.Lock()
		oe.nextPull = f.clk.Now()
		f.m.mu.Unlock()
		n := f.r.fetchCount()
		f.m.pullDue(ctx)
		if f.r.fetchCount() != n+1 {
			t.Fatalf("%d fetches", f.r.fetchCount()-n)
		}
		// Not due again until about an interval later (within jitter).
		f.m.pullDue(ctx)
		f.clk.Advance(40 * time.Second)
		f.m.pullDue(ctx)
		if f.r.fetchCount() != n+1 {
			t.Fatalf("pulled before its time: %d", f.r.fetchCount()-n)
		}
		f.clk.Advance(40 * time.Second) // 80 s in all: past any jitter of a 60 s interval (48 to 72 s)
		f.m.pullDue(ctx)
		if f.r.fetchCount() != n+2 {
			t.Fatalf("not pulled when due: %d", f.r.fetchCount()-n)
		}
		// A pull is not use: the space goes idle and closes, and a closed space is not pulled.
		last := e().last
		f.clk.Advance(15 * time.Minute)
		f.m.pullDue(ctx)
		if e() != nil && !e().last.Equal(last) {
			t.Fatal("a pull counted as use")
		}
		f.m.Trim()
		if f.m.OpenCount() != 0 {
			t.Fatalf("%d spaces still open", f.m.OpenCount())
		}
		n = f.r.fetchCount()
		f.clk.Advance(time.Hour)
		f.m.pullDue(ctx)
		if f.r.fetchCount() != n {
			t.Fatal("a closed space was pulled")
		}
		// Opening it again pulls it, once, soon.
		h2, err := f.m.Open(ctx, f.user, in.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer h2.Release()
		f.m.pullDue(ctx)
		if f.r.fetchCount() != n+1 {
			t.Fatalf("an opened space was not pulled: %d", f.r.fetchCount()-n)
		}
	})
}

func TestPullerRunsAFewAtATime(t *testing.T) {
	t.Parallel()
	st := storetest.Open(t, "sqlite")
	f := remoteManager(t, st, func(o *Options) { o.MaxSpaces = 0 })
	const n = 12
	var ids []string
	for i := 0; i < n; i++ {
		in, h := f.space(fmt.Sprint("S", i))
		if _, err := f.m.Connect(ctx, h, tokenReq(bare(t))); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, in.ID)
		h.Release()
	}
	f.m.mu.Lock()
	for _, id := range ids {
		f.m.open[id].nextPull = f.clk.Now()
	}
	f.m.mu.Unlock()
	f.r.mu.Lock()
	f.r.hold = make(chan struct{})
	f.r.entered = make(chan struct{}, n)
	entered := f.r.entered
	f.r.fetches, f.r.max = 0, 0
	f.r.mu.Unlock()
	done := make(chan struct{})
	go func() { f.m.pullDue(ctx); close(done) }()
	// Wait until the workers are all inside a fetch, then let them go.
	for i := 0; i < pullWorkers; i++ {
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatalf("only %d fetches started", i)
		}
	}
	select {
	case <-entered:
		t.Fatal("more fetches started than the puller has workers")
	case <-time.After(100 * time.Millisecond): // a negative needs a moment of waiting
	}
	f.r.mu.Lock()
	close(f.r.hold)
	f.r.hold = nil
	f.r.mu.Unlock()
	<-done
	f.r.mu.Lock()
	defer f.r.mu.Unlock()
	if f.r.max != pullWorkers || f.r.fetches != n {
		t.Fatalf("max %d at once (want %d), %d fetches (want %d)", f.r.max, pullWorkers, f.r.fetches, n)
	}
}

func TestPullUpdatesIndexOfAnOpenSpaceWithoutARequest(t *testing.T) {
	t.Parallel()
	st := storetest.Open(t, "sqlite")
	f := remoteManager(t, st)
	in, h := f.space("Q")
	remote := bare(t)
	if _, err := f.m.Connect(ctx, h, tokenReq(remote)); err != nil {
		t.Fatal(err)
	}
	f.m.mu.Lock()
	f.m.open[in.ID].nextPull = f.clk.Now()
	f.m.mu.Unlock()
	push(t, remote, "FromGitHub.md", pageFile("GTHBX00001", "From GitHub", "hello\n"))
	f.m.pullDue(ctx)
	res, err := h.Space.List(index.Query{Limit: 10})
	if err != nil || len(res.Items) != 1 || res.Items[0].Title != "From GitHub" {
		t.Fatalf("%+v %v", res, err)
	}
}
