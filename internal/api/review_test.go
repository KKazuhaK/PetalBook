package api_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KKazuhaK/PetalBook/internal/store"
)

// A member who was made a viewer keeps the right to throw their own draft away.
func TestAViewerMayDiscardTheirOwnDraft(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		base := "/api/spaces/" + tm.sid
		p := tm.owner.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "Shared"}, "body": "x\n"}).want(t, 201, "")
		url := base + "/pages/" + p.body["id"].(string)
		tm.editor.draft(url, p.body["rev"], "Shared", "typed\n").want(t, 200, "")
		tm.owner.do("PATCH", base+"/members/"+tm.editorID, map[string]any{"role": "viewer"}).want(t, 200, "")
		tm.editor.draft(url, p.body["rev"], "Shared", "more\n").want(t, 403, "no_permission")
		tm.editor.do("DELETE", url+"/draft", nil).want(t, 204, "")
		var n int
		a.st.Q().QueryRow(context.Background(), `SELECT COUNT(*) FROM drafts WHERE space_id = ?`, tm.sid).Scan(&n)
		if n != 0 {
			t.Fatalf("%d drafts left", n)
		}
		// A non-member still learns nothing.
		tm.outsider.do("DELETE", url+"/draft", nil).want(t, 404, "not_found")
	})
}

// Members can leave a space that an administrator has switched off.
func TestAMemberMayLeaveADisabledSpace(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tm := newTeam(t, a)
		base := "/api/spaces/" + tm.sid
		admin.do("POST", "/api/admin/spaces/"+tm.sid+"/disable", "{}").want(t, 204, "")
		tm.viewer.do("GET", base+"/tree", nil).want(t, 403, "space_disabled")
		tm.viewer.do("DELETE", base+"/members/"+tm.viewerID, nil).want(t, 204, "")
		for _, s := range tm.viewer.do("GET", "/api/spaces", nil).body["spaces"].([]any) {
			if s.(map[string]any)["id"] == tm.sid {
				t.Fatal("a member who left still has the space in their list")
			}
		}
		// Others still cannot be removed through a space that cannot be opened.
		tm.owner.do("DELETE", base+"/members/"+tm.editorID, nil).want(t, 403, "space_disabled")
	})
}

// A disabled user cannot be the one remaining owner.
func TestTheLastEnabledOwnerStays(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		base := "/api/spaces/" + tm.sid + "/members"
		tm.owner.do("PATCH", base+"/"+tm.editorID, map[string]any{"role": "owner"}).want(t, 200, "")
		if err := store.SetDisabled(context.Background(), a.st.Q(), tm.ownerID, true); err != nil {
			t.Fatal(err)
		}
		// Edith is the only owner who can sign in: she cannot step down or leave.
		tm.editor.do("PATCH", base+"/"+tm.editorID, map[string]any{"role": "editor"}).want(t, 409, "last_owner")
		tm.editor.do("DELETE", base+"/"+tm.editorID, nil).want(t, 409, "last_owner")
		// The disabled owner may be removed: someone else is left.
		tm.editor.do("DELETE", base+"/"+tm.ownerID, nil).want(t, 204, "")
	})
}

// Deleting a space keeps what members had typed and not saved, next to the
// directory in the trash; if it cannot be kept, nothing is deleted.
func TestAdminDeleteKeepsTheDraftsInTheTrash(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tm := newTeam(t, a)
		base := "/api/spaces/" + tm.sid
		p := tm.editor.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "Held"}, "body": "x\n"}).want(t, 201, "")
		url := base + "/pages/" + p.body["id"].(string)
		tm.editor.draft(url, p.body["rev"], "Held", "unsaved text\n").want(t, 200, "")
		tm.owner.draft(url, p.body["rev"], "Held", "the owner's text\n").want(t, 200, "")
		a.st.Q().Exec(context.Background(), `UPDATE drafts SET held = 1 WHERE user_id = ?`, tm.ownerID)

		// The trash cannot be made: refused, nothing changed.
		if err := os.WriteFile(filepath.Join(a.dir, "trash"), []byte("in the way"), 0o644); err != nil {
			t.Fatal(err)
		}
		if r := admin.do("DELETE", "/api/admin/spaces/"+tm.sid, nil); r.code < 400 {
			t.Fatalf("the space was deleted without keeping its drafts: %d %s", r.code, r.raw)
		}
		tm.owner.do("GET", base+"/tree", nil).want(t, 200, "")
		var n int
		a.st.Q().QueryRow(context.Background(), `SELECT COUNT(*) FROM drafts WHERE space_id = ?`, tm.sid).Scan(&n)
		if n != 2 {
			t.Fatalf("%d drafts after a refused delete", n)
		}
		if err := os.Remove(filepath.Join(a.dir, "trash")); err != nil {
			t.Fatal(err)
		}

		r := admin.do("DELETE", "/api/admin/spaces/"+tm.sid, nil).want(t, 200, "")
		if len(r.body["warnings"].([]any)) != 0 {
			t.Fatal(r.raw)
		}
		files, _ := filepath.Glob(filepath.Join(a.dir, "trash", tm.sid+"-*.drafts.json"))
		if len(files) != 1 {
			t.Fatalf("drafts files: %v", files)
		}
		b, err := os.ReadFile(files[0])
		if err != nil {
			t.Fatal(err)
		}
		var got []struct {
			Space, Page, UserID, Username, Body string
			Held                                bool
			Meta                                map[string]any
		}
		if err := json.Unmarshal(b, &got); err != nil || len(got) != 2 {
			t.Fatalf("%v %s", err, b)
		}
		seen := map[string]bool{}
		for _, d := range got {
			seen[d.Username+"|"+strings.TrimSpace(d.Body)] = true
			if d.Space != tm.sid || d.Page != p.body["id"] || d.Meta["title"] != "Held" || d.UserID == "" || d.Held != (d.Username == "olive") {
				t.Fatalf("%+v", d)
			}
		}
		if !seen["edith|unsaved text"] || !seen["olive|the owner's text"] {
			t.Fatalf("%v", seen)
		}
	})
}

// Disabling a user, and changing their role, is seen by the drafts sweeper at once.
func TestUserChangesReachTheWriteCheckAtOnce(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tm := newTeam(t, a)
		may := func() bool {
			ok, err := a.mgr.MayWrite(context.Background(), tm.editorID, tm.sid)
			if err != nil {
				t.Fatal(err)
			}
			return ok
		}
		if !may() {
			t.Fatal("an editor may not write")
		}
		admin.do("PATCH", "/api/users/"+tm.editorID, map[string]any{"disabled": true}).want(t, 200, "")
		if may() {
			t.Fatal("a disabled user may still write")
		}
		admin.do("PATCH", "/api/users/"+tm.editorID, map[string]any{"disabled": false}).want(t, 200, "")
		if !may() {
			t.Fatal("an enabled user may not write")
		}
		// An administrator may write in every space; the change from member shows at once.
		oscar := mustUserID(t, a, "oscar")
		if ok, _ := a.mgr.MayWrite(context.Background(), oscar, tm.sid); ok {
			t.Fatal("an outsider may write")
		}
		admin.do("PATCH", "/api/users/"+oscar, map[string]any{"role": "admin"}).want(t, 200, "")
		if ok, _ := a.mgr.MayWrite(context.Background(), oscar, tm.sid); !ok {
			t.Fatal("a new administrator may not write")
		}
		admin.do("PATCH", "/api/users/"+oscar, map[string]any{"role": "member"}).want(t, 200, "")
		if ok, _ := a.mgr.MayWrite(context.Background(), oscar, tm.sid); ok {
			t.Fatal("a demoted administrator may still write")
		}
	})
}
