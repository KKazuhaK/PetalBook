package api

import (
	"errors"
	"html"
	"io"
	"net/http"
	"net/url"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/authz"
)

const samlACSPattern = "/api/auth/saml/{slug}/acs"

// oidcFormPostPattern is where a provider that answers with response_mode
// form_post (Apple) posts the code.
const oidcFormPostPattern = "/api/auth/oidc/{slug}/callback"

// formRoute says whether a route ("METHOD pattern") takes a form an identity
// provider posts, not JSON.
func formRoute(route string) bool {
	return route == "POST "+samlACSPattern || route == "POST "+oidcFormPostPattern
}

// heldAllowed are the routes a held session may still use, by what it waits
// for (docs/api.md, "When a passkey is required", and the temporary password
// of POST /api/users/{id}/password); everything else is refused. The passkey
// routes are a passkey hold's only: registering one lifts that hold, and must
// not lift a hold for a new password (the administrator would still know the
// password in use).
func heldAllowed(holdBy, route string) bool {
	if heldAlways[route] {
		return true
	}
	if holdBy == auth.HoldPassword {
		return route == "POST /api/account/password"
	}
	return heldForPasskey[route]
}

var heldAlways = map[string]bool{
	"GET /api/session":        true,
	"GET /api/meta":           true,
	"GET /api/auth/providers": true,
	"GET /api/account":        true,
	"POST /api/logout":        true,
}

var heldForPasskey = map[string]bool{
	"GET /api/account/passkeys":         true,
	"POST /api/account/passkeys/begin":  true,
	"POST /api/account/passkeys/finish": true,
	"PATCH /api/account/passkeys/{id}":  true,
	"DELETE /api/account/passkeys/{id}": true,
}

// signinRoutes are the routes of milestone 2.8, joined to the table at start-up.
var signinRoutes = []route{
	{"GET", "/api/auth/providers", public.always(), true, false, false, (*server).getAuthProviders},
	{"POST", "/api/auth/discover", public.always(), true, false, false, (*server).discoverSSO},
	{"GET", "/api/captcha", public.always(), true, false, false, (*server).getCaptcha},

	{"GET", "/api/auth/{kind}/{slug}/start", public.always(), true, false, false, (*server).ssoStartGET},
	{"POST", "/api/auth/{kind}/{slug}/start", public.always(), true, true, false, (*server).ssoStartPOST},
	{"GET", "/api/auth/oidc/{slug}/callback", public.always(), true, false, false, (*server).oidcSlugCallback},
	{"POST", oidcFormPostPattern, public.always(), true, false, false, (*server).oidcFormPost},
	{"GET", "/api/auth/github/{slug}/callback", public.always(), true, false, false, (*server).oidcSlugCallback},
	{"POST", samlACSPattern, public.always(), true, false, false, (*server).samlACS},
	{"GET", "/api/auth/saml/{slug}/metadata", public.always(), true, false, false, (*server).samlMetadata},

	{"POST", "/api/auth/passkey/begin", public.always(), true, false, false, (*server).passkeyBegin},
	{"POST", "/api/auth/passkey/finish", public.always(), true, false, false, (*server).passkeyFinish},

	{"GET", "/api/account/passkeys", signedIn.always(), true, false, false, (*server).listAccountPasskeys},
	{"POST", "/api/account/passkeys/begin", signedIn.always(), true, false, false, (*server).accountPasskeyBegin},
	{"POST", "/api/account/passkeys/finish", signedIn.always(), true, false, false, (*server).accountPasskeyFinish},
	{"PATCH", "/api/account/passkeys/{id}", signedIn.always(), true, false, false, (*server).renameAccountPasskey},
	{"DELETE", "/api/account/passkeys/{id}", signedIn.always(), true, false, false, (*server).deleteAccountPasskey},

	{"GET", "/api/users/{id}/passkeys", needs(authz.TenantUsersRead).always(), true, false, false, (*server).listUserPasskeys},
	{"DELETE", "/api/users/{id}/passkeys/{passkey}", needs(authz.TenantUsersManage).always(), true, false, false, (*server).deleteUserPasskey},

	{"GET", "/api/admin/security", needs(authz.SiteSecurityManage).always(), true, false, false, (*server).adminGetSecurity},
	{"PUT", "/api/admin/security", needs(authz.SiteSecurityManage).always(), true, false, false, (*server).adminPutSecurity},
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
	// Without ?tenant the page is everyone's: the site's enabled providers.
	// With it, what that tenant's people may use (3.6 d3): the social ones it
	// allows, the site's SSO offered to it, and its own.
	info := s.auth.PublicInfo(c.r.Context())
	if c.r.URL.Query().Has("tenant") {
		info = s.auth.PublicInfoFor(c.r.Context(), tenant)
	}
	writeJSON(c.w, http.StatusOK, struct {
		Providers []auth.PublicProvider `json:"providers"`
		LoginMode string                `json:"loginMode"`
		Local     bool                  `json:"local"`
		SSO       bool                  `json:"sso"`
		Passkey   bool                  `json:"passkey"`
		Register  bool                  `json:"register"`
		Forgot    bool                  `json:"forgot"`
		// OrgCreation: a person may register an organisation of their own.
		OrgCreation bool `json:"orgCreation"`
		// IdentifierFirst: the page asks for the address (or username) first,
		// and POST /api/auth/discover says where it signs in; SSOEntry: the
		// "Sign in with SSO" entry (/login/sso) is offered (3.6 d4).
		IdentifierFirst bool `json:"identifierFirst"`
		SSOEntry        bool `json:"ssoEntry"`
	}{info.Providers, info.Mode, info.Local, info.SSO, info.Passkey, rules.Open || s.auth.DomainJoinOpen(c.r.Context()), s.auth.MailEnabled(), s.auth.OrgCreation(c.r.Context()).Open,
		info.IdentifierFirst, info.SSOEntry})
}

// discoverSSO is POST /api/auth/discover {"email"}: where the domain of the
// address routes its sign-in, or null. It never looks at an account, so the
// answer is the same whether one exists; something without an @ (a
// username) is null.
func (s *server) discoverSSO(c *call) {
	var req struct {
		Email string `json:"email"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	d, err := s.auth.Discover(c.r.Context(), req.Email, s.auth.ClientAddr(c.r))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		SSO *auth.Discovered `json:"sso"`
	}{d})
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
	// Only an administrator's own route starts a test (TestBegin).
	if req.Intent == "test" {
		writeError(c.w, http.StatusBadRequest, "invalid", "Unknown intent.")
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
		writeError(c.w, http.StatusNotFound, "not_found", "No such endpoint.")
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
		writeError(c.w, http.StatusNotFound, "not_found", "No such endpoint.")
		return
	}
	res, err := s.auth.SSOBegin(c.r.Context(), kind, c.r.PathValue("slug"), auth.BeginParams{Intent: "login", Return: c.r.URL.Query().Get("return"), LoginHint: c.r.URL.Query().Get("login_hint")}, s.auth.ClientAddr(c.r))
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

func (s *server) oidcSlugCallback(c *call) {
	slug := c.r.PathValue("slug")
	c.w.Header().Set("Cache-Control", "no-store")
	binding, sessionTok := "", ""
	if ck, err := c.r.Cookie(s.auth.SSOCookieName()); err == nil {
		binding = ck.Value
	}
	if ck, err := c.r.Cookie(s.auth.SessionCookieName()); err == nil {
		sessionTok = ck.Value
	}
	out := s.auth.OIDCComplete(auth.WithAcceptLanguage(c.r.Context(), c.r.Header.Get("Accept-Language")), slug, binding, sessionTok, c.r.URL.Query(), s.auth.ClientAddr(c.r))
	s.finishOutcome(c, out)
}

// oidcFormPost takes the answer a provider posts (response_mode=form_post:
// Apple's) and sends the browser on to the callback with it, by a page of this
// site. The post comes from the provider's site, so the browser sends it no
// SameSite=Lax cookie: neither the attempt's binding nor the session (for a
// link). The page's own navigation to the callback is same-site, so the
// callback gets both and checks the attempt exactly as for a redirect. Only a
// provider that asked for form_post is answered so.
func (s *server) oidcFormPost(c *call) {
	c.w.Header().Set("Cache-Control", "no-store")
	slug := c.r.PathValue("slug")
	c.r.Body = http.MaxBytesReader(c.w, c.r.Body, 64<<10)
	if err := c.r.ParseForm(); err != nil || !s.auth.TakesFormPost(slug) {
		http.Redirect(c.w, c.r, "/login?sso_error=provider_unavailable", http.StatusSeeOther)
		return
	}
	// The name Apple posts (user) stays here, with the attempt: the address
	// the browser goes on to (its history, the logs) carries only the
	// protocol's values.
	if vs := c.r.PostForm["state"]; len(vs) == 1 {
		if us := c.r.PostForm["user"]; len(us) == 1 {
			s.auth.KeepFormPost(slug, vs[0], us[0])
		}
	}
	q := url.Values{}
	for _, k := range []string{"code", "state", "error"} {
		if v, ok := c.r.PostForm[k]; ok {
			q[k] = v
		}
	}
	target := "/api/auth/oidc/" + url.PathEscape(slug) + "/callback?" + q.Encode()
	h := c.w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	esc := html.EscapeString(target)
	c.w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(c.w, `<!doctype html><meta charset="utf-8"><meta name="referrer" content="no-referrer"><meta http-equiv="refresh" content="0;url=`+esc+`"><title>Signing in</title><a href="`+esc+`">Continue</a>`)
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
	out := s.auth.SAMLComplete(auth.WithAcceptLanguage(c.r.Context(), c.r.Header.Get("Accept-Language")), c.r.PathValue("slug"), c.r, sessionTok, s.auth.ClientAddr(c.r))
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
	res, err := s.auth.PasskeyFinish(c.r.Context(), c.r.URL.Query().Get("ceremony"), s.auth.ClientAddr(c.r), c.r)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	s.startSession(c, res.Token)
	user, hold, ok := s.signedIn(c, *res.User, res.Held, res.HeldBy)
	if !ok {
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		signInAnswer
		Return string `json:"return"`
	}{signInAnswer{user, hold}, auth.SafeReturn(c.r.URL.Query().Get("return"))})
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
	l, err := s.auth.UserPasskeys(c.r.Context(), c.reach(), c.r.PathValue("id"))
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

// securityBody is what GET /api/admin/security shows: what the sign-in policy
// means now (the policy itself is under /api/admin/settings) and the captcha's
// keys, the secret only as set or not.
func (s *server) securityBody(v *auth.SecurityView) any {
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
	return map[string]any{
		"login":          map[string]any{"effectiveMode": v.EffectiveMode, "ssoOnlyInEffect": v.SSOOnlyInEffect, "ssoAvailable": v.SSOAvailable},
		"passkeys":       map[string]any{"available": v.PasskeysAvailable, "required": v.Required, "requiredWithout": v.RequiredWithout},
		"captcha":        map[string]any{"siteKey": v.CaptchaSiteKey, "hasSecret": v.CaptchaHasSecret},
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

// adminPutSecurity sets the captcha provider's keys, the part of the sign-in
// policy that is not a setting. The secret is write-only.
func (s *server) adminPutSecurity(c *call) {
	var req struct {
		Captcha *struct {
			SiteKey *string `json:"siteKey"`
			Secret  *string `json:"secret"`
		} `json:"captcha"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	if req.Captcha == nil {
		writeError(c.w, http.StatusBadRequest, "invalid", "Give captcha: {siteKey, secret}. The rest of the sign-in policy is under /api/admin/settings.")
		return
	}
	if err := s.auth.UpdateCaptchaKeys(c.r.Context(), c.user().ID, s.auth.ClientAddr(c.r), req.Captcha.SiteKey, req.Captcha.Secret); err != nil {
		s.fail(c.w, err)
		return
	}
	s.adminGetSecurity(c)
}
