package api_test

import (
	"strings"
	"testing"
	"time"

	"petalbook.app/server/internal/mail/mailtest"
)

// Joining an organisation by email domain at sign-up (3.6 d5). B (acme) has
// verified acme.example, C (beta) beta.example; the site's registration stays
// by invitation, so every account made here is made by a domain's join mode. The DNS
// and the clock are the fixture's; nothing waits but the mail queue.

type joinFx struct {
	*domFx
	srv *mailtest.Server
}

func newJoinFx(t *testing.T, a *acc) *joinFx {
	t.Helper()
	f := &joinFx{domFx: newDomFx(t, a), srv: fakeMail(t)}
	enableMail(t, f.root, f.srv)
	f.verifyDomain(t, f.admB, f.tenB, "acme.example")
	f.verifyDomain(t, f.admC, f.tenC, "beta.example")
	return f
}

// verifyDomain claims the domain for the tenant and verifies it by its TXT record.
func (f *domFx) verifyDomain(t *testing.T, c *client, tenant, domain string) {
	t.Helper()
	key, code := claim(t, c, tenant, domain)
	f.a.dns.SetTXT("_petalbook."+key, "petalbook-verify="+code)
	if r := checkNow(c, tenant, key).want(t, 200, ""); r.body["state"] != "verified" {
		t.Fatal(r.raw)
	}
}

// setJoin sets a domain's join mode as the tenant's administrator.
func setJoin(c *client, tenant, domain, mode string, extra ...map[string]any) result {
	body := map[string]any{"join": mode}
	for _, e := range extra {
		for k, v := range e {
			body[k] = v
		}
	}
	return c.do("PATCH", domainsPath(tenant)+"/"+domain, body)
}

func joinPath(tenant string) string { return "/api/admin/tenants/" + tenant + "/join-requests" }

// signUp registers username with email and follows the link: the answer of
// the confirmation.
func (f *joinFx) signUp(t *testing.T, c *client, username, email string) result {
	t.Helper()
	tok := f.mailed(t, email, func() {
		reg(c, map[string]any{"username": username, "name": username, "email": email}).want(t, 202, "")
	}).Token(t)
	return c.do("POST", "/api/register/confirm", map[string]any{"token": tok})
}

// request signs up for a tenant whose domain asks for approval, and returns
// the token of the waiting page.
func (f *joinFx) request(t *testing.T, c *client, username, email string) string {
	t.Helper()
	tok := f.mailed(t, email, func() {
		reg(c, map[string]any{"username": username, "name": username, "email": email}).want(t, 202, "")
	}).Token(t)
	r := c.do("POST", "/api/register/confirm", map[string]any{"token": tok}).want(t, 202, "")
	req, _ := r.body["request"].(map[string]any)
	if req == nil || req["state"] != "pending" || req["tenant"].(map[string]any)["slug"] != "acme" || req["expires"] == nil {
		t.Fatalf("the confirmation answers the request: %s", r.raw)
	}
	if _, ok := r.body["user"]; ok {
		t.Fatalf("a request signs nobody in: %s", r.raw)
	}
	return tok
}

// mailed runs fn, waits for the one message it sends, and returns the last one to email.
func (f *joinFx) mailed(t *testing.T, email string, fn func()) mailtest.Message {
	t.Helper()
	n := len(f.srv.Messages())
	fn()
	f.srv.Wait(n + 1)
	return lastTo(t, f.srv, email)
}

func joinStatus(c *client, tok string) result {
	return c.do("POST", "/api/register/request", map[string]any{"token": tok})
}

// pendingID is the id of the one pending request of the tenant, as its administrator lists it.
func pendingID(t *testing.T, c *client, tenant string) string {
	t.Helper()
	r := c.do("GET", joinPath(tenant), nil).want(t, 200, "")
	items := r.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("want one pending request: %s", r.raw)
	}
	return items[0].(map[string]any)["id"].(string)
}

// Off by default: an address of a verified domain is no way in while its
// mode is off, nor one of a subdomain while it is on; a mode needs a verified
// domain.
func TestJoiningByDomainIsOffByDefaultAndExactlyTheDomain(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newJoinFx(t, a)
		anon := a.newClient("")
		if d := f.admB.do("GET", domainsPath(f.tenB), nil).want(t, 200, "").body["domains"].([]any)[0].(map[string]any); d["join"] != "off" {
			t.Fatalf("join is off by default: %v", d)
		}
		reg(anon, map[string]any{"username": "alice", "email": "alice@acme.example"}).want(t, 403, "invite_required")
		if g := anon.do("GET", "/api/register", nil).want(t, 200, ""); g.body["domainJoin"] != false {
			t.Fatal(g.raw)
		}
		// Only a verified domain takes a mode, and only these three.
		key, _ := claim(t, f.admB, f.tenB, "pending.example")
		setJoin(f.admB, f.tenB, key, "auto").want(t, 409, "domain_required")
		setJoin(f.admB, f.tenB, "acme.example", "maybe").want(t, 400, "invalid")
		// Another tenant's administrator cannot set it.
		setJoin(f.admC, f.tenB, "acme.example", "auto").want(t, 404, "not_found")
		r := setJoin(f.admB, f.tenB, "acme.example", "auto").want(t, 200, "")
		if r.body["join"] != "auto" {
			t.Fatal(r.raw)
		}
		if g := anon.do("GET", "/api/register", nil).want(t, 200, ""); g.body["domainJoin"] != true {
			t.Fatal(g.raw)
		}
		// A subdomain is not the domain: nothing is taken, nothing is sent.
		reg(anon, map[string]any{"username": "dee", "email": "dee@sub.acme.example"}).want(t, 403, "invite_required")
		reg(anon, map[string]any{"username": "eve", "email": "eve@acmeexample.test"}).want(t, 403, "invite_required")
		// An address on the domain is taken, but is no account until its link is followed.
		reg(anon, map[string]any{"username": "alice", "email": "alice@acme.example"}).want(t, 202, "")
		if !mustNoUser(a, "alice") {
			t.Fatal("an unconfirmed address joined")
		}
		// Off again: the link that was sent no longer joins.
		tok := f.srv.Wait(1)[0].Token(t)
		setJoin(f.admB, f.tenB, "acme.example", "off").want(t, 200, "")
		anon.do("POST", "/api/register/confirm", map[string]any{"token": tok}).want(t, 403, "invite_required")
		if !mustNoUser(a, "alice") {
			t.Fatal("a join mode turned off still joined")
		}
		a.auth.FlushAudit(t.Context())
		if rows := f.admB.audit(t, "domain_changed"); len(rows) != 2 || rows[0]["detail"].(map[string]any)["join"] != "off" {
			t.Fatalf("domain_changed: %v", rows)
		}
	})
}

// auto: the confirmed address makes an account in the organisation, as a
// member, and the answer names it.
func TestAutoJoinMakesTheAccountInTheOrganisation(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newJoinFx(t, a)
		setJoin(f.admB, f.tenB, "acme.example", "auto").want(t, 200, "")
		anon := a.newClient("")
		reg(anon, map[string]any{"username": "alice", "name": "Alice", "email": "Alice@ACME.example"}).want(t, 202, "")
		m := f.srv.Wait(1)[0]
		if !strings.Contains(m.Text, "Acme") {
			t.Fatalf("the message names the organisation: %q", m.Text)
		}
		r := anon.do("POST", "/api/register/confirm", map[string]any{"token": m.Token(t)}).want(t, 201, "")
		u := r.user()
		if u["role"] != "member" || u["siteAdmin"] != false || u["tenant"].(map[string]any)["slug"] != "acme" {
			t.Fatal(r.raw)
		}
		if j, _ := r.body["joined"].(map[string]any); j == nil || j["slug"] != "acme" || j["name"] != "Acme" {
			t.Fatalf("the answer says which organisation was joined: %s", r.raw)
		}
		anon.personalSpace()
		a.auth.FlushAudit(t.Context())
		rows := f.admB.audit(t, "join_auto")
		if len(rows) != 1 || rows[0]["detail"].(map[string]any)["domain"] != "acme.example" {
			t.Fatalf("join_auto: %v", rows)
		}
		if rows := f.admC.audit(t, "join_auto"); len(rows) != 0 {
			t.Fatalf("another tenant sees the join: %v", rows)
		}
	})
}

// request: the confirmed address becomes a request that holds the username;
// the administrators are told; approving makes the account, and the person is
// mailed. Denying mails them and frees the username.
func TestARequestIsApprovedOrDenied(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newJoinFx(t, a)
		setJoin(f.admB, f.tenB, "acme.example", "request").want(t, 200, "")
		anon := a.newClient("")
		tok := f.request(t, anon, "bob", "bob@acme.example")
		if !mustNoUser(a, "bob") {
			t.Fatal("a request made an account")
		}
		if rowCount(t, a, `SELECT COUNT(*) FROM registrations`) != 0 || rowCount(t, a, `SELECT COUNT(*) FROM join_requests WHERE state = 'pending'`) != 1 {
			t.Fatal("the registration did not become a request")
		}
		// The request holds the username.
		reg(anon, map[string]any{"username": "bob", "email": "bob2@acme.example"}).want(t, 409, "exists")
		// The administrators are told, and only B's.
		if n := f.admB.do("GET", "/api/notifications", nil).want(t, 200, "").raw; !strings.Contains(n, "join_requested") || !strings.Contains(n, "bob") {
			t.Fatalf("B's administrator is not told: %s", n)
		}
		if n := f.admC.do("GET", "/api/notifications", nil).want(t, 200, "").raw; strings.Contains(n, "join_requested") {
			t.Fatal("C's administrator is told")
		}
		if s := joinStatus(anon, tok).want(t, 200, ""); s.body["state"] != "pending" || s.body["username"] != "bob" {
			t.Fatal(s.raw)
		}
		joinStatus(anon, "nope").want(t, 404, "not_found")
		id := pendingID(t, f.admB, f.tenB)
		var r result
		m := f.mailed(t, "bob@acme.example", func() {
			r = f.admB.do("POST", joinPath(f.tenB)+"/"+id+"/approve", map[string]any{}).want(t, 200, "")
		})
		if r.body["state"] != "approved" || r.body["user"] == nil || r.body["decidedBy"].(map[string]any)["name"] != "adm-b" {
			t.Fatal(r.raw)
		}
		u := userTenant(t, a, "bob")
		if u.TenantID != f.tenB || u.Role != "member" || !u.EmailVerified {
			t.Fatalf("%+v", u)
		}
		a.newClient("").do("POST", "/api/login", map[string]any{"username": "bob", "password": pw}).want(t, 200, "")
		if !strings.Contains(m.Subject+m.Text, "Acme") || !strings.Contains(m.Text, "/login") {
			t.Fatalf("the approval mail: %+v", m)
		}
		if s := joinStatus(anon, tok).want(t, 200, ""); s.body["state"] != "approved" {
			t.Fatal(s.raw)
		}
		f.admB.do("POST", joinPath(f.tenB)+"/"+id+"/deny", nil).want(t, 409, "join_decided")
		if rowCount(t, a, `SELECT COUNT(*) FROM join_requests WHERE password_hash <> ''`) != 0 {
			t.Fatal("a decided request keeps the password hash")
		}

		// Denied: mailed, and the username is free again.
		tok2 := f.request(t, anon, "carl", "carl@acme.example")
		id2 := pendingID(t, f.admB, f.tenB)
		m = f.mailed(t, "carl@acme.example", func() {
			f.admB.do("POST", joinPath(f.tenB)+"/"+id2+"/deny", nil).want(t, 200, "")
		})
		if !strings.Contains(m.Subject+m.Text, "Acme") {
			t.Fatal("no denial mail")
		}
		if !mustNoUser(a, "carl") {
			t.Fatal("a denied request made an account")
		}
		if s := joinStatus(anon, tok2).want(t, 200, ""); s.body["state"] != "denied" {
			t.Fatal(s.raw)
		}
		a.clock.Advance(61 * time.Minute) // the address's hour of mail
		reg(anon, map[string]any{"username": "carl", "email": "carl3@acme.example"}).want(t, 202, "")
		// The list of decided ones, and the audit rows.
		if l := f.admB.do("GET", joinPath(f.tenB)+"?state=all", nil).want(t, 200, ""); len(l.body["items"].([]any)) != 2 {
			t.Fatal(l.raw)
		}
		a.auth.FlushAudit(t.Context())
		for _, k := range []string{"join_requested", "join_approved", "join_denied"} {
			if rows := f.admB.audit(t, k); len(rows) == 0 {
				t.Errorf("no %s", k)
			}
			if rows := f.admC.audit(t, k); len(rows) != 0 {
				t.Errorf("C sees %s", k)
			}
		}
	})
}

// One pending request per person and organisation; the person withdraws it
// from the waiting page, which frees the username.
func TestOnePendingRequestAndWithdrawing(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newJoinFx(t, a)
		setJoin(f.admB, f.tenB, "acme.example", "request").want(t, 200, "")
		anon := a.newClient("")
		tok := f.request(t, anon, "bob", "bob@acme.example")
		f.signUp(t, anon, "bobby", "bob@acme.example").want(t, 409, "join_pending")
		if rowCount(t, a, `SELECT COUNT(*) FROM join_requests`) != 1 {
			t.Fatal("a second request was stored")
		}
		r := anon.do("POST", "/api/register/request/withdraw", map[string]any{"token": tok}).want(t, 200, "")
		if r.body["state"] != "withdrawn" {
			t.Fatal(r.raw)
		}
		anon.do("POST", "/api/register/request/withdraw", map[string]any{"token": tok}).want(t, 409, "join_decided")
		if l := f.admB.do("GET", joinPath(f.tenB), nil).want(t, 200, ""); len(l.body["items"].([]any)) != 0 {
			t.Fatal(l.raw)
		}
		a.clock.Advance(61 * time.Minute)
		reg(anon, map[string]any{"username": "bob", "email": "bob@acme.example"}).want(t, 202, "")
		a.auth.FlushAudit(t.Context())
		if rows := f.admB.audit(t, "join_withdrawn"); len(rows) != 1 {
			t.Fatalf("join_withdrawn: %v", rows)
		}
	})
}

// A request not handled in 30 days expires: the username is free, and it
// can no longer be approved.
func TestARequestExpiresAfterThirtyDays(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newJoinFx(t, a)
		setJoin(f.admB, f.tenB, "acme.example", "request").want(t, 200, "")
		anon := a.newClient("")
		tok := f.request(t, anon, "bob", "bob@acme.example")
		id := pendingID(t, f.admB, f.tenB)
		// The domain's daily recheck keeps finding its record meanwhile.
		a.clock.Advance(30*24*time.Hour - time.Minute)
		reg(anon, map[string]any{"username": "bob", "email": "other@acme.example"}).want(t, 409, "exists")
		a.clock.Advance(2 * time.Minute)
		reg(anon, map[string]any{"username": "bob", "email": "other@acme.example"}).want(t, 202, "")
		// The administrator signs in again (a session does not last 30 days).
		adm := a.newClient("")
		adm.do("POST", "/api/login", good("adm-b")).want(t, 200, "")
		adm.do("POST", joinPath(f.tenB)+"/"+id+"/approve", map[string]any{}).want(t, 409, "join_decided")
		if s := joinStatus(anon, tok).want(t, 200, ""); s.body["state"] != "expired" {
			t.Fatal(s.raw)
		}
		if !mustNoUser(a, "bob") {
			t.Fatal("an expired request made an account")
		}
	})
}

// Only the organisation's own administrators decide: another tenant's is
// answered as for a request that does not exist.
func TestAnotherTenantsAdministratorCannotDecide(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newJoinFx(t, a)
		setJoin(f.admB, f.tenB, "acme.example", "request").want(t, 200, "")
		f.request(t, a.newClient(""), "bob", "bob@acme.example")
		id := pendingID(t, f.admB, f.tenB)
		for _, path := range []string{joinPath(f.tenB) + "/" + id, joinPath(f.tenC) + "/" + id} {
			f.admC.do("POST", path+"/approve", map[string]any{}).want(t, 404, "not_found")
			f.admC.do("POST", path+"/deny", nil).want(t, 404, "not_found")
		}
		f.admC.do("GET", joinPath(f.tenB), nil).want(t, 404, "not_found")
		if l := f.admC.do("GET", joinPath(f.tenC), nil).want(t, 200, ""); len(l.body["items"].([]any)) != 0 {
			t.Fatal(l.raw)
		}
		// A member of B is no administrator.
		m := (&ssoFx{domFx: f.domFx}).member(t, f.admB, "mem")
		m.do("POST", joinPath(f.tenB)+"/"+id+"/approve", map[string]any{}).want(t, 403, "forbidden")
		if !mustNoUser(a, "bob") || pendingID(t, f.admB, f.tenB) != id {
			t.Fatal("the request changed")
		}
	})
}

// Requests per person are limited: three a day for one address.
func TestRequestsPerPersonAreLimited(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newJoinFx(t, a)
		setJoin(f.admB, f.tenB, "acme.example", "request").want(t, 200, "")
		anon := a.newClient("")
		for i, name := range []string{"bob", "bobb", "bobbb"} {
			if i > 0 {
				a.clock.Advance(61 * time.Minute)
			}
			tok := f.request(t, anon, name, "bob@acme.example")
			anon.do("POST", "/api/register/request/withdraw", map[string]any{"token": tok}).want(t, 200, "")
		}
		a.clock.Advance(61 * time.Minute)
		r := f.signUp(t, anon, "bobbbb", "bob@acme.example").want(t, 429, "rate_limited")
		if r.resp.Header.Get("Retry-After") == "" {
			t.Fatal("no Retry-After")
		}
		if rowCount(t, a, `SELECT COUNT(*) FROM join_requests WHERE state = 'pending'`) != 0 {
			t.Fatal("a request over the limit was stored")
		}
		// Another address has its own; and the next day this one has three again.
		f.request(t, anon, "carl", "carl@acme.example")
		a.clock.Advance(24 * time.Hour)
		f.request(t, anon, "bobbbbb", "bob@acme.example")
	})
}

// Removing the domain, turning its mode off or the domain lapsing cancels its
// pending requests; the first two while any is pending need the confirmation.
func TestTheDomainGoingCancelsItsPendingRequests(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newJoinFx(t, a)
		setJoin(f.admB, f.tenB, "acme.example", "request").want(t, 200, "")
		anon := a.newClient("")
		tok := f.request(t, anon, "bob", "bob@acme.example")
		confirm := func(domain string) string {
			t.Helper()
			r := f.admB.do("POST", "/api/admin/confirm", map[string]any{"action": "domain_join_off", "tenant": f.tenB, "domain": domain}).want(t, 200, "")
			if p := r.body["plan"].([]any); len(p) != 1 || p[0].(map[string]any)["requests"] != float64(1) {
				t.Fatal(r.raw)
			}
			return r.body["confirm"].(string)
		}
		// Off, with one pending: the confirmation first.
		r := setJoin(f.admB, f.tenB, "acme.example", "off").want(t, 409, "confirm_required")
		if p := r.body["plan"].([]any); len(p) != 1 {
			t.Fatal(r.raw)
		}
		f.admC.do("POST", "/api/admin/confirm", map[string]any{"action": "domain_join_off", "tenant": f.tenB, "domain": "acme.example"}).want(t, 404, "not_found")
		// auto keeps it pending; off with the confirmation cancels it.
		setJoin(f.admB, f.tenB, "acme.example", "auto").want(t, 200, "")
		setJoin(f.admB, f.tenB, "acme.example", "request").want(t, 200, "")
		setJoin(f.admB, f.tenB, "acme.example", "off", map[string]any{"confirm": confirm("acme.example")}).want(t, 200, "")
		if s := joinStatus(anon, tok).want(t, 200, ""); s.body["state"] != "cancelled" {
			t.Fatal(s.raw)
		}
		// Removal.
		setJoin(f.admB, f.tenB, "acme.example", "request").want(t, 200, "")
		a.clock.Advance(61 * time.Minute)
		tok = f.request(t, anon, "bob", "bob@acme.example")
		f.admB.do("DELETE", domainsPath(f.tenB)+"/acme.example", nil).want(t, 409, "confirm_required")
		f.admB.do("DELETE", domainsPath(f.tenB)+"/acme.example?confirm="+confirm("acme.example"), nil).want(t, 204, "")
		if s := joinStatus(anon, tok).want(t, 200, ""); s.body["state"] != "cancelled" {
			t.Fatal(s.raw)
		}
		// A lapse: the daily recheck no longer finds the record for seven days.
		f.verifyDomain(t, f.admB, f.tenB, "acme.example")
		setJoin(f.admB, f.tenB, "acme.example", "request").want(t, 200, "")
		a.clock.Advance(61 * time.Minute)
		tok = f.request(t, anon, "bob", "bob@acme.example")
		a.dns.SetTXT("_petalbook.acme.example")
		for range 9 {
			a.clock.Advance(24 * time.Hour)
			a.domains.RecheckDue(t.Context())
		}
		if s := joinStatus(anon, tok).want(t, 200, ""); s.body["state"] != "cancelled" {
			t.Fatal(s.raw)
		}
		a.auth.FlushAudit(t.Context())
		if rows := f.admB.audit(t, "join_cancelled"); len(rows) != 3 {
			t.Fatalf("join_cancelled: %v", rows)
		}
	})
}

// Turning features.domains off for a tenant with pending requests shows them
// in the confirmation's plan.
func TestTurningDomainsOffCountsThePendingRequests(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newJoinFx(t, a)
		setJoin(f.admB, f.tenB, "acme.example", "request").want(t, 200, "")
		f.request(t, a.newClient(""), "bob", "bob@acme.example")
		r := f.root.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/features.domains", map[string]any{"value": false}).want(t, 409, "confirm_required")
		p := r.body["plan"].([]any)
		if len(p) != 1 || p[0].(map[string]any)["requests"] != float64(1) {
			t.Fatal(r.raw)
		}
	})
}

// An organisation that requires its SSO is joined through it: the sign-up
// answers 409 sso_required, with where to go.
func TestAnSSOOnlyOrganisationIsJoinedThroughItsSSO(t *testing.T) {
	t.Parallel()
	idp := ssoIDP(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		f := newJoinFx(t, a)
		sf := &ssoFx{domFx: f.domFx, idp: idp}
		own := sf.orgProvider(t, f.admB, f.tenB, "acme-sso", nil)
		sf.route(t, f.admB, f.tenB, "acme.example", own)
		setJoin(f.admB, f.tenB, "acme.example", "auto").want(t, 200, "")
		anon := a.newClient("")
		reg(anon, map[string]any{"username": "ann", "email": "ann@acme.example"}).want(t, 202, "")
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/signin.ssoOnly", map[string]any{"value": true}).want(t, 200, "")
		r := reg(anon, map[string]any{"username": "ann2", "email": "ann2@acme.example"}).want(t, 409, "sso_required")
		if sso, _ := r.body["sso"].(map[string]any); sso == nil || sso["slug"] != "acme-sso" {
			t.Fatal(r.raw)
		}
		tok := lastTo(t, f.srv, "ann@acme.example").Token(t)
		anon.do("POST", "/api/register/confirm", map[string]any{"token": tok}).want(t, 409, "sso_required")
		// A request made before is not approved into a password account either.
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/signin.ssoOnly", map[string]any{"value": false}).want(t, 200, "")
		setJoin(f.admB, f.tenB, "acme.example", "request").want(t, 200, "")
		f.request(t, anon, "bob", "bob@acme.example")
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/signin.ssoOnly", map[string]any{"value": true}).want(t, 200, "")
		f.admB.do("POST", joinPath(f.tenB)+"/"+pendingID(t, f.admB, f.tenB)+"/approve", map[string]any{}).want(t, 409, "sso_required")
		if !mustNoUser(a, "bob") || !mustNoUser(a, "ann") {
			t.Fatal("a password account was made")
		}
	})
}
