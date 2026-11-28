package api_test

import (
	"context"
	"strconv"
	"testing"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/store"
)

// limitFx is two tenants that each have a person with a space and a token, and
// a site administrator who sets the limits.
type limitFx struct {
	a            *acc
	root         *client
	tenA, tenB   string
	tokA, tokB   *client // bearer clients
	cookA        *client // the web session of tenant A's person
	sidA, sidB   string
	pagesA, pagB string
}

func newLimitFx(t *testing.T, a *acc) *limitFx {
	t.Helper()
	f := &limitFx{a: a, root: a.setup("root", pw), tenA: store.DefaultTenantID}
	f.root.putSetting("features.tenancy", true).want(t, 200, "")
	var admB *client
	f.tenB, admB = auditTenant(t, a, f.root, "globex")
	mem := a.addUser("mem", pw, "member")
	f.cookA = mem
	f.sidA = mem.do("POST", "/api/spaces", map[string]any{"name": "A"}).want(t, 201, "").body["id"].(string)
	f.sidB = admB.do("POST", "/api/spaces", map[string]any{"name": "B"}).want(t, 201, "").body["id"].(string)
	_, sa := mkToken(t, mem, "read", nil, f.sidA)
	_, sb := mkToken(t, admB, "read", nil, f.sidB)
	f.tokA, f.tokB = a.bearer(sa, ""), a.bearer(sb, "")
	f.pagesA, f.pagB = spacePath(f.sidA)+"/pages", spacePath(f.sidB)+"/pages"
	return f
}

func (f *limitFx) rule(t *testing.T, tenant, key string, value any) {
	t.Helper()
	rule := any(nil)
	if value != nil {
		rule = map[string]any{"value": value}
	}
	f.root.do("PUT", "/api/admin/tenants/"+tenant+"/settings/"+key, map[string]any{"siteRule": rule}).want(t, 200, "")
}

func dayRows(t *testing.T, a *acc, tenant string) map[string]int64 {
	t.Helper()
	rows, err := store.ListUsageDaily(context.Background(), a.st.Q(), tenant, "2000-01-01", "2100-01-01")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, r := range rows {
		out[r.Key] += r.Value
	}
	return out
}

// Over rate.apiPerMinute a token's request is 429 with Retry-After, for that
// tenant alone; with no limit nothing is refused; the web app's own requests
// (a cookie) are not held to it.
func TestTokenRequestsAreHeldToTheTenantsLimit(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newLimitFx(t, a)
		for range 8 { // no limit: nothing is refused
			f.tokA.do("GET", f.pagesA, nil).want(t, 200, "")
		}
		f.rule(t, f.tenA, "rate.apiPerMinute", 5)
		a.clock.Advance(61e9) // a new minute
		for i := range 5 {
			if r := f.tokA.do("GET", f.pagesA, nil); r.code != 200 {
				t.Fatalf("request %d: %d %s", i+1, r.code, r.raw)
			}
		}
		r := f.tokA.do("GET", f.pagesA, nil).want(t, 429, "rate_limited")
		if n, err := strconv.Atoi(r.resp.Header.Get("Retry-After")); err != nil || n < 1 || n > 60 {
			t.Fatalf("Retry-After %q", r.resp.Header.Get("Retry-After"))
		}
		// Another tenant's flood is not this one's, and this one's is not another's.
		for range 20 {
			f.tokB.do("GET", f.pagB, nil).want(t, 200, "")
		}
		f.tokA.do("GET", f.pagesA, nil).want(t, 429, "rate_limited")
		// The web app's requests of the same tenant are not limited.
		for range 10 {
			f.cookA.do("GET", f.pagesA, nil).want(t, 200, "")
		}
		// The window ends: allowed again.
		a.clock.Advance(61e9)
		f.tokA.do("GET", f.pagesA, nil).want(t, 200, "")
		// A rule for the other tenant limits it, and only it.
		f.rule(t, f.tenB, "rate.apiPerMinute", 1)
		a.clock.Advance(61e9)
		f.tokB.do("GET", f.pagB, nil).want(t, 200, "")
		f.tokB.do("GET", f.pagB, nil).want(t, 429, "rate_limited")
		f.tokA.do("GET", f.pagesA, nil).want(t, 200, "")
		// 0 allows nothing; lifting the rule lifts the limit.
		f.rule(t, f.tenB, "rate.apiPerMinute", nil)
		f.tokB.do("GET", f.pagB, nil).want(t, 200, "")
	})
}

// rate.mcpPerMinute holds /mcp, and is its own count: API requests do not
// spend it.
func TestMCPRequestsAreHeldToTheTenantsLimit(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		f := newLimitFx(t, a)
		f.rule(t, f.tenA, "rate.mcpPerMinute", 3)
		for range 10 { // the API is not limited by it
			f.tokA.do("GET", f.pagesA, nil).want(t, 200, "")
		}
		for i := range 3 {
			if r := f.tokA.rpc("tools/list", nil); r.code != 200 {
				t.Fatalf("call %d: %d %s", i+1, r.code, r.raw)
			}
		}
		r := f.tokA.rpc("tools/list", nil).want(t, 429, "rate_limited")
		if r.resp.Header.Get("Retry-After") == "" {
			t.Fatal("no Retry-After")
		}
		for range 6 {
			if r := f.tokB.rpc("tools/list", nil); r.code != 200 {
				t.Fatalf("another tenant: %d %s", r.code, r.raw)
			}
		}
	})
}

// A request costs the database nothing for the limit or the count: the
// settings come from the cache and the counters are written in a batch.
func TestRequestsCostNoQueryForLimitsOrUsage(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		ctx := context.Background()
		f := newLimitFx(t, a)
		f.rule(t, f.tenA, "rate.apiPerMinute", 100000)
		f.rule(t, f.tenA, "rate.mcpPerMinute", 100000)
		f.tokA.do("GET", f.pagesA, nil).want(t, 200, "")
		f.tokA.rpc("tools/list", nil).want(t, 200, "")
		if err := a.h.FlushUsage(ctx); err != nil {
			t.Fatal(err)
		}
		base := dayRows(t, a, f.tenA)
		computes := auth.SettingsComputes(a.auth)
		for range 40 {
			f.tokA.do("GET", f.pagesA, nil).want(t, 200, "")
			f.tokA.rpc("tools/list", nil).want(t, 200, "")
		}
		if n := computes.Load(); n != 0 {
			t.Fatalf("%d reads of the settings from the database in 80 requests", n)
		}
		// Nothing was written for them: the rows are as they were.
		now := dayRows(t, a, f.tenA)
		if now[store.UsageKeyAPIRequests] != base[store.UsageKeyAPIRequests] || now[store.UsageKeyMCPRequests] != base[store.UsageKeyMCPRequests] {
			t.Fatalf("usage rows written per request: %v -> %v", base, now)
		}
		// The flush writes exactly what was counted.
		if err := a.h.FlushUsage(ctx); err != nil {
			t.Fatal(err)
		}
		after := dayRows(t, a, f.tenA)
		if got := after[store.UsageKeyAPIRequests] - base[store.UsageKeyAPIRequests]; got != 40 {
			t.Fatalf("api requests counted %d, want 40", got)
		}
		if got := after[store.UsageKeyMCPRequests] - base[store.UsageKeyMCPRequests]; got != 40 {
			t.Fatalf("mcp requests counted %d, want 40", got)
		}
		if dayRows(t, a, f.tenB)[store.UsageKeyAPIRequests] != 0 {
			t.Fatal("another tenant was counted")
		}
	})
}

// rate.mailPerDay holds the site's mailer for a tenant: over it the message is
// refused (429 with Retry-After), what was sent is counted as mail, and the
// next day starts again. The count survives a flush: the sum is what is
// written plus what is in memory.
func TestAMailLimitPerDayHoldsTheSiteMailerForATenant(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		ctx := context.Background()
		srv := fakeMail(t)
		f := newLimitFx(t, a)
		enableMail(t, f.root, srv)
		f.rule(t, f.tenA, "rate.mailPerDay", 2)
		c := f.cookA
		c.do("PATCH", "/api/account", map[string]any{"email": "mem@example.test"}).want(t, 200, "")
		verify := func(code int) result {
			t.Helper()
			r := c.do("POST", "/api/account/email/verify", map[string]any{})
			if r.code != code {
				t.Fatalf("want %d, got %d %s", code, r.code, r.raw)
			}
			return r
		}
		verify(202)
		if err := a.h.FlushUsage(ctx); err != nil { // one written, one in memory
			t.Fatal(err)
		}
		verify(202)
		r := verify(429)
		if r.body["error"] != "rate_limited" || r.resp.Header.Get("Retry-After") == "" {
			t.Fatalf("%s", r.raw)
		}
		if err := a.h.FlushUsage(ctx); err != nil {
			t.Fatal(err)
		}
		if got := dayRows(t, a, f.tenA)[store.UsageKeyMail]; got != 2 {
			t.Fatalf("mail counted %d, want 2 (a refused message is not counted)", got)
		}
		if n := len(srv.Wait(2)); n != 2 {
			t.Fatalf("%d messages sent", n)
		}
		// The crossing is audited once.
		if got := auditKinds(t, f.root, "quota_exceeded"); len(got) != 1 {
			t.Fatalf("quota_exceeded rows: %d", len(got))
		}
		a.clock.Advance(25 * 3600e9) // the next day, and the address's hour is over
		verify(202)
	})
}
