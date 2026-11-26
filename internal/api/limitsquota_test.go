package api_test

import (
	"context"
	"encoding/json"
	"testing"

	sheets "petalbook.app/server/internal/limits"
)

// peopleOver counts the over-quota rows for people of a tenant whose total of
// people was used (the rows carry the figures, not the tenant).
func peopleOver(t *testing.T, f *tenFx, used float64) int {
	t.Helper()
	f.a.auth.FlushAudit(context.Background())
	n := 0
	for _, row := range f.root.audit(t, "quota_exceeded") {
		if d, _ := row["detail"].(map[string]any); d["quota"] == "people" && d["scope"] == "tenant" && d["used"] == used {
			n++
		}
	}
	return n
}

// A change of the site's default limit is looked at for every tenant it moves,
// so a tenant over the new limit is told now.
func TestASiteDefaultLimitEvaluatesTheQuotasAtOnce(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		f.root.do("PUT", "/api/admin/capabilities", map[string]any{"values": map[string]any{"other.people": 1}}).want(t, 200, "")
		if peopleOver(t, f, 2) == 0 {
			t.Fatal("tenant B has two people over a limit of one, and nothing was recorded")
		}
	})
}

// A dated value that starts changes what a tenant may have; the report to the
// server looks at the totals again.
func TestADatedValueStartingEvaluatesTheQuotas(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		if _, err := a.h.Limits().SetManual(context.Background(), f.tenB, "", map[string]*sheets.Change{"other.people": {Value: json.RawMessage("1")}}); err != nil {
			t.Fatal(err)
		}
		if n := peopleOver(t, f, 2); n != 0 {
			t.Fatalf("%d rows before the boundary", n)
		}
		a.h.Boundary(f.tenB, sheets.Applied{Key: "other.people", To: int64(1), Source: sheets.FromManual})
		if peopleOver(t, f, 2) == 0 {
			t.Fatal("the boundary did not look at the quotas")
		}
	})
}

// A server whose limits sheet could not be read answers 503 where it would have
// read it, and does not crash.
func TestWithoutTheSheetTheLimitsRoutesAnswer503(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		a.h.DropLimits()
		tenant := "/api/admin/tenants/" + f.tenB
		for _, rt := range [][2]string{{"GET", "/api/admin/capabilities"}, {"PUT", "/api/admin/capabilities"}, {"GET", tenant + "/limits"},
			{"PUT", tenant + "/limits"}, {"GET", tenant + "/capabilities"}, {"PUT", tenant + "/plan"}, {"GET", "/api/admin/plans"}, {"POST", "/api/admin/plans"},
			{"GET", "/api/admin/plans/x"}, {"PATCH", "/api/admin/plans/x"}, {"DELETE", "/api/admin/plans/x"}} {
			f.root.do(rt[0], rt[1], map[string]any{}).want(t, 503, "unavailable")
		}
	})
}
