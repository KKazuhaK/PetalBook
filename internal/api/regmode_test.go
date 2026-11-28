package api_test

import (
	"fmt"
	"strings"
	"testing"

	"petalbook.app/server/internal/auth/testidp"
	"petalbook.app/server/internal/mail/mailtest"
)

// The registration mode (docs/design/decisions-2026-11-27.md): open, invite or
// closed decides whether a way of making an account works. The site's value
// governs the people who belong to no organisation (the default tenant); an
// organisation has its own, not bounded by the site's. These tests walk every
// way an account is made against the three modes.

var regModes = []string{"open", "invite", "closed"}

// modeRefusal is the error code a way of making an account answers under a mode
// ("" when it works).
var modeRefusal = map[string]string{"open": "", "invite": "invite_required", "closed": "registration_closed"}

// inviteRefusal is the same for an invitation, which is the way in that invite
// mode is for: only closed stops it.
var inviteRefusal = map[string]string{"open": "", "invite": "", "closed": "registration_closed"}

// setMode sets a registration mode: the site's (tenant "") as root, or a
// tenant's as its administrator.
func setMode(t *testing.T, c *client, tenant, mode string) {
	t.Helper()
	if tenant == "" {
		c.putSetting("registration.mode", mode).want(t, 200, "")
		return
	}
	c.do("PUT", "/api/admin/tenants/"+tenant+"/settings/registration.mode", map[string]any{"value": mode}).want(t, 200, "")
}

// wantSignUp checks a password sign-up's answer: 202 when the mode lets it be,
// else 403 and the mode's code.
func wantSignUp(t *testing.T, r result, mode string) {
	t.Helper()
	if code := modeRefusal[mode]; code == "" {
		r.want(t, 202, "")
	} else {
		r.want(t, 403, code)
	}
}

// ssoErr is the ?sso_error= of a sign-in's end, "" when it did not end in one.
func ssoErr(loc string) string {
	_, q, ok := strings.Cut(loc, "sso_error=")
	if !ok {
		return ""
	}
	q, _, _ = strings.Cut(q, "&")
	return q
}

// wantSSO checks where an external sign-in ended under a mode.
func wantSSO(t *testing.T, loc, mode, what string) {
	t.Helper()
	wantSSOCode(t, loc, modeRefusal[mode], mode, what)
}

func wantSSOCode(t *testing.T, loc, code, mode, what string) {
	t.Helper()
	if code == "" {
		if ssoErr(loc) != "" {
			t.Fatalf("%s: refused under %s: %s", what, mode, loc)
		}
	} else if ssoErr(loc) != code {
		t.Fatalf("%s: under %s want sso_error=%s, got %s", what, mode, code, loc)
	}
}

// A password sign-up: a personal one is governed by the site's mode, one into an
// organisation (?tenant=) by that organisation's, whatever the site says.
func TestRegModePasswordSignUp(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newJoinFx(t, a)
		anon := a.newClient("")
		n := 0
		for _, site := range regModes {
			for _, org := range regModes {
				setMode(t, f.root, "", site)
				setMode(t, f.admB, f.tenB, org)
				n++
				wantSignUp(t, reg(anon, map[string]any{"username": fmt.Sprintf("p%d", n), "email": fmt.Sprintf("p%d@mail.test", n)}), site)
				wantSignUp(t, reg(anon, map[string]any{"tenant": "acme", "username": fmt.Sprintf("o%d", n), "email": fmt.Sprintf("o%d@mail.test", n)}), org)
				// What the page is told.
				if g := anon.do("GET", "/api/register", nil).want(t, 200, ""); g.body["mode"] != site || g.body["open"] != (site == "open") {
					t.Fatalf("personal page under %s: %s", site, g.raw)
				}
				if g := anon.do("GET", "/api/register?tenant=acme", nil).want(t, 200, ""); g.body["mode"] != org || g.body["open"] != (org == "open") {
					t.Fatalf("organisation page under %s: %s", org, g.raw)
				}
				if p := anon.do("GET", "/api/auth/providers", nil).want(t, 200, ""); p.body["register"] != (site == "open") {
					t.Fatalf("the sign-in page's link under %s: %s", site, p.raw)
				}
				if p := anon.do("GET", "/api/auth/providers?tenant=acme", nil).want(t, 200, ""); p.body["register"] != (org == "open") {
					t.Fatalf("the organisation's sign-in page's link under %s: %s", org, p.raw)
				}
			}
		}
	})
}

// The link of a confirmation is asked again: a mode that tightened since, by a
// way that does not cancel what is pending, refuses it.
func TestRegModeTheConfirmLinkIsAskedAgain(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		root := a.setup("root", pw)
		openRegistration(t, root, srv)
		anon := a.newClient("")
		for i, tighter := range []string{"invite", "closed"} {
			a.setSetting(t, "registration.mode", "open")
			u := fmt.Sprintf("late%d", i)
			reg(anon, map[string]any{"username": u, "email": u + "@mail.test"}).want(t, 202, "")
			tok := srv.Wait(i + 1)[i].Token(t)
			a.setSetting(t, "registration.mode", tighter) // the settings service: nothing is cancelled
			anon.do("POST", "/api/register/confirm", map[string]any{"token": tok}).want(t, 403, modeRefusal[tighter])
			if !mustNoUser(a, u) {
				t.Fatalf("an account was made under %s", tighter)
			}
		}
		// Through the API, tightening cancels what is pending: the link is gone.
		a.setSetting(t, "registration.mode", "open")
		reg(anon, map[string]any{"username": "gone", "email": "gone@mail.test"}).want(t, 202, "")
		tok := srv.Wait(3)[2].Token(t)
		setMode(t, root, "", "invite")
		anon.do("POST", "/api/register/confirm", map[string]any{"token": tok}).want(t, 404, "not_found")
	})
}

// An invitation: works under open and invite, by password and by a sign-in;
// closed stops invite links, which then answer a clear error.
func TestRegModeInvites(t *testing.T) {
	t.Parallel()
	idp := newIDPFor(t, "soc")
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newJoinFx(t, a)
		f.root.do("POST", "/api/admin/providers", idp.providerBody("soc", map[string]any{"enabled": true, "class": "social"})).want(t, 201, "")
		n := 0
		invite := func(c *client) string {
			return c.do("POST", "/api/invites", map[string]any{"role": "member"}).want(t, 201, "").body["token"].(string)
		}
		for _, mode := range regModes {
			for _, who := range []struct {
				name   string
				admin  *client
				tenant string
			}{{"personal", f.root, ""}, {"acme", f.admB, f.tenB}} {
				setMode(t, who.admin, who.tenant, mode)
				anon := a.newClient("")
				// By password.
				n++
				tok := invite(who.admin)
				user := fmt.Sprintf("ip%d", n)
				r := anon.do("POST", "/api/invites/accept", map[string]any{"token": tok, "username": user, "name": user, "password": pw})
				if code := inviteRefusal[mode]; code == "" {
					r.want(t, 201, "")
				} else {
					r.want(t, 403, code)
					// The page that checks the link answers the same.
					anon.do("POST", "/api/invites/check", map[string]any{"token": tok}).want(t, 403, code)
				}
				// By a sign-in of an external provider.
				n++
				user = fmt.Sprintf("is%d", n)
				idp.addUser(user, testidp.Claims{"sub": "sub-" + user, "preferred_username": user, "email": user + "@other.test", "email_verified": true, "name": user})
				loc := idp.signIn2(t, a.newClient(""), "soc", user, map[string]any{"intent": "invite", "token": invite(who.admin)})
				wantSSOCode(t, loc, inviteRefusal[mode], mode, "invite by sign-in")
				if inviteRefusal[mode] == "" && userTenant(t, a, user).TenantID != map[string]string{"": "0000000000", f.tenB: f.tenB}[who.tenant] {
					t.Fatalf("%s: the account is in tenant %s", user, userTenant(t, a, user).TenantID)
				}
			}
		}
	})
}

// A sign-in of the site's provider that is no invitation: the account lands in
// the tenant the sign-in is aimed at and that tenant's mode decides. The site's
// is for the default tenant; an address on a joining domain lands in that
// organisation (and its mode, or the domain's own opt-in, decides); a provider
// aimed at an organisation lands there.
func TestRegModeSiteSocialSignIn(t *testing.T) {
	t.Parallel()
	soc, socB := newIDPFor(t, "soc"), newIDPFor(t, "socb")
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newJoinFx(t, a)
		f.root.do("POST", "/api/admin/providers", soc.providerBody("soc", map[string]any{"enabled": true, "class": "social", "mergeVerifiedEmail": true})).want(t, 201, "")
		f.root.do("POST", "/api/admin/providers", socB.providerBody("socb", map[string]any{"enabled": true, "class": "social", "tenant": f.tenB})).want(t, 201, "")
		n := 0
		signIn := func(idp *fakeIDP, slug, email string, verified bool) (string, string) {
			n++
			user := fmt.Sprintf("s%d", n)
			idp.addUser(user, testidp.Claims{"sub": "sub-" + user, "preferred_username": user, "email": email, "email_verified": verified, "name": user})
			return idp.signIn2(t, a.newClient(""), slug, user, nil), user
		}
		// Landing in the default tenant: the site's mode.
		for _, site := range regModes {
			setMode(t, f.root, "", site)
			setMode(t, f.admB, f.tenB, "closed") // not the one that decides
			loc, user := signIn(soc, "soc", fmt.Sprintf("x%d@other.test", n), true)
			wantSSO(t, loc, site, "personal")
			if site == "open" && userTenant(t, a, user).TenantID != "0000000000" {
				t.Fatalf("landed in %s", userTenant(t, a, user).TenantID)
			}
		}
		// A provider aimed at an organisation: the organisation's mode, whatever the site's.
		setMode(t, f.root, "", "closed")
		for _, org := range regModes {
			setMode(t, f.admB, f.tenB, org)
			loc, user := signIn(socB, "socb", fmt.Sprintf("y%d@other.test", n), true)
			wantSSO(t, loc, org, "provider aimed at an organisation")
			if org == "open" && userTenant(t, a, user).TenantID != f.tenB {
				t.Fatalf("landed in %s", userTenant(t, a, user).TenantID)
			}
		}
		// A verified address on a domain that joins an organisation lands there:
		// under open and under invite (the domain's own opt-in), and not under
		// closed, where every join is off and the site's mode decides.
		setMode(t, f.root, "", "closed")
		setJoin(f.admB, f.tenB, "acme.example", "auto").want(t, 200, "")
		for _, org := range regModes {
			setMode(t, f.admB, f.tenB, org)
			loc, user := signIn(soc, "soc", fmt.Sprintf("j%d@acme.example", n), true)
			if org == "closed" {
				wantSSO(t, loc, "closed", "joining under closed")
				continue
			}
			if ssoErr(loc) != "" || userTenant(t, a, user).TenantID != f.tenB {
				t.Fatalf("a join under %s: %s", org, loc)
			}
		}
		// With joins off by the mode, an address of the domain is a stranger: the site's mode.
		setMode(t, f.root, "", "open")
		loc, user := signIn(soc, "soc", "closedjoin@acme.example", true)
		if ssoErr(loc) != "" || userTenant(t, a, user).TenantID != "0000000000" {
			t.Fatalf("a join under closed: %s", loc)
		}
		// The address the provider does not vouch for never joins.
		setMode(t, f.admB, f.tenB, "open")
		if loc, user = signIn(soc, "soc", "liar@acme.example", false); userTenant(t, a, user).TenantID == f.tenB {
			t.Fatalf("an unverified address joined: %s", loc)
		}
		// The domain asking for approval: a request, under invite; none under closed.
		setJoin(f.admB, f.tenB, "acme.example", "request").want(t, 200, "")
		setMode(t, f.admB, f.tenB, "invite")
		loc, _ = signIn(soc, "soc", "req@acme.example", true)
		if !strings.HasPrefix(loc, "/register/waiting#") {
			t.Fatalf("request under invite: %s", loc)
		}
		setMode(t, f.root, "", "closed")
		setMode(t, f.admB, f.tenB, "closed")
		if loc, _ = signIn(soc, "soc", "req2@acme.example", true); ssoErr(loc) != "registration_closed" {
			t.Fatalf("request under closed: %s", loc)
		}
	})
}

// An organisation's own single sign-on lands in that organisation: open together
// with ssoOnly makes its people by the SSO alone.
func TestRegModeAnOrganisationsOwnSSO(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		enableMail(t, f.root, fakeMail(t))
		f.orgProvider(t, f.admB, f.tenB, "acme-sso", nil)
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/signin.ssoOnly", map[string]any{"value": true}).want(t, 200, "")
		for _, org := range regModes {
			setMode(t, f.root, "", "closed") // the site's does not matter
			setMode(t, f.admB, f.tenB, org)
			// Password sign-up is refused whatever the mode (single sign-on is required).
			if code := modeRefusal[org]; code == "" {
				reg(a.newClient(""), map[string]any{"tenant": "acme", "username": "pw-" + org, "email": "pw-" + org + "@mail.test"}).want(t, 409, "sso_required")
			} else {
				reg(a.newClient(""), map[string]any{"tenant": "acme", "username": "pw-" + org, "email": "pw-" + org + "@mail.test"}).want(t, 403, code)
			}
			who := "alice-" + org
			idp.addUser(who, testidp.Claims{"sub": "sub-" + who, "preferred_username": who, "email": who + "@acme.example", "name": who})
			loc := idp.signIn2(t, a.newClient(""), "acme-sso", who, nil)
			wantSSO(t, loc, org, "the organisation's own SSO")
			if org == "open" {
				if u := userTenant(t, a, who); u.TenantID != f.tenB || u.Role != "member" {
					t.Fatalf("%+v", u)
				}
			}
		}
	})
}

// A domain's join mode is the organisation's own opt-in: it works under invite
// (the default); closed turns every join off, no request is taken, but a request
// that is already pending stays approvable.
func TestRegModeDomainJoins(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newJoinFx(t, a)
		anon := a.newClient("")
		// auto, under invite.
		setJoin(f.admB, f.tenB, "acme.example", "auto").want(t, 200, "")
		f.signUp(t, anon, "ann", "ann@acme.example").want(t, 201, "")
		if u := userTenant(t, a, "ann"); u.TenantID != f.tenB {
			t.Fatalf("%+v", u)
		}
		// request, under invite.
		setJoin(f.admB, f.tenB, "acme.example", "request").want(t, 200, "")
		f.request(t, a.newClient(""), "rob", "rob@acme.example")
		id := pendingID(t, f.admB, f.tenB)
		// Closed: no new request, and nobody joins; the site's mode (invite) answers.
		setMode(t, f.admB, f.tenB, "closed")
		reg(a.newClient(""), map[string]any{"username": "late", "email": "late@acme.example"}).want(t, 403, "invite_required")
		setJoin(f.admB, f.tenB, "acme.example", "auto").want(t, 200, "")
		reg(a.newClient(""), map[string]any{"username": "late", "email": "late@acme.example"}).want(t, 403, "invite_required")
		if g := a.newClient("").do("GET", "/api/register", nil).want(t, 200, ""); g.body["domainJoin"] != false {
			t.Fatalf("a closed organisation's join is offered: %s", g.raw)
		}
		// The pending request is an administrator's to decide: approved while closed.
		f.admB.do("POST", joinPath(f.tenB)+"/"+id+"/approve", map[string]any{}).want(t, 200, "")
		if u := userTenant(t, a, "rob"); u.TenantID != f.tenB {
			t.Fatalf("%+v", u)
		}
		// A sign-up in the organisation's link that was pending is cancelled by closing.
		setMode(t, f.admB, f.tenB, "invite")
		tok := f.mailed(t, "sue@acme.example", func() {
			reg(a.newClient(""), map[string]any{"username": "sue", "email": "sue@acme.example"}).want(t, 202, "")
		}).Token(t)
		setMode(t, f.admB, f.tenB, "closed")
		a.newClient("").do("POST", "/api/register/confirm", map[string]any{"token": tok}).want(t, 404, "not_found")
	})
}

// quota.people holds every way into a tenant: an invite by password and by a
// sign-in, an automatic join, a request's approval, the organisation's own SSO,
// and an organisation's open sign-up.
func TestRegModeQuotaPeopleHoldsEveryPath(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		srv := fakeMail(t)
		enableMail(t, f.root, srv)
		f.orgProvider(t, f.admB, f.tenB, "acme-sso", nil)
		setMode(t, f.admB, f.tenB, "open")
		setJoin(f.admB, f.tenB, "acme.example", "request").want(t, 200, "")
		anon := a.newClient("")
		// A request made while there was room.
		tok := regToken(t, srv, anon, "acme", "waiting", "waiting@acme.example")
		anon.do("POST", "/api/register/confirm", map[string]any{"token": tok}).want(t, 202, "")
		id := pendingID(t, f.admB, f.tenB)
		// The tenant has its one person (the administrator) and may have no more.
		setTotals(t, f.root, f.tenB, map[string]any{"people": 1})
		inv := f.admB.do("POST", "/api/invites", map[string]any{"role": "member"}).want(t, 201, "").body["token"]
		anon.do("POST", "/api/invites/accept", map[string]any{"token": inv, "username": "inv1", "name": "inv1", "password": pw}).want(t, 409, "people_limit")
		if loc := idp.signIn2(t, a.newClient(""), "acme-sso", "alice", map[string]any{"intent": "invite", "token": inv}); ssoErr(loc) != "people_limit" {
			t.Fatalf("an invite by a sign-in: %s", loc)
		}
		if loc := idp.signIn2(t, a.newClient(""), "acme-sso", "alice", nil); ssoErr(loc) != "people_limit" {
			t.Fatalf("the organisation's own SSO: %s", loc)
		}
		f.admB.do("POST", joinPath(f.tenB)+"/"+id+"/approve", map[string]any{}).want(t, 409, "people_limit")
		setJoin(f.admB, f.tenB, "acme.example", "auto").want(t, 200, "")
		tok = regToken(t, srv, a.newClient(""), "", "auto", "auto@acme.example")
		anon.do("POST", "/api/register/confirm", map[string]any{"token": tok}).want(t, 409, "people_limit")
		tok = regToken(t, srv, a.newClient(""), "acme", "plain", "plain@mail.test")
		anon.do("POST", "/api/register/confirm", map[string]any{"token": tok}).want(t, 409, "people_limit")
		for _, u := range []string{"inv1", "alice", "waiting", "auto", "plain"} {
			if !mustNoUser(a, u) {
				t.Fatalf("%s was made over the quota", u)
			}
		}
		// Room again: the same invite works, and so does the approval.
		setTotals(t, f.root, f.tenB, map[string]any{"people": 3})
		anon.do("POST", "/api/invites/accept", map[string]any{"token": inv, "username": "inv1", "name": "inv1", "password": pw}).want(t, 201, "")
		f.admB.do("POST", joinPath(f.tenB)+"/"+id+"/approve", map[string]any{}).want(t, 200, "")
		// The default tenant has no quota.people unless the site sets one for it.
		if !mustNoUser(a, "alice") {
			t.Fatal("alice")
		}
	})
}

// regToken registers (in the organisation named by slug, "" for the default
// tenant's page) and returns the token of the mail.
func regToken(t *testing.T, srv *mailtest.Server, c *client, slug, user, email string) string {
	t.Helper()
	n := len(srv.Messages())
	body := map[string]any{"username": user, "email": email}
	if slug != "" {
		body["tenant"] = slug
	}
	reg(c, body).want(t, 202, "")
	srv.Wait(n + 1)
	return lastTo(t, srv, email).Token(t)
}

// Tenancy off: everything is the default tenant, and the site's mode is the
// one that decides.
func TestRegModeWithTenancyOff(t *testing.T) {
	t.Parallel()
	idp := newIDPFor(t, "soc")
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		root := a.setup("root", pw)
		enableMail(t, root, srv)
		root.do("POST", "/api/admin/providers", idp.providerBody("soc", map[string]any{"enabled": true, "class": "social"})).want(t, 201, "")
		for i, mode := range regModes {
			setMode(t, root, "", mode)
			anon := a.newClient("")
			wantSignUp(t, reg(anon, map[string]any{"username": fmt.Sprintf("t%d", i), "email": fmt.Sprintf("t%d@mail.test", i)}), mode)
			user := fmt.Sprintf("so%d", i)
			idp.addUser(user, testidp.Claims{"sub": "sub-" + user, "preferred_username": user, "email": user + "@other.test", "name": user})
			wantSSO(t, idp.signIn2(t, a.newClient(""), "soc", user, nil), mode, "sign-in")
			// An invitation works under open and invite.
			tok := root.do("POST", "/api/invites", map[string]any{"role": "member"}).want(t, 201, "").body["token"]
			r := a.newClient("").do("POST", "/api/invites/accept", map[string]any{"token": tok, "username": fmt.Sprintf("iv%d", i), "name": "x", "password": pw})
			if code := inviteRefusal[mode]; code == "" {
				r.want(t, 201, "")
			} else {
				r.want(t, 403, code)
			}
		}
	})
}
