package api_test

import (
	"fmt"
	"strings"
	"testing"

	crewjamsaml "github.com/crewjam/saml"

	"github.com/KKazuhaK/PetalBook/internal/auth/testidp"
)

func gmap(pairs ...string) []map[string]any {
	out := []map[string]any{}
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, map[string]any{"value": pairs[i], "groupId": pairs[i+1]})
	}
	return out
}

// groupNames is the names of the groups an account is in, as the account says.
func groupNames(t *testing.T, c *client) string {
	t.Helper()
	var names []string
	for _, g := range c.do("GET", "/api/account", nil).want(t, 200, "").body["groups"].([]any) {
		names = append(names, g.(map[string]any)["name"].(string))
	}
	return strings.Join(names, ",")
}

func lastAudit(t *testing.T, c *client, kind string) map[string]any {
	t.Helper()
	rows := c.audit(t, kind)
	if len(rows) == 0 {
		t.Fatalf("no %s row", kind)
	}
	return rows[0]
}

func TestProviderGroupsSetting(t *testing.T) {
	t.Parallel()
	one := newIDPFor(t, "one")
	gh := newFakeGitHub(t)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		syn := admin.mkGroup(t, "Staff", "synced")
		loc := admin.mkGroup(t, "Local", "local")
		admin.do("POST", "/api/admin/auth/providers", one.providerBody("one", nil)).want(t, 201, "")
		// Off by default, with the defaults shown.
		p := admin.do("GET", "/api/admin/auth/providers/one", nil).want(t, 200, "")
		g := p.body["groups"].(map[string]any)
		if g["enabled"] != false || g["claim"] != "groups" || g["missing"] != "keep" || len(g["map"].([]any)) != 0 {
			t.Fatal(p.raw)
		}
		patch := func(groups map[string]any) result {
			return admin.do("PATCH", "/api/admin/auth/providers/one", map[string]any{"groups": groups})
		}
		patch(map[string]any{"enabled": true, "map": gmap("a", loc)}).want(t, 409, "group_not_synced")
		patch(map[string]any{"map": gmap("a", "0000000000")}).want(t, 404, "group_not_found")
		patch(map[string]any{"map": gmap("a", syn, "a", syn)}).want(t, 409, "exists")
		patch(map[string]any{"map": gmap("", syn)}).want(t, 400, "invalid")
		patch(map[string]any{"map": gmap(strings.Repeat("v", 257), syn)}).want(t, 400, "invalid")
		patch(map[string]any{"missing": "forget"}).want(t, 400, "invalid")
		patch(map[string]any{"claim": strings.Repeat("c", 257)}).want(t, 400, "invalid")
		big := make([]map[string]any, 1001)
		for i := range big {
			big[i] = map[string]any{"value": fmt.Sprint(i), "groupId": syn}
		}
		patch(map[string]any{"map": big}).want(t, 400, "invalid")
		// Nothing of the refused requests was kept.
		if g := admin.do("GET", "/api/admin/auth/providers/one", nil).body["groups"].(map[string]any); g["enabled"] != false || len(g["map"].([]any)) != 0 {
			t.Fatal(g)
		}
		r := patch(map[string]any{"enabled": true, "claim": "roles", "missing": "clear", "map": gmap("a", syn, "b", syn)}).want(t, 200, "")
		if g := r.body["groups"].(map[string]any); g["enabled"] != true || g["claim"] != "roles" || g["missing"] != "clear" || len(g["map"].([]any)) != 2 {
			t.Fatal(r.raw)
		}
		// The group says where it is fed from; a local one never.
		gr := admin.do("GET", "/api/admin/groups/"+syn, nil).want(t, 200, "")
		if mf := gr.body["mappedFrom"].([]any); len(mf) != 2 || mf[0].(map[string]any)["provider"] != "one" {
			t.Fatal(gr.raw)
		}
		// Changing something else keeps the mapping.
		r = admin.do("PATCH", "/api/admin/auth/providers/one", map[string]any{"name": "Renamed"}).want(t, 200, "")
		if len(r.body["groups"].(map[string]any)["map"].([]any)) != 2 {
			t.Fatal(r.raw)
		}
		// The audit row of the change.
		ch := lastAudit(t, admin, "provider_changed")["detail"].(map[string]any)
		if ch["slug"] != "one" {
			t.Fatal(ch)
		}
		// Deleting the group needs force while it is mapped, and unmaps it.
		d := admin.do("DELETE", "/api/admin/groups/"+syn, nil).want(t, 409, "group_in_use")
		if d.body["mappings"] != float64(2) || d.body["spaces"] != float64(0) {
			t.Fatal(d.raw)
		}
		admin.do("DELETE", "/api/admin/groups/"+syn+"?force=1", nil).want(t, 204, "")
		if g := admin.do("GET", "/api/admin/auth/providers/one", nil).body["groups"].(map[string]any); len(g["map"].([]any)) != 0 || g["enabled"] != true {
			t.Fatal(g)
		}
		// GitHub's claim is the organisations: not settable. SAML needs an attribute name.
		admin.do("POST", "/api/admin/auth/providers", gh.body("gh", map[string]any{"groups": map[string]any{"enabled": true, "claim": "orgs"}})).want(t, 400, "invalid")
		admin.do("POST", "/api/admin/auth/providers", gh.body("gh", map[string]any{"groups": map[string]any{"enabled": true}})).want(t, 201, "")
		if g := admin.do("GET", "/api/admin/auth/providers/gh", nil).body["groups"].(map[string]any); g["claim"] != "" || g["enabled"] != true {
			t.Fatal(g)
		}
	})
}

func TestOIDCGroupsSyncAtSignIn(t *testing.T) {
	t.Parallel()
	one, two := newIDPFor(t, "one"), newIDPFor(t, "two")
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		eng, staff, other := admin.mkGroup(t, "Engineering", "synced"), admin.mkGroup(t, "Staff", "synced"), admin.mkGroup(t, "Other", "synced")
		hand := admin.mkGroup(t, "By hand", "local")
		admin.do("POST", "/api/admin/auth/providers", one.providerBody("one", map[string]any{"autoCreate": true, "groups": map[string]any{"enabled": true,
			"map": gmap("5f1c-engineering", eng, "/staff", staff)}})).want(t, 201, "")
		admin.do("POST", "/api/admin/auth/providers", two.providerBody("two", map[string]any{"autoCreate": true, "groups": map[string]any{"enabled": true, "claim": "roles",
			"map": gmap("dev", eng, "x", other)}})).want(t, 201, "")
		team := admin.do("POST", "/api/spaces", map[string]any{"name": "Team"}).want(t, 201, "").body["id"].(string)
		admin.do("POST", spacePath(team)+"/groups", map[string]any{"groupId": eng, "role": "editor"}).want(t, 201, "")

		set := func(idp *fakeIDP, claims map[string]any) {
			c := testidp.Claims{"sub": "sub-ann", "preferred_username": "ann", "email": "ann@example.test"}
			for k, v := range claims {
				c[k] = v
			}
			idp.addUser("ann", c)
		}
		ann := a.newClient("")
		// First sign-in: the account is made, and the groups follow; unmapped values make no group.
		set(one, map[string]any{"groups": []string{"5f1c-engineering", "unmapped", "/staff"}})
		if loc := one.signIn2(t, ann, "one", "ann", nil); loc != "/" {
			t.Fatal(loc)
		}
		if got := groupNames(t, ann); got != "Engineering,Staff" {
			t.Fatal(got)
		}
		ann.do("GET", spacePath(team)+"/members", nil).want(t, 200, "") // in through the group
		sy := lastAudit(t, admin, "groups_synced")
		d := sy["detail"].(map[string]any)
		if d["provider"] != "one" || len(d["added"].([]any)) != 2 || len(d["removed"].([]any)) != 0 || sy["actor"] != nil || sy["user"] == nil {
			t.Fatalf("%v", sy)
		}
		if g := admin.do("GET", "/api/admin/groups/"+eng+"/members", nil).want(t, 200, ""); g.body["items"].([]any)[0].(map[string]any)["from"].([]any)[0].(map[string]any)["value"] != "5f1c-engineering" {
			t.Fatal(g.raw)
		}
		// Local groups and other providers' memberships are not touched.
		admin.groupAdd(t, hand, mustUserID(t, a, "ann")).want(t, 200, "")
		rows := len(admin.audit(t, "groups_synced"))
		// The same claim again changes nothing: no row, nothing dropped.
		a.mgr.List(t.Context(), mustUserID(t, a, "ann"))
		if one.signIn2(t, a.newClient(""), "one", "ann", nil) != "/" {
			t.Fatal("sign-in")
		}
		if len(admin.audit(t, "groups_synced")) != rows {
			t.Fatal("a sync without a change wrote a row")
		}
		// A string is a list of one.
		set(one, map[string]any{"groups": "/staff"})
		one.signIn2(t, a.newClient(""), "one", "ann", nil)
		if got := groupNames(t, ann); got != "By hand,Staff" {
			t.Fatal(got)
		}
		// Gone from the claim at the next sign-in, and not before: the space is lost then.
		ann.do("GET", spacePath(team)+"/members", nil).want(t, 404, "not_found")
		// An empty list takes everything this provider brought.
		set(one, map[string]any{"groups": []string{}})
		one.signIn2(t, a.newClient(""), "one", "ann", nil)
		if got := groupNames(t, ann); got != "By hand" {
			t.Fatal(got)
		}
		// A second provider's values add to the same group; one's removal keeps the other's.
		set(one, map[string]any{"groups": []string{"5f1c-engineering"}})
		one.signIn2(t, a.newClient(""), "one", "ann", nil)
		two.addUser("ann", testidp.Claims{"sub": "sub-ann-2", "preferred_username": "ann", "email": "ann@example.test", "roles": []string{"dev", "x"}})
		// two does not know her: link it from her session, which also syncs.
		if loc := two.signIn2(t, ann, "two", "ann", map[string]any{"intent": "link", "return": "/settings"}); loc != "/settings" {
			t.Fatal(loc)
		}
		if got := groupNames(t, ann); got != "By hand,Engineering,Other" {
			t.Fatal(got)
		}
		set(one, map[string]any{"groups": []string{}})
		one.signIn2(t, a.newClient(""), "one", "ann", nil)
		if got := groupNames(t, ann); got != "By hand,Engineering,Other" {
			t.Fatalf("provider two's membership went with provider one's: %s", got)
		}
		ann.do("GET", spacePath(team)+"/members", nil).want(t, 200, "")

		// A missing claim keeps the groups and says so; "clear" empties them.
		set(one, map[string]any{"groups": []string{"/staff"}})
		one.signIn2(t, a.newClient(""), "one", "ann", nil)
		set(one, nil)
		idp := one.idp.Users["ann"]
		delete(idp, "groups")
		one.signIn2(t, a.newClient(""), "one", "ann", nil)
		if got := groupNames(t, ann); got != "By hand,Engineering,Other,Staff" {
			t.Fatal(got)
		}
		m := lastAudit(t, admin, "groups_claim_missing")["detail"].(map[string]any)
		if m["provider"] != "one" || m["missing"] != "keep" || m["reason"] != "absent" {
			t.Fatalf("%v", m)
		}
		// Entra's overage: a pointer instead of the list.
		idp["_claim_names"] = map[string]any{"groups": "src1"}
		idp["_claim_sources"] = map[string]any{"src1": map[string]any{"endpoint": "https://graph.example.test/users/1/getMemberObjects"}}
		if loc := one.signIn2(t, a.newClient(""), "one", "ann", nil); loc != "/" {
			t.Fatal(loc)
		}
		if m = lastAudit(t, admin, "groups_claim_missing")["detail"].(map[string]any); m["reason"] != "overage" || m["missing"] != "keep" {
			t.Fatalf("%v %v", m, admin.audit(t, "groups_claim_missing"))
		}
		if got := groupNames(t, ann); got != "By hand,Engineering,Other,Staff" {
			t.Fatal(got)
		}
		admin.do("PATCH", "/api/admin/auth/providers/one", map[string]any{"groups": map[string]any{"missing": "clear"}}).want(t, 200, "")
		one.signIn2(t, a.newClient(""), "one", "ann", nil)
		if got := groupNames(t, ann); got != "By hand,Engineering,Other" { // provider one's Staff goes; two's stay
			t.Fatal(got)
		}
		if m = lastAudit(t, admin, "groups_claim_missing")["detail"].(map[string]any); m["missing"] != "clear" {
			t.Fatalf("%v", m)
		}

		// More values than are read: the first 1,000, and a row says so.
		many := make([]string, 1100)
		for i := range many {
			many[i] = fmt.Sprintf("v%d", i)
		}
		many[1050] = "/staff" // past the limit: ignored
		many[3] = "5f1c-engineering"
		set(one, map[string]any{"groups": many})
		delete(one.idp.Users["ann"], "_claim_names")
		delete(one.idp.Users["ann"], "_claim_sources")
		one.signIn2(t, a.newClient(""), "one", "ann", nil)
		if tr := lastAudit(t, admin, "groups_claim_truncated")["detail"].(map[string]any); tr["provider"] != "one" || tr["limit"] != "values" {
			t.Fatalf("%v", tr)
		}
		if got := groupNames(t, ann); got != "By hand,Engineering,Other" {
			t.Fatal(got)
		}

		// A mapping entry taken out removes what it brought, at once, from everyone — not what another value brought.
		set(one, map[string]any{"groups": []string{"5f1c-engineering", "/staff"}})
		one.signIn2(t, a.newClient(""), "one", "ann", nil)
		admin.do("PATCH", "/api/admin/auth/providers/one", map[string]any{"groups": map[string]any{"map": gmap("/staff", staff)}}).want(t, 200, "")
		if got := groupNames(t, ann); got != "By hand,Engineering,Other,Staff" { // Engineering stays through provider two
			t.Fatal(got)
		}
		admin.do("PATCH", "/api/admin/auth/providers/one", map[string]any{"groups": map[string]any{"map": gmap("/staff", other)}}).want(t, 200, "")
		if got := groupNames(t, ann); got != "By hand,Engineering,Other" { // pointed elsewhere: the old rows go, the new come at sign-in
			t.Fatal(got)
		}
		// Turning the sync off changes nothing; the person keeps what they have.
		admin.do("PATCH", "/api/admin/auth/providers/one", map[string]any{"groups": map[string]any{"enabled": false}}).want(t, 200, "")
		set(one, map[string]any{"groups": []string{}})
		one.signIn2(t, a.newClient(""), "one", "ann", nil)
		if got := groupNames(t, ann); got != "By hand,Engineering,Other" {
			t.Fatal(got)
		}
		// Deleting a provider takes what it brought; the identities stay.
		admin.do("DELETE", "/api/admin/auth/providers/two?force=1", nil).want(t, 204, "")
		if got := groupNames(t, ann); got != "By hand" {
			t.Fatal(got)
		}
		ann.do("GET", spacePath(team)+"/members", nil).want(t, 404, "not_found")
	})
}

func TestGroupsSyncAtInvite(t *testing.T) {
	t.Parallel()
	one := newIDPFor(t, "one")
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		admin := a.setup("kazuha", pw)
		eng := admin.mkGroup(t, "Engineering", "synced")
		admin.do("POST", "/api/admin/auth/providers", one.providerBody("one", map[string]any{"groups": map[string]any{"enabled": true, "map": gmap("eng", eng)}})).want(t, 201, "")
		one.addUser("bob", testidp.Claims{"sub": "sub-bob", "preferred_username": "bob", "groups": []string{"eng"}})
		token := admin.do("POST", "/api/invites", nil).want(t, 201, "").body["token"].(string)
		c := a.newClient("")
		if loc := one.signIn2(t, c, "one", "bob", map[string]any{"intent": "invite", "token": token, "return": "/welcome"}); loc != "/welcome" {
			t.Fatal(loc)
		}
		if got := groupNames(t, c); got != "Engineering" {
			t.Fatal(got)
		}
	})
}

func samlAttrs(name string, values ...string) crewjamsaml.Attribute {
	at := crewjamsaml.Attribute{Name: name, NameFormat: "urn:oasis:names:tc:SAML:2.0:attrname-format:basic"}
	for _, v := range values {
		at.Values = append(at.Values, crewjamsaml.AttributeValue{Type: "xs:string", Value: v})
	}
	return at
}

func TestSAMLGroupsSyncAtSignIn(t *testing.T) {
	t.Parallel()
	idp := newFakeSAML(t, false)
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		admin := samlFixture(t, a, idp, map[string]any{"autoCreate": true})
		eng, staff := admin.mkGroup(t, "Engineering", "synced"), admin.mkGroup(t, "Staff", "synced")
		// An attribute name is needed to turn it on.
		admin.do("PATCH", "/api/admin/auth/providers/campus", map[string]any{"groups": map[string]any{"enabled": true}}).want(t, 400, "invalid")
		const attr = "http://schemas.microsoft.com/ws/2008/06/identity/claims/groups"
		admin.do("PATCH", "/api/admin/auth/providers/campus", map[string]any{"groups": map[string]any{"enabled": true, "claim": attr, "map": gmap("g-eng", eng, "g-staff", staff)}}).want(t, 200, "")
		with := func(values ...string) {
			idp.setSession(func(x *crewjamsaml.Session) {
				x.CustomAttributes = []crewjamsaml.Attribute{samlAttr("mail", "saml.person@example.test"), samlAttr("displayName", "Saml Person"), samlAttr("uid", "Saml.Person")}
				if values != nil {
					x.CustomAttributes = append(x.CustomAttributes, samlAttrs(attr, values...))
				}
			})
		}
		signIn := func(c *client, extra map[string]any) {
			t.Helper()
			at := idp.begin(t, c, samlSlug, extra)
			if r := c.postACS(samlSlug, at.response, at.relay, at.cookie); r.code != 303 || strings.Contains(r.resp.Header.Get("Location"), "sso_error") {
				t.Fatalf("%d %s", r.code, r.resp.Header.Get("Location"))
			}
		}
		c := a.newClient("")
		with("g-eng", "g-staff", "g-other")
		signIn(c, nil)
		if got := groupNames(t, c); got != "Engineering,Staff" {
			t.Fatal(got)
		}
		with("g-staff")
		signIn(a.newClient(""), nil)
		if got := groupNames(t, c); got != "Staff" {
			t.Fatal(got)
		}
		d := lastAudit(t, admin, "groups_synced")["detail"].(map[string]any)
		if d["provider"] != "campus" || len(d["removed"].([]any)) != 1 || d["removed"].([]any)[0] != eng {
			t.Fatalf("%v", d)
		}
		// No attribute at all: kept, with a row.
		with()
		signIn(a.newClient(""), nil)
		if got := groupNames(t, c); got != "Staff" {
			t.Fatal(got)
		}
		if m := lastAudit(t, admin, "groups_claim_missing")["detail"].(map[string]any); m["provider"] != "campus" || m["reason"] != "absent" || m["missing"] != "keep" {
			t.Fatalf("%v", m)
		}
		// An attribute with no values is an empty list.
		idp.setSession(func(x *crewjamsaml.Session) {
			x.CustomAttributes = []crewjamsaml.Attribute{samlAttr("mail", "saml.person@example.test"), samlAttr("uid", "Saml.Person"), samlAttrs(attr)}
		})
		signIn(a.newClient(""), nil)
		if got := groupNames(t, c); got != "" {
			t.Fatal(got)
		}
	})
}

func TestGitHubGroupsAreOrganisations(t *testing.T) {
	t.Parallel()
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		gh := newFakeGitHub(t)
		gh.users["m"] = ghFake{ID: 7, Login: "mona", Email: "mona@example.test", Verified: true, Orgs: []string{"KazuhaHub", "Elsewhere"}}
		admin := a.setup("kazuha", pw)
		hub, else_ := admin.mkGroup(t, "Hub", "synced"), admin.mkGroup(t, "Else", "synced")
		body := gh.body("gh", map[string]any{"autoCreate": true, "groups": map[string]any{"enabled": true, "map": gmap("KazuhaHub", hub, "Elsewhere", else_)}})
		body["github"].(map[string]any)["organizations"] = []string{"KazuhaHub"}
		admin.do("POST", "/api/admin/auth/providers", body).want(t, 201, "")
		c := a.newClient("")
		if end, _ := gh.signIn(t, c, "gh", "m", nil); end != "/" {
			t.Fatal(end)
		}
		if got := groupNames(t, c); got != "Else,Hub" {
			t.Fatal(got)
		}
		gh.mu.Lock()
		o := gh.users["m"]
		o.Orgs = []string{"KazuhaHub"}
		gh.users["m"] = o
		gh.mu.Unlock()
		gh.signIn(t, a.newClient(""), "gh", "m", nil)
		if got := groupNames(t, c); got != "Hub" {
			t.Fatal(got)
		}
	})
}

// The organisation that lets a person in is on the first page of /user/orgs and
// a later page fails: the sign-in goes on, with the groups claim missing.
func TestGitHubOrgsLaterPageFails(t *testing.T) {
	t.Parallel()
	eachAccounts(t, providersOpts(), func(t *testing.T, a *acc) {
		gh := newFakeGitHub(t)
		orgs := []string{"KazuhaHub"}
		for i := 0; i < 150; i++ {
			orgs = append(orgs, fmt.Sprintf("org%03d", i))
		}
		gh.users["m"] = ghFake{ID: 7, Login: "mona", Email: "mona@example.test", Verified: true, Orgs: orgs}
		gh.failOrgsFrom = 2
		admin := a.setup("kazuha", pw)
		hub := admin.mkGroup(t, "Hub", "synced")
		body := gh.body("gh", map[string]any{"autoCreate": true, "groups": map[string]any{"enabled": true, "map": gmap("KazuhaHub", hub)}})
		body["github"].(map[string]any)["organizations"] = []string{"KazuhaHub"}
		admin.do("POST", "/api/admin/auth/providers", body).want(t, 201, "")
		c := a.newClient("")
		if end, _ := gh.signIn(t, c, "gh", "m", nil); end != "/" {
			t.Fatal(end)
		}
		if got := groupNames(t, c); got != "" {
			t.Fatal(got)
		}
		if d := lastAudit(t, admin, "groups_claim_missing")["detail"].(map[string]any); d["provider"] != "gh" {
			t.Fatal(d)
		}
		// Without the organisation on a page that was read, a failure still refuses.
		gh.users["n"] = ghFake{ID: 8, Login: "nemo", Email: "nemo@example.test", Verified: true, Orgs: append(orgs[1:], "KazuhaHub")}
		if end, _ := gh.signIn(t, a.newClient(""), "gh", "n", nil); end == "/" {
			t.Fatal("signed in without the organisation being read")
		}
	})
}
