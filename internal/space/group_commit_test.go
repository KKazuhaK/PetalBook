package space

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Changes that come while a push is running are not held back by it: they
// are made and committed, and go out together in the next push. Each is
// answered only once its own commit is on the remote.
func TestChangesDuringAPushGoOutTogetherInTheNextOne(t *testing.T) {
	t.Parallel()
	f := newRemoteSpace(t)
	var pages []*Page
	for i := 0; i < 4; i++ {
		pages = append(pages, mustCreate(t, f.s, "", meta("title", fmt.Sprintf("P%d", i)), "v1\n"))
	}
	before := len(f.remoteLog())
	pushesBefore := f.net.count("push")
	release := make(chan struct{})
	var released sync.Once
	letGo := func() { released.Do(func() { close(release) }) }
	t.Cleanup(letGo)
	entered := make(chan struct{})
	var once sync.Once
	f.net.mu.Lock()
	f.net.beforePush = func() {
		once.Do(func() {
			close(entered)
			<-release
		})
	}
	f.net.mu.Unlock()
	errs := make(chan error, 4)
	save := func(p *Page) {
		_, err := f.s.Save(Author{}, p.ID, p.Rev, meta("title", p.Meta.Title()), "v2\n")
		errs <- err
	}
	go save(pages[0])
	<-entered // the first change's push is running
	for _, p := range pages[1:] {
		go save(p)
	}
	// The others are made and committed while the push still runs.
	deadline := time.Now().Add(10 * time.Second)
	for {
		ahead := git(t, f.root, "rev-list", "--count", "HEAD", "--not", "--remotes")
		if ahead == "4" {
			break
		}
		if time.Now().After(deadline) {
			letGo()
			for range pages {
				<-errs
			}
			t.Fatalf("the changes waited for the push: %s commits ahead", ahead)
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case err := <-errs:
		t.Fatalf("a change was answered before its push: %v", err)
	default:
	}
	letGo()
	for range pages {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if got := len(f.remoteLog()) - before; got != 4 {
		t.Fatalf("%d commits on the remote, want 4", got)
	}
	// The push that was held reads HEAD only when it runs: it may carry
	// all four, or the first and then the three together.
	if n := f.net.count("push") - pushesBefore; n < 1 || n > 2 {
		t.Errorf("%d pushes, want one or two", n)
	}
	if st := git(t, f.root, "status", "--porcelain"); st != "" {
		t.Errorf("the clone: %q", st)
	}
	consistent(t, f.s)
}

// A push of a group that the remote refuses (it moved on) takes back every
// change of the group, and each is made again on the new tip: none is lost,
// none is pushed twice.
func TestARefusedGroupIsMadeAgainOnTheNewTip(t *testing.T) {
	t.Parallel()
	f := newRemoteSpace(t)
	var pages []*Page
	for i := 0; i < 3; i++ {
		pages = append(pages, mustCreate(t, f.s, "", meta("title", fmt.Sprintf("P%d", i)), "v1\n"))
	}
	release := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	f.net.mu.Lock()
	f.net.beforePush = func() {
		once.Do(func() {
			// Somebody else pushes first: this push is refused.
			o := f.theirs()
			o.write("Theirs.md", pageText("THEMS00001", "Theirs", "t\n"))
			o.push("theirs")
			close(entered)
			<-release
		})
	}
	f.net.mu.Unlock()
	errs := make(chan error, 3)
	save := func(p *Page) {
		_, err := f.s.Save(Author{}, p.ID, p.Rev, meta("title", p.Meta.Title()), "v2\n")
		errs <- err
	}
	go save(pages[0])
	<-entered
	go save(pages[1])
	go save(pages[2])
	time.Sleep(200 * time.Millisecond)
	close(release)
	for range pages {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range pages {
		if got := git(t, f.remote, "show", "main:"+p.Path); got != pageText(p.ID, p.Meta.Title(), "v2") && !containsLine(got, "v2") {
			t.Errorf("%s on the remote:\n%s", p.Path, got)
		}
	}
	if got := git(t, f.remote, "show", "main:Theirs.md"); !containsLine(got, "t") {
		t.Errorf("their page: %q", got)
	}
	consistent(t, f.s)
}

func containsLine(s, line string) bool {
	for _, l := range splitLines(s) {
		if l == line {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// Two changes committed and waiting for their push when the server stops:
// the next start takes back both, to the commit the first started from.
func TestAGroupNotPushedIsTakenBackWhole(t *testing.T) {
	t.Parallel()
	f := newRemoteSpace(t)
	a := mustCreate(t, f.s, "", meta("title", "A"), "a1\n")
	b := mustCreate(t, f.s, "", meta("title", "B"), "b1\n")
	tip := f.tip()
	crash(f.s)
	commitAs := func(p *Page, body string) string {
		if err := os.WriteFile(filepath.Join(f.root, p.Path), []byte(pageText(p.ID, p.Meta.Title(), body)), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, f.root, "add", p.Path)
		git(t, f.root, "-c", "user.name=T", "-c", "user.email=t@example.test", "commit", "-qm", "x")
		return git(t, f.root, "rev-parse", "HEAD")
	}
	first := commitAs(a, "a2\n")
	commitAs(b, "b2\n")
	journal := `{"op":"rp","rev":"` + tip + `"}` + "\n" +
		`{"op":"x","tx":2,"pn":1}` + "\n" + `{"op":"p","tx":2,"p":"` + a.Path + `"}` + "\n" +
		`{"op":"rp","rev":"` + first + `"}` + "\n" +
		`{"op":"x","tx":5,"pn":1}` + "\n" + `{"op":"p","tx":5,"p":"` + b.Path + `"}` + "\n"
	if err := os.WriteFile(journalFile(f.root), framed(journal), 0o644); err != nil {
		t.Fatal(err)
	}
	f.reopen()
	if head := git(t, f.root, "rev-parse", "HEAD"); head != tip {
		t.Errorf("HEAD %s, the remote %s", head, tip)
	}
	for _, p := range []*Page{a, b} {
		if got := readDisk(t, f.s, p.Path); strings.Contains(got, "2\n") {
			t.Errorf("%s was not taken back:\n%s", p.Path, got)
		}
	}
	consistent(t, f.s)
}
