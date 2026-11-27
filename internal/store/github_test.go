package store_test

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

func TestGitHubInstallationsAndSpaces(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "A", "a", "member")
		now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
		q := st.Q()
		mkGitHubRow(t, st, now)
		// Spaces by installation and by user.
		for _, id := range []string{"S1", "S2", "S3"} {
			if err := store.InsertSpace(ctx, q, store.DefaultTenantID, id, id, "", "", now); err != nil {
				t.Fatal(err)
			}
		}
		put := func(id string, r store.Remote) {
			r.SpaceID, r.URL, r.Branch, r.State, r.Updated, r.CredType = id, "https://github.com/o/"+id+".git", "main", "ok", now, "connection"
			if err := store.PutRemote(ctx, q, r); err != nil {
				t.Fatal(err)
			}
		}
		put("S1", store.Remote{Via: "app", InstallationID: 10, RepoID: 1, CredUserID: "A"})
		put("S2", store.Remote{Via: "app", InstallationID: 10, RepoID: 2, CredUserID: "A"})
		put("S3", store.Remote{Via: "user", CredUserID: "A", RepoID: 3})
		counts, err := store.GitHubSpaceCounts(ctx, q, store.InstallOwner{UserID: "A"}, []int64{10, 20})
		if err != nil || counts[10] != 2 || counts[20] != 0 {
			t.Fatalf("%v %v", counts, err)
		}
		if n, _ := store.CountGitHubSpaces(ctx, q); n != 3 {
			t.Fatal(n)
		}
		if err := store.InsertSpace(ctx, q, store.DefaultTenantID, "S4", "S4", "", "", now); err != nil {
			t.Fatal(err)
		}
		if err := store.PutRemote(ctx, q, store.Remote{SpaceID: "S4", URL: "https://example.test/o/S4.git", Branch: "main", State: "ok", Updated: now, CredType: "token"}); err != nil {
			t.Fatal(err)
		}
		if n, err := store.CountGitSpaces(ctx, q); err != nil || n != 1 {
			t.Fatal(n, err)
		}
		if n, _ := store.CountUserGitHubSpaces(ctx, q, "A"); n != 1 {
			t.Fatal(n)
		}
		r, _ := store.GetRemote(ctx, q, "S3", store.RoleContent)
		if r.Via != "user" || r.CredUserID != "A" || r.RepoID != 3 {
			t.Fatalf("%+v", r)
		}
		// Standing in a space.
		if err := store.AddMember(ctx, q, "S3", "A", "owner", now); err != nil {
			t.Fatal(err)
		}
		if roles, disabled, err := store.MemberStanding(ctx, q, "S3", "A"); err != nil || len(roles) != 1 || roles[0] != "owner" || disabled {
			t.Fatalf("%q %v %v", roles, disabled, err)
		}
		store.SetDisabled(ctx, q, "A", true)
		if _, disabled, _ := store.MemberStanding(ctx, q, "S3", "A"); !disabled {
			t.Fatal("not disabled")
		}
		if _, _, err := store.MemberStanding(ctx, q, "S1", "A"); !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
		// Through a group, and both ways at once.
		if err := store.InsertGroup(ctx, q, store.Group{ID: "GMAINT0000", Name: "Maintainers", Source: store.GroupLocal, Created: now, Updated: now}); err != nil {
			t.Fatal(err)
		}
		if err := store.AddLocalMembers(ctx, q, "GMAINT0000", []string{"A"}, now); err != nil {
			t.Fatal(err)
		}
		for _, sp := range []string{"S1", "S3"} {
			if err := store.AddSpaceGroup(ctx, q, sp, "GMAINT0000", "editor", now); err != nil {
				t.Fatal(err)
			}
		}
		if roles, _, err := store.MemberStanding(ctx, q, "S1", "A"); err != nil || len(roles) != 1 || roles[0] != "editor" {
			t.Fatalf("%q %v", roles, err)
		}
		if roles, _, err := store.MemberStanding(ctx, q, "S3", "A"); err != nil || len(roles) != 2 || roles[0] != "owner" || roles[1] != "editor" {
			t.Fatalf("%q %v", roles, err)
		}
		if err := store.RemoveLocalMember(ctx, q, "GMAINT0000", "A"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.MemberStanding(ctx, q, "S1", "A"); !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
	})
}

// An attachment repository on GitHub depends on its installation or its user
// as the space's own repository does: the counts include it, once per space.
func TestGitHubSpaceCountsIncludeAttachmentRepositories(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
		q := st.Q()
		mkGitHubRow(t, st, now)
		for _, id := range []string{"S1", "S2", "S3"} {
			if err := store.InsertSpace(ctx, q, store.DefaultTenantID, id, id, "", "", now); err != nil {
				t.Fatal(err)
			}
		}
		rem := func(id string, r store.Remote) store.Remote {
			r.SpaceID, r.URL, r.Branch, r.State, r.Updated, r.CredType = id, "https://github.com/o/"+id+".git", "main", "ok", now, "connection"
			return r
		}
		// S1: the space on a token, its attachments through installation 10.
		if err := store.PutRemote(ctx, q, store.Remote{SpaceID: "S1", URL: "https://example.test/s1.git", Branch: "main", State: "ok", Updated: now, CredType: "token"}); err != nil {
			t.Fatal(err)
		}
		if err := store.PutRemote(ctx, q, rem("S1", store.Remote{Role: store.RoleAssets, Via: "app", InstallationID: 10, RepoID: 11, CredUserID: "A"})); err != nil {
			t.Fatal(err)
		}
		// S2: both through installation 10.
		if err := store.PutRemote(ctx, q, rem("S2", store.Remote{Via: "app", InstallationID: 10, RepoID: 2, CredUserID: "A"})); err != nil {
			t.Fatal(err)
		}
		if err := store.PutRemote(ctx, q, rem("S2", store.Remote{Role: store.RoleAssets, Via: "app", InstallationID: 10, RepoID: 21, CredUserID: "A"})); err != nil {
			t.Fatal(err)
		}
		// S3: the attachments only, through user A.
		if err := store.PutRemote(ctx, q, rem("S3", store.Remote{Role: store.RoleAssets, Via: "user", CredUserID: "A", RepoID: 31})); err != nil {
			t.Fatal(err)
		}
		if counts, err := store.GitHubSpaceCounts(ctx, q, store.InstallOwner{UserID: "A"}, []int64{10}); err != nil || counts[10] != 2 {
			t.Fatalf("%v %v", counts, err)
		}
		if n, err := store.CountGitHubSpaces(ctx, q); err != nil || n != 3 {
			t.Fatalf("%d %v", n, err)
		}
		if n, err := store.CountUserGitHubSpaces(ctx, q, "A"); err != nil || n != 1 {
			t.Fatalf("%d %v", n, err)
		}
	})
}

// An installation is connected to one owner on this server, a person or a
// tenant: a claim by another owner is refused (also when two race), the same
// owner's claim refreshes it, and it goes with its owner and with the provider.
func TestAGitHubInstallationHasOneOwner(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
		mkGitHubRow(t, st, now)
		mkUser(t, st, "A", "a", "member")
		mkUser(t, st, "B", "b", "member")
		for _, id := range []string{"T1", "T2"} {
			if err := st.CreateTenant(ctx, store.Tenant{ID: id, Slug: strings.ToLower(id), Name: id, Created: now}); err != nil {
				t.Fatal(err)
			}
		}
		claim := func(id int64, o store.InstallOwner, account string) error {
			return store.ClaimGitHubInstallation(ctx, q, store.GitHubInstallation{ID: id, Account: account, AccountID: id * 10, AccountType: "Organization",
				OwnerUser: o.UserID, OwnerTenant: o.TenantID, ClaimedBy: "A", Created: now, Updated: now})
		}
		t1, t2, a, b := store.InstallOwner{TenantID: "T1"}, store.InstallOwner{TenantID: "T2"}, store.InstallOwner{UserID: "A"}, store.InstallOwner{UserID: "B"}
		if err := claim(1, t1, "Org1"); err != nil {
			t.Fatal(err)
		}
		if err := claim(2, a, "a-gh"); err != nil {
			t.Fatal(err)
		}
		for _, o := range []store.InstallOwner{t2, a} {
			if err := claim(1, o, "Org1"); !errors.Is(err, store.ErrInstallationTaken) {
				t.Fatalf("another owner took installation 1: %v", err)
			}
		}
		for _, o := range []store.InstallOwner{b, t1} {
			if err := claim(2, o, "a-gh"); !errors.Is(err, store.ErrInstallationTaken) {
				t.Fatalf("another owner took installation 2: %v", err)
			}
		}
		if err := claim(1, store.InstallOwner{UserID: "A", TenantID: "T1"}, "x"); err == nil {
			t.Fatal("a claim with two owners was taken")
		}
		// The same owner again: refreshed, not refused.
		if err := claim(1, t1, "Org1-renamed"); err != nil {
			t.Fatal(err)
		}
		in, err := store.GetGitHubInstallation(ctx, q, 1)
		if err != nil || in.OwnerTenant != "T1" || in.OwnerUser != "" || in.Account != "Org1-renamed" || in.AccountID != 10 {
			t.Fatalf("%+v %v", in, err)
		}
		// Two owners racing for one installation: one owner gets it, the other is refused.
		var mu sync.Mutex
		var wg sync.WaitGroup
		got := map[string]int{}
		for i := 0; i < 8; i++ {
			o := t1
			if i%2 == 1 {
				o = t2
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				err := claim(7, o, "Race")
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					got[o.TenantID]++
				case errors.Is(err, store.ErrInstallationTaken):
				default:
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		in, _ = store.GetGitHubInstallation(ctx, q, 7)
		if in == nil || len(got) != 1 || got[in.OwnerTenant] != 4 {
			t.Fatalf("racing owners: %v, stored %+v", got, in)
		}
		// Each owner lists its own; by account, case ignored.
		if l, _ := store.ListGitHubInstallationsOwned(ctx, q, t1); len(l) == 0 || l[0].ID != 1 {
			t.Fatalf("%+v", l)
		}
		if l, _ := store.ListGitHubInstallationsOwned(ctx, q, b); len(l) != 0 {
			t.Fatalf("%+v", l)
		}
		if i, err := store.GitHubInstallationOwnedOn(ctx, q, a, "A-GH"); err != nil || i.ID != 2 {
			t.Fatal(i, err)
		}
		if _, err := store.GitHubInstallationOwnedOn(ctx, q, b, "a-gh"); !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
		// Only the owner disconnects it.
		if _, err := store.DisconnectGitHubInstallation(ctx, q, 2, b, "gone", "any", now); !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
		// It goes with its owner: a deleted tenant, a deleted person.
		if err := st.DeleteTenant(ctx, "T1"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.GetGitHubInstallation(ctx, q, 1); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a deleted tenant's installation stayed: %v", err)
		}
		if _, err := q.Exec(ctx, `DELETE FROM users WHERE id = 'A'`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.GetGitHubInstallation(ctx, q, 2); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a deleted person's installation stayed: %v", err)
		}
		if err := store.DeleteGitHubProvider(ctx, q); err != nil {
			t.Fatal(err)
		}
		if n := count(t, st, `SELECT COUNT(*) FROM provider_installations`); n != 0 {
			t.Fatalf("%d installations outlived their provider", n)
		}
	})
}

// Disconnecting an installation lists and marks the spaces on it as having
// lost access, keeps their rows, and the storage requirement counts them as
// lacking a remote until the same owner connects it again.
func TestDisconnectingAnInstallationLosesItsSpacesAccess(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
		mkGitHubRow(t, st, now)
		if err := st.CreateTenant(ctx, store.Tenant{ID: "T1", Slug: "t1", Name: "T1", Created: now}); err != nil {
			t.Fatal(err)
		}
		t1 := store.InstallOwner{TenantID: "T1"}
		claim := func(tenant string) {
			t.Helper()
			if err := store.ClaimGitHubInstallation(ctx, q, store.GitHubInstallation{ID: 5, Account: "Org", AccountType: "Organization", OwnerTenant: tenant, Created: now, Updated: now}); err != nil {
				t.Fatal(err)
			}
		}
		claim("T1")
		for _, id := range []string{"SB", "SA", "SC"} {
			if err := store.InsertSpace(ctx, q, "T1", id, "name "+id, "", "", now); err != nil {
				t.Fatal(err)
			}
		}
		put := func(id string, r store.Remote) {
			r.SpaceID, r.URL, r.Branch, r.State, r.Updated, r.CredType, r.Provider = id, "https://github.com/o/"+id+".git", "main", "ok", now, "connection", "github"
			if err := store.PutRemote(ctx, q, r); err != nil {
				t.Fatal(err)
			}
		}
		put("SA", store.Remote{Via: "app", InstallationID: 5, RepoID: 1})
		put("SB", store.Remote{Via: "app", InstallationID: 5, RepoID: 2})
		put("SB", store.Remote{Role: store.RoleAssets, Via: "app", InstallationID: 5, RepoID: 3})
		put("SC", store.Remote{Via: "app", InstallationID: 6, RepoID: 4})
		list, n, err := store.SpacesOnGitHubInstallation(ctx, q, t1, 5)
		if err != nil || n != 2 || len(list) != 2 || list[0].ID != "SA" || list[1].ID != "SB" || list[0].Name != "name SA" {
			t.Fatalf("%+v %d %v", list, n, err)
		}
		pol := store.RemotePolicy{Require: "github", All: true, Since: now.Add(-time.Hour), Grace: 24 * time.Hour, Now: now}
		g, o, _, err := store.PolicyCounts(ctx, q, pol)
		// SC syncs through an installation nobody connected: it has lost access already.
		if err != nil || g != 1 || o != 0 {
			t.Fatalf("grace %d overdue %d %v", g, o, err)
		}
		ids, err := store.DisconnectGitHubInstallation(ctx, q, 5, t1, "lost", "any", now)
		if err != nil || len(ids) != 2 {
			t.Fatalf("%v %v", ids, err)
		}
		for _, role := range []string{store.RoleContent, store.RoleAssets} {
			r, err := store.GetRemote(ctx, q, "SB", role)
			if err != nil || r.State != store.RemoteError || r.ErrorKind != store.AccessLost || r.InstallationID != 5 || r.URL == "" {
				t.Fatalf("%s: %+v %v", role, r, err)
			}
		}
		if g, _, _, _ = store.PolicyCounts(ctx, q, pol); g != 3 {
			t.Fatalf("%d spaces in their grace period, want 3", g)
		}
		f, err := store.GetPolicyFacts(ctx, q, "SA")
		if err != nil || f.CredType != "" {
			t.Fatalf("%+v %v", f, err)
		}
		if _, err := store.DisconnectGitHubInstallation(ctx, q, 5, t1, "lost", "any", now); !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
		// Connected again by its owner: the spaces have it again.
		claim("T1")
		if g, _, _, _ = store.PolicyCounts(ctx, q, pol); g != 1 {
			t.Fatalf("%d spaces in their grace period, want 1", g)
		}
		// Claimed by another tenant: not the space's owner's, still lost.
		if _, err := store.DisconnectGitHubInstallation(ctx, q, 5, t1, "lost", "any", now); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateTenant(ctx, store.Tenant{ID: "T2", Slug: "t2", Name: "T2", Created: now}); err != nil {
			t.Fatal(err)
		}
		claim("T2")
		if g, _, _, _ = store.PolicyCounts(ctx, q, pol); g != 3 {
			t.Fatalf("%d spaces in their grace period, want 3", g)
		}
	})
}

// mkGitHubRow sets an App up on the GitHub row, which makes the row.
func mkGitHubRow(t *testing.T, st *store.Store, now time.Time) {
	t.Helper()
	if err := store.PutGitHubApp(ctx, st.Q(), store.GitHubApp{App: `{"appId":"1","fingerprint":"f"}`, ClientID: "Iv1.x", BaseURL: "https://github.com"}, now); err != nil {
		t.Fatal(err)
	}
}

// The App is the config of the GitHub row: setting it up makes the row (a sign-in
// provider that is off, holding repositories), an administrator's changes of the
// row never touch it, and it is replaced only by whoever read what is there.
func TestTheGitHubAppIsTheConfigOfTheGitHubRow(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
		if _, ok, err := store.GetGitHubApp(ctx, q); err != nil || ok {
			t.Fatalf("an App from nothing: %v %v", ok, err)
		}
		mkGitHubRow(t, st, now)
		row, err := store.GetProvider(ctx, q, store.ProviderGitHub)
		if err != nil || row.Kind != "github" || row.Enabled || !row.Repos || row.Owner != store.ProviderSite {
			t.Fatalf("%+v %v", row, err)
		}
		app, ok, err := store.GetGitHubApp(ctx, q)
		if err != nil || !ok || app != `{"appId":"1","fingerprint":"f"}` {
			t.Fatalf("%q %v %v", app, ok, err)
		}
		// The sign-in side writes the whole config back, as it read it: the App's
		// keys stay as stored, whatever it wrote for them.
		row.Config = `{"organizations":["acme"],"clientId":"mine","baseUrl":"https://elsewhere.test","app":{"appId":"2"}}`
		row.Enabled, row.Updated = true, now.Add(time.Minute)
		if err := store.UpdateProvider(ctx, q, *row); err != nil {
			t.Fatal(err)
		}
		again, _ := store.GetProvider(ctx, q, store.ProviderGitHub)
		if !again.Enabled || !strings.Contains(again.Config, `"organizations":["acme"]`) || !strings.Contains(again.Config, `"clientId":"Iv1.x"`) ||
			!strings.Contains(again.Config, `"baseUrl":"https://github.com"`) || !strings.Contains(again.Config, `"appId":"1"`) || strings.Contains(again.Config, `"appId":"2"`) {
			t.Fatalf("%s", again.Config)
		}
		// A change made on what an older read saw is not written over a newer App.
		if ok, err := store.CompareAndSetGitHubApp(ctx, q, `{"appId":"0"}`, `{"appId":"9"}`, now); err != nil || ok {
			t.Fatalf("an old read wrote: %v %v", ok, err)
		}
		if ok, err := store.CompareAndSetGitHubApp(ctx, q, app, `{"appId":"1","slug":"s"}`, now); err != nil || !ok {
			t.Fatalf("%v %v", ok, err)
		}
		if got, _, _ := store.GetGitHubApp(ctx, q); got != `{"appId":"1","slug":"s"}` {
			t.Fatalf("%q", got)
		}
		if again, _ = store.GetProvider(ctx, q, store.ProviderGitHub); !strings.Contains(again.Config, `"organizations":["acme"]`) {
			t.Fatalf("the sign-in settings were lost: %s", again.Config)
		}
		// A row an administrator made for signing in takes the App's client.
		if err := store.DeleteGitHubProvider(ctx, q); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := store.GetGitHubApp(ctx, q); ok {
			t.Fatal("the App outlived its row")
		}
		own := store.ProviderRow{ID: store.ProviderGitHub, Owner: store.ProviderSite, Slug: "github", Kind: "github", Name: "GitHub", Enabled: true,
			Config: `{"clientId":"own","baseUrl":"https://github.com","organizations":["acme"]}`, SecretEnc: "sealed", Created: now, Updated: now}
		if err := store.InsertProvider(ctx, q, own); err != nil {
			t.Fatal(err)
		}
		if got, _ := store.GetProvider(ctx, q, store.ProviderGitHub); got.Repos {
			t.Fatal("a sign-in provider holds repositories")
		}
		mkGitHubRow(t, st, now.Add(time.Hour))
		got, _ := store.GetProvider(ctx, q, store.ProviderGitHub)
		if !got.Enabled || got.SecretEnc != "" || !got.Repos || !strings.Contains(got.Config, `"organizations":["acme"]`) || !strings.Contains(got.Config, `"clientId":"Iv1.x"`) {
			t.Fatalf("%+v", got)
		}
	})
}

// An installation another owner claims after the first let it go shows that
// owner only its own spaces: the first owner's, still marked with the
// installation's id, are neither listed nor counted for it, and its disconnect
// leaves them alone.
func TestAReclaimedInstallationShowsOnlyTheNewOwnersSpaces(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
		mkGitHubRow(t, st, now)
		for _, id := range []string{"TA", "TB"} {
			if err := st.CreateTenant(ctx, store.Tenant{ID: id, Slug: strings.ToLower(id), Name: id, Created: now}); err != nil {
				t.Fatal(err)
			}
		}
		a, b := store.InstallOwner{TenantID: "TA"}, store.InstallOwner{TenantID: "TB"}
		claim := func(o store.InstallOwner) {
			t.Helper()
			if err := store.ClaimGitHubInstallation(ctx, q, store.GitHubInstallation{ID: 5, Account: "Org", AccountType: "Organization", OwnerTenant: o.TenantID, Created: now, Updated: now}); err != nil {
				t.Fatal(err)
			}
		}
		claim(a)
		if err := store.InsertSpace(ctx, q, "TA", "SA", "A's notes", "", "", now); err != nil {
			t.Fatal(err)
		}
		if err := store.PutRemote(ctx, q, store.Remote{SpaceID: "SA", URL: "https://github.com/o/a.git", Branch: "main", State: "ok", Updated: now,
			CredType: "connection", Provider: "github", Via: "app", InstallationID: 5, RepoID: 1}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.DisconnectGitHubInstallation(ctx, q, 5, a, "lost by A", "any", now); err != nil {
			t.Fatal(err)
		}
		claim(b)
		list, n, err := store.SpacesOnGitHubInstallation(ctx, q, b, 5)
		if err != nil || n != 0 || len(list) != 0 {
			t.Fatalf("B sees %d space(s): %+v %v", n, list, err)
		}
		counts, err := store.GitHubSpaceCounts(ctx, q, b, []int64{5})
		if err != nil || counts[5] != 0 {
			t.Fatalf("B counts %v %v", counts, err)
		}
		ids, err := store.DisconnectGitHubInstallation(ctx, q, 5, b, "lost by B", "any", now)
		if err != nil || len(ids) != 0 {
			t.Fatalf("B's disconnect reached %v %v", ids, err)
		}
		r, err := store.GetRemote(ctx, q, "SA", store.RoleContent)
		if err != nil || r.LastError != "lost by A" {
			t.Fatalf("B's disconnect touched A's remote: %+v %v", r, err)
		}
	})
}

// A space whose own repository syncs through something else (a token) and only
// its attachment repository through the installation keeps the state of its own
// remote when the installation is disconnected: only the attachment remote loses access.
func TestDisconnectingMarksASpaceOnlyWhenItsOwnRepositoryWasOnIt(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
		mkGitHubRow(t, st, now)
		if err := st.CreateTenant(ctx, store.Tenant{ID: "T1", Slug: "t1", Name: "T1", Created: now}); err != nil {
			t.Fatal(err)
		}
		if err := store.ClaimGitHubInstallation(ctx, q, store.GitHubInstallation{ID: 5, Account: "Org", AccountType: "Organization", OwnerTenant: "T1", Created: now, Updated: now}); err != nil {
			t.Fatal(err)
		}
		if err := store.InsertSpace(ctx, q, "T1", "SX", "x", "", "", now); err != nil {
			t.Fatal(err)
		}
		if err := store.PutRemote(ctx, q, store.Remote{SpaceID: "SX", URL: "https://example.test/x.git", Branch: "main", State: "ok", Updated: now, CredType: "token"}); err != nil {
			t.Fatal(err)
		}
		if err := store.PutRemote(ctx, q, store.Remote{SpaceID: "SX", Role: store.RoleAssets, URL: "https://github.com/o/x-assets.git", Branch: "main", State: "ok", Updated: now,
			CredType: "connection", Provider: "github", Via: "app", InstallationID: 5, RepoID: 9}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.DisconnectGitHubInstallation(ctx, q, 5, store.InstallOwner{TenantID: "T1"}, "lost", "any", now); err != nil {
			t.Fatal(err)
		}
		if n := count(t, st, `SELECT COUNT(*) FROM spaces WHERE id = 'SX' AND remote_state = 'error'`); n != 0 {
			t.Fatal("the space's own remote state was marked although its own repository is not on the installation")
		}
		if r, _ := store.GetRemote(ctx, q, "SX", store.RoleAssets); r.ErrorKind != store.AccessLost {
			t.Fatalf("%+v", r)
		}
		if r, _ := store.GetRemote(ctx, q, "SX", store.RoleContent); r.State != "ok" {
			t.Fatalf("%+v", r)
		}
	})
}
