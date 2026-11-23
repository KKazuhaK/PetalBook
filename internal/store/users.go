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
	// EmailVerified: the address came from a provider that vouched for it, or an
	// administrator set or confirmed it.
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

const userCols = `id, username, name, email, role, disabled, created, email_verified, passkey_required, tenant_id, site_admin`

func scanUser(sc interface{ Scan(...any) error }) (*User, error) {
	var u User
	var disabled, created, verified, required, siteAdmin int64
	if err := sc.Scan(&u.ID, &u.Username, &u.Name, &u.Email, &u.Role, &disabled, &created, &verified, &required, &u.TenantID, &siteAdmin); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	u.Disabled, u.Created, u.EmailVerified, u.PasskeyRequired = disabled != 0, fromMS(created), verified != 0, required != 0
	u.SiteAdmin = siteAdmin != 0
	return &u, nil
}

// GetUser returns the user with this id, or ErrNotFound; a user of a tenant
// outside the scope is not found.
func GetUser(ctx context.Context, q Q, sc Scope, id string) (*User, error) {
	cond, args := sc.cond("tenant_id")
	return scanUser(q.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id = ? AND `+cond, append([]any{id}, args...)...))
}

// GetUserByUsername finds a user by the stored (lower case) username.
func GetUserByUsername(ctx context.Context, q Q, username string) (*User, error) {
	return scanUser(q.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE username = ?`, username))
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
	rows, err := q.Query(ctx, `SELECT `+userCols+` FROM users WHERE `+cond+` ORDER BY created, id`, args...)
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
// case, oldest first; an empty query matches everyone. enabledOnly leaves out the
// disabled accounts before the limit counts. limit <= 0 means no limit.
func SearchUsers(ctx context.Context, q Q, d Dialect, sc Scope, query string, enabledOnly bool, limit int) ([]User, error) {
	var conds []string
	args := []any{}
	sCond, sArgs := sc.cond("tenant_id")
	conds, args = append(conds, sCond), append(args, sArgs...)
	if query != "" {
		lower := "LOWER"
		if d == SQLite {
			lower = sqliteLower
		}
		conds = append(conds, `(`+lower+`(username) LIKE ? ESCAPE '\' OR `+lower+`(name) LIKE ? ESCAPE '\')`)
		pat := "%" + likeEscape(strings.ToLower(query)) + "%"
		args = append(args, pat, pat)
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

// InsertUser adds a user. A taken username or id is a unique violation (see UniqueOn).
func InsertUser(ctx context.Context, q Q, u User) error {
	if u.TenantID == "" {
		u.TenantID = DefaultTenantID
	}
	_, err := q.Exec(ctx, `INSERT INTO users(`+userCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.Username, u.Name, u.Email, u.Role, b2i(u.Disabled), ms(u.Created), b2i(u.EmailVerified), b2i(u.PasskeyRequired), u.TenantID, b2i(u.SiteAdmin))
	return err
}

// UpdateProfile sets the name and email, and whether the email is verified.
func UpdateProfile(ctx context.Context, q Q, id, name, email string, verified bool) error {
	res, err := q.Exec(ctx, `UPDATE users SET name = ?, email = ?, email_verified = ? WHERE id = ?`, name, email, b2i(verified), id)
	return oneRow(res, err)
}

// SetEmail sets a user's address and whether it is verified.
func SetEmail(ctx context.Context, q Q, id, email string, verified bool) error {
	res, err := q.Exec(ctx, `UPDATE users SET email = ?, email_verified = ? WHERE id = ?`, email, b2i(verified), id)
	return oneRow(res, err)
}

// SetEmailVerified marks the address verified or not.
func SetEmailVerified(ctx context.Context, q Q, id string, verified bool) error {
	res, err := q.Exec(ctx, `UPDATE users SET email_verified = ? WHERE id = ?`, b2i(verified), id)
	return oneRow(res, err)
}

// SetPasskeyRequired sets whether this person must use a passkey.
func SetPasskeyRequired(ctx context.Context, q Q, id string, required bool) error {
	res, err := q.Exec(ctx, `UPDATE users SET passkey_required = ? WHERE id = ?`, b2i(required), id)
	return oneRow(res, err)
}

// UsersByEmail returns the users whose address equals email, ignoring case
// (at most limit; limit <= 0: all).
func UsersByEmail(ctx context.Context, q Q, d Dialect, sc Scope, email string, limit int) ([]User, error) {
	lower := "LOWER"
	if d == SQLite {
		lower = sqliteLower
	}
	cond, cargs := sc.cond("tenant_id")
	sqlText := `SELECT ` + userCols + ` FROM users WHERE email <> '' AND ` + lower + `(email) = ? AND ` + cond + ` ORDER BY created, id`
	args := append([]any{strings.ToLower(email)}, cargs...)
	if limit > 0 {
		sqlText += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := q.Query(ctx, sqlText, args...)
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

// CountEnabledPasswordAdmins counts the enabled site administrators who have a password.
func CountEnabledPasswordAdmins(ctx context.Context, q Q) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(*) FROM users u JOIN passwords p ON p.user_id = u.id WHERE u.role = 'admin' AND u.site_admin = 1 AND u.disabled = 0`).Scan(&n)
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

// SetDisabled disables or enables a user.
func SetDisabled(ctx context.Context, q Q, id string, disabled bool) error {
	res, err := q.Exec(ctx, `UPDATE users SET disabled = ? WHERE id = ?`, b2i(disabled), id)
	return oneRow(res, err)
}

// CountEnabledAdmins counts the site administrators who can sign in: the rule
// "keep one administrator" is about the server.
func CountEnabledAdmins(ctx context.Context, q Q) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE role = 'admin' AND site_admin = 1 AND disabled = 0`).Scan(&n)
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
		if _, err := q.Exec(ctx, `INSERT INTO settings(key, value) VALUES ('first_user', ?)`, now.UTC().Format(time.RFC3339)); err != nil {
			if IsUnique(err) {
				return ErrExists
			}
			return err
		}
		return nil
	}
	_, err := q.Exec(ctx, `INSERT INTO settings(key, value) VALUES ('first_user', ?) ON CONFLICT (key) DO NOTHING`, now.UTC().Format(time.RFC3339))
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
	_, err := q.Exec(ctx, `INSERT INTO passwords(user_id, hash, updated) VALUES (?, ?, ?)
		ON CONFLICT (user_id) DO UPDATE SET hash = excluded.hash, updated = excluded.updated`, userID, hash, ms(now))
	return err
}

// --- identities --------------------------------------------------------------

// Identity is an external sign-in (issuer plus subject) attached to a user.
type Identity struct {
	ID      string
	UserID  string
	Issuer  string
	Subject string
	Label   string
	Created time.Time
}

// InsertIdentity attaches an identity; (issuer, subject) is unique.
func InsertIdentity(ctx context.Context, q Q, i Identity) error {
	_, err := q.Exec(ctx, `INSERT INTO identities(id, user_id, issuer, subject, label, created) VALUES (?, ?, ?, ?, ?, ?)`,
		i.ID, i.UserID, i.Issuer, i.Subject, i.Label, ms(i.Created))
	return err
}

// FindIdentity looks an identity up by issuer and subject.
func FindIdentity(ctx context.Context, q Q, issuer, subject string) (*Identity, error) {
	var i Identity
	var created int64
	err := q.QueryRow(ctx, `SELECT id, user_id, issuer, subject, label, created FROM identities WHERE issuer = ? AND subject = ?`, issuer, subject).
		Scan(&i.ID, &i.UserID, &i.Issuer, &i.Subject, &i.Label, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	i.Created = fromMS(created)
	return &i, err
}

// ListIdentities returns a user's identities, oldest first.
func ListIdentities(ctx context.Context, q Q, userID string) ([]Identity, error) {
	rows, err := q.Query(ctx, `SELECT id, user_id, issuer, subject, label, created FROM identities WHERE user_id = ? ORDER BY created, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Identity
	for rows.Next() {
		var i Identity
		var created int64
		if err := rows.Scan(&i.ID, &i.UserID, &i.Issuer, &i.Subject, &i.Label, &created); err != nil {
			return nil, err
		}
		i.Created = fromMS(created)
		out = append(out, i)
	}
	return out, rows.Err()
}

// DeleteIdentity removes one of the user's identities; ErrNotFound when it is
// not theirs or does not exist.
func DeleteIdentity(ctx context.Context, q Q, id, userID string) error {
	res, err := q.Exec(ctx, `DELETE FROM identities WHERE id = ? AND user_id = ?`, id, userID)
	return oneRow(res, err)
}
