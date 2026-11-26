package api

import (
	"errors"
	"net/http"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/secrets"
	"petalbook.app/server/internal/store"
)

// The secrets of the site and of tenants (internal/secrets). They are
// write-only: every answer says whether a secret is set and when it changed,
// and nothing here, the log and the audit trail included, ever holds a value.
// Site administrators manage the site's; a tenant's administrators their own
// tenant's, and only those the site allows the tenant (a secret that belongs
// to a feature is refused while the tenant does not have it, feature_off).

type secretsResp struct {
	Secrets []secrets.Meta `json:"secrets"`
}

// failSecret answers a refusal of the secrets.
func (s *server) failSecret(c *call, err error) {
	var se *secrets.Error
	if !errors.As(err, &se) {
		s.fail(c.w, err)
		return
	}
	status := http.StatusBadRequest
	if se.Code == "not_found" {
		status = http.StatusNotFound
	}
	writeError(c.w, status, se.Code, se.Msg)
}

func (s *server) listSecrets(c *call, tenant string) {
	l, err := s.vault.List(c.r.Context(), s.opt.Store.Q(), tenant)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, secretsResp{l})
}

// tenantSecretAllowed refuses a secret of a tenant that the tenant does not
// have the feature for; a site administrator is bound by it too, since there
// would be nothing to use the secret.
func (s *server) tenantSecretAllowed(c *call, tenant, name string) bool {
	sp, ok := secrets.SpecOf(name)
	if !ok || !sp.Tenant {
		writeError(c.w, http.StatusNotFound, "not_found", "No such secret.")
		return false
	}
	if sp.Feature == "" {
		return true
	}
	e, err := s.settings.Get(c.r.Context(), tenant, sp.Feature)
	if err != nil {
		s.fail(c.w, err)
		return false
	}
	if on, _ := e.Value.(bool); !on {
		s.refuse(c.w, featureOffError(sp.Feature, ""))
		return false
	}
	return true
}

// managed refuses a change of a secret that has an endpoint of its own, which
// checks it with the service it is for (the GitHub App's, by GitHub): it is
// listed here as set or not, and changed there.
func (s *server) managed(c *call, name string) bool {
	if sp, ok := secrets.SpecOf(name); ok && sp.ManagedBy != "" {
		writeError(c.w, http.StatusConflict, "conflict", "This secret is changed through "+sp.ManagedBy+", which checks it.")
		return true
	}
	return false
}

// putSecret stores the value of the body, replacing the secret. An empty value
// is refused (clearing is DELETE, which is held to what needs the secret); the
// GitHub App's secrets, whose absence would break the App, are managed
// elsewhere; the mail password may go, since a mail server may need none.
func (s *server) putSecret(c *call, tenant, name string) {
	if s.managed(c, name) {
		return
	}
	var req struct {
		Value *string `json:"value"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, secrets.MaxBytes+1024) {
		return
	}
	if req.Value == nil {
		writeError(c.w, http.StatusBadRequest, "invalid", "Give value, the secret. Clear it with DELETE.")
		return
	}
	ctx := c.r.Context()
	err := s.opt.Store.Tx(ctx, func(q store.Q) error {
		return s.vault.Set(ctx, q, tenant, name, *req.Value, c.user().ID)
	})
	if err != nil {
		s.failSecret(c, err)
		return
	}
	s.secretChanged(c, tenant, name, "set")
	s.listSecrets(c, tenant)
}

func (s *server) deleteSecret(c *call, tenant, name string) {
	if s.managed(c, name) {
		return
	}
	ctx := c.r.Context()
	var err error
	if tenant == "" && name == secrets.CaptchaSecret && s.auth != nil {
		// Not while a third-party captcha needs it: nobody could sign in.
		err = s.auth.ClearCaptchaSecret(ctx)
	} else {
		err = s.opt.Store.Tx(ctx, func(q store.Q) error { return s.vault.Clear(ctx, q, tenant, name) })
	}
	if err != nil {
		s.failSecret(c, err)
		return
	}
	s.secretChanged(c, tenant, name, "cleared")
	s.listSecrets(c, tenant)
}

// secretChanged tells what keeps something made from a secret that it changed,
// and records that it did: the name and what was done, never the value.
func (s *server) secretChanged(c *call, tenant, name, what string) {
	ctx := c.r.Context()
	if tenant == "" {
		switch name {
		case secrets.MailPassword:
			if s.auth != nil {
				if err := s.auth.ReloadMail(ctx); err != nil {
					s.log.Warn("could not apply the mail password", "error", err)
				}
			}
		case secrets.CaptchaSecret:
			s.settings.Invalidate() // the captcha check is built from it
		}
	}
	at := tenant
	if at == "" {
		at = "-"
	}
	s.audit(c, auth.AuditEvent{Kind: "secret_changed", Actor: c.user().ID, Tenant: at,
		Detail: map[string]any{"name": name, "scope": scopeName(tenant), "change": what}})
}

func scopeName(tenant string) string {
	if tenant == "" {
		return "site"
	}
	return "tenant"
}

func (s *server) adminListSecrets(c *call) { s.listSecrets(c, "") }

func (s *server) adminPutSecret(c *call) { s.putSecret(c, "", c.r.PathValue("name")) }

func (s *server) adminDeleteSecret(c *call) { s.deleteSecret(c, "", c.r.PathValue("name")) }

func (s *server) adminListTenantSecrets(c *call) {
	t, ok := s.visibleTenant(c)
	if !ok {
		return
	}
	s.listSecrets(c, t.ID)
}

func (s *server) adminPutTenantSecret(c *call) {
	t, ok := s.visibleTenant(c)
	if !ok || !s.tenantSecretAllowed(c, t.ID, c.r.PathValue("name")) {
		return
	}
	s.putSecret(c, t.ID, c.r.PathValue("name"))
}

func (s *server) adminDeleteTenantSecret(c *call) {
	t, ok := s.visibleTenant(c)
	if !ok {
		return
	}
	// Clearing is always allowed: a tenant that lost the feature may remove what it left.
	if _, known := secrets.SpecOf(c.r.PathValue("name")); !known {
		writeError(c.w, http.StatusNotFound, "not_found", "No such secret.")
		return
	}
	s.deleteSecret(c, t.ID, c.r.PathValue("name"))
}
