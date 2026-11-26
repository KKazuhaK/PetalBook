package settings

import (
	"context"
	"encoding/json"
	"errors"
	"math"
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
	// Get reads the site value; Set writes it (inside the transaction q). Both
	// nil: the value is stored in layered_settings as usual, and the binding
	// only checks it (Check) and follows it (After).
	Get func(ctx context.Context, q store.Q) (any, error)
	Set func(ctx context.Context, q store.Q, v any) error
	// After runs inside the transaction of a change of the site's value, once
	// the value is written: old is the value in effect before, new the one
	// written (nil for a limit that was set to none). The code that keeps
	// something in step with a setting (the storage policy's "since", a secret
	// that belongs to a provider) does it here, in the same transaction.
	After func(ctx context.Context, q store.Q, old, new any) error
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

	// afterPlanRows, set by tests only, runs when a tenant's computation has
	// read the plans' rules, before it uses them.
	afterPlanRows func()

	// boundaryFault, set by tests only, makes the timer's reads fail while it
	// answers true.
	boundaryFault func() bool

	ceiling atomic.Int64 // --max-upload, see SetFileCeiling

	// The cache. A site write bumps siteGen, which every snapshot is checked
	// against; a tenant write bumps only that tenant's slot; a write of a plan's
	// rules bumps planGen. A snapshot is current while the generations it was
	// computed at are, and while now is before its next: the start or end of a
	// window of one of its rules.
	siteGen atomic.Uint64
	planGen atomic.Uint64
	serials atomic.Uint64 // numbers the snapshots (see Site)
	slots   sync.Map      // tenant id ("" for the site alone) -> *slot

	// plans is every rule of every plan, read again when planGen moves.
	planMu   sync.Mutex
	planSnap atomic.Pointer[planSnapshot]

	// Changes made by another process (the command line, a standby server) are
	// seen through settings_rev, read at most once per RevCheck, on a request.
	revCheck  time.Duration
	lastCheck atomic.Int64 // Unix ms of the last read
	seenRev   atomic.Int64

	// The timer of the windows' starts and ends (see Watch).
	tmu       sync.Mutex
	watching  bool
	onBound   func(context.Context, []Boundary)
	handled   time.Time
	armed     stopper
	afterFunc func(d time.Duration, f func()) stopper
}

type stopper interface{ Stop() bool }

// RevCheck is how often a busy process reads settings_rev.
const RevCheck = 60 * time.Second

// planSnapshot is the rules of the plans, by plan id and key.
type planSnapshot struct {
	gen  uint64
	rows map[string]map[string]*store.LayeredRow
}

// slot is one tenant's cache entry. It lives as long as the service (about a
// hundred bytes a tenant with the map's own); its snapshot is replaced, never
// changed.
type slot struct {
	gen  atomic.Uint64
	snap atomic.Pointer[snapshot]
}

type snapshot struct {
	site, tenant, plan uint64
	serial             uint64
	// next is the earliest start or end of a window of a rule in m after the
	// snapshot was made, in Unix milliseconds (math.MaxInt64: none): the
	// snapshot is current only before it.
	next int64
	m    map[string]*Effective
	// In the site's snapshot only: follow is what a tenant with no row for a
	// setting sees of it, shared by every such tenant; all is the whole map of a
	// tenant with no rows at all; plans are the maps of the tenants that have a
	// plan and no rows of their own, by plan id.
	follow, all map[string]*Effective
	plans       sync.Map
}

// Of returns the settings of a store: one for every package that reads them, so
// that a change made through any of them drops the one cache. Tests of the
// package make their own with New.
func Of(st *store.Store) *Service {
	return st.Shared("settings", func() any { return New(st) }).(*Service)
}

// New makes the service over the Definitions.
func New(st *store.Store) *Service {
	s := &Service{st: st, byK: map[string]*Def{}, bind: map[string]SiteBinding{}, now: time.Now, revCheck: RevCheck,
		afterFunc: func(d time.Duration, f func()) stopper { return time.AfterFunc(d, f) }}
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

// SetDefault makes v the value of a setting until a site administrator sets one
// (a value the server was started with, such as the session lifetimes). v is
// checked like any value; it panics on a key or value that is not one, since
// that is a mistake of the program.
func (s *Service) SetDefault(key string, v any) {
	d, ok := s.byK[key]
	if !ok {
		panic("settings: no setting " + key)
	}
	nv, err := d.Value(v)
	if err != nil {
		panic("settings: default of " + key + ": " + err.Error())
	}
	d.Default = nv
	s.Invalidate()
}

// Site is the site's own settings (as For does for "") with a number that is
// different for every set of values the service has made: the code that works
// something out from them (the sign-in policy, the storage policy) keeps its
// result with the number, and works it out again only when the number moves.
func (s *Service) Site(ctx context.Context) (map[string]*Effective, uint64, error) {
	p, err := s.snapshotOf(ctx, "")
	if err != nil {
		return nil, 0, err
	}
	return p.m, p.serial, nil
}

// SetClock sets the clock (tests).
func (s *Service) SetClock(now func() time.Time) {
	s.now = now
	s.lastCheck.Store(0) // the revision is looked at again, by the new clock
}

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
	// SiteRule is the rule in effect for this one tenant (the first of Rules
	// whose window holds now); nil when none is.
	SiteRule *RuleView `json:"-"`
	// Rules are the tenant's rules for the setting, in the order manual, billing,
	// plan: at most three, one per source.
	Rules []RuleView `json:"rules,omitempty"`
	// Next is the earliest start or end of a window of one of Rules after now.
	Next time.Time `json:"next,omitzero"`
}

// The sources of a rule, strongest first.
const (
	FromManual  Source = "manual"
	FromBilling Source = "billing"
	FromPlan    Source = "plan"
)

// Source says where a rule for a tenant comes from.
type Source string

// Rule is the site's layer for one tenant: a value the site gives this tenant
// in place of its own (nil: the site's), and the rule the tenant's layer is
// held to in place of the site's lower (nil: the site's). A lock is how the
// site forces a value for one tenant ("this organisation does not have Y" is
// Y locked off for it), a restriction how it bounds one ("at most 10 spaces").
// It is the site's choice for that tenant, so it is not held to the site's
// lower for tenants; only the site's lock over everyone beats it.
//
// A rule may have a window: it counts from From up to, not including, Until
// (the zero time is none).
type Rule struct {
	Value any       `json:"value"`
	Lower *Lower    `json:"lower"`
	From  time.Time `json:"from,omitzero"`
	Until time.Time `json:"until,omitzero"`
}

// RuleView is a Rule as a tenant sees it, with its source. InEffect is true for
// the rule applied; it is false for a rule the window of which does not hold
// now, for one an earlier rule covers, and while the site's lock over everyone
// beats it (kept, and back when that lock is lifted). Underlying is the rule
// that applies when the one in effect ends or is removed.
type RuleView struct {
	Rule
	Source     Source `json:"source"`
	SetBy      string `json:"setBy,omitempty"`
	InEffect   bool   `json:"inEffect"`
	Underlying bool   `json:"underlying,omitempty"`
}

// active says whether the rule's window holds at now.
func (r *RuleView) active(now time.Time) bool {
	return (r.From.IsZero() || !now.Before(r.From)) && (r.Until.IsZero() || now.Before(r.Until))
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
	now := s.now()
	s.checkRev(ctx, now)
	site, gen := s.siteGen.Load(), sl.gen.Load()
	var plan uint64 // the site's own snapshot does not depend on the plans
	if tenant != "" {
		plan = s.planGen.Load()
	}
	if p := sl.snap.Load(); p != nil && p.site == site && p.tenant == gen && p.plan == plan && (p.next == math.MaxInt64 || now.UnixMilli() < p.next) {
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
		p, err := s.computeSite(ctx, now)
		if err != nil {
			return nil, err
		}
		p.site, p.tenant, p.plan, p.next = site, gen, plan, math.MaxInt64
		p.serial = s.serials.Add(1)
		sl.snap.Store(p)
		return p, nil
	}
	m, next, err := s.computeTenant(ctx, tenant, now)
	if err != nil {
		return nil, err
	}
	p := &snapshot{site: site, tenant: gen, plan: plan, next: next, m: m}
	sl.snap.Store(p)
	return p, nil
}

// checkRev reads settings_rev when RevCheck has passed since the last time, one
// request paying for it, and drops every cached value when a write of another
// process moved it: the site's and the tenants' (siteGen), the plans' rules
// (planGen), and the tenants in memory, whose PlanID a change of plan there
// moved (read again before the generations move, so a recomputation sees it).
func (s *Service) checkRev(ctx context.Context, now time.Time) {
	t, last := now.UnixMilli(), s.lastCheck.Load()
	if t-last < s.revCheck.Milliseconds() || !s.lastCheck.CompareAndSwap(last, t) {
		return
	}
	rev, err := store.SettingsRev(ctx, s.st.Q())
	if err != nil {
		return
	}
	if old := s.seenRev.Swap(rev); old != rev && last != 0 {
		if err := s.st.TenantStateReload(ctx); err != nil {
			s.seenRev.CompareAndSwap(rev, old) // seen at the next check instead
		}
		s.planGen.Add(1)
		s.siteGen.Add(1)
	}
}

// write runs a change of layered_settings in a transaction that also counts it
// in settings_rev, which is how another process sees it.
func (s *Service) write(ctx context.Context, fn func(q store.Q) error) error {
	var rev int64
	err := s.st.Tx(ctx, func(q store.Q) error {
		if err := fn(q); err != nil {
			return err
		}
		var err error
		rev, err = store.BumpSettingsRev(ctx, q)
		return err
	})
	if err == nil {
		// Our own count is not a change by another process (unless one slipped in
		// between: then the revision is further on and the next check sees it).
		s.seenRev.CompareAndSwap(rev-1, rev)
	}
	return err
}

// planRows is every plan's rules, read when a plan changed.
func (s *Service) planRows(ctx context.Context) (*planSnapshot, error) {
	gen := s.planGen.Load()
	if p := s.planSnap.Load(); p != nil && p.gen == gen {
		return p, nil
	}
	s.planMu.Lock()
	defer s.planMu.Unlock()
	if p := s.planSnap.Load(); p != nil && p.gen == gen {
		return p, nil
	}
	rows, err := store.ListLayeredLayer(ctx, s.st.Q(), store.LayeredPlan)
	if err != nil {
		return nil, err
	}
	p := &planSnapshot{gen: gen, rows: map[string]map[string]*store.LayeredRow{}}
	for i := range rows {
		r := &rows[i]
		if p.rows[r.LayerID] == nil {
			p.rows[r.LayerID] = map[string]*store.LayeredRow{}
		}
		p.rows[r.LayerID][r.Key] = r
	}
	s.planSnap.Store(p)
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
func (s *Service) computeSite(ctx context.Context, now time.Time) (*snapshot, error) {
	q := s.st.Q()
	rows, err := store.ListLayered(ctx, q, string(Site), "")
	if err != nil {
		return nil, err
	}
	site := rowsByKey(rows)
	p := &snapshot{m: make(map[string]*Effective, len(s.defs)), follow: map[string]*Effective{}, all: make(map[string]*Effective, len(s.defs))}
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
			p.follow[d.Key], _ = tenantEffective(d, e.Site, nil, nil, now)
			p.all[d.Key] = p.follow[d.Key]
		} else {
			p.all[d.Key] = e
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
	bound = bound && b.Get != nil
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
	// By is who makes the change (a user id), kept with the rows it writes.
	By string
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
	err = s.write(ctx, func(q store.Q) error {
		row, err := store.GetLayered(ctx, q, string(Site), "", key, store.SourceManual)
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
		var before any
		if ch.HasValue {
			b, bound := s.bind[key]
			if bound && b.After != nil {
				v, err := s.siteView(ctx, q, d, row)
				if err != nil {
					return err
				}
				before = v.Value
			}
			if bound && b.Set != nil {
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
		row.Updated, row.SetBy = s.now(), ch.By
		if err := store.PutLayered(ctx, q, *row); err != nil {
			return err
		}
		if b := s.bind[key]; ch.HasValue && b.After != nil {
			if err := b.After(ctx, q, before, val); err != nil {
				return err
			}
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
