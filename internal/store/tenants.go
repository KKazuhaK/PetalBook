package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// DefaultTenantID is the tenant made with the database (and the one rows of
// single-user mode belong to).
const DefaultTenantID = "0000000000"

// DefaultTenantSlug is the slug of the default tenant.
const DefaultTenantSlug = "default"

// Scope is whose rows a query may see: one tenant's, or, for a site
// administrator or a background job, every tenant's. Every method that reads
// or lists users, spaces, groups, invites or audit rows by anything a client
// sends takes one, so a method that forgets it does not compile; the few that
// look across tenants on purpose are on the allowlist of scope_test.go, each
// with its reason. The zero Scope sees nothing, so a forgotten value fails
// closed.
type Scope struct {
	tenant string
	all    bool
	// noPersonal leaves personal spaces out of what the scope sees of spaces and
	// of the recycle bin (AdminScopeOf: a tenant administrator's).
	noPersonal bool
}

// InTenant is the scope of one tenant.
func InTenant(id string) Scope { return Scope{tenant: id} }

// AllTenants is the scope of every tenant: the server's own.
func AllTenants() Scope { return Scope{all: true} }

// All reports whether the scope covers every tenant.
func (sc Scope) All() bool { return sc.all }

// Tenant is the tenant of a one-tenant scope ("" for AllTenants).
func (sc Scope) Tenant() string { return sc.tenant }

// Narrow is sc, cut to tenant id when id is not empty and sc allows it: a
// site administrator's ?tenant= filter. A one-tenant scope stays as it is.
func (sc Scope) Narrow(id string) Scope {
	if sc.all && id != "" {
		return Scope{tenant: id, noPersonal: sc.noPersonal}
	}
	return sc
}

// cond is the condition on the column col, with its argument.
func (sc Scope) cond(col string) (string, []any) {
	switch {
	case sc.all:
		return "1 = 1", nil
	case sc.tenant == "":
		return "1 = 0", nil
	}
	return col + " = ?", []any{sc.tenant}
}

// ofUser is the condition that the user in column col is in the scope, for the
// tables that hang under a user (tokens, grants).
func (sc Scope) ofUser(col string) (string, []any) {
	switch {
	case sc.all:
		return "1 = 1", nil
	case sc.tenant == "":
		return "1 = 0", nil
	}
	// A probe of each row's user by its key: no index of users by tenant is
	// needed, and a page of a list stops after its rows.
	return "EXISTS (SELECT 1 FROM users ou WHERE ou.id = " + col + " AND ou.tenant_id = ?)", []any{sc.tenant}
}

// AdminScopeOf is ScopeOf for what an administrator reaches of spaces through
// the administration: a tenant administrator's scope leaves personal spaces
// out, live or in the recycle bin, so that every query of a space by id or by
// list answers for a personal one as for a space that does not exist
// (docs/api.md, "Viewing a space": a personal space is never reachable by a
// tenant administrator). Deleting a person, which takes their personal space
// with them, uses ScopeOf. Restoring one uses this scope too: the person's item
// is not a personal space, and their personal space comes back with them by
// the item it was recycled with, not through the scope.
func AdminScopeOf(u User) Scope {
	sc := ScopeOf(u)
	if !sc.all {
		sc.noPersonal = true
	}
	return sc
}

// spaceCond is cond on a row of a space (tenantCol), with the condition that it
// is not a personal one (notPersonal) for a scope that leaves those out.
func (sc Scope) spaceCond(tenantCol, notPersonal string) (string, []any) {
	cond, args := sc.cond(tenantCol)
	if sc.noPersonal {
		cond += " AND " + notPersonal
	}
	return cond, args
}

// ScopeOf is the scope an administrator acts in: every tenant for a site
// administrator, their own tenant for any other user.
func ScopeOf(u User) Scope {
	if u.IsSiteAdmin() {
		return AllTenants()
	}
	return InTenant(u.TenantID)
}

// Tenant is a tenant of the server.
type Tenant struct {
	ID       string
	Slug     string
	Name     string
	Disabled bool
	Created  time.Time
	Updated  time.Time
}

// IsDefault reports whether this is the default tenant.
func (t Tenant) IsDefault() bool { return t.ID == DefaultTenantID }

// MaxTenants is how many tenants a server may have.
const MaxTenants = 1000

// ErrTenantNotEmpty is the refusal of deleting a tenant that still has rows.
type ErrTenantNotEmpty struct{ Users, Spaces, Groups, Invites, Recycle int }

func (*ErrTenantNotEmpty) Error() string { return "the tenant is not empty" }

// ErrTenantLimit is the refusal of the 1,001st tenant.
var ErrTenantLimit = errors.New("too many tenants")

const tenantCols = `id, slug, name, disabled, created, updated`

func scanTenant(sc interface{ Scan(...any) error }) (*Tenant, error) {
	var t Tenant
	var disabled, created, updated int64
	if err := sc.Scan(&t.ID, &t.Slug, &t.Name, &disabled, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	t.Disabled, t.Created, t.Updated = disabled != 0, fromMS(created), fromMS(updated)
	return &t, nil
}

// GetTenant returns a tenant by id, or ErrNotFound. A tenants row is not a
// tenant's data: callers decide who may see which tenant.
func GetTenant(ctx context.Context, q Q, id string) (*Tenant, error) {
	return scanTenant(q.QueryRow(ctx, `SELECT `+tenantCols+` FROM tenants WHERE id = ?`, id))
}

// GetTenantBySlug returns a tenant by slug, or ErrNotFound.
func GetTenantBySlug(ctx context.Context, q Q, slug string) (*Tenant, error) {
	return scanTenant(q.QueryRow(ctx, `SELECT `+tenantCols+` FROM tenants WHERE slug = ?`, slug))
}

// CountTenants counts the tenants.
func CountTenants(ctx context.Context, q Q) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(*) FROM tenants`).Scan(&n)
	return
}

// ListTenants returns one page: the default tenant first, then by name (bytewise
// on the folded name) and id. The cursor is the position "after" the last item,
// given as the id of that tenant; an unknown id starts at the beginning. One
// query per page.
func ListTenants(ctx context.Context, q Q, d Dialect, query, after string, limit int) (out []Tenant, next string, err error) {
	if limit <= 0 {
		limit = 50
	}
	var where []string
	var args []any
	if query != "" {
		pat := "%" + likeEscape(FoldName(query)) + "%"
		where = append(where, `(name_key LIKE ? ESCAPE '\' OR slug LIKE ? ESCAPE '\')`)
		args = append(args, pat, pat)
	}
	if after != "" {
		// The sort key is (default first, name key, id); it is the same row's
		// key, looked up in the same statement.
		where = append(where, `(CASE WHEN id = '`+DefaultTenantID+`' THEN 0 ELSE 1 END, name_key, id) > (SELECT CASE WHEN a.id = '`+DefaultTenantID+`' THEN 0 ELSE 1 END, a.name_key, a.id FROM tenants a WHERE a.id = ?)`)
		args = append(args, after)
	}
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, limit+1)
	rows, err := q.Query(ctx, `SELECT `+tenantCols+` FROM tenants`+cond+` ORDER BY CASE WHEN id = '`+DefaultTenantID+`' THEN 0 ELSE 1 END, name_key, id LIMIT ?`, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	for rows.Next() {
		t, err := scanTenant(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(out) > limit {
		out = out[:limit]
		next = out[len(out)-1].ID
	}
	return out, next, nil
}

// TenantCounts is what a tenant holds.
type TenantCounts struct {
	Users, Spaces, Groups, Invites int

	// Recycle is the items of the recycle bin held for the tenant: it cannot be
	// deleted while one is there, so a restore always finds its tenant.
	Recycle int
}

// CountTenantRows counts the users, spaces, groups and invites of a tenant.
func CountTenantRows(ctx context.Context, q Q, id string) (c TenantCounts, err error) {
	err = q.QueryRow(ctx, `SELECT
		(SELECT COUNT(*) FROM users WHERE tenant_id = ? AND deleted_at IS NULL),
		(SELECT COUNT(*) FROM spaces WHERE tenant_id = ?),
		(SELECT COUNT(*) FROM groups WHERE tenant_id = ?),
		(SELECT COUNT(*) FROM invites WHERE tenant_id = ?),
		(SELECT COUNT(*) FROM recycle_items WHERE tenant_id = ?)`, id, id, id, id, id).Scan(&c.Users, &c.Spaces, &c.Groups, &c.Invites, &c.Recycle)
	return
}

// TenantRef is a tenant as another object names it.
type TenantRef struct{ ID, Slug, Name string }

// tenantState is what the request path needs of the tenants: which are
// disabled, and how each is named. It is one small map (at most MaxTenants
// entries), replaced whole when a tenant changes.
type tenantState struct {
	refs     map[string]TenantRef
	bySlug   map[string]string
	disabled map[string]bool
}

func (s *Store) loadTenantState(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, slug, name, disabled FROM tenants`)
	if err != nil {
		return err
	}
	defer rows.Close()
	st := &tenantState{refs: map[string]TenantRef{}, bySlug: map[string]string{}, disabled: map[string]bool{}}
	for rows.Next() {
		var r TenantRef
		var dis int64
		if err := rows.Scan(&r.ID, &r.Slug, &r.Name, &dis); err != nil {
			return err
		}
		st.refs[r.ID], st.bySlug[r.Slug] = r, r.ID
		if dis != 0 {
			st.disabled[r.ID] = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.tenantState.Store(st)
	return nil
}

// TenantDisabled reports whether a tenant is disabled: one atomic load and,
// when some tenant is disabled, one lookup in a map of those. This is the whole
// cost of tenants on a request.
func (s *Store) TenantDisabled(id string) bool {
	st := s.tenantState.Load()
	if st == nil || len(st.disabled) == 0 {
		return false
	}
	return st.disabled[id]
}

// TenantRefOf names a tenant from memory; ok is false for one that does not exist.
func (s *Store) TenantRefOf(id string) (TenantRef, bool) {
	st := s.tenantState.Load()
	if st == nil {
		return TenantRef{}, false
	}
	r, ok := st.refs[id]
	return r, ok
}

// TenantIDBySlug finds a tenant's id by its slug, from memory.
func (s *Store) TenantIDBySlug(slug string) (string, bool) {
	st := s.tenantState.Load()
	if st == nil {
		return "", false
	}
	id, ok := st.bySlug[slug]
	return id, ok
}

// TenantIDs lists every tenant's id, from memory.
func (s *Store) TenantIDs() []string {
	st := s.tenantState.Load()
	if st == nil {
		return nil
	}
	out := make([]string, 0, len(st.refs))
	for id := range st.refs {
		out = append(out, id)
	}
	return out
}

// TenantCount is how many tenants there are, from memory.
func (s *Store) TenantCount() int {
	if st := s.tenantState.Load(); st != nil {
		return len(st.refs)
	}
	return 0
}

// TenantStateReload re-reads which tenants are disabled; the methods that
// change a tenant call it, and a second server process would call it on a timer.
func (s *Store) TenantStateReload(ctx context.Context) error { return s.loadTenantState(ctx) }

// CreateTenant adds a tenant, refusing the 1,001st (ErrTenantLimit). A taken
// slug is a unique violation.
func (s *Store) CreateTenant(ctx context.Context, t Tenant) error {
	err := s.Tx(ctx, func(q Q) error {
		n, err := CountTenants(ctx, q)
		if err != nil {
			return err
		}
		if n >= MaxTenants {
			return ErrTenantLimit
		}
		_, err = q.Exec(ctx, `INSERT INTO tenants(`+tenantCols+`, name_key) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			t.ID, t.Slug, t.Name, b2i(t.Disabled), ms(t.Created), ms(t.Updated), FoldName(t.Name))
		return err
	})
	if err != nil {
		return err
	}
	return s.loadTenantState(ctx)
}

// UpdateTenant writes a tenant's name, slug, and disabled state (the totals are the limits sheet's).
func (s *Store) UpdateTenant(ctx context.Context, t Tenant) error {
	res, err := s.Q().Exec(ctx, `UPDATE tenants SET slug = ?, name = ?, name_key = ?, disabled = ?, updated = ? WHERE id = ?`,
		t.Slug, t.Name, FoldName(t.Name), b2i(t.Disabled), ms(t.Updated), t.ID)
	if err := oneRow(res, err); err != nil {
		return err
	}
	return s.loadTenantState(ctx)
}

// DeleteTenant deletes an empty tenant (its settings go with it), or fails with
// *ErrTenantNotEmpty: one with users, spaces, groups, invites or items in the
// recycle bin. The counts and the delete are one transaction.
//
// What is left of the tenant that the tenants foreign keys would otherwise
// refuse: pending registrations, its share of the GitHub App's installations
// and its usage row are deleted; a sign-in provider that put new accounts here
// puts them in the default tenant; the tombstones of purged people (kept only
// so the audit log can name them), the exports and the bulk jobs move to the
// default tenant, the exports and jobs as site administrators' own (site_only),
// where they expire and are pruned as any other. The audit log keeps its rows:
// its tenant_id is not a foreign key.
func (s *Store) DeleteTenant(ctx context.Context, id string) error {
	err := s.Tx(ctx, func(q Q) error {
		c, err := CountTenantRows(ctx, q, id)
		if err != nil {
			return err
		}
		if c.Users+c.Spaces+c.Groups+c.Invites+c.Recycle > 0 {
			return &ErrTenantNotEmpty{c.Users, c.Spaces, c.Groups, c.Invites, c.Recycle}
		}
		if _, err := q.Exec(ctx, `DELETE FROM layered_settings WHERE layer = 'tenant' AND layer_id = ?`, id); err != nil {
			return err
		}
		// Installations shared with it are not any more.
		if _, err := q.Exec(ctx, `DELETE FROM github_installation_tenants WHERE tenant_id = ?`, id); err != nil {
			return err
		}
		// Its custom roles, and the grants of any role held in it, go with it.
		if _, err := q.Exec(ctx, `DELETE FROM role_grants WHERE tenant_id = ?`, id); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `DELETE FROM roles WHERE tenant_id = ?`, id); err != nil {
			return err
		}
		// Pending registrations are deleted with it.
		if _, err := q.Exec(ctx, `DELETE FROM registrations WHERE tenant_id = ?`, id); err != nil {
			return err
		}
		// A provider that put new accounts here goes back to the default tenant.
		if _, err := q.Exec(ctx, `UPDATE sign_in_providers SET tenant_id = ? WHERE tenant_id = ?`, DefaultTenantID, id); err != nil {
			return err
		}
		for _, st := range []string{
			`UPDATE users SET tenant_id = '` + DefaultTenantID + `' WHERE tenant_id = ? AND deleted_at IS NOT NULL`,
			`UPDATE exports SET tenant_id = '` + DefaultTenantID + `', site_only = 1 WHERE tenant_id = ?`,
			`UPDATE bulk_jobs SET tenant_id = '` + DefaultTenantID + `', site_only = 1 WHERE tenant_id = ?`,
			`DELETE FROM usage WHERE tenant_id = ?`,
		} {
			if _, err := q.Exec(ctx, st, id); err != nil {
				return err
			}
		}
		res, err := q.Exec(ctx, `DELETE FROM tenants WHERE id = ?`, id)
		return oneRow(res, err)
	})
	if err != nil {
		return err
	}
	return s.loadTenantState(ctx)
}

func tenantOrDefault(id string) string {
	if id == "" {
		return DefaultTenantID
	}
	return id
}
