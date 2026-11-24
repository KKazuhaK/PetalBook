package store_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

// 0013_github.sql and 0018_tenants.sql were written on two branches, so an
// existing database meets them in either order: one migrated on feat/github-app
// has 0013 and gets 0014, 0016 and 0018 after the merge; one migrated on main
// has 0018 and gets 0013. Both end with the same schema and keep their rows.
// 0013's tables need no tenant_id: a connection hangs off its user, a remote off
// its space, and the App's installations are the server's own.

// appliedVersions lists schema_migrations in order.
func appliedVersions(t *testing.T, st *store.Store) string {
	t.Helper()
	rows, err := st.Q().Query(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		got = append(got, v)
	}
	return fmt.Sprint(got)
}

// checkGitHubAfterMerge checks what both orders must leave: every version
// applied, the user in the default tenant with their connection, the remote with
// its GitHub columns, the installation shared with the default tenant, the space counted for its owner,
// and the connection deleted with its user.
func checkGitHubAfterMerge(t *testing.T, st *store.Store) {
	t.Helper()
	files, _ := store.MigrationVersions()
	if got := appliedVersions(t, st); got != fmt.Sprint(files) {
		t.Fatalf("applied %s, files %v", got, files)
	}
	u, err := store.GetUser(ctx, st.Q(), store.InTenant(store.DefaultTenantID), "U1")
	if err != nil || u.TenantID != store.DefaultTenantID {
		t.Fatalf("%+v %v", u, err)
	}
	a, err := store.GetGitHubAuthorization(ctx, st.Q(), "U1")
	if err != nil || a.GitHubID != 4242 || a.Login != "tang-gh" {
		t.Fatalf("%+v %v", a, err)
	}
	if id, err := store.GitHubAccountUser(ctx, st.Q(), 4242); err != nil || id != "U1" {
		t.Fatalf("%q %v", id, err)
	}
	r, err := store.GetRemote(ctx, st.Q(), "S1")
	if err != nil || r.CredType != "connection" || r.Provider != "github" || r.GHVia != "user" || r.GHUserID != "U1" || r.GHRepoID != 77 {
		t.Fatalf("%+v %v", r, err)
	}
	if n, err := store.CountUserGitHubSpaces(ctx, st.Q(), "U1"); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if ok, err := store.GitHubInstallationShared(ctx, st.Q(), store.InTenant(store.DefaultTenantID), 9001); err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	if sp, err := store.GetSpace(ctx, st.Q(), store.InTenant(store.DefaultTenantID), "S1"); err != nil || sp.TenantID != store.DefaultTenantID {
		t.Fatalf("%+v %v", sp, err)
	}
	if use, err := store.GetUsage(ctx, st.Q(), store.UsageUser, "U1"); err != nil || use.Spaces != 1 {
		t.Fatalf("%+v %v", use, err)
	}
	if l, err := store.ListGitHubLeftovers(ctx, st.Q(), "U1"); err != nil || len(l) != 1 || l[0].RepoID != 78 || l[0].SpaceName != "Tang" {
		t.Fatalf("%+v %v", l, err)
	}
	// The foreign key to users survives both orders.
	if _, err := st.Q().Exec(ctx, `DELETE FROM users WHERE id = 'U1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetGitHubAuthorization(ctx, st.Q(), "U1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the connection outlived its user: %v", err)
	}
	if l, _ := store.ListGitHubLeftovers(ctx, st.Q(), "U1"); len(l) != 0 {
		t.Fatalf("the reminders outlived their user: %+v", l)
	}
}

func seedUserAndSpace(t *testing.T, st *store.Store, exec func(q string, a ...any)) {
	t.Helper()
	now := time.Now().UnixMilli()
	exec(`INSERT INTO users(id, username, name, role, disabled, created) VALUES ('U1', 'tang', 'Tang', 'member', 0, ?)`, now)
	exec(`INSERT INTO spaces(id, name, name_key, created) VALUES ('S1', 'Tang', 'tang', ?)`, now)
	exec(`INSERT INTO space_members(space_id, user_id, role, created) VALUES ('S1', 'U1', 'owner', ?)`, now)
}

func seedGitHub(t *testing.T, exec func(q string, a ...any)) {
	t.Helper()
	now := time.Now().UnixMilli()
	exec(`INSERT INTO github_authorizations(user_id, github_id, login, connected, updated) VALUES ('U1', 4242, 'tang-gh', ?, ?)`, now, now)
	exec(`INSERT INTO github_installations(id, account, updated) VALUES (9001, 'kazuhahub', ?)`, now)
	exec(`INSERT INTO github_installation_tenants(installation_id, tenant_id) VALUES (9001, ?)`, store.DefaultTenantID)
	exec(`INSERT INTO github_leftovers(user_id, repo_id, full_name, html_url, space_name, created) VALUES ('U1', 78, 'tang-gh/left', 'https://github.com/tang-gh/left', 'Tang', ?)`, now)
	exec(`INSERT INTO remotes(space_id, url, branch, cred_type, provider, state, gh_via, gh_repo_id, gh_user_id, updated) VALUES ('S1', 'https://github.com/tang-gh/notes.git', 'main', 'connection', 'github', 'ok', 'user', 77, 'U1', ?)`, now)
}

func execer(t *testing.T, st *store.Store) func(q string, a ...any) {
	return func(q string, a ...any) {
		t.Helper()
		if _, err := st.Q().Exec(ctx, q, a...); err != nil {
			t.Fatal(q, err)
		}
	}
}

// A database of this branch before the merge: 0013 is there, 0014 to 0018 come.
func TestGitHubMigrationBeforeTheTenantsMigration(t *testing.T) {
	storetest.EachAtVersion(t, 13, func(t *testing.T, st *store.Store) {
		exec := execer(t, st)
		seedUserAndSpace(t, st, exec)
		seedGitHub(t, exec)
		if err := st.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		checkGitHubAfterMerge(t, st)
	})
}

// A database of main before the merge: 0018 is there, 0013 comes last.
func TestGitHubMigrationAfterTheTenantsMigration(t *testing.T) {
	storetest.EachWithoutVersion(t, []int{13}, func(t *testing.T, st *store.Store) {
		if n := count(t, st, `SELECT COUNT(*) FROM schema_migrations WHERE version = 13`); n != 0 {
			t.Fatal("0013 is applied already")
		}
		exec := execer(t, st)
		seedUserAndSpace(t, st, exec)
		// Usage rows as 3.3 keeps them for a space made on main.
		if _, err := st.RecountUsage(ctx); err != nil {
			t.Fatal(err)
		}
		if err := st.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		seedGitHub(t, exec)
		checkGitHubAfterMerge(t, st)
	})
}
