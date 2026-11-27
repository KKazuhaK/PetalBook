package github_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"petalbook.app/server/internal/connections"
	"petalbook.app/server/internal/githubapp/githubapptest"
	"petalbook.app/server/internal/gitstore"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// world is a configured server with the user U1 connected as the GitHub user
// "tang", a member of the organisation "kazuhahub".
type world struct {
	*harness
	kazuhahub int64 // installation, all repositories
}

func newWorld(t *testing.T, st *store.Store) *world {
	h := newHarness(t, st)
	h.configure()
	h.user("U1", "tang")
	h.f.AddUser("tang")
	h.f.AddOrg("kazuhahub", "tang")
	w := &world{harness: h, kazuhahub: h.f.Install("kazuhahub", "all")}
	h.connect("U1", "tang")
	return w
}

func owners(t *testing.T, h *harness, user string) (all []connections.Owner, pages int) {
	t.Helper()
	cur := ""
	for {
		items, next, err := h.svc.Owners(ctx, user, cur)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, items...)
		pages++
		if next == "" {
			return
		}
		cur = next
	}
}

func TestOwnersAreListedOnePageAtATime(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		w := newWorld(t, st)
		w.f.AddOrg("petal-team")
		w.f.Install("petal-team", "all")
		w.f.AddOrg("other")
		w.f.Install("other", "all")
		got, _ := owners(t, w.harness, "U1")
		var s []string
		for _, o := range got {
			s = append(s, fmt.Sprintf("%s/%s/%s/%v", o.Login, o.Type, o.Via, o.InstallationID > 0))
		}
		// The person's own account and the organisations their own token reaches.
		want := "tang/User/user/false kazuhahub/Organization/user/true"
		if strings.Join(s, " ") != want {
			t.Fatalf("%v", s)
		}
		// A person without a connection has none.
		w.user("U2", "nobody")
		if got, _ = owners(t, w.harness, "U2"); len(got) != 0 {
			t.Fatalf("%+v", got)
		}
	})
}

func TestOwnersPagesCostOneGitHubRequestEach(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		w := newWorld(t, st)
		for i := 0; i < 70; i++ {
			login := fmt.Sprintf("team-%03d", i)
			w.f.AddOrg(login, "tang")
			w.f.Install(login, "all")
		}
		before := w.f.Count("GET", "/user/installations")
		got, pages := owners(t, w.harness, "U1")
		// 71 organisations and the user's own account.
		if len(got) != 72 || pages != 3 {
			t.Fatalf("%d owners in %d pages", len(got), pages)
		}
		if n := w.f.Count("GET", "/user/installations") - before; n != 3 {
			t.Fatalf("%d GitHub requests for 3 pages", n)
		}
		if _, _, err := w.svc.Owners(ctx, "U1", "garbage"); code(err) != "invalid" {
			t.Fatalf("%v", err)
		}
	})
}

// An organisation's member chooses among the organisation's own installations,
// read from the database: GitHub is not asked.
func TestAnOrganisationsOwnersAreItsOwnInstallations(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		o := newOrg(t, st)
		o.f.AddOrg("second", "kaz-admin")
		o.installOrg("A1", "T1", "petal-team", "kaz-admin")
		o.installOrg("A1", "T1", "second", "kaz-admin")
		before := len(o.f.Requests())
		got, pages := owners(t, o.harness, "M1")
		if len(got) != 2 || pages != 1 || got[0].Via != "app" || got[1].Via != "app" {
			t.Fatalf("%+v in %d pages", got, pages)
		}
		if n := len(o.f.Requests()) - before; n != 0 {
			t.Fatalf("%d requests to GitHub for a list kept in the database", n)
		}
		// Another tenant's member sees none of them.
		o.tenantUser("T2", "B1", "b")
		if got, _ := owners(t, o.harness, "B1"); len(got) != 0 {
			t.Fatalf("%+v", got)
		}
	})
}

func TestPlanCreate(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		w := newWorld(t, st)
		w.f.AddOrg("petal-team")
		w.f.Install("petal-team", "all")
		w.f.AddOrg("other")
		w.f.Install("other", "all")

		p, err := w.svc.PlanCreate(ctx, "U1", "TANG")
		if err != nil || p.Via != "user" || p.Org != "" || p.Owner != "tang" {
			t.Fatalf("%+v %v", p, err)
		}
		p, err = w.svc.PlanCreate(ctx, "U1", "kazuhahub")
		if err != nil || p.Via != "user" || p.Org != "kazuhahub" || p.InstallationID != w.kazuhahub {
			t.Fatalf("%+v %v", p, err)
		}
		for _, owner := range []string{"petal-team", "other", "someone-else"} {
			if _, err := w.svc.PlanCreate(ctx, "U1", owner); code(err) != "owner_not_allowed" || status(err) != 403 {
				t.Fatalf("%s: %v", owner, err)
			}
		}
		if _, err := w.svc.PlanCreate(ctx, "U1", "a/b"); code(err) != "invalid" {
			t.Fatalf("%v", err)
		}
		// An expired connection cannot create anywhere.
		w.f.ExpireRefresh("tang")
		w.clk.Advance(9 * time.Hour)
		w.svc.UserToken(ctx, "U1")
		for _, owner := range []string{"tang", "kazuhahub"} {
			if _, err := w.svc.PlanCreate(ctx, "U1", owner); code(err) != "not_connected" || status(err) != 409 {
				t.Fatalf("%s: %v", owner, err)
			}
		}
	})
}

func create(t *testing.T, w *world, user, owner, name string) (*connections.Repo, *connections.Plan, error) {
	t.Helper()
	p, err := w.svc.PlanCreate(ctx, user, owner)
	if err != nil {
		return nil, nil, err
	}
	r, err := w.svc.CreateRepo(ctx, p, connections.NewRepoSpec{Name: name, Private: true, Description: "d"})
	return r, p, err
}

func TestCreateRepositories(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		w := newWorld(t, st)
		// In the user's own account, and in an organisation, with the user's token.
		r, _, err := create(t, w, "U1", "tang", "notes")
		if err != nil || r.FullName != "tang/notes" || !r.Private {
			t.Fatalf("%+v %v", r, err)
		}
		r, _, err = create(t, w, "U1", "kazuhahub", "notes")
		if err != nil || r.FullName != "kazuhahub/notes" {
			t.Fatalf("%+v %v", r, err)
		}
		// The repository is empty: no automatic first commit.
		if out := gitOut(t, w.f.BareDir("kazuhahub/notes"), "rev-parse", "--verify", "-q", "main"); out != "" {
			t.Fatalf("the new repository has a commit: %q", out)
		}
		// A name already taken.
		if _, _, err := create(t, w, "U1", "tang", "notes"); code(err) != "name_taken" || status(err) != 409 {
			t.Fatalf("%v", err)
		}
		// An organisation that does not let members create repositories: GitHub's own reason.
		w.f.SetAccount("kazuhahub", func(a *githubapptest.Account) { a.NoCreate = true })
		_, _, err = create(t, w, "U1", "kazuhahub", "another")
		if code(err) != "provider_refused" || status(err) != 403 || !strings.Contains(err.Error(), "permission to create repositories") {
			t.Fatalf("%v", err)
		}
		w.f.SetAccount("kazuhahub", func(a *githubapptest.Account) { a.NoCreate, a.NoPrivate = false, true })
		_, _, err = create(t, w, "U1", "kazuhahub", "another")
		if code(err) != "provider_refused" || !strings.Contains(err.Error(), "Private repositories") {
			t.Fatalf("%v", err)
		}
		// A refusal of the fields.
		p, _ := w.svc.PlanCreate(ctx, "U1", "tang")
		if _, err := w.svc.CreateRepo(ctx, p, connections.NewRepoSpec{Name: "bad name"}); code(err) != "invalid" || status(err) != 400 {
			t.Fatalf("%v", err)
		}
		// GitHub limiting the server answers 429 with the wait.
		w.f.LimitUntil(w.clk.Now().Add(90 * time.Second))
		_, err = w.svc.CreateRepo(ctx, p, connections.NewRepoSpec{Name: "limited"})
		var he *connections.Error
		if !errors.As(err, &he) || he.Code != "rate_limited" || he.Status != 429 || he.RetryAfter < 89*time.Second {
			t.Fatalf("%v", err)
		}
	})
}

func TestCreateWithTheOrganisationsOwnInstallation(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		o := newOrg(t, st)
		o.installOrg("A1", "T1", "petal-team", "kaz-admin")
		// M1 has no GitHub connection at all.
		r, p, err := create(t, o.world, "M1", "petal-team", "wiki")
		if err != nil || r.FullName != "petal-team/wiki" || p.Via != "app" {
			t.Fatalf("%+v %v", r, err)
		}
		// The administration token is made for this request alone.
		minted := o.f.Minted()
		if _, _, err := create(t, o.world, "M1", "petal-team", "wiki2"); err != nil || o.f.Minted() != minted+1 {
			t.Fatalf("%v %d", err, o.f.Minted()-minted)
		}
		// An App without the Administration permission: the answer names the permission.
		o.f.AppPerms = map[string]string{"members": "read", "contents": "write", "metadata": "read"}
		if _, _, err := create(t, o.world, "M1", "petal-team", "wiki3"); code(err) != "app_missing_permission" || status(err) != 403 {
			t.Fatalf("%v", err)
		}
	})
}

func TestChooseCredential(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		w := newWorld(t, st)
		// A person's space in an organisation GitHub lists for them: their own token.
		r, p, _ := create(t, w, "U1", "kazuhahub", "a")
		c, err := w.svc.ChooseCredential(ctx, p, r)
		if err != nil || c.Via != "user" || c.UserID != "U1" || c.RepoID != r.ID {
			t.Fatalf("%+v %v", c, err)
		}
		// Their own account without an installation of theirs here: their token,
		// even with the App installed there on GitHub.
		w.f.Install("tang", "all")
		r, p, _ = create(t, w, "U1", "tang", "a")
		if c, err = w.svc.ChooseCredential(ctx, p, r); err != nil || c.Via != "user" || c.UserID != "U1" || c.RepoID != r.ID {
			t.Fatalf("%+v %v", c, err)
		}
		// Connected to them: the installation's, in their name.
		tangInst := w.installMine("U1", "tang")
		r, p, _ = create(t, w, "U1", "tang", "b")
		if c, err = w.svc.ChooseCredential(ctx, p, r); err != nil || c.Via != "app" || c.InstallationID != tangInst || c.UserID != "U1" {
			t.Fatalf("%+v %v", c, err)
		}
	})
}

func TestSelectedRepositoriesInstallations(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		o := newOrg(t, st)
		o.f.AddOrg("sel", "kaz-admin")
		sel := o.f.Install("sel", "selected")
		o.installOrg("A1", "T1", "sel", "kaz-admin")
		// An organisation's installation that does not get the new repository: 409
		// naming it; nobody's personal token adds it.
		r, p, err := create(t, o.world, "M1", "sel", "stuck")
		if err != nil {
			t.Fatal(err)
		}
		_, err = o.svc.ChooseCredential(ctx, p, r)
		var he *connections.Error
		if !errors.As(err, &he) || he.Code != "repo_not_in_installation" || he.Status != 409 || he.Repo == nil || he.Repo.FullName != "sel/stuck" {
			t.Fatalf("%v", err)
		}
		if o.f.Count("PUT", "/user/installations/") != 0 {
			t.Fatal("a person's token changed the organisation's installation")
		}
		// When GitHub adds it by itself it is the installation's.
		o.f.AutoAdd = true
		r, p, _ = create(t, o.world, "M1", "sel", "auto")
		if c, err := o.svc.ChooseCredential(ctx, p, r); err != nil || c.Via != "app" || c.InstallationID != sel {
			t.Fatalf("%+v %v", c, err)
		}
		o.f.AutoAdd = false
		// A person's own installation of selected repositories: their token adds it.
		tangInst := o.f.Install("tang", "selected")
		o.installMine("U1", "tang")
		r, p, _ = create(t, o.world, "U1", "tang", "mine")
		c, err := o.svc.ChooseCredential(ctx, p, r)
		if err != nil || c.Via != "app" || c.InstallationID != tangInst || o.f.Count("PUT", "/user/installations/") != 1 {
			t.Fatalf("%+v %v", c, err)
		}
		// May not add: their own token.
		o.f.SetInstall(tangInst, func(i *githubapptest.Install) { i.NoAdd = true })
		r, p, _ = create(t, o.world, "U1", "tang", "mine2")
		if c, err = o.svc.ChooseCredential(ctx, p, r); err != nil || c.Via != "user" {
			t.Fatalf("%+v %v", c, err)
		}
	})
}

func TestExistingRepositories(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		w := newWorld(t, st)
		w.f.AddRepo("kazuhahub", "wiki", true)
		w.f.AddRepo("tang", "mine", true)
		w.f.AddOrg("none")
		w.f.AddRepo("none", "x", true)

		// An organisation's installation is no person's: via the App is refused,
		// and with no preference the person's own token is used.
		if _, _, err := w.svc.ResolveExisting(ctx, "U1", "kazuhahub", "wiki", "app"); code(err) != "owner_not_allowed" || status(err) != 403 {
			t.Fatalf("%v", err)
		}
		c, r, err := w.svc.ResolveExisting(ctx, "U1", "kazuhahub", "wiki", "")
		if err != nil || c.Via != "user" || c.UserID != "U1" || r.FullName != "kazuhahub/wiki" || c.RepoID != r.ID {
			t.Fatalf("%+v %v", c, err)
		}
		w.user("U2", "nobody")
		if _, _, err := w.svc.ResolveExisting(ctx, "U2", "kazuhahub", "wiki", ""); code(err) != "owner_not_allowed" {
			t.Fatalf("%v", err)
		}
		// The App is not installed there.
		if _, _, err := w.svc.ResolveExisting(ctx, "U1", "none", "x", "app"); code(err) != "repo_not_in_installation" || status(err) != 409 {
			t.Fatalf("%v", err)
		}
		// Via the user: needs the connection and push access.
		if c, _, err := w.svc.ResolveExisting(ctx, "U1", "tang", "mine", "user"); err != nil || c.Via != "user" || c.UserID != "U1" {
			t.Fatalf("%+v %v", c, err)
		}
		if _, _, err := w.svc.ResolveExisting(ctx, "U2", "tang", "mine", "user"); code(err) != "not_connected" {
			t.Fatalf("%v", err)
		}
		if _, _, err := w.svc.ResolveExisting(ctx, "U1", "none", "x", "user"); code(err) != "owner_not_allowed" {
			t.Fatalf("%v", err)
		}
		// With no preference, a repository the App does not reach falls back to the user.
		if c, _, err := w.svc.ResolveExisting(ctx, "U1", "tang", "mine", ""); err != nil || c.Via != "user" {
			t.Fatalf("%+v %v", c, err)
		}
		// Their own installation: via the App, in their name.
		inst := w.installMine("U1", "tang")
		if c, _, err := w.svc.ResolveExisting(ctx, "U1", "tang", "mine", ""); err != nil || c.Via != "app" || c.InstallationID != inst || c.UserID != "U1" {
			t.Fatalf("%+v %v", c, err)
		}
		// An organisation's installation of selected repositories that does not include it.
		o := &org{world: w}
		w.tenantUser("T1", "A1", "admin1")
		w.f.AddUser("kaz-admin")
		w.f.AddOrg("sel", "kaz-admin")
		w.f.Install("sel", "selected")
		w.f.AddRepo("sel", "left-out", true)
		o.installOrg("A1", "T1", "sel", "kaz-admin")
		if _, _, err := w.svc.ResolveExistingIn(ctx, "T1", "A1", "sel", "left-out", "app"); code(err) != "repo_not_in_installation" {
			t.Fatalf("%v", err)
		}
	})
}

func TestRepositoryListsArePaged(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		w := newWorld(t, st)
		for i := 0; i < 40; i++ {
			w.f.AddRepo("kazuhahub", fmt.Sprintf("r%02d", i), i%2 == 0)
			w.f.AddRepo("tang", fmt.Sprintf("m%02d", i), true)
		}
		page := func(inst int64, mine bool, cur string) ([]connections.RepoItem, string) {
			items, next, err := w.svc.Repos(ctx, "U1", connections.RepoQuery{Installation: inst, Mine: mine, Cursor: cur})
			if err != nil {
				t.Fatal(err)
			}
			return items, next
		}
		before := w.f.Count("GET", "/user/installations/")
		p1, next := page(w.kazuhahub, false, "")
		if len(p1) != 30 || next == "" || !p1[0].CanPush || p1[0].DefaultBranch != "main" {
			t.Fatalf("%d %q %+v", len(p1), next, p1[0])
		}
		p2, next := page(w.kazuhahub, false, next)
		if len(p2) != 10 || next != "" || w.f.Count("GET", "/user/installations/")-before != 2 {
			t.Fatalf("%d %q", len(p2), next)
		}
		m1, next := page(0, true, "")
		if len(m1) != 30 || next == "" {
			t.Fatalf("%d", len(m1))
		}
		// An installation the caller can neither reach nor has.
		w.f.AddOrg("other")
		other := w.f.Install("other", "all")
		if _, _, err := w.svc.Repos(ctx, "U1", connections.RepoQuery{Installation: other, Mine: false, Cursor: ""}); code(err) != "not_found" || status(err) != 404 {
			t.Fatalf("%v", err)
		}
		// Their own: every repository, through the installation's token.
		inst := w.installMine("U1", "tang")
		items, _, err := w.svc.Repos(ctx, "U1", connections.RepoQuery{Installation: inst, Mine: false, Cursor: ""})
		if err != nil || len(items) != 30 || !items[0].CanPush {
			t.Fatalf("%d %v", len(items), err)
		}
		// Mine needs the connection.
		w.user("U2", "nobody")
		if _, _, err := w.svc.Repos(ctx, "U2", connections.RepoQuery{Installation: 0, Mine: true, Cursor: ""}); code(err) != "not_connected" {
			t.Fatalf("%v", err)
		}
	})
}

func TestParseRepoURL(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		h := newHarness(t, st)
		host := strings.TrimPrefix(h.f.URL, "http://")
		for _, c := range []struct {
			in, owner, name string
			ok              bool
		}{
			{h.f.URL + "/kazuhahub/notes.git", "kazuhahub", "notes", true},
			{h.f.URL + "/kazuhahub/notes", "kazuhahub", "notes", true},
			{h.f.URL + "/kazuhahub/notes/", "kazuhahub", "notes", true},
			{"ssh://git@" + host + "/kazuhahub/notes.git", "kazuhahub", "notes", true},
			{"git@" + strings.Split(host, ":")[0] + ":kazuhahub/notes.git", "kazuhahub", "notes", true},
			{"https://example.com/a/b.git", "", "", false},
			{h.f.URL + "/a/b/c.git", "", "", false},
			{h.f.URL + "/a", "", "", false},
			{h.f.URL + "/a/..", "", "", false},
			{"http://token@" + host + "/a/b.git", "", "", false},
			{h.f.URL + "/a/b.git?x=1", "", "", false},
			{"file:///tmp/x", "", "", false},
		} {
			o, n, ok := h.svc.ParseRepoURL(c.in)
			if ok != c.ok || o != c.owner || n != c.name {
				t.Errorf("%q: %q %q %v", c.in, o, n, ok)
			}
		}
	})
}

func TestGitTokenOfASpace(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		w := newWorld(t, st)
		repo := w.f.AddRepo("kazuhahub", "wiki", true)
		w.asOrg("kazuhahub")
		w.spaceIn("T1", "S1", "", store.Remote{Via: "app", InstallationID: w.kazuhahub, RepoID: repo.ID})
		w.space("S2", store.Remote{Via: "user", CredUserID: "U1", RepoID: repo.ID})
		if err := store.AddMember(ctx, st.Q(), "S2", "U1", "owner", w.now); err != nil {
			t.Fatal(err)
		}
		get := func(id string) (string, *gitstore.CredentialError) {
			t.Helper()
			r, err := store.GetRemote(ctx, st.Q(), id, store.RoleContent)
			if err != nil {
				t.Fatal(err)
			}
			tok, err := w.svc.GitToken(ctx, r, false)
			var ce *gitstore.CredentialError
			if err != nil && !errors.As(err, &ce) {
				t.Fatalf("not a credential error: %v", err)
			}
			return tok, ce
		}
		// An installation token narrowed to the repository, kept until five minutes before its end.
		tok, ce := get("S1")
		if ce != nil || !strings.HasPrefix(tok, "ghs_") {
			t.Fatalf("%q %v", tok, ce)
		}
		if again, _ := get("S1"); again != tok || w.f.Minted() != 1 {
			t.Fatalf("a second command minted again (%d)", w.f.Minted())
		}
		w.clk.Advance(56 * time.Minute)
		if renewed, _ := get("S1"); renewed == tok || w.f.Minted() != 2 {
			t.Fatalf("the token was not replaced near its end (%d)", w.f.Minted())
		}
		// The user's token.
		utok, ce := get("S2")
		if ce != nil || utok != w.f.AccessTokenOf("tang") {
			t.Fatalf("%q %v", utok, ce)
		}
		// She lost manage, or was disabled: the connection stops syncing the space.
		if err := store.SetMemberRole(ctx, st.Q(), "S2", "U1", "reader"); err != nil {
			t.Fatal(err)
		}
		if _, ce := get("S2"); ce == nil || ce.Code != "connection_missing" {
			t.Fatalf("%v", ce)
		}
		store.SetMemberRole(ctx, st.Q(), "S2", "U1", "owner")
		store.SetDisabled(ctx, st.Q(), "U1", true)
		if _, ce := get("S2"); ce == nil || ce.Code != "connection_missing" {
			t.Fatalf("%v", ce)
		}
		store.SetDisabled(ctx, st.Q(), "U1", false)
		// Expired: the code says so.
		w.f.ExpireRefresh("tang")
		w.clk.Advance(9 * time.Hour)
		if _, ce := get("S2"); ce == nil || ce.Code != "connection_expired" || ce.Offline {
			t.Fatalf("%v", ce)
		}
		// Disconnected: missing.
		if err := w.svc.Disconnect(ctx, "U1"); err != nil {
			t.Fatal(err)
		}
		if _, ce := get("S2"); ce == nil || ce.Code != "connection_missing" {
			t.Fatalf("%v", ce)
		}
		// Uninstalled, repository taken out, or suspended.
		w.f.SetInstall(w.kazuhahub, func(i *githubapptest.Install) { i.Deleted = true })
		w.clk.Advance(2 * time.Hour)
		if _, ce := get("S1"); ce == nil || ce.Code != "installation_gone" {
			t.Fatalf("%v", ce)
		}
		// GitHub that cannot be asked is not the credential's fault.
		w.f.SetInstall(w.kazuhahub, func(i *githubapptest.Install) { i.Deleted = false })
		w.clk.Advance(2 * time.Hour)
		w.f.LimitUntil(w.clk.Now().Add(time.Minute))
		if _, ce := get("S1"); ce == nil || !ce.Offline || ce.Code != "" {
			t.Fatalf("%v", ce)
		}
		w.f.LimitUntil(time.Time{})
		// GitHub no longer accepts the App's key.
		w.f.Key = githubapptest.NewKey(t)
		w.clk.Advance(2 * time.Hour)
		if _, ce := get("S1"); ce == nil || ce.Code != "provider_not_configured" {
			t.Fatalf("%v", ce)
		}
		store.DeleteRemote(ctx, st.Q(), "S2", store.RoleContent)
		store.DeleteRemote(ctx, st.Q(), "S1", store.RoleContent)
		w.mustNotLeak()
	})
}

func TestLeftoversAreListedWithInfoAndDismissedOneByOne(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		w := newWorld(t, st)
		w.user("U2", "other")
		repo := &connections.Repo{ID: 77, Owner: "kazuhahub", Name: "left", FullName: "kazuhahub/left", HTMLURL: "https://github.com/kazuhahub/left"}
		if info, err := w.svc.Info(ctx, "U1"); err != nil || len(info.Leftovers) != 0 {
			t.Fatalf("%+v %v", info, err)
		}
		if err := w.svc.RecordLeftover(ctx, "U1", repo, "Notes"); err != nil {
			t.Fatal(err)
		}
		info, err := w.svc.Info(ctx, "U1")
		if err != nil || len(info.Leftovers) != 1 || info.Leftovers[0].RepoID != 77 || info.Leftovers[0].FullName != "kazuhahub/left" || info.Leftovers[0].SpaceName != "Notes" {
			t.Fatalf("%+v %v", info, err)
		}
		// A parent context that has run out (a slow create) does not lose the reminder.
		dead, cancel := context.WithCancel(ctx)
		cancel()
		if err := w.svc.RecordLeftover(dead, "U1", &connections.Repo{ID: 78, FullName: "kazuhahub/late", HTMLURL: "https://github.com/kazuhahub/late"}, "Late"); err != nil {
			t.Fatalf("a reminder was lost with its context: %v", err)
		}
		if info, err := w.svc.Info(ctx, "U1"); err != nil || len(info.Leftovers) != 2 {
			t.Fatalf("%+v %v", info, err)
		}
		if err := w.svc.DismissLeftover(ctx, "U1", 78); err != nil {
			t.Fatal(err)
		}
		// Per user.
		if info, _ := w.svc.Info(ctx, "U2"); len(info.Leftovers) != 0 {
			t.Fatalf("%+v", info.Leftovers)
		}
		if err := w.svc.DismissLeftover(ctx, "U2", 77); err != nil {
			t.Fatal(err)
		}
		if info, _ := w.svc.Info(ctx, "U1"); len(info.Leftovers) != 1 {
			t.Fatal("another user's dismissal took it")
		}
		if err := w.svc.DismissLeftover(ctx, "U1", 77); err != nil {
			t.Fatal(err)
		}
		if err := w.svc.DismissLeftover(ctx, "U1", 77); err != nil {
			t.Fatalf("dismissing twice is fine: %v", err)
		}
		if info, _ := w.svc.Info(ctx, "U1"); len(info.Leftovers) != 0 {
			t.Fatalf("%+v", info.Leftovers)
		}
	})
}

// An App or an installation without a permission is not the same as a
// repository the installation does not reach: the person must be told which, and
// which permission is missing.
func TestMissingPermissionIsToldApartFromANotReachedRepository(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		w := newWorld(t, st)
		missing := func(err error) []string {
			var he *connections.Error
			if errors.As(err, &he) {
				return he.Missing
			}
			return nil
		}
		w.asOrg("kazuhahub")
		r, p, err := create(t, w, "M1", "kazuhahub", "perm")
		if err != nil {
			t.Fatal(err)
		}
		// The App has no Contents permission: GitHub answers 422 about permissions.
		w.f.AppPerms = map[string]string{"members": "read", "administration": "write", "metadata": "read"}
		_, err = w.svc.ChooseCredential(ctx, p, r)
		if code(err) != "app_missing_permission" || status(err) != 403 || strings.Join(missing(err), ",") != "contents" {
			t.Fatalf("422 about permissions: %v (%s) %v", err, code(err), missing(err))
		}
		// The same with GitHub's other wording, a 403.
		w.clk.Advance(2 * time.Minute)
		w.f.PermissionRefusal403 = true
		_, err = w.svc.ChooseCredential(ctx, p, r)
		if code(err) != "app_missing_permission" || strings.Join(missing(err), ",") != "contents" {
			t.Fatalf("403: %v (%s)", err, code(err))
		}
		w.f.PermissionRefusal403 = false
		// An App that has it, installed where it has not been accepted yet: the installation lacks it.
		w.f.AppPerms = map[string]string{"members": "read", "administration": "write", "contents": "write", "metadata": "read"}
		w.f.SetInstall(w.kazuhahub, func(i *githubapptest.Install) {
			i.Perms = map[string]string{"members": "read", "administration": "write", "metadata": "read"}
		})
		if _, err = w.svc.ChooseCredential(ctx, p, r); code(err) != "app_missing_permission" || strings.Join(missing(err), ",") != "contents" {
			t.Fatalf("installation: %v (%s)", err, code(err))
		}
		// Connecting an existing repository says the same.
		w.f.AddRepo("kazuhahub", "wiki", true)
		if _, _, err = w.svc.ResolveExistingIn(ctx, "T1", "M1", "kazuhahub", "wiki", "app"); code(err) != "app_missing_permission" {
			t.Fatalf("existing: %v (%s)", err, code(err))
		}
		w.f.SetInstall(w.kazuhahub, func(i *githubapptest.Install) { i.Perms = nil })
		// A repository outside a selection is still the other case.
		w.f.AddOrg("sel")
		w.f.Install("sel", "selected")
		w.asOrg("sel")
		r2, p2, _ := create(t, w, "M1", "sel", "left-out")
		if _, err = w.svc.ChooseCredential(ctx, p2, r2); code(err) != "repo_not_in_installation" {
			t.Fatalf("selection: %v (%s)", err, code(err))
		}
		// A space's own credential: the remote shows the code.
		repo := w.f.AddRepo("kazuhahub", "docs", true)
		w.spaceIn("T1", "S1", "", store.Remote{Via: "app", InstallationID: w.kazuhahub, RepoID: repo.ID})
		w.f.AppPerms = map[string]string{"members": "read", "administration": "write", "metadata": "read"}
		rem, _ := store.GetRemote(ctx, st.Q(), "S1", store.RoleContent)
		_, err = w.svc.GitToken(ctx, rem, false)
		var ce *gitstore.CredentialError
		if !errors.As(err, &ce) || ce.Code != "app_missing_permission" {
			t.Fatalf("git token: %v", err)
		}
		// Creating a repository without the Administration permission names it.
		w.f.AppPerms = map[string]string{"members": "read", "contents": "write", "metadata": "read"}
		if _, _, err = create(t, w, "M1", "kazuhahub", "wiki3"); code(err) != "app_missing_permission" || strings.Join(missing(err), ",") != "administration" {
			t.Fatalf("create: %v (%s) %v", err, code(err), missing(err))
		}
	})
}

// What GitHub refuses is in the server's log with GitHub's own words, and no
// secret is.
func TestGitHubsRefusalsAreInTheLogWithoutASecret(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		w := newWorld(t, st)
		w.f.SetAccount("kazuhahub", func(a *githubapptest.Account) { a.NoCreate = true })
		if _, _, err := create(t, w, "U1", "kazuhahub", "nope"); code(err) != "provider_refused" {
			t.Fatalf("%v", err)
		}
		w.f.AppPerms = map[string]string{"members": "read", "administration": "write", "metadata": "read"}
		w.f.AddOrg("petal-team")
		w.f.Install("petal-team", "all")
		w.asOrg("petal-team")
		r, _, _ := create(t, w, "M1", "petal-team", "wiki")
		p, _ := w.svc.PlanCreate(ctx, "M1", "petal-team")
		_, _ = w.svc.ChooseCredential(ctx, p, r)
		logs := w.logs.String()
		for _, want := range []string{"GitHub refused a request", "kind=create_org_repo", "status=403", "permission to create repositories", "kind=installation_token", "status=422", "not granted to this installation"} {
			if !strings.Contains(logs, want) {
				t.Errorf("the log lacks %q:\n%s", want, logs)
			}
		}
		w.mustNotLeak()
	})
}
