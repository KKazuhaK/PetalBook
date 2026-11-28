package auth

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"petalbook.app/server/internal/secrets"
	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
)

// Login modes of the sign-in page.
const (
	ModeDual        = "dual"
	ModeSSOFirst    = "sso_first"
	ModeSSORedirect = "sso_redirect"
	ModeLocalOnly   = "local_only"
)

// Captcha providers and contexts.
const (
	CaptchaImage     = "image"
	CaptchaTurnstile = "turnstile"
	CaptchaRecaptcha = "recaptcha"
	CaptchaHCaptcha  = "hcaptcha"

	CtxLogin    = "login"
	CtxInvite   = "invite"
	CtxSetup    = "setup"
	CtxForgot   = "forgot"
	CtxRegister = "register"
)

// CaptchaContexts are the contexts that can be switched on.
var CaptchaContexts = []string{CtxLogin, CtxInvite, CtxSetup, CtxForgot, CtxRegister}

// CaptchaCtx is the setting of one context.
type CaptchaCtx struct {
	On       bool   `json:"on"`
	When     string `json:"when"` // always or after_failures
	Failures int    `json:"failures"`
}

// Security is the sign-in policy in force, as the code that enforces it reads
// it: worked out from the layered settings (signin.*, sessions.*, captcha.*,
// features.passkeys) and the captcha's keys. It is never stored.
type Security struct {
	Login struct {
		Mode    string `json:"mode"`
		SSOOnly bool   `json:"ssoOnly"`
	} `json:"login"`
	Passkeys struct {
		Enabled      bool     `json:"enabled"`
		RequireRoles []string `json:"requireRoles"`
	} `json:"passkeys"`
	Captcha struct {
		Provider string `json:"provider"`
		SiteKey  string `json:"siteKey"`
		// HasSecret says the provider's secret is set; SecretUnreadable that this
		// server's key cannot open it. The secret itself is secret.
		HasSecret, SecretUnreadable bool
		secret                      string
		Contexts                    map[string]CaptchaCtx `json:"contexts"`
	} `json:"captcha"`
	Lockout struct {
		Enabled bool   `json:"enabled"`
		Minutes int    `json:"minutes"`
		Scope   string `json:"scope"`
	} `json:"lockout"`
	Limits struct {
		PerMinute       int `json:"perMinute"`
		MaxFailures     int `json:"maxFailures"`
		WindowMinutes   int `json:"windowMinutes"`
		SessionIdleDays int `json:"sessionIdleDays"`
		SessionMaxDays  int `json:"sessionMaxDays"`
	} `json:"limits"`
}

func (c Config) defaultSecurity() Security {
	var sec Security
	sec.Login.Mode = ModeDual
	sec.Passkeys.Enabled = true
	sec.Passkeys.RequireRoles = []string{}
	sec.Captcha.Provider = CaptchaImage
	sec.Captcha.Contexts = map[string]CaptchaCtx{}
	for _, k := range CaptchaContexts {
		sec.Captcha.Contexts[k] = CaptchaCtx{When: "after_failures", Failures: 3}
	}
	sec.Lockout.Minutes, sec.Lockout.Scope = 15, "ip_account"
	sec.Limits.PerMinute, sec.Limits.MaxFailures, sec.Limits.WindowMinutes = 10, 10, 15
	sec.Limits.SessionIdleDays, sec.Limits.SessionMaxDays = int(c.SessionIdle/(24*time.Hour)), int(c.SessionMax/(24*time.Hour))
	return sec
}

// secState is the settings in force, with what is worked out from them.
type secState struct {
	Security
	// serial is the number of the set of settings it was worked out from (see
	// settings.Service.Site); it is current while the service still has that one.
	serial uint64
	window time.Duration
	lock   time.Duration
	idle   time.Duration
	max    time.Duration
	// roles holds the roles a passkey is required of.
	roles map[string]bool
}

func newSecState(sec Security, serial uint64) *secState {
	st := &secState{Security: sec, serial: serial}
	st.window = time.Duration(sec.Limits.WindowMinutes) * time.Minute
	st.lock = time.Duration(sec.Lockout.Minutes) * time.Minute
	st.idle = time.Duration(sec.Limits.SessionIdleDays) * 24 * time.Hour
	st.max = time.Duration(sec.Limits.SessionMaxDays) * 24 * time.Hour
	st.roles = map[string]bool{}
	for _, r := range sec.Passkeys.RequireRoles {
		st.roles[r] = true
	}
	return st
}

// The values of the site's settings, as Go types.
func effBool(m map[string]*settings.Effective, k string) bool {
	b, _ := m[k].Value.(bool)
	return b
}

func effStr(m map[string]*settings.Effective, k string) string {
	s, _ := m[k].Value.(string)
	return s
}

func effInt(m map[string]*settings.Effective, k string) int {
	n, _ := m[k].Value.(int64)
	return int(n)
}

func effStrings(m map[string]*settings.Effective, k string) []string {
	l, _ := m[k].Value.([]string)
	if l == nil {
		return []string{}
	}
	return slices.Clone(l)
}

// captchaRecord is the site record of the captcha's public part. The secret is
// not in it: it is the secret captcha.secret.
const captchaRecord = "captcha"

type captchaRec struct {
	SiteKey string `json:"siteKey"`
}

func (s *Service) readCaptchaRec(ctx context.Context, q store.Q) (captchaRec, error) {
	var r captchaRec
	raw, _, err := store.GetRecord(ctx, q, captchaRecord)
	if errors.Is(err, store.ErrNotFound) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	_ = json.Unmarshal([]byte(raw), &r)
	return r, nil
}

// contextsOf reads the value of captcha.contexts (a generic document, or nil)
// over the default of each context: a missing context or field is the default.
func contextsOf(v any, base map[string]CaptchaCtx) (map[string]CaptchaCtx, error) {
	out := make(map[string]CaptchaCtx, len(CaptchaContexts))
	for _, k := range CaptchaContexts {
		c, ok := base[k]
		if !ok {
			c = CaptchaCtx{When: "after_failures", Failures: 3}
		}
		out[k] = c
	}
	if v == nil {
		return out, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var in map[string]*struct {
		On       *bool   `json:"on"`
		When     *string `json:"when"`
		Failures *int    `json:"failures"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, errf(Invalid, "captcha.contexts is an object of contexts.")
	}
	for name, v := range in {
		c, ok := out[name]
		if !ok {
			return nil, errf(Invalid, "captcha.contexts has login, invite, setup, forgot and register (%q is not one).", name)
		}
		if v == nil {
			continue
		}
		if v.On != nil {
			c.On = *v.On
		}
		if v.When != nil {
			if *v.When != "always" && *v.When != "after_failures" {
				return nil, errf(Invalid, "captcha.contexts.%s.when is always or after_failures.", name)
			}
			c.When = *v.When
		}
		if v.Failures != nil {
			if *v.Failures < 1 || *v.Failures > 100 {
				return nil, errf(Invalid, "captcha.contexts.%s.failures is 1 to 100.", name)
			}
			c.Failures = *v.Failures
		}
		out[name] = c
	}
	return out, nil
}

// contextsDoc is the document stored for captcha.contexts.
func contextsDoc(c map[string]CaptchaCtx) map[string]any {
	out := make(map[string]any, len(c))
	for k, v := range c {
		out[k] = map[string]any{"on": v.On, "when": v.When, "failures": v.Failures}
	}
	return out
}

// sec returns the settings in force. They are worked out from the layered
// settings (the sign-in policy is theirs) once for each set of values: a
// request costs one atomic load and the service's own cached read. A change
// made by the command line reaches a running server within settings.RevCheck.
func (s *Service) sec(ctx context.Context) *secState {
	set := s.settings.Load()
	st := s.secPtr.Load()
	m, serial, err := set.Site(ctx)
	if err != nil {
		s.log.Warn("could not read the security settings", "error", err)
		if st != nil {
			return st
		}
		return newSecState(s.cfg.defaultSecurity(), 0)
	}
	if st != nil && st.serial == serial {
		return st
	}
	s.secMu.Lock()
	defer s.secMu.Unlock()
	if st = s.secPtr.Load(); st != nil && st.serial == serial {
		return st
	}
	nst, err := s.buildSecurity(ctx, m, serial)
	if err != nil {
		s.log.Warn("could not read the captcha settings", "error", err)
		if st != nil {
			return st
		}
		return newSecState(s.cfg.defaultSecurity(), 0)
	}
	old := s.secPtr.Swap(nst)
	s.onSecurityChanged(nst, old)
	return nst
}

func (s *Service) buildSecurity(ctx context.Context, m map[string]*settings.Effective, serial uint64) (*secState, error) {
	var sec Security
	sec.Login.Mode, sec.Login.SSOOnly = effStr(m, settings.SigninMode), effBool(m, settings.SigninSSOOnly)
	sec.Passkeys.Enabled, sec.Passkeys.RequireRoles = effBool(m, settings.FeaturePasskeys), effStrings(m, settings.SigninPasskeyRoles)
	sec.Lockout.Enabled, sec.Lockout.Minutes, sec.Lockout.Scope = effBool(m, settings.SigninLockoutEnabled), effInt(m, settings.SigninLockoutMinutes), effStr(m, settings.SigninLockoutScope)
	l := &sec.Limits
	l.PerMinute, l.MaxFailures, l.WindowMinutes = effInt(m, settings.SigninPerMinute), effInt(m, settings.SigninMaxFailures), effInt(m, settings.SigninWindowMinutes)
	l.SessionIdleDays, l.SessionMaxDays = effInt(m, settings.SessionsIdleDays), effInt(m, settings.SessionsMaxDays)
	sec.Captcha.Provider = effStr(m, settings.CaptchaProvider)
	ctxs, err := contextsOf(m[settings.CaptchaContexts].Value, nil)
	if err != nil {
		return nil, err
	}
	sec.Captcha.Contexts = ctxs
	if sec.Captcha.Provider != CaptchaImage {
		q := s.st.Q()
		rec, err := s.readCaptchaRec(ctx, q)
		if err != nil {
			return nil, err
		}
		sec.Captcha.SiteKey = rec.SiteKey
		secret, ok, err := s.vault.Open(ctx, q, "", secrets.CaptchaSecret)
		switch {
		case err != nil:
			// Set, but not by this server's key: kept as set, unusable.
			sec.Captcha.HasSecret, sec.Captcha.SecretUnreadable = true, true
		case ok:
			sec.Captcha.HasSecret, sec.Captcha.secret = true, secret
		}
	}
	return newSecState(sec, serial), nil
}

// BindSettings makes the layered settings keep what the sign-in policy needs of
// them: the checks that look at more than one value, and the credentials of a
// captcha provider that belong to it. It is called once, by New.
func (s *Service) bindSettings() {
	set := s.settings.Load()
	days := func(d time.Duration) int64 { return max(1, int64(d/(24*time.Hour))) }
	set.SetDefault(settings.SessionsIdleDays, days(s.cfg.SessionIdle))
	set.SetDefault(settings.SessionsMaxDays, max(days(s.cfg.SessionMax), days(s.cfg.SessionIdle)))
	// One lock for the writes of the sign-in policy (and the captcha's keys,
	// UpdateCaptchaKeys): its checks look at each other's values, and two
	// writes at once could each pass a check the other then breaks.
	bind := func(key string, b settings.SiteBinding) {
		b.Lock = &s.keysMu
		set.Bind(key, b)
	}
	bind(settings.SigninMode, settings.SiteBinding{Check: func(ctx context.Context, tenant string, v any) (any, error) {
		if tenant != "" {
			// A tenant's mode only shapes its sign-in page (see storedModeOf).
			return v, nil
		}
		// Leaving local_only while ssoOnly is on needs a way out.
		if st := s.sec(ctx); st.Login.SSOOnly && st.Login.Mode == ModeLocalOnly && v != ModeLocalOnly {
			return v, s.needPasswordAdmin(ctx)
		}
		return v, nil
	}})
	bind(settings.SigninSSOOnly, settings.SiteBinding{Check: func(ctx context.Context, tenant string, v any) (any, error) {
		if tenant != "" {
			// A tenant's own layer, or the site's rule for it (3.6 d4).
			if v != true {
				return v, nil
			}
			return v, s.checkTenantSSOOnly(ctx, tenant)
		}
		if v != true || s.sec(ctx).Login.SSOOnly {
			return v, nil
		}
		// Organisations people make are confirmed by a password sign-up, which
		// ssoOnly refuses. (Registration of the open mode is not asked: an
		// account of the site's own sign-ins is made by them, not by a password.)
		if s.OrgCreation(ctx).Open {
			return nil, coded(400, "sso_only_with_registration", "Organization creation is open; close it before refusing password sign-in.")
		}
		return v, s.needPasswordAdmin(ctx)
	}})
	bind(settings.FeaturePasskeys, settings.SiteBinding{Check: func(ctx context.Context, _ string, v any) (any, error) {
		st := s.sec(ctx)
		return v, s.checkPasskeys(ctx, v == true, st.Passkeys.RequireRoles, st.Passkeys.Enabled)
	}})
	bind(settings.SigninPasskeyRoles, settings.SiteBinding{Check: func(ctx context.Context, tenant string, v any) (any, error) {
		in, _ := v.([]string)
		var roles []string
		for _, r := range in {
			if r != "admin" && r != "member" {
				return nil, errf(Invalid, "signin.passkeyRoles holds admin and member.")
			}
			if !slices.Contains(roles, r) {
				roles = append(roles, r)
			}
		}
		slices.Sort(roles)
		if roles == nil {
			roles = []string{}
		}
		st := s.sec(ctx)
		if tenant != "" {
			// An organisation's requirement needs passkeys that can work; whom it
			// asks is for the organisation (the site's lock-out check counts the
			// site's roles).
			if len(roles) > 0 && !s.passkeysOn(st) {
				return nil, coded(400, "passkey_unavailable", "Passkeys cannot be used here: they need a --public-url that is https, and passkeys switched on.")
			}
			return roles, nil
		}
		return roles, s.checkPasskeys(ctx, st.Passkeys.Enabled, roles, st.Passkeys.Enabled)
	}})
	// The other lifetime, as the layer written has it: the site's, or the
	// tenant's (what its people have now).
	idleDays := func(ctx context.Context, tenant string) int {
		idle, _ := s.lifetimesOf(ctx, s.sec(ctx), tenant)
		return int(idle / (24 * time.Hour))
	}
	maxDays := func(ctx context.Context, tenant string) int {
		_, max := s.lifetimesOf(ctx, s.sec(ctx), tenant)
		return int(max / (24 * time.Hour))
	}
	// A tenant's idle time is never more than its lifetime when read (see
	// lifetimesOf), so only the site's two are held to each other.
	bind(settings.SessionsIdleDays, settings.SiteBinding{Check: func(ctx context.Context, tenant string, v any) (any, error) {
		if n, _ := v.(int64); tenant == "" && int(n) > maxDays(ctx, tenant) {
			return nil, errf(Invalid, "sessions.maxDays is at least sessions.idleDays.")
		}
		return v, nil
	}})
	bind(settings.SessionsMaxDays, settings.SiteBinding{Check: func(ctx context.Context, tenant string, v any) (any, error) {
		if n, _ := v.(int64); tenant == "" && int(n) < idleDays(ctx, tenant) {
			return nil, errf(Invalid, "sessions.maxDays is at least sessions.idleDays.")
		}
		return v, nil
	}})
	bind(settings.CaptchaProvider, settings.SiteBinding{
		Check: func(ctx context.Context, _ string, v any) (any, error) {
			st := s.sec(ctx)
			if v == st.Captcha.Provider || v == CaptchaImage {
				return v, nil
			}
			if s.cfg.PublicURL == "" {
				return nil, errNeedsPublicURL()
			}
			// A secret belongs to the provider it was made for: a new provider has none.
			for _, c := range st.Captcha.Contexts {
				if c.On {
					return nil, errNeedsSiteKey()
				}
			}
			return v, nil
		},
		After: func(ctx context.Context, q store.Q, old, new any) error {
			if old == new {
				return nil
			}
			if err := s.vault.Clear(ctx, q, "", secrets.CaptchaSecret); err != nil {
				return err
			}
			raw, _ := json.Marshal(captchaRec{})
			_, err := store.PutRecord(ctx, q, captchaRecord, string(raw), s.now())
			return err
		},
	})
	bind(settings.CaptchaContexts, settings.SiteBinding{Check: func(ctx context.Context, tenant string, v any) (any, error) {
		st := s.sec(ctx)
		if tenant != "" {
			return s.checkTenantCaptcha(ctx, st, tenant, v)
		}
		next, err := contextsOf(v, st.Captcha.Contexts)
		if err != nil {
			return nil, err
		}
		if st.Captcha.Provider != CaptchaImage {
			if s.cfg.PublicURL == "" {
				return nil, errNeedsPublicURL()
			}
			if anyOn(next) {
				if err := s.needCaptchaKeys(st.Captcha.SiteKey, st.Captcha.HasSecret); err != nil {
					return nil, err
				}
			}
		}
		return contextsDoc(next), nil
	}})
}

// checkTenantCaptcha validates the captcha.contexts an organisation writes: a
// document of login, forgot and register (the invitation and the setup are the
// site's), merged over what the organisation has now and written whole. Asking
// a third-party captcha needs the site's keys, which an organisation does not
// have of its own here.
func (s *Service) checkTenantCaptcha(ctx context.Context, st *secState, tenant string, v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var names map[string]json.RawMessage
	if err := json.Unmarshal(raw, &names); err != nil {
		return nil, errf(Invalid, "captcha.contexts is an object of contexts.")
	}
	for name := range names {
		if !slices.Contains(settings.CaptchaTenantContexts, name) {
			return nil, errf(Invalid, "captcha.contexts of an organization has login, forgot and register (%q is the site's).", name)
		}
	}
	base := st.Captcha.Contexts
	if cur, ok := s.tenantValue(ctx, tenant, settings.CaptchaContexts); ok {
		if m, err := contextsOf(cur, base); err == nil {
			base = m
		}
	}
	next, err := contextsOf(v, base)
	if err != nil {
		return nil, err
	}
	mine := make(map[string]CaptchaCtx, len(settings.CaptchaTenantContexts))
	for _, k := range settings.CaptchaTenantContexts {
		mine[k] = next[k]
	}
	if st.Captcha.Provider != CaptchaImage && anyOn(mine) {
		if s.cfg.PublicURL == "" {
			return nil, errNeedsPublicURL()
		}
		if err := s.needCaptchaKeys(st.Captcha.SiteKey, st.Captcha.HasSecret); err != nil {
			return nil, err
		}
	}
	return contextsDoc(mine), nil
}

func anyOn(c map[string]CaptchaCtx) bool {
	for _, x := range c {
		if x.On {
			return true
		}
	}
	return false
}

func errNeedsPublicURL() error {
	return coded(400, "captcha_needs_public_url", "A third-party captcha is checked against the host of --public-url; start the server with it.")
}

func errNeedsSiteKey() error {
	return coded(400, "captcha_site_key_required", "Give the site key of the captcha provider.")
}

func (s *Service) needCaptchaKeys(siteKey string, hasSecret bool) error {
	if siteKey == "" {
		return errNeedsSiteKey()
	}
	if !hasSecret {
		return coded(400, "captcha_secret_required", "Give the secret of the captcha provider (a secret saved for another provider is not used).")
	}
	return nil
}

// checkPasskeys refuses a passkey setting that would leave someone locked out
// or that the server cannot serve: enabled and roles are the values it would
// have, curEnabled the one it has.
func (s *Service) checkPasskeys(ctx context.Context, enabled bool, roles []string, curEnabled bool) error {
	required, _, err := store.CountPasskeyRequired(ctx, s.st.Q(), roles)
	if err != nil {
		return err
	}
	hostOK := s.passkeyHostOK()
	if len(roles) > 0 && !hostOK || enabled && !curEnabled && required > 0 && !hostOK {
		return coded(400, "passkey_unavailable", "Passkeys need a --public-url that is https (or http on a loopback name).")
	}
	if !enabled && required > 0 {
		return coded(400, "passkey_requirement_unsatisfiable", "A passkey is required of someone: lift the requirements before switching passkeys off.")
	}
	return nil
}

// UpdateCaptchaKeys sets the site key and the secret of the captcha provider
// (each when given; a secret of "" removes it). They are the only part of the
// sign-in policy that is not a setting: the key is a record, the secret a
// secret (captcha.secret), and a value of either is never given back.
func (s *Service) UpdateCaptchaKeys(ctx context.Context, actorID, ip string, siteKey, secret *string) error {
	s.keysMu.Lock()
	defer s.keysMu.Unlock()
	st := s.sec(ctx)
	if siteKey == nil && secret == nil {
		return nil
	}
	if st.Captcha.Provider == CaptchaImage {
		return errf(Invalid, "The built-in image captcha has no keys; choose a provider first.")
	}
	key := st.Captcha.SiteKey
	if siteKey != nil {
		k := strings.TrimSpace(*siteKey)
		if len(k) > 512 || strings.ContainsFunc(k, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return errf(Invalid, "captcha.siteKey is at most 512 characters.")
		}
		key = k
	}
	hasSecret := st.Captcha.HasSecret
	if secret != nil {
		if len(*secret) > 2048 {
			return errf(Invalid, "captcha.secret is at most 2048 bytes.")
		}
		hasSecret = *secret != "" // stored as given, spaces and all
	}
	if anyOn(st.Captcha.Contexts) {
		if err := s.needCaptchaKeys(key, hasSecret); err != nil {
			return err
		}
	}
	err := s.st.Tx(ctx, func(q store.Q) error {
		if siteKey != nil {
			raw, _ := json.Marshal(captchaRec{SiteKey: key})
			if _, err := store.PutRecord(ctx, q, captchaRecord, string(raw), s.now()); err != nil {
				return err
			}
		}
		if secret != nil {
			if *secret == "" {
				return s.vault.Clear(ctx, q, "", secrets.CaptchaSecret)
			}
			return s.vault.Set(ctx, q, "", secrets.CaptchaSecret, *secret, actorID)
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.settings.Load().Invalidate()
	detail := map[string]any{"layer": "site"}
	if siteKey != nil && key != st.Captcha.SiteKey {
		s.AuditIn(ctx, AuditEvent{Kind: "setting_changed", Actor: actorID, IP: ip, Tenant: "-", Detail: map[string]any{"key": "captcha.siteKey", "layer": "site", "from": st.Captcha.SiteKey, "to": key}})
	}
	if secret != nil {
		detail["key"], detail["to"] = "captcha.secret", "changed"
		s.AuditIn(ctx, AuditEvent{Kind: "setting_changed", Actor: actorID, IP: ip, Tenant: "-", Detail: detail})
	}
	return nil
}

// ClearCaptchaSecret removes the captcha's secret, as DELETE
// /api/admin/secrets/captcha.secret asks: refused, as the captcha form refuses
// it, while a third-party captcha is on somewhere, since nobody could pass it.
func (s *Service) ClearCaptchaSecret(ctx context.Context) error {
	s.keysMu.Lock()
	defer s.keysMu.Unlock()
	st := s.sec(ctx)
	if st.Captcha.Provider != CaptchaImage && anyOn(st.Captcha.Contexts) {
		if err := s.needCaptchaKeys(st.Captcha.SiteKey, false); err != nil {
			return err
		}
	}
	err := s.st.Tx(ctx, func(q store.Q) error { return s.vault.Clear(ctx, q, "", secrets.CaptchaSecret) })
	s.settings.Load().Invalidate()
	return err
}

// ResetSecurity is what "petalbook security reset" does: the login mode back
// to dual, ssoOnly, lockout and every captcha context off, no role required to
// use a passkey. Providers (of sign-in and of the captcha), their keys and
// people are untouched. It is the rescue, so it must work whatever the policy
// is and wherever it runs (the command line has no --public-url): the values
// are written in one transaction past the checks, which only refuse a change
// that could lock people out, and these values only let people in. Each
// changed key is in the audit log, by "cli"; settings_rev is moved, which a
// running server notices within settings.RevCheck.
func (s *Service) ResetSecurity(ctx context.Context) error {
	st := s.sec(ctx)
	off := map[string]CaptchaCtx{}
	for k, c := range st.Captcha.Contexts {
		c.On = false
		off[k] = c
	}
	changed, err := s.settings.Load().ForceSite(ctx, "cli", []settings.KeyValue{
		{Key: settings.SigninSSOOnly, Value: false},
		{Key: settings.SigninMode, Value: ModeDual},
		{Key: settings.SigninLockoutEnabled, Value: false},
		{Key: settings.CaptchaContexts, Value: contextsDoc(off)},
		{Key: settings.SigninPasskeyRoles, Value: []string{}},
	})
	if err != nil {
		return err
	}
	for _, c := range changed {
		s.Audit(AuditEvent{Kind: "setting_changed", Tenant: "-", Detail: map[string]any{
			"key": c.Key, "layer": "site", "source": "manual", "from": c.From, "to": c.To, "by": "cli", "reset": "security"}})
	}
	if len(changed) == 0 {
		s.Audit(AuditEvent{Kind: "setting_changed", Tenant: "-", Detail: map[string]any{"by": "cli", "reset": "security"}})
	}
	s.FlushAudit(ctx)
	return nil
}

// SecurityView is what GET /api/admin/security shows: what the sign-in policy
// means now, and the captcha's keys (the secret only as set or not). The policy
// itself is under /api/admin/settings.
type SecurityView struct {
	CaptchaSiteKey    string
	CaptchaHasSecret  bool
	EffectiveMode     string
	SSOAvailable      bool
	SSOOnlyInEffect   bool
	PasskeysAvailable bool
	Required          int
	RequiredWithout   int
	PasswordAdmins    int
	Notices           []Notice
}

// Notice is something the administrators should know.
type Notice struct {
	Code    string
	Message string
	Slug    string
	Field   string
}

// SecurityView describes the settings in force and what they mean now.
func (s *Service) SecurityView(ctx context.Context) (*SecurityView, error) {
	st := s.sec(ctx)
	v := &SecurityView{CaptchaSiteKey: st.Captcha.SiteKey, CaptchaHasSecret: st.Captcha.HasSecret}
	v.EffectiveMode = s.effectiveMode(st)
	v.SSOAvailable = s.provs.anyEnabled()
	v.SSOOnlyInEffect = st.Login.SSOOnly && v.EffectiveMode != ModeLocalOnly
	v.PasskeysAvailable = st.Passkeys.Enabled && s.passkeyHostOK()
	var err error
	if v.Required, v.RequiredWithout, err = store.CountPasskeyRequired(ctx, s.st.Q(), st.Passkeys.RequireRoles); err != nil {
		return nil, err
	}
	if v.PasswordAdmins, err = store.CountEnabledPasswordAdmins(ctx, s.st.Q()); err != nil {
		return nil, err
	}
	v.Notices = s.notices(ctx)
	return v, nil
}

func (s *Service) effectiveMode(st *secState) string {
	if !s.provs.anyEnabled() {
		return ModeLocalOnly
	}
	return st.Login.Mode
}

// needPasswordAdmin refuses a change that would put ssoOnly in effect while no
// enabled administrator has a password.
func (s *Service) needPasswordAdmin(ctx context.Context) error {
	n, err := store.CountEnabledPasswordAdmins(ctx, s.st.Q())
	if err != nil {
		return err
	}
	if n == 0 {
		return coded(400, "local_admin_required", "No enabled administrator has a password: refusing passwords would lock everyone out if the identity provider fails.")
	}
	return nil
}

// ssoOnlyInEffect says whether password and passkey sign-in are refused to
// non-administrators: ssoOnly is on and the sign-in page really offers the
// providers.
func (s *Service) ssoOnlyInEffect(st *secState) bool {
	return st.Login.SSOOnly && s.effectiveMode(st) != ModeLocalOnly
}

// onSecurityChanged runs after the settings in force changed: whatever was
// built from them is built again.
func (s *Service) onSecurityChanged(nst, old *secState) {
	s.captchaRebuild(nst, old)
}
