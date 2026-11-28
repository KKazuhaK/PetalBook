package settings_test

import (
	"encoding/json"
	"testing"

	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// The sign-in policy an organisation decides inside what the site allows:
// signin.mode, signin.passkeyRoles, sessions.* and captcha.contexts have a
// tenant layer. The mode has no order of strictness; the others only tighten
// until the site says allow.
func TestTheSigninKeysHaveATenantLayer(t *testing.T) {
	t.Parallel()
	s := settings.New(nil)
	for k, want := range map[string]struct {
		strict settings.Strictness
		mode   string
	}{
		settings.SigninMode:         {"", settings.Allow},
		settings.SigninPasskeyRoles: {settings.StrictSuperset, settings.Tighten},
		settings.SessionsIdleDays:   {settings.StrictLower, settings.Tighten},
		settings.SessionsMaxDays:    {settings.StrictLower, settings.Tighten},
		settings.CaptchaContexts:    {settings.StrictDoc, settings.Tighten},
	} {
		d, ok := s.Def(k)
		if !ok || !d.Has(settings.Tenant) || d.ServerWide != "" || d.Strict != want.strict || d.Lower.Mode != want.mode {
			t.Errorf("%s: %+v", k, d)
		}
	}
	// What decides before any tenant is known stays the site's.
	for _, k := range []string{settings.SigninLockoutEnabled, settings.SigninLockoutMinutes, settings.SigninLockoutScope,
		settings.SigninPerMinute, settings.SigninMaxFailures, settings.SigninWindowMinutes, settings.CaptchaProvider,
		settings.StorageHosts, settings.StorageRemotesScope} {
		if d, _ := s.Def(k); d == nil || d.Has(settings.Tenant) || d.ServerWide == "" {
			t.Errorf("%s: %+v", k, d)
		}
	}
}

func TestStrictnessOfTheSigninKeys(t *testing.T) {
	t.Parallel()
	roles := &settings.Def{Kind: settings.List, Strict: settings.StrictSuperset}
	doc := func() *settings.Def { d, _ := settings.New(nil).Def(settings.CaptchaContexts); return d }()
	ctxs := func(m map[string]map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range m {
			out[k] = v
		}
		return out
	}
	on := map[string]any{"on": true, "when": "after_failures", "failures": float64(3)}
	always := map[string]any{"on": true, "when": "always", "failures": float64(3)}
	soon := map[string]any{"on": true, "when": "after_failures", "failures": float64(1)}
	late := map[string]any{"on": true, "when": "after_failures", "failures": float64(9)}
	off := map[string]any{"on": false, "when": "after_failures", "failures": float64(3)}
	for name, c := range map[string]struct {
		d        *settings.Def
		v, above any
		looser   bool
	}{
		"more roles":               {roles, []string{"admin", "member"}, []string{"admin"}, false},
		"the same roles":           {roles, []string{"admin"}, []string{"admin"}, false},
		"a role dropped":           {roles, []string{"member"}, []string{"admin"}, true},
		"none under some":          {roles, []string{}, []string{"member"}, true},
		"some under none":          {roles, []string{"admin"}, []string{}, false},
		"a form switched off":      {doc, ctxs(map[string]map[string]any{"login": off}), ctxs(map[string]map[string]any{"login": on}), true},
		"a form left out":          {doc, ctxs(nil), ctxs(map[string]map[string]any{"login": on}), true},
		"a form switched on":       {doc, ctxs(map[string]map[string]any{"login": on}), ctxs(map[string]map[string]any{"login": off}), false},
		"always under after":       {doc, ctxs(map[string]map[string]any{"forgot": always}), ctxs(map[string]map[string]any{"forgot": on}), false},
		"after under always":       {doc, ctxs(map[string]map[string]any{"forgot": on}), ctxs(map[string]map[string]any{"forgot": always}), true},
		"fewer failures":           {doc, ctxs(map[string]map[string]any{"register": soon}), ctxs(map[string]map[string]any{"register": on}), false},
		"more failures":            {doc, ctxs(map[string]map[string]any{"register": late}), ctxs(map[string]map[string]any{"register": on}), true},
		"failures under always":    {doc, ctxs(map[string]map[string]any{"register": soon}), ctxs(map[string]map[string]any{"register": always}), true},
		"another form is not tied": {doc, ctxs(map[string]map[string]any{"forgot": on}), ctxs(map[string]map[string]any{"login": off}), false},
		"a form off above is free": {doc, ctxs(map[string]map[string]any{"login": late}), ctxs(map[string]map[string]any{"login": off}), false},
	} {
		if got := settings.Looser(c.d, c.v, c.above); got != c.looser {
			t.Errorf("%s: %v", name, got)
		}
	}
}

// A tenant's value applies to its own people only; a tenant that sets none has
// the site's. A site's tighten holds the tenant to it; allow lets it loosen.
func TestATenantsSigninValuesAreItsOwn(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setupTwo(t, st)
		short := int64(7)
		on := func(when string, failures float64) map[string]any {
			return map[string]any{"on": true, "when": when, "failures": failures}
		}
		for _, c := range []struct {
			key      string
			site     any // the site's value, set first
			tenant   any // a stricter value of the tenant's
			looser   any // a looser one
			wantSite any
		}{
			{settings.SessionsMaxDays, int64(90), short, int64(120), int64(90)},
			{settings.SessionsIdleDays, int64(30), int64(5), int64(60), int64(30)},
			{settings.SigninPasskeyRoles, []string{"admin"}, []string{"admin", "member"}, []string{}, []string{"admin"}},
			{settings.CaptchaContexts, map[string]any{"login": on("after_failures", 3)}, map[string]any{"login": on("always", 3)},
				map[string]any{"login": map[string]any{"on": false, "when": "after_failures", "failures": float64(3)}}, nil},
		} {
			if c.key == settings.CaptchaContexts {
				// The site's document is whole.
				doc := map[string]any{}
				for _, k := range []string{"login", "invite", "setup", "forgot", "register"} {
					doc[k] = map[string]any{"on": false, "when": "after_failures", "failures": float64(3)}
				}
				doc["login"] = on("after_failures", 3)
				c.site, c.wantSite = doc, doc
			}
			if _, err := s.SetSite(ctx, c.key, settings.Change{HasValue: true, Value: c.site}); err != nil {
				t.Fatalf("%s: %v", c.key, err)
			}
			// By default the layer below may only tighten.
			if e, _ := s.Get(ctx, ten, c.key); e.Above.Mode != settings.Tighten {
				t.Fatalf("%s: %+v", c.key, e.Above)
			}
			if err := s.SetTenant(ctx, ten, c.key, settings.Change{HasValue: true, Value: c.looser}); code(err) != settings.CodeRestricted {
				t.Fatalf("%s: a looser value: %v", c.key, err)
			}
			if err := s.SetTenant(ctx, ten, c.key, settings.Change{HasValue: true, Value: c.tenant}); err != nil {
				t.Fatalf("%s: %v", c.key, err)
			}
			mine, _ := s.Get(ctx, ten, c.key)
			other, _ := s.Get(ctx, ten2, c.key)
			site, _ := s.Get(ctx, "", c.key)
			def, _ := s.Get(ctx, store.DefaultTenantID, c.key)
			if !mine.Tenant.InEffect || mine.Source != settings.Tenant {
				t.Fatalf("%s: the tenant's own value is not in effect: %+v", c.key, mine)
			}
			for who, e := range map[string]*settings.Effective{"another tenant": other, "the site": site, "the default tenant": def} {
				if e.Source == settings.Tenant || !equal(e.Value, c.wantSite) {
					t.Fatalf("%s: the tenant's value reached %s: %+v", c.key, who, e.Value)
				}
			}
			// The site tightens past the tenant's value: the site's holds.
			// (Captcha: the site asks always where the tenant asks after failures.)
			// A site lock holds the tenant to the site's value.
			if _, err := s.SetSite(ctx, c.key, settings.Change{HasLower: true, Lower: lower(settings.Lock)}); err != nil {
				t.Fatalf("%s: %v", c.key, err)
			}
			if e, _ := s.Get(ctx, ten, c.key); !equal(e.Value, c.wantSite) {
				t.Fatalf("%s: a locked key took the tenant's value: %+v", c.key, e.Value)
			}
			if err := s.SetTenant(ctx, ten, c.key, settings.Change{HasValue: true, Value: c.tenant}); code(err) != settings.CodeLocked {
				t.Fatalf("%s: a write under a lock: %v", c.key, err)
			}
			// Allow lets it be looser.
			if _, err := s.SetSite(ctx, c.key, settings.Change{HasLower: true, Lower: lower(settings.Allow)}); err != nil {
				t.Fatal(err)
			}
			if err := s.SetTenant(ctx, ten, c.key, settings.Change{HasValue: true, Value: c.looser}); err != nil {
				t.Fatalf("%s: allow: %v", c.key, err)
			}
			if e, _ := s.Get(ctx, ten, c.key); !equal(e.Value, c.looser) {
				t.Fatalf("%s: %+v", c.key, e.Value)
			}
			if err := s.SetTenant(ctx, ten, c.key, settings.Change{HasValue: true, Value: nil}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.SetSite(ctx, c.key, settings.Change{HasLower: true, Lower: lower(settings.Tighten)}); err != nil {
				t.Fatal(err)
			}
		}
	})
}

// A site's tighten after the tenant chose: a tenant value that is now looser
// is kept and not in effect.
func TestASiteTightenOverridesALooserSigninValue(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		if _, err := s.SetSite(ctx, settings.SessionsMaxDays, settings.Change{HasLower: true, Lower: lower(settings.Allow)}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetTenant(ctx, ten, settings.SessionsMaxDays, settings.Change{HasValue: true, Value: int64(200)}); err != nil {
			t.Fatal(err)
		}
		if e, _ := s.Get(ctx, ten, settings.SessionsMaxDays); e.Value != int64(200) {
			t.Fatalf("%+v", e.Value)
		}
		if _, err := s.SetSite(ctx, settings.SessionsMaxDays, settings.Change{HasLower: true, Lower: lower(settings.Tighten)}); err != nil {
			t.Fatal(err)
		}
		e, _ := s.Get(ctx, ten, settings.SessionsMaxDays)
		if e.Value != int64(90) || e.Tenant.InEffect || e.Tenant.Value != int64(200) {
			t.Fatalf("%+v %+v", e.Value, e.Tenant)
		}
	})
}

// signin.mode has no order, so it cannot be tightened; the site may restrict
// or lock it.
func TestSigninModeCannotBeTightened(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		if _, err := s.SetSite(ctx, settings.SigninMode, settings.Change{HasLower: true, Lower: lower(settings.Tighten)}); code(err) != settings.CodeInvalid {
			t.Fatalf("%v", err)
		}
		if err := s.SetTenant(ctx, ten, settings.SigninMode, settings.Change{HasValue: true, Value: "sso_first"}); err != nil {
			t.Fatal(err)
		}
		if e, _ := s.Get(ctx, ten, settings.SigninMode); e.Value != "sso_first" {
			t.Fatalf("%+v", e.Value)
		}
		if e, _ := s.Get(ctx, ten2, settings.SigninMode); e.Value != "dual" {
			t.Fatalf("%+v", e.Value)
		}
		if _, err := s.SetSite(ctx, settings.SigninMode, settings.Change{HasLower: true, Lower: &settings.Lower{Mode: settings.Restrict, Options: []any{"dual", "local_only"}}}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetTenant(ctx, ten, settings.SigninMode, settings.Change{HasValue: true, Value: "sso_redirect"}); code(err) != settings.CodeRestricted {
			t.Fatalf("%v", err)
		}
	})
}

// equal compares two values as JSON has them.
func equal(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// A tenant's document holds only login, forgot and register: a form the site
// asks that a tenant cannot hold (the invitation) does not make its values
// looser.
func TestCaptchaLooserComparesOnlyAnOrganisationsForms(t *testing.T) {
	t.Parallel()
	d, _ := settings.New(nil).Def(settings.CaptchaContexts)
	on := map[string]any{"on": true, "when": "after_failures", "failures": float64(3)}
	always := map[string]any{"on": true, "when": "always", "failures": float64(3)}
	site := map[string]any{"invite": on, "setup": on, "login": on}
	tenant := map[string]any{"login": always}
	if settings.Looser(d, tenant, site) {
		t.Fatal("a stricter login is looser for want of the invitation")
	}
	if !settings.Looser(d, map[string]any{}, site) {
		t.Fatal("a login left out under a login that is on is looser")
	}
}
