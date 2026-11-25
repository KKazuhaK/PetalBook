package api

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/auth"
	"github.com/KKazuhaK/PetalBook/internal/oauth"
	"github.com/KKazuhaK/PetalBook/internal/store"
)

// oauthPaths are the paths of the authorization server that are not under /api
// and are not a page of the web app. HandlesPath says so to the server that
// hands the rest to the app.
func oauthPath(p string) bool {
	switch p {
	case oauth.PathMetadata, oauth.PathResource, oauth.PathResourceMCP, oauth.PathAuthorize, oauth.PathToken, oauth.PathRegister:
		return true
	}
	return false
}

// HandlesPath says whether a request path is the API's: the API itself and the
// endpoints of the authorization server, as opposed to the pages of the web app
// (the consent page, /oauth/consent, among them).
func HandlesPath(p string) bool {
	return strings.HasPrefix(p, "/api/") || p == mcpPath || oauthPath(p)
}

// oauthNoCORS are the paths that a page of another site may call, or send the
// browser to: the cross-site rules of the API do not apply to them. They carry
// no cookie and no ambient credential (the authorization request only asks the
// person, who is signed in here, to approve it on a page of this server).
func oauthCrossSite(r *http.Request) bool {
	switch r.URL.Path {
	case oauth.PathMetadata, oauth.PathResource, oauth.PathResourceMCP, oauth.PathToken, oauth.PathRegister:
		return true
	case oauth.PathAuthorize:
		return r.Method == http.MethodGet
	}
	return false
}

// oauthRefuse answers a refusal of the checks in front of the handlers in the
// OAuth error shape when the request is for /oauth/*, as docs/api.md promises
// there, and reports whether it did.
func oauthRefuse(w http.ResponseWriter, r *http.Request, status int, code, desc string) bool {
	if !strings.HasPrefix(r.URL.Path, "/oauth/") {
		return false
	}
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
	return true
}

// oauthForm is the one POST of the API whose body is a form.
func oauthForm(r *http.Request) bool { return r.URL.Path == oauth.PathToken }

// mountOAuth registers the endpoints of the authorization server on the mux.
func (s *server) mountOAuth(mux *http.ServeMux) {
	if s.oauth == nil {
		return
	}
	for p, h := range s.oauth.Public() {
		mux.Handle(p, h)
	}
	mux.Handle("GET "+oauth.PathAuthorize, s.oauth.Gate(http.HandlerFunc(s.oauthAuthorize)))
}

// oauthAuthorize is GET /oauth/authorize. The person must have a session; if
// they have none the sign-in page sends them back here.
func (s *server) oauthAuthorize(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	// The browser of a person: the session cookie decides, never a token.
	p, err := s.principal(r)
	if err != nil {
		var ae *auth.Error
		if errors.As(err, &ae) {
			back := r.URL.RequestURI()
			http.Redirect(w, r, "/login?return="+url.QueryEscape(back), http.StatusFound)
			return
		}
		s.fail(w, err)
		return
	}
	dest, problem := s.oauth.Begin(r, p.User.ID)
	if problem != nil {
		s.oauth.WriteProblem(w, r, problem)
		return
	}
	http.Redirect(w, r, dest, http.StatusFound)
}

// oauthThere answers 404 unless there is an authorization server. The grants
// can be listed and revoked while an administrator has MCP switched off: they
// are kept, and a person may want one gone.
func (s *server) oauthThere(c *call) bool {
	if s.oauth != nil {
		return true
	}
	writeError(c.w, http.StatusNotFound, "not_found", "No such endpoint.")
	return false
}

// oauthOn answers 404 unless the authorization server is there and switched on.
func (s *server) oauthOn(c *call) bool {
	if s.oauth != nil {
		on, err := s.oauth.On(c.r.Context())
		if err != nil {
			s.fail(c.w, err)
			return false
		}
		if on {
			return true
		}
	}
	writeError(c.w, http.StatusNotFound, "not_found", "No such endpoint.")
	return false
}

// oauthPrincipal is who a request with an OAuth access token acts for: the
// owner of the grant, narrowed to its spaces and access. It is the way /mcp
// takes such a token; the routes of /api refuse it (see handle).
func (s *server) oauthPrincipal(r *http.Request, raw string) (*auth.Principal, error) {
	ip := auth.ClientAddr(r, nil)
	if s.auth != nil {
		ip = s.auth.ClientAddr(r)
	}
	return s.oauth.Authenticate(r.Context(), raw, ip)
}

// --- consent -------------------------------------------------------------------

type consentSpace struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
	Personal    bool     `json:"personal"`
}

func (s *server) oauthConsentInfo(c *call) {
	if !s.oauthOn(c) {
		return
	}
	u := c.user()
	info, err := s.oauth.Consent(u.ID, c.r.URL.Query().Get("req"))
	if errors.Is(err, oauth.ErrNotFound) {
		writeError(c.w, http.StatusNotFound, "not_found", "This authorization request has expired, was decided already or is not yours; start again from the client.")
		return
	}
	if err != nil {
		s.fail(c.w, err)
		return
	}
	l, err := s.spaces.List(c.r.Context(), u.ID)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	sp := make([]consentSpace, 0, len(l))
	for _, in := range l {
		if in.Disabled {
			continue
		}
		sp = append(sp, consentSpace{in.ID, in.Name, in.Perms.Names(), in.Personal})
	}
	type client struct {
		Name string `json:"name"`
		ID   string `json:"id"`
		Host string `json:"host"`
	}
	type user struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	writeJSON(c.w, http.StatusOK, struct {
		Client       client         `json:"client"`
		RedirectHost string         `json:"redirectHost"`
		Punycode     bool           `json:"punycode"`
		Scopes       []string       `json:"scopes"`
		Spaces       []consentSpace `json:"spaces"`
		User         user           `json:"user"`
	}{client{info.ClientName, info.ClientID, info.ClientHost}, info.RedirectHost, info.Punycode, info.Scopes, sp, user{u.ID, u.Name}})
}

func (s *server) oauthConsentDecide(c *call) {
	if !s.oauthOn(c) {
		return
	}
	var req struct {
		Req     string   `json:"req"`
		Approve bool     `json:"approve"`
		Spaces  []string `json:"spaces"`
		Access  string   `json:"access"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	u := c.user()
	d := oauth.Decision{Req: req.Req, Approve: req.Approve, Spaces: req.Spaces, Access: req.Access}
	if req.Approve {
		// Only spaces the person is a member of now may be named.
		l, err := s.spaces.List(c.r.Context(), u.ID)
		if err != nil {
			s.fail(c.w, err)
			return
		}
		member := make(map[string]bool, len(l))
		for _, in := range l {
			member[in.ID] = !in.Disabled
		}
		for _, id := range req.Spaces {
			if !member[id] {
				// A request that is gone is 404 whatever else is wrong with the answer.
				if _, err := s.oauth.Consent(u.ID, req.Req); err != nil {
					break
				}
				writeError(c.w, http.StatusBadRequest, "invalid", "You are not a member of the space "+clipID(id)+".")
				return
			}
		}
	}
	dest, err := s.oauth.Decide(u.ID, d)
	var inv *oauth.Invalid
	switch {
	case errors.Is(err, oauth.ErrNotFound):
		writeError(c.w, http.StatusNotFound, "not_found", "This authorization request has expired, was decided already or is not yours; start again from the client.")
	case errors.As(err, &inv):
		writeError(c.w, http.StatusBadRequest, "invalid", inv.Msg)
	case err != nil:
		s.fail(c.w, err)
	default:
		writeJSON(c.w, http.StatusOK, struct {
			Redirect string `json:"redirect"`
		}{dest})
	}
}

// --- grants --------------------------------------------------------------------

type grantClient struct {
	Name string `json:"name"`
	ID   string `json:"id"`
}

type grantResp struct {
	ID       string           `json:"id"`
	Client   grantClient      `json:"client"`
	Access   string           `json:"access"`
	Spaces   []tokenSpaceResp `json:"spaces"`
	Created  string           `json:"created"`
	LastUsed *string          `json:"lastUsed"`
}

type adminGrantResp struct {
	grantResp
	Owner struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Name     string `json:"name"`
	} `json:"owner"`
}

func toGrant(g store.OAuthGrant) grantResp {
	sp := make([]tokenSpaceResp, len(g.Spaces))
	for i, x := range g.Spaces {
		sp[i] = tokenSpaceResp{x.ID, x.Name}
	}
	return grantResp{ID: g.ID, Client: grantClient{g.ClientName, g.ClientID}, Access: g.Access, Spaces: sp, Created: rfc3339(g.Created), LastUsed: timeOrNil(g.LastUsed)}
}

// liveGrants drops the grants that went idle past their expiry while the sweep
// was not looking.
func (s *server) liveGrants(rows []store.OAuthGrantRow) []store.OAuthGrantRow {
	now := s.tokens.Now()
	out := rows[:0:0]
	for _, r := range rows {
		if now.Sub(r.LastUsed) <= oauth.IdleExpiry {
			out = append(out, r)
		}
	}
	return out
}

func (s *server) listMyGrants(c *call) {
	if !s.oauthThere(c) {
		return
	}
	rows, err := s.oauth.List(c.r.Context(), c.ownScope(), store.GrantFilter{UserID: c.user().ID, Limit: 500})
	if err != nil {
		s.fail(c.w, err)
		return
	}
	rows = s.liveGrants(rows)
	out := make([]grantResp, len(rows))
	for i, r := range rows {
		out[i] = toGrant(r.OAuthGrant)
	}
	writeJSON(c.w, http.StatusOK, struct {
		Grants []grantResp `json:"grants"`
	}{out})
}

func (s *server) revokeMyGrant(c *call) { s.revokeGrant(c, c.user().ID) }

// revokeGrant revokes a grant; owner "" is any (an administrator).
func (s *server) revokeGrant(c *call, owner string) {
	if !s.oauthThere(c) {
		return
	}
	id := c.r.PathValue("id")
	rows, err := s.oauth.List(c.r.Context(), store.ScopeOf(c.user()), store.GrantFilter{ID: id, Limit: 1})
	if err != nil {
		s.fail(c.w, err)
		return
	}
	if len(rows) == 0 || (owner != "" && rows[0].UserID != owner) {
		writeError(c.w, http.StatusNotFound, "not_found", "No such grant.")
		return
	}
	if owner == "" && s.refuseSiteAdminOwner(c, rows[0].UserID) {
		return
	}
	who := "the owner"
	if owner == "" {
		who = "an administrator"
	}
	done, err := s.oauth.Revoke(c.r.Context(), id, "revoked by "+who)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	if done {
		s.audit(c, auth.AuditEvent{Kind: "grant_revoked", Actor: c.user().ID, User: rows[0].UserID,
			Detail: map[string]any{"grant": id, "client": rows[0].ClientName, "by": revokedBy(owner)}})
	}
	c.w.WriteHeader(http.StatusNoContent)
}

func (s *server) adminListGrants(c *call) {
	if !s.oauthThere(c) {
		return
	}
	sc, ok := s.adminScope(c)
	if !ok {
		return
	}
	q := c.r.URL.Query()
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			writeError(c.w, http.StatusBadRequest, "invalid", "Limit must be from 1 to 200.")
			return
		}
		limit = n
	}
	f := store.GrantFilter{UserID: q.Get("user"), Limit: limit + 1}
	if cur := q.Get("cursor"); cur != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cur)
		msStr, id, ok := strings.Cut(string(raw), ":")
		n, perr := strconv.ParseInt(msStr, 10, 64)
		if err != nil || !ok || perr != nil || id == "" {
			writeError(c.w, http.StatusBadRequest, "invalid", "The cursor is not one this server gave.")
			return
		}
		f.AfterCreated, f.AfterID = time.UnixMilli(n).UTC(), id
	}
	rows, err := s.oauth.List(c.r.Context(), sc, f)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1]
		next = base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(last.Created.UnixMilli(), 10) + ":" + last.ID))
	}
	rows = s.liveGrants(rows)
	items := make([]adminGrantResp, len(rows))
	for i, r := range rows {
		items[i].grantResp = toGrant(r.OAuthGrant)
		items[i].Owner.ID, items[i].Owner.Username, items[i].Owner.Name = r.UserID, r.Username, r.OwnerName
	}
	writeJSON(c.w, http.StatusOK, struct {
		Items []adminGrantResp `json:"items"`
		Next  string           `json:"next"`
	}{items, next})
}

func (s *server) adminRevokeGrant(c *call) { s.revokeGrant(c, "") }
