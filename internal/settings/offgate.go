package settings

import (
	"context"
	"slices"
	"time"

	"petalbook.app/server/internal/store"
)

// What a change would switch off (docs/api.md, change_capabilities): for a
// boolean setting and a tenant, whether the change leaves it off at some
// moment from now on at which it is on as things stand. The moments are now
// and every start and end of a rule's window, before the change and after it:
// the value only changes there, so a rule dated to switch it off later, or a
// change that leaves it to go off when a rule holding it on ends, is seen.
// Nothing is written: the inputs of the tenant's value are made as the change
// would leave them and evaluated with tenantEffective.

// inputs are what a tenant's value of a setting is made of: the site's layer,
// the tenant's rules (manual, billing, plan; nil entries are none) and its own
// layer (nil without one).
type inputs struct {
	site  SiteView
	rules []*RuleView
	own   *TenantView
}

// inputsOf are a tenant's inputs for d as they are.
func (s *Service) inputsOf(ctx context.Context, tenant string, d *Def) (inputs, error) {
	eff, err := s.Get(ctx, tenant, d.Key)
	if err != nil {
		return inputs{}, err
	}
	in := inputs{site: eff.Site}
	if eff.Tenant != nil {
		for i := range eff.Tenant.Rules {
			rv := eff.Tenant.Rules[i]
			in.rules = append(in.rules, &rv)
		}
		if d.Has(Tenant) {
			in.own = &TenantView{InEffect: true, Value: eff.Tenant.Value, Lower: eff.Tenant.Lower}
		}
	}
	return in, nil
}

// at is the value the inputs give at t.
func (in inputs) at(d *Def, t time.Time) any {
	var own *TenantView
	if in.own != nil {
		own = &TenantView{InEffect: true, Value: in.own.Value, Lower: in.own.Lower}
	}
	e, _ := tenantEffective(d, in.site, in.rules, own, t)
	return e.Value
}

// without is the rules but those of a source.
func (in inputs) without(src Source) inputs {
	out := in
	out.rules = nil
	for _, r := range in.rules {
		if r != nil && r.Source != src {
			out.rules = append(out.rules, r)
		}
	}
	return out
}

// with is the rules with r (nil: none) of its source in place of the one there
// was, in the order manual, billing, plan.
func (in inputs) with(src Source, r *Rule) inputs {
	out := in.without(src)
	if r != nil {
		out.rules = append(out.rules, &RuleView{Rule: *r, Source: src})
	}
	order := map[Source]int{FromManual: 0, FromBilling: 1, FromPlan: 2}
	slices.SortStableFunc(out.rules, func(a, b *RuleView) int { return order[a.Source] - order[b.Source] })
	return out
}

// lost says whether a boolean that is on before at some moment from now is
// not on after at the same moment.
func lost(d *Def, before, after inputs, now time.Time) bool {
	times := []time.Time{now}
	for _, in := range []inputs{before, after} {
		for _, r := range in.rules {
			if r == nil {
				continue
			}
			for _, b := range []time.Time{r.From, r.Until} {
				if b.After(now) {
					times = append(times, b)
				}
			}
		}
	}
	for _, t := range times {
		if before.at(d, t) == true && after.at(d, t) != true {
			return true
		}
	}
	return false
}

func (s *Service) boolDef(key string) (*Def, error) {
	d, ok := s.byK[key]
	if !ok || !d.PerTenant() || d.Kind != Bool {
		return nil, &Error{Code: CodeNotFound, Msg: "no such feature"}
	}
	return d, nil
}

// SiteSwitchesOff lists those of the tenants a change of the site's layer
// would switch the feature off for, now or later. A change SetSite would
// refuse answers that refusal.
func (s *Service) SiteSwitchesOff(ctx context.Context, key string, ch Change, tenants []string) ([]string, error) {
	d, err := s.boolDef(key)
	if err != nil {
		return nil, err
	}
	site, err := s.siteAfter(ctx, d, ch)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, t := range tenants {
		before, err := s.inputsOf(ctx, t, d)
		if err != nil {
			return nil, err
		}
		after := before
		after.site = site
		if lost(d, before, after, s.now()) {
			out = append(out, t)
		}
	}
	return out, nil
}

// TenantSwitchesOff says whether a change of the tenant's layer or of the
// site's rule for it would switch the feature off, now or later. A change
// SetTenantResult would refuse answers that refusal.
func (s *Service) TenantSwitchesOff(ctx context.Context, tenant, key string, ch Change) (bool, error) {
	d, err := s.boolDef(key)
	if err != nil {
		return false, err
	}
	before, err := s.inputsOf(ctx, tenant, d)
	if err != nil {
		return false, err
	}
	_, after, err := s.setTenant(ctx, tenant, key, ch, true)
	if err != nil {
		return false, err
	}
	return lost(d, before, *after, s.now()), nil
}

// PlanSwitchesOff lists those of the tenants the plan's rules (rules: what
// SetPlanRules would write; a key that is not there keeps its rule) would
// switch the feature off for. With assign, the tenants are given the plan
// (SetTenantPlan): their manual rules go too.
func (s *Service) PlanSwitchesOff(ctx context.Context, key, planID string, rules map[string]*Rule, assign bool, tenants []string) ([]string, error) {
	d, err := s.boolDef(key)
	if err != nil {
		return nil, err
	}
	var rule *Rule
	r, given := rules[key]
	switch {
	case given:
		rule = r
	default:
		pr, err := s.PlanRules(ctx, planID)
		if err != nil {
			return nil, err
		}
		if v, ok := pr[key]; ok && planID != "" {
			rule = &v
		}
	}
	var out []string
	for _, t := range tenants {
		before, err := s.inputsOf(ctx, t, d)
		if err != nil {
			return nil, err
		}
		after := before.with(FromPlan, rule)
		if assign {
			after = after.without(FromManual)
		}
		if lost(d, before, after, s.now()) {
			out = append(out, t)
		}
	}
	return out, nil
}

// BillingSwitchesOff says whether a billing rule (nil: none) would switch the
// feature off for the tenant.
func (s *Service) BillingSwitchesOff(ctx context.Context, tenant, key string, r *Rule) (bool, error) {
	d, err := s.boolDef(key)
	if err != nil {
		return false, err
	}
	before, err := s.inputsOf(ctx, tenant, d)
	if err != nil {
		return false, err
	}
	return lost(d, before, before.with(FromBilling, r), s.now()), nil
}

// UseGuard is a check SetBilling runs for each feature its rules would switch
// off for the tenant: an error (confirm_required) refuses the whole change.
// The billing module has nobody to confirm, so what is in use is refused.
func (s *Service) UseGuard(g func(ctx context.Context, tenant, key string) error) { s.guard.Store(&g) }

// siteAfter is the site's layer of d as the change would leave it, with
// SetSite's checks.
func (s *Service) siteAfter(ctx context.Context, d *Def, ch Change) (SiteView, error) {
	var val any
	var err error
	if ch.HasValue {
		if val, err = s.check(ctx, d, "", ch.Value); err != nil {
			return SiteView{}, err
		}
	}
	q := s.st.Q()
	row, err := store.GetLayered(ctx, q, string(Site), "", d.Key, store.SourceManual)
	if err != nil {
		if !isNotFound(err) {
			return SiteView{}, err
		}
		row = &store.LayeredRow{Layer: string(Site), Key: d.Key}
	}
	r := *row
	if ch.HasValue {
		raw, err := jsonOf(val)
		if err != nil {
			return SiteView{}, err
		}
		r.Value = raw
	}
	if ch.HasLower {
		r.Lower = ""
		if ch.Lower != nil {
			l, err := d.CheckLower(*ch.Lower)
			if err != nil {
				return SiteView{}, err
			}
			raw, _ := jsonOf(l)
			r.Lower = raw
		}
	}
	site, err := s.siteView(ctx, q, d, &r)
	if err != nil {
		return SiteView{}, err
	}
	if b, bound := s.binding(d.Key); ch.HasValue && bound && b.Get != nil {
		site.Value = val // a bound value is read from its binding, which is not written here
	}
	return site, nil
}
