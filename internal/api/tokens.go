package api

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KKazuhaK/PetalBook/internal/auth"
	"github.com/KKazuhaK/PetalBook/internal/perm"
	"github.com/KKazuhaK/PetalBook/internal/spaces"
	"github.com/KKazuhaK/PetalBook/internal/store"
	"github.com/KKazuhaK/PetalBook/internal/tokens"
)

// NewTokens makes the service of API tokens for a server. a is nil in
// single-user mode, where the owner of every token is the built-in user.
// cfg.Store is required; the owner lookup is filled in here.
func NewTokens(cfg tokens.Config, a *auth.Service) *tokens.Service {
	if a != nil {
		cfg.Users = a
	} else {
		cfg.Users = &localUser{st: cfg.Store}
	}
	return tokens.New(cfg)
}

// localUser is the owner of every token in single-user mode. The built-in user
// cannot change, so it is read once.
type localUser struct {
	st *store.Store
	mu sync.Mutex
	u  *store.User
}

func (l *localUser) CachedUser(ctx context.Context, id string) (*store.User, error) {
	if id != store.LocalUserID {
		return nil, store.ErrNotFound
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.u == nil {
		u, err := store.GetUser(ctx, l.st.Q(), store.AllTenants(), id)
		if err != nil {
			return nil, err
		}
		l.u = u
	}
	u := *l.u
	return &u, nil
}

// bearerToken is the secret of an "Authorization: Bearer" header, and whether
// the request has one. A header of another scheme is not a token.
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	scheme, rest, _ := strings.Cut(strings.TrimSpace(h), " ")
	if !strings.EqualFold(scheme, "bearer") {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// tokenRoutes are the routes outside the spaces that a token may use.
var tokenRoutes = map[string]bool{
	"GET /api/meta":    true,
	"GET /api/session": true,
	"GET /api/spaces":  true,
	// The heads of the offline sync: the spaces a token names, read only.
	"GET /api/sync/heads": true,
}

// tokenAllowed says whether a route may be used with a token: those on a
// space that ask for read or write, and the few above. Everything else, a
// route that needs members or manage included, is for a person.
func tokenAllowed(rt route) bool {
	if rt.access.kind == kindSpace {
		// The drafts are a person's, kept in the browser. What a token writes
		// goes straight to a commit, with its own via.
		if strings.HasSuffix(rt.pattern, "/draft") || strings.HasSuffix(rt.pattern, "/commit") {
			return false
		}
		return !rt.access.orSelf && (rt.access.need == perm.Read || rt.access.need == perm.Write)
	}
	return tokenRoutes[rt.method+" "+rt.pattern]
}

// tokenChanges says whether a request changes anything; a token without write
// is refused those, whatever permission the route asks for.
func tokenChanges(r *http.Request) bool {
	return r.Method != http.MethodGet && r.Method != http.MethodHead
}

// tokenPrincipal is who a request with a bearer token acts for.
func (s *server) tokenPrincipal(r *http.Request, secret string) (*auth.Principal, error) {
	ip := auth.ClientAddr(r, nil)
	if s.auth != nil {
		ip = s.auth.ClientAddr(r)
	}
	p, err := s.tokens.Authenticate(r.Context(), secret, ip)
	if err != nil {
		return nil, err
	}
	if s.single {
		// The built-in user as the server has it, not a copy read at another time.
		p.User = s.local
	}
	return p, nil
}

// failToken answers a refusal of the token itself.
func (s *server) failToken(w http.ResponseWriter, e *tokens.Error) {
	switch {
	case e.Status == http.StatusUnauthorized:
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
	case e.Status == http.StatusTooManyRequests:
		secs := int((e.RetryAfter + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", strconv.Itoa(max(secs, 1)))
	}
	writeError(w, e.Status, e.Code, e.Msg)
}

// visibleSpaces are the spaces the caller sees: their own list, or for a token
// the spaces it names, with the permissions it has in each.
func (s *server) visibleSpaces(c *call) ([]spaces.Info, error) {
	l, err := s.spaces.List(c.r.Context(), c.user().ID)
	if err != nil {
		return nil, err
	}
	g := c.p.Grant
	if g == nil {
		return l, nil
	}
	out := make([]spaces.Info, 0, len(g.Spaces))
	for _, in := range l {
		if g.Names(in.ID) {
			in.Perms &= g.Perms()
			out = append(out, in)
		}
	}
	return out, nil
}

// --- the token as the API shows it ------------------------------------------

type tokenSpaceResp struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type tokenResp struct {
	ID       string           `json:"id"`
	Name     string           `json:"name"`
	Prefix   string           `json:"prefix"`
	Access   string           `json:"access"`
	Spaces   []tokenSpaceResp `json:"spaces"`
	Created  string           `json:"created"`
	Expires  *string          `json:"expires"`
	LastUsed *string          `json:"lastUsed"`
	Revoked  *string          `json:"revoked"`
	State    string           `json:"state"`
}

type adminTokenResp struct {
	tokenResp
	Owner struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Name     string `json:"name"`
	} `json:"owner"`
}

func timeOrNil(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	v := rfc3339(t)
	return &v
}

func toToken(t store.APIToken, now time.Time) tokenResp {
	state := "active"
	switch {
	case !t.Revoked.IsZero():
		state = "revoked"
	case !t.Expires.IsZero() && !now.Before(t.Expires):
		state = "expired"
	}
	sp := make([]tokenSpaceResp, len(t.Spaces))
	for i, x := range t.Spaces {
		sp[i] = tokenSpaceResp{x.ID, x.Name}
	}
	return tokenResp{ID: t.ID, Name: t.Name, Prefix: t.Prefix, Access: t.Access, Spaces: sp, Created: rfc3339(t.Created),
		Expires: timeOrNil(t.Expires), LastUsed: timeOrNil(t.LastUsed), Revoked: timeOrNil(t.Revoked), State: state}
}

// --- a person's own tokens ---------------------------------------------------

func (s *server) listMyTokens(c *call) {
	rows, err := s.tokens.List(c.r.Context(), c.ownScope(), store.TokenFilter{UserID: c.user().ID, Limit: 500})
	if err != nil {
		s.fail(c.w, err)
		return
	}
	now := s.tokens.Now()
	out := make([]tokenResp, len(rows))
	for i, r := range rows {
		out[i] = toToken(r.APIToken, now)
	}
	writeJSON(c.w, http.StatusOK, struct {
		Tokens []tokenResp `json:"tokens"`
	}{out})
}

func (s *server) createToken(c *call) {
	var req struct {
		Name          string   `json:"name"`
		Access        string   `json:"access"`
		Spaces        []string `json:"spaces"`
		ExpiresInDays *int     `json:"expiresInDays"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	days := tokens.DefaultDays
	if req.ExpiresInDays != nil {
		days = *req.ExpiresInDays
	}
	if days < 0 || days > tokens.MaxDays {
		writeError(c.w, http.StatusBadRequest, "invalid", "expiresInDays must be from 1 to 365, or 0 for a token that never expires")
		return
	}
	// Every space must be one the caller belongs to now.
	l, err := s.spaces.List(c.r.Context(), c.user().ID)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	member := make(map[string]bool, len(l))
	for _, in := range l {
		member[in.ID] = true
	}
	// The shape of the request is judged first, so a bad one is 400 whoever
	// the spaces are.
	if _, err := tokens.CleanName(req.Name); err != nil {
		s.failTokenRequest(c.w, err)
		return
	}
	if req.Access != "" && req.Access != tokens.Read && req.Access != tokens.Write {
		writeError(c.w, http.StatusBadRequest, "invalid", "access must be read or write")
		return
	}
	if len(req.Spaces) < 1 || len(req.Spaces) > tokens.MaxSpaces {
		writeError(c.w, http.StatusBadRequest, "invalid", "name 1 to "+strconv.Itoa(tokens.MaxSpaces)+" spaces")
		return
	}
	for _, id := range req.Spaces {
		if !member[id] {
			writeError(c.w, http.StatusNotFound, "not_found", "no such space: "+clipID(id))
			return
		}
	}
	t, secret, err := s.tokens.Create(c.r.Context(), tokens.CreateParams{UserID: c.user().ID, Name: req.Name, Access: req.Access,
		Spaces: req.Spaces, ExpiresIn: time.Duration(days) * 24 * time.Hour})
	if err != nil {
		s.failTokenRequest(c.w, err)
		return
	}
	for i := range t.Spaces {
		for _, in := range l {
			if in.ID == t.Spaces[i].ID {
				t.Spaces[i].Name = in.Name
			}
		}
	}
	ids := make([]string, len(t.Spaces))
	for i, sp := range t.Spaces {
		ids[i] = sp.ID
	}
	s.audit(c, auth.AuditEvent{Kind: "token_created", Actor: c.user().ID, User: c.user().ID,
		Detail: map[string]any{"token": t.ID, "name": t.Name, "access": t.Access, "spaces": ids}})
	c.w.Header().Set("Location", "/api/account/tokens")
	writeJSON(c.w, http.StatusCreated, struct {
		Token  tokenResp `json:"token"`
		Secret string    `json:"secret"`
	}{toToken(t, s.tokens.Now()), secret})
}

func clipID(id string) string {
	if len(id) > 40 {
		return id[:40] + "…"
	}
	return id
}

func (s *server) failTokenRequest(w http.ResponseWriter, err error) {
	var inv *tokens.Invalid
	switch {
	case errors.As(err, &inv):
		writeError(w, http.StatusBadRequest, "invalid", inv.Msg)
	case errors.Is(err, tokens.ErrLimit):
		writeError(w, http.StatusBadRequest, "token_limit", "you have "+strconv.Itoa(tokens.MaxLive)+" tokens in use; revoke one first")
	default:
		s.fail(w, err)
	}
}

func (s *server) revokeMyToken(c *call) {
	s.revokeToken(c, c.user().ID)
}

// revokeToken revokes a token; owner "" is any (an administrator).
func (s *server) revokeToken(c *call, owner string) {
	id := c.r.PathValue("id")
	rows, err := s.tokens.List(c.r.Context(), store.ScopeOf(c.user()), store.TokenFilter{ID: id, Limit: 1})
	if err != nil {
		s.fail(c.w, err)
		return
	}
	if len(rows) == 0 || (owner != "" && rows[0].UserID != owner) {
		writeError(c.w, http.StatusNotFound, "not_found", "no such token")
		return
	}
	if owner == "" && s.refuseSiteAdminOwner(c, rows[0].UserID) {
		return
	}
	done, err := s.tokens.Revoke(c.r.Context(), id)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	if done && rows[0].Revoked.IsZero() { // one row per revocation, not per request
		s.audit(c, auth.AuditEvent{Kind: "token_revoked", Actor: c.user().ID, User: rows[0].UserID,
			Detail: map[string]any{"token": id, "name": rows[0].Name, "by": revokedBy(owner)}})
	}
	c.w.WriteHeader(http.StatusNoContent)
}

// refuseSiteAdminOwner answers 403 and returns true when the person is a site
// administrator and the caller is not one: a tenant administrator may not end
// what a site administrator holds (as with changing the account itself).
func (s *server) refuseSiteAdminOwner(c *call, userID string) bool {
	if cu := c.user(); cu.SiteAdmin && cu.Role == "admin" {
		return false
	}
	u, err := store.GetUser(c.r.Context(), s.opt.Store.Q(), store.AllTenants(), userID)
	if errors.Is(err, store.ErrNotFound) {
		return false
	}
	if err != nil {
		s.fail(c.w, err)
		return true
	}
	if u.SiteAdmin {
		writeError(c.w, http.StatusForbidden, "forbidden", "only a site administrator may change a site administrator")
		return true
	}
	return false
}

// revokedBy is the "by" of a revocation's audit row: owner "" is an administrator.
func revokedBy(owner string) string {
	if owner == "" {
		return "admin"
	}
	return "user"
}

// audit records an event in the audit log of the sign-in service, when there
// is one (not with the built-in user), with the caller's address.
func (s *server) audit(c *call, ev auth.AuditEvent) {
	if s.auth == nil {
		return
	}
	ev.IP = s.auth.ClientAddr(c.r)
	s.auth.Audit(ev)
}

// --- administration -----------------------------------------------------------

type accessResp struct {
	Tokens bool `json:"tokens"`
	MCP    bool `json:"mcp"`
	// OAuth is whether the authorization server is there: it needs MCP on and a
	// public URL.
	OAuth bool `json:"oauth"`
}

func (s *server) accessResp(a tokens.Access) accessResp {
	return accessResp{Tokens: a.Tokens, MCP: a.MCP, OAuth: a.MCP && s.oauth != nil}
}

func (s *server) adminGetAccess(c *call) {
	a, err := s.tokens.Access(c.r.Context())
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, s.accessResp(a))
}

func (s *server) adminPutAccess(c *call) {
	var req struct {
		Tokens *bool `json:"tokens"`
		MCP    *bool `json:"mcp"`
	}
	if !s.readJSONMax(c.w, c.r, &req, false, authBodyLimit) {
		return
	}
	a, err := s.tokens.SetAccess(c.r.Context(), req.Tokens, req.MCP)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, s.accessResp(a))
}

func (s *server) adminListTokens(c *call) {
	sc, ok := s.adminScope(c)
	if !ok {
		return
	}
	q := c.r.URL.Query()
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			writeError(c.w, http.StatusBadRequest, "invalid", "limit must be from 1 to 200")
			return
		}
		limit = n
	}
	f := store.TokenFilter{UserID: q.Get("user"), Limit: limit + 1}
	if cur := q.Get("cursor"); cur != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cur)
		msStr, id, ok := strings.Cut(string(raw), ":")
		n, perr := strconv.ParseInt(msStr, 10, 64)
		if err != nil || !ok || perr != nil || id == "" {
			writeError(c.w, http.StatusBadRequest, "invalid", "the cursor is not one this server gave")
			return
		}
		f.AfterCreated, f.AfterID = time.UnixMilli(n).UTC(), id
	}
	rows, err := s.tokens.List(c.r.Context(), sc, f)
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
	now := s.tokens.Now()
	items := make([]adminTokenResp, len(rows))
	for i, r := range rows {
		items[i].tokenResp = toToken(r.APIToken, now)
		items[i].Owner.ID, items[i].Owner.Username, items[i].Owner.Name = r.UserID, r.Username, r.OwnerName
	}
	writeJSON(c.w, http.StatusOK, struct {
		Items []adminTokenResp `json:"items"`
		Next  string           `json:"next"`
	}{items, next})
}

func (s *server) adminRevokeToken(c *call) { s.revokeToken(c, "") }
