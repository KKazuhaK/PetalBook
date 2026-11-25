package api_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/api"
	"github.com/KKazuhaK/PetalBook/internal/auth"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

func reg(c *client, body map[string]any) result {
	b := map[string]any{"username": "tang", "name": "Tang", "email": "tang@example.test", "password": pw, "lang": "en"}
	for k, v := range body {
		b[k] = v
	}
	return c.do("POST", "/api/register", b)
}

func TestRegistrationStartsToEndWithTheLinkInTheMail(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		root := a.setup("root", pw)
		root.do("PUT", "/api/admin/settings/legal.termsUrl", map[string]any{"value": "https://notes.example.test/terms"}).want(t, 200, "")
		openRegistration(t, root, srv)
		anon := a.newClient("")
		g := anon.do("GET", "/api/register", nil).want(t, 200, "")
		if g.body["open"] != true || g.body["termsUrl"] != "https://notes.example.test/terms" || g.body["password"].(map[string]any)["minLength"] != float64(8) ||
			g.resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatal(g.raw, g.resp.Header)
		}
		if p := anon.do("GET", "/api/auth/providers", nil).body; p["register"] != true || p["forgot"] != true {
			t.Fatal(p)
		}
		reg(anon, map[string]any{"acceptTerms": true}).want(t, 202, "")
		if r := reg(anon, map[string]any{"acceptTerms": true, "username": "tang"}); r.code != 409 || r.body["error"] != "exists" {
			t.Fatalf("a pending registration holds its username: %s", r.raw)
		}
		// Nothing exists yet: no account, and the password is a hash.
		if _, err := a.auth.Users(context.Background(), store.AllTenants()); err != nil {
			t.Fatal(err)
		}
		if mustNoUser(a, "tang") == false {
			t.Fatal("the account was made before the link was followed")
		}
		m := srv.Wait(1)[0]
		tok := m.Token(t)
		if m.To != "tang@example.test" || !contains(m.Text, "https://notes.example.test/register/confirm#"+tok) || strings.Contains(m.Text, "?") && strings.Contains(m.Text, "token=") {
			t.Fatalf("%+v", m)
		}
		if rowCount(t, a, `SELECT COUNT(*) FROM registrations WHERE token_hash = ?`, tok) != 0 || rowCount(t, a, `SELECT COUNT(*) FROM registrations WHERE password_hash LIKE '$argon2id$%'`) != 1 {
			t.Fatal("the token or the password is stored in the clear")
		}
		// A wrong token is not found; the right one makes the account and a session.
		anon.do("POST", "/api/register/confirm", map[string]any{"token": "nope"}).want(t, 404, "not_found")
		r := anon.do("POST", "/api/register/confirm", map[string]any{"token": tok}).want(t, 201, "")
		u := r.user()
		if u["username"] != "tang" || u["emailVerified"] != true || u["role"] != "member" || u["email"] != "tang@example.test" {
			t.Fatal(r.raw)
		}
		anon.do("GET", "/api/account", nil).want(t, 200, "")
		anon.personalSpace()
		anon.do("POST", "/api/register/confirm", map[string]any{"token": tok}).want(t, 404, "not_found")
		// The acceptance is recorded: when, and which addresses.
		var terms struct{ At, TermsURL, PrivacyURL string }
		raw, err := store.GetTermsAccepted(context.Background(), a.st.Q(), mustUserID(t, a, "tang"))
		if err != nil || json.Unmarshal([]byte(raw), &terms) != nil || terms.TermsURL != "https://notes.example.test/terms" || terms.At == "" {
			t.Fatalf("%q %v", raw, err)
		}
		if rowCount(t, a, `SELECT COUNT(*) FROM registrations`) != 0 {
			t.Fatal("a used registration stays")
		}
		if len(auditKinds(t, root, "registered")) != 1 {
			t.Fatal("registered is not audited")
		}
		// The person signs in with the password they chose.
		a.newClient("").do("POST", "/api/login", map[string]any{"username": "tang", "password": pw}).want(t, 200, "")
	})
}

func mustNoUser(a *acc, name string) bool {
	us, _ := a.auth.Users(context.Background(), store.AllTenants())
	for _, u := range us {
		if u.Username == name {
			return false
		}
	}
	return true
}

func TestRegistrationRefusalsComeInTheContractsOrder(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		root := a.setup("root", pw)
		anon := a.newClient("")
		// Closed: 403 before anything else, even with garbage fields.
		reg(anon, map[string]any{"username": "!"}).want(t, 403, "registration_closed")
		openRegistration(t, root, srv)
		root.do("PUT", "/api/admin/security", map[string]any{"captcha": map[string]any{"contexts": map[string]any{"register": map[string]any{"on": true, "when": "always"}}}}).want(t, 200, "")
		// The captcha comes before the fields.
		reg(anon, map[string]any{"username": "!"}).want(t, 400, "captcha_failed")
		root.do("PUT", "/api/admin/security", map[string]any{"captcha": map[string]any{"contexts": map[string]any{"register": map[string]any{"on": false}}}}).want(t, 200, "")
		for name, body := range map[string]map[string]any{
			"username": {"username": "!"}, "name": {"name": strings.Repeat("n", 81)}, "email": {"email": "not-an-address"}, "name-in-address": {"email": "Tang <t@example.test>"},
			"long email": {"email": strings.Repeat("a", 250) + "@example.test"}, "password": {"password": "short"}, "no email": {"email": ""},
		} {
			if r := reg(anon, body); r.code != 400 || r.body["error"] != "invalid" {
				t.Fatalf("%s: %s", name, r.raw)
			}
		}
		root.do("PUT", "/api/admin/settings/registration.domains", map[string]any{"value": []string{"example.test"}}).want(t, 200, "")
		reg(anon, map[string]any{"email": "tang@other.test"}).want(t, 400, "email_domain_not_allowed")
		reg(anon, map[string]any{"email": "tang@lookalike-example.test"}).want(t, 400, "email_domain_not_allowed")
		reg(anon, map[string]any{"email": "tang@mail.example.test"}).want(t, 202, "") // a subdomain is allowed
		root.do("PUT", "/api/admin/settings/legal.privacyUrl", map[string]any{"value": "https://notes.example.test/privacy"}).want(t, 200, "")
		reg(anon, map[string]any{"username": "other"}).want(t, 400, "terms_required")
		reg(anon, map[string]any{"username": "other", "acceptTerms": false}).want(t, 400, "terms_required")
		reg(anon, map[string]any{"username": "other", "acceptTerms": true}).want(t, 202, "")
		verifiedUser(t, a, "taken", "taken@example.test")
		reg(anon, map[string]any{"username": "Taken", "acceptTerms": true, "email": "x@example.test"}).want(t, 409, "exists")
	})
}

func TestRegistrationAnswersTheSameWhetherOrNotAMessageGoes(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		root := a.setup("root", pw)
		openRegistration(t, root, srv)
		verifiedUser(t, a, "owner", "owner@example.test")
		anon := a.newClient("")
		// An address that already has an account: the same 202, no registration,
		// and a message that says so with a link to reset the password.
		r1 := reg(anon, map[string]any{"username": "newname", "email": "owner@example.test"}).want(t, 202, "")
		r2 := reg(anon, map[string]any{"username": "another", "email": "nobody@example.test"}).want(t, 202, "")
		if r1.raw != r2.raw {
			t.Fatalf("the answers differ: %s / %s", r1.raw, r2.raw)
		}
		srv.Wait(2)
		dup := lastTo(t, srv, "owner@example.test")
		if !contains(dup.Text, "already") || !contains(dup.Text, "/reset-password#") {
			t.Fatalf("%s", dup.Text)
		}
		// The username is held all the same (a decoy whose link is never sent).
		if rowCount(t, a, `SELECT COUNT(*) FROM registrations WHERE username = 'newname'`) != 1 || !mustNoUser(a, "newname") {
			t.Fatal("the username is not held, or an account was made, for an address that has an account")
		}
		// The 3-an-hour cap of one address: the fourth message is not sent, and not said.
		for i := 0; i < 3; i++ {
			reg(a.newClient("203.0.113."+string(rune('1'+i))), map[string]any{"username": "u" + string(rune('a'+i)), "email": "busy@example.test"}).want(t, 202, "")
		}
		srv.Wait(5)
		r := reg(a.newClient("203.0.113.9"), map[string]any{"username": "ud", "email": "busy@example.test"}).want(t, 202, "")
		if r.raw != r1.raw {
			t.Fatal("the answer differs when the cap is hit")
		}
		settle()
		if countTo(srv, "busy@example.test") != 3 {
			t.Fatalf("messages to the address: %d", countTo(srv, "busy@example.test"))
		}
		if rowCount(t, a, `SELECT COUNT(*) FROM registrations WHERE username = 'ud'`) != 0 {
			t.Fatal("a registration that no message carries was stored")
		}
		// Five messages caused by one caller's address.
		c := a.newClient("198.51.100.77")
		for i := 0; i < 6; i++ {
			reg(c, map[string]any{"username": "v" + string(rune('a'+i)), "email": "v" + string(rune('a'+i)) + "@example.test"}).want(t, 202, "")
		}
		srv.Wait(10) // 2 + 3 earlier, and 5 from this caller; the sixth is never sent
		settle()
		sent := 0
		for _, m := range srv.Messages() {
			if strings.HasPrefix(m.To, "v") {
				sent++
			}
		}
		if sent != 5 {
			t.Fatalf("one caller caused %d messages, want 5", sent)
		}
	})
}

func TestRegistrationsPerHourAndPendingAreBounded(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		root := a.setup("root", pw)
		openRegistration(t, root, srv)
		root.do("PUT", "/api/admin/settings/registration.perHour", map[string]any{"value": 2}).want(t, 200, "")
		reg(a.newClient("203.0.113.1"), map[string]any{"username": "r1", "email": "r1@example.test"}).want(t, 202, "")
		reg(a.newClient("203.0.113.2"), map[string]any{"username": "r2", "email": "r2@example.test"}).want(t, 202, "")
		r := reg(a.newClient("203.0.113.3"), map[string]any{"username": "r3", "email": "r3@example.test"}).want(t, 429, "rate_limited")
		if r.resp.Header.Get("Retry-After") == "" {
			t.Fatal("no Retry-After")
		}
		a.clock.Advance(61 * time.Minute)
		reg(a.newClient("203.0.113.3"), map[string]any{"username": "r3", "email": "r3@example.test"}).want(t, 202, "")
	})
}

func TestClosingRegistrationCancelsThePendingOnes(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		root := a.setup("root", pw)
		openRegistration(t, root, srv)
		anon := a.newClient("")
		reg(anon, nil).want(t, 202, "")
		tok := srv.Wait(1)[0].Token(t)
		root.do("PUT", "/api/admin/settings/registration.open", map[string]any{"value": false}).want(t, 200, "")
		if rowCount(t, a, `SELECT COUNT(*) FROM registrations`) != 0 {
			t.Fatal("closing registration left a pending one")
		}
		anon.do("POST", "/api/register/confirm", map[string]any{"token": tok}).want(t, 404, "not_found")
		// Reopening does not bring it back.
		root.do("PUT", "/api/admin/settings/registration.open", map[string]any{"value": true}).want(t, 200, "")
		anon.do("POST", "/api/register/confirm", map[string]any{"token": tok}).want(t, 404, "not_found")
		// Switching mail off closes it in effect, and cancels what is pending.
		reg(anon, map[string]any{"username": "second", "email": "second@example.test"}).want(t, 202, "")
		tok2 := srv.Wait(2)[1].Token(t)
		root.do("PUT", "/api/admin/mail", map[string]any{"enabled": false}).want(t, 200, "")
		if g := anon.do("GET", "/api/register", nil).want(t, 200, ""); g.body["open"] != false {
			t.Fatal(g.raw)
		}
		reg(anon, map[string]any{"username": "third"}).want(t, 403, "registration_closed")
		anon.do("POST", "/api/register/confirm", map[string]any{"token": tok2}).want(t, 404, "not_found")
		if rowCount(t, a, `SELECT COUNT(*) FROM registrations`) != 0 {
			t.Fatal("pending registrations survived switching mail off")
		}
	})
}

func TestAnExpiredRegistrationFreesItsUsernameAndItsLink(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		root := a.setup("root", pw)
		openRegistration(t, root, srv)
		anon := a.newClient("")
		reg(anon, nil).want(t, 202, "")
		tok := srv.Wait(1)[0].Token(t)
		a.clock.Advance(25 * time.Hour)
		anon.do("POST", "/api/register/confirm", map[string]any{"token": tok}).want(t, 404, "not_found")
		reg(anon, map[string]any{"email": "tang2@example.test"}).want(t, 202, "")
	})
}

func TestRegistrationNeedsMailAndSSOOnlyExcludesIt(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		root := a.setup("root", pw)
		// Opening: no mail, no registration. Locked at the site for tenants, by default.
		root.do("PUT", "/api/admin/settings/registration.open", map[string]any{"value": true}).want(t, 409, "mail_not_configured")
		enableMail(t, root, srv)
		// ssoOnly stored on (with a provider so it can be set): opening is refused.
		a.auth.UpdateSecurity(context.Background(), "", "", mustPatch(t, `{"login":{"ssoOnly":true}}`))
		root.do("PUT", "/api/admin/settings/registration.open", map[string]any{"value": true}).want(t, 409, "sso_only_with_registration")
		a.auth.UpdateSecurity(context.Background(), "", "", mustPatch(t, `{"login":{"ssoOnly":false}}`))
		root.do("PUT", "/api/admin/settings/registration.open", map[string]any{"value": true}).want(t, 200, "")
		// The other direction: with registration open, ssoOnly cannot be switched on.
		root.do("PUT", "/api/admin/security", map[string]any{"login": map[string]any{"ssoOnly": true}}).want(t, 400, "sso_only_with_registration")
		root.do("PUT", "/api/admin/settings/registration.open", map[string]any{"value": false}).want(t, 200, "")
		root.do("PUT", "/api/admin/security", map[string]any{"login": map[string]any{"ssoOnly": true}}).want(t, 200, "")
	})
}

func mustPatch(t *testing.T, raw string) auth.SecurityPatch {
	var p auth.SecurityPatch
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestUnlockingRegistrationWhileATenantHasTrueStoredIsChecked(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		f := newTenFx(t, a)
		enableMail(t, f.root, srv)
		// Open for tenant B at the site's pleasure, then lose the mail.
		f.root.do("PUT", "/api/admin/settings/registration.open", map[string]any{"lower": map[string]any{"mode": "allow"}}).want(t, 200, "")
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/registration.open", map[string]any{"value": true}).want(t, 200, "")
		a.auth.UpdateSecurity(context.Background(), "", "", mustPatch(t, `{"login":{"ssoOnly":false}}`))
		f.root.do("PUT", "/api/admin/settings/registration.open", map[string]any{"lower": map[string]any{"mode": "lock"}}).want(t, 200, "")
		f.root.do("PUT", "/api/admin/mail", map[string]any{"enabled": false}).want(t, 200, "")
		// Unlocking now would let the stored true take effect with no mail.
		f.root.do("PUT", "/api/admin/settings/registration.open", map[string]any{"lower": map[string]any{"mode": "allow"}}).want(t, 409, "mail_not_configured")
		enableMail(t, f.root, srv)
		f.root.do("PUT", "/api/admin/settings/registration.open", map[string]any{"lower": map[string]any{"mode": "allow"}}).want(t, 200, "")
		// A tenant registers in its own tenant, named by slug.
		anon := a.newClient("")
		reg(anon, map[string]any{"tenant": "acme", "username": "bee", "email": "bee@example.test"}).want(t, 202, "")
		tok := srv.Wait(1)[0].Token(t)
		anon.do("POST", "/api/register/confirm", map[string]any{"token": tok}).want(t, 201, "")
		us, _ := a.auth.Users(context.Background(), store.InTenant(f.tenB))
		found := false
		for _, u := range us {
			found = found || u.Username == "bee"
		}
		if !found {
			t.Fatal("the account was not made in the tenant registered for")
		}
		// The default tenant is not open: 403 there, and 404 for an unknown or disabled slug.
		reg(anon, map[string]any{"username": "cee"}).want(t, 403, "registration_closed")
		reg(anon, map[string]any{"tenant": "nope"}).want(t, 404, "not_found")
		anon.do("GET", "/api/register?tenant=nope", nil).want(t, 404, "not_found")
		anon.do("GET", "/api/auth/providers?tenant=nope", nil).want(t, 404, "not_found")
		if g := anon.do("GET", "/api/register?tenant=acme", nil).want(t, 200, ""); g.body["open"] != true || g.body["tenant"].(map[string]any)["slug"] != "acme" {
			t.Fatal(g.raw)
		}
		if p := anon.do("GET", "/api/auth/providers?tenant=acme", nil).body; p["register"] != true {
			t.Fatal(p)
		}
		if p := anon.do("GET", "/api/auth/providers", nil).body; p["register"] != false || p["forgot"] != true {
			t.Fatal(p)
		}
	})
}

func TestMailRoutesAreAccountsOnly(t *testing.T) {
	t.Parallel()
	want := map[string]bool{"/api/admin/mail": true, "/api/admin/mail/test": true, "/api/register": true, "/api/register/confirm": true, "/api/account/email/verify": true,
		"/api/email/confirm": true, "/api/password/forgot": true, "/api/password/reset/check": true, "/api/password/reset": true, "/api/users/{id}/password-reset": true}
	seen := map[string]bool{}
	for _, r := range api.Routes() {
		if want[r.Pattern] {
			seen[r.Pattern] = true
			if !r.AccountsOnly {
				t.Errorf("%s %s is not accounts-only: single-user mode has no mail or registration", r.Method, r.Pattern)
			}
		}
	}
	for p := range want {
		if !seen[p] {
			t.Errorf("no route %s", p)
		}
	}
}
