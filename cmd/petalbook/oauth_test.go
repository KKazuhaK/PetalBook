package main

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/api"
)

func call(t *testing.T, base, host, method, path, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
	req.Host = host
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m
}

// With the address people use, accounts mode has the authorization server, and
// its paths go to the API and not to the web app.
func TestServeHasTheAuthorizationServerWithAPublicURL(t *testing.T) {
	needGit(t)
	data := filepath.Join(tempDir(t), "data")
	srv := start(t, serveConfig{data: data, publicURL: "https://notes.example.test", committer: "Svc <svc@example.test>", commitIdle: time.Hour, commitMax: time.Hour})
	const host = "notes.example.test"
	code, m := call(t, srv.base, host, "GET", "/.well-known/oauth-authorization-server", "")
	if code != 200 || m["issuer"] != "https://notes.example.test" || m["token_endpoint"] != "https://notes.example.test/oauth/token" {
		t.Fatal(code, m)
	}
	if code, m := call(t, srv.base, host, "GET", "/.well-known/oauth-protected-resource/mcp", ""); code != 200 || m["resource"] != "https://notes.example.test/mcp" {
		t.Fatal(code, m)
	}
	if code, m := call(t, srv.base, host, "POST", "/oauth/register", `{"client_name":"x","redirect_uris":["http://localhost:1/cb"]}`); code != 201 || m["client_id"] == nil {
		t.Fatal(code, m)
	}
	// An administrator can read that it is there.
	if code, m := call(t, srv.base, host, "GET", "/api/admin/access", ""); code != 401 {
		t.Fatal(code, m)
	}
}

func TestServeHasNoAuthorizationServerWithoutOne(t *testing.T) {
	needGit(t)
	// Accounts mode without a public URL, and single-user mode.
	for name, c := range map[string]serveConfig{
		"accounts": {data: filepath.Join(tempDir(t), "data")},
		"single":   {space: filepath.Join(tempDir(t), "space")},
	} {
		c.committer, c.commitIdle, c.commitMax = "Svc <svc@example.test>", time.Hour, time.Hour
		srv := start(t, c)
		if code, m := call(t, srv.base, "", "GET", "/.well-known/oauth-authorization-server", ""); code != 404 {
			t.Errorf("%s: %d %v", name, code, m)
		}
		if code, m := call(t, srv.base, "", "POST", "/oauth/register", `{"redirect_uris":["http://localhost:1/cb"]}`); code != 404 {
			t.Errorf("%s: %d %v", name, code, m)
		}
	}
}

func TestWhichPathsAreTheAPIs(t *testing.T) {
	for p, want := range map[string]bool{
		"/api/spaces": true, "/mcp": true, "/mcp/x": false, "/oauth/token": true, "/oauth/register": true, "/oauth/authorize": true,
		"/.well-known/oauth-authorization-server": true, "/.well-known/oauth-protected-resource": true, "/.well-known/oauth-protected-resource/mcp": true,
		"/oauth/consent": false, "/oauth/": false, "/": false, "/login": false, "/.well-known/security.txt": false, "/oauth/token/x": false,
	} {
		if got := api.HandlesPath(p); got != want {
			t.Errorf("%s: %v", p, got)
		}
	}
}

// /mcp is the API's, in both modes; --mcp=false switches it off in single-user
// mode, and is refused in accounts mode, where an administrator decides.
func TestServeMCP(t *testing.T) {
	needGit(t)
	for name, c := range map[string]serveConfig{
		"accounts": {data: filepath.Join(tempDir(t), "data")},
		"single":   {space: filepath.Join(tempDir(t), "space")},
		"off":      {space: filepath.Join(tempDir(t), "space"), noMCP: true},
	} {
		c.committer, c.commitIdle, c.commitMax = "Svc <svc@example.test>", time.Hour, time.Hour
		srv := start(t, c)
		want := 401 // there, and wants a token
		if name == "off" {
			want = 404
		}
		code, m := call(t, srv.base, "", "POST", "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
		if code != want || (want == 401 && m["error"] != "unauthenticated") || (want == 404 && m["error"] != "not_found") {
			t.Errorf("%s: %d %v", name, code, m)
		}
	}
	if _, err := (serveConfig{data: filepath.Join(tempDir(t), "data"), mcpSet: true}).check(); err == nil || !strings.Contains(err.Error(), "--mcp") {
		t.Fatalf("%v", err)
	}
	if _, err := (serveConfig{space: filepath.Join(tempDir(t), "space"), mcpSet: true, noMCP: true}).check(); err != nil {
		t.Fatal(err)
	}
}
