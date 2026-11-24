package store_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

func TestGitHubAuthorizations(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "A", "a", "member")
		mkUser(t, st, "B", "b", "member")
		now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
		q := st.Q()
		a := store.GitHubAuthorization{UserID: "A", GitHubID: 77, Login: "a-gh", AccessEnc: "ae", RefreshEnc: "re", State: "ok",
			AccessExpires: now.Add(8 * time.Hour), RefreshExpires: now.Add(100 * 24 * time.Hour), Connected: now, Updated: now}
		if err := store.PutGitHubAuthorization(ctx, q, a); err != nil {
			t.Fatal(err)
		}
		got, err := store.GetGitHubAuthorization(ctx, q, "A")
		if err != nil || got.Version != 1 || got.Login != "a-gh" || !got.RefreshExpires.Equal(a.RefreshExpires) {
			t.Fatalf("%+v %v", got, err)
		}
		// One GitHub account, one user.
		b := a
		b.UserID = "B"
		if err := store.PutGitHubAuthorization(ctx, q, b); !errors.Is(err, store.ErrExists) {
			t.Fatalf("%v", err)
		}
		if u, err := store.GitHubAccountUser(ctx, q, 77); err != nil || u != "A" {
			t.Fatalf("%q %v", u, err)
		}
		// Connecting again replaces the pair and raises the version.
		a.AccessEnc = "ae2"
		if err := store.PutGitHubAuthorization(ctx, q, a); err != nil {
			t.Fatal(err)
		}
		got, _ = store.GetGitHubAuthorization(ctx, q, "A")
		if got.Version != 2 || got.AccessEnc != "ae2" {
			t.Fatalf("%+v", got)
		}
		// A renewal replaces only the pair it was made from.
		ok, err := store.ReplaceGitHubTokens(ctx, q, "A", 1, "x", "y", now, now, now)
		if err != nil || ok {
			t.Fatalf("an old version replaced the pair: %v %v", ok, err)
		}
		ok, err = store.ReplaceGitHubTokens(ctx, q, "A", 2, "ae3", "re3", now.Add(8*time.Hour), now.Add(180*24*time.Hour), now)
		if err != nil || !ok {
			t.Fatalf("%v %v", ok, err)
		}
		got, _ = store.GetGitHubAuthorization(ctx, q, "A")
		if got.Version != 3 || got.RefreshEnc != "re3" {
			t.Fatalf("%+v", got)
		}
		// Due: refresh tokens ending before the given time, in good standing.
		if due, _ := store.DueGitHubRenewals(ctx, q, now.Add(30*24*time.Hour), store.DueCursor{}, 10); len(due) != 0 {
			t.Fatalf("%+v", due)
		}
		if due, _ := store.DueGitHubRenewals(ctx, q, now.Add(200*24*time.Hour), store.DueCursor{}, 10); len(due) != 1 {
			t.Fatalf("%+v", due)
		}
		// Expiring drops the tokens, only for the version it was asked about.
		if ok, _ := store.ExpireGitHubAuthorization(ctx, q, "A", 1, now); ok {
			t.Fatal("expired an old version")
		}
		if ok, _ := store.ExpireGitHubAuthorization(ctx, q, "A", 3, now); !ok {
			t.Fatal("did not expire")
		}
		got, _ = store.GetGitHubAuthorization(ctx, q, "A")
		if got.State != "expired" || got.AccessEnc != "" || got.RefreshEnc != "" {
			t.Fatalf("%+v", got)
		}
		if due, _ := store.DueGitHubRenewals(ctx, q, now.Add(400*24*time.Hour), store.DueCursor{}, 10); len(due) != 0 {
			t.Fatalf("an expired connection is due: %+v", due)
		}
		if n, _ := store.CountGitHubAuthorizations(ctx, q); n != 1 {
			t.Fatal(n)
		}
		if err := store.DeleteGitHubAuthorization(ctx, q, "A"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.GetGitHubAuthorization(ctx, q, "A"); !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
		// Deleting the user deletes the connection.
		if err := store.PutGitHubAuthorization(ctx, q, b); err != nil {
			t.Fatal(err)
		}
		if _, err := q.Exec(ctx, `DELETE FROM users WHERE id = 'B'`); err != nil {
			t.Fatal(err)
		}
		if n, _ := store.CountGitHubAuthorizations(ctx, q); n != 0 {
			t.Fatal("a deleted user's connection stayed")
		}
	})
}

func TestGitHubInstallationsAndSpaces(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "A", "a", "member")
		now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
		q := st.Q()
		for _, i := range []store.GitHubInstallation{{ID: 30, Account: "Zeta", AccountType: "Organization"}, {ID: 10, Account: "Alpha", AccountType: "Organization"},
			{ID: 20, Account: "Mid", AccountType: "Organization"}} {
			i.Updated = now
			if err := store.PutGitHubInstallation(ctx, q, i); err != nil {
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
		put("S1", store.Remote{GHVia: "app", GHInstallationID: 10, GHRepoID: 1})
		put("S2", store.Remote{GHVia: "app", GHInstallationID: 10, GHRepoID: 2})
		put("S3", store.Remote{GHVia: "user", GHUserID: "A", GHRepoID: 3})
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
		r, _ := store.GetRemote(ctx, q, "S3")
		if r.GHVia != "user" || r.GHUserID != "A" || r.GHRepoID != 3 {
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

// Due renewals page by (refresh end, user id), ties included.
func TestDueGitHubRenewalsPageByKeyset(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
		q := st.Q()
		ends := map[string]time.Duration{"A": 2, "B": 1, "C": 1, "D": 1, "E": 3}
		var i int64
		for _, id := range []string{"A", "B", "C", "D", "E"} {
			i++
			mkUser(t, st, id, "u"+id, "member")
			err := store.PutGitHubAuthorization(ctx, q, store.GitHubAuthorization{UserID: id, GitHubID: i, Login: id, AccessEnc: "ae", RefreshEnc: "re",
				State: "ok", RefreshExpires: now.Add(ends[id] * time.Hour), Connected: now, Updated: now})
			if err != nil {
				t.Fatal(err)
			}
		}
		var got []string
		var cur store.DueCursor
		for pages := 0; pages < 10; pages++ {
			due, err := store.DueGitHubRenewals(ctx, q, now.Add(24*time.Hour), cur, 2)
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range due {
				got = append(got, a.UserID)
				cur = a.Next()
			}
			if len(due) < 2 {
				break
			}
		}
		if strings.Join(got, "") != "BCDAE" {
			t.Fatalf("%v", got)
		}
	})
}

// Two users connecting one GitHub account at once: the one that loses the race
// gets ErrExists from the unique index, not a database error.
func TestConnectingAnAccountTakenMeanwhileIsErrExists(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "A", "a", "member")
		mkUser(t, st, "B", "b", "member")
		now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
		q := st.Q()
		a := store.GitHubAuthorization{UserID: "A", GitHubID: 77, Login: "gh", AccessEnc: "ae", State: "ok", Connected: now, Updated: now}
		b := a
		b.UserID = "B"
		store.SetGitHubPutRaceHook(func() {
			store.SetGitHubPutRaceHook(nil)
			if err := store.PutGitHubAuthorization(ctx, q, a); err != nil {
				t.Error(err)
			}
		})
		t.Cleanup(func() { store.SetGitHubPutRaceHook(nil) })
		if err := store.PutGitHubAuthorization(ctx, q, b); !errors.Is(err, store.ErrExists) {
			t.Fatalf("%v", err)
		}
		if u, _ := store.GitHubAccountUser(ctx, q, 77); u != "A" {
			t.Fatalf("%q", u)
		}
	})
}

// Forgetting the installations forgets who they were shared with; deleting a
// tenant takes its shares.
func TestGitHubInstallationSharesGoWithTheirOwners(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
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
		if err := store.DeleteGitHubInstallations(ctx, q); err != nil {
			t.Fatal(err)
		}
		if n := count(t, st, `SELECT COUNT(*) FROM github_installation_tenants`); n != 0 {
			t.Fatalf("%d shares outlived their installations", n)
		}
	})
}

func TestGitHubLeftovers(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "A", "a", "member")
		mkUser(t, st, "B", "b", "member")
		q := st.Q()
		now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
		put := func(user string, repo int64, at time.Time) {
			t.Helper()
			l := store.GitHubLeftover{UserID: user, RepoID: repo, FullName: fmt.Sprint("o/r", repo), HTMLURL: fmt.Sprint("https://github.com/o/r", repo), SpaceName: "Notes", Created: at}
			if err := store.PutGitHubLeftover(ctx, q, l); err != nil {
				t.Fatal(err)
			}
		}
		put("A", 1, now)
		put("A", 2, now.Add(time.Minute))
		put("B", 1, now)
		put("A", 1, now.Add(2*time.Minute)) // the same repository again: one reminder, the newer
		list, err := store.ListGitHubLeftovers(ctx, q, "A")
		if err != nil || len(list) != 2 || list[0].RepoID != 1 || list[1].RepoID != 2 || list[0].SpaceName != "Notes" || !list[0].Created.Equal(now.Add(2*time.Minute)) {
			t.Fatalf("newest first: %+v %v", list, err)
		}
		// Per user: A's dismissal leaves B's.
		if ok, err := store.DeleteGitHubLeftover(ctx, q, "A", 1); err != nil || !ok {
			t.Fatal(ok, err)
		}
		if ok, _ := store.DeleteGitHubLeftover(ctx, q, "A", 1); ok {
			t.Fatal("dismissed twice")
		}
		if l, _ := store.ListGitHubLeftovers(ctx, q, "B"); len(l) != 1 {
			t.Fatalf("%+v", l)
		}
		// A space connecting to the repository clears the reminder, for whoever has it.
		for _, id := range []string{"S1"} {
			if err := store.InsertSpace(ctx, q, store.DefaultTenantID, id, id, "", "", now); err != nil {
				t.Fatal(err)
			}
		}
		r := store.Remote{SpaceID: "S1", URL: "https://github.com/o/r1.git", Branch: "main", State: "ok", Updated: now, CredType: "connection", GHVia: "user", GHUserID: "A", GHRepoID: 1}
		if err := store.PutRemote(ctx, q, r); err != nil {
			t.Fatal(err)
		}
		if l, _ := store.ListGitHubLeftovers(ctx, q, "B"); len(l) != 0 {
			t.Fatalf("a connected repository is still a leftover: %+v", l)
		}
		if l, _ := store.ListGitHubLeftovers(ctx, q, "A"); len(l) != 1 || l[0].RepoID != 2 {
			t.Fatalf("%+v", l)
		}
		// Only the newest ones are kept.
		for i := int64(100); i < 140; i++ {
			put("A", i, now.Add(time.Duration(i)*time.Hour))
		}
		if l, _ := store.ListGitHubLeftovers(ctx, q, "A"); len(l) != store.MaxGitHubLeftovers || l[0].RepoID != 139 {
			t.Fatalf("%d kept, first %+v", len(l), l[0])
		}
		// Deleting the user deletes their reminders.
		if _, err := q.Exec(ctx, `DELETE FROM users WHERE id = 'B'`); err != nil {
			t.Fatal(err)
		}
		if n := count(t, st, `SELECT COUNT(*) FROM github_leftovers WHERE user_id = 'B'`); n != 0 {
			t.Fatal("a deleted user's reminders stayed")
		}
	})
}
