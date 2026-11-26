package limits

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
)

func entryOf(f *fixture, tenant, key string) Entry {
	for _, e := range f.svc.Describe(tenant) {
		if e.Key == key {
			return e
		}
	}
	panic(key)
}

func TestADatedWriteReplacesAnUndatedValueAndSaysSo(t *testing.T) {
	t.Parallel()
	each(t, func(t *testing.T, f *fixture) {
		const key = "storage.quota.bytes"
		h := func(n int) time.Time { return t0.Add(time.Duration(n) * time.Hour) }
		f.manual(tenB, key, raw("100"))
		res, err := f.svc.SetManual(ctx, tenB, "U1", map[string]*Change{key: window("200", h(1), h(2))})
		if err != nil || !slices.Equal(res.Replaced, []string{key}) {
			t.Fatalf("%+v %v", res, err)
		}
		a := res.Applied[0]
		if a.From != int64(100) || a.To != nil || a.Source != FromManual || !a.Start.Equal(h(1)) {
			t.Fatalf("before the window the site's value applies: %+v", a)
		}
		// Outside the window the undated value is not back: there is one row.
		e := entryOf(f, tenB, key)
		if e.Source != FromSite || e.Manual == nil || e.Manual.Value != int64(200) {
			t.Fatalf("%+v", e)
		}
		f.clk.advanceTo(h(1))
		e = entryOf(f, tenB, key)
		if e.Value != int64(200) || e.Source != FromManual || !e.Until.Equal(h(2)) || e.Underlying == nil || e.Underlying.Source != FromSite || e.Manual != nil {
			t.Fatalf("%+v", e)
		}
		// Another window over a dated one is not a replacement of an undated value.
		res, _ = f.svc.SetManual(ctx, tenB, "U1", map[string]*Change{key: window("300", h(3), h(4))})
		if len(res.Replaced) != 0 {
			t.Fatalf("%v", res.Replaced)
		}
		res, _ = f.svc.SetManual(ctx, tenB, "U1", map[string]*Change{key: nil})
		if !res.Applied[0].Removed || res.Applied[0].Source != FromSite {
			t.Fatalf("%+v", res.Applied[0])
		}
	})
}

func TestAPlanReplacesTheOverridesAndAnEndedValueIsReported(t *testing.T) {
	t.Parallel()
	each(t, func(t *testing.T, f *fixture) {
		q := f.st.Q()
		if err := store.InsertPlan(ctx, q, store.Plan{ID: "P1", Slug: "team", Name: "Team", Values: `{"other.people": 10}`}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.Reload(ctx); err != nil {
			t.Fatal(err)
		}
		var told []Applied
		f.svc.boundary = func(tenant string, a Applied) { told = append(told, a) }
		f.manual(tenB, "other.people", window("99", t0.Add(time.Hour), t0.Add(2*time.Hour)))
		f.manual(tenB, "signin.sso", raw("false"))
		f.setAs(tenB, store.LimitBilling, "usage.mailPerDay", raw("5"))
		f.clk.advanceTo(t0.Add(time.Hour))
		if len(told) != 1 || told[0].Key != "other.people" || told[0].To != int64(99) {
			t.Fatalf("a start was not reported: %+v", told)
		}

		cleared, err := f.svc.SetPlan(ctx, tenB, "P1")
		if err != nil || len(cleared) != 2 || cleared[0].Key != "other.people" || cleared[1].Key != "signin.sso" {
			t.Fatalf("%+v %v", cleared, err)
		}
		sh := f.svc.Sheet(tenB)
		if n, _ := sh.Limit("other.people"); n != 10 || !sh.Bool("signin.sso") {
			t.Fatal("the overrides still apply")
		}
		if n, _ := sh.Limit("usage.mailPerDay"); n != 5 {
			t.Fatal("billing is not an override and stays")
		}
		if f.svc.PlanID(tenB) != "P1" || f.svc.PlanTenants()["P1"] != 1 {
			t.Fatal("plan")
		}
		if _, err := f.svc.SetPlan(ctx, tenB, "nope"); err == nil {
			t.Fatal("a plan that does not exist")
		}
		if _, err := f.svc.SetPlan(ctx, tenB, ""); err != nil || f.svc.PlanID(tenB) != "" {
			t.Fatal(err)
		}
		if p := f.clk.pending(); len(p) != 0 {
			t.Fatalf("the cleared window still has a timer: %v", p)
		}
	})
}

func TestTheSiteDefaultNamesTheTenantsItChanges(t *testing.T) {
	t.Parallel()
	each(t, func(t *testing.T, f *fixture) {
		f.manual(tenB, "signin.idpAdmins", raw("false"))
		affected, err := f.svc.SetSite(ctx, map[string]json.RawMessage{"signin.idpAdmins": json.RawMessage("true")})
		if err != nil || !slices.Equal(affected, []string{store.DefaultTenantID}) {
			t.Fatalf("%v %v", affected, err)
		}
		if f.svc.Sheet(tenB).Bool("signin.idpAdmins") || !f.svc.Sheet(store.DefaultTenantID).Bool("signin.idpAdmins") {
			t.Fatal("the override still wins, the default is on")
		}
		if _, err := f.svc.SetSite(ctx, map[string]json.RawMessage{"signin.idpAdmins": json.RawMessage("1")}); err == nil {
			t.Fatal("an invalid value")
		}
		if _, err := f.svc.SetSite(ctx, map[string]json.RawMessage{"signin.idpAdmins": nil}); err != nil || f.svc.Sheet(store.DefaultTenantID).Bool("signin.idpAdmins") {
			t.Fatal(err)
		}
	})
}

func TestForgettingATenantDropsItsInputs(t *testing.T) {
	t.Parallel()
	each(t, func(t *testing.T, f *fixture) {
		f.manual(tenB, "other.people", window("1", t0.Add(time.Hour), time.Time{}))
		f.svc.Forget(tenB)
		if len(f.svc.rows) != 0 || f.svc.Sheet(tenB) != f.svc.cur.Load().site {
			t.Fatal("still held")
		}
		if p := f.clk.pending(); len(p) != 0 {
			t.Fatalf("the timer waits for a tenant that is gone: %v", p)
		}
	})
}
