package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
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

// AuditRowCheck, when set, is called with every row InsertAudit writes, before
// it is written: every writer of the log (the queue, the notices) goes through
// InsertAudit. The tests set it once, at start (see CheckAuditAddresses).
var AuditRowCheck func(r AuditRow)

// CheckAuditAddresses makes InsertAudit record every row whose detail holds a
// value shaped like an email address in a place its kind does not declare
// (auditlog.Kind.AddressFields): the redaction of the audit list reads only the
// declared places. For the main of a test binary: it returns the function that
// lists what was recorded ("kind detail.path"), to fail the run with. (A panic
// would be swallowed by a handler's recover.)
func CheckAuditAddresses() func() []string {
	var mu sync.Mutex
	seen := map[string]bool{}
	AuditRowCheck = func(r AuditRow) {
		if path, _ := auditlog.UndeclaredAddress(r.Kind, r.Detail); path != "" {
			mu.Lock()
			seen[r.Kind+" detail."+path] = true
			mu.Unlock()
		}
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := make([]string, 0, len(seen))
		for k := range seen {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
}

// InsertAudit writes rows (a repeated id is ignored).
func InsertAudit(ctx context.Context, q Q, rows []AuditRow) error {
	for _, r := range rows {
		if AuditRowCheck != nil {
			AuditRowCheck(r)
		}
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

// AuditRetention is what the log keeps (the audit.keep* settings). Nothing is
// kept by a floor: every category's retention is the site's to set.
type AuditRetention struct {
	// Days and Rows: a row is kept while it is younger than Days OR (Mode "or",
	// the default) / AND (Mode "and") among the newest Rows rows. Days 0 is no
	// age limit: with "or" nothing is deleted, with "and" only the rows outside
	// the newest Rows.
	Days, Rows int
	Mode       string
	// OptionalDays is how long a row of an optional category is kept at most
	// (0: no limit of its own).
	OptionalDays int
	// CategoryDays says how long the rows of one category (by id) are kept,
	// instead of all of the above: 0 keeps them forever, n deletes them after n
	// days, whatever Rows says. A category not in it follows the rules above.
	CategoryDays map[string]int
}

// auditTrimBounds works out what a trim deletes: for every category the id
// below which its rows go ("" is nothing). The rows kept are ids from the bound
// up, so each is a plain range of (category, id).
//
//	old      = id < D (older than Days; "" when Days is 0)
//	outside  = id < R (not among the newest Rows; "" when there are fewer rows)
//	or:  delete what is old and outside        -> min(D, R)
//	and: delete what is old or outside         -> max(D, R)
//
// An optional category's rows also go when older than OptionalDays; a category
// with its own days has that bound alone.
func auditTrimBounds(now time.Time, p AuditRetention, oldestKept string) map[string]string {
	before := func(days int) string {
		if days <= 0 {
			return ""
		}
		return AuditIDBefore(now.Add(-time.Duration(days) * 24 * time.Hour))
	}
	general := before(p.Days)
	if p.Mode == "and" {
		if oldestKept > general {
			general = oldestKept
		}
	} else if oldestKept == "" || oldestKept < general {
		general = oldestKept
	}
	out := map[string]string{}
	for _, c := range auditlog.Categories() {
		id := string(c.ID)
		if d, own := p.CategoryDays[id]; own {
			out[id] = before(d)
			continue
		}
		b := general
		if !c.Core {
			if o := before(p.OptionalDays); o > b {
				b = o
			}
		}
		out[id] = b
	}
	// A category the registry does not know (a row of an older or later
	// version) follows the general rule.
	out[unknownCategories] = general
	return out
}

// unknownCategories is the key of auditTrimBounds for the rows whose category
// is none of auditlog.Categories().
const unknownCategories = "*"

// TrimAudit deletes what p says the log does not keep, in batches of
// AuditTrimBatch rows, each batch its own statement: on q the database (not a
// transaction) each is its own transaction, so a large trim never holds
// SQLite's one write connection, nor PostgreSQL's locks, for long. It reads one
// id (the newest-Rows cut) and runs a range delete by (category, id) for each
// category; no table scan.
func TrimAudit(ctx context.Context, q Q, now time.Time, p AuditRetention) (int64, error) {
	if p.Rows < 1 {
		p.Rows = 1
	}
	var oldestKept string
	err := q.QueryRow(ctx, `SELECT id FROM audit_log ORDER BY id DESC LIMIT 1 OFFSET ?`, p.Rows-1).Scan(&oldestKept)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	bounds := auditTrimBounds(now, p, oldestKept)
	var total int64
	// The record of purges is never trimmed: it is the only trace of a deletion.
	for _, c := range auditlog.Categories() {
		b := bounds[string(c.ID)]
		if b == "" {
			continue
		}
		n, err := trimAuditWhere(ctx, q, nil, `category = ? AND kind <> ? AND id < ?`, string(c.ID), AuditPurgedKind, b)
		total += n
		if err != nil {
			return total, err
		}
	}
	if b := bounds[unknownCategories]; b != "" {
		cats := auditlog.Categories()
		marks := strings.TrimSuffix(strings.Repeat("?, ", len(cats)), ", ")
		args := make([]any, 0, len(cats)+2)
		for _, c := range cats {
			args = append(args, string(c.ID))
		}
		args = append(args, AuditPurgedKind, b)
		n, err := trimAuditWhere(ctx, q, nil, `category NOT IN (`+marks+`) AND kind <> ? AND id < ?`, args...)
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// AuditJobPurge is the kind of the job that runs a purge by hand, and
// AuditPurgeLock its lock key: one purge runs at a time.
const (
	JobAuditPurge  = "audit.purge"
	AuditPurgeLock = "audit.purge"
)

// AuditPurgedKind is the kind of the one row a purge writes about itself.
const AuditPurgedKind = "audit_purged"

// AuditPurge says which rows a manual purge covers: those of Category ("" for
// every category) written before Before (zero for any time). The rows of the
// kind AuditPurgedKind, the record of purges, are never covered.
type AuditPurge struct {
	Category string
	Before   time.Time
}

func (p AuditPurge) where() (string, []any) {
	cond, args := `kind <> ?`, []any{AuditPurgedKind}
	if p.Category != "" {
		cond += ` AND category = ?`
		args = append(args, p.Category)
	}
	if !p.Before.IsZero() {
		cond += ` AND id < ?`
		args = append(args, AuditIDBefore(p.Before))
	}
	return cond, args
}

// AuditCountCap is the most CountAuditPurge counts: a dry run never reads more
// rows than this, whatever the log holds.
const AuditCountCap = 1_000_000

// AuditPurgePause is how long a purge rests between two batches, so that the
// writers of the log (and SQLite's one write connection) get their turn.
var AuditPurgePause = 20 * time.Millisecond

// CountAuditPurge is how many rows PurgeAudit would delete now (the dry run),
// at most AuditCountCap: more says the count stopped there. The count reads
// the (category, id) index when p names a category, and an id range otherwise,
// and stops at the cap either way.
func CountAuditPurge(ctx context.Context, q Q, p AuditPurge) (n int64, more bool, err error) {
	return CountAuditPurgeUpTo(ctx, q, p, AuditCountCap)
}

// CountAuditPurgeUpTo is CountAuditPurge with the cap given.
func CountAuditPurgeUpTo(ctx context.Context, q Q, p AuditPurge, limit int64) (n int64, more bool, err error) {
	cond, args := p.where()
	err = q.QueryRow(ctx, `SELECT COUNT(*) FROM (SELECT 1 FROM audit_log WHERE `+cond+` LIMIT ?) c`, append(args, limit+1)...).Scan(&n)
	if n > limit {
		n, more = limit, true
	}
	return n, more, err
}

// PurgeAudit deletes the rows p covers, oldest first, in batches of
// AuditTrimBatch like TrimAudit, resting AuditPurgePause between them, and
// returns how many went. step, when set, is told how many each batch deleted.
// Running it again after a stop is safe: it deletes what the same bounds still
// cover.
func PurgeAudit(ctx context.Context, q Q, p AuditPurge, step func(n int64)) (int64, error) {
	cond, args := p.where()
	return trimAuditWhere(ctx, q, step, cond, args...)
}

// trimAuditWhere deletes the rows cond matches (a range of ids, with the
// category if it names one), the oldest first, in batches, each its own
// statement; args are cond's. step, when set, is told each batch's count.
func trimAuditWhere(ctx context.Context, q Q, step func(int64), cond string, args ...any) (int64, error) {
	var total int64
	for {
		res, err := q.Exec(ctx, `DELETE FROM audit_log WHERE id IN (SELECT id FROM audit_log WHERE `+cond+` ORDER BY id LIMIT ?)`, append(append([]any(nil), args...), AuditTrimBatch)...)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
		if step != nil && n > 0 {
			step(n)
		}
		if n < AuditTrimBatch || ctx.Err() != nil {
			return total, ctx.Err()
		}
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		case <-time.After(AuditPurgePause):
		}
	}
}
