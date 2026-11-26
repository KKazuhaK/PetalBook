package api_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"petalbook.app/server/internal/store"
)

// measure asks for a measurement of a space now (what the background sizer does
// on its own turn) and flushes the audit log.
func (f *tenFx) measure(t *testing.T, c *client, space string) {
	t.Helper()
	c.do("POST", "/api/admin/spaces/"+space+"/size", nil).want(t, 200, "")
	f.a.auth.FlushAudit(context.Background())
}

func usageOf(t *testing.T, c *client) map[string]any {
	t.Helper()
	return c.do("GET", "/api/account/usage", nil).want(t, 200, "").body
}

func usedOf(t *testing.T, u map[string]any, k string) float64 {
	return u[k].(map[string]any)["used"].(float64)
}

func limitOf(u map[string]any, k string) any { return u[k].(map[string]any)["limit"] }

func addPages(t *testing.T, c *client, space string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		c.do("POST", "/api/spaces/"+space+"/pages", map[string]any{"folder": "F", "meta": map[string]any{"title": fmt.Sprintf("page %d", i)}}).want(t, 201, "")
	}
}

func TestAccountUsageIsTheCallersOwnNumbers(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		u := usageOf(t, f.memA)
		// The personal space is one; the person has nothing measured yet.
		if usedOf(t, u, "spaces") != 1 || limitOf(u, "spaces") != float64(20) || usedOf(t, u, "bytes") != 0 || limitOf(u, "bytes") != nil ||
			limitOf(u, "pages") != nil || u["measuredAt"] != nil || len(u["notices"].([]any)) != 0 {
			t.Fatalf("%v", u)
		}
		f.memA.do("POST", "/api/spaces", map[string]any{"name": "Second"}).want(t, 201, "")
		mine := f.memA.personalSpace()
		addPages(t, f.memA, mine, 3)
		// Nothing is measured on the request path: the numbers are the stored ones.
		if u = usageOf(t, f.memA); usedOf(t, u, "spaces") != 2 || usedOf(t, u, "bytes") != 0 || usedOf(t, u, "pages") != 0 {
			t.Fatalf("%v", u)
		}
		f.measure(t, f.root, mine) // a personal space: the site measures it, a tenant administrator never reaches it
		u = usageOf(t, f.memA)
		if usedOf(t, u, "bytes") <= 0 || usedOf(t, u, "pages") != 3 || u["measuredAt"] == nil {
			t.Fatalf("%v", u)
		}
		// Others' numbers do not mix in: the same tenant's administrator, another tenant's member.
		if o := usageOf(t, f.memB); usedOf(t, o, "bytes") != 0 || usedOf(t, o, "pages") != 0 {
			t.Fatalf("%v", o)
		}
		// Deleting a space gives its numbers back.
		sec := f.memA.do("GET", "/api/spaces", nil).body["spaces"].([]any)
		var second string
		for _, s := range sec {
			if s.(map[string]any)["name"] == "Second" {
				second = s.(map[string]any)["id"].(string)
			}
		}
		f.admA.do("DELETE", "/api/admin/spaces/"+second, nil).want(t, 200, "")
		if u = usageOf(t, f.memA); usedOf(t, u, "spaces") != 1 {
			t.Fatalf("%v", u)
		}
		a.newClient("").do("GET", "/api/account/usage", nil).want(t, 401, "")
	})
}

// The limit on spaces is a layered setting: the site's, a tenant's own, and a
// person's own on top; it is the one quota that is refused, with what ran out.
func TestTheSpaceLimitIsLayeredAndRefused(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		mk := func(c *client, name string) result { return c.do("POST", "/api/spaces", map[string]any{"name": name}) }
		// The site: 3 each (the personal space is the first).
		f.root.do("PUT", "/api/admin/settings/quota.spacesPerPerson", map[string]any{"value": 3}).want(t, 200, "")
		mk(f.memA, "A2").want(t, 201, "")
		mk(f.memA, "A3").want(t, 201, "")
		r := mk(f.memA, "A4").want(t, 403, "quota_exceeded")
		if r.body["quota"] != "spaces" || r.body["limit"] != float64(3) || r.body["used"] != float64(3) {
			t.Fatalf("%s", r.raw)
		}
		// The tenant: B's administrator raises nothing above the site's by a restriction, but
		// may lower it for their people.
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/quota.spacesPerPerson", map[string]any{"value": 2}).want(t, 200, "")
		mk(f.memB, "B2").want(t, 201, "")
		if r = mk(f.memB, "B3").want(t, 403, "quota_exceeded"); r.body["limit"] != float64(2) {
			t.Fatalf("%s", r.raw)
		}
		// A's tenant still has the site's 3, and a person's own limit is above both.
		f.root.do("PATCH", "/api/users/"+f.userB, map[string]any{"quota": map[string]any{"spaces": 5}}).want(t, 200, "")
		mk(f.memB, "B3").want(t, 201, "")
		mk(f.memB, "B4").want(t, 201, "")
		// 0 is no limit for a person; null follows the tenant again.
		f.root.do("PATCH", "/api/users/"+f.userB, map[string]any{"quota": map[string]any{"spaces": 0}}).want(t, 200, "")
		mk(f.memB, "B5").want(t, 201, "")
		f.root.do("PATCH", "/api/users/"+f.userB, map[string]any{"quota": map[string]any{"spaces": nil}}).want(t, 200, "")
		mk(f.memB, "B6").want(t, 403, "quota_exceeded")
		// The account says what applies.
		if u := usageOf(t, f.memB); limitOf(u, "spaces") != float64(2) {
			t.Fatalf("%v", u)
		}
		// A tenant's total of spaces is refused too, whoever asks.
		setTotals(t, f.root, f.tenB, map[string]any{"spaces": 7})
		f.root.do("PATCH", "/api/users/"+f.userB, map[string]any{"quota": map[string]any{"spaces": 50}}).want(t, 200, "")
		r = mk(f.memB, "B7").want(t, 403, "quota_exceeded")
		if r.body["limit"] != float64(7) {
			t.Fatalf("%s", r.raw)
		}
		// 0 spaces for the site is no limit.
		f.root.do("PUT", "/api/admin/settings/quota.spacesPerPerson", map[string]any{"value": 0}).want(t, 200, "")
		mk(f.memA, "A4").want(t, 201, "")
	})
}

// --max-spaces, when given, is the site's value and read only; without it the
// stored setting (20) is.
func TestMaxSpacesFlagIsTheSitesReadOnlyQuota(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{maxSpaces: 4}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		r := root.do("GET", "/api/admin/settings", nil).want(t, 200, "")
		s := settingOf(t, r, "quota.spacesPerPerson")["site"].(map[string]any)
		if s["value"] != float64(4) || s["fromFlag"] != true {
			t.Fatalf("%v", s)
		}
		root.do("PUT", "/api/admin/settings/quota.spacesPerPerson", map[string]any{"value": 9}).want(t, 409, "conflict")
		// Its lower rule can still be set; the bytes and pages are the stored ones.
		root.do("PUT", "/api/admin/settings/quota.bytesPerPerson", map[string]any{"value": 1000}).want(t, 200, "")
		if u := usageOf(t, root); limitOf(u, "spaces") != float64(4) || limitOf(u, "bytes") != float64(1000) {
			t.Fatalf("%v", u)
		}
	})
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		s := settingOf(t, root.do("GET", "/api/admin/settings", nil).want(t, 200, ""), "quota.spacesPerPerson")["site"].(map[string]any)
		if s["value"] != float64(20) || s["fromFlag"] != nil {
			t.Fatalf("%v", s)
		}
	})
}

func TestSettingValuesOfTheQuotasAreChecked(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		put := func(k string, v any) result {
			return f.root.do("PUT", "/api/admin/settings/"+k, map[string]any{"value": v})
		}
		put("quota.spacesPerPerson", -1).want(t, 400, "invalid")
		put("quota.spacesPerPerson", 10001).want(t, 400, "invalid")
		if r := put("quota.spacesPerPerson", nil).want(t, 200, ""); r.body["site"].(map[string]any)["value"] != float64(20) { // null: back to the default
			t.Fatalf("%s", r.raw)
		}
		put("quota.bytesPerPerson", 0).want(t, 400, "invalid")
		put("quota.bytesPerPerson", "big").want(t, 400, "invalid")
		put("quota.bytesPerPerson", nil).want(t, 200, "") // null: no limit
		put("quota.pagesPerPerson", 100).want(t, 200, "")
		// A tenant may be locked out of them by the site.
		f.root.do("PUT", "/api/admin/settings/quota.pagesPerPerson", map[string]any{"lower": map[string]any{"mode": "restrict", "max": 500}}).want(t, 200, "")
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/quota.pagesPerPerson", map[string]any{"value": 600}).want(t, 409, "setting_restricted")
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/quota.pagesPerPerson", map[string]any{"value": 400}).want(t, 200, "")
		f.root.do("PUT", "/api/admin/settings/quota.pagesPerPerson", map[string]any{"lower": map[string]any{"mode": "lock"}}).want(t, 200, "")
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/quota.pagesPerPerson", map[string]any{"value": 300}).want(t, 409, "setting_locked")
		// The person layer of a quota is not a setting a person has.
		for _, it := range f.memA.do("GET", "/api/settings", nil).want(t, 200, "").body["settings"].([]any) {
			if strings.HasPrefix(it.(map[string]any)["key"].(string), "quota.") {
				t.Fatalf("%v", it)
			}
		}
	})
}

// quota on PATCH /api/users/{id}: who may set what, and that a refusal changes nothing.
func TestAPersonsOwnLimitsHaveRules(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		path := "/api/users/" + f.userB
		set := func(c *client, q any) result { return c.do("PATCH", path, map[string]any{"quota": q}) }
		f.root.do("PUT", "/api/admin/settings/quota.bytesPerPerson", map[string]any{"value": 1000}).want(t, 200, "")
		f.root.do("PUT", "/api/admin/settings/quota.spacesPerPerson", map[string]any{"value": 4}).want(t, 200, "")
		// The tenant's administrator may lower, not raise (nor lift) a person's limit.
		r := set(f.admB, map[string]any{"bytes": 500, "spaces": 3}).want(t, 200, "")
		q := r.user()["quota"].(map[string]any)
		if q["bytes"] != float64(500) || q["spaces"] != float64(3) || q["pages"] != nil {
			t.Fatalf("%s", r.raw)
		}
		set(f.admB, map[string]any{"bytes": 5000}).want(t, 403, "forbidden")
		set(f.admB, map[string]any{"spaces": 0}).want(t, 403, "forbidden") // no limit is the highest
		set(f.admB, map[string]any{"spaces": 5}).want(t, 403, "forbidden")
		// A refusal changes nothing, also of the other fields of the same request.
		set(f.admB, map[string]any{"bytes": 5000}).want(t, 403, "forbidden")
		f.admB.do("PATCH", path, map[string]any{"disabled": true, "quota": map[string]any{"bytes": 5000}}).want(t, 403, "forbidden")
		if got := f.root.do("GET", "/api/users?tenant="+f.tenB, nil).want(t, 200, "").raw; strings.Contains(got, `"disabled":true`) {
			t.Fatalf("the refused patch changed another field: %s", got)
		}
		// The site's administrator may go above the tenant.
		if r = set(f.root, map[string]any{"bytes": 5000}).want(t, 200, ""); r.user()["quota"].(map[string]any)["bytes"] != float64(5000) {
			t.Fatalf("%s", r.raw)
		}
		// The list shows it.
		var seen bool
		for _, u := range f.admB.do("GET", "/api/users", nil).want(t, 200, "").body["users"].([]any) {
			m := u.(map[string]any)
			if m["id"] == f.userB {
				seen = m["quota"].(map[string]any)["bytes"] == float64(5000)
			} else if m["quota"].(map[string]any)["bytes"] != nil {
				t.Fatalf("%v", m)
			}
		}
		if !seen {
			t.Fatal("the list does not show the person's limit")
		}
		// Bad requests.
		for _, bad := range []any{"x", []any{1}, map[string]any{"disk": 1}, map[string]any{"bytes": "1"}, map[string]any{"bytes": 0}, map[string]any{"spaces": -1},
			map[string]any{"spaces": 10001}, map[string]any{"pages": 1.5}} {
			set(f.root, bad).want(t, 400, "invalid")
		}
		// Not another tenant's person, and not by a member.
		f.admA.do("PATCH", path, map[string]any{"quota": map[string]any{"bytes": 1}}).want(t, 404, "not_found")
		f.memA.do("PATCH", path, map[string]any{"quota": map[string]any{"bytes": 1}}).want(t, 403, "forbidden")
		// null clears.
		r = set(f.root, map[string]any{"bytes": nil, "spaces": nil}).want(t, 200, "")
		if q = r.user()["quota"].(map[string]any); q["bytes"] != nil || q["spaces"] != nil {
			t.Fatalf("%s", r.raw)
		}
		// A lock above stops even the site's administrator at the site's own rule.
		f.root.do("PUT", "/api/admin/settings/quota.pagesPerPerson", map[string]any{"lower": map[string]any{"mode": "lock"}}).want(t, 200, "")
		set(f.root, map[string]any{"pages": 10}).want(t, 409, "setting_locked")
	})
}

// Bytes and pages only warn: the person is told (a notice), the administrators
// see who is over, and the audit log gets one row for each crossing.
func TestBytesAndPagesWarnOncePerCrossing(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		mine := f.memA.personalSpace()
		addPages(t, f.memA, mine, 4)
		f.root.do("PUT", "/api/admin/settings/quota.pagesPerPerson", map[string]any{"value": 3}).want(t, 200, "")
		f.root.do("PUT", "/api/admin/settings/quota.bytesPerPerson", map[string]any{"value": 1}).want(t, 200, "")
		f.measure(t, f.root, mine) // a personal space: the site measures it, a tenant administrator never reaches it
		u := usageOf(t, f.memA)
		notices := u["notices"].([]any)
		if len(notices) != 2 {
			t.Fatalf("%v", u)
		}
		got := map[string]map[string]any{}
		for _, n := range notices {
			m := n.(map[string]any)
			got[m["quota"].(string)] = m
		}
		if got["pages"]["scope"] != "person" || got["pages"]["limit"] != float64(3) || got["pages"]["used"] != float64(4) || got["bytes"]["limit"] != float64(1) {
			t.Fatalf("%v", notices)
		}
		// Nothing was refused: the person goes on writing.
		addPages(t, f.memA, mine, 1)
		// One audit row per quota crossed, however often it is measured.
		rows := f.root.audit(t, "quota_exceeded")
		if len(rows) != 2 {
			t.Fatalf("%d rows: %v", len(rows), rows)
		}
		f.measure(t, f.root, mine) // a personal space: the site measures it, a tenant administrator never reaches it
		f.measure(t, f.root, mine) // a personal space: the site measures it, a tenant administrator never reaches it
		if rows = f.root.audit(t, "quota_exceeded"); len(rows) != 2 {
			t.Fatalf("a crossing was audited again: %d rows", len(rows))
		}
		detail := rows[0]["detail"].(map[string]any)
		if (detail["quota"] != "pages" && detail["quota"] != "bytes") || detail["scope"] != "person" || detail["limit"] == nil || detail["used"] == nil || rows[0]["user"] != f.userA {
			t.Fatalf("%v", rows[0])
		}
		// The administrators see who is over.
		r := f.admA.do("GET", "/api/admin/usage?over=1", nil).want(t, 200, "")
		items := r.body["items"].([]any)
		if len(items) != 1 || items[0].(map[string]any)["user"].(map[string]any)["id"] != f.userA ||
			strings.Join(strs(items[0].(map[string]any)["over"]), ",") != "pages,bytes" && strings.Join(strs(items[0].(map[string]any)["over"]), ",") != "bytes,pages" {
			t.Fatalf("%s", r.raw)
		}
		// Raising the limit clears it (looked at once, without a new measurement), and
		// crossing again is a new crossing.
		f.root.do("PUT", "/api/admin/settings/quota.pagesPerPerson", map[string]any{"value": 100}).want(t, 200, "")
		if n := len(usageOf(t, f.memA)["notices"].([]any)); n != 1 {
			t.Fatalf("%d notices", n)
		}
		if items = f.admA.do("GET", "/api/admin/usage?over=1", nil).body["items"].([]any); len(items) != 1 ||
			strings.Join(strs(items[0].(map[string]any)["over"]), ",") != "bytes" {
			t.Fatalf("%v", items)
		}
		f.root.do("PUT", "/api/admin/settings/quota.pagesPerPerson", map[string]any{"value": 2}).want(t, 200, "")
		f.a.auth.FlushAudit(context.Background())
		if rows = f.root.audit(t, "quota_exceeded"); len(rows) != 3 {
			t.Fatalf("a second crossing of pages: %d rows", len(rows))
		}
		// A person's own limit is looked at the same way.
		f.root.do("PATCH", "/api/users/"+f.userA, map[string]any{"quota": map[string]any{"pages": 1000, "bytes": 1000000000}}).want(t, 200, "")
		if n := len(usageOf(t, f.memA)["notices"].([]any)); n != 0 {
			t.Fatalf("%d notices", n)
		}
		if items = f.admA.do("GET", "/api/admin/usage?over=1", nil).body["items"].([]any); len(items) != 0 {
			t.Fatalf("%v", items)
		}
	})
}

// The tenant's totals: what it uses is on the Tenant object, its people, bytes
// and pages only warn, its spaces are refused.
func TestTenantUsageAndItsTotals(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		spaceB := f.admB.personalSpace()
		addPages(t, f.admB, spaceB, 2)
		f.measure(t, f.root, spaceB)
		r := f.root.do("GET", "/api/admin/tenants/"+f.tenB, nil).want(t, 200, "")
		us := r.body["usage"].(map[string]any)
		// adm-b and mem-b, the two personal spaces and Space B.
		if us["people"] != float64(2) || us["spaces"] != float64(3) || us["bytes"].(float64) <= 0 || us["pages"] != float64(2) || us["measuredAt"] == nil || len(us["over"].([]any)) != 0 {
			t.Fatalf("%v", us)
		}
		// The tenant's own administrator reads theirs, the list has it for every tenant.
		if f.admB.do("GET", "/api/admin/tenants/"+f.tenB, nil).want(t, 200, "").body["usage"].(map[string]any)["pages"] != float64(2) {
			t.Fatal("a tenant administrator does not see their tenant's usage")
		}
		list := f.root.do("GET", "/api/admin/tenants", nil).want(t, 200, "").body["items"].([]any)
		var seen int
		for _, it := range list {
			m := it.(map[string]any)
			usage := m["usage"].(map[string]any)
			if m["id"] == f.tenB && usage["pages"] == float64(2) {
				seen++
			}
			if m["id"] == store.DefaultTenantID && usage["people"].(float64) < 3 {
				t.Fatalf("%v", usage)
			}
		}
		if seen != 1 {
			t.Fatalf("%v", list)
		}
		// Bytes and pages and people warn (once), for the tenant and for its administrators.
		setTotals(t, f.root, f.tenB, map[string]any{"people": 1, "pages": 1, "bytes": 1})
		a.auth.FlushAudit(context.Background())
		rows := f.root.audit(t, "quota_exceeded")
		kinds := map[string]bool{}
		for _, row := range rows {
			d := row["detail"].(map[string]any)
			if d["scope"] == "tenant" {
				kinds[d["quota"].(string)] = true
			}
		}
		if !kinds["people"] || !kinds["pages"] || !kinds["bytes"] || len(kinds) != 3 {
			t.Fatalf("%v", rows)
		}
		over := f.root.do("GET", "/api/admin/tenants/"+f.tenB, nil).body["usage"].(map[string]any)["over"].([]any)
		if len(over) != 3 {
			t.Fatalf("%v", over)
		}
		// The tenant's people are told in their notices; nobody is refused.
		var tenantNotices int
		for _, n := range usageOf(t, f.memB)["notices"].([]any) {
			if n.(map[string]any)["scope"] == "tenant" {
				tenantNotices++
			}
		}
		if tenantNotices != 3 {
			t.Fatalf("%d notices of the tenant", tenantNotices)
		}
		f.admB.do("POST", "/api/invites", map[string]any{"role": "member"}).want(t, 201, "")
		addPages(t, f.admB, spaceB, 1)
		// Saving the same quota again is not a new crossing.
		setTotals(t, f.root, f.tenB, map[string]any{"people": 1})
		a.auth.FlushAudit(context.Background())
		if n := len(f.root.audit(t, "quota_exceeded")); n != len(rows) {
			t.Fatalf("%d rows, was %d", n, len(rows))
		}
		// A tenant administrator's cannot set the totals, and they are checked.
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/limits", map[string]any{"values": map[string]any{"other.people": 5}}).want(t, 403, "forbidden")
	})
}

// People of another tenant are not in an administrator's list; a site
// administrator can ask for one tenant.
func TestAdminUsageListsPeopleByBytesInTheirScope(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		for _, c := range []*client{f.memA, f.admA} {
			addPages(t, c, c.personalSpace(), 1)
		}
		addPages(t, f.memA, f.memA.personalSpace(), 5)
		f.measure(t, f.root, f.memA.personalSpace())
		f.measure(t, f.root, f.admA.personalSpace())
		f.measure(t, f.root, f.memB.personalSpace())
		r := f.admA.do("GET", "/api/admin/usage", nil).want(t, 200, "")
		raw := r.raw
		for _, id := range f.bIDs() {
			if strings.Contains(raw, id) {
				t.Fatalf("tenant B's %q is in A's list: %s", id, raw)
			}
		}
		items := r.body["items"].([]any)
		if len(items) < 3 {
			t.Fatalf("%s", raw)
		}
		first := items[0].(map[string]any)
		if first["user"].(map[string]any)["id"] != f.userA || first["pages"] != float64(6) || first["limits"].(map[string]any)["spaces"] != float64(20) || first["limits"].(map[string]any)["bytes"] != nil {
			t.Fatalf("%v", first)
		}
		if own, _ := first["own"].(map[string]any); own == nil || own["spaces"] != nil || own["bytes"] != nil || own["pages"] != nil {
			t.Fatalf("a person with no limits of their own: %v", first["own"])
		}
		for i := 1; i < len(items); i++ {
			if items[i].(map[string]any)["bytes"].(float64) > items[i-1].(map[string]any)["bytes"].(float64) {
				t.Fatalf("not by bytes: %s", raw)
			}
		}
		// Pages by cursor, in the same order, with nothing twice.
		var ids []string
		cursor := ""
		for {
			p := f.root.do("GET", "/api/admin/usage?limit=2&cursor="+cursor, nil).want(t, 200, "")
			for _, it := range p.body["items"].([]any) {
				ids = append(ids, it.(map[string]any)["user"].(map[string]any)["id"].(string))
			}
			cursor, _ = p.body["next"].(string)
			if cursor == "" {
				break
			}
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if seen[id] {
				t.Fatalf("%s twice in %v", id, ids)
			}
			seen[id] = true
		}
		if !seen[f.userA] || !seen[f.userB] {
			t.Fatalf("%v", ids)
		}
		// A site administrator narrows to a tenant; a tenant administrator cannot leave theirs.
		for _, it := range f.root.do("GET", "/api/admin/usage?tenant="+f.tenB, nil).want(t, 200, "").body["items"].([]any) {
			if it.(map[string]any)["tenant"] != f.tenB {
				t.Fatalf("%v", it)
			}
		}
		f.admA.do("GET", "/api/admin/usage?tenant="+f.tenB, nil).want(t, 404, "not_found")
		f.root.do("GET", "/api/admin/usage?tenant="+fakeID, nil).want(t, 404, "not_found")
		f.memA.do("GET", "/api/admin/usage", nil).want(t, 403, "forbidden")
		f.root.do("GET", "/api/admin/usage?limit=0", nil).want(t, 400, "invalid")
		f.root.do("GET", "/api/admin/usage?over=yes", nil).want(t, 400, "invalid")
		f.root.do("GET", "/api/admin/usage?cursor=junk", nil).want(t, 400, "invalid")
	})
}

// POST /api/admin/tenants with admin makes an invite that fills in the form that
// accepts it: the username, name and address. Nothing is mailed.
func TestCreatingATenantPrefillsTheAdministratorsInvite(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		root.do("PUT", "/api/admin/tenancy", map[string]any{"enabled": true}).want(t, 200, "")
		r := root.do("POST", "/api/admin/tenants", map[string]any{"slug": "acme", "name": "Acme",
			"admin": map[string]any{"username": " Wu ", "name": "Wu Wei", "email": "wu@acme.example"}}).want(t, 201, "")
		inv := r.body["invite"].(map[string]any)
		if inv["username"] != "wu" || inv["name"] != "Wu Wei" || inv["email"] != "wu@acme.example" || inv["role"] != "admin" || inv["siteAdmin"] != false {
			t.Fatalf("%v", inv)
		}
		// What the page that accepts it starts with.
		visitor := a.newClient("")
		chk := visitor.do("POST", "/api/invites/check", map[string]any{"token": inv["token"]}).want(t, 200, "")
		if chk.body["username"] != "wu" || chk.body["name"] != "Wu Wei" || chk.body["email"] != "wu@acme.example" || chk.body["role"] != "admin" {
			t.Fatalf("%s", chk.raw)
		}
		// Accepting with the password alone makes the account the invite described.
		acc := visitor.do("POST", "/api/invites/accept", map[string]any{"token": inv["token"], "password": pw}).want(t, 201, "")
		u := acc.user()
		if u["username"] != "wu" || u["name"] != "Wu Wei" || u["email"] != "wu@acme.example" || u["emailVerified"] != false || u["role"] != "admin" || u["siteAdmin"] != false {
			t.Fatalf("%v", u)
		}
		if u["tenant"].(map[string]any)["slug"] != "acme" {
			t.Fatalf("%v", u)
		}
		// What the form sends wins over what it was filled in with; the address is the invite's.
		r2 := root.do("POST", "/api/admin/tenants", map[string]any{"slug": "beta", "name": "Beta",
			"admin": map[string]any{"username": "lee", "name": "Lee", "email": "lee@beta.example"}}).want(t, 201, "")
		v2 := a.newClient("")
		u2 := v2.do("POST", "/api/invites/accept", map[string]any{"token": r2.body["invite"].(map[string]any)["token"], "username": "lee2", "name": "Lee Two", "password": pw}).want(t, 201, "").user()
		if u2["username"] != "lee2" || u2["name"] != "Lee Two" || u2["email"] != "lee@beta.example" {
			t.Fatalf("%v", u2)
		}
		// Without admin there is no invite, and an ordinary invite has nothing filled in.
		r3 := root.do("POST", "/api/admin/tenants", map[string]any{"slug": "gamma", "name": "Gamma"}).want(t, 201, "")
		if _, has := r3.body["invite"]; has {
			t.Fatalf("%s", r3.raw)
		}
		plain := root.do("POST", "/api/invites", map[string]any{"role": "member"}).want(t, 201, "")
		c := a.newClient("").do("POST", "/api/invites/check", map[string]any{"token": plain.body["token"]}).want(t, 200, "")
		if c.body["username"] != "" || c.body["name"] != "" || c.body["email"] != "" {
			t.Fatalf("%s", c.raw)
		}
		// Bad values are refused when the tenant is made, and nothing is made.
		root.do("POST", "/api/admin/tenants", map[string]any{"slug": "delta", "name": "Delta", "admin": map[string]any{"username": "x"}}).want(t, 400, "invalid")
		root.do("POST", "/api/admin/tenants", map[string]any{"slug": "delta", "name": "Delta", "admin": map[string]any{"username": "dee", "email": "nope"}}).want(t, 400, "invalid")
		if n := root.do("GET", "/api/admin/tenants", nil).want(t, 200, "").body["items"].([]any); len(n) != 4 {
			t.Fatalf("%d tenants", len(n))
		}
	})
}

// The first administrator's details are checked as the invite checks them, before
// the tenant is made: an address that is not ASCII, an address with nothing after
// the @, or a name with a control character answer 400 and make no tenant.
func TestATenantWithABadAdministratorIsNotMade(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		root.do("PUT", "/api/admin/tenancy", map[string]any{"enabled": true}).want(t, 200, "")
		count := func() int {
			return len(root.do("GET", "/api/admin/tenants", nil).want(t, 200, "").body["items"].([]any))
		}
		before := count()
		for _, admin := range []map[string]any{
			{"username": "wu", "email": "wü@acme.example"},
			{"username": "wu", "email": "wu@"},
			{"username": "wu", "name": "Wu\x07Wei"},
		} {
			root.do("POST", "/api/admin/tenants", map[string]any{"slug": "acme", "name": "Acme", "admin": admin}).want(t, 400, "") // invalid, or email_not_ascii
			if n := count(); n != before {
				t.Fatalf("%v: %d tenants, want %d", admin, n, before)
			}
		}
	})
}

// admin without a username: the contract fills each field with "" when it is not
// given, so the invite is made with no username and the person picks one when
// they accept it.
func TestATenantsAdministratorMayPickTheUsernameAtAccept(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		root.do("PUT", "/api/admin/tenancy", map[string]any{"enabled": true}).want(t, 200, "")
		r := root.do("POST", "/api/admin/tenants", map[string]any{"slug": "acme", "name": "Acme",
			"admin": map[string]any{"email": "wu@acme.example"}}).want(t, 201, "")
		inv := r.body["invite"].(map[string]any)
		if inv["username"] != "" || inv["email"] != "wu@acme.example" || inv["role"] != "admin" {
			t.Fatalf("%v", inv)
		}
		visitor := a.newClient("")
		visitor.do("POST", "/api/invites/accept", map[string]any{"token": inv["token"], "password": pw}).want(t, 400, "invalid")
		u := visitor.do("POST", "/api/invites/accept", map[string]any{"token": inv["token"], "username": "wu", "name": "Wu", "password": pw}).want(t, 201, "").user()
		if u["username"] != "wu" || u["role"] != "admin" || u["tenant"].(map[string]any)["slug"] != "acme" {
			t.Fatalf("%v", u)
		}
	})
}

// A person's own limit answers to a lock or a restriction put on the setting
// later, by the rule of the layer that set it: a site administrator's to the
// site's, a tenant administrator's to the stricter of the site's and the
// tenant's. Under a lock it is not in effect; under a restriction it counts up
// to the restriction's max. Either way it is kept, shown with inEffect false.
func TestAPersonsOwnLimitAnswersToALaterLockOrRestriction(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		mk := func(name string) result { return f.memB.do("POST", "/api/spaces", map[string]any{"name": name}) }
		row := func() map[string]any {
			for _, it := range f.root.do("GET", "/api/admin/usage?tenant="+f.tenB, nil).want(t, 200, "").body["items"].([]any) {
				if m := it.(map[string]any); m["user"].(map[string]any)["id"] == f.userB {
					return m
				}
			}
			t.Fatal("no row for the person")
			return nil
		}
		inEffect := func(m map[string]any, k string) any { return m["ownInEffect"].(map[string]any)[k] }
		// The site's administrator gives the person 15; they make two more spaces.
		f.root.do("PATCH", "/api/users/"+f.userB, map[string]any{"quota": map[string]any{"spaces": 15}}).want(t, 200, "")
		mk("B2").want(t, 201, "")
		mk("B3").want(t, 201, "")
		if m := row(); m["limits"].(map[string]any)["spaces"] != float64(15) || inEffect(m, "spaces") != true {
			t.Fatalf("%v", m)
		}
		// The site then locks the setting at 2: the own 15 is kept but not in effect.
		f.root.do("PUT", "/api/admin/settings/quota.spacesPerPerson", map[string]any{"value": 2, "lower": map[string]any{"mode": "lock"}}).want(t, 200, "")
		if u := usageOf(t, f.memB); limitOf(u, "spaces") != float64(2) {
			t.Fatalf("%v", u)
		}
		if r := mk("B4").want(t, 403, "quota_exceeded"); r.body["limit"] != float64(2) {
			t.Fatalf("%s", r.raw)
		}
		if m := row(); m["limits"].(map[string]any)["spaces"] != float64(2) || m["own"].(map[string]any)["spaces"] != float64(15) || inEffect(m, "spaces") != false {
			t.Fatalf("%v", m)
		}
		// A restriction at 5 instead: the own 15 counts up to 5.
		f.root.do("PUT", "/api/admin/settings/quota.spacesPerPerson", map[string]any{"lower": map[string]any{"mode": "restrict", "max": 5}}).want(t, 200, "")
		if m := row(); m["limits"].(map[string]any)["spaces"] != float64(5) || inEffect(m, "spaces") != false {
			t.Fatalf("%v", m)
		}
		mk("B4").want(t, 201, "")
		mk("B5").want(t, 201, "")
		mk("B6").want(t, 403, "quota_exceeded")
		// The rule lifted, the own 15 is in effect again.
		f.root.do("PUT", "/api/admin/settings/quota.spacesPerPerson", map[string]any{"lower": map[string]any{"mode": "allow"}}).want(t, 200, "")
		if m := row(); m["limits"].(map[string]any)["spaces"] != float64(15) || inEffect(m, "spaces") != true {
			t.Fatalf("%v", m)
		}
		// The tenant's administrator sets the person's pages; the tenant then locks
		// the setting for its people: that value is not in effect, the site's is.
		// The site administrator's 15 spaces do not answer to the tenant's lock.
		f.admB.do("PATCH", "/api/users/"+f.userB, map[string]any{"quota": map[string]any{"pages": 100}}).want(t, 200, "")
		if m := row(); m["limits"].(map[string]any)["pages"] != float64(100) || inEffect(m, "pages") != true {
			t.Fatalf("%v", m)
		}
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/quota.pagesPerPerson", map[string]any{"lower": map[string]any{"mode": "lock"}}).want(t, 200, "")
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/quota.spacesPerPerson", map[string]any{"lower": map[string]any{"mode": "lock"}}).want(t, 200, "")
		m := row()
		if m["limits"].(map[string]any)["pages"] != nil || inEffect(m, "pages") != false || m["own"].(map[string]any)["pages"] != float64(100) {
			t.Fatalf("%v", m)
		}
		if m["limits"].(map[string]any)["spaces"] != float64(15) || inEffect(m, "spaces") != true {
			t.Fatalf("%v", m)
		}
	})
}

// A transfer, a change of owners and a new person change whom usage is counted
// for at once, so whether anyone is over is looked at then, not at the next
// measurement or recount: the new owner's notices, the old owner's, the tenant's
// people and the audit log are right straight away.
func TestOverFollowsTransfersOwnersAndNewPeopleAtOnce(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		admB := mustUserID(t, a, "adm-b")
		notices := func(c *client, scope string) []string {
			var out []string
			for _, n := range usageOf(t, c)["notices"].([]any) {
				if m := n.(map[string]any); m["scope"] == scope {
					out = append(out, m["quota"].(string))
				}
			}
			return out
		}
		audited := func(user, quota string) int {
			a.auth.FlushAudit(context.Background())
			n := 0
			for _, row := range f.root.audit(t, "quota_exceeded") {
				d := row["detail"].(map[string]any)
				if d["quota"] == quota && (user == "" && d["scope"] == "tenant" || row["user"] == user) {
					n++
				}
			}
			return n
		}
		f.root.do("PUT", "/api/admin/settings/quota.pagesPerPerson", map[string]any{"value": 3}).want(t, 200, "")
		addPages(t, f.admB, f.spaceB, 4)
		f.measure(t, f.root, f.spaceB)
		if got := notices(f.admB, "person"); len(got) != 1 || got[0] != "pages" {
			t.Fatalf("%v", got)
		}
		// The space is handed to mem-b: the pages are theirs now, and they are over.
		f.root.do("POST", "/api/admin/spaces/"+f.spaceB+"/transfer", map[string]any{"userId": f.userB}).want(t, 204, "")
		if got := notices(f.memB, "person"); len(got) != 1 || got[0] != "pages" {
			t.Fatalf("the new owner: %v", got)
		}
		if got := notices(f.admB, "person"); len(got) != 0 {
			t.Fatalf("the old owner: %v", got)
		}
		if audited(f.userB, "pages") != 1 {
			t.Fatal("the new owner's crossing is not in the audit log")
		}
		// mem-b makes adm-b (an editor since the transfer) an owner again and
		// leaves: adm-b is the one owner, and the space moves back.
		f.memB.do("PATCH", "/api/spaces/"+f.spaceB+"/members/"+admB, map[string]any{"role": "owner"}).want(t, 200, "")
		f.memB.do("DELETE", "/api/spaces/"+f.spaceB+"/members/"+f.userB, nil).want(t, 204, "")
		if got := notices(f.memB, "person"); len(got) != 0 {
			t.Fatalf("the old owner: %v", got)
		}
		if got := notices(f.admB, "person"); len(got) != 1 || got[0] != "pages" {
			t.Fatalf("the new owner: %v", got)
		}
		if audited(admB, "pages") != 2 {
			t.Fatal("the second crossing of adm-b is not in the audit log")
		}
		// The tenant may have 2 people; a third arrives and the tenant is over at once.
		setTotals(t, f.root, f.tenB, map[string]any{"people": 2})
		if got := notices(f.memB, "tenant"); len(got) != 0 {
			t.Fatalf("%v", got)
		}
		inv := f.admB.do("POST", "/api/invites", map[string]any{"role": "member"}).want(t, 201, "")
		a.newClient("").do("POST", "/api/invites/accept", map[string]any{"token": inv.body["token"], "username": "third-b", "name": "Third", "password": pw}).want(t, 201, "")
		if got := notices(f.memB, "tenant"); len(got) != 1 || got[0] != "people" {
			t.Fatalf("the tenant's people: %v", got)
		}
		if audited("", "people") != 1 {
			t.Fatal("the tenant's crossing is not in the audit log")
		}
	})
}
