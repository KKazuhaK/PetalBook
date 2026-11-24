package github_test

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/connections"
	"github.com/KKazuhaK/PetalBook/internal/github"
	"github.com/KKazuhaK/PetalBook/internal/githubapp/githubapptest"
	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/store/storetest"
)

// The five problems the review of the first 2.7 found (docs/design.md, "2.7b
// 做出来的样子"), each pinned by a test.

// 1. GitHub made the App but saving it failed because GitHub could not be
// asked: the administrator is not told "nothing was saved". The credentials
// are kept, sealed, for a retry, and the redirect says so.
func TestAnAppMadeOnGitHubThatCouldNotBeSavedCanBeSavedAgain(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		h := newHarness(t, st)
		h.user("A1", "root")
		h.user("A2", "other")
		s, _ := h.svc.StartManifest(ctx, "A1", connections.ManifestRequest{})
		q := submit(t, h, s)
		h.f.AppDown = true
		to, clear := h.svc.CompleteManifest(ctx, s.Binding, "A1", q)
		if !clear || !strings.HasPrefix(to, "/admin/storage?connection_app_error=unavailable") ||
			!strings.Contains(to, "&pending=1") || !strings.Contains(to, "&app="+h.f.Slug) {
			t.Fatalf("%q", to)
		}
		if h.svc.Configured(ctx) {
			t.Fatal("the App was saved although GitHub could not be asked")
		}
		// Nothing secret is in the redirect or the logs.
		if strings.Contains(to, h.f.ClientSecret) || strings.Contains(h.logs.String(), h.f.ClientSecret) {
			t.Fatal("a secret leaked")
		}
		// Only the administrator who made it can finish it.
		if _, err := h.svc.RetryManifest(ctx, "A2"); code(err) != "not_found" {
			t.Fatalf("%v", err)
		}
		// GitHub still down: it stays pending.
		if _, err := h.svc.RetryManifest(ctx, "A1"); code(err) != "unavailable" || status(err) != 503 {
			t.Fatalf("%v", err)
		}
		h.f.AppDown = false
		v, err := h.svc.RetryManifest(ctx, "A1")
		if err != nil || h.svc.Configured(ctx) == false {
			t.Fatalf("%v %v", v, err)
		}
		if _, err := h.svc.RetryManifest(ctx, "A1"); code(err) != "not_found" {
			t.Fatalf("a pending App was used twice: %v", err)
		}
	})
}

func TestAnAppThatWaitedTooLongIsNotKept(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		h := newHarness(t, st)
		h.user("A1", "root")
		s, _ := h.svc.StartManifest(ctx, "A1", connections.ManifestRequest{})
		q := submit(t, h, s)
		h.f.AppDown = true
		h.svc.CompleteManifest(ctx, s.Binding, "A1", q)
		h.f.AppDown = false
		h.clk.Advance(11 * time.Minute)
		if _, err := h.svc.RetryManifest(ctx, "A1"); code(err) != "not_found" {
			t.Fatalf("%v", err)
		}
	})
}

// 2. An automatic re-check reads the App, asks GitHub, and writes back: if the
// App was saved again meanwhile, that newer one stays.
func TestARecheckNeverOverwritesANewlySavedApp(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		h := newHarness(t, st)
		h.configure()
		other := map[string]any{"appId": "777", "keyEnc": "x", "slug": "newer", "name": "Newer", "permissions": map[string]string{}}
		raw, _ := json.Marshal(other)
		// The App is saved again between the re-check's read and its write.
		github.SetRefreshWriteHook(func() {
			if err := store.SetSiteSetting(ctx, st.Q(), "github_app", string(raw), h.clk.Now()); err != nil {
				t.Error(err)
			}
		})
		defer github.SetRefreshWriteHook(nil)
		h.svc.Check(ctx)
		got, _, err := store.GetSiteSetting(ctx, st.Q(), "github_app")
		if err != nil || got != string(raw) {
			t.Fatalf("the newer App was overwritten: %s %v", got, err)
		}
	})
}

// 3. "The App is not installed for this person" is remembered for a minute: a
// listing of owners asks GitHub once, and logs once, however often it is read.
func TestNotInstalledForAPersonIsRemembered(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		w := newWorld(t, st)
		w.f.PersonalNeedsInstall = true
		w.f.AddUser("kai")
		w.user("U2", "kai")
		w.connect("U2", "kai")
		first, _ := owners(t, w.harness, "U2")
		if len(first) == 0 || !first[0].NeedsInstall {
			t.Fatalf("%+v", first)
		}
		asked := w.f.Count("GET", "/users/kai/installation")
		logged := strings.Count(w.logs.String(), "kai")
		for i := 0; i < 5; i++ {
			if o, _ := owners(t, w.harness, "U2"); !o[0].NeedsInstall {
				t.Fatalf("%+v", o)
			}
		}
		if n := w.f.Count("GET", "/users/kai/installation"); n != asked {
			t.Fatalf("GitHub was asked %d more times", n-asked)
		}
		if n := strings.Count(w.logs.String(), "kai"); n != logged {
			t.Fatalf("%d more log lines", n-logged)
		}
		// After a minute it is asked again (the person may have installed it since).
		w.clk.Advance(61 * time.Second)
		w.f.AddUser("kai")
		w.f.Install("kai", "all")
		if o, _ := owners(t, w.harness, "U2"); o[0].NeedsInstall {
			t.Fatalf("%+v", o)
		}
	})
}

// 4. A person's own token refused in an organisation the App is not installed
// on is "not installed" (with where to install it), never a missing permission.
func TestAnOrganisationWithoutTheAppIsNotAMissingPermission(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		w := newWorld(t, st)
		w.f.OrgNeedsInstall = true
		p, err := w.svc.PlanCreate(ctx, "U1", "kazuhahub")
		if err != nil || p.Via != "user" || p.Org != "kazuhahub" {
			t.Fatalf("%+v %v", p, err)
		}
		w.f.SetInstall(w.kazuhahub, func(i *githubapptest.Install) { i.Deleted = true })
		_, err = w.svc.CreateRepo(ctx, p, connections.NewRepoSpec{Name: "x", Private: true})
		var he *connections.Error
		if !asErr(err, &he) || he.Code != "app_not_installed" || he.Status != 409 || !strings.HasSuffix(he.InstallURL, "/installations/new") || he.Provider != "github" {
			t.Fatalf("%v", err)
		}
	})
}

// 5. The attempts of connecting accounts, however many, never push out an
// administrator's attempt to make the App.
func TestManyConnectStartsDoNotEvictAnAdministratorsManifestAttempt(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		h := newHarness(t, st)
		h.configure() // an App that nobody uses yet may be replaced
		h.user("A1", "root")
		s, err := h.svc.StartManifest(ctx, "A1", connections.ManifestRequest{})
		if err != nil {
			t.Fatal(err)
		}
		q := submit(t, h, s)
		for i := 0; i < 10_050; i++ {
			if _, _, err := h.svc.StartAuthorization(ctx, "U"+strconv.Itoa(i), "/"); err != nil {
				t.Fatal(err)
			}
		}
		if to, _ := h.svc.CompleteManifest(ctx, s.Binding, "A1", q); !strings.HasSuffix(to, "connection_app=created") {
			t.Fatalf("the administrator's attempt was pushed out: %q", to)
		}
	})
}

func asErr(err error, target **connections.Error) bool { return errors.As(err, target) }
