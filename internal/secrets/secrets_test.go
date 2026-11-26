package secrets_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"petalbook.app/server/internal/secret"
	"petalbook.app/server/internal/secrets"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

var ctx = context.Background()

const ten = "T000000005"

func setup(t *testing.T, st *store.Store) *secrets.Service {
	t.Helper()
	now := time.Now()
	if err := st.CreateTenant(ctx, store.Tenant{ID: ten, Slug: "acme", Name: "Acme", Created: now, Updated: now}); err != nil {
		t.Fatal(err)
	}
	s := secrets.Of(st, secret.NewBoxWithKey([32]byte{7}))
	s.SetClock(func() time.Time { return time.UnixMilli(1_700_000_000_000) })
	return s
}

func set(t *testing.T, s *secrets.Service, st *store.Store, tenant, name, value string) error {
	t.Helper()
	return st.Tx(ctx, func(q store.Q) error { return s.Set(ctx, q, tenant, name, value, "U1") })
}

func TestASecretIsSealedAndOnlyTheCodeThatUsesItReadsIt(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		if err := set(t, s, st, "", secrets.MailPassword, "hunter2-hunter2"); err != nil {
			t.Fatal(err)
		}
		row, err := store.GetSecret(ctx, st.Q(), "", secrets.MailPassword)
		if err != nil || row.Sealed == "" || strings.Contains(row.Sealed, "hunter2") || row.UpdatedBy != "U1" {
			t.Fatalf("stored in the clear or lost: %+v %v", row, err)
		}
		if v, ok, err := s.Open(ctx, st.Q(), "", secrets.MailPassword); err != nil || !ok || v != "hunter2-hunter2" {
			t.Fatalf("%q %v %v", v, ok, err)
		}
		// The listing is what the API tells: set or not, and when. It has no value to give.
		l, err := s.List(ctx, st.Q(), "")
		if err != nil {
			t.Fatal(err)
		}
		var found bool
		for _, m := range l {
			if m.Name == secrets.MailPassword {
				found = m.Set && !m.UpdatedAt.IsZero()
			} else if m.Set {
				t.Errorf("%s is set", m.Name)
			}
		}
		if !found {
			t.Fatalf("%+v", l)
		}
		// Replaced, then cleared.
		if err := set(t, s, st, "", secrets.MailPassword, "second-one"); err != nil {
			t.Fatal(err)
		}
		if v, _, _ := s.Open(ctx, st.Q(), "", secrets.MailPassword); v != "second-one" {
			t.Fatal(v)
		}
		if err := st.Tx(ctx, func(q store.Q) error { return s.Clear(ctx, q, "", secrets.MailPassword) }); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := s.Open(ctx, st.Q(), "", secrets.MailPassword); err != nil || ok {
			t.Fatalf("cleared but %v %v", ok, err)
		}
	})
}

// A value sealed for one scope and name opens for no other, so a row copied
// from a tenant to the site, or the other way, is no secret of its own.
func TestASecretIsBoundToItsScope(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		if err := set(t, s, st, ten, secrets.MailPassword, "tenant-only"); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := s.Open(ctx, st.Q(), "", secrets.MailPassword); ok {
			t.Fatal("the site has the tenant's secret")
		}
		row, _ := store.GetSecret(ctx, st.Q(), ten, secrets.MailPassword)
		row.TenantID = ""
		if err := store.PutSecret(ctx, st.Q(), row); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := s.Open(ctx, st.Q(), "", secrets.MailPassword); err == nil || ok {
			t.Fatalf("a row copied to the site opened: %v %v", ok, err)
		}
	})
}

func TestWhoMayHoldWhichSecret(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		s := setup(t, st)
		for _, c := range []struct{ tenant, name string }{
			{ten, secrets.GitHubAppKey},  // the site's alone
			{ten, secrets.CaptchaSecret}, // the site's alone for now
			{"", "nothing.at.all"},       // not a secret
			{ten, "also.not.a.secret"},   // not a secret
			{ten, ""},                    // no name
		} {
			err := set(t, s, st, c.tenant, c.name, "x")
			var se *secrets.Error
			if !errors.As(err, &se) || se.Code != "not_found" {
				t.Errorf("%q in %q: %v", c.name, c.tenant, err)
			}
		}
		if err := set(t, s, st, "", secrets.MailPassword, "   "); err == nil {
			t.Error("an empty secret was stored")
		}
		if err := set(t, s, st, "", secrets.MailPassword, strings.Repeat("k", secrets.MaxBytes+1)); err == nil {
			t.Error("a secret over the limit was stored")
		}
		// A tenant lists only the names it may hold.
		l, err := s.List(ctx, st.Q(), ten)
		if err != nil || len(l) != 1 || l[0].Name != secrets.MailPassword || l[0].Set {
			t.Fatalf("%+v %v", l, err)
		}
	})
}
