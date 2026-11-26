package spaces

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"petalbook.app/server/internal/settings"
)

// StoragePatch is a change to the storage policy as the tests write it: the
// policy is settings now, and setStorage turns the change into one write for
// each setting that differs.
type StoragePatch = func(*StorageSettings) error

// setStorage applies patch to the policy in force by writing the settings it
// changes, as the settings API does, and returns the policy and what it asks of
// the spaces. A refusal of a setting is ErrInvalid, as it was.
func setStorage(m *Manager, ctx context.Context, patch StoragePatch) (StorageSettings, Affected, error) {
	cur := m.Storage()
	next := cur
	next.Hosts.Allowed = slices.Clone(cur.Hosts.Allowed)
	if err := patch(&next); err != nil {
		return StorageSettings{}, Affected{}, err
	}
	set := settings.Of(m.opt.Store)
	var errs []error
	write := func(key string, v any) {
		if _, err := set.SetSite(ctx, key, settings.Change{HasValue: true, Value: v}); err != nil {
			var se *settings.Error
			if errors.As(err, &se) && se.Code == settings.CodeInvalid {
				err = fmt.Errorf("%w: %s", ErrInvalid, se.Msg)
			}
			errs = append(errs, err)
		}
	}
	num := func(p *int64) any {
		if p == nil {
			return nil
		}
		return *p
	}
	if cur.Remotes.Scope != next.Remotes.Scope {
		write(settings.StorageRemotesScope, next.Remotes.Scope)
	}
	if deref(cur.Remotes.Require) != deref(next.Remotes.Require) {
		write(settings.StorageRemotesRequire, deref(next.Remotes.Require))
	}
	if cur.Remotes.GraceDays != next.Remotes.GraceDays {
		write(settings.StorageRemotesGraceDays, int64(next.Remotes.GraceDays))
	}
	if !slices.Equal(cur.Hosts.Allowed, next.Hosts.Allowed) {
		write(settings.StorageHosts, next.Hosts.Allowed)
	}
	if cur.LocalSpaces.MaxBytes != next.LocalSpaces.MaxBytes && (cur.LocalSpaces.MaxBytes == nil || next.LocalSpaces.MaxBytes == nil || *cur.LocalSpaces.MaxBytes != *next.LocalSpaces.MaxBytes) {
		write(settings.StorageLocalMaxBytes, num(next.LocalSpaces.MaxBytes))
	}
	c, n := cur.Cache, next.Cache
	if c.Partial != n.Partial {
		write(settings.CachePartial, n.Partial)
	}
	if (c.EvictAfterDays == nil) != (n.EvictAfterDays == nil) || c.EvictAfterDays != nil && *c.EvictAfterDays != *n.EvictAfterDays {
		var v any
		if n.EvictAfterDays != nil {
			v = int64(*n.EvictAfterDays)
		}
		write(settings.CacheEvictAfterDays, v)
	}
	if c.MinIdleHours != n.MinIdleHours {
		write(settings.CacheMinIdleHours, int64(n.MinIdleHours))
	}
	for _, f := range []struct {
		key  string
		a, b *int64
	}{{settings.CacheClonesBytes, c.ClonesBytes, n.ClonesBytes}, {settings.CacheMinFreeBytes, c.MinFreeBytes, n.MinFreeBytes}} {
		if (f.a == nil) != (f.b == nil) || f.a != nil && *f.a != *f.b {
			write(f.key, num(f.b))
		}
	}
	if c.AssetsBytes != n.AssetsBytes {
		write(settings.CacheAssetsBytes, n.AssetsBytes)
	}
	if len(errs) > 0 {
		return StorageSettings{}, Affected{}, errs[0]
	}
	aff, err := m.StoragePolicyChanged(ctx)
	return m.Storage(), aff, err
}
