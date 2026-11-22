package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"
)

// SpaceInfo is a space as one member sees it.
type SpaceInfo struct {
	ID       string
	Name     string
	Role     string // the member's role in the space
	Personal bool
	// Inbox is the folder quick notes go to.
	Inbox   string
	Created time.Time
}

// DefaultInbox is the inbox folder of a space made without one.
const DefaultInbox = "Inbox"

// InsertSpace adds a space row. personalOf is the user a personal space
// belongs to, or "". An empty inbox is DefaultInbox.
func InsertSpace(ctx context.Context, q Q, id, name, personalOf, inbox string, now time.Time) error {
	var owner any
	if personalOf != "" {
		owner = personalOf
	}
	if inbox == "" {
		inbox = DefaultInbox
	}
	_, err := q.Exec(ctx, `INSERT INTO spaces(id, name, personal_of, inbox, created) VALUES (?, ?, ?, ?, ?)`, id, name, owner, inbox, ms(now))
	return err
}

func AddMember(ctx context.Context, q Q, spaceID, userID, role string, now time.Time) error {
	_, err := q.Exec(ctx, `INSERT INTO space_members(space_id, user_id, role, created) VALUES (?, ?, ?, ?)`, spaceID, userID, role, ms(now))
	return err
}

// ListUserSpaces returns the spaces a user is a member of, the personal one
// first, then by name: one query on the member's index.
func ListUserSpaces(ctx context.Context, q Q, userID string) ([]SpaceInfo, error) {
	rows, err := q.Query(ctx, `SELECT s.id, s.name, m.role, s.personal_of, s.inbox, s.created
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
		if err := rows.Scan(&s.ID, &s.Name, &s.Role, &personal, &s.Inbox, &created); err != nil {
			return nil, err
		}
		s.Personal, s.Created = personal.Valid, fromMS(created)
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetSpace returns a space by id, with no member's role: for an administrator
// who is not a member. One primary-key lookup.
func GetSpace(ctx context.Context, q Q, id string) (*SpaceInfo, error) {
	var s SpaceInfo
	var personal sql.NullString
	var created int64
	err := q.QueryRow(ctx, `SELECT id, name, personal_of, inbox, created FROM spaces WHERE id = ?`, id).
		Scan(&s.ID, &s.Name, &personal, &s.Inbox, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	s.Personal, s.Created = personal.Valid, fromMS(created)
	return &s, nil
}

// Member is a user's membership of a space, with who they are.
type Member struct {
	UserID   string
	Username string
	Name     string
	Role     string
	Created  time.Time
}

// ListMembers returns the members of a space, owners first, then by name. One
// query on the space's index, joined to users; a space has as many members as
// people share it, not as many as the server has users.
func ListMembers(ctx context.Context, q Q, spaceID string) ([]Member, error) {
	rows, err := q.Query(ctx, `SELECT m.user_id, u.username, u.name, m.role, m.created
		FROM space_members m JOIN users u ON u.id = m.user_id
		WHERE m.space_id = ?`, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		var m Member
		var created int64
		if err := rows.Scan(&m.UserID, &m.Username, &m.Name, &m.Role, &created); err != nil {
			return nil, err
		}
		m.Created = fromMS(created)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Sorted here, not by the database, so SQLite and PostgreSQL agree on case and accents.
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.Role == "owner") != (b.Role == "owner") {
			return a.Role == "owner"
		}
		if an, bn := strings.ToLower(a.Name), strings.ToLower(b.Name); an != bn {
			return an < bn
		}
		if a.Username != b.Username {
			return a.Username < b.Username
		}
		return a.UserID < b.UserID
	})
	return out, nil
}

// GetMember returns one member with who they are, or ErrNotFound.
func GetMember(ctx context.Context, q Q, spaceID, userID string) (*Member, error) {
	var m Member
	var created int64
	err := q.QueryRow(ctx, `SELECT m.user_id, u.username, u.name, m.role, m.created
		FROM space_members m JOIN users u ON u.id = m.user_id
		WHERE m.space_id = ? AND m.user_id = ?`, spaceID, userID).Scan(&m.UserID, &m.Username, &m.Name, &m.Role, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	m.Created = fromMS(created)
	return &m, nil
}

// MemberRole returns a user's role in a space, or ErrNotFound if they are not a member.
func MemberRole(ctx context.Context, q Q, spaceID, userID string) (string, error) {
	var role string
	err := q.QueryRow(ctx, `SELECT role FROM space_members WHERE space_id = ? AND user_id = ?`, spaceID, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return role, err
}

// SetMemberRole changes a member's role.
func SetMemberRole(ctx context.Context, q Q, spaceID, userID, role string) error {
	res, err := q.Exec(ctx, `UPDATE space_members SET role = ? WHERE space_id = ? AND user_id = ?`, role, spaceID, userID)
	return oneRow(res, err)
}

// RemoveMember removes a member from a space.
func RemoveMember(ctx context.Context, q Q, spaceID, userID string) error {
	res, err := q.Exec(ctx, `DELETE FROM space_members WHERE space_id = ? AND user_id = ?`, spaceID, userID)
	return oneRow(res, err)
}

// CountOwners counts the owners of a space.
func CountOwners(ctx context.Context, q Q, spaceID string) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(*) FROM space_members WHERE space_id = ? AND role = 'owner'`, spaceID).Scan(&n)
	return
}

// CountOtherEnabledOwners counts the owners of the space other than except who
// can sign in: a disabled owner cannot act as one, so the "keep one owner" rule
// asks whether anyone else would still be able to.
func CountOtherEnabledOwners(ctx context.Context, q Q, spaceID, except string) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(*) FROM space_members m JOIN users u ON u.id = m.user_id
		WHERE m.space_id = ? AND m.role = 'owner' AND m.user_id <> ? AND u.disabled = 0`, spaceID, except).Scan(&n)
	return
}

// LockSpace makes the rest of the transaction the only one changing who is a
// member of the space: the "keep one owner" rule reads a count and then
// writes, and two people leaving at once must not both pass. A no-op write is
// the lock that works the same on both databases (PostgreSQL holds the row
// until commit; SQLite's transactions are serialized already).
func LockSpace(ctx context.Context, q Q, spaceID string) error {
	res, err := q.Exec(ctx, `UPDATE spaces SET id = id WHERE id = ?`, spaceID)
	return oneRow(res, err)
}

// RenameSpace changes a space's display name.
func RenameSpace(ctx context.Context, q Q, id, name string) error {
	res, err := q.Exec(ctx, `UPDATE spaces SET name = ? WHERE id = ?`, name, id)
	return oneRow(res, err)
}

// SetSpaceInbox changes the folder quick notes of a space go to.
func SetSpaceInbox(ctx context.Context, q Q, id, inbox string) error {
	res, err := q.Exec(ctx, `UPDATE spaces SET inbox = ? WHERE id = ?`, inbox, id)
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
