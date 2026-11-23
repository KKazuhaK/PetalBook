package api

import (
	"context"
	"errors"
	"testing"
	"time"
)

func newIdem() *mcpIdem { return &mcpIdem{m: map[string]*mcpIdemEntry{}, now: time.Now} }

// A create that panics is not remembered: a later call with the same
// requestId makes it instead of waiting forever.
func TestMCPIdemForgetsACreateThatPanicked(t *testing.T) {
	t.Parallel()
	i := newIdem()
	func() {
		defer func() { _ = recover() }()
		_, _, _ = i.do(context.Background(), "k", func() (*mcpItem, error) { panic("boom") })
	}()
	got := make(chan *mcpItem, 1)
	go func() {
		item, replayed, err := i.do(context.Background(), "k", func() (*mcpItem, error) { return &mcpItem{ID: "x"}, nil })
		if err != nil || replayed {
			t.Errorf("%v %v", replayed, err)
		}
		got <- item
	}()
	select {
	case item := <-got:
		if item == nil || item.ID != "x" {
			t.Fatalf("%+v", item)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call after a panic hangs")
	}
}

// A call waiting for another with the same requestId gives up when its own
// request does.
func TestMCPIdemWaiterFollowsItsContext(t *testing.T) {
	t.Parallel()
	i := newIdem()
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	go func() {
		_, _, _ = i.do(context.Background(), "k", func() (*mcpItem, error) {
			close(started)
			<-release
			return &mcpItem{ID: "x"}, nil
		})
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := i.do(ctx, "k", func() (*mcpItem, error) { return nil, errors.New("must not run") })
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the waiter ignores its context")
	}
}
