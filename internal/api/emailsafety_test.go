package api_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/auth/testidp"
	"petalbook.app/server/internal/store"
)

// Go's strings.ToLower folds U+0130 (İ) to "i" and U+212A (the Kelvin sign)
// to "k", so these look-alikes of tom@github.test and kim@example.test came
// out equal to them. Addresses are ASCII only now.
const (
	lookDotI   = "tom@gİthub.test"
	lookKelvin = "Kim@example.test"
)

func TestALookalikeAddressNeverReachesAnAccountsMail(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		root := a.setup("root", pw)
		openRegistration(t, root, srv)
		verifiedUser(t, a, "tom", "tom@github.test")
		verifiedUser(t, a, "kim", "kim@example.test")
		anon := a.newClient("")
		for i, addr := range []string{lookDotI, lookKelvin} {
			reg(anon, map[string]any{"username": "probe" + string(rune('a'+i)), "email": addr}).want(t, 400, "email_not_ascii")
			forgot(a.newClient("203.0.113."+string(rune('1'+i))), addr).want(t, 202, "")
		}
		settle()
		if n := len(srv.Messages()); n != 0 {
			t.Fatalf("%d messages went out for look-alike addresses: %+v", n, srv.Messages())
		}
		// The address is ASCII wherever it is set, while users.emailNonAscii is off.
		c := a.addUser("tang", pw, "member")
		c.do("PATCH", "/api/account", map[string]any{"email": lookDotI}).want(t, 400, "email_not_ascii")
		root.do("PATCH", "/api/users/"+mustUserID(t, a, "tang"), map[string]any{"email": lookKelvin, "emailVerified": true}).want(t, 400, "email_not_ascii")
	})
}

// The "you already have an account" message goes to the stored address, in
// the stored form, never to what was typed.
func TestTheExistingAccountMessageGoesToTheStoredAddress(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		root := a.setup("root", pw)
		openRegistration(t, root, srv)
		verifiedUser(t, a, "tom", "Tom@GitHub.test")
		reg(a.newClient(""), map[string]any{"username": "newtom", "email": "tom@github.test"}).want(t, 202, "")
		m := srv.Wait(1)[0]
		if m.To != "Tom@GitHub.test" {
			t.Fatalf("sent to %q", m.To)
		}
	})
}

func TestTheDomainAllowListComparesASCIIOnly(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		root := a.setup("root", pw)
		openRegistration(t, root, srv)
		root.do("PUT", "/api/admin/settings/registration.domains", map[string]any{"value": []string{"github.test", "kite.test"}}).want(t, 200, "")
		anon := a.newClient("")
		for i, addr := range []string{"x@gİthub.test", "x@Kite.test"} {
			if r := reg(anon, map[string]any{"username": "dom" + string(rune('a'+i)), "email": addr}); r.code == 202 {
				t.Fatalf("%q passed the allow-list: %s", addr, r.raw)
			}
		}
		reg(anon, map[string]any{"username": "domok", "email": "x@GitHub.test"}).want(t, 202, "")
		srv.Wait(1)
		settle()
		if n := len(srv.Messages()); n != 1 {
			t.Fatalf("%d messages", n)
		}
	})
}

// A look-alike domain is another domain (IDNA, not Unicode lower case); the
// Kelvin sign is canonically "K" (NFC), so that spelling is kim's own address.
func TestALookupByAddressNeverFoldsALookAlikeDomain(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		a.setup("root", pw)
		ctx := context.Background()
		verifiedUser(t, a, "tom", "tom@github.test")
		verifiedUser(t, a, "kim", "KIM@example.test")
		a.addUser("old", pw, "member")
		if err := store.SetEmail(ctx, a.st.Q(), mustUserID(t, a, "old"), "old@gİthub.test", true, ""); err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct{ email, want string }{
			{"tom@github.test", "tom"}, {"TOM@GITHUB.TEST", "tom"}, {"kim@example.test", "kim"}, {lookKelvin, "kim"},
			{lookDotI, ""}, {"old@github.test", ""},
		} {
			got := ""
			u, _, err := store.UserByVerifiedEmail(ctx, a.st.Q(), c.email)
			if err == nil {
				got = u.Username
			} else if !errors.Is(err, store.ErrNotFound) {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("%q: %q, want %q", c.email, got, c.want)
			}
		}
	})
}

// An address stored before addresses were ASCII only stays as it is, matches
// nothing, and gets no account mail: the person sets an ASCII address.
func TestAStoredNonASCIIAddressGetsNoAccountMail(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		f := newTenFx(t, a)
		enableMail(t, f.root, srv)
		ctx := context.Background()
		c := a.addUser("old", pw, "member")
		uid := mustUserID(t, a, "old")
		if err := store.SetEmail(ctx, a.st.Q(), uid, "old@gİthub.test", true, ""); err != nil {
			t.Fatal(err)
		}
		forgot(a.newClient("203.0.113.1"), "old").want(t, 202, "")
		f.root.do("POST", "/api/users/"+uid+"/password-reset", map[string]any{}).want(t, 409, "email_unverified")
		c.do("PATCH", "/api/account", map[string]any{"email": "old@gİthub.test"}).want(t, 400, "email_not_ascii")
		// A name change keeps the stored address as it is.
		if u := c.do("PATCH", "/api/account", map[string]any{"name": "Old"}).want(t, 200, "").user(); u["email"] != "old@gİthub.test" {
			t.Fatal(u)
		}
		if err := store.SetEmail(ctx, a.st.Q(), uid, "old@gİthub.test", false, ""); err != nil {
			t.Fatal(err)
		}
		c.do("POST", "/api/account/email/verify", map[string]any{}).want(t, 400, "email_not_ascii")
		settle()
		if n := len(srv.Messages()); n != 0 {
			t.Fatalf("%d messages to a non-ASCII address", n)
		}
		// An ASCII address works as for anyone.
		c.do("PATCH", "/api/account", map[string]any{"email": "old@github.test"}).want(t, 200, "")
		c.do("POST", "/api/account/email/verify", map[string]any{}).want(t, 202, "")
		srv.Wait(1)
	})
}

func TestALookalikeAddressFromAProviderNeverMerges(t *testing.T) {
	t.Parallel()
	f := newIDPFor(t, "m")
	f.addUser("dot", testidp.Claims{"sub": "s-dot", "preferred_username": "dot", "email": "tom@gİthub.test", "email_verified": true, "name": "dot"})
	f.addUser("kel", testidp.Claims{"sub": "s-kel", "preferred_username": "kel", "email": lookKelvin, "email_verified": true, "name": "kel"})
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		admin.do("POST", "/api/admin/auth/providers", f.providerBody("m", map[string]any{"mergeVerifiedEmail": true})).want(t, 201, "")
		ctx := context.Background()
		for _, p := range [][2]string{{"tom-local", "tom@github.test"}, {"kim-local", "kim@example.test"}} {
			if _, err := a.auth.CreateUser(ctx, auth.CreateParams{Username: p[0], Role: "member", Password: pw, Email: p[1], EmailVerified: true}); err != nil {
				t.Fatal(err)
			}
		}
		for _, key := range []string{"dot", "kel"} {
			c := a.newClient("")
			if loc := f.signIn2(t, c, "m", key, map[string]any{}); loc != "/login?sso_error=no_account" {
				t.Errorf("%s: %s (signed in as %q)", key, loc, signedInAs(t, c))
			}
		}
		if rows := admin.audit(t, "identity_linked"); len(rows) != 0 {
			t.Fatalf("a look-alike address merged: %v", rows)
		}
	})
}

// A username asked for with an address that has an account is held as long
// as one asked for with a free address: the second answer does not tell.
func TestARegistrationProbeGetsTheSameAnswerForATakenAndAFreeAddress(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		root := a.setup("root", pw)
		openRegistration(t, root, srv)
		verifiedUser(t, a, "owner", "owner@example.test")
		var answers [2][2]string
		for i, addr := range []string{"owner@example.test", "free@example.test"} {
			name := "probe" + string(rune('a'+i))
			answers[i][0] = reg(a.newClient("203.0.113."+string(rune('1'+i))), map[string]any{"username": name, "email": addr}).want(t, 202, "").raw
			r := reg(a.newClient("203.0.113."+string(rune('5'+i))), map[string]any{"username": name, "email": "other" + string(rune('a'+i)) + "@example.test"})
			answers[i][1] = r.raw
			if r.code != 409 {
				t.Errorf("%s: the second request got %d %s", addr, r.code, r.raw)
			}
		}
		if answers[0] != answers[1] {
			t.Fatalf("the answers differ: %v / %v", answers[0], answers[1])
		}
		srv.Wait(2)
		if !mustNoUser(a, "probea") {
			t.Fatal("the held username made an account")
		}
	})
}

func TestAnAddressChangeMakesResetLinksUnusable(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		root := a.setup("root", pw)
		enableMail(t, root, srv)
		verifiedUser(t, a, "tang", "tang@example.test")
		uid := mustUserID(t, a, "tang")
		anon := a.newClient("")
		c := a.newClient("")
		c.do("POST", "/api/login", map[string]any{"username": "tang", "password": pw}).want(t, 200, "")
		n := 0
		seen := map[string]bool{}
		fresh := func() string {
			t.Helper()
			a.clock.Advance(61 * time.Minute) // a new hour of the address's three messages
			if err := store.SetEmail(context.Background(), a.st.Q(), uid, "tang@example.test", true, ""); err != nil {
				t.Fatal(err)
			}
			forgot(a.newClient("203.0.113."+string(rune('1'+n))), "tang").want(t, 202, "")
			n++
			for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
				for _, m := range srv.Messages() {
					if contains(m.Text, "/reset-password#") && !seen[m.Text] {
						seen[m.Text] = true
						return m.Token(t)
					}
				}
			}
			t.Fatal("no reset link came")
			return ""
		}
		check := func(what, tok string) {
			t.Helper()
			if r := anon.do("POST", "/api/password/reset/check", map[string]any{"token": tok}); r.code != 404 {
				t.Errorf("%s: the link still works: %d %s", what, r.code, r.raw)
			}
		}
		// The person changes it.
		tok := fresh()
		c.do("PATCH", "/api/account", map[string]any{"email": "tang2@example.test"}).want(t, 200, "")
		check("the person's change", tok)
		// An administrator changes it, even marking the new one verified.
		tok = fresh()
		root.do("PATCH", "/api/users/"+uid, map[string]any{"email": "tang3@example.test", "emailVerified": true}).want(t, 200, "")
		check("an administrator's change", tok)
		// Changed underneath: the link is for the address it was sent to.
		tok = fresh()
		if err := store.SetEmail(context.Background(), a.st.Q(), uid, "tang4@example.test", true, ""); err != nil {
			t.Fatal(err)
		}
		check("a stored change", tok)
	})
}

// An address only a tenant administrator says is verified gets no reset link:
// they could give a member their own address and take the account over.
func TestATenantAdministratorsMarkGetsNoResetLink(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		f := newTenFx(t, a)
		enableMail(t, f.root, srv)
		f.root.do("PUT", "/api/admin/settings/registration.open", map[string]any{"value": true}).want(t, 200, "")
		f.admA.do("PATCH", "/api/users/"+f.userA, map[string]any{"email": "evil@example.test", "emailVerified": true}).want(t, 200, "")
		if by, _ := store.EmailVerifiedBy(context.Background(), a.st.Q(), f.userA); by != store.VerifiedByTenantAdmin {
			t.Fatalf("by %q", by)
		}
		forgot(a.newClient("203.0.113.1"), "mem-a").want(t, 202, "")
		forgot(a.newClient("203.0.113.2"), "evil@example.test").want(t, 202, "")
		f.admA.do("POST", "/api/users/"+f.userA+"/password-reset", map[string]any{}).want(t, 409, "email_unverified")
		f.root.do("POST", "/api/users/"+f.userA+"/password-reset", map[string]any{}).want(t, 409, "email_unverified")
		reg(a.newClient("203.0.113.3"), map[string]any{"username": "evil", "email": "evil@example.test"}).want(t, 202, "")
		settle()
		for _, m := range srv.Messages() {
			if contains(m.Text, "/reset-password#") {
				t.Fatalf("a reset link went to %s", m.To)
			}
		}
		// A site administrator's mark counts.
		f.root.do("PATCH", "/api/users/"+f.userA, map[string]any{"emailVerified": true}).want(t, 200, "")
		f.root.do("POST", "/api/users/"+f.userA+"/password-reset", map[string]any{}).want(t, 202, "")
	})
}

// A member whose address only a tenant administrator marked may confirm it
// themself; that clears the mark, and the address then gets a reset link.
func TestAMemberConfirmsAnAddressATenantAdministratorMarked(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		f := newTenFx(t, a)
		enableMail(t, f.root, srv)
		f.admA.do("PATCH", "/api/users/"+f.userA, map[string]any{"email": "mem@example.test", "emailVerified": true}).want(t, 200, "")
		before := len(srv.Messages())
		f.memA.do("POST", "/api/account/email/verify", map[string]any{}).want(t, 202, "")
		tok := srv.Wait(before + 1)[before].Token(t)
		a.newClient("").do("POST", "/api/email/confirm", map[string]any{"token": tok}).want(t, 204, "")
		if by, _ := store.EmailVerifiedBy(context.Background(), a.st.Q(), f.userA); by != "" {
			t.Fatalf("by %q", by)
		}
		f.memA.do("POST", "/api/account/email/verify", map[string]any{}).want(t, 409, "email_verified")
		before = len(srv.Messages())
		forgot(a.newClient("203.0.113.4"), "mem@example.test").want(t, 202, "")
		if m := srv.Wait(before + 1)[before]; !contains(m.Text, "/reset-password#") {
			t.Fatal("no reset link")
		}
	})
}
