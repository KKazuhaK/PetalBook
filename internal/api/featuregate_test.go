package api_test

import (
	"errors"
	"strings"
	"testing"
)

// A route that declares a feature is answered 403 feature_off, naming the
// feature, before its handler runs, when the caller's tenant does not have it;
// a tenant's own switch is its own, and a site lock reaches everybody.
// POST /api/account/tokens declares features.tokens; an empty body would be a
// 400 from the handler, so a 403 shows the handler was never reached.
func TestARouteWhoseFeatureIsOffIsRefusedBeforeItsHandler(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		const key = "features.tokens"
		ten := func(id string) string { return "/api/admin/tenants/" + id + "/settings/" + key }
		create := func(c *client) result { return c.do("POST", "/api/account/tokens", map[string]any{}) }

		// On: the request reaches the handler (which finds the body wanting).
		create(f.memB).want(t, 400, "invalid")

		// A tenant turns it off for itself: refused, naming the feature, in a clear sentence; a neighbour is not.
		f.admB.do("PUT", ten(f.tenB), map[string]any{"value": false}).want(t, 200, "")
		r := create(f.memB).want(t, 403, "feature_off")
		msg, _ := r.body["message"].(string)
		if r.body["feature"] != key || !strings.HasPrefix(msg, "The API tokens feature is") || !strings.HasSuffix(msg, ".") {
			t.Fatalf("not a clear answer: %s", r.raw)
		}
		create(f.memA).want(t, 400, "invalid")
		// What the route does not declare is untouched: a token can still be listed and revoked.
		f.memB.do("GET", "/api/account/tokens", nil).want(t, 200, "")
		f.memB.do("DELETE", "/api/account/tokens/"+fakeID, nil).want(t, 404, "not_found")

		// Back on, then locked off by the site: everybody, a site administrator too, and the tenant cannot turn it on.
		f.admB.do("PUT", ten(f.tenB), map[string]any{"value": true}).want(t, 200, "")
		create(f.memB).want(t, 400, "invalid")
		f.root.putSetting(key, false).want(t, 200, "")
		f.root.do("PUT", "/api/admin/settings/"+key, map[string]any{"lower": map[string]any{"mode": "lock"}}).want(t, 200, "")
		for _, c := range []*client{f.memA, f.memB, f.root} {
			create(c).want(t, 403, "feature_off")
		}
		f.admB.do("PUT", ten(f.tenB), map[string]any{"value": true}).want(t, 409, "setting_locked")
	})
}

// GET /api/settings answers what the caller's tenant has of every feature, as
// the web app reads it, with the version of the tenant's settings as its ETag:
// the same version is answered 304, and a change moves it for its tenant only.
func TestTheSettingsAnswerSaysWhichFeaturesTheTenantHasAndIsRevalidated(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		featuresOf := func(r result) map[string]any { return r.body["features"].(map[string]any) }
		get := func(c *client, tag string) result {
			if tag == "" {
				return c.do("GET", "/api/settings", nil)
			}
			return c.do("GET", "/api/settings", nil, "If-None-Match", tag)
		}
		b := get(f.memB, "").want(t, 200, "")
		tagB := b.resp.Header.Get("ETag")
		fb := featuresOf(b)
		if tagB == "" || b.resp.Header.Get("Cache-Control") != "private, no-cache" || fb["features.tokens"] != true || fb["features.offline"] != true || fb["mail.ownSender"] != false {
			t.Fatalf("%s: %s", tagB, b.raw)
		}
		if _, ok := fb["features.registration"]; !ok || fb["storage.assets"] != nil {
			t.Fatalf("every feature and only features: %v", fb)
		}
		// Revalidated: the same version is a 304 with no body.
		if r := get(f.memB, tagB); r.code != 304 || r.raw != "" {
			t.Fatalf("%d %q", r.code, r.raw)
		}
		tagA := get(f.memA, "").want(t, 200, "").resp.Header.Get("ETag")
		if tagA == tagB {
			t.Fatal("a version is its tenant's own")
		}

		// B turns tokens off for itself: B's version moves and says so, A's stays.
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/features.tokens", map[string]any{"value": false}).want(t, 200, "")
		r := get(f.memB, tagB)
		if r.code != 200 || r.resp.Header.Get("ETag") == tagB || featuresOf(r)["features.tokens"] != false {
			t.Fatalf("%d %s", r.code, r.raw)
		}
		if get(f.memA, tagA).code != 304 {
			t.Fatal("a neighbour's change does not move this tenant's version")
		}
	})
}

// When the feature cannot be read the gate refuses (500) and the handler does
// not run: not knowing is not "on".
func TestAFeatureGateThatCannotReadFailsClosed(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		create := func() result { return f.memB.do("POST", "/api/account/tokens", map[string]any{}) }
		create().want(t, 400, "invalid")
		a.h.SetFeatureFault(func(string, string) error { return errors.New("settings read failed") })
		create().want(t, 500, "internal")
		// The same for a key that needs a feature (the admin data view): refused, never granted.
		view := func() result { return f.admA.do("POST", "/api/admin/spaces/"+f.spaceA+"/view", map[string]any{}) }
		view().want(t, 500, "internal")
		a.h.SetFeatureFault(nil)
		create().want(t, 400, "invalid")
		view().want(t, 400, "invalid")
	})
}
