package api_test

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"petalbook.app/server/internal/store"
)

// rawGet is a GET whose body may be several JSON documents (client.do reads one).
func rawGet(c *client, path string) (int, http.Header, string) {
	c.a.t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.Host = strings.TrimPrefix(strings.TrimPrefix(c.a.public, "https://"), "http://")
	req.RemoteAddr = c.ip + ":4321"
	for _, ck := range c.cookies {
		req.AddCookie(&http.Cookie{Name: ck.Name, Value: ck.Value})
	}
	rec := httptest.NewRecorder()
	c.a.h.ServeHTTP(rec, req)
	return rec.Code, rec.Result().Header, rec.Body.String()
}

func exportPath(tenant, query string) string {
	return "/api/admin/tenants/" + tenant + "/usage/export" + query
}

// The usage export: one row per day and key, within the days asked for, as CSV
// or JSON Lines; a tenant's administrator exports their own tenant's rows and
// no other's, a site administrator any tenant's, and a person who is no
// administrator none.
func TestUsageExport(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		ctx := context.Background()
		root := a.setup("root", pw)
		root.putSetting("features.tenancy", true).want(t, 200, "")
		tenB, admB := auditTenant(t, a, root, "globex")
		_, admC := auditTenant(t, a, root, "initech")
		mem := a.addUser("mem", pw, "member")
		q := a.st.Q()
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		must(store.SetUsageDaily(ctx, q, tenB, "2026-05-01", store.UsageKeyPeople, 4))
		must(store.AddUsageDaily(ctx, q, tenB, "2026-05-01", store.UsageKeyAPIRequests, 120))
		must(store.AddUsageDaily(ctx, q, tenB, "2026-05-02", store.UsageKeyMail, 3))
		must(store.SetUsageDaily(ctx, q, tenB, "2026-05-03", store.UsageKeyBytes, 9000))
		must(store.SetUsageDaily(ctx, q, store.DefaultTenantID, "2026-05-02", store.UsageKeyPeople, 77))

		// CSV is the default: a header, then the rows by day and key.
		r := admB.do("GET", exportPath(tenB, "?from=2026-05-01&to=2026-05-02"), nil)
		if r.code != 200 || !strings.HasPrefix(r.resp.Header.Get("Content-Type"), "text/csv") || !strings.Contains(r.resp.Header.Get("Content-Disposition"), "attachment") {
			t.Fatalf("%d %v %s", r.code, r.resp.Header, r.raw)
		}
		recs, err := csv.NewReader(strings.NewReader(r.raw)).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		want := [][]string{{"tenant", "day", "key", "value"}, {tenB, "2026-05-01", "apiRequests", "120"}, {tenB, "2026-05-01", "people", "4"}, {tenB, "2026-05-02", "mail", "3"}}
		if len(recs) != len(want) {
			t.Fatalf("%v", recs)
		}
		for i := range want {
			if strings.Join(recs[i], ",") != strings.Join(want[i], ",") {
				t.Fatalf("row %d: %v, want %v", i, recs[i], want[i])
			}
		}
		// JSON Lines, the whole range.
		code, hdr, raw := rawGet(admB, exportPath(tenB, "?from=2026-05-01&to=2026-05-31&format=jsonl"))
		if code != 200 || !strings.HasPrefix(hdr.Get("Content-Type"), "application/x-ndjson") {
			t.Fatalf("%d %v", code, hdr)
		}
		lines := strings.Split(strings.TrimSpace(raw), "\n")
		if len(lines) != 4 {
			t.Fatalf("%q", raw)
		}
		var last struct {
			Tenant, Day, Key string
			Value            int64
		}
		if err := json.Unmarshal([]byte(lines[3]), &last); err != nil || last.Tenant != tenB || last.Day != "2026-05-03" || last.Key != "bytes" || last.Value != 9000 {
			t.Fatalf("%q %v", lines[3], err)
		}
		// Nothing in the range is an empty file, not an error.
		if code, _, raw = rawGet(admB, exportPath(tenB, "?from=2025-01-01&to=2025-01-31&format=jsonl")); code != 200 || raw != "" {
			t.Fatalf("%d %q", code, raw)
		}

		// Scope: a tenant's administrator reaches their own tenant only.
		admC.do("GET", exportPath(tenB, "?from=2026-05-01&to=2026-05-31"), nil).want(t, 404, "not_found")
		admB.do("GET", exportPath(store.DefaultTenantID, "?from=2026-05-01&to=2026-05-31"), nil).want(t, 404, "not_found")
		mem.do("GET", exportPath(store.DefaultTenantID, ""), nil).want(t, 403, "forbidden")
		if r = root.do("GET", exportPath(store.DefaultTenantID, "?from=2026-05-01&to=2026-05-31"), nil); r.code != 200 || !strings.Contains(r.raw, ",77") {
			t.Fatalf("%d %s", r.code, r.raw)
		}
		if r = root.do("GET", exportPath(tenB, "?from=2026-05-01&to=2026-05-31"), nil); r.code != 200 || !strings.Contains(r.raw, "9000") {
			t.Fatalf("%d %s", r.code, r.raw)
		}
		root.do("GET", exportPath("T999999999", ""), nil).want(t, 404, "not_found")

		// The days and the format are checked: at most 366 days.
		for _, bad := range []string{"?from=2026-05-01&to=2027-05-02", "?from=2026-05-05&to=2026-05-01", "?from=May&to=2026-05-01", "?from=2026-05-01&to=2026-05-02&format=xml"} {
			admB.do("GET", exportPath(tenB, bad), nil).want(t, 400, "invalid")
		}
		admB.do("GET", exportPath(tenB, "?from=2025-05-01&to=2026-05-01"), nil).want(t, 200, "") // 366 days, both ends included

		// Each export is audited with what was asked and how many rows.
		a.auth.FlushAudit(ctx)
		rows := auditKinds(t, root, "usage_exported")
		if len(rows) < 3 {
			t.Fatalf("%d usage_exported rows", len(rows))
		}
	})
}
