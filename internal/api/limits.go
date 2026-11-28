package api

import (
	"context"
	"math"
	"net/http"
	"time"

	"petalbook.app/server/internal/ratelimit"
	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/usage"
)

// The per-minute limits of a tenant (rate.apiPerMinute, rate.mcpPerMinute) and
// the count of its requests (tenant_usage_daily). Everything here is in memory:
// the limit is read from the settings' cache (one map lookup), the allowance is
// one entry of a ratelimit.Window for the tenant, and the count is an atomic
// add that internal/usage writes in a batch every minute. A request costs the
// database nothing.
//
// The limits apply to requests made with an API token and through MCP, the
// calls a script or an assistant makes; the web app's own requests (a session
// cookie) are the person's own use and are neither limited nor counted here.

// maxRateTenants bounds the tenants a table holds; a tenant beyond it is
// counted in the table's overflow sketch (ratelimit), never let go unlimited.
const maxRateTenants = 20_000

// usageFlushEvery and usageRecountEvery are how often the daily usage rows are
// written and the totals set again.
var usageFlushEvery, usageRecountEvery = usage.DefaultFlushEvery, usage.DefaultRecountEvery

// tenantLimits holds the allowances and the meter.
type tenantLimits struct {
	meter    *usage.Meter
	api, mcp *ratelimit.Window
}

func newTenantLimits(now func() time.Time) *tenantLimits {
	return &tenantLimits{
		meter: usage.NewMeter(now),
		api:   ratelimit.NewWindow(maxRateTenants, time.Minute, ratelimit.EvictFree, now).OnFull(ratelimit.Overflow),
		mcp:   ratelimit.NewWindow(maxRateTenants, time.Minute, ratelimit.EvictFree, now).OnFull(ratelimit.Overflow),
	}
}

// startUsage runs the meter's flush and the daily recount until Close, which
// flushes a last time. Accounts mode only.
func (s *server) startUsage() {
	s.limits = newTenantLimits(s.now)
	s.auth.UseUsage(s.limits.meter)
	s.goBg(func(ctx context.Context) {
		s.limits.meter.Run(ctx, s.opt.Store, s.log, usageFlushEvery, usageRecountEvery)
	})
}

// FlushUsage writes the counted usage now (tests, and a stop that wants it
// written before it goes on).
func (h *Handler) FlushUsage(ctx context.Context) error {
	if h.s.limits == nil {
		return nil
	}
	return h.s.limits.meter.Flush(ctx, h.s.opt.Store)
}

// meterRequest counts a request made with a token (or through MCP when mcp is
// set) for the tenant and holds it to the tenant's limit. Over the limit it
// answers 429 rate_limited with Retry-After and reports false.
func (s *server) meterRequest(w http.ResponseWriter, tenant string, mcp bool) bool {
	l := s.limits
	if l == nil || tenant == "" {
		return true
	}
	key, win, count := settings.RateAPIPerMinute, l.api, usage.APIRequests
	if mcp {
		key, win, count = settings.RateMCPPerMinute, l.mcp, usage.MCPRequests
	}
	if limit, ok := s.settings.LimitOf(context.Background(), tenant, key); ok {
		if wait, ok, _ := win.Take(tenant, 1, int(min(limit, math.MaxInt32))); !ok {
			s.noteOver(tenant, key, limit)
			w.Header().Set("Retry-After", retrySeconds(wait))
			writeError(w, http.StatusTooManyRequests, "rate_limited", "This organisation has made too many requests this minute; wait and try again.")
			return false
		}
	}
	l.meter.Add(tenant, count, 1)
	return true
}

// noteOver writes quota_exceeded for a limit crossed, at most once a day for a
// tenant and key (docs/api.md, "Events").
func (s *server) noteOver(tenant, key string, limit int64) { s.auth.OverLimit(tenant, key, limit) }
