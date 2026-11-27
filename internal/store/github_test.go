package store_test

import (
	"errors"
	"fmt"
	"strings"
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
		for _, i := range []store.GitHubInstallation{{ID: 30, Account: "Zeta", AccountType: "Organization"}, {ID: 10, Account: "Alpha", AccountType: "Organization"},
			{ID: 20, Account: "Mid", AccountType: "Organization"}} {
			i.Updated = now
			if err := store.PutGitHubInstallation(ctx, q, i); err != nil {
				t.Fatal(err)
			}
		}
		for _, tn := range []string{"t1", "t2", "t3"} {
			if err := st.CreateTenant(ctx, store.Tenant{ID: tn, Slug: tn, Name: tn, Created: now, Updated: now}); err != nil {
				t.Fatal(err)
			}
		}
		// Shared per tenant: 30 with t1 and t2, 10 with t1, 20 with nobody.
		for id, ts := range map[int64][]string{30: {"t2", "t1"}, 10: {"t1"}} {
			if err := store.SetGitHubInstallationTenants(ctx, q, id, ts); err != nil {
				t.Fatal(err)
			}
		}
		t1, t2, t3 := store.InTenant("t1"), store.InTenant("t2"), store.InTenant("t3")
		list, err := store.ListGitHubInstallationsOf(ctx, q, t1, 0, 10)
		if err != nil || len(list) != 2 || list[0].ID != 10 || list[1].ID != 30 {
			t.Fatalf("%+v %v", list, err)
		}
		if list, _ = store.ListGitHubInstallationsOf(ctx, q, t1, 10, 10); len(list) != 1 || list[0].ID != 30 {
			t.Fatalf("%+v", list)
		}
		if list, _ = store.ListGitHubInstallationsOf(ctx, q, t2, 0, 10); len(list) != 1 || list[0].ID != 30 {
			t.Fatalf("%+v", list)
		}
		if list, _ = store.ListGitHubInstallationsOf(ctx, q, t3, 0, 10); len(list) != 0 {
			t.Fatalf("a tenant nothing is shared with sees %+v", list)
		}
		if list, _ = store.ListGitHubInstallationsOf(ctx, q, store.Scope{}, 0, 10); len(list) != 0 {
			t.Fatalf("the zero scope sees %+v", list)
		}
		if i, err := store.GitHubInstallationOf(ctx, q, t2, "ZETA"); err != nil || i.ID != 30 {
			t.Fatalf("%+v %v", i, err)
		}
		if _, err := store.GitHubInstallationOf(ctx, q, t2, "alpha"); !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
		if _, err := store.GitHubInstallationOf(ctx, q, t1, "mid"); !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
		if ok, err := store.GitHubInstallationShared(ctx, q, t1, 10); err != nil || !ok {
			t.Fatal(ok, err)
		}
		if ok, _ := store.GitHubInstallationShared(ctx, q, t2, 10); ok {
			t.Fatal("shared with the wrong tenant")
		}
		if ok, _ := store.GitHubInstallationShared(ctx, q, store.Scope{}, 10); ok {
			t.Fatal("the zero scope reaches an installation")
		}
		m, err := store.GitHubInstallationTenants(ctx, q, []int64{10, 20, 30, 40})
		if err != nil || len(m) != 2 || fmt.Sprint(m[10]) != "[t1]" || fmt.Sprint(m[30]) != "[t1 t2]" {
			t.Fatalf("%v %v", m, err)
		}
		// Setting replaces the whole set; an empty set stops sharing.
		if err := store.SetGitHubInstallationTenants(ctx, q, 30, []string{"t3", "t3"}); err != nil {
			t.Fatal(err)
		}
		if m, _ = store.GitHubInstallationTenants(ctx, q, []int64{30}); fmt.Sprint(m[30]) != "[t3]" {
			t.Fatalf("%v", m)
		}
		if err := store.SetGitHubInstallationTenants(ctx, q, 30, nil); err != nil {
			t.Fatal(err)
		}
		if m, _ = store.GitHubInstallationTenants(ctx, q, []int64{30}); len(m) != 0 {
			t.Fatalf("%v", m)
		}
		if err := store.SetGitHubInstallationTenants(ctx, q, 30, []string{"t1"}); err != nil {
			t.Fatal(err)
		}
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
		put("S1", store.Remote{Via: "app", InstallationID: 10, RepoID: 1})
		put("S2", store.Remote{Via: "app", InstallationID: 10, RepoID: 2})
		put("S3", store.Remote{Via: "user", CredUserID: "A", RepoID: 3})
		counts, err := store.GitHubSpaceCounts(ctx, q, []int64{10, 20})
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
		if err := store.PutRemote(ctx, q, rem("S1", store.Remote{Role: store.RoleAssets, Via: "app", InstallationID: 10, RepoID: 11})); err != nil {
			t.Fatal(err)
		}
		// S2: both through installation 10.
		if err := store.PutRemote(ctx, q, rem("S2", store.Remote{Via: "app", InstallationID: 10, RepoID: 2})); err != nil {
			t.Fatal(err)
		}
		if err := store.PutRemote(ctx, q, rem("S2", store.Remote{Role: store.RoleAssets, Via: "app", InstallationID: 10, RepoID: 21})); err != nil {
			t.Fatal(err)
		}
		// S3: the attachments only, through user A.
		if err := store.PutRemote(ctx, q, rem("S3", store.Remote{Role: store.RoleAssets, Via: "user", CredUserID: "A", RepoID: 31})); err != nil {
			t.Fatal(err)
		}
		if counts, err := store.GitHubSpaceCounts(ctx, q, []int64{10}); err != nil || counts[10] != 2 {
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

// Deleting the GitHub provider forgets its installations and who they were
// shared with; deleting a tenant takes its shares.
func TestGitHubInstallationSharesGoWithTheirOwners(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
		mkGitHubRow(t, st, now)
		for _, id := range []int64{1, 2} {
			if err := store.PutGitHubInstallation(ctx, q, store.GitHubInstallation{ID: id, Account: fmt.Sprint("org", id), AccountType: "Organization", Updated: now}); err != nil {
				t.Fatal(err)
			}
		}
		if err := st.CreateTenant(ctx, store.Tenant{ID: "T1", Slug: "one", Name: "One", Created: now}); err != nil {
			t.Fatal(err)
		}
		for _, id := range []int64{1, 2} {
			if err := store.SetGitHubInstallationTenants(ctx, q, id, []string{"T1", store.DefaultTenantID}); err != nil {
				t.Fatal(err)
			}
		}
		if err := st.DeleteTenant(ctx, "T1"); err != nil {
			t.Fatal(err)
		}
		m, _ := store.GitHubInstallationTenants(ctx, q, []int64{1, 2})
		if fmt.Sprint(m[1]) != "["+store.DefaultTenantID+"]" || len(m[2]) != 1 {
			t.Fatalf("a deleted tenant is still shared with: %v", m)
		}
		if err := store.DeleteGitHubProvider(ctx, q); err != nil {
			t.Fatal(err)
		}
		if n := count(t, st, `SELECT COUNT(*) FROM provider_shares`); n != 0 {
			t.Fatalf("%d shares outlived their provider", n)
		}
		if n := count(t, st, `SELECT COUNT(*) FROM provider_installations`); n != 0 {
			t.Fatalf("%d installations outlived their provider", n)
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
