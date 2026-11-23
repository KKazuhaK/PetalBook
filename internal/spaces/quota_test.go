package spaces

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/page"
	"github.com/KKazuhaK/PetalBook/internal/space"
	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

func limitsOf(person, tenant int64) func(ctxT, store.User) (SpaceLimits, error) {
	return func(ctxT, store.User) (SpaceLimits, error) { return SpaceLimits{Person: person, Tenant: tenant}, nil }
}

// The number of spaces is the one quota that is refused: a person's, then a
// tenant's, with what ran out; a deleted space gives the room back.
func TestTheNumberOfSpacesIsRefusedAtThePersonsAndTheTenantsLimit(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		m := testManager(t, st)
		m.SetQuota(limitsOf(2, 3), nil)
		one := addUser(t, st, "U1", "one")
		two := addUser(t, st, "U2", "two")
		a, err := m.Create(ctx, one.ID, "A")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.Create(ctx, one.ID, "B"); err != nil {
			t.Fatal(err)
		}
		_, err = m.Create(ctx, one.ID, "C")
		var qe *QuotaError
		if !errors.Is(err, ErrLimit) || !errors.As(err, &qe) || qe.Quota != "spaces" || qe.Limit != 2 || qe.Used != 2 || qe.Tenant {
			t.Fatalf("%v", err)
		}
		// The tenant's total: one made it three, so the next of anyone is refused.
		if _, err := m.Create(ctx, two.ID, "D"); err != nil {
			t.Fatal(err)
		}
		_, err = m.Create(ctx, two.ID, "E")
		if !errors.As(err, &qe) || qe.Limit != 3 || qe.Used != 3 || !qe.Tenant {
			t.Fatalf("%v", err)
		}
		// A refused creation left nothing behind.
		if rows, _, _ := store.ListAllSpaces(ctx, st.Q(), store.AllTenants(), store.SpaceFilter{}); len(rows) != 3 {
			t.Fatalf("%d spaces", len(rows))
		}
		if _, err := m.DeleteSpace(ctx, store.AllTenants(), a.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Create(ctx, one.ID, "F"); err != nil {
			t.Fatalf("the room was not given back: %v", err)
		}
		// No limit (0) is none.
		m.SetQuota(limitsOf(0, 0), nil)
		for i := 0; i < 4; i++ {
			if _, err := m.Create(ctx, one.ID, "G"+strconv.Itoa(i)); err != nil {
				t.Fatal(err)
			}
		}
	})
}

// Racing creations cannot pass the limit between them.
func TestRacingCreationsCannotPassTheLimit(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		m := testManager(t, st)
		m.SetQuota(limitsOf(3, 0), nil)
		u := addUser(t, st, "U1", "one")
		var wg sync.WaitGroup
		var mu sync.Mutex
		made, refused := 0, 0
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := m.Create(ctx, u.ID, "S"+strconv.Itoa(i))
				mu.Lock()
				defer mu.Unlock()
				if err == nil {
					made++
				} else if errors.Is(err, ErrLimit) {
					refused++
				} else {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		if made != 3 || refused != 5 {
			t.Fatalf("made %d, refused %d", made, refused)
		}
		if n, err := st.RecountUsage(ctx); err != nil || n != 0 {
			t.Fatalf("recount corrected %d rows: %v", n, err)
		}
	})
}

func makePages(t *testing.T, m *Manager, owner store.User, id string, n int) {
	t.Helper()
	h, err := m.Open(ctx, owner, id)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Release()
	for i := 0; i < n; i++ {
		if _, err := h.Space.Create(space.Author{}, space.CreateInput{Meta: page.Meta{{Key: "title", Value: "Page " + strconv.Itoa(i)}}, Body: "text of the page\n"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.Space.Flush(); err != nil {
		t.Fatal(err)
	}
}

// A measurement stores the bytes and the page count and adds them to the owner
// and the tenant; the hook is told whom it reached. The pages come from the open
// space, or from the index file when the space is closed, and a space whose
// index is gone keeps the count it had.
func TestAMeasurementCountsBytesAndPagesAndTellsTheHook(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		m := testManager(t, st)
		var mu sync.Mutex
		var events []UsageEvent
		m.SetQuota(nil, func(_ ctxT, ev UsageEvent) { mu.Lock(); events = append(events, ev); mu.Unlock() })
		owner := addUser(t, st, "U1", "owner")
		info, err := m.Create(ctx, owner.ID, "Team")
		if err != nil {
			t.Fatal(err)
		}
		makePages(t, m, owner, info.ID, 3)
		n, _, err := m.Resize(ctx, store.AllTenants(), info.ID)
		if err != nil || n == 0 {
			t.Fatalf("%d %v", n, err)
		}
		u, _ := store.GetUsage(ctx, st.Q(), store.UsageUser, owner.ID)
		tn, _ := store.GetUsage(ctx, st.Q(), store.UsageTenant, store.DefaultTenantID)
		if u.Spaces < 1 || u.Bytes != n || u.Pages != 3 || tn.Bytes != n || tn.Pages != 3 {
			t.Fatalf("%+v %+v (%d bytes)", u, tn, n)
		}
		mu.Lock()
		// One event for the new space (its owner and tenant), one for the measurement.
		if len(events) != 2 || events[0].Everyone || len(events[0].Touched) != 1 || events[0].Touched[0] != (store.Touched{UserID: owner.ID, TenantID: store.DefaultTenantID}) ||
			events[1].Everyone || len(events[1].Touched) != 1 || events[1].Touched[0].UserID != owner.ID {
			t.Fatalf("%+v", events)
		}
		mu.Unlock()

		// The space is closed and the pages are counted from its index file.
		if err := m.Close(); err != nil {
			t.Fatal(err)
		}
		m2, err := NewAccounts(Options{Store: st, DataDir: m.opt.DataDir, Committer: "Svc <svc@example.test>", CloseWait: 200 * time.Millisecond, CommitIdle: time.Hour, CommitMax: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		defer m2.Close()
		if _, ok := m2.open[info.ID]; ok {
			t.Fatal("the space is open")
		}
		if c, ok := m2.pageCount(ctx, info.ID); !ok || c != 3 {
			t.Fatalf("closed space: %d %v", c, ok)
		}
		if _, _, err := m2.Resize(ctx, store.AllTenants(), info.ID); err != nil {
			t.Fatal(err)
		}
		if _, ok := m2.open[info.ID]; ok {
			t.Fatal("measuring opened the space")
		}
		usageHas(t, st, owner.ID, -1, 3)
		// No index (a cache that was cleared): the bytes are measured, the pages stay.
		_, state := m2.paths(info.ID)
		if err := os.RemoveAll(filepath.Join(state, "index.db")); err != nil {
			t.Fatal(err)
		}
		if _, ok := m2.pageCount(ctx, info.ID); ok {
			t.Fatal("a space without an index has a page count")
		}
		if _, _, err := m2.Resize(ctx, store.AllTenants(), info.ID); err != nil {
			t.Fatal(err)
		}
		usageHas(t, st, owner.ID, -1, 3)
	})
}

func usageHas(t *testing.T, st *store.Store, user string, bytes, pages int64) {
	t.Helper()
	u, err := store.GetUsage(ctx, st.Q(), store.UsageUser, user)
	if err != nil {
		t.Fatal(err)
	}
	if bytes >= 0 && u.Bytes != bytes || u.Pages != pages {
		t.Fatalf("%+v, want bytes %d pages %d", u, bytes, pages)
	}
}

// The background pass puts a drifted row right by itself: once when it starts (a
// crash may have left a difference) and then every RecountEvery, and tells the
// hook that everyone's usage was looked at. A re-measure corrects a space's own
// numbers the same way.
func TestTheBackgroundRecountCorrectsDriftAndTellsTheHook(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		needGit(t)
		dir, err := os.MkdirTemp("", "petalbook-spaces-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
		m, err := NewAccounts(Options{Store: st, DataDir: filepath.Join(dir, "data"), Committer: "Svc <svc@example.test>", CloseWait: 200 * time.Millisecond,
			CommitIdle: time.Hour, CommitMax: time.Hour, SizeEvery: time.Hour, RecountEvery: 20 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { m.Close() })
		owner := addUser(t, st, "U1", "owner")
		if _, err := m.Create(ctx, owner.ID, "Team"); err != nil {
			t.Fatal(err)
		}
		// A crash left the person's row wrong.
		if _, err := st.Q().Exec(ctx, `UPDATE usage SET spaces = 9, bytes = 12345 WHERE kind = 'user' AND id = 'U1'`); err != nil {
			t.Fatal(err)
		}
		everyone := make(chan struct{}, 100)
		m.SetQuota(nil, func(_ ctxT, ev UsageEvent) {
			if ev.Everyone {
				select {
				case everyone <- struct{}{}:
				default:
				}
			}
		})
		bg, stop := ctxWithCancel()
		done := make(chan struct{})
		go func() { defer close(done); m.Run(bg) }()
		deadline := time.After(10 * time.Second)
		for {
			u, _ := store.GetUsage(ctx, st.Q(), store.UsageUser, "U1")
			if u.Spaces == 1 && u.Bytes == 0 {
				break
			}
			select {
			case <-deadline:
				t.Fatalf("the recount did not correct %+v", u)
			case <-time.After(10 * time.Millisecond):
			}
		}
		select {
		case <-everyone:
		case <-time.After(10 * time.Second):
			t.Fatal("the hook was not told that everyone's usage was recounted")
		}
		stop()
		<-done
	})
}

type ctxT = context.Context

func ctxWithCancel() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}

// What one measuring turn costs for a space of 20,000 pages: the walk of its
// files, the page count from the open space and from the index file of a closed
// one. It prints the timings (the design notes quote them): set PETALBOOK_PERF=1.
func TestMeasuringCostsOfALargeSpace(t *testing.T) {
	if os.Getenv("PETALBOOK_PERF") == "" {
		t.Skip("set PETALBOOK_PERF=1 to measure")
	}
	const pages = 20000
	st := storetest.Open(t, "sqlite")
	m := testManager(t, st)
	owner := addUser(t, st, "U1", "owner")
	info, err := m.Create(ctx, owner.ID, "Big")
	if err != nil {
		t.Fatal(err)
	}
	root, _ := m.paths(info.ID)
	for i := 0; i < pages; i++ {
		dir := filepath.Join(root, "notes", strconv.Itoa(i/200))
		if i%200 == 0 {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		body := "---\nid: " + page.NewID(time.Now().Add(time.Duration(i)*time.Millisecond)) + "\ntitle: Page " + strconv.Itoa(i) + "\ncreated: 2026-11-23\n---\ntext of the page " + strconv.Itoa(i) + "\n"
		if err := os.WriteFile(filepath.Join(dir, "p"+strconv.Itoa(i)+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Close it and open it again so that the index reads the files.
	if err := m.evict(ctx, info.ID); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	h, err := m.Open(ctx, owner, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	h.Release()
	t.Logf("PERF indexing %d pages when the space is opened%s%v", pages, "                    ", time.Since(start).Round(time.Millisecond))
	if err := m.evict(ctx, info.ID); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	c, ok := m.pageCount(ctx, info.ID)
	t.Logf("PERF page count of a closed space, from its index file (%d, %v)%s%v", c, ok, "          ", time.Since(start).Round(time.Microsecond))
	start = time.Now()
	n, _, err := m.Resize(ctx, store.AllTenants(), info.ID)
	t.Logf("PERF a whole measuring turn of the closed space (%d bytes)%s%v", n, "                ", time.Since(start).Round(time.Millisecond))
	if err != nil || c < pages-100 { // a few generated ids collide
		t.Fatal(err, c)
	}
	h, err = m.Open(ctx, owner, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Release()
	start = time.Now()
	c, ok = m.pageCount(ctx, info.ID)
	t.Logf("PERF page count of an open space (%d, %v)%s%v", c, ok, "                              ", time.Since(start).Round(time.Microsecond))
}
