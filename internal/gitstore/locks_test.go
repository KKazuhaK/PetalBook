package gitstore

import (
	"context"
	"testing"
	"time"
)

// A write of the index or a ref is not stopped by cancelling, but it is not
// unbounded either: at most writeCap, or the caller's earlier deadline.
func TestUncancelledWritesAreCapped(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, done := notCancelled(ctx)
	defer done()
	if c.Err() != nil {
		t.Fatal("cancelling stops the write")
	}
	if d, ok := c.Deadline(); !ok || time.Until(d) > writeCap {
		t.Errorf("no cap: %v %v", d, ok)
	}
	early, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	c, done = notCancelled(early)
	defer done()
	if d, _ := c.Deadline(); time.Until(d) > time.Second {
		t.Errorf("the caller's deadline is not kept: %v", d)
	}
}
