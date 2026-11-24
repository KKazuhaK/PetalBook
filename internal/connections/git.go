package connections

import (
	"context"
	"net/http"
	"net/url"

	"github.com/KKazuhaK/PetalBook/internal/gitstore"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// Git is the provider "git": a plain Git address with a deploy key or a token.
// It has no connection, no owners and no repositories to list; a space's
// credential is its own (the credential types ssh and token), so this provider
// only describes itself.
type Git struct{}

// NewGit returns the provider "git".
func NewGit() *Git { return &Git{} }

func (*Git) Descriptor() Descriptor {
	return Descriptor{ID: GitID, Type: GitID, Name: "Git", Owner: "site", Capabilities: Capabilities{PartialSync: "unknown"}}
}

func (*Git) Info(context.Context, string) (*Info, error) {
	return &Info{Configured: true, Leftovers: []Leftover{}}, nil
}

func (g *Git) notFound() *Error {
	return Errorf(GitID, http.StatusNotFound, CodeNotFound, "a plain Git address has nothing to connect")
}

func (g *Git) StartConnect(context.Context, string, StartRequest) (*Start, error) {
	return nil, g.notFound()
}

func (*Git) CompleteConnect(context.Context, string, string, url.Values) (string, bool) {
	return "/", true
}

func (*Git) Disconnect(context.Context, string) error { return nil }

func (*Git) DismissLeftover(context.Context, string, int64) error { return nil }

func (*Git) Owners(context.Context, string, string) ([]Owner, string, error) {
	return nil, "", (&Git{}).notFound()
}

// Repos is an empty list: there is nothing to list at an address.
func (*Git) Repos(context.Context, string, RepoQuery) ([]RepoItem, string, error) {
	return []RepoItem{}, "", nil
}

func (*Git) ParseRepoURL(string) (string, string, bool) { return "", "", false }
func (*Git) RepoURL(string, string) string              { return "" }
func (*Git) ValidRepoName(string) bool                  { return false }

func (g *Git) PlanCreate(context.Context, string, string) (*Plan, error) { return nil, g.notFound() }
func (g *Git) CreateRepo(context.Context, *Plan, NewRepoSpec) (*Repo, error) {
	return nil, g.notFound()
}
func (g *Git) ChooseCredential(context.Context, *Plan, *Repo) (*Cred, error) {
	return nil, g.notFound()
}
func (g *Git) ResolveExistingIn(context.Context, string, string, string, string, string) (*Cred, *Repo, error) {
	return nil, nil, g.notFound()
}
func (*Git) RecordLeftover(context.Context, string, *Repo, string) error { return nil }

func (*Git) GitToken(context.Context, *store.Remote, bool) (string, error) {
	return "", &gitstore.CredentialError{Code: CodeNotConfigured, Msg: "a plain Git address has no connection to take a token from"}
}

func (*Git) Check(context.Context) error { return nil }
func (*Git) OnChange(func(string))       {}
