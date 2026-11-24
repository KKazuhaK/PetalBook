package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"time"
)

// Draft is one user's unsaved version of a page, kept apart from the file.
type Draft struct {
	SpaceID string
	PageID  string
	UserID  string
	// Version is opaque and new every time the draft is stored.
	Version string
	BaseRev string
	Meta    string // JSON object, keys in header order
	Body    string
	Created time.Time // when it was first stored (reset when the base changes)
	Updated time.Time
	Held    bool      // could not be applied; the sweeper skips it until it is stored again
	Due     time.Time // when the sweeper should apply it
}

const draftCols = `space_id, page_id, user_id, version, base_rev, meta, body, created, updated, held, due`

func scanDraft(sc interface{ Scan(...any) error }) (*Draft, error) {
	var d Draft
	var created, updated, held, due int64
	if err := sc.Scan(&d.SpaceID, &d.PageID, &d.UserID, &d.Version, &d.BaseRev, &d.Meta, &d.Body, &created, &updated, &held, &due); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	d.Created, d.Updated, d.Held, d.Due = fromMS(created), fromMS(updated), held != 0, fromMS(due)
	return &d, nil
}

// GetDraft reads one draft by its primary key.
func GetDraft(ctx context.Context, q Q, spaceID, pageID, userID string) (*Draft, error) {
	return scanDraft(q.QueryRow(ctx, `SELECT `+draftCols+` FROM drafts WHERE space_id = ? AND page_id = ? AND user_id = ?`, spaceID, pageID, userID))
}

// UserDrafts reads one user's drafts of several pages of a space in one query,
// by page id. A page without a draft of theirs is not in the answer.
func UserDrafts(ctx context.Context, q Q, spaceID, userID string, pageIDs []string) (map[string]*Draft, error) {
	out := map[string]*Draft{}
	if len(pageIDs) == 0 {
		return out, nil
	}
	args := []any{spaceID, userID}
	for _, id := range pageIDs {
		args = append(args, id)
	}
	rows, err := q.Query(ctx, `SELECT `+draftCols+` FROM drafts WHERE space_id = ? AND user_id = ? AND page_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(pageIDs)), ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		d, err := scanDraft(rows)
		if err != nil {
			return nil, err
		}
		out[d.PageID] = d
	}
	return out, rows.Err()
}

// DraftConflict means the draft on the server is not the version the caller
// expected. Current is the draft there is, or nil when there is none.
type DraftConflict struct{ Current *Draft }

func (*DraftConflict) Error() string { return "the draft on the server is not the version expected" }

func newVersion() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("store: no randomness: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// testHookAfterDraftRead is called between PutDraft's read and its write
// (tests make two stores meet there).
var testHookAfterDraftRead func()

// PutDraft stores a draft that replaces version prev ("" when the caller knows
// of none), or fails with *DraftConflict and stores nothing. The check and the
// write are one statement, so two stores racing for the same draft cannot both
// win, on either database. Every stored draft gets a fresh random version.
//
// created is kept while the draft goes on from the same base and was not held,
// so the "at most max after the first change" rule counts from the first
// change; a new base or a held draft starts the count again. idle and max
// decide when it falls due.
func PutDraft(ctx context.Context, q Q, d Draft, prev string, idle, max time.Duration) (Draft, error) {
	old, err := GetDraft(ctx, q, d.SpaceID, d.PageID, d.UserID)
	if errors.Is(err, ErrNotFound) {
		old, err = nil, nil
	}
	if err != nil {
		return Draft{}, err
	}
	if (prev == "" && old != nil) || (prev != "" && (old == nil || old.Version != prev)) {
		return Draft{}, &DraftConflict{Current: old}
	}
	d.Created = d.Updated
	if old != nil && old.BaseRev == d.BaseRev && !old.Held {
		d.Created = old.Created
	}
	d.Version, d.Held = newVersion(), false
	d.Due = dueTime(d.Created, d.Updated, idle, max)
	if testHookAfterDraftRead != nil {
		testHookAfterDraftRead()
	}
	var res sql.Result
	if old == nil {
		res, err = q.Exec(ctx, `INSERT INTO drafts(`+draftCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)
			ON CONFLICT (space_id, page_id, user_id) DO NOTHING`,
			d.SpaceID, d.PageID, d.UserID, d.Version, d.BaseRev, d.Meta, d.Body, ms(d.Created), ms(d.Updated), ms(d.Due))
	} else {
		res, err = q.Exec(ctx, `UPDATE drafts SET version = ?, base_rev = ?, meta = ?, body = ?, created = ?, updated = ?, held = 0, due = ?
			WHERE space_id = ? AND page_id = ? AND user_id = ? AND version = ?`,
			d.Version, d.BaseRev, d.Meta, d.Body, ms(d.Created), ms(d.Updated), ms(d.Due), d.SpaceID, d.PageID, d.UserID, prev)
	}
	if err != nil {
		return Draft{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Someone got in between the read and the write.
		cur, err := GetDraft(ctx, q, d.SpaceID, d.PageID, d.UserID)
		if errors.Is(err, ErrNotFound) {
			cur, err = nil, nil
		}
		if err != nil {
			return Draft{}, err
		}
		return Draft{}, &DraftConflict{Current: cur}
	}
	return d, nil
}

func dueTime(created, updated time.Time, idle, max time.Duration) time.Time {
	due := updated.Add(idle)
	if m := created.Add(max); m.Before(due) {
		due = m
	}
	return due
}

// DeleteDraft removes one draft, whether or not there was one.
func DeleteDraft(ctx context.Context, q Q, spaceID, pageID, userID string) error {
	_, err := q.Exec(ctx, `DELETE FROM drafts WHERE space_id = ? AND page_id = ? AND user_id = ?`, spaceID, pageID, userID)
	return err
}

// DeleteDraftVersion removes the draft only if it is that version, and
// reports whether it did. A newer version (an edit made while the draft was
// being applied) is left alone.
func DeleteDraftVersion(ctx context.Context, q Q, spaceID, pageID, userID, version string) (bool, error) {
	res, err := q.Exec(ctx, `DELETE FROM drafts WHERE space_id = ? AND page_id = ? AND user_id = ? AND version = ?`, spaceID, pageID, userID, version)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// DeletePageDrafts removes every user's draft of a page (the page was deleted).
func DeletePageDrafts(ctx context.Context, q Q, spaceID, pageID string) error {
	_, err := q.Exec(ctx, `DELETE FROM drafts WHERE space_id = ? AND page_id = ?`, spaceID, pageID)
	return err
}

// HoldDraft marks a draft the sweeper must skip until it is stored again.
// Only that version is held; a newer one is left alone.
func HoldDraft(ctx context.Context, q Q, spaceID, pageID, userID, version string) error {
	_, err := q.Exec(ctx, `UPDATE drafts SET held = 1 WHERE space_id = ? AND page_id = ? AND user_id = ? AND version = ?`, spaceID, pageID, userID, version)
	return err
}

// DelayDraft pushes the due time of a draft that failed for a reason that may pass.
func DelayDraft(ctx context.Context, q Q, spaceID, pageID, userID, version string, until time.Time) error {
	_, err := q.Exec(ctx, `UPDATE drafts SET due = ? WHERE space_id = ? AND page_id = ? AND user_id = ? AND version = ?`, ms(until), spaceID, pageID, userID, version)
	return err
}

// DueDrafts returns up to limit drafts that are due at now, oldest first: one
// range scan of the (held, due) index.
func DueDrafts(ctx context.Context, q Q, now time.Time, limit int) ([]Draft, error) {
	rows, err := q.Query(ctx, `SELECT `+draftCols+` FROM drafts WHERE held = 0 AND due <= ? ORDER BY due LIMIT ?`, ms(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Draft
	for rows.Next() {
		d, err := scanDraft(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// RescheduleDrafts recomputes the due time of every waiting draft with the
// idle and max in force now, so changing the flags applies to drafts stored
// by an earlier run.
func RescheduleDrafts(ctx context.Context, q Q, idle, max time.Duration) error {
	_, err := q.Exec(ctx, `UPDATE drafts SET due = CASE WHEN created + ? < updated + ? THEN created + ? ELSE updated + ? END WHERE held = 0`,
		max.Milliseconds(), idle.Milliseconds(), max.Milliseconds(), idle.Milliseconds())
	return err
}

// DraftInfo describes a draft without its text.
type DraftInfo struct {
	SpaceID  string
	PageID   string
	UserID   string
	Username string
	Version  string
	BaseRev  string
	Updated  time.Time
	Chars    int
}

// ListHeldDrafts lists the drafts the sweeper has set aside (stale, or for a
// page that is gone), oldest first, for an administrator to look at.
func ListHeldDrafts(ctx context.Context, q Q) ([]DraftInfo, error) {
	rows, err := q.Query(ctx, `SELECT d.space_id, d.page_id, d.user_id, u.username, d.version, d.base_rev, d.updated, LENGTH(d.body)
		FROM drafts d JOIN users u ON u.id = d.user_id WHERE d.held = 1 ORDER BY d.updated, d.space_id, d.page_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DraftInfo
	for rows.Next() {
		var d DraftInfo
		var updated int64
		if err := rows.Scan(&d.SpaceID, &d.PageID, &d.UserID, &d.Username, &d.Version, &d.BaseRev, &updated, &d.Chars); err != nil {
			return nil, err
		}
		d.Updated = fromMS(updated)
		out = append(out, d)
	}
	return out, rows.Err()
}

// PageDrafts returns every user's draft of one page, with the username.
func PageDrafts(ctx context.Context, q Q, spaceID, pageID string) ([]Draft, []string, error) {
	rows, err := q.Query(ctx, `SELECT `+prefixed("d", draftCols)+`, u.username FROM drafts d JOIN users u ON u.id = d.user_id
		WHERE d.space_id = ? AND d.page_id = ? ORDER BY d.updated`, spaceID, pageID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var ds []Draft
	var names []string
	for rows.Next() {
		var d Draft
		var created, updated, held, due int64
		var name string
		if err := rows.Scan(&d.SpaceID, &d.PageID, &d.UserID, &d.Version, &d.BaseRev, &d.Meta, &d.Body, &created, &updated, &held, &due, &name); err != nil {
			return nil, nil, err
		}
		d.Created, d.Updated, d.Held, d.Due = fromMS(created), fromMS(updated), held != 0, fromMS(due)
		ds, names = append(ds, d), append(names, name)
	}
	return ds, names, rows.Err()
}

func prefixed(alias, cols string) string {
	parts := strings.Split(cols, ", ")
	for i, c := range parts {
		parts[i] = alias + "." + c
	}
	return strings.Join(parts, ", ")
}

// SpaceDrafts returns every user's drafts in a space, held or not, with the
// username (empty if the user is gone), oldest first. It reads the texts, so
// it is for the rare deletion of a space, never for a request.
func SpaceDrafts(ctx context.Context, q Q, spaceID string) ([]Draft, []string, error) {
	rows, err := q.Query(ctx, `SELECT `+prefixed("d", draftCols)+`, COALESCE(u.username, '') FROM drafts d LEFT JOIN users u ON u.id = d.user_id
		WHERE d.space_id = ? ORDER BY d.updated, d.page_id, d.user_id`, spaceID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var ds []Draft
	var names []string
	for rows.Next() {
		var d Draft
		var created, updated, held, due int64
		var name string
		if err := rows.Scan(&d.SpaceID, &d.PageID, &d.UserID, &d.Version, &d.BaseRev, &d.Meta, &d.Body, &created, &updated, &held, &due, &name); err != nil {
			return nil, nil, err
		}
		d.Created, d.Updated, d.Held, d.Due = fromMS(created), fromMS(updated), held != 0, fromMS(due)
		ds, names = append(ds, d), append(names, name)
	}
	return ds, names, rows.Err()
}
