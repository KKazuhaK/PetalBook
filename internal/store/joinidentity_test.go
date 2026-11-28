package store_test

import (
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// A request made with a social sign-in holds the identity until it is
// decided: deny, withdraw, cancel and the sweep of an expired one each blank
// provider_id, subject and identity_label.
func TestEveryEndOfARequestBlanksItsIdentity(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Now().Truncate(time.Millisecond)
		q := st.Q()
		mk := func(id, domain string, expires time.Time) {
			t.Helper()
			r := joinRow(id, store.DefaultTenantID, id, now.Add(-time.Hour), expires)
			r.Domain, r.PasswordHash = domain, ""
			r.ProviderID, r.Subject, r.IdentityLabel = "P1", "sub-"+id, "label-"+id
			if err := store.InsertJoinRequest(ctx, q, r, now.Add(-time.Hour)); err != nil {
				t.Fatal(err)
			}
			if got, err := store.GetJoinRequest(ctx, q, store.DefaultTenantID, id); err != nil || got.Subject != "sub-"+id || got.IdentityLabel != "label-"+id || got.ProviderID != "P1" {
				t.Fatalf("the identity is stored: %+v %v", got, err)
			}
		}
		live := now.Add(time.Hour)
		mk("deny", "d1.example", live)
		mk("withdraw", "d2.example", live)
		mk("cancel", "d3.example", live)
		mk("sweep", "d4.example", now.Add(-time.Minute))

		if ok, err := store.DecideJoinRequest(ctx, q, store.DefaultTenantID, "deny", store.JoinDenied, "admin", "", now); err != nil || !ok {
			t.Fatal(ok, err)
		}
		if ok, err := store.WithdrawJoinRequest(ctx, q, "tok-withdraw", now); err != nil || !ok {
			t.Fatal(ok, err)
		}
		if got, err := store.CancelJoinRequests(ctx, q, store.DefaultTenantID, "d3.example", now); err != nil || len(got) != 1 {
			t.Fatal(got, err)
		}
		if err := store.SweepJoinRequests(ctx, q, now); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"deny", "withdraw", "cancel", "sweep"} {
			got, err := store.GetJoinRequest(ctx, q, store.DefaultTenantID, id)
			if err != nil || got.State == store.JoinPending || got.ProviderID != "" || got.Subject != "" || got.IdentityLabel != "" {
				t.Errorf("%s keeps its identity: %+v %v", id, got, err)
			}
		}
	})
}
