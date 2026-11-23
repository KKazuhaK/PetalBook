package api_test

import (
	"fmt"
	"strings"
	"testing"
)

// mcpEnv is a team with a space, and a token of the owner that writes and one
// that only reads.
type mcpEnv struct {
	a      *acc
	tm     team
	w, r   *client // bearer clients: write and read tokens of the owner
	sid    string
	wToken string
}

func newMCP(t *testing.T, a *acc) *mcpEnv {
	t.Helper()
	tm := newTeam(t, a)
	_, ws := mkToken(t, tm.owner, "write", nil, tm.sid)
	_, rs := mkToken(t, tm.owner, "read", nil, tm.sid)
	return &mcpEnv{a: a, tm: tm, w: a.bearer(ws, ""), r: a.bearer(rs, ""), sid: tm.sid, wToken: ws}
}

// create makes a page through the tool and returns its item.
func (e *mcpEnv) create(t *testing.T, args map[string]any) map[string]any {
	t.Helper()
	args["space"] = e.sid
	return e.w.tool("page_create", args).ok(t).page(t)
}

func (e *mcpEnv) args(kv ...any) map[string]any {
	m := map[string]any{"space": e.sid}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

func TestMCPTransport(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		e := newMCP(t, a)
		anon := a.newClient("")
		// Stateless, and only POST.
		for _, m := range []string{"GET", "DELETE", "PUT"} {
			r := anon.do(m, "/mcp", nil)
			if r.code != 405 || r.resp.Header.Get("Allow") != "POST" {
				t.Errorf("%s: %d %v", m, r.code, r.resp.Header)
			}
		}
		// Without a token: the challenge that points at the protected resource.
		r := anon.rpc("tools/list", nil)
		r.want(t, 401, "unauthenticated")
		if got := r.resp.Header.Get("WWW-Authenticate"); !strings.Contains(got, `resource_metadata="https://notes.example.test/.well-known/oauth-protected-resource/mcp"`) || !strings.Contains(got, "Bearer") {
			t.Fatalf("%q", got)
		}
		// A session cookie is not accepted.
		e.tm.owner.rpc("tools/list", nil).want(t, 401, "unauthenticated")
		// A token that does not work is refused for itself, and never falls back to the cookie.
		a.bearer("", "").rpc("tools/list", nil).want(t, 401, "token_invalid")
		bad := e.tm.owner.rpc("tools/list", nil, "Authorization", "Bearer pbk_"+strings.Repeat("a", 43))
		bad.want(t, 401, "token_invalid")
		if !strings.Contains(bad.resp.Header.Get("WWW-Authenticate"), "invalid_token") {
			t.Fatalf("%v", bad.resp.Header)
		}
		// The rules of the API: JSON bodies, no cross-site requests, a bounded body.
		e.w.do("POST", "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, "Content-Type", "text/plain").want(t, 415, "invalid")
		e.w.rpc("tools/list", nil, "Origin", "https://evil.example").want(t, 403, "forbidden")
		e.w.rpc("tools/list", nil, "Sec-Fetch-Site", "cross-site").want(t, 403, "forbidden")
		big := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"spaces_list","arguments":{"space":"` + strings.Repeat("x", 2<<20) + `"}}}`
		e.w.do("POST", "/mcp", big, "Accept", "application/json, text/event-stream").want(t, 413, "invalid")
		// A JSON answer, no session.
		init := e.w.rpc("initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "t", "version": "1"}}).want(t, 200, "")
		if ct := init.resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") || init.resp.Header.Get("Mcp-Session-Id") != "" {
			t.Fatalf("%v", init.resp.Header)
		}
		if init.body["result"].(map[string]any)["serverInfo"].(map[string]any)["name"] != "petalbook" {
			t.Fatal(init.raw)
		}
		// The answer to a call of a tool is JSON, the structure and the same as text.
		if sl := e.w.tool("spaces_list", nil).ok(t); firstText(sl.r.body["result"].(map[string]any)) == "" {
			t.Fatalf("%s", sl.r.raw)
		}
	})
}

func toolNames(t *testing.T, c *client) map[string]bool {
	t.Helper()
	r := c.rpc("tools/list", nil).want(t, 200, "")
	out := map[string]bool{}
	for _, x := range r.body["result"].(map[string]any)["tools"].([]any) {
		out[x.(map[string]any)["name"].(string)] = true
	}
	return out
}

var (
	readTools  = []string{"spaces_list", "folders_list", "tags_list", "types_list", "pages_search", "pages_query", "page_get", "page_history", "page_version"}
	writeTools = []string{"page_create", "page_update", "page_move", "page_tag"}
)

func TestMCPToolListFollowsWhatTheCredentialCanDo(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		e := newMCP(t, a)
		all := toolNames(t, e.w)
		if len(all) != 13 {
			t.Fatalf("%v", all)
		}
		for _, n := range append(append([]string{}, readTools...), writeTools...) {
			if !all[n] {
				t.Errorf("no tool %s", n)
			}
		}
		rd := toolNames(t, e.r)
		if len(rd) != len(readTools) {
			t.Fatalf("%v", rd)
		}
		for _, n := range writeTools {
			if rd[n] {
				t.Errorf("a read token is offered %s", n)
			}
		}
		// A write token of a viewer can write nowhere: it is offered the same as a read one.
		_, vs := mkToken(t, e.tm.viewer, "write", nil, e.sid)
		if got := toolNames(t, a.bearer(vs, "")); len(got) != len(readTools) {
			t.Fatalf("%v", got)
		}
		// And so is a grant that only reads.
		if got := toolNames(t, a.bearer(grant(t, a, e.tm.owner, "read", e.sid).access, "")); len(got) != len(readTools) {
			t.Fatalf("%v", got)
		}
		if got := toolNames(t, a.bearer(grant(t, a, e.tm.owner, "write", e.sid).access, "")); len(got) != 13 {
			t.Fatalf("%v", got)
		}
	})
}

func TestMCPSpacesList(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		e := newMCP(t, a)
		mine := e.tm.owner.personalSpace()
		_, two := mkToken(t, e.tm.owner, "write", nil, e.sid, mine)
		out := a.bearer(two, "").tool("spaces_list", nil).ok(t).out
		if out["user"].(map[string]any)["id"] != e.tm.ownerID || out["credential"].(map[string]any)["kind"] != "token" || out["credential"].(map[string]any)["access"] != "write" {
			t.Fatalf("%v", out)
		}
		if len(out["spaces"].([]any)) != 2 {
			t.Fatalf("%v", out["spaces"])
		}
		lim := out["limits"].(map[string]any)
		if lim["maxResultBytes"] != float64(1<<20) || lim["maxBodyChunk"] != float64(128<<10) || lim["maxWriteBody"] != float64(1<<20) {
			t.Fatalf("%v", lim)
		}
		// Only the permissions the credential gives, never members or manage; a read token's are read.
		for _, s := range e.r.tool("spaces_list", nil).ok(t).out["spaces"].([]any) {
			if fmt.Sprint(s.(map[string]any)["permissions"]) != "[read]" || s.(map[string]any)["inbox"] != "Inbox" {
				t.Fatalf("%v", s)
			}
		}
		// A grant says it is one.
		g := a.bearer(grant(t, a, e.tm.owner, "write", e.sid).access, "")
		if got := g.tool("spaces_list", nil).ok(t).out["credential"].(map[string]any)["kind"]; got != "oauth" {
			t.Fatal(got)
		}
		// A space the owner has left is not listed.
		e.tm.owner.do("PATCH", spacePath(e.sid)+"/members/"+e.tm.editorID, map[string]any{"role": "viewer"}).want(t, 200, "")
		_, es := mkToken(t, e.tm.editor, "write", nil, e.sid)
		if got := a.bearer(es, "").tool("spaces_list", nil).ok(t).out["spaces"].([]any); fmt.Sprint(got[0].(map[string]any)["permissions"]) != "[read]" {
			t.Fatalf("%v", got)
		}
		e.tm.owner.do("DELETE", spacePath(e.sid)+"/members/"+e.tm.editorID, nil).want(t, 204, "")
		if got := a.bearer(es, "").tool("spaces_list", nil).ok(t).out["spaces"].([]any); len(got) != 0 {
			t.Fatalf("%v", got)
		}
	})
}

func TestMCPFoldersTagsAndTypes(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		e := newMCP(t, a)
		for _, f := range []string{"Tang", "Tang/Late", "Tang/Early", "Song", "Yuan/Deep/Deeper"} {
			e.create(t, map[string]any{"folder": f, "title": "in " + f, "tags": []string{"poem"}})
		}
		e.create(t, map[string]any{"title": "root page", "tags": []string{"poem", "note"}})
		e.create(t, map[string]any{"title": "root two", "tags": []string{"poem", "note", "rare"}})
		paths := func(r toolRes) []string {
			var out []string
			for _, it := range r.items(t) {
				out = append(out, fmt.Sprintf("%s:%v", it["path"], it["pages"]))
			}
			return out
		}
		got := paths(e.r.tool("folders_list", e.args()).ok(t))
		if fmt.Sprint(got) != "[Song:1 Tang:1 Yuan:0]" {
			t.Fatalf("%v", got)
		}
		got = paths(e.r.tool("folders_list", e.args("parent", "Tang")).ok(t))
		if fmt.Sprint(got) != "[Tang/Early:1 Tang/Late:1]" {
			t.Fatalf("%v", got)
		}
		got = paths(e.r.tool("folders_list", e.args("recursive", true)).ok(t))
		if fmt.Sprint(got) != "[Song:1 Tang:1 Tang/Early:1 Tang/Late:1 Yuan:0 Yuan/Deep:0 Yuan/Deep/Deeper:1]" {
			t.Fatalf("%v", got)
		}
		got = paths(e.r.tool("folders_list", e.args("parent", "yuan", "recursive", true)).ok(t)) // case as the app takes it
		if fmt.Sprint(got) != "[Yuan/Deep:0 Yuan/Deep/Deeper:1]" {
			t.Fatalf("%v", got)
		}
		// Paged, in path order, and the pages join up.
		var all []string
		cursor := ""
		for i := 0; i < 10; i++ {
			r := e.r.tool("folders_list", e.args("recursive", true, "limit", 3, "cursor", cursor)).ok(t)
			if n := len(r.items(t)); n > 3 {
				t.Fatalf("%d items", n)
			}
			all = append(all, paths(r)...)
			if cursor, _ = r.out["next"].(string); cursor == "" {
				break
			}
		}
		if len(all) != 7 || all[0] != "Song:1" || all[6] != "Yuan/Deep/Deeper:1" {
			t.Fatalf("%v", all)
		}
		e.r.tool("folders_list", e.args("parent", "Nowhere")).refused(t, "not_found")
		e.r.tool("folders_list", e.args("parent", "../x")).refused(t, "invalid")
		e.r.tool("folders_list", e.args("limit", 1001)).refused(t, "invalid")
		e.r.tool("folders_list", e.args("cursor", "!!")).refused(t, "invalid")
		if r := e.r.tool("folders_list", e.args("limit", "x")); r.rpcErr == nil || r.rpcErr["code"] != float64(-32602) {
			t.Fatalf("%s", r.r.raw)
		}

		// Tags: the most used first, paged.
		r := e.r.tool("tags_list", e.args()).ok(t)
		var tags []string
		for _, it := range r.items(t) {
			tags = append(tags, fmt.Sprintf("%s:%v", it["tag"], it["pages"]))
		}
		if fmt.Sprint(tags) != "[poem:7 note:2 rare:1]" {
			t.Fatalf("%v", tags)
		}
		p1 := e.r.tool("tags_list", e.args("limit", 2)).ok(t)
		p2 := e.r.tool("tags_list", e.args("limit", 2, "cursor", p1.out["next"])).ok(t)
		if len(p1.items(t)) != 2 || p1.out["next"] == "" || len(p2.items(t)) != 1 || p2.items(t)[0]["tag"] != "rare" || p2.out["next"] != "" {
			t.Fatalf("%s %s", p1.r.raw, p2.r.raw)
		}
		e.r.tool("tags_list", e.args("cursor", "AAAA")).refused(t, "invalid")

		// Types, with the folders they are bound to.
		sp := spacePath(e.sid)
		e.tm.owner.do("PUT", sp+"/types/poem", map[string]any{"title": "Poem", "fields": []map[string]any{
			{"key": "status", "title": "Status", "kind": "select", "options": []string{"draft", "done"}}, {"key": "year", "title": "Year", "kind": "number"}}}).want(t, 200, "")
		e.tm.owner.do("PUT", sp+"/folders/type", map[string]any{"folder": "Tang", "type": "poem"}).want(t, 200, "")
		ty := e.r.tool("types_list", e.args()).ok(t).out["types"].([]any)
		if len(ty) != 1 {
			t.Fatalf("%v", ty)
		}
		one := ty[0].(map[string]any)
		f := one["fields"].([]any)
		if one["name"] != "poem" || one["title"] != "Poem" || len(f) != 2 || f[0].(map[string]any)["key"] != "status" || f[0].(map[string]any)["kind"] != "select" ||
			fmt.Sprint(f[0].(map[string]any)["options"]) != "[draft done]" || fmt.Sprint(one["folders"]) != "[Tang]" {
			t.Fatalf("%v", one)
		}
	})
}

func TestMCPSearchAndQuery(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		e := newMCP(t, a)
		sp := spacePath(e.sid)
		e.tm.owner.do("PUT", sp+"/types/poem", map[string]any{"title": "Poem", "fields": []map[string]any{
			{"key": "status", "title": "Status", "kind": "select", "options": []string{"draft", "done"}}, {"key": "year", "title": "Year", "kind": "number"}}}).want(t, 200, "")
		e.tm.owner.do("POST", sp+"/folders", map[string]any{"path": "Tang"}).want(t, 201, "")
		e.tm.owner.do("PUT", sp+"/folders/type", map[string]any{"folder": "Tang", "type": "poem"}).want(t, 200, "")
		long := strings.Repeat("长夜漫漫，", 200)
		mk := func(title, status string, year int, tags ...string) {
			e.create(t, map[string]any{"folder": "Tang", "title": title, "tags": tags, "props": map[string]any{"status": status, "year": year},
				"body": title + " 黄昏 " + long})
		}
		for i := 0; i < 5; i++ {
			mk(fmt.Sprintf("Draft %d", i), "draft", 700+i, "a")
		}
		mk("Done one", "done", 800, "a", "b")
		mk("Done two", "done", 810, "b")
		e.create(t, map[string]any{"title": "Elsewhere", "body": "黄昏 only here\n"})

		q := func(args map[string]any) toolRes { return e.r.tool("pages_query", args).ok(t) }
		titles := func(r toolRes) string {
			var out []string
			for _, it := range r.items(t) {
				out = append(out, it["title"].(string))
			}
			return strings.Join(out, ",")
		}
		// Typed filters and sorts, as the REST listing has them.
		r := q(e.args("folder", "Tang", "filter", []any{map[string]any{"key": "status", "op": "in", "values": []any{"done"}}}, "sort", "prop:year"))
		if titles(r) != "Done one,Done two" {
			t.Fatalf("%s", r.r.raw)
		}
		it := r.items(t)[0]
		if it["space"] != e.sid || it["effectiveType"] != "poem" || it["rev"] == "" || it["props"].(map[string]any)["year"] != float64(800) || it["id"] == "" {
			t.Fatalf("%v", it)
		}
		if _, ok := it["body"]; ok || len([]rune(it["excerpt"].(string))) > 400 || len([]rune(it["excerpt"].(string))) < 100 {
			t.Fatalf("an item has a body, or an excerpt of the wrong size: %d", len([]rune(it["excerpt"].(string))))
		}
		r = q(e.args("folder", "Tang", "filter", []any{map[string]any{"key": "year", "op": "range", "values": []any{705, 805}}}, "sort", "-prop:year"))
		if titles(r) != "Done one" {
			t.Fatalf("%s", r.r.raw)
		}
		if got := titles(q(e.args("folder", "Tang", "tag", []string{"a", "b"}))); got != "Done one" {
			t.Fatal(got)
		}
		if got := titles(q(e.args("folder", "", "sort", "title"))); got != "Elsewhere" {
			t.Fatal(got)
		}
		if got := titles(q(e.args("folder", "", "recursive", true, "sort", "-title", "limit", 2))); got != "Elsewhere,Draft 4" {
			t.Fatal(got)
		}
		if got := titles(q(e.args("createdFrom", "2261-01-01"))); got != "" {
			t.Fatal(got)
		}
		// What the REST listing refuses.
		for name, args := range map[string]map[string]any{
			"filter keys":   e.args("filter", []any{map[string]any{"key": "status", "op": "in", "values": []any{"x"}, "extra": 1}}),
			"filter op":     e.args("filter", []any{map[string]any{"key": "status", "op": "nope", "values": []any{}}}),
			"too many tags": e.args("tag", []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"}),
			"sort":          e.args("sort", "nope"),
			"day":           e.args("updatedFrom", "yesterday"),
			"cursor":        e.args("cursor", "garbage"),
			"limit":         e.args("limit", 201),
			"folder":        e.args("folder", "../x"),
		} {
			if r := e.r.tool("pages_query", args); !r.isErr || r.out["error"] != "invalid" {
				t.Errorf("%s: %s", name, r.r.raw)
			}
		}
		// The filter is an array, not text: that is a malformed argument.
		if r := e.r.tool("pages_query", e.args("filter", "[]")); r.rpcErr == nil || r.rpcErr["code"] != float64(-32602) {
			t.Fatalf("%s", r.r.raw)
		}
		// Paged: every page once, in order, with the keyset cursor of the index.
		seen := map[string]bool{}
		cursor := ""
		pages := 0
		for {
			r := q(e.args("limit", 3, "cursor", cursor, "sort", "title"))
			for _, it := range r.items(t) {
				if seen[it["id"].(string)] {
					t.Fatalf("%v twice", it["title"])
				}
				seen[it["id"].(string)] = true
			}
			pages++
			if cursor, _ = r.out["next"].(string); cursor == "" {
				break
			}
		}
		if len(seen) != 8 || pages != 3 {
			t.Fatalf("%d pages seen in %d answers", len(seen), pages)
		}

		// Search: by relevance, from the index, with the same cap on a page.
		s := e.r.tool("pages_search", e.args("q", "黄昏", "limit", 4)).ok(t)
		if len(s.items(t)) != 4 || s.out["next"] == "" {
			t.Fatalf("%s", s.r.raw)
		}
		s2 := e.r.tool("pages_search", e.args("q", "黄昏", "limit", 50, "cursor", s.out["next"])).ok(t)
		if len(s2.items(t)) != 4 || s2.out["next"] != "" {
			t.Fatalf("%d", len(s2.items(t)))
		}
		if got := titles(e.r.tool("pages_search", e.args("q", "Elsewhere", "in", "title")).ok(t)); got != "Elsewhere" {
			t.Fatal(got)
		}
		if got := titles(e.r.tool("pages_search", e.args("q", "黄昏", "folder", "Tang", "tag", []string{"b"})).ok(t)); got != "Done two,Done one" && got != "Done one,Done two" {
			t.Fatal(got)
		}
		e.r.tool("pages_search", e.args("q", "")).refused(t, "invalid")
		e.r.tool("pages_search", e.args("q", strings.Repeat("x", 501))).refused(t, "invalid")
		e.r.tool("pages_search", e.args("q", "x", "limit", 51)).refused(t, "invalid")
		e.r.tool("pages_search", e.args("q", "x", "in", "body")).refused(t, "invalid")
		if r := e.r.tool("pages_search", e.args()); r.rpcErr == nil {
			t.Fatalf("q is required: %s", r.r.raw)
		}
	})
}

// fitsTheCap checks the sizes docs/api.md gives: the structured result at most
// 1 MiB as JSON; the response, which also carries that JSON as text, escaped
// once more as a JSON string, at most three times that and the envelope.
func fitsTheCap(t *testing.T, r toolRes) {
	t.Helper()
	text := firstText(r.r.body["result"].(map[string]any))
	if len(text) > 1<<20 || len(r.r.raw) > 3*len(text)+4096 {
		t.Fatalf("a result of %d bytes in a response of %d", len(text), len(r.r.raw))
	}
}

// A result over 1 MiB is refused with a limit that fits.
func TestMCPTooLarge(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		e := newMCP(t, a)
		// Each item carries a property of 100 long texts, which a listing holds cut
		// to 300 characters each: about 30 KB. Fifty of them are over a mebibyte.
		var texts []string
		for i := 0; i < 100; i++ {
			texts = append(texts, strings.Repeat("字", 300))
		}
		for i := 0; i < 50; i++ {
			e.create(t, map[string]any{"title": fmt.Sprintf("Big %02d", i), "props": map[string]any{"lines": texts}})
		}
		r := e.r.tool("pages_query", e.args("sort", "title")).refused(t, "too_large")
		d := r.out["details"].(map[string]any)
		suggested := int(d["suggestedLimit"].(float64))
		if d["limitBytes"] != float64(1<<20) || d["sizeBytes"].(float64) <= float64(1<<20) || suggested < 1 || suggested >= 50 {
			t.Fatalf("%v", d)
		}
		// The advice works, with the same cursor.
		ok := e.r.tool("pages_query", e.args("sort", "title", "limit", suggested)).ok(t)
		if len(ok.items(t)) != suggested {
			t.Fatalf("%d items", len(ok.items(t)))
		}
		fitsTheCap(t, ok)
		// Over the REST listing the same page is fine: the cap is the tool's.
		if len(e.r.tool("pages_query", e.args("limit", 1)).ok(t).items(t)) != 1 {
			t.Fatal("one item")
		}
	})
}

func TestMCPPageGetInPieces(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		e := newMCP(t, a)
		// 3-byte characters, so that a cut at 128 KiB falls inside one.
		line := strings.Repeat("长", 100) + "\n"
		body := strings.Repeat(line, 1500) // about 450 KB
		pg := e.create(t, map[string]any{"folder": "Tang", "title": "Long", "body": body})
		id := pg["id"].(string)
		var got strings.Builder
		offset, n := 0, 0
		var rev any
		for {
			r := e.r.tool("page_get", e.args("id", id, "offset", offset)).ok(t)
			b := r.out["body"].(string)
			if len(b) > 128<<10 || len(b) == 0 {
				t.Fatalf("a piece of %d bytes", len(b))
			}
			got.WriteString(b)
			if rev != nil && rev != r.out["rev"] {
				t.Fatal("the rev changed")
			}
			rev = r.out["rev"]
			if r.out["bodyBytes"] != float64(len(body)) || r.out["offset"] != float64(offset) || r.out["path"] != "Tang/Long.md" || r.out["effectiveType"] != "" ||
				r.out["meta"].(map[string]any)["title"] != "Long" {
				t.Fatalf("%v", r.out["path"])
			}
			if _, ok := r.out["header"]; ok {
				t.Fatal("a header")
			}
			n++
			next, ok := r.out["next"].(float64)
			if !ok {
				break
			}
			offset = int(next)
		}
		if got.String() != body || n != 4 {
			t.Fatalf("%d pieces, equal %v", n, got.String() == body)
		}
		// By path, as links name it; the first piece has the offset 0.
		r := e.r.tool("page_get", e.args("path", "Tang/Long.md")).ok(t)
		if r.out["id"] != id || r.out["offset"] != float64(0) {
			t.Fatalf("%v", r.out["id"])
		}
		// Offsets that are not at a character, or outside the body.
		e.r.tool("page_get", e.args("id", id, "offset", 1)).refused(t, "invalid")
		e.r.tool("page_get", e.args("id", id, "offset", -1)).refused(t, "invalid")
		e.r.tool("page_get", e.args("id", id, "offset", len(body)+3)).refused(t, "invalid")
		end := e.r.tool("page_get", e.args("id", id, "offset", len(body))).ok(t)
		if end.out["body"] != "" || end.out["next"] != nil {
			t.Fatalf("%v", end.out)
		}
		// Which page.
		e.r.tool("page_get", e.args()).refused(t, "invalid")
		e.r.tool("page_get", e.args("id", id, "path", "Tang/Long.md")).refused(t, "invalid")
		e.r.tool("page_get", e.args("id", "01JD3K8Q7Z")).refused(t, "not_found")
		e.r.tool("page_get", e.args("id", "nope")).refused(t, "not_found")
		e.r.tool("page_get", e.args("path", "Tang/Nothing.md")).refused(t, "not_found")
		e.r.tool("page_get", e.args("path", "../../etc/passwd")).refused(t, "not_found")
		// The draft of the owner is not part of it.
		e.tm.owner.do("PUT", spacePath(e.sid)+"/pages/"+id+"/draft", map[string]any{"baseRev": rev, "meta": map[string]any{"title": "Long"}, "body": "draft\n"}).want(t, 200, "")
		if got := e.r.tool("page_get", e.args("id", id)).ok(t).out; got["draft"] != nil || got["body"] == "draft\n" {
			t.Fatal("the draft shows")
		}
	})
}

// A header near the 1 MiB of a result leaves less room for the body: the pieces
// are smaller, and every one of them fits. A header over it alone is too_large.
func TestMCPPageGetWithALargeHeader(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		e := newMCP(t, a)
		// Quotes, backslashes and line ends take two bytes each in JSON.
		body := strings.Repeat("\"长\" \\ x\n", 30000) // about 330 KB
		big := strings.Repeat("y", 1<<20-60<<10)
		id := e.create(t, map[string]any{"title": "Wide", "props": map[string]any{"big": big}, "body": body})["id"].(string)
		var got strings.Builder
		offset, n := 0, 0
		for {
			r := e.r.tool("page_get", e.args("id", id, "offset", offset)).ok(t)
			if len(r.out["body"].(string)) == 0 || r.out["meta"].(map[string]any)["big"] != big {
				t.Fatalf("a piece of %d bytes", len(r.out["body"].(string)))
			}
			got.WriteString(r.out["body"].(string))
			n++
			fitsTheCap(t, r)
			next, ok := r.out["next"].(float64)
			if !ok {
				break
			}
			offset = int(next)
		}
		if got.String() != body || n < 3 {
			t.Fatalf("%d pieces, equal %v", n, got.String() == body)
		}
		huge := e.create(t, map[string]any{"title": "Wider", "props": map[string]any{"big": strings.Repeat("y", 1<<20)}, "body": "x\n"})["id"].(string)
		r := e.r.tool("page_get", e.args("id", huge)).refused(t, "too_large")
		if !strings.Contains(r.out["message"].(string), "header") {
			t.Fatalf("%v", r.out)
		}
	})
}

func TestMCPHistoryAndVersions(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		e := newMCP(t, a)
		pg := e.create(t, map[string]any{"title": "Doc", "body": "one\n"})
		id := pg["id"].(string)
		a.flush(e.sid)
		rev := pg["rev"]
		for _, text := range []string{"two\n", "three\n", "four\n"} {
			u := e.w.tool("page_update", e.args("id", id, "baseRev", rev, "body", text)).ok(t).page(t)
			rev = u["rev"]
			a.flush(e.sid)
		}
		h := e.r.tool("page_history", e.args("id", id, "limit", 3)).ok(t)
		items := h.items(t)
		if len(items) != 3 || h.out["next"] == "" || items[0]["rev"] != rev || items[0]["via"] != "mcp" || items[0]["change"] != "edit" || items[0]["author"] == "" {
			t.Fatalf("%s", h.r.raw)
		}
		h2 := e.r.tool("page_history", e.args("id", id, "limit", 3, "cursor", h.out["next"])).ok(t)
		last := h2.items(t)
		if len(last) != 1 || last[0]["change"] != "add" || last[0]["via"] != "mcp" || h2.out["next"] != "" {
			t.Fatalf("%s", h2.r.raw)
		}
		v := e.r.tool("page_version", e.args("id", id, "commit", last[0]["commit"])).ok(t).out
		if v["body"] != "one\n" || v["rev"] != last[0]["rev"] || v["bodyBytes"] != float64(4) || v["meta"].(map[string]any)["title"] != "Doc" || v["next"] != nil {
			t.Fatalf("%v", v)
		}
		e.r.tool("page_version", e.args("id", id, "commit", strings.Repeat("0", 40))).refused(t, "not_found")
		e.r.tool("page_version", e.args("id", id, "commit", "HEAD")).refused(t, "not_found")
		e.r.tool("page_history", e.args("id", id, "limit", 101)).refused(t, "invalid")
		e.r.tool("page_history", e.args("id", id, "cursor", "zzz")).refused(t, "invalid")
		e.r.tool("page_history", e.args("id", "01JD3K8Q7Z")).refused(t, "not_found")
		// A long old version comes in pieces too.
		big := strings.Repeat("字", 100000) // 300 KB
		bp := e.create(t, map[string]any{"title": "Big", "body": big})
		a.flush(e.sid)
		e.w.tool("page_update", e.args("id", bp["id"], "baseRev", bp["rev"], "body", "short\n")).ok(t)
		a.flush(e.sid)
		old := e.r.tool("page_history", e.args("id", bp["id"])).ok(t).items(t)[1]["commit"]
		p1 := e.r.tool("page_version", e.args("id", bp["id"], "commit", old)).ok(t).out
		p2 := e.r.tool("page_version", e.args("id", bp["id"], "commit", old, "offset", p1["next"])).ok(t).out
		p3 := e.r.tool("page_version", e.args("id", bp["id"], "commit", old, "offset", p2["next"])).ok(t).out
		if p1["body"].(string)+p2["body"].(string)+p3["body"].(string) != big || p3["next"] != nil {
			t.Fatal("the pieces do not make the version")
		}
		// An earlier name of the page is followed.
		e.w.tool("page_update", e.args("id", id, "baseRev", rev, "title", "Renamed")).ok(t)
		a.flush(e.sid)
		if got := e.r.tool("page_history", e.args("id", id, "limit", 100)).ok(t).items(t); len(got) != 5 {
			t.Fatalf("%d versions across a rename", len(got))
		}
	})
}
