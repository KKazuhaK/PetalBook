package store_test

import (
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// Requests to join by domain (3.6 d5) and the join mode of a domain, as rows.

func joinRow(id, tenant, username string, created, expires time.Time) store.JoinRequest {
	return store.JoinRequest{ID: id, TenantID: tenant, Username: username, Name: username, Email: username + "@acme.example",
		EmailKey: username + "@acme.example", Domain: "acme.example", PasswordHash: "h", Lang: "en", TokenHash: "tok-" + id,
		Created: created, Expires: expires}
}

// Cancelling a domain's requests cancels exactly the ones it read and returns:
// one that has expired (not yet swept) is not cancelled behind the audit's back.
func TestCancellingADomainsRequestsTouchesOnlyTheOnesItReturns(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Now()
		q := st.Q()
		if err := store.InsertJoinRequest(ctx, q, joinRow("J1", store.DefaultTenantID, "old", now.Add(-31*24*time.Hour), now.Add(-time.Hour)), now.Add(-31*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := store.InsertJoinRequest(ctx, q, joinRow("J2", store.DefaultTenantID, "live", now, now.Add(time.Hour)), now); err != nil {
			t.Fatal(err)
		}
		got, err := store.CancelJoinRequests(ctx, q, store.DefaultTenantID, "acme.example", now)
		if err != nil || len(got) != 1 || got[0].ID != "J2" {
			t.Fatalf("%+v %v", got, err)
		}
		old, err := store.GetJoinRequest(ctx, q, store.DefaultTenantID, "J1")
		if err != nil || old.State == store.JoinCancelled {
			t.Fatalf("an expired request was cancelled without being returned: %+v %v", old, err)
		}
		live, _ := store.GetJoinRequest(ctx, q, store.DefaultTenantID, "J2")
		if live.State != store.JoinCancelled || live.PasswordHash != "" {
			t.Fatalf("%+v", live)
		}
	})
}

// A domain that lapses, or moves to another tenant, joins nobody until its
// (new) holder turns joining on again.
func TestALapsedOrTransferredDomainJoinsNobody(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Now().Truncate(time.Millisecond)
		q := st.Q()
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		must(st.CreateTenant(ctx, store.Tenant{ID: "T000000002", Slug: "beta", Name: "Beta", Created: now, Updated: now}))
		claim := func(tenant, domain string) {
			t.Helper()
			must(store.InsertDomain(ctx, q, store.DomainRow{TenantID: tenant, Domain: domain, CodeHash: "c", CodeExpires: now.Add(time.Hour), Created: now, Updated: now}))
		}
		verify := func(tenant, domain string) store.DomainRow {
			t.Helper()
			if ok, err := store.VerifyDomain(ctx, q, tenant, domain, "", now, now.Add(time.Hour)); err != nil || !ok {
				t.Fatalf("verify %s: %v %v", domain, ok, err)
			}
			_, err := store.SetDomainJoin(ctx, q, tenant, domain, store.JoinModeAuto, now)
			must(err)
			r, err := store.GetDomain(ctx, q, tenant, domain)
			must(err)
			return r
		}
		mode := func(tenant, domain string) string {
			t.Helper()
			r, err := store.GetDomain(ctx, q, tenant, domain)
			must(err)
			return r.JoinMode
		}
		// A lapse.
		claim(store.DefaultTenantID, "gone.example")
		r := verify(store.DefaultTenantID, "gone.example")
		if ok, err := store.LapseDomain(ctx, q, r, "", now); err != nil || !ok {
			t.Fatal(ok, err)
		}
		if m := mode(store.DefaultTenantID, "gone.example"); m != store.JoinModeOff {
			t.Fatalf("a lapsed domain keeps join %q", m)
		}
		// A transfer onto a claim the other tenant had made (and once had joining on).
		claim("T000000002", "moved.example")
		_, err := store.SetDomainJoin(ctx, q, "T000000002", "moved.example", store.JoinModeRequest, now)
		must(err)
		claim(store.DefaultTenantID, "moved.example")
		verify(store.DefaultTenantID, "moved.example")
		if _, err := store.AssignDomain(ctx, q, "moved.example", "T000000002", "U1", now); err != nil {
			t.Fatal(err)
		}
		if m := mode("T000000002", "moved.example"); m != store.JoinModeOff {
			t.Fatalf("a transferred domain keeps join %q", m)
		}
	})
}
