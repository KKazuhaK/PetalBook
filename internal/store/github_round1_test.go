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

// A GitHub sign-in row's base URL moves to another host only while nobody is
// linked to it, whoever writes it (here the sign-in side's UpdateProvider):
// the links are GitHub's numeric ids of the host they were made on.
func TestAGitHubRowNeverMovesHostWhileAccountsAreLinked(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		now := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)
		mkUser(t, st, "U1", "u1", "member")
		row := store.ProviderRow{ID: "ghe", Owner: store.ProviderSite, Slug: "ghe", Kind: "github", Name: "GHE", Enabled: true,
			Config: `{"clientId":"c","baseUrl":"https://ghe.example.test"}`, Created: now, Updated: now}
		if err := store.InsertProvider(ctx, q, row); err != nil {
			t.Fatal(err)
		}
		if err := store.InsertLink(ctx, q, store.Link{ID: "L1", ProviderID: "ghe", UserID: "U1", Subject: "42", SignIn: true, Created: now}); err != nil {
			t.Fatal(err)
		}
		moved := row
		moved.Config = `{"clientId":"c","baseUrl":"https://github.com"}`
		var he *store.GitHubHostError
		if err := store.UpdateProvider(ctx, q, moved); !errors.As(err, &he) || he.Links != 1 || he.Want != "https://github.com" {
			t.Fatalf("%v", err)
		}
		if got, _ := store.GetProvider(ctx, q, "ghe"); got.Config != row.Config {
			t.Fatalf("a refused move was written: %s", got.Config)
		}
		// Other changes, and the same host written otherwise, are written.
		same := row
		same.Name, same.Config = "GHE 2", `{"clientId":"c","baseUrl":"https://GHE.example.test/"}`
		if err := store.UpdateProvider(ctx, q, same); err != nil {
			t.Fatal(err)
		}
		// Without links it moves.
		if _, err := q.Exec(ctx, `DELETE FROM linked_accounts WHERE provider_id = 'ghe'`); err != nil {
			t.Fatal(err)
		}
		if err := store.UpdateProvider(ctx, q, moved); err != nil {
			t.Fatal(err)
		}
	})
}

// A remote names a provider whose row is there: one deleted meanwhile takes
// none (its deletion counted no space and left nothing to hold it).
func TestARemoteIsRefusedOnAProviderThatIsGone(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		now := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)
		if err := store.InsertSpace(ctx, q, store.DefaultTenantID, "S1", "S1", "", "", now); err != nil {
			t.Fatal(err)
		}
		r := store.Remote{SpaceID: "S1", URL: "https://gitlab.example.test/o/s1.git", Branch: "main", State: "ok", Updated: now,
			CredType: "connection", Provider: "gone", Via: "user", CredUserID: "U1"}
		if err := store.PutRemote(ctx, q, r); !errors.Is(err, store.ErrProviderGone) {
			t.Fatalf("%v", err)
		}
		if _, err := store.GetRemote(ctx, q, "S1", store.RoleContent); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("the remote was written: %v", err)
		}
		// A plain address names no provider row.
		r.Provider, r.CredType = store.ProviderGit, "token"
		if err := store.PutRemote(ctx, q, r); err != nil {
			t.Fatal(err)
		}
	})
}

// A host's provider is deleted only while nobody is connected and no space
// syncs through it, counted in the deleting transaction.
func TestAHostInUseIsNeverDeleted(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		now := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)
		if err := store.InsertConnProviderRow(ctx, q, store.ConnProviderRow{ID: "gl", Type: "gitlab", Host: "gitlab.example.test", Scheme: "https", Name: "GL", Owner: store.ConnProviderSite, Created: now, Updated: now}); err != nil {
			t.Fatal(err)
		}
		if err := store.InsertSpace(ctx, q, store.DefaultTenantID, "S1", "S1", "", "", now); err != nil {
			t.Fatal(err)
		}
		if err := store.PutRemote(ctx, q, store.Remote{SpaceID: "S1", URL: "https://gitlab.example.test/o/s1.git", Branch: "main", State: "ok", Updated: now,
			CredType: "connection", Provider: "gl", Via: "user", CredUserID: "U1"}); err != nil {
			t.Fatal(err)
		}
		var iu *store.ProviderInUseError
		if err := store.DeleteUnusedConnProviderRow(ctx, q, "gl"); !errors.As(err, &iu) || iu.Spaces != 1 {
			t.Fatalf("%v", err)
		}
		if err := store.DeleteRemote(ctx, q, "S1", store.RoleContent); err != nil {
			t.Fatal(err)
		}
		if err := store.DeleteUnusedConnProviderRow(ctx, q, "gl"); err != nil {
			t.Fatal(err)
		}
		if err := store.DeleteUnusedConnProviderRow(ctx, q, "gl"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%v", err)
		}
	})
}
