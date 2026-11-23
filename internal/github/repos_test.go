package github_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/github"
	"github.com/KKazuhaK/PetalBook/internal/githubapp/githubapptest"
	"github.com/KKazuhaK/PetalBook/internal/gitstore"
	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
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

func owners(t *testing.T, h *harness, user string) (all []github.Owner, pages int) {
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
		peti := w.f.Install("petal-team", "all")
		w.f.AddOrg("other")
		w.f.Install("other", "all")
		if _, err := w.svc.SetTenants(ctx, peti, []string{store.DefaultTenantID}); err != nil {
			t.Fatal(err)
		}
		// Sharing kazuhahub too must not list it twice.
		if _, err := w.svc.SetTenants(ctx, w.kazuhahub, []string{store.DefaultTenantID}); err != nil {
			t.Fatal(err)
		}
		got, _ := owners(t, w.harness, "U1")
		var s []string
		for _, o := range got {
			s = append(s, fmt.Sprintf("%s/%s/%s/%v", o.Login, o.Type, o.Via, o.InstallationID > 0))
		}
		want := "tang/User/user/false kazuhahub/Organization/user/true petal-team/Organization/app/true"
		if strings.Join(s, " ") != want {
			t.Fatalf("%v", s)
		}
		// A caller without a connection sees the shared ones only.
		w.user("U2", "nobody")
		got, _ = owners(t, w.harness, "U2")
		if len(got) != 2 || got[0].Via != "app" || got[1].Via != "app" {
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

func TestSharedOwnersPageFromTheDatabase(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		w := newWorld(t, st)
		w.user("U2", "nobody")
		for i := 0; i < 35; i++ {
			login := fmt.Sprintf("shared-%02d", i)
			w.f.AddOrg(login)
			if _, err := w.svc.SetTenants(ctx, w.f.Install(login, "all"), []string{store.DefaultTenantID}); err != nil {
				t.Fatal(err)
			}
		}
		before := len(w.f.Requests())
		got, pages := owners(t, w.harness, "U2")
		if len(got) != 35 || pages != 2 {
			t.Fatalf("%d in %d pages", len(got), pages)
		}
		if n := len(w.f.Requests()) - before; n != 0 {
			t.Fatalf("%d requests to GitHub for a list kept in the database", n)
		}
	})
}

func TestPlanCreate(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		w := newWorld(t, st)
		w.f.AddOrg("petal-team")
		peti := w.f.Install("petal-team", "all")
		w.f.AddOrg("other")
		w.f.Install("other", "all")
		w.svc.SetTenants(ctx, peti, []string{store.DefaultTenantID})

		p, err := w.svc.PlanCreate(ctx, "U1", "TANG")
		if err != nil || p.Via != "user" || p.Org != "" || p.Owner != "tang" {
			t.Fatalf("%+v %v", p, err)
		}
		p, err = w.svc.PlanCreate(ctx, "U1", "kazuhahub")
		if err != nil || p.Via != "user" || p.Org != "kazuhahub" || p.InstallationID != w.kazuhahub {
			t.Fatalf("%+v %v", p, err)
		}
		p, err = w.svc.PlanCreate(ctx, "U1", "Petal-Team")
		if err != nil || p.Via != "app" || p.Org != "petal-team" || p.InstallationID != peti {
			t.Fatalf("%+v %v", p, err)
		}
		for _, owner := range []string{"other", "someone-else"} {
			if _, err := w.svc.PlanCreate(ctx, "U1", owner); code(err) != "github_owner_not_allowed" || status(err) != 403 {
				t.Fatalf("%s: %v", owner, err)
			}
		}
		if _, err := w.svc.PlanCreate(ctx, "U1", "a/b"); code(err) != "invalid" {
			t.Fatalf("%v", err)
		}
		// An expired connection cannot create in its own account.
		w.f.ExpireRefresh("tang")
		w.clk.Advance(9 * time.Hour)
		w.svc.UserToken(ctx, "U1")
		if _, err := w.svc.PlanCreate(ctx, "U1", "tang"); code(err) != "github_not_authorized" || status(err) != 409 {
			t.Fatalf("%v", err)
		}
		// ...but a shared organisation still works without it.
		if p, err := w.svc.PlanCreate(ctx, "U1", "petal-team"); err != nil || p.Via != "app" {
			t.Fatalf("%+v %v", p, err)
		}
	})
}

func create(t *testing.T, w *world, user, owner, name string) (*github.Repo, *github.Plan, error) {
	t.Helper()
	p, err := w.svc.PlanCreate(ctx, user, owner)
	if err != nil {
		return nil, nil, err
	}
	r, err := w.svc.CreateRepo(ctx, p, github.NewRepoSpec{Name: name, Private: true, Description: "d"})
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
		if _, _, err := create(t, w, "U1", "tang", "notes"); code(err) != "github_name_taken" || status(err) != 409 {
			t.Fatalf("%v", err)
		}
		// An organisation that does not let members create repositories: GitHub's own reason.
		w.f.SetAccount("kazuhahub", func(a *githubapptest.Account) { a.NoCreate = true })
		_, _, err = create(t, w, "U1", "kazuhahub", "another")
		if code(err) != "github_no_permission" || status(err) != 403 || !strings.Contains(err.Error(), "permission to create repositories") {
			t.Fatalf("%v", err)
		}
		w.f.SetAccount("kazuhahub", func(a *githubapptest.Account) { a.NoCreate, a.NoPrivate = false, true })
		_, _, err = create(t, w, "U1", "kazuhahub", "another")
		if code(err) != "github_no_permission" || !strings.Contains(err.Error(), "Private repositories") {
			t.Fatalf("%v", err)
		}
		// A refusal of the fields.
		p, _ := w.svc.PlanCreate(ctx, "U1", "tang")
		if _, err := w.svc.CreateRepo(ctx, p, github.NewRepoSpec{Name: "bad name"}); code(err) != "invalid" || status(err) != 400 {
			t.Fatalf("%v", err)
		}
		// GitHub limiting the server answers 429 with the wait.
		w.f.LimitUntil(w.clk.Now().Add(90 * time.Second))
		_, err = w.svc.CreateRepo(ctx, p, github.NewRepoSpec{Name: "limited"})
		var he *github.Error
		if !errors.As(err, &he) || he.Code != "rate_limited" || he.Status != 429 || he.RetryAfter < 89*time.Second {
			t.Fatalf("%v", err)
		}
	})
}

func TestCreateWithTheInstallationOfASharedOrganisation(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		w := newWorld(t, st)
		w.f.AddOrg("petal-team")
		peti := w.f.Install("petal-team", "all")
		w.svc.SetTenants(ctx, peti, []string{store.DefaultTenantID})
		w.user("U2", "nobody") // no GitHub connection at all
		r, p, err := create(t, w, "U2", "petal-team", "wiki")
		if err != nil || r.FullName != "petal-team/wiki" || p.Via != "app" {
			t.Fatalf("%+v %v", r, err)
		}
		// The administration token is made for this request alone.
		minted := w.f.Minted()
		if _, _, err := create(t, w, "U2", "petal-team", "wiki2"); err != nil || w.f.Minted() != minted+1 {
			t.Fatalf("%v %d", err, w.f.Minted()-minted)
		}
		// An App without the Administration permission: GitHub's refusal, passed on.
		w.f.AppPerms = map[string]string{"contents": "write", "metadata": "read"}
		if _, _, err := create(t, w, "U2", "petal-team", "wiki3"); code(err) != "github_no_permission" || status(err) != 403 {
			t.Fatalf("%v", err)
		}
	})
}

func TestChooseCredential(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		w := newWorld(t, st)
		// An organisation: the installation's token, narrowed to the new repository.
		r, p, _ := create(t, w, "U1", "kazuhahub", "a")
		c, err := w.svc.ChooseCredential(ctx, p, r)
		if err != nil || c.Via != "app" || c.InstallationID != w.kazuhahub || c.RepoID != r.ID {
			t.Fatalf("%+v %v", c, err)
		}
		// The caller's own account: the App is not installed there, so the caller's token.
		r, p, _ = create(t, w, "U1", "tang", "a")
		if c, err = w.svc.ChooseCredential(ctx, p, r); err != nil || c.Via != "user" || c.UserID != "U1" || c.RepoID != r.ID {
			t.Fatalf("%+v %v", c, err)
		}
		// Installed on the account: the installation's.
		tangInst := w.f.Install("tang", "all")
		r, p, _ = create(t, w, "U1", "tang", "b")
		if c, err = w.svc.ChooseCredential(ctx, p, r); err != nil || c.Via != "app" || c.InstallationID != tangInst {
			t.Fatalf("%+v %v", c, err)
		}
	})
}

func TestSelectedRepositoriesInstallations(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		w := newWorld(t, st)
		w.f.AddOrg("sel", "tang")
		sel := w.f.Install("sel", "selected")
		// GitHub does not add the new repository by itself: the user's token adds it.
		r, p, err := create(t, w, "U1", "sel", "notes")
		if err != nil {
			t.Fatal(err)
		}
		c, err := w.svc.ChooseCredential(ctx, p, r)
		if err != nil || c.Via != "app" || c.InstallationID != sel {
			t.Fatalf("%+v %v", c, err)
		}
		if w.f.Count("PUT", "/user/installations/") != 1 {
			t.Fatal("the repository was not added to the installation")
		}
		// When GitHub adds it by itself nothing is asked.
		w.f.AutoAdd = true
		r, p, _ = create(t, w, "U1", "sel", "auto")
		if _, err := w.svc.ChooseCredential(ctx, p, r); err != nil || w.f.Count("PUT", "/user/installations/") != 1 {
			t.Fatalf("%v", err)
		}
		w.f.AutoAdd = false
		// The user may not add: an organisation answers 409 and names the repository.
		w.f.SetInstall(sel, func(i *githubapptest.Install) { i.NoAdd = true })
		r, p, _ = create(t, w, "U1", "sel", "stuck")
		_, err = w.svc.ChooseCredential(ctx, p, r)
		var he *github.Error
		if !errors.As(err, &he) || he.Code != "github_repo_not_in_installation" || he.Status != 409 || he.Repo == nil || he.Repo.FullName != "sel/stuck" {
			t.Fatalf("%v", err)
		}
		// ...while a personal account falls back to the user's own token.
		tangInst := w.f.Install("tang", "selected")
		w.f.SetInstall(tangInst, func(i *githubapptest.Install) { i.NoAdd = true })
		r, p, _ = create(t, w, "U1", "tang", "mine")
		if c, err = w.svc.ChooseCredential(ctx, p, r); err != nil || c.Via != "user" {
			t.Fatalf("%+v %v", c, err)
		}
		// With the permission it is added and the installation's token is used.
		w.f.SetInstall(tangInst, func(i *githubapptest.Install) { i.NoAdd = false })
		r, p, _ = create(t, w, "U1", "tang", "mine2")
		if c, err = w.svc.ChooseCredential(ctx, p, r); err != nil || c.Via != "app" || c.InstallationID != tangInst {
			t.Fatalf("%+v %v", c, err)
		}
	})
}

func TestExistingRepositories(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		w := newWorld(t, st)
		w.f.AddRepo("kazuhahub", "wiki", true)
		w.f.AddOrg("petal-team")
		peti := w.f.Install("petal-team", "all")
		w.f.AddRepo("petal-team", "docs", true)
		w.f.AddRepo("tang", "mine", true)
		w.f.AddOrg("none")
		w.f.AddRepo("none", "x", true)

		// The caller can push: via the App (the installation reaches it).
		c, r, err := w.svc.ResolveExisting(ctx, "U1", "kazuhahub", "wiki", "app")
		if err != nil || c.Via != "app" || c.InstallationID != w.kazuhahub || r.FullName != "kazuhahub/wiki" || c.RepoID != r.ID {
			t.Fatalf("%+v %v", c, err)
		}
		// Without a choice the App is used when the caller may.
		if c, _, err = w.svc.ResolveExisting(ctx, "U1", "kazuhahub", "wiki", ""); err != nil || c.Via != "app" {
			t.Fatalf("%+v %v", c, err)
		}
		// Not the caller's, not shared: refused; shared: allowed without a connection.
		if _, _, err := w.svc.ResolveExisting(ctx, "U1", "petal-team", "docs", "app"); code(err) != "github_owner_not_allowed" || status(err) != 403 {
			t.Fatalf("%v", err)
		}
		w.user("U2", "nobody")
		if _, _, err := w.svc.ResolveExisting(ctx, "U2", "petal-team", "docs", "app"); code(err) != "github_not_authorized" || status(err) != 409 {
			t.Fatalf("%v", err)
		}
		w.svc.SetTenants(ctx, peti, []string{store.DefaultTenantID})
		if c, _, err := w.svc.ResolveExisting(ctx, "U2", "petal-team", "docs", "app"); err != nil || c.Via != "app" {
			t.Fatalf("%+v %v", c, err)
		}
		// The App is not installed there.
		if _, _, err := w.svc.ResolveExisting(ctx, "U1", "none", "x", "app"); code(err) != "github_repo_not_in_installation" || status(err) != 409 {
			t.Fatalf("%v", err)
		}
		// Via the user: needs the connection and push access.
		if c, _, err := w.svc.ResolveExisting(ctx, "U1", "tang", "mine", "user"); err != nil || c.Via != "user" || c.UserID != "U1" {
			t.Fatalf("%+v %v", c, err)
		}
		if _, _, err := w.svc.ResolveExisting(ctx, "U2", "tang", "mine", "user"); code(err) != "github_not_authorized" {
			t.Fatalf("%v", err)
		}
		if _, _, err := w.svc.ResolveExisting(ctx, "U1", "none", "x", "user"); code(err) != "github_owner_not_allowed" {
			t.Fatalf("%v", err)
		}
		// With no preference, a repository the App does not reach falls back to the user.
		if c, _, err := w.svc.ResolveExisting(ctx, "U1", "tang", "mine", ""); err != nil || c.Via != "user" {
			t.Fatalf("%+v %v", c, err)
		}
		// An installation of selected repositories that does not include it.
		w.f.AddOrg("sel", "tang")
		w.f.Install("sel", "selected")
		w.f.AddRepo("sel", "left-out", true)
		if _, _, err := w.svc.ResolveExisting(ctx, "U1", "sel", "left-out", "app"); code(err) != "github_repo_not_in_installation" {
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
		page := func(inst int64, mine bool, cur string) ([]github.RepoItem, string) {
			items, next, err := w.svc.Repos(ctx, "U1", inst, mine, cur)
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
		// An installation the caller can neither reach nor use as shared.
		w.f.AddOrg("other")
		other := w.f.Install("other", "all")
		if _, _, err := w.svc.Repos(ctx, "U1", other, false, ""); code(err) != "not_found" || status(err) != 404 {
			t.Fatalf("%v", err)
		}
		// Shared: every repository, through the installation's token.
		w.f.AddRepo("other", "pub", false)
		w.svc.SetTenants(ctx, other, []string{store.DefaultTenantID})
		items, _, err := w.svc.Repos(ctx, "U1", other, false, "")
		if err != nil || len(items) != 1 || items[0].FullName != "other/pub" || !items[0].CanPush {
			t.Fatalf("%+v %v", items, err)
		}
		// Mine needs the connection.
		w.user("U2", "nobody")
		if _, _, err := w.svc.Repos(ctx, "U2", 0, true, ""); code(err) != "github_not_authorized" {
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
		w.space("S1", store.Remote{GHVia: "app", GHInstallationID: w.kazuhahub, GHRepoID: repo.ID})
		w.space("S2", store.Remote{GHVia: "user", GHUserID: "U1", GHRepoID: repo.ID})
		if err := store.AddMember(ctx, st.Q(), "S2", "U1", "owner", w.now); err != nil {
			t.Fatal(err)
		}
		get := func(id string) (string, *gitstore.CredentialError) {
			t.Helper()
			r, err := store.GetRemote(ctx, st.Q(), id)
			if err != nil {
				t.Fatal(err)
			}
			tok, err := w.svc.GitToken(ctx, r)
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
		if _, ce := get("S2"); ce == nil || ce.Code != "github_authorization_missing" {
			t.Fatalf("%v", ce)
		}
		store.SetMemberRole(ctx, st.Q(), "S2", "U1", "owner")
		store.SetDisabled(ctx, st.Q(), "U1", true)
		if _, ce := get("S2"); ce == nil || ce.Code != "github_authorization_missing" {
			t.Fatalf("%v", ce)
		}
		store.SetDisabled(ctx, st.Q(), "U1", false)
		// Expired: the code says so.
		w.f.ExpireRefresh("tang")
		w.clk.Advance(9 * time.Hour)
		if _, ce := get("S2"); ce == nil || ce.Code != "github_authorization_expired" || ce.Offline {
			t.Fatalf("%v", ce)
		}
		// Disconnected: missing.
		if err := w.svc.Disconnect(ctx, "U1"); err != nil {
			t.Fatal(err)
		}
		if _, ce := get("S2"); ce == nil || ce.Code != "github_authorization_missing" {
			t.Fatalf("%v", ce)
		}
		// Uninstalled, repository taken out, or suspended.
		w.f.SetInstall(w.kazuhahub, func(i *githubapptest.Install) { i.Deleted = true })
		w.clk.Advance(2 * time.Hour)
		if _, ce := get("S1"); ce == nil || ce.Code != "github_installation_gone" {
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
		if _, ce := get("S1"); ce == nil || ce.Code != "github_not_configured" {
			t.Fatalf("%v", ce)
		}
		store.DeleteRemote(ctx, st.Q(), "S2")
		store.DeleteRemote(ctx, st.Q(), "S1")
		w.mustNotLeak()
	})
}

// An installation is shared per tenant: only the people of the tenants it was
// shared with can list it, create in it and connect to its repositories.
func TestSharingIsPerTenant(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		w := newWorld(t, st)
		w.f.AddOrg("petal-team")
		peti := w.f.Install("petal-team", "all")
		w.f.AddRepo("petal-team", "docs", true)
		w.tenantUser("T2", "U2", "two")   // in a tenant it is shared with
		w.tenantUser("T3", "U3", "three") // in a tenant it is not
		if _, err := w.svc.SetTenants(ctx, peti, []string{"T2"}); err != nil {
			t.Fatal(err)
		}
		names := func(user string) string {
			var s []string
			got, _ := owners(t, w.harness, user)
			for _, o := range got {
				s = append(s, o.Login)
			}
			return strings.Join(s, ",")
		}
		if got := names("U2"); got != "petal-team" {
			t.Fatalf("U2 sees %q", got)
		}
		if got := names("U3"); got != "" {
			t.Fatalf("U3 sees %q", got)
		}
		// U1 (default tenant) has its own connection, which does not reach petal-team.
		if got := names("U1"); strings.Contains(got, "petal-team") {
			t.Fatalf("U1 sees %q", got)
		}
		if p, err := w.svc.PlanCreate(ctx, "U2", "petal-team"); err != nil || p.Via != "app" || p.InstallationID != peti {
			t.Fatalf("%+v %v", p, err)
		}
		for _, u := range []string{"U1", "U3"} {
			if _, err := w.svc.PlanCreate(ctx, u, "petal-team"); code(err) != "github_owner_not_allowed" {
				t.Fatalf("%s: %v", u, err)
			}
			if _, _, err := w.svc.ResolveExisting(ctx, u, "petal-team", "docs", "app"); err == nil {
				t.Fatalf("%s connected to an installation not shared with their tenant", u)
			}
			if _, _, err := w.svc.Repos(ctx, u, peti, false, ""); code(err) != "not_found" {
				t.Fatalf("%s: %v", u, err)
			}
		}
		if c, _, err := w.svc.ResolveExisting(ctx, "U2", "petal-team", "docs", "app"); err != nil || c.Via != "app" {
			t.Fatalf("%+v %v", c, err)
		}
		if items, _, err := w.svc.Repos(ctx, "U2", peti, false, ""); err != nil || len(items) != 1 {
			t.Fatalf("%+v %v", items, err)
		}
		// The share is checked against the tenant of the space, not the caller's.
		if c, _, err := w.svc.ResolveExistingIn(ctx, "T2", "U1", "petal-team", "docs", "app"); err != nil || c.Via != "app" {
			t.Fatalf("a caller of another tenant, for a space of T2: %+v %v", c, err)
		}
		if _, _, err := w.svc.ResolveExistingIn(ctx, "T3", "U2", "petal-team", "docs", "app"); err == nil {
			t.Fatal("a caller of T2 used the share for a space of T3")
		}
		// Un-sharing takes it away again; a deleted tenant takes its share.
		if _, err := w.svc.SetTenants(ctx, peti, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := w.svc.PlanCreate(ctx, "U2", "petal-team"); code(err) != "github_owner_not_allowed" {
			t.Fatalf("%v", err)
		}
	})
}

func TestLeftoversAreListedWithInfoAndDismissedOneByOne(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		w := newWorld(t, st)
		w.user("U2", "other")
		repo := &github.Repo{ID: 77, Owner: "kazuhahub", Name: "left", FullName: "kazuhahub/left", HTMLURL: "https://github.com/kazuhahub/left"}
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
		if err := w.svc.RecordLeftover(dead, "U1", &github.Repo{ID: 78, FullName: "kazuhahub/late", HTMLURL: "https://github.com/kazuhahub/late"}, "Late"); err != nil {
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
