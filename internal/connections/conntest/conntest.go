// Package conntest is the contract every connections.Provider keeps: the same
// checks run against the fake provider in this package, against the GitHub
// implementation, and (milestone 3.8) against GitLab and Gitea.
package conntest

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/KKazuhaK/PetalBook/internal/connections"
)

// Harness is what the checks need of a provider under test.
type Harness struct {
	// Provider is a provider that is set up, as far as it can be without anyone
	// being connected.
	Provider connections.Provider
	// Unconfigured is a provider of the same kind with nothing set up, or nil
	// for a kind that has no such state (the provider "git").
	Unconfigured connections.Provider
	// User is the id of a person who has no connection.
	User string
}

var idRe = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)

var ctx = context.Background()

// Run checks the contract.
func Run(t *testing.T, h Harness) {
	t.Helper()
	t.Run("describes itself", func(t *testing.T) { describes(t, h) })
	t.Run("a person without a connection", func(t *testing.T) { noConnection(t, h) })
	t.Run("addresses and names", func(t *testing.T) { addresses(t, h) })
	if h.Unconfigured != nil {
		t.Run("unconfigured", func(t *testing.T) { unconfigured(t, h) })
	}
	t.Run("errors name their provider", func(t *testing.T) { errorsNameTheirProvider(t, h) })
	t.Run("is told when something is fixed", func(t *testing.T) {
		h.Provider.OnChange(func(string) {}) // registering must not fail or block
	})
}

func describes(t *testing.T, h Harness) {
	d := h.Provider.Descriptor()
	if !idRe.MatchString(d.ID) || d.Type == "" || d.Name == "" || d.Owner == "" {
		t.Fatalf("%+v", d)
	}
	if d.Capabilities.CreateRepo && d.Host == "" {
		t.Fatalf("a provider that creates repositories has a host: %+v", d)
	}
	switch d.Capabilities.PartialSync {
	case "yes", "no", "unknown":
	default:
		t.Fatalf("partialSync is %q", d.Capabilities.PartialSync)
	}
	if reg := connections.NewRegistry(h.Provider); reg == nil {
		t.Fatal("no registry")
	} else if p, ok := reg.Get(d.ID); !ok || p != h.Provider {
		t.Fatal("the registry does not find the provider by its id")
	}
}

func noConnection(t *testing.T, h Harness) {
	in, err := h.Provider.Info(ctx, h.User)
	if err != nil {
		t.Fatal(err)
	}
	if in.Connection != nil || in.Leftovers == nil || len(in.Leftovers) != 0 {
		t.Fatalf("%+v", in)
	}
	d := h.Provider.Descriptor()
	if d.Type == connections.GitID {
		// A plain address has nothing to connect: its routes are 404 not_found.
		for name, err := range map[string]error{
			"disconnect": h.Provider.Disconnect(ctx, h.User),
			"dismiss":    h.Provider.DismissLeftover(ctx, h.User, 424242),
		} {
			var ce *connections.Error
			if !errors.As(err, &ce) || ce.Status != 404 || ce.Code != connections.CodeNotFound || ce.Provider != d.ID {
				t.Fatalf("%s on a provider without connections: %v", name, err)
			}
		}
	} else {
		// Disconnecting nothing, and dismissing a reminder there is not, are fine.
		if err := h.Provider.Disconnect(ctx, h.User); err != nil {
			t.Fatal(err)
		}
		if err := h.Provider.DismissLeftover(ctx, h.User, 424242); err != nil {
			t.Fatal(err)
		}
		// A callback that matches no attempt changes nothing and still redirects.
		if to, _ := h.Provider.CompleteConnect(ctx, "no-such-binding", h.User, nil); to == "" || to[0] != '/' {
			t.Fatalf("the callback redirects to %q", to)
		}
	}
	// Reading a list never fails for a person who has nothing.
	if d.Type == connections.GitID {
		items, next, err := h.Provider.Repos(ctx, h.User, connections.RepoQuery{Mine: true})
		if err != nil || items == nil || len(items) != 0 || next != "" {
			t.Fatalf("%v %q %v", items, next, err)
		}
	}
}

func addresses(t *testing.T, h Harness) {
	d := h.Provider.Descriptor()
	if !d.Capabilities.CreateRepo {
		if _, _, ok := h.Provider.ParseRepoURL("https://example.test/a/b.git"); ok {
			t.Fatal("a provider that makes no repositories reads no address")
		}
		return
	}
	u := h.Provider.RepoURL("tang", "notes")
	if !strings.Contains(u, d.Host) || !strings.HasSuffix(u, "/tang/notes.git") {
		t.Fatalf("%q", u)
	}
	if o, n, ok := h.Provider.ParseRepoURL(u); !ok || o != "tang" || n != "notes" {
		t.Fatalf("%q read as %q %q %v", u, o, n, ok)
	}
	for _, bad := range []string{"", "not a url", "https://elsewhere.invalid/tang/notes.git", "ftp://" + d.Host + "/tang/notes.git"} {
		if _, _, ok := h.Provider.ParseRepoURL(bad); ok {
			t.Fatalf("%q is read as an address of this provider", bad)
		}
	}
	for _, good := range []string{"notes", "my-notes_2.0", "a"} {
		if !h.Provider.ValidRepoName(good) {
			t.Fatalf("%q is refused as a name", good)
		}
	}
	for _, bad := range []string{"", " ", "a b", "..", ".", "x.git", "a/b", strings.Repeat("a", 101)} {
		if h.Provider.ValidRepoName(bad) {
			t.Fatalf("%q is accepted as a name", bad)
		}
	}
}

// isProviderError says err is a *connections.Error of the provider.
func isProviderError(err error, id string) (*connections.Error, bool) {
	var ce *connections.Error
	if errors.As(err, &ce) && ce.Provider == id && ce.Code != "" && ce.Status >= 400 {
		return ce, true
	}
	return nil, false
}

func unconfigured(t *testing.T, h Harness) {
	p := h.Unconfigured
	id := p.Descriptor().ID
	in, err := p.Info(ctx, h.User)
	if err != nil || in.Configured || in.Connection != nil {
		t.Fatalf("%+v %v", in, err)
	}
	if _, err := p.StartConnect(ctx, h.User, connections.StartRequest{Return: "/"}); !hasCode(err, id, connections.CodeNotConfigured) {
		t.Fatalf("start: %v", err)
	}
	if _, _, err := p.Owners(ctx, h.User, ""); !hasCode(err, id, connections.CodeNotConfigured) {
		t.Fatalf("owners: %v", err)
	}
	if _, _, err := p.Repos(ctx, h.User, connections.RepoQuery{Mine: true}); !hasCode(err, id, connections.CodeNotConfigured) {
		t.Fatalf("repos: %v", err)
	}
	if _, err := p.PlanCreate(ctx, h.User, "tang"); !hasCode(err, id, connections.CodeNotConfigured) {
		t.Fatalf("plan: %v", err)
	}
}

func hasCode(err error, id, code string) bool {
	ce, ok := isProviderError(err, id)
	return ok && ce.Code == code
}

func errorsNameTheirProvider(t *testing.T, h Harness) {
	d := h.Provider.Descriptor()
	if !d.Capabilities.CreateRepo {
		return
	}
	// A repository that is not there, and an owner nobody may create in, are
	// refused with the provider's own errors.
	if _, _, err := h.Provider.ResolveExistingIn(ctx, "no-tenant", h.User, "nobody-at-all", "nothing", ""); err == nil {
		t.Fatal("a repository that does not exist was resolved")
	} else if _, ok := isProviderError(err, d.ID); !ok {
		t.Fatalf("%T %v is not an error of the provider", err, err)
	}
	if _, err := h.Provider.PlanCreate(ctx, h.User, "nobody-at-all"); err == nil {
		t.Fatal("planned a repository for an owner nobody may create in")
	} else if _, ok := isProviderError(err, d.ID); !ok {
		t.Fatalf("%T %v is not an error of the provider", err, err)
	}
}
