package api_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCrossSiteRulesInAccountsMode(t *testing.T) {
	eachAccounts(t, accOpts{public: "https://Notes.Example.test"}, func(t *testing.T, a *acc) {
		c := a.setup("kazuha", pw)
		sid := c.personalSpace()
		own := "https://notes.example.test"
		for _, tc := range []struct {
			name    string
			method  string
			path    string
			headers []string
			want    int
		}{
			{"own origin", "POST", "/api/spaces/" + sid + "/pages", []string{"Origin", own}, 201},
			{"own origin, upper case", "POST", "/api/spaces/" + sid + "/pages", []string{"Origin", "https://NOTES.example.test"}, 201},
			{"same-origin fetch", "POST", "/api/spaces/" + sid + "/pages", []string{"Sec-Fetch-Site", "same-origin", "Origin", own}, 201},
			{"navigation", "GET", "/api/session", []string{"Sec-Fetch-Site", "none"}, 200},
			{"no headers (curl)", "POST", "/api/spaces/" + sid + "/pages", nil, 201},
			{"foreign origin", "POST", "/api/spaces/" + sid + "/pages", []string{"Origin", "https://evil.example"}, 403},
			{"http version of our origin", "POST", "/api/spaces/" + sid + "/pages", []string{"Origin", "http://notes.example.test"}, 403},
			{"other port", "POST", "/api/spaces/" + sid + "/pages", []string{"Origin", "https://notes.example.test:8443"}, 403},
			{"subdomain", "POST", "/api/spaces/" + sid + "/pages", []string{"Origin", "https://evil.notes.example.test"}, 403},
			{"null origin", "POST", "/api/spaces/" + sid + "/pages", []string{"Origin", "null"}, 403},
			{"cross-site fetch metadata", "POST", "/api/spaces/" + sid + "/pages", []string{"Sec-Fetch-Site", "cross-site"}, 403},
			{"same-site fetch metadata", "POST", "/api/spaces/" + sid + "/pages", []string{"Sec-Fetch-Site", "same-site"}, 403},
			{"cross-site read", "GET", "/api/session", []string{"Sec-Fetch-Site", "cross-site"}, 403},
			{"foreign origin read", "GET", "/api/spaces", []string{"Origin", "https://evil.example"}, 403},
			{"cross-site login", "POST", "/api/login", []string{"Origin", "https://evil.example"}, 403},
			{"no content type", "POST", "/api/spaces/" + sid + "/pages", []string{"Content-Type", ""}, 415},
		} {
			r := c.do(tc.method, tc.path, "{}", tc.headers...)
			if r.code != tc.want {
				t.Errorf("%s: %d %s", tc.name, r.code, r.raw)
			}
			if tc.want == 403 && r.body["error"] != "forbidden" {
				t.Errorf("%s: %s", tc.name, r.raw)
			}
		}
		// A refused request did nothing: only the four accepted creates above.
		if items := c.do("GET", "/api/spaces/"+sid+"/pages", nil).body["items"].([]any); len(items) != 4 {
			t.Fatalf("%d pages", len(items))
		}
	})
}

func TestOIDCCallbackIsExemptFromTheCrossSiteRules(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		c := a.newClient("")
		// The provider's redirect is cross-site by nature: it must get through to
		// the handler (here no provider is configured, so the handler refuses).
		r := c.do("GET", "/api/auth/oidc/callback?state=x&code=y", nil, "Sec-Fetch-Site", "cross-site", "Origin", "https://idp.example")
		if r.code != 303 || r.resp.Header.Get("Location") != "/login?error=idp_error" {
			t.Fatalf("%d %s %s", r.code, r.resp.Header.Get("Location"), r.raw)
		}
		// Only that one route is exempt.
		c.do("GET", "/api/session", nil, "Sec-Fetch-Site", "cross-site").want(t, 403, "forbidden")
		c.do("POST", "/api/auth/oidc/callback", "{}", "Sec-Fetch-Site", "cross-site").want(t, 403, "forbidden")
		c.do("GET", "/api/auth/oidc/callback", nil, "Origin", "https://idp.example").want(t, 303, "")
	})
}

func TestWrongMethodAndUnknownRoutes(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		c := a.setup("kazuha", pw)
		r := c.do("GET", "/api/invites/accept", nil).want(t, 405, "invalid")
		if r.resp.Header.Get("Allow") != "POST" {
			t.Fatalf("Allow %q", r.resp.Header.Get("Allow"))
		}
		r = c.do("POST", "/api/invites/some-id", "{}").want(t, 405, "invalid")
		if r.resp.Header.Get("Allow") != "DELETE" {
			t.Fatalf("Allow %q", r.resp.Header.Get("Allow"))
		}
		c.do("PUT", "/api/session", "{}").want(t, 405, "invalid")
		c.do("GET", "/api/nothing", nil).want(t, 404, "not_found")
		c.do("GET", "/", nil).want(t, 404, "not_found")
	})
}

func TestCommitsCarryTheOwnerAsAuthor(t *testing.T) {
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		ctx := context.Background()
		c := a.setup("kazuha", pw)
		sid := c.personalSpace()
		h, err := a.mgr.Open(ctx, mustUserID(t, a, "kazuha"), sid)
		if err != nil {
			t.Fatal(err)
		}
		last := func() string {
			out, err := exec.Command("git", "-C", filepath.Join(a.dir, "spaces", sid), "log", "-1", "--format=%an <%ae>|%cn <%ce>").Output()
			if err != nil {
				t.Fatal(err)
			}
			return strings.TrimSpace(string(out))
		}
		const service = "PetalBook Test <svc@example.test>"
		// No name or email set: the username-based address.
		c.do("POST", "/api/spaces/"+sid+"/pages", map[string]any{"meta": map[string]any{"title": "one"}}).want(t, 201, "")
		h.Space.Flush()
		if got := last(); got != "kazuha <kazuha@users.petalbook.invalid>|"+service {
			t.Fatalf("%q", got)
		}
		// A profile change applies to the next commit of the open space.
		c.do("PATCH", "/api/account", map[string]any{"name": "Kazuha Z", "email": "k@example.test"}).want(t, 200, "")
		c.do("POST", "/api/spaces/"+sid+"/pages", map[string]any{"meta": map[string]any{"title": "two"}}).want(t, 201, "")
		h.Space.Flush()
		if got := last(); got != "Kazuha Z <k@example.test>|"+service {
			t.Fatalf("%q", got)
		}
		// Characters that would break the identity are dropped, not passed to Git.
		c.do("PATCH", "/api/account", map[string]any{"name": "A <b> C", "email": ""}).want(t, 200, "")
		c.do("POST", "/api/spaces/"+sid+"/pages", map[string]any{"meta": map[string]any{"title": "three"}}).want(t, 201, "")
		h.Space.Flush()
		if got := last(); got != "A b C <kazuha@users.petalbook.invalid>|"+service {
			t.Fatalf("%q", got)
		}
		// A space created later gets the same treatment from its first commit.
		r := c.do("POST", "/api/spaces", map[string]any{"name": "More"}).want(t, 201, "")
		nsid := r.body["id"].(string)
		c.do("POST", "/api/spaces/"+nsid+"/pages", map[string]any{"meta": map[string]any{"title": "x"}}).want(t, 201, "")
		nh, _ := a.mgr.Open(ctx, mustUserID(t, a, "kazuha"), nsid)
		nh.Space.Flush()
		out, _ := exec.Command("git", "-C", filepath.Join(a.dir, "spaces", nsid), "log", "-1", "--format=%an|%cn").Output()
		if strings.TrimSpace(string(out)) != "A b C|PetalBook Test" {
			t.Fatalf("%q", out)
		}
	})
}
