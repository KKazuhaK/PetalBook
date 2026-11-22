package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"time"

	"modernc.org/sqlite"
)

// AdminSpace is a space as the administration list shows it.
type AdminSpace struct {
	ID       string
	Name     string
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
}

// AdminOwner is who owns a space.
type AdminOwner struct {
	ID, Username, Name string
}

// SpaceFilter picks a page of ListAllSpaces.
type SpaceFilter struct {
	// Query keeps the spaces whose name contains it, ignoring case.
	Query string
	// Dialect is the database in use (Store.Dialect). On SQLite the name is
	// folded by a function the store registers, so that case is ignored for all
	// of Unicode and not only ASCII, which is all SQLite's own LOWER does; on
	// PostgreSQL LOWER follows the database's locale (the C locale folds ASCII
	// only). Either way it is the same single query and page.
	Dialect Dialect
	// ID keeps only the space with this id: the same row the list has, found
	// directly.
	ID string
	// After is the id the page continues after ("" for the first page).
	After string
	// Limit is the page size; at most one more row is read to learn whether
	// there is another page.
	Limit int
}

// sqliteLower is the name of the function that lower-cases text on SQLite
// (registered in init), for case-insensitive search beyond ASCII.
const sqliteLower = "petalbook_lower"

func init() {
	sqlite.MustRegisterDeterministicScalarFunction(sqliteLower, 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		switch v := args[0].(type) {
		case string:
			return strings.ToLower(v), nil
		case []byte:
			return strings.ToLower(string(v)), nil
		}
		return args[0], nil
	})
}

// likeEscape makes s a literal inside a LIKE pattern (ESCAPE '\').
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// ListAllSpaces returns one page of every space on the server, in id order (so
// by creation time), with the first owner and the number of members: one
// query. The page is cut in the inner select, before the owner and the count
// are looked up, so the work is per row returned and not per space on the
// server. next is the cursor of the following page, "" at the end.
func ListAllSpaces(ctx context.Context, q Q, f SpaceFilter) (out []AdminSpace, next string, err error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	var where []string
	var args []any
	if f.After != "" {
		where = append(where, `s.id > ?`)
		args = append(args, f.After)
	}
	if f.ID != "" {
		where = append(where, `s.id = ?`)
		args = append(args, f.ID)
	}
	if f.Query != "" {
		lower := "LOWER"
		if f.Dialect == SQLite {
			lower = sqliteLower
		}
		where = append(where, lower+`(s.name) LIKE ? ESCAPE '\'`)
		args = append(args, "%"+likeEscape(strings.ToLower(f.Query))+"%")
	}
	cond := ""
	if len(where) > 0 {
		cond = "WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, f.Limit+1)
	rows, err := q.Query(ctx, `SELECT p.id, p.name, p.personal_of, p.inbox, p.created, p.disabled, p.size_bytes, p.size_at, p.members, u.id, u.username, u.name
		FROM (SELECT s.id, s.name, s.personal_of, s.inbox, s.created, s.disabled, s.size_bytes, s.size_at,
			(SELECT COUNT(*) FROM space_members m WHERE m.space_id = s.id) AS members,
			(SELECT m.user_id FROM space_members m WHERE m.space_id = s.id AND m.role = 'owner' ORDER BY m.created, m.user_id LIMIT 1) AS owner_id
			FROM spaces s `+cond+` ORDER BY s.id LIMIT ?) p
		LEFT JOIN users u ON u.id = p.owner_id
		ORDER BY p.id`, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	for rows.Next() {
		var s AdminSpace
		var personal, ownerID, ownerUser, ownerName sql.NullString
		var created, disabled int64
		var size, sizeAt sql.NullInt64
		if err := rows.Scan(&s.ID, &s.Name, &personal, &s.Inbox, &created, &disabled, &size, &sizeAt, &s.Members, &ownerID, &ownerUser, &ownerName); err != nil {
			return nil, "", err
		}
		s.Personal, s.Created, s.Disabled = personal.Valid, fromMS(created), disabled != 0
		// A space whose first measurement failed has a size_at (it was put at the
		// back of the queue, see SkipSpaceSize) but no size: it is reported as
		// not measured, both null, never a time with nothing measured.
		if size.Valid {
			v := size.Int64
			s.SizeBytes = &v
			if sizeAt.Valid {
				t := fromMS(sizeAt.Int64)
				s.SizeAt = &t
			}
		}
		if ownerID.Valid {
			s.Owner = &AdminOwner{ID: ownerID.String, Username: ownerUser.String, Name: ownerName.String}
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(out) > f.Limit {
		out = out[:f.Limit]
		next = out[len(out)-1].ID
	}
	return out, next, nil
}

// GetAdminSpace returns one space as the list shows it (the same query, cut to
// the one id), or ErrNotFound.
func GetAdminSpace(ctx context.Context, q Q, id string) (AdminSpace, error) {
	rows, _, err := ListAllSpaces(ctx, q, SpaceFilter{ID: id, Limit: 1})
	if err != nil {
		return AdminSpace{}, err
	}
	if len(rows) == 0 {
		return AdminSpace{}, ErrNotFound
	}
	return rows[0], nil
}

// SetSpaceDisabled stops or allows opening a space.
func SetSpaceDisabled(ctx context.Context, q Q, id string, disabled bool) error {
	res, err := q.Exec(ctx, `UPDATE spaces SET disabled = ? WHERE id = ?`, b2i(disabled), id)
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

// SetSpaceSize stores a measurement of the space's directory.
func SetSpaceSize(ctx context.Context, q Q, id string, bytes int64, at time.Time) error {
	res, err := q.Exec(ctx, `UPDATE spaces SET size_bytes = ?, size_at = ? WHERE id = ?`, bytes, ms(at), id)
	return oneRow(res, err)
}

// SkipSpaceSize records that measuring failed at this time, keeping the
// earlier size: the space goes to the back of the queue instead of being tried
// again at the next turn, for ever. For a space never measured this sets the
// time but no size, which the list reports as not measured (both null).
func SkipSpaceSize(ctx context.Context, q Q, id string, at time.Time) error {
	res, err := q.Exec(ctx, `UPDATE spaces SET size_at = ? WHERE id = ?`, ms(at), id)
	return oneRow(res, err)
}

// NextSpaceToMeasure returns the space that was measured longest ago, if it
// was before olderThan: one never measured first. ok is false when every space
// is recent enough. Two index lookups.
func NextSpaceToMeasure(ctx context.Context, q Q, olderThan time.Time) (id string, ok bool, err error) {
	err = q.QueryRow(ctx, `SELECT id FROM spaces WHERE size_at IS NULL ORDER BY id LIMIT 1`).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}
	err = q.QueryRow(ctx, `SELECT id FROM spaces WHERE size_at < ? ORDER BY size_at, id LIMIT 1`, ms(olderThan)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return id, err == nil, err
}

// TransferOwnership makes newOwner the owner of the space, adding them as a
// member if they are none, and makes every other owner an editor. Call it in a
// transaction, after LockSpace. It returns the users whose role changed or who
// were added, for the caller to drop their cached lists.
func TransferOwnership(ctx context.Context, q Q, spaceID, newOwner string, now time.Time) (touched []string, err error) {
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
	if _, err := q.Exec(ctx, `UPDATE space_members SET role = 'editor' WHERE space_id = ? AND role = 'owner' AND user_id <> ?`, spaceID, newOwner); err != nil {
		return nil, err
	}
	// One statement, so a membership that appeared (or was there all along) is
	// made the owner's instead of failing on the key. Callers also hold
	// LockSpace, as every change of members does.
	if _, err := q.Exec(ctx, `INSERT INTO space_members(space_id, user_id, role, created) VALUES (?, ?, 'owner', ?)
		ON CONFLICT (space_id, user_id) DO UPDATE SET role = 'owner'`, spaceID, newOwner, ms(now)); err != nil {
		return nil, err
	}
	return append(touched, newOwner), nil
}

// DeleteSpace removes a space's rows: its drafts, remote, members and the
// space itself. Call it in a transaction. It deletes nothing on disk. It
// returns who the members were, for the caller to drop their cached lists.
func DeleteSpace(ctx context.Context, q Q, id string) (members []string, err error) {
	if members, err = SpaceMemberIDs(ctx, q, id); err != nil {
		return nil, err
	}
	for _, stmt := range []string{
		`DELETE FROM drafts WHERE space_id = ?`,
		`DELETE FROM remotes WHERE space_id = ?`,
		`DELETE FROM space_members WHERE space_id = ?`,
	} {
		if _, err := q.Exec(ctx, stmt, id); err != nil {
			return nil, err
		}
	}
	res, err := q.Exec(ctx, `DELETE FROM spaces WHERE id = ?`, id)
	return members, oneRow(res, err)
}

// CountSpaces and CountUsers are the totals for the status page.
func CountSpaces(ctx context.Context, q Q) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(*) FROM spaces`).Scan(&n)
	return
}

func CountUsers(ctx context.Context, q Q) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return
}
