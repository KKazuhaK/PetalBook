package api_test

import "testing"

// The tenant's own provider is read at startup, and dropped with its tenant.
func TestATenantsCaptchaSurvivesARestart(t *testing.T) {
	t.Parallel()
	sv := newTCVerify(t)
	eachAccounts(t, tcOpts(sv), func(t *testing.T, a *acc) {
		f := newTLFx(t, a)
		f.tenantAsks(t, f.admB, f.tenB, "login")
		f.own(t, f.admB, f.tenB, "turnstile", tcPicky).want(t, 200, "")
		c := a.newClient("203.0.113.70")
		a.auth.ForgetTenantCaptcha(f.tenB) // what a restart does to memory
		if b := tcAsk(c, "login", "acme", ""); b["provider"] != "image" {
			t.Fatalf("the provider was still in memory: %v", b)
		}
		if err := a.auth.ReloadTenantCaptcha(t.Context(), f.tenB); err != nil {
			t.Fatal(err)
		}
		if b := tcAsk(c, "login", "acme", ""); b["provider"] != "turnstile" {
			t.Fatalf("%v", b)
		}
		tcLogin(c, "mb", "acme", map[string]any{"token": "t-good"}).want(t, 200, "")
	})
}
