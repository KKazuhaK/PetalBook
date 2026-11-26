package api_test

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	crewjamsaml "github.com/crewjam/saml"

	"petalbook.app/server/internal/store"
)

const samlSlug = "campus"

func samlAttr(name, value string) crewjamsaml.Attribute {
	return crewjamsaml.Attribute{Name: name, NameFormat: "urn:oasis:names:tc:SAML:2.0:attrname-format:basic",
		Values: []crewjamsaml.AttributeValue{{Type: "xs:string", Value: value}}}
}

func (s *fakeSAML) setSession(f func(*crewjamsaml.Session)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(&s.session)
}

func (s *fakeSAML) reset() {
	s.setSession(func(x *crewjamsaml.Session) {
		x.NameID, x.NameIDFormat, x.UserEmail = "subject-001", string(crewjamsaml.PersistentNameIDFormat), ""
		x.CustomAttributes = []crewjamsaml.Attribute{samlAttr("mail", "saml.person@example.test"), samlAttr("displayName", "Saml Person"), samlAttr("uid", "Saml.Person")}
	})
	s.acs = ""
}

func samlFixture(t *testing.T, a *acc, idp *fakeSAML, extra map[string]any) *client {
	t.Helper()
	// The assertions are signed with the real time, which the service reads from its clock.
	a.clock.Advance(time.Since(a.clock.Now()))
	admin := a.setup("kazuha", pw)
	p := admin.do("POST", "/api/admin/auth/providers", idp.body(samlSlug, extra)).want(t, 201, "")
	s := p.body["saml"].(map[string]any)
	if s["acsUrl"] != testPublic+"/api/auth/saml/campus/acs" || s["entityId"] != testPublic+"/api/auth/saml/campus/metadata" || s["metadataPath"] != "/api/auth/saml/campus/metadata" ||
		s["idpEntityId"] == "" || !strings.HasPrefix(s["certificate"].(string), "-----BEGIN CERTIFICATE-----") || s["certificateExpires"] == "" ||
		strings.Contains(p.raw, "PRIVATE KEY") || p.body["status"].(map[string]any)["ok"] != true {
		t.Fatal(p.raw)
	}
	idp.learn(t, admin, samlSlug)
	idp.reset()
	return admin
}

func TestSAMLSignIn(t *testing.T) {
	t.Parallel()
	idp := newFakeSAML(t, false)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		admin := samlFixture(t, a, idp, nil)
		// The attempt cookie crosses from the identity provider's site: SameSite=None, Secure, only for the ACS.
		c := a.newClient("")
		start := c.do("POST", "/api/auth/saml/campus/start", map[string]any{"return": "/s/x"}).want(t, 200, "")
		ck := start.resp.Cookies()[0]
		if ck.Name != "__Secure-petalbook_saml_campus" || ck.SameSite != http.SameSiteNoneMode || !ck.Secure || !ck.HttpOnly || ck.Path != "/api/auth/saml/campus/acs" || ck.MaxAge != 600 || ck.Value == "" {
			t.Fatalf("%+v", ck)
		}
		if !strings.HasPrefix(start.body["url"].(string), idp.srv.URL+"/sso?SAMLRequest=") || !strings.Contains(start.body["url"].(string), "RelayState="+ck.Value) {
			t.Fatal(start.body["url"])
		}

		// Nobody has this identity and the provider does not create accounts.
		at := idp.begin(t, c, samlSlug, map[string]any{})
		r := c.postACS(samlSlug, at.response, at.relay, at.cookie)
		if r.code != 303 || r.resp.Header.Get("Location") != "/login?sso_error=no_account" {
			t.Fatalf("%d %s", r.code, r.resp.Header.Get("Location"))
		}
		clear := false
		for _, x := range r.resp.Cookies() {
			clear = clear || x.Name == ck.Name && x.MaxAge < 0
		}
		if !clear {
			t.Fatal("the attempt cookie was not cleared")
		}
		// Making accounts from the mapped attributes.
		admin.do("PATCH", "/api/admin/auth/providers/campus", map[string]any{"autoCreate": true}).want(t, 200, "")
		c = a.newClient("")
		at = idp.begin(t, c, samlSlug, map[string]any{"return": "/s/x"})
		r = c.postACS(samlSlug, at.response, at.relay, at.cookie)
		if r.code != 303 || r.resp.Header.Get("Location") != "/s/x" || signedInAs(t, c) != "saml.person" {
			t.Fatalf("%d %s %s", r.code, r.resp.Header.Get("Location"), r.raw)
		}
		u := c.do("GET", "/api/session", nil).user()
		if u["name"] != "Saml Person" || u["email"] != "saml.person@example.test" || u["emailVerified"] != false || u["role"] != "member" {
			t.Fatal(u)
		}
		acct := c.do("GET", "/api/account", nil).want(t, 200, "")
		id := acct.body["identities"].([]any)[0].(map[string]any)
		if id["kind"] != "saml" || id["providerSlug"] != "campus" || id["label"] != "saml.person@example.test" {
			t.Fatal(acct.raw)
		}
		// The identity is the identity provider's entity ID and the NameID.
		if _, err := store.FindIdentity(context.Background(), a.st.Q(), idp.idp.Metadata().EntityID, "subject-001"); err != nil {
			t.Fatal(err)
		}
		// The same person signs in again and gets the same account; an attempt is used once.
		d := a.newClient("")
		at = idp.begin(t, d, samlSlug, map[string]any{})
		d.postACS(samlSlug, at.response, at.relay, at.cookie)
		if signedInAs(t, d) != "saml.person" {
			t.Fatal("not signed in again")
		}
		e := a.newClient("")
		if r := e.postACS(samlSlug, at.response, at.relay, at.cookie); r.resp.Header.Get("Location") != "/login?sso_error=expired" {
			t.Fatalf("%s", r.resp.Header.Get("Location"))
		}
		// Linking works although the session cookie is not sent with the post: the
		// browser that started it has a host-only binding cookie, sent cross-site.
		tang := a.addUser("tang", pw, "member")
		idp.setSession(func(s *crewjamsaml.Session) { s.NameID = "subject-tang" })
		at = idp.begin(t, tang, samlSlug, map[string]any{"intent": "link", "return": "/settings"})
		bind := tang.cookies["__Host-petalbook_saml_link_campus"]
		if bind == nil || bind.SameSite != http.SameSiteNoneMode || !bind.Secure || !bind.HttpOnly || bind.Path != "/" || bind.Domain != "" || bind.Value == "" {
			t.Fatalf("binding cookie %+v", bind)
		}
		// An attempt cookie planted in another browser (a sibling subdomain can
		// set a __Secure- cookie) has no binding: refused, and nothing is linked.
		planted := idp.begin(t, tang, samlSlug, map[string]any{"intent": "link", "return": "/settings"})
		if lr := a.newClient("").postACS(samlSlug, planted.response, planted.relay, planted.cookie); lr.resp.Header.Get("Location") != "/settings?sso_error=expired" {
			t.Fatalf("planted: %s", lr.resp.Header.Get("Location"))
		}
		other := a.newClient("")
		other.cookies[bind.Name] = &http.Cookie{Name: bind.Name, Value: "not-the-binding", SameSite: http.SameSiteNoneMode}
		if lr := other.postACS(samlSlug, at.response, at.relay, at.cookie); lr.resp.Header.Get("Location") != "/settings?sso_error=expired" {
			t.Fatalf("wrong binding: %s", lr.resp.Header.Get("Location"))
		}
		if ids := tang.do("GET", "/api/account", nil).body["identities"].([]any); len(ids) != 0 {
			t.Fatal(ids)
		}
		at = idp.begin(t, tang, samlSlug, map[string]any{"intent": "link", "return": "/settings"})
		lr := tang.postACS(samlSlug, at.response, at.relay, at.cookie)
		if lr.resp.Header.Get("Location") != "/settings" {
			t.Fatalf("%s", lr.resp.Header.Get("Location"))
		}
		if ids := tang.do("GET", "/api/account", nil).body["identities"].([]any); len(ids) != 1 {
			t.Fatal(ids)
		}
		// Which another account has.
		at = idp.begin(t, admin, samlSlug, map[string]any{"intent": "link", "return": "/settings"})
		if lr := admin.postACS(samlSlug, at.response, at.relay, at.cookie); lr.resp.Header.Get("Location") != "/settings?sso_error=identity_in_use" {
			t.Fatalf("%s", lr.resp.Header.Get("Location"))
		}
		idp.reset()
	})
}

func TestSAMLRefusesWhatItMust(t *testing.T) {
	t.Parallel()
	idp := newFakeSAML(t, false)
	weak := newFakeSAML(t, true)
	evil := newFakeSAML(t, false)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		admin := samlFixture(t, a, idp, map[string]any{"autoCreate": true})
		weak.spMeta, evil.spMeta = idp.spMeta, idp.spMeta
		post := func(body []byte, relay string, ck *http.Cookie) string {
			r := a.newClient("").postACS(samlSlug, body, relay, ck)
			if r.code != 303 {
				t.Fatalf("%d %s", r.code, r.raw)
			}
			return r.resp.Header.Get("Location")
		}
		expect := func(name, code string, mutate func(at samlAttempt) ([]byte, string, *http.Cookie)) {
			t.Helper()
			idp.reset()
			c := a.newClient("")
			at := idp.begin(t, c, samlSlug, map[string]any{})
			body, relay, ck := at.response, at.relay, at.cookie
			if mutate != nil {
				body, relay, ck = mutate(at)
			}
			if loc := post(body, relay, ck); loc != "/login?sso_error="+code {
				t.Errorf("%s: %s, want %s", name, loc, code)
			}
		}
		same := func(at samlAttempt) ([]byte, string, *http.Cookie) { return at.response, at.relay, at.cookie }
		_ = same

		expect("a wrong RelayState", "expired", func(at samlAttempt) ([]byte, string, *http.Cookie) { return at.response, "forged-relay", at.cookie })
		expect("no RelayState", "expired", func(at samlAttempt) ([]byte, string, *http.Cookie) { return at.response, "", at.cookie })
		expect("no cookie", "expired", func(at samlAttempt) ([]byte, string, *http.Cookie) { return at.response, at.relay, nil })
		expect("another browser's cookie", "expired", func(at samlAttempt) ([]byte, string, *http.Cookie) {
			return at.response, at.relay, &http.Cookie{Name: at.cookie.Name, Value: "somebody-else"}
		})
		// A mismatch does not burn the real attempt: it can still be finished.
		idp.reset()
		c := a.newClient("")
		at := idp.begin(t, c, samlSlug, map[string]any{})
		if loc := post(at.response, "forged", at.cookie); loc != "/login?sso_error=expired" {
			t.Fatal(loc)
		}
		if loc := post(at.response, at.relay, at.cookie); loc != "/" {
			t.Fatalf("the real attempt was cancelled: %s", loc)
		}
		// Expired attempts.
		idp.reset()
		at = idp.begin(t, c, samlSlug, map[string]any{})
		a.clock.Advance(11 * time.Minute)
		if loc := post(at.response, at.relay, at.cookie); loc != "/login?sso_error=expired" {
			t.Fatal(loc)
		}

		// The response must be for this service's ACS.
		expect("an unsigned response", "saml_assertion_rejected", func(at samlAttempt) ([]byte, string, *http.Cookie) {
			x := regexp.MustCompile(`(?s)<(ds:)?Signature.*?</(ds:)?Signature>`).ReplaceAll(at.response, nil)
			return x, at.relay, at.cookie
		})
		expect("a tampered assertion", "saml_assertion_rejected", func(at samlAttempt) ([]byte, string, *http.Cookie) {
			return []byte(strings.Replace(string(at.response), "subject-001", "subject-admin", 1)), at.relay, at.cookie
		})
		expect("two assertions", "saml_multiple_assertions", func(at samlAttempt) ([]byte, string, *http.Cookie) {
			s := string(at.response)
			i := strings.Index(s, "<saml:Assertion")
			j := strings.Index(s, "</saml:Assertion>") + len("</saml:Assertion>")
			if i < 0 || j < i {
				t.Fatalf("no assertion in %s", s)
			}
			return []byte(s[:j] + s[i:j] + s[j:]), at.relay, at.cookie
		})
		expect("an encrypted assertion", "saml_encrypted", func(at samlAttempt) ([]byte, string, *http.Cookie) {
			s := strings.NewReplacer("<saml:Assertion", "<saml:EncryptedAssertion", "</saml:Assertion>", "</saml:EncryptedAssertion>").Replace(string(at.response))
			return []byte(s), at.relay, at.cookie
		})
		// A response signed by somebody else.
		idp.reset()
		c = a.newClient("")
		at = idp.begin(t, c, samlSlug, map[string]any{})
		evilResp, _ := func() ([]byte, string) {
			start := a.newClient("").do("POST", "/api/auth/saml/campus/start", map[string]any{}).want(t, 200, "")
			_ = start
			return nil, ""
		}()
		_ = evilResp
		// (the attacker answers the request it saw with its own key)
		evilAt := evil.beginAgainst(t, at)
		if loc := post(evilAt, at.relay, at.cookie); loc != "/login?sso_error=saml_assertion_rejected" {
			t.Errorf("a response signed by another key: %s", loc)
		}
		// SHA-1 is too weak.
		idp.reset()
		c = a.newClient("")
		at = idp.begin(t, c, samlSlug, map[string]any{})
		weakAt := weak.beginAgainst(t, at)
		if loc := post(weakAt, at.relay, at.cookie); loc != "/login?sso_error=saml_weak_signature" {
			t.Errorf("sha-1: %s", loc)
		}

		// What the assertion says.
		withSession := func(f func(*crewjamsaml.Session)) func() {
			return func() { idp.setSession(f) }
		}
		for name, c := range map[string]struct {
			set  func()
			code string
		}{
			"a transient NameID": {withSession(func(s *crewjamsaml.Session) { s.NameIDFormat = string(crewjamsaml.TransientNameIDFormat) }), "saml_transient_nameid"},
			"no NameID":          {withSession(func(s *crewjamsaml.Session) { s.NameID = "" }), "saml_no_nameid"},
			"a repeated attribute": {withSession(func(s *crewjamsaml.Session) {
				s.CustomAttributes = []crewjamsaml.Attribute{samlAttr("mail", "a@example.test"), samlAttr("mail", "b@example.test")}
			}), "saml_attributes"},
		} {
			idp.reset()
			c.set()
			cl := a.newClient("")
			at := idp.begin(t, cl, samlSlug, map[string]any{})
			if loc := post(at.response, at.relay, at.cookie); loc != "/login?sso_error="+c.code {
				t.Errorf("%s: %s", name, loc)
			}
		}
		idp.reset()
		// Nothing above made an account or a session.
		if rows := admin.audit(t, "login_failed"); len(rows) < 8 {
			t.Fatalf("%d refused sign-ins recorded", len(rows))
		}
		users := admin.do("GET", "/api/users", nil).want(t, 200, "")
		if n := len(users.body["users"].([]any)); n != 2 { // kazuha, and the one real sign-in above
			t.Fatalf("%d users: %s", n, users.raw)
		}
	})
}

// beginAgainst answers the AuthnRequest of at (the one the real provider's
// attempt made) with this identity provider's own key.
func (s *fakeSAML) beginAgainst(t *testing.T, at samlAttempt) []byte {
	t.Helper()
	// Rebuild the request from the response's InResponseTo: the other provider
	// answers a request made out to it.
	m := regexp.MustCompile(`InResponseTo="([^"]+)"`).FindSubmatch(at.response)
	if m == nil {
		t.Fatal("no InResponseTo")
	}
	return s.respondTo(t, string(m[1]))
}

func TestSAMLIdPInitiatedAndReplay(t *testing.T) {
	t.Parallel()
	idp := newFakeSAML(t, false)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		admin := samlFixture(t, a, idp, map[string]any{"autoCreate": true})
		// Refused by default.
		r := a.newClient("").postACS(samlSlug, idp.idpInitiated(t), "", nil)
		if r.resp.Header.Get("Location") != "/login?sso_error=expired" {
			t.Fatalf("%s", r.resp.Header.Get("Location"))
		}
		admin.do("PATCH", "/api/admin/auth/providers/campus", map[string]any{"saml": map[string]any{"allowIdpInitiated": true}}).want(t, 200, "")
		c := a.newClient("")
		resp := idp.idpInitiated(t)
		r = c.postACS(samlSlug, resp, "", nil)
		if r.resp.Header.Get("Location") != "/" || signedInAs(t, c) != "saml.person" {
			t.Fatalf("%s", r.resp.Header.Get("Location"))
		}
		// The same assertion again is a replay.
		r = a.newClient("").postACS(samlSlug, resp, "", nil)
		if r.resp.Header.Get("Location") != "/login?sso_error=saml_replay" {
			t.Fatalf("%s", r.resp.Header.Get("Location"))
		}
		// A response addressed to another ACS (its Destination) is refused.
		idp.acs = testPublic + "/somewhere/else"
		r = a.newClient("").postACS(samlSlug, idp.idpInitiated(t), "", nil)
		if r.resp.Header.Get("Location") != "/login?sso_error=saml_destination" {
			t.Fatalf("%s", r.resp.Header.Get("Location"))
		}
		idp.acs = ""
		// An unsolicited response never goes anywhere but to the front page, and not as a link.
		resp2 := idp.idpInitiated(t)
		r = a.newClient("").postACS(samlSlug, resp2, "/s/elsewhere", nil)
		if r.resp.Header.Get("Location") != "/login?sso_error=expired" {
			t.Fatalf("a RelayState without the cookie was believed: %s", r.resp.Header.Get("Location"))
		}
	})
}

func TestSAMLEndpoints(t *testing.T) {
	t.Parallel()
	idp := newFakeSAML(t, false)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		admin := samlFixture(t, a, idp, map[string]any{"enabled": false})
		// The metadata is served for a provider that is not enabled yet.
		m := a.newClient("").do("GET", "/api/auth/saml/campus/metadata", nil)
		if m.code != 200 || !strings.Contains(m.raw, "EntityDescriptor") || !strings.Contains(m.raw, "/api/auth/saml/campus/acs") || strings.Contains(m.raw, "PRIVATE") {
			t.Fatalf("%d %s", m.code, m.raw)
		}
		a.newClient("").do("GET", "/api/auth/saml/nope/metadata", nil).want(t, 404, "not_found")
		a.newClient("").do("GET", "/api/auth/oidc/campus/metadata", nil).want(t, 404, "not_found")
		// A disabled provider answers 404 / back to the page.
		c := a.newClient("")
		c.do("POST", "/api/auth/saml/campus/start", map[string]any{}).want(t, 404, "not_found")
		if r := c.do("GET", "/api/auth/saml/campus/start", nil); r.resp.Header.Get("Location") != "/login?sso_error=provider_unavailable" {
			t.Fatalf("%s", r.resp.Header.Get("Location"))
		}
		at := samlAttempt{response: []byte("<Response/>")}
		if r := c.postACS(samlSlug, at.response, "", nil); r.resp.Header.Get("Location") != "/login?sso_error=provider_unavailable" {
			t.Fatalf("%s", r.resp.Header.Get("Location"))
		}
		// The ACS is a form post and nothing else; a GET is the wrong method.
		c.do("POST", "/api/auth/saml/campus/acs", map[string]any{"SAMLResponse": "x"}).want(t, 415, "invalid")
		c.do("GET", "/api/auth/saml/campus/acs", nil).want(t, 405, "invalid")
		// Garbage and over-large responses.
		admin.do("PATCH", "/api/admin/auth/providers/campus", map[string]any{"enabled": true}).want(t, 200, "")
		for _, body := range [][]byte{[]byte("not xml at all"), []byte(`<?xml version="1.0"?><!DOCTYPE x [<!ENTITY a "b">]><Response>&a;</Response>`), []byte(strings.Repeat("A", 1<<21))} {
			r := a.newClient("").postACS(samlSlug, body, "", nil)
			if loc := r.resp.Header.Get("Location"); r.code != 303 || !strings.HasPrefix(loc, "/login?sso_error=") {
				t.Errorf("%d %s", r.code, loc)
			}
		}
		// A per-address allowance for the ACS.
		admin.putSetting("signin.perMinute", 2).want(t, 200, "")
		d := a.newClient("203.0.113.99")
		d.postACS(samlSlug, []byte("x"), "", nil)
		d.postACS(samlSlug, []byte("x"), "", nil)
		if r := d.postACS(samlSlug, []byte("x"), "", nil); r.resp.Header.Get("Location") != "/login?sso_error=rate_limited" {
			t.Fatalf("%s", r.resp.Header.Get("Location"))
		}
	})
}

func TestSAMLProviderFromMetadataAddress(t *testing.T) {
	t.Parallel()
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		idp, other := newFakeSAML(t, false), newFakeSAML(t, false) // one pair per database: the test shuts one down
		a.clock.Advance(time.Since(a.clock.Now()))
		admin := a.setup("kazuha", pw)
		b := idp.body("byurl", nil)
		b["saml"] = map[string]any{"metadataUrl": idp.srv.URL + "/metadata"}
		p := admin.do("POST", "/api/admin/auth/providers", b).want(t, 201, "")
		if p.body["enabled"] != true || p.body["saml"].(map[string]any)["idpEntityId"] != idp.idp.Metadata().EntityID {
			t.Fatal(p.raw)
		}
		// The same identity provider twice is refused.
		b2 := idp.body("again", nil)
		b2["saml"] = map[string]any{"metadataUrl": idp.srv.URL + "/metadata"}
		admin.do("POST", "/api/admin/auth/providers", b2).want(t, 409, "provider_issuer_in_use")
		// Metadata that cannot be used.
		for name, xml := range map[string]string{"garbage": "nope", "no idp": `<EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" entityID="https://x.example"></EntityDescriptor>`,
			"no certificate": regexp.MustCompile(`(?s)<KeyDescriptor.*?</KeyDescriptor>`).ReplaceAllString(other.metadata, "")} {
			bb := other.body("bad", nil)
			bb["saml"] = map[string]any{"metadataXml": xml}
			admin.do("POST", "/api/admin/auth/providers", bb).want(t, 400, "saml_metadata_invalid")
			_ = name
		}
		both := other.body("both", nil)
		both["saml"].(map[string]any)["metadataUrl"] = other.srv.URL + "/metadata"
		admin.do("POST", "/api/admin/auth/providers", both).want(t, 400, "invalid")
		neither := other.body("neither", nil)
		neither["saml"] = map[string]any{}
		admin.do("POST", "/api/admin/auth/providers", neither).want(t, 400, "invalid")
		// An address that cannot be reached is stored, disabled.
		down := other.body("down", nil)
		down["saml"] = map[string]any{"metadataUrl": "https://127.0.0.1:1/metadata"}
		d := admin.do("POST", "/api/admin/auth/providers", down).want(t, 201, "")
		if d.body["enabled"] != false || d.body["status"].(map[string]any)["ok"] != true && d.body["status"].(map[string]any)["message"] == "" {
			t.Fatal(d.raw)
		}
		// Refresh fetches again; the stored metadata keeps serving when the address is down.
		admin.do("POST", "/api/admin/auth/providers/byurl/refresh", nil).want(t, 200, "")
		idp.srv.Close()
		r := admin.do("POST", "/api/admin/auth/providers/byurl/refresh", nil).want(t, 200, "")
		if r.body["status"].(map[string]any)["ok"] != false {
			t.Fatal(r.raw)
		}
		if m := a.newClient("").do("GET", "/api/auth/saml/byurl/metadata", nil); m.code != 200 {
			t.Fatalf("the metadata is gone with the address: %d", m.code)
		}
	})
}
