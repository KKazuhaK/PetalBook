package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/quota"
	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/spaces"
	"petalbook.app/server/internal/store"
)

// Quotas and usage (milestone 3.3, docs/api.md "Quotas"). What a request here
// costs: a few reads of rows by key (a person's usage row, their tenant's, their
// own limits) and the tenant's cached settings; nothing measures, walks or
// counts.

// bindQuota wires the quota service: it answers the space manager's limits,
// is told when usage changes in the background, and gives the site's value of
// quota.spacesPerPerson to --max-spaces when the flag was given.
func (s *server) bindQuota() {
	s.quota = quota.New(s.opt.Store, s.settings, func(tenant, user string, detail map[string]any) {
		s.auth.Audit(auth.AuditEvent{Kind: "quota_exceeded", User: user, Tenant: tenant, Detail: detail})
	}, s.log)
	s.spaces.SetQuota(s.quota.SpaceLimits, s.quota.Handle)
	s.auth.OnNewUser(func(ctx context.Context, touched []store.Touched) {
		s.quota.Handle(ctx, spaces.UsageEvent{Touched: touched})
	})
	if n := s.opt.MaxSpaces; n != nil {
		v := int64(*n)
		s.settings.Bind(settings.QuotaSpacesPerPerson, settings.SiteBinding{
			Get:      func(context.Context, store.Q) (any, error) { return v, nil },
			Set:      func(context.Context, store.Q, any) error { return errors.New("set by --max-spaces") },
			ReadOnly: func() bool { return true },
		})
	}
}

// limitJSON is a limit as the API shows it: the number, or null for none.
type limitJSON = *int64

type usageOfResp struct {
	Used  int64     `json:"used"`
	Limit limitJSON `json:"limit"`
}

type noticeResp struct {
	Quota string `json:"quota"`
	// Scope is "person" (the person's own limit) or "tenant" (the tenant's total).
	Scope string `json:"scope"`
	Limit int64  `json:"limit"`
	Used  int64  `json:"used"`
}

func measuredAt(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	v := rfc3339(t)
	return &v
}

// accountUsage is GET /api/account/usage: the caller's own numbers.
func (s *server) accountUsage(c *call) {
	ctx := c.r.Context()
	q := s.opt.Store.Q()
	u := c.user()
	raw, err := store.UserQuota(ctx, q, store.AllTenants(), u.ID)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	lim, err := s.quota.Limits(ctx, u.TenantID, quota.ParseOwn(raw))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	row, err := store.GetUsage(ctx, q, store.UsageUser, u.ID)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	notices := []noticeResp{}
	for _, k := range quota.OverPerson(row, lim) {
		notices = append(notices, noticeResp{k, "person", limitValue(k, lim), usedOf(row, k)})
	}
	// The tenant's total is the person's business too, once it is over.
	t, err := store.GetTenant(ctx, q, u.TenantID)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	trow, err := store.GetUsage(ctx, q, store.UsageTenant, u.TenantID)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	tl := quota.ParseTenant(t.Quota)
	for _, k := range quota.OverTenant(trow, tl) {
		var l int64
		switch k {
		case quota.People:
			l = *tl.People
		case quota.Spaces:
			l = *tl.Spaces
		case quota.Bytes:
			l = *tl.Bytes
		case quota.Pages:
			l = *tl.Pages
		}
		notices = append(notices, noticeResp{k, "tenant", l, usedOf(trow, k)})
	}
	writeJSON(c.w, http.StatusOK, struct {
		Spaces     usageOfResp  `json:"spaces"`
		Bytes      usageOfResp  `json:"bytes"`
		Pages      usageOfResp  `json:"pages"`
		MeasuredAt *string      `json:"measuredAt"`
		Notices    []noticeResp `json:"notices"`
	}{usageOfResp{row.Spaces, lim.Spaces}, usageOfResp{row.Bytes, lim.Bytes}, usageOfResp{row.Pages, lim.Pages}, measuredAt(row.Measured), notices})
}

func usedOf(u store.Usage, k string) int64 {
	switch k {
	case quota.Spaces:
		return u.Spaces
	case quota.Bytes:
		return u.Bytes
	case quota.Pages:
		return u.Pages
	}
	return u.People
}

func limitValue(k string, l quota.Person) int64 {
	var p *int64
	switch k {
	case quota.Spaces:
		p = l.Spaces
	case quota.Bytes:
		p = l.Bytes
	case quota.Pages:
		p = l.Pages
	}
	if p == nil {
		return 0
	}
	return *p
}

type personUsageResp struct {
	User struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Name     string `json:"name"`
	} `json:"user"`
	Tenant string `json:"tenant"`
	Spaces int64  `json:"spaces"`
	Bytes  int64  `json:"bytes"`
	Pages  int64  `json:"pages"`
	Limits limits `json:"limits"`
	// Own is the person's own limits (null follows the tenant): what the edit form starts with.
	Own *ownQuotaResp `json:"own"`
	// OwnInEffect is false for an own limit a lock or restriction above now forbids.
	OwnInEffect quota.OwnInEffect `json:"ownInEffect"`
	Over        []string          `json:"over"`
	MeasuredAt  *string           `json:"measuredAt"`
}

type limits struct {
	Spaces limitJSON `json:"spaces"`
	Bytes  limitJSON `json:"bytes"`
	Pages  limitJSON `json:"pages"`
}

// adminUsage is GET /api/admin/usage: people by usage, the most bytes first.
func (s *server) adminUsage(c *call) {
	sc, ok := s.adminScope(c)
	if !ok {
		return
	}
	limit, ok := listLimit(c, defaultAdminLimit, maxAdminLimit)
	if !ok {
		return
	}
	qv := c.r.URL.Query()
	over := false
	switch qv.Get("over") {
	case "1":
		over = true
	case "", "0":
	default:
		writeError(c.w, http.StatusBadRequest, "invalid", "Over is 1 or 0.")
		return
	}
	ctx := c.r.Context()
	rows, next, err := store.ListUserUsage(ctx, s.opt.Store.Q(), sc, store.UsageFilter{OverOnly: over, After: qv.Get("cursor"), Limit: limit})
	if errors.Is(err, store.ErrBadCursor) {
		writeError(c.w, http.StatusBadRequest, "invalid", "That cursor is not one this list gave.")
		return
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	items := make([]personUsageResp, len(rows))
	for i, r := range rows {
		l, in, err := s.quota.Layered(ctx, r.TenantID, quota.ParseOwn(r.Quota))
		if err != nil {
			s.fail(c.w, err)
			return
		}
		it := personUsageResp{Tenant: r.TenantID, Spaces: r.Spaces, Bytes: r.Bytes, Pages: r.Pages, Limits: limits{l.Spaces, l.Bytes, l.Pages}, Own: toOwnQuota(r.Quota), OwnInEffect: in,
			Over: r.Over, MeasuredAt: measuredAt(r.Measured)}
		if it.Over == nil {
			it.Over = []string{}
		}
		it.User.ID, it.User.Username, it.User.Name = r.ID, r.Username, r.Name
		items[i] = it
	}
	writeJSON(c.w, http.StatusOK, struct {
		Items []personUsageResp `json:"items"`
		Next  string            `json:"next"`
	}{items, next})
}

// --- a person's own limits ----------------------------------------------------

type ownQuotaResp struct {
	Spaces limitJSON `json:"spaces"`
	Bytes  limitJSON `json:"bytes"`
	Pages  limitJSON `json:"pages"`
}

func toOwnQuota(raw string) *ownQuotaResp {
	o := quota.ParseOwn(raw)
	return &ownQuotaResp{o.Spaces, o.Bytes, o.Pages}
}

// rank orders limits for "may not be above the tenant's": no limit is the
// highest (0 is no limit for spaces, null for the others).
func rank(v *int64, zeroIsNone bool) int64 {
	const none = int64(1) << 62
	if v == nil || zeroIsNone && *v == 0 {
		return none
	}
	return *v
}

// mergeOwnQuota applies the quota object of a PATCH to a person's stored limits
// and checks it for the caller: a number inside the setting's range, null to
// follow the tenant; above the tenant's value only a site administrator, and
// never past a lock or restriction. ok false means it answered.
func (s *server) mergeOwnQuota(c *call, target store.User, raw json.RawMessage, cur string) (string, bool) {
	var in map[string]json.RawMessage
	if json.Unmarshal(raw, &in) != nil || in == nil {
		writeError(c.w, http.StatusBadRequest, "invalid", "Quota is an object with spaces, bytes and pages.")
		return "", false
	}
	own := quota.ParseOwn(cur)
	site := c.isSiteAdmin()
	eff, err := s.settings.For(c.r.Context(), target.TenantID)
	if err != nil {
		s.fail(c.w, err)
		return "", false
	}
	for k, v := range in {
		var key string
		switch k {
		case "spaces":
			key = settings.QuotaSpacesPerPerson
		case "bytes":
			key = settings.QuotaBytesPerPerson
		case "pages":
			key = settings.QuotaPagesPerPerson
		default:
			writeError(c.w, http.StatusBadRequest, "invalid", "Quota has spaces, bytes and pages.")
			return "", false
		}
		var n *int64
		if json.Unmarshal(v, &n) != nil {
			writeError(c.w, http.StatusBadRequest, "invalid", "quota."+k+" is a number, or null to follow the tenant.")
			return "", false
		}
		if n == nil {
			own.Set(k, nil, false)
			continue
		}
		d, _ := s.settings.Def(key)
		if _, err := d.Value(*n); err != nil {
			writeError(c.w, http.StatusBadRequest, "invalid", "quota."+k+": "+err.Error()+".")
			return "", false
		}
		e := eff[key]
		below := e.Below
		if site {
			below = e.Site.Lower
		}
		switch {
		case below.Mode == settings.Lock:
			writeError(c.w, http.StatusConflict, settings.CodeLocked, key+" is locked: nobody below it may change it.")
			return "", false
		case !below.Allows(d, *n):
			writeError(c.w, http.StatusConflict, settings.CodeRestricted, key+": above what the layer above allows.")
			return "", false
		}
		if !site {
			var cap *int64
			if ev, ok := e.Value.(int64); ok {
				cap = &ev
			}
			if rank(n, k == "spaces") > rank(cap, k == "spaces") {
				writeError(c.w, http.StatusForbidden, "forbidden", "Only an administrator of the whole server may give a person more than their tenant allows.")
				return "", false
			}
		}
		own.Set(k, n, site)
	}
	return own.Encode(), true
}

// checkQuotaPatch is the quota part of PATCH /api/users/{id}. It is checked before
// anything else is changed (so a refused quota changes nothing), and applied
// after.
func (s *server) checkQuotaPatch(c *call, id string, raw json.RawMessage) (apply func() bool, ok bool) {
	ctx := c.r.Context()
	sc := store.ScopeOf(c.user())
	target, err := store.GetUser(ctx, s.opt.Store.Q(), sc, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(c.w, http.StatusNotFound, "not_found", "No such user.")
		return nil, false
	}
	if err != nil {
		s.fail(c.w, err)
		return nil, false
	}
	cur, err := store.UserQuota(ctx, s.opt.Store.Q(), sc, id)
	if err != nil {
		s.fail(c.w, err)
		return nil, false
	}
	next, ok := s.mergeOwnQuota(c, *target, raw, cur)
	if !ok {
		return nil, false
	}
	return func() bool {
		if next == cur {
			return true
		}
		if err := store.SetUserQuota(ctx, s.opt.Store.Q(), sc, id, next); err != nil {
			s.fail(c.w, err)
			return false
		}
		s.audit(c, auth.AuditEvent{Kind: "user_changed", Actor: c.user().ID, User: id, Tenant: target.TenantID,
			Detail: map[string]any{"quota": toOwnQuota(next)}})
		_ = s.quota.Evaluate(ctx, []store.Touched{{UserID: id}})
		return true
	}, true
}

// quotaSettingChanged looks at everyone a changed limit may concern.
func (s *server) quotaSettingChanged(ctx context.Context, key string, sc store.Scope) {
	if !strings.HasPrefix(key, "quota.") || s.quota == nil {
		return
	}
	if err := s.quota.EvaluateScope(ctx, sc); err != nil {
		s.log.Warn("could not check the quotas after a change", "error", err)
	}
}

// tenantUsageOf is a tenant's usage as the Tenant object shows it.
type tenantUsageResp struct {
	People     int64    `json:"people"`
	Spaces     int64    `json:"spaces"`
	Bytes      int64    `json:"bytes"`
	Pages      int64    `json:"pages"`
	MeasuredAt *string  `json:"measuredAt"`
	Over       []string `json:"over"`
}

func toTenantUsage(u store.Usage) tenantUsageResp {
	r := tenantUsageResp{u.People, u.Spaces, u.Bytes, u.Pages, measuredAt(u.Measured), u.Over}
	if r.Over == nil {
		r.Over = []string{}
	}
	return r
}
