package auth

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/KazuhaHub/authcore/captcha"
)

// CaptchaAnswer is what a form sends: {id, answer} for the image, {token} for a third party.
type CaptchaAnswer struct {
	ID     string `json:"id"`
	Answer string `json:"answer"`
	Token  string `json:"token"`
}

// CaptchaChallenge answers GET /api/captcha.
type CaptchaChallenge struct {
	Required bool
	Provider string
	SiteKey  string
	ID       string
	Image    string
}

type tokenChecker struct {
	provider string
	v        *captcha.TokenVerifier
}

func errCaptcha() *Error {
	return coded(400, "captcha_failed", "The captcha was missing, wrong or expired; try the new one.")
}

func (s *Service) initCaptcha() {
	s.capStore = captcha.NewMemoryStore(captcha.DefaultMemoryStoreCapacity, captcha.DefaultMemoryStoreTTL)
	s.capImg = captcha.NewImageGenerator(s.capStore)
}

// captchaRebuild builds the verifier of a third-party provider when the
// settings in force changed.
func (s *Service) captchaRebuild(nst, old *secState) {
	c := nst.Captcha
	if c.Provider == CaptchaImage || !c.HasSecret {
		s.capTok.Store(nil)
		s.capSecretErr.Store(false)
		return
	}
	if old != nil && old.Captcha.Provider == c.Provider && old.Captcha.secret == c.secret && s.capTok.Load() != nil {
		return
	}
	if c.SecretUnreadable {
		s.log.Warn("the stored captcha secret cannot be opened with this server's key")
		s.capTok.Store(nil)
		s.capSecretErr.Store(true)
		return
	}
	secret := c.secret
	s.capSecretErr.Store(false)
	v, err := s.newVerifier(c.Provider, secret)
	if err != nil {
		s.log.Warn("could not set up the captcha check", "reason", err.Error())
		s.capTok.Store(nil)
		return
	}
	s.capTok.Store(&tokenChecker{provider: c.Provider, v: v})
}

// CaptchaProvider is the third-party captcha provider in force ("" for the
// built-in image), for the web app's page policy. It reads the cached
// settings, so it costs no query on most calls.
func (s *Service) CaptchaProvider(ctx context.Context) string {
	if p := s.sec(ctx).Captcha.Provider; p != CaptchaImage {
		return p
	}
	return ""
}

// needs says whether a setting of a form asks a captcha of this address (and,
// for the login, this username).
func (s *Service) needs(st *secState, c CaptchaCtx, ctxName, ip, username string) bool {
	if !c.On {
		return false
	}
	if c.When == "always" {
		return true
	}
	if s.failIP.Count("ip:"+ip, st.window) >= c.Failures {
		return true
	}
	return ctxName == CtxLogin && username != "" && s.failTyped.Count(userKey(username), st.window) >= c.Failures
}

// capPlan is what a form is asked, for a request scoped to a tenant (tenant "":
// the request names none, and the site's settings are used).
//
// required: the stricter of the site's setting and the tenant's, never less
// than the site's (captchaCtxOf).
//
// own: whose provider asks and checks it. The site's (nil) wherever the site's
// own setting asks a captcha of this address and name. The tenant's own
// provider only where the tenant alone asks: whoever controls a provider's
// secret can make it pass for anything, so it may stand in for no captcha, never
// for the site's. The choice depends on the tenant the request names, the
// settings and the failures counted for the address and the name the form
// carries, and never on whether the name is an account or whose.
type capPlan struct {
	required bool
	own      *tenantCaptcha
}

func (s *Service) captchaPlan(ctx context.Context, st *secState, tenant, ctxName, ip, username string) capPlan {
	c, ok := s.captchaCtxOf(ctx, st, tenant, ctxName)
	if !ok || !s.needs(st, c, ctxName, ip, username) {
		return capPlan{}
	}
	if site, found := st.Captcha.Contexts[ctxName]; found && s.needs(st, site, ctxName, ip, username) {
		return capPlan{required: true}
	}
	return capPlan{required: true, own: s.tenantCaptchaFor(ctx, tenant)}
}

// CaptchaFor answers GET /api/captcha. tenant is the id of the tenant the
// request is scoped to, or "" for none: the captcha of login, forgot and
// register is then the stricter of the tenant's and the site's, asked by the
// provider captchaPlan names.
func (s *Service) CaptchaFor(ctx context.Context, tenant, ctxName, account, ip string) (*CaptchaChallenge, error) {
	if err := s.allow(ctx, ip); err != nil {
		return nil, err
	}
	known := false
	for _, c := range CaptchaContexts {
		known = known || c == ctxName
	}
	if !known {
		return nil, errf(Invalid, "Ctx is login, invite, setup, forgot or register.")
	}
	st := s.sec(ctx)
	plan := s.captchaPlan(ctx, st, tenant, ctxName, ip, account)
	if !plan.required {
		return &CaptchaChallenge{}, nil
	}
	provider, siteKey := st.Captcha.Provider, st.Captcha.SiteKey
	if plan.own != nil {
		provider, siteKey = plan.own.Provider, plan.own.SiteKey
	}
	out := &CaptchaChallenge{Required: true, Provider: provider}
	if provider != CaptchaImage {
		out.SiteKey = siteKey
		return out, nil
	}
	ch, err := s.capImg.Generate()
	if err != nil {
		return nil, err
	}
	out.ID, out.Image = ch.ID, ch.Image
	return out, nil
}

// checkCaptcha refuses the request when the context needs a captcha of this
// address and the answer is not right. It runs before anything costs work. The
// provider that checks is captchaPlan's, never one the request chose.
func (s *Service) checkCaptcha(ctx context.Context, st *secState, tenant, ctxName, username, ip string, ans *CaptchaAnswer) error {
	plan := s.captchaPlan(ctx, st, tenant, ctxName, ip, username)
	if !plan.required {
		return nil
	}
	if ans == nil || (ans.Token == "" && (ans.ID == "" || ans.Answer == "")) {
		return errCaptcha()
	}
	provider, tc := st.Captcha.Provider, s.capTok.Load()
	if plan.own != nil {
		provider, tc = plan.own.Provider, nil
		if plan.own.v != nil {
			tc = &tokenChecker{provider: provider, v: plan.own.v}
		}
	}
	if provider == CaptchaImage {
		if !s.capImg.Verify(ans.ID, ans.Answer) {
			return errCaptcha()
		}
		return nil
	}
	if tc == nil || tc.provider != provider || ans.Token == "" || len(ans.Token) > 4096 {
		return errCaptcha()
	}
	res, err := tc.v.Verify(ctx, ans.Token, ip)
	if err != nil {
		// The check could not be made: the form is not let through.
		s.log.Warn("the captcha provider could not be asked", "provider", tc.provider, "reason", captchaReason(err))
		return errCaptcha()
	}
	if !res.Success {
		if res.HostnameMismatch {
			s.log.Warn("the captcha was solved on another host", "provider", tc.provider)
		}
		return errCaptcha()
	}
	return nil
}

func captchaReason(err error) string {
	switch {
	case errors.Is(err, captcha.ErrNetwork):
		return "network"
	case errors.Is(err, captcha.ErrBadStatus):
		return "status"
	case errors.Is(err, captcha.ErrDecodeResponse):
		return "unreadable answer"
	}
	return "other"
}

// publicHost is the host of the public URL ("" when there is none).
func (s *Service) publicHost() string {
	u, err := url.Parse(s.cfg.PublicURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}
