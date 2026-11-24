package store_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

// auditID is an id as the audit log writes them: the time, then a sequence.
func auditID(at time.Time, seq int) string {
	return fmt.Sprintf("%012x%06x", at.UnixMilli(), seq)
}

// The trim deletes by id range, in batches: everything older than the bound,
// then all but the newest rows to keep, and nothing else.
func TestTrimAuditDeletesARangeInBatches(t *testing.T) {
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
		n, err := store.TrimAudit(ctx, st.Q(), base.Add(24*time.Hour), 1_000_000)
		if err != nil || n != int64(old) {
			t.Fatalf("trimmed %d (%v), want %d", n, err, old)
		}
		if c := count(t, st, `SELECT COUNT(*) FROM audit_log`); c != recent {
			t.Fatalf("%d rows left", c)
		}
		// Keep the newest 100 of what is left.
		if n, err := store.TrimAudit(ctx, st.Q(), base, 100); err != nil || n != recent-100 {
			t.Fatalf("trimmed %d (%v)", n, err)
		}
		var oldest string
		st.Q().QueryRow(ctx, `SELECT MIN(id) FROM audit_log`).Scan(&oldest)
		if c := count(t, st, `SELECT COUNT(*) FROM audit_log`); c != 100 || oldest != auditID(base.Add(48*time.Hour+time.Duration(old+recent-100)*time.Second), old+recent-100) {
			t.Fatalf("%d rows left, the oldest %s", c, oldest)
		}
	})
}

// A tenant's log by kind reads audit_log_tenant_kind (it read a tenant's whole
// log, 414 ms on a million rows); the trim's batches read the primary key.
func TestAuditQueriesUseTheirIndexes(t *testing.T) {
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
		bulk(t, st, "audit_log", "id, tenant_id, at, kind", 6000, 500, func(i int) []any {
			at := now.Add(time.Duration(i) * time.Second)
			return []any{auditID(at, i), tenants[i%len(tenants)], at.UnixMilli(), kinds[i%len(kinds)]}
		})
		withDearScans(t, st, func(q store.Q) {
			pq := &planQ{Q: q, d: st.Dialect()}
			if _, err := store.ListAudit(ctx, pq, store.InTenant(tenants[3]), true, "member_added", "", "", 50); err != nil {
				t.Fatal(err)
			}
			if len(pq.plans) != 1 || !usesIndex(st, pq.plans[0], "audit_log_tenant_kind") {
				t.Errorf("a tenant's log by kind:\n%s", strings.Join(pq.plans, "\n--\n"))
			}
			plan := explain(t, st, q, `SELECT id FROM audit_log WHERE id < ? ORDER BY id LIMIT 5000`, store.AuditIDBefore(now.Add(time.Minute)))
			if strings.Contains(plan, "SCAN audit_log\n") || strings.HasSuffix(plan, "SCAN audit_log") || strings.Contains(plan, "Seq Scan") || strings.Contains(plan, "TEMP B-TREE") {
				t.Errorf("the trim's range is not read by the key:\n%s", plan)
			}
		})
	})
}
