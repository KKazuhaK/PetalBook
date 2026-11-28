package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"petalbook.app/server/internal/auth"
)

// An organisation's own captcha provider (3.6): its configuration, and which
// provider a form uses. The rule under test: the site's captcha is asked, and
// checked with the site's provider, wherever the site asks one; the
// organisation's own provider serves a form only where the organisation alone
// asks. A tenant administrator controls their own secret, so a provider of
// theirs is never what stands between a request and the site's own rule.

const (
	tcAlways = "tenant-always-passes" // the secret of a provider that accepts any token
	tcPicky  = "tenant-picky-secret"  // accepts only the token "t-good"
	tcSite   = "site-secret-value"    // the site's: accepts only "s-good"
)

// tcVerify is the siteverify endpoint of every third-party provider: what it
// accepts depends on the secret it is sent, like the real ones.
type tcVerify struct {
	srv  *httptest.Server
	mu   sync.Mutex
	got  []string // "secret/token" of every request
	host string   // the hostname it reports
}

func newTCVerify(t *testing.T) *tcVerify {
	f := &tcVerify{host: "notes.example.test"}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		secret, token := r.PostForm.Get("secret"), r.PostForm.Get("response")
		f.mu.Lock()
		f.got = append(f.got, secret+"/"+token)
		host := f.host
		f.mu.Unlock()
		ok := secret == tcAlways || (secret == tcPicky && token == "t-good") || (secret == tcSite && token == "s-good")
		out := map[string]any{"success": ok}
		if ok {
			out["hostname"] = host
		}
		json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *tcVerify) used(secret string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, g := range f.got {
		if strings.HasPrefix(g, secret+"/") {
			n++
		}
	}
	return n
}

func tcOpts(f *tcVerify) accOpts {
	return accOpts{httpc: anyTLSClient(), mod: func(c *auth.Config) { c.CaptchaEndpoint = f.srv.URL }}
}

func tcPath(tenant string) string { return "/api/admin/tenants/" + tenant + "/captcha" }

// own configures a tenant's own provider as its administrator.
func (f *tlFx) own(t *testing.T, adm *client, tenant, provider, secret string) result {
	t.Helper()
	return adm.do("PUT", tcPath(tenant), map[string]any{"enabled": true, "provider": provider, "siteKey": "key-" + provider, "secret": secret})
}

func (f *tlFx) tenantAsks(t *testing.T, adm *client, tenant string, ctxs ...string) {
	t.Helper()
	doc := map[string]any{}
	for _, c := range ctxs {
		doc[c] = map[string]any{"on": true, "when": "always"}
	}
	f.tset(adm, tenant, "captcha.contexts", doc).want(t, 200, "")
}

func tcAsk(c *client, ctx, tenant, account string) map[string]any {
	path := "/api/captcha?ctx=" + ctx
	if tenant != "" {
		path += "&tenant=" + tenant
	}
	if account != "" {
		path += "&account=" + account
	}
	r := c.do("GET", path, nil)
	return r.body
}

func tcLogin(c *client, user, tenant string, cap any) result {
	b := map[string]any{"identifier": user, "password": pw}
	if tenant != "" {
		b["tenant"] = tenant
	}
	if cap != nil {
		b["captcha"] = cap
	}
	return c.do("POST", "/api/login", b)
}

func TestATenantsCaptchaConfigurationIsItsOwnAndTheSecretIsWriteOnly(t *testing.T) {
	t.Parallel()
	sv := newTCVerify(t)
	eachAccounts(t, tcOpts(sv), func(t *testing.T, a *acc) {
		f := newTLFx(t, a)
		got := f.admB.do("GET", tcPath(f.tenB), nil).want(t, 200, "")
		if got.body["enabled"] != false || got.body["provider"] != "image" || got.body["siteKey"] != "" || got.body["hasSecret"] != false || got.body["ready"] != false {
			t.Fatal(got.raw)
		}
		// The provider is one the site has; the image has no keys; a third party needs both before it is on.
		f.admB.do("PUT", tcPath(f.tenB), map[string]any{"provider": "bogus"}).want(t, 400, "invalid")
		f.admB.do("PUT", tcPath(f.tenB), map[string]any{"provider": "image", "siteKey": "k"}).want(t, 400, "invalid")
		f.admB.do("PUT", tcPath(f.tenB), map[string]any{"provider": "turnstile", "enabled": true}).want(t, 400, "captcha_site_key_required")
		f.admB.do("PUT", tcPath(f.tenB), map[string]any{"provider": "turnstile", "siteKey": "1x00", "enabled": true}).want(t, 400, "captcha_secret_required")
		f.admB.do("PUT", tcPath(f.tenB), map[string]any{"provider": "turnstile", "siteKey": "k", "secret": strings.Repeat("x", 5000)}).want(t, 400, "")
		if r := f.admB.do("GET", tcPath(f.tenB), nil).want(t, 200, ""); r.body["provider"] != "image" || r.body["hasSecret"] != false {
			t.Fatalf("a refused change was stored: %s", r.raw)
		}
		// Set, never shown.
		put := f.own(t, f.admB, f.tenB, "turnstile", tcPicky).want(t, 200, "")
		for _, r := range []result{put, f.admB.do("GET", tcPath(f.tenB), nil).want(t, 200, ""), f.root.do("GET", tcPath(f.tenB), nil).want(t, 200, "")} {
			if strings.Contains(r.raw, tcPicky) || r.body["hasSecret"] != true || r.body["siteKey"] != "key-turnstile" || r.body["provider"] != "turnstile" || r.body["enabled"] != true || r.body["ready"] != true {
				t.Fatal(r.raw)
			}
		}
		l := f.admB.do("GET", "/api/admin/tenants/"+f.tenB+"/secrets", nil).want(t, 200, "")
		if strings.Contains(l.raw, tcPicky) || !strings.Contains(l.raw, `"captcha.secret"`) {
			t.Fatal(l.raw)
		}
		// A change that leaves the secret out keeps it; a change of provider clears the keys.
		f.admB.do("PUT", tcPath(f.tenB), map[string]any{"siteKey": "key-2"}).want(t, 200, "")
		if r := f.admB.do("GET", tcPath(f.tenB), nil).want(t, 200, ""); r.body["hasSecret"] != true || r.body["siteKey"] != "key-2" {
			t.Fatal(r.raw)
		}
		f.admB.do("PUT", tcPath(f.tenB), map[string]any{"secret": ""}).want(t, 400, "captcha_secret_required") // it is in use
		f.admB.do("PUT", tcPath(f.tenB), map[string]any{"provider": "hcaptcha"}).want(t, 400, "captcha_site_key_required")
		f.admB.do("PUT", tcPath(f.tenB), map[string]any{"enabled": false}).want(t, 200, "")
		r := f.admB.do("PUT", tcPath(f.tenB), map[string]any{"provider": "hcaptcha"}).want(t, 200, "")
		if r.body["siteKey"] != "" || r.body["hasSecret"] != false || r.body["provider"] != "hcaptcha" {
			t.Fatal("the keys belong to the provider: " + r.raw)
		}
		// The site's own secret and keys are another thing.
		if sec := f.root.do("GET", "/api/admin/security", nil).want(t, 200, ""); sec.body["captcha"].(map[string]any)["hasSecret"] != false {
			t.Fatal(sec.raw)
		}
		// Scoping: another tenant's administrators, members and nobody at all; the other tenants have none.
		for _, c := range []struct {
			who  *client
			code int
		}{{f.admC, 404}, {f.mB, 403}, {f.mC, 403}, {a.newClient(""), 401}} {
			c.who.do("GET", tcPath(f.tenB), nil).want(t, c.code, "")
			c.who.do("PUT", tcPath(f.tenB), map[string]any{"provider": "turnstile"}).want(t, c.code, "")
			c.who.do("DELETE", tcPath(f.tenB), nil).want(t, c.code, "")
			c.who.do("POST", tcPath(f.tenB)+"/test", map[string]any{"token": "x"}).want(t, c.code, "")
		}
		if r := f.admC.do("GET", tcPath(f.tenC), nil).want(t, 200, ""); r.body["provider"] != "image" || r.body["hasSecret"] != false {
			t.Fatal("beta has acme's: " + r.raw)
		}
		f.root.do("GET", tcPath(fakeID), nil).want(t, 404, "not_found")
		// The feature is the site's to grant: off, a tenant's own writes are refused, and what was configured is not in force.
		f.own(t, f.admB, f.tenB, "turnstile", tcPicky).want(t, 200, "")
		f.ownCaptchaOff(t, f.tenB)
		f.admB.do("GET", tcPath(f.tenB), nil).want(t, 403, "feature_off")
		f.admB.do("PUT", tcPath(f.tenB), map[string]any{"enabled": false}).want(t, 403, "feature_off")
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/secrets/captcha.secret", map[string]any{"value": "x"}).want(t, 403, "feature_off")
		f.root.do("DELETE", "/api/admin/tenants/"+f.tenB+"/settings/features.ownCaptcha", nil)
		// Removing it clears the secret too.
		f.root.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/features.ownCaptcha", map[string]any{"siteRule": map[string]any{"value": true}}).want(t, 200, "")
		f.admB.do("DELETE", tcPath(f.tenB), nil).want(t, 204, "")
		if r := f.admB.do("GET", tcPath(f.tenB), nil).want(t, 200, ""); r.body["provider"] != "image" || r.body["hasSecret"] != false || r.body["enabled"] != false {
			t.Fatal(r.raw)
		}
		// The audit has what changed, never a secret.
		a.auth.FlushAudit(t.Context())
		rows := f.admB.do("GET", "/api/admin/audit?kind=tenant_captcha_changed", nil).want(t, 200, "")
		if len(rows.body["items"].([]any)) < 4 || strings.Contains(rows.raw, tcPicky) || strings.Contains(rows.raw, "key-turnstile-secret") || !strings.Contains(rows.raw, `"secret":"changed"`) {
			t.Fatal(rows.raw)
		}
	})
}

func TestATenantsCaptchaCanBeTried(t *testing.T) {
	t.Parallel()
	sv := newTCVerify(t)
	eachAccounts(t, tcOpts(sv), func(t *testing.T, a *acc) {
		f := newTLFx(t, a)
		f.admB.do("POST", tcPath(f.tenB)+"/test", map[string]any{"token": "t-good"}).want(t, 409, "captcha_not_configured")
		f.admB.do("PUT", tcPath(f.tenB), map[string]any{"provider": "image"}).want(t, 200, "")
		f.admB.do("POST", tcPath(f.tenB)+"/test", map[string]any{"token": "t-good"}).want(t, 400, "invalid") // the image needs no test
		// A configuration that is off can be tried before it goes live.
		f.admB.do("PUT", tcPath(f.tenB), map[string]any{"provider": "turnstile", "siteKey": "k", "secret": tcPicky}).want(t, 200, "")
		if r := f.admB.do("POST", tcPath(f.tenB)+"/test", map[string]any{"token": "t-good"}).want(t, 200, ""); r.body["ok"] != true {
			t.Fatal(r.raw)
		}
		r := f.admB.do("POST", tcPath(f.tenB)+"/test", map[string]any{"token": "nope"}).want(t, 200, "")
		if r.body["ok"] != false || r.body["message"] == nil || strings.Contains(r.raw, tcPicky) {
			t.Fatal(r.raw)
		}
		f.admB.do("POST", tcPath(f.tenB)+"/test", map[string]any{}).want(t, 400, "invalid")
		// A token solved on another host is refused, as on the forms.
		sv.mu.Lock()
		sv.host = "evil.example"
		sv.mu.Unlock()
		if r := f.admB.do("POST", tcPath(f.tenB)+"/test", map[string]any{"token": "t-good"}).want(t, 200, ""); r.body["ok"] != false {
			t.Fatal(r.raw)
		}
		a.auth.FlushAudit(t.Context())
		if rows := f.admB.do("GET", "/api/admin/audit?kind=tenant_captcha_test", nil).want(t, 200, ""); len(rows.body["items"].([]any)) != 3 || strings.Contains(rows.raw, "t-good") {
			t.Fatal(rows.raw)
		}
	})
}

// Where only the organisation asks a captcha, its own provider is what the
// form shows and what the answer is checked with. Another organisation, and a
// request naming none, are asked nothing.
func TestATenantsOwnProviderServesItsFormsWhereOnlyItAsks(t *testing.T) {
	t.Parallel()
	sv := newTCVerify(t)
	eachAccounts(t, tcOpts(sv), func(t *testing.T, a *acc) {
		f := newTLFx(t, a)
		srv := fakeMail(t)
		enableMail(t, f.root, srv)
		f.tenantAsks(t, f.admB, f.tenB, "login", "forgot", "register")
		c := a.newClient("203.0.113.60")
		// No provider of its own: the site's (the image) is what acme's forms show.
		for _, ctx := range []string{"login", "forgot", "register"} {
			if b := tcAsk(c, ctx, "acme", ""); b["required"] != true || b["provider"] != "image" || b["id"] == nil {
				t.Fatalf("%s: %v", ctx, b)
			}
		}
		f.own(t, f.admB, f.tenB, "turnstile", tcPicky).want(t, 200, "")
		for _, ctx := range []string{"login", "forgot", "register"} {
			if b := tcAsk(c, ctx, "acme", ""); b["required"] != true || b["provider"] != "turnstile" || b["siteKey"] != "key-turnstile" || b["id"] != nil || b["image"] != nil {
				t.Fatalf("%s: %v", ctx, b)
			}
			if b := tcAsk(c, ctx, "beta", ""); b["required"] != false {
				t.Fatalf("beta %s: %v", ctx, b)
			}
			if b := tcAsk(c, ctx, "", ""); b["required"] != false {
				t.Fatalf("unscoped %s: %v", ctx, b)
			}
		}
		// The invitation and the setup are the site's, whatever is named.
		if b := tcAsk(c, "invite", "acme", ""); b["required"] != false {
			t.Fatal(b)
		}
		// The answer is checked with acme's secret, and by it alone.
		tcLogin(c, "mb", "acme", nil).want(t, 400, "captcha_failed")
		tcLogin(c, "mb", "acme", map[string]any{"token": "nope"}).want(t, 400, "captcha_failed")
		tcLogin(c, "mb", "acme", map[string]any{"token": "t-good"}).want(t, 200, "")
		if sv.used(tcPicky) != 2 || sv.used(tcSite) != 0 {
			t.Fatalf("the provider was asked %v", sv.got)
		}
		forgot(c, "mb", "tenant", "acme").want(t, 400, "captcha_failed")
		forgot(c, "mb", "tenant", "acme", "captcha", map[string]any{"token": "t-good"}).want(t, 202, "")
		// An image token for a form that wants the provider's is no answer.
		b := tcAsk(c, "login", "beta", "")
		_ = b
		// Its own image: the built-in one, chosen by the organisation while the site's is a third party.
		f.admB.do("PUT", tcPath(f.tenB), map[string]any{"enabled": false}).want(t, 200, "")
		if b := tcAsk(c, "login", "acme", ""); b["provider"] != "image" {
			t.Fatalf("a disabled provider is not used: %v", b)
		}
		f.admB.do("PUT", tcPath(f.tenB), map[string]any{"provider": "image", "enabled": true}).want(t, 200, "")
		b = tcAsk(c, "login", "acme", "")
		tcLogin(c, "mb", "acme", map[string]any{"id": b["id"], "answer": auth.CaptchaSolution(a.auth, b["id"].(string))}).want(t, 200, "")
		// A provider that cannot be used falls back to the site's: the secret cleared as a secret.
		f.own(t, f.admB, f.tenB, "turnstile", tcPicky).want(t, 200, "")
		if b := tcAsk(c, "login", "acme", ""); b["provider"] != "turnstile" {
			t.Fatal(b)
		}
		f.admB.do("DELETE", "/api/admin/tenants/"+f.tenB+"/secrets/captcha.secret", nil).want(t, 200, "")
		if b := tcAsk(c, "login", "acme", ""); b["provider"] != "image" {
			t.Fatalf("a provider without its secret is not used: %v", b)
		}
		if r := f.admB.do("GET", tcPath(f.tenB), nil).want(t, 200, ""); r.body["ready"] != false || r.body["hasSecret"] != false {
			t.Fatal(r.raw)
		}
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/secrets/captcha.secret", map[string]any{"value": tcPicky}).want(t, 200, "")
		if b := tcAsk(c, "login", "acme", ""); b["provider"] != "turnstile" {
			t.Fatalf("the secret set as a secret is in force at once: %v", b)
		}
		// What the site no longer grants is not in force.
		f.ownCaptchaOff(t, f.tenB)
		if b := tcAsk(c, "login", "acme", ""); b["provider"] != "image" {
			t.Fatalf("the feature is off: %v", b)
		}
	})
}

// The attack: an organisation's administrator makes their own captcha accept
// anything and names the organisation on a sign-in for an account outside it.
// The site's captcha is still what is asked, and what is checked.
func TestATenantsOwnProviderNeverStandsInForTheSitesCaptcha(t *testing.T) {
	t.Parallel()
	sv := newTCVerify(t)
	eachAccounts(t, tcOpts(sv), func(t *testing.T, a *acc) {
		f := newTLFx(t, a)
		srv := fakeMail(t)
		enableMail(t, f.root, srv)
		f.tenantAsks(t, f.admB, f.tenB, "login", "forgot", "register")
		f.own(t, f.admB, f.tenB, "turnstile", tcAlways).want(t, 200, "")
		c := a.newClient("203.0.113.61")
		// The site asks the sign-in always (the image).
		f.root.putSetting("captcha.contexts", map[string]any{"login": map[string]any{"on": true, "when": "always"}, "forgot": map[string]any{"on": true, "when": "always"},
			"register": map[string]any{"on": true, "when": "always"}}).want(t, 200, "")
		for _, ctx := range []string{"login", "forgot", "register"} {
			if b := tcAsk(c, ctx, "acme", ""); b["required"] != true || b["provider"] != "image" || b["id"] == nil {
				t.Fatalf("%s: the site's provider is shown: %v", ctx, b)
			}
		}
		// md is in the default tenant, mb in acme; naming acme changes nothing about either.
		for _, who := range []string{"md", "mb", "root"} {
			tcLogin(c, who, "acme", map[string]any{"token": "anything"}).want(t, 400, "captcha_failed")
			tcLogin(c, who, "acme", nil).want(t, 400, "captcha_failed")
			tcLogin(c, who, "", map[string]any{"token": "anything"}).want(t, 400, "captcha_failed")
		}
		forgot(c, "md", "tenant", "acme", "captcha", map[string]any{"token": "anything"}).want(t, 400, "captcha_failed")
		if sv.used(tcAlways) != 0 {
			t.Fatalf("the tenant's provider was asked: %v", sv.got)
		}
		// The site's answer passes, once, and does so for an account outside the organisation too.
		b := tcAsk(c, "login", "acme", "")
		tcLogin(c, "md", "acme", map[string]any{"id": b["id"], "answer": auth.CaptchaSolution(a.auth, b["id"].(string))}).want(t, 200, "")

		// The site asks after failures only: until then only the organisation asks, so its own
		// provider is no weaker than no captcha; once the site's count is reached the site's is back.
		f.root.putSetting("captcha.contexts", map[string]any{"login": map[string]any{"on": true, "when": "after_failures", "failures": 2}, "forgot": map[string]any{"on": false},
			"register": map[string]any{"on": false}}).want(t, 200, "")
		d := a.newClient("203.0.113.62")
		if b := tcAsk(d, "login", "acme", "md"); b["provider"] != "turnstile" {
			t.Fatalf("only acme asks: %v", b)
		}
		tcLogin(d, "md", "acme", map[string]any{"token": "anything"}).want(t, 200, "")
		e := a.newClient("203.0.113.63")
		for range 2 {
			d := map[string]any{"identifier": "md", "password": "wrong password here", "tenant": "acme", "captcha": map[string]any{"token": "anything"}}
			e.do("POST", "/api/login", d).want(t, 401, "")
		}
		// Two failures of the name: the site asks now, and acme's provider is not the answer.
		if b := tcAsk(e, "login", "acme", "md"); b["provider"] != "image" || b["required"] != true {
			t.Fatalf("the site asks after its failures: %v", b)
		}
		tcLogin(e, "md", "acme", map[string]any{"token": "anything"}).want(t, 400, "captcha_failed")
		before := sv.used(tcAlways)
		// The site's own provider, a third party: acme's accepts anything and the site's does not.
		f.root.putSetting("captcha.contexts", map[string]any{"login": map[string]any{"on": false}}).want(t, 200, "")
		f.root.putSetting("captcha.provider", "recaptcha").want(t, 200, "")
		f.root.do("PUT", "/api/admin/security", map[string]any{"captcha": map[string]any{"siteKey": "site-key", "secret": tcSite}}).want(t, 200, "")
		f.root.putSetting("captcha.contexts", map[string]any{"login": map[string]any{"on": true, "when": "always"}}).want(t, 200, "")
		g := a.newClient("203.0.113.64")
		if b := tcAsk(g, "login", "acme", ""); b["provider"] != "recaptcha" || b["siteKey"] != "site-key" {
			t.Fatalf("the site's provider and key: %v", b)
		}
		tcLogin(g, "md", "acme", map[string]any{"token": "anything"}).want(t, 400, "captcha_failed")
		tcLogin(g, "md", "acme", map[string]any{"token": "s-good"}).want(t, 200, "")
		if sv.used(tcAlways) != before || sv.used(tcSite) < 2 {
			t.Fatalf("which provider checked what: %v", sv.got)
		}
	})
}

// The page that comes before the identifier, and the answer to a wrong one,
// do not depend on whether the account exists or which organisation holds it.
func TestTheCaptchaChoiceTellsNothingAboutAnAccount(t *testing.T) {
	t.Parallel()
	sv := newTCVerify(t)
	eachAccounts(t, tcOpts(sv), func(t *testing.T, a *acc) {
		f := newTLFx(t, a)
		f.tenantAsks(t, f.admB, f.tenB, "login")
		f.own(t, f.admB, f.tenB, "hcaptcha", tcPicky).want(t, 200, "")
		c := a.newClient("203.0.113.65")
		shape := func(who string) string {
			b := tcAsk(c, "login", "acme", who)
			delete(b, "id")
			delete(b, "image")
			j, _ := json.Marshal(b)
			return string(j)
		}
		// In acme, in the default tenant, a site administrator, and nobody.
		want := shape("mb")
		for _, who := range []string{"md", "root", "nobody-has-this-name", "adm-b"} {
			if got := shape(who); got != want {
				t.Fatalf("%s: %s differs from %s", who, got, want)
			}
		}
		// The refusal is the same for each too, and not a different failure than a wrong password.
		for _, who := range []string{"mb", "md", "nobody-has-this-name"} {
			tcLogin(c, who, "acme", map[string]any{"token": "nope"}).want(t, 400, "captcha_failed")
		}
		// With the right token the answer is the password's, the same for all.
		tcLogin(c, "nobody-has-this-name", "acme", map[string]any{"token": "t-good"}).want(t, 401, "")
		tcLogin(c, "md", "acme", map[string]any{"token": "t-good"}).want(t, 200, "")
	})
}

// A third-party site provider needs its keys for a tenant to ask a captcha of
// its own, unless the tenant has a provider of its own; and a provider the
// tenant's forms depend on is not removed from under them.
func TestATenantsOwnProviderStandsInForTheSitesKeys(t *testing.T) {
	t.Parallel()
	sv := newTCVerify(t)
	eachAccounts(t, tcOpts(sv), func(t *testing.T, a *acc) {
		f := newTLFx(t, a)
		f.root.putSetting("captcha.provider", "turnstile").want(t, 200, "")
		f.tset(f.admB, f.tenB, "captcha.contexts", map[string]any{"login": map[string]any{"on": true, "when": "always"}}).want(t, 400, "captcha_site_key_required")
		f.own(t, f.admB, f.tenB, "hcaptcha", tcPicky).want(t, 200, "")
		f.tenantAsks(t, f.admB, f.tenB, "login")
		c := a.newClient("203.0.113.66")
		if b := tcAsk(c, "login", "acme", ""); b["provider"] != "hcaptcha" || b["siteKey"] != "key-hcaptcha" {
			t.Fatal(b)
		}
		// Taking it away would leave the form with a site provider that has no keys.
		f.admB.do("PUT", tcPath(f.tenB), map[string]any{"enabled": false}).want(t, 400, "captcha_needed")
		f.admB.do("DELETE", tcPath(f.tenB), nil).want(t, 400, "captcha_needed")
		f.admB.do("DELETE", "/api/admin/tenants/"+f.tenB+"/secrets/captcha.secret", nil).want(t, 400, "captcha_needed")
		f.admB.do("PUT", tcPath(f.tenB), map[string]any{"enabled": true}).want(t, 200, "")
		tcLogin(c, "md", "acme", map[string]any{"token": "t-good"}).want(t, 200, "")
		f.tset(f.admB, f.tenB, "captcha.contexts", map[string]any{"login": map[string]any{"on": false}}).want(t, 200, "")
		f.admB.do("DELETE", tcPath(f.tenB), nil).want(t, 204, "")
	})
}
