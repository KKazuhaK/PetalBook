package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"petalbook.app/server/internal/auth"
	sheets "petalbook.app/server/internal/limits"
	"petalbook.app/server/internal/page"
	"petalbook.app/server/internal/store"
)

// The limits sheet, its plans and the site's defaults (docs/api.md, "Tenant
// autonomy": Capabilities, Plans, The limits sheet). Reading is the tenant's
// administrators' too; every write is the site's (site.tenants.manage). A
// check of a capability or a limit is a map read (internal/limits); what is
// here runs when an administrator looks at or changes the sheet.

// MaxPlans is how many plans there may be.
const MaxPlans = 100

type planRefResp struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type limitUnderlyingResp struct {
	Value  any    `json:"value"`
	Source string `json:"source"`
}

type limitPendingResp struct {
	Value any     `json:"value"`
	From  *string `json:"from"`
	Until *string `json:"until"`
}

// limitEntryResp is one key of a sheet.
type limitEntryResp struct {
	Key         string  `json:"key"`
	Kind        string  `json:"kind"`
	Description string  `json:"description"`
	Value       any     `json:"value"`
	Source      string  `json:"source"`
	From        *string `json:"from,omitempty"`
	Until       *string `json:"until,omitempty"`
	// Underlying is what applies outside the window or when the manual value is
	// removed; Manual is a manual value that is not in effect (its window has not
	// begun or has ended); Used is what is counted, for the limits that are.
	Underlying *limitUnderlyingResp `json:"underlying,omitempty"`
	Manual     *limitPendingResp    `json:"manual,omitempty"`
	Used       *int64               `json:"used,omitempty"`
}

type limitWarningResp struct {
	Code    string   `json:"code"`
	Keys    []string `json:"keys"`
	Message string   `json:"message"`
}

// planRef is the plan a tenant has, nil for none.
func (s *server) planRef(ctx context.Context, tenant string) (*planRefResp, error) {
	id := s.limits.PlanID(tenant)
	if id == "" {
		return nil, nil
	}
	all, err := store.ListPlans(ctx, s.opt.Store.Q())
	if err != nil {
		return nil, err
	}
	for _, p := range all {
		if p.ID == id {
			return &planRefResp{p.Slug, p.Name}, nil
		}
	}
	return nil, nil
}

// sheetEntries describes a tenant's sheet, the capability keys only or all.
func (s *server) sheetEntries(ctx context.Context, tenant string, capsOnly bool) ([]limitEntryResp, error) {
	var u store.Usage
	if !capsOnly {
		var err error
		if u, err = s.usageOf(ctx, tenant); err != nil {
			return nil, err
		}
	}
	used := map[string]int64{"other.people": u.People, "storage.quota.spaces": u.Spaces, "storage.quota.bytes": u.Bytes, "storage.quota.pages": u.Pages}
	out := []limitEntryResp{}
	for _, e := range s.limits.Describe(tenant) {
		d, _ := sheets.Lookup(e.Key)
		if capsOnly && !d.Capability {
			continue
		}
		r := limitEntryResp{Key: e.Key, Kind: d.Kind.String(), Description: d.Description, Value: e.Value, Source: string(e.Source),
			From: optTime(e.From), Until: optTime(e.Until)}
		if e.Underlying != nil {
			r.Underlying = &limitUnderlyingResp{e.Underlying.Value, string(e.Underlying.Source)}
		}
		if e.Manual != nil {
			r.Manual = &limitPendingResp{e.Manual.Value, optTime(e.Manual.From), optTime(e.Manual.Until)}
		}
		if n, ok := used[e.Key]; ok && !capsOnly {
			r.Used = &n
		}
		out = append(out, r)
	}
	return out, nil
}

// writeSheet answers the sheet of a tenant: {"plan", "limits" or
// "capabilities", "warning"?}.
func (s *server) writeSheet(c *call, tenant string, capsOnly bool, warn *limitWarningResp) {
	ctx := c.r.Context()
	plan, err := s.planRef(ctx, tenant)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	entries, err := s.sheetEntries(ctx, tenant, capsOnly)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	out := map[string]any{"plan": plan}
	if capsOnly {
		out["capabilities"] = entries
	} else {
		out["limits"] = entries
	}
	if warn != nil {
		out["warning"] = warn
	}
	writeJSON(c.w, http.StatusOK, out)
}

func (s *server) adminGetTenantLimits(c *call) {
	if t, ok := s.visibleTenant(c); ok {
		s.writeSheet(c, t.ID, false, nil)
	}
}

func (s *server) adminGetTenantCapabilities(c *call) {
	if t, ok := s.visibleTenant(c); ok {
		s.writeSheet(c, t.ID, true, nil)
	}
}

// valueIn reads one entry of "values" in a write. null removes the manual
// value (a nil Change). An object is {"value": …, "from": …, "until": …}, which
// is the only way to write null as a value (no limit); anything else is the
// value itself. One encoding for the capabilities and the limits.
func valueIn(raw json.RawMessage) (*sheets.Change, error) {
	raw = bytes.TrimSpace(raw)
	if string(raw) == "null" {
		return nil, nil
	}
	if len(raw) == 0 || raw[0] != '{' {
		return &sheets.Change{Value: raw}, nil
	}
	var env struct {
		Value       json.RawMessage `json:"value"`
		From, Until *string
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&env); err != nil || env.Value == nil {
		return nil, errors.New(`an object is {"value": …, "from": …, "until": …}`)
	}
	ch := &sheets.Change{Value: env.Value}
	for _, f := range []struct {
		in  *string
		out *time.Time
	}{{env.From, &ch.From}, {env.Until, &ch.Until}} {
		if f.in == nil {
			continue
		}
		t, err := time.Parse(time.RFC3339, *f.in)
		if err != nil {
			return nil, errors.New("from and until are RFC 3339 times")
		}
		*f.out = t.UTC()
	}
	return ch, nil
}

func (s *server) adminPutTenantLimits(c *call)       { s.putSheet(c, false) }
func (s *server) adminPutTenantCapabilities(c *call) { s.putSheet(c, true) }

// putSheet is PUT …/limits and PUT …/capabilities.
func (s *server) putSheet(c *call, capsOnly bool) {
	var req struct {
		Values  map[string]json.RawMessage `json:"values"`
		Confirm string                     `json:"confirm"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	t, ok := s.visibleTenant(c)
	if !ok {
		return
	}
	if len(req.Values) == 0 {
		writeError(c.w, http.StatusBadRequest, "invalid", "values is an object of the keys to change.")
		return
	}
	changes := map[string]*sheets.Change{}
	for k, raw := range req.Values {
		d, known := sheets.Lookup(k)
		if !known || capsOnly && !d.Capability {
			writeError(c.w, http.StatusBadRequest, "invalid", k+" is not a key of this sheet.")
			return
		}
		ch, err := valueIn(raw)
		if err != nil {
			writeError(c.w, http.StatusBadRequest, "invalid", k+": "+err.Error())
			return
		}
		changes[k] = ch
	}
	ctx := c.r.Context()
	res, err := s.limits.SetManual(ctx, t.ID, c.user().ID, changes)
	var ve *sheets.ValueError
	switch {
	case errors.As(err, &ve):
		writeError(c.w, http.StatusBadRequest, "invalid", ve.Error())
		return
	case errors.Is(err, store.ErrNotFound):
		tenantNotFound(c)
		return
	case err != nil:
		s.fail(c.w, err)
		return
	}
	counted := false
	for _, a := range res.Applied {
		s.auditLimit(c, t.ID, a)
		if d, _ := sheets.Lookup(a.Key); !d.Capability {
			counted = true
		}
	}
	// The totals may have moved over, or back under, what is used.
	if counted && s.quota != nil {
		_ = s.quota.Evaluate(ctx, []store.Touched{{TenantID: t.ID}})
	}
	var warn *limitWarningResp
	if len(res.Replaced) > 0 {
		warn = &limitWarningResp{"undated_value_replaced", res.Replaced,
			"There is one manual value for each key. A dated value replaced the undated one: when its window ends, the value is not the undated one but what is below it (billing, the plan, the site)."}
	}
	s.writeSheet(c, t.ID, capsOnly, warn)
}

// windowDetail is the window of an audit row, nil for none.
func windowDetail(a sheets.Applied) any {
	if a.Start.IsZero() && a.End.IsZero() {
		return nil
	}
	return map[string]any{"from": optTime(a.Start), "until": optTime(a.End)}
}

// auditLimit records a change of one key of a tenant's sheet made by a person.
func (s *server) auditLimit(c *call, tenant string, a sheets.Applied) {
	kind := "tenant_limit_changed"
	if d, _ := sheets.Lookup(a.Key); d.Capability {
		kind = "capability_changed"
	}
	s.audit(c, auth.AuditEvent{Kind: kind, Actor: c.user().ID, Tenant: tenant,
		Detail: map[string]any{"tenant": tenant, "key": a.Key, "from": a.From, "to": a.To, "source": string(a.Source), "window": windowDetail(a)}})
}

// auditBoundary records a dated value that started or ended: nobody did it.
func (s *server) auditBoundary(tenant string, a sheets.Applied) {
	if s.auth == nil {
		return
	}
	s.auth.Audit(auth.AuditEvent{Kind: "tenant_limit_changed", Tenant: tenant,
		Detail: map[string]any{"tenant": tenant, "key": a.Key, "from": a.From, "to": a.To, "source": string(a.Source), "window": nil}})
}

// --- the site's defaults ----------------------------------------------------

type capabilityDefResp struct {
	Key         string `json:"key"`
	Kind        string `json:"kind"`
	Group       string `json:"group"`
	Description string `json:"description"`
	Default     any    `json:"default"`
	Value       any    `json:"value"`
	Capability  bool   `json:"capability"`
}

func (s *server) siteDefaults() []capabilityDefResp {
	site := s.limits.SiteValues()
	out := make([]capabilityDefResp, 0, len(site))
	for _, e := range site {
		d, _ := sheets.Lookup(e.Key)
		out = append(out, capabilityDefResp{Key: d.Key, Kind: d.Kind.String(), Group: strings.SplitN(d.Key, ".", 2)[0], Description: d.Description,
			Default: d.Default, Value: e.Value, Capability: d.Capability})
	}
	return out
}

func (s *server) adminGetCapabilities(c *call) {
	writeJSON(c.w, http.StatusOK, map[string]any{"capabilities": s.siteDefaults()})
}

func (s *server) adminPutCapabilities(c *call) {
	var req struct {
		Values map[string]json.RawMessage `json:"values"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	if len(req.Values) == 0 {
		writeError(c.w, http.StatusBadRequest, "invalid", "values is an object of the keys to change.")
		return
	}
	changes := map[string]json.RawMessage{}
	for k, raw := range req.Values {
		ch, err := valueIn(raw)
		if err != nil || ch != nil && (!ch.From.IsZero() || !ch.Until.IsZero()) {
			writeError(c.w, http.StatusBadRequest, "invalid", k+": the site's default has no dates; a value, or null for the built-in default.")
			return
		}
		if ch == nil {
			changes[k] = nil
		} else {
			changes[k] = ch.Value
		}
	}
	affected, err := s.limits.SetSite(c.r.Context(), changes)
	var ve *sheets.ValueError
	if errors.As(err, &ve) {
		writeError(c.w, http.StatusBadRequest, "invalid", ve.Error())
		return
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	s.audit(c, auth.AuditEvent{Kind: "capabilities_default_changed", Actor: c.user().ID, Detail: map[string]any{"values": req.Values, "affected": affected}})
	writeJSON(c.w, http.StatusOK, map[string]any{"affected": affected, "capabilities": s.siteDefaults()})
}

// --- plans ----------------------------------------------------------------------

type planResp struct {
	Slug        string         `json:"slug"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Values      map[string]any `json:"values"`
	Tenants     int            `json:"tenants"`
}

func (s *server) toPlan(p store.Plan) planResp {
	vals, _ := sheets.PlanValues(p.Values)
	if vals == nil {
		vals = map[string]any{}
	}
	return planResp{p.Slug, p.Name, p.Description, vals, s.limits.PlanTenants()[p.ID]}
}

func (s *server) adminListPlans(c *call) {
	all, err := store.ListPlans(c.r.Context(), s.opt.Store.Q())
	if err != nil {
		s.fail(c.w, err)
		return
	}
	out := make([]planResp, 0, len(all))
	for _, p := range all {
		out = append(out, s.toPlan(p))
	}
	writeJSON(c.w, http.StatusOK, map[string]any{"plans": out})
}

func (s *server) planBySlug(c *call) (*store.Plan, bool) {
	p, err := store.GetPlanBySlug(c.r.Context(), s.opt.Store.Q(), c.r.PathValue("slug"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(c.w, http.StatusNotFound, "not_found", "No such plan.")
		return nil, false
	}
	if err != nil {
		s.fail(c.w, err)
		return nil, false
	}
	return p, true
}

func (s *server) adminGetPlan(c *call) {
	if p, ok := s.planBySlug(c); ok {
		writeJSON(c.w, http.StatusOK, s.toPlan(*p))
	}
}

// planFields checks the name and description of a plan.
func planFields(name, desc *string) (string, string, bool) {
	var n, d string
	if name != nil {
		var ok bool
		if n, ok = cleanTenantName(*name); !ok {
			return "", "", false
		}
	}
	if desc != nil {
		d = strings.TrimSpace(*desc)
		if len([]rune(d)) > 500 {
			return "", "", false
		}
	}
	return n, d, true
}

func (s *server) adminCreatePlan(c *call) {
	var req struct {
		Slug        string                     `json:"slug"`
		Name        *string                    `json:"name"`
		Description *string                    `json:"description"`
		Values      map[string]json.RawMessage `json:"values"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	ctx := c.r.Context()
	name, desc, ok := planFields(req.Name, req.Description)
	if !ok || req.Name == nil || !tenantSlugRE.MatchString(req.Slug) {
		writeError(c.w, http.StatusBadRequest, "invalid", "A plan has a slug (2 to 32 characters of a-z, 0-9 and -), a name (1 to 80 characters) and a description of at most 500.")
		return
	}
	values, err := sheets.ParseValues(req.Values)
	if err != nil {
		writeError(c.w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	if all, err := store.ListPlans(ctx, s.opt.Store.Q()); err != nil {
		s.fail(c.w, err)
		return
	} else if len(all) >= MaxPlans {
		writeError(c.w, http.StatusConflict, "plan_limit", "There are at most 100 plans.")
		return
	}
	p := store.Plan{ID: page.NewID(s.now()), Slug: req.Slug, Name: name, Description: desc, Values: values}
	if err := store.InsertPlan(ctx, s.opt.Store.Q(), p); errors.Is(err, store.ErrExists) {
		writeError(c.w, http.StatusConflict, "exists", "That slug is taken.")
		return
	} else if err != nil {
		s.fail(c.w, err)
		return
	}
	if _, err := s.limits.Reload(ctx); err != nil {
		s.fail(c.w, err)
		return
	}
	s.audit(c, auth.AuditEvent{Kind: "plan_created", Actor: c.user().ID, Detail: map[string]any{"plan": p.Slug, "values": req.Values, "affected": []string{}}})
	c.w.Header().Set("Location", "/api/admin/plans/"+p.Slug)
	writeJSON(c.w, http.StatusCreated, s.toPlan(p))
}

func (s *server) adminPatchPlan(c *call) {
	var raw map[string]json.RawMessage
	if !s.readJSONMax(c.w, c.r, &raw, false, authBodyLimit) {
		return
	}
	var req struct {
		Name        *string                    `json:"name"`
		Description *string                    `json:"description"`
		Values      map[string]json.RawMessage `json:"values"`
	}
	for k, v := range raw {
		var err error
		switch k {
		case "name":
			err = json.Unmarshal(v, &req.Name)
		case "description":
			err = json.Unmarshal(v, &req.Description)
		case "values":
			err = json.Unmarshal(v, &req.Values)
		default:
			err = errors.New("unknown field")
		}
		if err != nil {
			writeError(c.w, http.StatusBadRequest, "invalid", "A plan changes by name, description and values: "+k+".")
			return
		}
	}
	p, ok := s.planBySlug(c)
	if !ok {
		return
	}
	name, desc, valid := planFields(req.Name, req.Description)
	if !valid || req.Name != nil && name == "" {
		writeError(c.w, http.StatusBadRequest, "invalid", "A plan's name is 1 to 80 characters and its description at most 500.")
		return
	}
	if req.Name != nil {
		p.Name = name
	}
	if req.Description != nil {
		p.Description = desc
	}
	if req.Values != nil {
		// The keys given are set; a key with null leaves the plan.
		cur := map[string]json.RawMessage{}
		_ = json.Unmarshal([]byte(p.Values), &cur)
		for k, raw := range req.Values {
			if string(bytes.TrimSpace(raw)) == "null" {
				if _, known := sheets.Lookup(k); !known {
					writeError(c.w, http.StatusBadRequest, "invalid", k+" is not a key of the limits sheet.")
					return
				}
				delete(cur, k)
			} else {
				ch, err := valueIn(raw)
				if err != nil || ch == nil || !ch.From.IsZero() || !ch.Until.IsZero() {
					writeError(c.w, http.StatusBadRequest, "invalid", k+": a plan's value has no dates; a value, or null to leave it out.")
					return
				}
				cur[k] = ch.Value
			}
		}
		v, err := sheets.ParseValues(cur)
		if err != nil {
			writeError(c.w, http.StatusBadRequest, "invalid", err.Error())
			return
		}
		p.Values = v
	}
	ctx := c.r.Context()
	if err := store.UpdatePlan(ctx, s.opt.Store.Q(), *p); err != nil {
		s.fail(c.w, err)
		return
	}
	affected, err := s.limits.Reload(ctx)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	s.audit(c, auth.AuditEvent{Kind: "plan_changed", Actor: c.user().ID, Detail: map[string]any{"plan": p.Slug, "values": req.Values, "affected": affected}})
	if s.quota != nil && len(affected) > 0 {
		for _, id := range affected {
			_ = s.quota.Evaluate(ctx, []store.Touched{{TenantID: id}})
		}
	}
	writeJSON(c.w, http.StatusOK, struct {
		planResp
		Affected []string `json:"affected"`
	}{s.toPlan(*p), affected})
}

func (s *server) adminDeletePlan(c *call) {
	p, ok := s.planBySlug(c)
	if !ok {
		return
	}
	ctx := c.r.Context()
	if n := s.limits.PlanTenants()[p.ID]; n > 0 {
		writeJSON(c.w, http.StatusConflict, struct {
			Error   string `json:"error"`
			Message string `json:"message"`
			Tenants int    `json:"tenants"`
		}{"plan_in_use", "Tenants use this plan; give them another first.", n})
		return
	}
	if err := store.DeletePlan(ctx, s.opt.Store.Q(), p.ID); err != nil {
		s.fail(c.w, err)
		return
	}
	if _, err := s.limits.Reload(ctx); err != nil {
		s.fail(c.w, err)
		return
	}
	s.audit(c, auth.AuditEvent{Kind: "plan_deleted", Actor: c.user().ID, Detail: map[string]any{"plan": p.Slug, "values": json.RawMessage(p.Values), "affected": []string{}}})
	c.w.WriteHeader(http.StatusNoContent)
}

// adminPutTenantPlan gives a tenant a plan, or none, and clears its manual
// overrides: the answer lists what it cleared.
func (s *server) adminPutTenantPlan(c *call) {
	var req struct {
		Plan    *string `json:"plan"`
		Confirm string  `json:"confirm"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	t, ok := s.visibleTenant(c)
	if !ok {
		return
	}
	ctx := c.r.Context()
	var plan *store.Plan
	if req.Plan != nil {
		p, err := store.GetPlanBySlug(ctx, s.opt.Store.Q(), *req.Plan)
		if errors.Is(err, store.ErrNotFound) {
			writeError(c.w, http.StatusBadRequest, "invalid", "There is no plan "+*req.Plan+".")
			return
		}
		if err != nil {
			s.fail(c.w, err)
			return
		}
		plan = p
	}
	before, err := s.planRef(ctx, t.ID)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	id := ""
	if plan != nil {
		id = plan.ID
	}
	cleared, err := s.limits.SetPlan(ctx, t.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		tenantNotFound(c)
		return
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	type clearedResp struct {
		Key   string  `json:"key"`
		Value any     `json:"value"`
		From  *string `json:"from"`
		Until *string `json:"until"`
	}
	list := make([]clearedResp, 0, len(cleared))
	keys := make([]string, 0, len(cleared))
	for _, x := range cleared {
		list = append(list, clearedResp{x.Key, x.Value, optTime(x.From), optTime(x.Until)})
		keys = append(keys, x.Key)
	}
	var from, to any
	if before != nil {
		from = before.Slug
	}
	if plan != nil {
		to = plan.Slug
	}
	s.audit(c, auth.AuditEvent{Kind: "tenant_plan_changed", Actor: c.user().ID, Tenant: t.ID, Detail: map[string]any{"tenant": t.ID, "from": from, "to": to, "cleared": keys}})
	if s.quota != nil {
		_ = s.quota.Evaluate(ctx, []store.Touched{{TenantID: t.ID}})
	}
	ref, err := s.planRef(ctx, t.ID)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, map[string]any{"plan": ref, "cleared": list})
}
