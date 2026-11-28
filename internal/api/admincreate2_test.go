package api_test

import (
	"context"
	"errors"
	"testing"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/store"
)

// A waiting account can only be claimed by an address its tenant's SSO may
// vouch for: one on a verified domain of the tenant (or routed to it). Anything
// else would only lock the address out of every other sign-in.
func TestAnSSOAccountNeedsAnAddressOnAVerifiedDomain(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		f.orgProvider(t, f.admB, f.tenB, "acme-sso", nil)
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/signin.ssoOnly", map[string]any{"value": true}).want(t, 200, "")
		for _, email := range []string{"ceo@othercorp.example", "x@gmail.com", "x@mail.acme.example", "x@beta.example"} {
			f.admB.do("POST", adminUsers, map[string]any{"username": "m1", "email": email}).want(t, 400, "email_domain_not_verified")
		}
		if n := rowCount(t, a, `SELECT COUNT(*) FROM users WHERE username = 'm1'`); n != 0 {
			t.Fatal("an account was left")
		}
		f.admB.do("POST", adminUsers, map[string]any{"username": "m1", "email": "m1@ACME.example"}).want(t, 201, "")
	})
}

// With the site's own ssoOnly the default tenant's people are matched by no
// domain: they sign in through the SSO themselves, under the registration mode.
func TestAnSSOAccountIsNotMadeInTheDefaultTenant(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newSSOFx(t, a, idp)
		f.root.do("POST", "/api/admin/providers", idp.providerBody("acme-sso", map[string]any{"enabled": true})).want(t, 201, "")
		f.root.putSetting("signin.ssoOnly", true).want(t, 200, "")
		f.root.do("POST", adminUsers, map[string]any{"username": "p1", "email": "p1@acme.example"}).want(t, 400, "sso_default_tenant")
		// An administrator of the server keeps the password.
		f.root.do("POST", adminUsers, map[string]any{"username": "p2", "email": "p2@acme.example", "role": "admin", "siteAdmin": true}).want(t, 201, "")
	})
}

// An administrator of the default tenant made by a site administrator is a
// site administrator: the request must say so.
func TestASiteAdministratorIsMadeOnlyOnPurpose(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		root.do("POST", adminUsers, map[string]any{"username": "d1", "email": "d1@mail.test", "role": "admin"}).want(t, 400, "site_admin_explicit")
		r := root.do("POST", adminUsers, map[string]any{"username": "d1", "email": "d1@mail.test", "role": "admin", "siteAdmin": true}).want(t, 201, "")
		if r.user()["siteAdmin"] != true {
			t.Fatal(r.raw)
		}
		r = root.do("POST", adminUsers, map[string]any{"username": "d2", "email": "d2@mail.test", "role": "admin", "siteAdmin": false}).want(t, 201, "")
		if r.user()["siteAdmin"] != false {
			t.Fatal(r.raw)
		}
	})
}

// The account and what lets the person in are made together: a failure after
// the account's row leaves no account behind.
func TestAnAccountAndItsFirstWayInAreOneTransaction(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		a.setup("root", pw)
		_, err := a.auth.CreateUser(context.Background(), auth.CreateParams{Username: "half", Role: "member",
			AfterCreate: func(ctx context.Context, q store.Q, u store.User) error { return errors.New("boom") }})
		if err == nil {
			t.Fatal("no error")
		}
		if n := rowCount(t, a, `SELECT COUNT(*) FROM users WHERE username = 'half'`); n != 0 {
			t.Fatal("a half-made account")
		}
		if _, err := a.auth.CreateUser(context.Background(), auth.CreateParams{Username: "half", Role: "member"}); err != nil {
			t.Fatalf("the name is not free: %v", err)
		}
	})
}

// "Forgot password" for an account an administrator made, whose address a site
// administrator has vouched for and whose person has not used the link yet,
// sends a reset link, not "sign in with your provider".
func TestForgotPasswordOfAnAdminMadeAccountSendsAResetLink(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		root := a.setup("root", pw)
		enableMail(t, root, srv)
		r := root.do("POST", adminUsers, map[string]any{"username": "newbie", "email": "newbie@mail.test"}).want(t, 201, "")
		srv.Wait(1)
		root.do("PATCH", "/api/users/"+r.user()["id"].(string), map[string]any{"emailVerified": true}).want(t, 200, "")
		n := len(srv.Messages())
		a.newClient("203.0.113.9").do("POST", "/api/password/forgot", map[string]any{"login": "newbie"}).want(t, 202, "")
		srv.Wait(n + 1)
		m := lastTo(t, srv, "newbie@mail.test")
		if m.Subject == "" || !contains(m.Text, "/reset-password#") {
			t.Fatalf("not a reset link: %s\n%s", m.Subject, m.Text)
		}
		a.newClient("203.0.113.9").do("POST", "/api/password/reset", map[string]any{"token": m.Token(t), "password": "a brand new password"}).want(t, 204, "")
	})
}
