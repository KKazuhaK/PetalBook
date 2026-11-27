package api_test

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

// installGitHub plays the browser of c through installing the App: start is
// the API's answer (POST .../install), login installs it on the GitHub
// account on. It answers where the callback sends the browser.
func installGitHub(t *testing.T, a *acc, c *client, path string, body map[string]any, login, on string) string {
	t.Helper()
	r := c.do("POST", path, body).want(t, 200, "")
	back, _ := url.Parse(a.fake.InstallAppOn(r.body["url"].(string), login, on, "https://notes.example.test/api/account/links/github/callback", true))
	cb := c.do("GET", back.Path+"?"+back.RawQuery, nil, "Sec-Fetch-Site", "cross-site")
	if cb.code != 303 {
		t.Fatalf("the callback answered %d %s", cb.code, cb.raw)
	}
	return cb.resp.Header.Get("Location")
}

// A tenant administrator B2, invited by B's first administrator.
func secondAdminB(t *testing.T, a *acc, f *tenFx) *client {
	t.Helper()
	inv := f.admB.do("POST", "/api/invites", map[string]any{"role": "admin"}).want(t, 201, "")
	c := a.newClient("")
	c.do("POST", "/api/invites/accept", map[string]any{"token": inv.body["token"], "username": "adm-b2", "name": "Admin B2", "password": pw}).want(t, 201, "")
	return c
}

func items(t *testing.T, r result) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, it := range r.body["items"].([]any) {
		out = append(out, it.(map[string]any))
	}
	return out
}

// Anyone installs the App on their own GitHub account from their settings; it
// is theirs alone, and their own space syncs through it.
func TestAPersonInstallsTheAppOnTheirOwnAccount(t *testing.T) {
	t.Parallel()
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		f := ghTenFx(t, a)
		a.fake.AddUser("mem-a-gh")
		a.fake.AddRepo("mem-a-gh", "notes", true)
		to := installGitHub(t, a, f.memA, "/api/account/github/install", map[string]any{}, "mem-a-gh", "mem-a-gh")
		if to != "/settings/connections" {
			t.Fatalf("%q", to)
		}
		list := items(t, f.memA.do("GET", "/api/account/github/installations", nil).want(t, 200, ""))
		if len(list) != 1 || list[0]["account"] != "mem-a-gh" || list[0]["accountType"] != "User" || list[0]["owner"].(map[string]any)["kind"] != "person" {
			t.Fatalf("%v", list)
		}
		id := itoa(int64(list[0]["id"].(float64)))
		repos := items(t, f.memA.do("GET", "/api/account/github/installations/"+id+"/repos", nil).want(t, 200, ""))
		if len(repos) != 1 || repos[0]["fullName"] != "mem-a-gh/notes" {
			t.Fatalf("%v", repos)
		}
		// Theirs alone: another person, and an organisation's administrator, see nothing of it.
		for _, c := range []*client{f.memB, f.admB} {
			if l := items(t, c.do("GET", "/api/account/github/installations", nil).want(t, 200, "")); len(l) != 0 {
				t.Fatalf("%v", l)
			}
			c.do("GET", "/api/account/github/installations/"+id+"/spaces", nil).want(t, 404, "not_found")
			c.do("DELETE", "/api/account/github/installations/"+id, nil).want(t, 404, "not_found")
		}
		f.admB.do("DELETE", "/api/admin/github/installations/"+id, nil).want(t, 404, "not_found")
		// Their space syncs through it.
		r := f.memA.do("POST", "/api/spaces", map[string]any{"name": "Mine", "storage": ghSync(map[string]any{"owner": "mem-a-gh", "name": "notes", "create": false})}).want(t, 201, "")
		rem := f.memA.do("GET", "/api/spaces/"+r.body["id"].(string)+"/remote", nil).want(t, 200, "")
		if rem.body["credential"].(map[string]any)["via"] != "app" {
			t.Fatalf("%s", rem.raw)
		}
		sp := f.memA.do("GET", "/api/account/github/installations/"+id+"/spaces", nil).want(t, 200, "")
		if sp.body["total"] != float64(1) {
			t.Fatalf("%s", sp.raw)
		}
		d := f.memA.do("DELETE", "/api/account/github/installations/"+id, nil).want(t, 200, "")
		if d.body["spaces"] != float64(1) {
			t.Fatalf("%s", d.raw)
		}
		audit := f.root.do("GET", "/api/admin/audit?kind=github_installation_connected", nil).want(t, 200, "")
		if !strings.Contains(audit.raw, "mem-a-gh") {
			t.Fatalf("the connection was not audited: %s", audit.raw)
		}
	})
}

// A tenant's administrator installs the App on the GitHub organisation from the
// organisation's console: it is the tenant's, any of its administrators manages
// and disconnects it, its members' spaces sync through it, and nobody else's do.
func TestATenantAdministratorInstallsTheAppForTheOrganisation(t *testing.T) {
	t.Parallel()
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		f := ghTenFx(t, a)
		a.fake.AddUser("adm-b-gh")
		a.fake.SetAccount("acme-gh", func(x *ghAccount) { x.Members["adm-b-gh"] = true })
		// A member who is not the tenant's administrator may not.
		f.memB.do("POST", "/api/admin/github/install", map[string]any{"account": "acme-gh"}).want(t, 403, "forbidden")
		f.memB.do("GET", "/api/admin/github/installations", nil).want(t, 403, "forbidden")
		// Nor for the default tenant, which is no organisation.
		f.admA.do("POST", "/api/admin/github/install", map[string]any{"account": "acme-gh"}).want(t, 403, "forbidden")
		to := installGitHub(t, a, f.admB, "/api/admin/github/install", map[string]any{"account": "acme-gh"}, "adm-b-gh", "acme-gh")
		if to != "/admin/storage/github" {
			t.Fatalf("%q", to)
		}
		b2 := secondAdminB(t, a, f)
		list := items(t, b2.do("GET", "/api/admin/github/installations", nil).want(t, 200, ""))
		if len(list) != 1 || list[0]["account"] != "acme-gh" || list[0]["owner"].(map[string]any)["kind"] != "organization" || list[0]["owner"].(map[string]any)["id"] != f.tenB {
			t.Fatalf("%v", list)
		}
		id := itoa(int64(list[0]["id"].(float64)))
		// Its member, who has their own GitHub link too, makes a space with it.
		r := f.memB.do("POST", "/api/spaces", map[string]any{"name": "Acme notes", "storage": ghSync(map[string]any{"owner": "acme-gh", "name": "notes"})}).want(t, 201, "")
		spaceID := r.body["id"].(string)
		rem := f.memB.do("GET", "/api/spaces/"+spaceID+"/remote", nil).want(t, 200, "")
		if rem.body["credential"].(map[string]any)["via"] != "app" {
			t.Fatalf("%s", rem.raw)
		}
		// A person of another tenant, a member of acme-gh on GitHub, never syncs
		// through it: their own space in acme-gh goes through their own link.
		f.memA.do("POST", "/api/spaces", map[string]any{"name": "Theirs", "storage": ghSync(map[string]any{"owner": "acme-gh", "name": "x"})}).want(t, 409, "not_connected")
		a.fake.AddUser("mem-a-gh")
		a.fake.SetAccount("acme-gh", func(x *ghAccount) { x.Members["mem-a-gh"] = true })
		connectGitHub(t, a, f.memA, "mem-a-gh")
		theirs := f.memA.do("POST", "/api/spaces", map[string]any{"name": "Theirs", "storage": ghSync(map[string]any{"owner": "acme-gh", "name": "x"})}).want(t, 201, "")
		trem := f.memA.do("GET", "/api/spaces/"+theirs.body["id"].(string)+"/remote", nil).want(t, 200, "")
		if c := trem.body["credential"].(map[string]any); c["via"] != "user" || c["installationId"] != nil {
			t.Fatalf("%s", trem.raw)
		}
		// The other administrator sees what disconnecting affects, then disconnects it.
		sp := b2.do("GET", "/api/admin/github/installations/"+id+"/spaces", nil).want(t, 200, "")
		if sp.body["total"] != float64(1) || items(t, sp)[0]["name"] != "Acme notes" {
			t.Fatalf("%s", sp.raw)
		}
		b2.do("GET", "/api/admin/github/installations/"+id+"/repos", nil).want(t, 200, "")
		f.memB.do("DELETE", "/api/admin/github/installations/"+id, nil).want(t, 403, "forbidden")
		b2.do("DELETE", "/api/admin/github/installations/"+id, nil).want(t, 200, "")
		// The space shows it lost access (it is fetched again at once, in the background).
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			rem = f.memB.do("GET", "/api/spaces/"+spaceID+"/remote", nil).want(t, 200, "")
			if rem.body["errorCode"] == "access_lost" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s", rem.raw)
			}
		}
		b2.do("DELETE", "/api/admin/github/installations/"+id, nil).want(t, 404, "not_found")
	})
}

// Another owner's claim of a connected installation is refused, with a clear
// answer on the page returned to.
func TestASecondOwnerCannotClaimAnInstallation(t *testing.T) {
	t.Parallel()
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		f := ghTenFx(t, a)
		a.fake.AddUser("adm-b-gh")
		a.fake.SetAccount("acme-gh", func(x *ghAccount) { x.Members["adm-b-gh"] = true })
		installGitHub(t, a, f.admB, "/api/admin/github/install", map[string]any{"account": "acme-gh"}, "adm-b-gh", "acme-gh")
		// The site's own administrator in tenant C's scope is the next to try.
		c := f.root.do("POST", "/api/admin/tenants", map[string]any{"slug": "third", "name": "Third", "admin": map[string]any{"username": "adm-c"}}).want(t, 201, "")
		inv := c.body["invite"].(map[string]any)
		admC := a.newClient("")
		admC.do("POST", "/api/invites/accept", map[string]any{"token": inv["token"], "username": "adm-c", "name": "Admin C", "password": pw}).want(t, 201, "")
		a.fake.AddUser("adm-c-gh")
		a.fake.SetAccount("acme-gh", func(x *ghAccount) { x.Members["adm-c-gh"] = true })
		to := installGitHub(t, a, admC, "/api/admin/github/install", map[string]any{"account": "acme-gh"}, "adm-c-gh", "acme-gh")
		if to != "/admin/storage/github?connection_error=installation_taken" {
			t.Fatalf("%q", to)
		}
		if l := items(t, admC.do("GET", "/api/admin/github/installations", nil).want(t, 200, "")); len(l) != 0 {
			t.Fatalf("%v", l)
		}
	})
}

// The site's switch: off for the site, nobody may install; off for one tenant
// by a rule, that tenant's administrators may not.
func TestTheSiteSwitchTurnsGitHubInstallsOff(t *testing.T) {
	t.Parallel()
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		f := ghTenFx(t, a)
		f.root.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/features.githubConnect", map[string]any{"siteLock": true, "value": false}).want(t, 200, "")
		r := f.admB.do("POST", "/api/admin/github/install", map[string]any{"account": "acme-gh"}).want(t, 403, "feature_off")
		if r.body["feature"] != "features.githubConnect" {
			t.Fatalf("%s", r.raw)
		}
		f.memB.do("POST", "/api/account/github/install", map[string]any{}).want(t, 403, "feature_off")
		f.memA.do("POST", "/api/account/github/install", map[string]any{}).want(t, 200, "")
		f.root.putSetting("features.githubConnect", false).want(t, 200, "")
		f.memA.do("POST", "/api/account/github/install", map[string]any{}).want(t, 403, "feature_off")
		// The install page of "Connect GitHub" is refused the same way.
		f.memA.do("POST", "/api/account/links/github/start", map[string]any{"purpose": "repos", "install": true}).want(t, 403, "feature_off")
		// Listing and disconnecting what is there stays possible.
		f.memA.do("GET", "/api/account/github/installations", nil).want(t, 200, "")
		f.admB.do("GET", "/api/admin/github/installations", nil).want(t, 200, "")
	})
}
