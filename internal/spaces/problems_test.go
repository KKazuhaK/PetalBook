package spaces

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/space"
	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

func TestProblemsAreReadFromOpenSpacesAndRememberedWhenTheyClose(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		m, clk := limitedManager(t, st, func(o *Options) { o.IdleAfter = 10 * time.Minute; o.MaxSpaces = 0 })
		owner := addUser(t, st, "U1", "owner")
		viewer := addUser(t, st, "U2", "viewer")
		a, err := m.Create(ctx, owner.ID, "Notes")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AddMember(ctx, st.Q(), a.ID, viewer.ID, "viewer", clk.now()); err != nil {
			t.Fatal(err)
		}
		m.MembershipChanged(viewer.ID)
		h, err := m.Open(ctx, owner, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		// A space with nothing wrong says nothing.
		if got, err := m.SpaceProblems(ctx, owner.ID); err != nil || len(got) != 0 {
			t.Fatalf("%v %v", got, err)
		}
		root, _ := m.paths(a.ID)
		if err := os.WriteFile(filepath.Join(root, "broken.md"), []byte("---\ntitle: no id\n---\nbody\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := h.Space.Reindex(); err != nil {
			t.Fatal(err)
		}
		// The count of an open space is reused for a few seconds, then read again.
		clk.add(time.Minute)
		got, err := m.SpaceProblems(ctx, owner.ID)
		if err != nil || len(got) != 1 || got[0].ID != a.ID || got[0].Name != "Notes" || got[0].Total != 1 || got[0].Counts["no_id"] != 1 || got[0].Sig == "" {
			t.Fatalf("%+v %v", got, err)
		}
		sig := got[0].Sig
		// Only someone who can write in the space is told.
		if got, err := m.SpaceProblems(ctx, viewer.ID); err != nil || len(got) != 0 {
			t.Fatalf("a viewer is told: %v %v", got, err)
		}
		// Nothing is opened for this: closed, the space still answers, from memory.
		h.Release()
		clk.add(11 * time.Minute)
		m.Trim()
		if m.OpenCount() != 0 {
			t.Fatal("the space did not close")
		}
		got, _ = m.SpaceProblems(ctx, owner.ID)
		if len(got) != 1 || got[0].Sig != sig {
			t.Fatalf("after closing: %+v", got)
		}
		if m.OpenCount() != 0 {
			t.Fatal("asking for the problems opened a space")
		}
		// Mending it is seen once the space is open again.
		os.Remove(filepath.Join(root, "broken.md"))
		h, err = m.Open(ctx, owner, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer h.Release()
		if _, err := h.Space.Reindex(); err != nil {
			t.Fatal(err)
		}
		clk.add(time.Minute)
		if got, _ := m.SpaceProblems(ctx, owner.ID); len(got) != 0 {
			t.Fatalf("still told: %+v", got)
		}
	})
}

func TestTheSignatureOfProblemsChangesWithThem(t *testing.T) {
	a := problemsOf("S", "S", map[string]int{"no_id": 1, "bad_header": 0})
	b := problemsOf("S", "S", map[string]int{"no_id": 1})
	c := problemsOf("S", "S", map[string]int{"no_id": 2})
	if a == nil || b == nil || a.Sig != b.Sig || a.Sig == c.Sig || a.Total != 1 || len(a.Counts) != 1 {
		t.Fatalf("%+v %+v %+v", a, b, c)
	}
	if problemsOf("S", "S", map[string]int{"no_id": 0}) != nil {
		t.Fatal("no problems is no item")
	}
}

func TestOwnersAreToldWhenARemoteNeedsAttention(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		m, clk := limitedManager(t, st, func(o *Options) { o.MaxSpaces = 0 })
		owner := addUser(t, st, "U1", "owner")
		editor := addUser(t, st, "U2", "editor")
		a, err := m.Create(ctx, owner.ID, "Notes")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AddMember(ctx, st.Q(), a.ID, editor.ID, "editor", clk.now()); err != nil {
			t.Fatal(err)
		}
		report := m.persistStatus(a.ID)
		told := func(u store.User) []store.Notification {
			l, err := store.ListNotifications(ctx, st.Q(), u.ID, u.TenantID, store.NotifyFilter{Kind: store.NotifySync, Limit: 50})
			if err != nil {
				t.Fatal(err)
			}
			return l
		}
		// The first state is not a change; "offline" comes back by itself and is never told.
		report(space.RemoteStatus{State: space.RemoteOK})
		report(space.RemoteStatus{State: space.RemoteOffline})
		report(space.RemoteStatus{State: space.RemoteOK})
		if n := len(told(owner)); n != 0 {
			t.Fatalf("%d notices for a network that was away", n)
		}
		// Broken once, however often it says so again.
		report(space.RemoteStatus{State: space.RemoteError, Kind: space.KindAuth})
		report(space.RemoteStatus{State: space.RemoteError, Kind: space.KindAuth})
		report(space.RemoteStatus{State: space.RemoteRewritten})
		l := told(owner)
		if len(l) != 2 || l[0].Ref != a.ID || l[0].Tenant != owner.TenantID {
			t.Fatalf("%+v", l)
		}
		if l[1].Payload != `{"event":"failed","kind":"auth","space":"Notes","state":"error"}` {
			t.Fatalf("%s", l[1].Payload)
		}
		report(space.RemoteStatus{State: space.RemoteOK})
		l = told(owner)
		if len(l) != 3 || l[0].Payload != `{"event":"ok","kind":"","space":"Notes","state":"ok"}` {
			t.Fatalf("%+v", l)
		}
		// Only the owners.
		if n := len(told(editor)); n != 0 {
			t.Fatalf("an editor was told: %d", n)
		}
	})
}
