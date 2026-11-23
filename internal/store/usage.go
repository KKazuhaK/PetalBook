package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Usage (milestone 3.3, docs/api.md "Quotas"): what each person and each tenant
// use, kept as one row of the usage table apiece so that reading anyone's usage
// is a primary-key lookup, never a walk.
//
// How the rows stay right, and what each costs:
//
//   - A space counts for its tenant from the moment it is made, and for its
//     first owner (the earliest owner membership) from the moment it has one.
//     The statements that change a space, a member or a measurement adjust the
//     rows by the difference, in the transaction that changes the thing, so a
//     crash cannot leave a half: two upserts, no scan.
//   - spaces.usage_user remembers whom a space is counted for, so that a change
//     of the first owner moves exactly what was added, whatever happened to the
//     measurements in between (settleOwner).
//   - Bytes and pages are what the background measurement last found
//     (RecordMeasurement): the difference to the space's previous values is
//     added to its owner's row and its tenant's.
//   - Whatever drifts anyway (a crash between two transactions, a hand-edited
//     database) is put right once a day by RecountUsage: one grouped query per
//     table, and an update of the rows that differ.
//
// Row locks are taken in a fixed order (the rows sorted by kind and id, after
// the space's own row), so two transactions on PostgreSQL cannot wait for each
// other's rows.

// The kinds of usage rows.
const (
	UsageUser   = "user"
	UsageTenant = "tenant"
)

// Usage is one row of the usage table.
type Usage struct {
	Kind, ID, TenantID string
	Spaces, Bytes      int64
	Pages, People      int64
	// Over lists the quotas the row was last found to be over.
	Over []string
	// Measured is when a measurement last changed the row (zero: never).
	Measured time.Time
}

type usageDelta struct {
	kind, id, tenant             string
	spaces, bytes, pages, people int64
	measured                     int64 // Unix milliseconds, 0: not a measurement
}

func (d usageDelta) key() string { return d.kind + "\x00" + d.id }

// addUsage applies deltas, the rows in a fixed order. A row that does not exist
// yet is made.
func addUsage(ctx context.Context, q Q, ds ...usageDelta) error {
	sort.Slice(ds, func(i, j int) bool { return ds[i].key() < ds[j].key() })
	for _, d := range ds {
		if _, err := q.Exec(ctx, `INSERT INTO usage(kind, id, tenant_id, spaces, bytes, pages, people, measured) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (kind, id) DO UPDATE SET spaces = usage.spaces + excluded.spaces, bytes = usage.bytes + excluded.bytes,
				pages = usage.pages + excluded.pages, people = usage.people + excluded.people,
				measured = CASE WHEN excluded.measured > 0 THEN excluded.measured ELSE usage.measured END`,
			d.kind, d.id, d.tenant, d.spaces, d.bytes, d.pages, d.people, d.measured); err != nil {
			return err
		}
	}
	return nil
}

// usageOfNewUser is what InsertUser adds: the person's own row, and one more
// person in the tenant.
func usageOfNewUser(ctx context.Context, q Q, id, tenant string) error {
	return addUsage(ctx, q,
		usageDelta{kind: UsageUser, id: id, tenant: tenant},
		usageDelta{kind: UsageTenant, id: tenant, tenant: tenant, people: 1})
}

// usageOfNewSpace is what InsertSpace adds: one more space of the tenant. The
// person it is counted for comes with its first owner (settleOwner).
func usageOfNewSpace(ctx context.Context, q Q, tenant string) error {
	return addUsage(ctx, q, usageDelta{kind: UsageTenant, id: tenant, tenant: tenant, spaces: 1})
}

type spaceUsageRow struct {
	tenant, user  string
	bytes, pages  int64
	firstOwner    string
	hasFirstOwner bool
}

func readSpaceUsage(ctx context.Context, q Q, id string) (spaceUsageRow, error) {
	var r spaceUsageRow
	err := q.QueryRow(ctx, `SELECT tenant_id, usage_user, usage_bytes, page_count FROM spaces WHERE id = ?`, id).Scan(&r.tenant, &r.user, &r.bytes, &r.pages)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// firstOwnerOf is the person a space is counted for: the earliest owner, the
// one whose name is the default Git author of the space (see SpaceOwner), or ""
// when nobody owns it.
func firstOwnerOf(ctx context.Context, q Q, spaceID string) (string, error) {
	var id string
	err := q.QueryRow(ctx, `SELECT user_id FROM space_members WHERE space_id = ? AND role = 'owner' ORDER BY created, user_id LIMIT 1`, spaceID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// settleOwner makes the usage rows say that the space is counted for its first
// owner: when that is not who it was counted for, what the space holds moves
// from one row to the other. It is called after every change of who owns a
// space, in the same transaction, and may be called any number of times. It
// takes the space's lock first (as every change of members does), so two
// changes of one space are one after the other.
func settleOwner(ctx context.Context, q Q, spaceID string) error {
	if err := LockSpace(ctx, q, spaceID); err != nil {
		return err
	}
	r, err := readSpaceUsage(ctx, q, spaceID)
	if err != nil {
		return err
	}
	first, err := firstOwnerOf(ctx, q, spaceID)
	if err != nil {
		return err
	}
	if first == r.user {
		return nil
	}
	if _, err := q.Exec(ctx, `UPDATE spaces SET usage_user = ? WHERE id = ?`, first, spaceID); err != nil {
		return err
	}
	var ds []usageDelta
	if r.user != "" {
		ds = append(ds, usageDelta{kind: UsageUser, id: r.user, tenant: r.tenant, spaces: -1, bytes: -r.bytes, pages: -r.pages})
	}
	if first != "" {
		ds = append(ds, usageDelta{kind: UsageUser, id: first, tenant: r.tenant, spaces: 1, bytes: r.bytes, pages: r.pages})
	}
	return addUsage(ctx, q, ds...)
}

// forgetSpaceUsage takes a space that is about to be deleted out of the rows it
// is counted in.
func forgetSpaceUsage(ctx context.Context, q Q, spaceID string) error {
	r, err := readSpaceUsage(ctx, q, spaceID)
	if err != nil {
		return err
	}
	ds := []usageDelta{{kind: UsageTenant, id: r.tenant, tenant: r.tenant, spaces: -1, bytes: -r.bytes, pages: -r.pages}}
	if r.user != "" {
		ds = append(ds, usageDelta{kind: UsageUser, id: r.user, tenant: r.tenant, spaces: -1, bytes: -r.bytes, pages: -r.pages})
	}
	return addUsage(ctx, q, ds...)
}

// Measurement is what the background measurement found in one space.
type Measurement struct {
	// Bytes is the size of the files of the space's directory.
	Bytes int64
	// Pages is the number of pages in its index; HavePages is false when the
	// index could not be read (a space never opened), and then the space keeps
	// the page count it had.
	Pages     int64
	HavePages bool
	At        time.Time
}

// Touched is who a change of usage reached, for the caller to look at their
// quotas.
type Touched struct {
	UserID, TenantID string
}

// RecordMeasurement stores a measurement of a space: its listing size (size_bytes
// and size_at), and the bytes and pages the quotas count. The difference to what
// the space had is added to the rows of its first owner and its tenant in the
// same transaction. It returns ErrNotFound for a space that is gone.
//
// A space whose measurement covers only part of it (a partial clone, or one
// whose files were evicted, as 3.1 will have) must not call this: its last full
// measurement stays what the quotas count, because what the server holds of such
// a space is not what the space is. Such code writes the listing's size with
// SetSpaceSize and leaves usage alone.
func (s *Store) RecordMeasurement(ctx context.Context, spaceID string, m Measurement) (t Touched, err error) {
	err = s.Tx(ctx, func(q Q) error {
		if err := LockSpace(ctx, q, spaceID); err != nil {
			return err
		}
		r, err := readSpaceUsage(ctx, q, spaceID)
		if err != nil {
			return err
		}
		pages := r.pages
		if m.HavePages {
			pages = m.Pages
		}
		if _, err := q.Exec(ctx, `UPDATE spaces SET size_bytes = ?, size_at = ?, usage_bytes = ?, page_count = ? WHERE id = ?`,
			m.Bytes, ms(m.At), m.Bytes, pages, spaceID); err != nil {
			return err
		}
		db, dp := m.Bytes-r.bytes, pages-r.pages
		ds := []usageDelta{{kind: UsageTenant, id: r.tenant, tenant: r.tenant, bytes: db, pages: dp, measured: ms(m.At)}}
		if r.user != "" {
			ds = append(ds, usageDelta{kind: UsageUser, id: r.user, tenant: r.tenant, bytes: db, pages: dp, measured: ms(m.At)})
		}
		t = Touched{UserID: r.user, TenantID: r.tenant}
		return addUsage(ctx, q, ds...)
	})
	return t, err
}

// GetUsage returns the row of a person or a tenant; a row that does not exist
// yet is all zeros.
func GetUsage(ctx context.Context, q Q, kind, id string) (Usage, error) {
	u := Usage{Kind: kind, ID: id}
	var over string
	var measured int64
	err := q.QueryRow(ctx, `SELECT tenant_id, spaces, bytes, pages, people, over, measured FROM usage WHERE kind = ? AND id = ?`, kind, id).
		Scan(&u.TenantID, &u.Spaces, &u.Bytes, &u.Pages, &u.People, &over, &measured)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return u, err
	}
	u.Over = splitOver(over)
	if measured > 0 {
		u.Measured = fromMS(measured)
	}
	return u, nil
}

// UsageOfTenants returns the rows of the given tenants, by id: one query for
// the page of a list.
func UsageOfTenants(ctx context.Context, q Q, ids []string) (map[string]Usage, error) {
	out := map[string]Usage{}
	if len(ids) == 0 {
		return out, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", ")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := q.Query(ctx, `SELECT id, tenant_id, spaces, bytes, pages, people, over, measured FROM usage WHERE kind = 'tenant' AND id IN (`+marks+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		u := Usage{Kind: UsageTenant}
		var over string
		var measured int64
		if err := rows.Scan(&u.ID, &u.TenantID, &u.Spaces, &u.Bytes, &u.Pages, &u.People, &over, &measured); err != nil {
			return nil, err
		}
		u.Over = splitOver(over)
		if measured > 0 {
			u.Measured = fromMS(measured)
		}
		out[u.ID] = u
	}
	return out, rows.Err()
}

func splitOver(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// SetUsageOver records which quotas a row is over, and reports whether that
// changed it (so a crossing is noticed once).
func SetUsageOver(ctx context.Context, q Q, kind, id string, over []string) (changed bool, err error) {
	res, err := q.Exec(ctx, `UPDATE usage SET over = ? WHERE kind = ? AND id = ? AND over <> ?`, strings.Join(over, ","), kind, id, strings.Join(over, ","))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// UsageEntry is one person in the administrators' list.
type UsageEntry struct {
	Usage
	Username, Name string
	Quota          string // the person's own limits, JSON
}

// UsageFilter picks a page of the list of people by usage.
type UsageFilter struct {
	OverOnly bool
	After    string // the cursor of the page before ("" for the first)
	Limit    int
}

// ListUserUsage returns one page of people in the scope, the most bytes first
// (then by id): one query over the usage table's index, cut before the people's
// names are looked up. next is the cursor of the following page.
func ListUserUsage(ctx context.Context, q Q, sc Scope, f UsageFilter) (out []UsageEntry, next string, err error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	cond, args := sc.cond("tenant_id")
	where := []string{"kind = 'user'", cond}
	if f.OverOnly {
		where = append(where, "over <> ''")
	}
	if f.After != "" {
		bytes, id, ok := strings.Cut(f.After, ":")
		n, perr := strconv.ParseInt(bytes, 10, 64)
		if !ok || perr != nil || id == "" {
			return nil, "", ErrBadCursor
		}
		where = append(where, "(bytes < ? OR (bytes = ? AND id > ?))")
		args = append(args, n, n, id)
	}
	args = append(args, f.Limit+1)
	rows, err := q.Query(ctx, `SELECT p.id, p.tenant_id, p.spaces, p.bytes, p.pages, p.over, p.measured, u.username, u.name, u.quota
		FROM (SELECT id, tenant_id, spaces, bytes, pages, over, measured FROM usage WHERE `+strings.Join(where, " AND ")+`
			ORDER BY bytes DESC, id LIMIT ?) p
		JOIN users u ON u.id = p.id
		ORDER BY p.bytes DESC, p.id`, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	for rows.Next() {
		e := UsageEntry{}
		e.Kind = UsageUser
		var over string
		var measured int64
		if err := rows.Scan(&e.ID, &e.TenantID, &e.Spaces, &e.Bytes, &e.Pages, &over, &measured, &e.Username, &e.Name, &e.Quota); err != nil {
			return nil, "", err
		}
		e.Over = splitOver(over)
		if measured > 0 {
			e.Measured = fromMS(measured)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(out) > f.Limit {
		out = out[:f.Limit]
		last := out[len(out)-1]
		next = strconv.FormatInt(last.Bytes, 10) + ":" + last.ID
	}
	return out, next, nil
}

// ErrBadCursor is a cursor that is not one a list gave.
var ErrBadCursor = errors.New("bad cursor")

// UserQuota returns a person's own limits (JSON, "" for none) and tenant.
func UserQuota(ctx context.Context, q Q, sc Scope, id string) (quota string, err error) {
	cond, args := sc.cond("tenant_id")
	err = q.QueryRow(ctx, `SELECT quota FROM users WHERE id = ? AND `+cond, append([]any{id}, args...)...).Scan(&quota)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return
}

// SetUserQuota stores a person's own limits.
func SetUserQuota(ctx context.Context, q Q, sc Scope, id, quota string) error {
	cond, args := sc.cond("tenant_id")
	res, err := q.Exec(ctx, `UPDATE users SET quota = ? WHERE id = ? AND `+cond, append([]any{quota, id}, args...)...)
	return oneRow(res, err)
}

// UsersWithQuota lists the ids and own limits of the people in a tenant who
// have limits of their own (a person's limit applies over the tenant's).
func UsersWithQuota(ctx context.Context, q Q, sc Scope) (map[string]string, error) {
	cond, args := sc.cond("tenant_id")
	rows, err := q.Query(ctx, `SELECT id, quota FROM users WHERE quota <> '' AND `+cond, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, quota string
		if err := rows.Scan(&id, &quota); err != nil {
			return nil, err
		}
		out[id] = quota
	}
	return out, rows.Err()
}

// UserUsageRows pages through the person rows of a scope in id order, for the
// code that looks at everyone's limits (after a limit changed, or the daily
// recount). It returns the rows after the id given.
func UserUsageRows(ctx context.Context, q Q, sc Scope, after string, limit int) ([]Usage, error) {
	cond, args := sc.cond("tenant_id")
	rows, err := q.Query(ctx, `SELECT id, tenant_id, spaces, bytes, pages, over FROM usage WHERE kind = 'user' AND id > ? AND `+cond+` ORDER BY id LIMIT ?`,
		append(append([]any{after}, args...), limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Usage
	for rows.Next() {
		u := Usage{Kind: UsageUser}
		var over string
		if err := rows.Scan(&u.ID, &u.TenantID, &u.Spaces, &u.Bytes, &u.Pages, &over); err != nil {
			return nil, err
		}
		u.Over = splitOver(over)
		out = append(out, u)
	}
	return out, rows.Err()
}

// seedUsage makes the usage rows of a database that has none (the last step of the
// migration): the first owner of every space, then one row per person and per
// tenant from two grouped queries, each a single statement, so that a server with
// tens of thousands of people and spaces migrates in a fraction of a second. What
// it makes is what recountUsage would make; the daily pass takes over from here.
func seedUsage(ctx context.Context, q Q) error {
	if _, err := q.Exec(ctx, `UPDATE spaces SET usage_user = COALESCE((SELECT m.user_id FROM space_members m WHERE m.space_id = spaces.id AND m.role = 'owner'
			ORDER BY m.created, m.user_id LIMIT 1), '')`); err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `INSERT INTO usage(kind, id, tenant_id, spaces, bytes, pages, people)
		SELECT 'user', u.id, u.tenant_id, COALESCE(a.c, 0), COALESCE(a.b, 0), COALESCE(a.p, 0), 0
		FROM users u LEFT JOIN (SELECT usage_user, COUNT(*) AS c, SUM(usage_bytes) AS b, SUM(page_count) AS p FROM spaces WHERE usage_user <> '' GROUP BY usage_user) a ON a.usage_user = u.id`); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `INSERT INTO usage(kind, id, tenant_id, spaces, bytes, pages, people)
		SELECT 'tenant', t.id, t.id, COALESCE(s.c, 0), COALESCE(s.b, 0), COALESCE(s.p, 0), COALESCE(p.c, 0)
		FROM tenants t
		LEFT JOIN (SELECT tenant_id, COUNT(*) AS c, SUM(usage_bytes) AS b, SUM(page_count) AS p FROM spaces GROUP BY tenant_id) s ON s.tenant_id = t.id
		LEFT JOIN (SELECT tenant_id, COUNT(*) AS c FROM users GROUP BY tenant_id) p ON p.tenant_id = t.id`)
	return err
}

// RecountUsage puts the usage table right from the rows it summarises: the
// first owner of every space, the spaces, bytes and pages of every person and
// every tenant, and the people of every tenant. It is the daily pass (the
// migration makes the rows with seedUsage). Each table is read once, grouped, and only the
// rows that differ are written. It reports how many rows it had to correct.
//
// The reads must see one state of the tables, the one the usage rows describe.
// On SQLite the transaction holds the only write connection, so they do. On
// PostgreSQL each statement has its own snapshot, so a write committed between
// two reads would be undone (or a new row deleted as stale); the recount
// therefore takes the usage table in SHARE ROW EXCLUSIVE mode before it reads.
// Every change of what is counted writes its usage rows in its own transaction,
// so once the lock is held no such change is half done, and none can commit
// until the recount does: they wait, as they wait for the write connection on
// SQLite. The lock is the only one the recount takes before its own writes to
// usage, so it cannot be part of a cycle with the writers' order (space, tenant,
// people, then usage): the first step (whom each space is counted for), which
// locks space rows, is its own transaction before it.
func (s *Store) RecountUsage(ctx context.Context) (corrected int, err error) {
	var moved int
	err = s.Tx(ctx, func(q Q) error {
		moved, err = settleUsageUsers(ctx, q)
		return err
	})
	if err != nil {
		return 0, err
	}
	err = s.Tx(ctx, func(q Q) error {
		if s.dialect == Postgres {
			if _, err := q.Exec(ctx, `LOCK TABLE usage IN SHARE ROW EXCLUSIVE MODE`); err != nil {
				return err
			}
		}
		corrected, err = recountUsage(ctx, q)
		return err
	})
	return moved + corrected, err
}

// settleUsageUsers sets whom each space is counted for to its first owner, where
// it says someone else. The usage rows are put right by recountUsage after it.
func settleUsageUsers(ctx context.Context, q Q) (int, error) {
	res, err := q.Exec(ctx, `UPDATE spaces SET usage_user = COALESCE((SELECT m.user_id FROM space_members m WHERE m.space_id = spaces.id AND m.role = 'owner'
			ORDER BY m.created, m.user_id LIMIT 1), '')
		WHERE usage_user <> COALESCE((SELECT m.user_id FROM space_members m WHERE m.space_id = spaces.id AND m.role = 'owner'
			ORDER BY m.created, m.user_id LIMIT 1), '')`)
	if err != nil {
		return 0, err
	}
	c, err := res.RowsAffected()
	return int(c), err
}

func recountUsage(ctx context.Context, q Q) (int, error) {
	n := 0
	type sum struct{ spaces, bytes, pages, people int64 }
	want := map[string]*sum{} // by usageDelta key
	tenantOf := map[string]string{}
	get := func(kind, id string) *sum {
		k := kind + "\x00" + id
		if want[k] == nil {
			want[k] = &sum{}
		}
		return want[k]
	}
	rows, err := q.Query(ctx, `SELECT usage_user, tenant_id, COUNT(*), SUM(usage_bytes), SUM(page_count) FROM spaces WHERE usage_user <> '' GROUP BY usage_user, tenant_id`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var id, tenant string
		var c, b, p int64
		if err := rows.Scan(&id, &tenant, &c, &b, &p); err != nil {
			rows.Close()
			return 0, err
		}
		s := get(UsageUser, id)
		s.spaces, s.bytes, s.pages = c, b, p
		tenantOf[UsageUser+"\x00"+id] = tenant
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	rows, err = q.Query(ctx, `SELECT tenant_id, COUNT(*), SUM(usage_bytes), SUM(page_count) FROM spaces GROUP BY tenant_id`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var id string
		var c, b, p int64
		if err := rows.Scan(&id, &c, &b, &p); err != nil {
			rows.Close()
			return 0, err
		}
		s := get(UsageTenant, id)
		s.spaces, s.bytes, s.pages = c, b, p
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	// Every person and every tenant has a row, with the people counted.
	rows, err = q.Query(ctx, `SELECT tenant_id, id FROM users`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var tenant, id string
		if err := rows.Scan(&tenant, &id); err != nil {
			rows.Close()
			return 0, err
		}
		get(UsageUser, id)
		tenantOf[UsageUser+"\x00"+id] = tenant
		get(UsageTenant, tenant).people++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	rows, err = q.Query(ctx, `SELECT id FROM tenants`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		get(UsageTenant, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	// What the table says.
	have := map[string]*sum{}
	rows, err = q.Query(ctx, `SELECT kind, id, spaces, bytes, pages, people FROM usage`)
	if err != nil {
		return 0, err
	}
	var stale []usageDelta
	for rows.Next() {
		var kind, id string
		var s sum
		if err := rows.Scan(&kind, &id, &s.spaces, &s.bytes, &s.pages, &s.people); err != nil {
			rows.Close()
			return 0, err
		}
		have[kind+"\x00"+id] = &s
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	var ds []usageDelta
	for k, w := range want {
		h := have[k]
		if h == nil {
			h = &sum{}
		}
		if *h == *w {
			continue
		}
		kind, id, _ := strings.Cut(k, "\x00")
		tenant := id
		if kind == UsageUser {
			tenant = tenantOf[k]
		}
		ds = append(ds, usageDelta{kind: kind, id: id, tenant: tenant, spaces: w.spaces - h.spaces, bytes: w.bytes - h.bytes, pages: w.pages - h.pages, people: w.people - h.people})
	}
	// A row of a person or tenant that is gone.
	for k := range have {
		if want[k] == nil {
			kind, id, _ := strings.Cut(k, "\x00")
			stale = append(stale, usageDelta{kind: kind, id: id})
		}
	}
	if err := addUsage(ctx, q, ds...); err != nil {
		return 0, err
	}
	for _, d := range stale {
		if _, err := q.Exec(ctx, `DELETE FROM usage WHERE kind = ? AND id = ?`, d.kind, d.id); err != nil {
			return 0, err
		}
	}
	return n + len(ds) + len(stale), nil
}
