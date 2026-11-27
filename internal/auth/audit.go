package auth

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"petalbook.app/server/internal/auditlog"
	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
)

// The audit log of docs/api.md: rows are queued in memory and written in
// batches in the background, so a sign-in never waits for them.
const (
	auditQueueSize = 10_000
	auditBatch     = 1_000
	// Past this many queued rows only core rows are queued: an optional row
	// (reads, downloads, API calls, sign-ins) is dropped, so a flood of them
	// never crowds a security or admin row out of the queue.
	auditOptionalLimit = auditQueueSize * 8 / 10
)

// AuditEvent is one thing worth recording.
type AuditEvent struct {
	Kind     string
	Actor    string // the user who acted
	User     string // the account it is about
	Username string // as typed, for refused sign-ins
	IP       string
	Detail   map[string]any
	// Tenant is the tenant the event belongs to; "" takes it from the user the
	// event is about, else the actor, else it is the server's own.
	Tenant string
	// Capacity is the capacity the actor acted in (auditlog.Capacity*); "" for none.
	Capacity string
}

type auditLog struct {
	ch      chan store.AuditRow
	dropped drops
	flushMu sync.Mutex
}

// drops counts the rows dropped since the last "dropped" row, and how many of
// them were of an optional category, in one word: the high half is all of them,
// the low half the optional ones, so a reader never sees one without the other.
type drops struct{ n atomic.Uint64 }

func (d *drops) add(optional bool) {
	if optional {
		d.n.Add(1<<32 | 1)
		return
	}
	d.n.Add(1 << 32)
}

// load returns the counts: all dropped, and of those the optional ones.
func (d *drops) load() (total, optional uint64) {
	v := d.n.Load()
	return v >> 32, v & (1<<32 - 1)
}

// take returns the counts and resets them.
func (d *drops) take() (total, optional uint64) {
	v := d.n.Swap(0)
	return v >> 32, v & (1<<32 - 1)
}

func newAuditLog() *auditLog { return &auditLog{ch: make(chan store.AuditRow, auditQueueSize)} }

// Audit queues an event. It never blocks: when the queue is full the event is
// dropped and counted, and the next batch carries a "dropped" row. An event of
// an optional category (auditlog) that is switched off for its tenant is not
// queued at all.
func (s *Service) Audit(ev AuditEvent) {
	k, known := auditlog.Lookup(ev.Kind)
	optional := false
	if !known {
		// Filed under admin (auditlog.CategoryOf); the source scan of
		// internal/auditlog keeps this from happening.
		s.log.Warn("an audit row of a kind that is not registered", "kind", ev.Kind)
	} else if !s.AuditCategoryOn(ev.Tenant, k.Category) {
		return
	} else if info, ok := auditlog.CategoryInfo(k.Category); ok && !info.Core {
		optional = true
	}
	if optional && len(s.audit.ch) >= auditOptionalLimit {
		s.audit.dropped.add(true)
		return
	}
	row, ok := s.audit.row(ev, s.now())
	if !ok {
		return
	}
	select {
	case s.audit.ch <- row:
	default:
		s.audit.dropped.add(optional)
	}
}

// AuditIn is Audit for an event of a request: one that does not say its
// capacity takes the one ctx carries (auditlog.WithCapacity), so every row of
// an administrator's request records the capacity they acted in.
func (s *Service) AuditIn(ctx context.Context, ev AuditEvent) {
	if ev.Capacity == "" {
		ev.Capacity = auditlog.CapacityOf(ctx)
	}
	s.Audit(ev)
}

func (a *auditLog) row(ev AuditEvent, now time.Time) (store.AuditRow, bool) {
	detail := "{}"
	if len(ev.Detail) > 0 {
		b, err := json.Marshal(ev.Detail)
		if err != nil {
			return store.AuditRow{}, false
		}
		detail = string(b)
	}
	// Sorts by time, then by order of arrival.
	id := store.NewAuditID(now)
	return store.AuditRow{ID: id, At: now.UTC(), Kind: ev.Kind, Actor: ev.Actor, UserID: ev.User, Username: clip(ev.Username, 100), IP: ev.IP, Detail: detail, TenantID: ev.Tenant,
		Category: string(auditlog.CategoryOf(ev.Kind)), Capacity: ev.Capacity}, true
}

// AuditWanted says whether an event of the kind would be queued now for the
// tenant: false for an optional category that is switched off. A caller that
// has work to do before an event (a dedupe table, a response wrapper) asks first,
// so a category that is off costs it one cached settings read and nothing else.
func (s *Service) AuditWanted(tenant, kind string) bool {
	k, ok := auditlog.Lookup(kind)
	return !ok || s.AuditCategoryOn(tenant, k.Category)
}

// AuditCategoryOn says whether rows of the category are written for the tenant
// ("" and "-": the site's own value): a core category always is, an optional
// one when its setting is on. It is one read of the settings' cached snapshot;
// a failed read takes the category's default.
func (s *Service) AuditCategoryOn(tenant string, c auditlog.Category) bool {
	info, ok := auditlog.CategoryInfo(c)
	if !ok || info.Core {
		return true
	}
	st := s.settings.Load()
	if st == nil {
		return info.Default
	}
	if tenant == "-" {
		tenant = ""
	}
	e, err := st.Get(context.Background(), tenant, info.Setting)
	if err != nil {
		return info.Default
	}
	if on, ok := e.Value.(bool); ok {
		return on
	}
	return info.Default
}

// FlushAudit writes what is queued now. The background loop does it every
// second; reading the log does it first, so a page shows what just happened.
func (s *Service) FlushAudit(ctx context.Context) {
	a := s.audit
	a.flushMu.Lock()
	defer a.flushMu.Unlock()
	for {
		var rows []store.AuditRow
	drain:
		for len(rows) < auditBatch {
			select {
			case r := <-a.ch:
				rows = append(rows, r)
			default:
				break drain
			}
		}
		if n, o := a.dropped.take(); n > 0 {
			d := map[string]any{"count": n}
			if o > 0 {
				d["optional"] = o
			}
			if r, ok := a.row(AuditEvent{Kind: "dropped", Detail: d}, s.now()); ok {
				rows = append(rows, r)
			}
		}
		if len(rows) == 0 {
			return
		}
		if err := s.st.Tx(ctx, func(q store.Q) error { return store.InsertAudit(ctx, q, rows) }); err != nil {
			s.log.Warn("could not write the audit log", "error", err, "rows", len(rows))
			return
		}
		if len(rows) < auditBatch {
			return
		}
	}
}

// AuditRetention is what the site's settings say the log keeps (the audit.keep*
// settings, and each category's own days; the defaults while they cannot be read).
func (s *Service) AuditRetention(ctx context.Context) store.AuditRetention {
	p := store.AuditRetention{Days: settings.AuditDefaultKeepDays, Rows: 100_000, Mode: "or", OptionalDays: 30}
	st := s.settings.Load()
	if st == nil {
		return p
	}
	num := func(key string, into *int) {
		if e, err := st.Get(ctx, "", key); err == nil {
			if n, ok := e.Value.(int64); ok {
				*into = int(n)
			}
		}
	}
	num(settings.AuditKeepDays, &p.Days)
	num(settings.AuditKeepRows, &p.Rows)
	num(settings.AuditOptionalKeepDays, &p.OptionalDays)
	if e, err := st.Get(ctx, "", settings.AuditKeepMode); err == nil {
		if m, ok := e.Value.(string); ok {
			p.Mode = m
		}
	}
	for _, c := range auditlog.Categories() {
		if e, err := st.Get(ctx, "", settings.AuditCategoryKeepDays(c.ID)); err == nil {
			if n, ok := e.Value.(int64); ok {
				if p.CategoryDays == nil {
					p.CategoryDays = map[string]int{}
				}
				p.CategoryDays[string(c.ID)] = int(n)
			}
		}
	}
	return p
}

// CountAuditPurge is how many rows a purge by hand would delete (the dry run),
// at most store.AuditCountCap; more says the count stopped there. What is
// queued is written first.
func (s *Service) CountAuditPurge(ctx context.Context, p store.AuditPurge) (int64, bool, error) {
	s.FlushAudit(ctx)
	return store.CountAuditPurge(ctx, s.st.Q(), p)
}

// PurgeAudit deletes the rows of the log p covers (a purge by hand, see
// store.AuditPurge), after writing what is queued; step is told each batch's
// count. The purge writes no row itself: its job records it once, at its end.
func (s *Service) PurgeAudit(ctx context.Context, p store.AuditPurge, step func(int64)) (int64, error) {
	s.FlushAudit(ctx)
	return store.PurgeAudit(ctx, s.st.Q(), p, step)
}

func (s *Service) trimAudit(ctx context.Context) {
	if _, err := store.TrimAudit(ctx, s.st.Q(), s.now(), s.AuditRetention(ctx)); err != nil {
		s.log.Warn("could not trim the audit log", "error", err)
	}
}

// AuditEntry is a row as the API shows it.
type AuditEntry struct {
	ID       string
	At       time.Time
	Kind     string
	Category string
	Capacity string
	Tenant   string
	Actor    string
	User     string
	Username string
	IP       string
	Detail   json.RawMessage
}

func auditEntry(r store.AuditRow) AuditEntry {
	return AuditEntry{ID: r.ID, At: r.At, Kind: r.Kind, Category: r.Category, Capacity: r.Capacity, Tenant: r.TenantID,
		Actor: r.Actor, User: r.UserID, Username: r.Username, IP: r.IP, Detail: json.RawMessage(r.Detail)}
}

// ListAudit returns a page of the log, newest first, and the cursor of the
// next page ("" at the end).
func (s *Service) ListAudit(ctx context.Context, sc store.Scope, f store.AuditFilter, limit int) ([]AuditEntry, string, error) {
	s.FlushAudit(ctx)
	rows, err := store.ListAudit(ctx, s.st.Q(), sc, f, limit)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = rows[len(rows)-1].ID
	}
	out := make([]AuditEntry, len(rows))
	for i, r := range rows {
		out[i] = auditEntry(r)
	}
	return out, next, nil
}

// StreamAudit calls fn for each row of the filter, newest first, at most max of
// them, one page of store.AuditStreamPage at a time; more says rows were left.
func (s *Service) StreamAudit(ctx context.Context, sc store.Scope, f store.AuditFilter, max int, fn func(AuditEntry) error) (n int, more bool, err error) {
	s.FlushAudit(ctx)
	return store.StreamAudit(ctx, s.st.Q(), sc, f, max, func(r store.AuditRow) error { return fn(auditEntry(r)) })
}
