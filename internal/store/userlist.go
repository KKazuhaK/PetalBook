package store

// The administration's list of people (milestone 3.10; docs/api.md, "GET
// /api/users"): keyset pages in the style of the storage lists (lists.go), a
// search, filters and three sorts, each over an index of users. The per-row
// counts (passkeys, linked accounts) are read for the ids of the page alone.

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

var userSorts = []sortSpec{
	{name: "created", col: "created"},
	{name: "name", col: "name_key", text: true},
	{name: "lastActive", col: "last_active", desc: true, none: "last_active IS NULL", has: "last_active IS NOT NULL"},
}

// UserSorts, UserRoles and UserStates are the names ListUserRows accepts.
var (
	UserSorts  = []string{"created", "name", "lastActive"}
	UserRoles  = []string{"member", "admin", "site"}
	UserStates = []string{"enabled", "disabled"}
)

// UserFilter picks a page of ListUserRows. The filters combine.
type UserFilter struct {
	// Query keeps the people whose username or name contains it, ignoring case.
	Query string
	// Role keeps "member"s, "admin"istrators (of a tenant or the server) or the
	// administrators of the whole server ("site").
	Role string
	// State keeps the "enabled" or the "disabled" accounts.
	State string
	// InactiveBefore keeps those last active before it, or never; ActiveSince
	// those active at or after it.
	InactiveBefore, ActiveSince time.Time
	// Sort and Order: one of UserSorts and "asc" or "desc" ("" for the sort's own).
	Sort, Order string
	// After is the cursor of the page before ("" for the first); Limit the page
	// size (at most one more row is read to learn whether there is another page).
	After string
	Limit int
}

// UserRow is a person as the list shows them.
type UserRow struct {
	User
	LastActive time.Time // zero: never
	// Quota is the person's own limits as stored ("" when they follow the tenant).
	Quota string
	// Passkeys and Identities count what they hold.
	Passkeys, Identities int
}

// ListUserRows returns one page of the people in the scope who are not deleted.
func ListUserRows(ctx context.Context, q Q, sc Scope, f UserFilter) (out []UserRow, next string, err error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Sort == "" {
		f.Sort = "created"
	}
	k, ok := pickSort("users", userSorts, f.Sort, f.Order)
	if !ok {
		return nil, "", ErrBadFilter
	}
	cond, cargs := sc.cond("tenant_id")
	where := []string{`deleted_at IS NULL`, cond}
	args := append([]any(nil), cargs...)
	add := func(c string, a ...any) { where, args = append(where, c), append(args, a...) }
	if f.Query != "" {
		c, a := userMatches(f.Query)
		add(c, a...)
	}
	switch f.Role {
	case "":
	case "member", "admin":
		add(`role = ?`, f.Role)
	case "site":
		add(`role = 'admin' AND site_admin = 1`)
	default:
		return nil, "", ErrBadFilter
	}
	switch f.State {
	case "":
	case "enabled":
		add(`disabled = 0`)
	case "disabled":
		add(`disabled <> 0`)
	default:
		return nil, "", ErrBadFilter
	}
	if !f.InactiveBefore.IsZero() {
		add(`(last_active IS NULL OR last_active < ?)`, ms(f.InactiveBefore))
	}
	if !f.ActiveSince.IsZero() {
		add(`last_active >= ?`, ms(f.ActiveSince))
	}
	pq := pageQuery{k: k, idCol: "id", cursor: f.After, limit: f.Limit}
	var vals []any
	err = pq.run(func(extra string, eargs []any, orderBy string, n int, none bool) (int, error) {
		w := append(append([]string(nil), where...), extra)
		a := append(append([]any(nil), args...), eargs...)
		a = append(a, n)
		rows, err := q.Query(ctx, `SELECT `+userCols+`, name_key, last_active, quota FROM users WHERE `+strings.Join(nonEmpty(w), " AND ")+` ORDER BY `+orderBy+` LIMIT ?`, a...)
		if err != nil {
			return 0, err
		}
		defer rows.Close()
		got := 0
		for rows.Next() {
			var e UserRow
			var disabled, created, verified, required, siteAdmin, exempt int64
			var active sql.NullInt64
			var key string
			if err := rows.Scan(&e.ID, &e.Username, &e.Name, &e.Email, &e.Role, &disabled, &created, &verified, &required, &e.TenantID, &siteAdmin, &exempt,
				&key, &active, &e.Quota); err != nil {
				return 0, err
			}
			e.Disabled, e.Created, e.EmailVerified, e.PasskeyRequired = disabled != 0, fromMS(created), verified != 0, required != 0
			e.SiteAdmin, e.RemoteExempt = siteAdmin != 0, exempt != 0
			var v any
			switch f.Sort {
			case "name":
				v = key
			case "lastActive":
				v = int64(0)
				if active.Valid {
					e.LastActive = fromMS(active.Int64)
					v = active.Int64
				}
			default:
				v = created
			}
			out = append(out, e)
			vals = append(vals, v)
			got++
		}
		return got, rows.Err()
	})
	if err != nil {
		return nil, "", err
	}
	if len(out) > f.Limit {
		last := out[f.Limit-1]
		next = k.cursorOf(vals[f.Limit-1], last.ID, f.Sort == "lastActive" && last.LastActive.IsZero())
		out = out[:f.Limit]
	}
	if len(out) == 0 {
		return out, next, nil
	}
	ids := make([]any, len(out))
	for i, e := range out {
		ids[i] = e.ID
	}
	pk, err := countByUser(ctx, q, `passkeys`, ids)
	if err != nil {
		return nil, "", err
	}
	idn, err := countByUser(ctx, q, `linked_accounts`, ids, `signin = 1`)
	if err != nil {
		return nil, "", err
	}
	for i := range out {
		out[i].Passkeys, out[i].Identities = pk[out[i].ID], idn[out[i].ID]
	}
	return out, next, nil
}

// countByUser counts the rows of a table that hangs under users, for the given
// ids only (by the table's index on user_id).
func countByUser(ctx context.Context, q Q, table string, ids []any, and ...string) (map[string]int, error) {
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", ")
	rows, err := q.Query(ctx, `SELECT user_id, COUNT(*) FROM `+table+` WHERE user_id IN (`+marks+`)`+strings.Join(append([]string{""}, and...), " AND ")+` GROUP BY user_id`, ids...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// GetUserRow is one person as the list shows them, or ErrNotFound (a person
// outside the scope is not found).
func GetUserRow(ctx context.Context, q Q, sc Scope, id string) (UserRow, error) {
	u, err := GetUser(ctx, q, sc, id)
	if err != nil {
		return UserRow{}, err
	}
	e := UserRow{User: *u}
	var active sql.NullInt64
	if err := q.QueryRow(ctx, `SELECT last_active, quota FROM users WHERE id = ? AND deleted_at IS NULL`, id).Scan(&active, &e.Quota); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return UserRow{}, ErrNotFound // deleted between the two reads
		}
		return UserRow{}, err
	}
	if active.Valid {
		e.LastActive = fromMS(active.Int64)
	}
	ids := []any{id}
	pk, err := countByUser(ctx, q, `passkeys`, ids)
	if err != nil {
		return UserRow{}, err
	}
	idn, err := countByUser(ctx, q, `linked_accounts`, ids, `signin = 1`)
	if err != nil {
		return UserRow{}, err
	}
	e.Passkeys, e.Identities = pk[id], idn[id]
	return e, nil
}

// userMatches is the search of the users list: the username, the name, or the
// address (by its key, which is lower case).
func userMatches(query string) (string, []any) {
	pat := "%" + likeEscape(FoldName(query)) + "%"
	email := "%" + likeEscape(strings.ToLower(query)) + "%"
	return `(username LIKE ? ESCAPE '\' OR name_key LIKE ? ESCAPE '\' OR email_key LIKE ? ESCAPE '\')`, []any{pat, pat, email}
}
