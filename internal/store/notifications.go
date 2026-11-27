package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// The notification centre (docs/design/decisions-2026-11-24.md, 3.10). One row is one thing that
// happened to one person; it says what (Kind), about what (Ref) and the facts
// (Payload, JSON). The sentence is the web app's, written in the person's
// language, so no prose is stored.

// The kinds of notification: the registry. A kind is reserved here before any
// code makes one, so the web app and the filters know its name. Producer says
// who writes it today ("" for a kind that is reserved and made by nothing yet).
const (
	// NotifyProblems is the problems of a space the person can write in. It is
	// derived: no row, it is read from the index when the list is asked for.
	NotifyProblems = "problems"
	// NotifyInvite: the person was added to a space.
	NotifyInvite = "invite"
	// NotifyMention: someone mentioned the person. Reserved: there are no
	// mentions yet.
	NotifyMention = "mention"
	// NotifySync: the remote of a space the person owns stopped working, or
	// works again after it did not.
	NotifySync = "sync"
	// NotifyTodo: a reminder of the person's own (milestone 5.x). Reserved.
	NotifyTodo = "todo"
	// NotifySpaceTakenOver: an administrator took over a space the person
	// owned (payload {"space", "by", "reason"}; ref is the space).
	NotifySpaceTakenOver = "space_taken_over"
	// NotifySpaceTransferred: an administrator transferred a space the person
	// owned to someone else (payload as space_taken_over).
	NotifySpaceTransferred = "space_transferred"
	// NotifyAccountChanged: someone else changed how the person is known or
	// reached (payload {"fields": [name, username, email, emailVerified], "by"},
	// never the values).
	NotifyAccountChanged = "account_changed"
	// NotifyPasswordSet: an administrator gave the person a temporary password
	// (payload {"by"}).
	NotifyPasswordSet = "password_set"
	// NotifyEmailReleased: an address the person held unverified moved to the
	// account that proved it (payload {"email"}).
	NotifyEmailReleased = "email_released"
	// NotifyViewedAs: an administrator viewed the app as the person (payload
	// {"by", "reason", "until"}; ref is the view). Made by nothing while the
	// owner decides whether the person is told (api.notifyViewedPerson).
	NotifyViewedAs = "viewed_as"
	// NotifyLinkReauth: a linked account's tokens could not be renewed (the
	// grant was revoked or ended) and the person must authorise it again
	// (payload {"provider", "name", "login"}; ref is the provider). Written once
	// per link that stops working.
	NotifyLinkReauth = "link_reauth"
	// NotifyDomainLapsed: an email domain the person's organisation had
	// verified lapsed, its DNS record missing for seven days (payload
	// {"domain", "missingSince"}; ref is the domain's key). Sent to the
	// tenant's administrators.
	NotifyDomainLapsed = "domain_lapsed"
)

// NotifyKind is one entry of the registry.
type NotifyKind struct {
	Kind string
	// Derived kinds have no rows.
	Derived bool
	// Producer is the part of the server that makes the kind ("" when none does).
	Producer string
}

// NotifyKinds is every kind, in the order the web app lists its filters.
var NotifyKinds = []NotifyKind{
	{NotifyProblems, true, "the index of each space"},
	{NotifyInvite, false, "adding a member to a space"},
	{NotifyMention, false, ""},
	{NotifySync, false, "the state of a space's remote"},
	{NotifyTodo, false, ""},
	{NotifySpaceTakenOver, false, "an administrator taking a space over"},
	{NotifySpaceTransferred, false, "an administrator transferring a space"},
	{NotifyAccountChanged, false, "someone else editing the person's account"},
	{NotifyPasswordSet, false, "an administrator setting a temporary password"},
	{NotifyEmailReleased, false, "another account proving an address the person held unverified"},
	{NotifyViewedAs, false, ""},
	{NotifyLinkReauth, false, "the renewal of a linked account's tokens failing for good"},
	{NotifyDomainLapsed, false, "the daily recheck of an organisation's verified email domains"},
}

// KnownNotifyKind says whether the registry has the kind.
func KnownNotifyKind(kind string) bool {
	for _, k := range NotifyKinds {
		if k.Kind == kind {
			return true
		}
	}
	return false
}

// ErrNotifyKind: a notification of a kind the registry does not have, or a
// derived kind, which has no rows.
var ErrNotifyKind = errors.New("unknown notification kind")

// Notification is one row.
type Notification struct {
	ID      string
	UserID  string
	Tenant  string
	Kind    string
	Ref     string
	Payload string // JSON object
	Created time.Time
	Read    bool
}

var notifySeq atomic.Uint64

// Atomically runs fn in one transaction: a new one when q is the database
// itself, or q when it already is a transaction.
func Atomically(ctx context.Context, q Q, fn func(Q) error) error { return atomically(ctx, q, fn) }

// NewNotificationID is an id for a row made at t: the time as 12 hexadecimal
// digits of Unix milliseconds, then a sequence, so that ids sort by time.
func NewNotificationID(t time.Time) string {
	return fmt.Sprintf("%012x%06x", ms(t), notifySeq.Add(1)&0xffffff)
}

// NotifyKeep is how long a notification is kept at most, and NotifyKeepRead
// how long once it was read; NotifyMaxPerUser is the most rows one person has.
const (
	NotifyKeep       = 180 * 24 * time.Hour
	NotifyKeepRead   = 30 * 24 * time.Hour
	NotifyMaxPerUser = 500
)

// InsertNotification adds a row for a person (tenantID is the person's). The
// kind must be in the registry and not a derived one.
func InsertNotification(ctx context.Context, q Q, n Notification) (string, error) {
	if !KnownNotifyKind(n.Kind) {
		return "", ErrNotifyKind
	}
	for _, k := range NotifyKinds {
		if k.Kind == n.Kind && k.Derived {
			return "", ErrNotifyKind
		}
	}
	if n.UserID == "" || n.Tenant == "" {
		return "", errors.New("a notification needs a person and a tenant")
	}
	if n.Created.IsZero() {
		n.Created = time.Now()
	}
	if n.Payload == "" {
		n.Payload = "{}"
	}
	if n.ID == "" {
		n.ID = NewNotificationID(n.Created)
	}
	_, err := q.Exec(ctx, `INSERT INTO notifications(id, user_id, tenant_id, kind, ref, payload, created) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		n.ID, n.UserID, n.Tenant, n.Kind, n.Ref, n.Payload, ms(n.Created))
	if err != nil {
		return "", err
	}
	// The cap per person is kept here, where a person's rows grow: one step
	// down notifications_user finds the oldest row past it (rows are few to
	// a person, inserts few to a day), so no clean-up has to group the table.
	var cut string
	err = q.QueryRow(ctx, `SELECT id FROM notifications WHERE user_id = ? ORDER BY id DESC LIMIT 1 OFFSET ?`, n.UserID, NotifyMaxPerUser).Scan(&cut)
	if errors.Is(err, sql.ErrNoRows) {
		return n.ID, nil
	}
	if err != nil {
		return n.ID, err
	}
	_, err = q.Exec(ctx, `DELETE FROM notifications WHERE user_id = ? AND id <= ?`, n.UserID, cut)
	return n.ID, err
}

// NotifyUser adds a row for a person whose tenant is looked up (the producers
// know who, not which tenant). A person who is gone or deleted gets nothing and
// no error. payload is marshalled as JSON.
func NotifyUser(ctx context.Context, q Q, userID, kind, ref string, payload any, now time.Time) error {
	var tenant string
	err := q.QueryRow(ctx, `SELECT tenant_id FROM users WHERE id = ? AND deleted_at IS NULL`, userID).Scan(&tenant)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = InsertNotification(ctx, q, Notification{UserID: userID, Tenant: tenant, Kind: kind, Ref: ref, Payload: string(raw), Created: now})
	return err
}

// NotifyFilter narrows a list.
type NotifyFilter struct {
	Kind       string // "" for every kind
	UnreadOnly bool
	Before     string // the id the previous page ended at ("" for the first page)
	Limit      int
}

// ListNotifications returns a person's rows, newest first, one more than Limit
// at most (the caller cuts it and reads the cursor off the last row it keeps).
// It is one range scan of notifications_user. tenantID is the caller's: a row
// of another tenant is never in the answer, whatever the user id.
func ListNotifications(ctx context.Context, q Q, userID, tenantID string, f NotifyFilter) ([]Notification, error) {
	conds := []string{`user_id = ?`, `tenant_id = ?`}
	args := []any{userID, tenantID}
	if f.Kind != "" {
		conds = append(conds, `kind = ?`)
		args = append(args, f.Kind)
	}
	if f.UnreadOnly {
		conds = append(conds, `read_at IS NULL`)
	}
	if f.Before != "" {
		conds = append(conds, `id < ?`)
		args = append(args, f.Before)
	}
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 30
	}
	args = append(args, f.Limit+1)
	rows, err := q.Query(ctx, `SELECT id, user_id, tenant_id, kind, ref, payload, created, read_at FROM notifications WHERE `+
		strings.Join(conds, ` AND `)+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		var n Notification
		var created int64
		var read sql.NullInt64
		if err := rows.Scan(&n.ID, &n.UserID, &n.Tenant, &n.Kind, &n.Ref, &n.Payload, &created, &read); err != nil {
			return nil, err
		}
		n.Created, n.Read = fromMS(created), read.Valid
		out = append(out, n)
	}
	return out, rows.Err()
}

// UnreadCapped counts a person's unread rows, up to limit (the badge says
// "99+", so counting further is wasted work). It reads the partial index
// notifications_unread.
func UnreadCapped(ctx context.Context, q Q, userID, tenantID string, limit int) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT COUNT(*) FROM (SELECT 1 FROM notifications WHERE user_id = ? AND tenant_id = ? AND read_at IS NULL LIMIT ?) AS u`,
		userID, tenantID, limit).Scan(&n)
	return n, err
}

// MarkNotificationsRead marks the person's own rows with these ids as read and
// returns how many were unread before. Ids that are not theirs, or are read
// already, change nothing.
func MarkNotificationsRead(ctx context.Context, q Q, userID, tenantID string, ids []string, now time.Time) (int64, error) {
	var total int64
	for len(ids) > 0 {
		n := min(len(ids), 100)
		batch := ids[:n]
		ids = ids[n:]
		marks := strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
		args := []any{ms(now), userID, tenantID}
		for _, id := range batch {
			args = append(args, id)
		}
		res, err := q.Exec(ctx, `UPDATE notifications SET read_at = ? WHERE user_id = ? AND tenant_id = ? AND read_at IS NULL AND id IN (`+marks+`)`, args...)
		if err != nil {
			return total, err
		}
		c, _ := res.RowsAffected()
		total += c
	}
	return total, nil
}

// MarkAllNotificationsRead marks every unread row of the person (of one kind
// when kind is not "") as read.
func MarkAllNotificationsRead(ctx context.Context, q Q, userID, tenantID, kind string, now time.Time) (int64, error) {
	stmt := `UPDATE notifications SET read_at = ? WHERE user_id = ? AND tenant_id = ? AND read_at IS NULL`
	args := []any{ms(now), userID, tenantID}
	if kind != "" {
		stmt += ` AND kind = ?`
		args = append(args, kind)
	}
	res, err := q.Exec(ctx, stmt, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ListSeen returns the person's cursors of derived notifications.
func ListSeen(ctx context.Context, q Q, userID string) (map[string]string, error) {
	rows, err := q.Query(ctx, `SELECT key, sig FROM notification_seen WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, s string
		if err := rows.Scan(&k, &s); err != nil {
			return nil, err
		}
		out[k] = s
	}
	return out, rows.Err()
}

// SetSeen records that the person has read the derived notification key as it
// said sig. A later change of what it says makes it unread again.
func SetSeen(ctx context.Context, q Q, userID, key, sig string, now time.Time) error {
	_, err := q.Exec(ctx, `INSERT INTO notification_seen(user_id, key, sig, seen) VALUES (?, ?, ?, ?)
		ON CONFLICT (user_id, key) DO UPDATE SET sig = excluded.sig, seen = excluded.seen`, userID, key, sig, ms(now))
	return err
}

// ForgetSeen drops the cursors of derived notifications whose key is not in keep
// (a space that no longer has problems or that the person left).
func ForgetSeen(ctx context.Context, q Q, userID string, keep []string) error {
	stmt := `DELETE FROM notification_seen WHERE user_id = ?`
	args := []any{userID}
	if len(keep) > 0 {
		stmt += ` AND key NOT IN (` + strings.TrimSuffix(strings.Repeat("?, ", len(keep)), ", ") + `)`
		for _, k := range keep {
			args = append(args, k)
		}
	}
	_, err := q.Exec(ctx, stmt, args...)
	return err
}

// TrimNotifications is the daily clean-up, across tenants (a background job):
// rows older than NotifyKeep go, and read rows older than NotifyKeepRead go,
// in batches of AuditTrimBatch, the oldest first, as the audit log is trimmed,
// so neither holds the database for long. (The cap of NotifyMaxPerUser rows a
// person is kept when a row is added: see InsertNotification.) The ids are
// times (see NewNotificationID), so "older than" is a range of the primary key.
func TrimNotifications(ctx context.Context, q Q, now time.Time) (int64, error) {
	total, err := trimNotificationsWhere(ctx, q, `id < ?`, AuditIDBefore(now.Add(-NotifyKeep)))
	if err != nil {
		return total, err
	}
	n, err := trimNotificationsWhere(ctx, q, `read_at IS NOT NULL AND read_at < ?`, ms(now.Add(-NotifyKeepRead)))
	return total + n, err
}

func trimNotificationsWhere(ctx context.Context, q Q, cond string, bound any) (int64, error) {
	var total int64
	for {
		res, err := q.Exec(ctx, `DELETE FROM notifications WHERE id IN (SELECT id FROM notifications WHERE `+cond+` ORDER BY id LIMIT ?)`, bound, AuditTrimBatch)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
		if n < AuditTrimBatch || ctx.Err() != nil {
			return total, ctx.Err()
		}
	}
}
