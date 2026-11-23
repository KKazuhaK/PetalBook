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
	return coded(400, "captcha_failed", "the captcha was missing, wrong or expired; try the new one")
}

func (s *Service) initCaptcha() {
	s.capStore = captcha.NewMemoryStore(captcha.DefaultMemoryStoreCapacity, captcha.DefaultMemoryStoreTTL)
	s.capImg = captcha.NewImageGenerator(s.capStore)
}

// captchaRebuild builds the verifier of a third-party provider when the
// settings in force changed.
func (s *Service) captchaRebuild(nst, old *secState) {
	c := nst.Captcha
	if c.Provider == CaptchaImage || c.SecretEnc == "" {
		s.capTok.Store(nil)
		s.capSecretErr.Store(false)
		return
	}
	if old != nil && old.Captcha.Provider == c.Provider && old.Captcha.SecretEnc == c.SecretEnc && s.capTok.Load() != nil {
		return
	}
	secret, err := s.open("captcha:"+c.Provider, c.SecretEnc)
	if err != nil {
		s.log.Warn("the stored captcha secret cannot be opened with this server's key")
		s.capTok.Store(nil)
		s.capSecretErr.Store(true)
		return
	}
	s.capSecretErr.Store(false)
	opts := []captcha.TokenOption{captcha.WithAllowedHostnames(captcha.HostOf(s.cfg.PublicURL))}
	if s.cfg.HTTPClient != nil {
		opts = append(opts, captcha.WithHTTPClient(s.cfg.HTTPClient))
	}
	if s.cfg.CaptchaEndpoint != "" {
		opts = append(opts, captcha.WithEndpoint(s.cfg.CaptchaEndpoint))
	}
	v, err := captcha.NewTokenVerifier(captcha.Provider(c.Provider), secret, opts...)
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

// captchaRequired says whether the context asks for a captcha of this address
// (and, for the login, this username).
func (s *Service) captchaRequired(st *secState, ctxName, ip, username string) bool {
	c, ok := st.Captcha.Contexts[ctxName]
	if !ok || !c.On {
		return false
	}
	if c.When == "always" {
		return true
	}
	if s.failIP.count("ip:"+ip, st.window) >= c.Failures {
		return true
	}
	return ctxName == CtxLogin && username != "" && s.failUser.count(userKey(username), st.window) >= c.Failures
}

// CaptchaFor answers GET /api/captcha.
func (s *Service) CaptchaFor(ctx context.Context, ctxName, account, ip string) (*CaptchaChallenge, error) {
	if err := s.allow(ctx, ip); err != nil {
		return nil, err
	}
	known := false
	for _, c := range append(append([]string{}, CaptchaContexts...), reservedContexts...) {
		known = known || c == ctxName
	}
	if !known {
		return nil, errf(Invalid, "ctx is login, invite, setup, forgot or register")
	}
	st := s.sec(ctx)
	if !s.captchaRequired(st, ctxName, ip, account) {
		return &CaptchaChallenge{}, nil
	}
	out := &CaptchaChallenge{Required: true, Provider: st.Captcha.Provider}
	if st.Captcha.Provider != CaptchaImage {
		out.SiteKey = st.Captcha.SiteKey
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
// address and the answer is not right. It runs before anything costs work.
func (s *Service) checkCaptcha(ctx context.Context, st *secState, ctxName, username, ip string, ans *CaptchaAnswer) error {
	if !s.captchaRequired(st, ctxName, ip, username) {
		return nil
	}
	if ans == nil || (ans.Token == "" && (ans.ID == "" || ans.Answer == "")) {
		return errCaptcha()
	}
	if st.Captcha.Provider == CaptchaImage {
		if !s.capImg.Verify(ans.ID, ans.Answer) {
			return errCaptcha()
		}
		return nil
	}
	tc := s.capTok.Load()
	if tc == nil || tc.provider != st.Captcha.Provider || ans.Token == "" || len(ans.Token) > 4096 {
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
