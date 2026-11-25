package store_test

import (
	"fmt"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

func mkGroup(t *testing.T, st *store.Store, id, name, source string) {
	t.Helper()
	now := time.Now()
	if err := store.InsertGroup(ctx, st.Q(), store.Group{ID: id, Name: name, Source: source, Created: now, Updated: now}); err != nil {
		t.Fatal(err)
	}
}

func TestGroupListsPageByName(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		d := st.Dialect()
		for i, n := range []string{"beta", "Alpha", "gamma", "Épsilon", "delta"} {
			mkGroup(t, st, fmt.Sprintf("G%09d", i), n, store.GroupLocal)
		}
		mkGroup(t, st, "GSYNC00000", "zeta synced", store.GroupSynced)
		if err := store.InsertGroup(ctx, st.Q(), store.Group{ID: "GDUP000000", Name: "ALPHA", Source: "local"}); !store.IsUnique(err) {
			t.Fatalf("a name is unique ignoring case: %v", err)
		}
		var names []string
		after := ""
		for {
			page, next, err := store.ListGroups(ctx, st.Q(), d, store.AllTenants(), store.GroupFilter{After: after, Limit: 2})
			if err != nil {
				t.Fatal(err)
			}
			if len(page) > 2 {
				t.Fatal(len(page))
			}
			for _, g := range page {
				names = append(names, g.Name)
			}
			if next == "" {
				break
			}
			after = next
		}
		// Bytewise by folded name on both databases: "épsilon" sorts after the ASCII ones.
		want := []string{"Alpha", "beta", "delta", "gamma", "zeta synced", "Épsilon"}
		if fmt.Sprint(names) != fmt.Sprint(want) {
			t.Fatalf("%v", names)
		}
		got, _, _ := store.ListGroups(ctx, st.Q(), d, store.AllTenants(), store.GroupFilter{Query: "ALP"})
		if len(got) != 1 || got[0].Name != "Alpha" {
			t.Fatal(got)
		}
		got, _, _ = store.ListGroups(ctx, st.Q(), d, store.AllTenants(), store.GroupFilter{Source: store.GroupSynced})
		if len(got) != 1 {
			t.Fatal(got)
		}
		// A search treats % and _ literally.
		if got, _, _ = store.ListGroups(ctx, st.Q(), d, store.AllTenants(), store.GroupFilter{Query: "%"}); len(got) != 0 {
			t.Fatal(got)
		}
		refs, err := store.SearchGroups(ctx, st.Q(), d, store.AllTenants(), "ta", 20)
		if err != nil || len(refs) != 3 { // beta, delta, zeta synced
			t.Fatal(refs, err)
		}
	})
}

func TestPeopleAndEffectiveListsAreOneQueryPages(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		d := st.Dialect()
		now := time.Now()
		for _, u := range []string{"ann", "bob", "cy", "dee"} {
			mkUser(t, st, "U-"+u, u, "member")
		}
		if err := store.SetDisabled(ctx, st.Q(), "U-dee", true); err != nil {
			t.Fatal(err)
		}
		mkGroup(t, st, "GLOCAL0000", "Local", store.GroupLocal)
		mkGroup(t, st, "GSYNCED000", "Synced", store.GroupSynced)
		if err := store.AddLocalMembers(ctx, st.Q(), "GLOCAL0000", []string{"U-ann", "U-bob", "U-dee"}, now); err != nil {
			t.Fatal(err)
		}
		if err := store.ApplySynced(ctx, st.Q(), "U-bob", "co", nil, []store.SyncedMembership{{"GSYNCED000", "a"}, {"GSYNCED000", "b"}}, now); err != nil {
			t.Fatal(err)
		}
		// A synced membership of a group that does not exist is skipped.
		if err := store.ApplySynced(ctx, st.Q(), "U-bob", "co", nil, []store.SyncedMembership{{"GNOTHERE00", "a"}}, now); err != nil {
			t.Fatal(err)
		}
		page, next, err := store.ListGroupPeople(ctx, st.Q(), d, "GSYNCED000", "", "", 10)
		if err != nil || len(page) != 1 || next != "" || len(page[0].From) != 2 || page[0].From[0].Provider != "co" {
			t.Fatalf("%+v %q %v", page, next, err)
		}
		page, next, err = store.ListGroupPeople(ctx, st.Q(), d, "GLOCAL0000", "", "", 2)
		if err != nil || len(page) != 2 || next != "bob" || len(page[0].From) != 0 {
			t.Fatalf("%+v %q %v", page, next, err)
		}
		page, next, _ = store.ListGroupPeople(ctx, st.Q(), d, "GLOCAL0000", "", next, 2)
		if len(page) != 1 || page[0].Username != "dee" || !page[0].Disabled || next != "" {
			t.Fatalf("%+v %q", page, next)
		}
		if page, _, _ = store.ListGroupPeople(ctx, st.Q(), d, "GLOCAL0000", "BO", "", 10); len(page) != 1 {
			t.Fatal(page)
		}

		if err := store.InsertSpace(ctx, st.Q(), store.DefaultTenantID, "S1", "Alpha", "", "", now); err != nil {
			t.Fatal(err)
		}
		if err := store.AddMember(ctx, st.Q(), "S1", "U-cy", "owner", now); err != nil {
			t.Fatal(err)
		}
		if err := store.AddMember(ctx, st.Q(), "S1", "U-bob", "viewer", now); err != nil {
			t.Fatal(err)
		}
		for g, role := range map[string]string{"GLOCAL0000": "editor", "GSYNCED000": "viewer"} {
			if err := store.AddSpaceGroup(ctx, st.Q(), "S1", g, role, now); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.AddSpaceGroup(ctx, st.Q(), "S1", "GNOTHERE00", "viewer", now); err != store.ErrNotFound {
			t.Fatalf("%v", err)
		}
		eff, next, err := store.ListEffective(ctx, st.Q(), d, "S1", "", "", 2)
		if err != nil || len(eff) != 2 || next != "bob" {
			t.Fatalf("%+v %q %v", eff, next, err)
		}
		if eff[0].Username != "ann" || eff[0].DirectRole != "" || len(eff[0].Groups) != 1 || eff[0].Groups[0].Name != "Local" {
			t.Fatalf("%+v", eff[0])
		}
		if eff[1].DirectRole != "viewer" || len(eff[1].Groups) != 2 { // bob: two groups, one row each however many values
			t.Fatalf("%+v", eff[1])
		}
		eff, next, _ = store.ListEffective(ctx, st.Q(), d, "S1", "", next, 2)
		if len(eff) != 1 || eff[0].Username != "cy" || eff[0].DirectRole != "owner" || next != "" { // dee is disabled and left out
			t.Fatalf("%+v %q", eff, next)
		}

		rows, err := store.ListUserAccess(ctx, st.Q(), "U-bob")
		if err != nil || len(rows) != 3 { // direct, and one row per distinct group role
			t.Fatalf("%+v %v", rows, err)
		}
		spaces, next, err := store.ListGroupSpaces(ctx, st.Q(), d, "GLOCAL0000", "", 10)
		if err != nil || len(spaces) != 1 || spaces[0].Role != "editor" || next != "" {
			t.Fatalf("%+v %v", spaces, err)
		}
		ids, _ := store.SpaceGroupUserIDs(ctx, st.Q(), "S1")
		if len(ids) != 3 { // ann, bob, dee, each once
			t.Fatal(ids)
		}
		// Deleting a group takes its rows and nothing else.
		if err := store.DeleteGroup(ctx, st.Q(), "GLOCAL0000"); err != nil {
			t.Fatal(err)
		}
		if n, _ := store.CountSpaceGroups(ctx, st.Q(), "S1"); n != 1 {
			t.Fatal(n)
		}
		if _, err := store.GetUser(ctx, st.Q(), store.AllTenants(), "U-ann"); err != nil {
			t.Fatal(err)
		}
	})
}
