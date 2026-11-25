package conntest

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"petalbook.app/server/internal/connections"
	"petalbook.app/server/internal/gitstore"
	"petalbook.app/server/internal/store"
)

// Fake is a provider that keeps everything in memory, for the contract's own
// test and for tests of what uses providers. It is a platform at Host with the
// people and repositories a test gives it.
type Fake struct {
	ID, Kind, Name, Host string
	// Configured says the provider is set up; nothing else works without it.
	Configured bool

	mu       sync.Mutex
	conns    map[string]*connections.Connection
	left     map[string][]connections.Leftover
	repos    map[string]*connections.Repo // by "owner/name"
	nextRepo int64
	onChange func(string)
}

// NewFake returns a configured fake provider with the id, of the type "fake".
func NewFake(id string) *Fake {
	return &Fake{ID: id, Kind: "fake", Name: "Fake " + id, Host: id + ".example.test", Configured: true,
		conns: map[string]*connections.Connection{}, left: map[string][]connections.Leftover{}, repos: map[string]*connections.Repo{}}
}

func (f *Fake) err(status int, code, msg string) *connections.Error {
	return connections.Errorf(f.ID, status, code, "%s.", msg)
}

func (f *Fake) need() error {
	if !f.Configured {
		return f.err(http.StatusConflict, connections.CodeNotConfigured, "The fake provider is not set up.")
	}
	return nil
}

// Connect connects a person (a test's shortcut for the browser's round trip).
func (f *Fake) Connect(userID, login string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conns[userID] = &connections.Connection{Login: login, AccountID: "1", State: "ok"}
	if f.onChange != nil {
		f.onChange(userID)
	}
}

// AddRepo makes a repository exist.
func (f *Fake) AddRepo(owner, name string) *connections.Repo {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextRepo++
	r := &connections.Repo{ID: f.nextRepo, Owner: owner, Name: name, FullName: owner + "/" + name, HTMLURL: "https://" + f.Host + "/" + owner + "/" + name, Private: true}
	f.repos[r.FullName] = r
	return r
}

func (f *Fake) Descriptor() connections.Descriptor {
	return connections.Descriptor{ID: f.ID, Type: f.Kind, Host: f.Host, Name: f.Name, Owner: "site",
		Capabilities: connections.Capabilities{CreateRepo: true, ConnectAccount: true, StorageTarget: true, PartialSync: "unknown"}}
}

func (f *Fake) Info(_ context.Context, userID string) (*connections.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	in := &connections.Info{Configured: f.Configured, UserAuthorization: f.Configured, Leftovers: append([]connections.Leftover{}, f.left[userID]...)}
	if c := f.conns[userID]; c != nil {
		cc := *c
		in.Connection = &cc
	}
	return in, nil
}

func (f *Fake) StartConnect(_ context.Context, userID string, req connections.StartRequest) (*connections.Start, error) {
	if err := f.need(); err != nil {
		return nil, err
	}
	return &connections.Start{URL: "https://" + f.Host + "/authorize?state=x", Binding: "b-" + userID}, nil
}

func (f *Fake) CompleteConnect(_ context.Context, binding, userID string, _ url.Values) (string, bool) {
	if binding != "b-"+userID || userID == "" {
		return "/settings?provider=" + f.ID + "&connection_error=expired", false
	}
	f.Connect(userID, "login-"+userID)
	return "/", true
}

func (f *Fake) Disconnect(_ context.Context, userID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.conns, userID)
	return nil
}

func (f *Fake) DismissLeftover(_ context.Context, userID string, repoID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	keep := f.left[userID][:0]
	for _, l := range f.left[userID] {
		if l.RepoID != repoID {
			keep = append(keep, l)
		}
	}
	f.left[userID] = keep
	return nil
}

func (f *Fake) Owners(_ context.Context, userID, _ string) ([]connections.Owner, string, error) {
	if err := f.need(); err != nil {
		return nil, "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if c := f.conns[userID]; c != nil {
		return []connections.Owner{{Login: c.Login, Type: "User", Via: connections.ViaUser}}, "", nil
	}
	return []connections.Owner{}, "", nil
}

func (f *Fake) Repos(_ context.Context, _ string, _ connections.RepoQuery) ([]connections.RepoItem, string, error) {
	if err := f.need(); err != nil {
		return nil, "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []connections.RepoItem{}
	for _, r := range f.repos {
		out = append(out, connections.RepoItem{ID: r.ID, FullName: r.FullName, Private: r.Private, DefaultBranch: "main", HTMLURL: r.HTMLURL, CanPush: true})
	}
	return out, "", nil
}

func (f *Fake) ParseRepoURL(raw string) (string, string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host != f.Host {
		return "", "", false
	}
	o, n, ok := strings.Cut(strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git"), "/")
	if !ok || o == "" || n == "" || strings.Contains(n, "/") {
		return "", "", false
	}
	return o, n, true
}

func (f *Fake) RepoURL(owner, name string) string {
	return "https://" + f.Host + "/" + owner + "/" + name + ".git"
}

func (f *Fake) ValidRepoName(name string) bool {
	return len(name) >= 1 && len(name) <= 100 && name != "." && name != ".." && !strings.HasSuffix(name, ".git") &&
		strings.Trim(name, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-") == ""
}

func (f *Fake) PlanCreate(_ context.Context, userID, owner string) (*connections.Plan, error) {
	if err := f.need(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.conns[userID]
	if c == nil {
		return nil, f.err(http.StatusConflict, connections.CodeNotConnected, "Connect your account first.")
	}
	if !strings.EqualFold(c.Login, owner) {
		return nil, f.err(http.StatusForbidden, connections.CodeOwnerNotAllowed, "You cannot create a repository for "+owner+".")
	}
	return &connections.Plan{Owner: c.Login, Via: connections.ViaUser, UserID: userID}, nil
}

func (f *Fake) CreateRepo(_ context.Context, p *connections.Plan, n connections.NewRepoSpec) (*connections.Repo, error) {
	f.mu.Lock()
	_, taken := f.repos[p.Owner+"/"+n.Name]
	f.mu.Unlock()
	if taken {
		return nil, f.err(http.StatusConflict, connections.CodeNameTaken, p.Owner+" already has a repository with that name.")
	}
	return f.AddRepo(p.Owner, n.Name), nil
}

func (f *Fake) ChooseCredential(_ context.Context, p *connections.Plan, repo *connections.Repo) (*connections.Cred, error) {
	return &connections.Cred{Provider: f.ID, Via: connections.ViaUser, UserID: p.UserID, RepoID: repo.ID}, nil
}

func (f *Fake) ResolveExistingIn(_ context.Context, _, userID, owner, name, _ string) (*connections.Cred, *connections.Repo, error) {
	if err := f.need(); err != nil {
		return nil, nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repos[owner+"/"+name]
	if r == nil {
		return nil, nil, f.err(http.StatusForbidden, connections.CodeOwnerNotAllowed, "Your connection cannot push to "+owner+"/"+name+".")
	}
	return &connections.Cred{Provider: f.ID, Via: connections.ViaUser, UserID: userID, RepoID: r.ID}, r, nil
}

func (f *Fake) RecordLeftover(_ context.Context, userID string, repo *connections.Repo, spaceName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.left[userID] = append([]connections.Leftover{{RepoID: repo.ID, FullName: repo.FullName, HTMLURL: repo.HTMLURL, SpaceName: spaceName}}, f.left[userID]...)
	return nil
}

func (f *Fake) GitToken(_ context.Context, r *store.Remote, _ bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.conns[r.GHUserID] == nil {
		return "", &gitstore.CredentialError{Code: connections.CodeConnectionMissing, Msg: "The person is not connected."}
	}
	return "fake-token", nil
}

func (f *Fake) Check(context.Context) error { return f.need() }

func (f *Fake) OnChange(fn func(string)) {
	f.mu.Lock()
	f.onChange = fn
	f.mu.Unlock()
}
