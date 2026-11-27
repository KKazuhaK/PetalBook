package githubapp

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Account is a user or an organisation.
type Account struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Type  string `json:"type"` // "User" or "Organization"
}

// AppInfo is what GitHub says about the App (GET /app).
type AppInfo struct {
	ID          int64             `json:"id"`
	Slug        string            `json:"slug"`
	Name        string            `json:"name"`
	ClientID    string            `json:"client_id"`
	HTMLURL     string            `json:"html_url"`
	Owner       Account           `json:"owner"`
	Permissions map[string]string `json:"permissions"`
	// Public says whether the App can be installed on any account (nil: GitHub
	// did not say).
	Public *bool `json:"public"`
}

// Installation is one place the App is installed.
type Installation struct {
	ID                  int64             `json:"id"`
	Account             Account           `json:"account"`
	RepositorySelection string            `json:"repository_selection"` // "all" or "selected"
	Permissions         map[string]string `json:"permissions"`
	HTMLURL             string            `json:"html_url"`
	SuspendedAt         *time.Time        `json:"suspended_at"`
}

// Suspended reports whether the installation is suspended.
func (i Installation) Suspended() bool { return i.SuspendedAt != nil }

// Repo is a repository.
type Repo struct {
	ID            int64   `json:"id"`
	Name          string  `json:"name"`
	FullName      string  `json:"full_name"`
	Private       bool    `json:"private"`
	DefaultBranch string  `json:"default_branch"`
	HTMLURL       string  `json:"html_url"`
	Owner         Account `json:"owner"`
	Permissions   struct {
		Push  bool `json:"push"`
		Admin bool `json:"admin"`
	} `json:"permissions"`
}

// NewRepo is what a repository is created with: empty, so the first commit
// pushed is its first commit.
type NewRepo struct {
	Name        string
	Private     bool
	Description string
}

func (n NewRepo) body() map[string]any {
	b := map[string]any{"name": n.Name, "private": n.Private, "auto_init": false}
	if n.Description != "" {
		b["description"] = n.Description
	}
	return b
}

// AppInfo asks GitHub who the App is (GET /app); it is also the check that the
// key and the App id belong together.
func (a *App) AppInfo(ctx context.Context) (*AppInfo, error) {
	var out AppInfo
	if _, err := a.asApp(ctx, http.MethodGet, "/app", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Installations lists the App's installations: one page, and the next page (0
// after the last).
func (a *App) Installations(ctx context.Context, page int) ([]Installation, int, error) {
	var out []Installation
	h, err := a.asApp(ctx, http.MethodGet, "/app/installations", pageQuery(page), nil, &out)
	if err != nil {
		return nil, 0, err
	}
	return out, nextPage(h), nil
}

// Installation is one installation of the App.
func (a *App) Installation(ctx context.Context, id int64) (*Installation, error) {
	var out Installation
	if _, err := a.asApp(ctx, http.MethodGet, "/app/installations/"+strconv.FormatInt(id, 10), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CheckClient asks GitHub to check a made-up token with the App's client id and
// secret: 404 means the pair is right, 401 that it is not.
func (c *Client) CheckClient(ctx context.Context, clientID, secret string) (ok bool, err error) {
	if clientID == "" || strings.ContainsAny(clientID, "/ ?#") {
		return false, nil
	}
	_, err = c.do(ctx, request{method: http.MethodPost, base: c.api, path: "/applications/" + clientID + "/token",
		basicUser: clientID, basicPw: secret, body: map[string]string{"access_token": "petalbook-check-0000000000000000"}}, nil)
	switch {
	case err == nil:
		return true, nil // GitHub knew the token: the pair is right all the same
	case IsStatus(err, http.StatusNotFound, http.StatusUnprocessableEntity):
		return true, nil
	case IsStatus(err, http.StatusUnauthorized, http.StatusForbidden) && !isLimited(err):
		return false, nil
	}
	return false, err
}

func isLimited(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.RateLimited
}

// --- calls with an installation or user token ------------------------------------

func (c *Client) bearer(ctx context.Context, token, method, path string, query url.Values, body, out any) (http.Header, error) {
	return c.do(ctx, request{method: method, base: c.api, path: path, query: query, bearer: token, body: body}, out)
}

// CreateRepo creates a repository: in the organisation org, or, when org is
// empty, in the account of the user the token belongs to.
func (c *Client) CreateRepo(ctx context.Context, token, org string, n NewRepo) (*Repo, error) {
	path := "/user/repos"
	if org != "" {
		if !validName(org) {
			return nil, errors.New("github: unusable organisation name")
		}
		path = "/orgs/" + org + "/repos"
	}
	var out Repo
	if _, err := c.bearer(ctx, token, http.MethodPost, path, nil, n.body(), &out); err != nil {
		return nil, err
	}
	if out.ID == 0 || out.FullName == "" {
		return nil, errors.New("github: the repository answer is incomplete")
	}
	return &out, nil
}

// GetRepo reads one repository.
func (c *Client) GetRepo(ctx context.Context, token, owner, name string) (*Repo, error) {
	if !validName(owner) || !validName(name) {
		return nil, errors.New("github: unusable repository name")
	}
	var out Repo
	if _, err := c.bearer(ctx, token, http.MethodGet, "/repos/"+owner+"/"+name, nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// InstallationRepos lists the repositories an installation token reaches (one
// page, and the next page or 0).
func (c *Client) InstallationRepos(ctx context.Context, token string, page int) ([]Repo, int, error) {
	var out struct {
		Repositories []Repo `json:"repositories"`
	}
	h, err := c.bearer(ctx, token, http.MethodGet, "/installation/repositories", pageQuery(page), nil, &out)
	if err != nil {
		return nil, 0, err
	}
	return out.Repositories, nextPage(h), nil
}

// --- the user's own view ---------------------------------------------------------

// User is a GitHub account as /user says.
type User struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

// User reads the account a user token belongs to.
func (c *Client) User(ctx context.Context, token string) (*User, error) {
	var out User
	if _, err := c.bearer(ctx, token, http.MethodGet, "/user", nil, nil, &out); err != nil {
		return nil, err
	}
	if out.ID == 0 || out.Login == "" {
		return nil, errors.New("github: the account answer is incomplete")
	}
	return &out, nil
}

// UserInstallations lists the installations of the App the user can reach.
func (c *Client) UserInstallations(ctx context.Context, token string, page int) ([]Installation, int, error) {
	var out struct {
		Installations []Installation `json:"installations"`
	}
	h, err := c.bearer(ctx, token, http.MethodGet, "/user/installations", pageQuery(page), nil, &out)
	if err != nil {
		return nil, 0, err
	}
	return out.Installations, nextPage(h), nil
}

// UserInstallationRepos lists the repositories of an installation that the user reaches.
func (c *Client) UserInstallationRepos(ctx context.Context, token string, installation int64, page int) ([]Repo, int, error) {
	var out struct {
		Repositories []Repo `json:"repositories"`
	}
	h, err := c.bearer(ctx, token, http.MethodGet, "/user/installations/"+strconv.FormatInt(installation, 10)+"/repositories", pageQuery(page), nil, &out)
	if err != nil {
		return nil, 0, err
	}
	return out.Repositories, nextPage(h), nil
}

// UserRepos lists the repositories the user owns.
func (c *Client) UserRepos(ctx context.Context, token string, page int) ([]Repo, int, error) {
	q := pageQuery(page)
	q.Set("affiliation", "owner")
	q.Set("sort", "full_name")
	var out []Repo
	h, err := c.bearer(ctx, token, http.MethodGet, "/user/repos", q, nil, &out)
	if err != nil {
		return nil, 0, err
	}
	return out, nextPage(h), nil
}

// AddRepoToInstallation adds a repository to an installation that only
// reaches selected repositories, with the user's token.
func (c *Client) AddRepoToInstallation(ctx context.Context, token string, installation, repo int64) error {
	_, err := c.bearer(ctx, token, http.MethodPut,
		"/user/installations/"+strconv.FormatInt(installation, 10)+"/repositories/"+strconv.FormatInt(repo, 10), nil, nil, nil)
	return err
}

// AppCredentials is everything GitHub hands over once, when a manifest is
// turned into an App. Keep none of it in a log or an error.
type AppCredentials struct {
	ID            int64             `json:"id"`
	Slug          string            `json:"slug"`
	Name          string            `json:"name"`
	ClientID      string            `json:"client_id"`
	ClientSecret  string            `json:"client_secret"`
	WebhookSecret string            `json:"webhook_secret"`
	PEM           string            `json:"pem"`
	HTMLURL       string            `json:"html_url"`
	Owner         Account           `json:"owner"`
	Permissions   map[string]string `json:"permissions"`
}

// ConvertManifest trades the code GitHub sent back after the manifest flow for
// the new App's credentials. The code works once, for an hour, and needs no
// other credential.
func (c *Client) ConvertManifest(ctx context.Context, code string) (*AppCredentials, error) {
	if code == "" || len(code) > 128 || strings.ContainsAny(code, "/ ?#%\\") {
		return nil, errors.New("github: the manifest code is unusable")
	}
	var out AppCredentials
	if _, err := c.do(ctx, request{method: http.MethodPost, base: c.api, path: "/app-manifests/" + code + "/conversions", label: "/app-manifests/{code}/conversions"}, &out); err != nil {
		return nil, err
	}
	if out.ID == 0 || out.ClientID == "" || out.ClientSecret == "" || out.PEM == "" {
		return nil, errors.New("github: the manifest answer is incomplete")
	}
	return &out, nil
}

// Manifest is the App Manifest GitHub makes an App from.
type Manifest struct {
	Name               string            `json:"name"`
	URL                string            `json:"url"`
	HookAttributes     ManifestHook      `json:"hook_attributes"`
	RedirectURL        string            `json:"redirect_url"`
	CallbackURLs       []string          `json:"callback_urls"`
	SetupURL           string            `json:"setup_url"`
	Public             bool              `json:"public"`
	RequestOAuthOnInst bool              `json:"request_oauth_on_install"`
	DefaultPermissions map[string]string `json:"default_permissions"`
}

// ManifestHook is the manifest's webhook: none.
type ManifestHook struct {
	URL    string `json:"url"`
	Active bool   `json:"active"`
}

// ManifestURL is where the browser posts the manifest: the page of the
// organisation's App settings when org is given, else the person's own.
func (c *Client) ManifestURL(org, state string) string {
	base := c.web + "/settings/apps/new"
	if org != "" {
		base = c.web + "/organizations/" + org + "/settings/apps/new"
	}
	return base + "?state=" + url.QueryEscape(state)
}

// OrgMembership is a user's membership of an organisation: state is "active"
// or "pending", role "admin" (an owner) or "member".
type OrgMembership struct {
	State string `json:"state"`
	Role  string `json:"role"`
}

// githubOrgOwner is GitHub's role name for an organisation's owner: a role on
// GitHub, not one of this server's (those are asked through authz).
const githubOrgOwner = "admin"

// ActiveOwner reports whether the membership is an accepted one as an owner.
func (m *OrgMembership) ActiveOwner() bool {
	return m.State == "active" && m.Role == githubOrgOwner
}

// UserOrgMembership reads the token's user's membership of an organisation
// (GET /user/memberships/orgs/{org}); a 404 when they are not a member.
func (c *Client) UserOrgMembership(ctx context.Context, token, org string) (*OrgMembership, error) {
	var out OrgMembership
	if _, err := c.bearer(ctx, token, http.MethodGet, "/user/memberships/orgs/"+url.PathEscape(org), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
