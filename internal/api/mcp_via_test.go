package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/KKazuhaK/PetalBook/internal/api"
	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/tokens"
)

// A change made through MCP is committed as the owner, says so in the trailer,
// and is never committed together with a change made another way.
func TestMCPCommitsTheOwnersChangesWithATrailer(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		e := newMCP(t, a)
		_, ts := mkToken(t, e.tm.owner, "write", nil, e.sid)
		tok := a.bearer(ts, "")
		page := func(id any) string { return spacePath(e.sid) + "/pages/" + id.(string) }

		// Typed, written through MCP, typed, written with a token, written through
		// MCP by a grant: with no commit in between, each is still a commit of its own.
		typed := newPage(t, e.tm.owner, e.sid, "Doc")
		id := typed["id"]
		put := func(c *client, rev any, text string) any {
			return c.do("PUT", page(id), map[string]any{"baseRev": rev, "meta": map[string]any{"title": "Doc"}, "body": text}).want(t, 200, "").body["rev"]
		}
		rev := e.w.tool("page_update", e.args("id", id, "baseRev", typed["rev"], "body", "by mcp\n")).ok(t).page(t)["rev"]
		rev = put(e.tm.owner, rev, "by hand\n")
		rev = put(tok, rev, "by token\n")
		g := a.bearer(grant(t, a, e.tm.owner, "write", e.sid).access, "")
		g.tool("page_tag", e.args("id", id, "add", []string{"t"})).ok(t)
		made := e.create(t, map[string]any{"title": "Made through MCP", "body": "x\n"})
		e.w.tool("page_move", e.args("id", made["id"], "folder", "Tang")).ok(t)
		a.flush(e.sid)

		items := e.r.tool("page_history", e.args("id", id)).ok(t).items(t)
		var vias []string
		for _, it := range items {
			v, _ := it["via"].(string)
			vias = append(vias, v)
			if !strings.Contains(it["author"].(string), "olive") {
				t.Errorf("author %v", it["author"])
			}
		}
		if got := strings.Join(vias, ","); got != "mcp,token,,mcp," {
			t.Fatalf("via of the versions, newest first: %q", got)
		}
		// The REST history says the same.
		rest := e.tm.owner.do("GET", page(id)+"/history", nil).want(t, 200, "").body["items"].([]any)
		if len(rest) != 5 || rest[0].(map[string]any)["via"] != "mcp" || rest[1].(map[string]any)["via"] != "token" || rest[2].(map[string]any)["via"] != nil {
			t.Fatalf("%v", rest)
		}
		sp, release, err := a.mgr.Acquire(context.Background(), e.sid)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		out := gitLog(t, sp.Root())
		// What was made through MCP in a row is one commit (same person, same means): the
		// tag, the page made and moved; the first update is another. The trailer is the last
		// line, and names neither the token nor the client.
		if strings.Count(out, "\n\nChanged-via: MCP\n@@") != 2 || strings.Count(out, "Changed-via: API token") != 1 || strings.Contains(out, "Changed-via: script") {
			t.Fatalf("%s", out)
		}
		if strings.Count(out, "Co-Authored") != 0 || !strings.Contains(out, "olive|") {
			t.Fatalf("%s", out)
		}
		// The author is the owner, not the service.
		if strings.Contains(out, "PetalBook Test|") {
			t.Fatalf("a commit by the service: %s", out)
		}
	})
}

// A client of the SDK talks to the handler over HTTP: it connects, lists the
// tools and uses them, with a token and with the access token of a grant.
func TestMCPWithTheSDKClient(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		e := newMCP(t, a)
		srv := httptest.NewServer(a.h)
		defer srv.Close()
		g := grant(t, a, e.tm.owner, "write", e.sid)
		for name, secret := range map[string]string{"token": e.wToken, "grant": g.access} {
			ctx := context.Background()
			hc := &http.Client{Transport: bearerTransport{secret}}
			cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx,
				&mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp", HTTPClient: hc, DisableStandaloneSSE: true}, nil)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			tools, err := cs.ListTools(ctx, nil)
			if err != nil || len(tools.Tools) != 13 {
				t.Fatalf("%s: %v %v", name, tools, err)
			}
			for _, tl := range tools.Tools {
				if tl.InputSchema == nil || tl.Description == "" {
					t.Errorf("%s has no schema or description", tl.Name)
				}
			}
			created, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "page_create", Arguments: map[string]any{"space": e.sid, "title": "From " + name, "body": "黄昏\n"}})
			if err != nil || created.IsError {
				t.Fatalf("%s: %v %+v", name, err, created)
			}
			item := created.StructuredContent.(map[string]any)["page"].(map[string]any)
			got, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "page_get", Arguments: map[string]any{"space": e.sid, "id": item["id"]}})
			if err != nil || got.IsError || got.StructuredContent.(map[string]any)["body"] != "黄昏\n" {
				t.Fatalf("%s: %v %+v", name, err, got)
			}
			// A refusal is a result with isError, structured as the API's errors are.
			bad, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "page_update", Arguments: map[string]any{"space": e.sid, "id": item["id"], "baseRev": "0000", "body": "x"}})
			if err != nil || !bad.IsError || bad.StructuredContent.(map[string]any)["error"] != "conflict" {
				t.Fatalf("%s: %v %+v", name, err, bad)
			}
			// A malformed call is a protocol error.
			if _, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "page_get", Arguments: map[string]any{"space": 7}}); err == nil {
				t.Fatalf("%s: a number for a space was taken", name)
			}
			cs.Close()
		}
		// Without a token the client is told to authorize.
		_, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(context.Background(),
			&mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp", DisableStandaloneSSE: true}, nil)
		if err == nil {
			t.Fatal("connected without a token")
		}
	})
}

type bearerTransport struct{ secret string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.secret)
	return http.DefaultTransport.RoundTrip(r)
}

func TestMCPInSingleUserMode(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	svc := api.NewTokens(tokens.Config{Store: f.opts.Store}, nil)
	ctx := context.Background()
	_, wsecret, err := svc.Create(ctx, tokens.CreateParams{UserID: store.LocalUserID, Name: "cli", Access: "write", Spaces: []string{"default"}, ExpiresIn: 3600e9})
	if err != nil {
		t.Fatal(err)
	}
	_, rsecret, err := svc.Create(ctx, tokens.CreateParams{UserID: store.LocalUserID, Name: "ro", Access: "read", Spaces: []string{"default"}, ExpiresIn: 3600e9})
	if err != nil {
		t.Fatal(err)
	}
	h := api.New(api.Options{Store: f.opts.Store, Spaces: f.opts.Spaces, Drafts: f.opts.Drafts, Tokens: svc, Version: "test-1"})
	t.Cleanup(h.Close)
	f.h = h
	call := func(secret, tool string, args map[string]any) (int, map[string]any, string) {
		msg := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}}
		code, r, raw := f.do("POST", "/mcp", msg, "Authorization", "Bearer "+secret, "Accept", "application/json, text/event-stream")
		res, _ := r["result"].(map[string]any)
		out, _ := res["structuredContent"].(map[string]any)
		return code, out, raw
	}
	// With no token: unauthenticated, the plain challenge (there is no authorization server).
	f.h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/mcp", nil))
	code, r, raw := f.do("POST", "/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}, "Accept", "application/json, text/event-stream")
	if code != 401 || r["error"] != "unauthenticated" {
		t.Fatal(raw)
	}
	if code, _, raw := f.do("GET", "/mcp", nil); code != 405 {
		t.Fatal(raw)
	}
	// The space may be left out; it is default.
	code, out, raw := call(wsecret, "page_create", map[string]any{"title": "Single", "body": "one\n"})
	if code != 200 || out["page"] == nil || out["page"].(map[string]any)["space"] != "default" {
		t.Fatal(raw)
	}
	id := out["page"].(map[string]any)["id"]
	if _, out, raw = call(wsecret, "page_get", map[string]any{"id": id}); out["body"] != "one\n" {
		t.Fatal(raw)
	}
	if _, out, raw = call(wsecret, "page_get", map[string]any{"space": "default", "id": id}); out["body"] != "one\n" {
		t.Fatal(raw)
	}
	if _, out, raw = call(wsecret, "page_get", map[string]any{"space": "other", "id": id}); out["error"] != "token_scope" {
		t.Fatal(raw)
	}
	if _, out, raw = call(rsecret, "page_tag", map[string]any{"id": id, "add": []string{"x"}}); out["error"] != "token_scope" {
		t.Fatal(raw)
	}
	if _, out, raw = call(rsecret, "spaces_list", nil); out["spaces"].([]any)[0].(map[string]any)["id"] != "default" {
		t.Fatal(raw)
	}
	if code, _, raw := call("pbk_"+strings.Repeat("a", 43), "spaces_list", nil); code != 401 {
		t.Fatal(raw)
	}
	// The switch of the command line.
	off := api.New(api.Options{Store: f.opts.Store, Spaces: f.opts.Spaces, Drafts: f.opts.Drafts, Tokens: svc, Version: "test-1", NoMCP: true})
	t.Cleanup(off.Close)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+wsecret)
	req.Host = "localhost:8080"
	off.ServeHTTP(rec, req)
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "not_found") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
