package store_test

import (
	"errors"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// One query answers a cookie hash: the session, or why it ended, or neither.
func TestLookupSessionAnswersTheSessionOrWhyItEnded(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "u1", "kazuha", "admin")
		now := time.UnixMilli(time.Now().UnixMilli()).UTC()
		if err := store.InsertSession(ctx, st.Q(), store.Session{ID: "live", UserID: "u1", Created: now, LastUsed: now, Held: true}); err != nil {
			t.Fatal(err)
		}
		s, reason, err := store.LookupSession(ctx, st.Q(), "live")
		if err != nil || reason != "" || s.UserID != "u1" || !s.Held || !s.Created.Equal(now) || !s.LastUsed.Equal(now) {
			t.Fatalf("live: %+v %q %v", s, reason, err)
		}
		if err := store.InsertSession(ctx, st.Q(), store.Session{ID: "gone", UserID: "u1", Created: now, LastUsed: now}); err != nil {
			t.Fatal(err)
		}
		if err := store.EndSession(ctx, st.Q(), "gone", "u1", store.EndPassword, now); err != nil {
			t.Fatal(err)
		}
		if s, reason, err := store.LookupSession(ctx, st.Q(), "gone"); !errors.Is(err, store.ErrNotFound) || s != nil || reason != store.EndPassword {
			t.Fatalf("gone: %+v %q %v", s, reason, err)
		}
		if s, reason, err := store.LookupSession(ctx, st.Q(), "never"); !errors.Is(err, store.ErrNotFound) || s != nil || reason != "" {
			t.Fatalf("never: %+v %q %v", s, reason, err)
		}
	})
}

// The records of a disabled person are theirs: enabling them again forgets those, and only those.
func TestTheDisabledRecordsOfAPersonAreForgottenOnEnabling(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "u1", "kazuha", "admin")
		mkUser(t, st, "u2", "tang", "member")
		now := time.Now()
		for _, s := range []store.Session{{ID: "a", UserID: "u1"}, {ID: "b", UserID: "u1"}, {ID: "c", UserID: "u2"}} {
			s.Created, s.LastUsed = now, now
			if err := store.InsertSession(ctx, st.Q(), s); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.EndSession(ctx, st.Q(), "a", "u1", store.EndPassword, now); err != nil {
			t.Fatal(err)
		}
		for _, u := range []string{"u1", "u2"} {
			if err := store.DeleteUserSessions(ctx, st.Q(), u, "", store.EndDisabled, now); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.DeleteUserSessionEnds(ctx, st.Q(), "u1", store.EndDisabled); err != nil {
			t.Fatal(err)
		}
		for id, want := range map[string]string{"a": store.EndPassword, "b": "", "c": store.EndDisabled} {
			if _, got, _ := store.LookupSession(ctx, st.Q(), id); got != want {
				t.Fatalf("%s: %q, want %q", id, got, want)
			}
		}
	})
}

// The sweep of expired sessions keeps to each tenant's own cutoffs: the people
// of a tenant with lifetimes of its own go by theirs, everyone else by the site's.
func TestExpiredSessionsGoByTheirTenantsCutoffs(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.UnixMilli(time.Now().UnixMilli()).UTC()
		const other = "T000000009"
		if err := st.CreateTenant(ctx, store.Tenant{ID: other, Slug: "acme", Name: "Acme", Created: now, Updated: now}); err != nil {
			t.Fatal(err)
		}
		mkUser(t, st, "site", "site", "member")
		if err := store.InsertUser(ctx, st.Q(), store.User{ID: "own", Username: "own", Name: "own", Role: "member", TenantID: other, Created: now}); err != nil {
			t.Fatal(err)
		}
		old := now.Add(-48 * time.Hour)
		for _, id := range []string{"site", "own"} {
			if err := store.InsertSession(ctx, st.Q(), store.Session{ID: "s-" + id, UserID: id, Created: old, LastUsed: old}); err != nil {
				t.Fatal(err)
			}
		}
		// The site lets a session sit idle 30 days: nothing goes. The tenant's own cutoff is a day: its person's goes.
		own := []store.SessionCutoff{{Tenant: other, IdleBefore: now.Add(-24 * time.Hour), CreatedBefore: now.Add(-90 * 24 * time.Hour)}}
		n, err := store.DeleteExpiredSessions(ctx, st.Q(), now.Add(-30*24*time.Hour), now.Add(-90*24*time.Hour), now, own)
		if err != nil || n != 1 {
			t.Fatalf("%d %v", n, err)
		}
		if _, reason, err := store.LookupSession(ctx, st.Q(), "s-own"); !errors.Is(err, store.ErrNotFound) || reason != store.EndExpired {
			t.Fatalf("own: %q %v", reason, err)
		}
		if _, _, err := store.LookupSession(ctx, st.Q(), "s-site"); err != nil {
			t.Fatalf("site: %v", err)
		}
		// The other way: the tenant lets its people sit idle longer than the site does.
		if err := store.InsertSession(ctx, st.Q(), store.Session{ID: "s-own2", UserID: "own", Created: old, LastUsed: old}); err != nil {
			t.Fatal(err)
		}
		own = []store.SessionCutoff{{Tenant: other, IdleBefore: now.Add(-60 * 24 * time.Hour), CreatedBefore: now.Add(-90 * 24 * time.Hour)}}
		n, err = store.DeleteExpiredSessions(ctx, st.Q(), now.Add(-24*time.Hour), now.Add(-90*24*time.Hour), now, own)
		if err != nil || n != 1 {
			t.Fatalf("%d %v", n, err)
		}
		if _, _, err := store.LookupSession(ctx, st.Q(), "s-own2"); err != nil {
			t.Fatalf("a longer lifetime of the tenant's was not kept: %v", err)
		}
		if _, _, err := store.LookupSession(ctx, st.Q(), "s-site"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("site: %v", err)
		}
	})
}
