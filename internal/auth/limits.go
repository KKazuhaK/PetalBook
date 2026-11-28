package auth

import (
	"time"

	"petalbook.app/server/internal/ratelimit"
)

// Every limiter here is a table of internal/ratelimit: in memory, in this one
// process, and bounded. A full table forgets only state that no longer holds
// anyone back, and turns a newcomer away rather than forget a lock or a cap.
const maxLimitKeys = 10_000

// initLimits makes the limiters of the service. The per-minute and failure
// limits read their numbers from the security settings at each call; the
// rest are fixed here. When a table is full of held keys (see
// internal/ratelimit): the per-minute allowance and the starts of single
// sign-on let a new address through (Open: a first request is never turned
// away); the failure tables let every attempt try and give new keys'
// failures the overflow bucket; the throttled audit rows use the overflow
// bucket; the mail caps fail closed (refused until a window ends, as before).
// The per-minute allowance and the failure tables still hold each new key to
// its own count and lock in their overflow sketch, so a full table is no way
// round the cap of one name or address.
func (s *Service) initLimits(now ratelimit.Clock) {
	s.perMin = ratelimit.NewWindow(maxLimitKeys, time.Minute, ratelimit.EvictFree, now).OnFull(ratelimit.Open)
	s.auditIP = ratelimit.NewWindow(maxLimitKeys, time.Minute, ratelimit.EvictFree, now)
	s.failIP = ratelimit.NewLockout(maxLimitKeys, now)
	s.failUser = ratelimit.NewLockout(maxLimitKeys, now)
	s.failPair = ratelimit.NewLockout(maxLimitKeys, now)
	s.failTyped = ratelimit.NewLockout(maxLimitKeys, now)
	s.starts = ratelimit.NewBucket(10_000, 30, 20, now).OnFull(ratelimit.Open) // starts of single sign-on: 30 a minute, a burst of 20
	s.mailCaps = newHourCaps(maxLimitKeys, now)
	s.testCaps = newHourCaps(4, now)
	s.regCaps = newHourCaps(2000, now)
	s.joinCaps = ratelimit.NewWindow(maxLimitKeys, 24*time.Hour, ratelimit.KeepLive, now)
}
