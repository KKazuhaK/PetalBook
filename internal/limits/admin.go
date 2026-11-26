package limits

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"petalbook.app/server/internal/store"
)

// Applied is one key of a write, or of a dated value that started or ended: the
// effective value before and after, and where the new one comes from.
type Applied struct {
	Key      string
	From, To any
	// Source is where the effective value comes from now; for a write that sets
	// a value it is the source written, which another source may still cover.
	Source Source
	// Start and End are the window of the value written (zero for none).
	Start, End time.Time
	// Removed is true when the write took the value away.
	Removed bool
}

// Result is what a write did.
type Result struct {
	Applied []Applied
	// Replaced lists the keys whose undated value of the same source was
	// replaced by a dated one: there is one value per tenant, key and source, so
	// when the window ends the value is not the undated one but what is below.
	Replaced []string
}

// differing lists the tenants whose sheet differs between two states, sorted.
func differing(old, cur *state) []string {
	out := []string{}
	if old == nil {
		return out
	}
	for id, sh := range cur.byTenant {
		prev, ok := old.byTenant[id]
		if !ok {
			prev = old.site
		}
		if len(diffSheets(prev, sh)) > 0 {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

func diffSheets(a, b *Sheet) []Applied {
	if a == b {
		return nil
	}
	var out []Applied
	for i, d := range registry {
		x, y := a.vals[i], b.vals[i]
		if x == y || (x.src == y.src && reflect.DeepEqual(x.v, y.v)) {
			continue
		}
		out = append(out, Applied{Key: d.Key, From: x.v, To: y.v, Source: y.src})
	}
	return out
}

// tell reports to OnBoundary the values that changed in the sheets of the
// tenants in due, other than skip (whose change the caller reports itself).
func (s *Service) tell(old, cur *state, due []string, skip string) {
	if s.boundary == nil {
		return
	}
	for _, id := range due {
		if id == skip {
			continue
		}
		prev, ok := old.byTenant[id]
		if !ok {
			prev = old.site
		}
		for _, a := range diffSheets(prev, cur.byTenant[id]) {
			s.boundary(id, a)
		}
	}
}

// Under is what a value falls back to.
type Under struct {
	Value  any
	Source Source
}

// Pending is a manual value that is not the effective one (yet or any more).
type Pending struct {
	Value       any
	From, Until time.Time
}

// Entry describes one key of a tenant's sheet, for the administrators' view.
type Entry struct {
	Key         string
	Value       any
	Source      Source
	From, Until time.Time
	// Underlying is what applies outside the window, or when the value is
	// removed; set when the value comes from manual or billing.
	Underlying *Under
	// Manual is the tenant's manual value when it is not the effective one: it
	// has a window that has not begun, or has ended.
	Manual *Pending
}

type cand struct {
	v           any
	src         Source
	from, until time.Time
	active      bool
}

// Describe lists every key of a tenant's sheet, in registry order, with where
// each value comes from, its window and what it falls back to. It reads the
// inputs in memory and is for the administrators' pages, not for a request.
func (s *Service) Describe(tenant string) []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	plan := s.plans[s.planOf[tenant]]
	out := make([]Entry, len(registry))
	for i, d := range registry {
		var chain []cand
		for _, src := range []Source{FromManual, FromBilling} {
			for k := range s.rows[tenant] {
				if r := &s.rows[tenant][k]; r.key == d.Key && r.source == src {
					chain = append(chain, cand{r.v, src, r.from, r.until, r.active(now)})
				}
			}
		}
		if v, ok := plan[d.Key]; ok {
			chain = append(chain, cand{v: v, src: FromPlan, active: true})
		}
		sv, ok := s.site[d.Key]
		if !ok {
			sv = d.Default
		}
		chain = append(chain, cand{v: sv, src: FromSite, active: true})

		e := Entry{Key: d.Key}
		first := -1
		for j, c := range chain {
			if c.active {
				first = j
				e.Value, e.Source, e.From, e.Until = c.v, c.src, c.from, c.until
				break
			}
		}
		if e.Source == FromManual || e.Source == FromBilling {
			for _, c := range chain[first+1:] {
				if c.active {
					e.Underlying = &Under{c.v, c.src}
					break
				}
			}
		}
		if c := chain[0]; c.src == FromManual && !c.active {
			e.Manual = &Pending{c.v, c.from, c.until}
		}
		out[i] = e
	}
	return out
}

// PlanID is the id of a tenant's plan, "" for none.
func (s *Service) PlanID(tenant string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.planOf[tenant]
}

// Cleared is a manual override that a change of plan removed.
type Cleared struct {
	Key         string
	Value       any
	From, Until time.Time
}

// SetPlan gives a tenant a plan ("" for none) and clears its manual overrides
// (docs/api.md, "Plans"): the tenant's sheet is then the site's and the plan's,
// and billing's. It answers the overrides it cleared.
func (s *Service) SetPlan(ctx context.Context, tenant, planID string) ([]Cleared, error) {
	if _, ok := s.st.TenantRefOf(tenant); !ok {
		return nil, store.ErrNotFound
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("limits: closed")
	}
	if _, ok := s.plans[planID]; planID != "" && !ok {
		s.mu.Unlock()
		return nil, store.ErrNotFound
	}
	err := s.st.Tx(ctx, func(q store.Q) error {
		if err := store.SetTenantPlan(ctx, q, tenant, planID); err != nil {
			return err
		}
		return store.DeleteTenantLimitsOf(ctx, q, tenant, store.LimitManual)
	})
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	var cleared []Cleared
	var kept []row
	for _, r := range s.rows[tenant] {
		if r.source == FromManual {
			cleared = append(cleared, Cleared{r.key, r.v, r.from, r.until})
		} else {
			kept = append(kept, r)
		}
	}
	slices.SortFunc(cleared, func(a, b Cleared) int { return strings.Compare(a.Key, b.Key) })
	if len(kept) == 0 {
		delete(s.rows, tenant)
	} else {
		s.rows[tenant] = kept
	}
	if planID == "" {
		delete(s.planOf, tenant)
	} else {
		s.planOf[tenant] = planID
	}
	s.publishTenants(s.clock.Now(), tenant)
	s.schedule()
	s.mu.Unlock()
	s.invalidate()
	return cleared, nil
}

// Forget drops a tenant that was deleted: its overrides and plan (the database
// rows go with the tenant) and its sheet.
func (s *Service) Forget(tenant string) {
	s.mu.Lock()
	delete(s.rows, tenant)
	delete(s.planOf, tenant)
	old := s.cur.Load()
	m := make(map[string]*Sheet, len(old.byTenant))
	for k, v := range old.byTenant {
		if k != tenant {
			m[k] = v
		}
	}
	s.cur.Store(&state{site: old.site, byTenant: m})
	s.schedule()
	s.mu.Unlock()
}

// SetSite changes the site's default values (the key capabilities of
// site_settings): a key with a value is set, a key with a nil entry goes back
// to the built-in default. It answers the tenants whose sheet changed.
func (s *Service) SetSite(ctx context.Context, changes map[string]json.RawMessage) (affected []string, err error) {
	for k, raw := range changes {
		if raw == nil {
			if _, ok := Lookup(k); !ok {
				return nil, &ValueError{fmt.Errorf("%s is not a key of the limits sheet", k)}
			}
			continue
		}
		if _, err := Parse(k, raw); err != nil {
			return nil, &ValueError{err}
		}
	}
	s.mu.Lock()
	cur := map[string]json.RawMessage{}
	if raw, _, err := store.GetSetting(ctx, s.st.Q(), store.SettingCapabilities); err == nil {
		_ = json.Unmarshal([]byte(raw), &cur)
	} else if !errors.Is(err, store.ErrNotFound) {
		s.mu.Unlock()
		return nil, err
	}
	for k, raw := range changes {
		if raw == nil {
			delete(cur, k)
		} else {
			cur[k] = raw
		}
	}
	b, _ := json.Marshal(cur)
	if _, err := store.PutSetting(ctx, s.st.Q(), store.SettingCapabilities, string(b), s.clock.Now()); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	affected, err = s.load(ctx)
	if err == nil {
		s.schedule()
	}
	s.mu.Unlock()
	if err == nil {
		s.invalidate()
	}
	return affected, err
}

// SiteValues lists the site's value of every key, in registry order.
func (s *Service) SiteValues() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, len(registry))
	for i, d := range registry {
		v, ok := s.site[d.Key]
		if !ok {
			v = d.Default
		}
		out[i] = Entry{Key: d.Key, Value: v, Source: FromSite}
	}
	return out
}

// ParseValues checks a plan's values (an object of key to value) and returns
// them in the form they are stored in.
func ParseValues(m map[string]json.RawMessage) (string, error) {
	out := make(map[string]any, len(m))
	for k, raw := range m {
		v, err := Parse(k, raw)
		if err != nil {
			return "", err
		}
		out[k] = v
	}
	b, err := json.Marshal(out)
	return string(b), err
}

// PlanValues parses the stored values of a plan.
func PlanValues(raw string) (map[string]any, error) { return parseSet(raw) }

// PlanTenants says how many tenants use each plan, by plan id.
func (s *Service) PlanTenants() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := map[string]int{}
	for _, p := range s.planOf {
		n[p]++
	}
	return n
}
