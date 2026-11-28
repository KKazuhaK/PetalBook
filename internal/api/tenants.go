package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"petalbook.app/server/internal/auditlog"
	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/page"
	"petalbook.app/server/internal/store"
)

// Tenants (milestone 3.3, docs/api.md "Tenants, registration and mail"): the
// switch that allows more than the default tenant, and the tenants themselves.

var tenantSlugRE = store.TenantSlugRE

type tenantRefResp struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type tenantResp struct {
	ID       string `json:"id"`
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	Default  bool   `json:"default"`
	Disabled bool   `json:"disabled"`
	// Registration is the tenant's registration mode: open, invite or closed.
	Registration struct {
		Mode string `json:"mode"`
	} `json:"registration"`
	Quota map[string]*int64 `json:"quota"`
	// Plan is the plan the tenant has, null for none.
	Plan *planRefResp `json:"plan"`
	// Origin is how the tenant came to be: default, admin or self; CreatedBy
	// the id of the person who made a self one ("" otherwise).
	Origin    string          `json:"origin"`
	CreatedBy string          `json:"createdBy"`
	Usage     tenantUsageResp `json:"usage"`
	Created   string          `json:"created"`
	Updated   string          `json:"updated"`
}

// quotaOf is the tenant's totals in force, from its quota.* settings: read only
// here, set through the tenant's settings.
func (s *server) quotaOf(ctx context.Context, tenant string) map[string]*int64 {
	t := s.quota.Totals(ctx, tenant)
	return map[string]*int64{"people": t.People, "spaces": t.Spaces, "bytes": t.Bytes, "pages": t.Pages}
}

// toTenant is the tenant as the API shows it, with the usage row u (one
// lookup of its own, or one query for a page: see usageOf) and plans the
// request's plan index (nil reads it).
func (s *server) toTenant(ctx context.Context, t store.Tenant, u store.Usage, plans planIndex) (r tenantResp, err error) {
	r = tenantResp{ID: t.ID, Slug: t.Slug, Name: t.Name, Default: t.IsDefault(), Disabled: t.Disabled, Quota: s.quotaOf(ctx, t.ID), Origin: t.Origin, CreatedBy: t.CreatedBy,
		Usage: toTenantUsage(u), Created: rfc3339(t.Created), Updated: rfc3339(t.Updated)}
	if r.Plan, err = s.planRef(ctx, t.ID, plans); err != nil {
		return r, err
	}
	r.Registration.Mode = s.auth.RegistrationMode(ctx, t.ID)
	return r, nil
}

// usageOf reads a tenant's usage row.
func (s *server) usageOf(ctx context.Context, id string) (store.Usage, error) {
	return store.GetUsage(ctx, s.opt.Store.Q(), store.UsageTenant, id)
}

// registrationReady says whether registration can work at all: it needs mail
// enabled and a public address.
func (s *server) registrationReady() bool { return s.auth != nil && s.auth.MailEnabled() }

// toTenantRef names a user's tenant.
func (s *server) toTenantRef(id string) *tenantRefResp {
	if r, ok := s.opt.Store.TenantRefOf(id); ok {
		return &tenantRefResp{r.ID, r.Slug, r.Name}
	}
	return nil
}

func tenantNotFound(c *call) { writeError(c.w, http.StatusNotFound, "not_found", "No such tenant.") }

// --- the tenants -----------------------------------------------------------------------

func (s *server) adminListTenants(c *call) {
	limit, ok := listLimit(c, defaultAdminLimit, maxAdminLimit)
	if !ok {
		return
	}
	q := c.r.URL.Query()
	switch q.Get("origin") {
	case "", store.OriginDefault, store.OriginAdmin, store.OriginSelf:
	default:
		writeError(c.w, http.StatusBadRequest, "invalid", "Origin is default, admin or self.")
		return
	}
	rows, next, err := store.ListTenants(c.r.Context(), s.opt.Store.Q(), s.opt.Store.Dialect(), strings.TrimSpace(q.Get("q")), q.Get("origin"), q.Get("cursor"), limit)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	ids := make([]string, len(rows))
	for i, t := range rows {
		ids[i] = t.ID
	}
	usage, err := store.UsageOfTenants(c.r.Context(), s.opt.Store.Q(), ids) // one query for the page
	if err != nil {
		s.fail(c.w, err)
		return
	}
	plans, err := s.planIndexOf(c.r.Context()) // one query for the page
	if err != nil {
		s.fail(c.w, err)
		return
	}
	items := make([]tenantResp, len(rows))
	for i, t := range rows {
		if items[i], err = s.toTenant(c.r.Context(), t, usage[t.ID], plans); err != nil {
			s.fail(c.w, err)
			return
		}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Items []tenantResp `json:"items"`
		Next  string       `json:"next"`
		// Count is how many tenants the server has, the default one included.
		Count int `json:"count"`
	}{items, next, s.opt.Store.TenantCount()})
}

// visibleTenant reads the {tenant} of the path for a caller who may see it: a
// site administrator any, a tenant administrator their own, anything else is
// answered as unknown.
func (s *server) visibleTenant(c *call) (*store.Tenant, bool) {
	id := c.r.PathValue("tenant")
	if !c.p.Access.Can(authz.TenantSettingsRead, id) {
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
	u, err := s.usageOf(c.r.Context(), t.ID)
	if err == nil {
		var r tenantResp
		if r, err = s.toTenant(c.r.Context(), *t, u, nil); err == nil {
			writeJSON(c.w, http.StatusOK, r)
			return
		}
	}
	s.fail(c.w, err)
}

func cleanTenantName(in string) (string, bool) { return store.CleanTenantName(in) }

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
	name, ok := cleanTenantName(req.Name)
	if !ok || !tenantSlugRE.MatchString(req.Slug) {
		writeError(c.w, http.StatusBadRequest, "invalid", "Slug is 2 to 32 characters of a-z, 0-9 and -, starting with a letter or digit; name is 1 to 80 characters.")
		return
	}
	if store.SlugReserved(req.Slug) {
		writeError(c.w, http.StatusConflict, "slug_reserved", "That organization address is reserved.")
		return
	}
	// The first administrator's details are checked as the invite checks them,
	// before the tenant is made, so a bad one makes nothing. Each may be "": the
	// person fills it in when they accept (a username they must).
	var fill auth.InviteFill
	var err error
	if req.Admin != nil {
		if fill, err = s.auth.CleanInviteFill(ctx, auth.InviteFill{Username: req.Admin.Username, Name: req.Admin.Name, Email: req.Admin.Email}); err != nil {
			s.fail(c.w, err)
			return
		}
	}
	now := time.Now().UTC()
	t := store.Tenant{ID: page.NewID(now), Slug: req.Slug, Name: name, Origin: store.OriginAdmin, CreatedBy: c.user().ID, Created: now, Updated: now}
	var stamped func()
	err = s.opt.Store.CreateTenantWith(ctx, t, func(q store.Q) (err error) {
		stamped, err = s.settings.StampNewTenant(ctx, q, t.ID, c.user().ID, auditlog.CapacityOf(ctx))
		return err
	})
	if err == nil {
		stamped()
	}
	switch {
	case errors.Is(err, store.ErrTenantLimit):
		writeError(c.w, http.StatusConflict, "tenant_limit", "A server has at most "+strconv.Itoa(store.MaxTenants)+" tenants.")
		return
	case store.IsUnique(err):
		if store.TenantSlugClash(err) {
			writeError(c.w, http.StatusConflict, "exists", "That slug is taken.")
			return
		}
		// A taken id: a new one is made on the next request.
		writeError(c.w, http.StatusConflict, "conflict", "Try again.")
		return
	case err != nil:
		s.fail(c.w, err)
		return
	}
	s.audit(c, auth.AuditEvent{Kind: "tenant_created", Actor: c.user().ID, Tenant: t.ID, Detail: map[string]any{"tenant": t.ID, "slug": t.Slug, "origin": store.OriginAdmin}})
	resp, err := s.toTenant(ctx, t, store.Usage{}, nil)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	out := struct {
		tenantResp
		Invite any `json:"invite,omitempty"`
	}{tenantResp: resp}
	if req.Admin != nil {
		// The first administrator's invite carries the account's name, username
		// and address, which the form that accepts it starts with. Nothing is mailed.
		inv, err := s.auth.CreateInvite(ctx, c.user(), c.p.Access, t.ID, "admin", nil, nil, fill)
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
			Username  string `json:"username"`
			Name      string `json:"name"`
			Email     string `json:"email"`
		}{inv.ID, inv.Token, inv.Role, rfc3339(inv.Created), rfc3339(inv.Expires), inv.TenantID, inv.SiteAdmin, inv.Username, inv.Name, inv.Email}
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
		case "quota":
			writeError(c.w, http.StatusBadRequest, "invalid", "The totals are settings: a site administrator sets them with PUT /api/admin/tenants/{tenant}/settings/quota.people (and quota.spaces, quota.bytes, quota.pages).")
			return
		case "slug", "disabled":
			if !c.can(authz.SiteTenantsManage) {
				writeError(c.w, http.StatusForbidden, "forbidden", "Only an administrator of the whole server may change "+k+".")
				return
			}
		default:
			writeError(c.w, http.StatusBadRequest, "invalid", "Unknown field "+k+".")
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
			writeError(c.w, http.StatusBadRequest, "invalid", "Name is 1 to 80 characters.")
			return
		}
		if n != t.Name {
			changed["name"], t.Name = n, n
		}
	}
	if raw, ok := req["slug"]; ok {
		var v string
		if json.Unmarshal(raw, &v) != nil || !tenantSlugRE.MatchString(v) {
			writeError(c.w, http.StatusBadRequest, "invalid", "Slug is 2 to 32 characters of a-z, 0-9 and -, starting with a letter or digit.")
			return
		}
		if t.IsDefault() && v != store.DefaultTenantSlug {
			writeError(c.w, http.StatusConflict, "default_tenant", "The default tenant's slug is default.")
			return
		}
		if v != t.Slug {
			changed["slug"], t.Slug = v, v
		}
	}
	if raw, ok := req["disabled"]; ok {
		var v bool
		if json.Unmarshal(raw, &v) != nil {
			writeError(c.w, http.StatusBadRequest, "invalid", "Disabled is true or false.")
			return
		}
		if v && t.IsDefault() {
			writeError(c.w, http.StatusConflict, "default_tenant", "The default tenant cannot be disabled.")
			return
		}
		if v && t.ID == c.tenant() {
			writeError(c.w, http.StatusConflict, "conflict", "You cannot disable your own tenant.")
			return
		}
		if v && !t.Disabled {
			// Its site administrators stop counting: some other one must stay.
			if n, err := store.CountEnabledAdminsOutside(c.r.Context(), s.opt.Store.Q(), t.ID); err != nil {
				s.fail(c.w, err)
				return
			} else if n < 1 {
				writeError(c.w, http.StatusConflict, "conflict", "There must be at least one enabled administrator of the server outside this tenant.")
				return
			}
		}
		if v != t.Disabled {
			changed["disabled"], t.Disabled = v, v
		}
	}
	if len(changed) > 0 {
		t.Updated = time.Now().UTC()
		if err := s.opt.Store.UpdateTenant(c.r.Context(), t); err != nil {
			if on, ok := store.UniqueOn(err); ok && strings.Contains(on, "slug") {
				writeError(c.w, http.StatusConflict, "exists", "That slug is taken.")
				return
			}
			s.fail(c.w, err)
			return
		}
		changed["tenant"], changed["slug"] = t.ID, t.Slug
		s.audit(c, auth.AuditEvent{Kind: "tenant_changed", Actor: c.user().ID, Tenant: t.ID, Detail: changed})
	}
	// Whenever it is asked for, changed or not: a repeat finishes what a failure left undone.
	if _, ok := req["disabled"]; ok && s.auth != nil {
		if err := s.auth.TenantAccessChanged(c.r.Context(), t.ID, t.Disabled); err != nil {
			s.fail(c.w, err)
			return
		}
	}
	u, err := s.usageOf(c.r.Context(), t.ID)
	if err == nil {
		var r tenantResp
		if r, err = s.toTenant(c.r.Context(), t, u, nil); err == nil {
			writeJSON(c.w, http.StatusOK, r)
			return
		}
	}
	s.fail(c.w, err)
}

func (s *server) adminDeleteTenant(c *call) {
	t, ok := s.visibleTenant(c)
	if !ok {
		return
	}
	if t.IsDefault() {
		writeError(c.w, http.StatusConflict, "default_tenant", "The default tenant cannot be deleted.")
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
			Recycle int    `json:"recycle"`
		}{"tenant_not_empty", "the tenant still has users, spaces, groups, invites or items in the recycle bin; they cannot be moved to another tenant", ne.Users, ne.Spaces, ne.Groups, ne.Invites, ne.Recycle})
		return
	}
	if errors.Is(err, store.ErrTenantTargetInUse) {
		writeError(c.w, http.StatusConflict, "conflict", "A remote still uses one of the tenant's storage targets; move or disconnect it first.")
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
	s.settings.Forget(t.ID)
	s.auth.TenantDeleted(c.r.Context())
	// Its domains went with it (ON DELETE CASCADE).
	s.domains.Changed(c.r.Context())
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
