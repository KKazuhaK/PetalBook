package store_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

func TestPlansAndATenantsPlan(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		if err := store.InsertPlan(ctx, q, store.Plan{ID: "P1", Slug: "team", Name: "Team"}); err != nil {
			t.Fatal(err)
		}
		if err := store.InsertPlan(ctx, q, store.Plan{ID: "P2", Slug: "team", Name: "Again"}); !errors.Is(err, store.ErrExists) {
			t.Fatalf("a second plan with the slug: %v", err)
		}
		p, err := store.GetPlanBySlug(ctx, q, "team")
		if err != nil || p.Name != "Team" {
			t.Fatalf("%+v %v", p, err)
		}
		if _, err := store.GetPlanBySlug(ctx, q, "none"); !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
		p.Name = "Team 2"
		if err := store.UpdatePlan(ctx, q, *p); err != nil {
			t.Fatal(err)
		}
		if all, err := store.ListPlans(ctx, q); err != nil || len(all) != 1 || all[0].Name != "Team 2" {
			t.Fatalf("%+v %v", all, err)
		}
		if err := store.PutLayered(ctx, q, store.LayeredRow{Layer: store.LayeredPlan, LayerID: "P1", Key: "quota.spaces", Source: store.SourcePlan, Value: "3", Updated: time.Now()}); err != nil {
			t.Fatal(err)
		}

		// A tenant's plan is part of its reference in memory once the state is read again.
		if err := store.SetTenantPlan(ctx, q, store.DefaultTenantID, "P1"); err != nil {
			t.Fatal(err)
		}
		if err := st.TenantStateReload(ctx); err != nil {
			t.Fatal(err)
		}
		if r, _ := st.TenantRefOf(store.DefaultTenantID); r.PlanID != "P1" {
			t.Fatalf("%+v", r)
		}
		if err := store.DeletePlan(ctx, q, "P1"); !errors.Is(err, store.ErrExists) {
			t.Fatalf("a plan in use: %v", err)
		}
		if err := store.SetTenantPlan(ctx, q, store.DefaultTenantID, ""); err != nil {
			t.Fatal(err)
		}
		if err := store.DeletePlan(ctx, q, "P1"); err != nil {
			t.Fatal(err)
		}
		if rows, err := store.ListLayeredLayer(ctx, q, store.LayeredPlan); err != nil || len(rows) != 0 {
			t.Fatalf("the rules of a deleted plan: %v %v", rows, err)
		}
		if err := store.DeletePlan(ctx, q, "P1"); !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
	})
}

func TestRulesAreOneRowPerTenantKeyAndSource(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		twoTenants(t, st)
		q := st.Q()
		at := time.UnixMilli(1_700_000_000_000).UTC()
		put := func(tenant, key, source, value string, from, until time.Time) error {
			return store.PutLayered(ctx, q, store.LayeredRow{Layer: "site", LayerID: tenant, Key: key, Source: source, Value: value,
				Starts: from, Ends: until, SetBy: "U1", Updated: at})
		}
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		must(put(tenB, "quota.bytes", "manual", "100", at, at.Add(time.Hour)))
		must(put(tenB, "quota.bytes", "billing", "50", time.Time{}, time.Time{}))
		must(put(tenB, "quota.bytes", "manual", "7", time.Time{}, time.Time{})) // replaces the first, window and all
		must(put(tenA, "features.ownSso", "manual", "false", time.Time{}, time.Time{}))

		rows, err := store.ListLayered(ctx, q, "site", tenB)
		if err != nil || len(rows) != 2 {
			t.Fatalf("%+v %v", rows, err)
		}
		bySource := map[string]store.LayeredRow{}
		for _, r := range rows {
			bySource[r.Source] = r
		}
		if m := bySource["manual"]; m.Value != "7" || !m.Starts.IsZero() || !m.Ends.IsZero() || m.SetBy != "U1" {
			t.Fatalf("the replaced row keeps nothing of the old window: %+v", m)
		}
		if b := bySource["billing"]; b.Value != "50" {
			t.Fatalf("%+v", b)
		}
		if err := put(tenB, "k", "manual", "1", at.Add(time.Hour), at); err == nil {
			t.Fatal("an end before the start was written")
		}
		if err := put(tenB, "k", "other", "1", time.Time{}, time.Time{}); err == nil {
			t.Fatal("a source that is not manual, billing or plan")
		}
		if _, err := store.GetLayered(ctx, q, "site", tenB, "quota.bytes", "plan"); !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}

		// Every manual rule goes on a change of plan, dated or not; billing's stay.
		must(put(tenB, "quota.pages", "manual", "9", time.Time{}, at.Add(time.Hour)))
		gone, err := store.DeleteManualRules(ctx, q, tenB)
		if err != nil || len(gone) != 2 {
			t.Fatalf("%+v %v", gone, err)
		}
		if rows, _ = store.ListLayered(ctx, q, "site", tenB); len(rows) != 1 || rows[0].Source != "billing" {
			t.Fatalf("billing stays alone: %+v", rows)
		}

		// The rows of a tenant go with it, and counting a write moves the revision.
		must(st.CreateTenant(ctx, store.Tenant{ID: "T000000003", Slug: "empty", Name: "Empty", Created: at, Updated: at}))
		must(put("T000000003", "features.ownSso", "manual", "false", time.Time{}, time.Time{}))
		before, _ := store.SettingsRev(ctx, q)
		must(st.DeleteTenant(ctx, "T000000003"))
		if rows, _ = store.ListLayered(ctx, q, "site", "T000000003"); len(rows) != 0 {
			t.Fatalf("the rules of a deleted tenant: %+v", rows)
		}
		if after, _ := store.SettingsRev(ctx, q); after != before+1 {
			t.Fatalf("revision %d -> %d", before, after)
		}
	})
}

func TestRuleBoundariesAreFoundThroughThePartialIndexes(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		twoTenants(t, st)
		q := st.Q()
		at := time.UnixMilli(1_700_000_000_000).UTC()
		const n = 3000
		bulk(t, st, "layered_settings", "layer, layer_id, key, source, value, lower, starts, ends, set_by, updated", n, 200, func(i int) []any {
			var from, until any
			if i%500 == 0 { // a few dated rules among the undated
				from, until = at.Add(time.Duration(i)*time.Minute).UnixMilli(), at.Add(time.Duration(i+60)*time.Minute).UnixMilli()
			}
			return []any{"site", tenB, fmt.Sprintf("k%05d", i), "manual", "1", "", from, until, "", at.UnixMilli()}
		})
		if _, err := q.Exec(ctx, `ANALYZE`); err != nil {
			t.Fatal(err)
		}
		next, ok, err := store.NextRuleBoundary(ctx, q, at.Add(10*time.Minute))
		if err != nil || !ok || !next.Equal(at.Add(60*time.Minute)) {
			t.Fatalf("%v %v %v", next, ok, err)
		}
		if _, ok, _ := store.NextRuleBoundary(ctx, q, at.Add(100*time.Hour)); ok {
			t.Fatal("nothing is dated after the last window")
		}
		got, err := store.RuleBoundaries(ctx, q, at.Add(59*time.Minute), at.Add(60*time.Minute))
		if err != nil || len(got) != 1 || got[0].Started || got[0].Key != "k00000" {
			t.Fatalf("an end in (after, upTo]: %+v %v", got, err)
		}
		if got, _ = store.RuleBoundaries(ctx, q, at.Add(60*time.Minute), at.Add(61*time.Minute)); len(got) != 0 {
			t.Fatalf("the end is not in a window that starts at it: %+v", got)
		}
		if got, _ = store.RuleBoundaries(ctx, q, at.Add(-time.Minute), at); len(got) != 1 || !got[0].Started {
			t.Fatalf("a start: %+v", got)
		}

		for _, query := range []string{
			`SELECT MIN(starts) FROM layered_settings WHERE starts > ?`,
			`SELECT MIN(ends) FROM layered_settings WHERE ends > ?`,
			`SELECT layer_id, key, source, ends FROM layered_settings WHERE ends > ? AND ends <= ? AND layer = 'site'`,
		} {
			ex := "EXPLAIN "
			if st.Dialect() == store.SQLite {
				ex = "EXPLAIN QUERY PLAN "
			}
			args := []any{at.UnixMilli()}
			if strings.Count(query, "?") == 2 {
				args = append(args, at.Add(time.Hour).UnixMilli())
			}
			rows, err := q.Query(ctx, ex+query, args...)
			if err != nil {
				t.Fatal(err)
			}
			var plan []string
			cs, _ := rows.Columns()
			for rows.Next() {
				cols := make([]any, len(cs))
				ptrs := make([]any, len(cs))
				for i := range cols {
					ptrs[i] = &cols[i]
				}
				if err := rows.Scan(ptrs...); err != nil {
					t.Fatal(err)
				}
				plan = append(plan, fmt.Sprint(cols[len(cols)-1]))
			}
			rows.Close()
			text := strings.Join(plan, "\n")
			if !strings.Contains(text, "layered_settings_") {
				t.Errorf("%s\nuses no partial index:\n%s", query, text)
			}
		}
	})
}
