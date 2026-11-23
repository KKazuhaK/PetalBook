package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/github"
)

// The GitHub endpoints (docs/api.md, "GitHub" and "Administration", "GitHub
// App"). Accounts mode only. Every one answers 409 github_not_configured while
// the server has no App.

const (
	githubCallbackPath = "/api/github/authorization/callback"
	githubCookieBase   = "petalbook_github"
	githubAttemptTTL   = 10 * time.Minute
)

func (s *server) gh(c *call) (*github.Service, bool) {
	if s.opt.GitHub == nil {
		s.fail(c.w, &github.Error{Code: github.CodeNotConfigured, Status: http.StatusConflict, Msg: "this server has no GitHub App set up"})
		return nil, false
	}
	return s.opt.GitHub, true
}

type githubAuthResp struct {
	Login          string `json:"login"`
	GitHubID       int64  `json:"githubId"`
	State          string `json:"state"`
	Connected      string `json:"connected"`
	RefreshExpires string `json:"refreshExpires,omitempty"`
	Spaces         int    `json:"spaces"`
}

func toGitHubAuth(a *github.Authorization) *githubAuthResp {
	if a == nil {
		return nil
	}
	r := &githubAuthResp{Login: a.Login, GitHubID: a.GitHubID, State: a.State, Connected: rfc3339(a.Connected), Spaces: a.Spaces}
	if !a.RefreshExpires.IsZero() {
		r.RefreshExpires = rfc3339(a.RefreshExpires)
	}
	return r
}

func (s *server) getGitHub(c *call) {
	var info *github.Info
	if s.opt.GitHub != nil {
		var err error
		if info, err = s.opt.GitHub.Info(c.r.Context(), c.user().ID); err != nil {
			s.fail(c.w, err)
			return
		}
	} else {
		info = &github.Info{}
	}
	type appResp struct {
		Name       string `json:"name"`
		Slug       string `json:"slug"`
		HTMLURL    string `json:"htmlUrl"`
		InstallURL string `json:"installUrl"`
	}
	var app *appResp
	if info.Configured {
		app = &appResp{info.Name, info.Slug, info.HTMLURL, info.InstallURL}
	}
	writeJSON(c.w, http.StatusOK, struct {
		App               *appResp        `json:"app"`
		UserAuthorization bool            `json:"userAuthorization"`
		Authorization     *githubAuthResp `json:"authorization"`
	}{app, info.UserAuthorization, toGitHubAuth(info.Authorization)})
}

func (s *server) githubAuthStart(c *call) {
	gh, ok := s.gh(c)
	if !ok {
		return
	}
	var req struct {
		Return string `json:"return"`
	}
	if !s.readJSONMax(c.w, c.r, &req, true, authBodyLimit) {
		return
	}
	authURL, binding, err := gh.StartAuthorization(c.r.Context(), c.user().ID, req.Return)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	http.SetCookie(c.w, s.auth.FlowCookie(githubCookieBase, binding, githubAttemptTTL))
	writeJSON(c.w, http.StatusOK, struct {
		URL string `json:"url"`
	}{authURL})
}

// githubAuthCallback is where GitHub sends the browser back to. It answers with
// a redirect, never JSON, and is a cross-site navigation by nature (see wrap):
// it changes nothing unless the attempt's state matches this browser's cookie
// and the session is the user's who started it.
func (s *server) githubAuthCallback(c *call) {
	c.w.Header().Set("Cache-Control", "no-store")
	if s.opt.GitHub == nil {
		http.Redirect(c.w, c.r, "/settings?github_error=github_error", http.StatusSeeOther)
		return
	}
	binding := ""
	if ck, err := c.r.Cookie(s.auth.FlowCookieName(githubCookieBase)); err == nil {
		binding = ck.Value
	}
	userID := ""
	if c.p != nil {
		userID = c.user().ID
	}
	to, clear := s.opt.GitHub.CompleteAuthorization(c.r.Context(), binding, userID, c.r.URL.Query())
	if clear {
		http.SetCookie(c.w, s.auth.ClearFlowCookie(githubCookieBase))
	}
	http.Redirect(c.w, c.r, to, http.StatusSeeOther)
}

func (s *server) githubDisconnect(c *call) {
	gh, ok := s.gh(c)
	if !ok {
		return
	}
	if err := gh.Disconnect(c.r.Context(), c.user().ID); err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
}

func (s *server) githubOwners(c *call) {
	gh, ok := s.gh(c)
	if !ok {
		return
	}
	items, next, err := gh.Owners(c.r.Context(), c.user().ID, c.r.URL.Query().Get("cursor"))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	type ownerResp struct {
		Login          string `json:"login"`
		Type           string `json:"type"`
		Via            string `json:"via"`
		InstallationID *int64 `json:"installationId"`
	}
	out := make([]ownerResp, len(items))
	for i, o := range items {
		out[i] = ownerResp{Login: o.Login, Type: o.Type, Via: o.Via}
		if o.InstallationID > 0 {
			id := o.InstallationID
			out[i].InstallationID = &id
		}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Items []ownerResp `json:"items"`
		Next  string      `json:"next"`
	}{out, next})
}

func (s *server) githubRepos(c *call) {
	gh, ok := s.gh(c)
	if !ok {
		return
	}
	q := c.r.URL.Query()
	var inst int64
	if v := q.Get("installation"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			writeError(c.w, http.StatusBadRequest, "invalid", "installation is a number")
			return
		}
		inst = n
	}
	mine := q.Get("mine") == "true"
	if (inst == 0) == !mine {
		writeError(c.w, http.StatusBadRequest, "invalid", "give installation or mine=true, not both and not neither")
		return
	}
	items, next, err := gh.Repos(c.r.Context(), c.user().ID, inst, mine, q.Get("cursor"))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	type repoResp struct {
		ID            int64  `json:"id"`
		FullName      string `json:"fullName"`
		Private       bool   `json:"private"`
		DefaultBranch string `json:"defaultBranch"`
		HTMLURL       string `json:"htmlUrl"`
		CanPush       bool   `json:"canPush"`
	}
	out := make([]repoResp, len(items))
	for i, r := range items {
		out[i] = repoResp{r.ID, r.FullName, r.Private, r.DefaultBranch, r.HTMLURL, r.CanPush}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Items []repoResp `json:"items"`
		Next  string     `json:"next"`
	}{out, next})
}

// --- administration -------------------------------------------------------------------

type adminGitHubOff struct {
	Configured  bool   `json:"configured"`
	CallbackURL string `json:"callbackUrl"`
}

type adminGitHubResp struct {
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
}

func toAdminGitHub(v *github.AppView) any {
	if !v.Configured {
		return adminGitHubOff{CallbackURL: v.CallbackURL}
	}
	r := adminGitHubResp{Configured: true, AppID: v.AppID, ClientID: v.ClientID, Slug: v.Slug, Name: v.Name, Owner: v.Owner,
		HTMLURL: v.HTMLURL, InstallURL: v.InstallURL, CallbackURL: v.CallbackURL, Permissions: v.Permissions, Missing: v.Missing,
		UserAuthorization: v.UserAuthorizaton, Checked: rfc3339(v.Checked), Spaces: v.Spaces, Authorizations: v.Authorizations}
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

func (s *server) adminGetGitHub(c *call) {
	if s.opt.GitHub == nil {
		writeJSON(c.w, http.StatusOK, adminGitHubOff{})
		return
	}
	v, err := s.opt.GitHub.View(c.r.Context())
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, toAdminGitHub(v))
}

func (s *server) adminPutGitHub(c *call) {
	gh, ok := s.gh(c)
	if !ok {
		return
	}
	var req struct {
		AppID        string  `json:"appId"`
		PrivateKey   string  `json:"privateKey"`
		ClientID     *string `json:"clientId"`
		ClientSecret string  `json:"clientSecret"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	v, err := gh.Save(c.r.Context(), github.SaveRequest{AppID: req.AppID, PrivateKey: req.PrivateKey, ClientID: req.ClientID, ClientSecret: req.ClientSecret})
	if err != nil {
		s.fail(c.w, err)
		return
	}
	// Spaces that were waiting for the App are fetched again at once.
	s.spaces.GitHubFixed("")
	writeJSON(c.w, http.StatusOK, toAdminGitHub(v))
}

func (s *server) adminDeleteGitHub(c *call) {
	gh, ok := s.gh(c)
	if !ok {
		return
	}
	if err := gh.Delete(c.r.Context()); err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
}

type installationResp struct {
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
	Shared              bool              `json:"shared"`
	Spaces              int               `json:"spaces"`
}

func toInstallation(i github.Installation) installationResp {
	r := installationResp{ID: i.ID, RepositorySelection: i.RepositorySelection, Suspended: i.Suspended, Permissions: i.Permissions,
		HTMLURL: i.HTMLURL, Shared: i.Shared, Spaces: i.Spaces}
	r.Account.Login, r.Account.Type, r.Account.ID = i.Account.Login, i.Account.Type, i.Account.ID
	if r.Permissions == nil {
		r.Permissions = map[string]string{}
	}
	return r
}

func (s *server) adminGitHubInstallations(c *call) {
	gh, ok := s.gh(c)
	if !ok {
		return
	}
	items, next, err := gh.Installations(c.r.Context(), c.r.URL.Query().Get("cursor"))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	out := make([]installationResp, len(items))
	for i, it := range items {
		out[i] = toInstallation(it)
	}
	writeJSON(c.w, http.StatusOK, struct {
		Items []installationResp `json:"items"`
		Next  string             `json:"next"`
	}{out, next})
}

func (s *server) adminPutGitHubInstallation(c *call) {
	gh, ok := s.gh(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(c.w, http.StatusNotFound, "not_found", "no such installation")
		return
	}
	var req struct {
		Shared *bool `json:"shared"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	if req.Shared == nil {
		writeError(c.w, http.StatusBadRequest, "invalid", "give shared: true or false")
		return
	}
	it, err := gh.SetShared(c.r.Context(), id, *req.Shared)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, toInstallation(*it))
}
