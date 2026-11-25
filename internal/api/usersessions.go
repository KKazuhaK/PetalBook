package api

import (
	"errors"
	"net/http"

	"github.com/KKazuhaK/PetalBook/internal/store"
)

// The person's page of the administration (milestone 3.10): their sessions and
// connected accounts, which only an administrator of the whole server sees, and
// the places they belong to. A tenant's administrator is answered as for a
// person who does not exist, so nothing tells them the page is there.

// siteOnly answers "no such user" to anyone but an administrator of the server.
func (s *server) siteOnly(c *call) bool {
	if c.isSiteAdmin() {
		return true
	}
	writeError(c.w, http.StatusNotFound, "not_found", "No such user.")
	return false
}

type sessionResp struct {
	ID        string `json:"id"`
	UserAgent string `json:"userAgent"`
	// Method is how they signed in: "password", "passkey", "sso:<provider>", "invite", "registration" or "setup" ("" when not known).
	Method   string `json:"method"`
	Created  string `json:"created"`
	LastUsed string `json:"lastUsed"`
	Held     bool   `json:"held,omitempty"`
}

func (s *server) userSessions(c *call) {
	if !s.siteOnly(c) {
		return
	}
	rows, err := s.auth.UserSessions(c.r.Context(), c.p, c.r.PathValue("id"))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	items := make([]sessionResp, len(rows))
	for i, r := range rows {
		items[i] = sessionResp{r.ID, r.UserAgent, r.Method, rfc3339(r.Created), rfc3339(r.LastUsed), r.Held}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Sessions []sessionResp `json:"sessions"`
	}{items})
}

func (s *server) endUserSession(c *call) {
	if !s.siteOnly(c) {
		return
	}
	if err := s.auth.EndUserSession(c.r.Context(), c.p, c.r.PathValue("id"), c.r.PathValue("session"), s.auth.ClientAddr(c.r)); err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
}

func (s *server) endUserSessions(c *call) {
	if !s.siteOnly(c) {
		return
	}
	n, err := s.auth.EndUserSessions(c.r.Context(), c.p, c.r.PathValue("id"), s.auth.ClientAddr(c.r))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	writeJSON(c.w, http.StatusOK, struct {
		Ended int `json:"ended"`
	}{n})
}

type identityResp struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Slug     string `json:"slug"`
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Created  string `json:"created"`
}

func (s *server) userIdentities(c *call) {
	if !s.siteOnly(c) {
		return
	}
	rows, err := s.auth.UserIdentities(c.r.Context(), c.p, c.r.PathValue("id"))
	if err != nil {
		s.fail(c.w, err)
		return
	}
	items := make([]identityResp, len(rows))
	for i, r := range rows {
		items[i] = identityResp{r.ID, r.Provider, r.Slug, r.Kind, r.Label, rfc3339(r.Created)}
	}
	writeJSON(c.w, http.StatusOK, struct {
		Identities []identityResp `json:"identities"`
	}{items})
}

func (s *server) disconnectUserIdentity(c *call) {
	if !s.siteOnly(c) {
		return
	}
	if err := s.auth.DisconnectIdentity(c.r.Context(), c.p, c.r.PathValue("id"), c.r.PathValue("identity"), s.auth.ClientAddr(c.r)); err != nil {
		s.fail(c.w, err)
		return
	}
	c.w.WriteHeader(http.StatusNoContent)
}

// maxMemberships is the most spaces (and groups) the person's page lists.
const maxMemberships = 200

// userMemberships is the spaces a person is a member of and the groups they are in. A
// tenant administrator's answer leaves the person's personal space out.
func (s *server) userMemberships(c *call) {
	ctx := c.r.Context()
	id := c.r.PathValue("id")
	if _, err := store.GetUser(ctx, s.opt.Store.Q(), store.AdminScopeOf(c.user()), id); errors.Is(err, store.ErrNotFound) {
		writeError(c.w, http.StatusNotFound, "not_found", "No such user.")
		return
	} else if err != nil {
		s.fail(c.w, err)
		return
	}
	spaces, err := store.ListUserSpaces(ctx, s.opt.Store.Q(), id)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	groups, err := store.UserGroups(ctx, s.opt.Store.Q(), id)
	if err != nil {
		s.fail(c.w, err)
		return
	}
	type spaceItem struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Role     string `json:"role"`
		Personal bool   `json:"personal"`
	}
	type groupItem struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Source string `json:"source"`
	}
	out := struct {
		Spaces []spaceItem `json:"spaces"`
		Groups []groupItem `json:"groups"`
	}{[]spaceItem{}, []groupItem{}}
	for _, sp := range spaces {
		if sp.Personal && !c.isSiteAdmin() {
			continue
		}
		if len(out.Spaces) < maxMemberships {
			out.Spaces = append(out.Spaces, spaceItem{sp.ID, sp.Name, sp.Role, sp.Personal})
		}
	}
	for _, g := range groups {
		if len(out.Groups) < maxMemberships {
			out.Groups = append(out.Groups, groupItem{g.ID, g.Name, g.Source})
		}
	}
	writeJSON(c.w, http.StatusOK, out)
}
