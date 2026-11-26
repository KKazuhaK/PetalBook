package forge_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"petalbook.app/server/internal/connections"
	"petalbook.app/server/internal/forge"
	"petalbook.app/server/internal/forge/forgetest"
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

// kinds are the two platforms every test runs against.
var kinds = []string{"gitlab", "gitea"}

type harness struct {
	t    *testing.T
	st   *store.Store
	f    *forgetest.Fake
	clk  *forgetest.Clock
	reg  *connections.Registry
	mgr  *forge.Manager
	box  *secret.Box
	logs *lockedBuffer
	kind string
	now  time.Time
	opt  forge.Options
}

func newFake(t *testing.T, kind string, clk forgetest.Clocker) *forgetest.Fake {
	if kind == "gitlab" {
		return forgetest.NewGitLab(t, clk)
	}
	return forgetest.NewGitea(t, clk)
}

// newHarness makes a manager over the store, unsafe (the fake is on loopback, plain http).
func newHarness(t *testing.T, st *store.Store, kind string) *harness {
	t.Helper()
	clk := forgetest.NewClock()
	f := newFake(t, kind, clk)
	box := secret.NewBoxWithKey([32]byte{7, 8, 9})
	logs := &lockedBuffer{}
	reg := connections.NewRegistry(connections.NewGit())
	opt := forge.Options{Store: st, Secrets: box, Registry: reg, PublicURL: publicURL, Now: clk.Now, Unsafe: true,
		Logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	mgr, err := forge.NewManager(ctx, opt)
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, st: st, f: f, clk: clk, reg: reg, mgr: mgr, box: box, logs: logs, kind: kind, now: clk.Now(), opt: opt}
}

func (h *harness) body(id string, extra map[string]any) []byte { return h.bodyFor(h.f, id, extra) }

func (h *harness) bodyFor(f *forgetest.Fake, id string, extra map[string]any) []byte {
	m := map[string]any{"id": id, "type": h.kind, "host": f.HostURL(), "name": "Test " + id, "clientId": f.ClientID, "clientSecret": f.ClientSecret}
	for k, v := range extra {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return b
}

// add sets a provider up as an administrator would.
func (h *harness) add(id string) connections.Provider {
	h.t.Helper()
	p, _, err := h.mgr.CreateProvider(ctx, "", h.body(id, nil))
	if err != nil {
		h.t.Fatal(err)
	}
	return p
}

func (h *harness) user(id, username string) {
	h.t.Helper()
	if err := store.InsertUser(ctx, h.st.Q(), store.User{ID: id, Username: username, Name: username, Role: "member", Created: h.now}); err != nil {
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

// connect plays a browser that connects the PetalBook user to the login on the host.
func (h *harness) connect(p connections.Provider, userID, login string) string {
	h.t.Helper()
	st, err := p.StartConnect(ctx, userID, connections.StartRequest{Return: "/s/ABC/settings"})
	if err != nil {
		h.t.Fatal(err)
	}
	back := h.f.Authorize(st.URL, login)
	u, _ := url.Parse(back)
	to, clear := p.CompleteConnect(ctx, st.Binding, userID, u.Query())
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
	if strings.Contains(logs, h.f.ClientSecret) {
		h.t.Fatal("the client secret reached the log")
	}
}

func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

func (h *harness) prov(id string) connections.Provider {
	h.t.Helper()
	p, ok := h.reg.Get(id)
	if !ok {
		h.t.Fatalf("no provider %q", id)
	}
	return p
}
