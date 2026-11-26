package api

import (
	"fmt"
	"testing"
	"time"
)

// A key is noted once an hour; the generations rotate with the clock and at
// the cap, and the table stays within its bound.
func TestOnceAnHourRotatesWithTheClockAndAtTheCap(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	o := newOnceAnHour(10) // five keys a generation
	if !o.first("a", now) || o.first("a", now.Add(59*time.Minute)) {
		t.Fatal("not once an hour")
	}
	// Rotated once by the clock, "a" is still within its hour (in the previous generation)...
	o.first("b", now.Add(59*time.Minute))
	if o.first("b", now.Add(61*time.Minute)) {
		t.Fatal("b was noted 2 minutes ago")
	}
	// ...and an hour after it was noted it is new again.
	if !o.first("a", now.Add(61*time.Minute)) {
		t.Fatal("a was not noted again after an hour")
	}
	// Two hours of quiet drop everything.
	o.first("c", now.Add(61*time.Minute))
	o.first("z", now.Add(4*time.Hour))
	if o.len() != 1 {
		t.Fatalf("%d keys after a quiet spell", o.len())
	}

	// A burst of new keys rotates at the cap and never exceeds the bound; the
	// recent keys are still known.
	o = newOnceAnHour(10)
	for i := range 1000 {
		if !o.first(fmt.Sprint(i), now) {
			t.Fatalf("key %d was not new", i)
		}
		if o.len() > 10 {
			t.Fatalf("%d keys held", o.len())
		}
	}
	if o.first("999", now) {
		t.Fatal("the latest key was forgotten")
	}
	if !o.first("0", now) {
		t.Fatal("the oldest key of the burst is remembered beyond the bound")
	}
}
