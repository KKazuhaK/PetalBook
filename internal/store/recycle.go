package store

// The recycle bin (milestone 3.5, docs/api.md "The recycle bin"). Deleting a
// space copies everything the database holds of it into one JSON document,
// recycle_items.records, and deletes the live rows in the same transaction:
// from then on no query of the live tables can see the space, and nothing is
// lost, because the document holds what the rows held. Restoring puts the rows
// back, under the same ids, by statements that join each person and group to
// the space on the tenant (so a restore cannot link two tenants).

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The states of a recycle item. moving: the rows are already gone from the live
// tables and the directories are being moved to DATA/recycle/<id>; a crash
// leaves it and the next start finishes the move. restoring and purging are
// held while a restore or a purge runs, so the two never run at once.
const (
	RecycleMoving    = "moving"
	RecycleReady     = "ready"
	RecycleRestoring = "restoring"
	RecyclePurging   = "purging"
)

// The kinds of item.
const (
	RecycleSpace = "space"
	RecycleUser  = "user"
)

var (
	// ErrOwnerRequired: restoring a space that has no owner left needs one named.
	ErrOwnerRequired = errors.New("the space has no owner left: name one")
	// ErrRestoreConflict: a space with the id exists again.
	ErrRestoreConflict = errors.New("a space with that id exists")
	// ErrBusyItem: the item is being restored, purged or moved.
	ErrBusyItem = errors.New("the item is in use")
	// ErrNeedsPerson: a personal space is restored with its person.
	ErrNeedsPerson = errors.New("a personal space is restored with its person")
)

// RecycleItem is a row of recycle_items, without its records unless they were
// asked for.
type RecycleItem struct {
	ID, TenantID, Kind string
	SpaceID, UserID    string
	// Parent is the item of the person this space was recycled with.
	Parent   string
	Name     string
	Personal bool
	// Dirs are the directories that were moved, "space" and "cache".
	Dirs                     []string
	Bytes                    int64
	DeletedAt                time.Time
	DeletedBy, DeletedByName string
	Reason                   string
	State                    string
	StateAt                  time.Time
	Records                  string
}

const recycleCols = `id, tenant_id, kind, space_id, user_id, parent, name, personal, dirs, bytes, deleted_at, deleted_by, deleted_by_name, reason, state, state_at`

func scanRecycle(sc interface{ Scan(...any) error }, withRecords bool) (*RecycleItem, error) {
	var it RecycleItem
	var personal, deleted, stateAt int64
	var dirs string
	dest := []any{&it.ID, &it.TenantID, &it.Kind, &it.SpaceID, &it.UserID, &it.Parent, &it.Name, &personal, &dirs, &it.Bytes, &deleted,
		&it.DeletedBy, &it.DeletedByName, &it.Reason, &it.State, &stateAt}
	if withRecords {
		dest = append(dest, &it.Records)
	}
	if err := sc.Scan(dest...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	it.Personal, it.DeletedAt, it.StateAt = personal != 0, fromMS(deleted), fromMS(stateAt)
	_ = json.Unmarshal([]byte(dirs), &it.Dirs)
	return &it, nil
}

// GetRecycleItem returns an item of a tenant in the scope. The records are
// read only when asked for: they are the size of a space's drafts.
func GetRecycleItem(ctx context.Context, q Q, sc Scope, id string, withRecords bool) (*RecycleItem, error) {
	cond, args := sc.cond("tenant_id")
	cols := recycleCols
	if withRecords {
		cols += `, records`
	}
	return scanRecycle(q.QueryRow(ctx, `SELECT `+cols+` FROM recycle_items WHERE id = ? AND `+cond, append([]any{id}, args...)...), withRecords)
}

// RecycleFilter picks a page of ListRecycle.
type RecycleFilter struct {
	Kind  string
	Query string
	// After is the cursor of the page ("<deleted ms>:<id>"; "" for the first).
	After string
	Limit int
}

// ListRecycle returns a page of the items of the scope, newest first: one
// query on the (tenant, deleted_at, id) index for a tenant, a scan of the
// newest rows for the whole server. The records are not read. next is the
// cursor of the following page.
func ListRecycle(ctx context.Context, q Q, sc Scope, f RecycleFilter) (out []RecycleItem, next string, err error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	cond, args := sc.cond("tenant_id")
	where := []string{cond}
	if f.Kind != "" {
		where, args = append(where, `kind = ?`), append(args, f.Kind)
	}
	if f.Query != "" {
		where, args = append(where, `name_key LIKE ? ESCAPE '\'`), append(args, "%"+likeEscape(FoldName(f.Query))+"%")
	}
	if f.After != "" {
		at, id, ok := strings.Cut(f.After, ":")
		n, perr := strconv.ParseInt(at, 10, 64)
		if !ok || perr != nil {
			return nil, "", ErrBadCursor
		}
		where = append(where, `(deleted_at < ? OR (deleted_at = ? AND id < ?))`)
		args = append(args, n, n, id)
	}
	args = append(args, f.Limit+1)
	rows, err := q.Query(ctx, `SELECT `+recycleCols+` FROM recycle_items WHERE `+strings.Join(where, " AND ")+` ORDER BY deleted_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	for rows.Next() {
		it, err := scanRecycle(rows, false)
		if err != nil {
			return nil, "", err
		}
		out = append(out, *it)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(out) > f.Limit {
		out = out[:f.Limit]
		last := out[len(out)-1]
		next = strconv.FormatInt(ms(last.DeletedAt), 10) + ":" + last.ID
	}
	return out, next, nil
}

// RecycleChildren returns the items recycled with the person of item parent.
func RecycleChildren(ctx context.Context, q Q, parent string) ([]RecycleItem, error) {
	rows, err := q.Query(ctx, `SELECT `+recycleCols+` FROM recycle_items WHERE parent = ? ORDER BY deleted_at, id`, parent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RecycleItem
	for rows.Next() {
		it, err := scanRecycle(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, *it)
	}
	return out, rows.Err()
}

// RecycleNotAtRest lists the items that a crash may have left half done, for
// the start to finish: the oldest state change first.
func RecycleNotAtRest(ctx context.Context, q Q) ([]RecycleItem, error) {
	rows, err := q.Query(ctx, `SELECT `+recycleCols+` FROM recycle_items WHERE state <> 'ready' ORDER BY state_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RecycleItem
	for rows.Next() {
		it, err := scanRecycle(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, *it)
	}
	return out, rows.Err()
}

// NextExpiredRecycle is the oldest item at rest that was deleted before the
// time: the purge job takes one at a time. ok is false when there is none. A
// user's own item waits for the spaces recycled with them to go first (they
// are older or equal, and purged first, as they are found first).
func NextExpiredRecycle(ctx context.Context, q Q, before time.Time) (it *RecycleItem, ok bool, err error) {
	it, err = scanRecycle(q.QueryRow(ctx, `SELECT `+recycleCols+` FROM recycle_items WHERE deleted_at < ? AND state = 'ready' ORDER BY deleted_at, CASE kind WHEN 'space' THEN 0 ELSE 1 END, id LIMIT 1`, ms(before)), false)
	if errors.Is(err, ErrNotFound) {
		return nil, false, nil
	}
	return it, err == nil, err
}

// SetRecycleState moves an item from one state to another, and reports
// ErrBusyItem when it was not in the first (somebody else holds it) and
// ErrNotFound when there is no such item.
func SetRecycleState(ctx context.Context, q Q, id, from, to string, now time.Time) error {
	res, err := q.Exec(ctx, `UPDATE recycle_items SET state = ?, state_at = ? WHERE id = ? AND state = ?`, to, ms(now), id, from)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var one int
		if err := q.QueryRow(ctx, `SELECT 1 FROM recycle_items WHERE id = ?`, id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return ErrBusyItem
	}
	return nil
}

// SetRecycleDirs records which directories of an item were moved.
func SetRecycleDirs(ctx context.Context, q Q, id string, dirs []string) error {
	b, _ := json.Marshal(dirs)
	res, err := q.Exec(ctx, `UPDATE recycle_items SET dirs = ? WHERE id = ?`, string(b), id)
	return oneRow(res, err)
}

// DeleteRecycleItem removes the row of an item that was purged. It must be in
// the state purging.
func DeleteRecycleItem(ctx context.Context, q Q, id string) error {
	res, err := q.Exec(ctx, `DELETE FROM recycle_items WHERE id = ? AND state = 'purging'`, id)
	return oneRow(res, err)
}

// --- the records of a space -----------------------------------------------------

// tableRows is a table's rows as they were read: the column names and one slice
// of values per row (strings, numbers, null).
type tableRows struct {
	Cols []string `json:"cols"`
	Rows [][]any  `json:"rows"`
}

// spaceRecords is the document in recycle_items.records for a space.
type spaceRecords struct {
	Version int                  `json:"version"`
	Space   tableRows            `json:"space"`
	Tables  map[string]tableRows `json:"tables"`
}

// recycledTable is a table with rows of a space, and which of its columns name
// a person or a group (a restore joins those to the space's tenant).
type recycledTable struct {
	name      string
	user, grp string
}

// recycledTables is every table that holds rows of a space, in the order a
// restore inserts them. A table added to the schema with a space_id is added
// here, and the test of the recycle bin checks that none is forgotten.
var recycledTables = []recycledTable{
	{name: "space_members", user: "user_id"},
	{name: "space_groups", grp: "group_id"},
	{name: "remotes"},
	{name: "asset_remotes"},
	{name: "space_daily"},
	{name: "folder_moves"},
	{name: "favorites", user: "user_id"},
	{name: "drafts", user: "user_id"},
}

// RecycledTables names the tables a recycled space's rows are kept from (for
// the test that every table with a space_id is among them).
func RecycledTables() []string {
	out := make([]string, len(recycledTables))
	for i, t := range recycledTables {
		out[i] = t.name
	}
	return out
}

func dumpTable(ctx context.Context, q Q, table, where string, args ...any) (tableRows, error) {
	rows, err := q.Query(ctx, `SELECT * FROM `+table+` WHERE `+where, args...)
	if err != nil {
		return tableRows{}, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return tableRows{}, err
	}
	out := tableRows{Cols: cols, Rows: [][]any{}}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return tableRows{}, err
		}
		for i, v := range vals {
			switch x := v.(type) {
			case []byte:
				vals[i] = string(x)
			case int, int32:
				vals[i] = toInt64(x)
			case bool:
				vals[i] = b2i(x)
			case time.Time:
				vals[i] = ms(x)
			}
		}
		out.Rows = append(out.Rows, vals)
	}
	return out, rows.Err()
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case int:
		return int64(x)
	case int32:
		return int64(x)
	}
	return 0
}

// RecycleSpaceIn says what RecycleSpaceRows does.
type RecycleSpaceIn struct {
	// ItemID is the id of the new item; Parent the item of the person the space
	// is recycled with ("" for none).
	ItemID, Parent string
	SpaceID        string
	// By is who deleted it; Reason why (free text).
	By, ByName, Reason string
	// Dirs are the directories that exist and will be moved.
	Dirs []string
	Now  time.Time
}

// RecycledSpace is what RecycleSpaceRows did.
type RecycledSpace struct {
	Item *RecycleItem
	// Members are the people whose lists the space was in (its members and the
	// people of its groups).
	Members []string
	// Counted is whom the space was counted for, and its tenant.
	Counted Touched
}

// RecycleSpaceRows moves a space's rows into a recycle item: the item is made
// with state "moving" (the directories are moved after this commits, see
// internal/spaces), the space's usage is taken out of the quotas, and every
// live row of the space is deleted. Call it in a transaction. It returns the
// item and the people whose lists the space was in (members and the people of
// its groups), for the caller to drop their cached lists.
func RecycleSpaceRows(ctx context.Context, q Q, sc Scope, in RecycleSpaceIn) (out RecycledSpace, err error) {
	sp, err := GetSpace(ctx, q, sc, in.SpaceID)
	if err != nil {
		return out, err
	}
	if err := LockSpace(ctx, q, in.SpaceID); err != nil {
		return out, err
	}
	members, err := SpaceMemberIDs(ctx, q, in.SpaceID)
	if err != nil {
		return out, err
	}
	viaGroups, err := SpaceGroupUserIDs(ctx, q, in.SpaceID)
	if err != nil {
		return out, err
	}
	out.Members = append(members, viaGroups...)
	if out.Counted, err = SpaceCountedFor(ctx, q, in.SpaceID); err != nil {
		return out, err
	}

	rec := spaceRecords{Version: 1, Tables: map[string]tableRows{}}
	if rec.Space, err = dumpTable(ctx, q, "spaces", "id = ?", in.SpaceID); err != nil {
		return out, err
	}
	if len(rec.Space.Rows) != 1 {
		return out, ErrNotFound
	}
	for _, t := range recycledTables {
		if rec.Tables[t.name], err = dumpTable(ctx, q, t.name, "space_id = ?", in.SpaceID); err != nil {
			return out, err
		}
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return out, err
	}
	// What the space weighs on the server: its measured directory and its index.
	var size sql.NullInt64
	var index int64
	if err := q.QueryRow(ctx, `SELECT size_bytes, index_bytes FROM spaces WHERE id = ?`, in.SpaceID).Scan(&size, &index); err != nil {
		return out, err
	}
	bytesHeld := size.Int64 + index
	dirs, _ := json.Marshal(in.Dirs)
	if in.Dirs == nil {
		dirs = []byte("[]")
	}
	if _, err := q.Exec(ctx, `INSERT INTO recycle_items(id, tenant_id, kind, space_id, user_id, parent, name, name_key, personal, records, dirs, bytes, deleted_at, deleted_by, deleted_by_name, reason, state, state_at)
		VALUES (?, ?, 'space', ?, '', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'moving', ?)`,
		in.ItemID, sp.TenantID, in.SpaceID, in.Parent, sp.Name, FoldName(sp.Name), b2i(sp.Personal), string(raw), string(dirs), bytesHeld, ms(in.Now), in.By, in.ByName, in.Reason, ms(in.Now)); err != nil {
		return out, err
	}
	if err := forgetSpaceUsage(ctx, q, in.SpaceID); err != nil {
		return out, err
	}
	for _, stmt := range []string{
		`DELETE FROM drafts WHERE space_id = ?`,
		`DELETE FROM remotes WHERE space_id = ?`,
		`DELETE FROM asset_remotes WHERE space_id = ?`,
		`DELETE FROM favorites WHERE space_id = ?`,
		`DELETE FROM space_daily WHERE space_id = ?`,
		`DELETE FROM folder_moves WHERE space_id = ?`,
		`DELETE FROM space_members WHERE space_id = ?`,
		`DELETE FROM space_groups WHERE space_id = ?`,
	} {
		if _, err := q.Exec(ctx, stmt, in.SpaceID); err != nil {
			return out, err
		}
	}
	res, err := q.Exec(ctx, `DELETE FROM spaces WHERE id = ?`, in.SpaceID)
	if err := oneRow(res, err); err != nil {
		return out, err
	}
	out.Item, err = scanRecycle(q.QueryRow(ctx, `SELECT `+recycleCols+` FROM recycle_items WHERE id = ?`, in.ItemID), false)
	return out, err
}

// RestoreSkipped is a row of a recycled space that was left out of a restore.
type RestoreSkipped struct {
	User   string `json:"user,omitempty"`
	Group  string `json:"group,omitempty"`
	Reason string `json:"reason"`
}

// RestoreOutcome is what RestoreSpaceRows did.
type RestoreOutcome struct {
	Skipped []RestoreSkipped
	// Members are the people whose lists the space is in again.
	Members []string
	// Counted is whom the space is counted for now and its tenant.
	Counted Touched
	// Cache is the space's cache state as it was recorded.
	Cache string
	// Remote is the state of its remote ("" for none), AssetsRemote whether it
	// has an attachment repository.
	Remote       string
	AssetsRemote bool
	// Disabled: the space was switched off when it was deleted and still is.
	Disabled bool
}

func jsonValue(v any) any {
	switch x := v.(type) {
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n
		}
		f, _ := x.Float64()
		return f
	case float64:
		if x == float64(int64(x)) {
			return int64(x)
		}
	}
	return v
}

func decodeRecords(raw string) (*spaceRecords, error) {
	var rec spaceRecords
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.UseNumber()
	if err := dec.Decode(&rec); err != nil {
		return nil, err
	}
	if rec.Version != 1 {
		return nil, fmt.Errorf("recycle item: unknown records version %d", rec.Version)
	}
	return &rec, nil
}

func (t tableRows) col(name string) int {
	for i, c := range t.Cols {
		if c == name {
			return i
		}
	}
	return -1
}

// castParam is a placeholder for v in the select list of an INSERT … SELECT,
// where PostgreSQL has no column to take its type from.
func castParam(v any) (sqlText string, args []any) {
	switch x := jsonValue(v).(type) {
	case nil:
		return "NULL", nil
	case int64:
		return "CAST(? AS BIGINT)", []any{x}
	case float64:
		return "CAST(? AS BIGINT)", []any{int64(x)}
	case string:
		return "CAST(? AS TEXT)", []any{x}
	default:
		return "CAST(? AS TEXT)", []any{fmt.Sprint(x)}
	}
}

// RestoreSpaceRows puts a recycled space's rows back under their ids, in the
// original tenant, and deletes the item, in the transaction q. People and
// groups that no longer exist (or are deleted) are left out and listed. When
// no owner is left, owner must name one (ErrOwnerRequired otherwise); an owner
// given is made an owner either way. A space with the id existing again is
// ErrRestoreConflict. The item must be in state restoring.
//
// The quotas count the space again (a restore is never refused for a quota).
func RestoreSpaceRows(ctx context.Context, q Q, item *RecycleItem, owner string, withPerson bool, now time.Time) (RestoreOutcome, error) {
	var out RestoreOutcome
	if item.Kind != RecycleSpace {
		return out, fmt.Errorf("recycle item %s is not a space", item.ID)
	}
	var raw string
	if err := q.QueryRow(ctx, `SELECT records FROM recycle_items WHERE id = ? AND state = 'restoring'`, item.ID).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return out, ErrBusyItem
		}
		return out, err
	}
	rec, err := decodeRecords(raw)
	if err != nil {
		return out, err
	}
	// The tenant must still exist; deleting a tenant is refused while items are
	// held for it, so this is a check, not an expected answer.
	var one int
	if err := q.QueryRow(ctx, `SELECT 1 FROM tenants WHERE id = ?`, item.TenantID).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return out, ErrNotFound
		}
		return out, err
	}
	// A personal space goes with its person: on its own it would be a second
	// home for someone who is not there.
	if item.Personal && !withPerson {
		return out, ErrNeedsPerson
	}
	// The space row, with its counters reset: it is counted again below.
	sc := rec.Space
	row := append([]any(nil), sc.Rows[0]...)
	set := func(col string, v any) {
		if i := sc.col(col); i >= 0 {
			row[i] = v
		}
	}
	set("tenant_id", item.TenantID)
	set("usage_user", "")
	var usageBytes, pages int64
	if i := sc.col("usage_bytes"); i >= 0 {
		usageBytes, _ = jsonValue(row[i]).(int64)
	}
	if i := sc.col("page_count"); i >= 0 {
		pages, _ = jsonValue(row[i]).(int64)
	}
	if i := sc.col("cache_state"); i >= 0 {
		out.Cache, _ = row[i].(string)
	}
	ph := strings.TrimSuffix(strings.Repeat("?, ", len(sc.Cols)), ", ")
	args := make([]any, len(row))
	for i, v := range row {
		args[i] = jsonValue(v)
	}
	if _, err := q.Exec(ctx, `INSERT INTO spaces(`+strings.Join(sc.Cols, ", ")+`) VALUES (`+ph+`)`, args...); err != nil {
		if IsUnique(err) {
			return out, ErrRestoreConflict
		}
		return out, err
	}
	if err := addUsage(ctx, q, usageDelta{kind: UsageTenant, id: item.TenantID, tenant: item.TenantID, spaces: 1, bytes: usageBytes, pages: pages}); err != nil {
		return out, err
	}
	for _, t := range recycledTables {
		tr := rec.Tables[t.name]
		spaceCol := tr.col("space_id")
		userCol, groupCol := -1, -1
		if t.user != "" {
			userCol = tr.col(t.user)
		}
		if t.grp != "" {
			groupCol = tr.col(t.grp)
		}
		for _, r := range tr.Rows {
			var sel []string
			var selArgs []any
			for i := range tr.Cols {
				switch {
				case i == spaceCol:
					sel = append(sel, "s.id")
				case i == userCol:
					sel = append(sel, "u.id")
				case i == groupCol:
					sel = append(sel, "g.id")
				default:
					text, a := castParam(r[i])
					sel = append(sel, text)
					selArgs = append(selArgs, a...)
				}
			}
			from := `FROM spaces s`
			where := `WHERE s.id = ?`
			wargs := []any{item.SpaceID}
			if userCol >= 0 {
				from += ` JOIN users u ON u.tenant_id = s.tenant_id`
				where += ` AND u.id = ? AND u.deleted_at IS NULL`
				wargs = append(wargs, jsonValue(r[userCol]))
			}
			if groupCol >= 0 {
				from += ` JOIN groups g ON g.tenant_id = s.tenant_id`
				where += ` AND g.id = ?`
				wargs = append(wargs, jsonValue(r[groupCol]))
			}
			res, err := q.Exec(ctx, `INSERT INTO `+t.name+`(`+strings.Join(tr.Cols, ", ")+`) SELECT `+strings.Join(sel, ", ")+` `+from+` `+where, append(selArgs, wargs...)...)
			if err != nil {
				return out, err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				switch {
				case userCol >= 0 && t.name == "space_members":
					out.Skipped = append(out.Skipped, RestoreSkipped{User: fmt.Sprint(jsonValue(r[userCol])), Reason: "deleted"})
				case groupCol >= 0:
					out.Skipped = append(out.Skipped, RestoreSkipped{Group: fmt.Sprint(jsonValue(r[groupCol])), Reason: "deleted"})
				}
			}
		}
	}
	// An owner named is made an owner, in the same tenant and only if they can
	// be one (they exist, are not deleted, are not disabled).
	if owner != "" {
		res, err := q.Exec(ctx, `INSERT INTO space_members(space_id, user_id, role, created)
			SELECT s.id, u.id, 'owner', ? FROM spaces s JOIN users u ON u.tenant_id = s.tenant_id
			WHERE s.id = ? AND u.id = ? AND u.deleted_at IS NULL AND u.disabled = 0
			ON CONFLICT (space_id, user_id) DO UPDATE SET role = 'owner'`, ms(now), item.SpaceID, owner)
		if err := oneRow(res, err); err != nil {
			return out, err // ErrNotFound: not a person who can own it
		}
	}
	var owners int
	if err := q.QueryRow(ctx, `SELECT COUNT(*) FROM space_members WHERE space_id = ? AND role = 'owner'`, item.SpaceID).Scan(&owners); err != nil {
		return out, err
	}
	if owners == 0 {
		return out, ErrOwnerRequired
	}
	if err := settleOwner(ctx, q, item.SpaceID); err != nil {
		return out, err
	}
	var err2 error
	if out.Members, err2 = SpaceMemberIDs(ctx, q, item.SpaceID); err2 != nil {
		return out, err2
	}
	viaGroups, err := SpaceGroupUserIDs(ctx, q, item.SpaceID)
	if err != nil {
		return out, err
	}
	out.Members = append(out.Members, viaGroups...)
	if out.Counted, err = SpaceCountedFor(ctx, q, item.SpaceID); err != nil {
		return out, err
	}
	var disabled int64
	if err := q.QueryRow(ctx, `SELECT disabled FROM spaces WHERE id = ?`, item.SpaceID).Scan(&disabled); err != nil {
		return out, err
	}
	out.Disabled = disabled != 0
	if err := q.QueryRow(ctx, `SELECT state FROM remotes WHERE space_id = ? AND url <> ''`, item.SpaceID).Scan(&out.Remote); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	var ar int
	if err := q.QueryRow(ctx, `SELECT COUNT(*) FROM asset_remotes WHERE space_id = ? AND url <> ''`, item.SpaceID).Scan(&ar); err != nil {
		return out, err
	}
	out.AssetsRemote = ar > 0
	if _, err := q.Exec(ctx, `DELETE FROM recycle_items WHERE id = ? AND state = 'restoring'`, item.ID); err != nil {
		return out, err
	}
	return out, nil
}

// RecycledDraft is a draft a recycled space held.
type RecycledDraft struct {
	PageID, UserID, Username, BaseRev, Meta, Body string
	Held                                          bool
	Updated                                       time.Time
}

// RecycledSummary is what an export reads from the records of a recycled space.
type RecycledSummary struct {
	RemoteURL, AssetsURL string
	Drafts               []RecycledDraft
}

// RecycledView reads the drafts and the remote addresses (never a
// credential) out of the records of an item that was read with them.
func RecycledView(ctx context.Context, q Q, it *RecycleItem) (RecycledSummary, error) {
	var out RecycledSummary
	rec, err := decodeRecords(it.Records)
	if err != nil {
		return out, err
	}
	str := func(tr tableRows, r []any, col string) string {
		if i := tr.col(col); i >= 0 && i < len(r) {
			if s, ok := r[i].(string); ok {
				return s
			}
		}
		return ""
	}
	if tr, ok := rec.Tables["remotes"]; ok && len(tr.Rows) > 0 {
		out.RemoteURL = str(tr, tr.Rows[0], "url")
	}
	if tr, ok := rec.Tables["asset_remotes"]; ok && len(tr.Rows) > 0 {
		out.AssetsURL = str(tr, tr.Rows[0], "url")
	}
	tr := rec.Tables["drafts"]
	names := map[string]string{}
	for _, r := range tr.Rows {
		d := RecycledDraft{PageID: str(tr, r, "page_id"), UserID: str(tr, r, "user_id"), BaseRev: str(tr, r, "base_rev"), Meta: str(tr, r, "meta"), Body: str(tr, r, "body")}
		if i := tr.col("held"); i >= 0 {
			h, _ := jsonValue(r[i]).(int64)
			d.Held = h != 0
		}
		if i := tr.col("updated"); i >= 0 {
			u, _ := jsonValue(r[i]).(int64)
			d.Updated = fromMS(u)
		}
		if n, ok := names[d.UserID]; ok {
			d.Username = n
		} else {
			var n string
			_ = q.QueryRow(ctx, `SELECT username FROM users WHERE id = ?`, d.UserID).Scan(&n)
			names[d.UserID], d.Username = n, n
		}
		out.Drafts = append(out.Drafts, d)
	}
	return out, nil
}

// GetRecycleItems returns the items with these ids that are in the scope, by
// id; ids that are not there or not in the scope are left out. The records are
// not read.
func GetRecycleItems(ctx context.Context, q Q, sc Scope, ids []string) (map[string]RecycleItem, error) {
	out := map[string]RecycleItem{}
	if len(ids) == 0 {
		return out, nil
	}
	cond, args := sc.cond("tenant_id")
	ph := strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", ")
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := q.Query(ctx, `SELECT `+recycleCols+` FROM recycle_items WHERE `+cond+` AND id IN (`+ph+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		it, err := scanRecycle(rows, false)
		if err != nil {
			return nil, err
		}
		out[it.ID] = *it
	}
	return out, rows.Err()
}
