package api

import (
	"net/http"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/mail"
	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
)

// Mail, registration, email verification and password reset (milestone 3.3,
// docs/api.md "Mail", "Registration", "Email verification", "Password reset").

var mailRoutes = []route{
	{"GET", "/api/admin/mail", needs(authz.SiteMailManage).always(), true, false, false, (*server).adminGetMail},
	{"PUT", "/api/admin/mail", needs(authz.SiteMailManage).always(), true, false, false, (*server).adminPutMail},
	{"POST", "/api/admin/mail/test", needs(authz.SiteMailManage).always(), true, false, false, (*server).adminTestMail},

	{"GET", "/api/register", public.always(), true, false, false, (*server).getRegister},
	{"POST", "/api/register", public.always(), true, false, false, (*server).postRegister},
	{"POST", "/api/register/confirm", public.always(), true, false, false, (*server).registerConfirm},
	// The waiting page of a sign-up that asked to join an organisation (3.6 d5):
	// the registration link's token shows the request, and withdraws it.
	{"POST", "/api/register/request", public.always(), true, false, false, (*server).registerRequestStatus},
	{"POST", "/api/register/request/withdraw", public.always(), true, false, false, (*server).registerRequestWithdraw},

	{"POST", "/api/account/email/verify", signedIn.always(), true, false, false, (*server).accountEmailVerify},
	// More than one address (accounts.multipleEmails): the list is GET /api/account's.
	{"POST", "/api/account/emails", signedIn.on(settings.AccountsMultipleEmails), true, false, false, (*server).accountEmailAdd},
	{"POST", "/api/account/emails/verify", signedIn.on(settings.AccountsMultipleEmails), true, false, false, (*server).accountEmailResend},
	{"POST", "/api/account/emails/primary", signedIn.on(settings.AccountsMultipleEmails), true, false, false, (*server).accountEmailPrimary},
	{"DELETE", "/api/account/emails/{email}", signedIn.on(settings.AccountsMultipleEmails), true, false, false, (*server).accountEmailRemove},
	{"POST", "/api/email/confirm", public.always(), true, false, false, (*server).emailConfirm},

	{"POST", "/api/password/forgot", public.always(), true, false, false, (*server).passwordForgot},
	{"POST", "/api/password/reset/check", public.always(), true, false, false, (*server).passwordResetCheck},
	{"POST", "/api/password/reset", public.always(), true, false, false, (*server).passwordReset},
	{"POST", "/api/users/{id}/password-reset", needs(authz.TenantUsersManage).always(), true, false, false, (*server).userPasswordReset},
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
	// A person may also make an organisation of their own (the default tenant's
	// terms apply), when the site lets them.
	orgOpen := s.auth.OrgCreation(c.r.Context()).Open
	// An address of a domain that joins its organisation signs up whatever
	// the tenant's own registration says (3.6 d5).
	domainJoin := s.auth.DomainJoinOpen(c.r.Context())
	if !rules.Open && !orgOpen && !domainJoin {
		writeJSON(c.w, http.StatusOK, map[string]any{"open": false, "orgCreation": map[string]bool{"open": false}, "domainJoin": false})
		return
	}
	joinOpen := rules.Open
	if !rules.Open {
		id = store.DefaultTenantID
		if rules, err = s.auth.Registration(c.r.Context(), id); err != nil {
			s.fail(c.w, err)
			return
		}
	}
	ref, _ := s.opt.Store.TenantRefOf(id)
	domains := rules.Domains
	if domains == nil {
		domains = []string{}
	}
	writeJSON(c.w, http.StatusOK, map[string]any{"open": joinOpen, "orgCreation": map[string]bool{"open": orgOpen}, "domainJoin": domainJoin,
		"tenant": map[string]string{"slug": ref.Slug, "name": ref.Name}, "domains": domains, "termsUrl": rules.TermsURL, "privacyUrl": rules.PrivacyURL, "password": map[string]int{"minLength": auth.MinPasswordChars}})
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
		// Org asks for an organisation of the person's own (the site must allow it).
		Org *struct {
			Name string `json:"name"`
			Slug string `json:"slug"`
		} `json:"org"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	id, ok := store.DefaultTenantID, true
	if req.Org == nil {
		if id, ok = s.tenantOfRequest(c, req.Tenant); !ok {
			return
		}
	}
	p := auth.RegisterParams{TenantID: id, Username: req.Username, Name: req.Name, Email: req.Email, Password: req.Password,
		AcceptTerms: req.AcceptTerms, Captcha: req.Captcha, Lang: langOf(c, req.Lang), IP: s.auth.ClientAddr(c.r)}
	if req.Org != nil {
		p.Org = &auth.OrgParams{Name: req.Org.Name, Slug: req.Org.Slug}
	}
	err := s.auth.Register(c.r.Context(), p)
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
	res, err := s.auth.RegisterConfirm(c.r.Context(), req.Token, langOf(c, ""), s.auth.ClientAddr(c.r))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	if res.Request != nil {
		// A request to join: no account yet, and nobody is signed in.
		writeJSON(c.w, http.StatusAccepted, struct {
			Request joinStatusResp `json:"request"`
		}{joinStatusOf(res.Request)})
		return
	}
	s.startSession(c, res.Token)
	user, hold, ok := s.signedIn(c, *res.User, res.Held, res.HeldBy)
	if !ok {
		return
	}
	out := struct {
		signInAnswer
		Tenant *struct {
			Slug    string `json:"slug"`
			Name    string `json:"name"`
			Renamed bool   `json:"renamed"`
		} `json:"tenant,omitempty"`
		Joined *tenantNameResp `json:"joined,omitempty"`
	}{signInAnswer: signInAnswer{user, hold}}
	if res.Joined != nil {
		out.Joined = &tenantNameResp{res.Joined.Slug, res.Joined.Name}
	}
	if res.Org != nil {
		out.Tenant = &struct {
			Slug    string `json:"slug"`
			Name    string `json:"name"`
			Renamed bool   `json:"renamed"`
		}{res.Org.Slug, res.Org.Name, res.Renamed}
	}
	writeJSON(c.w, http.StatusCreated, out)
}

// tenantNameResp names an organisation in an answer.
type tenantNameResp struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// joinStatusResp is a request to join as its waiting page shows it.
type joinStatusResp struct {
	State     string         `json:"state"`
	Tenant    tenantNameResp `json:"tenant"`
	Username  string         `json:"username"`
	Email     string         `json:"email"`
	Created   string         `json:"created"`
	Expires   string         `json:"expires"`
	DecidedAt *string        `json:"decidedAt"`
}

func joinStatusOf(j *auth.JoinStatus) joinStatusResp {
	return joinStatusResp{State: j.State, Tenant: tenantNameResp{j.TenantSlug, j.TenantName}, Username: j.Username, Email: j.Email,
		Created: rfc3339(j.Created), Expires: rfc3339(j.Expires), DecidedAt: optTime(j.DecidedAt)}
}

// registerRequestStatus is POST /api/register/request {"token"}: the request
// a registration link became, for its waiting page.
func (s *server) registerRequestStatus(c *call) {
	var req tokenReq
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	j, err := s.auth.JoinRequestStatus(c.r.Context(), req.Token, s.auth.ClientAddr(c.r))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.Header().Set("Cache-Control", "no-store")
	writeJSON(c.w, http.StatusOK, joinStatusOf(j))
}

// registerRequestWithdraw is POST /api/register/request/withdraw {"token"}:
// the person withdraws their pending request.
func (s *server) registerRequestWithdraw(c *call) {
	var req tokenReq
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	j, err := s.auth.WithdrawJoinRequest(c.r.Context(), req.Token, s.auth.ClientAddr(c.r))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, joinStatusOf(j))
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

// emailReq is the body of the routes about one of the person's addresses.
type emailReq struct {
	Email string `json:"email"`
	Lang  string `json:"lang"`
}

// emailResp is one address of an account as the API shows it (docs/api.md,
// "More than one email address"). MarkedByAdmin: verified by a tenant
// administrator's mark alone, which the person may still confirm.
type emailResp struct {
	Email         string `json:"email"`
	Primary       bool   `json:"primary"`
	Verified      bool   `json:"verified"`
	MarkedByAdmin bool   `json:"markedByAdmin,omitempty"`
	// Pending: a claim of an address another account holds unverified; it has
	// no row until its link is followed, and DELETE withdraws it.
	Pending bool   `json:"pending,omitempty"`
	Created string `json:"created"`
}

func toEmails(rows []store.EmailRow) []emailResp {
	out := make([]emailResp, len(rows))
	for i, r := range rows {
		out[i] = emailResp{r.Email, r.Primary, r.Verified, r.Verified && r.VerifiedBy == store.VerifiedByTenantAdmin, false, rfc3339(r.Created)}
	}
	return out
}

// withClaims adds the pending claims after the addresses.
func withClaims(l []emailResp, claims []store.EmailClaim) []emailResp {
	for _, c := range claims {
		l = append(l, emailResp{Email: c.Email, Pending: true, Created: rfc3339(c.Created)})
	}
	return l
}

func (s *server) accountEmailAdd(c *call) {
	var req emailReq
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	if err := s.auth.AddEmail(c.r.Context(), c.user(), req.Email, langOf(c, req.Lang), s.auth.ClientAddr(c.r)); err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusAccepted, map[string]bool{"sent": true})
}

func (s *server) accountEmailResend(c *call) {
	var req emailReq
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	if err := s.auth.ResendEmailVerify(c.r.Context(), c.user(), req.Email, langOf(c, req.Lang), s.auth.ClientAddr(c.r)); err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusAccepted, map[string]bool{"sent": true})
}

func (s *server) accountEmailPrimary(c *call) {
	var req emailReq
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	u, err := s.auth.MakePrimaryEmail(c.r.Context(), c.user(), req.Email, langOf(c, req.Lang), s.auth.ClientAddr(c.r))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		User userResp `json:"user"`
	}{s.toUser(*u)})
}

func (s *server) accountEmailRemove(c *call) {
	if err := s.auth.RemoveEmail(c.r.Context(), c.user(), c.r.PathValue("email"), langOf(c, ""), s.auth.ClientAddr(c.r)); err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
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
	if err := s.auth.AdminPasswordReset(c.r.Context(), c.user(), c.p.Access, s.auth.ClientAddr(c.r), c.r.PathValue("id"), langOf(c, req.Lang)); err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusAccepted, map[string]bool{"sent": true})
}
