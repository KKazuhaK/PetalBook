package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
)

// The audit log of docs/api.md: rows are queued in memory and written in
// batches in the background, so a sign-in never waits for them.
const (
	auditQueueSize = 10_000
	auditKeepRows  = 100_000
	auditKeepDays  = 90
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
}

type auditLog struct {
	ch      chan store.AuditRow
	dropped atomic.Int64
	seq     atomic.Uint64
	flushMu sync.Mutex
}

func newAuditLog() *auditLog { return &auditLog{ch: make(chan store.AuditRow, auditQueueSize)} }

// Audit queues an event. It never blocks: when the queue is full the event is
// dropped and counted, and the next batch carries a "dropped" row.
func (s *Service) Audit(ev AuditEvent) {
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
	id := fmt.Sprintf("%012x%06x", now.UnixMilli(), a.seq.Add(1)&0xffffff)
	return store.AuditRow{ID: id, At: now.UTC(), Kind: ev.Kind, Actor: ev.Actor, UserID: ev.User, Username: clip(ev.Username, 100), IP: ev.IP, Detail: detail}, true
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

func (s *Service) trimAudit(ctx context.Context) {
	if _, err := store.TrimAudit(ctx, s.st.Q(), s.now().Add(-auditKeepDays*24*time.Hour), auditKeepRows); err != nil {
		s.log.Warn("could not trim the audit log", "error", err)
	}
}

// AuditEntry is a row as the API shows it.
type AuditEntry struct {
	ID       string
	At       time.Time
	Kind     string
	Actor    string
	User     string
	Username string
	IP       string
	Detail   json.RawMessage
}

// ListAudit returns a page of the log, newest first, and the cursor of the
// next page ("" at the end).
func (s *Service) ListAudit(ctx context.Context, kind, user, cursor string, limit int) ([]AuditEntry, string, error) {
	s.FlushAudit(ctx)
	rows, err := store.ListAudit(ctx, s.st.Q(), kind, user, cursor, limit)
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
		out[i] = AuditEntry{ID: r.ID, At: r.At, Kind: r.Kind, Actor: r.Actor, User: r.UserID, Username: r.Username, IP: r.IP, Detail: json.RawMessage(r.Detail)}
	}
	return out, next, nil
}
