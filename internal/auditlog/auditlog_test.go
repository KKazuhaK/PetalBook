package auditlog_test

import (
	"slices"
	"testing"

	"petalbook.app/server/internal/auditlog"
	"petalbook.app/server/internal/settings"
)

// Each kind has a category that exists; the core categories and the optional
// ones are the ones the design names; the kinds only the server's own
// administrators see are the server's own doings.
func TestRegistryIsConsistent(t *testing.T) {
	t.Parallel()
	var core, optional []string
	for _, c := range auditlog.Categories() {
		if c.Core {
			core = append(core, string(c.ID))
		} else {
			optional = append(optional, string(c.ID))
		}
	}
	if !slices.Equal(core, []string{"security", "admin", "data", "system"}) || !slices.Equal(optional, []string{"signins", "reads", "downloads", "api"}) {
		t.Fatalf("core %v, optional %v", core, optional)
	}
	for _, k := range auditlog.Kinds() {
		if _, ok := auditlog.CategoryInfo(k.Category); !ok {
			t.Errorf("kind %s: no category %s", k.Name, k.Category)
		}
	}
	if got := auditlog.CategoryOf("nobody-knows"); got != auditlog.Admin {
		t.Errorf("an unregistered kind is filed under %s", got)
	}
	want := []string{"audit_purged", "connection_provider_changed", "dropped", "lockout", "mail_changed", "mail_test", "plan_changed", "plan_created", "plan_deleted", "provider_changed", "provider_created", "provider_deleted", "rate_limited"}
	if got := auditlog.SiteOnlyKinds(); !slices.Equal(got, want) {
		t.Errorf("site-only kinds %v, want %v", got, want)
	}
	if k, _ := auditlog.Lookup("login"); k.Category != auditlog.Signins || k.Core() {
		t.Errorf("login: %+v", k)
	}
	if k, _ := auditlog.Lookup("audit_downloaded"); k.Category != auditlog.Data || !k.Core() {
		t.Errorf("audit_downloaded: %+v", k)
	}
}

// A core category has no setting: the settings are made from the registry's
// optional categories, so nothing can switch a core one off, and the retention
// cannot go below the floor of core rows.
func TestSettingsFollowTheRegistry(t *testing.T) {
	t.Parallel()
	defs := map[string]*settings.Def{}
	for _, d := range settings.Definitions() {
		defs[d.Key] = d
	}
	for _, c := range auditlog.Categories() {
		d := defs["audit."+string(c.ID)]
		switch {
		case c.Core && d != nil:
			t.Errorf("core category %s has a setting", c.ID)
		case !c.Core && (d == nil || d.Key != c.Setting || d.Default != c.Default || len(d.Layers) != 2):
			t.Errorf("optional category %s: %+v", c.ID, d)
		}
	}
	if d := defs[settings.AuditKeepDays]; d == nil || d.Min != 0 || d.Default != int64(90) || len(d.Layers) != 1 || d.ServerWide == "" {
		t.Errorf("keepDays: %+v", d)
	}
	for _, k := range []string{settings.AuditKeepRows, settings.AuditKeepMode, settings.AuditOptionalKeepDays} {
		if d := defs[k]; d == nil || len(d.Layers) != 1 || d.ServerWide == "" {
			t.Errorf("%s is not site-only: %+v", k, d)
		}
	}
	// No floor: every category, core or not, has days of its own, none by default.
	for _, c := range auditlog.Categories() {
		d := defs[settings.AuditCategoryKeepDays(c.ID)]
		if d == nil || d.Min != 0 || !d.Nullable || d.Default != nil || len(d.Layers) != 1 || d.ServerWide == "" {
			t.Errorf("category %s has no days of its own: %+v", c.ID, d)
		}
	}
}

func TestUndeclaredAddressFindsAddressesOutsideTheDeclaredPlaces(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ kind, detail, path string }{
		{"email_added", `{"email":"a@b.example"}`, ""},
		{"user_changed", `{"email":"a@b.example","old":{"email":"c@d.example","name":"x"}}`, ""},
		{"members_batch", `{"results":[{"user":"a@b.example"},{"user":"bob"}]}`, ""},
		{"login", `{"note":"a@b.example"}`, "note"},
		{"user_changed", `{"old":{"username":"a@b.example"}}`, "old.username"},
		{"members_batch", `{"results":[{"detail":"a@b.example"}]}`, "results[].detail"},
		{"login", `{"reason":"mail bob@x.example soon"}`, ""}, // text, not an address
	} {
		if p, _ := auditlog.UndeclaredAddress(tc.kind, tc.detail); p != tc.path {
			t.Errorf("%s %s: path %q, want %q", tc.kind, tc.detail, p, tc.path)
		}
	}
}

func TestRedactAddressesHidesOnlyDeclaredAddresses(t *testing.T) {
	t.Parallel()
	got := auditlog.RedactAddresses("members_batch", []byte(`{"results":[{"user":"a@b.example"},{"user":"bob"}],"x":"c@d.example"}`), func(a string) bool { return false })
	if string(got) != `{"results":[{"user":"(hidden)"},{"user":"bob"}],"x":"c@d.example"}` {
		t.Fatal(string(got))
	}
}
