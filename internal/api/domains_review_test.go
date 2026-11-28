package api_test

import (
	"testing"
	"time"

	"petalbook.app/server/internal/api"
)

// featureOf is what a tenant's settings say a feature comes to for it.
func featureOf(t *testing.T, c *client, tenant, key string) any {
	t.Helper()
	for _, it := range c.do("GET", "/api/admin/tenants/"+tenant+"/settings", nil).want(t, 200, "").body["settings"].([]any) {
		if m := it.(map[string]any); m["key"] == key {
			return m["effective"]
		}
	}
	t.Fatalf("no %s", key)
	return nil
}

// A refused check spends nothing: a tenant over its hour does not use up the
// site's minute, and the minute of a domain is the domain's, whoever asks.
func TestARefusedCheckSpendsNothingAndADomainHasOneMinute(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newDomFx(t, a)
		key, _ := claim(t, f.admB, f.tenB, "b.example")
		for range 20 {
			checkNow(f.admB, f.tenB, key).want(t, 200, "")
			a.clock.Advance(61 * time.Second)
		}
		f.root.putSetting("domains.checksPerMinute", 2).want(t, 200, "")
		// B is over its hour: refused, again and again, on the domain's fresh minute.
		for range 5 {
			checkNow(f.admB, f.tenB, key).want(t, 429, "rate_limited")
		}
		// The site's minute is untouched: C gets both of its two.
		k1, _ := claim(t, f.admC, f.tenC, "c1.example")
		k2, _ := claim(t, f.admC, f.tenC, "c2.example")
		checkNow(f.admC, f.tenC, k1).want(t, 200, "")
		checkNow(f.admC, f.tenC, k2).want(t, 200, "")
		// One domain, two tenants: one minute between them.
		a.clock.Advance(61 * time.Second)
		claim(t, f.admC, f.tenC, "shared.example")
		claim(t, f.root, f.tenB, "shared.example")
		checkNow(f.admC, f.tenC, "shared.example").want(t, 200, "")
		checkNow(f.root, f.tenB, "shared.example").want(t, 429, "rate_limited")
	})
}

// A public suffix is no organisation's: co.uk, github.io and their like are
// refused; a name below one is a domain like any other.
func TestPublicSuffixesCannotBeClaimed(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newDomFx(t, a)
		for _, d := range []string{"co.uk", "CO.UK.", "github.io", "com.cn", "gov.uk", "blogspot.com"} {
			f.admB.do("POST", domainsPath(f.tenB), map[string]any{"domain": d}).want(t, 400, "domain_suffix")
		}
		claim(t, f.admB, f.tenB, "acme.co.uk")
		claim(t, f.admB, f.tenB, "acme.github.io")
	})
}

// A domain counts for itself only: an address of a subdomain is not held by a
// restriction on the domain above it.
func TestARestrictedDomainDoesNotHoldItsSubdomains(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root, _ := orgSetup(t, a)
		tenB := root.do("POST", "/api/admin/tenants", map[string]any{"slug": "acme", "name": "Acme"}).want(t, 201, "").body["id"].(string)
		key, code := claim(t, root, tenB, "acme.example")
		a.dns.SetTXT("_petalbook."+key, "petalbook-verify="+code)
		checkNow(root, tenB, key).want(t, 200, "")
		root.do("PATCH", domainsPath(tenB)+"/"+key, map[string]any{"restrictOrgs": true}).want(t, 200, "")
		anon := a.newClient("")
		regOrg(anon, "Sub", "subs", map[string]any{"email": "dee@sub.acme.example", "username": "dee"}).want(t, 202, "")
		regOrg(anon, "Top", "tops", map[string]any{"email": "eve@acme.example", "username": "eve"}).want(t, 409, "domain_managed")
	})
}

// The CNAME target is under the site's host name: none when the public address
// is an IP address, and the punycode form of a Unicode name.
func TestTheCNAMETargetIsAHostName(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"https://notes.example.org":      "notes.example.org",
		"https://Notes.Example.org:8443": "notes.example.org",
		"https://bücher.example/":        "xn--bcher-kva.example",
		"https://127.0.0.1:8080":         "",
		"https://[::1]:8080":             "",
		"http://localhost:8080":          "",
		"":                               "",
	} {
		if got := api.SiteHostOf(in); got != want {
			t.Errorf("SiteHostOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// The lists say which domains mix scripts, so a lookalike is seen before it is
// trusted or moved.
func TestTheListsWarnOfMixedScriptDomains(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newDomFx(t, a)
		look, _ := claim(t, f.admB, f.tenB, "аpple.example") // a Cyrillic а
		plain, _ := claim(t, f.admB, f.tenB, "apple.example")
		got := map[string]any{}
		for _, it := range f.root.do("GET", "/api/admin/domains", nil).want(t, 200, "").body["domains"].([]any) {
			m := it.(map[string]any)
			got[m["domain"].(string)] = m["mixedScript"]
		}
		if got[look] != true || got[plain] != false {
			t.Fatal(got)
		}
	})
}

// What decides is what each tenant gets, not the words of the request: a
// restriction to [false] switches a boolean off as surely as a value of false.
func TestSwitchingDomainsOffByARestrictNeedsTheConfirmation(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newDomFx(t, a)
		claim(t, f.admB, f.tenB, "acme.example")
		restrict := map[string]any{"mode": "restrict", "options": []any{false}}
		// The site's rule for everyone, and a rule for the tenant.
		f.root.do("PUT", "/api/admin/settings/features.domains", map[string]any{"lower": restrict}).want(t, 409, "confirm_required")
		f.root.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/features.domains", map[string]any{"siteRule": map[string]any{"lower": restrict}}).want(t, 409, "confirm_required")
		if v := featureOf(t, f.root, f.tenB, "features.domains"); v != true {
			t.Fatalf("a refused change was written: %v", v)
		}
		// A tenant that uses nothing of it is changed directly.
		f.root.do("PUT", "/api/admin/tenants/"+f.tenC+"/settings/features.domains", map[string]any{"siteRule": map[string]any{"lower": restrict}}).want(t, 200, "")
		if v := featureOf(t, f.root, f.tenC, "features.domains"); v != false {
			t.Fatalf("the restriction does switch it off: %v", v)
		}
		// With the confirmation the site's restriction goes through, and B has it off.
		tok := f.root.do("POST", "/api/admin/confirm", map[string]any{"action": "change_capabilities", "key": "features.domains"}).want(t, 200, "").body["confirm"]
		f.root.do("PUT", "/api/admin/settings/features.domains", map[string]any{"lower": restrict, "confirm": tok}).want(t, 200, "")
		if v := featureOf(t, f.root, f.tenB, "features.domains"); v != false {
			t.Fatalf("after the confirmation: %v", v)
		}
	})
}
