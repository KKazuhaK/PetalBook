package settings

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
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

	mu      sync.Mutex
	ceiling int64 // --max-upload, see SetFileCeiling
	gen     uint64
	cache   map[string]*cached // by tenant id ("" for the site alone)
}

type cached struct {
	gen uint64
	m   map[string]*Effective
}

// New makes the service over the Definitions.
func New(st *store.Store) *Service {
	s := &Service{st: st, byK: map[string]*Def{}, bind: map[string]SiteBinding{}, now: time.Now, cache: map[string]*cached{}}
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
	s.mu.Lock()
	s.ceiling = max
	s.mu.Unlock()
	s.Invalidate()
}

// FileLimit is files.maxBytes in effect for a tenant: the settings cache's
// value (one map lookup), at most the ceiling. 0 when no ceiling was set.
func (s *Service) FileLimit(ctx context.Context, tenant string) int64 {
	s.mu.Lock()
	ceil := s.ceiling
	s.mu.Unlock()
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
func (s *Service) Invalidate() {
	s.mu.Lock()
	s.gen++
	s.mu.Unlock()
}

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
	// SiteLocked is true when a site administrator set this value for the tenant
	// and locked it: the tenant's administrators cannot change the value or the
	// rule below it. It beats the site's own value, and the site's lock over
	// everyone beats it.
	SiteLocked bool `json:"siteLocked"`
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
}

// For returns every setting as the tenant sees it ("" for the site alone). It
// is cached by (tenant, generation); a request reads one map.
func (s *Service) For(ctx context.Context, tenant string) (map[string]*Effective, error) {
	s.mu.Lock()
	if c := s.cache[tenant]; c != nil && c.gen == s.gen {
		m := c.m
		s.mu.Unlock()
		return m, nil
	}
	gen := s.gen
	s.mu.Unlock()
	m, err := s.compute(ctx, tenant)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.gen == gen {
		if len(s.cache) > 2000 {
			s.cache = map[string]*cached{}
		}
		s.cache[tenant] = &cached{gen: gen, m: m}
	}
	s.mu.Unlock()
	return m, nil
}

// Get is For for one key.
func (s *Service) Get(ctx context.Context, tenant, key string) (*Effective, error) {
	m, err := s.For(ctx, tenant)
	if err != nil {
		return nil, err
	}
	e := m[key]
	if e == nil {
		return nil, &Error{CodeNotFound, "no such setting"}
	}
	return e, nil
}

func (s *Service) compute(ctx context.Context, tenant string) (map[string]*Effective, error) {
	q := s.st.Q()
	siteRows, err := store.ListLayered(ctx, q, string(Site), "")
	if err != nil {
		return nil, err
	}
	var tenantRows []store.LayeredRow
	if tenant != "" {
		if tenantRows, err = store.ListLayered(ctx, q, string(Tenant), tenant); err != nil {
			return nil, err
		}
	}
	site, ten := rowsByKey(siteRows), rowsByKey(tenantRows)
	out := make(map[string]*Effective, len(s.defs))
	for _, d := range s.defs {
		e := &Effective{Def: d}
		var err error
		if e.Site, err = s.siteView(ctx, q, d, site[d.Key]); err != nil {
			return nil, err
		}
		e.Value, e.Below, e.Source = e.Site.Value, e.Site.Lower, Site
		if tenant != "" && d.Has(Tenant) {
			tv := &TenantView{InEffect: true}
			if r := ten[d.Key]; r != nil {
				if r.Value != "" {
					if tv.Value, err = decode(r.Value); err != nil {
						return nil, err
					}
					if d.Kind != Doc && tv.Value != nil {
						if nv, err := d.Value(tv.Value); err == nil {
							tv.Value = nv
						}
					}
					tv.InEffect = tv.Value == nil || e.Site.Lower.Allows(d, tv.Value)
					// A value the site set for this tenant is not the tenant's to
					// have forbidden by the site's rule for tenants, only by its lock
					// over everyone.
					if r.SiteLocked && tv.Value != nil {
						tv.SiteLocked = true
						tv.InEffect = e.Site.Lower.Mode != Lock
					}
				}
				if r.Lower != "" {
					var l Lower
					if err := json.Unmarshal([]byte(r.Lower), &l); err != nil {
						return nil, err
					}
					tv.Lower = &l
				}
			}
			e.Tenant = tv
			if tv.Value != nil && tv.InEffect {
				e.Value, e.Source = tv.Value, Tenant
			}
			if tv.Lower != nil {
				e.Below = e.Site.Lower.Combine(d, *tv.Lower)
			}
			if tv.SiteLocked && tv.InEffect {
				e.Below = Lower{Mode: Lock}
			}
		}
		switch {
		case e.Site.Lower.Mode == Lock:
			e.LockedBy = Site
		case e.Tenant != nil && e.Tenant.SiteLocked && e.Tenant.InEffect:
			e.LockedBy = Site
		case e.Below.Mode == Lock:
			e.LockedBy = Tenant
		}
		out[d.Key] = e
	}
	return out, nil
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
		if err := json.Unmarshal([]byte(r.Lower), &v.Lower); err != nil {
			return v, err
		}
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
	// SiteLock lock (or free) the tenant's value for the tenant: only a site
	// administrator may, and only a value that is there can be locked.
	Site, HasSiteLock, SiteLock bool
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
		return 0, &Error{CodeNotFound, "no such setting"}
	}
	var val any
	if ch.HasValue {
		b, bound := s.bind[key]
		if bound && b.ReadOnly != nil && b.ReadOnly() {
			return 0, &Error{CodeConflict, key + " is set by a command-line flag; change the flag"}
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

// SetTenant changes a tenant's layer. The site's rule decides: locked is
// setting_locked (for a site administrator too: unlock at the site first), a
// value or rule outside it is setting_restricted. A value a site administrator
// locked for this tenant (Change.SiteLock) is the site's to change: the tenant's
// own administrators get setting_locked, and what the site writes there is not
// held to the site's rule for tenants, since it is the site's own choice.
func (s *Service) SetTenant(ctx context.Context, tenant, key string, ch Change) error {
	d, ok := s.byK[key]
	if !ok || !d.Has(Tenant) {
		return &Error{CodeNotFound, "no such setting"}
	}
	eff, err := s.Get(ctx, tenant, key)
	if err != nil {
		return err
	}
	site := eff.Site.Lower
	cur := eff.Tenant
	if !ch.Site {
		if ch.HasSiteLock {
			return &Error{CodeLocked, key + ": only the site's administrators lock a value for a tenant"}
		}
		if cur != nil && cur.SiteLocked {
			return &Error{CodeLocked, key + " is locked for this tenant by the site's administrators"}
		}
	}
	// Whether the tenant's value ends up locked by the site, and what it is.
	locking := cur != nil && cur.SiteLocked
	if ch.HasSiteLock {
		locking = ch.SiteLock
	}
	final := any(nil)
	if cur != nil {
		final = cur.Value
	}
	if ch.HasValue {
		final = ch.Value
	}
	if final == nil {
		if ch.HasSiteLock && ch.SiteLock {
			return bad("%s: a lock for the tenant needs a value to lock", key)
		}
		locking = false
	}
	forced := ch.Site && locking
	var val any
	if ch.HasValue && ch.Value != nil {
		if site.Mode == Lock && !forced {
			return &Error{CodeLocked, key + " is locked by the site's administrators"}
		}
		if val, err = s.check(ctx, d, tenant, ch.Value); err != nil {
			return err
		}
		if !forced && !site.Allows(d, val) {
			return &Error{CodeRestricted, key + ": " + describe(d, site)}
		}
	}
	var lower *Lower
	if ch.HasLower && ch.Lower != nil {
		l, err := d.CheckLower(*ch.Lower)
		if err != nil {
			return err
		}
		if !forced {
			if site.Mode == Lock && l.Mode != Lock {
				return &Error{CodeLocked, key + " is locked by the site's administrators"}
			}
			if !l.Tightens(d, site) {
				return &Error{CodeRestricted, key + ": the tenant's rule must be at least as strict as the site's"}
			}
		}
		lower = &l
	}
	err = s.st.Tx(ctx, func(q store.Q) error {
		row, err := store.GetLayered(ctx, q, string(Tenant), tenant, key)
		if errors.Is(err, store.ErrNotFound) {
			row, err = &store.LayeredRow{Layer: string(Tenant), LayerID: tenant, Key: key}, nil
		}
		if err != nil {
			return err
		}
		// The lock is read again here, not taken from the cached view: a lock written
		// since then (or a stale cache) must still refuse a tenant administrator.
		if !ch.Site && row.SiteLocked {
			return &Error{CodeLocked, key + " is locked for this tenant by the site's administrators"}
		}
		if ch.HasValue {
			row.Value = ""
			if ch.Value != nil {
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
		if ch.Site {
			row.SiteLocked = locking // only a site administrator's change touches the lock
		}
		row.Updated = s.now()
		return store.PutLayered(ctx, q, *row)
	})
	s.Invalidate()
	return err
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
