package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
)

// SpaceFingerprints says, for each of the spaces in the scope, what a plan that
// touches it was made from: its state, its owners and its members with their
// roles. A confirmation token is bound to the hash of these (docs/api.md,
// "Confirmations and bulk actions"), so that a member who was added since the
// plan was shown makes the token stale. A space that is not in the scope is not
// in the map. Two queries for any number of spaces.
func SpaceFingerprints(ctx context.Context, q Q, sc Scope, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	cond, args := sc.spaceCond("tenant_id", "personal_of IS NULL")
	ph := strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", ")
	idArgs := make([]any, len(ids))
	for i, id := range ids {
		idArgs[i] = id
	}
	rows, err := q.Query(ctx, `SELECT id, tenant_id, disabled, cache_state, pinned, exempt, COALESCE(personal_of, '') FROM spaces WHERE `+cond+` AND id IN (`+ph+`)`, append(args, idArgs...)...)
	if err != nil {
		return nil, err
	}
	base := map[string]string{}
	for rows.Next() {
		var id, tenant, cache, personal string
		var disabled, pinned, exempt int64
		if err := rows.Scan(&id, &tenant, &disabled, &cache, &pinned, &exempt, &personal); err != nil {
			rows.Close()
			return nil, err
		}
		base[id] = strings.Join([]string{id, tenant, itoa(disabled), cache, itoa(pinned), itoa(exempt), personal}, "|")
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	mrows, err := q.Query(ctx, `SELECT space_id, user_id, role FROM space_members WHERE space_id IN (`+ph+`) ORDER BY space_id, user_id`, idArgs...)
	if err != nil {
		return nil, err
	}
	defer mrows.Close()
	members := map[string][]string{}
	for mrows.Next() {
		var sid, uid, role string
		if err := mrows.Scan(&sid, &uid, &role); err != nil {
			return nil, err
		}
		members[sid] = append(members[sid], uid+":"+role)
	}
	if err := mrows.Err(); err != nil {
		return nil, err
	}
	for id, b := range base {
		m := members[id]
		sort.Strings(m)
		out[id] = b + "|" + strings.Join(m, ",")
	}
	return out, nil
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// DeletionFacts are the stored values the preview of deleting a space shows.
type DeletionFacts struct {
	Pages, Bytes, HeldBytes int64
	Members, Groups, Drafts int
	Remote, AssetsRemote    string // the addresses; "" for none
	RemoteState             string
}

// GetDeletionFacts reads, for a space in the scope, the numbers of its
// deletion preview: stored values and counts on the space's own indexes, never
// a file.
func GetDeletionFacts(ctx context.Context, q Q, sc Scope, id string) (DeletionFacts, error) {
	var f DeletionFacts
	cond, args := sc.spaceCond("s.tenant_id", "s.personal_of IS NULL")
	var size *int64
	var index int64
	err := q.QueryRow(ctx, `SELECT s.page_count, s.usage_bytes, s.size_bytes, s.index_bytes,
		(SELECT COUNT(*) FROM space_members m WHERE m.space_id = s.id),
		(SELECT COUNT(*) FROM space_groups g WHERE g.space_id = s.id),
		(SELECT COUNT(*) FROM drafts d WHERE d.space_id = s.id),
		COALESCE((SELECT r.url FROM remotes r WHERE r.space_id = s.id), ''),
		COALESCE((SELECT r.state FROM remotes r WHERE r.space_id = s.id AND r.url <> ''), ''),
		COALESCE((SELECT r.url FROM asset_remotes r WHERE r.space_id = s.id), '')
		FROM spaces s WHERE s.id = ? AND `+cond, append([]any{id}, args...)...).
		Scan(&f.Pages, &f.Bytes, &size, &index, &f.Members, &f.Groups, &f.Drafts, &f.Remote, &f.RemoteState, &f.AssetsRemote)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return f, ErrNotFound
		}
		return f, err
	}
	if size != nil {
		f.HeldBytes = *size
	}
	f.HeldBytes += index
	return f, nil
}

// UserFingerprints is SpaceFingerprints for people: their state, their role, and
// the spaces they belong to with their roles there, for the plans of deleting,
// disabling and enabling people. A person who is not in the scope (or is
// deleted) is not in the map.
func UserFingerprints(ctx context.Context, q Q, sc Scope, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	cond, args := sc.cond("tenant_id")
	ph := strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", ")
	idArgs := make([]any, len(ids))
	for i, id := range ids {
		idArgs[i] = id
	}
	rows, err := q.Query(ctx, `SELECT id, tenant_id, role, disabled, site_admin FROM users WHERE deleted_at IS NULL AND `+cond+` AND id IN (`+ph+`)`, append(args, idArgs...)...)
	if err != nil {
		return nil, err
	}
	base := map[string]string{}
	for rows.Next() {
		var id, tenant, role string
		var disabled, site int64
		if err := rows.Scan(&id, &tenant, &role, &disabled, &site); err != nil {
			rows.Close()
			return nil, err
		}
		base[id] = strings.Join([]string{id, tenant, role, itoa(disabled), itoa(site)}, "|")
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	mrows, err := q.Query(ctx, `SELECT user_id, space_id, role FROM space_members WHERE user_id IN (`+ph+`) ORDER BY user_id, space_id`, idArgs...)
	if err != nil {
		return nil, err
	}
	defer mrows.Close()
	member := map[string][]string{}
	for mrows.Next() {
		var uid, sid, role string
		if err := mrows.Scan(&uid, &sid, &role); err != nil {
			return nil, err
		}
		member[uid] = append(member[uid], sid+":"+role)
	}
	if err := mrows.Err(); err != nil {
		return nil, err
	}
	for id, b := range base {
		out[id] = b + "|" + strings.Join(member[id], ",")
	}
	return out, nil
}
