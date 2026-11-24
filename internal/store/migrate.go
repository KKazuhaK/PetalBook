package store

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// goSteps are the migrations that need more than SQL: the function of a
// version runs inside the same transaction, after the statements of its file.
var goSteps = map[int]func(ctx context.Context, q Q) error{
	1: defaultTenantStep,
}

// defaultTenantStep is the Go part of the baseline: the default tenant, made
// with the time of the first start, and its (empty) usage row.
func defaultTenantStep(ctx context.Context, q Q) error {
	now := ms(time.Now())
	if _, err := q.Exec(ctx, `INSERT INTO tenants(id, slug, name, name_key, disabled, quota, created, updated) VALUES (?, ?, 'Default', ?, 0, '{}', ?, ?)`,
		DefaultTenantID, DefaultTenantSlug, FoldName("Default"), now, now); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `INSERT INTO usage(kind, id, tenant_id) VALUES ('tenant', ?, ?)`, DefaultTenantID, DefaultTenantID)
	return err
}

type migration struct {
	version int
	name    string
	stmts   []statement
}

// statement is one statement of a migration file, and the database it is for
// ("" for both).
type statement struct {
	sql     string
	dialect Dialect
}

func loadMigrations() ([]migration, error) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		num, _, _ := strings.Cut(e.Name(), "_")
		v, err := strconv.Atoi(num)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("migration %q: the name must start with a number", e.Name())
		}
		raw, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		stmts, err := splitStatements(string(raw))
		if err != nil {
			return nil, fmt.Errorf("migration %q: %w", e.Name(), err)
		}
		out = append(out, migration{version: v, name: e.Name(), stmts: stmts})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i := 1; i < len(out); i++ {
		if out[i].version == out[i-1].version {
			return nil, fmt.Errorf("two migrations are numbered %d", out[i].version)
		}
	}
	return out, nil
}

// dialectMark is the comment line that makes the next statement run on one
// database only: "-- dialect: postgres" or "-- dialect: sqlite".
const dialectMark = "-- dialect:"

// splitStatements cuts a migration at semicolons, dropping comment lines. A
// dialect mark applies to the statement that starts after it.
func splitStatements(sql string) ([]statement, error) {
	var out []statement
	var cur []string
	var mark Dialect
	flush := func() {
		if s := strings.TrimSpace(strings.Join(cur, "\n")); s != "" {
			out = append(out, statement{sql: s, dialect: mark})
			mark = ""
		}
		cur = nil
	}
	for _, l := range strings.Split(sql, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, dialectMark) {
			d := Dialect(strings.TrimSpace(strings.TrimPrefix(t, dialectMark)))
			if d != SQLite && d != Postgres {
				return nil, fmt.Errorf("unknown dialect %q", d)
			}
			if strings.TrimSpace(strings.Join(cur, "\n")) != "" {
				return nil, fmt.Errorf("a dialect mark inside a statement: %q", t)
			}
			mark = d
			continue
		}
		if strings.HasPrefix(t, "--") {
			continue
		}
		for {
			before, after, found := strings.Cut(l, ";")
			cur = append(cur, before)
			if !found {
				break
			}
			flush()
			l = after
		}
	}
	flush()
	return out, nil
}

// forDialect is a statement as the database d runs it: PostgreSQL runs the file
// as written; SQLite compares text bytewise with its built-in BINARY collation,
// which is what COLLATE "C" means on PostgreSQL.
func (st statement) forDialect(d Dialect) (string, bool) {
	if st.dialect != "" && st.dialect != d {
		return "", false
	}
	if d == SQLite {
		return strings.ReplaceAll(st.sql, `COLLATE "C"`, `COLLATE BINARY`), true
	}
	return st.sql, true
}

// migrate applies the migrations that have not run yet, in one transaction, so
// a failure leaves the database as it was. It is safe to run at every start,
// and on PostgreSQL two servers starting together take turns.
func (s *Store) migrate(ctx context.Context) error {
	list, err := loadMigrations()
	if err != nil {
		return err
	}
	return s.Tx(ctx, func(q Q) error { return s.applyMigrations(ctx, q, list) })
}

func (s *Store) applyMigrations(ctx context.Context, q Q, list []migration) error {
	if s.dialect == Postgres {
		if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(727274001)`); err != nil {
			return err
		}
	}
	if _, err := q.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version BIGINT PRIMARY KEY, applied BIGINT NOT NULL)`); err != nil {
		return err
	}
	rows, err := q.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return err
	}
	done := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		done[v] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	known := map[int]bool{}
	for _, m := range list {
		known[m.version] = true
	}
	for v := range done {
		if !known[v] {
			return fmt.Errorf("the database was made by a newer version of PetalBook, or by one before the baseline of 2026-11-24 (schema %d): make it again", v)
		}
	}
	for _, m := range list {
		if done[m.version] {
			continue
		}
		for _, st := range m.stmts {
			text, ok := st.forDialect(s.dialect)
			if !ok {
				continue
			}
			if _, err := q.Exec(ctx, text); err != nil {
				return fmt.Errorf("migration %s: %w", m.name, err)
			}
		}
		if step := goSteps[m.version]; step != nil {
			if err := step(ctx, q); err != nil {
				return fmt.Errorf("migration %s: %w", m.name, err)
			}
		}
		if _, err := q.Exec(ctx, `INSERT INTO schema_migrations(version, applied) VALUES (?, ?)`, m.version, ms(time.Now())); err != nil {
			return err
		}
	}
	return nil
}
