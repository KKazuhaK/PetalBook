package api_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/KKazuhaK/PetalBook/internal/store"
)

// signIn runs a sign-in at a GitHub provider as the fake user and returns where it ended.
func (g *fakeGitHub) signIn(t *testing.T, c *client, slug, who string, body map[string]any) (string, map[string]any) {
	t.Helper()
	r := c.do("POST", "/api/auth/github/"+slug+"/start", body).want(t, 200, "")
	authURL := r.body["url"].(string)
	hc := anyTLSClient()
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := hc.Get(authURL + "&user=" + url.QueryEscape(who))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 302 {
		t.Fatalf("GitHub answered %d", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || loc.Path != "/api/auth/github/"+slug+"/callback" {
		t.Fatalf("redirect back to %v", resp.Header.Get("Location"))
	}
	end, _ := finish(t, c, loc.RequestURI())
	q, _ := url.Parse(authURL)
	return end, map[string]any{"scope": q.Query().Get("scope"), "challenge": q.Query().Get("code_challenge_method")}
}

func TestGitHubSignInNeedsAnOrganisationToCreateAccounts(t *testing.T) {
	t.Parallel()
	gh := newFakeGitHub(t)
	gh.users["member"] = ghFake{ID: 4711, Login: "Octo-Cat", Name: "Octo Cat", Email: "octo@example.test", Verified: true, Orgs: []string{"kazuhahub", "other"}}
	gh.users["outsider"] = ghFake{ID: 99, Login: "outsider", Orgs: []string{"somewhere"}}
	gh.users["unverified"] = ghFake{ID: 100, Login: "newbie", Email: "n@example.test", Verified: false, Orgs: []string{"KazuhaHub"}}
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		admin.do("POST", "/api/admin/auth/providers", gh.body("gh", map[string]any{"autoCreate": true})).want(t, 400, "github_auto_create_needs_org")
		body := gh.body("gh", map[string]any{"autoCreate": true})
		body["github"].(map[string]any)["organizations"] = []string{"KazuhaHub"}
		p := admin.do("POST", "/api/admin/auth/providers", body).want(t, 201, "")
		g := p.body["github"].(map[string]any)
		if g["redirectUrl"] != testPublic+"/api/auth/github/gh/callback" || g["hasSecret"] != true || g["baseUrl"] != gh.srv.URL || strings.Contains(p.raw, "gh-secret") {
			t.Fatal(p.raw)
		}
		// A member of the organisation (named in another case) gets an account, named from the login.
		c := a.newClient("")
		end, q := gh.signIn(t, c, "gh", "member", map[string]any{"return": "/s/x"})
		if end != "/s/x" || signedInAs(t, c) != "octo-cat" {
			t.Fatalf("%s %s", end, signedInAs(t, c))
		}
		if q["scope"] != "read:user user:email read:org" || q["challenge"] != "S256" {
			t.Fatalf("%v", q)
		}
		u := c.do("GET", "/api/session", nil).user()
		if u["name"] != "Octo Cat" || u["email"] != "octo@example.test" || u["emailVerified"] != true || u["role"] != "member" {
			t.Fatal(u)
		}
		// The identity is the base address and the numeric id, never the login name.
		if _, err := store.FindIdentity(context.Background(), a.st.Q(), gh.srv.URL, "4711"); err != nil {
			t.Fatal(err)
		}
		acct := c.do("GET", "/api/account", nil).want(t, 200, "")
		id := acct.body["identities"].([]any)[0].(map[string]any)
		if id["kind"] != "github" || id["providerSlug"] != "gh" || id["label"] != "Octo-Cat" {
			t.Fatal(acct.raw)
		}
		// The person renames their GitHub account: the same person signs in.
		gh.mu.Lock()
		o := gh.users["member"]
		o.Login = "renamed"
		gh.users["member"] = o
		gh.mu.Unlock()
		d := a.newClient("")
		if end, _ := gh.signIn(t, d, "gh", "member", map[string]any{}); end != "/" || signedInAs(t, d) != "octo-cat" {
			t.Fatalf("%s %s", end, signedInAs(t, d))
		}
		// Someone in none of the organisations is turned away, and nothing was made.
		e := a.newClient("")
		if end, _ := gh.signIn(t, e, "gh", "outsider", map[string]any{}); end != "/login?sso_error=not_allowed" || signedInAs(t, e) != "" {
			t.Fatal(end)
		}
		if _, err := store.FindIdentity(context.Background(), a.st.Q(), gh.srv.URL, "99"); err == nil {
			t.Fatal("an identity was made for someone not allowed")
		}
		// An address that is not verified is kept as it is, unverified.
		f := a.newClient("")
		gh.signIn(t, f, "gh", "unverified", map[string]any{})
		if u := f.do("GET", "/api/session", nil).user(); u["emailVerified"] != false || u["email"] != "n@example.test" {
			t.Fatal(u)
		}
		// The user's token is used for what GitHub says about them and nothing else.
		for _, p := range gh.seen {
			if p != "/api/v3/user" && p != "/api/v3/user/emails" && p != "/api/v3/user/orgs" {
				t.Fatalf("GitHub was asked %s", p)
			}
		}
		// Links from the account page.
		tang := a.addUser("tang", pw, "member")
		gh.users["tang-gh"] = ghFake{ID: 7, Login: "tang", Orgs: []string{"KazuhaHub"}}
		if end, _ := gh.signIn(t, tang, "gh", "tang-gh", map[string]any{"intent": "link", "return": "/settings"}); end != "/settings" {
			t.Fatal(end)
		}
		if ids := tang.do("GET", "/api/account", nil).body["identities"].([]any); len(ids) != 1 {
			t.Fatal(ids)
		}
		// A provider that does not create accounts: no_account for someone nobody has.
		admin.do("PATCH", "/api/admin/auth/providers/gh", map[string]any{"autoCreate": false}).want(t, 200, "")
		gh.users["fresh"] = ghFake{ID: 8, Login: "fresh", Orgs: []string{"KazuhaHub"}}
		if end, _ := gh.signIn(t, a.newClient(""), "gh", "fresh", map[string]any{}); end != "/login?sso_error=no_account" {
			t.Fatal(end)
		}
		// Without an organisation named, anyone with a GitHub account may sign in to an account they have, and none is created.
		admin.do("PATCH", "/api/admin/auth/providers/gh", map[string]any{"github": map[string]any{"organizations": []string{}}}).want(t, 200, "")
		if end, q := gh.signIn(t, a.newClient(""), "gh", "member", map[string]any{}); end != "/" || q["scope"] != "read:user user:email" {
			t.Fatalf("%s %v", end, q)
		}
	})
}

func TestGitHubRefusedCodeAndBadSecret(t *testing.T) {
	t.Parallel()
	gh := newFakeGitHub(t)
	gh.users["x"] = ghFake{ID: 1, Login: "x"}
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		body := gh.body("gh", map[string]any{})
		body["github"].(map[string]any)["clientSecret"] = "wrong-secret"
		admin.do("POST", "/api/admin/auth/providers", body).want(t, 201, "")
		c := a.newClient("")
		if end, _ := gh.signIn(t, c, "gh", "x", map[string]any{}); end != "/login?sso_error=idp_error" {
			t.Fatal(end)
		}
	})
}
