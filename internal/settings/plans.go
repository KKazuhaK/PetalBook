package settings

import (
	"context"
	"slices"
	"strings"

	"petalbook.app/server/internal/store"
)

// A plan is a bundle of rules: its rules are rows of layered_settings with
// layer plan and source plan, and a tenant that has the plan has them as the
// lowest source of its own rules (see ruleOf). A plan's rule has no window.

// BillingWriter is how the internal billing module writes rules, with the
// source billing (docs/api.md, "The billing boundary"). A nil rule removes
// the billing rule of the key. The rules are checked as a manual rule is; a
// manual rule of the same key wins while its window holds. *Service
// implements it.
type BillingWriter interface {
	SetBilling(ctx context.Context, tenant, by string, rules map[string]*Rule) error
}

var _ BillingWriter = (*Service)(nil)

// SetBilling writes the billing rules of a tenant in one transaction. Nothing is
// written when one is invalid.
func (s *Service) SetBilling(ctx context.Context, tenant, by string, rules map[string]*Rule) error {
	if _, ok := s.st.TenantRefOf(tenant); !ok {
		return store.ErrNotFound
	}
	rows := make([]*store.LayeredRow, 0, len(rules))
	for key, r := range rules {
		d, ok := s.byK[key]
		if !ok || !d.PerTenant() {
			return &Error{Code: CodeNotFound, Msg: key + ": no such setting"}
		}
		// A feature the tenant uses is not switched off by billing: nobody is
		// there to confirm what would stop (UseGuard).
		if g := s.guard.Load(); g != nil && d.Kind == Bool {
			if off, err := s.BillingSwitchesOff(ctx, tenant, key, r); err != nil {
				return err
			} else if off {
				if err := (*g)(ctx, tenant, key); err != nil {
					return err
				}
			}
		}
		row, err := s.ruleRow(ctx, d, store.LayeredSite, tenant, tenant, store.SourceBilling, by, r)
		if err != nil {
			return err
		}
		if err := s.checkRuleMax(d, r); err != nil {
			return err
		}
		rows = append(rows, row)
	}
	err := s.write(ctx, func(q store.Q) error {
		for _, row := range rows {
			var err error
			if row.Value == "" && row.Lower == "" {
				err = store.DeleteLayered(ctx, q, row.Layer, row.LayerID, row.Key, row.Source)
			} else {
				err = store.PutLayered(ctx, q, *row)
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
	s.InvalidateTenant(tenant)
	s.rearm(ctx)
	return err
}

// checkRuleMax checks a plan's or billing's rule: a limit with a tenant layer
// needs a maximum or a lock (setting_rule_needs_max), and a value above the
// maximum the rule itself holds the tenant to is refused.
func (s *Service) checkRuleMax(d *Def, r *Rule) error {
	// A limit the tenant also sets: a plan's or billing's value alone would be
	// a default the tenant's own value ranks above, so it must cap (as the
	// limits sheet's storage.maxFileBytes and storage.perPerson.* did). A
	// manual rule may still be a default.
	if r != nil && r.Value != nil && d.Kind == Limit && d.Has(Tenant) && (r.Lower == nil || r.Lower.Mode == Allow) {
		return &Error{Code: CodeRuleNeedsMax, Msg: "A plan or billing rule for " + d.Key + " must set a maximum or a lock."}
	}
	if r != nil && r.Value != nil && r.Lower != nil {
		l, err := d.CheckLower(*r.Lower)
		if err != nil {
			return err
		}
		return aboveMax(d, l, r.Value)
	}
	return nil
}

// PlanRules is the rules of a plan by key, from memory.
func (s *Service) PlanRules(ctx context.Context, planID string) (map[string]Rule, error) {
	ps, err := s.planRows(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]Rule{}
	for key, row := range ps.rows[planID] {
		d, ok := s.byK[key]
		if !ok {
			continue
		}
		rv, err := ruleView(d, row)
		if err != nil {
			return nil, err
		}
		if rv != nil {
			out[key] = rv.Rule
		}
	}
	return out, nil
}

// SetPlanRules writes the rules of a plan: a key with a rule is set, a key with
// nil leaves the plan. Each is checked as a rule for one tenant is. Nothing is
// written when one is invalid. The tenants that have the plan see the change at
// their next read.
func (s *Service) SetPlanRules(ctx context.Context, planID, by string, rules map[string]*Rule) error {
	rows := make([]*store.LayeredRow, 0, len(rules))
	for key, r := range rules {
		d, ok := s.byK[key]
		if !ok || !d.PerTenant() {
			return bad("%s is not a setting a plan can have a rule for", key)
		}
		if r != nil && (!r.From.IsZero() || !r.Until.IsZero()) {
			return bad("%s: a plan's rule has no dates", key)
		}
		row, err := s.ruleRow(ctx, d, store.LayeredPlan, planID, "", store.SourcePlan, by, r)
		if err != nil {
			return err
		}
		if err := s.checkRuleMax(d, r); err != nil {
			return err
		}
		rows = append(rows, row)
	}
	for _, t := range s.TenantsOfPlan(planID) {
		if err := s.checkPlanFor(ctx, t, rules); err != nil {
			return err
		}
	}
	err := s.write(ctx, func(q store.Q) error {
		for _, row := range rows {
			var err error
			if row.Value == "" && row.Lower == "" {
				err = store.DeleteLayered(ctx, q, row.Layer, row.LayerID, row.Key, row.Source)
			} else {
				err = store.PutLayered(ctx, q, *row)
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
	s.planGen.Add(1)
	return err
}

// checkPlanFor runs the bindings' checks of a plan's values for one tenant on
// it (signin.ssoOnly needs a single sign-on the tenant's people can use): a
// plan is no tenant's, so its values are checked for each, never as the
// site's.
func (s *Service) checkPlanFor(ctx context.Context, tenant string, rules map[string]*Rule) error {
	keys := make([]string, 0, len(rules))
	for k := range rules {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		r, d := rules[k], s.byK[k]
		if r == nil || r.Value == nil || d == nil {
			continue
		}
		v, err := s.checkShape(ctx, d, tenant, r.Value)
		if err != nil {
			return err
		}
		if _, err := s.checkBound(ctx, d, tenant, v); err != nil {
			return err
		}
	}
	return nil
}

// ForgetPlan tells the service that a plan and its rules were deleted
// (store.DeletePlan).
func (s *Service) ForgetPlan() { s.planGen.Add(1); s.siteGen.Add(1) }

// PlanTenants says how many tenants have each plan, by plan id, from memory.
func (s *Service) PlanTenants() map[string]int {
	n := map[string]int{}
	for _, id := range s.st.TenantIDs() {
		if r, ok := s.st.TenantRefOf(id); ok && r.PlanID != "" {
			n[r.PlanID]++
		}
	}
	return n
}

// TenantsOfPlan lists the ids of the tenants that have a plan, sorted.
func (s *Service) TenantsOfPlan(planID string) []string {
	out := []string{}
	for _, id := range s.st.TenantIDs() {
		if r, ok := s.st.TenantRefOf(id); ok && r.PlanID == planID {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// KeyedRule is a rule of a tenant with the key it is for.
type KeyedRule struct {
	Key string `json:"key"`
	RuleView
}

// SetTenantPlan gives a tenant a plan ("" for none) and clears every manual
// rule of the tenant, dated or not: they were adjustments to the old plan
// (docs/api.md, "Plans"; the owner's decision of 2026-11-26). The billing
// rules stay, and so does the tenant's audit switch (StampNewTenant: it is the
// site's decision about the tenant, not an adjustment to a plan). It answers the
// rules it cleared.
func (s *Service) SetTenantPlan(ctx context.Context, tenant, planID string) (cleared []KeyedRule, err error) {
	if _, ok := s.st.TenantRefOf(tenant); !ok {
		return nil, store.ErrNotFound
	}
	// The plan's values are checked for the tenant it is given to.
	if planID != "" {
		rules, err := s.PlanRules(ctx, planID)
		if err != nil {
			return nil, err
		}
		ptrs := make(map[string]*Rule, len(rules))
		for k, r := range rules {
			ptrs[k] = &r
		}
		if err := s.checkPlanFor(ctx, tenant, ptrs); err != nil {
			return nil, err
		}
	}
	var gone []store.LayeredRow
	err = s.write(ctx, func(q store.Q) error {
		if err := store.SetTenantPlan(ctx, q, tenant, planID); err != nil {
			return err
		}
		var err error
		gone, err = store.DeleteManualRules(ctx, q, tenant, FeatureTenantAudit)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := s.st.TenantStateReload(ctx); err != nil {
		return nil, err
	}
	s.InvalidateTenant(tenant)
	s.rearm(ctx)
	return s.keyed(gone), nil
}

// keyed makes the rules of rows, sorted by key.
func (s *Service) keyed(rows []store.LayeredRow) []KeyedRule {
	out := []KeyedRule{}
	for i := range rows {
		d, ok := s.byK[rows[i].Key]
		if !ok {
			continue
		}
		if rv, err := ruleView(d, &rows[i]); err == nil && rv != nil {
			out = append(out, KeyedRule{rows[i].Key, *rv})
		}
	}
	slices.SortFunc(out, func(a, b KeyedRule) int { return strings.Compare(a.Key, b.Key) })
	return out
}

// On says whether a feature (a boolean setting) is on for a tenant, which is
// what internal/authz asks: one cached read. A key that is not a boolean
// setting, or a setting that cannot be read, is on: only what a feature gates
// can be switched off.
func (s *Service) On(ctx context.Context, tenant, feature string) bool {
	on, err := s.Feature(ctx, tenant, feature)
	return on || err != nil
}

// Feature is On with the failure of the read told: a caller that must not run
// what is off when it cannot know (a route's gate) refuses on the error.
func (s *Service) Feature(ctx context.Context, tenant, feature string) (bool, error) {
	e, err := s.Get(ctx, tenant, feature)
	if err != nil {
		return true, err
	}
	on, ok := e.Value.(bool)
	return on || !ok, nil
}
