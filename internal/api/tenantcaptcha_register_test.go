package api_test

import (
	"fmt"
	"testing"

	"petalbook.app/server/internal/auth"
)

// A registration in an organisation is asked its own provider's answer where
// only the organisation asks, and the site's where the site asks too.
func TestARegistrationIsCheckedByTheProviderThePlanNames(t *testing.T) {
	t.Parallel()
	sv := newTCVerify(t)
	eachAccounts(t, tcOpts(sv), func(t *testing.T, a *acc) {
		f := newTLFx(t, a)
		openRegistration(t, f.root, fakeMail(t))
		f.root.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/registration.mode", map[string]any{"value": "open"}).want(t, 200, "")
		f.tenantAsks(t, f.admB, f.tenB, "register")
		f.own(t, f.admB, f.tenB, "turnstile", tcPicky).want(t, 200, "")
		n := 0
		reg := func(user string, cap any) result {
			n++
			b := map[string]any{"tenant": "acme", "username": user, "name": user, "email": user + "@example.test", "password": pw, "acceptTerms": true}
			if cap != nil {
				b["captcha"] = cap
			}
			return a.newClient(fmt.Sprintf("198.51.100.%d", n)).do("POST", "/api/register", b)
		}
		reg("nobody1", nil).want(t, 400, "captcha_failed")
		reg("nobody2", map[string]any{"token": "nope"}).want(t, 400, "captcha_failed")
		reg("nobody3", map[string]any{"token": "t-good"}).want(t, 202, "")
		// Once the site asks as well, its provider (the image) is the one that counts.
		f.root.putSetting("captcha.contexts", map[string]any{"register": map[string]any{"on": true, "when": "always"}}).want(t, 200, "")
		reg("nobody4", map[string]any{"token": "t-good"}).want(t, 400, "captcha_failed")
		b := tcAsk(a.newClient("198.51.100.99"), "register", "acme", "")
		reg("nobody5", map[string]any{"id": b["id"], "answer": auth.CaptchaSolution(a.auth, b["id"].(string))}).want(t, 202, "")
	})
}
