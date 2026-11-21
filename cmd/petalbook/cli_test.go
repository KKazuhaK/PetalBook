package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

func TestVersion(t *testing.T) {
	out, err := run(t, "version")
	if err != nil || out != "petalbook dev\n" {
		t.Fatalf("%q %v", out, err)
	}
	out, err = run(t, "--version")
	if err != nil || out != "petalbook dev\n" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestServeNeedsASpace(t *testing.T) {
	if _, err := run(t, "serve"); err == nil || !strings.Contains(err.Error(), "space") {
		t.Fatalf("%v", err)
	}
}

func TestRefusesToExposeWithoutAuthentication(t *testing.T) {
	needGit(t)
	dir := filepath.Join(t.TempDir(), "s")
	for _, addr := range []string{"0.0.0.0:0", ":0", "[::]:0"} {
		_, err := run(t, "serve", "--space", dir, "--listen", addr)
		if err == nil || !strings.Contains(err.Error(), "refusing to listen") || !strings.Contains(err.Error(), "--insecure-no-auth") {
			t.Errorf("%s: %v", addr, err)
		}
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("the space was set up although the server refused to start")
	}
}

func TestCheckListen(t *testing.T) {
	addr := func(s string) net.Addr { a, _ := net.ResolveTCPAddr("tcp", s); return a }
	for _, c := range []struct {
		addr     string
		insecure bool
		ok       bool
	}{
		{"127.0.0.1:80", false, true}, {"[::1]:80", false, true}, {"127.1.2.3:80", false, true},
		{"0.0.0.0:80", false, false}, {"192.168.1.5:80", false, false}, {"[::]:80", false, false}, {"10.0.0.1:80", false, false},
		{"0.0.0.0:80", true, true}, {"192.168.1.5:80", true, true},
	} {
		if err := checkListen(addr(c.addr), c.insecure); (err == nil) != c.ok {
			t.Errorf("%s insecure=%v: %v", c.addr, c.insecure, err)
		}
	}
	for addr, want := range map[string]bool{"127.0.0.1:1": true, "[::1]:1": true, "[::1%lo0]:1": true, "localhost:1": false, "garbage": false} {
		if isLoopback(addr) != want {
			t.Errorf("isLoopback(%q) != %v", addr, want)
		}
	}
}

func TestServeEndToEndAndGracefulShutdown(t *testing.T) {
	needGit(t)
	dir := filepath.Join(t.TempDir(), "notes") // not a repository, does not exist
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan net.Addr, 1)
	done := make(chan error, 1)
	go func() {
		done <- runServe(ctx, serveConfig{
			space: dir, listen: "127.0.0.1:0", name: "我的笔记", nameSet: true,
			committer: "Test <t@example.com>", commitIdle: time.Hour, commitMax: time.Hour,
			onReady: func(a net.Addr) { ready <- a },
		})
	}()
	var addr net.Addr
	select {
	case addr = <-ready:
	case err := <-done:
		t.Fatalf("server stopped: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("server did not start")
	}
	base := "http://" + addr.String()

	get := func(path string) map[string]any {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		json.NewDecoder(resp.Body).Decode(&m)
		return m
	}
	meta := get("/api/meta")
	sp := meta["spaces"].([]any)[0].(map[string]any)
	if sp["id"] != "default" || sp["name"] != "我的笔记" {
		t.Fatalf("%v", meta)
	}
	resp, err := http.Post(base+"/api/spaces/default/pages", "application/json", strings.NewReader(`{"meta":{"title":"退出前写的"},"body":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	// The Host check is on for a loopback server: a foreign Host is refused.
	req, _ := http.NewRequest("GET", base+"/api/meta", nil)
	req.Host = "evil.example.com"
	if r, err := http.DefaultClient.Do(req); err != nil || r.StatusCode != 403 {
		t.Fatalf("foreign host: %v %v", r, err)
	}

	// Nothing is committed yet (the quiet period is an hour) ...
	if out, _ := exec.Command("git", "-C", dir, "log", "--format=%s").Output(); len(out) != 0 {
		t.Fatalf("committed early: %q", out)
	}
	// ... until the server is stopped.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("server did not stop")
	}
	out, err := exec.Command("git", "-C", dir, "log", "--format=%an <%ae>|%s").Output()
	if err != nil || !strings.Contains(string(out), "Test <t@example.com>|") {
		t.Fatalf("no commit after shutdown: %q %v", out, err)
	}
	files, _ := exec.Command("git", "-C", dir, "-c", "core.quotepath=false", "ls-files").Output()
	if !strings.Contains(string(files), "退出前写的.md") || !strings.Contains(string(files), ".petalbook/space.yml") {
		t.Fatalf("%s", files)
	}
	if _, err := http.Get(base + "/api/meta"); err == nil {
		t.Fatal("still serving")
	}
}

func TestEnvironmentSuppliesFlags(t *testing.T) {
	t.Setenv("PETALBOOK_LISTEN", "0.0.0.0:0")
	_, err := run(t, "serve", "--space", filepath.Join(t.TempDir(), "x"))
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("PETALBOOK_LISTEN not applied: %v", err)
	}
	t.Setenv("PETALBOOK_COMMIT_IDLE", "banana")
	if _, err := run(t, "serve", "--space", filepath.Join(t.TempDir(), "x")); err == nil || !strings.Contains(err.Error(), "PETALBOOK_COMMIT_IDLE") {
		t.Fatalf("%v", err)
	}
}

func TestParseOrigins(t *testing.T) {
	got, err := parseOrigins([]string{"http://localhost:5173", "HTTPS://Dev.Example.com/", ""})
	if err != nil || len(got) != 2 || got[0] != "http://localhost:5173" || got[1] != "https://dev.example.com" {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range []string{"localhost:5173", "http://localhost:5173/path", "ftp://x", "http://", "*", "http://u@h"} {
		if _, err := parseOrigins([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	// The flag is repeatable and the environment can give a list.
	t.Setenv("PETALBOOK_ALLOW_ORIGIN", "banana")
	if _, err := run(t, "serve", "--space", filepath.Join(t.TempDir(), "x"), "--listen", "127.0.0.1:0"); err == nil || !strings.Contains(err.Error(), "--allow-origin") {
		t.Fatalf("env value not validated: %v", err)
	}
}

func TestServerRefusesCrossSiteEvenWithInsecureFlag(t *testing.T) {
	needGit(t)
	dir := filepath.Join(t.TempDir(), "n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan net.Addr, 1)
	done := make(chan error, 1)
	go func() {
		done <- runServe(ctx, serveConfig{
			space: dir, listen: "127.0.0.1:0", insecure: true, allowOrigins: []string{"http://localhost:5173"},
			commitIdle: time.Hour, commitMax: time.Hour, onReady: func(a net.Addr) { ready <- a },
		})
	}()
	addr := <-ready
	post := func(origin string) int {
		req, _ := http.NewRequest("POST", "http://"+addr.String()+"/api/spaces/default/pages", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := post("https://evil.example.com"); c != 403 {
		t.Fatalf("foreign origin: %d", c)
	}
	if c := post("http://localhost:5173"); c != 201 {
		t.Fatalf("allowed origin: %d", c)
	}
	if c := post(""); c != 201 {
		t.Fatalf("no origin: %d", c)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestInsecureModeWarnsWhenBrowsersAreLockedOut(t *testing.T) {
	if w := noOriginWarning(nil); !strings.Contains(w, "--allow-origin") || !strings.Contains(w, "--insecure-no-auth") {
		t.Fatalf("%q", w)
	}
	if w := noOriginWarning([]string{"http://10.0.0.5:8080"}); w != "" {
		t.Fatalf("%q", w)
	}
	if got := browserOrigins(true, nil); !strings.Contains(got, "loopback") {
		t.Fatal(got)
	}
	if got := browserOrigins(false, []string{"http://a:1"}); !strings.Contains(got, "none implicit") || !strings.Contains(got, "http://a:1") {
		t.Fatal(got)
	}
	// The flag help says so.
	out, _ := run(t, "serve", "--help")
	if !strings.Contains(out, "no implicit own origin") {
		t.Fatalf("help does not explain --insecure-no-auth:\n%s", out)
	}
}
