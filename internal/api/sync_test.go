package api_test

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/api"
	"github.com/KKazuhaK/PetalBook/internal/spaces"
)

func syncBase(sid string) string { return spacePath(sid) }

func makePage(t *testing.T, c *client, sid, folder, title, body string) map[string]any {
	t.Helper()
	return c.do("POST", spacePath(sid)+"/pages", map[string]any{"folder": folder, "meta": map[string]any{"title": title}, "body": body}).want(t, 201, "").body
}

func ids(items any) []string {
	var out []string
	for _, it := range items.([]any) {
		out = append(out, it.(map[string]any)["id"].(string))
	}
	return out
}

// walkCatalog reads a whole catalogue in small batches and returns its pages,
// the first batch's change cursor, and the last tree and settings stamps.
func walkCatalog(t *testing.T, c *client, sid string, limit int, q string) (items []any, changes, tree, settings string) {
	t.Helper()
	cursor := ""
	for n := 0; n < 100; n++ {
		path := fmt.Sprintf("%s/sync/catalog?limit=%d%s", spacePath(sid), limit, q)
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		r := c.do("GET", path, nil).want(t, 200, "")
		if (cursor == "") != (r.body["changes"] != nil) {
			t.Fatalf("changes belongs to the first batch only: %s", r.raw)
		}
		if cursor == "" {
			changes = r.body["changes"].(string)
		}
		items = append(items, r.body["items"].([]any)...)
		tree, settings = r.body["tree"].(string), r.body["settings"].(string)
		next := r.body["next"].(string)
		if next == "" {
			return
		}
		cursor = next
	}
	t.Fatal("a catalogue that does not end")
	return
}

func TestSyncCatalogAndChanges(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		sid := tm.sid
		var made []string
		for i := 0; i < 5; i++ {
			folder := []string{"", "Tang", "Tang/Li", "Song", "Tangible"}[i]
			made = append(made, makePage(t, tm.editor, sid, folder, fmt.Sprintf("P%d", i), "text\n")["id"].(string))
		}
		items, changes, tree0, settings0 := walkCatalog(t, tm.viewer, sid, 2, "")
		if len(items) != 5 || changes == "" || tree0 == "" || settings0 == "" {
			t.Fatalf("%d pages, %q %q %q", len(items), changes, tree0, settings0)
		}
		e := items[0].(map[string]any)
		for _, k := range []string{"id", "path", "folder", "label", "untitled", "tags", "type", "updated", "rev"} {
			if _, ok := e[k]; !ok {
				t.Fatalf("an entry without %s: %v", k, e)
			}
		}
		if _, ok := e["excerpt"]; ok {
			t.Fatalf("an entry has an excerpt: %v", e)
		}
		if f, _, _, _ := walkCatalog(t, tm.viewer, sid, 2, "&folder=Tang"); len(f) != 2 {
			t.Fatalf("the folder Tang and below: %d", len(f))
		}
		if f, _, _, _ := walkCatalog(t, tm.viewer, sid, 3, "&folder=Song&folder=Tang%2FLi"); len(f) != 2 {
			t.Fatalf("two folders: %d", len(f))
		}

		// Nothing changed: nothing comes, and the cursor stays.
		r := tm.viewer.do("GET", syncBase(sid)+"/changes?since="+changes, nil).want(t, 200, "")
		if len(r.body["items"].([]any)) != 0 || r.body["more"] != false || r.body["next"] != changes {
			t.Fatalf("%s", r.raw)
		}

		// An edit, a move, a deletion and a new page.
		got := tm.editor.do("GET", spacePath(sid)+"/pages/"+made[0], nil).want(t, 200, "").body
		tm.editor.do("PUT", spacePath(sid)+"/pages/"+made[0], map[string]any{"baseRev": got["rev"], "meta": map[string]any{"title": "P0 edited"}, "body": "new\n"}).want(t, 200, "")
		tm.editor.do("POST", spacePath(sid)+"/pages/"+made[1]+"/move", map[string]any{"folder": "Song"}).want(t, 200, "")
		tm.editor.do("DELETE", spacePath(sid)+"/pages/"+made[2], nil).want(t, 204, "")
		fresh := makePage(t, tm.editor, sid, "", "Fresh", "x\n")["id"].(string)
		r = tm.viewer.do("GET", syncBase(sid)+"/changes?since="+changes+"&limit=3", nil).want(t, 200, "")
		if r.body["more"] != true || len(r.body["items"].([]any)) != 3 {
			t.Fatalf("%s", r.raw)
		}
		r2 := tm.viewer.do("GET", syncBase(sid)+"/changes?since="+r.body["next"].(string), nil).want(t, 200, "")
		all := append(r.body["items"].([]any), r2.body["items"].([]any)...)
		if len(all) != 4 || r2.body["more"] != false {
			t.Fatalf("%s %s", r.raw, r2.raw)
		}
		by := map[string]map[string]any{}
		for _, it := range all {
			by[it.(map[string]any)["id"].(string)] = it.(map[string]any)
		}
		if by[made[0]]["title"] != "P0 edited" || by[made[0]]["rev"] == got["rev"] || !strings.HasPrefix(by[made[1]]["path"].(string), "Song/") ||
			by[made[2]]["deleted"] != true || by[made[2]]["path"] != nil || by[fresh]["title"] != "Fresh" {
			t.Fatalf("%v", by)
		}
		if r2.body["next"] == changes || r2.body["tree"] == tree0 {
			t.Fatalf("the tree stamp did not move: %s", r2.raw)
		}

		// A new folder is a change of the tree; a type is a change of the settings.
		tm.owner.do("POST", spacePath(sid)+"/folders", map[string]any{"path": "Empty"}).want(t, 201, "")
		tm.owner.do("PUT", spacePath(sid)+"/types/task", map[string]any{"title": "Task", "fields": []any{map[string]any{"key": "points", "kind": "number"}}}).want(t, 200, "")
		r3 := tm.viewer.do("GET", syncBase(sid)+"/changes?since="+r2.body["next"].(string), nil).want(t, 200, "")
		if len(r3.body["items"].([]any)) != 0 || r3.body["tree"] == r2.body["tree"] || r3.body["settings"] == r2.body["settings"] {
			t.Fatalf("%s", r3.raw)
		}
	})
}

func TestSyncCursorErrors(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		sid := tm.sid
		makePage(t, tm.editor, sid, "", "One", "x\n")
		_, changes, _, _ := walkCatalog(t, tm.viewer, sid, 1, "")
		b := syncBase(sid)
		tm.viewer.do("GET", b+"/changes", nil).want(t, 400, "invalid")
		tm.viewer.do("GET", b+"/changes?since=", nil).want(t, 400, "invalid")
		tm.viewer.do("GET", b+"/changes?since=nope", nil).want(t, 400, "invalid")
		for _, l := range []string{"0", "-1", "5001", "x"} {
			tm.viewer.do("GET", b+"/changes?since="+changes+"&limit="+l, nil).want(t, 400, "invalid")
			tm.viewer.do("GET", b+"/sync/catalog?limit="+l, nil).want(t, 400, "invalid")
		}
		tm.viewer.do("GET", b+"/sync/catalog?cursor=nope", nil).want(t, 400, "invalid")
		var many strings.Builder
		for i := 0; i < 51; i++ {
			many.WriteString(fmt.Sprintf("&folder=f%d", i))
		}
		tm.viewer.do("GET", b+"/sync/catalog?x=1"+many.String(), nil).want(t, 400, "invalid")
		// A catalogue cursor made for other folders is refused.
		r := tm.viewer.do("GET", b+"/sync/catalog?limit=1&folder=a", nil).want(t, 200, "")
		_ = r
		makePage(t, tm.editor, sid, "a", "A1", "x\n")
		makePage(t, tm.editor, sid, "a", "A2", "x\n")
		r = tm.viewer.do("GET", b+"/sync/catalog?limit=1&folder=a", nil).want(t, 200, "")
		tm.viewer.do("GET", b+"/sync/catalog?limit=1&folder=b&cursor="+url.QueryEscape(r.body["next"].(string)), nil).want(t, 400, "invalid")

		// The index is built again: every cursor is void.
		_, first, _, _ := walkCatalog(t, tm.viewer, sid, 1, "")
		mid := tm.viewer.do("GET", b+"/sync/catalog?limit=1", nil).want(t, 200, "").body["next"].(string)
		tm.owner.do("POST", b+"/reindex", nil).want(t, 200, "")
		tm.viewer.do("GET", b+"/changes?since="+first, nil).want(t, 410, "changes_expired")
		tm.viewer.do("GET", b+"/sync/catalog?cursor="+url.QueryEscape(mid), nil).want(t, 410, "changes_expired")
		// And the new catalogue starts from a good one.
		_, changes, _, _ = walkCatalog(t, tm.viewer, sid, 1, "")
		tm.viewer.do("GET", b+"/changes?since="+changes, nil).want(t, 200, "")
	})
}

func TestSyncPages(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		sid := tm.sid
		p1 := makePage(t, tm.editor, sid, "", "One", "first\n")
		p2 := makePage(t, tm.editor, sid, "Sub", "Two", "second\n")
		id1, id2 := p1["id"].(string), p2["id"].(string)
		// The editor has a draft of the first page; the owner has none.
		tm.editor.do("PUT", spacePath(sid)+"/pages/"+id1+"/draft", draftBody(p1["rev"], "One", "first, being written\n")).want(t, 200, "")
		b := syncBase(sid) + "/sync/pages?ids="
		r := tm.editor.do("GET", b+id1+","+"01JD3K8Q7Z"+","+id2, nil).want(t, 200, "")
		pages := r.body["pages"].([]any)
		if len(pages) != 2 || ids(pages)[0] != id1 || ids(pages)[1] != id2 {
			t.Fatalf("%s", r.raw)
		}
		first := pages[0].(map[string]any)
		one := tm.editor.do("GET", spacePath(sid)+"/pages/"+id1, nil).want(t, 200, "").body
		if first["body"] != one["body"] || first["rev"] != one["rev"] || first["header"] != one["header"] || first["draft"] == nil ||
			first["draft"].(map[string]any)["body"] != "first, being written\n" {
			t.Fatalf("%v\n%v", first, one)
		}
		if pages[1].(map[string]any)["draft"] != nil {
			t.Fatalf("a draft that is not there: %s", r.raw)
		}
		if m := r.body["missing"].([]any); len(m) != 1 || m[0] != "01JD3K8Q7Z" || r.body["rest"] != nil {
			t.Fatalf("%s", r.raw)
		}
		// Somebody else's draft is not given to the owner.
		o := tm.owner.do("GET", b+id1, nil).want(t, 200, "")
		if o.body["pages"].([]any)[0].(map[string]any)["draft"] != nil || o.body["missing"] != nil {
			t.Fatalf("%s", o.raw)
		}
		// Refusals.
		tm.viewer.do("GET", syncBase(sid)+"/sync/pages", nil).want(t, 400, "invalid")
		tm.viewer.do("GET", b, nil).want(t, 400, "invalid")
		tm.viewer.do("GET", b+id1+","+id1, nil).want(t, 400, "invalid")
		tm.viewer.do("GET", b+id1+",x", nil).want(t, 400, "invalid")
		tm.viewer.do("GET", b+id1+",", nil).want(t, 400, "invalid")
		var l []string
		for i := 0; i < 101; i++ {
			l = append(l, fmt.Sprintf("01JD3%05d", i))
		}
		tm.viewer.do("GET", b+strings.Join(l, ","), nil).want(t, 400, "invalid")
		// 100 ids are fine, and all of them missing.
		r = tm.viewer.do("GET", b+strings.Join(l[:100], ","), nil).want(t, 200, "")
		if len(r.body["pages"].([]any)) != 0 || len(r.body["missing"].([]any)) != 100 {
			t.Fatalf("%s", r.raw)
		}
	})
}

func TestSyncPagesStopAtTheCap(t *testing.T) {
	// Not parallel: the cap is a variable of the package, made small here
	// (the real one is 8 MiB, which is slow to write in a test).
	old := api.SetSyncPagesBytes(100 << 10)
	defer api.SetSyncPagesBytes(old)
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		api.SetSyncPagesBytes(100 << 10)
		tm := newTeam(t, a)
		big := strings.Repeat("0123456789abcdef", 1<<11) // 32 KiB
		var want []string
		for i := 0; i < 5; i++ {
			want = append(want, makePage(t, tm.owner, tm.sid, "", fmt.Sprintf("Big%d", i), big+"\n")["id"].(string))
		}
		r := tm.viewer.do("GET", syncBase(tm.sid)+"/sync/pages?ids="+strings.Join(want, ","), nil).want(t, 200, "")
		got := ids(r.body["pages"])
		rest := r.body["rest"].([]any)
		if len(got) != 3 || len(rest) != 2 || rest[0] != want[3] || rest[1] != want[4] {
			t.Fatalf("%d pages, rest %v", len(got), rest)
		}
		// What did not fit comes with the next request.
		r = tm.viewer.do("GET", syncBase(tm.sid)+"/sync/pages?ids="+want[3]+","+want[4], nil).want(t, 200, "")
		if len(r.body["pages"].([]any)) != 2 || r.body["rest"] != nil {
			t.Fatalf("%s", r.raw[:200])
		}
		// The first page always fits, whatever its size.
		api.SetSyncPagesBytes(1)
		r = tm.viewer.do("GET", syncBase(tm.sid)+"/sync/pages?ids="+want[0]+","+want[1], nil).want(t, 200, "")
		if len(r.body["pages"].([]any)) != 1 || len(r.body["rest"].([]any)) != 1 {
			t.Fatalf("%.200s", r.raw)
		}
	})
}

func TestSyncPermissions(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tm := newTeam(t, a)
		sid := tm.sid
		p := makePage(t, tm.owner, sid, "", "One", "x\n")["id"].(string)
		_, changes, _, _ := walkCatalog(t, tm.viewer, sid, 10, "")
		paths := []string{"/sync/catalog", "/changes?since=" + changes, "/sync/pages?ids=" + p}
		// Members read, a stranger is told there is no such space.
		for _, path := range paths {
			for _, c := range []*client{tm.viewer, tm.editor, tm.owner} {
				c.do("GET", syncBase(sid)+path, nil).want(t, 200, "")
			}
			tm.outsider.do("GET", syncBase(sid)+path, nil).want(t, 404, "not_found")
			a.newClient("").do("GET", syncBase(sid)+path, nil).want(t, 401, "")
		}
		// An API token reads the spaces it names, and no others.
		_, secret := mkToken(t, tm.viewer, "read", nil, sid)
		b := a.bearer(secret, "")
		for _, path := range paths {
			b.do("GET", syncBase(sid)+path, nil).want(t, 200, "")
		}
		mine := tm.viewer.personalSpace()
		b.do("GET", syncBase(mine)+"/sync/catalog", nil).want(t, 403, "token_scope")
		b.do("GET", "/api/sync/heads?space="+sid+"&space="+mine, nil).want(t, 200, "")
		// A token's owner who lost the space is refused at once.
		tm.owner.do("DELETE", spacePath(sid)+"/members/"+tm.viewerID, nil).want(t, 204, "")
		b.do("GET", syncBase(sid)+"/sync/catalog", nil).want(t, 404, "not_found")
		// A disabled space is refused like any other route, and drops out of the heads.
		admin.do("POST", "/api/admin/spaces/"+sid+"/disable", nil).want(t, 204, "")
		tm.editor.do("GET", syncBase(sid)+"/changes?since="+changes, nil).want(t, 403, "space_disabled")
		r := tm.editor.do("GET", "/api/sync/heads?space="+sid, nil).want(t, 200, "")
		if len(r.body["heads"].(map[string]any)) != 0 || r.body["missing"].([]any)[0] != sid {
			t.Fatalf("%s", r.raw)
		}
	})
}

func TestSyncHeads(t *testing.T) {
	t.Parallel()
	opened := 0
	eachAccounts(t, accOpts{spaces: func(o *spaces.Options) {
		o.OpenHook = func(string) { opened++ }
		o.IdleAfter = time.Minute
	}}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		sid := tm.sid
		mine := tm.owner.personalSpace()
		page := makePage(t, tm.editor, sid, "", "One", "x\n")
		makePage(t, tm.owner, mine, "", "Mine", "x\n")
		heads := func(c *client, q string) result { return c.do("GET", "/api/sync/heads?"+q, nil).want(t, 200, "") }
		r := heads(tm.editor, "space="+sid)
		h1 := r.body["heads"].(map[string]any)[sid].(string)
		_, cursor, _, _ := walkCatalog(t, tm.editor, sid, 100, "")
		if h1 == "" || h1 != cursor || r.body["missing"] != nil {
			t.Fatalf("head %q, the catalogue's cursor %q: %s", h1, cursor, r.raw)
		}
		// A write moves the head; a read does not.
		heads(tm.editor, "space="+sid)
		walkCatalog(t, tm.editor, sid, 100, "")
		if h := heads(tm.editor, "space="+sid).body["heads"].(map[string]any)[sid]; h != h1 {
			t.Fatalf("a read moved the head: %v", h)
		}
		got := tm.editor.do("GET", spacePath(sid)+"/pages/"+page["id"].(string), nil).want(t, 200, "").body
		tm.editor.do("PUT", spacePath(sid)+"/pages/"+page["id"].(string), map[string]any{"baseRev": got["rev"], "meta": map[string]any{"title": "Two"}, "body": "y\n"}).want(t, 200, "")
		h2 := heads(tm.editor, "space="+sid).body["heads"].(map[string]any)[sid].(string)
		if h2 == h1 {
			t.Fatal("a write did not move the head")
		}

		// The spaces are closed: the heads are answered, and nothing is opened.
		a.clock.Advance(time.Hour)
		a.mgr.Trim()
		if a.mgr.OpenCount() != 0 {
			t.Fatalf("%d spaces open", a.mgr.OpenCount())
		}
		before := opened
		for i := 0; i < 20; i++ {
			r = heads(tm.editor, "space="+sid+"&space=01JD3K8Q7Z")
		}
		if opened != before || a.mgr.OpenCount() != 0 {
			t.Fatal("heads opened a space")
		}
		if r.body["heads"].(map[string]any)[sid] != h2 {
			t.Fatalf("a closed space has the head it had: %s", r.raw)
		}
		// A space the caller may not see is missing, whether or not it exists.
		if m := r.body["missing"].([]any); len(m) != 1 || m[0] != "01JD3K8Q7Z" {
			t.Fatalf("%s", r.raw)
		}
		r = heads(tm.outsider, "space="+sid)
		if len(r.body["heads"].(map[string]any)) != 0 || r.body["missing"].([]any)[0] != sid {
			t.Fatalf("%s", r.raw)
		}
		// Many at once.
		r = heads(tm.owner, "space="+sid+"&space="+mine)
		if len(r.body["heads"].(map[string]any)) != 2 {
			t.Fatalf("%s", r.raw)
		}
		if opened != before {
			t.Fatal("heads opened a space")
		}
		// The head in the cursor is the one the space gives when it is opened again.
		cl := tm.editor.do("GET", syncBase(sid)+"/changes?since="+h1, nil).want(t, 200, "")
		if cl.body["next"] != h2 || len(cl.body["items"].([]any)) != 1 {
			t.Fatalf("%s", cl.raw)
		}
		// Refusals.
		tm.editor.do("GET", "/api/sync/heads", nil).want(t, 400, "invalid")
		tm.editor.do("GET", "/api/sync/heads?space="+sid+"&space="+sid, nil).want(t, 400, "invalid")
		var l []string
		for i := 0; i < 101; i++ {
			l = append(l, fmt.Sprintf("space=01JD3%05d", i))
		}
		tm.editor.do("GET", "/api/sync/heads?"+strings.Join(l, "&"), nil).want(t, 400, "invalid")
		heads(tm.editor, strings.Join(l[:100], "&"))
		a.newClient("").do("GET", "/api/sync/heads?space="+sid, nil).want(t, 401, "")
	})
}

func TestSyncHeadsOfATokenAndOfAColdSpace(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		mine := tm.owner.personalSpace()
		_, secret := mkToken(t, tm.owner, "read", nil, tm.sid)
		r := a.bearer(secret, "").do("GET", "/api/sync/heads?space="+tm.sid+"&space="+mine, nil).want(t, 200, "")
		if _, ok := r.body["heads"].(map[string]any)[tm.sid]; !ok || r.body["missing"].([]any)[0] != mine {
			t.Fatalf("a token names one space: %s", r.raw)
		}
		// A space that has not been open since the server started has no head
		// to give: "" says to ask for its changes.
		opened := 0
		m2, err := spaces.NewAccounts(spaces.Options{Store: a.st, DataDir: a.dir, Committer: "PetalBook Test <svc@example.test>", OpenHook: func(string) { opened++ }})
		if err != nil {
			t.Fatal(err)
		}
		defer m2.Close()
		hs, err := m2.Heads(context.Background(), tm.ownerID, []string{tm.sid, mine, "01JD3K8Q7Z"})
		if err != nil || len(hs) != 2 || hs[tm.sid].Head != "" || hs[mine].Head != "" || opened != 0 || m2.OpenCount() != 0 {
			t.Fatalf("%v %v opened %d", hs, err, opened)
		}
		// Asking for its changes opens it; from then on the head is known.
		sp, release, err := m2.Acquire(context.Background(), tm.sid)
		if err != nil {
			t.Fatal(err)
		}
		head := sp.Head()
		release()
		if hs, _ = m2.Heads(context.Background(), tm.ownerID, []string{tm.sid}); hs[tm.sid].Head != head || head == "" {
			t.Fatalf("%v %q", hs, head)
		}
	})
}

func TestSingleUserSync(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for i := 0; i < 3; i++ {
		if code, _, raw := f.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": fmt.Sprintf("S%d", i)}, "body": "x\n"}); code != 201 {
			t.Fatal(raw)
		}
	}
	code, r, raw := f.do("GET", base+"/sync/catalog?limit=2", nil)
	if code != 200 || len(r["items"].([]any)) != 2 || r["next"] == "" || r["changes"] == "" {
		t.Fatal(raw)
	}
	changes := r["changes"].(string)
	code, r, raw = f.do("GET", "/api/sync/heads?space=default&space=other", nil)
	if code != 200 || r["heads"].(map[string]any)["default"] == "" || r["missing"].([]any)[0] != "other" {
		t.Fatal(raw)
	}
	head := r["heads"].(map[string]any)["default"].(string)
	if head != changes {
		t.Fatalf("head %s, cursor %s", head, changes)
	}
	code, _, raw = f.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "More"}, "body": "x\n"})
	if code != 201 {
		t.Fatal(raw)
	}
	code, r, raw = f.do("GET", base+"/changes?since="+changes, nil)
	if code != 200 || len(r["items"].([]any)) != 1 || r["next"] == changes {
		t.Fatal(raw)
	}
	code, r, raw = f.do("GET", base+"/sync/pages?ids="+r["items"].([]any)[0].(map[string]any)["id"].(string), nil)
	if code != 200 || len(r["pages"].([]any)) != 1 {
		t.Fatal(raw)
	}
}
