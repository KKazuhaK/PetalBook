package api_test

import "testing"

// Turning features.ownCaptcha off while organisations have a provider in force would change what their
// forms use (and, with the site's provider keyless, lock them): it previews them and needs the confirmation.
func TestOwnCaptchaOffNeedsTheConfirmation(t *testing.T) {
	t.Parallel()
	sv := newTCVerify(t)
	eachAccounts(t, tcOpts(sv), func(t *testing.T, a *acc) {
		f := newTLFx(t, a)
		f.own(t, f.admB, f.tenB, "turnstile", tcPicky).want(t, 200, "")
		rule := "/api/admin/tenants/" + f.tenB + "/settings/features.ownCaptcha"
		// The site's switch: the preview names acme and its provider.
		r := f.root.putSetting("features.ownCaptcha", false).want(t, 409, "confirm_required")
		p, _ := r.body["plan"].([]any)
		if len(p) != 1 || p[0].(map[string]any)["tenant"] != f.tenB || p[0].(map[string]any)["captcha"] != "turnstile" {
			t.Fatalf("the preview: %s", r.raw)
		}
		f.root.do("PUT", rule, map[string]any{"siteRule": map[string]any{"value": false}}).want(t, 409, "confirm_required")
		// beta has none: its rule is direct.
		f.root.do("PUT", "/api/admin/tenants/"+f.tenC+"/settings/features.ownCaptcha", map[string]any{"siteRule": map[string]any{"value": false}}).want(t, 200, "")
		// With the confirmation: off for acme, and its provider stops being used; nothing is deleted.
		tok := f.root.do("POST", "/api/admin/confirm", map[string]any{"action": "change_capabilities", "key": "features.ownCaptcha", "tenant": f.tenB}).want(t, 200, "").body["confirm"]
		f.root.do("PUT", rule, map[string]any{"siteRule": map[string]any{"value": false}, "confirm": tok}).want(t, 200, "")
		f.admB.do("GET", tcPath(f.tenB), nil).want(t, 403, "feature_off")
		f.root.do("GET", tcPath(f.tenB), nil).want(t, 200, "")
	})
}

// ownCaptchaOff takes features.ownCaptcha away from a tenant the way the console does: with the confirmation.
func (f *tlFx) ownCaptchaOff(t *testing.T, tenant string) {
	t.Helper()
	path := "/api/admin/tenants/" + tenant + "/settings/features.ownCaptcha"
	body := map[string]any{"siteRule": map[string]any{"value": false}}
	if r := f.root.do("PUT", path, body); r.code == 200 {
		return
	}
	tok := f.root.do("POST", "/api/admin/confirm", map[string]any{"action": "change_capabilities", "key": "features.ownCaptcha", "tenant": tenant}).want(t, 200, "").body["confirm"]
	body["confirm"] = tok
	f.root.do("PUT", path, body).want(t, 200, "")
}
