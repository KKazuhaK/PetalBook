package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
)

// The layered settings of docs/api.md ("Settings by layer").

// failSetting answers a refusal of the settings package.
func (s *server) failSetting(c *call, err error) {
	var se *settings.Error
	if !errors.As(err, &se) {
		s.fail(c.w, err)
		return
	}
	status := http.StatusBadRequest
	switch se.Code {
	case settings.CodeNotFound:
		status = http.StatusNotFound
	case settings.CodeConflict, settings.CodeLocked, settings.CodeRestricted, "mail_not_configured", "sso_only_with_registration":
		status = http.StatusConflict
	}
	writeError(c.w, status, se.Code, se.Msg)
}

type siteSettingResp struct {
	Key    string            `json:"key"`
	Kind   settings.Kind     `json:"kind"`
	Layers []settings.Layer  `json:"layers"`
	Site   settings.SiteView `json:"site"`
	// Choices are the values of a choice setting, loosest first.
	Choices []string `json:"choices,omitempty"`
	// A setting with no tenant layer has one of these: ForTenant, a site
	// administrator may set it for one tenant (siteRule or siteLock on the
	// tenant's settings); ServerWide, why it is the same for every tenant.
	ForTenant  bool   `json:"forTenant,omitempty"`
	ServerWide string `json:"serverWide,omitempty"`
}

type tenantSettingResp struct {
	Key       string               `json:"key"`
	Kind      settings.Kind        `json:"kind"`
	Layers    []settings.Layer     `json:"layers"`
	Site      tenantSiteView       `json:"site"`
	Tenant    *settings.TenantView `json:"tenant"`
	Effective any                  `json:"effective"`
	// Choices are the values of a choice setting, loosest first.
	Choices []string `json:"choices,omitempty"`
}

func tenantSettingOf(e *settings.Effective) tenantSettingResp {
	d := e.Def
	return tenantSettingResp{d.Key, d.Kind, d.Layers, tenantSiteView{e.Site.Value, e.Site.Lower}, e.Tenant, e.Value, d.Choices}
}

// tenantSiteView is what a tenant is shown of the site's layer: the value and
// the rule, not whether a flag set it.
type tenantSiteView struct {
	Value any            `json:"value"`
	Lower settings.Lower `json:"lower"`
}

func (s *server) adminGetSettings(c *call) {
	eff, err := s.settings.For(c.r.Context(), "")
	if err != nil {
		s.fail(c.w, err)
		return
	}
	out := []siteSettingResp{}
	for _, d := range s.settings.Defs() {
		e := eff[d.Key]
		out = append(out, siteSettingResp{d.Key, d.Kind, d.Layers, e.Site, d.Choices, d.ForTenant, d.ServerWide})
	}
	writeJSON(c.w, http.StatusOK, struct {
		Settings []siteSettingResp `json:"settings"`
	}{out})
}

// readChange reads {"value": …, "lower": {…}} and, for a tenant's layer,
// "siteLock": which of them were given.
func (s *server) readChange(c *call, tenantLayer bool) (settings.Change, bool) {
	var req map[string]json.RawMessage
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return settings.Change{}, false
	}
	var ch settings.Change
	for k := range req {
		if k != "value" && k != "lower" && !(tenantLayer && (k == "siteLock" || k == "siteRule")) {
			writeError(c.w, http.StatusBadRequest, "invalid", "Unknown field "+k+".")
			return ch, false
		}
	}
	if raw, ok := req["value"]; ok {
		ch.HasValue = true
		if err := json.Unmarshal(raw, &ch.Value); err != nil {
			writeError(c.w, http.StatusBadRequest, "invalid", "Value is not JSON.")
			return ch, false
		}
	}
	if raw, ok := req["lower"]; ok {
		ch.HasLower = true
		if err := json.Unmarshal(raw, &ch.Lower); err != nil {
			writeError(c.w, http.StatusBadRequest, "invalid", "Lower is {\"mode\": …}.")
			return ch, false
		}
	}
	if raw, ok := req["siteLock"]; ok {
		ch.HasSiteLock = true
		if err := json.Unmarshal(raw, &ch.SiteLock); err != nil {
			writeError(c.w, http.StatusBadRequest, "invalid", "SiteLock is true or false.")
			return ch, false
		}
	}
	if raw, ok := req["siteRule"]; ok {
		// {"value": …, "lower": {…}, "from": …, "until": …}, any of them; null
		// removes the manual rule. from and until are RFC 3339 times.
		ch.HasSiteRule = true
		if err := json.Unmarshal(raw, &ch.SiteRule); err != nil {
			writeError(c.w, http.StatusBadRequest, "invalid", "SiteRule is {\"value\": …, \"lower\": {…}, \"from\": …, \"until\": …} or null, the times in RFC 3339.")
			return ch, false
		}
	}
	if !ch.HasValue && !ch.HasLower && !ch.HasSiteLock && !ch.HasSiteRule {
		writeError(c.w, http.StatusBadRequest, "invalid", "Give value, lower, siteLock, siteRule or any of them.")
		return ch, false
	}
	return ch, true
}

// beforeOpening is the checks of "Registration" for a value that opens it.
// Registration needs mail (the next part of 3.3), so until it exists nothing
// opens; the check for ssoOnly joins here with it.
func (s *server) beforeOpening(ctx context.Context, key string, ch settings.Change, site bool) error {
	if key != settings.RegistrationOpen {
		return nil
	}
	opens := ch.HasValue && ch.Value == true || ch.HasSiteRule && ch.SiteRule != nil && ch.SiteRule.Value == true
	if !opens && site && ch.HasLower && ch.Lower != nil && ch.Lower.Mode != settings.Lock {
		// Unlocking lets a tenant's stored true take effect, or the site's for
		// one tenant.
		for _, l := range []settings.Layer{settings.Tenant, settings.Site} {
			on, err := store.AnyLayeredValue(ctx, s.opt.Store.Q(), string(l), key, "true")
			if err != nil {
				return err
			}
			opens = opens || on
		}
	}
	if !opens {
		return nil
	}
	if !s.registrationReady() {
		return &settings.Error{Code: "mail_not_configured", Msg: "Registration verifies addresses by mail, and mail is not enabled."}
	}
	if s.auth.SSOOnlyStored(ctx) {
		return &settings.Error{Code: "sso_only_with_registration", Msg: "Password sign-in is refused (ssoOnly), so nobody could finish registering; switch ssoOnly off first."}
	}
	return nil
}

// closedRegistration cancels the pending registrations of tenants that are not
// open now, after a change that may have closed one.
func (s *server) closedRegistration(ctx context.Context, key string) {
	if key != settings.RegistrationOpen {
		return
	}
	if err := s.auth.CancelClosedRegistrations(ctx); err != nil {
		s.log.Warn("could not cancel pending registrations", "error", err)
	}
}

func (s *server) adminPutSetting(c *call) {
	key := c.r.PathValue("key")
	ch, ok := s.readChange(c, false)
	if !ok {
		return
	}
	ctx := c.r.Context()
	if _, ok := s.settings.Def(key); !ok {
		s.failSetting(c, &settings.Error{Code: settings.CodeNotFound, Msg: "No such setting."})
		return
	}
	if err := s.beforeOpening(ctx, key, ch, true); err != nil {
		s.failSetting(c, err)
		return
	}
	before, err := s.settings.Get(ctx, "", key)
	if err != nil {
		s.failSetting(c, err)
		return
	}
	ch.By = c.user().ID
	affected, err := s.settings.SetSite(ctx, key, ch)
	if err != nil {
		s.failSetting(c, err)
		return
	}
	s.closedRegistration(ctx, key)
	s.quotaSettingChanged(ctx, key, store.AllTenants())
	after, err := s.settings.Get(ctx, "", key)
	if err != nil {
		s.failSetting(c, err)
		return
	}
	s.audit(c, auth.AuditEvent{Kind: "setting_changed", Actor: c.user().ID, Tenant: "-",
		Detail: map[string]any{"key": key, "layer": "site", "source": "manual", "from": before.Site, "to": after.Site}})
	d, _ := s.settings.Def(key)
	out := struct {
		siteSettingResp
		Affected *int `json:"affected,omitempty"`
	}{siteSettingResp: siteSettingResp{d.Key, d.Kind, d.Layers, after.Site, d.Choices, d.ForTenant, d.ServerWide}}
	if ch.HasLower {
		out.Affected = &affected
	}
	writeJSON(c.w, http.StatusOK, out)
}

func (s *server) tenantSettingsOf(ctx context.Context, tenant string) ([]tenantSettingResp, error) {
	eff, err := s.settings.For(ctx, tenant)
	if err != nil {
		return nil, err
	}
	out := []tenantSettingResp{}
	for _, d := range s.settings.Defs() {
		if !d.PerTenant() {
			continue
		}
		e := eff[d.Key]
		out = append(out, tenantSettingOf(e))
	}
	return out, nil
}

func (s *server) adminGetTenantSettings(c *call) {
	t, ok := s.visibleTenant(c)
	if !ok {
		return
	}
	out, err := s.tenantSettingsOf(c.r.Context(), t.ID)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Settings []tenantSettingResp `json:"settings"`
	}{out})
}

func (s *server) adminPutTenantSetting(c *call) {
	t, ok := s.visibleTenant(c)
	if !ok {
		return
	}
	key := c.r.PathValue("key")
	ch, ok := s.readChange(c, true)
	if !ok {
		return
	}
	ctx := c.r.Context()
	before, err := s.settings.Get(ctx, t.ID, key)
	if err != nil {
		s.failSetting(c, err)
		return
	}
	// What the site locks is refused as locked, before anything else is asked of it.
	if before.Site.Lower.Mode != settings.Lock {
		if err := s.beforeOpening(ctx, key, ch, false); err != nil {
			s.failSetting(c, err)
			return
		}
	}
	ch.Site, ch.By = c.siteWide(authz.TenantSettingsWrite), c.user().ID
	res, err := s.settings.SetTenantResult(ctx, t.ID, key, ch)
	if err != nil {
		s.failSetting(c, err)
		return
	}
	s.closedRegistration(ctx, key)
	s.quotaSettingChanged(ctx, key, store.InTenant(t.ID))
	after, err := s.settings.Get(ctx, t.ID, key)
	if err != nil {
		s.failSetting(c, err)
		return
	}
	detail := map[string]any{"key": key, "layer": "tenant", "from": before.Tenant, "to": after.Tenant}
	if ch.HasSiteRule {
		// A rule for the tenant: its source and window.
		detail["source"] = "manual"
		if r := ch.SiteRule; r != nil && (!r.From.IsZero() || !r.Until.IsZero()) {
			detail["window"] = map[string]any{"from": optTime(r.From), "until": optTime(r.Until)}
		}
	}
	s.audit(c, auth.AuditEvent{Kind: "setting_changed", Actor: c.user().ID, Tenant: t.ID, Detail: detail})
	out := struct {
		tenantSettingResp
		Warning *warningResp `json:"warning,omitempty"`
	}{tenantSettingResp: tenantSettingOf(after)}
	if res.UndatedReplaced {
		out.Warning = &warningResp{"undated_value_replaced", []string{key},
			"There is one manual rule for each setting. A dated rule replaced the undated one: when its window ends, the value is not the undated one but what is below it (billing, the plan, the site)."}
	}
	writeJSON(c.w, http.StatusOK, out)
}

// warningResp is a warning that comes with a change that went through.
type warningResp struct {
	Code    string   `json:"code"`
	Keys    []string `json:"keys"`
	Message string   `json:"message"`
}

// mySettings is GET /api/settings: what each setting that has a space or
// person layer comes to for the caller (and for a space, when one is given), and
// whether that layer may change it, so a client greys out "set by the
// administrators".
func (s *server) mySettings(c *call) {
	ctx := c.r.Context()
	if sp := c.r.URL.Query().Get("space"); sp != "" {
		h, err := s.spaces.Open(ctx, c.user(), sp)
		if err != nil {
			s.fail(c.w, err)
			return
		}
		h.Release()
	}
	eff, err := s.settings.For(ctx, c.user().TenantID)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	type layerView struct {
		CanChange bool `json:"canChange"`
		InEffect  bool `json:"inEffect"`
		// Source is the layer that keeps it from changing: a lock, or a value that
		// switches it off ("site" or "tenant"); absent when nothing does.
		Source settings.Layer `json:"source,omitempty"`
	}
	type item struct {
		Key       string     `json:"key"`
		Effective any        `json:"effective"`
		Space     *layerView `json:"space,omitempty"`
		Person    *layerView `json:"person,omitempty"`
		// Allowed and Locked are for storage.assets: what the person may ask for when they make a space,
		// and whether a lock leaves them no choice.
		Allowed []string `json:"allowed,omitempty"`
		Locked  *bool    `json:"locked,omitempty"`
	}
	out := []item{}
	for _, d := range s.settings.Defs() {
		if d.Key == settings.StorageAssets {
			r, err := s.assetsRule(ctx, c.user().TenantID)
			if err != nil {
				s.fail(c.w, err)
				return
			}
			locked := r.LockedBy != ""
			out = append(out, item{Key: d.Key, Effective: r.Default, Allowed: r.Allowed, Locked: &locked})
			continue
		}
		if d.Key == settings.OfflineCopies {
			// Not the person's to change: the client only reads whether it may keep copies.
			out = append(out, item{Key: d.Key, Effective: eff[d.Key].Value})
			continue
		}
		if !d.Has(settings.Space) && !d.Has(settings.Person) || strings.HasPrefix(d.Key, "quota.") {
			continue // the quotas' person layer is an administrator's override, not the person's own
		}
		e := eff[d.Key]
		it := item{Key: d.Key, Effective: e.Value}
		// A boolean is what the layers below may do: when it is off they cannot
		// use it, and what they saved stays without effect.
		on, _ := e.Value.(bool)
		lv := &layerView{CanChange: on && e.Below.Mode != settings.Lock, InEffect: on}
		switch {
		case e.LockedBy != "":
			lv.Source = e.LockedBy
		case !on:
			lv.Source = e.Source
		}
		if d.Has(settings.Space) {
			it.Space = lv
		}
		if d.Has(settings.Person) {
			it.Person = lv
		}
		out = append(out, it)
	}
	writeJSON(c.w, http.StatusOK, struct {
		Settings []item `json:"settings"`
	}{out})
}

var _ = store.DefaultTenantID
