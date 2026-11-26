package api

import (
	"testing"
	"time"
)

// Every viewable route is in the route table: a name that matches nothing
// would make nothing viewable and say otherwise.
func TestEveryViewableRouteIsInTheTable(t *testing.T) {
	t.Parallel()
	in := map[string]bool{}
	for _, rt := range routeTable {
		in[rt.method+" "+rt.pattern] = true
	}
	for k := range viewableRoutes {
		if !in[k] {
			t.Errorf("%s is viewable and is not a route", k)
		}
	}
}

// The table of views keeps one per session and forgets what has ended.
func TestTheViewAsTableKeepsOnePerSession(t *testing.T) {
	t.Parallel()
	tb := newViewAsTable()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := tb.open(viewAs{session: "s1", until: now.Add(viewAsTTL)}, now)
	b := tb.open(viewAs{session: "s1", until: now.Add(viewAsTTL)}, now)
	if _, ok := tb.get(a.id, "s1", now); ok {
		t.Fatal("the replaced view is still open")
	}
	if _, ok := tb.get(b.id, "s2", now); ok {
		t.Fatal("another session reads the view")
	}
	if _, ok := tb.get(b.id, "s1", now.Add(viewAsTTL)); ok {
		t.Fatal("the view outlived its hour")
	}
	if len(tb.byID) != 0 || len(tb.bySession) != 0 {
		t.Fatalf("an ended view is kept: %d %d", len(tb.byID), len(tb.bySession))
	}
}
