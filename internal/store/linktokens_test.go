package store_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// The repository links (consolidation 5b): GitLab and Gitea connections are rows
// of linked_accounts that carry tokens, the reminders rows of repo_leftovers.

func mkForge(t *testing.T, st *store.Store, id string) {
	t.Helper()
	now := time.Now()
	if err := store.InsertConnProviderRow(ctx, st.Q(), store.ConnProviderRow{ID: id, Type: "gitea", Host: id + ".example.com", Scheme: "https", Name: id,
		Owner: store.ConnProviderSite, State: "ok", Created: now, Updated: now}); err != nil {
		t.Fatal(err)
	}
}

func repoLink(id, provider, user, subject string, renewAt time.Time) store.RepoLink {
	now := time.Now()
	l := store.RepoLink{ID: id, ProviderID: provider, UserID: user, Subject: subject, Login: user + "-login", AccessEnc: "a", RefreshEnc: "r",
		State: store.LinkOK, Connected: now, Updated: now}
	if !renewAt.IsZero() {
		l.RefreshExpires, l.RenewAt = renewAt.Add(24*time.Hour), renewAt
	}
	return l
}

func TestOnePersonHasOneLinkAndOneAccountBelongsToOnePerson(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "U1", "u1", "member")
		mkUser(t, st, "U2", "u2", "member")
		mkForge(t, st, "gt")
		id, err := store.PutRepoLink(ctx, st.Q(), repoLink("L1", "gt", "U1", "7", time.Time{}))
		if err != nil || id != "L1" {
			t.Fatalf("%q %v", id, err)
		}
		// The account is U1's: U2 cannot have it, and nothing of U1's changes.
		if _, err := store.PutRepoLink(ctx, st.Q(), repoLink("L2", "gt", "U2", "7", time.Time{})); !errors.Is(err, store.ErrExists) {
			t.Fatalf("%v", err)
		}
		// U1 connecting again replaces their link: the id stays, the version goes up.
		again := repoLink("L3", "gt", "U1", "7", time.Time{})
		again.Login = "renamed"
		if id, err := store.PutRepoLink(ctx, st.Q(), again); err != nil || id != "L1" {
			t.Fatalf("%q %v", id, err)
		}
		l, err := store.GetRepoLink(ctx, st.Q(), "gt", "U1")
		if err != nil || l.Version != 2 || l.Login != "renamed" {
			t.Fatalf("%+v %v", l, err)
		}
		if n, _ := store.CountProviderLinks(ctx, st.Q(), "gt"); n != 1 {
			t.Fatalf("%d links", n)
		}
	})
}

func TestTwoPeopleLinkingOneAccountAtOnceOnlyOneWins(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkForge(t, st, "gt")
		const n = 6
		for i := 0; i < n; i++ {
			mkUser(t, st, "U"+string(rune('A'+i)), "u"+string(rune('a'+i)), "member")
		}
		var wg sync.WaitGroup
		errs := make([]error, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				u := "U" + string(rune('A'+i))
				_, errs[i] = store.PutRepoLink(ctx, st.Q(), repoLink("L"+u, "gt", u, "99", time.Time{}))
			}()
		}
		wg.Wait()
		won := 0
		for _, err := range errs {
			switch {
			case err == nil:
				won++
			case !errors.Is(err, store.ErrExists):
				t.Fatalf("%v", err)
			}
		}
		if c, _ := store.CountProviderLinks(ctx, st.Q(), "gt"); won != 1 || c != 1 {
			t.Fatalf("%d won, %d links", won, c)
		}
	})
}

func TestAPersonsLinksGoWithThePersonAndTheProvidersWithTheProvider(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "U1", "u1", "member")
		mkUser(t, st, "U2", "u2", "member")
		mkForge(t, st, "gt")
		mkForge(t, st, "gt2")
		now := time.Now()
		for _, c := range []struct{ id, prov, user, subj string }{{"L1", "gt", "U1", "1"}, {"L2", "gt2", "U1", "1"}, {"L3", "gt", "U2", "2"}} {
			if _, err := store.PutRepoLink(ctx, st.Q(), repoLink(c.id, c.prov, c.user, c.subj, time.Time{})); err != nil {
				t.Fatal(err)
			}
		}
		for _, p := range []string{"gt", "gt2"} {
			if err := store.PutRepoLeftover(ctx, st.Q(), store.RepoLeftover{ProviderID: p, UserID: "U1", RepoID: 5, FullName: "o/r", HTMLURL: "u", Created: now}); err != nil {
				t.Fatal(err)
			}
		}
		// The provider goes: its links and reminders with it, the other provider's stay.
		if err := store.DeleteConnProviderRow(ctx, st.Q(), "gt2"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.GetRepoLink(ctx, st.Q(), "gt2", "U1"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%v", err)
		}
		if ls, _ := store.ListRepoLeftovers(ctx, st.Q(), "gt2", "U1"); len(ls) != 0 {
			t.Fatalf("%d reminders of a removed provider", len(ls))
		}
		if _, err := store.GetRepoLink(ctx, st.Q(), "gt", "U1"); err != nil {
			t.Fatal(err)
		}
		// The person goes: theirs with them.
		if _, err := st.Q().Exec(ctx, `DELETE FROM users WHERE id = 'U1'`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.GetRepoLink(ctx, st.Q(), "gt", "U1"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%v", err)
		}
		if ls, _ := store.ListRepoLeftovers(ctx, st.Q(), "gt", "U1"); len(ls) != 0 {
			t.Fatalf("%d reminders of a removed person", len(ls))
		}
		if n, _ := store.CountProviderLinks(ctx, st.Q(), "gt"); n != 1 {
			t.Fatalf("%d links", n)
		}
	})
}

// A sign-in link has no tokens: it is not a connection, and taking a person's
// repositories away from a link that signs them in leaves the sign-in.
func TestASignInLinkIsNotAConnectionAndKeepsItsSignInWhenTheTokensGo(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "U1", "u1", "member")
		mkForge(t, st, "gt")
		if err := store.InsertLink(ctx, st.Q(), store.Link{ID: "S1", ProviderID: "gt", UserID: "U1", Subject: "1", SignIn: true, Created: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.GetRepoLink(ctx, st.Q(), "gt", "U1"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a sign-in link is a connection: %v", err)
		}
		if n, _ := store.CountProviderLinks(ctx, st.Q(), "gt"); n != 0 {
			t.Fatalf("%d connections", n)
		}
		l := repoLink("X", "gt", "U1", "1", time.Time{})
		if id, err := store.PutRepoLink(ctx, st.Q(), l); err != nil || id != "S1" {
			t.Fatalf("%q %v", id, err)
		}
		if err := store.DeleteRepoLink(ctx, st.Q(), "gt", "U1"); err != nil {
			t.Fatal(err)
		}
		if got, err := store.FindLink(ctx, st.Q(), "gt", "1"); err != nil || !got.SignIn {
			t.Fatalf("%+v %v", got, err)
		}
		if _, err := store.GetRepoLink(ctx, st.Q(), "gt", "U1"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%v", err)
		}
	})
}

func TestTheRenewalIndexServesTheDueLinksInOrderAndTheNextOne(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		for _, u := range []string{"U1", "U2", "U3", "U4", "U5"} {
			mkUser(t, st, u, u, "member")
		}
		mkForge(t, st, "gt")
		base := time.Now().Truncate(time.Millisecond)
		due := map[string]time.Time{"U1": base.Add(3 * time.Hour), "U2": base.Add(time.Hour), "U3": base.Add(time.Hour), "U4": base.Add(10 * time.Hour)}
		for u, at := range due {
			if _, err := store.PutRepoLink(ctx, st.Q(), repoLink("L"+u, "gt", u, u, at)); err != nil {
				t.Fatal(err)
			}
		}
		// A link that never ends (no renewal time) is never due.
		if _, err := store.PutRepoLink(ctx, st.Q(), repoLink("LU5", "gt", "U5", "U5", time.Time{})); err != nil {
			t.Fatal(err)
		}
		got, err := store.DueLinkRenewals(ctx, st.Q(), base.Add(3*time.Hour), store.DueLinkCursor{}, 10)
		if err != nil || len(got) != 3 || got[0].ID != "LU2" || got[1].ID != "LU3" || got[2].ID != "LU1" {
			t.Fatalf("%v %v", ids(got), err)
		}
		// Pages by the cursor: no link twice, none missed.
		page, _ := store.DueLinkRenewals(ctx, st.Q(), base.Add(3*time.Hour), store.DueLinkCursor{}, 2)
		rest, _ := store.DueLinkRenewals(ctx, st.Q(), base.Add(3*time.Hour), store.DueLinkCursor{RenewAt: page[1].RenewAt, ID: page[1].ID}, 2)
		if len(page) != 2 || len(rest) != 1 || rest[0].ID != "LU1" {
			t.Fatalf("%v then %v", ids(page), ids(rest))
		}
		next, ok, err := store.NextLinkRenewal(ctx, st.Q(), base.Add(3*time.Hour))
		if err != nil || !ok || !next.Equal(base.Add(10*time.Hour)) {
			t.Fatalf("%v %v %v", next, ok, err)
		}
		if _, ok, _ := store.NextLinkRenewal(ctx, st.Q(), base.Add(10*time.Hour)); ok {
			t.Fatal("a next renewal after the last one")
		}
		// A link that needs the person is not renewed; it leaves the index.
		l, _ := store.GetRepoLink(ctx, st.Q(), "gt", "U2")
		if ok, err := store.ExpireLink(ctx, st.Q(), l.ID, l.Version, base); err != nil || !ok {
			t.Fatalf("%v %v", ok, err)
		}
		if ok, _ := store.ExpireLink(ctx, st.Q(), l.ID, l.Version, base); ok {
			t.Fatal("a link was ended twice: two callers would both tell the person")
		}
		got, _ = store.DueLinkRenewals(ctx, st.Q(), base.Add(3*time.Hour), store.DueLinkCursor{}, 10)
		if len(got) != 2 {
			t.Fatalf("%v", ids(got))
		}
		if e, _ := store.GetRepoLink(ctx, st.Q(), "gt", "U2"); e.State != store.LinkExpired || e.AccessEnc != "" || e.RefreshEnc != "" {
			t.Fatalf("%+v", e)
		}
	})
}

func ids(ls []store.RepoLink) []string {
	var out []string
	for _, l := range ls {
		out = append(out, l.ID)
	}
	return out
}

// Replacing a pair works only from the version it was made from, so a renewal
// that was overtaken by a reconnection writes nothing.
func TestReplacingTokensWorksOnlyFromTheVersionTheyWereMadeFrom(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "U1", "u1", "member")
		mkForge(t, st, "gt")
		if _, err := store.PutRepoLink(ctx, st.Q(), repoLink("L1", "gt", "U1", "1", time.Time{})); err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		if ok, err := store.ReplaceLinkTokens(ctx, st.Q(), "L1", 1, "a2", "r2", now.Add(time.Hour), now.Add(48*time.Hour), now.Add(40*time.Hour), now); err != nil || !ok {
			t.Fatalf("%v %v", ok, err)
		}
		if ok, _ := store.ReplaceLinkTokens(ctx, st.Q(), "L1", 1, "a3", "r3", time.Time{}, time.Time{}, time.Time{}, now); ok {
			t.Fatal("a replacement from an old version was written")
		}
		l, _ := store.GetRepoLink(ctx, st.Q(), "gt", "U1")
		if l.Version != 2 || l.AccessEnc != "a2" || l.RenewAt.IsZero() {
			t.Fatalf("%+v", l)
		}
	})
}

func TestRemindersAreKeptPerPersonAndProviderNewestFirstAndCapped(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "U1", "u1", "member")
		mkForge(t, st, "gt")
		base := time.Now()
		for i := 1; i <= store.MaxGitHubLeftovers+3; i++ {
			l := store.RepoLeftover{ProviderID: "gt", UserID: "U1", RepoID: int64(i), FullName: "o/r", HTMLURL: "u", Created: base.Add(time.Duration(i) * time.Second)}
			if err := store.PutRepoLeftover(ctx, st.Q(), l); err != nil {
				t.Fatal(err)
			}
		}
		got, err := store.ListRepoLeftovers(ctx, st.Q(), "gt", "U1")
		if err != nil || len(got) != store.MaxGitHubLeftovers || got[0].RepoID != int64(store.MaxGitHubLeftovers+3) {
			t.Fatalf("%d reminders, first %+v, %v", len(got), got[0], err)
		}
		if ok, _ := store.DeleteRepoLeftover(ctx, st.Q(), "gt", "U1", got[0].RepoID); !ok {
			t.Fatal("not deleted")
		}
		if ok, _ := store.DeleteRepoLeftover(ctx, st.Q(), "gt", "U1", got[0].RepoID); ok {
			t.Fatal("deleted twice")
		}
	})
}
