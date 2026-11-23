package api

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/KKazuhaK/PetalBook/internal/auth"
	"github.com/KKazuhaK/PetalBook/internal/spaces"
	"github.com/KKazuhaK/PetalBook/internal/store"
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
}

// toUser is the user as the API shows it. passkeyRequired is whether the
// person must use a passkey, by themselves or by their role.
func (s *server) toUser(u store.User) userResp {
	req := u.PasskeyRequired
	if s.auth != nil {
		req = s.auth.PasskeyRequiredOf(u)
	}
	return userResp{u.ID, u.Username, u.Name, u.Email, u.Role, u.Disabled, rfc3339(u.Created), u.EmailVerified, req}
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
}

func toSpaces(in []spaces.Info) []spaceResp {
	out := make([]spaceResp, len(in))
	for i, s := range in {
		out[i] = spaceResp{s.ID, s.Name, s.Role, s.Perms.Names(), s.Personal, s.Sync, s.Inbox, s.Disabled, s.DirectRole, s.ViaGroups}
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
	writeJSON(c.w, http.StatusOK, struct {
		Version   string      `json:"version"`
		MaxUpload int64       `json:"maxUpload"`
		MaxImport int64       `json:"maxImport"`
		Spaces    []spaceResp `json:"spaces"`
	}{s.opt.Version, s.opt.MaxUpload, s.opt.MaxImport, list})
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
		Mode    string    `json:"mode"`
		User    *userResp `json:"user"`
		Setup   bool      `json:"setup"`
		Hold    *string   `json:"hold"`
		Methods *methods  `json:"methods,omitempty"`
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
		Role    string `json:"role"`
		Expires string `json:"expires"`
	}{inv.Role, rfc3339(inv.Expires)})
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
	u, err := s.auth.UpdateProfile(c.r.Context(), c.user(), req.Name, req.Email)
	if err != nil {
		s.fail(c.w, err)
		return
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

func (s *server) listUsers(c *call) {
	sc, ok := s.adminScope(c)
	if !ok {
		return
	}
	q := c.r.URL.Query()
	var us []store.User
	var err error
	enabledOnly := false
	if q.Has("enabled") {
		switch q.Get("enabled") {
		case "1":
			enabledOnly = true
		case "0":
		default:
			writeError(c.w, http.StatusBadRequest, "invalid", "enabled is 1 or 0")
			return
		}
	}
	if q.Has("q") || q.Has("limit") || enabledOnly {
		limit := 0
		if v := q.Get("limit"); q.Has("limit") {
			n, aerr := strconv.Atoi(v)
			if aerr != nil || n < 1 || n > maxAdminLimit {
				writeError(c.w, http.StatusBadRequest, "invalid", "limit is a number from 1 to "+strconv.Itoa(maxAdminLimit))
				return
			}
			limit = n
		}
		us, err = s.auth.FindUsers(c.r.Context(), sc, strings.TrimSpace(q.Get("q")), enabledOnly, limit)
	} else {
		us, err = s.auth.Users(c.r.Context(), sc)
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	counts, err := s.auth.UserCounts(c.r.Context())
	if err != nil {
		s.fail(c.w, err)
		return
	}
	type userEx struct {
		userResp
		Passkeys int `json:"passkeys"`
		SSO      int `json:"sso"`
	}
	out := make([]userEx, len(us))
	for i, u := range us {
		out[i] = userEx{s.toUser(u), counts.Passkeys[u.ID], counts.SSO[u.ID]}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Users []userEx `json:"users"`
	}{out})
}

func (s *server) patchUser(c *call) {
	var req struct {
		Role            *string `json:"role"`
		Disabled        *bool   `json:"disabled"`
		PasskeyRequired *bool   `json:"passkeyRequired"`
		Email           *string `json:"email"`
		EmailVerified   *bool   `json:"emailVerified"`
		SiteAdmin       *bool   `json:"siteAdmin"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	u, err := s.auth.UpdateUser(c.r.Context(), c.user(), s.auth.ClientAddr(c.r), c.r.PathValue("id"), auth.UserPatch{Role: req.Role, Disabled: req.Disabled,
		PasskeyRequired: req.PasskeyRequired, Email: req.Email, EmailVerified: req.EmailVerified, SiteAdmin: req.SiteAdmin})
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		User userResp `json:"user"`
	}{s.toUser(*u)})
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
		Name  string `json:"name"`
		Inbox string `json:"inbox"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	in, err := s.spaces.CreateWith(c.r.Context(), c.user().ID, req.Name, req.Inbox)
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
		writeError(c.w, http.StatusBadRequest, "invalid", "give a name, an inbox folder, or both")
		return
	}
	in, err := s.spaces.Update(c.r.Context(), c.user(), c.sp.Info.ID, req.Name, req.Inbox)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, toSpaces([]spaces.Info{in})[0])
}
