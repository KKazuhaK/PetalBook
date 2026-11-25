package api

import (
	"context"

	"github.com/KKazuhaK/PetalBook/internal/settings"
	"github.com/KKazuhaK/PetalBook/internal/spaces"
)

// assetsRule is where the attachments of a tenant's new space go: the layered
// settings' answer (settings.AssetsRule) with the tenant's inputs. Both the
// creation path and GET /api/settings ask here, so the dialog's box shows what
// the server will do.
//
// The two inputs the settings cannot know yet are fixed: no tenant is on a
// target of its own (3.6's storage.modes "tenant"), and no limits table turns
// sync.assetsRepository off. When those exist this is the one place that passes
// them, and the rule already honours both.
func (s *server) assetsRule(ctx context.Context, tenantID string) (settings.AssetsRule, error) {
	return s.settings.AssetsRule(ctx, tenantID, settings.AssetsInput{OwnStorage: false, RepositoryOff: false})
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
