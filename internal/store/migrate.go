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
//
// A step is made by a function of the dialect so that one version can do
// different things on SQLite and PostgreSQL (see tenantsStep).
var goSteps = map[int]func(d Dialect) func(ctx context.Context, q Q) error{
	7:  func(Dialect) func(context.Context, Q) error { return fillSpaceNameKeys },
	18: tenantsStep,
}

// foreignKeysOff lists the versions whose step rebuilds a table on SQLite. A
// rebuild must run with foreign keys off (dropping the table would otherwise
// delete the rows that reference it), and that pragma does nothing inside a
// transaction. So when such a version is pending, SQLite runs the whole
// migration on one pinned connection: foreign keys are switched off before the
// transaction starts, PRAGMA foreign_key_check ends the transaction (a failure
// rolls everything back), and they are switched on again after it, whatever
// happened. PostgreSQL has no such need and runs as always.
var foreignKeysOff = map[int]bool{18: true}

type migration struct {
	version int
	name    string
	stmts   []string
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
		out = append(out, migration{version: v, name: e.Name(), stmts: splitStatements(string(raw))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i := 1; i < len(out); i++ {
		if out[i].version == out[i-1].version {
			return nil, fmt.Errorf("two migrations are numbered %d", out[i].version)
		}
	}
	return out, nil
}

// splitStatements cuts a migration at semicolons, dropping comment lines.
func splitStatements(sql string) []string {
	var lines []string
	for _, l := range strings.Split(sql, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "--") {
			continue
		}
		lines = append(lines, l)
	}
	var out []string
	for _, s := range strings.Split(strings.Join(lines, "\n"), ";") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// migrate applies the migrations that have not run yet, in one transaction, so
// a failure leaves the database as it was. It is safe to run at every start,
// and on PostgreSQL two servers starting together take turns.
func (s *Store) migrate(ctx context.Context) error { return s.migrateTo(ctx, 0) }

// migrateTo is migrate, stopping after version max (0: no limit); tests use it
// to make a database as an older version left it.
func (s *Store) migrateTo(ctx context.Context, max int) error {
	return s.migrateWhere(ctx, func(v int) bool { return max <= 0 || v <= max })
}

// migrateWhere is migrate, applying only the versions keep accepts; tests use
// it to make a database that lacks one version, as one migrated on another
// branch does.
func (s *Store) migrateWhere(ctx context.Context, keep func(version int) bool) error {
	list, err := loadMigrations()
	if err != nil {
		return err
	}
	var cut []migration
	for _, m := range list {
		if keep(m.version) {
			cut = append(cut, m)
		}
	}
	list = cut
	run := func(q Q) error { return s.applyMigrations(ctx, q, list) }
	if s.dialect != SQLite {
		return s.Tx(ctx, run)
	}
	pending, err := s.pendingForeignKeysOff(ctx, list)
	if err != nil || !pending {
		if err != nil {
			return err
		}
		return s.Tx(ctx, run)
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	// Whatever happens next, the connection goes back to the pool with foreign
	// keys on.
	defer conn.ExecContext(context.WithoutCancel(ctx), `PRAGMA foreign_keys = ON`)
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := run(bound{s, tx}); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// pendingForeignKeysOff reports whether a migration that must run with foreign
// keys off has not been applied yet.
func (s *Store) pendingForeignKeysOff(ctx context.Context, list []migration) (bool, error) {
	var has int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&has); err != nil {
		return false, err
	}
	done := map[int]bool{}
	if has > 0 {
		rows, err := s.db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
		if err != nil {
			return false, err
		}
		defer rows.Close()
		for rows.Next() {
			var v int
			if err := rows.Scan(&v); err != nil {
				return false, err
			}
			done[v] = true
		}
		if err := rows.Err(); err != nil {
			return false, err
		}
	}
	for _, m := range list {
		if foreignKeysOff[m.version] && !done[m.version] {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) applyMigrations(ctx context.Context, q Q, list []migration) error {
	{
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
				return fmt.Errorf("the database was made by a newer version of PetalBook (schema %d)", v)
			}
		}
		for _, m := range list {
			if done[m.version] {
				continue
			}
			for _, st := range m.stmts {
				if _, err := q.Exec(ctx, st); err != nil {
					return fmt.Errorf("migration %s: %w", m.name, err)
				}
			}
			if mk := goSteps[m.version]; mk != nil {
				if err := mk(s.dialect)(ctx, q); err != nil {
					return fmt.Errorf("migration %s: %w", m.name, err)
				}
			}
			if s.dialect == SQLite && foreignKeysOff[m.version] {
				if err := checkForeignKeys(ctx, q); err != nil {
					return fmt.Errorf("migration %s: %w", m.name, err)
				}
			}
			if _, err := q.Exec(ctx, `INSERT INTO schema_migrations(version, applied) VALUES (?, ?)`, m.version, ms(time.Now())); err != nil {
				return err
			}
		}
		return nil
	}
}
