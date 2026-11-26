package settings

import (
	"context"
	"encoding/json"

	"petalbook.app/server/internal/store"
)

// A tenant's settings come from three rows per key, each optional:
//
//   - the site's row (layer site, layer_id ""): the site's value and lower for
//     everyone;
//   - the site's row for this tenant (layer site, layer_id the tenant): the
//     site's Rule for this one tenant;
//   - the tenant's row (layer tenant): its own value and lower.
//
// Strongest first: the site's lock over everyone > the site's rule for the
// tenant (a lock forces its value, a restriction bounds the tenant's) > the
// tenant's value > the site's value for the tenant > the site's value.
//
// The site's rule for a tenant is where a plan, a bill or a time window will
// come from: siteRuleOf is the one place that picks the rule in effect from the
// tenant's site rows, so a source (manual > billing > plan) and a window
// (starts, ends) are added there and in the row, and the precedence above does
// not change.

// siteRuleOf is the site's rule for the tenant in effect now, from its row;
// nil when there is none.
func siteRuleOf(d *Def, r *store.LayeredRow) (*Rule, error) {
	if r == nil || (r.Value == "" && r.Lower == "") {
		return nil, nil
	}
	rule := &Rule{}
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
		rule.Value = v
	}
	if r.Lower != "" {
		var l Lower
		if err := json.Unmarshal([]byte(r.Lower), &l); err != nil {
			return nil, err
		}
		rule.Lower = &l
	}
	return rule, nil
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

// computeTenant reads the tenant's rows over the site's snapshot. A setting
// the tenant has no row of, and the site no rule for it, is the entry the
// site's snapshot computed for every such tenant.
func (s *Service) computeTenant(ctx context.Context, tenant string) (map[string]*Effective, error) {
	sp, err := s.snapshotOf(ctx, "")
	if err != nil {
		return nil, err
	}
	q := s.st.Q()
	ruleRows, err := store.ListLayered(ctx, q, string(Site), tenant)
	if err != nil {
		return nil, err
	}
	ownRows, err := store.ListLayered(ctx, q, string(Tenant), tenant)
	if err != nil {
		return nil, err
	}
	rules, own := rowsByKey(ruleRows), rowsByKey(ownRows)
	out := make(map[string]*Effective, len(s.defs))
	for _, d := range s.defs {
		if !d.PerTenant() {
			out[d.Key] = sp.m[d.Key]
			continue
		}
		rr, tr := rules[d.Key], own[d.Key]
		if rr == nil && (tr == nil || !d.Has(Tenant)) {
			out[d.Key] = sp.follow[d.Key]
			continue
		}
		rule, err := siteRuleOf(d, rr)
		if err != nil {
			return nil, err
		}
		tv, err := tenantViewOf(d, tr)
		if err != nil {
			return nil, err
		}
		out[d.Key] = tenantEffective(d, sp.m[d.Key].Site, rule, tv)
	}
	return out, nil
}

// tenantEffective is a setting as a tenant sees it, from the site's layer, the
// site's rule for the tenant (nil when none) and the tenant's own layer (nil
// when it has no row).
func tenantEffective(d *Def, site SiteView, rule *Rule, tv *TenantView) *Effective {
	if tv == nil {
		tv = &TenantView{InEffect: true}
	}
	e := &Effective{Def: d, Site: site, Value: site.Value, Above: site.Lower, Source: Site, Tenant: tv}
	if rule != nil {
		rv := &RuleView{Rule: *rule, InEffect: site.Lower.Mode != Lock}
		tv.SiteRule = rv
		tv.SiteLocked = rule.Lower != nil && rule.Lower.Mode == Lock
		if rv.InEffect {
			if rule.Value != nil {
				e.Value = rule.Value
			}
			if rule.Lower != nil {
				e.Above = *rule.Lower
			}
		}
	}
	if tv.Value != nil {
		tv.InEffect = e.Above.Allows(d, tv.Value)
		if tv.InEffect {
			e.Value, e.Source = tv.Value, Tenant
		}
	}
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
	return e
}

// SetTenant changes a tenant's layer, or the site's rule for the tenant.
//
// The tenant's layer is held to Effective.Above: locked is setting_locked (for
// a site administrator too: lift the lock first), a value or rule outside it is
// setting_restricted. The site's rule for the tenant (Change.SiteRule, or its
// short form SiteLock) is a site administrator's only, and is not held to the
// site's lower for tenants: it is the site's own choice for that tenant. A
// setting with no tenant layer (ForTenant) has only the site's rule; a value
// given for it is the site's value for that tenant.
func (s *Service) SetTenant(ctx context.Context, tenant, key string, ch Change) error {
	d, ok := s.byK[key]
	if !ok || !d.PerTenant() {
		return &Error{Code: CodeNotFound, Msg: "no such setting"}
	}
	eff, err := s.Get(ctx, tenant, key)
	if err != nil {
		return err
	}
	var cur *Rule
	if eff.Tenant != nil && eff.Tenant.SiteRule != nil {
		r := eff.Tenant.SiteRule.Rule
		cur = &r
	}
	if (ch.HasSiteLock || ch.HasSiteRule) && !ch.Site {
		return &Error{Code: CodeLocked, Msg: key + ": only the site's administrators set a rule for a tenant"}
	}
	if ch.HasSiteLock && ch.HasSiteRule {
		return bad("%s: give siteRule or siteLock, not both", key)
	}
	siteOnly := !d.Has(Tenant)
	if siteOnly {
		if !ch.Site {
			return &Error{Code: CodeLocked, Msg: key + " is the site's: only its administrators set it for a tenant"}
		}
		if ch.HasLower {
			return bad("%s has no tenant layer: give the rule for the tenant as siteRule", key)
		}
	}
	// siteLock is the short form of a rule: true locks the value given (or the
	// rule's own value, or the site's), false removes the rule. Without a tenant
	// layer a value is the site's value for the tenant.
	if ch.HasSiteLock {
		ch.HasSiteRule = true
		ch.SiteRule = nil
		if ch.SiteLock {
			r := &Rule{Lower: &Lower{Mode: Lock}}
			if cur != nil {
				r.Value = cur.Value
			}
			if ch.HasValue {
				r.Value, ch.HasValue, ch.Value = ch.Value, false, nil
			}
			ch.SiteRule = r
		}
	}
	if siteOnly && ch.HasValue {
		r := &Rule{Value: ch.Value}
		if ch.HasSiteRule && ch.SiteRule != nil {
			r.Lower = ch.SiteRule.Lower
		} else if cur != nil {
			r.Lower = cur.Lower
		}
		ch.HasSiteRule, ch.SiteRule, ch.HasValue, ch.Value = true, r, false, nil
	}
	if !ch.Site && eff.Tenant != nil && eff.Tenant.SiteLocked && eff.Tenant.SiteRule.InEffect {
		return &Error{Code: CodeLocked, Msg: key + " is locked for this tenant by the site's administrators"}
	}

	// The site's rule for the tenant.
	var ruleRow *store.LayeredRow
	if ch.HasSiteRule {
		ruleRow = &store.LayeredRow{Layer: string(Site), LayerID: tenant, Key: key, Updated: s.now()}
		if r := ch.SiteRule; r != nil {
			if r.Value != nil {
				v, err := s.check(ctx, d, tenant, r.Value)
				if err != nil {
					return err
				}
				raw, err := json.Marshal(v)
				if err != nil {
					return err
				}
				ruleRow.Value = string(raw)
			}
			if r.Lower != nil {
				l, err := d.CheckLower(*r.Lower)
				if err != nil {
					return err
				}
				raw, _ := json.Marshal(l)
				ruleRow.Lower = string(raw)
			}
		}
	}

	// The tenant's own layer, held to the rule above it as it will be.
	above := eff.Above
	if ch.HasSiteRule {
		var r *Rule
		if ruleRow.Value != "" || ruleRow.Lower != "" {
			if r, err = siteRuleOf(d, ruleRow); err != nil {
				return err
			}
		}
		above = tenantEffective(d, eff.Site, r, nil).Above
	}
	var rawValue, rawLower string
	if ch.HasValue && ch.Value != nil {
		if above.Mode == Lock {
			return &Error{Code: CodeLocked, Msg: key + " is locked by the site's administrators"}
		}
		val, err := s.check(ctx, d, tenant, ch.Value)
		if err != nil {
			return err
		}
		if !above.Allows(d, val) {
			return &Error{Code: CodeRestricted, Msg: key + ": " + describe(d, above)}
		}
		raw, err := json.Marshal(val)
		if err != nil {
			return err
		}
		rawValue = string(raw)
	}
	if ch.HasLower && ch.Lower != nil {
		l, err := d.CheckLower(*ch.Lower)
		if err != nil {
			return err
		}
		if above.Mode == Lock && l.Mode != Lock {
			return &Error{Code: CodeLocked, Msg: key + " is locked by the site's administrators"}
		}
		if !l.Tightens(d, above) {
			return &Error{Code: CodeRestricted, Msg: key + ": the tenant's rule must be at least as strict as the site's"}
		}
		raw, _ := json.Marshal(l)
		rawLower = string(raw)
	}

	err = s.st.Tx(ctx, func(q store.Q) error {
		if s.beforeTenantWrite != nil && !ch.Site {
			s.beforeTenantWrite()
		}
		if ch.HasValue || ch.HasLower {
			// The tenant's row and the site's rule are separate rows: a write of
			// one never changes the other, whatever the cache said.
			if _, err := store.PutLayeredUnlocked(ctx, q, store.LayeredRow{Layer: string(Tenant), LayerID: tenant, Key: key,
				Value: rawValue, Lower: rawLower, Updated: s.now()}, ch.HasValue, ch.HasLower); err != nil {
				return err
			}
		}
		if ruleRow != nil {
			return store.PutLayered(ctx, q, *ruleRow)
		}
		return nil
	})
	s.InvalidateTenant(tenant)
	return err
}
