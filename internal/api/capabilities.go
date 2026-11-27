package api

import (
	"context"
	"net/http"
	"slices"
	"strconv"

	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
)

// Turning off a feature that is in use (phase-3-autonomy-and-providers.md,
// "Capabilities"): the change first shows what would stop, and goes through
// the second confirmation (POST /api/admin/confirm, action
// change_capabilities). What is stored is not deleted: it has no effect while
// the feature is off, and works again as it was when it is on. A feature not
// in use is changed directly.
//
// Features that have a count of their use: features.domains (the tenants'
// claimed email domains). The others are added with the first thing of theirs
// that can be counted.

// capabilityUse counts what a feature's tenants use of it.
var capabilityUse = map[string]func(s *server, ctx context.Context) (map[string]capabilityCount, error){
	settings.FeatureDomains: (*server).domainUse,
}

// capabilityCount is what one tenant would lose.
type capabilityCount struct {
	Claims, Verified int
}

func (s *server) domainUse(ctx context.Context) (map[string]capabilityCount, error) {
	rows, err := store.DomainUseByTenant(ctx, s.opt.Store.Q())
	if err != nil {
		return nil, err
	}
	out := make(map[string]capabilityCount, len(rows))
	for _, r := range rows {
		out[r.TenantID] = capabilityCount{r.Claims, r.Verified}
	}
	return out, nil
}

// turnsOff says whether a change of a feature may switch it off: a value,
// rule or lock of false.
func turnsOff(ch settings.Change) bool {
	switch {
	case ch.HasValue && ch.Value == false:
		return true
	case ch.HasSiteRule && ch.SiteRule != nil && ch.SiteRule.Value == false:
		return true
	case ch.HasSiteLock && ch.SiteLock && ch.Value == false:
		return true
	case ch.HasLower && ch.Lower != nil && ch.Lower.Mode == settings.Lock:
		return true
	}
	return false
}

// capabilityItem is one tenant of the preview.
type capabilityItem struct {
	Tenant   string `json:"tenant"`
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	Feature  string `json:"feature"`
	Claims   int    `json:"domains"`
	Verified int    `json:"verified"`
}

// capabilityPlan is the preview of switching a feature off: for the site
// (tenant ""), every tenant that has the feature now and uses it; for one
// tenant, that one. Empty when nothing is in use.
func (s *server) capabilityPlan(ctx context.Context, key, tenant string) (confirmPlan, error) {
	use, ok := capabilityUse[key]
	if !ok {
		return confirmPlan{}, nil
	}
	counts, err := use(s, ctx)
	if err != nil {
		return confirmPlan{}, err
	}
	var plan []any
	var parts []string
	ids := s.opt.Store.TenantIDs()
	slices.Sort(ids)
	for _, id := range ids {
		ref, _ := s.opt.Store.TenantRefOf(id)
		n, ok := counts[id]
		if !ok || (tenant != "" && ref.ID != tenant) {
			continue
		}
		on, err := s.settings.Feature(ctx, ref.ID, key)
		if err != nil {
			return confirmPlan{}, err
		}
		if !on {
			continue
		}
		plan = append(plan, capabilityItem{ref.ID, ref.Slug, ref.Name, key, n.Claims, n.Verified})
		parts = append(parts, ref.ID, strconv.Itoa(n.Claims), strconv.Itoa(n.Verified))
	}
	return confirmPlan{Plan: plan, Hash: hashOf(append([]string{key, tenant}, parts...)...)}, nil
}

// planCapabilities is the confirmation's plan of change_capabilities: the
// caller must be one who may make the change (the site's settings, or a
// site administrator's rule for the tenant).
func (s *server) planCapabilities(c *call, req confirmRequest) (confirmPlan, bool) {
	if _, ok := capabilityUse[req.Key]; !ok {
		writeError(c.w, http.StatusBadRequest, "invalid", "That setting has no preview: it is changed directly.")
		return confirmPlan{}, false
	}
	if req.Tenant == "" && !c.p.Access.Site.Has(authz.SiteSettingsManage) || req.Tenant != "" && (!c.siteWide(authz.TenantSettingsWrite) || !s.knownTenant(req.Tenant)) {
		writeError(c.w, http.StatusNotFound, "not_found", "No such setting.")
		return confirmPlan{}, false
	}
	p, err := s.capabilityPlan(c.r.Context(), req.Key, req.Tenant)
	if err != nil {
		s.fail(c.w, err)
		return p, false
	}
	return p, true
}

// capabilityGate is the check of a change of a feature before it is written:
// one that may switch it off for a tenant that uses it needs the confirmation
// (the request's "confirm"); without one it is answered 409 confirm_required,
// and the client asks POST /api/admin/confirm for the preview. It reports
// false after answering.
func (s *server) capabilityGate(c *call, key, tenant string, ch settings.Change, tok string) bool {
	if _, ok := capabilityUse[key]; !ok || !turnsOff(ch) {
		return true
	}
	p, err := s.capabilityPlan(c.r.Context(), key, tenant)
	if err != nil {
		s.fail(c.w, err)
		return false
	}
	if len(p.Plan) == 0 {
		return true
	}
	if tok == "" {
		writeError(c.w, http.StatusConflict, "confirm_required", "This feature is in use: ask POST /api/admin/confirm (change_capabilities) for what would stop, and send its confirm.")
		return false
	}
	return s.useConfirm(c, tok, confirmRequest{Action: "change_capabilities", Key: key, Tenant: tenant})
}
