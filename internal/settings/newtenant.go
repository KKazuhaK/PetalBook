package settings

import (
	"context"
	"encoding/json"

	"petalbook.app/server/internal/store"
)

// StampNewTenant writes the rules a tenant starts with, in the transaction q
// that makes it: today the site's TenantsNewAuditDefault, as a locked manual
// rule for FeatureTenantAudit (the tenant has no layer for it, so it can never
// change it; a site administrator does, by hand). The default is read as it is
// now and never again, so changing it later touches no tenant that exists.
// The caller calls the returned function after the commit.
func (s *Service) StampNewTenant(ctx context.Context, q store.Q, tenant, by string) (func(), error) {
	e, err := s.Get(ctx, "", TenantsNewAuditDefault)
	if err != nil {
		return nil, err
	}
	on, _ := e.Value.(bool)
	val, _ := json.Marshal(on)
	lock, _ := json.Marshal(Lower{Mode: Lock})
	if err := store.PutLayered(ctx, q, store.LayeredRow{Layer: store.LayeredSite, LayerID: tenant, Key: FeatureTenantAudit,
		Source: store.SourceManual, Value: string(val), Lower: string(lock), SetBy: by, Updated: s.now()}); err != nil {
		return nil, err
	}
	rev, err := store.BumpSettingsRev(ctx, q)
	if err != nil {
		return nil, err
	}
	return func() {
		// Our own count is not a change by another process.
		s.seenRev.CompareAndSwap(rev-1, rev)
		s.InvalidateTenant(tenant)
	}, nil
}
