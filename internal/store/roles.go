package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Custom roles (3.6, reserved: no endpoint makes them yet). A role is a named
// list of permission keys (internal/authz) of one scope; a grant gives it to a
// person or a group, across the site, in a tenant, or in a space. The built-in
// roles are code and are not stored.
//
// Reading a person's grants costs a query, so the session cache asks for them
// only while some role exists: Store.CustomRoles is one atomic load, set when
// the store opens and when a role is inserted.

// The scopes of a role.
const (
	RoleScopeSite   = "site"
	RoleScopeTenant = "tenant"
	RoleScopeSpace  = "space"
)

// Who a grant is to.
const (
	GrantToUser  = "user"
	GrantToGroup = "group"
)

// IsAdminRole reports whether an account role is the administrator's. The
// rules about an account itself (an invitation's role, a person signing in)
// ask this; what a caller may do is asked of their keys (internal/authz).
func IsAdminRole(role string) bool { return role == "admin" }

// IsAdmin reports whether the account is an administrator (of its tenant, or
// with SiteAdmin of the server): a fact about the account, for the rules about
// it as the one acted on (an administrator is never merged by address; one
// signs in with a password where others may not).
func (u User) IsAdmin() bool { return IsAdminRole(u.Role) }

// IsSiteAdmin reports whether the account is an administrator of the whole
// server: for the rules about it as the one acted on (only those who manage
// site administrators change one; the last one is kept).
func (u User) IsSiteAdmin() bool { return IsSiteAdminRole(u.Role, u.SiteAdmin) }

// IsSiteAdminRole reports whether an account of role with the flag siteAdmin
// is an administrator of the whole server: only an administrator can be one.
func IsSiteAdminRole(role string, siteAdmin bool) bool { return IsAdminRole(role) && siteAdmin }

// Role is a custom role.
type Role struct {
	ID string
	// TenantID is the tenant the role belongs to, "" for a role of the site.
	TenantID string
	Scope    string
	Name     string
	// Keys are the names of its permission keys, as stored.
	Keys             []string
	Created, Updated time.Time
}

// RoleGrant is a role held by a person or a group.
type RoleGrant struct {
	RoleID      string
	SubjectKind string // GrantToUser or GrantToGroup
	SubjectID   string
	// TenantID is where the role is held, "" across the site; SpaceID, when
	// set, a space (the space level's, reserved).
	TenantID, SpaceID string
	Created           time.Time
}

// HeldRole is one role a person holds, directly or through a group, as the
// session cache builds their permissions from it.
type HeldRole struct {
	Scope string
	Keys  []string
	// TenantID is where it is held: the grant's tenant, or else the role's.
	TenantID string
	SpaceID  string
}

// CustomRoles reports whether any custom role may exist: one atomic load. It
// is false only while none does, so a person's grants are not looked for.
func (s *Store) CustomRoles() bool { return s.customRoles.Load() }

func (s *Store) loadCustomRoles(ctx context.Context) error {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM roles LIMIT 1`).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		s.customRoles.Store(false)
		return nil
	}
	if err != nil {
		return err
	}
	s.customRoles.Store(true)
	return nil
}

func keysJSON(keys []string) (string, error) {
	if keys == nil {
		keys = []string{}
	}
	b, err := json.Marshal(keys)
	return string(b), err
}

// InsertRole adds a role; a name taken in its tenant is a unique violation.
// CustomRoles is true from before the row is written, so no reader of a
// grant ever misses it (in a transaction that rolls back, it stays true
// until the next start, which costs only the look for grants).
func (s *Store) InsertRole(ctx context.Context, q Q, r Role) error {
	keys, err := keysJSON(r.Keys)
	if err != nil {
		return err
	}
	s.customRoles.Store(true)
	_, err = q.Exec(ctx, `INSERT INTO roles(id, tenant_id, scope, name, keys, created, updated) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.TenantID, r.Scope, r.Name, keys, ms(r.Created), ms(r.Updated))
	return err
}

// UpdateRole changes a role's name and keys.
func UpdateRole(ctx context.Context, q Q, id, name string, keys []string, now time.Time) error {
	k, err := keysJSON(keys)
	if err != nil {
		return err
	}
	res, err := q.Exec(ctx, `UPDATE roles SET name = ?, keys = ?, updated = ? WHERE id = ?`, name, k, ms(now), id)
	return oneRow(res, err)
}

// DeleteRole deletes a role and its grants.
func DeleteRole(ctx context.Context, q Q, id string) error {
	res, err := q.Exec(ctx, `DELETE FROM roles WHERE id = ?`, id)
	return oneRow(res, err)
}

const roleCols = `id, tenant_id, scope, name, keys, created, updated`

func scanRole(sc interface{ Scan(...any) error }) (*Role, error) {
	var r Role
	var keys string
	var cr, up int64
	if err := sc.Scan(&r.ID, &r.TenantID, &r.Scope, &r.Name, &keys, &cr, &up); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if err := json.Unmarshal([]byte(keys), &r.Keys); err != nil {
		return nil, err
	}
	r.Created, r.Updated = fromMS(cr), fromMS(up)
	return &r, nil
}

// GetRole returns a role, or ErrNotFound.
func GetRole(ctx context.Context, q Q, id string) (*Role, error) {
	return scanRole(q.QueryRow(ctx, `SELECT `+roleCols+` FROM roles WHERE id = ?`, id))
}

// ListRoles lists the roles of a tenant ("" for the site's) by name.
func ListRoles(ctx context.Context, q Q, tenantID string) ([]Role, error) {
	rows, err := q.Query(ctx, `SELECT `+roleCols+` FROM roles WHERE tenant_id = ? ORDER BY name, id`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Role{}
	for rows.Next() {
		r, err := scanRole(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// InsertRoleGrant gives a role; the same grant twice is a unique violation.
func InsertRoleGrant(ctx context.Context, q Q, g RoleGrant) error {
	_, err := q.Exec(ctx, `INSERT INTO role_grants(role_id, subject_kind, subject_id, tenant_id, space_id, created) VALUES (?, ?, ?, ?, ?, ?)`,
		g.RoleID, g.SubjectKind, g.SubjectID, g.TenantID, g.SpaceID, ms(g.Created))
	return err
}

// DeleteRoleGrant takes a grant back.
func DeleteRoleGrant(ctx context.Context, q Q, g RoleGrant) error {
	res, err := q.Exec(ctx, `DELETE FROM role_grants WHERE role_id = ? AND subject_kind = ? AND subject_id = ? AND tenant_id = ? AND space_id = ?`,
		g.RoleID, g.SubjectKind, g.SubjectID, g.TenantID, g.SpaceID)
	return oneRow(res, err)
}

// HeldRoles is every role a person holds: given to them, or to a group they
// are in. Each half reads role_grants by its index on the subject.
func HeldRoles(ctx context.Context, q Q, userID string) ([]HeldRole, error) {
	const cols = `r.scope, r.keys, CASE WHEN g.tenant_id <> '' THEN g.tenant_id ELSE r.tenant_id END, g.space_id`
	rows, err := q.Query(ctx, `SELECT `+cols+` FROM role_grants g JOIN roles r ON r.id = g.role_id
		WHERE g.subject_kind = 'user' AND g.subject_id = ?
		UNION ALL
		SELECT `+cols+` FROM role_grants g JOIN roles r ON r.id = g.role_id
		WHERE g.subject_kind = 'group' AND g.subject_id IN (SELECT group_id FROM group_members WHERE user_id = ?)`, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HeldRole
	for rows.Next() {
		var h HeldRole
		var keys string
		if err := rows.Scan(&h.Scope, &keys, &h.TenantID, &h.SpaceID); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(keys), &h.Keys); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
