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
			"/api/users/" + f.userB + "/sessions", "/api/users/" + f.userB + "/identities",
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
			{"GET", "/api/admin/status"}, {"GET", "/api/admin/plans"}, {"GET", "/api/admin/auth/providers"},
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
// so a site administrator narrowed to a tenant is narrowed everywhere.
func TestNoHandlerReadsThePersonsTenantDirectly(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
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
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "scope.go" {
			continue
		}
		fset := token.NewFileSet()
		af, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				if (x.Sel.Name == "TenantID" || x.Sel.Name == "IsSiteAdmin") && isCallUser(x.X) {
					t.Errorf("%s: reads the person's %s: use c.tenant() or a key (scope.go)", fset.Position(x.Pos()), x.Sel.Name)
				}
			case *ast.CallExpr:
				if sel, ok := x.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "ScopeOf" || sel.Sel.Name == "AdminScopeOf") {
					if p, ok := sel.X.(*ast.Ident); ok && p.Name == "store" && len(x.Args) == 1 && isCallUser(x.Args[0]) {
						t.Errorf("%s: store.%s of the person: use c.reach() or c.adminReach() (scope.go)", fset.Position(x.Pos()), sel.Sel.Name)
					}
				}
			}
			return true
		})
	}
}
