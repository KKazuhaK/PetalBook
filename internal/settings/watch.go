package settings

import (
	"context"
	"time"

	"petalbook.app/server/internal/store"
)

// Boundary is a rule for a tenant whose window started or ended.
type Boundary struct {
	Tenant, Key string
	Source      Source
	// Started is true for the start of the window, false for its end.
	Started bool
	At      time.Time
}

// SetAfter sets how a timer is made (tests give a clock they move by hand).
func (s *Service) SetAfter(f func(d time.Duration, fn func()) interface{ Stop() bool }) {
	s.tmu.Lock()
	s.afterFunc = func(d time.Duration, fn func()) stopper { return f(d, fn) }
	s.tmu.Unlock()
}

// Watch starts the one timer of the windows: reads are correct without it (a
// snapshot is current only until the next start or end of a window), but the
// side effects of a boundary, its audit entry and the quota check, need it.
// onBoundary is told, outside any lock, of each start or end since the last
// time. Only the active server watches. With no window ahead there is no timer.
func (s *Service) Watch(ctx context.Context, onBoundary func(context.Context, []Boundary)) {
	s.tmu.Lock()
	s.watching, s.onBound, s.handled = true, onBoundary, s.now()
	s.tmu.Unlock()
	s.rearm(ctx)
}

// StopWatching stops the timer.
func (s *Service) StopWatching() {
	s.tmu.Lock()
	defer s.tmu.Unlock()
	s.watching = false
	if s.armed != nil {
		s.armed.Stop()
		s.armed = nil
	}
}

// CheckBoundaries takes in the starts and ends of windows up to now and arms
// the timer for the next one; the timer calls it, and so does every write of a
// rule, so that none is forgotten when the timer is stopped.
func (s *Service) CheckBoundaries(ctx context.Context) { s.rearm(ctx) }

func (s *Service) rearm(ctx context.Context) {
	ctx = context.WithoutCancel(ctx)
	s.tmu.Lock()
	if !s.watching {
		s.tmu.Unlock()
		return
	}
	if s.armed != nil {
		s.armed.Stop()
		s.armed = nil
	}
	now := s.now()
	var due []store.RuleBoundary
	if now.After(s.handled) {
		var err error
		if due, err = store.RuleBoundaries(ctx, s.st.Q(), s.handled, now); err != nil {
			due = nil
		} else {
			s.handled = now
		}
	}
	if next, ok, err := store.NextRuleBoundary(ctx, s.st.Q(), now); err == nil && ok {
		s.armed = s.afterFunc(max(next.Sub(now), time.Millisecond), func() { s.rearm(ctx) })
	}
	cb := s.onBound
	s.tmu.Unlock()
	if len(due) == 0 {
		return
	}
	out := make([]Boundary, len(due))
	for i, b := range due {
		s.InvalidateTenant(b.Tenant)
		out[i] = Boundary{Tenant: b.Tenant, Key: b.Key, Source: Source(b.Source), Started: b.Started, At: b.At}
	}
	if cb != nil {
		cb(ctx, out)
	}
}

// Close stops the timer.
func (s *Service) Close() { s.StopWatching() }
