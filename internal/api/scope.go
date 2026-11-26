package api

import (
	"errors"
	"net/http"

	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/store"
)

// can reports whether the caller holds k in their own tenant (a site key:
// across the site). A handler asks this for what it does beyond the key of
// its route; in single-user mode the one user holds every key.
func (c *call) can(k authz.Key) bool {
	return c.p != nil && c.p.Access.Can(k, c.user().TenantID)
}

// siteWide reports whether the caller holds k across the site: a tenant key
// in every tenant, which is what reaches beyond their own (what a site
// administrator holds).
func (c *call) siteWide(k authz.Key) bool {
	return c.p != nil && c.p.Access.SiteWide(k)
}

// ownScope is the caller's own tenant: what a person sees of anything that is
// not an administrator's view (their groups, their tokens).
func (c *call) ownScope() store.Scope { return store.InTenant(c.user().TenantID) }

// adminScope is what an administrator's request covers: every tenant for a
// site administrator, their own for a tenant administrator, narrowed by
// ?tenant={id}. Another tenant than one's own is answered as an id that does
// not exist, for a tenant administrator, and so is a tenant that does not
// exist. It answers and returns false on such a refusal.
func (s *server) adminScope(c *call) (store.Scope, bool) {
	sc := store.AdminScopeOf(c.user())
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
// the one ?tenant= names, else their own.
func (s *server) createIn(c *call) (string, bool) {
	sc, ok := s.adminScope(c)
	if !ok {
		return "", false
	}
	if sc.All() {
		return c.user().TenantID, true
	}
	return sc.Tenant(), true
}
