package store_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

func mkGrant(t *testing.T, st *store.Store, id, user, hash string, created time.Time, spaces ...string) {
	t.Helper()
	g := store.OAuthGrant{ID: id, UserID: user, ClientID: "pbc_x", ClientName: "Helper", Access: "write", Resource: "https://n.example/mcp",
		Created: created, LastUsed: created, RefreshHash: hash}
	for _, s := range spaces {
		g.Spaces = append(g.Spaces, store.TokenSpace{ID: s})
	}
	if err := st.Tx(ctx, func(q store.Q) error { return store.InsertOAuthGrant(ctx, q, g) }); err != nil {
		t.Fatal(err)
	}
}

func TestOAuthSigningKeyIsStoredOnce(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		if _, ok, err := store.GetOAuthSigningKey(ctx, st.Q()); err != nil || ok {
			t.Fatalf("%v %v", ok, err)
		}
		now := time.Now()
		if won, err := store.InsertOAuthSigningKey(ctx, st.Q(), "first", now); err != nil || !won {
			t.Fatalf("%v %v", won, err)
		}
		if won, err := store.InsertOAuthSigningKey(ctx, st.Q(), "second", now); err != nil || won {
			t.Fatalf("%v %v", won, err)
		}
		if v, ok, err := store.GetOAuthSigningKey(ctx, st.Q()); err != nil || !ok || v != "first" {
			t.Fatalf("%q %v %v", v, ok, err)
		}
	})
}

func TestOAuthGrantsAndRefreshRotation(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "A", "a", "member")
		mkUser(t, st, "B", "b", "member")
		mkSpace(t, st, "S1", "one")
		now := time.UnixMilli(1_700_000_000_000)
		mkGrant(t, st, "G1", "A", "h0", now, "S1", "S2")
		mkGrant(t, st, "G2", "B", "k0", now.Add(time.Second), "S1")

		g, err := store.GetOAuthGrant(ctx, st.Q(), "G1", false)
		if err != nil || g.UserID != "A" || g.Access != "write" || len(g.Spaces) != 2 || g.RefreshHash != "h0" || g.Retired != nil {
			t.Fatalf("%+v %v", g, err)
		}
		if _, err := store.GetOAuthGrant(ctx, st.Q(), "nope", false); err != store.ErrNotFound {
			t.Fatal(err)
		}

		// The token is replaced only while it is the one that was read.
		for i := 0; i < 12; i++ {
			old, next := fmt.Sprintf("h%d", i), fmt.Sprintf("h%d", i+1)
			if won, err := store.RotateOAuthRefresh(ctx, st.Q(), "G1", old, next, now.Add(time.Duration(i+1)*time.Minute), 8); err != nil || !won {
				t.Fatalf("%d: %v %v", i, won, err)
			}
			if won, err := store.RotateOAuthRefresh(ctx, st.Q(), "G1", old, "other", now, 8); err != nil || won {
				t.Fatalf("%d: the replaced token replaced again: %v %v", i, won, err)
			}
		}
		g, err = store.GetOAuthGrant(ctx, st.Q(), "G1", true)
		if err != nil || g.RefreshHash != "h12" || len(g.Retired) != 8 || g.Retired[0].Hash != "h4" || g.Retired[7].Hash != "h11" || !g.LastUsed.After(now) {
			t.Fatalf("%+v %v", g, err)
		}

		// Listing: newest first, with owners and the names of spaces the owner is in.
		if err := st.Tx(ctx, func(q store.Q) error { return store.AddMember(ctx, q, "S1", "A", "owner", now) }); err != nil {
			t.Fatal(err)
		}
		rows, err := store.ListOAuthGrants(ctx, st.Q(), store.GrantFilter{})
		if err != nil || len(rows) != 2 || rows[0].ID != "G2" || rows[1].ID != "G1" || rows[1].Username != "a" {
			t.Fatalf("%+v %v", rows, err)
		}
		names := map[string]string{}
		for _, s := range rows[1].Spaces {
			names[s.ID] = s.Name
		}
		if names["S1"] != "one" || names["S2"] != "" || len(names) != 2 || rows[0].Spaces[0].Name != "" {
			t.Fatalf("%v", names)
		}
		page, _ := store.ListOAuthGrants(ctx, st.Q(), store.GrantFilter{Limit: 1})
		after, _ := store.ListOAuthGrants(ctx, st.Q(), store.GrantFilter{AfterCreated: page[0].Created, AfterID: page[0].ID})
		if len(page) != 1 || len(after) != 1 || after[0].ID != "G1" {
			t.Fatalf("%v %v", page, after)
		}
		if one, _ := store.ListOAuthGrants(ctx, st.Q(), store.GrantFilter{UserID: "B"}); len(one) != 1 || one[0].ID != "G2" {
			t.Fatal(one)
		}

		// A grant ends with everything that hangs on it.
		if ok, err := store.DeleteOAuthGrant(ctx, st.Q(), "G1"); err != nil || !ok {
			t.Fatalf("%v %v", ok, err)
		}
		if ok, err := store.DeleteOAuthGrant(ctx, st.Q(), "G1"); err != nil || ok {
			t.Fatalf("%v %v", ok, err)
		}
		var n int
		st.Q().QueryRow(ctx, `SELECT COUNT(*) FROM oauth_retired`).Scan(&n)
		if n != 0 {
			t.Fatalf("%d retired hashes outlived their grant", n)
		}
		// Idle ones end by themselves: G2 was last used at now+1s.
		if n, err := store.DeleteIdleOAuthGrants(ctx, st.Q(), now); err != nil || n != 0 {
			t.Fatalf("%d %v", n, err)
		}
		if n, err := store.DeleteIdleOAuthGrants(ctx, st.Q(), now.Add(time.Minute)); err != nil || n != 1 {
			t.Fatalf("%d %v", n, err)
		}
	})
}

func TestOAuthClients(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.UnixMilli(1_700_000_000_000)
		mk := func(id, fp string, created time.Time) {
			t.Helper()
			if err := store.InsertOAuthClient(ctx, st.Q(), store.OAuthClient{ID: id, Name: id, RedirectURIs: []string{"https://a.example/cb"}, Fingerprint: fp, Created: created}); err != nil {
				t.Fatal(err)
			}
		}
		mk("pbc_1", "f1", now)
		mk("pbc_2", "f2", now.Add(time.Hour))
		if err := store.InsertOAuthClient(ctx, st.Q(), store.OAuthClient{ID: "pbc_3", Fingerprint: "f1", RedirectURIs: []string{}}); !store.IsUnique(err) {
			t.Fatalf("%v", err)
		}
		c, err := store.FindOAuthClient(ctx, st.Q(), "f2")
		if err != nil || c.ID != "pbc_2" || c.RedirectURIs[0] != "https://a.example/cb" || !c.LastUsed.IsZero() {
			t.Fatalf("%+v %v", c, err)
		}
		if _, err := store.GetOAuthClient(ctx, st.Q(), "x"); err != store.ErrNotFound {
			t.Fatal(err)
		}
		// Never authorized for a day: gone. Authorized, then idle for a month: gone, unless a grant remains.
		if n, err := store.PruneOAuthClients(ctx, st.Q(), now.Add(30*time.Minute), now.Add(-time.Hour)); err != nil || n != 1 {
			t.Fatalf("%d %v", n, err)
		}
		if _, err := store.GetOAuthClient(ctx, st.Q(), "pbc_1"); err != store.ErrNotFound {
			t.Fatal("the old unused client stayed")
		}
		store.TouchOAuthClient(ctx, st.Q(), "pbc_2", now)
		mkUser(t, st, "A", "a", "member")
		if n, _ := store.PruneOAuthClients(ctx, st.Q(), now, now.Add(time.Hour)); n != 1 {
			t.Fatalf("an idle client stayed: %d", n)
		}
		mk("pbc_4", "f4", now)
		store.TouchOAuthClient(ctx, st.Q(), "pbc_4", now)
		g := store.OAuthGrant{ID: "G", UserID: "A", ClientID: "pbc_4", ClientName: "x", Access: "read", Resource: "r", Created: now, LastUsed: now, RefreshHash: "h"}
		st.Tx(ctx, func(q store.Q) error { return store.InsertOAuthGrant(ctx, q, g) })
		if n, _ := store.PruneOAuthClients(ctx, st.Q(), now, now.Add(time.Hour)); n != 0 {
			t.Fatal("a client with a grant went")
		}
		if ok, err := store.DeleteOldestUnusedOAuthClient(ctx, st.Q()); err != nil || ok {
			t.Fatalf("%v %v", ok, err)
		}
		mk("pbc_5", "f5", now)
		if ok, err := store.DeleteOldestUnusedOAuthClient(ctx, st.Q()); err != nil || !ok {
			t.Fatalf("%v %v", ok, err)
		}
		if n, _ := store.CountOAuthClients(ctx, st.Q()); n != 1 {
			t.Fatalf("%d", n)
		}
	})
}
