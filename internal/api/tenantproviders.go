package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/authz"
)

// An organisation's own single sign-on (3.6 d3, docs/api.md "Sign-in: social
// providers and SSO"): its administrators add, change, test and remove its
// OpenID Connect and SAML providers (tenant.signin.manage, features.ownSso);
// a site administrator does the same for any tenant. The provider is the same
// object as the site's (/api/admin/providers), owned by the tenant: it signs
// in, links and makes accounts of that tenant only, and never a site
// administrator. Templates (social sign-in) and GitHub are the site's.

var tenantProviderRoutes = []route{
	{"GET", "/api/admin/tenants/{tenant}/providers", needs(authz.TenantSigninManage), true, false, false, (*server).tenantListProviders},
	{"POST", "/api/admin/tenants/{tenant}/providers", needs(authz.TenantSigninManage), true, false, false, (*server).tenantCreateProvider},
	{"GET", "/api/admin/tenants/{tenant}/providers/{id}", needs(authz.TenantSigninManage), true, false, false, (*server).tenantGetProvider},
	{"PATCH", "/api/admin/tenants/{tenant}/providers/{id}", needs(authz.TenantSigninManage), true, false, false, (*server).tenantPatchProvider},
	{"DELETE", "/api/admin/tenants/{tenant}/providers/{id}", needs(authz.TenantSigninManage), true, false, false, (*server).tenantDeleteProvider},
	{"POST", "/api/admin/tenants/{tenant}/providers/{id}/check", needs(authz.TenantSigninManage), true, false, false, (*server).tenantCheckProvider},
	{"POST", "/api/admin/tenants/{tenant}/providers/{id}/test", needs(authz.TenantSigninManage), true, true, false, (*server).tenantTestProvider},
	{"GET", "/api/admin/tenants/{tenant}/providers/{id}/test", needs(authz.TenantSigninManage), true, false, false, (*server).tenantTestResult},
}

func init() { routeTable = append(routeTable, tenantProviderRoutes...) }

// signinTenant is the {tenant} of a provider route: one the caller may manage
// the sign-in of, else not found.
func (s *server) signinTenant(c *call) (string, bool) {
	id := c.r.PathValue("tenant")
	if !c.p.Access.Can(authz.TenantSigninManage, id) || !s.knownTenant(id) {
		tenantNotFound(c)
		return "", false
	}
	return id, true
}

// tenantProvider is the {tenant} and the {id} of a provider route: a provider
// the tenant owns, else not found (another tenant's or the site's are none).
func (s *server) tenantProvider(c *call) (tenant, id string, ok bool) {
	tenant, ok = s.signinTenant(c)
	if !ok {
		return "", "", false
	}
	id = c.r.PathValue("id")
	if s.auth.ProviderOwner(id) != tenant {
		writeError(c.w, http.StatusNotFound, "not_found", "No such provider.")
		return "", "", false
	}
	return tenant, id, true
}

// tenantListProviders is GET /api/admin/tenants/{tenant}/providers.
func (s *server) tenantListProviders(c *call) {
	t, ok := s.signinTenant(c)
	if !ok {
		return
	}
	l, err := s.auth.ProvidersOf(c.r.Context(), t)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Providers []auth.ProviderView `json:"providers"`
		// Offered is the site's SSO providers offered to the tenant, read
		// only: what its domains may be routed to besides its own (3.6 d4).
		Offered []auth.OfferedProvider `json:"offered"`
	}{l, s.auth.OfferedSSO(c.r.Context(), t)})
}

func (s *server) tenantGetProvider(c *call) {
	_, id, ok := s.tenantProvider(c)
	if !ok {
		return
	}
	s.writeTenantProvider(c, http.StatusOK, id)
}

func (s *server) writeTenantProvider(c *call, status int, id string) {
	v, err := s.auth.Provider(c.r.Context(), id)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, status, v)
}

// tenantCreateProvider is POST /api/admin/tenants/{tenant}/providers: the
// body of POST /api/admin/providers for kind oidc or saml, owned by the tenant
// (owner may be left out or be the tenant), with no template.
func (s *server) tenantCreateProvider(c *call) {
	t, ok := s.signinTenant(c)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.w, c.r.Body, authBodyLimit))
	if err != nil {
		writeError(c.w, http.StatusBadRequest, "invalid", "The body could not be read.")
		return
	}
	var req auth.ProviderInput
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(c.w, http.StatusBadRequest, "invalid", "The body is not what this takes: "+err.Error()+".")
		return
	}
	if req.Template != nil {
		writeError(c.w, http.StatusBadRequest, "invalid", "Templates are the site's social sign-in: an organisation's single sign-on is OpenID Connect or SAML.")
		return
	}
	if req.Owner != nil && *req.Owner != t {
		writeError(c.w, http.StatusBadRequest, "invalid", "The owner is the organisation of the address.")
		return
	}
	req.Owner = &t
	v, err := s.auth.CreateProvider(c.r.Context(), c.user().ID, s.auth.ClientAddr(c.r), req)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.Header().Set("Location", "/api/admin/tenants/"+url.PathEscape(t)+"/providers/"+url.PathEscape(v.ID))
	s.writeTenantProvider(c, http.StatusCreated, v.ID)
}

// tenantPatchProvider is PATCH …/providers/{id}: auth's body, as for the site's.
func (s *server) tenantPatchProvider(c *call) {
	_, id, ok := s.tenantProvider(c)
	if !ok {
		return
	}
	var req auth.ProviderInput
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	if _, err := s.auth.UpdateProvider(c.r.Context(), c.user().ID, s.auth.ClientAddr(c.r), id, req); err != nil {
		s.fail(c.w, err)
		return
	}
	s.writeTenantProvider(c, http.StatusOK, id)
}

// tenantDeleteProvider is DELETE …/providers/{id}[?force=1]: as the site's
// sign-in provider (409 provider_in_use while it is somebody's only way in).
func (s *server) tenantDeleteProvider(c *call) {
	_, id, ok := s.tenantProvider(c)
	if !ok {
		return
	}
	force := false
	if q := c.r.URL.Query(); q.Has("force") {
		switch q.Get("force") {
		case "1":
			force = true
		case "0":
		default:
			writeError(c.w, http.StatusBadRequest, "invalid", "Force is 1 or 0.")
			return
		}
	}
	if err := s.auth.DeleteProvider(c.r.Context(), c.user().ID, s.auth.ClientAddr(c.r), id, force); err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
}

// tenantCheckProvider is POST …/providers/{id}/check: the discovery document
// or the metadata is fetched again.
func (s *server) tenantCheckProvider(c *call) {
	_, id, ok := s.tenantProvider(c)
	if !ok {
		return
	}
	if _, err := s.auth.RefreshProvider(c.r.Context(), id); err != nil {
		s.fail(c.w, err)
		return
	}
	s.writeTenantProvider(c, http.StatusOK, id)
}

// tenantTestProvider is POST …/providers/{id}/test {"return"}: a test
// sign-in, on or off; the browser comes back to return (a console address;
// the organisation console's sign-in tab without one).
func (s *server) tenantTestProvider(c *call) {
	t, id, ok := s.tenantProvider(c)
	if !ok {
		return
	}
	var req struct {
		Return string `json:"return"`
	}
	if !s.readJSONMax(c.w, c.r, &req, true, authBodyLimit) {
		return
	}
	u := c.user()
	res, err := s.auth.TenantTestBegin(c.r.Context(), t, id, &u, req.Return, s.auth.ClientAddr(c.r))
	if err != nil {
		var ae *auth.Error
		if errors.As(err, &ae) && ae.Kind == auth.NotFound {
			writeError(c.w, http.StatusNotFound, "not_found", "No such provider.")
			return
		}
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

// tenantTestResult is GET …/providers/{id}/test: what the caller's last test
// sign-in at the provider found, once.
func (s *server) tenantTestResult(c *call) {
	if _, _, ok := s.tenantProvider(c); !ok {
		return
	}
	s.adminTestResult(c)
}
