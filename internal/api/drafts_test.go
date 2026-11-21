package api_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KazuhaHub/PetalBook/internal/store"
)

// draftPage makes a page in the client's personal space and returns its URL
// and the page as created.
func draftPage(t *testing.T, c *client, title, body string) (string, map[string]any) {
	t.Helper()
	sid := c.personalSpace()
	r := c.do("POST", "/api/spaces/"+sid+"/pages", map[string]any{"meta": map[string]any{"title": title}, "body": body}).want(t, 201, "")
	return "/api/spaces/" + sid + "/pages/" + r.body["id"].(string), r.body
}

func draftBody(rev any, title, body string) map[string]any {
	return map[string]any{"baseRev": rev, "meta": map[string]any{"title": title}, "body": body}
}

// draft stores a draft the way the web app does: replacing the version this
// client last stored or saw for that page, if any.
func (c *client) draft(url string, rev any, title, body string) result {
	m := draftBody(rev, title, body)
	if v := c.versions[url]; v != "" {
		m["prev"] = v
	}
	r := c.do("PUT", url+"/draft", m)
	if r.code == 200 {
		c.versions[url] = r.body["version"].(string)
	}
	return r
}

// commit commits the draft this client last stored for the page.
func (c *client) commit(url string) result {
	r := c.do("POST", url+"/commit", map[string]any{"version": c.versions[url]})
	if r.code == 200 {
		delete(c.versions, url)
	}
	return r
}

// forget drops what the client knows of its draft (it saved the file, or the draft was discarded).
func (c *client) forget(url string) { delete(c.versions, url) }

func fileOf(t *testing.T, a *acc, c *client, p map[string]any) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(a.dir, "spaces", c.personalSpace(), filepath.FromSlash(p["path"].(string))))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDraftStoredApartFromTheFile(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		c := a.setup("kazuha", pw)
		url, p := draftPage(t, c, "暮雪", "one\n")
		before := fileOf(t, a, c, p)

		r := c.draft(url, p["rev"], "暮雪 2", "two\n").want(t, 200, "")
		if r.body["baseRev"] != p["rev"] || r.body["stale"] != false || !rfc3339.MatchString(r.body["updated"].(string)) {
			t.Fatal(r.raw)
		}
		// The file, the listing and search still show the saved version.
		if fileOf(t, a, c, p) != before {
			t.Fatal("storing a draft touched the file")
		}
		g := c.do("GET", url, nil).want(t, 200, "")
		if g.body["body"] != "one\n" || g.body["meta"].(map[string]any)["title"] != "暮雪" || g.body["rev"] != p["rev"] {
			t.Fatalf("meta and body are not the file's: %s", g.raw)
		}
		d := g.body["draft"].(map[string]any)
		if d["body"] != "two\n" || d["baseRev"] != p["rev"] || d["stale"] != false || d["meta"].(map[string]any)["title"] != "暮雪 2" || d["updated"] == nil {
			t.Fatalf("%s", g.raw)
		}
		sid := c.personalSpace()
		items := c.do("GET", "/api/spaces/"+sid+"/pages?q=two", nil).body["items"].([]any)
		if len(items) != 0 {
			t.Fatal("search finds a draft")
		}
		if items := c.do("GET", "/api/spaces/"+sid+"/pages", nil).body["items"].([]any); items[0].(map[string]any)["title"] != "暮雪" {
			t.Fatal("the list shows a draft")
		}
		// Replacing it keeps one draft.
		c.draft(url, p["rev"], "暮雪 3", "three\n").want(t, 200, "")
		var n int
		a.st.Q().QueryRow(context.Background(), `SELECT COUNT(*) FROM drafts`).Scan(&n)
		if n != 1 {
			t.Fatalf("%d drafts", n)
		}
		if c.do("GET", url, nil).body["draft"].(map[string]any)["body"] != "three\n" {
			t.Fatal("not replaced")
		}
		// Discarding.
		c.do("DELETE", url+"/draft", nil).want(t, 204, "")
		c.do("DELETE", url+"/draft", nil).want(t, 204, "")
		if _, has := c.do("GET", url, nil).body["draft"]; has {
			t.Fatal("draft after delete")
		}
	})
}

func TestDraftHeaderOrderIsKept(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		c := a.setup("kazuha", pw)
		url, p := draftPage(t, c, "T", "x\n")
		raw := `{"baseRev":"` + p["rev"].(string) + `","meta":{"zeta":1,"title":"T","alpha":[3,"x",{"b":1,"a":2}],"mid":null},"body":"y"}`
		c.do("PUT", url+"/draft", raw).want(t, 200, "")
		got := c.do("GET", url, nil).raw
		i := strings.Index(got, `"draft"`)
		if i < 0 || !strings.Contains(got[i:], `"meta":{"zeta":1,"title":"T","alpha":[3,"x",{"b":1,"a":2}],"mid":null}`) {
			t.Fatalf("header order lost:\n%s", got)
		}
	})
}

func TestDraftChecksAreTheChecksOfASave(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		c := a.setup("kazuha", pw)
		url, p := draftPage(t, c, "T", "x\n")
		sid := c.personalSpace()
		c.do("PUT", url+"/draft", map[string]any{"meta": map[string]any{}, "body": "x"}).want(t, 400, "invalid")         // no baseRev
		c.do("PUT", url+"/draft", map[string]any{"baseRev": p["rev"], "body": "x"}).want(t, 400, "invalid")              // no meta
		c.do("PUT", url+"/draft", map[string]any{"baseRev": p["rev"], "meta": map[string]any{}}).want(t, 400, "invalid") // no body
		c.do("PUT", url+"/draft", map[string]any{"baseRev": p["rev"], "meta": map[string]any{"id": "0000000000"}, "body": "x"}).want(t, 400, "invalid")
		c.do("PUT", url+"/draft", []byte(nil)).want(t, 400, "invalid")
		c.do("PUT", url+"/draft", "{\"baseRev\":\""+p["rev"].(string)+"\",\"meta\":{},\"body\":\"bad \xff\"}").want(t, 400, "invalid")
		c.do("PUT", url+"/draft", "{nope").want(t, 400, "invalid")
		// A NUL cannot be stored as text on every database, so it is refused on all.
		c.draft(url, p["rev"], "T", "a\u0000b").want(t, 400, "invalid")
		// A meta.id equal to the page's is fine.
		c.do("PUT", url+"/draft", map[string]any{"baseRev": p["rev"], "meta": map[string]any{"id": p["id"]}, "body": "x"}).want(t, 200, "")
		// A page that does not exist, or cannot be an id.
		c.do("PUT", "/api/spaces/"+sid+"/pages/0000000000/draft", draftBody(p["rev"], "T", "x")).want(t, 404, "not_found")
		c.do("PUT", "/api/spaces/"+sid+"/pages/short/draft", draftBody(p["rev"], "T", "x")).want(t, 404, "not_found")
		c.do("POST", "/api/spaces/"+sid+"/pages/0000000000/commit", map[string]any{"version": "x"}).want(t, 404, "not_found")
		// A commit names the version it commits.
		c.do("POST", url+"/commit", map[string]any{}).want(t, 400, "invalid")
		c.do("POST", url+"/commit", map[string]any{"version": ""}).want(t, 400, "invalid")
		c.do("POST", url+"/commit", nil).want(t, 400, "invalid")
		// The same wrong content-type rule as everywhere.
		c.do("PUT", url+"/draft", "{}", "Content-Type", "text/plain").want(t, 415, "invalid")
		c.do("POST", url+"/commit", nil, "Content-Type", "").want(t, 415, "invalid")
	})
}

func TestDraftsOfDifferentPeopleAreSeparate(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		ctx := context.Background()
		c := a.setup("kazuha", pw)
		url, p := draftPage(t, c, "T", "x\n")
		c.draft(url, p["rev"], "T", "kazuha's words\n").want(t, 200, "")
		// A second user in the same space (spaces have only owners in this
		// milestone, so add the membership directly).
		other := a.addUser("tang", pw, "member")
		sid := c.personalSpace()
		if err := store.AddMember(ctx, a.st.Q(), sid, mustUserID(t, a, "tang"), "owner", a.clock.Now()); err != nil {
			t.Fatal(err)
		}
		a.mgr.UserChanged(ctx, store.User{ID: mustUserID(t, a, "tang")})
		a.dropMembershipCache(t, "tang")

		g := other.do("GET", url, nil).want(t, 200, "")
		if _, has := g.body["draft"]; has {
			t.Fatalf("one user sees another's draft: %s", g.raw)
		}
		other.draft(url, p["rev"], "T", "tang's words\n").want(t, 200, "")
		if c.do("GET", url, nil).body["draft"].(map[string]any)["body"] != "kazuha's words\n" {
			t.Fatal("a draft was replaced by another user's draft")
		}
		// Deleting and committing act on the caller's own.
		other.do("DELETE", url+"/draft", nil).want(t, 204, "")
		if c.do("GET", url, nil).body["draft"] == nil {
			t.Fatal("someone else's delete removed my draft")
		}
		// A commit by the other user cannot reach mine, even with my version.
		other.versions[url] = c.versions[url]
		other.commit(url).want(t, 409, "conflict")
		if g := c.do("GET", url, nil); g.body["body"] != "x\n" || g.body["draft"] == nil {
			t.Fatalf("%s", g.raw)
		}
		other.forget(url)
		// A save by one leaves both drafts, now stale: neither text is in the file.
		other.draft(url, p["rev"], "T", "tang again\n").want(t, 200, "")
		c.do("PUT", url, draftBody(p["rev"], "T", "kazuha saves\n")).want(t, 200, "")
		if own, _ := c.do("GET", url, nil).body["draft"].(map[string]any); own == nil || own["stale"] != true {
			t.Fatal("a save took the saver's own draft away")
		}
		g = other.do("GET", url, nil)
		d := g.body["draft"].(map[string]any)
		if d["body"] != "tang again\n" || d["stale"] != true {
			t.Fatalf("the other draft should remain, now stale: %s", g.raw)
		}
	})
}

func TestSaveLeavesTheCallersDraftUnlessItSaysTheSame(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		c := a.setup("kazuha", pw)
		url, p := draftPage(t, c, "T", "x\n")
		c.draft(url, p["rev"], "T", "draft\n").want(t, 200, "")
		// A refused save keeps the draft.
		c.do("PUT", url, draftBody("0000000000000000000000000000000000000000", "T", "z\n")).want(t, 409, "conflict")
		if c.do("GET", url, nil).body["draft"] == nil {
			t.Fatal("a conflicting save removed the draft")
		}
		// A save of other text leaves the draft, stale: its text is nowhere else.
		saved := c.do("PUT", url, draftBody(p["rev"], "T", "saved\n")).want(t, 200, "")
		d, _ := c.do("GET", url, nil).body["draft"].(map[string]any)
		if d == nil || d["body"] != "draft\n" || d["stale"] != true {
			t.Fatalf("a save of other text removed the draft: %v", d)
		}
		// A save of the draft's own text leaves it nothing to add: it goes.
		c.do("PUT", url, draftBody(saved.body["rev"], "T", "draft\n")).want(t, 200, "")
		if _, has := c.do("GET", url, nil).body["draft"]; has {
			t.Fatal("a draft that says what was saved stayed")
		}
	})
}

func TestDeletingAPageDeletesItsDrafts(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		c := a.setup("kazuha", pw)
		url, p := draftPage(t, c, "T", "x\n")
		c.draft(url, p["rev"], "T", "draft\n").want(t, 200, "")
		// Another user's draft of the same page, written directly.
		ctx := context.Background()
		other := a.addUser("tang", pw, "member")
		_ = other
		if _, err := store.PutDraft(ctx, a.st.Q(), store.Draft{SpaceID: c.personalSpace(), PageID: p["id"].(string), UserID: mustUserID(t, a, "tang"),
			BaseRev: "r", Meta: "{}", Body: "b", Updated: a.clock.Now()}, "", time.Minute, time.Hour); err != nil {
			t.Fatal(err)
		}
		c.do("DELETE", url, nil).want(t, 204, "")
		var n int
		a.st.Q().QueryRow(ctx, `SELECT COUNT(*) FROM drafts`).Scan(&n)
		if n != 0 {
			t.Fatalf("%d drafts left for a deleted page", n)
		}
	})
}

func TestCommitWritesTheDraftLikeASave(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		c := a.setup("kazuha", pw)
		url, p := draftPage(t, c, "Old title", "one\n")
		// No draft: nothing to commit; the answer shows the page as it is.
		r := c.do("POST", url+"/commit", map[string]any{"version": "never-stored"}).want(t, 409, "conflict")
		if r.body["draft"] != nil || r.body["current"].(map[string]any)["rev"] != p["rev"] {
			t.Fatal(r.raw)
		}
		st := c.do("PUT", url+"/draft", map[string]any{"baseRev": p["rev"], "meta": map[string]any{"title": "New title", "tags": []any{"a"}}, "body": "two\n"}).want(t, 200, "")
		c.versions[url] = st.body["version"].(string)
		r = c.commit(url).want(t, 200, "")
		if r.body["rev"] == p["rev"] || r.body["body"] != "two\n" || r.body["meta"].(map[string]any)["title"] != "New title" || r.body["path"] != "New title.md" {
			t.Fatalf("%s", r.raw)
		}
		if got := fileOf(t, a, c, r.body); !strings.Contains(got, "two\n") || !strings.Contains(got, "New title") {
			t.Fatal(got)
		}
		if _, has := c.do("GET", "/api/spaces/"+c.personalSpace()+"/pages/"+p["id"].(string), nil).body["draft"]; has {
			t.Fatal("the draft is still there")
		}
	})
}

func TestStaleDraftConflictsAndIsHeld(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		ctx := context.Background()
		c := a.setup("kazuha", pw)
		url, p := draftPage(t, c, "T", "one\n")
		c.draft(url, p["rev"], "T", "my draft\n").want(t, 200, "")
		// The file moves on under the draft.
		moved := c.do("PUT", url, draftBody(p["rev"], "T", "file moved\n")).want(t, 200, "") // the draft stays, stale
		old := c.draft(url, p["rev"], "T", "my draft\n").want(t, 200, "")
		if old.body["stale"] != true {
			t.Fatalf("a draft on an old rev is not stale: %s", old.raw)
		}
		if g := c.do("GET", url, nil); g.body["draft"].(map[string]any)["stale"] != true {
			t.Fatal(g.raw)
		}
		r := c.commit(url).want(t, 409, "conflict")
		cur := r.body["current"].(map[string]any)
		if cur["rev"] != moved.body["rev"] || cur["body"] != "file moved\n" {
			t.Fatalf("%s", r.raw)
		}
		if c.do("GET", url, nil).body["draft"] == nil {
			t.Fatal("a stale draft was dropped")
		}
		// The sweeper leaves it alone however long it waits.
		a.clock.Advance(time.Hour)
		if res := a.drafts.Sweep(ctx); res.Applied != 0 || res.Held != 0 {
			t.Fatalf("%+v", res)
		}
		if c.do("GET", url, nil).body["draft"] == nil {
			t.Fatal("the sweeper dropped a held draft")
		}
		// Storing it again with the current rev lets it through.
		c.versions[url] = c.versions[url] // the held draft is still the one the client knows
		c.draft(url, moved.body["rev"], "T", "resolved\n").want(t, 200, "")
		a.clock.Advance(3 * time.Minute)
		if res := a.drafts.Sweep(ctx); res.Applied != 1 {
			t.Fatalf("%+v", res)
		}
		if g := c.do("GET", url, nil); g.body["body"] != "resolved\n" || g.body["draft"] != nil {
			t.Fatal(g.raw)
		}
	})
}

func TestSweeperAppliesWhenIdleOrOld(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		ctx := context.Background()
		c := a.setup("kazuha", pw)
		url, p := draftPage(t, c, "T", "one\n")
		c.draft(url, p["rev"], "T", "v1\n").want(t, 200, "")
		a.clock.Advance(119 * time.Second)
		if res := a.drafts.Sweep(ctx); res.Applied != 0 {
			t.Fatalf("applied before the idle time: %+v", res)
		}
		// Typing on pushes the idle deadline out ...
		c.draft(url, p["rev"], "T", "v2\n").want(t, 200, "")
		a.clock.Advance(119 * time.Second)
		if res := a.drafts.Sweep(ctx); res.Applied != 0 {
			t.Fatalf("%+v", res)
		}
		// ... but not past 10 minutes from the first change.
		for i := 0; i < 4; i++ {
			c.draft(url, p["rev"], "T", "v3\n").want(t, 200, "")
			a.clock.Advance(100 * time.Second)
		}
		// 119 + 119 + 400 = 638 seconds since the first draft at t=0.
		if res := a.drafts.Sweep(ctx); res.Applied != 1 {
			t.Fatalf("a draft older than the maximum was not applied: %+v", res)
		}
		if g := c.do("GET", url, nil); g.body["body"] != "v3\n" || g.body["draft"] != nil {
			t.Fatal(g.raw)
		}
		c.forget(url)
		// Idle path.
		c.draft(url, g(c, url)["rev"], "T", "v4\n").want(t, 200, "")
		a.clock.Advance(2*time.Minute + time.Second)
		if res := a.drafts.Sweep(ctx); res.Applied != 1 {
			t.Fatalf("%+v", res)
		}
		if c.do("GET", url, nil).body["body"] != "v4\n" {
			t.Fatal("not applied")
		}
	})
}

func g(c *client, url string) map[string]any { return c.do("GET", url, nil).body }

func TestApplyingAnUnchangedDraftWritesNothing(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		ctx := context.Background()
		c := a.setup("kazuha", pw)
		url, p := draftPage(t, c, "T", "same\n")
		sid := c.personalSpace()
		h, err := a.mgr.Open(ctx, mustUserID(t, a, "kazuha"), sid)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.Space.Flush(); err != nil {
			t.Fatal(err)
		}
		commits := func() int {
			out, err := exec.Command("git", "-C", filepath.Join(a.dir, "spaces", sid), "rev-list", "--count", "HEAD").Output()
			if err != nil {
				t.Fatal(err)
			}
			return atoi(strings.TrimSpace(string(out)))
		}
		file := filepath.Join(a.dir, "spaces", sid, "T.md")
		st0, _ := os.Stat(file)
		n0 := commits()

		// Through the user's commit ...
		c.draft(url, p["rev"], "T", "same\n").want(t, 200, "")
		r := c.commit(url).want(t, 200, "")
		if r.body["rev"] != p["rev"] {
			t.Fatalf("rev changed: %s", r.raw)
		}
		// ... and through the sweeper.
		c.draft(url, p["rev"], "T", "same\n").want(t, 200, "")
		a.clock.Advance(3 * time.Minute)
		if res := a.drafts.Sweep(ctx); res.Applied != 1 {
			t.Fatalf("%+v", res)
		}
		if err := h.Space.Flush(); err != nil {
			t.Fatal(err)
		}
		st1, _ := os.Stat(file)
		if !st1.ModTime().Equal(st0.ModTime()) {
			t.Fatal("the file was rewritten")
		}
		if commits() != n0 {
			t.Fatal("a commit was made for nothing")
		}
		if g := c.do("GET", url, nil); g.body["rev"] != p["rev"] || g.body["draft"] != nil {
			t.Fatal(g.raw)
		}
	})
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

func TestADraftOutlivesItsPageFileUntilThePageIsDeleted(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		ctx := context.Background()
		c := a.setup("kazuha", pw)
		url, p := draftPage(t, c, "T", "x\n")
		c.draft(url, p["rev"], "T", "text that exists nowhere else\n").want(t, 200, "")
		// The file disappears outside the server.
		if err := os.Remove(filepath.Join(a.dir, "spaces", c.personalSpace(), "T.md")); err != nil {
			t.Fatal(err)
		}
		a.clock.Advance(3 * time.Minute)
		if res := a.drafts.Sweep(ctx); res.Held != 1 {
			t.Fatalf("%+v", res)
		}
		var body string
		a.st.Q().QueryRow(ctx, `SELECT body FROM drafts`).Scan(&body)
		if body != "text that exists nowhere else\n" {
			t.Fatalf("the draft of a vanished page was deleted: %q", body)
		}
		c.commit(url).want(t, 404, "not_found")
		var n int
		a.st.Q().QueryRow(ctx, `SELECT COUNT(*) FROM drafts`).Scan(&n)
		if n != 1 {
			t.Fatal("a commit on a missing page deleted the draft")
		}
	})
}

func TestTypingPastTheDraftMaximumRebasesInsteadOfHoldingForever(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		ctx := context.Background()
		c := a.setup("kazuha", pw)
		url, p := draftPage(t, c, "T", "one\n")
		c.draft(url, p["rev"], "T", "typing…\n").want(t, 200, "")
		// The person never pauses for the 2 minutes of --draft-idle: a store every 100
		// seconds. Only the 10 minutes since the first store can make it due.
		for range 5 {
			a.clock.Advance(100 * time.Second)
			c.draft(url, p["rev"], "T", "typing on…\n").want(t, 200, "")
			if res := a.drafts.Sweep(ctx); res.Applied != 0 {
				t.Fatalf("applied before the maximum age: %+v", res)
			}
		}
		a.clock.Advance(100 * time.Second) // 600 seconds since the first store
		c.draft(url, p["rev"], "T", "typing on…\n").want(t, 200, "")
		v1 := c.versions[url]
		if res := a.drafts.Sweep(ctx); res.Applied != 1 {
			t.Fatalf("the draft did not fall due by its maximum age: %+v", res)
		}
		// The next store still carries the old version and base: refused, not held forever.
		r := c.do("PUT", url+"/draft", map[string]any{"prev": v1, "baseRev": p["rev"], "meta": map[string]any{"title": "T"}, "body": "typing on, more\n"}).want(t, 409, "conflict")
		if v, has := r.body["draft"]; !has || v != nil {
			t.Fatalf("draft must be present and null: %s", r.raw)
		}
		cur := r.body["current"].(map[string]any)
		if cur["body"] != "typing on…\n" || cur["rev"] == p["rev"] {
			t.Fatalf("current is not the applied text: %s", r.raw)
		}
		// Committing the version it knew says the same, and writes nothing.
		cm := c.do("POST", url+"/commit", map[string]any{"version": v1}).want(t, 409, "conflict")
		if cm.body["draft"] != nil || cm.body["current"].(map[string]any)["rev"] != cur["rev"] {
			t.Fatalf("%s", cm.raw)
		}
		// The client re-bases: no draft known, the current rev as base. It works, and it is not stale or held.
		re := c.do("PUT", url+"/draft", map[string]any{"baseRev": cur["rev"], "meta": map[string]any{"title": "T"}, "body": "typing on, more\n"}).want(t, 200, "")
		if re.body["stale"] != false {
			t.Fatal(re.raw)
		}
		c.versions[url] = re.body["version"].(string)
		c.commit(url).want(t, 200, "")
		if g := c.do("GET", url, nil); g.body["body"] != "typing on, more\n" || g.body["draft"] != nil {
			t.Fatal(g.raw)
		}
	})
}

func TestDraftVersionsGuardAgainstAnotherDevice(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		c := a.setup("kazuha", pw)
		url, p := draftPage(t, c, "T", "one\n")
		one := c.do("PUT", url+"/draft", draftBody(p["rev"], "T", "phone\n")).want(t, 200, "")
		v1 := one.body["version"].(string)
		if v1 == "" {
			t.Fatal(one.raw)
		}
		// A second device that knows of no draft cannot replace it: it is shown the one there is.
		r := c.do("PUT", url+"/draft", draftBody(p["rev"], "T", "laptop\n")).want(t, 409, "conflict")
		d := r.body["draft"].(map[string]any)
		if d["version"] != v1 || d["body"] != "phone\n" || d["baseRev"] != p["rev"] || d["stale"] != false || r.body["current"].(map[string]any)["rev"] != p["rev"] {
			t.Fatalf("%s", r.raw)
		}
		// So cannot one that knows of another version.
		r = c.do("PUT", url+"/draft", map[string]any{"prev": "someone-elses", "baseRev": p["rev"], "meta": map[string]any{"title": "T"}, "body": "x"}).want(t, 409, "conflict")
		if r.body["draft"].(map[string]any)["version"] != v1 {
			t.Fatal(r.raw)
		}
		// Nothing was stored by the refused ones.
		if g := c.do("GET", url, nil); g.body["draft"].(map[string]any)["body"] != "phone\n" || g.body["draft"].(map[string]any)["version"] != v1 {
			t.Fatal(g.raw)
		}
		// The right version replaces it, with a new one, even in the same instant.
		two := c.do("PUT", url+"/draft", map[string]any{"prev": v1, "baseRev": p["rev"], "meta": map[string]any{"title": "T"}, "body": "phone, more\n"}).want(t, 200, "")
		three := c.do("PUT", url+"/draft", map[string]any{"prev": two.body["version"], "baseRev": p["rev"], "meta": map[string]any{"title": "T"}, "body": "phone, more\n"}).want(t, 200, "")
		if two.body["version"] == v1 || three.body["version"] == two.body["version"] || three.body["version"] == v1 {
			t.Fatalf("versions repeat: %v %v %v", v1, two.body["version"], three.body["version"])
		}
		v3 := three.body["version"].(string)
		// Commit and delete name the version too.
		cm := c.do("POST", url+"/commit", map[string]any{"version": v1}).want(t, 409, "conflict")
		if cm.body["draft"].(map[string]any)["version"] != v3 {
			t.Fatal(cm.raw)
		}
		c.do("DELETE", url+"/draft?version="+v1, nil).want(t, 409, "conflict")
		if c.do("GET", url, nil).body["draft"] == nil {
			t.Fatal("a refused delete deleted")
		}
		c.do("DELETE", url+"/draft?version="+v3, nil).want(t, 204, "")
		c.do("DELETE", url+"/draft", nil).want(t, 204, "") // none, unversioned: fine
		// Knowing of a draft that has been deleted since: refused, with none shown.
		r = c.do("PUT", url+"/draft", map[string]any{"prev": v3, "baseRev": p["rev"], "meta": map[string]any{"title": "T"}, "body": "x"}).want(t, 409, "conflict")
		if r.body["draft"] != nil {
			t.Fatal(r.raw)
		}
	})
}

func TestTwoDevicesStoringAtOnceHaveOneWinner(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		c := a.setup("kazuha", pw)
		url, p := draftPage(t, c, "T", "one\n")
		const n = 8
		codes := make([]int, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				dev := a.newClient("")
				dev.cookies = c.cookies
				codes[i] = dev.do("PUT", url+"/draft", draftBody(p["rev"], "T", fmt.Sprintf("device %d\n", i))).code
			}()
		}
		wg.Wait()
		wins := 0
		for _, code := range codes {
			switch code {
			case 200:
				wins++
			case 409:
			default:
				t.Errorf("status %d", code)
			}
		}
		if wins != 1 {
			t.Fatalf("codes %v", codes)
		}
	})
}

func TestVersionedDraftDelete(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		ctx := context.Background()
		c := a.setup("kazuha", pw)
		url, p := draftPage(t, c, "T", "one\n")
		sid := c.personalSpace()

		// The right version: 204.
		v := c.draft(url, p["rev"], "T", "first\n").want(t, 200, "").body["version"].(string)
		c.do("DELETE", url+"/draft?version="+v, nil).want(t, 204, "")
		c.forget(url)

		// Unversioned with no draft: 204.
		c.do("DELETE", url+"/draft", nil).want(t, 204, "")

		// The server applied the draft by itself: a delete naming its version must find out.
		v = c.draft(url, p["rev"], "T", "applied by the sweeper\n").want(t, 200, "").body["version"].(string)
		a.clock.Advance(3 * time.Minute)
		if res := a.drafts.Sweep(ctx); res.Applied != 1 {
			t.Fatalf("%+v", res)
		}
		r := c.do("DELETE", url+"/draft?version="+v, nil).want(t, 409, "conflict")
		if d, has := r.body["draft"]; !has || d != nil {
			t.Fatalf("draft must be present and null: %s", r.raw)
		}
		if r.body["current"].(map[string]any)["body"] != "applied by the sweeper\n" {
			t.Fatalf("current is not the applied text: %s", r.raw)
		}
		// Without the version the same call is still 204.
		c.do("DELETE", url+"/draft", nil).want(t, 204, "")

		// A page that is gone is 404.
		c.do("DELETE", "/api/spaces/"+sid+"/pages/0000000000/draft?version=x", nil).want(t, 404, "not_found")
	})
}
