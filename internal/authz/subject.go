package authz

import (
	"context"
	"sync"
)

// Subject is what a person holds outside the content of spaces: the keys they
// hold across the site, and the keys they hold in each tenant. A tenant key in
// Site is held in every tenant. It is built when the session cache loads the
// person, and is never changed after: requests share it.
//
// Today it is made from the account's role and site-administrator flag
// (SubjectFor); from 3.7 a person's memberships of organizations fill Tenant.
type Subject struct {
	Site   Set
	Tenant map[string]Set
}

// Can reports whether the subject holds k in tenant (tenant is ignored for a
// site key). Space keys are never held here: a space's permissions are its
// members' (internal/perm).
func (s Subject) Can(k Key, tenant string) bool {
	switch k.Scope() {
	case ScopeSite:
		return s.Site.Has(k)
	case ScopeTenant:
		return s.Site.Has(k) || s.Tenant[tenant].Has(k)
	}
	return false
}

// SiteWide reports whether the subject holds k across the site, which is not
// bound by any tenant's features.
func (s Subject) SiteWide(k Key) bool { return s.Site.Has(k) }

// In is every key the subject holds that applies in tenant: its site keys,
// and its tenant keys there.
func (s Subject) In(tenant string) Set { return s.Site.Union(s.Tenant[tenant]) }

// NarrowTo is the subject cut to the tenant keys it holds in tenant, held
// there only: what it may do acting as that tenant's administrator. Site keys
// are set aside, and no key is held across the site, so the tenant's features
// bind it (Decide). It never holds more than s. s is not changed.
// It costs one intersection of bit sets: the tenant keys are built once.
func (s Subject) NarrowTo(tenant string) Subject {
	buildRoles()
	return Subject{Tenant: map[string]Set{tenant: s.In(tenant).Intersect(tenantAdminSet)}}
}

// TenantKeys are the keys the subject holds acting in tenant (what NarrowTo keeps): the tenant keys of its site keys
// and of its roles there.
func (s Subject) TenantKeys(tenant string) Set {
	buildRoles()
	return s.In(tenant).Intersect(tenantAdminSet)
}

// TenantCovers reports whether the tenant's own roles give the subject everything it holds acting in tenant: false
// when some of it is held only across the site, which makes its acts there a site administrator's.
func (s Subject) TenantCovers(tenant string) bool {
	buildRoles()
	return s.Tenant[tenant].Intersect(tenantAdminSet) == s.TenantKeys(tenant)
}

// Grant is a custom role a person holds, directly or through a group, as the
// subject is built from it: the role's scope and keys, and where it is held
// (Tenant "" is across the site).
type Grant struct {
	Scope  Scope
	Keys   Set
	Tenant string
}

// WithGrants is the subject with the keys of custom roles added. s is not
// changed (a subject is shared once built). The guardrails of the scopes hold
// whatever a stored role lists:
//   - a site role held across the site adds its site and tenant keys to Site;
//     held in one tenant, only its tenant keys, there;
//   - a tenant role adds only tenant keys, in the tenant it is held in (one
//     held nowhere adds nothing): a role of a tenant never reaches the site;
//   - a space role is the space level's (reserved) and adds nothing here, and
//     no space key is ever held outside a space;
//   - a key only the built-in roles hold (BuiltInOnly) is never added.
func (s Subject) WithGrants(gs []Grant) Subject {
	if len(gs) == 0 {
		return s
	}
	grantable := GrantableKeys()
	tenantKeys := OfScope(ScopeTenant).Intersect(grantable)
	out := Subject{Site: s.Site, Tenant: make(map[string]Set, len(s.Tenant)+len(gs))}
	for t, set := range s.Tenant {
		out.Tenant[t] = set
	}
	for _, g := range gs {
		switch {
		case g.Scope == ScopeSite && g.Tenant == "":
			out.Site = out.Site.Union(g.Keys.Intersect(OfScope(ScopeSite).Intersect(grantable).Union(tenantKeys)))
		case (g.Scope == ScopeSite || g.Scope == ScopeTenant) && g.Tenant != "":
			out.Tenant[g.Tenant] = out.Tenant[g.Tenant].Union(g.Keys.Intersect(tenantKeys))
		}
	}
	return out
}

// The built-in roles. They are code, not rows: custom roles (a later part of
// 3.6) are stored, and these stay as they are.
const (
	// RoleSiteAdmin: every site key, and every tenant key in every tenant.
	RoleSiteAdmin = "siteAdmin"
	// RoleTenantAdmin: every tenant key in their tenant.
	RoleTenantAdmin = "tenantAdmin"
	// RoleMember and RoleViewer: a few tenant keys.
	RoleMember = "member"
	RoleViewer = "viewer"
)

var (
	rolesOnce                    sync.Once
	siteAdminSet, tenantAdminSet Set
	memberSet, viewerSet         Set
)

// memberKeys and viewerKeys are what the two roles without administration hold.
var (
	memberKeys = []*Key{&TenantSpacesCreate}
	viewerKeys = []*Key{}
)

func buildRoles() {
	rolesOnce.Do(func() {
		mu.Lock()
		frozen = true
		mu.Unlock()
		tenantAdminSet = OfScope(ScopeTenant)
		siteAdminSet = OfScope(ScopeSite).Union(tenantAdminSet)
		for _, k := range memberKeys {
			memberSet = memberSet.With(*k)
		}
		for _, k := range viewerKeys {
			viewerSet = viewerSet.With(*k)
		}
	})
}

// RoleSet is what a built-in role holds, and false for a name that is none.
func RoleSet(role string) (Set, bool) {
	buildRoles()
	switch role {
	case RoleSiteAdmin:
		return siteAdminSet, true
	case RoleTenantAdmin:
		return tenantAdminSet, true
	case RoleMember:
		return memberSet, true
	case RoleViewer:
		return viewerSet, true
	}
	return Set{}, false
}

// SubjectFor is the subject of an account from what the account says today:
// its role ("admin" or "member"), whether it administers the whole server, and
// its tenant. An administrator of the whole server is a site administrator;
// another administrator one of their tenant.
func SubjectFor(role string, siteAdmin bool, tenant string) Subject {
	buildRoles()
	switch {
	case role == "admin" && siteAdmin:
		return Subject{Site: siteAdminSet}
	case role == "admin":
		return Subject{Tenant: map[string]Set{tenant: tenantAdminSet}}
	case role == "member":
		return Subject{Tenant: map[string]Set{tenant: memberSet}}
	case role == "viewer":
		return Subject{Tenant: map[string]Set{tenant: viewerSet}}
	}
	return Subject{}
}

// Features says whether a feature (a boolean setting, features.*) is on for a
// tenant. It is read from the cached settings (one map read); nil means every
// feature is on.
type Features interface {
	On(ctx context.Context, tenant, feature string) bool
}

// Decision is the outcome of Decide.
type Decision uint8

const (
	// Allowed: the subject may.
	Allowed Decision = iota
	// FeatureOff: the key needs a feature the tenant does not have.
	FeatureOff
	// Denied: the subject does not hold the key there.
	Denied
)

// Decide is the check of a key in a tenant, in its order: first the tenant's
// feature (a holder of the key across the site is not bound by it), then
// the key. The caller adds what comes after (the per-organization check of 3.7).
func Decide(ctx context.Context, s Subject, k Key, tenant string, feats Features) Decision {
	if c := k.Feature(); c != "" && feats != nil && !s.SiteWide(k) && !feats.On(ctx, tenant, c) {
		return FeatureOff
	}
	if !s.Can(k, tenant) {
		return Denied
	}
	return Allowed
}
