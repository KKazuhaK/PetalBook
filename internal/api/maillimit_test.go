package api_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"petalbook.app/server/internal/auth"
)

func mailCount(t *testing.T, a *acc, tenant string) int64 {
	t.Helper()
	if err := a.h.FlushUsage(context.Background()); err != nil {
		t.Fatal(err)
	}
	return dayRows(t, a, tenant)["mail"]
}

// An invitation sent by mail is mail of the inviting tenant: counted, and held
// to rate.mailPerDay. A bulk invitation past the limit stops at it, and the rows
// it did not send say so (mail "limited", with the link to hand over).
func TestInvitationMailIsCountedAndHeldToTheDailyLimit(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		srv := fakeMail(t)
		enableMail(t, f.root, srv)
		f.root.do("PUT", "/api/admin/tenants/"+store0+"/settings/rate.mailPerDay", map[string]any{"siteRule": map[string]any{"value": 2}}).want(t, 200, "")
		rows := []map[string]any{}
		for i := range 4 {
			rows = append(rows, map[string]any{"user": fmt.Sprintf("inv%d@example.test", i), "role": "member"})
		}
		r := f.admA.do("POST", "/api/admin/members/batch", map[string]any{"rows": rows}).want(t, 200, "")
		sent, limited := 0, 0
		for _, x := range r.body["results"].([]any) {
			switch x.(map[string]any)["mail"] {
			case "sent":
				sent++
			case "limited":
				limited++
			}
		}
		if sent != 2 || limited != 2 {
			t.Fatalf("sent %d, limited %d: %s", sent, limited, r.raw)
		}
		if got := mailCount(t, a, store0); got != 2 {
			t.Fatalf("counted %d", got)
		}
		if n := len(srv.Wait(2)); n != 2 {
			t.Fatalf("%d messages", n)
		}
	})
}

const store0 = "0000000000" // the default tenant

// What a registration costs is charged by the request, not by what became of
// it: one message of the tenant the page named (none, for an organisation of
// the person's own), whether the address is new, held by a live account of another
// tenant, a disabled or a deleted account, or marked by a tenant administrator,
// and whether or not an organisation's caps are reached. So the usage export
// tells an administrator nothing of other tenants' accounts. A request to reset a
// password charges no tenant in any of those cases.
func TestAnonymousMailIsChargedTheSameWhateverExists(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		ctx := context.Background()
		f := newTenFx(t, a)
		srv := fakeMail(t)
		openRegistration(t, f.root, srv)
		f.root.putSetting("features.orgCreation", true).want(t, 200, "")
		sql := func(q string, args ...any) {
			t.Helper()
			if _, err := a.st.Q().Exec(ctx, q, args...); err != nil {
				t.Fatal(err)
			}
		}
		n := 0
		// Each case makes an address in the state it names and returns it.
		cases := []struct {
			name  string
			setup func(user, email string)
		}{
			{"a new address", func(user, email string) {}},
			{"a live account of another tenant", func(user, email string) {
				mk(t, a, user, email, f.tenB)
			}},
			{"a disabled account", func(user, email string) {
				mk(t, a, user, email, f.tenB)
				sql(`UPDATE users SET disabled = 1 WHERE username = ?`, user)
			}},
			{"a deleted account", func(user, email string) {
				mk(t, a, user, email, f.tenB)
				sql(`UPDATE users SET deleted_at = 1700000000000 WHERE username = ?`, user)
			}},
			{"an address a tenant administrator marked", func(user, email string) {
				mk(t, a, user, email, f.tenB)
				sql(`UPDATE user_emails SET verified_by = 'tenant_admin' WHERE email = ?`, email)
			}},
		}
		kinds := []struct {
			name string
			org  bool
			// charged is what each tenant is charged for one request.
			defaultCharge int64
		}{{"a tenant registration", false, 1}, {"an organisation registration", true, 0}}
		counts := func() [2]int64 { return [2]int64{mailCount(t, a, store0), mailCount(t, a, f.tenB)} }
		for pass := 0; pass < 3; pass++ {
			if pass == 2 { // the organisation caps are reached
				f.root.putSetting("orgCreation.perPerson", 0).want(t, 200, "")
			}
			k := kinds[0]
			if pass > 0 {
				k = kinds[1]
			}
			for _, c := range cases {
				n++
				user, email := fmt.Sprintf("u%d", n), fmt.Sprintf("u%d@example.test", n)
				c.setup(user, email)
				before := counts()
				p := auth.RegisterParams{TenantID: store0, Username: fmt.Sprintf("r%d", n), Name: "R", Email: email, Password: pw, AcceptTerms: true, IP: fmt.Sprintf("10.0.%d.9", n)}
				if k.org {
					p.Org = &auth.OrgParams{Name: fmt.Sprintf("Org %d", n), Slug: fmt.Sprintf("org-%d", n)}
				}
				if err := a.auth.Register(ctx, p); err != nil {
					t.Fatalf("%s, %s: %v", k.name, c.name, err)
				}
				after := counts()
				if d := [2]int64{after[0] - before[0], after[1] - before[1]}; d != [2]int64{k.defaultCharge, 0} {
					t.Errorf("pass %d, %s, %s: charged %v, want [%d 0]", pass, k.name, c.name, d, k.defaultCharge)
				}
			}
		}
		// Forgetting a password charges no tenant: whoever has the account, or no one does.
		forgot := []struct {
			name  string
			login string
			setup func()
		}{
			{"no account", "nobody", func() {}},
			{"a live account", "fg-live", func() { mk(t, a, "fg-live", "fg-live@example.test", f.tenB) }},
			{"a disabled account", "fg-dis", func() {
				mk(t, a, "fg-dis", "fg-dis@example.test", f.tenB)
				sql(`UPDATE users SET disabled = 1 WHERE username = 'fg-dis'`)
			}},
			{"a deleted account", "fg-del", func() {
				mk(t, a, "fg-del", "fg-del@example.test", f.tenB)
				sql(`UPDATE users SET deleted_at = 1700000000000 WHERE username = 'fg-del'`)
			}},
		}
		for i, c := range forgot {
			c.setup()
			before := counts()
			if err := a.auth.Forgot(ctx, auth.ForgotParams{Login: c.login, IP: fmt.Sprintf("10.9.0.%d", i+1)}); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			if after := counts(); after != before {
				t.Errorf("a reset for %s charged %v -> %v", c.name, before, after)
			}
		}
	})
}

func mk(t *testing.T, a *acc, user, email, tenant string) {
	t.Helper()
	if _, err := a.auth.CreateUser(context.Background(), auth.CreateParams{Username: user, Password: pw, Role: "member", TenantID: tenant, Email: email, EmailVerified: true}); err != nil {
		t.Fatal(err)
	}
}

// Security mail (a verification, a reset, a registration's confirmation) is
// counted but never held to the day's limit: anonymous requests must not be able
// to use up a tenant's allowance and so lock its people out of their passwords.
func TestSecurityMailIsCountedButNotCapped(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		srv := fakeMail(t)
		enableMail(t, f.root, srv)
		f.root.do("PUT", "/api/admin/tenants/"+store0+"/settings/rate.mailPerDay", map[string]any{"siteRule": map[string]any{"value": 1}}).want(t, 200, "")
		f.memA.do("PATCH", "/api/account", map[string]any{"email": "mem@example.test"}).want(t, 200, "")
		for range 3 {
			f.memA.do("POST", "/api/account/email/verify", map[string]any{}).want(t, 202, "")
		}
		if got := mailCount(t, a, store0); got != 3 {
			t.Fatalf("counted %d, want 3", got)
		}
		// Other mail is held: the allowance of 1 is spent.
		r := f.admA.do("POST", "/api/admin/members/batch", map[string]any{"rows": []map[string]any{{"user": "x@example.test", "role": "member"}}}).want(t, 200, "")
		if r.body["results"].([]any)[0].(map[string]any)["mail"] != "limited" {
			t.Fatalf("%s", r.raw)
		}
	})
}

// Two requests that each find one message left of the day's limit do not both
// send: the slot is taken in memory in one step.
func TestTheLastMessageOfTheDayGoesToOneRequestOnly(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		srv := fakeMail(t)
		enableMail(t, f.root, srv)
		f.root.do("PUT", "/api/admin/tenants/"+store0+"/settings/rate.mailPerDay", map[string]any{"siteRule": map[string]any{"value": 3}}).want(t, 200, "")
		var wg sync.WaitGroup
		results := make(chan string, 12)
		for i := range 12 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				body := fmt.Sprintf(`{"rows":[{"user":"race%d@example.test","role":"member"}]}`, i)
				req := httptest.NewRequest("POST", "/api/admin/members/batch", strings.NewReader(body))
				req.Host = strings.TrimPrefix(strings.TrimPrefix(a.public, "https://"), "http://")
				req.Header.Set("Content-Type", "application/json")
				for _, ck := range f.admA.cookies {
					req.AddCookie(&http.Cookie{Name: ck.Name, Value: ck.Value})
				}
				rec := httptest.NewRecorder()
				a.h.ServeHTTP(rec, req)
				switch {
				case strings.Contains(rec.Body.String(), `"mail":"sent"`):
					results <- "sent"
				default:
					results <- "other"
				}
			}()
		}
		wg.Wait()
		close(results)
		sent := 0
		for r := range results {
			if r == "sent" {
				sent++
			}
		}
		if sent != 3 {
			t.Fatalf("%d sent, the limit is 3", sent)
		}
	})
}
