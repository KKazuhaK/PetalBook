package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"time"

	"golang.org/x/text/cases"
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
}

// AdminOwner is who owns a space.
type AdminOwner struct {
	ID, Username, Name string
}

// SpaceFilter picks a page of ListAllSpaces.
type SpaceFilter struct {
	// Query keeps the spaces whose name contains it, ignoring case.
	Query string
	// The search compares the folded name (FoldName) the space was written with,
	// so both databases find the same spaces.
	// ID keeps only the space with this id: the same row the list has, found
	// directly.
	ID string
	// Cache keeps the spaces in this cache state: "local" (no remote), "evicted",
	// "partial" or "full" (a space that follows a remote and is neither of the
	// others). "restoring" is not stored and keeps nothing.
	Cache string
	// Overdue, when set, keeps the spaces the policy asks for a remote that they
	// lack and whose deadline has passed.
	Overdue *RemotePolicy
	// After is the id the page continues after ("" for the first page).
	After string
	// Limit is the page size; at most one more row is read to learn whether
	// there is another page.
	Limit int
}

// sqliteLower is the name of the function that lower-cases text on SQLite
// (registered in init), for the search of users.
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
	sqlite.MustRegisterDeterministicScalarFunction(sqliteASCIILower, 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		switch v := args[0].(type) {
		case string:
			return asciiLowerGo(v), nil
		case []byte:
			return asciiLowerGo(string(v)), nil
		}
		return args[0], nil
	})
}

// sqliteASCIILower lower-cases A to Z and nothing else (see asciiLower).
const sqliteASCIILower = "petalbook_ascii_lower"

func asciiLowerGo(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// FoldName is a space name as it is searched: Unicode case folding, which
// makes "STRASSE" and "Straße" the same, and the two spellings of an
// i with a dot the same too. It is stored in spaces.name_key.
func FoldName(s string) string { return cases.Fold().String(s) }

// fillSpaceNameKeys folds the names of the spaces that exist (migration 7).
func fillSpaceNameKeys(ctx context.Context, q Q) error {
	rows, err := q.Query(ctx, `SELECT id, name FROM spaces`)
	if err != nil {
		return err
	}
	type row struct{ id, key string }
	var all []row
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return err
		}
		all = append(all, row{id, FoldName(name)})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range all {
		if _, err := q.Exec(ctx, `UPDATE spaces SET name_key = ? WHERE id = ?`, r.key, r.id); err != nil {
			return err
		}
	}
	return nil
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
func ListAllSpaces(ctx context.Context, q Q, sc Scope, f SpaceFilter) (out []AdminSpace, next string, err error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	var where []string
	var args []any
	sCond, sArgs := sc.spaceCond("s.tenant_id", "s.personal_of IS NULL")
	where, args = append(where, sCond), append(args, sArgs...)
	if f.After != "" {
		where = append(where, `s.id > ?`)
		args = append(args, f.After)
	}
	if f.ID != "" {
		where = append(where, `s.id = ?`)
		args = append(args, f.ID)
	}
	if f.Query != "" {
		where = append(where, `s.name_key LIKE ? ESCAPE '\'`)
		args = append(args, "%"+likeEscape(FoldName(f.Query))+"%")
	}
	hasRemote := `EXISTS (SELECT 1 FROM remotes r WHERE r.space_id = s.id AND r.url <> '')`
	switch f.Cache {
	case "":
	case "local":
		where = append(where, `NOT `+hasRemote)
	case "evicted":
		where = append(where, hasRemote+` AND s.cache_state = 'evicted'`)
	case "partial":
		where = append(where, hasRemote+` AND s.cache_state = 'partial'`)
	case "full":
		where = append(where, hasRemote+` AND s.cache_state NOT IN ('evicted', 'partial')`)
	default:
		where = append(where, `1 = 0`)
	}
	if f.Overdue != nil && f.Overdue.Active() {
		asked, aargs, dl, dargs := policyConds(*f.Overdue)
		where = append(where, asked+` AND `+dl+` < CAST(? AS BIGINT)`)
		args = append(args, aargs...)
		args = append(args, dargs...)
		args = append(args, ms(f.Overdue.Now))
	} else if f.Overdue != nil {
		where = append(where, `1 = 0`)
	}
	cond := ""
	if len(where) > 0 {
		cond = "WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, f.Limit+1)
	rows, err := q.Query(ctx, `SELECT p.id, p.name, p.personal_of, p.inbox, p.created, p.disabled, p.size_bytes, p.size_at, p.members, p.tenant_id, u.id, u.username, u.name,
			p.last_used, p.cache_state, p.pinned, p.exempt, p.index_bytes, p.cred_type, p.provider, p.owner_exempt, p.has_assets
		FROM (SELECT s.id, s.name, s.personal_of, s.inbox, s.created, s.disabled, s.size_bytes, s.size_at, s.tenant_id,
			s.last_used, s.cache_state, s.pinned, s.exempt, s.index_bytes,
			COALESCE((SELECT r.cred_type FROM remotes r WHERE r.space_id = s.id AND r.url <> ''), '') AS cred_type,
			COALESCE((SELECT r.provider FROM remotes r WHERE r.space_id = s.id AND r.url <> ''), '') AS provider,
			(SELECT COUNT(*) FROM asset_remotes ar WHERE ar.space_id = s.id AND ar.url <> '') AS has_assets,
			(SELECT COUNT(*) FROM space_members om JOIN users ou ON ou.id = om.user_id WHERE om.space_id = s.id AND om.role = 'owner' AND ou.remote_exempt <> 0) AS owner_exempt,
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
		var used, pinned, exempt, ownerExempt, hasAssets int64
		if err := rows.Scan(&s.ID, &s.Name, &personal, &s.Inbox, &created, &disabled, &size, &sizeAt, &s.Members, &s.TenantID, &ownerID, &ownerUser, &ownerName,
			&used, &s.Storage.Cache, &pinned, &exempt, &s.Storage.IndexBytes, &s.CredType, &s.Provider, &ownerExempt, &hasAssets); err != nil {
			return nil, "", err
		}
		if used > 0 {
			s.Storage.LastUsed = fromMS(used)
		}
		s.Storage.Pinned, s.Storage.Exempt, s.OwnerExempt = pinned != 0, exempt != 0, ownerExempt != 0
		s.Storage.AssetsRepo = hasAssets != 0
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
	// One statement, so a membership that appeared (or was there all along) is
	// made the owner's instead of failing on the key; it joins the space and the
	// user on the same tenant, so nobody of another tenant becomes an owner
	// (nothing is written and the answer is ErrNotFound). Callers also hold
	// LockSpace, as every change of members does.
	res, err := q.Exec(ctx, `INSERT INTO space_members(space_id, user_id, role, created)
		SELECT s.id, u.id, 'owner', ? FROM spaces s JOIN users u ON u.tenant_id = s.tenant_id WHERE s.id = ? AND u.id = ? AND u.deleted_at IS NULL
		ON CONFLICT (space_id, user_id) DO UPDATE SET role = 'owner'`, ms(now), spaceID, newOwner)
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
