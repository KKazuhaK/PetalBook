package api_test

import (
	"testing"
	"time"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/store"
)

// purgeFx writes rows of three categories at three times (10, 5 and 1 days
// ago, the clock moved on in between) and returns the time five days ago.
func purgeFx(t *testing.T, a *acc, tenant string) (fiveDaysAgo time.Time) {
	t.Helper()
	day := 24 * time.Hour
	start := a.clock.Now()
	for i, age := range []time.Duration{10 * day, 5 * day, day} {
		a.clock.Advance(start.Add(-age).Sub(a.clock.Now()))
		a.auth.Audit(auth.AuditEvent{Kind: "login_failed", Tenant: tenant, Username: "x"})
		a.auth.Audit(auth.AuditEvent{Kind: "space_deleted", Tenant: tenant})
		a.auth.Audit(auth.AuditEvent{Kind: "tenant_changed", Tenant: tenant})
		a.auth.FlushAudit(ctxBG)
		if i == 1 {
			fiveDaysAgo = a.clock.Now()
		}
	}
	a.clock.Advance(start.Sub(a.clock.Now()))
	return fiveDaysAgo
}

func auditCount(t *testing.T, c *client, query string) int {
	t.Helper()
	return len(c.do("GET", "/api/admin/audit?limit=200"+query, nil).want(t, 200, "").body["items"].([]any))
}

// Only a site administrator across the site purges: a tenant's administrator,
// a member and a site administrator in a tenant's scope are refused, and the
// log is whole afterwards.
func TestAuditPurgeIsTheSitesAlone(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		purgeFx(t, a, f.tenB)
		before := auditCounts(t, f.root, "")
		body := map[string]any{"category": "all"}
		f.admB.do("POST", "/api/admin/audit/purge", body).want(t, 403, "forbidden")
		f.admB.do("POST", "/api/admin/audit/purge?dry_run=true", body).want(t, 403, "forbidden")
		f.memB.do("POST", "/api/admin/audit/purge", body).want(t, 403, "forbidden")
		f.root.do("POST", "/api/admin/audit/purge", body, scopeHeader, "tenant:"+f.tenB).want(t, 403, "forbidden")
		a.newClient("").do("POST", "/api/admin/audit/purge", body).want(t, 401, "")
		for kind, n := range auditCounts(t, f.root, "") {
			if n < before[kind] {
				t.Fatalf("%s: %d rows, was %d: a refused purge deleted", kind, n, before[kind])
			}
		}
	})
}

// A dry run counts what a purge would delete, deletes nothing and writes no row.
func TestAuditPurgeDryRunCounts(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		five := purgeFx(t, a, f.tenB)
		before := auditCount(t, f.root, "")
		r := f.root.do("POST", "/api/admin/audit/purge?dry_run=true", map[string]any{"category": "data", "before": five.Format(time.RFC3339Nano)}).want(t, 200, "")
		if r.body["dryRun"] != true || r.body["count"] != float64(1) || r.body["category"] != "data" {
			t.Fatalf("%v", r.body)
		}
		all := f.root.do("POST", "/api/admin/audit/purge?dry_run=true", map[string]any{}).want(t, 200, "")
		// Every row, the one a fixture's job may still be writing aside.
		got := auditCount(t, f.root, "")
		if n := int(all.body["count"].(float64)); all.body["dryRun"] != true || all.body["category"] != "all" || n < before || n > got {
			t.Fatalf("%v, the log has %d to %d", all.body, before, got)
		}
		if got < before || auditCounts(t, f.root, "")["audit_purged"] != 0 {
			t.Fatalf("a dry run changed the log: %d rows, was %d", got, before)
		}
		f.root.do("POST", "/api/admin/audit/purge?dry_run=maybe", nil).want(t, 400, "invalid")
	})
}

// A purge by category, by date, and of everything leaves exactly one
// audit_purged row each time, which no later purge deletes.
func TestAuditPurgeByCategoryDateAndAll(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		base := auditCounts(t, f.root, "")
		five := purgeFx(t, a, f.tenB)
		// The counts of the three kinds the fixture wrote, the log's own others aside.
		kinds := func() map[string]int {
			k := auditCounts(t, f.root, "")
			for _, name := range []string{"space_deleted", "login_failed", "tenant_changed"} {
				if k[name] -= base[name]; k[name] < 0 {
					t.Fatalf("%s went below where it began: %v", name, k)
				}
			}
			return k
		}
		if k := kinds(); k["space_deleted"] != 3 || k["login_failed"] != 3 || k["tenant_changed"] != 3 {
			t.Fatalf("%v", k)
		}

		// One category, before a date: only the oldest row of the category goes.
		r := f.root.do("POST", "/api/admin/audit/purge", map[string]any{"category": "data", "before": five.Format(time.RFC3339Nano)}).want(t, 200, "")
		if r.body["dryRun"] != false || r.body["count"] != float64(1) {
			t.Fatalf("%v", r.body)
		}
		k := kinds()
		if k["space_deleted"] != 2 || k["login_failed"] != 3 || k["tenant_changed"] != 3 || k["audit_purged"] != 1 {
			t.Fatalf("after a purge of data: %v", k)
		}
		row := f.root.do("GET", "/api/admin/audit?kind=audit_purged", nil).want(t, 200, "").body["items"].([]any)[0].(map[string]any)
		d := row["detail"].(map[string]any)
		if row["kind"] != store.AuditPurgedKind || row["actor"] != mustUserID(t, a, "root") || row["category"] != "system" || d["category"] != "data" || d["deleted"] != float64(1) || d["before"] == nil {
			t.Fatalf("the row says %v", row)
		}
		// A tenant's administrator does not see it: it is the site's.
		if auditCounts(t, f.admB, "")["audit_purged"] != 0 {
			t.Fatal("a tenant's administrator sees the record of a purge")
		}

		// One category, every date.
		f.root.do("POST", "/api/admin/audit/purge", map[string]any{"category": "security"}).want(t, 200, "")
		if k := kinds(); k["login_failed"] != 0 || k["space_deleted"] != 2 || k["audit_purged"] != 2 {
			t.Fatalf("after a purge of security: %v", k)
		}

		// A date alone covers every category.
		a.clock.Advance(time.Hour)
		f.root.do("POST", "/api/admin/audit/purge", map[string]any{"before": five.Format(time.RFC3339Nano)}).want(t, 200, "")
		if k := kinds(); k["tenant_changed"] != 2 || k["audit_purged"] != 3 {
			t.Fatalf("after a purge before a date: %v", k)
		}

		// Everything: the records of the purges stay, and one more is written.
		a.clock.Advance(time.Hour)
		all := f.root.do("POST", "/api/admin/audit/purge", map[string]any{"category": "all"}).want(t, 200, "")
		if all.body["category"] != "all" {
			t.Fatalf("%v", all.body)
		}
		if k = auditCounts(t, f.root, ""); k["audit_purged"] != 4 || len(k) != 1 {
			t.Fatalf("after a purge of everything: %v", k)
		}
		// Nothing is left to delete, and it is still said once.
		if n := f.root.do("POST", "/api/admin/audit/purge", map[string]any{}).want(t, 200, "").body["count"]; n != float64(0) {
			t.Fatalf("%v", n)
		}
		if k = auditCounts(t, f.root, ""); k["audit_purged"] != 5 {
			t.Fatalf("%v", k)
		}
	})
}

// A purge names a category that exists and a time it can read.
func TestAuditPurgeChecksItsInput(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		root.do("POST", "/api/admin/audit/purge", map[string]any{"category": "nope"}).want(t, 400, "invalid")
		root.do("POST", "/api/admin/audit/purge", map[string]any{"before": "last week"}).want(t, 400, "invalid")
		root.do("POST", "/api/admin/audit/purge", map[string]any{"before": "2020-01-02"}).want(t, 200, "")
	})
}
