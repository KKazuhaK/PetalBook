package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
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
	// Groups are the ids of the local groups the account joins when it is made.
	Groups []string
}

func InsertInvite(ctx context.Context, q Q, i Invite) error {
	if i.TenantID == "" {
		i.TenantID = DefaultTenantID
	}
	key, _ := EmailKey(i.Email)
	_, err := q.Exec(ctx, `INSERT INTO invites(id, token_hash, role, created_by, created, expires, tenant_id, site_admin, username, name, email, groups, email_key) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		i.ID, i.TokenHash, i.Role, i.CreatedBy, ms(i.Created), ms(i.Expires), i.TenantID, b2i(i.SiteAdmin), i.Username, i.Name, i.Email, strings.Join(i.Groups, ","), key)
	return err
}

// OpenInviteEmails says which of the addresses (EmailKey form) have an invite
// of the scope that is neither used nor expired at now: one statement per 200
// keys on the invites_email index.
func OpenInviteEmails(ctx context.Context, q Q, sc Scope, keys []string, now time.Time) (map[string]bool, error) {
	out := map[string]bool{}
	for i := 0; i < len(keys); i += 200 {
		chunk := keys[i:min(i+200, len(keys))]
		cond, args := sc.cond("tenant_id")
		args = append(args, ms(now))
		for _, k := range chunk {
			args = append(args, k)
		}
		got, err := strCol(ctx, q, `SELECT DISTINCT email_key FROM invites WHERE `+cond+` AND used = 0 AND expires > ? AND email_key <> '' AND email_key IN (`+
			strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for _, k := range got {
			out[k] = true
		}
	}
	return out, nil
}

// FindUsableInvite returns the invite with this token hash if it is neither
// used nor expired at now, else ErrNotFound.
func FindUsableInvite(ctx context.Context, q Q, tokenHash string, now time.Time) (*Invite, error) {
	var i Invite
	var created, expires, siteAdmin int64
	var groups string
	err := q.QueryRow(ctx, `SELECT id, token_hash, role, created_by, created, expires, tenant_id, site_admin, username, name, email, groups FROM invites
		WHERE token_hash = ? AND used = 0 AND expires > ?`, tokenHash, ms(now)).
		Scan(&i.ID, &i.TokenHash, &i.Role, &i.CreatedBy, &created, &expires, &i.TenantID, &siteAdmin, &i.Username, &i.Name, &i.Email, &groups)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if groups != "" {
		i.Groups = strings.Split(groups, ",")
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
