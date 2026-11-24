package api

import (
	"testing"
	"time"
)

// A tenant runs one bulk job at a time and the server two; the others wait.
func TestTheJobRunnerAllowsOnePerTenantAndTwoOnTheServer(t *testing.T) {
	r := newJobRunner()
	r.acquire("A")
	r.acquire("B")
	got := make(chan string, 4)
	go func() { r.acquire("C"); got <- "C" }()
	go func() { r.acquire("A"); got <- "A" }()
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
