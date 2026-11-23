package github_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/KKazuhaK/PetalBook/internal/github"
	"github.com/KKazuhaK/PetalBook/internal/githubapp/githubapptest"
	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

func TestSaveChecksWithGitHubAndStoresSealed(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		h := newHarness(t, st)
		if v, err := h.svc.View(ctx); err != nil || v.Configured || v.CallbackURL != publicURL+"/api/github/authorization/callback" {
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
			{"not a key", github.SaveRequest{AppID: "424242", PrivateKey: "hello"}, "github_key_invalid", 400},
			{"another key", github.SaveRequest{AppID: "424242", PrivateKey: githubapptest.KeyPEM(githubapptest.NewKey(t))}, "github_app_rejected", 400},
			{"another app id", github.SaveRequest{AppID: "7", PrivateKey: h.f.KeyPEM()}, "github_app_rejected", 400},
			{"another client id", github.SaveRequest{AppID: "424242", PrivateKey: h.f.KeyPEM(), ClientID: &bad, ClientSecret: "x"}, "github_client_rejected", 400},
			{"wrong secret", github.SaveRequest{AppID: "424242", PrivateKey: h.f.KeyPEM(), ClientID: &id, ClientSecret: "wrong"}, "github_client_rejected", 400},
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
		if code(err) != "github_app_in_use" || status(err) != http.StatusConflict {
			t.Fatalf("removing the client id while a user is connected: %v", err)
		}
		if _, err := h.svc.Save(ctx, github.SaveRequest{AppID: "999", PrivateKey: githubapptestKeyPEM(h)}); code(err) != "github_app_in_use" {
			t.Fatalf("another App id: %v", err)
		}
		// Deleting is refused while a space syncs through the App; connections go with it otherwise.
		h.space("S1", store.Remote{GHVia: "user", GHUserID: "U1", GHRepoID: 5})
		if err := h.svc.Delete(ctx); code(err) != "github_app_in_use" {
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
		if _, _, err := h.svc.Installations(ctx, ""); code(err) != "github_not_configured" || status(err) != 409 {
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
		it, err := h.svc.SetShared(ctx, orgInst, true)
		if err != nil || !it.Shared {
			t.Fatalf("%+v %v", it, err)
		}
		if _, err := h.svc.SetShared(ctx, userInst, true); code(err) != "invalid" {
			t.Fatalf("%v", err)
		}
		if _, err := h.svc.SetShared(ctx, 12345, true); code(err) != "not_found" || status(err) != 404 {
			t.Fatalf("%v", err)
		}
		list, _, _ := h.svc.Installations(ctx, next)
		shared := 0
		all, _, _ := h.svc.Installations(ctx, "")
		for _, x := range append(all, list...) {
			if x.Shared {
				shared++
			}
		}
		if shared != 1 {
			t.Fatalf("%d shared", shared)
		}
		// A space counts toward its installation.
		h.user("U1", "kazuha")
		h.space("S1", store.Remote{GHVia: "app", GHInstallationID: orgInst, GHRepoID: 1})
		it, err = h.svc.SetShared(ctx, orgInst, false)
		if err != nil || it.Shared || it.Spaces != 1 {
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
		if _, err := h.svc.Save(ctx, github.SaveRequest{AppID: "999", PrivateKey: githubapptestKeyPEM(h)}); code(err) != "github_app_in_use" {
			t.Fatalf("another App id: %v", err)
		}
	})
}
