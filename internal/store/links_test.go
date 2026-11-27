package store_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

func mkProvider(t *testing.T, st *store.Store, id, owner, issuer string) {
	t.Helper()
	now := time.Now()
	if err := store.InsertProvider(ctx, st.Q(), store.ProviderRow{ID: id, Owner: owner, Slug: id, Kind: "oidc", Name: id, Issuer: issuer, Config: "{}", Created: now, Updated: now}); err != nil {
		t.Fatal(err)
	}
}

// An account is found by the provider's own id and the subject. Two providers
// with the same issuer string (an organisation copied another's) and the same
// subject are two accounts, never one.
func TestTheSameIssuerAndSubjectAtTwoProvidersAreTwoLinkedAccounts(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "U1", "u1", "member")
		mkUser(t, st, "U2", "u2", "member")
		mkProvider(t, st, "site-sso", store.ProviderSite, "https://idp.example")
		mkProvider(t, st, "org-sso", store.DefaultTenantID, "https://idp.example")
		now := time.Now()
		if err := store.InsertLink(ctx, st.Q(), store.Link{ID: "L1", ProviderID: "site-sso", UserID: "U1", Subject: "same-sub", SignIn: true, Created: now}); err != nil {
			t.Fatal(err)
		}
		if err := store.InsertLink(ctx, st.Q(), store.Link{ID: "L2", ProviderID: "org-sso", UserID: "U2", Subject: "same-sub", SignIn: true, Created: now}); err != nil {
			t.Fatal(err)
		}
		for provider, want := range map[string]string{"site-sso": "U1", "org-sso": "U2"} {
			l, err := store.FindLink(ctx, st.Q(), provider, "same-sub")
			if err != nil || l.UserID != want {
				t.Fatalf("%s: %+v %v, want %s", provider, l, err, want)
			}
		}
		// Neither is found through a provider that has no such account, whatever its issuer.
		mkProvider(t, st, "third", store.DefaultTenantID, "https://idp.example")
		if _, err := store.FindLink(ctx, st.Q(), "third", "same-sub"); !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
	})
}

// One link per provider per person, one person per (provider, subject): the
// unique indexes decide, also between concurrent sign-ins.
func TestALinkIsUniquePerProviderAndPersonAndPerSubject(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "U1", "u1", "member")
		mkUser(t, st, "U2", "u2", "member")
		mkProvider(t, st, "p", store.ProviderSite, "")
		now := time.Now()
		if err := store.InsertLink(ctx, st.Q(), store.Link{ID: "L1", ProviderID: "p", UserID: "U1", Subject: "s1", SignIn: true, Created: now}); err != nil {
			t.Fatal(err)
		}
		if err := store.InsertLink(ctx, st.Q(), store.Link{ID: "L2", ProviderID: "p", UserID: "U1", Subject: "s2", SignIn: true, Created: now}); !errors.Is(err, store.ErrLinkProviderTaken) {
			t.Fatalf("a second account of the same person at the provider: %v", err)
		}
		if err := store.InsertLink(ctx, st.Q(), store.Link{ID: "L3", ProviderID: "p", UserID: "U2", Subject: "s1", SignIn: true, Created: now}); !errors.Is(err, store.ErrLinkSubjectTaken) {
			t.Fatalf("the same account for another person: %v", err)
		}
		// Unlink, then link another: allowed.
		if _, err := store.DeleteLink(ctx, st.Q(), "L1", "U2", "any", time.Now()); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("another person's link: %v", err)
		}
		if _, err := store.DeleteLink(ctx, st.Q(), "L1", "U1", "any", time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := store.InsertLink(ctx, st.Q(), store.Link{ID: "L2", ProviderID: "p", UserID: "U1", Subject: "s2", SignIn: true, Created: now}); err != nil {
			t.Fatal(err)
		}
		// Deleting the provider takes the links with it.
		if err := store.DeleteProvider(ctx, st.Q(), "p"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.FindLink(ctx, st.Q(), "p", "s2"); !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
	})
}

func TestConcurrentLinksOfOnePersonOrOneSubjectHaveOneWinner(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		const n = 8
		for i := range n {
			mkUser(t, st, fmt.Sprintf("U%d", i), fmt.Sprintf("u%d", i), "member")
		}
		mkProvider(t, st, "p", store.ProviderSite, "")
		now := time.Now()
		race := func(link func(i int) store.Link) (wins int, errs []error) {
			var mu sync.Mutex
			var wg sync.WaitGroup
			for i := range n {
				wg.Add(1)
				go func() {
					defer wg.Done()
					err := store.InsertLink(ctx, st.Q(), link(i))
					mu.Lock()
					defer mu.Unlock()
					if err == nil {
						wins++
					} else {
						errs = append(errs, err)
					}
				}()
			}
			wg.Wait()
			return
		}
		// One person, eight different accounts at the provider.
		wins, errs := race(func(i int) store.Link {
			return store.Link{ID: fmt.Sprintf("A%d", i), ProviderID: "p", UserID: "U0", Subject: fmt.Sprintf("sub-%d", i), SignIn: true, Created: now}
		})
		if wins != 1 {
			t.Fatalf("%d links of one person were made: %v", wins, errs)
		}
		for _, err := range errs {
			if !errors.Is(err, store.ErrLinkProviderTaken) {
				t.Fatalf("%v", err)
			}
		}
		// Eight people, one account at the provider.
		wins, errs = race(func(i int) store.Link {
			return store.Link{ID: fmt.Sprintf("B%d", i), ProviderID: "p", UserID: fmt.Sprintf("U%d", i), Subject: "shared", SignIn: true, Created: now}
		})
		// U0 already has a link there, so it cannot win: at most one of the others does.
		if wins != 1 {
			t.Fatalf("%d people took one account: %v", wins, errs)
		}
		for _, err := range errs {
			if !errors.Is(err, store.ErrLinkSubjectTaken) && !errors.Is(err, store.ErrLinkProviderTaken) {
				t.Fatalf("%v", err)
			}
		}
	})
}

// Touching a link writes only when the address, its trust or the label changed,
// or the last use is old.
func TestTouchLinkWritesOnlyWhatChanged(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "U1", "u1", "member")
		mkProvider(t, st, "p", store.ProviderSite, "")
		now := time.Now().Truncate(time.Millisecond)
		if err := store.InsertLink(ctx, st.Q(), store.Link{ID: "L1", ProviderID: "p", UserID: "U1", Subject: "s", Email: "a@x.test", Label: "a", SignIn: true, Created: now, LastUsed: now}); err != nil {
			t.Fatal(err)
		}
		l, err := store.FindLink(ctx, st.Q(), "p", "s")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.TouchLink(ctx, st.Q(), l, "a@x.test", false, "a", now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if l2, _ := store.FindLink(ctx, st.Q(), "p", "s"); !l2.LastUsed.Equal(l.LastUsed) {
			t.Fatal("an unchanged link written within the day")
		}
		if err := store.TouchLink(ctx, st.Q(), l, "b@x.test", true, "b", now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		l2, _ := store.FindLink(ctx, st.Q(), "p", "s")
		if l2.Email != "b@x.test" || !l2.EmailTrusted || l2.Label != "b" || !l2.LastUsed.After(l.LastUsed) {
			t.Fatalf("%+v", l2)
		}
	})
}
