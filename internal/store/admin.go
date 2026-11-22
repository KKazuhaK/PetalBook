package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// AdminSpace is a space as the administration list shows it.
type AdminSpace struct {
	ID       string
	Name     string
	Personal bool
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
	// After is the id the page continues after ("" for the first page).
	After string
	// Limit is the page size; at most one more row is read to learn whether
	// there is another page.
	Limit int
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
	if f.Query != "" {
		where = append(where, `LOWER(s.name) LIKE ? ESCAPE '\'`)
		args = append(args, "%"+likeEscape(strings.ToLower(f.Query))+"%")
	}
	cond := ""
	if len(where) > 0 {
		cond = "WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, f.Limit+1)
	rows, err := q.Query(ctx, `SELECT p.id, p.name, p.personal_of, p.created, p.disabled, p.size_bytes, p.size_at, p.members, u.id, u.username, u.name
		FROM (SELECT s.id, s.name, s.personal_of, s.created, s.disabled, s.size_bytes, s.size_at,
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
		if err := rows.Scan(&s.ID, &s.Name, &personal, &created, &disabled, &size, &sizeAt, &s.Members, &ownerID, &ownerUser, &ownerName); err != nil {
			return nil, "", err
		}
		s.Personal, s.Created, s.Disabled = personal.Valid, fromMS(created), disabled != 0
		if size.Valid {
			v := size.Int64
			s.SizeBytes = &v
		}
		if sizeAt.Valid {
			t := fromMS(sizeAt.Int64)
			s.SizeAt = &t
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
// again at the next turn, for ever.
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
	switch _, err := MemberRole(ctx, q, spaceID, newOwner); {
	case errors.Is(err, ErrNotFound):
		err = AddMember(ctx, q, spaceID, newOwner, "owner", now)
		if err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	default:
		if err := SetMemberRole(ctx, q, spaceID, newOwner, "owner"); err != nil {
			return nil, err
		}
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
