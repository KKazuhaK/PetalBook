package api_test

import (
	"context"
	"testing"
)

// A site administrator takes over a space of any tenant (and only explicitly);
// a tenant's administrator only one of their own; a personal space nobody.
func TestASiteAdministratorTakesOverASpaceOfAnyTenant(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		base := "/api/spaces/" + f.spaceB
		// Not implicitly.
		f.root.do("PUT", base+"/remote", map[string]any{"url": "https://git.example.test/r/x.git"}).want(t, 403, "takeover_required")
		// A tenant administrator of A does not reach B's space at all.
		f.admA.do("POST", "/api/admin/spaces/"+f.spaceB+"/takeover", map[string]any{"reason": "mine now"}).want(t, 404, "not_found")
		// Nobody takes over a personal space.
		f.root.do("POST", "/api/admin/spaces/"+f.memB.personalSpace()+"/takeover", map[string]any{"reason": "x"}).want(t, 409, "personal_space")
		f.root.do("POST", "/api/admin/spaces/"+f.memA.personalSpace()+"/takeover", map[string]any{"reason": "x"}).want(t, 409, "personal_space")

		f.root.do("POST", "/api/admin/spaces/"+f.spaceB+"/takeover", map[string]any{"reason": "the tenant asked for help"}).want(t, 204, "")
		f.root.do("GET", base+"/pages", nil).want(t, 200, "")
		roles := map[string]string{}
		for _, m := range f.root.do("GET", base+"/members", nil).want(t, 200, "").body["members"].([]any) {
			m := m.(map[string]any)
			roles[m["username"].(string)] = m["role"].(string)
		}
		if roles["root"] != "owner" || roles["adm-b"] != "editor" {
			t.Fatal(roles)
		}
		a.auth.FlushAudit(context.Background())
		if rows := f.root.audit(t, "space_taken_over"); len(rows) != 1 || rows[0]["detail"].(map[string]any)["space"] != f.spaceB {
			t.Fatal(rows)
		}
		items, _, _ := noteItems(t, f.admB, "")
		if len(items) == 0 || items[0]["kind"] != "space_taken_over" {
			t.Fatal(items)
		}
	})
}

// A transfer to oneself is a takeover without a reason: refused, pointing to
// the takeover.
func TestATransferToOneselfIsRefused(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		own := a.addUser("own", pw, "member")
		sp := own.do("POST", "/api/spaces", map[string]any{"name": "Team"}).want(t, 201, "").body["id"].(string)
		root.do("POST", "/api/admin/spaces/"+sp+"/transfer", map[string]any{"userId": mustUserID(t, a, "root")}).want(t, 403, "takeover_required")
	})
}

// The people of a group that holds the owner role are told of a takeover too.
func TestATakeoverTellsThePeopleOfAnOwnerGroup(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		own := a.addUser("own", pw, "member")
		gm := a.addUser("gm", pw, "member")
		sp := own.do("POST", "/api/spaces", map[string]any{"name": "Team"}).want(t, 201, "").body["id"].(string)
		g := root.mkGroup(t, "Owners", "local")
		root.groupAdd(t, g, mustUserID(t, a, "gm")).want(t, 200, "")
		own.do("POST", "/api/spaces/"+sp+"/groups", map[string]any{"groupId": g, "role": "owner"}).want(t, 201, "")
		root.do("POST", "/api/admin/spaces/"+sp+"/takeover", map[string]any{"reason": "audit"}).want(t, 204, "")
		items, _, _ := noteItems(t, gm, "")
		found := false
		for _, it := range items {
			if it["kind"] == "space_taken_over" {
				found = true
			}
		}
		if !found {
			t.Fatalf("the owner group's people were not told: %v", items)
		}
	})
}
