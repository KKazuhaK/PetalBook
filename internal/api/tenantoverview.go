package api

import (
	"context"
	"net/http"
	"sort"

	"petalbook.app/server/internal/auditlog"
	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/settings"
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
	admins, adminCount, err := store.TenantAdmins(ctx, q, store.InTenant(t.ID), overviewAdmins)
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
	// A tenant without features.tenantAudit gets none, as the audit routes refuse it.
	auditOn := true
	if c.p.Access.Can(authz.TenantAuditRead, t.ID) && !c.siteWide(authz.TenantAuditRead) && !s.single && s.settings != nil {
		var err error
		if auditOn, err = s.featureOn(ctx, t.ID, settings.FeatureTenantAudit); err != nil {
			s.fail(c.w, err)
			return
		}
	}
	if auditOn && c.p.Access.Can(authz.TenantAuditRead, t.ID) {
		rows, err := s.recentAdminActivity(ctx, t.ID, !c.siteWide(authz.TenantAuditRead))
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

// recentAdminActivity is the newest overviewAudit rows of the tenant's audit
// that are administrative: done in an administrator's capacity, or of the admin
// or security categories. Page reads, downloads, sign-ins and API calls are
// left out. Each of the four is one index range read with a LIMIT (the capacity
// ones on audit_log_tenant_capacity), merged by id, so a tenant with millions of
// reads costs no more than one with none.
func (s *server) recentAdminActivity(ctx context.Context, tenant string, hideSite bool) ([]auth.AuditEntry, error) {
	sc := store.InTenant(tenant)
	filters := []store.AuditFilter{
		{Categories: []string{string(auditlog.Admin), string(auditlog.Security)}},
		{Capacity: auditlog.CapacitySiteAdmin},
		{Capacity: auditlog.CapacityTenantAdmin},
	}
	seen := map[string]bool{}
	var all []auth.AuditEntry
	for _, f := range filters {
		f.HideSite = hideSite
		rows, _, err := s.auth.ListAudit(ctx, sc, f, overviewAudit)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if !seen[r.ID] {
				seen[r.ID] = true
				all = append(all, r)
			}
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID > all[j].ID })
	if len(all) > overviewAudit {
		all = all[:overviewAudit]
	}
	return all, nil
}

func nullableStr(v string) any {
	if v == "" {
		return nil
	}
	return v
}
