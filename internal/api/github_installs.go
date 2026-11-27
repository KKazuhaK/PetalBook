package api

import (
	"context"
	"net/http"
	"strconv"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/connections"
	"petalbook.app/server/internal/github"
	"petalbook.app/server/internal/settings"
	"petalbook.app/server/internal/store"
)

// GitHub installations (docs/api.md, "The GitHub provider", "Installations"):
// anyone may install the site's one GitHub App on their own GitHub account,
// and an organisation's administrators on the organisation's GitHub
// organisation. Each installation belongs to that one owner here; a person's
// spaces sync only through what is theirs, an organisation's only through its
// own. The person's routes are under /api/account/github, the organisation's
// under /api/admin/github (the acting tenant; any of its administrators).
var githubInstallRoutes = []route{
	{"POST", "/api/account/github/install", signedIn.on(settings.FeatureGitHubConnect), true, true, false, (*server).githubInstallMine},
	{"GET", "/api/account/github/installations", signedIn.always(), true, false, false, (*server).githubListMine},
	{"GET", "/api/account/github/installations/{id}/spaces", signedIn.always(), true, false, false, (*server).githubSpacesMine},
	{"GET", "/api/account/github/installations/{id}/repos", signedIn.always(), true, false, false, (*server).githubReposMine},
	{"DELETE", "/api/account/github/installations/{id}", signedIn.always(), true, false, false, (*server).githubDisconnectMine},

	{"POST", "/api/admin/github/install", needs(authz.TenantGitHubManage).on(settings.FeatureGitHubConnect), true, true, false, (*server).githubInstallOrg},
	{"GET", "/api/admin/github/installations", needs(authz.TenantGitHubManage).always(), true, false, false, (*server).githubListOrg},
	{"GET", "/api/admin/github/installations/{id}/spaces", needs(authz.TenantGitHubManage).always(), true, false, false, (*server).githubSpacesOrg},
	{"GET", "/api/admin/github/installations/{id}/repos", needs(authz.TenantGitHubManage).always(), true, false, false, (*server).githubReposOrg},
	{"DELETE", "/api/admin/github/installations/{id}", needs(authz.TenantGitHubManage).always(), true, false, false, (*server).githubDisconnectOrg},
}

func init() { routeTable = append(routeTable, githubInstallRoutes...) }

// githubService is the GitHub provider, or a 409 provider_not_configured.
func (s *server) githubService(c *call) (*github.Service, bool) {
	if p, ok := s.registry().Get(github.ProviderID); ok {
		if svc, ok := p.(*github.Service); ok {
			return svc, true
		}
	}
	s.connError(c, github.ProviderID, http.StatusConflict, connections.CodeNotConfigured, "This server has no GitHub App set up.")
	return nil, false
}

// githubHooks are what the GitHub service is lent for an installation's start
// and callback: whether the caller may connect one for an owner (their own
// account when the feature is on for their tenant; an organisation when they
// hold tenant.github.manage there and the feature is on for it), and the audit.
func (s *server) githubHooks(c *call) connections.InstallHooks {
	return connections.InstallHooks{
		Allow: func(ctx context.Context, o store.InstallOwner) error {
			if c.p == nil {
				return connections.Errorf(github.ProviderID, http.StatusForbidden, connections.CodeForbidden, "Sign in first.")
			}
			u := c.user()
			tenant := o.TenantID
			if o.UserID != "" {
				if o.UserID != u.ID {
					return connections.Errorf(github.ProviderID, http.StatusForbidden, connections.CodeForbidden, "An installation of your own is connected to yourself only.")
				}
				tenant = c.tenant() // the person's own: their routes take no scope
			} else {
				if tenant == store.DefaultTenantID {
					return connections.Errorf(github.ProviderID, http.StatusForbidden, connections.CodeForbidden, "Only an organisation connects an installation of a GitHub organisation.")
				}
				if err := s.allow(c, authz.TenantGitHubManage, tenant); err != nil {
					return err
				}
			}
			if s.single || s.settings == nil {
				return nil
			}
			on, err := s.featureOn(ctx, tenant, settings.FeatureGitHubConnect)
			if err != nil {
				return err
			}
			if !on {
				return featureOffError(settings.FeatureGitHubConnect, "")
			}
			return nil
		},
		Claimed: func(_ context.Context, o store.InstallOwner, id int64, account string) {
			s.audit(c, auth.AuditEvent{Kind: "github_installation_connected", Actor: c.user().ID, User: o.UserID, Tenant: o.TenantID,
				Detail: map[string]any{"installation": id, "account": account}})
		},
	}
}

type installStartReq struct {
	Account string `json:"account"`
	Return  string `json:"return"`
}

func (s *server) githubInstallStart(c *call, o store.InstallOwner, ret string) {
	svc, ok := s.githubService(c)
	if !ok {
		return
	}
	var req installStartReq
	if !s.readJSONMax(c.w, c.r, &req, true, authBodyLimit) {
		return
	}
	if req.Return == "" {
		req.Return = ret
	}
	ctx := connections.WithInstallHooks(c.r.Context(), s.githubHooks(c))
	u, binding, err := svc.StartInstallFor(ctx, c.user().ID, o, req.Account, req.Return)
	if err != nil {
		s.refuse(c.w, err)
		return
	}
	http.SetCookie(c.w, s.auth.FlowCookie(connCookieBase+github.ProviderID, binding, connAttemptTTL))
	writeJSON(c.w, http.StatusOK, struct {
		URL string `json:"url"`
	}{u})
}

func (s *server) githubInstallMine(c *call) {
	s.githubInstallStart(c, store.InstallOwner{UserID: c.user().ID}, "/settings/connections")
}

// orgOwner is the acting tenant as an owner of installations: the default
// tenant is nobody's organisation (its people's spaces are their own).
func (s *server) orgOwner(c *call) (store.InstallOwner, bool) {
	if c.tenant() == store.DefaultTenantID {
		s.connError(c, github.ProviderID, http.StatusForbidden, connections.CodeForbidden, "The default tenant has no organisation installations: its people connect GitHub in their own settings.")
		return store.InstallOwner{}, false
	}
	return store.InstallOwner{TenantID: c.tenant()}, true
}

func (s *server) githubInstallOrg(c *call) {
	o, ok := s.orgOwner(c)
	if !ok {
		return
	}
	s.githubInstallStart(c, o, "/admin/storage/github")
}

type installationResp struct {
	ID          int64  `json:"id"`
	Account     string `json:"account"`
	AccountType string `json:"accountType"`
	Owner       struct {
		Kind string `json:"kind"` // "person" or "organization"
		ID   string `json:"id"`
	} `json:"owner"`
	ClaimedBy string `json:"claimedBy"`
	Connected string `json:"connected"`
	Spaces    int    `json:"spaces"`
	ManageURL string `json:"manageUrl"`
}

func (s *server) githubList(c *call, o store.InstallOwner) {
	svc, ok := s.githubService(c)
	if !ok {
		return
	}
	items, err := svc.Installations(c.r.Context(), o)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	out := make([]installationResp, len(items))
	for i, it := range items {
		r := installationResp{ID: it.ID, Account: it.Account, AccountType: it.AccountType, ClaimedBy: it.ClaimedBy, Connected: it.Connected,
			Spaces: it.Spaces, ManageURL: it.ManageURL}
		if it.Owner.UserID != "" {
			r.Owner.Kind, r.Owner.ID = "person", it.Owner.UserID
		} else {
			r.Owner.Kind, r.Owner.ID = "organization", it.Owner.TenantID
		}
		out[i] = r
	}
	writeJSON(c.w, http.StatusOK, struct {
		Items []installationResp `json:"items"`
	}{out})
}

func (s *server) githubListMine(c *call) { s.githubList(c, store.InstallOwner{UserID: c.user().ID}) }

func (s *server) githubListOrg(c *call) {
	if o, ok := s.orgOwner(c); ok {
		s.githubList(c, o)
	}
}

func (s *server) installationID(c *call) (int64, bool) {
	id, err := strconv.ParseInt(c.r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.connError(c, github.ProviderID, http.StatusNotFound, connections.CodeNotFound, "No such installation.")
		return 0, false
	}
	return id, true
}

type affectedSpace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (s *server) githubSpaces(c *call, o store.InstallOwner) {
	svc, ok := s.githubService(c)
	if !ok {
		return
	}
	id, ok := s.installationID(c)
	if !ok {
		return
	}
	list, n, err := svc.InstallationSpaces(c.r.Context(), o, id)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	out := make([]affectedSpace, len(list))
	for i, sp := range list {
		out[i] = affectedSpace{ID: sp.ID, Name: sp.Name}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Items []affectedSpace `json:"items"`
		Total int             `json:"total"`
	}{out, n})
}

func (s *server) githubSpacesMine(c *call) {
	s.githubSpaces(c, store.InstallOwner{UserID: c.user().ID})
}

func (s *server) githubSpacesOrg(c *call) {
	if o, ok := s.orgOwner(c); ok {
		s.githubSpaces(c, o)
	}
}

func (s *server) githubRepos(c *call, o store.InstallOwner) {
	svc, ok := s.githubService(c)
	if !ok {
		return
	}
	id, ok := s.installationID(c)
	if !ok {
		return
	}
	items, next, err := svc.InstallationRepos(c.r.Context(), o, id, c.r.URL.Query().Get("cursor"))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	out := make([]repoResp, len(items))
	for i, r := range items {
		out[i] = repoRespOf(r)
	}
	writeJSON(c.w, http.StatusOK, struct {
		Items []repoResp `json:"items"`
		Next  string     `json:"next"`
	}{out, next})
}

func (s *server) githubReposMine(c *call) { s.githubRepos(c, store.InstallOwner{UserID: c.user().ID}) }

func (s *server) githubReposOrg(c *call) {
	if o, ok := s.orgOwner(c); ok {
		s.githubRepos(c, o)
	}
}

func (s *server) githubDisconnect(c *call, o store.InstallOwner) {
	svc, ok := s.githubService(c)
	if !ok {
		return
	}
	id, ok := s.installationID(c)
	if !ok {
		return
	}
	ids, err := svc.DisconnectInstallation(c.r.Context(), o, id)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	s.audit(c, auth.AuditEvent{Kind: "github_installation_disconnected", Actor: c.user().ID, User: o.UserID, Tenant: o.TenantID,
		Detail: map[string]any{"installation": id, "spaces": len(ids)}})
	writeJSON(c.w, http.StatusOK, struct {
		Spaces int `json:"spaces"`
	}{len(ids)})
}

func (s *server) githubDisconnectMine(c *call) {
	s.githubDisconnect(c, store.InstallOwner{UserID: c.user().ID})
}

func (s *server) githubDisconnectOrg(c *call) {
	if o, ok := s.orgOwner(c); ok {
		s.githubDisconnect(c, o)
	}
}
