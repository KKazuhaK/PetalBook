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
}

func InsertSession(ctx context.Context, q Q, s Session) error {
	_, err := q.Exec(ctx, `INSERT INTO sessions(id, user_id, created, last_used) VALUES (?, ?, ?, ?)`, s.ID, s.UserID, ms(s.Created), ms(s.LastUsed))
	return err
}

func GetSession(ctx context.Context, q Q, id string) (*Session, error) {
	var s Session
	var created, used int64
	err := q.QueryRow(ctx, `SELECT id, user_id, created, last_used FROM sessions WHERE id = ?`, id).Scan(&s.ID, &s.UserID, &created, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	s.Created, s.LastUsed = fromMS(created), fromMS(used)
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
func DeleteUserSessions(ctx context.Context, q Q, userID, keep string) error {
	_, err := q.Exec(ctx, `DELETE FROM sessions WHERE user_id = ? AND id <> ?`, userID, keep)
	return err
}

// TrimSessions keeps only the newest n sessions of a user, so signing in over
// and over cannot grow the table without bound.
func TrimSessions(ctx context.Context, q Q, userID string, n int) error {
	_, err := q.Exec(ctx, `DELETE FROM sessions WHERE user_id = ? AND id NOT IN
		(SELECT id FROM sessions WHERE user_id = ? ORDER BY last_used DESC, id LIMIT ?)`, userID, userID, n)
	return err
}

// DeleteExpiredSessions removes sessions idle since idleBefore or older than
// createdBefore, and reports how many.
func DeleteExpiredSessions(ctx context.Context, q Q, idleBefore, createdBefore time.Time) (int64, error) {
	var total int64
	for _, st := range []struct {
		sql string
		at  time.Time
	}{
		{`DELETE FROM sessions WHERE last_used < ?`, idleBefore},
		{`DELETE FROM sessions WHERE created < ?`, createdBefore},
	} {
		res, err := q.Exec(ctx, st.sql, ms(st.at))
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}
