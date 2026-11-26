package space

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"petalbook.app/server/internal/index"
)

// localSpace is an ordinary space in a fresh directory, as a new account's
// space is before it is connected.
func localSpace(t *testing.T) *Space {
	t.Helper()
	needGit(t)
	return openAt(t, filepath.Join(t.TempDir(), "space"), func(o *Options) {
		o.Committer = "Service <svc@example.test>"
		o.Author = "Ann <ann@example.test>"
		o.Name = "Notes"
	})
}

func connectOpts(remote string, n *netRunner) RemoteOptions {
	return RemoteOptions{URL: remote, Branch: "main", AllowUnsafe: true, Runner: n, FetchFresh: time.Nanosecond, FailFast: time.Nanosecond}
}

func fileList(t *testing.T, repo, ref string) string {
	t.Helper()
	return git(t, repo, "-c", "core.quotepath=false", "ls-tree", "-r", "--name-only", ref)
}

// preloaded is a bare remote that somebody else has put pages in.
func preloaded(t *testing.T, files map[string]string, branch string) (remote string) {
	t.Helper()
	remote = bareRepo(t)
	dir := filepath.Join(t.TempDir(), "seed")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q", "-b", "main")
	for rel, c := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitO(t, dir, "add", "-A")
	gitO(t, dir, "commit", "-qm", "their content")
	gitO(t, dir, "push", "-q", remote, "main:"+branch)
	return remote
}

func TestConnectToANewEmptyRemotePushesTheHistory(t *testing.T) {
	t.Parallel()
	s := localSpace(t)
	mustCreate(t, s, "Tang", meta("title", "暮雪"), "v1\n")
	mustCreate(t, s, "", meta("title", "Second"), "v2\n")
	// A repository that has other branches but not this one counts as empty.
	remote := preloaded(t, map[string]string{"x.txt": "x\n"}, "dev")
	net := &netRunner{}
	if err := s.Connect(context.Background(), connectOpts(remote, net)); err != nil {
		t.Fatal(err)
	}
	files := fileList(t, remote, "main")
	for _, want := range []string{"Tang/暮雪.md", "Second.md", ".petalbook/space.yml"} {
		if !strings.Contains(files, want) {
			t.Errorf("%s was not pushed:\n%s", want, files)
		}
	}
	if git(t, remote, "rev-parse", "dev") == git(t, remote, "rev-parse", "main") {
		t.Fatal("the other branch was touched")
	}
	if got := git(t, s.Root(), "rev-parse", "HEAD"); got != git(t, remote, "rev-parse", "main") {
		t.Fatal("the clone is not at the remote's tip")
	}
	st, ok := s.Remote()
	if !ok || st.State != RemoteOK || st.LastGood == "" || st.LastSync.IsZero() {
		t.Fatalf("%+v %v", st, ok)
	}
	// From here on it is a remote space.
	mustCreate(t, s, "", meta("title", "Third"), "v3\n")
	if !strings.Contains(fileList(t, remote, "main"), "Third.md") {
		t.Fatal("a change after connecting was not pushed")
	}
	consistent(t, s)
}

func TestConnectingAPristineSpaceTakesTheRemotesContentAndHistory(t *testing.T) {
	t.Parallel()
	remote := preloaded(t, map[string]string{
		"Theirs.md":   pageText("THEMS00001", "Theirs", "t\n"),
		"d/Nested.md": pageText("THEMS00002", "Nested", "n\n"),
		"readme.txt":  "not a page\n",
	}, "main")
	theirTip := git(t, remote, "rev-parse", "main")
	s := localSpace(t)
	// Local mode may not even have committed its own set-up yet: it is still pristine.
	net := &netRunner{}
	if err := s.Connect(context.Background(), connectOpts(remote, net)); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get("THEMS00001"); err != nil || got.Body != "t\n" {
		t.Fatalf("%v %v", got, err)
	}
	if got, err := s.Get("THEMS00002"); err != nil || got.Path != "d/Nested.md" {
		t.Fatalf("%v %v", got, err)
	}
	// The remote had no space.yml: it was made, committed and pushed on top of their history.
	if files := fileList(t, remote, "main"); !strings.Contains(files, ".petalbook/space.yml") || !strings.Contains(files, "Theirs.md") {
		t.Fatalf("remote files:\n%s", files)
	}
	if n := git(t, remote, "rev-list", "--count", theirTip+"..main"); n != "1" {
		t.Fatalf("%s commits were added to their history", n)
	}
	// Their history is the space's history: the space's own first commit is gone.
	if n := git(t, s.Root(), "rev-list", "--count", "HEAD"); n != "2" {
		t.Fatalf("%s commits in the clone", n)
	}
	// No untracked space.yml (or anything else) is left behind.
	if st := git(t, s.Root(), "status", "--porcelain", "--untracked-files=all"); st != "" {
		t.Fatalf("git status:\n%s", st)
	}
	if b, _ := os.ReadFile(filepath.Join(s.Root(), ".petalbook", "space.yml")); !strings.Contains(string(b), "Notes") {
		t.Fatalf("space.yml: %q", b)
	}
	consistent(t, s)
	// A remote that has its own space.yml keeps it, and nothing is pushed.
	remote2 := preloaded(t, map[string]string{"A.md": pageText("THEMS00003", "A", "a\n"), ".petalbook/space.yml": "name: Theirs\n"}, "main")
	tip2 := git(t, remote2, "rev-parse", "main")
	s2 := localSpace(t)
	if err := s2.Connect(context.Background(), connectOpts(remote2, net)); err != nil {
		t.Fatal(err)
	}
	if git(t, remote2, "rev-parse", "main") != tip2 {
		t.Fatal("something was pushed although the remote had a space.yml")
	}
	if b, _ := os.ReadFile(filepath.Join(s2.Root(), ".petalbook", "space.yml")); string(b) != "name: Theirs\n" {
		t.Fatalf("%q", b)
	}
	if st := git(t, s2.Root(), "status", "--porcelain", "--untracked-files=all"); st != "" {
		t.Fatalf("git status:\n%s", st)
	}
	consistent(t, s2)
}

func TestConnectWhenOneHistoryContainsTheOther(t *testing.T) {
	t.Parallel()
	// The space is behind: it was connected before, and somebody pushed since.
	f := newRemoteSpace(t)
	mustCreate(t, f.s, "", meta("title", "Ours"), "o\n")
	if err := f.s.Disconnect(); err != nil {
		t.Fatal(err)
	}
	o := f.theirs()
	o.write("Later.md", pageText("THEMS00004", "Later", "l\n"))
	o.push("later")
	tip := f.tip()
	if err := f.s.Connect(context.Background(), f.options()); err != nil {
		t.Fatal(err)
	}
	if f.tip() != tip || git(t, f.root, "rev-parse", "HEAD") != tip {
		t.Fatal("the space should have moved forward and nothing else")
	}
	if got, err := f.s.Get("THEMS00004"); err != nil || got.Body != "l\n" {
		t.Fatalf("%v %v", got, err)
	}
	consistent(t, f.s)

	// The space is ahead: the remote is moved forward.
	if err := f.s.Disconnect(); err != nil {
		t.Fatal(err)
	}
	mustCreate(t, f.s, "", meta("title", "Local only"), "x\n")
	if err := f.s.Flush(); err != nil {
		t.Fatal(err)
	}
	head := git(t, f.root, "rev-parse", "HEAD")
	if head == tip {
		t.Fatal("no local commit")
	}
	if err := f.s.Connect(context.Background(), f.options()); err != nil {
		t.Fatal(err)
	}
	if f.tip() != head || !strings.Contains(fileList(t, f.remote, "main"), "Local only.md") {
		t.Fatal("the remote was not moved forward")
	}
	consistent(t, f.s)
}

func TestConnectWithTwoHistoriesIsAConflictAndChangesNothing(t *testing.T) {
	t.Parallel()
	remote := preloaded(t, map[string]string{"Theirs.md": pageText("THEMS00001", "Theirs", "t\n")}, "main")
	theirTip := git(t, remote, "rev-parse", "main")
	s := localSpace(t)
	mustCreate(t, s, "", meta("title", "Mine"), "m\n")
	s.Flush()
	before := snapshot(t, s)
	err := s.Connect(context.Background(), connectOpts(remote, &netRunner{}))
	if !errors.Is(err, ErrDiverged) {
		t.Fatalf("want a conflict, got %v", err)
	}
	if now := snapshot(t, s); !reflect.DeepEqual(before, now) {
		t.Fatalf("the space changed:\n%+v\n%+v", before, now)
	}
	if git(t, remote, "rev-parse", "main") != theirTip {
		t.Fatal("the remote changed")
	}
	if out := git(t, s.Root(), "remote"); out != "" {
		t.Fatalf("a remote was left configured: %q", out)
	}
	if _, ok := s.Remote(); ok {
		t.Fatal("the space thinks it follows a remote")
	}
	// And a space whose history is its own but whose set-up differs is a conflict as well:
	// "pristine" means nothing was ever made, not "nothing is there now".
	s2 := localSpace(t)
	p := mustCreate(t, s2, "", meta("title", "Made then deleted"), "m\n")
	s2.Flush() // Git has seen it
	if err := s2.Delete(Author{}, p.ID); err != nil {
		t.Fatal(err)
	}
	s2.Flush()
	if err := s2.Connect(context.Background(), connectOpts(remote, &netRunner{})); !errors.Is(err, ErrDiverged) {
		t.Fatalf("a space that once had a page is not pristine: %v", err)
	}
	// A folder counts too.
	s3 := localSpace(t)
	if _, err := s3.CreateFolder(Author{}, "Drafts"); err != nil {
		t.Fatal(err)
	}
	s3.Flush()
	if err := s3.Connect(context.Background(), connectOpts(remote, &netRunner{})); !errors.Is(err, ErrDiverged) {
		t.Fatalf("a space with a folder is not pristine: %v", err)
	}
}

func TestFailedConnectionLeavesTheSpaceAsItWas(t *testing.T) {
	t.Parallel()
	// The remote has content but no space.yml, so connecting must push one:
	// make that push fail, after the clone has already taken the remote's content.
	remote := preloaded(t, map[string]string{"Theirs.md": pageText("THEMS00001", "Theirs", "t\n")}, "main")
	theirTip := git(t, remote, "rev-parse", "main")
	s := localSpace(t)
	s.Flush()
	before := snapshot(t, s)
	net := &netRunner{}
	net.beforePush = func() { net.setDown(true) }
	err := s.Connect(context.Background(), connectOpts(remote, net))
	var ue *UnavailableError
	if !errors.As(err, &ue) {
		t.Fatalf("want unavailable, got %v", err)
	}
	if now := snapshot(t, s); !reflect.DeepEqual(before, now) {
		t.Fatalf("the space changed:\nbefore %+v\nafter  %+v", before, now)
	}
	if _, err := s.Get("THEMS00001"); !errors.Is(err, ErrNotFound) {
		t.Fatal("the remote's content is half taken")
	}
	if git(t, remote, "rev-parse", "main") != theirTip {
		t.Fatal("the remote changed")
	}
	if out := git(t, s.Root(), "remote"); out != "" {
		t.Fatalf("a remote was left configured: %q", out)
	}
	consistent(t, s)
	// The space still works as a local one.
	mustCreate(t, s, "", meta("title", "Still local"), "x\n")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	// An unreachable remote fails before anything is touched.
	gone := filepath.Join(t.TempDir(), "gone.git")
	if err := s.Connect(context.Background(), connectOpts(gone, &netRunner{})); err == nil {
		t.Fatal("connecting to nothing worked")
	}
	if out := git(t, s.Root(), "remote"); out != "" {
		t.Fatalf("a remote was left configured: %q", out)
	}
	if st := git(t, s.Root(), "status", "--porcelain"); st != "" {
		t.Fatalf("%q", st)
	}
}

func TestDisconnectMakesTheSpaceLocalAgain(t *testing.T) {
	t.Parallel()
	f := newRemoteSpace(t)
	mustCreate(t, f.s, "", meta("title", "Before"), "b\n")
	tip := f.tip()
	if err := f.s.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.s.Remote(); ok {
		t.Fatal("still remote")
	}
	if out := git(t, f.root, "remote"); out != "" {
		t.Fatalf("remote left configured: %q", out)
	}
	subs := len(f.net.subs)
	p := mustCreate(t, f.s, "", meta("title", "After"), "a\n")
	if err := f.s.Flush(); err != nil { // the background committer is back
		t.Fatal(err)
	}
	if got := git(t, f.root, "log", "-1", "--format=%s"); got != "Add After" {
		t.Fatalf("%q", got)
	}
	if len(f.net.subs) != subs || f.tip() != tip {
		t.Fatal("the remote was touched after disconnecting")
	}
	if _, err := f.s.Get(p.ID); err != nil {
		t.Fatal(err)
	}
	if st := git(t, f.root, "status", "--porcelain"); st != "" {
		t.Fatalf("%q", st)
	}
	// Disconnecting twice is fine.
	if err := f.s.Disconnect(); err != nil {
		t.Fatal(err)
	}
}

func TestSpaceListsFoldersOfARemoteSpaceAfterReconnect(t *testing.T) {
	t.Parallel()
	// Folders made while local (with .gitkeep) are pushed like anything else.
	s := localSpace(t)
	if _, err := s.CreateFolder(Author{}, "Drafts/2027"); err != nil {
		t.Fatal(err)
	}
	remote := bareRepo(t)
	if err := s.Connect(context.Background(), connectOpts(remote, &netRunner{})); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fileList(t, remote, "main"), "Drafts/2027/.gitkeep") {
		t.Fatal("the folder was not pushed")
	}
	res, _ := s.Tree()
	var got []string
	for _, f := range res {
		got = append(got, f.Path)
	}
	if !strings.Contains(strings.Join(got, ","), "Drafts/2027") {
		t.Fatalf("%v", got)
	}
	_ = index.Query{}
}
