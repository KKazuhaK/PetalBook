package api_test

import (
	"fmt"
	"strings"
	"testing"
)

func gid(r result) string { return r.body["id"].(string) }

func (c *client) mkGroup(t *testing.T, name, source string) string {
	t.Helper()
	return gid(c.do("POST", "/api/admin/groups", map[string]any{"name": name, "source": source}).want(t, 201, ""))
}

func (c *client) groupAdd(t *testing.T, g string, ids ...string) result {
	t.Helper()
	return c.do("POST", "/api/admin/groups/"+g+"/members", map[string]any{"userIds": ids})
}

func TestAdminGroups(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		ann := a.addUser("ann", pw, "member")
		a.addUser("bob", pw, "member")
		annID, bobID := mustUserID(t, a, "ann"), mustUserID(t, a, "bob")

		// Only administrators; a visitor is not signed in.
		ann.do("GET", "/api/admin/groups", nil).want(t, 403, "forbidden")
		ann.do("POST", "/api/admin/groups", map[string]any{"name": "x", "source": "local"}).want(t, 403, "forbidden")
		a.newClient("").do("GET", "/api/admin/groups", nil).want(t, 401, "unauthenticated")

		// Making one.
		r := admin.do("POST", "/api/admin/groups", map[string]any{"name": "  Engineering ", "description": " The builders ", "source": "local"}).want(t, 201, "")
		eng := gid(r)
		if r.body["name"] != "Engineering" || r.body["description"] != "The builders" || r.body["source"] == "" || r.body["people"] != float64(0) ||
			r.body["spaces"] != float64(0) || len(r.body["mappedFrom"].([]any)) != 0 || !rfc3339.MatchString(r.body["created"].(string)) {
			t.Fatal(r.raw)
		}
		syn := admin.mkGroup(t, "Staff", "synced")
		for name, body := range map[string]map[string]any{
			"no source":    {"name": "x"},
			"bad source":   {"name": "x", "source": "both"},
			"empty name":   {"name": " ", "source": "local"},
			"long name":    {"name": strings.Repeat("n", 81), "source": "local"},
			"long details": {"name": "x", "source": "local", "description": strings.Repeat("d", 501)},
		} {
			if r := admin.do("POST", "/api/admin/groups", body); r.code != 400 || r.body["error"] != "invalid" {
				t.Errorf("%s: %d %s", name, r.code, r.raw)
			}
		}
		// Names are unique ignoring case.
		admin.do("POST", "/api/admin/groups", map[string]any{"name": "ENGINEERING", "source": "synced"}).want(t, 409, "exists")

		// Reading, changing.
		admin.do("GET", "/api/admin/groups/"+eng, nil).want(t, 200, "")
		admin.do("GET", "/api/admin/groups/0000000000", nil).want(t, 404, "not_found")
		admin.do("PATCH", "/api/admin/groups/"+eng, map[string]any{"source": "synced"}).want(t, 400, "invalid")
		admin.do("PATCH", "/api/admin/groups/"+eng, map[string]any{"name": "staff"}).want(t, 409, "exists")
		r = admin.do("PATCH", "/api/admin/groups/"+eng, map[string]any{"name": "Platform"}).want(t, 200, "")
		if r.body["name"] != "Platform" || r.body["description"] != "The builders" {
			t.Fatal(r.raw)
		}
		admin.do("PATCH", "/api/admin/groups/0000000000", map[string]any{"name": "z"}).want(t, 404, "not_found")

		// People: local groups only, all or nothing, 1 to 500.
		admin.groupAdd(t, syn, annID).want(t, 409, "group_synced")
		admin.do("DELETE", "/api/admin/groups/"+syn+"/members/"+annID, nil).want(t, 409, "group_synced")
		admin.groupAdd(t, "0000000000", annID).want(t, 404, "not_found")
		admin.groupAdd(t, eng).want(t, 400, "invalid")
		many := make([]string, 501)
		for i := range many {
			many[i] = fmt.Sprintf("u%d", i)
		}
		admin.groupAdd(t, eng, many...).want(t, 400, "invalid")
		r = admin.groupAdd(t, eng, annID, "ghost-1", "ghost-2").want(t, 404, "user_not_found")
		if m := r.body["missing"].([]any); len(m) != 2 {
			t.Fatal(r.raw)
		}
		if g := admin.do("GET", "/api/admin/groups/"+eng, nil).body; g["people"] != float64(0) {
			t.Fatalf("a refused request added people: %v", g)
		}
		r = admin.groupAdd(t, eng, annID, bobID, annID).want(t, 200, "")
		if r.body["added"] != float64(2) || r.body["already"] != float64(0) {
			t.Fatal(r.raw)
		}
		r = admin.groupAdd(t, eng, annID).want(t, 200, "")
		if r.body["added"] != float64(0) || r.body["already"] != float64(1) {
			t.Fatal(r.raw)
		}
		ppl := admin.do("GET", "/api/admin/groups/"+eng+"/members?limit=1", nil).want(t, 200, "")
		items := ppl.body["items"].([]any)
		if len(items) != 1 || ppl.body["next"] != "ann" || items[0].(map[string]any)["username"] != "ann" || len(items[0].(map[string]any)["from"].([]any)) != 0 || items[0].(map[string]any)["disabled"] != false {
			t.Fatal(ppl.raw)
		}
		ppl = admin.do("GET", "/api/admin/groups/"+eng+"/members?cursor=ann&limit=5", nil).want(t, 200, "")
		if items = ppl.body["items"].([]any); len(items) != 1 || ppl.body["next"] != "" {
			t.Fatal(ppl.raw)
		}
		admin.do("GET", "/api/admin/groups/"+eng+"/members?limit=201", nil).want(t, 400, "invalid")

		// An account sees its groups, and the admin finds a person's groups by filter.
		acct := ann.do("GET", "/api/account", nil).want(t, 200, "")
		gs := acct.body["groups"].([]any)
		if len(gs) != 1 || gs[0].(map[string]any)["name"] != "Platform" || gs[0].(map[string]any)["source"] != "local" || len(gs[0].(map[string]any)) != 3 {
			t.Fatal(acct.raw)
		}
		l := admin.do("GET", "/api/admin/groups?user="+annID, nil).want(t, 200, "")
		if it := l.body["items"].([]any); len(it) != 1 || it[0].(map[string]any)["people"] != float64(2) {
			t.Fatal(l.raw)
		}

		// The list: by name, filtered, paged.
		for _, n := range []string{"Alpha", "Beta", "Gamma"} {
			admin.mkGroup(t, n, "local")
		}
		var names []string
		cursor := ""
		for {
			r := admin.do("GET", "/api/admin/groups?limit=2&cursor="+cursor, nil).want(t, 200, "")
			for _, it := range r.body["items"].([]any) {
				names = append(names, it.(map[string]any)["name"].(string))
			}
			if cursor = r.body["next"].(string); cursor == "" {
				break
			}
		}
		if fmt.Sprint(names) != "[Alpha Beta Gamma Platform Staff]" {
			t.Fatal(names)
		}
		if r := admin.do("GET", "/api/admin/groups?q=ALP&source=local", nil).want(t, 200, ""); len(r.body["items"].([]any)) != 1 {
			t.Fatal(r.raw)
		}
		if r := admin.do("GET", "/api/admin/groups?source=synced", nil).want(t, 200, ""); len(r.body["items"].([]any)) != 1 {
			t.Fatal(r.raw)
		}
		admin.do("GET", "/api/admin/groups?source=both", nil).want(t, 400, "invalid")
		admin.do("GET", "/api/admin/groups?limit=0", nil).want(t, 400, "invalid")

		// Taking a person out.
		admin.do("DELETE", "/api/admin/groups/"+eng+"/members/"+bobID, nil).want(t, 204, "")
		admin.do("DELETE", "/api/admin/groups/"+eng+"/members/"+bobID, nil).want(t, 404, "not_found")

		// A group in a space is not deleted without force; with it, nothing else goes.
		sid := ann.do("POST", "/api/spaces", map[string]any{"name": "Team"}).want(t, 201, "").body["id"].(string)
		ann.do("POST", spacePath(sid)+"/groups", map[string]any{"groupId": eng, "role": "viewer"}).want(t, 201, "")
		sp := admin.do("GET", "/api/admin/groups/"+eng+"/spaces", nil).want(t, 200, "")
		if it := sp.body["items"].([]any); len(it) != 1 || it[0].(map[string]any)["id"] != sid || it[0].(map[string]any)["role"] != "viewer" || it[0].(map[string]any)["name"] != "Team" {
			t.Fatal(sp.raw)
		}
		r = admin.do("DELETE", "/api/admin/groups/"+eng, nil).want(t, 409, "group_in_use")
		if r.body["spaces"] != float64(1) || r.body["mappings"] != float64(0) {
			t.Fatal(r.raw)
		}
		admin.do("DELETE", "/api/admin/groups/"+eng+"?force=2", nil).want(t, 400, "invalid")
		admin.do("DELETE", "/api/admin/groups/"+eng+"?force=1", nil).want(t, 204, "")
		admin.do("GET", "/api/admin/groups/"+eng, nil).want(t, 404, "not_found")
		admin.do("GET", "/api/admin/groups/"+eng+"/spaces", nil).want(t, 404, "not_found")
		ann.do("GET", spacePath(sid)+"/members", nil).want(t, 200, "")
		if g := ann.do("GET", "/api/account", nil).body["groups"].([]any); len(g) != 0 {
			t.Fatal(g)
		}
		// An unused group goes without force.
		admin.do("DELETE", "/api/admin/groups/"+syn, nil).want(t, 204, "")

		// The audit log has every step, by whom.
		want := map[string]int{"group_created": 5, "group_changed": 1, "group_member_added": 1, "group_member_removed": 1, "space_group_added": 1, "group_deleted": 2}
		for kind, n := range want {
			rows := admin.audit(t, kind)
			if len(rows) != n {
				t.Errorf("%s: %d rows, want %d", kind, len(rows), n)
				continue
			}
			if rows[0]["actor"] == nil {
				t.Errorf("%s has no actor", kind)
			}
		}
		del := admin.audit(t, "group_deleted")
		d := del[1]["detail"].(map[string]any) // oldest first is the forced one
		if d["name"] != "Platform" || d["people"] != float64(1) || d["spaces"] != float64(1) || d["source"] != "local" {
			t.Fatalf("%v", del)
		}
		add := admin.audit(t, "group_member_added")
		if ad := add[len(add)-1]["detail"].(map[string]any); len(ad["users"].([]any)) != 2 {
			t.Fatalf("%v", add)
		}
	})
}

func TestGroupsInSpaces(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tm := newTeam(t, a) // olive owns Team, edith edits, victor views, oscar is outside
		ann := a.addUser("ann", pw, "member")
		annID := mustUserID(t, a, "ann")
		eng := admin.mkGroup(t, "Engineering", "local")
		ops := admin.mkGroup(t, "Operations", "local")
		admin.groupAdd(t, eng, annID, tm.viewerID).want(t, 200, "")
		admin.groupAdd(t, ops, mustUserID(t, a, "oscar")).want(t, 200, "")
		base := spacePath(tm.sid)

		// Only whoever may change members; groups that exist.
		tm.editor.do("POST", base+"/groups", map[string]any{"groupId": eng, "role": "viewer"}).want(t, 403, "no_permission")
		tm.outsider.do("POST", base+"/groups", map[string]any{"groupId": eng, "role": "viewer"}).want(t, 404, "not_found")
		tm.owner.do("POST", base+"/groups", map[string]any{"groupId": "0000000000", "role": "viewer"}).want(t, 404, "group_not_found")
		tm.owner.do("POST", base+"/groups", map[string]any{"groupId": eng, "role": "chief"}).want(t, 400, "invalid")
		tm.owner.do("POST", base+"/groups", map[string]any{"groupId": eng}).want(t, 400, "invalid")
		tm.owner.do("POST", tm.owner.personalSpaceBase()+"/groups", map[string]any{"groupId": eng, "role": "viewer"}).want(t, 409, "personal_space")

		// ann reaches nothing yet.
		ann.do("GET", base+"/members", nil).want(t, 404, "not_found")
		r := tm.owner.do("POST", base+"/groups", map[string]any{"groupId": eng, "role": "editor"}).want(t, 201, "")
		if r.body["groupId"] != eng || r.body["name"] != "Engineering" || r.body["source"] != "local" || r.body["role"] != "editor" || r.body["people"] != float64(2) || r.body["yours"] != false {
			t.Fatal(r.raw)
		}
		tm.owner.do("POST", base+"/groups", map[string]any{"groupId": eng, "role": "viewer"}).want(t, 409, "exists")

		// Very next request: ann is an editor through the group.
		sp := ann.do("GET", "/api/spaces", nil).want(t, 200, "")
		var team map[string]any
		for _, s := range sp.body["spaces"].([]any) {
			if s.(map[string]any)["id"] == tm.sid {
				team = s.(map[string]any)
			}
		}
		if team == nil || team["role"] != "editor" || team["directRole"] != "" || team["viaGroups"] != true || len(team["permissions"].([]any)) != 2 {
			t.Fatalf("%s", sp.raw)
		}
		newPage(t, ann, tm.sid, "By ann")
		ann.do("POST", base+"/members", map[string]any{"username": "oscar", "role": "viewer"}).want(t, 403, "no_permission")

		// victor is a direct viewer and an editor through the group: the union.
		vs := tm.viewer.do("GET", "/api/spaces", nil).want(t, 200, "")
		for _, s := range vs.body["spaces"].([]any) {
			if m := s.(map[string]any); m["id"] == tm.sid && (m["role"] != "editor" || m["directRole"] != "viewer" || m["viaGroups"] != true) {
				t.Fatalf("%s", vs.raw)
			}
		}
		newPage(t, tm.viewer, tm.sid, "By victor")

		// The members list: direct members as before, plus groups.
		ml := ann.do("GET", base+"/members", nil).want(t, 200, "")
		members, groups := ml.body["members"].([]any), ml.body["groups"].([]any)
		if len(members) != 3 || len(groups) != 1 {
			t.Fatal(ml.raw)
		}
		g0 := groups[0].(map[string]any)
		if g0["name"] != "Engineering" || g0["people"] != float64(2) || g0["yours"] != true || g0["role"] != "editor" {
			t.Fatal(ml.raw)
		}
		for _, m := range members {
			mm := m.(map[string]any)
			if mm["username"] == "victor" && (mm["viaGroups"] != true || mm["effectiveRole"] != "editor" || mm["role"] != "viewer") {
				t.Fatal(ml.raw)
			}
			if mm["username"] == "edith" && (mm["viaGroups"] != false || mm["effectiveRole"] != "editor") {
				t.Fatal(ml.raw)
			}
		}
		if g := tm.owner.do("GET", base+"/members", nil).body["groups"].([]any); g[0].(map[string]any)["yours"] != false {
			t.Fatal(g)
		}

		// Effective members: everyone who can reach it, with how.
		ef := tm.viewer.do("GET", base+"/members/effective?limit=2", nil).want(t, 200, "")
		items := ef.body["items"].([]any)
		if len(items) != 2 || ef.body["next"] != "edith" || items[0].(map[string]any)["username"] != "ann" {
			t.Fatal(ef.raw)
		}
		a0 := items[0].(map[string]any)
		if a0["role"] != "editor" || a0["directRole"] != "" || len(a0["groups"].([]any)) != 1 || a0["groups"].([]any)[0].(map[string]any)["name"] != "Engineering" || a0["you"] != false {
			t.Fatal(ef.raw)
		}
		ef = tm.viewer.do("GET", base+"/members/effective?cursor=edith&limit=10", nil).want(t, 200, "")
		var seen []string
		for _, it := range ef.body["items"].([]any) {
			m := it.(map[string]any)
			seen = append(seen, m["username"].(string))
			if m["username"] == "victor" && (m["you"] != true || m["directRole"] != "viewer" || m["role"] != "editor") {
				t.Fatal(ef.raw)
			}
		}
		if fmt.Sprint(seen) != "[olive victor]" || ef.body["next"] != "" {
			t.Fatal(ef.raw)
		}
		if r := tm.viewer.do("GET", base+"/members/effective?q=ANN", nil).want(t, 200, ""); len(r.body["items"].([]any)) != 1 {
			t.Fatal(r.raw)
		}
		tm.outsider.do("GET", base+"/members/effective", nil).want(t, 404, "not_found")
		tm.viewer.do("GET", base+"/members/effective?limit=0", nil).want(t, 400, "invalid")
		// A disabled person is left out.
		admin.do("PATCH", "/api/users/"+annID, map[string]any{"disabled": true}).want(t, 200, "")
		if r := tm.viewer.do("GET", base+"/members/effective?q=ann", nil).want(t, 200, ""); len(r.body["items"].([]any)) != 0 {
			t.Fatal(r.raw)
		}
		admin.do("PATCH", "/api/users/"+annID, map[string]any{"disabled": false}).want(t, 200, "")
		ann = a.newClient("")
		ann.do("POST", "/api/login", map[string]any{"identifier": "ann", "password": pw}).want(t, 200, "")

		// Changing the role, the next request.
		tm.owner.do("PATCH", base+"/groups/"+eng, map[string]any{"role": "viewer"}).want(t, 200, "")
		ann.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "no"}, "body": "x\n"}).want(t, 403, "no_permission")
		tm.owner.do("PATCH", base+"/groups/"+eng, map[string]any{"role": "chief"}).want(t, 400, "invalid")
		tm.owner.do("PATCH", base+"/groups/"+ops, map[string]any{"role": "viewer"}).want(t, 404, "not_found")
		tm.editor.do("PATCH", base+"/groups/"+eng, map[string]any{"role": "owner"}).want(t, 403, "no_permission")

		// Leaving: a person who is only in through a group cannot; one who is both can.
		r = ann.do("DELETE", base+"/members/"+annID, nil).want(t, 409, "member_via_group")
		if !strings.Contains(r.body["message"].(string), "Engineering") {
			t.Fatal(r.raw)
		}
		tm.viewer.do("DELETE", base+"/members/"+tm.viewerID, nil).want(t, 204, "")
		vs = tm.viewer.do("GET", "/api/spaces", nil).want(t, 200, "")
		for _, s := range vs.body["spaces"].([]any) {
			if m := s.(map[string]any); m["id"] == tm.sid && (m["role"] != "viewer" || m["directRole"] != "") {
				t.Fatalf("%s", vs.raw)
			}
		}
		// A group of owners does not let the last direct owner go.
		tm.owner.do("PATCH", base+"/groups/"+eng, map[string]any{"role": "owner"}).want(t, 200, "")
		tm.owner.do("PATCH", base+"/members/"+tm.editorID, map[string]any{"role": "viewer"}).want(t, 200, "")
		tm.owner.do("DELETE", base+"/members/"+tm.ownerID, nil).want(t, 409, "last_owner")
		tm.owner.do("PATCH", base+"/members/"+tm.ownerID, map[string]any{"role": "editor"}).want(t, 409, "last_owner")
		ann.do("PATCH", base+"/members/"+tm.ownerID, map[string]any{"role": "editor"}).want(t, 409, "last_owner") // ann may manage members through the group

		// Removed from the space, and out of the group.
		tm.owner.do("DELETE", base+"/groups/"+eng, nil).want(t, 204, "")
		ann.do("GET", base+"/members", nil).want(t, 404, "not_found")
		tm.owner.do("DELETE", base+"/groups/"+eng, nil).want(t, 404, "not_found")

		// Finding a group by name: any signed-in person, names only.
		fg := ann.do("GET", "/api/groups?q=ENGI", nil).want(t, 200, "")
		l := fg.body["groups"].([]any)
		if len(l) != 1 || len(l[0].(map[string]any)) != 3 || l[0].(map[string]any)["id"] != eng {
			t.Fatal(fg.raw)
		}
		ann.do("GET", "/api/groups", nil).want(t, 400, "invalid")
		ann.do("GET", "/api/groups?q="+strings.Repeat("x", 81), nil).want(t, 400, "invalid")
		ann.do("GET", "/api/groups?q=o&limit=21", nil).want(t, 400, "invalid")
		if r := ann.do("GET", "/api/groups?q=o&limit=1", nil).want(t, 200, ""); len(r.body["groups"].([]any)) != 1 {
			t.Fatal(r.raw)
		}
		a.newClient("").do("GET", "/api/groups?q=o", nil).want(t, 401, "unauthenticated")

		// The audit rows.
		for kind, n := range map[string]int{"space_group_added": 1, "space_group_changed": 2, "space_group_removed": 1} {
			rows := admin.audit(t, kind)
			if len(rows) != n {
				t.Errorf("%s: %d, want %d", kind, len(rows), n)
			}
		}
		ch := admin.audit(t, "space_group_changed")[0]["detail"].(map[string]any)
		if ch["space"] != tm.sid || ch["group"] != eng || ch["role"].(map[string]any)["old"] != "viewer" || ch["role"].(map[string]any)["new"] != "owner" {
			t.Fatalf("%v", ch)
		}
	})
}

// personalSpaceBase is the path of the client's personal space.
func (c *client) personalSpaceBase() string { return spacePath(c.personalSpace()) }

func TestGroupsDoNotExistInSingleUserMode(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for _, p := range []string{"/api/groups?q=x", "/api/admin/groups", "/api/admin/groups/x", "/api/spaces/default/members/effective"} {
		if code, _, raw := f.do("GET", p, nil); code != 404 {
			t.Errorf("%s: %d %s", p, code, raw)
		}
	}
	if code, _, raw := f.do("POST", "/api/spaces/default/groups", map[string]any{"groupId": "x", "role": "viewer"}); code != 404 {
		t.Errorf("%d %s", code, raw)
	}
}

// A token (and an OAuth grant) is never more than its owner has in the space
// now, and now includes groups: it gains access when the person joins a group
// of the space, and loses it when they leave or the group does.
func TestTokensAndGrantsFollowGroupMembership(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tm := newTeam(t, a)
		ann := a.addUser("ann", pw, "member")
		annID := mustUserID(t, a, "ann")
		eng := admin.mkGroup(t, "Engineering", "local")
		base := spacePath(tm.sid)
		p := newPage(t, tm.owner, tm.sid, "Seed")
		upd := func(rev any, text string) map[string]any {
			return map[string]any{"space": tm.sid, "id": p["id"], "baseRev": rev, "body": text}
		}

		// ann is in no space of Team: a token cannot name it. (A space reached only
		// through a group can be named, once she gets there.)
		ann.do("POST", "/api/account/tokens", map[string]any{"name": "x", "access": "write", "spaces": []string{tm.sid}}).want(t, 404, "not_found")
		admin.groupAdd(t, eng, annID).want(t, 200, "")
		tm.owner.do("POST", base+"/groups", map[string]any{"groupId": eng, "role": "editor"}).want(t, 201, "")
		_, secret := mkToken(t, ann, "write", nil, tm.sid)
		tok := a.bearer(secret, "")
		gr := a.bearer(grant(t, a, ann, "write", tm.sid).access, "")

		tok.do("GET", base+"/pages", nil).want(t, 200, "")
		page := tok.do("GET", base+"/pages/"+p["id"].(string), nil).want(t, 200, "")
		tok.do("PUT", base+"/pages/"+p["id"].(string), map[string]any{"baseRev": page.body["rev"], "meta": page.body["meta"], "body": "by token\n"}).want(t, 200, "")
		tool := gr.tool("page_get", map[string]any{"space": tm.sid, "id": p["id"]}).ok(t)
		if l := gr.tool("spaces_list", nil).ok(t).out["spaces"].([]any); len(l) != 1 {
			t.Fatalf("%v", l)
		}

		// The group's role drops to viewer: the token is a viewer's.
		tm.owner.do("PATCH", base+"/groups/"+eng, map[string]any{"role": "viewer"}).want(t, 200, "")
		tok.do("GET", base+"/pages", nil).want(t, 200, "")
		tok.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "n"}, "body": "x\n"}).want(t, 403, "no_permission")
		gr.tool("page_update", upd(tool.out["rev"], "x\n")).refused(t, "no_permission")
		tm.owner.do("PATCH", base+"/groups/"+eng, map[string]any{"role": "editor"}).want(t, 200, "")
		tok.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "n"}, "body": "x\n"}).want(t, 201, "")

		// She leaves the group: gone, as for a removed member.
		admin.do("DELETE", "/api/admin/groups/"+eng+"/members/"+annID, nil).want(t, 204, "")
		tok.do("GET", base+"/pages", nil).want(t, 404, "not_found")
		gr.tool("page_get", map[string]any{"space": tm.sid, "id": p["id"]}).refused(t, "not_found")
		if l := gr.tool("spaces_list", nil).ok(t).out["spaces"].([]any); len(l) != 0 {
			t.Fatalf("%v", l)
		}
		// She joins again: the same token and grant work.
		admin.groupAdd(t, eng, annID).want(t, 200, "")
		tok.do("GET", base+"/pages", nil).want(t, 200, "")
		gr.tool("page_get", map[string]any{"space": tm.sid, "id": p["id"]}).ok(t)
		// The group leaves the space.
		tm.owner.do("DELETE", base+"/groups/"+eng, nil).want(t, 204, "")
		tok.do("GET", base+"/pages", nil).want(t, 404, "not_found")
		gr.tool("page_get", map[string]any{"space": tm.sid, "id": p["id"]}).refused(t, "not_found")
		// And the group deleted while in the space.
		tm.owner.do("POST", base+"/groups", map[string]any{"groupId": eng, "role": "editor"}).want(t, 201, "")
		tok.do("GET", base+"/pages", nil).want(t, 200, "")
		admin.do("DELETE", "/api/admin/groups/"+eng+"?force=1", nil).want(t, 204, "")
		tok.do("GET", base+"/pages", nil).want(t, 404, "not_found")
	})
}

// A draft waiting to be written is written only while its author may write,
// and through a group they may.
func TestSweptDraftsFollowGroupRoles(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tm := newTeam(t, a)
		a.addUser("ann", pw, "member")
		annID := mustUserID(t, a, "ann")
		eng := admin.mkGroup(t, "Engineering", "local")
		admin.groupAdd(t, eng, annID).want(t, 200, "")
		tm.owner.do("POST", spacePath(tm.sid)+"/groups", map[string]any{"groupId": eng, "role": "editor"}).want(t, 201, "")
		if ok, err := a.mgr.MayWrite(t.Context(), annID, tm.sid); err != nil || !ok {
			t.Fatal(ok, err)
		}
		tm.owner.do("PATCH", spacePath(tm.sid)+"/groups/"+eng, map[string]any{"role": "viewer"}).want(t, 200, "")
		if ok, _ := a.mgr.MayWrite(t.Context(), annID, tm.sid); ok {
			t.Fatal("a viewer through a group may write")
		}
	})
}

// The audit row of an add names only the people it added, not those who were
// in the group already.
func TestGroupMemberAddedAuditListsOnlyTheAdded(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		a.addUser("ann", pw, "member")
		a.addUser("bob", pw, "member")
		annID, bobID := mustUserID(t, a, "ann"), mustUserID(t, a, "bob")
		g := admin.mkGroup(t, "Eng", "local")
		admin.groupAdd(t, g, annID).want(t, 200, "")
		r := admin.groupAdd(t, g, annID, bobID).want(t, 200, "")
		if r.body["added"] != float64(1) || r.body["already"] != float64(1) {
			t.Fatal(r.raw)
		}
		rows := admin.audit(t, "group_member_added")
		users := rows[0]["detail"].(map[string]any)["users"].([]any) // newest first
		if len(users) != 1 || users[0] != bobID {
			t.Fatalf("users %v", users)
		}
	})
}
