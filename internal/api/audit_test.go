package api_test

import (
	"context"
	"encoding/csv"
	"strings"
	"testing"
	"time"

	"petalbook.app/server/internal/api"
	"petalbook.app/server/internal/auth"
)

// auditCounts lists the kinds of a page of the log as a count by kind.
func auditCounts(t *testing.T, c *client, query string) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, it := range c.do("GET", "/api/admin/audit?limit=200"+query, nil).want(t, 200, "").body["items"].([]any) {
		out[it.(map[string]any)["kind"].(string)]++
	}
	return out
}

// The filters of the log combine; a tenant's administrators see the rows about
// their tenant, a site administrator's changes included, and never the kinds
// of the server's own; the registry of kinds tells the web the same.
func TestAuditFiltersCategoriesAndVisibility(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		rootID := mustUserID(t, a, "root")
		a.clock.Advance(time.Second)
		a.auth.Audit(auth.AuditEvent{Kind: "tenant_changed", Actor: rootID, Tenant: f.tenB, Detail: map[string]any{"name": "Acme"}})
		a.auth.Audit(auth.AuditEvent{Kind: "provider_created", Actor: rootID, Tenant: f.tenB})
		a.auth.Audit(auth.AuditEvent{Kind: "setting_changed", Actor: rootID, Tenant: f.tenB, Capacity: "site_admin"})
		a.auth.Audit(auth.AuditEvent{Kind: "space_deleted", Actor: rootID, Tenant: f.tenB})

		b := auditCounts(t, f.admB, "")
		if b["tenant_changed"] != 1 || b["tenant_created"] != 1 || b["setting_changed"] != 1 || b["provider_created"] != 0 {
			t.Fatalf("tenant B's administrator sees %v", b)
		}
		if all := auditCounts(t, f.root, ""); all["provider_created"] != 1 {
			t.Fatalf("a site administrator sees %v", all)
		}
		// Categories: one, several (repeated or with commas), and one that is not.
		if got := auditCounts(t, f.root, "&category=data"); got["space_deleted"] < 1 || got["tenant_changed"] != 0 || got["login_failed"] != 0 {
			t.Fatalf("data: %v", got)
		}
		both := auditCounts(t, f.root, "&category=data&category=security")
		if both["space_deleted"] < 1 || both["provider_created"] != 1 || both["tenant_changed"] != 0 {
			t.Fatalf("data and security: %v", both)
		}
		if got := auditCounts(t, f.root, "&category=data,security"); len(got) != len(both) {
			t.Fatalf("with a comma: %v", got)
		}
		// The rest of the filters: kind, actor, capacity, the tenant of a site administrator.
		if got := auditCounts(t, f.root, "&category=admin&kind=tenant_changed&actor="+rootID); len(got) != 1 {
			t.Fatalf("kind and actor: %v", got)
		}
		// The fixture's own administration records its capacity too (the site
		// administrator's setting and new tenant; B's administrator's invites).
		if got := auditCounts(t, f.root, "&capacity=site_admin"); got["setting_changed"] != 3 || got["tenant_created"] != 1 || got["invites_batch"] != 0 || got["tenant_changed"] != 0 {
			t.Fatalf("capacity: %v", got)
		}
		for _, it := range f.root.do("GET", "/api/admin/audit?limit=200&capacity=tenant_admin", nil).want(t, 200, "").body["items"].([]any) {
			if it.(map[string]any)["capacity"] != "tenant_admin" {
				t.Fatalf("capacity=tenant_admin: %v", it)
			}
		}
		if got := auditCounts(t, f.root, "&tenant="+f.tenB+"&category=data"); got["space_deleted"] < 1 || got["login_failed"] != 0 {
			t.Fatalf("tenant: %v", got)
		}
		// The date range: a day includes the whole day; a time is exact.
		day := a.clock.Now().UTC().Format("2006-01-02")
		if got := auditCounts(t, f.root, "&category=data&from="+day+"&to="+day); got["space_deleted"] < 1 {
			t.Fatalf("today: %v", got)
		}
		if got := auditCounts(t, f.root, "&from=2099-01-01"); len(got) != 0 {
			t.Fatalf("the future: %v", got)
		}
		if got := auditCounts(t, f.root, "&to=2001-01-01T00:00:00Z"); len(got) != 0 {
			t.Fatalf("the past: %v", got)
		}
		for _, bad := range []string{"category=nope", "capacity=root", "from=yesterday", "to=2026-13-40"} {
			f.root.do("GET", "/api/admin/audit?"+bad, nil).want(t, 400, "invalid")
		}
		// Another tenant's id is not found for a tenant administrator.
		f.admB.do("GET", "/api/admin/audit?tenant="+f.tenB+"x", nil).want(t, 404, "")

		// The registry: every kind with its category, without the server's own for a tenant.
		kinds := func(c *client) map[string]bool {
			out := map[string]bool{}
			for _, cat := range c.do("GET", "/api/admin/audit/kinds", nil).want(t, 200, "").body["categories"].([]any) {
				for _, k := range cat.(map[string]any)["kinds"].([]any) {
					out[k.(map[string]any)["name"].(string)] = k.(map[string]any)["site"].(bool)
				}
			}
			return out
		}
		if k := kinds(f.root); !k["provider_created"] || k["login"] || len(k) < 60 {
			t.Fatalf("the site's kinds: %v", k)
		}
		if k := kinds(f.admB); len(k) < 50 || k["provider_created"] || k["lockout"] || k["dropped"] || k["tenant_changed"] != false {
			t.Fatalf("the tenant's kinds: %v", k)
		}
		f.memB.do("GET", "/api/admin/audit/kinds", nil).want(t, 403, "")
	})
}

// An optional category is written while its switch is on and not at all while it
// is off, for the tenant the event belongs to; core ones cannot be switched.
func TestAuditOptionalCategoriesFollowTheirSettings(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		logins := func() int { return auditCounts(t, f.root, "&kind=login&tenant="+f.tenB)["login"] }
		signIn := func() {
			a.newClient("").do("POST", "/api/login", map[string]any{"username": "mem-b", "password": pw}).want(t, 200, "")
		}
		before := logins()
		signIn()
		if logins() != before+1 {
			t.Fatal("a sign-in is written by default")
		}
		// The site turns signins off: nothing is written, and the registry says so.
		f.root.putSetting("audit.signins", false).want(t, 200, "")
		signIn()
		if logins() != before+1 {
			t.Fatal("a sign-in was written with the category off")
		}
		cats := f.root.do("GET", "/api/admin/audit/kinds", nil).want(t, 200, "").body["categories"].([]any)
		on := map[string]bool{}
		for _, c := range cats {
			m := c.(map[string]any)
			on[m["id"].(string)] = m["on"].(bool)
			if m["core"] == true && m["setting"] != nil {
				t.Fatalf("a core category with a switch: %v", m)
			}
		}
		if on["signins"] || !on["security"] || on["reads"] {
			t.Fatalf("on: %v", on)
		}
		// A tenant may switch it back on for itself while the site only allows.
		f.root.putSetting("audit.signins", true).want(t, 200, "")
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/audit.signins", map[string]any{"value": false}).want(t, 200, "")
		signIn()
		if logins() != before+1 {
			t.Fatal("a sign-in was written for a tenant that turned them off")
		}
		// No setting exists for a core category to switch it off, and the
		// retention has no floor: days from 1, or 0 for no age limit.
		f.root.putSetting("audit.security", false).want(t, 404, "")
		f.root.putSetting("audit.keepDays", -1).want(t, 400, "")
		f.root.putSetting("audit.keepDays", 1).want(t, 200, "")
		f.root.putSetting("audit.keepDays", 0).want(t, 200, "")
		f.root.putSetting("audit.keepDays", 120).want(t, 200, "")
		f.root.putSetting("audit.keepRows", 5000).want(t, 200, "")
		f.root.putSetting("audit.keepMode", "and").want(t, 200, "")
		f.root.putSetting("audit.optionalKeepDays", 7).want(t, 200, "")
		f.root.putSetting("audit.optionalKeepDays", 0).want(t, 400, "")
		f.root.putSetting("audit.optionalKeepDays", 3651).want(t, 400, "")
		// Each category has days of its own: 0 forever, null follows the rest.
		f.root.putSetting("audit.keepDays.security", 1).want(t, 200, "")
		f.root.putSetting("audit.keepDays.data", 0).want(t, 200, "")
		f.root.putSetting("audit.keepDays.system", 3651).want(t, 400, "")
		if got := a.auth.AuditRetention(context.Background()); got.CategoryDays["security"] != 1 || got.CategoryDays["data"] != 0 || len(got.CategoryDays) != 2 {
			t.Fatalf("the trim reads %+v", got)
		}
		f.root.putSetting("audit.keepDays.security", nil).want(t, 200, "")
		f.root.putSetting("audit.keepDays.data", nil).want(t, 200, "")
		if got := a.auth.AuditRetention(context.Background()); got.Days != 120 || got.Rows != 5000 || got.Mode != "and" || got.OptionalDays != 7 {
			t.Fatalf("the trim reads %+v", got)
		}
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/audit.keepRows", map[string]any{"value": 5000}).want(t, 404, "")
	})
}

// A plan is the site's: its rows say which tenants it touches, so no tenant's
// administrator sees them, not in the list, the kinds or the download.
func TestPlanRowsAreTheSites(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		f.root.do("POST", "/api/admin/plans", map[string]any{"slug": "team", "name": "Team"}).want(t, 201, "")
		f.root.do("PATCH", "/api/admin/plans/team", map[string]any{"name": "Teams"}).want(t, 200, "")
		f.root.do("DELETE", "/api/admin/plans/team", nil).want(t, 204, "")
		a.auth.FlushAudit(context.Background())
		kinds := []string{"plan_created", "plan_changed", "plan_deleted"}
		for _, k := range kinds {
			if len(f.root.audit(t, k)) != 1 {
				t.Fatalf("the site administrator sees no %s row", k)
			}
			if got := auditCounts(t, f.admA, "&kind="+k); len(got) != 0 {
				t.Fatalf("a tenant administrator sees %v", got)
			}
		}
		for _, cat := range f.admA.do("GET", "/api/admin/audit/kinds", nil).want(t, 200, "").body["categories"].([]any) {
			for _, k := range cat.(map[string]any)["kinds"].([]any) {
				if n := k.(map[string]any)["name"].(string); strings.HasPrefix(n, "plan_") {
					t.Fatalf("the tenant's kinds hold %s", n)
				}
			}
		}
		if raw := f.admA.do("GET", "/api/admin/audit/download?category=admin", nil).want(t, 200, "").raw; strings.Contains(raw, "plan_") {
			t.Fatal(raw)
		}
	})
}

// A cell is escaped when a spreadsheet would read it as a formula, past leading
// blanks and |.
func TestCSVCellEscapes(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{" =1+1": "' =1+1", "|=cmd": "'|=cmd", "\t=1": "'\t=1", "\r\n @x": "'\r\n @x", "=1": "'=1",
		"": "", "a=1": "a=1", " plain": " plain", "| - |": "'| - |", "-": "'-"} {
		if got := api.CSVCell(in); got != want {
			t.Errorf("%q gives %q, want %q", in, got, want)
		}
	}
}

// The download is CSV of the filter, for the caller's tenant only, never with a
// site kind, safe to open in a spreadsheet, capped, one at a time, and audited.
func TestAuditDownloadIsCSV(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		rootID := mustUserID(t, a, "root")
		a.clock.Advance(time.Second)
		a.auth.Audit(auth.AuditEvent{Kind: "login_failed", Username: "=cmd|' /C calc'!A0", Tenant: f.tenB, Detail: map[string]any{"reason": "bad_password"}})
		a.auth.Audit(auth.AuditEvent{Kind: "login_failed", Username: "+1; -2, \"quoted\"", Tenant: f.tenB})
		a.auth.Audit(auth.AuditEvent{Kind: "provider_created", Actor: rootID, Tenant: f.tenB})
		a.auth.Audit(auth.AuditEvent{Kind: "login_failed", Username: "other-tenant"}) // the default tenant's

		r := f.admB.do("GET", "/api/admin/audit/download?category=security", nil).want(t, 200, "")
		if ct := r.resp.Header.Get("Content-Type"); ct != "text/csv; charset=utf-8" {
			t.Fatalf("content type %q", ct)
		}
		if cd := r.resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment; filename=\"audit-") {
			t.Fatalf("disposition %q", cd)
		}
		rows, err := csv.NewReader(strings.NewReader(r.raw)).ReadAll()
		if err != nil {
			t.Fatalf("%v\n%s", err, r.raw)
		}
		if strings.Join(rows[0], ",") != "id,time,category,kind,capacity,tenant,actor,user,username,ip,detail" {
			t.Fatalf("header %v", rows[0])
		}
		var names []string
		for _, row := range rows[1:] {
			if row[3] == "provider_created" || row[5] != f.tenB {
				t.Fatalf("a row of the server's own or of another tenant: %v", row)
			}
			names = append(names, row[8])
		}
		// Newest first; a cell that starts like a formula is quoted as text.
		if got := strings.Join(names, "|"); !strings.Contains(got, "'+1; -2, \"quoted\"|'=cmd|' /C calc'!A0") {
			t.Fatalf("usernames %q", got)
		}
		for _, row := range rows[1:] {
			for _, cell := range row {
				if cell != "" && strings.ContainsRune("=+-@\t\r", rune(cell[0])) {
					t.Fatalf("an unescaped cell %q", cell)
				}
			}
		}
		// A site administrator gets every tenant's rows, the server's own too.
		all := f.root.do("GET", "/api/admin/audit/download?kind=provider_created", nil).want(t, 200, "")
		if !strings.Contains(all.raw, "provider_created") {
			t.Fatal(all.raw)
		}
		// The download is a row of the log, with its filter and count.
		a.auth.FlushAudit(context.Background())
		d := f.root.do("GET", "/api/admin/audit?kind=audit_downloaded", nil).want(t, 200, "").body["items"].([]any)
		if len(d) != 2 {
			t.Fatalf("%d audited downloads", len(d))
		}
		detail := d[1].(map[string]any)["detail"].(map[string]any)
		if detail["category"] != "security" || detail["rows"] != float64(len(rows)-1) || detail["truncated"] != false || d[1].(map[string]any)["category"] != "data" {
			t.Fatalf("%v", d[1])
		}

		// Capped: it says so after the body.
		defer api.SetAuditDownloadMax(api.SetAuditDownloadMax(1))
		capped := f.admB.do("GET", "/api/admin/audit/download?category=security", nil).want(t, 200, "")
		if n := strings.Count(capped.raw, "\n"); n != 3 || !strings.Contains(capped.raw, "\nTruncated: the first 100000 rows; narrow the filter to see the rest.,") {
			t.Fatalf("%d lines in a download capped at 1 row, the last one saying so:\n%s", n, capped.raw)
		}
		_ = capped.resp.Body.Close()
		if capped.resp.Trailer.Get("X-Audit-Truncated") != "true" {
			t.Fatalf("no sign of the cap: %v", capped.resp.Trailer)
		}

		// One at a time, and a bad filter is refused before anything is sent.
		release := api.HoldAuditDownload(mustUserID(t, a, "adm-b"))
		f.admB.do("GET", "/api/admin/audit/download", nil).want(t, 429, "rate_limited")
		release()
		f.admB.do("GET", "/api/admin/audit/download?category=nope", nil).want(t, 400, "invalid")
		f.memB.do("GET", "/api/admin/audit/download", nil).want(t, 403, "")

		// Off for the tenant: 403 feature_off, and the list still works.
		f.root.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/features.auditDownload", map[string]any{"siteLock": true, "value": false}).want(t, 200, "")
		f.admB.do("GET", "/api/admin/audit/download", nil).want(t, 403, "feature_off")
		f.admB.do("GET", "/api/admin/audit", nil).want(t, 200, "")
		f.root.do("GET", "/api/admin/audit/download", nil).want(t, 200, "")
	})
}
