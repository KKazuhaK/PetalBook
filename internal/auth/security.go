package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
)

const securityKey = "security"

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

// Security is the settings of the 安全 page.
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
		// SecretEnc is the sealed secret of the third-party provider.
		SecretEnc string                `json:"secretEnc,omitempty"`
		Contexts  map[string]CaptchaCtx `json:"contexts"`
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

func (s Security) clone() Security {
	s.Passkeys.RequireRoles = slices.Clone(s.Passkeys.RequireRoles)
	m := make(map[string]CaptchaCtx, len(s.Captcha.Contexts))
	for k, v := range s.Captcha.Contexts {
		m[k] = v
	}
	s.Captcha.Contexts = m
	return s
}

// fill makes a stored value whole: a setting added later takes its default.
func (s *Security) fill(def Security) {
	if s.Login.Mode == "" {
		s.Login.Mode = def.Login.Mode
	}
	if s.Passkeys.RequireRoles == nil {
		s.Passkeys.RequireRoles = []string{}
	}
	if s.Captcha.Provider == "" {
		s.Captcha.Provider = def.Captcha.Provider
	}
	if s.Captcha.Contexts == nil {
		s.Captcha.Contexts = map[string]CaptchaCtx{}
	}
	for _, k := range CaptchaContexts {
		c, ok := s.Captcha.Contexts[k]
		if !ok || c.When == "" || c.Failures < 1 {
			d := def.Captcha.Contexts[k]
			c.When, c.Failures = d.When, d.Failures
			s.Captcha.Contexts[k] = c
		}
	}
	if s.Lockout.Minutes < 1 {
		s.Lockout.Minutes = def.Lockout.Minutes
	}
	if s.Lockout.Scope == "" {
		s.Lockout.Scope = def.Lockout.Scope
	}
	l, d := &s.Limits, def.Limits
	if l.PerMinute < 1 {
		l.PerMinute = d.PerMinute
	}
	if l.MaxFailures < 1 {
		l.MaxFailures = d.MaxFailures
	}
	if l.WindowMinutes < 1 {
		l.WindowMinutes = d.WindowMinutes
	}
	if l.SessionIdleDays < 1 {
		l.SessionIdleDays = d.SessionIdleDays
	}
	if l.SessionMaxDays < 1 {
		l.SessionMaxDays = d.SessionMaxDays
	}
}

// secState is the settings in force, with what is worked out from them.
type secState struct {
	Security
	rev    int64
	loaded time.Time
	window time.Duration
	lock   time.Duration
	idle   time.Duration
	max    time.Duration
	// roles holds the roles a passkey is required of.
	roles map[string]bool
}

func newSecState(sec Security, rev int64, now time.Time) *secState {
	st := &secState{Security: sec, rev: rev, loaded: now}
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

// sec returns the settings in force. They are read from the state database
// once, and again only when the revision of the stored row changed, which is
// asked about at most once every CacheTTL: a change made by the command-line
// tool reaches a running server within that.
func (s *Service) sec(ctx context.Context) *secState {
	st := s.secPtr.Load()
	if st != nil && s.now().Sub(st.loaded) < s.cfg.CacheTTL {
		return st
	}
	s.secMu.Lock()
	defer s.secMu.Unlock()
	st = s.secPtr.Load()
	if st != nil && s.now().Sub(st.loaded) < s.cfg.CacheTTL {
		return st
	}
	rev, err := store.SettingRevision(ctx, s.st.Q(), securityKey)
	if err != nil {
		s.log.Warn("could not read the security settings revision", "error", err)
		if st != nil {
			return st
		}
	} else if st != nil && st.rev == rev {
		fresh := *st
		fresh.loaded = s.now()
		s.secPtr.Store(&fresh)
		return &fresh
	}
	nst, err := s.loadSecurity(ctx)
	if err != nil {
		s.log.Warn("could not read the security settings", "error", err)
		if st != nil {
			return st
		}
		def := s.cfg.defaultSecurity()
		return newSecState(def, 0, s.now())
	}
	s.secPtr.Store(nst)
	s.onSecurityChanged(nst, st)
	return nst
}

func (s *Service) loadSecurity(ctx context.Context) (*secState, error) {
	def := s.cfg.defaultSecurity()
	sec := def.clone()
	raw, rev, err := store.GetSetting(ctx, s.st.Q(), securityKey)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal([]byte(raw), &sec); err != nil {
			return nil, fmt.Errorf("the stored security settings are not readable: %w", err)
		}
	}
	sec.fill(def)
	return newSecState(sec, rev, s.now()), nil
}

// saveSecurity stores the settings and makes them the ones in force.
func (s *Service) saveSecurity(ctx context.Context, sec Security) (*secState, error) {
	raw, err := json.Marshal(sec)
	if err != nil {
		return nil, err
	}
	rev, err := store.PutSetting(ctx, s.st.Q(), securityKey, string(raw), s.now())
	if err != nil {
		return nil, err
	}
	nst := newSecState(sec, rev, s.now())
	old := s.secPtr.Swap(nst)
	s.onSecurityChanged(nst, old)
	return nst, nil
}

// SecurityPatch is a PUT of the settings: any block, any field in it.
type SecurityPatch struct {
	Login *struct {
		Mode    *string `json:"mode"`
		SSOOnly *bool   `json:"ssoOnly"`
	} `json:"login"`
	Passkeys *struct {
		Enabled      *bool     `json:"enabled"`
		RequireRoles *[]string `json:"requireRoles"`
	} `json:"passkeys"`
	Captcha *struct {
		Provider *string `json:"provider"`
		SiteKey  *string `json:"siteKey"`
		Secret   *string `json:"secret"`
		Contexts map[string]*struct {
			On       *bool   `json:"on"`
			When     *string `json:"when"`
			Failures *int    `json:"failures"`
		} `json:"contexts"`
	} `json:"captcha"`
	Lockout *struct {
		Enabled *bool   `json:"enabled"`
		Minutes *int    `json:"minutes"`
		Scope   *string `json:"scope"`
	} `json:"lockout"`
	Limits *struct {
		PerMinute       *int `json:"perMinute"`
		MaxFailures     *int `json:"maxFailures"`
		WindowMinutes   *int `json:"windowMinutes"`
		SessionIdleDays *int `json:"sessionIdleDays"`
		SessionMaxDays  *int `json:"sessionMaxDays"`
	} `json:"limits"`
}

func inRange(name string, v *int, lo, hi int) error {
	if v != nil && (*v < lo || *v > hi) {
		return errf(Invalid, "%s is %d to %d", name, lo, hi)
	}
	return nil
}

// UpdateSecurity applies a patch. Everything is checked before anything is
// stored, and a value out of range is refused, never clamped.
func (s *Service) UpdateSecurity(ctx context.Context, actorID, ip string, p SecurityPatch) (*secState, error) {
	s.secMu.Lock()
	defer s.secMu.Unlock()
	cur, err := s.loadSecurity(ctx)
	if err != nil {
		return nil, err
	}
	next := cur.Security.clone()
	var secretGiven bool
	var newSecret string

	if l := p.Login; l != nil {
		if l.Mode != nil {
			if !slices.Contains([]string{ModeDual, ModeSSOFirst, ModeSSORedirect, ModeLocalOnly}, *l.Mode) {
				return nil, errf(Invalid, "login.mode is dual, sso_first, sso_redirect or local_only")
			}
			next.Login.Mode = *l.Mode
		}
		if l.SSOOnly != nil {
			next.Login.SSOOnly = *l.SSOOnly
		}
	}
	if k := p.Passkeys; k != nil {
		if k.Enabled != nil {
			next.Passkeys.Enabled = *k.Enabled
		}
		if k.RequireRoles != nil {
			var roles []string
			for _, r := range *k.RequireRoles {
				if r != "admin" && r != "member" {
					return nil, errf(Invalid, "passkeys.requireRoles holds admin and member")
				}
				if !slices.Contains(roles, r) {
					roles = append(roles, r)
				}
			}
			slices.SortFunc(roles, func(a, b string) int { return strings.Compare(a, b) })
			if roles == nil {
				roles = []string{}
			}
			next.Passkeys.RequireRoles = roles
		}
	}
	if c := p.Captcha; c != nil {
		if c.Provider != nil {
			if !slices.Contains([]string{CaptchaImage, CaptchaTurnstile, CaptchaRecaptcha, CaptchaHCaptcha}, *c.Provider) {
				return nil, errf(Invalid, "captcha.provider is image, turnstile, recaptcha or hcaptcha")
			}
			if *c.Provider != next.Captcha.Provider {
				// A secret belongs to the provider it was made for.
				next.Captcha.Provider, next.Captcha.SecretEnc = *c.Provider, ""
				next.Captcha.SiteKey = ""
			}
		}
		if c.SiteKey != nil {
			if len(*c.SiteKey) > 512 || strings.ContainsFunc(*c.SiteKey, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
				return nil, errf(Invalid, "captcha.siteKey is at most 512 characters")
			}
			next.Captcha.SiteKey = strings.TrimSpace(*c.SiteKey)
		}
		if c.Secret != nil {
			if len(*c.Secret) > 2048 {
				return nil, errf(Invalid, "captcha.secret is at most 2048 bytes")
			}
			secretGiven, newSecret = true, strings.TrimSpace(*c.Secret)
		}
		for name, v := range c.Contexts {
			cur, ok := next.Captcha.Contexts[name]
			if !ok {
				return nil, errf(Invalid, "captcha.contexts has login, invite, setup, forgot and register (%q is not one)", name)
			}
			if v == nil {
				continue
			}
			if v.On != nil {
				cur.On = *v.On
			}
			if v.When != nil {
				if *v.When != "always" && *v.When != "after_failures" {
					return nil, errf(Invalid, "captcha.contexts.%s.when is always or after_failures", name)
				}
				cur.When = *v.When
			}
			if v.Failures != nil {
				if err := inRange("captcha.contexts."+name+".failures", v.Failures, 1, 100); err != nil {
					return nil, err
				}
				cur.Failures = *v.Failures
			}
			next.Captcha.Contexts[name] = cur
		}
	}
	if l := p.Lockout; l != nil {
		if l.Enabled != nil {
			next.Lockout.Enabled = *l.Enabled
		}
		if err := inRange("lockout.minutes", l.Minutes, 1, 10080); err != nil {
			return nil, err
		}
		if l.Minutes != nil {
			next.Lockout.Minutes = *l.Minutes
		}
		if l.Scope != nil {
			if !slices.Contains([]string{"ip_account", "ip", "account"}, *l.Scope) {
				return nil, errf(Invalid, "lockout.scope is ip_account, ip or account")
			}
			next.Lockout.Scope = *l.Scope
		}
	}
	if l := p.Limits; l != nil {
		for _, c := range []struct {
			name   string
			v      *int
			lo, hi int
			dst    *int
		}{
			{"limits.perMinute", l.PerMinute, 1, 100000, &next.Limits.PerMinute},
			{"limits.maxFailures", l.MaxFailures, 1, 100000, &next.Limits.MaxFailures},
			{"limits.windowMinutes", l.WindowMinutes, 1, 10080, &next.Limits.WindowMinutes},
			{"limits.sessionIdleDays", l.SessionIdleDays, 1, 365, &next.Limits.SessionIdleDays},
			{"limits.sessionMaxDays", l.SessionMaxDays, 1, 3650, &next.Limits.SessionMaxDays},
		} {
			if err := inRange(c.name, c.v, c.lo, c.hi); err != nil {
				return nil, err
			}
			if c.v != nil {
				*c.dst = *c.v
			}
		}
		if next.Limits.SessionMaxDays < next.Limits.SessionIdleDays {
			return nil, errf(Invalid, "limits.sessionMaxDays is at least sessionIdleDays")
		}
	}

	// Rules that need more than one field.
	third := next.Captcha.Provider != CaptchaImage
	if third {
		if s.cfg.PublicURL == "" {
			return nil, coded(400, "captcha_needs_public_url", "a third-party captcha is checked against the host of --public-url; start the server with it")
		}
		anyOn := false
		for _, c := range next.Captcha.Contexts {
			anyOn = anyOn || c.On
		}
		if anyOn {
			if next.Captcha.SiteKey == "" {
				return nil, coded(400, "captcha_site_key_required", "give the site key of the captcha provider")
			}
			hasSecret := next.Captcha.SecretEnc != "" && !(secretGiven && newSecret == "")
			if secretGiven && newSecret != "" {
				hasSecret = true
			}
			if !hasSecret {
				return nil, coded(400, "captcha_secret_required", "give the secret of the captcha provider (a secret saved for another provider is not used)")
			}
		}
	}
	// Open registration needs people who can sign in with a password.
	if next.Login.SSOOnly && !cur.Login.SSOOnly {
		if open, err := s.RegistrationOpenAnywhere(ctx); err != nil {
			return nil, err
		} else if open {
			return nil, coded(400, "sso_only_with_registration", "registration is open for a tenant; close it before refusing password sign-in")
		}
	}
	// Turning ssoOnly on, or leaving local_only while it is on, needs a way out.
	if next.Login.SSOOnly && (!cur.Login.SSOOnly || cur.Login.Mode == ModeLocalOnly && next.Login.Mode != ModeLocalOnly) {
		if err := s.needPasswordAdmin(ctx); err != nil {
			return nil, err
		}
	}
	required, _, err := store.CountPasskeyRequired(ctx, s.st.Q(), next.Passkeys.RequireRoles)
	if err != nil {
		return nil, err
	}
	hostOK := s.passkeyHostOK()
	if len(next.Passkeys.RequireRoles) > 0 && !hostOK || next.Passkeys.Enabled && !cur.Passkeys.Enabled && required > 0 && !hostOK {
		return nil, coded(400, "passkey_unavailable", "passkeys need a --public-url that is https (or http on a loopback name)")
	}
	if !next.Passkeys.Enabled && required > 0 {
		return nil, coded(400, "passkey_requirement_unsatisfiable", "a passkey is required of someone: lift the requirements before switching passkeys off")
	}

	// Seal the new secret only once everything has passed.
	if secretGiven {
		if newSecret == "" {
			next.Captcha.SecretEnc = ""
		} else {
			enc, err := s.seal("captcha:"+next.Captcha.Provider, newSecret)
			if err != nil {
				return nil, err
			}
			next.Captcha.SecretEnc = enc
		}
	}
	if next.Captcha.Provider == CaptchaImage {
		next.Captcha.SecretEnc, next.Captcha.SiteKey = "", ""
	}
	from, to := securityDiff(cur.Security, next, secretGiven)
	if len(to) == 0 {
		return s.sec(ctx), nil
	}
	nst, err := s.saveSecurity(ctx, next)
	if err != nil {
		return nil, err
	}
	s.Audit(AuditEvent{Kind: "security_changed", Actor: actorID, IP: ip, Detail: map[string]any{"from": from, "to": to}})
	return nst, nil
}

// securityDiff lists the changed fields, as dotted paths; a secret shows as "changed".
func securityDiff(a, b Security, secretGiven bool) (from, to map[string]any) {
	from, to = map[string]any{}, map[string]any{}
	add := func(k string, x, y any) {
		if fmt.Sprint(x) != fmt.Sprint(y) {
			from[k], to[k] = x, y
		}
	}
	add("login.mode", a.Login.Mode, b.Login.Mode)
	add("login.ssoOnly", a.Login.SSOOnly, b.Login.SSOOnly)
	add("passkeys.enabled", a.Passkeys.Enabled, b.Passkeys.Enabled)
	add("passkeys.requireRoles", strings.Join(a.Passkeys.RequireRoles, ","), strings.Join(b.Passkeys.RequireRoles, ","))
	add("captcha.provider", a.Captcha.Provider, b.Captcha.Provider)
	add("captcha.siteKey", a.Captcha.SiteKey, b.Captcha.SiteKey)
	if secretGiven || a.Captcha.SecretEnc != b.Captcha.SecretEnc {
		from["captcha.secret"], to["captcha.secret"] = "changed", "changed"
	}
	for _, k := range CaptchaContexts {
		x, y := a.Captcha.Contexts[k], b.Captcha.Contexts[k]
		add("captcha.contexts."+k+".on", x.On, y.On)
		add("captcha.contexts."+k+".when", x.When, y.When)
		add("captcha.contexts."+k+".failures", x.Failures, y.Failures)
	}
	add("lockout.enabled", a.Lockout.Enabled, b.Lockout.Enabled)
	add("lockout.minutes", a.Lockout.Minutes, b.Lockout.Minutes)
	add("lockout.scope", a.Lockout.Scope, b.Lockout.Scope)
	add("limits.perMinute", a.Limits.PerMinute, b.Limits.PerMinute)
	add("limits.maxFailures", a.Limits.MaxFailures, b.Limits.MaxFailures)
	add("limits.windowMinutes", a.Limits.WindowMinutes, b.Limits.WindowMinutes)
	add("limits.sessionIdleDays", a.Limits.SessionIdleDays, b.Limits.SessionIdleDays)
	add("limits.sessionMaxDays", a.Limits.SessionMaxDays, b.Limits.SessionMaxDays)
	return from, to
}

// ResetSecurity is what "petalbook security reset" does: the login mode back
// to dual, ssoOnly, lockout and every captcha context off, no role required to
// use a passkey. Providers and people are untouched.
func (s *Service) ResetSecurity(ctx context.Context) error {
	s.secMu.Lock()
	defer s.secMu.Unlock()
	cur, err := s.loadSecurity(ctx)
	if err != nil {
		return err
	}
	next := cur.Security.clone()
	next.Login.Mode, next.Login.SSOOnly = ModeDual, false
	next.Lockout.Enabled = false
	for k, c := range next.Captcha.Contexts {
		c.On = false
		next.Captcha.Contexts[k] = c
	}
	next.Passkeys.RequireRoles = []string{}
	if _, err := s.saveSecurity(ctx, next); err != nil {
		return err
	}
	s.Audit(AuditEvent{Kind: "security_changed", Detail: map[string]any{"by": "cli"}})
	s.FlushAudit(ctx)
	return nil
}

// SecurityView is what GET /api/admin/security shows.
type SecurityView struct {
	Security
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
	v := &SecurityView{Security: st.Security.clone()}
	v.CaptchaHasSecret = st.Captcha.SecretEnc != ""
	v.Captcha.SecretEnc = ""
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
		return coded(400, "local_admin_required", "no enabled administrator has a password: refusing passwords would lock everyone out if the identity provider fails")
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
