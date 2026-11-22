package space

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/gitstore"
	"github.com/KKazuhaK/PetalBook/internal/index"
)

// netRunner is the real git with a switch for the network: while down, every
// command that talks to the remote fails like an unreachable host.
type netRunner struct {
	mu         sync.Mutex
	down       bool
	losePush   bool   // run the push, then report it as failed (the answer was lost)
	beforePush func() // runs before every push
	beforeNet  func(sub string)
	subs       []string
	pushes     [][]string // the arguments of every push
}

func (n *netRunner) setDown(v bool) { n.mu.Lock(); n.down = v; n.mu.Unlock() }

func (n *netRunner) count(sub string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	c := 0
	for _, s := range n.subs {
		if s == sub {
			c++
		}
	}
	return c
}

func (n *netRunner) Run(ctx context.Context, c gitstore.Cmd) (string, string, error) {
	sub, _ := gitstore.Subcommand(c.Args)
	n.mu.Lock()
	n.subs = append(n.subs, sub)
	if sub == "push" {
		n.pushes = append(n.pushes, append([]string{}, c.Args...))
	}
	down, lose, bp, bn := n.down, n.losePush, n.beforePush, n.beforeNet
	n.mu.Unlock()
	net := sub == "fetch" || sub == "push" || sub == "ls-remote"
	if net && bn != nil {
		bn(sub)
	}
	if sub == "push" && bp != nil {
		bp()
		n.mu.Lock()
		down = n.down
		n.mu.Unlock()
	}
	if net && down {
		return "", "fatal: unable to access 'https://example.test/x.git/': Could not resolve host: example.test", errors.New("exit status 128")
	}
	out, errOut, err := gitstore.RunReal(ctx, c)
	if sub == "push" && lose && err == nil {
		return out, "fatal: the remote end hung up unexpectedly", errors.New("exit status 128")
	}
	return out, errOut, err
}

func gitO(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return git(t, dir, append([]string{"-c", "user.name=Other", "-c", "user.email=other@example.test", "-c", "commit.gpgsign=false"}, args...)...)
}

type rfx struct {
	t      *testing.T
	remote string
	root   string
	s      *Space
	net    *netRunner
	mu     sync.Mutex
	sts    []RemoteStatus
	mod    []func(*RemoteOptions)
}

// checkPushes fails the test if any push the space made was not a plain
// fast-forward push of HEAD to the branch.
func (f *rfx) checkPushes() {
	f.net.mu.Lock()
	defer f.net.mu.Unlock()
	for _, a := range f.net.pushes {
		if strings.Join(a, " ") != "push --quiet origin HEAD:refs/heads/main" {
			f.t.Errorf("unexpected push: git %s", strings.Join(a, " "))
		}
	}
}

func (f *rfx) options() RemoteOptions {
	o := RemoteOptions{URL: f.remote, Branch: "main", AllowUnsafe: true, Runner: f.net, FetchFresh: time.Nanosecond, FailFast: time.Nanosecond,
		OnStatus: func(st RemoteStatus) { f.mu.Lock(); f.sts = append(f.sts, st); f.mu.Unlock() }}
	for _, m := range f.mod {
		m(&o)
	}
	return o
}

func (f *rfx) last() RemoteStatus {
	st, _ := f.s.Remote()
	return st
}

func bareRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "remote.git")
	os.MkdirAll(dir, 0o755)
	git(t, dir, "init", "--bare", "-q", "-b", "main")
	return dir
}

// newRemoteSpace opens a local space, makes it follow a new empty bare
// repository, and returns it.
func newRemoteSpace(t *testing.T, mod ...func(*RemoteOptions)) *rfx {
	t.Helper()
	needGit(t)
	f := &rfx{t: t, remote: bareRepo(t), root: filepath.Join(t.TempDir(), "space"), net: &netRunner{}, mod: mod}
	t.Cleanup(f.checkPushes)
	f.s = openAt(t, f.root, func(o *Options) {
		o.Committer = "Service <svc@example.test>"
		o.Author = "Ann <ann@example.test>"
		o.Name = "Notes"
	})
	if err := f.s.Connect(context.Background(), f.options()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	return f
}

// reopen closes the space and opens it again as a remote space.
func (f *rfx) reopen() {
	f.t.Helper()
	f.s.Close()
	o := f.options()
	o.Status = RemoteStatus{LastGood: f.tip()}
	f.s = openAt(f.t, f.root, func(opt *Options) {
		opt.Committer = "Service <svc@example.test>"
		opt.Author = "Ann <ann@example.test>"
		opt.Remote = &o
	})
}

func (f *rfx) tip() string { return git(f.t, f.remote, "rev-parse", "main") }

func (f *rfx) remoteLog() []string {
	out := git(f.t, f.remote, "log", "--format=%s", "main")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// theirs is somebody else's clone of the remote.
type theirs struct {
	t   *testing.T
	dir string
}

func (f *rfx) theirs() *theirs {
	dir := filepath.Join(f.t.TempDir(), "theirs")
	cmd := exec.Command("git", "clone", "-q", f.remote, dir)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
	if out, err := cmd.CombinedOutput(); err != nil {
		f.t.Fatalf("clone: %v\n%s", err, out)
	}
	gitO(f.t, dir, "checkout", "-q", "-B", "main")
	return &theirs{f.t, dir}
}

func (o *theirs) write(rel, content string) {
	o.t.Helper()
	p := filepath.Join(o.dir, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		o.t.Fatal(err)
	}
}

func (o *theirs) push(msg string) {
	o.t.Helper()
	gitO(o.t, o.dir, "add", "-A")
	gitO(o.t, o.dir, "commit", "-qm", msg)
	gitO(o.t, o.dir, "push", "-q", "origin", "main")
}

func pageText(id, title, body string) string {
	return "---\nid: " + id + "\ntitle: " + title + "\n---\n" + body
}

// state is everything that can be observed about a space: the files, the
// index, the folders, the clone's HEAD and what git says is modified.
type state struct {
	Files   map[string]string
	Pages   []string
	Folders []string
	Head    string
	Status  string
}

func snapshot(t *testing.T, s *Space) state {
	t.Helper()
	st := state{Files: map[string]string{}}
	filepath.WalkDir(s.Root(), func(p string, d fs.DirEntry, err error) error {
		rel, _ := filepath.Rel(s.Root(), p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == ".git" || rel == ".petalbook/tmp" || rel == ".petalbook/cache" {
				return filepath.SkipDir
			}
			if rel != "." {
				st.Files[rel+"/"] = ""
			}
			return nil
		}
		b, _ := os.ReadFile(p)
		st.Files[rel] = string(b)
		return nil
	})
	res, err := s.List(index.Query{Recursive: true, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range res.Items {
		st.Pages = append(st.Pages, it.ID+"@"+it.Path+"#"+it.Title)
	}
	sort.Strings(st.Pages)
	tree, _ := s.Tree()
	for _, f := range tree {
		st.Folders = append(st.Folders, fmt.Sprintf("%s:%d", f.Path, f.Pages))
	}
	sort.Strings(st.Folders)
	st.Head = git(t, s.Root(), "rev-parse", "HEAD")
	st.Status = git(t, s.Root(), "status", "--porcelain")
	return st
}

// consistent checks the index against a rebuild of the same files, and that
// git has nothing modified or untracked besides what is expected.
func consistent(t *testing.T, s *Space) {
	t.Helper()
	got := snapshot(t, s)
	if got.Status != "" {
		t.Errorf("git status is not clean:\n%s", got.Status)
	}
	fresh, err := index.Open(filepath.Join(t.TempDir(), "fresh.db"), s.root)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if _, err := fresh.Sync(s.src, true); err != nil {
		t.Fatal(err)
	}
	want, _ := fresh.List(index.Query{Recursive: true, Limit: 200})
	var w []string
	for _, it := range want.Items {
		w = append(w, it.ID+"@"+it.Path+"#"+it.Title)
	}
	sort.Strings(w)
	if !reflect.DeepEqual(w, got.Pages) && !(len(w) == 0 && len(got.Pages) == 0) {
		t.Errorf("the index does not match the files:\n index %v\n files %v", got.Pages, w)
	}
	wtree, _ := fresh.Tree()
	var wf []string
	for _, f := range wtree {
		wf = append(wf, fmt.Sprintf("%s:%d", f.Path, f.Pages))
	}
	sort.Strings(wf)
	// Empty folders that exist only here are on disk too, so the walk sees them.
	if !reflect.DeepEqual(wf, got.Folders) {
		t.Errorf("the folders of the index do not match the files:\n index %v\n files %v", got.Folders, wf)
	}
}

func TestEveryChangeIsPushedBeforeItIsAnswered(t *testing.T) {
	f := newRemoteSpace(t)
	if got := f.remoteLog(); len(got) != 1 {
		t.Fatalf("connecting pushed %v", got)
	}
	p := mustCreate(t, f.s, "Tang", meta("title", "暮雪"), "v1\n")
	if got := f.remoteLog(); len(got) != 2 || got[0] != "Add 暮雪" {
		t.Fatalf("create was not pushed before it returned: %v", got)
	}
	if who := git(t, f.remote, "log", "-1", "--format=%an <%ae>|%cn <%ce>"); who != "Ann <ann@example.test>|Service <svc@example.test>" {
		t.Fatalf("author and committer: %q", who)
	}
	p2, err := f.s.Save(Author{}, p.ID, p.Rev, meta("title", "暮雪"), "v2\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := f.remoteLog(); len(got) != 3 || got[0] != "Edit 暮雪" {
		t.Fatalf("save: %v", got)
	}
	// A rename by a new title is one commit that moves the file.
	p3, err := f.s.Save(Author{}, p.ID, p2.Rev, meta("title", "夜雪"), "v2\n")
	if err != nil || p3.Path != "Tang/夜雪.md" {
		t.Fatalf("%v %v", p3, err)
	}
	if got := f.remoteLog(); len(got) != 4 {
		t.Fatalf("rename: %v", got)
	}
	if _, err := f.s.Move(Author{}, p.ID, "随笔"); err != nil {
		t.Fatal(err)
	}
	if got := f.remoteLog(); len(got) != 5 || got[0] != "Move 夜雪" {
		t.Fatalf("move: %v", got)
	}
	if files := git(t, f.remote, "-c", "core.quotepath=false", "ls-tree", "-r", "--name-only", "main"); !strings.Contains(files, "随笔/夜雪.md") || strings.Contains(files, "Tang") {
		t.Fatalf("remote files:\n%s", files)
	}
	if err := f.s.Delete(Author{}, p.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.remoteLog(); len(got) != 6 || got[0] != "Delete 夜雪" {
		t.Fatalf("delete: %v", got)
	}
	// Nothing is left for the background committer, and the clone is the remote.
	if st := git(t, f.root, "status", "--porcelain"); st != "" {
		t.Fatalf("status: %q", st)
	}
	if git(t, f.root, "rev-parse", "HEAD") != f.tip() {
		t.Fatal("the clone is not at the remote's tip")
	}
	if b, _ := os.ReadFile(filepath.Join(f.s.StateDir(), "pending.journal")); len(b) != 0 {
		t.Fatalf("the journal still holds %q", b)
	}
	if st := f.last(); st.State != RemoteOK || st.LastGood != f.tip() || st.LastSync.IsZero() {
		t.Fatalf("status %+v", st)
	}
	consistent(t, f.s)
}

func TestSpaceFilesOfARemoteSpaceAreTracked(t *testing.T) {
	f := newRemoteSpace(t)
	if files := git(t, f.remote, "-c", "core.quotepath=false", "ls-tree", "-r", "--name-only", "main"); !strings.Contains(files, ".petalbook/space.yml") {
		t.Fatalf("space.yml was not pushed:\n%s", files)
	}
	// Opening again as a remote space makes no file of its own.
	f.reopen()
	if st := git(t, f.root, "status", "--porcelain", "--untracked-files=all"); st != "" {
		t.Fatalf("status after reopening: %q", st)
	}
	if f.s.Name() != "Notes" {
		t.Fatalf("name %q", f.s.Name())
	}
}

func TestRejectedPushIsReplayedOnTheNewTip(t *testing.T) {
	f := newRemoteSpace(t)
	o := f.theirs()
	var once sync.Once
	f.net.beforePush = func() {
		once.Do(func() {
			o.write("theirs.md", pageText("THEMS00001", "Theirs", "x\n"))
			o.push("theirs")
		})
	}
	p := mustCreate(t, f.s, "", meta("title", "Ours"), "mine\n")
	log := f.remoteLog()
	if len(log) != 3 || log[0] != "Add Ours" || log[1] != "theirs" {
		t.Fatalf("remote log %v", log)
	}
	if got, err := f.s.Get("THEMS00001"); err != nil || got.Body != "x\n" {
		t.Fatalf("the other change is not in the space: %v %v", got, err)
	}
	if got, err := f.s.Get(p.ID); err != nil || got.Body != "mine\n" {
		t.Fatal("our page is gone")
	}
	if n := f.net.count("push"); n != 3 { // connecting, the rejected push, the replay
		t.Fatalf("%d pushes", n)
	}
	consistent(t, f.s)
	if git(t, f.root, "rev-parse", "HEAD") != f.tip() {
		t.Fatal("not at the tip")
	}
}

func TestReplayThatConflictsAnswersTheOrdinaryConflict(t *testing.T) {
	f := newRemoteSpace(t)
	p := mustCreate(t, f.s, "", meta("title", "Shared"), "v1\n")
	o := f.theirs()
	var once sync.Once
	f.net.beforePush = func() {
		once.Do(func() {
			o.write(p.Path, pageText(p.ID, "Shared", "their edit\n"))
			o.push("their edit")
		})
	}
	before := snapshot(t, f.s)
	_, err := f.s.Save(Author{}, p.ID, p.Rev, meta("title", "Shared"), "my edit\n")
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("want a conflict, got %v", err)
	}
	if ce.Current.Body != "their edit\n" {
		t.Fatalf("the conflict shows %q", ce.Current.Body)
	}
	// Our change is gone completely; the remote holds only their commit.
	if got := f.remoteLog(); got[0] != "their edit" || len(got) != 3 {
		t.Fatalf("remote log %v", got)
	}
	after := snapshot(t, f.s)
	if after.Files[p.Path] != pageText(p.ID, "Shared", "their edit\n") || strings.Contains(after.Files[p.Path], "my edit") {
		t.Fatalf("the file: %q", after.Files[p.Path])
	}
	if after.Status != "" || after.Head != f.tip() || len(after.Files) != len(before.Files) {
		t.Fatalf("%+v", after)
	}
	consistent(t, f.s)
}

func TestPushWhoseAnswerWasLostIsNotAFailureAndNotRepeated(t *testing.T) {
	f := newRemoteSpace(t)
	f.net.losePush = true
	p, err := f.s.Create(Author{}, CreateInput{Meta: meta("title", "Lost answer"), Body: "x\n"})
	f.net.losePush = false
	if err != nil {
		t.Fatalf("the change landed, so it must succeed: %v", err)
	}
	if got := f.remoteLog(); len(got) != 2 || got[0] != "Add Lost answer" {
		t.Fatalf("remote log %v", got)
	}
	if f.net.count("push") != 2 { // the connect push and this one: no retry
		t.Fatalf("%d pushes", f.net.count("push"))
	}
	if _, err := f.s.Get(p.ID); err != nil {
		t.Fatal(err)
	}
	if st := f.last(); st.LastGood != f.tip() || st.State != RemoteOK {
		t.Fatalf("%+v", st)
	}
	consistent(t, f.s)
}

func TestUnreachableRemoteUndoesTheChangeCompletely(t *testing.T) {
	for _, when := range []string{"before anything", "after the commit"} {
		t.Run(when, func(t *testing.T) {
			f := newRemoteSpace(t)
			a := mustCreate(t, f.s, "A", meta("title", "Alpha"), "a\n")
			b := mustCreate(t, f.s, "", meta("title", "Beta"), "b\n")
			c := mustCreate(t, f.s, "", meta("title", "Gamma"), "c\n")
			if when == "before anything" {
				f.net.setDown(true)
			} else {
				f.net.beforePush = func() { f.net.setDown(true) }
			}
			before := snapshot(t, f.s)
			unavailable := func(what string, err error) {
				t.Helper()
				var ue *UnavailableError
				if !errors.As(err, &ue) {
					t.Fatalf("%s: want unavailable, got %v", what, err)
				}
				if !strings.Contains(ue.Msg, "could not reach the remote") {
					t.Fatalf("%s: message %q", what, ue.Msg)
				}
				now := snapshot(t, f.s)
				if !reflect.DeepEqual(before, now) {
					t.Fatalf("%s changed something:\nbefore %+v\nafter  %+v", what, before, now)
				}
				consistent(t, f.s)
			}
			_, err := f.s.Create(Author{}, CreateInput{Folder: "New/Deep", Meta: meta("title", "Fresh"), Body: "n\n"})
			unavailable("create into a new folder", err)
			_, err = f.s.Save(Author{}, a.ID, a.Rev, meta("title", "Alpha edited"), "a2\n") // content and file name
			unavailable("save with a rename", err)
			_, err = f.s.Move(Author{}, b.ID, "Moved/To/Here")
			unavailable("move into a new folder", err)
			unavailable("delete", f.s.Delete(Author{}, c.ID))
			// Reads kept working all along, and the status says why.
			if p, err := f.s.Get(a.ID); err != nil || p.Body != "a\n" {
				t.Fatal("a read failed while the remote was down")
			}
			if st := f.last(); st.State != RemoteOffline || !strings.Contains(st.Error, "could not reach") {
				t.Fatalf("status %+v", st)
			}
			// When the remote is back the same changes go through.
			f.net.beforePush = nil
			f.net.setDown(false)
			if _, err := f.s.Save(Author{}, a.ID, a.Rev, meta("title", "Alpha edited"), "a2\n"); err != nil {
				t.Fatal(err)
			}
			if st := f.last(); st.State != RemoteOK || st.Error != "" {
				t.Fatalf("status after recovery %+v", st)
			}
			consistent(t, f.s)
		})
	}
}

func TestFetchIsSkippedRightAfterAnotherAndFailuresAreAnsweredFast(t *testing.T) {
	f := newRemoteSpace(t, func(o *RemoteOptions) { o.FetchFresh, o.FailFast = time.Hour, time.Hour })
	for i := 0; i < 3; i++ {
		mustCreate(t, f.s, "", meta("title", fmt.Sprint("P", i)), "x\n")
	}
	if n := f.net.count("fetch"); n > 1 { // the one of connecting at most; none for the three changes
		t.Fatalf("%d fetches for three changes in a row", n)
	}
	f.net.setDown(true)
	f.net.beforePush = nil
	f.s.Pull(context.Background(), true)
	n := f.net.count("fetch")
	for i := 0; i < 3; i++ {
		if _, err := f.s.Create(Author{}, CreateInput{Meta: meta("title", "x")}); err == nil {
			t.Fatal("no error")
		}
	}
	// The first failure is remembered: nothing further is tried for an hour.
	if f.net.count("fetch") > n+1 {
		t.Fatalf("%d fetches while the remote is known to be down", f.net.count("fetch")-n)
	}
}

func TestRewrittenHistoryPausesChangesAndAcceptingResumesThem(t *testing.T) {
	f := newRemoteSpace(t)
	p := mustCreate(t, f.s, "", meta("title", "Kept"), "old\n")
	o := f.theirs()
	gitO(t, o.dir, "checkout", "-q", "--orphan", "fresh")
	gitO(t, o.dir, "rm", "-rfq", ".")
	o.write("Replacement.md", pageText("REPAACE001", "Replacement", "new\n"))
	gitO(t, o.dir, "add", "-A")
	gitO(t, o.dir, "commit", "-qm", "new history")
	gitO(t, o.dir, "push", "-q", "--force", "origin", "fresh:main")
	newTip := f.tip()
	oldTip := f.last().LastGood

	err := f.s.Pull(context.Background(), true)
	var ue *UnavailableError
	if !errors.As(err, &ue) || !errors.Is(err, gitstore.ErrHistoryRewritten) {
		t.Fatalf("pull: %v", err)
	}
	st := f.last()
	if st.State != RemoteRewritten || st.LastGood != oldTip || st.RemoteTip != newTip {
		t.Fatalf("status %+v", st)
	}
	// Writes are refused; reads keep serving what the server had.
	if _, err := f.s.Create(Author{}, CreateInput{Meta: meta("title", "Nope")}); !errors.As(err, &ue) {
		t.Fatalf("create while paused: %v", err)
	}
	if _, err := f.s.Save(Author{}, p.ID, p.Rev, meta("title", "Kept"), "x\n"); !errors.As(err, &ue) {
		t.Fatalf("save while paused: %v", err)
	}
	if got, err := f.s.Get(p.ID); err != nil || got.Body != "old\n" {
		t.Fatalf("read while paused: %v %v", got, err)
	}
	if _, err := f.s.Get("REPAACE001"); !errors.Is(err, ErrNotFound) {
		t.Fatal("the rewritten content leaked in before it was accepted")
	}
	if res, _ := f.s.List(index.Query{Limit: 10}); len(res.Items) != 1 {
		t.Fatalf("list while paused: %+v", res.Items)
	}
	// Accepting needs exactly the remote's tip.
	if err := f.s.Accept(context.Background(), oldTip); !errors.Is(err, ErrNotTip) {
		t.Fatalf("accept of the wrong commit: %v", err)
	}
	if f.last().State != RemoteRewritten {
		t.Fatal("a refused accept changed the state")
	}
	if err := f.s.Accept(context.Background(), newTip); err != nil {
		t.Fatal(err)
	}
	if st := f.last(); st.State != RemoteOK || st.LastGood != newTip || st.RemoteTip != "" {
		t.Fatalf("status after accept %+v", st)
	}
	if got, err := f.s.Get("REPAACE001"); err != nil || got.Body != "new\n" {
		t.Fatalf("the accepted content: %v %v", got, err)
	}
	if _, err := f.s.Get(p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("the old page is still served")
	}
	if err := f.s.Accept(context.Background(), newTip); !errors.Is(err, ErrNothingToAccept) {
		t.Fatalf("a second accept: %v", err)
	}
	if _, err := f.s.Create(Author{}, CreateInput{Meta: meta("title", "After")}); err != nil {
		t.Fatalf("writes did not resume: %v", err)
	}
	consistent(t, f.s)
}

func TestPullUpdatesTheIndexForExactlyTheChangedFiles(t *testing.T) {
	f := newRemoteSpace(t)
	keep := mustCreate(t, f.s, "k", meta("title", "Keep"), "keep\n")
	edit := mustCreate(t, f.s, "k", meta("title", "Edit"), "e\n")
	del := mustCreate(t, f.s, "gone", meta("title", "Delete"), "d\n")
	ren := mustCreate(t, f.s, "", meta("title", "Rename"), "r\n")
	chg := mustCreate(t, f.s, "", meta("title", "NewID"), "c\n")
	for i := 0; i < 5; i++ {
		mustCreate(t, f.s, "bulk", meta("title", fmt.Sprint("Bulk", i)), "b\n")
	}
	_ = keep

	o := f.theirs()
	o.write(edit.Path, pageText(edit.ID, "Edit", "edited elsewhere\n")) // edited
	os.Remove(filepath.Join(o.dir, del.Path))                           // deleted, and its folder goes with it
	os.MkdirAll(filepath.Join(o.dir, "elsewhere"), 0o755)
	gitO(t, o.dir, "mv", ren.Path, "elsewhere/Renamed.md")        // renamed: same id, new path
	o.write(chg.Path, pageText("CHANGEDXD1", "NewID", "c\n"))     // the file now has another id
	o.write("added/New.md", pageText("ADDEDPAGE1", "New", "n\n")) // added, in a folder nobody had
	o.write("added/not a page.txt", "plain\n")                    // not a page at all
	o.write(".petalbook/other.yml", "x: 1\n")                     // not a page either
	o.push("many changes")

	// Count what the pull reads and walks.
	src := &countingSource{Source: f.s.src}
	f.s.src = src
	walks := f.s.walks.Load()
	if err := f.s.Pull(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if src.walks.Load() != 0 || f.s.walks.Load() != walks {
		t.Fatal("the pull walked the whole tree")
	}
	// Four files changed or appeared as pages (edit, rename target, new id, added); nothing else is read.
	if got := src.reads.Load(); got != 4 {
		t.Fatalf("the pull read %d files, want 4", got)
	}
	if p, err := f.s.Get(edit.ID); err != nil || p.Body != "edited elsewhere\n" {
		t.Fatalf("edit: %v %v", p, err)
	}
	if _, err := f.s.Get(del.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("the deleted page is still there")
	}
	if p, err := f.s.Get(ren.ID); err != nil || p.Path != "elsewhere/Renamed.md" {
		t.Fatalf("rename: %v %v", p, err)
	}
	if _, err := f.s.Get(chg.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("the old id still resolves")
	}
	if p, err := f.s.Get("CHANGEDXD1"); err != nil || p.Path != chg.Path {
		t.Fatalf("new id: %v %v", p, err)
	}
	if p, err := f.s.Get("ADDEDPAGE1"); err != nil || p.Path != "added/New.md" {
		t.Fatalf("added: %v %v", p, err)
	}
	tree, _ := f.s.Tree()
	var folders []string
	for _, fo := range tree {
		folders = append(folders, fo.Path)
	}
	if strings.Contains(strings.Join(folders, ","), "gone") || !strings.Contains(strings.Join(folders, ","), "added") || !strings.Contains(strings.Join(folders, ","), "elsewhere") {
		t.Fatalf("folders %v", folders)
	}
	if st := f.last(); st.State != RemoteOK || st.LastGood != f.tip() {
		t.Fatalf("status %+v", st)
	}
	consistent(t, f.s)
	// Pulling again with nothing new reads and walks nothing.
	src.reads.Store(0)
	src.walks.Store(0)
	if err := f.s.Pull(context.Background(), true); err != nil || src.reads.Load() != 0 || src.walks.Load() != 0 {
		t.Fatalf("%v %d %d", err, src.reads.Load(), src.walks.Load())
	}
}

type countingSource struct {
	index.Source
	walks, reads atomicInt
}

type atomicInt struct {
	mu sync.Mutex
	n  int64
}

func (a *atomicInt) Add(d int64) { a.mu.Lock(); a.n += d; a.mu.Unlock() }
func (a *atomicInt) Store(v int64) {
	a.mu.Lock()
	a.n = v
	a.mu.Unlock()
}
func (a *atomicInt) Load() int64 { a.mu.Lock(); defer a.mu.Unlock(); return a.n }

func (c *countingSource) Walk(file func(index.FileInfo) error, dir func(string) error) error {
	c.walks.Add(1)
	return c.Source.Walk(file, dir)
}
func (c *countingSource) Read(rel string) ([]byte, error) {
	c.reads.Add(1)
	return c.Source.Read(rel)
}

func TestReadsDoNotWaitForTheNetwork(t *testing.T) {
	f := newRemoteSpace(t)
	p := mustCreate(t, f.s, "d", meta("title", "Readable"), "r\n")
	reached, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.net.beforePush = func() { once.Do(func() { close(reached) }); <-release }
	done := make(chan error, 1)
	go func() {
		_, err := f.s.Create(Author{}, CreateInput{Meta: meta("title", "Slow push")})
		done <- err
	}()
	select {
	case <-reached:
	case <-time.After(10 * time.Second):
		t.Fatal("the push never started")
	}
	// A change is in the middle of a push. Reads answer at once.
	read := make(chan error, 1)
	go func() {
		if got, err := f.s.Get(p.ID); err != nil || got.Body != "r\n" {
			read <- fmt.Errorf("get: %v %v", got, err)
			return
		}
		if res, err := f.s.List(index.Query{Recursive: true, Limit: 10}); err != nil || len(res.Items) < 1 {
			read <- fmt.Errorf("list: %v", err)
			return
		}
		if _, err := f.s.Tree(); err != nil {
			read <- err
			return
		}
		if _, err := f.s.Tags(); err != nil {
			read <- err
			return
		}
		if _, err := f.s.CheckDraft(p.ID, meta("title", "Readable"), "r\n"); err != nil {
			read <- err
			return
		}
		read <- nil
	}()
	select {
	case err := <-read:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("a read waited for a push")
	}
	// A second change does wait for the first.
	second := make(chan error, 1)
	go func() { _, err := f.s.Create(Author{}, CreateInput{Meta: meta("title", "Second")}); second <- err }()
	select {
	case <-second:
		t.Fatal("a second change did not wait for the push in progress")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	// The same for a fetch in a pull.
	reached2, release2 := make(chan struct{}), make(chan struct{})
	var once2 sync.Once
	f.net.beforeNet = func(sub string) {
		if sub == "fetch" {
			once2.Do(func() { close(reached2) })
			<-release2
		}
	}
	pulled := make(chan error, 1)
	go func() { pulled <- f.s.Pull(context.Background(), true) }()
	<-reached2
	readDone := make(chan error, 1)
	go func() { _, err := f.s.Get(p.ID); readDone <- err }()
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		close(release2)
		t.Fatal("a read waited for a fetch")
	}
	close(release2)
	if err := <-pulled; err != nil {
		t.Fatal(err)
	}
	f.net.beforeNet = nil
	consistent(t, f.s)
}

func TestEmptyFoldersStayOnTheServer(t *testing.T) {
	f := newRemoteSpace(t)
	before := f.remoteLog()
	subs := len(f.net.subs)
	got, err := f.s.CreateFolder(Author{}, "Drafts/2027")
	if err != nil || got != "Drafts/2027" {
		t.Fatalf("%q %v", got, err)
	}
	if len(f.net.subs) != subs || !reflect.DeepEqual(f.remoteLog(), before) {
		t.Fatal("making a folder touched the remote")
	}
	if git(t, f.root, "status", "--porcelain") != "" {
		t.Fatal("making a folder left something for Git")
	}
	if _, err := os.Stat(filepath.Join(f.root, "Drafts", "2027", ".gitkeep")); err == nil {
		t.Fatal("a .gitkeep was made")
	}
	tree, _ := f.s.Tree()
	found := false
	for _, fo := range tree {
		found = found || fo.Path == "Drafts/2027"
	}
	if !found {
		t.Fatal("the folder is not listed")
	}
	if _, err := f.s.CreateFolder(Author{}, "Drafts/2027"); !errors.Is(err, ErrExists) {
		t.Fatalf("%v", err)
	}
	// A page can be made in it, and then it is carried like any folder.
	mustCreate(t, f.s, "Drafts/2027", meta("title", "In it"), "x\n")
	if files := git(t, f.remote, "-c", "core.quotepath=false", "ls-tree", "-r", "--name-only", "main"); !strings.Contains(files, "Drafts/2027/In it.md") {
		t.Fatalf("%s", files)
	}
	// A server-only folder survives a reopen.
	f.s.CreateFolder(Author{}, "Only here")
	f.reopen()
	tree, _ = f.s.Tree()
	found = false
	for _, fo := range tree {
		found = found || fo.Path == "Only here"
	}
	if !found {
		t.Fatal("the empty folder was lost on reopening")
	}
}

func TestNoJournalOrCommitterWorkIsLeftBetweenChanges(t *testing.T) {
	f := newRemoteSpace(t)
	mustCreate(t, f.s, "", meta("title", "A"), "a\n")
	f.s.cm.mu.Lock()
	n := len(f.s.cm.paths)
	f.s.cm.mu.Unlock()
	if n != 0 {
		t.Fatal("the committer was handed a remote space's change")
	}
	// Flushing the committer has nothing to do, and every local commit is on the remote.
	before := git(t, f.root, "rev-parse", "HEAD")
	if err := f.s.Flush(); err != nil || git(t, f.root, "rev-parse", "HEAD") != before {
		t.Fatalf("flush: %v", err)
	}
	if got := git(t, f.root, "rev-list", "--count", "origin/main..HEAD"); got != "0" {
		t.Fatalf("%s local commits are not on the remote", got)
	}
}

// A folder move whose push fails, and which then cannot be moved back: the
// files stay where they are (they may be the only copy), nothing at the new
// place is removed, and the space refuses changes until someone has looked.
func TestFolderThatCannotBeMovedBackIsLeftWhereItIsAndTheSpaceBlocked(t *testing.T) {
	f := newRemoteSpace(t)
	mustCreate(t, f.s, "old", meta("title", "Tracked"), "t\n")
	hand := filepath.Join(f.root, "old", "by-hand.md")
	if err := os.WriteFile(hand, []byte("no header, never committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.net.beforePush = func() { f.net.setDown(true) }
	rename := f.s.ops.Rename
	calls := 0
	f.s.ops.Rename = func(from, to string) error {
		calls++
		if calls == 2 { // the first is the move, the second the move back
			return errors.New("the disk says no")
		}
		return rename(from, to)
	}
	_, err := f.s.MoveFolder(Author{}, "old", "new")
	var ue *UnavailableError
	if !errors.As(err, &ue) {
		t.Fatalf("%v", err)
	}
	f.s.ops.Rename = rename
	for _, rel := range []string{"new/Tracked.md", "new/by-hand.md"} {
		if _, err := os.Stat(filepath.Join(f.root, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("%s was removed: %v", rel, err)
		}
	}
	if st := f.last(); st.State != RemoteError || !strings.Contains(st.Error, "could not be moved back") {
		t.Fatalf("%+v", st)
	}
	f.net.setDown(false)
	f.net.beforePush = nil
	if _, err := f.s.Create(Author{}, CreateInput{Meta: meta("title", "Later")}); err == nil {
		t.Fatal("a change was accepted in a space that could not undo the last one")
	}
}
