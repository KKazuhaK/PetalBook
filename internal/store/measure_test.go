package store_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// The background measurement keeps its queue in space_measure: a new space is
// in it, never measured, and comes first; a measurement that finds what the one
// before found writes the queue's row and nothing else; the tenant's row is never
// stamped, its time is read from the queue.
func TestMeasurementQueue(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
		q := st.Q()
		mkUser(t, st, "U1", "u1", "member")
		for _, id := range []string{"S1", "S2"} {
			if err := store.InsertSpace(ctx, q, store.DefaultTenantID, id, id, "", "", now); err != nil {
				t.Fatal(err)
			}
			if err := store.AddMember(ctx, q, id, "U1", "owner", now); err != nil {
				t.Fatal(err)
			}
		}
		next := func(older time.Time) string {
			t.Helper()
			id, ok, err := store.NextSpaceToMeasure(ctx, q, older)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				return ""
			}
			return id
		}
		if got := next(now); got != "S1" {
			t.Fatalf("the first never measured: %q", got)
		}
		// The first measurement of an empty space finds nothing to count, and
		// still says when the person's spaces were measured.
		if _, err := st.RecordMeasurement(ctx, "S1", store.Measurement{Bytes: 0, Pages: 0, HavePages: true, At: now}); err != nil {
			t.Fatal(err)
		}
		if u := usageIs(t, st, store.UsageUser, "U1", 2, 0, 0); !u.Measured.Equal(now) {
			t.Fatalf("the person's measured: %v", u.Measured)
		}
		if got := next(now.Add(time.Hour)); got != "S2" {
			t.Fatalf("never measured before measured: %q", got)
		}
		at2 := now.Add(time.Minute)
		if _, err := st.RecordMeasurement(ctx, "S2", store.Measurement{Bytes: 500, Pages: 3, HavePages: true, At: at2}); err != nil {
			t.Fatal(err)
		}
		if got := next(now.Add(30 * time.Second)); got != "S1" {
			t.Fatalf("the oldest measured before the bound: %q", got)
		}
		if got := next(now); got != "" {
			t.Fatalf("nothing is older than the bound: %q", got)
		}
		// The tenant's time is the newest of the queue; its row was never stamped.
		if u, _ := store.GetUsage(ctx, q, store.UsageTenant, store.DefaultTenantID); !u.Measured.Equal(at2) || u.Bytes != 500 {
			t.Fatalf("tenant %+v", u)
		}
		if n := count(t, st, `SELECT COUNT(*) FROM usage WHERE kind = 'tenant' AND measured <> 0`); n != 0 {
			t.Fatal("the tenant's row was stamped")
		}
		if l, _ := store.UsageOfTenants(ctx, q, []string{store.DefaultTenantID}); !l[store.DefaultTenantID].Measured.Equal(at2) {
			t.Fatalf("%+v", l)
		}
		if sum, err := store.GetStorageSummary(ctx, q, store.AllTenants()); err != nil || !sum.Measured.Equal(at2) {
			t.Fatalf("%+v %v", sum, err)
		}
		// The same again: only the queue's row is written.
		at3 := now.Add(2 * time.Minute)
		var before, after int64
		changes := func() (n int64) {
			t.Helper()
			stmt := `SELECT total_changes()`
			if st.Dialect() == store.Postgres {
				stmt = `SELECT n_tup_upd + n_tup_ins + n_tup_hot_upd FROM pg_stat_xact_user_tables WHERE relname = 'spaces'`
			}
			if err := st.Tx(ctx, func(q store.Q) error { return q.QueryRow(ctx, stmt).Scan(&n) }); err != nil {
				t.Fatal(err)
			}
			return n
		}
		spacesVersion := func() (v string) {
			if st.Dialect() == store.Postgres {
				q.QueryRow(ctx, `SELECT xmin::text FROM spaces WHERE id = 'S2'`).Scan(&v)
			}
			return v
		}
		usageVersion := func() (v string) {
			if st.Dialect() == store.Postgres {
				q.QueryRow(ctx, `SELECT string_agg(xmin::text, ',' ORDER BY kind, id) FROM usage`).Scan(&v)
			}
			return v
		}
		before = changes()
		sv, uv := spacesVersion(), usageVersion()
		if _, err := st.RecordMeasurement(ctx, "S2", store.Measurement{Bytes: 500, Pages: 3, HavePages: true, At: at3}); err != nil {
			t.Fatal(err)
		}
		after = changes()
		if st.Dialect() == store.SQLite && after-before != 1 {
			t.Errorf("an unchanged measurement changed %d rows, want 1 (the queue's)", after-before)
		}
		if spacesVersion() != sv || usageVersion() != uv {
			t.Error("an unchanged measurement wrote the space's or a usage row")
		}
		if u, _ := store.GetUsage(ctx, q, store.UsageTenant, store.DefaultTenantID); !u.Measured.Equal(at3) {
			t.Fatalf("the tenant's time did not follow the queue: %v", u.Measured)
		}
		// A space that is missing from the queue is put back by the daily pass.
		if _, err := q.Exec(ctx, `DELETE FROM space_measure WHERE space_id = 'S1'`); err != nil {
			t.Fatal(err)
		}
		if n, err := st.RecountUsage(ctx); err != nil || n != 1 {
			t.Fatalf("recount: %d %v", n, err)
		}
		if got := next(now); got != "S1" {
			t.Fatalf("the space put back is never measured: %q", got)
		}
	})
}

// The queue's two questions and the tenant's time are lookups of its indexes.
func TestMeasurementQueueUsesItsIndexes(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Now().UnixMilli()
		const n = 3000
		tenants := []string{store.DefaultTenantID}
		for i := 1; i < 20; i++ {
			id := fmt.Sprintf("T%09d", i)
			if err := st.CreateTenant(ctx, store.Tenant{ID: id, Slug: id, Name: id, Created: time.Now(), Updated: time.Now()}); err != nil {
				t.Fatal(err)
			}
			tenants = append(tenants, id)
		}
		bulk(t, st, "spaces", "id, name, name_key, created, tenant_id", n, 200, func(i int) []any {
			return []any{fmt.Sprintf("S%05d", i), "s", "s", now, tenants[i%len(tenants)]}
		})
		bulk(t, st, "space_measure", "space_id, tenant_id, size_at, measured_at", n, 200, func(i int) []any {
			var at any = now - int64(i)*1000
			if i%1000 == 0 {
				at = nil
			}
			return []any{fmt.Sprintf("S%05d", i), tenants[i%len(tenants)], at, at}
		})
		withDearScans(t, st, func(q store.Q) {
			pq := &planQ{Q: q, d: st.Dialect()}
			if _, _, err := store.NextSpaceToMeasure(ctx, pq, time.Now()); err != nil {
				t.Fatal(err)
			}
			for _, p := range pq.plans {
				if !usesIndex(st, p, "space_measure_due") {
					t.Errorf("the queue is not read by its index:\n%s", p)
				}
			}
			plan := explain(t, st, q, `SELECT COALESCE(MAX(measured_at), 0) FROM space_measure WHERE tenant_id = ?`, tenants[7])
			if !strings.Contains(plan, "space_measure_tenant") {
				t.Errorf("the tenant's time is not one lookup:\n%s", plan)
			}
		})
	})
}

// A measurement that failed moves the space to the back of the queue and is not
// a measurement: the tenant's time (and the scope's) stays where it was.
func TestFailedMeasurementIsNotCounted(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
		q := st.Q()
		mkUser(t, st, "U1", "u1", "member")
		if err := store.InsertSpace(ctx, q, store.DefaultTenantID, "S1", "S1", "", "", now); err != nil {
			t.Fatal(err)
		}
		if err := store.AddMember(ctx, q, "S1", "U1", "owner", now); err != nil {
			t.Fatal(err)
		}
		measured := func() time.Time {
			t.Helper()
			u, err := store.GetUsage(ctx, q, store.UsageTenant, store.DefaultTenantID)
			if err != nil {
				t.Fatal(err)
			}
			sum, err := store.GetStorageSummary(ctx, q, store.AllTenants())
			if err != nil || !sum.Measured.Equal(u.Measured) {
				t.Fatalf("summary %v (%v), tenant %v", sum.Measured, err, u.Measured)
			}
			l, err := store.UsageOfTenants(ctx, q, []string{store.DefaultTenantID})
			if err != nil || !l[store.DefaultTenantID].Measured.Equal(u.Measured) {
				t.Fatalf("list %+v %v", l, err)
			}
			return u.Measured
		}
		// Never measured, tried and failed: still never measured, but out of the way.
		failed := now.Add(time.Minute)
		if err := store.SkipSpaceSize(ctx, q, "S1", failed); err != nil {
			t.Fatal(err)
		}
		if got := measured(); !got.IsZero() {
			t.Fatalf("a failure counted as a measurement: %v", got)
		}
		if _, ok, _ := store.NextSpaceToMeasure(ctx, q, failed); ok {
			t.Fatal("the failing space is tried again at once")
		}
		if _, ok, _ := store.NextSpaceToMeasure(ctx, q, failed.Add(time.Second)); !ok {
			t.Fatal("the failing space never comes round again")
		}
		// Measured, then a failure: the tenant keeps the time of the measurement.
		if _, err := st.RecordMeasurement(ctx, "S1", store.Measurement{Bytes: 10, Pages: 1, HavePages: true, At: now.Add(2 * time.Minute)}); err != nil {
			t.Fatal(err)
		}
		good := now.Add(2 * time.Minute)
		if got := measured(); !got.Equal(good) {
			t.Fatalf("measured %v, want %v", got, good)
		}
		if err := store.SkipSpaceSize(ctx, q, "S1", now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if got := measured(); !got.Equal(good) {
			t.Fatalf("a later failure moved the tenant's time: %v", got)
		}
		if _, ok, _ := store.NextSpaceToMeasure(ctx, q, now.Add(time.Hour)); ok {
			t.Fatal("the failed space is still due")
		}
	})
}
