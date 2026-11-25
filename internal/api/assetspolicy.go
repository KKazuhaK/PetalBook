package api

import (
	"context"

	"github.com/KKazuhaK/PetalBook/internal/settings"
	"github.com/KKazuhaK/PetalBook/internal/spaces"
)

// bindAssetsPolicy tells the space manager where attachments go for a tenant:
// the layered settings' answer (settings.AssetsRule), one cached lookup.
//
// The two inputs the settings cannot know yet are fixed: no tenant is on a
// target of its own (3.6's storage.modes "tenant"), and no limits table turns
// sync.assetsRepository off. When those exist this is the one place that passes
// them, and the rule already honours both.
func (s *server) bindAssetsPolicy() {
	s.spaces.SetAssetsPolicy(func(ctx context.Context, tenantID string) (spaces.AssetsPolicy, error) {
		r, err := s.settings.AssetsRule(ctx, tenantID, settings.AssetsInput{})
		if err != nil {
			return spaces.AssetsPolicy{}, err
		}
		return spaces.AssetsPolicy{Default: r.Default, Allowed: r.Allowed, Locked: r.LockedBy != ""}, nil
	})
}
