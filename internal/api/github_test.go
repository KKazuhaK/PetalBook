package api_test

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/githubapp/githubapptest"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

var ghOpts = accOpts{github: true}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, _ := c.Output()
	return strings.TrimSpace(string(out))
}

// ghSetup makes an administrator who has saved the App, and a member "tang"
// who has connected the GitHub account "tang-gh", a member of the organisation
// "kazuhahub".
func ghSetup(t *testing.T, a *acc) (admin, member *client) {
	t.Helper()
	admin = a.setup("root", pw)
	admin.do("PUT", "/api/admin/github", map[string]any{"appId": "424242", "privateKey": a.fake.KeyPEM(),
		"clientId": a.fake.ClientID, "clientSecret": a.fake.ClientSecret}).want(t, 200, "")
	member = a.addUser("tang", pw, "member")
	a.fake.AddUser("tang-gh")
	a.fake.AddOrg("kazuhahub", "tang-gh")
	a.fake.Install("kazuhahub", "all")
	connectGitHub(t, a, member, "tang-gh")
	return admin, member
}

// connectGitHub plays the browser through the whole authorisation.
func connectGitHub(t *testing.T, a *acc, c *client, login string) {
	t.Helper()
	r := c.do("POST", "/api/github/authorization/start", map[string]any{"return": "/s/ABC/settings"}).want(t, 200, "")
	back, _ := url.Parse(a.fake.Authorize(r.body["url"].(string), login))
	cb := c.do("GET", back.Path+"?"+back.RawQuery, nil, "Sec-Fetch-Site", "cross-site")
	if cb.code != 303 || cb.resp.Header.Get("Location") != "/s/ABC/settings" {
		t.Fatalf("the callback answered %d %q", cb.code, cb.resp.Header.Get("Location"))
	}
}

func TestGitHubIsNotConfiguredByDefault(t *testing.T) {
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		admin := a.setup("root", pw)
		r := admin.do("GET", "/api/github", nil).want(t, 200, "")
		if r.body["app"] != nil || r.body["userAuthorization"] != false || r.body["authorization"] != nil {
			t.Fatalf("%s", r.raw)
		}
		a0 := admin.do("GET", "/api/admin/github", nil).want(t, 200, "")
		if a0.body["configured"] != false || a0.body["callbackUrl"] != "https://notes.example.test/api/github/authorization/callback" {
			t.Fatalf("%s", a0.raw)
		}
		for _, p := range []string{"/api/github/owners", "/api/github/repos?mine=true", "/api/admin/github/installations"} {
			admin.do("GET", p, nil).want(t, 409, "github_not_configured")
		}
		admin.do("POST", "/api/github/authorization/start", map[string]any{}).want(t, 409, "github_not_configured")
		admin.do("POST", "/api/spaces", map[string]any{"name": "X", "github": map[string]any{"owner": "o", "name": "n"}}).want(t, 409, "github_not_configured")
	})
}

func TestTheAdministratorsSaveTheApp(t *testing.T) {
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		admin := a.setup("root", pw)
		member := a.addUser("tang", pw, "member")
		member.do("GET", "/api/admin/github", nil).want(t, 403, "forbidden")
		member.do("PUT", "/api/admin/github", map[string]any{}).want(t, 403, "forbidden")
		member.do("DELETE", "/api/admin/github", nil).want(t, 403, "forbidden")
		member.do("GET", "/api/admin/github/installations", nil).want(t, 403, "forbidden")
		member.do("PUT", "/api/admin/github/installations/1", map[string]any{"shared": true}).want(t, 403, "forbidden")

		admin.do("PUT", "/api/admin/github", map[string]any{"appId": "424242"}).want(t, 400, "invalid")
		admin.do("PUT", "/api/admin/github", map[string]any{"appId": "424242", "privateKey": "nonsense"}).want(t, 400, "github_key_invalid")
		admin.do("PUT", "/api/admin/github", map[string]any{"appId": "1", "privateKey": a.fake.KeyPEM()}).want(t, 400, "github_app_rejected")
		admin.do("PUT", "/api/admin/github", map[string]any{"appId": "424242", "privateKey": a.fake.KeyPEM(), "clientId": a.fake.ClientID, "clientSecret": "no"}).want(t, 400, "github_client_rejected")
		if r := admin.do("GET", "/api/admin/github", nil); r.body["configured"] != false {
			t.Fatalf("a refused save left settings: %s", r.raw)
		}
		r := admin.do("PUT", "/api/admin/github", map[string]any{"appId": "424242", "privateKey": a.fake.KeyPEM(), "clientId": a.fake.ClientID, "clientSecret": a.fake.ClientSecret}).want(t, 200, "")
		if r.body["configured"] != true || r.body["appId"] != "424242" || r.body["slug"] != "petalbook-notes" || r.body["userAuthorization"] != true ||
			r.body["installUrl"] != "https://github.com/apps/petalbook-notes/installations/new" {
			t.Fatalf("%s", r.raw)
		}
		pk := r.body["privateKey"].(map[string]any)
		if pk["set"] != true || !strings.HasPrefix(pk["fingerprint"].(string), "SHA256:") || r.body["clientSecret"].(map[string]any)["set"] != true {
			t.Fatalf("%s", r.raw)
		}
		if m := r.body["missing"].([]any); len(m) != 0 {
			t.Fatalf("%s", r.raw)
		}
		// Nothing secret is ever returned.
		got := admin.do("GET", "/api/admin/github", nil)
		for _, secret := range []string{a.fake.ClientSecret, "PRIVATE KEY", "MII"} {
			if strings.Contains(r.raw, secret) || strings.Contains(got.raw, secret) {
				t.Fatalf("the response holds %q", secret)
			}
		}
		// Anyone signed in sees the App's name and where to install it.
		g := member.do("GET", "/api/github", nil).want(t, 200, "")
		app := g.body["app"].(map[string]any)
		if app["name"] != "PetalBook Notes" || app["installUrl"] == "" || g.body["userAuthorization"] != true {
			t.Fatalf("%s", g.raw)
		}
		admin.do("DELETE", "/api/admin/github", nil).want(t, 204, "")
		if r := admin.do("GET", "/api/admin/github", nil); r.body["configured"] != false {
			t.Fatal(r.raw)
		}
	})
}

func TestInstallationsAndTheirSharing(t *testing.T) {
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		admin, member := ghSetup(t, a)
		for i := 0; i < 35; i++ {
			a.fake.AddOrg("org-" + strings.Repeat("x", i+1))
			a.fake.Install("org-"+strings.Repeat("x", i+1), "all")
		}
		r := admin.do("GET", "/api/admin/github/installations", nil).want(t, 200, "")
		items := r.body["items"].([]any)
		next, _ := r.body["next"].(string)
		if len(items) != 30 || next == "" {
			t.Fatalf("%d %q", len(items), next)
		}
		first := items[0].(map[string]any)
		acct := first["account"].(map[string]any)
		if acct["login"] != "kazuhahub" || acct["type"] != "Organization" || first["repositorySelection"] != "all" || fmt.Sprint(first["tenants"]) != "[]" || first["suspended"] != false {
			t.Fatalf("%v", first)
		}
		r2 := admin.do("GET", "/api/admin/github/installations?cursor="+url.QueryEscape(next), nil).want(t, 200, "")
		if len(r2.body["items"].([]any)) != 6 || r2.body["next"] != "" {
			t.Fatalf("%s", r2.raw)
		}
		admin.do("GET", "/api/admin/github/installations?cursor=junk", nil).want(t, 400, "invalid")
		id := int64(first["id"].(float64))
		path := "/api/admin/github/installations/" + itoa(id)
		admin.do("PUT", path, map[string]any{}).want(t, 400, "invalid")
		admin.do("PUT", path, map[string]any{"shared": true}).want(t, 400, "invalid") // the old shape
		admin.do("PUT", path, map[string]any{"tenants": []string{"nope"}}).want(t, 400, "invalid")
		put := admin.do("PUT", path, map[string]any{"tenants": []string{store.DefaultTenantID}}).want(t, 200, "")
		if fmt.Sprint(put.body["tenants"]) != "["+store.DefaultTenantID+"]" || put.body["id"] != float64(id) {
			t.Fatalf("%s", put.raw)
		}
		admin.do("PUT", "/api/admin/github/installations/999999", map[string]any{"tenants": []string{store.DefaultTenantID}}).want(t, 404, "not_found")
		admin.do("PUT", "/api/admin/github/installations/nope", map[string]any{"tenants": []string{store.DefaultTenantID}}).want(t, 404, "not_found")
		// A member without a GitHub connection can then create in the shared organisation.
		other := a.addUser("nobody", pw, "member")
		o := other.do("GET", "/api/github/owners", nil).want(t, 200, "")
		list := o.body["items"].([]any)
		if len(list) != 1 || list[0].(map[string]any)["login"] != "kazuhahub" || list[0].(map[string]any)["via"] != "app" {
			t.Fatalf("%s", o.raw)
		}
		// Stopping to share takes it away again.
		admin.do("PUT", path, map[string]any{"tenants": []string{}}).want(t, 200, "")
		o = other.do("GET", "/api/github/owners", nil).want(t, 200, "")
		if len(o.body["items"].([]any)) != 0 {
			t.Fatalf("%s", o.raw)
		}
		_ = member
	})
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func fmtInt(n int64) string { return itoa(n) }

type ghAccount = githubapptest.Account

func TestConnectingAnAccountThroughTheApi(t *testing.T) {
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		admin, member := ghSetup(t, a)
		g := member.do("GET", "/api/github", nil).want(t, 200, "")
		au := g.body["authorization"].(map[string]any)
		if au["login"] != "tang-gh" || au["state"] != "ok" || au["spaces"] != float64(0) || au["refreshExpires"] == nil || au["githubId"] == nil {
			t.Fatalf("%s", g.raw)
		}
		// The callback is the one cross-site navigation; it needs the browser's cookie and the user's own session.
		start := member.do("POST", "/api/github/authorization/start", map[string]any{"return": "https://evil.example/x"}).want(t, 200, "")
		u, _ := url.Parse(start.body["url"].(string))
		q := u.Query()
		if q.Get("redirect_uri") != "https://notes.example.test/api/github/authorization/callback" || q.Get("code_challenge_method") != "S256" || q.Get("login") != "tang-gh" {
			t.Fatalf("%s", u)
		}
		back, _ := url.Parse(a.fake.Authorize(start.body["url"].(string), "tang-gh"))
		cbPath := back.Path + "?" + back.RawQuery
		// Another user's session: expired, and the attempt stays for its owner.
		other := a.addUser("other", pw, "member")
		for k, v := range member.cookies {
			if strings.Contains(k, "petalbook_github") {
				other.cookies[k] = v
			}
		}
		r := other.do("GET", cbPath, nil, "Sec-Fetch-Site", "cross-site")
		if r.code != 303 || r.resp.Header.Get("Location") != "/settings?github_error=expired" {
			t.Fatalf("%d %q", r.code, r.resp.Header.Get("Location"))
		}
		// No session at all.
		anon := a.newClient("")
		for k, v := range member.cookies {
			if strings.Contains(k, "petalbook_github") {
				anon.cookies[k] = v
			}
		}
		if r := anon.do("GET", cbPath, nil, "Sec-Fetch-Site", "cross-site"); r.code != 303 || r.resp.Header.Get("Location") != "/settings?github_error=expired" {
			t.Fatalf("%d %q", r.code, r.resp.Header.Get("Location"))
		}
		// The right browser: back to a safe return path (an address elsewhere becomes /).
		r = member.do("GET", cbPath, nil, "Sec-Fetch-Site", "cross-site")
		if r.code != 303 || r.resp.Header.Get("Location") != "/" || r.resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("%d %q", r.code, r.resp.Header.Get("Location"))
		}
		// Replay.
		if r := member.do("GET", cbPath, nil, "Sec-Fetch-Site", "cross-site"); r.resp.Header.Get("Location") != "/settings?github_error=expired" {
			t.Fatalf("%q", r.resp.Header.Get("Location"))
		}
		// Cross-site requests to anything else are still refused.
		member.do("POST", "/api/github/authorization/start", map[string]any{}, "Sec-Fetch-Site", "cross-site").want(t, 403, "forbidden")
		// Another PetalBook user cannot take the same GitHub account.
		start = other.do("POST", "/api/github/authorization/start", map[string]any{}).want(t, 200, "")
		back, _ = url.Parse(a.fake.Authorize(start.body["url"].(string), "tang-gh"))
		if r := other.do("GET", back.Path+"?"+back.RawQuery, nil, "Sec-Fetch-Site", "cross-site"); r.resp.Header.Get("Location") != "/settings?github_error=account_in_use" {
			t.Fatalf("%q", r.resp.Header.Get("Location"))
		}
		// Disconnecting.
		member.do("DELETE", "/api/github/authorization", nil).want(t, 204, "")
		if g := member.do("GET", "/api/github", nil); g.body["authorization"] != nil {
			t.Fatal(g.raw)
		}
		member.do("DELETE", "/api/github/authorization", nil).want(t, 204, "")
		// Signed-out visitors cannot start.
		a.newClient("").do("POST", "/api/github/authorization/start", map[string]any{}).want(t, 401, "unauthenticated")
		_ = admin
	})
}

func TestOwnersAndRepositoriesAreListedByPage(t *testing.T) {
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		_, member := ghSetup(t, a)
		o := member.do("GET", "/api/github/owners", nil).want(t, 200, "")
		items := o.body["items"].([]any)
		if len(items) != 2 || items[0].(map[string]any)["login"] != "tang-gh" || items[0].(map[string]any)["installationId"] != nil ||
			items[1].(map[string]any)["via"] != "user" || items[1].(map[string]any)["installationId"] == nil {
			t.Fatalf("%s", o.raw)
		}
		for i := 0; i < 35; i++ {
			a.fake.AddRepo("kazuhahub", "r"+fmtInt(int64(100+i)), true)
		}
		inst := int64(items[1].(map[string]any)["installationId"].(float64))
		r := member.do("GET", "/api/github/repos?installation="+fmtInt(inst), nil).want(t, 200, "")
		list := r.body["items"].([]any)
		first := list[0].(map[string]any)
		if len(list) != 30 || r.body["next"] == "" || first["fullName"] == nil || first["canPush"] != true || first["defaultBranch"] != "main" {
			t.Fatalf("%s", r.raw)
		}
		r2 := member.do("GET", "/api/github/repos?installation="+fmtInt(inst)+"&cursor="+url.QueryEscape(r.body["next"].(string)), nil).want(t, 200, "")
		if len(r2.body["items"].([]any)) != 5 || r2.body["next"] != "" {
			t.Fatalf("%s", r2.raw)
		}
		member.do("GET", "/api/github/repos", nil).want(t, 400, "invalid")
		member.do("GET", "/api/github/repos?installation=x", nil).want(t, 400, "invalid")
		member.do("GET", "/api/github/repos?installation=5&mine=true", nil).want(t, 400, "invalid")
		member.do("GET", "/api/github/repos?installation=4242424", nil).want(t, 404, "not_found")
		mine := member.do("GET", "/api/github/repos?mine=true", nil).want(t, 200, "")
		if len(mine.body["items"].([]any)) != 0 {
			t.Fatalf("%s", mine.raw)
		}
		a.newClient("").do("GET", "/api/github/owners", nil).want(t, 401, "unauthenticated")
	})
}

func TestCreatingASpaceWithANewRepository(t *testing.T) {
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		_, member := ghSetup(t, a)
		r := member.do("POST", "/api/spaces", map[string]any{"name": "Tang", "inbox": "收件箱", "github": map[string]any{
			"owner": "kazuhahub", "name": "tang-notes", "private": true, "description": "Notes on Tang poetry", "create": true}}).want(t, 201, "")
		if r.body["name"] != "Tang" || r.body["sync"] != "ok" || r.resp.Header.Get("Location") != "/api/spaces/"+r.body["id"].(string) {
			t.Fatalf("%s", r.raw)
		}
		id := r.body["id"].(string)
		repo := a.fake.RepoByName("kazuhahub/tang-notes")
		if repo == nil || !repo.Private || repo.Desc != "Notes on Tang poetry" {
			t.Fatalf("%+v", repo)
		}
		rem := member.do("GET", "/api/spaces/"+id+"/remote", nil).want(t, 200, "")
		cred := rem.body["credential"].(map[string]any)
		if rem.body["url"] != a.fake.URL+"/kazuhahub/tang-notes.git" || rem.body["state"] != "ok" || rem.body["errorCode"] != "" ||
			cred["type"] != "github" || cred["via"] != "app" || cred["account"] != "kazuhahub" || cred["repoId"] != float64(repo.ID) || cred["installationId"] == nil {
			t.Fatalf("%s", rem.raw)
		}
		for _, secret := range []string{"ghs_", "ghu_", "ghr_"} {
			if strings.Contains(rem.raw, secret) {
				t.Fatalf("a token in the remote: %s", rem.raw)
			}
		}
		if n := gitOut(t, a.fake.BareDir("kazuhahub/tang-notes"), "rev-list", "--count", "main"); n != "1" {
			t.Fatalf("%s", n)
		}
		// A page written in it is pushed.
		member.do("POST", "/api/spaces/"+id+"/pages", map[string]any{"meta": map[string]any{"title": "x"}, "body": "hello"}).want(t, 201, "")
		if n := gitOut(t, a.fake.BareDir("kazuhahub/tang-notes"), "rev-list", "--count", "main"); n != "2" {
			t.Fatalf("%s", n)
		}
		// In the user's own account the remote names the user and the GitHub login.
		own := member.do("POST", "/api/spaces", map[string]any{"name": "Own", "github": map[string]any{"owner": "tang-gh", "name": "own"}}).want(t, 201, "")
		ownRemote := member.do("GET", "/api/spaces/"+own.body["id"].(string)+"/remote", nil).want(t, 200, "")
		oc := ownRemote.body["credential"].(map[string]any)
		u := oc["user"].(map[string]any)
		if oc["via"] != "user" || oc["login"] != "tang-gh" || u["username"] != "tang" || u["id"] == "" || oc["installationId"] != nil {
			t.Fatalf("%s", ownRemote.raw)
		}
		if !a.fake.RepoByName("tang-gh/own").Private {
			t.Fatal("private is the default")
		}
	})
}

func TestCreatingASpaceFailsCleanly(t *testing.T) {
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		_, member := ghSetup(t, a)
		body := func(owner, name string, create bool) map[string]any {
			return map[string]any{"name": "S", "github": map[string]any{"owner": owner, "name": name, "create": create}}
		}
		dirsBefore := len(listDir(t, a.dir+"/spaces"))
		a.fake.AddRepo("kazuhahub", "taken", false)
		member.do("POST", "/api/spaces", body("kazuhahub", "taken", true)).want(t, 409, "github_name_taken")
		member.do("POST", "/api/spaces", body("nobody-i-know", "x", true)).want(t, 403, "github_owner_not_allowed")
		member.do("POST", "/api/spaces", body("kazuhahub", "bad name", true)).want(t, 400, "invalid")
		member.do("POST", "/api/spaces", map[string]any{"name": "S", "github": map[string]any{"owner": "kazuhahub"}}).want(t, 400, "invalid")
		a.fake.SetAccount("kazuhahub", func(ac *ghAccount) { ac.NoCreate = true })
		nop := member.do("POST", "/api/spaces", body("kazuhahub", "fresh", true)).want(t, 403, "github_no_permission")
		if !strings.Contains(nop.body["message"].(string), "permission") {
			t.Fatalf("%s", nop.raw)
		}
		a.fake.SetAccount("kazuhahub", func(ac *ghAccount) { ac.NoCreate = false })
		// An App without the Contents permission: its own code, naming the permission,
		// and the repository that was made is named too.
		a.fake.AppPerms = map[string]string{"administration": "write", "metadata": "read"}
		mp := member.do("POST", "/api/spaces", body("kazuhahub", "noperm", true)).want(t, 403, "github_app_missing_permission")
		if m, _ := mp.body["missing"].([]any); len(m) != 1 || m[0] != "contents" || mp.body["repo"] == nil {
			t.Fatalf("%s", mp.raw)
		}
		a.fake.AppPerms = map[string]string{"administration": "write", "contents": "write", "metadata": "read"}
		member.do("DELETE", fmt.Sprintf("/api/github/leftovers/%v", mp.body["repo"].(map[string]any)["id"]), nil).want(t, 204, "")
		// A first push that never works: 502 naming the empty repository, no space.
		a.fake.HiddenGit = 100
		r := member.do("POST", "/api/spaces", body("kazuhahub", "slow", true)).want(t, 502, "github_push_failed")
		repo := r.body["repo"].(map[string]any)
		if repo["fullName"] != "kazuhahub/slow" || repo["id"] == nil || repo["htmlUrl"] == "" {
			t.Fatalf("%s", r.raw)
		}
		list := member.do("GET", "/api/spaces", nil).want(t, 200, "")
		for _, s := range list.body["spaces"].([]any) {
			if s.(map[string]any)["name"] == "S" {
				t.Fatalf("a failed creation is listed: %s", list.raw)
			}
		}
		if dirs := listDir(t, a.dir+"/spaces"); len(dirs) != dirsBefore {
			t.Fatalf("%v", dirs)
		}
		// The failure left a reminder the person sees later, with what they need to try again.
		g := member.do("GET", "/api/github", nil).want(t, 200, "")
		lo := g.body["leftovers"].([]any)
		if len(lo) != 1 {
			t.Fatalf("%s", g.raw)
		}
		one := lo[0].(map[string]any)
		if one["repoId"] != repo["id"] || one["fullName"] != "kazuhahub/slow" || one["htmlUrl"] != repo["htmlUrl"] || one["spaceName"] != "S" || one["created"] == "" {
			t.Fatalf("%s", g.raw)
		}
		// The same request with create: false, once GitHub finds it.
		a.fake.HideRepo("kazuhahub/slow", 0)
		member.do("POST", "/api/spaces", body("kazuhahub", "slow", false)).want(t, 201, "")
		// ...and the space connecting to the repository cleared the reminder.
		if g := member.do("GET", "/api/github", nil).want(t, 200, ""); fmt.Sprint(g.body["leftovers"]) != "[]" {
			t.Fatalf("%s", g.raw)
		}
		// GitHub limiting the server: 429 with Retry-After.
		a.fake.LimitUntil(a.clock.Now().Add(2 * time.Minute))
		lim := member.do("POST", "/api/spaces", body("kazuhahub", "another", true)).want(t, 429, "rate_limited")
		if ra := lim.resp.Header.Get("Retry-After"); ra != "120" {
			t.Fatalf("Retry-After %q", ra)
		}
	})
}

func TestMaxSpacesAppliesToSpacesWithRepositories(t *testing.T) {
	o := ghOpts
	o.maxSpaces = 1 // the personal space is the one
	eachAccounts(t, o, func(t *testing.T, a *acc) {
		_, member := ghSetup(t, a)
		before := a.fake.Count("POST", "/orgs/kazuhahub/repos")
		member.do("POST", "/api/spaces", map[string]any{"name": "S", "github": map[string]any{"owner": "kazuhahub", "name": "x"}}).want(t, 403, "quota_exceeded")
		if a.fake.Count("POST", "/orgs/kazuhahub/repos") != before {
			t.Fatal("a repository was made for a refused space")
		}
	})
}

func TestSwitchingASpaceToGitHubThroughTheRemoteEndpoint(t *testing.T) {
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		_, member := ghSetup(t, a)
		repo := a.fake.AddRepo("kazuhahub", "wiki", true)
		sp := member.do("POST", "/api/spaces", map[string]any{"name": "Wiki"}).want(t, 201, "").body["id"].(string)
		put := func(body map[string]any) result { return member.do("PUT", "/api/spaces/"+sp+"/remote", body) }
		cred := func(via string) map[string]any { return map[string]any{"type": "github", "via": via} }
		put(map[string]any{"url": a.fake.URL + "/kazuhahub/wiki.git", "credential": map[string]any{"type": "github"}}).want(t, 400, "invalid")
		put(map[string]any{"url": "https://example.com/a/b.git", "credential": cred("app")}).want(t, 400, "invalid")
		r := put(map[string]any{"url": "git@" + strings.Split(strings.TrimPrefix(a.fake.URL, "http://"), ":")[0] + ":kazuhahub/wiki.git", "credential": cred("app")}).want(t, 200, "")
		c := r.body["credential"].(map[string]any)
		if r.body["url"] != a.fake.URL+"/kazuhahub/wiki.git" || c["type"] != "github" || c["repoId"] != float64(repo.ID) || r.body["state"] != "ok" {
			t.Fatalf("%s", r.raw)
		}
		a.fake.AddOrg("none")
		a.fake.AddRepo("none", "x", true)
		put(map[string]any{"url": a.fake.URL + "/none/x.git", "credential": cred("app")}).want(t, 409, "github_repo_not_in_installation")
		put(map[string]any{"url": a.fake.URL + "/none/x.git", "credential": cred("user")}).want(t, 403, "github_owner_not_allowed")
		// Without a connection.
		other := a.addUser("nobody", pw, "member")
		osp := other.do("POST", "/api/spaces", map[string]any{"name": "O"}).want(t, 201, "").body["id"].(string)
		other.do("PUT", "/api/spaces/"+osp+"/remote", map[string]any{"url": a.fake.URL + "/kazuhahub/wiki.git", "credential": cred("user")}).want(t, 409, "github_not_authorized")
		other.do("PUT", "/api/spaces/"+osp+"/remote", map[string]any{"url": a.fake.URL + "/kazuhahub/wiki.git", "credential": cred("app")}).want(t, 409, "github_not_authorized")
		// Deleting the remote leaves the App and the authorisation as they are.
		member.do("DELETE", "/api/spaces/"+sp+"/remote", nil).want(t, 204, "")
		if g := member.do("GET", "/api/github", nil); g.body["authorization"] == nil {
			t.Fatal("the connection went with the remote")
		}
	})
}

// Manage through a group is manage: a person who may manage a space only
// through a group can switch it to GitHub with their own connection, and when
// they leave the group they can no longer switch it, and their connection
// stops being used for it.
func TestManageThroughAGroupCoversGitHub(t *testing.T) {
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		admin, tang := ghSetup(t, a)
		a.fake.AddRepo("kazuhahub", "team", true)
		ann := a.addUser("ann", pw, "member")
		sp := ann.do("POST", "/api/spaces", map[string]any{"name": "Team"}).want(t, 201, "").body["id"].(string)
		base := "/api/spaces/" + sp
		ann.do("POST", base+"/members", map[string]any{"username": "tang", "role": "editor"}).want(t, 201, "")
		body := map[string]any{"url": a.fake.URL + "/kazuhahub/team.git", "credential": map[string]any{"type": "github", "via": "user"}}

		// A direct editor may not switch the remote.
		tang.do("PUT", base+"/remote", body).want(t, 403, "no_permission")

		// Owner through a group: the remote switches with tang's connection.
		g := admin.mkGroup(t, "Maintainers", "local")
		tangID := mustUserID(t, a, "tang")
		admin.groupAdd(t, g, tangID).want(t, 200, "")
		ann.do("POST", base+"/groups", map[string]any{"groupId": g, "role": "owner"}).want(t, 201, "")
		r := tang.do("PUT", base+"/remote", body).want(t, 200, "")
		if c := r.body["credential"].(map[string]any); c["type"] != "github" || r.body["state"] != "ok" {
			t.Fatalf("%s", r.raw)
		}
		newPage(t, ann, sp, "while tang manages")

		// Out of the group: no switching, and the connection is not used.
		admin.do("DELETE", "/api/admin/groups/"+g+"/members/"+tangID, nil).want(t, 204, "")
		tang.do("PUT", base+"/remote", body).want(t, 403, "no_permission")
		ann.do("POST", base+"/pages", map[string]any{"meta": map[string]any{"title": "after"}, "body": "x"}).want(t, 503, "unavailable")
		rem := ann.do("GET", base+"/remote", nil).want(t, 200, "")
		if rem.body["state"] != "error" || rem.body["errorCode"] != "github_authorization_missing" {
			t.Fatalf("%s", rem.raw)
		}
	})
}

func TestAnExpiredAuthorizationShowsItsCodeAndRefusesWrites(t *testing.T) {
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		_, member := ghSetup(t, a)
		id := member.do("POST", "/api/spaces", map[string]any{"name": "Mine", "github": map[string]any{"owner": "tang-gh", "name": "mine"}}).want(t, 201, "").body["id"].(string)
		page := member.do("POST", "/api/spaces/"+id+"/pages", map[string]any{"meta": map[string]any{"title": "first"}, "body": "one"}).want(t, 201, "").body["id"].(string)
		a.fake.ExpireRefresh("tang-gh")
		a.clock.Advance(9 * time.Hour)
		// The first write finds out: 503, and nothing changed.
		member.do("POST", "/api/spaces/"+id+"/pages", map[string]any{"meta": map[string]any{"title": "second"}, "body": "two"}).want(t, 503, "unavailable")
		rem := member.do("GET", "/api/spaces/"+id+"/remote", nil).want(t, 200, "")
		if rem.body["state"] != "error" || rem.body["errorCode"] != "github_authorization_expired" || rem.body["error"] == "" {
			t.Fatalf("%s", rem.raw)
		}
		if g := member.do("GET", "/api/github", nil); g.body["authorization"].(map[string]any)["state"] != "expired" || g.body["authorization"].(map[string]any)["spaces"] != float64(1) {
			t.Fatalf("%s", g.raw)
		}
		// Reads work, and a draft is kept.
		member.do("GET", "/api/spaces/"+id+"/pages/"+page, nil).want(t, 200, "")
		sp := member.do("GET", "/api/spaces", nil).want(t, 200, "")
		found := false
		for _, s := range sp.body["spaces"].([]any) {
			if s.(map[string]any)["id"] == id && s.(map[string]any)["sync"] == "error" {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s", sp.raw)
		}
		// Reconnecting fixes the space without anything else being done.
		connectGitHub(t, a, member, "tang-gh")
		for i := 0; i < 100; i++ {
			if r := member.do("GET", "/api/spaces/"+id+"/remote", nil); r.body["state"] == "ok" {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		member.do("POST", "/api/spaces/"+id+"/pages", map[string]any{"meta": map[string]any{"title": "third"}, "body": "three"}).want(t, 201, "")
	})
}

func TestRemoteObjectsOfOtherKindsHaveAnErrorCode(t *testing.T) {
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		_, member := ghSetup(t, a)
		id := member.do("POST", "/api/spaces", map[string]any{"name": "Tok"}).want(t, 201, "").body["id"].(string)
		a.fake.AddRepo("kazuhahub", "tok", true)
		ut := a.fake.AccessTokenOf("tang-gh")
		r := member.do("PUT", "/api/spaces/"+id+"/remote", map[string]any{"url": a.fake.URL + "/kazuhahub/tok.git", "credential": map[string]any{"type": "token", "token": ut}}).want(t, 200, "")
		if v, ok := r.body["errorCode"]; !ok || v != "" {
			t.Fatalf("%s", r.raw)
		}
	})
}

// A session held until a passkey is registered reaches none of GitHub: it may
// not connect or disconnect an account for repos, list what it could use, or
// create a space with a repository. An attempt started before the hold does
// not finish under it either.
func TestAHeldSessionCannotUseGitHub(t *testing.T) {
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		admin, member := ghSetup(t, a)
		start := member.do("POST", "/api/github/authorization/start", map[string]any{}).want(t, 200, "")
		back, _ := url.Parse(a.fake.Authorize(start.body["url"].(string), "tang-gh"))
		admin.putSecurity(t, map[string]any{"passkeys": map[string]any{"requireRoles": []string{"member"}}})

		held := a.newClient("")
		held.do("POST", "/api/login", map[string]any{"username": "tang", "password": pw}).want(t, 200, "")
		if s := held.do("GET", "/api/session", nil).want(t, 200, ""); s.body["hold"] != "passkey" {
			t.Fatal(s.raw)
		}
		for k, v := range member.cookies {
			if strings.Contains(k, "petalbook_github") {
				held.cookies[k] = v
			}
		}
		for _, p := range []string{"/api/github", "/api/github/owners", "/api/github/repos?mine=1"} {
			held.do("GET", p, nil).want(t, 403, "passkey_required")
		}
		held.do("POST", "/api/github/authorization/start", map[string]any{}).want(t, 403, "passkey_required")
		held.do("GET", back.Path+"?"+back.RawQuery, nil, "Sec-Fetch-Site", "cross-site").want(t, 403, "passkey_required")
		held.do("DELETE", "/api/github/authorization", nil).want(t, 403, "passkey_required")
		held.do("POST", "/api/spaces", map[string]any{"name": "Tang", "github": map[string]any{
			"owner": "kazuhahub", "name": "tang-notes", "private": true, "create": true}}).want(t, 403, "passkey_required")
		if a.fake.RepoByName("kazuhahub/tang-notes") != nil {
			t.Fatal("a repository was made")
		}
		// The connection made before the hold is untouched.
		if g := member.do("GET", "/api/github", nil).want(t, 200, ""); g.body["authorization"].(map[string]any)["login"] != "tang-gh" {
			t.Fatal(g.raw)
		}
	})
}

func TestDismissingALeftoverRepository(t *testing.T) {
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		_, member := ghSetup(t, a)
		other := a.addUser("other", pw, "member")
		a.fake.HiddenGit = 100
		repoID := func(owner, name string) float64 {
			r := member.do("POST", "/api/spaces", map[string]any{"name": "Left", "github": map[string]any{"owner": owner, "name": name}}).want(t, 502, "github_push_failed")
			return r.body["repo"].(map[string]any)["id"].(float64)
		}
		id := repoID("kazuhahub", "left-a")
		keep := repoID("kazuhahub", "left-b")
		path := func(id float64) string { return "/api/github/leftovers/" + itoa(int64(id)) }
		// Not another person's business.
		other.do("DELETE", path(id), nil).want(t, 204, "")
		if n := len(member.do("GET", "/api/github", nil).want(t, 200, "").body["leftovers"].([]any)); n != 2 {
			t.Fatalf("%d reminders", n)
		}
		member.do("DELETE", path(id), nil).want(t, 204, "")
		member.do("DELETE", path(id), nil).want(t, 204, "") // gone already: still fine
		left := member.do("GET", "/api/github", nil).want(t, 200, "").body["leftovers"].([]any)
		if len(left) != 1 || left[0].(map[string]any)["repoId"] != keep {
			t.Fatalf("%v", left)
		}
		member.do("DELETE", "/api/github/leftovers/nope", nil).want(t, 404, "not_found")
		member.do("DELETE", "/api/github/leftovers/0", nil).want(t, 404, "not_found")
		a.newClient("").do("DELETE", path(keep), nil).want(t, 401, "unauthenticated")
	})
}

func TestLeftoversAreThereWithoutAnApp(t *testing.T) {
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		admin := a.setup("root", pw)
		g := admin.do("GET", "/api/github", nil).want(t, 200, "")
		if fmt.Sprint(g.body["leftovers"]) != "[]" {
			t.Fatalf("%s", g.raw)
		}
	})
}

func TestTheAdministratorsCheckTheAppAgain(t *testing.T) {
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		admin := a.setup("root", pw)
		member := a.addUser("tang", pw, "member")
		member.do("POST", "/api/admin/github/check", nil).want(t, 403, "forbidden")
		admin.do("POST", "/api/admin/github/check", nil).want(t, 409, "github_not_configured")
		a.fake.AppPerms = map[string]string{"contents": "write", "metadata": "read"}
		saved := admin.do("PUT", "/api/admin/github", map[string]any{"appId": "424242", "privateKey": a.fake.KeyPEM()}).want(t, 200, "")
		if m := saved.body["missing"].([]any); len(m) != 1 || m[0] != "administration" {
			t.Fatalf("%s", saved.raw)
		}
		// The permission is given on GitHub; an installation has not accepted it.
		a.fake.AppPerms = map[string]string{"administration": "write", "contents": "write", "metadata": "read"}
		a.fake.AddOrg("kazuhahub")
		inst := a.fake.Install("kazuhahub", "all")
		a.fake.SetInstall(inst, func(i *githubapptest.Install) { i.Perms = map[string]string{"contents": "write", "metadata": "read"} })
		if r := admin.do("GET", "/api/admin/github", nil).want(t, 200, ""); len(r.body["missing"].([]any)) != 1 {
			t.Fatalf("a GET asked GitHub: %s", r.raw)
		}
		r := admin.do("POST", "/api/admin/github/check", nil).want(t, 200, "")
		gaps := r.body["gaps"].([]any)
		if len(r.body["missing"].([]any)) != 0 || len(gaps) != 1 || r.body["anyAccount"] != true {
			t.Fatalf("%s", r.raw)
		}
		g := gaps[0].(map[string]any)
		if g["account"] != "kazuhahub" || g["installationId"] != float64(inst) || g["missing"].([]any)[0] != "administration" {
			t.Fatalf("%s", r.raw)
		}
		if got := admin.do("GET", "/api/admin/github", nil).want(t, 200, ""); len(got.body["gaps"].([]any)) != 1 {
			t.Fatalf("the check was not kept: %s", got.raw)
		}
	})
}

// A person connects by installing the App on their own account in one GitHub
// flow; without the App there, a repository in their account answers what to do.
func TestConnectingByInstallingAndThePersonalAccountNeedsTheApp(t *testing.T) {
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		admin := a.setup("root", pw)
		admin.do("PUT", "/api/admin/github", map[string]any{"appId": "424242", "privateKey": a.fake.KeyPEM(),
			"clientId": a.fake.ClientID, "clientSecret": a.fake.ClientSecret}).want(t, 200, "")
		a.fake.PersonalNeedsInstall = true
		member := a.addUser("tang", pw, "member")
		a.fake.AddUser("tang-gh")
		r := member.do("POST", "/api/github/authorization/start", map[string]any{"return": "/settings/github", "install": true}).want(t, 200, "")
		installURL := r.body["url"].(string)
		if !strings.Contains(installURL, "/installations/new?state=") {
			t.Fatalf("%s", installURL)
		}
		back, _ := url.Parse(a.fake.InstallApp(installURL, "tang-gh", "https://notes.example.test/api/github/authorization/callback"))
		cb := member.do("GET", back.Path+"?"+back.RawQuery, nil, "Sec-Fetch-Site", "cross-site")
		if cb.code != 303 || cb.resp.Header.Get("Location") != "/settings/github" {
			t.Fatalf("the callback answered %d %q", cb.code, cb.resp.Header.Get("Location"))
		}
		g := member.do("GET", "/api/github", nil).want(t, 200, "")
		if g.body["authorization"].(map[string]any)["login"] != "tang-gh" {
			t.Fatalf("%s", g.raw)
		}
		// It was installed in the same flow: a repository in the account is made.
		member.do("POST", "/api/spaces", map[string]any{"name": "Mine", "github": map[string]any{"owner": "tang-gh", "name": "mine"}}).want(t, 201, "")

		// Another person authorises without installing: the owners say so and creating is refused with the way out.
		other := a.addUser("kai", pw, "member")
		a.fake.AddUser("kai-gh")
		connectGitHub(t, a, other, "kai-gh")
		o := other.do("GET", "/api/github/owners", nil).want(t, 200, "")
		first := o.body["items"].([]any)[0].(map[string]any)
		if first["login"] != "kai-gh" || first["needsInstall"] != true {
			t.Fatalf("%s", o.raw)
		}
		e := other.do("POST", "/api/spaces", map[string]any{"name": "Kai", "github": map[string]any{"owner": "kai-gh", "name": "kai"}}).want(t, 409, "github_app_not_installed")
		if e.body["installUrl"] != "https://github.com/apps/petalbook-notes/installations/new" || strings.Contains(e.raw, "integration") {
			t.Fatalf("%s", e.raw)
		}
		if m := member.do("GET", "/api/github/owners", nil).want(t, 200, ""); m.body["items"].([]any)[0].(map[string]any)["needsInstall"] != false {
			t.Fatalf("%s", m.raw)
		}
	})
}

// The administrator makes the App on GitHub in one click: the server hands
// over the manifest, GitHub sends the browser back, the server saves the App.
func TestMakingTheAppFromAManifestThroughTheAPI(t *testing.T) {
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		admin := a.setup("root", pw)
		member := a.addUser("tang", pw, "member")
		member.do("POST", "/api/admin/github/manifest", map[string]any{}).want(t, 403, "forbidden")
		admin.do("POST", "/api/admin/github/manifest", map[string]any{"organization": "a/b"}).want(t, 400, "invalid")
		r := admin.do("POST", "/api/admin/github/manifest", map[string]any{"organization": "kazuhahub", "name": "Team notes"}).want(t, 200, "")
		postURL := r.body["url"].(string)
		m := r.body["manifest"].(map[string]any)
		if !strings.Contains(postURL, "/organizations/kazuhahub/settings/apps/new?state=") || m["name"] != "Team notes" || m["public"] != true ||
			m["request_oauth_on_install"] != true || m["redirect_url"] != "https://notes.example.test/api/github/manifest/callback" ||
			m["hook_attributes"].(map[string]any)["active"] != false {
			t.Fatalf("%s", r.raw)
		}
		manifest, _ := json.Marshal(m)
		back, _ := url.Parse(a.fake.SubmitManifest(postURL, string(manifest)))
		path := back.Path + "?" + back.RawQuery
		// Someone else's browser (no cookie, or a member's session) finishes nothing.
		if cb := member.do("GET", path, nil, "Sec-Fetch-Site", "cross-site"); cb.code != 303 || cb.resp.Header.Get("Location") != "/admin/github?github_app_error=expired" {
			t.Fatalf("%d %q", cb.code, cb.resp.Header.Get("Location"))
		}
		if admin.do("GET", "/api/admin/github", nil).body["configured"] != false {
			t.Fatal("a refused return configured the App")
		}
		cb := admin.do("GET", path, nil, "Sec-Fetch-Site", "cross-site")
		if cb.code != 303 || cb.resp.Header.Get("Location") != "/admin/github?github_app=created" {
			t.Fatalf("%d %q", cb.code, cb.resp.Header.Get("Location"))
		}
		got := admin.do("GET", "/api/admin/github", nil).want(t, 200, "")
		if got.body["configured"] != true || got.body["appId"] != "424242" || got.body["userAuthorization"] != true || got.body["clientSecret"].(map[string]any)["set"] != true {
			t.Fatalf("%s", got.raw)
		}
		for _, secret := range []string{a.fake.ClientSecret, "PRIVATE KEY", "fake-webhook-secret"} {
			if strings.Contains(got.raw, secret) || strings.Contains(r.raw, secret) {
				t.Fatalf("a secret is in a response: %q", secret)
			}
		}
		// The state worked once.
		if cb := admin.do("GET", path, nil, "Sec-Fetch-Site", "cross-site"); cb.resp.Header.Get("Location") != "/admin/github?github_app_error=expired" {
			t.Fatalf("a replay: %q", cb.resp.Header.Get("Location"))
		}
	})
}
