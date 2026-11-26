package api

import (
	"errors"
	"net/http"
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
		c.scope.capacity = capacityFor(c.p.Access, rt.access.key)
	}
	vals := c.r.Header.Values(ScopeHeader)
	if len(vals) > 0 {
		if !admin {
			writeError(c.w, http.StatusBadRequest, "invalid", ScopeHeader+" is for administration endpoints only.")
			return false
		}
		id, ok := strings.CutPrefix(vals[0], "tenant:")
		if len(vals) > 1 || !ok || id == "" || strings.TrimSpace(id) != id {
			writeError(c.w, http.StatusBadRequest, "invalid", ScopeHeader+" is tenant:<id>.")
			return false
		}
		// Who may not read the tenant's settings there is told it does not
		// exist, as for an unknown one; the request never falls back to the
		// person's own scope.
		if !c.p.Access.Can(authz.TenantSettingsRead, id) || !s.knownTenant(id) {
			writeError(c.w, http.StatusNotFound, "not_found", "No such tenant.")
			return false
		}
		siteWide := c.p.Access.SiteWide(authz.TenantSettingsRead)
		p := *c.p
		p.User, p.Access = narrowTo(p.User, p.Access, id)
		c.p = &p
		c.scope.tenant, c.scope.narrowed, c.scope.entered = id, true, siteWide
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
