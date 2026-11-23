// Package store is the state database: users, sign-in, spaces and drafts. It
// runs on SQLite (the default, one file) and PostgreSQL with one schema and one
// set of queries: only TEXT and BIGINT columns, times as Unix milliseconds,
// booleans as 0/1, and "?" placeholders that are rewritten for PostgreSQL.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // PostgreSQL driver
	"modernc.org/sqlite"
)

// Dialect names the database in use.
type Dialect string

const (
	SQLite   Dialect = "sqlite"
	Postgres Dialect = "postgres"
)

// Errors the callers map to answers.
var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("already exists")
)

// Store is safe for concurrent use.
type Store struct {
	db      *sql.DB
	dialect Dialect
	rebinds sync.Map // query -> rewritten query (PostgreSQL only)
	// tenantState is which tenants are disabled (see TenantDisabled).
	tenantState atomic.Pointer[tenantState]
}

// Q is what queries run on: the database, or a transaction of it.
type Q interface {
	Exec(ctx context.Context, query string, args ...any) (sql.Result, error)
	Query(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, query string, args ...any) *sql.Row
}

type runner interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type bound struct {
	s *Store
	r runner
}

func (b bound) Exec(ctx context.Context, q string, a ...any) (sql.Result, error) {
	return b.r.ExecContext(ctx, b.s.rebind(q), a...)
}
func (b bound) Query(ctx context.Context, q string, a ...any) (*sql.Rows, error) {
	return b.r.QueryContext(ctx, b.s.rebind(q), a...)
}
func (b bound) QueryRow(ctx context.Context, q string, a ...any) *sql.Row {
	return b.r.QueryRowContext(ctx, b.s.rebind(q), a...)
}

// Q runs queries outside a transaction.
func (s *Store) Q() Q { return bound{s, s.db} }

// Dialect reports which database this is.
func (s *Store) Dialect() Dialect { return s.dialect }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Tx runs fn in a transaction: committed when it returns nil, rolled back
// otherwise. Inside fn use only the Q it is given. On SQLite the whole store
// has one connection, so a query on s.Q() inside fn would wait for the
// transaction forever.
func (s *Store) Tx(ctx context.Context, fn func(Q) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(bound{s, tx}); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// rebind rewrites ? placeholders to $1, $2... for PostgreSQL.
func (s *Store) rebind(q string) string {
	if s.dialect != Postgres {
		return q
	}
	if v, ok := s.rebinds.Load(q); ok {
		return v.(string)
	}
	var b strings.Builder
	n := 0
	for i := 0; i < len(q); i++ {
		if q[i] == '?' {
			n++
			b.WriteString("$" + strconv.Itoa(n))
		} else {
			b.WriteByte(q[i])
		}
	}
	s.rebinds.Store(q, b.String())
	return b.String()
}

// Open opens the state database at target and brings its schema up to date.
// target is a postgres:// or postgresql:// URL, or the path of a SQLite file
// (created if missing). The error never contains the password of a URL.
func Open(ctx context.Context, target string) (*Store, error) {
	var s *Store
	var err error
	if strings.HasPrefix(target, "postgres://") || strings.HasPrefix(target, "postgresql://") {
		s, err = openPostgres(target)
	} else {
		s, err = openSQLite(target)
	}
	if err != nil {
		return nil, redact(err, target)
	}
	if err := s.migrate(ctx); err != nil {
		s.Close()
		return nil, redact(err, target)
	}
	if err := s.loadTenantState(ctx); err != nil {
		s.Close()
		return nil, redact(err, target)
	}
	return s, nil
}

func openSQLite(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("no database path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	// A file: URI keeps characters such as ? and # in the path from being
	// read as options. One connection: SQLite has one writer anyway, and
	// this way a write never fails with SQLITE_BUSY inside the process.
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	// FULL: a draft answered with 200 must survive a power cut, as a file
	// written by 1.1 did. (The index is a cache and keeps NORMAL.)
	q.Add("_pragma", "synchronous(FULL)")
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Set("_txlock", "immediate")
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs), RawQuery: q.Encode()}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, dialect: SQLite}, nil
}

func openPostgres(dsn string) (*Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(20)
	db.SetConnMaxLifetime(30 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, dialect: Postgres}, nil
}

// redact removes the target, and the password inside it, from an error text.
func redact(err error, target string) error {
	msg := err.Error()
	if target != "" {
		msg = strings.ReplaceAll(msg, target, "<database>")
	}
	if u, perr := url.Parse(target); perr == nil && u.User != nil {
		if pw, ok := u.User.Password(); ok && pw != "" {
			msg = strings.ReplaceAll(msg, pw, "xxxxx")
		}
	}
	return fmt.Errorf("database: %s", msg)
}

// UniqueOn reports whether err is a unique-constraint violation, and which
// columns or constraint it names (for example "users.username" on SQLite,
// "users_username_key" on PostgreSQL), in lower case.
func UniqueOn(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return strings.ToLower(pe.ConstraintName), pe.Code == "23505"
	}
	var se *sqlite.Error
	if errors.As(err, &se) || strings.Contains(err.Error(), "UNIQUE constraint failed") {
		msg := err.Error()
		if i := strings.Index(msg, "UNIQUE constraint failed:"); i >= 0 {
			return strings.ToLower(strings.TrimSpace(msg[i+len("UNIQUE constraint failed:"):])), true
		}
	}
	return "", false
}

// IsUnique reports a unique-constraint violation of any kind.
func IsUnique(err error) bool { _, ok := UniqueOn(err); return ok }

func ms(t time.Time) int64     { return t.UnixMilli() }
func fromMS(v int64) time.Time { return time.UnixMilli(v).UTC() }
func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
