package api

import (
	"testing"
	"time"
)

// One flush at a time per person, and a value written while a flush runs is
// never lost or overwritten by the older one, whatever the flush's outcome.
func TestPrefsHubKeepsNewerValuesAndFlushesOneAtATime(t *testing.T) {
	t.Parallel()
	var h prefsHub
	fire := func() {}
	if err := h.add("u", map[string]any{"a": 1, "b": 1}, time.Hour, fire, 0, nil); err != nil {
		t.Fatal(err)
	}
	snap, ok := h.begin("u")
	if !ok || len(snap) != 2 {
		t.Fatalf("%v %v", snap, ok)
	}
	if _, again := h.begin("u"); again {
		t.Fatal("a second flush began while the first runs")
	}
	h.add("u", map[string]any{"a": 2}, time.Hour, fire, 0, nil)
	// The flush is to be tried again: the newer a stays, b too.
	h.finish("u", snap, true, time.Hour, fire)
	if p := h.peek("u"); p["a"] != 2 || p["b"] != 1 {
		t.Fatalf("%v", p)
	}
	snap, _ = h.begin("u")
	h.add("u", map[string]any{"a": 3}, time.Hour, fire, 0, nil)
	// Written: what it wrote goes, what came in meanwhile stays.
	h.finish("u", snap, false, time.Hour, fire)
	if p := h.peek("u"); len(p) != 1 || p["a"] != 3 {
		t.Fatalf("%v", p)
	}
	snap, _ = h.begin("u")
	h.finish("u", snap, false, time.Hour, fire)
	if p := h.peek("u"); p != nil {
		t.Fatalf("%v", p)
	}
}

// A flush that keeps failing for a reason that may pass is tried a few times,
// each after a longer wait, and then given up.
func TestPrefsHubGivesUpAfterAFewTries(t *testing.T) {
	t.Parallel()
	var h prefsHub
	fire := func() {}
	h.add("u", map[string]any{"a": 1}, time.Minute, fire, 0, nil)
	var waits []time.Duration
	for i := 0; ; i++ {
		snap, ok := h.begin("u")
		if !ok {
			t.Fatalf("nothing to flush after %d tries", i)
		}
		if h.finish("u", snap, true, time.Minute, fire) {
			break
		}
		h.mu.Lock()
		p := h.pending["u"]
		waits = append(waits, time.Minute<<p.retries)
		h.mu.Unlock()
		if i > 20 {
			t.Fatal("never given up")
		}
	}
	if len(waits) != maxPrefsRetries || waits[len(waits)-1] != time.Minute<<maxPrefsRetries {
		t.Fatalf("%v", waits)
	}
	if h.peek("u") != nil {
		t.Fatal("kept after giving up")
	}
}
