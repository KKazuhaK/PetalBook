package store_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// The plans of the database baseline (docs/design/database.md, "5.3 Database baseline (2026-11-24)"):
// each query below must be served by its index, on SQLite and PostgreSQL. As in
// TestListQueriesUseIndexes, PostgreSQL is asked with scans and sorts made dear,
// so a plan that still has one has no index to use instead.

// explain returns the plan of one statement, one line per node.
func explain(t *testing.T, st *store.Store, q store.Q, sqlText string, args ...any) string {
	t.Helper()
	ex := "EXPLAIN "
	if st.Dialect() == store.SQLite {
		ex = "EXPLAIN QUERY PLAN "
	}
	rows, err := q.Query(ctx, ex+sqlText, args...)
	if err != nil {
		t.Fatalf("%v in %s", err, sqlText)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		cs, _ := rows.Columns()
		cols := make([]any, len(cs))
		ptrs := make([]any, len(cs))
		for i := range cols {
			ptrs[i] = &cols[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		if st.Dialect() == store.SQLite {
			plan = append(plan, fmt.Sprint(cols[len(cols)-1]))
		} else {
			plan = append(plan, fmt.Sprint(cols[0]))
		}
	}
	return strings.Join(plan, "\n")
}

// withDearScans runs fn in a transaction in which PostgreSQL avoids sequential
// scans and sorts whenever an index can do instead.
func withDearScans(t *testing.T, st *store.Store, fn func(q store.Q)) {
	t.Helper()
	withDear(t, st, []string{"enable_seqscan", "enable_sort"}, fn)
}

// withDear is withDearScans with the planner settings to switch off named.
func withDear(t *testing.T, st *store.Store, off []string, fn func(q store.Q)) {
	t.Helper()
	if _, err := st.Q().Exec(ctx, `ANALYZE`); err != nil {
		t.Fatal(err)
	}
	err := st.Tx(ctx, func(q store.Q) error {
		if st.Dialect() == store.Postgres {
			for _, set := range off {
				if _, err := q.Exec(ctx, `SET LOCAL `+set+` = off`); err != nil {
					return err
				}
			}
		}
		fn(q)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// usesIndex reports whether a plan reads the named index and sorts nothing.
func usesIndex(st *store.Store, plan, index string) bool {
	if !strings.Contains(plan, index) {
		return false
	}
	if st.Dialect() == store.SQLite {
		return !strings.Contains(plan, "USE TEMP B-TREE")
	}
	for _, l := range strings.Split(plan, "\n") {
		if strings.HasPrefix(strings.TrimPrefix(strings.TrimSpace(l), "->  "), "Sort  (") {
			return false
		}
	}
	return true
}

// Every text column compares bytewise: COLLATE "C" on PostgreSQL, BINARY (the
// default) on SQLite, so both sort the same and an index serves every ORDER BY
// and cursor comparison as it is written.
func TestEveryTextColumnIsBytewise(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		if st.Dialect() == store.SQLite {
			if n := count(t, st, `SELECT COUNT(*) FROM sqlite_master WHERE sql LIKE '%COLLATE "C"%'`); n != 0 {
				t.Fatalf("%d tables say COLLATE \"C\" on SQLite", n)
			}
			return
		}
		rows, err := st.Q().Query(ctx, `SELECT table_name, column_name, COALESCE(collation_name, '') FROM information_schema.columns
			WHERE table_schema = current_schema() AND data_type = 'text' AND COALESCE(collation_name, '') <> 'C' ORDER BY 1, 2`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var table, col, coll string
			rows.Scan(&table, &col, &coll)
			t.Errorf("%s.%s has collation %q", table, col, coll)
		}
		if n := count(t, st, `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = current_schema() AND data_type = 'text'`); n < 200 {
			t.Fatalf("only %d text columns: is the schema there?", n)
		}
	})
}

// Names sort by their bytes on both databases, and the sorted list of groups
// pages over its index (on PostgreSQL a COLLATE "C" of the query alone, as
// before the baseline, could not use the column's index and sorted every group).
func TestCollatedSortsUseTheirIndexes(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Now()
		names := []string{"Zed", "alpha", "_under", "Émile", "été", "beta", "Ärger", "zeta"}
		for i, n := range names {
			if err := store.InsertGroup(ctx, st.Q(), store.Group{ID: fmt.Sprintf("G%d", i), Name: n, Source: store.GroupLocal, Created: now, Updated: now}); err != nil {
				t.Fatal(err)
			}
		}
		page, _, err := store.ListGroups(ctx, st.Q(), st.Dialect(), store.InTenant(store.DefaultTenantID), store.GroupFilter{Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, g := range page {
			got = append(got, store.FoldName(g.Name))
		}
		// Byte order of the folded names: "_" (0x5F) before the letters, and the
		// accented ones (0xC3...) after z.
		if want := "[_under alpha beta zed zeta ärger émile été]"; fmt.Sprint(got) != want {
			t.Fatalf("%v, want %s", got, want)
		}
		var rows []any
		for i := 0; i < 3000; i++ {
			rows = append(rows, fmt.Sprintf("H%05d", i), fmt.Sprintf("Group %d", i), fmt.Sprintf("group %d", i), "local", now.UnixMilli(), now.UnixMilli())
		}
		bulk(t, st, "groups", "id, name, name_key, source, created, updated", 3000, 200, func(i int) []any { return rows[i*6 : i*6+6] })
		withDearScans(t, st, func(q store.Q) {
			pq := &planQ{Q: q, d: st.Dialect()}
			if _, _, err := store.ListGroups(ctx, pq, st.Dialect(), store.InTenant(store.DefaultTenantID), store.GroupFilter{After: "group 1500", Limit: 50}); err != nil {
				t.Fatal(err)
			}
			if len(pq.plans) == 0 || !usesIndex(st, pq.plans[0], "groups_tenant_id_name_key") && !usesIndex(st, pq.plans[0], "sqlite_autoindex_groups") {
				t.Errorf("the list of groups does not page over its index:\n%s", strings.Join(pq.plans, "\n--\n"))
			}
		})
	})
}

// A lookup by address reads users_email (it scanned every person of the tenant,
// 88 ms on 30,000 people, when it lower-cased each stored address); the cascades
// of a person's deletion read the child indexes; the sweep of old session ends
// and the attachment moves read theirs.
func TestLookupsUseTheirIndexes(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Now().UnixMilli()
		const n = 3000
		bulk(t, st, "users", "id, username, name, name_key, email, email_key, role, created", n, 200, func(i int) []any {
			return []any{fmt.Sprintf("U%05d", i), fmt.Sprintf("user%d", i), fmt.Sprintf("User %d", i), fmt.Sprintf("user %d", i),
				fmt.Sprintf("User%d@Example.com", i), fmt.Sprintf("user%d@example.com", i), "member", now}
		})
		bulk(t, st, "session_ends", "id, user_id, reason, ended", n, 200, func(i int) []any {
			return []any{fmt.Sprintf("E%05d", i), fmt.Sprintf("U%05d", i), "disabled", now - int64(i)*1000}
		})
		bulk(t, st, "spaces", "id, name, name_key, created", n, 200, func(i int) []any {
			return []any{fmt.Sprintf("S%05d", i), "s", "s", now}
		})
		bulk(t, st, "asset_remotes", "space_id, updated, move_state", n, 200, func(i int) []any {
			state := ""
			if i%500 == 0 {
				state = store.MoveCopying
			}
			return []any{fmt.Sprintf("S%05d", i), now, state}
		})
		// A lookup sorts its few rows: only scans of whole tables are made dear.
		withDear(t, st, []string{"enable_seqscan"}, func(q store.Q) {
			pq := &planQ{Q: q, d: st.Dialect()}
			if _, err := store.UsersByEmail(ctx, pq, st.Dialect(), store.InTenant(store.DefaultTenantID), "USER42@example.com", 2); err != nil {
				t.Fatal(err)
			}
			if len(pq.plans) != 1 || !strings.Contains(pq.plans[0], "users_email") {
				t.Errorf("the lookup by address does not read users_email:\n%s", strings.Join(pq.plans, "\n--\n"))
			}
			for _, c := range []struct{ sql, index string }{
				{`SELECT 1 FROM drafts WHERE user_id = ?`, "drafts_user"},
				{`SELECT 1 FROM invites WHERE created_by = ?`, "invites_created_by"},
				{`SELECT 1 FROM provider_leftovers WHERE user_id = ?`, "provider_leftovers_user"},
				{`SELECT id FROM session_ends WHERE ended < ?`, "session_ends_ended"},
				{`SELECT space_id FROM asset_remotes WHERE move_state IN (?, ?) AND move_state <> '' ORDER BY space_id`, "asset_remotes_move"},
			} {
				var arg any = "U00042"
				if strings.Contains(c.sql, "ended") {
					// The sweep's cutoff is 30 days back: few rows are older.
					arg = now - int64(n-10)*1000
				}
				args := []any{arg}
				if strings.Contains(c.sql, "move_state") {
					args = []any{store.MoveCopying, store.MoveSwitching}
				}
				if plan := explain(t, st, q, c.sql, args...); !strings.Contains(plan, c.index) {
					t.Errorf("%s does not read %s:\n%s", c.sql, c.index, plan)
				}
			}
		})
	})
}

// PostgreSQL leaves room in the pages of the tables that are rewritten often, so
// that their updates can be HOT.
func TestOftenUpdatedTablesHaveAFillFactor(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		if st.Dialect() != store.Postgres {
			t.Skip("a PostgreSQL storage parameter")
		}
		for _, table := range []string{"usage", "spaces", "users", "sessions", "remotes"} {
			if n := count(t, st, `SELECT COUNT(*) FROM pg_class WHERE oid = to_regclass(?) AND 'fillfactor=85' = ANY(reloptions)`, table); n != 1 {
				t.Errorf("%s has no fillfactor 85", table)
			}
		}
	})
}

// PostgreSQL keeps its connections between bursts: as many idle as open.
func TestPostgresKeepsItsConnections(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		if st.Dialect() != store.Postgres {
			t.Skip("a PostgreSQL pool")
		}
		if got := st.Stats().MaxOpenConnections; got != 20 {
			t.Fatalf("max open %d", got)
		}
		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				st.Tx(ctx, func(q store.Q) error { _, err := q.Exec(ctx, `SELECT pg_sleep(0.05)`); return err })
			}()
		}
		wg.Wait()
		if s := st.Stats(); s.Idle < 10 || s.MaxIdleClosed != 0 {
			t.Fatalf("after a burst of 10: %d idle, %d closed for want of room", s.Idle, s.MaxIdleClosed)
		}
	})
}
