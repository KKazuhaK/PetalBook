package api_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/api"
	"github.com/KKazuhaK/PetalBook/internal/auth"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// The isolation fixture of milestone 3.3: two tenants holding the same shapes
// of data, and every route of the API called by tenant A's administrator, member
// and API token with tenant B's ids. Each answer must be the one for an id that
// does not exist, and no list may hold anything of B.

// tenFx is the fixture.
type tenFx struct {
	a    *acc
	root *client // a site administrator (tenant A, the default one)

	// tenant A's people
	admA, memA, tokA *client
	// tenant B's
	admB, memB *client

	tenB                                           string
	userB, spaceB, groupB, inviteB, tokenB, grantB string
	userA, spaceA, groupA, inviteA                 string
	secretB                                        string
}

// fakeID is an id of the right form that nothing has.
const fakeID = "0000000ZZZ"

func newTenFx(t *testing.T, a *acc) *tenFx {
	t.Helper()
	ctx := context.Background()
	f := &tenFx{a: a}
	f.root = a.setup("root", pw)
	f.root.do("PUT", "/api/admin/tenancy", map[string]any{"enabled": true}).want(t, 200, "")

	// Tenant B, with its first administrator through the invite the creation gives.
	r := f.root.do("POST", "/api/admin/tenants", map[string]any{"slug": "acme", "name": "Acme", "admin": map[string]any{"username": "adm-b"}}).want(t, 201, "")
	f.tenB = r.body["id"].(string)
	inv := r.body["invite"].(map[string]any)
	if inv["tenant"] != f.tenB || inv["siteAdmin"] != false || inv["role"] != "admin" {
		t.Fatalf("%v", inv)
	}
	f.admB = a.newClient("")
	f.admB.do("POST", "/api/invites/accept", map[string]any{"token": inv["token"], "username": "adm-b", "name": "Admin B", "password": pw}).want(t, 201, "")
	ri := f.admB.do("POST", "/api/invites", map[string]any{"role": "member"}).want(t, 201, "")
	f.memB = a.newClient("")
	f.memB.do("POST", "/api/invites/accept", map[string]any{"token": ri.body["token"], "username": "mem-b", "name": "Member B", "password": pw}).want(t, 201, "")
	f.inviteB = f.admB.do("POST", "/api/invites", map[string]any{"role": "member"}).want(t, 201, "").body["id"].(string)

	// Tenant A: the site administrator is in it too; a tenant administrator and a member.
	if _, err := a.auth.CreateUser(ctx, auth.CreateParams{Username: "adm-a", Role: "admin", Password: pw, TenantID: store.DefaultTenantID}); err != nil {
		t.Fatal(err)
	}
	f.admA = a.newClient("")
	f.admA.do("POST", "/api/login", map[string]any{"username": "adm-a", "password": pw}).want(t, 200, "")
	f.memA = a.addUser("mem-a", pw, "member")
	f.inviteA = f.admA.do("POST", "/api/invites", map[string]any{"role": "member"}).want(t, 201, "").body["id"].(string)

	f.userA, f.userB = mustUserID(t, a, "mem-a"), mustUserID(t, a, "mem-b")
	f.spaceA = f.admA.do("POST", "/api/spaces", map[string]any{"name": "Space A"}).want(t, 201, "").body["id"].(string)
	f.spaceB = f.admB.do("POST", "/api/spaces", map[string]any{"name": "Space B"}).want(t, 201, "").body["id"].(string)
	f.admA.do("POST", "/api/spaces/"+f.spaceA+"/members", map[string]any{"username": "mem-a", "role": "editor"}).want(t, 201, "")
	f.admB.do("POST", "/api/spaces/"+f.spaceB+"/members", map[string]any{"username": "mem-b", "role": "editor"}).want(t, 201, "")
	f.groupA = f.admA.mkGroup(t, "Group A", "local")
	f.groupB = f.admB.mkGroup(t, "Group B", "local")
	f.groupAdd(t, f.admA, f.groupA, f.userA)
	f.groupAdd(t, f.admB, f.groupB, f.userB)
	f.admA.do("POST", "/api/spaces/"+f.spaceA+"/groups", map[string]any{"groupId": f.groupA, "role": "viewer"}).want(t, 201, "")
	f.admB.do("POST", "/api/spaces/"+f.spaceB+"/groups", map[string]any{"groupId": f.groupB, "role": "viewer"}).want(t, 201, "")

	// Tokens: A's member has one; B's has one (with its secret, for the tokens' own tests).
	ra := f.memA.do("POST", "/api/account/tokens", map[string]any{"name": "a", "access": "read", "spaces": []string{f.spaceA}}).want(t, 201, "")
	f.tokA = a.newClient("")
	f.tokA.headers["Authorization"] = "Bearer " + ra.body["secret"].(string)
	rb := f.memB.do("POST", "/api/account/tokens", map[string]any{"name": "b", "access": "read", "spaces": []string{f.spaceB}}).want(t, 201, "")
	f.tokenB = rb.body["token"].(map[string]any)["id"].(string)
	f.secretB = rb.body["secret"].(string)

	// An OAuth grant of B's member.
	now := a.clock.Now()
	f.grantB = "GRANTB0001"
	if err := store.InsertOAuthClient(ctx, a.st.Q(), store.OAuthClient{ID: "CLIENTB001", Name: "client", RedirectURIs: []string{"https://c.example/cb"}, Fingerprint: "fp-b", Created: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertOAuthGrant(ctx, a.st.Q(), store.OAuthGrant{ID: f.grantB, UserID: f.userB, ClientID: "CLIENTB001", ClientName: "client B", Access: "read",
		Resource: "https://notes.example.test/mcp", Created: now, LastUsed: now, RefreshHash: "rh", Spaces: []store.TokenSpace{{ID: f.spaceB}}}); err != nil {
		t.Fatal(err)
	}
	a.auth.FlushAudit(ctx)
	return f
}

func (f *tenFx) groupAdd(t *testing.T, c *client, g string, ids ...string) {
	t.Helper()
	c.groupAdd(t, g, ids...).want(t, 200, "")
}

// bIDs are everything of tenant B that must never show in an answer to A.
func (f *tenFx) bIDs() []string {
	return []string{f.tenB, f.userB, f.spaceB, f.groupB, f.inviteB, f.tokenB, f.grantB, "adm-b", "mem-b", "Space B", "Group B", "Acme", f.secretB}
}

// --- the table of routes -----------------------------------------------------------

type isoKind int

const (
	isoParam  isoKind = iota + 1 // an id of the path or body: B's id must answer as a made-up one
	isoList                      // a list: nothing of B in it, and ?tenant=B as for a made-up tenant
	isoSite                      // the server's own: a tenant administrator is refused
	isoExempt                    // nothing of another tenant can be named, with the reason
)

type isoEntry struct {
	kind   isoKind
	reason string
	// path and body build the request for an id (B's, or a made-up one); nil
	// uses the pattern's parameters by name.
	body func(f *tenFx, id string) any
	ids  func(f *tenFx) map[string]string // parameters of the path (name -> B's id)
}

// isolationRoutes has an entry for every route that is not a space route (those
// are all isoParam by their {space}). A route added to the API without an entry
// here fails TestEveryRouteIsInTheIsolationTable.
var isolationRoutes = map[string]isoEntry{
	"GET /api/meta":                        {kind: isoList},
	"GET /api/session":                     {kind: isoExempt, reason: "the caller's own user"},
	"POST /api/setup":                      {kind: isoExempt, reason: "public: nothing of a tenant"},
	"POST /api/login":                      {kind: isoExempt, reason: "public: the account that signs in decides the tenant"},
	"POST /api/logout":                     {kind: isoExempt, reason: "the caller's own session"},
	"POST /api/auth/oidc/start":            {kind: isoExempt, reason: "public sign-in"},
	"GET /api/auth/oidc/callback":          {kind: isoExempt, reason: "public sign-in"},
	"POST /api/invites/check":              {kind: isoExempt, reason: "the secret token of an invite is the capability, in any tenant"},
	"POST /api/invites/accept":             {kind: isoExempt, reason: "the secret token of an invite is the capability, in any tenant"},
	"GET /api/auth/providers":              {kind: isoExempt, reason: "public; ?tenant= is tested apart"},
	"GET /api/captcha":                     {kind: isoExempt, reason: "public"},
	"GET /api/auth/{kind}/{slug}/start":    {kind: isoExempt, reason: "public sign-in"},
	"POST /api/auth/{kind}/{slug}/start":   {kind: isoExempt, reason: "public sign-in"},
	"GET /api/auth/oidc/{slug}/callback":   {kind: isoExempt, reason: "public sign-in"},
	"GET /api/auth/github/{slug}/callback": {kind: isoExempt, reason: "public sign-in"},
	"POST /api/auth/saml/{slug}/acs":       {kind: isoExempt, reason: "public sign-in"},
	"GET /api/auth/saml/{slug}/metadata":   {kind: isoExempt, reason: "public"},
	"POST /api/auth/passkey/begin":         {kind: isoExempt, reason: "public sign-in"},
	"POST /api/auth/passkey/finish":        {kind: isoExempt, reason: "public sign-in"},

	"GET /api/account":                    {kind: isoExempt, reason: "the caller's own account"},
	"PATCH /api/account":                  {kind: isoExempt, reason: "the caller's own account"},
	"POST /api/account/password":          {kind: isoExempt, reason: "the caller's own account"},
	"DELETE /api/account/identities/{id}": {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.userB} }},
	"GET /api/account/tokens":             {kind: isoList},
	"POST /api/account/tokens":            {kind: isoExempt, reason: "a token for the caller's own spaces (a space of another tenant is not one of them: tested in the tenant tests)"},
	"DELETE /api/account/tokens/{id}":     {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.tokenB} }},
	"GET /api/account/grants":             {kind: isoList},
	"DELETE /api/account/grants/{id}":     {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.grantB} }},
	"GET /api/oauth/consent":              {kind: isoExempt, reason: "the caller's own consent screen"},
	"POST /api/oauth/consent":             {kind: isoExempt, reason: "the caller's own consent"},
	"GET /api/account/passkeys":           {kind: isoExempt, reason: "the caller's own passkeys"},
	"POST /api/account/passkeys/begin":    {kind: isoExempt, reason: "the caller's own passkeys"},
	"POST /api/account/passkeys/finish":   {kind: isoExempt, reason: "the caller's own passkeys"},
	"PATCH /api/account/passkeys/{id}":    {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.userB} }},
	"DELETE /api/account/passkeys/{id}":   {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.userB} }},

	"GET /api/groups":   {kind: isoList},
	"GET /api/settings": {kind: isoList},
	"GET /api/spaces":   {kind: isoList},
	"POST /api/spaces":  {kind: isoExempt, reason: "makes a space of the caller's own tenant"},

	"GET /api/users":                            {kind: isoList},
	"PATCH /api/users/{id}":                     {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.userB} }, body: func(f *tenFx, id string) any { return map[string]any{"disabled": true} }},
	"POST /api/invites":                         {kind: isoParam, body: func(f *tenFx, id string) any { return map[string]any{"role": "member", "tenant": id} }, ids: func(f *tenFx) map[string]string { return map[string]string{} }},
	"GET /api/invites":                          {kind: isoList},
	"DELETE /api/invites/{id}":                  {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.inviteB} }},
	"GET /api/users/{id}/passkeys":              {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.userB} }},
	"DELETE /api/users/{id}/passkeys/{passkey}": {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.userB, "passkey": fakeID} }},

	"GET /api/admin/spaces":                           {kind: isoList},
	"GET /api/admin/spaces/{space}":                   {kind: isoParam},
	"POST /api/admin/spaces/{space}/size":             {kind: isoParam},
	"POST /api/admin/spaces/{space}/transfer":         {kind: isoParam, body: func(f *tenFx, id string) any { return map[string]any{"userId": f.userB} }},
	"POST /api/admin/spaces/{space}/disable":          {kind: isoParam},
	"POST /api/admin/spaces/{space}/enable":           {kind: isoParam},
	"DELETE /api/admin/spaces/{space}":                {kind: isoParam},
	"GET /api/admin/groups":                           {kind: isoList},
	"POST /api/admin/groups":                          {kind: isoList},
	"GET /api/admin/groups/{group}":                   {kind: isoParam},
	"PATCH /api/admin/groups/{group}":                 {kind: isoParam, body: func(f *tenFx, id string) any { return map[string]any{"name": "renamed"} }},
	"DELETE /api/admin/groups/{group}":                {kind: isoParam},
	"GET /api/admin/groups/{group}/members":           {kind: isoParam},
	"POST /api/admin/groups/{group}/members":          {kind: isoParam, body: func(f *tenFx, id string) any { return map[string]any{"userIds": []string{f.userB}} }},
	"DELETE /api/admin/groups/{group}/members/{user}": {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"group": f.groupB, "user": f.userB} }},
	"GET /api/admin/groups/{group}/spaces":            {kind: isoParam},
	"GET /api/admin/tokens":                           {kind: isoList},
	"DELETE /api/admin/tokens/{id}":                   {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.tokenB} }},
	"GET /api/admin/grants":                           {kind: isoList},
	"DELETE /api/admin/grants/{id}":                   {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"id": f.grantB} }},
	"GET /api/admin/audit":                            {kind: isoList},
	"GET /api/admin/tenants/{tenant}":                 {kind: isoParam},
	"PATCH /api/admin/tenants/{tenant}":               {kind: isoParam, body: func(f *tenFx, id string) any { return map[string]any{"name": "taken over"} }},
	"GET /api/admin/tenants/{tenant}/settings":        {kind: isoParam},
	"PUT /api/admin/tenants/{tenant}/settings/{key}":  {kind: isoParam, ids: func(f *tenFx) map[string]string { return map[string]string{"tenant": f.tenB, "key": "legal.termsUrl"} }, body: func(f *tenFx, id string) any { return map[string]any{"value": "https://evil.example"} }},
}

func init() {
	// The server's own: every route marked site-admin-only is one entry.
	for _, r := range api.Routes() {
		if r.SiteAdmin {
			isolationRoutes[r.Method+" "+r.Pattern] = isoEntry{kind: isoSite}
		}
	}
}

func TestEveryRouteIsInTheIsolationTable(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range api.Routes() {
		key := r.Method + " " + r.Pattern
		seen[key] = true
		if r.Space {
			continue // by its {space}
		}
		if _, ok := isolationRoutes[key]; !ok {
			t.Errorf("%s is not in isolationRoutes: say how it keeps tenants apart (or why it needs no check)", key)
		}
	}
	for key, e := range isolationRoutes {
		if !seen[key] {
			t.Errorf("isolationRoutes has %s, which is no route any more", key)
		}
		if e.kind == isoExempt && e.reason == "" {
			t.Errorf("%s is exempt without a reason", key)
		}
	}
}

// build makes the request of a route: other picks B's ids, else made-up ones.
func (f *tenFx) build(rt api.RouteInfo, e isoEntry, other bool) (path string, body any) {
	val := func(name, bID string) string {
		if other {
			return bID
		}
		return fakeID
	}
	ids := map[string]string{}
	if e.ids != nil {
		ids = e.ids(f)
	}
	params := map[string]string{"space": f.spaceB, "group": f.groupB, "user": f.userB, "tenant": f.tenB, "id": f.userB, "key": "legal.termsUrl",
		"tag": "x", "name": "x", "commit": "abc", "path...": "x.md", "passkey": fakeID}
	path = rt.Pattern
	for name, bID := range params {
		if v, ok := ids[name]; ok {
			bID = v
		}
		keep := name == "key" || name == "tag" || name == "name" || name == "commit" || name == "path..." || name == "passkey"
		v := bID
		if !keep {
			v = val(name, bID)
		}
		if name == "path..." {
			path = strings.ReplaceAll(path, "{path...}", v)
		}
		path = strings.ReplaceAll(path, "{"+name+"}", v)
	}
	// A page id of the path is not another tenant's secret: the same either way.
	if strings.Contains(rt.Pattern, "/pages/{id}") {
		path = strings.ReplaceAll(rt.Pattern, "{id}", "0000000AAA")
		path = strings.ReplaceAll(path, "{space}", val("space", f.spaceB))
		for _, p := range []string{"{tag}", "{name}", "{commit}", "{group}", "{user}"} {
			path = strings.ReplaceAll(path, p, "x")
		}
		path = strings.ReplaceAll(path, "{path...}", "x.md")
	}
	body = map[string]any{}
	if e.body != nil {
		body = e.body(f, val("id", f.tenB))
	}
	if rt.Space && strings.Contains(rt.Pattern, "/groups") && rt.Method == "POST" {
		body = map[string]any{"groupId": val("group", f.groupB), "role": "viewer"}
	}
	if rt.Space && strings.HasSuffix(rt.Pattern, "/members") && rt.Method == "POST" {
		body = map[string]any{"username": "mem-b", "role": "viewer"}
	}
	if rt.Method == "GET" || rt.Method == "DELETE" && body != nil && len(body.(map[string]any)) == 0 {
		body = nil
	}
	return path, body
}

// shape is what an answer says that must not depend on whose id was asked.
func shape(r result) string {
	code, _ := r.body["error"].(string)
	return fmt.Sprintf("%d %s", r.code, code)
}

func TestTenantsAreIsolatedOnEveryRoute(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{oauth: true}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		callers := []struct {
			name string
			c    *client
		}{{"tenant administrator", f.admA}, {"member", f.memA}, {"token", f.tokA}}
		b := f.bIDs()
		checked := 0
		routes := api.Routes()
		sort.Slice(routes, func(i, j int) bool { return routes[i].Pattern+routes[i].Method < routes[j].Pattern+routes[j].Method })
		for _, rt := range routes {
			key := rt.Method + " " + rt.Pattern
			e, ok := isolationRoutes[key]
			if rt.Space {
				e, ok = isoEntry{kind: isoParam}, true
			}
			if !ok {
				continue
			}
			switch e.kind {
			case isoExempt:
				continue
			case isoSite:
				for _, cl := range callers[:2] {
					path := strings.NewReplacer("{tenant}", f.tenB, "{key}", "legal.termsUrl", "{id}", "x", "{slug}", "x").Replace(rt.Pattern)
					cl.c.do(rt.Method, path, "{}").want(t, 403, "forbidden")
				}
				continue
			case isoList:
				for _, cl := range callers {
					path := rt.Pattern
					if strings.Contains(path, "{") {
						continue
					}
					body := any(nil)
					if rt.Method != "GET" {
						body = map[string]any{"name": "x-" + cl.name, "source": "local"}
					}
					r := cl.c.do(rt.Method, path, body)
					for _, id := range b {
						if strings.Contains(r.raw, id) {
							t.Errorf("%s as %s: the answer names %q of tenant B: %s", key, cl.name, id, r.raw)
						}
					}
					if rt.Method == "GET" && rt.Admin && cl.name == "tenant administrator" {
						// ?tenant=B is as ?tenant=made-up.
						rb := cl.c.do("GET", path+"?tenant="+f.tenB, nil)
						rf := cl.c.do("GET", path+"?tenant="+fakeID, nil)
						if shape(rb) != shape(rf) || shape(rb) != "404 not_found" {
							t.Errorf("%s ?tenant=: B %s, made-up %s", key, shape(rb), shape(rf))
						}
					}
					checked++
				}
				if rt.Method == "POST" && rt.Pattern == "/api/admin/groups" {
					rb := f.admA.do("POST", "/api/admin/groups?tenant="+f.tenB, map[string]any{"name": "evil", "source": "local"})
					rf := f.admA.do("POST", "/api/admin/groups?tenant="+fakeID, map[string]any{"name": "evil", "source": "local"})
					if shape(rb) != "404 not_found" || shape(rb) != shape(rf) {
						t.Errorf("creating a group in another tenant: %s / %s", shape(rb), shape(rf))
					}
				}
			case isoParam:
				for _, cl := range callers {
					pb, bb := f.build(rt, e, true)
					pf, bf := f.build(rt, e, false)
					rb := cl.c.do(rt.Method, pb, bb)
					rf := cl.c.do(rt.Method, pf, bf)
					if shape(rb) != shape(rf) {
						t.Errorf("%s as %s: B's id answered %s, a made-up id %s\n  %s\n  %s", key, cl.name, shape(rb), shape(rf), rb.raw, rf.raw)
					}
					if rb.code < 400 {
						t.Errorf("%s as %s: B's id was accepted: %d %s", key, cl.name, rb.code, rb.raw)
					}
					for _, id := range b {
						if strings.Contains(rb.raw, id) {
							t.Errorf("%s as %s: the refusal names %q of tenant B: %s", key, cl.name, id, rb.raw)
						}
					}
					checked++
				}
			}
		}
		if checked < 150 {
			t.Fatalf("only %d calls were made: the table is not being read", checked)
		}
		// Nothing of B changed, and no link joins two tenants.
		a.auth.FlushAudit(context.Background())
		for q, want := range map[string]int{
			`SELECT COUNT(*) FROM spaces WHERE id = '` + f.spaceB + `' AND disabled = 0`:     1,
			`SELECT COUNT(*) FROM groups WHERE id = '` + f.groupB + `' AND name = 'Group B'`: 1,
			`SELECT COUNT(*) FROM users WHERE id = '` + f.userB + `' AND disabled = 0`:       1,
			`SELECT COUNT(*) FROM tenants WHERE id = '` + f.tenB + `' AND name = 'Acme'`:     1,
			`SELECT COUNT(*) FROM invites WHERE id = '` + f.inviteB + `'`:                    1,
		} {
			var n int
			if err := a.st.Q().QueryRow(context.Background(), q).Scan(&n); err != nil || n != want {
				t.Errorf("%s: %d %v", q, n, err)
			}
		}
		assertNoCrossTenantMemberships(t, a)
		if n := f.countOf(`SELECT COUNT(*) FROM api_tokens WHERE id = '` + f.tokenB + `' AND revoked = 0`); n != 1 {
			t.Errorf("B's token was revoked")
		}
		if n := f.countOf(`SELECT COUNT(*) FROM oauth_grants WHERE id = '` + f.grantB + `'`); n != 1 {
			t.Errorf("B's grant was revoked")
		}
	})
}

func (f *tenFx) countOf(q string) int {
	var n int
	if err := f.a.st.Q().QueryRow(context.Background(), q).Scan(&n); err != nil {
		f.a.t.Fatal(q, err)
	}
	return n
}

// assertNoCrossTenantMemberships is the query every test of the fixture ends
// with: no membership, group membership or place of a group in a space joins
// two tenants.
func assertNoCrossTenantMemberships(t *testing.T, a *acc) {
	t.Helper()
	for name, q := range map[string]string{
		"space_members": `SELECT COUNT(*) FROM space_members m JOIN spaces s ON s.id = m.space_id JOIN users u ON u.id = m.user_id WHERE s.tenant_id <> u.tenant_id`,
		"group_members": `SELECT COUNT(*) FROM group_members m JOIN groups g ON g.id = m.group_id JOIN users u ON u.id = m.user_id WHERE g.tenant_id <> u.tenant_id`,
		"space_groups":  `SELECT COUNT(*) FROM space_groups x JOIN spaces s ON s.id = x.space_id JOIN groups g ON g.id = x.group_id WHERE s.tenant_id <> g.tenant_id`,
		"token spaces":  `SELECT COUNT(*) FROM api_token_spaces x JOIN api_tokens k ON k.id = x.token_id JOIN users u ON u.id = k.user_id JOIN spaces s ON s.id = x.space_id WHERE s.tenant_id <> u.tenant_id`,
	} {
		var n int
		if err := a.st.Q().QueryRow(context.Background(), q).Scan(&n); err != nil {
			t.Fatal(name, err)
		}
		if n != 0 {
			t.Errorf("%d rows of %s join two tenants", n, name)
		}
	}
}

var _ = time.Second
