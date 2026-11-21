package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// SpaceInfo is a space as one member sees it.
type SpaceInfo struct {
	ID       string
	Name     string
	Role     string // the member's role in the space
	Personal bool
	Created  time.Time
}

// InsertSpace adds a space row. personalOf is the user a personal space belongs to, or "".
func InsertSpace(ctx context.Context, q Q, id, name, personalOf string, now time.Time) error {
	var owner any
	if personalOf != "" {
		owner = personalOf
	}
	_, err := q.Exec(ctx, `INSERT INTO spaces(id, name, personal_of, created) VALUES (?, ?, ?, ?)`, id, name, owner, ms(now))
	return err
}

func AddMember(ctx context.Context, q Q, spaceID, userID, role string, now time.Time) error {
	_, err := q.Exec(ctx, `INSERT INTO space_members(space_id, user_id, role, created) VALUES (?, ?, ?, ?)`, spaceID, userID, role, ms(now))
	return err
}

// ListUserSpaces returns the spaces a user is a member of, the personal one
// first, then by name: one query on the member's index.
func ListUserSpaces(ctx context.Context, q Q, userID string) ([]SpaceInfo, error) {
	rows, err := q.Query(ctx, `SELECT s.id, s.name, m.role, s.personal_of, s.created
		FROM space_members m JOIN spaces s ON s.id = m.space_id
		WHERE m.user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SpaceInfo
	for rows.Next() {
		var s SpaceInfo
		var personal sql.NullString
		var created int64
		if err := rows.Scan(&s.ID, &s.Name, &s.Role, &personal, &created); err != nil {
			return nil, err
		}
		s.Personal, s.Created = personal.Valid, fromMS(created)
		out = append(out, s)
	}
	return out, rows.Err()
}

// RenameSpace changes a space's display name.
func RenameSpace(ctx context.Context, q Q, id, name string) error {
	res, err := q.Exec(ctx, `UPDATE spaces SET name = ? WHERE id = ?`, name, id)
	return oneRow(res, err)
}

// SpaceMemberIDs lists the users who are members of a space.
func SpaceMemberIDs(ctx context.Context, q Q, spaceID string) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT user_id FROM space_members WHERE space_id = ?`, spaceID)
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

// SpaceOwner returns the first owner of a space (the one whose name and email
// are the Git author of its commits) and the space's name.
func SpaceOwner(ctx context.Context, q Q, spaceID string) (*User, string, error) {
	var name string
	if err := q.QueryRow(ctx, `SELECT name FROM spaces WHERE id = ?`, spaceID).Scan(&name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", ErrNotFound
		}
		return nil, "", err
	}
	u, err := scanUser(q.QueryRow(ctx, `SELECT u.id, u.username, u.name, u.email, u.role, u.disabled, u.created
		FROM space_members m JOIN users u ON u.id = m.user_id
		WHERE m.space_id = ? AND m.role = 'owner' ORDER BY m.created, u.id LIMIT 1`, spaceID))
	if errors.Is(err, ErrNotFound) {
		return nil, name, nil
	}
	return u, name, err
}

// EnsureLocal makes the built-in user and the one space of single-user mode
// exist, so drafts can refer to them like to any other user and space.
func EnsureLocal(ctx context.Context, q Q, spaceID, spaceName string, now time.Time) error {
	if _, err := q.Exec(ctx, `INSERT INTO users(`+userCols+`) VALUES (?, 'local', 'Local user', '', 'admin', 0, ?)
		ON CONFLICT (id) DO NOTHING`, LocalUserID, ms(now)); err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `INSERT INTO spaces(id, name, personal_of, created) VALUES (?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET name = excluded.name`, spaceID, spaceName, LocalUserID, ms(now)); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `INSERT INTO space_members(space_id, user_id, role, created) VALUES (?, ?, 'owner', ?)
		ON CONFLICT (space_id, user_id) DO NOTHING`, spaceID, LocalUserID, ms(now))
	return err
}

// CountOwnedSpaces counts the spaces a user is an owner of (their personal one included).
func CountOwnedSpaces(ctx context.Context, q Q, userID string) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(*) FROM space_members WHERE user_id = ? AND role = 'owner'`, userID).Scan(&n)
	return
}
