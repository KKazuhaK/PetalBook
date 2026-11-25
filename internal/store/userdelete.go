package store

// Deleting a person (milestone 3.5, docs/api.md "Deleting a user"). A person is
// never removed at once: their row is marked deleted (users.deleted_at) and a
// recycle item of kind "user" holds what was taken from them (their group
// memberships, who got their spaces, which they left). Everything that lists or
// finds people leaves a deleted person out. Purging the item deletes their
// personal data and leaves the row as a tombstone while anything refers to it.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ErrUserDeleted is a person who is in the recycle bin (or purged) where a
// person who is not was needed.
var ErrUserDeleted = errors.New("the person is deleted")

// UserState is whether a person is deleted, and the item that holds them.
type UserState struct {
	Deleted bool
	Item    string
	Tenant  string
	// Tombstone: the personal data is purged; only the row is left.
	Tombstone bool
}

// GetUserState reads whether a person is deleted, whoever they are (a deleted
// person is invisible to GetUser).
func GetUserState(ctx context.Context, q Q, id string) (UserState, error) {
	var st UserState
	var deleted sql.NullInt64
	err := q.QueryRow(ctx, `SELECT deleted_at, recycle_item, tenant_id FROM users WHERE id = ?`, id).Scan(&deleted, &st.Item, &st.Tenant)
	if errors.Is(err, sql.ErrNoRows) {
		return st, ErrNotFound
	}
	st.Deleted = deleted.Valid
	st.Tombstone = st.Deleted && st.Item == ""
	return st, err
}

// --- the plan ---------------------------------------------------------------------

// PlanSpace is a space of a person as the plan of deleting them sees it.
type PlanSpace struct {
	ID, Name string
	Personal bool
	Role     string
	Bytes    int64
}

// PlanTransfer is a space handed to someone else.
type PlanTransfer struct {
	Space PlanSpace
	// To is the heir, Why how they were chosen (named, editor, heir).
	To         string
	ToUsername string
	Why        string
	// Credential is "user" when the space syncs with the person's own token.
	Credential string
}

// UserPlan is what deleting a person would do, computed from records only.
type UserPlan struct {
	Recycle   []PlanSpace
	Transfer  []PlanTransfer
	Leave     []PlanSpace
	NeedsHeir []PlanSpace
	Groups    int
	Tokens    int
	Grants    int
	Passkeys  int
	Identity  int
	GitHub    bool
	// Connections: the providers of 3.8 the person is connected to, by id.
	Connections []string
	Sessions    int
}

// PlanUserDeletion works out what deleting the person would do, in a constant
// number of queries however many spaces they have. transfers names an heir for
// a space; heir is the fallback of the contract's order (named, the earliest
// direct editor, heir). The person must be in the scope and not deleted.
func PlanUserDeletion(ctx context.Context, q Q, sc Scope, userID, heir string, transfers map[string]string) (*UserPlan, error) {
	u, err := GetUser(ctx, q, sc, userID)
	if err != nil {
		return nil, err
	}
	p := &UserPlan{}
	rows, err := q.Query(ctx, `SELECT s.id, s.name, m.role, COALESCE(s.personal_of, ''), COALESCE(s.size_bytes, 0) + s.index_bytes
		FROM space_members m JOIN spaces s ON s.id = m.space_id WHERE m.user_id = ? ORDER BY s.created, s.id`, userID)
	if err != nil {
		return nil, err
	}
	var mine []PlanSpace
	for rows.Next() {
		var s PlanSpace
		var personal string
		if err := rows.Scan(&s.ID, &s.Name, &s.Role, &personal, &s.Bytes); err != nil {
			rows.Close()
			return nil, err
		}
		s.Personal = personal != ""
		mine = append(mine, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// The spaces' other people, in one query each.
	inMine := `space_id IN (SELECT space_id FROM space_members WHERE user_id = ?)`
	count := func(query string, args ...any) (map[string]int, error) {
		rs, err := q.Query(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		defer rs.Close()
		out := map[string]int{}
		for rs.Next() {
			var id string
			var n int
			if err := rs.Scan(&id, &n); err != nil {
				return nil, err
			}
			out[id] = n
		}
		return out, rs.Err()
	}
	otherOwners, err := count(`SELECT space_id, COUNT(*) FROM space_members WHERE role = 'owner' AND user_id <> ? AND `+inMine+` GROUP BY space_id`, userID, userID)
	if err != nil {
		return nil, err
	}
	others, err := count(`SELECT space_id, COUNT(*) FROM space_members WHERE user_id <> ? AND `+inMine+` GROUP BY space_id`, userID, userID)
	if err != nil {
		return nil, err
	}
	groups, err := count(`SELECT space_id, COUNT(*) FROM space_groups WHERE `+inMine+` GROUP BY space_id`, userID)
	if err != nil {
		return nil, err
	}
	// The earliest direct editor of each space who could be an heir.
	firstEditor := map[string]string{}
	er, err := q.Query(ctx, `SELECT m.space_id, m.user_id FROM space_members m JOIN users u ON u.id = m.user_id
		WHERE m.role = 'editor' AND m.user_id <> ? AND u.disabled = 0 AND u.deleted_at IS NULL AND m.`+inMine+` ORDER BY m.created, m.user_id`, userID, userID)
	if err != nil {
		return nil, err
	}
	for er.Next() {
		var sid, uid string
		if err := er.Scan(&sid, &uid); err != nil {
			er.Close()
			return nil, err
		}
		if _, ok := firstEditor[sid]; !ok {
			firstEditor[sid] = uid
		}
	}
	er.Close()
	if err := er.Err(); err != nil {
		return nil, err
	}
	cred := map[string]bool{}
	cr, err := q.Query(ctx, `SELECT space_id FROM remotes WHERE cred_type = 'connection' AND gh_via = 'user' AND gh_user_id = ? AND `+inMine, userID, userID)
	if err != nil {
		return nil, err
	}
	for cr.Next() {
		var sid string
		if err := cr.Scan(&sid); err != nil {
			cr.Close()
			return nil, err
		}
		cred[sid] = true
	}
	cr.Close()
	if err := cr.Err(); err != nil {
		return nil, err
	}
	// Who may inherit: enabled, not deleted, of the person's tenant.
	canInherit := map[string]string{} // id -> username
	check := func(id string) (string, bool) {
		if id == "" || id == userID {
			return "", false
		}
		if n, ok := canInherit[id]; ok {
			return n, n != ""
		}
		var name string
		err := q.QueryRow(ctx, `SELECT username FROM users WHERE id = ? AND tenant_id = ? AND disabled = 0 AND deleted_at IS NULL`, id, u.TenantID).Scan(&name)
		canInherit[id] = name
		return name, err == nil
	}
	for _, s := range mine {
		switch {
		case s.Personal:
			p.Recycle = append(p.Recycle, s)
		case s.Role == "owner" && otherOwners[s.ID] == 0:
			if others[s.ID] == 0 && groups[s.ID] == 0 {
				p.Recycle = append(p.Recycle, s) // rule 2: nobody else is in it
				continue
			}
			to, why := "", ""
			if _, ok := check(transfers[s.ID]); ok {
				to, why = transfers[s.ID], "named"
			} else if e := firstEditor[s.ID]; e != "" {
				to, why = e, "editor"
			} else if _, ok := check(heir); ok {
				to, why = heir, "heir"
			}
			if to == "" {
				p.NeedsHeir = append(p.NeedsHeir, s)
				continue
			}
			name, _ := check(to)
			t := PlanTransfer{Space: s, To: to, ToUsername: name, Why: why}
			if cred[s.ID] {
				t.Credential = "user"
			}
			p.Transfer = append(p.Transfer, t)
		default:
			p.Leave = append(p.Leave, s)
		}
	}
	counts := []struct {
		to    *int
		query string
	}{
		{&p.Groups, `SELECT COUNT(*) FROM group_members WHERE user_id = ?`},
		{&p.Tokens, `SELECT COUNT(*) FROM api_tokens WHERE user_id = ? AND revoked = 0`},
		{&p.Grants, `SELECT COUNT(*) FROM oauth_grants WHERE user_id = ?`},
		{&p.Passkeys, `SELECT COUNT(*) FROM passkeys WHERE user_id = ?`},
		{&p.Identity, `SELECT COUNT(*) FROM identities WHERE user_id = ?`},
		{&p.Sessions, `SELECT COUNT(*) FROM sessions WHERE user_id = ?`},
	}
	for _, c := range counts {
		if err := q.QueryRow(ctx, c.query, userID).Scan(c.to); err != nil {
			return nil, err
		}
	}
	var gh int
	if err := q.QueryRow(ctx, `SELECT COUNT(*) FROM github_authorizations WHERE user_id = ?`, userID).Scan(&gh); err != nil {
		return nil, err
	}
	p.GitHub = gh > 0
	rows, err = q.Query(ctx, `SELECT provider FROM provider_authorizations WHERE user_id = ? ORDER BY provider`, userID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		p.Connections = append(p.Connections, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return p, nil
}

// LeaveSpace removes a person's membership of a space they do not own alone: in
// one transaction that checks, under the space's lock, that someone else still
// owns it (ErrLastOwner otherwise: nothing is removed). It returns the users
// whose lists changed (the person) and whom the space was counted for.
func LeaveSpace(ctx context.Context, q Q, spaceID, userID string) (Touched, Touched, error) {
	var before, after Touched
	if err := LockSpace(ctx, q, spaceID); err != nil {
		return before, after, err
	}
	var role string
	err := q.QueryRow(ctx, `SELECT role FROM space_members WHERE space_id = ? AND user_id = ?`, spaceID, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return before, after, nil // not a member any more: done
	}
	if err != nil {
		return before, after, err
	}
	if role == "owner" {
		var others int
		// A deleted person's row is nobody: it never counts as another owner.
		if err := q.QueryRow(ctx, `SELECT COUNT(*) FROM space_members m JOIN users u ON u.id = m.user_id
			WHERE m.space_id = ? AND m.role = 'owner' AND m.user_id <> ? AND u.deleted_at IS NULL`, spaceID, userID).Scan(&others); err != nil {
			return before, after, err
		}
		if others == 0 {
			return before, after, ErrLastOwnerOfSpace
		}
	}
	if before, err = SpaceCountedFor(ctx, q, spaceID); err != nil {
		return before, after, err
	}
	if _, err := q.Exec(ctx, `DELETE FROM space_members WHERE space_id = ? AND user_id = ?`, spaceID, userID); err != nil {
		return before, after, err
	}
	// The same person's favourites and drafts in the space stay with the
	// person (they are restored with them); counted for the next owner.
	if err := settleOwner(ctx, q, spaceID); err != nil {
		return before, after, err
	}
	after, err = SpaceCountedFor(ctx, q, spaceID)
	return before, after, err
}

// ErrLastOwnerOfSpace: the person is the only owner of the space.
var ErrLastOwnerOfSpace = errors.New("the person is the only owner of the space")

// --- the item -----------------------------------------------------------------------

// UserTransfer is a space that was handed to someone when a person was deleted.
type UserTransfer struct {
	Space      string `json:"space"`
	Name       string `json:"name"`
	To         string `json:"to"`
	ToUsername string `json:"toUsername"`
}

// UserLeft is a space the person left.
type UserLeft struct {
	Space string `json:"space"`
	Name  string `json:"name"`
	Role  string `json:"role"`
}

// userRecords is the document of a person's item.
type userRecords struct {
	Version     int       `json:"version"`
	WasDisabled bool      `json:"wasDisabled"`
	Groups      tableRows `json:"groups"`
	// GroupNames are the names of those groups as they were, for a restore that
	// finds one of them gone.
	GroupNames map[string]string `json:"groupNames,omitempty"`
	Transfers  []UserTransfer    `json:"transfers"`
	Left       []UserLeft        `json:"left"`
	Recycled   []string          `json:"recycled"`
}

// UserDeletionPending is what a deletion of a person under way has done so far,
// kept on the person's row (users.delete_pending) from before its first step,
// so that a deletion asked again after a failure records what the first attempt
// knew: whether the person was disabled before it disabled them, and the spaces
// it handed on and left.
type UserDeletionPending struct {
	WasDisabled bool           `json:"wasDisabled"`
	Transfers   []UserTransfer `json:"transfers"`
	Left        []UserLeft     `json:"left"`
}

// BeginUserDeletion records that a deletion of the person starts, before
// anything is done (wasDisabled: whether they are disabled now), unless one is
// under way already; it returns what the deletion under way has done (the one
// just begun, or the earlier attempt's), and began when it is this one. A
// deletion's first step disables the person, so a record on a person who is not
// disabled is stale (an attempt that never got that far, or one an
// administrator ended by enabling them): it is replaced, never reused.
func BeginUserDeletion(ctx context.Context, q Q, userID string, wasDisabled bool) (p UserDeletionPending, began bool, err error) {
	raw, _ := json.Marshal(UserDeletionPending{WasDisabled: wasDisabled})
	res, err := q.Exec(ctx, `UPDATE users SET delete_pending = ? WHERE id = ? AND (delete_pending = '' OR disabled = 0) AND deleted_at IS NULL`, string(raw), userID)
	if err != nil {
		return p, false, err
	}
	n, _ := res.RowsAffected()
	p, _, err = userDeletionPending(ctx, q, userID)
	return p, n == 1, err
}

// ForgetUserDeletion drops the record of a deletion that changed nothing (its
// first step was refused).
func ForgetUserDeletion(ctx context.Context, q Q, userID string) error {
	_, err := q.Exec(ctx, `UPDATE users SET delete_pending = '' WHERE id = ? AND deleted_at IS NULL`, userID)
	return err
}

// userDeletionPending reads the deletion under way of the person (ok false:
// none is).
func userDeletionPending(ctx context.Context, q Q, userID string) (p UserDeletionPending, ok bool, err error) {
	var raw string
	if err := q.QueryRow(ctx, `SELECT delete_pending FROM users WHERE id = ?`, userID).Scan(&raw); err != nil {
		return p, false, mapNoRows(err)
	}
	if raw == "" {
		return p, false, nil
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return p, false, err
	}
	return p, true, nil
}

// NoteUserDeletion adds a transfer or a membership left to the deletion under
// way (one at a time, after each is done).
func NoteUserDeletion(ctx context.Context, q Q, userID string, t *UserTransfer, l *UserLeft) error {
	p, _, err := userDeletionPending(ctx, q, userID)
	if err != nil {
		return err
	}
	if t != nil {
		p.Transfers = MergeTransfers(p.Transfers, []UserTransfer{*t})
	}
	if l != nil {
		p.Left = mergeLeft(p.Left, []UserLeft{*l})
	}
	raw, _ := json.Marshal(p)
	_, err = q.Exec(ctx, `UPDATE users SET delete_pending = ? WHERE id = ? AND deleted_at IS NULL`, string(raw), userID)
	return err
}

// MergeTransfers is a, then what of b is not in it (by space): an earlier
// attempt's entry wins.
func MergeTransfers(a, b []UserTransfer) []UserTransfer {
	seen := map[string]bool{}
	for _, t := range a {
		seen[t.Space] = true
	}
	for _, t := range b {
		if !seen[t.Space] {
			a, seen[t.Space] = append(a, t), true
		}
	}
	return a
}

func mergeLeft(a, b []UserLeft) []UserLeft {
	seen := map[string]bool{}
	for _, l := range a {
		seen[l.Space] = true
	}
	for _, l := range b {
		if !seen[l.Space] {
			a, seen[l.Space] = append(a, l), true
		}
	}
	return a
}

// FinishUserDeletionIn says what FinishUserDeletion records.
type FinishUserDeletionIn struct {
	ItemID, UserID  string
	By, ByName      string
	Reason          string
	WasDisabled     bool
	Transfers       []UserTransfer
	Left            []UserLeft
	Recycled        []string
	Now             time.Time
	PendingChildren string // the marker the person's spaces were recycled under
}

// UserPendingMarker is the parent a person's spaces carry while they are being
// recycled with the person; FinishUserDeletion replaces it with the item.
func UserPendingMarker(userID string) string { return "u:" + userID }

// FinishUserDeletion is the last step of deleting a person, in one transaction:
// the item is made, the person is marked deleted, their group memberships are
// taken out (and kept in the item), the spaces recycled with them are put under
// the item, and the tenant has one person fewer. Call it inside Tx.
func FinishUserDeletion(ctx context.Context, q Q, sc Scope, in FinishUserDeletionIn) (*RecycleItem, error) {
	u, err := GetUser(ctx, q, sc, in.UserID)
	if err != nil {
		return nil, err
	}
	groups, err := dumpTable(ctx, q, "group_members", "user_id = ?", in.UserID)
	if err != nil {
		return nil, err
	}
	groupNames, err := nameMap(ctx, q, `SELECT id, name FROM groups WHERE id IN (SELECT group_id FROM group_members WHERE user_id = ?)`, in.UserID)
	if err != nil {
		return nil, err
	}
	// What an earlier attempt of this deletion did and knew comes first.
	pending, ok, err := userDeletionPending(ctx, q, in.UserID)
	if err != nil {
		return nil, err
	}
	if ok {
		in.WasDisabled = pending.WasDisabled
		in.Transfers = MergeTransfers(pending.Transfers, in.Transfers)
		in.Left = mergeLeft(pending.Left, in.Left)
	}
	raw, err := json.Marshal(userRecords{Version: 1, WasDisabled: in.WasDisabled, Groups: groups, GroupNames: groupNames, Transfers: in.Transfers, Left: in.Left, Recycled: in.Recycled})
	if err != nil {
		return nil, err
	}
	var bytesHeld int64
	if err := q.QueryRow(ctx, `SELECT COALESCE(SUM(bytes), 0) FROM recycle_items WHERE parent = ?`, UserPendingMarker(in.UserID)).Scan(&bytesHeld); err != nil {
		return nil, err
	}
	if _, err := q.Exec(ctx, `INSERT INTO recycle_items(id, tenant_id, kind, space_id, user_id, parent, name, name_key, personal, records, dirs, bytes, deleted_at, deleted_by, deleted_by_name, reason, state, state_at)
		VALUES (?, ?, 'user', '', ?, '', ?, ?, 0, ?, '[]', ?, ?, ?, ?, ?, 'ready', ?)`,
		in.ItemID, u.TenantID, u.ID, u.Username, FoldName(u.Username), string(raw), bytesHeld, ms(in.Now), in.By, in.ByName, in.Reason, ms(in.Now)); err != nil {
		return nil, err
	}
	res, err := q.Exec(ctx, `UPDATE users SET deleted_at = ?, recycle_item = ?, disabled = 1, delete_pending = '' WHERE id = ? AND deleted_at IS NULL`, ms(in.Now), in.ItemID, in.UserID)
	if err := oneRow(res, err); err != nil {
		return nil, err
	}
	if _, err := q.Exec(ctx, `DELETE FROM group_members WHERE user_id = ?`, in.UserID); err != nil {
		return nil, err
	}
	if _, err := q.Exec(ctx, `UPDATE recycle_items SET parent = ? WHERE parent = ?`, in.ItemID, UserPendingMarker(in.UserID)); err != nil {
		return nil, err
	}
	conn, dorm, err := personFlags(ctx, q, in.UserID)
	if err != nil {
		return nil, err
	}
	// A deleted person is not one of the tenant's people, nor of its dormant or
	// connected ones.
	if err := addUsage(ctx, q, usageDelta{kind: UsageTenant, id: u.TenantID, tenant: u.TenantID, people: -1, connected: -conn, dormant: -dorm}); err != nil {
		return nil, err
	}
	return scanRecycle(q.QueryRow(ctx, `SELECT `+recycleCols+` FROM recycle_items WHERE id = ?`, in.ItemID), false)
}

// UserRestoreOutcome is what RestoreUserRows did.
type UserRestoreOutcome struct {
	Username     string
	Disabled     bool
	Skipped      []RestoreSkipped
	Transfers    []UserTransfer
	Left         []UserLeft
	Recycled     []string
	TenantID     string
	GroupMembers int
}

// RestoreUserRows brings a person back as they were before they were disabled
// for the deletion: their group memberships that still can be (a group that is
// gone is listed), and enabled or disabled as they were. The item stays (its
// state restoring) until the spaces recycled with them are back; the caller
// deletes it (DeleteRecycleItemIn). Call it inside Tx.
func RestoreUserRows(ctx context.Context, q Q, it *RecycleItem, now time.Time) (UserRestoreOutcome, error) {
	var out UserRestoreOutcome
	if it.Kind != RecycleUser {
		return out, errors.New("not a person's item")
	}
	var raw string
	if err := q.QueryRow(ctx, `SELECT records FROM recycle_items WHERE id = ? AND state = 'restoring'`, it.ID).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return out, ErrBusyItem
		}
		return out, err
	}
	var rec userRecords
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&rec); err != nil {
		return out, err
	}
	var username string
	var deleted sql.NullInt64
	var recycleItem string
	if err := q.QueryRow(ctx, `SELECT username, deleted_at, recycle_item FROM users WHERE id = ?`, it.UserID).Scan(&username, &deleted, &recycleItem); err != nil {
		return out, mapNoRows(err)
	}
	out.Username, out.TenantID = username, it.TenantID
	// A person whose row is not marked (a deletion cut short before its last step)
	// is only enabled again as they were; a marked one is brought back.
	if deleted.Valid && recycleItem == it.ID {
		res, err := q.Exec(ctx, `UPDATE users SET deleted_at = NULL, recycle_item = '', delete_pending = '', disabled = ? WHERE id = ?`, b2i(rec.WasDisabled), it.UserID)
		if err := oneRow(res, err); err != nil {
			return out, err
		}
		conn, dorm, err := personFlags(ctx, q, it.UserID)
		if err != nil {
			return out, err
		}
		if err := addUsage(ctx, q, usageDelta{kind: UsageTenant, id: it.TenantID, tenant: it.TenantID, people: 1, connected: conn, dormant: dorm}); err != nil {
			return out, err
		}
		for _, r := range rec.Groups.Rows {
			gi, pi, vi, ci := rec.Groups.col("group_id"), rec.Groups.col("provider"), rec.Groups.col("value"), rec.Groups.col("created")
			if gi < 0 || pi < 0 || vi < 0 || ci < 0 {
				continue
			}
			gid := jsonStr(r[gi])
			res, err := q.Exec(ctx, `INSERT INTO group_members(group_id, user_id, provider, value, created)
				SELECT g.id, u.id, CAST(? AS TEXT), CAST(? AS TEXT), CAST(? AS BIGINT) FROM groups g JOIN users u ON u.tenant_id = g.tenant_id
				WHERE g.id = ? AND u.id = ? AND u.deleted_at IS NULL ON CONFLICT DO NOTHING`, jsonStr(r[pi]), jsonStr(r[vi]), jsonValue(r[ci]), gid, it.UserID)
			if err != nil {
				return out, err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				out.Skipped = append(out.Skipped, RestoreSkipped{Group: gid, Name: rec.GroupNames[gid], Reason: "deleted"})
			} else {
				out.GroupMembers++
			}
		}
		out.Disabled = rec.WasDisabled
	}
	out.Transfers, out.Left, out.Recycled = rec.Transfers, rec.Left, rec.Recycled
	return out, nil
}

func jsonStr(v any) string {
	if s, ok := jsonValue(v).(string); ok {
		return s
	}
	return ""
}

func mapNoRows(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// DeleteUserItem removes a person's item whose state is restoring or purging.
func DeleteUserItem(ctx context.Context, q Q, id string) error {
	res, err := q.Exec(ctx, `DELETE FROM recycle_items WHERE id = ? AND state IN ('restoring', 'purging')`, id)
	return oneRow(res, err)
}

// UserSpaceItems are the items of the spaces recycled with the person's item.
// The spaces waiting under the person's pending marker are not among them: they
// belong to a deletion of the person that is under way (one cut short), which
// adopts them when it finishes, never to an item that exists already (a stale
// one, whose restore was cut short, may be older than that deletion).
func UserSpaceItems(ctx context.Context, q Q, itemID string) ([]RecycleItem, error) {
	rows, err := q.Query(ctx, `SELECT `+recycleCols+` FROM recycle_items WHERE parent = ? ORDER BY deleted_at, id`, itemID)
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

// --- purging ----------------------------------------------------------------------------

// PurgeUserData deletes what a person's data is: their password, passkeys,
// identities, sessions, tokens and grants, GitHub connection, favourites, notifications,
// drafts, links sent to their address, group memberships and usage row, and
// empties their row to a tombstone: the id, the tenant and the time of deletion
// stay, the username becomes deleted-<id>, the name and address go. The username
// is recorded as retired, folded. Nothing else is touched: audit rows, the
// groups and invites they made, and everything stored by their id refer to the
// tombstone. itemID is the person's item being purged: the data is purged only
// while that item is the one that holds the person (users.recycle_item), so that
// purging a stale item never touches a person who is back or who a later
// deletion holds (ErrUserNotDeleted then, and nothing is changed). Call it
// inside Tx.
func PurgeUserData(ctx context.Context, q Q, userID, itemID string, now time.Time) error {
	var username, holder string
	var deleted sql.NullInt64
	if err := q.QueryRow(ctx, `SELECT username, deleted_at, recycle_item FROM users WHERE id = ?`, userID).Scan(&username, &deleted, &holder); err != nil {
		return mapNoRows(err)
	}
	if !deleted.Valid || holder != itemID || itemID == "" {
		return ErrUserNotDeleted
	}
	for _, stmt := range []string{
		`DELETE FROM passwords WHERE user_id = ?`,
		`DELETE FROM passkeys WHERE user_id = ?`,
		`DELETE FROM identities WHERE user_id = ?`,
		`DELETE FROM sessions WHERE user_id = ?`,
		`DELETE FROM email_tokens WHERE user_id = ?`,
		`DELETE FROM favorites WHERE user_id = ?`,
		`DELETE FROM notifications WHERE user_id = ?`,
		`DELETE FROM notification_seen WHERE user_id = ?`,
		`DELETE FROM drafts WHERE user_id = ?`,
		`DELETE FROM group_members WHERE user_id = ?`,
		`DELETE FROM space_members WHERE user_id = ?`,
		`DELETE FROM api_token_spaces WHERE token_id IN (SELECT id FROM api_tokens WHERE user_id = ?)`,
		`DELETE FROM api_tokens WHERE user_id = ?`,
		`DELETE FROM oauth_grant_spaces WHERE grant_id IN (SELECT id FROM oauth_grants WHERE user_id = ?)`,
		`DELETE FROM oauth_grants WHERE user_id = ?`,
		`DELETE FROM github_authorizations WHERE user_id = ?`,
		`DELETE FROM provider_authorizations WHERE user_id = ?`,
		`DELETE FROM provider_leftovers WHERE user_id = ?`,
		`DELETE FROM github_leftovers WHERE user_id = ?`,
		`DELETE FROM usage WHERE kind = 'user' AND id = ?`,
	} {
		if _, err := q.Exec(ctx, stmt, userID); err != nil {
			return err
		}
	}
	if _, err := q.Exec(ctx, `INSERT INTO retired_usernames(name, purged_at) VALUES (?, ?) ON CONFLICT (name) DO NOTHING`, RetiredKey(username), ms(now)); err != nil {
		return err
	}
	res, err := q.Exec(ctx, `UPDATE users SET username = ?, name = '', name_key = '', email = '', email_key = '', email_verified = 0, email_verified_by = '', quota = '', terms_accepted = '',
		passkey_required = 0, recycle_item = '' WHERE id = ? AND deleted_at IS NOT NULL AND recycle_item = ?`, TombstoneName(userID), userID, itemID)
	return oneRow(res, err)
}

// ErrUserNotDeleted: a purge of a person who is not in the recycle bin under
// the item being purged.
var ErrUserNotDeleted = errors.New("the person is not deleted")

// TombstoneName is the username a purged person's row keeps.
func TombstoneName(id string) string { return "deleted-" + id }

// RetiredKey is a username as retired_usernames has it: case folded.
func RetiredKey(username string) string { return FoldName(username) }

// UsernameRetired reports whether a username was ever purged.
func UsernameRetired(ctx context.Context, q Q, username string) (bool, error) {
	var one int
	err := q.QueryRow(ctx, `SELECT 1 FROM retired_usernames WHERE name = ?`, RetiredKey(username)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// SweepTombstones hard-deletes the rows of purged people that nothing refers to
// any more: no audit row names them, they made no invite and used none, no
// recycle item or export is theirs. It returns how many it deleted. The daily
// job runs it; the 90 days of the audit log are the usual last reference.
func SweepTombstones(ctx context.Context, q Q, limit int) (int64, error) {
	res, err := q.Exec(ctx, `DELETE FROM users WHERE id IN (
		SELECT u.id FROM users u WHERE u.deleted_at IS NOT NULL AND u.recycle_item = '' AND u.username = 'deleted-' || u.id
		AND NOT EXISTS (SELECT 1 FROM audit_log a WHERE a.user_id = u.id OR a.actor = u.id)
		AND NOT EXISTS (SELECT 1 FROM invites i WHERE i.created_by = u.id OR i.used_by = u.id)
		AND NOT EXISTS (SELECT 1 FROM recycle_items r WHERE r.user_id = u.id OR r.deleted_by = u.id)
		AND NOT EXISTS (SELECT 1 FROM exports e WHERE e.made_by = u.id)
		AND NOT EXISTS (SELECT 1 FROM spaces s WHERE s.personal_of = u.id OR s.usage_user = u.id)
		LIMIT ?)`, limit)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
