package spaces

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/page"
	"github.com/KKazuhaK/PetalBook/internal/perm"
	"github.com/KKazuhaK/PetalBook/internal/space"
	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

// Another owner editing their profile does not change the author of the
// space's default commits: that is the first owner.
func TestOnlyTheFirstOwnersProfileSetsTheDefaultAuthor(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		m := testManager(t, st)
		ann := addUser(t, st, "U1", "ann")
		bo := addUser(t, st, "U2", "bo")
		info, err := m.Create(ctx, ann.ID, "Team")
		if err != nil {
			t.Fatal(err)
		}
		h, err := m.Open(ctx, ann, info.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer h.Release()
		if _, err := m.AddMember(ctx, h, "bo", perm.Owner); err != nil {
			t.Fatal(err)
		}
		bo.Name, bo.Email = "Bo Renamed", "bo@example.test"
		m.UserChanged(ctx, bo)
		if _, err := h.Space.Create(space.Author{}, space.CreateInput{Meta: page.Meta{{Key: "title", Value: "P"}}, Body: "x\n"}); err != nil {
			t.Fatal(err)
		}
		if err := h.Space.Flush(); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("git", "-C", h.Space.Root(), "log", "--format=%an", "-1").Output()
		if err != nil || strings.TrimSpace(string(out)) != "ann" {
			t.Fatalf("the default author is %q: %v", out, err)
		}
	})
}

// A transfer to someone who is added at the same moment neither fails on the
// key nor loses the ownership.
func TestTransferAndAddOfTheSameUserDoNotCollide(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		m := testManager(t, st)
		owner := addUser(t, st, "U1", "owner")
		for i := 0; i < 8; i++ {
			u := addUser(t, st, "V"+string(rune('a'+i)), "user"+string(rune('a'+i)))
			info, err := m.Create(ctx, owner.ID, "S"+u.ID)
			if err != nil {
				t.Fatal(err)
			}
			h, err := m.Open(ctx, owner, info.ID)
			if err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			var addErr, transferErr error
			wg.Add(2)
			go func() { defer wg.Done(); _, addErr = m.AddMember(ctx, h, u.Username, perm.Viewer) }()
			go func() { defer wg.Done(); transferErr = m.Transfer(ctx, store.AllTenants(), info.ID, u.ID) }()
			wg.Wait()
			h.Release()
			if transferErr != nil || (addErr != nil && !errors.Is(addErr, ErrAlreadyMember)) {
				t.Fatalf("transfer: %v; add: %v", transferErr, addErr)
			}
			if role, err := store.MemberRole(ctx, st.Q(), info.ID, u.ID); err != nil || role != perm.Owner && role != perm.Viewer {
				t.Fatalf("%q %v", role, err)
			}
		}
		// Transferring to a member who exists makes them the owner.
		info, _ := m.Create(ctx, owner.ID, "Again")
		u := addUser(t, st, "W1", "walt")
		h, _ := m.Open(ctx, owner, info.ID)
		defer h.Release()
		if _, err := m.AddMember(ctx, h, "walt", perm.Viewer); err != nil {
			t.Fatal(err)
		}
		if err := m.Transfer(ctx, store.AllTenants(), info.ID, u.ID); err != nil {
			t.Fatal(err)
		}
		if role, _ := store.MemberRole(ctx, st.Q(), info.ID, u.ID); role != perm.Owner {
			t.Fatalf("%q", role)
		}
		if role, _ := store.MemberRole(ctx, st.Q(), info.ID, owner.ID); role != perm.Editor {
			t.Fatalf("%q", role)
		}
	})
}

// A space deleted while it is being measured is skipped without a failure, and
// one whose first measurement fails is listed as not measured.
func TestSizerSkipsADeletedSpaceAndReportsAFailedFirstMeasurementAsNone(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		m := testManager(t, st)
		owner := addUser(t, st, "U1", "owner")
		gone, err := m.Create(ctx, owner.ID, "Gone")
		if err != nil {
			t.Fatal(err)
		}
		broken, err := m.Create(ctx, owner.ID, "Broken")
		if err != nil {
			t.Fatal(err)
		}

		// Deleted after Resize passed its first look and before the walk.
		m.sizeSem <- struct{}{}
		done := make(chan error, 1)
		go func() { _, _, err := m.Resize(ctx, store.AllTenants(), gone.ID); done <- err }()
		time.Sleep(100 * time.Millisecond)
		if _, err := m.DeleteSpace(ctx, store.AllTenants(), gone.ID); err != nil {
			t.Fatal(err)
		}
		<-m.sizeSem
		if err := <-done; !errors.Is(err, ErrNotFound) {
			t.Fatalf("a space deleted meanwhile is %v, not ErrNotFound", err)
		}

		// A first measurement that fails: the space is not "measured at some time" with no size.
		root, _ := m.paths(broken.ID)
		if err := os.RemoveAll(root); err != nil {
			t.Fatal(err)
		}
		if !m.measureNext(ctx, time.Hour) {
			t.Fatal("a failing space stopped the round")
		}
		rows, _, err := store.ListAllSpaces(ctx, st.Q(), store.AllTenants(), store.SpaceFilter{})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r.ID == broken.ID && (r.SizeBytes != nil || r.SizeAt != nil) {
				t.Fatalf("a failed measurement is listed as %v at %v", r.SizeBytes, r.SizeAt)
			}
		}
	})
}

// The name search ignores case beyond ASCII, the same on both databases: the
// names are folded by Go (name_key), not by the database's own idea of case.
func TestSpaceSearchIgnoresCaseBeyondASCII(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		m := testManager(t, st)
		owner := addUser(t, st, "U1", "owner")
		names := []string{"Éclair", "Plain", "唐诗 Tang", "Straße", "İstanbul", "Hello World"}
		for _, n := range names {
			if _, err := m.Create(ctx, owner.ID, n); err != nil {
				t.Fatal(err)
			}
		}
		for _, c := range []struct {
			query string
			want  []string
		}{
			{"éCLAIR", []string{"Éclair"}},
			{"ÉCLAIR", []string{"Éclair"}},
			{"唐诗", []string{"唐诗 Tang"}},
			{"tang", []string{"唐诗 Tang"}},
			{"唐诗 TANG", []string{"唐诗 Tang"}},
			{"STRASSE", []string{"Straße"}},
			{"straße", []string{"Straße"}},
			{"STRAẞE", []string{"Straße"}}, // the capital sharp s
			{"İstan", []string{"İstanbul"}},
			{"i", []string{"Éclair", "İstanbul", "Plain"}},
			{"world", []string{"Hello World"}},
			{"nothing like it", nil},
			{"%", nil}, // a wildcard is a character
		} {
			rows, _, err := store.ListAllSpaces(ctx, st.Q(), store.AllTenants(), store.SpaceFilter{Query: c.query})
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]bool{}
			for _, r := range rows {
				got[r.Name] = true
			}
			if len(got) != len(c.want) {
				t.Fatalf("%q found %v, want %v", c.query, got, c.want)
			}
			for _, w := range c.want {
				if !got[w] {
					t.Fatalf("%q found %v, want %v", c.query, got, c.want)
				}
			}
		}
		// A rename is searched under its new name only.
		rows, _, _ := store.ListAllSpaces(ctx, st.Q(), store.AllTenants(), store.SpaceFilter{Query: "plain"})
		if len(rows) != 1 {
			t.Fatalf("%v", rows)
		}
		if err := store.RenameSpace(ctx, st.Q(), rows[0].ID, "Größe"); err != nil {
			t.Fatal(err)
		}
		for q, n := range map[string]int{"plain": 0, "GRÖSSE": 1, "größe": 1} {
			rows, _, _ := store.ListAllSpaces(ctx, st.Q(), store.AllTenants(), store.SpaceFilter{Query: q})
			if len(rows) != n {
				t.Fatalf("%q after the rename: %d spaces, want %d", q, len(rows), n)
			}
		}
	})
}
