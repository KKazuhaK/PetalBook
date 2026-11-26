package auditlog_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"petalbook.app/server/internal/auditlog"
)

// Every audit kind the code writes is registered. The scan reads the source of
// the module (not its tests) for the places a kind is named: the Kind of an
// AuditEvent or AuditRow, the kind argument of the server's audit helpers, the
// audit word of a notify.Event, and the strings a function assigns to the kind
// variable it then writes. A kind that is written and not registered would be
// filed under no category, so the filter and the trim would not know it.
func TestEveryKindWrittenIsRegistered(t *testing.T) {
	t.Parallel()
	found := scanKinds(t, "../..")
	if len(found) < 60 {
		t.Fatalf("the scan found only %d kinds: it no longer sees the writers", len(found))
	}
	var missing []string
	for k, where := range found {
		if _, ok := auditlog.Lookup(k); !ok {
			missing = append(missing, k+" ("+where+")")
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("audit kinds written but not registered in internal/auditlog:\n  %s", strings.Join(missing, "\n  "))
	}
	// And the other way: a registered kind that nothing writes is a typo or a
	// leftover (the kinds reserved for later parts are listed as such).
	for _, k := range auditlog.Kinds() {
		if _, ok := found[k.Name]; !ok && !auditlog.Reserved(k.Name) {
			t.Errorf("kind %q is registered and nothing writes it (mark it reserved if a later part will)", k.Name)
		}
	}
}

func scanKinds(t *testing.T, root string) map[string]string {
	t.Helper()
	found := map[string]string{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", "web", ".claude", "auditlog":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.Contains(string(src), "udit") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return err
		}
		add := func(e ast.Expr, pos token.Pos) {
			if s, ok := lit(e); ok {
				found[s] = fset.Position(pos).String()
			}
		}
		for _, decl := range f.Decls {
			fn, _ := decl.(*ast.FuncDecl)
			ast.Inspect(decl, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.CompositeLit:
					switch typeName(n.Type) {
					case "AuditEvent", "AuditRow":
						for _, el := range n.Elts {
							kv, ok := el.(*ast.KeyValueExpr)
							if !ok || !isIdent(kv.Key, "Kind") {
								continue
							}
							if id, ok := kv.Value.(*ast.Ident); ok && fn != nil && fn.Body != nil {
								for _, s := range assignedLiterals(fn, id.Name) {
									found[s] = fset.Position(kv.Pos()).String()
								}
								continue
							}
							add(kv.Value, kv.Pos())
						}
					case "Event":
						if len(n.Elts) == 2 {
							add(n.Elts[1], n.Pos())
						}
					}
				case *ast.CallExpr:
					switch callName(n.Fun) {
					case "auditRow", "by":
						i := 1
						if callName(n.Fun) == "by" {
							i = 0
						}
						if len(n.Args) > i {
							add(n.Args[i], n.Pos())
						}
					case "auditGroup":
						if len(n.Args) > 2 {
							add(n.Args[2], n.Pos())
						}
					}
				case *ast.AssignStmt:
					for i, l := range n.Lhs {
						if sel, ok := l.(*ast.SelectorExpr); ok && sel.Sel.Name == "Kind" && i < len(n.Rhs) {
							if x, ok := sel.X.(*ast.Ident); ok && x.Name == "ev" {
								add(n.Rhs[i], n.Pos())
							}
						}
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func lit(e ast.Expr) (string, bool) {
	b, ok := e.(*ast.BasicLit)
	if !ok || b.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(b.Value)
	return s, err == nil
}

func isIdent(e ast.Expr, name string) bool { id, ok := e.(*ast.Ident); return ok && id.Name == name }

func typeName(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	}
	return ""
}

func callName(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	}
	return ""
}

// assignedLiterals are the string literals fn assigns to the variable name.
func assignedLiterals(fn *ast.FuncDecl, name string) []string {
	var out []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		a, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, l := range a.Lhs {
			if isIdent(l, name) && i < len(a.Rhs) {
				if s, ok := lit(a.Rhs[i]); ok {
					out = append(out, s)
				}
			}
		}
		return true
	})
	return out
}
