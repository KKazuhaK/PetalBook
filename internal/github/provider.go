package github

// The GitHub provider: Service as a connections.Provider, and the parts of it
// that only some providers have (an administrator's App, installations, the
// App made in one click).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"petalbook.app/server/internal/connections"
	"petalbook.app/server/internal/store"
)

var (
	_ connections.Provider      = (*Service)(nil)
	_ connections.Administered  = (*Service)(nil)
	_ connections.Installable   = (*Service)(nil)
	_ connections.ManifestMaker = (*Service)(nil)
)

// Descriptor says what this provider is.
func (s *Service) Descriptor() connections.Descriptor {
	return connections.Descriptor{ID: ProviderID, Type: "github", Host: s.c.Host(), Name: "GitHub", Owner: "site",
		Capabilities: connections.Capabilities{CreateRepo: true, Installations: true, ConnectAccount: true, StorageTarget: true, PartialSync: "yes"}}
}

// StartConnect begins connecting a person's GitHub account: through the App's
// installation page (which authorises them as well) or the authorisation page alone.
func (s *Service) StartConnect(ctx context.Context, userID string, req connections.StartRequest) (*connections.Start, error) {
	var u, binding string
	var err error
	if req.Install {
		u, binding, err = s.StartInstall(ctx, userID, req.Return)
	} else {
		u, binding, err = s.StartAuthorization(ctx, userID, req.Return)
	}
	if err != nil {
		return nil, err
	}
	return &connections.Start{URL: u, Binding: binding}, nil
}

// CompleteConnect finishes connecting from GitHub's redirect.
func (s *Service) CompleteConnect(ctx context.Context, binding, userID string, q url.Values) (string, bool) {
	return s.CompleteAuthorization(ctx, binding, userID, q)
}

// RepoURL is the address of a repository on this GitHub.
func (s *Service) RepoURL(owner, name string) string { return s.c.RepoURL(owner, name) }

// ValidRepoName checks a repository name: what GitHub keeps as it is.
func (s *Service) ValidRepoName(name string) bool { return ValidRepoName(name) }

// Check reads the App and its installations from GitHub again.
func (s *Service) Check(ctx context.Context) error {
	_, err := s.Recheck(ctx)
	return err
}

// --- what an administrator sees -----------------------------------------------------

type adminOff struct {
	Configured  bool   `json:"configured"`
	CallbackURL string `json:"callbackUrl"`
}

type adminView struct {
	Configured bool   `json:"configured"`
	AppID      string `json:"appId"`
	ClientID   string `json:"clientId"`
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	Owner      string `json:"owner"`
	HTMLURL    string `json:"htmlUrl"`
	InstallURL string `json:"installUrl"`
	PrivateKey struct {
		Set         bool   `json:"set"`
		Fingerprint string `json:"fingerprint"`
	} `json:"privateKey"`
	ClientSecret struct {
		Set bool `json:"set"`
	} `json:"clientSecret"`
	CallbackURL       string            `json:"callbackUrl"`
	Permissions       map[string]string `json:"permissions"`
	Missing           []string          `json:"missing"`
	UserAuthorization bool              `json:"userAuthorization"`
	Checked           string            `json:"checked"`
	Spaces            int               `json:"spaces"`
	Authorizations    int               `json:"authorizations"`
	// Installations that lack a permission, from the last check.
	Gaps []adminGap `json:"gaps"`
	// Whether the App can be installed on any account; null when GitHub did not say.
	AnyAccount *bool `json:"anyAccount"`
}

type adminGap struct {
	InstallationID int64    `json:"installationId"`
	Account        string   `json:"account"`
	Missing        []string `json:"missing"`
}

func toAdminView(v *AppView) any {
	if !v.Configured {
		return adminOff{CallbackURL: v.CallbackURL}
	}
	r := adminView{Configured: true, AppID: v.AppID, ClientID: v.ClientID, Slug: v.Slug, Name: v.Name, Owner: v.Owner,
		HTMLURL: v.HTMLURL, InstallURL: v.InstallURL, CallbackURL: v.CallbackURL, Permissions: v.Permissions, Missing: v.Missing,
		UserAuthorization: v.UserAuthorizaton, Checked: v.Checked.UTC().Format("2006-01-02T15:04:05Z"), Spaces: v.Spaces, Authorizations: v.Authorizations}
	r.AnyAccount = v.AnyAccount
	r.Gaps = make([]adminGap, len(v.Gaps))
	for i, g := range v.Gaps {
		r.Gaps[i] = adminGap{g.ID, g.Account, g.Missing}
	}
	r.PrivateKey.Set, r.PrivateKey.Fingerprint = v.KeySet, v.Fingerprint
	r.ClientSecret.Set = v.SecretSet
	if r.Missing == nil {
		r.Missing = []string{}
	}
	if r.Permissions == nil {
		r.Permissions = map[string]string{}
	}
	return r
}

// AdminView is the App's settings as an administrator sees them.
func (s *Service) AdminView(ctx context.Context) (any, error) {
	v, err := s.View(ctx)
	if err != nil {
		return nil, err
	}
	return toAdminView(v), nil
}

// AdminSave saves the App entered by hand: {appId, privateKey, clientId, clientSecret}.
func (s *Service) AdminSave(ctx context.Context, body []byte) (any, error) {
	var req struct {
		AppID        string  `json:"appId"`
		PrivateKey   string  `json:"privateKey"`
		ClientID     *string `json:"clientId"`
		ClientSecret string  `json:"clientSecret"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, errf(http.StatusBadRequest, CodeInvalid, "The body is not what this takes: %s.", err.Error())
	}
	v, err := s.Save(ctx, SaveRequest{AppID: req.AppID, PrivateKey: req.PrivateKey, ClientID: req.ClientID, ClientSecret: req.ClientSecret})
	if err != nil {
		return nil, err
	}
	return toAdminView(v), nil
}

// AdminCheck reads the App again from GitHub.
func (s *Service) AdminCheck(ctx context.Context) (any, error) {
	v, err := s.Recheck(ctx)
	if err != nil {
		return nil, err
	}
	return toAdminView(v), nil
}

// AdminDelete removes the App.
func (s *Service) AdminDelete(ctx context.Context) error { return s.Delete(ctx) }

// AdminCounts is how many people are connected and spaces sync through the App.
func (s *Service) AdminCounts(ctx context.Context) (int, int, error) {
	q := s.opt.Store.Q()
	auths, err := store.CountGitHubAuthorizations(ctx, q)
	if err != nil {
		return 0, 0, err
	}
	spaces, err := store.CountGitHubSpaces(ctx, q)
	return auths, spaces, err
}

type installationView struct {
	ID      int64 `json:"id"`
	Account struct {
		Login string `json:"login"`
		Type  string `json:"type"`
		ID    int64  `json:"id"`
	} `json:"account"`
	RepositorySelection string            `json:"repositorySelection"`
	Suspended           bool              `json:"suspended"`
	Permissions         map[string]string `json:"permissions"`
	HTMLURL             string            `json:"htmlUrl"`
	Tenants             []string          `json:"tenants"`
	Spaces              int               `json:"spaces"`
}

func toInstallationView(i Installation) installationView {
	r := installationView{ID: i.ID, RepositorySelection: i.RepositorySelection, Suspended: i.Suspended, Permissions: i.Permissions,
		HTMLURL: i.HTMLURL, Tenants: i.Tenants, Spaces: i.Spaces}
	r.Account.Login, r.Account.Type, r.Account.ID = i.Account.Login, i.Account.Type, i.Account.ID
	if r.Permissions == nil {
		r.Permissions = map[string]string{}
	}
	if r.Tenants == nil {
		r.Tenants = []string{}
	}
	return r
}

// InstallationsView is Installations in the shape the API shows.
func (s *Service) InstallationsView(ctx context.Context, cursor string) (any, string, error) {
	items, next, err := s.Installations(ctx, cursor)
	if err != nil {
		return nil, "", err
	}
	out := make([]installationView, len(items))
	for i, it := range items {
		out[i] = toInstallationView(it)
	}
	return out, next, nil
}

// SetInstallationTenants shares an installation with exactly these tenants.
func (s *Service) SetInstallationTenants(ctx context.Context, id int64, tenants []string) (any, error) {
	it, err := s.SetTenants(ctx, id, tenants)
	if err != nil {
		return nil, err
	}
	return toInstallationView(*it), nil
}
