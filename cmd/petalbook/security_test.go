package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/space"
	"petalbook.app/server/internal/spaces"
	"petalbook.app/server/internal/store"
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

// lockWithTurnstile sets a policy that keeps everyone out, with a third-party
// captcha, as an administrator of a server with a public URL would: through a
// state handle of its own, as another process.
func lockWithTurnstile(t *testing.T, data string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(data, "petalbook.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	mgr, err := spaces.NewAccounts(spaces.Options{Store: st, DataDir: data, Committer: space.DefaultCommitter})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	svc, err := auth.New(ctx, auth.Config{Store: st, Spaces: mgr, Password: passwordParams, PublicURL: "https://notes.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	set := settings.Of(st)
	put := func(key string, v any) {
		t.Helper()
		if _, err := set.SetSite(ctx, key, settings.Change{HasValue: true, Value: v, By: "admin"}); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
	}
	put(settings.CaptchaProvider, "turnstile")
	siteKey, secret := "1x00", "s3cret"
	if err := svc.UpdateCaptchaKeys(ctx, "admin", "", &siteKey, &secret); err != nil {
		t.Fatal(err)
	}
	put(settings.CaptchaContexts, map[string]any{"login": map[string]any{"on": true, "when": "always"}})
	put(settings.SigninMode, "local_only")
	put(settings.SigninLockoutEnabled, true)
	put(settings.SigninPasskeyRoles, []string{"member"})
}

// checkReset reads the state as a fresh process would: every key the reset
// names is reset, the captcha's provider is kept, and each change is audited.
func checkReset(t *testing.T, data string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(data, "petalbook.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m, _, err := settings.New(st).Site(ctx)
	if err != nil {
		t.Fatal(err)
	}
	login, _ := m[settings.CaptchaContexts].Value.(map[string]any)["login"].(map[string]any)
	if m[settings.SigninMode].Value != "dual" || m[settings.SigninSSOOnly].Value != false || m[settings.SigninLockoutEnabled].Value != false ||
		login["on"] != false || len(m[settings.SigninPasskeyRoles].Value.([]string)) != 0 || m[settings.CaptchaProvider].Value != "turnstile" {
		t.Fatalf("not reset: mode %v ssoOnly %v lockout %v login %v roles %v provider %v", m[settings.SigninMode].Value, m[settings.SigninSSOOnly].Value,
			m[settings.SigninLockoutEnabled].Value, login, m[settings.SigninPasskeyRoles].Value, m[settings.CaptchaProvider].Value)
	}
	rows, err := store.ListAudit(ctx, st.Q(), store.AllTenants(), store.AuditFilter{Kind: "setting_changed"}, 100)
	if err != nil {
		t.Fatal(err)
	}
	audited := map[string]bool{}
	for _, r := range rows {
		var d map[string]any
		json.Unmarshal([]byte(r.Detail), &d)
		if d["by"] == "cli" && r.Actor == "" {
			k, _ := d["key"].(string)
			audited[k] = true
		}
	}
	for _, k := range []string{settings.SigninMode, settings.SigninLockoutEnabled, settings.CaptchaContexts, settings.SigninPasskeyRoles} {
		if !audited[k] {
			t.Errorf("the reset of %s is not in the audit log: %v", k, audited)
		}
	}
}

// The reset is the rescue: it works with a third-party captcha on, which the
// command line (no --public-url) could not set, with the server stopped and
// with it running.
func TestSecurityResetWithAThirdPartyCaptcha(t *testing.T) {
	needGit(t)
	data := filepath.Join(tempDir(t), "data")
	if _, err := runIn(t, "first password here\n", "user", "add", "--data", data, "--username", "kazuha", "--admin"); err != nil {
		t.Fatal(err)
	}
	lockWithTurnstile(t, data)
	if out, err := runIn(t, "", "security", "reset", "--data", data); err != nil {
		t.Fatalf("server stopped: %q %v", out, err)
	}
	checkReset(t, data)
	start(t, serveConfig{data: data, commitIdle: time.Hour, commitMax: time.Hour})
	lockWithTurnstile(t, data)
	if out, err := runIn(t, "", "security", "reset", "--data", data); err != nil {
		t.Fatalf("server running: %q %v", out, err)
	}
	checkReset(t, data)
}
