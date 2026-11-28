package usage_test

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
	"petalbook.app/server/internal/usage"
)

var ctx = context.Background()

func value(t *testing.T, st *store.Store, tenant, day, key string) int64 {
	t.Helper()
	n, err := store.UsageDailyValue(ctx, st.Q(), tenant, day, key)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func makeTenant(t *testing.T, st *store.Store, id string) {
	t.Helper()
	now := time.UnixMilli(1_700_000_000_000)
	if err := st.CreateTenant(ctx, store.Tenant{ID: id, Slug: "t" + id, Name: id, Created: now, Updated: now}); err != nil {
		t.Fatal(err)
	}
}

// Concurrent adds are all counted, a flush writes exactly the sum (and what
// is counted while it runs is written by the next), and Pending shows what the
// database does not have yet.
func TestFlushIsExactUnderConcurrentAdds(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC)
		m := usage.NewMeter(func() time.Time { return now })
		makeTenant(t, st, "T000000011")
		const workers, each = 16, 500
		var wg sync.WaitGroup
		var stop atomic.Bool
		flushed := make(chan struct{})
		go func() { // flushes run while the adds do
			defer close(flushed)
			for !stop.Load() {
				if err := m.Flush(ctx, st); err != nil {
					t.Error(err)
					return
				}
			}
		}()
		for w := range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range each {
					m.Add(store.DefaultTenantID, usage.APIRequests, 1)
					m.Add("T000000011", usage.MCPRequests, 2)
					if w == 0 {
						m.Add(store.DefaultTenantID, usage.Mail, 1)
					}
				}
			}()
		}
		wg.Wait()
		stop.Store(true)
		<-flushed
		if err := m.Flush(ctx, st); err != nil {
			t.Fatal(err)
		}
		day := store.UsageDay(now)
		if got := value(t, st, store.DefaultTenantID, day, store.UsageKeyAPIRequests); got != workers*each {
			t.Fatalf("api requests %d, want %d", got, workers*each)
		}
		if got := value(t, st, "T000000011", day, store.UsageKeyMCPRequests); got != workers*each*2 {
			t.Fatalf("mcp requests %d", got)
		}
		if got := value(t, st, store.DefaultTenantID, day, store.UsageKeyMail); got != each {
			t.Fatalf("mail %d", got)
		}
		if m.Pending(store.DefaultTenantID, usage.APIRequests) != 0 {
			t.Fatal("nothing is pending after a flush")
		}
		// An empty flush writes nothing and asks nothing of the database.
		if err := m.Flush(ctx, st); err != nil {
			t.Fatal(err)
		}
	})
}

// Pending counts the unwritten part of today only, and a count made before
// midnight is written to its own day.
func TestPendingAndTheDayChange(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Date(2026, 3, 5, 23, 59, 50, 0, time.UTC)
		m := usage.NewMeter(func() time.Time { return now })
		m.Add(store.DefaultTenantID, usage.Mail, 3)
		if got := m.Pending(store.DefaultTenantID, usage.Mail); got != 3 {
			t.Fatalf("pending %d", got)
		}
		now = now.Add(time.Minute) // past midnight, before the flush
		m.Add(store.DefaultTenantID, usage.Mail, 4)
		if got := m.Pending(store.DefaultTenantID, usage.Mail); got != 4 {
			t.Fatalf("pending today %d, yesterday's three are not today's", got)
		}
		if err := m.Flush(ctx, st); err != nil {
			t.Fatal(err)
		}
		if a, b := value(t, st, store.DefaultTenantID, "2026-03-05", store.UsageKeyMail), value(t, st, store.DefaultTenantID, "2026-03-06", store.UsageKeyMail); a != 3 || b != 4 {
			t.Fatalf("days: %d and %d", a, b)
		}
	})
}

// A tenant that was deleted does not hold the others' counts back, and a
// failing database loses nothing: the counts are made again.
func TestFlushKeepsWhatItCouldNotWrite(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC)
		m := usage.NewMeter(func() time.Time { return now })
		m.Add("T000000099", usage.APIRequests, 5) // no such tenant
		m.Add(store.DefaultTenantID, usage.APIRequests, 7)
		if err := m.Flush(ctx, st); err != nil {
			t.Fatal(err)
		}
		if got := value(t, st, store.DefaultTenantID, "2026-03-05", store.UsageKeyAPIRequests); got != 7 {
			t.Fatalf("%d", got)
		}
		if m.Pending("T000000099", usage.APIRequests) != 0 {
			t.Fatal("the count of a tenant that is gone was kept")
		}
		// The database is closed: the flush fails and the counts stay.
		m.Add(store.DefaultTenantID, usage.APIRequests, 2)
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		if err := m.Flush(cctx, st); err == nil {
			t.Fatal("a flush with a cancelled context succeeded")
		}
		if got := m.Pending(store.DefaultTenantID, usage.APIRequests); got != 2 {
			t.Fatalf("pending after the failed flush %d, want 2", got)
		}
		if err := m.Flush(ctx, st); err != nil {
			t.Fatal(err)
		}
		if got := value(t, st, store.DefaultTenantID, "2026-03-05", store.UsageKeyAPIRequests); got != 9 {
			t.Fatalf("%d", got)
		}
	})
}

// The recount sets the day's totals from the usage rows and the pinned spaces,
// and sets them again (not adds) the next time.
func TestRecountFillsTheTotals(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		makeTenant(t, st, "T000000012")
		for _, s := range []struct {
			id, tenant string
			size       int64
			pin        bool
		}{{"S1", store.DefaultTenantID, 100, true}, {"S2", store.DefaultTenantID, 40, false}, {"S3", "T000000012", 7, true}, {"S4", "T000000012", 5, true}} {
			if err := store.InsertSpace(ctx, q, s.tenant, s.id, s.id, "", "", t0); err != nil {
				t.Fatal(err)
			}
			if _, err := q.Exec(ctx, `UPDATE spaces SET size_bytes = ? WHERE id = ?`, s.size, s.id); err != nil {
				t.Fatal(err)
			}
			if s.pin {
				if err := store.SetSpacePinned(ctx, q, s.id, true); err != nil {
					t.Fatal(err)
				}
			}
		}
		if _, err := q.Exec(ctx, `UPDATE usage SET people = 3, spaces = 2, bytes = 140 WHERE kind = 'tenant' AND id = ?`, store.DefaultTenantID); err != nil {
			t.Fatal(err)
		}
		now := time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC)
		var rc usage.Recounter
		first, err := rc.Run(ctx, st, now)
		if err != nil || first != 8 { // four totals of two tenants
			t.Fatalf("first run wrote %d: %v", first, err)
		}
		// Nothing changed: nothing is written, and a recounter that starts again
		// (a restart) learns what is there from the rows of the day.
		for _, r := range []*usage.Recounter{&rc, {}} {
			if n, err := r.Run(ctx, st, now); err != nil || n != 0 {
				t.Fatalf("a run with nothing changed wrote %d: %v", n, err)
			}
		}
		day := "2026-03-05"
		for _, c := range []struct {
			tenant, key string
			want        int64
		}{
			{store.DefaultTenantID, store.UsageKeyPeople, 3}, {store.DefaultTenantID, store.UsageKeySpaces, 2},
			{store.DefaultTenantID, store.UsageKeyBytes, 140}, {store.DefaultTenantID, store.UsageKeyPinnedBytes, 100},
			{"T000000012", store.UsageKeyPinnedBytes, 12}, {"T000000012", store.UsageKeyPeople, 0},
		} {
			if got := value(t, st, c.tenant, day, c.key); got != c.want {
				t.Fatalf("%s %s: %d, want %d", c.tenant, c.key, got, c.want)
			}
		}
		// Pinning another space is seen by the next recount of the day.
		if err := store.SetSpacePinned(ctx, q, "S2", true); err != nil {
			t.Fatal(err)
		}
		if n, err := rc.Run(ctx, st, now); err != nil || n != 1 {
			t.Fatalf("one value changed, %d written: %v", n, err)
		}
		if got := value(t, st, store.DefaultTenantID, day, store.UsageKeyPinnedBytes); got != 140 {
			t.Fatalf("pinned %d", got)
		}
		// A new day writes the day's rows again.
		if n, err := rc.Run(ctx, st, now.Add(24*time.Hour)); err != nil || n != 8 {
			t.Fatalf("a new day wrote %d: %v", n, err)
		}
	})
}

// Run flushes when the server stops, so a clean stop loses nothing, and sets
// the totals at its start.
func TestRunFlushesWhenItStops(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC)
		m := usage.NewMeter(func() time.Time { return now })
		rctx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			m.Run(rctx, st, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Hour, time.Hour)
		}()
		m.Add(store.DefaultTenantID, usage.MCPRequests, 11)
		cancel()
		<-done
		if got := value(t, st, store.DefaultTenantID, "2026-03-05", store.UsageKeyMCPRequests); got != 11 {
			t.Fatalf("%d", got)
		}
	})
}

// Counting is a lookup and an atomic add: no allocation once the tenant has
// its counters.
func TestAddDoesNotAllocate(t *testing.T) {
	m := usage.NewMeter(nil)
	m.Add("t", usage.APIRequests, 1)
	if n := testing.AllocsPerRun(100, func() { m.Add("t", usage.APIRequests, 1) }); n != 0 {
		t.Fatalf("%v allocations", n)
	}
}

// The last message of a limit goes to one caller only, whatever the number of
// callers at once; a reservation can be undone; a flush that ended since the
// row was read makes the reservation ask for the row again.
func TestReserveTakesTheLastSlotOnce(t *testing.T) {
	m := usage.NewMeter(nil)
	var wg sync.WaitGroup
	var took atomic.Int64
	gen := m.Gen()
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok, _ := m.Reserve("t", usage.Mail, 1, 4, 10, gen); ok { // 4 are written already
				took.Add(1)
			}
		}()
	}
	wg.Wait()
	if took.Load() != 6 {
		t.Fatalf("%d reservations, 6 slots were left", took.Load())
	}
	d, _, _ := m.Reserve("t", usage.Mail, 1, 4, 10, gen) // refused: the day of the attempt
	m.AddAt(d, "t", usage.Mail, -1)                      // one of the six is given back
	if _, ok, _ := m.Reserve("t", usage.Mail, 1, 4, 10, gen); !ok {
		t.Fatal("the undone slot was not free")
	}
	if _, ok, stale := m.Reserve("t", usage.Mail, 1, 4, 10, gen+1); ok || !stale {
		t.Fatal("a stale row was taken as current")
	}
}

// FlushTenant writes one tenant's counts and leaves the others for their own flush.
func TestFlushTenantWritesOnlyThatTenant(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC)
		m := usage.NewMeter(func() time.Time { return now })
		makeTenant(t, st, "T000000014")
		m.Add(store.DefaultTenantID, usage.APIRequests, 3)
		m.Add("T000000014", usage.APIRequests, 4)
		if err := m.FlushTenant(ctx, st, "T000000014"); err != nil {
			t.Fatal(err)
		}
		if value(t, st, "T000000014", "2026-03-05", store.UsageKeyAPIRequests) != 4 || value(t, st, store.DefaultTenantID, "2026-03-05", store.UsageKeyAPIRequests) != 0 {
			t.Fatal("one tenant's flush wrote another's")
		}
		if m.Pending(store.DefaultTenantID, usage.APIRequests) != 3 {
			t.Fatal("the other tenant's count was taken")
		}
	})
}

// A flush with nothing to write does not tell Reserve that a flush ended; one
// that writes does. And a slot given back after midnight is given back on the day
// it was taken.
func TestGenChangesOnlyWhenRowsAreWritten(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Date(2026, 3, 5, 23, 59, 59, 0, time.UTC)
		m := usage.NewMeter(func() time.Time { return now })
		g := m.Gen()
		if err := m.Flush(ctx, st); err != nil || m.Gen() != g {
			t.Fatalf("an empty flush changed gen: %v", err)
		}
		d, ok, _ := m.Reserve("t", usage.Mail, 1, 0, 10, g)
		if !ok {
			t.Fatal("not reserved")
		}
		now = now.Add(2 * time.Second) // midnight passes
		m.AddAt(d, store.DefaultTenantID, usage.Mail, 0)
		m.AddAt(d, "t", usage.Mail, -1) // given back on its own day
		m.Add(store.DefaultTenantID, usage.Mail, 2)
		if p := m.Pending(store.DefaultTenantID, usage.Mail); p != 2 {
			t.Fatalf("today's pending %d", p)
		}
		if err := m.Flush(ctx, st); err != nil || m.Gen() == g {
			t.Fatalf("a flush that wrote did not change gen: %v", err)
		}
		if got := value(t, st, store.DefaultTenantID, "2026-03-06", store.UsageKeyMail); got != 2 {
			t.Fatalf("%d", got)
		}
	})
}
