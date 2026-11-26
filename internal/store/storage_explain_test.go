package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// The lists are one query a page over an index. This asks the database how it
// would run each, on a server of 16,000 spaces and people in three tenants, and
// refuses a scan of a whole table, and, for the ordered lists, a sort of the rows
// found (docs/api.md, "Storage management"): EXPLAIN QUERY PLAN on SQLite, EXPLAIN
// on PostgreSQL.

// planQ runs EXPLAIN of every SELECT it is given and keeps the plans.
type planQ struct {
	store.Q
	d     store.Dialect
	plans []string
}

func (p *planQ) Query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	if strings.HasPrefix(strings.TrimSpace(q), "SELECT") {
		ex := "EXPLAIN "
		if p.d == store.SQLite {
			ex = "EXPLAIN QUERY PLAN "
		}
		rows, err := p.Q.Query(ctx, ex+q, args...)
		if err != nil {
			return nil, fmt.Errorf("%w in %s", err, q)
		}
		var plan []string
		for rows.Next() {
			var cols []any
			cs, _ := rows.Columns()
			cols = make([]any, len(cs))
			ptrs := make([]any, len(cs))
			for i := range cols {
				ptrs[i] = &cols[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				return nil, err
			}
			if p.d == store.SQLite {
				plan = append(plan, fmt.Sprint(cols[len(cols)-1])) // id, parent, notused, detail
			} else {
				plan = append(plan, fmt.Sprint(cols[0]))
			}
		}
		rows.Close()
		p.plans = append(p.plans, strings.Join(plan, "\n"))
	}
	return p.Q.Query(ctx, q, args...)
}

func bulk(t *testing.T, st *store.Store, table, cols string, n, per int, row func(i int) []any) {
	t.Helper()
	width := len(strings.Split(cols, ","))
	err := st.Tx(ctx, func(q store.Q) error {
		for from := 0; from < n; from += per {
			to := min(from+per, n)
			var marks []string
			var args []any
			for i := from; i < to; i++ {
				marks = append(marks, "("+strings.TrimSuffix(strings.Repeat("?,", width), ",")+")")
				args = append(args, row(i)...)
			}
			if _, err := q.Exec(ctx, `INSERT INTO `+table+`(`+cols+`) VALUES `+strings.Join(marks, ","), args...); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestListQueriesUseIndexes(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("makes 16,000 rows")
	}
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
		for _, tn := range []string{"T000000002", "T000000003"} {
			if err := st.CreateTenant(ctx, store.Tenant{ID: tn, Slug: "t" + tn[len(tn)-1:], Name: tn, Created: now, Updated: now}); err != nil {
				t.Fatal(err)
			}
		}
		tenants := []string{store.DefaultTenantID, "T000000002", "T000000003"}
		const n = 8000
		tenantOf := func(i int) string { return tenants[[]int{0, 0, 0, 0, 1, 1, 2, 0}[i%8]] }
		bulk(t, st, "users", "id, username, name, name_key, role, disabled, created, tenant_id, last_active", n, 200, func(i int) []any {
			role := "member"
			if i%50 == 0 {
				role = "admin"
			}
			var active any
			if i%3 != 0 {
				active = now.Add(-time.Duration(i%977) * time.Hour).UnixMilli()
			}
			return []any{fmt.Sprintf("U%07d", i), fmt.Sprintf("user%d", i), fmt.Sprintf("User %d", i), fmt.Sprintf("user %d", i), role, 0, now.Add(time.Duration(i) * time.Minute).UnixMilli(), tenantOf(i), active}
		})
		bulk(t, st, "usage", "kind, id, tenant_id, spaces, bytes, held_bytes, remote_spaces, dormant, created, last_active, grace_spaces, overdue_spaces", n, 100, func(i int) []any {
			over := 0
			if i%500 == 0 {
				over = 1
			}
			return []any{"user", fmt.Sprintf("U%07d", i), tenantOf(i), i % 5, (i * 37) % 100000, (i * 91) % 100000, i % 2, i % 3 / 2, now.Add(time.Duration(i) * time.Minute).UnixMilli(), (i % 4) * 1000, 0, over}
		})
		bulk(t, st, "spaces", "id, name, name_key, created, tenant_id, size_bytes, last_used, remote_via, remote_state, location, cache_state, pinned, exempt, usage_user", n, 100, func(i int) []any {
			var size any
			if i%5 != 0 {
				size = (i * 53) % 1000000
			}
			via, state, loc, cache := "", "", "server", ""
			if i%2 == 0 {
				via, state, loc = "git", "ok", "own"
				cache = []string{"", "partial", "evicted"}[i%3]
				if i%50 == 0 {
					state = "error"
				}
			}
			pinned, exempt := 0, 0
			if i%400 == 0 {
				pinned = 1
			}
			if i%300 == 0 {
				exempt = 1
			}
			return []any{fmt.Sprintf("S%07d", i), fmt.Sprintf("Space %d", i%977), fmt.Sprintf("space %d", i%977), now.Add(-time.Duration(i) * time.Minute).UnixMilli(), tenantOf(i), size,
				(i % 7) * 100000, via, state, loc, cache, pinned, exempt, fmt.Sprintf("U%07d", i)}
		})
		if _, err := st.Q().Exec(ctx, `ANALYZE`); err != nil {
			t.Fatal(err)
		}
		// PostgreSQL costs a plan on what it estimates, and at this size a sort of a few
		// rows or a scan of a small table can be the cheapest. What matters is that
		// an index gives the order and the range, so the plans are asked for with
		// scans and sorts made dear: if the index is not there, they cannot avoid them.
		err := st.Tx(ctx, func(q store.Q) error {
			if st.Dialect() == store.Postgres {
				for _, set := range []string{"enable_seqscan", "enable_sort"} {
					if _, err := q.Exec(ctx, `SET LOCAL `+set+` = off`); err != nil {
						return err
					}
				}
			}
			checkPlans(t, st, q, now, tenants)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

func checkPlans(t *testing.T, st *store.Store, q store.Q, now time.Time, tenants []string) {
	{
		pq := &planQ{Q: q, d: st.Dialect()}
		pol := store.RemotePolicy{Require: "any", Since: now.Add(-200 * 24 * time.Hour), Grace: 30 * 24 * time.Hour, Now: now, All: true}
		// The third tenant is the small one (an eighth of the rows): the planner has
		// a reason to search by the tenant.
		small := tenants[2]
		tenantScope, site := store.InTenant(small), store.AllTenants()
		tenantAdmin := store.AdminScopeOf(store.User{TenantID: small, Role: "admin"})
		type spaceCase struct {
			name    string
			sc      store.Scope
			f       store.SpaceFilter
			ordered bool // the rows come in the order asked, with no sort of what was found
		}
		var cases []spaceCase
		for _, sortName := range store.SpaceSorts {
			for _, order := range []string{"asc", "desc"} {
				cases = append(cases, spaceCase{"tenant sorted by " + sortName + " " + order, tenantScope, store.SpaceFilter{Sort: sortName, Order: order, Limit: 50}, true},
					spaceCase{"site sorted by " + sortName + " " + order, site, store.SpaceFilter{Sort: sortName, Order: order, Limit: 50}, true},
					spaceCase{"tenant admin sorted by " + sortName + " " + order, tenantAdmin, store.SpaceFilter{Sort: sortName, Order: order, Limit: 50}, true})
			}
		}
		cases = append(cases,
			spaceCase{"cursor", tenantScope, store.SpaceFilter{Sort: "created", After: "spaces.created.asc:1700000000000:S0001000", Limit: 50}, true},
			spaceCase{"cursor, bytes", tenantScope, store.SpaceFilter{Sort: "bytes", After: "spaces.bytes.desc:5000:S0001000", Limit: 50}, true},
			spaceCase{"cursor, never measured", tenantScope, store.SpaceFilter{Sort: "bytes", After: "spaces.bytes.desc.none::S0001000", Limit: 50}, true},
			spaceCase{"cache evicted", tenantScope, store.SpaceFilter{Cache: "evicted", Limit: 50}, true},
			spaceCase{"cache local", tenantScope, store.SpaceFilter{Cache: "local", Limit: 50}, true},
			spaceCase{"remote ok", tenantScope, store.SpaceFilter{Remote: "ok", Limit: 50}, true},
			spaceCase{"remote error", tenantScope, store.SpaceFilter{Remote: "error", Limit: 50}, false},
			spaceCase{"remote none", site, store.SpaceFilter{Remote: "none", Limit: 50}, true},
			spaceCase{"pinned", tenantScope, store.SpaceFilter{Pinned: true, Limit: 50}, false},
			spaceCase{"exempt", site, store.SpaceFilter{Exempt: true, Limit: 50}, false},
			spaceCase{"deadline overdue", tenantScope, store.SpaceFilter{Deadline: "overdue", Policy: &pol, Limit: 50}, false},
			spaceCase{"deadline grace", site, store.SpaceFilter{Deadline: "grace", Policy: &pol, Limit: 50}, false},
			spaceCase{"idleDays", tenantScope, store.SpaceFilter{IdleBefore: now.Add(-48 * time.Hour), Sort: "lastUsed", Limit: 50}, true},
			spaceCase{"owner", tenantScope, store.SpaceFilter{Owner: "U0000004", Limit: 50}, false},
			spaceCase{"by id", tenantScope, store.SpaceFilter{ID: "S0000004", Limit: 1}, false},
		)
		var bad []string
		for _, c := range cases {
			pq.plans = nil
			if _, _, err := store.ListAllSpaces(ctx, pq, c.sc, c.f); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			for _, p := range pq.plans {
				if why := planProblem(st.Dialect(), p, c.ordered); why != "" {
					bad = append(bad, c.name+": "+why+"\n"+p)
				}
			}
			if testing.Verbose() {
				t.Logf("%s:\n%s", c.name, strings.Join(pq.plans, "\n--\n"))
			}
		}
		type peopleCase struct {
			name    string
			sc      store.Scope
			f       store.PeopleFilter
			ordered bool
		}
		var pcases []peopleCase
		for _, sortName := range store.PersonSorts {
			for _, order := range []string{"asc", "desc"} {
				pcases = append(pcases, peopleCase{"people tenant " + sortName + " " + order, tenantScope, store.PeopleFilter{Sort: sortName, Order: order, Limit: 50}, true},
					peopleCase{"people site " + sortName + " " + order, site, store.PeopleFilter{Sort: sortName, Order: order, Limit: 50}, true})
			}
		}
		pcases = append(pcases,
			peopleCase{"people dormant", tenantScope, store.PeopleFilter{Dormant: true, Sort: "created", Limit: 50}, true},
			peopleCase{"people dormant, held", tenantScope, store.PeopleFilter{Dormant: true, Limit: 50}, false},
			peopleCase{"people inactive", tenantScope, store.PeopleFilter{InactiveBefore: now, Sort: "lastActive", Order: "asc", Limit: 50}, true},
			peopleCase{"people remote none", tenantScope, store.PeopleFilter{Remote: "none", Limit: 50}, true},
			peopleCase{"people overdue", tenantScope, store.PeopleFilter{Deadline: "overdue", Limit: 50}, false},
			peopleCase{"people by id", tenantScope, store.PeopleFilter{ID: "U0000004", Limit: 1}, false},
			peopleCase{"people cursor", tenantScope, store.PeopleFilter{After: "people.held.desc:5000:U0001000", Limit: 50}, true},
		)
		for _, c := range pcases {
			pq.plans = nil
			if _, _, err := store.ListPeople(ctx, pq, st.Dialect(), c.sc, c.f); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			for _, p := range pq.plans {
				if why := planProblem(st.Dialect(), p, c.ordered); why != "" {
					bad = append(bad, c.name+": "+why+"\n"+p)
				}
			}
			if testing.Verbose() {
				t.Logf("%s:\n%s", c.name, strings.Join(pq.plans, "\n--\n"))
			}
		}
		type userCase struct {
			name string
			sc   store.Scope
			f    store.UserFilter
		}
		var ucases []userCase
		for _, sortName := range store.UserSorts {
			for _, order := range []string{"asc", "desc"} {
				ucases = append(ucases, userCase{"users tenant " + sortName + " " + order, tenantScope, store.UserFilter{Sort: sortName, Order: order, Limit: 50}},
					userCase{"users site " + sortName + " " + order, site, store.UserFilter{Sort: sortName, Order: order, Limit: 50}},
					userCase{"users tenant admin " + sortName + " " + order, tenantAdmin, store.UserFilter{Sort: sortName, Order: order, Limit: 50}})
			}
		}
		ucases = append(ucases,
			userCase{"users cursor", tenantScope, store.UserFilter{After: "users.created.asc:1700000000000:U0001000", Limit: 50}},
			userCase{"users cursor, name", site, store.UserFilter{Sort: "name", After: "users.name.asc:user 100:U0001000", Limit: 50}},
			userCase{"users cursor, never active", tenantScope, store.UserFilter{Sort: "lastActive", After: "users.lastActive.desc.none::U0001000", Limit: 50}},
			userCase{"users enabled", tenantScope, store.UserFilter{State: "enabled", Limit: 50}},
			userCase{"users admins", site, store.UserFilter{Role: "admin", Sort: "name", Limit: 50}},
			userCase{"users inactive", tenantScope, store.UserFilter{InactiveBefore: now, Sort: "lastActive", Order: "asc", Limit: 50}},
			userCase{"users search", tenantScope, store.UserFilter{Query: "user 77", Limit: 50}},
		)
		for _, c := range ucases {
			pq.plans = nil
			if _, _, err := store.ListUserRows(ctx, pq, c.sc, c.f); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			for _, p := range pq.plans {
				// The counts of the page are looked up by the ids of the page: not a part of the order.
				if strings.Contains(p, "passkeys") || strings.Contains(p, "identities") {
					continue
				}
				if why := planProblem(st.Dialect(), p, c.f.Query == "" && c.f.Role == ""); why != "" {
					bad = append(bad, c.name+": "+why+"\n"+p)
				}
			}
			if testing.Verbose() {
				t.Logf("%s:\n%s", c.name, strings.Join(pq.plans, "\n--\n"))
			}
		}
		for _, q := range []struct {
			name string
			sc   store.Scope
		}{{"summary tenant", tenantScope}, {"summary site", site}, {"summary tenant admin", tenantAdmin}} {
			pq.plans = nil
			if _, err := store.GetStorageSummary(ctx, pq, q.sc); err != nil {
				t.Fatal(err)
			}
			for _, p := range pq.plans {
				if why := planProblem(st.Dialect(), p, false); why != "" {
					bad = append(bad, q.name+": "+why+"\n"+p)
				}
			}
			if testing.Verbose() {
				t.Logf("%s:\n%s", q.name, strings.Join(pq.plans, "\n--\n"))
			}
		}
		for _, b := range bad {
			t.Error(b)
		}
	}
}

var sqliteScan = regexp.MustCompile(`^\s*SCAN (\S+)(.*)$`)

// planProblem is what is wrong with a plan, "" when nothing: a scan of a whole
// table (the tables here are big, and none of these lists may read one whole) and,
// when the rows must come in order, a sort.
func planProblem(d store.Dialect, plan string, ordered bool) string {
	if d == store.SQLite {
		lines := strings.Split(plan, "\n")
		for i, l := range lines {
			if m := sqliteScan.FindStringSubmatch(l); m != nil && !strings.Contains(m[2], "USING") && !strings.HasPrefix(m[1], "CONSTANT") && m[1] != "p" {
				return "a scan of a whole table: " + l
			}
			// The page is put in order again after it is cut (at most limit + 1
			// rows: "SCAN p", the derived table); any other sort is of what was found.
			if ordered && strings.Contains(l, "USE TEMP B-TREE FOR ORDER BY") && !(i+1 < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i+1]), "SCAN p")) {
				return "a sort of the rows found: " + l
			}
		}
		return ""
	}
	// PostgreSQL: the root may be the sort that puts the page in order after it is
	// cut; a correlated subquery of one row's own (SubPlan) is its own business.
	skip := -1
	for i, l := range strings.Split(plan, "\n") {
		indent := len(l) - len(strings.TrimLeft(l, " "))
		t := strings.TrimSpace(l)
		if skip >= 0 && indent > skip {
			continue
		}
		skip = -1
		if strings.HasPrefix(t, "SubPlan") || strings.HasPrefix(t, "InitPlan") {
			skip = indent
			continue
		}
		if strings.Contains(l, "Seq Scan on spaces") || strings.Contains(l, "Seq Scan on usage") {
			return "a scan of a whole table: " + l
		}
		if ordered && i > 0 && strings.HasPrefix(strings.TrimPrefix(t, "->  "), "Sort  (") {
			return "a sort of the rows found: " + l
		}
	}
	return ""
}
