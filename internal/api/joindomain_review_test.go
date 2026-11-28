package api_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"petalbook.app/server/internal/store"
)

// The review of joining by domain (3.6 d5): the feature of the path's tenant,
// the fallback to the tenant's registration, a disabled tenant, and the limits.

// The requests' routes check features.domains of the tenant in the path: a
// site administrator who has not entered a tenant's scope is refused for one
// that has it off, and served for one that has it on, whatever the default
// tenant (their own) has.
func TestTheRequestsRoutesCheckThePathsTenantsFeature(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newDomFx(t, a)
		feature := func(tenant string, on bool) {
			t.Helper()
			f.root.do("PUT", "/api/admin/tenants/"+tenant+"/settings/features.domains", map[string]any{"value": on}).want(t, 200, "")
		}
		feature(store.DefaultTenantID, false)
		f.root.do("GET", joinPath(f.tenB), nil).want(t, 200, "")
		feature(store.DefaultTenantID, true)
		feature(f.tenB, false)
		f.root.do("GET", joinPath(f.tenB), nil).want(t, 403, "feature_off")
		f.root.do("POST", joinPath(f.tenB)+"/"+fakeID+"/deny", nil).want(t, 403, "feature_off")
		// Another tenant's administrator learns nothing of B's switch: not found, as for any tenant not theirs.
		f.admC.do("GET", joinPath(f.tenB), nil).want(t, 404, "not_found")
		// The domains' own routes are unchanged: a site administrator is not bound by the switch.
		f.root.do("GET", domainsPath(f.tenB), nil).want(t, 200, "")
	})
}

// A registration that joined by its domain, whose join was turned off before
// its link was followed, falls back to the tenant's registration, with all of
// its rules: the domain list too.
func TestTurningJoinOffFallsBackToTheTenantsDomainList(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newJoinFx(t, a)
		f.root.do("PUT", "/api/admin/settings/registration.mode", map[string]any{"value": "open"}).want(t, 200, "")
		f.root.do("PUT", "/api/admin/settings/registration.domains", map[string]any{"value": []string{"other.example"}}).want(t, 200, "")
		setJoin(f.admB, f.tenB, "acme.example", "auto").want(t, 200, "")
		anon := a.newClient("")
		tok := f.mailed(t, "alice@acme.example", func() {
			reg(anon, map[string]any{"username": "alice", "email": "alice@acme.example"}).want(t, 202, "")
		}).Token(t)
		setJoin(f.admB, f.tenB, "acme.example", "off").want(t, 200, "")
		anon.do("POST", "/api/register/confirm", map[string]any{"token": tok}).want(t, 400, "email_domain_not_allowed")
		if !mustNoUser(a, "alice") {
			t.Fatal("an address outside the tenant's domain list made an account")
		}
	})
}

// A request is not approved into a disabled organisation.
func TestARequestIsNotApprovedIntoADisabledTenant(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newJoinFx(t, a)
		setJoin(f.admB, f.tenB, "acme.example", "request").want(t, 200, "")
		f.request(t, a.newClient(""), "bob", "bob@acme.example")
		id := pendingID(t, f.admB, f.tenB)
		f.root.do("PATCH", "/api/admin/tenants/"+f.tenB, map[string]any{"disabled": true}).want(t, 200, "")
		f.root.do("POST", joinPath(f.tenB)+"/"+id+"/approve", map[string]any{}).want(t, 409, "tenant_disabled")
		if !mustNoUser(a, "bob") {
			t.Fatal("an account was made in a disabled tenant")
		}
		if n := rowCount(t, a, `SELECT COUNT(*) FROM join_requests WHERE state = 'pending'`); n != 1 {
			t.Fatalf("the request is no longer waiting: %d", n)
		}
	})
}

// The day's three requests count the address without its +tag; and a tenant
// holds at most 1,000 waiting requests.
func TestRequestLimitsIgnoreTagsAndCapATenant(t *testing.T) {
	t.Parallel()
	eachAccounts(t, accOpts{}, func(t *testing.T, a *acc) {
		f := newJoinFx(t, a)
		setJoin(f.admB, f.tenB, "acme.example", "request").want(t, 200, "")
		anon := a.newClient("")
		for i, tag := range []string{"a", "b", "c"} {
			if i > 0 {
				a.clock.Advance(61 * time.Minute)
			}
			f.request(t, anon, "bob"+tag, "bob+"+tag+"@acme.example")
		}
		a.clock.Advance(61 * time.Minute)
		f.signUp(t, anon, "bobd", "bob+d@acme.example").want(t, 429, "rate_limited")
		// The tenant's cap: 1,000 waiting (made here as confirmed sign-ups make them).
		now := a.clock.Now()
		err := a.st.Tx(t.Context(), func(q store.Q) error {
			for i := range 1000 - 3 {
				id := fmt.Sprintf("FILL%06d", i)
				if err := store.InsertJoinRequest(t.Context(), q, store.JoinRequest{ID: id, TenantID: f.tenB, Username: "fill" + id[4:], Name: "x",
					Email: id + "@acme.example", EmailKey: strings.ToLower(id) + "@acme.example", Domain: "acme.example", Lang: "en", TokenHash: "t" + id,
					Created: now, Expires: now.Add(time.Hour)}, now); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		f.signUp(t, anon, "carl", "carl@acme.example").want(t, 429, "join_limit")
		if !mustNoUser(a, "carl") || rowCount(t, a, `SELECT COUNT(*) FROM join_requests WHERE username = 'carl'`) != 0 {
			t.Fatal("a request over the tenant's cap was stored")
		}
	})
}
