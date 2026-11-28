package api_test

import "testing"

// The captcha of a registration is the named tenant's, the one GET /api/captcha asked for, even when the
// address's domain then joins another organisation.
func TestARegistrationsCaptchaIsTheNamedTenantsNotTheJoinedOnes(t *testing.T) {
	t.Parallel()
	sv := newTCVerify(t)
	eachAccounts(t, tcOpts(sv), func(t *testing.T, a *acc) {
		f := &tlFx{domFx: newJoinFx(t, a).domFx}
		enableMail(t, f.root, fakeMail(t))
		setJoin(f.admC, f.tenC, "beta.example", "auto").want(t, 200, "")
		f.tenantAsks(t, f.admB, f.tenB, "register")
		f.own(t, f.admB, f.tenB, "turnstile", tcPicky).want(t, 200, "")
		c := a.newClient("203.0.113.80")
		body := func(cap any) map[string]any {
			b := map[string]any{"tenant": "acme", "username": "joiner", "name": "J", "email": "joiner@beta.example", "password": pw, "acceptTerms": true}
			if cap != nil {
				b["captcha"] = cap
			}
			return b
		}
		// The page named acme and was asked acme's; beta's address joins beta but the form was acme's.
		if b := tcAsk(c, "register", "acme", ""); b["provider"] != "turnstile" {
			t.Fatal(b)
		}
		c.do("POST", "/api/register", body(nil)).want(t, 400, "captcha_failed")
		c.do("POST", "/api/register", body(map[string]any{"token": "nope"})).want(t, 400, "captcha_failed")
		c.do("POST", "/api/register", body(map[string]any{"token": "t-good"})).want(t, 202, "")
	})
}
