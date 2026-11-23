package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
)

// APIToken is a token of docs/api.md ("API tokens"). Only the SHA-256 of its
// secret is stored. The zero Expires means never, the zero Revoked not revoked,
// the zero LastUsed never used.
type APIToken struct {
	ID       string
	UserID   string
	Name     string
	Prefix   string
	Hash     string
	Access   string // "read" or "write"
	Created  time.Time
	Expires  time.Time
	LastUsed time.Time
	Revoked  time.Time
	Spaces   []TokenSpace
}

// TokenSpace is a space a token names; Name is "" when the space is gone or
// the token's owner is no longer a member (only the listings fill it).
type TokenSpace struct{ ID, Name string }

// TokenRow is a token with its owner, as the listings give it.
type TokenRow struct {
	APIToken
	Username  string
	OwnerName string
}

func zeroMS(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return ms(t)
}

func msOrZero(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return fromMS(v)
}

// InsertAPIToken stores a token and the spaces it names. Call it in a
// transaction.
func InsertAPIToken(ctx context.Context, q Q, t APIToken) error {
	if _, err := q.Exec(ctx, `INSERT INTO api_tokens(id, user_id, name, prefix, hash, access, created, expires, last_used, revoked)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, 0)`, t.ID, t.UserID, t.Name, t.Prefix, t.Hash, t.Access, ms(t.Created), zeroMS(t.Expires)); err != nil {
		return err
	}
	for _, s := range t.Spaces {
		if _, err := q.Exec(ctx, `INSERT INTO api_token_spaces(token_id, space_id) VALUES (?, ?)`, t.ID, s.ID); err != nil {
			return err
		}
	}
	return nil
}

// CountLiveAPITokens counts a user's tokens that are neither revoked nor expired at now.
func CountLiveAPITokens(ctx context.Context, q Q, userID string, now time.Time) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(*) FROM api_tokens WHERE user_id = ? AND revoked = 0 AND (expires = 0 OR expires > ?)`,
		userID, ms(now)).Scan(&n)
	return
}

// GetAPITokenByHash finds a token by the SHA-256 of its secret (hex), with the
// ids of its spaces, in one query on the unique index. ErrNotFound when there
// is none.
func GetAPITokenByHash(ctx context.Context, q Q, hash string) (*APIToken, error) {
	rows, err := q.Query(ctx, `SELECT t.id, t.user_id, t.name, t.prefix, t.hash, t.access, t.created, t.expires, t.last_used, t.revoked, ts.space_id
		FROM api_tokens t LEFT JOIN api_token_spaces ts ON ts.token_id = t.id WHERE t.hash = ?`, hash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var t *APIToken
	for rows.Next() {
		var cur APIToken
		var created, expires, used, revoked int64
		var space sql.NullString
		if err := rows.Scan(&cur.ID, &cur.UserID, &cur.Name, &cur.Prefix, &cur.Hash, &cur.Access, &created, &expires, &used, &revoked, &space); err != nil {
			return nil, err
		}
		if t == nil {
			cur.Created, cur.Expires, cur.LastUsed, cur.Revoked = fromMS(created), msOrZero(expires), msOrZero(used), msOrZero(revoked)
			t = &cur
		}
		if space.Valid {
			t.Spaces = append(t.Spaces, TokenSpace{ID: space.String})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if t == nil {
		return nil, ErrNotFound
	}
	return t, nil
}

// TokenFilter selects tokens for ListAPITokens.
type TokenFilter struct {
	UserID string // "" for everyone's
	ID     string // one token
	Prefix string // tokens with this prefix
	// After continues a listing: the Created and ID of the last token of the
	// page before (both zero for the first page).
	AfterCreated time.Time
	AfterID      string
	// Limit is how many tokens to return (at most; the caller asks for one more
	// to know whether there is a next page).
	Limit int
}

// ListAPITokens returns tokens newest first with their owners and the spaces
// they name, in one query. A space's Name is its name while its token's owner
// is a member of it, "" otherwise.
func ListAPITokens(ctx context.Context, q Q, sc Scope, f TokenFilter) ([]TokenRow, error) {
	var where []string
	var args []any
	sCond, sArgs := sc.ofUser("user_id")
	where, args = append(where, sCond), append(args, sArgs...)
	if f.UserID != "" {
		where = append(where, "user_id = ?")
		args = append(args, f.UserID)
	}
	if f.ID != "" {
		where = append(where, "id = ?")
		args = append(args, f.ID)
	}
	if f.Prefix != "" {
		where = append(where, "prefix = ?")
		args = append(args, f.Prefix)
	}
	if !f.AfterCreated.IsZero() {
		where = append(where, "(created < ? OR (created = ? AND id < ?))")
		args = append(args, ms(f.AfterCreated), ms(f.AfterCreated), f.AfterID)
	}
	inner := `SELECT id, user_id, name, prefix, access, created, expires, last_used, revoked FROM api_tokens`
	if len(where) > 0 {
		inner += ` WHERE ` + strings.Join(where, " AND ")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	inner += ` ORDER BY created DESC, id DESC LIMIT ` + strconv.Itoa(limit)
	rows, err := q.Query(ctx, `SELECT t.id, t.user_id, u.username, u.name, t.name, t.prefix, t.access, t.created, t.expires, t.last_used, t.revoked,
		ts.space_id, CASE WHEN m.user_id IS NULL THEN '' ELSE COALESCE(sp.name, '') END
		FROM (`+inner+`) t JOIN users u ON u.id = t.user_id
		LEFT JOIN api_token_spaces ts ON ts.token_id = t.id
		LEFT JOIN spaces sp ON sp.id = ts.space_id
		LEFT JOIN space_members m ON m.space_id = ts.space_id AND m.user_id = t.user_id
		ORDER BY t.created DESC, t.id DESC, ts.space_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TokenRow
	for rows.Next() {
		var cur TokenRow
		var created, expires, used, revoked int64
		var space, spaceName sql.NullString
		if err := rows.Scan(&cur.ID, &cur.UserID, &cur.Username, &cur.OwnerName, &cur.Name, &cur.Prefix, &cur.Access, &created, &expires, &used, &revoked, &space, &spaceName); err != nil {
			return nil, err
		}
		if n := len(out); n == 0 || out[n-1].ID != cur.ID {
			cur.Created, cur.Expires, cur.LastUsed, cur.Revoked = fromMS(created), msOrZero(expires), msOrZero(used), msOrZero(revoked)
			out = append(out, cur)
		}
		if space.Valid {
			last := &out[len(out)-1]
			last.Spaces = append(last.Spaces, TokenSpace{ID: space.String, Name: spaceName.String})
		}
	}
	return out, rows.Err()
}

// RevokeAPIToken revokes a token at the given time. It keeps the time of an
// earlier revocation, and reports whether the token exists.
func RevokeAPIToken(ctx context.Context, q Q, id string, at time.Time) (bool, error) {
	if _, err := q.Exec(ctx, `UPDATE api_tokens SET revoked = ? WHERE id = ? AND revoked = 0`, ms(at), id); err != nil {
		return false, err
	}
	var one int
	err := q.QueryRow(ctx, `SELECT 1 FROM api_tokens WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// TouchAPIToken records that a token was used.
func TouchAPIToken(ctx context.Context, q Q, id string, at time.Time) error {
	_, err := q.Exec(ctx, `UPDATE api_tokens SET last_used = ? WHERE id = ?`, ms(at), id)
	return err
}

// DeleteOldAPITokens removes tokens revoked, or expired, before cutoff, and
// reports how many.
func DeleteOldAPITokens(ctx context.Context, q Q, cutoff time.Time) (int64, error) {
	res, err := q.Exec(ctx, `DELETE FROM api_tokens WHERE (revoked > 0 AND revoked < ?) OR (expires > 0 AND expires < ?)`, ms(cutoff), ms(cutoff))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
