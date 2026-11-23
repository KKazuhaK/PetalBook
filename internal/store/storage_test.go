package store_test

import (
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

// The queries of the storage milestone, on every database: the requirement of
// a remote with its deadlines, the filters of the administration's list, the
// sums of the status and the candidates of the eviction.
func TestStoragePolicyQueries(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		q := st.Q()
		day := 24 * time.Hour
		t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		for _, u := range []store.User{{ID: "U1", Username: "u1", Name: "u1", Role: "member", Created: t0}, {ID: "U2", Username: "u2", Name: "u2", Role: "member", Created: t0}} {
			if err := store.InsertUser(ctx, q, u); err != nil {
				t.Fatal(err)
			}
		}
		// Four spaces: an old local one, an old one with a token remote, a new local one, an old one whose owner is exempt.
		spaces := []struct {
			id, owner string
			created   time.Time
			cred      string
		}{
			{"S1", "U1", t0, ""}, {"S2", "U1", t0, "token"}, {"S3", "U1", t0.Add(100 * day), ""}, {"S4", "U2", t0, ""},
		}
		for _, s := range spaces {
			if err := store.InsertSpace(ctx, q, s.id, s.id, "", "", s.created); err != nil {
				t.Fatal(err)
			}
			if err := store.AddMember(ctx, q, s.id, s.owner, "owner", s.created); err != nil {
				t.Fatal(err)
			}
			if s.cred != "" {
				if err := store.PutRemote(ctx, q, store.Remote{SpaceID: s.id, URL: "https://example.test/x.git", Branch: "main", CredType: s.cred, State: store.RemoteOK, Updated: t0}); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := store.SetRemoteExempt(ctx, q, "U2", true); err != nil {
			t.Fatal(err)
		}
		since := t0.Add(50 * day)
		pol := store.RemotePolicy{Require: "any", All: true, Since: since, Grace: 10 * day, Now: since.Add(5 * day)}
		// S1 is asked (older than since, scope all): deadline since+10d, not past. S2 meets it. S3 was made
		// after since: its own creation + 10d, in the past by then? created t0+100d: after Now, so in grace.
		// S4's owner is exempt.
		g, o, e, err := store.PolicyCounts(ctx, q, pol)
		if err != nil || g != 2 || o != 0 || e != 1 {
			t.Fatalf("grace %d overdue %d exempt %d: %v", g, o, e, err)
		}
		pol.Now = since.Add(11 * day)
		if g, o, _, _ = mustCounts(t, q, pol); g != 1 || o != 1 {
			t.Fatalf("after the first deadline: grace %d overdue %d", g, o)
		}
		// "github" is not met by a token.
		gh := pol
		gh.Require = "github"
		if g, o, _, _ = mustCounts(t, q, gh); g+o != 3 {
			t.Fatalf("github: %d+%d", g, o)
		}
		// Scope "new": the old spaces are not covered, the new one is.
		newOnly := pol
		newOnly.All = false
		if g, o, _, _ = mustCounts(t, q, newOnly); g != 1 || o != 0 {
			t.Fatalf("scope new: grace %d overdue %d", g, o)
		}
		if d, ok := newOnly.Deadline(t0); ok {
			t.Fatalf("an old space has a deadline under scope new: %v", d)
		}
		// No requirement, nothing is counted.
		if g, o, e, _ = mustCounts(t, q, store.RemotePolicy{Now: pol.Now}); g+o+e != 0 {
			t.Fatal("counted without a requirement")
		}
		// The list filters.
		over, _, err := store.ListAllSpaces(ctx, q, store.SpaceFilter{Overdue: &pol, Limit: 50})
		if err != nil || len(over) != 1 || over[0].ID != "S1" || over[0].Storage.Exempt {
			t.Fatalf("%+v %v", over, err)
		}
		for cache, want := range map[string][]string{"local": {"S1", "S3", "S4"}, "full": {"S2"}, "partial": {}, "evicted": {}} {
			rows, _, err := store.ListAllSpaces(ctx, q, store.SpaceFilter{Cache: cache, Limit: 50})
			var ids []string
			for _, r := range rows {
				ids = append(ids, r.ID)
			}
			if err != nil || len(ids) != len(want) {
				t.Fatalf("%s: %v %v", cache, ids, err)
			}
		}
		if err := store.SetSpaceCache(ctx, q, "S2", store.CachePartial); err != nil {
			t.Fatal(err)
		}
		// Sums and candidates.
		if err := store.SetSpaceSize(ctx, q, "S2", 1000, t0); err != nil {
			t.Fatal(err)
		}
		if err := store.SetSpaceIndexBytes(ctx, q, "S2", 10); err != nil {
			t.Fatal(err)
		}
		sum, err := store.SumStorage(ctx, q)
		if err != nil || sum.Local != 3 || sum.Partial != 1 || sum.Full != 0 || sum.Evicted != 0 || sum.CloneBytes != 1000 || sum.IndexBytes != 10 {
			t.Fatalf("%+v %v", sum, err)
		}
		if err := store.TouchSpaceUsed(ctx, q, "S2", t0.Add(day)); err != nil {
			t.Fatal(err)
		}
		cs, err := store.EvictionCandidates(ctx, q, t0.Add(2*day), 10)
		if err != nil || len(cs) != 1 || cs[0].ID != "S2" || cs[0].CloneBytes != 1000 {
			t.Fatalf("%+v %v", cs, err)
		}
		// Used since: not a candidate. Pinned: not a candidate. A remote that is not ok: not a candidate.
		if cs, _ = store.EvictionCandidates(ctx, q, t0.Add(day), 10); len(cs) != 0 {
			t.Fatalf("a space used at the cut is a candidate: %+v", cs)
		}
		if err := store.SetSpacePinned(ctx, q, "S2", true); err != nil {
			t.Fatal(err)
		}
		if cs, _ = store.EvictionCandidates(ctx, q, t0.Add(2*day), 10); len(cs) != 0 {
			t.Fatalf("a pinned space is a candidate: %+v", cs)
		}
		store.SetSpacePinned(ctx, q, "S2", false)
		if err := store.UpdateRemoteStatus(ctx, q, "S2", store.RemoteStatus{State: store.RemoteOffline}, t0); err != nil {
			t.Fatal(err)
		}
		if cs, _ = store.EvictionCandidates(ctx, q, t0.Add(2*day), 10); len(cs) != 0 {
			t.Fatalf("a space whose remote is down is a candidate: %+v", cs)
		}
		// A space that was never used (last_used 0) is not one either; creation records a use.
		if f, err := store.GetPolicyFacts(ctx, q, "S4"); err != nil || !f.OwnerExempt || f.CredType != "" || f.Created.IsZero() {
			t.Fatalf("%+v %v", f, err)
		}
		if n, err := store.CountDrafts(ctx, q, "S1"); err != nil || n != 0 {
			t.Fatalf("%d %v", n, err)
		}
	})
}

func mustCounts(t *testing.T, q store.Q, p store.RemotePolicy) (int, int, int, error) {
	t.Helper()
	g, o, e, err := store.PolicyCounts(ctx, q, p)
	if err != nil {
		t.Fatal(err)
	}
	return g, o, e, err
}
