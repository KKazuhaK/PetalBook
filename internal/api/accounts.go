package api

import (
	"net/http"
	"net/url"

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
}

func toUser(u store.User) userResp {
	return userResp{u.ID, u.Username, u.Name, u.Email, u.Role, u.Disabled, rfc3339(u.Created)}
}

type spaceResp struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	Personal bool   `json:"personal"`
	Sync     string `json:"sync"`
	Inbox    string `json:"inbox"`
}

func toSpaces(in []spaces.Info) []spaceResp {
	out := make([]spaceResp, len(in))
	for i, s := range in {
		out[i] = spaceResp{s.ID, s.Name, s.Role, s.Personal, s.Sync, s.Inbox}
	}
	return out
}

// --- meta and session --------------------------------------------------------

func (s *server) getMeta(c *call) {
	list := []spaceResp{}
	if c.p != nil {
		l, err := s.spaces.List(c.r.Context(), c.user().ID)
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
		Methods *methods  `json:"methods,omitempty"`
	}{Mode: "single"}
	if c.p != nil {
		u := toUser(c.user())
		out.User = &u
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
		if name := s.auth.OIDCName(); name != "" {
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
	Token    string `json:"token"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

func (s *server) setup(c *call) {
	var req credentials
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	u, tok, err := s.auth.Setup(c.r.Context(), req.Token, req.Username, req.Name, req.Password, s.auth.ClientAddr(c.r))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	s.startSession(c, tok)
	writeJSON(c.w, http.StatusCreated, struct {
		User userResp `json:"user"`
	}{toUser(*u)})
}

func (s *server) login(c *call) {
	var req credentials
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	u, tok, err := s.auth.Login(c.r.Context(), req.Username, req.Password, s.auth.ClientAddr(c.r))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	s.startSession(c, tok)
	writeJSON(c.w, http.StatusOK, struct {
		User userResp `json:"user"`
	}{toUser(*u)})
}

func (s *server) logout(c *call) {
	if ck, err := c.r.Cookie(s.auth.SessionCookieName()); err == nil {
		s.auth.Logout(c.r.Context(), ck.Value)
	}
	http.SetCookie(c.w, s.auth.ClearSessionCookie())
	c.w.WriteHeader(http.StatusNoContent)
}

func (s *server) checkInvite(c *call) {
	var req struct {
		Token string `json:"token"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	inv, err := s.auth.CheckInvite(c.r.Context(), req.Token, s.auth.ClientAddr(c.r))
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
	u, tok, err := s.auth.AcceptInvite(c.r.Context(), req.Token, req.Username, req.Name, req.Password, s.auth.ClientAddr(c.r))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	s.startSession(c, tok)
	writeJSON(c.w, http.StatusCreated, struct {
		User userResp `json:"user"`
	}{toUser(*u)})
}

// --- OpenID Connect ----------------------------------------------------------

func (s *server) oidcStart(c *call) {
	var req struct {
		Intent string `json:"intent"`
		Token  string `json:"token"`
		Return string `json:"return"`
	}
	if !s.readJSONMax(c.w, c.r, &req, true, authBodyLimit) {
		return
	}
	bp := auth.BeginParams{Intent: req.Intent, Token: req.Token, Return: req.Return}
	if c.p != nil {
		u := c.user()
		bp.User = &u
	}
	authURL, binding, err := s.auth.OIDCBegin(c.r.Context(), bp, s.auth.ClientAddr(c.r))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	http.SetCookie(c.w, s.auth.OIDCCookie(binding))
	writeJSON(c.w, http.StatusOK, struct {
		URL string `json:"url"`
	}{authURL})
}

func (s *server) oidcCallback(c *call) {
	c.w.Header().Set("Cache-Control", "no-store")
	binding, sessionTok := "", ""
	if ck, err := c.r.Cookie(s.auth.OIDCCookieName()); err == nil {
		binding = ck.Value
	}
	if ck, err := c.r.Cookie(s.auth.SessionCookieName()); err == nil {
		sessionTok = ck.Value
	}
	out := s.auth.OIDCComplete(c.r.Context(), binding, sessionTok, c.r.URL.Query(), s.auth.ClientAddr(c.r))
	if out.ClearBinding {
		http.SetCookie(c.w, s.auth.ClearOIDCCookie())
	}
	if out.Session != "" {
		s.startSession(c, out.Session)
	}
	http.Redirect(c.w, c.r, out.Redirect, http.StatusSeeOther)
}

// --- account -----------------------------------------------------------------

func (s *server) getAccount(c *call) {
	v, err := s.auth.Account(c.r.Context(), c.user())
	if err != nil {
		s.fail(c.w, err)
		return
	}
	type ident struct {
		ID       string `json:"id"`
		Provider string `json:"provider"`
		Label    string `json:"label"`
		Created  string `json:"created"`
	}
	ids := make([]ident, len(v.Identities))
	for i, x := range v.Identities {
		ids[i] = ident{x.ID, x.Provider, x.Label, rfc3339(x.Created)}
	}
	writeJSON(c.w, http.StatusOK, struct {
		User        userResp `json:"user"`
		HasPassword bool     `json:"hasPassword"`
		Identities  []ident  `json:"identities"`
	}{toUser(v.User), v.HasPassword, ids})
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
	}{toUser(*u)})
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
	us, err := s.auth.Users(c.r.Context())
	if err != nil {
		s.fail(c.w, err)
		return
	}
	out := make([]userResp, len(us))
	for i, u := range us {
		out[i] = toUser(u)
	}
	writeJSON(c.w, http.StatusOK, struct {
		Users []userResp `json:"users"`
	}{out})
}

func (s *server) patchUser(c *call) {
	var req struct {
		Role     *string `json:"role"`
		Disabled *bool   `json:"disabled"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	u, err := s.auth.UpdateUser(c.r.Context(), c.user().ID, c.r.PathValue("id"), auth.UserPatch{Role: req.Role, Disabled: req.Disabled})
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		User userResp `json:"user"`
	}{toUser(*u)})
}

func (s *server) createInvite(c *call) {
	var req struct {
		Role string `json:"role"`
		Days *int   `json:"days"`
	}
	if !s.readJSONMax(c.w, c.r, &req, true, authBodyLimit) {
		return
	}
	inv, err := s.auth.CreateInvite(c.r.Context(), c.user().ID, req.Role, req.Days)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusCreated, struct {
		ID      string `json:"id"`
		Token   string `json:"token"`
		Role    string `json:"role"`
		Created string `json:"created"`
		Expires string `json:"expires"`
	}{inv.ID, inv.Token, inv.Role, rfc3339(inv.Created), rfc3339(inv.Expires)})
}

func (s *server) listInvites(c *call) {
	l, err := s.auth.OpenInvites(c.r.Context())
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
	}
	out := make([]inv, len(l))
	for i, x := range l {
		out[i] = inv{x.ID, x.Role, rfc3339(x.Created), rfc3339(x.Expires), x.CreatedBy}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Invites []inv `json:"invites"`
	}{out})
}

func (s *server) deleteInvite(c *call) {
	if err := s.auth.DeleteInvite(c.r.Context(), c.r.PathValue("id")); err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
}

// --- spaces ------------------------------------------------------------------

func (s *server) listSpaces(c *call) {
	l, err := s.spaces.List(c.r.Context(), c.user().ID)
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
	in, err := s.spaces.Update(c.r.Context(), c.user().ID, c.sp.Info.ID, req.Name, req.Inbox)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, toSpaces([]spaces.Info{in})[0])
}
