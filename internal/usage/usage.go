// Package usage counts what a tenant uses each day (tenant_usage_daily).
//
// The counters (API and MCP requests, mail, outgoing calls) live in memory: an
// Add is a map read under a read lock and an atomic add, never a query. Flush
// writes them in one transaction (one AddUsageDaily per tenant and key), every
// minute and when the server stops, and a failed flush puts what it took back.
// A crash loses at most what was counted since the last flush (a minute by
// default); the totals (people, spaces, bytes, pinned bytes) are not counted but
// set by the recount from the usage rows and the spaces (Recount).
package usage

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"petalbook.app/server/internal/store"
)

// The counters, in the order of their slots.
const (
	APIRequests = iota
	MCPRequests
	Mail
	Outgoing
	nKeys
)

var keyNames = [nKeys]string{store.UsageKeyAPIRequests, store.UsageKeyMCPRequests, store.UsageKeyMail, store.UsageKeyOutgoing}

// DefaultFlushEvery is how often the counters are written; DefaultRecountEvery
// how often the totals are set again (the day's row keeps the last).
const (
	DefaultFlushEvery   = time.Minute
	DefaultRecountEvery = time.Hour
)

const day = 24 * time.Hour

type counts [nKeys]atomic.Int64

type row struct {
	day    int64
	tenant string
	key    int
	n      int64
}

// Meter holds the counters of every tenant.
type Meter struct {
	now func() time.Time

	flushMu sync.Mutex // one flush at a time

	mu       sync.RWMutex
	today    int64 // days since the epoch (UTC) of cur
	cur      map[string]*counts
	past     map[int64]map[string]*counts // counted on earlier days, not yet written
	inflight []row                        // being written: still part of Pending
}

// NewMeter makes a meter; now is the clock (nil for the real one).
func NewMeter(now func() time.Time) *Meter {
	if now == nil {
		now = time.Now
	}
	return &Meter{now: now, cur: map[string]*counts{}, past: map[int64]map[string]*counts{}}
}

func dayNumber(t time.Time) int64 { return t.UTC().Unix() / int64(day/time.Second) }

func dayText(n int64) string { return store.UsageDay(time.Unix(n*int64(day/time.Second), 0)) }

// Add counts n of a key for a tenant today. It never touches the database.
func (m *Meter) Add(tenant string, key int, n int64) {
	d := dayNumber(m.now())
	m.mu.RLock()
	if m.today == d {
		if c := m.cur[tenant]; c != nil {
			c[key].Add(n)
			m.mu.RUnlock()
			return
		}
	}
	m.mu.RUnlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.addLocked(d, tenant, key, n)
}

func (m *Meter) addLocked(d int64, tenant string, key int, n int64) {
	if d > m.today {
		if m.today != 0 && len(m.cur) > 0 {
			m.past[m.today] = m.cur
		}
		m.today, m.cur = d, map[string]*counts{}
	}
	set := m.cur
	if d < m.today {
		if set = m.past[d]; set == nil {
			set = map[string]*counts{}
			m.past[d] = set
		}
	}
	c := set[tenant]
	if c == nil {
		c = new(counts)
		set[tenant] = c
	}
	c[key].Add(n)
}

// Pending is what a tenant has counted today that the database does not have
// yet: the counter in memory and what a flush in progress is writing. Read it
// before the day's row, so that a flush ending between the two counts the
// difference twice rather than not at all.
func (m *Meter) Pending(tenant string, key int) int64 {
	d := dayNumber(m.now())
	m.mu.RLock()
	defer m.mu.RUnlock()
	var n int64
	if m.today == d {
		if c := m.cur[tenant]; c != nil {
			n = c[key].Load()
		}
	}
	for _, r := range m.inflight {
		if r.day == d && r.tenant == tenant && r.key == key {
			n += r.n
		}
	}
	return n
}

// take empties the counters into rows, which stay visible to Pending.
func (m *Meter) take() []row {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []row
	drain := func(d int64, set map[string]*counts) {
		for tenant, c := range set {
			for k := range c {
				if n := c[k].Swap(0); n != 0 {
					out = append(out, row{d, tenant, k, n})
				}
			}
		}
	}
	drain(m.today, m.cur)
	for d, set := range m.past {
		drain(d, set)
		delete(m.past, d)
	}
	m.inflight = out
	return out
}

func (m *Meter) putBack(rows []row) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inflight = nil
	for _, r := range rows {
		m.addLocked(r.day, r.tenant, r.key, r.n)
	}
}

func (m *Meter) done() {
	m.mu.Lock()
	m.inflight = nil
	m.mu.Unlock()
}

// Flush writes the counters with AddUsageDaily, in one transaction. On failure
// nothing is lost: what could not be written is counted again (a tenant that
// no longer exists is let go).
func (m *Meter) Flush(ctx context.Context, st *store.Store) error {
	m.flushMu.Lock()
	defer m.flushMu.Unlock()
	rows := m.take()
	if len(rows) == 0 {
		return nil
	}
	write := func(q store.Q, rs []row) error {
		for _, r := range rs {
			if err := store.AddUsageDaily(ctx, q, r.tenant, dayText(r.day), keyNames[r.key], r.n); err != nil {
				return err
			}
		}
		return nil
	}
	err := st.Tx(ctx, func(q store.Q) error { return write(q, rows) })
	if err == nil {
		m.done()
		return nil
	}
	// One tenant's failure (it was deleted) must not hold back the others.
	byTenant := map[string][]row{}
	for _, r := range rows {
		byTenant[r.tenant] = append(byTenant[r.tenant], r)
	}
	var failed []row
	last := err
	for tenant, rs := range byTenant {
		if e := st.Tx(ctx, func(q store.Q) error { return write(q, rs) }); e != nil {
			if _, gone := store.GetTenant(ctx, st.Q(), tenant); errors.Is(gone, store.ErrNotFound) {
				continue
			}
			failed, last = append(failed, rs...), e
		}
	}
	m.putBack(failed)
	if len(failed) == 0 {
		return nil
	}
	return last
}

// Recount sets today's totals of every tenant (people, spaces, bytes, pinned
// bytes), replacing the earlier values of the day. It reads two queries and
// writes in transactions of 200 tenants.
func Recount(ctx context.Context, st *store.Store, now time.Time) error {
	totals, err := store.ListTenantTotals(ctx, st.Q())
	if err != nil {
		return err
	}
	d := store.UsageDay(now)
	for len(totals) > 0 {
		n := min(len(totals), 200)
		chunk := totals[:n]
		totals = totals[n:]
		if err := st.Tx(ctx, func(q store.Q) error {
			for _, t := range chunk {
				if err := store.SetTenantTotals(ctx, q, d, t); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// Run flushes every flushEvery and recounts every recountEvery (and once at
// the start) until ctx ends, then flushes a last time. It is the whole of the
// daily job: the work is two small writes an hour, so it has a loop of its own
// rather than a row in the jobs table (which is for work people ask for).
func (m *Meter) Run(ctx context.Context, st *store.Store, log *slog.Logger, flushEvery, recountEvery time.Duration) {
	if flushEvery <= 0 {
		flushEvery = DefaultFlushEvery
	}
	if recountEvery <= 0 {
		recountEvery = DefaultRecountEvery
	}
	ft, rt := time.NewTicker(flushEvery), time.NewTicker(recountEvery)
	defer ft.Stop()
	defer rt.Stop()
	recount := func() {
		if err := Recount(ctx, st, m.now()); err != nil && ctx.Err() == nil {
			log.Warn("could not recount the tenants' daily usage", "error", err)
		}
	}
	recount()
	for {
		select {
		case <-ctx.Done():
			end, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			if err := m.Flush(end, st); err != nil {
				log.Warn("could not write the tenants' daily usage when stopping", "error", err)
			}
			cancel()
			return
		case <-ft.C:
			if err := m.Flush(ctx, st); err != nil && ctx.Err() == nil {
				log.Warn("could not write the tenants' daily usage", "error", err)
			}
		case <-rt.C:
			recount()
		}
	}
}
