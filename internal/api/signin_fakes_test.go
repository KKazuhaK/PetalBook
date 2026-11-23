package api_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/auth/testidp"
	crewjamsaml "github.com/crewjam/saml"
	dsig "github.com/russellhaering/goxmldsig"
)

// anyTLSClient trusts every test server: the fake providers each have a
// certificate of their own.
func anyTLSClient() *http.Client {
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, Timeout: 20 * time.Second}
}

// newIDPFor makes a fake OpenID Connect provider for the provider slug.
func newIDPFor(t *testing.T, slug string) *fakeIDP {
	t.Helper()
	idp := testidp.New("", idpClient, idpSecret, testPublic+"/api/auth/oidc/"+slug+"/callback")
	srv := httptest.NewTLSServer(idp.Handler())
	t.Cleanup(srv.Close)
	idp.Issuer = srv.URL
	idp.Users = map[string]testidp.Claims{}
	return &fakeIDP{idp, srv}
}

func (f *fakeIDP) addUser(key string, c testidp.Claims) { f.idp.Users[key] = c }

// providerBody is a POST /api/admin/auth/providers body for this provider.
func (f *fakeIDP) providerBody(slug string, extra map[string]any) map[string]any {
	b := map[string]any{"slug": slug, "kind": "oidc", "name": "Provider " + slug,
		"oidc": map[string]any{"issuer": f.srv.URL, "clientId": idpClient, "clientSecret": idpSecret}}
	for k, v := range extra {
		b[k] = v
	}
	return b
}

// --- GitHub ----------------------------------------------------------------------

type fakeGitHub struct {
	srv   *httptest.Server
	mu    sync.Mutex
	users map[string]ghFake // by key
	codes map[string]string // code -> key
	toks  map[string]string // token -> key
	// seen is what the server was asked, for tests of what the user token is used for.
	seen []string
}

type ghFake struct {
	ID       int64
	Login    string
	Name     string
	Email    string
	Verified bool
	Orgs     []string
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{users: map[string]ghFake{}, codes: map[string]string{}, toks: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login/oauth/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		key := q.Get("user")
		if _, ok := g.users[key]; !ok || q.Get("client_id") != "gh-client" || q.Get("code_challenge") == "" || q.Get("state") == "" {
			http.Error(w, "bad request", 400)
			return
		}
		code := fmt.Sprintf("code-%d", len(g.codes)+1)
		g.mu.Lock()
		g.codes[code] = key
		g.mu.Unlock()
		to, _ := url.Parse(q.Get("redirect_uri"))
		v := to.Query()
		v.Set("code", code)
		v.Set("state", q.Get("state"))
		to.RawQuery = v.Encode()
		http.Redirect(w, r, to.String(), http.StatusFound)
	})
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		g.mu.Lock()
		defer g.mu.Unlock()
		key, ok := g.codes[r.PostForm.Get("code")]
		if !ok || r.PostForm.Get("client_secret") != "gh-secret" || r.PostForm.Get("code_verifier") == "" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"error": "bad_verification_code"})
			return
		}
		delete(g.codes, r.PostForm.Get("code"))
		tok := "gho_" + key
		g.toks[tok] = key
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"access_token": tok, "token_type": "bearer", "scope": "read:user,user:email"})
	})
	user := func(w http.ResponseWriter, r *http.Request) (ghFake, bool) {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.seen = append(g.seen, r.URL.Path)
		key, ok := g.toks[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		if !ok {
			http.Error(w, "unauthorized", 401)
			return ghFake{}, false
		}
		return g.users[key], true
	}
	mux.HandleFunc("GET /api/v3/user", func(w http.ResponseWriter, r *http.Request) {
		if u, ok := user(w, r); ok {
			json.NewEncoder(w).Encode(map[string]any{"id": u.ID, "login": u.Login, "name": u.Name})
		}
	})
	mux.HandleFunc("GET /api/v3/user/emails", func(w http.ResponseWriter, r *http.Request) {
		if u, ok := user(w, r); ok {
			json.NewEncoder(w).Encode([]map[string]any{{"email": "noise@users.noreply.example", "primary": false, "verified": true}, {"email": u.Email, "primary": true, "verified": u.Verified}})
		}
	})
	mux.HandleFunc("GET /api/v3/user/orgs", func(w http.ResponseWriter, r *http.Request) {
		if u, ok := user(w, r); ok {
			out := []map[string]string{}
			for _, o := range u.Orgs {
				out = append(out, map[string]string{"login": o})
			}
			json.NewEncoder(w).Encode(out)
		}
	})
	g.srv = httptest.NewTLSServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGitHub) body(slug string, extra map[string]any) map[string]any {
	b := map[string]any{"slug": slug, "kind": "github", "name": "GitHub", "icon": "preset:github",
		"github": map[string]any{"baseUrl": g.srv.URL, "clientId": "gh-client", "clientSecret": "gh-secret"}}
	for k, v := range extra {
		b[k] = v
	}
	return b
}

// --- SAML ---------------------------------------------------------------------------

type fakeSAML struct {
	t        *testing.T
	idp      *crewjamsaml.IdentityProvider
	srv      *httptest.Server
	metadata string
	spMeta   *crewjamsaml.EntityDescriptor
	// session is what the identity provider says about the person who signs in.
	mu      sync.Mutex
	session crewjamsaml.Session
	// acs, when set, is the ACS address the identity provider addresses responses to (to test a wrong Destination).
	acs string
}

func newFakeSAML(t *testing.T, sha1 bool) *fakeSAML {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 100))
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "idp.example.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	s := &fakeSAML{t: t, session: crewjamsaml.Session{ID: "sess-1", CreateTime: time.Now(), ExpireTime: time.Now().Add(time.Hour), Index: "idx-1",
		NameID: "subject-001", NameIDFormat: string(crewjamsaml.PersistentNameIDFormat), UserEmail: "saml.person@example.test", UserCommonName: "Saml Person"}}
	s.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metadata" {
			w.Header().Set("Content-Type", "application/samlmetadata+xml")
			w.Write([]byte(s.metadata))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(s.srv.Close)
	method := dsig.RSASHA256SignatureMethod
	if sha1 {
		method = dsig.RSASHA1SignatureMethod
	}
	metaURL, _ := url.Parse(s.srv.URL + "/metadata")
	ssoURL, _ := url.Parse(s.srv.URL + "/sso")
	s.idp = &crewjamsaml.IdentityProvider{Key: key, Certificate: cert, MetadataURL: *metaURL, SSOURL: *ssoURL, SignatureMethod: method,
		ServiceProviderProvider: s, SessionProvider: s}
	raw, err := xml.Marshal(s.idp.Metadata())
	if err != nil {
		t.Fatal(err)
	}
	s.metadata = string(raw)
	return s
}

func (s *fakeSAML) GetServiceProvider(_ *http.Request, id string) (*crewjamsaml.EntityDescriptor, error) {
	if s.spMeta == nil || id != s.spMeta.EntityID {
		return nil, fmt.Errorf("unknown service provider %q", id)
	}
	d := *s.spMeta
	if s.acs != "" {
		sp := d.SPSSODescriptors[0]
		acs := make([]crewjamsaml.IndexedEndpoint, len(sp.AssertionConsumerServices))
		copy(acs, sp.AssertionConsumerServices)
		acs[0].Location = s.acs
		sp.AssertionConsumerServices = acs
		d.SPSSODescriptors = []crewjamsaml.SPSSODescriptor{sp}
	}
	return &d, nil
}

func (s *fakeSAML) GetSession(_ http.ResponseWriter, _ *http.Request, _ *crewjamsaml.IdpAuthnRequest) *crewjamsaml.Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.session
	return &sess
}

func (s *fakeSAML) body(slug string, extra map[string]any) map[string]any {
	b := map[string]any{"slug": slug, "kind": "saml", "name": "Campus " + slug,
		"saml": map[string]any{"metadataXml": s.metadata, "attributes": map[string]any{"email": "mail", "name": "displayName", "username": "uid"}}}
	for k, v := range extra {
		b[k] = v
	}
	return b
}

// learn reads the service provider metadata the app serves, so the identity provider knows it.
func (s *fakeSAML) learn(t *testing.T, c *client, slug string) {
	t.Helper()
	r := c.do("GET", "/api/auth/saml/"+slug+"/metadata", nil)
	if r.code != 200 || !strings.HasPrefix(r.resp.Header.Get("Content-Type"), "application/samlmetadata+xml") {
		t.Fatalf("metadata: %d %s %s", r.code, r.resp.Header.Get("Content-Type"), r.raw)
	}
	var d crewjamsaml.EntityDescriptor
	if err := xml.Unmarshal([]byte(r.raw), &d); err != nil {
		t.Fatal(err)
	}
	s.spMeta = &d
}

// samlAttempt is a SAML sign-in between the start and the post to the ACS.
type samlAttempt struct {
	cookie *http.Cookie
	relay  string
	// response is the decoded <Response> the identity provider made.
	response []byte
}

// begin starts a sign-in (the browser's part) and lets the identity provider answer.
func (s *fakeSAML) begin(t *testing.T, c *client, slug string, body map[string]any) samlAttempt {
	t.Helper()
	r := c.do("POST", "/api/auth/saml/"+slug+"/start", body).want(t, 200, "")
	var ck *http.Cookie
	for _, x := range r.resp.Cookies() {
		if x.Name == "__Secure-petalbook_saml_"+slug {
			ck = x
		}
	}
	if ck == nil {
		t.Fatalf("no attempt cookie: %v", r.resp.Cookies())
	}
	u, _ := url.Parse(r.body["url"].(string))
	req := httptest.NewRequest("GET", u.String(), nil)
	rec := httptest.NewRecorder()
	s.idp.ServeSSO(rec, req)
	if rec.Code != 200 {
		t.Fatalf("the identity provider answered %d: %s", rec.Code, rec.Body.String())
	}
	resp, relay := formFields(t, rec.Body.String())
	return samlAttempt{cookie: ck, relay: relay, response: resp}
}

// idpInitiated has the identity provider answer unasked.
func (s *fakeSAML) idpInitiated(t *testing.T) []byte {
	t.Helper()
	rec := httptest.NewRecorder()
	s.idp.ServeIDPInitiated(rec, httptest.NewRequest("GET", s.srv.URL+"/idp-initiated", nil), s.spMeta.EntityID, "")
	if rec.Code != 200 {
		t.Fatalf("idp-initiated: %d %s", rec.Code, rec.Body.String())
	}
	resp, _ := formFields(t, rec.Body.String())
	return resp
}

func formFields(t *testing.T, page string) (response []byte, relay string) {
	t.Helper()
	field := func(name string) string {
		m := `name="` + name + `" value="`
		i := strings.Index(page, m)
		if i < 0 {
			return ""
		}
		rest := page[i+len(m):]
		return html.UnescapeString(rest[:strings.Index(rest, `"`)])
	}
	raw, err := base64.StdEncoding.DecodeString(field("SAMLResponse"))
	if err != nil || len(raw) == 0 {
		t.Fatalf("no SAMLResponse in %s", page)
	}
	return raw, field("RelayState")
}

// post sends a response to the ACS the way the identity provider's page does:
// a form from another site, with the browser's attempt cookie when it has one.
func (c *client) postACS(slug string, response []byte, relay string, ck *http.Cookie) result {
	form := url.Values{"SAMLResponse": {base64.StdEncoding.EncodeToString(response)}}
	if relay != "" {
		form.Set("RelayState", relay)
	}
	path := "/api/auth/saml/" + slug + "/acs"
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Host = strings.TrimPrefix(c.a.public, "https://")
	req.RemoteAddr = c.ip + ":4321"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Origin", "https://idp.example.test")
	if ck != nil {
		req.AddCookie(&http.Cookie{Name: ck.Name, Value: ck.Value})
	}
	for _, x := range c.cookies {
		req.AddCookie(&http.Cookie{Name: x.Name, Value: x.Value})
	}
	rec := httptest.NewRecorder()
	c.a.h.ServeHTTP(rec, req)
	resp := rec.Result()
	for _, x := range resp.Cookies() {
		if x.MaxAge < 0 || x.Value == "" {
			delete(c.cookies, x.Name)
		} else if x.Name == "__Host-petalbook_session" || x.Name == "petalbook_session" {
			c.cookies[x.Name] = x
		}
	}
	return result{rec.Code, nil, rec.Body.String(), resp}
}
