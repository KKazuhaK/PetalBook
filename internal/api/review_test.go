package api_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/KKazuhaK/PetalBook/internal/store"
)

// A member who was made a viewer keeps the right to throw their own draft away.
func TestAViewerMayDiscardTheirOwnDraft(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// Deleting a space keeps what members had typed and not saved, in the recycle
// item; restoring gives it back, held drafts held.
func TestAdminDeleteKeepsTheDraftsInTheItem(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tm := newTeam(t, a)
		base := "/api/spaces/" + tm.sid
		p := tm.editor.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "Held"}, "body": "x\n"}).want(t, 201, "")
		url := base + "/pages/" + p.body["id"].(string)
		tm.editor.draft(url, p.body["rev"], "Held", "unsaved text\n").want(t, 200, "")
		tm.owner.draft(url, p.body["rev"], "Held", "the owner's text\n").want(t, 200, "")
		a.st.Q().Exec(context.Background(), `UPDATE drafts SET held = 1 WHERE user_id = ?`, tm.ownerID)
		drafts := func() map[string]string {
			rows, err := a.st.Q().Query(context.Background(), `SELECT user_id, body, held FROM drafts WHERE space_id = ?`, tm.sid)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			out := map[string]string{}
			for rows.Next() {
				var u, b string
				var h int
				if err := rows.Scan(&u, &b, &h); err != nil {
					t.Fatal(err)
				}
				out[u] = fmt.Sprintf("%s|%d", strings.TrimSpace(b), h)
			}
			return out
		}
		before := drafts()
		if len(before) != 2 {
			t.Fatalf("%v", before)
		}

		item := admin.do("DELETE", "/api/admin/spaces/"+tm.sid, nil).want(t, 200, "").body["id"].(string)
		if n := len(drafts()); n != 0 {
			t.Fatalf("%d drafts are still live", n)
		}
		admin.do("POST", "/api/admin/recycle/"+item+"/restore", map[string]any{}).want(t, 200, "")
		after := drafts()
		if len(after) != 2 || after[tm.ownerID] != "the owner's text|1" || after[tm.editorID] != "unsaved text|0" {
			t.Fatalf("%v", after)
		}
		for k, v := range before {
			if after[k] != v {
				t.Fatalf("%s: %q, was %q", k, after[k], v)
			}
		}
	})
}

// Disabling a user, and changing their role, is seen by the drafts sweeper at once.
func TestUserChangesReachTheWriteCheckAtOnce(t *testing.T) {
	t.Parallel()
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
