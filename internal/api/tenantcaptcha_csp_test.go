package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"petalbook.app/server/internal/webui"
)

// The page's policy lets a widget load that any page of the server may show: the site's provider and every
// organisation's own that is in force. Otherwise a tenant's Turnstile is blocked in a browser and its forms fail.
func TestThePagePolicyLetsAnOrganisationsOwnProviderLoad(t *testing.T) {
	t.Parallel()
	sv := newTCVerify(t)
	eachAccounts(t, tcOpts(sv), func(t *testing.T, a *acc) {
		f := newTLFx(t, a)
		page := webui.New(fstest.MapFS{"index.html": {Data: []byte("<html></html>")}},
			webui.WithCaptcha(func(r *http.Request) []string { return a.auth.CaptchaProviders(r.Context()) }))
		csp := func(path string) string {
			rec := httptest.NewRecorder()
			page.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
			return rec.Header().Get("Content-Security-Policy")
		}
		const turnstile = "https://challenges.cloudflare.com"
		for _, p := range []string{"/login?tenant=acme", "/admin/tenants/" + f.tenB + "/captcha"} {
			if strings.Contains(csp(p), turnstile) {
				t.Fatalf("%s: a provider nobody uses is allowed", p)
			}
		}
		f.tenantAsks(t, f.admB, f.tenB, "login")
		f.own(t, f.admB, f.tenB, "turnstile", tcPicky).want(t, 200, "") // the site uses the image
		for _, p := range []string{"/login?tenant=acme", "/admin/tenants/" + f.tenB + "/captcha"} {
			c := csp(p)
			if !strings.Contains(c, "script-src 'self' "+turnstile) || !strings.Contains(c, "frame-src "+turnstile) {
				t.Fatalf("%s: %s", p, c)
			}
			if strings.Contains(c, "recaptcha") || strings.Contains(c, "hcaptcha") {
				t.Fatalf("%s: another provider: %s", p, c)
			}
		}
		// Another organisation's, and the site's own, add theirs; taking one away takes its sources away.
		f.own(t, f.admC, f.tenC, "hcaptcha", tcPicky).want(t, 200, "")
		if c := csp("/"); !strings.Contains(c, turnstile) || !strings.Contains(c, "hcaptcha.com") {
			t.Fatal(c)
		}
		f.admB.do("PUT", tcPath(f.tenB), map[string]any{"enabled": false}).want(t, 200, "")
		f.admC.do("DELETE", tcPath(f.tenC), nil).want(t, 204, "")
		if c := csp("/"); strings.Contains(c, turnstile) || strings.Contains(c, "hcaptcha") {
			t.Fatal(c)
		}
		// Only the fixed origins of a known provider ever reach the policy: the key is not in it.
		f.own(t, f.admB, f.tenB, "recaptcha", tcPicky).want(t, 200, "")
		if c := csp("/"); strings.Contains(c, "key-recaptcha") || !strings.Contains(c, "https://www.google.com/recaptcha/") {
			t.Fatal(c)
		}
	})
}
