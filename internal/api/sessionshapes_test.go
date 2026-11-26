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

// Requests add no query for the session and write nothing: the new fields are
// written only with the hourly last_used update.
func TestSessionShapesAddNoQueryPerRequestAndTravelWithLastUsed(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{mod: func(c *auth.Config) { c.CacheTTL = 24 * time.Hour }}, func(t *testing.T, a *acc) {
		a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		ctx := context.Background()
		p, err := a.auth.Authenticate(ctx, tang.session(t))
		if err != nil {
			t.Fatal(err)
		}
		id, tenant := p.SessionID, p.User.TenantID
		// Change the stored proofs behind the cache's back: a request within the
		// hour must neither read nor write them.
		if err := store.UpdateSessionState(ctx, a.st.Q(), id, "", map[string]store.Proof{}); err != nil {
			t.Fatal(err)
		}
		for range 5 {
			a.clock.Advance(time.Minute)
			if _, err := a.auth.Authenticate(ctx, tang.session(t)); err != nil {
				t.Fatal(err)
			}
		}
		if s, _ := store.GetSessionState(ctx, a.st.Q(), id); len(s.Proofs) != 0 {
			t.Fatalf("a request wrote the proofs: %+v", s.Proofs)
		}
		// The hourly update writes them with last_used.
		a.clock.Advance(61 * time.Minute)
		if _, err := a.auth.Authenticate(ctx, tang.session(t)); err != nil {
			t.Fatal(err)
		}
		s, _ := store.GetSessionState(ctx, a.st.Q(), id)
		if _, ok := s.Proofs[tenant]; !ok || s.Client != "web" {
			t.Fatalf("not written with last_used: %+v", s)
		}
	})
}
