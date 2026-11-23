// Package storetest opens state databases for tests. A test that touches the
// database runs once on SQLite and, when PETALBOOK_TEST_POSTGRES holds a
// postgres:// URL, once more on PostgreSQL in a schema of its own that is
// dropped afterwards, so the suite can be repeated and run in parallel.
package storetest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/KKazuhaK/PetalBook/internal/store"
)

// EnvPostgres names the variable that switches the PostgreSQL runs on.
const EnvPostgres = "PETALBOOK_TEST_POSTGRES"

// EachAtVersion runs fn per available database, on a schema that stops at
// version max (see OpenAtVersion).
func EachAtVersion(t *testing.T, max int, fn func(t *testing.T, st *store.Store)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) { fn(t, OpenAtVersion(t, "sqlite", max)) })
	if postgresDSN() != "" {
		t.Run("postgres", func(t *testing.T) { fn(t, OpenAtVersion(t, "postgres", max)) })
	}
}

// Each runs fn as a subtest per available database.
func Each(t *testing.T, fn func(t *testing.T, st *store.Store)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) { fn(t, Open(t, "sqlite")) })
	if postgresDSN() != "" {
		t.Run("postgres", func(t *testing.T) { fn(t, Open(t, "postgres")) })
	}
}

func postgresDSN() string {
	return os.Getenv(EnvPostgres)
}

// Open returns a fresh, migrated store on the named backend; it is closed, and
// for PostgreSQL its schema dropped, when the test ends.
func Open(t testing.TB, backend string) *store.Store {
	t.Helper()
	ctx := context.Background()
	if backend == "postgres" {
		return openPostgres(ctx, t)
	}
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// OpenAtVersion is Open for a database whose schema stops at version max, to
// be filled as an older version would have and then brought up with Migrate.
func OpenAtVersion(t testing.TB, backend string, max int) *store.Store {
	t.Helper()
	ctx := context.Background()
	if backend == "postgres" {
		return openPostgresAt(ctx, t, max)
	}
	st, err := store.OpenAtVersion(ctx, filepath.Join(t.TempDir(), "state.db"), max)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func openPostgres(ctx context.Context, t testing.TB) *store.Store {
	return openPostgresAt(ctx, t, 0)
}

func openPostgresAt(ctx context.Context, t testing.TB, max int) *store.Store {
	dsn := postgresDSN()
	if dsn == "" {
		t.Skip(EnvPostgres + " is not set")
	}
	var b [6]byte
	rand.Read(b[:])
	schema := "pbtest_" + hex.EncodeToString(b[:])
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	var st *store.Store
	if max > 0 {
		st, err = store.OpenAtVersion(ctx, u.String(), max)
	} else {
		st, err = store.Open(ctx, u.String())
	}
	if err != nil {
		admin.ExecContext(ctx, `DROP SCHEMA `+schema+` CASCADE`)
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		st.Close()
		admin.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		admin.Close()
	})
	return st
}
