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
	"sync"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/KKazuhaK/PetalBook/internal/store"
)

// EnvPostgres names the variable that switches the PostgreSQL runs on.
const EnvPostgres = "PETALBOOK_TEST_POSTGRES"

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
	path := filepath.Join(t.TempDir(), "state.db")
	if err := os.WriteFile(path, sqliteTemplate(t), 0o600); err != nil {
		t.Fatal(err)
	}
	// The copy has every migration applied, so Open only checks that and loads
	// the tenant state, as a start of the server on an existing database does.
	st, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// The migration of a fresh SQLite database takes longer than a test that uses
// it, most of all under the race detector, so it runs once per test binary:
// every test gets a copy of that file. (store.Open on an empty path still runs
// it, and PostgreSQL schemas are made as before.)
var template struct {
	once sync.Once
	data []byte
	err  error
}

func sqliteTemplate(t testing.TB) []byte {
	t.Helper()
	template.once.Do(func() { template.data, template.err = buildTemplate() })
	if template.err != nil {
		t.Fatal(template.err)
	}
	return template.data
}

func buildTemplate() ([]byte, error) {
	dir, err := os.MkdirTemp("", "petalbook-state-template-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "state.db")
	ctx := context.Background()
	st, err := store.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	// Everything in the main file, so that it is the whole database.
	if _, err := st.Q().Exec(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		st.Close()
		return nil, err
	}
	if err := st.Close(); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func openPostgres(ctx context.Context, t testing.TB) *store.Store {
	return openPostgresWith(ctx, t, store.Open)
}

// openPostgresWith opens a store with open in a schema of its own, dropped when
// the test ends.
func openPostgresWith(ctx context.Context, t testing.TB, open func(ctx context.Context, target string) (*store.Store, error)) *store.Store {
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
	st, err := open(ctx, u.String())
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
