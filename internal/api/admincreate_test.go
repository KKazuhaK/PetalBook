package api_test

import (
	"strings"
	"testing"
)

// An administrator makes an account by hand (docs/design/decisions-2026-11-27.md:
// under registration mode closed, only administrators make accounts). It is an
// administrator's act, so no registration mode gates it; quota.people still
// bounds it. The administrator never knows the password: the person gets a
// one-time set-password link.

const adminUsers = "/api/admin/users"

func tokenOf(link string) string {
	_, tok, _ := strings.Cut(link, "#")
	return tok
}

func TestAnAdministratorMakesAnAccountAndHandsOverASetPasswordLink(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		r := root.do("POST", adminUsers, map[string]any{"username": "newbie", "name": "New Bie", "email": "newbie@mail.test", "role": "member"}).want(t, 201, "")
		u := r.user()
		if u["username"] != "newbie" || u["name"] != "New Bie" || u["email"] != "newbie@mail.test" || u["role"] != "member" || u["emailVerified"] != false {
			t.Fatalf("the user: %s", r.raw)
		}
		link, _ := r.body["link"].(string)
		if !strings.HasPrefix(link, a.public+"/reset-password#") || r.body["signIn"] != "link" || r.body["mail"] != "none" || r.body["expires"] == nil {
			t.Fatalf("the answer: %s", r.raw)
		}
		tok := tokenOf(link)
		anon := a.newClient("203.0.113.9")
		anon.do("POST", "/api/password/reset/check", map[string]any{"token": tok}).want(t, 200, "")
		anon.do("POST", "/api/password/reset", map[string]any{"token": tok, "password": "a brand new password"}).want(t, 204, "")
		// The link works once, and a copied link does not verify the address.
		anon.do("POST", "/api/password/reset/check", map[string]any{"token": tok}).want(t, 404, "not_found")
		p := a.newClient("")
		li := p.do("POST", "/api/login", map[string]any{"username": "newbie", "password": "a brand new password"}).want(t, 200, "")
		if li.user()["emailVerified"] != false {
			t.Fatalf("a copied link does not verify: %s", li.raw)
		}
		items := auditKinds(t, root, "user_created")
		if len(items) != 1 {
			t.Fatalf("one user_created row: %v", items)
		}
		row := items[0].(map[string]any)
		if row["user"] != u["id"] || row["actor"] == nil {
			t.Fatalf("the row names the person and the actor: %v", row)
		}
	})
}

func TestTheMailedSetPasswordLinkVerifiesTheAddressAndTheCopiedOneDiesWithIt(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		root := a.setup("root", pw)
		enableMail(t, root, srv)
		r := root.do("POST", adminUsers, map[string]any{"username": "newbie", "email": "newbie@mail.test", "lang": "zh-Hans"}).want(t, 201, "")
		if r.body["mail"] != "sent" || r.body["signIn"] != "link" {
			t.Fatalf("the answer: %s", r.raw)
		}
		srv.Wait(1)
		m := lastTo(t, srv, "newbie@mail.test")
		mailed, copied := m.Token(t), tokenOf(r.body["link"].(string))
		if mailed == copied || !strings.Contains(m.Text, "/reset-password#") {
			t.Fatalf("the mailed link is a link of its own: %s", m.Text)
		}
		if !strings.Contains(m.Subject, "账号") {
			t.Fatalf("the person's language: %s", m.Subject)
		}
		anon := a.newClient("203.0.113.9")
		anon.do("POST", "/api/password/reset", map[string]any{"token": mailed, "password": "a brand new password"}).want(t, 204, "")
		anon.do("POST", "/api/password/reset", map[string]any{"token": copied, "password": "another new password"}).want(t, 404, "not_found")
		li := a.newClient("").do("POST", "/api/login", map[string]any{"username": "newbie", "password": "a brand new password"}).want(t, 200, "")
		if li.user()["emailVerified"] != true {
			t.Fatalf("the mailed link verifies the address: %s", li.raw)
		}
	})
}

func TestAnOrganisationAdministratorMakesAccountsOnlyInTheirOwnTenant(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newDomFx(t, a)
		// Their own tenant, whatever role they may grant there.
		r := f.admB.do("POST", adminUsers, map[string]any{"username": "b1", "email": "b1@mail.test", "role": "admin"}).want(t, 201, "")
		if tn := r.user()["tenant"].(map[string]any); tn["id"] != f.tenB || r.user()["role"] != "admin" || r.user()["siteAdmin"] != false {
			t.Fatalf("made in the administrator's tenant: %s", r.raw)
		}
		f.admB.do("POST", adminUsers, map[string]any{"username": "b2", "email": "b2@mail.test", "tenant": f.tenB}).want(t, 201, "")
		// Another tenant is a tenant that does not exist; no site administrator is made by one.
		f.admB.do("POST", adminUsers, map[string]any{"username": "c1", "email": "c1@mail.test", "tenant": f.tenC}).want(t, 404, "not_found")
		f.admB.do("POST", adminUsers, map[string]any{"username": "b3", "email": "b3@mail.test", "role": "admin", "siteAdmin": true}).want(t, 403, "forbidden")
		f.admB.do("POST", adminUsers, map[string]any{"username": "b4", "email": "b4@mail.test", "role": "owner"}).want(t, 400, "invalid")
		// A member is no administrator.
		a.addUser("plain", pw, "member").do("POST", adminUsers, map[string]any{"username": "x1", "email": "x1@mail.test"}).want(t, 403, "forbidden")
		a.newClient("").do("POST", adminUsers, map[string]any{"username": "x2"}).want(t, 401, "unauthenticated")
	})
}

func TestASiteAdministratorMakesAccountsInTheDefaultTenantOrAnOrganisation(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newDomFx(t, a)
		r := f.root.do("POST", adminUsers, map[string]any{"username": "d1", "email": "d1@mail.test"}).want(t, 201, "")
		if id := r.user()["tenant"].(map[string]any)["id"]; id == f.tenB || id == f.tenC || r.user()["siteAdmin"] != false {
			t.Fatalf("a personal account: %s", r.raw)
		}
		r = f.root.do("POST", adminUsers, map[string]any{"username": "c1", "email": "c1@mail.test", "tenant": f.tenC}).want(t, 201, "")
		if r.user()["tenant"].(map[string]any)["id"] != f.tenC {
			t.Fatalf("made in the organisation: %s", r.raw)
		}
		f.admC.do("GET", "/api/users/"+r.user()["id"].(string), nil).want(t, 200, "")
		// Acting in an organisation's scope, the account lands there.
		r = f.root.do("POST", adminUsers, map[string]any{"username": "b1", "email": "b1@mail.test"}, "PetalBook-Scope", "tenant:"+f.tenB).want(t, 201, "")
		if r.user()["tenant"].(map[string]any)["id"] != f.tenB {
			t.Fatalf("made in the acting scope: %s", r.raw)
		}
		// In an organisation's scope nothing reaches another one, and a site administrator is not made there.
		f.root.do("POST", adminUsers, map[string]any{"username": "c2", "email": "c2@mail.test", "tenant": f.tenC}, "PetalBook-Scope", "tenant:"+f.tenB).want(t, 404, "not_found")
		// A site administrator, made by who manages them.
		r = f.root.do("POST", adminUsers, map[string]any{"username": "d2", "email": "d2@mail.test", "role": "admin"}).want(t, 201, "")
		if r.user()["siteAdmin"] != true {
			t.Fatalf("an administrator of the default tenant is one of the server: %s", r.raw)
		}
		f.root.do("POST", adminUsers, map[string]any{"username": "d3", "email": "d3@mail.test", "role": "admin", "siteAdmin": false}).want(t, 201, "")
	})
}

func TestMakingAnAccountByHandIsNotGatedByTheRegistrationModeButByTheCap(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newDomFx(t, a)
		setMode(t, f.root, "", "closed")
		setMode(t, f.admB, f.tenB, "closed")
		// Neither the mode nor registration.domains stops an administrator.
		f.root.do("PUT", "/api/admin/settings/registration.domains", map[string]any{"value": []string{"only.example"}}).want(t, 200, "")
		f.root.do("POST", adminUsers, map[string]any{"username": "d1", "email": "d1@elsewhere.test"}).want(t, 201, "")
		f.admB.do("POST", adminUsers, map[string]any{"username": "b1", "email": "b1@elsewhere.test"}).want(t, 201, "")
		// The people cap bounds it: B has two people now.
		f.root.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/quota.people", map[string]any{"value": 2}).want(t, 200, "")
		f.admB.do("POST", adminUsers, map[string]any{"username": "b2", "email": "b2@elsewhere.test"}).want(t, 409, "people_limit")
		f.root.do("POST", adminUsers, map[string]any{"username": "b3", "email": "b3@elsewhere.test", "tenant": f.tenB}).want(t, 409, "people_limit")
		// A refused account leaves nothing: the name is free.
		f.root.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/quota.people", map[string]any{"value": 3}).want(t, 200, "")
		f.admB.do("POST", adminUsers, map[string]any{"username": "b2", "email": "b2@elsewhere.test"}).want(t, 201, "")
	})
}

func TestMakingAnAccountByHandKeepsTheRulesOfNamesAndAddresses(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		root.do("POST", adminUsers, map[string]any{"username": "d1", "email": "d1@mail.test"}).want(t, 201, "")
		root.do("POST", adminUsers, map[string]any{"username": "D1", "email": "other@mail.test"}).want(t, 409, "")
		root.do("POST", adminUsers, map[string]any{"username": "root", "email": "other@mail.test"}).want(t, 409, "")
		root.do("POST", adminUsers, map[string]any{"username": "d2", "email": "D1@mail.test"}).want(t, 409, "email_taken")
		root.do("POST", adminUsers, map[string]any{"username": "d3", "email": "not an address"}).want(t, 400, "")
		root.do("POST", adminUsers, map[string]any{"username": "no spaces", "email": "d3@mail.test"}).want(t, 400, "")
		root.do("POST", adminUsers, map[string]any{"email": "d3@mail.test"}).want(t, 400, "")
		root.do("POST", adminUsers, map[string]any{"username": "d3", "tenant": "nowhere"}).want(t, 404, "not_found")
		// An account without an address has no mail to send: the link is the way in.
		r := root.do("POST", adminUsers, map[string]any{"username": "d4"}).want(t, 201, "")
		if r.body["link"] == nil || r.body["mail"] != "none" {
			t.Fatalf("no address: %s", r.raw)
		}
	})
}

// An organisation that requires its single sign-on: a member signs in there,
// so no set-password link is made (an administrator is held to it only with
// ssoOnlyAdmins).
func TestAnSSOOnlyTenantsNewPeopleGetNoSetPasswordLink(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		f.orgProvider(t, f.admB, f.tenB, "acme-sso", nil)
		put := func(key string, v bool) {
			f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/"+key, map[string]any{"value": v}).want(t, 200, "")
		}
		put("signin.ssoOnly", true)
		// The SSO finds the account by its address, so one is needed.
		f.admB.do("POST", adminUsers, map[string]any{"username": "m0"}).want(t, 400, "invalid")
		r := f.admB.do("POST", adminUsers, map[string]any{"username": "m1", "email": "m1@acme.example"}).want(t, 201, "")
		if r.body["signIn"] != "sso" || r.body["link"] != nil || r.body["mail"] != "none" {
			t.Fatalf("a member of an ssoOnly tenant: %s", r.raw)
		}
		// An administrator keeps the password unless ssoOnlyAdmins holds them too.
		r = f.admB.do("POST", adminUsers, map[string]any{"username": "a2", "email": "a2@acme.example", "role": "admin"}).want(t, 201, "")
		if r.body["signIn"] != "link" || r.body["link"] == nil {
			t.Fatalf("an administrator: %s", r.raw)
		}
		put("signin.ssoOnlyAdmins", true)
		r = f.admB.do("POST", adminUsers, map[string]any{"username": "a3", "email": "a3@acme.example", "role": "admin"}).want(t, 201, "")
		if r.body["signIn"] != "sso" || r.body["link"] != nil {
			t.Fatalf("an administrator held to the SSO: %s", r.raw)
		}
		// A site administrator keeps the password anywhere.
		r = f.root.do("POST", adminUsers, map[string]any{"username": "a4", "email": "a4@acme.example", "role": "admin", "siteAdmin": true}).want(t, 201, "")
		if r.body["signIn"] != "link" {
			t.Fatalf("a site administrator: %s", r.raw)
		}
	})
}

// A temporary password given later, or a changed password, ends the link too.
func TestASetPasswordLinkEndsWhenAnAdministratorGivesATemporaryPassword(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		r := root.do("POST", adminUsers, map[string]any{"username": "newbie", "email": "newbie@mail.test"}).want(t, 201, "")
		tok := tokenOf(r.body["link"].(string))
		root.do("POST", "/api/users/"+r.user()["id"].(string)+"/password", map[string]any{"password": "a temporary pass"}).want(t, 204, "")
		a.newClient("203.0.113.9").do("POST", "/api/password/reset/check", map[string]any{"token": tok}).want(t, 404, "not_found")
	})
}
