package notify_test

import (
	"context"
	"testing"
	"time"

	"petalbook.app/server/internal/notify"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// Each event writes exactly one audit row and one notification, and the
// notification kind is the event's own, so the two cannot drift apart.
func TestEachEventWritesOneAuditRowAndOneNotification(t *testing.T) {
	t.Parallel()
	events := []notify.Event{notify.SpaceTakenOver, notify.SpaceTransferred, notify.AccountChanged, notify.PasswordSet, notify.EmailReleased}
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		ctx := context.Background()
		for _, id := range []string{"A", "B"} {
			if err := store.InsertUser(ctx, st.Q(), store.User{ID: id, Username: id, Name: id, Role: "member", Created: time.Now()}); err != nil {
				t.Fatal(err)
			}
		}
		for _, ev := range events {
			err := notify.WithAudit(ctx, st.Q(), ev, notify.Entry{Actor: "B", User: "A", To: []string{"A"}, Payload: map[string]string{"by": "B"}, Now: time.Now()})
			if err != nil {
				t.Fatalf("%s: %v", ev.Kind, err)
			}
			rows, err := store.ListAudit(ctx, st.Q(), store.AllTenants(), false, ev.Audit, "A", "", 10)
			if err != nil || len(rows) != 1 {
				t.Fatalf("%s: audit rows %d %v", ev.Audit, len(rows), err)
			}
			list, err := store.ListNotifications(ctx, st.Q(), "A", store.DefaultTenantID, store.NotifyFilter{Kind: ev.Kind, Limit: 10})
			if err != nil || len(list) != 1 {
				t.Fatalf("%s: notifications %d %v", ev.Kind, len(list), err)
			}
			if !store.KnownNotifyKind(ev.Kind) {
				t.Errorf("%s is not in the registry", ev.Kind)
			}
		}
		// Nobody to tell: only the audit row. A person who is gone is not told.
		if err := notify.WithAudit(ctx, st.Q(), notify.AccountChanged, notify.Entry{Actor: "A", User: "A", Now: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if rows, _ := store.ListAudit(ctx, st.Q(), store.AllTenants(), false, notify.AccountChanged.Audit, "A", "", 10); len(rows) != 2 {
			t.Errorf("audit rows %d", len(rows))
		}
		if list, _ := store.ListNotifications(ctx, st.Q(), "A", store.DefaultTenantID, store.NotifyFilter{Kind: notify.AccountChanged.Kind, Limit: 10}); len(list) != 1 {
			t.Errorf("notifications %d", len(list))
		}
	})
}
