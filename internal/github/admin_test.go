package github_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/github"
	"github.com/KKazuhaK/PetalBook/internal/githubapp/githubapptest"
	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

func TestSaveChecksWithGitHubAndStoresSealed(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		h := newHarness(t, st)
		if v, err := h.svc.View(ctx); err != nil || v.Configured || v.CallbackURL != publicURL+"/api/connections/github/authorization/callback" {
			t.Fatalf("%+v %v", v, err)
		}
		v := h.configure()
		if !v.Configured || v.AppID != "424242" || v.Slug != "petalbook-notes" || !v.KeySet || !v.SecretSet || !v.UserAuthorizaton ||
			!strings.HasPrefix(v.Fingerprint, "SHA256:") || len(v.Missing) != 0 || v.InstallURL != "https://github.com/apps/petalbook-notes/installations/new" {
			t.Fatalf("%+v", v)
		}
		// The key and the secret are in the database only sealed.
		raw, ok, err := store.GetSiteSetting(ctx, st.Q(), "github_app")
		if err != nil || !ok {
			t.Fatal(err)
		}
		for _, plain := range []string{h.f.ClientSecret, "BEGIN RSA PRIVATE KEY", "MIIE"} {
			if strings.Contains(raw, plain) {
				t.Fatalf("the stored settings hold %q", plain)
			}
		}
		// A key or secret left out keeps the stored one; the same App may change its key.
		if _, err := h.svc.Save(ctx, github.SaveRequest{}); err != nil {
			t.Fatalf("a save without changes: %v", err)
		}
		h.mustNotLeak()
	})
}

func TestSaveRefusesWhatGitHubRefusesAndSavesNothing(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		h := newHarness(t, st)
		id, bad := h.f.ClientID, "Iv23other"
		cases := []struct {
			name   string
			req    github.SaveRequest
			code   string
			status int
		}{
			{"nothing", github.SaveRequest{}, "invalid", 400},
			{"no key", github.SaveRequest{AppID: "424242"}, "invalid", 400},
			{"not a key", github.SaveRequest{AppID: "424242", PrivateKey: "hello"}, "key_invalid", 400},
			{"another key", github.SaveRequest{AppID: "424242", PrivateKey: githubapptest.KeyPEM(githubapptest.NewKey(t))}, "app_rejected", 400},
			{"another app id", github.SaveRequest{AppID: "7", PrivateKey: h.f.KeyPEM()}, "app_rejected", 400},
			{"another client id", github.SaveRequest{AppID: "424242", PrivateKey: h.f.KeyPEM(), ClientID: &bad, ClientSecret: "x"}, "client_rejected", 400},
			{"wrong secret", github.SaveRequest{AppID: "424242", PrivateKey: h.f.KeyPEM(), ClientID: &id, ClientSecret: "wrong"}, "client_rejected", 400},
			{"id without secret", github.SaveRequest{AppID: "424242", PrivateKey: h.f.KeyPEM(), ClientID: &id}, "invalid", 400},
		}
		for _, c := range cases {
			_, err := h.svc.Save(ctx, c.req)
			if code(err) != c.code || status(err) != c.status {
				t.Errorf("%s: %v (%s)", c.name, err, code(err))
			}
		}
		if _, ok, _ := store.GetSiteSetting(ctx, st.Q(), "github_app"); ok {
			t.Fatal("a refused save stored something")
		}
		// GitHub that cannot be reached: 503, nothing saved.
		h.f.Server.Close()
		if _, err := h.svc.Save(ctx, github.SaveRequest{AppID: "424242", PrivateKey: h.f.KeyPEM()}); code(err) != "unavailable" || status(err) != 503 {
			t.Fatalf("%v", err)
		}
		if _, ok, _ := store.GetSiteSetting(ctx, st.Q(), "github_app"); ok {
			t.Fatal("an unreachable GitHub left settings")
		}
	})
}

func TestMissingPermissionsAreOnlyAWarning(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		h := newHarness(t, st)
		h.f.AppPerms = map[string]string{"contents": "read", "metadata": "read"}
		v, err := h.svc.Save(ctx, github.SaveRequest{AppID: "424242", PrivateKey: h.f.KeyPEM()})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(v.Missing, ",") != "administration,contents" || v.UserAuthorizaton {
			t.Fatalf("%+v", v)
		}
	})
}

func TestAnotherAppIsRefusedWhileInUse(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		h := newHarness(t, st)
		h.configure()
		h.user("U1", "kazuha")
		h.f.AddUser("kazuha-gh")
		// Nothing uses it yet: another App could be saved; a new key for the same one is always fine.
		h.connect("U1", "kazuha-gh")
		if _, err := h.svc.Save(ctx, github.SaveRequest{PrivateKey: githubapptestKeyPEM(h)}); err != nil {
			t.Fatalf("the same App with its key again: %v", err)
		}
		empty := ""
		_, err := h.svc.Save(ctx, github.SaveRequest{ClientID: &empty})
		if code(err) != "provider_in_use" || status(err) != http.StatusConflict {
			t.Fatalf("removing the client id while a user is connected: %v", err)
		}
		if _, err := h.svc.Save(ctx, github.SaveRequest{AppID: "999", PrivateKey: githubapptestKeyPEM(h)}); code(err) != "provider_in_use" {
			t.Fatalf("another App id: %v", err)
		}
		// Deleting is refused while a space syncs through the App; connections go with it otherwise.
		h.space("S1", store.Remote{GHVia: "user", GHUserID: "U1", GHRepoID: 5})
		if err := h.svc.Delete(ctx); code(err) != "provider_in_use" {
			t.Fatalf("%v", err)
		}
		if err := store.DeleteRemote(ctx, st.Q(), "S1"); err != nil {
			t.Fatal(err)
		}
		if err := h.svc.Delete(ctx); err != nil {
			t.Fatal(err)
		}
		if n, _ := store.CountGitHubAuthorizations(ctx, st.Q()); n != 0 {
			t.Fatal("connections survived the App")
		}
		if h.svc.Configured(ctx) {
			t.Fatal("still configured")
		}
		// GitHub was asked to revoke the grant first.
		if h.f.Count("DELETE", "/applications/") != 1 {
			t.Fatalf("%d revocations", h.f.Count("DELETE", "/applications/"))
		}
	})
}

func githubapptestKeyPEM(h *harness) string { return h.f.KeyPEM() }

func TestInstallationsListAndSharing(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		h := newHarness(t, st)
		if _, _, err := h.svc.Installations(ctx, ""); code(err) != "provider_not_configured" || status(err) != 409 {
			t.Fatalf("%v", err)
		}
		h.configure()
		h.f.AddUser("tang")
		var orgInst int64
		for i := 0; i < 40; i++ {
			login := "org" + strings.Repeat("a", i+1)
			h.f.AddOrg(login)
			id := h.f.Install(login, "all")
			if i == 35 {
				orgInst = id
			}
		}
		userInst := h.f.Install("tang", "all")
		p1, next, err := h.svc.Installations(ctx, "")
		if err != nil || len(p1) != 30 || next == "" {
			t.Fatalf("%d %q %v", len(p1), next, err)
		}
		before := h.f.Count("GET", "/app/installations")
		p2, next2, err := h.svc.Installations(ctx, next)
		if err != nil || len(p2) != 11 || next2 != "" || h.f.Count("GET", "/app/installations") != before+1 {
			t.Fatalf("%d %q %v", len(p2), next2, err)
		}
		if _, _, err := h.svc.Installations(ctx, "garbage"); code(err) != "invalid" {
			t.Fatalf("%v", err)
		}
		// Share one organisation; a user's account cannot be shared; GitHub must know the installation.
		it, err := h.svc.SetTenants(ctx, orgInst, []string{store.DefaultTenantID})
		if err != nil || fmt.Sprint(it.Tenants) != "["+store.DefaultTenantID+"]" {
			t.Fatalf("%+v %v", it, err)
		}
		if _, err := h.svc.SetTenants(ctx, userInst, []string{store.DefaultTenantID}); code(err) != "invalid" {
			t.Fatalf("%v", err)
		}
		if _, err := h.svc.SetTenants(ctx, 12345, []string{store.DefaultTenantID}); code(err) != "not_found" || status(err) != 404 {
			t.Fatalf("%v", err)
		}
		list, _, _ := h.svc.Installations(ctx, next)
		shared := 0
		all, _, _ := h.svc.Installations(ctx, "")
		for _, x := range append(all, list...) {
			if len(x.Tenants) > 0 {
				shared++
			}
		}
		if shared != 1 {
			t.Fatalf("%d shared", shared)
		}
		// Sharing with a tenant that does not exist, or twice with one, or with none.
		if _, err := h.svc.SetTenants(ctx, orgInst, []string{"no-such-tenant"}); code(err) != "invalid" || status(err) != 400 {
			t.Fatalf("%v", err)
		}
		if err := st.CreateTenant(ctx, store.Tenant{ID: "T2", Slug: "two", Name: "Two", Created: h.now}); err != nil {
			t.Fatal(err)
		}
		it, err = h.svc.SetTenants(ctx, orgInst, []string{"T2", store.DefaultTenantID, "T2"})
		if err != nil || fmt.Sprint(it.Tenants) != "["+store.DefaultTenantID+" T2]" {
			t.Fatalf("%+v %v", it, err)
		}
		if _, err := h.svc.SetTenants(ctx, userInst, nil); err != nil {
			t.Fatalf("stopping to share a user's account is allowed: %v", err)
		}
		// A space counts toward its installation.
		h.user("U1", "kazuha")
		h.space("S1", store.Remote{GHVia: "app", GHInstallationID: orgInst, GHRepoID: 1})
		it, err = h.svc.SetTenants(ctx, orgInst, nil)
		if err != nil || len(it.Tenants) != 0 || it.Spaces != 1 {
			t.Fatalf("%+v %v", it, err)
		}
	})
}

// Removing the client id only ends users' connections: spaces that sync
// through the App's installations do not need it, so only connected users
// stand in the way.
func TestRemovingTheClientIDCountsOnlyConnectedUsers(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		h := newHarness(t, st)
		h.configure()
		h.user("U1", "kazuha")
		h.space("S1", store.Remote{GHVia: "app", GHInstallationID: 7, GHRepoID: 5})
		empty := ""
		v, err := h.svc.Save(ctx, github.SaveRequest{ClientID: &empty})
		if err != nil {
			t.Fatalf("removing the client id while only App spaces exist: %v", err)
		}
		if v.ClientID != "" {
			t.Fatalf("%+v", v)
		}
		// Another App is still refused while the space syncs through this one.
		if _, err := h.svc.Save(ctx, github.SaveRequest{AppID: "999", PrivateKey: githubapptestKeyPEM(h)}); code(err) != "provider_in_use" {
			t.Fatalf("another App id: %v", err)
		}
	})
}

// reads counts the requests for the App itself (GET /app), which a re-check makes.
func appReads(h *harness) int {
	n := 0
	for _, r := range h.f.Requests() {
		if r.Method == "GET" && r.Path == "/app" {
			n++
		}
	}
	return n
}

func TestRecheckReadsThePermissionsAgain(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		h := newHarness(t, st)
		h.f.AppPerms = map[string]string{"contents": "write", "metadata": "read"}
		v, err := h.svc.Save(ctx, github.SaveRequest{AppID: "424242", PrivateKey: h.f.KeyPEM()})
		if err != nil || strings.Join(v.Missing, ",") != "administration" {
			t.Fatalf("%+v %v", v, err)
		}
		// The owner gives the App the permission on GitHub: the stored view is old until it is checked again.
		h.f.AppPerms = map[string]string{"administration": "write", "contents": "write", "metadata": "read"}
		if v, _ = h.svc.View(ctx); strings.Join(v.Missing, ",") != "administration" {
			t.Fatalf("a view read GitHub: %+v", v)
		}
		h.clk.Advance(time.Hour)
		v, err = h.svc.Recheck(ctx)
		if err != nil || len(v.Missing) != 0 || !v.Checked.Equal(h.clk.Now()) || v.Permissions["administration"] != "write" {
			t.Fatalf("%+v %v", v, err)
		}
		if v, _ = h.svc.View(ctx); len(v.Missing) != 0 {
			t.Fatalf("the re-check was not stored: %+v", v)
		}
		// An installation that has not accepted the new permission is told apart from the App.
		h.f.AddOrg("kazuhahub")
		inst := h.f.Install("kazuhahub", "all")
		h.f.SetInstall(inst, func(i *githubapptest.Install) { i.Perms = map[string]string{"contents": "read", "metadata": "read"} })
		v, err = h.svc.Recheck(ctx)
		if err != nil || len(v.Missing) != 0 || len(v.Gaps) != 1 || v.Gaps[0].ID != inst || v.Gaps[0].Account != "kazuhahub" ||
			strings.Join(v.Gaps[0].Missing, ",") != "administration,contents" {
			t.Fatalf("%+v %v", v, err)
		}
		h.f.SetInstall(inst, func(i *githubapptest.Install) { i.Perms = nil })
		if v, err = h.svc.Recheck(ctx); err != nil || len(v.Gaps) != 0 {
			t.Fatalf("%+v %v", v, err)
		}
		// A key GitHub no longer accepts is the App's problem, said so.
		h.f.Key = githubapptest.NewKey(t)
		if _, err = h.svc.Recheck(ctx); code(err) != "provider_not_configured" || status(err) != 409 {
			t.Fatalf("%v", err)
		}
		h.mustNotLeak()
	})
}

func TestRecheckWithoutAnApp(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		h := newHarness(t, st)
		if _, err := h.svc.Recheck(ctx); code(err) != "provider_not_configured" {
			t.Fatalf("%v", err)
		}
	})
}

// A token request refused for a permission looks at the App again by itself, at
// most once a minute.
func TestAPermissionRefusalRechecksTheAppByItself(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		w := newWorld(t, st)
		repo := w.f.AddRepo("kazuhahub", "wiki", true)
		w.space("S1", store.Remote{GHVia: "app", GHInstallationID: w.kazuhahub, GHRepoID: repo.ID})
		rem, _ := store.GetRemote(ctx, st.Q(), "S1")
		w.f.AppPerms = map[string]string{"administration": "write", "metadata": "read"}
		if v, _ := w.svc.View(ctx); len(v.Missing) != 0 {
			t.Fatalf("%+v", v)
		}
		before := appReads(w.harness)
		if _, err := w.svc.GitToken(ctx, rem, false); err == nil {
			t.Fatal("a token was made without the permission")
		}
		v, _ := w.svc.View(ctx)
		if strings.Join(v.Missing, ",") != "contents" || appReads(w.harness) != before+1 {
			t.Fatalf("the refusal did not refresh the view (%d reads): %+v", appReads(w.harness)-before, v)
		}
		// A storm of refusals costs one read a minute.
		for i := 0; i < 5; i++ {
			w.svc.GitToken(ctx, rem, false)
		}
		if n := appReads(w.harness) - before; n != 1 {
			t.Fatalf("%d reads of the App for six refusals", n)
		}
		w.clk.Advance(61 * time.Second)
		w.f.AppPerms = map[string]string{"administration": "write", "contents": "write", "metadata": "read"}
		w.f.SetInstall(w.kazuhahub, func(i *githubapptest.Install) {
			i.Perms = map[string]string{"administration": "write", "metadata": "read"}
		})
		w.svc.GitToken(ctx, rem, false)
		if v, _ = w.svc.View(ctx); len(v.Missing) != 0 || appReads(w.harness) != before+2 || len(v.Gaps) != 1 {
			t.Fatalf("after a minute: %+v", v)
		}
	})
}

// Whether the App can be installed on any account is read from GitHub's data;
// without it, an installation of another account proves it.
func TestTheAppsVisibilityIsRead(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		h := newHarness(t, st)
		// GitHub does not say: unknown.
		h.f.Public = nil
		v := h.configure()
		if v.AnyAccount != nil {
			t.Fatalf("unknown was guessed: %v", *v.AnyAccount)
		}
		// Installed on the owner's own organisation only: still unknown.
		h.f.AddOrg("kazuhahub")
		h.f.Install("kazuhahub", "all")
		if v, _ = h.svc.Recheck(ctx); v.AnyAccount != nil {
			t.Fatalf("the owner's own installation proves nothing: %v", *v.AnyAccount)
		}
		// An installation on someone else's account proves it can be.
		h.f.AddUser("tang")
		h.f.Install("tang", "all")
		if v, _ = h.svc.Recheck(ctx); v.AnyAccount == nil || !*v.AnyAccount {
			t.Fatalf("%v", v.AnyAccount)
		}
		// What GitHub says wins.
		no := false
		h.f.Public = &no
		if v, _ = h.svc.Recheck(ctx); v.AnyAccount == nil || *v.AnyAccount {
			t.Fatalf("a private App: %v", v.AnyAccount)
		}
		yes := true
		h.f.Public = &yes
		if v, _ = h.svc.Recheck(ctx); v.AnyAccount == nil || !*v.AnyAccount {
			t.Fatalf("a public App: %v", v.AnyAccount)
		}
	})
}
