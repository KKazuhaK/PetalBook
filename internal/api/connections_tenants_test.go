package api_test

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/KKazuhaK/PetalBook/internal/store"
)

// 2.7 under the tenants of 3.3 (docs/design.md, "2.7 的约定"): the App is the
// server's, a GitHub connection is its user's, and a space made with a
// repository is its creator's tenant's, under its quotas.

// ghTenFx is the isolation fixture with the server's App saved, and a GitHub
// account and an organisation of B's member.
func ghTenFx(t *testing.T, a *acc) *tenFx {
	t.Helper()
	f := newTenFx(t, a)
	f.root.do("PUT", "/api/admin/connections/github", map[string]any{"appId": "424242", "privateKey": a.fake.KeyPEM(),
		"clientId": a.fake.ClientID, "clientSecret": a.fake.ClientSecret}).want(t, 200, "")
	a.fake.AddUser("mem-b-gh")
	a.fake.AddOrg("acme-gh", "mem-b-gh")
	a.fake.Install("acme-gh", "all")
	connectGitHub(t, a, f.memB, "mem-b-gh")
	return f
}

func TestATenantAdministratorCannotReachTheApp(t *testing.T) {
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		f := ghTenFx(t, a)
		for _, c := range []*client{f.admA, f.admB} {
			c.do("GET", "/api/admin/connections/github", nil).want(t, 403, "forbidden")
			c.do("PUT", "/api/admin/connections/github", map[string]any{"appId": "1"}).want(t, 403, "forbidden")
			c.do("DELETE", "/api/admin/connections/github", nil).want(t, 403, "forbidden")
			c.do("GET", "/api/admin/connections/github/installations", nil).want(t, 403, "forbidden")
			c.do("PUT", "/api/admin/connections/github/installations/1", map[string]any{"tenants": []string{}}).want(t, 403, "forbidden")
		}
		f.root.do("GET", "/api/admin/connections/github/installations", nil).want(t, 200, "")
	})
}

func TestASpaceWithARepositoryIsItsCreatorsTenantsAndCounts(t *testing.T) {
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		f := ghTenFx(t, a)
		ctx := context.Background()
		before := f.memB.do("GET", "/api/account/usage", nil).want(t, 200, "")
		r := f.memB.do("POST", "/api/spaces", map[string]any{"name": "Acme notes", "storage": ghSync(map[string]any{"owner": "acme-gh", "name": "notes"})}).want(t, 201, "")
		id := r.body["id"].(string)
		sp, err := store.GetSpace(ctx, a.st.Q(), store.AllTenants(), id)
		if err != nil || sp.TenantID != f.tenB {
			t.Fatalf("the space is in tenant %q (%v), want B's %q", sp.TenantID, err, f.tenB)
		}
		// A's people see nothing of it.
		f.admA.do("GET", "/api/admin/spaces/"+id, nil).want(t, 404, "not_found")
		after := f.memB.do("GET", "/api/account/usage", nil).want(t, 200, "")
		if spacesUsed(t, after) != spacesUsed(t, before)+1 {
			t.Fatalf("usage did not count the space: %s -> %s", before.raw, after.raw)
		}
		// The quota of B's tenant refuses the next one, before anything is made on GitHub.
		f.admB.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/quota.spacesPerPerson", map[string]any{"value": spacesUsed(t, after)}).want(t, 200, "")
		n := a.fake.Count("POST", "/orgs/acme-gh/repos")
		f.memB.do("POST", "/api/spaces", map[string]any{"name": "More", "storage": ghSync(map[string]any{"owner": "acme-gh", "name": "more"})}).want(t, 403, "quota_exceeded")
		if a.fake.Count("POST", "/orgs/acme-gh/repos") != n {
			t.Fatal("a repository was made for a refused space")
		}
	})
}

// spacesUsed reads the person's number of spaces from GET /api/account/usage.
func spacesUsed(t *testing.T, r result) int64 {
	t.Helper()
	m, ok := r.body["spaces"].(map[string]any)
	n, ok2 := m["used"].(float64)
	if !ok || !ok2 {
		t.Fatalf("no spaces.used in %s", r.raw)
	}
	return int64(n)
}

func TestAConnectionSyncsOnlySpacesOfItsOwnTenant(t *testing.T) {
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		f := ghTenFx(t, a)
		// The site administrator (tenant A) may open B's space, but their own
		// GitHub connection is not one B's space can sync through.
		a.fake.AddUser("root-gh")
		a.fake.AddRepo("root-gh", "wiki", true)
		connectGitHub(t, a, f.root, "root-gh")
		body := map[string]any{"url": a.fake.URL + "/root-gh/wiki.git", "credential": map[string]any{"type": "connection", "provider": "github", "via": "user"}}
		calls := len(a.fake.Requests())
		r := f.root.do("PUT", "/api/spaces/"+f.spaceB+"/remote", body)
		if r.code != 403 || r.body["error"] != "forbidden" {
			t.Fatalf("%d %s", r.code, r.raw)
		}
		if len(a.fake.Requests()) != calls {
			t.Fatal("GitHub was asked before the tenant was checked")
		}
		// In a space of their own tenant (one they own) it is fine.
		own := f.root.do("POST", "/api/spaces", map[string]any{"name": "Root's"}).want(t, 201, "").body["id"].(string)
		f.root.do("PUT", "/api/spaces/"+own+"/remote", body).want(t, 200, "")
	})
}

func TestAGitHubAccountIsLinkedOnceAcrossTenants(t *testing.T) {
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		f := ghTenFx(t, a)
		// A's member tries B's member's GitHub account: taken, and the answer
		// says nothing of who has it or in which tenant.
		start := f.memA.do("POST", "/api/connections/github/authorization/start", map[string]any{}).want(t, 200, "")
		back, _ := url.Parse(a.fake.Authorize(start.body["url"].(string), "mem-b-gh"))
		r := f.memA.do("GET", back.Path+"?"+back.RawQuery, nil, "Sec-Fetch-Site", "cross-site")
		if r.code != 303 || r.resp.Header.Get("Location") != "/settings?provider=github&connection_error=account_in_use" {
			t.Fatalf("%d %q", r.code, r.resp.Header.Get("Location"))
		}
		for _, id := range f.bIDs() {
			if strings.Contains(r.raw, id) || strings.Contains(r.resp.Header.Get("Location"), id) {
				t.Fatalf("the refusal names %q of tenant B", id)
			}
		}
		if g := ghProvider(t, f.memA); g.body["connection"] != nil {
			t.Fatalf("%s", g.raw)
		}
	})
}

// An installation is shared with chosen tenants: only their people can use it,
// and a site administrator is the only one who chooses.
func TestAnInstallationIsSharedWithChosenTenantsOnly(t *testing.T) {
	eachAccounts(t, ghOpts, func(t *testing.T, a *acc) {
		f := ghTenFx(t, a)
		a.fake.AddOrg("petal-team")
		inst := a.fake.Install("petal-team", "all")
		a.fake.AddRepo("petal-team", "docs", true)
		path := "/api/admin/connections/github/installations/" + itoa(inst)
		put := f.root.do("PUT", path, map[string]any{"tenants": []string{f.tenB}}).want(t, 200, "")
		if fmt.Sprint(put.body["tenants"]) != "["+f.tenB+"]" {
			t.Fatalf("%s", put.raw)
		}
		owners := func(c *client) string {
			var s []string
			for _, o := range c.do("GET", "/api/connections/github/owners", nil).want(t, 200, "").body["items"].([]any) {
				s = append(s, o.(map[string]any)["login"].(string))
			}
			return strings.Join(s, ",")
		}
		if got := owners(f.memB); !strings.Contains(got, "petal-team") {
			t.Fatalf("B's member sees %q", got)
		}
		if got := owners(f.memA); strings.Contains(got, "petal-team") {
			t.Fatalf("A's member sees %q", got)
		}
		// B creates in it without a connection of its own to that organisation; A's member cannot.
		before := a.fake.Count("POST", "/orgs/petal-team/repos")
		f.memA.do("POST", "/api/spaces", map[string]any{"name": "Docs A", "storage": ghSync(map[string]any{"owner": "petal-team", "name": "docs-a"})}).want(t, 403, "owner_not_allowed")
		f.memA.do("GET", "/api/connections/github/repos?installation="+itoa(inst), nil).want(t, 404, "not_found")
		if a.fake.Count("POST", "/orgs/petal-team/repos") != before {
			t.Fatal("a repository was made for a tenant the installation is not shared with")
		}
		f.memB.do("POST", "/api/spaces", map[string]any{"name": "Docs B", "storage": ghSync(map[string]any{"owner": "petal-team", "name": "docs-b"})}).want(t, 201, "")
		// The list of installations tells the administrator who has it.
		for _, it := range f.root.do("GET", "/api/admin/connections/github/installations", nil).want(t, 200, "").body["items"].([]any) {
			m := it.(map[string]any)
			if m["id"] == float64(inst) && fmt.Sprint(m["tenants"]) != "["+f.tenB+"]" {
				t.Fatalf("%v", m)
			}
		}
		// Deleting a tenant (it must be empty first) is covered by the store; un-sharing here.
		f.root.do("PUT", path, map[string]any{"tenants": []string{}}).want(t, 200, "")
		f.memB.do("POST", "/api/spaces", map[string]any{"name": "Docs B2", "storage": ghSync(map[string]any{"owner": "petal-team", "name": "docs-b2"})}).want(t, 403, "owner_not_allowed")
	})
}
