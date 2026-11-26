package limits

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"petalbook.app/server/internal/store"
)

// Source says where a value of the sheet comes from.
type Source string

const (
	// FromSite is the site's value: its default, or what a site administrator
	// set for all tenants.
	FromSite Source = "site"
	// FromPlan is a value of the tenant's plan.
	FromPlan Source = "plan"
	// FromBilling is a value the internal billing module wrote.
	FromBilling Source = "billing"
	// FromManual is a value a site administrator set for this tenant.
	FromManual Source = "manual"
)

// val is one value with its source. A Sheet holds pointers to these, and the
// values that come from the site are one shared copy for every tenant.
type val struct {
	v   any
	src Source
}

// Sheet is a tenant's effective values, computed once and never changed after:
// a recomputation makes a new Sheet. It is safe for any number of readers.
type Sheet struct {
	vals []*val // by position in the registry
}

func (s *Sheet) at(key string) *val {
	if i, ok := index[key]; ok {
		return s.vals[i]
	}
	return nil
}

// Bool is the value of a boolean key; false for any other key.
func (s *Sheet) Bool(key string) bool {
	if v := s.at(key); v != nil {
		b, _ := v.v.(bool)
		return b
	}
	return false
}

// Limit is the value of a limit key; ok is false for null (no limit) and for
// any other key.
func (s *Sheet) Limit(key string) (n int64, ok bool) {
	if v := s.at(key); v != nil {
		n, ok = v.v.(int64)
	}
	return
}

// List is the value of a list key, sorted; nil for any other key. The caller
// must not change it.
func (s *Sheet) List(key string) []string {
	if v := s.at(key); v != nil {
		l, _ := v.v.([]string)
		return l
	}
	return nil
}

// Value is the value of a key (a bool, an int64, nil or a []string) and where
// it comes from; false for a key that is not in the registry.
func (s *Sheet) Value(key string) (any, Source, bool) {
	if v := s.at(key); v != nil {
		return v.v, v.src, true
	}
	return nil, "", false
}

// state is everything a reader needs, replaced as a whole.
type state struct {
	site     *Sheet            // what a tenant without overrides or plan has
	byTenant map[string]*Sheet // never changed after it is published
}

// Change is a new value of a key in a write. A nil *Change removes the value.
// From and Until are zero for none; Until must be after From.
type Change struct {
	Value       json.RawMessage
	From, Until time.Time
}

// BillingWriter is how the internal billing module writes the sheet, with the
// source billing (docs/api.md, "The billing boundary"). It is an interface
// only: nothing implements it yet. The values are checked as SetManual checks
// them; a manual value of the same key still wins.
type BillingWriter interface {
	SetBilling(ctx context.Context, tenant, by string, changes map[string]*Change) error
}

// Invalidator is told that the sheet changed, so that what depends on it (the
// effective settings) is computed again. *settings.Service is one.
type Invalidator interface{ Invalidate() }

// Timer is what Clock.AfterFunc starts.
type Timer interface{ Stop() bool }

// Clock is time, injected so that a test needs no sleeping.
type Clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) Timer
}

type realClock struct{ now func() time.Time }

func (c realClock) Now() time.Time { return c.now() }
func (realClock) AfterFunc(d time.Duration, f func()) Timer {
	return time.AfterFunc(d, f)
}

// RealClock is the system's, or now's when it is not nil.
func RealClock(now func() time.Time) Clock {
	if now == nil {
		now = time.Now
	}
	return realClock{now}
}

// Config is what New needs.
type Config struct {
	Store *store.Store
	// Clock defaults to the system's.
	Clock Clock
	// Invalidate is called after any change of any sheet; may be nil.
	Invalidate Invalidator
	Logger     *slog.Logger
}

// row is an override held in memory: the stored row, parsed.
type row struct {
	key         string
	source      Source
	v           any
	from, until time.Time
}

func (r *row) active(now time.Time) bool {
	return (r.from.IsZero() || !now.Before(r.from)) && (r.until.IsZero() || now.Before(r.until))
}

// Service holds the sheets. Reads (Sheet, On) take no lock and make no query.
type Service struct {
	st    *store.Store
	clock Clock
	inv   Invalidator
	log   *slog.Logger

	cur atomic.Pointer[state]

	// mu guards what is below, which every write and every recomputation reads:
	// the inputs of the sheets are in memory, so a recomputation never queries.
	mu    sync.Mutex
	site  map[string]any            // the site's values over the built-in defaults
	plans map[string]map[string]any // by plan id
	// planOf is each tenant's plan id, rows its overrides by tenant.
	planOf map[string]string
	rows   map[string][]row
	timer  Timer
	// handled is the time up to which the starts and ends of rows have been
	// taken into the sheets.
	handled time.Time
	closed  bool

	// recomputed, set by tests only, sees each tenant's recomputation.
	recomputed func(tenant string)
}

// New reads the inputs of every sheet (the site's default, the plans, the
// tenants' plans and overrides), computes every sheet and starts the timer for
// the next start or end of a dated value.
func New(ctx context.Context, c Config) (*Service, error) {
	s := &Service{st: c.Store, clock: c.Clock, inv: c.Invalidate, log: c.Logger}
	if s.clock == nil {
		s.clock = RealClock(nil)
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(ctx); err != nil {
		return nil, err
	}
	s.schedule()
	return s, nil
}

// Reload reads every input again and recomputes every sheet: after the site's
// default, a plan or a tenant's plan was written (those are rare; a limit is
// written with SetManual, which recomputes one tenant).
func (s *Service) Reload(ctx context.Context) error {
	s.mu.Lock()
	err := s.load(ctx)
	if err == nil {
		s.schedule()
	}
	s.mu.Unlock()
	if err == nil {
		s.invalidate()
	}
	return err
}

// Close stops the timer.
func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}

func (s *Service) load(ctx context.Context) error {
	q := s.st.Q()
	site := map[string]any{}
	switch raw, _, err := store.GetSetting(ctx, q, store.SettingCapabilities); {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return err
	default:
		m, err := parseSet(raw)
		if err != nil {
			return fmt.Errorf("the site's capabilities: %w", err)
		}
		site = m
	}
	plans := map[string]map[string]any{}
	list, err := store.ListPlans(ctx, q)
	if err != nil {
		return err
	}
	for _, p := range list {
		m, err := parseSet(p.Values)
		if err != nil {
			return fmt.Errorf("plan %s: %w", p.Slug, err)
		}
		plans[p.ID] = m
	}
	planOf, err := store.TenantPlanIDs(ctx, q)
	if err != nil {
		return err
	}
	stored, err := store.ListTenantLimits(ctx, q)
	if err != nil {
		return err
	}
	rows := map[string][]row{}
	for _, l := range stored {
		v, err := Parse(l.Key, json.RawMessage(l.Value))
		if err != nil {
			// A value the registry no longer accepts does nothing; the rest of
			// the sheet is not held back by it.
			s.log.Warn("a limit of a tenant is ignored", "tenant", l.Tenant, "key", l.Key, "error", err)
			continue
		}
		rows[l.Tenant] = append(rows[l.Tenant], row{key: l.Key, source: Source(l.Source), v: v, from: l.Starts, until: l.Ends})
	}
	s.site, s.plans, s.planOf, s.rows = site, plans, planOf, rows
	s.handled = s.clock.Now()
	s.publishAll(s.handled)
	return nil
}

// siteSheet makes the sheet of the site's values.
func (s *Service) siteSheet() *Sheet {
	sh := &Sheet{vals: make([]*val, len(registry))}
	for i, d := range registry {
		v, ok := s.site[d.Key]
		if !ok {
			v = d.Default
		}
		sh.vals[i] = &val{v, FromSite}
	}
	return sh
}

// publishAll computes every tenant's sheet and publishes them.
func (s *Service) publishAll(now time.Time) {
	site := s.siteSheet()
	ids := s.st.TenantIDs()
	m := make(map[string]*Sheet, len(ids))
	for _, id := range ids {
		m[id] = s.compute(id, site, now)
	}
	s.cur.Store(&state{site: site, byTenant: m})
}

// publishTenants recomputes the sheets of the tenants and publishes them in a
// copy of the map: the sheets of the others are the very ones readers hold.
func (s *Service) publishTenants(now time.Time, tenants ...string) {
	old := s.cur.Load()
	m := make(map[string]*Sheet, len(old.byTenant)+len(tenants))
	for k, v := range old.byTenant {
		m[k] = v
	}
	for _, id := range tenants {
		m[id] = s.compute(id, old.site, now)
	}
	s.cur.Store(&state{site: old.site, byTenant: m})
}

// compute is a tenant's sheet: for each key the first present of its manual
// value, its billing value, its plan's and the site's; a value counts only
// while now is in its window.
func (s *Service) compute(tenant string, site *Sheet, now time.Time) *Sheet {
	if s.recomputed != nil {
		s.recomputed(tenant)
	}
	rows, plan := s.rows[tenant], s.plans[s.planOf[tenant]]
	if len(rows) == 0 && len(plan) == 0 {
		return site
	}
	sh := &Sheet{vals: make([]*val, len(registry))}
	copy(sh.vals, site.vals)
	for i, d := range registry {
		if v, ok := plan[d.Key]; ok {
			sh.vals[i] = &val{v, FromPlan}
		}
	}
	// Billing first, so that a manual value of the same key replaces it.
	for _, src := range []Source{FromBilling, FromManual} {
		for k := range rows {
			if r := &rows[k]; r.source == src && r.active(now) {
				sh.vals[index[r.key]] = &val{r.v, src}
			}
		}
	}
	return sh
}

// Sheet is a tenant's sheet. A tenant the service has not seen (made after the
// last Reload) has the site's.
func (s *Service) Sheet(tenant string) *Sheet {
	st := s.cur.Load()
	if sh, ok := st.byTenant[tenant]; ok {
		return sh
	}
	return st.site
}

// On says whether a capability is on for a tenant, which is what
// internal/authz asks. A key the registry does not know, or does not define as
// a capability, is on: only what a capability gates can be switched off.
func (s *Service) On(tenant, capability string) bool {
	d, ok := Lookup(capability)
	if !ok || !d.Capability || d.Kind != KindBool {
		return true
	}
	return s.Sheet(tenant).Bool(capability)
}

// SetManual writes, for a tenant, the manual values of the keys in changes (a
// nil *Change removes the value), as set_by by, then computes that tenant's
// sheet again and tells the settings. Nothing is written when any change is
// invalid.
func (s *Service) SetManual(ctx context.Context, tenant, by string, changes map[string]*Change) error {
	return s.set(ctx, tenant, by, store.LimitManual, changes)
}

func (s *Service) set(ctx context.Context, tenant, by, source string, changes map[string]*Change) error {
	type write struct {
		key string
		r   row
		raw string
	}
	var ws []write
	for key, c := range changes {
		w := write{key: key, r: row{key: key, source: Source(source)}}
		if c != nil {
			v, err := Parse(key, c.Value)
			if err != nil {
				return &ValueError{err}
			}
			if !c.From.IsZero() && !c.Until.IsZero() && !c.Until.After(c.From) {
				return &ValueError{fmt.Errorf("%s: until is not after from", key)}
			}
			w.r.v, w.r.from, w.r.until = v, c.From, c.Until
			if raw, err := json.Marshal(v); err == nil {
				w.raw = string(raw)
			}
		}
		ws = append(ws, w)
	}
	if _, ok := s.st.TenantRefOf(tenant); !ok {
		return store.ErrNotFound
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("limits: closed")
	}
	now := s.clock.Now()
	err := s.st.Tx(ctx, func(q store.Q) error {
		for _, w := range ws {
			if changes[w.key] == nil {
				if err := store.DeleteTenantLimit(ctx, q, tenant, w.key, source); err != nil {
					return err
				}
				continue
			}
			if err := store.PutTenantLimit(ctx, q, store.TenantLimit{Tenant: tenant, Key: w.key, Source: source, Value: w.raw,
				Starts: w.r.from, Ends: w.r.until, SetBy: by, Updated: now}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.mu.Unlock()
		return err
	}
	kept := s.rows[tenant][:0:0]
	for _, r := range s.rows[tenant] {
		if _, touched := changes[r.key]; !touched || string(r.source) != source {
			kept = append(kept, r)
		}
	}
	for _, w := range ws {
		if changes[w.key] != nil {
			kept = append(kept, w.r)
		}
	}
	if s.rows == nil {
		s.rows = map[string][]row{}
	}
	if len(kept) == 0 {
		delete(s.rows, tenant)
	} else {
		s.rows[tenant] = kept
	}
	// The write stops the timer, which may be about to fire for another tenant:
	// what is due is taken in here, so that it is not forgotten.
	tenants := []string{tenant}
	for _, id := range s.catchUp(now) {
		if id != tenant {
			tenants = append(tenants, id)
		}
	}
	s.publishTenants(now, tenants...)
	s.schedule()
	s.mu.Unlock()
	s.invalidate()
	return nil
}

// ValueError is a refusal of a value: the caller answers 400 invalid.
type ValueError struct{ Err error }

func (e *ValueError) Error() string { return e.Err.Error() }
func (e *ValueError) Unwrap() error { return e.Err }

func (s *Service) invalidate() {
	if s.inv != nil {
		s.inv.Invalidate()
	}
}

// next is the earliest start or end of a value after now, across all tenants.
func (s *Service) next(now time.Time) (t time.Time, ok bool) {
	for _, rows := range s.rows {
		for k := range rows {
			for _, b := range [2]time.Time{rows[k].from, rows[k].until} {
				if !b.IsZero() && b.After(now) && (!ok || b.Before(t)) {
					t, ok = b, true
				}
			}
		}
	}
	return
}

// schedule (re)starts the one timer, for the next start or end of any value.
// It is called with s.mu held, after every change of the rows. With nothing
// dated there is no timer: nothing polls.
func (s *Service) schedule() {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	if s.closed {
		return
	}
	now := s.clock.Now()
	if t, ok := s.next(now); ok {
		s.timer = s.clock.AfterFunc(t.Sub(now), s.fire)
	}
}

// catchUp finds the tenants that have a value whose start or end passed since
// the last time (handled), and moves handled to now. s.mu is held.
func (s *Service) catchUp(now time.Time) (due []string) {
	for tenant, rows := range s.rows {
		for k := range rows {
			if crossed(rows[k].from, s.handled, now) || crossed(rows[k].until, s.handled, now) {
				due = append(due, tenant)
				break
			}
		}
	}
	s.handled = now
	return due
}

// fire is the timer: it computes again the tenants that have a value whose
// start or end passed, then sets the timer for the next.
func (s *Service) fire() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	now := s.clock.Now()
	due := s.catchUp(now)
	if len(due) > 0 {
		s.publishTenants(now, due...)
	}
	s.schedule()
	s.mu.Unlock()
	if len(due) > 0 {
		s.invalidate()
	}
}

// crossed says whether the boundary b is in (after, now].
func crossed(b, after, now time.Time) bool {
	return !b.IsZero() && b.After(after) && !b.After(now)
}
