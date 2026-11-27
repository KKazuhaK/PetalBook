package api_test

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/xml"
	"io"
	"net/url"
	"testing"

	crewjamsaml "github.com/crewjam/saml"
)

// samlRequestAt decodes the AuthnRequest of the start's redirect.
func samlRequestAt(t *testing.T, r result) crewjamsaml.AuthnRequest {
	t.Helper()
	if r.code != 303 {
		t.Fatalf("%d %s", r.code, r.raw)
	}
	loc, err := url.Parse(r.resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	deflated, err := base64.StdEncoding.DecodeString(loc.Query().Get("SAMLRequest"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(flate.NewReader(bytes.NewReader(deflated)))
	if err != nil {
		t.Fatal(err)
	}
	var req crewjamsaml.AuthnRequest
	if err := xml.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	return req
}

// A SAML provider whose NameID is the address gets the address the person
// typed as the request's Subject (the SAML side of login_hint, 3.6 d4); one
// whose NameID is something else gets none, since an identity provider that
// honours a Subject authenticates that subject alone.
func TestTheSAMLStartCarriesTheAddressAsTheSubject(t *testing.T) {
	t.Parallel()
	idp := newFakeSAML(t, false)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		admin := samlFixture(t, a, idp, nil)
		start := func(hint string) crewjamsaml.AuthnRequest {
			return samlRequestAt(t, a.newClient("").do("GET", "/api/auth/saml/"+samlSlug+"/start?login_hint="+url.QueryEscape(hint), nil))
		}
		if req := start("tang@acme.example"); req.Subject != nil {
			t.Fatalf("a Subject for a provider whose NameID is not the address: %+v", req.Subject.NameID)
		}
		admin.do("PATCH", "/api/admin/providers/"+samlSlug, map[string]any{"saml": map[string]any{"nameIdFormat": "emailAddress"}}).want(t, 200, "")
		req := start("tang@acme.example")
		if req.Subject == nil || req.Subject.NameID == nil || req.Subject.NameID.Value != "tang@acme.example" ||
			req.Subject.NameID.Format != string(crewjamsaml.EmailAddressNameIDFormat) {
			t.Fatalf("%+v", req.Subject)
		}
		// A hint that is not one line is not sent.
		if req := start("a\nb@acme.example"); req.Subject != nil {
			t.Fatalf("%+v", req.Subject.NameID)
		}
		if req := samlRequestAt(t, a.newClient("").do("GET", "/api/auth/saml/"+samlSlug+"/start", nil)); req.Subject != nil {
			t.Fatal("a Subject without a hint")
		}
	})
}
