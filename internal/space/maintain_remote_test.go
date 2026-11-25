package space

import (
	"testing"
)

// A space that follows a remote does its fetches and pushes under the space
// lock, not under the committer's lock, so the committer must never pack it.
// Disconnecting makes it local again, and packing resumes.
func TestRemoteSpaceIsNotPackedByTheCommitterUntilDisconnected(t *testing.T) {
	f := newRemoteSpace(t)
	l := &gitLog{}
	f.s.cm.git.before = l.hook
	f.s.cm.maintainEvery.Store(1)
	// A count that would trigger a packing at once, were it not paused.
	f.s.cm.sinceMaintain.Store(5)
	f.s.cm.maintain(false)
	f.s.cm.maintain(true)
	if n := l.count("repack"); n != 0 {
		t.Fatalf("the committer packed a remote space (%d)", n)
	}
	if err := f.s.Disconnect(); err != nil {
		t.Fatal(err)
	}
	f.s.cm.maintain(false)
	if n := l.count("repack"); n != 1 {
		t.Fatalf("%d packings after disconnecting, want 1", n)
	}
}

// A remote space is packed from inside its changes, under the space lock that
// its fetches and pushes take, after the push of every N-th change.
func TestRemoteSpaceIsPackedEveryNChangesAfterThePush(t *testing.T) {
	f := newRemoteSpace(t)
	f.s.rem.Load().packEvery = 3
	mustCreate(t, f.s, "", meta("title", "One"), "1\n")
	mustCreate(t, f.s, "", meta("title", "Two"), "2\n")
	if n := f.net.count("repack"); n != 0 {
		t.Fatalf("packed after 2 changes (%d)", n)
	}
	pushes := f.net.count("push")
	mustCreate(t, f.s, "", meta("title", "Three"), "3\n")
	if n := f.net.count("repack"); n != 1 {
		t.Fatalf("%d packings after 3 changes, want 1", n)
	}
	f.net.mu.Lock()
	subs := append([]string(nil), f.net.subs...)
	f.net.mu.Unlock()
	lastPush, firstRepack := -1, -1
	for i, s := range subs {
		if s == "push" {
			lastPush = i
		}
		if s == "repack" && firstRepack < 0 {
			firstRepack = i
		}
	}
	if f.net.count("push") != pushes+1 || firstRepack < lastPush {
		t.Fatalf("the packing did not follow the push: %v", subs)
	}
	git(t, f.root, "fsck", "--strict")
	if got := f.remoteLog(); len(got) < 4 {
		t.Fatalf("remote log %v", got)
	}
	// Three more changes, one more packing; the space is still consistent.
	for _, title := range []string{"Four", "Five", "Six"} {
		mustCreate(t, f.s, "", meta("title", title), "x\n")
	}
	if n := f.net.count("repack"); n != 2 {
		t.Fatalf("%d packings after 6 changes, want 2", n)
	}
	consistent(t, f.s)
}
