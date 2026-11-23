package api_test

import (
	"encoding/json"
	"testing"
)

// toolRes is the answer to a call of a tool on /mcp.
type toolRes struct {
	r result
	// isErr: the tool refused (a result with isError); out is its content,
	// the structured error or the structured answer.
	isErr bool
	out   map[string]any
	// rpcErr is the JSON-RPC error of a call the protocol refused.
	rpcErr map[string]any
}

// rpc sends one JSON-RPC message to /mcp.
func (c *client) rpc(method string, params any, extra ...string) result {
	msg := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		msg["params"] = params
	}
	extra = append([]string{"Accept", "application/json, text/event-stream"}, extra...)
	return c.do("POST", "/mcp", msg, extra...)
}

// tool calls a tool.
func (c *client) tool(name string, args map[string]any, extra ...string) toolRes {
	c.a.t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	return c.toolRaw(map[string]any{"name": name, "arguments": args}, extra...)
}

func (c *client) toolRaw(params map[string]any, extra ...string) toolRes {
	c.a.t.Helper()
	r := c.rpc("tools/call", params, extra...)
	tr := toolRes{r: r}
	if r.code != 200 {
		return tr
	}
	if e, ok := r.body["error"].(map[string]any); ok {
		tr.rpcErr = e
		return tr
	}
	res, _ := r.body["result"].(map[string]any)
	tr.isErr, _ = res["isError"].(bool)
	tr.out, _ = res["structuredContent"].(map[string]any)
	if text := firstText(res); tr.out == nil && text != "" {
		_ = json.Unmarshal([]byte(text), &tr.out)
	}
	return tr
}

func firstText(res map[string]any) string {
	l, _ := res["content"].([]any)
	if len(l) == 0 {
		return ""
	}
	m, _ := l[0].(map[string]any)
	s, _ := m["text"].(string)
	return s
}

// ok wants the tool to have answered.
func (tr toolRes) ok(t *testing.T) toolRes {
	t.Helper()
	if tr.r.code != 200 || tr.isErr || tr.rpcErr != nil || tr.out == nil {
		t.Fatalf("want an answer, got %d isError=%v %s", tr.r.code, tr.isErr, tr.r.raw)
	}
	return tr
}

// refused wants the tool to have refused with the code.
func (tr toolRes) refused(t *testing.T, code string) toolRes {
	t.Helper()
	if tr.r.code != 200 || !tr.isErr || tr.out["error"] != code {
		t.Fatalf("want %s, got %d isError=%v %s", code, tr.r.code, tr.isErr, tr.r.raw)
	}
	return tr
}

// http wants the request itself refused with the status (and code).
func (tr toolRes) http(t *testing.T, status int, code string) toolRes {
	t.Helper()
	tr.r.want(t, status, code)
	return tr
}

func (tr toolRes) items(t *testing.T) []map[string]any {
	t.Helper()
	l, _ := tr.out["items"].([]any)
	out := make([]map[string]any, len(l))
	for i, x := range l {
		out[i], _ = x.(map[string]any)
	}
	return out
}

func (tr toolRes) page(t *testing.T) map[string]any {
	t.Helper()
	p, _ := tr.out["page"].(map[string]any)
	if p == nil {
		t.Fatalf("no page: %s", tr.r.raw)
	}
	return p
}
