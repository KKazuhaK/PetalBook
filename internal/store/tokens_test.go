package store_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

func mkToken(t *testing.T, st *store.Store, id, user, hash string, created time.Time, spaces ...string) store.APIToken {
	t.Helper()
	tok := store.APIToken{ID: id, UserID: user, Name: "token " + id, Prefix: "pbk_" + id, Hash: hash, Access: "write", Created: created}
	for _, s := range spaces {
		tok.Spaces = append(tok.Spaces, store.TokenSpace{ID: s})
	}
	if err := st.Tx(ctx, func(q store.Q) error { return store.InsertAPIToken(ctx, q, tok) }); err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestAPITokens(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "A", "a", "member")
		mkUser(t, st, "B", "b", "member")
		mkSpace(t, st, "S1", "one")
		mkSpace(t, st, "S2", "two")
		now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
		// A belongs to S1 only: S2 is named by the token but is not A's any more.
		if err := store.AddMember(ctx, st.Q(), "S1", "A", "owner", now); err != nil {
			t.Fatal(err)
		}
		mkToken(t, st, "T1", "A", "h1", now, "S1", "S2", "GONE")
		mkToken(t, st, "T2", "A", "h2", now.Add(time.Hour), "S1")
		mkToken(t, st, "T3", "B", "h3", now.Add(2*time.Hour), "S1")

		t.Run("by hash, one query, with the spaces", func(t *testing.T) {
			got, err := store.GetAPITokenByHash(ctx, st.Q(), "h1")
			if err != nil || got.ID != "T1" || got.UserID != "A" || got.Access != "write" || len(got.Spaces) != 3 || !got.Revoked.IsZero() || !got.Expires.IsZero() || !got.LastUsed.IsZero() {
				t.Fatalf("%+v %v", got, err)
			}
			if _, err := store.GetAPITokenByHash(ctx, st.Q(), "nope"); !errors.Is(err, store.ErrNotFound) {
				t.Fatal(err)
			}
		})

		t.Run("the hash is unique", func(t *testing.T) {
			err := st.Tx(ctx, func(q store.Q) error {
				return store.InsertAPIToken(ctx, q, store.APIToken{ID: "T9", UserID: "A", Name: "x", Prefix: "p", Hash: "h1", Access: "read", Created: now})
			})
			if !store.IsUnique(err) {
				t.Fatalf("%v", err)
			}
		})

		t.Run("listing: newest first, names only while the owner is a member", func(t *testing.T) {
			rows, err := store.ListAPITokens(ctx, st.Q(), store.AllTenants(), store.TokenFilter{UserID: "A"})
			if err != nil || len(rows) != 2 || rows[0].ID != "T2" || rows[1].ID != "T1" {
				t.Fatalf("%+v %v", rows, err)
			}
			if rows[0].Username != "a" {
				t.Fatalf("%+v", rows[0])
			}
			names := map[string]string{}
			for _, s := range rows[1].Spaces {
				names[s.ID] = s.Name
			}
			if len(names) != 3 || names["S1"] != "one" || names["S2"] != "" || names["GONE"] != "" {
				t.Fatalf("%v", names)
			}
			all, err := store.ListAPITokens(ctx, st.Q(), store.AllTenants(), store.TokenFilter{})
			if err != nil || len(all) != 3 || all[0].ID != "T3" {
				t.Fatalf("%+v %v", all, err)
			}
		})

		t.Run("paging by a cursor", func(t *testing.T) {
			for i := 0; i < 5; i++ { // same second: the id breaks the tie
				mkToken(t, st, fmt.Sprintf("P%d", i), "A", fmt.Sprintf("hp%d", i), now.Add(24*time.Hour), "S1")
			}
			var seen []string
			f := store.TokenFilter{UserID: "A", Limit: 3}
			for pages := 0; pages < 10; pages++ {
				rows, err := store.ListAPITokens(ctx, st.Q(), store.AllTenants(), f)
				if err != nil {
					t.Fatal(err)
				}
				for _, r := range rows {
					seen = append(seen, r.ID)
				}
				if len(rows) < 3 {
					break
				}
				last := rows[len(rows)-1]
				f.AfterCreated, f.AfterID = last.Created, last.ID
			}
			want := []string{"P4", "P3", "P2", "P1", "P0", "T2", "T1"}
			if fmt.Sprint(seen) != fmt.Sprint(want) {
				t.Fatalf("%v", seen)
			}
		})

		t.Run("filters by id and prefix", func(t *testing.T) {
			rows, err := store.ListAPITokens(ctx, st.Q(), store.AllTenants(), store.TokenFilter{ID: "T3"})
			if err != nil || len(rows) != 1 || rows[0].UserID != "B" {
				t.Fatalf("%+v %v", rows, err)
			}
			rows, err = store.ListAPITokens(ctx, st.Q(), store.AllTenants(), store.TokenFilter{Prefix: "pbk_T1"})
			if err != nil || len(rows) != 1 || rows[0].ID != "T1" {
				t.Fatalf("%+v %v", rows, err)
			}
		})

		t.Run("revoking keeps the first time and reports a missing token", func(t *testing.T) {
			ok, err := store.RevokeAPIToken(ctx, st.Q(), "T3", now.Add(time.Minute))
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			if ok, err = store.RevokeAPIToken(ctx, st.Q(), "T3", now.Add(time.Hour)); err != nil || !ok {
				t.Fatal(ok, err)
			}
			got, _ := store.GetAPITokenByHash(ctx, st.Q(), "h3")
			if !got.Revoked.Equal(now.Add(time.Minute)) {
				t.Fatalf("%v", got.Revoked)
			}
			if ok, err = store.RevokeAPIToken(ctx, st.Q(), "nope", now); err != nil || ok {
				t.Fatal(ok, err)
			}
		})

		t.Run("live tokens are those not revoked and not expired", func(t *testing.T) {
			if err := st.Tx(ctx, func(q store.Q) error {
				return store.InsertAPIToken(ctx, q, store.APIToken{ID: "E1", UserID: "B", Name: "x", Prefix: "p", Hash: "he", Access: "read", Created: now, Expires: now.Add(time.Hour), Spaces: []store.TokenSpace{{ID: "S1"}}})
			}); err != nil {
				t.Fatal(err)
			}
			n, err := store.CountLiveAPITokens(ctx, st.Q(), "B", now)
			if err != nil || n != 1 { // T3 is revoked
				t.Fatal(n, err)
			}
			if n, _ = store.CountLiveAPITokens(ctx, st.Q(), "B", now.Add(2*time.Hour)); n != 0 {
				t.Fatal(n)
			}
		})

		t.Run("use is recorded", func(t *testing.T) {
			if err := store.TouchAPIToken(ctx, st.Q(), "T1", now.Add(3*time.Hour)); err != nil {
				t.Fatal(err)
			}
			got, _ := store.GetAPITokenByHash(ctx, st.Q(), "h1")
			if !got.LastUsed.Equal(now.Add(3 * time.Hour)) {
				t.Fatalf("%v", got.LastUsed)
			}
		})

		t.Run("old ended tokens go, with their spaces", func(t *testing.T) {
			n, err := store.DeleteOldAPITokens(ctx, st.Q(), now.Add(30*time.Minute)) // T3 revoked at +1m, E1 expires +1h
			if err != nil || n != 1 {
				t.Fatal(n, err)
			}
			if _, err := store.GetAPITokenByHash(ctx, st.Q(), "h3"); !errors.Is(err, store.ErrNotFound) {
				t.Fatal(err)
			}
			var left int
			if err := st.Q().QueryRow(ctx, `SELECT COUNT(*) FROM api_token_spaces WHERE token_id = 'T3'`).Scan(&left); err != nil || left != 0 {
				t.Fatal(left, err)
			}
		})

		t.Run("a token goes with its owner", func(t *testing.T) {
			if _, err := st.Q().Exec(ctx, `DELETE FROM users WHERE id = 'A'`); err != nil {
				t.Fatal(err)
			}
			rows, _ := store.ListAPITokens(ctx, st.Q(), store.AllTenants(), store.TokenFilter{UserID: "A"})
			if len(rows) != 0 {
				t.Fatalf("%+v", rows)
			}
		})
	})
}
