package space

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"petalbook.app/server/internal/gitstore"
)

// queued is a remote space with change A pushed and held right after its
// push went through, and change B committed and waiting for the next push.
type queued struct {
	t     *testing.T
	f     *rfx
	a, b  *Page
	hold  chan struct{}
	aDone chan error
	bDone chan error
	pa    atomic.Pointer[Page] // A as saved
	// release lets A's push return.
	release func()
}

func queueBehindAPush(t *testing.T) *queued {
	t.Helper()
	f := newRemoteSpace(t)
	q := &queued{t: t, f: f, hold: make(chan struct{}), aDone: make(chan error, 1), bDone: make(chan error, 1)}
	q.a = mustCreate(t, f.s, "", meta("title", "A"), "a1\n")
	q.b = mustCreate(t, f.s, "", meta("title", "B"), "b1\n")
	entered := make(chan struct{})
	var once, rel sync.Once
	t.Cleanup(func() { rel.Do(func() { close(q.hold) }) })
	q.release = func() { rel.Do(func() { close(q.hold) }) }
	f.net.mu.Lock()
	f.net.afterPush = func() { once.Do(func() { close(entered); <-q.hold }) }
	f.net.mu.Unlock()
	go func() {
		p, err := f.s.Save(Author{}, q.a.ID, q.a.Rev, meta("title", "A"), "a2\n")
		q.pa.Store(p)
		q.aDone <- err
	}()
	<-entered // A is on the remote; its push has not returned yet
	go func() {
		_, err := f.s.Save(Author{}, q.b.ID, q.b.Rev, meta("title", "B"), "b2\n")
		q.bDone <- err
	}()
	deadline := time.Now().Add(10 * time.Second)
	for git(t, f.root, "rev-list", "--count", "HEAD", "--not", "--remotes") != "1" {
		if time.Now().After(deadline) {
			t.Fatal("B was not committed while A's push ran")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return q
}

// clone is what can change in the clone: HEAD and the files git sees as changed.
func (q *queued) clone() string {
	return git(q.t, q.f.root, "rev-parse", "HEAD") + "\n" + git(q.t, q.f.root, "status", "--porcelain")
}

// act runs op while B waits for its push, then lets A's push return. It
// returns op's error and the clone as op left it.
func (q *queued) act(op func() error) (error, string) {
	done := make(chan error, 1)
	go func() { done <- op() }()
	time.Sleep(50 * time.Millisecond) // op is waiting for the space
	q.release()
	select {
	case err := <-done:
		return err, q.clone()
	case <-time.After(20 * time.Second):
		q.t.Fatal("the operation did not return")
	}
	return nil, ""
}

// answer is B's answer.
func (q *queued) answer() error {
	select {
	case err := <-q.bDone:
		return err
	case <-time.After(20 * time.Second):
		q.t.Fatal("B was not answered")
	}
	return nil
}

func (q *queued) remoteHasB(remote string) bool {
	return containsLine(git(q.t, remote, "show", "main:"+q.b.Path), "b2")
}

// Disconnecting while a change waits for its push: that change goes out
// first, through the remote it was made for, and nothing of it is taken back
// afterwards; a local commit made after the disconnect stays.
func TestDisconnectWithAChangeQueuedPushesItFirstAndTakesNothingBackAfter(t *testing.T) {
	t.Parallel()
	for i := 0; i < 8; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			q := queueBehindAPush(t)
			var late atomic.Bool
			q.f.net.mu.Lock()
			q.f.net.beforePush = func() {
				if q.f.s.rem.Load() == nil {
					late.Store(true)
				}
			}
			q.f.net.mu.Unlock()
			err, after := q.act(q.f.s.Disconnect)
			if err != nil {
				t.Fatal(err)
			}
			if err := <-q.aDone; err != nil {
				t.Fatal(err)
			}
			// A local change, committed now that the space is local.
			pa := q.pa.Load()
			if _, err := q.f.s.Save(Author{}, pa.ID, pa.Rev, meta("title", "A"), "local\n"); err != nil {
				t.Fatal(err)
			}
			if err := q.f.s.Flush(); err != nil {
				t.Fatal(err)
			}
			local := git(t, q.f.root, "rev-parse", "HEAD")
			if local == strings.SplitN(after, "\n", 2)[0] {
				t.Fatal("the local change was not committed")
			}
			bErr := q.answer()
			time.Sleep(50 * time.Millisecond)
			if late.Load() {
				t.Error("a push ran through the remote after it was disconnected")
			}
			if bErr != nil {
				t.Errorf("B: %v", bErr)
			} else if !q.remoteHasB(q.f.remote) {
				t.Error("B was answered but is not on the remote")
			}
			if h := git(t, q.f.root, "rev-parse", "HEAD"); h != local {
				t.Errorf("HEAD moved from the local commit %s to %s", local, h)
			}
			if got := readDisk(t, q.f.s, pa.Path); !strings.HasSuffix(got, "local\n") {
				t.Errorf("the local change is gone:\n%s", got)
			}
			if st := git(t, q.f.root, "status", "--porcelain"); st != "" {
				t.Errorf("the clone: %q", st)
			}
		})
	}
}

// Accepting a rewritten history while a change waits for its push: the
// change is settled first (here: refused, the remote went another way), and
// nothing of it is put back over the accepted history.
func TestAcceptWithAChangeQueuedIsNotUndoneAfterwards(t *testing.T) {
	t.Parallel()
	for i := 0; i < 8; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			q := queueBehindAPush(t)
			o := q.f.theirs()
			gitO(t, o.dir, "checkout", "-q", "--orphan", "fresh")
			gitO(t, o.dir, "rm", "-rfq", ".")
			o.write("Replacement.md", pageText("REPAACE001", "Replacement", "new\n"))
			gitO(t, o.dir, "add", "-A")
			gitO(t, o.dir, "commit", "-qm", "new history")
			gitO(t, o.dir, "push", "-q", "--force", "origin", "fresh:main")
			newTip := q.f.tip()
			err, after := q.act(func() error { return q.f.s.Accept(context.Background(), newTip) })
			<-q.aDone
			q.answer()
			time.Sleep(50 * time.Millisecond)
			if err != nil {
				t.Fatalf("accept: %v", err)
			}
			if now := q.clone(); now != after {
				t.Errorf("the clone changed after the accept:\n%s\nnow\n%s", after, now)
			}
			if h := git(t, q.f.root, "rev-parse", "HEAD"); h != newTip {
				t.Errorf("HEAD %s, the accepted history %s", h, newTip)
			}
			if st := git(t, q.f.root, "status", "--porcelain"); st != "" {
				t.Errorf("the clone: %q", st)
			}
			consistent(t, q.f.s)
		})
	}
}

// Connecting to another remote while a change waits for its push: the
// change goes out to the remote it was made for, before the space moves on.
func TestConnectWithAChangeQueuedPushesItToTheOldRemoteFirst(t *testing.T) {
	t.Parallel()
	for i := 0; i < 8; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			q := queueBehindAPush(t)
			old := q.f.s.rem.Load()
			var late atomic.Bool
			q.f.net.mu.Lock()
			q.f.net.beforePush = func() {
				if r := q.f.s.rem.Load(); r != nil && r != old {
					late.Store(true)
				}
			}
			q.f.net.mu.Unlock()
			oldRemote := q.f.remote
			next := bareRepo(t)
			o := q.f.options()
			o.URL = next
			err, after := q.act(func() error { return q.f.s.Connect(context.Background(), o) })
			if err != nil {
				t.Fatal(err)
			}
			<-q.aDone
			bErr := q.answer()
			time.Sleep(50 * time.Millisecond)
			if bErr != nil {
				t.Errorf("B: %v", bErr)
			} else if !q.remoteHasB(oldRemote) {
				t.Error("B was answered but is not on the remote it was made for")
			}
			if late.Load() {
				t.Error("a push of the old remote's change ran after the connection")
			}
			if now := q.clone(); now != after {
				t.Errorf("the clone changed after connecting:\n%s\nnow\n%s", after, now)
			}
			if h, tip := git(t, q.f.root, "rev-parse", "HEAD"), git(t, next, "rev-parse", "main"); h != tip {
				t.Errorf("HEAD %s, the new remote %s", h, tip)
			}
		})
	}
}

// Closing while a change waits for its push: the change is settled before
// Close returns, and nothing touches the clone afterwards.
func TestCloseWithAChangeQueuedSettlesItFirst(t *testing.T) {
	t.Parallel()
	for i := 0; i < 8; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			q := queueBehindAPush(t)
			var ran atomic.Int64 // git commands run when Close returned
			err, after := q.act(func() error {
				err := q.f.s.Close()
				q.f.net.mu.Lock()
				ran.Store(int64(len(q.f.net.subs)))
				q.f.net.mu.Unlock()
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			// A's push went through before Close: A is done, and stays.
			if err := <-q.aDone; err != nil {
				t.Errorf("A, on the remote before Close, was answered %v", err)
			}
			q.answer()
			time.Sleep(50 * time.Millisecond)
			q.f.net.mu.Lock()
			late := append([]string{}, q.f.net.subs[ran.Load():]...)
			q.f.net.mu.Unlock()
			if len(late) > 0 {
				t.Errorf("git ran after Close returned: %v", late)
			}
			if now := q.clone(); now != after {
				t.Errorf("the clone changed after Close:\n%s\nnow\n%s", after, now)
			}
			o := q.f.options()
			o.Status = RemoteStatus{LastGood: q.f.tip()}
			q.f.s = openAt(t, q.f.root, func(opt *Options) {
				opt.Committer = "Service <svc@example.test>"
				opt.Author = "Ann <ann@example.test>"
				opt.Remote = &o
			})
			if st := git(t, q.f.root, "status", "--porcelain"); st != "" {
				t.Errorf("the clone: %q", st)
			}
			consistent(t, q.f.s)
		})
	}
}

// The change that started a push is answered at that push while writers
// keep coming: it does not wait for them.
func TestTheFirstChangeIsAnsweredWhileWritersKeepComing(t *testing.T) {
	t.Parallel()
	f := newRemoteSpace(t)
	var pages []*Page
	for i := 0; i < 9; i++ {
		pages = append(pages, mustCreate(t, f.s, "", meta("title", fmt.Sprintf("P%d", i)), "v0\n"))
	}
	hold, entered := make(chan struct{}), make(chan struct{})
	var once, rel sync.Once
	release := func() { rel.Do(func() { close(hold) }) }
	t.Cleanup(release)
	f.net.mu.Lock()
	f.net.beforePush = func() { once.Do(func() { close(entered); <-hold }) }
	f.net.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, err := f.s.Save(Author{}, pages[0].ID, pages[0].Rev, meta("title", "P0"), "mine\n")
		done <- err
	}()
	<-entered
	var stop atomic.Bool
	var wg sync.WaitGroup
	var made atomic.Int64
	for w := 1; w < 9; w++ {
		wg.Add(1)
		go func(p *Page) {
			defer wg.Done()
			for i := 0; !stop.Load(); i++ {
				cur, err := f.s.Get(p.ID)
				if err != nil {
					return
				}
				if _, err := f.s.Save(Author{}, p.ID, cur.Rev, meta("title", p.Meta.Title()), fmt.Sprintf("v%d\n", i)); err == nil {
					made.Add(1)
				}
			}
		}(pages[w])
	}
	time.Sleep(100 * time.Millisecond) // the writers queue behind the held push
	release()
	start := time.Now()
	var err error
	select {
	case err = <-done:
	case <-time.After(3 * time.Second):
	}
	took := time.Since(start)
	stop.Store(true)
	wg.Wait()
	if took >= 3*time.Second {
		<-done
		t.Fatalf("the first change was not answered in %v while writers kept coming (%d of theirs made)", took, made.Load())
	}
	if err != nil {
		t.Fatal(err)
	}
}

// killAtUpdateRef runs git like the netRunner it wraps, except for the first
// move of HEAD once armed: Close is started there, and if the command is then
// run under a cancelled context, it ends the way a kill at the worst moment
// ends it (the runner kills git when the context is cancelled): HEAD has
// moved, and HEAD.lock, which git removes only after that, is left behind.
type killAtUpdateRef struct {
	*netRunner
	armed atomic.Bool
	fired atomic.Bool
	close func()
	ctxOf func() context.Context // the space's own context
	root  string
}

func (k *killAtUpdateRef) Run(ctx context.Context, c gitstore.Cmd) (string, string, error) {
	sub, args := gitstore.Subcommand(c.Args)
	if sub != "update-ref" || len(args) == 0 || args[0] != "HEAD" || !k.armed.Load() || !k.fired.CompareAndSwap(false, true) {
		return k.netRunner.Run(ctx, c)
	}
	go k.close()
	for k.ctxOf().Err() == nil { // Close has begun: the space's context is cancelled
		time.Sleep(time.Millisecond)
	}
	if ctx.Err() == nil {
		return k.netRunner.Run(ctx, c)
	}
	out, errOut, err := k.netRunner.Run(context.Background(), c)
	if err != nil {
		return out, errOut, err
	}
	if err := os.WriteFile(filepath.Join(k.root, ".git", "HEAD.lock"), nil, 0o644); err != nil {
		return out, errOut, err
	}
	return "", "", ctx.Err()
}

// Closing while a change is being committed: the commit is not cut short
// half way, so that nothing of it is left behind that the change's undo, or
// the next start, cannot take back. The test above caught Close killing git
// between moving HEAD and removing HEAD.lock: the undo could not move HEAD
// back, the clone kept an unpushed commit with the index of an older one
// ("MM"), and the space opened again refusing changes.
func TestCloseWhileAChangeIsCommittedLeavesTheCloneClean(t *testing.T) {
	t.Parallel()
	k := &killAtUpdateRef{netRunner: &netRunner{}}
	f := newRemoteSpace(t, func(o *RemoteOptions) { o.Runner = k })
	k.root = f.root
	s := f.s
	k.ctxOf = func() context.Context { return s.ctx }
	closed := make(chan error, 1)
	k.close = func() { closed <- s.Close() }
	a := mustCreate(t, s, "", meta("title", "A"), "a1\n")
	tip := f.tip()
	k.armed.Store(true)
	if _, err := s.Save(Author{}, a.ID, a.Rev, meta("title", "A"), "a2\n"); err == nil {
		t.Error("the change was answered as done, though Close cut off its push")
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if !k.fired.Load() {
		t.Fatal("the change never moved HEAD")
	}
	if _, err := os.Stat(filepath.Join(f.root, ".git", "HEAD.lock")); err == nil {
		t.Error("Close left HEAD.lock behind")
	}
	if got := f.tip(); got != tip {
		t.Fatalf("the remote moved: %s, was %s", got, tip)
	}
	if h := git(t, f.root, "rev-parse", "HEAD"); h != tip {
		t.Errorf("HEAD %s after Close, the remote %s", h, tip)
	}
	if st := git(t, f.root, "status", "--porcelain"); st != "" {
		t.Errorf("the clone after Close: %q", st)
	}
	k.armed.Store(false)
	o := f.options()
	o.Status = RemoteStatus{LastGood: tip}
	f.s = openAt(t, f.root, func(opt *Options) {
		opt.Committer = "Service <svc@example.test>"
		opt.Author = "Ann <ann@example.test>"
		opt.Remote = &o
	})
	if st := git(t, f.root, "status", "--porcelain"); st != "" {
		t.Errorf("the clone after opening again: %q", st)
	}
	cur, err := f.s.Get(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Save(Author{}, a.ID, cur.Rev, meta("title", "A"), "a3\n"); err != nil {
		t.Errorf("the space opened again refuses changes: %v", err)
	}
	consistent(t, f.s)
}

// A push whose answer was lost, with Close coming right then: the look at
// the remote that tells it went through is not cut short, so the change is
// answered as done and stays, instead of being taken back from the clone
// while it is on the remote.
func TestCloseAfterAPushWhoseAnswerWasLostAnswersItDone(t *testing.T) {
	t.Parallel()
	f := newRemoteSpace(t)
	s := f.s
	a := mustCreate(t, s, "", meta("title", "A"), "a1\n")
	closed := make(chan error, 1)
	var once sync.Once
	f.net.mu.Lock()
	f.net.losePush = true
	f.net.afterPush = func() {
		once.Do(func() {
			go func() { closed <- s.Close() }()
			select {
			case <-s.ctx.Done(): // Close has begun
			case <-time.After(10 * time.Second):
				t.Error("Close did not begin")
			}
		})
	}
	f.net.mu.Unlock()
	if _, err := s.Save(Author{}, a.ID, a.Rev, meta("title", "A"), "a2\n"); err != nil {
		t.Errorf("the change on the remote was answered %v", err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if h, tip := git(t, f.root, "rev-parse", "HEAD"), f.tip(); h != tip {
		t.Errorf("HEAD %s, the remote %s", h, tip)
	}
	if !containsLine(git(t, f.remote, "show", "main:"+a.Path), "a2") {
		t.Error("the change is not on the remote")
	}
	if st := git(t, f.root, "status", "--porcelain"); st != "" {
		t.Errorf("the clone: %q", st)
	}
}
