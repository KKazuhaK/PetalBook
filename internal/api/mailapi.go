package api

import (
	"net/http"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/mail"
	"petalbook.app/server/internal/store"
)

// Mail, registration, email verification and password reset (milestone 3.3,
// docs/api.md "Mail", "Registration", "Email verification", "Password reset").

var mailRoutes = []route{
	{"GET", "/api/admin/mail", siteAdmin, true, false, false, (*server).adminGetMail},
	{"PUT", "/api/admin/mail", siteAdmin, true, false, false, (*server).adminPutMail},
	{"POST", "/api/admin/mail/test", siteAdmin, true, false, false, (*server).adminTestMail},

	{"GET", "/api/register", public, true, false, false, (*server).getRegister},
	{"POST", "/api/register", public, true, false, false, (*server).postRegister},
	{"POST", "/api/register/confirm", public, true, false, false, (*server).registerConfirm},

	{"POST", "/api/account/email/verify", signedIn, true, false, false, (*server).accountEmailVerify},
	{"POST", "/api/email/confirm", public, true, false, false, (*server).emailConfirm},

	{"POST", "/api/password/forgot", public, true, false, false, (*server).passwordForgot},
	{"POST", "/api/password/reset/check", public, true, false, false, (*server).passwordResetCheck},
	{"POST", "/api/password/reset", public, true, false, false, (*server).passwordReset},
	{"POST", "/api/users/{id}/password-reset", admin, true, false, false, (*server).userPasswordReset},
}

func init() { routeTable = append(routeTable, mailRoutes...) }

// langOf is the language of a message: the body's, else Accept-Language, else English.
func langOf(c *call, body string) string {
	return mail.PickLang(body, c.r.Header.Get("Accept-Language"))
}

// tenantOfRequest is the tenant a public endpoint names by slug (the default
// tenant when none); an unknown or disabled one is answered as not found.
func (s *server) tenantOfRequest(c *call, slug string) (string, bool) {
	if slug == "" {
		return store.DefaultTenantID, true
	}
	r, ok := s.publicTenant(c, slug)
	return r.ID, ok
}

// --- mail settings -------------------------------------------------------------------

type mailResp struct {
	Enabled     bool    `json:"enabled"`
	Host        string  `json:"host"`
	Port        int     `json:"port"`
	Security    string  `json:"security"`
	Username    string  `json:"username"`
	HasPassword bool    `json:"hasPassword"`
	From        string  `json:"from"`
	FromName    string  `json:"fromName"`
	ReplyTo     string  `json:"replyTo"`
	Status      mailSt  `json:"status"`
	Queue       mailQue `json:"queue"`
}

type mailSt struct {
	OK      bool    `json:"ok"`
	Checked *string `json:"checked"`
	Message string  `json:"message"`
}

type mailQue struct {
	Waiting        int `json:"waiting"`
	SentLastHour   int `json:"sentLastHour"`
	FailedLastHour int `json:"failedLastHour"`
}

func toMail(v *auth.MailView) mailResp {
	r := mailResp{Enabled: v.Enabled, Host: v.Host, Port: v.Port, Security: v.Security, Username: v.Username, HasPassword: v.HasPassword,
		From: v.From, FromName: v.FromName, ReplyTo: v.ReplyTo, Status: mailSt{OK: v.Status.OK, Message: v.Status.Message},
		Queue: mailQue{v.Queue.Waiting, v.Queue.SentLastHour, v.Queue.FailedLastHour}}
	if !v.Status.Checked.IsZero() {
		t := rfc3339(v.Status.Checked)
		r.Status.Checked = &t
	}
	return r
}

func (s *server) adminGetMail(c *call) {
	v, err := s.auth.GetMail(c.r.Context())
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, toMail(v))
}

func (s *server) adminPutMail(c *call) {
	var req struct {
		Enabled  *bool   `json:"enabled"`
		Host     *string `json:"host"`
		Port     *int    `json:"port"`
		Security *string `json:"security"`
		Username *string `json:"username"`
		Password *string `json:"password"`
		From     *string `json:"from"`
		FromName *string `json:"fromName"`
		ReplyTo  *string `json:"replyTo"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	v, err := s.auth.UpdateMail(c.r.Context(), c.user().ID, s.auth.ClientAddr(c.r), auth.MailPatch{Enabled: req.Enabled, Host: req.Host, Port: req.Port,
		Security: req.Security, Username: req.Username, Password: req.Password, From: req.From, FromName: req.FromName, ReplyTo: req.ReplyTo})
	if err != nil {
		s.fail(c.w, err)
		return
	}
	// Mail off closes registration in effect, and closing cancels the pending ones.
	if err := s.auth.CancelClosedRegistrations(c.r.Context()); err != nil {
		s.log.Warn("could not cancel pending registrations", "error", err)
	}
	writeJSON(c.w, http.StatusOK, toMail(v))
}

func (s *server) adminTestMail(c *call) {
	var req struct {
		To   string `json:"to"`
		Lang string `json:"lang"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	res, err := s.auth.TestMail(c.r.Context(), c.user().ID, s.auth.ClientAddr(c.r), req.To, langOf(c, req.Lang))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	out := map[string]any{"ok": res.OK}
	if !res.OK {
		out["message"] = res.Message
	}
	writeJSON(c.w, http.StatusOK, out)
}

// --- registration ---------------------------------------------------------------------

func (s *server) getRegister(c *call) {
	c.w.Header().Set("Cache-Control", "no-store")
	if err := s.auth.AllowPublic(c.r.Context(), s.auth.ClientAddr(c.r)); err != nil {
		s.fail(c.w, err)
		return
	}
	id, ok := s.tenantOfRequest(c, c.r.URL.Query().Get("tenant"))
	if !ok {
		return
	}
	rules, err := s.auth.Registration(c.r.Context(), id)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	if !rules.Open {
		writeJSON(c.w, http.StatusOK, map[string]any{"open": false})
		return
	}
	ref, _ := s.opt.Store.TenantRefOf(id)
	domains := rules.Domains
	if domains == nil {
		domains = []string{}
	}
	writeJSON(c.w, http.StatusOK, map[string]any{"open": true, "tenant": map[string]string{"slug": ref.Slug, "name": ref.Name},
		"domains": domains, "termsUrl": rules.TermsURL, "privacyUrl": rules.PrivacyURL, "password": map[string]int{"minLength": auth.MinPasswordChars}})
}

func (s *server) postRegister(c *call) {
	var req struct {
		Tenant      string              `json:"tenant"`
		Username    string              `json:"username"`
		Name        string              `json:"name"`
		Email       string              `json:"email"`
		Password    string              `json:"password"`
		AcceptTerms bool                `json:"acceptTerms"`
		Captcha     *auth.CaptchaAnswer `json:"captcha"`
		Lang        string              `json:"lang"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	id, ok := s.tenantOfRequest(c, req.Tenant)
	if !ok {
		return
	}
	err := s.auth.Register(c.r.Context(), auth.RegisterParams{TenantID: id, Username: req.Username, Name: req.Name, Email: req.Email, Password: req.Password,
		AcceptTerms: req.AcceptTerms, Captcha: req.Captcha, Lang: langOf(c, req.Lang), IP: s.auth.ClientAddr(c.r)})
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusAccepted, map[string]bool{"sent": true})
}

type tokenReq struct {
	Token string `json:"token"`
}

func (s *server) registerConfirm(c *call) {
	var req tokenReq
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	u, tok, err := s.auth.RegisterConfirm(c.r.Context(), req.Token, s.auth.ClientAddr(c.r))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	s.startSession(c, tok)
	writeJSON(c.w, http.StatusCreated, struct {
		User userResp `json:"user"`
	}{s.toUser(*u)})
}

// --- email verification ---------------------------------------------------------------

func (s *server) accountEmailVerify(c *call) {
	var req struct {
		Lang string `json:"lang"`
	}
	if !s.readJSONMax(c.w, c.r, &req, true, authBodyLimit) {
		return
	}
	if err := s.auth.RequestEmailVerify(c.r.Context(), c.user(), langOf(c, req.Lang), s.auth.ClientAddr(c.r)); err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusAccepted, map[string]bool{"sent": true})
}

func (s *server) emailConfirm(c *call) {
	var req tokenReq
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	if err := s.auth.ConfirmEmail(c.r.Context(), req.Token, s.auth.ClientAddr(c.r)); err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
}

// --- password reset -------------------------------------------------------------------

func (s *server) passwordForgot(c *call) {
	var req struct {
		Tenant  string              `json:"tenant"`
		Login   string              `json:"login"`
		Captcha *auth.CaptchaAnswer `json:"captcha"`
		Lang    string              `json:"lang"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	id, ok := s.tenantOfRequest(c, req.Tenant)
	if !ok {
		return
	}
	if err := s.auth.Forgot(c.r.Context(), auth.ForgotParams{TenantID: id, Login: req.Login, Captcha: req.Captcha, Lang: langOf(c, req.Lang), IP: s.auth.ClientAddr(c.r)}); err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusAccepted, map[string]bool{"sent": true})
}

func (s *server) passwordResetCheck(c *call) {
	var req tokenReq
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	username, expires, err := s.auth.ResetCheck(c.r.Context(), req.Token, s.auth.ClientAddr(c.r))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, map[string]string{"username": username, "expires": rfc3339(expires)})
}

func (s *server) passwordReset(c *call) {
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	if err := s.auth.ResetPassword(c.r.Context(), req.Token, req.Password, s.auth.ClientAddr(c.r)); err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
}

func (s *server) userPasswordReset(c *call) {
	var req struct {
		Lang string `json:"lang"`
	}
	if !s.readJSONMax(c.w, c.r, &req, true, authBodyLimit) {
		return
	}
	if err := s.auth.AdminPasswordReset(c.r.Context(), c.user(), s.auth.ClientAddr(c.r), c.r.PathValue("id"), langOf(c, req.Lang)); err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusAccepted, map[string]bool{"sent": true})
}
