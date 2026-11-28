package api_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
)

// The callback the form of a new host shows comes from the server, from the
// path its route is made of: it is the address the provider is given once it
// exists (GitHub's view says the same of its own).
func TestTheProviderListGivesTheCallbackOfItsRoute(t *testing.T) {
	t.Parallel()
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		admin := a.setup("root", pw)
		tmpl, _ := admin.do("GET", "/api/admin/providers", nil).want(t, 200, "").body["linkCallback"].(string)
		if !strings.Contains(tmpl, "{id}") {
			t.Fatalf("%q", tmpl)
		}
		gh := admin.do("GET", "/api/admin/providers/github", nil).want(t, 200, "")
		if want := strings.Replace(tmpl, "{id}", "github", 1); gh.body["callbackUrl"] != want {
			t.Fatalf("%v, want %q", gh.body["callbackUrl"], want)
		}
		// Who does not manage connections is not given it.
		signin := a.addUser("signin", pw, "member")
		grantRole(t, a, "R-signin", store.RoleScopeSite, "", store.GrantToUser, mustUserID(t, a, "signin"), "site.signin.manage")
		if got := signin.do("GET", "/api/admin/providers", nil).want(t, 200, "").body["linkCallback"]; got != "" {
			t.Fatalf("%v", got)
		}
	})
}

// Removing the GitHub row people sign in with ends their sign-ins: an
// administrator who manages only connections is refused and nothing changes;
// one who manages sign-in removes it, asked about who would be locked out.
func TestRemovingGitHubThatPeopleSignInWithTakesTheSigninPermission(t *testing.T) {
	t.Parallel()
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		admin := a.setup("root", pw)
		saveGitHub(t, a, admin, nil)
		ann := a.addUser("ann", pw, "member")
		a.fake.AddUser("ann-gh")
		ghLink(t, a, ann, "signin", "ann-gh")
		ghLink(t, a, ann, "repos", "ann-gh")

		conn := a.addUser("conn", pw, "member")
		grantRole(t, a, "R-conn", store.RoleScopeSite, "", store.GrantToUser, mustUserID(t, a, "conn"), "site.connections.manage")
		conn.do("DELETE", "/api/admin/providers/github?force=1", nil).want(t, 403, "forbidden")
		if _, err := store.GetProvider(context.Background(), a.st.Q(), "github"); err != nil {
			t.Fatalf("a refused removal removed the row: %v", err)
		}
		if l := linkAt(t, ann, "github"); l == nil || l["signin"] != true || l["repos"] != true || a.fake.AccessTokenOf("ann-gh") == "" {
			t.Fatalf("a refused removal changed the link: %v", l)
		}
		if !a.ghsvc.Configured(context.Background()) {
			t.Fatal("a refused removal removed the App")
		}
		admin.do("DELETE", "/api/admin/providers/github", nil).want(t, 204, "")
		if _, err := store.GetProvider(context.Background(), a.st.Q(), "github"); err == nil {
			t.Fatal("the row is still there")
		}
	})
}

// The App is never set up on a GitHub sign-in row whose people linked their
// accounts on another GitHub host: their links name accounts of that host.
func TestTheAppIsRefusedOnASigninRowOfAnotherHost(t *testing.T) {
	t.Parallel()
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		admin := a.setup("root", pw)
		admin.do("POST", "/api/admin/providers", map[string]any{"slug": "github", "kind": "github", "name": "GHE",
			"github": map[string]any{"baseUrl": "https://ghe.example.test", "clientId": "c", "clientSecret": "s"}}).want(t, 201, "")
		ann := a.addUser("ann", pw, "member")
		_ = ann
		if err := store.InsertLink(context.Background(), a.st.Q(), store.Link{ID: "L1", ProviderID: "github", UserID: mustUserID(t, a, "ann"),
			Subject: "1", SignIn: true, Created: time.Now()}); err != nil {
			t.Fatal(err)
		}
		r := admin.do("PUT", "/api/admin/providers/github", map[string]any{"appId": "424242", "privateKey": a.fake.KeyPEM(),
			"clientId": a.fake.ClientID, "clientSecret": a.fake.ClientSecret}).want(t, 409, "provider_host_mismatch")
		if msg, _ := r.body["message"].(string); !strings.Contains(msg, "https://ghe.example.test") || msg[0] < 'A' || msg[0] > 'Z' || strings.Contains(msg, "change its base URL") {
			t.Fatalf("%s", r.raw)
		}
		row, err := store.GetProvider(context.Background(), a.st.Q(), "github")
		if err != nil || row.Repos || !strings.Contains(row.Config, "ghe.example.test") {
			t.Fatalf("%+v %v", row, err)
		}
	})
}

// The ids of the server's own providers are their kind's alone.
func TestTheSlugsOfTheServersOwnProvidersAreReserved(t *testing.T) {
	t.Parallel()
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		admin := a.setup("root", pw)
		for _, slug := range []string{"github", "git"} {
			admin.do("POST", "/api/admin/providers", map[string]any{"slug": slug, "kind": "oidc", "name": "X",
				"oidc": map[string]any{"issuer": "https://idp.example.test", "clientId": "c", "clientSecret": "s"}}).want(t, 409, "slug_reserved")
		}
		admin.do("POST", "/api/admin/providers", map[string]any{"slug": "git", "kind": "github", "name": "X",
			"github": map[string]any{"baseUrl": "https://ghe.example.test", "clientId": "c", "clientSecret": "s"}}).want(t, 409, "slug_reserved")
	})
}

// Removing a link that signs a person in and holds their repositories is one
// step: refused, it keeps the tokens and GitHub's grant; allowed, the row goes
// first and the grant is revoked after.
func TestUnlinkingASigninThatHoldsRepositoriesIsOneStep(t *testing.T) {
	t.Parallel()
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		a.setSetting(t, "registration.mode", "open")
		admin := a.setup("root", pw)
		saveGitHub(t, a, admin, nil)
		a.fake.AddUser("only-gh")
		a.fake.AddOrg("kazuhahub", "only-gh")
		c := a.newClient("")
		ghSignIn(t, a, c, "only-gh")
		ghLink(t, a, c, "repos", "only-gh")
		id := linkAt(t, c, "github")["id"].(string)
		c.do("DELETE", "/api/account/links/"+id, nil).want(t, 409, "last_method")
		if l := linkAt(t, c, "github"); l == nil || l["signin"] != true || l["repos"] != true || a.fake.AccessTokenOf("only-gh") == "" {
			t.Fatalf("a refused removal dropped the tokens or the grant: %v", l)
		}
		c.do("GET", "/api/account/links/github/repos?mine=true", nil).want(t, 200, "")
		c.do("POST", "/api/account/password", map[string]any{"new": pw}).want(t, 204, "")
		c.do("DELETE", "/api/account/links/"+id, nil).want(t, 204, "")
		if linkAt(t, c, "github") != nil || a.fake.AccessTokenOf("only-gh") != "" {
			t.Fatal("the link or its grant survived")
		}
		c.do("GET", "/api/account/links/github/repos?mine=true", nil).want(t, 409, "not_connected")
	})
}

// A GitHub sign-in provider people linked their accounts on keeps its host:
// changing its base URL to another is refused, and the answer never suggests it.
func TestAGitHubSigninProviderKeepsItsHostWhileAccountsAreLinked(t *testing.T) {
	t.Parallel()
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		admin := a.setup("root", pw)
		admin.do("POST", "/api/admin/providers", map[string]any{"slug": "ghe", "kind": "github", "name": "GHE",
			"github": map[string]any{"baseUrl": "https://ghe.example.test", "clientId": "c", "clientSecret": "s"}}).want(t, 201, "")
		a.addUser("ann", pw, "member")
		if err := store.InsertLink(context.Background(), a.st.Q(), store.Link{ID: "L1", ProviderID: "ghe", UserID: mustUserID(t, a, "ann"),
			Subject: "1", SignIn: true, Created: time.Now()}); err != nil {
			t.Fatal(err)
		}
		r := admin.do("PATCH", "/api/admin/providers/ghe", map[string]any{"github": map[string]any{"baseUrl": "https://github.com"}}).want(t, 409, "provider_host_mismatch")
		if msg, _ := r.body["message"].(string); !strings.Contains(msg, "remove its linked accounts") || strings.Contains(msg, "change its base URL") {
			t.Fatalf("%s", r.raw)
		}
		got := admin.do("GET", "/api/admin/providers/ghe", nil).want(t, 200, "")
		if !strings.Contains(string(got.raw), "ghe.example.test") {
			t.Fatalf("a refused move was written: %s", got.raw)
		}
		// Other changes are made.
		admin.do("PATCH", "/api/admin/providers/ghe", map[string]any{"name": "GHE 2"}).want(t, 200, "")
	})
}
