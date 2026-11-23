package store_test

import (
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

// seedBefore3_3 fills a database that stops at the schema of 3.2 the way a
// running server would have: administrators and members, personal and shared
// spaces, groups with people and places in spaces, an invite, audit rows.
func seedBefore3_3(t *testing.T, st *store.Store) {
	t.Helper()
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := st.Q().Exec(ctx, q, a...); err != nil {
			t.Fatal(q, err)
		}
	}
	now := time.Now().UnixMilli()
	for _, u := range [][3]string{{"A1", "boss", "admin"}, {"A2", "second", "admin"}, {"M1", "tang", "member"}, {"M2", "kai", "member"}} {
		exec(`INSERT INTO users(id, username, name, role, disabled, created) VALUES (?, ?, ?, ?, 0, ?)`, u[0], u[1], u[1], u[2], now)
	}
	exec(`INSERT INTO spaces(id, name, name_key, personal_of, created) VALUES ('P1', 'My notes', 'my notes', 'M1', ?)`, now)
	exec(`INSERT INTO spaces(id, name, name_key, created) VALUES ('S1', 'Team', 'team', ?)`, now)
	exec(`INSERT INTO space_members(space_id, user_id, role, created) VALUES ('P1', 'M1', 'owner', ?), ('S1', 'A1', 'owner', ?), ('S1', 'M1', 'editor', ?)`, now, now, now)
	exec(`INSERT INTO groups(id, name, name_key, description, source, created, updated) VALUES ('G1', 'Writers', 'writers', '', 'local', ?, ?), ('G2', 'Readers', 'readers', '', 'synced', ?, ?)`, now, now, now, now)
	exec(`INSERT INTO group_members(group_id, user_id, provider, value, created) VALUES ('G1', 'M1', '', '', ?), ('G1', 'M2', '', '', ?), ('G2', 'M2', 'sso', 'readers', ?)`, now, now, now)
	exec(`INSERT INTO space_groups(space_id, group_id, role, created) VALUES ('S1', 'G1', 'editor', ?), ('S1', 'G2', 'viewer', ?)`, now, now)
	exec(`INSERT INTO invites(id, token_hash, role, created_by, created, expires) VALUES ('I1', 'hash1', 'member', 'A1', ?, ?)`, now, now+86400000)
	exec(`INSERT INTO invites(id, token_hash, role, created_by, created, expires) VALUES ('I2', 'hash2', 'admin', 'A1', ?, ?)`, now, now+86400000)
	exec(`INSERT INTO audit_log(id, at, kind, actor, user_id, username, ip, detail) VALUES ('R1', ?, 'login', '', 'M1', 'tang', '', '{}'), ('R2', ?, 'security_changed', 'A1', '', '', '', '{}')`, now, now)
	exec(`INSERT INTO sessions(id, user_id, created, last_used) VALUES ('T1', 'M1', ?, ?)`, now, now)
}

func count(t *testing.T, st *store.Store, q string, a ...any) int {
	t.Helper()
	var n int
	if err := st.Q().QueryRow(ctx, q, a...).Scan(&n); err != nil {
		t.Fatal(q, err)
	}
	return n
}

// The migration of 3.3 on a database of the previous schema that has data: the
// default tenant exists, every row is in it, administrators become site
// administrators, and the rebuild of groups on SQLite keeps every membership
// (dropping the table with foreign keys on would have deleted them).
func TestTenantsMigrationOnAnExistingDatabase(t *testing.T) {
	storetest.EachAtVersion(t, 17, func(t *testing.T, st *store.Store) {
		seedBefore3_3(t, st)
		if err := st.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		tn, err := store.GetTenant(ctx, st.Q(), store.DefaultTenantID)
		if err != nil || tn.Slug != "default" || tn.Disabled {
			t.Fatalf("%+v %v", tn, err)
		}
		for _, tbl := range []string{"users", "spaces", "groups", "invites", "audit_log"} {
			total := count(t, st, `SELECT COUNT(*) FROM `+tbl)
			in := count(t, st, `SELECT COUNT(*) FROM `+tbl+` WHERE tenant_id = ?`, store.DefaultTenantID)
			if total == 0 || in != total {
				t.Errorf("%s: %d rows, %d in the default tenant", tbl, total, in)
			}
		}
		if n := count(t, st, `SELECT COUNT(*) FROM users WHERE site_admin = 1 AND role = 'admin'`); n != 2 {
			t.Errorf("%d site administrators, want the 2 administrators", n)
		}
		if n := count(t, st, `SELECT COUNT(*) FROM users WHERE site_admin = 1 AND role <> 'admin'`); n != 0 {
			t.Errorf("%d members became site administrators", n)
		}
		// An administrator's invite made before 3.3 makes what it promised: an
		// administrator of the server.
		if n := count(t, st, `SELECT COUNT(*) FROM invites WHERE site_admin = 1 AND role = 'admin'`); n != 1 {
			t.Errorf("%d administrator invites are of a site administrator, want 1", n)
		}
		if n := count(t, st, `SELECT COUNT(*) FROM invites WHERE site_admin = 1 AND role <> 'admin'`); n != 0 {
			t.Errorf("%d member invites became of a site administrator", n)
		}
		// What hangs under the groups survived the rebuild.
		for q, want := range map[string]int{
			`SELECT COUNT(*) FROM groups`:        2,
			`SELECT COUNT(*) FROM group_members`: 3,
			`SELECT COUNT(*) FROM space_groups`:  2,
			`SELECT COUNT(*) FROM space_members`: 3,
			`SELECT COUNT(*) FROM sessions`:      1,
		} {
			if got := count(t, st, q); got != want {
				t.Errorf("%s: %d, want %d", q, got, want)
			}
		}
		g, err := store.GetGroup(ctx, st.Q(), store.AllTenants(), "G1")
		if err != nil || g.People != 2 || g.Spaces != 1 || g.TenantID != store.DefaultTenantID {
			t.Fatalf("%+v %v", g, err)
		}
		// Group names are unique within a tenant, not on the server.
		now := time.Now()
		if err := store.InsertGroup(ctx, st.Q(), store.Group{ID: "G3", Name: "writers", Source: "local", Created: now, Updated: now}); !store.IsUnique(err) {
			t.Fatalf("a second group of that name in the default tenant: %v", err)
		}
		if err := st.CreateTenant(ctx, store.Tenant{ID: "T000000002", Slug: "acme", Name: "Acme", Created: now, Updated: now}); err != nil {
			t.Fatal(err)
		}
		if err := store.InsertGroup(ctx, st.Q(), store.Group{ID: "G3", TenantID: "T000000002", Name: "Writers", Source: "local", Created: now, Updated: now}); err != nil {
			t.Fatalf("the same name in another tenant: %v", err)
		}
		// Rows that point nowhere are still refused: the keys are as before.
		if _, err := st.Q().Exec(ctx, `INSERT INTO group_members(group_id, user_id, provider, value, created) VALUES ('NOPE', 'M1', '', '', 0)`); err == nil {
			t.Fatal("a membership of a group that does not exist was accepted")
		}
		// Running the migrations again changes nothing.
		if err := st.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		if n := count(t, st, `SELECT COUNT(*) FROM tenants`); n != 2 {
			t.Fatalf("%d tenants", n)
		}
	})
}

// On SQLite the rebuild runs with foreign keys off, which cannot be done inside
// a transaction: the migration does it around the transaction and checks the
// keys before it commits. A row that points nowhere fails the migration, which
// leaves the database as it was, and foreign keys are switched on again.
func TestTenantsMigrationChecksForeignKeysAndRestoresThem(t *testing.T) {
	st := storetest.OpenAtVersion(t, "sqlite", 17)
	seedBefore3_3(t, st)
	// A pointer to nowhere that foreign keys, when on, would have refused.
	if _, err := st.Q().Exec(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Q().Exec(ctx, `INSERT INTO group_members(group_id, user_id, provider, value, created) VALUES ('GONE', 'M1', 'x', 'y', 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Q().Exec(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	err := st.Migrate(ctx)
	if err == nil || !strings.Contains(err.Error(), "foreign key") {
		t.Fatalf("%v", err)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM schema_migrations WHERE version = 18`); n != 0 {
		t.Fatal("the failed migration was recorded")
	}
	if n := count(t, st, `SELECT COUNT(*) FROM groups`); n != 2 {
		t.Fatalf("%d groups after the rollback", n)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM sqlite_master WHERE name = 'tenants'`); n != 0 {
		t.Fatal("the rolled-back migration left its table")
	}
	if n := count(t, st, `PRAGMA foreign_keys`); n != 1 {
		t.Fatalf("foreign_keys is %d after a failed migration", n)
	}
	// Once the stray row is gone the migration goes through, and the keys are on.
	if _, err := st.Q().Exec(ctx, `DELETE FROM group_members WHERE group_id = 'GONE'`); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(t, st, `PRAGMA foreign_keys`); n != 1 {
		t.Fatalf("foreign_keys is %d after the migration", n)
	}
}

// A fresh database (the common case) has the default tenant and works at once.
func TestFreshDatabaseHasTheDefaultTenant(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		if st.TenantCount() != 1 || st.TenantDisabled(store.DefaultTenantID) {
			t.Fatal("no default tenant in memory")
		}
		if r, ok := st.TenantRefOf(store.DefaultTenantID); !ok || r.Slug != "default" {
			t.Fatalf("%+v", r)
		}
		mkUser(t, st, "A", "alice", "admin")
		u, err := store.GetUser(ctx, st.Q(), store.AllTenants(), "A")
		if err != nil || u.TenantID != store.DefaultTenantID {
			t.Fatalf("%+v %v", u, err)
		}
	})
}
