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
	7: fillSpaceNameKeys,
}

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
func (s *Store) migrate(ctx context.Context) error {
	list, err := loadMigrations()
	if err != nil {
		return err
	}
	return s.Tx(ctx, func(q Q) error {
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
	})
}
