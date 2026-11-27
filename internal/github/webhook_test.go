package github_test

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"petalbook.app/server/internal/githubapp/githubapptest"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// startOrg has userID start installing the App on the GitHub organisation org
// for tenant, and GitHub login install it there: it answers the redirect
// GitHub sends the browser back with and the attempt's binding. The webhook
// event, if GitHub made one, waits in the fake.
func (h *harness) startOrg(userID, tenant, org, login string) (url.Values, string) {
	h.t.Helper()
	u, binding, err := h.svc.StartInstallFor(allowAll(ctx), userID, store.InstallOwner{TenantID: tenant}, org, "/back")
	if err != nil {
		h.t.Fatal(err)
	}
	back, _ := url.Parse(h.f.InstallAppOn(u, login, org, h.svc.CallbackURL(), true))
	return back.Query(), binding
}

func installerOf(t *testing.T, st *store.Store, id int64) *store.GitHubInstaller {
	t.Helper()
	in, err := store.GetGitHubInstaller(ctx, st.Q(), id)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func installationID(t *testing.T, q url.Values) int64 {
	t.Helper()
	var id int64
	for _, c := range q.Get("installation_id") {
		id = id*10 + int64(c-'0')
	}
	return id
}

// A delivery whose signature is not the App's webhook secret's, or that has
// none, is refused before anything of it is read, and records nothing; so is
// any delivery while the server keeps no webhook secret.
func TestAForgedOrUnsignedWebhookIsRefused(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		o := newOrg(t, st)
		q, _ := o.startOrg("A1", "T1", "petal-team", "kaz-admin")
		id := installationID(t, q)
		ev := o.f.Events()
		if len(ev) != 1 {
			t.Fatalf("%d events", len(ev))
		}
		bad := ev[0]
		bad.Body = []byte(strings.ReplaceAll(string(bad.Body), "kaz-admin", "mallory"))
		for name, e := range map[string]githubapptest.Event{"forged": ev[0].Forged(), "unsigned": ev[0].Unsigned(), "changed body": bad,
			"other prefix": {Name: ev[0].Name, Body: ev[0].Body, Signature: strings.Replace(ev[0].Signature, "sha256=", "sha1=", 1)}} {
			if _, err := o.svc.Webhook(ctx, e.Name, e.Signature, e.Body); code(err) != "unauthenticated" || status(err) != 401 {
				t.Fatalf("%s: %v", name, err)
			}
		}
		if installerOf(t, st, id) != nil {
			t.Fatal("a refused delivery was recorded")
		}
		// Without a webhook secret kept, even a well signed one is not believed.
		if _, err := st.Q().Exec(ctx, `DELETE FROM secrets WHERE name = 'github.webhookSecret'`); err != nil {
			t.Fatal(err)
		}
		o.clk.Advance(time.Second) // the App's settings are read again
		if _, err := o.svc.Webhook(ctx, ev[0].Name, ev[0].Signature, ev[0].Body); code(err) != "provider_not_configured" {
			t.Fatalf("%v", err)
		}
		if installerOf(t, st, id) != nil {
			t.Fatal("a delivery was believed without a secret")
		}
	})
}

// A member of the GitHub organisation who reaches its repositories, and so
// sees its installation listed, is not who installed it: their claim is
// refused, with or without an authorisation in the same flow. Only the
// installer's claim connects it.
func TestAnOrgMemberWhoDidNotInstallItCannotClaimIt(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		o := newOrg(t, st)
		o.f.AddUser("plain-member")
		o.f.AddMember("petal-team", "plain-member")
		// kaz-admin, an owner, installs it on GitHub; the event says so.
		q, _ := o.startOrg("A1", "T1", "petal-team", "kaz-admin")
		inst := installationID(t, q)
		o.deliver()
		if in := installerOf(t, st, inst); in == nil || in.Sender != "kaz-admin" || in.Account != "petal-team" || in.AccountType != "Organization" {
			t.Fatalf("%+v", in)
		}
		t1 := store.InstallOwner{TenantID: "T1"}
		for _, authorize := range []bool{true, false} {
			if !authorize {
				o.connect("A2", "plain-member") // no code: the claimer's own link is asked
			}
			if to := o.installFor("A2", t1, "petal-team", "plain-member", "petal-team", authorize); to != "/back?connection_error=not_installer" {
				t.Fatalf("authorize=%v: %q", authorize, to)
			}
			if _, err := store.GetGitHubInstallation(ctx, st.Q(), inst); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("a member who did not install it claimed it: %v", err)
			}
		}
		// The installer claims it.
		if id := o.installOrg("A1", "T1", "petal-team", "kaz-admin"); id != inst {
			t.Fatalf("%d", id)
		}
		if in, _ := store.GetGitHubInstallation(ctx, st.Q(), inst); in.OwnerTenant != "T1" || in.ClaimedBy != "A1" {
			t.Fatalf("%+v", in)
		}
	})
}

// The installer's claim, when GitHub's event came first, connects at once.
func TestTheInstallerClaimsTheOrgsInstallation(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		o := newOrg(t, st)
		q, binding := o.startOrg("A1", "T1", "petal-team", "kaz-admin")
		o.deliver()
		if to, _ := o.svc.CompleteAuthorization(allowAll(ctx), binding, "A1", q); to != "/back" {
			t.Fatalf("%q", to)
		}
		if id := o.claimed(store.InstallOwner{TenantID: "T1"}, "petal-team"); id != installationID(t, q) {
			t.Fatalf("%d", id)
		}
		if p, _ := o.svc.PendingClaims(ctx, store.InstallOwner{TenantID: "T1"}); len(p) != 0 {
			t.Fatalf("%+v", p)
		}
	})
}

// GitHub's redirect can come before its event: the claim waits, shown as
// pending to the organisation, and the event completes it. An event whose
// sender is someone else fails the claim instead, shown until it expires; a
// claim nobody's event completes expires with the attempt.
func TestTheWebhookAfterTheCallbackCompletesThePendingClaim(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		o := newOrg(t, st)
		t1 := store.InstallOwner{TenantID: "T1"}
		q, binding := o.startOrg("A1", "T1", "petal-team", "kaz-admin")
		inst := installationID(t, q)
		if to, _ := o.svc.CompleteAuthorization(allowAll(ctx), binding, "A1", q); to != "/back?connection_pending=petal-team" {
			t.Fatalf("%q", to)
		}
		if list, _ := o.svc.Installations(ctx, t1); len(list) != 0 {
			t.Fatalf("connected before GitHub said who installed it: %+v", list)
		}
		p, err := o.svc.PendingClaims(ctx, t1)
		if err != nil || len(p) != 1 || p[0].ID != inst || p[0].Account != "petal-team" || p[0].ClaimedBy != "A1" || p[0].Error != "" {
			t.Fatalf("%+v %v", p, err)
		}
		res := o.deliver()
		if len(res) != 1 || len(res[0].Claimed) != 1 || res[0].Claimed[0].Owner != t1 || res[0].Claimed[0].ClaimedBy != "A1" {
			t.Fatalf("%+v", res)
		}
		if id := o.claimed(t1, "petal-team"); id != inst {
			t.Fatalf("%d", id)
		}
		if p, _ := o.svc.PendingClaims(ctx, t1); len(p) != 0 {
			t.Fatalf("%+v", p)
		}

		// Another organisation's admin whose own GitHub user did not install it.
		o.tenantUser("T2", "B1", "b-admin")
		o.f.AddUser("b-gh")
		o.f.AddOrg("other-org", "b-gh", "kaz-admin")
		t2 := store.InstallOwner{TenantID: "T2"}
		q, binding = o.startOrg("B1", "T2", "other-org", "b-gh")
		other := installationID(t, q)
		o.f.Events() // b-gh's own event is lost on the way
		if to, _ := o.svc.CompleteAuthorization(allowAll(ctx), binding, "B1", q); to != "/back?connection_pending=other-org" {
			t.Fatalf("%q", to)
		}
		// GitHub says kaz-admin installed it.
		e := o.f.InstallationEvent("created", other, "kaz-admin")
		if r, err := o.svc.Webhook(ctx, e.Name, e.Signature, e.Body); err != nil || len(r.Claimed) != 0 {
			t.Fatalf("%+v %v", r, err)
		}
		if p, _ := o.svc.PendingClaims(ctx, t2); len(p) != 1 || p[0].Error != "not_installer" {
			t.Fatalf("%+v", p)
		}
		if list, _ := o.svc.Installations(ctx, t2); len(list) != 0 {
			t.Fatalf("%+v", list)
		}
		// It is shown until it expires, like an attempt.
		o.clk.Advance(11 * time.Minute)
		if p, _ := o.svc.PendingClaims(ctx, t2); len(p) != 0 {
			t.Fatalf("%+v", p)
		}
	})
}

// GitHub redelivers: the same created event again, before or after the claim,
// changes nothing and connects nothing twice; nor does a pending claim's event
// delivered twice.
func TestARedeliveredWebhookIsHarmless(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		o := newOrg(t, st)
		t1 := store.InstallOwner{TenantID: "T1"}
		q, binding := o.startOrg("A1", "T1", "petal-team", "kaz-admin")
		inst := installationID(t, q)
		ev := o.f.Events()
		if to, _ := o.svc.CompleteAuthorization(allowAll(ctx), binding, "A1", q); !strings.Contains(to, "connection_pending=") {
			t.Fatalf("%q", to)
		}
		claims := 0
		for range 3 {
			r, err := o.svc.Webhook(ctx, ev[0].Name, ev[0].Signature, ev[0].Body)
			if err != nil {
				t.Fatal(err)
			}
			claims += len(r.Claimed)
		}
		if claims != 1 {
			t.Fatalf("connected %d times", claims)
		}
		if in := installerOf(t, st, inst); in == nil || in.Sender != "kaz-admin" {
			t.Fatalf("%+v", in)
		}
		list, _ := o.svc.Installations(ctx, t1)
		if len(list) != 1 || list[0].ID != inst || list[0].ClaimedBy != "A1" {
			t.Fatalf("%+v", list)
		}
		if n := countRows(t, st, `SELECT COUNT(*) FROM github_installers`); n != 1 {
			t.Fatalf("%d installer rows", n)
		}
		// Another event, of no interest, is ignored.
		if r, err := o.svc.Webhook(ctx, "push", ev[0].Signature, ev[0].Body); err != nil || len(r.Claimed) != 0 || r.Uninstalled != nil {
			t.Fatalf("%+v %v", r, err)
		}
	})
}

// Uninstalled on GitHub: the installation is disconnected from whoever has it
// here, as a disconnect would (the spaces show they lost access), and who
// installed it is forgotten.
func TestAnUninstallDisconnects(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		o := newOrg(t, st)
		t1 := store.InstallOwner{TenantID: "T1"}
		o.peti = o.installOrg("A1", "T1", "petal-team", "kaz-admin")
		docs := o.f.RepoByName("petal-team/docs")
		o.spaceIn("T1", "S1", "M1", store.Remote{Via: "app", InstallationID: o.peti, RepoID: docs.ID})
		var lost []string
		o.svc.OnAccessLost(func(ids []string) { lost = ids })
		o.f.Uninstall(o.peti, "kaz-admin")
		res := o.deliver()
		if len(res) != 1 || res[0].Uninstalled == nil || res[0].Uninstalled.Owner != t1 || len(res[0].Uninstalled.Spaces) != 1 {
			t.Fatalf("%+v", res)
		}
		if len(lost) != 1 || lost[0] != "S1" {
			t.Fatalf("%v", lost)
		}
		if list, _ := o.svc.Installations(ctx, t1); len(list) != 0 {
			t.Fatalf("%+v", list)
		}
		r, _ := store.GetRemote(ctx, st.Q(), "S1", store.RoleContent)
		if r.State != store.RemoteError || r.ErrorKind != "access_lost" {
			t.Fatalf("%+v", r)
		}
		if installerOf(t, st, o.peti) != nil {
			t.Fatal("the installer of an uninstalled installation is kept")
		}
		// Delivered again: nothing more happens.
		e := o.f.InstallationEvent("deleted", o.peti, "kaz-admin")
		if r, err := o.svc.Webhook(ctx, e.Name, e.Signature, e.Body); err != nil || r.Uninstalled == nil || r.Uninstalled.Owner.Valid() {
			t.Fatalf("%+v %v", r, err)
		}
	})
}

// An installation no webhook said the installer of (GitHub did not send it,
// or it was made while the webhook was off) cannot be claimed for an
// organisation: GitHub says nothing new is coming (setup_action update).
func TestAnInstallationWithoutAnInstallerRecordIsRefused(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		o := newOrg(t, st)
		inst := o.f.Install("petal-team", "all") // installed with no event
		to := o.installFor("A1", store.InstallOwner{TenantID: "T1"}, "petal-team", "kaz-admin", "petal-team", true)
		if to != "/back?connection_error=not_installer" {
			t.Fatalf("%q", to)
		}
		if _, err := store.GetGitHubInstallation(ctx, st.Q(), inst); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%v", err)
		}
		if p, _ := o.svc.PendingClaims(ctx, store.InstallOwner{TenantID: "T1"}); len(p) != 0 {
			t.Fatalf("a claim waits for an event that is not coming: %+v", p)
		}
	})
}

func countRows(t *testing.T, st *store.Store, query string) int {
	t.Helper()
	var n int
	if err := st.Q().QueryRow(ctx, query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
