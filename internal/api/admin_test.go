package api_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/api"
	"github.com/KKazuhaK/PetalBook/internal/page"
	"github.com/KKazuhaK/PetalBook/internal/space"
	"github.com/KKazuhaK/PetalBook/internal/spaces"
)

func adminPath(rt api.RouteInfo, sid string) string {
	return strings.NewReplacer("{space}", sid).Replace(rt.Pattern)
}

func adminRoutes(t *testing.T) []api.RouteInfo {
	t.Helper()
	var out []api.RouteInfo
	for _, rt := range api.Routes() {
		if strings.HasPrefix(rt.Pattern, "/api/admin/") {
			if !(rt.Admin || rt.SiteAdmin) || !rt.AccountsOnly {
				t.Errorf("%s %s must be admin and accounts-only", rt.Method, rt.Pattern)
			}
			out = append(out, rt)
		}
	}
	if len(out) < 7 {
		t.Fatalf("only %d administration routes", len(out))
	}
	return out
}

func TestAdminRoutesAreForAdministrators(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		sid := tang.personalSpace()
		for _, rt := range adminRoutes(t) {
			path := adminPath(rt, sid)
			tang.do(rt.Method, path, "{}").want(t, 403, "forbidden")
			a.newClient("").do(rt.Method, path, "{}").want(t, 401, "")
		}
		// Nothing above changed anything.
		r := admin.do("GET", "/api/admin/spaces", nil).want(t, 200, "")
		if len(r.body["items"].([]any)) != 2 {
			t.Fatal(r.raw)
		}
	})
}

func TestAdminRoutesDoNotExistInSingleUserMode(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for _, rt := range adminRoutes(t) {
		code, r, raw := f.do(rt.Method, adminPath(rt, "default"), "{}")
		if code != 404 || r["error"] != "not_found" {
			t.Errorf("%s %s: %d %s", rt.Method, rt.Pattern, code, raw)
		}
	}
}

func TestAdminListsEverySpaceWithOwnersAndPages(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		a.addUser("mei", pw, "member")
		sid := tang.do("POST", "/api/spaces", map[string]any{"name": "Team Rocket"}).want(t, 201, "").body["id"].(string)
		tang.do("POST", "/api/spaces/"+sid+"/members", map[string]any{"username": "mei", "role": "editor"}).want(t, 201, "")
		for i := 0; i < 3; i++ {
			tang.do("POST", "/api/spaces", map[string]any{"name": fmt.Sprintf("Extra %d", i)}).want(t, 201, "")
			a.clock.Advance(time.Second)
		}

		// 3 personal + 4 shared.
		var all []map[string]any
		cursor := ""
		pages := 0
		for {
			r := admin.do("GET", "/api/admin/spaces?limit=3&cursor="+cursor, nil).want(t, 200, "")
			pages++
			for _, it := range r.body["items"].([]any) {
				all = append(all, it.(map[string]any))
			}
			if r.body["next"] == "" {
				break
			}
			cursor = r.body["next"].(string)
		}
		if len(all) != 7 || pages != 3 {
			t.Fatalf("%d spaces in %d pages", len(all), pages)
		}
		seen := map[string]bool{}
		for _, it := range all {
			id := it["id"].(string)
			if seen[id] {
				t.Fatalf("%s listed twice", id)
			}
			seen[id] = true
			if it["owner"] == nil || it["sizeBytes"] != nil || it["sizeAt"] != nil || it["disabled"] != false || it["sync"] != "local" || it["created"] == "" {
				t.Fatalf("%v", it)
			}
			if id == sid {
				o := it["owner"].(map[string]any)
				if it["name"] != "Team Rocket" || it["personal"] != false || it["inbox"] != "Inbox" || it["members"] != float64(2) || o["username"] != "tang" || o["id"] != mustUserID(t, a, "tang") {
					t.Fatalf("%v", it)
				}
			}
		}
		if !seen[sid] {
			t.Fatal("the shared space is missing")
		}

		// A name filter, ignoring case and treating % and _ as themselves.
		r := admin.do("GET", "/api/admin/spaces?q=ROCKET", nil).want(t, 200, "")
		if l := r.body["items"].([]any); len(l) != 1 || l[0].(map[string]any)["id"] != sid {
			t.Fatal(r.raw)
		}
		r = admin.do("GET", "/api/admin/spaces?q=%25", nil).want(t, 200, "")
		if l := r.body["items"].([]any); len(l) != 0 {
			t.Fatal(r.raw)
		}
		admin.do("GET", "/api/admin/spaces?limit=0", nil).want(t, 400, "invalid")
		admin.do("GET", "/api/admin/spaces?limit=x", nil).want(t, 400, "invalid")

	})
}

func TestAdminGetsOneSpaceAsTheListHasIt(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		a.addUser("mei", pw, "member")
		sid := tang.do("POST", "/api/spaces", map[string]any{"name": "Team Rocket"}).want(t, 201, "").body["id"].(string)
		tang.do("POST", "/api/spaces/"+sid+"/members", map[string]any{"username": "mei", "role": "editor"}).want(t, 201, "")

		// Every space, the personal ones included, is the same object as its list item.
		list := admin.do("GET", "/api/admin/spaces", nil).want(t, 200, "").body["items"].([]any)
		if len(list) != 4 {
			t.Fatalf("%d spaces", len(list))
		}
		for _, it := range list {
			want := it.(map[string]any)
			got := admin.do("GET", "/api/admin/spaces/"+want["id"].(string), nil).want(t, 200, "").body
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("%v != %v", got, want)
			}
		}
		one := admin.do("GET", "/api/admin/spaces/"+sid, nil).want(t, 200, "").body
		if o, _ := one["owner"].(map[string]any); one["members"] != float64(2) || o["username"] != "tang" {
			t.Fatalf("%v", one)
		}
		admin.do("GET", "/api/admin/spaces/01ZZZZZZZZ", nil).want(t, 404, "not_found")
	})
}

func TestAdminListDoesNotOpenSpaces(t *testing.T) {
	t.Parallel()
	opened := 0
	eachAccounts(t, accOpts{spaces: func(o *spaces.Options) { o.OpenHook = func(string) { opened++ } }}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		a.addUser("tang", pw, "member")
		before := opened
		admin.do("GET", "/api/admin/spaces", nil).want(t, 200, "")
		admin.do("GET", "/api/admin/status", nil).want(t, 200, "")
		if opened != before {
			t.Fatal("the list or the status opened a space")
		}
	})
}

func TestAdminDisableAndEnable(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{spaces: func(o *spaces.Options) { o.CloseWait = 200 * time.Millisecond }}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tm := newTeam(t, a)
		base := "/api/spaces/" + tm.sid
		tm.editor.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "one"}}).want(t, 201, "")
		open := a.mgr.OpenCount() // the spaces made in this test are open

		admin.do("POST", "/api/admin/spaces/"+tm.sid+"/disable", "{}").want(t, 204, "")
		if a.mgr.OpenCount() != open-1 {
			t.Fatal("a disabled space is still open")
		}
		// What was waiting to be committed was committed when it closed.
		out, err := exec.Command("git", "-C", filepath.Join(a.dir, "spaces", tm.sid), "log", "--format=%an").Output()
		if err != nil || !strings.Contains(string(out), "edith") {
			t.Fatalf("%q %v", out, err)
		}
		for who, c := range map[string]*client{"owner": tm.owner, "editor": tm.editor, "viewer": tm.viewer, "admin": admin} {
			c.do("GET", base+"/tree", nil).want(t, 403, "space_disabled")
			if who != "admin" {
				c.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "x"}}).want(t, 403, "space_disabled")
			}
		}
		tm.owner.do("PATCH", base, map[string]any{"name": "New"}).want(t, 403, "space_disabled")
		// An outsider still learns nothing.
		tm.outsider.do("GET", base+"/tree", nil).want(t, 404, "not_found")

		// Members see it in their list, with what they would have.
		for _, c := range []*client{tm.owner, tm.editor, tm.viewer} {
			var found map[string]any
			for _, x := range c.do("GET", "/api/spaces", nil).want(t, 200, "").body["spaces"].([]any) {
				if x.(map[string]any)["id"] == tm.sid {
					found = x.(map[string]any)
				}
			}
			if found == nil || found["disabled"] != true || len(found["permissions"].([]any)) == 0 {
				t.Fatalf("%v", found)
			}
		}
		// The administration still sees and handles it.
		r := admin.do("GET", "/api/admin/spaces?q=team", nil).want(t, 200, "")
		if l := r.body["items"].([]any); len(l) != 1 || l[0].(map[string]any)["disabled"] != true {
			t.Fatal(r.raw)
		}
		admin.do("POST", "/api/admin/spaces/"+tm.sid+"/size", "{}").want(t, 200, "")
		// The drafts sweeper cannot open it either.
		if _, _, err := a.mgr.Acquire(context.Background(), tm.sid); err != spaces.ErrDisabled {
			t.Fatalf("%v", err)
		}

		admin.do("POST", "/api/admin/spaces/"+tm.sid+"/enable", "{}").want(t, 204, "")
		tm.viewer.do("GET", base+"/tree", nil).want(t, 200, "")
		tm.editor.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "two"}}).want(t, 201, "")
		for _, x := range tm.viewer.do("GET", "/api/spaces", nil).body["spaces"].([]any) {
			if x.(map[string]any)["id"] == tm.sid && x.(map[string]any)["disabled"] != false {
				t.Fatal("still marked disabled")
			}
		}
		admin.do("POST", "/api/admin/spaces/NOSUCHSPAC/disable", "{}").want(t, 404, "not_found")
	})
}

func TestAdminDisableWaitsForRequestsInFlight(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{spaces: func(o *spaces.Options) { o.CloseWait = 100 * time.Millisecond }}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tm := newTeam(t, a)
		h, err := a.mgr.Open(context.Background(), mustUser(t, a, "olive"), tm.sid)
		if err != nil {
			t.Fatal(err)
		}
		open := a.mgr.OpenCount()
		admin.do("POST", "/api/admin/spaces/"+tm.sid+"/disable", "{}").want(t, 204, "")
		// The holder is not cut off, and nobody new gets in.
		if a.mgr.OpenCount() != open {
			t.Fatal("closed under a request")
		}
		tm.owner.do("GET", "/api/spaces/"+tm.sid+"/tree", nil).want(t, 403, "space_disabled")
		h.Release()
		a.mgr.Trim()
		if a.mgr.OpenCount() != open-1 {
			t.Fatal("a disabled space was not closed once it was free")
		}
	})
}

func TestAdminTransferOwnership(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		ctx := context.Background()
		admin := a.setup("kazuha", pw)
		tm := newTeam(t, a)
		base := "/api/spaces/" + tm.sid
		tm.owner.do("POST", base+"/members", map[string]any{"username": "oscar", "role": "owner"}).want(t, 201, "") // a second owner

		// Open now, so the author of an open space is what is tested.
		h, err := a.mgr.Open(ctx, mustUser(t, a, "kazuha"), tm.sid)
		if err != nil {
			t.Fatal(err)
		}
		defer h.Release()
		last := func() string {
			out, err := exec.Command("git", "-C", filepath.Join(a.dir, "spaces", tm.sid), "log", "-1", "--format=%an").Output()
			if err != nil {
				t.Fatal(err)
			}
			return strings.TrimSpace(string(out))
		}
		create := func(title string) {
			if _, err := h.Space.Create(space.Author{}, space.CreateInput{Meta: page.Meta{{Key: "title", Value: title}}}); err != nil {
				t.Fatal(err)
			}
			if err := h.Space.Flush(); err != nil {
				t.Fatal(err)
			}
		}
		create("before")
		if got := last(); got != "olive" {
			t.Fatalf("%q", got)
		}

		admin.do("POST", "/api/admin/spaces/"+tm.sid+"/transfer", map[string]any{"userId": tm.viewerID}).want(t, 204, "")
		roles := map[string]string{}
		for _, m := range tm.viewer.do("GET", base+"/members", nil).want(t, 200, "").body["members"].([]any) {
			m := m.(map[string]any)
			roles[m["username"].(string)] = m["role"].(string)
		}
		if roles["victor"] != "owner" || roles["olive"] != "editor" || roles["oscar"] != "editor" || roles["edith"] != "editor" || len(roles) != 4 {
			t.Fatalf("%v", roles)
		}
		// The caches of those concerned were dropped: the old owner cannot manage any more, the new one can.
		tm.owner.do("PATCH", base, map[string]any{"name": "Mine"}).want(t, 403, "no_permission")
		tm.viewer.do("PATCH", base, map[string]any{"name": "Victor's"}).want(t, 200, "")
		// Later commits are by the new owner.
		create("after")
		if got := last(); got != "victor" {
			t.Fatalf("%q", got)
		}

		// To someone who is no member: they join as owner.
		a.addUser("newbie", pw, "member")
		nid := mustUserID(t, a, "newbie")
		admin.do("POST", "/api/admin/spaces/"+tm.sid+"/transfer", map[string]any{"userId": nid}).want(t, 204, "")
		r := admin.do("GET", "/api/admin/spaces?q=victor", nil).want(t, 200, "")
		it := r.body["items"].([]any)[0].(map[string]any)
		if it["owner"].(map[string]any)["id"] != nid || it["members"] != float64(5) {
			t.Fatal(r.raw)
		}

		// Refusals.
		admin.do("POST", "/api/admin/spaces/"+tm.sid+"/transfer", map[string]any{"userId": "NOSUCHUSER"}).want(t, 404, "user_not_found")
		admin.do("POST", "/api/admin/spaces/NOSUCHSPAC/transfer", map[string]any{"userId": nid}).want(t, 404, "not_found")
		admin.do("PATCH", "/api/users/"+tm.editorID, map[string]any{"disabled": true}).want(t, 200, "")
		admin.do("POST", "/api/admin/spaces/"+tm.sid+"/transfer", map[string]any{"userId": tm.editorID}).want(t, 409, "user_disabled")
		personal := tm.outsider.personalSpace()
		admin.do("POST", "/api/admin/spaces/"+personal+"/transfer", map[string]any{"userId": nid}).want(t, 409, "personal_space")
	})
}

func TestAdminDeleteMovesTheDirectoryToTheTrash(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{spaces: func(o *spaces.Options) { o.CloseWait = 100 * time.Millisecond }}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tm := newTeam(t, a)
		base := "/api/spaces/" + tm.sid
		p := tm.editor.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "keep me"}}, "").want(t, 201, "")
		tm.editor.draft(base+"/pages/"+p.body["id"].(string), p.body["rev"], "keep me", "draft").want(t, 200, "")
		root := filepath.Join(a.dir, "spaces", tm.sid)
		if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
			t.Fatal(err)
		}

		// Refused while a request holds it: nothing changes.
		h, err := a.mgr.Open(context.Background(), mustUser(t, a, "olive"), tm.sid)
		if err != nil {
			t.Fatal(err)
		}
		admin.do("DELETE", "/api/admin/spaces/"+tm.sid, nil).want(t, 409, "busy")
		tm.owner.do("GET", base+"/tree", nil).want(t, 200, "") // not disabled by the attempt
		h.Release()

		// Personal spaces are refused.
		admin.do("DELETE", "/api/admin/spaces/"+tm.outsider.personalSpace(), nil).want(t, 409, "personal_space")
		admin.do("DELETE", "/api/admin/spaces/"+admin.personalSpace(), nil).want(t, 409, "personal_space")

		r := admin.do("DELETE", "/api/admin/spaces/"+tm.sid, nil).want(t, 200, "")
		trashed, _ := r.body["trashed"].(string)
		if !strings.HasPrefix(trashed, "trash/"+tm.sid+"-") || len(r.body["warnings"].([]any)) != 0 {
			t.Fatal(r.raw)
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatalf("the directory is still in place: %v", err)
		}
		out, err := exec.Command("git", "-C", filepath.Join(a.dir, filepath.FromSlash(trashed)), "log", "--format=%s").Output()
		if err != nil || !strings.Contains(string(out), "keep me") {
			t.Fatalf("the repository did not survive in the trash: %q %v", out, err)
		}
		if _, err := os.Stat(filepath.Join(a.dir, "cache", tm.sid)); !os.IsNotExist(err) {
			t.Fatal("the cache was left behind")
		}
		var drafts, members int
		a.st.Q().QueryRow(context.Background(), `SELECT COUNT(*) FROM drafts WHERE space_id = ?`, tm.sid).Scan(&drafts)
		a.st.Q().QueryRow(context.Background(), `SELECT COUNT(*) FROM space_members WHERE space_id = ?`, tm.sid).Scan(&members)
		if drafts != 0 || members != 0 {
			t.Fatalf("%d drafts, %d members left", drafts, members)
		}
		// Gone for everyone, at once.
		for _, c := range []*client{tm.owner, tm.editor, tm.viewer, admin} {
			c.do("GET", base+"/tree", nil).want(t, 404, "not_found")
		}
		for _, x := range tm.editor.do("GET", "/api/spaces", nil).body["spaces"].([]any) {
			if x.(map[string]any)["id"] == tm.sid {
				t.Fatal("still in the list")
			}
		}
		if l := admin.do("GET", "/api/admin/spaces?q=team", nil).body["items"].([]any); len(l) != 0 {
			t.Fatal("still in the administration list")
		}
		admin.do("DELETE", "/api/admin/spaces/"+tm.sid, nil).want(t, 404, "not_found")
		// Other spaces are untouched.
		tm.owner.do("GET", "/api/spaces/"+tm.owner.personalSpace()+"/tree", nil).want(t, 200, "")
	})
}

func TestAdminDeleteReportsAMoveThatFailed(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		sid := tang.do("POST", "/api/spaces", map[string]any{"name": "Doomed"}).want(t, 201, "").body["id"].(string)
		// A file where the trash directory should be: the move cannot happen.
		if err := os.WriteFile(filepath.Join(a.dir, "trash"), []byte("in the way"), 0o644); err != nil {
			t.Fatal(err)
		}
		r := admin.do("DELETE", "/api/admin/spaces/"+sid, nil).want(t, 200, "")
		w := r.body["warnings"].([]any)
		if r.body["trashed"] != "" || len(w) == 0 || !strings.Contains(w[0].(string), "left where it is") {
			t.Fatal(r.raw)
		}
		// The rows are gone, the files are not.
		tang.do("GET", "/api/spaces/"+sid+"/tree", nil).want(t, 404, "not_found")
		if _, err := os.Stat(filepath.Join(a.dir, "spaces", sid, ".git")); err != nil {
			t.Fatalf("files were removed: %v", err)
		}
	})
}

func TestAdminSizesAreMeasuredInTheBackgroundAndOnDemand(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{spaces: func(o *spaces.Options) {
		o.SizeEvery, o.SizeAge = 20*time.Millisecond, time.Hour
	}}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tang := a.addUser("tang", pw, "member")
		sid := tang.personalSpace()
		tang.do("POST", "/api/spaces/"+sid+"/pages", map[string]any{"meta": map[string]any{"title": "x"}}, "").want(t, 201, "")

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { a.mgr.Run(ctx); close(done) }()
		t.Cleanup(func() { cancel(); <-done })

		deadline := time.Now().Add(5 * time.Second)
		var items []any
		for {
			items = admin.do("GET", "/api/admin/spaces", nil).want(t, 200, "").body["items"].([]any)
			ready := true
			for _, it := range items {
				if it.(map[string]any)["sizeBytes"] == nil {
					ready = false
				}
			}
			if ready {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("not measured in time: %v", items)
			}
			time.Sleep(20 * time.Millisecond)
		}
		for _, it := range items {
			m := it.(map[string]any)
			if m["sizeBytes"].(float64) <= 0 || m["sizeAt"] == nil {
				t.Fatalf("%v", m)
			}
		}

		// On demand: a file added now is counted by the next measurement.
		before := sizeOf(t, admin, sid)
		if err := os.WriteFile(filepath.Join(a.dir, "spaces", sid, "big.bin"), make([]byte, 100_000), 0o644); err != nil {
			t.Fatal(err)
		}
		r := admin.do("POST", "/api/admin/spaces/"+sid+"/size", "{}").want(t, 200, "")
		if got := r.body["sizeBytes"].(float64); got < before+100_000 || r.body["sizeAt"] == "" {
			t.Fatalf("before %v, now %s", before, r.raw)
		}
		if sizeOf(t, admin, sid) != r.body["sizeBytes"].(float64) {
			t.Fatal("the list does not show the new size")
		}
		admin.do("POST", "/api/admin/spaces/NOSUCHSPAC/size", "{}").want(t, 404, "not_found")
	})
}

func sizeOf(t *testing.T, admin *client, sid string) float64 {
	t.Helper()
	for _, it := range admin.do("GET", "/api/admin/spaces", nil).want(t, 200, "").body["items"].([]any) {
		if m := it.(map[string]any); m["id"] == sid {
			return m["sizeBytes"].(float64)
		}
	}
	t.Fatal("space not listed")
	return 0
}

func TestAdminStatus(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		a.addUser("tang", pw, "member")
		r := admin.do("GET", "/api/admin/status", nil).want(t, 200, "")
		b := r.body
		if b["version"] != "test-1" || b["spaces"] != float64(2) || b["users"] != float64(2) || b["openSpaces"] != float64(a.mgr.OpenCount()) ||
			(b["database"] != "sqlite" && b["database"] != "postgres") || !strings.HasPrefix(b["goVersion"].(string), "go") ||
			b["startedAt"] == "" || b["uptimeSeconds"].(float64) < 0 {
			t.Fatal(r.raw)
		}
		if free, ok := b["diskFreeBytes"]; ok && free.(float64) <= 0 {
			t.Fatal(r.raw)
		}
	})
}

func TestAdminSizerDoesNotWaitBetweenStaleSpaces(t *testing.T) {
	t.Parallel()
	const every = time.Second
	eachAccounts(t, accOpts{spaces: func(o *spaces.Options) { o.SizeEvery, o.SizeAge = every, time.Hour }}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		for i := 0; i < 6; i++ {
			a.addUser(fmt.Sprintf("user%d", i), pw, "member")
		}
		start := time.Now()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { a.mgr.Run(ctx); close(done) }()
		t.Cleanup(func() { cancel(); <-done })
		// Seven spaces are stale. If the sizer waited between them it would take
		// seven ticks; all are done before the second one.
		for {
			n := 0
			items := admin.do("GET", "/api/admin/spaces", nil).want(t, 200, "").body["items"].([]any)
			for _, it := range items {
				if it.(map[string]any)["sizeBytes"] != nil {
					n++
				}
			}
			if n == len(items) && n == 7 {
				break
			}
			if time.Since(start) > 2*every {
				t.Fatalf("%d of %d spaces measured after two ticks", n, len(items))
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
}
