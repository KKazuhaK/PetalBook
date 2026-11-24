package api

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/KKazuhaK/PetalBook/internal/auth"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

const samlACSPattern = "/api/auth/saml/{slug}/acs"

// heldAllowed are the routes a session that must register a passkey may still
// use (docs/api.md, "When a passkey is required"); everything else is refused.
var heldAllowed = map[string]bool{
	"GET /api/session":                  true,
	"GET /api/meta":                     true,
	"GET /api/auth/providers":           true,
	"GET /api/account":                  true,
	"GET /api/account/passkeys":         true,
	"POST /api/account/passkeys/begin":  true,
	"POST /api/account/passkeys/finish": true,
	"PATCH /api/account/passkeys/{id}":  true,
	"DELETE /api/account/passkeys/{id}": true,
	"POST /api/logout":                  true,
}

// signinRoutes are the routes of milestone 2.8, joined to the table at start-up.
var signinRoutes = []route{
	{"GET", "/api/auth/providers", public, true, false, false, (*server).getAuthProviders},
	{"GET", "/api/captcha", public, true, false, false, (*server).getCaptcha},

	{"GET", "/api/auth/{kind}/{slug}/start", public, true, false, false, (*server).ssoStartGET},
	{"POST", "/api/auth/{kind}/{slug}/start", public, true, true, false, (*server).ssoStartPOST},
	{"GET", "/api/auth/oidc/{slug}/callback", public, true, false, false, (*server).oidcSlugCallback},
	{"GET", "/api/auth/github/{slug}/callback", public, true, false, false, (*server).oidcSlugCallback},
	{"POST", samlACSPattern, public, true, false, false, (*server).samlACS},
	{"GET", "/api/auth/saml/{slug}/metadata", public, true, false, false, (*server).samlMetadata},

	{"POST", "/api/auth/passkey/begin", public, true, false, false, (*server).passkeyBegin},
	{"POST", "/api/auth/passkey/finish", public, true, false, false, (*server).passkeyFinish},

	{"GET", "/api/account/passkeys", signedIn, true, false, false, (*server).listAccountPasskeys},
	{"POST", "/api/account/passkeys/begin", signedIn, true, false, false, (*server).accountPasskeyBegin},
	{"POST", "/api/account/passkeys/finish", signedIn, true, false, false, (*server).accountPasskeyFinish},
	{"PATCH", "/api/account/passkeys/{id}", signedIn, true, false, false, (*server).renameAccountPasskey},
	{"DELETE", "/api/account/passkeys/{id}", signedIn, true, false, false, (*server).deleteAccountPasskey},

	{"GET", "/api/users/{id}/passkeys", admin, true, false, false, (*server).listUserPasskeys},
	{"DELETE", "/api/users/{id}/passkeys/{passkey}", admin, true, false, false, (*server).deleteUserPasskey},

	{"GET", "/api/admin/security", siteAdmin, true, false, false, (*server).adminGetSecurity},
	{"PUT", "/api/admin/security", siteAdmin, true, false, false, (*server).adminPutSecurity},
	{"GET", "/api/admin/auth/providers", siteAdmin, true, false, false, (*server).adminListProviders},
	{"POST", "/api/admin/auth/providers", siteAdmin, true, false, false, (*server).adminCreateProvider},
	{"GET", "/api/admin/auth/providers/{slug}", siteAdmin, true, false, false, (*server).adminGetProvider},
	{"PATCH", "/api/admin/auth/providers/{slug}", siteAdmin, true, false, false, (*server).adminPatchProvider},
	{"DELETE", "/api/admin/auth/providers/{slug}", siteAdmin, true, false, false, (*server).adminDeleteProvider},
	{"POST", "/api/admin/auth/providers/{slug}/refresh", siteAdmin, true, false, false, (*server).adminRefreshProvider},
	{"GET", "/api/admin/audit", admin, true, false, false, (*server).adminAudit},
}

func init() { routeTable = append(routeTable, signinRoutes...) }

// --- the sign-in page ------------------------------------------------------------

func (s *server) getAuthProviders(c *call) {
	// The same providers for every tenant; ?tenant={slug} names the tenant the
	// page is for (absent: the default tenant), and an unknown or disabled one
	// is not found. register and forgot are what the page links to; they come
	// with mail and registration, which are the next part of 3.3.
	tenant, ok := s.tenantOfRequest(c, c.r.URL.Query().Get("tenant"))
	if !ok {
		return
	}
	rules, err := s.auth.Registration(c.r.Context(), tenant)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	info := s.auth.PublicInfo()
	writeJSON(c.w, http.StatusOK, struct {
		Providers []auth.PublicProvider `json:"providers"`
		LoginMode string                `json:"loginMode"`
		Local     bool                  `json:"local"`
		SSO       bool                  `json:"sso"`
		Passkey   bool                  `json:"passkey"`
		Register  bool                  `json:"register"`
		Forgot    bool                  `json:"forgot"`
	}{info.Providers, info.Mode, info.Local, info.SSO, info.Passkey, rules.Open, s.auth.MailEnabled()})
}

func (s *server) getCaptcha(c *call) {
	q := c.r.URL.Query()
	ch, err := s.auth.CaptchaFor(c.r.Context(), q.Get("ctx"), q.Get("account"), s.auth.ClientAddr(c.r))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	out := map[string]any{"required": ch.Required}
	if ch.Required {
		out["provider"], out["siteKey"] = ch.Provider, ch.SiteKey
		if ch.Provider == auth.CaptchaImage {
			out["id"], out["image"] = ch.ID, ch.Image
		}
	}
	writeJSON(c.w, http.StatusOK, out)
}

// --- single sign-on ------------------------------------------------------------------

func validKind(k string) bool {
	return k == auth.KindOIDC || k == auth.KindSAML || k == auth.KindGitHub
}

func (s *server) ssoStartJSON(c *call, kind, slug string) {
	var req struct {
		Intent string `json:"intent"`
		Token  string `json:"token"`
		Return string `json:"return"`
	}
	if !s.readJSONMax(c.w, c.r, &req, true, authBodyLimit) {
		return
	}
	bp := auth.BeginParams{Intent: req.Intent, Token: req.Token, Return: req.Return}
	if c.p != nil {
		u := c.user()
		bp.User = &u
	}
	res, err := s.auth.SSOBegin(c.r.Context(), kind, slug, bp, s.auth.ClientAddr(c.r))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	http.SetCookie(c.w, res.Cookie)
	if res.Binding != nil {
		http.SetCookie(c.w, res.Binding)
	}
	writeJSON(c.w, http.StatusOK, struct {
		URL string `json:"url"`
	}{res.URL})
}

func (s *server) ssoStartPOST(c *call) {
	kind := c.r.PathValue("kind")
	if !validKind(kind) {
		writeError(c.w, http.StatusNotFound, "not_found", "no such endpoint")
		return
	}
	s.ssoStartJSON(c, kind, c.r.PathValue("slug"))
}

// ssoStartGET is the button of the sign-in page: a navigation that ends in a
// redirect to the provider, or back to the sign-in page with the reason.
func (s *server) ssoStartGET(c *call) {
	c.w.Header().Set("Cache-Control", "no-store")
	kind := c.r.PathValue("kind")
	if !validKind(kind) {
		writeError(c.w, http.StatusNotFound, "not_found", "no such endpoint")
		return
	}
	res, err := s.auth.SSOBegin(c.r.Context(), kind, c.r.PathValue("slug"), auth.BeginParams{Intent: "login", Return: c.r.URL.Query().Get("return")}, s.auth.ClientAddr(c.r))
	if err != nil {
		code := "internal"
		var ae *auth.Error
		if errors.As(err, &ae) {
			switch ae.Kind {
			case auth.RateLimited:
				code = "rate_limited"
			case auth.NotFound, auth.Unavailable:
				code = "provider_unavailable"
			}
		} else {
			s.log.Error("could not start a sign-in", "error", err)
		}
		http.Redirect(c.w, c.r, "/login?sso_error="+code, http.StatusSeeOther)
		return
	}
	http.SetCookie(c.w, res.Cookie)
	http.Redirect(c.w, c.r, res.URL, http.StatusSeeOther)
}

func (s *server) oidcSlugCallback(c *call) { s.oidcCallbackOf(c, c.r.PathValue("slug")) }

func (s *server) oidcCallbackOf(c *call, slug string) {
	c.w.Header().Set("Cache-Control", "no-store")
	binding, sessionTok := "", ""
	if ck, err := c.r.Cookie(s.auth.SSOCookieName()); err == nil {
		binding = ck.Value
	}
	if ck, err := c.r.Cookie(s.auth.SessionCookieName()); err == nil {
		sessionTok = ck.Value
	}
	out := s.auth.OIDCComplete(c.r.Context(), slug, binding, sessionTok, c.r.URL.Query(), s.auth.ClientAddr(c.r))
	s.finishOutcome(c, out)
}

func (s *server) finishOutcome(c *call, out auth.Outcome) {
	for _, ck := range out.Clear {
		http.SetCookie(c.w, ck)
	}
	if out.Session != "" {
		s.startSession(c, out.Session)
	}
	http.Redirect(c.w, c.r, out.Redirect, http.StatusSeeOther)
}

func (s *server) samlACS(c *call) {
	c.w.Header().Set("Cache-Control", "no-store")
	c.r.Body = http.MaxBytesReader(c.w, c.r.Body, 1<<20)
	sessionTok := ""
	if ck, err := c.r.Cookie(s.auth.SessionCookieName()); err == nil {
		sessionTok = ck.Value
	}
	out := s.auth.SAMLComplete(c.r.Context(), c.r.PathValue("slug"), c.r, sessionTok, s.auth.ClientAddr(c.r))
	s.finishOutcome(c, out)
}

func (s *server) samlMetadata(c *call) {
	xml, err := s.auth.SAMLMetadata(c.r.PathValue("slug"))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.Header().Set("Content-Type", "application/samlmetadata+xml")
	c.w.Header().Set("Cache-Control", "no-store")
	c.w.Write(xml)
}

// --- passkeys ----------------------------------------------------------------------------

func (s *server) passkeyBegin(c *call) {
	var req struct {
		Username string `json:"username"`
	}
	if !s.readJSONMax(c.w, c.r, &req, true, authBodyLimit) {
		return
	}
	id, as, err := s.auth.PasskeyBegin(c.r.Context(), req.Username, s.auth.ClientAddr(c.r))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Ceremony string `json:"ceremony"`
		Options  any    `json:"options"`
	}{id, as.Response})
}

func (s *server) passkeyFinish(c *call) {
	c.r.Body = http.MaxBytesReader(c.w, c.r.Body, authBodyLimit)
	u, tok, err := s.auth.PasskeyFinish(c.r.Context(), c.r.URL.Query().Get("ceremony"), s.auth.ClientAddr(c.r), c.r)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	s.startSession(c, tok)
	writeJSON(c.w, http.StatusOK, struct {
		User   userResp `json:"user"`
		Return string   `json:"return"`
	}{s.toUser(*u), auth.SafeReturn(c.r.URL.Query().Get("return"))})
}

func (s *server) listAccountPasskeys(c *call) {
	l, err := s.auth.Passkeys(c.r.Context(), c.user().ID)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Passkeys []passkeyResp `json:"passkeys"`
	}{toPasskeys(l)})
}

func (s *server) accountPasskeyBegin(c *call) {
	var req struct {
		Name string `json:"name"`
	}
	if !s.readJSONMax(c.w, c.r, &req, true, authBodyLimit) {
		return
	}
	id, cc, err := s.auth.PasskeyRegisterBegin(c.r.Context(), c.p, req.Name)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Ceremony string `json:"ceremony"`
		Options  any    `json:"options"`
	}{id, cc.Response})
}

func (s *server) accountPasskeyFinish(c *call) {
	c.r.Body = http.MaxBytesReader(c.w, c.r.Body, authBodyLimit)
	l, err := s.auth.PasskeyRegisterFinish(c.r.Context(), c.p, c.r.URL.Query().Get("ceremony"), s.auth.ClientAddr(c.r), c.r)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusCreated, struct {
		Passkeys []passkeyResp `json:"passkeys"`
	}{toPasskeys(l)})
}

func (s *server) renameAccountPasskey(c *call) {
	var req struct {
		Name string `json:"name"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	v, err := s.auth.RenamePasskey(c.r.Context(), c.user().ID, c.r.PathValue("id"), req.Name)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, toPasskeys([]auth.PasskeyView{*v})[0])
}

func (s *server) deleteAccountPasskey(c *call) {
	if err := s.auth.DeletePasskey(c.r.Context(), c.p, c.user().ID, c.r.PathValue("id"), s.auth.ClientAddr(c.r)); err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
}

func (s *server) listUserPasskeys(c *call) {
	l, err := s.auth.UserPasskeys(c.r.Context(), store.ScopeOf(c.user()), c.r.PathValue("id"))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Passkeys []passkeyResp `json:"passkeys"`
	}{toPasskeys(l)})
}

func (s *server) deleteUserPasskey(c *call) {
	if err := s.auth.DeletePasskey(c.r.Context(), c.p, c.r.PathValue("id"), c.r.PathValue("passkey"), s.auth.ClientAddr(c.r)); err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
}

// --- administration -------------------------------------------------------------------------

type captchaCtxResp struct {
	On       bool   `json:"on"`
	When     string `json:"when"`
	Failures int    `json:"failures"`
}

func (s *server) securityBody(v *auth.SecurityView) any {
	ctxs := map[string]captchaCtxResp{}
	for _, k := range auth.CaptchaContexts {
		c := v.Captcha.Contexts[k]
		ctxs[k] = captchaCtxResp{c.On, c.When, c.Failures}
	}
	type notice struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Slug    string `json:"slug,omitempty"`
		Field   string `json:"field,omitempty"`
	}
	notices := make([]notice, len(v.Notices))
	for i, n := range v.Notices {
		notices[i] = notice{n.Code, n.Message, n.Slug, n.Field}
	}
	roles := v.Passkeys.RequireRoles
	if roles == nil {
		roles = []string{}
	}
	return map[string]any{
		"login": map[string]any{"mode": v.Login.Mode, "effectiveMode": v.EffectiveMode, "ssoOnly": v.Login.SSOOnly, "ssoOnlyInEffect": v.SSOOnlyInEffect, "ssoAvailable": v.SSOAvailable},
		"passkeys": map[string]any{"enabled": v.Passkeys.Enabled, "available": v.PasskeysAvailable, "requireRoles": roles,
			"required": v.Required, "requiredWithout": v.RequiredWithout},
		"captcha": map[string]any{"provider": v.Captcha.Provider, "siteKey": v.Captcha.SiteKey, "hasSecret": v.CaptchaHasSecret, "contexts": ctxs},
		"lockout": map[string]any{"enabled": v.Lockout.Enabled, "minutes": v.Lockout.Minutes, "scope": v.Lockout.Scope},
		"limits": map[string]any{"perMinute": v.Limits.PerMinute, "maxFailures": v.Limits.MaxFailures, "windowMinutes": v.Limits.WindowMinutes,
			"sessionIdleDays": v.Limits.SessionIdleDays, "sessionMaxDays": v.Limits.SessionMaxDays},
		"passwordAdmins": v.PasswordAdmins,
		"notices":        notices,
	}
}

func (s *server) adminGetSecurity(c *call) {
	v, err := s.auth.SecurityView(c.r.Context())
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, s.securityBody(v))
}

func (s *server) adminPutSecurity(c *call) {
	var req auth.SecurityPatch
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	if _, err := s.auth.UpdateSecurity(c.r.Context(), c.user().ID, s.auth.ClientAddr(c.r), req); err != nil {
		s.fail(c.w, err)
		return
	}
	v, err := s.auth.SecurityView(c.r.Context())
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, s.securityBody(v))
}

func (s *server) adminListProviders(c *call) {
	l, err := s.auth.Providers(c.r.Context())
	if err != nil {
		s.fail(c.w, err)
		return
	}
	if l == nil {
		l = []auth.ProviderView{}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Providers []auth.ProviderView `json:"providers"`
	}{l})
}

func (s *server) adminGetProvider(c *call) {
	v, err := s.auth.Provider(c.r.Context(), c.r.PathValue("slug"))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, v)
}

func (s *server) adminCreateProvider(c *call) {
	var req auth.ProviderInput
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	v, err := s.auth.CreateProvider(c.r.Context(), c.user().ID, s.auth.ClientAddr(c.r), req)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.Header().Set("Location", "/api/admin/auth/providers/"+url.PathEscape(v.Slug))
	writeJSON(c.w, http.StatusCreated, v)
}

func (s *server) adminPatchProvider(c *call) {
	var req auth.ProviderInput
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	v, err := s.auth.UpdateProvider(c.r.Context(), c.user().ID, s.auth.ClientAddr(c.r), c.r.PathValue("slug"), req)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, v)
}

func (s *server) adminDeleteProvider(c *call) {
	force := false
	if q := c.r.URL.Query(); q.Has("force") {
		switch q.Get("force") {
		case "1":
			force = true
		case "0":
		default:
			writeError(c.w, http.StatusBadRequest, "invalid", "force is 1 or 0")
			return
		}
	}
	if err := s.auth.DeleteProvider(c.r.Context(), c.user().ID, s.auth.ClientAddr(c.r), c.r.PathValue("slug"), force); err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
}

func (s *server) adminRefreshProvider(c *call) {
	v, err := s.auth.RefreshProvider(c.r.Context(), c.r.PathValue("slug"))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, v)
}

func (s *server) adminAudit(c *call) {
	q := c.r.URL.Query()
	limit := 50
	if q.Has("limit") {
		n, err := strconv.Atoi(q.Get("limit"))
		if err != nil || n < 1 || n > maxAdminLimit {
			writeError(c.w, http.StatusBadRequest, "invalid", "limit is a number from 1 to "+strconv.Itoa(maxAdminLimit))
			return
		}
		limit = n
	}
	sc, ok := s.adminScope(c)
	if !ok {
		return
	}
	rows, next, err := s.auth.ListAudit(c.r.Context(), sc, !c.isSiteAdmin(), strings.TrimSpace(q.Get("kind")), strings.TrimSpace(q.Get("user")), q.Get("cursor"), limit)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	type item struct {
		ID       string `json:"id"`
		At       string `json:"at"`
		Kind     string `json:"kind"`
		Actor    any    `json:"actor"`
		User     any    `json:"user"`
		Username string `json:"username"`
		IP       string `json:"ip"`
		Detail   any    `json:"detail"`
	}
	nullable := func(v string) any {
		if v == "" {
			return nil
		}
		return v
	}
	items := make([]item, len(rows))
	for i, r := range rows {
		items[i] = item{r.ID, rfc3339(r.At), r.Kind, nullable(r.Actor), nullable(r.User), r.Username, r.IP, r.Detail}
	}
	var nx any
	if next != "" {
		nx = next
	}
	writeJSON(c.w, http.StatusOK, struct {
		Items []item `json:"items"`
		Next  any    `json:"next"`
	}{items, nx})
}
