package api_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/store"
)

func TestAPersonVerifiesTheirAddressWithALinkThatWorksOnAnotherDevice(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		root := a.setup("root", pw)
		enableMail(t, root, srv)
		c := a.addUser("tang", pw, "member")
		c.do("POST", "/api/account/email/verify", map[string]any{}).want(t, 400, "invalid")
		c.do("PATCH", "/api/account", map[string]any{"email": "tang@example.test"}).want(t, 200, "")
		c.do("POST", "/api/account/email/verify", map[string]any{"lang": "zh-Hans"}).want(t, 202, "")
		m := srv.Wait(1)[0]
		tok := m.Token(t)
		if m.To != "tang@example.test" || !strings.Contains(m.Text, "https://notes.example.test/verify-email#"+tok) || !strings.Contains(m.Subject, "确认") {
			t.Fatalf("%+v", m)
		}
		if rowCount(t, a, `SELECT COUNT(*) FROM email_tokens WHERE hash = ?`, tok) != 0 {
			t.Fatal("the token is stored in the clear")
		}
		// Another device, no session.
		other := a.newClient("")
		other.do("POST", "/api/email/confirm", map[string]any{"token": "wrong"}).want(t, 404, "not_found")
		other.do("POST", "/api/email/confirm", map[string]any{"token": tok}).want(t, 204, "")
		if c.do("GET", "/api/account", nil).user()["emailVerified"] != true {
			t.Fatal("not verified")
		}
		other.do("POST", "/api/email/confirm", map[string]any{"token": tok}).want(t, 404, "not_found")
		c.do("POST", "/api/account/email/verify", map[string]any{}).want(t, 409, "email_verified")
		if len(auditKinds(t, root, "email_verified")) != 1 {
			t.Fatal("email_verified is not audited")
		}
	})
}

func TestAVerificationLinkOnlyConfirmsTheAddressItWasSentTo(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		root := a.setup("root", pw)
		enableMail(t, root, srv)
		c := a.addUser("tang", pw, "member")
		c.do("PATCH", "/api/account", map[string]any{"email": "old@example.test"}).want(t, 200, "")
		c.do("POST", "/api/account/email/verify", map[string]any{}).want(t, 202, "")
		tok := srv.Wait(1)[0].Token(t)
		c.do("PATCH", "/api/account", map[string]any{"email": "new@example.test"}).want(t, 200, "")
		a.newClient("").do("POST", "/api/email/confirm", map[string]any{"token": tok}).want(t, 404, "not_found")
		if c.do("GET", "/api/account", nil).user()["emailVerified"] != false {
			t.Fatal("the old link verified the new address")
		}
	})
}

func TestVerificationIsLimitedToThreeAnHourAndAskingAgainReplacesTheLink(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		root := a.setup("root", pw)
		enableMail(t, root, srv)
		c := a.addUser("tang", pw, "member")
		c.do("PATCH", "/api/account", map[string]any{"email": "tang@example.test"}).want(t, 200, "")
		for i := 0; i < 3; i++ {
			c.do("POST", "/api/account/email/verify", map[string]any{}).want(t, 202, "")
		}
		r := c.do("POST", "/api/account/email/verify", map[string]any{}).want(t, 429, "rate_limited")
		if r.resp.Header.Get("Retry-After") == "" {
			t.Fatal("no Retry-After")
		}
		ms := srv.Wait(3)
		first, last := ms[0].Token(t), ms[2].Token(t)
		a.newClient("").do("POST", "/api/email/confirm", map[string]any{"token": first}).want(t, 404, "not_found")
		a.newClient("").do("POST", "/api/email/confirm", map[string]any{"token": last}).want(t, 204, "")
		a.clock.Advance(61 * time.Minute)
	})
}

func TestVerifyNeedsMail(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		a.setup("root", pw)
		c := a.addUser("tang", pw, "member")
		c.do("PATCH", "/api/account", map[string]any{"email": "tang@example.test"}).want(t, 200, "")
		c.do("POST", "/api/account/email/verify", map[string]any{}).want(t, 409, "mail_not_configured")
	})
}

func TestChangingAVerifiedAddressTellsTheOldOneWithNoLink(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		root := a.setup("root", pw)
		enableMail(t, root, srv)
		verifiedUser(t, a, "tang", "old@example.test")
		c := a.newClient("")
		c.do("POST", "/api/login", map[string]any{"username": "tang", "password": pw}).want(t, 200, "")
		c.do("PATCH", "/api/account", map[string]any{"email": "new@example.test"}).want(t, 200, "")
		m := srv.Wait(1)[0]
		if m.To != "old@example.test" || strings.Contains(m.Text, "http") || strings.Contains(m.HTML, "href") {
			t.Fatalf("%+v", m)
		}
		if c.do("GET", "/api/account", nil).user()["emailVerified"] != false {
			t.Fatal("the new address is verified")
		}
		// An address that was never verified is not told; a name change tells nobody.
		c.do("PATCH", "/api/account", map[string]any{"email": "third@example.test"}).want(t, 200, "")
		c.do("PATCH", "/api/account", map[string]any{"name": "Tang Two"}).want(t, 200, "")
		settle()
		if len(srv.Messages()) != 1 {
			t.Fatalf("%d messages", len(srv.Messages()))
		}
	})
}

// The person's own verification clears the mark of a tenant administrator's.
func TestTheLinkClearsAMarkOnlyATenantAdministratorMade(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		root := a.setup("root", pw)
		enableMail(t, root, srv)
		c := a.addUser("tang", pw, "member")
		c.do("PATCH", "/api/account", map[string]any{"email": "tang@example.test"}).want(t, 200, "")
		uid := mustUserID(t, a, "tang")
		ctx := context.Background()
		if err := store.SetEmail(ctx, a.st.Q(), uid, "tang@example.test", true, store.VerifiedByTenantAdmin); err != nil {
			t.Fatal(err)
		}
		// Shown as verified, so the person cannot ask; an administrator unverifies, the person asks and follows the link.
		root.do("PATCH", "/api/users/"+uid, map[string]any{"emailVerified": false}).want(t, 200, "")
		c.do("POST", "/api/account/email/verify", map[string]any{}).want(t, 202, "")
		tok := srv.Wait(1)[0].Token(t)
		if err := store.SetEmail(ctx, a.st.Q(), uid, "tang@example.test", false, store.VerifiedByTenantAdmin); err != nil {
			t.Fatal(err)
		}
		a.newClient("").do("POST", "/api/email/confirm", map[string]any{"token": tok}).want(t, 204, "")
		if by, err := store.EmailVerifiedBy(ctx, a.st.Q(), uid); err != nil || by != "" {
			t.Fatalf("the mark stays after the person verified: %q %v", by, err)
		}
	})
}

// A site administrator's true always counts, even sent twice.
func TestASiteAdministratorsTrueAlwaysClearsTheMark(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		a.addUser("tang", pw, "member")
		uid := mustUserID(t, a, "tang")
		if err := store.SetEmail(context.Background(), a.st.Q(), uid, "tang@example.test", true, store.VerifiedByTenantAdmin); err != nil {
			t.Fatal(err)
		}
		root.do("PATCH", "/api/users/"+uid, map[string]any{"emailVerified": true}).want(t, 200, "")
		if by, _ := store.EmailVerifiedBy(context.Background(), a.st.Q(), uid); by != "" {
			t.Fatalf("the mark stays: %q", by)
		}
	})
}

// --- password reset --------------------------------------------------------------------

func forgot(c *client, login string, extra ...any) result {
	b := map[string]any{"login": login}
	for i := 0; i+1 < len(extra); i += 2 {
		b[extra[i].(string)] = extra[i+1]
	}
	return c.do("POST", "/api/password/forgot", b)
}

func TestAResetLinkSetsANewPasswordOnceAndEndsEverySession(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		root := a.setup("root", pw)
		enableMail(t, root, srv)
		verifiedUser(t, a, "tang", "tang@example.test")
		s1, s2 := a.newClient("198.51.100.1"), a.newClient("198.51.100.2")
		s1.do("POST", "/api/login", map[string]any{"username": "tang", "password": pw}).want(t, 200, "")
		s2.do("POST", "/api/login", map[string]any{"username": "tang", "password": pw}).want(t, 200, "")
		anon := a.newClient("")
		forgot(anon, "tang", "lang", "zh-Hans").want(t, 202, "")
		m := srv.Wait(1)[0]
		tok := m.Token(t)
		if m.To != "tang@example.test" || !strings.Contains(m.Text, "https://notes.example.test/reset-password#"+tok) || !strings.Contains(m.Subject, "重置") {
			t.Fatalf("%+v", m)
		}
		if rowCount(t, a, `SELECT COUNT(*) FROM email_tokens WHERE hash = ?`, tok) != 0 {
			t.Fatal("token stored in the clear")
		}
		r := anon.do("POST", "/api/password/reset/check", map[string]any{"token": tok}).want(t, 200, "")
		if r.body["username"] != "tang" || r.body["expires"] == nil {
			t.Fatal(r.raw)
		}
		anon.do("POST", "/api/password/reset/check", map[string]any{"token": "x"}).want(t, 404, "not_found")
		anon.do("POST", "/api/password/reset", map[string]any{"token": tok, "password": "short"}).want(t, 400, "invalid")
		// A refused password does not use up the link.
		anon.do("POST", "/api/password/reset", map[string]any{"token": tok, "password": "a brand new password"}).want(t, 204, "")
		if len(anon.cookies) != 0 {
			t.Fatal("a reset signs nobody in")
		}
		anon.do("POST", "/api/password/reset", map[string]any{"token": tok, "password": "another new password"}).want(t, 404, "not_found")
		anon.do("POST", "/api/password/reset/check", map[string]any{"token": tok}).want(t, 404, "not_found")
		// Every session ended; the old password is gone; the new one signs in.
		s1.do("GET", "/api/account", nil).want(t, 401, "")
		s2.do("GET", "/api/account", nil).want(t, 401, "")
		a.newClient("").do("POST", "/api/login", map[string]any{"username": "tang", "password": pw}).want(t, 401, "")
		a.newClient("").do("POST", "/api/login", map[string]any{"username": "tang", "password": "a brand new password"}).want(t, 200, "")
		if len(auditKinds(t, root, "password_reset")) != 1 || len(auditKinds(t, root, "password_reset_requested")) != 1 {
			t.Fatal("the reset is not audited")
		}
	})
}

func TestAskingAgainMakesTheEarlierResetLinkUnusable(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		root := a.setup("root", pw)
		enableMail(t, root, srv)
		verifiedUser(t, a, "tang", "tang@example.test")
		anon := a.newClient("")
		forgot(anon, "tang").want(t, 202, "")
		forgot(anon, "tang@example.test").want(t, 202, "") // by address, any case
		ms := srv.Wait(2)
		anon.do("POST", "/api/password/reset", map[string]any{"token": ms[0].Token(t), "password": "a brand new password"}).want(t, 404, "not_found")
		anon.do("POST", "/api/password/reset", map[string]any{"token": ms[1].Token(t), "password": "a brand new password"}).want(t, 204, "")
	})
}

func TestAResetLinkExpiresAfterAnHourAndAPasswordChangeKillsIt(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		root := a.setup("root", pw)
		enableMail(t, root, srv)
		verifiedUser(t, a, "tang", "tang@example.test")
		anon := a.newClient("")
		forgot(anon, "tang").want(t, 202, "")
		tok := srv.Wait(1)[0].Token(t)
		a.clock.Advance(61 * time.Minute)
		anon.do("POST", "/api/password/reset/check", map[string]any{"token": tok}).want(t, 404, "not_found")
		// A fresh link, then the person changes their password by other means.
		forgot(anon, "tang").want(t, 202, "")
		tok2 := srv.Wait(2)[1].Token(t)
		c := a.newClient("")
		c.do("POST", "/api/login", map[string]any{"username": "tang", "password": pw}).want(t, 200, "")
		c.do("POST", "/api/account/password", map[string]any{"current": pw, "new": "changed by the person"}).want(t, 204, "")
		anon.do("POST", "/api/password/reset", map[string]any{"token": tok2, "password": "a brand new password"}).want(t, 404, "not_found")
	})
}

func TestForgotNeverRevealsWhetherAnAccountExists(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		root := a.setup("root", pw)
		enableMail(t, root, srv)
		verifiedUser(t, a, "tang", "tang@example.test")
		a.addUser("noaddr", pw, "member")
		if _, err := a.auth.CreateUser(context.Background(), auth.CreateParams{Username: "unver", Role: "member", Password: pw, Email: "unver@example.test"}); err != nil {
			t.Fatal(err)
		}
		verifiedUser(t, a, "gone", "gone@example.test")
		root.do("PATCH", "/api/users/"+mustUserID(t, a, "gone"), map[string]any{"disabled": true}).want(t, 200, "")
		want := forgot(a.newClient("203.0.113.1"), "tang").want(t, 202, "").raw
		for i, login := range []string{"nobody", "noaddr", "unver", "unver@example.test", "gone", "gone@example.test", "nobody@example.test", "!!", strings.Repeat("a", 200)} {
			r := forgot(a.newClient("203.0.113."+string(rune('2'+i))), login)
			if r.code != 202 || r.raw != want {
				t.Fatalf("%q: %d %s (want %s)", login, r.code, r.raw, want)
			}
		}
		settle()
		if len(srv.Messages()) != 1 {
			t.Fatalf("messages: %d (only the real one)", len(srv.Messages()))
		}
		forgot(a.newClient(""), "").want(t, 400, "invalid")
		forgot(a.newClient(""), "tang", "tenant", "nope").want(t, 404, "not_found")
	})
}

func TestResetMailIsCappedPerAddressAndPerCaller(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		root := a.setup("root", pw)
		enableMail(t, root, srv)
		verifiedUser(t, a, "tang", "tang@example.test")
		for i := 0; i < 5; i++ {
			forgot(a.newClient("203.0.113."+string(rune('1'+i))), "tang").want(t, 202, "")
		}
		srv.Wait(3)
		settle()
		if countTo(srv, "tang@example.test") != 3 {
			t.Fatalf("3 an hour to one address, got %d", countTo(srv, "tang@example.test"))
		}
		a.clock.Advance(61 * time.Minute)
		forgot(a.newClient("203.0.113.9"), "tang").want(t, 202, "")
		srv.Wait(4)
		// One caller's address triggers at most five.
		for i := 0; i < 7; i++ {
			n := string(rune('a' + i))
			verifiedUser(t, a, "p"+n, "p"+n+"@example.test")
			forgot(a.newClient("198.51.100.200"), "p"+n).want(t, 202, "")
		}
		srv.Wait(9) // 4 earlier, and 5 from this caller; the others are never sent
		settle()
		sent := 0
		for _, m := range srv.Messages() {
			if strings.HasPrefix(m.To, "p") {
				sent++
			}
		}
		if sent != 5 {
			t.Fatalf("one caller caused %d, want 5", sent)
		}
	})
}

// A reset by address reads a verified address of an account, on the whole
// server: one held unverified finds nobody, and an account of another tenant
// is found as its username would find it. The link goes to the stored address.
func TestAResetByAddressFindsAVerifiedAddressOnly(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		f := newTenFx(t, a)
		enableMail(t, f.root, srv)
		ctx := context.Background()
		if err := store.SetEmail(ctx, a.st.Q(), f.userA, "Mem@Example.test", true, ""); err != nil {
			t.Fatal(err)
		}
		verifiedUser(t, a, "squat", "")
		if err := store.SetEmail(ctx, a.st.Q(), mustUserID(t, a, "squat"), "held@example.test", false, ""); err != nil {
			t.Fatal(err)
		}
		forgot(a.newClient("203.0.113.1"), "held@example.test").want(t, 202, "")
		forgot(a.newClient("203.0.113.2"), "MEM@example.test").want(t, 202, "")
		m := srv.Wait(1)[0]
		if m.To != "Mem@Example.test" || len(m.Tokens()) != 1 {
			t.Fatalf("%s: %s", m.To, m.Text)
		}
		settle()
		if n := len(srv.Messages()); n != 1 {
			t.Fatalf("%d messages", n)
		}
	})
}

func TestAnSSOOnlyAccountIsToldToSignInWithTheProviderNotGivenALink(t *testing.T) {
	t.Parallel()
	srv := fakeMail(t)
	idp := newIDPFor(t, "home")
	eachAccounts(t, accOpts{httpc: anyTLSClient()}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		enableMail(t, root, srv)
		root.do("POST", "/api/admin/auth/providers", idp.providerBody("home", map[string]any{"name": "Home IdP"})).want(t, 201, "")
		// An account made through the provider has no password.
		if _, err := a.auth.CreateUser(context.Background(), auth.CreateParams{Username: "sso", Role: "member", Email: "sso@example.test", EmailVerified: true,
			Identity: &auth.IdentityParams{Issuer: idp.srv.URL, Subject: "s1", Label: "sso"}}); err != nil {
			t.Fatal(err)
		}
		verifiedUser(t, a, "pw", "pw@example.test")
		forgot(a.newClient("203.0.113.1"), "sso").want(t, 202, "")
		m := srv.Wait(1)[0]
		if m.To != "sso@example.test" || len(m.Tokens()) != 0 || strings.Contains(m.Text, "reset-password") || !strings.Contains(m.Text, "Home IdP") {
			t.Fatalf("%+v", m)
		}
		if rowCount(t, a, `SELECT COUNT(*) FROM email_tokens`) != 0 {
			t.Fatal("a reset token was made for an account without a password")
		}
		// With ssoOnly in effect, an account with a password that is refused gets the same.
		root.do("PUT", "/api/admin/security", map[string]any{"login": map[string]any{"ssoOnly": true}}).want(t, 200, "")
		forgot(a.newClient("203.0.113.2"), "pw").want(t, 202, "")
		m = srv.Wait(2)[1]
		if m.To != "pw@example.test" || len(m.Tokens()) != 0 || !strings.Contains(m.Text, "Home IdP") {
			t.Fatalf("%+v", m)
		}
		// A link made before ssoOnly came on cannot add a password either.
		root.do("PUT", "/api/admin/security", map[string]any{"login": map[string]any{"ssoOnly": false}}).want(t, 200, "")
		forgot(a.newClient("203.0.113.3"), "pw").want(t, 202, "")
		tok := srv.Wait(3)[2].Token(t)
		root.do("PUT", "/api/admin/security", map[string]any{"login": map[string]any{"ssoOnly": true}}).want(t, 200, "")
		a.newClient("").do("POST", "/api/password/reset", map[string]any{"token": tok, "password": "a brand new password"}).want(t, 404, "not_found")
	})
}

func TestAResetForgivesTheFailuresAndLocksOfTheUsername(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		root := a.setup("root", pw)
		enableMail(t, root, srv)
		root.do("PUT", "/api/admin/security", map[string]any{"lockout": map[string]any{"enabled": true, "minutes": 15, "scope": "account"}, "limits": map[string]any{"maxFailures": 3}}).want(t, 200, "")
		verifiedUser(t, a, "tang", "tang@example.test")
		for i := 0; i < 3; i++ {
			a.newClient("198.51.100.5").do("POST", "/api/login", map[string]any{"username": "tang", "password": "wrong one"})
		}
		a.newClient("198.51.100.6").do("POST", "/api/login", map[string]any{"username": "tang", "password": pw}).want(t, 429, "rate_limited")
		forgot(a.newClient("203.0.113.1"), "tang").want(t, 202, "")
		tok := srv.Wait(1)[0].Token(t)
		a.newClient("203.0.113.1").do("POST", "/api/password/reset", map[string]any{"token": tok, "password": "a brand new password"}).want(t, 204, "")
		a.newClient("198.51.100.6").do("POST", "/api/login", map[string]any{"username": "tang", "password": "a brand new password"}).want(t, 200, "")
	})
}

func TestForgotHasItsOwnCaptchaContext(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		root := a.setup("root", pw)
		enableMail(t, root, srv)
		verifiedUser(t, a, "tang", "tang@example.test")
		sec := root.do("GET", "/api/admin/security", nil).want(t, 200, "")
		ctxs := sec.body["captcha"].(map[string]any)["contexts"].(map[string]any)
		if ctxs["forgot"] == nil || ctxs["register"] == nil {
			t.Fatal(sec.raw)
		}
		root.do("PUT", "/api/admin/security", map[string]any{"captcha": map[string]any{"contexts": map[string]any{"forgot": map[string]any{"on": true, "when": "always"}}}}).want(t, 200, "")
		anon := a.newClient("")
		forgot(anon, "tang").want(t, 400, "captcha_failed")
		if c := anon.do("GET", "/api/captcha?ctx=forgot", nil).want(t, 200, ""); c.body["required"] != true {
			t.Fatal(c.raw)
		}
		if c := anon.do("GET", "/api/captcha?ctx=register", nil).want(t, 200, ""); c.body["required"] != false {
			t.Fatal(c.raw)
		}
		settle()
		if len(srv.Messages()) != 0 {
			t.Fatal("a message went out without the captcha")
		}
	})
}

func TestForgotAndResetWrongTokensCountAsFailuresOfTheAddress(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		root := a.setup("root", pw)
		enableMail(t, root, srv)
		root.do("PUT", "/api/admin/security", map[string]any{"limits": map[string]any{"maxFailures": 3, "perMinute": 100}}).want(t, 200, "")
		anon := a.newClient("198.51.100.9")
		for i := 0; i < 3; i++ {
			anon.do("POST", "/api/email/confirm", map[string]any{"token": "wrong"}).want(t, 404, "not_found")
		}
		anon.do("POST", "/api/password/reset/check", map[string]any{"token": "wrong"}).want(t, 429, "rate_limited")
		anon.do("POST", "/api/register/confirm", map[string]any{"token": "wrong"}).want(t, 429, "rate_limited")
		// Another address is not affected.
		a.newClient("198.51.100.10").do("POST", "/api/password/reset/check", map[string]any{"token": "wrong"}).want(t, 404, "not_found")
	})
}

func TestAnAdministratorSendsAResetLinkToAVerifiedAddress(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		f := newTenFx(t, a)
		enableMail(t, f.root, srv)
		verifiedUser(t, a, "tang", "tang@example.test")
		uid := mustUserID(t, a, "tang")
		f.root.do("POST", "/api/users/"+uid+"/password-reset", map[string]any{}).want(t, 202, "")
		m := srv.Wait(1)[0]
		tok := m.Token(t)
		a.newClient("").do("POST", "/api/password/reset", map[string]any{"token": tok, "password": "a brand new password"}).want(t, 204, "")
		rows := auditKinds(t, f.root, "password_reset_requested")
		if len(rows) != 1 || rows[0].(map[string]any)["detail"].(map[string]any)["by"] != "admin" {
			t.Fatal(rows)
		}
		// Refusals.
		f.root.do("POST", "/api/users/"+uid+"/password-reset", map[string]any{}).want(t, 202, "") // fresh link: allowed again
		unver := a.addUser("unver", pw, "member")
		_ = unver
		f.root.do("POST", "/api/users/"+mustUserID(t, a, "unver")+"/password-reset", map[string]any{}).want(t, 409, "email_unverified")
		if _, err := a.auth.CreateUser(context.Background(), auth.CreateParams{Username: "nopw", Role: "member", Email: "nopw@example.test", EmailVerified: true}); err != nil {
			t.Fatal(err)
		}
		f.root.do("POST", "/api/users/"+mustUserID(t, a, "nopw")+"/password-reset", map[string]any{}).want(t, 409, "conflict")
		f.root.do("POST", "/api/users/nobody/password-reset", map[string]any{}).want(t, 404, "not_found")
		// A tenant administrator reaches only their tenant, and never a site administrator.
		f.admB.do("POST", "/api/users/"+uid+"/password-reset", map[string]any{}).want(t, 404, "not_found")
		f.admA.do("POST", "/api/users/"+mustUserID(t, a, "root")+"/password-reset", map[string]any{}).want(t, 403, "forbidden")
		f.memA.do("POST", "/api/users/"+uid+"/password-reset", map[string]any{}).want(t, 403, "forbidden")
		f.root.do("PUT", "/api/admin/mail", map[string]any{"enabled": false}).want(t, 200, "")
		f.root.do("POST", "/api/users/"+uid+"/password-reset", map[string]any{}).want(t, 409, "mail_not_configured")
	})
}
