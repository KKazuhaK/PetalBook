package github_test

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"petalbook.app/server/internal/connections"
	"petalbook.app/server/internal/githubapp/githubapptest"
	"petalbook.app/server/internal/gitstore"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// org is a world with an organisation: tenant T1, whose administrator A1 is
// the GitHub user "kaz-admin" (a member of the GitHub organisation
// "petal-team"), a second administrator A2 and a member M1, neither connected
// to GitHub. U1 ("tang", default tenant) is a person of their own.
type org struct {
	*world
	peti int64 // petal-team's installation once T1 connected it
}

func newOrg(t *testing.T, st *store.Store) *org {
	w := newWorld(t, st)
	w.tenantUser("T1", "A1", "admin1")
	w.tenantUser("T1", "A2", "admin2")
	w.tenantUser("T1", "M1", "member1")
	w.f.AddUser("kaz-admin")
	w.f.AddOrg("petal-team", "kaz-admin")
	w.f.AddRepo("petal-team", "docs", true)
	return &org{world: w}
}

// spaceIn makes a space of a tenant with a GitHub remote row and its owner.
func (h *harness) spaceIn(tenant, id, owner string, r store.Remote) {
	h.t.Helper()
	if err := store.InsertSpace(ctx, h.st.Q(), tenant, id, id, "", "", h.now); err != nil {
		h.t.Fatal(err)
	}
	r.SpaceID, r.URL, r.Branch, r.CredType, r.State, r.Updated, r.Provider = id, "https://github.com/x/y.git", "main", "connection", "ok", h.now, "github"
	if err := store.PutRemote(ctx, h.st.Q(), r); err != nil {
		h.t.Fatal(err)
	}
	if owner != "" {
		if err := store.AddMember(ctx, h.st.Q(), id, owner, "owner", h.now); err != nil {
			h.t.Fatal(err)
		}
	}
}

func (h *harness) gitToken(id string) (string, *gitstore.CredentialError) {
	h.t.Helper()
	r, err := store.GetRemote(ctx, h.st.Q(), id, store.RoleContent)
	if err != nil {
		h.t.Fatal(err)
	}
	tok, err := h.svc.GitToken(ctx, r, false)
	var ce *gitstore.CredentialError
	if err != nil && !errors.As(err, &ce) {
		h.t.Fatalf("not a credential error: %v", err)
	}
	return tok, ce
}

// A person installs the App on their own GitHub account: the installation is
// theirs, lists its repositories, and their own space syncs through it until
// they disconnect it.
func TestAPersonInstallsOnTheirOwnAccountAndTheirSpaceSyncs(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		w := newWorld(t, st)
		w.f.AddRepo("tang", "notes", true)
		id := w.installMine("U1", "tang")
		list, err := w.svc.Installations(ctx, store.InstallOwner{UserID: "U1"})
		if err != nil || len(list) != 1 || list[0].ID != id || list[0].Account != "tang" || list[0].AccountType != "User" ||
			list[0].ManageURL != w.f.URL+"/settings/installations/"+strconv.FormatInt(id, 10) {
			t.Fatalf("%+v %v", list, err)
		}
		repos, _, err := w.svc.InstallationRepos(ctx, store.InstallOwner{UserID: "U1"}, id, "")
		if err != nil || len(repos) != 1 || repos[0].FullName != "tang/notes" {
			t.Fatalf("%+v %v", repos, err)
		}
		// Someone else sees nothing of it.
		w.user("U9", "nine")
		if _, _, err := w.svc.InstallationRepos(ctx, store.InstallOwner{UserID: "U9"}, id, ""); code(err) != "not_found" {
			t.Fatalf("%v", err)
		}
		c, r, err := w.svc.ResolveExisting(ctx, "U1", "tang", "notes", "")
		if err != nil || c.Via != "app" || c.InstallationID != id || c.UserID != "U1" || r.FullName != "tang/notes" {
			t.Fatalf("%+v %v", c, err)
		}
		w.spaceIn(store.DefaultTenantID, "S1", "U1", store.Remote{Via: c.Via, InstallationID: c.InstallationID, RepoID: c.RepoID, CredUserID: c.UserID})
		if tok, ce := w.gitToken("S1"); ce != nil || !strings.HasPrefix(tok, "ghs_") {
			t.Fatalf("%q %v", tok, ce)
		}
		// Not the person's to manage any more: it stops.
		store.SetMemberRole(ctx, st.Q(), "S1", "U1", "reader")
		if _, ce := w.gitToken("S1"); ce == nil || ce.Code != "connection_missing" {
			t.Fatalf("%v", ce)
		}
		store.SetMemberRole(ctx, st.Q(), "S1", "U1", "owner")
		spaces, n, err := w.svc.InstallationSpaces(ctx, store.InstallOwner{UserID: "U1"}, id)
		if err != nil || n != 1 || spaces[0].ID != "S1" {
			t.Fatalf("%+v %d %v", spaces, n, err)
		}
		var lost []string
		w.svc.OnAccessLost(func(ids []string) { lost = ids })
		if _, err := w.svc.DisconnectInstallation(ctx, store.InstallOwner{UserID: "U9"}, id); code(err) != "not_found" {
			t.Fatalf("another person disconnected it: %v", err)
		}
		ids, err := w.svc.DisconnectInstallation(ctx, store.InstallOwner{UserID: "U1"}, id)
		if err != nil || len(ids) != 1 || len(lost) != 1 || lost[0] != "S1" {
			t.Fatalf("%v %v %v", ids, lost, err)
		}
		if _, ce := w.gitToken("S1"); ce == nil || ce.Code != "access_lost" {
			t.Fatalf("%v", ce)
		}
		// Connected again by its person: the space has it again.
		if again := w.installMine("U1", "tang"); again != id {
			t.Fatalf("%d", again)
		}
		if _, ce := w.gitToken("S1"); ce != nil {
			t.Fatalf("%v", ce)
		}
		w.mustNotLeak()
	})
}

// An organisation's administrator installs the App on the GitHub organisation:
// the installation is the tenant's, not theirs; the organisation's spaces sync
// through it alone; any administrator manages and disconnects it.
func TestAnOrgAdminInstallsOnTheGitHubOrgAndTheOrgSpaceSyncs(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		o := newOrg(t, st)
		t1 := store.InstallOwner{TenantID: "T1"}
		o.peti = o.installOrg("A1", "T1", "petal-team", "kaz-admin")
		// Installing for the organisation gave A1 no GitHub link of their own.
		if c, err := o.svc.Connection(ctx, "A1"); err != nil || c != nil {
			t.Fatalf("%+v %v", c, err)
		}
		list, err := o.svc.Installations(ctx, t1)
		if err != nil || len(list) != 1 || list[0].Owner != t1 || list[0].ClaimedBy != "A1" || list[0].AccountType != "Organization" ||
			!strings.Contains(list[0].ManageURL, "/organizations/petal-team/settings/installations/") {
			t.Fatalf("%+v %v", list, err)
		}
		// A member without any GitHub connection makes a space with it.
		got, _ := owners(t, o.harness, "M1")
		if len(got) != 1 || got[0].Login != "petal-team" || got[0].Via != "app" || got[0].InstallationID != o.peti {
			t.Fatalf("%+v", got)
		}
		r, p, err := create(t, o.world, "M1", "petal-team", "wiki")
		if err != nil || p.Via != "app" || r.FullName != "petal-team/wiki" {
			t.Fatalf("%+v %+v %v", p, r, err)
		}
		cred, err := o.svc.ChooseCredential(ctx, p, r)
		if err != nil || cred.Via != "app" || cred.InstallationID != o.peti || cred.UserID != "" {
			t.Fatalf("%+v %v", cred, err)
		}
		if items, _, err := o.svc.Repos(ctx, "M1", connections.RepoQuery{Installation: o.peti}); err != nil || len(items) != 2 {
			t.Fatalf("%+v %v", items, err)
		}
		c, _, err := o.svc.ResolveExistingIn(ctx, "T1", "M1", "petal-team", "docs", "")
		if err != nil || c.Via != "app" || c.InstallationID != o.peti {
			t.Fatalf("%+v %v", c, err)
		}
		o.spaceIn("T1", "S1", "M1", store.Remote{Via: c.Via, InstallationID: c.InstallationID, RepoID: c.RepoID})
		if tok, ce := o.gitToken("S1"); ce != nil || !strings.HasPrefix(tok, "ghs_") {
			t.Fatalf("%q %v", tok, ce)
		}
		// Another administrator of the tenant (the API checks the key) sees and disconnects it.
		spaces, n, err := o.svc.InstallationSpaces(ctx, t1, o.peti)
		if err != nil || n != 1 || spaces[0].ID != "S1" {
			t.Fatalf("%+v %d %v", spaces, n, err)
		}
		if _, err := o.svc.DisconnectInstallation(ctx, store.InstallOwner{TenantID: "T2"}, o.peti); code(err) != "not_found" {
			t.Fatalf("another tenant disconnected it: %v", err)
		}
		if _, err := o.svc.DisconnectInstallation(ctx, store.InstallOwner{UserID: "A1"}, o.peti); code(err) != "not_found" {
			t.Fatalf("the administrator who installed it owns it: %v", err)
		}
		if ids, err := o.svc.DisconnectInstallation(ctx, t1, o.peti); err != nil || len(ids) != 1 {
			t.Fatalf("%v %v", ids, err)
		}
		if _, ce := o.gitToken("S1"); ce == nil || ce.Code != "access_lost" {
			t.Fatalf("%v", ce)
		}
		r2, _ := store.GetRemote(ctx, st.Q(), "S1", store.RoleContent)
		if r2.State != store.RemoteError || r2.ErrorKind != "access_lost" {
			t.Fatalf("%+v", r2)
		}
		// Gone from the organisation: nothing to create in.
		if _, err := o.svc.PlanCreate(ctx, "M1", "petal-team"); code(err) != "owner_not_allowed" {
			t.Fatalf("%v", err)
		}
	})
}

// The installation_id of GitHub's redirect is the browser's claim only: one
// GitHub does not list for the person's own token is refused, with or without
// an authorisation in the same flow.
func TestAForgedInstallationIDIsRefused(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		o := newOrg(t, st)
		o.f.AddOrg("stranger")
		other := o.f.Install("stranger", "all") // tang and kaz-admin are not members
		hc := allowAll(ctx)
		for _, authorize := range []bool{true, false} {
			for _, who := range []struct {
				user  string
				owner store.InstallOwner
				acct  string
				login string
			}{{"U1", store.InstallOwner{UserID: "U1"}, "", "tang"}, {"A1", store.InstallOwner{TenantID: "T1"}, "stranger", "kaz-admin"}} {
				if !authorize && who.user == "A1" {
					o.connect("A1", "kaz-admin") // no code: the person's own link is asked
				}
				u, binding, err := o.svc.StartInstallFor(hc, who.user, who.owner, who.acct, "/back")
				if err != nil {
					t.Fatal(err)
				}
				back, _ := url.Parse(o.f.InstallAppOn(u, who.login, who.login, o.svc.CallbackURL(), authorize))
				q := back.Query()
				q.Set("installation_id", strconv.FormatInt(other, 10)) // forged
				to, _ := o.svc.CompleteAuthorization(hc, binding, who.user, q)
				if !strings.Contains(to, "connection_error=installation_not_yours") {
					t.Fatalf("%s authorize=%v: %q", who.user, authorize, to)
				}
			}
		}
		if _, err := store.GetGitHubInstallation(ctx, st.Q(), other); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a forged installation was connected: %v", err)
		}
		// Garbage and a missing id are refused too.
		for _, bad := range []string{"", "0", "-3", "x"} {
			u, binding, _ := o.svc.StartInstallFor(hc, "U1", store.InstallOwner{UserID: "U1"}, "", "/back")
			q := url.Values{"state": {stateOf(u)}, "setup_action": {"install"}}
			if bad != "" {
				q.Set("installation_id", bad)
			}
			if to, _ := o.svc.CompleteAuthorization(hc, binding, "U1", q); !strings.Contains(to, "connection_error=") {
				t.Fatalf("%q: %q", bad, to)
			}
		}
	})
}

// One installation has one owner here: a second owner's claim is refused, and
// it must be on the account the attempt was for.
func TestASecondOwnerOrAnotherAccountIsRefused(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		o := newOrg(t, st)
		o.peti = o.installOrg("A1", "T1", "petal-team", "kaz-admin")
		o.tenantUser("T2", "B1", "b-admin")
		o.f.AddUser("b-gh")
		o.f.SetAccount("petal-team", func(a *githubapptest.Account) { a.Members["b-gh"] = true })
		if to := o.installFor("B1", store.InstallOwner{TenantID: "T2"}, "petal-team", "b-gh", "petal-team", true); !strings.Contains(to, "connection_error=installation_taken") {
			t.Fatalf("%q", to)
		}
		if in, _ := store.GetGitHubInstallation(ctx, st.Q(), o.peti); in.OwnerTenant != "T1" {
			t.Fatalf("%+v", in)
		}
		// An organisation's attempt that ends on another organisation, or on a person's account.
		o.f.AddOrg("elsewhere", "kaz-admin")
		if to := o.installFor("A1", store.InstallOwner{TenantID: "T1"}, "petal-team", "kaz-admin", "elsewhere", true); !strings.Contains(to, "connection_error=wrong_account") {
			t.Fatalf("%q", to)
		}
		if to := o.installFor("A1", store.InstallOwner{TenantID: "T1"}, "kaz-admin", "kaz-admin", "kaz-admin", true); !strings.Contains(to, "connection_error=wrong_account") {
			t.Fatalf("%q", to)
		}
		// A person's attempt that ends on an organisation.
		if to := o.installFor("U1", store.InstallOwner{UserID: "U1"}, "", "tang", "kazuhahub", true); to != "/settings?provider=github&connection_error=wrong_account" {
			t.Fatalf("%q", to)
		}
		if _, err := store.GetGitHubInstallation(ctx, st.Q(), o.kazuhahub); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%v", err)
		}
		// Not for the default tenant, nor for someone else.
		if _, _, err := o.svc.StartInstallFor(allowAll(ctx), "U1", store.InstallOwner{TenantID: store.DefaultTenantID}, "kazuhahub", "/"); code(err) != "forbidden" {
			t.Fatalf("%v", err)
		}
		if _, _, err := o.svc.StartInstallFor(allowAll(ctx), "U1", store.InstallOwner{UserID: "A1"}, "", "/"); code(err) != "forbidden" {
			t.Fatalf("%v", err)
		}
		if _, _, err := o.svc.StartInstallFor(allowAll(ctx), "A1", store.InstallOwner{TenantID: "T1"}, "not/a/name", "/"); code(err) != "invalid" {
			t.Fatalf("%v", err)
		}
	})
}

// Whether the person may is asked at the start and again on the callback:
// without the caller's hooks, or with a refusal, nothing is connected.
func TestTheCallerDecidesWhoMayInstall(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		o := newOrg(t, st)
		t1 := store.InstallOwner{TenantID: "T1"}
		if _, _, err := o.svc.StartInstallFor(ctx, "A1", t1, "petal-team", "/"); code(err) != "forbidden" {
			t.Fatalf("no hooks: %v", err)
		}
		no := connections.Errorf("github", 403, "feature_off", "off")
		deny := connections.WithInstallHooks(ctx, connections.InstallHooks{Allow: func(context.Context, store.InstallOwner) error { return no }})
		if _, _, err := o.svc.StartInstallFor(deny, "A1", t1, "petal-team", "/"); code(err) != "feature_off" {
			t.Fatalf("%v", err)
		}
		// Allowed at the start, no longer on the callback (the key or the feature went).
		u, binding, err := o.svc.StartInstallFor(allowAll(ctx), "A1", t1, "petal-team", "/admin/github")
		if err != nil {
			t.Fatal(err)
		}
		back, _ := url.Parse(o.f.InstallAppOn(u, "kaz-admin", "petal-team", o.svc.CallbackURL(), true))
		if to, _ := o.svc.CompleteAuthorization(deny, binding, "A1", back.Query()); to != "/admin/github?connection_error=forbidden" {
			t.Fatalf("%q", to)
		}
		if list, _ := o.svc.Installations(ctx, t1); len(list) != 0 {
			t.Fatalf("%+v", list)
		}
		// An organisation that must approve the installation first.
		u, binding, _ = o.svc.StartInstallFor(allowAll(ctx), "A1", t1, "petal-team", "/admin/github")
		q := url.Values{"setup_action": {"request"}, "state": {stateOf(u)}}
		if to, _ := o.svc.CompleteAuthorization(allowAll(ctx), binding, "A1", q); to != "/admin/github?connection_error=requested" {
			t.Fatalf("%q", to)
		}
		// The Claimed hook hears of a connected one.
		var heard string
		hc := connections.WithInstallHooks(ctx, connections.InstallHooks{Allow: func(context.Context, store.InstallOwner) error { return nil },
			Claimed: func(_ context.Context, o store.InstallOwner, id int64, account string) {
				heard = o.TenantID + "/" + account
			}})
		u, binding, _ = o.svc.StartInstallFor(hc, "A1", t1, "petal-team", "/admin/github")
		back, _ = url.Parse(o.f.InstallAppOn(u, "kaz-admin", "petal-team", o.svc.CallbackURL(), true))
		if to, _ := o.svc.CompleteAuthorization(hc, binding, "A1", back.Query()); to != "/admin/github" || heard != "T1/petal-team" {
			t.Fatalf("%q %q", to, heard)
		}
	})
}

// An organisation's space never syncs with a person's own token, and a
// person's space never through an organisation's installation.
func TestOrgAndPersonalSpacesUseOnlyTheirOwn(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		o := newOrg(t, st)
		o.peti = o.installOrg("A1", "T1", "petal-team", "kaz-admin")
		o.connect("A1", "kaz-admin") // A1's own link, which may push to petal-team/docs
		// An organisation's space: not via the person's link.
		if _, _, err := o.svc.ResolveExistingIn(ctx, "T1", "A1", "petal-team", "docs", "user"); code(err) != "owner_not_allowed" {
			t.Fatalf("%v", err)
		}
		if _, _, err := o.svc.Repos(ctx, "A1", connections.RepoQuery{Mine: true}); code(err) != "owner_not_allowed" {
			t.Fatalf("%v", err)
		}
		if _, err := o.svc.PlanCreate(ctx, "A1", "kaz-admin"); code(err) != "owner_not_allowed" {
			t.Fatalf("an organisation's space in a person's account: %v", err)
		}
		docs := o.f.RepoByName("petal-team/docs")
		o.spaceIn("T1", "SO", "A1", store.Remote{Via: "user", CredUserID: "A1", RepoID: docs.ID})
		if _, ce := o.gitToken("SO"); ce == nil || ce.Code != "access_lost" {
			t.Fatalf("an organisation's space synced with a person's token: %v", ce)
		}
		// A person's space: not through the organisation's installation, though
		// the App's installation reaches the repository.
		o.f.SetAccount("petal-team", func(a *githubapptest.Account) { a.Members["tang"] = true })
		if _, _, err := o.svc.ResolveExisting(ctx, "U1", "petal-team", "docs", "app"); code(err) != "owner_not_allowed" {
			t.Fatalf("%v", err)
		}
		// Without a choice it falls back to the person's own link.
		if c, _, err := o.svc.ResolveExisting(ctx, "U1", "petal-team", "docs", ""); err != nil || c.Via != "user" || c.UserID != "U1" {
			t.Fatalf("%+v %v", c, err)
		}
		o.spaceIn(store.DefaultTenantID, "SP", "U1", store.Remote{Via: "app", InstallationID: o.peti, RepoID: docs.ID, CredUserID: "U1"})
		if _, ce := o.gitToken("SP"); ce == nil || ce.Code != "access_lost" {
			t.Fatalf("a person's space synced through an organisation's installation: %v", ce)
		}
		// An organisation's space through another organisation's installation.
		o.tenantUser("T2", "B1", "b")
		o.spaceIn("T2", "S2", "B1", store.Remote{Via: "app", InstallationID: o.peti, RepoID: docs.ID})
		if _, ce := o.gitToken("S2"); ce == nil || ce.Code != "access_lost" {
			t.Fatalf("%v", ce)
		}
		if _, _, err := o.svc.ResolveExistingIn(ctx, "T2", "B1", "petal-team", "docs", "app"); code(err) != "owner_not_allowed" {
			t.Fatalf("%v", err)
		}
		if _, _, err := o.svc.Repos(ctx, "B1", connections.RepoQuery{Installation: o.peti}); code(err) != "not_found" {
			t.Fatalf("%v", err)
		}
	})
}

// asOrg makes tenant T1 (its administrator A1, GitHub's "kaz-admin", and a
// member M1) if need be and has it connect the App's installations on these
// GitHub organisations, which must exist (kaz-admin is made a member of each).
func (w *world) asOrg(orgs ...string) map[string]int64 {
	w.t.Helper()
	if _, err := store.GetUser(ctx, w.st.Q(), store.AllTenants(), "A1"); err != nil {
		w.tenantUser("T1", "A1", "admin1")
		w.tenantUser("T1", "M1", "member1")
		w.f.AddUser("kaz-admin")
	}
	out := map[string]int64{}
	for _, login := range orgs {
		w.f.SetAccount(login, func(a *githubapptest.Account) { a.Members["kaz-admin"] = true })
		out[login] = w.installOrg("A1", "T1", login, "kaz-admin")
	}
	return out
}
