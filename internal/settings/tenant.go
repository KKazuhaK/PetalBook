package settings

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"petalbook.app/server/internal/store"
)

// A tenant's settings come from these rows per key, each optional:
//
//   - the site's row (layer site, layer_id ""): the site's value and lower for
//     everyone;
//   - the site's rules for this tenant (layer site, layer_id the tenant): at
//     most one per source, manual, billing, and the tenant's plan's (layer plan);
//     each may have a window. ruleOf picks the one in effect now: the first
//     active one, in that order. A whole rule replaces the one below it;
//   - the tenant's row (layer tenant): its own value and lower.
//
// Strongest first: the site's lock over everyone > the rule in effect for the
// tenant (a lock forces its value, a restriction bounds the tenant's) > the
// tenant's value > the rule's value > the site's value. The value in effect is
// then held to the restriction the tenant's layer meets (Lower.Nearest), so
// "at most 10" for a tenant is at most 10 whatever the site's value is.

// ruleView is a rule from its row; nil when the row holds neither a value nor a
// lower.
func ruleView(d *Def, r *store.LayeredRow) (*RuleView, error) {
	if r == nil || (r.Value == "" && r.Lower == "") {
		return nil, nil
	}
	rv := &RuleView{Source: Source(sourceOf(r)), SetBy: r.SetBy}
	rv.From, rv.Until = r.Starts, r.Ends
	if r.Value != "" {
		v, err := decode(r.Value)
		if err != nil {
			return nil, err
		}
		if d.Kind != Doc && v != nil {
			if nv, err := d.Value(v); err == nil {
				v = nv
			}
		}
		rv.Value = v
	}
	if r.Lower != "" {
		var l Lower
		if err := json.Unmarshal([]byte(r.Lower), &l); err != nil {
			return nil, err
		}
		rv.Lower = &l
	}
	return rv, nil
}

func sourceOf(r *store.LayeredRow) string {
	if r.Source == "" {
		return store.SourceManual
	}
	return r.Source
}

// ruleOf marks, among a tenant's rules for one key in the order manual,
// billing, plan, the one in effect at now (the first whose window holds now,
// InEffect unless the site's lock over everyone beats it) and the one under it
// (Underlying), and answers the earliest start or end of a window after now in
// Unix milliseconds (math.MaxInt64 when none). The views are copied.
func ruleOf(rules []*RuleView, siteLocked bool, now time.Time) (out []RuleView, chosen *RuleView, next int64) {
	next = math.MaxInt64
	out = make([]RuleView, 0, len(rules))
	for _, r := range rules {
		if r != nil {
			out = append(out, *r)
		}
	}
	seen := 0
	for i := range out {
		r := &out[i]
		r.InEffect, r.Underlying = false, false
		for _, b := range [2]time.Time{r.From, r.Until} {
			if !b.IsZero() && b.After(now) && b.UnixMilli() < next {
				next = b.UnixMilli()
			}
		}
		if !r.active(now) {
			continue
		}
		switch seen {
		case 0:
			r.InEffect, chosen = !siteLocked, r
		case 1:
			r.Underlying = true
		}
		seen++
	}
	return out, chosen, next
}

func tenantViewOf(d *Def, r *store.LayeredRow) (*TenantView, error) {
	tv := &TenantView{InEffect: true}
	if r == nil || !d.Has(Tenant) {
		return tv, nil
	}
	if r.Value != "" {
		v, err := decode(r.Value)
		if err != nil {
			return nil, err
		}
		if d.Kind != Doc && v != nil {
			if nv, err := d.Value(v); err == nil {
				v = nv
			}
		}
		tv.Value = v
	}
	if r.Lower != "" {
		var l Lower
		if err := json.Unmarshal([]byte(r.Lower), &l); err != nil {
			return nil, err
		}
		tv.Lower = &l
	}
	return tv, nil
}

// rulesByKey groups the site's rules for one tenant by key: the manual rule at
// index 0, the billing rule at index 1.
func rulesByKey(rows []store.LayeredRow) map[string]*[2]*store.LayeredRow {
	m := make(map[string]*[2]*store.LayeredRow, len(rows))
	for i := range rows {
		r := &rows[i]
		p := m[r.Key]
		if p == nil {
			p = &[2]*store.LayeredRow{}
			m[r.Key] = p
		}
		switch sourceOf(r) {
		case store.SourceManual:
			p[0] = r
		case store.SourceBilling:
			p[1] = r
		}
	}
	return m
}

// computeTenant reads the tenant's rows over the site's snapshot and the
// tenant's plan. A setting the tenant has no row of, and the site no rule for
// it, is the entry the site's snapshot computed for every such tenant; a tenant
// with no rows at all shares the site's map, or its plan's. It also answers
// the earliest start or end of a window after now (Unix milliseconds).
func (s *Service) computeTenant(ctx context.Context, tenant string, now time.Time) (map[string]*Effective, int64, error) {
	sp, err := s.snapshotOf(ctx, "")
	if err != nil {
		return nil, 0, err
	}
	q := s.st.Q()
	ruleRows, err := store.ListLayered(ctx, q, string(Site), tenant)
	if err != nil {
		return nil, 0, err
	}
	ownRows, err := store.ListLayered(ctx, q, string(Tenant), tenant)
	if err != nil {
		return nil, 0, err
	}
	var plan map[string]*store.LayeredRow
	var planID string
	var planGen uint64
	if ref, ok := s.st.TenantRefOf(tenant); ok && ref.PlanID != "" {
		ps, err := s.planRows(ctx)
		if err != nil {
			return nil, 0, err
		}
		planID, plan, planGen = ref.PlanID, ps.rows[ref.PlanID], ps.gen
		if s.afterPlanRows != nil {
			s.afterPlanRows()
		}
	}
	if len(ruleRows) == 0 && len(ownRows) == 0 {
		if len(plan) == 0 {
			return sp.all, math.MaxInt64, nil
		}
		m, err := s.sharedPlan(ctx, sp, planID, planGen, plan, now)
		return m, math.MaxInt64, err
	}
	rules, own := rulesByKey(ruleRows), rowsByKey(ownRows)
	out := make(map[string]*Effective, len(s.defs))
	next := int64(math.MaxInt64)
	for _, d := range s.defs {
		if !d.PerTenant() {
			out[d.Key] = sp.m[d.Key]
			continue
		}
		mr, pr, tr := rules[d.Key], plan[d.Key], own[d.Key]
		if mr == nil && pr == nil && (tr == nil || !d.Has(Tenant)) {
			out[d.Key] = sp.follow[d.Key]
			continue
		}
		var views []*RuleView
		if mr != nil {
			for _, r := range mr {
				v, err := ruleView(d, r)
				if err != nil {
					return nil, 0, err
				}
				views = append(views, v)
			}
		}
		pv, err := ruleView(d, pr)
		if err != nil {
			return nil, 0, err
		}
		views = append(views, pv)
		tv, err := tenantViewOf(d, tr)
		if err != nil {
			return nil, 0, err
		}
		e, nx := tenantEffective(d, sp.m[d.Key].Site, views, tv, now)
		out[d.Key] = e
		next = min(next, nx)
	}
	return out, next, nil
}

// sharedPlan is the map of every tenant that has the plan and no rows of its
// own: built once for the site's snapshot and the plan's generation. gen is
// the generation the rows plan were read at (planSnapshot.gen), not the one
// now: a plan written since then must not find its old rows under its new
// generation.
func (s *Service) sharedPlan(ctx context.Context, sp *snapshot, planID string, gen uint64, plan map[string]*store.LayeredRow, now time.Time) (map[string]*Effective, error) {
	if v, ok := sp.plans.Load(planID); ok && v.(*sharedPlanMap).gen == gen {
		return v.(*sharedPlanMap).m, nil
	}
	out := make(map[string]*Effective, len(s.defs))
	for _, d := range s.defs {
		pr := plan[d.Key]
		if !d.PerTenant() || pr == nil {
			out[d.Key] = sp.all[d.Key]
			continue
		}
		pv, err := ruleView(d, pr)
		if err != nil {
			return nil, err
		}
		out[d.Key], _ = tenantEffective(d, sp.m[d.Key].Site, []*RuleView{pv}, nil, now)
	}
	sp.plans.Store(planID, &sharedPlanMap{gen: gen, m: out})
	return out, nil
}

type sharedPlanMap struct {
	gen uint64
	m   map[string]*Effective
}

// tenantEffective is a setting as a tenant sees it, from the site's layer, the
// rules for the tenant (manual, billing, plan; nil entries are none) and the
// tenant's own layer (nil when it has no row). It also answers the earliest
// start or end of a rule's window after now, in Unix milliseconds.
func tenantEffective(d *Def, site SiteView, rules []*RuleView, tv *TenantView, now time.Time) (*Effective, int64) {
	if tv == nil {
		tv = &TenantView{InEffect: true}
	}
	e := &Effective{Def: d, Site: site, Value: site.Value, Above: site.Lower, Source: Site, Tenant: tv}
	views, chosen, next := ruleOf(rules, site.Lower.Mode == Lock, now)
	if len(views) > 0 {
		tv.Rules = views
		if next != math.MaxInt64 {
			tv.Next = time.UnixMilli(next).UTC()
		}
	}
	if chosen != nil {
		tv.SiteRule = chosen
		tv.SiteLocked = chosen.Lower != nil && chosen.Lower.Mode == Lock
		if chosen.InEffect {
			if chosen.Value != nil {
				e.Value = chosen.Value
			}
			if chosen.Lower != nil {
				e.Above = *chosen.Lower
			}
		}
	}
	if tv.Value != nil {
		tv.InEffect = e.Above.Allows(d, tv.Value)
		if tv.InEffect {
			e.Value, e.Source = tv.Value, Tenant
		}
	}
	// A restriction bounds the value in effect too: the site's value (or its
	// value for the tenant) outside it gives way to the nearest it allows.
	e.Value = e.Above.Nearest(d, e.Value)
	e.Below = e.Above
	if tv.Lower != nil && e.Above.Mode != Lock {
		e.Below = e.Above.Combine(d, *tv.Lower)
	}
	switch {
	case e.Above.Mode == Lock:
		e.LockedBy = Site
	case e.Below.Mode == Lock:
		e.LockedBy = Tenant
	}
	return e, next
}

// TenantResult is what SetTenant did besides writing.
type TenantResult struct {
	// UndatedReplaced is true when a dated manual rule replaced an undated one of
	// the same key: there is one manual rule per tenant and key, so when the
	// window ends the value is not the undated one but what is below it
	// (billing, the plan, the site).
	UndatedReplaced bool
}

// SetTenant changes a tenant's layer, or the site's rule for the tenant.
func (s *Service) SetTenant(ctx context.Context, tenant, key string, ch Change) error {
	_, err := s.SetTenantResult(ctx, tenant, key, ch)
	return err
}

// ruleRow checks a rule and makes its row of the layer, layer id and source.
func (s *Service) ruleRow(ctx context.Context, d *Def, layer, layerID, tenant, source, by string, r *Rule) (*store.LayeredRow, error) {
	row := &store.LayeredRow{Layer: layer, LayerID: layerID, Key: d.Key, Source: source, SetBy: by, Updated: s.now()}
	if r == nil {
		return row, nil
	}
	if !r.From.IsZero() && !r.Until.IsZero() && !r.Until.After(r.From) {
		return nil, bad("%s: until is not after from", d.Key)
	}
	if !r.Until.IsZero() && !r.Until.After(s.now()) {
		return nil, &Error{Code: CodeUntilPast, Msg: "The rule for " + d.Key + " ends at " + r.Until.UTC().Format(time.RFC3339) +
			", which has already passed: give a later until, or none."}
	}
	row.Starts, row.Ends = r.From.UTC(), r.Until.UTC()
	if r.Value != nil {
		v, err := s.check(ctx, d, tenant, r.Value)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		row.Value = string(raw)
	}
	if r.Lower != nil {
		l, err := d.CheckLower(*r.Lower)
		if err != nil {
			return nil, err
		}
		raw, _ := json.Marshal(l)
		row.Lower = string(raw)
	}
	return row, nil
}

// SetTenantResult is SetTenant with what else it did.
//
// The tenant's layer is held to Effective.Above: locked is setting_locked (for
// a site administrator too: lift the lock first), a value or rule outside it is
// setting_restricted. The site's rule for the tenant (Change.SiteRule, or its
// short form SiteLock) is a site administrator's only, and is not held to the
// site's lower for tenants: it is the site's own choice for that tenant. It is
// the tenant's manual rule: the rules of billing and of the plan are not
// changed here. A setting with no tenant layer (ForTenant) has only the rule; a
// value given for it is the site's value for that tenant.
func (s *Service) SetTenantResult(ctx context.Context, tenant, key string, ch Change) (TenantResult, error) {
	var res TenantResult
	d, ok := s.byK[key]
	if !ok || !d.PerTenant() {
		return res, &Error{Code: CodeNotFound, Msg: "no such setting"}
	}
	eff, err := s.Get(ctx, tenant, key)
	if err != nil {
		return res, err
	}
	// The manual rule now, and the rules as they are.
	var cur *Rule
	var others []*RuleView
	if eff.Tenant != nil {
		for i := range eff.Tenant.Rules {
			rv := &eff.Tenant.Rules[i]
			if rv.Source == FromManual {
				r := rv.Rule
				cur = &r
			} else {
				others = append(others, rv)
			}
		}
	}
	if (ch.HasSiteLock || ch.HasSiteRule) && !ch.Site {
		return res, &Error{Code: CodeLocked, Msg: key + ": only the site's administrators set a rule for a tenant"}
	}
	if ch.HasSiteLock && ch.HasSiteRule {
		return res, bad("%s: give siteRule or siteLock, not both", key)
	}
	siteOnly := !d.Has(Tenant)
	if siteOnly {
		if !ch.Site {
			return res, &Error{Code: CodeLocked, Msg: key + " is the site's: only its administrators set it for a tenant"}
		}
		if ch.HasLower {
			return res, bad("%s has no tenant layer: give the rule for the tenant as siteRule", key)
		}
	}
	// siteLock is the short form of a rule: true locks the value given, or
	// without one the value in effect now (the tenant's own, the rule's, or
	// the site's as a restriction bounds it), so a lock never changes what the
	// tenant sees; false removes the manual rule. Without a tenant layer a value
	// is the site's value for the tenant.
	if ch.HasSiteLock {
		ch.HasSiteRule = true
		ch.SiteRule = nil
		if ch.SiteLock {
			r := &Rule{Lower: &Lower{Mode: Lock}, Value: eff.Value}
			if ch.HasValue {
				r.Value, ch.HasValue, ch.Value = ch.Value, false, nil
			}
			ch.SiteRule = r
		}
	}
	if siteOnly && ch.HasValue {
		r := &Rule{Value: ch.Value}
		if ch.HasSiteRule && ch.SiteRule != nil {
			r.Lower, r.From, r.Until = ch.SiteRule.Lower, ch.SiteRule.From, ch.SiteRule.Until
		} else if cur != nil {
			r.Lower, r.From, r.Until = cur.Lower, cur.From, cur.Until
		}
		ch.HasSiteRule, ch.SiteRule, ch.HasValue, ch.Value = true, r, false, nil
	}
	if !ch.Site && eff.Tenant != nil && eff.Tenant.SiteLocked && eff.Tenant.SiteRule.InEffect {
		return res, &Error{Code: CodeLocked, Msg: key + " is locked for this tenant by the site's administrators"}
	}

	// The site's manual rule for the tenant.
	var ruleRow *store.LayeredRow
	if ch.HasSiteRule {
		if ruleRow, err = s.ruleRow(ctx, d, store.LayeredSite, tenant, tenant, store.SourceManual, ch.By, ch.SiteRule); err != nil {
			return res, err
		}
	}

	// The tenant's own layer, held to the rule above it as it will be.
	above := eff.Above
	if ch.HasSiteRule {
		var nv *RuleView
		if ruleRow.Value != "" || ruleRow.Lower != "" {
			if nv, err = ruleView(d, ruleRow); err != nil {
				return res, err
			}
		}
		views := append([]*RuleView{nv}, others...)
		e, _ := tenantEffective(d, eff.Site, views, nil, s.now())
		above = e.Above
		// The rule's value is held to the restriction it sets for the tenant.
		if nv != nil && nv.Value != nil {
			lim := above
			if nv.Lower != nil {
				lim = *nv.Lower
			}
			if err := aboveMax(d, lim, nv.Value); err != nil {
				return res, err
			}
		}
	}
	var rawValue, rawLower string
	if ch.HasValue && ch.Value != nil {
		if above.Mode == Lock {
			return res, &Error{Code: CodeLocked, Msg: key + " is locked by the site's administrators"}
		}
		val, err := s.check(ctx, d, tenant, ch.Value)
		if err != nil {
			return res, err
		}
		if !above.Allows(d, val) {
			return res, &Error{Code: CodeRestricted, Msg: key + ": " + describe(d, above)}
		}
		raw, err := json.Marshal(val)
		if err != nil {
			return res, err
		}
		rawValue = string(raw)
	}
	if ch.HasLower && ch.Lower != nil {
		l, err := d.CheckLower(*ch.Lower)
		if err != nil {
			return res, err
		}
		if above.Mode == Lock && l.Mode != Lock {
			return res, &Error{Code: CodeLocked, Msg: key + " is locked by the site's administrators"}
		}
		if !l.Tightens(d, above) {
			return res, &Error{Code: CodeRestricted, Msg: key + ": the tenant's rule must be at least as strict as the site's"}
		}
		raw, _ := json.Marshal(l)
		rawLower = string(raw)
	}

	err = s.write(ctx, func(q store.Q) error {
		if s.beforeTenantWrite != nil && !ch.Site {
			s.beforeTenantWrite()
		}
		if ch.HasValue || ch.HasLower {
			// The tenant's row and the site's rule are separate rows: a write of
			// one never changes the other, whatever the cache said.
			if err := store.PutLayeredFields(ctx, q, store.LayeredRow{Layer: string(Tenant), LayerID: tenant, Key: key,
				Value: rawValue, Lower: rawLower, SetBy: ch.By, Updated: s.now()}, ch.HasValue, ch.HasLower); err != nil {
				return err
			}
		}
		if ruleRow != nil {
			if ruleRow.Value == "" && ruleRow.Lower == "" {
				// No rule: no row, as for a tenant the site never had one for.
				return store.DeleteLayered(ctx, q, string(Site), tenant, key, store.SourceManual)
			}
			if !ruleRow.Starts.IsZero() || !ruleRow.Ends.IsZero() {
				old, err := store.GetLayered(ctx, q, string(Site), tenant, key, store.SourceManual)
				if err != nil && !errors.Is(err, store.ErrNotFound) {
					return err
				}
				res.UndatedReplaced = err == nil && old.Starts.IsZero() && old.Ends.IsZero()
			}
			return store.PutLayered(ctx, q, *ruleRow)
		}
		return nil
	})
	s.InvalidateTenant(tenant)
	if ruleRow != nil {
		s.rearm(ctx)
	}
	return res, err
}
