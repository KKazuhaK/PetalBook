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

// usingTenants are the tenants that use a feature (of one tenant, or every
// one for ""), sorted.
func (s *server) usingTenants(ctx context.Context, key, tenant string) ([]string, error) {
	counts, err := capabilityUse[key](s, ctx)
	if err != nil {
		return nil, err
	}
	var using []string
	for id := range counts {
		if (tenant == "" || id == tenant) && s.knownTenant(id) {
			using = append(using, id)
		}
	}
	slices.Sort(using)
	return using, nil
}

// switchesOff is the tenants that use the feature and that the change would
// switch it off for, now or at a later start or end of a rule's window
// (settings.SiteSwitchesOff, TenantSwitchesOff), whatever words the change
// uses (a value, a rule, a lock, a restriction, dates). For one tenant ("" for
// the site's layer). A change the settings refuse answers that refusal, so the
// caller's write answers it as it would.
func (s *server) switchesOff(ctx context.Context, key, tenant string, ch settings.Change) ([]string, error) {
	using, err := s.usingTenants(ctx, key, tenant)
	if err != nil || len(using) == 0 {
		return nil, err
	}
	if tenant == "" {
		return s.settings.SiteSwitchesOff(ctx, key, ch, using)
	}
	off, err := s.settings.TenantSwitchesOff(ctx, tenant, key, ch)
	if err != nil || !off {
		return nil, err
	}
	return using, nil
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
// one that would switch it off for a tenant that uses it needs the
// confirmation (the request's "confirm"); without one it is answered 409
// confirm_required with the preview of those tenants, and the client asks
// POST /api/admin/confirm for its token. It reports false after answering.
func (s *server) capabilityGate(c *call, key, tenant string, ch settings.Change, tok string) bool {
	if _, ok := capabilityUse[key]; !ok {
		return true
	}
	off, err := s.switchesOff(c.r.Context(), key, tenant, ch)
	if err != nil {
		s.failSetting(c, err)
		return false
	}
	return s.confirmOff(c, key, tenant, off, tok)
}

// planGate is capabilityGate for a plan: its rules (rules: what the change
// writes) given to tenants (assign: they are given the plan, and lose their
// manual rules), for every feature with a count of its use. tenant is the
// one tenant of an assignment, "" for a change of the plan itself.
func (s *server) planGate(c *call, planID string, rules map[string]*settings.Rule, assign bool, tenant string, tenants []string, tok string) bool {
	keys := make([]string, 0, len(capabilityUse))
	for k := range capabilityUse {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, key := range keys {
		using, err := s.usingTenants(c.r.Context(), key, tenant)
		if err != nil {
			s.fail(c.w, err)
			return false
		}
		using = slices.DeleteFunc(using, func(id string) bool { return !slices.Contains(tenants, id) })
		if len(using) == 0 {
			continue
		}
		off, err := s.settings.PlanSwitchesOff(c.r.Context(), key, planID, rules, assign, using)
		if err != nil {
			s.failSetting(c, err)
			return false
		}
		if !s.confirmOff(c, key, tenant, off, tok) {
			return false
		}
	}
	return true
}

// confirmOff answers a change that switches a feature off for the tenants off:
// nothing to ask when none, 409 confirm_required with their preview without a
// token, else the token's check (useConfirm).
func (s *server) confirmOff(c *call, key, tenant string, off []string, tok string) bool {
	if len(off) == 0 {
		return true
	}
	if tok == "" {
		p, err := s.capabilityPlan(c.r.Context(), key, tenant)
		if err != nil {
			s.fail(c.w, err)
			return false
		}
		var lines []any
		for _, l := range p.Plan {
			if it, ok := l.(capabilityItem); ok && slices.Contains(off, it.Tenant) {
				lines = append(lines, it)
			}
		}
		if lines == nil {
			lines = []any{}
		}
		writeJSON(c.w, http.StatusConflict, errorBody{Error: "confirm_required", Plan: lines,
			Message: "This feature is in use: ask POST /api/admin/confirm (change_capabilities) for what would stop, and send its confirm."})
		return false
	}
	return s.useConfirm(c, tok, confirmRequest{Action: "change_capabilities", Key: key, Tenant: tenant})
}

// billingGuard is what settings.SetBilling asks of a billing rule that would
// switch a feature off: refused while the tenant uses it, as nobody is there
// to confirm.
func (s *server) billingGuard(ctx context.Context, tenant, key string) error {
	if _, ok := capabilityUse[key]; !ok {
		return nil
	}
	using, err := s.usingTenants(ctx, key, tenant)
	if err != nil {
		return err
	}
	if len(using) > 0 {
		return &settings.Error{Code: "confirm_required", Msg: key + " is in use by the tenant: billing does not switch it off; a site administrator does, with the confirmation"}
	}
	return nil
}
