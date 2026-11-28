package api

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/domains"
	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
)

// The email domains a tenant claims and verifies by DNS (3.6 d2, docs/api.md
// "Email domains"). The rules are internal/domains'; here are the routes:
// a tenant's administrators manage their own tenant's (tenant.domains.manage,
// features.domains), a site administrator any tenant's, and the site's list
// and transfer are the site's (site.tenants.manage).

type domainTXTResp struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type domainCNAMEResp struct {
	Name   string `json:"name"`
	Target string `json:"target"`
}

// domainRecordsResp are the records that prove a claim, with its code: the
// CNAME only when the server has a public address.
type domainRecordsResp struct {
	TXT   domainTXTResp    `json:"txt"`
	CNAME *domainCNAMEResp `json:"cname"`
}

type domainCheckResp struct {
	At     string   `json:"at"`
	By     string   `json:"by"`
	Result string   `json:"result"`
	Via    string   `json:"via"`
	Seen   []string `json:"seen"`
	Error  string   `json:"error"`
}

type domainTenantResp struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type domainResp struct {
	Domain  string `json:"domain"`
	Display string `json:"display"`
	// MixedScript warns that a label mixes scripts (a lookalike of another name).
	MixedScript  bool             `json:"mixedScript"`
	State        string           `json:"state"`
	VerifiedAt   *string          `json:"verifiedAt"`
	MissingSince *string          `json:"missingSince"`
	LapsedAt     *string          `json:"lapsedAt"`
	CheckedAt    *string          `json:"checkedAt"`
	NextCheckAt  *string          `json:"nextCheckAt"`
	CodeExpires  *string          `json:"codeExpires"`
	CodeExpired  bool             `json:"codeExpired"`
	LastCheck    *domainCheckResp `json:"lastCheck"`
	// NextCheckAllowedAt is when "Check now" may be asked again after the
	// last check by hand (null when none was made by hand).
	NextCheckAllowedAt *string `json:"nextCheckAllowedAt"`
	RestrictOrgs       bool    `json:"restrictOrgs"`
	// Provider is the id of the tenant's own sign-in provider the domain
	// routes to (3.6 d3; null for none). Join is how people of the domain
	// join the tenant at sign-up (3.6 d5): off, auto or request.
	Provider *string `json:"provider"`
	Join     string  `json:"join"`
	// RecordName is where the record goes. Code and Records are given only by
	// the answer that made the code (the claim, a new code): it is stored as a
	// hash, so it is shown that once; otherwise null.
	RecordName string             `json:"recordName"`
	Code       *string            `json:"code"`
	Records    *domainRecordsResp `json:"records"`
	Created    string             `json:"created"`
	// Tenant is the claim's tenant, in the site's list only.
	Tenant *domainTenantResp `json:"tenant,omitempty"`
}

// domainState is a claim's state: verified (and missing, while the recheck
// does not find its record), lapsed (the recheck cancelled it), or unverified.
func domainState(r store.DomainRow) string {
	switch {
	case r.Verified() && !r.MissingSince.IsZero():
		return "missing"
	case r.Verified():
		return "verified"
	case !r.LapsedAt.IsZero():
		return "lapsed"
	}
	return "unverified"
}

func checkOf(c *domains.Check) *domainCheckResp {
	if c == nil {
		return nil
	}
	seen := c.Seen
	if seen == nil {
		seen = []string{}
	}
	return &domainCheckResp{At: rfc3339(c.At), By: c.By, Result: c.Result, Via: c.Via, Seen: seen, Error: c.Error}
}

func (s *server) domainOf(r store.DomainRow, code string) domainResp {
	out := domainResp{Domain: r.Domain, Display: domains.Display(r.Domain), MixedScript: domains.MixedScript(r.Domain), State: domainState(r),
		VerifiedAt: optTime(r.VerifiedAt), MissingSince: optTime(r.MissingSince), LapsedAt: optTime(r.LapsedAt), CheckedAt: optTime(r.CheckedAt),
		NextCheckAt: optTime(r.NextCheckAt), CodeExpires: optTime(r.CodeExpires), LastCheck: checkOf(domains.ParseCheck(r.LastCheck)),
		RestrictOrgs: r.RestrictOrgs, Join: domains.JoinModeName(r.JoinMode), RecordName: domains.RecordName(r.Domain), Created: rfc3339(r.Created)}
	if r.ProviderID != "" {
		p := r.ProviderID
		out.Provider = &p
	}
	if !r.Verified() {
		out.CodeExpired = !r.CodeExpires.After(s.now())
	}
	if lc := out.LastCheck; lc != nil && lc.By == domains.ByManual {
		if c := domains.ParseCheck(r.LastCheck); c != nil {
			out.NextCheckAllowedAt = optTime(c.At.Add(time.Minute))
		}
	}
	if code != "" {
		out.Code = &code
		out.Records = &domainRecordsResp{TXT: domainTXTResp{domains.RecordName(r.Domain), domains.TXTValue(code)}}
		if h := s.domains.SiteHost(); h != "" {
			out.Records.CNAME = &domainCNAMEResp{domains.RecordName(r.Domain), domains.CNAMETarget(code, h)}
		}
	}
	return out
}

// failDomain answers a refusal of internal/domains (429 with Retry-After).
func (s *server) failDomain(c *call, err error) {
	var de *domains.Error
	if !errors.As(err, &de) {
		s.fail(c.w, err)
		return
	}
	if de.Status == http.StatusTooManyRequests {
		c.w.Header().Set("Retry-After", strconv.Itoa(max(int((de.RetryAfter+time.Second-1)/time.Second), 1)))
	}
	writeError(c.w, de.Status, de.Code, de.Msg)
}

// domainTenant is the {tenant} of a domain route: one the caller may manage
// the domains of, else not found.
func (s *server) domainTenant(c *call) (string, bool) {
	id := c.r.PathValue("tenant")
	if !c.p.Access.Can(authz.TenantDomainsManage, id) || !s.knownTenant(id) {
		tenantNotFound(c)
		return "", false
	}
	return id, true
}

// adminListDomains is GET /api/admin/tenants/{tenant}/domains.
func (s *server) adminListDomains(c *call) {
	t, ok := s.domainTenant(c)
	if !ok {
		return
	}
	rows, err := store.TenantDomains(c.r.Context(), s.opt.Store.Q(), t)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	out := make([]domainResp, len(rows))
	for i, r := range rows {
		out[i] = s.domainOf(r, "")
	}
	writeJSON(c.w, http.StatusOK, struct {
		Domains  []domainResp `json:"domains"`
		SiteHost string       `json:"siteHost"`
	}{out, s.domains.SiteHost()})
}

// adminClaimDomain is POST /api/admin/tenants/{tenant}/domains {"domain"}.
func (s *server) adminClaimDomain(c *call) {
	t, ok := s.domainTenant(c)
	if !ok {
		return
	}
	var req struct {
		Domain string `json:"domain"`
	}
	if !s.readJSON(c.w, c.r, &req, false) {
		return
	}
	cl, err := s.domains.Claim(c.r.Context(), t, req.Domain, c.user().ID)
	if err != nil {
		s.failDomain(c, err)
		return
	}
	s.audit(c, auth.AuditEvent{Kind: "domain_added", Actor: c.user().ID, Tenant: t, Detail: map[string]any{"domain": cl.Row.Domain}})
	writeJSON(c.w, http.StatusCreated, s.domainOf(cl.Row, cl.Code))
}

// adminDomainCode is POST …/domains/{domain}/code: a new code for a claim
// that is not verified.
func (s *server) adminDomainCode(c *call) {
	t, ok := s.domainTenant(c)
	if !ok {
		return
	}
	cl, err := s.domains.NewCode(c.r.Context(), t, c.r.PathValue("domain"))
	if err != nil {
		s.failDomain(c, err)
		return
	}
	s.audit(c, auth.AuditEvent{Kind: "domain_code_made", Actor: c.user().ID, Tenant: t, Detail: map[string]any{"domain": cl.Row.Domain}})
	writeJSON(c.w, http.StatusOK, s.domainOf(cl.Row, cl.Code))
}

// adminVerifyDomain is POST …/domains/{domain}/verify: "Check now".
func (s *server) adminVerifyDomain(c *call) {
	t, ok := s.domainTenant(c)
	if !ok {
		return
	}
	res, err := s.domains.Verify(c.r.Context(), t, c.r.PathValue("domain"))
	if res.Check.Result != "" {
		// Every check that asked DNS is audited, a refused verification too.
		detail := map[string]any{"domain": res.Row.Domain, "result": res.Check.Result}
		if res.Check.Via != "" {
			detail["via"] = res.Check.Via
		}
		if len(res.Check.Seen) > 0 {
			detail["seen"] = res.Check.Seen
		}
		if res.Claimed {
			detail["claimed"] = true
		}
		s.audit(c, auth.AuditEvent{Kind: "domain_checked", Actor: c.user().ID, Tenant: t, Detail: detail})
		if res.Verified {
			s.audit(c, auth.AuditEvent{Kind: "domain_verified", Actor: c.user().ID, Tenant: t, Detail: map[string]any{"domain": res.Row.Domain, "via": res.Check.Via}})
		}
	}
	if err != nil {
		s.failDomain(c, err)
		return
	}
	// The claim, with this check as its lastCheck (verified now if it found the record).
	writeJSON(c.w, http.StatusOK, s.domainOf(res.Row, ""))
}

// adminPatchDomain is PATCH …/domains/{domain} {"restrictOrgs", "provider",
// "join"}, any of them: provider routes a verified domain to one of the
// tenant's own sign-in providers (its id; null for none), which needs
// tenant.signin.manage and features.ownSso too (3.6 d3); join is how people
// of the domain join the tenant at sign-up (off, auto, request; 3.6 d5).
// Turning join off while requests wait cancels them, which needs the
// confirmation (domain_join_off, in "confirm").
func (s *server) adminPatchDomain(c *call) {
	t, ok := s.domainTenant(c)
	if !ok {
		return
	}
	var raw map[string]json.RawMessage
	if !s.readJSON(c.w, c.r, &raw, false) {
		return
	}
	var restrict bool
	var provider *string
	var join, confirm string
	setProvider := false
	for k, v := range raw {
		switch k {
		case "restrictOrgs":
			if json.Unmarshal(v, &restrict) != nil {
				writeError(c.w, http.StatusBadRequest, "invalid", "RestrictOrgs is true or false.")
				return
			}
		case "provider":
			setProvider = true
			if json.Unmarshal(v, &provider) != nil {
				writeError(c.w, http.StatusBadRequest, "invalid", "Provider is the id of one of the organisation's sign-in providers, or null.")
				return
			}
		case "join":
			if json.Unmarshal(v, &join) != nil {
				writeError(c.w, http.StatusBadRequest, "invalid", "Join is off, auto or request.")
				return
			}
			if _, ok := domains.JoinModeOf(join); !ok {
				writeError(c.w, http.StatusBadRequest, "invalid", "Join is off, auto or request.")
				return
			}
		case "confirm":
			if json.Unmarshal(v, &confirm) != nil {
				writeError(c.w, http.StatusBadRequest, "invalid", "Confirm is the token of POST /api/admin/confirm.")
				return
			}
		default:
			writeError(c.w, http.StatusBadRequest, "invalid", "Unknown field "+k+": only restrictOrgs, provider and join can be changed.")
			return
		}
	}
	_, setRestrict := raw["restrictOrgs"]
	_, setJoin := raw["join"]
	if !setRestrict && !setProvider && !setJoin {
		writeError(c.w, http.StatusBadRequest, "invalid", "Give restrictOrgs, provider or join.")
		return
	}
	if setJoin && join == "off" && !s.joinOffGate(c, t, c.r.PathValue("domain"), confirm) {
		return
	}
	pid := ""
	if setProvider {
		if !c.p.Access.Can(authz.TenantSigninManage, t) {
			writeError(c.w, http.StatusForbidden, "forbidden", "Routing a domain to a sign-in provider takes tenant.signin.manage.")
			return
		}
		// The tenant's own provider needs features.ownSso; a site SSO provider
		// offered to the tenant (3.6 d4) does not. Clearing the route is held
		// to the feature as in d3.
		own := true
		if provider != nil {
			pid = *provider
			var site bool
			if own, site = s.auth.RoutableFor(c.r.Context(), pid, t); pid == "" || !own && !site {
				writeError(c.w, http.StatusNotFound, "not_found", "No such provider.")
				return
			}
		}
		if own {
			if on, err := s.settings.Feature(c.r.Context(), t, settings.FeatureOwnSSO); err != nil {
				s.fail(c.w, err)
				return
			} else if !on {
				writeError(c.w, http.StatusForbidden, "feature_off", "This organisation does not have its own single sign-on (features.ownSso).")
				return
			}
		}
	}
	var r store.DomainRow
	var err error
	// The routing is checked first (a provider needs a verified domain), so a
	// PATCH it refuses changes nothing.
	if setProvider {
		if r, err = s.domains.SetProvider(c.r.Context(), t, c.r.PathValue("domain"), pid); err != nil {
			s.failDomain(c, err)
			return
		}
	}
	if setJoin {
		if r, err = s.domains.SetJoin(c.r.Context(), t, c.r.PathValue("domain"), join, domains.Who{ID: c.user().ID, IP: s.auth.ClientAddr(c.r)}); err != nil {
			s.failDomain(c, err)
			return
		}
	}
	if setRestrict {
		if r, err = s.domains.SetRestrict(c.r.Context(), t, c.r.PathValue("domain"), restrict); err != nil {
			s.failDomain(c, err)
			return
		}
	}
	detail := map[string]any{"domain": r.Domain}
	if setRestrict {
		detail["restrictOrgs"] = restrict
	}
	if setJoin {
		detail["join"] = join
	}
	if setProvider {
		detail["provider"] = provider
	}
	s.audit(c, auth.AuditEvent{Kind: "domain_changed", Actor: c.user().ID, Tenant: t, Detail: detail})
	writeJSON(c.w, http.StatusOK, s.domainOf(r, ""))
}

// adminDeleteDomain is DELETE …/domains/{domain}[?confirm=]: removing a
// domain people asked to join by cancels their requests, which needs the
// confirmation (domain_join_off).
func (s *server) adminDeleteDomain(c *call) {
	t, ok := s.domainTenant(c)
	if !ok {
		return
	}
	if !s.joinOffGate(c, t, c.r.PathValue("domain"), c.r.URL.Query().Get("confirm")) {
		return
	}
	r, err := s.domains.Remove(c.r.Context(), t, c.r.PathValue("domain"), domains.Who{ID: c.user().ID, IP: s.auth.ClientAddr(c.r)})
	if err != nil {
		s.failDomain(c, err)
		return
	}
	s.audit(c, auth.AuditEvent{Kind: "domain_removed", Actor: c.user().ID, Tenant: t, Detail: map[string]any{"domain": r.Domain, "verified": r.Verified()}})
	c.w.WriteHeader(http.StatusNoContent)
}

// joinOffPlan is the confirmation's plan of domain_join_off: the pending
// requests of the tenant's domain that turning its join off, or removing it,
// would cancel. The caller must manage the tenant's domains (404 otherwise).
func (s *server) joinOffPlan(c *call, req confirmRequest) (confirmPlan, bool) {
	if req.Tenant == "" || !c.p.Access.Can(authz.TenantDomainsManage, req.Tenant) || !s.knownTenant(req.Tenant) {
		writeError(c.w, http.StatusNotFound, "not_found", "No such domain.")
		return confirmPlan{}, false
	}
	key, ok := domains.Key(req.Domain)
	if !ok {
		writeError(c.w, http.StatusNotFound, "not_found", "No such domain.")
		return confirmPlan{}, false
	}
	ids, err := s.domains.PendingJoins(c.r.Context(), req.Tenant, key)
	if err != nil {
		s.fail(c.w, err)
		return confirmPlan{}, false
	}
	n := len(ids)
	plan := []any{joinOffItem{Tenant: req.Tenant, Domain: key, Requests: n}}
	return confirmPlan{Plan: plan, Hash: hashOf(append([]string{req.Tenant, key}, ids...)...)}, true
}

// joinOffItem is the line of domain_join_off's plan.
type joinOffItem struct {
	Tenant   string `json:"tenant"`
	Domain   string `json:"domain"`
	Requests int    `json:"requests"`
}

// joinOffGate is the check before a change that cancels a domain's pending
// requests to join (its join turned off, or its removal): none pending, it
// goes ahead; else it needs the confirmation, and without one is answered 409
// confirm_required with the plan. It reports false after answering.
func (s *server) joinOffGate(c *call, tenant, domain, tok string) bool {
	ids, err := s.domains.PendingJoins(c.r.Context(), tenant, domain)
	if err != nil {
		s.fail(c.w, err)
		return false
	}
	if len(ids) == 0 {
		return true
	}
	req := confirmRequest{Action: "domain_join_off", Tenant: tenant, Domain: domain}
	if tok == "" {
		p, ok := s.joinOffPlan(c, req)
		if !ok {
			return false
		}
		writeJSON(c.w, http.StatusConflict, errorBody{Error: "confirm_required", Plan: p.Plan,
			Message: "People are waiting to join by this domain: ask POST /api/admin/confirm (domain_join_off) for what would be cancelled, and send its confirm."})
		return false
	}
	return s.useConfirm(c, tok, req)
}

// adminSiteDomains is GET /api/admin/domains: every tenant's claims, by
// domain (?verified=true for the held ones), in pages.
func (s *server) adminSiteDomains(c *call) {
	q := c.r.URL.Query()
	limit := 100
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			writeError(c.w, http.StatusBadRequest, "invalid", "Limit must be from 1 to 500.")
			return
		}
		limit = n
	}
	f := store.DomainFilter{Limit: limit + 1}
	switch q.Get("verified") {
	case "", "false":
	case "true":
		f.VerifiedOnly = true
	default:
		writeError(c.w, http.StatusBadRequest, "invalid", "Verified is true or false.")
		return
	}
	if cur := q.Get("cursor"); cur != "" {
		d, t, ok := strings.Cut(cur, "/")
		if !ok || d == "" || t == "" {
			writeError(c.w, http.StatusBadRequest, "invalid", "That is not a cursor of this list.")
			return
		}
		f.AfterDomain, f.AfterTenant = d, t
	}
	rows, err := store.ListDomains(c.r.Context(), s.opt.Store.Q(), f)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	var next *string
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		n := last.Domain + "/" + last.TenantID
		next = &n
	}
	out := make([]domainResp, len(rows))
	for i, r := range rows {
		out[i] = s.domainOf(r, "")
		ref, _ := s.opt.Store.TenantRefOf(r.TenantID)
		out[i].Tenant = &domainTenantResp{r.TenantID, ref.Slug, ref.Name}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Domains []domainResp `json:"domains"`
		Next    *string      `json:"next"`
	}{out, next})
}

// SiteHostOf is the host name of the public address, which a CNAME record of
// a domain claim points under ("" without one).
// An IP address, or a name that is not a domain one could claim (localhost),
// has none: a CNAME points at a name. A Unicode name is given as punycode.
func SiteHostOf(publicURL string) string {
	u, err := url.Parse(publicURL)
	if err != nil {
		return ""
	}
	h := u.Hostname()
	if h == "" || net.ParseIP(h) != nil {
		return ""
	}
	k, ok := domains.Key(h)
	if !ok {
		return ""
	}
	return k
}

// adminAssignDomain is POST /api/admin/domains/{domain}/assign {"tenant"}: a
// site administrator moves a verified domain to another tenant. Each tenant's
// audit says its side only.
func (s *server) adminAssignDomain(c *call) {
	var req struct {
		Tenant string `json:"tenant"`
	}
	if !s.readJSON(c.w, c.r, &req, false) {
		return
	}
	if !s.knownTenant(req.Tenant) {
		tenantNotFound(c)
		return
	}
	from, r, err := s.domains.Assign(c.r.Context(), c.r.PathValue("domain"), req.Tenant, c.user().ID)
	if err != nil {
		s.failDomain(c, err)
		return
	}
	if from != req.Tenant {
		s.audit(c, auth.AuditEvent{Kind: "domain_unassigned", Actor: c.user().ID, Tenant: from, Detail: map[string]any{"domain": r.Domain}})
		s.audit(c, auth.AuditEvent{Kind: "domain_assigned", Actor: c.user().ID, Tenant: req.Tenant, Detail: map[string]any{"domain": r.Domain}})
	}
	out := s.domainOf(r, "")
	ref, _ := s.opt.Store.TenantRefOf(r.TenantID)
	out.Tenant = &domainTenantResp{r.TenantID, ref.Slug, ref.Name}
	writeJSON(c.w, http.StatusOK, out)
}
