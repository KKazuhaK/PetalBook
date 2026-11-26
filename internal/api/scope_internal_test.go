package api

import (
	"fmt"
	"testing"
	"time"
)

// The console-open throttle keeps a table of its own: filling the views'
// audit table to its cap (which clears it) does not reset it, nor the reverse.
func TestTheConsoleThrottleHasItsOwnTable(t *testing.T) {
	t.Parallel()
	views, consoles := newViewTable(), newOnceAnHour(maxConsoleAudit)
	now := time.Now()
	if !consoles.first("scope:S\x00T", now) || consoles.first("scope:S\x00T", now) {
		t.Fatal("not once an hour")
	}
	if !views.firstOpen("A", "S", "p", now) {
		t.Fatal("views")
	}
	for i := range maxViewAudit {
		views.firstOpen("A", "S", fmt.Sprint(i), now)
	}
	if consoles.first("scope:S\x00T", now) {
		t.Fatal("clearing the views' table reset the console throttle")
	}
	for i := range maxConsoleAudit + 1 {
		consoles.first(fmt.Sprint(i), now)
	}
	if consoles.len() > maxConsoleAudit {
		t.Fatalf("console table grew to %d", consoles.len())
	}
}
