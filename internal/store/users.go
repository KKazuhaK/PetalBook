package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// User is an account. The built-in user of single-user mode has the id "local".
type User struct {
	ID       string
	Username string
	Name     string
	Email    string
	Role     string // "admin" or "member"
	Disabled bool
	Created  time.Time
	// RemoteExempt: the storage policy does not ask the spaces this user owns
	// for a remote (milestone 3.1).
	RemoteExempt bool
	// EmailVerified: the address came from a provider that vouched for it, or an
	// administrator set or confirmed it (see EmailVerifiedBy for whether it
	// counts for merging a sign-in).
	EmailVerified bool
	// PasskeyRequired: this person must sign in with a passkey (set for them
	// alone; a requirement of their role is in the security settings).
	PasskeyRequired bool
	// TenantID is the tenant the user belongs to (DefaultTenantID when empty
	// at insert); SiteAdmin makes an administrator one of the whole server.
	TenantID  string
	SiteAdmin bool
}

// LocalUserID is the built-in user of single-user mode.
const LocalUserID = "local"

const userCols = `id, username, name, email, role, disabled, created, email_verified, passkey_required, tenant_id, site_admin, remote_exempt`

func scanUser(sc interface{ Scan(...any) error }) (*User, error) {
	var u User
	var disabled, created, verified, required, siteAdmin, exempt int64
	if err := sc.Scan(&u.ID, &u.Username, &u.Name, &u.Email, &u.Role, &disabled, &created, &verified, &required, &u.TenantID, &siteAdmin, &exempt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	u.Disabled, u.Created, u.EmailVerified, u.PasskeyRequired = disabled != 0, fromMS(created), verified != 0, required != 0
	u.SiteAdmin, u.RemoteExempt = siteAdmin != 0, exempt != 0
	return &u, nil
}

// GetUser returns the user with this id, or ErrNotFound; a user of a tenant
// outside the scope is not found.
func GetUser(ctx context.Context, q Q, sc Scope, id string) (*User, error) {
	cond, args := sc.cond("tenant_id")
	return scanUser(q.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id = ? AND deleted_at IS NULL AND `+cond, append([]any{id}, args...)...))
}

// GetUserAny is GetUser for a person who may be in the recycle bin or a
// tombstone: the recycle bin and the audit log are the only places that name one.
func GetUserAny(ctx context.Context, q Q, id string) (*User, error) {
	return scanUser(q.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// GetUserByUsername finds a user by the stored (lower case) username. A deleted
// person is not found (they cannot sign in, nor be added anywhere); their name
// stays taken, which the unique key on it decides.
func GetUserByUsername(ctx context.Context, q Q, username string) (*User, error) {
	return scanUser(q.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE username = ? AND deleted_at IS NULL`, username))
}

// HasUsers reports whether any account exists.
func HasUsers(ctx context.Context, q Q) (bool, error) {
	var one int
	err := q.QueryRow(ctx, `SELECT 1 FROM users LIMIT 1`).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// ListUsers returns the users in the scope, oldest first.
func ListUsers(ctx context.Context, q Q, sc Scope) ([]User, error) {
	cond, args := sc.cond("tenant_id")
	rows, err := q.Query(ctx, `SELECT `+userCols+` FROM users WHERE deleted_at IS NULL AND `+cond+` ORDER BY created, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// SearchUsers returns the users whose username or name contains query, ignoring
// case (both folded as FoldName folds, the name in users.name_key), oldest first; an empty query matches everyone. enabledOnly leaves out the
// disabled accounts before the limit counts. limit <= 0 means no limit.
func SearchUsers(ctx context.Context, q Q, d Dialect, sc Scope, query string, enabledOnly bool, limit int) ([]User, error) {
	conds := []string{`deleted_at IS NULL`}
	args := []any{}
	sCond, sArgs := sc.cond("tenant_id")
	conds, args = append(conds, sCond), append(args, sArgs...)
	if query != "" {
		c, a := personMatches("", query)
		conds, args = append(conds, c), append(args, a...)
	}
	if enabledOnly {
		conds = append(conds, `disabled = 0`)
	}
	cond := ""
	if len(conds) > 0 {
		cond = ` WHERE ` + strings.Join(conds, ` AND `)
	}
	lim := ""
	if limit > 0 {
		lim = ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := q.Query(ctx, `SELECT `+userCols+` FROM users`+cond+` ORDER BY created, id`+lim, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// SetRemoteExempt sets whether the storage policy leaves this user's spaces out.
func SetRemoteExempt(ctx context.Context, q Q, id string, exempt bool) error {
	res, err := q.Exec(ctx, `UPDATE users SET remote_exempt = ? WHERE id = ?`, b2i(exempt), id)
	return oneRow(res, err)
}

// SetPasskeyRequired sets whether this person must use a passkey.
func SetPasskeyRequired(ctx context.Context, q Q, id string, required bool) error {
	res, err := q.Exec(ctx, `UPDATE users SET passkey_required = ? WHERE id = ?`, b2i(required), id)
	return oneRow(res, err)
}

// personMatches is the condition that the person (users row with the alias
// prefix, "" or "u.") has query in their username or name, ignoring case: the
// query is folded as FoldName folds and compared with the username (stored in
// lower case) and users.name_key (the folded name), so both databases find the
// same people and no function runs per row.
func personMatches(alias, query string) (string, []any) {
	pat := "%" + likeEscape(FoldName(query)) + "%"
	return `(` + alias + `username LIKE ? ESCAPE '\' OR ` + alias + `name_key LIKE ? ESCAPE '\')`, []any{pat, pat}
}

// inEnabledTenant is the condition on a users row (alias u) of a tenant that is
// not disabled: a disabled tenant signs nobody in.
const inEnabledTenant = `u.tenant_id NOT IN (SELECT id FROM tenants WHERE disabled = 1)`

// CountEnabledPasswordAdmins counts the enabled site administrators of enabled
// tenants who have a password.
func CountEnabledPasswordAdmins(ctx context.Context, q Q) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(*) FROM users u JOIN passwords p ON p.user_id = u.id WHERE u.deleted_at IS NULL AND u.role = 'admin' AND u.site_admin = 1 AND u.disabled = 0 AND `+inEnabledTenant).Scan(&n)
	return
}

// CountTenantPasswordAdmins counts the enabled administrators of one tenant
// who have a password (the back door an organisation that requires its single
// sign-on keeps, 3.6 d4), over users_admins.
func CountTenantPasswordAdmins(ctx context.Context, q Q, tenantID string) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(*) FROM users u JOIN passwords p ON p.user_id = u.id WHERE u.tenant_id = ? AND u.role = 'admin' AND u.deleted_at IS NULL AND u.disabled = 0`, tenantID).Scan(&n)
	return
}

// SetRole changes a user's role in their tenant. (A site administrator is an
// administrator with the flag: the callers that demote one clear the flag too,
// see SetSiteAdmin.)
func SetRole(ctx context.Context, q Q, id, role string) error {
	res, err := q.Exec(ctx, `UPDATE users SET role = ? WHERE id = ?`, role, id)
	return oneRow(res, err)
}

// SetSiteAdmin makes an administrator a site administrator or not; a member
// cannot be made one (ErrNotFound).
func SetSiteAdmin(ctx context.Context, q Q, id string, on bool) error {
	res, err := q.Exec(ctx, `UPDATE users SET site_admin = ? WHERE id = ? AND (role = 'admin' OR ? = 0)`, b2i(on), id, b2i(on))
	return oneRow(res, err)
}

// SetDisabled disables or enables a user. Enabling one ends a deletion of them
// that did not finish: its record (users.delete_pending) is dropped, so that a
// later deletion starts afresh.
func SetDisabled(ctx context.Context, q Q, id string, disabled bool) error {
	res, err := q.Exec(ctx, `UPDATE users SET disabled = ?, delete_pending = CASE WHEN ? = 1 THEN delete_pending ELSE '' END WHERE id = ?`, b2i(disabled), b2i(disabled), id)
	return oneRow(res, err)
}

// CountEnabledAdmins counts the site administrators who can sign in (enabled,
// in an enabled tenant): the rule "keep one administrator" is about the server.
func CountEnabledAdmins(ctx context.Context, q Q) (n int, err error) {
	return CountEnabledAdminsOutside(ctx, q, "")
}

// CountEnabledAdminsOutside is CountEnabledAdmins leaving out one tenant's
// (the one about to be disabled; "" leaves out none).
func CountEnabledAdminsOutside(ctx context.Context, q Q, tenantID string) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(*) FROM users u WHERE u.deleted_at IS NULL AND u.role = 'admin' AND u.site_admin = 1 AND u.disabled = 0 AND u.tenant_id <> ? AND `+inEnabledTenant, tenantID).Scan(&n)
	return
}

func oneRow(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkFirstUser records, in the same transaction that creates the first
// account, that one exists. With strict the second caller fails with ErrExists
// even when two run at once: the primary key is the arbiter, not a count that
// both could read as zero.
func MarkFirstUser(ctx context.Context, q Q, strict bool, now time.Time) error {
	if strict {
		ok, err := HasUsers(ctx, q)
		if err != nil {
			return err
		}
		if ok {
			return ErrExists
		}
		if _, err := q.Exec(ctx, `INSERT INTO site_records(key, value, updated) VALUES ('first_user', ?, ?)`, now.UTC().Format(time.RFC3339), ms(now)); err != nil {
			if IsUnique(err) {
				return ErrExists
			}
			return err
		}
		return nil
	}
	_, err := q.Exec(ctx, `INSERT INTO site_records(key, value, updated) VALUES ('first_user', ?, ?) ON CONFLICT (key) DO NOTHING`, now.UTC().Format(time.RFC3339), ms(now))
	return err
}

// --- passwords ---------------------------------------------------------------

// GetPasswordHash returns the encoded hash, or ErrNotFound when the user has no password.
func GetPasswordHash(ctx context.Context, q Q, userID string) (string, error) {
	var h string
	err := q.QueryRow(ctx, `SELECT hash FROM passwords WHERE user_id = ?`, userID).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return h, err
}

// SetPasswordHash stores or replaces a user's password hash.
func SetPasswordHash(ctx context.Context, q Q, userID, hash string, now time.Time) error {
	_, err := q.Exec(ctx, `INSERT INTO passwords(user_id, hash, updated, must_change) VALUES (?, ?, ?, 0)
		ON CONFLICT (user_id) DO UPDATE SET hash = excluded.hash, updated = excluded.updated, must_change = 0`, userID, hash, ms(now))
	return err
}

// SetTemporaryPassword stores a password an administrator chose: the person
// must replace it at the next sign-in (PasswordMustChange).
func SetTemporaryPassword(ctx context.Context, q Q, userID, hash string, now time.Time) error {
	_, err := q.Exec(ctx, `INSERT INTO passwords(user_id, hash, updated, must_change) VALUES (?, ?, ?, 1)
		ON CONFLICT (user_id) DO UPDATE SET hash = excluded.hash, updated = excluded.updated, must_change = 1`, userID, hash, ms(now))
	return err
}

// RehashPassword replaces the hash of the same password by one with newer
// parameters; it changes nothing else, so a temporary password stays one.
func RehashPassword(ctx context.Context, q Q, userID, hash string) error {
	_, err := q.Exec(ctx, `UPDATE passwords SET hash = ? WHERE user_id = ?`, hash, userID)
	return err
}

// PasswordMustChange says whether the person's password is a temporary one.
func PasswordMustChange(ctx context.Context, q Q, userID string) (bool, error) {
	var n int64
	err := q.QueryRow(ctx, `SELECT must_change FROM passwords WHERE user_id = ?`, userID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return n != 0, err
}

// SetUsername renames an account. The unique key on usernames refuses one that
// is taken (IsUnique).
func SetUsername(ctx context.Context, q Q, userID, username string) error {
	res, err := q.Exec(ctx, `UPDATE users SET username = ? WHERE id = ? AND deleted_at IS NULL`, username, userID)
	return oneRow(res, err)
}
