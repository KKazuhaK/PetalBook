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

// purge starts a purge by hand and waits for it: the answer is 202 with the
// job, which ends done, and its audit_purged row (written when the job ends)
// is there; it returns the job. rows is how many audit_purged rows the log has
// once it is written.
func purge(t *testing.T, c *client, body map[string]any, rows int) map[string]any {
	t.Helper()
	r := c.do("POST", "/api/admin/audit/purge", body).want(t, 202, "")
	j := r.body["job"].(map[string]any)
	if j["kind"] != "audit.purge" {
		t.Fatalf("%v", j)
	}
	done := waitJob(t, c, j["id"].(string))
	if done["state"] != "done" {
		t.Fatalf("%v", done)
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if auditCounts(t, c, "&kind=audit_purged")["audit_purged"] >= rows {
			return done
		}
	}
	t.Fatalf("the purge wrote no audit_purged row (want %d)", rows)
	return nil
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
		if r.body["dryRun"] != true || r.body["count"] != float64(1) || r.body["more"] != false || r.body["category"] != "data" {
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
		if j := purge(t, f.root, map[string]any{"category": "data", "before": five.Format(time.RFC3339Nano)}, 1); j["done"] != float64(1) {
			t.Fatalf("%v", j)
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
		purge(t, f.root, map[string]any{"category": "security"}, 2)
		if k := kinds(); k["login_failed"] != 0 || k["space_deleted"] != 2 || k["audit_purged"] != 2 {
			t.Fatalf("after a purge of security: %v", k)
		}

		// A date alone covers every category.
		a.clock.Advance(time.Hour)
		purge(t, f.root, map[string]any{"before": five.Format(time.RFC3339Nano)}, 3)
		if k := kinds(); k["tenant_changed"] != 2 || k["audit_purged"] != 3 {
			t.Fatalf("after a purge before a date: %v", k)
		}

		// Everything: the records of the purges stay, and one more is written.
		a.clock.Advance(time.Hour)
		purge(t, f.root, map[string]any{"category": "all"}, 4)
		if k = auditCounts(t, f.root, ""); k["audit_purged"] != 4 || len(k) != 1 {
			t.Fatalf("after a purge of everything: %v", k)
		}
		// Nothing is left to delete, and it is still said once.
		if j := purge(t, f.root, map[string]any{}, 5); j["done"] != float64(0) {
			t.Fatalf("%v", j)
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
		purge(t, root, map[string]any{"before": "2020-01-02"}, 1)
	})
}

// The purge is a job: the answer is 202 with the job, a site administrator
// follows it in the jobs, a tenant's administrator does not, a second purge
// while one runs is refused, and the row of the purge says how many went.
func TestAuditPurgeRunsAsAJobOnlyTheSiteFollows(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		purgeFx(t, a, f.tenB)
		j := purge(t, f.root, map[string]any{"category": "data"}, 1)
		id := j["id"].(string)
		if j["kind"] != "audit.purge" || j["done"] != float64(3) {
			t.Fatalf("%v", j)
		}
		f.admB.do("GET", "/api/admin/jobs/"+id, nil).want(t, 404, "not_found")
		for _, it := range f.admB.do("GET", "/api/admin/jobs", nil).want(t, 200, "").body["items"].([]any) {
			if it.(map[string]any)["kind"] == "audit.purge" {
				t.Fatal("a tenant's administrator sees the purge in the jobs")
			}
		}
		list := f.root.do("GET", "/api/admin/jobs?kind=audit.purge", nil).want(t, 200, "").body["items"].([]any)
		if len(list) != 1 {
			t.Fatalf("the site's list has %d purges", len(list))
		}
		row := f.root.do("GET", "/api/admin/audit?kind=audit_purged", nil).want(t, 200, "").body["items"].([]any)[0].(map[string]any)
		if d := row["detail"].(map[string]any); d["deleted"] != float64(3) || d["category"] != "data" || d["partial"] != nil {
			t.Fatalf("%v", row)
		}
	})
}

// One purge runs at a time: while one holds the lock, another is refused.
func TestAuditPurgeIsOneAtATime(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		if err := store.InsertJob(ctxBG, a.st.Q(), store.Job{ID: "HELD", TenantID: store.DefaultTenantID, Kind: store.JobAuditPurge, LockKey: store.AuditPurgeLock, State: "running", SiteOnly: true, Created: a.clock.Now()}); err != nil {
			t.Fatal(err)
		}
		root.do("POST", "/api/admin/audit/purge", map[string]any{}).want(t, 409, "busy")
	})
}

// A site role that holds only site.audit.purge starts a purge and follows its
// job (and sees no other kind of job); a tenant's administrator still does not.
func TestARoleWithOnlyThePurgeKeyFollowsItsJob(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		a.setup("root", pw)
		keeper := a.addUser("keeper", pw, "member")
		grantRole(t, a, "RP", store.RoleScopeSite, "", store.GrantToUser, mustUserID(t, a, "keeper"), "site.audit.purge")
		j := keeper.do("POST", "/api/admin/audit/purge", map[string]any{"before": "2020-01-02"}).want(t, 202, "").body["job"].(map[string]any)
		id := j["id"].(string)
		if done := waitJob(t, keeper, id); done["state"] != "done" {
			t.Fatalf("%v", done)
		}
		items := keeper.do("GET", "/api/admin/jobs", nil).want(t, 200, "").body["items"].([]any)
		if len(items) != 1 || items[0].(map[string]any)["id"] != id {
			t.Fatalf("the role sees %v", items)
		}
		keeper.do("GET", "/api/admin/jobs?kind=bulk", nil).want(t, 200, "")
	})
}
