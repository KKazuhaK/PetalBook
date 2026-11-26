package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"petalbook.app/server/internal/auth"
)

// challenge asks for a captcha and returns the answer the test may use.
func (c *client) challenge(t *testing.T, ctx, account string) (map[string]any, string) {
	t.Helper()
	path := "/api/captcha?ctx=" + ctx
	if account != "" {
		path += "&account=" + account
	}
	r := c.do("GET", path, nil).want(t, 200, "")
	id, _ := r.body["id"].(string)
	return r.body, auth.CaptchaSolution(c.a.auth, id)
}

func TestImageCaptchaIsCheckedBeforeTheHashAndUsedOnce(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		c := a.newClient("203.0.113.30")
		// Off by default: nothing is asked.
		if r := c.do("GET", "/api/captcha?ctx=login", nil).want(t, 200, ""); r.body["required"] != false || len(r.body) != 1 {
			t.Fatal(r.raw)
		}
		admin.putSecurity(t, map[string]any{"captcha": map[string]any{"contexts": map[string]any{"login": map[string]any{"on": true, "when": "always"}}}})
		body, answer := c.challenge(t, "login", "kazuha")
		if body["required"] != true || body["provider"] != "image" || body["siteKey"] != "" || !strings.HasPrefix(body["image"].(string), "data:image/png;base64,") || answer == "" {
			t.Fatal(body)
		}
		ops := auth.HashOps(a.auth)
		// Missing, wrong and unknown answers: 400 and no password work, and not counted as a wrong password.
		c.do("POST", "/api/login", good("kazuha")).want(t, 400, "captcha_failed")
		login := func(id, ans string) result {
			return c.do("POST", "/api/login", map[string]any{"username": "kazuha", "password": pw, "captcha": map[string]any{"id": id, "answer": ans}})
		}
		login(body["id"].(string), "00000").want(t, 400, "captcha_failed")
		login("nope", answer).want(t, 400, "captcha_failed")
		if auth.HashOps(a.auth) != ops {
			t.Fatal("a password was hashed before the captcha was right")
		}
		// A wrong answer used the challenge up, even if the next one is right.
		login(body["id"].(string), answer).want(t, 400, "captcha_failed")
		// A fresh one passes, once.
		body, answer = c.challenge(t, "login", "kazuha")
		login(body["id"].(string), answer).want(t, 200, "")
		login(body["id"].(string), answer).want(t, 400, "captcha_failed")
		// Failures of the password were not counted by the failed captchas: ten are still free.
		for range 9 {
			b, ans := c.challenge(t, "login", "kazuha")
			c.do("POST", "/api/login", map[string]any{"username": "kazuha", "password": "wrong password here", "captcha": map[string]any{"id": b["id"], "answer": ans}}).want(t, 401, "")
		}
		if n := auth.CaptchaHeld(a.auth); n > 10_000 {
			t.Fatalf("%d challenges held", n)
		}
		// The contexts that exist; the reserved ones never ask; others are refused.
		for _, ctx := range []string{"forgot", "register"} {
			if r := c.do("GET", "/api/captcha?ctx="+ctx, nil).want(t, 200, ""); r.body["required"] != false {
				t.Fatal(r.raw)
			}
		}
		c.do("GET", "/api/captcha?ctx=bogus", nil).want(t, 400, "invalid")
		c.do("GET", "/api/captcha", nil).want(t, 400, "invalid")
	})
}

func TestCaptchaAfterFailuresByAddressAndUsername(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		admin.putSecurity(t, map[string]any{"captcha": map[string]any{"contexts": map[string]any{"login": map[string]any{"on": true, "failures": 2}}}})
		required := func(c *client, account string) bool {
			return c.do("GET", "/api/captcha?ctx=login&account="+account, nil).want(t, 200, "").body["required"] == true
		}
		c := a.newClient("203.0.113.31")
		if required(c, "kazuha") {
			t.Fatal("asked before any failure")
		}
		c.do("POST", "/api/login", bad("someone")).want(t, 401, "")
		if required(c, "kazuha") {
			t.Fatal("asked after one failure")
		}
		c.do("POST", "/api/login", bad("someone")).want(t, 401, "")
		// Two failures of the address: now asked, whatever the name.
		if !required(c, "kazuha") || !required(c, "") {
			t.Fatal("not asked after two failures of the address")
		}
		// Another address is asked about a name that was failed twice.
		d := a.newClient("203.0.113.32")
		if required(d, "kazuha") {
			t.Fatal("a fresh address and name was asked")
		}
		d.do("POST", "/api/login", bad("kazuha")).want(t, 401, "")
		a.newClient("203.0.113.33").do("POST", "/api/login", bad("kazuha")).want(t, 401, "")
		if !required(a.newClient("203.0.113.34"), "KAZUHA%20") {
			t.Fatal("a name with two failures was not asked about")
		}
		// The captcha is not asked of invitations and setup unless switched on.
		if r := c.do("GET", "/api/captcha?ctx=invite", nil).want(t, 200, ""); r.body["required"] != false {
			t.Fatal(r.raw)
		}
	})
}

func TestCaptchaOnSetupAndInvites(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		// Setup first: no administrator yet, so no way to set it; use the service to switch it on.
		a.setSetting(t, "captcha.contexts", map[string]any{"setup": map[string]any{"on": true, "when": "always"}, "invite": map[string]any{"on": true, "when": "always"}})
		c := a.newClient("203.0.113.35")
		tok := a.auth.SetupToken()
		c.do("POST", "/api/setup", map[string]any{"token": tok, "username": "kazuha", "password": pw}).want(t, 400, "captcha_failed")
		b, ans := c.challenge(t, "setup", "")
		// A wrong token with the right captcha is a failure of the address, 403.
		c.do("POST", "/api/setup", map[string]any{"token": "wrong", "username": "kazuha", "password": pw, "captcha": map[string]any{"id": b["id"], "answer": ans}}).want(t, 403, "forbidden")
		b, ans = c.challenge(t, "setup", "")
		c.do("POST", "/api/setup", map[string]any{"token": tok, "username": "kazuha", "password": pw, "captcha": map[string]any{"id": b["id"], "answer": ans}}).want(t, 201, "")
		// Invitations: the check and the accept both need one.
		inv := c.do("POST", "/api/invites", map[string]any{}).want(t, 201, "")
		d := a.newClient("203.0.113.36")
		d.do("POST", "/api/invites/check", map[string]any{"token": inv.body["token"]}).want(t, 400, "captcha_failed")
		b, ans = d.challenge(t, "invite", "")
		d.do("POST", "/api/invites/check", map[string]any{"token": inv.body["token"], "captcha": map[string]any{"id": b["id"], "answer": ans}}).want(t, 200, "")
		d.do("POST", "/api/invites/accept", map[string]any{"token": inv.body["token"], "username": "tang", "password": pw}).want(t, 400, "captcha_failed")
		b, ans = d.challenge(t, "invite", "")
		d.do("POST", "/api/invites/accept", map[string]any{"token": inv.body["token"], "username": "tang", "password": pw, "captcha": map[string]any{"id": b["id"], "answer": ans}}).want(t, 201, "")
	})
}

// fakeSiteverify is a third-party captcha provider's siteverify endpoint.
type fakeSiteverify struct {
	srv    *httptest.Server
	mu     sync.Mutex
	hosts  map[string]string // token -> hostname reported
	status int
	got    []string // the secrets it was sent
}

func newSiteverify(t *testing.T) *fakeSiteverify {
	f := &fakeSiteverify{hosts: map[string]string{"good": "notes.example.test", "other-host": "evil.example", "no-host": ""}, status: 200}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.mu.Lock()
		f.got = append(f.got, r.PostForm.Get("secret"))
		st := f.status
		host, ok := f.hosts[r.PostForm.Get("response")]
		f.mu.Unlock()
		if st != 200 {
			http.Error(w, "down", st)
			return
		}
		out := map[string]any{"success": ok}
		if ok && host != "" {
			out["hostname"] = host
		}
		json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func TestThirdPartyCaptcha(t *testing.T) {
	t.Parallel()
	sv := newSiteverify(t)
	o := accOpts{httpc: anyTLSClient(), mod: func(c *auth.Config) { c.CaptchaEndpoint = sv.srv.URL }}
	eachAccounts(t, o, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		on := map[string]any{"login": map[string]any{"on": true, "when": "always"}}
		// Saving: the site key and the secret are both needed, in the same request as the provider.
		admin.do("PUT", "/api/admin/security", map[string]any{"captcha": map[string]any{"provider": "turnstile", "contexts": on}}).want(t, 400, "captcha_site_key_required")
		admin.do("PUT", "/api/admin/security", map[string]any{"captcha": map[string]any{"provider": "turnstile", "siteKey": "1x00", "contexts": on}}).want(t, 400, "captcha_secret_required")
		admin.do("PUT", "/api/admin/security", map[string]any{"captcha": map[string]any{"provider": "bogus"}}).want(t, 400, "invalid")
		// Nothing was stored by the refusals.
		if g := admin.do("GET", "/api/admin/security", nil).want(t, 200, ""); g.body["captcha"].(map[string]any)["provider"] != "image" {
			t.Fatal(g.raw)
		}
		r := admin.putSecurity(t, map[string]any{"captcha": map[string]any{"provider": "turnstile", "siteKey": "1x00", "secret": "s3cret-value", "contexts": on}})
		cp := r.body["captcha"].(map[string]any)
		if cp["provider"] != "turnstile" || cp["siteKey"] != "1x00" || cp["hasSecret"] != true || strings.Contains(r.raw, "s3cret-value") || strings.Contains(r.raw, "secretEnc") {
			t.Fatal(r.raw)
		}
		// The web app's page policy follows the provider in force (webui.WithCaptcha).
		if got := a.auth.CaptchaProvider(context.Background()); got != "turnstile" {
			t.Fatalf("CaptchaProvider %q", got)
		}
		c := a.newClient("203.0.113.40")
		ch := c.do("GET", "/api/captcha?ctx=login", nil).want(t, 200, "")
		if ch.body["provider"] != "turnstile" || ch.body["siteKey"] != "1x00" || ch.body["id"] != nil || ch.body["image"] != nil {
			t.Fatal(ch.raw)
		}
		login := func(tok string) result {
			return c.do("POST", "/api/login", map[string]any{"username": "kazuha", "password": pw, "captcha": map[string]any{"token": tok}})
		}
		login("").want(t, 400, "captcha_failed")
		login("rejected").want(t, 400, "captcha_failed")
		login("other-host").want(t, 400, "captcha_failed") // solved on another host
		login("good").want(t, 200, "")
		login("no-host").want(t, 200, "") // a provider that does not report a host is not held to it
		if len(sv.got) == 0 || sv.got[0] != "s3cret-value" {
			t.Fatalf("the provider was sent %v", sv.got)
		}
		// A provider that cannot be reached, or answers nonsense, fails the form.
		sv.mu.Lock()
		sv.status = 503
		sv.mu.Unlock()
		ops := auth.HashOps(a.auth)
		login("good").want(t, 400, "captcha_failed")
		if auth.HashOps(a.auth) != ops {
			t.Fatal("hashed after the captcha could not be checked")
		}
		sv.mu.Lock()
		sv.status = 200
		sv.mu.Unlock()
		// Another provider needs keys of its own, and a stored one does not carry over: while a
		// context is on there is no moment without, so the provider cannot change.
		admin.do("PUT", "/api/admin/settings/captcha.provider", map[string]any{"value": "hcaptcha"}).want(t, 400, "captcha_site_key_required")
		admin.putSecurity(t, map[string]any{"captcha": map[string]any{"contexts": map[string]any{"login": map[string]any{"on": false}}}})
		admin.do("PUT", "/api/admin/settings/captcha.provider", map[string]any{"value": "hcaptcha"}).want(t, 200, "")
		admin.do("PUT", "/api/admin/security", map[string]any{"captcha": map[string]any{"siteKey": "k"}}).want(t, 200, "")
		admin.do("PUT", "/api/admin/settings/captcha.contexts", map[string]any{"value": on}).want(t, 400, "captcha_secret_required")
		admin.putSecurity(t, map[string]any{"captcha": map[string]any{"siteKey": "k2", "secret": "other-secret", "contexts": on}})
		// Replacing and clearing the secret.
		admin.do("PUT", "/api/admin/security", map[string]any{"captcha": map[string]any{"secret": ""}}).want(t, 400, "captcha_secret_required") // it is in use
		g := admin.putSecurity(t, map[string]any{"captcha": map[string]any{"secret": "", "contexts": map[string]any{"login": map[string]any{"on": false}}}})
		if g.body["captcha"].(map[string]any)["hasSecret"] != false {
			t.Fatal(g.raw)
		}
		admin.do("PUT", "/api/admin/security", map[string]any{"captcha": map[string]any{"contexts": on}}).want(t, 400, "captcha_secret_required")
		admin.putSecurity(t, map[string]any{"captcha": map[string]any{"provider": "image"}})
		if got := a.auth.CaptchaProvider(context.Background()); got != "" {
			t.Fatalf("CaptchaProvider with the image %q", got)
		}
		// The audit log has the change and never the secret.
		for _, row := range admin.audit(t, "setting_changed") {
			if strings.Contains(strings.ToLower(jsonOf(row)), "s3cret") || strings.Contains(jsonOf(row), "other-secret") {
				t.Fatalf("a secret in the audit log: %v", row)
			}
		}
		if rows := admin.audit(t, "setting_changed"); len(rows) < 3 || !strings.Contains(jsonOf(rows), `"key":"captcha.secret"`) || !strings.Contains(jsonOf(rows), `"to":"changed"`) {
			t.Fatalf("%v", rows)
		}
	})
	// A third-party provider needs --public-url: its host is what answers are checked against.
	eachAccounts(t, accOpts{mod: func(c *auth.Config) { c.PublicURL = "" }}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		admin.do("PUT", "/api/admin/security", map[string]any{"captcha": map[string]any{"provider": "recaptcha", "siteKey": "k", "secret": "s"}}).want(t, 400, "captcha_needs_public_url")
	})
}

func jsonOf(v any) string { b, _ := json.Marshal(v); return string(b) }
