package api

import (
	"errors"
	"io"
	"net/http"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/github"
	"petalbook.app/server/internal/githubapp"
	"petalbook.app/server/internal/settings"
)

// GitHub's webhook (docs/api.md, "The GitHub provider", "Webhook"): GitHub
// itself posts the App's events here, with no session. Nothing in a delivery is
// believed until its signature (HMAC-SHA256 with the App's webhook secret) is
// checked; the body is capped before it is read. The site's own
// features.githubConnect switches receiving off with the rest of GitHub
// connections: no tenant is known to ask for.
var githubWebhookRoutes = []route{
	{"POST", github.WebhookPath, public.always(), true, false, false, (*server).githubWebhook},
}

func init() { routeTable = append(routeTable, githubWebhookRoutes...) }

func (s *server) githubWebhook(c *call) {
	svc, ok := s.githubService(c)
	if !ok {
		return
	}
	if !s.single && s.settings != nil {
		on, err := s.featureOn(c.r.Context(), "", settings.FeatureGitHubConnect)
		if err != nil {
			s.fail(c.w, err)
			return
		}
		if !on {
			s.refuse(c.w, featureOffError(settings.FeatureGitHubConnect, ""))
			return
		}
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.w, c.r.Body, github.MaxWebhookBody))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(c.w, http.StatusRequestEntityTooLarge, "too_large", "The delivery is larger than this server takes.")
			return
		}
		writeError(c.w, http.StatusBadRequest, "invalid", "The delivery could not be read.")
		return
	}
	res, err := svc.Webhook(c.r.Context(), c.r.Header.Get("X-GitHub-Event"), c.r.Header.Get(githubapp.SignatureHeader), body)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	for _, cl := range res.Claimed {
		s.audit(c, auth.AuditEvent{Kind: "github_installation_connected", Actor: cl.ClaimedBy, User: cl.Owner.UserID, Tenant: cl.Owner.TenantID,
			Detail: map[string]any{"installation": cl.ID, "account": cl.Account, "via": "webhook"}})
	}
	if u := res.Uninstalled; u != nil && u.Owner.Valid() {
		s.audit(c, auth.AuditEvent{Kind: "github_installation_disconnected", User: u.Owner.UserID, Tenant: u.Owner.TenantID,
			Detail: map[string]any{"installation": u.ID, "spaces": len(u.Spaces), "by": "github"}})
	}
	c.w.WriteHeader(http.StatusNoContent)
}
