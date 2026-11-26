package store_test

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// users.name_key and users.email_key are written with every write of the name
// and the address, and the searches and lookups read them: both databases find
// the same people, whatever their idea of lower case.
func TestPeopleAreFoundByTheirKeys(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		now := time.Now()
		q := st.Q()
		for _, u := range []store.User{
			{ID: "A", Username: "anna", Name: "Anna Straße", Email: "Anna@Example.COM"},
			{ID: "B", Username: "bob", Name: "ÉMILE Bob", Email: "bob@example.com"},
			{ID: "C", Username: "carl", Name: "Carl", Email: "cärl@example.com"},
		} {
			u.Role, u.Created = "member", now
			if err := store.InsertUser(ctx, q, u); err != nil {
				t.Fatal(err)
			}
		}
		sc := store.InTenant(store.DefaultTenantID)
		search := func(query string) string {
			t.Helper()
			l, err := store.SearchUsers(ctx, q, st.Dialect(), sc, query, false, 0)
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, u := range l {
				ids = append(ids, u.ID)
			}
			sort.Strings(ids)
			return strings.Join(ids, ",")
		}
		for query, want := range map[string]string{"STRASSE": "A", "straße": "A", "émile": "B", "Émile": "B", "ANN": "A", "BOB": "B", "x%": "", "": "A,B,C"} {
			if got := search(query); got != want {
				t.Errorf("search %q: %q, want %q", query, got, want)
			}
		}
		byEmail := func(email string) string {
			t.Helper()
			row, err := store.EmailOwner(ctx, q, email)
			if errors.Is(err, store.ErrNotFound) {
				return ""
			} else if err != nil {
				t.Fatal(err)
			}
			return row.UserID
		}
		if got := byEmail("anna@example.com"); got != "A" {
			t.Errorf("by address: %q", got)
		}
		if got := byEmail("BOB@EXAMPLE.COM"); got != "B" {
			t.Errorf("by address in capitals: %q", got)
		}
		if got := byEmail("CÄRL@example.com"); got != "C" {
			t.Errorf("an address that is not ASCII, in capitals: %q", got)
		}
		// Every write of the name or the address moves the keys.
		if err := store.UpdateProfile(ctx, q, "A", "Zoë", "zoe@Example.com", false); err != nil {
			t.Fatal(err)
		}
		if got := search("ZOË"); got != "A" {
			t.Errorf("after a new name: %q", got)
		}
		if got := search("strasse"); got != "" {
			t.Errorf("the old name is still found: %q", got)
		}
		if byEmail("zoe@example.com") != "A" || byEmail("anna@example.com") != "" {
			t.Error("the address key did not follow UpdateProfile")
		}
		if err := store.SetEmail(ctx, q, "B", "Robert@Example.com", true, ""); err != nil {
			t.Fatal(err)
		}
		if byEmail("robert@example.com") != "B" || byEmail("bob@example.com") != "" {
			t.Error("the address key did not follow SetEmail")
		}
		if err := store.SetEmail(ctx, q, "B", "", false, ""); err != nil {
			t.Fatal(err)
		}
		if n := count(t, st, `SELECT COUNT(*) FROM users WHERE id = 'B' AND email_key = ''`); n != 1 {
			t.Error("an empty address left a key")
		}
	})
}

// Every statement of the package that writes users.name or users.email writes
// the key that goes with it (name_key, email_key) in the same statement.
func TestEveryWriteOfANameOrAnAddressWritesItsKey(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	insert := regexp.MustCompile(`(?is)\bINSERT\s+INTO\s+users\s*\(`)
	update := regexp.MustCompile(`(?is)\bUPDATE\s+users\s+SET\b(.*?)\bWHERE\b`)
	sets := func(set, col string) bool {
		return regexp.MustCompile(`(^|[\s,])` + col + `\s*=`).MatchString(set)
	}
	var bad []string
	for name, f := range pkgs["store"].Files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			// The statement may be split over a concatenation: join the
			// literals of each call.
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				var text strings.Builder
				for _, a := range call.Args {
					ast.Inspect(a, func(n ast.Node) bool {
						if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
							if s, err := strconv.Unquote(lit.Value); err == nil {
								text.WriteString(s + " ")
							}
						}
						return true
					})
				}
				s := text.String()
				if insert.MatchString(s) && !(strings.Contains(s, "name_key") && strings.Contains(s, "email_key")) {
					bad = append(bad, fn.Name.Name+": INSERT INTO users without name_key and email_key")
				}
				for _, m := range update.FindAllStringSubmatch(s, -1) {
					if sets(m[1], "name") && !sets(m[1], "name_key") {
						bad = append(bad, fn.Name.Name+": sets users.name without name_key")
					}
					if sets(m[1], "email") && !sets(m[1], "email_key") {
						bad = append(bad, fn.Name.Name+": sets users.email without email_key")
					}
				}
				return true
			})
		}
	}
	sort.Strings(bad)
	for _, b := range bad {
		t.Error(b)
	}
}
