package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

// One query answers a cookie hash: the session, or why it ended, or neither.
func TestLookupSessionAnswersTheSessionOrWhyItEnded(t *testing.T) {
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
