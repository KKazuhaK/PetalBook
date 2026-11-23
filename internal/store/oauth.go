package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// --- the signing key ---------------------------------------------------------

const oauthSigningKeyID = "signing"

// GetOAuthSigningKey returns the sealed key of the authorization server; ok is
// false before the first start made one.
func GetOAuthSigningKey(ctx context.Context, q Q) (sealed string, ok bool, err error) {
	err = q.QueryRow(ctx, `SELECT sealed FROM oauth_keys WHERE id = ?`, oauthSigningKeyID).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return sealed, err == nil, err
}

// InsertOAuthSigningKey stores the sealed key unless one is there already, and
// reports whether this one was stored: of two servers starting together, one
// wins and the other reads its key.
func InsertOAuthSigningKey(ctx context.Context, q Q, sealed string, now time.Time) (bool, error) {
	res, err := q.Exec(ctx, `INSERT INTO oauth_keys(id, sealed, created) VALUES (?, ?, ?) ON CONFLICT (id) DO NOTHING`,
		oauthSigningKeyID, sealed, ms(now))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// --- clients -------------------------------------------------------------------

// OAuthClient is a client that registered itself.
type OAuthClient struct {
	ID           string
	Name         string
	RedirectURIs []string
	Fingerprint  string
	Created      time.Time
	LastUsed     time.Time // zero: no grant was ever made for it
}

// GetOAuthClient finds a registered client; ErrNotFound when there is none.
func GetOAuthClient(ctx context.Context, q Q, id string) (*OAuthClient, error) {
	return scanClient(q.QueryRow(ctx, `SELECT id, name, redirect_uris, fingerprint, created, last_used FROM oauth_clients WHERE id = ?`, id))
}

// FindOAuthClient finds the client registered with this fingerprint.
func FindOAuthClient(ctx context.Context, q Q, fingerprint string) (*OAuthClient, error) {
	return scanClient(q.QueryRow(ctx, `SELECT id, name, redirect_uris, fingerprint, created, last_used FROM oauth_clients WHERE fingerprint = ?`, fingerprint))
}

func scanClient(row interface{ Scan(...any) error }) (*OAuthClient, error) {
	var c OAuthClient
	var uris string
	var created, used int64
	if err := row.Scan(&c.ID, &c.Name, &uris, &c.Fingerprint, &created, &used); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if err := json.Unmarshal([]byte(uris), &c.RedirectURIs); err != nil {
		return nil, err
	}
	c.Created, c.LastUsed = fromMS(created), msOrZero(used)
	return &c, nil
}

// InsertOAuthClient stores a client; IsUnique tells a fingerprint that is there.
func InsertOAuthClient(ctx context.Context, q Q, c OAuthClient) error {
	uris, err := json.Marshal(c.RedirectURIs)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `INSERT INTO oauth_clients(id, name, redirect_uris, fingerprint, created, last_used) VALUES (?, ?, ?, ?, ?, 0)`,
		c.ID, c.Name, string(uris), c.Fingerprint, ms(c.Created))
	return err
}

// CountOAuthClients is how many clients are registered.
func CountOAuthClients(ctx context.Context, q Q) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(*) FROM oauth_clients`).Scan(&n)
	return
}

// DeleteOldestUnusedOAuthClient makes room: it drops the oldest client that no
// grant was ever made for, and reports whether there was one.
func DeleteOldestUnusedOAuthClient(ctx context.Context, q Q) (bool, error) {
	var id string
	err := q.QueryRow(ctx, `SELECT c.id FROM oauth_clients c WHERE c.last_used = 0
		AND NOT EXISTS (SELECT 1 FROM oauth_grants g WHERE g.client_id = c.id) ORDER BY c.created, c.id LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, err = q.Exec(ctx, `DELETE FROM oauth_clients WHERE id = ?`, id)
	return err == nil, err
}

// PruneOAuthClients drops the clients nobody uses: one never authorized and
// made before unusedBefore, one authorized but not since idleBefore, and in
// both cases without a grant now.
func PruneOAuthClients(ctx context.Context, q Q, unusedBefore, idleBefore time.Time) (int64, error) {
	res, err := q.Exec(ctx, `DELETE FROM oauth_clients WHERE NOT EXISTS (SELECT 1 FROM oauth_grants g WHERE g.client_id = oauth_clients.id)
		AND ((last_used = 0 AND created < ?) OR (last_used > 0 AND last_used < ?))`, ms(unusedBefore), ms(idleBefore))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// TouchOAuthClient records that a grant was made for a client.
func TouchOAuthClient(ctx context.Context, q Q, id string, at time.Time) error {
	_, err := q.Exec(ctx, `UPDATE oauth_clients SET last_used = ? WHERE id = ?`, ms(at), id)
	return err
}

// --- grants --------------------------------------------------------------------

// OAuthRetired is a refresh token (its hash) that was replaced at At.
type OAuthRetired struct {
	Hash string
	At   time.Time
}

// OAuthGrant is one approval of one client by one person. RefreshHash is the
// SHA-256 of the one refresh token that works now.
type OAuthGrant struct {
	ID          string
	UserID      string
	ClientID    string
	ClientName  string
	Access      string // "read" or "write"
	Resource    string
	Created     time.Time
	LastUsed    time.Time
	RefreshHash string
	Spaces      []TokenSpace
	Retired     []OAuthRetired // only when asked for
}

// OAuthGrantRow is a grant with its owner, as the listings give it.
type OAuthGrantRow struct {
	OAuthGrant
	Username  string
	OwnerName string
}

// InsertOAuthGrant stores a grant, its spaces and the use of its client, in the
// caller's transaction.
func InsertOAuthGrant(ctx context.Context, q Q, g OAuthGrant) error {
	if _, err := q.Exec(ctx, `INSERT INTO oauth_grants(id, user_id, client_id, client_name, access, resource, created, last_used, refresh_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, g.ID, g.UserID, g.ClientID, g.ClientName, g.Access, g.Resource, ms(g.Created), ms(g.LastUsed), g.RefreshHash); err != nil {
		return err
	}
	for _, s := range g.Spaces {
		if _, err := q.Exec(ctx, `INSERT INTO oauth_grant_spaces(grant_id, space_id) VALUES (?, ?)`, g.ID, s.ID); err != nil {
			return err
		}
	}
	return nil
}

// GetOAuthGrant finds a grant with the ids of its spaces, in one query on the
// primary key; ErrNotFound when there is none. withRetired adds the replaced
// refresh tokens.
func GetOAuthGrant(ctx context.Context, q Q, id string, withRetired bool) (*OAuthGrant, error) {
	rows, err := q.Query(ctx, `SELECT g.id, g.user_id, g.client_id, g.client_name, g.access, g.resource, g.created, g.last_used, g.refresh_hash, gs.space_id
		FROM oauth_grants g LEFT JOIN oauth_grant_spaces gs ON gs.grant_id = g.id WHERE g.id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var g *OAuthGrant
	for rows.Next() {
		var cur OAuthGrant
		var created, used int64
		var space sql.NullString
		if err := rows.Scan(&cur.ID, &cur.UserID, &cur.ClientID, &cur.ClientName, &cur.Access, &cur.Resource, &created, &used, &cur.RefreshHash, &space); err != nil {
			return nil, err
		}
		if g == nil {
			cur.Created, cur.LastUsed = fromMS(created), fromMS(used)
			g = &cur
		}
		if space.Valid {
			g.Spaces = append(g.Spaces, TokenSpace{ID: space.String})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if g == nil {
		return nil, ErrNotFound
	}
	if withRetired {
		r, err := q.Query(ctx, `SELECT hash, at FROM oauth_retired WHERE grant_id = ? ORDER BY at`, id)
		if err != nil {
			return nil, err
		}
		defer r.Close()
		for r.Next() {
			var x OAuthRetired
			var at int64
			if err := r.Scan(&x.Hash, &at); err != nil {
				return nil, err
			}
			x.At = fromMS(at)
			g.Retired = append(g.Retired, x)
		}
		if err := r.Err(); err != nil {
			return nil, err
		}
	}
	return g, nil
}

// GrantFilter selects grants for ListOAuthGrants, as TokenFilter does tokens.
type GrantFilter struct {
	UserID       string
	ID           string
	AfterCreated time.Time
	AfterID      string
	Limit        int
}

// ListOAuthGrants returns grants newest first with their owners and the spaces
// they name, in one query. A space's Name is its name while the grant's owner
// is a member of it, "" otherwise.
func ListOAuthGrants(ctx context.Context, q Q, sc Scope, f GrantFilter) ([]OAuthGrantRow, error) {
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
	if !f.AfterCreated.IsZero() {
		where = append(where, "(created < ? OR (created = ? AND id < ?))")
		args = append(args, ms(f.AfterCreated), ms(f.AfterCreated), f.AfterID)
	}
	inner := `SELECT id, user_id, client_id, client_name, access, created, last_used FROM oauth_grants`
	if len(where) > 0 {
		inner += ` WHERE ` + strings.Join(where, " AND ")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	inner += ` ORDER BY created DESC, id DESC LIMIT ` + strconv.Itoa(limit)
	rows, err := q.Query(ctx, `SELECT g.id, g.user_id, u.username, u.name, g.client_id, g.client_name, g.access, g.created, g.last_used,
		gs.space_id, CASE WHEN m.user_id IS NULL THEN '' ELSE COALESCE(sp.name, '') END
		FROM (`+inner+`) g JOIN users u ON u.id = g.user_id
		LEFT JOIN oauth_grant_spaces gs ON gs.grant_id = g.id
		LEFT JOIN spaces sp ON sp.id = gs.space_id
		LEFT JOIN space_members m ON m.space_id = gs.space_id AND m.user_id = g.user_id
		ORDER BY g.created DESC, g.id DESC, gs.space_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OAuthGrantRow
	for rows.Next() {
		var cur OAuthGrantRow
		var created, used int64
		var space, spaceName sql.NullString
		if err := rows.Scan(&cur.ID, &cur.UserID, &cur.Username, &cur.OwnerName, &cur.ClientID, &cur.ClientName, &cur.Access, &created, &used, &space, &spaceName); err != nil {
			return nil, err
		}
		if n := len(out); n == 0 || out[n-1].ID != cur.ID {
			cur.Created, cur.LastUsed = fromMS(created), fromMS(used)
			out = append(out, cur)
		}
		if space.Valid {
			last := &out[len(out)-1]
			last.Spaces = append(last.Spaces, TokenSpace{ID: space.String, Name: spaceName.String})
		}
	}
	return out, rows.Err()
}

// DeleteOAuthGrant ends a grant and reports whether it existed.
func DeleteOAuthGrant(ctx context.Context, q Q, id string) (bool, error) {
	res, err := q.Exec(ctx, `DELETE FROM oauth_grants WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// RotateOAuthRefresh replaces the live refresh token of a grant, but only while
// it is still oldHash, so two requests with the same token cannot both win. It
// keeps the last keep replaced hashes, and reports whether it replaced it. The
// three steps are one transaction: a failure leaves the old token live.
func RotateOAuthRefresh(ctx context.Context, s *Store, id, oldHash, newHash string, at time.Time, keep int) (bool, error) {
	won := false
	err := s.Tx(ctx, func(q Q) error {
		res, err := q.Exec(ctx, `UPDATE oauth_grants SET refresh_hash = ?, last_used = ? WHERE id = ? AND refresh_hash = ?`, newHash, ms(at), id, oldHash)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return err
		}
		if _, err := q.Exec(ctx, `INSERT INTO oauth_retired(grant_id, hash, at) VALUES (?, ?, ?) ON CONFLICT (grant_id, hash) DO NOTHING`, id, oldHash, ms(at)); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `DELETE FROM oauth_retired WHERE grant_id = ? AND hash NOT IN
		(SELECT hash FROM (SELECT hash FROM oauth_retired WHERE grant_id = ? ORDER BY at DESC, hash DESC LIMIT `+strconv.Itoa(keep)+`) r)`, id, id); err != nil {
			return err
		}
		won = true
		return nil
	})
	return won && err == nil, err
}

// DeleteIdleOAuthGrants ends the grants not used since cutoff.
func DeleteIdleOAuthGrants(ctx context.Context, q Q, cutoff time.Time) (int64, error) {
	res, err := q.Exec(ctx, `DELETE FROM oauth_grants WHERE last_used < ?`, ms(cutoff))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
