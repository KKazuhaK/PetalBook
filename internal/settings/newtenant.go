package settings

import (
	"context"
	"encoding/json"

	"petalbook.app/server/internal/auditlog"
	"petalbook.app/server/internal/store"
)

// StampNewTenant writes the rules a tenant starts with, in the transaction q
// that makes it: today the site's TenantsNewAuditDefault, as a locked manual
// rule for FeatureTenantAudit (the tenant has no layer for it, so it can never
// change it; a site administrator does, by hand). The default is read as it is
// now and never again, so changing it later touches no tenant that exists.
// The change is also a setting_changed row of the audit log, written in the same
// transaction (not queued: a tenant never exists without the row that says how
// its rule came to be); capacity is the one the creation was made in
// (auditlog.CapacityOf of the request, "" for a person making their own
// organisation). The caller calls the returned function after the commit.
func (s *Service) StampNewTenant(ctx context.Context, q store.Q, tenant, by, capacity string) (func(), error) {
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
	detail, _ := json.Marshal(map[string]any{"key": FeatureTenantAudit, "layer": "tenant", "source": "manual", "from": nil,
		"to": map[string]any{"value": on, "lower": Lower{Mode: Lock}, "inEffect": true, "siteLocked": true}, "newTenant": true})
	now := s.now()
	if err := store.InsertAudit(ctx, q, []store.AuditRow{{ID: store.NewAuditID(now), At: now.UTC(), Kind: "setting_changed", Actor: by,
		Detail: string(detail), TenantID: tenant, Category: string(auditlog.CategoryOf("setting_changed")), Capacity: capacity}}); err != nil {
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
