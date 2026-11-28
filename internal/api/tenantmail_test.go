package api_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/mail/mailtest"
)

// An organisation's own sender (3.6): its SMTP settings, which of the three
// senders (its own, the site's, none) a message uses, and what a request that
// names the tenant cannot tell.

const tenantSMTPPassword = "t3nant-smtp-secret"

// tenantSMTP is a fake server for a tenant: STARTTLS with a login, since a
// tenant's sender is never plain. It is trusted by the service of a.
func tenantSMTP(t *testing.T, a *acc) *mailtest.Server {
	t.Helper()
	srv := mailtest.New(t, mailtest.Options{StartTLS: true, User: "acme-bot", Password: tenantSMTPPassword})
	a.auth.TrustTenantMailForTests(srv.Roots)
	return srv
}

func (f *tenFx) allowOwnSender(t *testing.T) {
	t.Helper()
	f.root.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/mail.ownSender", map[string]any{"siteRule": map[string]any{"value": true}}).want(t, 200, "")
}

func (f *tenFx) mailPath() string { return "/api/admin/tenants/" + f.tenB + "/mail" }

func (f *tenFx) ownSMTP(srv *mailtest.Server, extra map[string]any) map[string]any {
	body := map[string]any{"enabled": true, "host": srv.Host(), "port": srv.Port(), "security": "starttls", "username": "acme-bot",
		"password": tenantSMTPPassword, "from": "hello@acme.example.test", "fromName": "Acme Mail", "replyTo": "help@acme.example.test"}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

func inviteVia(t *testing.T, f *tenFx, email string) (sender, mailState string) {
	t.Helper()
	r := f.admB.do("POST", "/api/admin/members/batch", map[string]any{"rows": []map[string]any{{"user": email, "role": "member"}}}).want(t, 200, "")
	m := r.body["mail"].(map[string]any)
	row := r.body["results"].([]any)[0].(map[string]any)
	return m["sender"].(string), row["mail"].(string)
}

func TestTheTenantsSenderIsOffUntilTheSiteAllowsIt(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		srv := tenantSMTP(t, a)
		f.admB.do("GET", f.mailPath(), nil).want(t, 403, "feature_off")
		f.admB.do("PUT", f.mailPath(), f.ownSMTP(srv, nil)).want(t, 403, "feature_off")
		f.allowOwnSender(t)
		f.admB.do("GET", f.mailPath(), nil).want(t, 200, "")
	})
}

// A tenant's administrators see and change their own; nobody else's. The
// password is never in an answer, a list or the audit, and the site's own mail
// is not touched.
func TestATenantsSMTPSettingsAreItsOwnAndThePasswordIsWriteOnly(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		ctx := context.Background()
		f := newTenFx(t, a)
		srv := tenantSMTP(t, a)
		f.allowOwnSender(t)
		got := f.admB.do("GET", f.mailPath(), nil).want(t, 200, "")
		if got.body["enabled"] != false || got.body["hasPassword"] != false || got.body["security"] != "starttls" || got.body["port"] != float64(587) {
			t.Fatalf("%s", got.raw)
		}
		put := f.admB.do("PUT", f.mailPath(), f.ownSMTP(srv, nil)).want(t, 200, "")
		for _, r := range []result{put, f.admB.do("GET", f.mailPath(), nil).want(t, 200, ""), f.root.do("GET", f.mailPath(), nil).want(t, 200, "")} {
			if strings.Contains(r.raw, tenantSMTPPassword) || r.body["hasPassword"] != true || r.body["host"] != srv.Host() || r.body["from"] != "hello@acme.example.test" ||
				r.body["enabled"] != true || r.body["sender"] != "tenant" {
				t.Fatalf("%s", r.raw)
			}
		}
		// A change without the password keeps it; "" clears it.
		f.admB.do("PUT", f.mailPath(), map[string]any{"fromName": "Acme Notes"}).want(t, 200, "")
		if r := f.admB.do("GET", f.mailPath(), nil).want(t, 200, ""); r.body["hasPassword"] != true || r.body["fromName"] != "Acme Notes" || r.body["host"] != srv.Host() {
			t.Fatalf("%s", r.raw)
		}
		// The secrets' list says it is set, and no endpoint shows the value.
		l := f.admB.do("GET", "/api/admin/tenants/"+f.tenB+"/secrets", nil).want(t, 200, "")
		if strings.Contains(l.raw, tenantSMTPPassword) || !strings.Contains(l.raw, `"mail.password"`) {
			t.Fatalf("%s", l.raw)
		}
		if has, err := a.auth.GetTenantMail(ctx, f.tenB); err != nil || !has.HasPassword {
			t.Fatalf("%v", err)
		}
		// The site's own mail is another thing.
		if site := f.root.do("GET", "/api/admin/mail", nil).want(t, 200, ""); site.body["hasPassword"] != false || site.body["host"] != "" {
			t.Fatalf("%s", site.raw)
		}
		// Scoping: another tenant's administrators, members and nobody at all.
		f.root.do("PUT", "/api/admin/tenants/"+store0+"/settings/mail.ownSender", map[string]any{"siteRule": map[string]any{"value": true}}).want(t, 200, "")
		for _, c := range []struct {
			who  *client
			code int
		}{{f.admA, 404}, {f.memA, 403}, {f.memB, 403}, {a.newClient(""), 401}} {
			c.who.do("GET", f.mailPath(), nil).want(t, c.code, "")
			c.who.do("PUT", f.mailPath(), map[string]any{"host": "x.example.test"}).want(t, c.code, "")
			c.who.do("DELETE", f.mailPath(), nil).want(t, c.code, "")
			c.who.do("POST", f.mailPath()+"/test", map[string]any{"to": "a@example.test"}).want(t, c.code, "")
		}
		f.admB.do("GET", "/api/admin/tenants/"+store0+"/mail", nil).want(t, 404, "not_found")
		f.admB.do("PUT", "/api/admin/tenants/"+store0+"/mail", map[string]any{"host": "x.example.test"}).want(t, 404, "not_found")
		// A site administrator sees any tenant's, and an unknown one is not found.
		f.root.do("GET", "/api/admin/tenants/"+store0+"/mail", nil).want(t, 200, "")
		f.root.do("GET", "/api/admin/tenants/"+fakeID+"/mail", nil).want(t, 404, "not_found")
		// The password cleared as a secret takes effect at once.
		f.admB.do("PUT", f.mailPath(), map[string]any{"password": ""}).want(t, 200, "")
		if r := f.admB.do("GET", f.mailPath(), nil).want(t, 200, ""); r.body["hasPassword"] != false {
			t.Fatalf("%s", r.raw)
		}
		f.admB.do("DELETE", f.mailPath(), nil).want(t, 204, "")
		r := f.admB.do("GET", f.mailPath(), nil).want(t, 200, "")
		if r.body["host"] != "" || r.body["enabled"] != false || r.body["sender"] != "site" && r.body["sender"] != "" {
			t.Fatalf("%s", r.raw)
		}
		a.auth.FlushAudit(ctx)
		rows := f.admB.do("GET", "/api/admin/audit?kind=tenant_mail_changed", nil).want(t, 200, "")
		if n := len(rows.body["items"].([]any)); n != 4 {
			t.Fatalf("%d rows: %s", n, rows.raw)
		}
		if strings.Contains(rows.raw, tenantSMTPPassword) || !strings.Contains(rows.raw, `"password":"changed"`) {
			t.Fatalf("%s", rows.raw)
		}
	})
}

func TestATenantsSenderReachesPublicAddressesOverTLSOnly(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a) // the senders of tests are not opened here
		f.allowOwnSender(t)
		put := func(body map[string]any) result {
			if _, ok := body["from"]; !ok {
				body["from"] = "hello@acme.example.test"
			}
			return f.admB.do("PUT", f.mailPath(), body)
		}
		for _, h := range []string{"127.0.0.1", "localhost", "10.0.0.5", "192.168.1.1", "169.254.169.254", "[::1]", "mail.localhost", "relay.internal"} {
			put(map[string]any{"host": h, "port": 587, "enabled": true}).want(t, 400, "mail_host_not_public")
		}
		put(map[string]any{"host": "smtp.example.com", "port": 22}).want(t, 400, "invalid")
		put(map[string]any{"host": "smtp.example.com", "port": 587, "security": "none"}).want(t, 400, "mail_insecure")
		put(map[string]any{"host": "127.0.0.1", "port": 25, "security": "none"}).want(t, 400, "mail_insecure")
		put(map[string]any{"host": "smtp.example.com", "port": 587, "enabled": true, "from": "x@notes.example.test"}).want(t, 400, "invalid")
		put(map[string]any{"host": "smtp.example.com", "port": 587, "enabled": true, "from": "x@nope"}).want(t, 200, "")
		f.admB.do("PUT", f.mailPath(), map[string]any{"enabled": true, "host": "smtp.example.com", "from": "x@acme.example.test", "replyTo": "not an address"}).want(t, 400, "invalid")
		// Nothing was stored by the refusals.
		if r := f.admB.do("GET", f.mailPath(), nil).want(t, 200, ""); r.body["host"] != "smtp.example.com" || r.body["security"] != "starttls" {
			t.Fatalf("%s", r.raw)
		}
	})
}

// Which sender a message uses, for every combination of the tenant's own, the
// site's and none.
func TestTheSenderOfATenantsMessagesIsItsOwnThenTheSitesThenNone(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		ctx := context.Background()
		f := newTenFx(t, a)
		f.allowOwnSender(t)
		siteSrv, ownSrv := fakeMail(t), tenantSMTP(t, a)
		useSite := func(on bool) {
			t.Helper()
			f.root.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/mail.useSite", map[string]any{"siteRule": map[string]any{"value": on}}).want(t, 200, "")
		}
		count := 0
		next := func() string { count++; return "inv" + string(rune('a'+count)) + "@example.test" }

		// Neither: no mail anywhere. The invitation hands its link over.
		useSite(true)
		if s, m := inviteVia(t, f, next()); s != "" || m != "none" {
			t.Fatalf("sender %q, mail %q", s, m)
		}
		// The site's only (mail.useSite on by default): the site's server, the tenant's name as the display name.
		enableMail(t, f.root, siteSrv)
		to := next()
		if s, m := inviteVia(t, f, to); s != "site" || m != "sent" {
			t.Fatalf("sender %q, mail %q", s, m)
		}
		got := lastTo(t, siteSrv, to)
		if !strings.Contains(got.Raw, `From: "Acme via Notes" <notes@example.test>`) {
			t.Fatalf("%s", got.Raw)
		}
		// useSite locked off, no sender of its own: not sent, recorded once a day, and answered as with mail off.
		useSite(false)
		if s, m := inviteVia(t, f, next()); s != "" || m != "none" {
			t.Fatalf("sender %q, mail %q", s, m)
		}
		verifiedTenantUser(t, a, f, "emb", "emb@example.test")
		// A reset is security mail: the site's sender, whatever mail.useSite says.
		f.admB.do("POST", "/api/users/"+mustUserID(t, a, "emb")+"/password-reset", nil).want(t, 202, "")
		lastTo(t, siteSrv, "emb@example.test")
		a.auth.FlushAudit(ctx)
		if n := len(auditKinds(t, f.root, "mail_not_sent")); n != 1 {
			t.Fatalf("%d mail_not_sent rows", n)
		}
		settle()
		if len(siteSrv.Messages()) != 2 { // the invitation of the previous step, and the reset
			t.Fatalf("an ordinary message went by the site's mailer though the tenant may not use it: %d", len(siteSrv.Messages()))
		}
		// Its own sender, with useSite off: its own server, its own display name and reply address.
		f.admB.do("PUT", f.mailPath(), f.ownSMTP(ownSrv, nil)).want(t, 200, "")
		to = next()
		if s, m := inviteVia(t, f, to); s != "tenant" || m != "sent" {
			t.Fatalf("sender %q, mail %q", s, m)
		}
		got = lastTo(t, ownSrv, to)
		if got.Auth != "acme-bot:"+tenantSMTPPassword || !strings.Contains(got.Raw, `From: "Acme Mail" <hello@acme.example.test>`) || !strings.Contains(got.Raw, "Reply-To: help@acme.example.test") {
			t.Fatalf("%s", got.Raw)
		}
		// Its own with useSite on: still its own, never the site's.
		useSite(true)
		to = next()
		if s, _ := inviteVia(t, f, to); s != "tenant" {
			t.Fatalf("sender %q", s)
		}
		lastTo(t, ownSrv, to)
		// Its own, not enabled: the site's again.
		f.admB.do("PUT", f.mailPath(), map[string]any{"enabled": false}).want(t, 200, "")
		to = next()
		if s, m := inviteVia(t, f, to); s != "site" || m != "sent" {
			t.Fatalf("sender %q, mail %q", s, m)
		}
		lastTo(t, siteSrv, to)
		// The site takes the feature away: the stored sender is not used.
		f.admB.do("PUT", f.mailPath(), map[string]any{"enabled": true}).want(t, 200, "")
		f.root.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/mail.ownSender", map[string]any{"siteRule": nil}).want(t, 200, "")
		if s, _ := inviteVia(t, f, next()); s != "site" {
			t.Fatalf("sender %q", s)
		}
		// The default tenant's mail never goes by another tenant's sender.
		if n := countTo(ownSrv, "emb@example.test"); n != 0 {
			t.Fatalf("%d", n)
		}
	})
}

func verifiedTenantUser(t *testing.T, a *acc, f *tenFx, username, email string) {
	t.Helper()
	if _, err := a.auth.CreateUser(context.Background(), auth.CreateParams{Username: username, Role: "member", Password: pw, Email: email, EmailVerified: true, TenantID: f.tenB}); err != nil {
		t.Fatal(err)
	}
}

// Security mail (a message that proves an address, or lets someone in or resets
// access) always goes by the SITE's sender, never a tenant's own, whatever
// mail.useSite says: registration (a new address, and the note that one has an
// account), verification, an address claim, a reset, an administrator's welcome
// link. Only ordinary mail, an invitation, goes by the tenant's server.
func TestSecurityMailNeverGoesByATenantsOwnSender(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		f.allowOwnSender(t)
		siteSrv, ownSrv := fakeMail(t), tenantSMTP(t, a)
		enableMail(t, f.root, siteSrv)
		f.root.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/registration.mode", map[string]any{"value": "open"}).want(t, 200, "")
		f.root.putSetting("accounts.multipleEmails", true).want(t, 200, "")
		f.admB.do("PUT", f.mailPath(), f.ownSMTP(ownSrv, nil)).want(t, 200, "")
		verifiedTenantUser(t, a, f, "emb", "emb@example.test")
		if _, err := a.auth.CreateUser(context.Background(), auth.CreateParams{Username: "unv", Role: "member", Password: pw, Email: "unv@example.test", TenantID: f.tenB}); err != nil {
			t.Fatal(err)
		}
		n := 0
		anyIP := func() *client { n++; return a.newClient(fmt.Sprintf("198.51.100.%d", n)) }
		for _, useSiteOn := range []bool{true, false} {
			f.root.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/mail.useSite", map[string]any{"siteRule": map[string]any{"value": useSiteOn}}).want(t, 200, "")
			tag := "-off"
			if useSiteOn {
				tag = "-on"
			}
			reg := func(user, email string) {
				t.Helper()
				anyIP().do("POST", "/api/register", map[string]any{"tenant": "acme", "username": user, "name": user, "email": email, "password": pw, "acceptTerms": true}).want(t, 202, "")
			}
			reg("fresh"+tag, "fresh"+tag+"@example.test") // a new address
			reg("other"+tag, "emb@example.test")          // an address that has an account: the "already registered" note
			anyIP().do("POST", "/api/password/forgot", map[string]any{"tenant": "acme", "login": "emb"}).want(t, 202, "")
			// Verification and a claim of another address, by a signed-in person of the tenant.
			p := a.newClient("")
			p.do("POST", "/api/login", map[string]any{"identifier": "unv", "password": pw}).want(t, 200, "")
			p.do("POST", "/api/account/email/verify", map[string]any{}).want(t, 202, "")
			p.do("POST", "/api/account/emails", map[string]any{"email": "claim" + tag + "@example.test"}).want(t, 202, "")
			// An administrator's new account, with its set-password link mailed.
			made := f.admB.do("POST", "/api/admin/users", map[string]any{"username": "made" + tag, "name": "Made", "email": "made" + tag + "@example.test", "role": "member"}).want(t, 201, "")
			if made.body["mail"] != "sent" {
				t.Fatalf("%s", made.raw)
			}
			for _, to := range []string{"fresh" + tag + "@example.test", "emb@example.test", "unv@example.test", "claim" + tag + "@example.test", "made" + tag + "@example.test"} {
				lastTo(t, siteSrv, to)
			}
		}
		settle()
		// What the tenant's server may have is a note with no link or token (the
		// "an address was added" notice), never anything that proves or lets in.
		for _, m := range ownSrv.Messages() {
			if len(m.Tokens()) > 0 || strings.Contains(m.Text, "#") {
				t.Fatalf("a message with a link went by the tenant's own sender: %s to %s", m.Subject, m.To)
			}
		}
		// An invitation into the tenant is ordinary mail: its own server.
		if s, m := inviteVia(t, f, "invited@example.test"); s != "tenant" || m != "sent" {
			t.Fatalf("sender %q, mail %q", s, m)
		}
		lastTo(t, ownSrv, "invited@example.test")
		if countTo(siteSrv, "invited@example.test") != 0 {
			t.Fatal("an invitation went by the site's sender")
		}
		// With neither the tenant's sender nor mail.useSite, security mail still goes by the site's.
		f.admB.do("DELETE", f.mailPath(), nil).want(t, 204, "")
		anyIP().do("POST", "/api/register", map[string]any{"tenant": "acme", "username": "fresh3", "name": "x", "email": "fresh3@example.test", "password": pw, "acceptTerms": true}).want(t, 202, "")
		lastTo(t, siteSrv, "fresh3@example.test")
	})
}

func TestATenantsTestMessageGoesByItsSenderAndIsLimited(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		ctx := context.Background()
		f := newTenFx(t, a)
		f.allowOwnSender(t)
		ownSrv := tenantSMTP(t, a)
		// Nothing to test with yet.
		f.admB.do("POST", f.mailPath()+"/test", map[string]any{"to": "me@example.test"}).want(t, 409, "mail_not_configured")
		f.admB.do("PUT", f.mailPath(), f.ownSMTP(ownSrv, map[string]any{"enabled": false})).want(t, 200, "")
		f.admB.do("POST", f.mailPath()+"/test", map[string]any{"to": "not an address"}).want(t, 400, "invalid")
		r := f.admB.do("POST", f.mailPath()+"/test", map[string]any{"to": "me@example.test"}).want(t, 200, "")
		if r.body["ok"] != true {
			t.Fatalf("%s", r.raw)
		}
		if m := lastTo(t, ownSrv, "me@example.test"); m.Auth != "acme-bot:"+tenantSMTPPassword {
			t.Fatalf("%+v", m)
		}
		if st := f.admB.do("GET", f.mailPath(), nil).want(t, 200, ""); st.body["status"].(map[string]any)["ok"] != true {
			t.Fatalf("%s", st.raw)
		}
		// A wrong password is told, without the password in the answer.
		f.admB.do("PUT", f.mailPath(), map[string]any{"password": "wrong-" + tenantSMTPPassword}).want(t, 200, "")
		r = f.admB.do("POST", f.mailPath()+"/test", map[string]any{"to": "me@example.test"}).want(t, 200, "")
		if r.body["ok"] != false || r.body["message"] == "" || strings.Contains(r.raw, "wrong-"+tenantSMTPPassword) {
			t.Fatalf("%s", r.raw)
		}
		// 10 an hour for the tenant; another tenant's are its own.
		for range 8 {
			f.admB.do("POST", f.mailPath()+"/test", map[string]any{"to": "me@example.test"}).want(t, 200, "")
		}
		r = f.admB.do("POST", f.mailPath()+"/test", map[string]any{"to": "me@example.test"}).want(t, 429, "rate_limited")
		if r.resp.Header.Get("Retry-After") == "" {
			t.Fatal("no Retry-After")
		}
		f.root.do("POST", "/api/admin/tenants/"+store0+"/mail/test", map[string]any{"to": "me@example.test"}).want(t, 409, "mail_not_configured")
		a.auth.FlushAudit(ctx)
		if n := len(auditKinds(t, f.root, "tenant_mail_test")); n != 10 {
			t.Fatalf("%d test rows", n)
		}
	})
}

// A password set or cleared as a secret of the tenant is what the sender uses at once.
func TestATenantsPasswordSetAsASecretIsUsedAtOnce(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		f.allowOwnSender(t)
		ownSrv := tenantSMTP(t, a)
		f.admB.do("PUT", f.mailPath(), f.ownSMTP(ownSrv, map[string]any{"password": "wrong"})).want(t, 200, "")
		f.root.do("PUT", "/api/admin/tenants/"+f.tenB+"/secrets/mail.password", map[string]any{"value": tenantSMTPPassword}).want(t, 200, "")
		sender, state := inviteVia(t, f, "viasecret@example.test")
		if sender != "tenant" || state != "sent" {
			t.Fatalf("%q %q", sender, state)
		}
		if m := lastTo(t, ownSrv, "viasecret@example.test"); m.Auth != "acme-bot:"+tenantSMTPPassword {
			t.Fatalf("%+v", m)
		}
	})
}

// The tenant's own sender is read at startup: a new service over the same
// store sends by it without anyone touching the settings.
func TestATenantsSenderSurvivesARestart(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		ctx := context.Background()
		f := newTenFx(t, a)
		f.allowOwnSender(t)
		ownSrv := tenantSMTP(t, a)
		f.admB.do("PUT", f.mailPath(), f.ownSMTP(ownSrv, nil)).want(t, 200, "")
		a.auth.ForgetTenantMail(f.tenB) // what a restart does to memory
		if a.auth.MailSender(f.tenB) == "tenant" {
			t.Fatal("the sender was still in memory")
		}
		if err := a.auth.ReloadTenantMail(ctx, f.tenB); err != nil {
			t.Fatal(err)
		}
		if got := a.auth.MailSender(f.tenB); got != "tenant" {
			t.Fatalf("%q", got)
		}
	})
}

// On the site's mailer the display name is "<tenant> via <site>"; a name that
// looks like an address is never put there, and a sender may not use the
// domain the site itself sends from.
func TestTheDisplayNameOnTheSitesMailerNamesTheSiteAndNeverAnAddress(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		f.allowOwnSender(t)
		siteSrv := fakeMail(t)
		enableMail(t, f.root, siteSrv)
		_, _ = inviteVia(t, f, "named@example.test")
		if got := lastTo(t, siteSrv, "named@example.test"); !strings.Contains(got.Raw, `From: "Acme via Notes" <notes@example.test>`) {
			t.Fatalf("%s", got.Raw)
		}
		f.root.do("PATCH", "/api/admin/tenants/"+f.tenB, map[string]any{"name": "ceo@victim.example"}).want(t, 200, "")
		_, _ = inviteVia(t, f, "unnamed@example.test")
		got := lastTo(t, siteSrv, "unnamed@example.test")
		if strings.Contains(got.Raw, "victim.example") || !strings.Contains(got.Raw, `From: "Notes" <notes@example.test>`) {
			t.Fatalf("%s", got.Raw)
		}
		// The domain the site sends from is the site's, like its own.
		f.admB.do("PUT", f.mailPath(), map[string]any{"host": "smtp.example.com", "from": "x@example.test"}).want(t, 400, "invalid")
	})
}
