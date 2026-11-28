package api_test

import (
	"context"
	"strings"
	"testing"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/mail/mailtest"
)

func TestMailIsOffUntilAnAdministratorEnablesIt(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		r := root.do("GET", "/api/admin/mail", nil).want(t, 200, "")
		if r.body["enabled"] != false || r.body["security"] != "starttls" || r.body["hasPassword"] != false {
			t.Fatal(r.raw)
		}
		anon := a.newClient("")
		// Everything that needs mail answers as switched off.
		p := anon.do("GET", "/api/auth/providers", nil).want(t, 200, "")
		if p.body["register"] != false || p.body["forgot"] != false {
			t.Fatal(p.raw)
		}
		anon.do("GET", "/api/register", nil).want(t, 200, "")
		if anon.do("GET", "/api/register", nil).body["open"] != false {
			t.Fatal("registration open without mail")
		}
		anon.do("POST", "/api/password/forgot", map[string]any{"login": "root"}).want(t, 403, "password_reset_off")
		root.do("POST", "/api/account/email/verify", map[string]any{}).want(t, 409, "mail_not_configured")
		root.do("PUT", "/api/admin/settings/registration.mode", map[string]any{"value": "open"}).want(t, 200, "")
		root.do("POST", "/api/admin/mail/test", map[string]any{"to": "x@example.test"}).want(t, 409, "mail_not_configured")
		// Members and tenant administrators have no say in it.
		mem := a.addUser("mem", pw, "member")
		mem.do("GET", "/api/admin/mail", nil).want(t, 403, "forbidden")
		mem.do("PUT", "/api/admin/mail", map[string]any{}).want(t, 403, "forbidden")
		mem.do("POST", "/api/admin/mail/test", map[string]any{"to": "x@example.test"}).want(t, 403, "forbidden")
	})
}

func TestMailSettingsAreCheckedBeforeAnythingIsStored(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		put := func(body map[string]any) result { return root.do("PUT", "/api/admin/mail", body) }
		put(map[string]any{"security": "none", "host": "mail.example.test"}).want(t, 400, "mail_insecure")
		put(map[string]any{"enabled": true, "host": "127.0.0.1"}).want(t, 400, "mail_from_required")
		put(map[string]any{"enabled": true}).want(t, 400, "invalid")
		put(map[string]any{"port": 0}).want(t, 400, "invalid")
		put(map[string]any{"security": "ssl"}).want(t, 400, "invalid")
		put(map[string]any{"from": "Name <a@example.test>"}).want(t, 400, "invalid")
		put(map[string]any{"fromName": strings.Repeat("x", 61)}).want(t, 400, "invalid")
		put(map[string]any{"host": "a b"}).want(t, 400, "invalid")
		// A refused change stores nothing, not even its valid fields.
		put(map[string]any{"host": "mail.example.test", "security": "ssl"}).want(t, 400, "invalid")
		if r := root.do("GET", "/api/admin/mail", nil).want(t, 200, ""); r.body["host"] != "" {
			t.Fatal(r.raw)
		}
		put(map[string]any{"host": "mail.example.test", "from": "notes@example.test"}).want(t, 200, "")
		put(map[string]any{"security": "none"}).want(t, 400, "mail_insecure")
		put(map[string]any{"security": "none", "host": "localhost", "enabled": true}).want(t, 200, "")
	})
}

func TestMailNeedsAPublicURLToBeEnabled(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{mod: func(c *auth.Config) { c.PublicURL = "" }}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		root.do("PUT", "/api/admin/mail", map[string]any{"enabled": true, "host": "127.0.0.1", "security": "none", "from": "a@example.test"}).want(t, 400, "mail_needs_public_url")
	})
}

func TestTheMailPasswordIsSealedAndNeverShown(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		const secret = "smtp-secret-9183"
		r := root.do("PUT", "/api/admin/mail", map[string]any{"host": "127.0.0.1", "security": "none", "username": "bot", "password": secret, "from": "a@example.test"}).want(t, 200, "")
		if r.body["hasPassword"] != true || strings.Contains(r.raw, secret) {
			t.Fatal(r.raw)
		}
		if strings.Contains(root.do("GET", "/api/admin/mail", nil).raw, secret) {
			t.Fatal("the password is shown")
		}
		var stored string
		if err := a.st.Q().QueryRow(context.Background(), `SELECT value FROM site_records WHERE key = 'mail'`).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(stored, secret) || strings.Contains(stored, "assword") {
			t.Fatalf("the record holds the password: %s", stored)
		}
		// It is the secret mail.password, sealed.
		var sealed string
		if err := a.st.Q().QueryRow(context.Background(), `SELECT value FROM secrets WHERE scope = 'site' AND tenant_id = '' AND name = 'mail.password'`).Scan(&sealed); err != nil {
			t.Fatal(err)
		}
		if sealed == "" || strings.Contains(sealed, secret) {
			t.Fatalf("the password is stored in the clear: %s", sealed)
		}
		// Absent keeps it, "" clears it.
		root.do("PUT", "/api/admin/mail", map[string]any{"fromName": "X"}).want(t, 200, "")
		if root.do("GET", "/api/admin/mail", nil).body["hasPassword"] != true {
			t.Fatal("kept password lost")
		}
		root.do("PUT", "/api/admin/mail", map[string]any{"password": ""}).want(t, 200, "")
		if root.do("GET", "/api/admin/mail", nil).body["hasPassword"] != false {
			t.Fatal("password not cleared")
		}
		// The audit log says "changed", never the value.
		rows := auditKinds(t, root, "mail_changed")
		if len(rows) < 2 {
			t.Fatal(rows)
		}
		raw := root.do("GET", "/api/admin/audit?kind=mail_changed", nil).raw
		if strings.Contains(raw, secret) || !strings.Contains(raw, `"password":"changed"`) {
			t.Fatal(raw)
		}
	})
}

func TestTheTestMessageGoesToTheAddressTheAdministratorTyped(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		srv := mailtest.New(t, mailtest.Options{User: "bot", Password: "pw-of-the-bot"})
		root := a.setup("root", pw)
		root.do("POST", "/api/admin/mail/test", map[string]any{"to": "admin@example.test"}).want(t, 409, "mail_not_configured")
		root.do("PUT", "/api/admin/mail", map[string]any{"host": srv.Host(), "port": srv.Port(), "security": "none", "username": "bot", "password": "wrong", "from": "notes@example.test"}).want(t, 200, "")
		// Works while mail is off. A wrong login is answered with the server's words, never the password.
		r := root.do("POST", "/api/admin/mail/test", map[string]any{"to": "admin@example.test"}).want(t, 200, "")
		if r.body["ok"] != false || !strings.Contains(r.body["message"].(string), "535") || strings.Contains(r.raw, "wrong") && strings.Contains(r.body["message"].(string), "wrong\"") {
			t.Fatal(r.raw)
		}
		before := len(srv.Messages())
		root.do("PUT", "/api/admin/mail", map[string]any{"password": "pw-of-the-bot"}).want(t, 200, "")
		r = root.do("POST", "/api/admin/mail/test", map[string]any{"to": "admin@example.test", "lang": "zh-Hans"}).want(t, 200, "")
		if r.body["ok"] != true {
			t.Fatal(r.raw)
		}
		m := srv.Wait(before + 1)[before]
		if m.To != "admin@example.test" || m.Auth != "bot:pw-of-the-bot" || !strings.Contains(m.Subject, "测试") {
			t.Fatalf("%+v", m)
		}
		if st := root.do("GET", "/api/admin/mail", nil).body["status"].(map[string]any); st["ok"] != true || st["checked"] == nil {
			t.Fatal(st)
		}
		// Bad addresses are refused before anything is sent.
		root.do("POST", "/api/admin/mail/test", map[string]any{"to": "not an address"}).want(t, 400, "invalid")
		root.do("POST", "/api/admin/mail/test", map[string]any{"to": "a@example.test\r\nBcc: b@example.test"}).want(t, 400, "invalid")
		if n := len(rows(root, "mail_test")); n != 2 {
			t.Fatalf("audit mail_test rows: %d", n)
		}
		if strings.Contains(root.do("GET", "/api/admin/audit?kind=mail_test", nil).raw, "pw-of-the-bot") {
			t.Fatal("password in the audit log")
		}
		// At most 10 an hour (two are used).
		var last result
		for i := 0; i < 9; i++ {
			last = root.do("POST", "/api/admin/mail/test", map[string]any{"to": "admin@example.test"})
		}
		last.want(t, 429, "rate_limited")
		a.clock.Advance(61 * 60 * 1e9)
		root.do("POST", "/api/admin/mail/test", map[string]any{"to": "admin@example.test"}).want(t, 200, "")
	})
}

func rows(c *client, kind string) []any { return auditKinds(c.a.t, c, kind) }
