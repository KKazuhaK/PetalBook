package store

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The schema is one baseline, schema.sql, edited in place until the first
// release. A database is made from it in one transaction and records which
// baseline it was made from (the hash of its statements, in the one row of
// schema_baseline). A database made from any other baseline, or by a build
// from before the baseline existed (schema_migrations), is refused: nothing
// here converts one. After the first release, the code that upgrades a
// database from one release to the next lives in internal/store/upgrade, apart
// from the schema.

//go:embed schema.sql
var schemaSQL string

// ErrOtherBaseline is the refusal of a database that was made from another
// baseline of the schema.
var ErrOtherBaseline = errors.New("this database was made by an earlier development build of PetalBook (another schema baseline); recreate it: delete the SQLite file, or drop and create the PostgreSQL database")

// statement is one statement of the schema, and the database it is for ("" for
// both).
type statement struct {
	sql     string
	dialect Dialect
}

// dialectMark is the comment line that makes the next statement run on one
// database only: "-- dialect: postgres" or "-- dialect: sqlite".
const dialectMark = "-- dialect:"

// splitStatements cuts a schema at semicolons, dropping comment lines. A
// dialect mark applies to the statement that starts after it.
func splitStatements(text string) ([]statement, error) {
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
	for _, l := range strings.Split(text, "\n") {
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

// sqlFor is the statement's text on d, whatever database it was marked for.
func (st statement) sqlFor(d Dialect) string {
	st.dialect = ""
	text, _ := st.forDialect(d)
	return text
}

// baseline is the schema's statements and its hash: that of the statements as
// written (comments and blank lines do not count), so a change of a comment
// leaves databases as they are and any other change refuses them.
func baseline() ([]statement, string, error) {
	stmts, err := splitStatements(schemaSQL)
	if err != nil {
		return nil, "", fmt.Errorf("schema.sql: %w", err)
	}
	h := sha256.New()
	for _, st := range stmts {
		fmt.Fprintf(h, "%s\x00%s\x00", st.dialect, st.sql)
	}
	return stmts, hex.EncodeToString(h.Sum(nil))[:16], nil
}

// BaselineHash is the hash of the schema this build makes and opens.
func BaselineHash() string {
	_, h, err := baseline()
	if err != nil {
		panic(err)
	}
	return h
}

// prepareSchema makes the schema in an empty database, or checks that a
// database that has one was made from this baseline. On PostgreSQL two servers
// starting together take turns.
func (s *Store) prepareSchema(ctx context.Context) error {
	stmts, hash, err := baseline()
	if err != nil {
		return err
	}
	return s.Tx(ctx, func(q Q) error {
		if s.dialect == Postgres {
			if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(727274001)`); err != nil {
				return err
			}
		}
		tables, err := s.tableNames(ctx, q)
		if err != nil {
			return err
		}
		if len(tables) == 0 {
			return s.makeSchema(ctx, q, stmts, hash)
		}
		if !tables["schema_baseline"] {
			return ErrOtherBaseline
		}
		var have string
		if err := q.QueryRow(ctx, `SELECT hash FROM schema_baseline WHERE id = 1`).Scan(&have); err != nil {
			return fmt.Errorf("reading the schema baseline: %w", err)
		}
		if have != hash {
			return ErrOtherBaseline
		}
		return nil
	})
}

// tableNames lists the tables of the database (on PostgreSQL, of the schema in
// use).
func (s *Store) tableNames(ctx context.Context, q Q) (map[string]bool, error) {
	stmt := `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`
	if s.dialect == Postgres {
		stmt = `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema()`
	}
	rows, err := q.Query(ctx, stmt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
	}
	return out, rows.Err()
}

// makeSchema runs the baseline in an empty database, makes the rows every
// database starts with (the default tenant, with the time it was made, and its
// usage row) and records the baseline.
func (s *Store) makeSchema(ctx context.Context, q Q, stmts []statement, hash string) error {
	for _, st := range stmts {
		text, ok := st.forDialect(s.dialect)
		if !ok {
			continue
		}
		if _, err := q.Exec(ctx, text); err != nil {
			return fmt.Errorf("schema.sql: %w in %.80q", err, text)
		}
	}
	now := ms(time.Now())
	for _, st := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tenants(id, slug, name, name_key, disabled, quota, created, updated) VALUES (?, ?, 'Default', ?, 0, '{}', ?, ?)`,
			[]any{DefaultTenantID, DefaultTenantSlug, FoldName("Default"), now, now}},
		{`INSERT INTO usage(kind, id, tenant_id) VALUES ('tenant', ?, ?)`, []any{DefaultTenantID, DefaultTenantID}},
		{statement{sql: `CREATE TABLE schema_baseline (id BIGINT PRIMARY KEY, hash TEXT COLLATE "C" NOT NULL, made BIGINT NOT NULL)`}.sqlFor(s.dialect), nil},
		{`INSERT INTO schema_baseline(id, hash, made) VALUES (1, ?, ?)`, []any{hash, now}},
	} {
		if _, err := q.Exec(ctx, st.sql, st.args...); err != nil {
			return err
		}
	}
	return nil
}
