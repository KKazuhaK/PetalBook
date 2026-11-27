package api_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"petalbook.app/server/internal/api"
	"petalbook.app/server/internal/settings"
)

// Every route says which feature it belongs to, or that it belongs to none, so
// a new route cannot forget to choose; a feature it names is a boolean setting
// that is a feature; a public route belongs to none (nobody is known to ask
// which tenant's feature); and what a feature gates is checked once, in handle.
func TestEveryRouteDeclaresItsFeatureOrIsAlwaysOn(t *testing.T) {
	t.Parallel()
	isFeature := map[string]bool{}
	for _, d := range settings.Definitions() {
		if d.IsFeature() {
			isFeature[d.Key] = true
		}
	}
	for _, r := range api.Routes() {
		name := r.Method + " " + r.Pattern
		switch {
		case r.Feature == "" && !r.AlwaysOn:
			t.Errorf("%s does not say which feature it belongs to (.on) or that it belongs to none (.always)", name)
		case r.Feature != "" && r.AlwaysOn:
			t.Errorf("%s is both in a feature and always on", name)
		case r.Feature != "" && !isFeature[r.Feature]:
			t.Errorf("%s names %s, which is not a feature (a boolean setting named features.*)", name, r.Feature)
		case r.Feature != "" && r.Public:
			t.Errorf("%s is public and names a feature: no tenant is known to check it for", name)
		case r.AlsoFeature != "" && (!isFeature[r.AlsoFeature] || r.AlsoFeature == r.Feature):
			t.Errorf("%s adds %s as a second feature, which is not a feature of its own (a boolean setting named features.*)", name, r.AlsoFeature)
		}
		if r.Pattern == "/api/admin/audit/download" && r.AlsoFeature != settings.FeatureAuditDownload {
			t.Errorf("%s needs the audit and its download: the second feature is %q", name, r.AlsoFeature)
		}
	}
}

// Nothing but the route table reads a feature to refuse a request: a handler or
// a helper that asks a features.* setting by name would be a check the table
// does not show. What does so for a reason of its own is listed, with the reason.
func TestNoHandlerChecksAFeatureTheTableCouldDeclare(t *testing.T) {
	t.Parallel()
	allowed := map[string]string{
		"routes.go":          "the declarations of the table",
		"features.go":        "the words of the refusals",
		"settings.go":        "features.registration is a setting with checks of its own on a change; the list of what the web may read",
		"tenantoverview.go":  "the overview is not the audit's route: it shows a tenant's administrators the recent audit rows only while the tenant has features.tenantAudit",
		"tenants.go":         "a tenant is shown with its effective registration, which is also held to mail being ready",
		"github_installs.go": "an installation's callback is a public route: the feature is asked again there for the owner it is connected to",
		"github_webhook.go":  "GitHub's webhook is a public route of no tenant: the site's own switch is asked there",
	}
	isKey := func(s string) bool { return strings.HasPrefix(s, "features.") || s == settings.MailOwnSender }
	consts := []string{"RegistrationOpen", "OfflineCopies", "MailOwnSender"}
	used := map[string]bool{}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		found := ""
		ast.Inspect(af, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.BasicLit:
				if v, err := strconv.Unquote(x.Value); x.Kind == token.STRING && err == nil && isKey(v) {
					found = v
				}
			case *ast.SelectorExpr:
				if p, ok := x.X.(*ast.Ident); ok && p.Name == "settings" && (strings.HasPrefix(x.Sel.Name, "Feature") || slices.Contains(consts, x.Sel.Name)) {
					found = "settings." + x.Sel.Name
				}
			}
			return true
		})
		if found != "" {
			used[f] = true
		}
		if found != "" && allowed[f] == "" {
			t.Errorf("%s reads the feature %s: declare it on the route (access.on) so handle checks it, or list the file here with the reason", f, found)
		}
	}
	for f := range allowed {
		if !used[f] {
			t.Errorf("%s is listed as reading a feature and does not: remove it from the list", f)
		}
	}
}
