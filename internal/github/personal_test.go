package github_test

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"petalbook.app/server/internal/connections"
	"petalbook.app/server/internal/githubapp/githubapptest"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// "Connect GitHub" leads to the App's installation page: installing on one's
// own account and authorising happen in one GitHub flow, and the callback
// finishes both: the link, and the installation connected to the person.
func TestConnectingByInstallingOnOnesOwnAccount(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		h := newHarness(t, st)
		h.user("U1", "kazuha")
		h.f.AddUser("kazuha-gh")
		ctx := allowAll(ctx)
		if _, _, err := h.svc.StartInstall(ctx, "U1", "/"); code(err) != "provider_not_configured" {
			t.Fatalf("%v", err)
		}
		h.configure()
		installURL, binding, err := h.svc.StartInstall(ctx, "U1", "/settings/connections")
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(installURL)
		if u.Host != "github.com" || u.Path != "/apps/petalbook-notes/installations/new" || len(u.Query().Get("state")) < 40 || u.Query().Get("code_challenge") != "" {
			t.Fatalf("%s", installURL)
		}
		back, _ := url.Parse(h.f.InstallApp(installURL, "kazuha-gh", h.svc.CallbackURL()))
		if back.Query().Get("setup_action") != "install" || back.Query().Get("installation_id") == "" {
			t.Fatalf("%s", back)
		}
		// Another browser or user does not finish it; the right one does, once.
		if to, _ := h.svc.CompleteAuthorization(ctx, "not-the-binding", "U1", back.Query()); to != "/settings?provider=github&connection_error=expired" {
			t.Fatalf("%q", to)
		}
		to, clear := h.svc.CompleteAuthorization(ctx, binding, "U1", back.Query())
		if to != "/settings/connections" || !clear {
			t.Fatalf("%q %v", to, clear)
		}
		if to, _ := h.svc.CompleteAuthorization(ctx, binding, "U1", back.Query()); to != "/settings?provider=github&connection_error=expired" {
			t.Fatalf("a replay: %q", to)
		}
		c, err := h.svc.Connection(ctx, "U1")
		if err != nil || c == nil || c.Login != "kazuha-gh" || c.State != "ok" {
			t.Fatalf("%+v %v", c, err)
		}
		if _, err := h.svc.PlanCreate(ctx, "U1", "kazuha-gh"); err != nil {
			t.Fatal(err)
		}
		inst, err := store.GitHubInstallationOwnedOn(ctx, st.Q(), store.InstallOwner{UserID: "U1"}, "kazuha-gh")
		if err != nil || inst.AccountType != "User" || inst.ClaimedBy != "U1" || back.Query().Get("installation_id") != strconv.FormatInt(inst.ID, 10) {
			t.Fatalf("%+v %v", inst, err)
		}
		h.mustNotLeak()

		// The plain authorisation still works with the same callback.
		authURL, binding, _ := h.svc.StartAuthorization(ctx, "U1", "/")
		back, _ = url.Parse(h.f.Authorize(authURL, "kazuha-gh"))
		if to, _ := h.svc.CompleteAuthorization(ctx, binding, "U1", back.Query()); to != "/" {
			t.Fatalf("%q", to)
		}

		// Installed, but GitHub did not ask for authorisation (the App does not
		// request it during installation): the person's own link is asked, and an
		// installation it does not list is not theirs.
		installURL, binding, _ = h.svc.StartInstall(ctx, "U1", "/")
		q := url.Values{"setup_action": {"install"}, "installation_id": {"7"}, "state": {stateOf(installURL)}}
		if to, _ := h.svc.CompleteAuthorization(ctx, binding, "U1", q); to != "/settings?provider=github&connection_error=installation_not_yours" {
			t.Fatalf("%q", to)
		}
		// ...and without a link there is nothing to ask with.
		h.user("U2", "two")
		installURL, binding, _ = h.svc.StartInstall(ctx, "U2", "/")
		q = url.Values{"setup_action": {"install"}, "installation_id": {"7"}, "state": {stateOf(installURL)}}
		if to, _ := h.svc.CompleteAuthorization(ctx, binding, "U2", q); to != "/settings?provider=github&connection_error=not_connected" {
			t.Fatalf("%q", to)
		}
		// An organisation that must approve the installation first.
		installURL, binding, _ = h.svc.StartInstall(ctx, "U1", "/")
		q = url.Values{"setup_action": {"request"}, "state": {stateOf(installURL)}}
		if to, _ := h.svc.CompleteAuthorization(ctx, binding, "U1", q); to != "/settings?provider=github&connection_error=requested" {
			t.Fatalf("%q", to)
		}
	})
}

func stateOf(installURL string) string {
	u, _ := url.Parse(installURL)
	return u.Query().Get("state")
}

func TestAPersonalAccountWithoutTheAppIsToldToInstallIt(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		w := newWorld(t, st)
		w.f.PersonalNeedsInstall = true
		got, _ := owners(t, w.harness, "U1")
		if len(got) == 0 || got[0].Login != "tang" || !got[0].NeedsInstall || got[0].Via != "user" {
			t.Fatalf("%+v", got)
		}
		for _, o := range got[1:] {
			if o.NeedsInstall {
				t.Fatalf("an organisation needs no install: %+v", o)
			}
		}
		// GitHub's 403 is not passed on: the answer says to install, and where.
		_, _, err := create(t, w, "U1", "tang", "notes")
		var he *connections.Error
		if !errors.As(err, &he) || he.Code != "app_not_installed" || he.Status != 409 ||
			he.InstallURL != "https://github.com/apps/petalbook-notes/installations/new" || strings.Contains(he.Msg, "integration") {
			t.Fatalf("%v", err)
		}
		// Installed there: it works, and the owners say so.
		w.f.Install("tang", "all")
		r, _, err := create(t, w, "U1", "tang", "notes")
		if err != nil || r.FullName != "tang/notes" {
			t.Fatalf("%v", err)
		}
		got, _ = owners(t, w.harness, "U1")
		if got[0].NeedsInstall {
			t.Fatalf("%+v", got[0])
		}
	})
}

func TestAnInstalledPersonalAccountWithoutAdministrationIsAPermissionProblem(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		w := newWorld(t, st)
		w.f.PersonalNeedsInstall = true
		in := w.f.Install("tang", "all")
		w.f.SetInstall(in, func(i *githubapptest.Install) {
			i.Perms = map[string]string{"members": "read", "contents": "write", "metadata": "read"}
		})
		_, _, err := create(t, w, "U1", "tang", "notes")
		if code(err) != "app_missing_permission" {
			t.Fatalf("%v (%s)", err, code(err))
		}
	})
}
