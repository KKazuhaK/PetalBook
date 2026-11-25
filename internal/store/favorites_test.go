package store_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

func mkSpace(t *testing.T, st *store.Store, id, name string) {
	t.Helper()
	if err := store.InsertSpace(ctx, st.Q(), store.DefaultTenantID, id, name, "", "", time.Now()); err != nil {
		t.Fatal(err)
	}
}

func favs(t *testing.T, st *store.Store, user, space string) []string {
	t.Helper()
	l, err := store.ListFavorites(ctx, st.Q(), user, space)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(l))
	for i, f := range l {
		out[i] = f.Kind + ":" + f.Target
	}
	return out
}

func same(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestFavorites(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "A", "a", "member")
		mkUser(t, st, "B", "b", "member")
		mkSpace(t, st, "S1", "one")
		mkSpace(t, st, "S2", "two")
		now := time.Now()
		add := func(user, space, kind, target string) bool {
			t.Helper()
			var added bool
			err := st.Tx(ctx, func(q store.Q) error {
				var err error
				added, err = store.AddFavorite(ctx, q, user, space, store.Favorite{Kind: kind, Target: target}, now)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			return added
		}
		if !add("A", "S1", "page", "P1") || !add("A", "S1", "folder", "Tang") || !add("A", "S1", "view", "folder:Tang#k3f9") {
			t.Fatal("not added")
		}
		// Adding again changes nothing, not even the place.
		if add("A", "S1", "page", "P1") {
			t.Fatal("a second add says it added")
		}
		add("A", "S2", "page", "P1")
		add("B", "S1", "page", "P9")
		if got := favs(t, st, "A", "S1"); !same(got, []string{"page:P1", "folder:Tang", "view:folder:Tang#k3f9"}) {
			t.Fatalf("%v", got)
		}
		// Order: the ones named, then the rest as they were; unknown names are ignored.
		order := func(items ...store.Favorite) {
			t.Helper()
			if err := st.Tx(ctx, func(q store.Q) error { return store.OrderFavorites(ctx, q, "A", "S1", items) }); err != nil {
				t.Fatal(err)
			}
		}
		order(store.Favorite{Kind: "view", Target: "folder:Tang#k3f9"}, store.Favorite{Kind: "page", Target: "nope"}, store.Favorite{Kind: "page", Target: "P1"})
		if got := favs(t, st, "A", "S1"); !same(got, []string{"view:folder:Tang#k3f9", "page:P1", "folder:Tang"}) {
			t.Fatalf("%v", got)
		}
		// One that is added later goes last.
		add("A", "S1", "page", "P2")
		if got := favs(t, st, "A", "S1"); got[len(got)-1] != "page:P2" {
			t.Fatalf("%v", got)
		}
		// Removing: reports whether there was one; other people's and spaces' stay.
		if ok, err := store.RemoveFavorite(ctx, st.Q(), "A", "S1", "page", "P1"); err != nil || !ok {
			t.Fatalf("%v %v", ok, err)
		}
		if ok, err := store.RemoveFavorite(ctx, st.Q(), "A", "S1", "page", "P1"); err != nil || ok {
			t.Fatalf("%v %v", ok, err)
		}
		if len(favs(t, st, "A", "S2")) != 1 || len(favs(t, st, "B", "S1")) != 1 {
			t.Fatal("another list changed")
		}
		// Deleting the space deletes its favourites, deleting the user theirs.
		if err := st.Tx(ctx, func(q store.Q) error { _, err := store.DeleteSpace(ctx, q, store.AllTenants(), "S2"); return err }); err != nil {
			t.Fatal(err)
		}
		if len(favs(t, st, "A", "S2")) != 0 {
			t.Fatal("favourites of a deleted space are still there")
		}
		if _, err := st.Q().Exec(ctx, `DELETE FROM users WHERE id = ?`, "B"); err != nil {
			t.Fatal(err)
		}
		if len(favs(t, st, "B", "S1")) != 0 {
			t.Fatal("favourites of a deleted user are still there")
		}
		var n int
		st.Q().QueryRow(ctx, `SELECT COUNT(*) FROM favorites WHERE user_id = 'B' OR space_id = 'S2'`).Scan(&n)
		if n != 0 {
			t.Fatalf("%d rows left", n)
		}
	})
}

func TestFavoritesLimit(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "A", "a", "member")
		mkSpace(t, st, "S1", "one")
		for i := 0; i < store.MaxFavorites; i++ {
			err := st.Tx(ctx, func(q store.Q) error {
				_, err := store.AddFavorite(ctx, q, "A", "S1", store.Favorite{Kind: "page", Target: fmt.Sprintf("P%03d", i)}, time.Now())
				return err
			})
			if err != nil {
				t.Fatal(i, err)
			}
		}
		err := st.Tx(ctx, func(q store.Q) error {
			_, err := store.AddFavorite(ctx, q, "A", "S1", store.Favorite{Kind: "page", Target: "one more"}, time.Now())
			return err
		})
		if !errors.Is(err, store.ErrFavoritesFull) {
			t.Fatalf("%v", err)
		}
		// One that is there is not "one more".
		err = st.Tx(ctx, func(q store.Q) error {
			added, err := store.AddFavorite(ctx, q, "A", "S1", store.Favorite{Kind: "page", Target: "P000"}, time.Now())
			if added {
				t.Fatal("added twice")
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		// The limit is per person and space.
		mkUser(t, st, "B", "b", "member")
		mkSpace(t, st, "S2", "two")
		for _, who := range [][2]string{{"B", "S1"}, {"A", "S2"}} {
			if err := st.Tx(ctx, func(q store.Q) error {
				_, err := store.AddFavorite(ctx, q, who[0], who[1], store.Favorite{Kind: "page", Target: "P"}, time.Now())
				return err
			}); err != nil {
				t.Fatal(who, err)
			}
		}
	})
}

func TestMoveFolderFavorites(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		mkUser(t, st, "A", "a", "member")
		mkUser(t, st, "B", "b", "member")
		mkSpace(t, st, "S1", "one")
		mkSpace(t, st, "S2", "two")
		put := func(user, space, kind, target string) {
			t.Helper()
			if err := st.Tx(ctx, func(q store.Q) error {
				_, err := store.AddFavorite(ctx, q, user, space, store.Favorite{Kind: kind, Target: target}, time.Now())
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}
		put("A", "S1", "folder", "Tang")
		put("A", "S1", "folder", "Tang/Poems")
		put("A", "S1", "folder", "Tangent") // not below Tang
		put("A", "S1", "view", "folder:Tang/Poems#v1")
		put("A", "S1", "view", "tag:Tang#v1") // a tag, not a folder
		put("A", "S1", "page", "Tang")        // a page id, whatever it looks like
		put("A", "S1", "folder", "New/Stale") // made after the move (or stale): kept
		put("B", "S1", "folder", "New")       // B already has the new place: kept, and the old one goes
		put("B", "S1", "folder", "Tang")
		put("A", "S2", "folder", "Tang") // another space
		if err := st.Tx(ctx, func(q store.Q) error { return store.MoveFolderFavorites(ctx, q, "S1", "Tang", "New") }); err != nil {
			t.Fatal(err)
		}
		want := []string{"folder:New", "folder:New/Poems", "folder:Tangent", "view:folder:New/Poems#v1", "view:tag:Tang#v1", "page:Tang", "folder:New/Stale"}
		if got := favs(t, st, "A", "S1"); !same(got, want) {
			t.Fatalf("%v", got)
		}
		if got := favs(t, st, "B", "S1"); !same(got, []string{"folder:New"}) {
			t.Fatalf("everybody's follow: %v", got)
		}
		if got := favs(t, st, "A", "S2"); !same(got, []string{"folder:Tang"}) {
			t.Fatalf("another space: %v", got)
		}
		// Done again (a recovery), it changes nothing.
		if err := st.Tx(ctx, func(q store.Q) error { return store.MoveFolderFavorites(ctx, q, "S1", "Tang", "New") }); err != nil {
			t.Fatal(err)
		}
		if got := favs(t, st, "A", "S1"); !same(got, want) {
			t.Fatalf("again: %v", got)
		}
		// Moves are recorded until they are done.
		if err := store.RecordFolderMove(ctx, st.Q(), "S1", "A", "B", time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := store.RecordFolderMove(ctx, st.Q(), "S1", "A", "B", time.Now()); err != nil {
			t.Fatal("twice:", err)
		}
		store.RecordFolderMove(ctx, st.Q(), "S2", "C", "D", time.Now())
		if l, err := store.PendingFolderMoves(ctx, st.Q(), "S1"); err != nil || len(l) != 1 || l[0].From != "A" || l[0].To != "B" || l[0].Created.IsZero() {
			t.Fatalf("%v %v", l, err)
		}
		if err := store.DoneFolderMove(ctx, st.Q(), "S1", "A", "B"); err != nil {
			t.Fatal(err)
		}
		if l, _ := store.PendingFolderMoves(ctx, st.Q(), "S1"); len(l) != 0 {
			t.Fatalf("%v", l)
		}
		if l, _ := store.PendingFolderMoves(ctx, st.Q(), "S2"); len(l) != 1 {
			t.Fatalf("%v", l)
		}
	})
}
