package api_test

import (
	"strings"
	"testing"
	"time"

	"petalbook.app/server/internal/mail/mailtest"
)

// Organisations people create themselves (docs/design/3-6-c.md, 2).

// orgSetup is a server with mail and tenancy on and the switch on.
func orgSetup(t *testing.T, a *acc) (root *client, srv *mailtest.Server) {
	t.Helper()
	srv = fakeMail(t)
	root = a.setup("root", pw)
	enableMail(t, root, srv)
	root.putSetting("features.tenancy", true).want(t, 200, "")
	root.putSetting("features.orgCreation", true).want(t, 200, "")
	return root, srv
}

func regOrg(c *client, name, slug string, body map[string]any) result {
	b := map[string]any{"org": map[string]any{"name": name, "slug": slug}}
	for k, v := range body {
		b[k] = v
	}
	return reg(c, b)
}

func TestOrganisationCreationIsOffUntilTheSiteTurnsItOn(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		root := a.setup("root", pw)
		anon := a.newClient("")
		if g := anon.do("GET", "/api/register", nil).want(t, 200, ""); g.body["open"] != false || g.body["orgCreation"].(map[string]any)["open"] != false {
			t.Fatal(g.raw)
		}
		regOrg(anon, "Acme", "acme", nil).want(t, 403, "registration_closed")
		// It needs tenants, and mail.
		root.putSetting("features.orgCreation", true).want(t, 409, "needs_tenancy")
		root.putSetting("features.tenancy", true).want(t, 200, "")
		root.putSetting("features.orgCreation", true).want(t, 409, "needs_mail")
		enableMail(t, root, srv)
		root.putSetting("features.orgCreation", true).want(t, 200, "")
		g := anon.do("GET", "/api/register", nil).want(t, 200, "")
		if g.body["open"] != false || g.body["orgCreation"].(map[string]any)["open"] != true {
			t.Fatal(g.raw)
		}
		if p := anon.do("GET", "/api/auth/providers", nil).body; p["orgCreation"] != true || p["register"] != false {
			t.Fatal(p)
		}
		// A plan that does not exist is refused.
		root.putSetting("orgCreation.plan", "nope").want(t, 400, "invalid")
		// Off again: the pending ones are cancelled and a link no longer works.
		regOrg(anon, "Acme", "acme", nil).want(t, 202, "")
		tok := srv.Wait(1)[0].Token(t)
		root.putSetting("features.orgCreation", false).want(t, 200, "")
		if rowCount(t, a, `SELECT COUNT(*) FROM registrations`) != 0 {
			t.Fatal("switching it off keeps the pending registrations")
		}
		anon.do("POST", "/api/register/confirm", map[string]any{"token": tok}).want(t, 404, "not_found")
	})
}

func TestAPersonRegistersAnOrganisationAndIsItsAdministrator(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root, srv := orgSetup(t, a)
		root.do("POST", "/api/admin/plans", map[string]any{"slug": "free", "name": "Free"}).want(t, 201, "")
		root.putSetting("orgCreation.plan", "free").want(t, 200, "")
		anon := a.newClient("")
		// The slug is checked like an administrator's, before any address is looked at.
		regOrg(anon, "Acme", "Bad Slug", nil).want(t, 400, "invalid")
		regOrg(anon, " ", "acme", nil).want(t, 400, "invalid")
		root.do("POST", "/api/admin/tenants", map[string]any{"slug": "taken", "name": "Taken"}).want(t, 201, "")
		regOrg(anon, "Taken", "taken", nil).want(t, 409, "slug_taken")
		regOrg(anon, "Acme", "acme", nil).want(t, 202, "")
		if r := regOrg(anon, "Other", "acme", map[string]any{"username": "other", "email": "other@example.test"}); r.code != 409 || r.body["error"] != "slug_taken" {
			t.Fatalf("a pending registration holds the slug: %s", r.raw)
		}
		// Nothing exists until the link is followed.
		if rowCount(t, a, `SELECT COUNT(*) FROM tenants WHERE slug = 'acme'`) != 0 {
			t.Fatal("the tenant was made before the link was followed")
		}
		m := srv.Wait(1)[0]
		if !strings.Contains(m.Text, "Acme") {
			t.Fatalf("the message does not name the organization: %q", m.Text)
		}
		r := anon.do("POST", "/api/register/confirm", map[string]any{"token": m.Token(t)}).want(t, 201, "")
		tn := r.body["tenant"].(map[string]any)
		u := r.user()
		if tn["slug"] != "acme" || tn["name"] != "Acme" || tn["renamed"] != false {
			t.Fatal(r.raw)
		}
		if u["role"] != "admin" || u["siteAdmin"] != false || u["tenant"].(map[string]any)["slug"] != "acme" {
			t.Fatalf("the person is the tenant's administrator and never the site's: %s", r.raw)
		}
		anon.personalSpace()
		// The tenant says who made it and how, and has the plan.
		list := root.do("GET", "/api/admin/tenants?origin=self", nil).want(t, 200, "")
		items := list.body["items"].([]any)
		if len(items) != 1 {
			t.Fatal(list.raw)
		}
		row := items[0].(map[string]any)
		if row["slug"] != "acme" || row["origin"] != "self" || row["createdBy"] != u["id"] || row["plan"].(map[string]any)["slug"] != "free" {
			t.Fatal(row)
		}
		if all := root.do("GET", "/api/admin/tenants", nil).want(t, 200, ""); len(all.body["items"].([]any)) != 3 {
			t.Fatal(all.raw)
		}
		root.do("GET", "/api/admin/tenants?origin=bogus", nil).want(t, 400, "invalid")
		// It is not a site route: the new administrator manages their own tenant only.
		anon.do("GET", "/api/admin/tenants", nil).want(t, 403, "forbidden")
		anon.do("GET", "/api/users", nil).want(t, 200, "")
		if len(auditKinds(t, root, "tenant_created")) != 2 {
			t.Fatal("tenant_created is audited for the site administrator's tenant and for this one")
		}
		// No registration is left, and the slug is no longer pending.
		if rowCount(t, a, `SELECT COUNT(*) FROM registrations`) != 0 {
			t.Fatal("a used registration stays")
		}
		a.newClient("").do("POST", "/api/login", map[string]any{"username": "tang", "password": pw}).want(t, 200, "")
	})
}

func TestASlugTakenBetweenTheRegistrationAndTheLinkGetsANumber(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root, srv := orgSetup(t, a)
		anon := a.newClient("")
		regOrg(anon, "Acme", "acme", nil).want(t, 202, "")
		tok := srv.Wait(1)[0].Token(t)
		root.do("POST", "/api/admin/tenants", map[string]any{"slug": "acme", "name": "Acme by admin"}).want(t, 201, "")
		r := anon.do("POST", "/api/register/confirm", map[string]any{"token": tok}).want(t, 201, "")
		tn := r.body["tenant"].(map[string]any)
		if tn["slug"] != "acme-2" || tn["renamed"] != true {
			t.Fatal(r.raw)
		}
	})
}

func TestTheCapsAnswerAcceptedAndSayNoOrganizationWasMade(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root, srv := orgSetup(t, a)
		// A person may make none: 202, no registration, a message that says so.
		root.putSetting("orgCreation.perPerson", 0).want(t, 200, "")
		before := regOrg(a.newClient("203.0.113.1"), "Acme", "acme", nil).want(t, 202, "")
		m := srv.Wait(1)[0]
		if m.To != "tang@example.test" || !strings.Contains(m.Subject, "not made") {
			t.Fatalf("%+v", m)
		}
		if rowCount(t, a, `SELECT COUNT(*) FROM registrations`) != 0 {
			t.Fatal("a refused organization holds a username or a slug")
		}
		// The answer is the one of a registration that went ahead.
		root.putSetting("orgCreation.perPerson", 3).want(t, 200, "")
		if ok := regOrg(a.newClient("203.0.113.2"), "Acme", "acme", map[string]any{"username": "u2", "email": "u2@example.test"}).want(t, 202, ""); ok.raw != before.raw {
			t.Fatalf("%s != %s", ok.raw, before.raw)
		}
		// The hour's cap is the site's and is unset by default.
		root.putSetting("orgCreation.perHour", 1).want(t, 200, "")
		regOrg(a.newClient("203.0.113.3"), "Two", "two", map[string]any{"username": "u3", "email": "u3@example.test"}).want(t, 202, "")
		regOrg(a.newClient("203.0.113.4"), "Three", "three", map[string]any{"username": "u4", "email": "u4@example.test"}).want(t, 202, "")
		if rowCount(t, a, `SELECT COUNT(*) FROM registrations WHERE org_slug <> ''`) != 2 {
			t.Fatal("the hour's cap lets one more through")
		}
		a.clock.Advance(61 * time.Minute)
		regOrg(a.newClient("203.0.113.5"), "Three", "three", map[string]any{"username": "u4", "email": "u4@example.test"}).want(t, 202, "")
		if rowCount(t, a, `SELECT COUNT(*) FROM registrations WHERE org_slug <> ''`) != 3 {
			t.Fatal("the cap does not end with the hour")
		}
	})
}

func TestAnAddressThatHasAnAccountMakesNoOrganization(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		_, srv := orgSetup(t, a)
		verifiedUser(t, a, "owner", "owner@example.test")
		anon := a.newClient("")
		regOrg(anon, "Acme", "acme", map[string]any{"username": "newname", "email": "owner@example.test"}).want(t, 202, "")
		if m := srv.Wait(1)[0]; !strings.Contains(m.Subject, "already have an account") {
			t.Fatalf("%+v", m)
		}
		if rowCount(t, a, `SELECT COUNT(*) FROM tenants WHERE slug = 'acme'`) != 0 {
			t.Fatal("a tenant was made")
		}
	})
}

func TestTheCapIsCheckedAgainWhenTheLinkIsFollowedAndNothingIsHalfMade(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root, srv := orgSetup(t, a)
		anon := a.newClient("")
		regOrg(anon, "Acme", "acme", nil).want(t, 202, "")
		tok := srv.Wait(1)[0].Token(t)
		root.putSetting("orgCreation.perPerson", 0).want(t, 200, "")
		anon.do("POST", "/api/register/confirm", map[string]any{"token": tok}).want(t, 403, "org_limit")
		// Neither the tenant nor the account was made, and the link still stands.
		if rowCount(t, a, `SELECT COUNT(*) FROM tenants WHERE slug = 'acme'`) != 0 || !mustNoUser(a, "tang") {
			t.Fatal("half of it was made")
		}
		root.putSetting("orgCreation.perPerson", 3).want(t, 200, "")
		anon.do("POST", "/api/register/confirm", map[string]any{"token": tok}).want(t, 201, "")
	})
}
