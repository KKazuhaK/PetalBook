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
	dropped atomic.Int64
	flushMu sync.Mutex
}

func newAuditLog() *auditLog { return &auditLog{ch: make(chan store.AuditRow, auditQueueSize)} }

// Audit queues an event. It never blocks: when the queue is full the event is
// dropped and counted, and the next batch carries a "dropped" row. An event of
// an optional category (auditlog) that is switched off for its tenant is not
// queued at all.
func (s *Service) Audit(ev AuditEvent) {
	k, known := auditlog.Lookup(ev.Kind)
	if !known {
		// Filed under admin (auditlog.CategoryOf); the source scan of
		// internal/auditlog keeps this from happening.
		s.log.Warn("an audit row of a kind that is not registered", "kind", ev.Kind)
	} else if !s.AuditCategoryOn(ev.Tenant, k.Category) {
		return
	}
	row, ok := s.audit.row(ev, s.now())
	if !ok {
		return
	}
	select {
	case s.audit.ch <- row:
	default:
		s.audit.dropped.Add(1)
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
		if n := a.dropped.Swap(0); n > 0 {
			if r, ok := a.row(AuditEvent{Kind: "dropped", Detail: map[string]any{"count": n}}, s.now()); ok {
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
// settings; the defaults while they cannot be read).
func (s *Service) AuditRetention(ctx context.Context) store.AuditRetention {
	p := store.AuditRetention{Days: auditlog.CoreFloorDays, Rows: 100_000, Mode: "or", OptionalDays: 30}
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
	return p
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
