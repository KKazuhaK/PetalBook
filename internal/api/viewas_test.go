package api_test

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"petalbook.app/server/internal/api"
	"petalbook.app/server/internal/store"
)

// Viewing as a person (docs/design/3-6-c.md section 6).

const viewAsHeader = "PetalBook-View-As"

// The viewable routes are reads outside the person's own areas: a GET, never
// under the account, the people and administration, notifications,
// preferences, connections, sync or MCP.
func TestViewableRoutesAreReadsOutsideThePersonsOwnAreas(t *testing.T) {
	t.Parallel()
	forbidden := []string{"/api/admin", "/api/users", "/api/account", "/api/notifications", "/api/prefs", "/api/account/links", "/api/sync", "/mcp", "/api/oauth", "/api/invites"}
	n := 0
	for _, r := range api.Routes() {
		if !r.Viewable {
			continue
		}
		n++
		name := r.Method + " " + r.Pattern
		if r.Method != "GET" {
			t.Errorf("%s is viewable and is not a GET", name)
		}
		for _, p := range forbidden {
			if strings.HasPrefix(r.Pattern, p) {
				t.Errorf("%s is viewable and is under %s", name, p)
			}
		}
		for _, bad := range []string{"/favorites", "/prefs", "/draft", "/sync", "/changes", "/remote", "/members", "/people", "/export"} {
			if strings.Contains(r.Pattern, bad) {
				t.Errorf("%s is viewable and reads the person's own or a space's administration (%s)", name, bad)
			}
		}
	}
	if n == 0 {
		t.Fatal("no route is viewable")
	}
}

var pathParam = regexp.MustCompile(`\{[^}]+\}`)

// Every route that is not viewable refuses a request in a view, whoever sends
// it and whatever it names, before anything else: a write never runs as the
// person or as the administrator.
func TestEveryRouteThatIsNotViewableRefusesAView(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		root := a.setup("root", pw)
		for _, r := range api.Routes() {
			if r.Viewable {
				continue
			}
			p := pathParam.ReplaceAllString(r.Pattern, fakeID)
			var body any
			if r.Method != "GET" {
				body = map[string]any{}
			}
			// The body type the server asks of the route, so the refusal is the view's.
			extra := []string{viewAsHeader, "va_x"}
			switch {
			case r.Upload:
				extra = append(extra, "Content-Type", "application/octet-stream")
			case strings.HasSuffix(r.Pattern, "/acs"):
				extra = append(extra, "Content-Type", "application/x-www-form-urlencoded")
			}
			if got := root.do(r.Method, p, body, extra...); got.code != 403 || got.body["error"] != "view_as_read_only" {
				t.Errorf("%s %s in a view: %d %s", r.Method, r.Pattern, got.code, got.raw)
			}
		}
	})
}

// viewAsFx is the tenant fixture with a page and a draft of A's member in A's
// space, and a page in their personal space.
type viewAsFx struct {
	*tenFx
	page, rev, personal string
}

func newViewAsFx(t *testing.T, a *acc) *viewAsFx {
	t.Helper()
	f := &viewAsFx{tenFx: newTenFx(t, a)}
	p := newPage(t, f.memA, f.spaceA, "Shared")
	f.page, f.rev = p["id"].(string), p["rev"].(string)
	f.memA.do("PUT", spacePath(f.spaceA)+"/pages/"+f.page+"/draft", map[string]any{"baseRev": f.rev, "meta": map[string]any{"title": "Secret draft"}, "body": "unsaved\n"}).want(t, 200, "")
	f.personal = f.memA.personalSpace()
	newPage(t, f.memA, f.personal, "Diary")
	return f
}

func (f *viewAsFx) start(t *testing.T, c *client, user string, extra ...string) string {
	t.Helper()
	r := c.do("POST", "/api/admin/users/"+user+"/view-as", map[string]any{"reason": "Support ticket 12"}, extra...).want(t, 200, "")
	return r.body["id"].(string)
}

func TestViewAsAPersonReadsTheirOrganisationsSpacesOnly(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newViewAsFx(t, a)
		path := "/api/admin/users/" + f.userA + "/view-as"
		// A reason is required.
		f.admA.do("POST", path, map[string]any{}).want(t, 400, "invalid")
		f.admA.do("POST", path, map[string]any{"reason": strings.Repeat("x", 501)}).want(t, 400, "invalid")
		f.memA.do("POST", "/api/admin/users/"+f.userB+"/view-as", map[string]any{"reason": "x"}).want(t, 403, "forbidden")

		start := a.clock.Now()
		r := f.admA.do("POST", path, map[string]any{"reason": "Support ticket 12"}).want(t, 200, "")
		id := r.body["id"].(string)
		if until, _ := time.Parse(time.RFC3339, r.body["until"].(string)); until.Sub(start) != time.Hour {
			t.Fatalf("%s", r.raw)
		}
		as := []string{viewAsHeader, id}

		// The session is the person's public profile, with no keys and no address.
		s := f.admA.do("GET", "/api/session", nil, as...).want(t, 200, "")
		u := s.body["user"].(map[string]any)
		if u["username"] != "mem-a" || u["email"] != "" || len(u["permissions"].([]any)) != 0 {
			t.Fatalf("%s", s.raw)
		}
		va := s.body["viewAs"].(map[string]any)
		if va["id"] != id || va["viewer"].(map[string]any)["name"] == "" || va["hidden"].(float64) < 1 {
			t.Fatalf("%s", s.raw)
		}
		// The organisation's space, read only; never the personal one.
		var listed []string
		for _, it := range f.admA.do("GET", "/api/spaces", nil, as...).want(t, 200, "").body["spaces"].([]any) {
			sp := it.(map[string]any)
			listed = append(listed, sp["id"].(string))
			if perms := sp["permissions"].([]any); len(perms) != 1 || perms[0] != "read" {
				t.Fatalf("%v", sp)
			}
		}
		if !slices.Contains(listed, f.spaceA) || slices.Contains(listed, f.personal) {
			t.Fatalf("spaces in the view: %v", listed)
		}
		f.admA.do("GET", spacePath(f.personal)+"/pages", nil, as...).want(t, 404, "not_found")
		f.admA.do("GET", spacePath(f.spaceB)+"/pages", nil, as...).want(t, 404, "not_found")
		f.admA.do("GET", "/api/settings?space="+f.personal, nil, as...).want(t, 404, "not_found")

		// The page, never the person's draft of it.
		pg := f.admA.do("GET", spacePath(f.spaceA)+"/pages/"+f.page, nil, as...).want(t, 200, "")
		if _, ok := pg.body["draft"]; ok || strings.Contains(pg.raw, "unsaved") {
			t.Fatalf("a view read a draft: %s", pg.raw)
		}
		f.admA.do("GET", spacePath(f.spaceA)+"/tree", nil, as...).want(t, 200, "")
		f.admA.do("GET", spacePath(f.spaceA)+"/pages/"+f.page+"/history", nil, as...).want(t, 200, "")
		// The person's draft is still there for them.
		if d := f.memA.do("GET", spacePath(f.spaceA)+"/pages/"+f.page, nil).want(t, 200, "").body["draft"]; d == nil {
			t.Fatal("the person's draft is gone")
		}

		// Nothing else: no write, nothing of the person's own.
		f.admA.do("PUT", spacePath(f.spaceA)+"/pages/"+f.page, map[string]any{"baseRev": f.rev, "meta": map[string]any{"title": "x"}, "body": "x"}, as...).want(t, 403, "view_as_read_only")
		f.admA.do("PUT", spacePath(f.spaceA)+"/pages/"+f.page+"/draft", map[string]any{"baseRev": f.rev, "meta": map[string]any{"title": "x"}, "body": "x"}, as...).want(t, 403, "view_as_read_only")
		for _, p := range []string{"/api/account", "/api/account/tokens", "/api/notifications", "/api/prefs", "/api/account/links", "/api/users/" + f.userA + "/sessions", spacePath(f.spaceA) + "/favorites"} {
			f.admA.do("GET", p, nil, as...).want(t, 403, "view_as_read_only")
		}
		f.admA.do("POST", "/api/account/password", map[string]any{"current": pw, "password": "another one 1"}, as...).want(t, 403, "view_as_read_only")

		// Bound to the session that opened it: another session, a token, two ids.
		other := a.newClient("")
		other.do("POST", "/api/login", map[string]any{"username": "adm-a", "password": pw}).want(t, 200, "")
		other.do("GET", "/api/spaces", nil, as...).want(t, 403, "view_as_ended")
		_, secret := mkToken(t, f.admA, "read", nil, f.spaceA)
		a.bearer(secret, "").do("GET", "/api/spaces", nil, as...).want(t, 403, "token_scope")
		f.admA.do("GET", "/api/spaces", nil, viewAsHeader, "va_unknown").want(t, 403, "view_as_ended")
		// Without the header the administrator is themself.
		if got := f.admA.do("GET", "/api/session", nil).want(t, 200, "").body["user"].(map[string]any)["username"]; got != "adm-a" {
			t.Fatalf("%v", got)
		}

		// Audited: the start with its reason, the page opened in it, the end.
		a.auth.FlushAudit(context.Background())
		started := f.admA.audit(t, "view_as_started")
		if len(started) != 1 {
			t.Fatalf("%v", started)
		}
		row := started[0]
		if d := row["detail"].(map[string]any); d["reason"] != "Support ticket 12" || d["viewAs"] != id || row["user"] != f.userA || row["capacity"] != "tenant_admin" || row["actor"] == f.userA {
			t.Fatalf("%v", row)
		}
		n := 0
		for _, pv := range f.admA.audit(t, "page_viewed") {
			if d := pv["detail"].(map[string]any); d["page"] == f.page && d["viewAs"] == id && pv["user"] == f.userA && pv["capacity"] == "tenant_admin" {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("page_viewed in the view: %d", n)
		}
		// The person is not told (owner decision pending).
		if got := f.memA.do("GET", "/api/notifications", nil).want(t, 200, "").raw; strings.Contains(got, "viewed_as") {
			t.Fatalf("the person was notified: %s", got)
		}

		f.admA.do("DELETE", path, nil).want(t, 204, "")
		f.admA.do("GET", "/api/spaces", nil, as...).want(t, 403, "view_as_ended")
		a.auth.FlushAudit(context.Background())
		if ended := f.admA.audit(t, "view_as_ended"); len(ended) != 1 || ended[0]["user"] != f.userA {
			t.Fatalf("%v", ended)
		}
	})
}

func TestViewAsEndsAfterAnHourAndWithTheKey(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newViewAsFx(t, a)
		as := []string{viewAsHeader, f.start(t, f.admA, f.userA)}
		a.clock.Advance(59 * time.Minute)
		f.admA.do("GET", "/api/spaces", nil, as...).want(t, 200, "")
		a.clock.Advance(2 * time.Minute)
		f.admA.do("GET", "/api/spaces", nil, as...).want(t, 403, "view_as_ended")

		// A new view replaces the session's last one.
		first := f.start(t, f.admA, f.userA)
		second := f.start(t, f.admA, f.userA)
		f.admA.do("GET", "/api/spaces", nil, viewAsHeader, first).want(t, 403, "view_as_ended")
		f.admA.do("GET", "/api/spaces", nil, viewAsHeader, second).want(t, 200, "")
		// The feature switched off takes the key with it: the view ends.
		f.root.putSetting("features.adminView", false).want(t, 200, "")
		f.admA.do("GET", "/api/spaces", nil, viewAsHeader, second).want(t, 403, "view_as_ended")
		f.admA.do("POST", "/api/admin/users/"+f.userA+"/view-as", map[string]any{"reason": "x"}).want(t, 403, "feature_off")
	})
}

func TestWhoMayBeViewedAs(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newViewAsFx(t, a)
		reason := map[string]any{"reason": "x"}
		rootID := mustUserID(t, a, "root")
		admAID := mustUserID(t, a, "adm-a")
		// Another tenant's person does not exist for a tenant administrator.
		f.admA.do("POST", "/api/admin/users/"+f.userB+"/view-as", reason).want(t, 404, "not_found")
		f.admB.do("POST", "/api/admin/users/"+f.userA+"/view-as", reason).want(t, 404, "not_found")
		// Never oneself; never a site administrator, for a tenant administrator.
		f.admA.do("POST", "/api/admin/users/"+admAID+"/view-as", reason).want(t, 400, "invalid")
		f.admA.do("POST", "/api/admin/users/"+rootID+"/view-as", reason).want(t, 403, "forbidden")
		// The site administrator may view anyone, in any tenant.
		f.start(t, f.root, admAID)
		f.start(t, f.root, f.userB)
		// In B's scope, the site administrator views B's person as B's
		// administrator would (the rows say a site administrator did); A's
		// person is not there.
		inB := []string{scopeHeader, "tenant:" + f.tenB}
		f.root.do("POST", "/api/admin/users/"+f.userA+"/view-as", reason, inB...).want(t, 404, "not_found")
		as := []string{viewAsHeader, f.start(t, f.root, f.userB, inB...)}
		var listed []string
		for _, it := range f.root.do("GET", "/api/spaces", nil, as...).want(t, 200, "").body["spaces"].([]any) {
			listed = append(listed, it.(map[string]any)["id"].(string))
		}
		if !slices.Contains(listed, f.spaceB) {
			t.Fatalf("%v", listed)
		}
		a.auth.FlushAudit(context.Background())
		for _, row := range f.root.audit(t, "view_as_started") {
			if row["capacity"] != "site_admin" {
				t.Fatalf("%v", row)
			}
		}
	})
}

// A page opened in two views (as one person, then another, or after the
// administrator's own space view) is a row for each view, not one an hour.
func TestEachViewAuditsThePagesItOpens(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newViewAsFx(t, a)
		for range 2 {
			as := []string{viewAsHeader, f.start(t, f.admA, f.userA)}
			f.admA.do("GET", spacePath(f.spaceA)+"/pages/"+f.page, nil, as...).want(t, 200, "")
			f.admA.do("GET", spacePath(f.spaceA)+"/pages/"+f.page, nil, as...).want(t, 200, "")
		}
		a.auth.FlushAudit(context.Background())
		ids := map[any]bool{}
		for _, pv := range f.admA.audit(t, "page_viewed") {
			if d := pv["detail"].(map[string]any); d["page"] == f.page {
				ids[d["viewAs"]] = true
			}
		}
		if len(ids) != 2 {
			t.Fatalf("rows for %d views, want 2: %v", len(ids), ids)
		}
	})
}

// The person is read again on every request: a disabled account ends the view.
func TestAViewEndsWhenThePersonIsDisabled(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newViewAsFx(t, a)
		as := []string{viewAsHeader, f.start(t, f.admA, f.userA)}
		f.admA.do("GET", "/api/spaces", nil, as...).want(t, 200, "")
		f.admA.do("PATCH", "/api/users/"+f.userA, map[string]any{"disabled": true}).want(t, 200, "")
		f.admA.do("GET", "/api/spaces", nil, as...).want(t, 403, "view_as_ended")
	})
}

// An administrator's key in another tenant, with that tenant's feature off,
// does not change what a view in their own tenant shows.
func TestAViewShowsSpacesWhereTheFeatureIsOn(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newViewAsFx(t, a)
		grantRole(t, a, "RB", store.RoleScopeTenant, f.tenB, store.GrantToUser, mustUserID(t, a, "adm-a"), "tenant.users.viewAs")
		f.root.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/features.adminView", map[string]any{"siteLock": true, "value": false}).want(t, 200, "")
		as := []string{viewAsHeader, f.start(t, f.admA, f.userA)}
		var listed []string
		for _, it := range f.admA.do("GET", "/api/spaces", nil, as...).want(t, 200, "").body["spaces"].([]any) {
			listed = append(listed, it.(map[string]any)["id"].(string))
		}
		if !slices.Contains(listed, f.spaceA) || slices.Contains(listed, f.spaceB) {
			t.Fatalf("%v", listed)
		}
	})
}
