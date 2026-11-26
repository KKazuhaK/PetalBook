package store_test

import (
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// Organisations people create themselves: who made a tenant and how, and the
// pending registrations that hold an organisation's slug.

func TestATenantRemembersHowItCameToBeAndWho(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Now()
		if d, err := store.GetTenant(ctx, st.Q(), store.DefaultTenantID); err != nil || d.Origin != store.OriginDefault || d.CreatedBy != "" {
			t.Fatalf("%+v %v", d, err)
		}
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		must(st.CreateTenant(ctx, store.Tenant{ID: "T000000002", Slug: "made-by-admin", Name: "A", Created: now, Updated: now}))
		for i, slug := range []string{"mine-one", "mine-two"} {
			must(st.CreateTenant(ctx, store.Tenant{ID: "T00000001" + string(rune('0'+i)), Slug: slug, Name: slug, Origin: store.OriginSelf, CreatedBy: "U1", Created: now, Updated: now}))
		}
		must(st.CreateTenant(ctx, store.Tenant{ID: "T000000030", Slug: "theirs", Name: "theirs", Origin: store.OriginSelf, CreatedBy: "U2", Created: now, Updated: now}))
		if got, _ := store.GetTenantBySlug(ctx, st.Q(), "made-by-admin"); got.Origin != store.OriginAdmin {
			t.Fatalf("the origin defaults to admin: %q", got.Origin)
		}
		if n, err := store.CountTenantsCreatedBy(ctx, st.Q(), "U1"); err != nil || n != 2 {
			t.Fatalf("created by U1: %d %v", n, err)
		}
		if n, _ := store.CountTenantsCreatedBy(ctx, st.Q(), ""); n != 0 {
			t.Fatal("nobody made the default tenant")
		}
		// Deleting a tenant gives the person their place back.
		must(st.DeleteTenant(ctx, "T000000010"))
		if n, _ := store.CountTenantsCreatedBy(ctx, st.Q(), "U1"); n != 1 {
			t.Fatalf("after a delete: %d", n)
		}
		// The list filters by origin.
		l, _, err := store.ListTenants(ctx, st.Q(), st.Dialect(), "", store.OriginSelf, "", 10)
		if err != nil || len(l) != 2 {
			t.Fatalf("%v %v", l, err)
		}
		// Only the three origins exist.
		if _, err := st.Q().Exec(ctx, `UPDATE tenants SET origin = 'other' WHERE id = 'T000000002'`); err == nil {
			t.Fatal("an origin outside default, admin and self was stored")
		}
	})
}

func TestARegistrationHoldsItsOrganisationSlugUntilItEnds(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Now().UTC()
		reg := func(id, user, slug string, expires time.Time) store.Registration {
			return store.Registration{ID: id, TenantID: store.DefaultTenantID, Username: user, Name: user, Email: user + "@example.test", PasswordHash: "h", TokenHash: "t-" + id,
				Created: now, Expires: expires, OrgName: "Acme", OrgSlug: slug}
		}
		insert := func(r store.Registration) error {
			return st.Tx(ctx, func(q store.Q) error { return store.InsertRegistration(ctx, q, r, now) })
		}
		if err := insert(reg("R1", "one", "acme", now.Add(time.Hour))); err != nil {
			t.Fatal(err)
		}
		if err := insert(reg("R2", "two", "acme", now.Add(time.Hour))); !store.IsUnique(err) {
			t.Fatalf("a live registration holds its slug: %v", err)
		}
		if held, _ := store.PendingOrgSlug(ctx, st.Q(), "acme", now); !held {
			t.Fatal("the slug is held")
		}
		// Joining registrations hold no slug, and are not counted as organisations.
		if err := insert(store.Registration{ID: "R3", TenantID: store.DefaultTenantID, Username: "three", Name: "t", Email: "t@example.test", PasswordHash: "h", TokenHash: "t-3", Created: now, Expires: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		if n, _ := store.CountPendingOrgRegistrations(ctx, st.Q(), now); n != 1 {
			t.Fatalf("organisations pending: %d", n)
		}
		if n, _ := store.CountPendingRegistrations(ctx, st.Q(), store.DefaultTenantID, now); n != 1 {
			t.Fatalf("joins pending: %d", n)
		}
		// Cancelling a tenant's joining registrations leaves the organisation's.
		if _, err := store.DeleteRegistrationsOf(ctx, st.Q(), store.DefaultTenantID); err != nil {
			t.Fatal(err)
		}
		if n, _ := store.CountPendingOrgRegistrations(ctx, st.Q(), now); n != 1 {
			t.Fatal("an organisation's registration was cancelled with the tenant's")
		}
		// Once it has expired, the slug is free again.
		later := now.Add(2 * time.Hour)
		if err := st.Tx(ctx, func(q store.Q) error {
			return store.InsertRegistration(ctx, q, reg("R4", "four", "acme", later.Add(time.Hour)), later)
		}); err != nil {
			t.Fatalf("an expired registration held its slug: %v", err)
		}
	})
}
