package api

import (
	"context"

	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/spaces"
)

// bindRecycle gives the manager the settings it reads when it purges and
// exports: recycle.days, exports.days and exports.maxBytes. The
// export settings fall back to their defaults when they cannot be read.
func (s *server) bindRecycle() {
	if s.opt.Spaces == nil {
		return
	}
	tenantInt := func(key string, def int64) func(context.Context, string) int64 {
		return func(ctx context.Context, tenant string) int64 {
			e, err := s.settings.Get(ctx, tenant, key)
			if err != nil {
				return def
			}
			if n, ok := e.Value.(int64); ok && n > 0 {
				return n
			}
			return def
		}
	}
	// recycle.days is read strictly: when it cannot be read the purge job skips
	// its round instead of purging on the default.
	s.opt.Spaces.SetRecycleDays(func(ctx context.Context) (int, error) {
		e, err := s.settings.Get(ctx, "", settings.RecycleDays)
		if err != nil {
			return 0, err
		}
		if n, ok := e.Value.(int64); ok && n > 0 {
			return int(n), nil
		}
		return spaces.DefaultRecycleDays, nil
	})
	// exports.days as the export's tenant has it (the site may force it for one
	// tenant); exports.maxBytes is the server's.
	expDays, expBytes := tenantInt(settings.ExportsDays, spaces.DefaultExportDays), tenantInt(settings.ExportsMaxBytes, spaces.DefaultExportBytes)
	s.opt.Spaces.SetExportLimits(func(ctx context.Context, tenant string) spaces.ExportLimits {
		return spaces.ExportLimits{Days: int(expDays(ctx, tenant)), Bytes: expBytes(ctx, "")}
	})
}
