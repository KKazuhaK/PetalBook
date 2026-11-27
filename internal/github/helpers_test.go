package github_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"petalbook.app/server/internal/connections"
	"petalbook.app/server/internal/github"
	"petalbook.app/server/internal/githubapp"
	"petalbook.app/server/internal/githubapp/githubapptest"
	"petalbook.app/server/internal/secret"
	"petalbook.app/server/internal/store"
)

var ctx = context.Background()

const publicURL = "https://notes.example.com"

// lockedBuffer is a log sink the tests can read safely.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuffer) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

type harness struct {
	t    *testing.T
	st   *store.Store
	f    *githubapptest.Fake
	clk  *githubapptest.Clock
	svc  *github.Service
	box  *secret.Box
	logs *lockedBuffer
	now  time.Time
}

func newHarness(t *testing.T, st *store.Store) *harness {
	t.Helper()
	clk := githubapptest.NewClock()
	f := githubapptest.New(t, clk)
	c, err := githubapp.New(githubapp.Config{API: f.URL, Web: f.URL, Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	box := secret.NewBoxWithKey([32]byte{1, 2, 3})
	logs := &lockedBuffer{}
	svc, err := github.New(github.Options{Store: st, Secrets: box, Client: c, PublicURL: publicURL, Now: clk.Now,
		Logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})), SettingsTTL: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, st: st, f: f, clk: clk, svc: svc, box: box, logs: logs, now: clk.Now()}
}

// configure saves the fake's App, with client id and secret.
func (h *harness) configure() *github.AppView {
	h.t.Helper()
	id := h.f.ClientID
	v, err := h.svc.Save(ctx, github.SaveRequest{AppID: "424242", PrivateKey: h.f.KeyPEM(), ClientID: &id, ClientSecret: h.f.ClientSecret})
	if err != nil {
		h.t.Fatal(err)
	}
	return v
}

func (h *harness) user(id, username string) {
	h.t.Helper()
	err := store.InsertUser(ctx, h.st.Q(), store.User{ID: id, Username: username, Name: username, Role: "member", Created: h.now})
	if err != nil {
		h.t.Fatal(err)
	}
}

// tenantUser makes a user in a tenant of their own (made if need be).
func (h *harness) tenantUser(tenant, id, username string) {
	h.t.Helper()
	if _, err := store.GetTenant(ctx, h.st.Q(), tenant); err != nil {
		if err := h.st.CreateTenant(ctx, store.Tenant{ID: tenant, Slug: strings.ToLower(tenant), Name: tenant, Created: h.now}); err != nil {
			h.t.Fatal(err)
		}
	}
	if err := store.InsertUser(ctx, h.st.Q(), store.User{ID: id, TenantID: tenant, Username: username, Name: username, Role: "member", Created: h.now}); err != nil {
		h.t.Fatal(err)
	}
}

// space makes a space with a github remote row, as the manager would.
func (h *harness) space(id string, r store.Remote) {
	h.t.Helper()
	if err := store.InsertSpace(ctx, h.st.Q(), store.DefaultTenantID, id, id, "", "", h.now); err != nil {
		h.t.Fatal(err)
	}
	r.SpaceID, r.URL, r.Branch, r.CredType, r.State, r.Updated = id, "https://github.com/x/y.git", "main", "connection", "ok", h.now
	if err := store.PutRemote(ctx, h.st.Q(), r); err != nil {
		h.t.Fatal(err)
	}
}

// connect plays a browser that connects the PetalBook user to the GitHub login.
func (h *harness) connect(userID, login string) string {
	h.t.Helper()
	authURL, binding, err := h.svc.StartAuthorization(ctx, userID, "/s/ABC/settings")
	if err != nil {
		h.t.Fatal(err)
	}
	back := h.f.Authorize(authURL, login)
	u, _ := url.Parse(back)
	to, clear := h.svc.CompleteAuthorization(ctx, binding, userID, u.Query())
	if !clear {
		h.t.Fatal("the binding was not cleared")
	}
	return to
}

func code(err error) string {
	var he *connections.Error
	if errors.As(err, &he) {
		return he.Code
	}
	return ""
}

func status(err error) int {
	var he *connections.Error
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

func (h *harness) mustNotLeak(secrets ...string) {
	h.t.Helper()
	logs := h.logs.String()
	for _, s := range append(secrets, h.f.AllTokens()...) {
		if len(s) > 6 && strings.Contains(logs, s) {
			h.t.Fatalf("a secret reached the log: %q", s)
		}
	}
	for _, s := range []string{h.f.KeyPEM(), h.f.ClientSecret} {
		if strings.Contains(logs, s) {
			h.t.Fatal("the key or the client secret reached the log")
		}
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, _ := c.Output()
	return strings.TrimSpace(string(out))
}

// allowAll lends the hooks of a caller who may connect any installation.
func allowAll(c context.Context) context.Context {
	return connections.WithInstallHooks(c, connections.InstallHooks{Allow: func(context.Context, store.InstallOwner) error { return nil }})
}

// installFor plays a browser of userID that installs the App for owner o
// (account: the GitHub organisation named for a tenant): GitHub login installs
// it on the account on, authorising during installation when authorize. It
// answers where the callback sends the browser.
func (h *harness) installFor(userID string, o store.InstallOwner, account, login, on string, authorize bool) string {
	h.t.Helper()
	hc := allowAll(ctx)
	u, binding, err := h.svc.StartInstallFor(hc, userID, o, account, "/back")
	if err != nil {
		h.t.Fatal(err)
	}
	back, _ := url.Parse(h.f.InstallAppOn(u, login, on, h.svc.CallbackURL(), authorize))
	to, _ := h.svc.CompleteAuthorization(hc, binding, userID, back.Query())
	return to
}

// installMine installs the App on the person's own GitHub account (login) and
// connects it to them; it answers the installation's id.
func (h *harness) installMine(userID, login string) int64 {
	h.t.Helper()
	if to := h.installFor(userID, store.InstallOwner{UserID: userID}, "", login, login, true); to != "/back" {
		h.t.Fatalf("installing on %s: %s", login, to)
	}
	return h.claimed(store.InstallOwner{UserID: userID}, login)
}

// installOrg has admin (GitHub login, a member of org) install the App on
// the organisation org for tenant; it answers the installation's id.
func (h *harness) installOrg(admin, tenant, org, login string) int64 {
	h.t.Helper()
	h.f.MakeAdmin(org, login) // an owner of the organisation installs it
	if to := h.installFor(admin, store.InstallOwner{TenantID: tenant}, org, login, org, true); to != "/back" {
		h.t.Fatalf("installing on %s: %s", org, to)
	}
	return h.claimed(store.InstallOwner{TenantID: tenant}, org)
}

func (h *harness) claimed(o store.InstallOwner, account string) int64 {
	h.t.Helper()
	in, err := store.GitHubInstallationOwnedOn(ctx, h.st.Q(), o, account)
	if err != nil {
		h.t.Fatalf("no installation of %+v on %s: %v", o, account, err)
	}
	return in.ID
}
