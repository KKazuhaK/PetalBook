package store_test

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"petalbook.app/server/internal/auditlog"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// auditID is an id as the audit log writes them: the time, then a sequence.
func auditID(at time.Time, seq int) string {
	return fmt.Sprintf("%012x%06x", at.UnixMilli(), seq)
}

var putSeq atomic.Int64

var trimNow = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

// putAudit inserts one row per age (how long before trimNow it was written) in
// the category.
func putAudit(t *testing.T, st *store.Store, category string, ages ...time.Duration) {
	t.Helper()
	bulk(t, st, "audit_log", "id, tenant_id, at, kind, category", len(ages), 500, func(i int) []any {
		at := trimNow.Add(-ages[i])
		return []any{auditID(at, int(putSeq.Add(1))), store.DefaultTenantID, at.UnixMilli(), "x", category}
	})
}

// left are the ages the rows still there were written at, oldest first.
func leftAudit(t *testing.T, st *store.Store) []time.Duration {
	t.Helper()
	rows, err := st.Q().Query(ctx, `SELECT at FROM audit_log ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []time.Duration
	for rows.Next() {
		var at int64
		if err := rows.Scan(&at); err != nil {
			t.Fatal(err)
		}
		out = append(out, trimNow.Sub(time.UnixMilli(at)))
	}
	return out
}

const hour = time.Hour

// The trim deletes by id range, in batches: what is old, then all but the
// newest rows to keep, and nothing else.
func TestTrimAuditDeletesARangeInBatches(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		old := 2*store.AuditTrimBatch + 17 // more than two batches
		const recent = 300
		bulk(t, st, "audit_log", "id, tenant_id, at, kind", old+recent, 500, func(i int) []any {
			at := base.Add(time.Duration(i) * time.Second)
			if i >= old {
				at = base.Add(48*time.Hour + time.Duration(i)*time.Second)
			}
			return []any{auditID(at, i), store.DefaultTenantID, at.UnixMilli(), "login"}
		})
		now := base.Add(100 * 24 * time.Hour) // every row is past the 90-day floor
		n, err := store.TrimAudit(ctx, st.Q(), now, store.AuditRetention{Days: 99, Rows: 1_000_000, Mode: "and", OptionalDays: 30})
		if err != nil || n != int64(old) {
			t.Fatalf("trimmed %d (%v), want %d", n, err, old)
		}
		if c := count(t, st, `SELECT COUNT(*) FROM audit_log`); c != recent {
			t.Fatalf("%d rows left", c)
		}
		// Keep the newest 100 of what is left.
		if n, err := store.TrimAudit(ctx, st.Q(), now, store.AuditRetention{Days: 3650, Rows: 100, Mode: "and", OptionalDays: 30}); err != nil || n != recent-100 {
			t.Fatalf("trimmed %d (%v)", n, err)
		}
		var oldest string
		st.Q().QueryRow(ctx, `SELECT MIN(id) FROM audit_log`).Scan(&oldest)
		if c := count(t, st, `SELECT COUNT(*) FROM audit_log`); c != 100 || oldest != auditID(base.Add(48*time.Hour+time.Duration(old+recent-100)*time.Second), old+recent-100) {
			t.Fatalf("%d rows left, the oldest %s", c, oldest)
		}
	})
}

// A row is kept while it is younger than the days OR among the newest rows
// ("or"), or only while both ("and"). Optional rows are used so that the floor
// of core rows does not get in the way.
func TestTrimAuditModes(t *testing.T) {
	t.Parallel()
	ages := []time.Duration{120 * hour, 96 * hour, 84 * hour, 48 * hour, 24 * hour}
	for _, tc := range []struct {
		mode string
		left []time.Duration
	}{
		// Old (more than 4 days) and outside the newest 2: only the 120 h row.
		{"or", []time.Duration{96 * hour, 84 * hour, 48 * hour, 24 * hour}},
		// Old, or outside the newest 2: the 120, 96 and 84 h rows.
		{"and", []time.Duration{48 * hour, 24 * hour}},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			t.Parallel()
			storetest.Each(t, func(t *testing.T, st *store.Store) {
				putAudit(t, st, "reads", ages...)
				if _, err := store.TrimAudit(ctx, st.Q(), trimNow, store.AuditRetention{Days: 4, Rows: 2, Mode: tc.mode, OptionalDays: 90}); err != nil {
					t.Fatal(err)
				}
				if got := leftAudit(t, st); fmt.Sprint(got) != fmt.Sprint(tc.left) {
					t.Fatalf("left %v, want %v", got, tc.left)
				}
			})
		})
	}
}

// A core row younger than the floor is never deleted, whatever the days and
// rows say; past it, it goes by the same rules.
func TestTrimAuditKeepsCoreRowsForTheFloor(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		day := 24 * hour
		putAudit(t, st, "security", 120*day, 100*day, 80*day, 10*day, hour)
		p := store.AuditRetention{Days: 1, Rows: 1, Mode: "and", OptionalDays: 1}
		if _, err := store.TrimAudit(ctx, st.Q(), trimNow, p); err != nil {
			t.Fatal(err)
		}
		// Only the rows older than 90 days go; the days and rows asked for far less.
		if got, want := leftAudit(t, st), []time.Duration{80 * day, 10 * day, hour}; fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("left %v, want %v", got, want)
		}
	})
}

// Optional rows go after their own, shorter days; core rows of the same age
// stay.
func TestTrimAuditOptionalRowsHaveTheirOwnDays(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		day := 24 * hour
		putAudit(t, st, "reads", 40*day, 20*day, 10*day)
		putAudit(t, st, "admin", 40*day)
		// Within the days and the rows asked for ("or": nothing is both old and outside).
		if _, err := store.TrimAudit(ctx, st.Q(), trimNow, store.AuditRetention{Days: 90, Rows: 100, Mode: "or", OptionalDays: 30}); err != nil {
			t.Fatal(err)
		}
		if n := count(t, st, `SELECT COUNT(*) FROM audit_log WHERE category = 'reads'`); n != 2 {
			t.Fatalf("%d optional rows left, want 2", n)
		}
		if n := count(t, st, `SELECT COUNT(*) FROM audit_log WHERE category = 'admin'`); n != 1 {
			t.Fatalf("the core row went")
		}
	})
}

// Rows are filed under the category of their kind when none is given, and keep
// the one they were written with.
func TestInsertAuditFilesTheCategory(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Now()
		if err := store.InsertAudit(ctx, st.Q(), []store.AuditRow{
			{ID: "1", At: now, Kind: "login_failed"},
			{ID: "2", At: now, Kind: "login"},
			{ID: "3", At: now, Kind: "login", Category: "reads"}, // as written
		}); err != nil {
			t.Fatal(err)
		}
		rows, err := store.ListAudit(ctx, st.Q(), store.AllTenants(), store.AuditFilter{}, 10)
		if err != nil || len(rows) != 3 {
			t.Fatalf("%v %v", rows, err)
		}
		got := rows[2].Category + " " + rows[1].Category + " " + rows[0].Category
		if got != "security signins reads" {
			t.Fatalf("categories %q", got)
		}
	})
}

// The filters combine, several categories are merged by id, the date range
// becomes an id range, a tenant sees nothing of another's rows, and the kinds of
// the server's own stay hidden from the tenant.
func TestListAuditFilters(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		day := 24 * hour
		other := "T000000001"
		mk := func(i int, ago time.Duration, kind, tenant, actor, user, capacity string) store.AuditRow {
			at := trimNow.Add(-ago)
			return store.AuditRow{ID: auditID(at, i), At: at, Kind: kind, TenantID: tenant, Actor: actor, UserID: user, Capacity: capacity}
		}
		if err := store.InsertAudit(ctx, st.Q(), []store.AuditRow{
			mk(1, 5*day, "login", store.DefaultTenantID, "", "u1", ""),
			mk(2, 4*day, "login_failed", store.DefaultTenantID, "", "u1", ""),
			mk(3, 3*day, "setting_changed", store.DefaultTenantID, "a1", "", "site_admin"),
			mk(4, 2*day, "space_deleted", store.DefaultTenantID, "a1", "u2", "tenant_admin"),
			mk(5, 1*day, "lockout", store.DefaultTenantID, "", "", ""),
			mk(6, 1*day, "login", other, "", "u9", ""),
		}); err != nil {
			t.Fatal(err)
		}
		ids := func(sc store.Scope, f store.AuditFilter) string {
			rows, err := store.ListAudit(ctx, st.Q(), sc, f, 50)
			if err != nil {
				t.Fatal(err)
			}
			s := ""
			for i := len(rows) - 1; i >= 0; i-- {
				s += strings.TrimLeft(rows[i].ID[12:], "0")
			}
			return s
		}
		in := store.InTenant(store.DefaultTenantID)
		all := store.AllTenants()
		for name, tc := range map[string]struct {
			sc   store.Scope
			f    store.AuditFilter
			want string
		}{
			"one category":       {in, store.AuditFilter{Categories: []string{"security"}}, "25"},
			"two categories":     {in, store.AuditFilter{Categories: []string{"signins", "data"}}, "14"},
			"category and kind":  {in, store.AuditFilter{Categories: []string{"security"}, Kind: "lockout"}, "5"},
			"person":             {in, store.AuditFilter{User: "u1"}, "12"},
			"actor":              {in, store.AuditFilter{Actor: "a1"}, "34"},
			"capacity":           {in, store.AuditFilter{Capacity: "site_admin"}, "3"},
			"from":               {in, store.AuditFilter{From: trimNow.Add(-3 * day)}, "345"},
			"from to":            {in, store.AuditFilter{From: trimNow.Add(-4 * day), To: trimNow.Add(-2 * day)}, "23"},
			"site hides":         {in, store.AuditFilter{HideSite: true}, "1234"},
			"site shows":         {all, store.AuditFilter{}, "123456"},
			"site by tenant":     {store.InTenant(other), store.AuditFilter{}, "6"},
			"signins everywhere": {all, store.AuditFilter{Categories: []string{"signins"}}, "16"},
			"cursor":             {in, store.AuditFilter{Cursor: auditID(trimNow.Add(-3*day), 3)}, "12"},
		} {
			if got := ids(tc.sc, tc.f); got != tc.want {
				t.Errorf("%s: rows %q, want %q", name, got, tc.want)
			}
		}
		// A page of two out of several categories, and the cursor from it.
		rows, err := store.ListAudit(ctx, st.Q(), all, store.AuditFilter{Categories: []string{"security", "admin", "data"}}, 2)
		if err != nil || len(rows) != 3 || rows[0].Kind != "lockout" || rows[1].Kind != "space_deleted" {
			t.Fatalf("%v %v", rows, err)
		}
	})
}

// StreamAudit reads page after page and says when it stopped at the maximum
// with rows left.
func TestStreamAuditStopsAtTheMaximum(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		total := 2*store.AuditStreamPage + 37
		bulk(t, st, "audit_log", "id, tenant_id, at, kind", total, 500, func(i int) []any {
			at := trimNow.Add(time.Duration(i) * time.Second)
			return []any{auditID(at, i), store.DefaultTenantID, at.UnixMilli(), "login"}
		})
		var last string
		stream := func(max int) (int, bool) {
			n, more, err := store.StreamAudit(ctx, st.Q(), store.AllTenants(), store.AuditFilter{}, max, func(r store.AuditRow) error {
				if last != "" && r.ID >= last {
					t.Fatalf("rows are not newest first")
				}
				last = r.ID
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			return n, more
		}
		if n, more := stream(total + 10); n != total || more {
			t.Fatalf("all: %d %v", n, more)
		}
		last = ""
		if n, more := stream(total); n != total || more {
			t.Fatalf("exactly all: %d %v", n, more)
		}
		last = ""
		if n, more := stream(store.AuditStreamPage + 5); n != store.AuditStreamPage+5 || !more {
			t.Fatalf("cut: %d %v", n, more)
		}
	})
}

// The listing reads an index that ends in id for every filter, and the trim's
// batches read the primary key or (category, id).
func TestAuditQueriesUseTheirIndexes(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Now()
		var tenants []string
		for i := 0; i < 20; i++ {
			id := fmt.Sprintf("T%09d", i)
			if err := st.CreateTenant(ctx, store.Tenant{ID: id, Slug: id, Name: id, Created: now, Updated: now}); err != nil {
				t.Fatal(err)
			}
			tenants = append(tenants, id)
		}
		kinds := []string{"login", "logout", "login_failed", "password_changed", "space_created", "member_added", "token_created", "setting_changed"}
		// reads is rare (as an optional category is), the others common.
		cats := []string{"signins", "security", "admin", "data"}
		bulk(t, st, "audit_log", "id, tenant_id, at, kind, category, actor", 6000, 500, func(i int) []any {
			at := now.Add(time.Duration(i) * time.Second)
			cat := cats[i%len(cats)]
			if i%100 == 7 {
				cat = "reads"
			}
			return []any{auditID(at, i), tenants[i%len(tenants)], at.UnixMilli(), kinds[i%len(kinds)], cat, fmt.Sprintf("A%d", i%50)}
		})
		withDearScans(t, st, func(q store.Q) {
			for name, tc := range map[string]struct {
				sc    store.Scope
				f     store.AuditFilter
				index string
			}{
				"a tenant's log by kind":     {store.InTenant(tenants[3]), store.AuditFilter{Kind: "member_added", HideSite: true}, "audit_log_tenant_kind"},
				"a tenant's log by category": {store.InTenant(tenants[3]), store.AuditFilter{Categories: []string{"data"}, HideSite: true}, "audit_log_tenant_cat"},
				"the site's log by category": {store.AllTenants(), store.AuditFilter{Categories: []string{"reads"}}, "audit_log_cat"},
				"an actor":                   {store.AllTenants(), store.AuditFilter{Actor: "A7"}, "audit_log_actor"},
				"a date range":               {store.AllTenants(), store.AuditFilter{From: now.Add(time.Minute), To: now.Add(time.Hour)}, "sqlite_autoindex_audit_log"},
			} {
				pq := &planQ{Q: q, d: st.Dialect()}
				if _, err := store.ListAudit(ctx, pq, tc.sc, tc.f, 50); err != nil {
					t.Fatal(err)
				}
				idx := tc.index
				if st.Dialect() == store.Postgres && idx == "sqlite_autoindex_audit_log" {
					idx = "audit_log_pkey"
				}
				if len(pq.plans) != 1 || !usesIndex(st, pq.plans[0], idx) {
					t.Errorf("%s:\n%s", name, strings.Join(pq.plans, "\n--\n"))
				}
			}
			plan := explain(t, st, q, `SELECT id FROM audit_log WHERE id < ? ORDER BY id LIMIT 5000`, store.AuditIDBefore(now.Add(time.Minute)))
			if strings.Contains(plan, "SCAN audit_log\n") || strings.HasSuffix(plan, "SCAN audit_log") || strings.Contains(plan, "Seq Scan") || strings.Contains(plan, "TEMP B-TREE") {
				t.Errorf("the trim's range is not read by the key:\n%s", plan)
			}
			plan = explain(t, st, q, `SELECT id FROM audit_log WHERE category = ? AND id < ? ORDER BY id LIMIT 5000`, "reads", store.AuditIDBefore(now.Add(time.Minute)))
			if !usesIndex(st, plan, "audit_log_cat") {
				t.Errorf("the trim of an optional category is not read by (category, id):\n%s", plan)
			}
		})
	})
}

// The categories the settings switch are the registry's optional ones, and a
// core category has no setting: nothing can turn it off.
func TestCoreCategoriesHaveNoSwitch(t *testing.T) {
	t.Parallel()
	for _, c := range auditlog.Categories() {
		if c.Core != (c.Setting == "") {
			t.Errorf("category %s: core %v, setting %q", c.ID, c.Core, c.Setting)
		}
	}
}
