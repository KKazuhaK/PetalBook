package api_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// restPage reads a page the way the app does.
func (e *mcpEnv) restPage(t *testing.T, id string) map[string]any {
	t.Helper()
	return e.tm.owner.do("GET", spacePath(e.sid)+"/pages/"+id, nil).want(t, 200, "").body
}

func TestMCPCreate(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		e := newMCP(t, a)
		out := e.w.tool("page_create", e.args("folder", "Tang/Late", "title", " 暮雪 ", "subtitle", "s", "source", "李", "tags", []string{"玉兰", " 玉兰 ", "", "雪"},
			"type", "poem", "props", map[string]any{"status": "draft", "n": 12, "ratio": 1.5, "list": []any{"a", 2}}, "body", "黄昏\n")).ok(t)
		pg := out.page(t)
		if out.out["replayed"] != false || pg["title"] != "暮雪" || pg["path"] != "Tang/Late/暮雪.md" || pg["folder"] != "Tang/Late" || pg["type"] != "poem" ||
			fmt.Sprint(pg["tags"]) != "[玉兰 雪]" || pg["space"] != e.sid || pg["rev"] == "" || pg["excerpt"] != "黄昏" {
			t.Fatalf("%v", pg)
		}
		// The file has what was sent, in the order the header is written in.
		rp := e.restPage(t, pg["id"].(string))
		h := rp["header"].(string)
		order := []string{"id:", "title:", "subtitle:", "source:", "tags:", "created:", "type:"}
		last := -1
		for _, k := range order {
			i := strings.Index(h, k)
			if i < 0 || i < last && k != "created:" {
				t.Fatalf("%q in %q after %d", k, h, last)
			}
			if i > last {
				last = i
			}
		}
		for _, k := range []string{"status: draft", "n: 12", "ratio: 1.5", "list: [a, 2]"} {
			if !strings.Contains(h, k) {
				t.Fatalf("%q is not in %q", k, h)
			}
		}
		if rp["body"] != "黄昏\n" {
			t.Fatalf("%v", rp["body"])
		}
		// Nothing but a title and the like is needed.
		bare := e.w.tool("page_create", e.args()).ok(t).page(t)
		if bare["untitled"] != true || bare["path"] == "" {
			t.Fatalf("%v", bare)
		}
		// Refusals: the header fields that are not built in, the body, the template.
		e.w.tool("page_create", e.args("props", map[string]any{"title": "x"})).refused(t, "invalid")
		e.w.tool("page_create", e.args("props", map[string]any{"id": "x"})).refused(t, "invalid")
		e.w.tool("page_create", e.args("props", map[string]any{"publish": true})).refused(t, "invalid")
		if r := e.w.tool("page_create", e.args("props", []any{1})); r.rpcErr == nil || r.rpcErr["code"] != float64(-32602) {
			t.Fatalf("%s", r.r.raw)
		}
		e.w.tool("page_create", e.args("tz", 900)).refused(t, "invalid")
		e.w.tool("page_create", e.args("tags", []string{"a\nb"})).refused(t, "invalid")
		e.w.tool("page_create", e.args("folder", "../x")).refused(t, "invalid")
		if r := e.w.tool("page_create", e.args("template", "no such template")); !r.isErr {
			t.Fatalf("%s", r.r.raw)
		}
		over := strings.Repeat("x", 1<<20+1)
		d := e.w.tool("page_create", e.args("body", over)).refused(t, "too_large").out["details"].(map[string]any)
		if d["limitBytes"] != float64(1<<20) || d["sizeBytes"] != float64(1<<20+1) {
			t.Fatalf("%v", d)
		}
		e.w.tool("page_create", e.args("title", "Exactly a mebibyte", "body", over[:1<<20])).ok(t)

		// A retry with the same requestId is the same page; another credential's is not.
		first := e.w.tool("page_create", e.args("title", "Once", "requestId", "r-1")).ok(t)
		again := e.w.tool("page_create", e.args("title", "Once", "requestId", "r-1")).ok(t)
		if first.out["replayed"] != false || again.out["replayed"] != true || again.page(t)["id"] != first.page(t)["id"] {
			t.Fatalf("%s %s", first.r.raw, again.r.raw)
		}
		other := a.bearer(grant(t, a, e.tm.owner, "write", e.sid).access, "")
		if o := other.tool("page_create", e.args("title", "Once", "requestId", "r-1")).ok(t); o.out["replayed"] != false || o.page(t)["id"] == first.page(t)["id"] {
			t.Fatalf("%s", o.r.raw)
		}
		// A create that failed is not remembered: the retry makes it.
		e.w.tool("page_create", e.args("title", "Late", "tags", []string{"a\nb"}, "requestId", "r-2")).refused(t, "invalid")
		if r := e.w.tool("page_create", e.args("title", "Late", "requestId", "r-2")).ok(t); r.out["replayed"] != false {
			t.Fatal(r.r.raw)
		}
		e.w.tool("page_create", e.args("requestId", strings.Repeat("r", 101))).refused(t, "invalid")
		n := len(e.r.tool("pages_search", e.args("q", "Once")).ok(t).items(t))
		if n != 2 {
			t.Fatalf("%d pages named Once, want one for each credential", n)
		}
	})
}

func TestMCPUpdate(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		e := newMCP(t, a)
		pg := e.create(t, map[string]any{"folder": "Tang", "title": "Doc", "tags": []string{"a", "b"}, "props": map[string]any{"keep": "me", "n": 3}, "body": "黄昏 one\n黄昏 two\n"})
		id := pg["id"].(string)
		before := e.restPage(t, id)
		up := func(rev any, kv ...any) toolRes {
			return e.w.tool("page_update", e.args(append([]any{"id", id, "baseRev", rev}, kv...)...))
		}

		// Edits: in order, each text exactly once; the rest of the file byte for byte.
		r := up(before["rev"], "edits", []any{map[string]any{"old": "one", "new": "ONE"}, map[string]any{"old": "ONE\n", "new": "1\n"}}).ok(t).page(t)
		after := e.restPage(t, id)
		if after["body"] != "黄昏 1\n黄昏 two\n" || after["header"] != before["header"] || r["rev"] != after["rev"] || r["rev"] == before["rev"] {
			t.Fatalf("%v", after)
		}
		rev := r["rev"]
		// A text that is not there, or is there twice, or overlaps itself: nothing is written.
		nm := up(rev, "edits", []any{map[string]any{"old": "黄昏 1", "new": "x"}, map[string]any{"old": "missing", "new": "x"}}).refused(t, "edit_no_match")
		if nm.out["details"].(map[string]any)["index"] != float64(1) {
			t.Fatalf("%v", nm.out)
		}
		am := up(rev, "edits", []any{map[string]any{"old": "黄昏", "new": "x"}}).refused(t, "edit_ambiguous")
		if am.out["details"].(map[string]any)["index"] != float64(0) {
			t.Fatalf("%v", am.out)
		}
		up(rev, "title", "Doc2", "edits", []any{map[string]any{"old": "\n", "new": "x"}}).refused(t, "edit_ambiguous")
		if got := e.restPage(t, id); got["rev"] != rev || got["path"] != "Tang/Doc.md" {
			t.Fatal("something was written by an edit that failed")
		}
		// Overlapping occurrences are more than one.
		e.w.tool("page_update", e.args("id", id, "baseRev", rev, "body", "aaa\n")).ok(t)
		rev = e.restPage(t, id)["rev"]
		up(rev, "edits", []any{map[string]any{"old": "aa", "new": "b"}}).refused(t, "edit_ambiguous")
		// Line ends as the file has them.
		up(rev, "edits", []any{map[string]any{"old": "aaa\r\n", "new": "x\r\ny\r\n"}}).ok(t)
		if got := e.restPage(t, id)["body"]; got != "x\ny\n" {
			t.Fatalf("%q", got)
		}
		rev = e.restPage(t, id)["rev"]
		// What the call may not be.
		up(rev, "edits", []any{map[string]any{"old": "", "new": "x"}}).refused(t, "invalid")
		up(rev, "body", "x", "edits", []any{map[string]any{"old": "x", "new": "y"}}).refused(t, "invalid")
		many := make([]any, 51)
		for i := range many {
			many[i] = map[string]any{"old": "x", "new": "x"}
		}
		up(rev, "edits", many).refused(t, "invalid")
		up(rev).refused(t, "invalid")
		e.w.tool("page_update", e.args("id", id, "baseRev", "", "body", "x")).refused(t, "invalid")
		if r := e.w.tool("page_update", e.args("id", id, "body", "x")); r.rpcErr == nil {
			t.Fatalf("baseRev is required: %s", r.r.raw)
		}
		up(rev, "set", map[string]any{"title": "x"}).refused(t, "invalid")
		up(rev, "set", map[string]any{"n": 1}, "unset", []string{"n"}).refused(t, "invalid")
		up(rev, "unset", []string{"id"}).refused(t, "invalid")
		up(rev, "tags", []string{"a\nb"}).refused(t, "invalid")
		e.w.tool("page_update", e.args("id", "01JD3K8Q7Z", "baseRev", rev, "body", "x")).refused(t, "not_found")
		up(rev, "body", strings.Repeat("x", 1<<20+1)).refused(t, "too_large")
		up(rev, "edits", []any{map[string]any{"old": "x", "new": strings.Repeat("y", 1<<20)}}).refused(t, "too_large")

		// A change that leaves the file as it is writes nothing and answers the same rev.
		same := up(rev, "body", "x\ny\n", "title", "Doc", "tags", []string{"a", "b"}, "set", map[string]any{"keep": "me"}).ok(t).page(t)
		if same["rev"] != rev {
			t.Fatalf("%v", same)
		}
		// Fields: replaced when given, removed with "" and [], other fields set and unset; the rest as it was.
		r = up(rev, "subtitle", "sub", "source", "src", "type", "poem", "tags", []string{"b", "c"}, "set", map[string]any{"n": 4, "added": []any{1, "x"}, "keep": nil}, "unset", []string{"missing"}).ok(t).page(t)
		if r["subtitle"] != "sub" || r["source"] != "src" || r["type"] != "poem" || fmt.Sprint(r["tags"]) != "[b c]" || r["props"].(map[string]any)["n"] != float64(4) {
			t.Fatalf("%v", r)
		}
		if _, ok := r["props"].(map[string]any)["keep"]; ok {
			t.Fatalf("keep was not removed: %v", r["props"])
		}
		h := e.restPage(t, id)["header"].(string)
		if !strings.Contains(h, "subtitle: sub") || !strings.Contains(h, "added:") || strings.Contains(h, "keep:") || strings.Index(h, "id:") > strings.Index(h, "title:") {
			t.Fatalf("%s", h)
		}
		rev = r["rev"]
		r = up(rev, "subtitle", "", "source", "", "type", "", "tags", []string{}, "unset", []string{"added"}).ok(t).page(t)
		h = e.restPage(t, id)["header"].(string)
		for _, k := range []string{"subtitle", "source", "type", "tags", "added"} {
			if strings.Contains(h, k+":") {
				t.Errorf("%s is still in %q", k, h)
			}
		}
		if r["title"] != "Doc" || r["props"].(map[string]any)["n"] != float64(4) {
			t.Fatalf("%v", r)
		}
		rev = r["rev"]
		// A new title renames the file.
		r = up(rev, "title", "Renamed").ok(t).page(t)
		if r["path"] != "Tang/Renamed.md" || r["title"] != "Renamed" {
			t.Fatalf("%v", r)
		}
		rev = r["rev"]
		if got := e.r.tool("page_get", e.args("path", "Tang/Renamed.md")).ok(t).out["id"]; got != id {
			t.Fatal(got)
		}

		// A conflict says what there is now, without the body, and writes nothing.
		e.tm.owner.do("PUT", spacePath(e.sid)+"/pages/"+id, map[string]any{"baseRev": rev, "meta": map[string]any{"title": "Renamed"}, "body": "typed by hand\n"}).want(t, 200, "")
		c := up(rev, "body", "mine\n").refused(t, "conflict")
		cur := c.out["details"].(map[string]any)["current"].(map[string]any)
		now := e.restPage(t, id)
		if cur["rev"] != now["rev"] || cur["path"] != "Tang/Renamed.md" || cur["meta"].(map[string]any)["title"] != "Renamed" || strings.Contains(c.r.raw, "typed by hand") {
			t.Fatalf("%s", c.r.raw)
		}
		if got := e.restPage(t, id)["body"]; got != "typed by hand\n" {
			t.Fatal(got)
		}
		up(rev, "edits", []any{map[string]any{"old": "typed", "new": "x"}}).refused(t, "conflict")
		up(now["rev"], "edits", []any{map[string]any{"old": "typed", "new": "made"}}).ok(t)
	})
}

// A whole body sent with a baseRev that is behind only by the service's link
// rewrites gets the same rewrites, as a save in the app does: a link without
// an id does not keep the old path.
func TestMCPUpdateBodyIsRebasedOverLinkRewrites(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		e := newMCP(t, a)
		target := e.create(t, map[string]any{"folder": "Tang", "title": "Doc", "body": "x\n"})
		viaMCP := e.create(t, map[string]any{"title": "One", "body": "see [Doc](Tang/Doc.md)\n"})
		viaApp := e.create(t, map[string]any{"title": "Two", "body": "see [Doc](Tang/Doc.md)\n"})
		e.w.tool("page_move", e.args("id", target["id"], "folder", "Song")).ok(t)
		body := "see [Doc](Tang/Doc.md)\nmore\n"
		e.w.tool("page_update", e.args("id", viaMCP["id"], "baseRev", viaMCP["rev"], "body", body)).ok(t)
		e.tm.owner.do("PUT", spacePath(e.sid)+"/pages/"+viaApp["id"].(string), map[string]any{"baseRev": viaApp["rev"], "meta": map[string]any{"title": "Two"}, "body": body}).want(t, 200, "")
		got, want := e.restPage(t, viaMCP["id"].(string))["body"].(string), e.restPage(t, viaApp["id"].(string))["body"].(string)
		if got != want || !strings.Contains(got, "Song/Doc.md") || strings.Contains(got, "Tang/") {
			t.Fatalf("by MCP %q, by the app %q", got, want)
		}
	})
}

// A write by MCP does not touch the owner's draft: it is left, and shows as stale.
func TestMCPWriteLeavesTheDraftAlone(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		e := newMCP(t, a)
		pg := e.create(t, map[string]any{"title": "Doc", "body": "one\n"})
		id := pg["id"].(string)
		page := spacePath(e.sid) + "/pages/" + id
		e.tm.owner.do("PUT", page+"/draft", map[string]any{"baseRev": pg["rev"], "meta": map[string]any{"title": "Doc"}, "body": "typing, not saved yet\n"}).want(t, 200, "")
		e.w.tool("page_update", e.args("id", id, "baseRev", pg["rev"], "edits", []any{map[string]any{"old": "one", "new": "two"}})).ok(t)
		e.w.tool("page_tag", e.args("id", id, "add", []string{"x"})).ok(t)
		got := e.tm.owner.do("GET", page, nil).want(t, 200, "").body
		d, _ := got["draft"].(map[string]any)
		if got["body"] != "two\n" || d == nil || d["body"] != "typing, not saved yet\n" || d["stale"] != true {
			t.Fatalf("%v", got)
		}
		// A draft that says what the file now says has nothing to add, as after a save.
		e.tm.owner.do("PUT", page+"/draft", map[string]any{"baseRev": got["rev"], "meta": got["meta"], "body": "two\nthree\n", "prev": d["version"]}).want(t, 200, "")
		cur := e.w.tool("page_get", e.args("id", id)).ok(t).out["rev"]
		e.w.tool("page_update", e.args("id", id, "baseRev", cur, "body", "two\nthree\n")).ok(t)
		if d := e.tm.owner.do("GET", page, nil).want(t, 200, "").body["draft"]; d != nil {
			t.Fatalf("%v", d)
		}
	})
}

func TestMCPMoveAndTag(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		e := newMCP(t, a)
		pg := e.create(t, map[string]any{"folder": "Tang", "title": "Doc", "tags": []string{"b", "a", "c"}, "props": map[string]any{"keep": "me"}, "body": "text\n"})
		id := pg["id"].(string)
		linker := e.create(t, map[string]any{"title": "Links", "body": "see [Doc](Tang/Doc.md)\n"})
		mv := func(kv ...any) toolRes { return e.w.tool("page_move", e.args(append([]any{"id", id}, kv...)...)) }

		moved := mv("folder", "Song/New").ok(t).page(t)
		if moved["path"] != "Song/New/Doc.md" || moved["folder"] != "Song/New" || moved["rev"] != pg["rev"] {
			t.Fatalf("%v", moved)
		}
		// The links follow.
		if got := e.restPage(t, linker["id"].(string))["body"]; !strings.Contains(got.(string), "Song/New/Doc.md") {
			t.Fatalf("%v", got)
		}
		if got := mv("folder", "").ok(t).page(t)["path"]; got != "Doc.md" {
			t.Fatal(got)
		}
		// baseRev: the page as read, or a conflict.
		rev := e.restPage(t, id)["rev"]
		mv("folder", "Tang", "baseRev", rev).ok(t)
		e.tm.owner.do("PUT", spacePath(e.sid)+"/pages/"+id, map[string]any{"baseRev": rev, "meta": e.restPage(t, id)["meta"], "body": "changed\n"}).want(t, 200, "")
		c := mv("folder", "Song", "baseRev", rev).refused(t, "conflict")
		if c.out["details"].(map[string]any)["current"].(map[string]any)["path"] != "Tang/Doc.md" {
			t.Fatalf("%v", c.out)
		}
		if got := e.restPage(t, id)["path"]; got != "Tang/Doc.md" {
			t.Fatal(got)
		}
		mv("folder", "Song").ok(t) // without baseRev it moves what is there
		mv("folder", "../x").refused(t, "invalid")
		e.w.tool("page_move", e.args("id", "01JD3K8Q7Z", "folder", "x")).refused(t, "not_found")
		if r := mv(); r.rpcErr == nil {
			t.Fatalf("folder is required: %s", r.r.raw)
		}

		// Tags: kept in order, new ones at the end, everything else as it was.
		hdr := e.restPage(t, id)["header"].(string)
		tag := func(kv ...any) toolRes { return e.w.tool("page_tag", e.args(append([]any{"id", id}, kv...)...)) }
		r := tag("add", []string{"d", "a", " e "}, "remove", []string{"b", "nope"}).ok(t).page(t)
		if fmt.Sprint(r["tags"]) != "[a c d e]" {
			t.Fatalf("%v", r["tags"])
		}
		now := e.restPage(t, id)
		if !strings.Contains(now["header"].(string), "keep: me") || now["body"] != "changed\n" || !strings.HasPrefix(now["header"].(string), strings.SplitN(hdr, "tags:", 2)[0]) {
			t.Fatalf("%q\n%q", hdr, now["header"])
		}
		// Adding what is there, removing what is not: nothing changes, the rev is the same.
		if same := tag("add", []string{"a"}, "remove", []string{"zzz"}).ok(t).page(t); same["rev"] != now["rev"] {
			t.Fatalf("%v", same)
		}
		// baseRev.
		tag("add", []string{"f"}, "baseRev", now["rev"]).ok(t)
		tag("add", []string{"g"}, "baseRev", now["rev"]).refused(t, "conflict")
		if got := e.r.tool("page_get", e.args("id", id)).ok(t).out["meta"].(map[string]any)["tags"]; fmt.Sprint(got) != "[a c d e f]" {
			t.Fatal(got)
		}
		tag("remove", []string{"a", "c", "d", "e", "f"}).ok(t)
		if got := e.restPage(t, id)["header"].(string); strings.Contains(got, "tags") {
			t.Fatalf("%q", got)
		}
		tag().refused(t, "invalid")
		tag("add", []string{"x"}, "remove", []string{"x"}).refused(t, "invalid")
		tag("add", []string{"a\nb"}).refused(t, "invalid")
		tag("add", []string{strings.Repeat("t", 1000)}).refused(t, "invalid")
		many := make([]string, 51)
		for i := range many {
			many[i] = fmt.Sprint("t", i)
		}
		tag("add", many).refused(t, "invalid")
		tag("remove", many).refused(t, "invalid")
		e.w.tool("page_tag", e.args("id", "01JD3K8Q7Z", "add", []string{"x"})).refused(t, "not_found")
	})
}

// A replay of a requestId is checked as a new call is: an owner who has been
// demoted or removed since gets the refusal, not the page.
func TestMCPCreateReplayChecksPermissionAgain(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		e := newMCP(t, a)
		_, es := mkToken(t, e.tm.editor, "write", nil, e.sid)
		ed := a.bearer(es, "")
		args := e.args("title", "Once", "requestId", "r-1")
		first := ed.tool("page_create", args).ok(t)
		if again := ed.tool("page_create", args).ok(t); again.out["replayed"] != true || again.page(t)["id"] != first.page(t)["id"] {
			t.Fatalf("%s", again.r.raw)
		}
		e.tm.owner.do("PATCH", spacePath(e.sid)+"/members/"+e.tm.editorID, map[string]any{"role": "viewer"}).want(t, 200, "")
		ed.tool("page_create", args).refused(t, "no_permission")
		e.tm.owner.do("DELETE", spacePath(e.sid)+"/members/"+e.tm.editorID, nil).want(t, 204, "")
		ed.tool("page_create", args).refused(t, "not_found")
	})
}

// What each tool may do follows the owner now, narrowed by the credential.
func TestMCPPermissions(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		e := newMCP(t, a)
		mine := e.tm.owner.personalSpace()
		pg := e.create(t, map[string]any{"title": "Doc", "body": "text\n"})
		id := pg["id"].(string)
		a.flush(e.sid)
		commit := e.w.tool("page_history", e.args("id", id)).ok(t).items(t)[0]["commit"]
		// One call of every tool, in a space.
		calls := func(sid string) map[string]map[string]any {
			return map[string]map[string]any{
				"folders_list": {"space": sid},
				"tags_list":    {"space": sid},
				"types_list":   {"space": sid},
				"pages_search": {"space": sid, "q": "text"},
				"pages_query":  {"space": sid},
				"page_get":     {"space": sid, "id": id},
				"page_history": {"space": sid, "id": id},
				"page_version": {"space": sid, "id": id, "commit": commit},
				"page_create":  {"space": sid, "title": "New"},
				"page_update":  {"space": sid, "id": id, "baseRev": pg["rev"], "body": "x\n"},
				"page_move":    {"space": sid, "id": id, "folder": "Tang"},
				"page_tag":     {"space": sid, "id": id, "add": []string{"x"}},
			}
		}
		if len(calls("")) != 12 { // spaces_list takes no space
			t.Fatal("a tool is not covered")
		}
		isWrite := map[string]bool{"page_create": true, "page_update": true, "page_move": true, "page_tag": true}

		// A read token and a read grant: every write tool is refused as such, and nothing is written.
		for who, c := range map[string]*client{"token": e.r, "grant": a.bearer(grant(t, a, e.tm.owner, "read", e.sid).access, "")} {
			for name, args := range calls(e.sid) {
				if isWrite[name] {
					c.tool(name, args).refused(t, "token_scope")
				} else if r := c.tool(name, args); r.isErr {
					t.Errorf("%s %s: %s", who, name, r.r.raw)
				}
			}
		}
		if got := e.restPage(t, id); got["rev"] != pg["rev"] || got["path"] != "Doc.md" {
			t.Fatal("a refused tool wrote")
		}
		// A space the credential does not name, however much the owner may do there.
		_, mineWrite := mkToken(t, e.tm.owner, "write", nil, mine)
		other := a.bearer(mineWrite, "")
		for name, args := range calls(e.sid) {
			other.tool(name, args).refused(t, "token_scope")
			e.w.tool(name, calls(mine)[name]).refused(t, "token_scope")
		}
		// The space has to be named, and a space that is not one is refused as one.
		e.w.tool("folders_list", nil).refused(t, "invalid")
		e.w.tool("folders_list", map[string]any{"space": "01JD3K8Q7Z"}).refused(t, "token_scope")
		// The administrator's own way into spaces is not a token's.
		_, as := mkToken(t, admin, "write", nil, admin.personalSpace())
		a.bearer(as, "").tool("folders_list", e.args()).refused(t, "token_scope")

		// A write token of an owner who is a viewer reads, and no write is let through.
		_, vs := mkToken(t, e.tm.viewer, "write", nil, e.sid)
		v := a.bearer(vs, "")
		for name, args := range calls(e.sid) {
			if isWrite[name] {
				v.tool(name, args).refused(t, "no_permission")
			} else if r := v.tool(name, args); r.isErr {
				t.Errorf("viewer %s: %s", name, r.r.raw)
			}
		}
		// Demoted: from the next call. Removed: the space is gone.
		_, es := mkToken(t, e.tm.editor, "write", nil, e.sid)
		ed := a.bearer(es, "")
		ed.tool("page_tag", calls(e.sid)["page_tag"]).ok(t)
		e.tm.owner.do("PATCH", spacePath(e.sid)+"/members/"+e.tm.editorID, map[string]any{"role": "viewer"}).want(t, 200, "")
		ed.tool("page_tag", calls(e.sid)["page_tag"]).refused(t, "no_permission")
		ed.tool("page_get", calls(e.sid)["page_get"]).ok(t)
		e.tm.owner.do("DELETE", spacePath(e.sid)+"/members/"+e.tm.editorID, nil).want(t, 204, "")
		for name, args := range calls(e.sid) {
			ed.tool(name, args).refused(t, "not_found")
		}
		// A space an administrator switched off.
		admin.do("POST", "/api/admin/spaces/"+e.sid+"/disable", nil).want(t, 204, "")
		for name, args := range calls(e.sid) {
			e.w.tool(name, args).refused(t, "space_disabled")
		}
		admin.do("POST", "/api/admin/spaces/"+e.sid+"/enable", nil).want(t, 204, "")
		e.w.tool("page_get", calls(e.sid)["page_get"]).ok(t)
	})
}

func TestMCPFollowsTheSwitchesAndTheAccount(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		e := newMCP(t, a)
		g := a.bearer(grant(t, a, e.tm.owner, "write", e.sid).access, "")
		e.w.tool("spaces_list", nil).ok(t)
		g.tool("spaces_list", nil).ok(t)
		// The switch of MCP: /mcp is not there, for either kind of credential, and not for none.
		admin.do("PUT", "/api/admin/access", map[string]any{"mcp": false}).want(t, 200, "")
		for _, c := range []*client{e.w, g, a.newClient("")} {
			c.tool("spaces_list", nil).http(t, 404, "not_found")
			c.do("GET", "/mcp", nil).want(t, 404, "not_found")
		}
		e.w.do("GET", spacePath(e.sid)+"/tree", nil).want(t, 200, "") // the API is still there
		admin.do("PUT", "/api/admin/access", map[string]any{"mcp": true}).want(t, 200, "")
		e.w.tool("spaces_list", nil).ok(t)
		// The switch of tokens: API tokens are refused, a grant is not.
		admin.do("PUT", "/api/admin/access", map[string]any{"tokens": false}).want(t, 200, "")
		e.w.tool("spaces_list", nil).http(t, 403, "tokens_off")
		g.tool("spaces_list", nil).ok(t)
		admin.do("PUT", "/api/admin/access", map[string]any{"tokens": true}).want(t, 200, "")
		// A token revoked, an account disabled.
		list := e.tm.owner.do("GET", "/api/account/tokens", nil).want(t, 200, "").body["tokens"].([]any)
		admin.do("PATCH", "/api/users/"+e.tm.ownerID, map[string]any{"disabled": true}).want(t, 200, "")
		e.w.tool("spaces_list", nil).http(t, 401, "token_owner_disabled")
		g.tool("spaces_list", nil).http(t, 401, "token_owner_disabled")
		admin.do("PATCH", "/api/users/"+e.tm.ownerID, map[string]any{"disabled": false}).want(t, 200, "")
		e.w.tool("spaces_list", nil).ok(t)
		e.tm.owner.do("POST", "/api/login", map[string]any{"username": "olive", "password": pw}).want(t, 200, "")
		e.tm.owner.do("DELETE", "/api/account/tokens/"+list[0].(map[string]any)["id"].(string), nil).want(t, 204, "")
		revoked := 0
		for _, c := range []*client{e.w, e.r} {
			if r := c.tool("spaces_list", nil); r.r.code == 401 && r.r.body["error"] == "token_revoked" {
				revoked++
			}
		}
		if revoked != 1 {
			t.Fatalf("%d tokens revoked", revoked)
		}
	})
}

// Authentication costs no query once a credential is known.
func TestMCPCostsNoQueryPerCall(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		e := newMCP(t, a)
		e.w.tool("spaces_list", nil).ok(t)
		if _, err := a.st.Q().Exec(context.Background(), `DELETE FROM api_tokens`); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 20; i++ {
			e.w.tool("spaces_list", nil).ok(t)
			e.w.tool("folders_list", e.args()).ok(t)
		}
		a.clock.Advance(40 * time.Second)
		e.w.tool("spaces_list", nil).http(t, 401, "token_invalid") // read again
	})
}
