package api_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"petalbook.app/server/internal/api"
	"petalbook.app/server/internal/authz"
)

// Every route is public, for anyone signed in, on a space, or names a key; every
// administration route names a key, and every key a route names is a site or
// tenant key of the registry.
func TestEveryAdminRouteHasAKey(t *testing.T) {
	t.Parallel()
	used := map[string]bool{}
	for _, r := range api.Routes() {
		name := r.Method + " " + r.Pattern
		admin := strings.HasPrefix(r.Pattern, "/api/admin/") || strings.HasPrefix(r.Pattern, "/api/users") ||
			(strings.HasPrefix(r.Pattern, "/api/invites") && !r.Public)
		if admin && r.Key == "" {
			t.Errorf("%s is an administration route without a permission key", name)
		}
		if r.Key == "" {
			continue
		}
		used[r.Key] = true
		k, ok := authz.Lookup(r.Key)
		if !ok {
			t.Errorf("%s names %s, which is not registered", name, r.Key)
			continue
		}
		if k.Scope() == authz.ScopeSpace || r.Space || r.Public {
			t.Errorf("%s: a route outside spaces names a site or tenant key (%s)", name, r.Key)
		}
		if r.SiteAdmin != (k.Scope() == authz.ScopeSite) {
			t.Errorf("%s: SiteAdmin does not follow its key's scope", name)
		}
	}
	// A site or tenant key no route names would be a key nobody can use,
	// unless a handler asks it for what it does beyond its route's key.
	for _, in := range authz.All() {
		if in.Scope != authz.ScopeSpace && !used[in.Name] && askedInHandlers[in.Name] == "" {
			t.Errorf("no route names %s", in.Name)
		}
	}
	src := ""
	for _, dir := range []string{".", "../auth", "../spaces"} {
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		for _, f := range files {
			if !strings.HasSuffix(f, "_test.go") {
				b, err := os.ReadFile(f)
				if err != nil {
					t.Fatal(err)
				}
				src += string(b)
			}
		}
	}
	for name := range askedInHandlers {
		k, ok := authz.Lookup(name)
		if !ok {
			t.Errorf("%s is not registered", name)
			continue
		}
		if !strings.Contains(src, "authz."+goName(k.Name())) {
			t.Errorf("no handler asks %s", name)
		}
	}
}

// askedInHandlers are the keys no route names: a handler asks them for part of
// what it does (part 2 of 3.6 moved the checks of the site administrator there).
var askedInHandlers = map[string]string{
	"site.admins.manage":           "changing, deleting, resetting a site administrator, or making one (accounts, tokens, passkeys, deletion)",
	"site.spaces.personal.meta":    "personal spaces in the bin, on a person's page, restored with their person, and managed without being a member",
	"site.spaces.personal.content": "the audited view and the export of a personal space (built-in site administrator only)",
}

// goName is the Go name of a key: site.admins.manage is SiteAdminsManage.
func goName(name string) string {
	var b strings.Builder
	for _, p := range strings.Split(name, ".") {
		b.WriteString(strings.ToUpper(p[:1]) + p[1:])
	}
	return b.String()
}

// The table of keys in docs/api.md lists exactly the registry, in its order (the
// web app's keys are generated: TestWebRegistriesAreCurrent).
func TestPermissionKeysMatchTheDocs(t *testing.T) {
	t.Parallel()
	var want []string
	scopes := map[string]string{}
	for _, in := range authz.All() {
		want = append(want, in.Name)
		capName := in.Feature
		if capName == "" {
			capName = "—"
		} else {
			capName = "`" + capName + "`"
		}
		scopes[in.Name] = in.Scope.String() + "|" + capName
	}

	doc, err := os.ReadFile("../../docs/api.md")
	if err != nil {
		t.Fatal(err)
	}
	sec := string(doc)
	i := strings.Index(sec, "\n### Permission keys\n")
	if i < 0 {
		t.Fatal(`docs/api.md has no "### Permission keys"`)
	}
	sec = sec[i+1:]
	if j := strings.Index(sec[4:], "\n### "); j >= 0 {
		sec = sec[:j+4]
	}
	row := regexp.MustCompile("^\\| `([a-z][a-zA-Z.]*)` \\| (site|tenant|space) \\| ([^|]+) \\|")
	var docKeys []string
	for _, line := range strings.Split(sec, "\n") {
		m := row.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		docKeys = append(docKeys, m[1])
		if got := m[2] + "|" + strings.TrimSpace(m[3]); got != scopes[m[1]] {
			t.Errorf("docs/api.md: %s is %q, the registry says %q", m[1], got, scopes[m[1]])
		}
	}
	if !slices.Equal(docKeys, want) {
		t.Errorf("docs/api.md lists\n%v\nthe registry has\n%v", docKeys, want)
	}
}

func TestSessionAndAdminListThePermissions(t *testing.T) {
	t.Parallel()
	f := newRouteAccessFx(t)
	perms := func(c *client) []string {
		var out []string
		for _, p := range c.do("GET", "/api/session", nil).want(t, 200, "").user()["permissions"].([]any) {
			out = append(out, p.(string))
		}
		return out
	}
	site, _ := authz.RoleSet(authz.RoleSiteAdmin)
	ten, _ := authz.RoleSet(authz.RoleTenantAdmin)
	if got := perms(f.site); !slices.Equal(got, site.Names()) {
		t.Errorf("site administrator: %v", got)
	}
	if got := perms(f.tenant); !slices.Equal(got, ten.Names()) || slices.Contains(got, "site.settings.manage") || !slices.Contains(got, "tenant.spaces.takeover") {
		t.Errorf("tenant administrator: %v", got)
	}
	if got := perms(f.mem); !slices.Equal(got, []string{"tenant.spaces.create"}) {
		t.Errorf("member: %v", got)
	}
	if got := perms(f.tok); len(got) != 0 {
		t.Errorf("token: %v", got)
	}
	r := f.tenant.do("GET", "/api/admin/permissions", nil).want(t, 200, "")
	l := r.body["permissions"].([]any)
	if len(l) != len(authz.All()) {
		t.Fatalf("%d keys, %d registered", len(l), len(authz.All()))
	}
	first := l[0].(map[string]any)
	if first["name"] != authz.All()[0].Name || first["scope"] == "" || first["description"] == "" {
		t.Fatal(first)
	}
	f.mem.do("GET", "/api/admin/permissions", nil).want(t, 403, "forbidden")
}
