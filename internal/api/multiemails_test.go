package api_test

import (
	"context"
	"net/url"
	"testing"

	"petalbook.app/server/internal/mail/mailtest"
	"petalbook.app/server/internal/store"
)

// emailsOf is the addresses GET /api/account shows, by address.
func emailsOf(t *testing.T, c *client) map[string]map[string]any {
	t.Helper()
	r := c.do("GET", "/api/account", nil).want(t, 200, "")
	out := map[string]map[string]any{}
	l, _ := r.body["emails"].([]any)
	for _, x := range l {
		m := x.(map[string]any)
		out[m["email"].(string)] = m
	}
	return out
}

// multiSetup is a server with mail, the switch on, and tang (primary
// tang@example.test, verified) signed in.
func multiSetup(t *testing.T, a *acc) (root, tang *client, srv *mailtest.Server) {
	t.Helper()
	srv = fakeMail(t)
	root = a.setup("root", pw)
	enableMail(t, root, srv)
	root.putSetting("accounts.multipleEmails", true).want(t, 200, "")
	verifiedUser(t, a, "tang", "tang@example.test")
	tang = a.newClient("")
	tang.do("POST", "/api/login", good("tang")).want(t, 200, "")
	return root, tang, srv
}

// addAndConfirm adds an address and follows the link sent to it.
func addAndConfirm(t *testing.T, a *acc, c *client, srv *mailtest.Server, email string) {
	t.Helper()
	n := len(srv.Messages())
	c.do("POST", "/api/account/emails", map[string]any{"email": email}).want(t, 202, "")
	var tok string
	for _, m := range srv.Wait(n + 1)[n:] {
		if m.To == email {
			tok = m.Token(t)
		}
	}
	if tok == "" {
		tok = lastTo(t, srv, email).Token(t)
	}
	a.newClient("").do("POST", "/api/email/confirm", map[string]any{"token": tok}).want(t, 204, "")
}

func TestMoreThanOneAddressIsOffByDefault(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		root := a.setup("root", pw)
		enableMail(t, root, srv)
		verifiedUser(t, a, "tang", "tang@example.test")
		c := a.newClient("")
		c.do("POST", "/api/login", good("tang")).want(t, 200, "")
		r := c.do("POST", "/api/account/emails", map[string]any{"email": "two@example.test"}).want(t, 403, "feature_off")
		if r.body["feature"] != "accounts.multipleEmails" {
			t.Fatalf("%v", r.body)
		}
		c.do("DELETE", "/api/account/emails/"+url.PathEscape("two@example.test"), nil).want(t, 403, "feature_off")
		c.do("POST", "/api/account/emails/primary", map[string]any{"email": "two@example.test"}).want(t, 403, "feature_off")
		c.do("POST", "/api/account/emails/verify", map[string]any{"email": "two@example.test"}).want(t, 403, "feature_off")
		got := emailsOf(t, c)
		if len(got) != 1 || got["tang@example.test"]["primary"] != true || got["tang@example.test"]["verified"] != true {
			t.Fatalf("%v", got)
		}
		feats := c.do("GET", "/api/settings", nil).want(t, 200, "").body["features"].(map[string]any)
		if feats["accounts.multipleEmails"] != false {
			t.Fatalf("%v", feats)
		}
	})
}

// Add, then confirm: the address counts only once its link is followed, and
// then it signs in (with the password) and resets as the primary does.
func TestASecondAddressSignsInOnceConfirmed(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root, c, srv := multiSetup(t, a)
		c.do("POST", "/api/account/emails", map[string]any{"email": "Second@Example.test", "lang": "en"}).want(t, 202, "")
		ms := srv.Wait(2)
		var link mailtest.Message
		toPrimary := 0
		for _, m := range ms {
			switch m.To {
			case "Second@Example.test":
				link = m
			case "tang@example.test":
				toPrimary++
			}
		}
		if link.To == "" || toPrimary != 1 {
			t.Fatalf("the link to the new address and a note to the primary: %+v", ms)
		}
		got := emailsOf(t, c)
		if len(got) != 2 || got["Second@Example.test"]["verified"] != false || got["Second@Example.test"]["primary"] != false {
			t.Fatalf("%v", got)
		}
		// Unverified: it does not sign in, and the answer is a wrong password's.
		a.newClient("").do("POST", "/api/login", map[string]any{"identifier": "second@example.test", "password": pw}).want(t, 401, "")
		// Adding it again, in any spelling, is refused.
		c.do("POST", "/api/account/emails", map[string]any{"email": "SECOND@example.test"}).want(t, 409, "exists")
		a.newClient("").do("POST", "/api/email/confirm", map[string]any{"token": link.Token(t)}).want(t, 204, "")
		if got := emailsOf(t, c); got["Second@Example.test"]["verified"] != true {
			t.Fatalf("%v", got)
		}
		in := a.newClient("")
		in.do("POST", "/api/login", map[string]any{"identifier": "second@EXAMPLE.test", "password": pw}).want(t, 200, "")
		if in.do("GET", "/api/account", nil).user()["username"] != "tang" {
			t.Fatal("signed in to another account")
		}
		// The primary signs in by address too; a wrong password by address is a wrong password.
		a.newClient("").do("POST", "/api/login", map[string]any{"identifier": "tang@example.test", "password": pw}).want(t, 200, "")
		a.newClient("").do("POST", "/api/login", map[string]any{"identifier": "second@example.test", "password": "not the password at all"}).want(t, 401, "")
		// A reset asked for by the second address goes to it, and works.
		n := len(srv.Messages())
		a.newClient("").do("POST", "/api/password/forgot", map[string]any{"login": "second@example.test"}).want(t, 202, "")
		m := srv.Wait(n + 1)[n]
		if m.To != "Second@Example.test" {
			t.Fatalf("the reset went to %s", m.To)
		}
		a.newClient("").do("POST", "/api/password/reset", map[string]any{"token": m.Token(t), "password": "a new password, long enough"}).want(t, 204, "")
		for _, k := range []string{"email_added", "email_verified"} {
			if len(root.audit(t, k)) == 0 {
				t.Fatalf("%s is not audited", k)
			}
		}
	})
}

// The rule of who holds an address applies to every address: one held
// verified elsewhere cannot be added; one held unverified elsewhere can be
// claimed, and goes to whoever proves it first.
func TestAnotherAccountsAddressAndTheRaceToProveIt(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		_, c, srv := multiSetup(t, a)
		verifiedUser(t, a, "mei", "mei@example.test")
		c.do("POST", "/api/account/emails", map[string]any{"email": "MEI@example.test"}).want(t, 409, "email_taken")
		// Held unverified by an account that is not the person's: the claim
		// waits as a link, with no row until it is followed.
		squat := a.addUser("squat", pw, "member")
		squat.do("PATCH", "/api/account", map[string]any{"email": "wanted@example.test"}).want(t, 200, "")
		n := len(srv.Messages())
		c.do("POST", "/api/account/emails", map[string]any{"email": "wanted@example.test"}).want(t, 202, "")
		var tangTok string
		for _, m := range srv.Wait(n + 2)[n:] {
			if m.To == "wanted@example.test" {
				tangTok = m.Token(t)
			}
		}
		if got := emailsOf(t, c)["wanted@example.test"]; got == nil || got["pending"] != true || got["verified"] != false {
			t.Fatalf("a claim of another account's address shows as pending until it is proved: %v", got)
		}
		a.newClient("").do("POST", "/api/email/confirm", map[string]any{"token": tangTok}).want(t, 204, "")
		if got := emailsOf(t, c); got["wanted@example.test"]["verified"] != true {
			t.Fatalf("%v", got)
		}
		if squat.do("GET", "/api/account", nil).user()["email"] != "" {
			t.Fatal("the unverified holder kept the address")
		}
		// The other way round: someone proves the address first; the person's
		// link then finds it taken.
		lee := a.addUser("lee", pw, "member")
		n = len(srv.Messages())
		c.do("POST", "/api/account/emails", map[string]any{"email": "race@example.test"}).want(t, 202, "")
		var raceTok string
		for _, m := range srv.Wait(n + 2)[n:] {
			if m.To == "race@example.test" {
				raceTok = m.Token(t)
			}
		}
		// tang's row is unverified; lee cannot take an address another account holds, even unverified, by PATCH.
		lee.do("PATCH", "/api/account", map[string]any{"email": "race@example.test"}).want(t, 409, "email_taken")
		uid := mustUserID(t, a, "tang")
		ctx := context.Background()
		if err := store.RemoveEmail(ctx, a.st.Q(), uid, "race@example.test"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.AddEmail(ctx, a.st.Q(), mustUserID(t, a, "lee"), "race@example.test", true, "", true, a.clock.Now()); err != nil {
			t.Fatal(err)
		}
		a.newClient("").do("POST", "/api/email/confirm", map[string]any{"token": raceTok}).want(t, 409, "email_taken")
	})
}

// The primary moves only to a verified address; the primary is never removed;
// each change tells the person and is audited.
func TestPrimaryNeedsAVerifiedAddressAndCannotBeRemoved(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root, c, srv := multiSetup(t, a)
		c.do("POST", "/api/account/emails", map[string]any{"email": "work@example.test"}).want(t, 202, "")
		srv.Wait(2)
		c.do("POST", "/api/account/emails/primary", map[string]any{"email": "work@example.test"}).want(t, 409, "email_unverified")
		c.do("POST", "/api/account/emails/primary", map[string]any{"email": "nobody@example.test"}).want(t, 404, "not_found")
		c.do("DELETE", "/api/account/emails/"+url.PathEscape("tang@example.test"), nil).want(t, 409, "primary_email")
		// Ask for the link again, then follow it.
		c.do("POST", "/api/account/emails/verify", map[string]any{"email": "work@example.test"}).want(t, 202, "")
		srv.Wait(3)
		tok := lastTo(t, srv, "work@example.test").Token(t)
		a.newClient("").do("POST", "/api/email/confirm", map[string]any{"token": tok}).want(t, 204, "")
		c.do("POST", "/api/account/emails/verify", map[string]any{"email": "work@example.test"}).want(t, 409, "email_verified")
		n := len(srv.Messages())
		r := c.do("POST", "/api/account/emails/primary", map[string]any{"email": "work@example.test"}).want(t, 200, "")
		if r.user()["email"] != "work@example.test" || r.user()["emailVerified"] != true {
			t.Fatalf("%v", r.user())
		}
		// The old primary is told, and the new one.
		ms := srv.Wait(n + 2)[n:]
		if !(ms[0].To == "tang@example.test" || ms[1].To == "tang@example.test") {
			t.Fatalf("%+v", ms)
		}
		got := emailsOf(t, c)
		if got["work@example.test"]["primary"] != true || got["tang@example.test"]["primary"] != false {
			t.Fatalf("%v", got)
		}
		// The old primary is now another address: it may go, and it is told (the
		// primary has had its three messages this hour, so its note is left out).
		n = len(srv.Messages())
		c.do("DELETE", "/api/account/emails/"+url.PathEscape("tang@example.test"), nil).want(t, 204, "")
		if m := srv.Wait(n + 1)[n]; m.To != "tang@example.test" || !contains(m.Text, "tang@example.test") {
			t.Fatalf("the removed address was not told: %+v", m)
		}
		if _, ok := emailsOf(t, c)["tang@example.test"]; ok {
			t.Fatal("not removed")
		}
		a.newClient("").do("POST", "/api/login", map[string]any{"identifier": "tang@example.test", "password": pw}).want(t, 401, "")
		c.do("DELETE", "/api/account/emails/"+url.PathEscape("tang@example.test"), nil).want(t, 404, "not_found")
		for _, k := range []string{"email_primary_changed", "email_removed"} {
			if len(root.audit(t, k)) != 1 {
				t.Fatalf("%s is not audited once", k)
			}
		}
	})
}

// Turned off again, the extra addresses are kept, but neither shown nor used to
// sign in or reset; on again, they are back.
func TestTurningTheSwitchOffKeepsTheExtraAddressesUnused(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root, c, srv := multiSetup(t, a)
		addAndConfirm(t, a, c, srv, "second@example.test")
		root.putSetting("accounts.multipleEmails", false).want(t, 200, "")
		if got := emailsOf(t, c); len(got) != 1 || got["tang@example.test"] == nil {
			t.Fatalf("%v", got)
		}
		if rowCount(t, a, `SELECT COUNT(*) FROM user_emails WHERE email = 'second@example.test'`) != 1 {
			t.Fatal("the address was dropped")
		}
		a.newClient("").do("POST", "/api/login", map[string]any{"identifier": "second@example.test", "password": pw}).want(t, 401, "")
		n := len(srv.Messages())
		a.newClient("").do("POST", "/api/password/forgot", map[string]any{"login": "second@example.test"}).want(t, 202, "")
		// The primary's reset goes out; the second's did not, before it.
		a.newClient("").do("POST", "/api/password/forgot", map[string]any{"login": "tang@example.test"}).want(t, 202, "")
		if m := srv.Wait(n + 1)[n]; m.To != "tang@example.test" {
			t.Fatalf("a reset went to %s with the switch off", m.To)
		}
		// The admin's view of the person shows only the primary too.
		uid := mustUserID(t, a, "tang")
		if l, _ := root.do("GET", "/api/users/"+uid, nil).want(t, 200, "").user()["emails"].([]any); len(l) != 1 {
			t.Fatalf("%v", l)
		}
		root.putSetting("accounts.multipleEmails", true).want(t, 200, "")
		if got := emailsOf(t, c); len(got) != 2 {
			t.Fatalf("%v", got)
		}
		a.newClient("").do("POST", "/api/login", map[string]any{"identifier": "second@example.test", "password": pw}).want(t, 200, "")
		if l, _ := root.do("GET", "/api/users/"+uid, nil).want(t, 200, "").user()["emails"].([]any); len(l) != 2 {
			t.Fatalf("%v", l)
		}
	})
}

// An organisation's domain requirement is met by any address the account
// holds verified in one of its domains, a second address too; an unverified
// one, one only a tenant administrator marked, or one of a subdomain does not
// meet it.
func TestTheDomainRequirementCountsASecondAddress(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		_, c, _ := multiSetup(t, a)
		ctx := context.Background()
		uid := mustUserID(t, a, "tang")
		u, err := store.GetUser(ctx, a.st.Q(), store.AllTenants(), uid)
		if err != nil {
			t.Fatal(err)
		}
		has := func() bool {
			ok, err := a.auth.HasVerifiedEmailIn(ctx, *u, []string{"acme.example"})
			if err != nil {
				t.Fatal(err)
			}
			return ok
		}
		if has() {
			t.Fatal("no address in the domain yet")
		}
		c.do("POST", "/api/account/emails", map[string]any{"email": "tang@eng.acme.example"}).want(t, 202, "")
		if has() {
			t.Fatal("an unverified address met the requirement")
		}
		if err := store.SetEmailRowVerified(ctx, a.st.Q(), uid, "tang@eng.acme.example", true, store.VerifiedByTenantAdmin); err != nil {
			t.Fatal(err)
		}
		if has() {
			t.Fatal("a tenant administrator's mark met the requirement")
		}
		if err := store.SetEmailRowVerified(ctx, a.st.Q(), uid, "tang@eng.acme.example", true, ""); err != nil {
			t.Fatal(err)
		}
		if has() {
			t.Fatal("an address of a subdomain met the requirement: the domain counts exactly")
		}
		c.do("POST", "/api/account/emails", map[string]any{"email": "tang@acme.example"}).want(t, 202, "")
		if err := store.SetEmailRowVerified(ctx, a.st.Q(), uid, "tang@acme.example", true, ""); err != nil {
			t.Fatal(err)
		}
		if !has() {
			t.Fatal("a verified second address in the domain did not meet it")
		}
	})
}

// Asking for links is held to the mail caps of the address.
func TestAddingAndResendingAreHeldToTheMailCaps(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		_, c, srv := multiSetup(t, a)
		c.do("POST", "/api/account/emails", map[string]any{"email": "cap@example.test"}).want(t, 202, "")
		c.do("POST", "/api/account/emails/verify", map[string]any{"email": "cap@example.test"}).want(t, 202, "")
		c.do("POST", "/api/account/emails/verify", map[string]any{"email": "cap@example.test"}).want(t, 202, "")
		r := c.do("POST", "/api/account/emails/verify", map[string]any{"email": "cap@example.test"}).want(t, 429, "rate_limited")
		if r.resp.Header.Get("Retry-After") == "" {
			t.Fatal("no Retry-After")
		}
		srv.Wait(4)
		// The only valid link is the last one sent.
		tok := lastTo(t, srv, "cap@example.test").Token(t)
		a.newClient("").do("POST", "/api/email/confirm", map[string]any{"token": tok}).want(t, 204, "")
	})
}
