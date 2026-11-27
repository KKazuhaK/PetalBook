package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/connections"
)

// The repository side of the providers (docs/api.md, "Linked accounts" and
// "Providers"). Accounts mode only. They are generic: a {provider} of the path
// is looked up in the registry, and what only some types of provider have (an
// administrator's App, installations, the App made in one click) is found by
// asking the provider for it. The routes are in providers.go.

const (
	// connCallbackPrefix and connCallbackSuffix make the path GitHub and the
	// like send the browser back to: /api/account/links/{provider}/callback.
	connCallbackPrefix = "/api/account/links/"
	connCallbackSuffix = "/callback"
	// connManifestPath is where GitHub sends the administrator's browser after
	// the App is made.
	connManifestPath = "/api/providers/github/manifest/callback"
	// The cookie of connecting an account is per provider; the manifest flow has
	// a cookie of its own: it ties the return to the administrator's browser.
	connCookieBase         = "petalbook_conn_"
	connManifestCookieBase = "petalbook_conn_manifest"
	connAttemptTTL         = 10 * time.Minute
)

// connectionReturn is true for a platform's redirect back after a person
// connects an account for repos, or an administrator makes the App. It is not
// a sign-in (that is /api/auth/github/{slug}/callback, in fromIdentityProvider)
// and changes nothing unless the attempt's state matches this browser's cookie.
func connectionReturn(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	if r.URL.Path == connManifestPath {
		return true
	}
	rest, ok := strings.CutPrefix(r.URL.Path, connCallbackPrefix)
	if !ok {
		return false
	}
	id, ok := strings.CutSuffix(rest, connCallbackSuffix)
	return ok && id != "" && !strings.Contains(id, "/")
}

// registry is the providers; never nil.
func (s *server) registry() *connections.Registry { return s.opt.Connections }

// provider is the {provider} of the path, or a 404.
func (s *server) provider(c *call) (connections.Provider, bool) {
	// A provider of another tenant is as good as not there.
	p, ok := s.registry().GetFor(c.r.PathValue("provider"), c.tenant())
	if !ok {
		s.connError(c, c.r.PathValue("provider"), http.StatusNotFound, connections.CodeNotFound, "No such provider.")
		return nil, false
	}
	return p, true
}

// connError answers an error of a connection route: like every one of them
// it carries the provider's id.
func (s *server) connError(c *call, provider string, status int, code, msg string) {
	s.failConnection(c.w, connections.Errorf(provider, status, code, "%s.", msg))
}

type capabilitiesResp struct {
	CreateRepo     bool   `json:"createRepo"`
	Installations  bool   `json:"installations"`
	ConnectAccount bool   `json:"connectAccount"`
	StorageTarget  bool   `json:"storageTarget"`
	PartialSync    string `json:"partialSync"`
}

type connectionResp struct {
	Login          string `json:"login"`
	AccountID      string `json:"accountId"`
	State          string `json:"state"`
	Connected      string `json:"connected"`
	RefreshExpires string `json:"refreshExpires,omitempty"`
	Spaces         int    `json:"spaces"`
	// Admin is there (true) when the host said the account is an administrator of the host.
	Admin bool `json:"admin,omitempty"`
}

type appResp struct {
	Name       string `json:"name"`
	Slug       string `json:"slug"`
	HTMLURL    string `json:"htmlUrl"`
	InstallURL string `json:"installUrl"`
}

type leftoverResp struct {
	RepoID    int64  `json:"repoId"`
	FullName  string `json:"fullName"`
	HTMLURL   string `json:"htmlUrl"`
	SpaceName string `json:"spaceName"`
	Created   string `json:"created"`
}

type providerResp struct {
	ID                string           `json:"id"`
	Type              string           `json:"type"`
	Host              string           `json:"host"`
	Name              string           `json:"name"`
	Owner             any              `json:"owner"`
	Capabilities      capabilitiesResp `json:"capabilities"`
	Configured        bool             `json:"configured"`
	App               *appResp         `json:"app"`
	UserAuthorization bool             `json:"userAuthorization"`
	Connection        *connectionResp  `json:"connection"`
	Leftovers         []leftoverResp   `json:"leftovers"`
}

func toConnection(a *connections.Connection) *connectionResp {
	if a == nil {
		return nil
	}
	r := &connectionResp{Login: a.Login, AccountID: a.AccountID, State: a.State, Connected: rfc3339(a.Connected), Spaces: a.Spaces, Admin: a.Admin}
	if !a.RefreshExpires.IsZero() {
		r.RefreshExpires = rfc3339(a.RefreshExpires)
	}
	return r
}

func toProvider(d connections.Descriptor, in *connections.Info) providerResp {
	var owner any = d.Owner
	if d.TenantID != "" {
		owner = map[string]string{"tenant": d.TenantID}
	}
	p := providerResp{ID: d.ID, Type: d.Type, Host: d.Host, Name: d.Name, Owner: owner,
		Capabilities: capabilitiesResp{d.Capabilities.CreateRepo, d.Capabilities.Installations, d.Capabilities.ConnectAccount,
			d.Capabilities.StorageTarget, d.Capabilities.PartialSync},
		Configured: in.Configured, UserAuthorization: in.UserAuthorization, Connection: toConnection(in.Connection),
		Leftovers: make([]leftoverResp, len(in.Leftovers))}
	if in.App != nil {
		p.App = &appResp{in.App.Name, in.App.Slug, in.App.HTMLURL, in.App.InstallURL}
	}
	for i, l := range in.Leftovers {
		p.Leftovers[i] = leftoverResp{l.RepoID, l.FullName, l.HTMLURL, l.SpaceName, rfc3339(l.Created)}
	}
	return p
}

// connectCallback is where the platform sends the browser back to. It answers
// with a redirect, never JSON, and is a cross-site navigation by nature (see
// wrap): it changes nothing unless the attempt's state matches this browser's
// cookie and the session is the user's who started it.
func (s *server) connectCallback(c *call) {
	c.w.Header().Set("Cache-Control", "no-store")
	p, ok := s.registry().Get(c.r.PathValue("provider"))
	if !ok {
		http.Redirect(c.w, c.r, "/settings?connection_error=provider_error", http.StatusSeeOther)
		return
	}
	id := p.Descriptor().ID
	if p.Descriptor().Type == connections.GitID {
		s.connError(c, id, http.StatusNotFound, connections.CodeNotFound, "A plain Git address has nothing to connect.")
		return
	}
	binding := ""
	if ck, err := c.r.Cookie(s.auth.FlowCookieName(connCookieBase + id)); err == nil {
		binding = ck.Value
	}
	userID := ""
	if c.p != nil {
		userID = c.user().ID
	}
	to, clear := p.CompleteConnect(c.r.Context(), binding, userID, c.r.URL.Query())
	if clear {
		http.SetCookie(c.w, s.auth.ClearFlowCookie(connCookieBase+id))
	}
	if userID != "" && !strings.Contains(to, "connection_error=") {
		if in, err := p.Info(c.r.Context(), userID); err == nil && in.Connection != nil {
			s.audit(c, auth.AuditEvent{Kind: "account_connected", Actor: userID, User: userID,
				Detail: map[string]any{"provider": id, "login": in.Connection.Login}})
		}
	}
	http.Redirect(c.w, c.r, to, http.StatusSeeOther)
}

// connectDismissLeftover forgets the caller's reminder of an empty repository.
// It needs no configured provider (the reminder is the caller's own) and says
// 204 when there is no such reminder.
func (s *server) connectDismissLeftover(c *call) {
	p, ok := s.provider(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.r.PathValue("repoId"), 10, 64)
	if err != nil || id <= 0 {
		s.connError(c, p.Descriptor().ID, http.StatusNotFound, connections.CodeNotFound, "No such repository.")
		return
	}
	if err := p.DismissLeftover(c.r.Context(), c.user().ID, id); err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
}

func (s *server) connectOwners(c *call) {
	p, ok := s.provider(c)
	if !ok {
		return
	}
	q := c.r.URL.Query()
	if rc, ok := p.(connections.Rechecker); ok && q.Get("recheck") == "true" && q.Get("cursor") == "" {
		rc.RecheckPerson(c.r.Context(), c.user().ID)
	}
	var items []connections.Owner
	var next string
	var err error
	if sr, ok := p.(connections.Searcher); ok && q.Get("q") != "" {
		items, next, err = sr.OwnersQ(c.r.Context(), c.user().ID, q.Get("cursor"), q.Get("q"))
	} else {
		items, next, err = p.Owners(c.r.Context(), c.user().ID, q.Get("cursor"))
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	type ownerResp struct {
		Login          string `json:"login"`
		Type           string `json:"type"`
		Via            string `json:"via"`
		InstallationID *int64 `json:"installationId"`
		NeedsInstall   bool   `json:"needsInstall"`
	}
	out := make([]ownerResp, len(items))
	for i, o := range items {
		out[i] = ownerResp{Login: o.Login, Type: o.Type, Via: o.Via, NeedsInstall: o.NeedsInstall}
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

func (s *server) connectRepos(c *call) {
	p, ok := s.provider(c)
	if !ok {
		return
	}
	q := c.r.URL.Query()
	d := p.Descriptor()
	if !d.Capabilities.Installations && q.Has("installation") {
		// Installations are GitHub's: a host has none to name, whatever the value.
		s.connError(c, d.ID, http.StatusBadRequest, connections.CodeInvalid, "This provider has no installations.")
		return
	}
	if d.Type != connections.GitID && d.Capabilities.Installations {
		var inst int64
		if v := q.Get("installation"); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n <= 0 {
				s.connError(c, d.ID, http.StatusBadRequest, connections.CodeInvalid, "Installation is a number.")
				return
			}
			inst = n
		}
		mine := q.Get("mine") == "true"
		if (inst == 0) == !mine {
			s.connError(c, d.ID, http.StatusBadRequest, connections.CodeInvalid, "Give installation or mine=true, not both and not neither.")
			return
		}
	}
	var inst int64
	if v := q.Get("installation"); v != "" {
		inst, _ = strconv.ParseInt(v, 10, 64)
	}
	// A provider without installations lists the repositories the caller's own
	// connection reaches, whether or not mine=true is said.
	items, next, err := p.Repos(c.r.Context(), c.user().ID, connections.RepoQuery{Installation: inst, Mine: q.Get("mine") == "true" || !p.Descriptor().Capabilities.Installations,
		Cursor: q.Get("cursor"), Q: q.Get("q")})
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

// providerChanged records that a provider was configured, changed or removed;
// the secrets are not in the row. It is the server's own event, not a tenant's.
func (s *server) providerChanged(c *call, p connections.Provider, change string) {
	d := p.Descriptor()
	s.audit(c, auth.AuditEvent{Kind: "connection_provider_changed", Actor: c.user().ID, Tenant: "-",
		Detail: map[string]any{"provider": d.ID, "type": d.Type, "change": change, "secrets": "changed"}})
}

// adminRepoProvider is the {id} of an administration path, as a provider that
// holds repositories (the registry's): a sign-in provider that is not one is not
// found here.
func (s *server) adminRepoProvider(c *call) (connections.Provider, bool) {
	id := c.r.PathValue("id")
	p, ok := s.registry().Get(id)
	if !ok {
		s.connError(c, id, http.StatusNotFound, connections.CodeNotFound, "No such provider.")
		return nil, false
	}
	return p, true
}

func (s *server) manifestMaker(c *call) (connections.Provider, connections.ManifestMaker, bool) {
	p, ok := s.adminRepoProvider(c)
	if !ok {
		return nil, nil, false
	}
	m, ok := p.(connections.ManifestMaker)
	if !ok {
		s.connError(c, p.Descriptor().ID, http.StatusNotFound, connections.CodeNotFound, "This provider's App cannot be made in one click.")
		return nil, nil, false
	}
	return p, m, true
}

// connectManifestCallback is where GitHub sends the administrator's browser
// after the App is made. Like connectCallback it answers with a redirect and
// changes nothing unless the state matches the attempt of this browser's
// cookie and the session is of the site administrator who started it.
func (s *server) connectManifestCallback(c *call) {
	c.w.Header().Set("Cache-Control", "no-store")
	p, ok := s.registry().Get("github")
	m, isMaker := p.(connections.ManifestMaker)
	if !ok || !isMaker {
		http.Redirect(c.w, c.r, "/admin/storage/connections?connection_app_error=expired", http.StatusSeeOther)
		return
	}
	binding := ""
	if ck, err := c.r.Cookie(s.auth.FlowCookieName(connManifestCookieBase)); err == nil {
		binding = ck.Value
	}
	userID := ""
	if c.can(authz.SiteConnectionsManage) {
		userID = c.user().ID
	}
	to, clear := m.CompleteManifest(c.r.Context(), binding, userID, c.r.URL.Query())
	if clear {
		http.SetCookie(c.w, s.auth.ClearFlowCookie(connManifestCookieBase))
	}
	if strings.Contains(to, "connection_app=created") {
		s.providerChanged(c, p, "saved")
		s.spaces.ConnectionFixed("") // spaces that were waiting for the App are fetched again at once
	}
	http.Redirect(c.w, c.r, to, http.StatusSeeOther)
}

func (s *server) installable(c *call) (connections.Installable, bool) {
	p, ok := s.adminRepoProvider(c)
	if !ok {
		return nil, false
	}
	in, ok := p.(connections.Installable)
	if !ok {
		s.connError(c, p.Descriptor().ID, http.StatusNotFound, connections.CodeNotFound, "This provider has no installations.")
		return nil, false
	}
	return in, true
}
