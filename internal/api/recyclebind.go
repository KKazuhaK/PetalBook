package api

import (
	"context"

	"github.com/KKazuhaK/PetalBook/internal/settings"
	"github.com/KKazuhaK/PetalBook/internal/spaces"
)

// bindRecycle gives the manager the settings it reads when it purges and
// exports: recycle.days, exports.days and exports.maxBytes (site only).
func (s *server) bindRecycle() {
	if s.opt.Spaces == nil {
		return
	}
	siteInt := func(key string, def int64) func(context.Context) int64 {
		return func(ctx context.Context) int64 {
			e, err := s.settings.Get(ctx, "", key)
			if err != nil {
				return def
			}
			if n, ok := e.Value.(int64); ok && n > 0 {
				return n
			}
			return def
		}
	}
	days := siteInt(settings.RecycleDays, spaces.DefaultRecycleDays)
	s.opt.Spaces.SetRecycleDays(func(ctx context.Context) int { return int(days(ctx)) })
	expDays, expBytes := siteInt(settings.ExportsDays, spaces.DefaultExportDays), siteInt(settings.ExportsMaxBytes, spaces.DefaultExportBytes)
	s.opt.Spaces.SetExportLimits(func(ctx context.Context) spaces.ExportLimits {
		return spaces.ExportLimits{Days: int(expDays(ctx)), Bytes: expBytes(ctx)}
	})
}
