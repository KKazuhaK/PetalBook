package api

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"petalbook.app/server/internal/auditlog"
	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/store"
)

// The acting scope (docs/design/3-6-c.md, section 5). Every request acts in
// one tenant, decided here and nowhere else: the person's own, or, for an
// administration route, the one the header PetalBook-Scope: tenant:<id>
// names. With the header a site administrator acts exactly as that tenant's
// administrator: their subject is cut to the tenant keys they hold there
// (authz.Subject.NarrowTo), so site keys, personal spaces, other tenants and
// accounts that hold site keys are out of reach and the tenant's features
// bind them. The header only ever narrows. Handlers ask c.tenant(), c.reach(),
// c.adminReach() and c.ownScope(); TestNoHandlerReadsThePersonsTenantDirectly
// keeps them from reading the person's tenant themselves.
//
// The request's capacity (auditlog.Capacity*) goes with it, in the context,
// and every audit row written for it records it.

// ScopeHeader names the tenant an administration request acts in.
const ScopeHeader = "PetalBook-Scope"

// ScopeParam is the query parameter that stands for ScopeHeader on an
// administration route's GET.
const ScopeParam = "scope"

// actingScope is the scope of a request.
type actingScope struct {
	// tenant is the tenant the request acts in.
	tenant string
	// own is the person's own tenant.
	own string
	// narrowed: the header named it, and the subject was cut to it.
	narrowed bool
	// capacity is what the person acts as on this route: site_admin,
	// tenant_admin, or "" for no administration.
	capacity string
	// entered: a site administrator entered the tenant's scope; it is
	// recorded (consoleOpened) once the route lets the request through.
	entered bool
}

// enterScope sets c's acting scope from the person and the header, and
// answers and returns false on a refusal. It reads nothing but the session's
// principal, the header and the cached list of tenants.
//
// A reason to enter a tenant's scope could be asked here later (owner
// question, 3-6-c.md section 11): the header would carry it and this would
// refuse without it; nothing else would change.
func (s *server) enterScope(c *call, rt route) bool {
	if c.p == nil {
		return true
	}
	c.scope = actingScope{tenant: c.p.User.TenantID, own: c.p.User.TenantID}
	admin := rt.access.kind == kindKey && administers(rt.access.key)
	if admin {
		c.scope.capacity = auditlog.CapacityTenantAdmin
		for _, k := range rt.access.keys() {
			if capacityFor(c.p.Access, k) == auditlog.CapacitySiteAdmin {
				c.scope.capacity = auditlog.CapacitySiteAdmin
			}
		}
	}
	vals := c.r.Header.Values(ScopeHeader)
	// What a browser fetches by its address (the audit's CSV) cannot carry a
	// header: on an administration route's GET, ?scope= is read as the header
	// is, with the same checks. A write names its scope with the header only;
	// both at once is refused. A person's own routes do not read the name.
	if q, ok := c.r.URL.Query()[ScopeParam]; ok && admin {
		if c.r.Method != http.MethodGet {
			writeError(c.w, http.StatusBadRequest, "invalid", "?"+ScopeParam+"= is for reads only; a write sends "+ScopeHeader+".")
			return false
		}
		vals = append(slices.Clone(vals), q...)
	}
	if len(vals) > 0 {
		if !admin {
			writeError(c.w, http.StatusBadRequest, "invalid", ScopeHeader+" is for administration endpoints only.")
			return false
		}
		id, ok := strings.CutPrefix(vals[0], "tenant:")
		if len(vals) > 1 || !ok || id == "" || strings.TrimSpace(id) != id {
			writeError(c.w, http.StatusBadRequest, "invalid", ScopeHeader+" (or ?"+ScopeParam+"=) is tenant:<id>, once.")
			return false
		}
		// Who may not read the tenant's settings there is told it does not
		// exist, as for an unknown one; the request never falls back to the
		// person's own scope.
		if !c.p.Access.Can(authz.TenantSettingsRead, id) || !s.knownTenant(id) {
			writeError(c.w, http.StatusNotFound, "not_found", "No such tenant.")
			return false
		}
		// A key held in the tenant itself is acted on as its administrator's;
		// one held only across the site (the narrowed subject keeps the
		// tenant keys of the site's) as a site administrator's, and that is
		// recorded as entering the scope, from the keys the route uses.
		if admin {
			c.scope.capacity = auditlog.CapacityTenantAdmin
			for _, k := range rt.access.keys() {
				if capacityIn(c.p.Access, k, id) == auditlog.CapacitySiteAdmin {
					c.scope.capacity = auditlog.CapacitySiteAdmin
				}
			}
		}
		p := *c.p
		p.User, p.Access = narrowTo(p.User, p.Access, id)
		c.p = &p
		c.scope.tenant, c.scope.narrowed, c.scope.entered = id, true, c.scope.capacity == auditlog.CapacitySiteAdmin
	}
	if c.scope.capacity != "" {
		c.r = c.r.WithContext(auditlog.WithCapacity(c.r.Context(), c.scope.capacity))
	}
	return true
}

// narrowTo is the person as that tenant's administrator: in its tenant, not a
// site administrator, and holding only the tenant keys they hold there. A
// bulk job started in a tenant's scope runs with it too.
func narrowTo(u store.User, access authz.Subject, tenant string) (store.User, authz.Subject) {
	u.TenantID, u.SiteAdmin = tenant, false
	return u, access.NarrowTo(tenant)
}

// knownTenant reports whether a tenant exists, from memory (the store keeps
// the tenants' names and states cached). In single-user mode there are none
// but the default one.
func (s *server) knownTenant(id string) bool {
	if _, ok := s.opt.Store.TenantRefOf(id); ok {
		return true
	}
	return id == store.DefaultTenantID && s.single
}

// administers reports whether a key is administration: a site key, or a
// tenant key that the roles without administration do not hold.
func administers(k authz.Key) bool {
	if k.Scope() == authz.ScopeSite {
		return true
	}
	member, _ := authz.RoleSet(authz.RoleMember)
	viewer, _ := authz.RoleSet(authz.RoleViewer)
	return k.Scope() == authz.ScopeTenant && !member.Has(k) && !viewer.Has(k)
}

// capacityFor is the capacity of a holder of k: a site administrator's for a
// key held across the site, a tenant administrator's otherwise.
func capacityFor(access authz.Subject, k authz.Key) string {
	if access.SiteWide(k) {
		return auditlog.CapacitySiteAdmin
	}
	return auditlog.CapacityTenantAdmin
}

// capacityIn is the capacity of a holder of k acting in tenant: a key held in
// the tenant itself is a tenant administrator's, one held only across the site
// a site administrator's.
func capacityIn(access authz.Subject, k authz.Key, tenant string) string {
	if access.Tenant[tenant].Has(k) {
		return auditlog.CapacityTenantAdmin
	}
	return capacityFor(access, k)
}

// scopeResp is one place the console may act: the whole site, or a tenant.
type scopeResp struct {
	// Kind is "site" or "tenant"; ID and Name are the tenant's, empty for the site.
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	// Capacity is what the person acts as there: tenant_admin when the tenant's own roles give them everything they
	// hold in it, site_admin when some of it is held only across the site (authz.Subject.TenantCovers).
	Capacity string `json:"capacity"`
	// Permissions are the keys they hold in that scope, as a request in it is held to (a tenant's: its tenant keys).
	Permissions []string `json:"permissions"`
}

// adminScopes answers GET /api/admin/scopes: where the person may act in the
// console, from the subject the session cache holds and the cached tenants'
// names, with no query. The site first, for whoever holds the tenant keys
// across it; then every tenant they administer (hold tenant.settings.read in,
// the key the header needs), by name. It asks for no key of the person's own
// tenant: someone who is a member of theirs and administers another one has
// the console for that. It is a person's own route (it never takes the scope
// header), and answers 403 when the list would be empty: no console.
func (s *server) adminScopes(c *call) {
	out := []scopeResp{}
	acc := c.p.Access
	if acc.SiteWide(authz.TenantSettingsRead) {
		out = append(out, scopeResp{Kind: "site", Capacity: auditlog.CapacitySiteAdmin, Permissions: acc.Site.Names()})
	}
	var tenants []scopeResp
	for id, set := range acc.Tenant {
		if !set.Has(authz.TenantSettingsRead) {
			continue
		}
		ref, ok := s.opt.Store.TenantRefOf(id)
		if !ok {
			continue
		}
		capacity := auditlog.CapacityTenantAdmin
		if !acc.TenantCovers(id) {
			capacity = auditlog.CapacitySiteAdmin
		}
		tenants = append(tenants, scopeResp{Kind: "tenant", ID: id, Name: ref.Name, Capacity: capacity, Permissions: acc.TenantKeys(id).Names()})
	}
	slices.SortFunc(tenants, func(a, b scopeResp) int {
		if n := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); n != 0 {
			return n
		}
		return strings.Compare(a.ID, b.ID)
	})
	out = append(out, tenants...)
	if len(out) == 0 {
		writeError(c.w, http.StatusForbidden, "forbidden", "Only an administrator may do that.")
		return
	}
	writeJSON(c.w, http.StatusOK, map[string]any{"scopes": out})
}

// maxConsoleAudit bounds the console-open throttle's table.
const maxConsoleAudit = 10000

// consoleOpened records that a site administrator entered a tenant's scope,
// for a request the route's key and feature let through (a refused one opened
// nothing): at most one row an hour for each session and tenant, in a bounded
// table of its own (the views' audit table is cleared at its cap, which would
// reset this one too). It opens no content, so it asks for no reason.
func (s *server) consoleOpened(c *call) {
	if !c.scope.entered {
		return
	}
	tenant := c.scope.tenant
	who := c.p.SessionID
	if who == "" {
		who = c.p.User.ID
	}
	if !s.consoles.first(who+"\x00"+tenant, s.now()) {
		return
	}
	s.audit(c, auth.AuditEvent{Kind: "tenant_console_opened", Actor: c.p.User.ID, Tenant: tenant, Capacity: c.scope.capacity,
		Detail: map[string]any{"tenant": tenant}})
}

// tenant is the tenant the request acts in.
func (c *call) tenant() string { return c.scope.tenant }

// inOtherTenant reports whether the request acts, narrowed, in another tenant
// than the person's own: what only a site key allows (becoming a member
// across tenants) is not done there, but in the site's scope.
func (c *call) inOtherTenant() bool { return c.scope.narrowed && c.scope.tenant != c.scope.own }

// tenant is the tenant an MCP call acts in: its owner's (MCP has no scope).
func (c *mcpCall) tenant() string { return c.p.User.TenantID }

// reach is what an administrator's request reaches of people and of things
// that hang under them: every tenant for a site administrator in the site's
// scope, else the acting tenant.
func (c *call) reach() store.Scope { return store.ScopeOf(c.user()) }

// adminReach is reach for spaces: in a tenant, personal spaces are left out
// (store.AdminScopeOf).
func (c *call) adminReach() store.Scope { return store.AdminScopeOf(c.user()) }

// can reports whether the caller holds k in the acting tenant (a site key:
// across the site). A handler asks this for what it does beyond the key of
// its route; in single-user mode the one user holds every key.
func (c *call) can(k authz.Key) bool {
	return c.p != nil && c.p.Access.Can(k, c.tenant())
}

// siteWide reports whether the caller holds k across the site: a tenant key
// in every tenant, which is what reaches beyond their own (what a site
// administrator holds, and never one in a tenant's scope).
func (c *call) siteWide(k authz.Key) bool {
	return c.p != nil && c.p.Access.SiteWide(k)
}

// ownScope is the acting tenant: what a person sees of anything that is not
// an administrator's view (their groups, their tokens).
func (c *call) ownScope() store.Scope { return store.InTenant(c.tenant()) }

// adminScope is what an administrator's request covers: every tenant for a
// site administrator, the acting one otherwise, narrowed by ?tenant={id}.
// Another tenant than that is answered as an id that does not exist, for a
// tenant administrator, and so is a tenant that does not exist. It answers
// and returns false on such a refusal.
func (s *server) adminScope(c *call) (store.Scope, bool) {
	sc := c.adminReach()
	id := c.r.URL.Query().Get("tenant")
	if id == "" {
		return sc, true
	}
	if !sc.All() && id != sc.Tenant() {
		writeError(c.w, http.StatusNotFound, "not_found", "No such tenant.")
		return sc, false
	}
	if sc.All() && !s.tenantExists(c, id) {
		return sc, false
	}
	return sc.Narrow(id), true
}

func (s *server) tenantExists(c *call, id string) bool {
	_, err := store.GetTenant(c.r.Context(), s.opt.Store.Q(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(c.w, http.StatusNotFound, "not_found", "No such tenant.")
		return false
	}
	if err != nil {
		s.fail(c.w, err)
		return false
	}
	return true
}

// createIn is the tenant something an administrator makes belongs to:
// the one ?tenant= names, else the acting one.
func (s *server) createIn(c *call) (string, bool) {
	sc, ok := s.adminScope(c)
	if !ok {
		return "", false
	}
	if sc.All() {
		return c.tenant(), true
	}
	return sc.Tenant(), true
}
