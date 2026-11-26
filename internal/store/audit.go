package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"petalbook.app/server/internal/auditlog"
)

// --- audit log ---------------------------------------------------------------

// AuditRow is one row of the audit log.
type AuditRow struct {
	ID       string
	At       time.Time
	Kind     string
	Actor    string
	UserID   string
	Username string
	IP       string
	Detail   string
	// TenantID is the tenant the row belongs to; "" when the writer did not say,
	// which InsertAudit fills in from the user the row is about, else the actor,
	// else the server's own (""); "-" says the server's own outright, for an
	// event of the site's layer made by someone who has a tenant.
	TenantID string
	// Category is filed under at insert; "" takes the registered kind's
	// (auditlog.CategoryOf). A row keeps the category it was written with.
	Category string
	// Capacity is the capacity the actor acted in (auditlog.Capacity*); "" for
	// none.
	Capacity string
}

var auditSeq atomic.Uint64

// NewAuditID is an id for an audit row made at t: Unix milliseconds, then a
// sequence, so ids sort by time and then by order of arrival. Every writer of
// the log takes its ids here, so two never collide.
func NewAuditID(t time.Time) string {
	return fmt.Sprintf("%012x%06x", ms(t), auditSeq.Add(1)&0xffffff)
}

// InsertAudit writes rows (a repeated id is ignored).
func InsertAudit(ctx context.Context, q Q, rows []AuditRow) error {
	for _, r := range rows {
		cat := r.Category
		if cat == "" {
			cat = string(auditlog.CategoryOf(r.Kind))
		}
		if _, err := q.Exec(ctx, `INSERT INTO audit_log(id, at, kind, actor, user_id, username, ip, detail, tenant_id, category, capacity)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, CASE WHEN ? = '-' THEN '' ELSE COALESCE(NULLIF(?, ''), (SELECT tenant_id FROM users WHERE id = ?), (SELECT tenant_id FROM users WHERE id = ?), '') END, ?, ?)
			ON CONFLICT (id) DO NOTHING`, r.ID, ms(r.At), r.Kind, r.Actor, r.UserID, r.Username, r.IP, r.Detail, r.TenantID, r.TenantID, r.UserID, r.Actor, cat, r.Capacity); err != nil {
			return err
		}
	}
	return nil
}

// AuditFilter narrows a read of the log. Every field is optional; each one that
// is set is answered by an index that ends in id (the log pages by id).
type AuditFilter struct {
	// Categories: any of these (several are read one index range each and
	// merged by id).
	Categories []string
	Kind       string
	// User is the person the row is about, Actor the one who acted.
	User, Actor string
	// Capacity is auditlog.CapacitySiteAdmin or CapacityTenantAdmin; "" is any.
	Capacity string
	// From and To bound the time: rows at From or later, before To (zero: no
	// bound). They become a range of ids, since an id starts with its time.
	From, To time.Time
	// HideSite leaves out the kinds only the server's administrators see.
	HideSite bool
	// Cursor: only the rows with an id below it ("" for the first page).
	Cursor string
}

// ListAudit returns one page of the log, newest first: at most limit+1 rows (the
// caller cuts it). Only the rows of the scope's tenants are read.
func ListAudit(ctx context.Context, q Q, sc Scope, f AuditFilter, limit int) ([]AuditRow, error) {
	cats := uniqueSorted(f.Categories)
	if len(cats) <= 1 {
		if len(cats) == 1 {
			f.Categories = cats
		}
		return listAudit(ctx, q, sc, f, limit)
	}
	// Several categories: the index of one is ordered by id, a union of them is
	// not, so each is read for one page and the pages merged.
	var all []AuditRow
	for _, c := range cats {
		f.Categories = []string{c}
		rows, err := listAudit(ctx, q, sc, f, limit)
		if err != nil {
			return nil, err
		}
		all = append(all, rows...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID > all[j].ID })
	if len(all) > limit+1 {
		all = all[:limit+1]
	}
	return all, nil
}

func uniqueSorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	n := 0
	for i, s := range out {
		if s != "" && (i == 0 || s != out[i-1]) {
			out[n] = s
			n++
		}
	}
	return out[:n]
}

func listAudit(ctx context.Context, q Q, sc Scope, f AuditFilter, limit int) ([]AuditRow, error) {
	var conds []string
	var args []any
	sCond, sArgs := sc.cond("tenant_id")
	conds, args = append(conds, sCond), append(args, sArgs...)
	if f.HideSite {
		site := auditlog.SiteOnlyKinds()
		if len(site) > 0 {
			conds = append(conds, `kind NOT IN (`+strings.TrimSuffix(strings.Repeat("?, ", len(site)), ", ")+`)`)
			for _, k := range site {
				args = append(args, k)
			}
		}
	}
	eq := func(col, v string) {
		if v != "" {
			conds, args = append(conds, col+` = ?`), append(args, v)
		}
	}
	if len(f.Categories) == 1 {
		eq("category", f.Categories[0])
	}
	eq("kind", f.Kind)
	eq("user_id", f.User)
	eq("actor", f.Actor)
	if f.Capacity != "" {
		// The partial index (capacity, id) holds the rows that have one only; the
		// second term is what lets the planner use it.
		conds = append(conds, `capacity = ? AND capacity <> ''`)
		args = append(args, f.Capacity)
	}
	if !f.From.IsZero() {
		conds, args = append(conds, `id >= ?`), append(args, AuditIDBefore(f.From))
	}
	if !f.To.IsZero() {
		conds, args = append(conds, `id < ?`), append(args, AuditIDBefore(f.To))
	}
	if f.Cursor != "" {
		conds, args = append(conds, `id < ?`), append(args, f.Cursor)
	}
	args = append(args, limit+1)
	rows, err := q.Query(ctx, `SELECT id, at, kind, actor, user_id, username, ip, detail, tenant_id, category, capacity FROM audit_log WHERE `+
		strings.Join(conds, ` AND `)+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditRow
	for rows.Next() {
		var r AuditRow
		var at int64
		if err := rows.Scan(&r.ID, &at, &r.Kind, &r.Actor, &r.UserID, &r.Username, &r.IP, &r.Detail, &r.TenantID, &r.Category, &r.Capacity); err != nil {
			return nil, err
		}
		r.At = fromMS(at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// AuditStreamPage is how many rows StreamAudit reads in one query.
const AuditStreamPage = 1000

// StreamAudit calls fn for each row of the filter, newest first, at most max of
// them, reading a page of AuditStreamPage rows at a time (each page its own
// query, so no read is held open while fn writes to a slow client). It says
// whether more rows were left when it stopped at max.
func StreamAudit(ctx context.Context, q Q, sc Scope, f AuditFilter, max int, fn func(AuditRow) error) (n int, more bool, err error) {
	for n < max {
		want := AuditStreamPage
		if max-n < want {
			want = max - n
		}
		rows, err := ListAudit(ctx, q, sc, f, want)
		if err != nil {
			return n, false, err
		}
		page := rows
		if len(page) > want {
			page = page[:want]
		}
		for _, r := range page {
			if err := fn(r); err != nil {
				return n, false, err
			}
			n++
		}
		if len(rows) <= want {
			return n, false, nil
		}
		f.Cursor = page[len(page)-1].ID
		if err := ctx.Err(); err != nil {
			return n, true, err
		}
	}
	return n, true, nil
}

// AuditTrimBatch is how many rows TrimAudit deletes in one statement.
const AuditTrimBatch = 5000

// AuditIDBefore is the smallest audit id of a row written at t or later: an id
// is the time in Unix milliseconds as 12 hexadecimal digits, then a sequence
// number (see NewAuditID), so the ids below it are the rows older than t.
func AuditIDBefore(t time.Time) string { return fmt.Sprintf("%012x", ms(t)) }

// AuditRetention is what the log keeps (the audit.keep* settings).
type AuditRetention struct {
	// Days and Rows: a row is kept while it is younger than Days OR (Mode "or",
	// the default) / AND (Mode "and") among the newest Rows rows.
	Days, Rows int
	Mode       string
	// OptionalDays is how long a row of an optional category is kept at most.
	OptionalDays int
}

// auditTrimBounds works out what a trim deletes: every row below core in id, and
// every row of an optional category below optional. "" is nothing. The rows
// kept are ids from the bounds up, so each is a plain range of the primary
// key (or of (category, id)).
//
//	old      = id < D (older than Days)
//	outside  = id < R (not among the newest Rows; "" when there are fewer rows)
//	or:  delete what is old and outside        -> min(D, R)
//	and: delete what is old or outside         -> max(D, R)
//
// A core row is never deleted while younger than the floor, so its bound is at
// most the floor's; an optional row also goes when older than OptionalDays.
func auditTrimBounds(now time.Time, p AuditRetention, oldestKept string) (core, optional string) {
	d := AuditIDBefore(now.Add(-time.Duration(p.Days) * 24 * time.Hour))
	general := d
	if p.Mode == "and" {
		if oldestKept > general {
			general = oldestKept
		}
	} else if oldestKept == "" || oldestKept < general {
		general = oldestKept
	}
	core = general
	if floor := AuditIDBefore(now.Add(-auditlog.CoreFloor)); general > floor {
		core = floor
	}
	optional = general
	if o := AuditIDBefore(now.Add(-time.Duration(p.OptionalDays) * 24 * time.Hour)); o > optional {
		optional = o
	}
	return core, optional
}

// TrimAudit deletes what p says the log does not keep, in batches of
// AuditTrimBatch rows, each batch its own statement: on q the database (not a
// transaction) each is its own transaction, so a large trim never holds
// SQLite's one write connection, nor PostgreSQL's locks, for long. It reads one
// id (the newest-Rows cut) and runs a range delete by primary key, then one by
// (category, id) for each optional category; no table scan.
func TrimAudit(ctx context.Context, q Q, now time.Time, p AuditRetention) (int64, error) {
	if p.Rows < 1 {
		p.Rows = 1
	}
	var oldestKept string
	err := q.QueryRow(ctx, `SELECT id FROM audit_log ORDER BY id DESC LIMIT 1 OFFSET ?`, p.Rows-1).Scan(&oldestKept)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	core, optional := auditTrimBounds(now, p, oldestKept)
	var total int64
	if core != "" {
		n, err := trimAuditWhere(ctx, q, `id < ?`, core)
		total += n
		if err != nil {
			return total, err
		}
	}
	if optional > core {
		for _, c := range auditlog.Categories() {
			if c.Core {
				continue
			}
			n, err := trimAuditWhere(ctx, q, `category = ? AND id < ?`, string(c.ID), optional)
			total += n
			if err != nil {
				return total, err
			}
		}
	}
	return total, nil
}

// trimAuditWhere deletes the rows cond matches (a range of ids, with the
// category if it names one), the oldest first, in batches; args are cond's.
func trimAuditWhere(ctx context.Context, q Q, cond string, args ...any) (int64, error) {
	var total int64
	for {
		res, err := q.Exec(ctx, `DELETE FROM audit_log WHERE id IN (SELECT id FROM audit_log WHERE `+cond+` ORDER BY id LIMIT ?)`, append(append([]any(nil), args...), AuditTrimBatch)...)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
		if n < AuditTrimBatch || ctx.Err() != nil {
			return total, ctx.Err()
		}
	}
}
