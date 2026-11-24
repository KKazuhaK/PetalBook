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
	db *sql.DB
	// rdb is SQLite's pool of read-only connections (nil on PostgreSQL), which
	// the reads outside a transaction use (see Q).
	rdb     *sql.DB
	dialect Dialect
	rebinds sync.Map // query -> rewritten query (PostgreSQL only)
	// people is what NoteActive and NoteWrite remember of who was written lately.
	people noted
	// tenantState is which tenants are disabled (see TenantDisabled).
	tenantState atomic.Pointer[tenantState]
	// localeWarning is what is wrong with a PostgreSQL database's locale ("" when
	// nothing is; see LocaleWarning).
	localeWarning string
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
	// reads, when set, runs the plain SELECTs (see Q).
	reads runner
}

func (b bound) Exec(ctx context.Context, q string, a ...any) (sql.Result, error) {
	return b.r.ExecContext(ctx, b.s.rebind(q), a...)
}
func (b bound) Query(ctx context.Context, q string, a ...any) (*sql.Rows, error) {
	return b.reader(q).QueryContext(ctx, b.s.rebind(q), a...)
}
func (b bound) QueryRow(ctx context.Context, q string, a ...any) *sql.Row {
	return b.reader(q).QueryRowContext(ctx, b.s.rebind(q), a...)
}

// reader is where a query runs: a plain SELECT on the read pool when there is
// one, anything else (a PRAGMA, a statement with RETURNING) on r.
func (b bound) reader(q string) runner {
	if b.reads != nil && isPlainSelect(q) {
		return b.reads
	}
	return b.r
}

// isPlainSelect reports whether q is a SELECT, which a read-only connection can
// run.
func isPlainSelect(q string) bool {
	q = strings.TrimSpace(q)
	return len(q) > 6 && strings.EqualFold(q[:6], "SELECT") && !strings.Contains(q, "FOR UPDATE") && !strings.Contains(q, "FOR NO KEY UPDATE")
}

// Q runs queries outside a transaction.
//
// On SQLite its SELECTs run on a pool of read-only connections (readConns),
// beside the one connection that writes, so that reads do not wait in line
// behind writes and each other; its other statements run on the writer. In WAL
// mode a read sees every write committed before it starts, so a caller that
// writes and then reads, each on Q, reads what it wrote. The rule: what must be
// read in the same state as a write (read, decide, write; or a write and a read
// that must not see a later write between them) runs in one Tx, which is on the
// writer, reads and all. A read on Q while the same goroutine holds a Tx open
// sees the database without that transaction's writes (on PostgreSQL as
// well); a Tx is used through its own Q only.
func (s *Store) Q() Q {
	if s.rdb != nil {
		return bound{s: s, r: s.db, reads: s.rdb}
	}
	return bound{s: s, r: s.db}
}

// Stats reports the statistics of the pool of connections that writes (on
// PostgreSQL, the only one).
func (s *Store) Stats() sql.DBStats { return s.db.Stats() }

// Dialect reports which database this is.
func (s *Store) Dialect() Dialect { return s.dialect }

// Close closes the database.
func (s *Store) Close() error {
	if s.rdb != nil {
		s.rdb.Close()
	}
	return s.db.Close()
}

// Tx runs fn in a transaction: committed when it returns nil, rolled back
// otherwise. Inside fn use only the Q it is given. On SQLite the store has one
// connection that writes, so a write on s.Q() inside fn would wait for the
// transaction forever (and a read on s.Q() would not see fn's writes).
func (s *Store) Tx(ctx context.Context, fn func(Q) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(bound{s: s, r: tx}); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// atomically runs fn in one transaction: a new one when q is the database
// itself, or q when it already is a transaction.
func atomically(ctx context.Context, q Q, fn func(Q) error) error {
	if b, ok := q.(bound); ok {
		if _, isDB := b.r.(*sql.DB); isDB {
			return b.s.Tx(ctx, fn)
		}
	}
	return fn(q)
}

// isPostgres reports whether q runs on PostgreSQL.
func isPostgres(q Q) bool {
	b, ok := q.(bound)
	return ok && b.s.dialect == Postgres
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
	if err := s.prepareSchema(ctx); err != nil {
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
	// read as options. One connection writes: SQLite has one writer anyway,
	// and this way a write never fails with SQLITE_BUSY inside the process.
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
	// The read pool: the same file, read only. It is opened after the writer
	// has made the file and switched it to WAL.
	r := url.Values{}
	r.Add("_pragma", "busy_timeout(10000)")
	r.Add("_pragma", "foreign_keys(1)")
	r.Add("_pragma", "query_only(1)")
	ru := url.URL{Scheme: "file", Path: filepath.ToSlash(abs), RawQuery: r.Encode()}
	rdb, err := sql.Open("sqlite", ru.String())
	if err != nil {
		db.Close()
		return nil, err
	}
	rdb.SetMaxOpenConns(readConns)
	rdb.SetMaxIdleConns(readConns)
	return &Store{db: db, rdb: rdb, dialect: SQLite}, nil
}

// readConns is the size of SQLite's read pool.
const readConns = 4

func openPostgres(dsn string) (*Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(20)
	// As many idle as open: Go keeps 2 by default, so a burst of requests
	// closed the connections it had just opened and the next burst opened them
	// again (a TCP and TLS handshake and a backend start each).
	db.SetMaxIdleConns(20)
	db.SetConnMaxLifetime(30 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db, dialect: Postgres}
	locale, err := databaseLocale(ctx, db)
	if err != nil {
		db.Close()
		return nil, err
	}
	s.localeWarning = localeProblem(locale)
	return s, nil
}

// LocaleWarning says what is wrong with the locale of a PostgreSQL database, or
// is "" when nothing is (and always on SQLite). Every text column of the schema
// is COLLATE "C", so what the store sorts and compares does not depend on it;
// but a database whose default collation is not bytewise sorts any other text
// (an expression, a literal) differently from SQLite, and lower() and upper()
// follow its LC_CTYPE. The server logs this at start; README.md says how
// to make the database.
func (s *Store) LocaleWarning() string { return s.localeWarning }

// databaseLocale reads the locale columns of the current database from
// pg_database: datcollate, datctype and, where the server has them,
// datlocprovider (15) and datlocale (17).
func databaseLocale(ctx context.Context, db *sql.DB) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT * FROM pg_database WHERE datname = current_database()`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if !rows.Next() {
		return out, rows.Err()
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	for i, c := range cols {
		switch c {
		case "datcollate", "datctype", "datlocprovider", "datlocale":
			if vals[i] != nil {
				out[c] = fmt.Sprint(vals[i])
			}
		}
	}
	return out, rows.Err()
}

// localeProblem is the warning for a database's locale columns, "" when it
// sorts text bytewise: the builtin provider (any of its locales sorts by code
// point, which is byte order in UTF-8), or libc with C, POSIX or C.UTF-8.
func localeProblem(l map[string]string) string {
	bytewise := func(v string) bool {
		return v == "C" || v == "POSIX" || strings.HasPrefix(v, "C.")
	}
	switch l["datlocprovider"] {
	case "b":
		return ""
	case "i":
		return "the database uses an ICU locale (" + l["datlocale"] + "); PetalBook's text columns sort bytewise (COLLATE \"C\") anyway, " +
			"but make the database with LOCALE_PROVIDER builtin BUILTIN_LOCALE 'C.UTF-8' (see README.md, PostgreSQL) so that nothing else depends on the locale"
	}
	if bytewise(l["datcollate"]) && bytewise(l["datctype"]) {
		return ""
	}
	return "the database's locale is LC_COLLATE " + l["datcollate"] + ", LC_CTYPE " + l["datctype"] + "; PetalBook's text columns sort bytewise (COLLATE \"C\") anyway, " +
		"but make the database with LOCALE_PROVIDER builtin BUILTIN_LOCALE 'C.UTF-8' (see README.md, PostgreSQL) so that nothing else depends on the locale"
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
