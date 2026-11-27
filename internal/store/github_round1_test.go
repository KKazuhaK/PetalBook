package store_test

import (
	"errors"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// An App on one GitHub host never takes over a sign-in row people linked on
// another: the links are keyed by GitHub's numeric ids, which an account of
// the App's host could share. Without links the row is taken over; on the
// same host it always is.
func TestAGitHubAppNeverTakesOverARowLinkedOnAnotherHost(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		now := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)
		mkUser(t, st, "U1", "u1", "member")
		ghe := store.ProviderRow{ID: store.ProviderGitHub, Owner: store.ProviderSite, Slug: "github", Kind: "github", Name: "GHE", Enabled: true,
			Config: `{"clientId":"own","baseUrl":"https://ghe.example.test"}`, SecretEnc: "sealed", Created: now, Updated: now}
		if err := store.InsertProvider(ctx, q, ghe); err != nil {
			t.Fatal(err)
		}
		if err := store.InsertLink(ctx, q, store.Link{ID: "L1", ProviderID: store.ProviderGitHub, UserID: "U1", Subject: "42", SignIn: true, Created: now}); err != nil {
			t.Fatal(err)
		}
		app := store.GitHubApp{App: `{"appId":"1"}`, ClientID: "Iv1.x", BaseURL: "https://github.com"}
		var he *store.GitHubHostError
		if err := store.PutGitHubApp(ctx, q, app, now); !errors.As(err, &he) || he.Links != 1 || he.Have != "https://ghe.example.test" {
			t.Fatalf("%v", err)
		}
		got, _ := store.GetProvider(ctx, q, store.ProviderGitHub)
		if got.Repos || got.SecretEnc != "sealed" || got.Config != ghe.Config {
			t.Fatalf("a refused takeover changed the row: %+v", got)
		}
		// The same host, written differently, is the same GitHub.
		if err := store.PutGitHubApp(ctx, q, store.GitHubApp{App: `{"appId":"1"}`, ClientID: "Iv1.x", BaseURL: "https://GHE.example.test"}, now); err != nil {
			t.Fatal(err)
		}
		// Without links, another host's App takes the row.
		if err := store.DeleteGitHubProvider(ctx, q); err != nil {
			t.Fatal(err)
		}
		if err := store.InsertProvider(ctx, q, ghe); err != nil {
			t.Fatal(err)
		}
		if err := store.PutGitHubApp(ctx, q, app, now); err != nil {
			t.Fatal(err)
		}
		if got, _ := store.GetProvider(ctx, q, store.ProviderGitHub); !got.Repos {
			t.Fatalf("%+v", got)
		}
	})
}

func TestSameGitHubHost(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"", "https://github.com", true},
		{"https://github.com/", "https://GitHub.com", true},
		{"https://ghe.test", "https://github.com", false},
		{"https://ghe.test:8443", "https://ghe.test", false},
		{"http://ghe.test", "https://ghe.test", false},
	} {
		if got := store.SameGitHubHost(c.a, c.b); got != c.want {
			t.Errorf("%q %q: %v", c.a, c.b, got)
		}
	}
}

// A provider is deleted only when no space syncs through it, counted in the
// deleting transaction: a remote put on it since an earlier count keeps it,
// whichever way it is deleted (as the sign-in provider or as GitHub).
func TestAProviderWithASpaceOnItIsNeverDeleted(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		now := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)
		mkGitHubRow(t, st, now)
		if err := store.InsertSpace(ctx, q, store.DefaultTenantID, "S1", "S1", "", "", now); err != nil {
			t.Fatal(err)
		}
		if err := store.PutRemote(ctx, q, store.Remote{SpaceID: "S1", URL: "https://github.com/o/s1.git", Branch: "main", State: "ok", Updated: now,
			CredType: "connection", Provider: store.ProviderGitHub, Via: "app", InstallationID: 1, RepoID: 1}); err != nil {
			t.Fatal(err)
		}
		var iu *store.ProviderInUseError
		if err := store.DeleteGitHubProvider(ctx, q); !errors.As(err, &iu) || iu.Spaces != 1 {
			t.Fatalf("%v", err)
		}
		if err := store.DeleteProvider(ctx, q, store.ProviderGitHub); !errors.As(err, &iu) || iu.Spaces != 1 {
			t.Fatalf("%v", err)
		}
		if _, err := store.GetProvider(ctx, q, store.ProviderGitHub); err != nil {
			t.Fatalf("the row went: %v", err)
		}
		if err := store.DeleteRemote(ctx, q, "S1", store.RoleContent); err != nil {
			t.Fatal(err)
		}
		if err := store.DeleteProvider(ctx, q, store.ProviderGitHub); err != nil {
			t.Fatal(err)
		}
	})
}
