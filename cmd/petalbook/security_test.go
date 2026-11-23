package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSecurityResetBringsPasswordSignInBack(t *testing.T) {
	needGit(t)
	data := filepath.Join(tempDir(t), "data")
	const pw1 = "first password here"
	if _, err := runIn(t, pw1+"\n", "user", "add", "--data", data, "--username", "kazuha", "--admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := runIn(t, pw1+"\n", "user", "add", "--data", data, "--username", "tang"); err != nil {
		t.Fatal(err)
	}
	// Lock things up as an administrator would, in a running server.
	srv := start(t, serveConfig{data: data, commitIdle: time.Hour, commitMax: time.Hour})
	b := &browser{t: t, base: srv.base}
	if code, _ := b.do("POST", "/api/login", map[string]any{"username": "kazuha", "password": pw1}); code != 200 {
		t.Fatal("cannot sign in")
	}
	put := map[string]any{
		"login":   map[string]any{"mode": "local_only"},
		"lockout": map[string]any{"enabled": true, "minutes": 60, "scope": "account"},
		"captcha": map[string]any{"contexts": map[string]any{"login": map[string]any{"on": true, "when": "always"}}},
		"limits":  map[string]any{"maxFailures": 7},
	}
	if code, r := b.do("PUT", "/api/admin/security", put); code != 200 || r["lockout"].(map[string]any)["enabled"] != true {
		t.Fatalf("%d %v", code, r)
	}
	if err := srv.stop(); err != nil {
		t.Fatal(err)
	}
	// Nobody can sign in without a captcha now; the command line puts it right.
	out, err := runIn(t, "", "security", "reset", "--data", data)
	if err != nil || !strings.Contains(out, "reset") {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := runIn(t, "", "security", "reset"); err == nil || !strings.Contains(err.Error(), "data") {
		t.Fatalf("%v", err)
	}
	srv = start(t, serveConfig{data: data, commitIdle: time.Hour, commitMax: time.Hour})
	b = &browser{t: t, base: srv.base}
	if code, _ := b.do("POST", "/api/login", map[string]any{"username": "tang", "password": pw1}); code != 200 {
		t.Fatal("the reset did not let people in")
	}
	b2 := &browser{t: t, base: srv.base}
	b2.do("POST", "/api/login", map[string]any{"username": "kazuha", "password": pw1})
	code, sec := b2.do("GET", "/api/admin/security", nil)
	if code != 200 || sec["lockout"].(map[string]any)["enabled"] != false || sec["login"].(map[string]any)["mode"] != "dual" || sec["limits"].(map[string]any)["maxFailures"] != float64(7) ||
		sec["captcha"].(map[string]any)["contexts"].(map[string]any)["login"].(map[string]any)["on"] != false {
		t.Fatalf("%d %v", code, sec)
	}
	_, audit := b2.do("GET", "/api/admin/audit?kind=security_changed", nil)
	found := false
	for _, it := range audit["items"].([]any) {
		m := it.(map[string]any)
		found = found || m["actor"] == nil && m["detail"].(map[string]any)["by"] == "cli"
	}
	if !found {
		t.Fatalf("%v", audit)
	}
}
