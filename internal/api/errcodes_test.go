package api

import (
	"encoding/json"
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var updateCodes = flag.Bool("update-codes", false, "rewrite web/src/i18n/errorCodes.json from the source")

// codesFile is the list of every error code the server can send. The web app has
// a test that every code on it has a Chinese and an English text.
const codesFile = "../../web/src/i18n/errorCodes.json"

// The type names whose `Code` field is not an HTTP error code of the API: JSON-RPC
// and MCP tool errors are for agents, the notices and the problems of a file are shown by their own pages.
var notAPICode = map[string]bool{"mcpError": true, "jsonrpc.Error": true, "Notice": true, "page.Problem": true}

// errorCodesInSource finds every error code in the non-test Go files under
// internal/: the code argument of writeError and coded, the Error of an errorBody,
// the code or Code of an error value, and the Code* constants. It reads the source
// instead of running the server, so a code is found when it is added, whether or
// not a test reaches it.
func errorCodesInSource(t *testing.T) []string {
	t.Helper()
	found := map[string]bool{}
	str := func(e ast.Expr) (string, bool) {
		lit, ok := e.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(lit.Value)
		return s, err == nil && s != ""
	}
	typeName := func(e ast.Expr) string {
		switch x := e.(type) {
		case *ast.Ident:
			return x.Name
		case *ast.SelectorExpr:
			if p, ok := x.X.(*ast.Ident); ok {
				return p.Name + "." + x.Sel.Name
			}
		case *ast.StarExpr:
			return ""
		}
		return ""
	}
	// statusLike: an HTTP status as the first argument (http.StatusX or a number).
	statusLike := func(e ast.Expr) bool {
		switch x := e.(type) {
		case *ast.BasicLit:
			return x.Kind == token.INT
		case *ast.SelectorExpr:
			p, ok := x.X.(*ast.Ident)
			return ok && p.Name == "http"
		}
		return false
	}
	fset := token.NewFileSet()
	err := filepath.WalkDir("..", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				var name string
				switch fn := x.Fun.(type) {
				case *ast.Ident:
					name = fn.Name
				case *ast.SelectorExpr:
					name = fn.Sel.Name
				}
				// The helpers that answer with a status and a code: where the code is.
				idx := map[string]int{"writeError": 2, "coded": 1, "refuse": 1, "errf": 1, "Errorf": 2, "connError": 3, "err": 1}[name]
				if idx == 0 || len(x.Args) <= idx {
					break
				}
				// errf of the auth package takes a kind and a format, not a status and a code.
				if name == "errf" || name == "err" {
					if !statusLike(x.Args[0]) {
						break
					}
				}
				if s, ok := str(x.Args[idx]); ok {
					found[s] = true
				}
			case *ast.CompositeLit:
				// An anonymous struct written with positional values: the one whose
				// field is tagged json:"error" says the code.
				if st, ok := x.Type.(*ast.StructType); ok {
					i := 0
					for _, fld := range st.Fields.List {
						n := max(len(fld.Names), 1)
						if fld.Tag != nil && strings.Contains(fld.Tag.Value, `json:"error"`) && i < len(x.Elts) {
							if s, ok := str(x.Elts[i]); ok {
								found[s] = true
							}
						}
						i += n
					}
				}
				tn := typeName(x.Type)
				if notAPICode[tn] {
					break
				}
				for _, el := range x.Elts {
					kv, ok := el.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					k, _ := kv.Key.(*ast.Ident)
					if k == nil {
						continue
					}
					switch k.Name {
					case "Code", "code":
						if s, ok := str(kv.Value); ok {
							found[s] = true
						}
					case "Error":
						if tn == "errorBody" {
							if s, ok := str(kv.Value); ok {
								found[s] = true
							}
						}
					}
				}
			case *ast.GenDecl:
				if x.Tok != token.CONST {
					break
				}
				for _, sp := range x.Specs {
					vs := sp.(*ast.ValueSpec)
					for i, name := range vs.Names {
						if strings.HasPrefix(strings.ToLower(name.Name), "code") && i < len(vs.Values) {
							if s, ok := str(vs.Values[i]); ok {
								found[s] = true
							}
						}
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(found))
	for c := range found {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// TestErrorCodesListIsCurrent keeps web/src/i18n/errorCodes.json equal to the codes
// in the source. When a code is added, this fails until the list is regenerated
// (go test ./internal/api -run TestErrorCodesListIsCurrent -update-codes), and then
// the web test fails until the code has a Chinese and an English text.
func TestErrorCodesListIsCurrent(t *testing.T) {
	t.Parallel()
	codes := errorCodesInSource(t)
	want, err := json.MarshalIndent(codes, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	if *updateCodes {
		if err := os.WriteFile(codesFile, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(codesFile)
	if err != nil || string(got) != string(want) {
		t.Fatalf("web/src/i18n/errorCodes.json is not the list of error codes in the source; run: go test ./internal/api -run TestErrorCodesListIsCurrent -update-codes (%v)", err)
	}
}

// TestServerSentencesAreSentences keeps the words the server puts in `message`
// written as sentences (a capital letter first, a full stop last): the web app
// shows its own text by code, and these are for the people who use the API and
// read the logs. It looks at the messages that are one string in the source; a
// message that starts with a name of the API (limit, oidc.issuer) may start in
// lower case.
func TestServerSentencesAreSentences(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	err := filepath.WalkDir("..", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, _ := call.Fun.(*ast.Ident)
			if id == nil || id.Name != "writeError" || len(call.Args) != 4 {
				return true
			}
			// A message that is one string; a Sprintf of one format (its tail may be a value); or a
			// sum of pieces, of which the first and the last that are strings are looked at.
			first, last := stringEnds(call.Args[3])
			if first == nil {
				return true
			}
			if msg, _ := strconv.Unquote(first.Value); msg != "" {
				word, _, _ := strings.Cut(msg, " ")
				if word[0] >= 'a' && word[0] <= 'z' && strings.Trim(word, "abcdefghijklmnopqrstuvwxyz") == "" {
					t.Errorf("%s: %q does not start with a capital letter", fset.Position(first.Pos()), msg)
				}
			}
			if msg, _ := strconv.Unquote(last.Value); msg != "" && !strings.HasSuffix(msg, "%s") && !strings.HasSuffix(msg, "%v") && !strings.HasSuffix(msg, "%q") {
				if c := msg[len(msg)-1]; c != '.' && c != '?' && c != '!' && !(last != first && c == ' ') {
					t.Errorf("%s: %q does not end with a full stop", fset.Position(last.Pos()), msg)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// stringEnds is the first and the last of the string literals a message is made of, in
// order: the message itself, the format of an fmt.Sprintf, or the pieces of a sum. A piece
// that is a name or an error is not one, so a message that ends in one is not judged at its end
// (writeError ends it), and one that starts with one is not judged at its start.
func stringEnds(e ast.Expr) (first, last *ast.BasicLit) {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind == token.STRING {
			return x, x
		}
	case *ast.CallExpr:
		if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Sprintf" && len(x.Args) > 0 {
			return stringEnds(x.Args[0])
		}
	case *ast.BinaryExpr:
		var parts []ast.Expr
		var flat func(ast.Expr)
		flat = func(e ast.Expr) {
			if b, ok := e.(*ast.BinaryExpr); ok && b.Op == token.ADD {
				flat(b.X)
				flat(b.Y)
				return
			}
			parts = append(parts, e)
		}
		flat(x)
		if f, ok := parts[0].(*ast.BasicLit); ok && f.Kind == token.STRING {
			first = f
		}
		if l, ok := parts[len(parts)-1].(*ast.BasicLit); ok && l.Kind == token.STRING {
			last = l
		}
		return first, last
	}
	return nil, nil
}
