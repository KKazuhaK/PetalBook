package api_test

import (
	"context"
	"testing"
	"time"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/store"
)

func sessionStateOf(t *testing.T, a *acc, c *client) (*store.Session, string) {
	t.Helper()
	p, err := a.auth.Authenticate(context.Background(), c.session(t))
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.GetSessionState(context.Background(), a.st.Q(), p.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	return s, p.User.TenantID
}

// Signing in says which client it is and proves the person's own context.
func TestSignInFillsTheClientAndTheHomeProof(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		s, tenant := sessionStateOf(t, a, tang)
		pr, ok := s.Proofs[tenant]
		if s.Client != "web" || s.CurrentTenant != "" || len(s.Proofs) != 1 || !ok || pr.Method != "password" || pr.At == 0 || pr.Seen != pr.At {
			t.Fatalf("%+v", s)
		}
	})
}

// A change of proofs made through the service is not undone by the hourly
// last_used update, which writes nothing else.
func TestTheHourlyTouchWritesOnlyLastUsed(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{mod: func(c *auth.Config) { c.CacheTTL = 24 * time.Hour }}, func(t *testing.T, a *acc) {
		a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		ctx := context.Background()
		p, err := a.auth.Authenticate(ctx, tang.session(t))
		if err != nil {
			t.Fatal(err)
		}
		id := p.SessionID
		before, _ := store.GetSessionState(ctx, a.st.Q(), id)
		set := map[string]store.Proof{"T2": {Method: "sso", Provider: "corp", At: 5, Seen: 6}}
		if err := a.auth.UpdateSessionState(ctx, id, "T2", set); err != nil {
			t.Fatal(err)
		}
		for range 3 {
			a.clock.Advance(30 * time.Minute)
			if _, err := a.auth.Authenticate(ctx, tang.session(t)); err != nil {
				t.Fatal(err)
			}
		}
		s, _ := store.GetSessionState(ctx, a.st.Q(), id)
		if !s.LastUsed.After(before.LastUsed) {
			t.Fatal("last_used did not move")
		}
		if s.CurrentTenant != "T2" || len(s.Proofs) != 1 || s.Proofs["T2"] != set["T2"] {
			t.Fatalf("the touch undid the change: %+v", s)
		}
	})
}
