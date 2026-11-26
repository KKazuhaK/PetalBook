package api_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/store"
)

// spaceRoleOf is the role a person holds in a space as they see it in their
// list ("" when they do not reach it).
func spaceRoleOf(t *testing.T, c *client, space string) string {
	t.Helper()
	for _, s := range c.do("GET", "/api/spaces", nil).want(t, 200, "").body["spaces"].([]any) {
		s := s.(map[string]any)
		if s["id"] == space {
			r, _ := s["role"].(string)
			return r
		}
	}
	return ""
}

// An administrator who adds themselves to a group that holds a role in a space
// they do not reach would take that space's role without a takeover. It is
// refused unless they say why (an explicit, audited action) and hold the
// takeover key; adding other people, or oneself to a group that reaches
// nothing new, stays as it was.
func TestAnAdministratorJoiningAGroupOfASpaceDoesItExplicitly(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		if _, err := a.auth.CreateUser(context.Background(), auth.CreateParams{Username: "adm", Name: "Ada Admin", Role: "admin", Password: pw, TenantID: store.DefaultTenantID}); err != nil {
			t.Fatal(err)
		}
		adm := a.newClient("")
		adm.do("POST", "/api/login", map[string]any{"username": "adm", "password": pw}).want(t, 200, "")
		own := a.addUser("own", pw, "member")
		a.addUser("gm", pw, "member")
		admID, gmID := mustUserID(t, a, "adm"), mustUserID(t, a, "gm")
		sp := own.do("POST", "/api/spaces", map[string]any{"name": "Team"}).want(t, 201, "").body["id"].(string)
		g := root.mkGroup(t, "Team people", "local")
		own.do("POST", "/api/spaces/"+sp+"/groups", map[string]any{"groupId": g, "role": "editor"}).want(t, 201, "")

		// Others: as before.
		adm.groupAdd(t, g, gmID).want(t, 200, "")
		// Themselves, implicitly: refused, and nothing was added (gm with them neither).
		adm.do("POST", "/api/admin/groups/"+g+"/members", map[string]any{"userIds": []string{admID, mustUserID(t, a, "own")}}).want(t, 403, "takeover_required")
		if spaceRoleOf(t, adm, sp) != "" {
			t.Fatal("the administrator reached the space")
		}
		// A group that reaches nothing new: as before.
		empty := adm.mkGroup(t, "Nothing", "local")
		adm.groupAdd(t, empty, admID).want(t, 200, "")
		mineSp := adm.do("POST", "/api/spaces", map[string]any{"name": "Mine"}).want(t, 201, "").body["id"].(string)
		mineG := adm.mkGroup(t, "Mine", "local")
		adm.do("POST", "/api/spaces/"+mineSp+"/groups", map[string]any{"groupId": mineG, "role": "viewer"}).want(t, 201, "")
		adm.groupAdd(t, mineG, admID).want(t, 200, "")

		// Explicitly, with a reason: done and audited with the spaces it reaches.
		adm.do("POST", "/api/admin/groups/"+g+"/members", map[string]any{"userIds": []string{admID}, "reason": "  "}).want(t, 400, "invalid")
		adm.do("POST", "/api/admin/groups/"+g+"/members", map[string]any{"userIds": []string{admID}, "reason": "covering for the team"}).want(t, 200, "")
		if r := spaceRoleOf(t, adm, sp); r != "editor" {
			t.Fatalf("role %q", r)
		}
		a.auth.FlushAudit(context.Background())
		rows := root.audit(t, "group_self_added")
		if len(rows) != 1 {
			t.Fatal(rows)
		}
		d := rows[0]["detail"].(map[string]any)
		if d["group"] != g || d["reason"] != "covering for the team" || !slices.Equal(toStrings(d["spaces"]), []string{sp}) {
			t.Fatal(d)
		}
	})
}

// A person given a custom role holds its keys (the session says so and the
// routes take it); without the takeover key, joining a group of a space they
// do not reach is refused even with a reason.
func TestACustomRoleGrantsItsKeysAndNoMore(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		own := a.addUser("own", pw, "member")
		mod := a.addUser("mod", pw, "member")
		modID := mustUserID(t, a, "mod")
		mod.do("GET", "/api/admin/groups", nil).want(t, 403, "forbidden")

		ctx, now := context.Background(), time.Now()
		if err := a.st.InsertRole(ctx, a.st.Q(), store.Role{ID: "R1", TenantID: store.DefaultTenantID, Scope: store.RoleScopeTenant, Name: "group keepers",
			Keys: []string{"tenant.groups.read", "tenant.groups.manage"}, Created: now, Updated: now}); err != nil {
			t.Fatal(err)
		}
		if err := store.InsertRoleGrant(ctx, a.st.Q(), store.RoleGrant{RoleID: "R1", SubjectKind: store.GrantToUser, SubjectID: modID, Created: now}); err != nil {
			t.Fatal(err)
		}
		a.auth.RolesChanged()

		perms := toStrings(mod.do("GET", "/api/session", nil).want(t, 200, "").body["user"].(map[string]any)["permissions"])
		if !slices.Contains(perms, "tenant.groups.manage") || !slices.Contains(perms, "tenant.spaces.create") {
			t.Fatal(perms)
		}
		mod.do("GET", "/api/admin/settings", nil).want(t, 403, "forbidden")
		sp := own.do("POST", "/api/spaces", map[string]any{"name": "Team"}).want(t, 201, "").body["id"].(string)
		g := mod.mkGroup(t, "Team people", "local")
		own.do("POST", "/api/spaces/"+sp+"/groups", map[string]any{"groupId": g, "role": "viewer"}).want(t, 201, "")
		mod.do("POST", "/api/admin/groups/"+g+"/members", map[string]any{"userIds": []string{modID}}).want(t, 403, "takeover_required")
		r := mod.do("POST", "/api/admin/groups/"+g+"/members", map[string]any{"userIds": []string{modID}, "reason": "let me in"}).want(t, 403, "forbidden")
		if r.body["permission"] != "tenant.spaces.takeover" {
			t.Fatal(r.raw)
		}
		if spaceRoleOf(t, mod, sp) != "" {
			t.Fatal("reached the space")
		}
		_ = root
	})
}

// A takeover or a transfer leaves no group an owner: groups that held the owner
// role become editors at once (their people's cached lists too), and the audit
// row names them.
func TestATakeoverMakesOwnerGroupsEditors(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		own := a.addUser("own", pw, "member")
		gm := a.addUser("gm", pw, "member")
		a.addUser("next", pw, "member")
		sp := own.do("POST", "/api/spaces", map[string]any{"name": "Team"}).want(t, 201, "").body["id"].(string)
		g := root.mkGroup(t, "Owners", "local")
		root.groupAdd(t, g, mustUserID(t, a, "gm")).want(t, 200, "")
		own.do("POST", "/api/spaces/"+sp+"/groups", map[string]any{"groupId": g, "role": "owner"}).want(t, 201, "")
		if r := spaceRoleOf(t, gm, sp); r != "owner" {
			t.Fatalf("role %q", r)
		}
		root.do("POST", "/api/admin/spaces/"+sp+"/takeover", map[string]any{"reason": "audit"}).want(t, 204, "")
		if r := spaceRoleOf(t, gm, sp); r != "editor" {
			t.Fatalf("after the takeover the group's person is %q", r)
		}
		for _, x := range root.do("GET", "/api/spaces/"+sp+"/members", nil).want(t, 200, "").body["groups"].([]any) {
			if x := x.(map[string]any); x["role"] != "editor" {
				t.Fatal(x)
			}
		}
		a.auth.FlushAudit(context.Background())
		rows := root.audit(t, "space_taken_over")
		if len(rows) != 1 || !slices.Equal(toStrings(rows[0]["detail"].(map[string]any)["ownerGroups"]), []string{g}) {
			t.Fatal(rows)
		}

		// A transfer does the same.
		root.do("PATCH", "/api/spaces/"+sp+"/groups/"+g, map[string]any{"role": "owner"}).want(t, 200, "")
		root.do("POST", "/api/admin/spaces/"+sp+"/transfer", map[string]any{"userId": mustUserID(t, a, "next")}).want(t, 204, "")
		if r := spaceRoleOf(t, gm, sp); r != "editor" {
			t.Fatalf("after the transfer the group's person is %q", r)
		}
	})
}

// A check moved into a handler asks a key, not the role: a tenant
// administrator given site.admins.manage may change a site administrator.
func TestAHandlerAsksTheKeyNotTheRole(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		a.setup("root", pw)
		if _, err := a.auth.CreateUser(context.Background(), auth.CreateParams{Username: "adm", Role: "admin", Password: pw, TenantID: store.DefaultTenantID}); err != nil {
			t.Fatal(err)
		}
		adm := a.newClient("")
		adm.do("POST", "/api/login", map[string]any{"username": "adm", "password": pw}).want(t, 200, "")
		rootID := mustUserID(t, a, "root")
		adm.do("PATCH", "/api/users/"+rootID, map[string]any{"passkeyRequired": false}).want(t, 403, "forbidden")

		ctx, now := context.Background(), time.Now()
		if err := a.st.InsertRole(ctx, a.st.Q(), store.Role{ID: "R1", Scope: store.RoleScopeSite, Name: "admin keepers", Keys: []string{"site.admins.manage"}, Created: now, Updated: now}); err != nil {
			t.Fatal(err)
		}
		if err := store.InsertRoleGrant(ctx, a.st.Q(), store.RoleGrant{RoleID: "R1", SubjectKind: store.GrantToUser, SubjectID: mustUserID(t, a, "adm"), Created: now}); err != nil {
			t.Fatal(err)
		}
		a.auth.RolesChanged()
		adm.do("PATCH", "/api/users/"+rootID, map[string]any{"passkeyRequired": false}).want(t, 200, "")
	})
}

// grantRole stores a role and gives it to a person or a group.
func grantRole(t *testing.T, a *acc, id, scope, tenant, kind, subject string, keys ...string) {
	t.Helper()
	ctx, now := context.Background(), time.Now()
	if err := a.st.InsertRole(ctx, a.st.Q(), store.Role{ID: id, TenantID: tenant, Scope: scope, Name: id, Keys: keys, Created: now, Updated: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertRoleGrant(ctx, a.st.Q(), store.RoleGrant{RoleID: id, SubjectKind: kind, SubjectID: subject, Created: now}); err != nil {
		t.Fatal(err)
	}
	a.auth.RolesChanged()
}

func permissionsOfSession(t *testing.T, c *client) []string {
	t.Helper()
	return toStrings(c.do("GET", "/api/session", nil).want(t, 200, "").body["user"].(map[string]any)["permissions"])
}

// A role held through a group follows the group's people at once, and joining
// such a group oneself is a gain like a space: explicit, with a reason, audited.
func TestARoleHeldThroughAGroup(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		if _, err := a.auth.CreateUser(context.Background(), auth.CreateParams{Username: "adm", Role: "admin", Password: pw, TenantID: store.DefaultTenantID}); err != nil {
			t.Fatal(err)
		}
		adm := a.newClient("")
		adm.do("POST", "/api/login", map[string]any{"username": "adm", "password": pw}).want(t, 200, "")
		mod := a.addUser("mod", pw, "member")
		modID, admID := mustUserID(t, a, "mod"), mustUserID(t, a, "adm")
		g := root.mkGroup(t, "Auditors", "local")
		grantRole(t, a, "R1", store.RoleScopeTenant, store.DefaultTenantID, store.GrantToGroup, g, "tenant.audit.read")

		if slices.Contains(permissionsOfSession(t, mod), "tenant.audit.read") {
			t.Fatal("held before joining")
		}
		root.groupAdd(t, g, modID).want(t, 200, "")
		if !slices.Contains(permissionsOfSession(t, mod), "tenant.audit.read") {
			t.Fatal("not held at once after joining")
		}
		root.do("DELETE", "/api/admin/groups/"+g+"/members/"+modID, nil).want(t, 204, "")
		if slices.Contains(permissionsOfSession(t, mod), "tenant.audit.read") {
			t.Fatal("still held after leaving")
		}

		adm.groupAdd(t, g, admID).want(t, 403, "takeover_required")
		adm.do("POST", "/api/admin/groups/"+g+"/members", map[string]any{"userIds": []string{admID}, "reason": "audit help"}).want(t, 200, "")
		a.auth.FlushAudit(context.Background())
		rows := root.audit(t, "group_self_added")
		if len(rows) != 1 || !slices.Equal(toStrings(rows[0]["detail"].(map[string]any)["roles"]), []string{"R1"}) {
			t.Fatal(rows)
		}
	})
}

// Who reads and manages people across the site without site.admins.manage
// may not end a site administrator's sessions or disconnect their accounts.
func TestASiteAdministratorsSessionsNeedTheKey(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		a.setup("root", pw)
		ops := a.addUser("ops", pw, "member")
		a.addUser("mem", pw, "member")
		grantRole(t, a, "R1", store.RoleScopeSite, "", store.GrantToUser, mustUserID(t, a, "ops"), "tenant.users.read", "tenant.users.manage")
		rootID := mustUserID(t, a, "root")
		ops.do("GET", "/api/users/"+rootID+"/sessions", nil).want(t, 200, "")
		ops.do("DELETE", "/api/users/"+rootID+"/sessions", nil).want(t, 403, "forbidden")
		ops.do("DELETE", "/api/users/"+rootID+"/sessions/S1", nil).want(t, 403, "forbidden")
		ops.do("DELETE", "/api/users/"+rootID+"/links/I1", nil).want(t, 403, "forbidden")
		ops.do("DELETE", "/api/users/"+mustUserID(t, a, "mem")+"/sessions", nil).want(t, 200, "")
	})
}

func toStrings(v any) []string {
	l, _ := v.([]any)
	out := make([]string, 0, len(l))
	for _, x := range l {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}
