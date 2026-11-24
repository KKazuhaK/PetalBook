package store_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

// 0015_storage.sql and 0017_assets.sql were written on the storage branch, and
// 0016_groups.sql and 0018_tenants.sql on main, so an existing database meets
// them in either order. Both orders end with every version applied and the
// rows of the other side kept.

// checkStorageAfterMerge checks what both orders must leave: every version
// applied, the user and the space in the default tenant with the storage
// columns, and an attachment repository that goes through a provider.
func checkStorageAfterMerge(t *testing.T, st *store.Store) {
	t.Helper()
	files, _ := store.MigrationVersions()
	if got := appliedVersions(t, st); got != fmt.Sprint(files) {
		t.Fatalf("applied %s, files %v", got, files)
	}
	q := st.Q()
	u, err := store.GetUser(ctx, q, store.InTenant(store.DefaultTenantID), "U1")
	if err != nil || u.TenantID != store.DefaultTenantID || u.RemoteExempt {
		t.Fatalf("%+v %v", u, err)
	}
	if err := store.SetRemoteExempt(ctx, q, "U1", true); err != nil {
		t.Fatal(err)
	}
	if u, err := store.GetUser(ctx, q, store.AllTenants(), "U1"); err != nil || !u.RemoteExempt {
		t.Fatalf("%+v %v", u, err)
	}
	if sp, err := store.GetSpace(ctx, q, store.InTenant(store.DefaultTenantID), "S1"); err != nil || sp.TenantID != store.DefaultTenantID {
		t.Fatalf("%+v %v", sp, err)
	}
	if err := store.SetSpaceCache(ctx, q, "S1", store.CachePartial); err != nil {
		t.Fatal(err)
	}
	if s, err := store.GetSpaceStorage(ctx, q, "S1"); err != nil || s.Cache != store.CachePartial {
		t.Fatalf("%+v %v", s, err)
	}
	now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	if err := store.PutAssetRemote(ctx, q, store.Remote{SpaceID: "S1", URL: "https://github.com/o/s1-assets.git", Branch: "main", State: "ok", Updated: now,
		CredType: store.CredConnection, GHVia: "app", GHInstallationID: 9, GHRepoID: 79}); err != nil {
		t.Fatal(err)
	}
	if r, err := store.GetAssetRemote(ctx, q, "S1"); err != nil || r.Provider != store.ProviderGitHub || r.GHRepoID != 79 {
		t.Fatalf("%+v %v", r, err)
	}
	if n, err := store.CountGitHubSpaces(ctx, q); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
}

func seedPlainUserAndSpace(t *testing.T, st *store.Store) {
	t.Helper()
	exec := execer(t, st)
	now := time.Now().UnixMilli()
	exec(`INSERT INTO users(id, username, name, role, disabled, created) VALUES ('U1', 'tang', 'Tang', 'member', 0, ?)`, now)
	exec(`INSERT INTO spaces(id, name, name_key, created) VALUES ('S1', 'Tang', 'tang', ?)`, now)
	exec(`INSERT INTO space_members(space_id, user_id, role, created) VALUES ('S1', 'U1', 'owner', ?)`, now)
}

// A database of main before the merge: 0016 and 0018 are there, 0015 and 0017 come.
func TestStorageMigrationsAfterTheTenantsMigration(t *testing.T) {
	storetest.EachWithoutVersion(t, []int{15, 17}, func(t *testing.T, st *store.Store) {
		if n := count(t, st, `SELECT COUNT(*) FROM schema_migrations WHERE version IN (15, 17)`); n != 0 {
			t.Fatal("0015 or 0017 is applied already")
		}
		seedPlainUserAndSpace(t, st)
		if _, err := st.RecountUsage(ctx); err != nil {
			t.Fatal(err)
		}
		if err := st.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		checkStorageAfterMerge(t, st)
	})
}

// A database of the storage branch before the merge: 0015 and 0017 are there,
// 0016 and 0018 come (and 0020, which needs 0018), and its storage rows are kept.
func TestStorageMigrationsBeforeTheTenantsMigration(t *testing.T) {
	storetest.EachWithoutVersion(t, []int{16, 18, 20}, func(t *testing.T, st *store.Store) {
		seedPlainUserAndSpace(t, st)
		exec := execer(t, st)
		exec(`UPDATE spaces SET pinned = 1, last_used = 5 WHERE id = 'S1'`)
		if err := st.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		if s, err := store.GetSpaceStorage(ctx, st.Q(), "S1"); err != nil || !s.Pinned || s.LastUsed.IsZero() {
			t.Fatalf("the storage branch's rows were lost: %+v %v", s, err)
		}
		checkStorageAfterMerge(t, st)
	})
}
