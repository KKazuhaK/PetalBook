package space

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Under writers that never stop, the change that started a push is answered
// once a push has its commit, not when the writers stop.
func TestTheChangeThatStartedAPushIsAnsweredUnderSteadyLoad(t *testing.T) {
	t.Parallel()
	f := newRemoteSpace(t)
	var pages []*Page
	for i := 0; i < 9; i++ {
		pages = append(pages, mustCreate(t, f.s, "", meta("title", fmt.Sprintf("P%d", i)), "v0\n"))
	}
	started := make(chan struct{})
	var first sync.Once
	f.net.mu.Lock()
	f.net.beforePush = func() { first.Do(func() { close(started) }) }
	// Commits made while a push ends (after git read HEAD) wait for the
	// next one.
	f.net.afterPush = func() { time.Sleep(50 * time.Millisecond) }
	f.net.mu.Unlock()
	// The change measured starts the push; the writers come while it runs.
	start := time.Now()
	done := make(chan error, 1)
	go func() {
		_, err := f.s.Save(Author{}, pages[0].ID, pages[0].Rev, meta("title", "P0"), "mine\n")
		done <- err
	}()
	<-started
	var stop atomic.Bool
	var wg sync.WaitGroup
	for w := 1; w < 9; w++ {
		wg.Add(1)
		go func(p *Page) {
			defer wg.Done()
			for i := 0; !stop.Load(); i++ {
				cur, err := f.s.Get(p.ID)
				if err != nil {
					return
				}
				f.s.Save(Author{}, p.ID, cur.Rev, meta("title", p.Meta.Title()), fmt.Sprintf("v%d\n", i))
			}
		}(pages[w])
	}
	var err error
	var took time.Duration
	select {
	case err = <-done:
		took = time.Since(start)
	case <-time.After(3 * time.Second):
		took = time.Since(start)
	}
	stop.Store(true)
	wg.Wait()
	if took >= 3*time.Second {
		<-done
		t.Fatalf("the change was not answered in %v under steady load", took)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// A push whose outcome cannot be told (what the remote has cannot be read
// after it) is a failed push: no change of it is answered as done.
func TestAPushWhoseOutcomeIsUnknownIsAFailure(t *testing.T) {
	t.Parallel()
	f := newRemoteSpace(t)
	p := mustCreate(t, f.s, "", meta("title", "P"), "v1\n")
	var once sync.Once
	f.net.mu.Lock()
	f.net.afterPush = func() {
		once.Do(func() { git(t, f.root, "update-ref", "-d", "refs/remotes/origin/main") })
	}
	f.net.mu.Unlock()
	if _, err := f.s.Save(Author{}, p.ID, p.Rev, meta("title", "P"), "v2\n"); err == nil {
		t.Error("the change was answered as done")
	}
}

// Disconnecting while a group push runs (with the space lock let go) waits
// for that push: it is never taken back from under the disconnect.
func TestDisconnectWaitsForAPushUnderWay(t *testing.T) {
	t.Parallel()
	f := newRemoteSpace(t)
	p := mustCreate(t, f.s, "", meta("title", "P"), "v1\n")
	release, entered := make(chan struct{}), make(chan struct{})
	var once, rel sync.Once
	letGo := func() { rel.Do(func() { close(release) }) }
	t.Cleanup(letGo)
	f.net.mu.Lock()
	f.net.beforePush = func() { once.Do(func() { close(entered); <-release }) }
	f.net.mu.Unlock()
	saved := make(chan error, 1)
	go func() {
		_, err := f.s.Save(Author{}, p.ID, p.Rev, meta("title", "P"), "v2\n")
		saved <- err
	}()
	<-entered
	disconnected := make(chan error, 1)
	go func() { disconnected <- f.s.Disconnect() }()
	select {
	case err := <-disconnected:
		letGo()
		<-saved
		t.Fatalf("disconnected while the push ran: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	letGo()
	if err := <-saved; err != nil {
		t.Fatal(err)
	}
	if err := <-disconnected; err != nil {
		t.Fatal(err)
	}
}
