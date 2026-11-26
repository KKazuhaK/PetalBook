package forge_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"petalbook.app/server/internal/connections"
	"petalbook.app/server/internal/forge"
	"petalbook.app/server/internal/forge/forgetest"
	"petalbook.app/server/internal/gitstore"
	"petalbook.app/server/internal/store"
	"petalbook.app/server/internal/store/storetest"
)

// each runs a test on both platforms and every database.
func each(t *testing.T, fn func(t *testing.T, h *harness)) {
	t.Helper()
	needGit(t)
	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			storetest.Each(t, func(t *testing.T, st *store.Store) { fn(t, newHarness(t, st, kind)) })
		})
	}
}

// space makes a space with a connection remote that names the user, as the manager would.
func (h *harness) space(id, provider, userID string, members map[string]string) {
	h.t.Helper()
	if err := store.InsertSpace(ctx, h.st.Q(), store.DefaultTenantID, id, id, "", "", h.now); err != nil {
		h.t.Fatal(err)
	}
	for u, role := range members {
		if err := store.AddMember(ctx, h.st.Q(), id, u, role, h.now); err != nil {
			h.t.Fatal(err)
		}
	}
	r := store.Remote{SpaceID: id, URL: h.f.URL + "/x/y.git", Branch: "main", CredType: "connection", Provider: provider, State: "ok", Updated: h.now,
		Via: "user", CredUserID: userID}
	if err := store.PutRemote(ctx, h.st.Q(), r); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) remote(spaceID string) *store.Remote {
	h.t.Helper()
	r, err := store.GetRemote(ctx, h.st.Q(), spaceID, store.RoleContent)
	if err != nil {
		h.t.Fatal(err)
	}
	return r
}

func wantCode(t *testing.T, err error, st int, c string) {
	t.Helper()
	if code(err) != c || status(err) != st {
		t.Fatalf("want %d %s, got %v", st, c, err)
	}
}

// --- setting a provider up -----------------------------------------------------------

func TestTheHostIsCheckedBeforeAProviderIsSaved(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		reason := func(body []byte) string {
			_, _, err := h.mgr.CreateProvider(ctx, "", body)
			var ce *connections.Error
			if !errors.As(err, &ce) || ce.Status != 400 || ce.Code != "invalid" {
				t.Fatalf("want 400 invalid, got %v", err)
			}
			return ce.Reason
		}
		// A server that is not this platform.
		h.f.NotThisPlatform = true
		if r := reason(h.body("x", nil)); r != forge.ReasonNotPlatform {
			t.Fatalf("a server that is not %s: %q", h.kind, r)
		}
		h.f.NotThisPlatform = false
		// A version too old to have PKCE.
		h.f.Version = map[string]string{"gitlab": "14.9.0", "gitea": "1.18.5"}[h.kind]
		if h.kind == "gitlab" {
			// GitLab does not say its version to the signed-out: it passes this check, and is read later.
			if _, _, err := h.mgr.CreateProvider(ctx, "", h.bodyFor(h.f, "old", nil)); err != nil {
				t.Fatal(err)
			}
			if err := h.prov("old").(connections.Administered).AdminDelete(ctx); err != nil {
				t.Fatal(err)
			}
		} else if r := reason(h.body("x", nil)); r != forge.ReasonVersion {
			t.Fatalf("an old version: %q", r)
		}
		// A Forgejo's version names the Gitea it follows.
		if h.kind == "gitea" {
			h.f.Version = "7.0.0+gitea-1.22.0"
			if _, _, err := h.mgr.CreateProvider(ctx, "", h.body("codeberg", map[string]any{"name": "Codeberg"})); err != nil {
				t.Fatalf("a Forgejo: %v", err)
			}
			h.f.Version = "1.22.0"
			if err := h.prov("codeberg").(connections.Administered).AdminDelete(ctx); err != nil {
				t.Fatal(err)
			}
		}
		// A client the host does not know.
		if r := reason(h.body("x", map[string]any{"clientSecret": "not-the-secret"})); r != forge.ReasonClient {
			t.Fatalf("a wrong secret: %q", r)
		}
		if r := reason(h.body("x", map[string]any{"clientId": "nobody"})); r != forge.ReasonClient {
			t.Fatalf("a wrong id: %q", r)
		}
		// Nothing was kept by any of those.
		if n := h.mgr.Providers(); n != 0 {
			t.Fatalf("%d providers after refusals", n)
		}
		rows, _ := store.ListConnProviderRows(ctx, h.st.Q())
		if len(rows) != 0 {
			t.Fatalf("%d rows", len(rows))
		}
	})
}

func TestTheRequestIsChecked(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		bad := []map[string]any{
			{"id": "GitLab"}, {"id": "git"}, {"id": "github"}, {"id": "t-acme-github"}, {"id": "-x"}, {"id": "x-"}, {"id": ""}, {"id": strings.Repeat("a", 41)},
			{"type": "github"}, {"type": ""}, {"type": "bitbucket"},
			{"host": ""}, {"host": "gitlab.example.com/path"}, {"host": "user@gitlab.example.com"}, {"host": "ftp://x.example.com"},
			{"host": "x.example.com:99999"}, {"host": "x_y.example.com"}, {"host": "x.example.com?a=b"},
			{"clientId": ""}, {"clientSecret": ""}, {"name": strings.Repeat("n", 81)}, {"name": "a\nb"},
		}
		for _, b := range bad {
			_, _, err := h.mgr.CreateProvider(ctx, "", h.body("ok", b))
			wantCode(t, err, 400, "invalid")
		}
		if _, _, err := h.mgr.CreateProvider(ctx, "", []byte(`{nonsense`)); code(err) != "invalid" {
			t.Fatalf("%v", err)
		}
		// The first good one, then the same host and the same id are taken.
		h.add("ok")
		_, _, err := h.mgr.CreateProvider(ctx, "", h.body("ok", nil))
		wantCode(t, err, 409, "exists")
		_, _, err = h.mgr.CreateProvider(ctx, "", h.body("other", nil))
		wantCode(t, err, 409, "exists") // one OAuth application per host
	})
}

func TestTheStorageHostsAreHeld(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		h.mgr.SetHostAllowed(func(host string) error { return &gitstore.HostError{Host: host} })
		_, _, err := h.mgr.CreateProvider(ctx, "", h.body("x", nil))
		var he *gitstore.HostError
		if !errors.As(err, &he) {
			t.Fatalf("a host the storage settings do not allow was accepted: %v", err)
		}
		h.mgr.SetHostAllowed(func(host string) error {
			if host != h.f.Host() {
				t.Errorf("asked about %q, want %q", host, h.f.Host())
			}
			return nil
		})
		h.add("x")
	})
}

// A host that is not on a public address is refused, whatever the allowed
// hosts say: it is the check every connection to a host passes.
func TestAHostThatIsNotPublicIsRefused(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		opt := h.opt
		opt.Unsafe = false
		opt.Registry = connections.NewRegistry(connections.NewGit())
		strict, err := forge.NewManager(ctx, opt)
		if err != nil {
			t.Fatal(err)
		}
		var ce *connections.Error
		_, _, err = strict.CreateProvider(ctx, "", h.body("x", map[string]any{"host": h.f.Host()}))
		if !errors.As(err, &ce) || ce.Reason != forge.ReasonPublicHost {
			t.Fatalf("a loopback host: %v", err)
		}
		// Plain http is for the unsafe switch only.
		_, _, err = strict.CreateProvider(ctx, "", h.body("x", nil))
		if !errors.As(err, &ce) || ce.Reason != forge.ReasonHost {
			t.Fatalf("plain http: %v", err)
		}
		for _, host := range []string{"127.0.0.1", "10.0.0.5", "169.254.169.254", "192.168.1.1:8080", "[::1]", "[fe80::1]:3000", "100.64.0.1"} {
			_, _, err = strict.CreateProvider(ctx, "", h.body("x", map[string]any{"host": host}))
			if !errors.As(err, &ce) || ce.Reason != forge.ReasonPublicHost {
				t.Fatalf("%s: %v", host, err)
			}
		}
		if got := h.f.Count("GET", "/api/"); got != 0 {
			t.Fatalf("the fake was reached %d times by a manager that must not reach it", got)
		}
	})
}

func TestTheLimitOfProviders(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		for i := 0; i < forge.SiteLimit; i++ {
			err := store.InsertConnProviderRow(ctx, h.st.Q(), store.ConnProviderRow{ID: fmt.Sprintf("p%d", i), Type: h.kind, Host: fmt.Sprintf("h%d.example.com", i), Scheme: "https",
				Name: "P", Owner: store.ConnProviderSite, ClientID: "c", SecretEnc: "x", State: "ok", PartialSync: "unknown", Created: h.now, Updated: h.now})
			if err != nil {
				t.Fatal(err)
			}
		}
		_, _, err := h.mgr.CreateProvider(ctx, "", h.body("one-more", nil))
		wantCode(t, err, 409, "provider_limit")
	})
}

func TestTheSecretIsSealedAndNeverShown(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p, view, err := h.mgr.CreateProvider(ctx, "root-id", h.body("main", nil))
		if err != nil {
			t.Fatal(err)
		}
		a := p.(connections.Administered)
		got, _ := a.AdminView(ctx)
		for _, v := range []any{view, got} {
			raw, _ := json.Marshal(v)
			if strings.Contains(string(raw), h.f.ClientSecret) {
				t.Fatalf("the view holds the secret: %s", raw)
			}
			var m map[string]any
			_ = json.Unmarshal(raw, &m)
			if m["hasSecret"] != true || m["clientId"] != h.f.ClientID || m["configured"] != true || m["callbackUrl"] != publicURL+"/api/connections/main/authorization/callback" {
				t.Fatalf("%s", raw)
			}
			if _, has := m["clientSecret"]; has {
				t.Fatalf("the view has a clientSecret field: %s", raw)
			}
		}
		row, err := store.GetConnProviderRow(ctx, h.st.Q(), "main")
		if err != nil || row.SecretEnc == "" || strings.Contains(row.SecretEnc, h.f.ClientSecret) || row.UpdatedBy != "root-id" {
			t.Fatalf("%+v %v", row, err)
		}
		// Sealed with the provider's id: the same bytes under another id do not open.
		if _, err := h.box.Open("other", "connection-secret", row.SecretEnc); err == nil {
			t.Fatal("the sealed secret opens for another provider")
		}
		h.mustNotLeak()
	})
}

func TestAProviderIsSavedAcrossARestart(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main")
		h.user("U1", "tang")
		h.f.AddUser("tang-" + h.kind)
		h.connect(p, "U1", "tang-"+h.kind)
		reg := connections.NewRegistry(connections.NewGit())
		opt := h.opt
		opt.Registry = reg
		if _, err := forge.NewManager(ctx, opt); err != nil {
			t.Fatal(err)
		}
		p2, ok := reg.Get("main")
		if !ok {
			t.Fatal("the provider is not back")
		}
		in, err := p2.Info(ctx, "U1")
		if err != nil || !in.Configured || in.Connection == nil || in.Connection.State != "ok" {
			t.Fatalf("%+v %v", in, err)
		}
		// Its secret was opened again: a renewal works.
		h.clk.Advance(5 * time.Hour)
		if _, err := p2.(*forge.Provider).UserToken(ctx, "U1"); err != nil {
			t.Fatal(err)
		}
	})
}

func TestAdministratorsChangeAProvider(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		h.user("U1", "tang")
		h.f.AddUser("tang-" + h.kind)
		h.connect(p, "U1", "tang-"+h.kind)
		// The name changes; id, type and host do not.
		if _, err := p.AdminSave(ctx, []byte(`{"name":"Renamed"}`)); err != nil {
			t.Fatal(err)
		}
		if p.Descriptor().Name != "Renamed" {
			t.Fatal("the name did not change")
		}
		for _, b := range []string{`{"id":"zz"}`, `{"type":"github"}`, `{"host":"elsewhere.example.com"}`} {
			_, err := p.AdminSave(ctx, []byte(b))
			wantCode(t, err, 400, "invalid")
		}
		// A secret the host does not accept is not kept.
		_, err := p.AdminSave(ctx, []byte(`{"clientSecret":"wrong"}`))
		wantCode(t, err, 400, "invalid")
		if _, err := p.UserToken(ctx, "U1"); err != nil {
			t.Fatalf("a refused save broke the connection: %v", err)
		}
		// A good one is, and what it replaces is not in the answer.
		v, err := p.AdminSave(ctx, []byte(fmt.Sprintf(`{"clientSecret":%q}`, h.f.ClientSecret)))
		if err != nil {
			t.Fatal(err)
		}
		if raw, _ := json.Marshal(v); strings.Contains(string(raw), h.f.ClientSecret) {
			t.Fatal("the secret is in the answer")
		}
		h.mustNotLeak("wrong")
	})
}

func TestACheckThatFailsLeavesTheProviderNotSetUp(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		h.user("U1", "tang")
		h.f.NotThisPlatform = true
		if _, err := p.AdminCheck(ctx); code(err) != "invalid" {
			t.Fatalf("%v", err)
		}
		in, _ := p.Info(ctx, "U1")
		if in.Configured {
			t.Fatal("a provider whose host fails its check is configured")
		}
		_, err := p.StartConnect(ctx, "U1", connections.StartRequest{})
		wantCode(t, err, 409, "provider_not_configured")
		h.f.NotThisPlatform = false
		if _, err := p.AdminCheck(ctx); err != nil {
			t.Fatal(err)
		}
		if in, _ = p.Info(ctx, "U1"); !in.Configured {
			t.Fatal("a check that passes does not set the provider up again")
		}
		// A host that cannot be reached is not a verdict.
		h.f.Down = true
		if _, err := p.AdminCheck(ctx); err == nil {
			t.Fatal("a host that is down passed its check")
		}
		if in, _ = p.Info(ctx, "U1"); !in.Configured {
			t.Fatal("a host that is down made the provider not set up")
		}
	})
}

func TestARemovedProviderMustNotBeInUse(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		h.user("U1", "tang")
		h.f.AddUser("tang-" + h.kind)
		h.connect(p, "U1", "tang-"+h.kind)
		h.space("S1", "main", "U1", map[string]string{"U1": "owner"})
		err := p.AdminDelete(ctx)
		wantCode(t, err, 409, "provider_in_use")
		var ce *connections.Error
		errors.As(err, &ce)
		if ce.Counts["connections"] != 1 || ce.Counts["spaces"] != 1 {
			t.Fatalf("%v", ce.Counts)
		}
		if err := store.DeleteRemote(ctx, h.st.Q(), "S1", store.RoleContent); err != nil {
			t.Fatal(err)
		}
		wantCode(t, p.AdminDelete(ctx), 409, "provider_in_use") // still one connection
		if err := p.Disconnect(ctx, "U1"); err != nil {
			t.Fatal(err)
		}
		if err := p.AdminDelete(ctx); err != nil {
			t.Fatal(err)
		}
		if _, ok := h.reg.Get("main"); ok {
			t.Fatal("the registry still has it")
		}
		if _, err := store.GetConnProviderRow(ctx, h.st.Q(), "main"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%v", err)
		}
	})
}

// --- connecting a person -------------------------------------------------------------

func TestAPersonConnectsOnceWithPKCE(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		h.user("U1", "tang")
		h.f.AddUser("tang-h")
		st, err := p.StartConnect(ctx, "U1", connections.StartRequest{Return: "/s/ABC/settings"})
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(st.URL)
		q := u.Query()
		if u.Host != h.f.Host() || q.Get("client_id") != h.f.ClientID || q.Get("code_challenge_method") != "S256" || len(q.Get("code_challenge")) < 40 ||
			len(q.Get("state")) < 40 || q.Get("redirect_uri") != publicURL+"/api/connections/main/authorization/callback" || q.Get("response_type") != "code" {
			t.Fatalf("%s", st.URL)
		}
		if strings.Contains(st.URL, h.f.ClientSecret) || st.Binding == "" || strings.Contains(st.URL, st.Binding) {
			t.Fatal("the secret or the binding is in the address")
		}
		back, _ := url.Parse(h.f.Authorize(st.URL, "tang-h"))
		to, clear := p.CompleteConnect(ctx, st.Binding, "U1", back.Query())
		if to != "/s/ABC/settings" || !clear {
			t.Fatalf("%q %v", to, clear)
		}
		in, _ := p.Info(ctx, "U1")
		if in.Connection == nil || in.Connection.Login != "tang-h" || in.Connection.State != "ok" || in.Connection.AccountID == "" || !in.UserAuthorization {
			t.Fatalf("%+v", in.Connection)
		}
		// The tokens are sealed in the database, apart.
		row, _ := store.GetRepoLink(ctx, h.st.Q(), "main", "U1")
		for _, s := range h.f.AllTokens() {
			if strings.Contains(row.AccessEnc, s) || strings.Contains(row.RefreshEnc, s) {
				t.Fatal("a token is stored as it is")
			}
		}
		if row.AccessEnc == "" || row.RefreshEnc == "" || row.AccessEnc == row.RefreshEnc {
			t.Fatalf("%+v", row)
		}
		// The attempt is one-time: the same return again changes nothing.
		to, _ = p.CompleteConnect(ctx, st.Binding, "U1", back.Query())
		if !strings.Contains(to, "connection_error=expired") {
			t.Fatalf("%q", to)
		}
		if p.Attempts() != 0 {
			t.Fatalf("%d attempts left", p.Attempts())
		}
		h.mustNotLeak()
	})
}

func TestTheReturnMustBeTheBrowsersOwn(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		h.user("U1", "tang")
		h.user("U2", "wu")
		h.f.AddUser("tang-h")
		st, _ := p.StartConnect(ctx, "U1", connections.StartRequest{})
		back, _ := url.Parse(h.f.Authorize(st.URL, "tang-h"))
		// Another person's session, no session, another browser's binding, a different state.
		for name, c := range map[string]struct{ binding, user string }{"another user": {st.Binding, "U2"}, "no session": {st.Binding, ""}, "no cookie": {"", "U1"}, "another cookie": {"x" + st.Binding, "U1"}} {
			to, _ := p.CompleteConnect(ctx, c.binding, c.user, back.Query())
			if !strings.Contains(to, "connection_error=expired") {
				t.Fatalf("%s: %q", name, to)
			}
		}
		bad := back.Query()
		bad.Set("state", "other")
		if to, _ := p.CompleteConnect(ctx, st.Binding, "U1", bad); !strings.Contains(to, "connection_error=expired") {
			t.Fatalf("%q", to)
		}
		dup := back.Query()
		dup.Add("state", "again")
		if to, _ := p.CompleteConnect(ctx, st.Binding, "U1", dup); !strings.Contains(to, "connection_error=expired") {
			t.Fatalf("%q", to)
		}
		for _, id := range []string{"U1", "U2"} {
			if in, _ := p.Info(ctx, id); in.Connection != nil {
				t.Fatalf("%s got connected by a return that was not theirs", id)
			}
		}
		// The owner's own return still works: the others did not spend it.
		if to, _ := p.CompleteConnect(ctx, st.Binding, "U1", back.Query()); strings.Contains(to, "connection_error") {
			t.Fatalf("%q", to)
		}
	})
}

func TestARefusedAuthorisationIsDeniedNotConnected(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		h.user("U1", "tang")
		st, _ := p.StartConnect(ctx, "U1", connections.StartRequest{})
		back, _ := url.Parse(h.f.Deny(st.URL))
		to, clear := p.CompleteConnect(ctx, st.Binding, "U1", back.Query())
		if !strings.Contains(to, "connection_error=denied") || !clear {
			t.Fatalf("%q", to)
		}
		// A code the host does not know is a provider error, and the attempt is over.
		st, _ = p.StartConnect(ctx, "U1", connections.StartRequest{})
		q := url.Values{"code": {"nonsense"}, "state": {mustState(st.URL)}}
		if to, _ := p.CompleteConnect(ctx, st.Binding, "U1", q); !strings.Contains(to, "connection_error=provider_error") {
			t.Fatalf("%q", to)
		}
		// An address that is not a path of this site is never returned to.
		for _, ret := range []string{"https://evil.example/", "//evil.example", "/api/spaces", "/login", "\\evil"} {
			st, _ = p.StartConnect(ctx, "U1", connections.StartRequest{Return: ret})
			h.f.AddUser("x")
			back, _ = url.Parse(h.f.Authorize(st.URL, "x"))
			if to, _ := p.CompleteConnect(ctx, st.Binding, "U1", back.Query()); to != "/" {
				t.Fatalf("return %q went to %q", ret, to)
			}
			_ = p.Disconnect(ctx, "U1")
		}
	})
}

func mustState(authURL string) string {
	u, _ := url.Parse(authURL)
	return u.Query().Get("state")
}

func TestOneAccountLinksToOnePerson(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		h.user("U1", "tang")
		h.user("U2", "wu")
		h.f.AddUser("shared")
		h.f.AddUser("other")
		if to := h.connect(p, "U1", "shared"); strings.Contains(to, "error") {
			t.Fatal(to)
		}
		if to := h.connect(p, "U2", "shared"); !strings.Contains(to, "connection_error=account_in_use") {
			t.Fatalf("%q", to)
		}
		if in, _ := p.Info(ctx, "U2"); in.Connection != nil {
			t.Fatal("the second person was connected")
		}
		// The first can change account until a space syncs through the old one.
		if to := h.connect(p, "U1", "other"); strings.Contains(to, "error") {
			t.Fatal(to)
		}
		h.space("S1", "main", "U1", map[string]string{"U1": "owner"})
		if to := h.connect(p, "U1", "shared"); !strings.Contains(to, "connection_error=different_account") {
			t.Fatalf("%q", to)
		}
		if in, _ := p.Info(ctx, "U1"); in.Connection.Login != "other" || in.Connection.Spaces != 1 {
			t.Fatalf("%+v", in.Connection)
		}
	})
}

func TestACallbackWithoutAProviderConnectionChangesNothing(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		to, clear := p.CompleteConnect(ctx, "", "", url.Values{"code": {"c"}, "state": {"s"}})
		if !strings.HasPrefix(to, "/settings?provider=main&connection_error=expired") || clear {
			t.Fatalf("%q %v", to, clear)
		}
	})
}

// --- renewal -------------------------------------------------------------------------

func TestAnExpiringTokenIsRenewedOnceAtATime(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		h.user("U1", "tang")
		h.f.AddUser("tang-h")
		h.connect(p, "U1", "tang-h")
		first, err := p.UserToken(ctx, "U1")
		if err != nil {
			t.Fatal(err)
		}
		oldRefresh := h.f.RefreshTokenOf("tang-h")
		// Within its life it is used as it is; no request is made.
		before := h.f.Count("POST", "/")
		if tok, _ := p.UserToken(ctx, "U1"); tok != first || h.f.Count("POST", "/") != before {
			t.Fatal("a token that is good was renewed")
		}
		// Four minutes before its end: renewed, by one request however many ask.
		h.clk.Advance(h.f.AccessTTL - 4*time.Minute)
		var wg sync.WaitGroup
		got := make([]string, 8)
		for i := range got {
			wg.Add(1)
			go func() {
				defer wg.Done()
				got[i], _ = p.UserToken(ctx, "U1")
			}()
		}
		wg.Wait()
		for _, g := range got {
			if g == "" || g == first || g != got[0] {
				t.Fatalf("%q after renewal, was %q", g, first)
			}
		}
		if n := h.f.Count("POST", "/") - before; n != 1 {
			t.Fatalf("%d renewals for 8 callers", n)
		}
		// The new pair is the stored one, the old refresh token is spent, and a
		// restart reads the new pair.
		row, _ := store.GetRepoLink(ctx, h.st.Q(), "main", "U1")
		if row.Version != 2 || row.State != "ok" {
			t.Fatalf("%+v", row)
		}
		if h.f.RefreshTokenOf("tang-h") == oldRefresh {
			t.Fatal("the refresh token was not rotated")
		}
		h.mustNotLeak()
	})
}

func TestARevokedConnectionExpiresAndReconnectingFixesIt(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		h.user("U1", "tang")
		h.f.AddUser("tang-h")
		h.connect(p, "U1", "tang-h")
		h.space("S1", "main", "U1", map[string]string{"U1": "owner"})
		h.f.RevokeUser("tang-h")
		h.clk.Advance(h.f.AccessTTL + time.Minute)
		_, err := p.GitToken(ctx, h.remote("S1"), false)
		var ce *gitstore.CredentialError
		if !errors.As(err, &ce) || ce.Code != connections.CodeConnectionExpired || ce.Offline {
			t.Fatalf("%v", err)
		}
		in, _ := p.Info(ctx, "U1")
		if in.Connection.State != "expired" {
			t.Fatalf("%+v", in.Connection)
		}
		// Everything that needs the token answers not_connected, with the provider's id.
		_, _, err = p.Owners(ctx, "U1", "")
		wantCode(t, err, 409, "not_connected")
		_, err = p.PlanCreate(ctx, "U1", "tang-h")
		wantCode(t, err, 409, "not_connected")
		// Connecting again mends it.
		fixed := make(chan string, 1)
		p.OnChange(func(u string) { fixed <- u })
		h.connect(p, "U1", "tang-h")
		if u := <-fixed; u != "U1" {
			t.Fatalf("%q", u)
		}
		if _, err := p.GitToken(ctx, h.remote("S1"), false); err != nil {
			t.Fatal(err)
		}
	})
}

func TestTheDailyRenewalKeepsRefreshTokensThatWouldEnd(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		h.user("U1", "tang")
		h.f.AddUser("tang-h")
		h.connect(p, "U1", "tang-h")
		in, _ := p.Info(ctx, "U1")
		if h.kind == "gitlab" {
			// GitLab's refresh tokens do not end: nothing is due, and nothing is asked.
			if !in.Connection.RefreshExpires.IsZero() {
				t.Fatalf("%v", in.Connection.RefreshExpires)
			}
			h.clk.Advance(1000 * 24 * time.Hour)
			if n := h.mgr.Tokens().RenewDue(ctx); n != 0 {
				t.Fatalf("%d renewed", n)
			}
			return
		}
		// Gitea's end 730 hours after they were made, which the token says.
		if want := h.clk.Now().Add(730 * time.Hour); in.Connection.RefreshExpires.Sub(want).Abs() > time.Minute {
			t.Fatalf("the refresh token ends %v, want about %v", in.Connection.RefreshExpires, want)
		}
		before := h.f.Count("POST", "/login/oauth/access_token")
		h.clk.Advance(20 * 24 * time.Hour) // 250 hours to go: not in the last tenth of its life
		if n := h.mgr.Tokens().RenewDue(ctx); n != 0 {
			t.Fatalf("%d renewed early", n)
		}
		h.clk.Advance(180 * time.Hour) // 70 hours to go
		if n := h.mgr.Tokens().RenewDue(ctx); n != 1 {
			t.Fatalf("%d renewed", n)
		}
		if h.f.Count("POST", "/login/oauth/access_token")-before != 1 {
			t.Fatal("not one request")
		}
		in, _ = p.Info(ctx, "U1")
		if in.Connection.RefreshExpires.Sub(h.clk.Now().Add(730*time.Hour)).Abs() > time.Minute || in.Connection.State != "ok" {
			t.Fatalf("%+v", in.Connection)
		}
		// The new refresh token has all its life ahead: no request again for weeks.
		for _, d := range []time.Duration{24 * time.Hour, 10 * 24 * time.Hour} {
			h.clk.Advance(d)
			if n := h.mgr.Tokens().RenewDue(ctx); n != 0 {
				t.Fatalf("%d renewed", n)
			}
		}
	})
}

func TestARefreshTokenThatEndedExpiresTheConnectionWithoutCrashing(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		h.user("U1", "tang")
		h.f.AddUser("tang-h")
		h.connect(p, "U1", "tang-h")
		h.f.ExpireRefresh("tang-h")
		h.clk.Advance(h.f.AccessTTL)
		if _, err := p.UserToken(ctx, "U1"); !errors.Is(err, forge.ErrAuthorizationExpired) {
			t.Fatalf("%v", err)
		}
		if in, _ := p.Info(ctx, "U1"); in.Connection.State != "expired" {
			t.Fatalf("%+v", in.Connection)
		}
		row, _ := store.GetRepoLink(ctx, h.st.Q(), "main", "U1")
		if row.AccessEnc != "" || row.RefreshEnc != "" {
			t.Fatal("an expired connection keeps its tokens")
		}
	})
}

func TestAHostThatIsDownDoesNotExpireAConnection(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		h.user("U1", "tang")
		h.f.AddUser("tang-h")
		h.connect(p, "U1", "tang-h")
		h.space("S1", "main", "U1", map[string]string{"U1": "owner"})
		h.clk.Advance(h.f.AccessTTL)
		h.f.Down = true
		_, err := p.GitToken(ctx, h.remote("S1"), false)
		var ce *gitstore.CredentialError
		if !errors.As(err, &ce) || !ce.Offline || ce.Code != "" {
			t.Fatalf("%v", err)
		}
		h.f.Down = false
		if in, _ := p.Info(ctx, "U1"); in.Connection.State != "ok" {
			t.Fatalf("%+v", in.Connection)
		}
		if _, err := p.GitToken(ctx, h.remote("S1"), false); err != nil {
			t.Fatal(err)
		}
	})
}

func TestDisconnectingRevokesWhereTheHostCanAndAlwaysDeletes(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		h.user("U1", "tang")
		h.f.AddUser("tang-h")
		h.connect(p, "U1", "tang-h")
		tok := h.f.AccessTokenOf("tang-h")
		if err := p.Disconnect(ctx, "U1"); err != nil {
			t.Fatal(err)
		}
		revokes := h.f.Count("POST", "/oauth/revoke")
		if h.kind == "gitlab" && revokes != 1 {
			t.Fatalf("%d revocations", revokes)
		}
		if h.kind == "gitea" && revokes != 0 {
			t.Fatal("a Gitea has no revocation endpoint")
		}
		if h.kind == "gitlab" {
			// The token no longer works at the host.
			if _, err := forgeUser(h, tok); err == nil {
				t.Fatal("the token still works at the host")
			}
		}
		if _, err := store.GetRepoLink(ctx, h.st.Q(), "main", "U1"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%v", err)
		}
		if _, err := p.UserToken(ctx, "U1"); !errors.Is(err, forge.ErrNoAuthorization) {
			t.Fatalf("%v", err)
		}
		// A host that is down does not keep the tokens: they go anyway.
		h.connect(p, "U1", "tang-h")
		h.f.Down = true
		if err := p.Disconnect(ctx, "U1"); err != nil {
			t.Fatal(err)
		}
		if in, _ := p.Info(ctx, "U1"); in.Connection != nil {
			t.Fatal("still connected")
		}
	})
}

// forgeUser asks the host who a token belongs to.
func forgeUser(h *harness, tok string) (int, error) {
	path := "/api/v4/user"
	if h.kind == "gitea" {
		path = "/api/v1/user"
	}
	req, _ := http.NewRequest("GET", h.f.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return resp.StatusCode, fmt.Errorf("%d", resp.StatusCode)
	}
	return 200, nil
}

// --- places and repositories ----------------------------------------------------------

func TestTheOwnersAreThePersonAndTheGroupsTheyMayCreateIn(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		h.user("U1", "tang")
		h.f.AddUser("tang-h")
		h.f.AddUser("boss")
		h.f.AddGroup("acme", map[string]int{"tang-h": forgetest.Owner})
		h.f.AddGroup("readers", map[string]int{"tang-h": forgetest.Reporter})
		h.f.AddGroup("others", map[string]int{"boss": forgetest.Owner})
		h.connect(p, "U1", "tang-h")
		owners, next, err := p.Owners(ctx, "U1", "")
		if err != nil || next != "" {
			t.Fatalf("%v %q", err, next)
		}
		var got []string
		for _, o := range owners {
			got = append(got, o.Type+":"+o.Login+":"+o.Via)
			if o.InstallationID != 0 || o.NeedsInstall {
				t.Fatalf("a provider without installations has one: %+v", o)
			}
		}
		if strings.Join(got, ",") != "User:tang-h:user,Organization:acme:user" {
			t.Fatalf("%v", got)
		}
		// A search narrows both.
		s := p.OwnersQ
		if o, _, _ := s(ctx, "U1", "", "ac"); len(o) != 1 || o[0].Login != "acme" {
			t.Fatalf("%+v", o)
		}
		if o, _, _ := s(ctx, "U1", "", "tang"); len(o) != 1 || o[0].Login != "tang-h" {
			t.Fatalf("%+v", o)
		}
		// Many groups are paged, and the person's own is only on the first page.
		for i := 0; i < 40; i++ {
			h.f.AddGroup(fmt.Sprintf("team-%02d", i), map[string]int{"tang-h": forgetest.Owner})
		}
		first, cur, err := p.Owners(ctx, "U1", "")
		if err != nil || cur == "" || first[0].Login != "tang-h" {
			t.Fatalf("%v %q", err, cur)
		}
		var all []string
		for _, o := range first {
			all = append(all, o.Login)
		}
		for cur != "" {
			var page []connections.Owner
			if page, cur, err = p.Owners(ctx, "U1", cur); err != nil {
				t.Fatal(err)
			}
			for _, o := range page {
				if o.Login == "tang-h" {
					t.Fatal("the person's own account is on a later page")
				}
				all = append(all, o.Login)
			}
		}
		seen := map[string]bool{}
		for _, l := range all {
			if seen[l] {
				t.Fatalf("%s is listed twice", l)
			}
			seen[l] = true
		}
		if len(seen) != 42 { // the person, acme and 40 teams
			t.Fatalf("%d owners: %v", len(seen), all)
		}
		if _, _, err := p.Owners(ctx, "U1", "not-a-cursor"); status(err) != 400 {
			t.Fatalf("%v", err)
		}
		// Someone who is not connected gets not_connected, not an empty list.
		h.user("U2", "wu")
		_, _, err = p.Owners(ctx, "U2", "")
		wantCode(t, err, 409, "not_connected")
	})
}

func TestTheRepositoriesAreThoseThePersonCanPushTo(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		h.user("U1", "tang")
		h.f.AddUser("tang-h")
		h.f.AddUser("boss")
		h.f.AddGroup("acme", map[string]int{"tang-h": forgetest.Developer})
		h.f.AddRepo("tang-h", "mine", true)
		h.f.AddRepo("acme", "team-notes", true)
		h.f.AddRepo("boss", "theirs", true)
		h.f.AddRepo("boss", "public-read", false)
		h.f.AddRepo("boss", "shared", true)
		h.f.Collaborate("boss/shared", "tang-h", forgetest.Developer)
		h.connect(p, "U1", "tang-h")
		items, _, err := p.Repos(ctx, "U1", connections.RepoQuery{Mine: true})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, r := range items {
			if !r.CanPush || r.HTMLURL == "" || r.ID == 0 {
				t.Fatalf("%+v", r)
			}
			got = append(got, r.FullName)
		}
		if strings.Join(got, ",") != "boss/shared,acme/team-notes,tang-h/mine" {
			t.Fatalf("%v", got)
		}
		items, _, _ = p.Repos(ctx, "U1", connections.RepoQuery{Mine: true, Q: "NOTES"})
		if len(items) != 1 || items[0].FullName != "acme/team-notes" {
			t.Fatalf("%+v", items)
		}
		// Many are paged.
		for i := 0; i < 35; i++ {
			h.f.AddRepo("tang-h", fmt.Sprintf("r%02d", i), true)
		}
		items, cur, err := p.Repos(ctx, "U1", connections.RepoQuery{})
		if err != nil || cur == "" || len(items) != 30 {
			t.Fatalf("%d %q %v", len(items), cur, err)
		}
		more, cur, _ := p.Repos(ctx, "U1", connections.RepoQuery{Cursor: cur})
		if len(more) != 8 || cur != "" {
			t.Fatalf("%d %q", len(more), cur)
		}
		_, _, err = p.Repos(ctx, "U1", connections.RepoQuery{Installation: 5})
		wantCode(t, err, 400, "invalid")
	})
}

func TestARepositoryIsMadeInTheRightPlaceWithTheRightName(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		h.user("U1", "tang")
		h.f.AddUser("tang-h")
		h.f.AddUser("boss")
		h.f.AddGroup("acme", map[string]int{"tang-h": forgetest.Owner})
		h.f.AddGroup("locked", map[string]int{"tang-h": forgetest.Guest})
		h.f.AddGroup("others", map[string]int{"boss": forgetest.Owner})
		if h.kind == "gitlab" {
			h.f.AddGroup("acme/sub", map[string]int{"tang-h": forgetest.Owner})
		}
		h.connect(p, "U1", "tang-h")

		// The person's own account.
		plan, err := p.PlanCreate(ctx, "U1", "TANG-H")
		if err != nil || plan.Owner != "tang-h" || plan.Org != "" || plan.Via != "user" {
			t.Fatalf("%+v %v", plan, err)
		}
		repo, err := p.CreateRepo(ctx, plan, connections.NewRepoSpec{Name: "notes", Private: true, Description: "my notes"})
		if err != nil || repo.FullName != "tang-h/notes" || !repo.Private || repo.ID == 0 {
			t.Fatalf("%+v %v", repo, err)
		}
		if r := h.f.RepoByName("tang-h/notes"); r == nil || !r.Private || r.Desc != "my notes" {
			t.Fatalf("%+v", r)
		}
		if got := p.RepoURL(repo.Owner, repo.Name); got != h.f.URL+"/tang-h/notes.git" {
			t.Fatalf("%q", got)
		}
		cred, err := p.ChooseCredential(ctx, plan, repo)
		if err != nil || cred.Provider != "main" || cred.Via != "user" || cred.UserID != "U1" || cred.RepoID != repo.ID {
			t.Fatalf("%+v %v", cred, err)
		}
		// Taken.
		_, err = p.CreateRepo(ctx, plan, connections.NewRepoSpec{Name: "notes", Private: true})
		wantCode(t, err, 409, "name_taken")

		// A group, and on GitLab a subgroup.
		places := []string{"acme"}
		if h.kind == "gitlab" {
			places = append(places, "acme/sub")
		}
		for _, place := range places {
			plan, err = p.PlanCreate(ctx, "U1", place)
			if err != nil || plan.Org != place || plan.Owner != place {
				t.Fatalf("%s: %+v %v", place, plan, err)
			}
			repo, err = p.CreateRepo(ctx, plan, connections.NewRepoSpec{Name: "team", Private: true})
			if err != nil || repo.FullName != place+"/team" || repo.Owner != place || repo.Name != "team" {
				t.Fatalf("%s: %+v %v", place, repo, err)
			}
		}
		// A group the person may not create in, one they are not in, and one that is not there.
		_, err = p.PlanCreate(ctx, "U1", "others")
		wantCode(t, err, 403, "owner_not_allowed")
		_, err = p.PlanCreate(ctx, "U1", "nobody-at-all")
		wantCode(t, err, 403, "owner_not_allowed")
		if plan, err = p.PlanCreate(ctx, "U1", "locked"); err == nil {
			// GitLab lists a group the person is in, whatever the group lets them do; the host refuses the creation.
			_, err = p.CreateRepo(ctx, plan, connections.NewRepoSpec{Name: "x", Private: true})
			wantCode(t, err, 403, "provider_refused")
		} else {
			wantCode(t, err, 403, "owner_not_allowed")
		}
		for _, bad := range []string{"", "a b", "../x", "a//b", strings.Repeat("a", 300), "-x"} {
			if _, err := p.PlanCreate(ctx, "U1", bad); status(err) != 400 {
				t.Fatalf("%q: %v", bad, err)
			}
		}
		// Names.
		for _, ok := range []string{"notes", "my-notes_2.0", "a"} {
			if !p.ValidRepoName(ok) {
				t.Fatalf("%q", ok)
			}
		}
		for _, bad := range []string{"", ".", "..", "x.git", "a/b", "a b", strings.Repeat("a", 101)} {
			if p.ValidRepoName(bad) {
				t.Fatalf("%q", bad)
			}
		}
	})
}

func TestARepositoryThatExistsIsUsedOnlyWhereThePersonCanPush(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		h.user("U1", "tang")
		h.f.AddUser("tang-h")
		h.f.AddUser("boss")
		h.f.AddGroup("acme", map[string]int{"tang-h": forgetest.Developer})
		h.f.AddRepo("acme", "team", true)
		h.f.AddRepo("boss", "theirs", true)
		h.f.AddRepo("boss", "readable", true)
		h.f.Collaborate("boss/readable", "tang-h", forgetest.Reporter)
		h.connect(p, "U1", "tang-h")
		cred, repo, err := p.ResolveExistingIn(ctx, store.DefaultTenantID, "U1", "acme", "team", "")
		if err != nil || cred.Via != "user" || cred.UserID != "U1" || cred.RepoID != repo.ID || repo.FullName != "acme/team" {
			t.Fatalf("%+v %v", cred, err)
		}
		if _, _, err := p.ResolveExistingIn(ctx, store.DefaultTenantID, "U1", "acme", "team", "user"); err != nil {
			t.Fatal(err)
		}
		for _, c := range [][2]string{{"boss", "theirs"}, {"boss", "readable"}, {"acme", "nothing"}} {
			_, _, err = p.ResolveExistingIn(ctx, store.DefaultTenantID, "U1", c[0], c[1], "")
			wantCode(t, err, 403, "owner_not_allowed")
		}
		_, _, err = p.ResolveExistingIn(ctx, store.DefaultTenantID, "U1", "acme", "team", "app")
		wantCode(t, err, 400, "invalid")
		_, _, err = p.ResolveExistingIn(ctx, store.DefaultTenantID, "U1", "acme", "team", "tenant")
		wantCode(t, err, 400, "invalid")
		h.user("U2", "wu")
		_, _, err = p.ResolveExistingIn(ctx, store.DefaultTenantID, "U2", "acme", "team", "")
		wantCode(t, err, 409, "not_connected")
	})
}

func TestAddressesOfRepositories(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		host := h.f.Host()
		for _, raw := range []string{h.f.URL + "/tang/notes.git", h.f.URL + "/tang/notes", "git@" + strings.Split(host, ":")[0] + ":tang/notes.git", "ssh://git@" + host + "/tang/notes.git"} {
			if o, n, ok := p.ParseRepoURL(raw); !ok || o != "tang" || n != "notes" {
				t.Fatalf("%q: %q %q %v", raw, o, n, ok)
			}
		}
		o, n, ok := p.ParseRepoURL(h.f.URL + "/acme/sub/notes.git")
		if h.kind == "gitlab" && (!ok || o != "acme/sub" || n != "notes") {
			t.Fatalf("a subgroup: %q %q %v", o, n, ok)
		}
		if h.kind == "gitea" && ok {
			t.Fatal("Gitea has no subgroups")
		}
		for _, bad := range []string{h.f.URL + "/tang/notes.git?x=1", "https://user:pw@" + host + "/a/b.git", "ssh://root@" + host + "/a/b.git",
			h.f.URL + "/notes.git", h.f.URL + "/a/../b.git", "https://elsewhere.example.com/a/b.git", "git://" + host + "/a/b.git"} {
			if _, _, ok := p.ParseRepoURL(bad); ok {
				t.Fatalf("%q was read", bad)
			}
		}
	})
}

// --- the credential of a space --------------------------------------------------------

func TestAConnectionIsNeverUsedForAnotherPersonsSpace(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		h.user("A", "ann")
		h.user("B", "bob")
		h.f.AddUser("ann-h")
		h.f.AddUser("bob-h")
		h.connect(p, "A", "ann-h")
		h.connect(p, "B", "bob-h")
		h.space("SA", "main", "A", map[string]string{"A": "owner", "B": "editor"})
		h.space("SB", "main", "B", map[string]string{"B": "owner"})
		ta, err := p.GitToken(ctx, h.remote("SA"), false)
		if err != nil || ta != h.f.AccessTokenOf("ann-h") || ta == h.f.AccessTokenOf("bob-h") {
			t.Fatalf("A's space got %q: %v", ta, err)
		}
		tb, err := p.GitToken(ctx, h.remote("SB"), false)
		if err != nil || tb != h.f.AccessTokenOf("bob-h") {
			t.Fatalf("B's space got %q: %v", tb, err)
		}
		// A space whose connection is A's, where A is no longer a manager, is not
		// synced with A's token, nor with B's: nothing is lost, the remote stops.
		var ce *gitstore.CredentialError
		if err := store.RemoveMember(ctx, h.st.Q(), "SA", "A"); err != nil {
			t.Fatal(err)
		}
		if _, err := p.GitToken(ctx, h.remote("SA"), false); !errors.As(err, &ce) || ce.Code != connections.CodeConnectionMissing {
			t.Fatalf("%v", err)
		}
		// A person who may only edit is not a manager.
		if err := store.AddMember(ctx, h.st.Q(), "SA", "A", "editor", h.now); err != nil {
			t.Fatal(err)
		}
		if _, err := p.GitToken(ctx, h.remote("SA"), false); !errors.As(err, &ce) || ce.Code != connections.CodeConnectionMissing {
			t.Fatalf("an editor's connection syncs the space: %v", err)
		}
		// A space being made has no member yet: that is not asked.
		if _, err := p.GitToken(ctx, h.remote("SA"), true); err != nil {
			t.Fatal(err)
		}
		// A connection that was disconnected.
		if err := store.AddMember(ctx, h.st.Q(), "SB", "A", "owner", h.now); err != nil {
			t.Fatal(err)
		}
		if err := p.Disconnect(ctx, "B"); err != nil {
			t.Fatal(err)
		}
		if _, err := p.GitToken(ctx, h.remote("SB"), false); !errors.As(err, &ce) || ce.Code != connections.CodeConnectionMissing {
			t.Fatalf("%v", err)
		}
		// Credentials this provider does not understand.
		r := h.remote("SB")
		r.Via = "app"
		if _, err := p.GitToken(ctx, r, true); !errors.As(err, &ce) || ce.Code != connections.CodeNotConfigured {
			t.Fatalf("%v", err)
		}
		r.Via, r.CredUserID = "user", ""
		if _, err := p.GitToken(ctx, r, true); !errors.As(err, &ce) || ce.Code != connections.CodeNotConfigured {
			t.Fatalf("%v", err)
		}
	})
}

func TestGitIsGivenTheUserNameTheHostWants(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		if u := p.GitUsername(); u != "oauth2" {
			t.Fatalf("%q", u)
		}
		var _ connections.GitUser = p
	})
}

// --- partial sync --------------------------------------------------------------------

func TestPartialSyncIsDetectedPerHost(t *testing.T) {
	for _, filter := range []bool{true, false} {
		t.Run(fmt.Sprint("filter ", filter), func(t *testing.T) {
			each(t, func(t *testing.T, h *harness) {
				h.f.NoPartial = !filter
				h.f.RefreshTTL = 400 * 24 * time.Hour // the host's refresh tokens outlast the month this test waits
				p := h.add("main").(*forge.Provider)
				if p.Descriptor().Capabilities.PartialSync != "unknown" {
					t.Fatalf("%+v", p.Descriptor().Capabilities)
				}
				h.user("U1", "tang")
				h.f.AddUser("tang-h")
				h.connect(p, "U1", "tang-h")
				plan, _ := p.PlanCreate(ctx, "U1", "tang-h")
				repo, err := p.CreateRepo(ctx, plan, connections.NewRepoSpec{Name: "notes", Private: true})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := p.ChooseCredential(ctx, plan, repo); err != nil {
					t.Fatal(err)
				}
				want := map[bool]string{true: "yes", false: "no"}[filter]
				if got := p.Descriptor().Capabilities.PartialSync; got != want {
					t.Fatalf("partialSync is %q, want %q", got, want)
				}
				row, _ := store.GetConnProviderRow(ctx, h.st.Q(), "main")
				if row.PartialSync != want || row.PartialChecked.IsZero() {
					t.Fatalf("%+v", row)
				}
				// Kept for a month: no second question.
				before := h.f.Count("GET", "/tang-h/notes.git")
				_, _ = p.ChooseCredential(ctx, plan, repo)
				if h.f.Count("GET", "/tang-h/notes.git") != before {
					t.Fatal("the host was asked again within a month")
				}
				h.clk.Advance(31 * 24 * time.Hour)
				_, _ = p.ChooseCredential(ctx, plan, repo)
				if h.f.Count("GET", "/tang-h/notes.git") != before+1 {
					t.Fatal("the host was not asked again after a month")
				}
			})
		})
	}
}

// --- the host misbehaving --------------------------------------------------------------

func TestARateLimitIsPassedOnWithItsWait(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		h.user("U1", "tang")
		h.f.AddUser("tang-h")
		h.connect(p, "U1", "tang-h")
		h.f.LimitUntil(h.clk.Now().Add(90 * time.Second))
		_, _, err := p.Owners(ctx, "U1", "")
		wantCode(t, err, 429, "rate_limited")
		var ce *connections.Error
		errors.As(err, &ce)
		if ce.RetryAfter < 60*time.Second || ce.RetryAfter > 120*time.Second {
			t.Fatalf("%v", ce.RetryAfter)
		}
		h.f.LimitUntil(time.Time{})
		h.f.Down = true
		_, _, err = p.Owners(ctx, "U1", "")
		wantCode(t, err, 503, "unavailable")
		h.f.Down = false
		if _, _, err = p.Owners(ctx, "U1", ""); err != nil {
			t.Fatal(err)
		}
	})
}

func TestARevokedTokenAtTheHostEndsTheConnectionOnUse(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		h.user("U1", "tang")
		h.f.AddUser("tang-h")
		h.connect(p, "U1", "tang-h")
		h.f.RevokeUser("tang-h") // the person removed the application in their settings
		_, _, err := p.Owners(ctx, "U1", "")
		wantCode(t, err, 409, "not_connected")
		if in, _ := p.Info(ctx, "U1"); in.Connection.State != "expired" {
			t.Fatalf("%+v", in.Connection)
		}
	})
}

func TestARedirectIsNeverFollowed(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		h.user("U1", "tang")
		h.f.AddUser("tang-h")
		h.connect(p, "U1", "tang-h")
		var hits int
		var gotAuth string
		evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits++
			gotAuth = r.Header.Get("Authorization")
		}))
		defer evil.Close()
		h.f.Redirect = evil.URL + "/steal"
		_, _, err := p.Owners(ctx, "U1", "")
		if err == nil || hits != 0 || gotAuth != "" {
			t.Fatalf("a redirect was followed: %d %q %v", hits, gotAuth, err)
		}
	})
}

func TestNoTokenEverReachesALogOrAnError(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		h.user("U1", "tang")
		h.f.AddUser("tang-h")
		h.connect(p, "U1", "tang-h")
		var errs []string
		note := func(err error) {
			if err != nil {
				errs = append(errs, err.Error())
			}
		}
		_, _, err := p.Repos(ctx, "U1", connections.RepoQuery{Mine: true, Q: "x"})
		note(err)
		_, err = p.PlanCreate(ctx, "U1", "nobody")
		note(err)
		_, _, err = p.ResolveExistingIn(ctx, store.DefaultTenantID, "U1", "boss", "x", "")
		note(err)
		h.f.Down = true
		_, _, err = p.Owners(ctx, "U1", "")
		note(err)
		h.clk.Advance(h.f.AccessTTL)
		_, err = p.UserToken(ctx, "U1")
		note(err)
		h.f.Down = false
		h.f.RevokeUser("tang-h")
		_, err = p.UserToken(ctx, "U1")
		note(err)
		_, err = p.AdminSave(ctx, []byte(`{"clientSecret":"wrong-secret-value"}`))
		note(err)
		all := strings.Join(errs, "\n")
		for _, s := range append(h.f.AllTokens(), h.f.ClientSecret, "wrong-secret-value") {
			if strings.Contains(all, s) {
				t.Fatalf("a secret is in an error: %q", s)
			}
		}
		h.mustNotLeak("wrong-secret-value")
	})
}

// --- tenants -------------------------------------------------------------------------

func TestATenantsProviderIsNobodyElses(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		h.tenantUser("T1", "A1", "alice")
		h.tenantUser("T2", "B1", "bob")
		h.user("S1", "site-user")
		h.f.AddUser("alice-h")
		h.f.AddUser("bob-h")
		sealed, _ := h.box.Seal("acme", "connection-secret:T1", []byte(h.f.ClientSecret))
		err := store.InsertConnProviderRow(ctx, h.st.Q(), store.ConnProviderRow{ID: "acme", Type: h.kind, Host: h.f.Host(), Scheme: "http", Name: "Acme", Owner: "T1",
			ClientID: h.f.ClientID, SecretEnc: sealed, State: "ok", PartialSync: "unknown", Created: h.now, Updated: h.now})
		if err != nil {
			t.Fatal(err)
		}
		reg := connections.NewRegistry(connections.NewGit())
		opt := h.opt
		opt.Registry = reg
		if _, err := forge.NewManager(ctx, opt); err != nil {
			t.Fatal(err)
		}
		p, ok := reg.Get("acme")
		if !ok {
			t.Fatal("the provider is not loaded")
		}
		if d := p.Descriptor(); d.Owner != "tenant" || d.TenantID != "T1" {
			t.Fatalf("%+v", d)
		}
		// Seen by T1's people, not by anyone else's.
		if _, ok := reg.GetFor("acme", "T1"); !ok {
			t.Fatal("its own tenant does not see it")
		}
		for _, other := range []string{"T2", store.DefaultTenantID} {
			if _, ok := reg.GetFor("acme", other); ok {
				t.Fatalf("%s sees it", other)
			}
			for _, vp := range reg.ListFor(other) {
				if vp.Descriptor().ID == "acme" {
					t.Fatalf("%s lists it", other)
				}
			}
		}
		// The provider itself refuses people of another tenant, as not there.
		for _, u := range []string{"B1", "S1"} {
			_, err := p.StartConnect(ctx, u, connections.StartRequest{})
			wantCode(t, err, 404, "not_found")
			_, _, err = p.Owners(ctx, u, "")
			wantCode(t, err, 404, "not_found")
			_, err = p.PlanCreate(ctx, u, "x")
			wantCode(t, err, 404, "not_found")
		}
		_, _, err = p.ResolveExistingIn(ctx, "T2", "A1", "a", "b", "")
		wantCode(t, err, 404, "not_found") // a space of another tenant
		h.connect(p, "A1", "alice-h")
		if _, _, err := p.Owners(ctx, "A1", ""); err != nil {
			t.Fatal(err)
		}
		// A secret of a tenant is sealed for that tenant: it does not open as a site's.
		if _, err := h.box.Open("acme", "connection-secret", sealed); err == nil {
			t.Fatal("a tenant's secret opens as a site's")
		}
	})
}

var _ = json.Marshal

func TestAHostThatHidesItsVersionIsReadAfterTheFirstConnection(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		version := func() string {
			v, err := p.AdminView(ctx)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(v)
			var m map[string]any
			_ = json.Unmarshal(raw, &m)
			return m["version"].(string)
		}
		want := h.f.Version
		if h.kind == "gitlab" {
			// GitLab says its version to the signed-in only.
			if v := version(); v != "" {
				t.Fatalf("%q before anyone connected", v)
			}
		} else if version() != want {
			t.Fatalf("%q", version())
		}
		h.user("U1", "tang")
		h.f.AddUser("tang-h")
		h.connect(p, "U1", "tang-h")
		if v := version(); v != want {
			t.Fatalf("version %q after connecting, want %q", v, want)
		}
		// A host that was upgraded the wrong way (or is older than first thought) stops being usable.
		if h.kind == "gitlab" {
			h.user("U2", "wu")
			h.f.AddUser("wu-h")
			h.f.Version = "14.0.1"
			row, _ := store.GetConnProviderRow(ctx, h.st.Q(), "main")
			row.PlatformVersion = ""
			_ = store.UpdateConnProviderRow(ctx, h.st.Q(), *row)
			p2 := h.reloaded()
			h.connect(p2, "U2", "wu-h")
			if in, _ := p2.Info(ctx, "U2"); in.Configured {
				t.Fatal("a version without PKCE is configured")
			}
		}
	})
}

// reloaded is the provider as a restarted server has it.
func (h *harness) reloaded() connections.Provider {
	h.t.Helper()
	reg := connections.NewRegistry(connections.NewGit())
	opt := h.opt
	opt.Registry = reg
	if _, err := forge.NewManager(ctx, opt); err != nil {
		h.t.Fatal(err)
	}
	p, ok := reg.Get("main")
	if !ok {
		h.t.Fatal("not back")
	}
	return p
}

func TestASearchTextIsBounded(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		h.user("U1", "tang")
		h.f.AddUser("tang-h")
		h.connect(p, "U1", "tang-h")
		for _, q := range []string{strings.Repeat("a", 101), "a\nb"} {
			_, _, err := p.OwnersQ(ctx, "U1", "", q)
			wantCode(t, err, 400, "invalid")
			_, _, err = p.Repos(ctx, "U1", connections.RepoQuery{Q: q})
			wantCode(t, err, 400, "invalid")
		}
	})
}
