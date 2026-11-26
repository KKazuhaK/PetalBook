package api

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"strings"
	"testing"

	"petalbook.app/server/internal/auditlog"
	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/settings"
)

var updateRegistries = flag.Bool("update-registries", false, "rewrite web/src/api/generated/ from the registries in the source")

// The web app does not keep its own copies of the server's registries: the
// permission keys, the audit categories with their kinds, and the setting keys
// are written here from the Go source, and the web imports them. The permission
// key type is a second file, because a TypeScript type cannot come from JSON.
const (
	registriesFile     = "../../web/src/api/generated/registries.json"
	permissionKeysFile = "../../web/src/api/generated/permissionKeys.ts"
)

func registriesJSON(t *testing.T) []byte {
	t.Helper()
	type perm struct {
		Name        string `json:"name"`
		Scope       string `json:"scope"`
		Feature     string `json:"feature"`
		Description string `json:"description"`
		BuiltInOnly bool   `json:"builtInOnly"`
	}
	type kind struct {
		Name string `json:"name"`
		Site bool   `json:"site"`
	}
	type category struct {
		ID      string  `json:"id"`
		Core    bool    `json:"core"`
		Setting *string `json:"setting"`
		Default bool    `json:"default"`
		Kinds   []kind  `json:"kinds"`
	}
	out := struct {
		Permissions     []perm     `json:"permissions"`
		AuditCategories []category `json:"auditCategories"`
		SettingKeys     []string   `json:"settingKeys"`
	}{Permissions: []perm{}, AuditCategories: []category{}, SettingKeys: []string{}}
	for _, in := range authz.All() {
		out.Permissions = append(out.Permissions, perm{in.Name, in.Scope.String(), in.Feature, in.Description, in.BuiltInOnly})
	}
	kinds := auditlog.Kinds()
	for _, ci := range auditlog.Categories() {
		c := category{ID: string(ci.ID), Core: ci.Core, Default: ci.Core || ci.Default, Kinds: []kind{}}
		if !ci.Core {
			s := ci.Setting
			c.Setting = &s
		}
		for _, k := range kinds {
			if k.Category == ci.ID {
				c.Kinds = append(c.Kinds, kind{k.Name, k.Visibility == auditlog.Site})
			}
		}
		out.AuditCategories = append(out.AuditCategories, c)
	}
	for _, d := range settings.Definitions() {
		out.SettingKeys = append(out.SettingKeys, d.Key)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func permissionKeysTS() []byte {
	var b strings.Builder
	b.WriteString("// The permission keys of the server's registry (internal/authz), in its order. Written by\n")
	b.WriteString("// go test ./internal/api -run TestWebRegistriesAreCurrent -update-registries; do not edit.\n\n")
	b.WriteString("export type PermissionKey =\n")
	for _, in := range authz.All() {
		b.WriteString("  | '" + in.Name + "'\n")
	}
	return []byte(b.String())
}

// TestWebRegistriesAreCurrent keeps web/src/api/generated/ equal to the
// registries in the source. When a key, kind or setting is added, this fails
// until the files are regenerated:
//
//	go test ./internal/api -run TestWebRegistriesAreCurrent -update-registries
func TestWebRegistriesAreCurrent(t *testing.T) {
	t.Parallel()
	files := map[string][]byte{registriesFile: registriesJSON(t), permissionKeysFile: permissionKeysTS()}
	for path, want := range files {
		if *updateRegistries {
			if err := os.MkdirAll("../../web/src/api/generated", 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s is not what the source's registries give; run: go test ./internal/api -run TestWebRegistriesAreCurrent -update-registries (%v)", strings.TrimPrefix(path, "../../"), err)
		}
	}
}
