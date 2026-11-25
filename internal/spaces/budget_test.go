package spaces

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/index"
	"github.com/KKazuhaK/PetalBook/internal/space"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

// openIDs lists the spaces that are open, once the trims under way are done.
func openIDs(m *Manager) []string {
	m.Trim() // waits for a background trim (trimMu); closes nothing more here
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []string
	for id := range m.open {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func sorted(ids ...string) []string { slices.Sort(ids); return ids }

// Over the budget, the least recently used idle spaces are closed until the
// rest fits; the order is that of use, not of opening.
func TestSpacesOverTheMemoryBudgetAreClosedLeastRecentlyUsedFirst(t *testing.T) {
	st := storetest.Open(t, "sqlite")
	m, clk := limitedManager(t, st, func(o *Options) {
		o.MemoryBudget, o.MaxOpen, o.MaxSpaces, o.IdleAfter = 250, 100, 0, time.Hour
	})
	m.testCost = func(string, *space.Space) int64 { return 100 }
	u := addUser(t, st, "U1", "one")
	var ids []string
	for _, n := range []string{"A", "B", "C", "D"} {
		in, err := m.Create(ctx, u.ID, n)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, in.ID)
		clk.add(time.Second)
	}
	a, b, c, d := ids[0], ids[1], ids[2], ids[3]
	if got := openIDs(m); !slices.Equal(got, sorted(c, d)) {
		t.Fatalf("open %v, want C and D %v", got, sorted(c, d))
	}
	if got := m.OpenCost(); got != 200 {
		t.Fatalf("open cost %d", got)
	}
	// C is used again: D is now the least recently used.
	h, err := m.Open(ctx, u, c)
	if err != nil {
		t.Fatal(err)
	}
	clk.add(time.Second)
	h.Release()
	clk.add(time.Second)
	h, err = m.Open(ctx, u, a) // reopened: over the budget again
	if err != nil {
		t.Fatal(err)
	}
	h.Release()
	if got := openIDs(m); !slices.Equal(got, sorted(a, c)) {
		t.Fatalf("open %v, want A and C %v (D least recently used)", got, sorted(a, c))
	}
	_ = b
}

// A space in use is never closed for the budget, however much it costs; it is
// once it is released.
func TestASpaceInUseIsNotClosedOverTheBudget(t *testing.T) {
	st := storetest.Open(t, "sqlite")
	m, clk := limitedManager(t, st, func(o *Options) {
		o.MemoryBudget, o.MaxOpen, o.MaxSpaces, o.IdleAfter = 50, 100, 0, time.Hour
	})
	m.testCost = func(string, *space.Space) int64 { return 100 }
	u := addUser(t, st, "U1", "one")
	a, err := m.Create(ctx, u.ID, "A")
	if err != nil {
		t.Fatal(err)
	}
	h, err := m.Open(ctx, u, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	clk.add(time.Second)
	b, err := m.Create(ctx, u.ID, "B")
	if err != nil {
		t.Fatal(err)
	}
	if got := openIDs(m); !slices.Equal(got, []string{a.ID}) {
		t.Fatalf("open %v, want only A (in use) %s; B %s is idle", got, a.ID, b.ID)
	}
	if _, err := h.Space.List(index.Query{}); err != nil {
		t.Fatal(err)
	}
	h.Release()
	if got := openIDs(m); len(got) != 0 {
		t.Fatalf("open %v after A was released, over the budget", got)
	}
}

// Requests, trims that close everything idle, and rebuilds (which take the
// server's slots for big syncs) run together without a deadlock.
func TestAcquireTrimAndRebuildDoNotDeadlock(t *testing.T) {
	st := storetest.Open(t, "sqlite")
	m, _ := limitedManager(t, st, func(o *Options) {
		o.MemoryBudget, o.MaxOpen, o.MaxSpaces = 1, 3, 0
	})
	u := addUser(t, st, "U1", "one")
	var ids []string
	for _, n := range []string{"A", "B", "C", "D", "E"} {
		in, err := m.Create(ctx, u.ID, n)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, in.ID)
	}
	const rounds = 8
	done := make(chan struct{})
	var wg sync.WaitGroup
	for g := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range rounds {
				h, err := m.Open(ctx, u, ids[(g+i)%len(ids)])
				if err != nil {
					t.Error(err)
					return
				}
				if (g+i)%4 == 0 {
					if _, err := h.Space.Reindex(); err != nil {
						t.Error(err)
					}
				} else if _, err := h.Space.List(index.Query{Text: "x"}); err != nil {
					t.Error(err)
				}
				h.Release()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range rounds * 3 {
			m.Trim()
		}
	}()
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Minute):
		t.Fatal("requests, trims and rebuilds did not finish: deadlock")
	}
	if got := openIDs(m); len(got) != 0 {
		t.Fatalf("open after all were released, over a budget of 1 byte: %v", got)
	}
}
