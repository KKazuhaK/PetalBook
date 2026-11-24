package api

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/auth"
	"github.com/KKazuhaK/PetalBook/internal/connections"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// The connection endpoints (docs/api.md, "Connections", "The GitHub provider"
// and "Administration", "Connections"). Accounts mode only. They are generic:
// a {provider} of the path is looked up in the registry, and what only some
// types of provider have (an administrator's App, installations, the App made
// in one click) is found by asking the provider for it.

const (
	// connCallbackSuffix ends the path GitHub and the like send the browser back
	// to: /api/connections/{provider}/authorization/callback.
	connCallbackPrefix = "/api/connections/"
	connCallbackSuffix = "/authorization/callback"
	// connManifestPath is where GitHub sends the administrator's browser after
	// the App is made.
	connManifestPath = "/api/connections/github/manifest/callback"
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
	p, ok := s.registry().Get(c.r.PathValue("provider"))
	if !ok {
		s.connError(c, c.r.PathValue("provider"), http.StatusNotFound, connections.CodeNotFound, "no such provider")
		return nil, false
	}
	return p, true
}

// connError answers an error of a connection route: like every one of them
// it carries the provider's id.
func (s *server) connError(c *call, provider string, status int, code, msg string) {
	s.failConnection(c.w, connections.Errorf(provider, status, code, "%s", msg))
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
	Owner             string           `json:"owner"`
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
	r := &connectionResp{Login: a.Login, AccountID: a.AccountID, State: a.State, Connected: rfc3339(a.Connected), Spaces: a.Spaces}
	if !a.RefreshExpires.IsZero() {
		r.RefreshExpires = rfc3339(a.RefreshExpires)
	}
	return r
}

func toProvider(d connections.Descriptor, in *connections.Info) providerResp {
	p := providerResp{ID: d.ID, Type: d.Type, Host: d.Host, Name: d.Name, Owner: d.Owner,
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

func (s *server) listConnections(c *call) {
	list := s.registry().List()
	out := make([]providerResp, 0, len(list))
	for _, p := range list {
		in, err := p.Info(c.r.Context(), c.user().ID)
		if err != nil {
			s.fail(c.w, err)
			return
		}
		out = append(out, toProvider(p.Descriptor(), in))
	}
	writeJSON(c.w, http.StatusOK, struct {
		Providers []providerResp `json:"providers"`
	}{out})
}

func (s *server) connectStart(c *call) {
	p, ok := s.provider(c)
	if !ok {
		return
	}
	var req struct {
		Return  string `json:"return"`
		Install bool   `json:"install"`
	}
	if !s.readJSONMax(c.w, c.r, &req, true, authBodyLimit) {
		return
	}
	st, err := p.StartConnect(c.r.Context(), c.user().ID, connections.StartRequest{Return: req.Return, Install: req.Install})
	if err != nil {
		s.fail(c.w, err)
		return
	}
	http.SetCookie(c.w, s.auth.FlowCookie(connCookieBase+p.Descriptor().ID, st.Binding, connAttemptTTL))
	writeJSON(c.w, http.StatusOK, struct {
		URL string `json:"url"`
	}{st.URL})
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
		s.connError(c, id, http.StatusNotFound, connections.CodeNotFound, "a plain Git address has nothing to connect")
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

func (s *server) connectDisconnect(c *call) {
	p, ok := s.provider(c)
	if !ok {
		return
	}
	ctx := c.r.Context()
	login := ""
	if in, err := p.Info(ctx, c.user().ID); err == nil && in.Connection != nil {
		login = in.Connection.Login
	}
	if err := p.Disconnect(ctx, c.user().ID); err != nil {
		s.fail(c.w, err)
		return
	}
	if login != "" {
		s.audit(c, auth.AuditEvent{Kind: "account_disconnected", Actor: c.user().ID, User: c.user().ID,
			Detail: map[string]any{"provider": p.Descriptor().ID, "login": login}})
	}
	c.w.WriteHeader(http.StatusNoContent)
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
		s.connError(c, p.Descriptor().ID, http.StatusNotFound, connections.CodeNotFound, "no such repository")
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
	items, next, err := p.Owners(c.r.Context(), c.user().ID, q.Get("cursor"))
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
	if p.Descriptor().Type != connections.GitID {
		var inst int64
		if v := q.Get("installation"); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n <= 0 {
				s.connError(c, p.Descriptor().ID, http.StatusBadRequest, connections.CodeInvalid, "installation is a number")
				return
			}
			inst = n
		}
		mine := q.Get("mine") == "true"
		if (inst == 0) == !mine {
			s.connError(c, p.Descriptor().ID, http.StatusBadRequest, connections.CodeInvalid, "give installation or mine=true, not both and not neither")
			return
		}
	}
	var inst int64
	if v := q.Get("installation"); v != "" {
		inst, _ = strconv.ParseInt(v, 10, 64)
	}
	items, next, err := p.Repos(c.r.Context(), c.user().ID, connections.RepoQuery{Installation: inst, Mine: q.Get("mine") == "true", Cursor: q.Get("cursor")})
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

// adminProvider is the {provider} of the path, as one an administrator sets up.
func (s *server) adminProvider(c *call) (connections.Provider, connections.Administered, bool) {
	p, ok := s.provider(c)
	if !ok {
		return nil, nil, false
	}
	a, ok := p.(connections.Administered)
	if !ok {
		s.connError(c, p.Descriptor().ID, http.StatusNotFound, connections.CodeNotFound, "this provider has no settings")
		return nil, nil, false
	}
	return p, a, true
}

func (s *server) adminListConnections(c *call) {
	type adminProviderResp struct {
		ID          string `json:"id"`
		Type        string `json:"type"`
		Name        string `json:"name"`
		Host        string `json:"host"`
		Configured  bool   `json:"configured"`
		PartialSync string `json:"partialSync"`
		Connections int    `json:"connections"`
		Spaces      int    `json:"spaces"`
	}
	list := s.registry().List()
	out := make([]adminProviderResp, 0, len(list))
	for _, p := range list {
		d := p.Descriptor()
		in, err := p.Info(c.r.Context(), c.user().ID)
		if err != nil {
			s.fail(c.w, err)
			return
		}
		r := adminProviderResp{ID: d.ID, Type: d.Type, Name: d.Name, Host: d.Host, Configured: in.Configured, PartialSync: d.Capabilities.PartialSync}
		if a, ok := p.(connections.Administered); ok {
			if r.Connections, r.Spaces, err = a.AdminCounts(c.r.Context()); err != nil {
				s.fail(c.w, err)
				return
			}
		}
		if d.Type == connections.GitID {
			// Nobody connects to a plain address; the spaces on one are counted.
			if r.Spaces, err = store.CountGitSpaces(c.r.Context(), s.opt.Store.Q()); err != nil {
				s.fail(c.w, err)
				return
			}
		}
		out = append(out, r)
	}
	writeJSON(c.w, http.StatusOK, struct {
		Providers []adminProviderResp `json:"providers"`
	}{out})
}

func (s *server) adminGetConnection(c *call) {
	_, a, ok := s.adminProvider(c)
	if !ok {
		return
	}
	v, err := a.AdminView(c.r.Context())
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, v)
}

func (s *server) adminPutConnection(c *call) {
	p, a, ok := s.adminProvider(c)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.w, c.r.Body, authBodyLimit))
	if err != nil {
		s.connError(c, p.Descriptor().ID, http.StatusBadRequest, connections.CodeInvalid, "the body could not be read")
		return
	}
	v, err := a.AdminSave(c.r.Context(), body)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	s.providerChanged(c, p, "saved")
	// Spaces that were waiting for the provider are fetched again at once.
	s.spaces.ConnectionFixed("")
	writeJSON(c.w, http.StatusOK, v)
}

// providerChanged records that a provider was configured, changed or removed;
// the secrets are not in the row. It is the server's own event, not a tenant's.
func (s *server) providerChanged(c *call, p connections.Provider, change string) {
	d := p.Descriptor()
	s.audit(c, auth.AuditEvent{Kind: "connection_provider_changed", Actor: c.user().ID, Tenant: "-",
		Detail: map[string]any{"provider": d.ID, "type": d.Type, "change": change, "secrets": "changed"}})
}

// adminCheckConnection reads the provider's configuration from the platform
// again and answers what the settings view now says.
func (s *server) adminCheckConnection(c *call) {
	_, a, ok := s.adminProvider(c)
	if !ok {
		return
	}
	v, err := a.AdminCheck(c.r.Context())
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, v)
}

func (s *server) adminDeleteConnection(c *call) {
	p, a, ok := s.adminProvider(c)
	if !ok {
		return
	}
	if err := a.AdminDelete(c.r.Context()); err != nil {
		s.fail(c.w, err)
		return
	}
	s.providerChanged(c, p, "removed")
	c.w.WriteHeader(http.StatusNoContent)
}

func (s *server) manifestMaker(c *call) (connections.Provider, connections.ManifestMaker, bool) {
	p, ok := s.provider(c)
	if !ok {
		return nil, nil, false
	}
	m, ok := p.(connections.ManifestMaker)
	if !ok {
		s.connError(c, p.Descriptor().ID, http.StatusNotFound, connections.CodeNotFound, "this provider's App cannot be made in one click")
		return nil, nil, false
	}
	return p, m, true
}

// adminConnectionManifest starts making the App on the platform in one click:
// the answer is the manifest and the address the browser posts it to.
func (s *server) adminConnectionManifest(c *call) {
	_, m, ok := s.manifestMaker(c)
	if !ok {
		return
	}
	var req struct {
		Organization string `json:"organization"`
		Name         string `json:"name"`
	}
	if !s.readJSONMax(c.w, c.r, &req, true, authBodyLimit) {
		return
	}
	start, err := m.StartManifest(c.r.Context(), c.user().ID, connections.ManifestRequest{Organization: req.Organization, Name: req.Name})
	if err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.Header().Set("Cache-Control", "no-store")
	http.SetCookie(c.w, s.auth.FlowCookie(connManifestCookieBase, start.Binding, connAttemptTTL))
	writeJSON(c.w, http.StatusOK, struct {
		URL      string `json:"url"`
		Manifest any    `json:"manifest"`
	}{start.URL, start.Manifest})
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
		http.Redirect(c.w, c.r, "/admin/storage?connection_app_error=expired", http.StatusSeeOther)
		return
	}
	binding := ""
	if ck, err := c.r.Cookie(s.auth.FlowCookieName(connManifestCookieBase)); err == nil {
		binding = ck.Value
	}
	userID := ""
	if c.p != nil && c.isSiteAdmin() {
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

// adminConnectionManifestRetry saves the App GitHub made when the server could
// not save it then.
func (s *server) adminConnectionManifestRetry(c *call) {
	p, m, ok := s.manifestMaker(c)
	if !ok {
		return
	}
	v, err := m.RetryManifest(c.r.Context(), c.user().ID)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	s.providerChanged(c, p, "saved")
	s.spaces.ConnectionFixed("")
	writeJSON(c.w, http.StatusOK, v)
}

func (s *server) installable(c *call) (connections.Installable, bool) {
	p, ok := s.provider(c)
	if !ok {
		return nil, false
	}
	in, ok := p.(connections.Installable)
	if !ok {
		s.connError(c, p.Descriptor().ID, http.StatusNotFound, connections.CodeNotFound, "this provider has no installations")
		return nil, false
	}
	return in, true
}

func (s *server) adminConnectionInstallations(c *call) {
	in, ok := s.installable(c)
	if !ok {
		return
	}
	items, next, err := in.InstallationsView(c.r.Context(), c.r.URL.Query().Get("cursor"))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Items any    `json:"items"`
		Next  string `json:"next"`
	}{items, next})
}

func (s *server) adminPutConnectionInstallation(c *call) {
	in, ok := s.installable(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.connError(c, c.r.PathValue("provider"), http.StatusNotFound, connections.CodeNotFound, "no such installation")
		return
	}
	var req struct {
		Tenants *[]string `json:"tenants"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	if req.Tenants == nil {
		s.connError(c, c.r.PathValue("provider"), http.StatusBadRequest, connections.CodeInvalid, "give tenants: the ids of the tenants to share with (none: [])")
		return
	}
	it, err := in.SetInstallationTenants(c.r.Context(), id, *req.Tenants)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, it)
}
