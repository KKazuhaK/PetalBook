package forge_test

import (
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"petalbook.app/server/internal/forge"
	"petalbook.app/server/internal/store"
)

// --- a 401 does not end a connection that can be renewed ----------------------------

func TestAnAccessTokenTheHostDroppedIsRenewedNotTheConnectionEnded(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		h.user("U1", "tang")
		h.f.AddUser("tang-h")
		h.connect(p, "U1", "tang-h")
		h.f.RevokeAccess("tang-h") // the refresh token is still good
		before := h.f.Count("POST", "/")
		if _, _, err := p.Owners(ctx, "U1", ""); err != nil {
			t.Fatalf("one refused access token ended the call: %v", err)
		}
		if n := h.f.Count("POST", "/") - before; n != 1 {
			t.Fatalf("%d renewals", n)
		}
		row, err := store.GetRepoLink(ctx, h.st.Q(), "main", "U1")
		if err != nil || row.State != "ok" || row.RefreshEnc == "" || row.Version != 2 {
			t.Fatalf("%+v %v", row, err)
		}
	})
}

func TestARequestWithAnOldTokenDoesNotEndAConnectionAParallelOneRenewed(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		h.user("U1", "tang")
		h.f.AddUser("tang-h")
		h.connect(p, "U1", "tang-h")
		var fired atomic.Bool
		h.f.OnRequest(func(method, path string) {
			if fired.Swap(true) {
				return
			}
			// While this request is on its way, another one renews the pair: the token this
			// request carries is no good any more.
			h.f.RevokeAccess("tang-h")
			h.clk.Advance(h.f.AccessTTL - 4*time.Minute)
			if _, err := p.UserToken(ctx, "U1"); err != nil {
				t.Error(err)
			}
		})
		before := h.f.Count("POST", "/")
		if _, _, err := p.Owners(ctx, "U1", ""); err != nil {
			t.Fatalf("a request with the old token ended the connection: %v", err)
		}
		if n := h.f.Count("POST", "/") - before; n != 1 {
			t.Fatalf("%d renewals, want the one", n)
		}
		row, err := store.GetRepoLink(ctx, h.st.Q(), "main", "U1")
		if err != nil || row.State != "ok" || row.RefreshEnc == "" || row.Version != 2 {
			t.Fatalf("%+v %v", row, err)
		}
	})
}

// --- the administration ------------------------------------------------------------

func TestTwoAdministratorsSavesDoNotOverwriteEachOther(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		slow := make(chan struct{})
		started := make(chan struct{})
		var once sync.Once
		h.f.OnRequest(func(method, path string) {
			once.Do(func() { close(started); <-slow })
		})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := p.AdminSave(ctx, []byte(`{"name":"First"}`)); err != nil {
				t.Error(err)
			}
		}()
		<-started
		go func() {
			defer wg.Done()
			// Taken while the first is checking its host, with the name it had.
			if _, err := p.AdminSave(ctx, []byte(`{"clientSecret":"`+h.f.ClientSecret+`"}`)); err != nil {
				t.Error(err)
			}
		}()
		time.Sleep(100 * time.Millisecond)
		close(slow)
		wg.Wait()
		row, err := store.GetConnProviderRow(ctx, h.st.Q(), "main")
		if err != nil || row.Name != "First" || p.Descriptor().Name != "First" {
			t.Fatalf("a stale copy was written back: %q %q %v", row.Name, p.Descriptor().Name, err)
		}
	})
}

func TestACheckThatStartedBeforeANewClientDoesNotJudgeIt(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		slow := make(chan struct{})
		started := make(chan struct{})
		var once sync.Once
		h.f.OnRequest(func(method, path string) {
			once.Do(func() { close(started); <-slow })
		})
		done := make(chan error, 1)
		go func() { _, err := p.AdminCheck(ctx); done <- err }()
		<-started
		// While the check waits for the host, an administrator gives the provider another client.
		h.f.OnRequest(nil)
		h.f.ClientID, h.f.ClientSecret = "another-client-id", "another-client-secret-0123456789"
		if _, err := p.AdminSave(ctx, []byte(`{"clientId":"another-client-id","clientSecret":"another-client-secret-0123456789"}`)); err != nil {
			t.Fatal(err)
		}
		close(slow)
		<-done // the old client is refused: that says nothing of the new one
		row, err := store.GetConnProviderRow(ctx, h.st.Q(), "main")
		if err != nil || row.ClientID != "another-client-id" || row.State != "ok" {
			t.Fatalf("%+v %v", row, err)
		}
		if !p.Descriptor().Capabilities.ConnectAccount || !configured(t, p) {
			t.Fatal("the stale verdict took the provider out of use")
		}
	})
}

func configured(t *testing.T, p *forge.Provider) bool {
	t.Helper()
	in, err := p.Info(ctx, "nobody")
	if err != nil {
		t.Fatal(err)
	}
	return in.Configured
}

func TestAReloadCannotBringBackAProviderRemovedMeanwhile(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		h.add("gone")
		forge.SetReloadHook(nil)
		defer forge.SetReloadHook(nil)
		// A second process has the provider; this one lost it from memory (as a Reload sees it).
		read := make(chan struct{})
		release := make(chan struct{})
		var once sync.Once
		forge.SetReloadHook(func() { once.Do(func() { close(read); <-release }) })
		reloaded := make(chan error, 1)
		go func() { reloaded <- h.mgr.Reload(ctx) }()
		<-read
		removed := make(chan error, 1)
		go func() { removed <- h.prov("gone").(*forge.Provider).AdminDelete(ctx) }()
		// A removal that does not wait for the Reload is over before the Reload goes on.
		var rerr error
		finished := false
		select {
		case rerr = <-removed:
			finished = true
		case <-time.After(300 * time.Millisecond):
		}
		close(release)
		if err := <-reloaded; err != nil {
			t.Fatal(err)
		}
		if !finished {
			select {
			case rerr = <-removed:
			case <-time.After(5 * time.Second):
				t.Fatal("the removal never finished")
			}
		}
		if rerr != nil {
			t.Fatal(rerr)
		}
		if _, ok := h.reg.Get("gone"); ok {
			t.Fatal("a removed provider is back")
		}
		if _, err := store.GetConnProviderRow(ctx, h.st.Q(), "gone"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%v", err)
		}
	})
}

func TestACheckNeverForgetsAVersionItKnew(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		h.user("U1", "tang")
		h.f.AddUser("tang-h")
		h.connect(p, "U1", "tang-h")
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
		if version() != want {
			t.Fatalf("%q", version())
		}
		if _, err := p.AdminCheck(ctx); err != nil {
			t.Fatal(err)
		}
		if version() != want {
			t.Fatalf("a check left %q", version())
		}
		if _, err := p.AdminSave(ctx, []byte(`{"name":"Renamed"}`)); err != nil {
			t.Fatal(err)
		}
		if version() != want {
			t.Fatalf("a save left %q", version())
		}
		row, _ := store.GetConnProviderRow(ctx, h.st.Q(), "main")
		if row.PlatformVersion != want {
			t.Fatalf("stored %q", row.PlatformVersion)
		}
	})
}

func TestTokensOfAReplacedApplicationAreNotUsed(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		h.user("U1", "tang")
		h.f.AddUser("tang-h")
		h.connect(p, "U1", "tang-h")
		if _, err := p.UserToken(ctx, "U1"); err != nil {
			t.Fatal(err)
		}
		// Another application on the same host.
		h.f.ClientID, h.f.ClientSecret = "another-client-id", "another-client-secret-0123456789"
		if _, err := p.AdminSave(ctx, []byte(`{"clientId":"another-client-id","clientSecret":"another-client-secret-0123456789"}`)); err != nil {
			t.Fatal(err)
		}
		if _, err := p.UserToken(ctx, "U1"); !errors.Is(err, forge.ErrAuthorizationExpired) {
			t.Fatalf("a token of the old application was used: %v", err)
		}
		row, _ := store.GetRepoLink(ctx, h.st.Q(), "main", "U1")
		if row.State != "expired" || row.AccessEnc != "" || row.RefreshEnc != "" {
			t.Fatalf("%+v", row)
		}
		// The person connects again, to the new one.
		h.connect(p, "U1", "tang-h")
		if _, err := p.UserToken(ctx, "U1"); err != nil {
			t.Fatal(err)
		}
		// A new secret of the same application keeps the connections.
		h.f.ClientSecret = "rotated-client-secret-0123456789"
		if _, err := p.AdminSave(ctx, []byte(`{"clientSecret":"rotated-client-secret-0123456789"}`)); err != nil {
			t.Fatal(err)
		}
		if _, err := p.UserToken(ctx, "U1"); err != nil {
			t.Fatalf("a new secret ended the connections: %v", err)
		}
	})
}

// --- accounts and renewals ------------------------------------------------------------

func TestAGiteaAdministratorsConnectionIsMarked(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		p := h.add("main").(*forge.Provider)
		h.user("U1", "tang")
		h.user("U2", "wu")
		h.f.AddUser("tang-h")
		h.f.AddUser("wu-h")
		h.f.SetAdmin("tang-h", true)
		h.connect(p, "U1", "tang-h")
		h.connect(p, "U2", "wu-h")
		in1, _ := p.Info(ctx, "U1")
		in2, _ := p.Info(ctx, "U2")
		if in1.Connection.Admin != (h.kind == "gitea") || in2.Connection.Admin {
			t.Fatalf("%+v %+v", in1.Connection, in2.Connection)
		}
		// The mark stays when the tokens are renewed.
		h.clk.Advance(h.f.AccessTTL)
		if _, err := p.UserToken(ctx, "U1"); err != nil {
			t.Fatal(err)
		}
		if in, _ := p.Info(ctx, "U1"); in.Connection.Admin != (h.kind == "gitea") {
			t.Fatalf("%+v", in.Connection)
		}
	})
}

func TestRenewalFollowsTheTokensOwnLifeNotAFixedWindow(t *testing.T) {
	each(t, func(t *testing.T, h *harness) {
		if h.kind != "gitea" {
			t.Skip("GitLab's refresh tokens do not end")
		}
		p := h.add("main").(*forge.Provider)
		h.user("U1", "tang")
		h.f.AddUser("tang-h")
		h.connect(p, "U1", "tang-h")
		// A refresh token that lives 730 hours is renewed in the last tenth of its
		// life (73 hours), not from 30 days out, as a fixed window did.
		for _, d := range []time.Duration{24 * time.Hour, 624 * time.Hour} { // 706, then 82 hours to go
			h.clk.Advance(d)
			if n := h.mgr.Tokens().RenewDue(ctx); n != 0 {
				t.Fatalf("%d renewed with %v gone", n, d)
			}
		}
		h.clk.Advance(10 * time.Hour) // 72 hours to go
		if n := h.mgr.Tokens().RenewDue(ctx); n != 1 {
			t.Fatalf("%d renewed in the last tenth", n)
		}
		h.clk.Advance(48 * time.Hour)
		if n := h.mgr.Tokens().RenewDue(ctx); n != 0 {
			t.Fatalf("%d renewed two days after a renewal", n)
		}
	})
}
