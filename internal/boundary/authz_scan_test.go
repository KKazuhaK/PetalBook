package boundary

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// What a caller may do is asked of their permission keys, through one path
// (internal/authz, and allow/can in internal/api), never of the account's role
// name or site-administrator flag (docs/design/auth-and-permissions.md §16).
// Rules about the account acted on ask store.User (IsAdmin, IsSiteAdmin).
//
// This test reads the syntax of every Go file of the server outside those two
// packages (no type checking: it parses, which takes a fraction of a second)
// and fails on:
//   - a comparison of a role with "admin" (x.Role == "admin", role != "admin");
//   - .SiteAdmin used as a condition: an operand of && or ||, negated, an if or
//     for condition, or compared with anything but nil. Copying the field
//     (SiteAdmin: req.SiteAdmin, a function argument) is not a condition;
//   - a switch on a role with a case "admin", and strings.EqualFold of a role
//     and "admin";
//   - a local the function reads .SiteAdmin into, used as such a condition;
//   - a call of isSiteAdmin, the helper this replaced.
var authzScanAllowed = []string{"internal/authz/", "internal/store/"}

func TestNoRoleChecksOutsideAuthz(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	var found []string
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				for _, a := range authzScanAllowed {
					if rel+"/" == a {
						return filepath.SkipDir
					}
				}
				if d.Name() == "testdata" || d.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			for _, pos := range roleChecks(f) {
				p := fset.Position(pos)
				found = append(found, rel+":"+strconv.Itoa(p.Line))
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range found {
		t.Errorf("%s: a check of a role name or the site-administrator flag; ask a permission key (authz, allow/can) for the caller, or store.User for the account acted on", f)
	}
}

// roleChecks finds the positions of the checks the test refuses in f, one
// function at a time: a local the function reads .SiteAdmin into is a
// condition wherever the field would be.
func roleChecks(f *ast.File) []token.Pos {
	var out []token.Pos
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
			out = append(out, roleChecksIn(fd.Body)...)
		}
	}
	return out
}

func roleChecksIn(body *ast.BlockStmt) []token.Pos {
	var out []token.Pos
	// The locals given .SiteAdmin (x := u.SiteAdmin, x = u.SiteAdmin, var x = u.SiteAdmin).
	locals := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			if len(n.Lhs) == len(n.Rhs) {
				for i, r := range n.Rhs {
					if id, ok := n.Lhs[i].(*ast.Ident); ok && isSiteAdminField(r) {
						locals[id.Name] = true
					}
				}
			}
		case *ast.ValueSpec:
			if len(n.Names) == len(n.Values) {
				for i, v := range n.Values {
					if isSiteAdminField(v) {
						locals[n.Names[i].Name] = true
					}
				}
			}
		}
		return true
	})
	cond := func(e ast.Expr) {
		if isSiteAdminField(e) {
			out = append(out, e.Pos())
		} else if id, ok := unparen(e).(*ast.Ident); ok && locals[id.Name] {
			out = append(out, e.Pos())
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SwitchStmt:
			if n.Tag != nil && isRole(n.Tag) {
				for _, s := range n.Body.List {
					for _, e := range s.(*ast.CaseClause).List {
						if isAdminLit(e) {
							out = append(out, e.Pos())
						}
					}
				}
			}
		case *ast.BinaryExpr:
			switch n.Op {
			case token.EQL, token.NEQ:
				if (isAdminLit(n.X) && isRole(n.Y)) || (isAdminLit(n.Y) && isRole(n.X)) {
					out = append(out, n.Pos())
				}
				if !isNil(n.X) && !isNil(n.Y) {
					cond(n.X)
					cond(n.Y)
				}
			case token.LAND, token.LOR:
				cond(n.X)
				cond(n.Y)
			}
		case *ast.UnaryExpr:
			if n.Op == token.NOT {
				cond(n.X)
			}
		case *ast.IfStmt:
			cond(n.Cond)
		case *ast.ForStmt:
			if n.Cond != nil {
				cond(n.Cond)
			}
		case *ast.CallExpr:
			if sel, ok := n.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "isSiteAdmin" {
				out = append(out, n.Pos())
			}
			if sel, ok := n.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "EqualFold" && len(n.Args) == 2 &&
				((isRole(n.Args[0]) && isAdminLit(n.Args[1])) || (isRole(n.Args[1]) && isAdminLit(n.Args[0]))) {
				out = append(out, n.Pos())
			}
		}
		return true
	})
	return out
}

func unparen(e ast.Expr) ast.Expr {
	for {
		switch x := e.(type) {
		case *ast.ParenExpr:
			e = x.X
		case *ast.StarExpr:
			e = x.X
		default:
			return e
		}
	}
}

func isSiteAdminField(e ast.Expr) bool {
	sel, ok := unparen(e).(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "SiteAdmin"
}

func isAdminLit(e ast.Expr) bool {
	lit, ok := unparen(e).(*ast.BasicLit)
	return ok && lit.Kind == token.STRING && lit.Value == `"admin"`
}

func isRole(e ast.Expr) bool {
	switch x := unparen(e).(type) {
	case *ast.SelectorExpr:
		return x.Sel.Name == "Role"
	case *ast.Ident:
		return strings.EqualFold(x.Name, "role")
	}
	return false
}

func isNil(e ast.Expr) bool {
	id, ok := unparen(e).(*ast.Ident)
	return ok && id.Name == "nil"
}

// The scan must see what it is meant to refuse, and leave copies of the field alone.
func TestRoleCheckScanFindsWhatItShould(t *testing.T) {
	src := `package x
func f() {
	if u.Role == "admin" {}
	_ = "admin" != role
	_ = u.SiteAdmin && ok
	_ = !(c.user().SiteAdmin)
	if p.SiteAdmin {}
	_ = u.SiteAdmin == other
	_ = c.isSiteAdmin()
	_ = User{SiteAdmin: req.SiteAdmin}
	g(u.SiteAdmin)
	_ = patch.SiteAdmin != nil
	_ = u.Role == "member"
	site := u.SiteAdmin
	_ = site
	switch u.Role {
	case "member", "admin":
	}
	switch role {
	case "member":
	}
	_ = strings.EqualFold(u.Role, "admin")
	sa := c.user().SiteAdmin
	if ok && sa {
	}
	var flag bool
	flag = p.SiteAdmin
	_ = !flag
}`
	f, err := parser.ParseFile(token.NewFileSet(), "x.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(roleChecks(f)); got != 11 {
		t.Fatalf("found %d checks, want 11", got)
	}
}
