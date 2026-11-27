package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Requests to join a tenant by the domain of an address (3.6 d5; table
// join_requests). The rules are internal/auth's; this is their rows.

// The states of a request. A pending one past its expiry is expired, whether
// or not the sweep has written it so yet.
const (
	JoinPending   = "pending"
	JoinApproved  = "approved"
	JoinDenied    = "denied"
	JoinWithdrawn = "withdrawn"
	JoinExpired   = "expired"
	JoinCancelled = "cancelled"
)

// Refusals of a new request: the username is held by another pending one, or
// the person (the address) has one pending in the tenant already.
var (
	ErrJoinUsername = errors.New("a pending request holds the username")
	ErrJoinPending  = errors.New("the person has a pending request in the tenant")
)

// JoinRequest is one request. A zero time is 0 in the row.
type JoinRequest struct {
	ID           string
	TenantID     string
	UserID       string
	Username     string
	Name         string
	Email        string
	EmailKey     string
	Domain       string
	PasswordHash string
	Terms        string
	Lang         string
	TokenHash    string
	State        string
	Created      time.Time
	Expires      time.Time
	DecidedAt    time.Time
	DecidedBy    string
	MadeUser     string
}

// StateAt is the request's state at now: a pending one past its expiry is expired.
func (r JoinRequest) StateAt(now time.Time) string {
	if r.State == JoinPending && !r.Expires.After(now) {
		return JoinExpired
	}
	return r.State
}

const joinCols = `id, tenant_id, user_id, username, name, email, email_key, domain, password_hash, terms, lang, token_hash, state,
	created, expires, decided_at, decided_by, made_user`

func scanJoin(sc interface{ Scan(...any) error }) (JoinRequest, error) {
	var r JoinRequest
	var cr, ex, de int64
	err := sc.Scan(&r.ID, &r.TenantID, &r.UserID, &r.Username, &r.Name, &r.Email, &r.EmailKey, &r.Domain, &r.PasswordHash, &r.Terms, &r.Lang,
		&r.TokenHash, &r.State, &cr, &ex, &de, &r.DecidedBy, &r.MadeUser)
	r.Created, r.Expires, r.DecidedAt = fromMS(cr), fromMS(ex), optFromMS(de)
	return r, err
}

func queryJoins(ctx context.Context, q Q, query string, args ...any) ([]JoinRequest, error) {
	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []JoinRequest{}
	for rows.Next() {
		r, err := scanJoin(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsertJoinRequest stores a new pending request. The pending ones of the
// same username, or of the same person in the tenant, that have expired are
// written expired first; a live one is ErrJoinUsername or ErrJoinPending.
// Run it in a transaction.
func InsertJoinRequest(ctx context.Context, q Q, r JoinRequest, now time.Time) error {
	if _, err := q.Exec(ctx, `UPDATE join_requests SET state = 'expired', password_hash = '' WHERE state = 'pending' AND expires <= ?
		AND (username = ? OR tenant_id = ? AND email_key = ?)`, ms(now), r.Username, r.TenantID, r.EmailKey); err != nil {
		return err
	}
	return atomically(ctx, q, func(q Q) error {
		_, err := q.Exec(ctx, `INSERT INTO join_requests(`+joinCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, 0, '', '')`,
			r.ID, r.TenantID, r.UserID, r.Username, r.Name, r.Email, r.EmailKey, r.Domain, r.PasswordHash, r.Terms, r.Lang, r.TokenHash,
			ms(r.Created), ms(r.Expires))
		if on, ok := UniqueOn(err); ok {
			if strings.Contains(on, "username") {
				return ErrJoinUsername
			}
			if strings.Contains(on, "email_key") || strings.Contains(on, "person") {
				return ErrJoinPending
			}
		}
		return err
	})
}

// JoinRequestByToken is the request of a registration link's token (its
// hash), whatever its state; ErrNotFound when there is none.
func JoinRequestByToken(ctx context.Context, q Q, tokenHash string) (JoinRequest, error) {
	r, err := scanJoin(q.QueryRow(ctx, `SELECT `+joinCols+` FROM join_requests WHERE token_hash = ?`, tokenHash))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// GetJoinRequest is a tenant's request; ErrNotFound when the tenant has no such one.
func GetJoinRequest(ctx context.Context, q Q, tenantID, id string) (JoinRequest, error) {
	r, err := scanJoin(q.QueryRow(ctx, `SELECT `+joinCols+` FROM join_requests WHERE id = ? AND tenant_id = ?`, id, tenantID))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// JoinFilter is a page of a tenant's list, newest first: the pending ones
// (that have not expired), or all; after the cursor (the created time and id
// of the last row shown).
type JoinFilter struct {
	PendingOnly  bool
	AfterCreated time.Time
	AfterID      string
	Limit        int
}

// ListJoinRequests is a page of a tenant's requests (join_requests_list for
// the pending ones, join_requests_all for every one).
func ListJoinRequests(ctx context.Context, q Q, tenantID string, f JoinFilter, now time.Time) ([]JoinRequest, error) {
	where := `tenant_id = ?`
	args := []any{tenantID}
	if f.PendingOnly {
		where += ` AND state = 'pending' AND expires > ?`
		args = append(args, ms(now))
	}
	if f.AfterID != "" {
		where += ` AND (created < ? OR created = ? AND id < ?)`
		args = append(args, ms(f.AfterCreated), ms(f.AfterCreated), f.AfterID)
	}
	args = append(args, f.Limit)
	return queryJoins(ctx, q, `SELECT `+joinCols+` FROM join_requests WHERE `+where+` ORDER BY created DESC, id DESC LIMIT ?`, args...)
}

// DecideJoinRequest moves a tenant's pending request that has not expired to
// state (approved, denied), by an administrator; madeUser is the account an
// approval made. The password hash goes. False when there is no such pending
// request: the single point where two decisions are told apart.
func DecideJoinRequest(ctx context.Context, q Q, tenantID, id, state, by, madeUser string, now time.Time) (bool, error) {
	res, err := q.Exec(ctx, `UPDATE join_requests SET state = ?, decided_at = ?, decided_by = ?, made_user = ?, password_hash = ''
		WHERE id = ? AND tenant_id = ? AND state = 'pending' AND expires > ?`, state, ms(now), by, madeUser, id, tenantID, ms(now))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// WithdrawJoinRequest withdraws the pending request of a token (the person,
// from the waiting page); false when it is not pending.
func WithdrawJoinRequest(ctx context.Context, q Q, tokenHash string, now time.Time) (bool, error) {
	res, err := q.Exec(ctx, `UPDATE join_requests SET state = 'withdrawn', decided_at = ?, password_hash = ''
		WHERE token_hash = ? AND state = 'pending' AND expires > ?`, ms(now), tokenHash, ms(now))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// CancelJoinRequests cancels the pending requests of a tenant's domain that
// have not expired (it was removed, it lapsed, it moved to another tenant, or
// its join mode was turned off), and returns exactly those it cancelled, as
// they were: each is cancelled by its id, only while it is still pending.
func CancelJoinRequests(ctx context.Context, q Q, tenantID, domain string, now time.Time) ([]JoinRequest, error) {
	var out []JoinRequest
	err := atomically(ctx, q, func(q Q) error {
		read, err := queryJoins(ctx, q, `SELECT `+joinCols+` FROM join_requests WHERE tenant_id = ? AND domain = ? AND state = 'pending' AND expires > ?`,
			tenantID, domain, ms(now))
		if err != nil {
			return err
		}
		out = out[:0]
		for _, r := range read {
			res, err := q.Exec(ctx, `UPDATE join_requests SET state = 'cancelled', decided_at = ?, password_hash = ''
				WHERE id = ? AND state = 'pending'`, ms(now), r.ID)
			if err != nil {
				return err
			}
			if n, err := res.RowsAffected(); err != nil {
				return err
			} else if n == 1 {
				out = append(out, r)
			}
		}
		return nil
	})
	return out, err
}

// CountPendingJoins is how many requests a tenant holds waiting that have not
// expired (join_requests_list).
func CountPendingJoins(ctx context.Context, q Q, tenantID string, now time.Time) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT COUNT(*) FROM join_requests WHERE tenant_id = ? AND state = 'pending' AND expires > ?`, tenantID, ms(now)).Scan(&n)
	return n, err
}

// PendingJoinIDs are the ids of a tenant's domain's pending requests that have
// not expired, sorted (what a confirmation to cancel them is made from).
func PendingJoinIDs(ctx context.Context, q Q, tenantID, domain string, now time.Time) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT id FROM join_requests WHERE tenant_id = ? AND domain = ? AND state = 'pending' AND expires > ? ORDER BY id`,
		tenantID, domain, ms(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// PendingJoinsByTenant counts every tenant's pending requests that have not
// expired (the preview of turning features.domains off).
func PendingJoinsByTenant(ctx context.Context, q Q, now time.Time) (map[string]int, error) {
	rows, err := q.Query(ctx, `SELECT tenant_id, COUNT(*) FROM join_requests WHERE state = 'pending' AND expires > ? GROUP BY tenant_id`, ms(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var t string
		var n int
		if err := rows.Scan(&t, &n); err != nil {
			return nil, err
		}
		out[t] = n
	}
	return out, rows.Err()
}

// JoinKeep is how long a request that is no longer pending is kept, for its
// tenant's list and its waiting page.
const JoinKeep = 30 * 24 * time.Hour

// SweepJoinRequests writes the pending requests that have expired expired
// (their password hash goes), and deletes those that ended more than
// JoinKeep before now.
func SweepJoinRequests(ctx context.Context, q Q, now time.Time) error {
	if _, err := q.Exec(ctx, `UPDATE join_requests SET state = 'expired', password_hash = '' WHERE state = 'pending' AND expires <= ?`, ms(now)); err != nil {
		return err
	}
	cut := ms(now.Add(-JoinKeep))
	_, err := q.Exec(ctx, `DELETE FROM join_requests WHERE state <> 'pending' AND (decided_at > 0 AND decided_at < ? OR decided_at = 0 AND expires < ?)`, cut, cut)
	return err
}
