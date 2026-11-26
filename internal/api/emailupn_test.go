package api_test

import (
	"context"
	"testing"

	"petalbook.app/server/internal/store"
)

// The address is the UPN: one account on the whole server has it, compared
// case folded, whoever sets it (3.6).
func TestATakenAddressIs409InEveryTenantAndSpelling(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		ctx := context.Background()
		if err := store.SetEmail(ctx, a.st.Q(), f.userA, "ann@example.test", false, ""); err != nil {
			t.Fatal(err)
		}
		// Tenant B's administrator, for a person of B.
		f.admB.do("PATCH", "/api/users/"+f.userB, map[string]any{"email": "ANN@Example.test"}).want(t, 409, "email_taken")
		// A person of B, for themself.
		mem := a.newClient("")
		mem.do("POST", "/api/login", good("mem-b")).want(t, 200, "")
		mem.do("PATCH", "/api/account", map[string]any{"email": "Ann@example.TEST"}).want(t, 409, "email_taken")
		// An invite that would make an account with it (a new tenant's first
		// administrator), before anything is made.
		f.root.do("POST", "/api/admin/tenants", map[string]any{"slug": "zeta", "name": "Zeta", "admin": map[string]any{"username": "adm-z", "email": "ann@EXAMPLE.test"}}).want(t, 409, "email_taken")
		if n := rowCount(t, a, `SELECT COUNT(*) FROM tenants WHERE slug = 'zeta'`); n != 0 {
			t.Fatal("the tenant was made")
		}
		// The holder may write it in another case.
		f.root.do("PATCH", "/api/users/"+f.userA, map[string]any{"email": "Ann@Example.test"}).want(t, 200, "")
	})
}

// A registration's confirmation takes an address another account holds
// unverified (that account loses it, is told, and the move is audited); one
// held verified is never displaced: its registration is a decoy whose link
// never comes.
func TestConfirmingARegistrationTakesAnUnverifiedAddressOnly(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := fakeMail(t)
		root := a.setup("root", pw)
		openRegistration(t, root, srv)
		ctx := context.Background()
		squat := a.addUser("squat", pw, "member")
		squat.do("PATCH", "/api/account", map[string]any{"email": "tang@example.test"}).want(t, 200, "")
		anon := a.newClient("")
		reg(anon, map[string]any{}).want(t, 202, "")
		tok := srv.Wait(1)[0].Token(t)
		anon.do("POST", "/api/register/confirm", map[string]any{"token": tok}).want(t, 201, "")
		if u := squat.do("GET", "/api/session", nil).user(); u["email"] != "" {
			t.Fatalf("the squatter kept %v", u["email"])
		}
		u, row, err := store.UserByVerifiedEmail(ctx, a.st.Q(), "tang@example.test")
		if err != nil || u.Username != "tang" || row.Email != "tang@example.test" {
			t.Fatalf("%v %v", u, err)
		}
		if rows := root.audit(t, "email_released"); len(rows) != 1 {
			t.Fatalf("%v", rows)
		}
		if n := rowCount(t, a, `SELECT COUNT(*) FROM notifications WHERE user_id = ? AND payload LIKE '%email_released%'`, mustUserID(t, a, "squat")); n != 1 {
			t.Fatalf("%d notices", n)
		}
		// Verified now: a second registration for it never gets a link, and
		// nobody takes it.
		before := len(srv.Messages())
		reg(a.newClient("203.0.113.9"), map[string]any{"username": "tang2", "email": "TANG@example.test"}).want(t, 202, "")
		m := srv.Wait(before + 1)[before]
		if contains(m.Text, "/register/confirm#") {
			t.Fatal("a registration for a verified address got a link")
		}
		if u, _, _ := store.UserByVerifiedEmail(ctx, a.st.Q(), "tang@example.test"); u == nil || u.Username != "tang" {
			t.Fatal("the verified holder lost the address")
		}
	})
}

// Addresses outside ASCII: refused while users.emailNonAscii is off, and when
// it is on, one address whatever the spelling of its domain.
func TestAnAddressOutsideASCIIFollowsTheSiteSetting(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		one := a.addUser("one", pw, "member")
		two := a.addUser("two", pw, "member")
		one.do("PATCH", "/api/account", map[string]any{"email": "用户@例子.公司"}).want(t, 400, "email_not_ascii")
		root.do("PUT", "/api/admin/settings/users.emailNonAscii", map[string]any{"value": true}).want(t, 200, "")
		if u := one.do("PATCH", "/api/account", map[string]any{"email": "用户@例子.公司"}).want(t, 200, "").user(); u["email"] != "用户@例子.公司" {
			t.Fatal(u)
		}
		two.do("PATCH", "/api/account", map[string]any{"email": "用户@XN--FSQU00A.xn--55qx5d"}).want(t, 409, "email_taken")
		if n := rowCount(t, a, `SELECT COUNT(*) FROM user_emails WHERE email_key = ?`, "用户@xn--fsqu00a.xn--55qx5d"); n != 1 {
			t.Fatalf("%d rows with the key", n)
		}
		// Off again: what is stored stays, and stays unique; no new one is set.
		root.do("PUT", "/api/admin/settings/users.emailNonAscii", map[string]any{"value": false}).want(t, 200, "")
		two.do("PATCH", "/api/account", map[string]any{"email": "émile@example.test"}).want(t, 400, "email_not_ascii")
	})
}
