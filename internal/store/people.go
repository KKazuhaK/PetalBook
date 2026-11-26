package store

import (
	"context"
	"strings"
)

// PersonRef is the little that a picker may show of a person: enough to pick
// them, nothing a tenant's directory should give away (no address, no role).
type PersonRef struct {
	ID       string
	Username string
	Name     string
}

// SearchPeople finds the enabled people of the scope whose username starts
// with query or whose name holds it, by name, at most limit of them. It reads
// the users_live_name index of the tenant and stops after its rows.
func SearchPeople(ctx context.Context, q Q, sc Scope, query string, limit int) ([]PersonRef, error) {
	cond, cargs := sc.cond("tenant_id")
	rows, err := q.Query(ctx, `SELECT id, username, name FROM users WHERE deleted_at IS NULL AND disabled = 0 AND `+cond+
		` AND (username LIKE ? ESCAPE '\' OR name_key LIKE ? ESCAPE '\') ORDER BY name_key, id LIMIT ?`,
		append(cargs, likeEscape(strings.ToLower(query))+"%", "%"+likeEscape(FoldName(query))+"%", limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PersonRef{}
	for rows.Next() {
		var p PersonRef
		if err := rows.Scan(&p.ID, &p.Username, &p.Name); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PersonHit is an account a file's row names, with the tenant it is in so that
// the caller can tell a person of another tenant from one nobody has.
type PersonHit struct {
	ID       string
	TenantID string
	Username string
	Name     string
	Disabled bool
}

// FindPeople looks up accounts that are not deleted by username (already
// lower case) and by address (EmailKey form, any address of the account, not
// only the primary one), in groups of 200 keys: one statement per group on
// the unique index of each key, never a scan. It returns them keyed by the
// key that matched.
func FindPeople(ctx context.Context, q Q, usernames, emailKeys []string) (byUsername, byEmail map[string]PersonHit, err error) {
	byUsername, byEmail = map[string]PersonHit{}, map[string]PersonHit{}
	scan := func(query string, keys []string, into map[string]PersonHit) error {
		for i := 0; i < len(keys); i += 200 {
			chunk := keys[i:min(i+200, len(keys))]
			args := make([]any, len(chunk))
			for j, k := range chunk {
				args[j] = k
			}
			rows, err := q.Query(ctx, strings.Replace(query, "(?)", "("+strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")+")", 1), args...)
			if err != nil {
				return err
			}
			for rows.Next() {
				var h PersonHit
				var key string
				var disabled int64
				if err := rows.Scan(&key, &h.ID, &h.TenantID, &h.Username, &h.Name, &disabled); err != nil {
					rows.Close()
					return err
				}
				h.Disabled = disabled != 0
				into[key] = h
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
		}
		return nil
	}
	if err = scan(`SELECT username, id, tenant_id, username, name, disabled FROM users WHERE deleted_at IS NULL AND username IN (?)`, usernames, byUsername); err != nil {
		return nil, nil, err
	}
	err = scan(`SELECT e.email_key, u.id, u.tenant_id, u.username, u.name, u.disabled FROM user_emails e JOIN users u ON u.id = e.user_id
		WHERE u.deleted_at IS NULL AND e.email_key <> '' AND e.email_key IN (?)`, emailKeys, byEmail)
	if err != nil {
		return nil, nil, err
	}
	return byUsername, byEmail, nil
}

// SpaceMembersAmong returns which of the users are members of the space, in
// groups of 200 ids (the primary key of space_members).
func SpaceMembersAmong(ctx context.Context, q Q, spaceID string, ids []string) (map[string]bool, error) {
	in := map[string]bool{}
	for i := 0; i < len(ids); i += 200 {
		chunk := ids[i:min(i+200, len(ids))]
		args := make([]any, 0, len(chunk)+1)
		args = append(args, spaceID)
		for _, id := range chunk {
			args = append(args, id)
		}
		got, err := strCol(ctx, q, `SELECT user_id FROM space_members WHERE space_id = ? AND user_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for _, id := range got {
			in[id] = true
		}
	}
	return in, nil
}

// LocalGroupsByName finds the local groups of the scope by name (compared as
// names are, folded), in groups of 200 names on the group's name index. It
// returns their ids by the folded name.
func LocalGroupsByName(ctx context.Context, q Q, sc Scope, names []string) (map[string]string, error) {
	out := map[string]string{}
	seen := map[string]bool{}
	var keys []string
	for _, n := range names {
		if k := FoldName(strings.TrimSpace(n)); k != "" && !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	for i := 0; i < len(keys); i += 200 {
		chunk := keys[i:min(i+200, len(keys))]
		cond, args := sc.cond("tenant_id")
		args = append(args, GroupLocal)
		for _, k := range chunk {
			args = append(args, k)
		}
		rows, err := q.Query(ctx, `SELECT name_key, id FROM groups WHERE `+cond+` AND source = ? AND name_key IN (`+strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var k, id string
			if err := rows.Scan(&k, &id); err != nil {
				rows.Close()
				return nil, err
			}
			out[k] = id
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}
