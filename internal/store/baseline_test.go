package store_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

func count(t *testing.T, st *store.Store, q string, a ...any) int {
	t.Helper()
	var n int
	if err := st.Q().QueryRow(ctx, q, a...).Scan(&n); err != nil {
		t.Fatal(q, err)
	}
	return n
}

// A fresh database has the default tenant, with its usage row, and works at once.
func TestFreshDatabaseHasTheDefaultTenant(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		if st.TenantCount() != 1 || st.TenantDisabled(store.DefaultTenantID) {
			t.Fatal("no default tenant in memory")
		}
		if r, ok := st.TenantRefOf(store.DefaultTenantID); !ok || r.Slug != "default" {
			t.Fatalf("%+v", r)
		}
		if n := count(t, st, `SELECT COUNT(*) FROM tenants WHERE id = ? AND created > 0`, store.DefaultTenantID); n != 1 {
			t.Fatal("the default tenant has no creation time")
		}
		mkUser(t, st, "A", "alice", "admin")
		u, err := store.GetUser(ctx, st.Q(), store.AllTenants(), "A")
		if err != nil || u.TenantID != store.DefaultTenantID {
			t.Fatalf("%+v %v", u, err)
		}
		// What the baseline makes is what the daily recount finds.
		if n, err := st.RecountUsage(ctx); err != nil || n != 0 {
			t.Fatalf("the recount corrected %d rows of a fresh database: %v", n, err)
		}
	})
}

// A database made by the migrations before the baseline (0001 to 0020) is not
// opened: it has to be made again.
func TestDatabaseBeforeTheBaselineIsRefused(t *testing.T) {
	path := t.TempDir() + "/s.db"
	st, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	for v := 2; v <= 20; v++ {
		if _, err := st.Q().Exec(ctx, `INSERT INTO schema_migrations(version, applied) VALUES (?, 0)`, v); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	if _, err := store.Open(ctx, path); err == nil || !strings.Contains(err.Error(), "baseline") {
		t.Fatalf("%v", err)
	}
}

// Every row that belongs to a tenant names one that exists: the database
// refuses the others.
func TestTenantForeignKeys(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Now()
		if err := store.InsertUser(ctx, st.Q(), store.User{ID: "U1", Username: "u1", Name: "u1", Role: "member", Created: now, TenantID: "nowhere"}); err == nil {
			t.Fatal("a user of a tenant that does not exist was stored")
		}
		if err := store.InsertSpace(ctx, st.Q(), "nowhere", "S1", "S1", "", "", now); err == nil {
			t.Fatal("a space of a tenant that does not exist was stored")
		}
		if err := store.InsertGroup(ctx, st.Q(), store.Group{ID: "G1", Name: "g", Source: store.GroupLocal, TenantID: "nowhere", Created: now, Updated: now}); err == nil {
			t.Fatal("a group of a tenant that does not exist was stored")
		}
		for _, stmt := range []string{
			`INSERT INTO invites(id, tenant_id, token_hash, role, created_by, created, expires) VALUES ('I1', 'nowhere', 'h', 'member', 'U1', 0, 0)`,
			`INSERT INTO usage(kind, id, tenant_id) VALUES ('tenant', 'nowhere', 'nowhere')`,
			`INSERT INTO recycle_items(id, tenant_id, kind, deleted_at) VALUES ('R1', 'nowhere', 'space', 0)`,
			`INSERT INTO exports(id, tenant_id, made_by, created, expires) VALUES ('E1', 'nowhere', 'U1', 0, 0)`,
			`INSERT INTO bulk_jobs(id, tenant_id, made_by, action, total, created, updated) VALUES ('B1', 'nowhere', 'U1', 'x', 1, 0, 0)`,
			`INSERT INTO registrations(id, tenant_id, username, name, email, password_hash, token_hash, created, expires) VALUES ('R1', 'nowhere', 'r', 'r', 'r@x', 'h', 'h', 0, 0)`,
		} {
			if _, err := st.Q().Exec(ctx, stmt); err == nil {
				t.Fatalf("stored: %s", stmt)
			}
		}
	})
}

// A tenant is deleted only when nothing that a restore or a person needs is
// left in it; what only history needs moves to the default tenant.
func TestDeletingATenantMovesItsLeftovers(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Now()
		const tn = "T000000002"
		if err := st.CreateTenant(ctx, store.Tenant{ID: tn, Slug: "two", Name: "Two", Created: now, Updated: now}); err != nil {
			t.Fatal(err)
		}
		// An item of the recycle bin keeps the tenant (a restore must find it).
		if _, err := st.Q().Exec(ctx, `INSERT INTO recycle_items(id, tenant_id, kind, deleted_at) VALUES ('R1', ?, 'space', 0)`, tn); err != nil {
			t.Fatal(err)
		}
		var ne *store.ErrTenantNotEmpty
		if err := st.DeleteTenant(ctx, tn); !errors.As(err, &ne) || ne.Recycle != 1 {
			t.Fatalf("a tenant with an item in the recycle bin: %v", err)
		}
		if _, err := st.Q().Exec(ctx, `DELETE FROM recycle_items WHERE id = 'R1'`); err != nil {
			t.Fatal(err)
		}
		// A tombstone, an export and a bulk job do not keep it.
		if err := store.InsertUser(ctx, st.Q(), store.User{ID: "U1", Username: "deleted-U1", Name: "", Role: "member", Created: now, TenantID: tn}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Q().Exec(ctx, `UPDATE users SET deleted_at = 1 WHERE id = 'U1'`); err != nil {
			t.Fatal(err)
		}
		if err := store.InsertExport(ctx, st.Q(), store.Export{ID: "E1", TenantID: tn, MadeBy: "U1", State: "ready", Created: now, Expires: now}); err != nil {
			t.Fatal(err)
		}
		if err := store.InsertBulkJob(ctx, st.Q(), store.BulkJob{ID: "B1", TenantID: tn, MadeBy: "U1", Action: "x", Total: 1, State: "done", Created: now}); err != nil {
			t.Fatal(err)
		}
		if err := st.DeleteTenant(ctx, tn); err != nil {
			t.Fatal(err)
		}
		if n := count(t, st, `SELECT COUNT(*) FROM users WHERE id = 'U1' AND tenant_id = ?`, store.DefaultTenantID); n != 1 {
			t.Fatal("the tombstone did not move to the default tenant")
		}
		for _, table := range []string{"exports", "bulk_jobs"} {
			if n := count(t, st, `SELECT COUNT(*) FROM `+table+` WHERE tenant_id = ? AND site_only = 1`, store.DefaultTenantID); n != 1 {
				t.Fatalf("the %s row did not become the site's", table)
			}
		}
	})
}

// The time the first account was made is a site setting (the baseline has no
// table of its own for it), and the strict mark is taken once.
func TestFirstUserMarkIsASiteSetting(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Now()
		if err := store.MarkFirstUser(ctx, st.Q(), true, now); err != nil {
			t.Fatal(err)
		}
		if err := store.MarkFirstUser(ctx, st.Q(), true, now); !errors.Is(err, store.ErrExists) {
			t.Fatalf("%v", err)
		}
		if err := store.MarkFirstUser(ctx, st.Q(), false, now); err != nil {
			t.Fatal(err)
		}
		if n := count(t, st, `SELECT COUNT(*) FROM site_settings WHERE key = 'first_user'`); n != 1 {
			t.Fatalf("%d", n)
		}
	})
}
