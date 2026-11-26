package spaces

import (
	"context"
	"errors"
	"slices"

	"petalbook.app/server/internal/connections"
	"petalbook.app/server/internal/settings"
)

// AssetsPolicy is where the attachments of a new space on a provider go, as the
// layered settings of the space's tenant leave it (settings.AssetsRule).
type AssetsPolicy struct {
	// Default is what a request that does not say gets: "space" (the space's own
	// repository) or "repository" (<name>-assets).
	Default string
	// Allowed is what the owner may ask for.
	Allowed []string
	// Locked is true when a lock, not a restriction, leaves the owner no choice.
	Locked bool
}

// defaultAssetsPolicy is what a manager with no settings (tests, the commands)
// applies: separate, and the owner may choose either.
var defaultAssetsPolicy = AssetsPolicy{Default: "repository", Allowed: []string{"space", "repository"}}

// SetAssetsPolicy installs how the settings of a tenant decide where
// attachments go. Call it before serving.
func (m *Manager) SetAssetsPolicy(f func(ctx context.Context, tenantID string) (AssetsPolicy, error)) {
	m.assetsPolicy = f
}

func (m *Manager) assetsPolicyOf(ctx context.Context, tenantID string) (AssetsPolicy, error) {
	if m.assetsPolicy == nil {
		return defaultAssetsPolicy, nil
	}
	return m.assetsPolicy(ctx, tenantID)
}

// check refuses an ask for attachments at where when the settings do not leave
// it to the owner: setting_locked under a lock, setting_restricted otherwise.
func (p AssetsPolicy) check(where string) error {
	if slices.Contains(p.Allowed, where) {
		return nil
	}
	if p.Locked {
		return &settings.Error{Code: settings.CodeLocked, Msg: "Where attachments go is set by the administrators (" + p.Default + ")."}
	}
	return &settings.Error{Code: settings.CodeRestricted, Msg: "The administrators do not allow attachments " + where + "."}
}

// withoutSecondRepo reports whether a failure to have an attachment repository
// is one the space can do without: the provider refused or cannot reach it. Not
// a wait or a failure of the provider itself, which are tried again later.
func withoutSecondRepo(err error) bool {
	var ce *connections.Error
	if !errors.As(err, &ce) {
		return false
	}
	switch ce.Code {
	case connections.CodeNameTaken, connections.CodeRefused, connections.CodeOwnerNotAllowed, connections.CodeMissingPermission,
		connections.CodeRepoNotInInstall, connections.CodeForbidden, connections.CodeNotFound:
		return true
	}
	return false
}
