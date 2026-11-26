package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"petalbook.app/server/internal/settings"
)

// The settings that used to have endpoints of their own (machine access,
// tenancy, the sign-in policy, the storage policy, the daily text's default,
// languages and switches) are layered settings now, and those endpoints are
// gone (the removal is tested in the settings fold tests, which use doRaw).
// Tests of what the settings do, and not of where they are written, still say
// "set the security settings to this": do answers those requests by the
// settings API and answers GET with the old shape, made of the settings and
// what the endpoints keep. The shim is the test client's only; the server has
// none of it.

// trySettings reads the site's settings: key -> value, or the refusal.
func (c *client) trySettings() (map[string]any, result) {
	c.a.t.Helper()
	r := c.doRaw("GET", "/api/admin/settings", nil)
	if r.code != http.StatusOK {
		return nil, r
	}
	out := map[string]any{}
	for _, it := range r.body["settings"].([]any) {
		m := it.(map[string]any)
		out[m["key"].(string)] = m["site"].(map[string]any)["value"]
	}
	return out, r
}

// settingsOf is trySettings for a caller that may read them.
func (c *client) settingsOf() map[string]any {
	c.a.t.Helper()
	out, r := c.trySettings()
	if out == nil {
		c.a.t.Fatalf("GET /api/admin/settings: %d %s", r.code, r.raw)
	}
	return out
}

// putKeys writes settings one after the other and stops at the first refusal.
func (c *client) putKeys(steps [][2]any) (result, bool) {
	for _, st := range steps {
		r := c.doRaw("PUT", "/api/admin/settings/"+st[0].(string), map[string]any{"value": st[1]})
		if r.code != http.StatusOK {
			return r, false
		}
	}
	return result{}, true
}

func lgMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func lgResult(m map[string]any, code int) result {
	raw, _ := json.Marshal(m)
	return result{code: code, body: m, raw: string(raw)}
}

// do is doRaw, with the requests to the removed endpoints turned into the
// settings requests and the old answers (see above).
func (c *client) do(method, path string, body any, extra ...string) result {
	c.a.t.Helper()
	if len(extra) == 0 {
		switch path {
		case "/api/admin/security":
			return c.legacySecurity(method, body)
		case "/api/admin/tenancy":
			return c.legacyTenancy(method, body)
		case "/api/admin/access":
			return c.legacyAccess(method, body)
		case "/api/admin/storage":
			if method == "PUT" {
				return c.legacyPutStorage(body)
			}
			if method == "GET" {
				return c.legacyGetStorage()
			}
		case "/api/admin/daily":
			if method == "PUT" {
				return c.legacyPutDaily(body)
			}
			if method == "GET" {
				return c.legacyGetDaily()
			}
		}
	}
	return c.doRaw(method, path, body, extra...)
}

// lgObject reads a body that is a JSON object, or says it is not.
func lgObject(body any) (map[string]any, bool) {
	switch b := body.(type) {
	case map[string]any:
		return b, true
	case string:
		var m map[string]any
		if json.Unmarshal([]byte(b), &m) == nil && m != nil {
			return m, true
		}
	}
	return nil, false
}

var lgBad = result{code: http.StatusBadRequest, body: map[string]any{"error": "invalid"}, raw: `{"error":"invalid"}`}

// --- tenancy ------------------------------------------------------------------

func (c *client) legacyTenancy(method string, body any) result {
	if method == "PUT" {
		req, ok := lgObject(body)
		if !ok {
			return lgBad
		}
		on, given := req["enabled"].(bool)
		if !given {
			return lgBad
		}
		if r, ok := c.putKeys([][2]any{{"features.tenancy", on}}); !ok {
			return r
		}
	}
	s, refused := c.trySettings()
	if s == nil {
		return refused
	}
	t := c.doRaw("GET", "/api/admin/tenants?limit=1", nil)
	if t.code != http.StatusOK {
		return t
	}
	return lgResult(map[string]any{"enabled": s["features.tenancy"], "tenants": t.body["count"]}, 200)
}

// --- machine access -----------------------------------------------------------

func (c *client) legacyAccess(method string, body any) result {
	if method == "PUT" {
		req, ok := lgObject(body)
		if !ok {
			return lgBad
		}
		// The old switch was for everybody: off is forced off (a lock), as the
		// Access panel does; on is the default, which tenants may change.
		for _, k := range []string{"tokens", "mcp"} {
			v, ok := req[k]
			if !ok {
				continue
			}
			body := map[string]any{"value": v, "lower": nil}
			if v == false {
				body["lower"] = map[string]any{"mode": "lock"}
			}
			if r := c.doRaw("PUT", "/api/admin/settings/features."+k, body); r.code != http.StatusOK {
				return r
			}
		}
	}
	s, refused := c.trySettings()
	if s == nil {
		return refused
	}
	// The authorization server is there when its discovery document is.
	oauth := c.doRaw("GET", "/.well-known/oauth-authorization-server", nil).code == http.StatusOK
	return lgResult(map[string]any{"tokens": s["features.tokens"], "mcp": s["features.mcp"], "oauth": oauth}, 200)
}

// --- the sign-in policy -------------------------------------------------------

func (c *client) legacySecurity(method string, body any) result {
	if cur, refused := c.trySettings(); method == "PUT" && cur == nil {
		return refused
	}
	if method == "PUT" {
		req, ok := lgObject(body)
		if !ok {
			return lgBad
		}
		if r, ok := c.legacySecuritySteps(req); !ok {
			return r
		}
	}
	r := c.doRaw("GET", "/api/admin/security", nil)
	if r.code != http.StatusOK {
		return r
	}
	s, refused := c.trySettings()
	if s == nil {
		return refused
	}
	out := r.body
	add := func(block string, kv map[string]any) {
		m := lgMap(out[block])
		if m == nil {
			m = map[string]any{}
			out[block] = m
		}
		for k, v := range kv {
			m[k] = v
		}
	}
	add("login", map[string]any{"mode": s["signin.mode"], "ssoOnly": s["signin.ssoOnly"]})
	add("passkeys", map[string]any{"enabled": s["features.passkeys"], "requireRoles": s["signin.passkeyRoles"]})
	add("captcha", map[string]any{"provider": s["captcha.provider"], "contexts": s["captcha.contexts"]})
	add("lockout", map[string]any{"enabled": s["signin.lockout.enabled"], "minutes": s["signin.lockout.minutes"], "scope": s["signin.lockout.scope"]})
	add("limits", map[string]any{"perMinute": s["signin.perMinute"], "maxFailures": s["signin.maxFailures"], "windowMinutes": s["signin.windowMinutes"],
		"sessionIdleDays": s["sessions.idleDays"], "sessionMaxDays": s["sessions.maxDays"]})
	return lgResult(out, 200)
}

func (c *client) legacySecuritySteps(req map[string]any) (result, bool) {
	for k := range req {
		switch k {
		case "login", "passkeys", "captcha", "lockout", "limits":
		default:
			return lgBad, false
		}
	}
	pick := func(block string, names map[string]string) [][2]any {
		var out [][2]any
		m := lgMap(req[block])
		for _, n := range []string{"mode", "ssoOnly", "enabled", "requireRoles", "minutes", "scope", "perMinute", "maxFailures", "windowMinutes", "sessionMaxDays", "sessionIdleDays"} {
			if v, ok := m[n]; ok && names[n] != "" {
				out = append(out, [2]any{names[n], v})
			}
		}
		return out
	}
	var steps [][2]any
	// One request was one change: a refusal after the provider was set puts it back.
	restore := func(r result) (result, bool) { return r, false }
	if cp := lgMap(req["captcha"]); cp != nil {
		if v, ok := cp["provider"]; ok {
			steps = append(steps, [2]any{"captcha.provider", v})
			cur := c.settingsOf()
			was, ctxs := cur["captcha.provider"], lgMap(cur["captcha.contexts"])
			restore = func(r result) (result, bool) {
				c.doRaw("PUT", "/api/admin/settings/captcha.provider", map[string]any{"value": was})
				c.doRaw("PUT", "/api/admin/settings/captcha.contexts", map[string]any{"value": ctxs})
				return r, false
			}
		}
		// Contexts that are turned off go before the keys, which may then be
		// taken away; contexts turned on go after them.
		ctxs, hasCtx := cp["contexts"]
		ctxFirst := hasCtx && !strings.Contains(lgJSON(ctxs), `"on":true`)
		if ctxFirst {
			steps = append(steps, [2]any{"captcha.contexts", ctxs})
		}
		keys := map[string]any{}
		for _, k := range []string{"siteKey", "secret"} {
			if v, ok := cp[k]; ok {
				keys[k] = v
			}
		}
		if r, ok := c.putKeys(steps); !ok {
			return r, false
		}
		steps = nil
		if len(keys) > 0 {
			if r := c.doRaw("PUT", "/api/admin/security", map[string]any{"captcha": keys}); r.code != http.StatusOK {
				return restore(r)
			}
		}
		if hasCtx && !ctxFirst {
			steps = append(steps, [2]any{"captcha.contexts", ctxs})
		}
	}
	steps = append(steps, pick("login", map[string]string{"ssoOnly": "signin.ssoOnly", "mode": "signin.mode"})...)
	steps = append(steps, pick("passkeys", map[string]string{"enabled": "features.passkeys", "requireRoles": "signin.passkeyRoles"})...)
	steps = append(steps, pick("lockout", map[string]string{"enabled": "signin.lockout.enabled", "minutes": "signin.lockout.minutes", "scope": "signin.lockout.scope"})...)
	steps = append(steps, pick("limits", map[string]string{"perMinute": "signin.perMinute", "maxFailures": "signin.maxFailures", "windowMinutes": "signin.windowMinutes"})...)
	// The two lifetimes bound each other: the one that moves away from the other
	// goes first.
	idle, hasIdle := lgMap(req["limits"])["sessionIdleDays"]
	max, hasMax := lgMap(req["limits"])["sessionMaxDays"]
	cur := c.settingsOf()
	if hasIdle && hasMax && lgNum(max) < lgNum(idle) {
		return lgBad, false
	}
	switch {
	case hasIdle && hasMax:
		if lgNum(idle) > lgNum(cur["sessions.maxDays"]) {
			steps = append(steps, [2]any{"sessions.maxDays", max}, [2]any{"sessions.idleDays", idle})
		} else {
			steps = append(steps, [2]any{"sessions.idleDays", idle}, [2]any{"sessions.maxDays", max})
		}
	case hasIdle:
		steps = append(steps, [2]any{"sessions.idleDays", idle})
	case hasMax:
		steps = append(steps, [2]any{"sessions.maxDays", max})
	}
	if r, ok := c.putKeys(steps); !ok {
		return restore(r)
	}
	return result{}, true
}

// --- storage ------------------------------------------------------------------

func (c *client) legacyPutStorage(body any) result {
	if cur, refused := c.trySettings(); cur == nil {
		return refused
	}
	req, ok := lgObject(body)
	if !ok {
		return lgBad
	}
	names := map[string]map[string]string{
		"remotes":     {"require": "storage.remotes.require", "scope": "storage.remotes.scope", "graceDays": "storage.remotes.graceDays"},
		"hosts":       {"allowed": "storage.hosts"},
		"localSpaces": {"maxBytes": "storage.localSpaces.maxBytes"},
		"cache": {"partial": "cache.partial", "evictAfterDays": "cache.evictAfterDays", "minIdleHours": "cache.minIdleHours",
			"clonesBytes": "cache.clonesBytes", "minFreeBytes": "cache.minFreeBytes", "assetsBytes": "cache.assetsBytes"},
	}
	var steps [][2]any
	for _, sec := range []string{"remotes", "hosts", "localSpaces", "cache"} {
		raw, has := req[sec]
		if !has {
			continue
		}
		m := lgMap(raw)
		if m == nil {
			return lgBad
		}
		for k, v := range m {
			key := names[sec][k]
			if key == "" {
				return lgBad
			}
			if key == "storage.remotes.require" && v == nil {
				v = ""
			}
			steps = append(steps, [2]any{key, v})
		}
	}
	// The scope after the requirement, as the requirement's own check is first.
	for i, st := range steps {
		if st[0] == "storage.remotes.scope" {
			steps = append(append(steps[:i:i], steps[i+1:]...), st)
			break
		}
	}
	if r, ok := c.putKeys(steps); !ok {
		return r
	}
	out := c.legacyGetStorage()
	delete(out.body, "summary") // a PUT answered the settings only
	return lgResult(out.body, out.code)
}

func (c *client) legacyGetStorage() result {
	r := c.doRaw("GET", "/api/admin/storage", nil)
	if r.code != http.StatusOK {
		return r
	}
	set := c.doRaw("GET", "/api/admin/settings", nil)
	if set.code != http.StatusOK {
		// A tenant administrator reads the summary only.
		return r
	}
	s := c.settingsOf()
	var fromFlag bool
	for _, it := range set.body["settings"].([]any) {
		if m := it.(map[string]any); m["key"] == "storage.hosts" {
			fromFlag, _ = lgMap(m["site"])["fromFlag"].(bool)
		}
	}
	out := r.body
	remotes := lgMap(out["remotes"])
	if remotes == nil {
		remotes = map[string]any{}
		out["remotes"] = remotes
	}
	if v, _ := s["storage.remotes.require"].(string); v == "" {
		remotes["require"] = nil
	} else {
		remotes["require"] = v
	}
	remotes["scope"], remotes["graceDays"] = s["storage.remotes.scope"], s["storage.remotes.graceDays"]
	out["hosts"] = map[string]any{"allowed": s["storage.hosts"], "fromFlag": fromFlag}
	out["localSpaces"] = map[string]any{"maxBytes": s["storage.localSpaces.maxBytes"]}
	out["cache"] = map[string]any{"partial": s["cache.partial"], "evictAfterDays": s["cache.evictAfterDays"], "minIdleHours": s["cache.minIdleHours"],
		"clonesBytes": s["cache.clonesBytes"], "minFreeBytes": s["cache.minFreeBytes"], "assetsBytes": s["cache.assetsBytes"]}
	return lgResult(out, 200)
}

// --- the daily text -----------------------------------------------------------

func (c *client) legacyPutDaily(body any) result {
	if cur, refused := c.trySettings(); cur == nil {
		return refused
	}
	req, ok := lgObject(body)
	if !ok {
		return lgBad
	}
	var steps [][2]any
	for k, v := range req {
		switch k {
		case "default":
			steps = append(steps, [2]any{"daily.default", v})
		case "languages":
			steps = append(steps, [2]any{"daily.languages", v})
		case "allow":
			m := lgMap(v)
			if len(m) == 0 {
				return lgBad
			}
			if x, ok := m["own"]; ok {
				steps = append(steps, [2]any{"daily.own", x})
			}
			if x, ok := m["api"]; ok {
				steps = append(steps, [2]any{"daily.api", x})
			}
		default:
			return lgBad
		}
	}
	if r, ok := c.putKeys(steps); !ok {
		return r
	}
	return c.legacyGetDaily()
}

func (c *client) legacyGetDaily() result {
	r := c.doRaw("GET", "/api/admin/daily", nil)
	if r.code != http.StatusOK {
		return r
	}
	s, refused := c.trySettings()
	if s == nil {
		return refused
	}
	out := r.body
	out["default"], out["languages"] = s["daily.default"], s["daily.languages"]
	out["allow"] = map[string]any{"own": s["daily.own"], "api": s["daily.api"]}
	return lgResult(out, 200)
}

// setSetting writes a site setting through the service, before anyone can
// sign in to do it through the API.
func (a *acc) setSetting(t *testing.T, key string, v any) {
	t.Helper()
	if _, err := settings.Of(a.st).SetSite(t.Context(), key, settings.Change{HasValue: true, Value: v}); err != nil {
		t.Fatal(err)
	}
}

// lgNum is a number given as any Go number type or as decoded JSON.
func lgNum(v any) float64 {
	raw, _ := json.Marshal(v)
	var f float64
	_ = json.Unmarshal(raw, &f)
	return f
}

func lgJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}
