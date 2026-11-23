package spaces

// Quotas of spaces (milestone 3.3, docs/api.md "Quotas"). The one quota that is
// refused is the number of spaces: a person's own and a tenant's. It is checked
// when a space is made against the usage table, which is kept by the
// statements that make, delete and hand over spaces (internal/store/usage.go),
// so the check is one primary-key lookup of the person's row (and one of the
// tenant's, when the tenant has a limit), however many spaces there are.
//
// Bytes and pages are measured in the background (sizes.go) and only warn:
// after each measurement, after a space is made, deleted or handed to another
// first owner (a transfer, a change of members), and after the daily recount of
// every row, a hook is told whom the numbers reached, and the code that owns the
// limits (the API) looks at them.

import (
	"context"
	"fmt"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
)

// DefaultRecountEvery is how often the usage table is recounted.
const DefaultRecountEvery = 24 * time.Hour

// SpaceLimits are how many spaces a person may own and a tenant may have; 0 is
// no limit.
type SpaceLimits struct {
	Person, Tenant int64
}

// UsageEvent says whose usage changed in the background: the people and tenants
// a measurement reached, or Everyone after a recount.
type UsageEvent struct {
	Touched  []store.Touched
	Everyone bool
}

type quotaHooks struct {
	limits func(ctx context.Context, u store.User) (SpaceLimits, error)
	usage  func(ctx context.Context, ev UsageEvent)
}

// SetQuota installs what the manager cannot know: the limits in force for a
// person (limits) and the code to tell when usage changed in the background
// (usage); either may be nil. Call it before serving.
func (m *Manager) SetQuota(limits func(ctx context.Context, u store.User) (SpaceLimits, error), usage func(ctx context.Context, ev UsageEvent)) {
	m.quota = quotaHooks{limits: limits, usage: usage}
}

// QuotaError is the refusal of a space over a limit. It is an ErrLimit.
type QuotaError struct {
	// Quota is what ran out ("spaces"), Limit what it is, Used what is used.
	Quota       string
	Limit, Used int64
	// Tenant is true when it is the tenant's total, not the person's.
	Tenant bool
}

func (e *QuotaError) Error() string {
	if e.Tenant {
		return fmt.Sprintf("%v: this tenant already has %d spaces, the most it may have", ErrLimit, e.Used)
	}
	return fmt.Sprintf("%v: you already own %d spaces, the most you may have", ErrLimit, e.Used)
}

// Is makes a QuotaError an ErrLimit.
func (e *QuotaError) Is(target error) bool { return target == ErrLimit }

// checkSpaceQuota refuses a new space over the person's limit or the tenant's.
// Two lookups of rows by key; createMu keeps two creations from both passing.
func (m *Manager) checkSpaceQuota(ctx context.Context, u store.User) error {
	lim := SpaceLimits{Person: int64(m.opt.MaxSpaces)}
	if m.quota.limits != nil {
		var err error
		if lim, err = m.quota.limits(ctx, u); err != nil {
			return err
		}
	}
	q := m.opt.Store.Q()
	if lim.Person > 0 {
		row, err := store.GetUsage(ctx, q, store.UsageUser, u.ID)
		if err != nil {
			return err
		}
		if row.Spaces >= lim.Person {
			return &QuotaError{Quota: "spaces", Limit: lim.Person, Used: row.Spaces}
		}
	}
	if lim.Tenant > 0 {
		row, err := store.GetUsage(ctx, q, store.UsageTenant, u.TenantID)
		if err != nil {
			return err
		}
		if row.Spaces >= lim.Tenant {
			return &QuotaError{Quota: "spaces", Limit: lim.Tenant, Used: row.Spaces, Tenant: true}
		}
	}
	return nil
}

// usageMoved tells the usage hook, after a change of who owns a space
// committed, whom it reached: the person the space was counted for before and
// the one after (nothing when they are the same), and its tenant.
func (m *Manager) usageMoved(ctx context.Context, before, after store.Touched) {
	if m.quota.usage == nil || before == after {
		return
	}
	m.quota.usage(context.WithoutCancel(ctx), UsageEvent{Touched: []store.Touched{before, after}})
}

// usageAdded tells the usage hook that a change added to (or took from) the
// rows of t: a new space, a deleted one.
func (m *Manager) usageAdded(ctx context.Context, t store.Touched) {
	if m.quota.usage != nil {
		m.quota.usage(context.WithoutCancel(ctx), UsageEvent{Touched: []store.Touched{t}})
	}
}

// RecountUsage puts the usage table right from the rows it summarises (the daily
// pass) and tells the usage hook. It returns how many rows it corrected.
func (m *Manager) RecountUsage(ctx context.Context) (int, error) {
	n, err := m.opt.Store.RecountUsage(ctx)
	if err != nil {
		return 0, err
	}
	if m.quota.usage != nil {
		m.quota.usage(ctx, UsageEvent{Everyone: true})
	}
	return n, nil
}

// runRecount recounts once when it starts (a crash may have left a difference,
// and it costs what a day's pass costs) and then every RecountEvery, until ctx
// ends.
func (m *Manager) runRecount(ctx context.Context) {
	every := m.opt.RecountEvery
	if every == 0 {
		every = DefaultRecountEvery
	}
	if every < 0 {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if n, err := m.RecountUsage(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			m.log.Warn("could not recount usage", "error", err)
		} else if n > 0 {
			m.log.Info("usage recounted", "corrected", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
