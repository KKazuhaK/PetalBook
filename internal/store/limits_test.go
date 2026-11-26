package store_test

import (
	"errors"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

func TestPlansAndATenantsPlan(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		if err := store.InsertPlan(ctx, q, store.Plan{ID: "P1", Slug: "team", Name: "Team", Values: `{"signin.sso":false}`}); err != nil {
			t.Fatal(err)
		}
		if err := store.InsertPlan(ctx, q, store.Plan{ID: "P2", Slug: "team", Name: "Again", Values: `{}`}); !errors.Is(err, store.ErrExists) {
			t.Fatalf("a second plan with the slug: %v", err)
		}
		p, err := store.GetPlanBySlug(ctx, q, "team")
		if err != nil || p.Values != `{"signin.sso":false}` {
			t.Fatalf("%+v %v", p, err)
		}
		if _, err := store.GetPlanBySlug(ctx, q, "none"); !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
		p.Name, p.Values = "Team 2", `{}`
		if err := store.UpdatePlan(ctx, q, *p); err != nil {
			t.Fatal(err)
		}
		if all, err := store.ListPlans(ctx, q); err != nil || len(all) != 1 || all[0].Name != "Team 2" {
			t.Fatalf("%+v %v", all, err)
		}

		if err := store.SetTenantPlan(ctx, q, store.DefaultTenantID, "P1"); err != nil {
			t.Fatal(err)
		}
		if m, err := store.TenantPlanIDs(ctx, q); err != nil || m[store.DefaultTenantID] != "P1" || len(m) != 1 {
			t.Fatalf("%v %v", m, err)
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
		if err := store.DeletePlan(ctx, q, "P1"); !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
	})
}

func TestTenantLimitsAreOneRowPerTenantKeyAndSource(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		twoTenants(t, st)
		q := st.Q()
		at := time.UnixMilli(1_700_000_000_000).UTC()
		put := func(tenant, key, source, value string, from, until time.Time) {
			t.Helper()
			if err := store.PutTenantLimit(ctx, q, store.TenantLimit{Tenant: tenant, Key: key, Source: source, Value: value, Starts: from, Ends: until, SetBy: "U1", Updated: at}); err != nil {
				t.Fatal(err)
			}
		}
		put(tenB, "storage.quota.bytes", "manual", "100", at, at.Add(time.Hour))
		put(tenB, "storage.quota.bytes", "billing", "50", time.Time{}, time.Time{})
		put(tenB, "storage.quota.bytes", "manual", "null", time.Time{}, time.Time{}) // replaces the first
		put(tenA, "signin.sso", "manual", "false", time.Time{}, time.Time{})

		all, err := store.ListTenantLimits(ctx, q)
		if err != nil || len(all) != 3 {
			t.Fatalf("%+v %v", all, err)
		}
		// Ordered by tenant, key, source: the default tenant's row, then B's billing and manual.
		if all[0].Tenant != tenA || all[1].Source != "billing" || all[2].Source != "manual" {
			t.Fatalf("%+v", all)
		}
		if all[2].Value != "null" || !all[2].Starts.IsZero() || !all[2].Ends.IsZero() {
			t.Fatalf("the replaced row keeps nothing of the old window: %+v", all[2])
		}
		if all[1].Value != "50" || all[1].SetBy != "U1" || !all[1].Updated.Equal(at) {
			t.Fatalf("%+v", all[1])
		}
		put(tenB, "storage.quota.bytes", "billing", "50", at, at.Add(time.Hour))
		if all, _ = store.ListTenantLimits(ctx, q); !all[1].Starts.Equal(at) || !all[1].Ends.Equal(at.Add(time.Hour)) {
			t.Fatalf("a window is stored as given: %+v", all[1])
		}

		if err := store.PutTenantLimit(ctx, q, store.TenantLimit{Tenant: "nobody", Key: "k", Source: "manual", Value: "1", Updated: at}); err == nil {
			t.Fatal("a limit of a tenant that does not exist was written")
		}
		if err := store.PutTenantLimit(ctx, q, store.TenantLimit{Tenant: tenB, Key: "k", Source: "plan", Value: "1", Updated: at}); err == nil {
			t.Fatal("only manual and billing are rows")
		}

		if err := store.DeleteTenantLimit(ctx, q, tenB, "storage.quota.bytes", "manual"); err != nil {
			t.Fatal(err)
		}
		if err := store.DeleteTenantLimit(ctx, q, tenB, "storage.quota.bytes", "manual"); err != nil {
			t.Fatalf("removing what is not there: %v", err)
		}
		if all, _ = store.ListTenantLimits(ctx, q); len(all) != 2 {
			t.Fatalf("%+v", all)
		}
		// The rows go with their tenant.
		if err := st.CreateTenant(ctx, store.Tenant{ID: "T000000003", Slug: "empty", Name: "Empty", Created: at, Updated: at}); err != nil {
			t.Fatal(err)
		}
		put("T000000003", "signin.sso", "manual", "false", time.Time{}, time.Time{})
		if err := st.DeleteTenant(ctx, "T000000003"); err != nil {
			t.Fatal(err)
		}
		if all, _ = store.ListTenantLimits(ctx, q); len(all) != 2 {
			t.Fatalf("the rows of a deleted tenant: %+v", all)
		}
	})
}
