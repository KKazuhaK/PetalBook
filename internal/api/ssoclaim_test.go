package api_test

import (
	"context"
	"testing"

	"petalbook.app/server/internal/auth/testidp"
	"petalbook.app/server/internal/store"
)

// An account an administrator made for an organisation that requires its SSO
// (signIn: "sso") waits for its first sign-in: the organisation's own SSO (or a
// site SSO routed to it) claims it when the sign-in's address is one the
// provider may vouch for and is the account's. Nothing else claims it, and no
// sign-in moves or duplicates its address.

func ssoClaimFx(t *testing.T, a *acc, idp *fakeIDP, mode string) *ssoFx {
	t.Helper()
	f := newSSOFx(t, a, idp)
	f.orgProvider(t, f.admB, f.tenB, "acme-sso", nil)
	f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/signin.ssoOnly", map[string]any{"value": true}).want(t, 200, "")
	setMode(t, f.root, "", "invite")
	setMode(t, f.admB, f.tenB, mode)
	return f
}

func TestAnAdminMadeSSOAccountIsClaimedByItsFirstSSOSignIn(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	for _, mode := range []string{"closed", "invite", "open"} {
		eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
			f := ssoClaimFx(t, a, idp, mode)
			r := f.admB.do("POST", adminUsers, map[string]any{"username": "al", "name": "Al", "email": "Alice@Acme.example"}).want(t, 201, "")
			if r.body["signIn"] != "sso" {
				t.Fatal(r.raw)
			}
			made := r.user()["id"].(string)
			c := a.newClient("")
			if loc := idp.signIn2(t, c, "acme-sso", "alice", nil); loc != "/" || signedInAs(t, c) != "al" {
				t.Fatalf("%s: %s %q", mode, loc, signedInAs(t, c))
			}
			u := userTenant(t, a, "al")
			if u.ID != made || !u.EmailVerified || u.TenantID != f.tenB {
				t.Fatalf("%+v", u)
			}
			if by, _ := store.EmailVerifiedBy(context.Background(), a.st.Q(), made); by != "provider:acme-sso" {
				t.Fatalf("verified by %q", by)
			}
			if rows := f.root.audit(t, "identity_linked"); len(rows) != 1 {
				t.Fatalf("%v", rows)
			}
			// Claimed once: the mark is gone, the link signs in, and nobody else is made.
			c2 := a.newClient("")
			if loc := idp.signIn2(t, c2, "acme-sso", "alice", nil); loc != "/" || signedInAs(t, c2) != "al" {
				t.Fatalf("%s %q", loc, signedInAs(t, c2))
			}
			if n := rowCount(t, a, `SELECT COUNT(*) FROM awaiting_sso`); n != 0 {
				t.Fatalf("%d still waiting", n)
			}
		})
	}
}

func TestAnAwaitingAccountIsNotClaimedByAnythingElse(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := ssoClaimFx(t, a, idp, "open")
		// The address is another's: carol signs in at the provider with carol@beta.example.
		f.admB.do("POST", adminUsers, map[string]any{"username": "other", "email": "someone@acme.example"}).want(t, 201, "")
		c := a.newClient("")
		// Carol's address is not on Acme's verified domains: the provider vouches for nothing of it.
		if idp.signIn2(t, c, "acme-sso", "carol", nil); signedInAs(t, c) == "other" {
			t.Fatal("claimed by another person")
		}
		// Disabled: not claimed, and the address stays where it is.
		f.admB.do("POST", adminUsers, map[string]any{"username": "al", "email": "alice@acme.example"}).want(t, 201, "")
		f.root.do("PATCH", "/api/users/"+mustUserID(t, a, "al"), map[string]any{"disabled": true}).want(t, 200, "")
		c = a.newClient("")
		if loc := idp.signIn2(t, c, "acme-sso", "alice", nil); signedInAs(t, c) != "" || loc != "/login?sso_error=email_in_use" {
			t.Fatalf("%s %q", loc, signedInAs(t, c))
		}
		if u := userTenant(t, a, "al"); u.Email != "alice@acme.example" || u.EmailVerified {
			t.Fatalf("%+v", u)
		}
	})
}

// A sign-in never moves an address an administrator-made account is waiting
// with, nor makes a second account for it: under open it used to take the
// address from the unverified holder and make a new account. A site provider
// that vouches for the address but serves no route to the account's tenant
// cannot claim it, so the person is sent to the account instead (email_in_use).
func TestASignInNeverMovesOrDuplicatesAnAddressOfAWaitingAccount(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	site := newIDPFor(t, "m")
	site.addUser("alice", testidp.Claims{"sub": "s-alice", "preferred_username": "alice", "email": "alice@acme.example", "email_verified": true, "name": "Alice"})
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := ssoClaimFx(t, a, idp, "open")
		setMode(t, f.root, "", "open")
		f.root.do("POST", "/api/admin/providers", site.providerBody("m", map[string]any{"mergeVerifiedEmail": true})).want(t, 201, "")
		f.admB.do("POST", adminUsers, map[string]any{"username": "al", "email": "alice@acme.example"}).want(t, 201, "")
		before := rowCount(t, a, `SELECT COUNT(*) FROM users`)
		c := a.newClient("")
		loc := site.signIn2(t, c, "m", "alice", nil)
		if loc != "/login?sso_error=email_in_use" || signedInAs(t, c) != "" || rowCount(t, a, `SELECT COUNT(*) FROM users`) != before {
			t.Fatalf("a second account: %s %q", loc, signedInAs(t, c))
		}
		if u := userTenant(t, a, "al"); u.Email != "alice@acme.example" {
			t.Fatalf("the address moved: %+v", u)
		}
	})
}
