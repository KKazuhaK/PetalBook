package api

import (
	"net/http"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/authz"
)

// An organisation's own sender (3.6, docs/api.md "Mail: GET/PUT
// /api/admin/tenants/{tenant}/mail"): its administrators (tenant.mail.manage,
// with mail.ownSender on for the tenant) see, set and remove an SMTP server of
// its own and send a test message through it; a site administrator does the
// same for any tenant. The password is write-only.

var tenantMailRoutes = []route{
	{"GET", "/api/admin/tenants/{tenant}/mail", needs(authz.TenantMailManage), true, false, false, (*server).tenantGetMail},
	{"PUT", "/api/admin/tenants/{tenant}/mail", needs(authz.TenantMailManage), true, false, false, (*server).tenantPutMail},
	{"DELETE", "/api/admin/tenants/{tenant}/mail", needs(authz.TenantMailManage), true, false, false, (*server).tenantDeleteMail},
	{"POST", "/api/admin/tenants/{tenant}/mail/test", needs(authz.TenantMailManage), true, false, false, (*server).tenantTestMail},
}

func init() { routeTable = append(routeTable, tenantMailRoutes...) }

// mailTenant is the {tenant} of a mail route: one the caller may manage the
// mail of, else not found.
func (s *server) mailTenant(c *call) (string, bool) {
	id := c.r.PathValue("tenant")
	if !c.p.Access.Can(authz.TenantMailManage, id) || !s.knownTenant(id) {
		tenantNotFound(c)
		return "", false
	}
	return id, true
}

type tenantMailResp struct {
	Enabled     bool   `json:"enabled"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Security    string `json:"security"`
	Username    string `json:"username"`
	HasPassword bool   `json:"hasPassword"`
	From        string `json:"from"`
	FromName    string `json:"fromName"`
	ReplyTo     string `json:"replyTo"`
	Status      mailSt `json:"status"`
	// Sender is whose mailer the tenant's messages go by now: "tenant", "site",
	// or "" (they are not sent).
	Sender string `json:"sender"`
}

func toTenantMail(v *auth.TenantMailView) tenantMailResp {
	r := tenantMailResp{Enabled: v.Enabled, Host: v.Host, Port: v.Port, Security: v.Security, Username: v.Username, HasPassword: v.HasPassword,
		From: v.From, FromName: v.FromName, ReplyTo: v.ReplyTo, Status: mailSt{OK: v.Status.OK, Message: v.Status.Message}, Sender: v.Sender}
	if !v.Status.Checked.IsZero() {
		t := rfc3339(v.Status.Checked)
		r.Status.Checked = &t
	}
	return r
}

func (s *server) tenantGetMail(c *call) {
	t, ok := s.mailTenant(c)
	if !ok {
		return
	}
	v, err := s.auth.GetTenantMail(c.r.Context(), t)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.Header().Set("Cache-Control", "no-store")
	writeJSON(c.w, http.StatusOK, toTenantMail(v))
}

func (s *server) tenantPutMail(c *call) {
	t, ok := s.mailTenant(c)
	if !ok {
		return
	}
	var req struct {
		Enabled  *bool   `json:"enabled"`
		Host     *string `json:"host"`
		Port     *int    `json:"port"`
		Security *string `json:"security"`
		Username *string `json:"username"`
		Password *string `json:"password"`
		From     *string `json:"from"`
		FromName *string `json:"fromName"`
		ReplyTo  *string `json:"replyTo"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	v, err := s.auth.UpdateTenantMail(c.r.Context(), c.user().ID, s.auth.ClientAddr(c.r), t, auth.TenantMailPatch{Enabled: req.Enabled, Host: req.Host, Port: req.Port,
		Security: req.Security, Username: req.Username, Password: req.Password, From: req.From, FromName: req.FromName, ReplyTo: req.ReplyTo})
	if err != nil {
		s.fail(c.w, err)
		return
	}
	s.afterTenantMail(c)
	c.w.Header().Set("Cache-Control", "no-store")
	writeJSON(c.w, http.StatusOK, toTenantMail(v))
}

// afterTenantMail cancels the pending registrations that the change of a
// tenant's sender closed (registration needs a sender).
func (s *server) afterTenantMail(c *call) {
	if err := s.auth.CancelClosedRegistrations(c.r.Context()); err != nil {
		s.log.Warn("could not cancel pending registrations", "error", err)
	}
}

func (s *server) tenantDeleteMail(c *call) {
	t, ok := s.mailTenant(c)
	if !ok {
		return
	}
	if err := s.auth.DeleteTenantMail(c.r.Context(), c.user().ID, s.auth.ClientAddr(c.r), t); err != nil {
		s.fail(c.w, err)
		return
	}
	s.afterTenantMail(c)
	c.w.WriteHeader(http.StatusNoContent)
}

func (s *server) tenantTestMail(c *call) {
	t, ok := s.mailTenant(c)
	if !ok {
		return
	}
	var req struct {
		To   string `json:"to"`
		Lang string `json:"lang"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	res, err := s.auth.TestTenantMail(c.r.Context(), c.user().ID, s.auth.ClientAddr(c.r), t, req.To, langOf(c, req.Lang))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	out := map[string]any{"ok": res.OK}
	if !res.OK {
		out["message"] = res.Message
	}
	writeJSON(c.w, http.StatusOK, out)
}
