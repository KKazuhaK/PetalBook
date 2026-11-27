package api_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// userEmails lists the addresses of a user page's "emails".
func userEmails(t *testing.T, r result) []string {
	t.Helper()
	var out []string
	for _, e := range r.user()["emails"].([]any) {
		out = append(out, e.(map[string]any)["email"].(string))
	}
	return out
}

// A tenant's administrator sees a member's primary address and the addresses in
// the tenant's verified domains, not the others; a site administrator across the
// site sees all; one in the tenant's scope sees what the tenant's administrator
// does. Until domains are verified (the hook of 3.6 d) the set is empty.
func TestTenantAdministratorsSeeTheOrganisationsAddressesOnly(t *testing.T) {
	t.Parallel()
	needGit(t)
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		var domains atomic.Pointer[[]string]
		a := newAcc(t, st, accOpts{mod: func(c *auth.Config) {
			c.OrgDomains = func(_ context.Context, _ string) []string {
				if d := domains.Load(); d != nil {
					return *d
				}
				return nil
			}
		}})
		f := newTenFx(t, a)
		ctx := context.Background()
		f.root.putSetting("accounts.multipleEmails", true).want(t, 200, "")
		if err := store.SetEmail(ctx, a.st.Q(), f.userB, "mem-b@acme.example", true, ""); err != nil {
			t.Fatal(err)
		}
		for _, e := range []string{"mem-b@home.example", "mem-b@eng.acme.example"} {
			if _, err := store.AddEmail(ctx, a.st.Q(), f.userB, e, true, "", false, a.clock.Now()); err != nil {
				t.Fatal(err)
			}
		}
		a.auth.Audit(auth.AuditEvent{Kind: "email_added", Actor: f.userB, User: f.userB, Detail: map[string]any{"email": "mem-b@home.example"}})
		a.auth.Audit(auth.AuditEvent{Kind: "email_added", Actor: f.userB, User: f.userB, Detail: map[string]any{"email": "mem-b@eng.acme.example"}})
		inB := []string{scopeHeader, "tenant:" + f.tenB}
		path := "/api/users/" + f.userB
		auditText := func(c *client, h ...string) string {
			return c.do("GET", "/api/admin/audit?kind=email_added&limit=50", nil, h...).want(t, 200, "").raw
		}
		check := func(name string, want int, got []string, audit string, shown map[string]bool) {
			t.Helper()
			if len(got) != want {
				t.Fatalf("%s sees %v, want %d addresses", name, got, want)
			}
			for addr, yes := range shown {
				if strings.Contains(audit, addr) != yes {
					t.Fatalf("%s: audit shows %s = %v, want %v: %s", name, addr, !yes, yes, audit)
				}
			}
		}
		home := func(yes bool) map[string]bool { return map[string]bool{"mem-b@home.example": yes} }

		// No verified domain yet: the primary alone, for both.
		adm := userEmails(t, f.admB.do("GET", path, nil).want(t, 200, ""))
		check("tenant admin", 1, adm, auditText(f.admB), home(false))
		if adm[0] != "mem-b@acme.example" {
			t.Fatalf("%v", adm)
		}
		check("scoped site admin", 1, userEmails(t, f.root.do("GET", path, nil, inB...).want(t, 200, "")), auditText(f.root, inB...), home(false))
		// The site administrator across the site sees all three.
		check("site admin", 3, userEmails(t, f.root.do("GET", path, nil).want(t, 200, "")), auditText(f.root), home(true))

		// A verified domain counts exactly: an address of its subdomain is not shown.
		domains.Store(&[]string{"acme.example"})
		check("tenant admin, parent domain", 1, userEmails(t, f.admB.do("GET", path, nil).want(t, 200, "")), auditText(f.admB),
			map[string]bool{"mem-b@home.example": false, "mem-b@eng.acme.example": false})
		// With the subdomain verified too: the addresses in it, still not the others.
		domains.Store(&[]string{"acme.example", "eng.acme.example"})
		sub := map[string]bool{"mem-b@home.example": false, "mem-b@eng.acme.example": true}
		check("tenant admin, domain", 2, userEmails(t, f.admB.do("GET", path, nil).want(t, 200, "")), auditText(f.admB), sub)
		check("scoped site admin, domain", 2, userEmails(t, f.root.do("GET", path, nil, inB...).want(t, 200, "")), auditText(f.root, inB...), sub)
		check("site admin, domain", 3, userEmails(t, f.root.do("GET", path, nil).want(t, 200, "")), auditText(f.root), home(true))
		// The CSV download is held to the same view.
		csv := f.admB.do("GET", "/api/admin/audit/download?kind=email_added", nil).want(t, 200, "").raw
		if strings.Contains(csv, "home.example") || !strings.Contains(csv, "eng.acme.example") {
			t.Fatalf("download: %s", csv)
		}
	})
}
