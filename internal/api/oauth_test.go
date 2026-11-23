package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/oauth"
	"github.com/KKazuhaK/PetalBook/internal/secret"
)

const (
	cbURI   = "http://localhost:7777/callback"
	mcpRes  = "https://notes.example.test/mcp"
	oaIssue = "https://notes.example.test"
)

func pkcePair(seed string) (verifier, challenge string) {
	verifier = strings.Repeat(seed, 50)[:50]
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

func oa() accOpts { return accOpts{oauth: true} }

func oaStandIn() accOpts { return accOpts{oauth: true, standIn: true} }

// registerClient registers a client the way an MCP client without a metadata
// document does, and returns its client_id.
func registerClient(t *testing.T, a *acc, name string, uris ...string) string {
	t.Helper()
	if len(uris) == 0 {
		uris = []string{cbURI}
	}
	r := a.newClient("").do("POST", "/oauth/register", map[string]any{"client_name": name, "redirect_uris": uris, "token_endpoint_auth_method": "none"})
	if r.code != 200 && r.code != 201 { // 200: the same client again
		t.Fatalf("register: %d %s", r.code, r.raw)
	}
	return r.body["client_id"].(string)
}

type authParams struct {
	clientID, redirect, challenge, resource, scope, state, method, responseType string
}

func (p authParams) query() string {
	v := url.Values{}
	set := func(k, val string) {
		if val != "" {
			v.Set(k, val)
		}
	}
	set("client_id", p.clientID)
	set("redirect_uri", p.redirect)
	set("code_challenge", p.challenge)
	set("code_challenge_method", p.method)
	set("resource", p.resource)
	set("scope", p.scope)
	set("state", p.state)
	set("response_type", p.responseType)
	return v.Encode()
}

func params(cid, challenge string) authParams {
	return authParams{clientID: cid, redirect: cbURI, challenge: challenge, resource: mcpRes, state: "st4te", method: "S256", responseType: "code"}
}

func location(t *testing.T, r result) *url.URL {
	t.Helper()
	u, err := url.Parse(r.resp.Header.Get("Location"))
	if err != nil || r.code != 302 {
		t.Fatalf("not a redirect: %d %q %s", r.code, r.resp.Header.Get("Location"), r.raw)
	}
	return u
}

// startConsent runs GET /oauth/authorize as c and returns the consent request id.
func startConsent(t *testing.T, c *client, p authParams) string {
	t.Helper()
	u := location(t, c.do("GET", "/oauth/authorize?"+p.query(), nil))
	if u.Path != "/oauth/consent" || u.Query().Get("req") == "" {
		t.Fatalf("not the consent page: %s", u)
	}
	return u.Query().Get("req")
}

func decide(t *testing.T, c *client, req string, approve bool, access string, spaces ...string) result {
	t.Helper()
	return c.do("POST", "/api/oauth/consent", map[string]any{"req": req, "approve": approve, "spaces": spaces, "access": access})
}

// approve runs the whole consent and returns the code.
func approve(t *testing.T, c *client, p authParams, access string, spaces ...string) string {
	t.Helper()
	req := startConsent(t, c, p)
	r := decide(t, c, req, true, access, spaces...).want(t, 200, "")
	u, err := url.Parse(r.body["redirect"].(string))
	if err != nil || u.Query().Get("code") == "" {
		t.Fatalf("no code: %s", r.raw)
	}
	if u.Query().Get("iss") != oaIssue || u.Query().Get("state") != p.state {
		t.Fatalf("iss or state missing: %s", u)
	}
	return u.Query().Get("code")
}

func form(t *testing.T, a *acc, kv ...string) result {
	t.Helper()
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return a.newClient("").do("POST", "/oauth/token", v.Encode(), "Content-Type", "application/x-www-form-urlencoded")
}

func exchange(t *testing.T, a *acc, cid, code, verifier string, extra ...string) result {
	t.Helper()
	return form(t, a, append([]string{"grant_type", "authorization_code", "client_id", cid, "code", code, "redirect_uri", cbURI, "code_verifier", verifier, "resource", mcpRes}, extra...)...)
}

func refreshWith(t *testing.T, a *acc, cid, refresh string, extra ...string) result {
	t.Helper()
	return form(t, a, append([]string{"grant_type", "refresh_token", "client_id", cid, "refresh_token", refresh}, extra...)...)
}

type granted struct {
	cid, access, refresh string
	verifier             string
}

// grant goes through the whole flow for a person and returns the tokens.
func grant(t *testing.T, a *acc, c *client, access string, spaces ...string) granted {
	t.Helper()
	cid := registerClient(t, a, "Notes helper")
	v, ch := pkcePair("a")
	code := approve(t, c, params(cid, ch), access, spaces...)
	r := exchange(t, a, cid, code, v).want(t, 200, "")
	return granted{cid: cid, access: r.body["access_token"].(string), refresh: r.body["refresh_token"].(string), verifier: v}
}

func TestOAuthFlowFromStartToFinish(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		mine := tm.owner.personalSpace()
		cid := registerClient(t, a, "Notes helper")
		v, ch := pkcePair("a")
		p := params(cid, ch)

		// No session: the person is sent to sign in, and comes back here.
		anon := a.newClient("")
		u := location(t, anon.do("GET", "/oauth/authorize?"+p.query(), nil))
		back := u.Query().Get("return")
		if u.Path != "/login" || !strings.HasPrefix(back, "/oauth/authorize?") || !strings.Contains(back, "code_challenge="+ch) {
			t.Fatalf("login redirect: %s", u)
		}
		// With a session: the consent page, which the app reads.
		req := startConsent(t, tm.owner, p)
		info := tm.owner.do("GET", "/api/oauth/consent?req="+url.QueryEscape(req), nil).want(t, 200, "")
		cl := info.body["client"].(map[string]any)
		if cl["name"] != "Notes helper" || cl["id"] != cid || cl["host"] != "" || info.body["redirectHost"] != "localhost:7777" || info.body["punycode"] != false {
			t.Fatalf("%s", info.raw)
		}
		if got := info.body["scopes"].([]any); len(got) != 2 || got[0] != "read" || got[1] != "write" {
			t.Fatalf("%s", info.raw)
		}
		var personal, team bool
		for _, sp := range info.body["spaces"].([]any) {
			m := sp.(map[string]any)
			personal = personal || (m["id"] == mine && m["personal"] == true)
			team = team || (m["id"] == tm.sid && m["name"] == "Team" && len(m["permissions"].([]any)) == 4)
		}
		if !personal || !team || info.body["user"].(map[string]any)["id"] != tm.ownerID {
			t.Fatalf("%s", info.raw)
		}
		// Approve with one space and write.
		r := decide(t, tm.owner, req, true, "write", tm.sid).want(t, 200, "")
		cb, _ := url.Parse(r.body["redirect"].(string))
		if cb.Scheme != "http" || cb.Host != "localhost:7777" || cb.Path != "/callback" || cb.Query().Get("state") != "st4te" || cb.Query().Get("iss") != oaIssue {
			t.Fatalf("%s", cb)
		}
		tr := exchange(t, a, cid, cb.Query().Get("code"), v).want(t, 200, "")
		if tr.body["token_type"] != "Bearer" || tr.body["expires_in"] != float64(600) || tr.body["scope"] != "read write" || !strings.HasPrefix(tr.body["refresh_token"].(string), "pbr_") {
			t.Fatalf("%s", tr.raw)
		}
		if cc := tr.resp.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
			t.Fatalf("Cache-Control %q", cc)
		}
		// The grant is listed for its owner, with the spaces it names.
		l := tm.owner.do("GET", "/api/account/grants", nil).want(t, 200, "").body["grants"].([]any)
		if len(l) != 1 {
			t.Fatal(l)
		}
		g := l[0].(map[string]any)
		sp := g["spaces"].([]any)
		if g["access"] != "write" || g["client"].(map[string]any)["name"] != "Notes helper" || len(sp) != 1 || sp[0].(map[string]any)["id"] != tm.sid ||
			sp[0].(map[string]any)["name"] != "Team" || g["lastUsed"] == nil || g["created"] == nil {
			t.Fatalf("%v", g)
		}
		// Someone else sees none of it.
		if l := tm.editor.do("GET", "/api/account/grants", nil).want(t, 200, "").body["grants"].([]any); len(l) != 0 {
			t.Fatal(l)
		}
		// An access token is for /mcp: the API refuses it.
		b := a.bearer(tr.body["access_token"].(string), "")
		b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 403, "token_scope")
		b.do("GET", "/api/spaces", nil).want(t, 403, "token_scope")
	})
}

func TestAuthorizeRefusals(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		cid := registerClient(t, a, "Notes helper")
		_, ch := pkcePair("a")
		get := func(p authParams) result { return tm.owner.do("GET", "/oauth/authorize?"+p.query(), nil) }

		// These show a page and redirect nowhere.
		for name, p := range map[string]authParams{
			"unknown client":         params("pbc_nobody", ch),
			"no client":              params("", ch),
			"redirect not of client": func() authParams { p := params(cid, ch); p.redirect = "http://localhost:7777/elsewhere"; return p }(),
			"no redirect":            func() authParams { p := params(cid, ch); p.redirect = ""; return p }(),
			"foreign redirect":       func() authParams { p := params(cid, ch); p.redirect = "https://evil.example/cb"; return p }(),
		} {
			r := get(p)
			if r.code != 400 || r.resp.Header.Get("Location") != "" || !strings.Contains(r.resp.Header.Get("Content-Type"), "text/html") {
				t.Errorf("%s: %d %q", name, r.code, r.resp.Header.Get("Location"))
			}
		}
		// The loopback port may differ (RFC 8252); the path may not.
		ok := params(cid, ch)
		ok.redirect = "http://localhost:51515/callback"
		if r := get(ok); r.code != 302 {
			t.Errorf("another loopback port: %d %s", r.code, r.raw)
		}

		// Before consent, a bad request is shown on the page too and never goes
		// back to the client (RFC 9700 section 4.11.2: no open redirect).
		for name, c := range map[string]struct {
			mod  func(*authParams)
			code string
		}{
			"plain PKCE":       {func(p *authParams) { p.method = "plain" }, "invalid_request"},
			"no PKCE":          {func(p *authParams) { p.challenge, p.method = "", "" }, "invalid_request"},
			"no method":        {func(p *authParams) { p.method = "" }, "invalid_request"},
			"short challenge":  {func(p *authParams) { p.challenge = "abc" }, "invalid_request"},
			"no resource":      {func(p *authParams) { p.resource = "" }, "invalid_target"},
			"another resource": {func(p *authParams) { p.resource = "https://notes.example.test/api" }, "invalid_target"},
			"resource, slash":  {func(p *authParams) { p.resource = mcpRes + "/" }, "invalid_target"},
			"unknown scope":    {func(p *authParams) { p.scope = "read admin" }, "invalid_scope"},
			"token response":   {func(p *authParams) { p.responseType = "token" }, "unsupported_response_type"},
			"no response type": {func(p *authParams) { p.responseType = "" }, "unsupported_response_type"},
		} {
			p := params(cid, ch)
			c.mod(&p)
			r := get(p)
			if r.code != 400 || r.resp.Header.Get("Location") != "" || !strings.Contains(r.resp.Header.Get("Content-Type"), "text/html") || !strings.Contains(r.raw, c.code) {
				t.Errorf("%s: %d %q %s", name, r.code, r.resp.Header.Get("Location"), r.raw)
			}
		}
		// A scope of read only, or none at all, is fine.
		for _, sc := range []string{"read", "read write", ""} {
			p := params(cid, ch)
			p.scope = sc
			startConsent(t, tm.owner, p)
		}
		// A token cannot start one, and only the browser of a person has a session.
		_, secret := mkToken(t, tm.owner, "write", nil, tm.sid)
		r := a.bearer(secret, "").do("GET", "/oauth/authorize?"+params(cid, ch).query(), nil)
		if u := location(t, r); u.Path != "/login" {
			t.Fatalf("%s", u)
		}
	})
}

func TestConsentRefusals(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		cid := registerClient(t, a, "Notes helper")
		_, ch := pkcePair("a")
		readOnly := params(cid, ch)
		readOnly.scope = "read"

		req := startConsent(t, tm.owner, params(cid, ch))
		// Someone else's request, and one that never was.
		tm.editor.do("GET", "/api/oauth/consent?req="+req, nil).want(t, 404, "not_found")
		decide(t, tm.editor, req, true, "read", tm.sid).want(t, 404, "not_found")
		tm.owner.do("GET", "/api/oauth/consent?req=nope", nil).want(t, 404, "not_found")
		decide(t, tm.owner, "nope", true, "read", tm.sid).want(t, 404, "not_found")
		// A bad answer does not use the request up.
		decide(t, tm.owner, req, true, "read").want(t, 400, "invalid")                       // no space
		decide(t, tm.owner, req, true, "read", tm.sid, "01JD3K8Q7Z").want(t, 400, "invalid") // a space that is not theirs
		decide(t, tm.owner, req, true, "root", tm.sid).want(t, 400, "invalid")               // not an access
		decide(t, tm.outsider, req, true, "read", tm.sid).want(t, 404, "not_found")
		// A space of someone else's, that exists.
		other := tm.outsider.personalSpace()
		decide(t, tm.owner, req, true, "read", other).want(t, 400, "invalid")
		// The request is still there, and the good answer works once.
		tm.owner.do("GET", "/api/oauth/consent?req="+req, nil).want(t, 200, "")
		decide(t, tm.owner, req, true, "write", tm.sid).want(t, 200, "")
		decide(t, tm.owner, req, true, "write", tm.sid).want(t, 404, "not_found")
		tm.owner.do("GET", "/api/oauth/consent?req="+req, nil).want(t, 404, "not_found")

		// Write cannot be approved when only read was asked for.
		req = startConsent(t, tm.owner, readOnly)
		if sc := tm.owner.do("GET", "/api/oauth/consent?req="+req, nil).want(t, 200, "").body["scopes"].([]any); len(sc) != 1 || sc[0] != "read" {
			t.Fatal(sc)
		}
		decide(t, tm.owner, req, true, "write", tm.sid).want(t, 400, "invalid")
		decide(t, tm.owner, req, true, "", tm.sid).want(t, 200, "") // access defaults to read

		// Denying goes back to the client with access_denied.
		req = startConsent(t, tm.owner, params(cid, ch))
		r := decide(t, tm.owner, req, false, "").want(t, 200, "")
		u, _ := url.Parse(r.body["redirect"].(string))
		if u.Query().Get("error") != "access_denied" || u.Query().Get("code") != "" || u.Query().Get("iss") != oaIssue || u.Query().Get("state") != "st4te" {
			t.Fatalf("%s", u)
		}
		decide(t, tm.owner, req, true, "read", tm.sid).want(t, 404, "not_found")

		// Out of time.
		req = startConsent(t, tm.owner, params(cid, ch))
		a.clock.Advance(11 * time.Minute)
		decide(t, tm.owner, req, true, "read", tm.sid).want(t, 404, "not_found")

		// No session, a token: not for them.
		a.newClient("").do("GET", "/api/oauth/consent?req=x", nil).want(t, 401, "")
		_, secret := mkToken(t, tm.owner, "write", nil, tm.sid)
		a.bearer(secret, "").do("GET", "/api/oauth/consent?req=x", nil).want(t, 403, "token_scope")
		a.bearer(secret, "").do("POST", "/api/oauth/consent", map[string]any{"req": "x"}).want(t, 403, "token_scope")
		a.bearer(secret, "").do("GET", "/api/account/grants", nil).want(t, 403, "token_scope")
		// The decision is a request that changes something: JSON, from this site.
		tm.owner.do("POST", "/api/oauth/consent", "req=x", "Content-Type", "application/x-www-form-urlencoded").want(t, 415, "invalid")
		tm.owner.do("POST", "/api/oauth/consent", map[string]any{"req": "x"}, "Origin", "https://evil.example").want(t, 403, "forbidden")
		tm.owner.do("POST", "/api/oauth/consent", map[string]any{"req": "x"}, "Sec-Fetch-Site", "cross-site").want(t, 403, "forbidden")
	})
}

func TestTokenEndpointRefusals(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		cid := registerClient(t, a, "Notes helper")
		other := registerClient(t, a, "Another", "http://localhost:9/cb")
		newCode := func() (string, string) {
			v, ch := pkcePair("a")
			return approve(t, tm.owner, params(cid, ch), "write", tm.sid), v
		}
		code, v := newCode()
		bad := func(r result, err string) {
			t.Helper()
			if r.code != 400 || r.body["error"] != err {
				t.Fatalf("want %s, got %d %s", err, r.code, r.raw)
			}
			if r.body["error_description"] == "" {
				t.Fatal(r.raw)
			}
		}
		bad(exchange(t, a, cid, code, "wrong-verifier-wrong-verifier-wrong-verifier-wrong"), "invalid_grant")
		// The code was spent by that attempt, right or wrong.
		code, v = newCode()
		bad(exchange(t, a, cid, code, "short"), "invalid_grant")
		code, v = newCode()
		bad(form(t, a, "grant_type", "authorization_code", "client_id", cid, "code", code, "redirect_uri", "http://localhost:7777/other", "code_verifier", v), "invalid_grant")
		code, v = newCode()
		bad(exchange(t, a, other, code, v), "invalid_grant")
		code, v = newCode()
		bad(exchange(t, a, cid, code, v, "resource", "https://notes.example.test/api"), "invalid_target")
		code, v = newCode()
		bad(exchange(t, a, cid, code+"x", v), "invalid_grant")
		code, v = newCode()
		a.clock.Advance(61 * time.Second)
		bad(exchange(t, a, cid, code, v), "invalid_grant") // out of time
		// A client secret is not what this server takes.
		code, v = newCode()
		bad(exchange(t, a, cid, code, v, "client_secret", "s"), "invalid_client")
		// The resource may be left out of the request.
		code, v = newCode()
		r := form(t, a, "grant_type", "authorization_code", "client_id", cid, "code", code, "redirect_uri", cbURI, "code_verifier", v)
		r.want(t, 200, "")
		// Shape of the request.
		bad(form(t, a, "grant_type", "password", "client_id", cid), "unsupported_grant_type")
		bad(form(t, a, "client_id", cid), "unsupported_grant_type")
		bad(a.newClient("").do("POST", "/oauth/token", "{}"), "invalid_request")
		if r := a.newClient("").do("GET", "/oauth/token", nil); r.code != 405 || r.body["error"] != "invalid_request" {
			t.Fatal(r.raw)
		}
		bad(a.newClient("").do("POST", "/oauth/token", strings.Repeat("a", 17<<10), "Content-Type", "application/x-www-form-urlencoded"), "invalid_request")
		// Parameters are read from the body, not the address.
		bad(a.newClient("").do("POST", "/oauth/token?grant_type=refresh_token", "x=1", "Content-Type", "application/x-www-form-urlencoded"), "unsupported_grant_type")
	})
}

func TestACodeUsedTwiceEndsTheGrant(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		cid := registerClient(t, a, "Notes helper")
		v, ch := pkcePair("a")
		code := approve(t, tm.owner, params(cid, ch), "write", tm.sid)
		first := exchange(t, a, cid, code, v).want(t, 200, "")
		exchange(t, a, cid, code, v).want(t, 400, "invalid_grant")
		// The grant the first exchange made is gone, and so is its refresh token.
		refreshWith(t, a, cid, first.body["refresh_token"].(string)).want(t, 400, "invalid_grant")
		if l := tm.owner.do("GET", "/api/account/grants", nil).want(t, 200, "").body["grants"].([]any); len(l) != 0 {
			t.Fatal(l)
		}
	})
}

func TestRefreshTokensRotate(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		g := grant(t, a, tm.owner, "write", tm.sid)
		// Each refresh gives a new pair and replaces the refresh token.
		r1 := refreshWith(t, a, g.cid, g.refresh).want(t, 200, "")
		next := r1.body["refresh_token"].(string)
		if next == g.refresh || r1.body["access_token"] == g.access || r1.body["scope"] != "read write" || r1.body["expires_in"] != float64(600) {
			t.Fatalf("%s", r1.raw)
		}
		// The replaced one, within 60 seconds, gets the very same answer (a lost response).
		a.clock.Advance(30 * time.Second)
		again := refreshWith(t, a, g.cid, g.refresh).want(t, 200, "")
		if again.raw != r1.raw {
			t.Fatalf("not the same answer:\n%s\n%s", again.raw, r1.raw)
		}
		// The new one still works, and the grant is alive.
		r2 := refreshWith(t, a, g.cid, next).want(t, 200, "")
		// Past the grace, the replaced token ends the whole grant: the new one too.
		a.clock.Advance(40 * time.Second)
		refreshWith(t, a, g.cid, g.refresh).want(t, 400, "invalid_grant")
		refreshWith(t, a, g.cid, r2.body["refresh_token"].(string)).want(t, 400, "invalid_grant")
		if l := tm.owner.do("GET", "/api/account/grants", nil).want(t, 200, "").body["grants"].([]any); len(l) != 0 {
			t.Fatal(l)
		}
	})
}

func TestRefreshRefusals(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		g := grant(t, a, tm.owner, "write", tm.sid)
		other := registerClient(t, a, "Another", "http://localhost:9/cb")
		// None of these end the grant or use the token up.
		refreshWith(t, a, g.cid, "").want(t, 400, "invalid_grant")
		refreshWith(t, a, g.cid, "garbage").want(t, 400, "invalid_grant")
		refreshWith(t, a, g.cid, "pbr_nobody.secret").want(t, 400, "invalid_grant")
		refreshWith(t, a, g.cid, g.refresh+"x").want(t, 400, "invalid_grant")
		refreshWith(t, a, other, g.refresh).want(t, 400, "invalid_grant") // another client's
		refreshWith(t, a, g.cid, g.refresh, "resource", "https://notes.example.test/api").want(t, 400, "invalid_target")
		// A narrower scope is allowed, a wider one is not (this grant is read only below).
		ro := grant(t, a, tm.owner, "read", tm.sid)
		refreshWith(t, a, ro.cid, ro.refresh, "scope", "read write").want(t, 400, "invalid_scope")
		refreshWith(t, a, ro.cid, ro.refresh, "scope", "admin").want(t, 400, "invalid_scope")
		r := refreshWith(t, a, g.cid, g.refresh, "scope", "read").want(t, 200, "")
		if r.body["scope"] != "read" {
			t.Fatal(r.raw)
		}
		// The first token still worked after all those refusals.
		refreshWith(t, a, ro.cid, ro.refresh).want(t, 200, "")
	})
}

func TestAGrantNotRefreshedFor90DaysEnds(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		g := grant(t, a, tm.owner, "write", tm.sid)
		a.clock.Advance(89 * 24 * time.Hour)
		r := refreshWith(t, a, g.cid, g.refresh).want(t, 200, "")
		a.clock.Advance(89 * 24 * time.Hour) // idle is counted from the last refresh
		r = refreshWith(t, a, g.cid, r.body["refresh_token"].(string)).want(t, 200, "")
		a.clock.Advance(91 * 24 * time.Hour)
		// (The person's own session is long over; the list is what an
		// administrator would see.)
		tm.owner.do("POST", "/api/login", map[string]any{"username": "olive", "password": pw}).want(t, 200, "")
		if l := tm.owner.do("GET", "/api/account/grants", nil).want(t, 200, "").body["grants"].([]any); len(l) != 0 {
			t.Fatalf("an idle grant is still listed: %v", l)
		}
		refreshWith(t, a, g.cid, r.body["refresh_token"].(string)).want(t, 400, "invalid_grant")
	})
}

func TestConcurrentRefreshesGetOneAnswer(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		g := grant(t, a, tm.owner, "write", tm.sid)
		var wg sync.WaitGroup
		out := make([]string, 6)
		for i := range out {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := refreshWith(t, a, g.cid, g.refresh)
				out[i] = r.raw
				if r.code != 200 {
					t.Errorf("%d %s", r.code, r.raw)
				}
			}()
		}
		wg.Wait()
		for _, o := range out {
			if o != out[0] {
				t.Fatalf("different answers:\n%s\n%s", o, out[0])
			}
		}
		var next map[string]any
		_ = json.Unmarshal([]byte(out[0]), &next)
		refreshWith(t, a, g.cid, next["refresh_token"].(string)).want(t, 200, "")
	})
}

func TestRevokingAGrant(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oaStandIn(), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tm := newTeam(t, a)
		g := grant(t, a, tm.owner, "write", tm.sid)
		b := a.bearer(g.access, "")
		b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 200, "")
		list := tm.owner.do("GET", "/api/account/grants", nil).want(t, 200, "").body["grants"].([]any)
		id := list[0].(map[string]any)["id"].(string)

		// Not someone else's.
		tm.editor.do("DELETE", "/api/account/grants/"+id, nil).want(t, 404, "not_found")
		tm.owner.do("DELETE", "/api/account/grants/nope", nil).want(t, 404, "not_found")
		b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 200, "")
		// The administrator sees it, with its owner.
		items := admin.do("GET", "/api/admin/grants", nil).want(t, 200, "").body["items"].([]any)
		if len(items) != 1 || items[0].(map[string]any)["owner"].(map[string]any)["username"] != "olive" || items[0].(map[string]any)["id"] != id {
			t.Fatalf("%v", items)
		}
		tm.owner.do("GET", "/api/admin/grants", nil).want(t, 403, "forbidden")
		tm.owner.do("DELETE", "/api/admin/grants/"+id, nil).want(t, 403, "forbidden")
		if items := admin.do("GET", "/api/admin/grants?user="+tm.editorID, nil).want(t, 200, "").body["items"].([]any); len(items) != 0 {
			t.Fatal(items)
		}

		// The owner revokes: the refresh token and the access token stop at once.
		tm.owner.do("DELETE", "/api/account/grants/"+id, nil).want(t, 204, "")
		refreshWith(t, a, g.cid, g.refresh).want(t, 400, "invalid_grant")
		r := b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 401, "token_revoked")
		if got := r.resp.Header.Get("WWW-Authenticate"); !strings.Contains(got, "invalid_token") {
			t.Fatalf("%q", got)
		}
		tm.owner.do("DELETE", "/api/account/grants/"+id, nil).want(t, 404, "not_found")

		// An administrator revokes someone's.
		g2 := grant(t, a, tm.editor, "read", tm.sid)
		items = admin.do("GET", "/api/admin/grants", nil).want(t, 200, "").body["items"].([]any)
		if len(items) != 1 {
			t.Fatal(items)
		}
		admin.do("DELETE", "/api/admin/grants/"+items[0].(map[string]any)["id"].(string), nil).want(t, 204, "")
		admin.do("DELETE", "/api/admin/grants/"+items[0].(map[string]any)["id"].(string), nil).want(t, 404, "not_found")
		a.bearer(g2.access, "").do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 401, "token_revoked")
		refreshWith(t, a, g2.cid, g2.refresh).want(t, 400, "invalid_grant")
	})
}

func TestAdminGrantListPages(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tm := newTeam(t, a)
		for i := 0; i < 3; i++ {
			grant(t, a, tm.owner, "read", tm.sid)
			a.clock.Advance(time.Second)
		}
		p1 := admin.do("GET", "/api/admin/grants?limit=2", nil).want(t, 200, "")
		if len(p1.body["items"].([]any)) != 2 || p1.body["next"] == "" {
			t.Fatal(p1.raw)
		}
		p2 := admin.do("GET", "/api/admin/grants?limit=2&cursor="+url.QueryEscape(p1.body["next"].(string)), nil).want(t, 200, "")
		if len(p2.body["items"].([]any)) != 1 || p2.body["next"] != "" {
			t.Fatal(p2.raw)
		}
		admin.do("GET", "/api/admin/grants?limit=0", nil).want(t, 400, "invalid")
		admin.do("GET", "/api/admin/grants?cursor=zzz", nil).want(t, 400, "invalid")
	})
}

// What a grant may do is what an API token with the same access and spaces may:
// the owner's role now, narrowed by the grant, never more.
func TestAGrantIsNeverMoreThanItsOwner(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oaStandIn(), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tm := newTeam(t, a)
		mine := tm.owner.personalSpace()
		p := newPage(t, tm.editor, tm.sid, "Seed")
		page := spacePath(tm.sid) + "/pages/" + p["id"].(string)
		save := map[string]any{"baseRev": p["rev"], "meta": map[string]any{"title": "Seed"}, "body": "by an app\n"}

		rd := a.bearer(grant(t, a, tm.editor, "read", tm.sid).access, "")
		wr := a.bearer(grant(t, a, tm.editor, "write", tm.sid).access, "")
		vw := a.bearer(grant(t, a, tm.viewer, "write", tm.sid).access, "")
		ow := a.bearer(grant(t, a, tm.owner, "write", tm.sid).access, "")

		rd.do("GET", page, nil).want(t, 200, "")
		rd.do("PUT", page, save).want(t, 403, "token_scope")
		rd.do("POST", spacePath(tm.sid)+"/favorites", map[string]any{"page": p["id"]}).want(t, 403, "token_scope")
		vw.do("PUT", page, save).want(t, 403, "no_permission") // the owner is a viewer
		wr.do("PUT", page, save).want(t, 200, "")
		// Never members or manage, and no drafts, whatever the owner is.
		ow.do("POST", spacePath(tm.sid)+"/members", map[string]any{"username": "oscar", "role": "viewer"}).want(t, 403, "token_scope")
		ow.do("PATCH", spacePath(tm.sid), map[string]any{"name": "Mine"}).want(t, 403, "token_scope")
		ow.do("POST", spacePath(tm.sid)+"/reindex", nil).want(t, 403, "token_scope")
		ow.do("PUT", page+"/draft", save).want(t, 403, "token_scope")
		// Not the owner's other spaces, and only the grant's are listed.
		ow.do("GET", spacePath(mine)+"/tree", nil).want(t, 403, "token_scope")
		l := ow.do("GET", "/api/spaces", nil).want(t, 200, "").body["spaces"].([]any)
		if len(l) != 1 || l[0].(map[string]any)["id"] != tm.sid || len(l[0].(map[string]any)["permissions"].([]any)) != 2 {
			t.Fatalf("%v", l)
		}
		// Nothing outside the spaces: the account, the administration, signing in.
		for _, path := range []string{"/api/account", "/api/account/tokens", "/api/account/grants", "/api/users", "/api/admin/grants", "/api/oauth/consent?req=x"} {
			ow.do("GET", path, nil).want(t, 403, "token_scope")
		}
		ow.do("POST", "/api/logout", nil).want(t, 403, "token_scope")

		// Demoted: the next request is a viewer's.
		tm.owner.do("PATCH", spacePath(tm.sid)+"/members/"+tm.editorID, map[string]any{"role": "viewer"}).want(t, 200, "")
		wr.do("GET", page, nil).want(t, 200, "")
		wr.do("PUT", page, save).want(t, 403, "no_permission")
		// Removed: the space is gone for it.
		tm.owner.do("DELETE", spacePath(tm.sid)+"/members/"+tm.editorID, nil).want(t, 204, "")
		wr.do("GET", page, nil).want(t, 404, "not_found")
		// Back in, it works again.
		tm.owner.do("POST", spacePath(tm.sid)+"/members", map[string]any{"username": "edith", "role": "editor"}).want(t, 201, "")
		wr.do("GET", page, nil).want(t, 200, "")
		wr.do("PUT", page, map[string]any{"baseRev": p["rev"], "meta": map[string]any{"title": "Seed"}, "body": "again\n"}).want(t, 409, "") // the rev moved: a write, not a refusal

		// An administrator's grant has no way into spaces they are not a member of.
		ag := a.bearer(grant(t, a, admin, "write", admin.personalSpace()).access, "")
		admin.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 200, "")
		ag.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 403, "token_scope")
		ag.do("GET", "/api/admin/status", nil).want(t, 403, "token_scope")
	})
}

func TestAGrantFollowsItsOwnersAccount(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oaStandIn(), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tm := newTeam(t, a)
		g := grant(t, a, tm.editor, "write", tm.sid)
		b := a.bearer(g.access, "")
		b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 200, "")
		admin.do("PATCH", "/api/users/"+tm.editorID, map[string]any{"disabled": true}).want(t, 200, "")
		b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 401, "token_owner_disabled")
		// Neither a refresh nor a code makes a token for an account that is off.
		refreshWith(t, a, g.cid, g.refresh).want(t, 400, "invalid_grant")
		// The grant is kept: when the account is back, the app works again.
		admin.do("PATCH", "/api/users/"+tm.editorID, map[string]any{"disabled": false}).want(t, 200, "")
		b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 200, "")
		refreshWith(t, a, g.cid, g.refresh).want(t, 200, "")

		// A person who is disabled cannot approve a code either (they have no session).
		v, ch := pkcePair("b")
		cid := registerClient(t, a, "Late")
		code := approve(t, tm.viewer, params(cid, ch), "read", tm.sid)
		admin.do("PATCH", "/api/users/"+tm.viewerID, map[string]any{"disabled": true}).want(t, 200, "")
		exchange(t, a, cid, code, v).want(t, 400, "invalid_grant")
	})
}

func TestMachineAccessSwitchTurnsOAuthOff(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oaStandIn(), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		tm := newTeam(t, a)
		g := grant(t, a, tm.owner, "write", tm.sid)
		anon := a.newClient("")
		b := a.bearer(g.access, "")
		b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 200, "")
		if got := admin.do("GET", "/api/admin/access", nil).want(t, 200, "").body; got["oauth"] != true || got["mcp"] != true || got["tokens"] != true {
			t.Fatal(got)
		}
		admin.do("PUT", "/api/admin/access", map[string]any{"mcp": false}).want(t, 200, "")
		if got := admin.do("GET", "/api/admin/access", nil).want(t, 200, "").body; got["oauth"] != false || got["mcp"] != false || got["tokens"] != true {
			t.Fatal(got)
		}
		// Everything of the authorization server is 404, in the OAuth shape.
		for _, path := range []string{"/.well-known/oauth-authorization-server", "/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
			anon.do("GET", path, nil).want(t, 404, "not_found")
		}
		_, ch := pkcePair("a")
		tm.owner.do("GET", "/oauth/authorize?"+params(g.cid, ch).query(), nil).want(t, 404, "not_found")
		form(t, a, "grant_type", "refresh_token", "client_id", g.cid, "refresh_token", g.refresh).want(t, 404, "not_found")
		anon.do("POST", "/oauth/register", map[string]any{"client_name": "x", "redirect_uris": []string{cbURI}}).want(t, 404, "not_found")
		anon.do("OPTIONS", "/oauth/token", nil).want(t, 404, "not_found")
		tm.owner.do("GET", "/api/oauth/consent?req=x", nil).want(t, 404, "not_found")
		tm.owner.do("POST", "/api/oauth/consent", map[string]any{"req": "x"}).want(t, 404, "not_found")
		// An access token is no use, and API tokens are a switch of their own.
		b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 404, "not_found")
		_, secret := mkToken(t, tm.owner, "write", nil, tm.sid)
		a.bearer(secret, "").do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 200, "")
		// The grants are kept, and listed, and can be revoked while it is off.
		l := tm.owner.do("GET", "/api/account/grants", nil).want(t, 200, "").body["grants"].([]any)
		if len(l) != 1 {
			t.Fatal(l)
		}
		// Back on: the grant works as before.
		admin.do("PUT", "/api/admin/access", map[string]any{"mcp": true}).want(t, 200, "")
		b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 200, "")
		refreshWith(t, a, g.cid, g.refresh).want(t, 200, "")
		tm.owner.do("DELETE", "/api/account/grants/"+l[0].(map[string]any)["id"].(string), nil).want(t, 204, "")
	})
}

func TestNoAuthorizationServerWithoutIt(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		anon := a.newClient("")
		for _, path := range []string{"/.well-known/oauth-authorization-server", "/.well-known/oauth-protected-resource/mcp", "/oauth/authorize", "/oauth/token"} {
			if r := anon.do("GET", path, nil); r.code != 404 {
				t.Errorf("%s: %d %s", path, r.code, r.raw)
			}
		}
		admin.do("GET", "/api/account/grants", nil).want(t, 404, "not_found")
		admin.do("GET", "/api/admin/grants", nil).want(t, 404, "not_found")
		admin.do("GET", "/api/oauth/consent?req=x", nil).want(t, 404, "not_found")
		if got := admin.do("GET", "/api/admin/access", nil).want(t, 200, "").body; got["oauth"] != false || got["mcp"] != true {
			t.Fatal(got)
		}
	})
}

func TestDiscoveryDocuments(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		anon := a.newClient("")
		for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
			r := anon.do("GET", path, nil).want(t, 200, "")
			if r.body["resource"] != mcpRes || r.body["authorization_servers"].([]any)[0] != oaIssue || r.body["bearer_methods_supported"].([]any)[0] != "header" ||
				len(r.body["scopes_supported"].([]any)) != 2 {
				t.Fatalf("%s: %s", path, r.raw)
			}
			if r.resp.Header.Get("Cache-Control") != "public, max-age=300" || r.resp.Header.Get("Access-Control-Allow-Origin") != "*" {
				t.Fatalf("%v", r.resp.Header)
			}
		}
		m := anon.do("GET", "/.well-known/oauth-authorization-server", nil).want(t, 200, "").body
		want := map[string]any{"issuer": oaIssue, "authorization_endpoint": oaIssue + "/oauth/authorize", "token_endpoint": oaIssue + "/oauth/token",
			"registration_endpoint": oaIssue + "/oauth/register", "client_id_metadata_document_supported": true, "authorization_response_iss_parameter_supported": true}
		for k, v := range want {
			if m[k] != v {
				t.Errorf("%s: %v", k, m[k])
			}
		}
		if m["code_challenge_methods_supported"].([]any)[0] != "S256" || len(m["code_challenge_methods_supported"].([]any)) != 1 ||
			m["token_endpoint_auth_methods_supported"].([]any)[0] != "none" || len(m["grant_types_supported"].([]any)) != 2 || m["jwks_uri"] != nil {
			t.Fatalf("%v", m)
		}
		if got := a.oauth.Challenge(); got != `Bearer resource_metadata="`+oaIssue+`/.well-known/oauth-protected-resource/mcp", scope="read write"` {
			t.Fatal(got)
		}
	})
}

// The endpoints of the authorization server are called from pages of other
// sites by design, and the rest of the API still is not.
func TestOAuthCrossSiteRules(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		anon := a.newClient("")
		site := []string{"Origin", "https://app.example", "Sec-Fetch-Site", "cross-site"}
		r := anon.do("OPTIONS", "/oauth/token", nil, site...)
		if r.code != 204 || r.resp.Header.Get("Access-Control-Allow-Origin") != "*" || !strings.Contains(r.resp.Header.Get("Access-Control-Allow-Methods"), "POST") {
			t.Fatalf("%d %v", r.code, r.resp.Header)
		}
		for _, path := range []string{"/.well-known/oauth-authorization-server", "/.well-known/oauth-protected-resource/mcp"} {
			anon.do("GET", path, nil, site...).want(t, 200, "")
		}
		anon.do("POST", "/oauth/register", map[string]any{"client_name": "Web", "redirect_uris": []string{"https://app.example/cb"}}, site...).want(t, 201, "")
		r = anon.do("POST", "/oauth/token", "grant_type=nope", append([]string{"Content-Type", "application/x-www-form-urlencoded"}, site...)...)
		if r.code != 400 || r.body["error"] != "unsupported_grant_type" || r.resp.Header.Get("Access-Control-Allow-Origin") != "*" {
			t.Fatalf("%d %s", r.code, r.raw)
		}
		// A link from another site may open the authorization page; it only asks.
		cid := registerClient(t, a, "Notes helper")
		_, ch := pkcePair("a")
		if u := location(t, tm.owner.do("GET", "/oauth/authorize?"+params(cid, ch).query(), nil, "Sec-Fetch-Site", "cross-site")); u.Path != "/oauth/consent" {
			t.Fatalf("%s", u)
		}
		// But nothing of the API is.
		tm.owner.do("GET", "/api/spaces", nil, site...).want(t, 403, "forbidden")
		tm.owner.do("GET", "/api/account/grants", nil, site...).want(t, 403, "forbidden")
		anon.do("POST", "/oauth/authorize", map[string]any{}).want(t, 404, "not_found")
		// The token endpoint does not take JSON for a form, and leaves no cookie on its answer.
		anon.do("POST", "/oauth/token", map[string]any{"grant_type": "refresh_token"}).want(t, 400, "invalid_request")
	})
}

func TestDynamicRegistration(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		anon := a.newClient("203.0.113.9")
		reg := func(body map[string]any) result { return anon.do("POST", "/oauth/register", body) }
		r := reg(map[string]any{"client_name": "  Claude‮ Code ", "redirect_uris": []string{cbURI}, "token_endpoint_auth_method": "none",
			"grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"}}).want(t, 201, "")
		id := r.body["client_id"].(string)
		if !strings.HasPrefix(id, "pbc_") || r.body["client_name"] != "Claude Code" || r.body["token_endpoint_auth_method"] != "none" || r.body["redirect_uris"].([]any)[0] != cbURI {
			t.Fatalf("%s", r.raw)
		}
		// The same details again are the same client, and cost nothing.
		for i := 0; i < 40; i++ {
			r2 := reg(map[string]any{"client_name": "Claude Code", "redirect_uris": []string{cbURI}}).want(t, 200, "")
			if r2.body["client_id"] != id {
				t.Fatal(r2.raw)
			}
		}
		for name, body := range map[string]map[string]any{
			"no uris":         {"client_name": "x"},
			"too many":        {"redirect_uris": []string{"https://a.example/1", "https://a.example/2", "https://a.example/3", "https://a.example/4", "https://a.example/5", "https://a.example/6", "https://a.example/7", "https://a.example/8", "https://a.example/9", "https://a.example/10", "https://a.example/11"}},
			"http, not local": {"redirect_uris": []string{"http://app.example/cb"}},
			"javascript":      {"redirect_uris": []string{"javascript:alert(1)"}},
			"fragment":        {"redirect_uris": []string{"https://app.example/cb#x"}},
			"look-alike":      {"redirect_uris": []string{"https://аpp.example/cb"}},
		} {
			if r := reg(body); r.code != 400 || r.body["error"] != "invalid_redirect_uri" {
				t.Errorf("%s: %d %s", name, r.code, r.raw)
			}
		}
		for name, body := range map[string]map[string]any{
			"confidential": {"redirect_uris": []string{cbURI}, "token_endpoint_auth_method": "client_secret_basic"},
			"implicit":     {"redirect_uris": []string{cbURI}, "grant_types": []string{"implicit"}},
			"token":        {"redirect_uris": []string{cbURI}, "response_types": []string{"token"}},
		} {
			if r := reg(body); r.code != 400 || r.body["error"] != "invalid_client_metadata" {
				t.Errorf("%s: %d %s", name, r.code, r.raw)
			}
		}
		anon.do("POST", "/oauth/register", "[1]").want(t, 400, "invalid_client_metadata")
		if r := anon.do("GET", "/oauth/register", nil); r.code != 405 {
			t.Fatal(r.raw)
		}
		// 30 new registrations in 10 minutes from one address; another address is its own.
		var limited result
		for i := 0; i < 32; i++ {
			limited = reg(map[string]any{"client_name": "n" + strings.Repeat("x", i), "redirect_uris": []string{cbURI}})
			if limited.code == 429 {
				break
			}
		}
		if limited.code != 429 || limited.body["error"] != "temporarily_unavailable" || limited.resp.Header.Get("Retry-After") == "" {
			t.Fatalf("%d %s", limited.code, limited.raw)
		}
		a.newClient("203.0.113.10").do("POST", "/oauth/register", map[string]any{"client_name": "other", "redirect_uris": []string{cbURI}}).want(t, 201, "")
		a.clock.Advance(11 * time.Minute)
		reg(map[string]any{"client_name": "later", "redirect_uris": []string{cbURI}}).want(t, 201, "")
	})
}

func TestMetadataDocumentClient(t *testing.T) {
	t.Parallel()
	doc := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"client_id": "https://" + r.Host + r.URL.Path, "client_name": "Doc Client",
			"redirect_uris": []string{cbURI}, "token_endpoint_auth_method": "none"})
	}))
	t.Cleanup(doc.Close)
	o := oa()
	o.cimd = doc.Client()
	eachAccounts(t, o, func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		id := doc.URL + "/client.json"
		v, ch := pkcePair("a")
		p := params(id, ch)
		req := startConsent(t, tm.owner, p)
		info := tm.owner.do("GET", "/api/oauth/consent?req="+req, nil).want(t, 200, "").body["client"].(map[string]any)
		if info["name"] != "Doc Client" || info["id"] != id || !strings.HasPrefix(info["host"].(string), "127.0.0.1") {
			t.Fatalf("%v", info)
		}
		code := approve(t, tm.owner, p, "read", tm.sid)
		tr := exchange(t, a, id, code, v).want(t, 200, "")
		// Nothing was registered for it, and the grant names it.
		g := tm.owner.do("GET", "/api/account/grants", nil).want(t, 200, "").body["grants"].([]any)[0].(map[string]any)
		if g["client"].(map[string]any)["id"] != id {
			t.Fatalf("%v", g)
		}
		refreshWith(t, a, id, tr.body["refresh_token"].(string)).want(t, 200, "")
		// A redirect the document does not list: a page, no redirect.
		bad := params(id, ch)
		bad.redirect = "http://localhost:7777/elsewhere"
		if r := tm.owner.do("GET", "/oauth/authorize?"+bad.query(), nil); r.code != 400 || r.resp.Header.Get("Location") != "" {
			t.Fatalf("%d", r.code)
		}
		// A document that cannot be had is the person's page, with the reason.
		gone := params("https://127.0.0.1:1/client.json", ch)
		if r := tm.owner.do("GET", "/oauth/authorize?"+gone.query(), nil); r.code != 400 || !strings.Contains(r.raw, "metadata document") {
			t.Fatalf("%d %s", r.code, r.raw)
		}
	})
}

func TestTheErrorPageSpeaksTheBrowsersLanguage(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		_, ch := pkcePair("a")
		q := "/oauth/authorize?" + params("pbc_nobody", ch).query()
		en := tm.owner.do("GET", q, nil, "Accept-Language", "en-GB,en;q=0.9")
		zh := tm.owner.do("GET", q, nil, "Accept-Language", "zh-CN,zh;q=0.9")
		if !strings.Contains(en.raw, `<html lang="en">`) || !strings.Contains(en.raw, "not registered") || !strings.Contains(zh.raw, `<html lang="zh-CN">`) || !strings.Contains(zh.raw, "授权失败") {
			t.Fatalf("%s\n%s", en.raw, zh.raw)
		}
		if en.resp.Header.Get("Cache-Control") != "no-store" || !strings.Contains(en.resp.Header.Get("Vary"), "Accept-Language") {
			t.Fatalf("%v", en.resp.Header)
		}
		// What the client sent is never put on the page as it came.
		p := params("pbc_<script>", ch)
		if r := tm.owner.do("GET", "/oauth/authorize?"+p.query(), nil); strings.Contains(r.raw, "<script>") {
			t.Fatal(r.raw)
		}
	})
}

// A check of an access token costs no query: the signature is checked once, and
// the grant is read from memory for 30 seconds.
func TestAnAccessTokenIsCheckedInMemory(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oaStandIn(), func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		g := grant(t, a, tm.owner, "write", tm.sid)
		b := a.bearer(g.access, "")
		b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 200, "")
		// The row goes away behind the server's back, as another process would do it.
		if _, err := a.st.Q().Exec(context.Background(), `DELETE FROM oauth_grants`); err != nil {
			t.Fatal(err)
		}
		a.clock.Advance(20 * time.Second)
		b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 200, "") // from memory
		a.clock.Advance(15 * time.Second)
		b.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 401, "token_revoked") // read again
		// An access token lives ten minutes.
		g2 := grant(t, a, tm.owner, "write", tm.sid)
		b2 := a.bearer(g2.access, "")
		b2.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 200, "")
		a.clock.Advance(9 * time.Minute)
		b2.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 200, "")
		a.clock.Advance(2 * time.Minute)
		b2.do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 401, "token_expired")
	})
}

func TestAccessTokenForgeries(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oaStandIn(), func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		g := grant(t, a, tm.owner, "write", tm.sid)
		parts := strings.Split(g.access, ".")
		forged := func(claims map[string]any) string {
			hdr := parts[0]
			c, _ := json.Marshal(claims)
			return hdr + "." + base64.RawURLEncoding.EncodeToString(c) + "." + parts[2]
		}
		var claims map[string]any
		raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
		_ = json.Unmarshal(raw, &claims)
		claims["sub"] = tm.editorID // another person's
		for name, tok := range map[string]string{
			"payload changed": forged(claims),
			"no signature":    parts[0] + "." + parts[1] + ".",
			"alg none":        base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"at+jwt"}`)) + "." + parts[1] + ".",
			"garbage":         "a.b.c",
			"truncated":       g.access[:len(g.access)-4],
		} {
			if r := a.bearer(tok, "").do("GET", spacePath(tm.sid)+"/tree", nil); r.code != 401 || r.body["error"] != "token_invalid" {
				t.Errorf("%s: %d %s", name, r.code, r.raw)
			}
		}
		// Ten refused tokens in a minute from one address are enough.
		for i := 0; i < 12; i++ {
			a.bearer("a.b.c", "198.51.100.50").do("GET", spacePath(tm.sid)+"/tree", nil)
		}
		a.bearer(g.access, "198.51.100.50").do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 429, "rate_limited")
		a.bearer(g.access, "198.51.100.51").do("GET", spacePath(tm.sid)+"/tree", nil).want(t, 200, "")
	})
}

func TestTheSigningKeyIsSealedAndShared(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oaStandIn(), func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		g := grant(t, a, tm.owner, "write", tm.sid)
		var sealed string
		if err := a.st.Q().QueryRow(context.Background(), `SELECT sealed FROM oauth_keys`).Scan(&sealed); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(sealed, "PRIVATE") || strings.Contains(sealed, "MIG") || len(sealed) < 100 {
			t.Fatalf("the key is not sealed: %q", sealed)
		}
		// A server started on the same database uses the same key: tokens survive a restart.
		again, err := oauth.New(context.Background(), oauth.Config{Store: a.st, Secrets: a.mgr.Secrets(), Users: a.auth, Machine: a.tokens, Issuer: oaIssue,
			Now: a.clock.Now})
		if err != nil {
			t.Fatal(err)
		}
		p, err := again.Authenticate(context.Background(), g.access, "198.51.100.7")
		if err != nil || p.User.ID != tm.ownerID || p.Grant == nil || p.Grant.Kind != oauth.GrantKind || !p.Grant.Write || !p.Grant.Names(tm.sid) {
			t.Fatalf("%v %+v", err, p)
		}
		// And refreshes: the refresh token is in the database, not in memory.
		refreshWith(t, a, g.cid, g.refresh).want(t, 200, "")
		var n int
		if err := a.st.Q().QueryRow(context.Background(), `SELECT COUNT(*) FROM oauth_keys`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%d %v", n, err)
		}
		// With the server key lost, the signing key cannot be opened, and the
		// server says so instead of making another that would void every grant.
		if _, err := oauth.New(context.Background(), oauth.Config{Store: a.st, Secrets: secret.NewBoxWithKey([32]byte{7}), Users: a.auth, Machine: a.tokens, Issuer: oaIssue}); err == nil {
			t.Fatal("started with another server key")
		}
	})
}

func TestSweepEndsIdleGrantsAndUnusedClients(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		g := grant(t, a, tm.owner, "write", tm.sid)
		unused := registerClient(t, a, "Never used", "http://localhost:3/cb")
		a.clock.Advance(25 * time.Hour)
		a.oauth.Sweep(context.Background())
		var n int
		a.st.Q().QueryRow(context.Background(), `SELECT COUNT(*) FROM oauth_clients WHERE id = ?`, unused).Scan(&n)
		if n != 0 {
			t.Fatal("a client nobody authorized stayed a day")
		}
		a.st.Q().QueryRow(context.Background(), `SELECT COUNT(*) FROM oauth_clients WHERE id = ?`, g.cid).Scan(&n)
		if n != 1 {
			t.Fatal("a client with a grant went")
		}
		a.clock.Advance(91 * 24 * time.Hour)
		a.oauth.Sweep(context.Background())
		a.st.Q().QueryRow(context.Background(), `SELECT COUNT(*) FROM oauth_grants`).Scan(&n)
		if n != 0 {
			t.Fatal("an idle grant stayed")
		}
		a.clock.Advance(31 * 24 * time.Hour)
		a.oauth.Sweep(context.Background())
		a.st.Q().QueryRow(context.Background(), `SELECT COUNT(*) FROM oauth_clients`).Scan(&n)
		if n != 0 {
			t.Fatal("a client with no grant stayed 30 days")
		}
	})
}

func TestGrantsGoWithTheirOwner(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		tm := newTeam(t, a)
		grant(t, a, tm.editor, "read", tm.sid)
		if _, err := a.st.Q().Exec(context.Background(), `DELETE FROM users WHERE id = ?`, tm.editorID); err != nil {
			t.Fatal(err)
		}
		var n int
		a.st.Q().QueryRow(context.Background(), `SELECT COUNT(*) FROM oauth_grants`).Scan(&n)
		a.st.Q().QueryRow(context.Background(), `SELECT COUNT(*) FROM oauth_grant_spaces`).Scan(new(int))
		if n != 0 {
			t.Fatalf("%d grants of a deleted user", n)
		}
	})
}

// docs/api.md promises the OAuth error shape on these endpoints, also for the
// refusals of the API's own checks in front of them.
func TestOAuthEndpointsAnswerInTheOAuthShape(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		anon := a.newClient("")
		body := `{"client_name":"x","redirect_uris":["` + cbURI + `"]}`
		for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded"} {
			r := anon.do("POST", "/oauth/register", body, "Content-Type", ct)
			if r.code != 400 || r.body["error"] != "invalid_client_metadata" || r.body["error_description"] == nil || r.body["message"] != nil {
				t.Errorf("register with %q: %d %s", ct, r.code, r.raw)
			}
		}
	})
}
