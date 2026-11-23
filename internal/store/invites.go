package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Invite lets one person make an account. Only the hash of its token is kept.
type Invite struct {
	ID        string
	TokenHash string
	Role      string
	CreatedBy string
	Created   time.Time
	Expires   time.Time
	Used      time.Time // zero while unused
	// TenantID is the tenant the account is made in; SiteAdmin makes an
	// administrator account a site administrator.
	TenantID  string
	SiteAdmin bool
	// Username, Name and Email start the form that accepts the invite (the
	// first administrator of a new tenant); an Email also becomes the account's
	// (not verified). All may be empty.
	Username, Name, Email string
}

func InsertInvite(ctx context.Context, q Q, i Invite) error {
	if i.TenantID == "" {
		i.TenantID = DefaultTenantID
	}
	_, err := q.Exec(ctx, `INSERT INTO invites(id, token_hash, role, created_by, created, expires, tenant_id, site_admin, username, name, email) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		i.ID, i.TokenHash, i.Role, i.CreatedBy, ms(i.Created), ms(i.Expires), i.TenantID, b2i(i.SiteAdmin), i.Username, i.Name, i.Email)
	return err
}

// FindUsableInvite returns the invite with this token hash if it is neither
// used nor expired at now, else ErrNotFound.
func FindUsableInvite(ctx context.Context, q Q, tokenHash string, now time.Time) (*Invite, error) {
	var i Invite
	var created, expires, siteAdmin int64
	err := q.QueryRow(ctx, `SELECT id, token_hash, role, created_by, created, expires, tenant_id, site_admin, username, name, email FROM invites
		WHERE token_hash = ? AND used = 0 AND expires > ?`, tokenHash, ms(now)).
		Scan(&i.ID, &i.TokenHash, &i.Role, &i.CreatedBy, &created, &expires, &i.TenantID, &siteAdmin, &i.Username, &i.Name, &i.Email)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	i.Created, i.Expires, i.SiteAdmin = fromMS(created), fromMS(expires), siteAdmin != 0
	return &i, err
}

// UseInvite marks the invite used. It reports false when someone else used it
// first (or it expired): the update is the single point where two people
// racing for one invite are told apart.
func UseInvite(ctx context.Context, q Q, id, userID string, now time.Time) (bool, error) {
	res, err := q.Exec(ctx, `UPDATE invites SET used = ?, used_by = ? WHERE id = ? AND used = 0 AND expires > ?`, ms(now), userID, id, ms(now))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ListOpenInvites returns the invites neither used nor expired, newest first.
func ListOpenInvites(ctx context.Context, q Q, sc Scope, now time.Time) ([]Invite, error) {
	cond, cargs := sc.cond("tenant_id")
	rows, err := q.Query(ctx, `SELECT id, token_hash, role, created_by, created, expires, tenant_id, site_admin FROM invites
		WHERE used = 0 AND expires > ? AND `+cond+` ORDER BY created DESC, id`, append([]any{ms(now)}, cargs...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Invite
	for rows.Next() {
		var i Invite
		var created, expires, siteAdmin int64
		if err := rows.Scan(&i.ID, &i.TokenHash, &i.Role, &i.CreatedBy, &created, &expires, &i.TenantID, &siteAdmin); err != nil {
			return nil, err
		}
		i.Created, i.Expires, i.SiteAdmin = fromMS(created), fromMS(expires), siteAdmin != 0
		out = append(out, i)
	}
	return out, rows.Err()
}

// DeleteInvite revokes an invite; ErrNotFound when there is none.
func DeleteInvite(ctx context.Context, q Q, sc Scope, id string) error {
	cond, cargs := sc.cond("tenant_id")
	res, err := q.Exec(ctx, `DELETE FROM invites WHERE id = ? AND `+cond, append([]any{id}, cargs...)...)
	return oneRow(res, err)
}

// DeleteOldInvites removes invites that expired before the time given.
func DeleteOldInvites(ctx context.Context, q Q, before time.Time) (int64, error) {
	res, err := q.Exec(ctx, `DELETE FROM invites WHERE expires < ?`, ms(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
