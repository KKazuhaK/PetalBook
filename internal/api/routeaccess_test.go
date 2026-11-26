package api_test

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"petalbook.app/server/internal/api"
	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// The access decision of every route, for four callers, pinned in a golden
// file written from main before the permission keys replaced the admin and
// siteAdmin levels. The keys must decide exactly as the levels did; the only
// differences are the ones listed in routeAccessChanged, each with its reason,
// and the routes added since, in routeAccessAdded.

var updateRouteAccess = flag.Bool("update-route-access", false, "rewrite testdata/route_access.golden")

const routeAccessGolden = "testdata/route_access.golden"

// routeAccessCallers are the columns of the golden file.
var routeAccessCallers = []string{"site admin", "tenant admin", "member", "token"}

// routeAccessChanged are the decisions that differ from the golden file on
// purpose: route -> caller -> the decision now.
//
// 3.6 part 1 closed the administrators' way to a space's remote: an
// administrator who is not a member of the space managed it with members and
// manage, which let them point its remote at a repository of their own. The
// remote is now for members only, and an administrator takes the space over
// first (POST /api/admin/spaces/{space}/takeover). The same holds for its
// owners and for giving themselves a role, which the handler refuses (the
// route is still reached): see TestATenantAdministratorTakesASpaceOverOnlyExplicitly.
var routeAccessChanged = map[string]map[string]string{}

func init() {
	for _, r := range []string{
		"POST /api/spaces/{space}/remote/key",
		"PUT /api/spaces/{space}/remote",
		"DELETE /api/spaces/{space}/remote",
		"POST /api/spaces/{space}/remote/accept",
		"PUT /api/spaces/{space}/remote/assets",
		"DELETE /api/spaces/{space}/remote/assets",
		"POST /api/spaces/{space}/remote/assets/move",
	} {
		routeAccessChanged[r] = map[string]string{"site admin": "403 takeover_required", "tenant admin": "403 takeover_required"}
	}
	// A route that declares a feature is refused while the tenant does not have it (3.6 part 6): more
	// tenants are off in this fixture, so a site administrator is told so, as the route's key comes first.
	routeAccessChanged["POST /api/admin/tenants"] = map[string]string{"site admin": "403 feature_off"}
}

// routeAccessAdded are the routes added after the golden file was written,
// with their decision for each caller.
var routeAccessAdded = map[string][4]string{
	"GET /api/admin/permissions":              {"ok", "ok", "403 forbidden", "403 token_scope"},
	"POST /api/admin/spaces/{space}/takeover": {"ok", "ok", "403 forbidden", "403 token_scope"},

	// A temporary password for a person.
	"POST /api/users/{id}/password": {"ok", "ok", "403 forbidden", "403 token_scope"},
	// A batch of people by file, and the picker's search.
	"GET /api/people":                              {"ok", "ok", "ok", "403 token_scope"},
	"POST /api/admin/members/batch":                {"ok", "ok", "403 forbidden", "403 token_scope"},
	"POST /api/admin/groups/{group}/members/batch": {"ok", "ok", "403 forbidden", "403 token_scope"},
	"POST /api/spaces/{space}/members/batch":       {"ok", "ok", "403 no_permission", "403 token_scope"},
	"GET /api/spaces/{space}/people":               {"ok", "ok", "403 no_permission", "403 token_scope"},

	// 3.6 part 4: the plans. Reading a tenant's rules is the tenant's administrators' too (its settings).
	"PUT /api/admin/tenants/{tenant}/plan": {"ok", "403 forbidden", "403 forbidden", "403 token_scope"},
	"GET /api/admin/plans":                 {"ok", "403 forbidden", "403 forbidden", "403 token_scope"},
	"POST /api/admin/plans":                {"ok", "403 forbidden", "403 forbidden", "403 token_scope"},
	"GET /api/admin/plans/{slug}":          {"ok", "403 forbidden", "403 forbidden", "403 token_scope"},
	"PATCH /api/admin/plans/{slug}":        {"ok", "403 forbidden", "403 forbidden", "403 token_scope"},
	"DELETE /api/admin/plans/{slug}":       {"ok", "403 forbidden", "403 forbidden", "403 token_scope"},

	// 3.6 part 5: the secrets of the site (its administrators) and of a tenant
	// (its own administrators, and the site's).
	"GET /api/admin/secrets":                            {"ok", "403 forbidden", "403 forbidden", "403 token_scope"},
	"PUT /api/admin/secrets/{name}":                     {"ok", "403 forbidden", "403 forbidden", "403 token_scope"},
	"DELETE /api/admin/secrets/{name}":                  {"ok", "403 forbidden", "403 forbidden", "403 token_scope"},
	"GET /api/admin/tenants/{tenant}/secrets":           {"ok", "ok", "403 forbidden", "403 token_scope"},
	"PUT /api/admin/tenants/{tenant}/secrets/{name}":    {"ok", "ok", "403 forbidden", "403 token_scope"},
	"DELETE /api/admin/tenants/{tenant}/secrets/{name}": {"ok", "ok", "403 forbidden", "403 token_scope"},
}

type routeAccessFx struct {
	a                       *acc
	site, tenant, mem, tok  *client
	space, ownerID, ownerUN string
}

func newRouteAccessFx(t *testing.T) *routeAccessFx {
	t.Helper()
	needGit(t)
	a := newAcc(t, storetest.Open(t, "sqlite"), accOpts{})
	f := &routeAccessFx{a: a}
	f.site = a.setup("root", pw)
	if _, err := a.auth.CreateUser(context.Background(), auth.CreateParams{Username: "adm", Role: "admin", Password: pw, TenantID: store.DefaultTenantID}); err != nil {
		t.Fatal(err)
	}
	f.tenant = a.newClient("")
	f.tenant.do("POST", "/api/login", map[string]any{"username": "adm", "password": pw}).want(t, 200, "")
	owner := a.addUser("own", pw, "member")
	f.mem = a.addUser("mem", pw, "member")
	// A space neither administrator is a member of, the member an editor in it.
	f.space = owner.do("POST", "/api/spaces", map[string]any{"name": "Team"}).want(t, 201, "").body["id"].(string)
	owner.do("POST", "/api/spaces/"+f.space+"/members", map[string]any{"username": "mem", "role": "editor"}).want(t, 201, "")
	f.ownerID = mustUserID(t, a, "own")
	r := f.mem.do("POST", "/api/account/tokens", map[string]any{"name": "t", "access": "write", "spaces": []string{f.space}}).want(t, 201, "")
	f.tok = a.newClient("")
	f.tok.headers["Authorization"] = "Bearer " + r.body["secret"].(string)
	return f
}

// decide runs a route's access decision for the client: "ok" when it reaches
// the handler, else the status and error code.
func (f *routeAccessFx) decide(t *testing.T, c *client, rt api.RouteInfo) string {
	t.Helper()
	values := map[string]string{}
	var segs []string
	for _, s := range strings.Split(rt.Pattern, "/") {
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			name := strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(s, "{"), "}"), "...")
			v := fakeID
			switch name {
			case "space":
				v = f.space
			case "user":
				v = f.ownerID // never the caller: acting on oneself is its own case
			case "path":
				v = "a.md"
			}
			values[name] = v
			s = v
		}
		segs = append(segs, s)
	}
	req := httptest.NewRequest(rt.Method, strings.Join(segs, "/"), nil)
	req.Host = "notes.example.test"
	req.RemoteAddr = c.ip + ":4321"
	for k, v := range values {
		req.SetPathValue(k, v)
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	for _, ck := range c.cookies {
		req.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	api.AccessDecision(f.a.h, rt.Method, rt.Pattern, rec, req)
	if rec.Code == 299 {
		return "ok"
	}
	var body struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return fmt.Sprintf("%d %s", rec.Code, body.Error)
}

func TestRouteAccessDecisionsAreUnchanged(t *testing.T) {
	f := newRouteAccessFx(t)
	callers := []*client{f.site, f.tenant, f.mem, f.tok}
	now := map[string][4]string{}
	for _, rt := range api.Routes() {
		var d [4]string
		for i, c := range callers {
			d[i] = f.decide(t, c, rt)
		}
		now[rt.Method+" "+rt.Pattern] = d
	}

	if *updateRouteAccess {
		keys := make([]string, 0, len(now))
		for k := range now {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteString("# route\t" + strings.Join(routeAccessCallers, "\t") + "\n")
		for _, k := range keys {
			d := now[k]
			b.WriteString(k + "\t" + strings.Join(d[:], "\t") + "\n")
		}
		if err := os.MkdirAll(filepath.Dir(routeAccessGolden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(routeAccessGolden, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}

	raw, err := os.ReadFile(routeAccessGolden)
	if err != nil {
		t.Fatal(err)
	}
	golden := map[string][4]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		p := strings.Split(line, "\t")
		if len(p) != 5 {
			t.Fatalf("bad golden line %q", line)
		}
		golden[p[0]] = [4]string{p[1], p[2], p[3], p[4]}
	}
	for k, want := range golden {
		for who, d := range routeAccessChanged[k] {
			want[callerIndex(t, who)] = d
		}
		got, ok := now[k]
		if !ok {
			t.Errorf("%s: in the golden file, but no route any more", k)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s, %s: was %q, now %q", k, routeAccessCallers[i], want[i], got[i])
			}
		}
	}
	for k, got := range now {
		if _, ok := golden[k]; ok {
			continue
		}
		want, ok := routeAccessAdded[k]
		if !ok {
			t.Errorf("%s: a new route; add it to routeAccessAdded with its decisions %q", k, got)
			continue
		}
		if got != want {
			t.Errorf("%s: want %q, got %q", k, want, got)
		}
	}
	for k := range routeAccessChanged {
		if _, ok := golden[k]; !ok {
			t.Errorf("routeAccessChanged has %s, which the golden file does not", k)
		}
	}
}

func callerIndex(t *testing.T, who string) int {
	for i, c := range routeAccessCallers {
		if c == who {
			return i
		}
	}
	t.Fatalf("no caller %q", who)
	return 0
}
