package api_test

import (
	"net/url"
	"testing"
)

// A link that proves an address names the account it is for; the claim of an
// address another account holds unverified is a message of its own that also
// says that confirming moves the address.
func TestVerifyMailNamesTheAccountAndAClaimSaysItMoves(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		_, c, srv := multiSetup(t, a)
		c.do("PATCH", "/api/account", map[string]any{"name": "Tang Mei"}).want(t, 200, "")
		c.do("POST", "/api/account/emails", map[string]any{"email": "free@example.test", "lang": "en"}).want(t, 202, "")
		srv.Wait(2)
		plain := lastTo(t, srv, "free@example.test")
		if !contains(plain.Text, "Tang Mei (tang)", "free@example.test") {
			t.Fatalf("the verification does not name the account:\n%s", plain.Text)
		}
		squat := a.addUser("squat", pw, "member")
		squat.do("PATCH", "/api/account", map[string]any{"email": "wanted@example.test"}).want(t, 200, "")
		c.do("POST", "/api/account/emails", map[string]any{"email": "wanted@example.test", "lang": "en"}).want(t, 202, "")
		srv.Wait(4)
		claim := lastTo(t, srv, "wanted@example.test")
		if claim.Subject == plain.Subject || !contains(claim.Text, "Tang Mei (tang)", "wanted@example.test", "moved away from") {
			t.Fatalf("the claim: %q\n%s", claim.Subject, claim.Text)
		}
		// In Chinese too.
		c.do("POST", "/api/account/emails/verify", map[string]any{"email": "free@example.test", "lang": "zh-Hans"}).want(t, 202, "")
		srv.Wait(5)
		if m := lastTo(t, srv, "free@example.test"); !contains(m.Text, "Tang Mei (tang)") {
			t.Fatalf("%s", m.Text)
		}
	})
}

// A pending claim shows in the list and can be withdrawn: its link then stops
// working, and the address stays with its holder.
func TestAPendingClaimCanBeWithdrawn(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		_, c, srv := multiSetup(t, a)
		squat := a.addUser("squat", pw, "member")
		squat.do("PATCH", "/api/account", map[string]any{"email": "wanted@example.test"}).want(t, 200, "")
		c.do("POST", "/api/account/emails", map[string]any{"email": "wanted@example.test"}).want(t, 202, "")
		srv.Wait(2)
		tok := lastTo(t, srv, "wanted@example.test").Token(t)
		got := emailsOf(t, c)["wanted@example.test"]
		if got == nil || got["pending"] != true || got["verified"] != false || got["primary"] != false {
			t.Fatalf("the claim is not listed as pending: %v", emailsOf(t, c))
		}
		if h := emailsOf(t, squat)["wanted@example.test"]; h["pending"] == true {
			t.Fatal("the holder shows another account's claim as its own")
		}
		// Another spelling of the address withdraws it too.
		c.do("DELETE", "/api/account/emails/"+url.PathEscape("WANTED@example.test"), nil).want(t, 204, "")
		if _, ok := emailsOf(t, c)["wanted@example.test"]; ok {
			t.Fatal("still listed")
		}
		a.newClient("").do("POST", "/api/email/confirm", map[string]any{"token": tok}).want(t, 404, "not_found")
		if squat.do("GET", "/api/account", nil).user()["email"] != "wanted@example.test" {
			t.Fatal("the holder lost the address")
		}
		c.do("DELETE", "/api/account/emails/"+url.PathEscape("wanted@example.test"), nil).want(t, 404, "not_found")
	})
}

// ConfirmEmail tells the primary from another address by comparing the link's
// address with the primary now, so every change of the primary must drop the
// links sent before it. Pinned for the person's own change and an
// administrator's.
func TestAChangeOfThePrimaryDropsTheEarlierLinks(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root, _, srv := multiSetup(t, a)
		for i, by := range []string{"self", "admin"} {
			name := []string{"pia", "pat"}[i]
			c := a.addUser(name, pw, "member")
			old, next := name+"-old@example.test", name+"-new@example.test"
			c.do("PATCH", "/api/account", map[string]any{"email": old}).want(t, 200, "")
			n := len(srv.Messages())
			c.do("POST", "/api/account/email/verify", map[string]any{}).want(t, 202, "")
			srv.Wait(n + 1)
			tok := lastTo(t, srv, old).Token(t)
			if by == "self" {
				c.do("PATCH", "/api/account", map[string]any{"email": next}).want(t, 200, "")
			} else {
				root.do("PATCH", "/api/users/"+mustUserID(t, a, name), map[string]any{"email": next}).want(t, 200, "")
			}
			a.newClient("").do("POST", "/api/email/confirm", map[string]any{"token": tok}).want(t, 404, "not_found")
			if c.do("GET", "/api/account", nil).user()["emailVerified"] == true {
				t.Fatalf("%s: the old link verified the new primary", by)
			}
		}
	})
}
