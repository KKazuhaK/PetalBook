package store_test

import (
	"errors"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// The shapes 3.7 will use: the context a session is in and the proof each
// organisation holds, kept as they were written.
func TestSessionProofsAndContextRoundTrip(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "u1", "kazuha", "admin")
		now := time.UnixMilli(time.Now().UnixMilli()).UTC()
		home := store.Proof{Method: "password", At: 1000, Seen: 2000}
		if err := store.InsertSession(ctx, st.Q(), store.Session{ID: "s", UserID: "u1", Created: now, LastUsed: now, Client: "app",
			Proofs: map[string]store.Proof{"T1": home}}); err != nil {
			t.Fatal(err)
		}
		s, _, err := store.LookupSession(ctx, st.Q(), "s")
		if err != nil || s.Client != "app" || s.CurrentTenant != "" || len(s.Proofs) != 1 || s.Proofs["T1"] != home {
			t.Fatalf("%+v %v", s, err)
		}
		org := store.Proof{Method: "sso", Provider: "corp", At: 3000, Seen: 4000}
		s.Proofs["T2"] = org
		if err := store.UpdateSessionState(ctx, st.Q(), "s", "T2", s.Proofs); err != nil {
			t.Fatal(err)
		}
		got, err := store.GetSessionState(ctx, st.Q(), "s")
		if err != nil || got.CurrentTenant != "T2" || got.Client != "app" || len(got.Proofs) != 2 || got.Proofs["T2"] != org || got.Proofs["T1"] != home {
			t.Fatalf("%+v %v", got, err)
		}
		// A session made without them is in the home context, as the web, with no proof.
		if err := store.InsertSession(ctx, st.Q(), store.Session{ID: "bare", UserID: "u1", Created: now, LastUsed: now}); err != nil {
			t.Fatal(err)
		}
		if b, err := store.GetSessionState(ctx, st.Q(), "bare"); err != nil || b.Client != "web" || b.CurrentTenant != "" || len(b.Proofs) != 0 {
			t.Fatalf("%+v %v", b, err)
		}
		if err := store.UpdateSessionState(ctx, st.Q(), "nope", "", nil); !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
	})
}
