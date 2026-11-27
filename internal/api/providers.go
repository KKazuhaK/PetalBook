package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/authz"
	"petalbook.app/server/internal/connections"
	"petalbook.app/server/internal/store"
)

// Providers and linked accounts (docs/api.md, "Providers" and "Linked
// accounts"): one administration resource for every outside system people sign
// in with or connect repositories to (OpenID Connect, SAML, GitHub, GitLab,
// Gitea), and one resource for a person's accounts there. What differs between
// the kinds is the body: a sign-in provider's settings are auth's, a
// repository provider's the connection registry's, and the GitHub row has both.

// providerKeys admit the providers' administration: the sign-in key shows and
// changes the sign-in side, the connections key the repository side; a handler
// asks which of them the caller holds for what it touches.
var providerKeys = needsAny(authz.SiteSigninManage, authz.SiteConnectionsManage).always()

var providerRoutes = []route{
	{"GET", "/api/admin/providers", providerKeys, true, false, false, (*server).adminListProviders},
	{"POST", "/api/admin/providers", providerKeys, true, false, false, (*server).adminCreateProvider},
	{"GET", "/api/admin/providers/{id}", providerKeys, true, false, false, (*server).adminGetProvider},
	{"PUT", "/api/admin/providers/{id}", providerKeys, true, false, false, (*server).adminPutProvider},
	{"PATCH", "/api/admin/providers/{id}", providerKeys, true, false, false, (*server).adminPatchProvider},
	{"DELETE", "/api/admin/providers/{id}", providerKeys, true, false, false, (*server).adminDeleteProvider},
	{"POST", "/api/admin/providers/{id}/check", providerKeys, true, false, false, (*server).adminCheckProvider},
	{"POST", "/api/admin/providers/{id}/test", needs(authz.SiteSigninManage).always(), true, true, false, (*server).adminTestProvider},
	{"GET", "/api/admin/providers/{id}/test", needs(authz.SiteSigninManage).always(), true, false, false, (*server).adminTestResult},
	{"POST", "/api/admin/providers/{id}/manifest", needs(authz.SiteConnectionsManage).always(), true, false, false, (*server).adminProviderManifest},
	{"POST", "/api/admin/providers/{id}/manifest/retry", needs(authz.SiteConnectionsManage).always(), true, false, false, (*server).adminProviderManifestRetry},
	{"GET", connManifestPath, public.always(), true, true, false, (*server).connectManifestCallback},

	{"GET", "/api/account/links", signedIn.always(), true, false, false, (*server).listAccountLinks},
	{"POST", "/api/account/links/{provider}/start", signedIn.always(), true, true, false, (*server).accountLinkStart},
	{"GET", connCallbackPrefix + "{provider}" + connCallbackSuffix, public.always(), true, true, false, (*server).connectCallback},
	{"DELETE", "/api/account/links/{id}", signedIn.always(), true, false, false, (*server).unlinkAccount},
	{"DELETE", "/api/account/links/{id}/repos", signedIn.always(), true, false, false, (*server).unlinkAccountRepos},
	{"GET", "/api/account/links/{provider}/owners", signedIn.always(), true, false, false, (*server).connectOwners},
	{"GET", "/api/account/links/{provider}/repos", signedIn.always(), true, false, false, (*server).connectRepos},
	{"DELETE", "/api/account/leftovers/{provider}/{repoId}", signedIn.always(), true, false, false, (*server).connectDismissLeftover},

	{"GET", "/api/users/{id}/links", needs(authz.TenantUsersRead).always(), true, false, false, (*server).userLinks},
	{"DELETE", "/api/users/{id}/links/{link}", needs(authz.TenantUsersManage).always(), true, false, false, (*server).disconnectUserLink},
}

func init() { routeTable = append(routeTable, providerRoutes...) }

// --- administration -------------------------------------------------------------------

// repoRowResp is what the list says of the repository side of a provider.
type repoRowResp struct {
	Host        string `json:"host"`
	Configured  bool   `json:"configured"`
	PartialSync string `json:"partialSync"`
	Connections int    `json:"connections"`
	Spaces      int    `json:"spaces"`
}

// signinItem is a provider people sign in with; Repo is its repository side
// when it has one (the GitHub row once its App is set up).
type signinItem struct {
	*auth.ProviderView
	Repo *repoRowResp `json:"repo,omitempty"`
}

// repoItem is a provider that only holds repositories (GitLab, Gitea, a plain
// Git address) or that is not set up yet (GitHub before its App), in the list.
type repoItem struct {
	ID      string `json:"id"`
	Slug    string `json:"slug"`
	Kind    string `json:"kind"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Owner   string `json:"owner"`
	Enabled bool   `json:"enabled"`
	Signin  bool   `json:"signin"`
	Repos   bool   `json:"repos"`
	repoRowResp
}

// canSignin and canRepos are what the caller may touch.
func (s *server) canSignin(c *call) bool { return c.can(authz.SiteSigninManage) }
func (s *server) canRepos(c *call) bool  { return c.can(authz.SiteConnectionsManage) }

func (s *server) noSuchProvider(c *call, id string) {
	s.connError(c, id, http.StatusNotFound, connections.CodeNotFound, "No such provider.")
}

// repoSummary is the repository side of a provider as the list shows it.
func (s *server) repoSummary(c *call, p connections.Provider) (*repoRowResp, error) {
	d := p.Descriptor()
	in, err := p.Info(c.r.Context(), c.user().ID)
	if err != nil {
		return nil, err
	}
	r := &repoRowResp{Host: d.Host, Configured: in.Configured, PartialSync: d.Capabilities.PartialSync}
	if a, ok := p.(connections.Administered); ok {
		if r.Connections, r.Spaces, err = a.AdminCounts(c.r.Context()); err != nil {
			return nil, err
		}
	}
	if d.Type == connections.GitID {
		// Nobody connects to a plain address; the spaces on one are counted.
		if r.Spaces, err = store.CountGitSpaces(c.r.Context(), s.opt.Store.Q()); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func (s *server) adminListProviders(c *call) {
	ctx := c.r.Context()
	items := []any{}
	seen := map[string]bool{}
	if s.canSignin(c) {
		l, err := s.auth.Providers(ctx)
		if err != nil {
			s.fail(c.w, err)
			return
		}
		for i := range l {
			v := l[i]
			it := signinItem{ProviderView: &v}
			if p, ok := s.registry().Get(v.ID); ok && v.Repos && s.canRepos(c) {
				r, err := s.repoSummary(c, p)
				if err != nil {
					s.fail(c.w, err)
					return
				}
				it.Repo = r
			}
			seen[v.ID] = true
			items = append(items, it)
		}
	}
	if s.canRepos(c) {
		for _, p := range s.registry().List() {
			d := p.Descriptor()
			if seen[d.ID] || d.Type == connections.GitID {
				continue // a plain Git address is no provider to set up
			}
			r, err := s.repoSummary(c, p)
			if err != nil {
				s.fail(c.w, err)
				return
			}
			items = append(items, repoItem{ID: d.ID, Slug: d.ID, Kind: d.Type, Type: d.Type, Name: d.Name, Owner: "site", Enabled: true,
				Repos: r.Configured, repoRowResp: *r})
		}
	}
	// Where a host's application sends people back to, for the form that adds
	// a host (its id is not known yet): {id} stands for it.
	callback := ""
	if s.canRepos(c) && s.opt.PublicURL != "" {
		callback = s.opt.PublicURL + connections.CallbackPath("{id}")
	}
	writeJSON(c.w, http.StatusOK, struct {
		Providers []any `json:"providers"`
		// LinkCallback is "" without a public address, or for who does not manage connections.
		LinkCallback string `json:"linkCallback"`
	}{items, callback})
}

// repoBody is the repository side of a provider as its administrator sees it
// (flat, the kind's own fields), with what says which provider it is.
func (s *server) repoBody(c *call, p connections.Provider, a connections.Administered, sv *auth.ProviderView) (any, error) {
	v, err := a.AdminView(c.r.Context())
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	d := p.Descriptor()
	m["kind"], m["signin"], m["repos"] = d.Type, sv != nil, true
	if sv != nil {
		m["provider"] = sv
	}
	return m, nil
}

// providerBody is one provider as an administrator sees it: a sign-in provider's
// settings, or a repository provider's (the GitHub provider has both, the
// sign-in side as "provider"). false when there is no such provider, or none
// the caller may see.
func (s *server) providerBody(c *call, id string) (any, bool, error) {
	ctx := c.r.Context()
	var sv *auth.ProviderView
	if s.canSignin(c) {
		v, err := s.auth.Provider(ctx, id)
		var ae *auth.Error
		switch {
		case err == nil:
			sv = v
		case errors.As(err, &ae) && ae.Kind == auth.NotFound:
		default:
			return nil, false, err
		}
	}
	if p, ok := s.registry().Get(id); ok && s.canRepos(c) {
		if a, ok := p.(connections.Administered); ok && (sv == nil || p.Descriptor().Type == "github") {
			v, err := s.repoBody(c, p, a, sv)
			return v, err == nil, err
		}
	}
	if sv != nil {
		it := signinItem{ProviderView: sv}
		if p, ok := s.registry().Get(id); ok && sv.Repos && s.canRepos(c) {
			r, err := s.repoSummary(c, p)
			if err != nil {
				return nil, false, err
			}
			it.Repo = r
		}
		return it, true, nil
	}
	return nil, false, nil
}

func (s *server) writeProvider(c *call, status int, id string) {
	v, ok, err := s.providerBody(c, id)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	if !ok {
		s.noSuchProvider(c, id)
		return
	}
	writeJSON(c.w, status, v)
}

func (s *server) adminGetProvider(c *call) {
	s.writeProvider(c, http.StatusOK, c.r.PathValue("id"))
}

// adminCreateProvider makes a provider: a sign-in provider (oidc, saml, github),
// or a GitLab or Gitea host (an OAuth application per host, checked against the
// storage settings' allowed hosts and asked before anything is kept). The
// GitHub App is set up with PUT on the GitHub provider.
func (s *server) adminCreateProvider(c *call) {
	body, err := io.ReadAll(http.MaxBytesReader(c.w, c.r.Body, authBodyLimit))
	if err != nil {
		writeError(c.w, http.StatusBadRequest, "invalid", "The body could not be read.")
		return
	}
	var peek struct {
		Kind string `json:"kind"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(body, &peek); err != nil {
		writeError(c.w, http.StatusBadRequest, "invalid", "The body is not what this takes: "+err.Error()+".")
		return
	}
	kind := peek.Kind
	if kind == "" {
		kind = peek.Type
	}
	switch kind {
	case "gitlab", "gitea":
		if !s.canRepos(c) {
			s.forbidden(c)
			return
		}
		cr := s.registry().Creator()
		if cr == nil {
			s.connError(c, "", http.StatusNotFound, connections.CodeNotFound, "This server makes no providers.")
			return
		}
		// The host's own fields: id is the slug, type the kind.
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			writeError(c.w, http.StatusBadRequest, "invalid", "The body is not what this takes: "+err.Error()+".")
			return
		}
		if _, ok := m["type"]; !ok {
			m["type"] = kind
		}
		if _, ok := m["id"]; !ok {
			if slug, ok := m["slug"]; ok {
				m["id"] = slug
			}
		}
		nb, _ := json.Marshal(m)
		p, _, err := cr.CreateProvider(c.r.Context(), c.user().ID, nb)
		if err != nil {
			s.fail(c.w, err)
			return
		}
		s.providerChanged(c, p, "created")
		c.w.Header().Set("Location", "/api/admin/providers/"+url.PathEscape(p.Descriptor().ID))
		s.writeProvider(c, http.StatusCreated, p.Descriptor().ID)
	default:
		if !s.canSignin(c) {
			s.forbidden(c)
			return
		}
		var req auth.ProviderInput
		if err := json.Unmarshal(body, &req); err != nil {
			writeError(c.w, http.StatusBadRequest, "invalid", "The body is not what this takes: "+err.Error()+".")
			return
		}
		v, err := s.auth.CreateProvider(c.r.Context(), c.user().ID, s.auth.ClientAddr(c.r), req)
		if err != nil {
			s.fail(c.w, err)
			return
		}
		c.w.Header().Set("Location", "/api/admin/providers/"+url.PathEscape(v.ID))
		s.writeProvider(c, http.StatusCreated, v.ID)
	}
}

func (s *server) forbidden(c *call) {
	writeError(c.w, http.StatusForbidden, "forbidden", "You may not do that.")
}

// adminPutProvider saves the repository side of a provider: a GitLab or Gitea
// host's name and client, or the GitHub App ({appId, privateKey, clientId,
// clientSecret}); each is checked with the platform before anything is kept.
func (s *server) adminPutProvider(c *call) {
	if !s.canRepos(c) {
		s.forbidden(c)
		return
	}
	s.saveRepoSide(c)
}

func (s *server) saveRepoSide(c *call) {
	id := c.r.PathValue("id")
	p, ok := s.registry().Get(id)
	a, isAdmin := p.(connections.Administered)
	if !ok || !isAdmin {
		if !ok {
			s.noSuchProvider(c, id)
		} else {
			s.connError(c, id, http.StatusNotFound, connections.CodeNotFound, "This provider has no settings.")
		}
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.w, c.r.Body, authBodyLimit))
	if err != nil {
		s.connError(c, id, http.StatusBadRequest, connections.CodeInvalid, "The body could not be read.")
		return
	}
	if _, err := a.AdminSave(connections.WithActor(c.r.Context(), c.user().ID), body); err != nil {
		s.fail(c.w, err)
		return
	}
	s.providerChanged(c, p, "saved")
	// Spaces that were waiting for the provider are fetched again at once.
	s.spaces.ConnectionFixed("")
	s.writeProvider(c, http.StatusOK, id)
}

// adminPatchProvider changes a provider: a sign-in provider's settings (the
// body is auth's), or, for a GitLab or Gitea host, what PUT saves.
func (s *server) adminPatchProvider(c *call) {
	id := c.r.PathValue("id")
	if s.canSignin(c) {
		if _, err := s.auth.Provider(c.r.Context(), id); err == nil {
			var req auth.ProviderInput
			if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
				return
			}
			if _, err := s.auth.UpdateProvider(c.r.Context(), c.user().ID, s.auth.ClientAddr(c.r), id, req); err != nil {
				s.fail(c.w, err)
				return
			}
			s.writeProvider(c, http.StatusOK, id)
			return
		}
	}
	if !s.canRepos(c) {
		s.forbidden(c)
		return
	}
	s.saveRepoSide(c)
}

// adminDeleteProvider removes a provider with the links people made to it. A
// repository provider the site requires, or that has spaces on it, stays
// (in_use); a sign-in provider that is somebody's only way in stays unless
// force=1.
func (s *server) adminDeleteProvider(c *call) {
	ctx := c.r.Context()
	id := c.r.PathValue("id")
	force := false
	if q := c.r.URL.Query(); q.Has("force") {
		switch q.Get("force") {
		case "1":
			force = true
		case "0":
		default:
			writeError(c.w, http.StatusBadRequest, "invalid", "Force is 1 or 0.")
			return
		}
	}
	p, hasRepo := s.registry().Get(id)
	if hasRepo && !s.canRepos(c) {
		s.forbidden(c)
		return
	}
	// Whether the row signs people in is read from the database whoever asks:
	// removing such a row ends sign-ins, which only who manages sign-in may do.
	signin := false
	if _, err := store.GetProvider(ctx, s.opt.Store.Q(), id); err == nil {
		signin = true
	} else if !errors.Is(err, store.ErrNotFound) {
		s.fail(c.w, err)
		return
	}
	if signin && !s.canSignin(c) {
		if !hasRepo {
			s.noSuchProvider(c, id)
			return
		}
		writeError(c.w, http.StatusForbidden, "forbidden", "People sign in with "+p.Descriptor().Name+": removing it takes the permission to manage sign-in (site.signin.manage).")
		return
	}
	if hasRepo && p.Descriptor().Type != connections.GitID {
		if r := s.spaces.Storage().Remotes.Require; r != nil && *r == id {
			s.connError(c, *r, http.StatusConflict, connections.CodeInUse, "The site requires a remote on "+p.Descriptor().Name+": change remotes.require in the storage settings before removing it.")
			return
		}
	}
	// The count of spaces made before is a quick answer; the deletion counts
	// them again in its own transaction, with the row locked.
	inUse := func(err error) bool {
		var iu *store.ProviderInUseError
		if !errors.As(err, &iu) {
			return false
		}
		e := connections.Errorf(id, http.StatusConflict, connections.CodeInUse, "%d space(s) sync through it: switch or disconnect those first.", iu.Spaces)
		e.Counts = map[string]int{"spaces": iu.Spaces}
		s.failConnection(c.w, e)
		return true
	}
	switch {
	case hasRepo && p.Descriptor().Type == "github":
		gh, ok := p.(interface {
			PrepareDelete(ctx context.Context) (func(context.Context), error)
		})
		if !ok {
			s.noSuchProvider(c, id)
			return
		}
		finish, err := gh.PrepareDelete(ctx)
		if err != nil {
			s.fail(c.w, err)
			return
		}
		if signin {
			// The row is the sign-in provider's too: its removal asks about the
			// people who can sign in only with it, ends their sessions and drops
			// the links with the row.
			if err := s.auth.DeleteProvider(ctx, c.user().ID, s.auth.ClientAddr(c.r), id, force); err != nil {
				if !inUse(err) {
					s.fail(c.w, err)
				}
				return
			}
		} else if finish != nil {
			if err := store.DeleteGitHubProvider(ctx, s.opt.Store.Q()); err != nil {
				if !inUse(err) {
					s.fail(c.w, err)
				}
				return
			}
		}
		if finish != nil {
			finish(ctx)
			s.providerChanged(c, p, "removed")
		}
	case signin:
		if err := s.auth.DeleteProvider(ctx, c.user().ID, s.auth.ClientAddr(c.r), id, force); err != nil {
			if !inUse(err) {
				s.fail(c.w, err)
			}
			return
		}
	case hasRepo:
		a, ok := p.(connections.Administered)
		if !ok {
			s.connError(c, id, http.StatusNotFound, connections.CodeNotFound, "This provider has no settings.")
			return
		}
		if err := a.AdminDelete(ctx); err != nil {
			s.fail(c.w, err)
			return
		}
		s.providerChanged(c, p, "removed")
	default:
		s.noSuchProvider(c, id)
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
}

// adminCheckProvider reads the provider's configuration from the platform
// again: the discovery document or metadata of a sign-in provider, the version
// and client of a host, the App's permissions and installations of GitHub.
func (s *server) adminCheckProvider(c *call) {
	ctx := c.r.Context()
	id := c.r.PathValue("id")
	p, hasRepo := s.registry().Get(id)
	hasRepo = hasRepo && s.canRepos(c)
	var signin bool
	if s.canSignin(c) {
		_, err := s.auth.Provider(ctx, id)
		signin = err == nil
	}
	if hasRepo {
		if a, ok := p.(connections.Administered); ok {
			if _, err := a.AdminCheck(ctx); err != nil {
				var ce *connections.Error
				// An App that is not set up is nothing to check; its sign-in side still is.
				if !(signin && errors.As(err, &ce) && ce.Code == connections.CodeNotConfigured) {
					s.fail(c.w, err)
					return
				}
			}
		}
	}
	if signin {
		if _, err := s.auth.RefreshProvider(ctx, id); err != nil {
			s.fail(c.w, err)
			return
		}
	}
	if !signin && !hasRepo {
		s.noSuchProvider(c, id)
		return
	}
	s.writeProvider(c, http.StatusOK, id)
}

// adminProviderManifest starts making the App on the platform in one click:
// the answer is the manifest and the address the browser posts it to.
func (s *server) adminProviderManifest(c *call) {
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

// adminProviderManifestRetry saves the App GitHub made when the server could
// not save it then.
func (s *server) adminProviderManifestRetry(c *call) {
	p, m, ok := s.manifestMaker(c)
	if !ok {
		return
	}
	if _, err := m.RetryManifest(c.r.Context(), c.user().ID); err != nil {
		s.fail(c.w, err)
		return
	}
	s.providerChanged(c, p, "saved")
	s.spaces.ConnectionFixed("")
	s.writeProvider(c, http.StatusOK, p.Descriptor().ID)
}

// --- a person's linked accounts ----------------------------------------------------------

// accountLinkResp is a link of the signed-in person: an account at a provider
// that signs them in, holds their repositories, or both.
type accountLinkResp struct {
	signinLinkResp
	Login string `json:"login"`
	// Repos: repositories are connected through this link (its tokens), or were
	// and the person must authorise again (state expired).
	Repos          bool   `json:"repos"`
	State          string `json:"state"`
	RefreshExpires string `json:"refreshExpires,omitempty"`
	Spaces         int    `json:"spaces"`
	Admin          bool   `json:"admin,omitempty"`
}

// listAccountLinks lists the person's links, and the providers they may connect
// repositories at (with their own connection and reminders).
func (s *server) listAccountLinks(c *call) {
	ctx := c.r.Context()
	rows, err := store.ListUserLinks(ctx, s.opt.Store.Q(), c.user().ID)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	links := make([]accountLinkResp, 0, len(rows))
	for _, l := range rows {
		name, slug, kind, ok := s.auth.ProviderInfo(l.ProviderID)
		if !ok {
			if p, found := s.registry().Get(l.ProviderID); found {
				d := p.Descriptor()
				name, slug, kind = d.Name, d.ID, d.Type
			}
		}
		r := accountLinkResp{signinLinkResp: signinLinkResp{ID: l.ID, ProviderID: l.ProviderID, Provider: name, Slug: slug, Kind: kind,
			Label: l.Label, SignIn: l.SignIn, Created: rfc3339(l.Created)}, Login: l.Login, Repos: l.Repos, State: l.State, Admin: l.Admin}
		if l.Repos {
			if r.Spaces, err = store.CountUserProviderSpaces(ctx, s.opt.Store.Q(), l.ProviderID, c.user().ID); err != nil {
				s.fail(c.w, err)
				return
			}
			if !l.RefreshExpires.IsZero() {
				r.RefreshExpires = rfc3339(l.RefreshExpires)
			}
		}
		links = append(links, r)
	}
	list := s.registry().ListFor(c.tenant())
	provs := make([]providerResp, 0, len(list))
	for _, p := range list {
		in, err := p.Info(ctx, c.user().ID)
		if err != nil {
			s.fail(c.w, err)
			return
		}
		provs = append(provs, toProvider(p.Descriptor(), in))
	}
	writeJSON(c.w, http.StatusOK, struct {
		Links     []accountLinkResp `json:"links"`
		Providers []providerResp    `json:"providers"`
	}{links, provs})
}

// accountLinkStart starts linking the signed-in person to an account at a
// provider for a purpose: "signin" (the account signs them in; one account per
// provider, a second is refused with already_linked until the first is
// unlinked) or "repos" (their repositories are connected through it, on the
// same link when they have one; GitHub, GitLab, Gitea). It answers the address
// to send the browser to.
func (s *server) accountLinkStart(c *call) {
	var req struct {
		Purpose string `json:"purpose"`
		Return  string `json:"return"`
		Install bool   `json:"install"`
	}
	if !s.readJSONMax(c.w, c.r, &req, true, authBodyLimit) {
		return
	}
	id := c.r.PathValue("provider")
	purpose := req.Purpose
	if purpose == "" {
		purpose = "repos"
		if _, _, _, ok := s.auth.ProviderInfo(id); ok {
			purpose = "signin"
		}
	}
	switch purpose {
	case "signin":
		u := c.user()
		res, err := s.auth.LinkBegin(c.r.Context(), id, &u, req.Return, s.auth.ClientAddr(c.r))
		if err != nil {
			s.fail(c.w, err)
			return
		}
		http.SetCookie(c.w, res.Cookie)
		if res.Binding != nil {
			http.SetCookie(c.w, res.Binding)
		}
		writeJSON(c.w, http.StatusOK, struct {
			URL string `json:"url"`
		}{res.URL})
	case "repos":
		if _, ok := s.registry().GetFor(id, c.tenant()); !ok {
			if _, _, _, signin := s.auth.ProviderInfo(id); signin {
				writeError(c.w, http.StatusBadRequest, "invalid", "This provider holds no repositories: purpose is signin.")
				return
			}
		}
		p, ok := s.provider(c)
		if !ok {
			return
		}
		// An installation through the install page connects it to the person: the
		// GitHub service asks the hooks whether they may (the feature).
		ctx := connections.WithInstallHooks(c.r.Context(), s.githubHooks(c))
		st, err := p.StartConnect(ctx, c.user().ID, connections.StartRequest{Return: req.Return, Install: req.Install})
		if err != nil {
			s.refuse(c.w, err)
			return
		}
		http.SetCookie(c.w, s.auth.FlowCookie(connCookieBase+p.Descriptor().ID, st.Binding, connAttemptTTL))
		writeJSON(c.w, http.StatusOK, struct {
			URL string `json:"url"`
		}{st.URL})
	default:
		writeError(c.w, http.StatusBadRequest, "invalid", "Purpose is signin or repos.")
	}
}

// requirement is the site's requirement of a remote now ("" when none).
func (s *server) requirement() string {
	if s.opt.Spaces == nil {
		return ""
	}
	if r := s.opt.Spaces.Storage().Remotes.Require; r != nil {
		return *r
	}
	return ""
}

// accessLost tells the spaces manager that these spaces lost the access their
// remote synced with (what a provider known to the registry does itself).
func (s *server) accessLost(ids []string) {
	if s.opt.Spaces != nil && len(ids) > 0 {
		s.opt.Spaces.AccessLost(ids)
	}
}

// disconnectRepos drops the person's repository tokens at the link's provider
// (the host is asked to revoke them, best effort) and audits it. A link that
// only held repositories is gone with them; one that signs them in stays.
func (s *server) disconnectRepos(c *call, l *store.UserLink) error {
	ctx := c.r.Context()
	if p, ok := s.registry().Get(l.ProviderID); ok {
		if err := p.Disconnect(ctx, l.UserID); err != nil {
			return err
		}
	} else {
		ids, err := store.DeleteRepoLink(ctx, s.opt.Store.Q(), l.ProviderID, l.UserID, s.requirement(), s.now())
		if err != nil {
			return err
		}
		s.accessLost(ids)
	}
	s.audit(c, auth.AuditEvent{Kind: "account_disconnected", Actor: c.user().ID, User: c.user().ID,
		Detail: map[string]any{"provider": l.ProviderID, "login": l.Login}})
	return nil
}

// myLink is the {id} of the path as one of the caller's links, or a 404.
func (s *server) myLink(c *call) (*store.UserLink, bool) {
	l, err := store.GetUserLink(c.r.Context(), s.opt.Store.Q(), c.r.PathValue("id"), c.user().ID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(c.w, http.StatusNotFound, "not_found", "No such linked account.")
		return nil, false
	}
	if err != nil {
		s.fail(c.w, err)
		return nil, false
	}
	return l, true
}

// unlinkAccount removes one of the caller's links, whatever it is for: what it
// connected is disconnected (the tokens are revoked), and what it signed in
// with is unlinked, unless that is the last way they can sign in (last_method).
// Nothing is changed when that is refused.
func (s *server) unlinkAccount(c *call) {
	l, ok := s.myLink(c)
	if !ok {
		return
	}
	ctx := c.r.Context()
	if !l.SignIn {
		if l.Repos {
			if err := s.disconnectRepos(c, l); err != nil {
				s.fail(c.w, err)
				return
			}
		}
		c.w.WriteHeader(http.StatusNoContent)
		return
	}
	// A sign-in: whether it may go and its removal are one transaction, and its
	// tokens go with the row. What the platform is asked to revoke is read
	// first, and asked only once the row is gone: a refused removal keeps the
	// tokens and the grant.
	var held *store.RepoLink
	if l.Repos {
		a, err := store.GetRepoLink(ctx, s.opt.Store.Q(), l.ProviderID, l.UserID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			s.fail(c.w, err)
			return
		}
		held = a
	}
	if err := s.auth.UnlinkAccount(ctx, l.UserID, l.ID); err != nil {
		s.fail(c.w, err)
		return
	}
	if held != nil {
		if p, ok := s.registry().Get(l.ProviderID); ok {
			if r, ok := p.(connections.Revoker); ok {
				r.RevokeRemoved(ctx, held)
			}
		}
		s.audit(c, auth.AuditEvent{Kind: "account_disconnected", Actor: c.user().ID, User: c.user().ID,
			Detail: map[string]any{"provider": l.ProviderID, "login": l.Login}})
	}
	c.w.WriteHeader(http.StatusNoContent)
}

// unlinkAccountRepos drops the tokens of a link and keeps what else it is for:
// the person's sign-in stays, a link that only held repositories goes.
func (s *server) unlinkAccountRepos(c *call) {
	l, ok := s.myLink(c)
	if !ok {
		return
	}
	if !l.Repos {
		writeError(c.w, http.StatusNotFound, "not_found", "No repositories are connected through this link.")
		return
	}
	if err := s.disconnectRepos(c, l); err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
}

// adminTestProvider starts a test sign-in: the administrator signs in at the
// provider (on or off) and sees what it says, with nothing made or linked.
func (s *server) adminTestProvider(c *call) {
	u := c.user()
	res, err := s.auth.TestBegin(c.r.Context(), c.r.PathValue("id"), &u, s.auth.ClientAddr(c.r))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	http.SetCookie(c.w, res.Cookie)
	if res.Binding != nil {
		http.SetCookie(c.w, res.Binding)
	}
	writeJSON(c.w, http.StatusOK, struct {
		URL string `json:"url"`
	}{res.URL})
}

// adminTestResult is what the administrator's last test sign-in found: once.
func (s *server) adminTestResult(c *call) {
	c.w.Header().Set("Cache-Control", "no-store")
	type result struct {
		Subject      string `json:"subject"`
		Email        string `json:"email"`
		EmailTrusted bool   `json:"emailTrusted"`
		Name         string `json:"name"`
		Username     string `json:"username"`
		At           string `json:"at"`
	}
	var out *result
	if r := s.auth.TestResultOf(c.user().ID, c.r.PathValue("id")); r != nil {
		out = &result{r.Subject, r.Email, r.EmailTrusted, r.Name, r.Username, rfc3339(r.At)}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Result *result `json:"result"`
	}{out})
}
