package api_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/store"
)

func noteItems(t *testing.T, c *client, query string) (items []map[string]any, next string, unread int) {
	t.Helper()
	r := c.do("GET", "/api/notifications"+query, nil).want(t, 200, "")
	for _, it := range r.body["items"].([]any) {
		items = append(items, it.(map[string]any))
	}
	next, _ = r.body["next"].(string)
	return items, next, int(r.body["unread"].(float64))
}

func unreadOf(t *testing.T, c *client) int {
	t.Helper()
	return int(c.do("GET", "/api/notifications/unread", nil).want(t, 200, "").body["count"].(float64))
}

func TestAddingAMemberTellsThemAndTheListIsTheirOwn(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		// newTeam added the editor and the viewer to the space: each was told once, the owner not.
		if n := unreadOf(t, tm.editor); n != 1 {
			t.Fatalf("the editor has %d unread", n)
		}
		if n := unreadOf(t, tm.owner); n != 0 {
			t.Fatalf("the owner was told of their own space: %d", n)
		}
		items, next, unread := noteItems(t, tm.editor, "")
		if len(items) != 1 || next != "" || unread != 1 {
			t.Fatalf("%v %q %d", items, next, unread)
		}
		it := items[0]
		p := it["payload"].(map[string]any)
		if it["kind"] != "invite" || it["ref"] != tm.sid || it["read"] != false || it["derived"] != nil || p["space"] != "Team" || p["role"] != "editor" || p["by"] != "olive" {
			t.Fatalf("%v", it)
		}
		if _, err := time.Parse(time.RFC3339, it["at"].(string)); err != nil {
			t.Fatalf("at: %v", err)
		}
		// Nobody else sees it.
		if items, _, _ := noteItems(t, tm.viewer, ""); len(items) != 1 || items[0]["id"] == it["id"] {
			t.Fatalf("the viewer sees %v", items)
		}
		if items, _, unread := noteItems(t, tm.outsider, ""); len(items) != 0 || unread != 0 {
			t.Fatalf("the outsider sees %v", items)
		}
		// Reading it is the person's own act: another person's mark changes nothing.
		tm.viewer.do("POST", "/api/notifications/read", map[string]any{"ids": []string{it["id"].(string)}}).want(t, 200, "")
		if n := unreadOf(t, tm.editor); n != 1 {
			t.Fatalf("the viewer read the editor's: %d", n)
		}
		r := tm.editor.do("POST", "/api/notifications/read", map[string]any{"ids": []string{it["id"].(string)}}).want(t, 200, "")
		if r.body["unread"] != float64(0) || unreadOf(t, tm.editor) != 0 {
			t.Fatalf("%s", r.raw)
		}
		items, _, _ = noteItems(t, tm.editor, "")
		if items[0]["read"] != true {
			t.Fatalf("%v", items[0])
		}
		if items, _, _ := noteItems(t, tm.editor, "?unread=1"); len(items) != 0 {
			t.Fatalf("%v", items)
		}
		// Changing a role or removing a member is no invitation.
		tm.owner.do("PATCH", "/api/spaces/"+tm.sid+"/members/"+tm.viewerID, map[string]any{"role": "editor"}).want(t, 200, "")
		if n := unreadOf(t, tm.viewer); n != 1 { // the one of being added, not read
			t.Fatalf("a change of role was told as an invitation: %d", n)
		}
	})
}

func TestTheListPagesNewestFirstAndFiltersByKind(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		u := a.setup("kazuha", pw)
		id := mustUserID(t, a, "kazuha")
		base := a.clock.Now()
		for i := 0; i < 7; i++ {
			kind := store.NotifyInvite
			if i%2 == 1 {
				kind = store.NotifySync
			}
			if err := store.NotifyUser(ctxBG, a.st.Q(), id, kind, fmt.Sprint("ref", i), map[string]any{"n": i}, base.Add(time.Duration(i)*time.Minute)); err != nil {
				t.Fatal(err)
			}
		}
		items, next, unread := noteItems(t, u, "?limit=3")
		if len(items) != 3 || next == "" || unread != 7 || items[0]["ref"] != "ref6" || items[2]["ref"] != "ref4" {
			t.Fatalf("%v %q %d", items, next, unread)
		}
		items, next2, _ := noteItems(t, u, "?limit=3&cursor="+next)
		if len(items) != 3 || next2 == "" || items[0]["ref"] != "ref3" {
			t.Fatalf("%v %q", items, next2)
		}
		items, next3, _ := noteItems(t, u, "?limit=3&cursor="+next2)
		if len(items) != 1 || next3 != "" || items[0]["ref"] != "ref0" {
			t.Fatalf("%v %q", items, next3)
		}
		items, _, _ = noteItems(t, u, "?kind=sync")
		if len(items) != 3 {
			t.Fatalf("%d syncs", len(items))
		}
		for _, it := range items {
			if it["kind"] != "sync" {
				t.Fatalf("%v", it)
			}
		}
		// Mark all of one kind, then all.
		r := u.do("POST", "/api/notifications/read-all", map[string]any{"kind": "sync"}).want(t, 200, "")
		if r.body["unread"] != float64(4) {
			t.Fatalf("%s", r.raw)
		}
		r = u.do("POST", "/api/notifications/read-all", nil).want(t, 200, "")
		if r.body["unread"] != float64(0) || unreadOf(t, u) != 0 {
			t.Fatalf("%s", r.raw)
		}
		// Refusals.
		u.do("GET", "/api/notifications?kind=bogus", nil).want(t, 400, "invalid")
		u.do("GET", "/api/notifications?limit=0", nil).want(t, 400, "invalid")
		u.do("GET", "/api/notifications?limit=101", nil).want(t, 400, "invalid")
		u.do("POST", "/api/notifications/read", map[string]any{"ids": []string{}}).want(t, 400, "invalid")
		u.do("POST", "/api/notifications/read", map[string]any{"ids": []string{strings.Repeat("x", 201)}}).want(t, 400, "invalid")
		u.do("POST", "/api/notifications/read-all", map[string]any{"kind": "bogus"}).want(t, 400, "invalid")
		a.newClient("").do("GET", "/api/notifications", nil).want(t, 401, "")
		a.newClient("").do("GET", "/api/notifications/unread", nil).want(t, 401, "")
		a.newClient("").do("POST", "/api/notifications/read-all", nil).want(t, 401, "")
	})
}

func TestTheUnreadCountStopsAtOneHundred(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		u := a.setup("kazuha", pw)
		id := mustUserID(t, a, "kazuha")
		for i := 0; i < 130; i++ {
			if err := store.NotifyUser(ctxBG, a.st.Q(), id, store.NotifySync, "r", map[string]any{}, a.clock.Now()); err != nil {
				t.Fatal(err)
			}
		}
		if n := unreadOf(t, u); n != 100 {
			t.Fatalf("%d", n)
		}
	})
}

// The problems of a space are not rows: they come from the index of the spaces
// the person writes in, and "read" is kept against what they said then.
func TestProblemsAreDerivedFromTheIndexAndReadAgainstWhatTheySaid(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		for _, c := range []*client{tm.owner, tm.editor, tm.viewer, tm.outsider} {
			c.do("POST", "/api/notifications/read-all", nil).want(t, 200, "") // the invitations
		}
		root := filepath.Join(a.dir, "spaces", tm.sid)
		// A page with nothing wrong is no notification.
		if n := unreadOf(t, tm.owner); n != 0 {
			t.Fatalf("%d", n)
		}
		writeRaw(t, root, "noid.md", "---\ntitle: no id\n---\nx\n")
		writeRaw(t, root, "broken.md", "---\ntitle: [x\n---\nbroken\n")
		tm.owner.do("POST", "/api/spaces/"+tm.sid+"/reindex", nil).want(t, 200, "")
		a.clock.Advance(time.Minute) // the count of an open space is reused for a few seconds

		items, _, unread := noteItems(t, tm.owner, "")
		if len(items) != 1 || unread != 1 {
			t.Fatalf("%v %d", items, unread)
		}
		it := items[0]
		p := it["payload"].(map[string]any)
		if it["kind"] != "problems" || it["derived"] != true || it["id"] != "problems:"+tm.sid || it["ref"] != tm.sid || it["at"] != nil || it["read"] != false ||
			p["space"] != "Team" || p["total"] != float64(2) || p["counts"].(map[string]any)["no_id"] != float64(1) {
			t.Fatalf("%v", it)
		}
		// The editor writes in the space, so is told; the viewer and the outsider are not.
		if n := unreadOf(t, tm.editor); n != 1 {
			t.Fatalf("editor %d", n)
		}
		if n := unreadOf(t, tm.viewer); n != 0 {
			t.Fatalf("viewer %d", n)
		}
		if n := unreadOf(t, tm.outsider); n != 0 {
			t.Fatalf("outsider %d", n)
		}
		// Filters: problems only, and only on the first page.
		if items, _, _ := noteItems(t, tm.owner, "?kind=problems"); len(items) != 1 {
			t.Fatalf("%v", items)
		}
		if items, _, _ := noteItems(t, tm.owner, "?kind=invite"); len(items) != 0 {
			t.Fatalf("%v", items)
		}

		// Reading it is the person's: the editor's problem stays unread for the editor.
		r := tm.owner.do("POST", "/api/notifications/read", map[string]any{"ids": []string{it["id"].(string)}}).want(t, 200, "")
		if r.body["unread"] != float64(0) || unreadOf(t, tm.editor) != 1 {
			t.Fatalf("%s", r.raw)
		}
		if items, _, _ := noteItems(t, tm.owner, ""); items[0]["read"] != true {
			t.Fatalf("%v", items[0])
		}
		// It becomes unread when what it says changes.
		writeRaw(t, root, "another.md", "---\ntitle: another\n---\nx\n")
		tm.owner.do("POST", "/api/spaces/"+tm.sid+"/reindex", nil).want(t, 200, "")
		a.clock.Advance(time.Minute)
		if items, _, unread := noteItems(t, tm.owner, ""); items[0]["read"] != false || unread != 1 {
			t.Fatalf("%v %d", items, unread)
		}
		// Mark all reads it again, and mending what was wrong takes it away.
		tm.owner.do("POST", "/api/notifications/read-all", nil).want(t, 200, "")
		if unreadOf(t, tm.owner) != 0 {
			t.Fatal("still unread")
		}
		for _, f := range []string{"noid.md", "broken.md", "another.md"} {
			if err := os.Remove(filepath.Join(root, f)); err != nil {
				t.Fatal(err)
			}
		}
		tm.owner.do("POST", "/api/spaces/"+tm.sid+"/reindex", nil).want(t, 200, "")
		a.clock.Advance(time.Minute)
		if items, _, _ := noteItems(t, tm.owner, ""); len(items) != 0 {
			t.Fatalf("%v", items)
		}
		// A key that is not the person's records nothing and breaks nothing.
		tm.outsider.do("POST", "/api/notifications/read", map[string]any{"ids": []string{"problems:" + tm.sid, "problems:nope"}}).want(t, 200, "")
	})
}

func TestNotificationsAreNotForATokenNorAnAdministratorsView(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		_, secret := mkToken(t, tm.editor, "read", nil, tm.sid)
		b := a.bearer(secret, "")
		b.do("GET", "/api/notifications", nil).want(t, 403, "token_scope")
		b.do("GET", "/api/notifications/unread", nil).want(t, 403, "token_scope")
		b.do("POST", "/api/notifications/read-all", nil).want(t, 403, "token_scope")
		b.do("GET", "/api/prefs", nil).want(t, 403, "token_scope")
		b.do("PATCH", "/api/prefs", map[string]any{"set": map[string]any{"a": 1}}).want(t, 403, "token_scope")
	})
}

func TestSingleUserHasNotificationsToo(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	code, r, raw := f.do("GET", "/api/notifications", nil)
	if code != 200 || len(r["items"].([]any)) != 0 || r["unread"] != float64(0) {
		t.Fatalf("%d %s", code, raw)
	}
	if code, r, raw = f.do("GET", "/api/notifications/unread", nil); code != 200 || r["count"] != float64(0) {
		t.Fatalf("%d %s", code, raw)
	}
	writeRaw(t, f.dir, "noid.md", "---\ntitle: no id\n---\nx\n")
	f.do("POST", base+"/reindex", nil)
	code, r, raw = f.do("GET", "/api/notifications", nil)
	items := r["items"].([]any)
	if code != 200 || len(items) != 1 || items[0].(map[string]any)["kind"] != "problems" || r["unread"] != float64(1) {
		t.Fatalf("%d %s", code, raw)
	}
	if code, r, raw = f.do("POST", "/api/notifications/read-all", nil); code != 200 || r["unread"] != float64(0) {
		t.Fatalf("%d %s", code, raw)
	}
}

// A person's notifications are read by their own id and tenant: nothing of another
// tenant is in a list, and another tenant's row cannot be marked with its id.
func TestNotificationsAreTheCallersOwnAcrossTenants(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		// B's member was added to B's space by B's administrator (newTenFx).
		itemsB, _, unreadB := noteItems(t, f.memB, "")
		if len(itemsB) == 0 || unreadB == 0 {
			t.Fatalf("B's member has nothing: %v", itemsB)
		}
		idB := itemsB[0]["id"].(string)
		for name, c := range map[string]*client{"tenant admin": f.admA, "member": f.memA, "site admin": f.root} {
			items, _, _ := noteItems(t, c, "")
			for _, it := range items {
				raw := fmt.Sprint(it)
				for _, b := range f.bIDs() {
					if strings.Contains(raw, b) {
						t.Errorf("%s sees %q of tenant B: %s", name, b, raw)
					}
				}
			}
			// Marking B's row, by its id, is as marking one that is not there.
			c.do("POST", "/api/notifications/read", map[string]any{"ids": []string{idB}}).want(t, 200, "")
		}
		if _, _, n := noteItems(t, f.memB, ""); n != unreadB {
			t.Fatalf("A marked B's: %d -> %d", unreadB, n)
		}
		for _, c := range []*client{f.admA, f.memA, f.root} {
			c.do("POST", "/api/notifications/read-all", nil).want(t, 200, "")
		}
		if _, _, n := noteItems(t, f.memB, ""); n != unreadB {
			t.Fatalf("A marked all of B's: %d -> %d", unreadB, n)
		}
		// A site administrator has the list of the account they are, not of a tenant.
		if _, _, n := noteItems(t, f.root, "?kind=invite"); n != 0 {
			t.Fatalf("%d", n)
		}
	})
}
