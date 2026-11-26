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
	for _, w := range []struct {
		key string
		v   any
	}{
		{"signin.mode", "local_only"},
		{"signin.lockout.enabled", true},
		{"signin.lockout.minutes", 60},
		{"signin.lockout.scope", "account"},
		{"captcha.contexts", map[string]any{"login": map[string]any{"on": true, "when": "always"}}},
		{"signin.maxFailures", 7},
	} {
		if code, r := b.do("PUT", "/api/admin/settings/"+w.key, map[string]any{"value": w.v}); code != 200 {
			t.Fatalf("%s: %d %v", w.key, code, r)
		}
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
	code, list := b2.do("GET", "/api/admin/settings", nil)
	if code != 200 {
		t.Fatalf("%d %v", code, list)
	}
	sec := map[string]any{}
	for _, it := range list["settings"].([]any) {
		m := it.(map[string]any)
		sec[m["key"].(string)] = m["site"].(map[string]any)["value"]
	}
	// Reset: dual, no lockout, no captcha; what it does not name (a limit, the lockout's length) stays.
	if sec["signin.lockout.enabled"] != false || sec["signin.mode"] != "dual" || sec["signin.maxFailures"] != float64(7) || sec["signin.lockout.minutes"] != float64(60) ||
		sec["captcha.contexts"].(map[string]any)["login"].(map[string]any)["on"] != false {
		t.Fatalf("%d %v", code, sec)
	}
	_, audit := b2.do("GET", "/api/admin/audit?kind=setting_changed", nil)
	found := false
	for _, it := range audit["items"].([]any) {
		m := it.(map[string]any)
		found = found || m["actor"] == nil && m["detail"].(map[string]any)["by"] == "cli"
	}
	if !found {
		t.Fatalf("the reset is not in the audit log: %v", audit)
	}
}
