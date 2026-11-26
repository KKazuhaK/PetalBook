package settings

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"petalbook.app/server/internal/store"
)

// SiteBinding lets a setting's site value live somewhere else than
// layered_settings: the daily text's switches are in its own record (2.3), and
// "the same record" is what keeps /api/admin/daily and this API one thing. Its
// Lower still lives here.
type SiteBinding struct {
	// Get reads the site value; Set writes it (inside the transaction q).
	Get func(ctx context.Context, q store.Q) (any, error)
	Set func(ctx context.Context, q store.Q, v any) error
	// Check validates a value of any layer beyond its shape; it may read the
	// database. tenant is the layer's tenant ("" for the site).
	Check func(ctx context.Context, tenant string, v any) (any, error)
	// ReadOnly, when it returns true, makes the site value one a flag set: it
	// cannot be written (409 conflict).
	ReadOnly func() bool
}

// Service reads and writes the layered settings.
type Service struct {
	st   *store.Store
	defs []*Def
	byK  map[string]*Def
	bind map[string]SiteBinding
	now  func() time.Time

	// beforeTenantWrite, set by tests only, runs in a tenant administrator's
	// transaction just before its write.
	beforeTenantWrite func()

	// onCompute, set by tests only, runs each time a tenant's settings are read
	// from the database.
	onCompute func(tenant string)

	ceiling atomic.Int64 // --max-upload, see SetFileCeiling

	// The cache. A site write bumps siteGen, which every snapshot is checked
	// against; a tenant write bumps only that tenant's slot. A snapshot is
	// current while both generations it was computed at are.
	siteGen atomic.Uint64
	slots   sync.Map // tenant id ("" for the site alone) -> *slot
}

// slot is one tenant's cache entry. It lives as long as the service (about a
// hundred bytes a tenant with the map's own); its snapshot is replaced, never
// changed.
type slot struct {
	gen  atomic.Uint64
	snap atomic.Pointer[snapshot]
}

type snapshot struct {
	site, tenant uint64
	m            map[string]*Effective
	// follow, in the site's snapshot only, is what a tenant with no row for a
	// setting sees of it: shared by every such tenant.
	follow map[string]*Effective
}

// New makes the service over the Definitions.
func New(st *store.Store) *Service {
	s := &Service{st: st, byK: map[string]*Def{}, bind: map[string]SiteBinding{}, now: time.Now}
	s.defs = Definitions()
	for _, d := range s.defs {
		s.byK[d.Key] = d
	}
	return s
}

// SetFileCeiling makes max (--max-upload) the site's value of files.maxBytes,
// which no request can change, and the largest value a tenant may set: the
// site's lower rule is "restrict, at most max" until a site administrator
// says otherwise. Whatever the rules are, the value in effect never goes
// above max (see FileLimit).
func (s *Service) SetFileCeiling(max int64) {
	d := s.byK[FilesMaxBytes]
	d.Default = max
	d.Lower = Lower{Mode: Restrict, Max: &max}
	s.Bind(FilesMaxBytes, SiteBinding{
		Get:      func(context.Context, store.Q) (any, error) { return max, nil },
		Set:      func(context.Context, store.Q, any) error { return errors.New("set by --max-upload") },
		ReadOnly: func() bool { return true },
	})
	s.ceiling.Store(max)
	s.Invalidate()
}

// FileLimit is files.maxBytes in effect for a tenant: the settings cache's
// value (one map lookup), at most the ceiling. 0 when no ceiling was set.
func (s *Service) FileLimit(ctx context.Context, tenant string) int64 {
	ceil := s.ceiling.Load()
	e, err := s.Get(ctx, tenant, FilesMaxBytes)
	if err != nil {
		return ceil
	}
	n, ok := e.Value.(int64)
	if !ok || n <= 0 || (ceil > 0 && n > ceil) {
		return ceil
	}
	return n
}

// Defs lists the definitions in order.
func (s *Service) Defs() []*Def { return s.defs }

// Def returns the definition of a key.
func (s *Service) Def(key string) (*Def, bool) { d, ok := s.byK[key]; return d, ok }

// Bind attaches a site binding to a key.
func (s *Service) Bind(key string, b SiteBinding) {
	if _, ok := s.byK[key]; !ok {
		panic("settings: no setting " + key)
	}
	s.bind[key] = b
}

// SetClock sets the clock (tests).
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// Invalidate drops every cached effective value; the code that changes a bound
// site value (or anything the settings read) calls it after the change.
func (s *Service) Invalidate() { s.siteGen.Add(1) }

// InvalidateTenant drops the cached effective values of one tenant; the
// others keep theirs.
func (s *Service) InvalidateTenant(tenant string) {
	if v, ok := s.slots.Load(tenant); ok {
		v.(*slot).gen.Add(1)
	}
}

// Forget drops the cache entry of a tenant that was deleted.
func (s *Service) Forget(tenant string) { s.slots.Delete(tenant) }

// SiteView is the site's layer of a setting.
type SiteView struct {
	Value    any   `json:"value"`
	Lower    Lower `json:"lower"`
	FromFlag bool  `json:"fromFlag,omitempty"`
}

// TenantView is a tenant's layer of a setting.
type TenantView struct {
	// Value is nil when the tenant follows the site.
	Value any `json:"value"`
	// Lower is what the tenant lets spaces and people do; nil follows the site's.
	Lower *Lower `json:"lower"`
	// InEffect is false while the site's lock or restriction forbids the value
	// (kept, and in effect again when the rule is lifted).
	InEffect bool `json:"inEffect"`
	// SiteLocked is true when the site's rule for this tenant (SiteRule) is a
	// lock: the tenant's administrators cannot change the value or the rule
	// below it.
	SiteLocked bool `json:"siteLocked"`
	// SiteRule is the site's layer for this one tenant; nil when the site has
	// none for it.
	SiteRule *RuleView `json:"siteRule,omitempty"`
}

// Rule is the site's layer for one tenant: a value the site gives this tenant
// in place of its own (nil: the site's), and the rule the tenant's layer is
// held to in place of the site's lower (nil: the site's). A lock is how the
// site forces a value for one tenant ("this organisation does not have Y" is
// Y locked off for it), a restriction how it bounds one ("at most 10 spaces").
// It is the site's choice for that tenant, so it is not held to the site's
// lower for tenants; only the site's lock over everyone beats it.
type Rule struct {
	Value any    `json:"value"`
	Lower *Lower `json:"lower"`
}

// RuleView is a Rule as a tenant sees it; InEffect is false while the site's
// lock over everyone beats it (kept, and back when that lock is lifted).
type RuleView struct {
	Rule
	InEffect bool `json:"inEffect"`
}

// Effective is a setting as one tenant sees it.
type Effective struct {
	Def    *Def
	Site   SiteView
	Tenant *TenantView // nil when the setting has no tenant layer
	// Value is the value in effect for the tenant.
	Value any
	// Below is what the layers under the tenant (space, person) may do: the
	// stricter of the site's and the tenant's rules.
	Below Lower
	// Source is the layer the value in effect comes from (site or tenant), and
	// LockedBy the layer whose lock keeps the layers below from changing it (""
	// when none does): what a client names when it greys a control out.
	Source, LockedBy Layer
	// Above is the rule the tenant's layer is held to: the site's lower, or the
	// site's rule for this tenant when it has one in effect.
	Above Lower
}

// For returns every setting as the tenant sees it ("" for the site alone). It
// is cached per tenant: warm, a request reads one map and two counters, with
// no lock and no query. The map is shared; callers do not change it.
func (s *Service) For(ctx context.Context, tenant string) (map[string]*Effective, error) {
	p, err := s.snapshotOf(ctx, tenant)
	if err != nil {
		return nil, err
	}
	return p.m, nil
}

func (s *Service) snapshotOf(ctx context.Context, tenant string) (*snapshot, error) {
	v, ok := s.slots.Load(tenant)
	if !ok {
		v, _ = s.slots.LoadOrStore(tenant, &slot{})
	}
	sl := v.(*slot)
	site, gen := s.siteGen.Load(), sl.gen.Load()
	if p := sl.snap.Load(); p != nil && p.site == site && p.tenant == gen {
		return p, nil
	}
	// The generations are read before the database: a write that commits while
	// this computes bumps one of them after its commit, so what is stored here
	// is then never taken for current (at worst it costs the next request a
	// computation).
	if s.onCompute != nil {
		s.onCompute(tenant)
	}

	if tenant == "" {
		p, err := s.computeSite(ctx)
		if err != nil {
			return nil, err
		}
		p.site, p.tenant = site, gen
		sl.snap.Store(p)
		return p, nil
	}
	m, err := s.computeTenant(ctx, tenant)
	if err != nil {
		return nil, err
	}
	p := &snapshot{site: site, tenant: gen, m: m}
	sl.snap.Store(p)
	return p, nil
}

// Get is For for one key.
func (s *Service) Get(ctx context.Context, tenant, key string) (*Effective, error) {
	m, err := s.For(ctx, tenant)
	if err != nil {
		return nil, err
	}
	e := m[key]
	if e == nil {
		return nil, &Error{Code: CodeNotFound, Msg: "no such setting"}
	}
	return e, nil
}

// computeSite reads the site's layer: the site's own map (no tenant views),
// and for each setting a tenant may have, what a tenant that has no row for
// it sees. Those are shared by every such tenant, so a tenant's map holds
// entries of its own only for the settings it has rows of.
func (s *Service) computeSite(ctx context.Context) (*snapshot, error) {
	q := s.st.Q()
	rows, err := store.ListLayered(ctx, q, string(Site), "")
	if err != nil {
		return nil, err
	}
	site := rowsByKey(rows)
	p := &snapshot{m: make(map[string]*Effective, len(s.defs)), follow: map[string]*Effective{}}
	for _, d := range s.defs {
		e := &Effective{Def: d}
		if e.Site, err = s.siteView(ctx, q, d, site[d.Key]); err != nil {
			return nil, err
		}
		e.Value, e.Below, e.Above, e.Source = e.Site.Value, e.Site.Lower, e.Site.Lower, Site
		if e.Site.Lower.Mode == Lock {
			e.LockedBy = Site
		}
		p.m[d.Key] = e
		if d.PerTenant() {
			p.follow[d.Key] = tenantEffective(d, e.Site, nil, nil)
		}
	}
	return p, nil
}

func rowsByKey(rows []store.LayeredRow) map[string]*store.LayeredRow {
	m := make(map[string]*store.LayeredRow, len(rows))
	for i := range rows {
		m[rows[i].Key] = &rows[i]
	}
	return m
}

func (s *Service) siteView(ctx context.Context, q store.Q, d *Def, r *store.LayeredRow) (SiteView, error) {
	v := SiteView{Value: d.Default, Lower: d.Lower}
	b, bound := s.bind[d.Key]
	switch {
	case bound:
		val, err := b.Get(ctx, q)
		if err != nil {
			return v, err
		}
		v.Value = val
		v.FromFlag = b.ReadOnly != nil && b.ReadOnly()
	case r != nil && r.Value != "":
		val, err := decode(r.Value)
		if err != nil {
			return v, err
		}
		if d.Kind == Doc {
			v.Value = val
		} else if val, err = d.Value(val); err == nil {
			v.Value = val
		}
	}
	if r != nil && r.Lower != "" {
		// Into a value of its own: v.Lower starts as a copy of d.Lower, whose
		// Max and Options point at memory every request shares (for files.maxBytes,
		// the --max-upload ceiling itself).
		var l Lower
		if err := json.Unmarshal([]byte(r.Lower), &l); err != nil {
			return v, err
		}
		v.Lower = l
	}
	return v, nil
}

// --- writing -------------------------------------------------------------------------

// Change is a request to set a layer: Set says a field is given.
type Change struct {
	HasValue bool
	Value    any
	HasLower bool
	Lower    *Lower // nil: back to the default (site) or to following (tenant)
	// Site says a site administrator makes the change to a tenant's layer;
	// without it the change is a tenant administrator's. HasSiteLock and
	// SiteLock are the short form of a site rule that locks (or of removing the
	// site's rule): with SiteLock, Value is the value locked for the tenant.
	Site, HasSiteLock, SiteLock bool
	// HasSiteRule and SiteRule set (nil: remove) the site's rule for the
	// tenant. Only a site administrator may.
	HasSiteRule bool
	SiteRule    *Rule
}

func (s *Service) check(ctx context.Context, d *Def, tenant string, v any) (any, error) {
	if v == nil && d.Kind != Limit && d.Kind != Doc {
		return nil, bad("%s needs a value", d.Key)
	}
	if v != nil && d.Kind != Doc {
		var err error
		if v, err = d.Value(v); err != nil {
			return nil, err
		}
	}
	if b, ok := s.bind[d.Key]; ok && b.Check != nil && v != nil {
		return b.Check(ctx, tenant, v)
	}
	return v, nil
}

// SetSite changes the site's layer. affected is how many tenants hold a value
// the new rule makes ineffective (0 when lower was not given).
func (s *Service) SetSite(ctx context.Context, key string, ch Change) (affected int, err error) {
	d, ok := s.byK[key]
	if !ok {
		return 0, &Error{Code: CodeNotFound, Msg: "no such setting"}
	}
	var val any
	if ch.HasValue {
		b, bound := s.bind[key]
		if bound && b.ReadOnly != nil && b.ReadOnly() {
			return 0, &Error{Code: CodeConflict, Msg: key + " is set by a command-line flag; change the flag"}
		}
		if val, err = s.check(ctx, d, "", ch.Value); err != nil {
			return 0, err
		}
	}
	var lower *Lower
	if ch.HasLower && ch.Lower != nil {
		l, err := d.CheckLower(*ch.Lower)
		if err != nil {
			return 0, err
		}
		lower = &l
	}
	err = s.st.Tx(ctx, func(q store.Q) error {
		row, err := store.GetLayered(ctx, q, string(Site), "", key)
		if errors.Is(err, store.ErrNotFound) {
			row, err = &store.LayeredRow{Layer: string(Site), Key: key}, nil
		}
		if err != nil {
			return err
		}
		// The value is held to the site's own restriction as it will be.
		if ch.HasValue {
			l := d.Lower
			switch {
			case ch.HasLower && lower != nil:
				l = *lower
			case !ch.HasLower && row.Lower != "":
				// Into a value of its own, as in siteView: d.Lower's Max is shared.
				var rl Lower
				if err := json.Unmarshal([]byte(row.Lower), &rl); err != nil {
					return err
				}
				l = rl
			}
			// null is no limit where a limit may be none, else the default.
			cmp := val
			if cmp == nil && !d.Nullable {
				cmp = d.Default
			}
			if err := aboveMax(d, l, cmp); err != nil {
				return err
			}
		}
		if ch.HasValue {
			if b, bound := s.bind[key]; bound {
				if err := b.Set(ctx, q, val); err != nil {
					return err
				}
			} else {
				raw, err := json.Marshal(val)
				if err != nil {
					return err
				}
				row.Value = string(raw)
			}
		}
		if ch.HasLower {
			row.Lower = ""
			if lower != nil {
				raw, _ := json.Marshal(lower)
				row.Lower = string(raw)
			}
		}
		row.Updated = s.now()
		if err := store.PutLayered(ctx, q, *row); err != nil {
			return err
		}
		if ch.HasLower {
			affected, err = s.affected(ctx, q, d, lower)
		}
		return err
	})
	s.Invalidate()
	return affected, err
}

// affected counts the tenants whose own value the site's new rule forbids, or
// whose own rule is looser than it.
func (s *Service) affected(ctx context.Context, q store.Q, d *Def, lower *Lower) (int, error) {
	if !d.Has(Tenant) {
		return 0, nil
	}
	l := d.Lower
	if lower != nil {
		l = *lower
	}
	rows, err := store.LayeredOfKey(ctx, q, string(Tenant), d.Key)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rows {
		hit := false
		if r.Value != "" {
			if v, err := decode(r.Value); err == nil && v != nil && !l.Allows(d, v) {
				hit = true
			}
		}
		if !hit && r.Lower != "" {
			var tl Lower
			if json.Unmarshal([]byte(r.Lower), &tl) == nil && !tl.Tightens(d, l) {
				hit = true
			}
		}
		if hit {
			n++
		}
	}
	return n, nil
}

func describe(d *Def, l Lower) string {
	switch {
	case l.Mode == Restrict && d.Kind == Limit && l.Max != nil:
		return "at most " + itoa(*l.Max)
	case l.Mode == Restrict && d.Kind == Bool:
		return "it can only be turned off"
	case l.Mode == Restrict:
		return "one of the site's options"
	}
	return "not allowed"
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
