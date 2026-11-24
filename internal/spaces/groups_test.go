package spaces

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/perm"
	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

// gfix is a manager with a few people, a shared space and groups. Spaces are
// made as rows only: what is tested is who may reach them, which is decided
// from the cached lists and never opens a repository.
type gfix struct {
	t  *testing.T
	st *store.Store
	m  *Manager
}

func newGfix(t *testing.T, st *store.Store) *gfix {
	t.Helper()
	return &gfix{t: t, st: st, m: testManager(t, st)}
}

func (f *gfix) user(id string) store.User {
	f.t.Helper()
	u := store.User{ID: id, Username: id, Name: id, Role: "member", Created: time.Now()}
	if err := store.InsertUser(ctx, f.st.Q(), u); err != nil {
		f.t.Fatal(err)
	}
	return u
}

func (f *gfix) space(id, name, owner string) {
	f.t.Helper()
	if err := store.InsertSpace(ctx, f.st.Q(), store.DefaultTenantID, id, name, "", "", time.Now()); err != nil {
		f.t.Fatal(err)
	}
	if owner != "" {
		if err := store.AddMember(ctx, f.st.Q(), id, owner, perm.Owner, time.Now()); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *gfix) group(name, source string) string {
	f.t.Helper()
	g, err := f.m.CreateGroup(ctx, store.DefaultTenantID, name, "", source)
	if err != nil {
		f.t.Fatal(err)
	}
	return g.ID
}

func (f *gfix) in(group string, users ...string) {
	f.t.Helper()
	if _, _, _, err := f.m.AddGroupPeople(ctx, store.AllTenants(), group, users); err != nil {
		f.t.Fatal(err)
	}
}

// handle is the owner's view of a space, for the calls that need a handle.
func (f *gfix) handle(owner store.User, space string) *Handle {
	f.t.Helper()
	h, err := f.m.OpenMembership(ctx, owner, space)
	if err != nil {
		f.t.Fatal(err)
	}
	return h
}

func (f *gfix) info(user, space string) (Info, bool) {
	f.t.Helper()
	in, ok, err := f.m.infoOf(ctx, user, space)
	if err != nil {
		f.t.Fatal(err)
	}
	return in, ok
}

func cachedUser(m *Manager, id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cache.users[id] != nil
}

func TestEffectivePermissionIsTheUnionOfDirectAndGroupRoles(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newGfix(t, st)
		boss := f.user("boss")
		f.user("ann")
		f.user("bob")
		f.user("cy")
		f.space("S1", "Alpha", "boss")
		h := f.handle(boss, "S1")
		eng, ops := f.group("Eng", "local"), f.group("Ops", "local")
		f.in(eng, "ann", "bob")
		f.in(ops, "bob", "cy")
		if err := store.AddMember(ctx, st.Q(), "S1", "bob", perm.Viewer, time.Now()); err != nil {
			t.Fatal(err)
		}
		// Nothing yet: groups that are not members give nothing.
		if _, ok := f.info("ann", "S1"); ok {
			t.Fatal("a group that is not a member gives access")
		}
		if _, err := f.m.AddSpaceGroup(ctx, h, "boss", eng, perm.Editor); err != nil {
			t.Fatal(err)
		}
		if _, err := f.m.AddSpaceGroup(ctx, h, "boss", ops, perm.Viewer); err != nil {
			t.Fatal(err)
		}
		ann, _ := f.info("ann", "S1")
		if ann.Role != perm.Editor || ann.DirectRole != "" || !ann.ViaGroups || !ann.Perms.Has(perm.Write) || ann.Perms.Has(perm.Members) {
			t.Fatalf("ann %+v", ann)
		}
		// Direct viewer, editor through Eng, viewer through Ops: the highest.
		bob, _ := f.info("bob", "S1")
		if bob.Role != perm.Editor || bob.DirectRole != perm.Viewer || !bob.ViaGroups {
			t.Fatalf("bob %+v", bob)
		}
		cy, _ := f.info("cy", "S1")
		if cy.Role != perm.Viewer || cy.Perms.Has(perm.Write) {
			t.Fatalf("cy %+v", cy)
		}
		// The role of the group changes: applies at the next request.
		if _, old, err := f.m.SetSpaceGroupRole(ctx, h, "boss", eng, perm.Owner); err != nil || old != perm.Editor {
			t.Fatal(old, err)
		}
		if ann, _ = f.info("ann", "S1"); ann.Role != perm.Owner || !ann.Perms.Has(perm.Manage) {
			t.Fatalf("ann %+v", ann)
		}
		// The list has it once, with the effective role.
		l, err := f.m.List(ctx, "bob")
		if err != nil || len(l) != 1 || l[0].Role != perm.Owner {
			t.Fatalf("%+v %v", l, err)
		}
		if _, err := f.m.RemoveSpaceGroup(ctx, h, eng); err != nil {
			t.Fatal(err)
		}
		if _, ok := f.info("ann", "S1"); ok {
			t.Fatal("ann keeps the space after the group left it")
		}
		if bob, _ = f.info("bob", "S1"); bob.Role != perm.Viewer {
			t.Fatalf("bob %+v", bob)
		}
		// Leaving the group takes the access away too.
		if err := f.m.RemoveGroupPerson(ctx, store.AllTenants(), ops, "cy"); err != nil {
			t.Fatal(err)
		}
		if _, ok := f.info("cy", "S1"); ok {
			t.Fatal("cy keeps the space after leaving the group")
		}
		// MayWrite (the drafts sweeper) sees the same permission.
		if ok, _ := f.m.MayWrite(ctx, "bob", "S1"); ok {
			t.Fatal("a viewer may write")
		}
	})
}

func TestAtLeastOneOwnerCountsDirectOwnersOnly(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newGfix(t, st)
		boss := f.user("boss")
		f.user("ann")
		f.space("S1", "Alpha", "boss")
		h := f.handle(boss, "S1")
		g := f.group("Owners", "local")
		f.in(g, "ann")
		if _, err := f.m.AddSpaceGroup(ctx, h, "boss", g, perm.Owner); err != nil {
			t.Fatal(err)
		}
		// A group of owners does not make the last direct owner dispensable.
		if err := f.m.RemoveMember(ctx, boss, h, "boss"); !errors.Is(err, ErrLastOwner) {
			t.Fatalf("leaving: %v", err)
		}
		if _, err := f.m.SetMemberRole(ctx, h, "boss", perm.Editor); !errors.Is(err, ErrLastOwner) {
			t.Fatalf("demoting: %v", err)
		}
	})
}

func TestLeavingASpaceReachedThroughAGroup(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newGfix(t, st)
		boss := f.user("boss")
		ann := f.user("ann")
		bob := f.user("bob")
		f.space("S1", "Alpha", "boss")
		h := f.handle(boss, "S1")
		g := f.group("Eng", "local")
		f.in(g, "ann", "bob")
		if _, err := f.m.AddSpaceGroup(ctx, h, "boss", g, perm.Editor); err != nil {
			t.Fatal(err)
		}
		if err := store.AddMember(ctx, st.Q(), "S1", "bob", perm.Viewer, time.Now()); err != nil {
			t.Fatal(err)
		}
		f.m.MembershipChanged("bob")
		var via *ViaGroupError
		if err := f.m.RemoveMember(ctx, ann, f.handle(ann, "S1"), "ann"); !errors.As(err, &via) || len(via.Groups) != 1 || via.Groups[0] != "Eng" {
			t.Fatalf("%v", err)
		}
		// Direct and in a group: leaving ends the direct membership, the group's role stays.
		if err := f.m.RemoveMember(ctx, bob, f.handle(bob, "S1"), "bob"); err != nil {
			t.Fatal(err)
		}
		in, ok := f.info("bob", "S1")
		if !ok || in.DirectRole != "" || in.Role != perm.Editor {
			t.Fatalf("%+v %v", in, ok)
		}
		// Someone who is nowhere near it is simply not a member.
		f.user("cy")
		if err := f.m.RemoveMember(ctx, store.User{ID: "cy", Role: "admin"}, &Handle{Info: Info{ID: "S1", Perms: perm.All, ViaAdmin: true}}, "cy"); !errors.Is(err, ErrNotMember) {
			t.Fatalf("%v", err)
		}
	})
}

func TestAPersonalSpaceHasNoGroups(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newGfix(t, st)
		boss := f.user("boss")
		if err := store.InsertSpace(ctx, st.Q(), store.DefaultTenantID, "P1", "Mine", "boss", "", time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := store.AddMember(ctx, st.Q(), "P1", "boss", perm.Owner, time.Now()); err != nil {
			t.Fatal(err)
		}
		g := f.group("Eng", "local")
		if _, err := f.m.AddSpaceGroup(ctx, f.handle(boss, "P1"), "boss", g, perm.Viewer); !errors.Is(err, ErrPersonal) {
			t.Fatalf("%v", err)
		}
	})
}

func TestSpaceGroupRefusals(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newGfix(t, st)
		boss := f.user("boss")
		viewer := f.user("vee")
		f.space("S1", "Alpha", "boss")
		if err := store.AddMember(ctx, st.Q(), "S1", "vee", perm.Viewer, time.Now()); err != nil {
			t.Fatal(err)
		}
		h := f.handle(boss, "S1")
		g := f.group("Eng", "local")
		if _, err := f.m.AddSpaceGroup(ctx, h, "boss", g, "chief"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("role: %v", err)
		}
		if _, err := f.m.AddSpaceGroup(ctx, h, "boss", "0000000000", perm.Viewer); !errors.Is(err, ErrGroupNotFound) {
			t.Fatalf("unknown group: %v", err)
		}
		if _, err := f.m.AddSpaceGroup(ctx, f.handle(viewer, "S1"), "vee", g, perm.Viewer); !errors.Is(err, ErrForbidden) {
			t.Fatalf("needs members: %v", err)
		}
		if _, err := f.m.AddSpaceGroup(ctx, h, "boss", g, perm.Viewer); err != nil {
			t.Fatal(err)
		}
		if _, err := f.m.AddSpaceGroup(ctx, h, "boss", g, perm.Viewer); !errors.Is(err, ErrGroupMember) {
			t.Fatalf("twice: %v", err)
		}
		if _, _, err := f.m.SetSpaceGroupRole(ctx, h, "boss", "0000000000", perm.Viewer); !errors.Is(err, ErrGroupNotMember) {
			t.Fatalf("patch: %v", err)
		}
		if _, err := f.m.RemoveSpaceGroup(ctx, h, "0000000000"); !errors.Is(err, ErrGroupNotMember) {
			t.Fatalf("delete: %v", err)
		}
	})
}

func TestGroupLimits(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newGfix(t, st)
		boss := f.user("boss")
		f.space("S1", "Alpha", "boss")
		h := f.handle(boss, "S1")
		// 200 groups in a space, the 201st is refused.
		for i := 0; i < MaxSpaceGroups; i++ {
			g := f.group(fmt.Sprintf("G%03d", i), "local")
			if _, err := f.m.AddSpaceGroup(ctx, h, "boss", g, perm.Viewer); err != nil {
				t.Fatal(i, err)
			}
		}
		g := f.group("One too many", "local")
		if _, err := f.m.AddSpaceGroup(ctx, h, "boss", g, perm.Viewer); !errors.Is(err, ErrGroupLimit) {
			t.Fatalf("%v", err)
		}
		// 500 people in a request, not 501; all or nothing for the unknown.
		var ids []string
		for i := 0; i < MaxAddPeople+1; i++ {
			ids = append(ids, fmt.Sprintf("u%04d", i))
		}
		if _, _, _, err := f.m.AddGroupPeople(ctx, store.AllTenants(), g, ids); !errors.Is(err, ErrInvalid) {
			t.Fatalf("501: %v", err)
		}
		if _, _, _, err := f.m.AddGroupPeople(ctx, store.AllTenants(), g, nil); !errors.Is(err, ErrInvalid) {
			t.Fatalf("none: %v", err)
		}
		f.user("real")
		var miss *MissingUsersError
		if _, _, _, err := f.m.AddGroupPeople(ctx, store.AllTenants(), g, []string{"real", "ghost", "ghost2"}); !errors.As(err, &miss) || len(miss.Missing) != 2 {
			t.Fatalf("missing: %v", err)
		}
		if n, _ := store.CountGroupPeople(ctx, st.Q(), g); n != 0 {
			t.Fatalf("a refused request added %d people", n)
		}
		added, already, _, err := f.m.AddGroupPeople(ctx, store.AllTenants(), g, []string{"real", "real"})
		if err != nil || added != 1 || already != 0 {
			t.Fatal(added, already, err)
		}
		if added, already, _, err = f.m.AddGroupPeople(ctx, store.AllTenants(), g, []string{"real"}); err != nil || added != 0 || already != 1 {
			t.Fatal(added, already, err)
		}
		// A person in 1000 groups cannot join a 1001st; a synced sync is cut there too.
		u := f.user("many")
		now := time.Now()
		for i := 0; i < MaxUserGroups; i++ {
			gid := fmt.Sprintf("X%09d", i)
			if _, err := st.Q().Exec(ctx, `INSERT INTO groups(id, name, name_key, description, source, created, updated) VALUES (?, ?, ?, '', 'local', 0, 0)`, gid, gid, gid); err != nil {
				t.Fatal(err)
			}
			if err := store.AddLocalMembers(ctx, st.Q(), gid, []string{u.ID}, now); err != nil {
				t.Fatal(err)
			}
		}
		if _, _, _, err := f.m.AddGroupPeople(ctx, store.AllTenants(), g, []string{"many"}); !errors.Is(err, ErrGroupLimit) {
			t.Fatalf("1001st: %v", err)
		}
		sg := f.group("Synced", "synced")
		f.provider("co")
		res, err := f.m.SyncProviderGroups(ctx, "many", "co", 0, []store.SyncedMembership{{GroupID: sg, Value: "x"}})
		if err != nil || !res.Limited || res.Changed() {
			t.Fatalf("%+v %v", res, err)
		}
	})
}

func TestEditingGroupsByHandRules(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newGfix(t, st)
		f.user("ann")
		syn := f.group("From IdP", "synced")
		loc := f.group("By hand", "local")
		if _, _, _, err := f.m.AddGroupPeople(ctx, store.AllTenants(), syn, []string{"ann"}); !errors.Is(err, ErrGroupSynced) {
			t.Fatalf("%v", err)
		}
		if err := f.m.RemoveGroupPerson(ctx, store.AllTenants(), syn, "ann"); !errors.Is(err, ErrGroupSynced) {
			t.Fatalf("%v", err)
		}
		if err := f.m.RemoveGroupPerson(ctx, store.AllTenants(), loc, "ann"); !errors.Is(err, ErrNotInGroup) {
			t.Fatalf("%v", err)
		}
		if err := f.m.RemoveGroupPerson(ctx, store.AllTenants(), "0000000000", "ann"); !errors.Is(err, ErrGroupNotFound) {
			t.Fatalf("%v", err)
		}
		if _, err := f.m.CreateGroup(ctx, store.DefaultTenantID, " by HAND ", "", "local"); !errors.Is(err, ErrGroupExists) {
			t.Fatalf("a name is unique ignoring case: %v", err)
		}
		if _, err := f.m.CreateGroup(ctx, store.DefaultTenantID, "Straße", "", "local"); err != nil {
			t.Fatal(err)
		}
		if _, err := f.m.CreateGroup(ctx, store.DefaultTenantID, "STRASSE", "", "local"); !errors.Is(err, ErrGroupExists) {
			t.Fatalf("case folding: %v", err)
		}
		for _, bad := range []string{"", "   ", string(make([]rune, 81)), "tab\there"} {
			if _, err := f.m.CreateGroup(ctx, store.DefaultTenantID, bad, "", "local"); !errors.Is(err, ErrInvalid) {
				t.Fatalf("%q: %v", bad, err)
			}
		}
		if _, err := f.m.CreateGroup(ctx, store.DefaultTenantID, "x", "", "other"); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
		if _, _, err := f.m.UpdateGroup(ctx, store.AllTenants(), loc, strp("From IdP"), nil); !errors.Is(err, ErrGroupExists) {
			t.Fatalf("rename onto another: %v", err)
		}
		g, changed, err := f.m.UpdateGroup(ctx, store.AllTenants(), loc, strp("Renamed"), strp("d"))
		if err != nil || g.Name != "Renamed" || len(changed) != 2 {
			t.Fatal(g, changed, err)
		}
	})
}

func strp(s string) *string { return &s }

// A change drops exactly the people it touches and nobody else.
func TestChangesDropOnlyTheAffectedPeople(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newGfix(t, st)
		boss := f.user("boss")
		for _, u := range []string{"ann", "bob", "out"} {
			f.user(u)
		}
		f.space("S1", "Alpha", "boss")
		h := f.handle(boss, "S1")
		g := f.group("Eng", "local")
		f.in(g, "ann", "bob")
		warm := func() {
			for _, u := range []string{"boss", "ann", "bob", "out"} {
				if _, err := f.m.List(ctx, u); err != nil {
					t.Fatal(err)
				}
			}
		}
		check := func(what string, dropped ...string) {
			t.Helper()
			for _, u := range []string{"boss", "ann", "bob", "out"} {
				want := true
				for _, d := range dropped {
					want = want && d != u
				}
				if cachedUser(f.m, u) != want {
					t.Errorf("%s: %s cached = %v, want %v", what, u, !want, want)
				}
			}
		}
		warm()
		if _, err := f.m.AddSpaceGroup(ctx, h, "boss", g, perm.Viewer); err != nil {
			t.Fatal(err)
		}
		check("group added to a space", "ann", "bob")
		warm()
		if _, _, err := f.m.SetSpaceGroupRole(ctx, h, "boss", g, perm.Editor); err != nil {
			t.Fatal(err)
		}
		check("role changed", "ann", "bob")
		warm()
		// A role set to what it is already drops nobody.
		if _, _, err := f.m.SetSpaceGroupRole(ctx, h, "boss", g, perm.Editor); err != nil {
			t.Fatal(err)
		}
		check("role unchanged")
		f.in(g, "out")
		check("person added to a local group", "out")
		warm()
		if err := f.m.RemoveGroupPerson(ctx, store.AllTenants(), g, "bob"); err != nil {
			t.Fatal(err)
		}
		check("person removed from a local group", "bob")
		warm()
		// A rename of the space changes names only.
		name := "Beta"
		if _, err := f.m.Update(ctx, store.User{ID: "boss", Role: "member"}, "S1", &name, nil); err == nil {
			t.Fatal("the space is not open, so Update cannot have worked in this fixture")
		}
		f.m.spaceChanged("S1")
		check("space renamed")
		if l, _ := f.m.List(ctx, "ann"); len(l) != 1 {
			t.Fatal(l)
		}
		// A sync with no change drops nobody, one with a change drops that person.
		sg := f.group("Synced", "synced")
		f.provider("co")
		if res, err := f.m.SyncProviderGroups(ctx, "ann", "co", 0, nil); err != nil || res.Changed() {
			t.Fatal(res, err)
		}
		check("sync without a change")
		if res, err := f.m.SyncProviderGroups(ctx, "ann", "co", 0, []store.SyncedMembership{{GroupID: sg, Value: "v"}}); err != nil || !res.Changed() {
			t.Fatal(res, err)
		}
		check("sync with a change", "ann")
		warm()
		if _, err := f.m.DeleteGroup(ctx, store.AllTenants(), g, false, 0); err == nil {
			t.Fatal("a group that is in a space was deleted without force")
		}
		check("refused delete")
		if _, err := f.m.DeleteGroup(ctx, store.AllTenants(), g, true, 0); err != nil {
			t.Fatal(err)
		}
		check("group deleted", "ann", "out")
		for _, u := range []string{"ann", "out"} {
			if _, ok := f.info(u, "S1"); ok {
				t.Fatalf("%s keeps the space after the group was deleted", u)
			}
		}
	})
}

func TestDeletingASpaceDropsItsGroupsPeople(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newGfix(t, st)
		boss := f.user("boss")
		f.user("ann")
		f.user("out")
		f.space("S1", "Alpha", "boss")
		g := f.group("Eng", "local")
		f.in(g, "ann")
		if _, err := f.m.AddSpaceGroup(ctx, f.handle(boss, "S1"), "boss", g, perm.Viewer); err != nil {
			t.Fatal(err)
		}
		for _, u := range []string{"boss", "ann", "out"} {
			f.m.List(ctx, u)
		}
		var members []string
		err := st.Tx(ctx, func(q store.Q) error {
			var err error
			members, err = store.DeleteSpace(ctx, q, store.AllTenants(), "S1")
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		f.m.MembershipChanged(members...)
		if cachedUser(f.m, "boss") || cachedUser(f.m, "ann") || !cachedUser(f.m, "out") {
			t.Fatalf("members %v", members)
		}
		if gr, _ := f.m.Group(ctx, store.AllTenants(), g); gr.Spaces != 0 {
			t.Fatalf("%+v", gr)
		}
	})
}

func TestTheCacheIsBoundedByEntries(t *testing.T) {
	st := storetest.Open(t, "sqlite")
	f := newGfix(t, st)
	f.m.cache.max = 5
	for _, u := range []string{"u1", "u2", "u3"} {
		f.user(u)
	}
	for i := 0; i < 3; i++ {
		f.space(fmt.Sprintf("S%d", i), fmt.Sprintf("Space %d", i), "u1")
		store.AddMember(ctx, st.Q(), fmt.Sprintf("S%d", i), "u2", perm.Viewer, time.Now())
	}
	f.m.List(ctx, "u1") // 3 entries
	f.m.List(ctx, "u2") // 3 more: u1, the least recently used, goes
	if cachedUser(f.m, "u1") || !cachedUser(f.m, "u2") {
		t.Fatal("the least recently used person was not the one dropped")
	}
	f.m.mu.Lock()
	n := f.m.cache.entries
	f.m.mu.Unlock()
	if n != 3 {
		t.Fatalf("entries %d", n)
	}
	// A list bigger than the whole cache is served but not kept.
	f.m.cache.max = 2
	if l, err := f.m.List(ctx, "u1"); err != nil || len(l) != 3 {
		t.Fatal(l, err)
	}
	if cachedUser(f.m, "u1") {
		t.Fatal("kept a list larger than the bound")
	}
}

// Requests keep reading while groups, roles and people change: run with -race.
// Afterwards every person's answer equals what the database says.
func TestConcurrentChangesAndReads(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newGfix(t, st)
		boss := f.user("boss")
		const people = 20
		var users []string
		for i := 0; i < people; i++ {
			id := fmt.Sprintf("p%02d", i)
			f.user(id)
			users = append(users, id)
		}
		f.space("S1", "Alpha", "boss")
		h := f.handle(boss, "S1")
		g1, g2 := f.group("One", "local"), f.group("Two", "local")
		f.in(g1, users[:people/2]...)
		f.in(g2, users[people/4:]...)
		if _, err := f.m.AddSpaceGroup(ctx, h, "boss", g1, perm.Viewer); err != nil {
			t.Fatal(err)
		}
		var stop atomic.Bool
		var wg sync.WaitGroup
		for r := 0; r < 4; r++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; !stop.Load(); i++ {
					u := users[i%people]
					if _, _, err := f.m.infoOf(ctx, u, "S1"); err != nil {
						t.Error(err)
						return
					}
					if _, err := f.m.List(ctx, u); err != nil {
						t.Error(err)
						return
					}
				}
			}()
		}
		for i := 0; i < 15; i++ {
			role := []string{perm.Editor, perm.Viewer, perm.Owner}[i%3]
			if _, _, err := f.m.SetSpaceGroupRole(ctx, h, "boss", g1, role); err != nil {
				t.Fatal(err)
			}
			if i%2 == 0 {
				if _, err := f.m.AddSpaceGroup(ctx, h, "boss", g2, perm.Editor); err != nil {
					t.Fatal(err)
				}
			} else if _, err := f.m.RemoveSpaceGroup(ctx, h, g2); err != nil {
				t.Fatal(err)
			}
			if err := f.m.RemoveGroupPerson(ctx, store.AllTenants(), g1, users[i%(people/2)]); err != nil {
				t.Fatal(err)
			}
			f.in(g1, users[i%(people/2)])
		}
		stop.Store(true)
		wg.Wait()
		// A removal is never missed by a read that raced it.
		for _, u := range users {
			got, ok := f.info(u, "S1")
			want := dbPerms(t, st, u, "S1")
			if ok != (want != 0) || (ok && got.Perms != want) {
				t.Fatalf("%s: cache %v %v, database %v", u, got.Perms, ok, want)
			}
		}
	})
}

// dbPerms is what the database says a person may do in a space.
func dbPerms(t *testing.T, st *store.Store, user, space string) perm.Set {
	t.Helper()
	rows, err := store.ListUserAccess(ctx, st.Q(), user)
	if err != nil {
		t.Fatal(err)
	}
	var ps perm.Set
	for _, r := range rows {
		if r.SpaceID != space {
			continue
		}
		role := r.Role
		if !r.Direct {
			role = r.GroupRole
		}
		p, _ := perm.ForRole(role)
		ps |= p
	}
	return ps
}

func TestSyncTouchesOnlyWhatTheProviderBrought(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newGfix(t, st)
		f.user("ann")
		f.provider("one")
		f.provider("two")
		a, b, c := f.group("A", "synced"), f.group("B", "synced"), f.group("C", "synced")
		local := f.group("Local", "local")
		f.in(local, "ann")
		sync := func(provider string, want ...store.SyncedMembership) SyncResult {
			t.Helper()
			r, err := f.m.SyncProviderGroups(ctx, "ann", provider, 0, want)
			if err != nil {
				t.Fatal(err)
			}
			return r
		}
		m := func(g, v string) store.SyncedMembership { return store.SyncedMembership{GroupID: g, Value: v} }
		if r := sync("one", m(a, "x"), m(b, "y")); len(r.Added) != 2 || len(r.Removed) != 0 {
			t.Fatalf("%+v", r)
		}
		if r := sync("two", m(b, "z"), m(c, "w")); len(r.Added) != 2 {
			t.Fatalf("%+v", r)
		}
		// Provider one stops sending y: only its membership goes; two's into B stays.
		if r := sync("one", m(a, "x")); len(r.Removed) != 1 || r.Removed[0] != b || len(r.Added) != 0 {
			t.Fatalf("%+v", r)
		}
		gs, _ := store.UserGroups(ctx, st.Q(), "ann")
		if len(gs) != 4 {
			t.Fatalf("groups %v", gs)
		}
		// Nothing changed: nothing written (the person's row count and the cache are as they were).
		if r := sync("one", m(a, "x")); r.Changed() {
			t.Fatalf("%+v", r)
		}
		// Empty list: out of everything of that provider, never the local group or provider two.
		if r := sync("one"); len(r.Removed) != 1 {
			t.Fatalf("%+v", r)
		}
		gs, _ = store.UserGroups(ctx, st.Q(), "ann")
		if len(gs) != 3 {
			t.Fatalf("groups %v", gs)
		}
		// Two values into one group are two rows and one group.
		sync("two", m(b, "z"), m(b, "z2"), m(c, "w"))
		if g, _ := f.m.Group(ctx, store.AllTenants(), b); g.People != 1 {
			t.Fatalf("%+v", g)
		}
		// A mapping entry removed takes its rows at once, not the other value's.
		drop := func(fn func(q store.Q) ([]string, error)) error {
			var ids []string
			err := st.Tx(ctx, func(q store.Q) (err error) { ids, err = fn(q); return })
			f.m.MembershipChanged(ids...)
			return err
		}
		if err := drop(func(q store.Q) ([]string, error) { return f.m.DropMapped(ctx, q, "two", "z", "") }); err != nil {
			t.Fatal(err)
		}
		if g, _ := f.m.Group(ctx, store.AllTenants(), b); g.People != 1 {
			t.Fatalf("the other value's row is gone: %+v", g)
		}
		if err := drop(func(q store.Q) ([]string, error) { return f.m.DropMapped(ctx, q, "two", "z2", "") }); err != nil {
			t.Fatal(err)
		}
		if g, _ := f.m.Group(ctx, store.AllTenants(), b); g.People != 0 {
			t.Fatalf("%+v", g)
		}
		// A deleted provider takes everything it brought.
		if err := drop(func(q store.Q) ([]string, error) { return f.m.DropProvider(ctx, q, "two") }); err != nil {
			t.Fatal(err)
		}
		gs, _ = store.UserGroups(ctx, st.Q(), "ann")
		if len(gs) != 1 || gs[0].ID != local {
			t.Fatalf("groups %v", gs)
		}
	})
}

// BenchmarkCacheMissInThousandGroups is a person in 1,000 groups that are in 200
// spaces: what one miss costs (the single query and the building of the table).
func BenchmarkCacheMissInThousandGroups(b *testing.B) {
	st := storetest.Open(b, "sqlite")
	m, err := NewAccounts(Options{Store: st, DataDir: b.TempDir() + "/d"})
	if err != nil {
		b.Fatal(err)
	}
	defer m.Close()
	now := time.Now()
	if err := store.InsertUser(ctx, st.Q(), store.User{ID: "u", Username: "u", Name: "u", Role: "member", Created: now}); err != nil {
		b.Fatal(err)
	}
	for s := 0; s < 200; s++ {
		if err := store.InsertSpace(ctx, st.Q(), store.DefaultTenantID, fmt.Sprintf("S%04d", s), fmt.Sprintf("Space %d", s), "", "", now); err != nil {
			b.Fatal(err)
		}
	}
	for g := 0; g < 1000; g++ {
		id := fmt.Sprintf("G%04d", g)
		if err := store.InsertGroup(ctx, st.Q(), store.Group{ID: id, Name: id, Source: "local", Created: now, Updated: now}); err != nil {
			b.Fatal(err)
		}
		if err := store.AddLocalMembers(ctx, st.Q(), id, []string{"u"}, now); err != nil {
			b.Fatal(err)
		}
		// Every group is in 4 spaces (800 pairs for the person, 200 distinct spaces).
		for k := 0; k < 4; k++ {
			if err := store.AddSpaceGroup(ctx, st.Q(), fmt.Sprintf("S%04d", (g*4+k)%200), id, perm.Viewer, now); err != nil {
				b.Fatal(err)
			}
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.MembershipChanged("u")
		l, err := m.List(ctx, "u")
		if err != nil || len(l) != 200 {
			b.Fatal(len(l), err)
		}
	}
}

// BenchmarkCacheHit is the permission check of a request: a person in 200
// spaces, asked about one, with the cache warm.
func BenchmarkCacheHit(b *testing.B) {
	st := storetest.Open(b, "sqlite")
	m, err := NewAccounts(Options{Store: st, DataDir: b.TempDir() + "/d"})
	if err != nil {
		b.Fatal(err)
	}
	defer m.Close()
	now := time.Now()
	if err := store.InsertUser(ctx, st.Q(), store.User{ID: "u", Username: "u", Name: "u", Role: "member", Created: now}); err != nil {
		b.Fatal(err)
	}
	for s := 0; s < 200; s++ {
		id := fmt.Sprintf("S%04d", s)
		if err := store.InsertSpace(ctx, st.Q(), store.DefaultTenantID, id, id, "", "", now); err != nil {
			b.Fatal(err)
		}
		if err := store.AddMember(ctx, st.Q(), id, "u", perm.Editor, now); err != nil {
			b.Fatal(err)
		}
	}
	if _, _, err := m.infoOf(ctx, "u", "S0100"); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok, err := m.infoOf(ctx, "u", "S0100"); err != nil || !ok {
			b.Fatal(ok, err)
		}
	}
}

// A change that takes access away from a group's people and a change that adds
// someone to the group race: the removal has read the group's people, then the
// add commits and the newcomer's list is read again, then the removal commits.
// The newcomer must not keep the access (on PostgreSQL, READ COMMITTED lets the
// add through unless the group is locked; SQLite has one writer at a time).
func TestRevokingRacesAnAddToTheGroup(t *testing.T) {
	type grant struct {
		name, source string
		add          func(f *gfix, g string) error
	}
	grants := []grant{
		{"local add", store.GroupLocal, func(f *gfix, g string) error {
			_, _, _, err := f.m.AddGroupPeople(ctx, store.AllTenants(), g, []string{"x"})
			return err
		}},
		{"sync", store.GroupSynced, func(f *gfix, g string) error {
			_, err := f.m.SyncProviderGroups(ctx, "x", "co", 0, []store.SyncedMembership{{GroupID: g, Value: "v"}})
			return err
		}},
	}
	type revoke struct {
		name string
		do   func(f *gfix, h *Handle, g string) error
	}
	revokes := []revoke{
		{"remove from space", func(f *gfix, h *Handle, g string) error {
			_, err := f.m.RemoveSpaceGroup(ctx, h, g)
			return err
		}},
		{"downgrade", func(f *gfix, h *Handle, g string) error {
			_, _, err := f.m.SetSpaceGroupRole(ctx, h, "boss", g, perm.Viewer)
			return err
		}},
		{"delete group", func(f *gfix, h *Handle, g string) error {
			_, err := f.m.DeleteGroup(ctx, store.AllTenants(), g, true, 0)
			return err
		}},
	}
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		if st.Dialect() != store.Postgres {
			t.Skip("SQLite has one writer at a time")
		}
		for _, gr := range grants {
			for _, rv := range revokes {
				t.Run(gr.name+"/"+rv.name, func(t *testing.T) {
					st := storetest.Open(t, "postgres")
					f := newGfix(t, st)
					boss := f.user("boss")
					f.user("x")
					f.provider("co")
					f.space("S1", "Alpha", "boss")
					h := f.handle(boss, "S1")
					g := f.group("G", gr.source)
					if _, err := f.m.AddSpaceGroup(ctx, h, "boss", g, perm.Editor); err != nil {
						t.Fatal(err)
					}
					done := make(chan error, 1)
					var once sync.Once
					f.m.testHook = func(point string) {
						if point != "group-people-read" {
							return
						}
						once.Do(func() {
							go func() { done <- gr.add(f, g) }()
							select {
							case err := <-done:
								// The add went through: the newcomer's list is read now.
								done <- err
								f.info("x", "S1")
							case <-time.After(300 * time.Millisecond):
								// The add waits for the group: as it should.
							}
						})
					}
					if err := rv.do(f, h, g); err != nil {
						t.Fatal(err)
					}
					// An add that waited for a deleted group finds it gone.
					if err := <-done; err != nil && !(rv.name == "delete group" && errors.Is(err, ErrGroupNotFound)) {
						t.Fatal(err)
					}
					f.m.testHook = nil
					got, ok := f.info("x", "S1")
					want := dbPerms(t, st, "x", "S1")
					if ok != (want != 0) || (ok && got.Perms != want) {
						t.Fatalf("x: cache %v %v, database %v", got.Perms, ok, want)
					}
				})
			}
		}
	})
}

// provider adds a sign-in provider row; its groups version starts at 0.
func (f *gfix) provider(slug string) store.ProviderRow {
	f.t.Helper()
	now := time.Now()
	p := store.ProviderRow{Slug: slug, Kind: "oidc", Name: slug, Config: "{}", Created: now, Updated: now}
	if err := store.InsertProvider(ctx, f.st.Q(), p); err != nil {
		f.t.Fatal(err)
	}
	got, err := store.GetProvider(ctx, f.st.Q(), slug)
	if err != nil {
		f.t.Fatal(err)
	}
	return *got
}

// A provider's mapping change (or deletion) drops what it brought in the same
// transaction as the provider's row, and a sign-in that still holds the old
// mapping cannot bring it back afterwards.
func TestASignInWithTheOldMappingCannotReAdd(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newGfix(t, st)
		f.user("ann")
		g := f.group("G", "synced")
		p := f.provider("co")
		want := []store.SyncedMembership{{GroupID: g, Value: "v"}}
		if res, err := f.m.SyncProviderGroups(ctx, "ann", "co", p.GroupsVersion, want); err != nil || !res.Changed() {
			t.Fatal(res, err)
		}
		// The entry is removed: the provider's row and the memberships, together.
		var dropped []string
		err := st.Tx(ctx, func(q store.Q) error {
			if err := store.UpdateProvider(ctx, q, p); err != nil {
				return err
			}
			var err error
			dropped, err = f.m.DropMapped(ctx, q, "co", "v", "")
			return err
		})
		if err != nil || len(dropped) != 1 || dropped[0] != "ann" {
			t.Fatal(dropped, err)
		}
		f.m.MembershipChanged(dropped...)
		// A sign-in that read the provider before the change comes late.
		if res, err := f.m.SyncProviderGroups(ctx, "ann", "co", p.GroupsVersion, want); err != nil || res.Changed() {
			t.Fatal(res, err)
		}
		if in, _ := store.UserInGroup(ctx, st.Q(), "ann", g); in {
			t.Fatal("a sign-in with the old mapping put ann back")
		}
		// With the provider's current version it syncs again.
		cur, err := store.GetProvider(ctx, st.Q(), "co")
		if err != nil || cur.GroupsVersion == p.GroupsVersion {
			t.Fatal(cur, err)
		}
		if res, err := f.m.SyncProviderGroups(ctx, "ann", "co", cur.GroupsVersion, want); err != nil || !res.Changed() {
			t.Fatal(res, err)
		}
		// A deleted provider: its memberships go with the row, and a late sign-in adds nothing.
		err = st.Tx(ctx, func(q store.Q) error {
			if err := store.DeleteProvider(ctx, q, "co"); err != nil {
				return err
			}
			var err error
			dropped, err = f.m.DropProvider(ctx, q, "co")
			return err
		})
		if err != nil || len(dropped) != 1 {
			t.Fatal(dropped, err)
		}
		f.m.MembershipChanged(dropped...)
		if res, err := f.m.SyncProviderGroups(ctx, "ann", "co", cur.GroupsVersion, want); err != nil || res.Changed() {
			t.Fatal(res, err)
		}
		if in, _ := store.UserInGroup(ctx, st.Q(), "ann", g); in {
			t.Fatal("a sign-in through a deleted provider put ann back")
		}
	})
}

// A new group whose random id is taken gets another id; only a taken name is
// "exists".
func TestCreateGroupRetriesAnIDClash(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newGfix(t, st)
		first := f.group("First", "local")
		calls := 0
		f.m.testID = func() string {
			calls++
			if calls == 1 {
				return first // the clash
			}
			return ""
		}
		g, err := f.m.CreateGroup(ctx, store.DefaultTenantID, "Second", "", "local")
		if err != nil {
			t.Fatal(err)
		}
		if g.ID == first || g.Name != "Second" || calls < 2 {
			t.Fatalf("%+v after %d ids", g, calls)
		}
		f.m.testID = nil
		if _, err := f.m.CreateGroup(ctx, store.DefaultTenantID, "first", "", "local"); !errors.Is(err, ErrGroupExists) {
			t.Fatalf("a taken name: %v", err)
		}
	})
}

// MaxGroups is a tenant's: one tenant at the limit leaves the others free, and
// the refusal says how many that tenant has.
func TestTheGroupLimitIsPerTenant(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		f := newGfix(t, st)
		now := time.Now()
		const tenB = "T000000002"
		if err := st.CreateTenant(ctx, store.Tenant{ID: tenB, Slug: "acme", Name: "Acme", Created: now, Updated: now}); err != nil {
			t.Fatal(err)
		}
		// B at the limit, in one statement.
		if _, err := st.Q().Exec(ctx, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?)
INSERT INTO groups(id, tenant_id, name, name_key, description, source, created, updated)
SELECT 'g' || i, ?, 'g' || i, 'g' || i, '', 'local', ?, ? FROM n`, MaxGroups, tenB, now.UnixMilli(), now.UnixMilli()); err != nil {
			t.Fatal(err)
		}
		if _, err := f.m.CreateGroup(ctx, store.DefaultTenantID, "Writers", "", store.GroupLocal); err != nil {
			t.Fatalf("another tenant's groups count against this one: %v", err)
		}
		_, err := f.m.CreateGroup(ctx, tenB, "One more", "", store.GroupLocal)
		if !errors.Is(err, ErrGroupLimit) {
			t.Fatalf("%v", err)
		}
		if want := fmt.Sprintf("a tenant has at most %d groups; this one has %d", MaxGroups, MaxGroups); !strings.HasSuffix(err.Error(), want) {
			t.Fatalf("%q, want %q", err, want)
		}
	})
}
