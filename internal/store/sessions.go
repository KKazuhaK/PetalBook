package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
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
	// UserAgent and Method are what the administrator's list of a person's
	// sessions shows (see the schema); empty when not known.
	UserAgent, Method string
	// Reserved for 3.7 (see the schema): the client ("web" or "app", "" is
	// "web"), the context the session is in ("" is the person's own) and the
	// proof each organization holds, by tenant id.
	Client        string
	CurrentTenant string
	Proofs        map[string]Proof
}

// Proof is how a person verified themselves to one organization in a session.
// Method is sso, domain, password, passkey or social; At and Seen are epoch
// milliseconds (when it was made, and when it was last used).
type Proof struct {
	Method   string `json:"method"`
	Provider string `json:"provider,omitempty"`
	At       int64  `json:"at"`
	Seen     int64  `json:"seen"`
}

func encodeProofs(p map[string]Proof) string {
	if len(p) == 0 {
		return "{}"
	}
	b, _ := json.Marshal(p) // a map of plain values cannot fail
	return string(b)
}

func decodeProofs(s string) map[string]Proof {
	p := map[string]Proof{}
	if s != "" {
		_ = json.Unmarshal([]byte(s), &p) // a damaged value is no proof
	}
	if p == nil {
		p = map[string]Proof{}
	}
	return p
}

// MaxUserAgent is the longest user agent a session keeps (bytes).
const MaxUserAgent = 200

func InsertSession(ctx context.Context, q Q, s Session) error {
	ua := s.UserAgent
	if len(ua) > MaxUserAgent {
		ua = strings.ToValidUTF8(ua[:MaxUserAgent], "")
	}
	client := s.Client
	if client == "" {
		client = "web"
	}
	_, err := q.Exec(ctx, `INSERT INTO sessions(id, user_id, created, last_used, held, user_agent, method, current_tenant, proofs, client) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.UserID, ms(s.Created), ms(s.LastUsed), b2i(s.Held), ua, s.Method, s.CurrentTenant, encodeProofs(s.Proofs), client)
	return err
}

// ListUserSessions is a person's sessions, the one used last first. A person has
// at most a few (TrimSessions), so the list is not paged.
func ListUserSessions(ctx context.Context, q Q, userID string) ([]Session, error) {
	rows, err := q.Query(ctx, `SELECT id, user_id, created, last_used, held, user_agent, method FROM sessions WHERE user_id = ? ORDER BY last_used DESC, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s Session
		var created, used, held int64
		if err := rows.Scan(&s.ID, &s.UserID, &created, &used, &held, &s.UserAgent, &s.Method); err != nil {
			return nil, err
		}
		s.Created, s.LastUsed, s.Held = fromMS(created), fromMS(used), held != 0
		out = append(out, s)
	}
	return out, rows.Err()
}

// EndUserSession ends one session of the user, recording reason; ErrNotFound when
// the user has no such session.
func EndUserSession(ctx context.Context, q Q, id, userID, reason string, at time.Time) error {
	res, err := q.Exec(ctx, `DELETE FROM sessions WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if err := oneRow(res, nil); err != nil {
		return err
	}
	_, err = q.Exec(ctx, `INSERT INTO session_ends(id, user_id, reason, ended) VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET reason = excluded.reason, ended = excluded.ended`, id, userID, reason, ms(at))
	return err
}

// LookupSession is the session of a cookie hash or, when there is none, why it
// ended ("" when no record is kept) with ErrNotFound: one query either way, so a
// cookie that is not a session costs one round trip, not two.
func LookupSession(ctx context.Context, q Q, id string) (*Session, string, error) {
	var userID, reason, cur, proofs, client, method sql.NullString
	var created, used, held sql.NullInt64
	err := q.QueryRow(ctx, `SELECT s.user_id, s.created, s.last_used, s.held, e.reason, s.current_tenant, s.proofs, s.client, s.method
		FROM (SELECT CAST(? AS TEXT) AS id) k
		LEFT JOIN sessions s ON s.id = k.id
		LEFT JOIN session_ends e ON e.id = k.id`, id).Scan(&userID, &created, &used, &held, &reason, &cur, &proofs, &client, &method)
	if err != nil {
		return nil, "", err
	}
	if !userID.Valid {
		return nil, reason.String, ErrNotFound
	}
	return &Session{ID: id, UserID: userID.String, Created: fromMS(created.Int64), LastUsed: fromMS(used.Int64), Held: held.Int64 != 0,
		Client: client.String, Method: method.String, CurrentTenant: cur.String, Proofs: decodeProofs(proofs.String)}, "", nil
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

// GetSessionState is a session with the fields reserved for 3.7 (client,
// current context, proofs).
func GetSessionState(ctx context.Context, q Q, id string) (*Session, error) {
	s, err := GetSession(ctx, q, id)
	if err != nil {
		return nil, err
	}
	var proofs string
	if err := q.QueryRow(ctx, `SELECT current_tenant, proofs, client FROM sessions WHERE id = ?`, id).Scan(&s.CurrentTenant, &proofs, &s.Client); err != nil {
		return nil, err
	}
	s.Proofs = decodeProofs(proofs)
	return s, nil
}

// UpdateSessionState sets the context a session is in and its proofs. ErrNotFound
// when there is no such session.
func UpdateSessionState(ctx context.Context, q Q, id, currentTenant string, proofs map[string]Proof) error {
	res, err := q.Exec(ctx, `UPDATE sessions SET current_tenant = ?, proofs = ? WHERE id = ?`, currentTenant, encodeProofs(proofs), id)
	if err != nil {
		return err
	}
	return oneRow(res, nil)
}

// TouchSession records use. Callers do it at most once an hour per session.
// It writes last_used only: the context and proofs change through
// UpdateSessionState, never from a copy that may be old.
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

// HoldSessionForPasskey keeps a session held, now for a passkey: its method
// becomes "password" (the sign-in it was), which is no hold for a new password.
func HoldSessionForPasskey(ctx context.Context, q Q, id string) error {
	_, err := q.Exec(ctx, `UPDATE sessions SET held = 1, method = 'password' WHERE id = ?`, id)
	return err
}

// ReleaseUserSessions lifts the hold of every session of the user.
func ReleaseUserSessions(ctx context.Context, q Q, userID string) error {
	_, err := q.Exec(ctx, `UPDATE sessions SET held = 0 WHERE user_id = ?`, userID)
	return err
}
