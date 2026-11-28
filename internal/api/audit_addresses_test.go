package api_test

import (
	"strings"
	"testing"

	"petalbook.app/server/internal/auditlog"
	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// Every kind that declares an address in its detail is hidden from a tenant's
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
			"user_created":          {"email": secret("user_created"), "role": "member"},
			"user_changed":          {"email": secret("user_changed"), "old": map[string]any{"email": secret("user_changed_old"), "name": "Old"}, "name": "New"},
		}
		// Joining by domain (3.6 d5): the address is on the tenant's domain while
		// it holds it, and hidden as any other once it does not.
		for _, kind := range []string{"join_auto", "join_requested", "join_approved", "join_denied", "join_withdrawn", "join_cancelled"} {
			details[kind] = map[string]any{"email": secret(kind), "username": "someone", "domain": "joined.example"}
		}
		// The batches: each entry's user is as typed, an address or a username.
		for _, kind := range []string{"members_batch", "group_members_batch", "invites_batch"} {
			details[kind] = map[string]any{"rows": 2, "results": []map[string]any{
				{"row": 1, "user": secret(kind), "status": "added"}, {"row": 2, "user": "plainname", "status": "added"}}}
		}
		// Every kind that declares address fields has a case here, and the other way.
		for _, k := range auditlog.Kinds() {
			if k.Visibility == auditlog.Tenant && (len(k.AddressFields) > 0) != (details[k.Name] != nil) {
				t.Errorf("kind %s: AddressFields %v, test case %v", k.Name, k.AddressFields, details[k.Name] != nil)
			}
		}
		for kind, d := range details {
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
			want, _ := d["email"].(string)
			if r, ok := d["results"].([]map[string]any); ok {
				want = r[0]["user"].(string)
			}
			if !strings.Contains(all, want) {
				t.Errorf("a site administrator does not see %s", want)
			}
		}
		if !strings.Contains(list(f.admB), "plainname") {
			t.Error("a username typed in a batch is hidden")
		}
		if !strings.Contains(all, "user_changed_old@secret.example") {
			t.Error("a site administrator does not see old.email")
		}
	})
}
