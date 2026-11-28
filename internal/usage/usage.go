// Package usage counts what a tenant uses each day (tenant_usage_daily).
//
// The counters (API and MCP requests, mail, outgoing calls) live in memory: an
// Add is a map read under a read lock and an atomic add, never a query. Flush
// writes them in one transaction (one AddUsageDaily per tenant and key), every
// minute and when the server stops, and a failed flush puts what it took back.
// A crash loses at most what was counted since the last flush (a minute by
// default); the totals (people, spaces, bytes, pinned bytes) are not counted but
// set by the recount from the usage rows and the spaces (Recounter).
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
	gen      uint64                       // counts the flushes that have ended (see Reserve)
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

// pendingLocked is Pending with the lock held.
func (m *Meter) pendingLocked(d int64, tenant string, key int) int64 {
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

// Pending is what a tenant has counted today that the database does not have
// yet: the counter in memory and what a flush in progress is writing.
func (m *Meter) Pending(tenant string, key int) int64 {
	d := dayNumber(m.now())
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.pendingLocked(d, tenant, key)
}

// Gen changes whenever a flush ends. Read it before the day's row is read, and
// hand it to Reserve with the row's value.
func (m *Meter) Gen() uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.gen
}

// Reserve takes n of a key for a tenant if the day's total stays within limit:
// written is the value the database has of today (read after Gen), and what the
// meter holds is added to it, in one step under the lock, so that concurrent
// requests cannot both take the last slot. stale is true when a flush ended
// since gen was read (written may then lack or repeat what it wrote): read the
// row again and retry. Undo a reservation with Add(…, -n).
func (m *Meter) Reserve(tenant string, key int, n, written, limit int64, gen uint64) (ok, stale bool) {
	d := dayNumber(m.now())
	m.mu.Lock()
	defer m.mu.Unlock()
	if gen != m.gen {
		return false, true
	}
	if written+m.pendingLocked(d, tenant, key)+n > limit {
		return false, false
	}
	m.addLocked(d, tenant, key, n)
	return true, false
}

// take empties the counters into rows, which stay visible to Pending; with a
// tenant, only that tenant's.
func (m *Meter) take(tenant string) []row {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []row
	drain := func(d int64, set map[string]*counts) {
		for t, c := range set {
			if tenant != "" && t != tenant {
				continue
			}
			for k := range c {
				if n := c[k].Swap(0); n != 0 {
					out = append(out, row{d, t, k, n})
				}
			}
		}
	}
	drain(m.today, m.cur)
	for d, set := range m.past {
		drain(d, set)
		if tenant == "" {
			delete(m.past, d)
		}
	}
	m.inflight = out
	return out
}

func (m *Meter) putBack(rows []row) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inflight = nil
	m.gen++
	for _, r := range rows {
		m.addLocked(r.day, r.tenant, r.key, r.n)
	}
}

func (m *Meter) done() {
	m.mu.Lock()
	m.inflight = nil
	m.gen++
	m.mu.Unlock()
}

// Flush writes the counters with AddUsageDaily, in one transaction. On failure
// nothing is lost: what could not be written is counted again (a tenant that
// no longer exists is let go).
func (m *Meter) Flush(ctx context.Context, st *store.Store) error { return m.flush(ctx, st, "") }

// FlushTenant writes the counters of one tenant: another tenant's rows can
// neither be waited for nor fail it.
func (m *Meter) FlushTenant(ctx context.Context, st *store.Store, tenant string) error {
	return m.flush(ctx, st, tenant)
}

func (m *Meter) flush(ctx context.Context, st *store.Store, tenant string) error {
	m.flushMu.Lock()
	defer m.flushMu.Unlock()
	rows := m.take(tenant)
	if len(rows) == 0 {
		m.done()
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
	for t, rs := range byTenant {
		if e := st.Tx(ctx, func(q store.Q) error { return write(q, rs) }); e != nil {
			if gone(ctx, st, t) {
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

// gone says whether the tenant has been deleted.
func gone(ctx context.Context, st *store.Store, tenant string) bool {
	_, err := store.GetTenant(ctx, st.Q(), tenant)
	return errors.Is(err, store.ErrNotFound)
}

// Recounter sets the totals of every tenant's day (people, spaces, bytes,
// pinned bytes) and writes only the ones that changed since it last wrote
// them: it remembers what it wrote, and reads today's rows once (when it first
// runs, and when the day changes) to know what is there.
type Recounter struct {
	day  string
	last map[string]map[string]int64 // tenant -> key -> the value the database has
}

const recountChunk = 200

// Run recounts once for the day of now and returns how many values it wrote.
// A tenant deleted meanwhile is left out; the others are written.
func (r *Recounter) Run(ctx context.Context, st *store.Store, now time.Time) (written int, err error) {
	totals, err := store.ListTenantTotals(ctx, st.Q())
	if err != nil {
		return 0, err
	}
	d := store.UsageDay(now)
	fresh := r.last == nil || r.day != d
	if fresh {
		r.day, r.last = d, map[string]map[string]int64{}
	}
	for start := 0; start < len(totals); start += recountChunk {
		chunk := totals[start:min(start+recountChunk, len(totals))]
		if fresh {
			ids := make([]string, len(chunk))
			for i, t := range chunk {
				ids[i] = t.TenantID
			}
			have, err := store.UsageDailyOfTenants(ctx, st.Q(), d, ids)
			if err != nil {
				return written, err
			}
			for id, kv := range have {
				r.last[id] = kv
			}
		}
		changes := map[string]map[string]int64{}
		for _, t := range chunk {
			for k, v := range t.Values() {
				if old, ok := r.last[t.TenantID][k]; !ok || old != v {
					if changes[t.TenantID] == nil {
						changes[t.TenantID] = map[string]int64{}
					}
					changes[t.TenantID][k] = v
				}
			}
		}
		n, err := r.write(ctx, st, d, changes)
		written += n
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

// write sets the changed values in one transaction; if it fails (a tenant was
// deleted), each tenant on its own, and one that is gone is skipped.
func (r *Recounter) write(ctx context.Context, st *store.Store, d string, changes map[string]map[string]int64) (int, error) {
	if len(changes) == 0 {
		return 0, nil
	}
	set := func(q store.Q, ids []string) error {
		for _, id := range ids {
			if err := store.SetTenantTotalKeys(ctx, q, id, d, changes[id]); err != nil {
				return err
			}
		}
		return nil
	}
	ids := make([]string, 0, len(changes))
	for id := range changes {
		ids = append(ids, id)
	}
	remember := func(id string) {
		if r.last[id] == nil {
			r.last[id] = map[string]int64{}
		}
		for k, v := range changes[id] {
			r.last[id][k] = v
		}
	}
	n := 0
	if err := st.Tx(ctx, func(q store.Q) error { return set(q, ids) }); err == nil {
		for _, id := range ids {
			remember(id)
			n += len(changes[id])
		}
		return n, nil
	} else if ctx.Err() != nil {
		return 0, err
	}
	var last error
	for _, id := range ids {
		if e := st.Tx(ctx, func(q store.Q) error { return set(q, []string{id}) }); e != nil {
			if !gone(ctx, st, id) {
				last = e
			}
			continue
		}
		remember(id)
		n += len(changes[id])
	}
	return n, last
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
	var rc Recounter
	recount := func() {
		if _, err := rc.Run(ctx, st, m.now()); err != nil && ctx.Err() == nil {
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
