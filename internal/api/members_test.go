package api_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/KKazuhaK/PetalBook/internal/api"
	"github.com/KKazuhaK/PetalBook/internal/perm"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// team is a shared space with one person in each role and one outsider.
type team struct {
	sid                             string
	owner, editor, viewer, outsider *client
	// ids are the user ids of the four.
	ownerID, editorID, viewerID string
}

func newTeam(t *testing.T, a *acc) team {
	t.Helper()
	var tm team
	tm.owner = a.addUser("olive", pw, "member")
	tm.editor = a.addUser("edith", pw, "member")
	tm.viewer = a.addUser("victor", pw, "member")
	tm.outsider = a.addUser("oscar", pw, "member")
	tm.sid = tm.owner.do("POST", "/api/spaces", map[string]any{"name": "Team"}).want(t, 201, "").body["id"].(string)
	base := "/api/spaces/" + tm.sid + "/members"
	tm.owner.do("POST", base, map[string]any{"username": "edith", "role": "editor"}).want(t, 201, "")
	tm.owner.do("POST", base, map[string]any{"username": "victor", "role": "viewer"}).want(t, 201, "")
	tm.ownerID, tm.editorID, tm.viewerID = mustUserID(t, a, "olive"), mustUserID(t, a, "edith"), mustUserID(t, a, "victor")
	return tm
}

func routePath(rt api.RouteInfo, sid string) string {
	return strings.NewReplacer("{space}", sid, "{id}", "01JD3K8Q7Z", "{user}", "01JD3K8Q7Z", "{commit}", "0000000000000000000000000000000000000000",
		"{tag}", "x", "{path...}", "assets/x.png").Replace(rt.Pattern)
}

// Every route of a space asks for a permission, and a role gets exactly what
// its permissions allow. The test is driven by Routes(), so a write route that
// is declared with too small a permission is caught here.
func TestRolesGetWhatTheirPermissionsAllow(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		n := 0
		for _, rt := range api.Routes() {
			if !rt.Space {
				continue
			}
			n++
			if rt.Perm == 0 {
				t.Errorf("%s %s: a space route without a permission", rt.Method, rt.Pattern)
			}
			// Discarding one's own draft is the one change a reader may make: a
			// person who lost the write permission can still throw theirs away.
			if rt.Method != "GET" && rt.Perm == perm.Read && !strings.HasSuffix(rt.Pattern, "/pages/{id}/draft") {
				t.Errorf("%s %s changes something and asks only for read", rt.Method, rt.Pattern)
			}
			path := routePath(rt, tm.sid)
			for who, c := range map[string]*client{"viewer": tm.viewer, "editor": tm.editor, "owner": tm.owner} {
				role := map[string]string{"viewer": perm.Viewer, "editor": perm.Editor, "owner": perm.Owner}[who]
				set, _ := perm.ForRole(role)
				got := c.do(rt.Method, path, "{}", typeFor(rt)...)
				if set.Has(rt.Perm) {
					if got.code == 403 {
						t.Errorf("%s %s as %s: refused: %s", rt.Method, rt.Pattern, who, got.raw)
					}
				} else if got.code != 403 || got.body["error"] != "no_permission" || !strings.Contains(got.raw, rt.Perm.String()) {
					t.Errorf("%s %s as %s: want 403 no_permission naming %q, got %d %s", rt.Method, rt.Pattern, who, rt.Perm, got.code, got.raw)
				}
			}
			if got := tm.outsider.do(rt.Method, path, "{}", typeFor(rt)...); got.code != 404 || got.body["error"] != "not_found" {
				t.Errorf("%s %s as a non-member: %d %s", rt.Method, rt.Pattern, got.code, got.raw)
			}
		}
		if n < 25 {
			t.Fatalf("only %d space routes", n)
		}
		// What a role may do really works, not only passes the gate.
		base := "/api/spaces/" + tm.sid
		p := tm.editor.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "By editor"}, "body": "x\n"}).want(t, 201, "")
		tm.viewer.do("GET", base+"/pages/"+p.body["id"].(string), nil).want(t, 200, "")
		tm.viewer.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "No"}}).want(t, 403, "no_permission")
		tm.editor.do("PATCH", base, map[string]any{"name": "Renamed"}).want(t, 403, "no_permission")
		tm.owner.do("PATCH", base, map[string]any{"name": "Renamed"}).want(t, 200, "")
		// The rename reaches everyone's list at once.
		for _, c := range []*client{tm.editor, tm.viewer} {
			found := false
			for _, s := range c.do("GET", "/api/spaces", nil).body["spaces"].([]any) {
				if s.(map[string]any)["id"] == tm.sid && s.(map[string]any)["name"] == "Renamed" {
					found = true
				}
			}
			if !found {
				t.Fatal("a member does not see the new name")
			}
		}
	})
}

func TestSpaceListSaysWhatYouMayDo(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		want := map[*client][]any{
			tm.owner:  {"owner", "read", "write", "members", "manage"},
			tm.editor: {"editor", "read", "write"},
			tm.viewer: {"viewer", "read"},
		}
		for c, w := range want {
			var got map[string]any
			for _, s := range c.do("GET", "/api/spaces", nil).body["spaces"].([]any) {
				if s.(map[string]any)["id"] == tm.sid {
					got = s.(map[string]any)
				}
			}
			if got == nil || got["role"] != w[0] || fmt.Sprint(got["permissions"]) != fmt.Sprint(w[1:]) {
				t.Errorf("want %v, got %v", w, got)
			}
		}
		// A personal space is the person's alone: all of it, and nothing to share.
		personal := tm.owner.do("GET", "/api/spaces", nil).body["spaces"].([]any)[0].(map[string]any)
		if personal["personal"] != true || len(personal["permissions"].([]any)) != 4 {
			t.Fatalf("%v", personal)
		}
	})
}

func TestAdministratorSeesEverySpace(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		boss := a.setup("kazuha", pw) // an administrator, a member of nothing but their own space
		tm := newTeam(t, a)
		base := "/api/spaces/" + tm.sid
		// All of it works, writing and managing too.
		boss.do("GET", base+"/tree", nil).want(t, 200, "")
		p := boss.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "By admin"}, "body": "x\n"}).want(t, 201, "")
		boss.do("GET", base+"/pages/"+p.body["id"].(string), nil).want(t, 200, "")
		r := boss.do("PATCH", base, map[string]any{"name": "Renamed by admin"}).want(t, 200, "")
		if r.body["role"] != "" || len(r.body["permissions"].([]any)) != 4 {
			t.Fatalf("%s", r.raw)
		}
		boss.do("GET", base+"/members", nil).want(t, 200, "")
		// ... without becoming a member: not in their list, and the members do not change.
		for _, s := range boss.do("GET", "/api/spaces", nil).body["spaces"].([]any) {
			if s.(map[string]any)["id"] == tm.sid {
				t.Fatal("an administrator's list shows a space they are not a member of")
			}
		}
		for _, m := range tm.owner.do("GET", base+"/members", nil).body["members"].([]any) {
			if m.(map[string]any)["username"] == "kazuha" {
				t.Fatal("the administrator became a member")
			}
		}
		// The members see the rename the administrator made.
		if tm.viewer.do("GET", "/api/spaces", nil).raw == "" || !strings.Contains(tm.viewer.do("GET", "/api/spaces", nil).raw, "Renamed by admin") {
			t.Fatal("a member does not see the administrator's rename")
		}
		// A space that is not there is still not there, and a plain member is not an administrator.
		boss.do("GET", "/api/spaces/0000000000/tree", nil).want(t, 404, "not_found")
		boss.do("GET", "/api/spaces/not-an-id/tree", nil).want(t, 404, "not_found")
		tm.outsider.do("GET", base+"/tree", nil).want(t, 404, "not_found")
		// An administrator is a person like any other in a space they do belong to.
		boss.do("POST", base+"/members", map[string]any{"username": "kazuha", "role": "viewer"}).want(t, 201, "")
		boss.do("GET", base+"/tree", nil).want(t, 200, "")
	})
}

func TestMembers(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		base := "/api/spaces/" + tm.sid + "/members"

		// Owners first, then by name; "you" marks the caller.
		r := tm.viewer.do("GET", base, nil).want(t, 200, "")
		var got []string
		for _, m := range r.body["members"].([]any) {
			m := m.(map[string]any)
			got = append(got, fmt.Sprintf("%s/%s/%v", m["username"], m["role"], m["you"]))
			if m["userId"] == "" || m["name"] == nil {
				t.Fatalf("%v", m)
			}
		}
		if fmt.Sprint(got) != "[olive/owner/false edith/editor/false victor/viewer/true]" {
			t.Fatalf("%v", got)
		}

		// Adding.
		tm.editor.do("POST", base, map[string]any{"username": "oscar", "role": "viewer"}).want(t, 403, "no_permission")
		tm.owner.do("POST", base, map[string]any{"username": "edith", "role": "viewer"}).want(t, 409, "exists")
		tm.owner.do("POST", base, map[string]any{"username": "nobody", "role": "viewer"}).want(t, 404, "user_not_found")
		tm.owner.do("POST", base, map[string]any{"username": "oscar", "role": "admin"}).want(t, 400, "invalid")
		tm.owner.do("POST", base, map[string]any{"username": "oscar", "role": ""}).want(t, 400, "invalid")
		// A disabled account cannot be added.
		if err := store.SetDisabled(context.Background(), a.st.Q(), mustUserID(t, a, "oscar"), true); err != nil {
			t.Fatal(err)
		}
		tm.owner.do("POST", base, map[string]any{"username": "oscar", "role": "viewer"}).want(t, 404, "user_not_found")
		if err := store.SetDisabled(context.Background(), a.st.Q(), mustUserID(t, a, "oscar"), false); err != nil {
			t.Fatal(err)
		}
		// The name is matched like at sign-in.
		r = tm.owner.do("POST", base, map[string]any{"username": " Oscar ", "role": "editor"}).want(t, 201, "")
		if r.body["username"] != "oscar" || r.body["role"] != "editor" || r.body["you"] != false {
			t.Fatalf("%s", r.raw)
		}
		tm.outsider.do("GET", "/api/spaces/"+tm.sid+"/tree", nil).want(t, 200, "")

		// Changing a role.
		oscarID := mustUserID(t, a, "oscar")
		tm.editor.do("PATCH", base+"/"+oscarID, map[string]any{"role": "owner"}).want(t, 403, "no_permission")
		tm.owner.do("PATCH", base+"/"+oscarID, map[string]any{"role": "boss"}).want(t, 400, "invalid")
		tm.owner.do("PATCH", base+"/01JD3K8Q7Z", map[string]any{"role": "viewer"}).want(t, 404, "not_found")
		r = tm.owner.do("PATCH", base+"/"+oscarID, map[string]any{"role": "viewer"}).want(t, 200, "")
		if r.body["role"] != "viewer" || r.body["username"] != "oscar" {
			t.Fatalf("%s", r.raw)
		}
		tm.outsider.do("POST", "/api/spaces/"+tm.sid+"/pages", map[string]any{"meta": map[string]any{"title": "No"}}).want(t, 403, "no_permission")

		// Removing, and leaving.
		tm.viewer.do("DELETE", base+"/"+oscarID, nil).want(t, 403, "no_permission")
		tm.owner.do("DELETE", base+"/"+oscarID, nil).want(t, 204, "")
		tm.outsider.do("GET", "/api/spaces/"+tm.sid+"/tree", nil).want(t, 404, "not_found")
		tm.owner.do("DELETE", base+"/"+oscarID, nil).want(t, 404, "not_found")
		tm.viewer.do("DELETE", base+"/"+tm.viewerID, nil).want(t, 204, "") // leaving needs only membership
		tm.viewer.do("GET", "/api/spaces/"+tm.sid+"/tree", nil).want(t, 404, "not_found")
		for _, s := range tm.viewer.do("GET", "/api/spaces", nil).body["spaces"].([]any) {
			if s.(map[string]any)["id"] == tm.sid {
				t.Fatal("a member who left still has the space in their list")
			}
		}
		// An editor may leave too, but not remove someone else.
		tm.owner.do("POST", base, map[string]any{"username": "victor", "role": "viewer"}).want(t, 201, "")
		tm.editor.do("DELETE", base+"/"+tm.viewerID, nil).want(t, 403, "no_permission")
		tm.editor.do("DELETE", base+"/"+tm.editorID, nil).want(t, 204, "")
	})
}

func TestASpaceKeepsAnOwner(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		base := "/api/spaces/" + tm.sid + "/members"
		tm.owner.do("PATCH", base+"/"+tm.ownerID, map[string]any{"role": "editor"}).want(t, 409, "last_owner")
		tm.owner.do("DELETE", base+"/"+tm.ownerID, nil).want(t, 409, "last_owner")
		// Setting the role it already has is no change.
		tm.owner.do("PATCH", base+"/"+tm.ownerID, map[string]any{"role": "owner"}).want(t, 200, "")
		tm.owner.do("PATCH", base+"/"+tm.editorID, map[string]any{"role": "owner"}).want(t, 200, "")
		// With two, one may leave; the other is then the last.
		tm.owner.do("DELETE", base+"/"+tm.ownerID, nil).want(t, 204, "")
		tm.editor.do("DELETE", base+"/"+tm.editorID, nil).want(t, 409, "last_owner")
		tm.editor.do("PATCH", base+"/"+tm.editorID, map[string]any{"role": "viewer"}).want(t, 409, "last_owner")
		// An administrator is no exception.
		boss := a.addUser("kazuha", pw, "admin")
		boss.do("DELETE", base+"/"+tm.editorID, nil).want(t, 409, "last_owner")
	})
}

// Two owners each demoting the other at the same moment: one of them has to lose.
func TestOwnersCannotBothStepDown(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		base := "/api/spaces/" + tm.sid + "/members"
		tm.owner.do("PATCH", base+"/"+tm.editorID, map[string]any{"role": "owner"}).want(t, 200, "")
		for round := 0; round < 5; round++ {
			var wg sync.WaitGroup
			codes := make([]int, 2)
			for i, who := range []struct {
				c  *client
				id string
			}{{tm.owner, tm.editorID}, {tm.editor, tm.ownerID}} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					codes[i] = who.c.do("DELETE", base+"/"+who.id, nil).code
				}()
			}
			wg.Wait()
			// The loser is told the space has gone (404, once removed) or that it
			// needs an owner (409), depending on which got there first.
			ok, lost := 0, 0
			for _, c := range codes {
				switch c {
				case 204:
					ok++
				case 409, 404:
					lost++
				}
			}
			if ok != 1 || lost != 1 {
				t.Fatalf("round %d: %v", round, codes)
			}
			// Whoever is left is the owner; put the other back as an owner.
			survivor, otherName := tm.owner, "edith"
			if codes[0] != 204 { // the owner's removal of the editor failed: the editor removed the owner
				survivor, otherName = tm.editor, "olive"
			}
			survivor.do("POST", base, map[string]any{"username": otherName, "role": "owner"}).want(t, 201, "")
		}
	})
}

func TestPersonalSpaceCannotBeShared(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tang := a.addUser("tang", pw, "member")
		a.addUser("kai", pw, "member")
		sid := tang.personalSpace()
		tang.do("POST", "/api/spaces/"+sid+"/members", map[string]any{"username": "kai", "role": "viewer"}).want(t, 409, "personal_space")
		// An administrator cannot either.
		boss := a.addUser("kazuha", pw, "admin")
		boss.do("POST", "/api/spaces/"+sid+"/members", map[string]any{"username": "kai", "role": "viewer"}).want(t, 409, "personal_space")
		if l := tang.do("GET", "/api/spaces/"+sid+"/members", nil).want(t, 200, "").body["members"].([]any); len(l) != 1 {
			t.Fatalf("%v", l)
		}
	})
}

// The membership cache is dropped for the people a change touches, so the very
// next request of theirs sees it.
func TestMembershipChangesApplyAtOnce(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		base := "/api/spaces/" + tm.sid
		// Warm the cache of both.
		tm.editor.do("GET", base+"/tree", nil).want(t, 200, "")
		tm.viewer.do("GET", base+"/tree", nil).want(t, 200, "")
		tm.editor.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "One"}}).want(t, 201, "")

		tm.owner.do("PATCH", base+"/members/"+tm.editorID, map[string]any{"role": "viewer"}).want(t, 200, "")
		tm.editor.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "Two"}}).want(t, 403, "no_permission")
		tm.editor.do("GET", base+"/tree", nil).want(t, 200, "")

		tm.owner.do("PATCH", base+"/members/"+tm.viewerID, map[string]any{"role": "editor"}).want(t, 200, "")
		tm.viewer.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "Three"}}).want(t, 201, "")

		tm.owner.do("DELETE", base+"/members/"+tm.editorID, nil).want(t, 204, "")
		tm.editor.do("GET", base+"/tree", nil).want(t, 404, "not_found")
		tm.editor.do("GET", base+"/pages", nil).want(t, 404, "not_found")
		// The other member was not touched.
		tm.viewer.do("GET", base+"/tree", nil).want(t, 200, "")
	})
}

// Only owner roles count against the limit on how many spaces a person may own.
func TestMembershipDoesNotCountAgainstTheSpaceLimit(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{maxSpaces: 2}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		// edith owns her personal space only; being an editor of Team is not owning it.
		tm.editor.do("POST", "/api/spaces", map[string]any{"name": "Mine"}).want(t, 201, "")
		tm.editor.do("POST", "/api/spaces", map[string]any{"name": "Too many"}).want(t, 403, "forbidden")
		// Becoming an owner of one counts.
		tm.owner.do("PATCH", "/api/spaces/"+tm.sid+"/members/"+tm.viewerID, map[string]any{"role": "owner"}).want(t, 200, "")
		tm.viewer.do("POST", "/api/spaces", map[string]any{"name": "More"}).want(t, 403, "forbidden")
	})
}

// What the server did about a settings file it could not use is told to those
// who may manage the space, and to nobody else.
func TestSettingsNoticesAreForThoseWhoManageTheSpace(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		base := "/api/spaces/" + tm.sid
		tm.editor.do("POST", base+"/folders", map[string]any{"path": "F"}).want(t, 201, "")
		file := filepath.Join(a.dir, "spaces", tm.sid, ".petalbook", "space.yml")
		if err := os.WriteFile(file, []byte("folders: [unclosed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		for who, c := range map[string]*client{"owner": tm.owner, "editor": tm.editor, "viewer": tm.viewer} {
			r := c.do("GET", base+"/types", nil).want(t, 200, "")
			list, has := r.body["settingsNotices"]
			if who == "owner" {
				if l, _ := list.([]any); len(l) != 1 || l[0].(map[string]any)["unreadable"] != true {
					t.Errorf("the owner is told %v", list)
				}
			} else if has {
				t.Errorf("the %s is told %v", who, list)
			}
		}
		// Everyone goes on working all the same.
		tm.editor.do("POST", base+"/pages", map[string]any{"folder": "F", "meta": map[string]any{"title": "one"}}).want(t, 201, "")
	})
}
