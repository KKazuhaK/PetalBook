package api

import (
	"net/http"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/authz"
)

// An organisation's own captcha provider (3.6, docs/api.md "Captcha:
// GET/PUT /api/admin/tenants/{tenant}/captcha"): its administrators
// (tenant.captcha.manage, with features.ownCaptcha on for the tenant) see, set,
// try and remove a provider of its own; a site administrator does the same for
// any tenant. The secret is write-only. Which provider a form uses is decided
// in internal/auth (captchaPlan), never here.

var tenantCaptchaRoutes = []route{
	{"GET", "/api/admin/tenants/{tenant}/captcha", needs(authz.TenantCaptchaManage), true, false, false, (*server).tenantGetCaptcha},
	{"PUT", "/api/admin/tenants/{tenant}/captcha", needs(authz.TenantCaptchaManage), true, false, false, (*server).tenantPutCaptcha},
	{"DELETE", "/api/admin/tenants/{tenant}/captcha", needs(authz.TenantCaptchaManage), true, false, false, (*server).tenantDeleteCaptcha},
	{"POST", "/api/admin/tenants/{tenant}/captcha/test", needs(authz.TenantCaptchaManage), true, false, false, (*server).tenantTestCaptcha},
}

func init() { routeTable = append(routeTable, tenantCaptchaRoutes...) }

// captchaTenant is the {tenant} of a captcha route: one the caller may manage
// the captcha of, else not found.
func (s *server) captchaTenant(c *call) (string, bool) {
	id := c.r.PathValue("tenant")
	if !c.p.Access.Can(authz.TenantCaptchaManage, id) || !s.knownTenant(id) {
		tenantNotFound(c)
		return "", false
	}
	return id, true
}

type tenantCaptchaResp struct {
	Enabled  bool   `json:"enabled"`
	Provider string `json:"provider"`
	// SiteKey is public: it goes into the page.
	SiteKey   string `json:"siteKey"`
	HasSecret bool   `json:"hasSecret"`
	// Ready says the organisation's forms use the provider now.
	Ready bool `json:"ready"`
}

func toTenantCaptcha(v *auth.TenantCaptchaView) tenantCaptchaResp {
	return tenantCaptchaResp{Enabled: v.Enabled, Provider: v.Provider, SiteKey: v.SiteKey, HasSecret: v.HasSecret, Ready: v.Ready}
}

func (s *server) tenantGetCaptcha(c *call) {
	t, ok := s.captchaTenant(c)
	if !ok {
		return
	}
	v, err := s.auth.GetTenantCaptcha(c.r.Context(), t)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.Header().Set("Cache-Control", "no-store")
	writeJSON(c.w, http.StatusOK, toTenantCaptcha(v))
}

func (s *server) tenantPutCaptcha(c *call) {
	t, ok := s.captchaTenant(c)
	if !ok {
		return
	}
	var req struct {
		Enabled  *bool   `json:"enabled"`
		Provider *string `json:"provider"`
		SiteKey  *string `json:"siteKey"`
		Secret   *string `json:"secret"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	v, err := s.auth.UpdateTenantCaptcha(c.r.Context(), c.user().ID, s.auth.ClientAddr(c.r), t,
		auth.TenantCaptchaPatch{Enabled: req.Enabled, Provider: req.Provider, SiteKey: req.SiteKey, Secret: req.Secret})
	if err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.Header().Set("Cache-Control", "no-store")
	writeJSON(c.w, http.StatusOK, toTenantCaptcha(v))
}

func (s *server) tenantDeleteCaptcha(c *call) {
	t, ok := s.captchaTenant(c)
	if !ok {
		return
	}
	if err := s.auth.DeleteTenantCaptcha(c.r.Context(), c.user().ID, s.auth.ClientAddr(c.r), t); err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
}

func (s *server) tenantTestCaptcha(c *call) {
	t, ok := s.captchaTenant(c)
	if !ok {
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	res, err := s.auth.TestTenantCaptcha(c.r.Context(), c.user().ID, s.auth.ClientAddr(c.r), t, req.Token)
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
