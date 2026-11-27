package api

import (
	"context"
	"errors"
	"io"
	"net/http"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/connections"
	"petalbook.app/server/internal/github"
	"petalbook.app/server/internal/githubapp"
	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
)

// GitHub's webhook (docs/api.md, "The GitHub provider", "Webhook"): GitHub
// itself posts the App's events here, with no session. Nothing in a delivery is
// believed until its signature (HMAC-SHA256 with the App's webhook secret) is
// checked; the body is capped before it is read. The site's own
// features.githubConnect refuses new installations' events with the rest of
// GitHub connections (no tenant is known to ask for); an uninstall is always
// taken. A pending claim is completed only if its claimer may still claim.
var githubWebhookRoutes = []route{
	{"POST", github.WebhookPath, public.always(), true, false, false, (*server).githubWebhook},
}

func init() { routeTable = append(routeTable, githubWebhookRoutes...) }

func (s *server) githubWebhook(c *call) {
	svc, ok := s.githubService(c)
	if !ok {
		return
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
	ctx := connections.WithInstallHooks(c.r.Context(), connections.InstallHooks{
		NewInstalls: func(ctx context.Context) error {
			if s.single || s.settings == nil {
				return nil
			}
			on, err := s.featureOn(ctx, "", settings.FeatureGitHubConnect)
			if err != nil {
				return err
			}
			if !on {
				return featureOffError(settings.FeatureGitHubConnect, "")
			}
			return nil
		},
		Recheck: s.claimerMayStill,
	})
	res, err := svc.Webhook(ctx, c.r.Header.Get("X-GitHub-Event"), c.r.Header.Get(githubapp.SignatureHeader), body)
	if err != nil {
		s.refuse(c.w, err)
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

// claimerMayStill says whether the person who made a pending claim may still
// claim for its owner, now that GitHub's event completes it: their account is
// active and, for an organisation, they hold tenant.github.manage in that
// tenant. It reads the person from the cache sessions use.
func (s *server) claimerMayStill(ctx context.Context, o store.InstallOwner, userID string) error {
	refused := connections.Errorf(github.ProviderID, http.StatusForbidden, connections.CodeForbidden, "The person who started connecting it may no longer.")
	if s.auth == nil || !o.Valid() {
		return refused
	}
	u, access, err := s.auth.CachedAccess(ctx, userID)
	if err != nil {
		return refused
	}
	if u.Disabled {
		return refused
	}
	if o.UserID != "" {
		if o.UserID != userID {
			return refused
		}
		return nil
	}
	feats := &featureRead{s: s}
	if authz.Decide(ctx, access, authz.TenantGitHubManage, o.TenantID, feats.features()) != authz.Allowed || feats.err != nil {
		return refused
	}
	return nil
}
