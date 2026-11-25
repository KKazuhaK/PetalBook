package api

import (
	"context"
	"testing"
	"time"
)

// A tenant runs one bulk job at a time and the server two; the others wait.
func TestTheJobRunnerAllowsOnePerTenantAndTwoOnTheServer(t *testing.T) {
	t.Parallel()
	bg := context.Background()
	r := newJobRunner()
	r.acquire(bg, "A")
	r.acquire(bg, "B")
	got := make(chan string, 4)
	go func() { r.acquire(bg, "C"); got <- "C" }()
	go func() { r.acquire(bg, "A"); got <- "A" }()
	select {
	case x := <-got:
		t.Fatalf("%s started while two jobs were running", x)
	case <-time.After(150 * time.Millisecond):
	}
	// One place is free: one of the waiting jobs takes it; the other still waits
	// (the second of tenant A, because tenant A's turn is taken; tenant C's, because
	// the server is full again).
	r.release("B")
	var first string
	select {
	case first = <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("nobody took the free place")
	}
	select {
	case x := <-got:
		t.Fatalf("%s started beside %s, over the limit", x, first)
	case <-time.After(150 * time.Millisecond):
	}
	// Tenant A's first job ends: its second may start if the server has room.
	r.release("A")
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("the waiting job never started")
	}
}

// A job that works on several tenants waits for the turn of each of them.
func TestAJobOfSeveralTenantsWaitsForEachOfThem(t *testing.T) {
	t.Parallel()
	bg := context.Background()
	r := newJobRunner()
	r.acquire(bg, "B")
	got := make(chan struct{})
	go func() { r.acquire(bg, "A", "B"); close(got) }()
	select {
	case <-got:
		t.Fatal("it started while tenant B's turn was taken")
	case <-time.After(150 * time.Millisecond):
	}
	r.release("B")
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("it never started")
	}
	r.release("A", "B")
}

// A job waiting for its turn stops waiting when its context ends (the handler
// is closed), having taken nothing.
func TestAWaitingJobStopsWaitingWhenItsContextEnds(t *testing.T) {
	t.Parallel()
	r := newJobRunner()
	r.acquire(context.Background(), "A")
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan bool)
	go func() { got <- r.acquire(ctx, "A") }()
	select {
	case <-got:
		t.Fatal("it started while tenant A's turn was taken")
	case <-time.After(150 * time.Millisecond):
	}
	cancel()
	r.wake()
	select {
	case ok := <-got:
		if ok {
			t.Fatal("it took a turn after its context ended")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("it went on waiting")
	}
	r.release("A")
	if !r.acquire(context.Background(), "A") {
		t.Fatal("tenant A's turn was not given back")
	}
}
