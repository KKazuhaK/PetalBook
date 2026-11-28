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

// What a registration costs a tenant does not depend on whether the address
// has an account somewhere else: both are mail of the tenant the page named,
// so the usage export tells an administrator nothing of other tenants. Nor does
// a request to reset a password charge any tenant.
func TestAnonymousMailIsChargedTheSameWhateverExists(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		ctx := context.Background()
		f := newTenFx(t, a)
		srv := fakeMail(t)
		openRegistration(t, f.root, srv)
		if _, err := a.auth.CreateUser(ctx, auth.CreateParams{Username: "old-b", Password: pw, Role: "member", TenantID: f.tenB, Email: "old@example.test", EmailVerified: true}); err != nil {
			t.Fatal(err)
		}
		reg := func(user, email string) {
			t.Helper()
			if err := a.auth.Register(ctx, auth.RegisterParams{TenantID: store0, Username: user, Name: user, Email: email, Password: pw, AcceptTerms: true, IP: "10.0.0.9"}); err != nil {
				t.Fatal(err)
			}
		}
		before := mailCount(t, a, store0)
		reg("newcomer", "new@example.test") // an address nobody has
		afterNew := mailCount(t, a, store0)
		reg("dupe", "old@example.test") // an address held by another tenant
		afterOld := mailCount(t, a, store0)
		if afterNew-before != 1 || afterOld-afterNew != 1 {
			t.Fatalf("a new address cost %d, an existing one %d", afterNew-before, afterOld-afterNew)
		}
		if got := mailCount(t, a, f.tenB); got != 0 {
			t.Fatalf("the other tenant was charged %d for someone else's registration", got)
		}
		// Forgetting a password charges no tenant, whoever has the account.
		if err := a.auth.Forgot(ctx, auth.ForgotParams{Login: "old-b", IP: "10.0.0.8"}); err != nil {
			t.Fatal(err)
		}
		if got := mailCount(t, a, f.tenB); got != 0 {
			t.Fatalf("a reset charged the account's tenant %d", got)
		}
		if got := mailCount(t, a, store0); got != afterOld {
			t.Fatalf("a reset charged the requested tenant: %d -> %d", afterOld, got)
		}
	})
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
