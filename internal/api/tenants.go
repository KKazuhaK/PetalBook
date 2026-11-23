package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/KKazuhaK/PetalBook/internal/auth"
	"github.com/KKazuhaK/PetalBook/internal/page"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// Tenants (milestone 3.3, docs/api.md "Tenants, registration and mail"): the
// switch that allows more than the default tenant, and the tenants themselves.

const tenancyKey = "tenancy"

var tenantSlugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,31}$`)

type tenantRefResp struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type tenantResp struct {
	ID           string `json:"id"`
	Slug         string `json:"slug"`
	Name         string `json:"name"`
	Default      bool   `json:"default"`
	Disabled     bool   `json:"disabled"`
	Registration struct {
		Open bool `json:"open"`
	} `json:"registration"`
	Quota   map[string]*int64 `json:"quota"`
	Created string            `json:"created"`
	Updated string            `json:"updated"`
}

var quotaKeys = []string{"people", "spaces", "bytes", "pages"}

func parseQuota(raw string) map[string]*int64 {
	out := map[string]*int64{}
	for _, k := range quotaKeys {
		out[k] = nil
	}
	var m map[string]*int64
	if json.Unmarshal([]byte(raw), &m) == nil {
		for _, k := range quotaKeys {
			out[k] = m[k]
		}
	}
	return out
}

func (s *server) toTenant(ctx context.Context, t store.Tenant) (tenantResp, error) {
	r := tenantResp{ID: t.ID, Slug: t.Slug, Name: t.Name, Default: t.IsDefault(), Disabled: t.Disabled, Quota: parseQuota(t.Quota),
		Created: rfc3339(t.Created), Updated: rfc3339(t.Updated)}
	e, err := s.settings.Get(ctx, t.ID, "registration.open")
	if err != nil {
		return r, err
	}
	r.Registration.Open, _ = e.Value.(bool) // mail is not there yet (see registrationReady): never open
	r.Registration.Open = r.Registration.Open && s.registrationReady()
	return r, nil
}

// registrationReady says whether registration can work at all: it needs mail
// (the next part of 3.3) and a public address. Until mail exists it is never.
func (s *server) registrationReady() bool { return false }

// toTenantRef names a user's tenant.
func (s *server) toTenantRef(id string) *tenantRefResp {
	if r, ok := s.opt.Store.TenantRefOf(id); ok {
		return &tenantRefResp{r.ID, r.Slug, r.Name}
	}
	return nil
}

func tenantNotFound(c *call) { writeError(c.w, http.StatusNotFound, "not_found", "no such tenant") }

// --- the switch ------------------------------------------------------------------------

func (s *server) tenancyEnabled(ctx context.Context) (bool, error) {
	raw, ok, err := store.GetSiteSetting(ctx, s.opt.Store.Q(), tenancyKey)
	if err != nil || !ok {
		return false, err
	}
	var v struct {
		Enabled bool `json:"enabled"`
	}
	if json.Unmarshal([]byte(raw), &v) != nil {
		return false, nil
	}
	return v.Enabled, nil
}

type tenancyResp struct {
	Enabled bool `json:"enabled"`
	Tenants int  `json:"tenants"`
}

func (s *server) adminGetTenancy(c *call) {
	on, err := s.tenancyEnabled(c.r.Context())
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, tenancyResp{on, s.opt.Store.TenantCount()})
}

func (s *server) adminPutTenancy(c *call) {
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	if req.Enabled == nil {
		writeError(c.w, http.StatusBadRequest, "invalid", "give enabled: true or false")
		return
	}
	ctx := c.r.Context()
	was, err := s.tenancyEnabled(ctx)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	b, _ := json.Marshal(struct {
		Enabled bool `json:"enabled"`
	}{*req.Enabled})
	if err := store.SetSiteSetting(ctx, s.opt.Store.Q(), tenancyKey, string(b), time.Now()); err != nil {
		s.fail(c.w, err)
		return
	}
	if was != *req.Enabled {
		s.audit(c, auth.AuditEvent{Kind: "setting_changed", Actor: c.user().ID, Tenant: "-", Detail: map[string]any{"key": "tenancy.enabled", "layer": "site", "from": was, "to": *req.Enabled}})
	}
	s.adminGetTenancy(c)
}

// --- the tenants -----------------------------------------------------------------------

func (s *server) adminListTenants(c *call) {
	limit, ok := listLimit(c, defaultAdminLimit, maxAdminLimit)
	if !ok {
		return
	}
	q := c.r.URL.Query()
	rows, next, err := store.ListTenants(c.r.Context(), s.opt.Store.Q(), s.opt.Store.Dialect(), strings.TrimSpace(q.Get("q")), q.Get("cursor"), limit)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	items := make([]tenantResp, len(rows))
	for i, t := range rows {
		if items[i], err = s.toTenant(c.r.Context(), t); err != nil {
			s.fail(c.w, err)
			return
		}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Items []tenantResp `json:"items"`
		Next  string       `json:"next"`
	}{items, next})
}

// visibleTenant reads the {tenant} of the path for a caller who may see it: a
// site administrator any, a tenant administrator their own, anything else is
// answered as unknown.
func (s *server) visibleTenant(c *call) (*store.Tenant, bool) {
	id := c.r.PathValue("tenant")
	if !c.isSiteAdmin() && id != c.user().TenantID {
		tenantNotFound(c)
		return nil, false
	}
	t, err := store.GetTenant(c.r.Context(), s.opt.Store.Q(), id)
	if errors.Is(err, store.ErrNotFound) {
		tenantNotFound(c)
		return nil, false
	}
	if err != nil {
		s.fail(c.w, err)
		return nil, false
	}
	return t, true
}

func (s *server) adminGetTenant(c *call) {
	t, ok := s.visibleTenant(c)
	if !ok {
		return
	}
	r, err := s.toTenant(c.r.Context(), *t)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, r)
}

func cleanTenantName(in string) (string, bool) {
	n := strings.TrimSpace(in)
	if n == "" || utf8.RuneCountInString(n) > 80 || strings.ContainsFunc(n, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", false
	}
	return n, true
}

func (s *server) adminCreateTenant(c *call) {
	var req struct {
		Slug  string `json:"slug"`
		Name  string `json:"name"`
		Admin *struct {
			Username string `json:"username"`
			Name     string `json:"name"`
			Email    string `json:"email"`
		} `json:"admin"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	ctx := c.r.Context()
	on, err := s.tenancyEnabled(ctx)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	if !on {
		writeError(c.w, http.StatusConflict, "tenants_off", "more than the default tenant is switched off; switch it on at /api/admin/tenancy")
		return
	}
	name, ok := cleanTenantName(req.Name)
	if !ok || !tenantSlugRE.MatchString(req.Slug) {
		writeError(c.w, http.StatusBadRequest, "invalid", "slug is 2 to 32 characters of a-z, 0-9 and -, starting with a letter or digit; name is 1 to 80 characters")
		return
	}
	if req.Admin != nil {
		if _, err := auth.NormalizeUsername(req.Admin.Username); err != nil {
			s.fail(c.w, err)
			return
		}
		if n := strings.TrimSpace(req.Admin.Name); n != "" && utf8.RuneCountInString(n) > 80 {
			writeError(c.w, http.StatusBadRequest, "invalid", "admin.name is at most 80 characters")
			return
		}
		if e := strings.TrimSpace(req.Admin.Email); e != "" && (len(e) > 254 || !strings.Contains(e, "@")) {
			writeError(c.w, http.StatusBadRequest, "invalid", "admin.email is not an address")
			return
		}
	}
	now := time.Now().UTC()
	t := store.Tenant{ID: page.NewID(now), Slug: req.Slug, Name: name, Quota: "{}", Created: now, Updated: now}
	err = s.opt.Store.CreateTenant(ctx, t)
	switch {
	case errors.Is(err, store.ErrTenantLimit):
		writeError(c.w, http.StatusConflict, "tenant_limit", "a server has at most "+strconv.Itoa(store.MaxTenants)+" tenants")
		return
	case store.IsUnique(err):
		if on, _ := store.UniqueOn(err); strings.Contains(on, "slug") {
			writeError(c.w, http.StatusConflict, "exists", "that slug is taken")
			return
		}
		// A taken id: a new one is made on the next request.
		writeError(c.w, http.StatusConflict, "conflict", "try again")
		return
	case err != nil:
		s.fail(c.w, err)
		return
	}
	s.audit(c, auth.AuditEvent{Kind: "tenant_created", Actor: c.user().ID, Tenant: t.ID, Detail: map[string]any{"tenant": t.ID, "slug": t.Slug}})
	resp, err := s.toTenant(ctx, t)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	out := struct {
		tenantResp
		Invite any `json:"invite,omitempty"`
	}{tenantResp: resp}
	if req.Admin != nil {
		inv, err := s.auth.CreateInvite(ctx, c.user(), t.ID, "admin", nil, nil)
		if err != nil {
			s.fail(c.w, err)
			return
		}
		out.Invite = struct {
			ID        string `json:"id"`
			Token     string `json:"token"`
			Role      string `json:"role"`
			Created   string `json:"created"`
			Expires   string `json:"expires"`
			Tenant    string `json:"tenant"`
			SiteAdmin bool   `json:"siteAdmin"`
		}{inv.ID, inv.Token, inv.Role, rfc3339(inv.Created), rfc3339(inv.Expires), inv.TenantID, inv.SiteAdmin}
	}
	c.w.Header().Set("Location", "/api/admin/tenants/"+t.ID)
	writeJSON(c.w, http.StatusCreated, out)
}

func (s *server) adminPatchTenant(c *call) {
	var req map[string]json.RawMessage
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	cur, ok := s.visibleTenant(c)
	if !ok {
		return
	}
	for k := range req {
		switch k {
		case "name":
		case "slug", "disabled", "quota":
			if !c.isSiteAdmin() {
				writeError(c.w, http.StatusForbidden, "forbidden", "only an administrator of the whole server may change "+k)
				return
			}
		default:
			writeError(c.w, http.StatusBadRequest, "invalid", "unknown field "+k)
			return
		}
	}
	t := *cur
	changed := map[string]any{}
	if raw, ok := req["name"]; ok {
		var v string
		n, ok := "", json.Unmarshal(raw, &v) == nil
		if ok {
			n, ok = cleanTenantName(v)
		}
		if !ok {
			writeError(c.w, http.StatusBadRequest, "invalid", "name is 1 to 80 characters")
			return
		}
		if n != t.Name {
			changed["name"], t.Name = n, n
		}
	}
	if raw, ok := req["slug"]; ok {
		var v string
		if json.Unmarshal(raw, &v) != nil || !tenantSlugRE.MatchString(v) {
			writeError(c.w, http.StatusBadRequest, "invalid", "slug is 2 to 32 characters of a-z, 0-9 and -, starting with a letter or digit")
			return
		}
		if t.IsDefault() && v != store.DefaultTenantSlug {
			writeError(c.w, http.StatusConflict, "default_tenant", "the default tenant's slug is default")
			return
		}
		if v != t.Slug {
			changed["slug"], t.Slug = v, v
		}
	}
	if raw, ok := req["disabled"]; ok {
		var v bool
		if json.Unmarshal(raw, &v) != nil {
			writeError(c.w, http.StatusBadRequest, "invalid", "disabled is true or false")
			return
		}
		if v && t.IsDefault() {
			writeError(c.w, http.StatusConflict, "default_tenant", "the default tenant cannot be disabled")
			return
		}
		if v && t.ID == c.user().TenantID {
			writeError(c.w, http.StatusConflict, "conflict", "you cannot disable your own tenant")
			return
		}
		if v != t.Disabled {
			changed["disabled"], t.Disabled = v, v
		}
	}
	if raw, ok := req["quota"]; ok {
		var in map[string]json.RawMessage
		if json.Unmarshal(raw, &in) != nil {
			writeError(c.w, http.StatusBadRequest, "invalid", "quota is an object")
			return
		}
		q := parseQuota(t.Quota)
		for k, v := range in {
			if k != "people" && k != "spaces" && k != "bytes" && k != "pages" {
				writeError(c.w, http.StatusBadRequest, "invalid", "quota has people, spaces, bytes and pages")
				return
			}
			var n *int64
			if json.Unmarshal(v, &n) != nil || n != nil && (*n < 0 || *n > 1<<50) {
				writeError(c.w, http.StatusBadRequest, "invalid", "quota."+k+" is a number from 0, or null for no limit")
				return
			}
			q[k] = n
		}
		b, _ := json.Marshal(q)
		if string(b) != t.Quota {
			changed["quota"], t.Quota = q, string(b)
		}
	}
	if len(changed) > 0 {
		t.Updated = time.Now().UTC()
		if err := s.opt.Store.UpdateTenant(c.r.Context(), t); err != nil {
			if on, ok := store.UniqueOn(err); ok && strings.Contains(on, "slug") {
				writeError(c.w, http.StatusConflict, "exists", "that slug is taken")
				return
			}
			s.fail(c.w, err)
			return
		}
		changed["tenant"], changed["slug"] = t.ID, t.Slug
		s.audit(c, auth.AuditEvent{Kind: "tenant_changed", Actor: c.user().ID, Tenant: t.ID, Detail: changed})
	}
	r, err := s.toTenant(c.r.Context(), t)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, r)
}

func (s *server) adminDeleteTenant(c *call) {
	t, ok := s.visibleTenant(c)
	if !ok {
		return
	}
	if t.IsDefault() {
		writeError(c.w, http.StatusConflict, "default_tenant", "the default tenant cannot be deleted")
		return
	}
	err := s.opt.Store.DeleteTenant(c.r.Context(), t.ID)
	var ne *store.ErrTenantNotEmpty
	if errors.As(err, &ne) {
		writeJSON(c.w, http.StatusConflict, struct {
			Error   string `json:"error"`
			Message string `json:"message"`
			Users   int    `json:"users"`
			Spaces  int    `json:"spaces"`
			Groups  int    `json:"groups"`
			Invites int    `json:"invites"`
		}{"tenant_not_empty", "the tenant still has users, spaces, groups or invites; 3.3 cannot move them to another tenant", ne.Users, ne.Spaces, ne.Groups, ne.Invites})
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		tenantNotFound(c)
		return
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	s.audit(c, auth.AuditEvent{Kind: "tenant_deleted", Actor: c.user().ID, Tenant: t.ID, Detail: map[string]any{"tenant": t.ID, "slug": t.Slug}})
	c.w.WriteHeader(http.StatusNoContent)
}

// publicTenant is the tenant a public endpoint names by slug: an unknown or
// disabled one is not found. In memory, no query.
func (s *server) publicTenant(c *call, slug string) (store.TenantRef, bool) {
	st := s.opt.Store
	id, ok := st.TenantIDBySlug(slug)
	if !ok || st.TenantDisabled(id) {
		tenantNotFound(c)
		return store.TenantRef{}, false
	}
	r, _ := st.TenantRefOf(id)
	return r, true
}
