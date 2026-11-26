package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/page"
	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
)

// Plans (docs/api.md, "Rules for a tenant"): a plan is a bundle of rules, each
// the same thing as a manual rule for one tenant (settings.Rule: a value, a
// lock or a restriction, and no dates). Every write is the site's
// (site.tenants.manage). A tenant that has a plan has its rules as the lowest
// source of its own; the cached settings answer every check, so nothing here
// is on a request's path.

// MaxPlans is how many plans there may be.
const MaxPlans = 100

type planRefResp struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// planRef is the plan a tenant has, nil for none.
func (s *server) planRef(ctx context.Context, tenant string) (*planRefResp, error) {
	ref, ok := s.opt.Store.TenantRefOf(tenant)
	if !ok || ref.PlanID == "" {
		return nil, nil
	}
	all, err := store.ListPlans(ctx, s.opt.Store.Q())
	if err != nil {
		return nil, err
	}
	for _, p := range all {
		if p.ID == ref.PlanID {
			return &planRefResp{p.Slug, p.Name}, nil
		}
	}
	return nil, nil
}

// evaluateQuotas looks again at the totals of tenants whose limits moved, so an
// over-quota notice (and its audit row) does not wait for the next write.
func (s *server) evaluateQuotas(ctx context.Context, tenants []string) {
	if s.quota == nil || len(tenants) == 0 {
		return
	}
	t := make([]store.Touched, len(tenants))
	for i, id := range tenants {
		t[i] = store.Touched{TenantID: id}
	}
	_ = s.quota.Evaluate(ctx, t)
}

// ruleBoundaries is told by the settings' timer that the window of a rule
// started or ended: nobody did it, so the audit entry has no actor, and the
// totals the rule moved are looked at again.
func (s *server) ruleBoundaries(ctx context.Context, bs []settings.Boundary) {
	if s.auth == nil {
		return
	}
	var counted []string
	for _, b := range bs {
		kind := "setting_rule_ended"
		if b.Started {
			kind = "setting_rule_started"
		}
		s.auth.Audit(auth.AuditEvent{Kind: kind, Tenant: b.Tenant,
			Detail: map[string]any{"tenant": b.Tenant, "key": b.Key, "source": string(b.Source), "at": rfc3339(b.At)}})
		if strings.HasPrefix(b.Key, "quota.") {
			counted = append(counted, b.Tenant)
		}
	}
	s.evaluateQuotas(ctx, counted)
}

// --- plans ------------------------------------------------------------------------

type planResp struct {
	Slug        string                   `json:"slug"`
	Name        string                   `json:"name"`
	Description string                   `json:"description"`
	Rules       map[string]settings.Rule `json:"rules"`
	Tenants     int                      `json:"tenants"`
}

func (s *server) toPlan(ctx context.Context, p store.Plan) (planResp, error) {
	rules, err := s.settings.PlanRules(ctx, p.ID)
	if err != nil {
		return planResp{}, err
	}
	return planResp{p.Slug, p.Name, p.Description, rules, s.settings.PlanTenants()[p.ID]}, nil
}

func (s *server) adminListPlans(c *call) {
	all, err := store.ListPlans(c.r.Context(), s.opt.Store.Q())
	if err != nil {
		s.fail(c.w, err)
		return
	}
	out := make([]planResp, 0, len(all))
	for _, p := range all {
		r, err := s.toPlan(c.r.Context(), p)
		if err != nil {
			s.fail(c.w, err)
			return
		}
		out = append(out, r)
	}
	writeJSON(c.w, http.StatusOK, map[string]any{"plans": out})
}

func (s *server) planBySlug(c *call) (*store.Plan, bool) {
	p, err := store.GetPlanBySlug(c.r.Context(), s.opt.Store.Q(), c.r.PathValue("slug"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(c.w, http.StatusNotFound, "not_found", "No such plan.")
		return nil, false
	}
	if err != nil {
		s.fail(c.w, err)
		return nil, false
	}
	return p, true
}

func (s *server) adminGetPlan(c *call) {
	p, ok := s.planBySlug(c)
	if !ok {
		return
	}
	r, err := s.toPlan(c.r.Context(), *p)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, r)
}

// planFields checks the name and description of a plan.
func planFields(name, desc *string) (string, string, bool) {
	var n, d string
	if name != nil {
		var ok bool
		if n, ok = cleanTenantName(*name); !ok {
			return "", "", false
		}
	}
	if desc != nil {
		d = strings.TrimSpace(*desc)
		if len([]rune(d)) > 500 {
			return "", "", false
		}
	}
	return n, d, true
}

func (s *server) adminCreatePlan(c *call) {
	var req struct {
		Slug        string                    `json:"slug"`
		Name        *string                   `json:"name"`
		Description *string                   `json:"description"`
		Rules       map[string]*settings.Rule `json:"rules"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	ctx := c.r.Context()
	name, desc, ok := planFields(req.Name, req.Description)
	if !ok || req.Name == nil || !tenantSlugRE.MatchString(req.Slug) {
		writeError(c.w, http.StatusBadRequest, "invalid", "A plan has a slug (2 to 32 characters of a-z, 0-9 and -), a name (1 to 80 characters) and a description of at most 500.")
		return
	}
	if all, err := store.ListPlans(ctx, s.opt.Store.Q()); err != nil {
		s.fail(c.w, err)
		return
	} else if len(all) >= MaxPlans {
		writeError(c.w, http.StatusConflict, "plan_limit", "There are at most 100 plans.")
		return
	}
	p := store.Plan{ID: page.NewID(s.now()), Slug: req.Slug, Name: name, Description: desc}
	if err := store.InsertPlan(ctx, s.opt.Store.Q(), p); errors.Is(err, store.ErrExists) {
		writeError(c.w, http.StatusConflict, "exists", "That slug is taken.")
		return
	} else if err != nil {
		s.fail(c.w, err)
		return
	}
	if err := s.settings.SetPlanRules(ctx, p.ID, c.user().ID, req.Rules); err != nil {
		// A plan with rules that are not valid is not made.
		_ = store.DeletePlan(ctx, s.opt.Store.Q(), p.ID)
		s.failSetting(c, err)
		return
	}
	s.audit(c, auth.AuditEvent{Kind: "plan_created", Actor: c.user().ID, Detail: map[string]any{"plan": p.Slug, "rules": req.Rules, "affected": []string{}}})
	c.w.Header().Set("Location", "/api/admin/plans/"+p.Slug)
	r, err := s.toPlan(ctx, p)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusCreated, r)
}

func (s *server) adminPatchPlan(c *call) {
	var req struct {
		Name        *string                   `json:"name"`
		Description *string                   `json:"description"`
		Rules       map[string]*settings.Rule `json:"rules"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	p, ok := s.planBySlug(c)
	if !ok {
		return
	}
	name, desc, valid := planFields(req.Name, req.Description)
	if !valid || req.Name != nil && name == "" {
		writeError(c.w, http.StatusBadRequest, "invalid", "A plan's name is 1 to 80 characters and its description at most 500.")
		return
	}
	ctx := c.r.Context()
	before, err := s.settings.PlanRules(ctx, p.ID)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	// The keys given are set; a key with null leaves the plan.
	if err := s.settings.SetPlanRules(ctx, p.ID, c.user().ID, req.Rules); err != nil {
		s.failSetting(c, err)
		return
	}
	if req.Name != nil {
		p.Name = name
	}
	if req.Description != nil {
		p.Description = desc
	}
	if err := store.UpdatePlan(ctx, s.opt.Store.Q(), *p); err != nil {
		s.fail(c.w, err)
		return
	}
	after, err := s.settings.PlanRules(ctx, p.ID)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	from, to := map[string]any{}, map[string]any{}
	for k := range req.Rules {
		from[k], to[k] = nilIfMissing(before, k), nilIfMissing(after, k)
	}
	affected := s.settings.TenantsOfPlan(p.ID)
	s.audit(c, auth.AuditEvent{Kind: "plan_changed", Actor: c.user().ID, Detail: map[string]any{"plan": p.Slug, "rules": req.Rules, "from": from, "to": to, "affected": affected}})
	s.evaluateQuotas(ctx, affected)
	r, err := s.toPlan(ctx, *p)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		planResp
		Affected []string `json:"affected"`
	}{r, affected})
}

func nilIfMissing(m map[string]settings.Rule, k string) any {
	if r, ok := m[k]; ok {
		return r
	}
	return nil
}

func (s *server) adminDeletePlan(c *call) {
	p, ok := s.planBySlug(c)
	if !ok {
		return
	}
	ctx := c.r.Context()
	if n := s.settings.PlanTenants()[p.ID]; n > 0 {
		writeJSON(c.w, http.StatusConflict, struct {
			Error   string `json:"error"`
			Message string `json:"message"`
			Tenants int    `json:"tenants"`
		}{"plan_in_use", "Tenants use this plan; give them another first.", n})
		return
	}
	rules, _ := s.settings.PlanRules(ctx, p.ID)
	if err := store.DeletePlan(ctx, s.opt.Store.Q(), p.ID); err != nil {
		s.fail(c.w, err)
		return
	}
	s.settings.ForgetPlan()
	s.audit(c, auth.AuditEvent{Kind: "plan_deleted", Actor: c.user().ID, Detail: map[string]any{"plan": p.Slug, "rules": rules, "affected": []string{}}})
	c.w.WriteHeader(http.StatusNoContent)
}

// adminPutTenantPlan gives a tenant a plan, or none, and clears its undated
// manual rules, which were adjustments to the old plan; a manual rule with an
// end is a time-limited grant and stays. The answer lists both.
func (s *server) adminPutTenantPlan(c *call) {
	var req struct {
		Plan    *string `json:"plan"`
		Confirm string  `json:"confirm"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	t, ok := s.visibleTenant(c)
	if !ok {
		return
	}
	ctx := c.r.Context()
	var plan *store.Plan
	if req.Plan != nil {
		p, err := store.GetPlanBySlug(ctx, s.opt.Store.Q(), *req.Plan)
		if errors.Is(err, store.ErrNotFound) {
			writeError(c.w, http.StatusBadRequest, "invalid", "There is no plan "+*req.Plan+".")
			return
		}
		if err != nil {
			s.fail(c.w, err)
			return
		}
		plan = p
	}
	before, err := s.planRef(ctx, t.ID)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	id := ""
	if plan != nil {
		id = plan.ID
	}
	cleared, kept, err := s.settings.SetTenantPlan(ctx, t.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		tenantNotFound(c)
		return
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	var from, to any
	if before != nil {
		from = before.Slug
	}
	if plan != nil {
		to = plan.Slug
	}
	s.audit(c, auth.AuditEvent{Kind: "tenant_plan_changed", Actor: c.user().ID, Tenant: t.ID, Detail: map[string]any{"tenant": t.ID, "from": from, "to": to, "cleared": cleared, "kept": kept}})
	s.evaluateQuotas(ctx, []string{t.ID})
	ref, err := s.planRef(ctx, t.ID)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, map[string]any{"plan": ref, "cleared": cleared, "kept": kept})
}
