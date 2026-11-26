package api_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"petalbook.app/server/internal/store"
)

// The acting scope (docs/design/3-6-c.md, section 5): a site administrator who
// sends PetalBook-Scope: tenant:<id> acts exactly as that tenant's
// administrator. These tests hold a site administrator in B's scope (root,
// whose own tenant is A) against B's own administrator.

const scopeHeader = "PetalBook-Scope"

func names(t *testing.T, r result, list, field string) []string {
	t.Helper()
	var out []string
	for _, it := range r.body[list].([]any) {
		out = append(out, it.(map[string]any)[field].(string))
	}
	return out
}

func TestASiteAdministratorInATenantsScopeActsAsItsAdministrator(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		inB := []string{scopeHeader, "tenant:" + f.tenB}
		personalB := f.memB.personalSpace()

		// Every read a tenant administrator has answers the narrowed site
		// administrator as it answers B's administrator.
		for _, p := range []string{
			"/api/users", "/api/users/" + f.userB, "/api/users/" + f.userA,
			"/api/users/" + f.userB + "/sessions", "/api/users/" + f.userB + "/links",
			"/api/invites", "/api/admin/usage", "/api/admin/spaces", "/api/admin/spaces/" + f.spaceB,
			"/api/admin/spaces/" + f.spaceA, "/api/admin/spaces/" + personalB, "/api/admin/storage",
			"/api/admin/storage/people", "/api/admin/recycle", "/api/admin/groups", "/api/admin/groups/" + f.groupB,
			"/api/admin/groups/" + f.groupA, "/api/admin/tokens", "/api/admin/grants", "/api/admin/exports",
			"/api/admin/tenants/" + f.tenB, "/api/admin/tenants/" + f.tenB + "/settings",
			"/api/admin/spaces?tenant=" + store.DefaultTenantID, "/api/users/" + f.userB + "/passkeys",
		} {
			want := f.admB.do("GET", p, nil).code
			if got := f.root.do("GET", p, nil, inB...); got.code != want {
				t.Errorf("GET %s: the site administrator in B's scope got %d, B's administrator %d: %s", p, got.code, want, got.raw)
			}
		}

		// Only B's people, spaces and groups; no personal space.
		users := names(t, f.root.do("GET", "/api/users", nil, inB...).want(t, 200, ""), "users", "username")
		if !slices.Contains(users, "mem-b") || slices.Contains(users, "mem-a") || slices.Contains(users, "root") {
			t.Fatalf("users in B's scope: %v", users)
		}
		spaces := names(t, f.root.do("GET", "/api/admin/spaces", nil, inB...).want(t, 200, ""), "items", "id")
		if !slices.Contains(spaces, f.spaceB) || slices.Contains(spaces, f.spaceA) || slices.Contains(spaces, personalB) {
			t.Fatalf("spaces in B's scope: %v", spaces)
		}
		f.root.do("GET", "/api/admin/spaces/"+personalB, nil, inB...).want(t, 404, "not_found")
		f.root.do("GET", "/api/admin/tenants/"+store.DefaultTenantID, nil, inB...).want(t, 404, "")

		// Site routes are refused; the same caller without the header is not.
		for _, rt := range [][2]string{
			{"GET", "/api/admin/settings"}, {"GET", "/api/admin/tenants"}, {"GET", "/api/admin/secrets"},
			{"GET", "/api/admin/status"}, {"GET", "/api/admin/plans"}, {"GET", "/api/admin/providers"},
			{"DELETE", "/api/admin/recycle/" + fakeID},
		} {
			f.root.do(rt[0], rt[1], nil, inB...).want(t, 403, "forbidden")
		}
		f.root.do("GET", "/api/admin/settings", nil).want(t, 200, "")

		// Writes reach B and nothing else.
		f.root.do("PATCH", "/api/users/"+f.userB, map[string]any{"name": "Member B2"}, inB...).want(t, 200, "")
		f.root.do("PATCH", "/api/users/"+f.userA, map[string]any{"name": "nope"}, inB...).want(t, 404, "")

		// A transfer inside B works as for B's administrator. A takeover would
		// make root a member across tenants, which only the site's scope does;
		// there it still works, and a personal space is never taken over.
		f.root.do("POST", "/api/admin/spaces/"+f.spaceB+"/transfer", map[string]any{"userId": f.userB}, inB...).want(t, 204, "")
		f.root.do("POST", "/api/admin/spaces/"+f.spaceA+"/transfer", map[string]any{"userId": f.userB}, inB...).want(t, 404, "")
		f.root.do("POST", "/api/admin/spaces/"+f.spaceB+"/takeover", map[string]any{"reason": "the tenant asked"}, inB...).want(t, 403, "forbidden")
		f.root.do("POST", "/api/admin/spaces/"+personalB+"/takeover", map[string]any{"reason": "x"}).want(t, 409, "personal_space")
		f.root.do("POST", "/api/admin/spaces/"+f.spaceB+"/takeover", map[string]any{"reason": "the tenant asked"}).want(t, 204, "")

		// The tenant's features bind them as they bind its administrator.
		f.root.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/features.adminView", map[string]any{"siteLock": true, "value": false}).want(t, 200, "")
		view := map[string]any{"reason": "a check"}
		f.admB.do("POST", "/api/admin/spaces/"+f.spaceB+"/view", view).want(t, 403, "feature_off")
		f.root.do("POST", "/api/admin/spaces/"+f.spaceB+"/view", view, inB...).want(t, 403, "feature_off")
		f.root.do("POST", "/api/admin/spaces/"+f.spaceB+"/view", view).want(t, 200, "")

		a.auth.FlushAudit(context.Background())
		// Opening the scope is recorded once (many requests above), on B's rows,
		// where B's administrator sees it.
		if rows := f.admB.audit(t, "tenant_console_opened"); len(rows) != 1 || rows[0]["capacity"] != "site_admin" || rows[0]["actor"] != mustUserID(t, a, "root") {
			t.Fatalf("tenant_console_opened: %v", rows)
		}
		if rows := f.admB.audit(t, "space_taken_over"); len(rows) != 1 || rows[0]["capacity"] != "site_admin" {
			t.Fatalf("the takeover's row: %v", rows)
		}
		if rows := f.admB.audit(t, "space_transferred"); len(rows) != 1 || rows[0]["capacity"] != "site_admin" {
			t.Fatalf("the transfer's row: %v", rows)
		}
	})
}

// The header narrows and never widens: only who may read the tenant's settings
// there may send it, it is for administration routes only, and a malformed or
// unknown one is refused rather than ignored.
func TestTheScopeHeaderNeverWidens(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		inB := []string{scopeHeader, "tenant:" + f.tenB}
		inA := []string{scopeHeader, "tenant:" + store.DefaultTenantID}

		f.memB.do("GET", "/api/users", nil, inB...).want(t, 404, "not_found")
		f.memB.do("GET", "/api/admin/tenants/"+f.tenB, nil, inB...).want(t, 404, "not_found")
		f.admA.do("GET", "/api/users", nil, inB...).want(t, 404, "not_found")
		f.admB.do("GET", "/api/users", nil, inA...).want(t, 404, "not_found")
		// One's own tenant changes nothing for its administrator.
		f.admB.do("GET", "/api/users", nil, inB...).want(t, 200, "")
		f.admA.do("GET", "/api/users", nil, inA...).want(t, 200, "")

		f.root.do("GET", "/api/users", nil, scopeHeader, "tenant:"+fakeID).want(t, 404, "not_found")
		for _, bad := range []string{"tenant:", "site", f.tenB, "tenant: " + f.tenB, "group:" + f.tenB} {
			f.root.do("GET", "/api/users", nil, scopeHeader, bad).want(t, 400, "invalid")
		}
		// Not for a person's own routes, nor for a space's content.
		f.root.do("GET", "/api/spaces", nil, inB...).want(t, 400, "invalid")
		f.root.do("GET", "/api/account", nil, inB...).want(t, 400, "invalid")
		f.root.do("GET", "/api/spaces/"+f.spaceB+"/pages", nil, inB...).want(t, 400, "invalid")
		f.root.do("POST", "/api/spaces", map[string]any{"name": "x"}, inB...).want(t, 400, "invalid")
		f.tokA.do("GET", "/api/spaces/"+f.spaceA+"/pages", nil, inA...).want(t, 400, "invalid")
	})
}

// Every audit row written by administration records the capacity the actor
// used: site_admin for a site administrator (in a tenant's scope too),
// tenant_admin for a tenant's administrator, none for one's own account.
func TestAuditRowsRecordTheCapacity(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		inB := []string{scopeHeader, "tenant:" + f.tenB}
		f.admB.do("PATCH", "/api/users/"+f.userB, map[string]any{"name": "by B"}).want(t, 200, "")
		f.root.do("PATCH", "/api/users/"+f.userB, map[string]any{"name": "by root"}).want(t, 200, "")
		f.root.do("PATCH", "/api/users/"+f.userB, map[string]any{"name": "by root in B"}, inB...).want(t, 200, "")
		f.memA.do("PATCH", "/api/account", map[string]any{"name": "me"}).want(t, 200, "")
		// Rows written by the accounts service (a notice and an audit row in one
		// transaction, and a queued row).
		f.root.do("DELETE", "/api/users/"+f.userB+"/sessions", nil).want(t, 200, "")
		f.admB.do("POST", "/api/users/"+f.userB+"/password", map[string]any{"password": "Another-pass-1"}).want(t, 204, "")
		a.auth.FlushAudit(context.Background())

		capOf := func(kind string) map[string]any {
			out := map[string]any{}
			for _, r := range f.root.audit(t, kind) {
				d, _ := r["detail"].(map[string]any)
				if n, ok := d["name"].(string); ok {
					out[n] = r["capacity"]
				} else {
					out[kind] = r["capacity"]
				}
			}
			return out
		}
		got := capOf("user_changed")
		want := map[string]any{"by B": "tenant_admin", "by root": "site_admin", "by root in B": "site_admin", "me": nil}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("user_changed %q: capacity %v, want %v (all: %v)", k, got[k], v, got)
			}
		}
		if c := capOf("password_set_by_admin")["password_set_by_admin"]; c != "tenant_admin" {
			t.Errorf("password_set_by_admin: capacity %v", c)
		}
		if c := capOf("sessions_ended")["sessions_ended"]; c != "site_admin" {
			t.Errorf("sessions_ended: capacity %v", c)
		}
		// A capacity filter finds them.
		r := f.root.do("GET", "/api/admin/audit?capacity=tenant_admin&kind=user_changed", nil).want(t, 200, "")
		if n := len(r.body["items"].([]any)); n != 1 {
			t.Fatalf("capacity=tenant_admin: %d rows", n)
		}
	})
}

// No handler reads the signed-in person's tenant or scope directly: the acting
// scope (scope.go) is the one place that says which tenant a request acts in,
// so a site administrator narrowed to a tenant is narrowed everywhere. An
// alias counts: a name given c.user() or c.p.User in the same function
// (u := c.user(); u.TenantID) is the person as well.
func TestNoHandlerReadsThePersonsTenantDirectly(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "scope.go" {
			continue
		}
		fset := token.NewFileSet()
		af, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, pos := range personTenantReads(af) {
			t.Errorf("%s: %s", fset.Position(pos.at), pos.what)
		}
	}
}

// The scan itself finds direct reads and aliases, and leaves other values be.
func TestTheTenantScanFindsAliases(t *testing.T) {
	t.Parallel()
	const src = `package api
func (s *server) a(c *call) { _ = c.user().TenantID }
func (s *server) b(c *call) { u := c.user(); _ = u.TenantID }
func (s *server) d(c *call) { var u = c.p.User; _ = u.IsSiteAdmin() }
func (s *server) e(c *call) { u, x := c.user(), 1; _ = x; _ = store.ScopeOf(u) }
func (s *server) g(c *call) { var u store.User; u = c.user(); go func() { _ = u.TenantID }() }
func (s *server) ok(c *call) { u := other(); _ = u.TenantID; _ = c.tenant() }
`
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, "x.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var lines []int
	for _, r := range personTenantReads(af) {
		lines = append(lines, fset.Position(r.at).Line)
	}
	if !slices.Equal(lines, []int{2, 3, 4, 5, 6}) {
		t.Fatalf("found on lines %v", lines)
	}
}

type tenantRead struct {
	at   token.Pos
	what string
}

// personTenantReads is every read of the person's tenant in a file: .TenantID
// or .IsSiteAdmin, or store.ScopeOf / AdminScopeOf, of c.user(), c.p.User, or
// a name a function gave either.
func personTenantReads(af *ast.File) []tenantRead {
	isCallUser := func(e ast.Expr) bool {
		// c.user(), or c.p.User
		if call, ok := e.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "user" && len(call.Args) == 0 {
				return true
			}
		}
		if sel, ok := e.(*ast.SelectorExpr); ok && sel.Sel.Name == "User" {
			if in, ok := sel.X.(*ast.SelectorExpr); ok && in.Sel.Name == "p" {
				return true
			}
		}
		return false
	}
	var out []tenantRead
	for _, d := range af.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		// The names given the person anywhere in the function (closures included).
		alias := map[string]bool{}
		bind := func(lhs []ast.Expr, rhs []ast.Expr) {
			if len(lhs) != len(rhs) {
				return
			}
			for i, r := range rhs {
				if id, ok := lhs[i].(*ast.Ident); ok && isCallUser(r) {
					alias[id.Name] = true
				}
			}
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.AssignStmt:
				bind(x.Lhs, x.Rhs)
			case *ast.ValueSpec:
				lhs := make([]ast.Expr, len(x.Names))
				for i, id := range x.Names {
					lhs[i] = id
				}
				bind(lhs, x.Values)
			}
			return true
		})
		isPerson := func(e ast.Expr) bool {
			if id, ok := e.(*ast.Ident); ok {
				return alias[id.Name]
			}
			return isCallUser(e)
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				if (x.Sel.Name == "TenantID" || x.Sel.Name == "IsSiteAdmin") && isPerson(x.X) {
					out = append(out, tenantRead{x.Pos(), "reads the person's " + x.Sel.Name + ": use c.tenant() or a key (scope.go)"})
				}
			case *ast.CallExpr:
				if sel, ok := x.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "ScopeOf" || sel.Sel.Name == "AdminScopeOf") {
					if p, ok := sel.X.(*ast.Ident); ok && p.Name == "store" && len(x.Args) == 1 && isPerson(x.Args[0]) {
						out = append(out, tenantRead{x.Pos(), "store." + sel.Sel.Name + " of the person: use c.reach() or c.adminReach() (scope.go)"})
					}
				}
			}
			return true
		})
	}
	return out
}

// The accounts service decides by the request's subject, never one rebuilt
// from the person: in a tenant's scope a custom site role brings no site key
// back, and an administrator of A holding a custom role in B holds there only
// what it grants, not B's built-in administrator.
func TestTheAccountsServiceDecidesByTheRequestsSubject(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		inB := []string{scopeHeader, "tenant:" + f.tenB}
		admBID := mustUserID(t, a, "adm-b")
		f.root.do("PATCH", "/api/users/"+admBID, map[string]any{"siteAdmin": true}).want(t, 200, "")
		grantRole(t, a, "RS", store.RoleScopeSite, "", store.GrantToUser, mustUserID(t, a, "root"),
			"site.admins.manage", "site.storage.manage", "tenant.invites.manage")

		// Site actions are refused in B's scope, whatever site role root holds.
		f.root.do("PATCH", "/api/users/"+f.userB, map[string]any{"remoteExempt": true}, inB...).want(t, 403, "forbidden")
		f.root.do("PATCH", "/api/users/"+admBID, map[string]any{"name": "nope"}, inB...).want(t, 403, "forbidden")
		f.root.do("POST", "/api/users/"+admBID+"/password", map[string]any{"password": "Another-pass-1"}, inB...).want(t, 403, "forbidden")
		f.root.do("POST", "/api/invites", map[string]any{"role": "admin", "siteAdmin": true}, inB...).want(t, 403, "forbidden")
		f.root.do("POST", "/api/invites", map[string]any{"tenant": store.DefaultTenantID}, inB...).want(t, 404, "not_found")
		f.root.do("POST", "/api/admin/members/batch", map[string]any{"tenant": store.DefaultTenantID, "dryRun": true,
			"rows": []map[string]any{{"user": "x@example.test"}}}, inB...).want(t, 404, "not_found")
		// Outside the scope they are root's.
		f.root.do("PATCH", "/api/users/"+f.userB, map[string]any{"remoteExempt": true}).want(t, 200, "")

		// A's administrator with a custom role in B holds there what it grants.
		grantRole(t, a, "RB", store.RoleScopeTenant, f.tenB, store.GrantToUser, mustUserID(t, a, "adm-a"),
			"tenant.settings.read", "tenant.users.read", "tenant.users.manage")
		f.admA.do("PATCH", "/api/users/"+f.userB, map[string]any{"name": "by A in B"}, inB...).want(t, 200, "")
		f.admA.do("POST", "/api/invites", map[string]any{"role": "member"}, inB...).want(t, 403, "forbidden")
		f.admA.do("POST", "/api/admin/members/batch", map[string]any{"dryRun": true, "rows": []map[string]any{{"user": "x@example.test"}}}, inB...).want(t, 403, "forbidden")
	})
}

// Entering a tenant's scope is recorded only for a request its route lets
// through: a refusal by the route's key or feature writes nothing.
func TestARefusedRequestRecordsNoConsoleOpening(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		inB := []string{scopeHeader, "tenant:" + f.tenB}
		f.root.do("PUT", "/api/admin/tenants/"+f.tenB+"/settings/features.adminView", map[string]any{"siteLock": true, "value": false}).want(t, 200, "")
		f.root.do("GET", "/api/admin/settings", nil, inB...).want(t, 403, "forbidden")
		f.root.do("POST", "/api/admin/spaces/"+f.spaceB+"/view", map[string]any{"reason": "a check"}, inB...).want(t, 403, "feature_off")
		a.auth.FlushAudit(context.Background())
		if rows := f.admB.audit(t, "tenant_console_opened"); len(rows) != 0 {
			t.Fatalf("refused requests wrote %v", rows)
		}
		f.root.do("GET", "/api/users", nil, inB...).want(t, 200, "")
		a.auth.FlushAudit(context.Background())
		if rows := f.admB.audit(t, "tenant_console_opened"); len(rows) != 1 {
			t.Fatalf("an allowed request wrote %v", rows)
		}
	})
}

// ?scope=tenant:<id> is the header for what a browser fetches by its address
// (the audit's CSV): on an administration route's GET only, read exactly as the
// header is, so it narrows and never widens; with the header too, or on a
// write, it is refused. Elsewhere the name is not the scope's and is ignored.
func TestTheScopeQueryParameterIsTheHeader(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newTenFx(t, a)
		inB := "scope=tenant:" + f.tenB
		a.auth.FlushAudit(context.Background())

		// The site administrator's download in B's scope holds B's rows only.
		whole := f.root.do("GET", "/api/admin/audit/download", nil).want(t, 200, "")
		if !strings.Contains(whole.raw, ","+store.DefaultTenantID+",") {
			t.Fatalf("the whole site's download has no row of A: %s", whole.raw)
		}
		r := f.root.do("GET", "/api/admin/audit/download?"+inB, nil).want(t, 200, "")
		lines := strings.Split(strings.TrimSpace(r.raw), "\n")
		if len(lines) < 2 {
			t.Fatalf("no rows in B's scope: %s", r.raw)
		}
		for _, l := range lines[1:] {
			if cells := strings.Split(l, ","); len(cells) < 6 || cells[5] != f.tenB {
				t.Errorf("a row outside B's scope: %s", l)
			}
		}
		users := names(t, f.root.do("GET", "/api/users?"+inB, nil).want(t, 200, ""), "users", "username")
		if slices.Contains(users, "mem-a") || !slices.Contains(users, "mem-b") {
			t.Fatalf("users in B's scope: %v", users)
		}

		// It never widens, and is validated as the header is.
		f.admA.do("GET", "/api/admin/audit/download?"+inB, nil).want(t, 404, "not_found")
		f.memB.do("GET", "/api/users?"+inB, nil).want(t, 404, "not_found")
		f.root.do("GET", "/api/admin/settings?"+inB, nil).want(t, 403, "forbidden")
		for _, bad := range []string{"tenant:", "site", f.tenB} {
			f.root.do("GET", "/api/users?scope="+bad, nil).want(t, 400, "invalid")
		}
		f.root.do("GET", "/api/users?"+inB+"&"+inB, nil).want(t, 400, "invalid")
		f.root.do("GET", "/api/users?"+inB, nil, scopeHeader, "tenant:"+f.tenB).want(t, 400, "invalid")
		// Not for a write: the header is how a write names its scope.
		f.root.do("PATCH", "/api/users/"+f.userB+"?"+inB, map[string]any{"name": "x"}).want(t, 400, "invalid")
		// A person's own routes do not read it.
		f.root.do("GET", "/api/account?"+inB, nil).want(t, 200, "")
	})
}
