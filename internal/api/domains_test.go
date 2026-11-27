package api_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
)

// The email domains tenants claim and verify by DNS (3.6 d2). The DNS is the
// fixture's fake (a.dns) and the clock its own; the daily recheck is run by
// the tests (a.domains.RecheckDue), never by a timer.

// domFx is two tenants besides the default one, each with an administrator.
type domFx struct {
	a                *acc
	root, admB, admC *client
	tenB, tenC       string
}

func newDomFx(t *testing.T, a *acc) *domFx {
	t.Helper()
	f := &domFx{a: a, root: a.setup("root", pw)}
	f.root.putSetting("features.tenancy", true).want(t, 200, "")
	// The tenants' administrators read their audit (the default for new tenants is off).
	f.root.putSetting("tenants.newAuditDefault", true).want(t, 200, "")
	mk := func(slug, name, admin string) (string, *client) {
		r := f.root.do("POST", "/api/admin/tenants", map[string]any{"slug": slug, "name": name, "admin": map[string]any{"username": admin}}).want(t, 201, "")
		c := a.newClient("")
		c.do("POST", "/api/invites/accept", map[string]any{"token": r.body["invite"].(map[string]any)["token"], "username": admin, "name": admin, "password": pw}).want(t, 201, "")
		return r.body["id"].(string), c
	}
	f.tenB, f.admB = mk("acme", "Acme", "adm-b")
	f.tenC, f.admC = mk("beta", "Beta Corp", "adm-c")
	return f
}

func domainsPath(tenant string) string { return "/api/admin/tenants/" + tenant + "/domains" }

func fmtAny(v any) string { return fmt.Sprint(v) }

// claim claims a domain for the tenant and returns its key and code.
func claim(t *testing.T, c *client, tenant, domain string) (key, code string) {
	t.Helper()
	r := c.do("POST", domainsPath(tenant), map[string]any{"domain": domain}).want(t, 201, "")
	recs := r.body["records"].(map[string]any)
	txt := recs["txt"].(map[string]any)
	code = r.body["code"].(string)
	if txt["name"] != "_petalbook."+r.body["domain"].(string) || txt["value"] != "petalbook-verify="+code {
		t.Fatalf("the TXT record: %s", r.raw)
	}
	if cn := recs["cname"].(map[string]any); cn["target"] != code+".verify.notes.example.test" {
		t.Fatalf("the CNAME record: %s", r.raw)
	}
	return r.body["domain"].(string), code
}

func lastCheck(r result) map[string]any { return r.body["lastCheck"].(map[string]any) }

func checkNow(c *client, tenant, key string) result {
	return c.do("POST", domainsPath(tenant)+"/"+key+"/verify", nil)
}

func TestADomainIsVerifiedByItsTXTOrCNAMERecord(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newDomFx(t, a)
		key, code := claim(t, f.admB, f.tenB, "Bücher.Example.")
		if key != "xn--bcher-kva.example" {
			t.Fatalf("the key is the IDNA form in punycode, lower case: %s", key)
		}
		list := f.admB.do("GET", domainsPath(f.tenB), nil).want(t, 200, "")
		d := list.body["domains"].([]any)[0].(map[string]any)
		if d["display"] != "bücher.example" || d["state"] != "unverified" || d["records"] != nil || d["code"] != nil || list.body["siteHost"] != "notes.example.test" {
			t.Fatalf("the list shows no code, and the domain as people read it: %s", list.raw)
		}
		name := "_petalbook." + key
		step := func() { a.clock.Advance(61 * time.Second) }

		// Not there.
		if r := checkNow(f.admB, f.tenB, key).want(t, 200, ""); lastCheck(r)["result"] != "missing" || r.body["state"] != "unverified" || r.body["nextCheckAllowedAt"] == nil {
			t.Fatal(r.raw)
		}
		// A value that is not the code: it is shown.
		step()
		a.dns.SetTXT(name, "petalbook-verify=somethingelse", "v=spf1 -all")
		r := checkNow(f.admB, f.tenB, key).want(t, 200, "")
		if c := lastCheck(r); c["result"] != "wrong_value" || len(c["seen"].([]any)) != 1 || c["seen"].([]any)[0] != "petalbook-verify=somethingelse" {
			t.Fatal(r.raw)
		}
		// DNS cannot be asked: an error, not "missing".
		step()
		a.dns.Fail(name, errors.New("server failure"))
		r = checkNow(f.admB, f.tenB, key).want(t, 200, "")
		if c := lastCheck(r); c["result"] != "dns_error" || c["error"] == "" || r.body["state"] != "unverified" {
			t.Fatal(r.raw)
		}
		a.dns.Fail(name, nil)
		// The CNAME to <code>.verify.<the site's host> is found.
		step()
		a.dns.SetTXT(name)
		a.dns.SetCNAME(name, code+".verify.notes.example.test.")
		r = checkNow(f.admB, f.tenB, key).want(t, 200, "")
		if c := lastCheck(r); c["result"] != "found" || c["via"] != "cname" || c["by"] != "manual" || r.body["state"] != "verified" {
			t.Fatal(r.raw)
		}
		if got := a.domains.OfTenant(context.Background(), f.tenB); len(got) != 1 || got[0] != key {
			t.Fatalf("the map that backs OrgDomains: %v", got)
		}
		// A TXT of the code is found too (another claim of B's).
		key2, code2 := claim(t, f.admB, f.tenB, "acme.example")
		a.dns.SetTXT("_petalbook."+key2, "petalbook-verify="+code2)
		if r := checkNow(f.admB, f.tenB, key2).want(t, 200, ""); lastCheck(r)["via"] != "txt" || r.body["state"] != "verified" {
			t.Fatal(r.raw)
		}
		a.auth.FlushAudit(context.Background())
		if n := len(f.admB.audit(t, "domain_checked")); n != 5 {
			t.Fatalf("every check by hand is audited: %d", n)
		}
		if n := len(f.admB.audit(t, "domain_verified")); n != 2 {
			t.Fatalf("domain_verified: %d", n)
		}
		// C sees none of it, and cannot reach B's.
		if l := f.admC.do("GET", domainsPath(f.tenC), nil).want(t, 200, ""); len(l.body["domains"].([]any)) != 0 {
			t.Fatal(l.raw)
		}
		checkNow(f.admC, f.tenB, key).want(t, 404, "not_found")
		// Removing a verified one takes it out of the map.
		f.admB.do("DELETE", domainsPath(f.tenB)+"/"+key2, nil).want(t, 204, "")
		if got := a.domains.OfTenant(context.Background(), f.tenB); len(got) != 1 {
			t.Fatalf("removed from the map: %v", got)
		}
	})
}

func TestTwoTenantsVerifyingOneDomainEndWithOneWinner(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newDomFx(t, a)
		key, codeB := claim(t, f.admB, f.tenB, "shared.example")
		_, codeC := claim(t, f.admC, f.tenC, "shared.example")
		a.dns.SetTXT("_petalbook."+key, "petalbook-verify="+codeB, "petalbook-verify="+codeC)
		// Both ask at once. The minute of a domain is one for both tenants, so
		// one of them may be told to wait instead; the race of the writes
		// themselves (two servers) is TestTwoVerificationsOfOneDomainRaceToOneWinner.
		var wg sync.WaitGroup
		res := make([]result, 2)
		for i, c := range []struct {
			cl *client
			t  string
		}{{f.admB, f.tenB}, {f.admC, f.tenC}} {
			wg.Add(1)
			go func() { defer wg.Done(); res[i] = checkNow(c.cl, c.t, key) }()
		}
		wg.Wait()
		won, lost := 0, 0
		for _, r := range res {
			switch {
			case r.code == 200 && r.body["state"] == "verified":
				won++
			case r.code == 409 && r.body["error"] == "domain_claimed", r.code == 429 && r.body["error"] == "rate_limited":
				lost++
				if strings.Contains(r.raw, "Acme") || strings.Contains(r.raw, "acme") || strings.Contains(r.raw, "Beta") || strings.Contains(r.raw, "beta") {
					t.Fatalf("the refusal names a tenant: %s", r.raw)
				}
			default:
				t.Fatalf("%d %s", r.code, r.raw)
			}
		}
		if won != 1 || lost != 1 {
			t.Fatalf("one verifies, one is refused: %d, %d", won, lost)
		}
		if n := rowCount(t, a, `SELECT COUNT(*) FROM tenant_domains WHERE domain = ? AND verified_at > 0`, key); n != 1 {
			t.Fatalf("verified rows: %d", n)
		}
		// The loser keeps its claim, unverified; a later try is refused the same way.
		loser, lc := f.admC, f.tenC
		if res[0].code != 200 {
			loser, lc = f.admB, f.tenB
		}
		a.clock.Advance(61 * time.Second)
		checkNow(loser, lc, key).want(t, 409, "domain_claimed")
	})
}

func TestAVerificationCodeExpiresAfterSevenDays(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newDomFx(t, a)
		key, code := claim(t, f.admB, f.tenB, "late.example")
		a.dns.SetTXT("_petalbook."+key, "petalbook-verify="+code)
		a.clock.Advance(7*24*time.Hour + time.Second)
		checkNow(f.admB, f.tenB, key).want(t, 409, "domain_code_expired")
		if a.dns.Asked("_petalbook."+key) != 0 {
			t.Fatal("an expired code is refused before DNS is asked")
		}
		if l := f.admB.do("GET", domainsPath(f.tenB), nil).want(t, 200, ""); l.body["domains"].([]any)[0].(map[string]any)["codeExpired"] != true {
			t.Fatal(l.raw)
		}
		// A new code: the old record no longer counts, the new one does.
		r := f.admB.do("POST", domainsPath(f.tenB)+"/"+key+"/code", nil).want(t, 200, "")
		code2 := r.body["code"].(string)
		if code2 == code || r.body["codeExpired"] != false {
			t.Fatal(r.raw)
		}
		if c := lastCheck(checkNow(f.admB, f.tenB, key).want(t, 200, "")); c["result"] != "wrong_value" {
			t.Fatal(c)
		}
		a.dns.SetTXT("_petalbook."+key, "petalbook-verify="+code2)
		a.clock.Advance(61 * time.Second)
		checkNow(f.admB, f.tenB, key).want(t, 200, "")
		// A verified domain keeps its record: no new code.
		f.admB.do("POST", domainsPath(f.tenB)+"/"+key+"/code", nil).want(t, 409, "domain_verified")
	})
}

func TestAVerifiedDomainLapsesAfterSevenDaysMissing(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		ctx := context.Background()
		f := newDomFx(t, a)
		key, code := claim(t, f.admB, f.tenB, "gone.example")
		name := "_petalbook." + key
		a.dns.SetTXT(name, "petalbook-verify="+code)
		checkNow(f.admB, f.tenB, key).want(t, 200, "")
		state := func() map[string]any {
			return f.admB.do("GET", domainsPath(f.tenB), nil).want(t, 200, "").body["domains"].([]any)[0].(map[string]any)
		}
		day := func() { a.clock.Advance(24 * time.Hour); a.domains.RecheckDue(ctx) }
		day()
		if d := state(); d["state"] != "verified" || d["lastCheck"].(map[string]any)["result"] != "found" || d["lastCheck"].(map[string]any)["by"] != "daily" || d["nextCheckAllowedAt"] != nil {
			t.Fatal(d)
		}
		// The record goes: missing since the first recheck that does not find it.
		a.dns.SetTXT(name)
		day()
		missingSince := state()["missingSince"]
		if d := state(); d["state"] != "missing" || missingSince == nil {
			t.Fatal(d)
		}
		// A DNS error counts for nothing, and is tried again within the hour.
		a.dns.Fail(name, errors.New("timeout"))
		day()
		if d := state(); d["state"] != "missing" || d["missingSince"] != missingSince || d["lastCheck"].(map[string]any)["result"] != "dns_error" {
			t.Fatal(d)
		}
		a.dns.Fail(name, nil)
		for range 5 {
			day()
			if d := state(); d["state"] != "missing" {
				t.Fatalf("lapsed early: %v", d)
			}
		}
		// Seven days after it went missing, the claim lapses: noticed and audited.
		day()
		if d := state(); d["state"] != "lapsed" || d["verifiedAt"] != nil {
			t.Fatal(d)
		}
		if got := a.domains.OfTenant(ctx, f.tenB); len(got) != 0 {
			t.Fatalf("a lapsed domain stays in the map: %v", got)
		}
		if n := f.admB.do("GET", "/api/notifications", nil).want(t, 200, "").raw; !strings.Contains(n, "domain_lapsed") || !strings.Contains(n, "gone.example") {
			t.Fatalf("the tenant's administrator is told: %s", n)
		}
		if rows := f.admB.audit(t, "domain_lapsed"); len(rows) != 1 {
			t.Fatalf("domain_lapsed: %v", rows)
		}
		if rows := f.admB.audit(t, "domain_missing"); len(rows) != 1 {
			t.Fatalf("domain_missing is written once, when it goes missing: %v", rows)
		}
	})
}

func TestCheckNowIsLimitedPerDomainPerTenantAndOnTheSite(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newDomFx(t, a)
		key, _ := claim(t, f.admB, f.tenB, "one.example")
		// Once a minute per domain.
		checkNow(f.admB, f.tenB, key).want(t, 200, "")
		r := checkNow(f.admB, f.tenB, key).want(t, 429, "rate_limited")
		if ra := r.resp.Header.Get("Retry-After"); ra != "60" {
			t.Fatalf("Retry-After: %q", ra)
		}
		a.clock.Advance(30 * time.Second)
		if ra := checkNow(f.admB, f.tenB, key).want(t, 429, "rate_limited").resp.Header.Get("Retry-After"); ra != "30" {
			t.Fatalf("Retry-After counts down: %q", ra)
		}
		// 20 an hour per tenant (the refusal above spent none of them).
		for i := 1; i < 20; i++ {
			a.clock.Advance(61 * time.Second)
			checkNow(f.admB, f.tenB, key).want(t, 200, "")
		}
		a.clock.Advance(61 * time.Second)
		r = checkNow(f.admB, f.tenB, key).want(t, 429, "rate_limited")
		if ra := r.resp.Header.Get("Retry-After"); ra == "" || ra == "60" || ra == "1" {
			t.Fatalf("the hour's Retry-After: %q", ra)
		}
		// C has its own hour; the site's minute is domains.checksPerMinute.
		f.root.putSetting("domains.checksPerMinute", 2).want(t, 200, "")
		a.clock.Advance(time.Hour)
		k1, _ := claim(t, f.admC, f.tenC, "c1.example")
		k2, _ := claim(t, f.admC, f.tenC, "c2.example")
		k3, _ := claim(t, f.admC, f.tenC, "c3.example")
		checkNow(f.admC, f.tenC, k1).want(t, 200, "")
		checkNow(f.admB, f.tenB, key).want(t, 200, "")
		checkNow(f.admC, f.tenC, k2).want(t, 429, "rate_limited")
		a.clock.Advance(61 * time.Second)
		checkNow(f.admC, f.tenC, k2).want(t, 200, "")
		// The checks count against the tenant's outbound share (rate.outboundPerMinute).
		f.root.putSetting("domains.checksPerMinute", 60).want(t, 200, "")
		f.root.do("PUT", "/api/admin/tenants/"+f.tenC+"/settings/rate.outboundPerMinute", map[string]any{"siteRule": map[string]any{"value": 1}}).want(t, 200, "")
		a.clock.Advance(61 * time.Second)
		checkNow(f.admC, f.tenC, k1).want(t, 200, "")
		checkNow(f.admC, f.tenC, k3).want(t, 429, "rate_limited")
	})
}

func TestDomainsAreNormalisedAndPublicOnesRefused(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newDomFx(t, a)
		for _, d := range []string{"gmail.com", "GMAIL.COM.", "qq.com", "Outlook.com"} {
			f.admB.do("POST", domainsPath(f.tenB), map[string]any{"domain": d}).want(t, 400, "domain_public")
		}
		for _, d := range []string{"", "localhost", "a@b.example", "1.2.3.4", "-bad.example", "exa mple.com", "https://x.example/", "xn--zz.example"} {
			f.admB.do("POST", domainsPath(f.tenB), map[string]any{"domain": d}).want(t, 400, "invalid")
		}
		// Two spellings of one domain are one claim.
		key, _ := claim(t, f.admB, f.tenB, "ÉCOLE.example")
		if key != "xn--cole-9oa.example" {
			t.Fatal(key)
		}
		f.admB.do("POST", domainsPath(f.tenB), map[string]any{"domain": "xn--cole-9oa.EXAMPLE"}).want(t, 409, "domain_exists")
		f.admB.do("POST", domainsPath(f.tenB), map[string]any{"domain": "école.example."}).want(t, 409, "domain_exists")
		// The path takes either spelling.
		checkNow(f.admB, f.tenB, "école.example").want(t, 200, "")
		// Another tenant may claim it too: the claim says nothing of others.
		claim(t, f.admC, f.tenC, "école.example")
	})
}

func TestARestrictedDomainRefusesNewOrganisationsOnIt(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root, _ := orgSetup(t, a)
		r := root.do("POST", "/api/admin/tenants", map[string]any{"slug": "acme", "name": "Acme"}).want(t, 201, "")
		tenB := r.body["id"].(string)
		key, code := claim(t, root, tenB, "acme.example")
		a.dns.SetTXT("_petalbook."+key, "petalbook-verify="+code)
		anon := a.newClient("")
		body := map[string]any{"email": "ann@acme.example", "username": "ann"}
		// Not verified, or not restricted: nothing is held.
		root.do("PATCH", domainsPath(tenB)+"/"+key, map[string]any{"restrictOrgs": true}).want(t, 200, "")
		regOrg(anon, "Ann's", "anns", body).want(t, 202, "")
		checkNow(root, tenB, key).want(t, 200, "")
		root.do("PATCH", domainsPath(tenB)+"/"+key, map[string]any{"restrictOrgs": false}).want(t, 200, "")
		regOrg(anon, "Ann's 2", "anns2", map[string]any{"email": "bo@acme.example", "username": "bo"}).want(t, 202, "")
		// Verified and restricted: 409 domain_managed, naming the organisation.
		root.do("PATCH", domainsPath(tenB)+"/"+key, map[string]any{"restrictOrgs": true}).want(t, 200, "")
		got := regOrg(anon, "Cy's", "cys", map[string]any{"email": "cy@ACME.example", "username": "cy"}).want(t, 409, "domain_managed")
		if o := got.body["organization"].(map[string]any); o["slug"] != "acme" || o["name"] != "Acme" {
			t.Fatal(got.raw)
		}
		// Another domain, and joining a tenant, are not held.
		regOrg(anon, "Dee's", "dees", map[string]any{"email": "dee@other.example", "username": "dee"}).want(t, 202, "")
		root.do("PATCH", domainsPath(tenB)+"/"+key, map[string]any{"joinMode": "x"}).want(t, 400, "invalid")
		// With the feature off for the tenant it has no effect (and is kept).
		root.do("PUT", "/api/admin/tenants/"+tenB+"/settings/features.domains", map[string]any{"siteRule": map[string]any{"value": false}}).want(t, 409, "confirm_required")
		tok := root.do("POST", "/api/admin/confirm", map[string]any{"action": "change_capabilities", "key": "features.domains", "tenant": tenB}).want(t, 200, "").body["confirm"]
		root.do("PUT", "/api/admin/tenants/"+tenB+"/settings/features.domains", map[string]any{"siteRule": map[string]any{"value": false}, "confirm": tok}).want(t, 200, "")
		regOrg(anon, "Cy's", "cys", map[string]any{"email": "cy@acme.example", "username": "cy"}).want(t, 202, "")
		if n := rowCount(t, a, `SELECT COUNT(*) FROM tenant_domains WHERE verified_at > 0`); n != 1 {
			t.Fatal("switching the feature off deleted the domain")
		}
	})
}

func TestSwitchingDomainsOffWhileInUseShowsWhatStopsFirst(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newDomFx(t, a)
		key, code := claim(t, f.admB, f.tenB, "acme.example")
		claim(t, f.admC, f.tenC, "beta.example")
		a.dns.SetTXT("_petalbook."+key, "petalbook-verify="+code)
		checkNow(f.admB, f.tenB, key).want(t, 200, "")
		// Turning it on, or off where nothing uses it, is direct.
		f.root.putSetting("features.domains", true).want(t, 200, "")
		f.root.do("PUT", "/api/admin/tenants/"+store.DefaultTenantID+"/settings/features.domains", map[string]any{"siteRule": map[string]any{"value": false}}).want(t, 200, "")
		// Off for the site: the preview names each tenant that uses it, then the confirmation.
		f.root.putSetting("features.domains", false).want(t, 409, "confirm_required")
		r := f.root.do("POST", "/api/admin/confirm", map[string]any{"action": "change_capabilities", "key": "features.domains"}).want(t, 200, "")
		plan := r.body["plan"].([]any)
		if len(plan) != 2 {
			t.Fatal(r.raw)
		}
		for _, p := range plan {
			m := p.(map[string]any)
			if m["slug"] == "acme" && (m["domains"] != float64(1) || m["verified"] != float64(1)) || m["slug"] == "beta" && m["verified"] != float64(0) {
				t.Fatal(r.raw)
			}
		}
		tok := r.body["confirm"]
		// What the plan was made from changes: the token is stale.
		claim(t, f.admC, f.tenC, "beta2.example")
		f.root.do("PUT", "/api/admin/settings/features.domains", map[string]any{"value": false, "confirm": tok}).want(t, 409, "confirm_stale")
		tok = f.root.do("POST", "/api/admin/confirm", map[string]any{"action": "change_capabilities", "key": "features.domains"}).want(t, 200, "").body["confirm"]
		// The token is the setting's, not a bulk action's.
		f.root.do("PUT", "/api/admin/settings/features.domains", map[string]any{"value": false, "confirm": tok}).want(t, 200, "")
		// Nothing is deleted; the routes are off for the tenants, not for the site.
		if n := rowCount(t, a, `SELECT COUNT(*) FROM tenant_domains`); n != 3 {
			t.Fatalf("rows: %d", n)
		}
		f.admB.do("GET", domainsPath(f.tenB), nil).want(t, 403, "feature_off")
		f.root.do("GET", domainsPath(f.tenB), nil).want(t, 200, "")
		if got := a.domains.OfTenant(context.Background(), f.tenB); len(got) != 0 {
			t.Fatalf("an off feature's domains have no effect: %v", got)
		}
		// On again: as it was.
		f.root.putSetting("features.domains", true).want(t, 200, "")
		if got := a.domains.OfTenant(context.Background(), f.tenB); len(got) != 1 {
			t.Fatalf("on again: %v", got)
		}
		f.root.do("POST", "/api/admin/confirm", map[string]any{"action": "change_capabilities", "key": "features.tokens"}).want(t, 400, "invalid")
		f.admB.do("POST", "/api/admin/confirm", map[string]any{"action": "change_capabilities", "key": "features.domains"}).want(t, 404, "not_found")
	})
}

func TestTheSiteListsEveryClaimAndTransfersAVerifiedDomain(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newDomFx(t, a)
		key, code := claim(t, f.admB, f.tenB, "move.example")
		claim(t, f.admC, f.tenC, "move.example")
		claim(t, f.admC, f.tenC, "a.example")
		a.dns.SetTXT("_petalbook."+key, "petalbook-verify="+code)
		checkNow(f.admB, f.tenB, key).want(t, 200, "")
		l := f.root.do("GET", "/api/admin/domains?limit=2", nil).want(t, 200, "")
		items := l.body["domains"].([]any)
		if len(items) != 2 || items[0].(map[string]any)["domain"] != "a.example" || l.body["next"] == nil {
			t.Fatal(l.raw)
		}
		l2 := f.root.do("GET", "/api/admin/domains?limit=2&cursor="+l.body["next"].(string), nil).want(t, 200, "")
		if len(l2.body["domains"].([]any)) != 1 || l2.body["next"] != nil {
			t.Fatal(l2.raw)
		}
		if v := f.root.do("GET", "/api/admin/domains?verified=true", nil).want(t, 200, ""); len(v.body["domains"].([]any)) != 1 ||
			v.body["domains"].([]any)[0].(map[string]any)["tenant"].(map[string]any)["slug"] != "acme" {
			t.Fatal(v.raw)
		}
		f.admB.do("GET", "/api/admin/domains", nil).want(t, 403, "forbidden")
		// The transfer: C holds it, with B's record, and B's claim is gone.
		r := f.root.do("POST", "/api/admin/domains/"+key+"/assign", map[string]any{"tenant": f.tenC}).want(t, 200, "")
		if r.body["state"] != "verified" || r.body["tenant"].(map[string]any)["id"] != f.tenC {
			t.Fatal(r.raw)
		}
		if got := a.domains.OfTenant(context.Background(), f.tenC); len(got) != 1 {
			t.Fatalf("the map follows: %v", got)
		}
		a.clock.Advance(24 * time.Hour)
		a.domains.RecheckDue(context.Background())
		if d := f.admC.do("GET", domainsPath(f.tenC), nil).want(t, 200, "").body["domains"].([]any)[1].(map[string]any); d["state"] != "verified" {
			t.Fatalf("the record still counts for the new holder: %v", d)
		}
		if l := f.admB.do("GET", domainsPath(f.tenB), nil).want(t, 200, ""); len(l.body["domains"].([]any)) != 0 {
			t.Fatal(l.raw)
		}
		f.root.do("POST", "/api/admin/domains/a.example/assign", map[string]any{"tenant": f.tenB}).want(t, 404, "not_found")
		f.root.do("POST", "/api/admin/domains/"+key+"/assign", map[string]any{"tenant": fakeID}).want(t, 404, "not_found")
		a.auth.FlushAudit(context.Background())
		if rows := f.admB.audit(t, "domain_unassigned"); len(rows) != 1 || strings.Contains(fmtAny(rows[0]), f.tenC) {
			t.Fatal(rows)
		}
		if rows := f.admC.audit(t, "domain_assigned"); len(rows) != 1 {
			t.Fatal(rows)
		}
	})
}
