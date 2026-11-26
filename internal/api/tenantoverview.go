package api

import (
	"net/http"
	"sort"

	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/store"
)

// overviewAdmins and overviewAudit are how many administrators and audit rows
// the overview shows.
const (
	overviewAdmins = 10
	overviewAudit  = 10
)

type overviewAdminResp struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Name      string `json:"name"`
	SiteAdmin bool   `json:"siteAdmin"`
	Disabled  bool   `json:"disabled"`
}

type overviewAuditResp struct {
	ID       string `json:"id"`
	At       string `json:"at"`
	Kind     string `json:"kind"`
	Category string `json:"category"`
	Capacity any    `json:"capacity"`
	Actor    any    `json:"actor"`
	Username string `json:"username"`
}

// adminTenantOverview is GET /api/admin/tenants/{tenant}/overview: one call
// for the tenant's page. It reads the tenant row, its usage row (stored
// counts), one count of its groups, its administrators and the newest rows of
// its audit, each through an index with a LIMIT; the features come from the
// settings' cache. Nothing scans the tenant's people, spaces or pages.
func (s *server) adminTenantOverview(c *call) {
	t, ok := s.visibleTenant(c)
	if !ok {
		return
	}
	ctx := c.r.Context()
	u, err := s.usageOf(ctx, t.ID)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	base, err := s.toTenant(ctx, *t, u, nil)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	q := s.opt.Store.Q()
	groups, err := store.CountGroups(ctx, q, store.InTenant(t.ID))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	admins, adminCount, err := store.TenantAdmins(ctx, q, t.ID, overviewAdmins)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	adminOut := make([]overviewAdminResp, len(admins))
	for i, a := range admins {
		adminOut[i] = overviewAdminResp{a.ID, a.Username, a.Name, a.SiteAdmin, a.Disabled}
	}
	createdBy := ""
	if t.CreatedBy != "" {
		if cu, err := store.GetUserAny(ctx, q, t.CreatedBy); err == nil {
			createdBy = cu.Username
		}
	}
	off := []string{}
	if !s.single && s.settings != nil {
		for f := range featureNames {
			on, err := s.featureOn(ctx, t.ID, f)
			if err != nil {
				s.fail(c.w, err)
				return
			}
			if !on {
				off = append(off, f)
			}
		}
	}
	sort.Strings(off)
	// The audit is read by those who hold the key to it; the rest get none.
	recent := []overviewAuditResp{}
	if c.p.Access.Can(authz.TenantAuditRead, t.ID) {
		f := store.AuditFilter{HideSite: !c.siteWide(authz.TenantAuditRead)}
		rows, _, err := s.auth.ListAudit(ctx, store.InTenant(t.ID), f, overviewAudit)
		if err != nil {
			s.fail(c.w, err)
			return
		}
		for _, r := range rows {
			recent = append(recent, overviewAuditResp{r.ID, rfc3339(r.At), r.Kind, r.Category, nullableStr(r.Capacity), nullableStr(r.Actor), r.Username})
		}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Tenant       tenantResp          `json:"tenant"`
		CreatedBy    string              `json:"createdByName"`
		Groups       int                 `json:"groups"`
		Admins       []overviewAdminResp `json:"admins"`
		AdminCount   int                 `json:"adminCount"`
		FeaturesOff  []string            `json:"featuresOff"`
		RecentActive []overviewAuditResp `json:"recentAdminActivity"`
	}{base, createdBy, groups, adminOut, adminCount, off, recent})
}

func nullableStr(v string) any {
	if v == "" {
		return nil
	}
	return v
}
