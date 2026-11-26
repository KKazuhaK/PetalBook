package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"petalbook.app/server/internal/auth"
	"petalbook.app/server/internal/connections"
	"petalbook.app/server/internal/spaces"
	"petalbook.app/server/internal/store"
)

type userResp struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Role     string `json:"role"`
	Disabled bool   `json:"disabled"`
	Created  string `json:"created"`
	// EmailVerified and PasskeyRequired: see docs/api.md, "User".
	EmailVerified   bool `json:"emailVerified"`
	PasskeyRequired bool `json:"passkeyRequired"`
	// SiteAdmin is an administrator of the whole server; Tenant is the tenant
	// the user belongs to (absent in single-user mode, which has none).
	SiteAdmin bool           `json:"siteAdmin"`
	Tenant    *tenantRefResp `json:"tenant,omitempty"`
	// Quota is the person's own limits (null follows the tenant); the
	// administrators' lists and PATCH answer have it, nothing else does.
	Quota *ownQuotaResp `json:"quota,omitempty"`
	// RemoteExempt: the storage policy does not ask the spaces this user owns
	// for a remote (absent when false).
	RemoteExempt bool `json:"remoteExempt,omitempty"`
}

// toUser is the user as the API shows it. passkeyRequired is whether the
// person must use a passkey, by themselves or by their role.
func (s *server) toUser(u store.User) userResp {
	req := u.PasskeyRequired
	if s.auth != nil {
		req = s.auth.PasskeyRequiredOf(u)
	}
	r := userResp{u.ID, u.Username, u.Name, u.Email, u.Role, u.Disabled, rfc3339(u.Created), u.EmailVerified, req, u.Role == "admin" && u.SiteAdmin, nil, nil, u.RemoteExempt}
	if !s.single {
		r.Tenant = s.toTenantRef(u.TenantID)
	}
	return r
}

type spaceResp struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Role is the caller's role as a member; "" for an administrator who is not one.
	Role string `json:"role"`
	// Permissions is what the caller may do in the space; clients show and hide
	// controls by these, never by the role's name.
	Permissions []string `json:"permissions"`
	Personal    bool     `json:"personal"`
	Sync        string   `json:"sync"`
	Inbox       string   `json:"inbox"`
	// Disabled: an administrator switched the space off; it is listed but cannot be opened.
	Disabled bool `json:"disabled"`
	// DirectRole is the caller's own role, "" when they reach the space only
	// through groups; ViaGroups is whether a group of theirs is a member.
	DirectRole string `json:"directRole"`
	ViaGroups  bool   `json:"viaGroups"`
	// Storage is what the space's copy on the server is and what the storage
	// policy asks of it (see "Server storage"); absent in single-user mode.
	Storage *spaceStorageResp `json:"storage,omitempty"`
}

// spaceStorageResp is the storage block of a Space.
type spaceStorageResp struct {
	Cache  string      `json:"cache"`
	Assets string      `json:"assets"`
	Remote *remoteNote `json:"remote"`
}

// remoteNote is a requirement a space does not meet yet.
type remoteNote struct {
	Required string `json:"required"`
	Deadline string `json:"deadline"`
	Overdue  bool   `json:"overdue"`
}

func toNote(n *spaces.RemoteNotice) *remoteNote {
	if n == nil {
		return nil
	}
	return &remoteNote{n.Required, rfc3339(n.Deadline), n.Overdue}
}

func toSpaces(in []spaces.Info) []spaceResp {
	out := make([]spaceResp, len(in))
	for i, s := range in {
		out[i] = spaceResp{ID: s.ID, Name: s.Name, Role: s.Role, Permissions: s.Perms.Names(), Personal: s.Personal, Sync: s.Sync, Inbox: s.Inbox, Disabled: s.Disabled,
			DirectRole: s.DirectRole, ViaGroups: s.ViaGroups}
		if s.Cache != "" {
			assets := s.Assets
			if assets == "" {
				assets = "space"
			}
			out[i].Storage = &spaceStorageResp{Cache: s.Cache, Assets: assets, Remote: toNote(s.RemoteNotice)}
		}
	}
	return out
}

// --- meta and session --------------------------------------------------------

func (s *server) getMeta(c *call) {
	list := []spaceResp{}
	if c.p != nil {
		l, err := s.visibleSpaces(c)
		if err != nil {
			s.fail(c.w, err)
			return
		}
		list = toSpaces(l)
	}
	// maxUpload is the largest file for the caller's tenant (the site's for a
	// caller who is not signed in).
	maxUpload := s.opt.MaxUpload
	if c.p != nil {
		maxUpload = s.fileLimit(c)
	}
	writeJSON(c.w, http.StatusOK, struct {
		Version   string      `json:"version"`
		MaxUpload int64       `json:"maxUpload"`
		MaxImport int64       `json:"maxImport"`
		Spaces    []spaceResp `json:"spaces"`
	}{s.opt.Version, maxUpload, s.opt.MaxImport, list})
}

func (s *server) getSession(c *call) {
	c.w.Header().Set("Cache-Control", "no-store")
	type methods struct {
		Password bool `json:"password"`
		OIDC     *struct {
			Name string `json:"name"`
		} `json:"oidc"`
	}
	out := struct {
		Mode  string    `json:"mode"`
		User  *userResp `json:"user"`
		Setup bool      `json:"setup"`
		Hold  *string   `json:"hold"`
		// EndedReason is why the session of the request's cookie ended, for a visitor
		// who is not signed in because of it; absent otherwise.
		EndedReason string   `json:"endedReason,omitempty"`
		Methods     *methods `json:"methods,omitempty"`
		// Token is the API token the request was made with; null for a session.
		Token *struct {
			ID     string   `json:"id"`
			Name   string   `json:"name"`
			Access string   `json:"access"`
			Spaces []string `json:"spaces"`
		} `json:"token"`
	}{Mode: "single"}
	if c.p != nil {
		u := s.toUser(c.user())
		out.User = &u
		if c.p.Held {
			h := "passkey"
			out.Hold = &h
		}
		if g := c.p.Grant; g != nil {
			out.Token = &struct {
				ID     string   `json:"id"`
				Name   string   `json:"name"`
				Access string   `json:"access"`
				Spaces []string `json:"spaces"`
			}{g.ID, g.Name, g.Access(), g.Spaces}
		}
	}
	if !s.single {
		out.Mode = "accounts"
		if c.p == nil {
			out.EndedReason = c.ended
		}
		setup, err := s.auth.SetupOpen(c.r.Context())
		if err != nil {
			s.fail(c.w, err)
			return
		}
		out.Setup = setup
		m := &methods{Password: true}
		if name := s.auth.LegacyOIDCName(); name != "" {
			m.OIDC = &struct {
				Name string `json:"name"`
			}{name}
		}
		out.Methods = m
	}
	writeJSON(c.w, http.StatusOK, out)
}

// --- signing in --------------------------------------------------------------

// startSession sends the cookie of a new session.
func (s *server) startSession(c *call, token string) {
	http.SetCookie(c.w, s.auth.SessionCookie(token))
}

type credentials struct {
	Token    string              `json:"token"`
	Username string              `json:"username"`
	Name     string              `json:"name"`
	Password string              `json:"password"`
	Captcha  *auth.CaptchaAnswer `json:"captcha"`
}

func (s *server) setup(c *call) {
	var req credentials
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	u, tok, err := s.auth.Setup(c.r.Context(), req.Token, req.Username, req.Name, req.Password, s.auth.ClientAddr(c.r), req.Captcha)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	s.startSession(c, tok)
	writeJSON(c.w, http.StatusCreated, struct {
		User userResp `json:"user"`
	}{s.toUser(*u)})
}

func (s *server) login(c *call) {
	var req credentials
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	res, err := s.auth.Login(c.r.Context(), auth.LoginParams{Username: req.Username, Password: req.Password, IP: s.auth.ClientAddr(c.r), Captcha: req.Captcha})
	if err != nil {
		s.fail(c.w, err)
		return
	}
	s.startSession(c, res.Token)
	writeJSON(c.w, http.StatusOK, struct {
		User userResp `json:"user"`
	}{s.toUser(*res.User)})
}

func (s *server) logout(c *call) {
	if ck, err := c.r.Cookie(s.auth.SessionCookieName()); err == nil {
		s.auth.Logout(c.r.Context(), ck.Value)
	}
	http.SetCookie(c.w, s.auth.ClearSessionCookie())
	c.w.WriteHeader(http.StatusNoContent)
}

func (s *server) checkInvite(c *call) {
	var req credentials
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	inv, err := s.auth.CheckInvite(c.r.Context(), req.Token, s.auth.ClientAddr(c.r), req.Captcha)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Role     string `json:"role"`
		Expires  string `json:"expires"`
		Username string `json:"username"`
		Name     string `json:"name"`
		Email    string `json:"email"`
	}{inv.Role, rfc3339(inv.Expires), inv.Username, inv.Name, inv.Email})
}

func (s *server) acceptInvite(c *call) {
	var req credentials
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	u, tok, err := s.auth.AcceptInvite(c.r.Context(), req.Token, req.Username, req.Name, req.Password, s.auth.ClientAddr(c.r), req.Captcha)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	s.startSession(c, tok)
	writeJSON(c.w, http.StatusCreated, struct {
		User userResp `json:"user"`
	}{s.toUser(*u)})
}

// --- the pre-2.8 OpenID Connect endpoints ------------------------------------

// oidcStart is POST /api/auth/oidc/start: unchanged, for the provider with the legacy callback.
func (s *server) oidcStart(c *call) {
	s.ssoStartJSON(c, auth.KindOIDC, "")
}

// oidcCallback is GET /api/auth/oidc/callback, for the same provider.
func (s *server) oidcCallback(c *call) {
	s.oidcCallbackOf(c, "")
}

// --- account -----------------------------------------------------------------

type passkeyResp struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Created  string  `json:"created"`
	LastUsed *string `json:"lastUsed"`
	Synced   bool    `json:"synced"`
}

func toPasskeys(in []auth.PasskeyView) []passkeyResp {
	out := make([]passkeyResp, len(in))
	for i, p := range in {
		out[i] = passkeyResp{ID: p.ID, Name: p.Name, Created: rfc3339(p.Created), Synced: p.Synced}
		if !p.LastUsed.IsZero() {
			t := rfc3339(p.LastUsed)
			out[i].LastUsed = &t
		}
	}
	return out
}

func (s *server) getAccount(c *call) {
	v, err := s.auth.Account(c.r.Context(), c.user())
	if err != nil {
		s.fail(c.w, err)
		return
	}
	type ident struct {
		ID           string `json:"id"`
		Provider     string `json:"provider"`
		ProviderSlug string `json:"providerSlug"`
		Kind         string `json:"kind"`
		Label        string `json:"label"`
		Created      string `json:"created"`
	}
	ids := make([]ident, len(v.Identities))
	for i, x := range v.Identities {
		ids[i] = ident{x.ID, x.Provider, x.Slug, x.Kind, x.Label, rfc3339(x.Created)}
	}
	groups, err := s.spaces.GroupsOf(c.r.Context(), c.user().ID)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		User        userResp      `json:"user"`
		HasPassword bool          `json:"hasPassword"`
		Identities  []ident       `json:"identities"`
		Passkeys    []passkeyResp `json:"passkeys"`
		Groups      []groupRef    `json:"groups"`
	}{s.toUser(v.User), v.HasPassword, ids, toPasskeys(v.Passkeys), toGroupRefs(groups)})
}

func (s *server) patchAccount(c *call) {
	var req struct {
		Name  *string `json:"name"`
		Email *string `json:"email"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	old := c.user()
	u, err := s.auth.UpdateProfile(c.r.Context(), old, req.Name, req.Email)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	if u.Email != old.Email {
		// The verified old address is told, with no link in the note.
		s.auth.NoteEmailChange(old, langOf(c, ""), s.auth.ClientAddr(c.r))
	}
	writeJSON(c.w, http.StatusOK, struct {
		User userResp `json:"user"`
	}{s.toUser(*u)})
}

func (s *server) changePassword(c *call) {
	var req struct {
		Current *string `json:"current"`
		New     string  `json:"new"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	if err := s.auth.ChangePassword(c.r.Context(), c.p, req.Current, req.New, s.auth.ClientAddr(c.r)); err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
}

func (s *server) unlinkIdentity(c *call) {
	if err := s.auth.UnlinkIdentity(c.r.Context(), c.user().ID, c.r.PathValue("id")); err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
}

// --- users and invites (administrators) --------------------------------------

// userEx is a user as the administration's list shows them.
type userEx struct {
	userResp
	Passkeys   int    `json:"passkeys"`
	SSO        int    `json:"sso"`
	LastActive string `json:"lastActive,omitempty"`
}

func (s *server) userEx(e store.UserRow) userEx {
	out := userEx{userResp: s.toUser(e.User), Passkeys: e.Passkeys, SSO: e.Identities}
	out.Quota = toOwnQuota(e.Quota)
	if !e.LastActive.IsZero() {
		out.LastActive = rfc3339(e.LastActive)
	}
	return out
}

// listUsers is one keyset page of the people in the administrator's scope
// (docs/api.md, "GET /api/users"): a search, filters and a sort, each over an
// index, and the counts read for the page's ids alone.
func (s *server) listUsers(c *call) {
	sc, ok := s.adminScope(c)
	if !ok {
		return
	}
	q := c.r.URL.Query()
	limit, present, ok := intParam(c, "limit", 1, maxAdminLimit)
	if !ok {
		return
	}
	if !present {
		if q.Has("limit") {
			writeError(c.w, http.StatusBadRequest, "invalid", "The limit is a number from 1 to "+strconv.Itoa(maxAdminLimit)+".")
			return
		}
		limit = defaultAdminLimit
	}
	f := store.UserFilter{Query: strings.TrimSpace(q.Get("q")), After: q.Get("cursor"), Limit: limit, Order: q.Get("order")}
	if f.Order != "" && f.Order != "asc" && f.Order != "desc" {
		writeError(c.w, http.StatusBadRequest, "invalid", "Order is asc or desc.")
		return
	}
	if f.Sort, ok = oneOf(c, "sort", store.UserSorts); !ok {
		return
	}
	if f.Role, ok = oneOf(c, "role", store.UserRoles); !ok {
		return
	}
	if f.State, ok = oneOf(c, "state", store.UserStates); !ok {
		return
	}
	// The older picker's way of asking for the accounts that are enabled.
	switch q.Get("enabled") {
	case "":
	case "1":
		f.State = "enabled"
	case "0":
	default:
		writeError(c.w, http.StatusBadRequest, "invalid", "Enabled is 1 or 0.")
		return
	}
	if days, present, ok := intParam(c, "inactiveDays", 0, 36500); !ok {
		return
	} else if present {
		f.InactiveBefore = s.now().Add(-time.Duration(days) * 24 * time.Hour)
	}
	if days, present, ok := intParam(c, "activeDays", 0, 36500); !ok {
		return
	} else if present {
		f.ActiveSince = s.now().Add(-time.Duration(days) * 24 * time.Hour)
	}
	rows, next, err := store.ListUserRows(c.r.Context(), s.opt.Store.Q(), sc, f)
	if s.badList(c, err) {
		return
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	out := make([]userEx, len(rows))
	for i, e := range rows {
		out[i] = s.userEx(e)
	}
	writeJSON(c.w, http.StatusOK, struct {
		Users []userEx `json:"users"`
		Next  string   `json:"next"`
	}{out, next})
}

// getUser is one person as the list shows them.
func (s *server) getUser(c *call) {
	e, err := store.GetUserRow(c.r.Context(), s.opt.Store.Q(), store.AdminScopeOf(c.user()), c.r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(c.w, http.StatusNotFound, "not_found", "No such user.")
		return
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		User userEx `json:"user"`
	}{s.userEx(e)})
}

func (s *server) patchUser(c *call) {
	var req struct {
		Role            *string         `json:"role"`
		Disabled        *bool           `json:"disabled"`
		PasskeyRequired *bool           `json:"passkeyRequired"`
		Email           *string         `json:"email"`
		EmailVerified   *bool           `json:"emailVerified"`
		SiteAdmin       *bool           `json:"siteAdmin"`
		Quota           json.RawMessage `json:"quota"`
		RemoteExempt    *bool           `json:"remoteExempt"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	// A refused quota changes nothing, so it is checked before the rest is applied.
	var applyQuota func() bool
	if req.Quota != nil {
		var ok bool
		if applyQuota, ok = s.checkQuotaPatch(c, c.r.PathValue("id"), req.Quota); !ok {
			return
		}
	}
	u, err := s.auth.UpdateUser(c.r.Context(), c.user(), s.auth.ClientAddr(c.r), c.r.PathValue("id"), auth.UserPatch{Role: req.Role, Disabled: req.Disabled,
		PasskeyRequired: req.PasskeyRequired, Email: req.Email, EmailVerified: req.EmailVerified, SiteAdmin: req.SiteAdmin, RemoteExempt: req.RemoteExempt})
	if err != nil {
		s.fail(c.w, err)
		return
	}
	if applyQuota != nil && !applyQuota() {
		return
	}
	out := s.toUser(*u)
	if raw, err := store.UserQuota(c.r.Context(), s.opt.Store.Q(), store.AllTenants(), u.ID); err == nil {
		out.Quota = toOwnQuota(raw)
	}
	writeJSON(c.w, http.StatusOK, struct {
		User userResp `json:"user"`
	}{out})
}

func (s *server) createInvite(c *call) {
	var req struct {
		Role      string `json:"role"`
		Days      *int   `json:"days"`
		Tenant    string `json:"tenant"`
		SiteAdmin *bool  `json:"siteAdmin"`
	}
	if !s.readJSONMax(c.w, c.r, &req, true, authBodyLimit) {
		return
	}
	inv, err := s.auth.CreateInvite(c.r.Context(), c.user(), req.Tenant, req.Role, req.Days, req.SiteAdmin)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusCreated, struct {
		ID        string `json:"id"`
		Token     string `json:"token"`
		Role      string `json:"role"`
		Created   string `json:"created"`
		Expires   string `json:"expires"`
		Tenant    string `json:"tenant"`
		SiteAdmin bool   `json:"siteAdmin"`
	}{inv.ID, inv.Token, inv.Role, rfc3339(inv.Created), rfc3339(inv.Expires), inv.TenantID, inv.SiteAdmin})
}

func (s *server) listInvites(c *call) {
	sc, ok := s.adminScope(c)
	if !ok {
		return
	}
	l, err := s.auth.OpenInvites(c.r.Context(), sc)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	type inv struct {
		ID        string `json:"id"`
		Role      string `json:"role"`
		Created   string `json:"created"`
		Expires   string `json:"expires"`
		CreatedBy string `json:"createdBy"`
		Tenant    string `json:"tenant"`
	}
	out := make([]inv, len(l))
	for i, x := range l {
		out[i] = inv{x.ID, x.Role, rfc3339(x.Created), rfc3339(x.Expires), x.CreatedBy, x.TenantID}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Invites []inv `json:"invites"`
	}{out})
}

func (s *server) deleteInvite(c *call) {
	if err := s.auth.DeleteInvite(c.r.Context(), store.ScopeOf(c.user()), c.r.PathValue("id")); err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
}

// --- spaces ------------------------------------------------------------------

func (s *server) listSpaces(c *call) {
	l, err := s.visibleSpaces(c)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Spaces []spaceResp `json:"spaces"`
	}{toSpaces(l)})
}

func (s *server) createSpace(c *call) {
	var req struct {
		Name    string `json:"name"`
		Inbox   string `json:"inbox"`
		Storage *struct {
			Sync *struct {
				Provider    string `json:"provider"`
				Owner       string `json:"owner"`
				Name        string `json:"name"`
				Private     *bool  `json:"private"`
				Description string `json:"description"`
				Create      *bool  `json:"create"`
				// Assets asks for an attachment repository beside the space's
				// (milestone 3.1): true, false, or {"name": "…"}.
				Assets json.RawMessage `json:"assets"`
			} `json:"sync"`
		} `json:"storage"`
		// Remote connects the new space to an existing repository at a plain
		// address, with a token (milestone 3.1): the body of PUT …/remote.
		Remote *struct {
			URL        string `json:"url"`
			Branch     string `json:"branch"`
			Credential struct {
				Type     string `json:"type"`
				Username string `json:"username"`
				Token    string `json:"token"`
			} `json:"credential"`
		} `json:"remote"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	var in spaces.Info
	var err error
	sync := req.Storage != nil && req.Storage.Sync != nil
	if req.Remote != nil && sync {
		writeError(c.w, http.StatusBadRequest, "invalid", "Give remote or storage.sync, not both.")
		return
	}
	if r := req.Remote; r != nil {
		in, err = s.spaces.CreateWithRemote(c.r.Context(), c.user().ID, req.Name, req.Inbox,
			spaces.ConnectRequest{URL: r.URL, Branch: r.Branch, CredType: r.Credential.Type, Username: r.Credential.Username, Token: r.Credential.Token})
	} else if sync {
		g := req.Storage.Sync
		if p, ok := s.registry().Get(g.Provider); !ok || p.Descriptor().Type == connections.GitID {
			if !ok {
				writeError(c.w, http.StatusBadRequest, "invalid", "storage.sync.provider is not a provider of this server.")
			} else {
				writeError(c.w, http.StatusBadRequest, "invalid", "A plain Git address is given as remote, or connected after the space is made with PUT /api/spaces/{space}/remote.")
			}
			return
		}
		sc := spaces.SyncCreate{Provider: g.Provider, Owner: g.Owner, Name: g.Name, Private: g.Private == nil || *g.Private, Description: g.Description, Create: g.Create == nil || *g.Create}
		assets, aerr := assetsAsked(g.Assets)
		if aerr != nil {
			s.fail(c.w, aerr)
			return
		}
		sc.Assets = assets
		in, err = s.spaces.CreateWithSync(c.r.Context(), c.user().ID, req.Name, req.Inbox, sc)
	} else {
		in, err = s.spaces.CreateWith(c.r.Context(), c.user().ID, req.Name, req.Inbox)
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.Header().Set("Location", "/api/spaces/"+url.PathEscape(in.ID))
	writeJSON(c.w, http.StatusCreated, toSpaces([]spaces.Info{in})[0])
}

func (s *server) renameSpace(c *call) {
	var req struct {
		Name  *string `json:"name"`
		Inbox *string `json:"inbox"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	if req.Name == nil && req.Inbox == nil {
		writeError(c.w, http.StatusBadRequest, "invalid", "Give a name, an inbox folder, or both.")
		return
	}
	in, err := s.spaces.Update(c.r.Context(), c.user(), c.sp.Info.ID, req.Name, req.Inbox)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, toSpaces([]spaces.Info{in})[0])
}

// assetsAsked reads storage.sync.assets: absent (the administrator's default
// applies), true, false, or an object that names the repository.
func assetsAsked(raw json.RawMessage) (*spaces.SyncAssets, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return &spaces.SyncAssets{Default: true}, nil
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		if !b {
			return nil, nil
		}
		return &spaces.SyncAssets{}, nil
	}
	var o struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &o); err != nil || o.Name == "" {
		return nil, fmt.Errorf("%w: storage.sync.assets is true, false or {\"name\": \"…\"}", spaces.ErrInvalid)
	}
	return &spaces.SyncAssets{Name: o.Name}, nil
}
