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
	if _, err := q.Exec(ctx, `INSERT INTO session_ends(id, reason, ended) SELECT id, ?, ? FROM sessions WHERE user_id = ? AND id <> ?
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

// EndSession records why a session ended and deletes it.
func EndSession(ctx context.Context, q Q, id, reason string, at time.Time) error {
	if _, err := q.Exec(ctx, `INSERT INTO session_ends(id, reason, ended) VALUES (?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET reason = excluded.reason, ended = excluded.ended`, id, reason, ms(at)); err != nil {
		return err
	}
	return DeleteSession(ctx, q, id)
}

// SessionEndReason is why the session of a cookie hash ended; "" when no record
// of it is kept (never a session, signed out on purpose, or older than the sweep).
func SessionEndReason(ctx context.Context, q Q, id string) (string, error) {
	var reason string
	err := q.QueryRow(ctx, `SELECT reason FROM session_ends WHERE id = ?`, id).Scan(&reason)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return reason, err
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
		if _, err := q.Exec(ctx, `INSERT INTO session_ends(id, reason, ended) SELECT id, ?, ? FROM sessions WHERE `+st.col+` < ?
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
