package store_test

import (
	"errors"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// One table holds the secrets of the site and of every tenant: a name is unique
// in its scope, the site's has no tenant, and a tenant's go when it does.
func TestSecretsOfTheSiteAndOfTenants(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		now := time.UnixMilli(1_700_000_000_000)
		const ten = "T000000007"
		if err := st.CreateTenant(ctx, store.Tenant{ID: ten, Slug: "globex", Name: "Globex", Created: now, Updated: now}); err != nil {
			t.Fatal(err)
		}
		put := func(tenant, name, sealed string) {
			t.Helper()
			if err := store.PutSecret(ctx, q, store.SecretRow{TenantID: tenant, Name: name, Sealed: sealed, Updated: now, UpdatedBy: "U1"}); err != nil {
				t.Fatal(err)
			}
		}
		put("", "mail.password", "site-sealed")
		put(ten, "mail.password", "tenant-sealed") // the same name in another scope is another secret
		put(ten, "mail.password", "tenant-sealed-2")
		if r, err := store.GetSecret(ctx, q, "", "mail.password"); err != nil || r.Sealed != "site-sealed" || r.UpdatedBy != "U1" || !r.Updated.Equal(now) {
			t.Fatalf("%+v %v", r, err)
		}
		if r, err := store.GetSecret(ctx, q, ten, "mail.password"); err != nil || r.Sealed != "tenant-sealed-2" {
			t.Fatalf("replaced: %+v %v", r, err)
		}
		if _, err := store.GetSecret(ctx, q, ten, "captcha.secret"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%v", err)
		}
		// The list says what is set and when, and holds no value.
		l, err := store.ListSecrets(ctx, q, ten)
		if err != nil || len(l) != 1 || l[0].Name != "mail.password" || l[0].UpdatedBy != "U1" {
			t.Fatalf("%+v %v", l, err)
		}
		// A scope and a tenant that disagree are refused by the table itself.
		if _, err := q.Exec(ctx, `INSERT INTO secrets(scope, tenant_id, name, value, updated) VALUES ('site', 'T000000007', 'x', 'v', 0)`); err == nil {
			t.Fatal("a site secret with a tenant was accepted")
		}
		if _, err := q.Exec(ctx, `INSERT INTO secrets(scope, tenant_id, name, value, updated) VALUES ('tenant', '', 'x', 'v', 0)`); err == nil {
			t.Fatal("a tenant secret with no tenant was accepted")
		}
		// Removing one is quiet when there is none.
		if err := store.DeleteSecret(ctx, q, "", "mail.password"); err != nil {
			t.Fatal(err)
		}
		if err := store.DeleteSecret(ctx, q, "", "mail.password"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.GetSecret(ctx, q, "", "mail.password"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%v", err)
		}
		// The tenant's go with it.
		if err := st.DeleteTenant(ctx, ten); err != nil {
			t.Fatal(err)
		}
		if l, err := store.ListSecrets(ctx, q, ten); err != nil || len(l) != 0 {
			t.Fatalf("a deleted tenant's secrets remain: %+v %v", l, err)
		}
	})
}
