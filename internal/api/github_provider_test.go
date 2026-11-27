package api_test

import (
	"context"
	"net/url"
	"testing"

	"petalbook.app/server/internal/store"
)

// The GitHub provider is one row for signing in and for connecting repositories
// (consolidation 5c). These tests run both through the server's one GitHub App,
// with the fake GitHub.

// saveGitHub saves the App and turns the provider on for signing in.
func saveGitHub(t *testing.T, a *acc, admin *client, patch map[string]any) {
	t.Helper()
	admin.do("PUT", "/api/admin/providers/github", map[string]any{"appId": "424242", "privateKey": a.fake.KeyPEM(),
		"clientId": a.fake.ClientID, "clientSecret": a.fake.ClientSecret}).want(t, 200, "")
	body := map[string]any{"enabled": true}
	for k, v := range patch {
		body[k] = v
	}
	admin.do("PATCH", "/api/admin/providers/github", body).want(t, 200, "")
}

// ghSignIn plays a visitor signing in with a GitHub login; it returns where the
// browser ends.
func ghSignIn(t *testing.T, a *acc, c *client, login string) string {
	t.Helper()
	r := c.do("POST", "/api/auth/github/github/start", map[string]any{}).want(t, 200, "")
	back, _ := url.Parse(a.fake.Authorize(r.body["url"].(string), login))
	end, _ := finish(t, c, back.Path+"?"+back.RawQuery, "Sec-Fetch-Site", "cross-site")
	return end
}

// ghLink plays a signed-in person linking the GitHub provider for a purpose.
func ghLink(t *testing.T, a *acc, c *client, purpose, login string) string {
	t.Helper()
	r := c.do("POST", "/api/account/links/github/start", map[string]any{"purpose": purpose, "return": "/settings"}).want(t, 200, "")
	back, _ := url.Parse(a.fake.Authorize(r.body["url"].(string), login))
	end, _ := finish(t, c, back.Path+"?"+back.RawQuery, "Sec-Fetch-Site", "cross-site")
	return end
}

func TestGitHubIsOneProviderForSigningInAndForRepositories(t *testing.T) {
	t.Parallel()
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		admin := a.setup("root", pw)
		saveGitHub(t, a, admin, nil)
		// One row, listed once: a sign-in provider that holds repositories.
		n := 0
		for _, p := range admin.do("GET", "/api/admin/providers", nil).want(t, 200, "").body["providers"].([]any) {
			m := p.(map[string]any)
			if m["id"] != "github" {
				continue
			}
			n++
			repo, _ := m["repo"].(map[string]any)
			if m["signin"] != true || m["repos"] != true || m["enabled"] != true || repo == nil || repo["configured"] != true {
				t.Fatalf("%v", m)
			}
		}
		if n != 1 {
			t.Fatalf("GitHub is listed %d times", n)
		}
		got := admin.do("GET", "/api/admin/providers/github", nil).want(t, 200, "")
		if got.body["configured"] != true || got.body["slug"] != "petalbook-notes" || got.body["provider"].(map[string]any)["kind"] != "github" {
			t.Fatalf("%s", got.raw)
		}

		// A person links their GitHub account to sign in with it: the link holds no token.
		ann := a.addUser("ann", pw, "member")
		a.fake.AddUser("ann-gh")
		if end := ghLink(t, a, ann, "signin", "ann-gh"); end != "/settings" {
			t.Fatalf("%q", end)
		}
		l := linkAt(t, ann, "github")
		if l == nil || l["signin"] != true || l["repos"] != false || l["login"] != "" && l["login"] != "ann-gh" {
			t.Fatalf("%v", l)
		}
		var tokens int
		if err := a.st.Q().QueryRow(context.Background(), `SELECT COUNT(*) FROM linked_accounts WHERE access_enc <> '' OR refresh_enc <> ''`).Scan(&tokens); err != nil || tokens != 0 {
			t.Fatalf("a sign-in link holds tokens: %d %v", tokens, err)
		}
		// Signing in with it later stores none either, and gives no repository access.
		visitor := a.newClient("")
		if end := ghSignIn(t, a, visitor, "ann-gh"); end != "/" || signedInAs(t, visitor) != "ann" {
			t.Fatalf("%q %q", end, signedInAs(t, visitor))
		}
		if err := a.st.Q().QueryRow(context.Background(), `SELECT COUNT(*) FROM linked_accounts WHERE access_enc <> '' OR refresh_enc <> ''`).Scan(&tokens); err != nil || tokens != 0 {
			t.Fatalf("signing in stored a token: %d %v", tokens, err)
		}
		visitor.do("GET", "/api/account/links/github/repos?mine=true", nil).want(t, 409, "not_connected")

		// Connecting repositories re-authorises on the same link, which stays a sign-in.
		if end := ghLink(t, a, ann, "repos", "ann-gh"); end != "/settings" {
			t.Fatalf("%q", end)
		}
		after := linkAt(t, ann, "github")
		if after["id"] != l["id"] || after["signin"] != true || after["repos"] != true || after["login"] != "ann-gh" || after["state"] != "ok" {
			t.Fatalf("%v then %v", l, after)
		}
		if links := ann.do("GET", "/api/account/links", nil).body["links"].([]any); len(links) != 1 {
			t.Fatalf("%v", links)
		}
		ann.do("GET", "/api/account/links/github/repos?mine=true", nil).want(t, 200, "")
	})
}

func TestAGitHubAccountConnectedOnlyForRepositoriesDoesNotSignIn(t *testing.T) {
	t.Parallel()
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		admin := a.setup("root", pw)
		saveGitHub(t, a, admin, nil)
		bob := a.addUser("bob", pw, "member")
		a.fake.AddUser("bob-gh")
		a.fake.AddUser("other-gh")
		ghLink(t, a, bob, "repos", "bob-gh")
		l := linkAt(t, bob, "github")
		if l["signin"] != false || l["repos"] != true {
			t.Fatalf("%v", l)
		}
		// Whoever holds that GitHub account is not thereby let in as bob.
		visitor := a.newClient("")
		if end := ghSignIn(t, a, visitor, "bob-gh"); end != "/login?sso_error=not_for_signin" || signedInAs(t, visitor) != "" {
			t.Fatalf("%q", end)
		}
		// One account at GitHub per person: a second one is not linked for signing in.
		if end := ghLink(t, a, bob, "signin", "other-gh"); end != "/settings?sso_error=already_linked" {
			t.Fatalf("%q", end)
		}
		// "Use to sign in" with the same account turns it on, on the same link.
		if end := ghLink(t, a, bob, "signin", "bob-gh"); end != "/settings" {
			t.Fatalf("%q", end)
		}
		after := linkAt(t, bob, "github")
		if after["id"] != l["id"] || after["signin"] != true || after["repos"] != true {
			t.Fatalf("%v", after)
		}
		v2 := a.newClient("")
		if end := ghSignIn(t, a, v2, "bob-gh"); end != "/" || signedInAs(t, v2) != "bob" {
			t.Fatalf("%q", end)
		}
		// The account is one person's: another person cannot link it either way.
		cy := a.addUser("cy", pw, "member")
		if end := ghLink(t, a, cy, "signin", "bob-gh"); end != "/settings?sso_error=identity_in_use" {
			t.Fatalf("%q", end)
		}
		if end := ghLink(t, a, cy, "repos", "bob-gh"); end != "/settings?provider=github&connection_error=account_in_use" {
			t.Fatalf("%q", end)
		}
	})
}

func TestConnectingRepositoriesNeverRemovesTheLastWayIn(t *testing.T) {
	t.Parallel()
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		admin := a.setup("root", pw)
		saveGitHub(t, a, admin, map[string]any{"autoCreate": true, "github": map[string]any{"organizations": []string{"kazuhahub"}}})
		a.fake.AddUser("new-gh")
		a.fake.AddUser("other-gh")
		a.fake.AddOrg("kazuhahub", "new-gh")
		// An account made by signing in with GitHub has no other way in.
		c := a.newClient("")
		if end := ghSignIn(t, a, c, "new-gh"); end != "/" || signedInAs(t, c) != "new-gh" {
			t.Fatalf("%q %q", end, signedInAs(t, c))
		}
		l := linkAt(t, c, "github")
		// Authorising for repositories with another GitHub account would move the
		// link, and its sign-in with it: refused, and nothing changes.
		if end := ghLink(t, a, c, "repos", "other-gh"); end != "/settings?provider=github&connection_error=last_method" {
			t.Fatalf("%q", end)
		}
		if now := linkAt(t, c, "github"); now["id"] != l["id"] || now["login"] != l["login"] || now["signin"] != true || now["repos"] != false {
			t.Fatalf("%v then %v", l, now)
		}
		// The same account is fine, and gives repositories on the same link.
		if end := ghLink(t, a, c, "repos", "new-gh"); end != "/settings" {
			t.Fatalf("%q", end)
		}
		// Removing the link is refused too, and keeps the tokens.
		id := linkAt(t, c, "github")["id"].(string)
		c.do("DELETE", "/api/account/links/"+id, nil).want(t, 409, "last_method")
		if now := linkAt(t, c, "github"); now == nil || now["repos"] != true {
			t.Fatalf("a refused removal changed the link: %v", now)
		}
		// With a password, the other account may take the link: the sign-in is not the other's.
		c.do("POST", "/api/account/password", map[string]any{"new": pw}).want(t, 204, "")
		disconnectRepos(t, c, "github")
		if end := ghLink(t, a, c, "repos", "other-gh"); end != "/settings" {
			t.Fatalf("%q", end)
		}
		now := linkAt(t, c, "github")
		if now["login"] != "other-gh" || now["signin"] != false || now["repos"] != true {
			t.Fatalf("%v", now)
		}
	})
}

func TestUnlinkingGitHubDisconnectsAndUnlinks(t *testing.T) {
	t.Parallel()
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		admin := a.setup("root", pw)
		saveGitHub(t, a, admin, nil)
		dee := a.addUser("dee", pw, "member")
		a.fake.AddUser("dee-gh")
		ghLink(t, a, dee, "signin", "dee-gh")
		ghLink(t, a, dee, "repos", "dee-gh")
		id := linkAt(t, dee, "github")["id"].(string)
		// Dropping the repositories keeps the sign-in; GitHub is asked to revoke the grant.
		dee.do("DELETE", "/api/account/links/"+id+"/repos", nil).want(t, 204, "")
		if a.fake.AccessTokenOf("dee-gh") != "" {
			t.Fatal("the grant was not revoked")
		}
		if l := linkAt(t, dee, "github"); l["signin"] != true || l["repos"] != false {
			t.Fatalf("%v", l)
		}
		dee.do("DELETE", "/api/account/links/"+id+"/repos", nil).want(t, 404, "not_found")
		// Removing a link of someone else's is a 404.
		other := a.addUser("eve", pw, "member")
		other.do("DELETE", "/api/account/links/"+id, nil).want(t, 404, "not_found")
		other.do("DELETE", "/api/account/links/"+id+"/repos", nil).want(t, 404, "not_found")
		// A link with both goes whole, tokens revoked, when another way in is left.
		ghLink(t, a, dee, "repos", "dee-gh")
		dee.do("DELETE", "/api/account/links/"+id, nil).want(t, 204, "")
		if linkAt(t, dee, "github") != nil || a.fake.AccessTokenOf("dee-gh") != "" {
			t.Fatal("the link or its grant survived")
		}
		var n int
		if err := a.st.Q().QueryRow(context.Background(), `SELECT COUNT(*) FROM linked_accounts WHERE provider_id = 'github'`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%d %v", n, err)
		}
	})
}

func TestDeletingTheGitHubProviderAsksAboutThePeopleWhoSignInWithIt(t *testing.T) {
	t.Parallel()
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		admin := a.setup("root", pw)
		saveGitHub(t, a, admin, map[string]any{"autoCreate": true, "github": map[string]any{"organizations": []string{"kazuhahub"}}})
		a.fake.AddUser("gh-only")
		a.fake.AddOrg("kazuhahub", "gh-only")
		c := a.newClient("")
		ghSignIn(t, a, c, "gh-only")
		ghLink(t, a, c, "repos", "gh-only")
		// Someone's only way in: refused without force, and nothing is revoked or removed.
		admin.do("DELETE", "/api/admin/providers/github", nil).want(t, 409, "provider_in_use")
		if a.fake.AccessTokenOf("gh-only") == "" || !a.ghsvc.Configured(context.Background()) {
			t.Fatal("a refused deletion changed something")
		}
		admin.do("DELETE", "/api/admin/providers/github?force=1", nil).want(t, 204, "")
		if a.fake.AccessTokenOf("gh-only") != "" {
			t.Fatal("the grant was not revoked")
		}
		if a.ghsvc.Configured(context.Background()) {
			t.Fatal("the App is still set up")
		}
		if _, err := store.GetProvider(context.Background(), a.st.Q(), "github"); err == nil {
			t.Fatal("the row is still there")
		}
		admin.do("GET", "/api/admin/providers/github", nil).want(t, 200, "")
		if got := admin.do("GET", "/api/admin/providers/github", nil); got.body["configured"] != false || got.body["provider"] != nil {
			t.Fatalf("%s", got.raw)
		}
		// Everything it kept is gone: no link, no secret.
		var n int
		a.st.Q().QueryRow(context.Background(), `SELECT COUNT(*) FROM linked_accounts`).Scan(&n)
		if n != 0 {
			t.Fatalf("%d links", n)
		}
		a.st.Q().QueryRow(context.Background(), `SELECT COUNT(*) FROM secrets WHERE name LIKE 'github%'`).Scan(&n)
		if n != 0 {
			t.Fatalf("%d secrets", n)
		}
	})
}
