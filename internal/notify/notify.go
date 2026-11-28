// Package notify writes what an event says twice, in one transaction: a row of
// the audit log (for the administrators) and a notification to each person it
// concerns (for them). An event is defined once, here, so the audit row and the
// notification cannot name it differently or be written without each other.
package notify

import (
	"context"
	"encoding/json"
	"time"

	"petalbook.app/server/internal/auditlog"
	"petalbook.app/server/internal/store"
)

// Event is one definition: the kind of the notification and of the audit row.
// They are the same word except where the log already used another.
type Event struct {
	Kind  string // the notification kind (store.NotifyKinds)
	Audit string // the audit kind
}

// The events that tell a person what someone else did.
var (
	SpaceTakenOver   = Event{store.NotifySpaceTakenOver, "space_taken_over"}
	SpaceTransferred = Event{store.NotifySpaceTransferred, "space_transferred"}
	AccountChanged   = Event{store.NotifyAccountChanged, "user_changed"}
	PasswordSet      = Event{store.NotifyPasswordSet, "password_set_by_admin"}
	EmailReleased    = Event{store.NotifyEmailReleased, "email_released"}
	ViewedAs         = Event{store.NotifyViewedAs, "view_as_started"}
	DomainLapsed     = Event{store.NotifyDomainLapsed, "domain_lapsed"}
)

// The events that are an audit row only (no one is told).
var (
	// DomainMissing: the daily recheck no longer finds a verified domain's record.
	DomainMissing = Event{"", "domain_missing"}
)

// Entry is what happened.
type Entry struct {
	// The audit row: who acted, whom it is about, from where, which tenant ("" as
	// for store.AuditRow) and the detail.
	Actor, User, Username, IP, Tenant string
	Detail                            map[string]any
	// The notification: each person told (none is allowed: then only the audit
	// row is written), what it is about, and the facts (marshalled as JSON).
	To      []string
	Ref     string
	Payload any
	Now     time.Time
}

// WithAudit writes the audit row of ev and a notification to each of e.To. It
// runs in one transaction: q's when it is one, else a new one. A person who is
// gone gets nothing (store.NotifyUser).
func WithAudit(ctx context.Context, q store.Q, ev Event, e Entry) error {
	detail := "{}"
	if len(e.Detail) > 0 {
		b, err := json.Marshal(e.Detail)
		if err != nil {
			return err
		}
		detail = string(b)
	}
	return store.Atomically(ctx, q, func(q store.Q) error {
		row := store.AuditRow{ID: store.NewAuditID(e.Now), At: e.Now.UTC(), Kind: ev.Audit, Actor: e.Actor, UserID: e.User, Username: e.Username, IP: e.IP, Detail: detail, TenantID: e.Tenant,
			Capacity: auditlog.CapacityOf(ctx)}
		if err := store.InsertAudit(ctx, q, []store.AuditRow{row}); err != nil {
			return err
		}
		for _, to := range e.To {
			if err := store.NotifyUser(ctx, q, to, ev.Kind, e.Ref, e.Payload, e.Now); err != nil {
				return err
			}
		}
		return nil
	})
}
