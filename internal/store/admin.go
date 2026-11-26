package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"golang.org/x/text/cases"
)

// AdminSpace is a space as the administration list shows it.
type AdminSpace struct {
	ID   string
	Name string
	// NameKey is the stored key the name sort orders by (the cursor keeps it).
	NameKey  string
	Personal bool
	// Inbox is the folder quick notes go to.
	Inbox    string
	Created  time.Time
	Disabled bool
	// SizeBytes and SizeAt are the last measurement (see SetSpaceSize); nil
	// until the space has been measured.
	SizeBytes *int64
	SizeAt    *time.Time
	// Owner is the first owner (the rule of SpaceOwner); nil if there is none.
	Owner   *AdminOwner
	Members int
	// TenantID is the tenant the space belongs to.
	TenantID string

	// Storage is what milestone 3.1 records of the space; CredType and
	// Provider are the type of the credential of the remote it follows and the
	// provider it goes through ("" for a local space), and OwnerExempt says an
	// owner is exempt from the requirement of a remote.
	Storage     SpaceStorage
	CredType    string
	Provider    string
	OwnerExempt bool

	// Location is where the space's bytes live (LocationServer and the others)
	// and RemoteState the state of its remote ("" without one), as stored.
	Location    string
	RemoteState string
}

// Counted reports whether the space counts toward quotas (see LocationCounts).
func (s AdminSpace) Counted() bool { return LocationCounts(s.Location) }

// AdminOwner is who owns a space.
type AdminOwner struct {
	ID, Username, Name string
}

// FoldName is a space name as it is searched: Unicode case folding, which
// makes "STRASSE" and "Straße" the same, and the two spellings of an
// i with a dot the same too. It is stored in spaces.name_key, users.name_key,
// groups.name_key and tenants.name_key.
func FoldName(s string) string { return cases.Fold().String(s) }

// likeEscape makes s a literal inside a LIKE pattern (ESCAPE '\').
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// GetAdminSpace returns one space as the list shows it (the same query, cut to
// the one id), or ErrNotFound.
func GetAdminSpace(ctx context.Context, q Q, sc Scope, id string) (AdminSpace, error) {
	rows, _, err := ListAllSpaces(ctx, q, sc, SpaceFilter{ID: id, Limit: 1})
	if err != nil {
		return AdminSpace{}, err
	}
	if len(rows) == 0 {
		return AdminSpace{}, ErrNotFound
	}
	return rows[0], nil
}

// SetSpaceDisabled stops or allows opening a space.
func SetSpaceDisabled(ctx context.Context, q Q, sc Scope, id string, disabled bool) error {
	cond, args := sc.spaceCond("tenant_id", "personal_of IS NULL")
	res, err := q.Exec(ctx, `UPDATE spaces SET disabled = ? WHERE id = ? AND `+cond, append([]any{b2i(disabled), id}, args...)...)
	return oneRow(res, err)
}

// DisabledSpaces lists the ids of the disabled spaces.
func DisabledSpaces(ctx context.Context, q Q) (map[string]bool, error) {
	rows, err := q.Query(ctx, `SELECT id FROM spaces WHERE disabled <> 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// queueMeasure puts a space in the queue of the background measurement, as
// never measured (a space that is in it already keeps its place).
func queueMeasure(ctx context.Context, q Q, spaceID string) error {
	_, err := q.Exec(ctx, `INSERT INTO space_measure(space_id, tenant_id) SELECT id, tenant_id FROM spaces WHERE id = ?
		ON CONFLICT (space_id) DO NOTHING`, spaceID)
	return err
}

// stampMeasured records when a space was measured: its place in the queue
// (size_at) and the time of its last measurement (measured_at). ErrNotFound for
// a space that is gone.
func stampMeasured(ctx context.Context, q Q, spaceID string, at time.Time) error {
	res, err := q.Exec(ctx, `INSERT INTO space_measure(space_id, tenant_id, size_at, measured_at) SELECT id, tenant_id, ?, ? FROM spaces WHERE id = ?
		ON CONFLICT (space_id) DO UPDATE SET size_at = excluded.size_at, measured_at = excluded.measured_at`, ms(at), ms(at), spaceID)
	return oneRow(res, err)
}

// stampTried records that measuring a space was tried and failed: its place in
// the queue moves (size_at), its time of last measurement does not.
func stampTried(ctx context.Context, q Q, spaceID string, at time.Time) error {
	res, err := q.Exec(ctx, `INSERT INTO space_measure(space_id, tenant_id, size_at) SELECT id, tenant_id, ? FROM spaces WHERE id = ?
		ON CONFLICT (space_id) DO UPDATE SET size_at = excluded.size_at`, ms(at), spaceID)
	return oneRow(res, err)
}

// SetSpaceSize stores a measurement of the space's directory (for the listing
// only: the quotas keep what RecordMeasurement counts). The space's row is
// written only when the size changed.
func SetSpaceSize(ctx context.Context, q Q, id string, bytes int64, at time.Time) error {
	return withSpace(ctx, q, id, func(q Q) error {
		if _, err := q.Exec(ctx, `UPDATE spaces SET size_bytes = ? WHERE id = ? AND (size_bytes IS NULL OR size_bytes <> ?)`, bytes, id, bytes); err != nil {
			return err
		}
		return stampMeasured(ctx, q, id, at)
	})
}

// SkipSpaceSize records that measuring failed at this time, keeping the
// earlier size: the space goes to the back of the queue instead of being tried
// again at the next turn, for ever. It is not a measurement: the tenant's
// measuredAt does not move. For a space never measured this sets the queue's
// time but no size, which the list reports as not measured (both null).
func SkipSpaceSize(ctx context.Context, q Q, id string, at time.Time) error {
	return stampTried(ctx, q, id, at)
}

// NextSpaceToMeasure returns the space that was measured longest ago, if it
// was before olderThan: one never measured first. ok is false when every space
// is recent enough. Two lookups of the queue's index (space_measure_due).
func NextSpaceToMeasure(ctx context.Context, q Q, olderThan time.Time) (id string, ok bool, err error) {
	err = q.QueryRow(ctx, `SELECT space_id FROM space_measure WHERE size_at IS NULL ORDER BY space_id LIMIT 1`).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}
	err = q.QueryRow(ctx, `SELECT space_id FROM space_measure WHERE size_at < ? ORDER BY size_at, space_id LIMIT 1`, ms(olderThan)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return id, err == nil, err
}

// queueMissingSpaces puts in the queue of the measurement any space that is
// not in it (a space every path that makes one puts there; this is the daily
// pass's safety net). It reports how many it added.
func queueMissingSpaces(ctx context.Context, q Q) (int, error) {
	res, err := q.Exec(ctx, `INSERT INTO space_measure(space_id, tenant_id)
		SELECT s.id, s.tenant_id FROM spaces s WHERE NOT EXISTS (SELECT 1 FROM space_measure m WHERE m.space_id = s.id)`)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// TransferOwnership makes newOwner the owner of the space, adding them as a
// member if they are none, and makes every other owner an editor. Call it in a
// transaction, after LockSpace. It returns the users whose role changed or who
// were added, for the caller to drop their cached lists. newOwner must be of
// the space's tenant, except with siteAdmin, where an administrator of the
// whole server is also accepted (the explicit takeover, 3.6): the one
// membership that may join two tenants.
func TransferOwnership(ctx context.Context, q Q, spaceID, newOwner string, now time.Time, siteAdmin bool) (touched []string, err error) {
	anyTenant := b2i(siteAdmin)
	old, err := q.Query(ctx, `SELECT user_id FROM space_members WHERE space_id = ? AND role = 'owner' AND user_id <> ?`, spaceID, newOwner)
	if err != nil {
		return nil, err
	}
	for old.Next() {
		var id string
		if err := old.Scan(&id); err != nil {
			old.Close()
			return nil, err
		}
		touched = append(touched, id)
	}
	if err := old.Err(); err != nil {
		old.Close()
		return nil, err
	}
	old.Close()
	// One statement, so a membership that appeared (or was there all along) is
	// made the owner's instead of failing on the key; it joins the space and the
	// user on the same tenant, so nobody of another tenant becomes an owner
	// (nothing is written and the answer is ErrNotFound). Callers also hold
	// LockSpace, as every change of members does.
	res, err := q.Exec(ctx, `INSERT INTO space_members(space_id, user_id, role, created)
		SELECT s.id, u.id, 'owner', ? FROM spaces s JOIN users u
			ON u.tenant_id = s.tenant_id OR (? = 1 AND u.role = 'admin' AND u.site_admin = 1)
		WHERE s.id = ? AND u.id = ? AND u.deleted_at IS NULL
		ON CONFLICT (space_id, user_id) DO UPDATE SET role = 'owner'`, ms(now), anyTenant, spaceID, newOwner)
	if err := oneRow(res, err); err != nil {
		return nil, err
	}
	if _, err := q.Exec(ctx, `UPDATE space_members SET role = 'editor' WHERE space_id = ? AND role = 'owner' AND user_id <> ?`, spaceID, newOwner); err != nil {
		return nil, err
	}
	// The space is now counted for its new owner.
	if err := settleOwner(ctx, q, spaceID); err != nil {
		return nil, err
	}
	return append(touched, newOwner), nil
}

// DeleteSpace removes a space's rows: its drafts, remote, members and the
// space itself. Call it in a transaction. It deletes nothing on disk. It
// returns who the members were, and the people of its groups, for the caller
// to drop their cached lists.
func DeleteSpace(ctx context.Context, q Q, sc Scope, id string) (members []string, err error) {
	if _, err := GetSpace(ctx, q, sc, id); err != nil {
		return nil, err
	}
	if members, err = SpaceMemberIDs(ctx, q, id); err != nil {
		return nil, err
	}
	viaGroups, err := SpaceGroupUserIDs(ctx, q, id)
	if err != nil {
		return nil, err
	}
	members = append(members, viaGroups...)
	if err := forgetSpaceUsage(ctx, q, id); err != nil {
		return nil, err
	}
	for _, stmt := range []string{
		`DELETE FROM drafts WHERE space_id = ?`,
		`DELETE FROM remotes WHERE space_id = ?`,
		`DELETE FROM asset_remotes WHERE space_id = ?`,
		`DELETE FROM favorites WHERE space_id = ?`,
		`DELETE FROM space_daily WHERE space_id = ?`,
		`DELETE FROM space_members WHERE space_id = ?`,
		`DELETE FROM space_groups WHERE space_id = ?`,
	} {
		if _, err := q.Exec(ctx, stmt, id); err != nil {
			return nil, err
		}
	}
	res, err := q.Exec(ctx, `DELETE FROM spaces WHERE id = ?`, id)
	return members, oneRow(res, err)
}

// CountSpaces and CountUsers are the totals for the status page.
func CountSpaces(ctx context.Context, q Q, sc Scope) (n int, err error) {
	cond, args := sc.cond("tenant_id")
	err = q.QueryRow(ctx, `SELECT COUNT(*) FROM spaces WHERE `+cond, args...).Scan(&n)
	return
}

func CountUsers(ctx context.Context, q Q, sc Scope) (n int, err error) {
	cond, args := sc.cond("tenant_id")
	err = q.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE deleted_at IS NULL AND `+cond, args...).Scan(&n)
	return
}
