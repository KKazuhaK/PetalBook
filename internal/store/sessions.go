package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Session is a sign-in. Its id is the SHA-256 of the cookie value, never the value.
type Session struct {
	ID       string
	UserID   string
	Created  time.Time
	LastUsed time.Time
	// Held: the session may only register a passkey and sign out.
	Held bool
}

func InsertSession(ctx context.Context, q Q, s Session) error {
	_, err := q.Exec(ctx, `INSERT INTO sessions(id, user_id, created, last_used, held) VALUES (?, ?, ?, ?, ?)`, s.ID, s.UserID, ms(s.Created), ms(s.LastUsed), b2i(s.Held))
	return err
}

// LookupSession is the session of a cookie hash or, when there is none, why it
// ended ("" when no record is kept) with ErrNotFound: one query either way, so a
// cookie that is not a session costs one round trip, not two.
func LookupSession(ctx context.Context, q Q, id string) (*Session, string, error) {
	var userID, reason sql.NullString
	var created, used, held sql.NullInt64
	err := q.QueryRow(ctx, `SELECT s.user_id, s.created, s.last_used, s.held, e.reason
		FROM (SELECT CAST(? AS TEXT) AS id) k
		LEFT JOIN sessions s ON s.id = k.id
		LEFT JOIN session_ends e ON e.id = k.id`, id).Scan(&userID, &created, &used, &held, &reason)
	if err != nil {
		return nil, "", err
	}
	if !userID.Valid {
		return nil, reason.String, ErrNotFound
	}
	return &Session{ID: id, UserID: userID.String, Created: fromMS(created.Int64), LastUsed: fromMS(used.Int64), Held: held.Int64 != 0}, "", nil
}

func GetSession(ctx context.Context, q Q, id string) (*Session, error) {
	var s Session
	var created, used, held int64
	err := q.QueryRow(ctx, `SELECT id, user_id, created, last_used, held FROM sessions WHERE id = ?`, id).Scan(&s.ID, &s.UserID, &created, &used, &held)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	s.Created, s.LastUsed, s.Held = fromMS(created), fromMS(used), held != 0
	return &s, err
}

// TouchSession records use. Callers do it at most once an hour per session.
func TouchSession(ctx context.Context, q Q, id string, at time.Time) error {
	_, err := q.Exec(ctx, `UPDATE sessions SET last_used = ? WHERE id = ?`, ms(at), id)
	return err
}

func DeleteSession(ctx context.Context, q Q, id string) error {
	_, err := q.Exec(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	return err
}

// DeleteUserSessions ends every session of the user except keep ("" keeps none).
//
// reason says why (one of the End* constants, never ""): a row of session_ends is
// kept for each, so the sign-in page can say so when the browser comes back.
func DeleteUserSessions(ctx context.Context, q Q, userID, keep, reason string, at time.Time) error {
	if _, err := q.Exec(ctx, `INSERT INTO session_ends(id, user_id, reason, ended) SELECT id, user_id, ?, ? FROM sessions WHERE user_id = ? AND id <> ?
		ON CONFLICT(id) DO UPDATE SET reason = excluded.reason, ended = excluded.ended`, reason, ms(at), userID, keep); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `DELETE FROM sessions WHERE user_id = ? AND id <> ?`, userID, keep)
	return err
}

// Why a session ended without the person signing out.
const (
	EndExpired   = "expired"              // idle or past its absolute lifetime
	EndElsewhere = "signed_out_elsewhere" // signed out everywhere, or revoked by an administrator
	EndPassword  = "password_changed"
	EndDisabled  = "disabled"
)

// EndSession records why a session of the user ended and deletes it.
func EndSession(ctx context.Context, q Q, id, userID, reason string, at time.Time) error {
	if _, err := q.Exec(ctx, `INSERT INTO session_ends(id, user_id, reason, ended) VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET reason = excluded.reason, ended = excluded.ended`, id, userID, reason, ms(at)); err != nil {
		return err
	}
	return DeleteSession(ctx, q, id)
}

// EndTenantSessions ends every session of the people of a tenant, recording
// reason for each (disabling the tenant).
func EndTenantSessions(ctx context.Context, q Q, tenantID, reason string, at time.Time) error {
	if _, err := q.Exec(ctx, `INSERT INTO session_ends(id, user_id, reason, ended)
		SELECT s.id, s.user_id, ?, ? FROM sessions s JOIN users u ON u.id = s.user_id WHERE u.tenant_id = ?
		ON CONFLICT(id) DO UPDATE SET reason = excluded.reason, ended = excluded.ended`, reason, ms(at), tenantID); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `DELETE FROM sessions WHERE user_id IN (SELECT id FROM users WHERE tenant_id = ?)`, tenantID)
	return err
}

// DeleteUserSessionEnds forgets the records of a user's sessions that ended for
// reason (enabling a person again forgets `disabled`).
func DeleteUserSessionEnds(ctx context.Context, q Q, userID, reason string) error {
	_, err := q.Exec(ctx, `DELETE FROM session_ends WHERE user_id = ? AND reason = ?`, userID, reason)
	return err
}

// DeleteTenantSessionEnds is DeleteUserSessionEnds for every enabled person of a
// tenant (one who is disabled on their own stays so).
func DeleteTenantSessionEnds(ctx context.Context, q Q, tenantID, reason string) error {
	_, err := q.Exec(ctx, `DELETE FROM session_ends WHERE reason = ? AND user_id IN (SELECT id FROM users WHERE tenant_id = ? AND disabled = 0)`, reason, tenantID)
	return err
}

// DeleteOldSessionEnds forgets the records made before the given time.
func DeleteOldSessionEnds(ctx context.Context, q Q, before time.Time) (int64, error) {
	res, err := q.Exec(ctx, `DELETE FROM session_ends WHERE ended < ?`, ms(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// TrimSessions keeps only the newest n sessions of a user, so signing in over
// and over cannot grow the table without bound.
func TrimSessions(ctx context.Context, q Q, userID string, n int) error {
	_, err := q.Exec(ctx, `DELETE FROM sessions WHERE user_id = ? AND id NOT IN
		(SELECT id FROM sessions WHERE user_id = ? ORDER BY last_used DESC, id LIMIT ?)`, userID, userID, n)
	return err
}

// DeleteExpiredSessions removes sessions idle since idleBefore or older than
// createdBefore (recording each as ended at `at` for want of use), and reports how many.
func DeleteExpiredSessions(ctx context.Context, q Q, idleBefore, createdBefore, at time.Time) (int64, error) {
	var total int64
	for _, st := range []struct {
		col string
		at  time.Time
	}{
		{"last_used", idleBefore},
		{"created", createdBefore},
	} {
		if _, err := q.Exec(ctx, `INSERT INTO session_ends(id, user_id, reason, ended) SELECT id, user_id, ?, ? FROM sessions WHERE `+st.col+` < ?
			ON CONFLICT(id) DO NOTHING`, EndExpired, ms(at), ms(st.at)); err != nil {
			return total, err
		}
		res, err := q.Exec(ctx, `DELETE FROM sessions WHERE `+st.col+` < ?`, ms(st.at))
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}

// ReleaseSession lifts the hold of one session.
func ReleaseSession(ctx context.Context, q Q, id string) error {
	_, err := q.Exec(ctx, `UPDATE sessions SET held = 0 WHERE id = ?`, id)
	return err
}

// ReleaseUserSessions lifts the hold of every session of the user.
func ReleaseUserSessions(ctx context.Context, q Q, userID string) error {
	_, err := q.Exec(ctx, `UPDATE sessions SET held = 0 WHERE user_id = ?`, userID)
	return err
}
