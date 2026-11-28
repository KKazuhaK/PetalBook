package auth

import (
	"context"
	"slices"
	"time"

	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
)

// The sign-in policy of a tenant: signin.mode, signin.passkeyRoles,
// sessions.* and captcha.contexts have a tenant layer, so the code that
// enforces them asks for the value of the tenant of the person concerned (or,
// for a form that is answered before the person is known, of the tenant the
// request is scoped to). Every read is one lookup in the settings' cache; none
// of them asks the database, and none may be made inside a transaction.

// tenantValue is the value of key for the people of tenant, from the settings'
// cache. ok is false for no tenant ("": the request is not scoped to one, the
// site's value is used) and for a value that cannot be read (the caller then
// uses the site's).
func (s *Service) tenantValue(ctx context.Context, tenant, key string) (any, bool) {
	if tenant == "" {
		return nil, false
	}
	set := s.settings.Load()
	if set == nil {
		return nil, false
	}
	e, err := set.Get(ctx, tenant, key)
	if err != nil || e == nil {
		return nil, false
	}
	return e.Value, true
}

// tenantOfPerson is the tenant whose sign-in policy applies to a person: their
// own, except for a site administrator, who always follows the site's (as for
// ssoOnly): "".
func tenantOfPerson(u *store.User) string { return u.PolicyTenant() }

func days(n int64) time.Duration { return time.Duration(n) * 24 * time.Hour }

// lifetimesOf is how long a session of the tenant's people may sit idle and
// last in all: sessions.idleDays and sessions.maxDays as the tenant has them,
// the site's (st) for no tenant. The idle time never exceeds the lifetime.
func (s *Service) lifetimesOf(ctx context.Context, st *secState, tenant string) (idle, max time.Duration) {
	idle, max = st.idle, st.max
	if v, ok := s.tenantValue(ctx, tenant, settings.SessionsIdleDays); ok {
		if n, ok := v.(int64); ok && n > 0 {
			idle = days(n)
		}
	}
	if v, ok := s.tenantValue(ctx, tenant, settings.SessionsMaxDays); ok {
		if n, ok := v.(int64); ok && n > 0 {
			max = days(n)
		}
	}
	return min(idle, max), max
}

// passkeyRoleHeld says whether the tenant's signin.passkeyRoles holds the
// role: the site's (st) for no tenant.
func (s *Service) passkeyRoleHeld(ctx context.Context, st *secState, tenant, role string) bool {
	if v, ok := s.tenantValue(ctx, tenant, settings.SigninPasskeyRoles); ok {
		l, _ := v.([]string)
		return slices.Contains(l, role)
	}
	return st.roles[role]
}

// captchaCtxOf is the setting of one captcha form for a request scoped to the
// tenant: the stricter of the site's own and the tenant's, never less than the
// site's, whatever the site's rule says: the tenant a request names is not tied
// to the account it signs in, so naming one must not weaken the site's form.
// The invitation and the first-run setup are never a tenant's (the invite is
// found only after the captcha, and there is no tenant at setup): the site's.
// A tenant that is unknown (the request names none) gets the site's.
func (s *Service) captchaCtxOf(ctx context.Context, st *secState, tenant, ctxName string) (CaptchaCtx, bool) {
	site, found := st.Captcha.Contexts[ctxName]
	if !slices.Contains(settings.CaptchaTenantContexts, ctxName) {
		return site, found
	}
	v, ok := s.tenantValue(ctx, tenant, settings.CaptchaContexts)
	if !ok {
		return site, found
	}
	ctxs, err := contextsOf(v, st.Captcha.Contexts)
	if err != nil {
		return site, found
	}
	own, ok := ctxs[ctxName]
	if !ok {
		return site, found
	}
	return stricterCaptcha(site, own), true
}

// stricterCaptcha is the stricter of two settings of a form: on beats off,
// asking always beats asking after failures, and fewer failures beat more.
func stricterCaptcha(a, b CaptchaCtx) CaptchaCtx {
	switch {
	case !a.On:
		return b
	case !b.On:
		return a
	case a.When == "always":
		return a
	case b.When == "always":
		return b
	}
	if b.Failures < a.Failures {
		return b
	}
	return a
}

// storedModeOf is the signin.mode of the tenant's people, as stored: the
// site's (st) for no tenant. A tenant's local_only never lifts the site's
// ssoOnly for it (the page then follows the site's mode, which is what is
// enforced).
func (s *Service) storedModeOf(ctx context.Context, st *secState, tenant string) string {
	v, ok := s.tenantValue(ctx, tenant, settings.SigninMode)
	m, _ := v.(string)
	if !ok || m == "" {
		return st.Login.Mode
	}
	if m == ModeLocalOnly && s.ssoOnlyFor(ctx, st, tenant) {
		return st.Login.Mode
	}
	return m
}

// modeOf is the mode of the sign-in page for the tenant's people, as the
// server enforces it: local_only while no provider is enabled, the stored mode
// of the tenant otherwise.
func (s *Service) modeOf(ctx context.Context, st *secState, tenant string) string {
	if !s.provs.anyEnabled() {
		return ModeLocalOnly
	}
	return s.storedModeOf(ctx, st, tenant)
}

// sessionCutoffs lists the tenants whose session lifetimes differ from the
// site's, with their cutoffs at now, for the sweep of expired sessions. Only
// a tenant with a value or a rule of its own for the two keys, or a plan with
// one, can differ, so only those are looked at (a read from the cache each).
func (s *Service) sessionCutoffs(ctx context.Context, st *secState, now time.Time) []store.SessionCutoff {
	owners, err := store.LayeredOwners(ctx, s.st.Q(), settings.SessionsIdleDays, settings.SessionsMaxDays)
	if err != nil {
		s.log.Warn("could not list the tenants with their own session lifetimes", "error", err)
		return nil
	}
	set := s.settings.Load()
	var candidates []string
	for _, o := range owners {
		switch {
		case o[0] != store.LayeredPlan:
			candidates = append(candidates, o[1])
		case set != nil:
			candidates = append(candidates, set.TenantsOfPlan(o[1])...)
		}
	}
	slices.Sort(candidates)
	candidates = slices.Compact(candidates)
	var out []store.SessionCutoff
	for _, id := range candidates {
		idle, max := s.lifetimesOf(ctx, st, id)
		if idle != st.idle || max != st.max {
			out = append(out, store.SessionCutoff{Tenant: id, IdleBefore: now.Add(-idle), CreatedBefore: now.Add(-max)})
		}
	}
	return out
}
