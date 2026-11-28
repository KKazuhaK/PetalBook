package api_test

import (
	"net/url"
	"testing"
	"time"
)

// Finding the SSO by an address, identifier-first sign-in, and an
// organisation's own signin.ssoOnly (3.6 d4). Two organisations, B (acme,
// acme.example) and C (beta, beta.example), on one fake identity provider;
// the DNS and the clock are the fixture's, nothing waits.

// route routes the tenant's verified domain to the provider (its id).
func (f *ssoFx) route(t *testing.T, c *client, tenant, domain, provider string) {
	t.Helper()
	c.do("PATCH", domainsPath(tenant)+"/"+domain, map[string]any{"provider": provider}).want(t, 200, "")
}

func discover(c *client, email string) result {
	return c.do("POST", "/api/auth/discover", map[string]any{"email": email})
}

// discovered is the sso of a discover answer (nil for null).
func discovered(t *testing.T, r result) map[string]any {
	t.Helper()
	r.want(t, 200, "")
	sso, ok := r.body["sso"]
	if !ok {
		t.Fatalf("no sso: %s", r.raw)
	}
	m, _ := sso.(map[string]any)
	return m
}

// The answer is about the domain alone: the same, to the byte, whether an
// account has the address or not; a username and a domain routed nowhere get
// null.
func TestDiscoverIsTheSameWhetherAnAccountExists(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		id := f.orgProvider(t, f.admB, f.tenB, "acme-sso", nil)
		anon := a.newClient("")
		// Not routed yet: null.
		if d := discovered(t, discover(anon, "alice@acme.example")); d != nil {
			t.Fatal(d)
		}
		f.route(t, f.admB, f.tenB, "acme.example", id)
		verifiedUser(t, a, "alice", "alice@acme.example")
		with, without := discover(anon, "alice@acme.example"), discover(anon, "nobody@acme.example")
		if with.raw != without.raw {
			t.Fatalf("the answer tells: %s / %s", with.raw, without.raw)
		}
		d := discovered(t, with)
		if d["slug"] != "acme-sso" || d["kind"] != "oidc" || d["start"] != "/api/auth/oidc/acme-sso/start" || d["enforced"] != false {
			t.Fatal(with.raw)
		}
		// Any spelling of the domain.
		if d := discovered(t, discover(anon, "Alice@ACME.Example")); d == nil || d["slug"] != "acme-sso" {
			t.Fatal(d)
		}
		// A username, an address of a subdomain, of an unclaimed domain, of a
		// verified domain routed nowhere: null.
		for _, in := range []string{"alice", "acme.example", "a@sub.acme.example", "a@other.example", "carol@beta.example", ""} {
			if d := discovered(t, discover(anon, in)); d != nil {
				t.Errorf("%q: %v", in, d)
			}
		}
		// The page asks for the address first, and offers the SSO entry.
		p := anon.do("GET", "/api/auth/providers", nil).want(t, 200, "").body
		if p["identifierFirst"] != true || p["ssoEntry"] != true {
			t.Fatal(p)
		}
		// The provider switched off: routed nowhere, and the page asks as before.
		f.admB.do("PATCH", providersPath(f.tenB)+"/"+id, map[string]any{"enabled": false}).want(t, 200, "")
		if d := discovered(t, discover(anon, "alice@acme.example")); d != nil {
			t.Fatal(d)
		}
		if p := anon.do("GET", "/api/auth/providers", nil).want(t, 200, "").body; p["identifierFirst"] != false || p["ssoEntry"] != false {
			t.Fatal(p)
		}
	})
}

// discover spends the address's requests of the minute, as POST /api/login
// does, from the same allowance: over it, 429 with Retry-After.
func TestDiscoverCountsAgainstTheAddressesMinute(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		f.root.putSetting("signin.perMinute", 3).want(t, 200, "")
		c := a.newClient("203.0.113.40")
		discover(c, "x@acme.example").want(t, 200, "")
		discover(c, "y@acme.example").want(t, 200, "")
		c.do("POST", "/api/login", map[string]any{"username": "adm-b", "password": pw}).want(t, 200, "")
		r := discover(c, "z@acme.example").want(t, 429, "rate_limited")
		if r.resp.Header.Get("Retry-After") == "" {
			t.Fatal("no Retry-After")
		}
		// Another address has its own minute.
		discover(a.newClient("203.0.113.41"), "z@acme.example").want(t, 200, "")
		// A new minute.
		a.clock.Advance(61 * time.Second)
		discover(c, "z@acme.example").want(t, 200, "")
	})
}

// A domain routes to its tenant's own provider, or to a site SSO provider
// offered to the tenant; never to one offered to others, nor to a social one.
func TestDiscoverRoutesToTheTenantsOwnOrAnOfferedSiteSSO(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	idp.idp.RedirectURIs = append(idp.idp.RedirectURIs, testPublic+"/api/auth/oidc/corp/callback", testPublic+"/api/auth/oidc/soc/callback")
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		f.root.do("POST", "/api/admin/providers", idp.providerBody("corp", map[string]any{"enabled": true, "tenants": []string{f.tenC}})).want(t, 201, "")
		f.root.do("POST", "/api/admin/providers", idp.providerBody("soc", map[string]any{"enabled": true, "class": "social"})).want(t, 201, "")
		own := f.orgProvider(t, f.admB, f.tenB, "acme-sso", nil)
		f.route(t, f.admB, f.tenB, "acme.example", own)
		// C lists the site's offered to it, and routes its domain there.
		l := f.admC.do("GET", providersPath(f.tenC), nil).want(t, 200, "").body["offered"].([]any)
		if len(l) != 1 || l[0].(map[string]any)["id"] != "corp" {
			t.Fatal(l)
		}
		f.route(t, f.admC, f.tenC, "beta.example", "corp")
		// B may not: corp is not offered to it; nobody may route to a social one,
		// nor to another tenant's own.
		domPatch := func(c *client, tenant, domain, p string) result {
			return c.do("PATCH", domainsPath(tenant)+"/"+domain, map[string]any{"provider": p})
		}
		domPatch(f.admB, f.tenB, "acme.example", "corp").want(t, 404, "not_found")
		domPatch(f.admC, f.tenC, "beta.example", "soc").want(t, 404, "not_found")
		domPatch(f.admC, f.tenC, "beta.example", own).want(t, 404, "not_found")
		anon := a.newClient("")
		if d := discovered(t, discover(anon, "carol@beta.example")); d == nil || d["slug"] != "corp" || d["enforced"] != false {
			t.Fatal(d)
		}
		if d := discovered(t, discover(anon, "alice@acme.example")); d == nil || d["slug"] != "acme-sso" {
			t.Fatal(d)
		}
		// The start the answer gives signs in through it.
		f.member(t, f.admC, "cm")
		f.link(t, "corp", "cm", "sub-carol")
		c := a.newClient("")
		if loc := idp.signIn2(t, c, "corp", "carol", nil); loc != "/" || signedInAs(t, c) != "cm" {
			t.Fatal(loc)
		}
		// No longer offered to C: routed nowhere.
		f.root.do("PATCH", "/api/admin/providers/corp", map[string]any{"tenants": []string{f.tenB}}).want(t, 200, "")
		if d := discovered(t, discover(anon, "carol@beta.example")); d != nil {
			t.Fatal(d)
		}
	})
}

// An organisation's own ssoOnly: B's people sign in through its SSO only
// (enforced in discover), its administrators keep the password, a site
// administrator is never refused, and C is not touched. The site's social
// sign-in is refused to B's people too, and left off B's page.
func TestATenantsSSOOnlyIsItsOwn(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	idp.idp.RedirectURIs = append(idp.idp.RedirectURIs, testPublic+"/api/auth/oidc/soc/callback")
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		setSSOOnly := func(c *client, tenant string, v bool) result {
			return c.do("PUT", "/api/admin/tenants/"+tenant+"/settings/signin.ssoOnly", map[string]any{"value": v})
		}
		// Without a single sign-on of its own (or offered), refused.
		setSSOOnly(f.admB, f.tenB, true).want(t, 400, "sso_provider_required")
		own := f.orgProvider(t, f.admB, f.tenB, "acme-sso", nil)
		f.route(t, f.admB, f.tenB, "acme.example", own)
		cOwn := f.orgProvider(t, f.admC, f.tenC, "beta-sso", nil)
		f.route(t, f.admC, f.tenC, "beta.example", cOwn)
		f.root.do("POST", "/api/admin/providers", idp.providerBody("soc", map[string]any{"enabled": true, "class": "social"})).want(t, 201, "")
		f.member(t, f.admB, "bm")
		f.member(t, f.admB, "bm2")
		f.member(t, f.admC, "cm")
		f.link(t, "soc", "bm", "sub-alice")
		f.link(t, "soc", "cm", "sub-carol")
		setSSOOnly(f.admB, f.tenB, true).want(t, 200, "")

		anon := a.newClient("")
		if d := discovered(t, discover(anon, "x@acme.example")); d == nil || d["enforced"] != true {
			t.Fatal(d)
		}
		if d := discovered(t, discover(anon, "x@beta.example")); d == nil || d["enforced"] != false {
			t.Fatal(d)
		}
		login := func(ip, user string) result {
			return a.newClient(ip).do("POST", "/api/login", map[string]any{"username": user, "password": pw})
		}
		login("203.0.113.60", "bm").want(t, 403, "local_login_refused")
		login("203.0.113.61", "adm-b").want(t, 200, "") // the back door
		login("203.0.113.62", "cm").want(t, 200, "")    // another organisation
		login("203.0.113.63", "root").want(t, 200, "")  // the site's administrator
		// A member's invite is not accepted with a password either.
		inv := f.admB.do("POST", "/api/invites", map[string]any{"role": "member"}).want(t, 201, "")
		a.newClient("").do("POST", "/api/invites/accept", map[string]any{"token": inv.body["token"], "username": "bm3", "name": "bm3", "password": pw}).want(t, 403, "local_login_refused")
		// The site's social sign-in: refused to B's people, not to C's.
		if loc := idp.signIn2(t, a.newClient(""), "soc", "alice", nil); loc != "/login?sso_error=sso_required" {
			t.Fatal(loc)
		}
		if c := a.newClient(""); idp.signIn2(t, c, "soc", "carol", nil) != "/" || signedInAs(t, c) != "cm" {
			t.Fatal("C's member was refused")
		}
		listed := func(q string) bool {
			for _, p := range anon.do("GET", "/api/auth/providers"+q, nil).want(t, 200, "").body["providers"].([]any) {
				if p.(map[string]any)["slug"] == "soc" {
					return true
				}
			}
			return false
		}
		if listed("?tenant=acme") || !listed("?tenant=beta") {
			t.Fatal("the social button on the pages")
		}
		// B's own SSO signs B's member in.
		f.link(t, own, "bm2", "sub-alice")
		if c := a.newClient(""); idp.signIn2(t, c, "acme-sso", "alice", nil) != "/" || signedInAs(t, c) != "bm2" {
			t.Fatal("B's own SSO was refused")
		}
		// While the site's is off, B may turn its own off again.
		setSSOOnly(f.admB, f.tenB, false).want(t, 200, "")
		login("203.0.113.64", "bm").want(t, 200, "")
		// Under the site's ssoOnly a tenant cannot turn it off.
		f.root.putSetting("signin.ssoOnly", true).want(t, 200, "")
		setSSOOnly(f.admC, f.tenC, false).want(t, 409, "setting_restricted")
	})
}

// The administrators' back door needs an administrator with a password: a
// tenant whose administrators have none cannot require its SSO.
func TestATenantsSSOOnlyNeedsAnAdministratorWithAPassword(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		f.orgProvider(t, f.admB, f.tenB, "acme-sso", nil)
		// B's only administrator loses the password (a site administrator removes it).
		if _, err := a.st.Q().Exec(t.Context(), `DELETE FROM passwords WHERE user_id = ?`, mustUserID(t, a, "adm-b")); err != nil {
			t.Fatal(err)
		}
		f.root.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/signin.ssoOnly", map[string]any{"value": true}).want(t, 400, "local_admin_required")
	})
}

// Registering with a password for an address of a domain whose organisation
// requires its SSO answers 409 sso_required with where to go; an address of
// a domain whose organisation does not is taken as before.
func TestRegistrationOnAnSSOOnlyDomainAnswersSSORequired(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		f := newSSOFx(t, a, idp)
		openRegistration(t, f.root, srv)
		own := f.orgProvider(t, f.admB, f.tenB, "acme-sso", nil)
		f.route(t, f.admB, f.tenB, "acme.example", own)
		anon := a.newClient("")
		reg(anon, map[string]any{"username": "ann", "email": "ann@acme.example"}).want(t, 202, "")
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/signin.ssoOnly", map[string]any{"value": true}).want(t, 200, "")
		r := reg(anon, map[string]any{"username": "ann2", "email": "ann2@acme.example"}).want(t, 409, "sso_required")
		sso, _ := r.body["sso"].(map[string]any)
		if sso == nil || sso["slug"] != "acme-sso" || sso["start"] != "/api/auth/oidc/acme-sso/start" || sso["enforced"] != true {
			t.Fatal(r.raw)
		}
		reg(anon, map[string]any{"username": "bob", "email": "bob@beta.example"}).want(t, 202, "")
		// The link of the registration made before is refused now too.
		tok := lastTo(t, srv, "ann@acme.example").Token(t)
		anon.do("POST", "/api/register/confirm", map[string]any{"token": tok}).want(t, 409, "sso_required")
	})
}

// The address goes to the provider as login_hint: through the start the
// discover answer gives.
func TestTheDiscoveredStartCarriesTheLoginHint(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		own := f.orgProvider(t, f.admB, f.tenB, "acme-sso", nil)
		f.route(t, f.admB, f.tenB, "acme.example", own)
		d := discovered(t, discover(a.newClient(""), "alice@acme.example"))
		r := a.newClient("").do("GET", d["start"].(string)+"?login_hint="+url.QueryEscape("alice@acme.example"), nil)
		loc, err := url.Parse(r.resp.Header.Get("Location"))
		if err != nil || r.code != 303 || loc.Query().Get("login_hint") != "alice@acme.example" {
			t.Fatalf("%d %v", r.code, loc)
		}
	})
}
