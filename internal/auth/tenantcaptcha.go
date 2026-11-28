package auth

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/KazuhaHub/authcore/captcha"

	"petalbook.app/server/internal/ratelimit"
	"petalbook.app/server/internal/secrets"
	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
)

// An organisation's own captcha provider (docs/api.md, "Captcha: GET/PUT
// /api/admin/tenants/{tenant}/captcha"). Its provider and site key are the
// tenant's integration record of kind captcha; its secret is the tenant's
// secret captcha.secret. Every one is kept in memory, read at startup and again
// for one tenant when it changes, so no request makes a query for it.
//
// The verification request goes to the provider's fixed address (authcore
// knows it; a tenant names no host), so nothing here dials an address a tenant
// chose.
//
// WHICH captcha a form uses is decided in captcha.go (captchaPlan): the site's
// wherever the site asks one, the organisation's own only where the
// organisation alone asks. Whoever controls this secret can make its captcha
// pass for anything, so it is never what stands between a request and a rule of
// the site's.

const (
	maxCaptchaKey    = 255
	maxCaptchaSecret = 1024
)

// tenantCaptchaStored is the record of kind captcha.
type tenantCaptchaStored struct {
	Enabled  bool   `json:"enabled"`
	Provider string `json:"provider"`
	SiteKey  string `json:"siteKey"`
}

func defaultTenantCaptcha() tenantCaptchaStored { return tenantCaptchaStored{Provider: CaptchaImage} }

func parseTenantCaptcha(raw string) (tenantCaptchaStored, error) {
	c := defaultTenantCaptcha()
	if raw == "" {
		return c, nil
	}
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return defaultTenantCaptcha(), err
	}
	if !validCaptchaProvider(c.Provider) {
		return defaultTenantCaptcha(), errors.New("unknown provider")
	}
	return c, nil
}

func validCaptchaProvider(p string) bool {
	return slices.Contains([]string{CaptchaImage, CaptchaTurnstile, CaptchaRecaptcha, CaptchaHCaptcha}, p)
}

// tenantCaptcha is a tenant's provider as the forms use it.
type tenantCaptcha struct {
	tenantCaptchaStored
	// v checks a third party's answers (nil for the image, and when it cannot be
	// built: no secret, an unreadable one, no public URL).
	v *captcha.TokenVerifier
}

// usable says whether the forms may use it: switched on, and either the
// built-in image or a third party that can be checked and shown.
func (t *tenantCaptcha) usable() bool {
	return t != nil && t.Enabled && (t.Provider == CaptchaImage || (t.v != nil && t.SiteKey != ""))
}

// TenantCaptchaView is what GET /api/admin/tenants/{tenant}/captcha shows. Ready
// says the forms use it now (it is on, complete, and the site grants the
// feature).
type TenantCaptchaView struct {
	tenantCaptchaStored
	HasSecret bool
	Ready     bool
}

// newVerifier builds the check of a third party's answers. The page the widget
// is on is the server's, so the host the provider reports is the public one.
func (s *Service) newVerifier(provider, secret string) (*captcha.TokenVerifier, error) {
	opts := []captcha.TokenOption{captcha.WithAllowedHostnames(captcha.HostOf(s.cfg.PublicURL))}
	if s.cfg.HTTPClient != nil {
		opts = append(opts, captcha.WithHTTPClient(s.cfg.HTTPClient))
	}
	if s.cfg.CaptchaEndpoint != "" {
		opts = append(opts, captcha.WithEndpoint(s.cfg.CaptchaEndpoint))
	}
	return captcha.NewTokenVerifier(captcha.Provider(provider), secret, opts...)
}

// loadTenantCaptcha reads every tenant's provider into memory.
func (s *Service) loadTenantCaptcha(ctx context.Context) error {
	rows, err := store.ListIntegrationsOfKind(ctx, s.st.Q(), store.IntegrationCaptcha)
	if err != nil {
		return err
	}
	for _, r := range rows {
		c, err := parseTenantCaptcha(r.Config)
		if err != nil {
			s.log.Warn("a tenant's captcha settings cannot be read; its own provider is off", "tenant", r.TenantID)
			continue
		}
		s.applyTenantCaptcha(ctx, r.TenantID, c)
	}
	return nil
}

func (s *Service) readTenantCaptcha(ctx context.Context, tenant string) (c tenantCaptchaStored, found bool, err error) {
	r, err := store.GetIntegration(ctx, s.st.Q(), tenant, store.IntegrationCaptcha, "")
	if errors.Is(err, store.ErrNotFound) {
		return defaultTenantCaptcha(), false, nil
	}
	if err != nil {
		return c, false, err
	}
	c, err = parseTenantCaptcha(r.Config)
	if err != nil {
		return defaultTenantCaptcha(), true, nil
	}
	return c, true, nil
}

// applyTenantCaptcha makes the stored settings and secret of a tenant the ones
// in force. A secret this server's key cannot open leaves no verifier: the
// tenant's forms use the site's provider.
func (s *Service) applyTenantCaptcha(ctx context.Context, tenant string, c tenantCaptchaStored) {
	t := &tenantCaptcha{tenantCaptchaStored: c}
	if c.Enabled && c.Provider != CaptchaImage && s.cfg.PublicURL != "" {
		secret, ok, err := s.vault.Open(ctx, s.st.Q(), tenant, secrets.CaptchaSecret)
		switch {
		case err != nil:
			s.log.Warn("a tenant's stored captcha secret cannot be read; its provider is not used", "tenant", tenant)
		case ok:
			if v, err := s.newVerifier(c.Provider, secret); err != nil {
				s.log.Warn("could not set up a tenant's captcha check", "tenant", tenant, "reason", err.Error())
			} else {
				t.v = v
			}
		}
	}
	s.tenantCaptchaSet(tenant, t)
}

func (s *Service) tenantCaptchaSet(tenant string, t *tenantCaptcha) {
	s.tenantCaptchaCache.Lock()
	defer s.tenantCaptchaCache.Unlock()
	if t == nil {
		delete(s.tenantCaptchaCache.m, tenant)
		return
	}
	if s.tenantCaptchaCache.m == nil {
		s.tenantCaptchaCache.m = map[string]*tenantCaptcha{}
	}
	s.tenantCaptchaCache.m[tenant] = t
}

// tenantCaptchaFor is the provider the forms of the tenant may use: its own, in
// force, and granted (features.ownCaptcha); nil otherwise. From memory.
func (s *Service) tenantCaptchaFor(ctx context.Context, tenant string) *tenantCaptcha {
	if tenant == "" {
		return nil
	}
	if set := s.settings.Load(); set != nil && !set.On(ctx, tenant, settings.FeatureOwnCaptcha) {
		return nil
	}
	s.tenantCaptchaCache.RLock()
	t := s.tenantCaptchaCache.m[tenant]
	s.tenantCaptchaCache.RUnlock()
	if !t.usable() {
		return nil
	}
	return t
}

// ReloadTenantCaptcha makes a tenant's stored settings and secret the ones in
// force: after its secret was changed as a secret.
func (s *Service) ReloadTenantCaptcha(ctx context.Context, tenant string) error {
	s.tenantCaptchaMu.Lock()
	defer s.tenantCaptchaMu.Unlock()
	c, found, err := s.readTenantCaptcha(ctx, tenant)
	if err != nil {
		return err
	}
	if !found {
		s.tenantCaptchaSet(tenant, nil)
		return nil
	}
	s.applyTenantCaptcha(ctx, tenant, c)
	return nil
}

// ForgetTenantCaptcha drops a deleted tenant's provider from memory.
func (s *Service) ForgetTenantCaptcha(tenant string) { s.tenantCaptchaSet(tenant, nil) }

// GetTenantCaptcha is a tenant's own provider as an administrator sees it.
func (s *Service) GetTenantCaptcha(ctx context.Context, tenant string) (*TenantCaptchaView, error) {
	c, _, err := s.readTenantCaptcha(ctx, tenant)
	if err != nil {
		return nil, err
	}
	has, err := s.vault.Has(ctx, s.st.Q(), tenant, secrets.CaptchaSecret)
	if err != nil {
		return nil, err
	}
	return &TenantCaptchaView{tenantCaptchaStored: c, HasSecret: has, Ready: s.tenantCaptchaFor(ctx, tenant) != nil}, nil
}

// TenantCaptchaPatch is a PUT of a tenant's provider; nil fields are left alone.
type TenantCaptchaPatch struct {
	Enabled  *bool
	Provider *string
	SiteKey  *string
	Secret   *string
}

// tenantAsksCaptcha says whether the tenant's own captcha.contexts ask a
// captcha of any of its forms.
func (s *Service) tenantAsksCaptcha(ctx context.Context, st *secState, tenant string) bool {
	v, ok := s.tenantValue(ctx, tenant, settings.CaptchaContexts)
	if !ok {
		return false
	}
	ctxs, err := contextsOf(v, st.Captcha.Contexts)
	if err != nil {
		return false
	}
	for _, name := range settings.CaptchaTenantContexts {
		if ctxs[name].On {
			return true
		}
	}
	return false
}

// siteProviderCannotCheck says whether the site's own provider could not take
// over a tenant's forms: a third party that has no keys.
func siteProviderCannotCheck(st *secState) bool {
	return st.Captcha.Provider != CaptchaImage && (st.Captcha.SiteKey == "" || !st.Captcha.HasSecret)
}

// errTenantCaptchaNeeded is the refusal of taking away a provider that the
// tenant's forms depend on.
func errTenantCaptchaNeeded() error {
	return coded(400, "captcha_needed", "The organisation's forms ask a captcha and the site's provider has no keys to take over; set the keys first or switch those forms off.")
}

// keepsFormsServed refuses a change that takes away the provider serving a
// tenant's forms, when it asks a captcha of them and the site's provider could
// not take over.
func (s *Service) keepsFormsServed(ctx context.Context, tenant string, usable bool) error {
	if usable || s.tenantCaptchaFor(ctx, tenant) == nil {
		// Nothing is taken away: its own provider is not what serves the forms now.
		return nil
	}
	st := s.sec(ctx)
	if siteProviderCannotCheck(st) && s.tenantAsksCaptcha(ctx, st, tenant) {
		return errTenantCaptchaNeeded()
	}
	return nil
}

// UpdateTenantCaptcha applies a patch to a tenant's provider. Everything is
// checked before anything is stored.
func (s *Service) UpdateTenantCaptcha(ctx context.Context, actorID, ip, tenant string, p TenantCaptchaPatch) (*TenantCaptchaView, error) {
	s.tenantCaptchaMu.Lock()
	defer s.tenantCaptchaMu.Unlock()
	cur, found, err := s.readTenantCaptcha(ctx, tenant)
	if err != nil {
		return nil, err
	}
	hadSecret, err := s.vault.Has(ctx, s.st.Q(), tenant, secrets.CaptchaSecret)
	if err != nil {
		return nil, err
	}
	next, hasSecret := cur, hadSecret
	if p.Provider != nil {
		if !validCaptchaProvider(*p.Provider) {
			return nil, errf(Invalid, "Provider is image, turnstile, recaptcha or hcaptcha.")
		}
		if *p.Provider != cur.Provider {
			// The keys belong to the provider they were made for.
			next.Provider, next.SiteKey, hasSecret = *p.Provider, "", false
		}
	}
	if p.SiteKey != nil {
		k := strings.TrimSpace(*p.SiteKey)
		if utf8.RuneCountInString(k) > maxCaptchaKey || !utf8.ValidString(k) || strings.ContainsFunc(k, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return nil, errf(Invalid, "SiteKey is at most %d characters.", maxCaptchaKey)
		}
		next.SiteKey = k
	}
	secretGiven := p.Secret != nil
	if secretGiven {
		if len(*p.Secret) > maxCaptchaSecret {
			return nil, errf(Invalid, "Secret is at most %d bytes.", maxCaptchaSecret)
		}
		hasSecret = *p.Secret != ""
	}
	if p.Enabled != nil {
		next.Enabled = *p.Enabled
	}
	if next.Provider == CaptchaImage && (next.SiteKey != "" || (secretGiven && *p.Secret != "")) {
		return nil, errf(Invalid, "The built-in image has no keys.")
	}
	if next.Enabled && next.Provider != CaptchaImage {
		if s.cfg.PublicURL == "" {
			return nil, errNeedsPublicURL()
		}
		if err := s.needCaptchaKeys(next.SiteKey, hasSecret); err != nil {
			return nil, err
		}
	}
	if err := s.keepsFormsServed(ctx, tenant, next.Enabled); err != nil {
		return nil, err
	}
	// The secret that goes with a provider that was changed is cleared even when no
	// secret was given.
	clearSecret := hadSecret && !hasSecret
	changed := map[string]any{}
	diff := func(k string, a, b any) {
		if a != b {
			changed[k] = b
		}
	}
	diff("enabled", cur.Enabled, next.Enabled)
	diff("provider", cur.Provider, next.Provider)
	diff("siteKey", cur.SiteKey, next.SiteKey)
	if secretGiven && hasSecret || clearSecret {
		if hasSecret {
			changed["secret"] = "changed" // never the value
		} else {
			changed["secret"] = "cleared"
		}
	}
	if len(changed) > 0 || !found {
		raw, err := json.Marshal(next)
		if err != nil {
			return nil, err
		}
		err = s.st.Tx(ctx, func(q store.Q) error {
			if err := store.PutIntegration(ctx, q, store.IntegrationRow{TenantID: tenant, Kind: store.IntegrationCaptcha, Config: string(raw), UpdatedBy: actorID, Updated: s.now()}); err != nil {
				return err
			}
			switch {
			case secretGiven && *p.Secret != "":
				return s.vault.Set(ctx, q, tenant, secrets.CaptchaSecret, *p.Secret, actorID)
			case clearSecret:
				return s.vault.Clear(ctx, q, tenant, secrets.CaptchaSecret)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		s.applyTenantCaptcha(ctx, tenant, next)
		s.AuditIn(ctx, AuditEvent{Kind: "tenant_captcha_changed", Actor: actorID, IP: ip, Tenant: tenant, Detail: changed})
	}
	return s.GetTenantCaptcha(ctx, tenant)
}

// DeleteTenantCaptcha removes a tenant's provider and its secret.
func (s *Service) DeleteTenantCaptcha(ctx context.Context, actorID, ip, tenant string) error {
	s.tenantCaptchaMu.Lock()
	defer s.tenantCaptchaMu.Unlock()
	_, found, err := s.readTenantCaptcha(ctx, tenant)
	if err != nil {
		return err
	}
	hadSecret, err := s.vault.Has(ctx, s.st.Q(), tenant, secrets.CaptchaSecret)
	if err != nil {
		return err
	}
	if !found && !hadSecret {
		return nil
	}
	if err := s.keepsFormsServed(ctx, tenant, false); err != nil {
		return err
	}
	err = s.st.Tx(ctx, func(q store.Q) error {
		if err := store.DeleteIntegration(ctx, q, tenant, store.IntegrationCaptcha, ""); err != nil {
			return err
		}
		return s.vault.Clear(ctx, q, tenant, secrets.CaptchaSecret)
	})
	if err != nil {
		return err
	}
	s.tenantCaptchaSet(tenant, nil)
	s.AuditIn(ctx, AuditEvent{Kind: "tenant_captcha_changed", Actor: actorID, IP: ip, Tenant: tenant, Detail: map[string]any{"removed": true}})
	return nil
}

// ClearTenantCaptchaSecret removes a tenant's secret, as DELETE
// /api/admin/tenants/{tenant}/secrets/captcha.secret asks: refused while the
// tenant's forms depend on its provider and the site's cannot take over.
func (s *Service) ClearTenantCaptchaSecret(ctx context.Context, tenant string) error {
	s.tenantCaptchaMu.Lock()
	defer s.tenantCaptchaMu.Unlock()
	if err := s.keepsFormsServed(ctx, tenant, false); err != nil {
		return err
	}
	if err := s.st.Tx(ctx, func(q store.Q) error { return s.vault.Clear(ctx, q, tenant, secrets.CaptchaSecret) }); err != nil {
		return err
	}
	c, found, err := s.readTenantCaptcha(ctx, tenant)
	if err != nil || !found {
		return err
	}
	s.applyTenantCaptcha(ctx, tenant, c)
	return nil
}

// TenantCaptchaTestResult is what trying a tenant's provider found.
type TenantCaptchaTestResult struct {
	OK      bool
	Message string
}

// TestTenantCaptcha checks a token the administrator got from the provider's
// widget against the stored provider (on or off), with the stored secret. It is
// held to the site's test limit, 10 an hour, for each tenant.
func (s *Service) TestTenantCaptcha(ctx context.Context, actorID, ip, tenant, token string) (*TenantCaptchaTestResult, error) {
	if token == "" || len(token) > 4096 {
		return nil, errf(Invalid, "Token is the answer the provider's widget gave.")
	}
	c, found, err := s.readTenantCaptcha(ctx, tenant)
	if err != nil {
		return nil, err
	}
	if found && c.Provider == CaptchaImage {
		return nil, errf(Invalid, "The built-in image needs no test.")
	}
	secret, hasSecret, err := s.vault.Open(ctx, s.st.Q(), tenant, secrets.CaptchaSecret)
	if err != nil {
		return &TenantCaptchaTestResult{false, "the stored secret cannot be opened with this server's key"}, nil
	}
	if !found || c.SiteKey == "" || !hasSecret {
		return nil, coded(409, "captcha_not_configured", "Set the provider's site key and secret first.")
	}
	if s.cfg.PublicURL == "" {
		return nil, errNeedsPublicURL()
	}
	if wait, ok := s.testCaps.TakeAll(ratelimit.Cost{Key: "captchatest:" + tenant, Limit: 10}); !ok {
		return nil, retryAfter(wait)
	}
	v, err := s.newVerifier(c.Provider, secret)
	if err != nil {
		return &TenantCaptchaTestResult{false, "the check could not be set up"}, nil
	}
	res := &TenantCaptchaTestResult{}
	r, err := v.Verify(ctx, token, ip)
	switch {
	case err != nil:
		res.Message = "the provider could not be asked (" + captchaReason(err) + ")"
	case r.HostnameMismatch:
		res.Message = "the token was solved on another host than the server's public address"
	case !r.Success:
		res.Message = "the provider did not accept the token or the secret"
	default:
		res.OK = true
	}
	s.AuditIn(ctx, AuditEvent{Kind: "tenant_captcha_test", Actor: actorID, IP: ip, Tenant: tenant, Detail: map[string]any{"provider": c.Provider, "ok": res.OK}})
	return res, nil
}
