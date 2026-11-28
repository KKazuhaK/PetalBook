package api_test

import (
	"context"
	"testing"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/store"
)

// An administrator who is not a member of a space manages it (tenant.spaces.manage)
// and has no owner's say: they cannot make or unmake an owner, give themselves or a
// group of theirs a role, or touch the remote. Taking the space over is the one way,
// and it is audited and its owners are told.
func TestATenantAdministratorTakesASpaceOverOnlyExplicitly(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		if _, err := a.auth.CreateUser(context.Background(), auth.CreateParams{Username: "adm", Name: "Ada Admin", Role: "admin", Password: pw, TenantID: store.DefaultTenantID}); err != nil {
			t.Fatal(err)
		}
		adm := a.newClient("")
		adm.do("POST", "/api/login", map[string]any{"identifier": "adm", "password": pw}).want(t, 200, "")
		own := a.addUser("own", pw, "member")
		mem := a.addUser("mem", pw, "member")
		a.addUser("ed", pw, "member")
		sp := own.do("POST", "/api/spaces", map[string]any{"name": "Team"}).want(t, 201, "").body["id"].(string)
		base := "/api/spaces/" + sp
		ownID, admID := mustUserID(t, a, "own"), mustUserID(t, a, "adm")

		// What managing still allows: a member who is no owner, and their role.
		adm.do("POST", base+"/members", map[string]any{"username": "mem", "role": "viewer"}).want(t, 201, "")
		adm.do("PATCH", base+"/members/"+mustUserID(t, a, "mem"), map[string]any{"role": "editor"}).want(t, 200, "")

		// Implicitly taking over: each is refused.
		adm.do("POST", base+"/members", map[string]any{"username": "adm", "role": "editor"}).want(t, 403, "takeover_required")
		adm.do("POST", base+"/members", map[string]any{"username": "ed", "role": "owner"}).want(t, 403, "takeover_required")
		adm.do("PATCH", base+"/members/"+mustUserID(t, a, "mem"), map[string]any{"role": "owner"}).want(t, 403, "takeover_required")
		adm.do("PATCH", base+"/members/"+ownID, map[string]any{"role": "editor"}).want(t, 403, "takeover_required")
		adm.do("DELETE", base+"/members/"+ownID, nil).want(t, 403, "takeover_required")
		adm.do("PUT", base+"/remote", map[string]any{"url": "https://git.example.test/adm/mine.git"}).want(t, 403, "takeover_required")
		adm.do("PUT", base+"/remote/assets", map[string]any{}).want(t, 403, "takeover_required")
		mine := adm.mkGroup(t, "Mine", "local")
		adm.groupAdd(t, mine, admID).want(t, 200, "")
		adm.do("POST", base+"/groups", map[string]any{"groupId": mine, "role": "viewer"}).want(t, 403, "takeover_required")
		other := adm.mkGroup(t, "Other", "local")
		adm.do("POST", base+"/groups", map[string]any{"groupId": other, "role": "owner"}).want(t, 403, "takeover_required")
		adm.do("POST", base+"/groups", map[string]any{"groupId": other, "role": "editor"}).want(t, 201, "")
		adm.do("PATCH", base+"/groups/"+other, map[string]any{"role": "owner"}).want(t, 403, "takeover_required")
		// Nor do they read the content.
		adm.do("GET", base+"/pages", nil).want(t, 403, "no_permission")
		// Nothing of that reached the members.
		for _, m := range own.do("GET", base+"/members", nil).want(t, 200, "").body["members"].([]any) {
			m := m.(map[string]any)
			if m["username"] == "adm" || (m["role"] == "owner") != (m["username"] == "own") {
				t.Fatalf("members changed: %v", m)
			}
		}

		// The member has no key to take over; a reason is required.
		mem.do("POST", "/api/admin/spaces/"+sp+"/takeover", map[string]any{"reason": "x"}).want(t, 403, "forbidden")
		adm.do("POST", "/api/admin/spaces/"+sp+"/takeover", map[string]any{"reason": "  "}).want(t, 400, "invalid")
		adm.do("POST", "/api/admin/spaces/"+sp+"/takeover", map[string]any{"reason": "the owner left the team"}).want(t, 204, "")

		// Now the administrator is the owner, and the owner an editor.
		roles := map[string]string{}
		for _, m := range adm.do("GET", base+"/members", nil).want(t, 200, "").body["members"].([]any) {
			m := m.(map[string]any)
			roles[m["username"].(string)] = m["role"].(string)
		}
		if roles["adm"] != "owner" || roles["own"] != "editor" {
			t.Fatal(roles)
		}
		adm.do("GET", base+"/pages", nil).want(t, 200, "")
		adm.do("POST", base+"/remote/key", nil).want(t, 200, "")

		// Audited, with the reason.
		a.auth.FlushAudit(context.Background())
		rows := root.audit(t, "space_taken_over")
		if len(rows) != 1 {
			t.Fatal(rows)
		}
		d := rows[0]["detail"].(map[string]any)
		if d["space"] != sp || d["to"] != admID || d["reason"] != "the owner left the team" {
			t.Fatal(rows[0])
		}
		if p, _ := d["previous"].([]any); len(p) != 1 || p[0] != ownID {
			t.Fatal(rows[0])
		}

		// The owner it replaced is told.
		items, _, _ := noteItems(t, own, "")
		found := false
		for _, it := range items {
			p, _ := it["payload"].(map[string]any)
			if it["kind"] == "space_taken_over" && it["ref"] == sp && p["space"] == "Team" && p["by"] == "Ada Admin" {
				found = true
			}
		}
		if !found {
			t.Fatalf("the owner was not told: %v", items)
		}
	})
}

// A transfer is an explicit change of owner too: audited, and the owners it
// replaces are told.
func TestATransferIsAuditedAndTellsTheOwners(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		own := a.addUser("own", pw, "member")
		a.addUser("heir", pw, "member")
		sp := own.do("POST", "/api/spaces", map[string]any{"name": "Team"}).want(t, 201, "").body["id"].(string)
		heir := mustUserID(t, a, "heir")
		root.do("POST", "/api/admin/spaces/"+sp+"/transfer", map[string]any{"userId": heir}).want(t, 204, "")
		a.auth.FlushAudit(context.Background())
		rows := root.audit(t, "space_transferred")
		if len(rows) != 1 || rows[0]["detail"].(map[string]any)["to"] != heir {
			t.Fatal(rows)
		}
		items, _, _ := noteItems(t, own, "")
		if len(items) == 0 || items[0]["kind"] != "space_transferred" {
			t.Fatal(items)
		}
	})
}
