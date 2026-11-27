package store

// The lists of the storage administration (milestone 3.5, part 4; docs/api.md,
// "Storage management"): spaces and people, with filters, sorting and cursors.
//
// Every list is one query a page over an index, whatever the page: the cursor is
// keyset ("<sort>.<order>:<value>:<id>"), never an offset. A sort whose value
// can be missing (a space never measured, a person never active) lists the rows
// that have one first and the rest after them in id order, as two ranges of the
// same index; the cursor says which range it is in. The page is cut in a derived
// table before the owners and member counts are looked up, so the work is per
// row returned, not per row on the server.

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"
)

// sortSpec describes one sort of a list.
type sortSpec struct {
	name string
	col  string // the column (with its alias), compared and ordered by
	// desc says the index is built with the column descending, which is the
	// order the list gives by default: the other order reads the index backwards.
	desc bool
	text bool // the value is text, not a number
	// none, for a sort whose value can be missing, is the SQL that says it is
	// ("" when it cannot be); the rows that have a value come first, which has
	// says in a form an index range can use.
	none, has string
}

// keyOrder is a sort and a direction.
type keyOrder struct {
	// list names the list the cursors are for: a cursor of another list is bad.
	list  string
	spec  sortSpec
	order string // "asc" or "desc"
}

func pickSort(list string, specs []sortSpec, name, order string) (keyOrder, bool) {
	for _, sp := range specs {
		if sp.name != name {
			continue
		}
		if order == "" {
			order = "asc"
			if sp.desc {
				order = "desc"
			}
		}
		if order != "asc" && order != "desc" {
			return keyOrder{}, false
		}
		return keyOrder{list, sp, order}, true
	}
	return keyOrder{}, false
}

// idAsc is the direction of the id inside equal values: the index's own when the
// list reads it forwards, the opposite when it reads it backwards.
func (k keyOrder) idAsc() bool { return (k.order == "desc") == k.spec.desc }

func (k keyOrder) orderBy(idCol string) string {
	dir := "ASC"
	if k.order == "desc" {
		dir = "DESC"
	}
	idDir := "DESC"
	if k.idAsc() {
		idDir = "ASC"
	}
	return k.spec.col + " " + dir + ", " + idCol + " " + idDir
}

func (k keyOrder) tag() string { return k.list + "." + k.spec.name + "." + k.order }

// cursorOf is the cursor of the page that continues after a row whose sort value
// is v (a string or an int64) and id, in the range of rows without a value
// when none is set.
func (k keyOrder) cursorOf(v any, id string, none bool) string {
	if none {
		return k.tag() + ".none::" + id
	}
	switch x := v.(type) {
	case int64:
		return k.tag() + ":" + strconv.FormatInt(x, 10) + ":" + id
	case string:
		return k.tag() + ":" + x + ":" + id
	}
	panic("cursorOf: a value of a sort is a number or text")
}

// after is the condition that continues a page after the cursor, whether the
// cursor is in the range without values, and its arguments; a cursor that was
// made for another sort, order or list is ErrBadCursor.
func (k keyOrder) after(cursor, idCol string) (cond string, args []any, none bool, err error) {
	head, rest, ok := strings.Cut(cursor, ":")
	if !ok {
		return "", nil, false, ErrBadCursor
	}
	switch head {
	case k.tag() + ".none":
		if k.spec.none == "" || !strings.HasPrefix(rest, ":") || len(rest) < 2 {
			return "", nil, false, ErrBadCursor
		}
		return idCol + " > ?", []any{rest[1:]}, true, nil
	case k.tag():
	default:
		return "", nil, false, ErrBadCursor
	}
	i := strings.LastIndex(rest, ":")
	if i < 0 || i == len(rest)-1 {
		return "", nil, false, ErrBadCursor
	}
	val, id := rest[:i], rest[i+1:]
	var v any = val
	if !k.spec.text {
		n, perr := strconv.ParseInt(val, 10, 64)
		if perr != nil {
			return "", nil, false, ErrBadCursor
		}
		v = n
	}
	cmp, idCmp := ">", "<"
	if k.order == "desc" {
		cmp = "<"
	}
	if k.idAsc() {
		idCmp = ">"
	}
	// The bound on the column alone is what the index is searched by; the rest
	// decides the rows that tie with the cursor, among a few. (As one OR the
	// planner may take two ranges and sort their union.)
	return k.spec.col + " " + cmp + "= ? AND (" + k.spec.col + " " + cmp + " ? OR " + idCol + " " + idCmp + " ?)", []any{v, v, id}, false, nil
}

// pageQuery runs a page of a list in its ranges: the rows that have a sort value
// (valued, which gets the keyset condition of the cursor when it is in that
// range), then, if the page is not full, the rows that have none (missing). run
// reads rows by the extra condition and arguments, in the order and up to the
// limit it is given.
type pageQuery struct {
	k      keyOrder
	idCol  string
	cursor string
	limit  int
}

// run calls read for each range it needs: read(extraCond, extraArgs, orderBy, n)
// appends up to n rows and reports each row's (value, id, none) through add.
func (pq pageQuery) run(read func(cond string, args []any, orderBy string, n int, none bool) (int, error)) error {
	var cond string
	var args []any
	inNone := false
	if pq.cursor != "" {
		var err error
		if cond, args, inNone, err = pq.k.after(pq.cursor, pq.idCol); err != nil {
			return err
		}
	}
	want := pq.limit + 1
	if !inNone {
		c := cond
		if pq.k.spec.none != "" {
			c = joinAnd(pq.k.spec.has, c)
		}
		n, err := read(c, args, pq.k.orderBy(pq.idCol), want, false)
		if err != nil {
			return err
		}
		want -= n
		cond, args = "", nil
	}
	if pq.k.spec.none == "" || want <= 0 {
		return nil
	}
	_, err := read(joinAnd("("+pq.k.spec.none+")", cond), args, pq.idCol+" ASC", want, true)
	return err
}

func joinAnd(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + " AND " + b
}

// --- spaces ----------------------------------------------------------------------

var spaceSorts = []sortSpec{
	{name: "created", col: "s.created"},
	{name: "name", col: "s.name_key", text: true},
	{name: "bytes", col: "s.size_bytes", desc: true, none: "s.size_bytes IS NULL", has: "s.size_bytes IS NOT NULL"},
	{name: "lastUsed", col: "s.last_used", desc: true, none: "s.last_used = 0", has: "s.last_used > 0"},
}

// SpaceSort and the like are the names ListAllSpaces accepts.
var (
	SpaceSorts    = []string{"created", "name", "bytes", "lastUsed"}
	SpaceCaches   = []string{"local", "full", "partial", "evicted", "archived", "restoring"}
	SpaceRemotes  = []string{"none", "ok", "offline", "error"}
	SpaceDeadline = []string{"grace", "overdue"}
)

// SpaceFilter picks a page of ListAllSpaces. The filters combine.
type SpaceFilter struct {
	// Query keeps the spaces whose name contains it, ignoring case. The search
	// compares the folded name (FoldName) the space was written with, so both
	// databases find the same spaces.
	Query string
	// ID keeps only the space with this id: the same row the list has, found
	// directly.
	ID string
	// Owner keeps the spaces counted for this person (their first owner).
	Owner string
	// Cache keeps the spaces in this cache state: "local" (no remote), "evicted",
	// "partial", "archived" or "full" (a space that follows a remote and is none
	// of the others). "restoring" is not stored and keeps nothing.
	Cache string
	// Remote keeps the spaces by where their remote stands: "none" (no remote),
	// "ok", "offline", or "error" (an error, or a rewritten history).
	Remote string
	// Deadline, with Policy, keeps the spaces the policy asks for a remote that
	// they lack and whose deadline has not passed ("grace") or has ("overdue").
	Deadline string
	Policy   *RemotePolicy
	// Pinned and Exempt keep the pinned spaces, and those an administrator
	// exempted.
	Pinned, Exempt bool
	// IdleBefore keeps the spaces last used before it, or never (idleDays).
	IdleBefore time.Time
	// Sort and Order: one of SpaceSorts, and "asc" or "desc" ("" for the default
	// of the sort).
	Sort, Order string
	// After is the cursor of the page before ("" for the first).
	After string
	// Limit is the page size; at most one more row is read to learn whether
	// there is another page.
	Limit int
}

// deadlineCond is the SQL that keeps the spaces in their grace period or past
// their deadline under a policy: a range of created, and the rest are columns
// (the space lacks what is required, is not exempt, and has no exempt owner).
func deadlineCond(p RemotePolicy, which string) (string, []any) {
	if !p.Active() {
		return "1 = 0", nil
	}
	// A GitHub remote that lost its access (its installation is no longer the
	// owner's) counts as none: only spaces that follow GitHub are looked at.
	lost := `(s.remote_via = 'github' AND EXISTS (SELECT 1 FROM remotes r WHERE r.space_id = s.id AND r.role = 'content' AND r.url <> '' AND NOT ` + usableRemote + `))`
	lacks, args := `(s.remote_via = '' OR `+lost+`)`, []any(nil)
	if p.Require != "any" {
		lacks, args = `(s.remote_via <> ? OR `+lost+`)`, []any{p.Require}
	}
	exempt := `s.exempt = 0 AND NOT EXISTS (SELECT 1 FROM space_members om JOIN users ou ON ou.id = om.user_id WHERE om.space_id = s.id AND om.role = 'owner' AND ou.remote_exempt <> 0)`
	since, now, grace := ms(p.Since), ms(p.Now), p.Grace.Milliseconds()
	// A space made since the requirement began is due at created + grace; an
	// older one (when the requirement is for every space) at since + grace.
	var parts []string
	var pargs []any
	if which == "overdue" {
		parts, pargs = append(parts, `(s.created >= ? AND s.created < ?)`), append(pargs, since, now-grace)
		if p.All && since+grace < now {
			parts, pargs = append(parts, `s.created < ?`), append(pargs, since)
		}
	} else {
		from := now - grace
		if since > from {
			from = since
		}
		parts, pargs = append(parts, `s.created >= ?`), append(pargs, from)
		if p.All && since+grace >= now {
			parts, pargs = append(parts, `s.created < ?`), append(pargs, since)
		}
	}
	return lacks + ` AND ` + exempt + ` AND (` + strings.Join(parts, " OR ") + `)`, append(args, pargs...)
}

// ListAllSpaces returns one page of the spaces in the scope: the first owner and
// the number of members come with the page only. next is the cursor of the
// following page, "" at the end. A sort or order it does not know, or a cursor
// made for another sort, is ErrBadCursor or ErrBadFilter.
func ListAllSpaces(ctx context.Context, q Q, sc Scope, f SpaceFilter) (out []AdminSpace, next string, err error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Sort == "" {
		f.Sort = "created"
	}
	k, ok := pickSort("spaces", spaceSorts, f.Sort, f.Order)
	if !ok {
		return nil, "", ErrBadFilter
	}
	var where []string
	var args []any
	sCond, sArgs := sc.spaceCond("s.tenant_id", "s.personal_of IS NULL")
	where, args = append(where, sCond), append(args, sArgs...)
	add := func(c string, a ...any) { where, args = append(where, c), append(args, a...) }
	if f.ID != "" {
		add(`s.id = ?`, f.ID)
	}
	if f.Owner != "" {
		add(`s.usage_user = ?`, f.Owner)
	}
	if f.Query != "" {
		add(`s.name_key LIKE ? ESCAPE '\'`, "%"+likeEscape(FoldName(f.Query))+"%")
	}
	switch f.Cache {
	case "":
	case "local":
		add(`s.remote_via = ''`)
	case "evicted", "partial", "archived":
		add(`s.remote_via <> '' AND s.cache_state = '` + f.Cache + `'`)
	case "full":
		add(`s.remote_via <> '' AND s.cache_state NOT IN ('evicted', 'partial', 'archived')`)
	case "restoring":
		add(`1 = 0`) // not stored: it is in memory
	default:
		return nil, "", ErrBadFilter
	}
	switch f.Remote {
	case "":
	case "none":
		add(`s.remote_via = ''`)
	case "ok", "offline":
		add(`s.remote_state = ?`, f.Remote)
	case "error":
		add(`s.remote_state IN ('error', 'rewritten')`)
	default:
		return nil, "", ErrBadFilter
	}
	switch f.Deadline {
	case "":
	case "grace", "overdue":
		if f.Policy == nil {
			add(`1 = 0`)
		} else {
			c, a := deadlineCond(*f.Policy, f.Deadline)
			add(c, a...)
		}
	default:
		return nil, "", ErrBadFilter
	}
	if f.Pinned {
		add(`s.pinned <> 0`)
	}
	if f.Exempt {
		add(`s.exempt <> 0`)
	}
	if !f.IdleBefore.IsZero() {
		add(`s.last_used < ?`, ms(f.IdleBefore))
	}
	pq := pageQuery{k: k, idCol: "s.id", cursor: f.After, limit: f.Limit}
	var vals []any // the sort value of each row, for the cursor
	err = pq.run(func(extra string, eargs []any, orderBy string, n int, none bool) (int, error) {
		w := append(append([]string(nil), where...), extra)
		a := append(append([]any(nil), args...), eargs...)
		a = append(a, n)
		got, err := queryAdminSpaces(ctx, q, strings.Join(nonEmpty(w), " AND "), orderBy, a)
		if err != nil {
			return 0, err
		}
		for _, r := range got {
			vals = append(vals, sortValueOf(f.Sort, r))
		}
		out = append(out, got...)
		return len(got), nil
	})
	if err != nil {
		return nil, "", err
	}
	if len(out) > f.Limit {
		last := out[f.Limit-1]
		next = k.cursorOf(vals[f.Limit-1], last.ID, sortMissing(f.Sort, last))
		out = out[:f.Limit]
	}
	return out, next, nil
}

func nonEmpty(l []string) []string {
	var out []string
	for _, s := range l {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// sortValueOf is the value of the sort in a row, as the cursor keeps it.
func sortValueOf(sort string, r AdminSpace) any {
	switch sort {
	case "name":
		return r.NameKey
	case "bytes":
		if r.SizeBytes != nil {
			return *r.SizeBytes
		}
		return int64(0)
	case "lastUsed":
		if r.Storage.LastUsed.IsZero() {
			return int64(0)
		}
		return ms(r.Storage.LastUsed)
	}
	return ms(r.Created)
}

// sortMissing says the row is in the range of rows without a value of the sort.
func sortMissing(sort string, r AdminSpace) bool {
	switch sort {
	case "bytes":
		return r.SizeBytes == nil
	case "lastUsed":
		return r.Storage.LastUsed.IsZero()
	}
	return false
}

func queryAdminSpaces(ctx context.Context, q Q, where, orderBy string, args []any) (out []AdminSpace, err error) {
	rows, err := q.Query(ctx, `SELECT p.id, p.name, p.personal_of, p.inbox, p.created, p.disabled, p.size_bytes,
			(SELECT sm.size_at FROM space_measure sm WHERE sm.space_id = p.id), p.members, p.tenant_id, u.id, u.username, u.name,
			p.last_used, p.cache_state, p.pinned, p.exempt, p.index_bytes, p.cred_type, p.provider, p.owner_exempt, p.has_assets, p.location, p.remote_state, p.name_key
		FROM (SELECT s.id, s.name, s.name_key, s.personal_of, s.inbox, s.created, s.disabled, s.size_bytes, s.tenant_id,
			s.last_used, s.cache_state, s.pinned, s.exempt, s.index_bytes, s.location, s.remote_state,
			COALESCE((SELECT r.cred_type FROM remotes r WHERE r.space_id = s.id AND r.role = 'content' AND r.url <> '' AND `+usableRemote+`), '') AS cred_type,
			COALESCE((SELECT r.provider FROM remotes r WHERE r.space_id = s.id AND r.role = 'content' AND r.url <> '' AND `+usableRemote+`), '') AS provider,
			(SELECT COUNT(*) FROM remotes ar WHERE ar.space_id = s.id AND ar.role = 'assets' AND ar.url <> '') AS has_assets,
			(SELECT COUNT(*) FROM space_members om JOIN users ou ON ou.id = om.user_id WHERE om.space_id = s.id AND om.role = 'owner' AND ou.remote_exempt <> 0) AS owner_exempt,
			(SELECT COUNT(*) FROM space_members m WHERE m.space_id = s.id) AS members,
			(SELECT m.user_id FROM space_members m WHERE m.space_id = s.id AND m.role = 'owner' ORDER BY m.created, m.user_id LIMIT 1) AS owner_id
			FROM spaces s WHERE `+where+` ORDER BY `+orderBy+` LIMIT ?) p
		LEFT JOIN users u ON u.id = p.owner_id
		ORDER BY `+strings.ReplaceAll(strings.ReplaceAll(orderBy, "s.id", "p.id"), "s.", "p."), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var s AdminSpace
		var personal, ownerID, ownerUser, ownerName sql.NullString
		var created, disabled int64
		var size, sizeAt sql.NullInt64
		var used, pinned, exempt, ownerExempt, hasAssets int64
		if err := rows.Scan(&s.ID, &s.Name, &personal, &s.Inbox, &created, &disabled, &size, &sizeAt, &s.Members, &s.TenantID, &ownerID, &ownerUser, &ownerName,
			&used, &s.Storage.Cache, &pinned, &exempt, &s.Storage.IndexBytes, &s.CredType, &s.Provider, &ownerExempt, &hasAssets, &s.Location, &s.RemoteState, &s.NameKey); err != nil {
			return nil, err
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
	return out, rows.Err()
}

// --- people ----------------------------------------------------------------------

var personSorts = []sortSpec{
	{name: "held", col: "p.held_bytes", desc: true},
	{name: "bytes", col: "p.bytes", desc: true},
	{name: "lastActive", col: "p.last_active", desc: true, none: "p.last_active = 0", has: "p.last_active > 0"},
	{name: "created", col: "p.created"},
	{name: "spaces", col: "p.spaces", desc: true},
}

// PersonSorts and the like are the names ListPeople accepts.
var (
	PersonSorts   = []string{"held", "bytes", "lastActive", "created", "spaces"}
	PersonRemotes = []string{"none", "some"}
)

// PersonUsage is one person with what the server holds for them: their usage row
// and the person.
type PersonUsage struct {
	ID, TenantID, Username, Name string
	Disabled                     bool
	Created                      time.Time
	LastActive, FirstWrite       time.Time // zero: never
	Dormant, Connected           bool
	Spaces, RemoteSpaces         int64
	EvictedSpaces, PinnedSpaces  int64
	GraceSpaces, OverdueSpaces   int64
	UsageBytes, HeldBytes        int64
	Measured                     time.Time // zero: never
}

// PeopleFilter picks a page of ListPeople. The filters combine.
type PeopleFilter struct {
	// ID keeps one person.
	ID string
	// Query keeps the people whose username or name contains it, ignoring case.
	Query string
	// Dormant keeps the people who never used the server nor connected anything.
	Dormant bool
	// Remote: "none" (no space follows a remote) or "some".
	Remote string
	// Deadline: "grace" (a space of theirs is in its grace period) or "overdue".
	Deadline string
	// InactiveBefore keeps the people last active before it, or never.
	InactiveBefore time.Time
	// Disabled keeps the disabled (true) or the enabled (false) people.
	Disabled    *bool
	Sort, Order string
	After       string
	Limit       int
}

// ListPeople returns one page of the people in the scope with what they hold,
// by the usage rows' indexes. A person who is deleted is not listed.
func ListPeople(ctx context.Context, q Q, d Dialect, sc Scope, f PeopleFilter) (out []PersonUsage, next string, err error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Sort == "" {
		f.Sort = "held"
	}
	k, ok := pickSort("people", personSorts, f.Sort, f.Order)
	if !ok {
		return nil, "", ErrBadFilter
	}
	cond, cargs := sc.cond("p.tenant_id")
	where := []string{`p.kind = 'user'`, cond, `u.deleted_at IS NULL`}
	args := append([]any(nil), cargs...)
	add := func(c string, a ...any) { where, args = append(where, c), append(args, a...) }
	if f.ID != "" {
		add(`p.id = ?`, f.ID)
	}
	if f.Query != "" {
		c, a := personMatches("u.", f.Query)
		add(c, a...)
	}
	if f.Dormant {
		add(`p.dormant = 1`)
	}
	switch f.Remote {
	case "":
	case "none":
		add(`p.remote_spaces = 0`)
	case "some":
		add(`p.remote_spaces > 0`)
	default:
		return nil, "", ErrBadFilter
	}
	switch f.Deadline {
	case "":
	case "grace":
		add(`p.grace_spaces > 0`)
	case "overdue":
		add(`p.overdue_spaces > 0`)
	default:
		return nil, "", ErrBadFilter
	}
	if !f.InactiveBefore.IsZero() {
		add(`p.last_active < ?`, ms(f.InactiveBefore))
	}
	if f.Disabled != nil {
		add(`u.disabled = ?`, b2i(*f.Disabled))
	}
	pq := pageQuery{k: k, idCol: "p.id", cursor: f.After, limit: f.Limit}
	var vals []any
	err = pq.run(func(extra string, eargs []any, orderBy string, n int, none bool) (int, error) {
		w := append(append([]string(nil), where...), extra)
		a := append(append([]any(nil), args...), eargs...)
		a = append(a, n)
		rows, err := q.Query(ctx, `SELECT p.id, p.tenant_id, u.username, u.name, u.disabled, p.created, p.last_active, u.first_write, p.dormant, p.connected,
				p.spaces, p.remote_spaces, p.evicted_spaces, p.pinned_spaces, p.grace_spaces, p.overdue_spaces, p.bytes, p.held_bytes, p.measured
			FROM usage p JOIN users u ON u.id = p.id WHERE `+strings.Join(nonEmpty(w), " AND ")+` ORDER BY `+orderBy+` LIMIT ?`, a...)
		if err != nil {
			return 0, err
		}
		defer rows.Close()
		got := 0
		for rows.Next() {
			var e PersonUsage
			var disabled, created, active, dormant, connected, measured int64
			var first sql.NullInt64
			if err := rows.Scan(&e.ID, &e.TenantID, &e.Username, &e.Name, &disabled, &created, &active, &first, &dormant, &connected,
				&e.Spaces, &e.RemoteSpaces, &e.EvictedSpaces, &e.PinnedSpaces, &e.GraceSpaces, &e.OverdueSpaces, &e.UsageBytes, &e.HeldBytes, &measured); err != nil {
				return 0, err
			}
			e.Disabled, e.Created, e.Dormant, e.Connected = disabled != 0, fromMS(created), dormant != 0, connected != 0
			if active > 0 {
				e.LastActive = fromMS(active)
			}
			if first.Valid {
				e.FirstWrite = fromMS(first.Int64)
			}
			if measured > 0 {
				e.Measured = fromMS(measured)
			}
			out = append(out, e)
			vals = append(vals, personSortValue(f.Sort, e))
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
	return out, next, nil
}

func personSortValue(sort string, e PersonUsage) any {
	switch sort {
	case "bytes":
		return e.UsageBytes
	case "lastActive":
		if e.LastActive.IsZero() {
			return int64(0)
		}
		return ms(e.LastActive)
	case "created":
		return ms(e.Created)
	case "spaces":
		return e.Spaces
	}
	return e.HeldBytes
}

// GetPerson returns one person as the list shows them, or ErrNotFound.
func GetPerson(ctx context.Context, q Q, d Dialect, sc Scope, id string) (PersonUsage, error) {
	rows, _, err := ListPeople(ctx, q, d, sc, PeopleFilter{ID: id, Limit: 1})
	if err != nil {
		return PersonUsage{}, err
	}
	if len(rows) == 0 {
		return PersonUsage{}, ErrNotFound
	}
	return rows[0], nil
}

// --- the summary -----------------------------------------------------------------

// StorageSummary is the totals of the storage administration for a tenant or the
// whole server: the usage rows' sums, never a walk of anything.
type StorageSummary struct {
	Local, Full, Partial, Evicted, Archived int64
	People, Dormant                         int64
	HeldBytes, UsageBytes                   int64
	InGrace, Overdue                        int64
	Measured                                time.Time // zero: never
	RecycleItems, RecycleBytes              int64
	ExportItems, ExportBytes                int64
}

// GetStorageSummary reads the summary of the scope: one usage row for a tenant,
// one aggregate over the tenants' rows for every tenant. The recycle bin and the
// exports are counted in the scope as their lists are (a tenant administrator
// sees neither a personal space's item nor a site administrator's export).
func GetStorageSummary(ctx context.Context, q Q, sc Scope) (StorageSummary, error) {
	var s StorageSummary
	cond, args := sc.cond("tenant_id")
	var spaces, remote, partial, measured int64
	err := q.QueryRow(ctx, `SELECT COALESCE(SUM(spaces), 0), COALESCE(SUM(remote_spaces), 0), COALESCE(SUM(partial_spaces), 0), COALESCE(SUM(evicted_spaces), 0),
		COALESCE(SUM(archived_spaces), 0), COALESCE(SUM(people), 0), COALESCE(SUM(dormant), 0), COALESCE(SUM(held_bytes), 0), COALESCE(SUM(bytes), 0),
		COALESCE(SUM(grace_spaces), 0), COALESCE(SUM(overdue_spaces), 0)
		FROM usage WHERE kind = 'tenant' AND `+cond, args...).
		Scan(&spaces, &remote, &partial, &s.Evicted, &s.Archived, &s.People, &s.Dormant, &s.HeldBytes, &s.UsageBytes, &s.InGrace, &s.Overdue)
	if err != nil {
		return s, err
	}
	// When a space of the scope was last measured: the newest of the queue, one
	// lookup of its index.
	if err := q.QueryRow(ctx, `SELECT COALESCE(MAX(measured_at), 0) FROM space_measure WHERE `+cond, args...).Scan(&measured); err != nil {
		return s, err
	}
	s.Local, s.Partial = max(spaces-remote, 0), partial
	s.Full = max(remote-partial-s.Evicted-s.Archived, 0)
	if measured > 0 {
		s.Measured = fromMS(measured)
	}
	rc, ra := sc.spaceCond("tenant_id", "personal = 0")
	if err := q.QueryRow(ctx, `SELECT COUNT(*), COALESCE(SUM(bytes), 0) FROM recycle_items WHERE `+rc, ra...).Scan(&s.RecycleItems, &s.RecycleBytes); err != nil {
		return s, err
	}
	ec, ea := jobCond(sc)
	err = q.QueryRow(ctx, `SELECT COUNT(*), COALESCE(SUM(bytes_done), 0) FROM jobs WHERE kind = 'export' AND state = 'done' AND `+ec, ea...).Scan(&s.ExportItems, &s.ExportBytes)
	return s, err
}

// --- the deadline counts ---------------------------------------------------------

// SettleDeadlines writes how many spaces of each person and each tenant are in
// their grace period and past their deadline under the policy. These two numbers
// change with the clock and with the policy, not only with a write, so they are
// not kept by difference: this one grouped query over the spaces that lack what
// is required, run every hour, when the policy changes and by the daily recount,
// sets them. It writes only the rows that differ and reports how many.
func (s *Store) SettleDeadlines(ctx context.Context, p RemotePolicy) (changed int, err error) {
	type pair struct{ grace, overdue int64 }
	want := map[string]pair{}
	if p.Active() {
		lacks, largs := `s.remote_via = ''`, []any(nil)
		if p.Require != "any" {
			lacks, largs = `s.remote_via <> ?`, []any{p.Require}
		}
		exempt := `s.exempt = 0 AND NOT EXISTS (SELECT 1 FROM space_members om JOIN users ou ON ou.id = om.user_id WHERE om.space_id = s.id AND om.role = 'owner' AND ou.remote_exempt <> 0)`
		dl := `(CASE WHEN s.created >= CAST(? AS BIGINT) THEN s.created WHEN CAST(? AS BIGINT) <> 0 THEN CAST(? AS BIGINT) END + CAST(? AS BIGINT))`
		dargs := []any{ms(p.Since), b2i(p.All), ms(p.Since), p.Grace.Milliseconds()}
		// The two counts of each person and, with usage_user '', of each tenant.
		query := `SELECT s.usage_user, s.tenant_id,
			COALESCE(SUM(CASE WHEN ` + dl + ` >= CAST(? AS BIGINT) THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN ` + dl + ` < CAST(? AS BIGINT) THEN 1 ELSE 0 END), 0)
			FROM spaces s WHERE ` + lacks + ` AND ` + exempt + ` AND ` + dl + ` IS NOT NULL GROUP BY s.usage_user, s.tenant_id`
		// The placeholders, in order: the deadline and now for each of the two
		// sums, what is lacked, and the deadline once more.
		args := concat(dargs, ms(p.Now), dargs, ms(p.Now), largs, dargs)
		rows, err := s.Q().Query(ctx, query, args...)
		if err != nil {
			return 0, err
		}
		for rows.Next() {
			var user, tenant string
			var g, o int64
			if err := rows.Scan(&user, &tenant, &g, &o); err != nil {
				rows.Close()
				return 0, err
			}
			if user != "" {
				w := want[UsageUser+"\x00"+user]
				want[UsageUser+"\x00"+user] = pair{w.grace + g, w.overdue + o}
			}
			w := want[UsageTenant+"\x00"+tenant]
			want[UsageTenant+"\x00"+tenant] = pair{w.grace + g, w.overdue + o}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return 0, err
		}
	}
	err = s.Tx(ctx, func(q Q) error {
		rows, err := q.Query(ctx, `SELECT kind, id, grace_spaces, overdue_spaces FROM usage WHERE grace_spaces <> 0 OR overdue_spaces <> 0`)
		if err != nil {
			return err
		}
		have := map[string]pair{}
		for rows.Next() {
			var kind, id string
			var g, o int64
			if err := rows.Scan(&kind, &id, &g, &o); err != nil {
				rows.Close()
				return err
			}
			have[kind+"\x00"+id] = pair{g, o}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		set := func(k string, w pair) error {
			kind, id, _ := strings.Cut(k, "\x00")
			res, err := q.Exec(ctx, `UPDATE usage SET grace_spaces = ?, overdue_spaces = ? WHERE kind = ? AND id = ? AND (grace_spaces <> ? OR overdue_spaces <> ?)`,
				w.grace, w.overdue, kind, id, w.grace, w.overdue)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				changed++
			}
			return nil
		}
		for k, w := range want {
			if have[k] != w {
				if err := set(k, w); err != nil {
					return err
				}
			}
		}
		for k := range have {
			if _, ok := want[k]; !ok {
				if err := set(k, pair{}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return changed, err
}
