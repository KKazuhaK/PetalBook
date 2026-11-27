package github_test

import (
	"encoding/json"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"petalbook.app/server/internal/connections"
	"petalbook.app/server/internal/github"
	"petalbook.app/server/internal/githubapp"
	"petalbook.app/server/internal/secret"
	"petalbook.app/server/internal/secrets"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// submit plays the administrator's browser on GitHub: the manifest is posted,
// the App is created and the browser comes back with a code.
func submit(t *testing.T, h *harness, s *connections.ManifestStart) url.Values {
	t.Helper()
	b, err := json.Marshal(s.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	back, err := url.Parse(h.f.SubmitManifest(s.URL, string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return back.Query()
}

func TestMakingTheAppFromAManifest(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		h := newHarness(t, st)
		h.user("A1", "root")
		s, err := h.svc.StartManifest(ctx, "A1", connections.ManifestRequest{Organization: "", Name: ""})
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(s.URL)
		if u.Path != "/settings/apps/new" || len(u.Query().Get("state")) < 40 || s.Binding == "" {
			t.Fatalf("%s", s.URL)
		}
		m := s.Manifest.(githubapp.Manifest)
		if m.Name == "" || len(m.Name) > 34 || m.URL != publicURL || m.RedirectURL != publicURL+"/api/providers/github/manifest/callback" || m.SetupURL != m.RedirectURL ||
			len(m.CallbackURLs) != 2 || m.CallbackURLs[0] != publicURL+"/api/account/links/github/callback" || m.CallbackURLs[1] != publicURL+"/api/auth/github/github/callback" || !m.Public || !m.RequestOAuthOnInst ||
			!m.HookAttributes.Active || m.HookAttributes.URL != publicURL+"/api/github/webhook" || m.DefaultPermissions["administration"] != "write" || m.DefaultPermissions["contents"] != "write" ||
			m.DefaultPermissions["metadata"] != "read" || len(m.DefaultPermissions) != 3 {
			t.Fatalf("%+v", m)
		}
		q := submit(t, h, s)
		// Another browser, another user, or an attempt gone does not finish it.
		if to, _ := h.svc.CompleteManifest(ctx, "not-the-binding", "A1", q); to != "/admin/storage/connections?connection_app_error=expired" {
			t.Fatalf("%q", to)
		}
		h.user("A2", "other")
		if to, _ := h.svc.CompleteManifest(ctx, s.Binding, "A2", q); to != "/admin/storage/connections?connection_app_error=expired" {
			t.Fatalf("%q", to)
		}
		if to, _ := h.svc.CompleteManifest(ctx, s.Binding, "", q); to != "/admin/storage/connections?connection_app_error=expired" {
			t.Fatalf("%q", to)
		}
		if h.svc.Configured(ctx) {
			t.Fatal("a refused return configured the App")
		}
		to, clear := h.svc.CompleteManifest(ctx, s.Binding, "A1", q)
		if to != "/admin/storage/connections?connection_app=created" || !clear {
			t.Fatalf("%q %v", to, clear)
		}
		v, err := h.svc.View(ctx)
		if err != nil || !v.Configured || v.AppID != "424242" || v.Slug != "petalbook-notes" || v.ClientID != h.f.ClientID || !v.KeySet || !v.SecretSet || !v.UserAuthorizaton || len(v.Missing) != 0 {
			t.Fatalf("%+v %v", v, err)
		}
		// Stored sealed, exactly as an App entered by hand, and the webhook secret too.
		raw, _, _ := store.GetGitHubApp(ctx, st.Q())
		for _, plain := range []string{h.f.ClientSecret, "BEGIN RSA PRIVATE KEY", "MIIE", "fake-webhook-secret"} {
			if strings.Contains(raw, plain) {
				t.Fatalf("the stored settings hold %q", plain)
			}
		}
		// The record has none of them: they are the secrets of the site.
		if strings.Contains(raw, "Enc") {
			t.Fatalf("the record holds sealed secrets: %s", raw)
		}
		vault := secrets.Of(st, h.box)
		for name, want := range map[string]string{secrets.GitHubWebhookSecret: "fake-webhook-secret-0123456789", secrets.GitHubClientSecret: h.f.ClientSecret} {
			if got, ok, err := vault.Open(ctx, st.Q(), "", name); err != nil || !ok || got != want {
				t.Fatalf("%s: %q %v %v", name, got, ok, err)
			}
		}
		if _, ok, err := vault.Open(ctx, st.Q(), "", secrets.GitHubAppKey); err != nil || !ok {
			t.Fatalf("the App's key is not a secret of the site: %v %v", ok, err)
		}
		// The state works once.
		if to, _ := h.svc.CompleteManifest(ctx, s.Binding, "A1", q); to != "/admin/storage/connections?connection_app_error=expired" {
			t.Fatalf("a replay: %q", to)
		}
		if ms := h.f.Manifests(); len(ms) != 1 {
			t.Fatalf("%d manifests", len(ms))
		}
		h.mustNotLeak("fake-webhook-secret-0123456789")
	})
}

func TestTheManifestStateExpiresAndTheCodeIsChecked(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		h := newHarness(t, st)
		h.user("A1", "root")
		s, _ := h.svc.StartManifest(ctx, "A1", connections.ManifestRequest{Organization: "", Name: ""})
		q := submit(t, h, s)
		h.clk.Advance(11 * time.Minute)
		if to, clear := h.svc.CompleteManifest(ctx, s.Binding, "A1", q); to != "/admin/storage/connections?connection_app_error=expired" || !clear {
			t.Fatalf("%q %v", to, clear)
		}
		// A code GitHub does not know: the attempt is spent and the App is not changed.
		s, _ = h.svc.StartManifest(ctx, "A1", connections.ManifestRequest{Organization: "", Name: ""})
		q = submit(t, h, s)
		q.Set("code", "not-a-code")
		if to, _ := h.svc.CompleteManifest(ctx, s.Binding, "A1", q); to != "/admin/storage/connections?connection_app_error=provider_error" {
			t.Fatalf("%q", to)
		}
		if h.svc.Configured(ctx) {
			t.Fatal("a refused conversion configured the App")
		}
		// Anything missing is an expired attempt; a return from an installation is not an error.
		if to, _ := h.svc.CompleteManifest(ctx, "", "A1", url.Values{}); to != "/admin/storage/connections?connection_app_error=expired" {
			t.Fatalf("%q", to)
		}
		if to, _ := h.svc.CompleteManifest(ctx, "", "", url.Values{"setup_action": {"install"}, "installation_id": {"5"}}); to != "/settings/connections" {
			t.Fatalf("%q", to)
		}
		// A state made for connecting an account is no state for this.
		h.configure()
		h.user("U1", "kazuha")
		_, binding, _ := h.svc.StartInstall(ctx, "U1", "/")
		if to, _ := h.svc.CompleteManifest(ctx, binding, "U1", url.Values{"state": {"x"}, "code": {"y"}}); to != "/admin/storage/connections?connection_app_error=expired" {
			t.Fatalf("%q", to)
		}
	})
}

func TestStartingTheManifestFlow(t *testing.T) {
	storetest.Each(t, func(t *testing.T, st *store.Store) {
		h := newHarness(t, st)
		h.user("A1", "root")
		// An organisation, and a name of the administrator's choosing.
		s, err := h.svc.StartManifest(ctx, "A1", connections.ManifestRequest{Organization: "kazuhahub", Name: "Notes of the team"})
		if err != nil {
			t.Fatal(err)
		}
		if u, _ := url.Parse(s.URL); u.Path != "/organizations/kazuhahub/settings/apps/new" || s.Manifest.(githubapp.Manifest).Name != "Notes of the team" {
			t.Fatalf("%s %q", s.URL, s.Manifest.(githubapp.Manifest).Name)
		}
		for _, bad := range []struct{ org, name string }{{"a/b", ""}, {"has space", ""}, {"", strings.Repeat("x", 35)}, {"", "bad\nname"}} {
			if _, err := h.svc.StartManifest(ctx, "A1", connections.ManifestRequest{Organization: bad.org, Name: bad.name}); code(err) != "invalid" || status(err) != 400 {
				t.Fatalf("%+v: %v", bad, err)
			}
		}
		// No public address: GitHub could not send the browser back.
		box := secret.NewBoxWithKey([32]byte{1, 2, 3})
		c, _ := githubapp.New(githubapp.Config{API: h.f.URL, Web: h.f.URL})
		bare, _ := github.New(github.Options{Store: st, Secrets: box, Client: c, Logger: slog.New(slog.DiscardHandler)})
		if _, err := bare.StartManifest(ctx, "A1", connections.ManifestRequest{Organization: "", Name: ""}); code(err) != "public_url_needed" || status(err) != 409 {
			t.Fatalf("%v", err)
		}
		// An App that is in use is not replaced.
		h.configure()
		h.user("U1", "kazuha")
		h.f.AddUser("kazuha-gh")
		h.connect("U1", "kazuha-gh")
		if _, err := h.svc.StartManifest(ctx, "A1", connections.ManifestRequest{Organization: "", Name: ""}); code(err) != "provider_in_use" || status(err) != 409 {
			t.Fatalf("%v", err)
		}
	})
}
