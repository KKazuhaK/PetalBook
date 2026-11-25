package space

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// A request that stops waiting does not take its change with it: the change
// is still pushed, and later requests are still answered.
func TestACancelledRequestsChangeIsStillPushed(t *testing.T) {
	t.Parallel()
	f := newRemoteSpace(t)
	p := mustCreate(t, f.s, "", meta("title", "P"), "v1\n")
	other := mustCreate(t, f.s, "", meta("title", "Q"), "q1\n")
	hold, entered := make(chan struct{}), make(chan struct{})
	var once, rel sync.Once
	release := func() { rel.Do(func() { close(hold) }) }
	t.Cleanup(release)
	f.net.mu.Lock()
	f.net.beforePush = func() { once.Do(func() { close(entered); <-hold }) }
	f.net.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := mutateIn(ctx, f.s, Author{}, func(tx *txn) (*Page, error) {
			return f.s.saveLocked(tx, p.ID, p.Rev, meta("title", "P"), "v2\n")
		})
		done <- err
	}()
	<-entered
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("the cancelled request: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled request kept waiting")
	}
	release()
	if _, err := f.s.Save(Author{}, other.ID, other.Rev, meta("title", "Q"), "q2\n"); err != nil {
		t.Fatalf("a later request: %v", err)
	}
	if got := git(t, f.remote, "show", "main:"+p.Path); !containsLine(got, "v2") {
		t.Errorf("the cancelled request's change is not on the remote:\n%s", got)
	}
	if got := git(t, f.remote, "show", "main:"+other.Path); !containsLine(got, "q2") {
		t.Errorf("the later change is not on the remote:\n%s", got)
	}
	consistent(t, f.s)
}

// Every pusher ends: Close and Disconnect wait for theirs, and Connect
// replaces the old one.
func TestNoPusherOutlivesItsRemote(t *testing.T) {
	t.Parallel()
	f := newRemoteSpace(t)
	mustCreate(t, f.s, "", meta("title", "P"), "v1\n")
	first := f.s.rem.Load()
	o := f.options()
	o.URL = bareRepo(t)
	if err := f.s.Connect(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	ended := func(r *remote) bool {
		select {
		case <-r.pusherDone:
			return true
		default:
			return false
		}
	}
	if !ended(first) {
		t.Error("the replaced remote's pusher is still running")
	}
	second := f.s.rem.Load()
	if ended(second) {
		t.Fatal("the new remote has no pusher")
	}
	if err := f.s.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if !ended(second) {
		t.Error("the pusher survived Disconnect")
	}
	f2 := newRemoteSpace(t)
	mustCreate(t, f2.s, "", meta("title", "P"), "v1\n")
	f2.reopen() // the pusher of a space opened with its remote
	r := f2.s.rem.Load()
	mustCreate(t, f2.s, "", meta("title", "Q"), "v1\n")
	if err := f2.s.Close(); err != nil {
		t.Fatal(err)
	}
	if !ended(r) {
		t.Error("the pusher survived Close")
	}
}
