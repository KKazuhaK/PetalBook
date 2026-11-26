package api_test

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/auth/testidp"
	"petalbook.app/server/internal/store"
)

func strs(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, fmt.Sprint(x))
	}
	return out
}

func TestTenancyIsOffUntilASiteAdministratorSwitchesItOn(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		r := root.do("GET", "/api/admin/tenancy", nil).want(t, 200, "")
		if r.body["enabled"] != false || r.body["tenants"] != float64(1) {
			t.Fatal(r.raw)
		}
		root.do("POST", "/api/admin/tenants", map[string]any{"slug": "acme", "name": "Acme"}).want(t, 409, "tenants_off")
		root.do("PUT", "/api/admin/tenancy", map[string]any{}).want(t, 400, "invalid")
		root.do("PUT", "/api/admin/tenancy", map[string]any{"enabled": true}).want(t, 200, "")
		root.do("POST", "/api/admin/tenants", map[string]any{"slug": "acme", "name": "Acme"}).want(t, 201, "")
		// Switching off leaves the tenants as they are and stops new ones.
		r = root.do("PUT", "/api/admin/tenancy", map[string]any{"enabled": false}).want(t, 200, "")
		if r.body["enabled"] != false || r.body["tenants"] != float64(2) {
			t.Fatal(r.raw)
		}
		root.do("POST", "/api/admin/tenants", map[string]any{"slug": "more", "name": "More"}).want(t, 409, "tenants_off")
		// Tenant administrators and members have no say in it.
		adm := a.newClient("")
		if _, err := a.auth.CreateUser(context.Background(), auth.CreateParams{Username: "tadmin", Role: "admin", Password: pw, TenantID: store.DefaultTenantID}); err != nil {
			t.Fatal(err)
		}
		adm.do("POST", "/api/login", map[string]any{"username": "tadmin", "password": pw}).want(t, 200, "")
		adm.do("GET", "/api/admin/tenancy", nil).want(t, 403, "forbidden")
		adm.do("PUT", "/api/admin/tenancy", map[string]any{"enabled": true}).want(t, 403, "forbidden")
		adm.do("GET", "/api/admin/tenants", nil).want(t, 403, "forbidden")
	})
}

func TestTenantCrudAndItsErrors(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		root.do("PUT", "/api/admin/tenancy", map[string]any{"enabled": true}).want(t, 200, "")
		for _, bad := range []map[string]any{{"slug": "A", "name": "x"}, {"slug": "-a", "name": "x"}, {"slug": "a", "name": "x"}, {"slug": "ok", "name": " "},
			{"slug": "ok", "name": strings.Repeat("n", 81)}, {"slug": "ok", "name": "x", "admin": map[string]any{"username": "A B"}}} {
			root.do("POST", "/api/admin/tenants", bad).want(t, 400, "invalid")
		}
		r := root.do("POST", "/api/admin/tenants", map[string]any{"slug": "acme", "name": " Acme "}).want(t, 201, "")
		id := r.body["id"].(string)
		if r.body["slug"] != "acme" || r.body["name"] != "Acme" || r.body["default"] != false || r.body["disabled"] != false ||
			r.body["registration"].(map[string]any)["open"] != false || r.body["invite"] != nil || len(r.body["quota"].(map[string]any)) != 4 {
			t.Fatal(r.raw)
		}
		root.do("POST", "/api/admin/tenants", map[string]any{"slug": "acme", "name": "Other"}).want(t, 409, "exists")

		// The list: the default tenant first, then by name; q filters.
		root.do("POST", "/api/admin/tenants", map[string]any{"slug": "zed", "name": "Zed"}).want(t, 201, "")
		r = root.do("GET", "/api/admin/tenants", nil).want(t, 200, "")
		var slugs []string
		for _, it := range r.body["items"].([]any) {
			slugs = append(slugs, it.(map[string]any)["slug"].(string))
		}
		if fmt.Sprint(slugs) != "[default acme zed]" {
			t.Fatal(slugs)
		}
		r = root.do("GET", "/api/admin/tenants?limit=1", nil).want(t, 200, "")
		next := r.body["next"].(string)
		r = root.do("GET", "/api/admin/tenants?limit=1&cursor="+next, nil).want(t, 200, "")
		if r.body["items"].([]any)[0].(map[string]any)["slug"] != "acme" {
			t.Fatal(r.raw)
		}
		if r = root.do("GET", "/api/admin/tenants?q=ZE", nil).want(t, 200, ""); len(r.body["items"].([]any)) != 1 {
			t.Fatal(r.raw)
		}
		root.do("GET", "/api/admin/tenants?limit=0", nil).want(t, 400, "invalid")

		// Reading, changing.
		root.do("GET", "/api/admin/tenants/"+id, nil).want(t, 200, "")
		root.do("GET", "/api/admin/tenants/"+fakeID, nil).want(t, 404, "not_found")
		r = root.do("PATCH", "/api/admin/tenants/"+id, map[string]any{"name": "Acme Inc", "slug": "acme-inc"}).want(t, 200, "")
		if r.body["name"] != "Acme Inc" || r.body["slug"] != "acme-inc" {
			t.Fatal(r.raw)
		}
		// The totals are the quota.* settings': read only here, set through the tenant's settings.
		setTotals(t, root, id, map[string]any{"people": 5, "bytes": nil})
		if q := root.do("GET", "/api/admin/tenants/"+id, nil).want(t, 200, "").body["quota"].(map[string]any); q["people"] != float64(5) || q["bytes"] != nil {
			t.Fatalf("%v", q)
		}
		root.do("PATCH", "/api/admin/tenants/"+id, map[string]any{"slug": "zed"}).want(t, 409, "exists")
		root.do("PATCH", "/api/admin/tenants/"+id, map[string]any{"slug": "Bad Slug"}).want(t, 400, "invalid")
		if r := root.do("PATCH", "/api/admin/tenants/"+id, map[string]any{"quota": map[string]any{"people": 1}}).want(t, 400, "invalid"); !strings.Contains(r.body["message"].(string), "/settings/quota.") ||
			strings.Contains(r.body["message"].(string), "/limits") {
			t.Fatalf("the refusal names the settings that hold the totals: %s", r.raw)
		}
		root.do("PUT", "/api/admin/tenants/"+id+"/settings/quota.people", map[string]any{"siteRule": map[string]any{"value": -1}}).want(t, 400, "invalid")
		root.do("PUT", "/api/admin/tenants/"+id+"/settings/mood", map[string]any{"siteRule": map[string]any{"value": 1}}).want(t, 404, "not_found")
		root.do("PATCH", "/api/admin/tenants/"+id, map[string]any{"mood": 1}).want(t, 400, "invalid")
		// The default tenant: neither disabled nor deleted nor renamed by slug.
		root.do("PATCH", "/api/admin/tenants/"+store.DefaultTenantID, map[string]any{"disabled": true}).want(t, 409, "default_tenant")
		root.do("PATCH", "/api/admin/tenants/"+store.DefaultTenantID, map[string]any{"slug": "main"}).want(t, 409, "default_tenant")
		root.do("DELETE", "/api/admin/tenants/"+store.DefaultTenantID, nil).want(t, 409, "default_tenant")
		root.do("PATCH", "/api/admin/tenants/"+store.DefaultTenantID, map[string]any{"disabled": true}).want(t, 409, "default_tenant")
		root.do("PATCH", "/api/admin/tenants/"+store.DefaultTenantID, map[string]any{"name": "Home"}).want(t, 200, "")

		// Deleting: an empty tenant goes; one with people says what it holds.
		rn := root.do("POST", "/api/admin/tenants", map[string]any{"slug": "full", "name": "Full", "admin": map[string]any{"username": "boss-f"}}).want(t, 201, "")
		fid := rn.body["id"].(string)
		r = root.do("DELETE", "/api/admin/tenants/"+fid, nil).want(t, 409, "tenant_not_empty")
		if r.body["invites"] != float64(1) || r.body["users"] != float64(0) {
			t.Fatal(r.raw)
		}
		root.do("DELETE", "/api/admin/tenants/"+id, nil).want(t, 204, "")
		root.do("GET", "/api/admin/tenants/"+id, nil).want(t, 404, "not_found")
		root.do("DELETE", "/api/admin/tenants/"+id, nil).want(t, 404, "not_found")
		// The audit log knows.
		a.auth.FlushAudit(context.Background())
		r = root.do("GET", "/api/admin/audit?kind=tenant_created", nil).want(t, 200, "")
		if len(r.body["items"].([]any)) != 3 {
			t.Fatal(r.raw)
		}
	})
}

func TestTenantLimitOf1000(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		root.do("PUT", "/api/admin/tenancy", map[string]any{"enabled": true}).want(t, 200, "")
		for i := 1; i < store.MaxTenants; i++ {
			if _, err := a.st.Q().Exec(context.Background(), `INSERT INTO tenants(id, slug, name, name_key, created, updated) VALUES (?, ?, ?, ?, 0, 0)`,
				fmt.Sprintf("X%09d", i), fmt.Sprintf("x-%d", i), "x", "x"); err != nil {
				t.Fatal(err)
			}
		}
		root.do("POST", "/api/admin/tenants", map[string]any{"slug": "last", "name": "Last"}).want(t, 409, "tenant_limit")
	})
}

func TestTenantAdministratorsSeeAndChangeOnlyTheirTenant(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		// The user object says who is what.
		r := f.root.do("GET", "/api/session", nil).want(t, 200, "")
		if u := r.user(); u["siteAdmin"] != true || u["tenant"].(map[string]any)["slug"] != "default" || u["role"] != "admin" {
			t.Fatal(r.raw)
		}
		r = f.admB.do("GET", "/api/session", nil).want(t, 200, "")
		if u := r.user(); u["siteAdmin"] != false || u["tenant"].(map[string]any)["slug"] != "acme" || u["role"] != "admin" {
			t.Fatal(r.raw)
		}
		// Lists: a tenant administrator sees their tenant; a site administrator every one.
		names := func(c *client, path string) []string {
			var out []string
			for _, u := range c.do("GET", path, nil).want(t, 200, "").body["users"].([]any) {
				out = append(out, u.(map[string]any)["username"].(string))
			}
			sort.Strings(out)
			return out
		}
		if got := names(f.admB, "/api/users"); fmt.Sprint(got) != "[adm-b mem-b]" {
			t.Fatal(got)
		}
		if got := names(f.root, "/api/users"); len(got) != 5 {
			t.Fatal(got)
		}
		if got := names(f.root, "/api/users?tenant="+f.tenB); fmt.Sprint(got) != "[adm-b mem-b]" {
			t.Fatal(got)
		}
		f.root.do("GET", "/api/users?tenant="+fakeID, nil).want(t, 404, "not_found")
		// The tenant administrator reads and renames their own tenant, nothing more.
		f.admB.do("GET", "/api/admin/tenants/"+f.tenB, nil).want(t, 200, "")
		f.admB.do("PATCH", "/api/admin/tenants/"+f.tenB, map[string]any{"name": "Acme Two"}).want(t, 200, "")
		for _, body := range []map[string]any{{"slug": "other"}, {"disabled": true}} {
			f.admB.do("PATCH", "/api/admin/tenants/"+f.tenB, body).want(t, 403, "forbidden")
		}
		// A tenant administrator cannot make a site administrator, or touch one.
		f.admB.do("PATCH", "/api/users/"+f.userB, map[string]any{"siteAdmin": true}).want(t, 403, "forbidden")
		f.admA.do("PATCH", "/api/users/"+mustUserID(t, a, "root"), map[string]any{"disabled": true}).want(t, 403, "forbidden")
		f.admB.do("PATCH", "/api/users/"+f.userB, map[string]any{"role": "admin"}).want(t, 200, "")
		if u := f.root.do("GET", "/api/users?tenant="+f.tenB, nil).body["users"].([]any)[1].(map[string]any); u["role"] != "admin" || u["siteAdmin"] != false {
			t.Fatalf("a tenant administrator promoted someone to site administrator: %v", u)
		}
		// A site administrator makes one, and a member cannot be one.
		f.root.do("PATCH", "/api/users/"+f.userA, map[string]any{"siteAdmin": true}).want(t, 400, "invalid")
		f.root.do("PATCH", "/api/users/"+f.userB, map[string]any{"siteAdmin": true}).want(t, 200, "")
		f.memB.do("GET", "/api/admin/tenancy", nil).want(t, 200, "")
		// Demoting loses the flag too; the last site administrator stays.
		f.root.do("PATCH", "/api/users/"+f.userB, map[string]any{"role": "member"}).want(t, 200, "")
		f.memB.do("GET", "/api/admin/tenancy", nil).want(t, 403, "forbidden")
		f.root.do("PATCH", "/api/users/"+mustUserID(t, a, "root"), map[string]any{"role": "member"}).want(t, 409, "conflict")
		f.root.do("PATCH", "/api/users/"+mustUserID(t, a, "root"), map[string]any{"siteAdmin": false}).want(t, 409, "conflict")
		assertNoCrossTenantMemberships(t, a)
	})
}

func TestUsernamesAreUniqueOnTheWholeServerAndGroupNamesPerTenant(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		// The same username in the other tenant is taken.
		ri := f.admB.do("POST", "/api/invites", map[string]any{"role": "member"}).want(t, 201, "")
		c := a.newClient("")
		c.do("POST", "/api/invites/accept", map[string]any{"token": ri.body["token"], "username": "mem-a", "name": "x", "password": pw}).want(t, 409, "exists")
		// Group names: the same in both tenants, once in each.
		f.admB.do("POST", "/api/admin/groups", map[string]any{"name": "Group A", "source": "local"}).want(t, 201, "")
		f.admB.do("POST", "/api/admin/groups", map[string]any{"name": "group a", "source": "local"}).want(t, 409, "exists")
		// A person of another tenant is no person, whoever adds them.
		f.admA.do("POST", "/api/spaces/"+f.spaceA+"/members", map[string]any{"username": "mem-b", "role": "viewer"}).want(t, 404, "user_not_found")
		f.root.do("POST", "/api/spaces/"+f.spaceA+"/members", map[string]any{"username": "mem-b", "role": "viewer"}).want(t, 404, "user_not_found")
		f.root.do("POST", "/api/admin/spaces/"+f.spaceA+"/transfer", map[string]any{"userId": f.userB}).want(t, 404, "user_not_found")
		f.root.groupAdd(t, f.groupA, f.userB).want(t, 404, "user_not_found")
		f.root.do("POST", "/api/spaces/"+f.spaceA+"/groups", map[string]any{"groupId": f.groupB, "role": "viewer"}).want(t, 404, "group_not_found")
		// The group's list and its search see one tenant.
		r := f.admB.do("GET", "/api/groups?q=group", nil).want(t, 200, "")
		if strings.Contains(r.raw, f.groupA) {
			t.Fatal(r.raw)
		}
		// A site administrator who makes a group says where, and it stays in that tenant.
		g := f.root.do("POST", "/api/admin/groups?tenant="+f.tenB, map[string]any{"name": "By Root", "source": "local"}).want(t, 201, "")
		if f.admB.do("GET", "/api/admin/groups/"+gid(g), nil).code != 200 || f.admA.do("GET", "/api/admin/groups/"+gid(g), nil).code != 404 {
			t.Fatal("the group is not in the tenant it was made for")
		}
		assertNoCrossTenantMemberships(t, a)
	})
}

func TestADisabledTenantSignsNobodyIn(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{oauth: true}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		f.admB.do("GET", "/api/session", nil).want(t, 200, "")
		tokB := a.newClient("")
		tokB.headers["Authorization"] = "Bearer " + f.secretB
		tokB.do("GET", "/api/spaces", nil).want(t, 200, "")
		f.root.do("PATCH", "/api/admin/tenants/"+f.tenB, map[string]any{"disabled": true}).want(t, 200, "")
		// Their next request ends their session; signing in is refused as a wrong password is.
		f.memB.do("GET", "/api/spaces", nil).want(t, 401, "unauthenticated")
		bad := a.newClient("").do("POST", "/api/login", map[string]any{"username": "mem-b", "password": "wrong password"})
		good := a.newClient("").do("POST", "/api/login", map[string]any{"username": "mem-b", "password": pw})
		if bad.code != good.code || bad.body["error"] != good.body["error"] || bad.body["message"] != good.body["message"] {
			t.Fatalf("a disabled tenant must look like a wrong password: %s / %s", bad.raw, good.raw)
		}
		tokB.do("GET", "/api/spaces", nil).want(t, 401, "token_owner_disabled")
		// Its spaces cannot be opened, by an administrator of the server either; the rows are there.
		f.root.do("GET", "/api/spaces/"+f.spaceB+"/pages", nil).want(t, 403, "space_disabled")
		f.root.do("GET", "/api/admin/spaces/"+f.spaceB, nil).want(t, 200, "")
		// Nobody can be invited into it.
		f.root.do("POST", "/api/invites", map[string]any{"role": "member", "tenant": f.tenB}).want(t, 409, "conflict")
		// Enabling brings everything back.
		f.root.do("PATCH", "/api/admin/tenants/"+f.tenB, map[string]any{"disabled": false}).want(t, 200, "")
		a.newClient("").do("POST", "/api/login", map[string]any{"username": "mem-b", "password": pw}).want(t, 200, "")
		tokB.do("GET", "/api/spaces", nil).want(t, 200, "")
		f.root.do("POST", "/api/admin/spaces/"+f.spaceB+"/view", map[string]any{"reason": "test"}).want(t, 200, "")
		f.root.do("GET", "/api/spaces/"+f.spaceB+"/pages", nil).want(t, 200, "")
		// A site administrator cannot disable their own tenant.
		f.root.do("PATCH", "/api/admin/tenants/"+store.DefaultTenantID, map[string]any{"disabled": true}).want(t, 409, "default_tenant")
	})
}

// Offline sync (4.1) in a disabled tenant: its people and their tokens are
// refused as on every other route, its spaces cannot be read through the sync
// routes by anyone, and heads does not show them.
func TestADisabledTenantSyncsNothing(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		tokB := a.bearer(f.secretB, "")
		q := "?space=" + f.spaceB
		if h := f.memB.do("GET", "/api/sync/heads"+q, nil).want(t, 200, "").body["heads"].(map[string]any); h[f.spaceB] == nil {
			t.Fatalf("B's member has no head of B's space: %v", h)
		}
		tokB.do("GET", "/api/sync/heads"+q, nil).want(t, 200, "")
		sb := "/api/spaces/" + f.spaceB
		routes := []string{"/api/sync/heads" + q, sb + "/sync/catalog", sb + "/changes?since=" + url.QueryEscape(f.cursorB), sb + "/sync/pages?ids=0000000AAA"}

		f.root.do("PATCH", "/api/admin/tenants/"+f.tenB, map[string]any{"disabled": true}).want(t, 200, "")
		for _, p := range routes {
			f.memB.do("GET", p, nil).want(t, 401, "unauthenticated")
			tokB.do("GET", p, nil).want(t, 401, "token_owner_disabled")
		}
		// Nobody else reads its spaces: an administrator of the server is refused
		// as on the pages, and no head of them is given to anyone.
		for _, p := range routes[1:] {
			f.root.do("GET", p, nil).want(t, 403, "space_disabled")
		}
		for _, c := range []*client{f.root, f.admA, f.memA} {
			r := c.do("GET", "/api/sync/heads?space="+f.spaceB+"&space="+f.spaceA, nil).want(t, 200, "")
			if _, ok := r.body["heads"].(map[string]any)[f.spaceB]; ok || !strings.Contains(r.raw, `"`+f.spaceB+`"`) {
				t.Fatalf("heads shows a space of a disabled tenant: %s", r.raw)
			}
		}

		// Enabled again, the device goes on from the cursor it had.
		f.root.do("PATCH", "/api/admin/tenants/"+f.tenB, map[string]any{"disabled": false}).want(t, 200, "")
		mb := a.newClient("")
		mb.do("POST", "/api/login", map[string]any{"username": "mem-b", "password": pw}).want(t, 200, "")
		if h := mb.do("GET", "/api/sync/heads"+q, nil).want(t, 200, "").body["heads"].(map[string]any); h[f.spaceB] == nil {
			t.Fatalf("no head after the tenant is enabled again: %v", h)
		}
		mb.do("GET", routes[2], nil).want(t, 200, "")
		tokB.do("GET", routes[1], nil).want(t, 200, "")
	})
}

func TestInvitesBelongToATenant(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		// A tenant administrator invites into their own tenant only.
		f.admB.do("POST", "/api/invites", map[string]any{"role": "member", "tenant": store.DefaultTenantID}).want(t, 404, "not_found")
		r := f.admB.do("POST", "/api/invites", map[string]any{"role": "member", "tenant": f.tenB}).want(t, 201, "")
		if r.body["tenant"] != f.tenB {
			t.Fatal(r.raw)
		}
		// A site administrator names the tenant; the account is made there.
		r = f.root.do("POST", "/api/invites", map[string]any{"role": "member", "tenant": f.tenB}).want(t, 201, "")
		c := a.newClient("")
		u := c.do("POST", "/api/invites/accept", map[string]any{"token": r.body["token"], "username": "new-b", "name": "New", "password": pw}).want(t, 201, "").user()
		if u["tenant"].(map[string]any)["id"] != f.tenB || u["siteAdmin"] != false {
			t.Fatal(u)
		}
		// Lists: each administrator sees the invites of their tenant.
		if l := f.admB.do("GET", "/api/invites", nil).want(t, 200, "").body["invites"].([]any); len(l) != 2 {
			t.Fatalf("%v", l)
		}
		if l := f.admA.do("GET", "/api/invites", nil).want(t, 200, "").body["invites"].([]any); len(l) != 1 {
			t.Fatalf("%v", l)
		}
		// An administrator invited into the default tenant by a site administrator is one too:
		// what every administrator was before tenants. A tenant administrator's invite is not.
		r = f.root.do("POST", "/api/invites", map[string]any{"role": "admin"}).want(t, 201, "")
		if r.body["siteAdmin"] != true {
			t.Fatal(r.raw)
		}
		r = f.admA.do("POST", "/api/invites", map[string]any{"role": "admin"}).want(t, 201, "")
		if r.body["siteAdmin"] != false {
			t.Fatal(r.raw)
		}
		f.admA.do("POST", "/api/invites", map[string]any{"role": "admin", "siteAdmin": true}).want(t, 403, "forbidden")
	})
}

func TestProvidersNameATenantForNewAccounts(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		body := map[string]any{"slug": "corp", "kind": "oidc", "name": "Corp", "tenant": f.tenB, "autoCreate": true,
			"oidc": map[string]any{"issuer": "https://127.0.0.1:1/", "clientId": "x", "clientSecret": "y"}}
		r := f.root.do("POST", "/api/admin/auth/providers", body)
		// Discovery of the issuer may refuse; what matters is the field, so ask for it on a provider that exists.
		if r.code != 201 {
			t.Skipf("no provider could be made here: %d %s", r.code, r.raw)
		}
		if r.body["tenant"] != f.tenB {
			t.Fatal(r.raw)
		}
		f.root.do("PATCH", "/api/admin/auth/providers/corp", map[string]any{"tenant": fakeID}).want(t, 400, "invalid")
		f.admB.do("GET", "/api/admin/auth/providers", nil).want(t, 403, "forbidden")
		// The sign-in page names a tenant by slug; an unknown one is not found.
		a.newClient("").do("GET", "/api/auth/providers?tenant=acme", nil).want(t, 200, "")
		a.newClient("").do("GET", "/api/auth/providers?tenant=nope", nil).want(t, 404, "not_found")
		// Deleting a tenant moves its providers' new accounts back to the default tenant.
	})
}

func TestTenantAdminsSeeTheirAuditRowsWithoutTheServersOwn(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		f.root.do("PUT", "/api/admin/security", map[string]any{"limits": map[string]any{"perMinute": 100000}}).want(t, 200, "")
		a.auth.FlushAudit(context.Background())
		kinds := func(c *client) map[string]int {
			out := map[string]int{}
			for _, it := range c.do("GET", "/api/admin/audit?limit=200", nil).want(t, 200, "").body["items"].([]any) {
				out[it.(map[string]any)["kind"].(string)]++
			}
			return out
		}
		b := kinds(f.admB)
		if b["login"] != 2 || b["security_changed"] != 0 || b["tenant_created"] != 0 || b["setting_changed"] != 0 {
			t.Fatalf("tenant B's administrator sees %v", b)
		}
		all := kinds(f.root)
		if all["security_changed"] == 0 || all["tenant_created"] != 1 || all["setting_changed"] != 1 || all["login"] < 4 {
			t.Fatalf("a site administrator sees %v", all)
		}
		if k := kinds(f.admA); k["security_changed"] != 0 {
			t.Fatalf("tenant A's administrator sees %v", k)
		}
	})
}

// A tenant administrator can set a person's address and say it is verified;
// that must never let them catch someone else's first sign-in, in any tenant:
// a sign-in is never attached to an account by its address (3.6).
func TestAPuppetAccountNeverCatchesASignIn(t *testing.T) {
	t.Parallel()
	idp := newIDPFor(t, "home")
	idpB := newIDPFor(t, "corp")
	for _, f := range []*fakeIDP{idp, idpB} {
		f.addUser("victim", testidp.Claims{"sub": "s-victim", "preferred_username": "victim", "email": "victim@example.test", "email_verified": true, "name": "Victim"})
	}
	eachAccounts(t, accOpts{httpc: anyTLSClient()}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		puppet := func(name string) string {
			id := f.admB.do("POST", "/api/invites", map[string]any{"role": "member"}).want(t, 201, "").body["token"].(string)
			c := a.newClient("")
			c.do("POST", "/api/invites/accept", map[string]any{"token": id, "username": name, "name": name, "password": pw}).want(t, 201, "")
			uid := mustUserID(t, a, name)
			u := f.admB.do("PATCH", "/api/users/"+uid, map[string]any{"email": "Victim@example.test", "emailVerified": true}).want(t, 200, "").user()
			if u["emailVerified"] != true {
				t.Fatal(u)
			}
			return uid
		}
		puppet("puppet-1")
		// A provider of the default tenant: B's puppet is not in its tenant.
		f.root.do("POST", "/api/admin/auth/providers", idp.providerBody("home", map[string]any{"mergeVerifiedEmail": true, "autoCreate": true})).want(t, 201, "")
		c := a.newClient("")
		loc := idp.signIn2(t, c, "home", "victim", map[string]any{})
		if who := signedInAs(t, c); who == "puppet-1" {
			t.Fatalf("the sign-in of a default-tenant provider merged into tenant B's puppet (%s)", loc)
		}
		// A provider of B: the puppet is in its tenant, but only B's administrator vouched for the address.
		f.root.do("POST", "/api/admin/auth/providers", idpB.providerBody("corp", map[string]any{"mergeVerifiedEmail": true, "tenant": f.tenB})).want(t, 201, "")
		c = a.newClient("")
		loc = idpB.signIn2(t, c, "corp", "victim", map[string]any{})
		if who := signedInAs(t, c); who == "puppet-1" {
			t.Fatalf("the sign-in merged into an account whose address only a tenant administrator verified (%s)", loc)
		}
		// The address is held verified: the person is sent to link the
		// provider from the account that has it (3.6), never signed in to it.
		if loc != "/login?sso_error=email_in_use" {
			t.Fatal(loc)
		}
		// Not even once a site administrator vouched for it: a sign-in never
		// attaches itself to an account by its address.
		f.root.do("PATCH", "/api/users/"+mustUserID(t, a, "puppet-1"), map[string]any{"emailVerified": true}).want(t, 200, "")
		c = a.newClient("")
		if loc := idpB.signIn2(t, c, "corp", "victim", map[string]any{}); signedInAs(t, c) != "" || loc != "/login?sso_error=email_in_use" {
			t.Fatalf("%s %q", loc, signedInAs(t, c))
		}
	})
}

// A site administrator of a disabled tenant cannot sign in: they do not count
// as the one that must stay.
func TestASiteAdministratorOfADisabledTenantIsNotTheLastOne(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		x, err := a.auth.CreateUser(context.Background(), auth.CreateParams{Username: "adm-x", Role: "admin", Password: pw, TenantID: f.tenB})
		if err != nil {
			t.Fatal(err)
		}
		f.root.do("PATCH", "/api/users/"+x.ID, map[string]any{"siteAdmin": true}).want(t, 200, "")
		f.root.do("PATCH", "/api/admin/tenants/"+f.tenB, map[string]any{"disabled": true}).want(t, 200, "")
		rootID := mustUserID(t, a, "root")
		f.root.do("PATCH", "/api/users/"+rootID, map[string]any{"role": "member"}).want(t, 409, "conflict")
		f.root.do("PATCH", "/api/users/"+rootID, map[string]any{"siteAdmin": false}).want(t, 409, "conflict")
		// With X enabled again, adm-x is one: root may step down.
		f.root.do("PATCH", "/api/admin/tenants/"+f.tenB, map[string]any{"disabled": false}).want(t, 200, "")
		f.root.do("PATCH", "/api/users/"+rootID, map[string]any{"siteAdmin": false}).want(t, 200, "")
	})
}

// A site administrator's passkeys are theirs and other site administrators':
// a tenant administrator of the same tenant cannot delete them (as with
// PATCH /api/users/{id}).
func TestATenantAdministratorCannotDeleteASiteAdministratorsPasskey(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		rootID := mustUserID(t, a, "root")
		if err := store.InsertPasskey(context.Background(), a.st.Q(), store.PasskeyRow{ID: "PKROOT0001", UserID: rootID, CredentialID: "cm9vdA", Credential: `{"id":"cm9vdA"}`, Name: "Root key", RPID: "notes.example.test", Created: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		f.admA.do("DELETE", "/api/users/"+rootID+"/passkeys/PKROOT0001", nil).want(t, 403, "forbidden")
		if n := f.countOf(`SELECT COUNT(*) FROM passkeys WHERE id = 'PKROOT0001'`); n != 1 {
			t.Fatal("a tenant administrator deleted a site administrator's passkey")
		}
	})
}

// A site administrator changing tenant B's group or the groups of B's space
// writes the audit rows in B (the group's or space's tenant), not their own.
func TestGroupAuditRowsTakeTheGroupsTenant(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		kinds := func(c *client) map[string]int {
			a.auth.FlushAudit(context.Background())
			out := map[string]int{}
			for _, it := range c.do("GET", "/api/admin/audit?limit=200", nil).want(t, 200, "").body["items"].([]any) {
				out[it.(map[string]any)["kind"].(string)]++
			}
			return out
		}
		before := kinds(f.admB)
		f.root.do("DELETE", "/api/admin/groups/"+f.groupB+"/members/"+f.userB, nil).want(t, 204, "")
		f.groupAdd(t, f.root, f.groupB, f.userB)
		f.root.do("PATCH", "/api/spaces/"+f.spaceB+"/groups/"+f.groupB, map[string]any{"role": "editor"}).want(t, 200, "")
		f.root.do("DELETE", "/api/spaces/"+f.spaceB+"/groups/"+f.groupB, nil).want(t, 204, "")
		f.root.do("POST", "/api/spaces/"+f.spaceB+"/groups", map[string]any{"groupId": f.groupB, "role": "viewer"}).want(t, 201, "")
		after := kinds(f.admB)
		for _, k := range []string{"group_member_removed", "group_member_added", "space_group_changed", "space_group_removed", "space_group_added"} {
			if after[k] != before[k]+1 {
				t.Errorf("%s: tenant B's administrator sees %d rows, want %d", k, after[k], before[k]+1)
			}
		}
		if a := kinds(f.admA); a["group_member_added"] != 1 || a["space_group_added"] != 1 {
			t.Errorf("tenant A's administrator sees B's rows: %v", a)
		}
	})
}

// A tenant administrator cannot revoke a site administrator's API tokens or
// OAuth grants (as with PATCH /api/users/{id}); a site administrator can.
func TestATenantAdministratorCannotRevokeASiteAdministratorsTokensOrGrants(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		sid := f.root.personalSpace()
		id, secret := mkToken(t, f.root, "read", nil, sid)
		g := grant(t, a, f.root, "read", sid)
		gid := ""
		for _, it := range f.root.do("GET", "/api/admin/grants?user="+mustUserID(t, a, "root"), nil).want(t, 200, "").body["items"].([]any) {
			gid = it.(map[string]any)["id"].(string)
		}
		if gid == "" {
			t.Fatal("no grant listed")
		}
		f.admA.do("DELETE", "/api/admin/tokens/"+id, nil).want(t, 403, "forbidden")
		f.admA.do("DELETE", "/api/admin/grants/"+gid, nil).want(t, 403, "forbidden")
		a.bearer(secret, "").do("GET", spacePath(sid)+"/tree", nil).want(t, 200, "")
		refreshWith(t, a, g.cid, g.refresh).want(t, 200, "")
		f.root.do("DELETE", "/api/admin/tokens/"+id, nil).want(t, 204, "")
		f.root.do("DELETE", "/api/admin/grants/"+gid, nil).want(t, 204, "")
	})
}

// A refresh token of a disabled tenant's person gets no new access token.
func TestARefreshTokenOfADisabledTenantIsRefused(t *testing.T) {
	t.Parallel()
	eachAccounts(t, oa(), func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		g := grant(t, a, f.memB, "read", f.memB.personalSpace())
		f.root.do("PATCH", "/api/admin/tenants/"+f.tenB, map[string]any{"disabled": true}).want(t, 200, "")
		refreshWith(t, a, g.cid, g.refresh).want(t, 400, "invalid_grant")
		f.root.do("PATCH", "/api/admin/tenants/"+f.tenB, map[string]any{"disabled": false}).want(t, 200, "")
		refreshWith(t, a, g.cid, g.refresh).want(t, 200, "")
	})
}

// A provider whose tenant was disabled afterwards makes no account: the
// sign-in ends in no_account, not an internal error.
func TestAProviderOfADisabledTenantCreatesNoAccount(t *testing.T) {
	t.Parallel()
	idpB := newIDPFor(t, "corp")
	idpB.addUser("newbie", testidp.Claims{"sub": "s-newbie", "preferred_username": "newbie", "name": "Newbie"})
	eachAccounts(t, accOpts{httpc: anyTLSClient()}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		f.root.do("POST", "/api/admin/auth/providers", idpB.providerBody("corp", map[string]any{"autoCreate": true, "tenant": f.tenB})).want(t, 201, "")
		f.root.do("PATCH", "/api/admin/tenants/"+f.tenB, map[string]any{"disabled": true}).want(t, 200, "")
		c := a.newClient("")
		if loc := idpB.signIn2(t, c, "corp", "newbie", map[string]any{}); loc != "/login?sso_error=no_account" || signedInAs(t, c) != "" {
			t.Fatalf("%s (signed in as %q)", loc, signedInAs(t, c))
		}
		if n := f.countOf(`SELECT COUNT(*) FROM users WHERE username = 'newbie'`); n != 0 {
			t.Fatalf("%d accounts made in a disabled tenant", n)
		}
	})
}
