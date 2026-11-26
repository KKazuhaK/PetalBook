// Package quota is what limits mean in 3.3 (docs/api.md "Quotas"): which limit
// applies to a person or a tenant, whether a row of the usage table is over it,
// and noticing, once per crossing, that one is. Only the number of spaces is
// ever refused (by the space manager, with the limits from here); bytes and
// pages and the tenant's people only warn.
//
// Costs: the limits of a person are the tenant's effective settings (cached by
// the settings service, one map read) and the person's own JSON; whether a row
// is over is arithmetic on the row. Looking at everyone (after a limit changed,
// and after the daily recount) reads the usage rows of the scope once, in pages
// of 500 by key, and the people who have limits of their own in one more query.
package quota

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"

	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/spaces"
	"petalbook.app/server/internal/store"
)

// The quotas, as the API names them.
const (
	Spaces = "spaces"
	Bytes  = "bytes"
	Pages  = "pages"
	People = "people"
)

// Person is what one person may use; nil is no limit.
type Person struct {
	Spaces, Bytes, Pages *int64
}

// Tenant is what a tenant may use in all; nil is no limit.
type Tenant struct {
	People, Spaces, Bytes, Pages *int64
}

// Own is a person's own limits (users.quota): a nil field follows the tenant.
// The BySite flags say a site administrator set the field: such a value answers
// to the site's rule (lock or restriction) alone; one a tenant administrator set
// answers to the stricter of the site's and the tenant's.
type Own struct {
	Spaces *int64 `json:"spaces,omitempty"`
	Bytes  *int64 `json:"bytes,omitempty"`
	Pages  *int64 `json:"pages,omitempty"`

	SpacesBySite bool `json:"spacesBySite,omitempty"`
	BytesBySite  bool `json:"bytesBySite,omitempty"`
	PagesBySite  bool `json:"pagesBySite,omitempty"`
}

// Set sets one field (spaces, bytes or pages; nil follows the tenant again) and
// remembers who set it.
func (o *Own) Set(k string, v *int64, bySite bool) {
	bySite = bySite && v != nil
	switch k {
	case Spaces:
		o.Spaces, o.SpacesBySite = v, bySite
	case Bytes:
		o.Bytes, o.BytesBySite = v, bySite
	case Pages:
		o.Pages, o.PagesBySite = v, bySite
	}
}

// OwnInEffect says, for each of a person's own limits, whether it is the one in
// force: false while a lock or a restriction above forbids it (it is kept, and
// counts again when the rule is lifted). A field with no own value is true.
type OwnInEffect struct {
	Spaces bool `json:"spaces"`
	Bytes  bool `json:"bytes"`
	Pages  bool `json:"pages"`
}

// ParseOwn reads a person's stored limits ("" is none).
func ParseOwn(raw string) Own {
	var o Own
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &o)
	}
	return o
}

// Encode is the stored form of a person's own limits ("" for none).
func (o Own) Encode() string {
	if o == (Own{}) {
		return ""
	}
	b, _ := json.Marshal(o)
	return string(b)
}

// ParseTenant reads a tenant's totals (the tenants.quota column).
func ParseTenant(raw string) Tenant {
	var m struct {
		People, Spaces, Bytes, Pages *int64
	}
	var in map[string]*int64
	if raw != "" && json.Unmarshal([]byte(raw), &in) == nil {
		m.People, m.Spaces, m.Bytes, m.Pages = in["people"], in["spaces"], in["bytes"], in["pages"]
	}
	return Tenant{m.People, m.Spaces, m.Bytes, m.Pages}
}

// Audit records that a quota was crossed: the tenant, the person it is about
// ("" for a tenant's total), and the detail (quota, limit, used, scope).
type Audit func(tenant, user string, detail map[string]any)

// Service answers which limits apply and notices crossings.
type Service struct {
	st    *store.Store
	set   *settings.Service
	audit Audit
	log   *slog.Logger

	// evalMu is one evaluation at a time, so two measurements of one person do
	// not both report the same crossing.
	evalMu sync.Mutex
}

// New returns the service; audit and log may be nil.
func New(st *store.Store, set *settings.Service, audit Audit, log *slog.Logger) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Service{st: st, set: set, audit: audit, log: log}
}

func asInt(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case float64:
		return int64(n), true
	}
	return 0, false
}

// Limits are the limits of a person of a tenant with their own limits own: the
// person's own over the tenant's setting over the site's (see Layered).
func (s *Service) Limits(ctx context.Context, tenant string, own Own) (Person, error) {
	p, _, err := s.Layered(ctx, tenant, own)
	return p, err
}

// Layered is Limits, and whether each own limit is in effect. An own value
// answers to the rule of the layers above the one that set it (the site's for a
// site administrator's, the stricter of the site's and the tenant's for a tenant
// administrator's): under a lock it is not in effect and the tenant's value
// applies; under a restriction it counts up to the restriction's max (no limit,
// 0 for spaces, is above any max).
func (s *Service) Layered(ctx context.Context, tenant string, own Own) (Person, OwnInEffect, error) {
	eff, err := s.set.For(ctx, tenant)
	if err != nil {
		return Person{}, OwnInEffect{}, err
	}
	pick := func(key string, o *int64, bySite, zeroIsNone bool) (*int64, bool) {
		e := eff[key]
		var above *int64
		if n, ok := asInt(e.Value); ok {
			above = &n
		}
		in := true
		if o != nil {
			rule := e.Below
			if bySite {
				rule = e.Site.Lower
			}
			switch {
			case rule.Mode == settings.Lock:
				o, in = nil, false
			case rule.Mode == settings.Restrict && rule.Max != nil && (zeroIsNone && *o == 0 || *o > *rule.Max):
				m := *rule.Max
				o, in = &m, false
			}
		}
		if o == nil {
			o = above
		}
		if o != nil && zeroIsNone && *o == 0 {
			return nil, in
		}
		return o, in
	}
	var p Person
	var in OwnInEffect
	p.Spaces, in.Spaces = pick(settings.QuotaSpacesPerPerson, own.Spaces, own.SpacesBySite, true)
	p.Bytes, in.Bytes = pick(settings.QuotaBytesPerPerson, own.Bytes, own.BytesBySite, false)
	p.Pages, in.Pages = pick(settings.QuotaPagesPerPerson, own.Pages, own.PagesBySite, false)
	return p, in, nil
}

// SpaceLimits is what the space manager asks when someone makes a space: the
// person's and the tenant's numbers of spaces (0: none).
func (s *Service) SpaceLimits(ctx context.Context, u store.User) (spaces.SpaceLimits, error) {
	q := s.st.Q()
	raw, err := store.UserQuota(ctx, q, store.AllTenants(), u.ID)
	if err != nil {
		return spaces.SpaceLimits{}, err
	}
	l, err := s.Limits(ctx, u.TenantID, ParseOwn(raw))
	if err != nil {
		return spaces.SpaceLimits{}, err
	}
	var out spaces.SpaceLimits
	if l.Spaces != nil {
		out.Person = *l.Spaces
	}
	t, err := store.GetTenant(ctx, q, u.TenantID)
	if err != nil {
		return out, err
	}
	if tl := ParseTenant(t.Quota); tl.Spaces != nil {
		out.Tenant = *tl.Spaces
	}
	return out, nil
}

func over(used int64, limit *int64) bool { return limit != nil && used > *limit }

// OverPerson lists the quotas a person's row is over.
func OverPerson(u store.Usage, l Person) []string {
	var out []string
	if over(u.Spaces, l.Spaces) {
		out = append(out, Spaces)
	}
	if over(u.Bytes, l.Bytes) {
		out = append(out, Bytes)
	}
	if over(u.Pages, l.Pages) {
		out = append(out, Pages)
	}
	return out
}

// OverTenant lists the quotas a tenant's row is over.
func OverTenant(u store.Usage, l Tenant) []string {
	var out []string
	if over(u.People, l.People) {
		out = append(out, People)
	}
	if over(u.Spaces, l.Spaces) {
		out = append(out, Spaces)
	}
	if over(u.Bytes, l.Bytes) {
		out = append(out, Bytes)
	}
	if over(u.Pages, l.Pages) {
		out = append(out, Pages)
	}
	return out
}

func used(u store.Usage, quota string) int64 {
	switch quota {
	case Spaces:
		return u.Spaces
	case Bytes:
		return u.Bytes
	case Pages:
		return u.Pages
	}
	return u.People
}

func limitOf(quota string, p Person, t Tenant, tenant bool) int64 {
	var l *int64
	switch {
	case tenant && quota == People:
		l = t.People
	case tenant && quota == Spaces:
		l = t.Spaces
	case tenant && quota == Bytes:
		l = t.Bytes
	case tenant && quota == Pages:
		l = t.Pages
	case quota == Spaces:
		l = p.Spaces
	case quota == Bytes:
		l = p.Bytes
	case quota == Pages:
		l = p.Pages
	}
	if l == nil {
		return 0
	}
	return *l
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		if !contains(b, x) {
			return false
		}
	}
	return true
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// settle stores which quotas a row is over and audits each one it was not over
// before: once per crossing, however often it is looked at.
func (s *Service) settle(ctx context.Context, u store.Usage, now []string, p Person, t Tenant, tenant bool) error {
	if sameSet(u.Over, now) {
		return nil // nothing to store: looking at everyone writes only what changed
	}
	changed, err := store.SetUsageOver(ctx, s.st.Q(), u.Kind, u.ID, now)
	if err != nil || !changed || s.audit == nil {
		return err
	}
	for _, q := range now {
		if contains(u.Over, q) {
			continue
		}
		detail := map[string]any{"quota": q, "limit": limitOf(q, p, t, tenant), "used": used(u, q)}
		if tenant {
			detail["scope"] = "tenant"
			s.audit(u.ID, "", detail)
		} else {
			detail["scope"] = "person"
			s.audit(u.TenantID, u.ID, detail)
		}
	}
	return nil
}

// Evaluate looks at the rows of the people and tenants a change reached.
func (s *Service) Evaluate(ctx context.Context, touched []store.Touched) error {
	s.evalMu.Lock()
	defer s.evalMu.Unlock()
	seenU, seenT := map[string]bool{}, map[string]bool{}
	for _, t := range touched {
		if t.UserID != "" && !seenU[t.UserID] {
			seenU[t.UserID] = true
			if err := s.person(ctx, t.UserID); err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
		}
		if t.TenantID != "" && !seenT[t.TenantID] {
			seenT[t.TenantID] = true
			if err := s.tenant(ctx, t.TenantID); err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
		}
	}
	return nil
}

func (s *Service) person(ctx context.Context, id string) error {
	q := s.st.Q()
	raw, err := store.UserQuota(ctx, q, store.AllTenants(), id)
	if err != nil {
		return err
	}
	u, err := store.GetUsage(ctx, q, store.UsageUser, id)
	if err != nil {
		return err
	}
	l, err := s.Limits(ctx, u.TenantID, ParseOwn(raw))
	if err != nil {
		return err
	}
	return s.settle(ctx, u, OverPerson(u, l), l, Tenant{}, false)
}

func (s *Service) tenant(ctx context.Context, id string) error {
	q := s.st.Q()
	t, err := store.GetTenant(ctx, q, id)
	if err != nil {
		return err
	}
	u, err := store.GetUsage(ctx, q, store.UsageTenant, id)
	if err != nil {
		return err
	}
	l := ParseTenant(t.Quota)
	return s.settle(ctx, u, OverTenant(u, l), Person{}, l, true)
}

// EvaluateScope looks at every person in the scope and the tenants in it: after
// a limit changed, and after the daily recount. It reads the usage rows once,
// 500 at a time.
func (s *Service) EvaluateScope(ctx context.Context, sc store.Scope) error {
	s.evalMu.Lock()
	defer s.evalMu.Unlock()
	q := s.st.Q()
	own, err := store.UsersWithQuota(ctx, q, sc)
	if err != nil {
		return err
	}
	limits := map[string]Person{}
	after := ""
	for {
		rows, err := store.UserUsageRows(ctx, q, sc, after, 500)
		if err != nil {
			return err
		}
		for _, u := range rows {
			if len(u.Over) == 0 && u.Spaces == 0 && u.Bytes == 0 && u.Pages == 0 {
				continue
			}
			key := u.TenantID
			l, ok := limits[key]
			if !ok || own[u.ID] != "" {
				if l, err = s.Limits(ctx, u.TenantID, ParseOwn(own[u.ID])); err != nil {
					return err
				}
				if own[u.ID] == "" {
					limits[key] = l
				}
			}
			if err := s.settle(ctx, u, OverPerson(u, l), l, Tenant{}, false); err != nil {
				return err
			}
		}
		if len(rows) < 500 {
			break
		}
		after = rows[len(rows)-1].ID
	}
	for _, id := range s.st.TenantIDs() {
		if !sc.All() && sc.Tenant() != id {
			continue
		}
		t, err := store.GetTenant(ctx, q, id)
		if err != nil {
			continue
		}
		u, err := store.GetUsage(ctx, q, store.UsageTenant, id)
		if err != nil {
			return err
		}
		l := ParseTenant(t.Quota)
		if err := s.settle(ctx, u, OverTenant(u, l), Person{}, l, true); err != nil {
			return err
		}
	}
	return nil
}

// Handle is what the space manager calls when usage changed in the background.
func (s *Service) Handle(ctx context.Context, ev spaces.UsageEvent) {
	var err error
	if ev.Everyone {
		err = s.EvaluateScope(ctx, store.AllTenants())
	} else {
		err = s.Evaluate(ctx, ev.Touched)
	}
	if err != nil && ctx.Err() == nil {
		s.log.Warn("could not check the quotas", "error", err)
	}
}
