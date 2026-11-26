package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/oauth"
	"petalbook.app/server/internal/perm"
	"petalbook.app/server/internal/sentence"
	"petalbook.app/server/internal/space"
	"petalbook.app/server/internal/spaces"
)

// The limits of /mcp (docs/api.md, "MCP").
const (
	mcpPath           = oauth.ResourcePath
	mcpMaxRequest     = 2 << 20
	mcpMaxResult      = 1 << 20
	mcpMaxBodyChunk   = 128 << 10
	mcpMaxWriteBody   = 1 << 20
	mcpMaxEdits       = 50
	mcpMaxTagChanges  = 50
	mcpMaxRequestID   = 100
	mcpIdemTTL        = time.Hour
	mcpIdemMax        = 10000
	mcpMaxTypeOptions = 500
)

const mcpInstructions = `PetalBook keeps Markdown pages, one file each, in spaces (folders of pages with tags and typed header fields).
Start with spaces_list: it says which spaces you may use, what you may do in each and the limits. Every other tool takes the space id as "space".
- Find pages: pages_search for words, pages_query for folder, tag, date and typed-field conditions (filter, sort). Both return page items without bodies, newest first unless sorted; pass "next" back as "cursor" for the following page.
- Read a page with page_get (by id or by path). A long body comes in pieces of at most 128 KiB: repeat with offset = next until next is null.
- Change a page with page_update, passing the rev you read as baseRev. To change a few places in a long text give edits (each "old" must occur exactly once) instead of the whole body. On a conflict read the page again and redo the change.
- page_create makes a page, page_move moves it to a folder, page_tag adds and removes tags. Nothing is ever deleted through this server.
- A result larger than 1 MiB is refused with too_large: repeat the same request with a smaller limit.`

// mcpIdentity is who a request to /mcp is from, kept in the request's context
// for the tools (the SDK hands the context of the HTTP request on).
type mcpIdentity struct {
	p *auth.Principal
	r *http.Request // for the caller's address in an audit row
}

type mcpIdentityKey struct{}

// mcpRequestFrom is the HTTP request a tool call came in.
func mcpRequestFrom(ctx context.Context) *http.Request {
	if id, ok := ctx.Value(mcpIdentityKey{}).(*mcpIdentity); ok {
		return id.r
	}
	return nil
}

func mcpIdentityFrom(ctx context.Context) *auth.Principal {
	if id, ok := ctx.Value(mcpIdentityKey{}).(*mcpIdentity); ok {
		return id.p
	}
	return nil
}

// mcpOn says whether /mcp is served at all: the command line's switch in
// single-user mode (Options.NoMCP). In accounts mode it is each tenant's
// features.mcp, asked once the caller is known (mcpOnFor).
func (s *server) mcpOn(ctx context.Context) (bool, error) {
	if s.single {
		return !s.opt.NoMCP, nil
	}
	return true, nil
}

// mcpOnFor says whether MCP is on for the tenant of a caller.
func (s *server) mcpOnFor(ctx context.Context, tenant string) (bool, error) {
	if s.single {
		return !s.opt.NoMCP, nil
	}
	return s.tokens.MCPOn(ctx, tenant)
}

// mcp is the state of the tools: the SDK's server, with its tools, and what
// they remember between calls (which is little).
type mcpTools struct {
	s      *server
	srv    *mcp.Server
	writes map[string]bool
	idem   *mcpIdem
}

// mountMCP registers /mcp on the mux.
func (s *server) mountMCP(mux *http.ServeMux) {
	t := &mcpTools{s: s, writes: map[string]bool{}, idem: &mcpIdem{m: map[string]*mcpIdemEntry{}, now: s.tokens.Now}}
	t.srv = mcp.NewServer(&mcp.Implementation{Name: "petalbook", Version: s.opt.Version}, &mcp.ServerOptions{Instructions: mcpInstructions})
	t.srv.AddReceivingMiddleware(t.middleware)
	t.addReadTools()
	t.addWriteTools()
	sdk := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return t.srv }, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		// The Host and Origin rules are those of the whole API (wrap).
		DisableLocalhostProtection: true,
	})
	mux.Handle(mcpPath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.serveMCP(w, r, sdk) }))
}

func (s *server) serveMCP(w http.ResponseWriter, r *http.Request, sdk http.Handler) {
	on, err := s.mcpOn(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if !on {
		writeError(w, http.StatusNotFound, "not_found", "No such endpoint.")
		return
	}
	// Stateless: there is no stream to open and no session to end.
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "invalid", "Method "+r.Method+" is not allowed here; use POST.")
		return
	}
	secret, ok := bearerToken(r)
	if !ok {
		challenge := "Bearer"
		if s.oauth != nil {
			challenge = s.oauth.Challenge()
		}
		w.Header().Set("WWW-Authenticate", challenge)
		writeError(w, http.StatusUnauthorized, "unauthenticated", "Send a token with Authorization: Bearer; a session cookie is not accepted here.")
		return
	}
	var p *auth.Principal
	if s.oauth != nil && oauth.LooksLikeAccessToken(secret) {
		p, err = s.oauthPrincipal(r, secret)
	} else {
		p, err = s.tokenPrincipal(r, secret)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	if on, err := s.mcpOnFor(r.Context(), p.User.TenantID); err != nil {
		s.fail(w, err)
		return
	} else if !on {
		writeError(w, http.StatusNotFound, "not_found", "No such endpoint.")
		return
	}
	// The request is read here, so that one that is too large answers 413 in
	// the API's shape and the SDK sees a body it can read.
	raw, err := io.ReadAll(io.LimitReader(r.Body, mcpMaxRequest+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid", "Could not read the request body.")
		return
	}
	if len(raw) > mcpMaxRequest {
		writeError(w, http.StatusRequestEntityTooLarge, "invalid", fmt.Sprintf("The request body is larger than %d bytes.", mcpMaxRequest))
		return
	}
	if wrapped, done := s.apiCalls(w, &call{w: w, r: r, p: p}, mcpPath, p.User.TenantID); done != nil {
		w = wrapped
		defer done()
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	r.ContentLength = int64(len(raw))
	ctx := context.WithValue(r.Context(), mcpIdentityKey{}, &mcpIdentity{p, r})
	w.Header().Set("Cache-Control", "no-store")
	sdk.ServeHTTP(w, r.WithContext(ctx))
}

// --- middleware --------------------------------------------------------------

// middleware leaves out of tools/list what the credential cannot use, and
// turns a call whose arguments the SDK refused into the JSON-RPC error the
// protocol has for it (every refusal of a tool of ours has a structured error).
func (t *mcpTools) middleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		res, err := next(ctx, method, req)
		if err != nil {
			return res, err
		}
		switch r := res.(type) {
		case *mcp.ListToolsResult:
			if p := mcpIdentityFrom(ctx); p != nil && !t.canWriteAnywhere(ctx, p) {
				r.Tools = slices.DeleteFunc(r.Tools, func(tool *mcp.Tool) bool { return t.writes[tool.Name] })
			}
		case *mcp.CallToolResult:
			if r.IsError && r.StructuredContent == nil {
				msg := "invalid arguments"
				if len(r.Content) > 0 {
					if tc, ok := r.Content[0].(*mcp.TextContent); ok {
						msg = tc.Text
					}
				}
				return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: msg}
			}
		}
		return res, nil
	}
}

// canWriteAnywhere says whether the credential can write in at least one of
// its spaces now: its access is write, and the owner may write in one of them.
func (t *mcpTools) canWriteAnywhere(ctx context.Context, p *auth.Principal) bool {
	g := p.Grant
	if g == nil || !g.Write {
		return false
	}
	l, err := t.s.spaces.List(ctx, p.User.ID)
	if err != nil {
		return true
	}
	for _, in := range l {
		if g.Names(in.ID) && !in.Disabled && in.Perms.Has(perm.Write) {
			return true
		}
	}
	return false
}

// --- errors ------------------------------------------------------------------

// mcpError is a refusal of a tool: the error of "MCP" in docs/api.md.
type mcpError struct {
	Code    string         `json:"error"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func (e *mcpError) Error() string { return e.Message }

func mcpErr(code, msg string) *mcpError { return &mcpError{Code: code, Message: sentence.End(msg)} }

func mcpInvalid(format string, args ...any) *mcpError {
	return mcpErr("invalid", fmt.Sprintf(format, args...))
}

// memWriter takes the answer s.fail writes, so that a tool has the codes and
// messages of the API for the same failure.
type memWriter struct {
	h    http.Header
	code int
	buf  bytes.Buffer
}

func (m *memWriter) Header() http.Header         { return m.h }
func (m *memWriter) WriteHeader(code int)        { m.code = code }
func (m *memWriter) Write(b []byte) (int, error) { return m.buf.Write(b) }

// toolError is the refusal an error is: the one the API answers it with.
func (s *server) toolError(err error) *mcpError {
	var me *mcpError
	if errors.As(err, &me) {
		return me
	}
	var ce *space.ConflictError
	if errors.As(err, &ce) {
		cur := ce.Current
		return &mcpError{Code: "conflict", Message: "The page changed since you read it; read it again with page_get and redo the change.",
			Details: map[string]any{"current": map[string]any{"rev": cur.Rev, "path": cur.Path, "meta": cur.Meta}}}
	}
	var ftl *space.FileTooLargeError
	if errors.As(err, &ftl) {
		d := map[string]any{"limit": ftl.Limit, "name": ftl.Name}
		if ftl.Size > 0 {
			d["size"] = ftl.Size
		}
		return &mcpError{Code: "file_too_large", Message: ftl.Error(), Details: d}
	}
	w := &memWriter{h: http.Header{}}
	s.fail(w, err)
	var b errorBody
	if json.Unmarshal(w.buf.Bytes(), &b) != nil || b.Error == "" {
		return mcpErr("internal", "Something went wrong on the server.")
	}
	return mcpErr(b.Error, b.Message)
}

// --- calls -------------------------------------------------------------------

// mcpCall is one call of a tool.
type mcpCall struct {
	t   *mcpTools
	s   *server
	ctx context.Context
	p   *auth.Principal
	req *mcp.CallToolRequest
}

// author is who a change by the credential's owner is credited to.
func (c *mcpCall) author() space.Author {
	a := c.s.spaces.AuthorOf(c.p.User)
	a.Via = space.ViaMCP
	a.MaxFile = c.s.fileLimitFor(c.ctx, c.tenant())
	return a
}

// open opens the space of a tool for the credential: only a space it names,
// the owner's permission there now, narrowed to the credential's access, never
// more than read and write. need says whether the tool changes anything.
func (c *mcpCall) open(id string, write bool) (*spaces.Handle, error) {
	if id == "" {
		if !c.s.single {
			return nil, mcpInvalid("space is required: the id of a space from spaces_list")
		}
		id = SpaceID
	}
	g := c.p.Grant
	if g == nil || !g.Names(id) {
		return nil, mcpErr("token_scope", "this credential does not name this space")
	}
	need := perm.Read
	if write {
		need = perm.Write
		if !g.Write {
			return nil, mcpErr("token_scope", "this credential is read only")
		}
	}
	h, err := c.s.spaces.OpenMember(c.ctx, c.p.User, id)
	if err != nil {
		return nil, err
	}
	h.Info.Perms &= g.Perms()
	if !h.Info.Perms.Has(need) {
		h.Release()
		return nil, mcpErr("no_permission", "your role in this space does not allow that; it needs the "+need.String()+" permission")
	}
	return h, nil
}

// addTool registers a tool. write says that it changes something: it is then
// left out of tools/list for a credential that cannot write, and fn opens its
// space for writing. fn returns what the tool answers, or an error.
//
// A field that holds JSON as it was sent (json.RawMessage, for the header
// fields of a page and the filter of a listing) has no schema of its own: kinds
// says what it is, by field name ("object" or "array").
func addTool[In any](t *mcpTools, name, desc string, write bool, kinds map[string]string, fn func(c *mcpCall, in In) (any, error)) {
	if write {
		t.writes[name] = true
	}
	schema, err := jsonschema.For[In](nil)
	if err != nil {
		panic(fmt.Sprintf("the schema of %s: %v", name, err))
	}
	for field, kind := range kinds {
		old := schema.Properties[field]
		schema.Properties[field] = &jsonschema.Schema{Type: kind, Description: old.Description}
		if kind == "array" {
			schema.Properties[field].Items = &jsonschema.Schema{Type: "object"}
		}
	}
	f := false
	mcp.AddTool(t.srv, &mcp.Tool{Name: name, Description: desc, InputSchema: schema, Annotations: &mcp.ToolAnnotations{
		ReadOnlyHint: !write, DestructiveHint: &f, OpenWorldHint: &f, IdempotentHint: !write,
	}}, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		p := mcpIdentityFrom(ctx)
		if p == nil {
			return t.errorResult(mcpErr("internal", "the request has no credential")), nil, nil
		}
		c := &mcpCall{t: t, s: t.s, ctx: space.ContextWithMaxFile(space.ContextWithVia(ctx, space.ViaMCP), t.s.fileLimitFor(ctx, p.User.TenantID)), p: p, req: req}
		out, err := fn(c, in)
		if err != nil {
			return t.errorResult(t.s.toolError(err)), nil, nil
		}
		return t.okResult(out), nil, nil
	})
}

func (t *mcpTools) errorResult(e *mcpError) *mcp.CallToolResult {
	b, _ := json.Marshal(e)
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}, StructuredContent: json.RawMessage(b)}
}

// okResult is the answer of a tool: its JSON as the structured content, and as
// text for clients that only read that. One more than 1 MiB is too_large, told
// how many items would have fitted when it is a list.
func (t *mcpTools) okResult(out any) *mcp.CallToolResult {
	b, err := mcpEncode(out)
	if err != nil {
		return t.errorResult(mcpErr("internal", "Could not encode the result."))
	}
	if len(b) > mcpMaxResult {
		details := map[string]any{"limitBytes": mcpMaxResult, "sizeBytes": len(b)}
		var list struct {
			Items []json.RawMessage `json:"items"`
		}
		if json.Unmarshal(b, &list) == nil && len(list.Items) > 1 {
			// A limit below what was returned, so that following the advice makes progress.
			n := int(float64(len(list.Items)) * float64(mcpMaxResult) / float64(len(b)) * 0.9)
			details["suggestedLimit"] = max(1, min(n, len(list.Items)-1))
		}
		return t.errorResult(&mcpError{Code: "too_large", Message: "The result is larger than the 1 MiB a result may be; repeat the request with a smaller limit.", Details: details})
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}, StructuredContent: json.RawMessage(b)}
}

// mcpEncode is the JSON of a result, as okResult sends it.
func mcpEncode(out any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// --- remembering a create ----------------------------------------------------

// mcpIdem remembers for an hour what a create with a requestId made, so that
// sending it again does not make another page. It is in memory only: at most
// mcpIdemMax of them, the oldest dropped first, and none survives a restart.
type mcpIdem struct {
	mu    sync.Mutex
	m     map[string]*mcpIdemEntry
	order []string
	now   func() time.Time
}

type mcpIdemEntry struct {
	at   time.Time
	done chan struct{}
	item *mcpItem
}

// do returns what an earlier call with the key made, or makes it with fn (a
// second call with the same key waits for the first, or until ctx is done). A
// failed fn, or one that panicked, is not remembered.
func (i *mcpIdem) do(ctx context.Context, key string, fn func() (*mcpItem, error)) (item *mcpItem, replayed bool, err error) {
	for {
		i.mu.Lock()
		e := i.m[key]
		if e != nil && e.item != nil && i.now().Sub(e.at) > mcpIdemTTL {
			delete(i.m, key)
			e = nil
		}
		if e == nil {
			e = &mcpIdemEntry{at: i.now(), done: make(chan struct{})}
			i.m[key] = e
			i.order = append(i.order, key)
			for len(i.m) > mcpIdemMax && len(i.order) > 0 {
				delete(i.m, i.order[0])
				i.order = i.order[1:]
			}
			if len(i.order) > 2*mcpIdemMax { // keys of calls that failed are still in it
				i.order = slices.DeleteFunc(i.order, func(k string) bool { return i.m[k] == nil })
			}
			i.mu.Unlock()
			return i.make(key, e, fn)
		}
		i.mu.Unlock()
		select {
		case <-e.done:
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
		i.mu.Lock()
		got := e.item
		i.mu.Unlock()
		if got != nil {
			return got, true, nil
		}
		// The call that was making it failed: this one makes it.
	}
}

// make runs fn for the entry e of key. Whatever fn does, a panic included,
// the waiters are let go, and e is dropped unless it made the item.
func (i *mcpIdem) make(key string, e *mcpIdemEntry, fn func() (*mcpItem, error)) (item *mcpItem, replayed bool, err error) {
	defer func() {
		i.mu.Lock()
		if item != nil && err == nil {
			e.item, e.at = item, i.now()
		} else if i.m[key] == e {
			delete(i.m, key)
		}
		i.mu.Unlock()
		close(e.done)
	}()
	item, err = fn()
	return item, false, err
}
