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

	// What 3.5 part 4 adds (docs/api.md, "Measurements this adds").
	held, remote, partial, evicted, archived, pinned int64
	connected, dormant                               int64
	// created is the person's creation time, written when their row is made.
	created int64
	// refresh asks for the person's dormant flag to be worked out again once the
	// deltas are in (it is also done for any delta that moves remote or connected).
	refresh bool
}

func (d usageDelta) key() string { return d.kind + "\x00" + d.id }

// merge adds the counters of o to d (the same row).
func (d *usageDelta) merge(o usageDelta) {
	d.spaces, d.bytes, d.pages, d.people = d.spaces+o.spaces, d.bytes+o.bytes, d.pages+o.pages, d.people+o.people
	d.held, d.remote, d.partial, d.evicted = d.held+o.held, d.remote+o.remote, d.partial+o.partial, d.evicted+o.evicted
	d.archived, d.pinned, d.connected = d.archived+o.archived, d.pinned+o.pinned, d.connected+o.connected
	d.dormant += o.dormant
	if o.measured > 0 {
		d.measured = o.measured
	}
	d.refresh = d.refresh || o.refresh
}

// addUsage applies deltas, the rows in a fixed order. A row that does not exist
// yet is made. A person whose remote spaces or connection changed has their
// dormant flag worked out again afterwards, and the tenant's count with it (the
// tenant's row is taken with the others, so the order of locks stays one).
func addUsage(ctx context.Context, q Q, ds ...usageDelta) error {
	byKey := map[string]*usageDelta{}
	var list []usageDelta
	for _, d := range ds {
		if p := byKey[d.key()]; p != nil {
			p.merge(d)
			continue
		}
		d := d
		byKey[d.key()] = &d
	}
	for _, p := range byKey {
		list = append(list, *p)
	}
	for _, d := range list {
		if d.kind == UsageUser && (d.remote != 0 || d.connected != 0 || d.refresh) {
			t := usageDelta{kind: UsageTenant, id: d.tenant, tenant: d.tenant}
			if byKey[t.key()] == nil {
				byKey[t.key()] = &t
				list = append(list, t)
			}
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].key() < list[j].key() })
	for _, d := range list {
		if _, err := q.Exec(ctx, `INSERT INTO usage(kind, id, tenant_id, spaces, bytes, pages, people, measured, held_bytes, remote_spaces, partial_spaces, evicted_spaces, archived_spaces, pinned_spaces, connected, dormant, created)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (kind, id) DO UPDATE SET spaces = usage.spaces + excluded.spaces, bytes = usage.bytes + excluded.bytes,
				pages = usage.pages + excluded.pages, people = usage.people + excluded.people,
				held_bytes = usage.held_bytes + excluded.held_bytes, remote_spaces = usage.remote_spaces + excluded.remote_spaces,
				partial_spaces = usage.partial_spaces + excluded.partial_spaces, evicted_spaces = usage.evicted_spaces + excluded.evicted_spaces,
				archived_spaces = usage.archived_spaces + excluded.archived_spaces, pinned_spaces = usage.pinned_spaces + excluded.pinned_spaces,
				connected = usage.connected + excluded.connected, dormant = usage.dormant + excluded.dormant,
				measured = CASE WHEN excluded.measured > 0 THEN excluded.measured ELSE usage.measured END`,
			d.kind, d.id, d.tenant, d.spaces, d.bytes, d.pages, d.people, d.measured, d.held, d.remote, d.partial, d.evicted, d.archived, d.pinned, d.connected, d.dormant, d.created); err != nil {
			return err
		}
	}
	for _, d := range list {
		if d.kind == UsageUser && (d.remote != 0 || d.connected != 0 || d.refresh) {
			if err := refreshDormant(ctx, q, d.id, d.tenant); err != nil {
				return err
			}
		}
	}
	return nil
}

// dormantNow is the SQL of whether the person whose usage row is u is dormant:
// registered, never changed anything, follows no remote, has no connection.
const dormantNow = `(CASE WHEN u.remote_spaces = 0 AND u.connected = 0 AND NOT EXISTS (SELECT 1 FROM users p WHERE p.id = u.id AND p.first_write IS NOT NULL) THEN 1 ELSE 0 END)`

// refreshDormant works out a person's dormant flag again, and moves the
// tenant's count by the difference. The tenant's row is already locked by the
// caller (addUsage).
func refreshDormant(ctx context.Context, q Q, userID, tenant string) error {
	var old, now int64
	err := q.QueryRow(ctx, `SELECT u.dormant, `+dormantNow+` FROM usage u WHERE u.kind = 'user' AND u.id = ?`, userID).Scan(&old, &now)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && old == now) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `UPDATE usage SET dormant = ? WHERE kind = 'user' AND id = ?`, now, userID); err != nil {
		return err
	}
	_, err = q.Exec(ctx, `UPDATE usage SET dormant = dormant + ? WHERE kind = 'tenant' AND id = ?`, now-old, tenant)
	return err
}

// usageOfNewUser is what InsertUser adds: the person's own row (dormant until
// they do something), and one more person in the tenant.
func usageOfNewUser(ctx context.Context, q Q, id, tenant string, created time.Time) error {
	return addUsage(ctx, q,
		usageDelta{kind: UsageUser, id: id, tenant: tenant, created: ms(created), refresh: true},
		usageDelta{kind: UsageTenant, id: tenant, tenant: tenant, people: 1})
}

// usageOfNewSpace is what InsertSpace adds: one more space of the tenant. The
// person it is counted for comes with its first owner (settleOwner).
func usageOfNewSpace(ctx context.Context, q Q, tenant string) error {
	return addUsage(ctx, q, usageDelta{kind: UsageTenant, id: tenant, tenant: tenant, spaces: 1})
}

// spaceUsageRow is what a space contributes to the usage rows it is counted in:
// a tenant, and the person it is counted for. Every field but the two names is
// a number a row adds up.
type spaceUsageRow struct {
	tenant, user  string
	bytes, pages  int64
	held          int64
	remote        int64 // 1: it follows a remote
	partial       int64 // 1: a remote space whose clone is partial
	evicted       int64
	archived      int64
	pinned        int64
	location      string
	firstOwner    string
	hasFirstOwner bool
}

// heldSQL is the SQL of what the server holds for a space (3.1): the clone
// (nothing once it is evicted or archived) and the index (nothing once it is
// archived), from the stored measurements. col adds a table alias to the
// columns.
func heldSQL(alias string) string {
	cs, sz, ix := alias+"cache_state", alias+"size_bytes", alias+"index_bytes"
	return `(CASE WHEN ` + cs + ` IN ('evicted', 'archived') THEN 0 ELSE COALESCE(` + sz + `, 0) END + CASE WHEN ` + cs + ` = 'archived' THEN 0 ELSE ` + ix + ` END)`
}

// flagSQL is the SQL of a 0/1 number a usage row counts, for a space.
func flagSQL(alias, what string) string {
	switch what {
	case "remote":
		return `(CASE WHEN ` + alias + `remote_via <> '' THEN 1 ELSE 0 END)`
	case "partial", "evicted", "archived":
		return `(CASE WHEN ` + alias + `remote_via <> '' AND ` + alias + `cache_state = '` + what + `' THEN 1 ELSE 0 END)`
	case "pinned":
		return `(CASE WHEN ` + alias + `pinned <> 0 THEN 1 ELSE 0 END)`
	}
	panic("flagSQL: " + what)
}

func readSpaceUsage(ctx context.Context, q Q, id string) (spaceUsageRow, error) {
	var r spaceUsageRow
	err := q.QueryRow(ctx, `SELECT tenant_id, usage_user, usage_bytes, page_count, `+heldSQL("")+`, `+flagSQL("", "remote")+`, `+flagSQL("", "partial")+`,
		`+flagSQL("", "evicted")+`, `+flagSQL("", "archived")+`, `+flagSQL("", "pinned")+`, location FROM spaces WHERE id = ?`, id).
		Scan(&r.tenant, &r.user, &r.bytes, &r.pages, &r.held, &r.remote, &r.partial, &r.evicted, &r.archived, &r.pinned, &r.location)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// delta is the space's contribution to the row of kind and id, times sign (1
// adds the space, -1 takes it out).
func (r spaceUsageRow) delta(kind, id string, sign int64) usageDelta {
	return usageDelta{kind: kind, id: id, tenant: r.tenant, spaces: sign, bytes: sign * r.bytes, pages: sign * r.pages,
		held: sign * r.held, remote: sign * r.remote, partial: sign * r.partial, evicted: sign * r.evicted, archived: sign * r.archived, pinned: sign * r.pinned}
}

// deltas are the contribution to the tenant and to the person the space is
// counted for.
func (r spaceUsageRow) deltas(sign int64) []usageDelta {
	ds := []usageDelta{r.delta(UsageTenant, r.tenant, sign)}
	if r.user != "" {
		ds = append(ds, r.delta(UsageUser, r.user, sign))
	}
	return ds
}

// changed is the difference from before to r, for the rows the space is counted
// in (the number of spaces does not change).
func (r spaceUsageRow) changed(before spaceUsageRow) []usageDelta {
	var ds []usageDelta
	for _, d := range r.deltas(1) {
		o := before.delta(d.kind, d.id, 1)
		d.spaces = 0
		d.bytes, d.pages, d.held, d.remote = d.bytes-o.bytes, d.pages-o.pages, d.held-o.held, d.remote-o.remote
		d.partial, d.evicted, d.archived, d.pinned = d.partial-o.partial, d.evicted-o.evicted, d.archived-o.archived, d.pinned-o.pinned
		if d.bytes|d.pages|d.held|d.remote|d.partial|d.evicted|d.archived|d.pinned != 0 {
			ds = append(ds, d)
		}
	}
	return ds
}

// withSpace runs a change of a space in one transaction, takes the space's lock
// first, and puts the rows the space is counted in right by the difference it
// made: its bytes, what the server holds for it, whether it follows a remote,
// is partial, evicted, archived or pinned. The derived columns that follow the
// remote (where it lives, whether it counts) are settled after the change.
func withSpace(ctx context.Context, q Q, spaceID string, change func(Q) error) error {
	return atomically(ctx, q, func(q Q) error {
		if err := LockSpace(ctx, q, spaceID); err != nil {
			return err
		}
		before, err := readSpaceUsage(ctx, q, spaceID)
		if err != nil {
			return err
		}
		if err := change(q); err != nil {
			return err
		}
		if err := settleDerived(ctx, q, spaceID); err != nil {
			return err
		}
		after, err := readSpaceUsage(ctx, q, spaceID)
		if err != nil {
			return err
		}
		return addUsage(ctx, q, after.changed(before)...)
	})
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
		ds = append(ds, r.delta(UsageUser, r.user, -1))
	}
	if first != "" {
		ds = append(ds, r.delta(UsageUser, first, 1))
	}
	return addUsage(ctx, q, ds...)
}

// SpaceCountedFor is whom a space's usage is counted for: its first owner (""
// when none) and its tenant. Code that changes who owns a space reads it before
// and after, to tell the quotas whose rows the change reached.
func SpaceCountedFor(ctx context.Context, q Q, spaceID string) (Touched, error) {
	r, err := readSpaceUsage(ctx, q, spaceID)
	return Touched{UserID: r.user, TenantID: r.tenant}, err
}

// forgetSpaceUsage takes a space that is about to be deleted out of the rows it
// is counted in.
func forgetSpaceUsage(ctx context.Context, q Q, spaceID string) error {
	r, err := readSpaceUsage(ctx, q, spaceID)
	if err != nil {
		return err
	}
	return addUsage(ctx, q, r.deltas(-1)...)
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
// The bytes the quotas count are the measurement only where the space lives on
// the server (LocationCounts): a space whose remote belongs to the person or the
// tenant is measured and shown, and counts nothing, and what the server holds of
// it (the clone, the index) is the pressure controller's business.
func (s *Store) RecordMeasurement(ctx context.Context, spaceID string, m Measurement) (t Touched, err error) {
	err = s.Tx(ctx, func(q Q) error {
		if err := LockSpace(ctx, q, spaceID); err != nil {
			return err
		}
		before, err := readSpaceUsage(ctx, q, spaceID)
		if err != nil {
			return err
		}
		pages := before.pages
		if m.HavePages {
			pages = m.Pages
		}
		counted := int64(0)
		if LocationCounts(before.location) {
			counted = m.Bytes
		}
		if _, err := q.Exec(ctx, `UPDATE spaces SET size_bytes = ?, size_at = ?, usage_bytes = ?, page_count = ? WHERE id = ?`,
			m.Bytes, ms(m.At), counted, pages, spaceID); err != nil {
			return err
		}
		after, err := readSpaceUsage(ctx, q, spaceID)
		if err != nil {
			return err
		}
		ds := after.changed(before)
		// The row is stamped even when nothing moved.
		seen := map[string]bool{}
		for i := range ds {
			ds[i].measured = ms(m.At)
			seen[ds[i].key()] = true
		}
		for _, d := range after.deltas(1) {
			if !seen[d.key()] {
				ds = append(ds, usageDelta{kind: d.kind, id: d.id, tenant: d.tenant, measured: ms(m.At)})
			}
		}
		t = Touched{UserID: after.user, TenantID: after.tenant}
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
		JOIN users u ON u.id = p.id AND u.deleted_at IS NULL
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

// ErrBadCursor is a cursor that is not one a list gave (or was made for another
// sort), and ErrBadFilter a sort, order or filter value the list does not know.
var (
	ErrBadCursor = errors.New("bad cursor")
	ErrBadFilter = errors.New("bad filter")
)

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
		if moved, err = settleSpaceFacts(ctx, q); err != nil {
			return err
		}
		m, err := settleUsageUsers(ctx, q)
		moved += m
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

// sums is what one usage row adds up: the columns the recount compares.
type sums struct {
	spaces, bytes, pages, people                     int64
	held, remote, partial, evicted, archived, pinned int64
	connected, dormant                               int64
	created, lastActive                              int64 // a person's own, not summed
}

func (a sums) delta(kind, id, tenant string, from sums) usageDelta {
	return usageDelta{kind: kind, id: id, tenant: tenant, spaces: a.spaces - from.spaces, bytes: a.bytes - from.bytes, pages: a.pages - from.pages,
		people: a.people - from.people, held: a.held - from.held, remote: a.remote - from.remote, partial: a.partial - from.partial,
		evicted: a.evicted - from.evicted, archived: a.archived - from.archived, pinned: a.pinned - from.pinned,
		connected: a.connected - from.connected, dormant: a.dormant - from.dormant, created: a.created}
}

// spaceSums is the SQL that adds up what spaces contribute to a usage row, in
// the order scanSpaceSums reads it.
const spaceSums = `COUNT(*), COALESCE(SUM(usage_bytes), 0), COALESCE(SUM(page_count), 0), COALESCE(SUM(` + "%HELD%" + `), 0),
	COALESCE(SUM(%remote%), 0), COALESCE(SUM(%partial%), 0), COALESCE(SUM(%evicted%), 0), COALESCE(SUM(%archived%), 0), COALESCE(SUM(%pinned%), 0)`

func spaceSumsSQL() string {
	r := strings.NewReplacer("%HELD%", heldSQL(""), "%remote%", flagSQL("", "remote"), "%partial%", flagSQL("", "partial"),
		"%evicted%", flagSQL("", "evicted"), "%archived%", flagSQL("", "archived"), "%pinned%", flagSQL("", "pinned"))
	return r.Replace(spaceSums)
}

func recountUsage(ctx context.Context, q Q) (int, error) {
	n := 0
	want := map[string]*sums{} // by usageDelta key
	tenantOf := map[string]string{}
	get := func(kind, id string) *sums {
		k := kind + "\x00" + id
		if want[k] == nil {
			want[k] = &sums{}
		}
		return want[k]
	}
	scan := func(rows *sql.Rows, s *sums, extra ...any) error {
		return rows.Scan(append(extra, &s.spaces, &s.bytes, &s.pages, &s.held, &s.remote, &s.partial, &s.evicted, &s.archived, &s.pinned)...)
	}
	rows, err := q.Query(ctx, `SELECT usage_user, tenant_id, `+spaceSumsSQL()+` FROM spaces WHERE usage_user <> '' GROUP BY usage_user, tenant_id`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var id, tenant string
		var s sums
		if err := scan(rows, &s, &id, &tenant); err != nil {
			rows.Close()
			return 0, err
		}
		*get(UsageUser, id) = s
		tenantOf[UsageUser+"\x00"+id] = tenant
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	rows, err = q.Query(ctx, `SELECT tenant_id, `+spaceSumsSQL()+` FROM spaces GROUP BY tenant_id`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var id string
		var s sums
		if err := scan(rows, &s, &id); err != nil {
			rows.Close()
			return 0, err
		}
		*get(UsageTenant, id) = s
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	// Every person and every tenant has a row, with the people counted, and
	// what a person is (connected, dormant) worked out again.
	rows, err = q.Query(ctx, `SELECT tenant_id, id, CASE WHEN deleted_at IS NULL THEN 0 ELSE 1 END, created, COALESCE(last_active, 0),
		CASE WHEN first_write IS NULL THEN 0 ELSE 1 END, CASE WHEN `+hasConnectionSQL("users.id")+` THEN 1 ELSE 0 END FROM users`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var tenant, id string
		var gone, written, connected int64
		var created, active int64
		if err := rows.Scan(&tenant, &id, &gone, &created, &active, &written, &connected); err != nil {
			rows.Close()
			return 0, err
		}
		u := get(UsageUser, id)
		u.created, u.lastActive, u.connected = created, active, connected
		if written == 0 && u.remote == 0 && connected == 0 {
			u.dormant = 1
		}
		tenantOf[UsageUser+"\x00"+id] = tenant
		if gone == 0 {
			t := get(UsageTenant, tenant)
			t.people++ // a deleted person is not one of the tenant's people
			t.connected += u.connected
			t.dormant += u.dormant
		}
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
	have := map[string]*sums{}
	rows, err = q.Query(ctx, `SELECT kind, id, spaces, bytes, pages, people, held_bytes, remote_spaces, partial_spaces, evicted_spaces, archived_spaces, pinned_spaces, connected, dormant, created, last_active FROM usage`)
	if err != nil {
		return 0, err
	}
	var stale []usageDelta
	for rows.Next() {
		var kind, id string
		var s sums
		if err := rows.Scan(&kind, &id, &s.spaces, &s.bytes, &s.pages, &s.people, &s.held, &s.remote, &s.partial, &s.evicted, &s.archived, &s.pinned, &s.connected, &s.dormant, &s.created, &s.lastActive); err != nil {
			rows.Close()
			return 0, err
		}
		have[kind+"\x00"+id] = &s
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	type own struct {
		id               string
		created, last_ac int64
	}
	var ds []usageDelta
	var owns []own
	for k, w := range want {
		h := have[k]
		if h == nil {
			h = &sums{}
		}
		if *h == *w {
			continue
		}
		kind, id, _ := strings.Cut(k, "\x00")
		tenant := id
		if kind == UsageUser {
			tenant = tenantOf[k]
		}
		ds = append(ds, w.delta(kind, id, tenant, *h))
		if kind == UsageUser && (h.created != w.created || h.lastActive != w.lastActive) {
			owns = append(owns, own{id, w.created, w.lastActive})
		}
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
	for _, o := range owns {
		if _, err := q.Exec(ctx, `UPDATE usage SET created = ?, last_active = ? WHERE kind = 'user' AND id = ?`, o.created, o.last_ac, o.id); err != nil {
			return 0, err
		}
	}
	for _, d := range stale {
		if _, err := q.Exec(ctx, `DELETE FROM usage WHERE kind = ? AND id = ?`, d.kind, d.id); err != nil {
			return 0, err
		}
	}
	return n + len(ds) + len(stale), nil
}
