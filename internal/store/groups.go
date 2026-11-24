package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"
)

// Groups (milestone 3.2). Every list here is one query a page, cut in an inner
// select so the work is per row returned. Names and usernames are ordered
// bytewise on both databases (the columns are COLLATE "C" on PostgreSQL), so a
// cursor means the same on both.

// Group sources.
const (
	GroupLocal  = "local"
	GroupSynced = "synced"
)

// Group is a group with its two counts.
type Group struct {
	ID, Name, Description, Source string
	// TenantID is the tenant the group belongs to (names are unique within it).
	TenantID         string
	People, Spaces   int
	Created, Updated time.Time
}

const groupCols = `g.id, g.name, g.description, g.source, g.created, g.updated, g.tenant_id,
	(SELECT COUNT(DISTINCT gm.user_id) FROM group_members gm WHERE gm.group_id = g.id),
	(SELECT COUNT(*) FROM space_groups sg WHERE sg.group_id = g.id)`

func scanGroup(sc interface{ Scan(...any) error }) (*Group, error) {
	var g Group
	var cr, up int64
	if err := sc.Scan(&g.ID, &g.Name, &g.Description, &g.Source, &cr, &up, &g.TenantID, &g.People, &g.Spaces); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	g.Created, g.Updated = fromMS(cr), fromMS(up)
	return &g, nil
}

// InsertGroup adds a group; a taken name is a unique violation.
func InsertGroup(ctx context.Context, q Q, g Group) error {
	if g.TenantID == "" {
		g.TenantID = DefaultTenantID
	}
	_, err := q.Exec(ctx, `INSERT INTO groups(id, name, name_key, description, source, created, updated, tenant_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		g.ID, g.Name, FoldName(g.Name), g.Description, g.Source, ms(g.Created), ms(g.Updated), g.TenantID)
	return err
}

// GetGroup returns a group with its counts, or ErrNotFound (also for a group
// of a tenant outside the scope).
func GetGroup(ctx context.Context, q Q, sc Scope, id string) (*Group, error) {
	cond, args := sc.cond("g.tenant_id")
	return scanGroup(q.QueryRow(ctx, `SELECT `+groupCols+` FROM groups g WHERE g.id = ? AND `+cond, append([]any{id}, args...)...))
}

// GroupSourceOf returns a group's source, or ErrNotFound. One primary-key lookup.
func GroupSourceOf(ctx context.Context, q Q, id string) (string, error) {
	var s string
	err := q.QueryRow(ctx, `SELECT source FROM groups WHERE id = ?`, id).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return s, err
}

// CountGroups counts the groups in the scope.
func CountGroups(ctx context.Context, q Q, sc Scope) (n int, err error) {
	cond, args := sc.cond("tenant_id")
	err = q.QueryRow(ctx, `SELECT COUNT(*) FROM groups WHERE `+cond, args...).Scan(&n)
	return
}

// UpdateGroup renames a group and sets its description.
func UpdateGroup(ctx context.Context, q Q, id, name, description string, now time.Time) error {
	res, err := q.Exec(ctx, `UPDATE groups SET name = ?, name_key = ?, description = ?, updated = ? WHERE id = ?`, name, FoldName(name), description, ms(now), id)
	return oneRow(res, err)
}

// DeleteGroup deletes a group; its memberships and its places in spaces go
// with it (the keys cascade). Nothing else is deleted.
func DeleteGroup(ctx context.Context, q Q, id string) error {
	res, err := q.Exec(ctx, `DELETE FROM groups WHERE id = ?`, id)
	return oneRow(res, err)
}

// GroupFilter picks a page of ListGroups.
type GroupFilter struct {
	Query, Source, UserID string
	After                 string // the folded name the page continues after
	Limit                 int
}

// ListGroups returns a page of groups by name, and the cursor of the next.
func ListGroups(ctx context.Context, q Q, d Dialect, sc Scope, f GroupFilter) (out []Group, next string, err error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	var where []string
	var args []any
	sCond, sArgs := sc.cond("g.tenant_id")
	where, args = append(where, sCond), append(args, sArgs...)
	if f.Query != "" {
		where = append(where, `g.name_key LIKE ? ESCAPE '\'`)
		args = append(args, "%"+likeEscape(FoldName(f.Query))+"%")
	}
	if f.Source != "" {
		where = append(where, `g.source = ?`)
		args = append(args, f.Source)
	}
	if f.UserID != "" {
		where = append(where, `g.id IN (SELECT group_id FROM group_members WHERE user_id = ?)`)
		args = append(args, f.UserID)
	}
	if f.After != "" {
		where = append(where, `g.name_key > ?`)
		args = append(args, f.After)
	}
	cond := ""
	if len(where) > 0 {
		cond = "WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, f.Limit+1)
	rows, err := q.Query(ctx, `SELECT `+groupCols+` FROM groups g `+cond+` ORDER BY g.name_key LIMIT ?`, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, *g)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(out) > f.Limit {
		out = out[:f.Limit]
		next = FoldName(out[len(out)-1].Name)
	}
	return out, next, nil
}

// GroupRef is a group as everyone may see it.
type GroupRef struct{ ID, Name, Source string }

// SearchGroups finds groups whose name contains q, by name: for choosing one.
func SearchGroups(ctx context.Context, q Q, d Dialect, sc Scope, query string, limit int) ([]GroupRef, error) {
	cond, cargs := sc.cond("tenant_id")
	rows, err := q.Query(ctx, `SELECT id, name, source FROM groups WHERE `+cond+` AND name_key LIKE ? ESCAPE '\' ORDER BY name_key LIMIT ?`,
		append(cargs, "%"+likeEscape(FoldName(query))+"%", limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GroupRef{}
	for rows.Next() {
		var g GroupRef
		if err := rows.Scan(&g.ID, &g.Name, &g.Source); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// UserGroups lists the groups a user is in, by name.
func UserGroups(ctx context.Context, q Q, userID string) ([]GroupRef, error) {
	rows, err := q.Query(ctx, `SELECT g.id, g.name, g.source FROM groups g
		WHERE g.id IN (SELECT group_id FROM group_members WHERE user_id = ?)`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GroupRef{}
	for rows.Next() {
		var g GroupRef
		if err := rows.Scan(&g.ID, &g.Name, &g.Source); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortRefs(out)
	return out, nil
}

func sortRefs(l []GroupRef) {
	sort.Slice(l, func(i, j int) bool {
		a, b := FoldName(l[i].Name), FoldName(l[j].Name)
		if a != b {
			return a < b
		}
		return l[i].ID < l[j].ID
	})
}

// GroupUserIDs lists the people in a group: one indexed query, for dropping
// their cached lists.
func GroupUserIDs(ctx context.Context, q Q, groupID string) ([]string, error) {
	return strCol(ctx, q, `SELECT DISTINCT user_id FROM group_members WHERE group_id = ?`, groupID)
}

// SpaceGroupUserIDs lists the people who reach a space through its groups.
func SpaceGroupUserIDs(ctx context.Context, q Q, spaceID string) ([]string, error) {
	return strCol(ctx, q, `SELECT DISTINCT gm.user_id FROM space_groups sg JOIN group_members gm ON gm.group_id = sg.group_id WHERE sg.space_id = ?`, spaceID)
}

func strCol(ctx context.Context, q Q, query string, args ...any) ([]string, error) {
	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GroupSpaceRef is a space a group is a member of.
type GroupSpaceRef struct{ ID, Name, Role string }

// ListGroupSpaces pages the spaces a group is a member of, by name. The cursor
// is "folded name, NUL, id".
func ListGroupSpaces(ctx context.Context, q Q, d Dialect, groupID, after string, limit int) (out []GroupSpaceRef, next string, err error) {
	if limit <= 0 {
		limit = 50
	}
	args := []any{groupID}
	cond := ""
	if after != "" {
		key, id, _ := strings.Cut(after, "\x00")
		cond = ` AND (s.name_key > ? OR (s.name_key = ? AND s.id > ?))`
		args = append(args, key, key, id)
	}
	args = append(args, limit+1)
	rows, err := q.Query(ctx, `SELECT s.id, s.name, sg.role, s.name_key FROM space_groups sg JOIN spaces s ON s.id = sg.space_id
		WHERE sg.group_id = ?`+cond+` ORDER BY s.name_key, s.id LIMIT ?`, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var r GroupSpaceRef
		var k string
		if err := rows.Scan(&r.ID, &r.Name, &r.Role, &k); err != nil {
			return nil, "", err
		}
		out, keys = append(out, r), append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(out) > limit {
		out = out[:limit]
		next = keys[limit-1] + "\x00" + out[limit-1].ID
	}
	return out, next, nil
}

// CountGroupSpaces counts the spaces a group is in, and CountGroupMappings is
// left to the providers (the mapping lives in their settings).
func CountGroupSpaces(ctx context.Context, q Q, groupID string) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(*) FROM space_groups WHERE group_id = ?`, groupID).Scan(&n)
	return
}

// --- people of a group -------------------------------------------------------------

// GroupPerson is one person of a group with where each membership came from.
type GroupPerson struct {
	UserID, Username, Name string
	Disabled               bool
	From                   []GroupFrom
	Added                  time.Time
}

// GroupFrom is the provider and claim value a synced membership came from.
type GroupFrom struct{ Provider, Value string }

// ListGroupPeople pages the people of a group by username: one query.
func ListGroupPeople(ctx context.Context, q Q, d Dialect, groupID, query, after string, limit int) (out []GroupPerson, next string, err error) {
	if limit <= 0 {
		limit = 50
	}
	where := `u.deleted_at IS NULL AND u.id IN (SELECT user_id FROM group_members WHERE group_id = ?)`
	args := []any{groupID}
	if query != "" {
		c, a := personMatches("u.", query)
		where += ` AND ` + c
		args = append(args, a...)
	}
	if after != "" {
		where += ` AND ` + `u.username` + ` > ?`
		args = append(args, after)
	}
	args = append(args, limit+1, groupID)
	rows, err := q.Query(ctx, `SELECT p.id, p.username, p.name, p.disabled, gm.provider, gm.value, gm.created
		FROM (SELECT u.id, u.username, u.name, u.disabled FROM users u WHERE `+where+` ORDER BY u.username LIMIT ?) p
		JOIN group_members gm ON gm.user_id = p.id AND gm.group_id = ?
		ORDER BY p.username, gm.provider, gm.value`, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	for rows.Next() {
		var id, un, name, prov, val string
		var dis, cr int64
		if err := rows.Scan(&id, &un, &name, &dis, &prov, &val, &cr); err != nil {
			return nil, "", err
		}
		if n := len(out); n == 0 || out[n-1].UserID != id {
			out = append(out, GroupPerson{UserID: id, Username: un, Name: name, Disabled: dis != 0, From: []GroupFrom{}, Added: fromMS(cr)})
		}
		p := &out[len(out)-1]
		if prov != "" {
			p.From = append(p.From, GroupFrom{prov, val})
		}
		if t := fromMS(cr); t.Before(p.Added) {
			p.Added = t
		}
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].Username
	}
	return out, next, nil
}

// ExistingUsers returns which of ids are users, in a set.
func ExistingUsers(ctx context.Context, q Q, sc Scope, ids []string) (map[string]bool, error) {
	have := map[string]bool{}
	cond, cargs := sc.cond("tenant_id")
	for i := 0; i < len(ids); i += 200 {
		chunk := ids[i:min(i+200, len(ids))]
		ph := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, len(chunk), len(chunk)+len(cargs))
		for j, id := range chunk {
			args[j] = id
		}
		args = append(args, cargs...)
		got, err := strCol(ctx, q, `SELECT id FROM users WHERE deleted_at IS NULL AND id IN (`+ph+`) AND `+cond, args...)
		if err != nil {
			return nil, err
		}
		for _, id := range got {
			have[id] = true
		}
	}
	return have, nil
}

// LocalMembers returns which of ids are in the (local) group already, and for
// each of the others how many groups they are in: what the limits are checked
// against before anything is written.
func LocalMembers(ctx context.Context, q Q, groupID string, ids []string) (already map[string]bool, groupsOf map[string]int, err error) {
	already, groupsOf = map[string]bool{}, map[string]int{}
	for i := 0; i < len(ids); i += 200 {
		chunk := ids[i:min(i+200, len(ids))]
		ph := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, 0, len(chunk)+1)
		args = append(args, groupID)
		for _, id := range chunk {
			args = append(args, id)
		}
		got, err := strCol(ctx, q, `SELECT user_id FROM group_members WHERE group_id = ? AND user_id IN (`+ph+`)`, args...)
		if err != nil {
			return nil, nil, err
		}
		for _, id := range got {
			already[id] = true
		}
		rows, err := q.Query(ctx, `SELECT user_id, COUNT(DISTINCT group_id) FROM group_members WHERE user_id IN (`+ph+`) GROUP BY user_id`, args[1:]...)
		if err != nil {
			return nil, nil, err
		}
		for rows.Next() {
			var id string
			var n int
			if err := rows.Scan(&id, &n); err != nil {
				rows.Close()
				return nil, nil, err
			}
			groupsOf[id] = n
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, nil, err
		}
	}
	return already, groupsOf, nil
}

// CountGroupPeople counts the people in a group.
func CountGroupPeople(ctx context.Context, q Q, groupID string) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(DISTINCT user_id) FROM group_members WHERE group_id = ?`, groupID).Scan(&n)
	return
}

// AddLocalMembers adds people to a local group (ids are all new to it).
func AddLocalMembers(ctx context.Context, q Q, groupID string, ids []string, now time.Time) error {
	for _, id := range ids {
		// The statement joins the group and the person on the same tenant: a
		// person of another tenant is not added (the callers have said so
		// before, with ExistingUsers, so this is the second wall).
		if _, err := q.Exec(ctx, `INSERT INTO group_members(group_id, user_id, provider, value, created)
			SELECT g.id, u.id, '', '', ? FROM groups g JOIN users u ON u.tenant_id = g.tenant_id WHERE g.id = ? AND u.id = ? AND u.deleted_at IS NULL
			ON CONFLICT DO NOTHING`, ms(now), groupID, id); err != nil {
			return err
		}
	}
	return nil
}

// RemoveLocalMember removes a person from a local group.
func RemoveLocalMember(ctx context.Context, q Q, groupID, userID string) error {
	res, err := q.Exec(ctx, `DELETE FROM group_members WHERE group_id = ? AND user_id = ? AND provider = ''`, groupID, userID)
	return oneRow(res, err)
}

// --- sync --------------------------------------------------------------------------

// SyncedMembership is a membership that came from a provider's claim value.
type SyncedMembership struct{ GroupID, Value string }

// UserSyncedMemberships reads, in one query, what a provider has brought a person.
func UserSyncedMemberships(ctx context.Context, q Q, userID, provider string) ([]SyncedMembership, error) {
	rows, err := q.Query(ctx, `SELECT group_id, value FROM group_members WHERE user_id = ? AND provider = ?`, userID, provider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SyncedMembership
	for rows.Next() {
		var m SyncedMembership
		if err := rows.Scan(&m.GroupID, &m.Value); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// UserGroupCount counts the groups a person is in.
func UserGroupCount(ctx context.Context, q Q, userID string) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(DISTINCT group_id) FROM group_members WHERE user_id = ?`, userID).Scan(&n)
	return
}

// UserInGroup reports whether a person is in a group, any way.
func UserInGroup(ctx context.Context, q Q, userID, groupID string) (bool, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT COUNT(*) FROM group_members WHERE user_id = ? AND group_id = ?`, userID, groupID).Scan(&n)
	return n > 0, err
}

// ApplySynced removes and adds a person's memberships from one provider. A
// group that has been deleted meanwhile is skipped, not an error. The groups
// are locked first (see LockGroups), so a change that is taking a group's
// access away and has read its people commits before anyone joins it.
func ApplySynced(ctx context.Context, q Q, userID, provider string, remove, add []SyncedMembership, now time.Time) error {
	ids := make([]string, 0, len(remove)+len(add))
	for _, m := range remove {
		ids = append(ids, m.GroupID)
	}
	for _, m := range add {
		ids = append(ids, m.GroupID)
	}
	if err := LockGroups(ctx, q, ids); err != nil {
		return err
	}
	for _, m := range remove {
		if _, err := q.Exec(ctx, `DELETE FROM group_members WHERE group_id = ? AND user_id = ? AND provider = ? AND value = ?`, m.GroupID, userID, provider, m.Value); err != nil {
			return err
		}
	}
	for _, m := range add {
		// Same tenant only: a mapping that names a group of another tenant brings
		// nothing to this person.
		if _, err := q.Exec(ctx, `INSERT INTO group_members(group_id, user_id, provider, value, created)
			SELECT g.id, u.id, ?, ?, ? FROM groups g JOIN users u ON u.tenant_id = g.tenant_id WHERE g.id = ? AND u.id = ? AND u.deleted_at IS NULL ON CONFLICT DO NOTHING`, provider, m.Value, ms(now), m.GroupID, userID); err != nil {
			return err
		}
	}
	return nil
}

// RemoveMapped deletes the memberships a provider's value brought, except
// those into keepGroup ("" keeps none), and returns who they were.
func RemoveMapped(ctx context.Context, q Q, provider, value, keepGroup string) ([]string, error) {
	ids, err := strCol(ctx, q, `SELECT DISTINCT user_id FROM group_members WHERE provider = ? AND value = ? AND group_id <> ?`, provider, value, keepGroup)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	_, err = q.Exec(ctx, `DELETE FROM group_members WHERE provider = ? AND value = ? AND group_id <> ?`, provider, value, keepGroup)
	return ids, err
}

// RemoveProviderMemberships deletes everything a provider brought and returns who it touched.
func RemoveProviderMemberships(ctx context.Context, q Q, provider string) ([]string, error) {
	ids, err := strCol(ctx, q, `SELECT DISTINCT user_id FROM group_members WHERE provider = ?`, provider)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	_, err = q.Exec(ctx, `DELETE FROM group_members WHERE provider = ?`, provider)
	return ids, err
}

// --- groups in spaces --------------------------------------------------------------

// SpaceGroup is a group that is a member of a space.
type SpaceGroup struct {
	GroupID, Name, Source, Role string
	People                      int
	Yours                       bool
}

// ListSpaceGroups lists the groups of a space by name; yours is whether userID is in each.
func ListSpaceGroups(ctx context.Context, q Q, spaceID, userID string) ([]SpaceGroup, error) {
	rows, err := q.Query(ctx, `SELECT g.id, g.name, g.source, sg.role,
		(SELECT COUNT(DISTINCT gm.user_id) FROM group_members gm WHERE gm.group_id = g.id),
		EXISTS (SELECT 1 FROM group_members gm WHERE gm.group_id = g.id AND gm.user_id = ?)
		FROM space_groups sg JOIN groups g ON g.id = sg.group_id WHERE sg.space_id = ?`, userID, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SpaceGroup{}
	for rows.Next() {
		var g SpaceGroup
		if err := rows.Scan(&g.GroupID, &g.Name, &g.Source, &g.Role, &g.People, &g.Yours); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := FoldName(out[i].Name), FoldName(out[j].Name)
		if a != b {
			return a < b
		}
		return out[i].GroupID < out[j].GroupID
	})
	return out, nil
}

// GetSpaceGroup returns one group member of a space, or ErrNotFound.
func GetSpaceGroup(ctx context.Context, q Q, spaceID, groupID, userID string) (*SpaceGroup, error) {
	l, err := ListSpaceGroups(ctx, q, spaceID, userID)
	if err != nil {
		return nil, err
	}
	for i := range l {
		if l[i].GroupID == groupID {
			return &l[i], nil
		}
	}
	return nil, ErrNotFound
}

// SpaceGroupRole returns a group's role in a space, or ErrNotFound.
func SpaceGroupRole(ctx context.Context, q Q, spaceID, groupID string) (string, error) {
	var role string
	err := q.QueryRow(ctx, `SELECT role FROM space_groups WHERE space_id = ? AND group_id = ?`, spaceID, groupID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return role, err
}

// CountSpaceGroups counts the groups that are members of a space.
func CountSpaceGroups(ctx context.Context, q Q, spaceID string) (n int, err error) {
	err = q.QueryRow(ctx, `SELECT COUNT(*) FROM space_groups WHERE space_id = ?`, spaceID).Scan(&n)
	return
}

// AddSpaceGroup makes a group a member of a space; a group already there is a
// unique violation, and a group that does not exist ErrNotFound.
func AddSpaceGroup(ctx context.Context, q Q, spaceID, groupID, role string, now time.Time) error {
	// The space and the group are joined on the same tenant, so they are never
	// linked across tenants; no row written is ErrNotFound.
	res, err := q.Exec(ctx, `INSERT INTO space_groups(space_id, group_id, role, created)
		SELECT s.id, g.id, ?, ? FROM spaces s JOIN groups g ON g.tenant_id = s.tenant_id WHERE s.id = ? AND g.id = ?`, role, ms(now), spaceID, groupID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetSpaceGroupRole changes a group's role in a space.
func SetSpaceGroupRole(ctx context.Context, q Q, spaceID, groupID, role string) error {
	res, err := q.Exec(ctx, `UPDATE space_groups SET role = ? WHERE space_id = ? AND group_id = ?`, role, spaceID, groupID)
	return oneRow(res, err)
}

// RemoveSpaceGroup takes a group out of a space.
func RemoveSpaceGroup(ctx context.Context, q Q, spaceID, groupID string) error {
	res, err := q.Exec(ctx, `DELETE FROM space_groups WHERE space_id = ? AND group_id = ?`, spaceID, groupID)
	return oneRow(res, err)
}

// MemberViaGroups says, for the direct members of a space, the roles of the
// groups they are in that are members of it: user id to the roles (one pair
// per distinct role).
func MemberViaGroups(ctx context.Context, q Q, spaceID string) (map[string][]string, error) {
	rows, err := q.Query(ctx, `SELECT gm.user_id, sg.role FROM space_groups sg JOIN group_members gm ON gm.group_id = sg.group_id
		WHERE sg.space_id = ? AND gm.user_id IN (SELECT user_id FROM space_members WHERE space_id = ?)
		GROUP BY gm.user_id, sg.role`, spaceID, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var u, r string
		if err := rows.Scan(&u, &r); err != nil {
			return nil, err
		}
		out[u] = append(out[u], r)
	}
	return out, rows.Err()
}

// UserSpaceGroups lists the groups through which a person reaches a space.
func UserSpaceGroups(ctx context.Context, q Q, spaceID, userID string) ([]SpaceGroup, error) {
	rows, err := q.Query(ctx, `SELECT g.id, g.name, g.source, sg.role FROM space_groups sg JOIN groups g ON g.id = sg.group_id
		WHERE sg.space_id = ? AND sg.group_id IN (SELECT group_id FROM group_members WHERE user_id = ?)`, spaceID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SpaceGroup
	for rows.Next() {
		var g SpaceGroup
		if err := rows.Scan(&g.GroupID, &g.Name, &g.Source, &g.Role); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// EffectivePerson is one person who can reach a space, and how.
type EffectivePerson struct {
	UserID, Username, Name string
	DirectRole             string // "" when only through groups
	Groups                 []SpaceGroup
}

// ListEffective pages the people who can reach a space by username: one query.
// Disabled users are left out.
func ListEffective(ctx context.Context, q Q, d Dialect, spaceID, query, after string, limit int) (out []EffectivePerson, next string, err error) {
	if limit <= 0 {
		limit = 50
	}
	where := `u.deleted_at IS NULL AND u.disabled = 0 AND (u.id IN (SELECT user_id FROM space_members WHERE space_id = ?)
		OR u.id IN (SELECT gm.user_id FROM space_groups sg JOIN group_members gm ON gm.group_id = sg.group_id WHERE sg.space_id = ?))`
	args := []any{spaceID, spaceID}
	if query != "" {
		c, a := personMatches("u.", query)
		where += ` AND ` + c
		args = append(args, a...)
	}
	if after != "" {
		where += ` AND ` + `u.username` + ` > ?`
		args = append(args, after)
	}
	args = append(args, limit+1, spaceID, spaceID)
	rows, err := q.Query(ctx, `SELECT p.id, p.username, p.name, m.role, g.id, g.name, g.source, sg.role
		FROM (SELECT u.id, u.username, u.name FROM users u WHERE `+where+` ORDER BY u.username LIMIT ?) p
		LEFT JOIN space_members m ON m.space_id = ? AND m.user_id = p.id
		LEFT JOIN space_groups sg ON sg.space_id = ? AND sg.group_id IN (SELECT group_id FROM group_members WHERE user_id = p.id)
		LEFT JOIN groups g ON g.id = sg.group_id
		ORDER BY p.username`, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	for rows.Next() {
		var id, un, name string
		var role, gid, gname, gsrc, grole sql.NullString
		if err := rows.Scan(&id, &un, &name, &role, &gid, &gname, &gsrc, &grole); err != nil {
			return nil, "", err
		}
		if n := len(out); n == 0 || out[n-1].UserID != id {
			out = append(out, EffectivePerson{UserID: id, Username: un, Name: name, DirectRole: role.String, Groups: []SpaceGroup{}})
		}
		if gid.Valid {
			p := &out[len(out)-1]
			p.Groups = append(p.Groups, SpaceGroup{GroupID: gid.String, Name: gname.String, Source: gsrc.String, Role: grole.String})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].Username
	}
	for i := range out {
		g := out[i].Groups
		sort.Slice(g, func(a, b int) bool { return FoldName(g[a].Name) < FoldName(g[b].Name) })
	}
	return out, next, nil
}

// UserAccess is one row of what a person reaches: the space, the direct role
// (when a member) and the roles of their groups' places there.
type UserAccess struct {
	SpaceID, Name, Inbox string
	Personal             bool
	Role                 string // the direct role, "" when none
	Direct               bool
	GroupRole            string // one role of a group of theirs; the caller unions them
	ViaGroup             bool
}

// ListUserAccess reads, in one query, every space a person can reach: their
// direct memberships and, grouped by space and role, those of their groups.
// It is the cache fill of a person; groups are never expanded anywhere else.
func ListUserAccess(ctx context.Context, q Q, userID string) ([]UserAccess, error) {
	rows, err := q.Query(ctx, `SELECT s.id, s.name, s.personal_of, s.inbox, u.role, u.direct
		FROM (SELECT space_id, role, 1 AS direct FROM space_members WHERE user_id = ?
			UNION ALL
			SELECT sg.space_id, sg.role, 0 AS direct FROM space_groups sg
			WHERE sg.group_id IN (SELECT group_id FROM group_members WHERE user_id = ?)
			GROUP BY sg.space_id, sg.role) u
		JOIN spaces s ON s.id = u.space_id`, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserAccess
	for rows.Next() {
		var a UserAccess
		var personal sql.NullString
		var direct int64
		var role string
		if err := rows.Scan(&a.SpaceID, &a.Name, &personal, &a.Inbox, &role, &direct); err != nil {
			return nil, err
		}
		a.Personal = personal.Valid
		if direct == 1 {
			a.Direct, a.Role = true, role
		} else {
			a.ViaGroup, a.GroupRole = true, role
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// LockGroup makes the rest of the transaction the only one changing the people
// of the group (a no-op write, as LockSpace does).
func LockGroup(ctx context.Context, q Q, groupID string) error {
	res, err := q.Exec(ctx, `UPDATE groups SET id = id WHERE id = ?`, groupID)
	return oneRow(res, err)
}

// LockGroups locks several groups, in the order of their ids so that two
// transactions never wait for each other. A group that does not exist is skipped.
func LockGroups(ctx context.Context, q Q, groupIDs []string) error {
	ids := append([]string(nil), groupIDs...)
	sort.Strings(ids)
	for i, id := range ids {
		if i > 0 && ids[i-1] == id {
			continue
		}
		if err := LockGroup(ctx, q, id); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	return nil
}

// UserGroupIDs lists the groups a person is in.
func UserGroupIDs(ctx context.Context, q Q, userID string) ([]string, error) {
	return strCol(ctx, q, `SELECT DISTINCT group_id FROM group_members WHERE user_id = ?`, userID)
}

// GroupSources returns the source of each of ids that is a group.
func GroupSources(ctx context.Context, q Q, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for i := 0; i < len(ids); i += 200 {
		chunk := ids[i:min(i+200, len(ids))]
		ph := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, len(chunk))
		for j, id := range chunk {
			args[j] = id
		}
		rows, err := q.Query(ctx, `SELECT id, source FROM groups WHERE id IN (`+ph+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, src string
			if err := rows.Scan(&id, &src); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = src
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
