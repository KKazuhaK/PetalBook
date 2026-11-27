package api_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"petalbook.app/server/internal/api"
	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// Every kind whose detail names an address is hidden from a tenant's
// administrator (list and CSV) when the address is outside the tenant's domains,
// and shown to a site administrator across the site: one case per kind, the
// details as their writers make them.
func TestAuditAddressKindsAreHiddenFromTenantAdministrators(t *testing.T) {
	t.Parallel()
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		a := newAcc(t, st, accOpts{})
		f := newTenFx(t, a)
		secret := func(kind string) string { return kind + "@secret.example" }
		details := map[string]map[string]any{
			"email_added":           {"email": secret("email_added")},
			"email_verified":        {"email": secret("email_verified"), "primary": false},
			"email_removed":         {"email": secret("email_removed")},
			"email_claim_withdrawn": {"email": secret("email_claim_withdrawn")},
			"email_primary_changed": {"email": secret("email_primary_changed"), "old": secret("email_primary_changed_old")},
			"email_released":        {"email": secret("email_released"), "to": f.userB},
			"registered":            {"email": secret("registered")},
			"user_changed":          {"email": secret("user_changed"), "old": map[string]any{"email": secret("user_changed_old"), "name": "Old"}, "name": "New"},
		}
		for kind := range api.AddressKinds() {
			if details[kind] == nil {
				t.Fatalf("kind %s is in addressKinds and has no case", kind)
			}
		}
		for kind, d := range details {
			if api.AddressKinds()[kind] == nil {
				t.Errorf("kind %s names an address and is not in addressKinds", kind)
			}
			a.auth.Audit(auth.AuditEvent{Kind: kind, Actor: f.userB, User: f.userB, Detail: d})
		}
		list := func(c *client, h ...string) string {
			return c.do("GET", "/api/admin/audit?limit=200&user="+f.userB, nil, h...).want(t, 200, "").raw
		}
		csv := f.admB.do("GET", "/api/admin/audit/download?user="+f.userB, nil).want(t, 200, "").raw
		for name, got := range map[string]string{
			"tenant admin":       list(f.admB),
			"scoped site admin":  list(f.root, scopeHeader, "tenant:"+f.tenB),
			"tenant admin's CSV": csv,
		} {
			if strings.Contains(got, "secret.example") {
				t.Errorf("%s sees an address: %s", name, got)
			}
			if !strings.Contains(got, "(hidden)") {
				t.Errorf("%s: nothing says hidden: %s", name, got)
			}
		}
		all := list(f.root)
		for _, d := range details {
			if want := d["email"].(string); !strings.Contains(all, want) {
				t.Errorf("a site administrator does not see %s", want)
			}
		}
		if !strings.Contains(all, "user_changed_old@secret.example") {
			t.Error("a site administrator does not see old.email")
		}
	})
}

// A writer that puts an email in the detail of a kind names that kind in
// addressKinds: the scan reads the source for a Kind and an "email" key on one
// line.
func TestEveryAddressWriterIsRedacted(t *testing.T) {
	t.Parallel()
	re := regexp.MustCompile(`Kind: "([a-z_]+)".*"email"`)
	found := 0
	for _, dir := range []string{"../auth", "../api", "../spaces", "../notify"} {
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range strings.Split(string(src), "\n") {
				if m := re.FindStringSubmatch(line); m != nil {
					found++
					if api.AddressKinds()[m[1]] == nil {
						t.Errorf("%s writes an email in the detail of %q, which the audit list does not redact", path, m[1])
					}
				}
			}
		}
	}
	if found < 5 {
		t.Fatalf("the scan found %d writers: it no longer sees them", found)
	}
}
