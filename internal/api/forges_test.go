package api_test

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"petalbook.app/server/internal/forge/forgetest"
	"petalbook.app/server/internal/store"
)

// Milestone 3.8 through the API: GitLab and Gitea (and so Forgejo and
// Codeberg) providers an administrator sets up, and people who connect to them.
// Everything runs against the fake hosts in forgetest, on both platforms.

var fgOpts = accOpts{forges: true}

var forgeKinds = []string{"gitlab", "gitea"}

// eachForge runs a test for each platform on every database.
func eachForge(t *testing.T, fn func(t *testing.T, a *acc, kind string, f *forgetest.Fake)) {
	t.Helper()
	for _, kind := range forgeKinds {
		t.Run(kind, func(t *testing.T) {
			eachAccounts(t, fgOpts, func(t *testing.T, a *acc) {
				f := a.gl
				if kind == "gitea" {
					f = a.gt
				}
				fn(t, a, kind, f)
			})
		})
	}
}

func fgBody(kind string, f *forgetest.Fake, extra map[string]any) map[string]any {
	m := map[string]any{"id": kind, "type": kind, "host": f.HostURL(), "name": "Acme " + kind, "clientId": f.ClientID, "clientSecret": f.ClientSecret}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// fgSetup makes an administrator who has set the provider up, and a member
// "tang" who has connected the account "tang-h", an owner of the group "acme".
func fgSetup(t *testing.T, a *acc, kind string, f *forgetest.Fake) (admin, member *client) {
	t.Helper()
	admin = a.setup("root", pw)
	admin.do("POST", "/api/admin/providers", fgBody(kind, f, nil)).want(t, 201, "")
	member = a.addUser("tang", pw, "member")
	f.AddUser("tang-h")
	f.AddGroup("acme", map[string]int{"tang-h": forgetest.Owner})
	connectForge(t, a, member, kind, f, "tang-h")
	return admin, member
}

// connectForge plays the browser through the whole authorisation.
func connectForge(t *testing.T, a *acc, c *client, kind string, f *forgetest.Fake, login string) {
	t.Helper()
	r := c.do("POST", "/api/account/links/"+kind+"/start", map[string]any{"purpose": "repos", "return": "/s/ABC/settings"}).want(t, 200, "")
	back, _ := url.Parse(f.Authorize(r.body["url"].(string), login))
	cb := c.do("GET", back.Path+"?"+back.RawQuery, nil, "Sec-Fetch-Site", "cross-site")
	if cb.code != 303 || cb.resp.Header.Get("Location") != "/s/ABC/settings" {
		t.Fatalf("the callback answered %d %q", cb.code, cb.resp.Header.Get("Location"))
	}
}

func fgSync(kind string, fields map[string]any) map[string]any {
	f := map[string]any{"provider": kind}
	for k, v := range fields {
		f[k] = v
	}
	return map[string]any{"sync": f}
}

// fgProvider is the provider's entry in GET /api/account/links.
func fgProvider(t *testing.T, c *client, id string) map[string]any {
	t.Helper()
	r := c.do("GET", "/api/account/links", nil).want(t, 200, "")
	for _, p := range r.body["providers"].([]any) {
		if m := p.(map[string]any); m["id"] == id {
			return m
		}
	}
	t.Fatalf("no provider %s in %s", id, r.raw)
	return nil
}

func TestAnAdministratorSetsAHostUp(t *testing.T) {
	t.Parallel()
	eachForge(t, func(t *testing.T, a *acc, kind string, f *forgetest.Fake) {
		admin := a.setup("root", pw)
		member := a.addUser("tang", pw, "member")
		// Only a site administrator.
		member.do("POST", "/api/admin/providers", fgBody(kind, f, nil)).want(t, 403, "forbidden")
		member.do("PATCH", "/api/admin/providers/x", map[string]any{}).want(t, 403, "forbidden")

		for _, bad := range []map[string]any{{"id": "GitHub"}, {"id": "github"}, {"type": "bitbucket"}, {"host": "a/b"}, {"clientSecret": "nope"}, {"clientId": "nope"}} {
			r := admin.do("POST", "/api/admin/providers", fgBody(kind, f, bad)).want(t, 400, "invalid")
			if bad["clientSecret"] != nil || bad["clientId"] != nil {
				if r.body["reason"] != "client_rejected" {
					t.Fatalf("%s", r.raw)
				}
			}
		}
		r := admin.do("POST", "/api/admin/providers", fgBody(kind, f, nil)).want(t, 201, "")
		if r.resp.Header.Get("Location") != "/api/admin/providers/"+kind {
			t.Fatalf("%q", r.resp.Header.Get("Location"))
		}
		for _, v := range []string{"id", "type", "host", "name", "clientId", "hasSecret", "callbackUrl", "scopes", "configured", "version", "partialSync", "connections", "spaces"} {
			if _, ok := r.body[v]; !ok {
				t.Fatalf("no %s in %s", v, r.raw)
			}
		}
		if r.body["hasSecret"] != true || r.body["callbackUrl"] != "https://notes.example.test/api/account/links/"+kind+"/callback" ||
			r.body["configured"] != true || r.body["owner"] != "site" || r.body["partialSync"] != "unknown" {
			t.Fatalf("%s", r.raw)
		}
		got := admin.do("GET", "/api/admin/providers/"+kind, nil).want(t, 200, "")
		for _, res := range []result{r, got, admin.do("GET", "/api/admin/providers", nil).want(t, 200, "")} {
			if strings.Contains(res.raw, f.ClientSecret) {
				t.Fatalf("the secret is shown: %s", res.raw)
			}
		}
		// A second one on the same host, and the same id again.
		admin.do("POST", "/api/admin/providers", fgBody(kind, f, map[string]any{"id": "again"})).want(t, 409, "exists")
		// The list for administrators has it with its counts and partial sync.
		var row map[string]any
		for _, p := range admin.do("GET", "/api/admin/providers", nil).body["providers"].([]any) {
			if m := p.(map[string]any); m["id"] == kind {
				row = m
			}
		}
		if row == nil || row["type"] != kind || row["configured"] != true || row["partialSync"] != "unknown" || row["connections"] != float64(0) || row["spaces"] != float64(0) {
			t.Fatalf("%v", row)
		}
		// Changing it; the secret is checked with the host before it is kept.
		admin.do("PATCH", "/api/admin/providers/"+kind, map[string]any{"name": "Renamed"}).want(t, 200, "")
		admin.do("PATCH", "/api/admin/providers/"+kind, map[string]any{"clientSecret": "nope"}).want(t, 400, "invalid")
		admin.do("PATCH", "/api/admin/providers/"+kind, map[string]any{"host": "elsewhere.example.com"}).want(t, 400, "invalid")
		admin.do("PUT", "/api/admin/providers/"+kind, map[string]any{"clientSecret": f.ClientSecret}).want(t, 200, "")
		if p := fgProvider(t, member, kind); p["name"] != "Renamed" {
			t.Fatalf("%v", p)
		}
		admin.do("POST", "/api/admin/providers/"+kind+"/check", nil).want(t, 200, "")
		f.NotThisPlatform = true
		admin.do("POST", "/api/admin/providers/"+kind+"/check", nil).want(t, 400, "invalid")
		if p := fgProvider(t, member, kind); p["configured"] != false {
			t.Fatalf("a provider whose host fails its check is configured: %v", p)
		}
		member.do("POST", "/api/account/links/"+kind+"/start", map[string]any{"purpose": "repos"}).want(t, 409, "provider_not_configured")
		f.NotThisPlatform = false
		admin.do("POST", "/api/admin/providers/"+kind+"/check", nil).want(t, 200, "")
		// No installations, no manifest: those are GitHub's.
		admin.do("GET", "/api/admin/providers/"+kind+"/installations", nil).want(t, 404, "not_found")
		admin.do("POST", "/api/admin/providers/"+kind+"/manifest", nil).want(t, 404, "not_found")
		// Removed.
		admin.do("DELETE", "/api/admin/providers/"+kind, nil).want(t, 204, "")
		admin.do("GET", "/api/admin/providers/"+kind, nil).want(t, 404, "not_found")
		// The audit has each change and no secret.
		a.auth.FlushAudit(context.Background())
		rows := auditKinds(t, admin, "connection_provider_changed")
		if len(rows) < 4 || strings.Contains(fmt.Sprint(rows), f.ClientSecret) {
			t.Fatalf("%v", rows)
		}
	})
}

func TestTheStorageHostsDecideWhichHostsAProviderMayBeOn(t *testing.T) {
	t.Parallel()
	eachForge(t, func(t *testing.T, a *acc, kind string, f *forgetest.Fake) {
		admin := a.setup("root", pw)
		admin.do("PUT", "/api/admin/settings/storage.hosts", map[string]any{"value": []string{"git.example.test"}}).want(t, 200, "")
		r := admin.do("POST", "/api/admin/providers", fgBody(kind, f, nil))
		if r.code < 400 || f.Count("GET", "/api/") != 0 {
			t.Fatalf("a host that is not allowed was accepted or asked: %d %s", r.code, r.raw)
		}
		admin.do("PUT", "/api/admin/settings/storage.hosts", map[string]any{"value": []string{"*"}}).want(t, 200, "")
		admin.do("POST", "/api/admin/providers", fgBody(kind, f, nil)).want(t, 201, "")
	})
}

func TestProvidersAreListedByCapability(t *testing.T) {
	t.Parallel()
	eachForge(t, func(t *testing.T, a *acc, kind string, f *forgetest.Fake) {
		_, member := fgSetup(t, a, kind, f)
		p := fgProvider(t, member, kind)
		caps := p["capabilities"].(map[string]any)
		if p["type"] != kind || p["host"] != f.Host() || p["owner"] != "site" || p["configured"] != true || p["userAuthorization"] != true ||
			caps["createRepo"] != true || caps["installations"] != false || caps["connectAccount"] != true || caps["storageTarget"] != true || caps["partialSync"] != "unknown" || p["app"] != nil {
			t.Fatalf("%v", p)
		}
		conn := p["connection"].(map[string]any)
		if conn["login"] != "tang-h" || conn["state"] != "ok" || conn["spaces"] != float64(0) || conn["accountId"] == "" {
			t.Fatalf("%v", conn)
		}
		if p["leftovers"] == nil {
			t.Fatal("leftovers is always there")
		}
		// GitHub (when there is one), the hosts, then plain Git, in a stable order.
		var ids []string
		for _, q := range member.do("GET", "/api/account/links", nil).body["providers"].([]any) {
			ids = append(ids, q.(map[string]any)["id"].(string))
		}
		if strings.Join(ids, ",") != kind+",git" {
			t.Fatalf("%v", ids)
		}
		// Their routes: installations are GitHub's.
		member.do("GET", "/api/account/links/"+kind+"/repos?installation=1", nil).want(t, 400, "invalid")
		// Whatever it says: a host has no installations to name.
		for _, v := range []string{"x", "0", "-3", ""} {
			member.do("GET", "/api/account/links/"+kind+"/repos?installation="+v, nil).want(t, 400, "invalid")
		}
		member.do("GET", "/api/account/links/"+kind+"/repos?mine=true", nil).want(t, 200, "")
	})
}

func TestConnectingAnAccountOnAHostThroughTheApi(t *testing.T) {
	t.Parallel()
	eachForge(t, func(t *testing.T, a *acc, kind string, f *forgetest.Fake) {
		admin, member := fgSetup(t, a, kind, f)
		a.auth.FlushAudit(context.Background())
		if rows := auditKinds(t, admin, "account_connected"); len(rows) != 1 || !strings.Contains(fmt.Sprint(rows[0]), kind) || !strings.Contains(fmt.Sprint(rows[0]), "tang-h") {
			t.Fatalf("%v", rows)
		}
		// Not signed in, a return that is not ours, the wrong cookie.
		stranger := a.newClient("")
		r := member.do("POST", "/api/account/links/"+kind+"/start", map[string]any{"purpose": "repos"}).want(t, 200, "")
		back, _ := url.Parse(f.Authorize(r.body["url"].(string), "tang-h"))
		cb := stranger.do("GET", back.Path+"?"+back.RawQuery, nil, "Sec-Fetch-Site", "cross-site")
		if cb.code != 303 || !strings.Contains(cb.resp.Header.Get("Location"), "connection_error=expired") {
			t.Fatalf("%d %q", cb.code, cb.resp.Header.Get("Location"))
		}
		other := a.addUser("wu", pw, "member")
		other.cookies = member.cookies
		// Another browser carrying the cookie but not the session is no use either.
		// The owner's own return still works.
		cb = member.do("GET", back.Path+"?"+back.RawQuery, nil, "Sec-Fetch-Site", "cross-site")
		if cb.code != 303 || strings.Contains(cb.resp.Header.Get("Location"), "connection_error") {
			t.Fatalf("%d %q", cb.code, cb.resp.Header.Get("Location"))
		}
		// One account links to one person.
		wu := a.addUser("wu2", pw, "member")
		rr := wu.do("POST", "/api/account/links/"+kind+"/start", map[string]any{"purpose": "repos"}).want(t, 200, "")
		back2, _ := url.Parse(f.Authorize(rr.body["url"].(string), "tang-h"))
		cb = wu.do("GET", back2.Path+"?"+back2.RawQuery, nil, "Sec-Fetch-Site", "cross-site")
		if !strings.Contains(cb.resp.Header.Get("Location"), "connection_error=account_in_use") {
			t.Fatalf("%q", cb.resp.Header.Get("Location"))
		}
		// Disconnecting; a second time there is no link left.
		gone := linkAt(t, member, kind)["id"].(string)
		disconnectRepos(t, member, kind)
		if p := fgProvider(t, member, kind); p["connection"] != nil {
			t.Fatalf("%v", p)
		}
		member.do("DELETE", "/api/account/links/"+gone+"/repos", nil).want(t, 404, "not_found")
		a.auth.FlushAudit(context.Background())
		if rows := auditKinds(t, admin, "account_disconnected"); len(rows) != 1 {
			t.Fatalf("%v", rows)
		}
		// Without a connection the lists are not_connected, and say which provider.
		r2 := member.do("GET", "/api/account/links/"+kind+"/owners", nil).want(t, 409, "not_connected")
		if r2.body["provider"] != kind {
			t.Fatalf("%s", r2.raw)
		}
	})
}

func TestAGiteaAdministratorsConnectionSaysItsTokenCarriesAdminRights(t *testing.T) {
	t.Parallel()
	eachForge(t, func(t *testing.T, a *acc, kind string, f *forgetest.Fake) {
		_, member := fgSetup(t, a, kind, f)
		if c := fgProvider(t, member, kind)["connection"].(map[string]any); c["admin"] == true {
			t.Fatalf("%v", c)
		}
		boss := a.addUser("boss", pw, "member")
		f.AddUser("boss-h")
		f.SetAdmin("boss-h", true)
		connectForge(t, a, boss, kind, f, "boss-h")
		c := fgProvider(t, boss, kind)["connection"].(map[string]any)
		if (c["admin"] == true) != (kind == "gitea") {
			t.Fatalf("%v", c)
		}
	})
}

func TestOwnersAndRepositoriesThroughTheApi(t *testing.T) {
	t.Parallel()
	eachForge(t, func(t *testing.T, a *acc, kind string, f *forgetest.Fake) {
		_, member := fgSetup(t, a, kind, f)
		f.AddUser("boss")
		f.AddRepo("tang-h", "mine", true)
		f.AddRepo("acme", "team-notes", true)
		f.AddRepo("boss", "theirs", true)
		r := member.do("GET", "/api/account/links/"+kind+"/owners", nil).want(t, 200, "")
		items := r.body["items"].([]any)
		if len(items) != 2 || items[0].(map[string]any)["login"] != "tang-h" || items[0].(map[string]any)["type"] != "User" || items[1].(map[string]any)["login"] != "acme" ||
			items[0].(map[string]any)["installationId"] != nil || items[0].(map[string]any)["needsInstall"] != false || r.body["next"] != "" {
			t.Fatalf("%s", r.raw)
		}
		if q := member.do("GET", "/api/account/links/"+kind+"/owners?q=ac", nil).want(t, 200, "").body["items"].([]any); len(q) != 1 {
			t.Fatalf("%v", q)
		}
		rr := member.do("GET", "/api/account/links/"+kind+"/repos", nil).want(t, 200, "")
		if got := len(rr.body["items"].([]any)); got != 2 {
			t.Fatalf("%s", rr.raw)
		}
		first := rr.body["items"].([]any)[0].(map[string]any)
		if first["canPush"] != true || first["htmlUrl"] == "" || first["fullName"] == "" || first["id"] == nil {
			t.Fatalf("%v", first)
		}
		if q := member.do("GET", "/api/account/links/"+kind+"/repos?mine=true&q=team", nil).want(t, 200, "").body["items"].([]any); len(q) != 1 {
			t.Fatalf("%v", q)
		}
		for _, bad := range []string{"owners?cursor=zz", "repos?cursor=-1"} {
			member.do("GET", "/api/account/links/"+kind+"/"+bad, nil).want(t, 400, "invalid")
		}
	})
}

func TestCreatingASpaceWithANewRepositoryOnAHost(t *testing.T) {
	t.Parallel()
	eachForge(t, func(t *testing.T, a *acc, kind string, f *forgetest.Fake) {
		admin, member := fgSetup(t, a, kind, f)
		r := member.do("POST", "/api/spaces", map[string]any{"name": "Team", "storage": fgSync(kind, map[string]any{
			"owner": "acme", "name": "team-notes", "private": true, "description": "Notes of the team", "create": true})}).want(t, 201, "")
		id := r.body["id"].(string)
		if r.body["sync"] != "ok" || r.resp.Header.Get("Location") != "/api/spaces/"+id {
			t.Fatalf("%s", r.raw)
		}
		repo := f.RepoByName("acme/team-notes")
		if repo == nil || !repo.Private || repo.Desc != "Notes of the team" {
			t.Fatalf("%+v", repo)
		}
		rem := member.do("GET", "/api/spaces/"+id+"/remote", nil).want(t, 200, "")
		cred := rem.body["credential"].(map[string]any)
		u := cred["user"].(map[string]any)
		if rem.body["url"] != f.URL+"/acme/team-notes.git" || rem.body["state"] != "ok" || rem.body["provider"] != kind || rem.body["errorCode"] != "" ||
			cred["type"] != "connection" || cred["via"] != "user" || cred["account"] != "acme" || cred["login"] != "tang-h" || u["username"] != "tang" ||
			cred["repoId"] != float64(repo.ID) || cred["installationId"] != nil {
			t.Fatalf("%s", rem.raw)
		}
		// Git was given the host's own user name and the person's token, and nothing else of theirs.
		if n := gitOut(t, f.BareDir("acme/team-notes"), "rev-list", "--count", "main"); n != "1" {
			t.Fatalf("%s", n)
		}
		for _, u := range f.GitUsers() {
			if u != "oauth2" {
				t.Fatalf("Git was given the user name %q", u)
			}
		}
		member.do("POST", "/api/spaces/"+id+"/pages", map[string]any{"meta": map[string]any{"title": "x"}, "body": "hello"}).want(t, 201, "")
		if n := gitOut(t, f.BareDir("acme/team-notes"), "rev-list", "--count", "main"); n != "2" {
			t.Fatalf("a page written in the space was not pushed: %s", n)
		}
		seen := map[string]bool{}
		for _, tok := range f.GitTokens() {
			seen[tok] = true
		}
		for tok := range seen {
			if tok != f.AccessTokenOf("tang-h") {
				t.Fatalf("Git was given a token that is not the connected person's")
			}
		}
		// The same in the person's own namespace; private is the default.
		own := member.do("POST", "/api/spaces", map[string]any{"name": "Own", "storage": fgSync(kind, map[string]any{"owner": "tang-h", "name": "own"})}).want(t, 201, "")
		if !f.RepoByName("tang-h/own").Private {
			t.Fatal("private is the default")
		}
		_ = own
		// A connected person with a space on it: the provider shows how many, and is in use.
		if p := fgProvider(t, member, kind); p["connection"].(map[string]any)["spaces"] != float64(2) {
			t.Fatalf("%v", p["connection"])
		}
		admin.do("DELETE", "/api/admin/providers/"+kind, nil).want(t, 409, "provider_in_use")
		del := admin.do("DELETE", "/api/admin/providers/"+kind, nil)
		if del.body["connections"] != float64(1) || del.body["spaces"] != float64(2) {
			t.Fatalf("%s", del.raw)
		}
		// Nothing of a token anywhere the API shows.
		for _, res := range []result{rem, r, member.do("GET", "/api/account/links", nil), admin.do("GET", "/api/admin/providers", nil)} {
			for _, tok := range f.AllTokens() {
				if strings.Contains(res.raw, tok) {
					t.Fatalf("a token is in a response: %s", res.raw)
				}
			}
		}
	})
}

func TestCreatingASpaceOnAHostFailsCleanly(t *testing.T) {
	t.Parallel()
	eachForge(t, func(t *testing.T, a *acc, kind string, f *forgetest.Fake) {
		_, member := fgSetup(t, a, kind, f)
		f.AddUser("boss")
		f.AddGroup("others", map[string]int{"boss": forgetest.Owner})
		f.AddRepo("tang-h", "taken", true)
		spacesBefore := len(member.do("GET", "/api/spaces", nil).body["spaces"].([]any))
		cases := []struct {
			fields map[string]any
			status int
			code   string
		}{
			{map[string]any{"owner": "tang-h", "name": "taken"}, 409, "name_taken"},
			{map[string]any{"owner": "others", "name": "x"}, 403, "owner_not_allowed"},
			{map[string]any{"owner": "nobody-at-all", "name": "x"}, 403, "owner_not_allowed"},
			{map[string]any{"owner": "tang-h", "name": "a b"}, 400, "invalid"},
			{map[string]any{"owner": "tang-h", "name": "x.git"}, 400, "invalid"},
			{map[string]any{"owner": "tang h", "name": "x"}, 400, "invalid"},
			{map[string]any{"owner": "tang-h", "name": "ok", "description": "a\nb"}, 400, "invalid"},
			{map[string]any{"owner": "tang-h", "name": "existing-missing", "create": false}, 403, "owner_not_allowed"},
		}
		for _, c := range cases {
			r := member.do("POST", "/api/spaces", map[string]any{"name": "X", "storage": fgSync(kind, c.fields)})
			if r.code != c.status || r.body["error"] != c.code || r.body["provider"] != kind {
				t.Errorf("%v: want %d %s, got %d %s", c.fields, c.status, c.code, r.code, r.raw)
			}
		}
		if got := len(member.do("GET", "/api/spaces", nil).body["spaces"].([]any)); got != spacesBefore {
			t.Fatalf("%d spaces, were %d: a refusal left a space", got, spacesBefore)
		}
		// A person who has not connected gets not_connected.
		other := a.addUser("wu", pw, "member")
		other.do("POST", "/api/spaces", map[string]any{"name": "X", "storage": fgSync(kind, map[string]any{"owner": "tang-h", "name": "y"})}).want(t, 409, "not_connected")
		// A provider that is not there, and plain Git, are not this route's.
		member.do("POST", "/api/spaces", map[string]any{"name": "X", "storage": fgSync("nope", map[string]any{"owner": "a", "name": "b"})}).want(t, 400, "invalid")
		// Connecting a space to a repository that is there.
		f.AddRepo("acme", "old-notes", true)
		ok := member.do("POST", "/api/spaces", map[string]any{"name": "Old", "storage": fgSync(kind, map[string]any{"owner": "acme", "name": "old-notes", "create": false})}).want(t, 201, "")
		if ok.body["sync"] != "ok" {
			t.Fatalf("%s", ok.raw)
		}
		// One the person can only read is refused.
		f.AddRepo("boss", "readable", true)
		f.Collaborate("boss/readable", "tang-h", forgetest.Reporter)
		member.do("POST", "/api/spaces", map[string]any{"name": "R", "storage": fgSync(kind, map[string]any{"owner": "boss", "name": "readable", "create": false})}).want(t, 403, "owner_not_allowed")
	})
}

func TestSwitchingASpaceToAHostThroughTheRemoteEndpoint(t *testing.T) {
	t.Parallel()
	eachForge(t, func(t *testing.T, a *acc, kind string, f *forgetest.Fake) {
		_, member := fgSetup(t, a, kind, f)
		f.AddRepo("acme", "wiki", true)
		sp := member.do("POST", "/api/spaces", map[string]any{"name": "Wiki"}).want(t, 201, "").body["id"].(string)
		cred := map[string]any{"type": "connection", "provider": kind, "via": "user"}
		r := member.do("PUT", "/api/spaces/"+sp+"/remote", map[string]any{"url": f.URL + "/acme/wiki.git", "credential": cred}).want(t, 200, "")
		c := r.body["credential"].(map[string]any)
		if c["via"] != "user" || c["login"] != "tang-h" || r.body["provider"] != kind {
			t.Fatalf("%s", r.raw)
		}
		// via app is GitHub's.
		sp2 := member.do("POST", "/api/spaces", map[string]any{"name": "Wiki2"}).want(t, 201, "").body["id"].(string)
		member.do("PUT", "/api/spaces/"+sp2+"/remote", map[string]any{"url": f.URL + "/acme/wiki.git", "credential": map[string]any{"type": "connection", "provider": kind, "via": "app"}}).want(t, 400, "invalid")
		// An address of another host.
		member.do("PUT", "/api/spaces/"+sp2+"/remote", map[string]any{"url": "https://example.com/acme/wiki.git", "credential": cred}).want(t, 400, "invalid")
	})
}

func TestAnExpiredConnectionShowsItsCodeAndRefusesWrites(t *testing.T) {
	t.Parallel()
	eachForge(t, func(t *testing.T, a *acc, kind string, f *forgetest.Fake) {
		_, member := fgSetup(t, a, kind, f)
		id := member.do("POST", "/api/spaces", map[string]any{"name": "Mine", "storage": fgSync(kind, map[string]any{"owner": "tang-h", "name": "mine"})}).want(t, 201, "").body["id"].(string)
		page := member.do("POST", "/api/spaces/"+id+"/pages", map[string]any{"meta": map[string]any{"title": "first"}, "body": "one"}).want(t, 201, "").body["id"].(string)
		f.RevokeUser("tang-h") // the person removed the application on the host
		a.clock.Advance(f.AccessTTL + time.Hour)
		member.do("POST", "/api/spaces/"+id+"/pages", map[string]any{"meta": map[string]any{"title": "second"}, "body": "two"}).want(t, 503, "unavailable")
		rem := member.do("GET", "/api/spaces/"+id+"/remote", nil).want(t, 200, "")
		if rem.body["state"] != "error" || rem.body["errorCode"] != "connection_expired" || rem.body["error"] == "" {
			t.Fatalf("%s", rem.raw)
		}
		if p := fgProvider(t, member, kind); p["connection"].(map[string]any)["state"] != "expired" {
			t.Fatalf("%v", p["connection"])
		}
		// Reads work.
		member.do("GET", "/api/spaces/"+id+"/pages/"+page, nil).want(t, 200, "")
		// Reconnecting fixes the space without anything else being done.
		connectForge(t, a, member, kind, f, "tang-h")
		for i := 0; i < 100; i++ {
			if r := member.do("GET", "/api/spaces/"+id+"/remote", nil); r.body["state"] == "ok" {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		member.do("POST", "/api/spaces/"+id+"/pages", map[string]any{"meta": map[string]any{"title": "third"}, "body": "three"}).want(t, 201, "")
	})
}

// A space that syncs with someone's connection stops, and loses nothing, when that
// person can no longer manage it; another person's connection is not borrowed.
func TestAnotherPersonsConnectionIsNeverBorrowed(t *testing.T) {
	t.Parallel()
	eachForge(t, func(t *testing.T, a *acc, kind string, f *forgetest.Fake) {
		admin, member := fgSetup(t, a, kind, f)
		bob := a.addUser("bob", pw, "member")
		f.AddUser("bob-h")
		f.AddGroup("bobs", map[string]int{"bob-h": forgetest.Owner})
		connectForge(t, a, bob, kind, f, "bob-h")
		// Bob cannot use tang's account: his own lists show his own.
		owners := bob.do("GET", "/api/account/links/"+kind+"/owners", nil).want(t, 200, "").body["items"].([]any)
		if len(owners) != 2 || owners[0].(map[string]any)["login"] != "bob-h" {
			t.Fatalf("%v", owners)
		}
		bob.do("POST", "/api/spaces", map[string]any{"name": "X", "storage": fgSync(kind, map[string]any{"owner": "acme", "name": "bobs-try"})}).want(t, 403, "owner_not_allowed")
		// Tang's space; Bob is an editor of it.
		id := member.do("POST", "/api/spaces", map[string]any{"name": "Tang", "storage": fgSync(kind, map[string]any{"owner": "tang-h", "name": "tang-notes"})}).want(t, 201, "").body["id"].(string)
		member.do("POST", "/api/spaces/"+id+"/members", map[string]any{"username": "bob", "role": "editor"}).want(t, 201, "")
		bob.do("PUT", "/api/spaces/"+id+"/remote", map[string]any{"url": f.URL + "/tang-h/tang-notes.git", "credential": map[string]any{"type": "connection", "provider": kind, "via": "user"}}).want(t, 403, "no_permission")
		bob.do("POST", "/api/spaces/"+id+"/pages", map[string]any{"meta": map[string]any{"title": "b"}, "body": "bob wrote"}).want(t, 201, "")
		for _, tok := range f.GitTokens() {
			if tok == f.AccessTokenOf("bob-h") {
				t.Fatal("Bob's token was given to Git for Tang's space")
			}
		}
		// Bob managing it and connecting his own account to a repository of his own switches the space to his.
		member.do("PATCH", "/api/spaces/"+id+"/members/"+mustUserID(t, a, "bob"), map[string]any{"role": "owner"}).want(t, 200, "")
		f.AddRepo("bob-h", "bob-notes", true)
		bob.do("PUT", "/api/spaces/"+id+"/remote", map[string]any{"url": f.URL + "/bob-h/bob-notes.git", "credential": map[string]any{"type": "connection", "provider": kind, "via": "user"}}).want(t, 200, "")
		// Tang is no longer the one whose token is used.
		before := len(f.GitTokens())
		bob.do("POST", "/api/spaces/"+id+"/pages", map[string]any{"meta": map[string]any{"title": "c"}, "body": "now on bob's"}).want(t, 201, "")
		for _, tok := range f.GitTokens()[before:] {
			if tok != f.AccessTokenOf("bob-h") {
				t.Fatal("the space did not sync with the token of the person who connected it")
			}
		}
		_ = admin
	})
}

// A provider of one tenant is not seen, listed or used by another's people.
func TestATenantsProviderIsInvisibleToOtherTenants(t *testing.T) {
	t.Parallel()
	eachForge(t, func(t *testing.T, a *acc, kind string, f *forgetest.Fake) {
		fx := newTenFx(t, a)
		sealed, err := a.fm.SealForTest("acme-"+kind, fx.tenB, f.ClientSecret)
		if err != nil {
			t.Fatal(err)
		}
		err = store.InsertConnProviderRow(context.Background(), a.st.Q(), store.ConnProviderRow{ID: "acme-" + kind, Type: kind, Host: f.Host(), Scheme: "http", Name: "Acme's own",
			Owner: fx.tenB, ClientID: f.ClientID, SecretEnc: sealed, State: "ok", PartialSync: "unknown", Created: a.clock.Now(), Updated: a.clock.Now()})
		if err != nil {
			t.Fatal(err)
		}
		_ = sealed
		if err := a.fm.Reload(context.Background()); err != nil {
			t.Fatal(err)
		}
		has := func(c *client) bool {
			for _, p := range c.do("GET", "/api/account/links", nil).want(t, 200, "").body["providers"].([]any) {
				if p.(map[string]any)["id"] == "acme-"+kind {
					return true
				}
			}
			return false
		}
		if !has(fx.memB) || !has(fx.admB) {
			t.Fatal("a tenant's people do not see their own provider")
		}
		if has(fx.memA) || has(fx.admA) || has(fx.root) {
			t.Fatal("another tenant sees a tenant's provider")
		}
		p := fgProvider(t, fx.memB, "acme-"+kind)
		if o := p["owner"].(map[string]any); o["tenant"] != fx.tenB {
			t.Fatalf("%v", p["owner"])
		}
		id := "acme-" + kind
		for _, c := range []*client{fx.memA, fx.admA, fx.root} {
			c.do("GET", "/api/account/links/"+id+"/owners", nil).want(t, 404, "not_found")
			c.do("GET", "/api/account/links/"+id+"/repos", nil).want(t, 404, "not_found")
			c.do("POST", "/api/account/links/"+id+"/start", map[string]any{"purpose": "repos"}).want(t, 404, "not_found")
			c.do("DELETE", "/api/account/links/"+id+"/repos", nil).want(t, 404, "not_found")
			c.do("POST", "/api/spaces", map[string]any{"name": "X", "storage": fgSync(id, map[string]any{"owner": "a", "name": "b"})}).want(t, 404, "not_found")
		}
		// Its own people connect and use it.
		f.AddUser("mem-b-h")
		connectForge(t, a, fx.memB, id, f, "mem-b-h")
		fx.memB.do("GET", "/api/account/links/"+id+"/owners", nil).want(t, 200, "")
		// The server's administrators' list shows it (a site administrator sees them all).
		var found bool
		for _, p := range fx.root.do("GET", "/api/admin/providers", nil).want(t, 200, "").body["providers"].([]any) {
			found = found || p.(map[string]any)["id"] == id
		}
		if !found {
			t.Fatal("the site administrators' list has no tenant's provider")
		}
	})
}

func TestEveryProviderErrorNamesItsProvider(t *testing.T) {
	t.Parallel()
	eachForge(t, func(t *testing.T, a *acc, kind string, f *forgetest.Fake) {
		admin, member := fgSetup(t, a, kind, f)
		probes := []struct {
			who          *client
			method, path string
			body         any
			status       int
			code         string
		}{
			{member, "GET", "/api/account/links/" + kind + "/repos?installation=3", nil, 400, "invalid"},
			{member, "DELETE", "/api/account/leftovers/" + kind + "/abc", nil, 404, "not_found"},
			{admin, "GET", "/api/admin/providers/" + kind + "/installations", nil, 404, "not_found"},
			{admin, "PUT", "/api/admin/providers/" + kind + "/installations/1", map[string]any{"tenants": []string{}}, 404, "not_found"},
			{admin, "POST", "/api/admin/providers/" + kind + "/manifest", nil, 404, "not_found"},
			{admin, "PATCH", "/api/admin/providers/" + kind, map[string]any{"clientSecret": "wrong"}, 400, "invalid"},
		}
		for _, p := range probes {
			r := p.who.do(p.method, p.path, p.body)
			if r.code != p.status || r.body["error"] != p.code || r.body["provider"] != kind {
				t.Errorf("%s %s: want %d %s from %q, got %d %s", p.method, p.path, p.status, p.code, kind, r.code, r.raw)
			}
		}
	})
}
