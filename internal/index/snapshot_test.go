package index

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
)

// The pages of the snapshot tests: `flipping` pages, half of them in state A
// (status todo, prio low) and half in state B (status done, prio high) at any
// time, and `constant` pages that are always todo and high. A write swaps one
// page of A with one of B in one transaction, so at every committed state:
//
//   - status todo AND prio high is exactly the constant pages (no state of a
//     flipping page meets both);
//   - status todo AND prio low is exactly flipping/2 pages, all in state A;
//   - status done is exactly flipping/2 pages, all in state B;
//   - the count by status is todo: flipping/2+constant, done: flipping/2.
//
// A listing or count that mixes two states breaks one of these.
const (
	flipping = 310
	constant = 10
)

func flipPage(i int, a bool) string {
	status, prio := "done", "high"
	if a {
		status, prio = "todo", "low"
	}
	return fmt.Sprintf("---\nid: F%09d\ntitle: f%04d\nstatus: %s\nprio: %s\n---\nx\n", i, i, status, prio)
}

// flipSpace opens an index of the pages above, and starts a writer that swaps
// pages until the test ends.
func flipSpace(t *testing.T) *Index {
	t.Helper()
	x := openIndex(t)
	m := newMem()
	inA := make([]bool, flipping)
	for i := range flipping {
		inA[i] = i%2 == 0
		m.put(fmt.Sprintf("a/f%04d.md", i), flipPage(i, inA[i]))
	}
	for i := range constant {
		m.put(fmt.Sprintf("a/c%04d.md", i), fmt.Sprintf("---\nid: C%09d\ntitle: c%04d\nstatus: todo\nprio: high\n---\nx\n", i, i))
	}
	syncAll(t, x, m, false)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		rng := rand.New(rand.NewSource(1))
		for {
			select {
			case <-stop:
				return
			default:
			}
			var a, b int
			for a = rng.Intn(flipping); !inA[a]; a = rng.Intn(flipping) {
			}
			for b = rng.Intn(flipping); inA[b]; b = rng.Intn(flipping) {
			}
			inA[a], inA[b] = false, true
			pa, pb := fmt.Sprintf("a/f%04d.md", a), fmt.Sprintf("a/f%04d.md", b)
			m.put(pa, flipPage(a, false))
			m.put(pb, flipPage(b, true))
			if _, err := x.Refresh(m, []string{pa, pb}, nil); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	t.Cleanup(func() { close(stop); wg.Wait() })
	return x
}

// flipRounds is how many times each reader reads each question.
func flipRounds() int {
	if raceDetector {
		return 6 // each reader still reads each of the five orders at least once
	}
	return 60
}

// checkFlipListing lists q, and every page that follows, and checks each
// against the invariant: every row meets the filters as the row itself shows
// it; a listing of one page has exactly want rows (want < 0: any number).
func checkFlipListing(t *testing.T, x *Index, q Query, status, prio string, want int) {
	t.Helper()
	q.Props = true
	for n := 0; ; n++ {
		r, err := x.List(q)
		if err != nil {
			t.Error(err)
			return
		}
		for _, s := range r.Items {
			var p struct{ Status, Prio string }
			if err := json.Unmarshal(s.Props, &p); err != nil {
				t.Error(err)
				return
			}
			if p.Status != status || (prio != "" && p.Prio != prio) {
				t.Errorf("%+v: page %s is listed with status %s, prio %s", q.Filters, s.ID, p.Status, p.Prio)
			}
		}
		if n == 0 && r.Next == "" && want >= 0 && len(r.Items) != want {
			t.Errorf("%+v, sort %q: %d pages, want %d", q.Filters, q.Sort, len(r.Items), want)
		}
		if r.Next == "" || n > 2*flipping {
			return
		}
		q.Cursor = r.Next
	}
}

// TestListingsReadOneSnapshot lists the flipping space by every way a listing
// can be read, while a writer swaps pages, and checks that each page of a
// listing holds exactly the pages that met its conditions at one moment.
func TestListingsReadOneSnapshot(t *testing.T) {
	t.Parallel()
	routes := []struct {
		name              string
		lead, set, budget int
	}{
		{"walk", 0, 0, 1 << 30},
		{"sets, order scanned", 0, 0, 0},
		{"sets, put in order", 0, 1 << 30, 0},
		{"smallest first, put in order", 1 << 30, 1 << 30, 0},
		{"smallest first, order scanned", 1 << 30, 0, 0},
	}
	sorts := []struct {
		sort string
		opts []string
	}{{"", nil}, {"title", nil}, {"-updated", nil}, {"prop:prio", []string{"low", "high"}}, {"-prop:status", nil}}
	f := func(key string, v ...any) Filter { return Filter{Key: key, Op: "in", Values: v} }
	half := flipping / 2
	// Each route reads its own index, with its own writer, beside the others.
	var shared, serial atomic.Int64
	t.Run("routes", func(t *testing.T) {
		for _, rt := range routes {
			t.Run(rt.name, func(t *testing.T) {
				t.Parallel()
				x := flipSpace(t)
				x.plan.part = 20 // many pages are looked up in parts, on several connections
				defer func() { shared.Add(x.shared.Load()); serial.Add(x.serial.Load()) }()
				setPlan(t, x, rt.lead, rt.set, rt.budget)
				var wg sync.WaitGroup
				for w := range 3 {
					wg.Add(1)
					go func() {
						defer wg.Done()
						for i := range flipRounds() {
							s := sorts[(i+w)%len(sorts)]
							a := "a"
							q := Query{Folder: &a, Recursive: i%2 == 1, Sort: s.sort, PropOptions: s.opts, Limit: 200}
							q.Filters = []Filter{f("status", "todo"), f("prio", "high")}
							checkFlipListing(t, x, q, "todo", "high", constant)
							q.Filters = []Filter{f("prio", "low"), f("status", "todo")}
							checkFlipListing(t, x, q, "todo", "low", half)
							q.Filters = []Filter{f("status", "done")}
							checkFlipListing(t, x, q, "done", "high", half)
							q.Limit = 40 // across pages: every page is right on its own
							q.Filters = []Filter{f("status", "todo")}
							checkFlipListing(t, x, q, "todo", "", -1)
						}
					}()
				}
				wg.Wait()
			})
		}
	})
	t.Logf("snapshot shared by other connections %d times, read in turn %d times", shared.Load(), serial.Load())
	if shared.Load() == 0 {
		t.Error("no read had other connections share its snapshot")
	}
}

// TestCountsReadOneSnapshot counts the flipping space by its values, with and
// without the remembered counts of Groups, while a writer swaps pages: every
// count is that of one moment.
func TestCountsReadOneSnapshot(t *testing.T) {
	t.Parallel()
	x := flipSpace(t)
	half := flipping / 2
	routes := []struct {
		name string
		lead int
	}{{"sets", 0}, {"smallest first", 1 << 30}}
	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			setPlan(t, x, rt.lead, defaultPlan.set, defaultPlan.budget(0))
			var wg sync.WaitGroup
			var cached atomic.Int64
			for w := range 3 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					a := "a"
					for i := range 4 * flipRounds() {
						groups := x.Groups
						if (i+w)%2 == 1 {
							groups = x.countGroups
						}
						q := GroupQuery{Folder: &a, Recursive: i%3 == 0, Key: "status"}
						checkGroups(t, groups, q, map[string]int{"todo": half + constant, "done": half})
						q.Key, q.Filters = "prio", []Filter{{Key: "status", Op: "in", Values: []any{"todo"}}}
						checkGroups(t, groups, q, map[string]int{"low": half, "high": constant})
						q.Key, q.Filters = "status", []Filter{{Key: "prio", Op: "in", Values: []any{"high"}}}
						checkGroups(t, groups, q, map[string]int{"todo": constant, "done": half})
						if g := x.gen.Load(); g%2 == 0 {
							if _, ok := x.groups.get(mustKey(q), g); ok {
								cached.Add(1)
							}
						}
					}
				}()
			}
			wg.Wait()
			t.Logf("%d counts found remembered", cached.Load())
		})
	}
}

func mustKey(q GroupQuery) string {
	k, _ := q.cacheKey()
	return k
}

func checkGroups(t *testing.T, groups func(GroupQuery) (GroupResult, error), q GroupQuery, want map[string]int) {
	t.Helper()
	r, err := groups(q)
	if err != nil {
		t.Error(err)
		return
	}
	got := map[string]int{}
	for _, g := range r.Groups {
		got[g.Value] = g.Count
	}
	if fmt.Sprint(got) != fmt.Sprint(want) || r.Empty != 0 {
		t.Errorf("groups of %s, %+v: %v (empty %d), want %v", q.Key, q.Filters, got, r.Empty, want)
	}
}

// TestGenerationMovesAtTheCommit checks that a write leaves gen even and
// moved, and that a read begun after it knows its generation.
func TestGenerationMovesAtTheCommit(t *testing.T) {
	t.Parallel()
	x := openIndex(t)
	before := x.gen.Load()
	if _, err := x.Put("a.md", []byte("---\nid: A\n---\nx\n"), clock()); err != nil {
		t.Fatal(err)
	}
	if err := x.AddFolder("b/c"); err != nil {
		t.Fatal(err)
	}
	g := x.gen.Load()
	if g%2 != 0 || g != before+4 {
		t.Fatalf("gen %d after two writes from %d", g, before)
	}
	r, err := x.begin()
	if err != nil {
		t.Fatal(err)
	}
	defer r.end()
	if !r.known || r.at != g {
		t.Fatalf("read at %d (known %v), gen %d", r.at, r.known, g)
	}
	reads, done := r.parallel(3)
	defer done()
	if len(reads) != 3 {
		t.Fatalf("%d reads of the snapshot, want 3", len(reads))
	}
}
