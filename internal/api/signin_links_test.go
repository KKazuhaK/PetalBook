package api_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/auth/testidp"
	"petalbook.app/server/internal/store"

	crewjamsaml "github.com/crewjam/saml"
)

// A provider an organisation made with another's issuer string reaches none of
// the other's accounts: an account is found by the provider's own id and the
// subject, never by the issuer.
func TestACopiedIssuerNeverReachesAnotherProvidersAccounts(t *testing.T) {
	t.Parallel()
	f := newIDPFor(t, "one")
	f.idp.RedirectURIs = append(f.idp.RedirectURIs, testPublic+"/api/auth/oidc/dup/callback")
	f.addUser("alice", testidp.Claims{"sub": "sub-alice", "preferred_username": "alice", "email": "alice@example.test", "name": "Alice A"})
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		ctx := context.Background()
		admin := a.setup("kazuha", pw)
		admin.do("POST", "/api/admin/providers", f.providerBody("one", map[string]any{"autoCreate": true})).want(t, 201, "")
		admin.do("POST", "/api/admin/providers", f.providerBody("dup", map[string]any{"autoCreate": true})).want(t, 201, "")

		// The same issuer, the same subject, two providers: two accounts.
		c1, c2 := a.newClient(""), a.newClient("")
		if loc := f.signIn2(t, c1, "one", "alice", map[string]any{}); loc != "/" {
			t.Fatal(loc)
		}
		if loc := f.signIn2(t, c2, "dup", "alice", map[string]any{}); loc != "/" {
			t.Fatal(loc)
		}
		u1, u2 := signedInAs(t, c1), signedInAs(t, c2)
		if u1 == "" || u2 == "" || u1 == u2 {
			t.Fatalf("one account for two providers: %q %q", u1, u2)
		}
		l1, err1 := store.FindLink(ctx, a.st.Q(), "one", "sub-alice")
		l2, err2 := store.FindLink(ctx, a.st.Q(), "dup", "sub-alice")
		if err1 != nil || err2 != nil || l1.UserID == l2.UserID {
			t.Fatalf("%+v %v / %+v %v", l1, err1, l2, err2)
		}
		// Each signs in again to its own.
		d1, d2 := a.newClient(""), a.newClient("")
		f.signIn2(t, d1, "one", "alice", map[string]any{})
		f.signIn2(t, d2, "dup", "alice", map[string]any{})
		if signedInAs(t, d1) != u1 || signedInAs(t, d2) != u2 {
			t.Fatal("a sign-in reached the other provider's account")
		}
		// An account of the first cannot be linked to the person of the second.
		if loc := f.signIn2(t, c2, "one", "alice", map[string]any{"intent": "link", "return": "/settings"}); loc != "/settings?sso_error=identity_in_use" {
			t.Fatal(loc)
		}

		// An organisation's own provider with the same issuer string: made, never
		// on (it has no verified domain), never reached by a slug, and it has no accounts.
		admin.putSetting("features.tenancy", true).want(t, 200, "")
		org := admin.do("POST", "/api/admin/tenants", map[string]any{"slug": "acme", "name": "Acme"}).want(t, 201, "").body["id"].(string)
		// Slugs are the server's: the site's "one" is taken for the organisation too.
		admin.do("POST", "/api/admin/providers", f.providerBody("one", map[string]any{"owner": org})).want(t, 409, "exists")
		p := admin.do("POST", "/api/admin/providers", f.providerBody("acme-one", map[string]any{"owner": org})).want(t, 201, "")
		if p.body["owner"] != org || p.body["enabled"] != false || p.body["id"] == "one" || p.body["tenant"] != org {
			t.Fatal(p.raw)
		}
		orgID := p.body["id"].(string)
		if _, err := store.FindLink(ctx, a.st.Q(), orgID, "sub-alice"); !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
		admin.do("PATCH", "/api/admin/providers/"+orgID, map[string]any{"enabled": true}).want(t, 409, "domain_required")
		admin.do("POST", "/api/admin/providers", f.providerBody("two", map[string]any{"owner": org, "enabled": true})).want(t, 409, "domain_required")
		pub := a.newClient("").do("GET", "/api/auth/providers", nil).want(t, 200, "")
		if n := len(pub.body["providers"].([]any)); n != 2 {
			t.Fatalf("the sign-in page lists %d providers: %s", n, pub.raw)
		}
		// The slug "one" is still the site's; the organisation's is another provider.
		if got := admin.do("GET", "/api/admin/providers/one", nil).want(t, 200, "").body["owner"]; got != "site" {
			t.Fatal(got)
		}
		admin.do("POST", "/api/admin/providers", f.providerBody("acme-one", map[string]any{"owner": org})).want(t, 409, "exists")
		admin.do("POST", "/api/admin/providers", f.providerBody("acme-one", map[string]any{})).want(t, 409, "exists")
		// An unknown owner is refused.
		admin.do("POST", "/api/admin/providers", f.providerBody("three", map[string]any{"owner": "NOSUCHTENANT"})).want(t, 400, "invalid")
		// Deleting the organisation deletes its providers (and the accounts linked to them).
		admin.do("DELETE", "/api/admin/tenants/"+org, nil).want(t, 204, "")
		admin.do("GET", "/api/admin/providers/"+orgID, nil).want(t, 404, "not_found")
		if got := admin.do("GET", "/api/admin/providers", nil).want(t, 200, ""); len(got.body["providers"].([]any)) != 2 {
			t.Fatal(got.raw)
		}
	})
}

// An organisation's provider can be turned on once its tenant has a verified
// domain (the hook of 3.6 (d)), and then vouches only for addresses on it.
func TestAnOrganisationsProviderNeedsAVerifiedDomain(t *testing.T) {
	t.Parallel()
	f := newIDPFor(t, "one")
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		ctx := context.Background()
		admin := a.setup("kazuha", pw)
		admin.putSetting("features.tenancy", true).want(t, 200, "")
		org := admin.do("POST", "/api/admin/tenants", map[string]any{"slug": "acme", "name": "Acme"}).want(t, 201, "").body["id"].(string)
		domains := []string{}
		svc, err := auth.New(ctx, auth.Config{Store: a.st, Spaces: a.mgr, PublicURL: testPublic, HTTPClient: anyTLSClient(), Password: fastPasswords, Now: a.clock.Now,
			OrgDomains: func(_ context.Context, tenant string) []string {
				if tenant == org {
					return domains
				}
				return nil
			}})
		if err != nil {
			t.Fatal(err)
		}
		on := true
		in := func(slug string) auth.ProviderInput {
			kind, name := "oidc", "Acme SSO "+slug
			issuer, id, secret := f.srv.URL, idpClient, idpSecret
			o := org
			return auth.ProviderInput{Owner: &o, Slug: &slug, Kind: &kind, Name: &name, Enabled: &on,
				OIDC: &auth.OIDCInput{Issuer: &issuer, ClientID: &id, ClientSecret: &secret}}
		}
		var ae *auth.Error
		if _, err := svc.CreateProvider(ctx, "", "", in("acme-sso")); !errors.As(err, &ae) || ae.Code != "domain_required" {
			t.Fatalf("without a verified domain: %v", err)
		}
		// With a verified domain it can be made on, and is the organisation's.
		domains = []string{"acme.test"}
		v, err := svc.CreateProvider(ctx, "", "", in("acme-sso"))
		if err != nil || !v.Enabled || v.Owner != org || v.ID == "acme-sso" {
			t.Fatalf("%+v %v", v, err)
		}
		// The domain goes away (3.6 (d) will say so): it cannot be switched on again.
		off := false
		if _, err := svc.UpdateProvider(ctx, "", "", v.ID, auth.ProviderInput{Enabled: &off}); err != nil {
			t.Fatal(err)
		}
		domains = nil
		if _, err := svc.UpdateProvider(ctx, "", "", v.ID, auth.ProviderInput{Enabled: &on}); !errors.As(err, &ae) || ae.Code != "domain_required" {
			t.Fatalf("%v", err)
		}
		// The site's own vouching switch is not an organisation's to set.
		yes := true
		if _, err := svc.UpdateProvider(ctx, "", "", v.ID, auth.ProviderInput{MergeVerifiedEmail: &yes}); !errors.As(err, &ae) || ae.Code != "invalid" {
			t.Fatalf("%v", err)
		}
	})
}

// One linked account per provider per person; to switch, unlink first. Linking
// again after unlinking works.
func TestOneLinkedAccountPerProviderUnlinkAndRelink(t *testing.T) {
	t.Parallel()
	f := newIDPFor(t, "one")
	f.addUser("alice", testidp.Claims{"sub": "sub-alice", "preferred_username": "alice", "email": "alice@example.test"})
	f.addUser("alice-two", testidp.Claims{"sub": "sub-alice-two", "preferred_username": "alice2", "email": "alice2@example.test"})
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		ctx := context.Background()
		admin := a.setup("kazuha", pw)
		admin.do("POST", "/api/admin/providers", f.providerBody("one", nil)).want(t, 201, "")
		tang := a.addUser("tang", pw, "member")
		tangID := mustUserID(t, a, "tang")

		start := func() *result {
			r := tang.do("POST", "/api/account/links/one/start", map[string]any{"purpose": "signin", "return": "/settings"})
			return &r
		}
		// The start of a link answers an address at the provider.
		if r := start().want(t, 200, ""); r.body["url"] == "" || r.body["url"] == nil {
			t.Fatal(r.raw)
		}
		tang.do("POST", "/api/account/links/one/start", map[string]any{"purpose": "repos"}).want(t, 400, "invalid")
		tang.do("POST", "/api/account/links/nope/start", map[string]any{}).want(t, 404, "not_found")
		a.newClient("").do("POST", "/api/account/links/one/start", map[string]any{}).want(t, 401, "unauthenticated")

		link := func(who string) string {
			at := f.beginAt(t, tang, "/api/account/links/one/start", "/api/auth/oidc/one/callback", who, map[string]any{"purpose": "signin", "return": "/settings"})
			loc, _ := finish(t, tang, at.cb)
			return loc
		}
		if loc := link("alice"); loc != "/settings" {
			t.Fatal(loc)
		}
		ls := tang.do("GET", "/api/account/links", nil).want(t, 200, "").body["links"].([]any)
		if len(ls) != 1 || ls[0].(map[string]any)["providerId"] != "one" || ls[0].(map[string]any)["signin"] != true {
			t.Fatalf("%v", ls)
		}
		first := ls[0].(map[string]any)["id"].(string)
		// A second account at the same provider is refused up front ...
		start().want(t, 409, "already_linked")
		// ... and when the other tab got there first, at the callback.
		tang.do("DELETE", "/api/account/links/"+first, nil).want(t, 204, "")
		at := f.beginAt(t, tang, "/api/account/links/one/start", "/api/auth/oidc/one/callback", "alice-two", map[string]any{"return": "/settings"})
		now := a.clock.Now()
		if err := store.InsertLink(ctx, a.st.Q(), store.Link{ID: "RACE000001", ProviderID: "one", UserID: tangID, Subject: "sub-alice", SignIn: true, Created: now}); err != nil {
			t.Fatal(err)
		}
		if loc, _ := finish(t, tang, at.cb); loc != "/settings?sso_error=already_linked" {
			t.Fatal(loc)
		}
		if _, err := store.FindLink(ctx, a.st.Q(), "one", "sub-alice-two"); !errors.Is(err, store.ErrNotFound) {
			t.Fatal("the second account was linked")
		}
		// To switch: unlink, then link the other; and the first is free again for anyone.
		tang.do("DELETE", "/api/account/links/RACE000001", nil).want(t, 204, "")
		if loc := link("alice-two"); loc != "/settings" {
			t.Fatal(loc)
		}
		ls = tang.do("GET", "/api/account/links", nil).want(t, 200, "").body["links"].([]any)
		if len(ls) != 1 {
			t.Fatalf("%v", ls)
		}
		l, err := store.FindLink(ctx, a.st.Q(), "one", "sub-alice-two")
		if err != nil || l.UserID != tangID || !l.SignIn {
			t.Fatalf("%+v %v", l, err)
		}
		// Signing in with the linked account reaches the person, and records its address.
		c := a.newClient("")
		if loc := f.signIn2(t, c, "one", "alice-two", map[string]any{}); loc != "/" || signedInAs(t, c) != "tang" {
			t.Fatal(loc)
		}
		if l, _ := store.FindLink(ctx, a.st.Q(), "one", "sub-alice-two"); l.Email != "alice2@example.test" || l.EmailTrusted || l.LastUsed.IsZero() {
			t.Fatalf("%+v", l)
		}
	})
}

// SAML: the address, which is the account's UPN, is required. It comes from the
// configured attribute (mail by default), else from the NameID when its format
// says it is an address; otherwise the sign-in is refused (saml_no_email). The
// subject is the NameID either way.
func TestSAMLEmailIsRequiredFromTheAttributeOrAnEmailNameID(t *testing.T) {
	t.Parallel()
	idp := newFakeSAML(t, false)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		ctx := context.Background()
		// No attributes given: the address attribute is mail.
		b := idp.body(samlSlug, map[string]any{"autoCreate": true})
		b["saml"] = map[string]any{"metadataXml": idp.metadata}
		a.clock.Advance(timeSince(a))
		admin := a.setup("kazuha", pw)
		p := admin.do("POST", "/api/admin/providers", b).want(t, 201, "")
		if p.body["saml"].(map[string]any)["attributes"].(map[string]any)["email"] != "mail" {
			t.Fatal(p.raw)
		}
		idp.learn(t, admin, samlSlug)
		sign := func(name string, mutate func(x *crewjamsaml.Session)) string {
			t.Helper()
			idp.reset()
			idp.setSession(mutate)
			c := a.newClient("")
			at := idp.begin(t, c, samlSlug, map[string]any{})
			r := c.postACS(samlSlug, at.response, at.relay, at.cookie)
			if r.code != 303 {
				t.Fatalf("%s: %d %s", name, r.code, r.raw)
			}
			return r.resp.Header.Get("Location")
		}
		noMail := func(x *crewjamsaml.Session) {
			x.CustomAttributes = []crewjamsaml.Attribute{samlAttr("displayName", "Pat")}
		}

		// An opaque NameID and no address: refused, and nothing is made.
		if loc := sign("opaque", func(x *crewjamsaml.Session) { noMail(x); x.NameID = "opaque-1" }); loc != "/login?sso_error=saml_no_email" {
			t.Fatal(loc)
		}
		if _, err := store.FindLink(ctx, a.st.Q(), samlSlug, "opaque-1"); !errors.Is(err, store.ErrNotFound) {
			t.Fatal("a link was made without an address")
		}
		// A NameID that looks like an address but is not in the emailAddress format: not an address.
		if loc := sign("persistent address", func(x *crewjamsaml.Session) { noMail(x); x.NameID = "pat@example.test" }); loc != "/login?sso_error=saml_no_email" {
			t.Fatal(loc)
		}
		// An attribute that is not an address does not count either.
		if loc := sign("junk", func(x *crewjamsaml.Session) {
			x.NameID = "opaque-2"
			x.CustomAttributes = []crewjamsaml.Attribute{samlAttr("mail", "not an address")}
		}); loc != "/login?sso_error=saml_no_email" {
			t.Fatal(loc)
		}
		// The NameID in the emailAddress format is the address, and still the subject.
		if loc := sign("email nameid", func(x *crewjamsaml.Session) {
			noMail(x)
			x.NameID, x.NameIDFormat = "pat@example.test", string(crewjamsaml.EmailAddressNameIDFormat)
		}); loc != "/" {
			t.Fatal(loc)
		}
		l, err := store.FindLink(ctx, a.st.Q(), samlSlug, "pat@example.test")
		if err != nil || l.Email != "pat@example.test" || l.EmailTrusted {
			t.Fatalf("%+v %v", l, err)
		}
		// The attribute wins over the NameID, and the NameID stays the subject: a
		// persistent NameID with a mail attribute is the recommended shape.
		if loc := sign("persistent", func(x *crewjamsaml.Session) {
			x.NameID = "persistent-3"
			x.CustomAttributes = []crewjamsaml.Attribute{samlAttr("mail", "lee@example.test")}
		}); loc != "/" {
			t.Fatal(loc)
		}
		if l, err := store.FindLink(ctx, a.st.Q(), samlSlug, "persistent-3"); err != nil || l.Email != "lee@example.test" {
			t.Fatalf("%+v %v", l, err)
		}
		// An administrator maps another attribute for the address.
		admin.do("PATCH", "/api/admin/providers/"+samlSlug, map[string]any{"saml": map[string]any{"attributes": map[string]any{"email": "upn", "name": "", "username": ""}}}).want(t, 200, "")
		if loc := sign("upn", func(x *crewjamsaml.Session) {
			x.NameID = "persistent-4"
			x.CustomAttributes = []crewjamsaml.Attribute{samlAttr("mail", "wrong@example.test"), samlAttr("upn", "kim@example.test")}
		}); loc != "/" {
			t.Fatal(loc)
		}
		if l, err := store.FindLink(ctx, a.st.Q(), samlSlug, "persistent-4"); err != nil || l.Email != "kim@example.test" {
			t.Fatalf("%+v %v", l, err)
		}
	})
}

// A SAML address is trusted (verified at once) only when the site's own
// provider has the administrator's opt-in, and one that another account holds
// verified is never linked automatically.
func TestSAMLAddressIsTrustedOnlyWithTheOptInAndNeverTakenFromAVerifiedHolder(t *testing.T) {
	t.Parallel()
	idp := newFakeSAML(t, false)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		admin := samlFixture(t, a, idp, map[string]any{"autoCreate": true})
		sign := func(nameID, mail string) (string, *client) {
			t.Helper()
			idp.reset()
			idp.setSession(func(x *crewjamsaml.Session) {
				x.NameID = nameID
				x.CustomAttributes = []crewjamsaml.Attribute{samlAttr("mail", mail), samlAttr("displayName", "P")}
			})
			c := a.newClient("")
			at := idp.begin(t, c, samlSlug, map[string]any{})
			r := c.postACS(samlSlug, at.response, at.relay, at.cookie)
			return r.resp.Header.Get("Location"), c
		}
		// Without the opt-in: the address is kept, unverified.
		if loc, c := sign("n-1", "one@example.test"); loc != "/" || c.do("GET", "/api/session", nil).user()["emailVerified"] != false {
			t.Fatal(loc)
		}
		admin.do("PATCH", "/api/admin/providers/"+samlSlug, map[string]any{"mergeVerifiedEmail": true}).want(t, 200, "")
		// With it: verified at once.
		loc, c := sign("n-2", "two@example.test")
		if u := c.do("GET", "/api/session", nil).user(); loc != "/" || u["emailVerified"] != true || u["email"] != "two@example.test" {
			t.Fatal(loc, u)
		}
		// An address verified on another account is never linked automatically: the
		// person is told to sign in to that account and link the provider there.
		if loc, c := sign("n-3", "two@example.test"); loc != "/login?sso_error=email_in_use" || signedInAs(t, c) != "" {
			t.Fatal(loc)
		}
	})
}

func timeSince(a *acc) (d time.Duration) { return time.Since(a.clock.Now()) }
