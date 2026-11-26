package api

import (
	"context"

	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/spaces"
)

// assetsRule is where the attachments of a tenant's new space go: the layered
// settings' answer (settings.AssetsRule) with the tenant's inputs. Both the
// creation path and GET /api/settings ask here, so the dialog's box shows what
// the server will do.
//
// The two inputs the settings cannot know: whether the tenant's spaces are on a
// storage target of its own, and whether its limits sheet allows an attachment
// repository (sync.assetsRepository, read from memory). OwnStorage stays false
// until 3.6's storage targets exist (storage.modes "tenant"); this is the one
// place that passes it, and the rule already honours it.
func (s *server) assetsRule(ctx context.Context, tenantID string) (settings.AssetsRule, error) {
	in := settings.AssetsInput{OwnStorage: false, RepositoryOff: s.limits != nil && !s.limits.Sheet(tenantID).Bool("sync.assetsRepository")}
	return s.settings.AssetsRule(ctx, tenantID, in)
}

// bindAssetsPolicy tells the space manager where attachments go for a tenant
// (assetsRule), one cached lookup.
func (s *server) bindAssetsPolicy() {
	s.spaces.SetAssetsPolicy(func(ctx context.Context, tenantID string) (spaces.AssetsPolicy, error) {
		r, err := s.assetsRule(ctx, tenantID)
		if err != nil {
			return spaces.AssetsPolicy{}, err
		}
		return spaces.AssetsPolicy{Default: r.Default, Allowed: r.Allowed, Locked: r.LockedBy != ""}, nil
	})
}
